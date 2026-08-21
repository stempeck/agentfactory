package readmodel

import (
	"context"
	"strings"
	"testing"
)

// Occupancy + recovery passthrough (K10-web, #596, review finding H-4).
//
// These tests exist because the af↔web contract is a hand-mirrored STRING schema, and Go's
// encoding/json discards an undeclared key silently and without error. Every other test in this
// package asserts values over a fixture written to match the struct under test, so fixture and
// struct drift together — structurally incapable of catching a DROPPED key. The assertions below
// are written so that removing a field from agentListItem or from the AgentView construction makes
// them fail, which is the only property that makes this file a guard rather than decoration.

// phase4AAgentJSON is the real 15-key `af agents list --json` row as of Phase 4A
// (internal/cmd/agents.go, agentListItem). Keeping the whole shape — not just the three new keys —
// is deliberate: the fixture doubles as the record of what af actually emits.
const phase4AAgentJSON = `[
  {"name":"halted","type":"autonomous","formula":"f","running":true,"status":"working",
   "step_id":"s1","step_title":"build","step_state":"ready","gate_id":"","inputs":{},
   "foreign_root":false,"context_pct":92,"context_state":"fresh","recovery":"halted"},
  {"name":"dark","type":"autonomous","formula":"f","running":true,"status":"working",
   "step_id":"s2","step_title":"ship","step_state":"ready","gate_id":"","inputs":{},
   "foreign_root":false,"context_pct":-1,"context_state":"dark","recovery":"recovering"},
  {"name":"nodatum","type":"autonomous","formula":"f","running":true,"status":"working",
   "step_id":"s3","step_title":"wait","step_state":"ready","gate_id":"","inputs":{},
   "foreign_root":false,"context_pct":-1,"context_state":"none","recovery":"none"}
]`

// TestReadModel_OccupancyRecoveryPassthrough — the three Phase-4A keys reach AgentView intact.
func TestReadModel_OccupancyRecoveryPassthrough(t *testing.T) {
	rm := New(
		fakeLister{json: phase4AAgentJSON},
		fakeLiveness{sessions: []string{"af-halted", "af-dark", "af-nodatum"}},
	)
	views, err := rm.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(views) != 3 {
		t.Fatalf("got %d views, want 3", len(views))
	}

	cases := []struct {
		name      string
		wantPct   int
		wantState string
		wantRecov string
	}{
		{"halted", 92, "fresh", "halted"},
		{"dark", -1, "dark", "recovering"},
		{"nodatum", -1, "none", "none"},
	}
	for i, c := range cases {
		v := views[i]
		if v.Name != c.name {
			t.Fatalf("views[%d].Name = %q, want %q", i, v.Name, c.name)
		}
		if v.ContextPct != c.wantPct {
			t.Errorf("%s: ContextPct = %d, want %d", c.name, v.ContextPct, c.wantPct)
		}
		if v.ContextState != c.wantState {
			t.Errorf("%s: ContextState = %q, want %q", c.name, v.ContextState, c.wantState)
		}
		if v.Recovery != c.wantRecov {
			t.Errorf("%s: Recovery = %q, want %q", c.name, v.Recovery, c.wantRecov)
		}
	}
}

// TestReadModel_ContextPctSentinelSurvives — the -1 "no datum" sentinel is never flattened to 0.
//
// This is the load-bearing non-vacuity proof for the whole file: a field that is NOT declared on
// agentListItem, or NOT copied in the AgentView construction, decodes to Go's int zero value. -1 is
// therefore the one value that cannot be produced by an accidental drop, and 0 is exactly the value
// af-core refuses to emit because it reads as "0% used = healthy" (agents.go, ContextPct doc).
func TestReadModel_ContextPctSentinelSurvives(t *testing.T) {
	js := `[
	  {"name":"x","type":"autonomous","formula":"f","running":true,"status":"working",
	   "step_id":"s1","step_title":"build","step_state":"ready","gate_id":"","inputs":{},
	   "context_pct":-1,"context_state":"none","recovery":"none"}
	]`
	rm := New(fakeLister{json: js}, fakeLiveness{sessions: []string{"af-x"}})
	views, err := rm.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if views[0].ContextPct == 0 {
		t.Fatalf("ContextPct = 0: the -1 sentinel was dropped or flattened; 0 reads as healthy")
	}
	if views[0].ContextPct != -1 {
		t.Fatalf("ContextPct = %d, want -1", views[0].ContextPct)
	}
}

