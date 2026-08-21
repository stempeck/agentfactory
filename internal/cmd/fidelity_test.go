package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stempeck/agentfactory/internal/config"
)

func TestFidelity_OffBlockedDuringFormula(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	writeRuntimeFile(t, dir, "hooked_formula", "bd-test-instance")

	err := runFidelity(fidelityCmd, []string{"off"})
	if err == nil {
		t.Fatal("expected error when hooked_formula exists, got nil")
	}
	if !strings.Contains(err.Error(), "cannot disable fidelity gate") {
		t.Errorf("error %q does not contain expected message", err.Error())
	}

	gateFile := filepath.Join(dir, ".agentfactory", ".fidelity-gate")
	data, err := os.ReadFile(gateFile)
	if err == nil && strings.TrimSpace(string(data)) == "off" {
		t.Error(".fidelity-gate was written to 'off' despite active formula — guard did not block")
	}
}

// setupTestFactoryForFidelity creates a minimal factory layout so
// config.FindFactoryRoot succeeds. Returns the tempdir path.
func setupTestFactoryForFidelity(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	return dir
}

func TestFidelity_DefaultOff(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "fidelity gate: off") {
		t.Errorf("output %q does not contain %q", out, "fidelity gate: off")
	}
}

func TestFidelity_OnByDefaultAfterInstall(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	// Simulate what af install --init does: create .fidelity-gate with "on"
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write .fidelity-gate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q does not contain %q", out, "fidelity gate: on")
	}
}

func TestFidelity_TurnOn(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	_ = captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, []string{"on"}); err != nil {
			t.Fatalf("runFidelity on: %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"))
	if err != nil {
		t.Fatalf("read .fidelity-gate: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("file contents = %q, want %q", string(data), "on\n")
	}
}

func TestFidelity_TurnOff(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	_ = captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, []string{"off"}); err != nil {
			t.Fatalf("runFidelity off: %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"))
	if err != nil {
		t.Fatalf("read .fidelity-gate: %v", err)
	}
	if string(data) != "off\n" {
		t.Errorf("file contents = %q, want %q", string(data), "off\n")
	}
}

func TestFidelity_StatusOnReport(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	if err := os.WriteFile(
		filepath.Join(dir, ".agentfactory", ".fidelity-gate"),
		[]byte("on\n"),
		0o644,
	); err != nil {
		t.Fatalf("pre-write .fidelity-gate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q does not contain %q", out, "fidelity gate: on")
	}
}

func TestFidelity_StatusOffReport(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	if err := os.WriteFile(
		filepath.Join(dir, ".agentfactory", ".fidelity-gate"),
		[]byte("off\n"),
		0o644,
	); err != nil {
		t.Fatalf("pre-write .fidelity-gate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "fidelity gate: off") {
		t.Errorf("output %q does not contain %q", out, "fidelity gate: off")
	}
}

func TestFidelity_BadArg(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	err := runFidelity(fidelityCmd, []string{"weird"})
	if err == nil {
		t.Fatal("expected error for bad arg, got nil")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("error %q does not contain %q", err.Error(), "usage")
	}
}

func TestFidelity_StatusWithStaleLock(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644)

	writeRuntimeFile(t, dir, "fidelity-gate.lock",
		`{"pid":99999999,"acquired_at":"2026-01-01T00:00:00Z","session_id":"dead-session"}`)

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "WARNING") {
		t.Errorf("output %q should contain WARNING for stale lock", out)
	}
	if !strings.Contains(out, "99999999") {
		t.Errorf("output %q should contain the dead PID", out)
	}
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q should still show gate on", out)
	}
}

func TestFidelity_StatusOnCleanNoWarning(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644)

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if strings.Contains(out, "WARNING") {
		t.Errorf("output %q should NOT contain WARNING when no stale lock", out)
	}
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q should contain clean on status", out)
	}
}

