package readmodel

import (
	"context"
	"testing"
)

// fakeLister returns canned `af agents list --json` output.
type fakeLister struct {
	json string
	err  error
}

func (f fakeLister) AgentsListJSON(ctx context.Context) (string, error) { return f.json, f.err }

// fakeLiveness returns a canned tmux session set.
type fakeLiveness struct {
	sessions []string
	err      error
}

func (f fakeLiveness) Sessions(ctx context.Context) ([]string, error) { return f.sessions, f.err }

var _ AgentsLister = fakeLister{}
var _ Liveness = fakeLiveness{}

// AC4 — Honest status: a running af-x with step_state "no_formula" renders Idle, not Working.
func TestReadModel_HonestStatus(t *testing.T) {
	// The exact Phase-0 11-key shape (agents.go:70-82); status already honestly "idle".
	js := `[
	  {"name":"x","type":"autonomous","formula":"","running":true,"status":"idle",
	   "step_id":"","step_title":"","step_state":"no_formula","gate_id":"","inputs":{}}
	]`
	rm := New(fakeLister{json: js}, fakeLiveness{sessions: []string{"af-x"}})

	views, err := rm.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("got %d views, want 1", len(views))
	}
	v := views[0]
	if v.Status == "working" {
		t.Fatalf("running+no_formula must NOT render Working")
	}
	if v.Status != "idle" {
		t.Fatalf("status = %q, want idle (surfaced faithfully from Phase-0)", v.Status)
	}
	if !v.Running {
		t.Fatalf("af-x is live in tmux, Running should be true")
	}
	if v.AssembledAt.IsZero() {
		t.Fatalf("AssembledAt must be stamped at assembly (the staleness clock source)")
	}
	// step_id is part of the Phase-0 contract even though the outline's AgentView prose drops it.
	if v.Name != "x" {
		t.Fatalf("name = %q, want x", v.Name)
	}
}

// Negative guard — status absent (empty) + running + no_formula must STILL render Idle, proving
// the read-model never falls back to Working off running==true alone (must-not-regress rule).
func TestReadModel_HonestStatus_NoFallbackToWorking(t *testing.T) {
	js := `[
	  {"name":"y","type":"autonomous","formula":"","running":true,"status":"",
	   "step_id":"","step_title":"","step_state":"no_formula","gate_id":"","inputs":{}}
	]`
	rm := New(fakeLister{json: js}, fakeLiveness{sessions: []string{"af-y"}})
	views, err := rm.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if views[0].Status == "working" {
		t.Fatalf("absent status must not become Working off running alone")
	}
	if views[0].Status != "idle" {
		t.Fatalf("status = %q, want idle (conservative fallback)", views[0].Status)
	}
}

// Branch on .state, not exit code — a {"state":"error",...} payload (exit 0) is surfaced as an
// error, never as a working/idle agent (design-doc Gap 14).
func TestReadModel_ErrorEnvelope(t *testing.T) {
	rm := New(fakeLister{json: `{"state":"error","error":"boom"}`}, fakeLiveness{})
	views, err := rm.Assemble(context.Background())
	if err == nil {
		t.Fatalf("error envelope must surface an error, got nil")
	}
	for _, v := range views {
		if v.Status == "working" {
			t.Fatalf("error envelope must never render a Working agent")
		}
	}
}

// Honesty cross-check — Phase-0 may report running, but if our own fresh tmux probe shows the
// af-<name> session is gone, the agent is Stopped, never Working.
func TestReadModel_NotLiveIsStopped(t *testing.T) {
	js := `[
	  {"name":"z","type":"autonomous","formula":"f","running":true,"status":"working",
	   "step_id":"s1","step_title":"build","step_state":"ready","gate_id":"","inputs":{}}
	]`
	rm := New(fakeLister{json: js}, fakeLiveness{sessions: []string{}}) // no af-z session
	views, err := rm.Assemble(context.Background())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if views[0].Running {
		t.Fatalf("af-z not in tmux ⇒ Running must be false")
	}
	if views[0].Status == "working" {
		t.Fatalf("a session that is not live must not render Working")
	}
	if views[0].Status != "stopped" {
		t.Fatalf("status = %q, want stopped", views[0].Status)
	}
}

// tmux list-sessions parsing (the real live-path helpers) — ErrNoServer/empty ⇒ no agents lit.
func TestSplitSessions(t *testing.T) {
	got := toSet(splitSessions("af-a\naf-b\naf-watchdog"))
	want := map[string]bool{"af-a": true, "af-b": true, "af-watchdog": true}
	for k := range want {
		if !got[k] {
			t.Errorf("missing session %q", k)
		}
	}
	if len(splitSessions("")) != 0 {
		t.Errorf("empty output must yield zero sessions")
	}
	if len(toSet(splitSessions("   "))) != 0 {
		t.Errorf("whitespace-only output must yield zero sessions")
	}
}
