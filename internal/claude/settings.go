package claude

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stempeck/agentfactory/internal/config"
)

//go:embed config/*.json
var settingsFS embed.FS

// RoleType represents the type of agent role.
type RoleType int

const (
	Interactive RoleType = iota
	Autonomous
)

// RoleTypeFor determines role type from agents.json config.
func RoleTypeFor(role string, agents *config.AgentConfig) RoleType {
	entry, ok := agents.Agents[role]
	if !ok {
		return Interactive
	}
	if entry.Type == "autonomous" {
		return Autonomous
	}
	return Interactive
}

// EnsureSettings writes the appropriate settings.json to the agent's .claude/ dir.
func EnsureSettings(workDir string, roleType RoleType) error {
	claudeDir := filepath.Join(workDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return fmt.Errorf("creating .claude dir: %w", err)
	}

	var templateName string
	if roleType == Autonomous {
		templateName = "config/settings-autonomous.json"
	} else {
		templateName = "config/settings-interactive.json"
	}

	data, err := settingsFS.ReadFile(templateName)
	if err != nil {
		return fmt.Errorf("reading settings template: %w", err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	return os.WriteFile(settingsPath, data, 0644)
}
