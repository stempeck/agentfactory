package worktree

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// initGitRepo initializes a real git repo with an initial commit in dir.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Create initial commit (required for git worktree add)
	readmePath := filepath.Join(dir, "README")
	if err := os.WriteFile(readmePath, []byte("init"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"add", "README"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// setupFactoryRoot creates a factory root with .agentfactory/factory.json and agents.json.
func setupFactoryRoot(t *testing.T, dir string) {
	t.Helper()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	factoryJSON := `{"type":"factory","version":1,"name":"test"}`
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(factoryJSON), 0o644); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	agentsJSON := `{"agents":{"solver":{"type":"autonomous","description":"Solves problems"}}}`
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"), []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}
}

func TestGenerateID(t *testing.T) {
	id := GenerateID()
	if !strings.HasPrefix(id, "wt-") {
		t.Errorf("GenerateID() = %q, want prefix %q", id, "wt-")
	}
	// "wt-" + 6 hex chars = 9 total
	if len(id) != 9 {
		t.Errorf("GenerateID() length = %d, want 9", len(id))
	}
	// Hex characters only after prefix
	matched, _ := regexp.MatchString(`^wt-[0-9a-f]{6}$`, id)
	if !matched {
		t.Errorf("GenerateID() = %q, does not match wt-[0-9a-f]{6}", id)
	}
	// Two calls produce different IDs
	id2 := GenerateID()
	if id == id2 {
		t.Errorf("two GenerateID() calls returned same value: %q", id)
	}
}

func TestBranchName(t *testing.T) {
	got := BranchName("solver", "wt-abc123")
	want := "af/solver-abc123"
	if got != want {
		t.Errorf("BranchName(\"solver\", \"wt-abc123\") = %q, want %q", got, want)
	}
}

func TestWorktreesDir(t *testing.T) {
	got := WorktreesDir("/factory")
	want := filepath.Join("/factory", ".agentfactory", "worktrees")
	if got != want {
		t.Errorf("WorktreesDir(\"/factory\") = %q, want %q", got, want)
	}
}

func TestMetaJSONRoundTrip(t *testing.T) {
	original := Meta{
		ID:           "wt-abc123",
		Owner:        "solver",
		Branch:       "af/solver-abc123",
		Path:         ".agentfactory/worktrees/wt-abc123",
		Agents:       []string{"solver", "reviewer"},
		CreatedAt:    "2026-04-11T12:00:00Z",
		ParentBranch: "main",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Meta
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.ID != original.ID {
		t.Errorf("ID: got %q, want %q", decoded.ID, original.ID)
	}
	if decoded.Owner != original.Owner {
		t.Errorf("Owner: got %q, want %q", decoded.Owner, original.Owner)
	}
	if decoded.Branch != original.Branch {
		t.Errorf("Branch: got %q, want %q", decoded.Branch, original.Branch)
	}
	if decoded.Path != original.Path {
		t.Errorf("Path: got %q, want %q", decoded.Path, original.Path)
	}
	if len(decoded.Agents) != len(original.Agents) {
		t.Errorf("Agents length: got %d, want %d", len(decoded.Agents), len(original.Agents))
	}
	if decoded.CreatedAt != original.CreatedAt {
		t.Errorf("CreatedAt: got %q, want %q", decoded.CreatedAt, original.CreatedAt)
	}
	if decoded.ParentBranch != original.ParentBranch {
		t.Errorf("ParentBranch: got %q, want %q", decoded.ParentBranch, original.ParentBranch)
	}
}

func TestWriteMetaReadMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	original := &Meta{
		ID:           "wt-aabbcc",
		Owner:        "solver",
		Branch:       "af/solver-aabbcc",
		Path:         ".agentfactory/worktrees/wt-aabbcc",
		Agents:       []string{"solver"},
		CreatedAt:    "2026-04-11T12:00:00Z",
		ParentBranch: "main",
	}

	if err := WriteMeta(realDir, original); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	got, err := ReadMeta(realDir, "wt-aabbcc")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}

	if got.ID != original.ID {
		t.Errorf("ID: got %q, want %q", got.ID, original.ID)
	}
	if got.Owner != original.Owner {
		t.Errorf("Owner: got %q, want %q", got.Owner, original.Owner)
	}
	if got.Branch != original.Branch {
		t.Errorf("Branch: got %q, want %q", got.Branch, original.Branch)
	}
	if got.Path != original.Path {
		t.Errorf("Path: got %q, want %q", got.Path, original.Path)
	}
	if len(got.Agents) != 1 || got.Agents[0] != "solver" {
		t.Errorf("Agents: got %v, want [solver]", got.Agents)
	}
}

