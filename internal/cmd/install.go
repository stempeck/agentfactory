package cmd

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/templates"
)

//go:embed install_hooks/*
var hooksFS embed.FS

//go:embed install_formulas/*
var formulasFS embed.FS

//go:embed install_skills/*
var skillsFS embed.FS

var installInitFlag bool
var installAgentsFlag bool
var installNoBuildFlag bool
var installLitellmFlag bool
var installNoTelemetryFlag bool
var installLitellmAuthFlag string

var installCmd = &cobra.Command{
	Use:   "install [role]",
	Short: "Initialize factory or provision an agent",
	Long: `Initialize a new factory workspace (--init) or provision an agent role.

Factory initialization creates the config directory, starter configs,
issue store database, and hooks directory.

Agent provisioning creates the agent directory, renders CLAUDE.md from
the role template, and writes Claude Code settings.json with hooks.

Redeploy all formula-derived agents (--agents) regenerates every specialist
template and reinstalls the factory in one command, resolving the AF source
tree and project directory for you. It runs agent-gen-all.sh (regenerate
templates + rebuild) first, then quickstart.sh (full bootstrap)
non-interactively. Add --no-build
to skip agent-gen-all.sh's duplicate rebuild (quickstart.sh always rebuilds the
binary). Add --litellm to also set up the gateway for running agents on OpenAI
models; it asks for your OpenAI API key the first time and reuses the stored
key on later runs. Add --no-telemetry to skip the telemetry backend and turn
recording off — without it, a successful redeploy turns recording on, resetting
any manual af telemetry toggle from before. It stops all agents (the wrapped 'af down --all' never restarts them),
so run 'af up' afterward. It requires an already-initialized factory:
agent-gen-all.sh runs first and aborts if .agentfactory/store/formulas/ is
absent, before quickstart.sh could bootstrap a cold factory; for a first-time or
cold-start setup run quickdocker.sh/quickstart.sh first. Residual risk: the
command is not transactional, so a mid-run failure can leave agents down and the
factory half-regenerated -- check the streamed exit code and end-state. A green
unit test confirms dispatch, not factory health; behavioral success requires the
e2e cold-start, 'af up', 'af sling', PR check.

AUTHORITY: --agents wraps 'af down --all', a factory-wide teardown and therefore
an operator action. Inside an af-managed agent session 'af install --agents'
refuses — run it from a host shell.`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().BoolVar(&installInitFlag, "init", false, "Initialize a new factory workspace")
	installCmd.Flags().BoolVar(&installAgentsFlag, "agents", false,
		"Regenerate and reinstall all formula-derived agents (runs agent-gen-all.sh then quickstart.sh)")
	installCmd.Flags().BoolVar(&installNoBuildFlag, "no-build", false,
		"With --agents: skip ONLY agent-gen-all.sh's duplicate rebuild — quickstart.sh always rebuilds the binary, so the embedded identity is never left stale")
	installCmd.Flags().BoolVar(&installLitellmFlag, "litellm", false,
		"With --agents: also set up the gateway for running agents on OpenAI models (asks for your OpenAI API key the first time; see USING_LITELLM.md)")
	installCmd.Flags().BoolVar(&installNoTelemetryFlag, "no-telemetry", false,
		"With --agents: skip the telemetry backend and turn recording off (without it, a successful redeploy turns recording on)")
	// Default is "" (unset), NOT "api-key": an empty sentinel distinguishes "no
	// flag passed" from an explicit "--litellm-auth=api-key" without depending on
	// cmd.Flags().Changed(), whose underlying pflag.Flag.Changed bit is never
	// reset across repeated Execute() calls on the same long-lived *cobra.Command
	// (harmless in real usage — one process, one Execute — but see decisions.md D9
	// for why a Changed()-based design broke test-suite reuse of installCmd).
	installCmd.Flags().StringVar(&installLitellmAuthFlag, "litellm-auth", "",
		"With --agents: choose the upstream auth mode for the LiteLLM gateway (api-key or codex-subscription); requires --litellm. Overrides the persisted gateway auth mode record and any inferred default.")
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	// --agents is checked before --init so its mutual-exclusion guard fires:
	// af install --agents --init must be rejected, not silently run --init.
	if installAgentsFlag {
		if installInitFlag {
			return fmt.Errorf("--agents and --init are mutually exclusive")
		}
		if len(args) > 0 {
			return fmt.Errorf("--agents takes no role argument (usage: af install --agents)")
		}
		return runInstallAgents(cmd)
	}
	if installLitellmFlag || installNoTelemetryFlag || installLitellmAuthFlag != "" {
		return fmt.Errorf("--litellm, --litellm-auth and --no-telemetry require --agents")
	}
	if installInitFlag {
		return runInstallInit(cmd)
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: af install <role> or af install --init")
	}
	return runInstallRole(cmd, args[0])
}

// warnIfEnclosingFactory reports (but never blocks — ADR-017) when af install --init
// runs inside the subtree of an existing enclosing factory. It is a bootstrap
// warning: it describes geometry and claims no identity, so it MUST NOT convert into
// a refusal. Env-free ancestor detection lives in config.FindEnclosingRoot (K5/K11).
func warnIfEnclosingFactory(cmd *cobra.Command, cwd string) {
	if enclosing, _ := config.FindEnclosingRoot(cwd); enclosing != "" {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: af install --init is running inside the subtree of an existing factory %s; "+
				"the new factory will be nested inside it (proceeding)\n",
			enclosing)
	}
}

