package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// startMouseAgent provisions a hermetic Manager.Start() path for the mouse tests:
// it stubs the pre-flight memory floor, installs the recording fake (with the
// given configuration applied), provisions a worktree-style workspace, and returns
// the wired Manager. The caller drives Start() and inspects the fake / stderr.
func startMouseAgent(t *testing.T, configure func(*fakeTmux)) (*Manager, *fakeTmux) {
	t.Helper()

	origMem := checkAvailableMemoryFunc
	checkAvailableMemoryFunc = func() (uint64, error) { return 100000, nil }
	t.Cleanup(func() { checkAvailableMemoryFunc = origMem })

	fake := newFakeTmux()
	if configure != nil {
		configure(fake)
	}
	restore := InstallHermeticForTest("af-test-", func() TmuxForTest { return fake })
	t.Cleanup(restore)

	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")
	agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "mouseagent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("creating agent dir: %v", err)
	}

	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager(tmpDir, "mouseagent", entry)
	if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}
	return mgr, fake
}

// captureStderr redirects os.Stderr through an os.Pipe while fn runs and returns
// what was written. Same idiom as internal/cmd/install_test.go. Safe here because
// the session package never calls t.Parallel (the hermetic seam is a package
// global), so swapping the global os.Stderr cannot race a sibling test.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	fn()

	_ = w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

// TestStart_AppliesMouseOption proves Fix A (Issue #412): Manager.Start() applies
// `mouse on` (session-scoped) to the agent session via the best-effort block. It
// drives the full Start() path through the recording hermetic fake (no real tmux),
// with a provisioned t.TempDir() workspace and the 512MB pre-flight floor stubbed,
// then asserts the fake recorded `SetOption <sessionID> mouse=on`.
func TestStart_AppliesMouseOption(t *testing.T) {
	// Stub the pre-flight memory floor so Start() reaches a clean return on any host.
	origMem := checkAvailableMemoryFunc
	checkAvailableMemoryFunc = func() (uint64, error) { return 100000, nil }
	t.Cleanup(func() { checkAvailableMemoryFunc = origMem })

	// Inject the recording fake and a test-scoped session prefix.
	fake := newFakeTmux()
	restore := InstallHermeticForTest("af-test-", func() TmuxForTest { return fake })
	t.Cleanup(restore)

	// Provision a worktree-style workspace so Start() passes ErrWorktreeNotSet and
	// ErrNotProvisioned (mirrors session_integration_test.go's TestStartAndStop).
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")
	agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "mouseagent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("creating agent dir: %v", err)
	}

	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager(tmpDir, "mouseagent", entry)
	if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	sessionID := mgr.SessionID()
	want := "SetOption " + sessionID + " mouse=on"
	for _, op := range fake.ops {
		if op == want {
			return // PASS
		}
	}
	t.Fatalf("Start() did not apply mouse on: want recorded op %q, got ops=%v", want, fake.ops)
}

// TestStart_WarnsWhenMouseOptionDidNotTake proves Phase 4 File 4 (Issue #412 Gap 7):
// when the best-effort `mouse on` apply silently fails to take, Manager.Start()
// reads the option back and emits exactly one stderr `warning:` — and still returns
// success, because the read-back must NEVER abort session creation.
func TestStart_WarnsWhenMouseOptionDidNotTake(t *testing.T) {
	mgr, _ := startMouseAgent(t, func(f *fakeTmux) {
		f.suppressOption["mouse"] = true // simulate a silent apply failure
	})

	var startErr error
	out := captureStderr(t, func() { startErr = mgr.Start() })

	// The read-back must NOT abort session creation.
	if startErr != nil {
		t.Fatalf("Start() must not abort on a silent mouse-apply failure, got: %v", startErr)
	}

	// Exactly one warning, and it names the session.
	if n := strings.Count(out, "warning: mouse option did not take"); n != 1 {
		t.Fatalf("want exactly one mouse read-back warning, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, mgr.SessionID()) {
		t.Errorf("read-back warning should name the session %q:\n%s", mgr.SessionID(), out)
	}
}

// TestStart_NoWarnWhenMouseOptionTakes proves the read-back is silent on success:
// when `mouse on` applies cleanly (the default fake echoes the set value back),
// Start() emits no mouse warning. Guards against a read-back that cries wolf.
func TestStart_NoWarnWhenMouseOptionTakes(t *testing.T) {
	mgr, _ := startMouseAgent(t, nil) // default: SetOption stores; ShowOption returns "on"

	var startErr error
	out := captureStderr(t, func() { startErr = mgr.Start() })

	if startErr != nil {
		t.Fatalf("Start() unexpected error: %v", startErr)
	}
	if strings.Contains(out, "warning: mouse option did not take") {
		t.Errorf("expected no mouse warning on a successful apply, got:\n%s", out)
	}
}