func TestCreate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	absPath, meta, err := Create(realDir, "solver")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify worktree directory exists
	if _, err := os.Stat(absPath); err != nil {
		t.Errorf("worktree dir does not exist: %v", err)
	}

	// Verify .factory-root redirect file
	redirectPath := filepath.Join(absPath, ".agentfactory", ".factory-root")
	redirectData, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("reading redirect file: %v", err)
	}
	redirectTarget := strings.TrimSpace(string(redirectData))
	if redirectTarget != realDir {
		t.Errorf("redirect file: got %q, want %q", redirectTarget, realDir)
	}

	// Verify .agentfactory/agents/ directory exists
	agentsDir := filepath.Join(absPath, ".agentfactory", "agents")
	if _, err := os.Stat(agentsDir); err != nil {
		t.Errorf("agents dir does not exist: %v", err)
	}

	// Verify meta file written
	metaFromDisk, err := ReadMeta(realDir, meta.ID)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if metaFromDisk.Owner != "solver" {
		t.Errorf("meta.Owner: got %q, want %q", metaFromDisk.Owner, "solver")
	}
	if !strings.HasPrefix(metaFromDisk.Branch, "af/solver-") {
		t.Errorf("meta.Branch: got %q, want prefix %q", metaFromDisk.Branch, "af/solver-")
	}

	// Verify Meta.Path is relative
	if filepath.IsAbs(meta.Path) {
		t.Errorf("meta.Path is absolute: %q, should be relative", meta.Path)
	}
	if !strings.HasPrefix(meta.Path, ".agentfactory/worktrees/") {
		t.Errorf("meta.Path: got %q, want prefix %q", meta.Path, ".agentfactory/worktrees/")
	}

	// Verify FindFactoryRoot from worktree context follows redirect
	factoryRoot, err := config.FindFactoryRoot(absPath)
	if err != nil {
		t.Fatalf("FindFactoryRoot from worktree: %v", err)
	}
	if factoryRoot != realDir {
		t.Errorf("FindFactoryRoot from worktree: got %q, want %q", factoryRoot, realDir)
	}

	// Verify FindLocalRoot from worktree returns the worktree root (nearest .agentfactory/)
	localRoot, err := config.FindLocalRoot(absPath)
	if err != nil {
		t.Fatalf("FindLocalRoot from worktree: %v", err)
	}
	if localRoot != absPath {
		t.Errorf("FindLocalRoot from worktree: got %q, want %q", localRoot, absPath)
	}
}

