package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #408 Phase 2: the watchdog process self-reads its scope from
// startup.json.watchdog_agents (NOT the --agents/--agent flags), validates
// membership against agents.json, and refuses to start (returns a non-nil error
// so cobra exits non-zero) on an empty OR all-unknown scope, writing a
// watchdog-namespaced breadcrumb to <root>/.runtime/watchdog_last_error first.
//
// These tests follow the package's hermetic seams: AF_ROOT + a t.TempDir()
// factory (per TestWatchdogToleratesMissingCwd) for the runWatchdog refuse path,
// and direct resolveWatchdogScope(root) calls for the positive scope assertions
// so they never block on the ticker loop.

// newTestFactoryRoot creates a t.TempDir() factory with .agentfactory/factory.json
// so resolveWatchdogRoot() (via AF_ROOT) resolves it.
func newTestFactoryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dotDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "factory.json"),
		[]byte(`{"type":"factory","version":1,"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// writeTestStartupConfig writes <root>/.agentfactory/startup.json.
func writeTestStartupConfig(t *testing.T, root, json string) {
	t.Helper()
	dotDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "startup.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

// AC-1: watchdog_agents:["alpha"] ⇒ scope is exactly {alpha}, sourced from
// startup.json and NOT from the --agents/--agent flags.
func TestWatchdog_SelfReadsStartupScope(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeTestStartupConfig(t, root, `{"watchdog_agents":["alpha"]}`)

	// The --agents/--agent flags are gone (Phase 3); scope is startup-sourced. The
	// assertions below prove the former flag names never leak into the resolved scope.
	ws, err := resolveWatchdogScope(root)
	if err != nil {
		t.Fatalf("valid startup scope must not refuse: %v", err)
	}
	if len(ws.agents) != 1 {
		t.Fatalf("scope size = %d, want 1 (got %v)", len(ws.agents), ws.agents)
	}
	if _, ok := ws.agents["alpha"]; !ok {
		t.Errorf("scope must be sourced from startup.json {alpha}, got %v", ws.agents)
	}
	if _, leaked := ws.agents["flagagent"]; leaked {
		t.Error("scope must NOT include the --agents flag value (flag-sourced scope removed)")
	}
	if _, leaked := ws.agents["flagsingle"]; leaked {
		t.Error("scope must NOT include the --agent flag value")
	}
}

// AC-2: absent/empty watchdog_agents ⇒ runWatchdog returns a non-nil error and
// writes a <root>/.runtime/watchdog_last_error breadcrumb.
func TestWatchdog_RefusesWhenScopeEmpty(t *testing.T) {
	cases := []struct {
		name      string
		writeFile bool
		startup   string
	}{
		{"absent startup.json", false, ""},
		{"explicit empty array", true, `{"watchdog_agents":[]}`},
		{"omitted field", true, `{"quality":"default"}`},
		{"all-blank entries", true, `{"watchdog_agents":["  ",""]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestFactoryRoot(t)
			if tc.writeFile {
				writeTestStartupConfig(t, root, tc.startup)
			}
			t.Setenv("AF_ROOT", root)

			cmd, _ := newTestCmd()
			err := runWatchdog(cmd, nil)
			if err == nil {
				t.Fatal("empty scope must refuse: runWatchdog returned nil error")
			}
			breadcrumb := filepath.Join(root, ".runtime", "watchdog_last_error")
			if _, statErr := os.Stat(breadcrumb); statErr != nil {
				t.Errorf("refusal must write breadcrumb %s: %v", breadcrumb, statErr)
			}
		})
	}
}

