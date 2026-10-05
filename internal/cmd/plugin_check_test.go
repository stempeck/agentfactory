package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// ---- AC7: af plugin check (L630-643; decisions D23, D24) -------------------

// intBCheckKeys is the exact check-record key set (L632-634), plus the content hash Phase 3's admission
// matches against the entry (D1).
var intBCheckKeys = []string{"at", "claude_code_version", "content_sha256", "duration_ms", "exit", "hook_fail_mode", "output", "plugin", "service_rss_kb", "state", "v"}

// intBCheckRowKeys is the exact --json result-row key set (design L352, L641-642).
var intBCheckRowKeys = []string{"duration_ms", "exit", "hook_fail_mode", "output", "plugin", "service_rss_kb", "state"}

const (
	intBPlantedSK     = "sk-abcdefghijklmnop"
	intBPlantedBearer = "xyz123secret"
	intBPlantedFile   = "s3cr3t-value-xyz"
)

func intBCheckRecordPath(root, name string) string {
	return filepath.Join(root, ".runtime", "integration_check", name+".json")
}

// intBCheckFixture acquires and records an installed integration whose [check] script is body.
// It also plants a file: secret under .agentfactory/secrets/ referenced from [env] (D10).
func intBCheckFixture(t *testing.T, e *intBEnv, name, body string, o intBManifestOpts) {
	t.Helper()
	secret := filepath.Join(config.ConfigDir(e.root), "secrets", name+"-token")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte(intBPlantedFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	relSecret, err := filepath.Rel(e.root, secret)
	if err != nil {
		t.Fatal(err)
	}
	o.extra += intBEnvPrefix(name) + "_TOKEN = \"file:" + filepath.ToSlash(relSecret) + "\"\n"
	files := intBSource(e, name, o)
	files["af/check.sh"] = intBFile{strings.ReplaceAll(body, "@SECRET@", secret), 0o755}
	intBAcquire(t, e, name, files)
	intBRecord(t, e, name, files, func(entry *config.PluginEntry) {
		p := intBEnvPrefix(name)
		entry.Integration.EnvKeys = []string{p + "_A", p + "_B", p + "_TOKEN"}
	})
	e.fake.present[name+"-svc"] = true
}

const intBCheckOKBody = "#!/bin/sh\necho check-marker\necho \"token " + intBPlantedSK + " leaked\"\necho \"Authorization: Bearer " + intBPlantedBearer + "\"\ncat '@SECRET@'\nexit 0\n"

// intBCheckEnv is an agent-context factory: check is agent-callable, so every case runs AF_ROLE=x.
func intBCheckEnv(t *testing.T) *intBEnv {
	t.Helper()
	e := intBFactory(t)
	t.Setenv("AF_ROLE", "x")
	origVer := claudeCodeVersionFn
	t.Cleanup(func() { claudeCodeVersionFn = origVer })
	claudeCodeVersionFn = func() string { return "9.9.9 (Claude Code)" }
	origPID := servicePanePIDFn
	t.Cleanup(func() { servicePanePIDFn = origPID })
	servicePanePIDFn = func(string) (int, error) { return os.Getpid(), nil }
	return e
}

func intBReadCheckRecord(t *testing.T, root, name string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(intBCheckRecordPath(root, name))
	if err != nil {
		t.Fatalf("check record for %s: %v", name, err)
	}
	var rec map[string]json.RawMessage
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("check record is not a JSON object: %v\n%s", err, b)
	}
	return rec
}

func intBSortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func intBJSONString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Errorf("want a JSON string, got %s", raw)
	}
	return s
}

func intBJSONInt(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Errorf("want a JSON integer, got %s", raw)
	}
	return n
}

func intBAssertNoSecrets(t *testing.T, what, s string) {
	t.Helper()
	for _, secret := range []string{intBPlantedSK, intBPlantedBearer, intBPlantedFile} {
		if strings.Contains(s, secret) {
			t.Errorf("%s leaks the planted secret %q:\n%s", what, secret, s)
		}
	}
}

// intBLastJSONObject decodes the last line of out that is a JSON object.
func intBLastJSONObject(t *testing.T, out string) map[string]json.RawMessage {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(l, "{") {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l), &m); err == nil {
			return m
		}
	}
	t.Fatalf("no JSON object line in output:\n%s", out)
	return nil
}

