package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSupplyChainDigestsArePinned keeps the verification half of the OpenObserve pin: the
// downloaded release must be checked against the pinned digest before it is unpacked. The digest
// and version VALUES are TestQuickstartPinValues' alone (D-61: one owner per datum), so none is
// repeated here; a pinned digest that is never checked is decoration.
func TestSupplyChainDigestsArePinned(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	install := topLevelShellFunction(string(data), "_install_openobserve")
	if install == "" {
		t.Fatal("_install_openobserve() is not defined at top level; the OpenObserve download and its verification live there")
	}
	if !strings.Contains(install, "sha256sum --check --strict") {
		t.Error("_install_openobserve() no longer verifies the downloaded artifact with `sha256sum --check --strict`; " +
			"the pinned OPENOBSERVE_SHA256_* values (owned by TestQuickstartPinValues) would be checked by nothing")
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
