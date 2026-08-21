package statusline

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// The K2 occupancy reader (issue #596). It turns the agent-writable snapshot directory the
// statusline renderer owns into a per-agent map of channel readings that a factory-side evaluator
// can act on.
//
// Two honesty invariants (the web/internal/readmodel device):
//   - A channel with NO trustworthy datum never reads healthy. Absent, malformed, oversized,
//     forged, unattributable, stale and dark all resolve to a non-fresh state, and none of them
//     exposes an occupancy percentage. A wedged agent reporting "0%" is strictly worse than one
//     reporting nothing, because the trigger would read it as maximally healthy (AC-5).
//   - Classification is a pure function of the INJECTED clock and the INJECTED thresholds. This
//     package reads no environment and loads no config (ADR-004); the roster and the staleness
//     knobs arrive as parameters from the cmd layer, which already holds them.
//
// This directory is a trust boundary, not a private store: the renderer runs with each agent's own
// filesystem authority, so any agent can write any file here. Validation IS the boundary
// (design-doc.md:179) — filename grammar, size cap, presence-checked decode, roster membership,
// clamped numerics, and a bounded forward-skew tolerance on written_at. HMAC signing was rejected
// upstream because no enforceable key boundary exists. A forged high reading costs at most one
// bounded recycle and leaves durable evidence; a SUPPRESSED channel becomes dark — visible, never
// healthy. Note that "clamp a future written_at to now", which the design states as the remedy for
// clock skew, is NOT sufficient on its own: the clamp re-applies on every read, so an unbounded
// forward stamp would read fresh indefinitely. ObservedReading therefore refuses beyond the
// tolerance rather than clamping without limit.

const (
	// maxObservationBytes bounds a single snapshot read by the READER, so a hostile or corrupt
	// file cannot force an unbounded read during a watchdog sweep. It is deliberately a separate
	// number from daily.go's maxSnapshotBytes: that one bounds the writer's own read-back of a
	// file it just wrote, while this one guards a boundary over files any agent may have
	// authored. Collapsing them would silently couple a hostile-input limit to an internal
	// bookkeeping limit (the internal/cmd/telemetry_usage.go:56-60 precedent).
	maxObservationBytes = 64 * 1024

	// minSchemaVersion is the oldest snapshot version carrying occupancy. #595's v1 files have no
	// schema key at all and decode to 0, so they fall below the floor and read as having no datum
	// — the safe direction (Gotcha 12). Newer versions are accepted because evolution is additive
	// and this reader validates the PRESENCE of the fields it needs rather than rejecting unknown
	// ones; a v3 that still carries the v2 fields stays readable.
	//
	// Deliberately the LITERAL 2, not schemaVersion. Aliasing the reader's floor to the writer's
	// current version would silently raise the floor on the next bump, and every v2 file already
	// on disk would read malformed for every agent at once — a factory-wide dark channel caused by
	// a version number. The floor moves only when occupancy's on-disk shape actually breaks.
	minSchemaVersion = 2
)

// ChannelState is one agent's occupancy-channel condition. The literal values are part of the
// on-disk/on-wire contract (they reach `af agents list --json`), so they are asserted separately
// from the identifier names — renaming a constant must never move the bytes that reach a caller
// (the internal/telemetry/event.go:8-10 rule).
type ChannelState string

const (
	// StateFresh is the ONLY healthy state: a validated datum, no older than Staleness.
	StateFresh ChannelState = "fresh"
	// StateStale is a validated datum older than Staleness but within DarkAfter — the channel is
	// lagging its expected refresh cadence but has not gone silent.
	StateStale ChannelState = "stale"
	// StateDark is a validated datum older than DarkAfter: the channel has stopped reporting.
	// The datum is still exposed, because "dark at high occupancy" is a different decision from
	// "dark at low occupancy".
	StateDark ChannelState = "dark"
	// StateNone means no snapshot is attributable to this agent at all — it has never rendered,
	// the factory statusline gate is off, or its files predate the agent field.
	StateNone ChannelState = "none"
	// StateMalformed means a file WAS attributable to this agent but carried no usable datum.
	// It is also what an unconstructed reading reports, so a zero value can never pass for health.
	StateMalformed ChannelState = "malformed"
)

// IsHealthy is the single gate every consumer must use. Comparing against a sentinel instead
// (state != StateDark) silently admits none and malformed as health — the defect class that
// shipped once already in mail/translate.go (commit 045c1e1, INV-5).
func (s ChannelState) IsHealthy() bool { return s == StateFresh }

