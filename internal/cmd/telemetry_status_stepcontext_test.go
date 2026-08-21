package cmd

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// These tests close the same class of enforcement hole TestStatusReportsProbeVerdicts closes
// (telemetry_status_probe_test.go:13): a wiring line in printTelemetryStatus that could be deleted
// with the whole suite staying green.
//
// #622 C7's only pinned surface was the JSON twin and the golden DTOs. Both are fed by
// telemetryStatusJSON, which does not go through printStepContextKnobs at all — so deleting the
// printStepContextKnobs call left every existing test green while the operator-facing half of C7,
// the half the acceptance criterion actually names, silently vanished. AC-5 is a CLI-level grep
// against a built binary, which only CI or a human runs.

// writeStartupForStatus writes a startup.json and returns nothing: a malformed one is a different
// test's subject, so this fails loudly rather than letting an unreadable file read as "no knobs".
func writeStartupForStatus(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(config.StartupConfigPath(root), []byte(body), 0o644); err != nil {
		t.Fatalf("write startup.json: %v", err)
	}
	if _, err := config.LoadStartupConfig(root); err != nil {
		t.Fatalf("fixture startup.json does not load: %v", err)
	}
}

// gateOnFactoryForStatus is the gate-ON factory AC-5 specifies. The knobs print on the gate-ON
// paths only, so a gate-off fixture would assert nothing.
func gateOnFactoryForStatus(t *testing.T) string {
	t.Helper()
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("seed gate: %v", err)
	}
	return root
}

// TestStatusPrintsStepContextKnobs is AC-5 at the unit tier. The values are deliberately NOT the
// shipped defaults: a fixture running 200000/75 would still pass against a hardcoded line, or
// against a printer reading some other factory's config.
func TestStatusPrintsStepContextKnobs(t *testing.T) {
	root := gateOnFactoryForStatus(t)
	writeStartupForStatus(t, root, `{
  "agents": [],
  "recovery": {"context_threshold_pct": 91, "context_advisory_pct": 70},
  "step_context": {"bound_tokens": 123456, "handoff_pct": 81}
}`)

	out := captureStdout(t, func() {
		if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
			t.Fatalf("runTelemetry status: %v", err)
		}
	})

	for _, want := range []string{"step context:", "bound_tokens 123456", "handoff_pct 81"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q — C7 asks for the operator-facing numbers, "+
				"and the JSON twin does not go through this printer", out, want)
		}
	}
	// The three-number ladder is the point of the line: an operator reading handoff_pct without
	// context_threshold_pct cannot tell the cooperative boundary from the forceful one.
	if !strings.Contains(out, "context_threshold_pct 91") {
		t.Errorf("stdout = %q, want the recovery threshold printed beside the step-context knobs", out)
	}
	for _, role := range []string{"per-step budget", "cooperative boundary", "forceful recovery"} {
		if !strings.Contains(out, role) {
			t.Errorf("stdout = %q, missing the role label %q — three bare integers do not distinguish "+
				"a measurement knob from an intervention knob", out, role)
		}
	}
}

// TestStatusWarnsWhenHandoffPctWasClamped pins the HIGH-1 lint half. A handoff_pct that was derived
// rather than chosen is a number the operator would otherwise read as the one they set.
func TestStatusWarnsWhenHandoffPctWasClamped(t *testing.T) {
	root := gateOnFactoryForStatus(t)
	// A ladder that cannot seat the shipped 75: threshold 60 forces min(75, 59) = 59, above the
	// advisory floor. step_context is omitted so the value is DERIVED, which is the lint's subject.
	writeStartupForStatus(t, root, `{
  "agents": [],
  "recovery": {"context_threshold_pct": 60, "context_advisory_pct": 50}
}`)

	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
				t.Fatalf("runTelemetry status: %v", err)
			}
		})
	})

	if !strings.Contains(out, "handoff_pct 59") {
		t.Errorf("stdout = %q, want the EFFECTIVE handoff_pct 59, not the shipped default", out)
	}
	if !strings.Contains(stderr, "cannot seat") {
		t.Errorf("stderr = %q, want the clamp warning: a silently derived handoff_pct reads as a chosen one", stderr)
	}
	if !strings.Contains(stderr, strconv.Itoa(59)) {
		t.Errorf("stderr = %q, want the warning to name the effective value", stderr)
	}
}

// TestStatusSurvivesUnreadableStartupConfig: the knobs are a courtesy, never a new failure path.
// LoadStartupConfig returns (nil, err) for a malformed file, and a status command that panicked or
// exited non-zero over two informational lines would be a worse regression than the missing lines.
func TestStatusSurvivesUnreadableStartupConfig(t *testing.T) {
	root := gateOnFactoryForStatus(t)
	if err := os.WriteFile(config.StartupConfigPath(root), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out string
	stderr := captureStderr(t, func() {
		out = captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
				t.Fatalf("runTelemetry status returned %v; the step-context knobs must not be able to "+
					"fail the status command", err)
			}
		})
	})

	if !strings.Contains(out, "telemetry: on") {
		t.Errorf("stdout = %q, want the rest of status still reported", out)
	}
	if strings.Contains(out, "step context:") {
		t.Errorf("stdout = %q, printed step-context knobs it could not read", out)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("stderr = %q, want a warning naming why the knobs are absent", stderr)
	}
}
