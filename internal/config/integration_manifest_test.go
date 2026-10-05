package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// imPluginDir creates <tmp>/<dirName>/ holding af-integration.toml = manifest plus the content a
// manifest may point at (run scripts, a Claude Code plugin dir), so a refusal can only come from
// the manifest text itself, never from a missing file.
func imPluginDir(t *testing.T, dirName, manifest string, extraPluginDirs ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), dirName)
	for _, rel := range append([]string{"claude-plugin"}, extraPluginDirs...) {
		pj := filepath.Join(dir, rel, ".claude-plugin", "plugin.json")
		if err := os.MkdirAll(filepath.Dir(pj), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pj, []byte(fmt.Sprintf(`{"name":%q}`, filepath.Base(rel))+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "af"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"install.sh", "check.sh", "serve.sh"} {
		if err := os.WriteFile(filepath.Join(dir, "af", s), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, IntegrationManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const (
	imHeader = "name = \"acme\"\ndescription = \"test integration\"\n"
	imClaude = "\n[claude]\nplugins = [\"claude-plugin\"]\n"
	// imService is a valid tmux-session service block; rows that test [env] or [claude] carry it
	// so the manifest is otherwise acceptable.
	imService = "\n[service]\nsession = \"acme-svc\"\nrun = \"af/serve.sh\"\nprobe = \"tmux-session\"\n"
)

func imService2(session, probe, extra string) string {
	return fmt.Sprintf("\n[service]\nsession = %q\nrun = \"af/serve.sh\"\nprobe = %q\n%s", session, probe, extra)
}

func imEnv(lines ...string) string {
	return "\n[env]\n" + strings.Join(lines, "\n") + "\n"
}

var imDesignCommit = strings.Repeat("0123456789", 4)
var imDesignSHA256 = strings.Repeat("0123456789abcdef", 4)

// imDesignSample is design-doc.md L282-317 verbatim, with the two placeholders ("<40-hex sha>",
// "<64-hex>") replaced by well-formed values.
func imDesignSample() string {
	return `name        = "defenseclaw"
description = "Cisco DefenseClaw: tool-call guard hooks, code and skill scanners, CodeGuard rules"
scope       = "factory"                      # "formula" (default) | "factory" (install needs --factory-wide)

[upstream]
repo   = "https://github.com/cisco-ai-defense/defenseclaw"
commit = "` + imDesignCommit + `"                      # a commit, never a branch or "latest"

[install]
run             = "af/install.sh"            # idempotent; stdin closed; bounded by timeout
timeout         = "10m"
external_writes = ["~/.local/bin/defenseclaw", "~/.local/bin/defenseclaw-gateway", "~/.defenseclaw/"]
shared          = true                       # host-global install: an existing one is adopted (hashed, recorded), not refused
[[install.artifacts]]
url    = "https://github.com/cisco-ai-defense/defenseclaw/releases/download/v0.8.10/defenseclaw-linux-arm64"
sha256 = "` + imDesignSHA256 + `"

[check]
run       = "af/check.sh"                    # exit 0 = healthy; also asserts action mode, not observe
timeout   = "30s"
fresh_for = "0"                              # re-run at every sling/up launch of a formula-scope binding (default)

[service]
session = "defenseclaw"                      # not af-*, not litellm/telemetry/dispatch/operator/watchdog
run     = "af/serve.sh"
probe   = "http-healthz"                     # "tmux-session" | "http-healthz" (+ healthz_url) — liveness only; [check] reports, never kills
healthz_url = "http://127.0.0.1:18970/healthz" # DefenseClaw's loopback API [X10]

[claude]
plugins        = ["claude-plugin"]           # dirs containing .claude-plugin/plugin.json; at most 8
hook_fail_mode = "open"                      # "open" (default) | "closed" — declared, exported as one fixed key and reported; enforced only by hooks that read it

[env]
DEFENSECLAW_TOKEN = "file:.agentfactory/secrets/defenseclaw.token"
`
}

// TestLoadIntegrationManifest_Refusals pins K2's strict manifest rules (spec L186-231; design
// L337-346; decisions D5, D10-D12, D29, D34). Every row asserts BOTH that no partial manifest is
// returned and that the error names the offending key, value or field.
func TestLoadIntegrationManifest_Refusals(t *testing.T) {
	longSession := "s" + strings.Repeat("x", 64) // 65 chars (D29: at most 64)
	ninePlugins := []string{}
	for i := 1; i <= 9; i++ {
		ninePlugins = append(ninePlugins, fmt.Sprintf("p%d", i))
	}
	quoted := func(ss []string) string {
		q := make([]string, len(ss))
		for i, s := range ss {
			q[i] = fmt.Sprintf("%q", s)
		}
		return strings.Join(q, ", ")
	}

	rows := []struct {
		name      string
		dirName   string
		manifest  string
		extraDirs []string
		want      []string // every substring must appear in the error
		notWant   []string // none may appear
	}{
		// --- strict decode: unknown keys (spec L187-189; design L339) ---
		{name: "unknown_table_did_you_mean",
			manifest: imHeader + "\n[servcie]\nsession = \"acme-svc\"\nrun = \"af/serve.sh\"\nprobe = \"tmux-session\"\n",
			want:     []string{`af-integration.toml: unknown key "servcie" (did you mean "service"?)`}},
		{name: "unknown_nested_key_did_you_mean",
			manifest: imHeader + "\n[service]\nsesion = \"acme-svc\"\nsession = \"acme-svc\"\nrun = \"af/serve.sh\"\nprobe = \"tmux-session\"\n",
			want:     []string{"af-integration.toml: unknown key", "sesion", `(did you mean "session"?)`}},
		{name: "unknown_key_no_near_match",
			manifest: imHeader + "zzqq_field = \"x\"\n" + imService,
			want:     []string{`af-integration.toml: unknown key "zzqq_field"`}},

		// --- [service] probe (spec L203-205; design L339, L463) ---
		{name: "service_missing_probe",
			manifest: imHeader + "\n[service]\nsession = \"acme-svc\"\nrun = \"af/serve.sh\"\n",
			want:     []string{`[service] needs probe = "tmux-session" or "http-healthz"`}},
		{name: "service_probe_check",
			manifest: imHeader + "\n[check]\nrun = \"af/check.sh\"\n" + imService2("acme-svc", "check", ""),
			want:     []string{"[service]", "probe"}},
		{name: "service_probe_unknown_kind",
			manifest: imHeader + imService2("acme-svc", "pgrep", ""),
			want:     []string{"[service]", "probe"}},
		{name: "healthz_missing_url",
			manifest: imHeader + imService2("acme-svc", "http-healthz", ""),
			want:     []string{"healthz_url"}},
		{name: "healthz_non_loopback",
			manifest: imHeader + imService2("acme-svc", "http-healthz", "healthz_url = \"http://example.com:18970/healthz\"\n"),
			want:     []string{"healthz_url", "example.com"}},
		{name: "healthz_non_http_scheme",
			manifest: imHeader + imService2("acme-svc", "http-healthz", "healthz_url = \"ftp://127.0.0.1:18970/healthz\"\n"),
			want:     []string{"healthz_url"}},
		{name: "healthz_url_with_tmux_probe",
			manifest: imHeader + imService2("acme-svc", "tmux-session", "healthz_url = \"http://127.0.0.1:18970/healthz\"\n"),
			want:     []string{"healthz_url"}},

		// --- [service] session naming (spec L206-209; B14; D29; D34) ---
		{name: "service_session_af_prefix",
			manifest: imHeader + imService2("af-x", "tmux-session", ""),
			want:     []string{"af-x"}},
		{name: "service_session_af_test_prefix",
			manifest: imHeader + imService2("af-test-x", "tmux-session", ""),
			want:     []string{"af-test-x"}},
		{name: "service_session_telemetry_prefix",
			manifest: imHeader + imService2("telemetry-x", "tmux-session", ""),
			want:     []string{"telemetry-x"}},
		{name: "service_session_telemetry_exact",
			manifest: imHeader + imService2("telemetry", "tmux-session", ""),
			want:     []string{"telemetry"}},
		{name: "service_session_litellm_prefix",
			manifest: imHeader + imService2("litellm-x", "tmux-session", ""),
			want:     []string{"litellm-x"}},
		{name: "service_session_reserved_watchdog",
			manifest: imHeader + imService2("watchdog", "tmux-session", ""),
			want:     []string{"watchdog"}},
		{name: "service_session_reserved_operator",
			manifest: imHeader + imService2("operator", "tmux-session", ""),
			want:     []string{"operator"}},
		{name: "service_session_charset",
			manifest: imHeader + imService2("acme.svc", "tmux-session", ""),
			want:     []string{"acme.svc"}},
		{name: "service_session_too_long",
			manifest: imHeader + imService2(longSession, "tmux-session", ""),
			want:     []string{longSession}},
		{name: "service_session_empty",
			manifest: imHeader + imService2("", "tmux-session", ""),
			want:     []string{"session"}},
		{name: "service_run_empty",
			manifest: imHeader + "\n[service]\nsession = \"acme-svc\"\nrun = \"\"\nprobe = \"tmux-session\"\n",
			want:     []string{"run"}},

		// --- [env] key rules (spec L211-231; design L345; D11 AF_ACTOR; D12 multi-key) ---
		{name: "env_PATH_toolchain",
			manifest: imHeader + imService + imEnv(`PATH = "/opt/bin"`),
			want:     []string{"af-integration.toml: [env] may not set PATH (toolchain key)"}},
		{name: "env_AF_ROLE_identity",
			manifest: imHeader + imService + imEnv(`AF_ROLE = "manager"`),
			want:     []string{"af-integration.toml: [env]", "AF_ROLE"}},
		{name: "env_AF_ACTOR_identity",
			manifest: imHeader + imService + imEnv(`AF_ACTOR = "manager"`),
			want:     []string{"af-integration.toml: [env]", "AF_ACTOR"}},
		{name: "env_ANTHROPIC_BASE_URL_redirect",
			manifest: imHeader + imService + imEnv(`ANTHROPIC_BASE_URL = "http://127.0.0.1:4000"`),
			want:     []string{"af-integration.toml: [env]", "ANTHROPIC_BASE_URL belongs to models.json profiles"}},
		{name: "env_CLAUDE_CONFIG_DIR_toolchain",
			manifest: imHeader + imService + imEnv(`CLAUDE_CONFIG_DIR = "/x"`),
			want:     []string{"af-integration.toml: [env] may not set CLAUDE_CONFIG_DIR (toolchain key)"}},
		{name: "env_CLAUDE_CODE_PLUGIN_DIRS_toolchain",
			manifest: imHeader + imService + imEnv(`CLAUDE_CODE_PLUGIN_DIRS = "/x"`),
			want:     []string{"af-integration.toml: [env] may not set CLAUDE_CODE_PLUGIN_DIRS (toolchain key)"}},
		{name: "env_LD_AUDIT_derived_shell_critical",
			manifest: imHeader + imService + imEnv(`LD_AUDIT = "/x.so"`),
			want:     []string{"af-integration.toml: [env] may not set LD_AUDIT (toolchain key)"}},
		{name: "env_CLAUDE_CODE_SUBAGENT_MODEL_derived_redirect",
			manifest: imHeader + imService + imEnv(`CLAUDE_CODE_SUBAGENT_MODEL = "x"`),
			want:     []string{"af-integration.toml: [env]", "CLAUDE_CODE_SUBAGENT_MODEL belongs to models.json profiles"}},
		{name: "env_telemetry_key",
			manifest: imHeader + imService + imEnv(`OTEL_EXPORTER_OTLP_ENDPOINT = "http://x"`),
			want:     []string{"af-integration.toml: [env]", "OTEL_EXPORTER_OTLP_ENDPOINT"}},
		{name: "env_gateway_upstream_key",
			manifest: imHeader + imService + imEnv(`OPENAI_API_KEY = "file:.agentfactory/secrets/o.key"`),
			want:     []string{"af-integration.toml: [env]", "OPENAI_API_KEY"}},
		{name: "env_multiple_denied_keys_all_named",
			manifest: imHeader + imService + imEnv(`PATH = "/opt/bin"`, `AF_ROLE = "manager"`),
			want:     []string{"af-integration.toml: [env]", "PATH", "AF_ROLE"}},
		{name: "env_design_L345_two_keys_verbatim",
			manifest: imHeader + imService + imEnv(`PATH = "/opt/bin"`, `ANTHROPIC_BASE_URL = "http://127.0.0.1:4000"`),
			want:     []string{"af-integration.toml: [env] may not set PATH (toolchain key); ANTHROPIC_BASE_URL belongs to models.json profiles"}},
		{name: "env_invalid_key_name",
			manifest: imHeader + imService + imEnv(`"MY-KEY" = "x"`),
			want:     []string{"MY-KEY"}},
		{name: "env_credential_literal",
			manifest: imHeader + imService + imEnv(`MY_TOKEN = "sk-live-abcdefghijklmnop0123"`),
			want:     []string{"MY_TOKEN"},
			notWant:  []string{"sk-live-abcdefghijklmnop0123"}},
		{name: "env_file_ref_outside_allowed_roots",
			manifest: imHeader + imService + imEnv(`MY_TOKEN = "file:/etc/passwd"`),
			want:     []string{"MY_TOKEN"}},
		{name: "env_non_string_value",
			manifest: imHeader + imService + imEnv(`FOO = 1`),
			want:     []string{"FOO"}},
		{name: "env_duplicate_key",
			manifest: imHeader + imService + imEnv(`FOO = "a"`, `FOO = "b"`),
			want:     []string{"FOO"}},

		// --- [claude] (spec L210) ---
		{name: "claude_nine_plugins_over_cap",
			manifest:  imHeader + imService + "\n[claude]\nplugins = [" + quoted(ninePlugins) + "]\n",
			extraDirs: ninePlugins,
			want:      []string{"plugins", "8"}},
		{name: "claude_plugin_path_escape",
			manifest: imHeader + imService + "\n[claude]\nplugins = [\"../outside\"]\n",
			want:     []string{"../outside"}},
		{name: "claude_plugin_path_absolute",
			manifest: imHeader + imService + "\n[claude]\nplugins = [\"/etc\"]\n",
			want:     []string{"/etc"}},
		{name: "claude_hook_fail_mode_invalid",
			manifest: imHeader + imService + "\n[claude]\nplugins = [\"claude-plugin\"]\nhook_fail_mode = \"maybe\"\n",
			want:     []string{"hook_fail_mode"}},

		// --- name / scope (spec L192, L202) ---
		{name: "name_differs_from_dir",
			manifest: "name = \"other\"\n" + imService,
			want:     []string{"other", "acme"}},
		{name: "name_invalid_agent_name", dirName: "9bad",
			manifest: "name = \"9bad\"\n" + imService,
			want:     []string{"9bad"}},
		{name: "scope_invalid",
			manifest: imHeader + "scope = \"global\"\n" + imService,
			want:     []string{"scope", "global"}},

		// --- [install] / [check] / [upstream] (D5, D34; spec L245-246) ---
		{name: "install_missing_run",
			manifest: imHeader + "\n[install]\ntimeout = \"10m\"\n",
			want:     []string{"[install]", "run"}},
		{name: "install_timeout_zero",
			manifest: imHeader + "\n[install]\nrun = \"af/install.sh\"\ntimeout = \"0\"\n",
			want:     []string{"timeout"}},
		{name: "install_timeout_bad_grammar",
			manifest: imHeader + "\n[install]\nrun = \"af/install.sh\"\ntimeout = \"1w\"\n",
			want:     []string{"timeout", `"1w"`}},
		{name: "check_timeout_zero",
			manifest: imHeader + "\n[check]\nrun = \"af/check.sh\"\ntimeout = \"0\"\n",
			want:     []string{"timeout"}},
		{name: "check_fresh_for_bad_grammar",
			manifest: imHeader + "\n[check]\nrun = \"af/check.sh\"\nfresh_for = \"5x\"\n",
			want:     []string{"fresh_for", `"5x"`}},
		{name: "upstream_commit_is_branch",
			manifest: imHeader + "\n[upstream]\nrepo = \"https://github.com/acme/up\"\ncommit = \"main\"\n",
			want:     []string{"commit", "main"}},
		{name: "upstream_commit_uppercase_hex",
			manifest: imHeader + "\n[upstream]\nrepo = \"https://github.com/acme/up\"\ncommit = \"" + strings.ToUpper(strings.Repeat("abcdef0123", 4)) + "\"\n",
			want:     []string{"commit"}},
		{name: "install_external_write_glob",
			manifest: imHeader + "\n[install]\nrun = \"af/install.sh\"\nexternal_writes = [\"~/.local/bin/*\"]\n",
			want:     []string{"external_writes", "~/.local/bin/*"}},
		{name: "install_external_write_relative",
			manifest: imHeader + "\n[install]\nrun = \"af/install.sh\"\nexternal_writes = [\"bin/acme\"]\n",
			want:     []string{"external_writes", "bin/acme"}},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			dirName := r.dirName
			if dirName == "" {
				dirName = "acme"
			}
			dir := imPluginDir(t, dirName, r.manifest, r.extraDirs...)
			m, err := LoadIntegrationManifest(dir)
			if m != nil {
				t.Errorf("a refused manifest must not be returned (no partial struct); got %+v", m)
			}
			if err == nil {
				t.Fatalf("manifest was accepted; want a refusal naming %q\n%s", r.want, r.manifest)
			}
			for _, w := range r.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal must contain %q:\n got: %v", w, err)
				}
			}
			for _, nw := range r.notWant {
				if strings.Contains(err.Error(), nw) {
					t.Errorf("refusal must not echo %q: %v", nw, err)
				}
			}
			if r.name == "unknown_table_did_you_mean" && strings.Count(err.Error(), "unknown key") != 1 {
				t.Errorf("an unknown table must be reported once (Undecoded() also lists its sub-keys): %v", err)
			}
		})
	}
}

