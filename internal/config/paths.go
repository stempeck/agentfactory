package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const dotDir = ".agentfactory"
const agentsSubdir = "agents"

func ConfigDir(root string) string           { return filepath.Join(root, dotDir) }
func AgentsDir(root string) string           { return filepath.Join(root, dotDir, agentsSubdir) }
func AgentDir(root, name string) string      { return filepath.Join(root, dotDir, agentsSubdir, name) }
func FactoryConfigPath(root string) string   { return filepath.Join(root, dotDir, "factory.json") }
func AgentsConfigPath(root string) string    { return filepath.Join(root, dotDir, "agents.json") }
func MessagingConfigPath(root string) string { return filepath.Join(root, dotDir, "messaging.json") }
func DispatchConfigPath(root string) string  { return filepath.Join(root, dotDir, "dispatch.json") }
func HooksDir(root string) string            { return filepath.Join(root, dotDir, "hooks") }
func StoreDir(root string) string            { return filepath.Join(root, dotDir, "store") }
func FormulasDir(root string) string         { return filepath.Join(StoreDir(root), "formulas") }

// DetectAgentFromCwd determines the agent name from the working directory
// relative to the factory root. It expects cwd to be under
// .agentfactory/agents/<name>/...
func DetectAgentFromCwd(cwd, root string) (string, error) {
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return "", fmt.Errorf("detecting agent: %w", err)
	}

	parts := strings.Split(rel, string(filepath.Separator))

	// cwd is factory root
	if len(parts) == 0 || parts[0] == "." {
		return "", fmt.Errorf("cannot detect agent: cwd is factory root")
	}

	// Must start with .agentfactory
	if parts[0] != dotDir {
		return "", fmt.Errorf("cannot detect agent: cwd is not inside %s", dotDir)
	}

	// .agentfactory/ only — no agents subdir
	if len(parts) < 2 || parts[1] != agentsSubdir {
		return "", fmt.Errorf("cannot detect agent: cwd is inside %s but not in an agent workspace", dotDir)
	}

	// .agentfactory/agents/ only — no agent name
	if len(parts) < 3 {
		return "", fmt.Errorf("cannot detect agent: cwd is the agents directory, not inside a specific agent")
	}

	return parts[2], nil
}

// FindLocalRoot returns the nearest ancestor directory containing
// .agentfactory/ (either factory.json or .factory-root). This is
// the "local project root" — the worktree root for worktree agents,
// or the factory root for non-worktree agents.
func FindLocalRoot(startDir string) (string, error) {
	dir := startDir
	for {
		if _, err := os.Stat(FactoryConfigPath(dir)); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, dotDir, ".factory-root")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in an agentfactory workspace")
		}
		dir = parent
	}
}
