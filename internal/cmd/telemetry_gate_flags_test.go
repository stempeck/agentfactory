package cmd

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/mail"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// gateVerdictFixture prepares a factory whose mail goes to a memstore, positioned so that
// sendGateVerdict files through the real `af mail send` verb.
func gateVerdictFixture(t *testing.T) *memstore.Store {
	t.Helper()
	factoryRoot := setupMailSendFixture(t)
	store := installMemStore(t)
	t.Chdir(factoryRoot)
	t.Setenv("AF_ROLE", "")
	return store
}

// sendGateVerdict files one verdict at the recipient, through the CLI rather than through
// store.Create. The gates are shell scripts that shell out to `af mail send`
// (hooks/quality-gate.sh:158, hooks/fidelity-gate.sh:333), so a fixture that built the issue
// directly would be asserting this test's idea of the mail wire format, not mail's.
//
// The sender is explicit where the gates let it be detected: they run from the agent's own
// directory and this runs from wherever the fixture put the test.
func sendGateVerdict(t *testing.T, from, to, subject string) {
	t.Helper()
	if err := execMailSend(t, to, "-s", subject, "-m", "verdict body", "--from", from); err != nil {
		t.Fatalf("filing a %s verdict at %s: %v", subject, to, err)
	}
}

func gateWindowStamp(offset time.Duration) string {
	return time.Now().UTC().Add(offset).Format(telemetry.TimestampLayout)
}

// gateFlagsDesc renders the result the way the failure needs to read it. %v on a *int64 prints an
// address, which tells a reader nothing about the count that was wrong.
func gateFlagsDesc(got *int64) string {
	if got == nil {
		return "absent"
	}
	return strconv.FormatInt(*got, 10)
}

func gateFlagsForBob(t *testing.T, store *memstore.Store, startOffset, endOffset time.Duration) *int64 {
	t.Helper()
	return gateFlagsInWindow(t.Context(), store, "bob",
		gateWindowStamp(startOffset), gateWindowStamp(endOffset))
}

// recordingStore wraps a memstore and captures the CreatedAfter bound List was
// asked for, so a test can prove gate_flags bounds the store read at the step
// start rather than pulling the agent's whole mail history and filtering it
// client-side (the partial-fix trap #679/T7 names).
type recordingStore struct {
	*memstore.Store
	listCreatedAfter string
}

func (r *recordingStore) List(ctx context.Context, f issuestore.Filter) ([]issuestore.Issue, error) {
	r.listCreatedAfter = f.CreatedAfter
	return r.Store.List(ctx, f)
}

// TestGateFlagsBoundsTheStoreReadAtTheStepStart pins that the store read is
// bounded IN the store, not after (#679/T7). A client-side-only filter still
// pulls the agent's entire mail archive over the wire on every af done; the
// reviewer asked for the bound "in the store." The count must stay correct
// through the bound, and the bound must be the step's own start.
func TestGateFlagsBoundsTheStoreReadAtTheStepStart(t *testing.T) {
	base := gateVerdictFixture(t)
	sendGateVerdict(t, "alice", "bob", "STEP_FIDELITY")
	rec := &recordingStore{Store: base}

	startTS, endTS := gateWindowStamp(-time.Minute), gateWindowStamp(time.Minute)
	got := gateFlagsInWindow(t.Context(), rec, "bob", startTS, endTS)
	if got == nil || *got != 1 {
		t.Fatalf("gate_flags = %s across the window, want 1 — the bound must not drop the verdict", gateFlagsDesc(got))
	}

	if rec.listCreatedAfter == "" {
		t.Fatal("gate_flags read the store with no created-after bound — the whole mail history returns on every af done (#679/T7)")
	}
	start, err := time.Parse(telemetry.TimestampLayout, startTS)
	if err != nil {
		t.Fatalf("parsing the fixture start: %v", err)
	}
	if want := start.UTC().Format("2006-01-02T15:04:05.000000Z"); rec.listCreatedAfter != want {
		t.Errorf("created-after bound = %q, want the step start %q", rec.listCreatedAfter, want)
	}
}

