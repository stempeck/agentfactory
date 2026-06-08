package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/session"
)

// Issue #309 Phase 4 (AC-4): watchdog resilience. These tests are hermetic —
// they install the recording fakeTmux via setupHermeticSessions(t) and drive the
// extracted watchdog-launch / root-resolution helpers directly, so they touch no
// real tmux server. Per Phase-3 enforcement they MUST NOT call t.Parallel (the
// seams are package globals) and MUST NOT issue raw exec.Command("tmux", ...).

// opIndex returns the index of the first recorded op equal to want, or -1.
func opIndex(ops []string, want string) int {
	for i, op := range ops {
		if op == want {
			return i
		}
	}
	return -1
}

// hasOp reports whether want was recorded.
func hasOp(ops []string, want string) bool { return opIndex(ops, want) >= 0 }

func newTestCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	return cmd, &buf
}

// TestDeadWatchdogReplaced: a present-but-dead watchdog (W1) must be killed and
// recreated, and the new session must receive AF_ROOT (W2).
func TestDeadWatchdogReplaced(t *testing.T) {
	fake, _ := setupHermeticSessions(t)
	ws := session.WatchdogSessionName()
	root := "/factory/root"

	// Present but NOT live: tmux session exists, but `af` is not running.
	fake.present[ws] = true
	fake.running[ws] = false

	cmd, _ := newTestCmd()
	launchWatchdog(cmd, fake, root)

	killIdx := opIndex(fake.ops, "KillSession "+ws)
	if killIdx < 0 {
		t.Fatalf("dead watchdog must be killed; ops=%v", fake.ops)
	}
	newIdx := opIndex(fake.ops, "NewSession "+ws+" "+root)
	if newIdx < 0 {
		t.Fatalf("dead watchdog must be recreated; ops=%v", fake.ops)
	}
	if killIdx > newIdx {
		t.Fatalf("KillSession must precede NewSession; ops=%v", fake.ops)
	}
	if !hasOp(fake.ops, "SetEnvironment "+ws+" AF_ROOT="+root) {
		t.Fatalf("recreated watchdog must receive AF_ROOT; ops=%v", fake.ops)
	}
}

// TestHealthyWatchdogNotDisturbed: a present AND live watchdog (W1) must be left
// alone — neither killed nor recreated.
func TestHealthyWatchdogNotDisturbed(t *testing.T) {
	fake, _ := setupHermeticSessions(t)
	ws := session.WatchdogSessionName()

	// Present AND live: tmux session exists and `af` is running.
	fake.present[ws] = true
	fake.running[ws] = true
	fake.paneCommand[ws] = "af"

	cmd, _ := newTestCmd()
	launchWatchdog(cmd, fake, "/factory/root")

	if hasOp(fake.ops, "KillSession "+ws) {
		t.Fatalf("healthy watchdog must NOT be killed; ops=%v", fake.ops)
	}
	for _, op := range fake.ops {
		if strings.HasPrefix(op, "NewSession "+ws) {
			t.Fatalf("healthy watchdog must NOT be recreated; ops=%v", fake.ops)
		}
	}
}

// TestWatchdogToleratesMissingCwd: with a valid AF_ROOT, runWatchdog's root
// resolution (W2) must succeed even when the cwd is gone — the condition that
// currently kills the watchdog.
func TestWatchdogToleratesMissingCwd(t *testing.T) {
	// AF_ROOT points at a valid factory root.
	factory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(factory, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factory, ".agentfactory", "factory.json"),
		[]byte(`{"type":"factory","version":1,"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_ROOT", factory)

	// Emulate the deleted-cwd failure mode: chdir into a dir, then remove it so
	// getWd() fails. With a usable AF_ROOT (consulted first), resolution must
	// still succeed without depending on the cwd.
	gone := filepath.Join(t.TempDir(), "deleted")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatalf("removing cwd: %v", err)
	}

	root, err := resolveWatchdogRoot()
	if err != nil {
		t.Fatalf("resolveWatchdogRoot must tolerate a deleted cwd when AF_ROOT is valid; got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".agentfactory", "factory.json")); statErr != nil {
		t.Fatalf("resolved root %q is not the AF_ROOT factory: %v", root, statErr)
	}
}
