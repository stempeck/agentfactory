package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// frozenStatusLineCmd is the byte-exact command the statusLine block must carry in BOTH
// settings templates (issue #591, K6). It reuses the same PATH-export idiom as every other
// hook command, swapping the verb to `af statusline render`. "padding": 0 is deliberately
// omitted (deferred to the P4b padding probe). Any drift here is a launch-adjacent contract
// break, so the pin tests below assert this string verbatim.
const frozenStatusLineCmd = `export PATH="$HOME/go/bin:$HOME/.local/bin:$HOME/bin:$PATH" && af statusline render`

// frozenStatusLineRefresh is the statusLine refresh cadence in SECONDS (issue #596 K1). Claude
// Code re-runs the statusline command every N seconds in addition to its event-driven updates;
// without it an idle or wedged session simply stops rendering and its occupancy snapshot freezes,
// making "stale" indistinguishable from "healthy but quiet".
//
// The value is load-bearing, not cosmetic: internal/config's staleness floor is
// 3x(refresh + watchdog tick) = 3x(30+30) = 180, which is EXACTLY the shipped staleness_secs
// default. TestRecoveryRefreshInterval_MatchesSettingsTemplates (internal/config) is the guard
// that keeps this number and recoveryRefreshIntervalSecs from drifting apart.
const frozenStatusLineRefresh = 30

func TestRoleTypeFor_Interactive(t *testing.T) {
	agents := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"manager": {Type: "interactive", Description: "test manager"},
		},
	}
	got := RoleTypeFor("manager", agents)
	if got != Interactive {
		t.Errorf("RoleTypeFor(manager) = %d, want Interactive (%d)", got, Interactive)
	}
}

func TestRoleTypeFor_Autonomous(t *testing.T) {
	agents := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"supervisor": {Type: "autonomous", Description: "test supervisor"},
		},
	}
	got := RoleTypeFor("supervisor", agents)
	if got != Autonomous {
		t.Errorf("RoleTypeFor(supervisor) = %d, want Autonomous (%d)", got, Autonomous)
	}
}

func TestRoleTypeFor_Default(t *testing.T) {
	agents := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{},
	}
	got := RoleTypeFor("unknown", agents)
	if got != Interactive {
		t.Errorf("RoleTypeFor(unknown) = %d, want Interactive (%d) as default", got, Interactive)
	}
}

// sessionStartWriters are the three SessionStart context writers, in the order the settings declare
// them — NOT the order they run in, which is unordered because matching hooks run in parallel
// (ADR-023 E6); no writer may depend on another having run. One entry per writer is the contract
// (#675 K3): the harness budgets each hook's stdout separately, so a chained `a && b && c` made the
// first writer's byte count a tax on the other two and truncated whatever was left. Declaration
// order is asserted anyway, because it is what an operator reads in the manual and in the file.
var sessionStartWriters = []string{
	"af prime --hook",
	"af mail check --inject",
	"af memory check --inject",
}

func assertSessionStartWriters(t *testing.T, hooks map[string]interface{}, roleName string) {
	t.Helper()
	sessionStart, ok := hooks["SessionStart"].([]interface{})
	if !ok || len(sessionStart) != 1 {
		t.Fatalf("%s settings.json SessionStart should hold exactly one matcher entry, got %v", roleName, hooks["SessionStart"])
	}
	hooksList := sessionStart[0].(map[string]interface{})["hooks"].([]interface{})
	if len(hooksList) != len(sessionStartWriters) {
		t.Fatalf("%s SessionStart has %d hook entries, want %d — one per writer, never chained with &&",
			roleName, len(hooksList), len(sessionStartWriters))
	}
	for i, want := range sessionStartWriters {
		cmd := hooksList[i].(map[string]interface{})["command"].(string)
		if !strings.HasSuffix(cmd, want) {
			t.Errorf("%s SessionStart entry %d should end in %q, got: %s", roleName, i, want, cmd)
		}
		if strings.Contains(cmd, "--inject &&") || strings.Contains(cmd, "--hook &&") {
			t.Errorf("%s SessionStart entry %d chains a second writer onto %q: %s", roleName, i, want, cmd)
		}
		if !strings.HasPrefix(cmd, "export PATH=") {
			t.Errorf("%s SessionStart entry %d must carry the PATH export every hook needs, got: %s", roleName, i, cmd)
		}
	}
}

