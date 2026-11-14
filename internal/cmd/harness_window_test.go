package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// No test in this file calls t.Parallel: each puts its own fake harness first on PATH.

const hwModel = "claude-fable-5-2"

// hwFakeScript dumps what the probe hands the harness (environment, which of its scratch directories
// exist, working directory, its entries and argv) and answers with what the test planted. It also
// writes where a real harness writes, the config directory and a .claude/ in its working directory,
// so a probe that leaves either behind, or runs in the caller's directory, is visible. The directory
// dump runs before that write, which would otherwise create the config directory itself.
const hwFakeScript = `#!/bin/bash
d='%s'
dump_dirs() {
  for p in "HOME=$HOME" "HOME/.claude=$HOME/.claude" "CLAUDE_CONFIG_DIR=$CLAUDE_CONFIG_DIR" "TMPDIR=$TMPDIR"; do
    if [ -n "${p#*=}" ] && [ -d "${p#*=}" ]; then echo "${p/=*/}=dir"; else echo "${p/=*/}=missing"; fi
  done
}
if [ "$1" = "--version" ]; then
  env > "$d/version.env"
  dump_dirs > "$d/version.dirs"
  cat "$d/version.out"
  exit 0
fi
env > "$d/run.env"
dump_dirs > "$d/run.dirs"
pwd > "$d/run.pwd"
ls -A > "$d/run.ls"
printf '%%s\n' "$@" > "$d/run.argv"
if [ -n "$CLAUDE_CONFIG_DIR" ]; then mkdir -p "$CLAUDE_CONFIG_DIR/projects" && echo '{}' > "$CLAUDE_CONFIG_DIR/projects/probe.jsonl"; fi
mkdir -p .claude && echo '{}' > .claude/settings.local.json
cat "$d/run.stderr" >&2
cat "$d/run.stdout"
exit "$(cat "$d/run.exit")"
`

type hwFake struct{ dir string }

// newHWFake puts a fake claude first on PATH. tryExecCapableDir runs before any t.Chdir, because it
// resolves the repository root from the working directory. The parent's HOME and CLAUDE_CONFIG_DIR
// are throwaway directories, so a probe that leaked the parent's environment could not write into a
// real one, and the test then stands in an empty directory for the same reason.
func newHWFake(t *testing.T) *hwFake {
	t.Helper()
	dir, err := tryExecCapableDir(t, "af-test-harnesswindow")
	if err != nil {
		t.Fatalf("no exec-capable directory for the fake claude: %v", err)
	}
	f := &hwFake{dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fmt.Sprintf(hwFakeScript, dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	f.version(t, "2.1.322 (Claude Code)\n")
	f.answer(t, `{}`, "", 0)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Chdir(t.TempDir())
	return f
}

func (f *hwFake) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *hwFake) version(t *testing.T, out string) { f.write(t, "version.out", out) }

// answer plants the headless turn's result: modelUsage is the raw JSON of that field, or "" for a
// result that has none.
func (f *hwFake) answer(t *testing.T, modelUsage, stderr string, exit int) {
	t.Helper()
	out := `{"type":"result","subtype":"success","is_error":false,"result":"ok"`
	if modelUsage != "" {
		out += `,"modelUsage":` + modelUsage
	}
	f.write(t, "run.stdout", out+"}\n")
	f.write(t, "run.stderr", stderr)
	f.write(t, "run.exit", fmt.Sprint(exit))
}

func (f *hwFake) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		t.Fatalf("the fake claude left no %s (it never ran that way): %v", name, err)
	}
	return string(data)
}