func TestPluginCheck_RecordAndRedaction(t *testing.T) {
	t.Run("record_keys_and_redaction", func(t *testing.T) {
		e := intBCheckEnv(t)
		intBCheckFixture(t, e, "acme-ok", intBCheckOKBody, intBManifestOpts{})

		out, err := runPlugin(t, "check", nil, "acme-ok")
		if err != nil {
			t.Fatalf("a passing check run by an agent (AF_ROLE=x) must succeed: check is agent-callable; got: %v\noutput:\n%s", err, out)
		}
		intBAssertNoSecrets(t, "af plugin check output", out)
		rec := intBReadCheckRecord(t, e.root, "acme-ok")
		if got := intBSortedKeys(rec); !reflect.DeepEqual(got, intBCheckKeys) {
			t.Errorf("check record keys = %v, want exactly %v", got, intBCheckKeys)
		}
		if v := intBJSONInt(t, rec["v"]); v != 1 {
			t.Errorf("v = %d, want 1", v)
		}
		if p := intBJSONString(t, rec["plugin"]); p != "acme-ok" {
			t.Errorf("plugin = %q", p)
		}
		if s := intBJSONString(t, rec["state"]); s != "ok" {
			t.Errorf("state = %q, want ok", s)
		}
		if x := intBJSONInt(t, rec["exit"]); x != 0 {
			t.Errorf("exit = %d, want 0", x)
		}
		if at := intBJSONString(t, rec["at"]); at == "" {
			t.Error("at is empty")
		} else if _, perr := time.Parse(time.RFC3339, at); perr != nil {
			t.Errorf("at = %q is not RFC3339: %v", at, perr)
		}
		if hm := intBJSONString(t, rec["hook_fail_mode"]); hm != "open" {
			t.Errorf("hook_fail_mode = %q, want open", hm)
		}
		if cv := intBJSONString(t, rec["claude_code_version"]); cv != "9.9.9 (Claude Code)" {
			t.Errorf("claude_code_version = %q, want the bounded `claude --version` output (G15)", cv)
		}
		if rss := intBJSONInt(t, rec["service_rss_kb"]); rss <= 0 {
			t.Errorf("service_rss_kb = %d, want the VmRSS of the service pane pid (servicePanePIDFn, D23)", rss)
		}
		output := intBJSONString(t, rec["output"])
		intBAssertNoSecrets(t, "the check record output", output)
		if !strings.Contains(output, "check-marker") {
			t.Errorf("the record output must keep the non-secret check output; got %q", output)
		}
	})

	t.Run("failing_check_nonzero", func(t *testing.T) {
		e := intBCheckEnv(t)
		intBCheckFixture(t, e, "acme-bad", "#!/bin/sh\necho check-bad\nexit 3\n", intBManifestOpts{})

		out, err := runPlugin(t, "check", nil, "acme-bad")
		if err == nil {
			t.Errorf("a failing check must exit non-zero; output:\n%s", out)
		}
		rec := intBReadCheckRecord(t, e.root, "acme-bad")
		if s := intBJSONString(t, rec["state"]); s != "fail" {
			t.Errorf("state = %q, want fail", s)
		}
		if x := intBJSONInt(t, rec["exit"]); x != 3 {
			t.Errorf("exit = %d, want the script's 3", x)
		}
	})

	t.Run("timeout_is_fail", func(t *testing.T) {
		e := intBCheckEnv(t)
		intBCheckFixture(t, e, "acme-slow", "#!/bin/sh\nexec sleep 30\n", intBManifestOpts{checkTimeout: "1s"})

		start := time.Now()
		out, err := runPlugin(t, "check", nil, "acme-slow")
		if err == nil {
			t.Errorf("a timed-out check must exit non-zero (D24); output:\n%s", out)
		}
		if el := time.Since(start); el > 20*time.Second {
			t.Errorf("check ran %v; the [check] timeout (1s) plus WaitDelay must bound it", el)
		}
		rec := intBReadCheckRecord(t, e.root, "acme-slow")
		if s := intBJSONString(t, rec["state"]); s != "fail" {
			t.Errorf("state = %q, want fail (timeout => fail, D24)", s)
		}
	})

	t.Run("no_check_section_no_record", func(t *testing.T) {
		e := intBCheckEnv(t)
		intBCheckFixture(t, e, "acme-none", intBCheckOKBody, intBManifestOpts{noCheck: true})

		out, err := runPlugin(t, "check", nil, "acme-none")
		if err != nil {
			t.Errorf("an integration with no [check] exits 0 (D24); got: %v\noutput:\n%s", err, out)
		}
		if intBExists(intBCheckRecordPath(e.root, "acme-none")) {
			t.Error("no [check] => no check record (D24)")
		}
	})

	t.Run("json_envelope", func(t *testing.T) {
		e := intBCheckEnv(t)
		intBCheckFixture(t, e, "acme-ok", intBCheckOKBody, intBManifestOpts{})
		intBCheckFixture(t, e, "acme-bad", "#!/bin/sh\necho check-bad\nexit 3\n", intBManifestOpts{})

		out, err := runPlugin(t, "check", map[string]string{"json": "true"}, "acme-ok", "acme-bad")
		if err == nil {
			t.Error("--json with a failing check still exits non-zero")
		}
		intBAssertNoSecrets(t, "--json output", out)
		env := intBLastJSONObject(t, out)
		if s := intBJSONString(t, env["state"]); s != "fail" {
			t.Errorf(`envelope "state" = %q, want fail`, s)
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(env["results"], &rows); err != nil {
			t.Fatalf(`envelope "results" is not an array of objects: %v (%s)`, err, env["results"])
		}
		states := map[string]string{}
		for _, r := range rows {
			if got := intBSortedKeys(r); !reflect.DeepEqual(got, intBCheckRowKeys) {
				t.Errorf("result row keys = %v, want exactly %v", got, intBCheckRowKeys)
			}
			states[intBJSONString(t, r["plugin"])] = intBJSONString(t, r["state"])
		}
		if want := map[string]string{"acme-ok": "ok", "acme-bad": "fail"}; !reflect.DeepEqual(states, want) {
			t.Errorf("result states = %v, want %v", states, want)
		}
	})

	t.Run("detector_rows_reported", func(t *testing.T) {
		e := intBCheckEnv(t)
		intBCheckFixture(t, e, "acme-ok", intBCheckOKBody, intBManifestOpts{})
		settings := `{"enabledPlugins":{"frontend-design@x":true}}` + "\n"
		if err := os.WriteFile(filepath.Join(e.claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}

		out, err := runPlugin(t, "check", nil, "acme-ok")
		combined := out
		if err != nil {
			combined += "\n" + err.Error()
		}
		if !strings.Contains(combined, "frontend-design") {
			t.Errorf("check output must carry the user-scope detector row naming the unaccounted plugin frontend-design; got:\n%s", combined)
		}
	})
}

// TestPR724_T12_KeepPluginCheckListsQuickstartPlaywright: bare af up leaves out the Playwright quickstart
// installs at user scope, but af plugin check, the on-demand audit, still names that channel.
func TestPR724_T12_KeepPluginCheckListsQuickstartPlaywright(t *testing.T) {
	e := intBCheckEnv(t)
	intBCheckFixture(t, e, "acme-ok", intBCheckOKBody, intBManifestOpts{})
	settings := `{"enabledPlugins":{"playwright@claude-plugins-official":true}}` + "\n"
	if err := os.WriteFile(filepath.Join(e.claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runPlugin(t, "check", nil, "acme-ok")
	combined := out
	if err != nil {
		combined += "\n" + err.Error()
	}
	if want := "user scope: playwright@claude-plugins-official is not accounted for by any installed integration"; !strings.Contains(combined, want) {
		t.Errorf("af plugin check must still list quickstart's user-scope Playwright (%q); got:\n%s", want, combined)
	}
}

// ---- B11: the free-text redactor --------------------------------------------

func TestRedactIntegrationOutput(t *testing.T) {
	rows := []struct {
		name     string
		in       string
		secrets  []string
		mustNot  []string
		mustHave []string
		exact    string // "" = not pinned exactly
	}{
		{name: "plain_text_unchanged", in: "all good\nsecond line", exact: "all good\nsecond line"},
		{name: "sk_token", in: "key=sk-abcdefghijklmnop end", mustNot: []string{"sk-abcdefghijklmnop"}, mustHave: []string{"key=", "end"}},
		{name: "sk_token_with_dash_underscore", in: "id sk-ab_cd-efgh1234 ok", mustNot: []string{"ab_cd-efgh1234"}, mustHave: []string{"id ", " ok"}},
		{name: "short_sk_not_a_token", in: "task-sk-abc", exact: "task-sk-abc"},
		{name: "bearer_header", in: "Authorization: Bearer xyz123secret", mustNot: []string{"xyz123secret"}, mustHave: []string{"Authorization:"}},
		{name: "exact_secret_value", in: "value is hunter2-long-secret here", secrets: []string{"hunter2-long-secret"}, mustNot: []string{"hunter2-long-secret"}, mustHave: []string{"value is", "here"}},
		{name: "file_ref_untouched", in: "ANTHROPIC_API_KEY=file:.agentfactory/secrets/key", exact: "ANTHROPIC_API_KEY=file:.agentfactory/secrets/key"},
		{name: "control_chars_display_safe", in: "evil\x1b[2Jclear", mustNot: []string{"\x1b"}, mustHave: []string{"evil", "clear"}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := redactIntegrationOutput(r.in, r.secrets)
			if r.exact != "" && got != r.exact {
				t.Errorf("redactIntegrationOutput(%q) = %q, want %q", r.in, got, r.exact)
			}
			for _, s := range r.mustNot {
				if strings.Contains(got, s) {
					t.Errorf("redactIntegrationOutput(%q) = %q still contains %q", r.in, got, s)
				}
			}
			for _, s := range r.mustHave {
				if !strings.Contains(got, s) {
					t.Errorf("redactIntegrationOutput(%q) = %q lost the non-secret %q", r.in, got, s)
				}
			}
			if again := redactIntegrationOutput(got, r.secrets); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}
