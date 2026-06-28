//go:build integration

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// TestInstallInit_CreatesDispatchJson exercises `af install --init` end-to-end,
// which spawns the Python MCP server via mcpstore.New. It is tagged
// `integration` so it does not run under `make test` (no python3 + server
// deps guaranteed). The rest of the install test suite stays in the unit tier
// and tests the filesystem/template paths that do not need the server.
func TestInstallInit_CreatesDispatchJson(t *testing.T) {
	requirePython3WithServerDeps(t)

	dir := t.TempDir()
	ensurePySymlink(t, dir)
	t.Cleanup(func() { terminateMCPServer(dir) })

	// git init — kept for repo-parity. mcpstore does not require git.
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.test"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// Deterministic discovery (issue #73 K2): this temp repo has no remote, so the
	// dispatch default degrades to the empty shape. Stub the seam so the assertion never
	// depends on the CI host's gh auth / ambient remote.
	origDetect := runGitDetect
	runGitDetect = func(workDir, name string, args ...string) string { return "" }
	t.Cleanup(func() { runGitDetect = origDetect })

	output, err := runInstallInDir(t, dir, "--init")
	// Reset flag to avoid affecting subsequent tests (cobra doesn't reset bool flags)
	installInitFlag = false
	if err != nil {
		t.Fatalf("install --init failed: %v\nOutput: %s", err, output)
	}

	// Verify dispatch.json was created
	dispatchPath := filepath.Join(dir, ".agentfactory", "dispatch.json")
	data, err := os.ReadFile(dispatchPath)
	if err != nil {
		t.Fatalf("dispatch.json not created: %v", err)
	}

	// Verify valid JSON with expected structure
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("dispatch.json is not valid JSON: %v", err)
	}

	if cfg["trigger_label"] != "agentic" {
		t.Errorf("trigger_label should be 'agentic', got: %v", cfg["trigger_label"])
	}
	repos, ok := cfg["repos"].([]interface{})
	if !ok || len(repos) != 0 {
		t.Errorf("repos should be empty array (no remote discovered), got: %v", cfg["repos"])
	}
	mappings, ok := cfg["mappings"].([]interface{})
	if !ok || len(mappings) != 0 {
		t.Errorf("mappings should be empty array (degraded default), got: %v", cfg["mappings"])
	}
	if interval, ok := cfg["interval_seconds"].(float64); !ok || int(interval) != 300 {
		t.Errorf("interval_seconds should be 300, got: %v", cfg["interval_seconds"])
	}
	if retry, ok := cfg["retry_after_seconds"].(float64); !ok || int(retry) != 1800 {
		t.Errorf("retry_after_seconds should be 1800, got: %v", cfg["retry_after_seconds"])
	}
	// notify_on_complete is now OMITTED from the default (issue #73 Gap-7): it defaults to
	// "manager" at runtime, so an explicit value would add a brittle cross-file check.
	if _, present := cfg["notify_on_complete"]; present {
		t.Errorf("notify_on_complete should be omitted from the default, got: %v", cfg["notify_on_complete"])
	}
}

