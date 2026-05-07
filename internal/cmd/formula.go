package cmd

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/templates"
	"github.com/stempeck/agentfactory/internal/tmux"
)

var (
	agentGenOutput bool
	agentGenName   string
	agentGenType   string
	agentGenDelete bool
	agentGenAFSrc  string
	agentGenBuild  bool
)

var compiledSourceRoot string

func SetSourceRoot(root string) {
	compiledSourceRoot = root
}

var formulaCmd = &cobra.Command{
	Use:   "formula",
	Short: "Formula management commands",
}

var agentGenCmd = &cobra.Command{
	Use:   "agent-gen <formula-name>",
	Short: "Generate agent .md from formula TOML",
	Long: `Generate a 3-layer CLAUDE.md from a formula TOML file.

The generated agent contains Identity, Operational Knowledge, and Behavioral
Discipline layers with formula-specific sling commands, step structure, gate
protocol, and the formula description verbatim.

Example:
  af formula agent-gen my-workflow
  af formula agent-gen my-workflow -o
  af formula agent-gen my-workflow --name my-agent --type autonomous`,
	Args: cobra.ExactArgs(1),
	RunE: runFormulaAgentGen,
}

func init() {
	agentGenCmd.Flags().BoolVarP(&agentGenOutput, "output", "o", false, "Print CLAUDE.md to stdout (dry run, no provisioning)")
	agentGenCmd.Flags().StringVar(&agentGenName, "name", "", "Override agent name (default: formula name)")
	agentGenCmd.Flags().StringVar(&agentGenType, "type", "autonomous", "Agent type")
	agentGenCmd.Flags().BoolVar(&agentGenDelete, "delete", false, "Remove formula-generated agent and its artifacts")
	agentGenCmd.Flags().StringVar(&agentGenAFSrc, "af-src", "", "Path to agentfactory source tree (overrides auto-detection)")
	agentGenCmd.Flags().BoolVar(&agentGenBuild, "build", false, "Run 'make install' after writing template")
	formulaCmd.AddCommand(agentGenCmd)
	rootCmd.AddCommand(formulaCmd)
}

func resolveAFSource(factoryRoot string) (resolved string, fallback bool) {
	candidates := []struct {
		label string
		path  string
	}{
		{"--af-src flag", agentGenAFSrc},
		{"AF_SOURCE_ROOT env", os.Getenv("AF_SOURCE_ROOT")},
		{"compiled sourceRoot", compiledSourceRoot},
	}
	for _, c := range candidates {
		if c.path == "" {
			continue
		}
		if validateAFSource(c.path) {
			if real, err := filepath.EvalSymlinks(c.path); err == nil {
				return real, false
			}
			return c.path, false
		}
	}
	return factoryRoot, true
}

