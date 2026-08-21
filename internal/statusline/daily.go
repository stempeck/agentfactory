package statusline

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

const (
	// throttleInterval is the minimum gap between real writes of one session's snapshot.
	// Values are cumulative, so a skipped write loses nothing permanently — the next write
	// catches up (scale.md S2a).
	throttleInterval = 10 * time.Second
	// pruneHorizon is how long a stale-dated file survives after its last real update before
	// it is hard-deleted; until then it is rolled over (baseline preserved) rather than
	// deleted, so the midnight mis-attribution cannot return through a delete/rewrite race.
	pruneHorizon = 48 * time.Hour
	// maxSnapshotBytes bounds a single snapshot read so a hostile/corrupt file cannot force an
	// unbounded read at render time (SEC-4).
	maxSnapshotBytes = 4 * 1024
	dateLayout       = "2006-01-02" // local day key (D8): pins the day boundary explicitly
	maxSessionIDLen  = 128
)

// schemaVersion is stamped on every snapshot (issue #596 K1). Evolution is additive and the
// reader validates the PRESENCE of the fields it needs rather than rejecting unknown ones, so a
// newer af can add a key an older one has never heard of (the internal/telemetry/event.go:3-6
// rule). The version exists so the occupancy reader can tell a v2 snapshot from one of #595's
// cost-only v1 files, which carry no occupancy at all and must never read as healthy.
const schemaVersion = 2

var errSnapshotTooLarge = errors.New("statusline: snapshot exceeds size cap")

// sessionSnapshot is one session's per-day snapshot. The cumulative values are the payload's
// running totals (not deltas); the baseline is what those totals were at the start of THIS
// day for THIS session, so the session's contribution to today is cumulative − baseline
// (cross-review C1). A fresh session today has baseline 0; on the session's own date rollover
// the new file's baseline is set to the previous cumulative, so lifetime spend is never
// re-attributed to the new day.
// The InputTokens/OutputTokens pair holds context-window OCCUPANCY copied from the payload. It is
// never summed by anything, and it must never be reinterpreted as spend: occupancy shrinks on
// compaction, which is exactly how PR #595's daily figure went negative (F1). The cumulative token
// counter therefore lives in DELIBERATELY DISTINCT fields below, so a file written by an older
// binary contributes zero tokens instead of poisoning the sum during the 48h upgrade window
// (Gap 7; data.md B2.2 was rejected for this).
//
// The occupancy fields (issue #596 K1) are a SECOND, independent concern riding the same file:
// they are point-in-time, unrelated to the daily/baseline cost semantics above, and they are
// POINTERS on purpose. encoding/json decodes an absent key and an explicit 0 to the same zero
// value, so a non-pointer context_used_pct would persist 0% for a payload that reported NOTHING
// — and the reader would see a present, valid, maximally-healthy datum for a wedged agent. The
// pointer preserves the distinction payload.go:17-18 deliberately introduced upstream.
//
// WrittenAt is a NEW field, never an alias of UpdatedAt. UpdatedAt is the cost snapshot's
// throttle-and-reaper clock, which Prune deliberately preserves across a rollover so the 48h
// reaper can still claim an absent session; WrittenAt is the occupancy observation's own stamp,
// which the reader future-clamps. Aliasing them would let a rollover re-publish a fresh-looking
// stamp for a channel that has gone dark. FirstSeen is a THIRD clock — the session's first-write
// stamp (carried forward via firstSeenOr) — and must not alias either of the other two.
type sessionSnapshot struct {
	Schema               int      `json:"schema"`
	SessionID            string   `json:"session_id"`
	Date                 string   `json:"date"`
	Agent                string   `json:"agent"`
	WrittenAt            string   `json:"written_at"`
	ContextUsedPct       *float64 `json:"context_used_pct"`
	ContextTokensUsed    *int64   `json:"context_tokens_used"`
	ContextTokensTotal   *int64   `json:"context_tokens_total"`
	BaselineCostUSD      float64  `json:"baseline_cost_usd"`
	BaselineInputTokens  int64    `json:"baseline_input_tokens"`
	BaselineOutputTokens int64    `json:"baseline_output_tokens"`
	CostUSD              float64  `json:"cost_usd"`
	InputTokens          int64    `json:"input_tokens"`
	OutputTokens         int64    `json:"output_tokens"`
	CumTokens            int64    `json:"cum_tokens"`
	BaselineCumTokens    int64    `json:"baseline_cum_tokens"`
	RederiveTokens       int64    `json:"rederive_tokens"`
	TranscriptOffset     int64    `json:"transcript_offset"`
	TranscriptHeadSig    int64    `json:"transcript_head_sig"`
	FirstSeen            string   `json:"first_seen"`
	UpdatedAt            string   `json:"updated_at"`
}