// fidelityFlagNames is the reset list resetFidelityFlags walks. It is a named var, not a literal
// inside the loop, so TestResetFidelityFlags_CoversEveryFidelityFlag can prove it stayed complete:
// the helper skips names it cannot find, so a flag added to `af fidelity` and forgotten here would
// leak its value into sibling tests with no compile error (mail_test.go:157-168 records the same
// trap for `af mail send`).
var fidelityFlagNames = []string{"agent"}

// resetFidelityFlags restores every fidelity flag to its default and clears Changed. Every test in
// this file drives the package-global fidelityCmd through runFidelity, so a --agent left set turns
// a later factory-wide toggle assertion into a per-agent one — silently.
func resetFidelityFlags(t *testing.T) {
	t.Helper()
	for _, name := range fidelityFlagNames {
		f := fidelityCmd.Flags().Lookup(name)
		if f == nil {
			continue
		}
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatalf("resetting --%s: %v", name, err)
		}
		f.Changed = false
	}
}

// setFidelityAgentFlag sets --agent and registers the reset before the value is set, so the
// cleanup survives a t.Fatal mid-test (improvement_test.go:51-57).
func setFidelityAgentFlag(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() { resetFidelityFlags(t) })
	if err := fidelityCmd.Flags().Set("agent", name); err != nil {
		t.Fatalf("set --agent: %v", err)
	}
}

func TestResetFidelityFlags_CoversEveryFidelityFlag(t *testing.T) {
	listed := map[string]bool{}
	for _, n := range fidelityFlagNames {
		listed[n] = true
	}
	fidelityCmd.Flags().VisitAll(func(f *pflag.Flag) {
		// cobra injects --help into the local set the first time the command runs, so whether it
		// is present depends on test order (mail_test.go:351-379).
		if f.Name == "help" {
			return
		}
		if !listed[f.Name] {
			t.Errorf("--%s is registered on fidelityCmd but missing from fidelityFlagNames, so its value leaks into sibling tests", f.Name)
		}
	})
	for _, n := range fidelityFlagNames {
		if fidelityCmd.Flags().Lookup(n) == nil {
			t.Errorf("fidelityFlagNames lists --%s but fidelityCmd does not register it", n)
		}
	}
}

func fidelityOverridePath(root, agent string) string {
	return filepath.Join(root, ".agentfactory", "fidelity-overrides", agent)
}

func fidelityProvenancePath(root string) string {
	return filepath.Join(root, ".agentfactory", ".fidelity-gate.log")
}

