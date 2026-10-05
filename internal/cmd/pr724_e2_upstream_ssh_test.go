package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

var p724e2Commit = strings.Repeat("0123456789", 4)

// p724e2SSHEnv hides the runner's own ssh and git configuration from git and puts a fake ssh first
// on PATH, so every arm sees only the ssh command it sets up itself and no network is touched.
type p724e2SSHEnv struct {
	bin, argvLog, pathSSH string
}

func p724e2NewSSHEnv(t *testing.T) p724e2SSHEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required for the upstream fetch: %v", err)
	}
	e := p724e2SSHEnv{bin: t.TempDir()}
	e.argvLog = filepath.Join(e.bin, "argv.log")
	e.pathSSH = e.fake(t, "path", "ssh")
	t.Setenv("PATH", filepath.Dir(e.pathSSH)+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// git treats an empty GIT_SSH_COMMAND or GIT_SSH as the command to run, so these must be absent.
	for _, k := range []string{"GIT_SSH_COMMAND", "GIT_SSH", "GIT_SSH_VARIANT", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return e
}

// fake writes <bin>/<dir>/<name>: it logs "<its own path> <argv>" and fails like an unreachable host.
func (e p724e2SSHEnv) fake(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(e.bin, dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$0 $*\" >> '" + e.argvLog + "'\nexit 1\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e p724e2SSHEnv) fetch(t *testing.T, repo string) (argv string, err error) {
	t.Helper()
	up := filepath.Join(t.TempDir(), ".upstream")
	err = fetchIntegrationUpstream(context.Background(), up, &config.IntegrationUpstream{Repo: repo, Commit: p724e2Commit}, 30*time.Second)
	b, _ := os.ReadFile(e.argvLog)
	return string(b), err
}

// mustRunBatchMode asserts the fetch failed loudly, ran program as its ssh (with marker, the operator's own
// option, still present) and handed it -o BatchMode=yes.
func p724e2MustRunBatchMode(t *testing.T, argv string, err error, program, marker string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "fetching upstream") {
		t.Fatalf("a fetch through the failing fake ssh must fail loudly naming the upstream fetch; got %v", err)
	}
	if !strings.Contains(argv, program+" ") {
		t.Fatalf("the operator's ssh program %s was never run; argv log %q (fetch error %v)", program, argv, err)
	}
	if marker != "" && !strings.Contains(argv, marker) {
		t.Fatalf("the operator's ssh option %s was dropped; argv log %q", marker, argv)
	}
	if !strings.Contains(argv, "-o BatchMode=yes") {
		t.Errorf("the upstream fetch ran ssh without -o BatchMode=yes, so ssh may still prompt on the terminal; argv log %q", argv)
	}
}

// ADR-014 "no prompt anywhere": every ssh the upstream fetch can reach gets BatchMode, added to the ssh
// command git itself would pick (env GIT_SSH_COMMAND, core.sshCommand, GIT_SSH, ssh), never replacing it.
func TestPR724_T15_UpstreamFetchSSHBatchMode(t *testing.T) {
	t.Run("default_ssh_on_path", func(t *testing.T) {
		e := p724e2NewSSHEnv(t)
		argv, err := e.fetch(t, "ssh://h.invalid/r")
		p724e2MustRunBatchMode(t, argv, err, e.pathSSH, "")
	})
	t.Run("operator_git_ssh_command", func(t *testing.T) {
		e := p724e2NewSSHEnv(t)
		program := e.fake(t, "env", "ssh")
		t.Setenv("GIT_SSH_COMMAND", program+" --marker-env")
		argv, err := e.fetch(t, "ssh://h.invalid/r")
		p724e2MustRunBatchMode(t, argv, err, program, "--marker-env")
	})
	t.Run("operator_core_ssh_command", func(t *testing.T) {
		e := p724e2NewSSHEnv(t)
		program := e.fake(t, "core", "ssh")
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.sshCommand")
		t.Setenv("GIT_CONFIG_VALUE_0", program+" --marker-core")
		argv, err := e.fetch(t, "ssh://h.invalid/r")
		p724e2MustRunBatchMode(t, argv, err, program, "--marker-core")
	})
	t.Run("operator_git_ssh_wrapper", func(t *testing.T) {
		e := p724e2NewSSHEnv(t)
		program := e.fake(t, "wrapper dir", "ssh-wrapper")
		t.Setenv("GIT_SSH", program)
		argv, err := e.fetch(t, "ssh://h.invalid/r")
		p724e2MustRunBatchMode(t, argv, err, program, "")
	})
	// An https-only check on the manifest string would not cover this route: the operator's insteadOf
	// rewrite turns the https repo into an ssh fetch.
	t.Run("https_repo_rewritten_to_ssh_by_insteadof", func(t *testing.T) {
		e := p724e2NewSSHEnv(t)
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "url.ssh://h.invalid/.insteadOf")
		t.Setenv("GIT_CONFIG_VALUE_0", "https://h.invalid/")
		argv, err := e.fetch(t, "https://h.invalid/r")
		p724e2MustRunBatchMode(t, argv, err, e.pathSSH, "")
	})
}

