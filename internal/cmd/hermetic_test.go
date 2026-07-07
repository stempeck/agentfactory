package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/session"
)

// fakeTmux is the single hermetic tmux double for issue #309 Phase 2. It
// satisfies BOTH the internal/session tmuxClient (14 methods, incl. the #412
// Phase-4 ShowOption read-back and the #508 UnsetEnvironment) and the internal/cmd
// cmdTmux (9 methods) seam interfaces — the 18-method distinct union — recording
// every would-be op in order and returning benign values. It
// performs NO real I/O, never sleeps, and never shells out; a default-suite test
// that installs it via setupHermeticSessions cannot reach the real tmux server.
//
// Per-session liveness is configurable so later phases can drive both the
// healthy (skip) and dead (kill+relaunch) watchdog branches:
//   - present       drives HasSession      (default: absent -> (false, nil))
//   - paneCommand    drives GetPaneCommand  (default: "" -> ("", nil))
//   - running        drives IsAgentRunning  (default: false)
//   - claudeRunning  drives IsClaudeRunning (default: false)
type fakeTmux struct {
	ops           []string
	present       map[string]bool
	paneCommand   map[string]string
	running       map[string]bool
	claudeRunning map[string]bool
}

func newFakeTmux() *fakeTmux {
	return &fakeTmux{
		present:       map[string]bool{},
		paneCommand:   map[string]string{},
		running:       map[string]bool{},
		claudeRunning: map[string]bool{},
	}
}

func (f *fakeTmux) record(op string) { f.ops = append(f.ops, op) }

// --- methods shared by tmuxClient and cmdTmux ---

func (f *fakeTmux) HasSession(name string) (bool, error) {
	f.record("HasSession " + name)
	return f.present[name], nil
}

func (f *fakeTmux) NewSession(name, workDir string) error {
	f.record(fmt.Sprintf("NewSession %s %s", name, workDir))
	return nil
}

func (f *fakeTmux) KillSession(name string) error {
	f.record("KillSession " + name)
	return nil
}

func (f *fakeTmux) SendKeysDelayed(sess, keys string, delayMs int) error {
	f.record(fmt.Sprintf("SendKeysDelayed %s %s %d", sess, keys, delayMs))
	return nil
}

func (f *fakeTmux) SetEnvironment(sess, key, value string) error {
	f.record(fmt.Sprintf("SetEnvironment %s %s=%s", sess, key, value))
	return nil
}

func (f *fakeTmux) UnsetEnvironment(sess, key string) error {
	f.record(fmt.Sprintf("UnsetEnvironment %s %s", sess, key))
	return nil
}

// --- tmuxClient-only methods ---

func (f *fakeTmux) SetOption(sess, name, value string) error {
	f.record(fmt.Sprintf("SetOption %s %s=%s", sess, name, value))
	return nil
}

// ShowOption models a successful apply for this no-op recorder fake: it returns
// "on" so the Issue #412 best-effort mouse read-back in Manager.Start() stays
// silent on the cmd-side hermetic paths (a return of "" would trip the warning).
func (f *fakeTmux) ShowOption(sess, name string) (string, error) {
	f.record(fmt.Sprintf("ShowOption %s %s", sess, name))
	return "on", nil
}

func (f *fakeTmux) IsClaudeRunning(sess string) bool {
	f.record("IsClaudeRunning " + sess)
	return f.claudeRunning[sess]
}

func (f *fakeTmux) WaitForShellReady(sess string, timeout time.Duration) error {
	f.record("WaitForShellReady " + sess)
	return nil
}

func (f *fakeTmux) WaitForCommand(sess string, excludeCommands []string, timeout time.Duration) error {
	f.record("WaitForCommand " + sess)
	return nil
}

func (f *fakeTmux) AcceptBypassPermissionsWarning(sess string) error {
	f.record("AcceptBypassPermissionsWarning " + sess)
	return nil
}

func (f *fakeTmux) NudgeSession(sess, message string) error {
	f.record(fmt.Sprintf("NudgeSession %s %s", sess, message))
	return nil
}

func (f *fakeTmux) SendKeysRaw(sess, keys string) error {
	f.record(fmt.Sprintf("SendKeysRaw %s %s", sess, keys))
	return nil
}

// --- cmdTmux-only methods ---

func (f *fakeTmux) IsAvailable() bool {
	f.record("IsAvailable")
	return true
}