func TestSetupAgent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	// Create a worktree first
	absPath, _, err := Create(realDir, "solver")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// SetupAgent
	agentDir, err := SetupAgent(realDir, absPath, "solver", true)
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}

	// Verify CLAUDE.md exists and was rendered (contains worktree path)
	claudeMD := filepath.Join(agentDir, "CLAUDE.md")
	claudeData, err := os.ReadFile(claudeMD)
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if len(claudeData) == 0 {
		t.Error("CLAUDE.md is empty")
	}
	// The rendered CLAUDE.md should contain the worktree path (RootDir=worktreePath)
	if !strings.Contains(string(claudeData), absPath) {
		t.Errorf("CLAUDE.md does not contain worktree path %q — was it rendered or copied?", absPath)
	}

	// Verify settings.json exists
	settingsPath := filepath.Join(agentDir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Errorf("settings.json does not exist: %v", err)
	}

	// Verify .runtime directory
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if _, err := os.Stat(runtimeDir); err != nil {
		t.Errorf(".runtime dir does not exist: %v", err)
	}

	// Verify worktree_id file
	wtIDData, err := os.ReadFile(filepath.Join(runtimeDir, "worktree_id"))
	if err != nil {
		t.Fatalf("reading worktree_id: %v", err)
	}
	wtID := strings.TrimSpace(string(wtIDData))
	if !strings.HasPrefix(wtID, "wt-") {
		t.Errorf("worktree_id: got %q, want prefix %q", wtID, "wt-")
	}

	// Verify worktree_owner file (isOwner=true)
	ownerData, err := os.ReadFile(filepath.Join(runtimeDir, "worktree_owner"))
	if err != nil {
		t.Fatalf("reading worktree_owner: %v", err)
	}
	if strings.TrimSpace(string(ownerData)) != "true" {
		t.Errorf("worktree_owner: got %q, want %q", strings.TrimSpace(string(ownerData)), "true")
	}
}

func TestSetupAgent_NotOwner(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	absPath, _, err := Create(realDir, "solver")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	agentDir, err := SetupAgent(realDir, absPath, "solver", false)
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}

	// worktree_owner should NOT exist when isOwner=false
	ownerPath := filepath.Join(agentDir, ".runtime", "worktree_owner")
	if _, err := os.Stat(ownerPath); !os.IsNotExist(err) {
		t.Errorf("worktree_owner should not exist when isOwner=false")
	}
}

func TestRemoveAgent(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// Write a meta with two agents
	meta := &Meta{
		ID:     "wt-test01",
		Owner:  "solver",
		Branch: "af/solver-test01",
		Path:   ".agentfactory/worktrees/wt-test01",
		Agents: []string{"solver", "reviewer"},
	}
	if err := WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	// Remove "solver" — should leave "reviewer", not empty
	updated, empty, err := RemoveAgent(realDir, "wt-test01", "solver")
	if err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	if empty {
		t.Error("expected empty=false, got true")
	}
	if len(updated.Agents) != 1 || updated.Agents[0] != "reviewer" {
		t.Errorf("Agents: got %v, want [reviewer]", updated.Agents)
	}

	// Remove "reviewer" — should be empty now
	updated2, empty2, err := RemoveAgent(realDir, "wt-test01", "reviewer")
	if err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	if !empty2 {
		t.Error("expected empty=true, got false")
	}
	if len(updated2.Agents) != 0 {
		t.Errorf("Agents: got %v, want []", updated2.Agents)
	}
}

func TestFindByOwner_Found(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	meta := &Meta{
		ID:    "wt-found1",
		Owner: "solver",
	}
	if err := WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	got, err := FindByOwner(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if got == nil {
		t.Fatal("FindByOwner returned nil, want non-nil")
	}
	if got.ID != "wt-found1" {
		t.Errorf("ID: got %q, want %q", got.ID, "wt-found1")
	}
}

func TestRemove_FullLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	// Add .agentfactory to .gitignore so worktree setup files don't block removal.
	// This matches production usage where .agentfactory/ is gitignored.
	gitignorePath := filepath.Join(realDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "add gitignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Create a worktree
	absPath, meta, err := Create(realDir, "solver")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify worktree exists
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("worktree dir should exist after Create: %v", err)
	}

	// Verify meta file exists
	metaFromDisk, err := ReadMeta(realDir, meta.ID)
	if err != nil {
		t.Fatalf("ReadMeta should work after Create: %v", err)
	}
	if metaFromDisk.Owner != "solver" {
		t.Errorf("meta.Owner: got %q, want %q", metaFromDisk.Owner, "solver")
	}

	// Verify branch exists
	branchCheck := exec.Command("git", "branch", "--list", meta.Branch)
	branchCheck.Dir = realDir
	branchOut, err := branchCheck.Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if !strings.Contains(string(branchOut), meta.Branch) {
		t.Fatalf("branch %q should exist after Create", meta.Branch)
	}

	// Remove the worktree
	if err := Remove(realDir, meta); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify worktree directory is gone
	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should not exist after Remove, got err: %v", err)
	}

	// Verify meta file is gone
	_, readErr := ReadMeta(realDir, meta.ID)
	if readErr == nil {
		t.Error("ReadMeta should fail after Remove (meta file deleted)")
	}

	// Verify branch is gone
	branchCheck2 := exec.Command("git", "branch", "--list", meta.Branch)
	branchCheck2.Dir = realDir
	branchOut2, err := branchCheck2.Output()
	if err != nil {
		t.Fatalf("git branch --list after Remove: %v", err)
	}
	if strings.Contains(string(branchOut2), meta.Branch) {
		t.Errorf("branch %q should not exist after Remove", meta.Branch)
	}
}

