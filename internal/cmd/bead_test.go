package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/issuestore"
)

// Test that all bead subcommands are registered
func TestBeadCmd_SubcommandRegistration(t *testing.T) {
	subcommands := []string{"show", "create", "update", "list", "close", "dep"}
	for _, name := range subcommands {
		found := false
		for _, cmd := range beadCmd.Commands() {
			if cmd.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("bead subcommand %q not registered", name)
		}
	}
}

// Test that create requires --title and --type
func TestBeadCreate_RequiredFlags(t *testing.T) {
	for _, cmd := range beadCmd.Commands() {
		if cmd.Name() == "create" {
			titleFlag := cmd.Flags().Lookup("title")
			if titleFlag == nil {
				t.Error("create should have --title flag")
			}
			typeFlag := cmd.Flags().Lookup("type")
			if typeFlag == nil {
				t.Error("create should have --type flag")
			}
			return
		}
	}
	t.Error("create subcommand not found")
}

// Test that show requires exactly 1 arg
func TestBeadShow_RequiresID(t *testing.T) {
	for _, cmd := range beadCmd.Commands() {
		if cmd.Name() == "show" {
			if cmd.Args == nil {
				t.Error("show should validate args")
			}
			return
		}
	}
	t.Error("show subcommand not found")
}

// Test detectCreatingAgent with .agentfactory/agents/ layout
func TestDetectCreatingAgent(t *testing.T) {
	t.Setenv("AF_ROLE", "")
	// resolveAgentName requires a loadable agents.json to validate a
	// path-derived name (GitHub issue #89). Build a real factory fixture
	// with "manager" registered, then derive sub-test paths from it.
	factoryRoot, managerDir := setupFactoryFixture(t, "manager")
	nestedDir := filepath.Join(managerDir, "src")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	agentsDir := filepath.Join(afDir, "agents")

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"agent dir", managerDir, "manager"},
		{"nested dir", nestedDir, "manager"},
		{"factory root", factoryRoot, ""},
		{"config dir", filepath.Join(factoryRoot, "config"), ""},
		{"dot dir only", afDir, ""},
		{"agents dir only", agentsDir, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectCreatingAgent(tt.cwd, factoryRoot)
			if got != tt.want {
				t.Errorf("detectCreatingAgent(%q, %q) = %q, want %q",
					tt.cwd, factoryRoot, got, tt.want)
			}
		})
	}
}

// TestDetectCreatingAgent_WorktreeAgent verifies that detectCreatingAgent
// resolves the agent name when cwd is inside a worktree. Uses real temp dirs
// because the fix calls FindLocalRoot internally.
func TestDetectCreatingAgent_WorktreeAgent(t *testing.T) {
	factoryRoot := t.TempDir()

	// Set up factory config
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	// agents.json with "solver" so resolveAgentName can validate membership.
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"solver":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	// Set up worktree structure
	wtRoot := filepath.Join(afDir, "worktrees", "wt-test")
	wtAfDir := filepath.Join(wtRoot, ".agentfactory")
	wtAgentDir := filepath.Join(wtAfDir, "agents", "solver")
	os.MkdirAll(wtAgentDir, 0o755)
	os.WriteFile(filepath.Join(wtAfDir, ".factory-root"), []byte(factoryRoot), 0o644)

	got := detectCreatingAgent(wtAgentDir, factoryRoot)
	if got != "solver" {
		t.Errorf("detectCreatingAgent from worktree = %q, want %q", got, "solver")
	}
}

// TestDetectCreatingAgent_WrongButNoError_HonorsAF_ROLE pins the fix for
// GitHub issue #88 at the bead-create identity boundary. A cwd at a typo
// directory (exists on disk, not in agents.json) must not silently attach a
// wrong created-by: label to the bd record. AF_ROLE, set by session.Manager,
// must be honored.
func TestDetectCreatingAgent_WrongButNoError_HonorsAF_ROLE(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "manager")

	staleDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "stale")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "manager")

	got := detectCreatingAgent(staleDir, factoryRoot)
	if got != "manager" {
		t.Errorf("detectCreatingAgent = %q, want %q (AF_ROLE overrides wrong path-derived name)", got, "manager")
	}
}

// TestDetectCreatingAgent_WrongButNoError_NoAF_ROLE_ReturnsEmpty verifies
// that without AF_ROLE, a wrong path-derived name is not silently attached
// to beads. The empty return triggers bead.go:170's `if agentLabel != ""`
// guard, preventing a corrupt created-by: label.
func TestDetectCreatingAgent_WrongButNoError_NoAF_ROLE_ReturnsEmpty(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "manager")

	staleDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "stale")
	if err := os.MkdirAll(staleDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "")

	got := detectCreatingAgent(staleDir, factoryRoot)
	if got == "stale" {
		t.Errorf("detectCreatingAgent must not return wrong path-derived name %q — would corrupt bd created-by: label", got)
	}
	if got != "" {
		t.Errorf("detectCreatingAgent = %q, want \"\" (so bead.go:170 `if agentLabel != \"\"` guard prevents wrong label)", got)
	}
}

