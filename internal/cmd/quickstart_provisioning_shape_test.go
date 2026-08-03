package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shellFnsFrom extracts the named functions from quickstart.sh and prefixes the script's own shell
// options, so a fragment is exercised the way production exercises it. Testing shell under
// different options than the product sets is how a construct can be green in CI and fatal on a
// real install.
func shellFnsFrom(t *testing.T, names ...string) string {
	t.Helper()
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "set -euo pipefail") {
		t.Fatal("quickstart.sh no longer sets `set -euo pipefail`; these harnesses assume it")
	}
	out := "set -euo pipefail\n"
	for _, n := range names {
		fn := extractShellFunction(content, n)
		if fn == "" {
			t.Fatalf("could not extract %s() from quickstart.sh", n)
		}
		out += fn + "\n"
	}
	return out
}

// TestCredentialWriteSurvivesTheRealCallShape reproduces the production call shape exactly:
// `_generate_telemetry_password > "$root_pass_file"`, under the script's own options, writing to a
// real file. Asserting on captured stdout is not enough — the failure this guards against destroys
// the FILE while producing no output and no message at all.
func TestCredentialWriteSurvivesTheRealCallShape(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "telemetry.root")

	script := shellFnsFrom(t, "_telemetry_password_is_compliant", "_generate_telemetry_password") + `
_generate_telemetry_password > "` + target + `"
echo "WROTE"`

	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("the real call shape aborted (%v). Under pipefail+errexit a SIGPIPE inside the "+
			"generator kills the script mid-redirect, leaving the credential file truncated and "+
			"printing nothing:\n%s", err, out)
	}
	if !strings.Contains(string(out), "WROTE") {
		t.Errorf("execution never reached the line after the write; output: %q", out)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the credential file: %v", err)
	}
	pw := strings.TrimSpace(string(got))
	if pw == "" {
		t.Fatal("the credential file is EMPTY after generation. `[ ! -s file ]` treats 0 bytes as " +
			"absent, so every re-run repeats the failure, and the backend can never start")
	}
	if missing := openObservePasswordPolicy(pw); len(missing) > 0 {
		t.Errorf("the written credential (%d chars) fails the backend policy, missing: %s",
			len(pw), strings.Join(missing, ", "))
	}
}

// TestSeededTelemetryEndpointCarriesTheOrgSegment gives B2 the enforcement it lacked. The seeded
// values are the entire fix — no Go code changed — so with nothing asserting them, the fix could be
// reverted with both suites staying green.
//
// The two values must move together: the exporter joins endpoint + traces path, so splitting the
// organisation segment across them keeps the af plane working while the agent sessions' own usage
// events, which derive their address from the base alone, go to an unserved path.
func TestSeededTelemetryEndpointCarriesTheOrgSegment(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	setup := extractShellFunction(string(data), "setup_telemetry")
	if setup == "" {
		t.Fatal("could not extract setup_telemetry()")
	}

	// Scope to the SEED heredoc. The migration branch legitimately mentions the old values in order
	// to recognise a pre-fix config, so asserting over the whole function would fail on a correct
	// implementation.
	const seedStart = `cat > ".agentfactory/telemetry.json" << EOF`
	i := strings.Index(setup, seedStart)
	if i < 0 {
		t.Fatal("could not locate the telemetry.json seed heredoc in setup_telemetry()")
	}
	rest := setup[i+len(seedStart):]
	j := strings.Index(rest, "\nEOF")
	if j < 0 {
		t.Fatal("unterminated seed heredoc")
	}
	seed := rest[:j]

	if !strings.Contains(seed, `"endpoint": "http://127.0.0.1:$TELEMETRY_PORT/api/default"`) {
		t.Errorf(`the seeded endpoint does not end in /api/default. The agent sessions derive their `+
			`own addresses by appending /v1/{signal} to this base, so without the organisation `+
			`segment the per-request token counts — the whole point of the feature — post to a path `+
			`the backend answers 404 to. Seed block was:%s`, seed)
	}
	if !strings.Contains(seed, `"otlp_http_path_traces": "/v1/traces"`) {
		t.Error(`the seeded traces path is not "/v1/traces". It must move in step with the endpoint: ` +
			`the exporter concatenates the two, so carrying the organisation segment in both yields ` +
			`/api/default/api/default/v1/traces`)
	}
	// The old broken pair must not reappear IN THE SEED.
	if strings.Contains(seed, `"otlp_http_path_traces": "/api/default/v1/traces"`) {
		t.Error("the seeded traces path still carries the organisation segment; combined with an " +
			"endpoint that also carries it, the af-plane URL doubles the segment")
	}
}

