package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const guardHookPrefix = "export PATH=\"$HOME/go/bin:$HOME/.local/bin:$HOME/bin:$PATH\" && "

func settingsHookCommands(t *testing.T, role RoleType, event string) []string {
	t.Helper()
	dir := t.TempDir()
	if err := EnsureSettings(dir, role); err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	var cmds []string
	for _, entry := range parsed.Hooks[event] {
		for _, h := range entry.Hooks {
			cmds = append(cmds, h.Command)
		}
	}
	return cmds
}

// The af-owned session guard rides the autonomous template only: an interactive pane has a human to
// answer its permission prompts (D3).
func TestEnsureSettings_GuardEventHooks(t *testing.T) {
	for event, want := range map[string]string{
		"PermissionRequest": "af plugin guard-event permission",
		"Elicitation":       "af plugin guard-event elicitation",
	} {
		t.Run("autonomous_"+event, func(t *testing.T) {
			cmds := settingsHookCommands(t, Autonomous, event)
			if len(cmds) != 1 || cmds[0] != guardHookPrefix+want {
				t.Errorf("autonomous %s hooks = %q, want exactly [%q]", event, cmds, guardHookPrefix+want)
			}
		})
		t.Run("interactive_"+event, func(t *testing.T) {
			for _, c := range settingsHookCommands(t, Interactive, event) {
				if strings.Contains(c, "guard-event") {
					t.Errorf("interactive %s carries the session guard %q; a human answers that pane", event, c)
				}
			}
		})
	}
}
