package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// writeDispatchJSON writes root/.agentfactory/dispatch.json with the given body.
func writeDispatchJSON(t *testing.T, root, body string) {
	t.Helper()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "dispatch.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func opRecorded(ops []string, substr string) bool {
	for _, op := range ops {
		if strings.Contains(op, substr) {
			return true
		}
	}
	return false
}

// startDispatch is benign when the dispatcher session is already present: it
// must not create a new session and must not error (the af-up path, distinct
// from the strict CLI error in TestDispatchStart_AlreadyRunning).
func TestStartDispatch_AlreadyRunningBenign(t *testing.T) {
	root := t.TempDir()
	writeDispatchJSON(t, root, `{"repos":["test/repo"],"trigger_label":"agentic","mappings":[{"label":"test","agent":"mgr"}],"interval_seconds":300}`)

	fake, _ := setupHermeticSessions(t)
	fake.present[dispatchSessionName] = true

	if err := startDispatch(&cobra.Command{}, root, fake); err != nil {
		t.Fatalf("startDispatch already-running should be benign, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("startDispatch must NOT create a session when already running; ops=%v", fake.ops)
	}
}

// HIGH-1: an absent dispatch.json (ErrNotFound) must friendly-skip, not abort.
func TestStartDispatch_AbsentConfigFriendlySkip(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	// No dispatch.json written.

	fake, _ := setupHermeticSessions(t)

	if err := startDispatch(&cobra.Command{}, root, fake); err != nil {
		t.Fatalf("absent dispatch.json must friendly-skip, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("absent config must not launch a session; ops=%v", fake.ops)
	}
}

// HIGH-1: the install-default dispatch.json (empty repos + empty mappings) fails
// validateDispatchConfig with ErrMissingField and must also friendly-skip.
func TestStartDispatch_EmptyDefaultConfigFriendlySkip(t *testing.T) {
	root := t.TempDir()
	writeDispatchJSON(t, root, `{"repos":[],"trigger_label":"","notify_on_complete":"","mappings":[]}`)

	fake, _ := setupHermeticSessions(t)

	if err := startDispatch(&cobra.Command{}, root, fake); err != nil {
		t.Fatalf("empty-default dispatch.json must friendly-skip, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("empty-default config must not launch a session; ops=%v", fake.ops)
	}
}

// A malformed dispatch.json (JSON parse error) is a real config error, not an
// unconfigured file: it must surface a warning instead of the friendly
// "not configured" skip, must not launch, and must still return nil (the af-up
// path never aborts).
func TestStartDispatch_MalformedJSONWarns(t *testing.T) {
	root := t.TempDir()
	writeDispatchJSON(t, root, `{not valid json`)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := startDispatch(cmd, root, fake); err != nil {
		t.Fatalf("malformed dispatch.json must not abort af up, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("malformed config must not launch a session; ops=%v", fake.ops)
	}
	combined := out.String() + errBuf.String()
	if !strings.Contains(combined, "warning") || !strings.Contains(combined, "dispatch") {
		t.Errorf("malformed dispatch.json must surface a warning, got %q", combined)
	}
	if strings.Contains(combined, "not configured") {
		t.Errorf("a parse error must NOT be reported as the friendly 'not configured' skip; got %q", combined)
	}
}

// A dispatch.json that parses but fails validation with ErrInvalidType (bad
// mapping source) is misconfigured, not unconfigured: warn, don't skip silently.
func TestStartDispatch_InvalidTypeWarns(t *testing.T) {
	root := t.TempDir()
	writeDispatchJSON(t, root, `{"repos":["t/r"],"trigger_label":"agentic","mappings":[{"label":"x","agent":"mgr","source":"bogus"}],"interval_seconds":300}`)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := startDispatch(cmd, root, fake); err != nil {
		t.Fatalf("invalid-type dispatch.json must not abort af up, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("invalid config must not launch a session; ops=%v", fake.ops)
	}
	combined := out.String() + errBuf.String()
	if !strings.Contains(combined, "warning") {
		t.Errorf("ErrInvalidType must surface a warning, got %q", combined)
	}
	if strings.Contains(combined, "not configured") {
		t.Errorf("ErrInvalidType must NOT be reported as the friendly 'not configured' skip; got %q", combined)
	}
}

// An unreadable dispatch.json (IO error that is not IsNotExist — here: the path
// is a directory) must surface a warning, not the friendly skip.
func TestStartDispatch_ReadIOErrorWarns(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agentfactory", "dispatch.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := startDispatch(cmd, root, fake); err != nil {
		t.Fatalf("unreadable dispatch.json must not abort af up, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("unreadable config must not launch a session; ops=%v", fake.ops)
	}
	combined := out.String() + errBuf.String()
	if !strings.Contains(combined, "warning") {
		t.Errorf("an IO error must surface a warning, got %q", combined)
	}
	if strings.Contains(combined, "not configured") {
		t.Errorf("an IO error must NOT be reported as the friendly 'not configured' skip; got %q", combined)
	}
}

// Pins the friendly-skip contract the discrimination must preserve: an absent
// dispatch.json keeps the "not configured" message and emits no warning.
func TestStartDispatch_FriendlySkipMessagePinned(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := startDispatch(cmd, root, fake); err != nil {
		t.Fatalf("absent dispatch.json must friendly-skip, got error: %v", err)
	}
	combined := out.String() + errBuf.String()
	if !strings.Contains(combined, "not configured") {
		t.Errorf("absent config must keep the friendly 'not configured' skip; got %q", combined)
	}
	if strings.Contains(combined, "warning") {
		t.Errorf("absent config must NOT warn; got %q", combined)
	}
}

// A valid dispatch.json launches: records NewSession + SendKeys for the session.
func TestStartDispatch_ValidConfigLaunches(t *testing.T) {
	root := t.TempDir()
	writeDispatchJSON(t, root, `{"repos":["test/repo"],"trigger_label":"agentic","mappings":[{"label":"test","agent":"mgr"}],"interval_seconds":300}`)

	fake, _ := setupHermeticSessions(t)

	if err := startDispatch(&cobra.Command{}, root, fake); err != nil {
		t.Fatalf("valid config should launch without error, got: %v", err)
	}
	if !opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("valid config must record a NewSession op; ops=%v", fake.ops)
	}
	if !opRecorded(fake.ops, "SendKeys "+dispatchSessionName) {
		t.Errorf("valid config must record a SendKeys op; ops=%v", fake.ops)
	}
}