// TestInstallInit_PopulatedDispatchAndAgents_WithDiscoveredRepo exercises issue #73's
// happy path end-to-end: with a discoverable repo, `af install --init` bakes the
// owner/name into dispatch.json's repos AND ships the four label->agent mappings +
// feature-workflow, seeds the four specialists into agents.json, and the result is
// valid-by-construction (the default cross-validates against the seeded agents — C1/C-6).
// A re-run does not clobber. Discovery is stubbed (in-process install shares the package
// seam) so the assertion is deterministic regardless of the CI host's auth/remote.
func TestInstallInit_PopulatedDispatchAndAgents_WithDiscoveredRepo(t *testing.T) {
	requirePython3WithServerDeps(t)

	dir := t.TempDir()
	ensurePySymlink(t, dir)
	t.Cleanup(func() { terminateMCPServer(dir) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.test"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	origDetect := runGitDetect
	runGitDetect = func(workDir, name string, args ...string) string {
		if name == "gh" {
			return "acme/widget"
		}
		return ""
	}
	t.Cleanup(func() { runGitDetect = origDetect })

	output, err := runInstallInDir(t, dir, "--init")
	installInitFlag = false
	if err != nil {
		t.Fatalf("install --init failed: %v\nOutput: %s", err, output)
	}

	dispatchPath := filepath.Join(dir, ".agentfactory", "dispatch.json")
	dispData, err := os.ReadFile(dispatchPath)
	if err != nil {
		t.Fatalf("read dispatch.json: %v", err)
	}
	var disp config.DispatchConfig
	if err := json.Unmarshal(dispData, &disp); err != nil {
		t.Fatalf("dispatch.json invalid: %v", err)
	}
	if len(disp.Repos) != 1 || disp.Repos[0] != "acme/widget" {
		t.Errorf("repos = %v, want [acme/widget]", disp.Repos)
	}
	if len(disp.Mappings) != 4 {
		t.Errorf("mappings = %d, want 4", len(disp.Mappings))
	}
	if len(disp.Workflows) != 1 || disp.Workflows[0].Label != "feature-workflow" {
		t.Errorf("workflows = %v, want one feature-workflow", disp.Workflows)
	}
	if strings.Contains(string(dispData), "notify_on_complete") {
		t.Errorf("notify_on_complete should be omitted; got: %s", dispData)
	}

	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("read agents.json: %v", err)
	}
	var agents config.AgentConfig
	if err := json.Unmarshal(agentsData, &agents); err != nil {
		t.Fatalf("agents.json invalid: %v", err)
	}
	for _, name := range []string{"manager", "supervisor", "rapid-soldesign-plan", "rapid-implement", "ultra-review", "rapid-increment"} {
		if _, ok := agents.Agents[name]; !ok {
			t.Errorf("agents.json missing seeded agent %q", name)
		}
	}
	// C1/C-6: the shipped default is valid-by-construction against the seeded agents.
	if err := config.ValidateDispatchConfig(&disp, &agents); err != nil {
		t.Errorf("default dispatch must cross-validate against seeded agents.json: %v", err)
	}

	// Idempotent (ADR-017 write-if-absent): a re-run must not clobber the populated files.
	before, _ := os.ReadFile(dispatchPath)
	if _, err := runInstallInDir(t, dir, "--init"); err != nil {
		installInitFlag = false
		t.Fatalf("second install --init failed: %v", err)
	}
	installInitFlag = false
	after, _ := os.ReadFile(dispatchPath)
	if string(before) != string(after) {
		t.Errorf("re-run clobbered dispatch.json:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestInstallInit_AgentsMdRelocation_Behavioral is the behavioral half of issue
// #305 (relocating the agent roster from ./AGENTS.md to ./.agentfactory/AGENTS.md).
// Phases 1 and 2 re-pathed every writer and reader; their tests are structural
// (grep / os.Stat / symlink-target). This test proves the three load-bearing
// acceptance criteria end-to-end against a real git-init'd repo:
//
//   - AC-1: the roster lands at .agentfactory/AGENTS.md (config.AgentsMdPath) with
//     the BEGIN/END block and the agent table.
//   - AC-2: `af install --init` leaves a *tracked* root AGENTS.md byte-unchanged
//     (a content/tracked-file check via `git diff --exit-code AGENTS.md`, NOT an
//     os.Stat existence check — .git/info/exclude only hides *untracked* files, so a
//     mutated tracked file would still show dirty), and creates none when absent.
//   - AC-3: a manager-style worktree resolves the roster via the
//     .agentfactory/AGENTS.md symlink.
//
// It deliberately does NOT attempt to prove AC-4/AC-5 (whole-system autonomy):
// `af sling --agent` dispatches off agents.json, not AGENTS.md, so those stay
// no-regression smoke covered by the existing suites (design-doc.md L255-267).
func TestInstallInit_AgentsMdRelocation_Behavioral(t *testing.T) {
	requirePython3WithServerDeps(t)

	// gitRun runs a git subcommand in dir, failing the test on error.
	gitRun := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitInit := func(t *testing.T, dir string) {
		t.Helper()
		gitRun(t, dir, "init", "-q")
		gitRun(t, dir, "config", "user.email", "test@test.test")
		gitRun(t, dir, "config", "user.name", "Test")
	}
	// commitRootAgentsMd writes a root AGENTS.md fixture and git add + commit so it
	// is a *tracked* file — the only meaningful subject for the AC-2 git-clean proof.
	commitRootAgentsMd := func(t *testing.T, dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write root AGENTS.md fixture: %v", err)
		}
		gitRun(t, dir, "add", "AGENTS.md")
		gitRun(t, dir, "commit", "-q", "-m", "fixture: tracked root AGENTS.md")
	}

	// AC-2 proof: `git diff --exit-code AGENTS.md` must report no changes to the
	// tracked host file after install.
	assertRootAgentsMdClean := func(t *testing.T, dir string) {
		t.Helper()
		cmd := exec.Command("git", "diff", "--exit-code", "--", "AGENTS.md")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("AC-2: `git diff --exit-code AGENTS.md` reported the tracked root "+
				"AGENTS.md changed — install must never mutate the host file: %v\n%s", err, out)
		}
	}

	// AC-1 proof: the roster lives at .agentfactory/AGENTS.md with the block + table.
	assertRosterRelocated := func(t *testing.T, dir string) {
		t.Helper()
		rosterPath := config.AgentsMdPath(dir)
		data, err := os.ReadFile(rosterPath)
		if err != nil {
			t.Fatalf("AC-1: roster not found at relocated path %s: %v", rosterPath, err)
		}
		content := string(data)
		for _, want := range []string{agentsMdBegin, agentsMdEnd, "| Agent | Type | Description |"} {
			if !strings.Contains(content, want) {
				t.Errorf("AC-1: roster at %s missing %q", rosterPath, want)
			}
		}
	}

	// installInit runs `af install --init` in dir via the in-process command and
	// resets the cobra bool flag afterward (cobra does not reset it between tests).
	installInit := func(t *testing.T, dir string) {
		t.Helper()
		output, err := runInstallInDir(t, dir, "--init")
		installInitFlag = false
		if err != nil {
			t.Fatalf("install --init failed: %v\nOutput: %s", err, output)
		}
	}

	t.Run("AC1_AC2_TrackedRootNoMarkers", func(t *testing.T) {
		dir := t.TempDir()
		ensurePySymlink(t, dir)
		t.Cleanup(func() { terminateMCPServer(dir) })
		gitInit(t, dir)
		commitRootAgentsMd(t, dir, "# Host Project Agents\n\nThis is the host project's own roster.\n")

		installInit(t, dir)

		assertRootAgentsMdClean(t, dir) // AC-2: tracked host file untouched
		assertRosterRelocated(t, dir)   // AC-1: roster at the new path with the table
	})

	t.Run("AC2_TrackedRootWithAFBlock", func(t *testing.T) {
		dir := t.TempDir()
		ensurePySymlink(t, dir)
		t.Cleanup(func() { terminateMCPServer(dir) })
		gitInit(t, dir)
		// A host file that ALREADY contains an AgentFactory block (e.g. committed by a
		// pre-relocation install in a prior life). Install must still leave it untouched
		// — it now only ever writes .agentfactory/AGENTS.md (leave-and-stop, data.md D2.4).
		withBlock := "# Host Project Agents\n\n" +
			agentsMdBegin + "\n\nstale roster body\n\n" + agentsMdEnd + "\n\nMore host content.\n"
		commitRootAgentsMd(t, dir, withBlock)

		installInit(t, dir)

		assertRootAgentsMdClean(t, dir) // AC-2: the host block is NOT rewritten
		assertRosterRelocated(t, dir)   // AC-1: the fresh roster lives at the new path
	})

	t.Run("AC2_NoRootFile_NoneCreated", func(t *testing.T) {
		dir := t.TempDir()
		ensurePySymlink(t, dir)
		t.Cleanup(func() { terminateMCPServer(dir) })
		gitInit(t, dir)
		// Intentionally NO root AGENTS.md.

		installInit(t, dir)

		if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
			t.Errorf("AC-2: install created a root AGENTS.md when none existed (stat err=%v); "+
				"the roster must live only at .agentfactory/AGENTS.md", err)
		}
		assertRosterRelocated(t, dir) // AC-1
	})

	t.Run("AC3_WorktreeResolvesRelocatedRoster", func(t *testing.T) {
		// AC-3 uses a REAL binary + the worktree Go API (mirrors
		// TestWorktreeLifecycle_SymlinksAndTeardown). A second `--init` inside a
		// worktree is forbidden by the install guard (install.go:69-70), so resolution
		// is proven through the worktree symlink, not a nested install.
		binary := buildAF(t)
		workspace := t.TempDir()
		realWorkspace, err := filepath.EvalSymlinks(workspace)
		if err != nil {
			t.Fatalf("eval symlinks on workspace: %v", err)
		}
		ensurePySymlink(t, realWorkspace)
		t.Cleanup(func() { terminateMCPServer(realWorkspace) })

		gitInit(t, realWorkspace)
		if err := os.WriteFile(filepath.Join(realWorkspace, ".gitignore"), []byte(".agentfactory/\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		gitRun(t, realWorkspace, "add", ".gitignore")
		gitRun(t, realWorkspace, "commit", "-q", "-m", "initial with gitignore")

		// Real-binary install writes the relocated roster to .agentfactory/AGENTS.md.
		runAF(t, binary, realWorkspace, "install", "--init")
		assertRosterRelocated(t, realWorkspace)

		// agents.json with a dispatchable specialist so a worktree can be created for it.
		agentsPath := filepath.Join(realWorkspace, ".agentfactory", "agents.json")
		if err := os.WriteFile(agentsPath, []byte(`{"agents":{"manager":{"type":"interactive","description":"manager"},"solver":{"type":"autonomous","description":"solver agent"}}}`), 0o644); err != nil {
			t.Fatalf("write agents.json: %v", err)
		}
		// Factory-root resources the worktree symlinks target (mirror).
		os.MkdirAll(filepath.Join(realWorkspace, ".claude", "skills"), 0o755)
		os.MkdirAll(filepath.Join(realWorkspace, ".runtime"), 0o755)

		wtPath, meta, err := worktree.Create(realWorkspace, "solver", worktree.CreateOpts{})
		if err != nil {
			t.Fatalf("worktree.Create: %v", err)
		}
		removed := false
		t.Cleanup(func() {
			if !removed {
				_ = worktree.ForceRemove(realWorkspace, meta)
			}
		})

		rel := filepath.Join(".agentfactory", "AGENTS.md")

		// AC-3 (structural): the worktree's roster symlink targets the factory root's roster.
		target, err := os.Readlink(filepath.Join(wtPath, rel))
		if err != nil {
			t.Fatalf("AC-3: readlink %s: %v", rel, err)
		}
		if expected := filepath.Join(realWorkspace, rel); target != expected {
			t.Errorf("AC-3: roster symlink target = %q, want %q", target, expected)
		}

		// AC-3 (behavioral): reading the roster THROUGH the worktree symlink resolves the
		// real roster content — what a manager sitting in the worktree actually sees.
		data, err := os.ReadFile(filepath.Join(wtPath, rel))
		if err != nil {
			t.Fatalf("AC-3: reading roster through worktree symlink: %v", err)
		}
		content := string(data)
		for _, want := range []string{agentsMdBegin, "| Agent | Type | Description |"} {
			if !strings.Contains(content, want) {
				t.Errorf("AC-3: roster resolved through worktree symlink missing %q", want)
			}
		}

		// Provision the agent (mirror the Create/SetupAgent path).
		if _, err := worktree.SetupAgent(realWorkspace, wtPath, "solver", true); err != nil {
			t.Fatalf("worktree.SetupAgent: %v", err)
		}

		// Teardown leaves the factory-root roster intact.
		if err := worktree.ForceRemove(realWorkspace, meta); err != nil {
			t.Fatalf("worktree.ForceRemove: %v", err)
		}
		removed = true
		if _, err := os.Stat(config.AgentsMdPath(realWorkspace)); err != nil {
			t.Errorf("AC-3: factory-root roster missing after worktree teardown: %v", err)
		}
	})
}
