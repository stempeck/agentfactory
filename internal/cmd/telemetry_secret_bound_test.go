package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestSecretRefIsBoundedToTheFactoryRoot exercises the containment bound directly.
//
// The bound exists because of the dereference, not independently of it: before the export path read
// these files, a traversing path was inert, and configuration validation rejects shell
// metacharacters but permits `..` and absolute paths. Dereferencing without a bound turns a
// writable telemetry.json into an arbitrary-file-read whose contents are then posted to a
// configured endpoint. A security bound with no test is a comment.
func TestSecretRefIsBoundedToTheFactoryRoot(t *testing.T) {
	root := t.TempDir()
	secrets := filepath.Join(root, ".agentfactory", "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "telemetry.auth"), []byte("Basic ok"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	// A file outside the root that must never be readable through a header reference.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("SENSITIVE"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	t.Run("a path inside the root resolves", func(t *testing.T) {
		cfg := config.TelemetryConfig{Headers: map[string]string{
			"Authorization": "file:.agentfactory/secrets/telemetry.auth",
		}}
		got, err := derefTelemetryHeaders(root, cfg)
		if err != nil {
			t.Fatalf("a legitimate in-root reference was refused: %v", err)
		}
		if got.Headers["Authorization"] != "Basic ok" {
			t.Errorf("Authorization = %q, want the file contents", got.Headers["Authorization"])
		}
	})

	t.Run("dot-dot traversal is refused", func(t *testing.T) {
		cfg := config.TelemetryConfig{Headers: map[string]string{
			"Authorization": "file:../../etc/passwd",
		}}
		_, err := derefTelemetryHeaders(root, cfg)
		if err == nil {
			t.Fatal("a traversing reference was accepted: telemetry.json becomes an arbitrary-file read")
		}
		if !strings.Contains(err.Error(), "Authorization") {
			t.Errorf("error does not name the header key: %v", err)
		}
		if strings.Contains(err.Error(), "passwd") {
			t.Errorf("error echoed the path, which the contract forbids: %v", err)
		}
	})

	t.Run("an absolute path outside the root is refused", func(t *testing.T) {
		cfg := config.TelemetryConfig{Headers: map[string]string{
			"Authorization": "file:" + outside,
		}}
		if _, err := derefTelemetryHeaders(root, cfg); err == nil {
			t.Fatal("an absolute out-of-root reference was accepted")
		}
	})

	t.Run("a symlink escaping the root is refused", func(t *testing.T) {
		link := filepath.Join(secrets, "escape.auth")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		cfg := config.TelemetryConfig{Headers: map[string]string{
			"Authorization": "file:.agentfactory/secrets/escape.auth",
		}}
		got, err := derefTelemetryHeaders(root, cfg)
		if err == nil {
			t.Fatalf("a symlink out of the root was followed: the bound is purely lexical and the "+
				"contents (%q) would be sent to the configured endpoint", got.Headers["Authorization"])
		}
	})

	t.Run("an empty secret file is refused rather than sent", func(t *testing.T) {
		empty := filepath.Join(secrets, "empty.auth")
		if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		cfg := config.TelemetryConfig{Headers: map[string]string{
			"Authorization": "file:.agentfactory/secrets/empty.auth",
		}}
		if _, err := derefTelemetryHeaders(root, cfg); err == nil {
			t.Error("an empty credential was accepted; it would reach the backend as an auth failure")
		}
	})

	t.Run("a whitespace-only secret file is refused", func(t *testing.T) {
		ws := filepath.Join(secrets, "ws.auth")
		if err := os.WriteFile(ws, []byte("   \t  \n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		cfg := config.TelemetryConfig{Headers: map[string]string{
			"Authorization": "file:.agentfactory/secrets/ws.auth",
		}}
		if _, err := derefTelemetryHeaders(root, cfg); err == nil {
			t.Error("a whitespace-only credential was accepted; it reaches the backend as a " +
				"malformed header and reads as an authentication failure")
		}
	})

	t.Run("a trailing newline is trimmed so both planes agree", func(t *testing.T) {
		nl := filepath.Join(secrets, "newline.auth")
		if err := os.WriteFile(nl, []byte("Basic trimmed\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		cfg := config.TelemetryConfig{Headers: map[string]string{
			"Authorization": "file:.agentfactory/secrets/newline.auth",
		}}
		got, err := derefTelemetryHeaders(root, cfg)
		if err != nil {
			t.Fatalf("deref: %v", err)
		}
		// The launch plane resolves the same reference with $(cat …), which strips trailing
		// newlines. Without the trim this is an invalid HTTP header value and fails at
		// request-write time — a different silent failure in place of the one being fixed.
		if got.Headers["Authorization"] != "Basic trimmed" {
			t.Errorf("Authorization = %q, want the trailing newline trimmed", got.Headers["Authorization"])
		}
	})

	t.Run("a literal header is passed through untouched", func(t *testing.T) {
		cfg := config.TelemetryConfig{Headers: map[string]string{"Authorization": "Basic literal"}}
		got, err := derefTelemetryHeaders(root, cfg)
		if err != nil {
			t.Fatalf("deref: %v", err)
		}
		if got.Headers["Authorization"] != "Basic literal" {
			t.Errorf("a non-reference header was altered: %q", got.Headers["Authorization"])
		}
	})
}

// TestSecretFailureCauseSurvivesToTheStatusSurface pins the CAUSE an operator is told.
//
// `af telemetry status` reports header counts and never header identity, so it cannot print the
// underlying error. It previously printed one fixed sentence — "could not be read from its secret
// file" — for every failure, which tells an operator whose credential was REFUSED for escaping the
// factory root to go and check that the file exists. It does exist; that was never the problem.
func TestSecretFailureCauseSurvivesToTheStatusSurface(t *testing.T) {
	root := t.TempDir()
	secrets := filepath.Join(root, ".agentfactory", "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Distinctive on purpose: a plausible word like "secret" also appears in the legitimate cause
	// prose ("its secret file is empty"), so it cannot distinguish a leak from a correct message.
	const secretContent = "s3cr3t-payload-must-never-be-printed"
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte(secretContent), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "empty.auth"), []byte("  \n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	for _, tc := range []struct {
		name, ref, wantCause string
	}{
		{"outside the root", "file:" + outside, "resolves outside the factory root and was refused"},
		{"missing file", "file:.agentfactory/secrets/absent.auth", "could not be read from its secret file"},
		{"empty file", "file:.agentfactory/secrets/empty.auth", "secret file is empty"},
		{"no path", "file:", "file: reference has no path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.TelemetryConfig{Headers: map[string]string{"Authorization": tc.ref}}
			_, err := derefTelemetryHeaders(root, cfg)
			if err == nil {
				t.Fatal("deref accepted a reference it must refuse")
			}
			got := telemetrySecretCause(err)
			if !strings.Contains(got, tc.wantCause) {
				t.Errorf("status would tell the operator %q, want it to name %q", got, tc.wantCause)
			}
			// The rule the whole surface is built on: the cause never carries identity or content.
			for _, leak := range []string{"Authorization", outside, secretContent} {
				if strings.Contains(got, leak) {
					t.Errorf("the status cause leaked %q: %s", leak, got)
				}
			}
		})
	}
}