func TestFindByOwner_NotFound(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// Create worktrees dir but no meta files
	if err := os.MkdirAll(WorktreesDir(realDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := FindByOwner(realDir, "nonexistent")
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if got != nil {
		t.Errorf("FindByOwner returned %+v, want nil", got)
	}
}

// --- Edge-case unit tests ---

func TestRemoveAgent_DuplicateAgent(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	meta := &Meta{
		ID:     "wt-dup001",
		Owner:  "solver",
		Branch: "af/solver-dup001",
		Path:   ".agentfactory/worktrees/wt-dup001",
		Agents: []string{"solver", "solver", "reviewer"},
	}
	if err := WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	updated, empty, err := RemoveAgent(realDir, "wt-dup001", "solver")
	if err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	if empty {
		t.Error("expected empty=false, got true")
	}
	if len(updated.Agents) != 1 || updated.Agents[0] != "reviewer" {
		t.Errorf("Agents: got %v, want [reviewer]", updated.Agents)
	}
}

func TestRemoveAgent_EmptyAgentsList(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	meta := &Meta{
		ID:     "wt-empty1",
		Owner:  "solver",
		Branch: "af/solver-empty1",
		Path:   ".agentfactory/worktrees/wt-empty1",
		Agents: []string{},
	}
	if err := WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	updated, empty, err := RemoveAgent(realDir, "wt-empty1", "solver")
	if err != nil {
		t.Fatalf("RemoveAgent: %v", err)
	}
	if !empty {
		t.Error("expected empty=true, got false")
	}
	if len(updated.Agents) != 0 {
		t.Errorf("Agents: got %v, want []", updated.Agents)
	}
}

func TestReadMeta_MissingFile(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// Ensure worktrees dir exists but no meta file
	if err := os.MkdirAll(WorktreesDir(realDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err = ReadMeta(realDir, "nonexistent-id")
	if err == nil {
		t.Fatal("ReadMeta should return error for missing file")
	}
}

func TestReadMeta_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// Create worktrees dir and write invalid JSON to a meta file
	wtDir := WorktreesDir(realDir)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	corruptPath := filepath.Join(wtDir, "wt-corrupt.meta.json")
	if err := os.WriteFile(corruptPath, []byte("not valid json {{{"), 0o644); err != nil {
		t.Fatalf("write corrupt meta: %v", err)
	}

	_, err = ReadMeta(realDir, "wt-corrupt")
	if err == nil {
		t.Fatal("ReadMeta should return error for corrupt JSON")
	}
}

func TestCreate_WorktreeDirAlreadyExists(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	// Pre-create a directory at a known worktree path with content.
	// GenerateID produces random IDs so we can't predict Create's target,
	// but we can test the underlying git behavior directly using our own
	// BranchName and WorktreesDir helpers (same package).
	conflictID := "wt-aaaaaa"
	conflictBranch := BranchName("solver", conflictID)
	conflictPath := filepath.Join(WorktreesDir(realDir), conflictID)
	if err := os.MkdirAll(conflictPath, 0o755); err != nil {
		t.Fatalf("mkdir conflict path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(conflictPath, "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	// git worktree add fails when the target directory already exists with content
	cmd := exec.Command("git", "worktree", "add", "--quiet", "-b", conflictBranch, conflictPath)
	cmd.Dir = realDir
	_, err = cmd.CombinedOutput()
	if err == nil {
		// Clean up if it somehow succeeded
		exec.Command("git", "worktree", "remove", "--force", conflictPath).Run()
		exec.Command("git", "branch", "-D", conflictBranch).Run()
		t.Fatal("expected git worktree add to fail when target directory already exists with content")
	}
}

func TestResolveOrCreate_EnvBranch(t *testing.T) {
	path, id, created, err := ResolveOrCreate("/unused/root", "solver", "", "/some/worktree", "wt-abc123")
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if path != "/some/worktree" {
		t.Errorf("path: got %q, want %q", path, "/some/worktree")
	}
	if id != "wt-abc123" {
		t.Errorf("id: got %q, want %q", id, "wt-abc123")
	}
	if created {
		t.Error("created: got true, want false")
	}
}

func TestResolveOrCreate_DiskFallback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	wtPath, meta, err := Create(realDir, "manager")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	path, id, created, err := ResolveOrCreate(realDir, "solver", "manager", "", "")
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if path != wtPath {
		t.Errorf("path: got %q, want %q", path, wtPath)
	}
	if id != meta.ID {
		t.Errorf("id: got %q, want %q", id, meta.ID)
	}
	if created {
		t.Error("created: got true, want false")
	}
}

func TestResolveOrCreate_CreatesWhenNoContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	path, id, created, err := ResolveOrCreate(realDir, "solver", "", "", "")
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if !created {
		t.Error("created: got false, want true")
	}
	if !strings.HasPrefix(id, "wt-") {
		t.Errorf("id: got %q, want wt- prefix", id)
	}
	if path == "" {
		t.Error("path is empty")
	}
	foundMeta, err := FindByOwner(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByOwner: %v", err)
	}
	if foundMeta == nil {
		t.Fatal("FindByOwner returned nil after ResolveOrCreate created a worktree")
	}
	if foundMeta.ID != id {
		t.Errorf("FindByOwner meta.ID: got %q, want %q", foundMeta.ID, id)
	}
}

func TestResolveOrCreate_SelfAdoption(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	wtPath, meta, err := Create(realDir, "manager")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	path, id, created, err := ResolveOrCreate(realDir, "manager", "", "", "")
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if created {
		t.Error("created: got true, want false (self-adoption)")
	}
	if path != wtPath {
		t.Errorf("path: got %q, want %q", path, wtPath)
	}
	if id != meta.ID {
		t.Errorf("id: got %q, want %q", id, meta.ID)
	}

	entries, err := os.ReadDir(WorktreesDir(realDir))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	metaCount := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			metaCount++
		}
	}
	if metaCount != 1 {
		t.Errorf("meta files: got %d, want 1 (no duplicate)", metaCount)
	}
}

func TestForceRemove_FullLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	gitignorePath := filepath.Join(realDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "add gitignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	absPath, meta, err := Create(realDir, "solver")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("worktree dir should exist after Create: %v", err)
	}

	if err := ForceRemove(realDir, meta); err != nil {
		t.Fatalf("ForceRemove: %v", err)
	}

	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should not exist after ForceRemove, got err: %v", err)
	}

	_, readErr := ReadMeta(realDir, meta.ID)
	if readErr == nil {
		t.Error("ReadMeta should fail after ForceRemove (meta file deleted)")
	}

	branchCheck := exec.Command("git", "branch", "--list", meta.Branch)
	branchCheck.Dir = realDir
	branchOut, err := branchCheck.Output()
	if err != nil {
		t.Fatalf("git branch --list after ForceRemove: %v", err)
	}
	if strings.Contains(string(branchOut), meta.Branch) {
		t.Errorf("branch %q should not exist after ForceRemove", meta.Branch)
	}
}