// The closure's own guards stay: git never inherits a terminal prompt or af's stdin.
func TestPR724_T15_KeepFetchPromptGuards(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd/0"); err != nil {
		t.Skipf("needs /proc to read the git child's stdin: %v", err)
	}
	e := p724e2NewSSHEnv(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitLog := filepath.Join(e.bin, "git.log")
	gitDir := filepath.Join(e.bin, "git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s prompt=%s stdin=%s\\n' \"$1\" \"${GIT_TERMINAL_PROMPT-unset}\" \"$(readlink /proc/$$/fd/0)\" >> '" + gitLog + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(gitDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gitDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = stdin; r.Close(); w.Close() })

	if _, err := e.fetch(t, "ssh://h.invalid/r"); err == nil {
		t.Fatal("a fetch through the failing fake ssh must fail")
	}
	b, _ := os.ReadFile(gitLog)
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		verb, rest, _ := strings.Cut(line, " ")
		if verb != "init" && verb != "fetch" {
			continue
		}
		seen[verb] = true
		if rest != "prompt=0 stdin=/dev/null" {
			t.Errorf("git %s ran with %q; want GIT_TERMINAL_PROMPT=0 and stdin /dev/null", verb, rest)
		}
	}
	if !seen["init"] || !seen["fetch"] {
		t.Fatalf("the fake git did not see both init and fetch; log %q", b)
	}
}

// A checkout already at the pin is reused without opening any transport, so no ssh runs at all.
func TestPR724_T15_KeepReuseFastPathMakesNoTransportCall(t *testing.T) {
	e := p724e2NewSSHEnv(t)
	up := filepath.Join(t.TempDir(), ".upstream")
	if err := os.MkdirAll(up, 0o755); err != nil {
		t.Fatal(err)
	}
	intBGit(t, up, "init", "-q")
	if err := os.WriteFile(filepath.Join(up, "kept"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intBGit(t, up, "add", "kept")
	intBGit(t, up, "commit", "-q", "-m", "pinned")
	head := intBGit(t, up, "rev-parse", "HEAD")

	err := fetchIntegrationUpstream(context.Background(), up, &config.IntegrationUpstream{Repo: "ssh://h.invalid/r", Commit: head}, 30*time.Second)
	if err != nil {
		t.Fatalf("a checkout already at the pin must be reused, got %v", err)
	}
	if b, _ := os.ReadFile(e.argvLog); len(b) != 0 {
		t.Errorf("the reuse path opened a transport; ssh ran with %q", b)
	}
	if _, err := os.Stat(filepath.Join(up, "kept")); err != nil {
		t.Errorf("the reused checkout was rebuilt: %v", err)
	}
}

// USING_PLUGINS.md's install bullet is where an operator looks when an ssh upstream now fails instead
// of prompting.
func TestPR724_T15_UsingPluginsNamesNonInteractiveFetch(t *testing.T) {
	const bullet = "- **`af plugin install <name>`** of an integration is the consent step."
	doc := g4ReadRepoDoc(t, "USING_PLUGINS.md")
	var line string
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, bullet) {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("USING_PLUGINS.md lost its install bullet %q", bullet)
	}
	for _, want := range []string{"fetches the pinned upstream without prompting", "-o BatchMode=yes", "known_hosts"} {
		if !strings.Contains(line, want) {
			t.Errorf("the install bullet must say %q (non-interactive upstream fetch)", want)
		}
	}
}
