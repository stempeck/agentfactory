package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	tmpl := New()
	if tmpl == nil {
		t.Fatal("New() returned nil")
	}
}

func TestRenderRole_Manager(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "manager") {
		t.Error("output should contain role name 'manager'")
	}
	if !strings.Contains(output, "Factory coordinator") {
		t.Error("output should contain description")
	}
}

func TestRenderRole_Supervisor(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "supervisor",
		Description: "Autonomous worker",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/supervisor",
	}
	output, err := tmpl.RenderRole("supervisor", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "supervisor") {
		t.Error("output should contain role name 'supervisor'")
	}
	if !strings.Contains(strings.ToLower(output), "autonomous") {
		t.Error("supervisor template should mention 'autonomous'")
	}
}

func TestRenderRole_UnknownRole(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "unknown",
		Description: "test",
		RootDir:     "/tmp",
		WorkDir:     "/tmp",
	}
	_, err := tmpl.RenderRole("unknown", data)
	if err == nil {
		t.Fatal("RenderRole should return error for unknown role")
	}
}

func TestRenderRole_AllFieldsSubstituted(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if strings.Contains(output, "{{ .") {
		t.Error("output contains unresolved template variables")
	}
}



func TestManagerTemplate_HasBehavioralSections(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Interactive agent for human-supervised work",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}

	requiredSections := []string{
		"## Role Boundary",
		"## Specialist Catalog",
		"## Behavioral Discipline",
		"## Failure Modes",
		"## Anti-Patterns to Avoid",
		"## Escalation Protocol",
	}
	for _, section := range requiredSections {
		if !strings.Contains(output, section) {
			t.Errorf("manager template missing required section: %s", section)
		}
	}

	preservedSections := []string{
		"## Mail Protocol",
		"## Constraints",
		"## Startup Protocol",
	}
	for _, section := range preservedSections {
		if !strings.Contains(output, section) {
			t.Errorf("manager template lost existing section: %s", section)
		}
	}

	if !strings.Contains(output, "| Situation | Action |") {
		t.Error("Failure Modes section missing '| Situation | Action |' table header")
	}

	if !strings.Contains(output, "| Anti-Pattern | Prevention |") {
		t.Error("Anti-Patterns section missing '| Anti-Pattern | Prevention |' table header")
	}

	if strings.Count(output, "af sling") < 2 {
		t.Error("manager template should contain at least 2 references to 'af sling'")
	}

	if !strings.Contains(output, "routine operational tasks") {
		t.Error("Startup Protocol step 2 should contain 'routine operational tasks'")
	}
}

func TestManagerTemplate_ContainsMonitoringSection(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "## Monitoring Dispatched Work") {
		t.Error("manager template should contain '## Monitoring Dispatched Work' section")
	}
	if !strings.Contains(output, "capture-pane") {
		t.Error("manager template should contain 'capture-pane' monitoring mechanism")
	}
}

func TestManagerTemplate_ReferencesAgentsMD(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "## Specialist Catalog") {
		t.Error("manager template should contain '## Specialist Catalog' section")
	}
	// Anchored, not a bare substring: a bare ".agentfactory/AGENTS.md" check passes even when the
	// path renders with an empty anchor as "/.agentfactory/AGENTS.md", which points nowhere.
	if !strings.Contains(output, "/home/dev/factory/.agentfactory/AGENTS.md") {
		t.Error("manager template should reference the RootDir-anchored .agentfactory/AGENTS.md for the dynamic agent catalog")
	}
}

