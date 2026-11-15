package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestMakefileSupplyChainInvariants(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("reading Makefile: %v", err)
	}
	content := string(data)

	t.Run("pip_require_hashes", func(t *testing.T) {
		if !strings.Contains(content, "--require-hashes") {
			t.Error("Makefile missing --require-hashes in pip install line")
		}
	})
}

// make lock deletes $(LOCK_VENV) and LOCK_VENV is a ?= variable any caller can override, so the recipe
// itself must refuse a path that is not its own throwaway venv. The recipe runs against a copy of the
// Makefile in a sandbox whose python fails right after the venv step, so nothing past it can run.
func TestMakeLockGuardsItsVenvPath(t *testing.T) {
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make not on PATH")
	}
	makefile := readRepoFile(t, "Makefile")

	t.Run("an overridden LOCK_VENV that is not af-lock-venv is refused before any rm", func(t *testing.T) {
		for name, victimOf := range map[string]func(sb adLockSandbox) string{
			"the home directory": func(sb adLockSandbox) string { return sb.home },
			"a source tree":      func(sb adLockSandbox) string { return filepath.Join(sb.root, "src") },
		} {
			t.Run(name, func(t *testing.T) {
				sb := newAdLockSandbox(t, makefile)
				victim := victimOf(sb)
				keep := filepath.Join(victim, "keep")
				if err := os.MkdirAll(victim, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				out, rc := sb.make(t, makeBin, "lock", "LOCK_VENV="+victim)
				if _, err := os.Stat(keep); err != nil {
					t.Errorf("make lock LOCK_VENV=%s deleted it (%v):\n%s", victim, err, out)
				}
				if rc == 0 {
					t.Errorf("make lock LOCK_VENV=%s exited 0; it must refuse:\n%s", victim, out)
				}
				if calls := sb.calls(); strings.Contains(calls, "-m venv") {
					t.Errorf("make lock went on to build a venv at %s:\n%s", victim, calls)
				}
				// The sandbox path carries the subtest's name, which itself says LOCK_VENV.
				if !strings.Contains(strings.ReplaceAll(out, sb.root, ""), "LOCK_VENV") {
					t.Errorf("the refusal does not name LOCK_VENV:\n%s", out)
				}
			})
		}
	})

	t.Run("the af-lock-venv name passes the guard", func(t *testing.T) {
		sb := newAdLockSandbox(t, makefile)
		venv := filepath.Join(sb.root, "cache", "af-lock-venv")
		out, _ := sb.make(t, makeBin, "lock", "LOCK_VENV="+venv)
		if calls := sb.calls(); !strings.Contains(calls, "-m venv "+venv) {
			t.Errorf("make lock LOCK_VENV=%s never reached the venv build:\n%s\ncalls:\n%s", venv, out, calls)
		}
	})

	t.Run("the wheel caches live outside the venv and the repo", func(t *testing.T) {
		sb := newAdLockSandbox(t, makefile)
		venv := filepath.Join(sb.home, ".cache", "af-lock-venv")
		recipe := sb.dryRun(t, makeBin, "LOCK_VENV="+venv)
		caches := adLockCaches(t, recipe)
		removed := adRemovedPaths(recipe)
		for _, c := range caches {
			if adUnder(c, venv) {
				t.Errorf("cache %s is inside the throwaway venv %s, so every run downloads every wheel again", c, venv)
			}
			if adUnder(c, sb.repo) {
				t.Errorf("cache %s is inside the repository %s", c, sb.repo)
			}
			for _, r := range removed {
				if adUnder(c, r) {
					t.Errorf("the lock recipe's rm of %s deletes the cache %s", r, c)
				}
			}
		}
	})

	t.Run("LOCK_CACHE relocates both caches", func(t *testing.T) {
		sb := newAdLockSandbox(t, makefile)
		custom := filepath.Join(sb.root, "custom-cache")
		recipe := sb.dryRun(t, makeBin, "LOCK_VENV="+filepath.Join(sb.home, ".cache", "af-lock-venv"), "LOCK_CACHE="+custom)
		caches := adLockCaches(t, recipe)
		for _, c := range caches {
			if !adUnder(c, custom) {
				t.Errorf("cache %s ignores LOCK_CACHE=%s", c, custom)
			}
		}
	})
}

type adLockSandbox struct{ root, repo, home, bin, log string }