func runInstallInit(cmd *cobra.Command) error {
	cwd, err := getWd()
	if err != nil {
		return err
	}

	if data, err := os.ReadFile(filepath.Join(config.ConfigDir(cwd), ".factory-root")); err == nil {
		return fmt.Errorf("cannot run af install --init inside a worktree (factory root: %s)", strings.TrimSpace(string(data)))
	}

	// K11 (#519 Phase 3): report — but never block (ADR-017) — an enclosing factory
	// above cwd. Unlike the own-.factory-root refusal above, this is a geometry
	// warning: a nested-but-deliberate inner factory is allowed to proceed.
	warnIfEnclosingFactory(cmd, cwd)

	// 1. Verify Python 3.12 before ANY filesystem mutation (C-16).
	//    af install --init must abort cleanly if Python is missing or wrong
	//    version — otherwise a mid-run failure leaves partial state that a
	//    subsequent re-run cannot detect or roll back.
	if err := checkPython312(); err != nil {
		return fmt.Errorf("af install --init requires Python 3.12: %w", err)
	}

	if err := checkPythonMCPDeps(cwd, cmd.OutOrStdout()); err != nil {
		return err
	}

	if err := migrateBeadsDir(cwd); err != nil {
		return fmt.Errorf("migrating legacy store directory: %w", err)
	}

	if err := cleanLegacyGateLocks(); err != nil {
		return fmt.Errorf("cleaning legacy gate locks: %w", err)
	}

	// 2. Create .agentfactory/ directory
	configDir := config.ConfigDir(cwd)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("creating .agentfactory directory: %w", err)
	}

	// 2b. Create .agentfactory/agents/ directory
	if err := os.MkdirAll(config.AgentsDir(cwd), 0755); err != nil {
		return fmt.Errorf("creating agents directory: %w", err)
	}

	// 2c. Create .agentfactory/memory/ — the learnings vault root. It sits beside agents/ rather
	// than inside it because it must be durable: agents/ is rewritten by af install and
	// worktrees/ is destroyed by teardown, and a learning has to outlive both.
	if err := os.MkdirAll(config.MemoryDir(cwd), 0755); err != nil {
		return fmt.Errorf("creating memory directory: %w", err)
	}

	// 3. Write starter configs (only if they don't exist — idempotent)
	starterConfigs := map[string]string{
		// Built from the in-code defaults (incl. the C-3 git_identity) so the on-disk
		// literal cannot drift from internal/config's constants (issue #371 Gap-6).
		"factory.json": config.DefaultFactoryConfigJSON(),
		// The directive names the verbs on both halves of the loop (#515). "Read your memory"
		// alone described a habit with no mechanism behind it; naming af memory list and
		// af memory add makes the seed a runnable instruction, which instruction_reality_test.go
		// then holds against the cobra tree. No backticks: this is a Go raw string literal.
		"agents.json":    `{"agents":{"manager":{"type":"interactive","description":"Interactive agent for human-supervised work","directive":"Read your memory (af memory list) and docs, and prove it. Record durable learnings with af memory add."},"supervisor":{"type":"autonomous","description":"Autonomous agent for independent task execution","directive":"Read your memory (af memory list) and docs, and prove it. Record durable learnings with af memory add."}}}`,
		"messaging.json": `{"groups":{"all":["manager","supervisor"]}}`,
		"dispatch.json":  `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`,
		// Opinionated defaults for fresh installs (see TestLoadStartupConfig_ScaffoldLoads).
		"startup.json": `{"agents":["manager"],"quality":"default","fidelity":"default","start_dispatch":true,"watchdog_agents":["manager","supervisor"]}`,
		// Per-agent model registry (issue #480); the default model tracks quickstart.sh
		// (see TestInstallScaffold_DefaultModel_MatchesQuickstart).
		"models.json": `{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-opus-5","ANTHROPIC_DEFAULT_OPUS_MODEL":"claude-opus-5","ANTHROPIC_DEFAULT_SONNET_MODEL":"claude-sonnet-5"},"lmstudio":{"ANTHROPIC_BASE_URL":"http://localhost:1234","ANTHROPIC_AUTH_TOKEN":"lm-studio","ANTHROPIC_MODEL":"qwen2.5-coder-32b","ANTHROPIC_API_KEY":""},"sonnet-5":{"ANTHROPIC_MODEL":"claude-sonnet-5"},"opus-4-8":{"ANTHROPIC_MODEL":"claude-opus-4-8"},"opus-5":{"ANTHROPIC_MODEL":"claude-opus-5"},"fable-5":{"ANTHROPIC_MODEL":"claude-fable-5"}},"agents":{"design":"opus-5","design-plan-impl":"opus-5","design-v3":"opus-5","design-v7":"opus-5","factoryworker":"sonnet-5","gherkin-breakdown":"opus-5","investigate":"opus-5","mergepatrol":"opus-5","minimalworker":"opus-5","rapid-implement":"sonnet-5","fable-implement":"sonnet-5","rapid-increment":"opus-5","fable-increment":"opus-5","rapid-soldesign-plan":"opus-5","rootcause-all":"opus-5","supervisor":"opus-5","ultra-review":"opus-5","fable-review":"opus-5","web-design":"opus-5"}}`,
	}

	for name, content := range starterConfigs {
		path := filepath.Join(configDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return fmt.Errorf("writing %s: %w", name, err)
			}
		}
	}

	// 4. Write/update AGENTS.md with available agents from agents.json.
	// Uses HTML comment markers for block-replace: existing content outside
	// the block is preserved; the block is regenerated on every init.
	if err := writeAgentsMd(cwd); err != nil {
		return fmt.Errorf("writing AGENTS.md: %w", err)
	}

	// 5. Initialize the issue store (mcpstore lazy-starts the Python MCP
	//    server under py/issuestore/; the first call opens/creates the SQLite
	//    database at <factoryRoot>/.agentfactory/store/issues.sqlite). This is the ONE consumer
	//    that uses mcpstore.New directly (not via the newIssueStore seam)
	//    because the install flow needs the user-visible side effect —
	//    database bootstrap — to print a confirmation banner. An empty actor
	//    is passed because install runs before any agent session and does not
	//    call List (actor scoping is a List-only concern). mcpstore.New is
	//    idempotent (the server performs CREATE TABLE IF NOT EXISTS), so no
	//    metadata.json stat-gate is needed.
	store, err := mcpstore.New(cwd, "")
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}
	_ = store
	fmt.Fprintln(cmd.OutOrStdout(), "Issue store initialized (SQLite + MCP server)")

	// 6. Create hooks/ directory and write quality gate files
	hooksDir := config.HooksDir(cwd)
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}

	// Write quality-gate.sh
	qgScript, err := hooksFS.ReadFile("install_hooks/quality-gate.sh")
	if err != nil {
		return fmt.Errorf("reading embedded quality-gate.sh: %w", err)
	}
	qgPath := filepath.Join(hooksDir, "quality-gate.sh")
	if err := os.WriteFile(qgPath, qgScript, 0755); err != nil {
		return fmt.Errorf("writing quality-gate.sh: %w", err)
	}

	// Write quality-gate-prompt.txt
	qgPrompt, err := hooksFS.ReadFile("install_hooks/quality-gate-prompt.txt")
	if err != nil {
		return fmt.Errorf("reading embedded quality-gate-prompt.txt: %w", err)
	}
	promptPath := filepath.Join(hooksDir, "quality-gate-prompt.txt")
	if err := os.WriteFile(promptPath, qgPrompt, 0644); err != nil {
		return fmt.Errorf("writing quality-gate-prompt.txt: %w", err)
	}

	// Write fidelity-gate.sh (mirrors the quality-gate.sh write block above;
	// the two hooks ship together so a fresh factory has both available).
	fgScript, err := hooksFS.ReadFile("install_hooks/fidelity-gate.sh")
	if err != nil {
		return fmt.Errorf("reading embedded fidelity-gate.sh: %w", err)
	}
	fgPath := filepath.Join(hooksDir, "fidelity-gate.sh")
	if err := os.WriteFile(fgPath, fgScript, 0755); err != nil {
		return fmt.Errorf("writing fidelity-gate.sh: %w", err)
	}

	// Write fidelity-gate-prompt.txt
	fgPrompt, err := hooksFS.ReadFile("install_hooks/fidelity-gate-prompt.txt")
	if err != nil {
		return fmt.Errorf("reading embedded fidelity-gate-prompt.txt: %w", err)
	}
	fgPromptPath := filepath.Join(hooksDir, "fidelity-gate-prompt.txt")
	if err := os.WriteFile(fgPromptPath, fgPrompt, 0644); err != nil {
		return fmt.Errorf("writing fidelity-gate-prompt.txt: %w", err)
	}

	// 6b. Render the af-managed git hooks (issue #371): the centralized
	// Co-authored-by trailer + a delegating pre-commit. They live in a dir
	// distinct from the Claude gate hooks and are activated per session via
	// core.hooksPath (so nothing is written to .git/). Rendered at install time
	// (not lazily) to avoid a first-commit race.
	if err := renderGitHooks(config.GitHooksDir(cwd)); err != nil {
		return err
	}

	// Enable fidelity gate by default for new factories
	if err := seedFidelityGate(cwd); err != nil {
		return err
	}

	// Seed the statusline gate on for new factories (issue #591). seed-if-absent — a
	// re-run --init must NOT clobber an operator's later `af statusline off`.
	statuslineGate := filepath.Join(configDir, ".statusline-gate")
	if _, err := os.Stat(statuslineGate); os.IsNotExist(err) {
		if err := os.WriteFile(statuslineGate, []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("writing .statusline-gate: %w", err)
		}
	}
	// Reflect the ACTUAL gate state, not an unconditional "on": an operator who ran
	// `af statusline off` before this re-init must not be told "on" (PR #595 T6/F3). The
	// seed above is seed-if-absent, so a fresh factory still reads "on" here.
	if statuslineFactoryEnabled(cwd) {
		fmt.Fprintln(cmd.OutOrStdout(), "Statusline: on (af statusline to configure)")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Statusline: off (af statusline on to enable)")
	}

	// 7b. Re-provision agent settings with current templates
	if err := reprovisionAgentSettings(cwd, cmd.OutOrStdout()); err != nil {
		return err
	}

	// 8. Write default formula files to store/formulas/ (skip if content matches)
	formulasDir := config.FormulasDir(cwd)
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		return fmt.Errorf("creating formulas directory: %w", err)
	}
	if err := writeFormulas(formulasDir); err != nil {
		return err
	}

	// 9. Write built-in skills to .claude/skills/ (recursive, skip-if-unchanged)
	skillsDir := filepath.Join(cwd, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		return fmt.Errorf("creating skills directory: %w", err)
	}
	if err := writeSkills(skillsDir); err != nil {
		return err
	}

	// 10. Ensure factory-managed paths are in .git/info/exclude
	if err := ensureGitExclude(cwd); err != nil {
		return fmt.Errorf("updating .git/info/exclude: %w", err)
	}

	// 11. Create .runtime/ directory (symlink target for worktrees)
	os.MkdirAll(filepath.Join(cwd, ".runtime"), 0755)

	// 12. macOS build-host auto-detection
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath("xcodebuild"); err == nil {
			bhPath := config.BuildHostConfigPath(cwd)
			if _, err := os.Stat(bhPath); os.IsNotExist(err) {
				cfg := &config.BuildHostConfig{Mode: "local"}
				if err := config.SaveBuildHostConfig(bhPath, cfg); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write build-host config: %v\n", err)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "Build host configured: local macOS (Xcode detected)")
				}
			}
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Hint: iOS builds available via 'af config build-host --mode ssh --host <mac-host> --user <user>'")
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Factory initialized successfully.")
	return nil
}