func (f *hwFake) env(t *testing.T, name string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, line := range strings.Split(f.read(t, name), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

func (f *hwFake) argv(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(f.read(t, "run.argv"), "\n"), "\n")
}

func hwUsage(windows ...any) string {
	var parts []string
	for i := 0; i+1 < len(windows); i += 2 {
		parts = append(parts, fmt.Sprintf(`%q:{"inputTokens":1,"outputTokens":1,"contextWindow":%v}`, windows[i], windows[i+1]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func hwProbe(t *testing.T, profile map[string]string) (harnessWindowProbe, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return probeHarnessWindow(ctx, profile)
}

func hwRequireArgs(t *testing.T, argv []string, want ...string) {
	t.Helper()
	for i := 0; i+1 < len(want); i += 2 {
		found := false
		for j := 0; j+1 < len(argv); j++ {
			found = found || (argv[j] == want[i] && argv[j+1] == want[i+1])
		}
		if !found {
			t.Errorf("the harness argv %q lacks %s %s", argv, want[i], want[i+1])
		}
	}
}

func TestProbeHarnessWindow_ParsesWindowAndMarker(t *testing.T) {
	profile := map[string]string{"ANTHROPIC_MODEL": hwModel, config.EnvMaxContextTokens: "1000000"}
	for _, tc := range []struct {
		name, stderr string
		want         harnessWindowProbe
	}{
		{"recognised model", "", harnessWindowProbe{ModelID: hwModel, ContextWindow: 200000, HarnessVersion: "2.1.322"}},
		{"unrecognised marker on stderr", "Error: [claude-code:unrecognized_model] " + hwModel + " is not a known model\n",
			harnessWindowProbe{ModelID: hwModel, ContextWindow: 200000, Unrecognized: true, HarnessVersion: "2.1.322"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHWFake(t)
			f.answer(t, hwUsage(hwModel, 200000), tc.stderr, 0)
			p, err := hwProbe(t, profile)
			if err != nil {
				t.Fatalf("probeHarnessWindow: %v", err)
			}
			if p != tc.want {
				t.Errorf("probe = %+v, want %+v", p, tc.want)
			}
			argv := f.argv(t)
			hwRequireArgs(t, argv, "--output-format", "json", "--model", hwModel)
			if len(argv) == 0 || argv[0] != "-p" {
				t.Errorf("the harness argv %q must open with -p (one headless turn)", argv)
			}
		})
	}
}

func TestProbeHarnessWindow_MissingModelUsageIsError(t *testing.T) {
	profile := map[string]string{"ANTHROPIC_MODEL": hwModel, config.EnvMaxContextTokens: "1000000"}
	t.Run("control: two keys, the requested one is read", func(t *testing.T) {
		f := newHWFake(t)
		f.answer(t, hwUsage("claude-haiku-5-5", 200000, hwModel, 1000000), "", 0)
		p, err := hwProbe(t, profile)
		if err != nil || p.ContextWindow != 1000000 || p.ModelID != hwModel {
			t.Fatalf("probe = %+v, %v; want the requested id's window 1000000", p, err)
		}
	})
	for _, tc := range []struct{ name, usage string }{
		{"no modelUsage", ""},
		{"empty modelUsage", `{}`},
		{"two keys neither the requested id", hwUsage("claude-haiku-5-5", 200000, "claude-opus-5-5", 1000000)},
		{"contextWindow absent", fmt.Sprintf(`{%q:{"inputTokens":1}}`, hwModel)},
		{"contextWindow not a number", fmt.Sprintf(`{%q:{"contextWindow":"1000000"}}`, hwModel)},
		{"contextWindow 0", hwUsage(hwModel, 0)},
		{"contextWindow negative", hwUsage(hwModel, -200000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHWFake(t)
			f.answer(t, tc.usage, "", 0)
			p, err := hwProbe(t, profile)
			if err == nil {
				t.Errorf("a result with %s was read as a window: %+v", tc.name, p)
			}
			if p != (harnessWindowProbe{}) {
				t.Errorf("a failed probe returned a measurement %+v; nothing may stand for a window", p)
			}
		})
	}
	t.Run("non-zero exit", func(t *testing.T) {
		f := newHWFake(t)
		f.answer(t, hwUsage(hwModel, 1000000), "boom\n", 1)
		if p, err := hwProbe(t, profile); err == nil || p != (harnessWindowProbe{}) {
			t.Errorf("a harness that exited 1 gave %+v, %v; want an error and no measurement", p, err)
		}
	})
}

func TestProbeHarnessWindow_NoProfileEndpointOrKeyReachesChild(t *testing.T) {
	if len(config.WindowDecidingEnvKeys) == 0 {
		t.Fatal("config.WindowDecidingEnvKeys is empty: the probe would take no window key from a profile")
	}
	has1M, hasDisableCompact := false, false
	for _, k := range config.WindowDecidingEnvKeys {
		has1M = has1M || k == "CLAUDE_CODE_DISABLE_1M_CONTEXT"
		hasDisableCompact = hasDisableCompact || k == "DISABLE_COMPACT"
	}
	if !has1M {
		t.Errorf("config.WindowDecidingEnvKeys = %q lacks CLAUDE_CODE_DISABLE_1M_CONTEXT, which moves the window on the pin", config.WindowDecidingEnvKeys)
	}
	if !hasDisableCompact {
		t.Errorf("config.WindowDecidingEnvKeys = %q lacks DISABLE_COMPACT, which moves an unrecognised model's window on the pin", config.WindowDecidingEnvKeys)
	}

	f := newHWFake(t)
	f.answer(t, hwUsage(hwModel, 200000), "", 0)
	canaries := map[string]string{}
	parent := map[string]string{
		"ANTHROPIC_API_KEY":       "parent-api-key-canary",
		"ANTHROPIC_AUTH_TOKEN":    "parent-auth-token-canary",
		"ANTHROPIC_BASE_URL":      "http://parent-base-url-canary.invalid",
		"CLAUDE_CODE_OAUTH_TOKEN": "parent-oauth-canary",
		"HTTP_PROXY":              "http://parent-proxy-canary.invalid:3128",
	}
	for k, v := range parent {
		t.Setenv(k, v)
		canaries["parent "+k] = v
	}
	profile := map[string]string{
		"ANTHROPIC_MODEL":           hwModel,
		"ANTHROPIC_BASE_URL":        "http://profile-base-url-canary.invalid:4000",
		"ANTHROPIC_AUTH_TOKEN":      "profile-auth-token-canary",
		"CLAUDE_CODE_OAUTH_TOKEN":   "profile-oauth-canary",
		"CLAUDE_CODE_USE_FOUNDRY":   "profile-foundry-switch-canary",
		"ANTHROPIC_FOUNDRY_API_KEY": "profile-foundry-key-canary",
		"HTTP_PROXY":                "http://profile-http-proxy-canary.invalid:3128",
		"HTTPS_PROXY":               "http://profile-https-proxy-canary.invalid:3128",
		"NODE_OPTIONS":              "--require=/profile-node-options-canary.js",
	}
	for k, v := range profile {
		if k != "ANTHROPIC_MODEL" {
			canaries["profile "+k] = v
		}
	}
	windowKeys := map[string]string{}
	for i, k := range config.WindowDecidingEnvKeys {
		v := fmt.Sprint(300000 + 1000*i)
		if k == "CLAUDE_CODE_DISABLE_1M_CONTEXT" || k == "DISABLE_COMPACT" {
			v = "1"
		}
		profile[k], windowKeys[k] = v, v
	}

	if _, err := hwProbe(t, profile); err != nil {
		t.Fatalf("probeHarnessWindow: %v", err)
	}
	for _, dump := range []string{"run.env", "version.env"} {
		raw := f.read(t, dump)
		for where, v := range canaries {
			if strings.Contains(raw, v) {
				t.Errorf("%s: the %s canary %q reached the harness", dump, where, v)
			}
		}
		env := f.env(t, dump)
		for _, k := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_FOUNDRY", "ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_AUTH_TOKEN", "HTTP_PROXY", "HTTPS_PROXY", "NODE_OPTIONS"} {
			if v, ok := env[k]; ok {
				t.Errorf("%s: %s=%q reached the harness; no export outside the allowlist may", dump, k, v)
			}
		}
	}
	env := f.env(t, "run.env")
	for k, v := range windowKeys {
		if env[k] != v {
			t.Errorf("the window key %s reached the harness as %q, want the profile's %q", k, env[k], v)
		}
	}
	if env["ANTHROPIC_MODEL"] != hwModel {
		t.Errorf("ANTHROPIC_MODEL reached the harness as %q, want %q", env["ANTHROPIC_MODEL"], hwModel)
	}
	if !strings.Contains(f.read(t, "run.env"), "NO_PROXY=127.0.0.1,localhost\n") {
		t.Errorf("the harness environment lacks NO_PROXY=127.0.0.1,localhost")
	}
	if base := env["ANTHROPIC_BASE_URL"]; !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want the probe's loopback stub", base)
	}
	if env["ANTHROPIC_API_KEY"] != "af-probe-dummy" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want the probe's dummy key", env["ANTHROPIC_API_KEY"])
	}
}

func TestProbeHarnessWindow_ScratchKeysWinOverProfileExports(t *testing.T) {
	f := newHWFake(t)
	f.answer(t, hwUsage(hwModel, 1000000), "", 0)
	parentPath, parentHome := os.Getenv("PATH"), os.Getenv("HOME")
	profile := map[string]string{
		"ANTHROPIC_MODEL":         hwModel,
		"HOME":                    "/profile-home-canary",
		"PATH":                    "/profile-path-canary",
		"CLAUDE_CODE_USE_BEDROCK": "profile-bedrock-canary",
	}
	if _, err := hwProbe(t, profile); err != nil {
		t.Fatalf("probeHarnessWindow: %v", err)
	}
	for _, dump := range []string{"run.env", "version.env"} {
		env := f.env(t, dump)
		if strings.Contains(f.read(t, dump), "canary") {
			t.Errorf("%s: a profile export reached the harness:\n%s", dump, f.read(t, dump))
		}
		if _, ok := env["CLAUDE_CODE_USE_BEDROCK"]; ok {
			t.Errorf("%s: CLAUDE_CODE_USE_BEDROCK reached the harness", dump)
		}
		if env["PATH"] != parentPath {
			t.Errorf("%s: PATH = %q, want the parent's %q", dump, env["PATH"], parentPath)
		}
		for _, k := range []string{"HOME", "CLAUDE_CONFIG_DIR", "TMPDIR"} {
			if v := env[k]; v == "" || v == parentHome || v == profile["HOME"] {
				t.Errorf("%s: %s = %q, want a scratch directory of the probe", dump, k, v)
			}
		}
	}
}

func TestProbeHarnessWindow_VersionIsTheGatesReduction(t *testing.T) {
	const out = "A newer Claude Code is available; run claude update to install it.\n2.1.322 (Claude Code)\n"
	if got := harnessVersionOf(out); got != "2.1.322" {
		t.Fatalf("harnessVersionOf(%q) = %q, want 2.1.322", out, got)
	}
	f := newHWFake(t)
	f.version(t, out)
	f.answer(t, hwUsage(hwModel, 1000000), "", 0)
	p, err := hwProbe(t, map[string]string{"ANTHROPIC_MODEL": hwModel})
	if err != nil {
		t.Fatalf("probeHarnessWindow: %v", err)
	}
	if p.HarnessVersion != harnessVersionOf(out) {
		t.Errorf("probe HarnessVersion = %q, want the gate's reduction %q of the same --version output", p.HarnessVersion, harnessVersionOf(out))
	}
}

func TestProbeHarnessWindow_StubAnswersEveryRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, stop, err := startHarnessProbeStub(ctx)
	if err != nil {
		t.Fatalf("startHarnessProbeStub: %v", err)
	}
	t.Cleanup(stop)
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("stub base URL %q is not on the loopback", base)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	send := func(t *testing.T, method, path, body string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("%s %s took %v; the stub must answer at once", method, path, d)
		}
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(data)
	}

	t.Run("GET /v1/models is a 404 at once", func(t *testing.T) {
		code, _, body := send(t, http.MethodGet, "/v1/models", "")
		if code != http.StatusNotFound || !strings.Contains(body, `"not_found_error"`) || !strings.Contains(body, "af window probe stub") {
			t.Errorf("GET /v1/models = %d %s, want 404 with the stub's not_found_error", code, body)
		}
	})
	t.Run("POST /v1/messages/count_tokens is a 200", func(t *testing.T) {
		code, _, body := send(t, http.MethodPost, "/v1/messages/count_tokens", `{"model":"`+hwModel+`","messages":[]}`)
		if code != http.StatusOK || strings.ReplaceAll(strings.TrimSpace(body), " ", "") != `{"input_tokens":1}` {
			t.Errorf("POST /v1/messages/count_tokens = %d %s, want 200 {\"input_tokens\":1}", code, body)
		}
	})
	t.Run("POST /v1/messages echoes the model in a six-event turn", func(t *testing.T) {
		code, ctype, body := send(t, http.MethodPost, "/v1/messages", `{"model":"claude-echo-9","stream":true,"messages":[]}`)
		if code != http.StatusOK || !strings.HasPrefix(ctype, "text/event-stream") {
			t.Fatalf("POST /v1/messages = %d %q, want 200 text/event-stream", code, ctype)
		}
		if n := strings.Count(body, "event: "); n != 6 {
			t.Errorf("the turn has %d events, want 6:\n%s", n, body)
		}
		if !strings.Contains(body, `"model":"claude-echo-9"`) {
			t.Errorf("message_start does not echo the requested model:\n%s", body)
		}
	})
}

