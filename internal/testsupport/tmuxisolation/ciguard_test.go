package tmuxisolation

import "testing"

// TestCheckCIOnly is the isolated unit test for issue #389 (AC#6). It drives the
// PURE decision function with synthetic cwd values — never the os.Exit wrapper — so it
// runs safely under `make test` (no integration build tag, no real resources, no
// integration suite invoked).
func TestCheckCIOnly(t *testing.T) {
	cases := []struct {
		name    string
		cwd     string
		blocked bool
	}{
		// BLOCKS — cwd under a factory worktree (the live-factory signal; immune to NeutralizeAFEnv).
		{"cwd under worktrees", "/home/dev/af/agentfactory-pro/.agentfactory/worktrees/wt-abc123/internal/cmd", true},
		{"cwd under worktrees, trailing", "/home/x/proj/.agentfactory/worktrees/wt-abc123", true},
		// ALLOWS — a checkout not under a factory worktree (the CI / clean-human path).
		{"clean CI checkout", "/home/runner/work/agentfactory-pro/agentfactory-pro/internal/cmd", false},
		{"human clean checkout", "/home/alice/code/agentfactory-pro/internal/session", false},
		// ALLOWS — a repo literally named "worktrees" must not false-positive (leading slash + dotdir guard it).
		{"unrelated worktrees dir", "/home/x/my-worktrees/project", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCIOnly(tc.cwd)
			if tc.blocked {
				if err == nil {
					t.Fatalf("checkCIOnly(%q): want blocked, got nil", tc.cwd)
				}
				if err.Error() != blockedMessage {
					t.Fatalf("checkCIOnly(%q): message drift: got %q, want %q", tc.cwd, err.Error(), blockedMessage)
				}
			} else if err != nil {
				t.Fatalf("checkCIOnly(%q): want allowed, got blocked: %v", tc.cwd, err)
			}
		})
	}
}

// TestEnvIsNotASignal pins the PR #390 fix: live-session AF_* env vars are NO LONGER a
// block signal — only a CWD under a factory worktree is. This is the exact regression
// TestEnvFamilyDifferentialProbe (#327) tripped: that probe re-execs the integration
// binary with the whole AF_* family set to junk to verify NeutralizeAFEnv, and the old
// secondary signal blocked the child before NeutralizeAFEnv ran. Detection is CWD-only,
// so even with every live AF_* var set in the real process env, a clean (non-worktree)
// cwd must be allowed — while a worktree cwd must still block.
func TestEnvIsNotASignal(t *testing.T) {
	for _, k := range []string{"AF_ROOT", "AF_ROLE", "AF_ACTOR", "AF_WORKTREE", "AF_WORKTREE_ID"} {
		t.Setenv(k, "ultra-implement-327-junk")
	}
	// Clean (non-worktree) cwd: allowed despite the live AF_* env (the probe-child case).
	if err := checkCIOnly("/home/runner/work/agentfactory-pro/agentfactory-pro/internal/cmd"); err != nil {
		t.Fatalf("env must not be a live-factory signal after #390: got blocked: %v", err)
	}
	// Worktree cwd: the load-bearing signal must still block, even with AF_* set.
	if err := checkCIOnly("/home/x/proj/.agentfactory/worktrees/wt-1/internal/cmd"); err == nil {
		t.Fatal("cwd under .agentfactory/worktrees/ must still block even with AF_* set")
	}
}

// TestBlockedMessageExactWording pins the CEO-specified literal so neither the
// constant nor the assertions above can drift silently (the apostrophe is load-bearing).
func TestBlockedMessageExactWording(t *testing.T) {
	const want = "agents are not allowed to run this, it's CI only"
	if blockedMessage != want {
		t.Fatalf("blockedMessage = %q, want %q", blockedMessage, want)
	}
}