// seedFidelityGate enables the fidelity gate for a new factory and records the seed as one
// provenance line in .agentfactory/.fidelity-gate.log — it is the third toggle writer, and an
// operator asking "who turned it back on" must be able to see it alongside the other two.
//
// It is seed-if-absent: a re-run --init must NOT clobber an operator's later `af fidelity off`,
// and since it writes nothing in that case it logs nothing either.
//
// Extracted from runInstallInit so it can be unit-tested without the Python 3.12 / MCP server
// dependencies that runInstallInit requires (mirrors renderGitHooks and writeFormulas).
func seedFidelityGate(factoryRoot string) error {
	fidelityToggle := fidelityGateFile(factoryRoot)
	if _, err := os.Stat(fidelityToggle); !os.IsNotExist(err) {
		return nil
	}
	if err := os.WriteFile(fidelityToggle, []byte("on\n"), 0644); err != nil {
		return fmt.Errorf("writing .fidelity-gate: %w", err)
	}
	appendFidelityProvenance(factoryRoot, fidelitySourceInstall, "on")
	return nil
}

// renderGitHooks writes the af-managed git hooks (issue #371) — the centralized
// Co-authored-by trailer and the delegating pre-commit — from the embedded
// install_hooks/ copies into gitHooksDir at mode 0755. Extracted from
// runInstallInit so it can be unit-tested without the Python 3.12 / MCP server
// dependencies that runInstallInit requires (mirrors writeFormulas).
func renderGitHooks(gitHooksDir string) error {
	if err := os.MkdirAll(gitHooksDir, 0755); err != nil {
		return fmt.Errorf("creating git hooks directory: %w", err)
	}
	for _, name := range []string{"prepare-commit-msg", "pre-commit"} {
		data, err := hooksFS.ReadFile("install_hooks/" + name)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", name, err)
		}
		// 0755 is mandatory: a non-executable hook is silently skipped by git.
		if err := os.WriteFile(filepath.Join(gitHooksDir, name), data, 0755); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}
	return nil
}