func TestProbeHarnessWindow_LeavesAgentRuntimeUnchanged(t *testing.T) {
	f := newHWFake(t)
	f.answer(t, hwUsage(hwModel, 1000000), "", 0)
	root := t.TempDir()
	agentDir := config.AgentDir(root, "manager")
	if err := os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, ".runtime", "session_id"), []byte("sess-live\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.StoreDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.StoreDir(root), "mailbox.jsonl"), []byte(`{"to":"manager"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := wdTreeBytes(t, root)
	t.Chdir(agentDir)

	if _, err := hwProbe(t, map[string]string{"ANTHROPIC_MODEL": hwModel}); err != nil {
		t.Fatalf("probeHarnessWindow: %v", err)
	}
	pwd := strings.TrimSpace(f.read(t, "run.pwd"))
	if pwd == agentDir || strings.HasPrefix(pwd, root+string(os.PathSeparator)) {
		t.Errorf("the harness ran in %s, inside the factory; it must run in a scratch directory", pwd)
	}
	if strings.Contains("\n"+f.read(t, "run.ls"), "\n.claude\n") {
		t.Errorf("the harness's working directory %s held a .claude/ when it started", pwd)
	}
	if after := wdTreeBytes(t, root); !reflect.DeepEqual(after, before) {
		t.Errorf("the probe changed the factory (.runtime/session_id, mailbox):\n before %v\n after  %v", before, after)
	}
	for name, dir := range map[string]string{"HOME": f.env(t, "run.env")["HOME"], "pwd": pwd} {
		if dir == "" {
			t.Errorf("the fake recorded no %s", name)
			continue
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("the probe's scratch %s %s still exists after it returned (stat: %v)", name, dir, err)
		}
	}
}

