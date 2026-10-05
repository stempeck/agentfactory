package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
)

var pluginGuardEventCmd = &cobra.Command{
	Use:    "guard-event <permission|elicitation>",
	Short:  "Decide a Claude Code permission or elicitation event for an integration-bound session",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE:   runPluginGuardEvent,
}

// Elicitation names its server in mcp_server_name, PermissionRequest in mcp_server.name.
type guardEventPayload struct {
	ToolName      string `json:"tool_name"`
	MCPServerName string `json:"mcp_server_name"`
	MCPServer     struct {
		Name string `json:"name"`
	} `json:"mcp_server"`
	Cwd string `json:"cwd"`
}

func (p guardEventPayload) serverName() string {
	if p.MCPServerName != "" {
		return p.MCPServerName
	}
	return p.MCPServer.Name
}

// runPluginGuardEvent returns nil on every path (ADR-007): the decision lives in the JSON it prints. An
// autonomous pane has nobody to answer a runtime prompt, so in a session whose pin says integrations are
// bound every permission request is denied and every elicitation declined. Without a pin it passes through,
// so a session with no integration behaves exactly as before.
func runPluginGuardEvent(cmd *cobra.Command, args []string) error {
	if args[0] != "permission" && args[0] != "elicitation" {
		return nil
	}
	var p guardEventPayload
	raw, _ := io.ReadAll(cmd.InOrStdin())
	_ = json.Unmarshal(raw, &p)
	if p.Cwd == "" {
		if wd, err := getWd(); err == nil {
			p.Cwd = wd
		}
	}
	root, agentDir, ok := guardSessionDirs(p.Cwd)
	if !ok {
		return nil
	}
	pin, found, err := readIntegrationPin(agentDir)
	if !found {
		return nil
	}

	var bindings []integrationPinBinding
	if err == nil {
		bindings = pin.Bindings
	}
	integration := attributeGuardEvent(bindings, p)
	subject := strings.TrimSpace(fmt.Sprintf("INTEGRATION_GUARD_DENIED %s: %s %s", integration, args[0], guardEventSource(p)))
	body := fmt.Sprintf("%s\n\nThe agent's session is integration-bound and unattended, so af denied the runtime prompt instead of leaving the pane waiting. Run the integration's operation from an interactive session, or grant it in the plugin's settings.", subject)
	if rerr := reportIntegration(root, integration, integrationReportGuardDenied, subject, body); rerr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", rerr)
	}

	emitGuardDecision(cmd.OutOrStdout(), args[0], fmt.Sprintf("af denied this %s prompt for integration %s: an integration-bound agent session is unattended, so nobody can answer it.", args[0], integration))
	return nil
}

// guardSessionDirs resolves the factory root, where report records live, and the agent dir, where the pin
// lives; for a worktree agent those are different trees.
func guardSessionDirs(cwd string) (root, agentDir string, ok bool) {
	if cwd == "" {
		return "", "", false
	}
	root, err := resolveInvokerRootWarn(cwd, io.Discard)
	if err != nil {
		return "", "", false
	}
	name, err := resolveAgentName(cwd, root)
	if err != nil || name == "" {
		return "", "", false
	}
	boundary, _ := resolveBoundary(cwd)
	if boundary == "" {
		boundary = root
	}
	return root, config.AgentDir(boundary, name), true
}

// toolNameUnsafe is what Claude Code replaces with "_" when it builds a tool name from a plugin name.
var toolNameUnsafe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// attributeGuardEvent maps the event to the pinned integration whose Claude plugin raised it. Claude Code
// names a plugin's MCP server plugin:<plugin>:<server> and its tools mcp__plugin_<plugin>_<server>__<tool>.
// Plugin foo's prefix also matches plugin foo_bar's tools, so the longest match wins.
func attributeGuardEvent(bindings []integrationPinBinding, p guardEventPayload) string {
	server := p.serverName()
	integration, longest := "unknown", 0
	for _, b := range bindings {
		for _, plugin := range b.ClaudePlugins {
			tool := "mcp__plugin_" + toolNameUnsafe.ReplaceAllString(plugin, "_") + "_"
			if (strings.HasPrefix(p.ToolName, tool) || strings.HasPrefix(server, "plugin:"+plugin+":")) && len(plugin) > longest {
				integration, longest = b.Name, len(plugin)
			}
		}
	}
	return integration
}

func guardEventSource(p guardEventPayload) string {
	if p.ToolName != "" {
		return p.ToolName
	}
	return p.serverName()
}

// emitGuardDecision writes the event's own hookSpecificOutput, shapes taken from the Claude Code hook
// output schema: PermissionRequest takes a deny decision, Elicitation a decline action. An Elicitation shows
// the agent only the top-level reason, so the decline carries the message there.
func emitGuardDecision(out io.Writer, kind, message string) {
	type permissionDecision struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message"`
	}
	var payload struct {
		Reason             string `json:"reason,omitempty"`
		HookSpecificOutput struct {
			HookEventName string              `json:"hookEventName"`
			Decision      *permissionDecision `json:"decision,omitempty"`
			Action        string              `json:"action,omitempty"`
		} `json:"hookSpecificOutput"`
	}
	if kind == "elicitation" {
		payload.Reason = message
		payload.HookSpecificOutput.HookEventName = "Elicitation"
		payload.HookSpecificOutput.Action = "decline"
	} else {
		payload.HookSpecificOutput.HookEventName = "PermissionRequest"
		payload.HookSpecificOutput.Decision = &permissionDecision{Behavior: "deny", Message: message}
	}
	_ = json.NewEncoder(out).Encode(&payload)
}
