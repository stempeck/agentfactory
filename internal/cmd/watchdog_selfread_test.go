package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #408 Phase 2: the watchdog process self-reads its scope from
// startup.json.watchdog_agents (NOT the --agents/--agent flags) and validates membership
// against agents.json.
//
// #596 Phase 3 SUPERSEDED the second half of that contract. The watchdog used to REFUSE
// to start — returning a non-nil error so cobra exits non-zero — on an empty or
// all-unknown scope. It no longer does: an empty pane scope is now an inert pane surface,
// not a refusal, because the refusal left a factory whose startup.json omits
// watchdog_agents with no recovery process running at all, which is precisely the incident
// #596 exists to prevent (design-doc.md Decision 4, conflicts.md:61-80). What #408 actually
// bounded — kill/respawn blast radius (.designs/408/security.md:13) — is preserved exactly,
// because pollAgents fail-closes per agent on the scope map.
//
// The empty-scope and all-unknown cases are deliberately no longer symmetric: an omitted
// key is a supported configuration and stays quiet, while names that do not exist in
// agents.json remain an operator error and keep the loud warning plus the durable
// breadcrumb. The start-in-recovery-only-mode assertions live in watchdog_phase3_test.go;
// what remains here is scope RESOLUTION.
//
// These tests follow the package's hermetic seams: AF_ROOT + a t.TempDir() factory (per
// TestWatchdogToleratesMissingCwd), and direct resolveWatchdogScope(root) calls for the
// scope assertions so they never block on the ticker loop.

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

// AC-2, REVISED by #596 Phase 3. This test previously asserted that each of these four
// empty-scope shapes made runWatchdog return a non-nil error and write a breadcrumb. The
// refusal is gone, so what it pins now is the surviving half of the old contract — every
// shape still resolves to an INERT pane surface, and none of them is mistaken for "monitor
// everything". That inversion is the point of the revision: the blast-radius containment
// #408 asked for is preserved, while the process keeps running so occupancy recovery can
// cover the agents the pane surface does not.
//
// resolveWatchdogScope is called directly rather than runWatchdog because the assertion is
// about scope RESOLUTION; the process-starts half is
// TestWatchdog_EmptyScopeStartsInRecoveryOnlyMode.
func TestWatchdog_EmptyScopeYieldsInertPaneSurface(t *testing.T) {
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

			ws, err := resolveWatchdogScope(root)
			if err != nil {
				t.Fatalf("an empty scope is a supported configuration, not an error: %v", err)
			}
			if len(ws.agents) != 0 {
				t.Errorf("an empty scope must resolve to an EMPTY pane set — never to 'all' — got %v", ws.agents)
			}
			if ws.agents == nil {
				t.Error("the pane set must stay a non-nil empty map (the Phase-1 buildWatchdogScope contract)")
			}
			if ws.paneInertReason == "" {
				t.Error("an inert pane surface must carry a reason, or the startup line cannot explain itself")
			}
			// An omitted key is a configuration choice, not a misconfiguration: it must not
			// be reported as an operator error.
			if ws.paneMisconfig {
				t.Error("an empty scope must NOT be flagged as a misconfiguration — that is reserved for names that do not exist")
			}
		})
	}
}

// AC-3 (R2-H1 path parity), REVISED by #596 Phase 3. Previously: an all-unknown scope made
// runWatchdog return a non-nil error naming the agent. Now the process starts — but this
// case is deliberately NOT treated like the empty-scope case above. Configured names that do
// not exist in agents.json are an operator error, so the pane surface goes inert AND is
// flagged as misconfigured, which is what keeps the warning and the durable breadcrumb.
// Losing that distinction would make a typo indistinguishable from a deliberate choice.
func TestWatchdog_AllUnknownScopeYieldsFlaggedInertPaneSurface(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{"realagent":{"type":"autonomous","description":"x"}}}`)
	writeTestStartupConfig(t, root, `{"watchdog_agents":["ghost"]}`)

	ws, err := resolveWatchdogScope(root)
	if err != nil {
		t.Fatalf("an all-unknown scope must no longer be an error: %v", err)
	}
	if len(ws.agents) != 0 {
		t.Errorf("no configured name exists, so the pane set must be empty, got %v", ws.agents)
	}
	if !ws.paneMisconfig {
		t.Error("names absent from agents.json are an operator error and must be flagged as such")
	}
	if !strings.Contains(ws.paneInertReason, "ghost") {
		t.Errorf("the reason must name the offending agent 'ghost', got %q", ws.paneInertReason)
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

// TestWatchdog_EmptyAgentsJSON_YieldsFlaggedInertPaneSurface pins T5 (PR #410). The T5
// distinction is untouched by #596 Phase 3 and must survive it: a successfully-parsed but
// EMPTY agents.json (`{"agents":{}}`) is NOT a transient read — LoadAgentConfig returns a
// non-nil config with an empty map and a nil error — so every configured name really is
// unknown, and this must route to the all-unknown branch rather than the
// presume-the-configured-scope branch. Only the CONSEQUENCE changed: the old contract was
// "refuse to start", the new one is "inert pane surface, flagged as a misconfiguration".
// Distinct from TestWatchdog_TransientAgentsReadGuard_DoesNotRefuse, which covers the
// genuinely ABSENT-file path (agErr != nil) and must still presume the configured scope.
func TestWatchdog_EmptyAgentsJSON_YieldsFlaggedInertPaneSurface(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{}}`) // valid parse, empty map (NOT a read failure)
	writeTestStartupConfig(t, root, `{"watchdog_agents":["manager","supervisor"]}`)

	ws, err := resolveWatchdogScope(root)
	if err != nil {
		t.Fatalf("an all-unknown scope is no longer an error: %v", err)
	}
	if len(ws.agents) != 0 {
		t.Errorf("a valid-but-empty agents.json makes every configured name unknown, so the pane set must be empty, got %v", ws.agents)
	}
	// The T5 hole itself: an empty map must NOT be mistaken for a failed read, which would
	// have monitored a configured-but-nonexistent scope.
	if !ws.paneMisconfig {
		t.Error("an empty agents.json map must route to the all-unknown branch (T5), not the transient-read branch")
	}
	if ws.membershipNote != "" {
		t.Errorf("an empty map is not a transient read, so no membership note may be set; got %q", ws.membershipNote)
	}
}