// writeFormulas is extracted from runInstallInit so it can be unit-tested
// without the Python 3.12 / MCP server dependencies that runInstallInit requires.
func writeFormulas(formulasDir string) error {
	entries, err := formulasFS.ReadDir("install_formulas")
	if err != nil {
		return fmt.Errorf("reading embedded formulas: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := formulasFS.ReadFile(filepath.Join("install_formulas", entry.Name()))
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", entry.Name(), err)
		}
		dest := filepath.Join(formulasDir, entry.Name())
		existing, err := os.ReadFile(dest)
		if err == nil && bytes.Equal(existing, data) {
			continue
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return fmt.Errorf("writing %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func writeSkills(skillsDir string) error {
	return fs.WalkDir(skillsFS, "install_skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, "install_skills/")
		if rel == "install_skills" || rel == "" {
			return nil
		}
		dest := filepath.Join(skillsDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		data, err := skillsFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading embedded %s: %w", rel, err)
		}
		existing, err := os.ReadFile(dest)
		if err == nil && bytes.Equal(existing, data) {
			return nil
		}
		return os.WriteFile(dest, data, 0644)
	})
}

func migrateBeadsDir(root string) error {
	oldDir := filepath.Join(root, ".beads")
	newDir := config.StoreDir(root)
	sentinel := filepath.Join(newDir, ".migration-complete")
	ownedEntries := []string{"issues.sqlite", "formulas", ".gitignore"}

	if _, err := os.Stat(sentinel); err == nil {
		// Already migrated — clean up only our leftovers from .beads/
		for _, entry := range ownedEntries {
			os.RemoveAll(filepath.Join(oldDir, entry))
		}
		removeIfEmpty(oldDir)
		return nil
	}

	if _, err := os.Stat(oldDir); os.IsNotExist(err) {
		return nil
	}
	if _, err := os.Stat(newDir); err == nil {
		return nil
	}

	if err := os.MkdirAll(newDir, 0755); err != nil {
		return err
	}

	migrated := false
	for _, entry := range ownedEntries {
		src := filepath.Join(oldDir, entry)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		dst := filepath.Join(newDir, entry)
		if err := copyEntry(src, dst); err != nil {
			return err
		}
		if err := os.RemoveAll(src); err != nil {
			return err
		}
		migrated = true
	}

	if migrated {
		if err := os.WriteFile(sentinel, []byte("migrated\n"), 0644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Migrated agentfactory files from '.beads/' -> '.agentfactory/store/'\n")
	}

	removeIfEmpty(oldDir)
	return nil
}

func copyEntry(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(src, dst)
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

func removeIfEmpty(dir string) {
	// os.Remove fails on non-empty directories — safe by design
	os.Remove(dir)
}

func cleanLegacyGateLocks() error {
	patterns := []string{
		"/tmp/af-fidelity-gate-*.lock",
		"/tmp/af-quality-gate-*.lock",
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("globbing %s: %w", pattern, err)
		}
		for _, match := range matches {
			os.RemoveAll(match)
		}
	}
	return nil
}

func runInstallRole(cmd *cobra.Command, role string) error {
	cwd, err := getWd()
	if err != nil {
		return err
	}

	// 1. Find factory root
	factoryRoot, err := resolveInvokerRoot(cwd)
	if err != nil {
		return fmt.Errorf("not in a factory workspace: %w", err)
	}

	// 2. Load agents.json and validate role exists
	agentsPath := config.AgentsConfigPath(factoryRoot)
	agents, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}
	entry, ok := agents.Agents[role]
	if !ok {
		return fmt.Errorf("agent %q not found in agents.json", role)
	}

	// 3. Create agent workspace directory
	roleDir := config.AgentDir(factoryRoot, role)
	if err := os.MkdirAll(roleDir, 0755); err != nil {
		return fmt.Errorf("creating role directory: %w", err)
	}

	// 4. Render CLAUDE.md from template — try agent-specific template first, fall back to type default
	claudeMD, err := templates.RenderIdentity(templates.New(), role, entry, factoryRoot, roleDir)
	if err != nil {
		return fmt.Errorf("rendering CLAUDE.md: %w", err)
	}
	if err := templates.WriteIdentity(roleDir, claudeMD); err != nil {
		return err
	}

	// 5. Write settings.json based on role type
	roleType := claude.RoleTypeFor(role, agents)
	if err := claude.EnsureSettings(roleDir, roleType); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	// 6. Seed the agent's vault index — seed-if-absent. RebuildIndex is deliberately
	// last-writer-wins (store.go:423-426), so the os.Stat guard is the caller's job: an existing
	// index.md may already describe a populated vault and must never be rewritten from here.
	// Seeding through the core rather than writing the empty-state text by hand keeps that
	// sentence to the one copy the drift test pins. The vault hangs off factoryRoot, not roleDir,
	// so it survives every worktree teardown.
	indexPath := filepath.Join(config.AgentMemoryDir(factoryRoot, role), "index.md")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		if err := memory.RebuildIndex(factoryRoot, role); err != nil {
			return fmt.Errorf("seeding memory index: %w", err)
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Agent %q provisioned successfully.\n", role)
	return nil
}

// selfExecEnv guards the one-shot re-exec in relinkSelfForReinstall so the copy it
// launches does not re-enter the re-exec and loop.
const selfExecEnv = "AF_INSTALL_SELFEXEC"

// relinkSelfForReinstall makes `af install --agents` and `af plugin install` survive
// reinstalling the very `af` they run as. The operation IS a reinstall of af:
// agent-gen-all.sh's `make install` and quickstart.sh's install_af both `cp` a freshly built af over
// ~/.local/bin/af. Linux refuses to open a file for writing while its inode has an
// active text (exec) mapping — ETXTBSY, "Text file busy" — so a `cp` over the inode
// THIS process is executing fails. The kernel guards the inode's mapping, not the
// name, so we move this process's mapping off that inode: copy our own binary to a
// throwaway file and re-exec from it. ~/.local/bin/af keeps its name (the scripts'
// own `af down`/`af version` still resolve on PATH) but is no longer the busy inode,
// so every downstream `cp` over it succeeds — without editing either script.
//
// On the re-exec'd copy this unlinks the throwaway file (Linux keeps the inode, and
// thus this process, alive until exit) so nothing lingers on disk. Best-effort
// throughout: any failure falls through to the original flow, which is no worse than
// today. No-op under `go test` (would exec a copy of the test binary) and once the
// re-exec has already happened (env guard). A package-var seam (ADR-009) so unit tests
// can observe when each verb relinks relative to its first write.
var relinkSelfForReinstall = func(cmd *cobra.Command) {
	if os.Getenv(selfExecEnv) != "" {
		if exe, err := os.Executable(); err == nil {
			os.Remove(exe)
		}
		return
	}
	if isTestBinary() {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return
	}
	// Same directory as the binary we replace: guaranteed to be on an exec-capable
	// filesystem (af already runs from there) and on the same device for cleanup.
	copyPath := filepath.Join(filepath.Dir(self), fmt.Sprintf(".af-selfexec-%d", os.Getpid()))
	if err := os.WriteFile(copyPath, data, 0o755); err != nil {
		return
	}
	env := append(os.Environ(), selfExecEnv+"=1")
	if err := syscall.Exec(copyPath, os.Args, env); err != nil {
		os.Remove(copyPath)
		fmt.Fprintf(cmd.ErrOrStderr(), "note: could not re-exec from a self-copy (%v); the af binary may be busy during reinstall\n", err)
	}
}

// runInstallAgents implements `af install --agents`. It re-execs from a throwaway
// self-copy FIRST — ETXTBSY safety so the scripts run below can reinstall the very af
// we run as — then runs the shared refusals (preflightInstallAgents), this verb's
// flag-derived policy, the shared installAgentsPipeline body, the telemetry gate
// write, and finally the verb's post-bootstrap K15 report. The flag-derived policy
// and the gate write live here, not in the pipeline, because `af plugin install` runs
// the same pipeline and must not override the operator's telemetry or gateway choices.
// relinkSelfForReinstall MUST stay in this caller ahead of the pipeline: it is a no-op
// under `go test` but in production can syscall.Exec and replace the process, so it
// cannot move into the body.
// With --litellm, it also runs preflightGatewayPort and (for codex-subscription
// mode) preflightCodexSubscription — including the D24 pre-teardown device-auth
// login — before agent-gen-all.sh, so a codex-subscription redeploy never takes
// agents down only to fail on a preventable auth/port problem afterward.
func runInstallAgents(cmd *cobra.Command) error {
	// Re-exec from a throwaway self-copy BEFORE any work, so the scripts below can
	// reinstall the very af we run as (ETXTBSY otherwise — see relinkSelfForReinstall).
	relinkSelfForReinstall(cmd)

	cwd, factoryRoot, afSrc, err := preflightInstallAgents(cmd, "af install --agents")
	if err != nil {
		return err
	}
	// PR #688 Phase 3: --litellm-auth=<mode> is the upstream-auth selector, but it
	// REQUIRES --litellm (IMPLREADME_PHASE3.md, "Required change" bullet 1, stated
	// twice, verbatim, with no accompanying ambiguity marker) — passing it alone is
	// a usage error, not an implicit gateway request. See decisions.md D10 for why
	// an earlier iteration read this as "implies" and the correction.
	if installLitellmAuthFlag != "" && !installLitellmFlag {
		return fmt.Errorf("--litellm-auth requires --litellm")
	}
	// Mode resolution runs the same ladder as quickstart.sh: explicit flag >
	// AF_LITELLM_AUTH env (D5, fails loud on an invalid value rather than silently
	// falling through — the silent-fallback anti-pattern this formula exists to
	// prevent) > single existing auth handle > both-handles-without-a-flag
	// refusal (E9) > neither handle exists, default to api-key.
	litellmRequested := installLitellmFlag
	litellmMode := "api-key"
	litellmModeExplicit := false
	if litellmRequested {
		mode, explicit, err := resolveLitellmAuthMode(cmd, factoryRoot)
		if err != nil {
			return err
		}
		litellmMode = mode
		litellmModeExplicit = explicit
	}

	// --litellm key acquisition, UP FRONT. setup_litellm runs LAST in the
	// bootstrap and its stdin is /dev/null through af (ADR-014), so the script's
	// own TTY prompt is unreachable — without this block a cold factory would
	// burn the full non-transactional bootstrap (agents down) and fail only at
	// the very end. Same source precedence as setup_litellm (secret file > env >
	// prompt); a prompted key is exported so quickstart's env branch receives and
	// persists it exactly as a directly-typed one. Subscription mode never needs
	// an OpenAI key at all (frame-lift invariant: the two auth modes are disjoint
	// code paths) — this block is skipped entirely, never falls back to it.
	if litellmRequested && litellmMode == "api-key" && os.Getenv("OPENAI_API_KEY") == "" {
		keyFile := filepath.Join(config.ConfigDir(factoryRoot), "secrets", "openai.key")
		if fi, err := os.Stat(keyFile); err != nil || fi.Size() == 0 {
			key, err := promptOpenAIKey(cmd.ErrOrStderr(), keyFile)
			if err != nil {
				return fmt.Errorf("cannot run af install --agents --litellm without an OpenAI API key: export OPENAI_API_KEY or create %s (%v)", keyFile, err)
			}
			os.Setenv("OPENAI_API_KEY", key)
		}
	}

	// K3/C-15 (T16, decisions.md D12): the stale-quickstart refusals must run BEFORE the K6 preflight.
	// preflightCodexSubscription (below) runs the consent prompt and an up-to-15-minute device-auth
	// login, so a stale checkout whose quickstart.sh cannot parse the forwarded mode must refuse here —
	// before that cost is spent — not only when the quickstart args are assembled further down. The
	// --litellm-auth marker is gated exactly like that assembly, which forwards the flag only when the
	// mode is explicit or non-default. The reconcile marker is checked on every --litellm run, because
	// setup_litellm reconciles the gateway in every mode. The asserts read afSrc source text alone, so
	// none depends on any preflight state.
	if litellmRequested {
		if litellmModeExplicit || litellmMode != "api-key" {
			if err := assertQuickstartSupports(afSrc, "--litellm-auth", "--litellm-auth"); err != nil {
				return err
			}
		}
		if err := assertQuickstartSupports(afSrc, "_reconcile_gateway", "the gateway reconcile path"); err != nil {
			return err
		}
		if litellmMode == "codex-subscription" {
			if err := assertQuickstartSupports(afSrc, "_ensure_codex_cli", "the codex CLI install/consent flow"); err != nil {
				return err
			}
		}
	}

	// K6 preflight, UP FRONT — symmetric to the api-key key-acquisition block above.
	// agent-gen-all.sh is expensive and non-transactional (it takes agents down
	// before quickstart.sh brings the gateway back up), so anything that would make
	// the bootstrap fail must refuse here rather than burn that whole run and fail
	// only when quickstart.sh gets to it, last (design-doc.md:120, decisions.md D4).
	// preflightGatewayPort runs for either auth mode; preflightCodexSubscription is
	// specific to codex-subscription (CLI install/consent + D24 pre-teardown login).
	if litellmRequested {
		if err := preflightGatewayPort(factoryRoot); err != nil {
			return err
		}
	}
	if litellmRequested && litellmMode == "codex-subscription" {
		if err := preflightCodexSubscription(cmd); err != nil {
			return err
		}
	}

	var quickstartArgs []string
	if litellmRequested {
		quickstartArgs = append(quickstartArgs, "--litellm")
		// Forward the resolved mode explicitly only when it was actually
		// disambiguated (an explicit flag, an explicit AF_LITELLM_AUTH env value, or
		// a non-default inferred mode) — the plain default case (no flag, no env, no
		// handle) stays byte-identical to the pre-Phase-3 wire format so an
		// unmodified quickstart.sh keeps working. litellmModeExplicit (not just
		// "flag != default") matters here: quickstart.sh's own ladder has no env-var
		// tier, so an operator-set AF_LITELLM_AUTH that happens to resolve to the
		// api-key default must still be forwarded, or quickstart.sh would silently
		// re-derive a different mode from disk handles alone.
		// The stale-quickstart refusals for these same markers already ran UP FRONT, above the
		// K6 preflight (T16/decisions.md D12), so a stale checkout was rejected before any consent
		// or device-auth cost; here the resolved mode is simply forwarded.
		if litellmModeExplicit || litellmMode != "api-key" {
			quickstartArgs = append(quickstartArgs, "--litellm-auth="+litellmMode)
		}
	}
	if installNoTelemetryFlag {
		quickstartArgs = append(quickstartArgs, "--no-telemetry")
	}
	if err := installAgentsPipeline(cmd, cwd, afSrc, quickstartArgs); err != nil {
		return err
	}

	// The redeploy drives the telemetry gate: --no-telemetry means the operator
	// chose no measurement, its absence means they chose measurement (the backend
	// was just provisioned). The flag is the source of truth on EVERY run —
	// deliberately overwriting a manual `af telemetry on|off` from before — and
	// only after a successful bootstrap, from the operator-only context the
	// console can never reach (telemetry.go's on/off restriction); quickstart
	// itself never touches the gate by contract (setup_telemetry header).
	gate := "on"
	if installNoTelemetryFlag {
		gate = "off"
	}
	if err := os.WriteFile(telemetryGateFile(factoryRoot), []byte(gate+"\n"), 0644); err != nil {
		return fmt.Errorf("setting telemetry %s: %w", gate, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "telemetry: %s\n", gate)

	// K15 (issue #538) — report-only plugin re-verify against the FRESHLY rebuilt
	// binary. GATED on plugins.json presence so a zero-plugin factory is byte-identical
	// to before this phase (AC-6): the Phase-2 TestInstallAgents* gate stays green.
	// Living in this verb covers both the direct `af install --agents` re-run and the
	// web console's detached subprocess (job.go:72) with no change to job.go, while
	// `af plugin install` keeps its own K9 verify as the one verify it runs. It NEVER
	// changes the exit code.
	// Gate on the manifest at the resolved factoryRoot (NOT cwd — a subdir invocation
	// has cwd != factoryRoot, and plugins.json lives at the factory root).
	if _, err := os.Stat(config.PluginsConfigPath(factoryRoot)); err == nil {
		runPluginVerifyReport(cmd, factoryRoot)
	}
	return nil
}

// refuseWorktreeCwd is Guard 1: install surfaces write the factory's store and record, which a
// worktree only mirrors, so they run from the main checkout.
func refuseWorktreeCwd(cwd, surface string) error {
	if data, err := os.ReadFile(filepath.Join(config.ConfigDir(cwd), ".factory-root")); err == nil {
		return fmt.Errorf("cannot run %s inside a worktree (factory root: %s); run from the main project checkout, not a worktree", surface, strings.TrimSpace(string(data)))
	}
	return nil
}

// preflightInstallAgents runs every REFUSAL a verb that ends in installAgentsPipeline
// needs, before that verb makes its first write: worktree, factory root, AF source tree
// and script presence, and the operator gate. surface names the verb in each refusal
// ("af install --agents", "af plugin install"). Guard 3's source-repo warning also
// prints here, between the script check and the operator gate, so `af install --agents`
// keeps its original output order. It returns the operator's cwd, the
// resolved factory root and the AF source tree for the caller to hand to the body.
func preflightInstallAgents(cmd *cobra.Command, surface string) (cwd, factoryRoot, afSrc string, err error) {
	cwd, err = getWd() // helpers.go
	if err != nil {
		return "", "", "", err
	}

	// Guard 1 (R2/G4) — REFUSE inside a worktree, BEFORE FindFactoryRoot. Mirrors
	// runInstallInit's .factory-root idiom (install.go:93-95) but with an
	// operator-facing message that points to the main checkout. This is a clearer,
	// earlier error for the specific worktree case; the script's own CWD check
	// (agent-gen-all.sh:39-42) still streams for other CWD problems.
	if err := refuseWorktreeCwd(cwd, surface); err != nil {
		return "", "", "", err
	}

	factoryRoot, err = resolveInvokerRoot(cwd) // as runInstallRole
	if err != nil {
		return "", "", "", err
	}

	afSrc, fallback := resolveAFSource(factoryRoot) // formula.go

	// Guard 2 (R3/G6) — REFUSE when the AF source tree is unresolvable or
	// incomplete, BEFORE either seam. The fallback bool covers "nothing valid
	// resolved"; the two os.Stat checks additionally catch a stale-but-valid moved
	// checkout that still passes validateAFSource's go.mod substring test but no
	// longer has the scripts. Both scripts must be present before either seam runs.
	if fallback {
		return "", "", "", fmt.Errorf("cannot run %s: agentfactory source tree not found (resolved to %q); set AF_SOURCE_ROOT to your agentfactory checkout or run from a built install", surface, afSrc)
	}
	for _, script := range []string{"agent-gen-all.sh", "quickstart.sh"} {
		if _, err := os.Stat(filepath.Join(afSrc, script)); err != nil {
			return "", "", "", fmt.Errorf("cannot run %s: %s missing under source tree %q; set AF_SOURCE_ROOT to a complete agentfactory checkout or run from a built install", surface, script, afSrc)
		}
	}

	// Guard 3 (R8/G11) — WARN (do not block) when CWD is the AF source repo: the
	// agent-gen orphan-removal pass (agent-gen-all.sh:82-106) is destructive there.
	// sameDir returns a==b on stat error, so worst case is a missed warning, never
	// a wrong block — do NOT promote to a refusal.
	if sameDir(cwd, afSrc) {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: running from the agentfactory source repo — agent-gen-all.sh will remove local formulas/templates that have no source counterpart (destructive orphan removal)")
	}

	// K6 (#541) — REFUSE in agent context, replacing Guard 5's WARN. This fires
	// BEFORE the runAgentGenScript seam, whose agent-gen-all.sh runs `af down --all`
	// and would SIGKILL every agent including this one. Operator context returns nil
	// and falls through to the byte-for-byte regeneration (AC-7 regen safety).
	if err := requireOperatorTeardown(surface); err != nil {
		return "", "", "", err
	}
	return cwd, factoryRoot, afSrc, nil
}

// installAgentsPipeline redeploys ALL formula-derived agents by running the two
// repo-root scripts in order: agent-gen-all.sh FIRST and, only on success,
// quickstart.sh SECOND. Aborting before quickstart on a non-zero agent-gen exit
// avoids stacking a half-bootstrap on a failed regen (design G8: no cross-script
// rollback). Both scripts are invoked through ADR-009 package-var seams so unit tests
// can assert dispatch without executing the real scripts. This is the shared body
// only — it emits the non-blocking warnings and runs the scripts. Every verb caller
// must first relinkSelfForReinstall (it can syscall.Exec and replay the caller, so it
// cannot live here) and then preflightInstallAgents for the refusals, and it owns its
// own verb policy, including the quickstartArgs it passes here for quickstart.sh.
func installAgentsPipeline(cmd *cobra.Command, cwd, afSrc string, quickstartArgs []string) error {
	// Guard 6 (R9/H1) — WARN when a locally-edited shipped formula would be
	// clobbered by agent-gen-all.sh's unconditional, mtime-based -nt cp
	// (agent-gen-all.sh:72-81). Compares each project formula by CONTENT against
	// the ON-DISK source copy (never the embedded formulasFS, which can diverge in
	// a stale binary). A net-new customer formula (no source counterpart) is
	// preserved by the script and warns nothing.
	warnShippedFormulaClobber(cmd, cwd, afSrc)

	// Guard 4 (D6, REFRAMED) — informational note only; NO staleness warning.
	// quickstart.sh runs second and always rebuilds/reinstalls the binary, so
	// af prime's embedded template is always fresh after a successful run.
	// --no-build skips only agent-gen-all.sh's duplicate build.
	if installNoBuildFlag {
		fmt.Fprintln(cmd.OutOrStdout(), "note: --no-build skips only agent-gen-all.sh's duplicate build; quickstart.sh always rebuilds the binary")
	}

	// Author PR #417: run BOTH scripts in order — agent-gen FIRST, then quickstart.
	if err := runAgentGenScript(cmd, afSrc, cwd, installNoBuildFlag); err != nil {
		return err // abort before quickstart on non-zero (no half-bootstrap)
	}
	if err := runQuickstartScript(cmd, afSrc, cwd, quickstartArgs); err != nil { // stdin←/dev/null (exec-bash mitigation)
		return err
	}
	return nil
}

// resolveLitellmAuthMode implements the --litellm-auth mode-resolution ladder
// (PR #688 Phase 3 / issue #693 Phase 2a K5, decisions D1/D5/D6/D7/D9): explicit
// --litellm-auth flag (via the installLitellmAuthFlag != "" sentinel check, NOT
// cmd.Flags().Changed — D9 found Changed()'s underlying pflag bit is never reset
// across repeated Execute() calls on the same long-lived *cobra.Command, so a ""
// default is used instead to distinguish "unset" from an explicit "api-key" —
// D1) wins; else AF_LITELLM_AUTH, validated and failed loud on an invalid value
// rather than silently ignored (D5); else K1's gatewayAuthMode(factoryRoot)
// record+migration ladder (decisions.md D1 — delegate, never reimplement; D2 —
// this function never writes the record; D3 — its both-handles-no-record
// refusal is propagated verbatim).
//
// The second return value reports whether the mode came from an explicit
// source (flag, env, or a present record) rather than migration inference.
// quickstart.sh's own ladder has no env-var tier, so a caller must forward the
// mode whenever it was explicit — even when it happens to equal the "api-key"
// default — or an AF_LITELLM_AUTH override (or a recorded mode) can silently
// diverge from what quickstart.sh independently re-derives from disk handles
// alone. A migration-inferred non-default mode is still forwarded by the
// caller's own separate "mode != api-key" check (decisions.md D7 case (e)), so
// this function does not need to special-case it here.
func resolveLitellmAuthMode(cmd *cobra.Command, factoryRoot string) (string, bool, error) {
	validate := func(mode, source string) (string, bool, error) {
		if mode != "api-key" && mode != "codex-subscription" {
			return "", false, fmt.Errorf("invalid %s value %q: must be api-key or codex-subscription", source, mode)
		}
		return mode, true, nil
	}
	if installLitellmAuthFlag != "" {
		return validate(installLitellmAuthFlag, "--litellm-auth")
	}
	if env := os.Getenv("AF_LITELLM_AUTH"); env != "" {
		return validate(env, "AF_LITELLM_AUTH")
	}

	mode, migrated, err := gatewayAuthMode(factoryRoot)
	if err != nil {
		return "", false, err
	}
	return mode, !migrated, nil
}

// assertQuickstartSupports guards against forwarding a feature to a
// version-skewed quickstart.sh: its unknown-flag arm only warns-and-ignores
// (quickstart.sh's `*)` case), so a stale script would silently fall through to
// api-key-mode behavior — or, for the function markers below, silently run an
// old code path lacking the guarded behavior entirely — instead of failing
// loud. Phase 1 of issue #693 renamed this from
// assertQuickstartSupportsLitellmAuth (single-argument, single-marker); Phase 2a
// generalizes it to this two-argument marker+feature form (design-doc.md K3).
//
// marker is either a literal flag ("--litellm-auth"), matched as a substring
// (a mention in a comment satisfies it, matching pre-Phase-2a behavior and the
// existing fixtures), or a bash function name ("_ensure_codex_cli",
// "_reconcile_gateway"), matched only as an actual function definition line —
// a comment mentioning the name does not satisfy it (cross-review HIGH-2).
// feature names the capability in the returned error.
func assertQuickstartSupports(afSrc, marker, feature string) error {
	scriptPath := filepath.Join(afSrc, "quickstart.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("cannot verify %s support: reading %s: %w", feature, scriptPath, err)
	}
	var found bool
	if strings.HasPrefix(marker, "-") {
		found = bytes.Contains(data, []byte(marker))
	} else {
		found = regexp.MustCompile(`(?m)^[ \t]*` + regexp.QuoteMeta(marker) + `\(\)[ \t]*\{`).Match(data)
	}
	if !found {
		return fmt.Errorf("cannot forward %s: %s does not recognize it (stale agentfactory source tree?); update the checkout at %s and retry", feature, scriptPath, afSrc)
	}
	return nil
}

// runPluginVerifyReport execs the freshly rebuilt af to re-verify every recorded
// plugin (`af plugin verify --all`). It is REPORT-ONLY (K15): a missing af on PATH or
// a failing verify is surfaced to the operator but never propagated as a pipeline
// error. A package-var seam so unit tests neither exec a binary nor hit noexec /tmp.
var runPluginVerifyReport = func(cmd *cobra.Command, projectDir string) {
	afPath, err := exec.LookPath("af")
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "plugin verify (report-only): af not found on PATH: %v\n", err)
		return
	}
	c := exec.Command(afPath, "plugin", "verify", "--all")
	c.Dir = projectDir
	out, _ := c.CombinedOutput()
	fmt.Fprintf(cmd.OutOrStdout(), "plugin verify (report-only, %s):\n%s", afPath, out)
}

// warnShippedFormulaClobber prints Guard 6's warning for each project formula
// under config.FormulasDir(cwd) that also exists under the ON-DISK
// $afSrc/internal/cmd/install_formulas/ AND differs by content — exactly the set
// agent-gen-all.sh:72-81 will silently overwrite via its mtime-based -nt cp. The
// comparison is on-disk-source vs on-disk-project (the formula_drift_test.go
// idiom), never the embedded formulasFS (which can diverge from what the script
// copies in a stale-binary case). Net-new customer formulas (no source
// counterpart) and any unreadable dir/file are skipped silently — a read error is
// not a clobber. Guard 2's os.Stat already proved afSrc is a real source tree.
func warnShippedFormulaClobber(cmd *cobra.Command, cwd, afSrc string) {
	formulasDir := config.FormulasDir(cwd)
	entries, err := os.ReadDir(formulasDir)
	if err != nil {
		return // no project formulas dir (or unreadable) → nothing to clobber
	}
	srcDir := filepath.Join(afSrc, "internal", "cmd", "install_formulas")
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".formula.toml") {
			continue
		}
		name := entry.Name()
		srcBytes, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			continue // net-new customer formula (no shipped counterpart) — not a clobber
		}
		projBytes, err := os.ReadFile(filepath.Join(formulasDir, name))
		if err != nil {
			continue // unreadable project file — not a clobber
		}
		if !bytes.Equal(srcBytes, projBytes) {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: shipped formula %q has local edits that agent-gen-all.sh will overwrite; make durable edits in internal/cmd/install_formulas/ and re-sync (ADR-015)\n", name)
		}
	}
}