func validateAFSource(path string) bool {
	data, err := os.ReadFile(filepath.Join(path, "go.mod"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "agentfactory")
}

func sameDir(a, b string) bool {
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return os.SameFile(infoA, infoB)
}

func runFormulaAgentGen(cmd *cobra.Command, args []string) error {
	// --delete: treat args[0] as agent name and branch to delete path
	if agentGenDelete {
		if agentGenOutput {
			return fmt.Errorf("--delete and --output are mutually exclusive")
		}
		if agentGenName != "" {
			return fmt.Errorf("--delete and --name are mutually exclusive")
		}
		if agentGenType != "autonomous" {
			return fmt.Errorf("--delete and --type are mutually exclusive")
		}
		return runFormulaAgentGenDelete(cmd, args[0])
	}

	formulaName := args[0]

	wd, err := getWd()
	if err != nil {
		return err
	}

	// Find and parse formula
	formulaPath, err := formula.FindFormulaFile(formulaName, wd)
	if err != nil {
		return fmt.Errorf("finding formula: %w", err)
	}

	f, err := formula.ParseFile(formulaPath)
	if err != nil {
		return fmt.Errorf("parsing formula %s: %w", formulaPath, err)
	}

	// Determine agent name
	agentName := formulaName
	if agentGenName != "" {
		agentName = agentGenName
	}

	// Validate agent name before generating content
	if err := config.ValidateAgentName(agentName); err != nil {
		return err
	}

	// Generate agent template
	tmplContent := generateAgentTemplate(f, agentName, agentGenType)

	// Find factory root — hard error if not found (needed for -o rendering too)
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	// Workspace dir (needed for rendering)
	wsDir := config.AgentDir(root, agentName)

	// Render template to get CLAUDE.md content
	data := templates.RoleData{
		Role:        agentName,
		Description: f.Description,
		RootDir:     root,
		WorkDir:     wsDir,
	}
	content, err := renderTemplateString(tmplContent, data)
	if err != nil {
		return fmt.Errorf("rendering agent template: %w", err)
	}

	// -o flag: dry run to stdout (rendered, not raw template)
	if agentGenOutput {
		fmt.Fprint(cmd.OutOrStdout(), content)
		return nil
	}

	// Compute step count for success output
	var stepCount int
	switch f.Type {
	case formula.TypeWorkflow:
		stepCount = len(f.Steps)
	case formula.TypeConvoy:
		stepCount = len(f.Legs)
	case formula.TypeExpansion:
		stepCount = len(f.Template)
	case formula.TypeAspect:
		stepCount = len(f.Aspects)
	}

	// Count gates
	gateCount := 0
	for _, s := range f.Steps {
		if stepHasGate(s) {
			gateCount++
		}
	}

	// Print formula info
	fmt.Fprintf(cmd.ErrOrStderr(), "✓ Formula: %s (%s, %d steps, %d gates)\n", f.Name, f.Type, stepCount, gateCount)

	// Load agents.json
	agentsPath := config.AgentsConfigPath(root)
	cfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	// Detect if this is an update (existing entry with formula field)
	existing, isUpdate := cfg.Agents[agentName]
	isUpdate = isUpdate && existing.Formula != ""

	// Build agent entry — description is the first sentence (up to and including first period).
	// Formula descriptions MUST start with a plain sentence, not a heading or markdown.
	desc := f.Description
	if i := strings.IndexByte(desc, '.'); i >= 0 {
		desc = strings.TrimSpace(desc[:i+1])
	}
	entry := config.AgentEntry{
		Type:        agentGenType,
		Description: desc,
		Directive:   "Run af prime to load formula context.",
		Formula:     f.Name,
	}

	// Add entry (returns error for manual agents)
	if err := config.AddAgentEntry(cfg, agentName, entry); err != nil {
		return err
	}

	// Save agents.json
	if err := config.SaveAgentConfig(agentsPath, cfg); err != nil {
		return err
	}

	if isUpdate {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ Agent entry updated in .agentfactory/agents.json\n")
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ Agent entry added to .agentfactory/agents.json (formula: %s)\n", f.Name)
	}

	afSrc, isFallback := resolveAFSource(root)
	if isFallback {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: could not locate AF source tree; skipping template write. Template will not be embedded.\n")
	} else {
		tmplDir := filepath.Join(afSrc, "internal", "templates", "roles")
		if err := os.MkdirAll(tmplDir, 0755); err != nil {
			return fmt.Errorf("creating template directory: %w", err)
		}
		tmplPath := filepath.Join(tmplDir, agentName+".md.tmpl")
		if err := os.WriteFile(tmplPath, []byte(tmplContent), 0644); err != nil {
			return fmt.Errorf("writing role template: %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ Role template written: internal/templates/roles/%s.md.tmpl\n", agentName)
	}

	// Create workspace directory
	wsDirCreated := false
	if _, err := os.Stat(wsDir); os.IsNotExist(err) {
		wsDirCreated = true
	}
	if err := os.MkdirAll(wsDir, 0755); err != nil {
		return fmt.Errorf("creating workspace directory: %w", err)
	}

	if wsDirCreated {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ Workspace created: %s/\n", wsDir)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ Workspace exists: %s/\n", wsDir)
	}

	// Write CLAUDE.md
	claudePath := filepath.Join(wsDir, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing CLAUDE.md: %w", err)
	}

	if isUpdate {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ CLAUDE.md updated (%s)\n", humanSize(len(content)))
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ CLAUDE.md written (%s)\n", humanSize(len(content)))
	}

	// Map agent type string to claude.RoleType
	roleType := claude.Interactive
	if agentGenType == "autonomous" {
		roleType = claude.Autonomous
	}

	// Write settings.json
	if err := claude.EnsureSettings(wsDir, roleType); err != nil {
		return fmt.Errorf("writing settings.json: %w", err)
	}

	if isUpdate {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ .claude/settings.json verified\n")
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ .claude/settings.json written (%s)\n", agentGenType)
	}

	// Final ready line
	if isUpdate {
		fmt.Fprintf(cmd.ErrOrStderr(), "\nAgent %q updated. Start with: af up %s\n", agentName, agentName)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "\nAgent %q is ready. Start with: af up %s\n", agentName, agentName)
	}
	if agentGenBuild {
		makeCmd := exec.Command("make", "-C", afSrc, "install")
		makeCmd.Stdout = cmd.OutOrStdout()
		makeCmd.Stderr = cmd.ErrOrStderr()
		if err := makeCmd.Run(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: make install failed: %v\nTemplate and workspace are written — agent is functional. Fix your Go environment and run 'make -C %s install' later.\n", err, afSrc)
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "✓ Binary rebuilt with new template.\n")
		}
	} else if !sameDir(afSrc, root) {
		fmt.Fprintf(cmd.ErrOrStderr(), "Template written to %s. Run 'make -C %s install' to embed, or re-run with --build.\n", afSrc, afSrc)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "Run 'make build' to compile the new template into the af binary.\n")
	}

	return nil
}