// TestLoadIntegrationManifest_AcceptsDesignSample is the positive half: the design's own sample
// manifest (design L282-317; the strict-decode schema) loads with every field decoded, and the
// edge values the spec and decisions permit are accepted.
func TestLoadIntegrationManifest_AcceptsDesignSample(t *testing.T) {
	t.Run("design_sample", func(t *testing.T) {
		dir := imPluginDir(t, "defenseclaw", imDesignSample())
		m, err := LoadIntegrationManifest(dir)
		if err != nil {
			t.Fatalf("the design's sample manifest must load: %v", err)
		}
		if m == nil {
			t.Fatal("LoadIntegrationManifest returned nil manifest with nil error")
		}
		if m.Name != "defenseclaw" || m.Scope != "factory" || !strings.HasPrefix(m.Description, "Cisco DefenseClaw") {
			t.Errorf("top-level fields: name=%q scope=%q description=%q", m.Name, m.Scope, m.Description)
		}
		if m.Upstream == nil || m.Upstream.Repo != "https://github.com/cisco-ai-defense/defenseclaw" || m.Upstream.Commit != imDesignCommit {
			t.Errorf("[upstream] = %+v", m.Upstream)
		}
		wantWrites := []string{"~/.local/bin/defenseclaw", "~/.local/bin/defenseclaw-gateway", "~/.defenseclaw/"}
		if m.Install == nil || m.Install.Run != "af/install.sh" || m.Install.Timeout != "10m" || !m.Install.Shared || !reflect.DeepEqual(m.Install.ExternalWrites, wantWrites) {
			t.Errorf("[install] = %+v", m.Install)
		} else if len(m.Install.Artifacts) != 1 || m.Install.Artifacts[0].SHA256 != imDesignSHA256 || !strings.HasSuffix(m.Install.Artifacts[0].URL, "defenseclaw-linux-arm64") {
			t.Errorf("[[install.artifacts]] = %+v", m.Install.Artifacts)
		}
		if m.Check == nil || m.Check.Run != "af/check.sh" || m.Check.Timeout != "30s" || m.Check.FreshFor != "0" {
			t.Errorf("[check] = %+v", m.Check)
		}
		if m.Service == nil || m.Service.Session != "defenseclaw" || m.Service.Run != "af/serve.sh" || m.Service.Probe != "http-healthz" || m.Service.HealthzURL != "http://127.0.0.1:18970/healthz" {
			t.Errorf("[service] = %+v", m.Service)
		}
		if m.Claude == nil || !reflect.DeepEqual(m.Claude.Plugins, []string{"claude-plugin"}) || m.Claude.HookFailMode != "open" {
			t.Errorf("[claude] = %+v", m.Claude)
		}
		if !reflect.DeepEqual(m.Env, map[string]string{"DEFENSECLAW_TOKEN": "file:.agentfactory/secrets/defenseclaw.token"}) {
			t.Errorf("[env] = %v", m.Env)
		}
	})

	t.Run("file_ref_under_the_plugin_dir", func(t *testing.T) {
		// Spec L227-228: a credential file: ref may live under .agentfactory/secrets/ or the plugin dir.
		ref := "file:.agentfactory/store/plugins/acme/secrets/acme.token"
		dir := imPluginDir(t, "acme", imHeader+imService+imEnv(`ACME_TOKEN = "`+ref+`"`))
		m, err := LoadIntegrationManifest(dir)
		if err != nil || m == nil {
			t.Fatalf("a file: ref under the plugin dir (store/plugins/acme/) must load: %v, %v", m, err)
		}
		if m.Env["ACME_TOKEN"] != ref {
			t.Errorf("[env] = %v", m.Env)
		}
		other := imPluginDir(t, "acme", imHeader+imService+imEnv(`ACME_TOKEN = "file:.agentfactory/store/plugins/other/acme.token"`))
		if _, err := LoadIntegrationManifest(other); err == nil || !strings.Contains(err.Error(), "ACME_TOKEN") {
			t.Errorf("a file: ref under another plugin's dir must be refused: %v", err)
		}
	})

	t.Run("named_loader_for_snapshot_dir", func(t *testing.T) {
		// D1: staged (.tmp-*) and snapshot (<sha>) dirs are not named after the integration.
		dir := imPluginDir(t, strings.Repeat("ab", 32), imDesignSample())
		m, err := LoadIntegrationManifestNamed(dir, "defenseclaw")
		if err != nil || m == nil {
			t.Fatalf("LoadIntegrationManifestNamed(<sha dir>, \"defenseclaw\") = %v, %v; want the manifest", m, err)
		}
		if m.Name != "defenseclaw" {
			t.Errorf("name = %q", m.Name)
		}
		if m2, err := LoadIntegrationManifestNamed(dir, "other"); err == nil || m2 != nil {
			t.Errorf("LoadIntegrationManifestNamed with the wrong expected name must refuse; got %v, %v", m2, err)
		} else if !strings.Contains(err.Error(), "other") {
			t.Errorf("wrong-name refusal must name the expected name: %v", err)
		}
		if m3, err := LoadIntegrationManifest(dir); err == nil || m3 != nil {
			t.Errorf("LoadIntegrationManifest on a dir not named after the integration must refuse; got %v, %v", m3, err)
		}
	})

	t.Run("edge_values_accepted", func(t *testing.T) {
		eight := []string{}
		for i := 1; i <= 8; i++ {
			eight = append(eight, fmt.Sprintf("p%d", i))
		}
		q := make([]string, len(eight))
		for i, s := range eight {
			q[i] = fmt.Sprintf("%q", s)
		}
		manifest := imHeader +
			"\n[check]\nrun = \"af/check.sh\"\nfresh_for = \"0\"\n" + // D5: fresh_for "0" stays valid; timeout omitted takes the default
			"\n[install]\nrun = \"af/install.sh\"\n" + // D5: timeout omitted
			imService + // tmux-session probe needs no healthz_url
			"\n[claude]\nplugins = [" + strings.Join(q, ", ") + "]\nhook_fail_mode = \"closed\"\n" + // exactly 8: at the cap
			imEnv(`ACME_TOKEN = "file:.agentfactory/secrets/acme.token"`, `ACME_MODE = "strict"`)
		dir := imPluginDir(t, "acme", manifest, eight...)
		m, err := LoadIntegrationManifest(dir)
		if err != nil || m == nil {
			t.Fatalf("edge-value manifest must load: %v, %v\n%s", m, err, manifest)
		}
		if m.Claude == nil || len(m.Claude.Plugins) != 8 {
			t.Errorf("[claude] plugins = %+v, want 8", m.Claude)
		}
	})
}

