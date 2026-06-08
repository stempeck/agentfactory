package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// fakeTmux mirrors the canonical hermetic fake in internal/cmd/hermetic_test.go.
// Go does NOT compile _test.go symbols into a package's importable surface, so
// internal/session cannot import the cmd-side fake; this is the documented
// duplicate the design permits (Round-2 LOW-1: "ONE fake; accept documented
// duplication only if cross-package visibility makes a single shared type
// impossible"). It implements the same 15-method union and configurable liveness
// fields; here it is consumed as a tmuxClient. Like its twin, it records ops and
// performs NO real I/O, never sleeps, and never shells out.
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

// --- tmuxClient-only methods ---

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

// --- cmdTmux-only methods (unused here; kept identical to the cmd twin) ---

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

// Compile-time proof: the session-side fake satisfies the session tmuxClient seam.
var _ tmuxClient = (*fakeTmux)(nil)

// hashName derives a deterministic hex tag from a test name (see the cmd twin).
func hashName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:8]
}

// installHermeticSession redirects the two session seams to a per-test namespace
// + the fake, reverting via t.Cleanup. Session-package tests use this directly;
// internal/cmd reaches the same redirect through the exported InstallHermeticForTest.
func installHermeticSession(t *testing.T) *fakeTmux {
	t.Helper()
	fake := newFakeTmux()
	prefix := "af-test-" + hashName(t.Name()) + "-"
	restore := InstallHermeticForTest(prefix, func() TmuxForTest { return fake })
	t.Cleanup(restore)
	return fake
}

func TestInstallHermeticForTest(t *testing.T) {
	fake := installHermeticSession(t)

	wd := WatchdogSessionName()
	if !strings.HasPrefix(wd, "af-test-") {
		t.Fatalf("WatchdogSessionName() = %q, want an af-test-<hex>- prefix", wd)
	}
	if wd == "af-watchdog" {
		t.Fatalf("WatchdogSessionName() leaked the production name %q", wd)
	}

	// NewManager must use the redirected newManagerTmux (i.e. the hermetic fake).
	m := NewManager("/root", "manager", config.AgentEntry{})
	if _, ok := m.tmux.(*fakeTmux); !ok {
		t.Fatalf("NewManager did not use the hermetic fake; got %T", m.tmux)
	}
	if m.tmux.(*fakeTmux) != fake {
		t.Fatal("NewManager used a different fake instance than the one installed")
	}
}
