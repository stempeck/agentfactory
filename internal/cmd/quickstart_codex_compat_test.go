package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The ChatGPT/Codex backend rejects system-role input items, and LiteLLM 1.93.0 forwards the
// block-array system prompt Claude Code sends on every turn as exactly that item (measured
// 2026-09-22; ADR-024). quickstart carries the fix as an af-owned LiteLLM pre-call hook that the
// subscription yaml names. These tests pin the pieces: the seed names the hook and the api-key
// seed does not; setup_litellm writes the hook and wires an older yaml; the writer is idempotent;
// the migration is idempotent and never edits an operator-managed callbacks entry; and the hook
// itself behaves, run under python3 against a stub CustomLogger (ADR-018: no network, no pip).

func TestQuickstartSubscriptionSeedNamesTheCodexCompatHook(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	sub := subscriptionSeedBlock(t, setup)
	settings := strings.Index(sub, "litellm_settings:")
	hook := strings.Index(sub, "callbacks: af_codex_compat.proxy_handler_instance")
	if hook < 0 {
		t.Fatal("the subscription seed must name af_codex_compat.proxy_handler_instance under litellm_settings.callbacks")
	}
	if settings < 0 || hook < settings {
		t.Error("the callbacks line must sit under the litellm_settings block; LiteLLM reads callbacks from nowhere else")
	}
	if strings.Contains(litellmSeedBlock(t, setup), "af_codex_compat") {
		t.Error("the api-key seed must not name the hook: it has no chatgpt/ lane for the hook to serve")
	}
}

func TestQuickstartSubscriptionBranchWritesAndWiresTheHook(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	for _, call := range []string{
		`_ensure_codex_compat_wiring ".agentfactory/litellm.codex-subscription.yaml" || exit 1`,
		`_write_codex_compat_module "$factory_root"`,
	} {
		if !strings.Contains(setup, call) {
			t.Errorf("setup_litellm() must call %s in the subscription branch", call)
		}
	}
	content := quickstartScriptContent(t)
	for _, fn := range []string{"_write_codex_compat_module", "_ensure_codex_compat_wiring"} {
		if extractShellFunction(content, fn) == "" {
			t.Errorf("quickstart.sh must define %s()", fn)
		}
	}
}

// codexCompatHarness extracts the named helpers and their logging dependencies from the REAL
// quickstart.sh so the tests below run the shipped logic against real files, never a pasted copy.
func codexCompatHarness(t *testing.T, content string, names ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("RED='' GREEN='' YELLOW='' BLUE='' NC=''\n")
	for _, n := range append([]string{"log_info", "log_warn", "log_error"}, names...) {
		fn := extractShellFunction(content, n)
		if fn == "" {
			t.Fatalf("could not extract %s() from quickstart.sh", n)
		}
		b.WriteString(fn)
		b.WriteString("\n")
	}
	return b.String()
}

func runCodexCompatScript(t *testing.T, dir, script string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestQuickstartEnsureCodexCompatWiring(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := codexCompatHarness(t, content, "_ensure_codex_compat_wiring")
	const wired = "  callbacks: af_codex_compat.proxy_handler_instance"
	const lanes = "model_list:\n  - model_name: a\n    litellm_params:\n      model: chatgpt/responses/a\n"
	cases := []struct {
		name         string
		before       string
		wantErr      bool
		wantSaved    bool
		wantContains string
	}{
		{"pre-hook seed gains the line under litellm_settings", lanes + "\nlitellm_settings:\n  drop_params: true\n\ngeneral_settings:\n  master_key: os.environ/LITELLM_MASTER_KEY\n",
			false, true, "litellm_settings:\n" + wired + "\n  drop_params: true\n"},
		{"already wired file is left alone", lanes + "\nlitellm_settings:\n" + wired + "\n  drop_params: true\n", false, false, ""},
		{"no litellm_settings block gets one appended", lanes, false, true, "\nlitellm_settings:\n" + wired + "\n"},
		{"operator-managed callbacks ends loud and untouched", lanes + "\nlitellm_settings:\n  callbacks: [\"prometheus\"]\n", true, false, ""},
		{"commented header gains the line and keeps the comment", lanes + "\nlitellm_settings:  # gateway\n  drop_params: true\n\ngeneral_settings:\n  master_key: os.environ/LITELLM_MASTER_KEY\n",
			false, true, "litellm_settings:  # gateway\n" + wired + "\n  drop_params: true\n"},
		{"flow-style litellm_settings ends loud and untouched", lanes + "\nlitellm_settings: {drop_params: true}\n", true, false, ""},
		{"anchored litellm_settings ends loud and untouched", lanes + "\nlitellm_settings: &settings\n  drop_params: true\n", true, false, ""},
		{"quoted litellm_settings key ends loud and untouched", lanes + "\n\"litellm_settings\":\n  drop_params: true\n", true, false, ""},
		{"spaced litellm_settings key ends loud and untouched", lanes + "\nlitellm_settings :\n  drop_params: true\n", true, false, ""},
		{"commented-out block is not a header", lanes + "\n# litellm_settings:\n#   drop_params: true\n", false, true, "\nlitellm_settings:\n" + wired + "\n"},
	}
	topLevelSettings := regexp.MustCompile(`(?m)^litellm_settings\s*:`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "litellm.codex-subscription.yaml")
			if err := os.WriteFile(target, []byte(tc.before), 0o644); err != nil {
				t.Fatal(err)
			}
			call := harness + "_ensure_codex_compat_wiring " + shellQuote(target) + "\n"
			out, err := runCodexCompatScript(t, dir, call)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, want error=%t; out=%s", err, tc.wantErr, out)
			}
			after, _ := os.ReadFile(target)
			saved, _ := filepath.Glob(target + ".*.saved")
			if tc.wantSaved != (len(saved) == 1) {
				t.Errorf("save-aside copies = %d, want one=%t; out=%s", len(saved), tc.wantSaved, out)
			}
			if tc.wantErr {
				if string(after) != tc.before {
					t.Error("a loud refusal must leave the operator's file byte-unchanged")
				}
				if !strings.Contains(out, "af_codex_compat.proxy_handler_instance") {
					t.Errorf("the refusal must name the exact entry to add; out=%s", out)
				}
				return
			}
			if n := len(topLevelSettings.FindAllIndex(after, -1)); n > 1 {
				t.Errorf("after has %d top-level litellm_settings keys; YAML keeps only the last, silently dropping the operator's settings. out=%s\nafter=%s", n, out, after)
			}
			if tc.wantContains != "" && !strings.Contains(string(after), tc.wantContains) {
				t.Errorf("after=%q, want it to contain %q", after, tc.wantContains)
			}
			if !tc.wantSaved && string(after) != tc.before {
				t.Error("an already-wired file must be byte-unchanged")
			}
			if tc.wantSaved {
				if body, _ := os.ReadFile(saved[0]); string(body) != tc.before {
					t.Error("the save-aside must hold the previous content verbatim")
				}
				if out, err := runCodexCompatScript(t, dir, call); err != nil {
					t.Fatalf("second run: %v: %s", err, out)
				}
				again, _ := os.ReadFile(target)
				saved2, _ := filepath.Glob(target + ".*.saved")
				if string(again) != string(after) || len(saved2) != 1 {
					t.Error("a second run must be a no-op: same content, no further save-aside")
				}
			}
		})
	}
	t.Run("missing file is nothing to do", func(t *testing.T) {
		dir := t.TempDir()
		if out, err := runCodexCompatScript(t, dir, harness+"_ensure_codex_compat_wiring "+shellQuote(filepath.Join(dir, "absent.yaml"))+"\n"); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	})
}

