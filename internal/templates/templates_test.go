package templates

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
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
		t.Error("Startup Protocol step 5 should contain 'routine operational tasks'")
	}
}

// startupProtocolSection returns the manager's `## Startup Protocol` body — its heading through the
// line before the next H2. Scoping the cost assertions to that section is the point: the mandate
// lives there, and a figure parked in some other section is not a price the reader sees when the
// read is ordered.
//
// Splits naively on the next `\n## ` rather than tracking fenced blocks the way cronsDocSection
// (internal/cmd/dispatch_crons_doc_test.go:86) has to. Safe here and asserted below: the section is
// a numbered list with no fenced block, and a fence that appeared later would end the section EARLY,
// which fails the assertions rather than passing them vacuously.
func startupProtocolSection(t *testing.T, output string) string {
	t.Helper()
	const heading = "## Startup Protocol"
	start := strings.Index(output, heading)
	if start < 0 {
		t.Fatalf("manager template has no %q section", heading)
	}
	rest := output[start+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	if strings.Contains(rest, "```") {
		t.Fatalf("the Startup Protocol now contains a fenced block; this slicer cannot be trusted to "+
			"find the section end, so it needs fence tracking:\n%s", rest)
	}
	return rest
}

// TestManagerTemplate_StatesStartupReadCosts pins K10/U1-A (#675): the operator kept the mandated
// full read of USING_AGENTFACTORY.md and the anti-pattern row that enforces it, and in exchange the
// template states what the mandated reads cost. A mandate with no stated price is one an agent
// cannot budget against — it re-reads on every startup with no way to know it just spent a tenth of
// its window.
//
// For .agentfactory/AGENTS.md only shape is pinned: that file is per-factory and gitignored, so any
// literal would be a fact about one machine. USING_AGENTFACTORY.md ships in this repo, so its stated
// size is checked against the file — a mandated read whose price is wrong is the exact defect #675
// Phase 3 exists to remove, and it went stale twice while that phase was being written.
func TestManagerTemplate_StatesStartupReadCosts(t *testing.T) {
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
	startup := startupProtocolSection(t, output)

	// A byte count ("12,345 B") or a token estimate ("9.9k") both count as a size figure. The
	// examples are synthetic on purpose — a real measurement in this comment would go stale.
	sizeFigure := regexp.MustCompile(`\d[\d,]*\s*B\b|\d+(\.\d+)?k\b`)
	// Undated, a measurement cannot be told apart from one that went stale two releases ago. Asserted
	// per line, not per section, so a date sitting beside some unrelated prose cannot satisfy it.
	dated := regexp.MustCompile(`\b20\d{2}-\d{2}-\d{2}\b`)

	for _, read := range []struct{ name, mandated string }{
		{"USING_AGENTFACTORY.md", "USING_AGENTFACTORY.md"},
		{".agentfactory/AGENTS.md", data.RootDir + "/.agentfactory/AGENTS.md"},
	} {
		var line string
		for _, l := range strings.Split(startup, "\n") {
			if strings.Contains(l, read.mandated) {
				line = l
				break
			}
		}
		if line == "" {
			t.Errorf("the Startup Protocol does not name %s among its mandated reads", read.name)
			continue
		}
		if !sizeFigure.MatchString(line) {
			t.Errorf("the Startup Protocol orders a read of %s without stating its size: %q", read.name, line)
		}
		if !dated.MatchString(line) {
			t.Errorf("the Startup Protocol's stated cost for %s carries no measurement date: %q", read.name, line)
			continue
		}
		if read.name == "USING_AGENTFACTORY.md" {
			assertStatedSizeMatchesFile(t, line, repoFile(t, "USING_AGENTFACTORY.md"))
		}
	}
}

// repoFile resolves a path relative to the module root, found by walking up for go.mod.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, rel)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s; cannot locate %s", dir, rel)
		}
		dir = parent
	}
}