// DailyTotals is the summed "today" contribution across every session that renders in this
// factory (design-doc.md:250-254). Cost sums cumulative − baseline unclamped, exactly as it always
// has. Tokens takes the same shape but CLAMPS each session's contribution at zero: the counter is
// fed by an incremental transcript reader that can be re-based by a concurrent write or a replaced
// transcript, and a single negative contribution is how PR #595 rendered "D $0.40 · -550000 tok".
// The clamp is load-bearing on its own, independently of the source being truthful.
type DailyTotals struct {
	CostUSD  float64
	Tokens   int64
	Sessions int
}

// WriteSnapshot records this session's cumulative spend AND its point-in-time context occupancy
// for the day at now, with per-day baseline semantics for the cost half. It is idempotent
// (cumulative, not delta), throttled (≥10s since the in-file updated_at, unless the date rolled
// over), atomic, and safe: an unusable session_id (empty after sanitizing to
// [A-Za-z0-9_-]{1,128}) SKIPS the write entirely — path traversal is impossible by construction
// (SEC-4).
//
// agent is the AF_ROLE the CMD LAYER resolved (ADR-004: this library reads no environment). It is
// recorded as an UNVALIDATED ambient claim — the render hot path is contractually silent and must
// not load agents.json per render (C-5). Validating it against the roster is deliberately deferred
// to the occupancy reader, which is the trust boundary over this agent-writable directory
// (design-doc.md:179: validation IS the boundary). An empty agent is written honestly rather than
// suppressed; the reader is what refuses to attribute it.
//
// Throttling covers occupancy too, so a reading is never fresher than throttleInterval — the first
// term of the design's latency formula, max(throttle, refresh_interval) (design-doc.md:238).
func WriteSnapshot(dir string, p Payload, agent string, now time.Time) error {
	return WriteSnapshotWith(dir, p, agent, now, nil)
}

// WriteSnapshotWith is WriteSnapshot plus the cumulative token counter. The accumulator is invoked
// ONLY on the branch that actually writes, with the cursor from the same read that decided the
// throttle — so transcript work is gated to the ≥10s window by construction rather than by the
// caller remembering to check (scale.md S3.4), there is no second read of the snapshot, and no
// TOCTOU gap between "was the window open" and "did the write land".
//
// A nil accumulator carries the previous counter forward untouched, which keeps the counter safe
// under every non-render caller of WriteSnapshot.
func WriteSnapshotWith(dir string, p Payload, agent string, now time.Time, acc TokenAccumulator) error {
	sid := sanitizeSessionID(p.SessionID)
	if sid == "" {
		return nil
	}
	path := filepath.Join(dir, sid+".json")
	today := now.Format(dateLayout)

	prev, _ := readSnapshot(path) // a corrupt/oversized/absent prior file ⇒ treat as fresh

	if prev != nil && prev.Date == today {
		if u, err := time.Parse(time.RFC3339, prev.UpdatedAt); err == nil && now.Sub(u) < throttleInterval {
			return nil
		}
	}

	stamp := now.UTC().Format(time.RFC3339)
	snap := sessionSnapshot{
		Schema:       schemaVersion,
		SessionID:    sid,
		Date:         today,
		Agent:        agent,
		WrittenAt:    stamp,
		CostUSD:      p.Cost.TotalCostUSD,
		InputTokens:  p.ContextWindow.TotalInputTokens,
		OutputTokens: p.ContextWindow.TotalOutputTokens,
		FirstSeen:    stamp,
		UpdatedAt:    stamp,
	}
	setOccupancy(&snap, p)
	switch {
	case prev == nil:
		// fresh session today ⇒ baseline stays 0
	case prev.Date == today:
		snap.BaselineCostUSD = prev.BaselineCostUSD
		snap.BaselineInputTokens = prev.BaselineInputTokens
		snap.BaselineOutputTokens = prev.BaselineOutputTokens
		snap.BaselineCumTokens = prev.BaselineCumTokens
		snap.FirstSeen = firstSeenOr(prev.FirstSeen, stamp)
	default: // the session's own date rolled over ⇒ baseline := previous cumulative (C1)
		snap.BaselineCostUSD = prev.CostUSD
		snap.BaselineInputTokens = prev.InputTokens
		snap.BaselineOutputTokens = prev.OutputTokens
		snap.BaselineCumTokens = prev.CumTokens
		snap.FirstSeen = firstSeenOr(prev.FirstSeen, stamp)
	}

	// The cursor survives BOTH arms: resetting the offset at midnight would silently re-derive the
	// entire transcript from byte zero on every post-rollover render.
	cur := TranscriptCursor{}
	if prev != nil {
		cur = TranscriptCursor{
			CumTokens:      prev.CumTokens,
			RederiveTokens: prev.RederiveTokens,
			Offset:         prev.TranscriptOffset,
			HeadSig:        prev.TranscriptHeadSig,
		}
	}
	if acc != nil {
		next := acc(cur)
		// Two concurrent renders of one session can each read the same prior snapshot and one will
		// clobber the other (fsutil.WriteFileAtomic is explicitly last-writer-wins). Clamping at the
		// single writer means the worst outcome of that race is a repeated read, never a decrement.
		if next.CumTokens < cur.CumTokens {
			next.CumTokens = cur.CumTokens
		}
		cur = next
	}
	snap.CumTokens = cur.CumTokens
	snap.RederiveTokens = cur.RederiveTokens
	snap.TranscriptOffset = cur.Offset
	snap.TranscriptHeadSig = cur.HeadSig

	return writeSnapshotFile(dir, snap)
}

