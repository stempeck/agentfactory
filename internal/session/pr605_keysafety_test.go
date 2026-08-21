package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestUniverseHygiene_NeverUnsetsShellCriticalVar pins PR #605 T-P1 (the blocker). PATH is a
// well-formed identifier, so F1's key-shape check cannot catch it — only the cleanup-side
// protected-name guard can. If the universe hygiene emits `unset PATH`, the respawn line
// becomes `… && unset PATH && claude`, and the bare `claude` can no longer be resolved: the
// agent never relaunches. Both twins must refuse it.
func TestUniverseHygiene_NeverUnsetsShellCriticalVar(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL", "PATH"})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	if hasOp(fake.ops, "UnsetEnvironment "+mgr.SessionID()+" PATH") {
		t.Errorf("universe hygiene must never unset PATH at the tmux twin; ops=%v", fake.ops)
	}
	inline := mgr.BuildStartupCommand()
	if hasUnsetToken(inline, "PATH") {
		t.Errorf("universe hygiene must never emit `unset PATH` inline (breaks bare `claude` resolution); got: %s", inline)
	}
	assertShellParses(t, respawnPrefix+inline)
}

// TestUniverseHygiene_NeverEmitsMalformedKey pins the malformed-key half of T-P1/T-F1 at the
// emission boundary: a profile key that is not a safe shell identifier must never reach the
// `unset` segment (where names ride in unquoted), on either twin.
func TestUniverseHygiene_NeverEmitsMalformedKey(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	const semi, space = "BAD;KEY", "BAD KEY"
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL", semi, space})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	inline := mgr.BuildStartupCommand()
	for _, bad := range []string{semi, space} {
		if strings.Contains(inline, bad) {
			t.Errorf("universe hygiene must never emit malformed key %q into the launch line; got: %s", bad, inline)
		}
		if hasOp(fake.ops, "UnsetEnvironment "+mgr.SessionID()+" "+bad) {
			t.Errorf("universe hygiene must never unset malformed key %q at the tmux twin; ops=%v", bad, fake.ops)
		}
	}
	assertShellParses(t, respawnPrefix+inline)
}

// TestUniverseHygiene_ShrunkenUniverse_LeavesKeyUncleared pins PR #605 T-F6's design-ACCEPTED
// residual (design-doc.md:50,209): a key deleted from EVERY profile leaves the universe, so the
// hygiene no longer clears it and a still-set session value survives until `af down && af up`.
// With the key absent from the handed universe, NO clear may be emitted. This is a
// characterization pin — it must stay GREEN; a manifest-style "fix" that cleared keys outside
// the universe (the design DECLINED it, Candidate 1) would turn this RED, which is the point.
func TestUniverseHygiene_ShrunkenUniverse_LeavesKeyUncleared(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL"}) // universeTestKey has LEFT the universe

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	if hasOp(fake.ops, "UnsetEnvironment "+mgr.SessionID()+" "+universeTestKey) {
		t.Errorf("a key that left the universe must NOT be cleared at the tmux twin (accepted residual); ops=%v", fake.ops)
	}
	inline := mgr.BuildStartupCommand()
	if hasUnsetToken(inline, universeTestKey) {
		t.Errorf("a key that left the universe must NOT be inline-unset (accepted residual); got: %s", inline)
	}
}