func TestEnsureSettings_Autonomous(t *testing.T) {
	dir := t.TempDir()

	err := EnsureSettings(dir, Autonomous)
	if err != nil {
		t.Fatalf("EnsureSettings(Autonomous) error: %v", err)
	}

	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	content := string(data)

	// Parse and check the SessionStart hook commands specifically. Asserting on the parsed entries
	// rather than the whole file is what stops the UserPromptSubmit occurrence of a verb from
	// satisfying a SessionStart claim (issue #515 Phase 3: injection is SessionStart-only).
	hooks := parsed["hooks"].(map[string]interface{})
	assertSessionStartWriters(t, hooks, "autonomous")

	// Stop hook must reference quality-gate.sh
	if !strings.Contains(content, "quality-gate.sh") {
		t.Error("autonomous settings.json missing quality-gate.sh in Stop hook")
	}
	if !strings.Contains(content, ".agentfactory/hooks/quality-gate.sh") {
		t.Error("autonomous settings.json Stop hook should use .agentfactory/hooks/ path for quality-gate.sh")
	}

	// Stop hooks must use ${AF_ROOT} for worktree-safe path resolution (C12)
	if strings.Contains(content, "$(af root)") {
		t.Error("autonomous settings.json Stop hook must use ${AF_ROOT}, not $(af root)")
	}
	if !strings.Contains(content, "${AF_ROOT}") {
		t.Error("autonomous settings.json Stop hook must reference ${AF_ROOT} for worktree compatibility")
	}

	if !strings.Contains(content, "af compact-handoff") {
		t.Error("autonomous settings.json PreCompact missing 'af compact-handoff'")
	}
}

func TestEnsureSettings_Interactive(t *testing.T) {
	dir := t.TempDir()

	err := EnsureSettings(dir, Interactive)
	if err != nil {
		t.Fatalf("EnsureSettings(Interactive) error: %v", err)
	}

	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	content := string(data)

	// Parse and check the SessionStart hook commands specifically. Interactive now carries the mail
	// writer too: with one entry per writer, mail no longer costs prime or memory any of its budget,
	// and the reason interactive withheld it was that cost (#675 K3).
	hooks := parsed["hooks"].(map[string]interface{})
	assertSessionStartWriters(t, hooks, "interactive")

	// Stop hook must reference quality-gate.sh
	if !strings.Contains(content, "quality-gate.sh") {
		t.Error("interactive settings.json missing quality-gate.sh in Stop hook")
	}
	if !strings.Contains(content, ".agentfactory/hooks/quality-gate.sh") {
		t.Error("interactive settings.json Stop hook should use .agentfactory/hooks/ path for quality-gate.sh")
	}

	// Stop hook must use ${AF_ROOT} for worktree-safe path resolution (C12)
	if strings.Contains(content, "$(af root)") {
		t.Error("interactive settings.json Stop hook must use ${AF_ROOT}, not $(af root)")
	}
	if !strings.Contains(content, "${AF_ROOT}") {
		t.Error("interactive settings.json Stop hook must reference ${AF_ROOT} for worktree compatibility")
	}

	preCompact := hooks["PreCompact"].([]interface{})
	preCompactEntry := preCompact[0].(map[string]interface{})
	preCompactHooksList := preCompactEntry["hooks"].([]interface{})
	preCompactHook := preCompactHooksList[0].(map[string]interface{})
	preCompactCmd := preCompactHook["command"].(string)
	if !strings.Contains(preCompactCmd, "af compact-handoff --interactive") {
		t.Errorf("interactive PreCompact command should contain 'af compact-handoff --interactive', got: %s", preCompactCmd)
	}
}