func TestProbeHarnessWindow_NoModelIDReadsTheSingleKey(t *testing.T) {
	profile := map[string]string{config.EnvMaxContextTokens: "1000000"}
	t.Run("one key", func(t *testing.T) {
		f := newHWFake(t)
		f.answer(t, hwUsage("claude-opus-5-5", 1000000), "", 0)
		p, err := hwProbe(t, profile)
		if err != nil {
			t.Fatalf("probeHarnessWindow: %v", err)
		}
		if p.ModelID != "claude-opus-5-5" || p.ContextWindow != 1000000 {
			t.Errorf("probe = %+v, want the single key claude-opus-5-5 at 1000000", p)
		}
		for _, a := range f.argv(t) {
			if a == "--model" {
				t.Errorf("a profile with no ANTHROPIC_MODEL was probed with --model: %q", f.argv(t))
			}
		}
		if v, ok := f.env(t, "run.env")["ANTHROPIC_MODEL"]; ok {
			t.Errorf("a profile with no ANTHROPIC_MODEL exported ANTHROPIC_MODEL=%q to the harness", v)
		}
	})
	t.Run("two keys is an error", func(t *testing.T) {
		f := newHWFake(t)
		f.answer(t, hwUsage("claude-opus-5-5", 1000000, "claude-haiku-5-5", 200000), "", 0)
		if p, err := hwProbe(t, profile); err == nil || p != (harnessWindowProbe{}) {
			t.Errorf("with no requested id, two modelUsage keys gave %+v, %v; want an error", p, err)
		}
	})
}

