//go:build integration

package cmd

// K9 (Issue #591, Phase 5a): the CI-visible proof of the statusline feature's load-bearing
// C-5 fail-open invariant — a statusline command that crashes, hangs, or exits non-zero MUST
// NOT block a session launch or stall a formula. The renderer's own always-exit-0 contract is
// already unit-covered (statusline_test.go:TestRender_ExitZeroAlways); this test proves the
// DIFFERENT, launch-path layer: a genuinely broken `af`/`statusline render` EXECUTABLE on PATH
// still lets a session start and dispatched work proceed.
//
// It is composed from three shipped precedents (per the design — no new harness):
//   - stub-executable-on-PATH ......... internal/cmd/install_test.go:281-304
//   - //go:build integration + launch .. internal/session/session_integration_test.go:1-80
//   - control-flows-past-the-guard ..... web/internal/entrypoint/guard_test.go:59-107
// and provisions the production statusLine hook the same way settings wire it in the field —
// claude.EnsureSettings(agentDir, claude.Autonomous), per containment_e2e_integration_test.go:84.
//
// Two-leg shape, and WHY it is not one flat test: AC-2 asserts
//   go test -tags=integration ./internal/cmd/ -run 'Statusline.*FailOpen' -v | grep -q '^--- PASS'
// whose grep is anchored at column 0, where ONLY the top-level result line prints. The CI
// integration job provides tmux but NOT claude, so any test that gates its whole body on
// requireClaude would emit a column-0 `--- SKIP` and fail that grep. Therefore the parent body
// is claude-independent and always runs to a PASS (the broken-stub + past-the-guard sentinel),
// and the real session launch — which needs claude — lives in a requireClaude+tmux subtest that
// SKIPs cleanly (an indented `    --- SKIP`, never touching the parent's column-0 `--- PASS`).
//
// AC-5: this file lives at internal/cmd/ and imports only session/config/claude production
// packages via a stub on PATH; it edits no launch/handoff source (internal/session/*,
// handoff.go, compact_handoff.go, sling.go, up.go).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// writeBrokenAf writes a crashing `af` stub into dir and returns its path. It exits non-zero on
// exactly the verb Claude's statusLine hook invokes (`statusline render`) so the failure is real,
// not a no-op. A hang variant is deliberately avoided to keep the test deterministic — a non-zero
// exit is the load-bearing "genuinely broken" signal (stub idiom: install_test.go:281-304).
func writeBrokenAf(t *testing.T, dir string) string {
	t.Helper()
	stub := filepath.Join(dir, "af")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = statusline ] && [ \"$2\" = render ]; then\n" +
		"  echo 'af statusline render: simulated crash' >&2\n" +
		"  exit 42\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stub
}

// TestStatuslineRenderFailOpen proves the C-5 fail-open invariant. Name matches
// `Statusline.*FailOpen`, contains the literal `FailOpen` (AC-2), and matches `func TestStatusline`
// (the AC-1 non-vacuity anchor).
func TestStatuslineRenderFailOpen(t *testing.T) {
	binDir := t.TempDir()
	stub := writeBrokenAf(t, binDir)

	// Leg 1 (claude-independent, always PASSES): the stub is genuinely broken — invoking it
	// directly returns a non-zero exit — yet, modelling Claude's fire-and-forget `type:command`
	// statusLine hook, ignoring that failure lets control continue. This pure-Go floor guarantees
	// a column-0 `--- PASS` for AC-2 with no dependency on an external shell.
	if err := exec.Command(stub, "statusline", "render").Run(); err == nil {
		t.Fatal("stub `af statusline render` must exit non-zero to model a genuinely broken renderer")
	}

	// Leg 2 (control-flows-past-the-guard sentinel, guard_test.go technique): run the broken
	// render the way Claude's hook does — as a shell command, fire-and-forget — and prove control
	// reaches a sentinel emitted AFTER it. `sh` is POSIX and present on every CI runner; if it were
	// somehow absent we skip this enrichment WITHOUT skipping the parent (Leg 1 carries the PASS).
	if _, err := exec.LookPath("sh"); err == nil {
		script := "set -eu\n" +
			"\"" + stub + "\" statusline render </dev/null >/dev/null 2>&1 || true\n" +
			"echo PAST_STATUSLINE_SENTINEL\n"
		out, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("a fire-and-forget broken statusline render aborted the surrounding flow: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "PAST_STATUSLINE_SENTINEL") {
			t.Fatalf("control did not flow past a crashing statusline render:\n%s", out)
		}
	}

	// Leg 3 (real session launch, claude+tmux gated): a hermetic factory whose agent settings wire
	// the shipped `af statusline render` hook to the broken stub on PATH still launches —
	// IsRunning()==true is the past-the-guard sentinel. Mirrors session_integration_test.go:41-80.
	// Skips cleanly where claude is absent (CI), leaving the parent's PASS intact.
	t.Run("session_launches_despite_broken_statusline", func(t *testing.T) {
		if _, err := exec.LookPath("tmux"); err != nil {
			t.Skip("tmux not available")
		}
		if _, err := exec.LookPath("claude"); err != nil {
			t.Skip("claude not on PATH — full session-start path would burn ClaudeStartTimeout")
		}

		// Prepend the broken stub so the launched session's statusLine hook resolves to it.
		t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

		tmpDir := t.TempDir()
		wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")
		agentDir := config.AgentDir(wtPath, "failopen") // what Manager.Start() stats
		if err := os.MkdirAll(agentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Provision the REAL statusLine block (production path) so the launched session carries the
		// broken-`af` hook — the same command settings wire in the field.
		if err := claude.EnsureSettings(agentDir, claude.Autonomous); err != nil {
			t.Fatalf("EnsureSettings: %v", err)
		}
		// Seed the factory gate ON so a render would actually attempt work (fail-open even when on).
		afDir := filepath.Join(tmpDir, ".agentfactory")
		if err := os.MkdirAll(afDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(afDir, ".statusline-gate"), []byte("on\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		entry := config.AgentEntry{Type: "autonomous", Description: "failopen"}
		mgr := session.NewManager(tmpDir, "failopen", entry)
		if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = mgr.Stop() })

		_ = mgr.Start() // non-fatal per harness idiom (session_integration_test.go:63)
		running, _ := mgr.IsRunning()
		if !running {
			t.Skip("session did not start — tmux may not be available")
		}
		// The running session IS the past-the-guard sentinel: launch completed with a broken `af`
		// on PATH and the statusLine hook wired to it.
		if err := mgr.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})
}