// TestEnsureSettings_DeniesAskUserQuestion proves the AskUserQuestion hard-disable
// (issue af-69d8bf24) propagates into every generated agent's .claude/settings.json,
// for BOTH role types.
//
//	Scenario: AskUserQuestion is denied fleet-wide
//	  Given an agent of any role type (interactive or autonomous)
//	  When EnsureSettings writes its .claude/settings.json
//	  Then the settings carry a permissions.deny entry for "AskUserQuestion"
//
// Motivation: in some environments the built-in AskUserQuestion tool auto-selects its
// DEFAULT option and times out as if the human answered, fabricating false approvals.
// Denying the tool by name blocks all of its uses, forcing agents to ask in plain text
// and wait for a real reply. EnsureSettings copies the embedded template verbatim, so a
// deny rule in the template is the propagation mechanism this test guards.
func TestEnsureSettings_DeniesAskUserQuestion(t *testing.T) {
	cases := []struct {
		name     string
		roleType RoleType
	}{
		{"Interactive", Interactive},
		{"Autonomous", Autonomous},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s) error: %v", tc.name, err)
			}

			settingsPath := filepath.Join(dir, ".claude", "settings.json")
			data, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}

			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			permsRaw, ok := parsed["permissions"]
			if !ok {
				t.Fatalf("%s settings.json missing top-level \"permissions\" block", tc.name)
			}
			perms, ok := permsRaw.(map[string]interface{})
			if !ok {
				t.Fatalf("%s settings.json \"permissions\" is not an object", tc.name)
			}
			denyRaw, ok := perms["deny"]
			if !ok {
				t.Fatalf("%s settings.json permissions missing \"deny\" array", tc.name)
			}
			deny, ok := denyRaw.([]interface{})
			if !ok {
				t.Fatalf("%s settings.json permissions.deny is not an array", tc.name)
			}

			found := false
			for _, entry := range deny {
				if s, ok := entry.(string); ok && s == "AskUserQuestion" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s settings.json permissions.deny must contain \"AskUserQuestion\", got: %v", tc.name, deny)
			}
		})
	}
}

func TestEnsureSettings_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")

	// .claude/ should not exist before
	if _, err := os.Stat(claudeDir); !os.IsNotExist(err) {
		t.Fatal(".claude/ already exists before EnsureSettings")
	}

	err := EnsureSettings(dir, Interactive)
	if err != nil {
		t.Fatalf("EnsureSettings error: %v", err)
	}

	// .claude/ should exist after
	if _, err := os.Stat(claudeDir); err != nil {
		t.Fatalf(".claude/ not created: %v", err)
	}

	// settings.json should exist
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
}