func TestProbeHarnessWindow_SuffixedModelIDReadsTheSingleKey(t *testing.T) {
	const suffixed = "claude-sonnet-5[1m]"
	f := newHWFake(t)
	f.answer(t, hwUsage("claude-sonnet-5", 1000000), "", 0)
	p, err := hwProbe(t, map[string]string{"ANTHROPIC_MODEL": suffixed, config.EnvMaxContextTokens: "1000000"})
	if err != nil {
		t.Fatalf("probeHarnessWindow: %v", err)
	}
	if p.ModelID != "claude-sonnet-5" || p.ContextWindow != 1000000 {
		t.Errorf("probe = %+v, want the one key claude-sonnet-5 at 1000000", p)
	}
	hwRequireArgs(t, f.argv(t), "--model", suffixed)
}

func TestProbeHarnessWindow_ScratchDirectoriesExistForTheChild(t *testing.T) {
	f := newHWFake(t)
	f.answer(t, hwUsage(hwModel, 1000000), "", 0)
	if _, err := hwProbe(t, map[string]string{"ANTHROPIC_MODEL": hwModel}); err != nil {
		t.Fatalf("probeHarnessWindow: %v", err)
	}
	for _, dump := range []string{"version.dirs", "run.dirs"} {
		got := f.read(t, dump)
		for _, k := range []string{"HOME", "HOME/.claude", "CLAUDE_CONFIG_DIR", "TMPDIR"} {
			if !strings.Contains("\n"+got, "\n"+k+"=dir\n") {
				t.Errorf("%s: the harness started with %s missing; a real install has it:\n%s", dump, k, got)
			}
		}
	}
}

