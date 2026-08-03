package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTelemetryGateToggle pins the byte-level toggle contract the telemetry gate
// shares with quality/fidelity/improvement: absent ⇒ off, "on\n"/"off\n" written at
// 0644, and ONLY the exact trimmed string "on" enabling it. The exact-match cases are
// net-new coverage — no existing gate suite pins them, yet all four gates depend on
// the same strings.TrimSpace(...) != "on" read.
func TestTelemetryGateToggle(t *testing.T) {
	t.Run("absent gate file reads off and is not created by a status read", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, nil); err != nil {
				t.Fatalf("runTelemetry status: %v", err)
			}
		})

		if !strings.Contains(out, "telemetry: off") {
			t.Errorf("status with no gate file = %q, want it to contain %q", out, "telemetry: off")
		}
		if _, err := os.Stat(telemetryGateFile(dir)); !os.IsNotExist(err) {
			t.Errorf("a status read must not create the gate file; stat err = %v", err)
		}
	})

	t.Run("on writes on", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"on"}); err != nil {
				t.Fatalf("runTelemetry on: %v", err)
			}
		})

		data, err := os.ReadFile(telemetryGateFile(dir))
		if err != nil {
			t.Fatalf("read .telemetry-gate: %v", err)
		}
		if string(data) != "on\n" {
			t.Errorf("file contents = %q, want %q", string(data), "on\n")
		}
		info, err := os.Stat(telemetryGateFile(dir))
		if err != nil {
			t.Fatalf("stat .telemetry-gate: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o644 {
			t.Errorf("gate file mode = %o, want %o", perm, 0o644)
		}
		if !strings.Contains(out, "telemetry: on") {
			t.Errorf("stdout = %q, want it to contain %q", out, "telemetry: on")
		}
	})

	t.Run("off writes off", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"off"}); err != nil {
				t.Fatalf("runTelemetry off: %v", err)
			}
		})

		data, err := os.ReadFile(telemetryGateFile(dir))
		if err != nil {
			t.Fatalf("read .telemetry-gate: %v", err)
		}
		if string(data) != "off\n" {
			t.Errorf("file contents = %q, want %q", string(data), "off\n")
		}
		if !strings.Contains(out, "telemetry: off") {
			t.Errorf("stdout = %q, want it to contain %q", out, "telemetry: off")
		}
	})

	t.Run("only the exact string on enables the gate", func(t *testing.T) {
		for _, contents := range []string{"off\n", "", "onn\n", "on x\n", "ON\n", "1\n", "no\n"} {
			dir := setupTestFactoryForFidelity(t)
			t.Chdir(dir)
			if err := os.WriteFile(telemetryGateFile(dir), []byte(contents), 0o644); err != nil {
				t.Fatalf("seed gate %q: %v", contents, err)
			}

			out := captureStdout(t, func() {
				if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
					t.Fatalf("runTelemetry status with gate %q: %v", contents, err)
				}
			})

			if !strings.Contains(out, "telemetry: off") {
				t.Errorf("gate contents %q must read as off, got stdout %q", contents, out)
			}
		}
	})

	t.Run("surrounding whitespace around on still reads on", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		if err := os.WriteFile(telemetryGateFile(dir), []byte("  on  \n"), 0o644); err != nil {
			t.Fatalf("seed gate: %v", err)
		}

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
				t.Fatalf("runTelemetry status: %v", err)
			}
		})

		if !strings.Contains(out, "telemetry: on") {
			t.Errorf("stdout = %q, want it to contain %q", out, "telemetry: on")
		}
	})

	t.Run("an unrecognised argument is a usage error and writes nothing", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)

		var err error
		_ = captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"weird"}) })

		if err == nil {
			t.Fatal("expected an error for an unrecognised argument, got nil")
		}
		if !strings.Contains(err.Error(), "usage") {
			t.Errorf("error = %q, want it to mention usage", err.Error())
		}
		if _, statErr := os.Stat(telemetryGateFile(dir)); !os.IsNotExist(statErr) {
			t.Errorf("a usage error must not write the gate file; stat err = %v", statErr)
		}
	})
}

