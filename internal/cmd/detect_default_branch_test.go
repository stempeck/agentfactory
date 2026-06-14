package cmd

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

// installRunGitDetect swaps the inner git/gh runner seam (runGitDetect) with a
// canned-output stub for the lifetime of the test, mirroring installMemStore.
// m1/m2/m3 are the outputs for detection methods 1 (symbolic-ref), 2 (ls-remote)
// and 3 (gh repo view) respectively; "" means that method "failed".
func installRunGitDetect(t *testing.T, m1, m2, m3 string) {
	t.Helper()
	orig := runGitDetect
	runGitDetect = func(workDir, name string, args ...string) string {
		switch {
		case name == "git" && len(args) > 0 && args[0] == "symbolic-ref":
			return m1
		case name == "git" && len(args) > 0 && args[0] == "ls-remote":
			return m2
		case name == "gh":
			return m3
		default:
			return ""
		}
	}
	t.Cleanup(func() { runGitDetect = orig })
}

// installDetectBranch swaps the detectDefaultBranch seam itself (used by the
// sling-level injection tests), mirroring installMemStore / installNoopLaunchSession.
func installDetectBranch(t *testing.T, val string) {
	t.Helper()
	orig := detectDefaultBranch
	detectDefaultBranch = func(string) string { return val }
	t.Cleanup(func() { detectDefaultBranch = orig })
}

func TestDetectDefaultBranch_IsValidBranchName(t *testing.T) {
	valid := []string{"main", "master", "trunk", "develop", "feature/x", "release-1.2.3", "a.b_c/d-e"}
	for _, s := range valid {
		if !isValidBranchName(s) {
			t.Errorf("isValidBranchName(%q) = false, want true", s)
		}
	}
	// The allowlist is the frozen contract `^[A-Za-z0-9._/-]+$` (IMPLREADME L153 /
	// design-doc K2). Its job is to block shell metacharacters baked into
	// agent-executed text — spaces, ';', '$', '|', '&', backticks, control chars,
	// non-ASCII. (Note: "." is allowed, so ".." passes — it carries no shell
	// metacharacter and is not an injection vector.)
	invalid := []string{"", "-rf", "-", "a b", "foo;rm -rf /", "$(whoami)", "a$b", "a|b", "a&b", "a\tb", "a\nb", "héllo"}
	for _, s := range invalid {
		if isValidBranchName(s) {
			t.Errorf("isValidBranchName(%q) = true, want false", s)
		}
	}
}

