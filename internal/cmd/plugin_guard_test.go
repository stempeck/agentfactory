package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const guardTool = "mcp__plugin_acme-int-plugin_srv__op"

func guardPayload(event, tool, cwd string) string {
	b, _ := json.Marshal(map[string]any{
		"hook_event_name": event,
		"tool_name":       tool,
		"tool_input":      map[string]any{"q": "x"},
		"cwd":             cwd,
		"session_id":      "s-1",
	})
	return string(b)
}

func invokeGuardEvent(t *testing.T, kind, payload string) (string, error) {
	t.Helper()
	c := &cobra.Command{}
	c.SetContext(t.Context())
	c.SetIn(strings.NewReader(payload))
	var out, errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	err := pluginGuardEventCmd.RunE(c, []string{kind})
	return out.String(), err
}

type guardDecision struct {
	HookSpecificOutput struct {
		HookEventName string `json:"hookEventName"`
		Decision      struct {
			Behavior string `json:"behavior"`
			Message  string `json:"message"`
		} `json:"decision"`
		Action string `json:"action"`
	} `json:"hookSpecificOutput"`
}

func decodeGuardDecision(t *testing.T, out string) guardDecision {
	t.Helper()
	var d guardDecision
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &d); err != nil {
		t.Fatalf("guard stdout is not a hook decision: %v\n%q", err, out)
	}
	return d
}

// guardEnv is a worktree agent session (cwd = its agent dir) with the report recorder installed.
func guardEnv(t *testing.T, withPin bool) (factoryRoot, agentDir string, recs *[]recordedReport) {
	t.Helper()
	worktreeRoot, agentDir := setupWorktreeContainmentEnv(t, "solver")
	factoryRoot = containmentFactoryRoot(worktreeRoot)
	if withPin {
		snap := filepath.Join(snapshotParent(factoryRoot, intBName), strings.Repeat("a", 64))
		pinFixtureWrite(t, agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{
			pinFixtureBinding(t, factoryRoot, intBName, snap, true),
		}})
	}
	t.Chdir(agentDir)
	return factoryRoot, agentDir, installIntegrationReportRecorder(t)
}

func TestGuardEvent_AskDeniedOnceMailed(t *testing.T) {
	if !pluginGuardEventCmd.Hidden {
		t.Error("guard-event is plumbing for a settings hook; it must be Hidden from af plugin --help")
	}
	factoryRoot, agentDir, recs := guardEnv(t, true)

	for i := 0; i < 2; i++ {
		out, err := invokeGuardEvent(t, "permission", guardPayload("PermissionRequest", guardTool, agentDir))
		if err != nil {
			t.Fatalf("permission #%d: guard must exit 0, got %v", i+1, err)
		}
		d := decodeGuardDecision(t, out)
		if d.HookSpecificOutput.HookEventName != "PermissionRequest" || d.HookSpecificOutput.Decision.Behavior != "deny" {
			t.Errorf("permission #%d: want a PermissionRequest deny, got %+v (%q)", i+1, d.HookSpecificOutput, out)
		}
		if !strings.Contains(d.HookSpecificOutput.Decision.Message, "acme-int") {
			t.Errorf("permission #%d: deny message should name the integration, got %q", i+1, d.HookSpecificOutput.Decision.Message)
		}
	}

	denied := reportsWithPrefix(*recs, "INTEGRATION_GUARD_DENIED acme-int: permission "+guardTool)
	if len(denied) != 1 {
		t.Fatalf("want exactly 1 INTEGRATION_GUARD_DENIED mail across two identical events, got %d: %+v", len(denied), *recs)
	}
	if denied[0].to != "manager" {
		t.Errorf("guard report recipient = %q, want manager", denied[0].to)
	}
	if _, err := os.Stat(filepath.Join(factoryRoot, ".runtime", "integration_report", "acme-int.guard-denied")); err != nil {
		t.Errorf("no guard-denied record for acme-int: %v", err)
	}

	out, err := invokeGuardEvent(t, "elicitation", guardPayload("Elicitation", guardTool, agentDir))
	if err != nil {
		t.Fatalf("elicitation: guard must exit 0, got %v", err)
	}
	d := decodeGuardDecision(t, out)
	if d.HookSpecificOutput.HookEventName != "Elicitation" || d.HookSpecificOutput.Action != "decline" {
		t.Errorf("elicitation: want an Elicitation decline, got %+v (%q)", d.HookSpecificOutput, out)
	}

	out, err = invokeGuardEvent(t, "permission", guardPayload("PermissionRequest", "Bash", agentDir))
	if err != nil {
		t.Fatalf("unattributable permission: guard must exit 0, got %v", err)
	}
	if d := decodeGuardDecision(t, out); d.HookSpecificOutput.Decision.Behavior != "deny" {
		t.Errorf("an unattributable permission in a bound session is still denied, got %q", out)
	}
	if len(reportsWithPrefix(*recs, "INTEGRATION_GUARD_DENIED unknown: permission Bash")) != 1 {
		t.Errorf("a tool that maps to no pinned plugin is attributed to unknown; recorded %+v", *recs)
	}
}