// runFormulaAgentGenDelete removes a formula-generated agent and its artifacts.
// It deletes the config entry from agents.json, the .md.tmpl template file,
// and the workspace directory. It refuses to delete manual agents or agents
// with live tmux sessions.
func runFormulaAgentGenDelete(cmd *cobra.Command, agentName string) error {
	// Validate agent name
	if err := config.ValidateAgentName(agentName); err != nil {
		return err
	}

	wd, err := getWd()
	if err != nil {
		return err
	}

	// Find factory root
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	// Load agents.json
	agentsPath := config.AgentsConfigPath(root)
	cfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	// Check for live tmux session before any mutations
	tmuxClient := tmux.NewTmux()
	sessionID := session.SessionName(agentName)
	running, _ := tmuxClient.HasSession(sessionID)
	if running {
		return fmt.Errorf("agent %q has a live tmux session (%s) — stop it first with: af down %s", agentName, sessionID, agentName)
	}

	// Remove config entry (refuses manual agents and nonexistent agents)
	if err := config.RemoveAgentEntry(cfg, agentName); err != nil {
		return err
	}

	// Paths
	afSrc, isFallback := resolveAFSource(root)
	wsDir := config.AgentDir(root, agentName)

	// Check workspace for uncommitted changes (warn only)
	if _, err := os.Stat(wsDir); err == nil {
		gitCmd := exec.Command("git", "status", "--porcelain", wsDir)
		gitCmd.Dir = root
		if output, gitErr := gitCmd.Output(); gitErr == nil && len(output) > 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "⚠ Workspace %s/ has uncommitted changes\n", agentName)
		}
	}

	// Save agents.json (entry already removed from cfg)
	if err := config.SaveAgentConfig(agentsPath, cfg); err != nil {
		return fmt.Errorf("saving agents.json: %w", err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "✓ Agent entry removed from .agentfactory/agents.json\n")

	// Remove template file (only when AF source tree is available)
	if !isFallback {
		tmplPath := filepath.Join(afSrc, "internal", "templates", "roles", agentName+".md.tmpl")
		if err := os.Remove(tmplPath); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(cmd.ErrOrStderr(), "✓ Role template already absent: internal/templates/roles/%s.md.tmpl\n", agentName)
			} else {
				return fmt.Errorf("removing template: %w", err)
			}
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "✓ Role template removed: internal/templates/roles/%s.md.tmpl\n", agentName)
		}
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ Skipping template removal (not in AF source tree)\n")
	}

	// Remove workspace directory (tolerate missing)
	if _, err := os.Stat(wsDir); os.IsNotExist(err) {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ Workspace already absent: %s/\n", wsDir)
	} else if err := os.RemoveAll(wsDir); err != nil {
		return fmt.Errorf("removing workspace: %w", err)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "✓ Workspace removed: %s/\n", wsDir)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "\nAgent %q deleted.\n", agentName)
	fmt.Fprintf(cmd.ErrOrStderr(), "Run 'make build' to remove the template from the compiled af binary.\n")

	return nil
}