// TestGateFlagsCountEveryVerdictTheStepEarned pins what gate_flags measures (#678 K1).
//
// The figure is the quality half of the efficiency question: K4 declines a token reduction whose
// arm shows more gate flags. That guard is only as good as the count, and the count has one way to
// fail that is worse than noise — reading mail is destructive here (MarkRead closes it, and Delete
// IS MarkRead), and fidelity-gate.sh:322 tells the agent to delete each verdict once acted on. A
// count taken from the inbox would therefore score the compliant agent 0 and the one that ignored
// its mail 3: anti-correlated with the quality it exists to protect, which would invert the guard
// rather than weaken it.
func TestGateFlagsCountEveryVerdictTheStepEarned(t *testing.T) {
	t.Run("a verdict the agent read and deleted still counts", func(t *testing.T) {
		store := gateVerdictFixture(t)
		sendGateVerdict(t, "alice", "bob", "QUALITY_GATE")

		box := mail.NewMailbox("bob", store)
		inbox, err := box.List(t.Context())
		if err != nil {
			t.Fatalf("fixture: reading bob's inbox: %v", err)
		}
		if len(inbox) != 1 {
			t.Fatalf("fixture: bob's inbox has %d messages, want 1", len(inbox))
		}
		if err := box.Delete(t.Context(), inbox[0].ID); err != nil {
			t.Fatalf("acting on the verdict: %v", err)
		}

		got := gateFlagsForBob(t, store, -time.Minute, time.Minute)
		if got == nil || *got != 1 {
			t.Errorf("gate_flags = %s after the agent acted on its verdict and deleted it, want 1 — "+
				"a flag that was raised does not stop having been raised", gateFlagsDesc(got))
		}
	})

	t.Run("both gate subjects count and nothing else does", func(t *testing.T) {
		store := gateVerdictFixture(t)
		sendGateVerdict(t, "alice", "bob", "QUALITY_GATE")
		sendGateVerdict(t, "alice", "bob", "STEP_FIDELITY")
		sendGateVerdict(t, "alice", "bob", "HANDOFF")

		got := gateFlagsForBob(t, store, -time.Minute, time.Minute)
		if got == nil || *got != 2 {
			t.Errorf("gate_flags = %s across two verdicts and one ordinary message, want 2 — "+
				"ordinary mail is traffic, not a failing verdict", gateFlagsDesc(got))
		}
	})

	t.Run("another agent's verdict is not this agent's flag", func(t *testing.T) {
		store := gateVerdictFixture(t)
		sendGateVerdict(t, "bob", "alice", "QUALITY_GATE")

		got := gateFlagsForBob(t, store, -time.Minute, time.Minute)
		if got == nil || *got != 0 {
			t.Errorf("gate_flags = %s for bob from a verdict filed at alice, want a measured 0", gateFlagsDesc(got))
		}
	})

	t.Run("verdicts outside the window belong to other steps", func(t *testing.T) {
		store := gateVerdictFixture(t)
		sendGateVerdict(t, "alice", "bob", "QUALITY_GATE")

		if got := gateFlagsForBob(t, store, -2*time.Hour, -time.Hour); got == nil || *got != 0 {
			t.Errorf("gate_flags = %s from a window that closed before the verdict was filed, "+
				"want a measured 0", gateFlagsDesc(got))
		}
		if got := gateFlagsForBob(t, store, time.Hour, 2*time.Hour); got == nil || *got != 0 {
			t.Errorf("gate_flags = %s from a window that opens after the verdict was filed, "+
				"want a measured 0", gateFlagsDesc(got))
		}
	})

	// nil and 0 are different facts. 0 says the gates raised nothing, which is a measurement the
	// efficiency baseline can use; nil says nobody looked, which it must decline.
	t.Run("nothing is claimed when nobody could look", func(t *testing.T) {
		store := gateVerdictFixture(t)
		sendGateVerdict(t, "alice", "bob", "QUALITY_GATE")

		start, end := gateWindowStamp(-time.Minute), gateWindowStamp(time.Minute)
		cases := []struct {
			name            string
			agent           string
			start, end      string
			withoutTheStore bool
		}{
			{name: "no store", agent: "bob", start: start, end: end, withoutTheStore: true},
			{name: "no agent to ask about", agent: "", start: start, end: end},
			{name: "the step never opened", agent: "bob", start: "", end: end},
			{name: "the step never closed", agent: "bob", start: start, end: ""},
			{name: "the window runs backwards", agent: "bob", start: end, end: start},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if tc.withoutTheStore {
					if got := gateFlagsInWindow(t.Context(), nil, tc.agent, tc.start, tc.end); got != nil {
						t.Errorf("gate_flags = %d, want absent", *got)
					}
					return
				}
				if got := gateFlagsInWindow(t.Context(), store, tc.agent, tc.start, tc.end); got != nil {
					t.Errorf("gate_flags = %d, want absent", *got)
				}
			})
		}
	})
}

// TestSupersededVerdictsStillCountN pins that gateFlagsInWindow counts every STEP_FIDELITY verdict a step
// earned, including a superseded one: supersession is `af mail delete` (MarkRead→Close), and
// gateFlagsInWindow counts through ListAll whose history filter sets IncludeClosed:true, so a closed
// verdict still counts. Two verdicts for one agent/step, the first deleted, count N=2, not a floor of 1.
// F18 is a COMMENT-ONLY fix (telemetry_gate_flags.go's "collapse to one … a floor" comment was wrong;
// the code was already correct); this test proves the count is N, so the stale comment was the only
// defect. It passes today and must keep passing. Models on the table above.
func TestSupersededVerdictsStillCountN(t *testing.T) {
	store := gateVerdictFixture(t)

	// Two fidelity verdicts for the same agent, as a step that flagged, was re-graded, and flagged
	// again would leave — the shape the "collapse to one" comment describes.
	sendGateVerdict(t, "alice", "bob", "STEP_FIDELITY")
	sendGateVerdict(t, "alice", "bob", "STEP_FIDELITY")

	// Supersede the first: `af mail delete` closes the bead but does not remove it from the history
	// gateFlagsInWindow reads.
	box := mail.NewMailbox("bob", store)
	inbox, err := box.List(t.Context())
	if err != nil {
		t.Fatalf("reading bob's inbox: %v", err)
	}
	if len(inbox) != 2 {
		t.Fatalf("fixture: bob's inbox has %d verdicts, want 2", len(inbox))
	}
	if err := box.Delete(t.Context(), inbox[0].ID); err != nil {
		t.Fatalf("superseding the first verdict: %v", err)
	}

	got := gateFlagsForBob(t, store, -time.Minute, time.Minute)
	if got == nil || *got != 2 {
		t.Errorf("gate_flags = %s after two fidelity verdicts with the first superseded, want 2 — a "+
			"superseded verdict is still a verdict the step earned; ListAll includes the closed one, so "+
			"the count is N, not a floor of 1", gateFlagsDesc(got))
	}
}