// TestManagerTemplate_CatalogPathAnchoredToRootDir pins that every one of the manager's three
// specialist-catalog references is anchored to RootDir -- the agent's own local root, which is
// the worktree root for a worktree agent and the factory root otherwise. The catalog is reachable
// there because worktreeSymlinks links .agentfactory/AGENTS.md into each worktree, and
// worktree.Contains allowlists exactly that path as in-bounds.
//
// It also pins the property issue #575 was actually about: the reference must never regress to
// the bare, cwd-relative "cat .agentfactory/AGENTS.md", which failed when run from an agent's own
// working directory. Absoluteness alone is NOT a sufficient guard -- an unset anchor renders
// "/.agentfactory/AGENTS.md", which is absolute and passes both filepath.IsAbs and a
// bare-relative-substring check -- so each occurrence is additionally required to sit under
// RootDir.
func TestManagerTemplate_CatalogPathAnchoredToRootDir(t *testing.T) {
	tmpl := New()
	rootDir := "/home/dev/factory/.agentfactory/worktrees/wt-x"
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     rootDir,
		WorkDir:     rootDir + "/.agentfactory/agents/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}

	wantPath := rootDir + "/.agentfactory/AGENTS.md"
	// One assertion per catalog site: the startup-checklist line, the "## Specialist Catalog"
	// prose line, and the fenced cat command.
	for _, site := range []struct{ name, fragment string }{
		{"startup checklist", "Check specialist catalog (Read `" + wantPath + "`)"},
		{"specialist-catalog prose", "Consult `" + wantPath + "`"},
		{"cat command", `cat "` + wantPath + `"`},
	} {
		if !strings.Contains(output, site.fragment) {
			t.Errorf("manager template %s should reference the RootDir-anchored catalog path: want %q in:\n%s", site.name, site.fragment, output)
		}
	}

	if got := strings.Count(output, wantPath); got != 3 {
		t.Errorf("manager template should reference the RootDir-anchored catalog path exactly 3 times, got %d", got)
	}
	if strings.Contains(output, "cat .agentfactory/AGENTS.md") {
		t.Error("manager template must not contain the bare, cwd-relative 'cat .agentfactory/AGENTS.md' -- that is the issue #575 bug")
	}
	if strings.Contains(output, "/.agentfactory/AGENTS.md\"") && !strings.Contains(output, rootDir+"/.agentfactory/AGENTS.md\"") {
		t.Error("the rendered cat command is absolute but not anchored under RootDir -- an unset anchor renders \"/.agentfactory/AGENTS.md\", which is absolute yet points nowhere")
	}
}