// runAgentGenScript is the ADR-009 seam tests override to avoid executing the
// real agent-gen-all.sh (which needs af on PATH, runs af down --all, rebuilds).
var runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error {
	scriptPath := filepath.Join(afSrc, "agent-gen-all.sh")
	args := []string{}
	if noBuild {
		args = append(args, "--no-build")
	}
	c := exec.Command(scriptPath, args...) // argv form, never a shell invocation (security.md SEC-1)
	c.Dir = projectDir
	c.Env = append(os.Environ(), "AF_SRC="+afSrc)
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	return c.Run() // propagate exit code as error
}

// promptOpenAIKey is the ADR-009 seam for the --litellm key prompt. af prompts
// here, before either bootstrap script runs, precisely because ADR-014 makes
// quickstart's own prompt unreachable through af. Echo is disabled via stty — a
// system binary through os/exec, exempt from ADR-013's go.mod freeze (x/term
// would be a new direct require). If stty fails the key echoes on the
// operator's own terminal; proceeding beats refusing there, since the
// documented alternative (export OPENAI_API_KEY) exposes strictly more.
var promptOpenAIKey = func(errW io.Writer, keyFile string) (string, error) {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return "", fmt.Errorf("stdin is not a terminal, cannot prompt")
	}
	fmt.Fprintf(errW, "OpenAI API key (stored at %s): ", keyFile)
	echoOff := exec.Command("stty", "-echo")
	echoOff.Stdin = os.Stdin
	sttyWorked := echoOff.Run() == nil
	line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
	if sttyWorked {
		echoOn := exec.Command("stty", "echo")
		echoOn.Stdin = os.Stdin
		echoOn.Run()
	}
	fmt.Fprintln(errW)
	key := strings.TrimSpace(line)
	if key == "" {
		if readErr != nil {
			return "", fmt.Errorf("could not read key: %v", readErr)
		}
		return "", fmt.Errorf("no key provided")
	}
	return key, nil
}