// TestDetectCreatingAgent_FactoryAgent_NoRegression verifies no regression
// for factory-root agents.
func TestDetectCreatingAgent_FactoryAgent_NoRegression(t *testing.T) {
	factoryRoot := t.TempDir()
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	agentDir := filepath.Join(afDir, "agents", "manager")
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	// agents.json with "manager" so resolveAgentName can validate membership.
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"manager":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	got := detectCreatingAgent(agentDir, factoryRoot)
	if got != "manager" {
		t.Errorf("detectCreatingAgent from factory = %q, want %q", got, "manager")
	}
}

// Test that update requires --notes
func TestBeadUpdate_RequiresNotes(t *testing.T) {
	for _, cmd := range beadCmd.Commands() {
		if cmd.Name() == "update" {
			notesFlag := cmd.Flags().Lookup("notes")
			if notesFlag == nil {
				t.Error("update should have --notes flag")
			}
			return
		}
	}
	t.Error("update subcommand not found")
}

// Test that dep requires exactly 2 args
func TestBeadDep_RequiresTwoArgs(t *testing.T) {
	for _, cmd := range beadCmd.Commands() {
		if cmd.Name() == "dep" {
			if cmd.Args == nil {
				t.Error("dep should validate args")
			}
			return
		}
	}
	t.Error("dep subcommand not found")
}

// Test that list has --all flag
func TestBeadList_HasAllFlag(t *testing.T) {
	for _, cmd := range beadCmd.Commands() {
		if cmd.Name() == "list" {
			allFlag := cmd.Flags().Lookup("all")
			if allFlag == nil {
				t.Error("list should have --all flag")
			}
			return
		}
	}
	t.Error("list subcommand not found")
}

// newBeadCreateCmd constructs an isolated cobra command whose flag set
// matches the production `af bead create` registration in bead.go. Tests
// use this instead of the package-global beadCmd so parallel tests cannot
// race on shared flag state.
func newBeadCreateCmd(t *testing.T) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "create", RunE: runBeadCreate}
	c.Flags().String("type", "", "")
	c.Flags().String("title", "", "")
	c.Flags().StringP("description", "d", "", "")
	c.Flags().String("priority", "", "")
	c.Flags().String("labels", "", "")
	c.Flags().String("parent", "", "")
	c.Flags().Bool("json", false, "")
	c.SetContext(context.Background())
	return c
}

// TestRunBeadCreate_ParentScoped_PopulatesAssignee pins the Commit 1
// requirement from IMPLREADME_PHASE1: when `af bead create --parent` is run
// from an agent workspace, the created bead's Assignee is auto-populated
// from detectCreatingAgent so the Phase 1 data-plane invariant
// (parent_id = '' OR assignee != '') is satisfied by construction.
func TestRunBeadCreate_ParentScoped_PopulatesAssignee(t *testing.T) {
	factoryRoot, agentDir := setupFactoryFixture(t, "alice")
	store := installMemStore(t)
	t.Setenv("AF_ROLE", "")
	t.Setenv("BD_ACTOR", "")

	ctx := context.Background()
	epic, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "parent epic",
		Assignee: "alice",
	})
	if err != nil {
		t.Fatalf("seed epic: %v", err)
	}

	t.Chdir(agentDir)
	_ = factoryRoot // fixture already rooted via t.Chdir

	cmd := newBeadCreateCmd(t)
	_ = cmd.Flags().Set("title", "child task")
	_ = cmd.Flags().Set("type", "task")
	_ = cmd.Flags().Set("parent", epic.ID)

	if err := runBeadCreate(cmd, nil); err != nil {
		t.Fatalf("runBeadCreate: %v", err)
	}

	children, err := store.List(ctx, issuestore.Filter{Parent: epic.ID, IncludeAllAgents: true})
	if err != nil {
		t.Fatalf("list children: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("child count = %d, want 1", len(children))
	}
	if children[0].Assignee != "alice" {
		t.Errorf("child.Assignee = %q, want %q", children[0].Assignee, "alice")
	}
}

// TestRunBeadCreate_ParentScoped_NoAgentContext_Errors pins the defensive
// boundary from IMPLREADME_PHASE1: `af bead create --parent` must error
// BEFORE store.Create is invoked when no agent identity is resolvable. This
// prevents an empty-Assignee parent-scoped bead from ever reaching the data
// plane, enforcing the invariant at the CLI layer.
func TestRunBeadCreate_ParentScoped_NoAgentContext_Errors(t *testing.T) {
	factoryRoot := t.TempDir()
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"someone":{"type":"autonomous","description":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	store := installMemStore(t)
	t.Setenv("AF_ROLE", "")
	t.Setenv("BD_ACTOR", "")
	t.Chdir(factoryRoot)

	cmd := newBeadCreateCmd(t)
	_ = cmd.Flags().Set("title", "orphan child")
	_ = cmd.Flags().Set("type", "task")
	_ = cmd.Flags().Set("parent", "epic-nonexistent")

	err := runBeadCreate(cmd, nil)
	if err == nil {
		t.Fatal("runBeadCreate: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "requires an assignable identity") {
		t.Errorf("error = %q, want substring %q", err.Error(), "requires an assignable identity")
	}

	all, listErr := store.List(context.Background(), issuestore.Filter{IncludeAllAgents: true, IncludeClosed: true})
	if listErr != nil {
		t.Fatalf("list after guarded error: %v", listErr)
	}
	if len(all) != 0 {
		t.Errorf("store bead count = %d, want 0 (store.Create must not be called after guard)", len(all))
	}
}
