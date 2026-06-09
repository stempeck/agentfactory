package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
)

// TestNoEnvReadsInLibraryPackages verifies the structural invariant that
// library code below the cobra command layer does not read or mutate named
// environment values from the process environment. Ambient state enters only
// at the CLI/main boundary (internal/cmd/) and flows downward as explicit
// values.
//
// The check flags any call to os.Getenv, os.LookupEnv, os.Setenv, or
// os.Unsetenv in non-test .go files outside internal/cmd/. os.Environ()
// (whole-environment passthrough used to forward env to subprocesses without
// reading a specific value into program logic) is structurally different and
// is not flagged.
//
// Background: established by issue #98 (Phases 1-4). Originally scoped to
// AF_*/CLAUDE_*/TMUX* prefixes; broadened to all named reads after issue #80
// Phase 3 (PR #117) demonstrated that prefix-based scoping let a new env-var
// family (BD_*) slip through. Further broadened to also catch writes
// (Setenv/Unsetenv) after issue #80 Phase 2 (PR #116) demonstrated that
// read-only scanning let a process-env seed in contract test setup slip
// through.
func TestNoEnvReadsInLibraryPackages(t *testing.T) {
	// Find the module root (parent of internal/)
	root := findModuleRoot(t)

	internalDir := filepath.Join(root, "internal")
	cmdDir := filepath.Join(root, "internal", "cmd")
	testsupportDir := filepath.Join(root, "internal", "testsupport")

	envReadPattern := regexp.MustCompile(`\bos\.(Getenv|LookupEnv|Setenv|Unsetenv)\(`)

	var violations []string

	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip internal/cmd/ entirely — command layer is permitted to read env
		if info.IsDir() && path == cmdDir {
			return filepath.SkipDir
		}
		// Skip internal/testsupport/ entirely — test-support code legitimately
		// sets TMUX_TMPDIR/TMUX (consumed by spawned tmux subprocesses, never read
		// by any internal/* library package). Sanctioned in ADR-004 (#317 Phase 2b).
		if info.IsDir() && path == testsupportDir {
			return filepath.SkipDir
		}
		// Skip non-Go files and test files
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if envReadPattern.MatchString(line) {
				violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("library code must not read or mutate named env values directly (os.Getenv/os.LookupEnv/os.Setenv/os.Unsetenv).\n"+
			"Ambient state should enter at the CLI boundary (internal/cmd/) and flow "+
			"as explicit parameters. os.Environ() (whole-env passthrough) is permitted.\n"+
			"Violations:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestSuiteHermiticWithAFVars is the deterministic mechanism test for #327/AC-4
// (design Comp-C.1). It proves NeutralizeAFEnv actually neutralizes the family,
// replacing a prior no-assertion body that passed whether or not the mechanism
// worked (the H-2 "CI-vacuous coverage" that let #327 survive).
//
// It seeds EVERY tmuxisolation.AFEnvFamily member to a junk value, an AF_TEST_*
// keep-set sentinel, and captures PATH; calls tmuxisolation.NeutralizeAFEnv()
// directly; then asserts every family member was wiped to "" while AF_TEST_KEEP
// and PATH survived. Because it seeds its own junk, it is deterministic in CI
// regardless of ambient state — and it goes RED if the wipe drops a member or
// clobbers the keep-set/PATH. (Do NOT weaken this to an `os.Getenv(member)==""`
// post-condition: that form is vacuous when the family is already unset.)
func TestSuiteHermiticWithAFVars(t *testing.T) {
	const junk = "ultra-implement-327-junk-sentinel"

	for _, key := range tmuxisolation.AFEnvFamily {
		t.Setenv(key, junk)
	}
	t.Setenv("AF_TEST_KEEP", "sentinel")
	wantPATH := os.Getenv("PATH")

	tmuxisolation.NeutralizeAFEnv()

	for _, key := range tmuxisolation.AFEnvFamily {
		if got := os.Getenv(key); got != "" {
			t.Errorf("NeutralizeAFEnv left %s=%q in the environment; want it wiped to %q", key, got, "")
		}
	}
	if got := os.Getenv("AF_TEST_KEEP"); got != "sentinel" {
		t.Errorf("NeutralizeAFEnv clobbered keep-set AF_TEST_KEEP=%q; want %q (AF_TEST_* must survive)", got, "sentinel")
	}
	if got := os.Getenv("PATH"); got != wantPATH {
		t.Errorf("NeutralizeAFEnv clobbered PATH=%q; want %q (PATH does not match AF_/CLAUDE_ and must survive)", got, wantPATH)
	}
}

// TestEnvFamilyDifferentialProbe is the required end-to-end differential probe
// for #327/AC-4 (design Comp-C.2). It re-execs THIS test binary
// (os.Executable()) with -test.run=TestProvisioningPipeline_CreatesTemplateFile
// — a representative agent-gen test — twice: once with the AF_*/CLAUDE_* family
// unset, once with the family junk and AF_SOURCE_ROOT pointed at a throwaway
// VALID AF dir. On the clean tree the child's own TestMain neutralizes the family
// before m.Run(), so both children resolve their compiledSourceRoot and PASS, and
// the throwaway dir receives no agent-gen template. If the NeutralizeAFEnv() call
// is removed from Setup, the junk-profile child instead resolves the throwaway
// AF_SOURCE_ROOT and leaks an internal/templates/roles/*.md.tmpl into it — failing
// the child run AND tripping the throwaway-empty assertion.
//
// FAIL-NOT-SKIP CONTRACT (H2-2): every setup step here t.Fatal's on error. A probe
// that t.Skip'd or passed when it could not re-exec would provide zero coverage and
// re-create the exact H-2 vacuity this component exists to kill. "Can't test" is a
// FAILURE, never a pass.
func TestEnvFamilyDifferentialProbe(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("H2-2: cannot locate the test binary to re-exec (os.Executable): %v", err)
	}

	// The container's /tmp is a noexec tmpfs; the Makefile points TMPDIR/GOTMPDIR
	// at $HOME/.cache/af-test (exec-capable). The child inherits this hazard, so we
	// MUST forward both — and fail (never skip) if they are absent.
	tmpDir := os.Getenv("TMPDIR")
	goTmpDir := os.Getenv("GOTMPDIR")
	if tmpDir == "" || goTmpDir == "" {
		t.Fatalf("H2-2: TMPDIR=%q GOTMPDIR=%q must both be set to an exec-capable dir before re-exec "+
			"(the container /tmp is noexec); run via `make test` or export TMPDIR/GOTMPDIR=$HOME/.cache/af-test",
			tmpDir, goTmpDir)
	}
	path := os.Getenv("PATH")
	if path == "" {
		t.Fatal("H2-2: PATH is empty; cannot forward a usable PATH to the re-exec child")
	}

	// Throwaway AF_SOURCE_ROOT target. It MUST be a *valid* AF dir: validateAFSource
	// requires a go.mod containing "agentfactory". A bare TempDir fails validation,
	// resolution falls through to compiledSourceRoot, and the probe would go green
	// even if neutralization regressed (a false negative).
	throwaway := t.TempDir()
	if err := os.WriteFile(filepath.Join(throwaway, "go.mod"),
		[]byte("module github.com/x/agentfactory\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatalf("H2-2: seeding throwaway go.mod: %v", err)
	}

	// Curated base env (mirrors protect_agent_scaffold_test.go's cmd.Env curation):
	// only the non-family vars the child needs. AF_/CLAUDE_ keys are added per profile.
	baseEnv := []string{
		"TMPDIR=" + tmpDir,
		"GOTMPDIR=" + goTmpDir,
		"PATH=" + path,
		"HOME=" + os.Getenv("HOME"),
	}

	// runChild re-execs the target test under the given profile and fails (never
	// skips) on any launch/exit error. Exit is extracted via *exec.ExitError, exactly
	// as protect_agent_scaffold_test.go does.
	runChild := func(profile string, familyEnv []string) {
		t.Helper()
		// -test.run targets a DIFFERENT test, so the child never re-enters this probe.
		cmd := exec.Command(exe, "-test.run=^TestProvisioningPipeline_CreatesTemplateFile$", "-test.count=1")
		cmd.Env = append(append([]string{}, baseEnv...), familyEnv...)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			exitErr, ok := runErr.(*exec.ExitError)
			if !ok {
				// Could not launch the child or could not read its exit status. This is a
				// FAILURE (H2-2), never a skip/pass.
				t.Fatalf("H2-2: re-exec child (%s) did not launch or its exit status was unreadable: %v\n--- child output ---\n%s",
					profile, runErr, out)
			}
			t.Fatalf("differential probe (%s): child exited %d; want 0.\n--- child output ---\n%s",
				profile, exitErr.ExitCode(), out)
		}
	}

	// Profile (a): family UNSET — curated env carries no AF_/CLAUDE_ key.
	runChild("family-unset", nil)

	// Profile (b): family JUNK, with AF_SOURCE_ROOT pointed at the throwaway valid AF
	// dir. The junk values are derived from AFEnvFamily so the profile stays in sync if
	// the inventory grows; AF_SOURCE_ROOT is the one member that gets a meaningful value
	// (the throwaway), which is what a regressed wipe would leak into.
	junkEnv := make([]string, 0, len(tmuxisolation.AFEnvFamily))
	for _, key := range tmuxisolation.AFEnvFamily {
		if key == "AF_SOURCE_ROOT" {
			junkEnv = append(junkEnv, "AF_SOURCE_ROOT="+throwaway)
			continue
		}
		junkEnv = append(junkEnv, key+"="+junkVal)
	}
	runChild("family-junk", junkEnv)

	// The leak guard: on the clean tree the junk-profile child neutralized
	// AF_SOURCE_ROOT before resolving, so the throwaway dir must hold NO agent-gen
	// template. A non-empty match means the wipe failed and the write leaked.
	leaked, err := filepath.Glob(filepath.Join(throwaway, "internal", "templates", "roles", "*.md.tmpl"))
	if err != nil {
		t.Fatalf("H2-2: globbing throwaway template dir: %v", err)
	}
	if len(leaked) > 0 {
		t.Fatalf("differential probe: throwaway AF dir leaked agent-gen template(s) %v; "+
			"NeutralizeAFEnv failed to wipe AF_SOURCE_ROOT before the child resolved it (#327 regression)", leaked)
	}
}

// junkVal is the per-member junk value seeded into the differential probe's
// family-junk profile. Its concrete content is irrelevant (the child wipes it on
// the clean tree); it only has to be non-empty so a regressed wipe leaves a
// detectable AF_SOURCE_ROOT.
const junkVal = "ultra-implement-327-junk"

// TestEnvFamilyInventoryDriftScan is the structural drift scan for #327/AC-4
// (design Comp-E). Modeled on TestNoEnvReadsInLibraryPackages and reusing
// findModuleRoot + filepath.Walk, it walks non-test .go files under BOTH
// internal/cmd AND cmd/af (cmd/af/main.go is the second AF_SOURCE_ROOT consumer),
// captures the string-literal argument of every os.Getenv("…")/os.LookupEnv("…")
// whose key matches ^(AF_|CLAUDE_), and asserts each such key is present in the
// documented inventory tmuxisolation.AFEnvFamily. It goes RED when a new literal
// family read is added without updating the inventory.
//
// IMPORTANT (inventory-honesty, not runtime coverage): the runtime neutralization
// is a PREFIX wipe, so it ALREADY neutralizes any new family member at runtime.
// AFEnvFamily exists only as the documented inventory and this scan's assertion
// target — the failure message says so, so a future dev does not "fix" the wipe.
func TestEnvFamilyInventoryDriftScan(t *testing.T) {
	root := findModuleRoot(t)
	scanDirs := []string{
		filepath.Join(root, "internal", "cmd"),
		filepath.Join(root, "cmd", "af"),
	}

	// Richer than TestNoEnvReadsInLibraryPackages's boolean match: capture the
	// string-literal key so we can filter on the AF_/CLAUDE_ prefix and report it.
	envReadPattern := regexp.MustCompile(`\bos\.(?:Getenv|LookupEnv)\("([^"]*)"\)`)

	inFamily := make(map[string]bool, len(tmuxisolation.AFEnvFamily))
	for _, k := range tmuxisolation.AFEnvFamily {
		inFamily[k] = true
	}

	var violations []string
	for _, dir := range scanDirs {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for i, line := range strings.Split(string(data), "\n") {
				for _, m := range envReadPattern.FindAllStringSubmatch(line, -1) {
					key := m[1]
					if !strings.HasPrefix(key, "AF_") && !strings.HasPrefix(key, "CLAUDE_") {
						continue
					}
					if !inFamily[key] {
						violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(m[0])))
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		t.Errorf("AF_*/CLAUDE_* literal env read(s) in non-test internal/cmd + cmd/af are missing "+
			"from the documented inventory tmuxisolation.AFEnvFamily.\n"+
			"NOTE: the runtime prefix wipe (tmuxisolation.NeutralizeAFEnv) ALREADY neutralizes these "+
			"keys at runtime — AFEnvFamily is ONLY the documented inventory and this scan's assertion "+
			"target. Add the key(s) below to AFEnvFamily to keep that inventory accurate; do NOT change "+
			"the wipe logic (it is deliberately prefix-based, not a named loop).\n"+
			"Unrecorded reads:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// findModuleRoot walks up from the working directory to find go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}