// TestFidelity_OffRequiresOperator pins design-doc L253 in BOTH directions: `af fidelity off`
// refuses inside an agent session and succeeds from an operator shell. The agent-context idiom is
// recovery_reset_test.go:137-138; the operator half needs no setup because main_test.go's
// tmuxisolation.Setup neutralizes the whole AF_*/TMUX family.
func TestFidelity_OffRequiresOperator(t *testing.T) {
	t.Run("refuses in agent context and leaves the toggle byte-unchanged", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		gateFile := filepath.Join(dir, ".agentfactory", ".fidelity-gate")
		if err := os.WriteFile(gateFile, []byte("on\n"), 0o644); err != nil {
			t.Fatalf("pre-write .fidelity-gate: %v", err)
		}
		before, err := os.ReadFile(gateFile)
		if err != nil {
			t.Fatal(err)
		}

		installFakeTmuxPresent(t)
		t.Setenv("AF_ROLE", "manager") // signal 1 ⇒ AuthorityAgent
		t.Setenv("TMUX", "")

		runErr := runFidelity(fidelityCmd, []string{"off"})
		if runErr == nil {
			t.Fatal("agent authority must be refused with a non-nil error so cobra exits 1")
		}

		after, err := os.ReadFile(gateFile)
		if err != nil {
			t.Fatalf("the toggle must survive a refusal: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("a refused off must not touch the toggle: before=%q after=%q", before, after)
		}
		if lines := gateProvenanceLines(t, dir); len(lines) != 0 {
			t.Errorf("a refused off must not append provenance, got %d line(s): %v", len(lines), lines)
		}

		// ux.md L36-39: never hand the agent a bypass recipe.
		for _, leak := range []string{"AF_ROLE", "TMUX", "tmux", "display-message", "#S", "$TMUX"} {
			if strings.Contains(runErr.Error(), leak) {
				t.Errorf("the refusal must not name the detection mechanism (%q): %q", leak, runErr.Error())
			}
		}
		// Gotcha 5: this is not a teardown surface — authority_test.go:13-21 enumerates those as four.
		for _, teardownClaim := range []string{"stops the whole factory", "kill YOU", "every sibling agent"} {
			if strings.Contains(runErr.Error(), teardownClaim) {
				t.Errorf("the fidelity refusal must not reuse the teardown body (%q): %q", teardownClaim, runErr.Error())
			}
		}
	})

	t.Run("succeeds from an operator shell", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		gateFile := filepath.Join(dir, ".agentfactory", ".fidelity-gate")
		if err := os.WriteFile(gateFile, []byte("on\n"), 0o644); err != nil {
			t.Fatalf("pre-write .fidelity-gate: %v", err)
		}

		var runErr error
		_ = captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"off"}) })
		if runErr != nil {
			t.Fatalf("operator context must succeed: %v", runErr)
		}
		data, err := os.ReadFile(gateFile)
		if err != nil {
			t.Fatalf("read .fidelity-gate: %v", err)
		}
		if string(data) != "off\n" {
			t.Errorf("file contents = %q, want %q", string(data), "off\n")
		}
	})

	t.Run("on is not gated in agent context", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)

		installFakeTmuxPresent(t)
		t.Setenv("AF_ROLE", "manager")
		t.Setenv("TMUX", "")

		var runErr error
		_ = captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"on"}) })
		if runErr != nil {
			t.Fatalf("re-enabling oversight must never be refused (fail toward oversight): %v", runErr)
		}
		data, err := os.ReadFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"))
		if err != nil {
			t.Fatalf("read .fidelity-gate: %v", err)
		}
		if string(data) != "on\n" {
			t.Errorf("file contents = %q, want %q", string(data), "on\n")
		}
	})

	t.Run("off --agent is authority-checked too", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		setFidelityAgentFlag(t, "alpha")

		installFakeTmuxPresent(t)
		t.Setenv("AF_ROLE", "manager")
		t.Setenv("TMUX", "")

		if err := runFidelity(fidelityCmd, []string{"off"}); err == nil {
			t.Fatal("security.md T2a authority-checks `off --agent` as well as bare `off`")
		}
		if _, err := os.Stat(fidelityOverridePath(dir, "alpha")); !os.IsNotExist(err) {
			t.Error("a refused scoped off must not create an override file")
		}
	})
}