// TestShellCriticalAndRedirectFamilyAreConfigOwned pins Lift B (spec L217-224, L322; AC 3): the
// shell-critical and redirect-family names are owned once, as exported config literals, and the
// integration [env] denylist is derived from them (so it also refuses IFS, LD_AUDIT and every
// redirect-family model key).
func TestShellCriticalAndRedirectFamilyAreConfigOwned(t *testing.T) {
	wantShell := []string{"PATH", "HOME", "SHELL", "IFS", "LD_LIBRARY_PATH", "LD_PRELOAD", "LD_AUDIT"}
	// session.go:82-98's exact order; the launch line emits KEY='' in this order.
	wantRedirect := []string{
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_FABLE_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
	}

	t.Run("shell_critical_members", func(t *testing.T) {
		got := append([]string{}, ShellCriticalEnvVars...)
		sort.Strings(got)
		want := append([]string{}, wantShell...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("config.ShellCriticalEnvVars = %v, want the member set %v (session.go:197-205)", ShellCriticalEnvVars, want)
		}
	})

	t.Run("redirect_family_order", func(t *testing.T) {
		if !reflect.DeepEqual(RedirectFamilyEnvVars, wantRedirect) {
			t.Errorf("config.RedirectFamilyEnvVars =\n %v\nwant (session.go:82-98 order)\n %v", RedirectFamilyEnvVars, wantRedirect)
		}
	})

	t.Run("single_owner_literals", func(t *testing.T) {
		// AC 3's grep, in-suite: each moved shell-critical name appears on exactly one line across
		// the non-test config sources (a doc comment quoting one would count too).
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		keys := []string{"IFS", "LD_PRELOAD", "LD_AUDIT", "LD_LIBRARY_PATH"}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
				continue
			}
			f, err := os.Open(n)
			if err != nil {
				t.Fatal(err)
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
			for sc.Scan() {
				for _, k := range keys {
					if strings.Contains(sc.Text(), `"`+k+`"`) {
						counts[k]++
					}
				}
			}
			f.Close()
		}
		for _, k := range keys {
			if counts[k] != 1 {
				t.Errorf("%q appears on %d lines of non-test internal/config/*.go, want exactly 1 (one owner, Lift B)", k, counts[k])
			}
		}
	})

	t.Run("env_denylist_derived", func(t *testing.T) {
		all := append(append(append([]string{}, wantShell...), wantRedirect...), "CLAUDE_CODE_PLUGIN_DIRS", "CLAUDE_CONFIG_DIR")
		for _, k := range all {
			t.Run(k, func(t *testing.T) {
				dir := imPluginDir(t, "acme", imHeader+imService+imEnv(k+` = "x"`))
				m, err := LoadIntegrationManifest(dir)
				if err == nil || m != nil {
					t.Fatalf("[env] %s must be refused (derived denylist); got %v, %v", k, m, err)
				}
				if !regexp.MustCompile(`\b` + regexp.QuoteMeta(k) + `\b`).MatchString(err.Error()) {
					t.Errorf("[env] %s refusal must name the key: %v", k, err)
				}
			})
		}
	})
}