// Observation is one validated occupancy datum. Every field is unexported and there is no exported
// constructor, so outside this package the ONLY source of an Observation is a decode that already
// passed every check in readObservationFile. A ChannelReading carrying one is therefore evidence
// that a datum existed — which is the property the whole recovery trigger rests on.
//
// The zero value is inert by construction: valid is false, so ObservedReading refuses to classify
// it. Per ADR-005 that is a runtime precondition plus construction discipline, NOT a type-level
// interlock — Go has no sum types, so a compile-time-impossibility claim would be false. The
// mechanical backstop is the API scan in TestObservation_NoHealthyWithoutDatum.
type Observation struct {
	valid       bool
	agent       string
	sessionID   string
	writtenAt   time.Time
	usedPct     float64
	tokensUsed  int64
	tokensTotal int64
}

// Agent is the roster-validated agent this datum belongs to.
func (o Observation) Agent() string { return o.agent }

// SessionID is the session that wrote it. One agent owns several across recycles.
func (o Observation) SessionID() string { return o.sessionID }

// WrittenAt is the render clock at capture. A datum reaches a caller only through
// ObservedReading, which refuses a stamp beyond the forward-skew tolerance and clamps a smaller
// one to the classification clock — so a reading never exposes a future timestamp.
func (o Observation) WrittenAt() time.Time { return o.writtenAt }

// UsedPct is context-window occupancy, clamped to [0,100]. Authoritative for the trigger.
func (o Observation) UsedPct() float64 { return o.usedPct }

// TokensUsed is the in-context token total. Diagnostic only: it drops after a compaction.
func (o Observation) TokensUsed() int64 { return o.tokensUsed }

// TokensTotal is the context window size the host reported. Diagnostic only.
func (o Observation) TokensTotal() int64 { return o.tokensTotal }

// ChannelReading is one agent's channel condition at an instant. Fields are unexported so the zero
// value cannot be spelled into existence as a healthy reading, and so a datum can only ride along
// on states that actually have one.
type ChannelReading struct {
	state ChannelState
	obs   *Observation
	age   time.Duration
}

// State reports the channel condition. An unconstructed reading normalises to StateMalformed
// rather than the empty string: a reading nobody produced is not evidence of health, and callers
// that switch on State must never meet a sixth, unhandled value.
func (r ChannelReading) State() ChannelState {
	if r.state == "" {
		return StateMalformed
	}
	return r.state
}

// IsHealthy reports whether this channel is fresh. Route every health decision through it.
func (r ChannelReading) IsHealthy() bool { return r.State().IsHealthy() }

// Observation returns the datum behind this reading, if any. The bool is false for none and
// malformed — there is nothing to return, and returning a zero Observation would reintroduce
// exactly the 0%-reads-healthy defect this package exists to prevent.
func (r ChannelReading) Observation() (Observation, bool) {
	if r.obs == nil {
		return Observation{}, false
	}
	return *r.obs, true
}

// UsedPct returns the occupancy percentage and whether one exists. Callers MUST check the bool:
// a false with a 0 is "no reading", not "empty context".
func (r ChannelReading) UsedPct() (float64, bool) {
	if r.obs == nil {
		return 0, false
	}
	return r.obs.usedPct, true
}

// Age is how long ago the datum was written, at the classification clock. Never negative.
func (r ChannelReading) Age() (time.Duration, bool) {
	if r.obs == nil {
		return 0, false
	}
	return r.age, true
}

// NoReading is the reading for an agent with no attributable snapshot. It carries no datum and
// can never be healthy.
func NoReading() ChannelReading { return ChannelReading{state: StateNone} }

// MalformedReading is the reading for an agent whose only attributable snapshots failed
// validation. It carries no datum and can never be healthy.
func MalformedReading() ChannelReading { return ChannelReading{state: StateMalformed} }

