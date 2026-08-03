package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// startupKey re-encodes the mirror struct and reports one key's value as the JSON layer sees
// it. Asserting through JSON rather than through a Go field is deliberate on two counts: the
// defect IS a serialization behaviour (encoding/json discarding a key the struct does not
// declare), and a field access would not compile until the field exists, which would stop the
// package's other tests from running at all while this one is red.
func startupKey(t *testing.T, s Startup, key string) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal Startup: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal Startup: %v", err)
	}
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s2, _ := v.(string)
	return s2, true
}

// TestSettings_Read_PreservesTelemetry is the exact twin of
// TestSettings_Read_PreservesImprovement, which exists because this same mirror dropped the
// Improvement field once before. internal/config/startup.go gained Telemetry; the mirror here
// did not, so encoding/json discards the key on Read. The console then round-trips what it
// read back through `af config startup set`, and an operator's "telemetry": "on" is silently
// reset to default the next time anyone saves settings from the web console.
//
// Neither CI lane catches this class on its own: the root `make test` does not descend into
// web/, and the web-unit lane does not know the field exists until this test names it.
func TestSettings_Read_PreservesTelemetry(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	onDisk := `{"agents":["manager"],"quality":"on","fidelity":"on","improvement":"on","telemetry":"on","start_dispatch":true}`
	if err := os.WriteFile(startupPath(root), []byte(onDisk), 0o644); err != nil {
		t.Fatalf("write startup.json: %v", err)
	}

	svc := New(root, nil) // read path needs no Setter
	got, err := svc.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// The sibling gates prove the fixture and the read path are sound, so a failure below is
	// about the telemetry key alone and not about the harness.
	if v, _ := startupKey(t, got.Startup, "improvement"); v != "on" {
		t.Fatalf("harness check: improvement=%q, want \"on\" — the fixture or Read is at fault, "+
			"not the field under test", v)
	}

	v, present := startupKey(t, got.Startup, "telemetry")
	if !present {
		t.Fatalf("the telemetry key is absent after Read: the Startup mirror does not declare " +
			"the field that internal/config/startup.go carries, so encoding/json discards it. " +
			"app.js then shallow-copies what Read returned and posts it back through " +
			"`af config startup set`, erasing the operator's setting from disk")
	}
	if v != "on" {
		t.Errorf("telemetry round-tripped as %q, want \"on\"", v)
	}
}

// TestSettings_DefaultStartup_SeedsTelemetry covers the half no review comment named. The
// struct field alone is not enough: defaultStartup() seeds Quality, Fidelity and Improvement
// with "default" and would leave Telemetry empty, so an absent startup.json would yield a
// value the root's own defaultStartupConfig never produces. The C-4 backward-compat rule is
// that an absent file yields defaults — not an error, and not a half-populated struct.
func TestSettings_DefaultStartup_SeedsTelemetry(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	svc := New(root, nil)
	got, err := svc.Read(context.Background())
	if err != nil {
		t.Fatalf("Read with no startup.json on disk: %v", err)
	}

	if v, _ := startupKey(t, got.Startup, "quality"); v != "default" {
		t.Fatalf("harness check: quality=%q, want \"default\"", v)
	}

	v, present := startupKey(t, got.Startup, "telemetry")
	if !present || v != "default" {
		t.Errorf("absent startup.json yielded telemetry=%q (present=%v), want \"default\" to "+
			"match internal/config/startup.go defaultStartupConfig; the sibling gates already "+
			"default correctly", v, present)
	}
}