// AC-3 (R2-H1 path parity): a non-empty but all-unknown scope ⇒ a direct
// af watchdog refuses with a non-nil error naming the offending agent — NOT a
// silent zero-agent start.
func TestWatchdog_RefusesWhenScopeAllUnknown(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{"realagent":{"type":"autonomous","description":"x"}}}`)
	writeTestStartupConfig(t, root, `{"watchdog_agents":["ghost"]}`)
	t.Setenv("AF_ROOT", root)

	cmd, _ := newTestCmd()
	err := runWatchdog(cmd, nil)
	if err == nil {
		t.Fatal("all-unknown scope must refuse (R2-H1 path parity): got nil error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("refusal error must name the offending agent 'ghost', got: %v", err)
	}
	breadcrumb := filepath.Join(root, ".runtime", "watchdog_last_error")
	if _, statErr := os.Stat(breadcrumb); statErr != nil {
		t.Errorf("refusal must write breadcrumb %s: %v", breadcrumb, statErr)
	}
}

// AC-4: membership is keyed on agents.json, NOT on a live session — a
// configured-but-not-running agent (supervisor on a fresh factory) is KNOWN and
// must NOT refuse.
func TestWatchdog_KnownButNotRunning_DoesNotRefuse(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{"supervisor":{"type":"interactive","description":"sup"}}}`)
	writeTestStartupConfig(t, root, `{"watchdog_agents":["supervisor"]}`)

	ws, err := resolveWatchdogScope(root)
	if err != nil {
		t.Fatalf("known-but-not-running agent must NOT refuse: %v", err)
	}
	if _, ok := ws.agents["supervisor"]; !ok || len(ws.agents) != 1 {
		t.Errorf("scope must be {supervisor}, got %v", ws.agents)
	}
}

// Per-name typos stay non-fatal: a scope with >=1 known name still launches and
// the unknown name is reported as a warning, not escalated to a refusal.
func TestWatchdog_PartialUnknown_DoesNotRefuse(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeTestStartupConfig(t, root, `{"watchdog_agents":["alpha","typo"]}`)

	ws, err := resolveWatchdogScope(root)
	if err != nil {
		t.Fatalf("a scope with >=1 known name must NOT refuse: %v", err)
	}
	if _, ok := ws.agents["alpha"]; !ok {
		t.Errorf("known name 'alpha' must remain in scope, got %v", ws.agents)
	}
	foundTypo := false
	for _, n := range ws.unknown {
		if n == "typo" {
			foundTypo = true
		}
	}
	if !foundTypo {
		t.Errorf("unknown name 'typo' must be reported as a per-name warning, got unknown=%v", ws.unknown)
	}
}

// Transient/partial-read guard: an unreadable agents.json must NOT be treated as
// "all-unknown ⇒ refuse"; the watchdog launches on the configured (non-empty)
// scope and surfaces an observability note (N-2).
func TestWatchdog_TransientAgentsReadGuard_DoesNotRefuse(t *testing.T) {
	root := newTestFactoryRoot(t)
	// agents.json intentionally absent => LoadAgentConfig returns ErrNotFound.
	writeTestStartupConfig(t, root, `{"watchdog_agents":["alpha"]}`)

	ws, err := resolveWatchdogScope(root)
	if err != nil {
		t.Fatalf("an unreadable agents.json must NOT be treated as all-unknown: %v", err)
	}
	if _, ok := ws.agents["alpha"]; !ok {
		t.Errorf("must launch on the configured scope {alpha}, got %v", ws.agents)
	}
	if ws.membershipNote == "" {
		t.Error("transient-read guard must surface an observability note (N-2)")
	}
}

// TestWatchdog_EmptyAgentsJSON_Refuses pins T5 (PR #410): a successfully-parsed but
// EMPTY agents.json (`{"agents":{}}`) is NOT a transient read — LoadAgentConfig returns
// a non-nil config with an empty map and a nil error. Every configured watchdog_agents
// name is then unknown, so the watchdog must REFUSE (all-unknown), not launch on a
// configured-but-nonexistent scope. This is the fail-open hole the empty-map case fell
// through; distinct from TestWatchdog_TransientAgentsReadGuard_DoesNotRefuse, which
// covers the genuinely ABSENT-file path (agErr != nil).
func TestWatchdog_EmptyAgentsJSON_Refuses(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{}}`) // valid parse, empty map (NOT a read failure)
	writeTestStartupConfig(t, root, `{"watchdog_agents":["manager","supervisor"]}`)

	if _, err := resolveWatchdogScope(root); err == nil {
		t.Fatal("a valid-but-empty agents.json must REFUSE (all configured names unknown), got nil error")
	}
}