func TestQuickstartCodexCompatHookIsWrittenIdempotentlyAndBehaves(t *testing.T) {
	content := quickstartScriptContent(t)
	// tmux is stubbed absent: the writer's restart branch needs a live gateway session, and this
	// harness must never touch one (ADR-018).
	harness := codexCompatHarness(t, content, "_write_codex_compat_module") + "tmux() { return 1; }\n"
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	call := harness + "_write_codex_compat_module " + shellQuote(root) + "\n"
	out, err := runCodexCompatScript(t, root, call)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	module := filepath.Join(root, ".agentfactory", "af_codex_compat.py")
	first, err := os.ReadFile(module)
	if err != nil {
		t.Fatalf("the writer must leave the hook next to the yaml, where LiteLLM resolves callbacks: %v", err)
	}
	if !strings.Contains(out, "Wrote ") {
		t.Errorf("the first write must say so; out=%s", out)
	}
	out, err = runCodexCompatScript(t, root, call)
	if err != nil {
		t.Fatalf("second write: %v: %s", err, out)
	}
	if strings.Contains(out, "Wrote ") {
		t.Errorf("an unchanged hook must not be rewritten (a rewrite would restart a live gateway for nothing); out=%s", out)
	}
	if again, _ := os.ReadFile(module); string(again) != string(first) {
		t.Error("the hook must be byte-stable across bootstraps")
	}
	if strays, _ := filepath.Glob(filepath.Join(root, ".agentfactory", ".af_codex_compat.*")); len(strays) != 0 {
		t.Errorf("temp files left behind: %v", strays)
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH; the hook's behaviour needs an interpreter")
	}
	stub := t.TempDir()
	for path, body := range map[string]string{
		"litellm/__init__.py":                   "",
		"litellm/integrations/__init__.py":      "",
		"litellm/integrations/custom_logger.py": "class CustomLogger:\n    pass\n",
	} {
		full := filepath.Join(stub, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const script = `
import asyncio, sys
sys.path.insert(0, sys.argv[1]); sys.path.insert(0, sys.argv[2])
import af_codex_compat as m
hook = m.proxy_handler_instance
def run(data, call_type="anthropic_messages"):
    return asyncio.run(hook.async_pre_call_hook(None, None, data, call_type))
blocks = [{"type": "text", "text": "You are Claude Code.", "cache_control": {"type": "ephemeral"}},
          {"type": "text", "text": "Second block."}]
out = run({"system": list(blocks), "messages": []})
assert out["system"] == "You are Claude Code.\n\nSecond block.", out
out = run({"system": "already a string", "messages": []})
assert out["system"] == "already a string", out
out = run({"messages": []})
assert "system" not in out, out
out = run({"system": list(blocks), "messages": []}, call_type="completion")
assert isinstance(out["system"], list), out
out = run({"system": [{"type": "text", "text": ""}], "messages": []})
assert "system" not in out, out
print("hook ok")
`
	behaviour, err := exec.Command(python, "-c", script, stub, filepath.Join(root, ".agentfactory")).CombinedOutput()
	if err != nil || !strings.Contains(string(behaviour), "hook ok") {
		t.Fatalf("hook behaviour under python3: %v\n%s", err, behaviour)
	}
}