// firstSeenOr preserves the original first-write stamp; a session that predates the field adopts
// the current one rather than staying blank.
func firstSeenOr(prev, fallback string) string {
	if prev != "" {
		return prev
	}
	return fallback
}

// setOccupancy copies the payload's context-window occupancy onto the snapshot, ALL THREE FIELDS
// OR NONE. The host reports occupancy as a set: used_percentage is null and context_window_size is
// 0 until the session has exchanged messages (payload.go:17-18). Persisting a partial block would
// hand the reader a datum it cannot interpret — and persisting 0 for an absent percentage would
// manufacture the "present, valid, 0%" reading that makes a wedged agent look maximally healthy,
// which is the exact inversion AC-5 exists to prevent. Nil fields make the file read `malformed`:
// dark-class, visible, and never healthy.
//
// TokensUsed sums the payload's in-context input and output totals. These are OCCUPANCY, not
// spend — they drop after a compaction (PR #595 T1/F1) — and they are diagnostic only:
// context_used_pct remains the authoritative signal the Phase 2 trigger reads.
func setOccupancy(snap *sessionSnapshot, p Payload) {
	if p.ContextWindow.UsedPercentage == nil || p.ContextWindow.ContextWindowSize <= 0 {
		return
	}
	pct := *p.ContextWindow.UsedPercentage
	used := p.ContextWindow.TotalInputTokens + p.ContextWindow.TotalOutputTokens
	total := p.ContextWindow.ContextWindowSize
	snap.ContextUsedPct = &pct
	snap.ContextTokensUsed = &used
	snap.ContextTokensTotal = &total
}

// SumDaily returns the factory's total spend for now's local day: the sum of cumulative −
// baseline over today-dated snapshot files. A corrupt, oversized, or foreign-dated file is
// skipped, not fatal — one bad file must never blank the whole daily element. A missing
// directory is an empty result, not an error.
func SumDaily(dir string, now time.Time) DailyTotals {
	var totals DailyTotals
	today := now.Format(dateLayout)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return totals
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		s, err := readSnapshot(filepath.Join(dir, e.Name()))
		if err != nil || s == nil || s.Date != today {
			continue
		}
		totals.CostUSD += s.CostUSD - s.BaselineCostUSD
		if tok := s.CumTokens - s.BaselineCumTokens; tok > 0 {
			totals.Tokens += tok
		}
		totals.Sessions++
	}
	return totals
}

// SessionTokens returns one session's cumulative token figure for now's local day — the number the
// `session` element's token half renders. It reads the session's own snapshot (the render path has
// just written it) and returns cum_tokens only when the snapshot is dated today; a missing, foreign-
// dated, corrupt, or unsanitizable session yields 0, which the renderer drops (per-half, H-R2) rather
// than showing a stale or fabricated figure. It reuses readSnapshot + sanitizeSessionID, so it lives
// here in package statusline where those unexported helpers do; the cmd layer passes it into
// RenderOpts.SessionTokens (a sibling read, not a WriteSnapshotWith return value — the write path
// keeps its errors-only contract). Lifetime cum_tokens (not cum−baseline) mirrors the session COST half, which is the
// session's lifetime running total, so both halves of the element span the same period (decisions D3).
func SessionTokens(dir, sessionID string, now time.Time) int64 {
	sid := sanitizeSessionID(sessionID)
	if sid == "" {
		return 0
	}
	s, err := readSnapshot(filepath.Join(dir, sid+".json"))
	if err != nil || s == nil || s.Date != now.Format(dateLayout) {
		return 0
	}
	return s.CumTokens
}