// TestTelemetryGateWorktreeContext is the AC-8 evidence: telemetry state resolved from
// inside a worktree agent directory must land at the OUTER factory root, because
// config.FindFactoryRoot (follows the .factory-root redirect) and config.FindLocalRoot
// (stops at the nearest marker) disagree there by design. Reading telemetry state
// through the wrong one silently yields no data for exactly the dispatched agents this
// feature exists to observe — the defect that superseded the earlier attempt.
func TestTelemetryGateWorktreeContext(t *testing.T) {
	t.Run("worktree agent dir writes the OUTER factory root's gate file", func(t *testing.T) {
		fx := buildNestedFactoryFixture(t)
		t.Setenv("AF_ROOT", fx.outer)
		t.Chdir(fx.worktree)

		_ = captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"on"}); err != nil {
				t.Fatalf("runTelemetry on from the worktree: %v", err)
			}
		})

		data, err := os.ReadFile(filepath.Join(fx.outer, ".agentfactory", ".telemetry-gate"))
		if err != nil {
			t.Fatalf("the outer factory root's gate file was not written: %v", err)
		}
		if string(data) != "on\n" {
			t.Errorf("outer gate = %q, want %q", string(data), "on\n")
		}
		// The non-vacuity guard: a FindLocalRoot implementation resolves the WORKTREE
		// root, so without this assertion an implementation that wrote both would pass.
		if _, err := os.Stat(filepath.Join(fx.worktree, ".agentfactory", ".telemetry-gate")); err == nil {
			t.Error("the gate leaked into the worktree root — telemetry state was resolved through the nearest marker instead of the redirect")
		}
	})

	t.Run("a nested clone refusal propagates instead of capturing the clone", func(t *testing.T) {
		fx := buildNestedFactoryFixture(t)
		t.Chdir(fx.clone)

		var err error
		_ = captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"on"}) })

		if err == nil {
			t.Fatal("expected an enclosing refusal from inside the nested clone, got success")
		}
		var enc *enclosingRootError
		if !errors.As(err, &enc) {
			t.Fatalf("expected *enclosingRootError, got %T: %v", err, err)
		}
		if _, statErr := os.Stat(filepath.Join(fx.clone, ".agentfactory", ".telemetry-gate")); statErr == nil {
			t.Error("a refused resolution must not write a gate file into the nested clone")
		}
	})
}

// TestTelemetryStatusLayering pins the two things af telemetry status does that the
// other gates' status bodies do not: it reports the config layer beneath the gate, and
// it never prints a header value. Telemetry has no lock file, so it has no stale-lock
// branch to mirror.
func TestTelemetryStatusLayering(t *testing.T) {
	const secret = "file:.agentfactory/secrets/telemetry.auth"

	t.Run("a valid config is summarised without ever printing a header value", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		if err := os.WriteFile(telemetryGateFile(dir), []byte("on\n"), 0o644); err != nil {
			t.Fatalf("seed gate: %v", err)
		}
		writeTelemetryJSON(t, dir, `{
  "endpoint": "http://127.0.0.1:5080",
  "otlp_http_path_traces": "/api/default/v1/traces",
  "headers": {"Authorization": "`+secret+`"},
  "protocol": "http/json",
  "export_timeout_ms": 500,
  "resource_attributes_extra": {}
}`)

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
				t.Fatalf("runTelemetry status: %v", err)
			}
		})

		if !strings.Contains(out, "telemetry: on") {
			t.Errorf("stdout = %q, want the gate layer %q", out, "telemetry: on")
		}
		if !strings.Contains(out, "http://127.0.0.1:5080") {
			t.Errorf("stdout = %q, want it to report the configured endpoint", out)
		}
		// The third layer. Asserted on its own line rather than folded into the endpoint
		// check above, which the config line already satisfies — without this the
		// reachability layer could be deleted with the whole suite staying green.
		//
		// It used to assert the literal "not probed", which was the layer declining to answer.
		// The layer now answers, so the assertion is on the label rather than the verdict: this
		// fixture's secret file does not exist, so the honest report is that the credential could
		// not be read — and an operator needs that distinguished from a reachable backend. The
		// assertion is kept stronger than before by ALSO requiring that the old
		// permanently-silent wording is gone, so the layer cannot regress to declining.
		if !strings.Contains(out, "endpoint:") {
			t.Errorf("stdout = %q, want the endpoint reachability layer", out)
		}
		if !strings.Contains(out, "could not be read from its secret file") {
			t.Errorf("stdout = %q, want the reachability layer to report why it could not probe "+
				"(this fixture's secret file is absent)", out)
		}
		if strings.Contains(out, "not probed (reachability checks land with the exporter)") {
			t.Error("status still declines to check reachability; the layer must answer, because " +
				"an operator cannot otherwise tell 'no data yet' from 'no data can ever arrive'")
		}
		if strings.Contains(out, secret) {
			t.Error("status printed a header VALUE; header values must never be echoed")
		}
		if strings.Contains(out, "Authorization") {
			t.Error("status printed a header NAME; the layered status reports counts, not header identity")
		}
	})

	t.Run("gate off reports off without reading the config", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		writeTelemetryJSON(t, dir, `{"endpoint":"ftp://nope"}`)

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
				t.Fatalf("a gate-off status must succeed even with an invalid config: %v", err)
			}
		})

		if !strings.Contains(out, "telemetry: off") {
			t.Errorf("stdout = %q, want %q", out, "telemetry: off")
		}
	})

	t.Run("an invalid config fails status and never echoes a header value", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)
		if err := os.WriteFile(telemetryGateFile(dir), []byte("on\n"), 0o644); err != nil {
			t.Fatalf("seed gate: %v", err)
		}
		writeTelemetryJSON(t, dir, `{"endpoint":"ftp://elsewhere/x","headers":{"Authorization":"file:a b;c"}}`)

		var err error
		_ = captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"status"}) })

		if err == nil {
			t.Fatal("expected an error for an invalid telemetry.json under status")
		}
		if !strings.Contains(err.Error(), "config invalid") {
			t.Errorf("error = %q, want it to report an invalid config", err.Error())
		}
		if strings.Contains(err.Error(), "file:a b;c") {
			t.Error("the error echoed a header value; header values must never be echoed, on the error paths too")
		}
	})

	t.Run("turning on with no telemetry.json says records stay local", func(t *testing.T) {
		dir := setupTestFactoryForFidelity(t)
		t.Chdir(dir)

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"on"}); err != nil {
				t.Fatalf("runTelemetry on: %v", err)
			}
		})

		if !strings.Contains(out, "telemetry: on") {
			t.Errorf("stdout = %q, want %q", out, "telemetry: on")
		}
		if !strings.Contains(out, "no telemetry.json") {
			t.Errorf("stdout = %q, want it to explain that records stay local", out)
		}
	})
}

