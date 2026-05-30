//go:build !integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupGitRepoWithStagedFiles creates a temp git repo, creates the given files,
// and stages them. Returns the repo directory.
func setupGitRepoWithStagedFiles(t *testing.T, files []string) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cmd %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "test")

	placeholder := filepath.Join(dir, ".gitkeep")
	os.WriteFile(placeholder, []byte(""), 0644)
	run("git", "add", ".gitkeep")
	run("git", "commit", "-m", "init")

	for _, f := range files {
		full := filepath.Join(dir, f)
		os.MkdirAll(filepath.Dir(full), 0755)
		os.WriteFile(full, []byte("test content"), 0644)
		run("git", "add", "-f", f)
	}

	return dir
}

func runProtectAgentScaffoldHook(t *testing.T, repoDir string, afRole string) (int, string) {
	t.Helper()
	repoRoot := findRepoRoot(t)
	hookPath := filepath.Join(repoRoot, "hooks", "protect-agent-scaffold.sh")

	cmd := exec.Command("bash", hookPath)
	cmd.Dir = repoDir
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "AF_ROLE=") {
			filtered = append(filtered, e)
		}
	}
	if afRole != "" {
		filtered = append(filtered, "AF_ROLE="+afRole)
	}
	cmd.Env = filtered

	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	return exitCode, string(out)
}

// Scenario: Hook blocks agent todos directory artifacts
func TestProtectAgentScaffold_BlocksTodosArtifacts(t *testing.T) {
	dir := setupGitRepoWithStagedFiles(t, []string{
		".agentfactory/agents/rootcause-all/todos/rootcause_analysis.md",
	})
	exitCode, output := runProtectAgentScaffoldHook(t, dir, "rootcause-all")
	if exitCode == 0 {
		t.Fatalf("expected hook to reject commit, but it passed.\nOutput: %s", output)
	}
	if !strings.Contains(output, "rootcause_analysis.md") {
		t.Errorf("error output should mention the blocked file, got: %s", output)
	}
}

// Scenario: Hook blocks any non-whitelisted agent file
func TestProtectAgentScaffold_BlocksNonWhitelistedAgentFile(t *testing.T) {
	dir := setupGitRepoWithStagedFiles(t, []string{
		".agentfactory/agents/designer/output/design.md",
	})
	exitCode, output := runProtectAgentScaffoldHook(t, dir, "designer")
	if exitCode == 0 {
		t.Fatalf("expected hook to reject commit, but it passed.\nOutput: %s", output)
	}
	if !strings.Contains(output, "design.md") {
		t.Errorf("error output should mention the blocked file, got: %s", output)
	}
}

// Scenario: Hook allows whitelisted CLAUDE.md through
// Note: The existing hook behavior BLOCKS CLAUDE.md (scaffold owned by agent-gen).
// The broadened hook maintains this: agents must not modify scaffold OR artifacts.
func TestProtectAgentScaffold_AllowsWhitelistedCLAUDEMD(t *testing.T) {
	dir := setupGitRepoWithStagedFiles(t, []string{
		".agentfactory/agents/rootcause-all/CLAUDE.md",
	})
	exitCode, _ := runProtectAgentScaffoldHook(t, dir, "rootcause-all")
	if exitCode == 0 {
		t.Fatalf("expected hook to block CLAUDE.md (agent-gen scaffold)")
	}
}

// Scenario: Hook allows whitelisted settings.json through
// Note: Same as above — scaffold files are blocked when agents try to modify them.
func TestProtectAgentScaffold_AllowsWhitelistedSettingsJSON(t *testing.T) {
	dir := setupGitRepoWithStagedFiles(t, []string{
		".agentfactory/agents/rootcause-all/.claude/settings.json",
	})
	exitCode, _ := runProtectAgentScaffoldHook(t, dir, "rootcause-all")
	if exitCode == 0 {
		t.Fatalf("expected hook to block .claude/settings.json (agent-gen scaffold)")
	}
}

// Scenario: Hook allows top-level config JSONs through
// Note: Top-level config JSONs (agents.json, dispatch.json, etc.) are also blocked.
func TestProtectAgentScaffold_AllowsTopLevelConfigJSONs(t *testing.T) {
	dir := setupGitRepoWithStagedFiles(t, []string{
		".agentfactory/agents.json",
	})
	exitCode, _ := runProtectAgentScaffoldHook(t, dir, "some-agent")
	if exitCode == 0 {
		t.Fatalf("expected hook to block agents.json (config owned by agent-gen)")
	}
}

// Scenario: Hook inactive when AF_ROLE is not set
func TestProtectAgentScaffold_InactiveWithoutAFRole(t *testing.T) {
	dir := setupGitRepoWithStagedFiles(t, []string{
		".agentfactory/agents/rootcause-all/todos/rootcause_analysis.md",
	})
	exitCode, output := runProtectAgentScaffoldHook(t, dir, "")
	if exitCode != 0 {
		t.Fatalf("expected hook to be inactive without AF_ROLE, but it rejected.\nOutput: %s", output)
	}
}

// Scenario: Hook allows designs directory commits
func TestProtectAgentScaffold_AllowsDesignsDirectory(t *testing.T) {
	dir := setupGitRepoWithStagedFiles(t, []string{
		".designs/224/design-doc.md",
	})
	exitCode, output := runProtectAgentScaffoldHook(t, dir, "designer")
	if exitCode != 0 {
		t.Fatalf("expected hook to allow .designs/ files, but it rejected.\nOutput: %s", output)
	}
}

// Scenario: Hook blocks deeply nested agent artifacts
func TestProtectAgentScaffold_BlocksDeeplyNestedArtifacts(t *testing.T) {
	dir := setupGitRepoWithStagedFiles(t, []string{
		".agentfactory/agents/ultra-impl/todos/ultra-implement/COMPLETION.md",
	})
	exitCode, output := runProtectAgentScaffoldHook(t, dir, "ultra-impl")
	if exitCode == 0 {
		t.Fatalf("expected hook to reject deeply nested artifact, but it passed.\nOutput: %s", output)
	}
}
