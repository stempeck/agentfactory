package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// telemetryFamilyClears are the empty structural clears the inline startup command must emit
// for the seven-var OTel family when telemetry is off — the only hygiene a respawn (which never
// calls Start) ever emits.
var telemetryFamilyClears = []string{
	"CLAUDE_CODE_ENABLE_TELEMETRY=''",
	"OTEL_METRICS_EXPORTER=''",
	"OTEL_LOGS_EXPORTER=''",
	"OTEL_EXPORTER_OTLP_PROTOCOL=''",
	"OTEL_EXPORTER_OTLP_ENDPOINT=''",
	"OTEL_EXPORTER_OTLP_HEADERS=''",
	"OTEL_RESOURCE_ATTRIBUTES=''",
}

// TestRespawnTelemetryOffClearsFamily proves the telemetry-off relaunch clears the whole OTel
// family on the RESPAWN path — the path that never calls Start(), so the inline KEY='' loop is
// its only hygiene (AC #4). It drives the real respawn path (respawnSession -> NewManager ->
// BuildStartupCommand -> RespawnPane) with the telemetry gate off (absent gate file) and asserts
// the respawned command clears every family var a prior telemetry-on run could have left behind.
func TestRespawnTelemetryOffClearsFamily(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")

	mock := &mockTmux{}
	err := respawnSession(RespawnOptions{
		FactoryRoot:  dir,
		AgentName:    "manager",
		AgentEntry:   config.AgentEntry{Type: "interactive"},
		AgentWorkDir: config.AgentDir(dir, "manager"),
		PaneID:       "%0",
		Tx:           mock,
	})
	if err != nil {
		t.Fatalf("respawnSession: %v", err)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Fatalf("RespawnPane should be called once, got %d", len(mock.respawnPaneCalls))
	}
	cmd := mock.respawnPaneCalls[0].cmd
	for _, want := range telemetryFamilyClears {
		if !strings.Contains(cmd, want) {
			t.Errorf("respawn with telemetry off must clear stale telemetry var (%q) so a prior telemetry-on run cannot survive the reused session; got: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "CLAUDE_CODE_ENABLE_TELEMETRY='1'") {
		t.Errorf("respawn with telemetry off must not enable telemetry; got: %s", cmd)
	}
}

// TestRespawnTelemetryOnStampsFamily is the positive companion: with the gate on and a valid
// telemetry.json, a respawn carries the OTel family — proving watchdog/handoff/compact-handoff
// respawns (all routed through respawnSession) gain telemetry, not just fresh af sling / af up
// launches.
func TestRespawnTelemetryOnStampsFamily(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	if err := os.WriteFile(telemetryGateFile(dir), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write gate: %v", err)
	}
	if err := os.WriteFile(config.TelemetryConfigPath(dir),
		[]byte(`{"endpoint":"https://otel.example.com","protocol":"http/json"}`), 0o644); err != nil {
		t.Fatalf("write telemetry.json: %v", err)
	}

	mock := &mockTmux{}
	err := respawnSession(RespawnOptions{
		FactoryRoot:  dir,
		AgentName:    "manager",
		AgentEntry:   config.AgentEntry{Type: "interactive"},
		AgentWorkDir: config.AgentDir(dir, "manager"),
		PaneID:       "%0",
		Tx:           mock,
	})
	if err != nil {
		t.Fatalf("respawnSession: %v", err)
	}
	cmd := mock.respawnPaneCalls[0].cmd
	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY='1'",
		"OTEL_EXPORTER_OTLP_ENDPOINT='https://otel.example.com'",
		"af.agent=manager",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("respawn with telemetry on must stamp the OTel family; missing %q in: %s", want, cmd)
		}
	}
}