// promptCodexInstallConsent is the K4 (issue #693 Phase 1) ADR-009 seam gating a privileged Codex
// CLI install: same "stdin is not a terminal" refusal shape as promptOpenAIKey above. It is
// unwired in Phase 1 (Phase 2a/K6 calls it before the install).
var promptCodexInstallConsent = func(errW io.Writer) (bool, error) {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false, fmt.Errorf("stdin is not a terminal, cannot prompt")
	}
	fmt.Fprint(errW, "This will INSTALL the codex cli, are you sure? y/N ")
	line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
	if readErr != nil && line == "" {
		return false, fmt.Errorf("could not read consent: %v", readErr)
	}
	ans := strings.TrimSpace(line)
	return ans == "y" || ans == "Y", nil
}

// lookPathCodex is the K4 seam wrapping exec.LookPath("codex").
var lookPathCodex = func() (string, error) {
	return exec.LookPath("codex")
}

// sudoNonInteractiveOK is the K4 seam probing `sudo -n true`.
var sudoNonInteractiveOK = func() bool {
	return exec.Command("sudo", "-n", "true").Run() == nil
}

// npmGlobalRootWritable is the K4 seam: `npm root -g`, then an O_CREATE|O_EXCL probe file removed
// immediately after.
var npmGlobalRootWritable = func() bool {
	out, err := exec.Command("npm", "root", "-g").Output()
	if err != nil {
		return false
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return false
	}
	probe := filepath.Join(root, ".af-npm-writable-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

// codexHomeWritable is the K4 seam: $CODEX_HOME (or ~/.codex) is created if absent, then checked
// for write access.
var codexHomeWritable = func() bool {
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".codex")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".af-writable-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

// codexSessionValid is the K4 Go twin of K8's bash predicate: it reads $CODEX_HOME/auth.json (or
// ~/.codex/auth.json) directly — never shelling out to `codex login status`, so an upstream wording
// change to the CLI's human status text can never invalidate a genuine session (D7/T14) — and keys
// on the two facts import itself keys on: a ChatGPT auth_mode (a NON-empty api-key auth_mode reads
// invalid, matching the bash side) plus a present refresh token. An absent auth_mode with tokens is
// treated as ChatGPT, mirroring import (gateway_auth.go:482-487) and Codex's own Option<AuthMode>.
var codexSessionValid = func() bool {
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".codex")
	}
	data, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err != nil {
		return false
	}
	var auth codexAuthDotJSON
	if err := json.Unmarshal(data, &auth); err != nil {
		return false
	}
	if auth.AuthMode != "" && !isChatGPTAuthMode(auth.AuthMode) {
		return false
	}
	return auth.Tokens != nil && auth.Tokens.RefreshToken != ""
}

