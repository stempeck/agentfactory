//go:build integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestT_INT_7_NestedCloneCaptureRefused is the binary-level (#519 Phase 3) twin of
// the unit-level TestT_INT_3_NestedCloneRefusal: it reproduces the real incident —
// a git clone of a factory, nested inside another factory's agent dir — and asserts
// the BUILT `af sling` refuses a state-writing dispatch rather than silently
// capturing it into the wrong factory.
//
// Two branches, both non-zero exit:
//   - AF_ROOT=<outer>  → the Phase-2 cross-check mismatch refusal (names both roots).
//   - AF_ROOT unset    → the Phase-3 K5 env-less enclosing refusal (the new hole this
//     phase closes), which also offers the `set AF_ROOT=<clone> to affirm` hatch.
//
// The refusal fires in resolveInvokerRoot (sling.go:105) BEFORE any store or tmux
// op, so no Python/MCP server is needed and (cross-review H2) a sentinel tmux
// session OUTSIDE the fixture must survive the refused sling — proof the wrong-root
// dispatch never reached a session-kill.
func TestT_INT_7_NestedCloneCaptureRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	binary := buildAF(t)

	// Outer factory: a git repo whose committed tree carries .agentfactory/factory.json.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	outer := filepath.Join(base, "outer")
	if err := os.MkdirAll(config.StoreDir(outer), 0o755); err != nil {
		t.Fatalf("mkdir outer store: %v", err)
	}
	if err := os.WriteFile(config.FactoryConfigPath(outer), []byte(`{"type":"factory","version":1,"name":"outer"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write outer factory.json: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
		{"add", "-A"},
		{"commit", "-q", "-m", "outer factory"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = outer
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// Nested clone: git clone the outer factory into an agent dir deep inside it, so
	// the clone is ITSELF a factory (its own factory.json) enclosed by outer.
	agentParent := filepath.Join(outer, ".agentfactory", "agents", "x")
	if err := os.MkdirAll(agentParent, 0o755); err != nil {
		t.Fatalf("mkdir agent parent: %v", err)
	}
	clone := filepath.Join(agentParent, "clone")
	cloneCmd := exec.Command("git", "clone", "-q", outer, clone)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %s\n%s", err, out)
	}
	if _, err := os.Stat(config.FactoryConfigPath(clone)); err != nil {
		t.Fatalf("clone must be a factory (own factory.json): %v", err)
	}

	// Sentinel session OUTSIDE the fixture (H2): it must survive the refused sling.
	sentinel := "af-tint7-outside-sentinel"
	exec.Command("tmux", "kill-session", "-t", "="+sentinel).Run() // clear any stale one
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", sentinel, "-c", base).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session sentinel: %s\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", "="+sentinel).Run() })
	sentinelAlive := func() bool {
		return exec.Command("tmux", "has-session", "-t", "="+sentinel).Run() == nil
	}

	// runClone runs the built `af sling --no-launch` from inside the clone with a
	// bespoke env (runAFMayFail sets only HOME, never AF_ROOT). Any ambient AF_ROOT is
	// stripped first, so afRoot=="" is a TRUE env-less shell regardless of the caller's
	// environment (correctness independent of NeutralizeAFEnv).
	runClone := func(afRoot string) (string, error) {
		env := make([]string, 0, len(os.Environ())+2)
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "AF_ROOT=") {
				continue
			}
			env = append(env, kv)
		}
		env = append(env, "HOME="+clone)
		if afRoot != "" {
			env = append(env, "AF_ROOT="+afRoot)
		}
		cmd := exec.Command(binary, "sling", "--formula", "does-not-matter", "--no-launch")
		cmd.Dir = clone
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Run("AF_ROOT=outer => cross-check mismatch refusal", func(t *testing.T) {
		out, err := runClone(outer)
		if err == nil {
			t.Fatalf("expected non-zero exit (mismatch refusal), got success\noutput: %s", out)
		}
		if !strings.Contains(out, "factory root mismatch") {
			t.Errorf("output missing mismatch head: %s", out)
		}
		if !strings.Contains(out, clone) || !strings.Contains(out, outer) {
			t.Errorf("refusal must name BOTH roots (clone %q, outer %q): %s", clone, outer, out)
		}
		if !sentinelAlive() {
			t.Errorf("H2 violation: sentinel session outside the fixture was stopped by a refused sling")
		}
	})

	t.Run("AF_ROOT unset => env-less enclosing refusal (K5)", func(t *testing.T) {
		out, err := runClone("")
		if err == nil {
			t.Fatalf("expected non-zero exit (enclosing refusal), got success\noutput: %s", out)
		}
		if !strings.Contains(out, "enclosed by") {
			t.Errorf("output missing enclosing-refusal wording: %s", out)
		}
		if !strings.Contains(out, clone) || !strings.Contains(out, outer) {
			t.Errorf("refusal must name BOTH roots (clone %q, outer %q): %s", clone, outer, out)
		}
		if !strings.Contains(out, "set AF_ROOT="+clone) {
			t.Errorf("refusal must offer the affirm hatch 'set AF_ROOT=%s': %s", clone, out)
		}
		if !sentinelAlive() {
			t.Errorf("H2 violation: sentinel session outside the fixture was stopped by a refused sling")
		}
	})
}
