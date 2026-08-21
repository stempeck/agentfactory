package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const dotDir = ".agentfactory"
const agentsSubdir = "agents"

func ConfigDir(root string) string            { return filepath.Join(root, dotDir) }
func AgentsDir(root string) string            { return filepath.Join(root, dotDir, agentsSubdir) }
func AgentDir(root, name string) string       { return filepath.Join(root, dotDir, agentsSubdir, name) }
func FactoryConfigPath(root string) string    { return filepath.Join(root, dotDir, "factory.json") }
func AgentsConfigPath(root string) string     { return filepath.Join(root, dotDir, "agents.json") }
func MessagingConfigPath(root string) string  { return filepath.Join(root, dotDir, "messaging.json") }
func DispatchConfigPath(root string) string   { return filepath.Join(root, dotDir, "dispatch.json") }
func StartupConfigPath(root string) string    { return filepath.Join(root, dotDir, "startup.json") }
func ModelsConfigPath(root string) string     { return filepath.Join(root, dotDir, "models.json") }
func TelemetryConfigPath(root string) string  { return filepath.Join(root, dotDir, "telemetry.json") }
func TelemetryDir(root string) string         { return filepath.Join(root, dotDir, "telemetry") }
func StatuslineConfigPath(root string) string { return filepath.Join(root, dotDir, "statusline.json") }
func StatuslineDir(root string) string        { return filepath.Join(root, dotDir, "statusline") }
func HooksDir(root string) string             { return filepath.Join(root, dotDir, "hooks") }

// GitHooksDir is the af-managed git hooks directory (issue #371). It is
// DISTINCT from HooksDir (which holds the Claude quality/fidelity gates): git
// hooks are activated per agent session via core.hooksPath, and writing them
// here keeps the customer's .git/ untouched (ADR-017-clean).
func GitHooksDir(root string) string         { return filepath.Join(root, dotDir, "githooks") }
func StoreDir(root string) string            { return filepath.Join(root, dotDir, "store") }
func FormulasDir(root string) string         { return filepath.Join(StoreDir(root), "formulas") }
func BuildHostConfigPath(root string) string { return filepath.Join(root, dotDir, "build-host.json") }
func AgentsMdPath(root string) string        { return filepath.Join(root, dotDir, "AGENTS.md") }

// FormulaStorePath is the absolute path of a single store formula. It exists so the
// production sites that must agree on one artifact compose it once rather than
// independently (issue #563 was a divergence between two spellings of this path).
// Test code deliberately keeps composing it by hand: a test that derived its expectation
// from this constructor could not detect a fault inside it.
func FormulaStorePath(root, name string) string {
	return filepath.Join(FormulasDir(root), name+".formula.toml")
}

// StatuslineSessionsDir is the per-session statusline snapshot directory. It exists for the same
// reason as FormulaStorePath: the sites that must agree on one artifact compose it once rather
// than independently (issue #563 was a divergence between two spellings of one path). Here the
// sites are the statusline renderer that WRITES occupancy snapshots and the reader that
// classifies them (issue #596) — a drift between those two spellings would present as every
// agent's channel reading "none", i.e. as a silently dark factory.
// Test code deliberately keeps composing it by hand: a test that derived its expectation from
// this constructor could not detect a fault inside it.
func StatuslineSessionsDir(root string) string {
	return filepath.Join(StatuslineDir(root), "sessions")
}

// MemoryDir is the agent learnings vault: one directory per agent, one Markdown note per file.
// It sits at the TOP level of .agentfactory/ deliberately, because that is the only place
// nothing else owns: it is outside .agentfactory/worktrees/, which every teardown path is free
// to destroy, and outside .agentfactory/agents/, which af install rewrites. A vault under either
// would be erased by a routine operation, which is the exact loss this subsystem exists to
// prevent. Git exclusion is NOT the reason — both placements are ignored today (.gitignore:48
// catches this one, .gitignore:59 catches a nested one), contrary to the peer-review note the
// Phase-1 plan carries; verified with git check-ignore on 2026-08-15. Durability is the reason.
func MemoryDir(root string) string { return filepath.Join(root, dotDir, "memory") }

// AgentMemoryDir is one agent's vault. It exists for the same reason as FormulaStorePath: the
// sites that must agree on one artifact compose it once rather than independently (issue #563
// was a divergence between two spellings of one path). Here the sites are the store that WRITES
// notes, the CLI verbs that list and mark them, and the startup hook that injects a slice — a
// drift between those spellings would present as learnings that survive teardown into a
// directory nothing ever reads, which is indistinguishable from having lost them.
// Test code deliberately keeps composing it by hand: a test that derived its expectation from
// this constructor could not detect a fault inside it.
func AgentMemoryDir(root, agent string) string {
	return filepath.Join(MemoryDir(root), agent)
}

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