// humanSize formats a byte count as a human-readable string.
func humanSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	return fmt.Sprintf("%.1f KB", float64(bytes)/1024.0)
}

// generateAgentTemplate produces a .md.tmpl Go template from a parsed formula.
// The output uses {{ .Role }}, {{ .Description }}, {{ .RootDir }}, {{ .WorkDir }}
// for variable content. Formula-specific content is baked in as literal text.
func generateAgentTemplate(f *formula.Formula, agentName string, agentType string) string {
	var b strings.Builder

	// HTML comment header
	b.WriteString(fmt.Sprintf("<!-- Generated by af formula agent-gen from %s v%d -->\n\n", f.Name, f.Version))

	// Identity (matches supervisor.md.tmpl structure)
	b.WriteString("# Agent Identity: {{ .Role }}\n\n")
	b.WriteString("You are **{{ .Role }}**, {{ .Description }}.\n\n")
	b.WriteString("You are an autonomous agent that acts independently without waiting for user input.\n\n")

	// Workspace
	b.WriteString("## Workspace\n\n")
	b.WriteString("- **Factory root**: `{{ .RootDir }}`\n")
	b.WriteString("- **Working directory**: `{{ .WorkDir }}`\n\n")

	// Operational Knowledge (formula-specific, escaped for template safety)
	b.WriteString("## Operational Knowledge\n\n")
	b.WriteString(escapeTmplDelimiters(generateOperationalPlaybook(f)))
	b.WriteString(escapeTmplDelimiters(generateGateProtocol(f)))
	b.WriteString(escapeTmplDelimiters(generateStructureTable(f)))
	b.WriteString(escapeTmplDelimiters(generateVariablesTable(f)))

	// Available Commands (merged: formula-specific + standard from supervisor.md.tmpl)
	hasGates := formulaHasGates(f)
	b.WriteString("### Available Commands\n")
	b.WriteString("- `af prime` — Re-inject identity and formula step context\n")
	b.WriteString("- `af done` — Close current step and advance\n")
	if hasGates {
		b.WriteString("- `af done --phase-complete --gate <id>` — Complete a gate step (session ends)\n")
	}
	b.WriteString("- `af mail send <to> -s <subject> -m <message>` — Send a message to an agent or group\n")
	b.WriteString("- `af mail inbox` — List unread messages\n")
	b.WriteString("- `af mail read <id>` — Read a specific message\n")
	b.WriteString("- `af mail delete <id>` — Delete/acknowledge a message\n")
	b.WriteString("- `af mail check` — Check for new mail\n")
	b.WriteString("- `af mail reply <id> -m <message>` — Reply to a message\n")
	b.WriteString("- `af prime` — Re-inject identity context\n")
	b.WriteString("- `af root` — Print factory root path\n")

	// Behavioral Discipline (formula-specific, escaped)
	b.WriteString("\n## Behavioral Discipline\n\n")
	if f.Description != "" {
		b.WriteString(escapeTmplDelimiters(f.Description))
		b.WriteString("\n")
	}

	// Standard sections from supervisor.md.tmpl
	b.WriteString("\n## Mail Protocol\n\n")
	b.WriteString("- Check your inbox on startup for pending instructions or status updates.\n")
	b.WriteString("- Respond to messages that require acknowledgment.\n")
	b.WriteString("- Send status updates when completing significant work.\n")
	b.WriteString("- Use `@all` to broadcast to all agents, or group names for targeted messages.\n")

	b.WriteString("\n## Startup Protocol\n\n")
	b.WriteString("1. Check mail for pending instructions (`af mail inbox`)\n")
	b.WriteString("2. Act on any hooked work or queued tasks\n")
	b.WriteString("3. Begin autonomous execution — monitor, patrol, and act independently\n")

	b.WriteString("\n## Constraints\n\n")
	b.WriteString("- Stay within your workspace directory.\n")
	b.WriteString("- Use `af` commands for all inter-agent communication.\n")
	b.WriteString("- Do not modify other agents' directories or mailboxes directly.\n")
	b.WriteString("- Follow the factory's established conventions and workflows.\n")
	b.WriteString("- Act autonomously — do not wait for user prompts between tasks.\n")

	return b.String()
}