// runCodexDeviceAuth is the K4 seam execing `codex login --device-auth` with stdout/stderr
// forwarded and stdin nil (the login needs no stdin), under a 15-minute deadline, then polling
// codexSessionValid every 2s until valid or the deadline (design-doc.md K4 row). Unwired in
// Phase 1; Phase 2a/6 call it.
var runCodexDeviceAuth = func(ctx context.Context, out, errW io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "login", "--device-auth")
	cmd.Stdin = nil
	cmd.Stdout = out
	cmd.Stderr = errW
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("codex login --device-auth: %w", err)
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if codexSessionValid() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("codex device-auth login did not complete within the deadline")
		case <-ticker.C:
		}
	}
}

// preflightCodexSubscription is K6 (issue #693 Phase 2a, design-doc.md:120,
// decisions.md D4/D5/D6/D9): it replaces the deleted E2 block in the same slot,
// before runAgentGenScript. Two disjoint branches on lookPathCodex:
//
//   - CLI absent: refuse before even offering to install unless the container
//     can actually install it (passwordless sudo or a writable npm global root)
//     and $CODEX_HOME is writable; otherwise gate on operator consent
//     (AF_CODEX_INSTALL_CONSENT=yes, or an interactive prompt when unset) before
//     forwarding consent to the quickstart.sh child process env (D6) — the
//     actual `npm install -g` + login happen in quickstart.sh's K7/K8 (Phase
//     2b), never here.
//   - CLI present: D24's pre-teardown login — if the session is not valid, log
//     in now (runCodexDeviceAuth), before runAgentGenScript takes agents down,
//     so the common redeploy case (installed, session expired) completes login
//     with agents still up.
func preflightCodexSubscription(cmd *cobra.Command) error {
	if _, err := lookPathCodex(); err != nil {
		if !sudoNonInteractiveOK() && !npmGlobalRootWritable() {
			return fmt.Errorf("cannot install the Codex CLI: the npm global prefix is root-owned and passwordless sudo is unavailable in this container")
		}
		if !codexHomeWritable() {
			dir := os.Getenv("CODEX_HOME")
			if dir == "" {
				if home, herr := os.UserHomeDir(); herr == nil {
					dir = filepath.Join(home, ".codex")
				}
			}
			return fmt.Errorf("cannot install the Codex CLI: %s is not writable", dir)
		}
		switch consent := os.Getenv("AF_CODEX_INSTALL_CONSENT"); consent {
		case "yes":
			// Pre-granted non-interactively; nothing to prompt.
		case "":
			ok, perr := promptCodexInstallConsent(cmd.ErrOrStderr())
			if perr != nil {
				return fmt.Errorf("cannot prompt for codex CLI install consent (%v): set AF_CODEX_INSTALL_CONSENT=yes to consent non-interactively", perr)
			}
			if !ok {
				return fmt.Errorf("codex CLI install declined — subscription mode cannot run without the Codex CLI; nothing was installed and no agents were touched")
			}
		default:
			return fmt.Errorf("invalid AF_CODEX_INSTALL_CONSENT value %q: must be unset or exactly %q", consent, "yes")
		}
		os.Setenv("AF_CODEX_INSTALL_CONSENT", "yes")
		return nil
	}
	if !codexSessionValid() {
		if err := runCodexDeviceAuth(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			return err
		}
	}
	return nil
}

// preflightGatewayPort is K6's mode-independent half (design-doc.md:120): the
// gateway profile's ANTHROPIC_BASE_URL (gatewayAuthProfileName in models.json),
// when loopback, must not already be served by a process outside the "litellm"
// tmux session — af never stops a process it did not start. No profile, no
// base URL, a non-loopback endpoint, or a free port are all no-ops: there is
// nothing to conflict with yet (first bootstrap, or the port genuinely idle).
func preflightGatewayPort(factoryRoot string) error {
	cfg, err := config.LoadModelsConfig(factoryRoot)
	if err != nil || cfg == nil {
		return nil // malformed/missing models.json is not this preflight's concern
	}
	// T4/K6: dial every gateway-endpoint profile (a file: ANTHROPIC_AUTH_TOKEN), not only the seeded
	// codex-subscription name — an api-key factory names its gateway profile "codex", and its foreign
	// listener must be refused too. Mirrors removeStaleGatewayCoverageRecord's enumeration
	// (gateway_auth.go:265-272): the seed name is always checked, plus every file:-token profile.
	for name, profile := range cfg.Models {
		if name != gatewayAuthProfileName && !strings.HasPrefix(profile[authTokenKey], secretPrefix) {
			continue
		}
		base := profile["ANTHROPIC_BASE_URL"]
		if base == "" || !config.IsLoopbackEndpoint(base) {
			continue
		}
		u, err := url.Parse(base)
		if err != nil || u.Port() == "" {
			continue
		}
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", u.Port()), 500*time.Millisecond)
		if dialErr != nil {
			continue // port free
		}
		conn.Close()
		if running, _ := newCmdTmux().HasSession("litellm"); running {
			continue
		}
		return fmt.Errorf("port %s is served by a process outside the 'litellm' tmux session; af does not stop processes it did not start", u.Port())
	}
	return nil
}

