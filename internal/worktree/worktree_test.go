package worktree

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
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

	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
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
	// K9a (#519 Phase 3): the factory root that created the worktree is durable
	// provenance in meta.json.
	if meta.FactoryRoot != realDir {
		t.Errorf("meta.FactoryRoot: got %q, want %q", meta.FactoryRoot, realDir)
	}
	if metaFromDisk.FactoryRoot != realDir {
		t.Errorf("on-disk meta.FactoryRoot: got %q, want %q", metaFromDisk.FactoryRoot, realDir)
	}
	if !strings.HasPrefix(metaFromDisk.Branch, "af/solver-") {
		t.Errorf("meta.Branch: got %q, want prefix %q", metaFromDisk.Branch, "af/solver-")
	}

	// Meta.Path for a freshly-Created worktree is relative to the factory root.
	// (Issue #392 K1/F-2 relaxed the *invariant* so a relocated worktree may
	// carry an ABSOLUTE path — see absWorktreePath / FindByGitRegistry — so the
	// blanket `!filepath.IsAbs` assertion is dropped. Create itself still writes
	// a relative path, asserted here via the relative-prefix check, which an
	// absolute path could not satisfy.)
	if !strings.HasPrefix(meta.Path, ".agentfactory/worktrees/") {
		t.Errorf("Create meta.Path: got %q, want relative prefix %q", meta.Path, ".agentfactory/worktrees/")
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
	absPath, _, err := Create(realDir, "solver", CreateOpts{})
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
	// The rendered CLAUDE.md should contain the factory root path (RootDir=factoryRoot)
	if !strings.Contains(string(claudeData), realDir) {
		t.Errorf("CLAUDE.md does not contain factory root path %q", realDir)
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

	absPath, _, err := Create(realDir, "solver", CreateOpts{})
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
	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
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

// TestRemove_PreservesBranchCommittedCollidingSkill drives the full public
// Remove path (the af down / formula-completion lifecycle the issue names) and
// asserts that a branch-committed skill whose name collides with a factory skill
// is preserved: teardown removes only the untracked merge copy, so the non-force
// git worktree remove succeeds instead of bricking on a tree dirtied by deleting
// tracked content (the originally reported symptom).
func TestRemove_PreservesBranchCommittedCollidingSkill(t *testing.T) {
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

	// .agentfactory/ is gitignored in production so worktree setup files do not
	// block non-force removal.
	if err := os.WriteFile(filepath.Join(realDir, ".gitignore"), []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	// Commit a skill under .claude/skills whose name collides with a factory
	// skill; the worktree inherits it as tracked branch content.
	committed := filepath.Join(realDir, ".claude", "skills", "github-issue", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(committed), 0o755); err != nil {
		t.Fatalf("mkdir committed skill: %v", err)
	}
	if err := os.WriteFile(committed, []byte("branch-committed github-issue"), 0o644); err != nil {
		t.Fatalf("write committed skill: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore", ".claude/skills/github-issue/SKILL.md"},
		{"commit", "-m", "add committed skill"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// A factory-only skill present on disk but NOT committed; the lifecycle
	// merge-copies it into the worktree as an untracked entry that teardown
	// should still remove.
	factoryOnly := filepath.Join(realDir, ".claude", "skills", "architecture-docs")
	if err := os.MkdirAll(factoryOnly, 0o755); err != nil {
		t.Fatalf("mkdir factory-only skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(factoryOnly, "SKILL.md"), []byte("factory architecture-docs"), 0o644); err != nil {
		t.Fatalf("write factory-only skill: %v", err)
	}

	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Preconditions: the worktree tracks the committed colliding skill and has
	// the untracked factory merge copy.
	if _, err := os.Stat(filepath.Join(absPath, ".claude", "skills", "github-issue", "SKILL.md")); err != nil {
		t.Fatalf("precondition: committed skill should exist in worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(absPath, ".claude", "skills", "architecture-docs")); err != nil {
		t.Fatalf("precondition: factory merge copy should exist in worktree: %v", err)
	}
	if tracked, ok := trackedSkillDirs(absPath); !ok || !tracked["github-issue"] {
		t.Fatalf("precondition: github-issue should be git-tracked in worktree (ok=%v tracked=%v)", ok, tracked)
	}

	// Non-force Remove must succeed. Against the pre-fix code it fails because
	// deleting the tracked skill dirties the tree and git refuses the remove.
	if err := Remove(realDir, meta); err != nil {
		t.Fatalf("Remove should succeed with branch-committed skills preserved: %v", err)
	}

	// Worktree is cleanly gone and no orphaned worktree survives with a deleted
	// committed skill.
	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should not exist after Remove, got err: %v", err)
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
	path, id, outcome, err := ResolveOrCreate("/unused/root", "solver", "", "/some/worktree", "wt-abc123", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if path != "/some/worktree" {
		t.Errorf("path: got %q, want %q", path, "/some/worktree")
	}
	if id != "wt-abc123" {
		t.Errorf("id: got %q, want %q", id, "wt-abc123")
	}
	if outcome.IsCreated() {
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

	wtPath, meta, err := Create(realDir, "manager", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	path, id, outcome, err := ResolveOrCreate(realDir, "solver", "manager", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if path != wtPath {
		t.Errorf("path: got %q, want %q", path, wtPath)
	}
	if id != meta.ID {
		t.Errorf("id: got %q, want %q", id, meta.ID)
	}
	if outcome.IsCreated() {
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

	path, id, outcome, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if !outcome.IsCreated() {
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

	wtPath, meta, err := Create(realDir, "manager", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	path, id, outcome, err := ResolveOrCreate(realDir, "manager", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if outcome.IsCreated() {
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

	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
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

	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
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

func addGitignore(t *testing.T, dir string) {
	t.Helper()
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "add gitignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestGC_DoesNotRemoveRunningSession and TestGC_ForceRemovesDeadSession live in
// worktree_gc_integration_test.go (//go:build integration): they stand up / kill
// real af-solver tmux sessions to prove GC respects (and force-removes) live vs
// dead sessions, which cannot be faked. They run only under
// `make test-integration`, never in the default suite (#309 Phase 3).

func TestGC_ForceRemoveRequiresCorrectSessionName(t *testing.T) {
	source, err := os.ReadFile("worktree.go")
	if err != nil {
		t.Fatalf("reading worktree.go: %v", err)
	}

	src := string(source)

	gcStart := strings.Index(src, "func GC(")
	if gcStart < 0 {
		t.Fatal("GC function not found in source")
	}

	gcBody := src[gcStart:]
	nextFunc := strings.Index(gcBody[1:], "\nfunc ")
	if nextFunc > 0 {
		gcBody = gcBody[:nextFunc+1]
	}

	hasAfPrefix := strings.Contains(gcBody, `"=af-"+meta.Owner`) ||
		strings.Contains(gcBody, `"=af-" + meta.Owner`)
	hasForceRemove := strings.Contains(gcBody, "ForceRemove(")

	if !hasAfPrefix {
		t.Error("GC function must contain \"af-\"+meta.Owner session check")
	}
	if !hasForceRemove {
		t.Error("GC function must contain ForceRemove call (not Remove)")
	}
	if hasForceRemove && !hasAfPrefix {
		t.Fatal("ATOMICITY VIOLATION: ForceRemove is reachable without correct session name check — would destroy all worktrees including live ones")
	}
}

// TestGC_SkipsWorktreeWithHookedFormula verifies the #392 Phase 3 GC guard:
// a worktree whose owner tmux session is dead is NOT force-removed when any of
// its agents (owner OR a co-tenant in meta.Agents) has a non-empty
// .runtime/hooked_formula pointer. Pure unit test: GC's `tmux has-session` for a
// unique/nonexistent owner fails, so GC falls through to the HasUnfinishedFormula
// guard, which `continue`s before ForceRemove — leaving the meta file intact.
func TestGC_SkipsWorktreeWithHookedFormula(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if err := os.MkdirAll(WorktreesDir(realDir), 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}

	// writeWT writes a meta and, when inflightAgent != "", a non-empty
	// hooked_formula for that agent inside the resolved worktree path.
	writeWT := func(id, owner string, agents []string, inflightAgent, content string) {
		meta := &Meta{
			ID:     id,
			Owner:  owner,
			Branch: "af/" + owner + "-" + id,
			Path:   filepath.Join(".agentfactory", "worktrees", id),
			Agents: agents,
		}
		if err := WriteMeta(realDir, meta); err != nil {
			t.Fatalf("WriteMeta %s: %v", id, err)
		}
		if inflightAgent != "" {
			rt := filepath.Join(realDir, meta.Path, ".agentfactory", "agents", inflightAgent, ".runtime")
			if err := os.MkdirAll(rt, 0o755); err != nil {
				t.Fatalf("mkdir runtime: %v", err)
			}
			if err := os.WriteFile(filepath.Join(rt, "hooked_formula"), []byte(content), 0o644); err != nil {
				t.Fatalf("write hooked_formula: %v", err)
			}
		}
	}

	// Owner names are unique + nonexistent so no real `af-<owner>` tmux session is
	// alive — GC's has-session check fails and the guard is reached.
	// Case A: single-agent owner mid-formula.
	writeWT("wt-gcskipa", "gcskip-owner-aaa", []string{"gcskip-owner-aaa"}, "gcskip-owner-aaa", "bd-epic-123")
	// Case B (co-tenant / Gap-5): owner session dead, owner has NO formula, but a
	// co-tenant in meta.Agents is mid-formula → worktree must still be preserved.
	writeWT("wt-gcskipb", "gcskip-owner-bbb", []string{"gcskip-owner-bbb", "gcskip-cotenant-bbb"}, "gcskip-cotenant-bbb", "bd-epic-456")

	if _, err := GC(realDir); err != nil {
		t.Fatalf("GC: %v", err)
	}

	if _, err := ReadMeta(realDir, "wt-gcskipa"); err != nil {
		t.Errorf("single-agent in-flight worktree must survive GC (guard skips ForceRemove): %v", err)
	}
	if _, err := ReadMeta(realDir, "wt-gcskipb"); err != nil {
		t.Errorf("co-tenant in-flight worktree must survive GC (Gap-5): %v", err)
	}
}

// TestHasUnfinishedFormula pins the helper's contract directly (#392 Phase 3),
// independent of the GC/`af down` call sites: it iterates all meta.Agents and a
// pointer only protects when non-empty after strings.TrimSpace. Whitespace-only,
// missing file, and missing .runtime dir all mean "not in flight" (removable).
func TestHasUnfinishedFormula(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// writePointer writes hooked_formula for agent under the worktree path.
	writePointer := func(wtPath, agent, content string) {
		rt := filepath.Join(wtPath, ".agentfactory", "agents", agent, ".runtime")
		if err := os.MkdirAll(rt, 0o755); err != nil {
			t.Fatalf("mkdir runtime: %v", err)
		}
		if err := os.WriteFile(filepath.Join(rt, "hooked_formula"), []byte(content), 0o644); err != nil {
			t.Fatalf("write hooked_formula: %v", err)
		}
	}

	mk := func(id string, agents []string) *Meta {
		return &Meta{
			ID:     id,
			Owner:  agents[0],
			Branch: "af/" + agents[0] + "-" + id,
			Path:   filepath.Join(".agentfactory", "worktrees", id),
			Agents: agents,
		}
	}

	// Case 1: a co-tenant (not the first agent) has a non-empty pointer → true.
	m1 := mk("wt-huf1", []string{"owner1", "cotenant1"})
	writePointer(filepath.Join(realDir, m1.Path), "cotenant1", "bd-epic-1\n")
	if !HasUnfinishedFormula(realDir, m1) {
		t.Error("non-empty co-tenant pointer must report true")
	}

	// Case 2: only a whitespace-only pointer → false (removable), mirroring readHookedFormulaID.
	m2 := mk("wt-huf2", []string{"owner2"})
	writePointer(filepath.Join(realDir, m2.Path), "owner2", "  \n\t ")
	if HasUnfinishedFormula(realDir, m2) {
		t.Error("whitespace-only pointer must report false")
	}

	// Case 3: no .runtime dir / no pointer at all → false.
	m3 := mk("wt-huf3", []string{"owner3", "cotenant3"})
	if HasUnfinishedFormula(realDir, m3) {
		t.Error("missing pointer must report false")
	}

	// Case 4: empty agents list → false.
	m4 := mk("wt-huf4", []string{"owner4"})
	m4.Agents = nil
	if HasUnfinishedFormula(realDir, m4) {
		t.Error("empty agents list must report false")
	}
}

func TestCountActiveWorktrees(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	count, err := countActiveWorktrees(realDir)
	if err != nil {
		t.Fatalf("countActiveWorktrees on nonexistent dir: %v", err)
	}
	if count != 0 {
		t.Errorf("count on nonexistent dir: got %d, want 0", count)
	}

	wtDir := WorktreesDir(realDir)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	count, err = countActiveWorktrees(realDir)
	if err != nil {
		t.Fatalf("countActiveWorktrees on empty dir: %v", err)
	}
	if count != 0 {
		t.Errorf("count on empty dir: got %d, want 0", count)
	}

	for i, name := range []string{"wt-aaa111", "wt-bbb222", "wt-ccc333"} {
		meta := &Meta{
			ID:    name,
			Owner: fmt.Sprintf("agent%d", i),
		}
		if err := WriteMeta(realDir, meta); err != nil {
			t.Fatalf("WriteMeta: %v", err)
		}
	}

	if err := os.WriteFile(filepath.Join(wtDir, "readme.txt"), []byte("not a meta"), 0o644); err != nil {
		t.Fatalf("write non-meta: %v", err)
	}

	count, err = countActiveWorktrees(realDir)
	if err != nil {
		t.Fatalf("countActiveWorktrees: %v", err)
	}
	if count != 3 {
		t.Errorf("count: got %d, want 3", count)
	}
}

// --- Resource Gate Tests ---

func TestCheckResources_RefusesWhenDiskLow(t *testing.T) {
	orig := statfsFunc
	statfsFunc = func(path string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 100_000_000 // ~381 GB total
		buf.Bavail = 100_000     // ~390 MB available — below 2GB
		return nil
	}
	t.Cleanup(func() { statfsFunc = orig })

	origMem := readMemInfoFunc
	readMemInfoFunc = func() (uint64, error) { return 8192, nil }
	t.Cleanup(func() { readMemInfoFunc = origMem })

	err := checkResources("/tmp")
	if err == nil {
		t.Fatal("expected error for low disk, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient") || !strings.Contains(err.Error(), "disk") {
		t.Errorf("error should mention insufficient disk, got: %v", err)
	}
	if !strings.Contains(err.Error(), "af down") {
		t.Errorf("error should include remediation guidance, got: %v", err)
	}
}

func TestCheckResources_RefusesWhenDiskPercentLow(t *testing.T) {
	orig := statfsFunc
	statfsFunc = func(path string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 1_000_000_000 // ~3.7 TB total
		buf.Bavail = 50_000_000    // ~190 GB avail, but only 5% of total
		return nil
	}
	t.Cleanup(func() { statfsFunc = orig })

	origMem := readMemInfoFunc
	readMemInfoFunc = func() (uint64, error) { return 8192, nil }
	t.Cleanup(func() { readMemInfoFunc = origMem })

	err := checkResources("/tmp")
	if err == nil {
		t.Fatal("expected error for low disk percentage, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient") || !strings.Contains(err.Error(), "disk") {
		t.Errorf("error should mention insufficient disk, got: %v", err)
	}
}

func TestCheckResources_PassesWhenDiskOK(t *testing.T) {
	orig := statfsFunc
	statfsFunc = func(path string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 100_000_000 // ~381 GB total
		buf.Bavail = 30_000_000  // ~114 GB avail (30%)
		return nil
	}
	t.Cleanup(func() { statfsFunc = orig })

	origMem := readMemInfoFunc
	readMemInfoFunc = func() (uint64, error) { return 8192, nil }
	t.Cleanup(func() { readMemInfoFunc = origMem })

	err := checkResources("/tmp")
	if err != nil {
		t.Errorf("expected nil for adequate disk, got: %v", err)
	}
}

func TestCheckResources_RefusesWhenMemoryLow(t *testing.T) {
	orig := statfsFunc
	statfsFunc = func(path string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 100_000_000
		buf.Bavail = 30_000_000
		return nil
	}
	t.Cleanup(func() { statfsFunc = orig })

	origMem := readMemInfoFunc
	readMemInfoFunc = func() (uint64, error) { return 512, nil } // 512MB < 1024MB threshold
	t.Cleanup(func() { readMemInfoFunc = origMem })

	err := checkResources("/tmp")
	if err == nil {
		t.Fatal("expected error for low memory, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient") || !strings.Contains(err.Error(), "memory") {
		t.Errorf("error should mention insufficient memory, got: %v", err)
	}
}

func TestCreate_MaxWorktreesEnforced(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	// Create 2 fake worktrees by writing meta files
	wtDir := WorktreesDir(realDir)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 2; i++ {
		meta := &Meta{ID: fmt.Sprintf("wt-fake%d", i), Owner: fmt.Sprintf("agent%d", i)}
		if err := WriteMeta(realDir, meta); err != nil {
			t.Fatalf("WriteMeta: %v", err)
		}
	}

	// Mock resources as adequate
	origStatfs := statfsFunc
	statfsFunc = func(path string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 100_000_000
		buf.Bavail = 30_000_000
		return nil
	}
	t.Cleanup(func() { statfsFunc = origStatfs })

	origMem := readMemInfoFunc
	readMemInfoFunc = func() (uint64, error) { return 8192, nil }
	t.Cleanup(func() { readMemInfoFunc = origMem })

	// Try to create with MaxWorktrees=2 (already at cap)
	_, _, err = Create(realDir, "newagent", CreateOpts{MaxWorktrees: 2})
	if err == nil {
		t.Fatal("expected error when at MaxWorktrees capacity, got nil")
	}
	if !strings.Contains(err.Error(), "at capacity") {
		t.Errorf("error should mention capacity, got: %v", err)
	}
	if !strings.Contains(err.Error(), "2/2") {
		t.Errorf("error should mention count/max, got: %v", err)
	}
}

func TestCreate_MaxWorktreesZeroIsUncapped(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	// Create several fake worktrees
	wtDir := WorktreesDir(realDir)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 5; i++ {
		meta := &Meta{ID: fmt.Sprintf("wt-fake%d", i), Owner: fmt.Sprintf("agent%d", i)}
		if err := WriteMeta(realDir, meta); err != nil {
			t.Fatalf("WriteMeta: %v", err)
		}
	}

	// Mock resources as adequate
	origStatfs := statfsFunc
	statfsFunc = func(path string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 100_000_000
		buf.Bavail = 30_000_000
		return nil
	}
	t.Cleanup(func() { statfsFunc = origStatfs })

	origMem := readMemInfoFunc
	readMemInfoFunc = func() (uint64, error) { return 8192, nil }
	t.Cleanup(func() { readMemInfoFunc = origMem })

	// MaxWorktrees=0 should not cap
	_, _, err = Create(realDir, "newagent", CreateOpts{MaxWorktrees: 0})
	if err != nil {
		t.Fatalf("expected no cap with MaxWorktrees=0, got: %v", err)
	}
}

func TestCreate_SerializedViaLock(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	// Mock resources as adequate
	origStatfs := statfsFunc
	statfsFunc = func(path string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 100_000_000
		buf.Bavail = 30_000_000
		return nil
	}
	t.Cleanup(func() { statfsFunc = origStatfs })

	origMem := readMemInfoFunc
	readMemInfoFunc = func() (uint64, error) { return 8192, nil }
	t.Cleanup(func() { readMemInfoFunc = origMem })

	// Run two creates concurrently. At least one should succeed,
	// and neither should panic or corrupt state.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, errs[idx] = Create(realDir, fmt.Sprintf("concurrent-%d", idx), CreateOpts{})
		}(i)
	}
	wg.Wait()

	// At least one must succeed
	successes := 0
	for _, e := range errs {
		if e == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatalf("expected at least 1 success from concurrent Create, got errors: %v, %v", errs[0], errs[1])
	}

	// Lock file should be cleaned up after both complete
	lockPath := filepath.Join(realDir, ".agentfactory", "worktrees", ".creation-lock")
	if _, err := os.Stat(lockPath); err == nil {
		t.Error("creation lock file should be cleaned up after Create completes")
	}
}

func TestCreate_ResourceCheckFailsBeforeGitWorktreeAdd(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	// Mock resources as insufficient
	origStatfs := statfsFunc
	statfsFunc = func(path string, buf *syscall.Statfs_t) error {
		buf.Bsize = 4096
		buf.Blocks = 100_000_000
		buf.Bavail = 100_000 // too low
		return nil
	}
	t.Cleanup(func() { statfsFunc = origStatfs })

	origMem := readMemInfoFunc
	readMemInfoFunc = func() (uint64, error) { return 8192, nil }
	t.Cleanup(func() { readMemInfoFunc = origMem })

	_, _, err = Create(realDir, "failagent", CreateOpts{})
	if err == nil {
		t.Fatal("expected error for insufficient resources")
	}

	// Verify no worktree directory was created
	entries, _ := os.ReadDir(WorktreesDir(realDir))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "wt-") && e.IsDir() {
			t.Errorf("worktree directory %s should not exist after resource check failure", e.Name())
		}
	}
}

func TestEnsureWorktreeLinks_CreatesSymlinks(t *testing.T) {
	factoryRoot := t.TempDir()
	worktreePath := t.TempDir()

	// Create symlink targets at factory root
	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".runtime"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".agentfactory"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"), []byte("# Agents\n"), 0o644)

	err := EnsureWorktreeLinks(factoryRoot, worktreePath)
	if err != nil {
		t.Fatalf("EnsureWorktreeLinks: %v", err)
	}

	// Verify .claude/skills symlink
	link, err := os.Readlink(filepath.Join(worktreePath, ".claude", "skills"))
	if err != nil {
		t.Fatalf("readlink .claude/skills: %v", err)
	}
	if link != filepath.Join(factoryRoot, ".claude", "skills") {
		t.Errorf(".claude/skills symlink: got %q, want %q", link, filepath.Join(factoryRoot, ".claude", "skills"))
	}

	// Verify .runtime symlink
	link, err = os.Readlink(filepath.Join(worktreePath, ".runtime"))
	if err != nil {
		t.Fatalf("readlink .runtime: %v", err)
	}
	if link != filepath.Join(factoryRoot, ".runtime") {
		t.Errorf(".runtime symlink: got %q, want %q", link, filepath.Join(factoryRoot, ".runtime"))
	}

	// Verify .agentfactory/AGENTS.md symlink
	link, err = os.Readlink(filepath.Join(worktreePath, ".agentfactory", "AGENTS.md"))
	if err != nil {
		t.Fatalf("readlink .agentfactory/AGENTS.md: %v", err)
	}
	if link != filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md") {
		t.Errorf(".agentfactory/AGENTS.md symlink: got %q, want %q", link, filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"))
	}
}

func TestEnsureWorktreeLinks_Idempotent(t *testing.T) {
	factoryRoot := t.TempDir()
	worktreePath := t.TempDir()

	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".runtime"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".agentfactory"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"), []byte("# Agents\n"), 0o644)

	// Call twice
	if err := EnsureWorktreeLinks(factoryRoot, worktreePath); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := EnsureWorktreeLinks(factoryRoot, worktreePath); err != nil {
		t.Fatalf("second call: %v", err)
	}

	// Verify symlinks still correct after second call
	link, err := os.Readlink(filepath.Join(worktreePath, ".claude", "skills"))
	if err != nil {
		t.Fatalf("readlink .claude/skills: %v", err)
	}
	if link != filepath.Join(factoryRoot, ".claude", "skills") {
		t.Errorf(".claude/skills symlink: got %q, want %q", link, filepath.Join(factoryRoot, ".claude", "skills"))
	}
}

func TestEnsureWorktreeLinks_ExistingRealFile(t *testing.T) {
	factoryRoot := t.TempDir()
	worktreePath := t.TempDir()

	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".runtime"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".agentfactory"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"), []byte("# Agents\n"), 0o644)

	// Create a real file at .agentfactory/AGENTS.md in worktree
	realContent := []byte("user content\n")
	os.MkdirAll(filepath.Join(worktreePath, ".agentfactory"), 0o755)
	os.WriteFile(filepath.Join(worktreePath, ".agentfactory", "AGENTS.md"), realContent, 0o644)

	err := EnsureWorktreeLinks(factoryRoot, worktreePath)
	if err != nil {
		t.Fatalf("EnsureWorktreeLinks: %v", err)
	}

	// Verify real file is preserved (not a symlink)
	fi, err := os.Lstat(filepath.Join(worktreePath, ".agentfactory", "AGENTS.md"))
	if err != nil {
		t.Fatalf("lstat .agentfactory/AGENTS.md: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error(".agentfactory/AGENTS.md should remain a real file, not be replaced with symlink")
	}
	data, _ := os.ReadFile(filepath.Join(worktreePath, ".agentfactory", "AGENTS.md"))
	if string(data) != string(realContent) {
		t.Errorf(".agentfactory/AGENTS.md content changed: got %q, want %q", data, realContent)
	}
}

func TestEnsureWorktreeLinks_CreatesParentDir(t *testing.T) {
	factoryRoot := t.TempDir()
	worktreePath := t.TempDir()

	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".runtime"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".agentfactory"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"), []byte("# Agents\n"), 0o644)

	// Verify .claude/ does NOT exist in worktree initially
	if _, err := os.Stat(filepath.Join(worktreePath, ".claude")); !os.IsNotExist(err) {
		t.Fatal(".claude/ should not exist before EnsureWorktreeLinks")
	}

	err := EnsureWorktreeLinks(factoryRoot, worktreePath)
	if err != nil {
		t.Fatalf("EnsureWorktreeLinks: %v", err)
	}

	// Verify .claude/ was created and .claude/skills symlink exists
	fi, err := os.Lstat(filepath.Join(worktreePath, ".claude", "skills"))
	if err != nil {
		t.Fatalf("lstat .claude/skills: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error(".claude/skills should be a symlink")
	}
}

func TestSetupAgent_RootDir(t *testing.T) {
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

	absPath, _, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	agentDir, err := SetupAgent(realDir, absPath, "solver", true)
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}

	claudeData, err := os.ReadFile(filepath.Join(agentDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}

	// CLAUDE.md must contain "Factory root" line with factory root path, not worktree path
	if !strings.Contains(string(claudeData), realDir) {
		t.Errorf("CLAUDE.md does not contain factory root path %q", realDir)
	}
	// The "Factory root:" line should reference realDir, not the worktree subpath
	for _, line := range strings.Split(string(claudeData), "\n") {
		if strings.Contains(line, "Factory root") && strings.Contains(line, absPath) && !strings.Contains(line, realDir) {
			t.Errorf("Factory root line references worktree path instead of factory root: %s", line)
		}
	}
}

func TestForceRemove_PreservesFactoryRoot(t *testing.T) {
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
	addGitignore(t, realDir)

	skillsDir := filepath.Join(realDir, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatalf("mkdir .claude/skills: %v", err)
	}
	runtimeDir := filepath.Join(realDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}
	agentsMD := filepath.Join(realDir, ".agentfactory", "AGENTS.md")
	agentsContent := []byte("# Agents\ntest content\n")
	if err := os.WriteFile(agentsMD, agentsContent, 0o644); err != nil {
		t.Fatalf("write .agentfactory/AGENTS.md: %v", err)
	}

	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, rel := range []string{filepath.Join(".claude", "skills"), ".runtime", filepath.Join(".agentfactory", "AGENTS.md")} {
		p := filepath.Join(absPath, rel)
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("symlink %s should exist before ForceRemove: %v", rel, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s should be a symlink before ForceRemove", rel)
		}
	}

	if err := ForceRemove(realDir, meta); err != nil {
		t.Fatalf("ForceRemove: %v", err)
	}

	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should not exist after ForceRemove, got err: %v", err)
	}

	if _, err := os.Stat(skillsDir); err != nil {
		t.Errorf("factory root .claude/skills should still exist after ForceRemove: %v", err)
	}
	if _, err := os.Stat(runtimeDir); err != nil {
		t.Errorf("factory root .runtime should still exist after ForceRemove: %v", err)
	}
	got, err := os.ReadFile(agentsMD)
	if err != nil {
		t.Errorf("factory root AGENTS.md should still exist after ForceRemove: %v", err)
	} else if string(got) != string(agentsContent) {
		t.Errorf("AGENTS.md content changed: got %q, want %q", got, agentsContent)
	}
}

func TestUnlinkBeforeRemove_RemovesSymlinks(t *testing.T) {
	factoryRoot := t.TempDir()
	worktreePath := t.TempDir()

	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".runtime"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".agentfactory"), 0o755)
	agentsContent := []byte("# Agents\n")
	os.WriteFile(filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"), agentsContent, 0o644)

	if err := EnsureWorktreeLinks(factoryRoot, worktreePath); err != nil {
		t.Fatalf("EnsureWorktreeLinks: %v", err)
	}

	for _, rel := range []string{filepath.Join(".claude", "skills"), ".runtime", filepath.Join(".agentfactory", "AGENTS.md")} {
		fi, err := os.Lstat(filepath.Join(worktreePath, rel))
		if err != nil {
			t.Fatalf("symlink %s should exist before unlinkBeforeRemove: %v", rel, err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s should be a symlink", rel)
		}
	}

	unlinkBeforeRemove(worktreePath)

	for _, rel := range []string{filepath.Join(".claude", "skills"), ".runtime", filepath.Join(".agentfactory", "AGENTS.md")} {
		_, err := os.Lstat(filepath.Join(worktreePath, rel))
		if !os.IsNotExist(err) {
			t.Errorf("symlink %s should not exist after unlinkBeforeRemove, got err: %v", rel, err)
		}
	}

	if _, err := os.Stat(filepath.Join(factoryRoot, ".claude", "skills")); err != nil {
		t.Errorf("factory root .claude/skills should still exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(factoryRoot, ".runtime")); err != nil {
		t.Errorf("factory root .runtime should still exist: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"))
	if err != nil {
		t.Errorf("factory root .agentfactory/AGENTS.md should still exist: %v", err)
	} else if string(got) != string(agentsContent) {
		t.Errorf("AGENTS.md content changed: got %q, want %q", got, agentsContent)
	}
}

func TestUnlinkBeforeRemove_ToleratesMissing(t *testing.T) {
	worktreePath := t.TempDir()
	unlinkBeforeRemove(worktreePath)
}

func TestEnsureWorktreeLinks_RealSkillsDir_MergesFactorySkills(t *testing.T) {
	factoryRoot := t.TempDir()
	worktreePath := t.TempDir()

	// Factory root has skills A and B
	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills", "factory-skill-A"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".claude", "skills", "factory-skill-A", "SKILL.md"), []byte("factory A content"), 0o644)
	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills", "factory-skill-B"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".claude", "skills", "factory-skill-B", "SKILL.md"), []byte("factory B content"), 0o644)

	os.MkdirAll(filepath.Join(factoryRoot, ".runtime"), 0o755)
	os.MkdirAll(filepath.Join(factoryRoot, ".agentfactory"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".agentfactory", "AGENTS.md"), []byte("# Agents\n"), 0o644)

	// Worktree has skill B as a git-tracked real directory (different content)
	os.MkdirAll(filepath.Join(worktreePath, ".claude", "skills", "factory-skill-B"), 0o755)
	os.WriteFile(filepath.Join(worktreePath, ".claude", "skills", "factory-skill-B", "SKILL.md"), []byte("git-tracked B content"), 0o644)

	// Capture stderr
	var buf bytes.Buffer
	origWriter := stderrWriter
	stderrWriter = &buf
	t.Cleanup(func() { stderrWriter = origWriter })

	err := EnsureWorktreeLinks(factoryRoot, worktreePath)
	if err != nil {
		t.Fatalf("EnsureWorktreeLinks: %v", err)
	}

	// Skill A should be merged (copied from factory)
	dataA, err := os.ReadFile(filepath.Join(worktreePath, ".claude", "skills", "factory-skill-A", "SKILL.md"))
	if err != nil {
		t.Fatalf("factory-skill-A should exist in worktree: %v", err)
	}
	if string(dataA) != "factory A content" {
		t.Errorf("factory-skill-A content: got %q, want %q", dataA, "factory A content")
	}

	// Skill B should retain git-tracked content (not overwritten)
	dataB, err := os.ReadFile(filepath.Join(worktreePath, ".claude", "skills", "factory-skill-B", "SKILL.md"))
	if err != nil {
		t.Fatalf("factory-skill-B should exist: %v", err)
	}
	if string(dataB) != "git-tracked B content" {
		t.Errorf("factory-skill-B should keep git-tracked content: got %q, want %q", dataB, "git-tracked B content")
	}

	// Info message should mention 1 merged skill
	output := buf.String()
	if !strings.Contains(output, "merged 1 factory skill(s)") {
		t.Errorf("expected info message about merged skills, got: %q", output)
	}

	// Should NOT contain the old "skipping symlink" warning for .claude/skills
	if strings.Contains(output, "exists as real file/dir, skipping symlink") && strings.Contains(output, "skills") {
		t.Errorf("should not warn about skipping .claude/skills, got: %q", output)
	}

	// .runtime should still be a symlink
	fi, err := os.Lstat(filepath.Join(worktreePath, ".runtime"))
	if err != nil {
		t.Fatalf("lstat .runtime: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error(".runtime should be a symlink")
	}
}

func TestCleanupMergedSkills_RemovesFactoryCopiedEntries(t *testing.T) {
	factoryRoot := t.TempDir()
	// Cleanup runs inside a real git worktree in production; provenance is
	// derived from git, so the test worktree must be a real git repo. The
	// factory-named copies below are left UNTRACKED (merge copies), so they
	// are still removed.
	worktreePath := t.TempDir()
	initGitRepo(t, worktreePath)

	// Factory has skills A and B
	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills", "skill-A"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".claude", "skills", "skill-A", "SKILL.md"), []byte("A"), 0o644)
	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills", "skill-B"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".claude", "skills", "skill-B", "SKILL.md"), []byte("B"), 0o644)

	// Worktree has skills A, B (untracked merge copies), and a non-colliding entry
	os.MkdirAll(filepath.Join(worktreePath, ".claude", "skills", "skill-A"), 0o755)
	os.WriteFile(filepath.Join(worktreePath, ".claude", "skills", "skill-A", "SKILL.md"), []byte("A"), 0o644)
	os.MkdirAll(filepath.Join(worktreePath, ".claude", "skills", "skill-B"), 0o755)
	os.WriteFile(filepath.Join(worktreePath, ".claude", "skills", "skill-B", "SKILL.md"), []byte("B"), 0o644)
	os.MkdirAll(filepath.Join(worktreePath, ".claude", "skills", "git-only-skill"), 0o755)
	os.WriteFile(filepath.Join(worktreePath, ".claude", "skills", "git-only-skill", "SKILL.md"), []byte("git"), 0o644)

	cleanupMergedSkills(factoryRoot, worktreePath)

	// Factory-matching untracked entries (A, B) should be removed
	if _, err := os.Stat(filepath.Join(worktreePath, ".claude", "skills", "skill-A")); !os.IsNotExist(err) {
		t.Error("skill-A should have been removed by cleanup")
	}
	if _, err := os.Stat(filepath.Join(worktreePath, ".claude", "skills", "skill-B")); !os.IsNotExist(err) {
		t.Error("skill-B should have been removed by cleanup")
	}

	// Non-colliding entry should survive (not a factory skill name)
	if _, err := os.Stat(filepath.Join(worktreePath, ".claude", "skills", "git-only-skill")); err != nil {
		t.Error("git-only-skill should survive cleanup")
	}
}

// TestCleanupMergedSkills_PreservesBranchCommittedCollidingSkill is the
// regression test for issue #59: a skill directory the worktree's branch
// committed itself must survive teardown even when its name collides with a
// factory skill, while an untracked merge copy of a factory skill is removed.
func TestCleanupMergedSkills_PreservesBranchCommittedCollidingSkill(t *testing.T) {
	factoryRoot := t.TempDir()
	for _, s := range []string{"github-issue", "architecture-docs"} {
		dir := filepath.Join(factoryRoot, ".claude", "skills", s)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir factory skill %s: %v", s, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("factory "+s), 0o644); err != nil {
			t.Fatalf("write factory skill %s: %v", s, err)
		}
	}

	// Worktree is a real git repo whose branch COMMITS .claude/skills/github-issue
	// (name collides with a factory skill, but it is branch content).
	worktreePath := t.TempDir()
	initGitRepo(t, worktreePath)
	committed := filepath.Join(worktreePath, ".claude", "skills", "github-issue", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(committed), 0o755); err != nil {
		t.Fatalf("mkdir committed skill: %v", err)
	}
	if err := os.WriteFile(committed, []byte("branch-committed github-issue"), 0o644); err != nil {
		t.Fatalf("write committed skill: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".claude/skills/github-issue/SKILL.md"},
		{"commit", "-m", "add branch-committed skill"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktreePath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// An UNTRACKED merge copy of a factory skill is also present (lifecycle artifact).
	mergedDir := filepath.Join(worktreePath, ".claude", "skills", "architecture-docs")
	if err := os.MkdirAll(mergedDir, 0o755); err != nil {
		t.Fatalf("mkdir merged skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mergedDir, "SKILL.md"), []byte("factory architecture-docs"), 0o644); err != nil {
		t.Fatalf("write merged skill: %v", err)
	}

	cleanupMergedSkills(factoryRoot, worktreePath)

	// Branch-committed colliding skill must survive with its content intact.
	data, err := os.ReadFile(committed)
	if err != nil {
		t.Fatalf("branch-committed colliding skill was destroyed: %v", err)
	}
	if string(data) != "branch-committed github-issue" {
		t.Errorf("branch-committed skill content changed: got %q", data)
	}

	// Untracked merge copy must be removed.
	if _, err := os.Stat(mergedDir); !os.IsNotExist(err) {
		t.Errorf("untracked merge copy should have been removed; err=%v", err)
	}
}

// TestCleanupMergedSkills_FailSafeWhenProvenanceUnknown asserts ADR-017 Rule 3:
// when the git invocation fails, cleanup deletes nothing rather than guessing.
// A .git file pointing at a non-existent gitdir forces git ls-files to error
// deterministically — independent of whether TMPDIR sits inside an enclosing
// git repository.
func TestCleanupMergedSkills_FailSafeWhenProvenanceUnknown(t *testing.T) {
	factoryRoot := t.TempDir()
	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills", "github-issue"), 0o755)
	os.WriteFile(filepath.Join(factoryRoot, ".claude", "skills", "github-issue", "SKILL.md"), []byte("factory"), 0o644)

	worktreePath := t.TempDir()
	// Broken git boundary: git stops upward discovery at this .git file and then
	// fails because the referenced gitdir does not exist.
	if err := os.WriteFile(filepath.Join(worktreePath, ".git"), []byte("gitdir: ./nonexistent-gitdir\n"), 0o644); err != nil {
		t.Fatalf("write broken .git: %v", err)
	}
	colliding := filepath.Join(worktreePath, ".claude", "skills", "github-issue")
	os.MkdirAll(colliding, 0o755)
	os.WriteFile(filepath.Join(colliding, "SKILL.md"), []byte("local"), 0o644)

	// Sanity: provenance must be undeterminable for this test to be meaningful.
	if _, ok := trackedSkillDirs(worktreePath); ok {
		t.Fatal("precondition failed: git tracking should be undeterminable here")
	}

	cleanupMergedSkills(factoryRoot, worktreePath)

	if _, err := os.Stat(filepath.Join(colliding, "SKILL.md")); err != nil {
		t.Errorf("nothing should be deleted when git provenance is unknown (ADR-017 Rule 3); err=%v", err)
	}
}

func TestCleanupMergedSkills_NoOpOnSymlink(t *testing.T) {
	factoryRoot := t.TempDir()
	worktreePath := t.TempDir()

	os.MkdirAll(filepath.Join(factoryRoot, ".claude", "skills"), 0o755)

	// .claude/skills is a symlink in the worktree (normal case)
	os.MkdirAll(filepath.Join(worktreePath, ".claude"), 0o755)
	os.Symlink(
		filepath.Join(factoryRoot, ".claude", "skills"),
		filepath.Join(worktreePath, ".claude", "skills"),
	)

	// Should not panic or remove the symlink
	cleanupMergedSkills(factoryRoot, worktreePath)

	fi, err := os.Lstat(filepath.Join(worktreePath, ".claude", "skills"))
	if err != nil {
		t.Fatalf("symlink should still exist: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error(".claude/skills should still be a symlink after cleanup")
	}
}

func TestMergeSkillsDir_SkipsSymlinksInFactory(t *testing.T) {
	factoryRoot := t.TempDir()
	worktreeSkillsDir := t.TempDir()

	// Factory has a real skill and a symlink skill
	factorySkillsDir := filepath.Join(factoryRoot, "skills")
	os.MkdirAll(filepath.Join(factorySkillsDir, "real-skill"), 0o755)
	os.WriteFile(filepath.Join(factorySkillsDir, "real-skill", "SKILL.md"), []byte("real"), 0o644)

	// Create a symlink entry in factory (should be skipped)
	tmpTarget := t.TempDir()
	os.Symlink(tmpTarget, filepath.Join(factorySkillsDir, "symlink-skill"))

	var buf bytes.Buffer
	origWriter := stderrWriter
	stderrWriter = &buf
	t.Cleanup(func() { stderrWriter = origWriter })

	merged, err := mergeSkillsDir(factorySkillsDir, worktreeSkillsDir)
	if err != nil {
		t.Fatalf("mergeSkillsDir: %v", err)
	}

	if merged != 1 {
		t.Errorf("expected 1 merged, got %d", merged)
	}

	// Real skill should be copied
	if _, err := os.Stat(filepath.Join(worktreeSkillsDir, "real-skill")); err != nil {
		t.Error("real-skill should exist in worktree")
	}

	// Symlink skill should NOT be copied
	if _, err := os.Stat(filepath.Join(worktreeSkillsDir, "symlink-skill")); !os.IsNotExist(err) {
		t.Error("symlink-skill should not be copied to worktree")
	}

	if !strings.Contains(buf.String(), "skipping symlink") {
		t.Errorf("expected warning about skipping symlink, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Issue #392 Phase 1: relocation-safe rediscovery via git's registry.
// ---------------------------------------------------------------------------

// countAfBranches returns the number of local `af/<agent>-*` branches.
func countAfBranches(t *testing.T, dir, agent string) int {
	t.Helper()
	cmd := exec.Command("git", "branch", "--list", "af/"+agent+"-*")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// countAfWorktrees returns the number of git worktrees checked out on an
// `af/<agent>-*` branch, per `git worktree list --porcelain`.
func countAfWorktrees(t *testing.T, dir, agent string) int {
	t.Helper()
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git worktree list --porcelain: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "branch refs/heads/af/"+agent+"-") {
			count++
		}
	}
	return count
}

func TestResolveOrCreate_ReattachesViaGitRegistry_WhenMetaMissing(t *testing.T) {
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

	wtPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate the #392 condition: the non-durable sidecar is lost.
	if err := os.Remove(metaPath(realDir, meta.ID)); err != nil {
		t.Fatalf("removing meta sidecar: %v", err)
	}
	// Sanity: the meta fast path can no longer find it.
	if m, err := FindByOwner(realDir, "solver"); err != nil || m != nil {
		t.Fatalf("FindByOwner after sidecar delete: meta=%v err=%v, want nil/nil", m, err)
	}

	path, id, outcome, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if outcome.IsCreated() {
		t.Error("outcome.IsCreated(): got true, want false (should reattach via git registry, not fork)")
	}
	if path != wtPath {
		t.Errorf("path: got %q, want %q (reattach to existing worktree)", path, wtPath)
	}
	if id != meta.ID {
		t.Errorf("id: got %q, want %q (reattach to existing worktree)", id, meta.ID)
	}

	// Self-heal: the sidecar must be rewritten from the git registry.
	healed, err := ReadMeta(realDir, meta.ID)
	if err != nil {
		t.Fatalf("ReadMeta after self-heal: %v", err)
	}
	if healed.Owner != "solver" {
		t.Errorf("healed meta.Owner: got %q, want %q", healed.Owner, "solver")
	}

	// No new dir/branch: exactly one af/solver-* worktree and branch.
	if n := countAfWorktrees(t, realDir, "solver"); n != 1 {
		t.Errorf("af/solver-* worktrees: got %d, want 1 (no new worktree forked)", n)
	}
	if n := countAfBranches(t, realDir, "solver"); n != 1 {
		t.Errorf("af/solver-* branches: got %d, want 1 (no new branch forked)", n)
	}

	// A second `af up` must also not fork (now via the healed sidecar fast path).
	path2, id2, outcome2, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate (2nd): %v", err)
	}
	if outcome2.IsCreated() || path2 != wtPath || id2 != meta.ID {
		t.Errorf("2nd ResolveOrCreate: created=%v path=%q id=%q, want reattach to %q/%q",
			outcome2.IsCreated(), path2, id2, wtPath, meta.ID)
	}
}

func TestFindByGitRegistry_MultipleMatches_Warns(t *testing.T) {
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

	// Two distinct worktrees, both owned by "solver" → two af/solver-* branches.
	if _, _, err := Create(realDir, "solver", CreateOpts{}); err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	if _, _, err := Create(realDir, "solver", CreateOpts{}); err != nil {
		t.Fatalf("Create #2: %v", err)
	}
	if n := countAfWorktrees(t, realDir, "solver"); n != 2 {
		t.Fatalf("setup: af/solver-* worktrees got %d, want 2", n)
	}

	var buf bytes.Buffer
	old := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = old }()

	meta, err := FindByGitRegistry(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByGitRegistry: %v", err)
	}
	if meta != nil {
		t.Errorf("FindByGitRegistry on >1 match: got meta %+v, want nil (no wrong-guess reattach)", meta)
	}
	if !strings.Contains(strings.ToLower(buf.String()), "warning") {
		t.Errorf("expected a warning on >1 match, stderr was: %q", buf.String())
	}
}

func TestResolveOrCreate_NewAgent_CreatesFresh(t *testing.T) {
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

	// A genuinely-new agent — no sidecar, no git branch — must get a fresh worktree.
	path, id, outcome, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if !outcome.IsCreated() {
		t.Error("outcome.IsCreated(): got false, want true (genuinely-new agent gets a fresh worktree)")
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
	if foundMeta == nil || foundMeta.ID != id {
		t.Errorf("FindByOwner after create: got %+v, want meta with ID %q", foundMeta, id)
	}
}

// TestSling_WorktreeCreation_Unchanged proves that after the created-bool →
// Outcome migration, ResolveOrCreate's create-vs-reattach behavior (the path
// af sling depends on) is byte-for-byte unchanged: first call creates
// (IsCreated()==true), self-adoption reattaches (IsCreated()==false), and
// exactly one worktree exists.
func TestSling_WorktreeCreation_Unchanged(t *testing.T) {
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

	path1, id1, outcome1, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate (create): %v", err)
	}
	if !outcome1.IsCreated() {
		t.Error("first ResolveOrCreate: IsCreated()=false, want true (create path)")
	}

	path2, id2, outcome2, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate (reattach): %v", err)
	}
	if outcome2.IsCreated() {
		t.Error("second ResolveOrCreate: IsCreated()=true, want false (self-adoption reattach)")
	}
	if path2 != path1 || id2 != id1 {
		t.Errorf("self-adoption: got %q/%q, want same as create %q/%q", path2, id2, path1, id1)
	}

	if n := countAfWorktrees(t, realDir, "solver"); n != 1 {
		t.Errorf("af/solver-* worktrees: got %d, want 1 (reattach must not fork)", n)
	}
}

func TestFindByGitRegistry_PreservesCoTenants(t *testing.T) {
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
	// Register the co-tenant and the lingering agent so SetupAgent / any
	// agents.json intersection accepts them.
	agentsJSON := `{"agents":{` +
		`"solver":{"type":"autonomous","description":"Solves problems"},` +
		`"reviewer":{"type":"autonomous","description":"Reviews"},` +
		`"ghost":{"type":"autonomous","description":"Deregistered"}}}`
	if err := os.WriteFile(filepath.Join(realDir, ".agentfactory", "agents.json"), []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("write expanded agents.json: %v", err)
	}

	wtPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// (a) CURRENT co-tenant "reviewer": a real per-agent dir with a matching
	// .runtime/worktree_id, but NO af/reviewer-* branch and NO hooked_formula.
	if _, err := SetupAgent(realDir, wtPath, "reviewer", false); err != nil {
		t.Fatalf("SetupAgent(reviewer): %v", err)
	}

	// (b) LINGERING DEREGISTERED dir "ghost": dir present but worktree_id stale.
	ghostRuntime := filepath.Join(wtPath, ".agentfactory", "agents", "ghost", ".runtime")
	if err := os.MkdirAll(ghostRuntime, 0o755); err != nil {
		t.Fatalf("mkdir ghost runtime: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ghostRuntime, "worktree_id"), []byte("wt-stale9\n"), 0o644); err != nil {
		t.Fatalf("write ghost worktree_id: %v", err)
	}

	// Lose the sidecar so FindByGitRegistry must reconstruct Meta.Agents.
	if err := os.Remove(metaPath(realDir, meta.ID)); err != nil {
		t.Fatalf("removing meta sidecar: %v", err)
	}

	healed, err := FindByGitRegistry(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByGitRegistry: %v", err)
	}
	if healed == nil {
		t.Fatal("FindByGitRegistry returned nil, want a self-healed meta")
	}

	has := func(agents []string, name string) bool {
		for _, a := range agents {
			if a == name {
				return true
			}
		}
		return false
	}
	if !has(healed.Agents, "solver") {
		t.Errorf("Meta.Agents %v missing owner 'solver'", healed.Agents)
	}
	if !has(healed.Agents, "reviewer") {
		t.Errorf("Meta.Agents %v missing current co-tenant 'reviewer' "+
			"(must come from per-agent-dir+worktree_id, not branch inference)", healed.Agents)
	}
	if has(healed.Agents, "ghost") {
		t.Errorf("Meta.Agents %v wrongly includes lingering deregistered 'ghost' "+
			"(worktree_id mismatch must exclude it)", healed.Agents)
	}
	if len(healed.Agents) == 1 {
		t.Errorf("Meta.Agents narrowed to %v — must never narrow to [self]", healed.Agents)
	}
	// ParentBranch recovered (never silently dropped).
	if healed.ParentBranch != meta.ParentBranch {
		t.Errorf("ParentBranch: got %q, want recovered %q", healed.ParentBranch, meta.ParentBranch)
	}
}

func TestFindByGitRegistry_StalePath_FallsThroughToCreate(t *testing.T) {
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

	wtPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Make the registry entry stale: remove the worktree dir on disk (git still
	// lists it as prunable) and drop the sidecar.
	if err := os.RemoveAll(wtPath); err != nil {
		t.Fatalf("RemoveAll worktree dir: %v", err)
	}
	if err := os.Remove(metaPath(realDir, meta.ID)); err != nil {
		t.Fatalf("removing meta sidecar: %v", err)
	}

	// Direct: FindByGitRegistry must NOT reattach to the stale path.
	stale, err := FindByGitRegistry(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByGitRegistry: %v", err)
	}
	if stale != nil {
		t.Errorf("FindByGitRegistry on stale path: got %+v, want nil (validate path-on-disk before reattach)", stale)
	}

	// End-to-end: ResolveOrCreate must fall through to Create.
	path, id, outcome, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if !outcome.IsCreated() {
		t.Error("outcome.IsCreated(): got false, want true (stale registry entry → fresh Create)")
	}
	if id == meta.ID {
		t.Errorf("id: got stale %q, want a fresh worktree id", id)
	}
	if path == "" {
		t.Error("path is empty after fall-through Create")
	}
}

// --- Phase 1 (#386): worktree.Contains containment primitive ---

// TestContains exercises the pure path-containment primitive across in-bounds,
// parent/sibling escapes, the worktreeSymlinks allowlist (paths that resolve OUT
// of the worktree but are EXPECTED in-bounds — Gap 3), a not-yet-created
// candidate, and degenerate (empty/relative) inputs. It needs no git, so it
// carries no t.Skip("git not available") guard.
func TestContains(t *testing.T) {
	// Build a realistic factory layout under an EvalSymlinks'd temp root so the
	// symlink assertions are not flaky (macOS /var->/private/var; a symlinked
	// $TMPDIR). Mirrors the existing t.TempDir()+EvalSymlinks idiom.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	factory := filepath.Join(base, "factory")
	wtA := filepath.Join(factory, ".agentfactory", "worktrees", "wt-a")
	wtB := filepath.Join(factory, ".agentfactory", "worktrees", "wt-b")

	for _, d := range []string{
		filepath.Join(factory, ".runtime"),
		filepath.Join(factory, ".claude", "skills"),
		filepath.Join(wtA, "src"),
		filepath.Join(wtA, ".claude"),
		wtB,
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// An in-bounds child file that actually exists.
	childFile := filepath.Join(wtA, "src", "main.go")
	if err := os.WriteFile(childFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}

	// The worktree's .runtime and .claude/skills are symlinks that resolve OUT of
	// the worktree to the factory root (exactly what EnsureWorktreeLinks creates).
	if err := os.Symlink(filepath.Join(factory, ".runtime"), filepath.Join(wtA, ".runtime")); err != nil {
		t.Fatalf("symlink .runtime: %v", err)
	}
	if err := os.Symlink(filepath.Join(factory, ".claude", "skills"), filepath.Join(wtA, ".claude", "skills")); err != nil {
		t.Fatalf("symlink .claude/skills: %v", err)
	}
	// A real file behind the .runtime symlink so EvalSymlinks genuinely resolves
	// the candidate OUT of the worktree — proving the allowlist is load-bearing.
	if err := os.WriteFile(filepath.Join(factory, ".runtime", "state.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}

	tests := []struct {
		name      string
		boundary  string
		candidate string
		want      bool
		wantErr   bool
	}{
		{
			name:      "in-bounds child file",
			boundary:  wtA,
			candidate: childFile,
			want:      true,
		},
		{
			name:      "boundary itself is in-bounds",
			boundary:  wtA,
			candidate: wtA,
			want:      true,
		},
		{
			name:      "parent escape (candidate is the factory root)",
			boundary:  wtA,
			candidate: factory,
			want:      false,
		},
		{
			name:      "sibling-worktree escape",
			boundary:  wtA,
			candidate: wtB,
			want:      false,
		},
		{
			// Through the allowlisted .runtime symlink: EvalSymlinks resolves this
			// to factory/.runtime/state.json (OUT of wt-a), but the worktreeSymlinks
			// allowlist keeps it in-bounds (Gap 3). WITHOUT the allowlist this row is
			// false — that is what makes it the load-bearing test.
			name:      "allowlisted .runtime symlink resolves out but stays in-bounds",
			boundary:  wtA,
			candidate: filepath.Join(wtA, ".runtime", "state.json"),
			want:      true,
		},
		{
			name:      "allowlisted .claude/skills symlink stays in-bounds",
			boundary:  wtA,
			candidate: filepath.Join(wtA, ".claude", "skills", "some-skill", "SKILL.md"),
			want:      true,
		},
		{
			// A cd/Write target that does not exist yet: EvalSymlinks errors with
			// os.IsNotExist; the impl must resolve the deepest existing ancestor and
			// re-append the tail rather than collapse to a false "out of bounds".
			name:      "not-yet-created candidate under the worktree is in-bounds",
			boundary:  wtA,
			candidate: filepath.Join(wtA, "newdir", "newfile.txt"),
			want:      true,
		},
		{
			name:      "empty boundary is an error",
			boundary:  "",
			candidate: wtA,
			wantErr:   true,
		},
		{
			name:      "empty candidate is an error",
			boundary:  wtA,
			candidate: "",
			wantErr:   true,
		},
		{
			// filepath.Abs resolves a relative path against the test's cwd (the
			// package dir), which is never under a fresh t.TempDir() — so this is
			// deterministically out of bounds.
			name:      "relative candidate resolving outside is out-of-bounds",
			boundary:  wtA,
			candidate: filepath.Join("some", "relative", "path"),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Contains(tt.boundary, tt.candidate)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Contains(%q, %q): expected error, got nil (result %v)", tt.boundary, tt.candidate, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Contains(%q, %q): unexpected error: %v", tt.boundary, tt.candidate, err)
			}
			if got != tt.want {
				t.Errorf("Contains(%q, %q) = %v, want %v", tt.boundary, tt.candidate, got, tt.want)
			}
		})
	}
}

// TestIsBaseNonDivergent verifies the directional base-non-divergence ancestry
// check added for issue #401 (Phase 1). isBaseNonDivergent must distinguish a
// candidate branch that is built on top of the factory base (adoptable -> true)
// from one that has diverged from it (not adoptable -> false), and must fail
// closed (false) when the base branch is unresolvable (detached HEAD prints the
// literal "HEAD"). The base is sourced in-layer from the factory root's HEAD,
// so each sub-fixture leaves HEAD on the base branch (except the detached case).
func TestIsBaseNonDivergent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// git runs a git command in dir, failing the test on error.
	git := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// commit writes name as a file and commits it in dir.
	commit := func(t *testing.T, dir, name, msg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		git(t, dir, "add", name)
		git(t, dir, "commit", "-m", msg)
	}
	// baseBranch returns dir's current HEAD branch name (host default branch may
	// be main/master/af-*, so derive it rather than hardcoding "main").
	baseBranch := func(t *testing.T, dir string) string {
		t.Helper()
		cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("rev-parse --abbrev-ref HEAD: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	// isAncestor reports the exit status of the raw git contract, so each fixture
	// is self-proving independent of the helper under test.
	isAncestor := func(t *testing.T, dir, base, candidate string) bool {
		t.Helper()
		cmd := exec.Command("git", "merge-base", "--is-ancestor", base, candidate)
		cmd.Dir = dir
		return cmd.Run() == nil
	}

	t.Run("divergent branch is not adoptable", func(t *testing.T) {
		dir := t.TempDir()
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("eval symlinks: %v", err)
		}
		initGitRepo(t, realDir) // base branch + commit C1
		base := baseBranch(t, realDir)
		const candidate = "af/solver-div"
		git(t, realDir, "branch", candidate, "HEAD") // candidate pinned at C1; HEAD stays on base
		commit(t, realDir, "f2", "second on base")   // advance base to C2 (past candidate)

		// Fixture self-check: base (C2) is NOT an ancestor of candidate (C1).
		if isAncestor(t, realDir, base, candidate) {
			t.Fatalf("fixture invalid: base %q unexpectedly an ancestor of %q", base, candidate)
		}
		if got := isBaseNonDivergent(realDir, candidate); got != false {
			t.Errorf("isBaseNonDivergent(divergent) = %v, want false", got)
		}
	})

	t.Run("ahead-of-base branch is adoptable", func(t *testing.T) {
		dir := t.TempDir()
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("eval symlinks: %v", err)
		}
		initGitRepo(t, realDir) // base branch + commit C1
		base := baseBranch(t, realDir)
		const candidate = "af/solver-ahead"
		git(t, realDir, "checkout", "-b", candidate)   // candidate at base tip (C1)
		commit(t, realDir, "f2", "extra on candidate") // candidate advances to C2'
		git(t, realDir, "checkout", base)              // restore HEAD to base (helper reads HEAD)

		// Fixture self-check: base (C1) IS an ancestor of candidate (C2').
		if !isAncestor(t, realDir, base, candidate) {
			t.Fatalf("fixture invalid: base %q should be an ancestor of %q", base, candidate)
		}
		if got := isBaseNonDivergent(realDir, candidate); got != true {
			t.Errorf("isBaseNonDivergent(ahead) = %v, want true", got)
		}
	})

	t.Run("unresolvable base fails closed", func(t *testing.T) {
		dir := t.TempDir()
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("eval symlinks: %v", err)
		}
		initGitRepo(t, realDir)
		const candidate = "af/solver-detached"
		git(t, realDir, "branch", candidate, "HEAD")
		// Detach HEAD: `git rev-parse --abbrev-ref HEAD` now prints literal "HEAD",
		// so the base is unresolvable and the helper must fail closed.
		git(t, realDir, "checkout", "--detach", "HEAD")
		if got := isBaseNonDivergent(realDir, candidate); got != false {
			t.Errorf("isBaseNonDivergent(detached base) = %v, want false", got)
		}
	})
}

// mustRunGit runs a git command in dir, failing the test on error. Shared by the
// FindByGitRegistry_Refuses* fixtures below (Issue #401 Phase 2): they build real
// out-of-root / inconsistent-identity / divergent-base worktrees synthetically.
func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// assertGateRefused asserts the three observable effects of the isAdoptableFactoryWorktree
// gate firing on an untrustworthy reattach candidate: FindByGitRegistry returns
// (nil, nil) (fall through to Create), NO foreign wt-<suffix>.meta.json was written,
// and a WARN mentioning the failing leg landed on stderrWriter.
func assertGateRefused(t *testing.T, realDir, wtID string, meta *Meta, err error, stderr, legToken string) {
	t.Helper()
	if err != nil {
		t.Fatalf("FindByGitRegistry: unexpected error %v (want nil — refusal is the (nil,nil) fall-through)", err)
	}
	if meta != nil {
		t.Errorf("FindByGitRegistry: got meta %+v, want nil (untrustworthy candidate must not be adopted)", meta)
	}
	if _, statErr := os.Stat(metaPath(realDir, wtID)); !os.IsNotExist(statErr) {
		t.Errorf("foreign sidecar %s exists (statErr=%v), want NOT written — the gate must precede WriteMeta", metaPath(realDir, wtID), statErr)
	}
	low := strings.ToLower(stderr)
	if !strings.Contains(low, "warning") {
		t.Errorf("expected a WARN on rejection, stderr was: %q", stderr)
	}
	if !strings.Contains(low, legToken) {
		t.Errorf("expected WARN to mention %q (the failing leg), stderr was: %q", legToken, stderr)
	}
}

// TestFindByGitRegistry_RefusesOutOfRoot isolates leg 1 (containment): a single
// registry match whose path lives OUTSIDE WorktreesDir — even with a basename that
// matches its branch-derived id — must not be self-healed as factory-owned.
// Red-before-green: pre-fix the un-gated self-heal writes wt-abc123.meta.json and
// returns a non-nil meta.
func TestFindByGitRegistry_RefusesOutOfRoot(t *testing.T) {
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

	// An out-of-root location: a sibling temp dir, NOT under WorktreesDir(realDir).
	outParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks (out-of-root parent): %v", err)
	}
	const wtID = "wt-abc123" // basename intentionally == wtID so ONLY containment fails
	extPath := filepath.Join(outParent, wtID)
	branch := BranchName("solver", wtID) // af/solver-abc123
	// Register the foreign worktree in realDir's git registry (single af/solver-* match).
	mustRunGit(t, realDir, "worktree", "add", "--quiet", "-b", branch, extPath)

	// Fixture self-check: the path is genuinely outside WorktreesDir.
	if in, cerr := Contains(WorktreesDir(realDir), extPath); cerr != nil || in {
		t.Fatalf("fixture invalid: %s should be outside %s (Contains=%v err=%v)", extPath, WorktreesDir(realDir), in, cerr)
	}

	var buf bytes.Buffer
	old := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = old }()

	meta, ferr := FindByGitRegistry(realDir, "solver")
	assertGateRefused(t, realDir, wtID, meta, ferr, buf.String(), "not under")
}

// TestFindByGitRegistry_RefusesInconsistentIdentity isolates leg 2 (identity): an
// in-root worktree whose on-disk basename disagrees with its branch-derived id (the
// real-world salvage incident: branch af/solver-2e45ca → id wt-2e45ca, but a human
// renamed the on-disk dir so its basename no longer matches the id) must not be
// self-healed. Containment and non-divergence both pass; only identity fails.
func TestFindByGitRegistry_RefusesInconsistentIdentity(t *testing.T) {
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

	if err := os.MkdirAll(WorktreesDir(realDir), 0o755); err != nil {
		t.Fatalf("mkdir worktrees dir: %v", err)
	}
	const wtID = "wt-2e45ca"                                         // synthesized from branch suffix 2e45ca (6 hex)
	branch := BranchName("solver", wtID)                             // af/solver-2e45ca
	wtDir := filepath.Join(WorktreesDir(realDir), "salvaged-2e45ca") // basename != wtID
	mustRunGit(t, realDir, "worktree", "add", "--quiet", "-b", branch, wtDir)

	// Fixture self-check: in-root (containment passes) but basename inconsistent.
	if in, cerr := Contains(WorktreesDir(realDir), wtDir); cerr != nil || !in {
		t.Fatalf("fixture invalid: %s should be inside %s (Contains=%v err=%v)", wtDir, WorktreesDir(realDir), in, cerr)
	}
	if filepath.Base(wtDir) == wtID {
		t.Fatalf("fixture invalid: basename %q should differ from id %q", filepath.Base(wtDir), wtID)
	}

	var buf bytes.Buffer
	old := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = old }()

	meta, ferr := FindByGitRegistry(realDir, "solver")
	assertGateRefused(t, realDir, wtID, meta, ferr, buf.String(), "inconsistent identity")
}

// TestFindByGitRegistry_RefusesDivergentBase isolates leg 3 (base non-divergence): an
// in-root, basename-consistent worktree on a branch the factory HEAD is NOT an ancestor
// of (diverged) must not be self-healed. Containment and identity both pass.
func TestFindByGitRegistry_RefusesDivergentBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	initGitRepo(t, realDir) // base branch + commit C1
	setupFactoryRoot(t, realDir)

	if err := os.MkdirAll(WorktreesDir(realDir), 0o755); err != nil {
		t.Fatalf("mkdir worktrees dir: %v", err)
	}
	const wtID = "wt-d1f0ba"             // 6-hex suffix; basename == wtID so identity passes
	branch := BranchName("solver", wtID) // af/solver-d1f0ba
	wtDir := filepath.Join(WorktreesDir(realDir), wtID)
	// Worktree branch pinned at C1; factory HEAD stays on the base branch.
	mustRunGit(t, realDir, "worktree", "add", "--quiet", "-b", branch, wtDir)
	// Advance the factory's base branch past C1 so base is no longer an ancestor of
	// the candidate (the divergent-base condition). Commit in realDir (still on base).
	if err := os.WriteFile(filepath.Join(realDir, "advance.txt"), []byte("advance"), 0o644); err != nil {
		t.Fatalf("write advance.txt: %v", err)
	}
	mustRunGit(t, realDir, "add", "advance.txt")
	mustRunGit(t, realDir, "commit", "-m", "advance base past candidate")

	// Fixture self-check: base is NOT an ancestor of the candidate branch.
	if isBaseNonDivergent(realDir, branch) {
		t.Fatalf("fixture invalid: branch %q should be divergent from factory HEAD", branch)
	}

	var buf bytes.Buffer
	old := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = old }()

	meta, ferr := FindByGitRegistry(realDir, "solver")
	assertGateRefused(t, realDir, wtID, meta, ferr, buf.String(), "diverged")
}

// TestResolveOrCreate_RefusesPoisonedFindByOwnerMeta proves the Phase-3 choke-point
// ownership guard (CMP-1, issue #401 Six-Sigma Gap 1): a pre-existing poisoned
// *.meta.json — Owner matches the agent, but Path points OUT-OF-ROOT — is adopted
// by the FindByOwner fast paths (branch 2/3) on pre-Phase-3 code, *before*
// FindByGitRegistry's Phase-2 gate ever runs. The guard must refuse the
// non-contained candidate (WARN + fall through, never an error) and let
// ResolveOrCreate Create a fresh in-root tree instead.
func TestResolveOrCreate_RefusesPoisonedFindByOwnerMeta(t *testing.T) {
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

	// A directory OUTSIDE the factory's worktrees dir — the poisoned target.
	// EvalSymlinks-canonical so Contains (which canonicalizes) compares cleanly.
	foreign, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks (foreign): %v", err)
	}
	foreignWT := filepath.Join(foreign, "af", "solver-aaaaaa")

	// Hand-write a poisoned sidecar: Owner is the agent, but Path is out-of-root.
	// An absolute meta.Path is returned verbatim by AbsWorktreePath (issue #392),
	// so it reaches the containment check unmodified.
	poison := &Meta{
		ID:     "wt-aaaaaa",
		Owner:  "solver",
		Branch: "af/solver-aaaaaa",
		Path:   foreignWT,
		Agents: []string{"solver"},
	}
	if err := WriteMeta(realDir, poison); err != nil {
		t.Fatalf("WriteMeta(poison): %v", err)
	}

	// Sanity: pre-guard, FindByOwner resolves the poison, so branch 3 would adopt it.
	if m, ferr := FindByOwner(realDir, "solver"); ferr != nil || m == nil || m.Path != foreignWT {
		t.Fatalf("FindByOwner sanity: meta=%+v err=%v, want poison with Path=%q", m, ferr, foreignWT)
	}

	var buf bytes.Buffer
	old := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = old }()

	path, id, outcome, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v (a non-contained candidate must fall through, never error)", err)
	}

	// The poisoned out-of-root tree must NOT be adopted; a fresh in-root tree is created.
	if !outcome.IsCreated() {
		t.Errorf("outcome.IsCreated(): got false, want true (poisoned meta must be refused, fresh tree created)")
	}
	if path == foreignWT {
		t.Errorf("path: adopted the poisoned out-of-root path %q; want a fresh in-root tree", path)
	}
	if id == poison.ID {
		t.Errorf("id: got poisoned id %q; want a fresh id", id)
	}
	if in, cerr := Contains(WorktreesDir(realDir), path); cerr != nil || !in {
		t.Errorf("returned path %q is not under WorktreesDir(%q) (in=%v err=%v)",
			path, WorktreesDir(realDir), in, cerr)
	}
}

// --- Phase 4 (#401 CMP-5): behavioral proof via the PUBLIC entry points, the
// drift interlock (Gap 5), and the AC-4-positive regression net (Gap 8). ---

// TestResolveOrCreate_RefusesOutOfRootCandidate is the PUBLIC ResolveOrCreate
// end-to-end analogue of TestFindByGitRegistry_RefusesOutOfRoot (which drives
// FindByGitRegistry directly, leg-1 isolation). An external worktree registered
// OUTSIDE WorktreesDir — with NO sidecar — must not be adopted on reattach:
// ResolveOrCreate falls through to Create a fresh in-root tree, writing no foreign
// sidecar. Red-before-green: against pre-#401 worktree.go the un-gated
// FindByGitRegistry self-heals the foreign tree and ResolveOrCreate reattaches to it
// (IsCreated()==false, path==foreign). (Maps to AC-1 / AC-5(i).)
func TestResolveOrCreate_RefusesOutOfRootCandidate(t *testing.T) {
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

	// An out-of-root location: a sibling temp dir, NOT under WorktreesDir(realDir).
	outParent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks (out-of-root parent): %v", err)
	}
	const wtID = "wt-abc123" // basename intentionally == wtID so ONLY containment fails
	extPath := filepath.Join(outParent, wtID)
	branch := BranchName("solver", wtID) // af/solver-abc123
	mustRunGit(t, realDir, "worktree", "add", "--quiet", "-b", branch, extPath)

	// Fixture self-checks: genuinely out-of-root, and no sidecar exists yet so the
	// reattach decision must come from the git registry (the gated path).
	if in, cerr := Contains(WorktreesDir(realDir), extPath); cerr != nil || in {
		t.Fatalf("fixture invalid: %s should be outside %s (Contains=%v err=%v)", extPath, WorktreesDir(realDir), in, cerr)
	}
	if m, ferr := FindByOwner(realDir, "solver"); ferr != nil || m != nil {
		t.Fatalf("fixture invalid: FindByOwner should be nil/nil pre-reattach, got meta=%v err=%v", m, ferr)
	}

	var buf bytes.Buffer
	old := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = old }()

	path, id, outcome, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v (a non-contained candidate must fall through, never error)", err)
	}
	// The foreign out-of-root tree must NOT be adopted; a fresh in-root tree is created.
	if !outcome.IsCreated() {
		t.Errorf("outcome.IsCreated(): got false, want true (out-of-root candidate must be refused, fresh tree created)")
	}
	if path == extPath {
		t.Errorf("path: adopted the foreign out-of-root path %q; want a fresh in-root tree", path)
	}
	if in, cerr := Contains(WorktreesDir(realDir), path); cerr != nil || !in {
		t.Errorf("returned path %q is not under WorktreesDir(%q) (in=%v err=%v)",
			path, WorktreesDir(realDir), in, cerr)
	}
	if id == wtID {
		t.Errorf("id: got foreign id %q; want a fresh id", id)
	}
	// No foreign sidecar was written for the out-of-root tree (the gate precedes WriteMeta).
	if _, statErr := os.Stat(metaPath(realDir, wtID)); !os.IsNotExist(statErr) {
		t.Errorf("foreign sidecar %s exists (statErr=%v), want NOT written", metaPath(realDir, wtID), statErr)
	}
	// The refusal is observable: a WARN naming the failing (containment) leg.
	if low := strings.ToLower(buf.String()); !strings.Contains(low, "not under") {
		t.Errorf("expected a WARN mentioning the containment leg (\"not under\"), stderr was: %q", buf.String())
	}
}

// TestFindByGitRegistry_AcceptsMissingOwnerMarkerWhenConsistent guards against
// OVER-rejection (AC-4 co-tenant preservation / AC-5(ii)): an in-root,
// basename-consistent, non-divergent worktree that has NO .runtime/worktree_owner
// marker file must still be ADOPTED. The owner-marker leg is satisfied STRUCTURALLY
// (containment + basename + non-divergence); the predicate never reads the
// attacker-controllable worktree_owner file (security D5-2 REJECTED). This is the
// positive inverse of assertGateRefused — adoption must SUCCEED.
func TestFindByGitRegistry_AcceptsMissingOwnerMarkerWhenConsistent(t *testing.T) {
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

	if err := os.MkdirAll(WorktreesDir(realDir), 0o755); err != nil {
		t.Fatalf("mkdir worktrees dir: %v", err)
	}
	const wtID = "wt-c0ffee"             // 6-hex suffix; basename == wtID so identity passes
	branch := BranchName("solver", wtID) // af/solver-c0ffee
	wtDir := filepath.Join(WorktreesDir(realDir), wtID)
	// In-root, basename-consistent, and (no base advance) non-divergent. Built via raw
	// `git worktree add`, so SetupAgent never runs and NO .runtime/worktree_owner exists.
	mustRunGit(t, realDir, "worktree", "add", "--quiet", "-b", branch, wtDir)

	// Fixture self-checks: all three structural legs pass, and the marker is absent.
	if in, cerr := Contains(WorktreesDir(realDir), wtDir); cerr != nil || !in {
		t.Fatalf("fixture invalid: %s should be inside %s (Contains=%v err=%v)", wtDir, WorktreesDir(realDir), in, cerr)
	}
	if filepath.Base(wtDir) != wtID {
		t.Fatalf("fixture invalid: basename %q should equal id %q (identity leg must pass)", filepath.Base(wtDir), wtID)
	}
	if !isBaseNonDivergent(realDir, branch) {
		t.Fatalf("fixture invalid: branch %q should be non-divergent from factory HEAD", branch)
	}
	markerPath := filepath.Join(wtDir, ".agentfactory", "agents", "solver", ".runtime", "worktree_owner")
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("fixture invalid: worktree_owner marker %s unexpectedly exists (statErr=%v); the missing-marker condition is the point of this test", markerPath, statErr)
	}

	var buf bytes.Buffer
	old := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = old }()

	meta, ferr := FindByGitRegistry(realDir, "solver")
	if ferr != nil {
		t.Fatalf("FindByGitRegistry: %v", ferr)
	}
	if meta == nil {
		t.Fatalf("FindByGitRegistry returned nil; a consistent in-root tree with no owner-marker "+
			"must be ADOPTED, not refused for the missing marker. stderr: %q", buf.String())
	}
	if meta.Owner != "solver" {
		t.Errorf("meta.Owner: got %q, want %q", meta.Owner, "solver")
	}
	if meta.ID != wtID {
		t.Errorf("meta.ID: got %q, want %q", meta.ID, wtID)
	}
	// Adoption self-heals the sidecar (the success counterpart to assertGateRefused's
	// "no foreign sidecar" assertion).
	if _, statErr := os.Stat(metaPath(realDir, wtID)); statErr != nil {
		t.Errorf("sidecar %s not written after adoption: %v", metaPath(realDir, wtID), statErr)
	}
}

// TestResolveOrCreate_AcceptsInRootLostSidecar is the AC-4 positive guard (the
// design-doc's named "new positive guard"): a legitimate in-root worktree whose
// non-durable sidecar was lost still REATTACHES (never forks). Behaviour is
// identical to TestResolveOrCreate_ReattachesViaGitRegistry_WhenMetaMissing, which
// it is modeled on; both are intentionally kept (the design-doc lists this name
// explicitly as the AC-4-positive regression anchor).
func TestResolveOrCreate_AcceptsInRootLostSidecar(t *testing.T) {
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

	wtPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Fixture self-check: the created tree is genuinely in-root (legitimate co-tenant).
	if in, cerr := Contains(WorktreesDir(realDir), wtPath); cerr != nil || !in {
		t.Fatalf("fixture invalid: created path %q should be under %s (Contains=%v err=%v)", wtPath, WorktreesDir(realDir), in, cerr)
	}

	// Lose the non-durable sidecar (the #392 condition this positive guard protects).
	if err := os.Remove(metaPath(realDir, meta.ID)); err != nil {
		t.Fatalf("removing meta sidecar: %v", err)
	}
	if m, err := FindByOwner(realDir, "solver"); err != nil || m != nil {
		t.Fatalf("FindByOwner after sidecar delete: meta=%v err=%v, want nil/nil", m, err)
	}

	path, id, outcome, err := ResolveOrCreate(realDir, "solver", "", "", "", CreateOpts{})
	if err != nil {
		t.Fatalf("ResolveOrCreate: %v", err)
	}
	if outcome.IsCreated() {
		t.Error("outcome.IsCreated(): got true, want false (legitimate in-root tree must reattach, not fork)")
	}
	if path != wtPath {
		t.Errorf("path: got %q, want %q (reattach to existing in-root worktree)", path, wtPath)
	}
	if id != meta.ID {
		t.Errorf("id: got %q, want %q (reattach to existing in-root worktree)", id, meta.ID)
	}

	// Self-heal: the sidecar is rewritten from the git registry.
	healed, err := ReadMeta(realDir, meta.ID)
	if err != nil {
		t.Fatalf("ReadMeta after self-heal: %v", err)
	}
	if healed.Owner != "solver" {
		t.Errorf("healed meta.Owner: got %q, want %q", healed.Owner, "solver")
	}

	// No fork: exactly one af/solver-* worktree and branch.
	if n := countAfWorktrees(t, realDir, "solver"); n != 1 {
		t.Errorf("af/solver-* worktrees: got %d, want 1 (reattach must not fork)", n)
	}
	if n := countAfBranches(t, realDir, "solver"); n != 1 {
		t.Errorf("af/solver-* branches: got %d, want 1 (reattach must not fork)", n)
	}
}

// TestDriftGuard_GenerateIDSuffixIsWorktreeSuffix is the Gap-5 drift interlock. It
// pins the cross-symbol contract that GenerateID's appended suffix is ALWAYS accepted
// by isWorktreeSuffix. The CMP-1 containment (basename == id) and CMP-2 identity legs
// both depend on this equivalence; TestGenerateID checks the wt-[0-9a-f]{6} shape but
// NOT this isWorktreeSuffix<->GenerateID invariant, so without this guard a change to
// either symbol could silently desynchronize them and let the identity leg rot. The
// test is white-box (package worktree) so both the exported and unexported symbols are
// in scope. Named with "DriftGuard" so the AC-1 `-run 'Refuses|Accepts|DriftGuard'`
// filter catches it.
func TestDriftGuard_GenerateIDSuffixIsWorktreeSuffix(t *testing.T) {
	// GenerateID is randomized; assert the invariant across many draws, not one.
	for i := 0; i < 256; i++ {
		id := GenerateID()
		suffix := strings.TrimPrefix(id, "wt-")
		if suffix == id {
			t.Fatalf("GenerateID() = %q lacks the %q prefix; the basename<->id contract is broken", id, "wt-")
		}
		if !isWorktreeSuffix(suffix) {
			t.Fatalf("isWorktreeSuffix(strings.TrimPrefix(GenerateID(), %q)) = false for id %q; "+
				"the id<->suffix contract drifted (identity/containment legs would silently rot)", "wt-", id)
		}
	}
}

// TestMeta_FactoryRoot_RoundTrip (K9a, #519 Phase 3) pins that the durable
// factory-root provenance survives a WriteMeta/ReadMeta round-trip.
func TestMeta_FactoryRoot_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	original := &Meta{
		ID:          "wt-provroot",
		Owner:       "solver",
		Branch:      "af/solver-provroot",
		Path:        ".agentfactory/worktrees/wt-provroot",
		Agents:      []string{"solver"},
		CreatedAt:   "2026-07-07T00:00:00Z",
		FactoryRoot: "/some/factory/root",
	}
	if err := WriteMeta(realDir, original); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	got, err := ReadMeta(realDir, "wt-provroot")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if got.FactoryRoot != "/some/factory/root" {
		t.Errorf("FactoryRoot: got %q, want %q", got.FactoryRoot, "/some/factory/root")
	}
}

// TestReadMeta_LegacyMetaZeroValuesFactoryRoot (K9a, #519 Phase 3) proves the new
// field is migration-safe: a meta.json written before the field existed unmarshals
// with FactoryRoot == "" and no error — no migration needed.
func TestReadMeta_LegacyMetaZeroValuesFactoryRoot(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if err := os.MkdirAll(WorktreesDir(realDir), 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	legacy := `{"id":"wt-legacy","owner":"solver","branch":"af/solver-legacy","path":".agentfactory/worktrees/wt-legacy","agents":["solver"],"created_at":"2026-01-01T00:00:00Z","parent_branch":"main"}`
	if err := os.WriteFile(metaPath(realDir, "wt-legacy"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy meta: %v", err)
	}
	got, err := ReadMeta(realDir, "wt-legacy")
	if err != nil {
		t.Fatalf("ReadMeta on legacy meta must not error: %v", err)
	}
	if got.FactoryRoot != "" {
		t.Errorf("legacy FactoryRoot: got %q, want empty (migration-safe)", got.FactoryRoot)
	}
}