// escapeTmplDelimiters escapes {{ and }} in literal text so it can be safely
// embedded in a Go template string without being interpreted as template actions.
func escapeTmplDelimiters(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if i+1 < len(s) && s[i] == '{' && s[i+1] == '{' {
			b.WriteString(`{{ "{{" }}`)
			i++ // skip second '{'
		} else if i+1 < len(s) && s[i] == '}' && s[i+1] == '}' {
			b.WriteString(`{{ "}}" }}`)
			i++ // skip second '}'
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// renderTemplateString parses and executes a Go template string with the given data.
func renderTemplateString(tmplStr string, data templates.RoleData) (string, error) {
	t, err := template.New("agent").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	return buf.String(), nil
}

// generateOperationalPlaybook builds the "How You Work" section with formula-specific
// sling command and drive loop instructions.
func generateOperationalPlaybook(f *formula.Formula) string {
	var b strings.Builder

	b.WriteString("### How You Work\n")

	// Sling command with formula-specific vars
	b.WriteString("When given work, instantiate your formula:\n")
	b.WriteString("```\n")
	sling := fmt.Sprintf("af sling --formula %s", f.Name)

	rvs, err := collectRenderVars(f)
	if err != nil {
		log.Printf("warning: %v", err)
		sling += " --no-launch"
		b.WriteString(sling + "\n")
		b.WriteString("```\n")
		return b.String()
	}

	// Filter to CLI-sourced entries only (source == "cli" or source == "")
	var cliRequired []renderVar
	var cliOptional []renderVar
	for _, rv := range rvs {
		if rv.Source != "cli" && rv.Source != "" {
			continue
		}
		if rv.Required {
			cliRequired = append(cliRequired, rv)
		} else {
			cliOptional = append(cliOptional, rv)
		}
	}

	// Emit --var flags for required CLI entries
	for _, rv := range cliRequired {
		sling += fmt.Sprintf(" --var %s=<%s>", rv.Name, varPlaceholder(rv))
	}

	sling += " --no-launch"
	b.WriteString(sling + "\n")

	// H1: If no required CLI entries but optional CLI entries exist,
	// emit commented optional lines for discoverability
	if len(cliRequired) == 0 && len(cliOptional) > 0 {
		for _, rv := range cliOptional {
			b.WriteString(fmt.Sprintf("# Optional: --var %s=<%s>\n", rv.Name, varPlaceholder(rv)))
		}
	}

	b.WriteString("```\n")

	// Handoff to clean session
	b.WriteString("\nThen cycle to a clean session:\n")
	b.WriteString("```\n")
	b.WriteString("af handoff\n")
	b.WriteString("```\n\n")

	// Drive loop
	b.WriteString("Then drive the workflow:\n")
	b.WriteString("```\n")
	b.WriteString("af prime              # Load identity + current step instructions\n")
	b.WriteString("[execute the step]\n")
	b.WriteString("af done               # Close step and advance\n")
	b.WriteString("```\n")
	b.WriteString("Repeat until all steps are complete.\n\n")

	b.WriteString("**Important:** Complete your current formula instance before accepting new work.\n\n")

	return b.String()
}

// generateGateProtocol builds the gate protocol section. Returns empty string if
// the formula has no gates (structural or title-heuristic).
func generateGateProtocol(f *formula.Formula) string {
	if !formulaHasGates(f) {
		return ""
	}

	// Count gates
	gateCount := 0
	for _, s := range f.Steps {
		if stepHasGate(s) {
			gateCount++
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("### Gate Steps\n"))
	b.WriteString(fmt.Sprintf("This formula has %d gate checkpoints. Some steps have gates — structural interlocks\n", gateCount))
	b.WriteString("that cannot be closed until an external condition is met. When you reach a gate step:\n")
	b.WriteString("1. Complete the work described in the step\n")
	b.WriteString("2. Run `af done --phase-complete --gate <gate-id>`\n")
	b.WriteString("3. Your session ends. A fresh agent resumes when the gate resolves.\n\n")

	return b.String()
}

// generateStructureTable builds the formula structure section with metadata and
// type-specific table (steps for workflows, legs for convoys, etc.).
func generateStructureTable(f *formula.Formula) string {
	var b strings.Builder

	b.WriteString("### Formula Structure\n")
	b.WriteString(fmt.Sprintf("- **Name**: %s\n", f.Name))
	b.WriteString(fmt.Sprintf("- **Type**: %s\n", f.Type))

	switch f.Type {
	case formula.TypeWorkflow:
		gateCount := 0
		hasTitleHeuristic := false
		for _, s := range f.Steps {
			if stepHasGate(s) {
				gateCount++
			}
			if s.Gate == nil && stepTitleHasGate(s) {
				hasTitleHeuristic = true
			}
		}
		b.WriteString(fmt.Sprintf("- **Steps**: %d (%d gates)\n", len(f.Steps), gateCount))

		b.WriteString("\n| # | Step | Gate |\n")
		b.WriteString("|---|------|------|\n")
		for i, s := range f.Steps {
			gate := ""
			if s.Gate != nil {
				gate = "GATE"
			} else if stepTitleHasGate(s) {
				gate = "GATE*"
			}
			b.WriteString(fmt.Sprintf("| %d | %s | %s |\n", i+1, s.Title, gate))
		}

		if hasTitleHeuristic {
			b.WriteString("\n*GATE markers with `*` are detected by title heuristic (case-insensitive \"gate\" in step title), not by structural `[gate]` definition in the TOML.\n")
		}
		b.WriteString("\n")

	case formula.TypeConvoy:
		b.WriteString(fmt.Sprintf("- **Legs**: %d (parallel)\n", len(f.Legs)))
		if len(f.Legs) > 0 {
			b.WriteString("\n| Leg | Focus |\n")
			b.WriteString("|-----|-------|\n")
			for _, l := range f.Legs {
				b.WriteString(fmt.Sprintf("| %s | %s |\n", l.Title, l.Focus))
			}
			b.WriteString("\n")
		}
		if f.Synthesis != nil {
			b.WriteString(fmt.Sprintf("**Synthesis**: %s — combines all leg outputs\n\n", f.Synthesis.Title))
		}

	case formula.TypeExpansion:
		b.WriteString(fmt.Sprintf("- **Templates**: %d\n", len(f.Template)))
		if len(f.Template) > 0 {
			b.WriteString("\n| # | Template |\n")
			b.WriteString("|---|----------|\n")
			for i, t := range f.Template {
				b.WriteString(fmt.Sprintf("| %d | %s |\n", i+1, t.Title))
			}
			b.WriteString("\n")
		}

	case formula.TypeAspect:
		b.WriteString(fmt.Sprintf("- **Aspects**: %d (parallel)\n", len(f.Aspects)))
		if len(f.Aspects) > 0 {
			b.WriteString("\n| Aspect | Focus |\n")
			b.WriteString("|--------|-------|\n")
			for _, a := range f.Aspects {
				b.WriteString(fmt.Sprintf("| %s | %s |\n", a.Title, a.Focus))
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// generateVariablesTable builds the variables section with Required/Source/Description columns.
// Returns empty string if the formula has no variables.
func generateVariablesTable(f *formula.Formula) string {
	if len(f.Inputs) == 0 && len(f.Vars) == 0 {
		return ""
	}

	rvs, err := collectRenderVars(f)
	if err != nil {
		log.Printf("warning: %v", err)
		return ""
	}

	if len(rvs) == 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString("### Variables\n\n")
	b.WriteString("| Variable | Required | Source | Description |\n")
	b.WriteString("|----------|----------|--------|-------------|\n")

	for _, rv := range rvs {
		req := "no"
		if rv.Required {
			req = "yes"
		}
		if len(rv.RequiredUnless) > 0 {
			req = "conditional"
		}
		source := rv.Source
		if source == "" {
			source = "-"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n", rv.Name, req, source, rv.Description))
	}
	b.WriteString("\n")

	return b.String()
}

// formulaHasGates returns true if any step has a structural gate or a title-heuristic gate.
func formulaHasGates(f *formula.Formula) bool {
	for _, s := range f.Steps {
		if stepHasGate(s) {
			return true
		}
	}
	return false
}

// stepHasGate returns true if a step has either a structural gate or title-heuristic gate.
func stepHasGate(s formula.Step) bool {
	return s.Gate != nil || stepTitleHasGate(s)
}

// stepTitleHasGate returns true if the step title contains "gate" (case-insensitive).
func stepTitleHasGate(s formula.Step) bool {
	return strings.Contains(strings.ToLower(s.Title), "gate")
}

// renderVar is a unified rendering adapter that bridges formula.Input and formula.Var
// into a single type for the generator functions.
type renderVar struct {
	Name           string
	Description    string
	Required       bool
	Source         string   // "cli" for inputs, actual source for vars
	IsInput        bool     // true if from [inputs] block
	HasDefault     bool
	Default        string
	RequiredUnless []string // only populated for inputs
}

// collectRenderVars builds a sorted slice of renderVar from a formula's Inputs and Vars.
// It returns an error if any input name collides with a var name.
func collectRenderVars(f *formula.Formula) ([]renderVar, error) {
	result := make([]renderVar, 0, len(f.Inputs)+len(f.Vars))

	// Map inputs to renderVars
	for name, input := range f.Inputs {
		if _, exists := f.Vars[name]; exists {
			return nil, fmt.Errorf("input %q collides with existing var", name)
		}
		result = append(result, renderVar{
			Name:           name,
			Description:    input.Description,
			Required:       input.Required,
			Source:         "cli",
			IsInput:        true,
			HasDefault:     input.Default != "",
			Default:        input.Default,
			RequiredUnless: input.RequiredUnless,
		})
	}

	// Map vars to renderVars
	for name, v := range f.Vars {
		result = append(result, renderVar{
			Name:        name,
			Description: v.Description,
			Required:    v.Required,
			Source:      v.Source,
			IsInput:     false,
			HasDefault:  v.Default != "",
			Default:     v.Default,
		})
	}

	// Sort: required inputs first, optional inputs, cli vars, non-cli vars
	// Within each group, alphabetical by name
	sort.SliceStable(result, func(i, j int) bool {
		ci := renderVarCategory(result[i])
		cj := renderVarCategory(result[j])
		if ci != cj {
			return ci < cj
		}
		return result[i].Name < result[j].Name
	})

	return result, nil
}

// renderVarCategory returns a sort key for ordering renderVars:
// 0 = required input, 1 = optional input, 2 = cli var, 3 = non-cli var
func renderVarCategory(rv renderVar) int {
	if rv.IsInput && rv.Required {
		return 0
	}
	if rv.IsInput {
		return 1
	}
	if rv.Source == "cli" || rv.Source == "" {
		return 2
	}
	return 3
}

// varPlaceholder returns a meaningful placeholder for a variable's sling argument.
func varPlaceholder(rv renderVar) string {
	desc := rv.Description
	if desc == "" {
		return "value"
	}
	// Truncate at the earliest separator (comma, period, paren, em-dash)
	minIdx := len(desc)
	for _, sep := range []string{",", ".", "(", " — "} {
		if idx := strings.Index(desc, sep); idx > 0 && idx < minIdx {
			minIdx = idx
		}
	}
	desc = desc[:minIdx]
	desc = strings.ToLower(strings.TrimSpace(desc))
	desc = strings.ReplaceAll(desc, " ", "-")
	return desc
}
