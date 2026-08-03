package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func opsContainKillSession(ops []string) bool {
	for _, op := range ops {
		if strings.Contains(op, "KillSession") {
			return true
		}
	}
	return false
}

// setAmbientCallerRole drives the K9 interlock's caller-role seam for the duration of a
// (sub)test. The session package's default seam returns "" (library purity); production
// wires it to os.Getenv("AF_ROLE") at the cmd boundary. Restored via t.Cleanup.
func setAmbientCallerRole(t *testing.T, role string) {
	t.Helper()
	orig := ambientCallerRole
	ambientCallerRole = func() string { return role }
	t.Cleanup(func() { ambientCallerRole = orig })
}

// TestStop_AgentContextInterlock exercises the K9 primitive-signal interlock: in agent
// context (AF_ROLE set), Manager.Stop refuses a NON-SELF interactive-type target and does
// NOT kill; a self target and a non-self autonomous specialist both pass through to the
// kill. The interlock uses only inline primitives (AF_ROLE + agentEntry.Type) because
// session cannot import cmd (would cycle) — see the WHY comment in session.go.
func TestStop_AgentContextInterlock(t *testing.T) {
	t.Run("nonself_interactive_refused", func(t *testing.T) {
		fake := installHermeticSession(t)
		mgr := NewManager(t.TempDir(), "manager", config.AgentEntry{Type: "interactive"})
		fake.present[mgr.SessionID()] = true // MUST be running, else ErrNotRunning short-circuits before the interlock
		setAmbientCallerRole(t, "witness")   // non-self agent context

		err := mgr.Stop()
		if err == nil {
			t.Fatal("Stop must refuse a non-self interactive-type target in agent context")
		}
		if opsContainKillSession(fake.ops) {
			t.Errorf("KillSession must NOT be invoked on a refused interactive target; ops=%v", fake.ops)
		}
	})

	t.Run("self_allowed", func(t *testing.T) {
		fake := installHermeticSession(t)
		mgr := NewManager(t.TempDir(), "manager", config.AgentEntry{Type: "interactive"})
		fake.present[mgr.SessionID()] = true
		setAmbientCallerRole(t, "manager") // self target

		if err := mgr.Stop(); err != nil {
			t.Fatalf("self-stop must pass, got %v", err)
		}
		if !opsContainKillSession(fake.ops) {
			t.Errorf("KillSession must be invoked on a self target; ops=%v", fake.ops)
		}
	})

	t.Run("specialist_autonomous_allowed", func(t *testing.T) {
		fake := installHermeticSession(t)
		mgr := NewManager(t.TempDir(), "solver", config.AgentEntry{Type: "autonomous", Formula: "factoryworker"})
		fake.present[mgr.SessionID()] = true
		setAmbientCallerRole(t, "witness") // non-self, but the target is autonomous (not interactive)

		if err := mgr.Stop(); err != nil {
			t.Fatalf("non-self autonomous specialist target must pass, got %v", err)
		}
		if !opsContainKillSession(fake.ops) {
			t.Errorf("KillSession must be invoked on an autonomous target; ops=%v", fake.ops)
		}
	})
}
