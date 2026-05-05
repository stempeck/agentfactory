//go:build integration

package mcpstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// requirePython3WithDeps skips when python3 is missing or cannot import the
// Phase 4 server's runtime deps. A bare LookPath check is insufficient: the
// venv on a host may have python3 without aiohttp/sqlalchemy and the test
// would otherwise fail at server startup with a noisy traceback.
func requirePython3WithDeps(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if out, err := exec.Command("python3", "-c", "import aiohttp, sqlalchemy").CombinedOutput(); err != nil {
		t.Skipf("python3 missing server deps (aiohttp/sqlalchemy): %s", out)
	}
}

// newFactoryRoot creates a tempdir and symlinks the in-tree py/ package into
// it so the Python subprocess can `python3 -m py.issuestore.server` with
// cmd.Dir set to the tempdir.
func newFactoryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Symlink(
		filepath.Join(findRepoRoot(t), "py"),
		filepath.Join(root, "py"),
	); err != nil {
		t.Fatalf("symlink py/ into %s: %v", root, err)
	}
	return root
}

// readPID returns the PID recorded in .runtime/mcp_server.json. Fails the
// test if the file is missing, malformed, or the PID is non-positive — those
// conditions would otherwise cause a SIGKILL of PID 0 / current process.
func readPID(t *testing.T, factoryRoot string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(factoryRoot, ".runtime", "mcp_server.json"))
	if err != nil {
		t.Fatalf("read endpoint file: %v", err)
	}
	var info struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatalf("parse endpoint file: %v", err)
	}
	if info.PID <= 0 {
		t.Fatalf("endpoint file has invalid pid: %d", info.PID)
	}
	return info.PID
}

// waitForProcessExit polls until kill -0 reports the process is gone or the
// deadline passes. Returns true on exit, false on timeout.
func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestCrashRecovery covers AC-8 (a): a fresh *MCPStore against the same
// factoryRoot must transparently respawn the server when the prior process
// died without removing .runtime/mcp_server.json (SIGKILL leaves the stale
// endpoint file behind — exactly the case discoverOrStart's tryLiveEndpoint
// must reject via the PID liveness check).
func TestCrashRecovery(t *testing.T) {
	requirePython3WithDeps(t)

	root := newFactoryRoot(t)
	t.Cleanup(func() { terminateServer(root) })

	store1, err := mcpstore.New(root, "lifecycle-crash")
	if err != nil {
		t.Fatalf("first mcpstore.New: %v", err)
	}
	seed, err := store1.Create(context.Background(), issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "before crash",
	})
	if err != nil {
		t.Fatalf("first store Create: %v", err)
	}

	pid := readPID(t, root)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL pid %d: %v", pid, err)
	}
	if !waitForProcessExit(pid, 5*time.Second) {
		t.Fatalf("server pid %d did not exit after SIGKILL", pid)
	}

	// Stale endpoint file is still present (SIGKILL bypasses cleanup). The
	// new store must detect the dead PID and respawn.
	store2, err := mcpstore.New(root, "lifecycle-crash")
	if err != nil {
		t.Fatalf("post-crash mcpstore.New: %v", err)
	}

	newPID := readPID(t, root)
	if newPID == pid {
		t.Fatalf("expected fresh server PID after crash, got same pid %d", pid)
	}

	// AC-8(a): data written pre-crash must survive. Proves SQLite WAL is
	// durable across a SIGKILL'd server, not just that a fresh server can
	// be spawned. Also proves the respawned server points at the same DB.
	got, err := store2.Get(context.Background(), seed.ID)
	if err != nil {
		t.Fatalf("post-crash Get(%s): %v", seed.ID, err)
	}
	if got.Title != "before crash" {
		t.Errorf("pre-crash data lost: got title %q, want %q", got.Title, "before crash")
	}

	// And the respawned server must accept new writes.
	if _, err := store2.Create(context.Background(), issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "after crash",
	}); err != nil {
		t.Fatalf("post-crash Create: %v", err)
	}
}

// TestSIGTERMCleanShutdown covers AC-8 (b): a SIGTERM'd server removes its
// endpoint file before exiting. The Python signal handler in
// py/issuestore/server.py disposes the SQLAlchemy engine (flushes WAL) and
// unlinks .runtime/mcp_server.json — leaving the file behind would cause
// later starts to attempt a (failed) liveness probe instead of a clean spawn.
func TestSIGTERMCleanShutdown(t *testing.T) {
	requirePython3WithDeps(t)

	root := newFactoryRoot(t)

	store, err := mcpstore.New(root, "lifecycle-term")
	if err != nil {
		t.Fatalf("mcpstore.New: %v", err)
	}
	seeds := make([]issuestore.Issue, 0, 3)
	for i := 0; i < 3; i++ {
		iss, err := store.Create(context.Background(), issuestore.CreateParams{
			Type:  issuestore.TypeTask,
			Title: "before term",
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		seeds = append(seeds, iss)
	}

	pid := readPID(t, root)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM pid %d: %v", pid, err)
	}
	if !waitForProcessExit(pid, 5*time.Second) {
		t.Fatalf("server pid %d did not exit after SIGTERM", pid)
	}

	epFile := filepath.Join(root, ".runtime", "mcp_server.json")
	if _, err := os.Stat(epFile); !os.IsNotExist(err) {
		t.Errorf("endpoint file should be removed by SIGTERM handler; stat err: %v", err)
	}

	// AC-8(b): SIGTERM must flush WAL before exit. A fresh server over the
	// same factoryRoot must see all three pre-SIGTERM writes — an unflushed
	// WAL would manifest as missing or short-titled issues on reopen.
	store2, err := mcpstore.New(root, "lifecycle-term")
	if err != nil {
		t.Fatalf("post-SIGTERM mcpstore.New: %v", err)
	}
	t.Cleanup(func() { terminateServer(root) })

	for _, seed := range seeds {
		got, err := store2.Get(context.Background(), seed.ID)
		if err != nil {
			t.Fatalf("post-SIGTERM Get(%s) — WAL flush likely failed: %v", seed.ID, err)
		}
		if got.Title != "before term" {
			t.Errorf("post-SIGTERM Get(%s): title=%q want %q", seed.ID, got.Title, "before term")
		}
	}
}
