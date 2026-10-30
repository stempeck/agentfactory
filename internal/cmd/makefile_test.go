package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
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