func TestDetectDefaultBranch_Chain(t *testing.T) {
	cases := []struct {
		name       string
		m1, m2, m3 string
		want       string
	}{
		{name: "method1_master", m1: "origin/master", want: "master"},
		{name: "method1_main", m1: "origin/main", want: "main"},
		{name: "method1_strips_origin_prefix_slash", m1: "origin/feature/x", want: "feature/x"},
		{name: "method1_invalid_falls_to_method2", m1: "origin/-evil", m2: "ref: refs/heads/develop\tHEAD", want: "develop"},
		{name: "method2_lsremote_parse", m2: "ref: refs/heads/trunk\tHEAD", want: "trunk"},
		{name: "method2_lsremote_with_sha_line", m2: "ref: refs/heads/main\tHEAD\n6c26740\tHEAD", want: "main"},
		{name: "method2_garbage_falls_to_method3", m2: "no symref line here", m3: "release", want: "release"},
		{name: "method3_gh", m3: "main", want: "main"},
		{name: "method3_gh_invalid_rejected", m3: "bad branch name", want: ""},
		{name: "all_empty_returns_empty", want: ""},
		{name: "method1_empty_value_falls_through", m1: "origin/", m2: "ref: refs/heads/master\tHEAD", want: "master"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installRunGitDetect(t, tc.m1, tc.m2, tc.m3)
			if got := detectDefaultBranch(t.TempDir()); got != tc.want {
				t.Errorf("detectDefaultBranch = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDetectDefaultBranch_LocalOriginHead is the hermetic real-git test: it builds
// a temp repo, sets origin/HEAD locally (no network), and proves method 1's actual
// `git symbolic-ref` invocation + origin/ stripping work end-to-end.
func TestDetectDefaultBranch_LocalOriginHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, branch := range []string{"master", "main"} {
		t.Run(branch, func(t *testing.T) {
			dir := t.TempDir()
			gitTestCmd(t, dir, "init", "-q", ".")
			gitTestCmd(t, dir, "config", "user.email", "t@t")
			gitTestCmd(t, dir, "config", "user.name", "t")
			gitTestCmd(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
			gitTestCmd(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
			if got := detectDefaultBranch(dir); got != branch {
				t.Errorf("detectDefaultBranch = %q, want %q", got, branch)
			}
		})
	}
}

// TestDetectDefaultBranch_LsRemoteFallback proves method 1 → method 2 fallthrough
// against real git output: origin/HEAD is NOT set locally, so detection must fall
// to `ls-remote --symref` against a local (offline) bare remote.
func TestDetectDefaultBranch_LsRemoteFallback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	work := t.TempDir()
	gitTestCmd(t, work, "init", "-q", ".")
	gitTestCmd(t, work, "config", "user.email", "t@t")
	gitTestCmd(t, work, "config", "user.name", "t")
	gitTestCmd(t, work, "commit", "-q", "--allow-empty", "-m", "init")
	gitTestCmd(t, work, "branch", "-m", "trunk")

	bare := t.TempDir()
	gitTestCmd(t, bare, "init", "-q", "--bare", ".")
	gitTestCmd(t, work, "remote", "add", "origin", bare)
	gitTestCmd(t, work, "push", "-q", "origin", "trunk")
	gitTestCmd(t, bare, "symbolic-ref", "HEAD", "refs/heads/trunk")

	// origin/HEAD is unset locally → method 1 fails → method 2 (ls-remote) resolves.
	if got := detectDefaultBranch(work); got != "trunk" {
		t.Errorf("detectDefaultBranch = %q, want %q (ls-remote fallback)", got, "trunk")
	}
}

func gitTestCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestDetectDefaultBranch_MasterRepoEndToEnd is the hermetic end-to-end proof (K8 /
// six_sigma_gaps Gap 8): on a real temp repo whose default branch is `master`, the
// REAL detectDefaultBranch (Phase 1) composed with expandStepVars (Phase 2
// tokenization) bakes step text that resolves to origin/master and --base master —
// never origin/main. This closes the structural-only gap: a green unit suite + green
// drift suite could otherwise all pass while the integrated detect→expand flow still
// produced `main` on a master repo. It uses the REAL detectDefaultBranch var, NOT the
// installDetectBranch stub, and is git-only (no tmux/Python) so it runs in the CI
// `unit` tier; guarded by exec.LookPath("git") like its siblings.
func TestDetectDefaultBranch_MasterRepoEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Build a temp repo whose origin/HEAD points at master (mirrors
	// TestDetectDefaultBranch_LocalOriginHead's local-origin/HEAD setup, master only).
	dir := t.TempDir()
	gitTestCmd(t, dir, "init", "-q", ".")
	gitTestCmd(t, dir, "config", "user.email", "t@t")
	gitTestCmd(t, dir, "config", "user.name", "t")
	gitTestCmd(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	gitTestCmd(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/master")

	// Detection half — the REAL detectDefaultBranch (not the stub).
	got := detectDefaultBranch(dir)
	if got != "master" {
		t.Fatalf("detectDefaultBranch = %q, want %q", got, "master")
	}

	// Tokenization half — parse the small formula and expand with the detected branch,
	// exactly as instantiateFormulaWorkflow does (inject default_branch, then
	// expandStepVars — sling.go:495-513).
	f, err := formula.Parse([]byte(defaultBranchFormulaTOML))
	if err != nil {
		t.Fatalf("parse formula: %v", err)
	}
	expandStepVars(f, map[string]string{"default_branch": got})

	step := f.Steps[0]
	baked := step.Title + "\n" + step.Description
	if !strings.Contains(step.Title, "origin/master") {
		t.Errorf("step title should bake to origin/master, got %q", step.Title)
	}
	if strings.Contains(baked, "origin/main") {
		t.Errorf("baked step text must not contain origin/main on a master repo, got %q", baked)
	}
	if !strings.Contains(step.Description, "--base master") {
		t.Errorf("PR-base token should resolve to --base master, got %q", step.Description)
	}
	if strings.Contains(baked, "{{default_branch}}") {
		t.Errorf("token left unexpanded after expandStepVars: %q", baked)
	}
}

// --- sling-level injection tests (K3/K9): exercise instantiateFormulaWorkflow ---

const defaultBranchFormulaTOML = `
formula = "test-default-branch"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Rebase onto origin/{{default_branch}}"
description = "Open PR with --base {{default_branch}}"
`

// TestDetectDefaultBranch_SlingInjectedBeforeExpand proves the detected branch is
// injected into resolvedVars BEFORE expandStepVars runs: the step bead text shows
// the resolved value, not the literal {{default_branch}} token. Also asserts the
// U2 success echo.
func TestDetectDefaultBranch_SlingInjectedBeforeExpand(t *testing.T) {
	store := installMemStore(t)
	installDetectBranch(t, "master")
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-default-branch", "test-agent", defaultBranchFormulaTOML)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-default-branch",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	step, err := store.Get(t.Context(), stepIDs["step1"])
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Title, "master") || !strings.Contains(step.Description, "master") {
		t.Errorf("step should contain expanded 'master', got title=%q desc=%q", step.Title, step.Description)
	}
	if strings.Contains(step.Description, "{{default_branch}}") {
		t.Errorf("token left unexpanded — injection did not happen before expandStepVars: %q", step.Description)
	}
	if !strings.Contains(buf.String(), "Default branch: master") {
		t.Errorf("expected success echo 'Default branch: master', got: %q", buf.String())
	}
}

// TestDetectDefaultBranch_SlingOverrideWins proves a --var default_branch override
// takes precedence over detection (A4): even though detection would return
// "master", the operator's "develop" wins.
func TestDetectDefaultBranch_SlingOverrideWins(t *testing.T) {
	store := installMemStore(t)
	installDetectBranch(t, "master")
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-default-branch", "test-agent", defaultBranchFormulaTOML)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-default-branch",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
		CLIVars:     []string{"default_branch=develop"},
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	step, err := store.Get(t.Context(), stepIDs["step1"])
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Description, "develop") {
		t.Errorf("override 'develop' should win, got desc=%q", step.Description)
	}
	if strings.Contains(step.Description, "master") {
		t.Errorf("detection 'master' must not override the operator's --var, got desc=%q", step.Description)
	}
}

// TestDetectDefaultBranch_SlingFailLoud proves the empty-detection path is fail-loud:
// a visible "warning:" is written to w and the surfaced "main" sentinel is used —
// never a silent "main".
func TestDetectDefaultBranch_SlingFailLoud(t *testing.T) {
	store := installMemStore(t)
	installDetectBranch(t, "") // total detection failure
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-default-branch", "test-agent", defaultBranchFormulaTOML)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-default-branch",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	if !strings.Contains(buf.String(), "warning:") {
		t.Errorf("empty detection must print a visible 'warning:', got: %q", buf.String())
	}
	step, err := store.Get(t.Context(), stepIDs["step1"])
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Description, "main") {
		t.Errorf("fallback should surface 'main' sentinel, got desc=%q", step.Description)
	}
}
