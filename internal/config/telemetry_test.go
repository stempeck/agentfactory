package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeTelemetryRoot creates a temp factory root with a telemetry.json containing data.
func writeTelemetryRoot(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "telemetry.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

func TestLoadTelemetryConfig(t *testing.T) {
	// The highest-value case: no telemetry.json is the NORMAL state before an operator
	// provisions a backend, so it must not be an error — otherwise every caller has to
	// special-case it. Mirrors LoadModelsConfig.
	t.Run("absent file yields an empty config and a nil error", func(t *testing.T) {
		dir := t.TempDir() // no telemetry.json written

		cfg, err := LoadTelemetryConfig(dir)
		if err != nil {
			t.Fatalf("expected nil error for absent file, got %v", err)
		}
		if errors.Is(err, ErrNotFound) {
			t.Fatal("absent file must NOT return ErrNotFound")
		}
		if cfg == nil {
			t.Fatal("expected non-nil cfg for absent file")
		}
		if cfg.Endpoint != "" || cfg.Headers != nil {
			t.Errorf("expected a zero-valued config, got %#v", cfg)
		}
	})

	t.Run("the canonical config round-trips every field", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{
  "endpoint": "http://127.0.0.1:5080",
  "otlp_http_path_traces": "/api/default/v1/traces",
  "headers": { "Authorization": "file:.agentfactory/secrets/telemetry.auth" },
  "protocol": "http/json",
  "export_timeout_ms": 500,
  "resource_attributes_extra": {}
}`)

		cfg, err := LoadTelemetryConfig(dir)
		if err != nil {
			t.Fatalf("LoadTelemetryConfig: %v", err)
		}
		if cfg.Endpoint != "http://127.0.0.1:5080" {
			t.Errorf("Endpoint = %q, want %q", cfg.Endpoint, "http://127.0.0.1:5080")
		}
		if cfg.OTLPHTTPPathTraces != "/api/default/v1/traces" {
			t.Errorf("OTLPHTTPPathTraces = %q, want %q", cfg.OTLPHTTPPathTraces, "/api/default/v1/traces")
		}
		wantHeaders := map[string]string{"Authorization": "file:.agentfactory/secrets/telemetry.auth"}
		if !reflect.DeepEqual(cfg.Headers, wantHeaders) {
			t.Errorf("Headers = %#v, want %#v", cfg.Headers, wantHeaders)
		}
		if cfg.Protocol != "http/json" {
			t.Errorf("Protocol = %q, want %q", cfg.Protocol, "http/json")
		}
		if cfg.ExportTimeoutMS != 500 {
			t.Errorf("ExportTimeoutMS = %d, want 500", cfg.ExportTimeoutMS)
		}
		if cfg.ResourceAttrsExtra == nil || len(cfg.ResourceAttrsExtra) != 0 {
			t.Errorf("ResourceAttrsExtra = %#v, want an empty non-nil map", cfg.ResourceAttrsExtra)
		}
	})

	// Additive schema evolution: a newer af writing a key this build does not know must
	// not break this build. Proves the decoder was left in its default tolerant mode
	// rather than being made strict about unrecognized keys.
	t.Run("unknown fields are tolerated", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{"endpoint":"http://127.0.0.1:5080","future_key":123,"another":{"nested":true}}`)

		cfg, err := LoadTelemetryConfig(dir)
		if err != nil {
			t.Fatalf("unknown fields must be ignored, got %v", err)
		}
		if cfg.Endpoint != "http://127.0.0.1:5080" {
			t.Errorf("Endpoint = %q, want the known field still populated", cfg.Endpoint)
		}
	})

	t.Run("an empty object loads and defaults the timeout", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{}`)

		cfg, err := LoadTelemetryConfig(dir)
		if err != nil {
			t.Fatalf("an empty object must load, got %v", err)
		}
		if cfg.ExportTimeoutMS != 500 {
			t.Errorf("ExportTimeoutMS = %d, want the 500 default", cfg.ExportTimeoutMS)
		}
	})

	t.Run("the endpoint scheme allowlist rejects non-http endpoints", func(t *testing.T) {
		for _, endpoint := range []string{"ftp://host/x", "file:///etc/passwd", "garbage", "http://", "://x"} {
			dir := writeTelemetryRoot(t, `{"endpoint":"`+endpoint+`"}`)

			_, err := LoadTelemetryConfig(dir)
			if err == nil {
				t.Errorf("endpoint %q must be rejected, got nil error", endpoint)
				continue
			}
			if !errors.Is(err, ErrInvalidType) {
				t.Errorf("endpoint %q: expected ErrInvalidType, got %v", endpoint, err)
			}
		}
	})

	// Plain http must stay legal: the design's default backend is reached over loopback
	// from inside the container, where https would be meaningless.
	t.Run("plain http on loopback is allowed", func(t *testing.T) {
		for _, endpoint := range []string{"http://127.0.0.1:5080", "https://collector.example.com"} {
			dir := writeTelemetryRoot(t, `{"endpoint":"`+endpoint+`"}`)

			if _, err := LoadTelemetryConfig(dir); err != nil {
				t.Errorf("endpoint %q must be accepted, got %v", endpoint, err)
			}
		}
	})

	t.Run("a non-positive export timeout is coerced to the default", func(t *testing.T) {
		for _, body := range []string{`{"export_timeout_ms":0}`, `{"export_timeout_ms":-1}`} {
			dir := writeTelemetryRoot(t, body)

			cfg, err := LoadTelemetryConfig(dir)
			if err != nil {
				t.Fatalf("%s must load, got %v", body, err)
			}
			if cfg.ExportTimeoutMS != 500 {
				t.Errorf("%s: ExportTimeoutMS = %d, want the 500 default", body, cfg.ExportTimeoutMS)
			}
		}
	})

	t.Run("an out-of-range export timeout is rejected", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{"export_timeout_ms":60001}`)

		_, err := LoadTelemetryConfig(dir)
		if !errors.Is(err, ErrInvalidType) {
			t.Fatalf("expected ErrInvalidType above the ceiling, got %v", err)
		}
	})

	t.Run("the ceiling itself is accepted", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{"export_timeout_ms":60000}`)

		cfg, err := LoadTelemetryConfig(dir)
		if err != nil {
			t.Fatalf("the ceiling value must be accepted, got %v", err)
		}
		if cfg.ExportTimeoutMS != 60000 {
			t.Errorf("ExportTimeoutMS = %d, want 60000", cfg.ExportTimeoutMS)
		}
	})

	t.Run("an unsupported protocol is rejected", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{"protocol":"grpc"}`)

		_, err := LoadTelemetryConfig(dir)
		if !errors.Is(err, ErrInvalidType) {
			t.Fatalf("expected ErrInvalidType for an unsupported protocol, got %v", err)
		}
	})

	t.Run("malformed JSON is an error", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{not json`)

		if _, err := LoadTelemetryConfig(dir); err == nil {
			t.Fatal("expected an error for malformed JSON")
		}
	})

	// A header value is a secret reference. It must never reach an operator's terminal,
	// and the error paths are the easiest place to leak it by accident.
	t.Run("a header validation error names the key but never the value", func(t *testing.T) {
		const badValue = "file:/etc/shadow; cat /etc/passwd"
		dir := writeTelemetryRoot(t, `{"headers":{"Authorization":"`+badValue+`"}}`)

		_, err := LoadTelemetryConfig(dir)
		if err == nil {
			t.Fatal("expected an error for a header reference containing shell metacharacters")
		}
		if !errors.Is(err, ErrInvalidType) {
			t.Errorf("expected ErrInvalidType, got %v", err)
		}
		if !strings.Contains(err.Error(), "Authorization") {
			t.Errorf("error = %q, want it to name the offending header key", err.Error())
		}
		if strings.Contains(err.Error(), badValue) || strings.Contains(err.Error(), "/etc/shadow") {
			t.Errorf("error leaked the header VALUE: %q", err.Error())
		}
	})

	t.Run("an empty header value is rejected", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{"headers":{"Authorization":""}}`)

		_, err := LoadTelemetryConfig(dir)
		if !errors.Is(err, ErrInvalidType) {
			t.Fatalf("expected ErrInvalidType for an empty header value, got %v", err)
		}
	})

	// A literal (non file:) header value is legal — an operator may point at a collector
	// that takes a static, non-secret header — but it still must not be echoed anywhere.
	t.Run("a literal header value is accepted", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `{"headers":{"X-Stream-Name":"default"}}`)

		if _, err := LoadTelemetryConfig(dir); err != nil {
			t.Fatalf("a literal header value must be accepted, got %v", err)
		}
	})

	t.Run("a non-object body is a parse error", func(t *testing.T) {
		dir := writeTelemetryRoot(t, `["endpoint"]`)

		if _, err := LoadTelemetryConfig(dir); err == nil {
			t.Fatal("expected an error for a JSON array body")
		}
	})
}

func TestTelemetryConfigPath(t *testing.T) {
	got := TelemetryConfigPath("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "telemetry.json")
	if got != want {
		t.Errorf("TelemetryConfigPath: got %q, want %q", got, want)
	}
}

func TestTelemetryDir(t *testing.T) {
	got := TelemetryDir("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "telemetry")
	if got != want {
		t.Errorf("TelemetryDir: got %q, want %q", got, want)
	}
}
