package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/worktree"
)

// t18TrackedScaffoldRepo makes root a git repository whose agent CLAUDE.md files are tracked, as this
// repository's own .gitignore allowlist keeps them. initTestGitRepo ignores all of .agentfactory/, so its
// worktrees stay clean and a non-forced `git worktree remove` succeeds; here worktree setup's re-render
// of a tracked CLAUDE.md dirties the worktree, which is what a refused launch really leaves behind.
// It runs `git init` itself, so it also converts a repository initTestGitRepo already made.
func t18TrackedScaffoldRepo(t *testing.T, root string, agents ...string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitignore := strings.Join([]string{
		".agentfactory/*",
		"!.agentfactory/agents/",
		".agentfactory/agents/**",
		"!.agentfactory/agents/*/",
		"!.agentfactory/agents/*/.claude/",
		"!.agentfactory/agents/*/.claude/settings.json",
		"!.agentfactory/agents/*/CLAUDE.md",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}
	add := []string{"add", ".gitignore"}
	for _, a := range agents {
		dir := filepath.Join(root, ".agentfactory", "agents", a)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# committed scaffold of "+a+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		add = append(add, filepath.Join(".agentfactory", "agents", a, "CLAUDE.md"))
	}
	gitTestCmd(t, root, "init", "-q")
	gitTestCmd(t, root, "config", "user.email", "test@test.com")
	gitTestCmd(t, root, "config", "user.name", "Test")
	gitTestCmd(t, root, add...)
	gitTestCmd(t, root, "commit", "-q", "-m", "tracked agent scaffold")
	tracked := t18Git(t, root, "ls-files", "--", ".agentfactory/agents")
	for _, a := range agents {
		if !strings.Contains(tracked, ".agentfactory/agents/"+a+"/CLAUDE.md") {
			t.Fatalf("fixture: %s's CLAUDE.md is not tracked; ls-files:\n%s", a, tracked)
		}
	}
}

func t18Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// t18CreateAgentWorktree creates and sets up a worktree for agent as `af up` does for an agent with none.
func t18CreateAgentWorktree(t *testing.T, root, agent string) (string, string) {
	t.Helper()
	wtPath, wtID, outcome, err := worktree.ResolveOrCreate(root, agent, "", "", "", worktree.CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate(%s): %v", agent, err)
	}
	if !outcome.IsCreated() {
		t.Fatalf("fixture: ResolveOrCreate(%s) = %s, want Created", agent, outcome)
	}
	if _, err := worktree.SetupAgent(root, wtPath, agent, true); err != nil {
		t.Fatalf("SetupAgent(%s): %v", agent, err)
	}
	return wtPath, wtID
}

func t18RequireDirtyScaffold(t *testing.T, wtPath, agent string) {
	t.Helper()
	status := t18Git(t, wtPath, "status", "--porcelain")
	if want := " M .agentfactory/agents/" + agent + "/CLAUDE.md"; !strings.Contains(status, want) {
		t.Fatalf("fixture: worktree setup left the tracked CLAUDE.md unmodified, so a plain remove would succeed and prove nothing; want %q in:\n%s", want, status)
	}
}

func t18RequireNoWorktree(t *testing.T, root, agent, when string) {
	t.Helper()
	entries, _ := os.ReadDir(worktree.WorktreesDir(root))
	var left []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "wt-") {
			left = append(left, e.Name())
		}
	}
	if len(left) != 0 {
		t.Errorf("%s: the refused worktree is still on disk: %v", when, left)
	}
	if list := t18Git(t, root, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Errorf("%s: git still registers a linked worktree:\n%s", when, list)
	}
	if branches := strings.TrimSpace(t18Git(t, root, "branch", "--list", "af/"+agent+"-*")); branches != "" {
		t.Errorf("%s: the refused worktree's branch is left: %s", when, branches)
	}
}

func TestRemoveRefusedWorktree_TakesBackADirtyCreatedWorktree(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	t18TrackedScaffoldRepo(t, root, "manager")
	wtPath, wtID := t18CreateAgentWorktree(t, root, "manager")
	t18RequireDirtyScaffold(t, wtPath, "manager")

	c, _, errOut := wgCmd(t)
	removeRefusedWorktree(c, root, wtID, "manager")

	if strings.Contains(errOut.String(), "warning:") {
		t.Errorf("taking back a worktree this run created warned instead of removing it:\n%s", errOut)
	}
	t18RequireNoWorktree(t, root, "manager", "after removeRefusedWorktree")
}

func TestRemoveRefusedWorktree_LeavesACoTenantsWorktree(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	t18TrackedScaffoldRepo(t, root, "manager", "supervisor")
	wtPath, wtID := t18CreateAgentWorktree(t, root, "supervisor")
	if _, err := worktree.SetupAgent(root, wtPath, "manager", false); err != nil {
		t.Fatalf("SetupAgent(manager): %v", err)
	}
	meta, err := worktree.ReadMeta(root, wtID)
	if err != nil {
		t.Fatal(err)
	}
	meta.Agents = append(meta.Agents, "manager")
	if err := worktree.WriteMeta(root, meta); err != nil {
		t.Fatal(err)
	}
	t18RequireDirtyScaffold(t, wtPath, "supervisor")

	c, _, errOut := wgCmd(t)
	removeRefusedWorktree(c, root, wtID, "manager")

	if errOut.Len() != 0 {
		t.Errorf("deregistering one co-tenant printed:\n%s", errOut)
	}
	after, err := worktree.ReadMeta(root, wtID)
	if err != nil {
		t.Fatalf("the co-tenant's worktree meta is gone: %v", err)
	}
	if !reflect.DeepEqual(after.Agents, []string{"supervisor"}) {
		t.Errorf("meta agents after deregistering manager = %v, want [supervisor]", after.Agents)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Errorf("the co-tenant's worktree directory is gone: %v", err)
	}
	if branches := strings.TrimSpace(t18Git(t, root, "branch", "--list", meta.Branch)); branches == "" {
		t.Errorf("the co-tenant's branch %s is gone", meta.Branch)
	}
	if status := t18Git(t, wtPath, "status", "--porcelain"); !strings.Contains(status, " M .agentfactory/agents/supervisor/CLAUDE.md") {
		t.Errorf("the co-tenant's uncommitted change is gone; status:\n%s", status)
	}
}