func TestGuardEvent_PassThroughWithoutPin(t *testing.T) {
	cases := []struct{ kind, payload string }{
		{"permission", guardPayload("PermissionRequest", guardTool, "")},
		{"elicitation", guardPayload("Elicitation", guardTool, "")},
		{"permission", ""},
	}
	t.Run("no_pin_emits_no_decision", func(t *testing.T) {
		factoryRoot, _, recs := guardEnv(t, false)
		for _, c := range cases {
			out, err := invokeGuardEvent(t, c.kind, c.payload)
			if err != nil || strings.TrimSpace(out) != "" {
				t.Errorf("%s %q without a pin: want pass-through (no stdout, nil error), got %q, %v", c.kind, c.payload, out, err)
			}
		}
		if len(*recs) != 0 {
			t.Errorf("pass-through must mail nothing, got %+v", *recs)
		}
		if _, err := os.Stat(filepath.Join(factoryRoot, ".runtime", "integration_report")); !os.IsNotExist(err) {
			t.Errorf("pass-through must write no record (stat err %v)", err)
		}
	})
	t.Run("the_pin_is_the_switch", func(t *testing.T) {
		_, agentDir, _ := guardEnv(t, true)
		out, err := invokeGuardEvent(t, "permission", guardPayload("PermissionRequest", guardTool, agentDir))
		if err != nil || strings.TrimSpace(out) == "" {
			t.Errorf("the same event in a pinned session must be decided; got %q, %v", out, err)
		}
	})
	t.Run("malformed_payload_with_pin_never_blocks", func(t *testing.T) {
		guardEnv(t, true)
		if _, err := invokeGuardEvent(t, "permission", "{not json"); err != nil {
			t.Errorf("a malformed payload must exit 0 (ADR-007), got %v", err)
		}
	})
}

// guardEnvPlugins is guardEnv with a pin binding each integration to the Claude plugins given, in order.
func guardEnvPlugins(t *testing.T, plugins map[string][]string, order []string) (agentDir string, recs *[]recordedReport) {
	t.Helper()
	worktreeRoot, agentDir := setupWorktreeContainmentEnv(t, "solver")
	factoryRoot := containmentFactoryRoot(worktreeRoot)
	var bindings []integrationPinBinding
	for i, name := range order {
		b := pinFixtureBinding(t, factoryRoot, name, filepath.Join(snapshotParent(factoryRoot, name), strings.Repeat(string(rune('a'+i)), 64)), true)
		b.ClaudePlugins = plugins[name]
		bindings = append(bindings, b)
	}
	pinFixtureWrite(t, agentDir, integrationPin{Formula: "f", Bindings: bindings})
	t.Chdir(agentDir)
	return agentDir, installIntegrationReportRecorder(t)
}