func newAdLockSandbox(t *testing.T, makefile string) adLockSandbox {
	t.Helper()
	root := t.TempDir()
	sb := adLockSandbox{root: root, repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"), bin: filepath.Join(root, "bin"), log: filepath.Join(root, "calls.log")}
	for _, d := range []string{sb.repo, sb.home, sb.bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string, mode os.FileMode) {
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(sb.repo, "Makefile"), makefile, 0o644)
	write(filepath.Join(sb.bin, "python3.12"), "#!/bin/sh\necho \"python $*\" >> \"$AD_CALLS\"\n[ \"$1\" = -c ] && exit 0\nexit 1\n", 0o755)
	for _, name := range []string{"python3", "python", "pip", "pip3", "pip-compile", "curl", "jq"} {
		write(filepath.Join(sb.bin, name), "#!/bin/sh\necho \"TRIPWIRE "+name+" $*\" >> \"$AD_CALLS\"\nexit 97\n", 0o755)
	}
	// The real rm, so an unguarded recipe shows what it deletes.
	for _, tool := range []string{"rm", "mkdir", "basename", "dirname", "cat", "sed", "grep", "tr", "expr", "printf", "echo", "test", "[", "true", "false", "env", "date"} {
		if p, err := exec.LookPath(tool); err == nil {
			if err := os.Symlink(p, filepath.Join(sb.bin, tool)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return sb
}

func (sb adLockSandbox) make(t *testing.T, makeBin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(makeBin, append([]string{"--no-print-directory", "-C", sb.repo, "PYTHON=" + filepath.Join(sb.bin, "python3.12")}, args...)...)
	cmd.Env = []string{"PATH=" + sb.bin, "HOME=" + sb.home, "AD_CALLS=" + sb.log, "LC_ALL=C"}
	out, err := cmd.CombinedOutput()
	rc := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running make: %v", err)
		}
		rc = exit.ExitCode()
	}
	if calls := sb.calls(); strings.Contains(calls, "TRIPWIRE") {
		t.Errorf("make lock reached a tool the sandbox forbids:\n%s", calls)
	}
	return string(out), rc
}

func (sb adLockSandbox) dryRun(t *testing.T, makeBin string, vars ...string) string {
	t.Helper()
	out, rc := sb.make(t, makeBin, append([]string{"-n", "lock"}, vars...)...)
	if rc != 0 {
		t.Fatalf("make -n lock exited %d:\n%s", rc, out)
	}
	return out
}

func (sb adLockSandbox) calls() string {
	b, _ := os.ReadFile(sb.log)
	return string(b)
}

var adCacheFlagRe = regexp.MustCompile(`(PIP_CACHE_DIR=|--cache-dir[= ])("[^"]*"|'[^']*'|\S+)`)

func adLockCaches(t *testing.T, recipe string) []string {
	t.Helper()
	var out []string
	flags := map[string]bool{}
	for _, m := range adCacheFlagRe.FindAllStringSubmatch(recipe, -1) {
		flags[strings.TrimRight(m[1], "= ")] = true
		out = append(out, filepath.Clean(strings.Trim(m[2], `"'`)))
	}
	if !flags["PIP_CACHE_DIR"] || !flags["--cache-dir"] {
		t.Fatalf("the lock recipe must give pip a PIP_CACHE_DIR= and pip-compile a --cache-dir:\n%s", recipe)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func adRemovedPaths(recipe string) []string {
	var out []string
	for _, line := range strings.Split(recipe, "\n") {
		words := strings.Fields(line)
		for i, w := range words {
			if w != "rm" {
				continue
			}
			for _, arg := range words[i+1:] {
				if arg == ";" || arg == "&&" || arg == "||" || arg == "|" {
					break
				}
				if !strings.HasPrefix(arg, "-") {
					out = append(out, filepath.Clean(strings.Trim(arg, `"';`)))
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func adUnder(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// The operator's decision in #138: internal/cmd alone outgrew the old 4m.
const mtiRootBudget = 6 * time.Minute

func TestMakeTestIntegrationPerPackageBudget(t *testing.T) {
	recipe, ok := mtiRecipe(readRepoFile(t, "Makefile"), "test-integration")
	if !ok {
		t.Fatal("Makefile has no test-integration: rule")
	}
	var root, web []mtiGoTestRun
	for _, run := range mtiGoTestRuns(recipe) {
		if run.inWeb {
			web = append(web, run)
		} else {
			root = append(root, run)
		}
	}
	if len(root) != 1 || len(web) != 1 {
		t.Fatalf("test-integration has %d root-module and %d web go test runs, want one of each:\n%s",
			len(root), len(web), strings.Join(recipe, "\n"))
	}

	t.Run(fmt.Sprintf("the root-module run allows each package %v", mtiRootBudget), func(t *testing.T) {
		if err := root[0].budgetErr(); err != nil {
			t.Errorf("root-module integration run: %v:\n%s", err, root[0].line)
		}
	})
	t.Run("the web module run keeps a budget of its own", func(t *testing.T) {
		if err := web[0].budgetErr(); err != nil {
			t.Errorf("web module integration run: %v:\n%s", err, web[0].line)
		}
	})
}

func TestMakeTestIntegrationPerPackageBudgetParsers(t *testing.T) {
	t.Run("the recipe ends where make ends it", func(t *testing.T) {
		for _, tc := range []struct {
			name, makefile string
			want           []string
		}{
			{"across a blank line", "t:\n\tone\n\n\ttwo\n", []string{"\tone", "\ttwo"}},
			{"across a column-0 comment", "t:\n\tone\n# note\n\ttwo\n", []string{"\tone", "\ttwo"}},
			{"across indented blank and comment lines", "t:\n\tone\n  \n  # note\n\ttwo\n", []string{"\tone", "\ttwo"}},
			{"keeping tab-prefixed notes as recipe lines", "t:\n\t@# note\n\t# note\n\tone\n", []string{"\t@# note", "\t# note", "\tone"}},
			{"at the next rule", "t:\n\tone\nu:\n\ttwo\n", []string{"\tone"}},
			{"at a variable whose value continues on tab lines", "t:\n\tone\n\n# note\nV = a \\\n\tb \\\n\tc\n\nu:\n\ttwo\n", []string{"\tone"}},
			{"at the end of the file", "t:\n\tone\n\ttwo", []string{"\tone", "\ttwo"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got, ok := mtiRecipe(tc.makefile, "t"); !ok || !slices.Equal(got, tc.want) {
					t.Errorf("mtiRecipe = %q, %v; want %q, true", got, ok, tc.want)
				}
			})
		}
		t.Run("not at all without the rule", func(t *testing.T) {
			if got, ok := mtiRecipe("u:\n\tone\n", "t"); ok {
				t.Errorf("mtiRecipe = %q, true; want false", got)
			}
		})
	})

	t.Run("each run's budget is judged", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			line    string
			inWeb   bool
			allowed bool
		}{
			{"the decided root budget", "\tgo test -tags=integration -timeout=" + mtiRootBudget.String() + " ./...", false, true},
			{"a root run back at the old 4m", "\tgo test -tags=integration -timeout=4m ./...", false, false},
			{"a root run without -timeout", "\tgo test -tags=integration ./...", false, false},
			{"a root run whose later -timeout would win", "\tgo test -timeout=" + mtiRootBudget.String() + " -timeout=4m ./...", false, false},
			{"a web run with a budget of its own", "\tcd web && go test -tags=integration -timeout=4m ./...", true, true},
			{"a web run without -timeout", "\tcd web && go test -tags=integration ./...", true, false},
			{"a web run whose -timeout=0 turns the timeout off", "\tcd web && go test -tags=integration -timeout=0 ./...", true, false},
			{"a web run whose negative -timeout turns the timeout off", "\tcd web && go test -tags=integration -timeout -1m ./...", true, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				runs := mtiGoTestRuns([]string{tc.line})
				if len(runs) != 1 || runs[0].inWeb != tc.inWeb {
					t.Fatalf("mtiGoTestRuns(%q) = %+v; want one run with inWeb=%v", tc.line, runs, tc.inWeb)
				}
				if err := runs[0].budgetErr(); (err == nil) != tc.allowed {
					t.Errorf("budgetErr() = %v; want allowed=%v", err, tc.allowed)
				}
			})
		}
	})

	t.Run("a shell comment that spells out a go test run is not one", func(t *testing.T) {
		if runs := mtiGoTestRuns([]string{"\t@# go test -timeout=0 ./... would run unbounded"}); len(runs) != 0 {
			t.Errorf("mtiGoTestRuns counted a comment as %d run(s): %+v", len(runs), runs)
		}
	})
}

type mtiGoTestRun struct {
	line    string
	inWeb   bool
	timeout time.Duration
	err     error
}

func (run mtiGoTestRun) budgetErr() error {
	switch {
	case run.err != nil:
		return run.err
	case run.inWeb && run.timeout <= 0:
		return fmt.Errorf("budget = %v, which go test reads as no timeout at all", run.timeout)
	case !run.inWeb && run.timeout != mtiRootBudget:
		return fmt.Errorf("budget = %v, want %v", run.timeout, mtiRootBudget)
	}
	return nil
}

func mtiRecipe(makefile, target string) ([]string, bool) {
	var recipe []string
	inRule := false
	for _, line := range strings.Split(makefile, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, target+":"):
			inRule = true
		case inRule && strings.HasPrefix(line, "\t"):
			recipe = append(recipe, line)
		case inRule && (trimmed == "" || strings.HasPrefix(trimmed, "#")):
		case inRule:
			return recipe, true
		}
	}
	return recipe, inRule
}

func mtiGoTestRuns(recipe []string) []mtiGoTestRun {
	var runs []mtiGoTestRun
	for _, line := range recipe {
		command := strings.TrimLeft(strings.TrimSpace(line), "@+-")
		if strings.HasPrefix(command, "#") {
			continue
		}
		fields := strings.Fields(command)
		start := slices.IndexFunc(fields, func(f string) bool { return f == "go" })
		if start < 0 || start+1 >= len(fields) || fields[start+1] != "test" {
			continue
		}
		run := mtiGoTestRun{line: line, inWeb: strings.Contains(command, "cd web")}
		var values []string
		for i, f := range fields[start+2:] {
			if v, ok := strings.CutPrefix(f, "-timeout="); ok {
				values = append(values, v)
			} else if f == "-timeout" && start+3+i < len(fields) {
				values = append(values, fields[start+3+i])
			}
		}
		if len(values) != 1 {
			run.err = fmt.Errorf("want exactly one -timeout flag, found %d", len(values))
		} else {
			run.timeout, run.err = time.ParseDuration(values[0])
		}
		runs = append(runs, run)
	}
	return runs
}