// TestFidelity_AgentOverride pins D-6's surgical storm response: `off --agent X` silences X alone,
// `on --agent X` clears it, and both leave one provenance line. The override lives under
// .agentfactory/, deliberately outside X's writable workspace (security.md T2b, data.md:112).
func TestFidelity_AgentOverride(t *testing.T) {
	t.Run("off --agent writes the override without touching the master toggle", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		gateFile := filepath.Join(dir, ".agentfactory", ".fidelity-gate")
		if err := os.WriteFile(gateFile, []byte("on\n"), 0o644); err != nil {
			t.Fatalf("pre-write .fidelity-gate: %v", err)
		}
		setFidelityAgentFlag(t, "alpha")

		var runErr error
		out := captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"off"}) })
		if runErr != nil {
			t.Fatalf("off --agent alpha: %v", runErr)
		}

		// SF-1: the success line must not read as "the storm is contained for this agent" when no
		// hook enforces the override. It must disclose the same caveat --help does — no hook reads
		// the override dir, so the gate still grades this agent — so an operator is not misled.
		if !strings.Contains(out, "no hook") {
			t.Errorf("off --agent success line must disclose that no hook enforces the override yet; got: %q", out)
		}

		data, err := os.ReadFile(fidelityOverridePath(dir, "alpha"))
		if err != nil {
			t.Fatalf("read override: %v", err)
		}
		if string(data) != "off\n" {
			t.Errorf("override contents = %q, want %q (data.md:112)", string(data), "off\n")
		}

		master, err := os.ReadFile(gateFile)
		if err != nil {
			t.Fatalf("read .fidelity-gate: %v", err)
		}
		if string(master) != "on\n" {
			t.Errorf("a scoped off must not disable the factory-wide gate: %q", string(master))
		}

		lines := gateProvenanceLines(t, dir)
		if len(lines) != 1 {
			t.Fatalf("want exactly 1 provenance line, got %d: %v", len(lines), lines)
		}
		fields := strings.Fields(lines[0])
		if len(fields) != 4 {
			t.Errorf("provenance line must be `ts actor source state`, got %d fields: %q", len(fields), lines[0])
		}
		if !strings.Contains(lines[0], "alpha") {
			t.Errorf("a scoped write must record which agent it scoped to: %q", lines[0])
		}
	})

	t.Run("on --agent removes only that agent's override", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		gateFile := filepath.Join(dir, ".agentfactory", ".fidelity-gate")
		if err := os.WriteFile(gateFile, []byte("on\n"), 0o644); err != nil {
			t.Fatalf("pre-write .fidelity-gate: %v", err)
		}
		overridesDir := filepath.Join(dir, ".agentfactory", "fidelity-overrides")
		if err := os.MkdirAll(overridesDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, a := range []string{"alpha", "beta"} {
			if err := os.WriteFile(fidelityOverridePath(dir, a), []byte("off\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		setFidelityAgentFlag(t, "alpha")

		var runErr error
		_ = captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"on"}) })
		if runErr != nil {
			t.Fatalf("on --agent alpha: %v", runErr)
		}
		if _, err := os.Stat(fidelityOverridePath(dir, "alpha")); !os.IsNotExist(err) {
			t.Error("on --agent alpha must remove alpha's override")
		}
		if _, err := os.Stat(fidelityOverridePath(dir, "beta")); err != nil {
			t.Errorf("beta's override must survive: %v", err)
		}
		if master, err := os.ReadFile(gateFile); err != nil || string(master) != "on\n" {
			t.Errorf("a scoped on must not touch the factory-wide gate: %q (%v)", master, err)
		}
		if lines := gateProvenanceLines(t, dir); len(lines) != 1 {
			t.Errorf("want exactly 1 provenance line, got %d: %v", len(lines), lines)
		}
	})

	t.Run("on --agent with no override is a benign no-op", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		setFidelityAgentFlag(t, "alpha")

		var runErr error
		_ = captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"on"}) })
		if runErr != nil {
			t.Fatalf("clearing an absent override must be a benign no-op: %v", runErr)
		}
	})

	t.Run("rejects an invalid agent name before any path is composed", func(t *testing.T) {
		for _, bad := range []string{"../../sentinel", "watchdog", "1bad", "a/b"} {
			t.Run(bad, func(t *testing.T) {
				dir := setupTestFactoryForFidelity(t)
				t.Chdir(dir)
				sentinel := filepath.Join(dir, "sentinel")
				if err := os.WriteFile(sentinel, []byte("keep\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				setFidelityAgentFlag(t, bad)

				if err := runFidelity(fidelityCmd, []string{"off"}); err == nil {
					t.Fatalf("agent name %q must be refused", bad)
				}
				kept, err := os.ReadFile(sentinel)
				if err != nil {
					t.Errorf("the name must be validated before any path is composed: %v", err)
				} else if string(kept) != "keep\n" {
					t.Errorf("the sentinel was overwritten (%q) — the path was composed before the name was validated", kept)
				}
				if lines := gateProvenanceLines(t, dir); len(lines) != 0 {
					t.Errorf("a refused write must not poison the audit log: %v", lines)
				}
			})
		}
	})
}

func TestFidelity_ProvenanceFromCLI(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	_ = captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, []string{"on"}); err != nil {
			t.Fatalf("on: %v", err)
		}
		if err := runFidelity(fidelityCmd, []string{"off"}); err != nil {
			t.Fatalf("off: %v", err)
		}
	})

	lines := gateProvenanceLines(t, dir)
	if len(lines) != 2 {
		t.Fatalf("every toggle write appends exactly one line; want 2, got %d: %v", len(lines), lines)
	}
	for i, want := range []string{"on", "off"} {
		fields := strings.Fields(lines[i])
		if len(fields) != 4 {
			t.Fatalf("line %d must be `ts actor source state`: %q", i, lines[i])
		}
		if fields[3] != want {
			t.Errorf("line %d state = %q, want %q", i, fields[3], want)
		}
	}

	// The toggle file the hook compares with `cat … != "on"` must stay pure (Gotcha 2).
	data, err := os.ReadFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "off\n" {
		t.Errorf("toggle file = %q, want %q — provenance belongs in the sibling .log", string(data), "off\n")
	}
}

