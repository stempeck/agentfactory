package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSupplyChainDigestsArePinned protects two literals that nothing else in the repository
// guards. A repo-wide search finds no other reference to either digest, and the
// supply-chain-lint CI job checks only pip --require-hashes, so the OpenObserve checksums are
// unguarded by construction.
//
// That matters here specifically: the credential fix edits quickstart.sh three lines below
// these constants. Both digests were independently verified against the real downloaded
// artifacts during review ("Digests self-computed from the real artifacts" was confirmed
// accurate for arm64 and amd64), so any change to them by this delivery is a supply-chain
// regression, not an improvement.
func TestSupplyChainDigestsArePinned(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	for _, want := range []struct{ name, line string }{
		{"version", `OPENOBSERVE_VERSION="v0.91.3"`},
		{"amd64", `OPENOBSERVE_SHA256_AMD64="d45cf6d0d249930f62d0627f4e2188390afaa4460d2dde6d7167029c3f2699fb"`},
		{"arm64", `OPENOBSERVE_SHA256_ARM64="49b22e69f04f026baddd8f23b9b588756e48a1127ac923121aaec1881769901b"`},
	} {
		if !strings.Contains(content, want.line) {
			t.Errorf("the pinned %s line is no longer present verbatim in quickstart.sh:\n  %s\n"+
				"These digests were verified against the real artifacts and are guarded by "+
				"nothing else in this repository", want.name, want.line)
		}
	}

	// The verification step itself must survive: a pinned digest that is never checked is
	// decoration.
	if !strings.Contains(content, "sha256sum --check") {
		t.Error("quickstart.sh no longer verifies the downloaded artifact with sha256sum --check")
	}
}

// TestSessionDoesNotImportTelemetry protects the layering rule the design states explicitly:
// internal/session receives a prepared []config.EnvVar and does NOT import internal/telemetry,
// mirroring how the models configuration is passed in. The rule keeps the launch package free
// of the telemetry package's types so telemetry can stay env-free and root-unaware.
//
// It is worth pinning now because the endpoint fix is adjacent to the launch env, and the
// tempting shortcut — reaching into internal/telemetry from the session layer — would be
// invisible until someone re-read the design doc.
func TestSessionDoesNotImportTelemetry(t *testing.T) {
	root := findModuleRoot(t)
	sessionDir := filepath.Join(root, "internal", "session")
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("reading internal/session: %v", err)
	}

	const forbidden = `"github.com/stempeck/agentfactory/internal/telemetry"`
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Test files may import it to build fixtures; the production boundary is what matters.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sessionDir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		checked++
		if strings.Contains(string(data), forbidden) {
			t.Errorf("internal/session/%s imports internal/telemetry: the session layer must "+
				"receive a prepared []config.EnvVar instead, so the telemetry package stays "+
				"env-free and is never handed a factory root through this path", e.Name())
		}
	}
	// An absence check that passes because it scanned nothing would be worthless.
	if checked == 0 {
		t.Fatal("scanned no production files in internal/session — the check proved nothing")
	}
}