// ObservedReading classifies a validated Observation against the injected clock and thresholds.
// It is the ONLY path to a healthy reading, and it enforces the ADR-005 runtime precondition: an
// Observation that did not come from a validated decode is refused, not classified.
//
// Zero thresholds classify everything as dark. That is the deliberate fail-closed direction, the
// same posture as the watchdog's scope set where "a nil scope is never all" — a caller that forgot
// to configure the reader gets a visibly dark factory, never a silently healthy one.
func ObservedReading(obs Observation, opts ReadOptions, now time.Time) ChannelReading {
	if !obs.valid {
		return MalformedReading()
	}

	// A stamp far enough ahead of the clock is not skew, it is an unusable datum. Clamping alone
	// is NOT a sufficient remedy: the clamp is re-applied on every read, so a written_at D into
	// the future reads fresh for the whole of D no matter how long the agent has actually been
	// dead. A container on a fast clock would therefore have recovery silently disabled — the
	// exact AC-5 inversion this reader exists to prevent — and a forged stamp would buy unbounded
	// health. Refusing beyond a bounded tolerance is what makes the clamp safe.
	if obs.writtenAt.After(now.Add(forwardSkewTolerance(opts))) {
		return MalformedReading()
	}

	age := now.Sub(obs.writtenAt)
	if age < 0 {
		// Benign skew, inside the tolerance: treat "slightly ahead" as "just now" so a healthy
		// agent on a marginally fast clock is not reported stale. The tolerance bounds how much
		// freshness this can buy, and the stamp is clamped so no caller ever sees a future time.
		age = 0
		obs.writtenAt = now
	}

	// An under-configured caller gets a visibly dark factory, never a silently healthy one — the
	// same fail-closed posture as the watchdog's scope set, where a nil scope is never "all".
	// Without this, a zero-valued threshold would classify a just-written datum as fresh, because
	// age 0 satisfies `age <= 0`.
	//
	// EITHER threshold being unset is disqualifying, not both: the two are a pair describing one
	// staleness ladder, so a half-configured reader is exactly as untrustworthy as an unconfigured
	// one and must not be allowed to emit health.
	if opts.Staleness <= 0 || opts.DarkAfter <= 0 {
		return ChannelReading{state: StateDark, obs: &obs, age: age}
	}

	state := StateDark
	switch {
	case age <= opts.Staleness:
		state = StateFresh
	case age <= opts.DarkAfter:
		state = StateStale
	}
	return ChannelReading{state: state, obs: &obs, age: age}
}

// forwardSkewTolerance bounds how far ahead of the reader's clock a written_at may sit before the
// datum is refused outright. It reuses Staleness rather than adding a knob: Staleness already
// answers "how long may this channel go unrefreshed before we stop trusting it", and allowing the
// same magnitude of forward error keeps one number governing one idea. A zero or negative
// Staleness admits no forward skew at all.
func forwardSkewTolerance(opts ReadOptions) time.Duration {
	if opts.Staleness > 0 {
		return opts.Staleness
	}
	return 0
}

// ReadOptions carries everything the reader may not discover for itself. design-doc.md:158's
// ReadObservations(dir, now) is labelled "proposed" and cannot express either the roster or the
// thresholds, so it is extended here (Gotcha 11).
//
// Note the roster is passed rather than loaded, and NOT because of an import cycle — the IMPLREADME
// states one, but internal/statusline already imports internal/config (render.go) and the reverse
// edge does not exist, so no cycle is possible. The real reasons: the Phase 2 caller already holds
// the decoded config, loading it here would give one function two unrelated failure domains on a
// trust boundary, and a parameter keeps the reader testable without a factory scaffold.
type ReadOptions struct {
	// KnownAgents is the agents.json roster. A snapshot naming an agent outside it is discarded.
	// Nil or empty means nothing is known, so nothing reads healthy — never "trust everything".
	//
	// It is also the ONLY place an agent can be excluded from the sweep, which is where Gotcha
	// 14's "skip ForeignRoot sessions" lands: ForeignRoot is derived from live tmux state
	// (internal/cmd/agents.go), which this library cannot and must not observe (ADR-004). The
	// CALLER is responsible for leaving foreign-root agents out of this set — the reader has no
	// way to notice they should have been.
	KnownAgents map[string]struct{}
	// Staleness is the fresh→stale boundary (Phase 0B's staleness_secs, 180s by default).
	Staleness time.Duration
	// DarkAfter is the stale→dark boundary (Phase 0B's dark_grace_secs, 600s by default).
	DarkAfter time.Duration
}

// observationFile is the reader's own decode target. Every required field is a POINTER so that an
// absent key, an explicit null, and a legitimate zero are three distinguishable inputs — the
// distinction the whole "absent ⇒ malformed, never 0%-healthy" rule rests on, and the same idiom
// payload.go:17-18 and startup.go:41-44 already use.
//
// Unknown fields are ignored, deliberately. That is the schema-drift tolerance the design relies
// on (payload.go:12-14): this reader validates the presence of what it needs, never the absence of
// what it does not know.
type observationFile struct {
	Schema             *int     `json:"schema"`
	SessionID          *string  `json:"session_id"`
	Agent              *string  `json:"agent"`
	WrittenAt          *string  `json:"written_at"`
	ContextUsedPct     *float64 `json:"context_used_pct"`
	ContextTokensUsed  *int64   `json:"context_tokens_used"`
	ContextTokensTotal *int64   `json:"context_tokens_total"`
}