// TestRoleData_FieldSet pins the RoleData contract at exactly the four fields every role template
// renders against. text/template fails loudly on a MISSING field but silently renders an empty
// string for a field a construction site forgot to set, so an additional path-anchoring field
// could be reintroduced and quietly render "/.agentfactory/..." at any site that omitted it. This
// guard fires at that reintroduction. An AST scan for struct literals is deliberately not used:
// the compiler already rejects a literal naming a field that does not exist.
func TestRoleData_FieldSet(t *testing.T) {
	want := []string{"Role", "Description", "RootDir", "WorkDir"}
	typ := reflect.TypeOf(RoleData{})
	var got []string
	for i := 0; i < typ.NumField(); i++ {
		got = append(got, typ.Field(i).Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RoleData fields = %v, want %v -- role templates render against this exact contract", got, want)
	}
}

// TestAllRoleTemplates_RenderWithRoleDataContract renders every embedded role template against
// the RoleData contract, so a template referencing a field that does not exist fails here rather
// than at an agent's session start. This is a standing guard, not a behavioural assertion: it
// passes both before and after the catalog-anchor change.
func TestAllRoleTemplates_RenderWithRoleDataContract(t *testing.T) {
	entries, err := templateFS.ReadDir("roles")
	if err != nil {
		t.Fatalf("ReadDir(roles): %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no role templates found -- this guard would pass vacuously")
	}
	root := t.TempDir()
	tmpl := New()
	for _, e := range entries {
		role := strings.TrimSuffix(e.Name(), ".md.tmpl")
		t.Run(role, func(t *testing.T) {
			if _, err := tmpl.RenderRole(role, RoleData{
				Role:        role,
				Description: "test",
				RootDir:     root,
				WorkDir:     filepath.Join(root, ".agentfactory", "agents", role),
			}); err != nil {
				t.Errorf("RenderRole(%s) against the RoleData contract: %v", role, err)
			}
		})
	}
}

// TestAC1AC5AC7_CatalogInstruction_ExecutesFromAgentWorkingDirectory pins issue #575's AC-1
// (works as written, no manual path correction), AC-5 (non-worktree manager unaffected), and AC-7
// (coverage from an agent's own working directory, not just a worktree root or factory root). It
// extracts the literal shell command from the rendered "cat ..." catalog-read line and executes it
// for real, from every cwd shape a manager agent could actually run from, proving the instruction
// is genuinely cwd-independent rather than merely referencing an absolute-looking string.
//
// This is the direct evidence that anchoring the reference to the agent's own local root keeps
// #575 fixed: the original bug was the bare, cwd-relative form, not the choice of anchor.
func TestAC1AC5AC7_CatalogInstruction_ExecutesFromAgentWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	agentsMdDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(agentsMdDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sentinel := "## BEGIN AgentFactory Agents\n| `solver` | autonomous | Solves things |\n"
	if err := os.WriteFile(filepath.Join(agentsMdDir, "AGENTS.md"), []byte(sentinel), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	cases := []struct {
		name    string
		workDir string // relative to root; created fresh for each case
	}{
		{"AC7_worktree_agent_workdir", filepath.Join(".agentfactory", "worktrees", "wt-x", ".agentfactory", "agents", "manager")},
		{"AC5_non_worktree_agent_workdir", filepath.Join(".agentfactory", "agents", "manager")},
		{"AC7_worktree_root", filepath.Join(".agentfactory", "worktrees", "wt-x")},
		{"AC7_factory_root", "."},
	}

	tmpl := New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir := filepath.Join(root, tc.workDir)
			if err := os.MkdirAll(workDir, 0o755); err != nil {
				t.Fatalf("mkdir workDir: %v", err)
			}
			data := RoleData{
				Role:        "manager",
				Description: "Factory coordinator",
				RootDir:     root,
				WorkDir:     workDir,
			}
			output, err := tmpl.RenderRole("manager", data)
			if err != nil {
				t.Fatalf("RenderRole failed: %v", err)
			}
			cmdLine := extractCatalogReadCommand(t, output)
			cmd := exec.Command("sh", "-c", cmdLine)
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("catalog-read command %q failed from cwd %s: %v\noutput: %s", cmdLine, workDir, err, out)
			}
			if !strings.Contains(string(out), "solver") {
				t.Errorf("catalog-read command %q from cwd %s did not return the live catalog content, got: %s", cmdLine, workDir, out)
			}
		})
	}
}

// TestManagerTemplate_WorktreeWithoutCatalogLink_FailsObservably pins what happens in a worktree
// that has no .agentfactory/AGENTS.md link at all -- the state a worktree is left in when
// unlinkBeforeRemove strips its symlinks ahead of a `git worktree remove` that then fails, since
// EnsureWorktreeLinks only ever runs at worktree creation.
//
// Because the catalog reference is anchored to the agent's own local root, that state is a MISS,
// and the property worth pinning is that the miss is LOUD: a non-zero exit with a visible message
// and no catalog content, never a silent empty roster that would read as "there are no agents".
// manager.md.tmpl turns that visible failure into an instruction to treat the roster as
// not-yet-built rather than concluding the factory is empty.
//
// This scenario deliberately remains covered. Only the asserted outcome changed, and it changed
// because the design changed: an earlier revision reached the shared factory root directly from
// inside a worktree, which review rejected as pointing agents outside the tree they work in.
func TestManagerTemplate_WorktreeWithoutCatalogLink_FailsObservably(t *testing.T) {
	factoryRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(factoryRoot, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sentinel := "## BEGIN AgentFactory Agents\n| `checker` | autonomous | Checks things |\n"
	if err := os.WriteFile(filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"), []byte(sentinel), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	worktreeRoot := filepath.Join(factoryRoot, ".agentfactory", "worktrees", "wt-unlinked")
	worktreeAgentDir := filepath.Join(worktreeRoot, ".agentfactory", "agents", "manager")
	if err := os.MkdirAll(worktreeAgentDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree agent dir: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(worktreeRoot, ".agentfactory", "AGENTS.md")); err == nil {
		t.Fatal("test setup bug: a catalog symlink/file exists in the simulated worktree, defeating the point of this test")
	}

	tmpl := New()
	output, err := tmpl.RenderRole("manager", RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     worktreeRoot, // what primeAgent's FindLocalRoot override yields in a worktree
		WorkDir:     worktreeAgentDir,
	})
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}

	// The instruction must still be anchored to this worktree, not silently redirected to the
	// shared factory root -- that redirection is the rejected design.
	if !strings.Contains(output, worktreeRoot+"/.agentfactory/AGENTS.md") {
		t.Errorf("catalog reference should stay anchored to the agent's own root %q, got:\n%s", worktreeRoot, output)
	}

	cmdLine := extractCatalogReadCommand(t, output)
	cmd := exec.Command("sh", "-c", cmdLine)
	cmd.Dir = worktreeAgentDir
	out, cmdErr := cmd.CombinedOutput()
	if cmdErr == nil {
		t.Fatalf("catalog-read command %q should fail observably in a worktree with no catalog link, but it succeeded with output: %s", cmdLine, out)
	}
	if len(out) == 0 {
		t.Error("catalog-read failure must be visible on stdout/stderr, not silent empty output")
	}
	if strings.Contains(string(out), "checker") {
		t.Errorf("an unlinked worktree must not reach the shared factory-root catalog; got its content: %s", out)
	}
}