func TestForceRemove_DirtyWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	gitignorePath := filepath.Join(realDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "add gitignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	absPath, meta, err := Create(realDir, "solver")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Create dirty state: write an uncommitted file in the worktree
	dirtyFile := filepath.Join(absPath, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted changes"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	// Stage it to make the worktree truly dirty (staged but not committed)
	stageCmd := exec.Command("git", "add", "dirty.txt")
	stageCmd.Dir = absPath
	if out, err := stageCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add dirty.txt: %v\n%s", err, out)
	}

	// Verify that non-force Remove would fail on this dirty worktree
	removeErr := Remove(realDir, meta)
	if removeErr == nil {
		t.Fatal("Remove should fail on dirty worktree, but it succeeded")
	}

	// Re-read meta since Remove might have partially modified state
	meta, err = ReadMeta(realDir, meta.ID)
	if err != nil {
		t.Fatalf("ReadMeta after failed Remove: %v", err)
	}

	// ForceRemove should succeed on dirty worktree
	if err := ForceRemove(realDir, meta); err != nil {
		t.Fatalf("ForceRemove should succeed on dirty worktree: %v", err)
	}

	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should not exist after ForceRemove, got err: %v", err)
	}

	_, readErr := ReadMeta(realDir, meta.ID)
	if readErr == nil {
		t.Error("ReadMeta should fail after ForceRemove (meta file deleted)")
	}

	branchCheck := exec.Command("git", "branch", "--list", meta.Branch)
	branchCheck.Dir = realDir
	branchOut, err := branchCheck.Output()
	if err != nil {
		t.Fatalf("git branch --list after ForceRemove: %v", err)
	}
	if strings.Contains(string(branchOut), meta.Branch) {
		t.Errorf("branch %q should not exist after ForceRemove", meta.Branch)
	}
}