// TestEnsureSettings_PreToolUseContainment is a STRUCTURAL presence check (Phase 3,
// issue #386): it proves BOTH templates carry the PreToolUse -> af containment-check
// hook after EnsureSettings, for every role type. It is deliberately SUBORDINATE and
// SEPARATE from the behavioral enforcement tests — presence-in-settings is necessary but
// is NOT the detection/correction test. The real interlock behavior is proven by the
// Phase 2 seam tests (af containment-check) and the Phase 4 e2e; do not let this
// structural check stand in for them.
func TestEnsureSettings_PreToolUseContainment(t *testing.T) {
	cases := []struct {
		name     string
		roleType RoleType
	}{
		{"Interactive", Interactive},
		{"Autonomous", Autonomous},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s) error: %v", tc.name, err)
			}

			settingsPath := filepath.Join(dir, ".claude", "settings.json")
			data, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}

			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			// Walk into hooks.PreToolUse[0].hooks[0].command (mirrors the SessionStart
			// idiom above). The PreToolUse lookup is comma-ok-guarded because its very
			// presence is what this test proves.
			hooks := parsed["hooks"].(map[string]interface{})
			preToolUseRaw, ok := hooks["PreToolUse"]
			if !ok {
				t.Fatalf("%s settings.json missing top-level hooks.PreToolUse entry", tc.name)
			}
			preToolUse := preToolUseRaw.([]interface{})
			if len(preToolUse) == 0 {
				t.Fatalf("%s PreToolUse array is empty", tc.name)
			}
			firstEntry := preToolUse[0].(map[string]interface{})

			// Matcher uniquely fingerprints this hook vs. the empty-matcher siblings (D-8:
			// scope to cwd-affecting tools).
			matcher, _ := firstEntry["matcher"].(string)
			if matcher != "Bash|Write|Edit" {
				t.Errorf("%s PreToolUse matcher = %q, want \"Bash|Write|Edit\"", tc.name, matcher)
			}

			hooksList := firstEntry["hooks"].([]interface{})
			if len(hooksList) == 0 {
				t.Fatalf("%s PreToolUse hooks array is empty", tc.name)
			}
			firstHook := hooksList[0].(map[string]interface{})
			cmd := firstHook["command"].(string)

			if !strings.Contains(cmd, "af containment-check") {
				t.Errorf("%s PreToolUse command should run 'af containment-check', got: %s", tc.name, cmd)
			}
			// Carries the same PATH-export prefix as every other direct-af hook. NOT
			// ${AF_ROOT} (that token belongs only to the bash-script Stop hooks).
			if !strings.Contains(cmd, `export PATH="$HOME/go/bin:`) {
				t.Errorf("%s PreToolUse command should carry the export PATH= prefix, got: %s", tc.name, cmd)
			}
		})
	}
}

// TestEnsureSettings_PreToolUseTaskCapacity is the #672 sibling of the containment test above: the
// deterministic sub-agent-dispatch capacity-admission owner is a NEW PreToolUse hook matched on the
// sub-agent tool, running `af dispatch-admit` — the first hook in the tree that may emit a blocking
// permissionDecision:"deny", under the ADR-007 (2026-08-31) enumerated exemption. It must be a
// SEPARATE PreToolUse array entry (not merged into the Bash|Write|Edit containment entry, which keeps
// never-blocking), present for BOTH role types, PATH-export-prefixed like every direct-af hook. The
// containment entry stays at index 0 (TestEnsureSettings_PreToolUseContainment pins that), so this
// selects the entry by its command rather than a fixed index, and asserts its matcher fires on BOTH
// sub-agent tool names — "Agent" (current Claude Code) and "Task" (older builds) — per #669 BROKEN-0.
func TestEnsureSettings_PreToolUseTaskCapacity(t *testing.T) {
	cases := []struct {
		name     string
		roleType RoleType
	}{
		{"Interactive", Interactive},
		{"Autonomous", Autonomous},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s) error: %v", tc.name, err)
			}

			data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}
			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			hooks := parsed["hooks"].(map[string]interface{})
			preToolUse, ok := hooks["PreToolUse"].([]interface{})
			if !ok || len(preToolUse) == 0 {
				t.Fatalf("%s settings.json has no hooks.PreToolUse entries", tc.name)
			}

			var dispatchCmd, dispatchMatcher string
			for _, raw := range preToolUse {
				entry := raw.(map[string]interface{})
				hooksList, _ := entry["hooks"].([]interface{})
				for _, h := range hooksList {
					cmd, _ := h.(map[string]interface{})["command"].(string)
					if strings.Contains(cmd, "af dispatch-admit") {
						dispatchCmd = cmd
						dispatchMatcher, _ = entry["matcher"].(string)
					}
				}
			}

			if dispatchCmd == "" {
				t.Fatalf("%s settings.json has no PreToolUse hook running 'af dispatch-admit' (the #672 capacity owner)", tc.name)
			}
			re, err := regexp.Compile(dispatchMatcher)
			if err != nil {
				t.Fatalf("%s PreToolUse dispatch-admit matcher %q is not a valid regexp: %v", tc.name, dispatchMatcher, err)
			}
			for _, tool := range []string{"Task", "Agent"} {
				if !re.MatchString(tool) {
					t.Errorf("%s PreToolUse dispatch-admit matcher %q must fire on the %q sub-agent tool; the platform "+
						"tool is named \"Agent\" on current Claude Code (#669 BROKEN-0)", tc.name, dispatchMatcher, tool)
				}
			}
			if !strings.Contains(dispatchCmd, `export PATH="$HOME/go/bin:`) {
				t.Errorf("%s PreToolUse dispatch-admit command should carry the export PATH= prefix, got: %s", tc.name, dispatchCmd)
			}
		})
	}
}