// TestReadModel_ContextStateFiveLiterals — all five reader literals survive verbatim, and
// malformed is never collapsed into none (internal/statusline/observation.go, StateMalformed).
func TestReadModel_ContextStateFiveLiterals(t *testing.T) {
	// The literals are duplicated as strings on purpose: the web module is compiler-forbidden
	// from importing internal/statusline (extractability guard), so this list IS the mirror.
	for _, literal := range []string{"fresh", "stale", "dark", "none", "malformed"} {
		js := `[
		  {"name":"a","type":"autonomous","formula":"f","running":true,"status":"working",
		   "step_id":"s1","step_title":"t","step_state":"ready","gate_id":"","inputs":{},
		   "context_pct":50,"context_state":"` + literal + `","recovery":"none"}
		]`
		rm := New(fakeLister{json: js}, fakeLiveness{sessions: []string{"af-a"}})
		views, err := rm.Assemble(context.Background())
		if err != nil {
			t.Fatalf("%s: Assemble: %v", literal, err)
		}
		if views[0].ContextState != literal {
			t.Errorf("ContextState = %q, want %q (verbatim, never remapped)",
				views[0].ContextState, literal)
		}
	}
}

// TestReadModel_RecoveryPassthrough_StatusNotRederived — the must-not-regress guard.
//
// The whole point of the passthrough (design Decision 13) is that it is NOT a re-derivation: the
// honesty enum keeps its three Phase-0 inputs and the new truth rides on separate fields. A halted,
// context-dark agent whose session is live and whose step is ready must STILL report status
// "working" here — the badge, not the enum, is what stops it reading as fine.
func TestReadModel_RecoveryPassthrough_StatusNotRederived(t *testing.T) {
	js := `[
	  {"name":"h","type":"autonomous","formula":"f","running":true,"status":"working",
	   "step_id":"s1","step_title":"build","step_state":"ready","gate_id":"","inputs":{},
	   "context_pct":99,"context_state":"dark","recovery":"halted"}
	]`
	rm := New(fakeLister{json: js}, fakeLiveness{sessions: []string{"af-h"}})
	views, err := rm.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if views[0].Status != "working" {
		t.Fatalf("Status = %q, want working: status is INHERITED from Phase 0, never re-derived "+
			"off the new fields", views[0].Status)
	}
	if views[0].Recovery != "halted" {
		t.Fatalf("Recovery = %q, want halted", views[0].Recovery)
	}
}

// TestReadModel_ContextVersionSkew_OldAfDegradesToEmpty — an old `af` (pre-Phase-4A) omits the
// three keys entirely. Go decodes the absent int as 0 and the absent strings as "".
//
// This test pins WHY the UI badge must key on the string fields: 0 here is Go's zero value, not
// af's -1 sentinel, and it is indistinguishable from a genuinely healthy 0% reading. The emitted
// domains of context_state and recovery both exclude "" (neither carries omitempty), so "" occurs
// if and only if the producer predates Phase 4A — making it a sound total version probe.
func TestReadModel_ContextVersionSkew_OldAfDegradesToEmpty(t *testing.T) {
	js := `[
	  {"name":"old","type":"autonomous","formula":"f","running":true,"status":"working",
	   "step_id":"s1","step_title":"build","step_state":"ready","gate_id":"","inputs":{}}
	]`
	rm := New(fakeLister{json: js}, fakeLiveness{sessions: []string{"af-old"}})
	views, err := rm.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if views[0].ContextState != "" {
		t.Errorf("ContextState = %q, want \"\" (no datum from a pre-4A af)", views[0].ContextState)
	}
	if views[0].Recovery != "" {
		t.Errorf("Recovery = %q, want \"\" (no datum from a pre-4A af)", views[0].Recovery)
	}
	if views[0].ContextPct != 0 {
		t.Errorf("ContextPct = %d, want 0 (Go's zero value for an absent key)", views[0].ContextPct)
	}
}

// TestReadModel_ContextTagsMirrorAfCore — the af↔web contract is a STRING match, not a type match.
// The module may not import af-core, so the tag spellings are asserted against literals here; if
// af-core ever renames a key, this test is the record of what the web side believed it was called.
func TestReadModel_ContextTagsMirrorAfCore(t *testing.T) {
	for _, key := range []string{`"context_pct"`, `"context_state"`, `"recovery"`} {
		js := `[
		  {"name":"a","type":"autonomous","formula":"f","running":true,"status":"working",
		   "step_id":"s1","step_title":"t","step_state":"ready","gate_id":"","inputs":{},
		   "context_pct":77,"context_state":"stale","recovery":"recovering"}
		]`
		if !strings.Contains(js, key) {
			t.Fatalf("fixture lost the %s key", key)
		}
		rm := New(fakeLister{json: js}, fakeLiveness{sessions: []string{"af-a"}})
		views, err := rm.Assemble(context.Background())
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		v := views[0]
		if v.ContextPct != 77 || v.ContextState != "stale" || v.Recovery != "recovering" {
			t.Fatalf("tag mismatch for %s: got pct=%d state=%q recovery=%q",
				key, v.ContextPct, v.ContextState, v.Recovery)
		}
	}
}