func TestFidelity_StatusReadsRunRecord(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The frozen K7 schema (design-doc.md:76, data.md:107). Phase 6 writes these; this phase reads.
	alpha := config.AgentDir(dir, "alpha")
	writeRuntimeFile(t, alpha, "fidelity_log.jsonl", strings.Join([]string{
		`{"ts":"2026-08-09T10:00:00Z","step_id":"bd-1","verdict_ok":true,"calls_total":3,"calls_shown":3,"violations_after":0,"escalated":false}`,
		`{"ts":"2026-08-09T10:05:00Z","step_id":"bd-1","verdict_ok":false,"calls_total":1,"calls_shown":1,"violations_after":1,"escalated":false}`,
		`{"ts":"2026-08-09T10:09:00Z","step_id":"bd-2","verdict_ok":false,"calls_total":0,"calls_shown":0,"violations_after":2,"escalated":true}`,
		`{ this line is not json`,
	}, "\n")+"\n")
	// Deliberately NOT the last step id: a shared value would let the last-step column satisfy the
	// latch assertion, and the test would pass with the latch read deleted.
	writeRuntimeFile(t, alpha, "fidelity_escalated_step", "bd-latched\n")

	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory", "fidelity-overrides"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fidelityOverridePath(dir, "beta"), []byte("off\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fidelityProvenancePath(dir), []byte("2026-08-09T09:00:00Z operator cli on\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var runErr error
	out := captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"status"}) })
	if runErr != nil {
		t.Fatalf("status must not fail on a malformed record line (ADR-007 posture): %v", runErr)
	}
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("status must keep the stable first line: %q", out)
	}
	for _, want := range []string{"beta", "operator cli on"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	// evaluations=3 (the fourth line is undecodable), failures=2, violations=2, last step bd-2,
	// latch bd-latched — asserted as one row so no column can stand in for another.
	row := regexp.MustCompile(`alpha\s+3\s+2\s+2\s+bd-2\s+bd-latched`)
	if !row.MatchString(out) {
		t.Errorf("status row for alpha does not match %v:\n%s", row, out)
	}
}

func TestFidelity_StatusEmptyFactoryIsQuiet(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var runErr error
	out := captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"status"}) })
	if runErr != nil {
		t.Fatalf("a factory with no agents dir must render, not error: %v", runErr)
	}
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q missing the stable first line", out)
	}
	if strings.Contains(out, "WARNING") {
		t.Errorf("status must not emit WARNING with nothing wrong: %q", out)
	}
}

