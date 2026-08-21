package cmd

import (
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// This file is #622 C4's read half: the one derivation of "how full is THIS session's context
// window right now", shared by the two lifecycle verbs that record it. af prime and af done ask
// the same question at the two ends of a step, and two spellings of it would drift — which is the
// failure this file exists to prevent, not merely a tidiness preference.

// stepContextReading classifies the occupancy channel of the agent's OWN session.
//
// Session-keyed, never agent-keyed (cross-review CRIT-1). ReadObservations resolves an agent's
// several session files by newest-written_at-wins, so for up to staleness_secs after a recycle the
// DEAD session's last snapshot is the newest one an agent-keyed read can find. A step boundary
// deciding on that would recycle a freshly-started session on its predecessor's occupancy.
//
// The RAW session id goes in. statusline sanitizes it internally and names the file from the
// result, so the caller never restates the filename grammar — the #563 drift class, where two
// spellings of one identifier sit on the two sides of a comparison and the match silently never
// happens (recovery.go documents the same trap for the recycle fence).
//
// Every failure returns a reading that carries no datum. That is the fail-closed direction the
// whole channel is built on: absence must never be able to arm an action.
func stepContextReading(factoryRoot, workDir, agentName string, rec config.RecoveryConfig, now time.Time) statusline.ChannelReading {
	if agentName == "" {
		// The roster check admits nothing without a name (observation.go), so an unnamed caller
		// would get a no-datum reading anyway; returning early says why.
		return statusline.NoReading()
	}
	staleness := time.Duration(rec.StalenessSecs) * time.Second
	darkAfter := time.Duration(rec.DarkGraceSecs) * time.Second
	if staleness <= 0 || darkAfter <= 0 {
		// ObservedReading classifies every datum dark when either bound is unset. Returning none
		// here rather than passing zeros keeps "this factory is not configured to judge freshness"
		// distinct from "this session has gone dark".
		return statusline.NoReading()
	}
	return statusline.SessionObservation(
		config.StatuslineSessionsDir(factoryRoot),
		readRuntimeSessionID(workDir),
		statusline.ReadOptions{
			// The one-element roster idiom every other cmd-layer caller uses: an agent asking
			// about itself knows exactly one name, and a nil roster would mean "trust nothing"
			// rather than "trust everything".
			KnownAgents: map[string]struct{}{agentName: {}},
			Staleness:   staleness,
			DarkAfter:   darkAfter,
		},
		now,
	)
}

// attachStepOccupancy records what was measured, and records nothing at all when nothing was.
//
// FRESH only. A stale or dark datum is a real number about a moment that has passed, and writing
// it as though it described this step would put a figure the improvement loop cannot date into the
// loop's own evidence. Absent, stale, malformed and dark all leave every pointer nil — and nil is
// the point: these fields are pointers precisely so that "nobody measured" and "measured as zero"
// stay different facts. A zero occupancy reads as an empty context window, which is the opposite
// of the conclusion an unmeasured overrun should produce.
func attachStepOccupancy(ev *telemetry.StepEvent, reading statusline.ChannelReading, factoryRoot string, now time.Time) {
	if !reading.IsHealthy() {
		return
	}
	obs, ok := reading.Observation()
	if !ok {
		return
	}

	usedPct := obs.UsedPct()
	tokensUsed := obs.TokensUsed()
	tokensTotal := obs.TokensTotal()
	ev.CtxUsedPct = &usedPct
	ev.CtxTokensUsed = &tokensUsed
	ev.CtxTokensTotal = &tokensTotal
	ev.CtxObservedAt = obs.WrittenAt().UTC().Format(telemetry.TimestampLayout)

	// cum_tokens has no accessor on Observation and no key in the reader's decode target, so it
	// comes from the one session-keyed source there is. Keyed on obs.SessionID() — the stem the
	// reading itself was decoded from — rather than on a second .runtime/session_id read, so the
	// two figures cannot come from different sessions if a respawn lands between them.
	// SessionTokens returns a bare 0 for absent, foreign-dated and corrupt alike, so presence is
	// inferred the way the renderer already infers it: > 0. The residual is that a session which
	// has genuinely consumed zero tokens records as unmeasured — the safe direction, because a
	// spurious 0 at either end of a step would produce a fabricated cum_tokens_delta in the loop's
	// PRIMARY figure, whereas a nil merely suppresses it.
	if cum := statusline.SessionTokens(config.StatuslineSessionsDir(factoryRoot), obs.SessionID(), now); cum > 0 {
		ev.CumTokens = &cum
	}
}
