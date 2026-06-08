//go:build integration

package mcpstore_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// TestOrphanServerBackstop is the behavioral proof for #309 Phase 5 (the AC-3
// interrupt clause / 185-orphan root cause). It spawns a REAL server in a temp
// factory root and asserts both OS-level backstops:
//
//  1. SelfExitsOnEndpointLoss — removing the endpoint file WITHOUT signalling
//     the process makes the Python watcher set _shutdown_event so the server
//     exits on its own. This is the ONLY backstop that survives a SIGKILL of
//     the parent `go test`, where Go-side t.Cleanup / Stop never runs.
//  2. StopReapsProcessGroup — MCPStore.Stop() group-kills a server this adapter
//     spawned (setProcGroup makes the recorded PID the group leader).
//
// Reuses the in-package integration helpers (requirePython3WithDeps,
// newFactoryRoot, readPID, waitForProcessExit) — do not redeclare them. Named
// TestOrphan* to match the AC-3 `-run 'Orphan'` filter.
func TestOrphanServerBackstop(t *testing.T) {
	requirePython3WithDeps(t)

	t.Run("SelfExitsOnEndpointLoss", func(t *testing.T) {
		root := newFactoryRoot(t)
		store, err := mcpstore.New(root, "orphan-selfexit")
		if err != nil {
			t.Fatalf("mcpstore.New: %v", err)
		}
		pid := readPID(t, root)
		// Belt-and-suspenders: if self-exit regresses, don't leave the very
		// orphan this test hunts. Group-SIGKILL the captured pid at teardown
		// (ESRCH once it has already exited is harmless).
		t.Cleanup(func() {
			_ = store.Stop()
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})

		// Simulate the interrupt: delete the endpoint file with NO signal.
		// This is the surrogate for temp-root deletion / SIGKILL-of-parent —
		// the case where nothing on the Go side ever signals the server.
		epFile := filepath.Join(root, ".runtime", "mcp_server.json")
		if err := os.Remove(epFile); err != nil {
			t.Fatalf("remove endpoint file: %v", err)
		}

		if !waitForProcessExit(pid, 20*time.Second) {
			t.Fatalf("server pid %d did not self-exit after its endpoint file was removed "+
				"(cwd/endpoint watcher missing or not wired to _shutdown_event)", pid)
		}
	})

	t.Run("StopReapsProcessGroup", func(t *testing.T) {
		root := newFactoryRoot(t)
		store, err := mcpstore.New(root, "orphan-stop")
		if err != nil {
			t.Fatalf("mcpstore.New: %v", err)
		}
		pid := readPID(t, root)
		t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

		if err := store.Stop(); err != nil {
			t.Fatalf("store.Stop(): %v", err)
		}
		if !waitForProcessExit(pid, 10*time.Second) {
			t.Fatalf("server pid %d did not exit after Stop() reaped its process group", pid)
		}
	})
}
