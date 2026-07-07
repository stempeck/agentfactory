package cmd

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
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
templates + rebuild) first, then quickstart.sh (full bootstrap), the latter
non-interactively so its terminal 'exec bash' exits on its own. Add --no-build
to skip agent-gen-all.sh's duplicate rebuild (quickstart.sh always rebuilds the
binary). It stops all agents (the wrapped 'af down --all' never restarts them),
so run 'af up' afterward. It requires an already-initialized factory:
agent-gen-all.sh runs first and aborts if .agentfactory/store/formulas/ is
absent, before quickstart.sh could bootstrap a cold factory; for a first-time or
cold-start setup run quickdocker.sh/quickstart.sh first. Residual risk: the
command is not transactional, so a mid-run failure can leave agents down and the
factory half-regenerated -- check the streamed exit code and end-state. A green
unit test confirms dispatch, not factory health; behavioral success requires the
e2e cold-start, 'af up', 'af sling', PR check.`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().BoolVar(&installInitFlag, "init", false, "Initialize a new factory workspace")
	installCmd.Flags().BoolVar(&installAgentsFlag, "agents", false,
		"Regenerate and reinstall all formula-derived agents (runs agent-gen-all.sh then quickstart.sh)")
	installCmd.Flags().BoolVar(&installNoBuildFlag, "no-build", false,
		"With --agents: skip ONLY agent-gen-all.sh's duplicate rebuild — quickstart.sh always rebuilds the binary, so the embedded identity is never left stale")
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
	if installInitFlag {
		return runInstallInit(cmd)
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: af install <role> or af install --init")
	}
	return runInstallRole(cmd, args[0])
}

func runInstallInit(cmd *cobra.Command) error {
	cwd, err := getWd()
	if err != nil {
		return err
	}

	if data, err := os.ReadFile(filepath.Join(config.ConfigDir(cwd), ".factory-root")); err == nil {
		return fmt.Errorf("cannot run af install --init inside a worktree (factory root: %s)", strings.TrimSpace(string(data)))
	}

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

	// 3. Write starter configs (only if they don't exist — idempotent)
	starterConfigs := map[string]string{
		// Built from the in-code defaults (incl. the C-3 git_identity) so the on-disk
		// literal cannot drift from internal/config's constants (issue #371 Gap-6).
		"factory.json":   config.DefaultFactoryConfigJSON(),
		"agents.json":    `{"agents":{"manager":{"type":"interactive","description":"Interactive agent for human-supervised work","directive":"Read your memory and docs, and prove it."},"supervisor":{"type":"autonomous","description":"Autonomous agent for independent task execution","directive":"Read your memory and docs, and prove it."}}}`,
		"messaging.json": `{"groups":{"all":["manager","supervisor"]}}`,
		"dispatch.json":  `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`,
		// Opinionated defaults for fresh installs (see TestLoadStartupConfig_ScaffoldLoads).
		"startup.json": `{"agents":["manager"],"quality":"default","fidelity":"default","start_dispatch":true,"watchdog_agents":["manager","supervisor"]}`,
		// Per-agent model registry (issue #480); the default model tracks quickstart.sh
		// (see TestInstallScaffold_DefaultModel_MatchesQuickstart).
		"models.json": `{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-fable-5","ANTHROPIC_DEFAULT_OPUS_MODEL":"claude-opus-4-8","ANTHROPIC_DEFAULT_SONNET_MODEL":"claude-sonnet-5"},"lmstudio":{"ANTHROPIC_BASE_URL":"http://localhost:1234","ANTHROPIC_AUTH_TOKEN":"lm-studio","ANTHROPIC_MODEL":"qwen2.5-coder-32b","ANTHROPIC_API_KEY":""},"sonnet-5":{"ANTHROPIC_MODEL":"claude-sonnet-5"}},"agents":{"factoryworker":"sonnet-5"}}`,
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
	fidelityToggle := filepath.Join(configDir, ".fidelity-gate")
	if _, err := os.Stat(fidelityToggle); os.IsNotExist(err) {
		if err := os.WriteFile(fidelityToggle, []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("writing .fidelity-gate: %w", err)
		}
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
	factoryRoot, err := config.FindFactoryRoot(cwd)
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
	tmpl := templates.New()
	templateRole := role
	if !tmpl.HasRole(templateRole) {
		if entry.Formula != "" {
			fmt.Fprintf(os.Stderr, "WARNING: agent %q is formula-generated but its template is not embedded in the binary. Agent will function via workspace CLAUDE.md but af prime will inject a generic template.\n", role)
		}
		templateRole = entry.Type
		if templateRole == "interactive" {
			templateRole = "manager"
		} else if templateRole == "autonomous" {
			templateRole = "supervisor"
		}
	}

	data := templates.RoleData{
		Role:        role,
		Description: entry.Description,
		RootDir:     factoryRoot,
		WorkDir:     roleDir,
	}
	claudeMD, err := tmpl.RenderRole(templateRole, data)
	if err != nil {
		return fmt.Errorf("rendering CLAUDE.md: %w", err)
	}
	if err := os.WriteFile(filepath.Join(roleDir, "CLAUDE.md"), []byte(claudeMD), 0644); err != nil {
		return fmt.Errorf("writing CLAUDE.md: %w", err)
	}

	// 5. Write settings.json based on role type
	roleType := claude.RoleTypeFor(role, agents)
	if err := claude.EnsureSettings(roleDir, roleType); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Agent %q provisioned successfully.\n", role)
	return nil
}

// selfExecEnv guards the one-shot re-exec in relinkSelfForReinstall so the copy it
// launches does not re-enter the re-exec and loop.
const selfExecEnv = "AF_INSTALL_SELFEXEC"

// relinkSelfForReinstall makes `af install --agents` survive reinstalling the very
// `af` it runs as. The operation IS a reinstall of af: agent-gen-all.sh's `make
// install` and quickstart.sh's install_af both `cp` a freshly built af over
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
// re-exec has already happened (env guard).
func relinkSelfForReinstall(cmd *cobra.Command) {
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

// runInstallAgents implements `af install --agents`: redeploy ALL formula-derived
// agents by running the two repo-root scripts in order. It resolves the operator's
// CWD (project dir) and the AF source tree, then runs agent-gen-all.sh FIRST and,
// only on success, quickstart.sh SECOND. Aborting before quickstart on a non-zero
// agent-gen exit avoids stacking a half-bootstrap on a failed regen (design G8:
// no cross-script rollback). Both scripts are invoked through ADR-009 package-var
// seams so unit tests can assert dispatch without executing the real scripts.
func runInstallAgents(cmd *cobra.Command) error {
	// Re-exec from a throwaway self-copy BEFORE any work, so the scripts below can
	// reinstall the very af we run as (ETXTBSY otherwise — see relinkSelfForReinstall).
	relinkSelfForReinstall(cmd)

	cwd, err := getWd() // helpers.go
	if err != nil {
		return err
	}

	// Guard 1 (R2/G4) — REFUSE inside a worktree, BEFORE FindFactoryRoot. Mirrors
	// runInstallInit's .factory-root idiom (install.go:93-95) but with an
	// operator-facing message that points to the main checkout. This is a clearer,
	// earlier error for the specific worktree case; the script's own CWD check
	// (agent-gen-all.sh:39-42) still streams for other CWD problems.
	if data, err := os.ReadFile(filepath.Join(config.ConfigDir(cwd), ".factory-root")); err == nil {
		return fmt.Errorf("cannot run af install --agents inside a worktree (factory root: %s); run from the main project checkout, not a worktree", strings.TrimSpace(string(data)))
	}

	factoryRoot, err := config.FindFactoryRoot(cwd) // as runInstallRole
	if err != nil {
		return err
	}

	afSrc, fallback := resolveAFSource(factoryRoot) // formula.go

	// Guard 2 (R3/G6) — REFUSE when the AF source tree is unresolvable or
	// incomplete, BEFORE either seam. The fallback bool covers "nothing valid
	// resolved"; the two os.Stat checks additionally catch a stale-but-valid moved
	// checkout that still passes validateAFSource's go.mod substring test but no
	// longer has the scripts. Both scripts must be present before either seam runs.
	if fallback {
		return fmt.Errorf("cannot run af install --agents: agentfactory source tree not found (resolved to %q); set AF_SOURCE_ROOT to your agentfactory checkout or run from a built install", afSrc)
	}
	for _, script := range []string{"agent-gen-all.sh", "quickstart.sh"} {
		if _, err := os.Stat(filepath.Join(afSrc, script)); err != nil {
			return fmt.Errorf("cannot run af install --agents: %s missing under source tree %q; set AF_SOURCE_ROOT to a complete agentfactory checkout or run from a built install", script, afSrc)
		}
	}

	// Guard 3 (R8/G11) — WARN (do not block) when CWD is the AF source repo: the
	// agent-gen orphan-removal pass (agent-gen-all.sh:82-106) is destructive there.
	// sameDir returns a==b on stat error, so worst case is a missed warning, never
	// a wrong block — do NOT promote to a refusal.
	if sameDir(cwd, afSrc) {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: running from the agentfactory source repo — agent-gen-all.sh will remove local formulas/templates that have no source counterpart (destructive orphan removal)")
	}

	// Guard 5 (R1/G3) — WARN when the caller is itself a live agent session: the
	// impending `af down --all` (agent-gen-all.sh:57) SIGKILLs every agent,
	// including this one. Transparent self-survival is infeasible (C-1) — surface
	// it, then proceed.
	if inAgentSession() {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: running inside a live agent session — agent-gen-all.sh runs `af down --all`, which will SIGKILL all agents including this session")
	}

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
	return runQuickstartScript(cmd, afSrc, cwd) // stdin←/dev/null (exec-bash mitigation)
}

// inAgentSession reports whether this process is running inside a live agent
// session, detected via the AF_ROLE env var the session manager sets
// (session.go:288-294). There is no central helper today — the package reads
// AF_ROLE directly in done.go/containment.go/helpers.go; this mirrors them.
func inAgentSession() bool { return os.Getenv("AF_ROLE") != "" }

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

// runQuickstartScript is the ADR-009 seam for quickstart.sh. Stdin is /dev/null
// so the script's terminal `exec bash` (quickstart.sh:625) reads EOF and exits
// instead of hanging or hijacking the session (ADR-014 mitigation).
var runQuickstartScript = func(cmd *cobra.Command, afSrc, projectDir string) error {
	c := exec.Command(filepath.Join(afSrc, "quickstart.sh")) // argv form; no --no-build
	c.Dir = projectDir
	c.Env = append(os.Environ(), "AF_SRC="+afSrc)
	c.Stdin = nil // nil ⇒ /dev/null in os/exec → exec bash gets EOF
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

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		roleType := claude.RoleTypeFor(name, agents)
		if err := claude.EnsureSettings(config.AgentDir(cwd, name), roleType); err != nil {
			fmt.Fprintf(out, "warning: could not re-provision settings for agent %s: %v\n", name, err)
		}
	}

	return nil
}