// Claude Code names a plugin's tools mcp__plugin_<plugin>_<server>__<tool> with every character outside
// [a-zA-Z0-9_-] replaced by "_", and sends PermissionRequest's server as mcp_server.name.
func TestGuardEvent_AttributesByClaudeCodeNaming(t *testing.T) {
	t.Run("normalised_plugin_name", func(t *testing.T) {
		agentDir, recs := guardEnvPlugins(t, map[string][]string{"dot-int": {"acme.tools"}}, []string{"dot-int"})
		if _, err := invokeGuardEvent(t, "permission", guardPayload("PermissionRequest", "mcp__plugin_acme_tools_srv__op", agentDir)); err != nil {
			t.Fatal(err)
		}
		if n := len(reportsWithPrefix(*recs, "INTEGRATION_GUARD_DENIED dot-int:")); n != 1 {
			t.Errorf("plugin acme.tools raises tools named mcp__plugin_acme_tools_*; want the denial attributed to dot-int, got %+v", *recs)
		}
	})

	t.Run("longest_plugin_prefix_wins", func(t *testing.T) {
		agentDir, recs := guardEnvPlugins(t, map[string][]string{"short-int": {"foo"}, "long-int": {"foo_bar"}}, []string{"short-int", "long-int"})
		if _, err := invokeGuardEvent(t, "permission", guardPayload("PermissionRequest", "mcp__plugin_foo_bar_srv__op", agentDir)); err != nil {
			t.Fatal(err)
		}
		if n := len(reportsWithPrefix(*recs, "INTEGRATION_GUARD_DENIED long-int:")); n != 1 {
			t.Errorf("a tool of plugin foo_bar must not be claimed by plugin foo; recorded %+v", *recs)
		}
	})

	t.Run("longest_plugin_prefix_wins_when_listed_first", func(t *testing.T) {
		agentDir, recs := guardEnvPlugins(t, map[string][]string{"short-int": {"foo"}, "long-int": {"foo_bar"}}, []string{"long-int", "short-int"})
		if _, err := invokeGuardEvent(t, "permission", guardPayload("PermissionRequest", "mcp__plugin_foo_bar_srv__op", agentDir)); err != nil {
			t.Fatal(err)
		}
		if n := len(reportsWithPrefix(*recs, "INTEGRATION_GUARD_DENIED long-int:")); n != 1 {
			t.Errorf("binding order must not decide attribution: a later, shorter plugin foo must not claim plugin foo_bar's tool; recorded %+v", *recs)
		}
	})

	t.Run("permission_mcp_server_object", func(t *testing.T) {
		agentDir, recs := guardEnvPlugins(t, map[string][]string{"srv-int": {"srv-plugin"}}, []string{"srv-int"})
		b, _ := json.Marshal(map[string]any{
			"hook_event_name": "PermissionRequest",
			"tool_name":       "op",
			"tool_input":      map[string]any{},
			"mcp_server":      map[string]any{"name": "plugin:srv-plugin:srv", "source": "plugin"},
			"cwd":             agentDir,
		})
		if _, err := invokeGuardEvent(t, "permission", string(b)); err != nil {
			t.Fatal(err)
		}
		if n := len(reportsWithPrefix(*recs, "INTEGRATION_GUARD_DENIED srv-int:")); n != 1 {
			t.Errorf("PermissionRequest names its MCP server in mcp_server.name; want the denial attributed to srv-int, got %+v", *recs)
		}
	})
}

// Claude Code shows an Elicitation decline's top-level reason to the agent, else a generic "Elicitation
// denied by hook".
func TestGuardEvent_ElicitationDeclineCarriesReason(t *testing.T) {
	_, agentDir, _ := guardEnv(t, true)
	out, err := invokeGuardEvent(t, "elicitation", guardPayload("Elicitation", guardTool, agentDir))
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Reason             string `json:"reason"`
		HookSpecificOutput struct {
			Action string `json:"action"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &d); err != nil {
		t.Fatalf("guard stdout is not JSON: %v\n%q", err, out)
	}
	if d.HookSpecificOutput.Action != "decline" {
		t.Fatalf("want a decline, got %q", out)
	}
	if !strings.Contains(d.Reason, "acme-int") {
		t.Errorf("the decline's reason should name the integration and why; got reason %q", d.Reason)
	}
}

// A plugin's tool can move the session's cwd into the pinned snapshot, outside every agent dir. The session
// still carries the AF_ROLE and AF_WORKTREE its launch line exported, and those locate the pin.
func TestGuardEvent_CwdInsidePinnedSnapshotStillDenies(t *testing.T) {
	factoryRoot, _, recs := guardEnv(t, true)
	t.Setenv("AF_ROLE", "solver")
	snapCwd := filepath.Join(snapshotParent(factoryRoot, intBName), strings.Repeat("a", 64), "claude-plugin")
	if err := os.MkdirAll(snapCwd, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := invokeGuardEvent(t, "permission", guardPayload("PermissionRequest", guardTool, snapCwd))
	if err != nil {
		t.Fatalf("guard must exit 0, got %v", err)
	}
	if d := decodeGuardDecision(t, out); d.HookSpecificOutput.Decision.Behavior != "deny" {
		t.Fatalf("a bound session whose cwd is inside its pinned snapshot must still be denied, got %q", out)
	}
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_GUARD_DENIED acme-int: permission "+guardTool)); n != 1 {
		t.Errorf("want 1 INTEGRATION_GUARD_DENIED acme-int report, got %d: %+v", n, *recs)
	}
}
