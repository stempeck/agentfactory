//go:build !integration

package cmd

import (
	"os"
	"strings"
	"testing"
)

// otelFamilyVars is the seven-variable OTel family Phase 4a stamps onto every
// launched session at the session.Manager chokepoint (internal/session
// telemetryFamilyVars / internal/telemetry LaunchEnv). Phase 4b forwards
// exactly this family out of each grader's `env -i` allowlist so per-turn
// grader Haiku spend is attributed instead of scrubbed. Order mirrors the
// source enumeration; OTEL_RESOURCE_ATTRIBUTES is the member that also carries
// the appended overhead marker.
var otelFamilyVars = []string{
	"CLAUDE_CODE_ENABLE_TELEMETRY",
	"OTEL_METRICS_EXPORTER",
	"OTEL_LOGS_EXPORTER",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_HEADERS",
	"OTEL_RESOURCE_ATTRIBUTES",
}

// TestHookGrader_ForwardsTelemetryFamily pins Phase 4b (issue #329): each hook's
// haiku grader forwards the seven-var OTel family through its `env -i` allowlist
// using ${VAR:+...} conditional expansion — a no-op when the var is unset so the
// line is byte-equivalent to the pre-change line when telemetry is off — and
// appends af.overhead=grader to the forwarded OTEL_RESOURCE_ATTRIBUTES so grader
// spend is bucketed as factory overhead (design-doc K10; binding join rule 1).
//
// Asserted across all four copies (both runtime hooks and both embedded twins)
// so the source and embedded mirrors stay locked together at the grader line;
// TestInstallHooks_NoDrift enforces byte-identity, this test enforces intent.
func TestHookGrader_ForwardsTelemetryFamily(t *testing.T) {
	repoRoot := findRepoRoot(t)

	// The exact forwarding token for OTEL_RESOURCE_ATTRIBUTES: forwarded via the
	// no-op-when-unset ${VAR:+...} form, with af.overhead=grader appended behind a
	// leading comma (the comma is load-bearing — it keeps the marker a distinct
	// resource attribute so join rule 1 can bucket the event as overhead).
	const overheadForward = `${OTEL_RESOURCE_ATTRIBUTES:+OTEL_RESOURCE_ATTRIBUTES="$OTEL_RESOURCE_ATTRIBUTES,af.overhead=grader"}`

	for _, f := range gateHookFiles(repoRoot) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		content := string(data)

		// Every family var is forwarded via the no-op-when-unset ${VAR:+...} form.
		for _, v := range otelFamilyVars {
			token := "${" + v + ":+"
			if !strings.Contains(content, token) {
				t.Errorf("%s: OTel family var %q is not forwarded via %s...} conditional expansion", f, v, token)
			}
		}

		// The overhead marker is appended to the forwarded OTEL_RESOURCE_ATTRIBUTES.
		if !strings.Contains(content, overheadForward) {
			t.Errorf("%s: OTEL_RESOURCE_ATTRIBUTES forwarding does not append the overhead marker; expected to contain:\n  %s", f, overheadForward)
		}

		// Exactly once — the marker belongs only on the forwarding expansion (AC #1).
		if n := strings.Count(content, "af.overhead=grader"); n != 1 {
			t.Errorf("%s: af.overhead=grader must appear exactly once, found %d", f, n)
		}
	}
}