// Prune keeps the snapshot directory bounded without ever re-attributing prior-day spend. A
// stale-dated file (some OTHER session that has not rendered today) is ROLLED OVER — rewritten
// as {date: today, baseline := cumulative} so it contributes 0 until that session renders
// again — rather than deleted, because deleting would drop the baseline and let the midnight
// mis-attribution return through the race (cross-review C1). Only files whose updated_at is
// older than the 48h horizon are hard-deleted. All errors are swallowed: a concurrent pruner
// or a vanished file is normal, and prune must never fail a render.
func Prune(dir string, now time.Time) {
	today := now.Format(dateLayout)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		s, err := readSnapshot(path)
		if err != nil || s == nil || s.Date == today {
			continue
		}
		if u, err := time.Parse(time.RFC3339, s.UpdatedAt); err == nil && now.Sub(u) > pruneHorizon {
			_ = os.Remove(path)
			continue
		}
		// Roll over, PRESERVING updated_at so the 48h reaper can still eventually claim a
		// session that never comes back.
		//
		// EVERY non-cost field is carried across verbatim. This literal rebuilds the struct
		// field-by-field, so anything omitted here is written as its zero value on the first
		// local midnight after deploy — silently producing the "absent field" the occupancy
		// reader must classify as malformed, on a file a HEALTHY agent owns. That defect class
		// has shipped in this repo before (commit 2e27bf98 rebuilt an AgentEntry from a partial
		// literal and wiped four operator-owned fields on every regen). On the token side the
		// same omission is just as silent: losing transcript_offset re-derives the whole
		// transcript from byte zero on every post-midnight render, and losing cum_tokens zeroes
		// the counter outright. If you add a field to sessionSnapshot, add it here in the same
		// commit.
		//
		// written_at is preserved, NOT advanced to today: a rollover is a bookkeeping rewrite,
		// not a new observation. Prune runs on every render by ANY session, so advancing it here
		// would let one live agent's render refresh a dead agent's channel back to fresh. The
		// carried occupancy is genuinely stale after a rollover, and saying so honestly is the
		// point — dropping the fields instead would forge a malformed reading.
		_ = writeSnapshotFile(dir, sessionSnapshot{
			Schema:               s.Schema,
			SessionID:            s.SessionID,
			Date:                 today,
			Agent:                s.Agent,
			WrittenAt:            s.WrittenAt,
			ContextUsedPct:       s.ContextUsedPct,
			ContextTokensUsed:    s.ContextTokensUsed,
			ContextTokensTotal:   s.ContextTokensTotal,
			BaselineCostUSD:      s.CostUSD,
			BaselineInputTokens:  s.InputTokens,
			BaselineOutputTokens: s.OutputTokens,
			BaselineCumTokens:    s.CumTokens,
			CostUSD:              s.CostUSD,
			InputTokens:          s.InputTokens,
			OutputTokens:         s.OutputTokens,
			CumTokens:            s.CumTokens,
			RederiveTokens:       s.RederiveTokens,
			TranscriptOffset:     s.TranscriptOffset,
			TranscriptHeadSig:    s.TranscriptHeadSig,
			FirstSeen:            s.FirstSeen,
			UpdatedAt:            s.UpdatedAt,
		})
	}
}

// sanitizeSessionID reduces a semi-trusted session_id to [A-Za-z0-9_-]{1,128}; the empty
// result signals "skip the write" (SEC-4).
func sanitizeSessionID(id string) string {
	b := make([]byte, 0, len(id))
	for i := 0; i < len(id) && len(b) < maxSessionIDLen; i++ {
		c := id[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			b = append(b, c)
		}
	}
	return string(b)
}

// readSnapshot reads and decodes one snapshot file with a size cap. ENOENT, an oversized file,
// and malformed JSON are all returned as errors so callers skip the file rather than trust it.
func readSnapshot(path string) (*sessionSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSnapshotBytes {
		return nil, errSnapshotTooLarge
	}
	var s sessionSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// writeSnapshotFile creates the sessions directory on demand and writes the snapshot atomically
// (temp+rename) so a concurrent SumDaily never observes a torn file (telemetry/store.go idiom;
// there is no fsutil.MkdirAll — D7).
func writeSnapshotFile(dir string, snap sessionSnapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, snap.SessionID+".json"), append(raw, '\n'), 0o644)
}