func TestFindByAgent_FoundInAgents(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	meta := &Meta{
		ID:     "wt-agent1",
		Owner:  "manager",
		Agents: []string{"manager", "solver"},
	}
	if err := WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	got, err := FindByAgent(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByAgent: %v", err)
	}
	if got == nil {
		t.Fatal("FindByAgent returned nil, want non-nil")
	}
	if got.ID != "wt-agent1" {
		t.Errorf("ID: got %q, want %q", got.ID, "wt-agent1")
	}
	if got.Owner != "manager" {
		t.Errorf("Owner: got %q, want %q", got.Owner, "manager")
	}
}

func TestFindByAgent_FoundAsOwner(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	meta := &Meta{
		ID:     "wt-owner1",
		Owner:  "solver",
		Agents: []string{},
	}
	if err := WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	got, err := FindByAgent(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByAgent: %v", err)
	}
	if got == nil {
		t.Fatal("FindByAgent returned nil, want non-nil (Owner fallback)")
	}
	if got.ID != "wt-owner1" {
		t.Errorf("ID: got %q, want %q", got.ID, "wt-owner1")
	}
}

func TestFindByAgent_NotFound(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	meta := &Meta{
		ID:     "wt-other1",
		Owner:  "manager",
		Agents: []string{"manager", "reviewer"},
	}
	if err := WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	got, err := FindByAgent(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByAgent: %v", err)
	}
	if got != nil {
		t.Errorf("FindByAgent returned %+v, want nil", got)
	}
}

func TestFindByAgent_NoWorktreesDir(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	got, err := FindByAgent(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByAgent: %v", err)
	}
	if got != nil {
		t.Errorf("FindByAgent returned %+v, want nil (no worktrees dir)", got)
	}
}