// ReadObservations classifies every known agent's occupancy channel from the snapshot directory.
//
// The returned map has an entry for EVERY agent in opts.KnownAgents — an agent with no data reads
// StateNone rather than being absent, so a caller iterating the map cannot mistake a missing key
// for "nothing to worry about". One agent may own several session files across recycles, so the
// newest valid written_at wins (Gotcha 15).
//
// A missing directory is not an error: with the factory statusline gate off, or before any agent
// has rendered, it legitimately does not exist and every channel is none. A genuinely unreadable
// directory DOES return an error, but still returns the fully-populated all-none map, so a caller
// that ignores the error cannot observe a healthy reading either. One bad file is skipped, never
// fatal (SumDaily's contract).
func ReadObservations(dir string, opts ReadOptions, now time.Time) (map[string]ChannelReading, error) {
	readings := make(map[string]ChannelReading, len(opts.KnownAgents))
	malformed := make(map[string]bool, len(opts.KnownAgents))
	for agent := range opts.KnownAgents {
		readings[agent] = NoReading()
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return readings, nil
		}
		return readings, err
	}

	newest := make(map[string]Observation, len(opts.KnownAgents))
	for _, e := range entries {
		// Regular files ONLY. A size cap bounds how much a hostile file can make us read, but it
		// cannot bound how LONG: opening a FIFO read-only blocks until a writer appears, so a
		// single `mkfifo evil.json` in this agent-writable directory would wedge the sweep
		// forever — recovery silently disabled, which is the failure class this reader exists to
		// eliminate. This also drops symlinks, devices and sockets, none of which the renderer
		// ever creates.
		if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		// The reader mirrors the WRITER's own filename grammar by calling its sanitizer rather
		// than restating it, so the two cannot drift into disagreeing about which files exist
		// (the issue #563 lesson, one level removed).
		sid := strings.TrimSuffix(e.Name(), ".json")
		if sid == "" || sanitizeSessionID(sid) != sid {
			continue
		}

		obs, agent, ok := readObservationFile(filepath.Join(dir, e.Name()), sid, opts)
		if agent == "" {
			continue // unattributable: no roster-valid agent to charge this file to
		}
		if !ok {
			malformed[agent] = true
			continue
		}
		if prev, seen := newest[agent]; !seen || obs.writtenAt.After(prev.writtenAt) {
			newest[agent] = obs
		}
	}

	for agent, obs := range newest {
		readings[agent] = ObservedReading(obs, opts, now)
	}
	// A malformed file only surfaces when the agent has NO valid datum. A live snapshot must not
	// be masked by a corrupt leftover from an earlier session of the same agent.
	for agent := range malformed {
		if _, hasDatum := newest[agent]; !hasDatum {
			readings[agent] = MalformedReading()
		}
	}
	return readings, nil
}

// readObservationFile reads and validates one snapshot. It returns the agent the file is
// ATTRIBUTABLE to (empty when it cannot be charged to any roster member) separately from whether
// the datum is USABLE, so a known agent's broken file reads malformed while an unattributable one
// — a #595 v1 file, or a forged name — simply contributes nothing to anybody.
func readObservationFile(path, sessionID string, opts ReadOptions) (obs Observation, agent string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return Observation{}, "", false
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxObservationBytes+1))
	if err != nil {
		return Observation{}, "", false
	}
	if len(data) > maxObservationBytes {
		return Observation{}, "", false // errObservationTooLarge: unattributable, we never decoded it
	}

	var of observationFile
	if err := json.Unmarshal(data, &of); err != nil {
		return Observation{}, "", false
	}

	// Attribution first: the agent name must be well-formed AND on the roster. Grammar reuses
	// config.ValidateAgentName so there is only one spelling of an agent name in the tree; the
	// roster check is a separate concern (a well-formed name for an agent that does not exist).
	if of.Agent == nil || config.ValidateAgentName(*of.Agent) != nil {
		return Observation{}, "", false
	}
	agent = *of.Agent
	if _, known := opts.KnownAgents[agent]; !known {
		return Observation{}, "", false
	}

	// From here the file belongs to a real agent, so every failure is that agent's malformed
	// channel rather than a file we can ignore.
	if of.Schema == nil || *of.Schema < minSchemaVersion {
		return Observation{}, agent, false
	}
	if of.WrittenAt == nil || of.ContextUsedPct == nil || of.ContextTokensUsed == nil || of.ContextTokensTotal == nil {
		return Observation{}, agent, false
	}
	writtenAt, err := time.Parse(time.RFC3339, *of.WrittenAt)
	if err != nil {
		return Observation{}, agent, false
	}
	pct := *of.ContextUsedPct
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	// The file's own session_id is advisory; the FILENAME is what the writer guarantees and what
	// the grammar check above already validated.
	return Observation{
		valid:       true,
		agent:       agent,
		sessionID:   sessionID,
		writtenAt:   writtenAt,
		usedPct:     pct,
		tokensUsed:  *of.ContextTokensUsed,
		tokensTotal: *of.ContextTokensTotal,
	}, agent, true
}