func (f *fakeTmux) SendKeys(sess, keys string) error {
	f.record(fmt.Sprintf("SendKeys %s %s", sess, keys))
	return nil
}

func (f *fakeTmux) GetPaneCommand(sess string) (string, error) {
	f.record("GetPaneCommand " + sess)
	return f.paneCommand[sess], nil
}

func (f *fakeTmux) IsAgentRunning(sess string, expectedPaneCommands ...string) bool {
	f.record("IsAgentRunning " + sess)
	return f.running[sess]
}

// Compile-time proofs: the SINGLE fake satisfies BOTH seam surfaces. The session
// side uses the exported alias because session.tmuxClient is unexported.
var _ cmdTmux = (*fakeTmux)(nil)
var _ session.TmuxForTest = (*fakeTmux)(nil)

// hashName derives a short, deterministic hex tag from a test name so the
// per-test namespace is stable across reruns and collision-free without
// t.Parallel. Derived from t.Name() (never rand/clock) per ADR-018: the seam is
// set directly, never read from the environment.
func hashName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:8]
}

// setupHermeticSessions swaps ALL FOUR seams to a per-test hermetic substrate
// and returns the recording fake plus the in-memory issue store:
//   - sessionPrefixFn -> "af-test-<hex>-" (hex = sha256(t.Name())[:8])
//   - newManagerTmux  -> the fake (via the internal/session exported hook)
//   - newCmdTmux      -> the same fake
//   - newIssueStore   -> memstore (via installMemStore)
//
// Every swap is reverted via t.Cleanup (LIFO). Call this AFTER any t.TempDir()
// so the seam restores run before the temp-dir delete (design R-7). The
// helper-using test MUST NOT call t.Parallel — the seams are package globals.
func setupHermeticSessions(t *testing.T) (*fakeTmux, *memstore.Store) {
	t.Helper()
	fake := newFakeTmux()
	prefix := "af-test-" + hashName(t.Name()) + "-"

	// internal/cmd cannot assign session's unexported sessionPrefixFn /
	// newManagerTmux, so it redirects them through the exported session hook.
	restoreSession := session.InstallHermeticForTest(prefix, func() session.TmuxForTest { return fake })
	t.Cleanup(restoreSession)

	origCmdTmux := newCmdTmux
	newCmdTmux = func() cmdTmux { return fake }
	t.Cleanup(func() { newCmdTmux = origCmdTmux })

	store := installMemStore(t)

	return fake, store
}

func TestSetupHermeticSessions(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	if store == nil {
		t.Fatal("setupHermeticSessions returned a nil memstore")
	}

	// Names resolve under the per-test namespace, never the production name.
	wd := session.WatchdogSessionName()
	if !strings.HasPrefix(wd, "af-test-") {
		t.Fatalf("WatchdogSessionName() = %q, want an af-test-<hex>- prefix", wd)
	}
	if wd == "af-watchdog" {
		t.Fatalf("WatchdogSessionName() leaked the production name %q", wd)
	}

	// The namespace is deterministic from t.Name() (no rand/clock).
	wantPrefix := "af-test-" + hashName(t.Name()) + "-"
	if !strings.HasPrefix(wd, wantPrefix) {
		t.Fatalf("namespace not deterministic: %q lacks prefix %q", wd, wantPrefix)
	}

	// A cmdTmux op records against the af-test name, never a production name.
	tm := newCmdTmux()
	if err := tm.NewSession(wd, "/root"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := tm.HasSession(session.DispatchSessionName()); err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if len(fake.ops) == 0 {
		t.Fatal("fake recorded no ops")
	}
	for _, op := range fake.ops {
		if strings.Contains(op, "af-watchdog") || strings.Contains(op, "af-dispatch") {
			t.Fatalf("recorded op targets a production session name: %q", op)
		}
	}
	foundTestNS := false
	for _, op := range fake.ops {
		if strings.Contains(op, "af-test-") {
			foundTestNS = true
			break
		}
	}
	if !foundTestNS {
		t.Fatalf("no recorded op targeted the af-test- namespace; ops=%v", fake.ops)
	}

	// Liveness is configurable and defaults to absent.
	if ok, _ := fake.HasSession("af-test-probe"); ok {
		t.Fatal("an unset session must default to absent")
	}
	fake.present["af-test-probe"] = true
	if ok, _ := fake.HasSession("af-test-probe"); !ok {
		t.Fatal("present[...]=true must make HasSession return true")
	}
}
