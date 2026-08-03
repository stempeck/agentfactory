package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dtoFixtureDir holds the golden State/Report DTO payloads that `af telemetry status --json`
// and `af telemetry report --json` must reproduce. They live under this package's testdata
// because the web module's contract tests read them by relative path across the module
// boundary — Go's module system gates imports, not os.ReadFile.
const dtoFixtureDir = "telemetry-dto"

// TestTelemetryDTOFixturesAreManifested guards a collision that is invisible from the
// directory the fixtures serve. TestOTLPConformanceAgainstPinnedFixtures walks the WHOLE
// testdata tree and fails any .json without a manifest entry, so a DTO golden dropped here
// turns the root lane red for a reason that reads as an OTLP conformance failure.
//
// The second and third legs matter as much as the first: the manifest's verdict and
// provenance are what steer a fixture AWAY from the OTLP validator and the upstream-hash
// re-verification. An entry that exists but says "accept" would feed a telemetry DTO to a
// trace-schema validator.
func TestTelemetryDTOFixturesAreManifested(t *testing.T) {
	manifest := loadFixtureManifest(t)

	byFile := make(map[string]fixtureEntry, len(manifest.Fixtures))
	for _, f := range manifest.Fixtures {
		byFile[f.File] = f
	}

	dir := filepath.Join(testdataDir, dtoFixtureDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	var found int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		found++
		rel := dtoFixtureDir + "/" + e.Name()

		entry, listed := byFile[rel]
		if !listed {
			t.Errorf("golden %s has no manifest entry; otlp_conformance_test.go walks the whole "+
				"testdata tree and fails every unrecorded .json, so this turns 'make test' red", rel)
			continue
		}
		if entry.Verdict != "not-otlp" {
			t.Errorf("golden %s has verdict %q; a DTO payload is not an OTLP document, and any "+
				"other verdict feeds it to the trace-schema validator", rel, entry.Verdict)
		}
		if entry.Provenance != "synthetic" {
			t.Errorf("golden %s has provenance %q; only %q keeps it out of the upstream-bytes "+
				"hash re-verification, which these hand-authored files would fail",
				rel, entry.Provenance, "synthetic")
		}
	}

	if found == 0 {
		t.Fatalf("no *.json in %s — every assertion above is vacuous without at least one golden", dir)
	}
}

// TestTelemetryDTOFixturesAreWellFormed keeps the corpus honest about the one property a
// consumer in another module depends on before it can read anything else: that each file
// parses, carries the schema version, and names a state.
func TestTelemetryDTOFixturesAreWellFormed(t *testing.T) {
	dir := filepath.Join(testdataDir, dtoFixtureDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	// The state enum is per FAMILY, not per corpus.
	//
	// status and report share one enum; usage has its own, closed and disjoint beyond "ok" — a
	// failed query is query_failed, never "error", because the query failing is data rather than
	// an infrastructure fault. Checking one union against every file would accept a status golden
	// carrying query_failed, which is the sort of quiet widening that makes a guard stop guarding.
	// The family comes from the filename prefix, which this corpus's README declares part of the
	// interface.
	statesByFamily := map[string]map[string]bool{
		"status": {"ok": true, "degraded": true, "error": true},
		"report": {"ok": true, "degraded": true, "error": true},
		"usage": {
			"ok": true, "not_installed": true, "backend_down": true,
			"credential_rejected": true, "query_failed": true,
		},
	}

	var found int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		found++

		raw, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			t.Fatalf("reading %s: %v", e.Name(), readErr)
		}

		var doc struct {
			V     int    `json:"v"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Errorf("%s does not parse as JSON: %v", e.Name(), err)
			continue
		}
		if doc.V != SchemaVersion {
			t.Errorf("%s has v = %d, want %d — the DTO families version with the record schema",
				e.Name(), doc.V, SchemaVersion)
		}
		family, _, _ := strings.Cut(e.Name(), "-")
		valid, known := statesByFamily[family]
		if !known {
			t.Errorf("%s belongs to no known DTO family (looked at the %q prefix). The naming "+
				"convention is part of this corpus's interface: a file outside it is pinned by no "+
				"state rule at all, which is coverage that looks present and is not", e.Name(), family)
			continue
		}
		if !valid[doc.State] {
			t.Errorf("%s has state %q, which is not in the %s family's enum. Each family closes its "+
				"own set — a usage DTO reports query_failed where a status DTO reports error — and "+
				"crossing them is how a golden comes to pin a shape its producer cannot emit",
				e.Name(), doc.State, family)
		}
	}

	if found == 0 {
		t.Fatalf("no *.json in %s", dir)
	}
}

// TestTelemetryDTOFixturesCarryNoSecrets is the corpus-side half of the rule the DTO
// producer enforces at runtime: header COUNTS reach a consumer, header names and values
// never do. A golden that leaked one would teach the web lane's mirror to expect it.
func TestTelemetryDTOFixturesCarryNoSecrets(t *testing.T) {
	dir := filepath.Join(testdataDir, dtoFixtureDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	banned := []string{"authorization", "basic ", "bearer ", "telemetry.auth", "header_name"}

	var found int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		found++

		raw, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
		if readErr != nil {
			t.Fatalf("reading %s: %v", e.Name(), readErr)
		}
		lowered := strings.ToLower(string(raw))
		for _, bad := range banned {
			if strings.Contains(lowered, bad) {
				t.Errorf("%s contains %q; the DTO reports how many headers are configured, "+
					"never which ones or what they hold", e.Name(), bad)
			}
		}
	}

	if found == 0 {
		t.Fatalf("no *.json in %s", dir)
	}
}