// t25GrandchildScript answers --version, then leaves a child of its own behind and never answers.
const t25GrandchildScript = `#!/bin/bash
d='%s'
if [ "$1" = "--version" ]; then
  cat "$d/version.out"
  exit 0
fi
sleep 300 &
echo $! > "$d/grandchild.pid"
exec sleep 300
`

// t25Alive reports whether pid is still the grandchild's sleep. A killed orphan that PID 1 has not
// reaped yet reads as state Z, and is dead; a reused pid is some other command, and not ours.
func t25Alive(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	_, rest, ok := strings.Cut(string(stat), ") ")
	if !ok || strings.HasPrefix(rest, "Z") || strings.HasPrefix(rest, "X") {
		return false
	}
	cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	return strings.HasPrefix(string(cmdline), "sleep\x00")
}

func TestProbeHarnessWindow_TimeoutKillsTheProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("tells a dead grandchild from a live one through /proc")
	}
	f := newHWFake(t)
	if err := os.WriteFile(filepath.Join(f.dir, "claude"), []byte(fmt.Sprintf(t25GrandchildScript, f.dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	grandchild := func() int {
		raw, err := os.ReadFile(filepath.Join(f.dir, "grandchild.pid"))
		if err != nil {
			return 0
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		return pid
	}
	t.Cleanup(func() {
		if pid := grandchild(); pid > 0 && t25Alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if p, err := probeHarnessWindow(ctx, map[string]string{"ANTHROPIC_MODEL": hwModel}); err == nil {
		t.Fatalf("a harness that never answered was read as a window: %+v", p)
	}
	pid := grandchild()
	if pid <= 0 {
		t.Fatal("the fake claude recorded no grandchild pid; it never reached its run leg")
	}
	for deadline := time.Now().Add(3 * time.Second); t25Alive(pid) && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if t25Alive(pid) {
		t.Errorf("the harness's own child %d outlived the probe's timeout: the probe killed the harness but not its process group", pid)
	}
}
