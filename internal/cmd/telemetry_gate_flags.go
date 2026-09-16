package cmd

import (
	"context"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/mail"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// gateSubjects are the two subjects the quality gates file against an agent's own mailbox. Both are
// sent ONLY on a failing verdict — quality-gate.sh:156 mails on `.ok == false`, fidelity-gate.sh:333
// mails inside the flagged branch — so a message bearing one of these subjects IS a flag, and the
// count of them is not a proxy for one.
var gateSubjects = map[string]bool{
	"QUALITY_GATE":  true,
	"STEP_FIDELITY": true,
}

// gateFlagsBoundLayout formats the store's created-after floor with microsecond
// precision. The Python backend compares this bound lexically against stored
// RFC-3339 timestamps; a millisecond bound could sort AFTER an equal-instant
// record carrying microseconds (the fixed 'Z' suffix outranks a fractional
// digit), which would drop an in-window verdict. Six fractional digits match
// the backend's own precision, so the lexical order is the chronological order.
const gateFlagsBoundLayout = "2006-01-02T15:04:05.000000Z"

// gateFlagsInWindow counts the gate flags raised against this agent during the step (#678 K1).
//
// This is the quality half of the efficiency question. Every other figure on a step_end says what
// the step SPENT; without a number saying what it got wrong, a reduction that made the work worse
// and a reduction that made it cheaper are the same measurement. K4's quality guard declines a
// reduction whose arm shows more of these, which it cannot do unless they are recorded per step.
//
// Read from the mail store rather than from a hook, because the mail store is af-owned and already
// open on this path: the gates are shell scripts the operator can edit, and a measurement that
// required them to also report to telemetry would be a measurement the factory could not trust.
//
// ListAll and not List, which is the whole correctness of this function. List is the INBOX — it
// pins Statuses to open (mailbox.go:48), and reading mail closes it (MarkRead, which Delete also
// is), while fidelity-gate.sh:322 tells the agent to delete each verdict once acted on. Counting the
// inbox would therefore count what the agent had not yet dealt with: a diligent agent records 0 and
// a negligent one records 3, which is not merely noisy but anti-correlated with the quality this
// figure exists to guard.
//
// One subtlety worth stating plainly: fidelity-gate.sh supersedes its own prior verdict for the same
// step before filing a new one (:330), but supersession is an af mail delete (MarkRead then Close),
// and ListAll reads the agent's whole history with IncludeClosed set (mailbox.go historyFilter), so a
// superseded verdict is still returned and still counted. The figure is therefore the TOTAL count of
// every fidelity verdict the step earned in the window — not a collapsed-to-one floor — and it is
// derived identically on every run, which is what makes two arms comparable.
//
// nil rather than zero on an unusable window or a store that will not answer: "the gates raised
// nothing" and "nobody looked" are different facts, and only one of them belongs in a baseline.
func gateFlagsInWindow(ctx context.Context, store issuestore.Store, agent, startTS, endTS string) *int64 {
	if store == nil || agent == "" {
		return nil
	}
	start, startErr := time.Parse(telemetry.TimestampLayout, startTS)
	end, endErr := time.Parse(telemetry.TimestampLayout, endTS)
	if startErr != nil || endErr != nil || end.Before(start) {
		return nil
	}
	// Bound the store read at the step's start instead of pulling the agent's
	// whole mail history on every af done (#679/T7). Microsecond precision so
	// the lexical bound the store applies never sorts before an equal-instant
	// record that carries finer precision; the exact half-open window is still
	// enforced below, so this floor only ever widens the read.
	messages, err := mail.NewMailbox(agent, store).ListAllSince(ctx, start.UTC().Format(gateFlagsBoundLayout))
	if err != nil {
		return nil
	}
	var flags int64
	for _, m := range messages {
		if !gateSubjects[m.Subject] {
			continue
		}
		// Half-open, matching every other step window in this package: a message stamped exactly at
		// the boundary belongs to the step that opens then, not to the one that just closed.
		if m.Timestamp.Before(start) || !m.Timestamp.Before(end) {
			continue
		}
		flags++
	}
	return &flags
}
