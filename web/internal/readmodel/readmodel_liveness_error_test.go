package readmodel

import (
	"context"
	"errors"
	"testing"
)

// TestReadModel_LivenessError_FallsBackToPhase0Running locks in the T3 fix: when the
// tmux liveness probe fails UNEXPECTEDLY (a non-nil error that is NOT the benign
// no-server case, which TmuxLiveness already maps to (nil,nil)), the read model must
// fall back to Phase-0's honest status instead of silently forcing EVERY agent to
// "stopped" — which would lie that running agents are dead and tempt an operator to
// reset agents that are actively running formulas.
func TestReadModel_LivenessError_FallsBackToPhase0Running(t *testing.T) {
	js := `[
	  {"name":"z","type":"autonomous","formula":"f","running":true,"status":"working",
	   "step_id":"s1","step_title":"build","step_state":"ready","gate_id":"","inputs":{}}
	]`
	rm := New(fakeLister{json: js}, fakeLiveness{err: errors.New("tmux probe failed")})

	views, err := rm.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("got %d views, want 1", len(views))
	}
	if !views[0].Running {
		t.Fatalf("a liveness probe error must fall back to Phase-0 running=true, got Running=false (every agent was forced stopped)")
	}
	if views[0].Status == "stopped" {
		t.Fatalf("a liveness probe error must not override the honest Phase-0 status to %q", "stopped")
	}
}