// TestAC6_MissingCatalogFile_FailsObservably pins issue #575's AC-6: if the catalog cannot be
// obtained, that must be observable (a normal, visible failure), never a silent fall-through to
// an empty or partial specialist list.
func TestAC6_MissingCatalogFile_FailsObservably(t *testing.T) {
	root := t.TempDir() // no .agentfactory/AGENTS.md written at all
	workDir := filepath.Join(root, ".agentfactory", "agents", "manager")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}

	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     root,
		WorkDir:     workDir,
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	cmdLine := extractCatalogReadCommand(t, output)
	cmd := exec.Command("sh", "-c", cmdLine)
	cmd.Dir = workDir
	out, cmdErr := cmd.CombinedOutput()
	if cmdErr == nil {
		t.Fatalf("catalog-read command %q should fail observably when no catalog exists, but it succeeded with output: %s", cmdLine, out)
	}
	if len(out) == 0 {
		t.Error("catalog-read command should produce a visible error message on stdout/stderr, not silent empty output")
	}
}

// extractCatalogReadCommand pulls the literal "cat ..." shell instruction for reading the
// specialist catalog out of a rendered manager CLAUDE.md, so tests exercise the actual
// instruction a manager agent would run rather than a hand-written approximation of it.
func extractCatalogReadCommand(t *testing.T, rendered string) string {
	t.Helper()
	for _, line := range strings.Split(rendered, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "cat ") && strings.Contains(trimmed, "AGENTS.md") {
			return trimmed
		}
	}
	t.Fatalf("could not find a catalog-read 'cat ... AGENTS.md' command in rendered output:\n%s", rendered)
	return ""
}

func TestManagerTemplate_NoHardcodedAgentNames(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	hardcodedAgents := []string{"| rootcause-all |", "| design-v7 |", "| ultra-implement |"}
	for _, agent := range hardcodedAgents {
		if strings.Contains(output, agent) {
			t.Errorf("manager template should NOT hardcode agent names, found: %s", agent)
		}
	}
}

func TestManagerTemplate_MonitoringIncludesFollowUpProtocol(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "af-<agent>") {
		t.Error("monitoring section should show session naming convention 'af-<agent>'")
	}
	if !strings.Contains(output, "progress") {
		t.Error("monitoring section should include guidance on checking agent progress")
	}
}

func TestHasRole(t *testing.T) {
	tmpl := New()

	if !tmpl.HasRole("manager") {
		t.Error("HasRole should return true for manager")
	}
	if !tmpl.HasRole("supervisor") {
		t.Error("HasRole should return true for supervisor")
	}
	if tmpl.HasRole("nonexistent") {
		t.Error("HasRole should return false for nonexistent role")
	}
}