// runQuickstartScript is the ADR-009 seam for quickstart.sh. Stdin is /dev/null
// so the bootstrap can never block on interactive input (ADR-014; originally the
// mitigation for a terminal `exec bash` the script no longer has).
var runQuickstartScript = func(cmd *cobra.Command, afSrc, projectDir string, extraArgs []string) error {
	// extraArgs carries only af-defined mirror flags (--litellm/--no-telemetry),
	// never operator-typed passthrough — quickstart's unknown-option arm only
	// warns, so an unvetted arg would be silently ignored mid-bootstrap.
	c := exec.Command(filepath.Join(afSrc, "quickstart.sh"), extraArgs...) // argv form, never a shell invocation (security.md SEC-1)
	c.Dir = projectDir
	c.Env = append(os.Environ(), "AF_SRC="+afSrc)
	c.Stdin = nil // nil ⇒ /dev/null in os/exec → any interactive read gets EOF
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	return c.Run()
}

const (
	agentsMdBegin = "## BEGIN AgentFactory Agents"
	agentsMdEnd   = "## END AgentFactory Agents"
)

func writeAgentsMd(root string) error {
	agentsPath := config.AgentsConfigPath(root)
	agents, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: agent roster not written: could not load %s: %v\n", agentsPath, err)
		return nil
	}

	names := make([]string, 0, len(agents.Agents))
	for name := range agents.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf strings.Builder
	buf.WriteString(agentsMdBegin + "\n\n")
	buf.WriteString("Dispatch work to a specialist agent:\n")
	buf.WriteString("```\naf sling --agent <name> \"task description\"\n```\n\n")
	buf.WriteString("| Agent | Type | Description |\n")
	buf.WriteString("|-------|------|-------------|\n")
	for _, name := range names {
		entry := agents.Agents[name]
		desc := agentDescriptionLine(entry.Description)
		buf.WriteString(fmt.Sprintf("| `%s` | %s | %s |\n", name, entry.Type, desc))
	}
	buf.WriteString(agentsMdEnd + "\n")

	block := buf.String()

	agentsMdPath := config.AgentsMdPath(root)
	existing, err := os.ReadFile(agentsMdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fsutil.WriteFileAtomic(agentsMdPath, []byte(block), 0644)
		}
		return fmt.Errorf("reading AGENTS.md: %w", err)
	}

	content := string(existing)
	beginIdx := strings.Index(content, agentsMdBegin)
	endIdx := strings.Index(content, agentsMdEnd)

	if beginIdx >= 0 && endIdx >= 0 {
		after := endIdx + len(agentsMdEnd)
		if after < len(content) && content[after] == '\n' {
			after++
		}
		newContent := content[:beginIdx] + block + content[after:]
		return fsutil.WriteFileAtomic(agentsMdPath, []byte(newContent), 0644)
	}

	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + block
	return fsutil.WriteFileAtomic(agentsMdPath, []byte(content), 0644)
}

// regenRoster rewrites .agentfactory/AGENTS.md from the authoritative agents.json.
// Surface-but-don't-fail: a roster-write error must not fail the agent-gen op.
func regenRoster(root string) {
	if err := writeAgentsMd(root); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not regenerate agent roster: %v\n", err)
	}
}

const gitExcludeSentinel = "# agentfactory managed paths"

func ensureGitExclude(root string) error {
	gitDir := filepath.Join(root, ".git")
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return nil
	}

	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		return fmt.Errorf("creating .git/info/: %w", err)
	}

	excludePath := filepath.Join(infoDir, "exclude")

	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading .git/info/exclude: %w", err)
	}

	content := string(existing)

	if strings.Contains(content, gitExcludeSentinel) {
		return nil
	}

	var buf strings.Builder
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		buf.WriteString("\n")
	}
	buf.WriteString(gitExcludeSentinel + "\n")
	buf.WriteString(".agentfactory/*\n")
	buf.WriteString(".runtime/\n")
	buf.WriteString("AGENTS.md\n")
	buf.WriteString(".claude/\n")

	return os.WriteFile(excludePath, []byte(content+buf.String()), 0644)
}

func agentDescriptionLine(desc string) string {
	var parts []string
	for _, line := range strings.Split(desc, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts = append(parts, trimmed)
	}
	result := strings.Join(parts, " ")
	if len(result) > 128 {
		return result[:125] + "..."
	}
	return result
}

func checkPythonMCPDeps(factoryRoot string, out io.Writer) error {
	pyRoot, err := mcpstore.ResolvePyPath(factoryRoot)
	if err != nil {
		return fmt.Errorf("py/ package not found: %w. Set AF_SOURCE_ROOT to the agentfactory source directory, or run from the agentfactory source tree.", err)
	}

	importCmd := exec.Command("python3", "-c", "import py.issuestore.server")
	importCmd.Env = append(os.Environ(), "PYTHONPATH="+pyRoot)
	if importOut, err := importCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("py.issuestore.server is not importable: %s. Ensure the agentfactory py/ package is intact.", strings.TrimSpace(string(importOut)))
	}

	if depsOut, err := exec.Command("python3", "-c", "import aiohttp, sqlalchemy").CombinedOutput(); err != nil {
		return fmt.Errorf("Missing Python dependencies: %s. Run: pip install -r py/requirements.txt", strings.TrimSpace(string(depsOut)))
	}

	fmt.Fprintln(out, "Python MCP dependencies verified")
	return nil
}

// checkPython312 verifies that python3 is available on PATH and reports
// version 3.12.x. af install --init requires Python 3.12 because the
// mcpstore adapter lazy-spawns `python3 -m py.issuestore.server`, which
// uses 3.12-only syntax. Returns a typed error with remediation guidance
// when missing or mismatched; callers must abort installation before any
// filesystem mutation.
func checkPython312() error {
	out, err := exec.Command("python3", "--version").Output()
	if err != nil {
		return fmt.Errorf("python3 not found on PATH: %w (install Python 3.12 via `uv python install 3.12` or your system package manager)", err)
	}
	ver := strings.TrimSpace(string(out))
	if !strings.Contains(ver, "Python 3.12") {
		return fmt.Errorf("python3 is %q, need Python 3.12.x (install via `uv python install 3.12` or your system package manager)", ver)
	}
	return nil
}

func reprovisionAgentSettings(cwd string, out io.Writer) error {
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(cwd))
	if err != nil {
		return nil
	}

	entries, err := os.ReadDir(config.AgentsDir(cwd))
	if err != nil {
		return nil
	}

	tmpl := templates.New()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		agentDir := config.AgentDir(cwd, name)
		roleType := claude.RoleTypeFor(name, agents)
		if err := claude.EnsureSettings(agentDir, roleType); err != nil {
			fmt.Fprintf(out, "warning: could not re-provision settings for agent %s: %v\n", name, err)
		}
		// The identity file is re-provisioned alongside settings.json: it is the carrier the model
		// actually reads at session start, and af install --init was the one funnel that refreshed
		// the settings half while leaving a stale CLAUDE.md in place.
		agentEntry, ok := agents.Agents[name]
		if !ok {
			continue
		}
		identity, err := templates.RenderIdentity(tmpl, name, agentEntry, cwd, agentDir)
		if err != nil {
			fmt.Fprintf(out, "warning: could not re-provision identity for agent %s: %v\n", name, err)
			continue
		}
		if err := templates.WriteIdentity(agentDir, identity); err != nil {
			fmt.Fprintf(out, "warning: could not re-provision identity for agent %s: %v\n", name, err)
		}
	}

	return nil
}