// writeTelemetryJSON seeds root/.agentfactory/telemetry.json.
func writeTelemetryJSON(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".agentfactory", "telemetry.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write telemetry.json: %v", err)
	}
}

func TestTelemetry_CommandWiring(t *testing.T) {
	sub, _, err := rootCmd.Find([]string{"telemetry"})
	if err != nil {
		t.Fatalf("rootCmd.Find telemetry: %v", err)
	}
	if sub == nil || sub.Name() != "telemetry" {
		t.Fatalf("af telemetry is not registered on rootCmd; found %v", sub)
	}
}

// The applyGate pair below is what makes the startup.json wiring real rather than a
// branch reachable only from a textual grep: it proves the case writes under the
// af-up-resolved root and no-ops on both sentinel states.

func TestApplyGate_TelemetryDirectWriteUsesRoot(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	if err := applyGate(root, formulaDir, "telemetry", "on"); err != nil {
		t.Fatalf("applyGate telemetry on: %v", err)
	}
	data, err := os.ReadFile(telemetryGateFile(root))
	if err != nil {
		t.Fatalf("read telemetry gate under root: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("telemetry gate = %q, want %q", string(data), "on\n")
	}
	if _, err := os.Stat(telemetryGateFile(formulaDir)); err == nil {
		t.Error("applyGate wrote under formulaDir; the gate belongs to the resolved root")
	}
}

func TestApplyGate_TelemetryNoOpOnSentinels(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	for _, state := range []string{"", "default"} {
		if err := applyGate(root, formulaDir, "telemetry", state); err != nil {
			t.Fatalf("applyGate telemetry %q: %v", state, err)
		}
	}
	if _, err := os.Stat(telemetryGateFile(root)); err == nil {
		t.Error("telemetry gate file written for a sentinel state")
	}
}

// Unlike fidelity, disabling telemetry loses visibility rather than correctness, so it
// carries no active-formula guard: an active formula must not block the write.
func TestApplyGate_TelemetryHasNoActiveFormulaGuard(t *testing.T) {
	root, _ := newGateRoot(t)
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("some-formula\n"), 0o644); err != nil {
		t.Fatalf("write hooked_formula: %v", err)
	}

	if err := applyGate(root, root, "telemetry", "off"); err != nil {
		t.Fatalf("an active formula must not block disabling telemetry: %v", err)
	}
	data, err := os.ReadFile(telemetryGateFile(root))
	if err != nil {
		t.Fatalf("read telemetry gate: %v", err)
	}
	if string(data) != "off\n" {
		t.Errorf("telemetry gate = %q, want %q", string(data), "off\n")
	}
}
