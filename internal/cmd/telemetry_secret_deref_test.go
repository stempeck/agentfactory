package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// seedSecretBackedTelemetry writes the EXACT configuration shape quickstart.sh installs: a
// header whose value is a file: reference relative to the factory root, plus the secret file
// it points at. The suite's only existing Headers fixture is a literal "Basic ..." value,
// which is precisely why the defect below is invisible to CI — nothing anywhere exercises the
// shape the installer actually writes.
func seedSecretBackedTelemetry(t *testing.T, root string) {
	t.Helper()
	secretsDir := filepath.Join(root, ".agentfactory", "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	// No trailing newline, matching quickstart.sh's `printf 'Basic %s'`.
	if err := os.WriteFile(filepath.Join(secretsDir, "telemetry.auth"),
		[]byte("Basic cm9vdEBhZ2VudGZhY3RvcnkubG9jYWw6c2VjcmV0"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	writeTelemetryJSON(t, root, `{
  "endpoint": "http://127.0.0.1:5080/api/default",
  "otlp_http_path_traces": "/v1/traces",
  "headers": { "Authorization": "file:.agentfactory/secrets/telemetry.auth" },
  "protocol": "http/json",
  "export_timeout_ms": 500,
  "resource_attributes_extra": {}
}`)
}

// captureExportedHeaders swaps telemetry.Export for a recorder, so the assertion is on what
// the export path WOULD send rather than on whether a socket happened to be listening.
func captureExportedHeaders(t *testing.T) *config.TelemetryConfig {
	t.Helper()
	orig := telemetry.Export
	var seen config.TelemetryConfig
	telemetry.Export = func(cfg config.TelemetryConfig, payload []byte) error {
		seen = cfg
		return nil
	}
	t.Cleanup(func() { telemetry.Export = orig })
	return &seen
}

// TestBoundedDrainDereferencesSecretHeaders pins the af-plane blocker. internal/telemetry
// refuses, correctly, to dereference a path it cannot bound — it is handed no factory root,
// so it cannot check the path resolves inside one, and its own comment says dereferencing
// "belongs to the caller that knows the factory root".
//
// No caller does. Both export paths hand the loaded config straight through, so with the
// configuration quickstart.sh itself installs, every af done prints a warning and no af-plane
// span ever reaches the backend — which also means no step windows, which means the join has
// nothing to join against.
//
// The seam is telemetry.Export rather than a live socket, so this runs with no backend.
func TestBoundedDrainDereferencesSecretHeaders(t *testing.T) {
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("seed gate: %v", err)
	}
	seedSecretBackedTelemetry(t, root)

	// One closed step so the drain has something to carry.
	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
		TS: "2026-07-25T01:00:00.000Z", Agent: "manager", InstanceID: "i1",
		StepID: "s1", StepSeq: 1, Status: telemetry.StatusClosed, DurationMS: 10,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	seen := captureExportedHeaders(t)
	drainTelemetryBounded(root, "manager")

	got := seen.Headers["Authorization"]
	if strings.HasPrefix(got, "file:") {
		t.Fatalf("drainTelemetryBounded handed telemetry.Drain an unresolved secret reference "+
			"(%q). Export refuses this by design, so the record is never sent and af done warns "+
			"on every close: the whole af plane is dark with the config quickstart.sh installs", got)
	}
	if got != "Basic cm9vdEBhZ2VudGZhY3RvcnkubG9jYWw6c2VjcmV0" {
		t.Errorf("Authorization header = %q, want the dereferenced contents of "+
			".agentfactory/secrets/telemetry.auth", got)
	}
}

// TestBacklogExportDereferencesSecretHeaders covers the second call site. It is a separate
// test because it is a separate unresolved thread and a separate code path: the operator's
// explicit `af telemetry report --export` drain, which loads its own config and fans out over
// every agent. Fixing one call site and not the other leaves half the feature dark.
func TestBacklogExportDereferencesSecretHeaders(t *testing.T) {
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("seed gate: %v", err)
	}
	seedSecretBackedTelemetry(t, root)

	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
		TS: "2026-07-25T01:00:00.000Z", Agent: "manager", InstanceID: "i1",
		StepID: "s1", StepSeq: 1, Status: telemetry.StatusClosed, DurationMS: 10,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	seen := captureExportedHeaders(t)
	if err := exportTelemetryBacklog(root, "manager"); err != nil {
		t.Fatalf("exportTelemetryBacklog: %v", err)
	}

	if got := seen.Headers["Authorization"]; strings.HasPrefix(got, "file:") {
		t.Fatalf("exportTelemetryBacklog handed telemetry.Drain an unresolved secret reference "+
			"(%q); this is the second of the two places that must dereference it", got)
	}
}

// TestSecretDerefGuardsAndSurfaces holds the no-silent-fallback line. A missing secret file
// must produce a named, surfaced failure — never a fallback to sending the literal file:
// reference (which is the bug), and never a fallback to sending no credential at all (which
// produces a 401 that reads like a backend problem).
//
// The error must name the header key and must NOT contain the value or the path: the
// package's existing contract is that header values are never echoed, including into errors,
// and an operator-chosen secret path can itself be sensitive.
func TestSecretDerefGuardsAndSurfaces(t *testing.T) {
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("seed gate: %v", err)
	}
	// Config points at a secret that does not exist.
	writeTelemetryJSON(t, root, `{
  "endpoint": "http://127.0.0.1:5080/api/default",
  "headers": { "Authorization": "file:.agentfactory/secrets/absent.auth" },
  "protocol": "http/json",
  "export_timeout_ms": 500
}`)

	called := false
	orig := telemetry.Export
	telemetry.Export = func(cfg config.TelemetryConfig, payload []byte) error {
		called = true
		return nil
	}
	t.Cleanup(func() { telemetry.Export = orig })

	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
		TS: "2026-07-25T01:00:00.000Z", Agent: "manager", InstanceID: "i1",
		StepID: "s1", StepSeq: 1, Status: telemetry.StatusClosed, DurationMS: 10,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	err := exportTelemetryBacklog(root, "manager")
	if err == nil {
		t.Fatal("an unreadable secret file produced no error: a silent fallback here " +
			"reintroduces the blocker where it is least visible")
	}
	if !strings.Contains(err.Error(), "Authorization") {
		t.Errorf("error does not name the offending header key: %v", err)
	}
	if strings.Contains(err.Error(), "absent.auth") {
		t.Errorf("error echoed the secret path, which the package contract forbids: %v", err)
	}
	if called {
		t.Error("an export was attempted with an unresolved credential")
	}
}