// TestSettingsTemplates_StatusLineKeyPinned pins the K6 contract (issue #591): BOTH embedded
// templates carry a top-level statusLine block, their subtrees are byte-identical, and the
// command is the frozen string. It compares ONLY the statusLine subtree, never the whole file:
// the two templates DIVERGE elsewhere (D9 — SessionStart, PreCompact, and the Stop array), so a
// whole-file parity test would spuriously fail.
func TestSettingsTemplates_StatusLineKeyPinned(t *testing.T) {
	load := func(name string) map[string]interface{} {
		data, err := settingsFS.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s is not valid JSON: %v", name, err)
		}
		return m
	}

	auto := load("config/settings-autonomous.json")
	inter := load("config/settings-interactive.json")

	autoSL, ok := auto["statusLine"]
	if !ok {
		t.Fatal("settings-autonomous.json is missing the top-level statusLine key")
	}
	interSL, ok := inter["statusLine"]
	if !ok {
		t.Fatal("settings-interactive.json is missing the top-level statusLine key")
	}

	if !reflect.DeepEqual(autoSL, interSL) {
		t.Errorf("statusLine subtrees diverge across templates:\n autonomous=%#v\n interactive=%#v", autoSL, interSL)
	}

	slMap, ok := autoSL.(map[string]interface{})
	if !ok {
		t.Fatalf("statusLine is not a JSON object: %T", autoSL)
	}
	if got, _ := slMap["type"].(string); got != "command" {
		t.Errorf("statusLine.type = %q, want \"command\"", got)
	}
	cmd, _ := slMap["command"].(string)
	if cmd != frozenStatusLineCmd {
		t.Errorf("statusLine.command = %q, want %q", cmd, frozenStatusLineCmd)
	}
	if !strings.HasSuffix(cmd, "af statusline render") {
		t.Errorf("statusLine.command must end with 'af statusline render', got %q", cmd)
	}
	// refreshInterval must be a bare JSON NUMBER. The host validates it with a zod schema that
	// silently drops anything else to undefined, installing no timer at all — a failure with no
	// error, no log, and a green "the key is present" grep (issue #596 K1).
	refresh, present := slMap["refreshInterval"]
	if !present {
		t.Fatal("statusLine is missing refreshInterval; renders would stay purely event-driven")
	}
	if got, ok := refresh.(float64); !ok || got != float64(frozenStatusLineRefresh) {
		t.Errorf("statusLine.refreshInterval = %#v (%T), want the number %d", refresh, refresh, frozenStatusLineRefresh)
	}
}