// TestExistingFactoryConfigIsMigrated pins the migration branch, which had no test at all: the seed
// is write-if-absent, so without it the fix reaches no factory that was ever provisioned — and per
// the defect's own nature, that is every factory installed before it.
func TestExistingFactoryConfigIsMigrated(t *testing.T) {
	// The REAL function is extracted and executed. An earlier version of this test ran its own
	// pasted copy of the sed, which meant the shipped rewrite could be changed to write any address
	// at all with the whole suite still green — the same asymmetry the review objected to.
	fns := shellFnsFrom(t, "_migrate_telemetry_endpoint")

	writePre := func(t *testing.T, dir, endpoint, tracesPath string) string {
		t.Helper()
		cfg := filepath.Join(dir, "telemetry.json")
		pre := `{
  "endpoint": "` + endpoint + `",
  "otlp_http_path_traces": "` + tracesPath + `",
  "headers": { "Authorization": "file:.agentfactory/secrets/telemetry.auth" },
  "protocol": "http/json",
  "export_timeout_ms": 500,
  "resource_attributes_extra": {}
}
`
		if err := os.WriteFile(cfg, []byte(pre), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return cfg
	}

	run := func(t *testing.T, cfg string) (int, string) {
		t.Helper()
		script := fns + "\n_migrate_telemetry_endpoint " + shellQuote(cfg) + " 5080\n"
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		code := 0
		if err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("running the migration: %v\n%s", err, out)
			}
			code = ee.ExitCode()
		}
		return code, string(out)
	}

	t.Run("repairs the shipped pre-fix pair", func(t *testing.T) {
		cfg := writePre(t, t.TempDir(), "http://127.0.0.1:5080", "/api/default/v1/traces")
		if code, out := run(t, cfg); code != 0 {
			t.Fatalf("migration returned %d, want 0 (repaired)\n%s", code, out)
		}
		got, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		s := string(got)
		// Asserting the exact destination values is what makes this an interlock: a rewrite that
		// runs cleanly but writes the wrong address fails here.
		if !strings.Contains(s, `"endpoint": "http://127.0.0.1:5080/api/default"`) {
			t.Errorf("migration did not move the organisation segment into the endpoint:\n%s", s)
		}
		if !strings.Contains(s, `"otlp_http_path_traces": "/v1/traces"`) {
			t.Errorf("migration did not standardise the traces path:\n%s", s)
		}
		if _, err := os.Stat(cfg + ".bak"); err == nil {
			t.Error("the migration left a .bak file in the operator's config directory")
		}
		// The af-plane URL must be unchanged by the move — that property is what makes it safe.
		if strings.Contains(s, "/api/default/api/default") {
			t.Error("the organisation segment is now doubled")
		}
	})

	// An operator who edited either value must be left alone, which is the whole reason the guard
	// matches the shipped pair exactly rather than pattern-matching the shape.
	for _, tc := range []struct {
		name, endpoint, tracesPath string
	}{
		{"operator changed the endpoint", "http://otel.internal:4318", "/api/default/v1/traces"},
		{"operator changed the traces path", "http://127.0.0.1:5080", "/v1/traces"},
		{"already migrated", "http://127.0.0.1:5080/api/default", "/v1/traces"},
	} {
		t.Run(tc.name+" is left untouched", func(t *testing.T) {
			dir := t.TempDir()
			cfg := writePre(t, dir, tc.endpoint, tc.tracesPath)
			before, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			code, out := run(t, cfg)
			if code != 1 {
				t.Fatalf("migration returned %d, want 1 (nothing to repair)\n%s", code, out)
			}
			after, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("an operator-edited config was rewritten:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}

	t.Run("an absent config is not an error", func(t *testing.T) {
		if code, out := run(t, filepath.Join(t.TempDir(), "telemetry.json")); code != 1 {
			t.Fatalf("migration returned %d for an absent config, want 1\n%s", code, out)
		}
	})
}
