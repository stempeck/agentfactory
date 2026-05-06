package cmd

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
	"github.com/stempeck/agentfactory/internal/templates"
)

//go:embed install_hooks/*
var hooksFS embed.FS

//go:embed install_formulas/*
var formulasFS embed.FS

var installInitFlag bool

var installCmd = &cobra.Command{
	Use:   "install [role]",
	Short: "Initialize factory or provision an agent",
	Long: `Initialize a new factory workspace (--init) or provision an agent role.

Factory initialization creates the config directory, starter configs,
beads database, and hooks directory.

Agent provisioning creates the agent directory, renders CLAUDE.md from
the role template, and writes Claude Code settings.json with hooks.`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().BoolVar(&installInitFlag, "init", false, "Initialize a new factory workspace")
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
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
		"factory.json": `{"type":"factory","version":1,"name":"agentfactory"}`,
		"agents.json":  `{"agents":{"manager":{"type":"interactive","description":"Interactive agent for human-supervised work","directive":"Read your memory and docs, and prove it."},"supervisor":{"type":"autonomous","description":"Autonomous agent for independent task execution","directive":"Read your memory and docs, and prove it."}}}`,
		"messaging.json": `{"groups":{"all":["manager","supervisor"]}}`,
		"dispatch.json":  `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`,
	}

	for name, content := range starterConfigs {
		path := filepath.Join(configDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return fmt.Errorf("writing %s: %w", name, err)
			}
		}
	}

	// 4. Write AGENTS.md (only if it doesn't exist — preempts bd init's version)
	agentsMd := filepath.Join(cwd, "AGENTS.md")
	if _, err := os.Stat(agentsMd); os.IsNotExist(err) {
		const agentsMdContent = `# Agent Commands

- ` + "`af prime`" + ` — Re-inject identity and formula step context
- ` + "`af done`" + ` — Close current step and advance
- ` + "`af mail send <to> -s <subject> -m <message>`" + ` — Send a message
- ` + "`af mail inbox`" + ` — List unread messages
- ` + "`af mail read <id>`" + ` — Read a message
- ` + "`af mail check`" + ` — Check for new mail
- ` + "`af mail reply <id> -m <message>`" + ` — Reply to a message
- ` + "`af root`" + ` — Print factory root path
`
		if err := os.WriteFile(agentsMd, []byte(agentsMdContent), 0644); err != nil {
			return fmt.Errorf("writing AGENTS.md: %w", err)
		}
	}

	// 5. Initialize the issue store (mcpstore lazy-starts the Python MCP
	//    server under py/issuestore/; the first call opens/creates the SQLite
	//    database at <factoryRoot>/.beads/beads.db). This is the ONE consumer
	//    that uses mcpstore.New directly (not via the newIssueStore seam)
	//    because the install flow needs the user-visible side effect —
	//    database bootstrap — to print a confirmation banner. An empty actor
	//    is passed because install runs before any agent session and does not
	//    call List (actor scoping is a List-only concern). mcpstore.New is
	//    idempotent (the server performs CREATE TABLE IF NOT EXISTS), so no
	//    metadata.json stat-gate is needed.
	beadsDir := filepath.Join(cwd, ".beads")
	store, err := mcpstore.New(cwd, "")
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}
	_ = store
	fmt.Fprintln(cmd.OutOrStdout(), "Issue store initialized (SQLite + MCP server)")

	// 6. Create hooks/ directory and write quality gate files
	hooksDir := filepath.Join(cwd, "hooks")
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

	// Enable fidelity gate by default for new factories
	fidelityToggle := filepath.Join(cwd, ".fidelity-gate")
	if _, err := os.Stat(fidelityToggle); os.IsNotExist(err) {
		if err := os.WriteFile(fidelityToggle, []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("writing .fidelity-gate: %w", err)
		}
	}

	// 7. Write default formula files to .beads/formulas/ (skip if content matches)
	formulasDir := filepath.Join(beadsDir, "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		return fmt.Errorf("creating formulas directory: %w", err)
	}
	if err := writeFormulas(formulasDir); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Factory initialized successfully.")
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