// TestEnsureSettings_ProvisionedContentHasKey proves provisioning writes the statusLine key
// (issue #591, K6). EnsureSettings copies the selected embedded template verbatim, so a fresh
// .claude/settings.json must carry the top-level statusLine block with the frozen command for
// both role types (mirrors the role-type walk in TestEnsureSettings_PreToolUseContainment).
func TestEnsureSettings_ProvisionedContentHasKey(t *testing.T) {
	cases := []struct {
		name     string
		roleType RoleType
	}{
		{"Interactive", Interactive},
		{"Autonomous", Autonomous},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s) error: %v", tc.name, err)
			}

			data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}
			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			sl, ok := parsed["statusLine"].(map[string]interface{})
			if !ok {
				t.Fatalf("%s settings.json missing top-level statusLine object", tc.name)
			}
			if cmd, _ := sl["command"].(string); cmd != frozenStatusLineCmd {
				t.Errorf("%s statusLine.command = %q, want %q", tc.name, cmd, frozenStatusLineCmd)
			}
			// The refresh cadence must reach the PROVISIONED file, not just the template:
			// an agent whose settings.json lacks it renders only on events, so its occupancy
			// channel freezes the moment it goes quiet and "stale" stops meaning anything
			// (issue #596 K1).
			if got, _ := sl["refreshInterval"].(float64); got != float64(frozenStatusLineRefresh) {
				t.Errorf("%s statusLine.refreshInterval = %v, want %d", tc.name, sl["refreshInterval"], frozenStatusLineRefresh)
			}
		})
	}
}

// TestEnsureSettings_PostToolUseSubagentObserver is #668 K18's deployment interlock, and it is the
// STRUCTURAL half only — the observer's behaviour (counsel once per episode, never block, silent
// when the mechanism is off) is proven in internal/cmd. It exists because every one of those
// behavioural tests passes against a verb no session ever invokes: PostToolUse/Task is the only
// event that fires when a sub-agent has finished, and if the templates do not carry it the whole
// mechanism is inert with nothing to say so.
func TestEnsureSettings_PostToolUseSubagentObserver(t *testing.T) {
	for _, tc := range []struct {
		name     string
		roleType RoleType
	}{
		{"Interactive", Interactive},
		{"Autonomous", Autonomous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s) error: %v", tc.name, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}

			var parsed struct {
				Hooks map[string][]struct {
					Matcher string `json:"matcher"`
					Hooks   []struct {
						Type    string `json:"type"`
						Command string `json:"command"`
					} `json:"hooks"`
				} `json:"hooks"`
			}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			groups, ok := parsed.Hooks["PostToolUse"]
			if !ok {
				t.Fatalf("%s settings.json missing top-level hooks.PostToolUse entry", tc.name)
			}
			var found bool
			for _, g := range groups {
				for _, h := range g.Hooks {
					if !strings.Contains(h.Command, "af subagent-observe") {
						continue
					}
					found = true
					// The matcher must fire on BOTH sub-agent tool names — "Agent" (current Claude
					// Code) and "Task" (older builds), #669 BROKEN-0 — yet stay non-empty: a
					// PostToolUse hook with an empty matcher runs after EVERY tool call, which for a
					// per-turn verb is a subprocess on every Read the agent does (the D-8 rule).
					if g.Matcher == "" {
						t.Errorf("%s PostToolUse subagent-observe matcher must not be empty (would run on every tool call)", tc.name)
					}
					re, err := regexp.Compile(g.Matcher)
					if err != nil {
						t.Errorf("%s PostToolUse subagent-observe matcher %q is not a valid regexp: %v", tc.name, g.Matcher, err)
					} else {
						for _, tool := range []string{"Task", "Agent"} {
							if !re.MatchString(tool) {
								t.Errorf("%s PostToolUse subagent-observe matcher %q must fire on the %q sub-agent tool", tc.name, g.Matcher, tool)
							}
						}
					}
					if !strings.Contains(h.Command, `export PATH="$HOME/go/bin:`) {
						t.Errorf("%s PostToolUse command should carry the export PATH= prefix, got: %s", tc.name, h.Command)
					}
					if h.Type != "command" {
						t.Errorf("%s PostToolUse hook type = %q, want \"command\"", tc.name, h.Type)
					}
				}
			}
			if !found {
				t.Errorf("%s has no PostToolUse hook running 'af subagent-observe'", tc.name)
			}
		})
	}
}