// assertStatedSizeMatchesFile holds the FIRST byte figure on a line against the file's real size.
// First, because the template states the operative measurement and then carries the superseded one
// as dated provenance — checking the provenance figure would red the moment the file changed, which
// is the opposite of what it is there for.
//
// The figure is a budget the manager reads before a large document, not a byte-exact ledger (K10
// states it "drifts and `wc -c` is the truth"), so it need only agree within ±10% (#681 T5 / D4).
// Exact equality reds `make test` on any one-byte edit to the doc — the churn the reviewer flagged —
// while a ±10% band still fails loudly on material drift that would make the stated cost a lie.
func assertStatedSizeMatchesFile(t *testing.T, line, path string) {
	t.Helper()
	m := regexp.MustCompile(`(\d[\d,]*)\s*B\b`).FindStringSubmatch(line)
	if m == nil {
		t.Errorf("no byte figure on the line naming %s: %q", filepath.Base(path), line)
		return
	}
	stated, err := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
	if err != nil {
		t.Errorf("unparseable byte figure %q: %v", m[1], err)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !t4581113_withinUsingBand(stated, info.Size()) {
		t.Errorf("the manager template prices %s at %d B but it is %d B — outside the ±10%% band.\n"+
			"Update the figure and the token estimate ((bytes+3)/4) in "+
			"internal/templates/roles/manager.md.tmpl, then re-render the deployed "+
			".agentfactory/agents/*/CLAUDE.md corpus.", filepath.Base(path), stated, info.Size())
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

// TestRoles_MatchesEmbeddedRoster pins the accessor to the embed.FS it reads from, by deriving the
// expected roster independently rather than comparing Roles() to itself.
//
// This lives in the UNIT lane on purpose. Roles() exists for the per-role SessionStart sweep in
// internal/cmd's integration lane, which builds only under -tags=integration on the self-hosted
// runner — so an accessor that quietly started returning a short list would shrink that sweep to
// whatever it still saw, and nothing that runs on every PR would notice. HasRole is checked for the
// same reason: a name Roles() reports but RenderRole cannot render is worse than a name it omits,
// because a caller that installs everything listed fails partway through.
func TestRoles_MatchesEmbeddedRoster(t *testing.T) {
	entries, err := templateFS.ReadDir(roleTemplateDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", roleTemplateDir, err)
	}
	var want []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), roleTemplateExt) {
			continue
		}
		want = append(want, strings.TrimSuffix(e.Name(), roleTemplateExt))
	}
	if len(want) == 0 {
		t.Fatal("the embedded roles directory holds no templates; this guard would pass vacuously")
	}

	got := Roles()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Roles() = %v, want %v", got, want)
	}
	tmpl := New()
	for _, role := range got {
		if !tmpl.HasRole(role) {
			t.Errorf("Roles() reports %q, which HasRole does not recognise", role)
		}
	}
}