// TestFidelity_StatusTailReadIsBounded plants a run record far larger than the 64 KiB window and
// pins that the reader stays bounded. The fragment left at the window boundary must not be counted
// as a record: the head lines carry verdict_ok true, so half of one decoding successfully would
// show up as an extra evaluation.
func TestFidelity_StatusTailReadIsBounded(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	// ~150 bytes per line ⇒ well past 64 KiB, with a distinctive step id only in the head.
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, `{"ts":"2026-08-09T10:00:00Z","step_id":"bd-head-%04d","verdict_ok":true,"calls_total":1,"calls_shown":1,"violations_after":0,"escalated":false}`+"\n", i)
	}
	fmt.Fprint(&b, `{"ts":"2026-08-09T11:00:00Z","step_id":"bd-tail","verdict_ok":false,"calls_total":2,"calls_shown":2,"violations_after":9,"escalated":false}`+"\n")
	writeRuntimeFile(t, config.AgentDir(dir, "alpha"), "fidelity_log.jsonl", b.String())

	var runErr error
	out := captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"status"}) })
	if runErr != nil {
		t.Fatalf("status over a large record: %v", runErr)
	}
	if !strings.Contains(out, "alpha") {
		t.Errorf("status must still report the agent:\n%s", out)
	}
	if strings.Contains(out, "2001") {
		t.Errorf("the reader must be bounded to the tail window, not the whole file:\n%s", out)
	}
}

// fixedWidthRecords builds n newline-terminated records of exactly width bytes each (newline
// included), numbered so a test can name which ones survived the tail window.
func fixedWidthRecords(t *testing.T, n, width int) (content string, lines []string) {
	t.Helper()
	var b strings.Builder
	lines = make([]string, n)
	for i := 0; i < n; i++ {
		line := fmt.Sprintf("#%06d", i)
		line += strings.Repeat("x", width-1-len(line))
		lines[i] = line
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String(), lines
}

// TestReadBoundedTail_WindowBoundaries pins the three ways the 64 KiB window can land relative to a
// record boundary. The widths are chosen so each case is exact rather than incidental: 65536 is
// divisible by 256, so the boundary-aligned case lands on a newline; 300 does not divide it, so the
// window opens mid-record; and 65535 = 257*255, so a 255-byte record puts the window's FIRST byte
// on a newline — the zero-length-fragment case, where dropping by count instead of by position
// would silently eat the following whole record.
func TestReadBoundedTail_WindowBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		n, width  int
		wantFirst int
	}{
		{"a file below the window is returned whole", 3, 20, 0},
		{"a window landing on a record boundary keeps every whole record", 257, 256, 1},
		{"a window landing inside a record drops only that fragment", 250, 300, 32},
		{"a window opening on a newline drops the empty fragment, not the record after it", 300, 255, 43},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content, lines := fixedWidthRecords(t, tc.n, tc.width)
			path := filepath.Join(t.TempDir(), "fidelity_log.jsonl")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}

			got, truncated := readBoundedTail(path)
			want := lines[tc.wantFirst:]
			if wantTruncated := len(content) > fidelityTailWindow; truncated != wantTruncated {
				t.Errorf("truncated = %v, want %v for a %d-byte file through a %d-byte window",
					truncated, wantTruncated, len(content), fidelityTailWindow)
			}
			if len(got) != len(want) {
				t.Fatalf("read %d records, want %d (first want %q, first got %q)",
					len(got), len(want), want[0], got[0])
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("record %d = %q, want %q", i, got[i], want[i])
				}
			}
			if len(content) > fidelityTailWindow && len(got) == tc.n {
				t.Errorf("a file of %d bytes must not be read whole through a %d-byte window",
					len(content), fidelityTailWindow)
			}
		})
	}
}

// TestFidelity_AgentFlagRejectedOnReadSurfaces pins the flag against the read verbs. --agent scopes
// a WRITE; silently ignoring it on `status` would report the whole factory under a name the
// operator asked to narrow to, which reads as "this agent is clean" when nothing was checked.
func TestFidelity_AgentFlagRejectedOnReadSurfaces(t *testing.T) {
	for _, args := range [][]string{{}, {"status"}} {
		name := "no verb"
		if len(args) > 0 {
			name = args[0]
		}
		t.Run(name, func(t *testing.T) {
			dir := setupTestFactoryForFidelity(t)
			t.Chdir(dir)
			setFidelityAgentFlag(t, "alpha")

			var runErr error
			captureStdout(t, func() { runErr = runFidelity(fidelityCmd, args) })
			if runErr == nil {
				t.Fatal("--agent on a read surface must be rejected, not ignored")
			}
			if !strings.Contains(runErr.Error(), "--agent") {
				t.Errorf("error %q does not name the flag it rejected", runErr.Error())
			}
		})
	}
}