// TestAllRoleTemplates_RenderWithRoleDataContract renders every embedded role template against
// the RoleData contract, so a template referencing a field that does not exist fails here rather
// than at an agent's session start. This is a standing guard, not a behavioural assertion: it
// passes both before and after the catalog-anchor change.
func TestAllRoleTemplates_RenderWithRoleDataContract(t *testing.T) {
	roles := Roles()
	if len(roles) == 0 {
		t.Fatal("no role templates found -- this guard would pass vacuously")
	}
	root := t.TempDir()
	tmpl := New()
	for _, role := range roles {
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

// TestEveryRoleTemplateCarriesMemoryProtocol holds the uniformity clause of AC-626-5: the memory
// protocol reaches EVERY role, not the roles someone remembered.
//
// Enumerated by glob over the embedded FS rather than checked against a list of 43 names, because
// a roster pinned by name or by count is a roster that silently stops covering the next role
// added. A new template inherits this guarantee by existing, and if it was authored without the
// section, the first `make test` after it lands says so.
//
// Contains-the-whole-block rather than contains-the-heading: the heading alone would pass on a
// template that kept the title and rewrote the body, which is the drift most likely to happen and
// the one that matters — 43 roles reading 43 different versions of the same protocol is the state
// this test exists to prevent.
//
// Checked against MemoryProtocolSection rather than a copy of it, so this is not a text-vs-text
// tautology: the const is what the generator EMITS and the .md.tmpl files are what agents READ.
// Editing the const without regenerating leaves those two out of step, and that is exactly the
// state this reds on.
func TestEveryRoleTemplateCarriesMemoryProtocol(t *testing.T) {
	var checked int
	for _, role := range Roles() {
		name := role + roleTemplateExt
		data, err := templateFS.ReadFile(roleTemplateDir + "/" + name)
		if err != nil {
			t.Fatalf("read embedded template %s: %v", name, err)
		}
		checked++
		if !strings.Contains(string(data), MemoryProtocolSection) {
			t.Errorf("%s does not carry MemoryProtocolSection verbatim; regenerate it with "+
				"`af formula agent-gen`, or if it is a hand-authored built-in (manager, "+
				"supervisor), re-mirror the const into it byte for byte", name)
		}
	}

	// Without this the test passes loudly on an empty enumeration, which is the exact way a
	// glob-driven sweep dies. No larger floor: the roster size is a per-factory fact, not a contract.
	if checked == 0 {
		t.Fatal("swept zero role templates; the embed glob is not seeing the roster")
	}
}

// The section is rendered, not just stored, so the vault path an agent reads names that agent.
// A literal placeholder here would have been a fifth thing telling agents about a directory they
// then have to translate themselves.
func TestMemoryProtocolRendersTheAgentsOwnVaultPath(t *testing.T) {
	output, err := New().RenderRole("supervisor", RoleData{
		Role:        "supervisor",
		Description: "test",
		RootDir:     "/tmp/factory",
		WorkDir:     "/tmp/factory",
	})
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, ".agentfactory/memory/supervisor/") {
		t.Error("rendered Memory Protocol should name the agent's own vault path")
	}
	if strings.Contains(output, "{{ .Role }}") {
		t.Error("rendered template still contains an unexpanded {{ .Role }} action")
	}
}

// t4581113_abs returns the absolute value of an int64.
func t4581113_abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// t4581113_withinUsingBand encodes the D4 decision for T5: the stated USING size need only agree
// with the real file within a ±10% tolerance band. It is the contract the fix must implement in
// assertStatedSizeMatchesFile (replacing exact equality). Written here so the reject-direction teeth
// (T5-c) can be asserted without invoking the t-based helper, whose t.Fatalf/t.Errorf would fail
// this test's own *testing.T via t.Run propagation.
func t4581113_withinUsingBand(stated, actual int64) bool {
	return 10*t4581113_abs(stated-actual) <= actual
}

// TestStatedUSINGSize_ToleratesOneByteDrift (T5-b) is the failing-first RED for the D4 tolerance:
// a stated USING size that differs from the file by a single byte must be accepted. It drives the
// EXISTING assertStatedSizeMatchesFile through a subtest and reads that subtest's pass/fail: at head
// the helper does exact equality, so a +1 byte figure reds (and, via t.Run propagation, reds this
// test) — the predicted RED. The D4 fix (±10% band) makes the subtest pass and this test green.
func TestStatedUSINGSize_ToleratesOneByteDrift(t *testing.T) {
	path := repoFile(t, "USING_AGENTFACTORY.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	stated := info.Size() + 1
	line := fmt.Sprintf("Refresh factory knowledge — costs %d B ≈ 13.0k tokens as measured 2026-09-14", stated)

	passed := t.Run("driftByOneByte", func(st *testing.T) {
		assertStatedSizeMatchesFile(st, line, path)
	})
	if !passed {
		t.Errorf("a 1-byte drift (%d B stated vs %d B actual) must be tolerated under the D4 ±10%% band, "+
			"but assertStatedSizeMatchesFile rejected it — loosen the exact-equality check", stated, info.Size())
	}
}

// TestStatedUSINGSize_StillRejectsGrossDrift (T5-c) is the teeth for T5: the tolerance must not
// widen into a no-op. It pins the D4 contract — a figure off by an order of magnitude lies OUTSIDE
// the ±10% band while a 1-byte drift lies inside — so a fix that made the comparator always pass
// would contradict this. Green at head and after the fix: the band contract is invariant.
//
// It asserts the band arithmetic rather than driving the t-based helper's reject path, because a
// rejecting assertStatedSizeMatchesFile signals failure via the *testing.T it is handed, and t.Run
// propagates that to the parent — which would red this protective test at head.
func TestStatedUSINGSize_StillRejectsGrossDrift(t *testing.T) {
	path := repoFile(t, "USING_AGENTFACTORY.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	actual := info.Size()

	if t4581113_withinUsingBand(actual*10, actual) {
		t.Errorf("a stated size 10x the real file (%d B vs %d B) must fall outside the ±10%% band; "+
			"the tolerance has been widened into a no-op", actual*10, actual)
	}
	if !t4581113_withinUsingBand(actual+1, actual) {
		t.Errorf("a 1-byte drift (%d B vs %d B) must fall inside the ±10%% band", actual+1, actual)
	}
}

// TestManagerTemplate_StartupProtocolMailStep (T11-b) pins that the manager template's Startup
// Protocol mail step reconciles the same way as the generated agent step and prime.go's directive:
// it must reflect "act on the mail delivered at session start", not instruct a redundant `af mail
// inbox` go-fetch. At head manager.md.tmpl:86 reads "Check mail for pending instructions (`af mail
// inbox`)" — RED. The D7 reword flips it green.
func TestManagerTemplate_StartupProtocolMailStep(t *testing.T) {
	tmpl := New()
	output, err := tmpl.RenderRole("manager", RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	})
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	startup := startupProtocolSection(t, output)

	if !strings.Contains(strings.ToLower(startup), "mail delivered at session start") {
		t.Errorf("manager Startup Protocol does not reflect the reconciled 'act on the mail delivered at session start' model:\n%s", startup)
	}
	if strings.Contains(startup, "Check mail for pending instructions") {
		t.Errorf("manager Startup Protocol still instructs a redundant `af mail inbox` go-fetch ('Check mail for pending instructions'):\n%s", startup)
	}
}