// TestFidelity_EmptyAgentValueIsRejected pins the gap between "--agent was not given" and
// "--agent was given as nothing". Treating the second as the first turns `af fidelity off --agent=`
// — which reads as scoping the disable to one agent — into a factory-wide disable of the gate.
func TestFidelity_EmptyAgentValueIsRejected(t *testing.T) {
	for _, verb := range []string{"off", "on"} {
		t.Run(verb, func(t *testing.T) {
			dir := setupTestFactoryForFidelity(t)
			t.Chdir(dir)
			gateFile := filepath.Join(dir, ".agentfactory", ".fidelity-gate")
			if err := os.WriteFile(gateFile, []byte("on\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			setFidelityAgentFlag(t, "")

			var runErr error
			captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{verb}) })
			if runErr == nil {
				t.Fatalf("af fidelity %s --agent= must be rejected, not treated as factory-wide", verb)
			}
			if !strings.Contains(runErr.Error(), "--agent") {
				t.Errorf("error %q does not name the flag it rejected", runErr.Error())
			}
			if data, _ := os.ReadFile(gateFile); string(data) != "on\n" {
				t.Errorf("the factory-wide toggle = %q, want %q — a rejected command must not write",
					string(data), "on\n")
			}
			if lines := gateProvenanceLines(t, dir); len(lines) != 0 {
				t.Errorf("a rejected command must not appear in the audit log, got %v", lines)
			}
		})
	}
}

// TestFidelity_StatusMarksTruncatedCounts pins the honesty of the numbers. A count derived from the
// last 64 KiB of a longer log is a floor, and an operator reading it as a total would conclude the
// gate had fired a fraction of the times it actually did.
func TestFidelity_StatusMarksTruncatedCounts(t *testing.T) {
	record := func(i int) string {
		return fmt.Sprintf(`{"ts":"2026-08-09T10:00:00Z","step_id":"bd-%04d","verdict_ok":false,"calls_total":1,"calls_shown":1,"violations_after":3,"escalated":false}`+"\n", i)
	}

	t.Run("a bounded read is marked as a floor", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		var b strings.Builder
		for i := 0; i < 2000; i++ {
			b.WriteString(record(i))
		}
		writeRuntimeFile(t, config.AgentDir(dir, "alpha"), "fidelity_log.jsonl", b.String())

		var runErr error
		out := captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"status"}) })
		if runErr != nil {
			t.Fatalf("status: %v", runErr)
		}
		if !regexp.MustCompile(`alpha\s+\d+\+\s+\d+\+`).MatchString(out) {
			t.Errorf("truncated counts must be marked as floors:\n%s", out)
		}
		if !strings.Contains(out, "earlier evaluations are not included") {
			t.Errorf("the marker needs a legend the operator can act on:\n%s", out)
		}
	})

	t.Run("a complete read is not marked", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		writeRuntimeFile(t, config.AgentDir(dir, "alpha"), "fidelity_log.jsonl", record(1)+record(2))

		var runErr error
		out := captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"status"}) })
		if runErr != nil {
			t.Fatalf("status: %v", runErr)
		}
		if !regexp.MustCompile(`alpha\s+2\s+2\s+3\s`).MatchString(out) {
			t.Errorf("a whole-file read must report exact counts:\n%s", out)
		}
		if strings.Contains(out, "earlier evaluations are not included") {
			t.Errorf("a complete read must not claim to be truncated:\n%s", out)
		}
	})
}
