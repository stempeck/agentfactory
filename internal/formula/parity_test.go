package formula

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parityDir is the checked-in corpus shared by BOTH validator consumers: this Go test (the
// engine-of-record) and web/conformance/test-engine.js (the shipped JS engine). One manifest,
// two consumers, two CI lanes — a rule that lands in one validator but not the other flips a
// recorded verdict and reddens a build. See .designs/502 conflicts.md T8 for why test DATA may
// cross the module wall (no import, no go.mod entry).
const parityDir = "testdata/parity"

type parityFixture struct {
	File    string `json:"file"`
	Verdict string `json:"verdict"`         // "accept" | "reject"
	Stage   string `json:"stage,omitempty"` // "parse" | "validate" | "toposort" (rejects only)
	Lamp    string `json:"lamp,omitempty"`  // JS lamp category, keyed on by the Node consumer
	Note    string `json:"note,omitempty"`
}

type parseDialectFixture struct {
	File string `json:"file"`
	Go   string `json:"go"` // "accept" | "reject"
	JS   string `json:"js"` // "accept" | "parse-error"
	Note string `json:"note,omitempty"`
}

type parityManifest struct {
	Fixtures     []parityFixture       `json:"fixtures"`
	ParseDialect []parseDialectFixture `json:"parseDialect"`
}

// composedVerdict runs the same pipeline af sling does — decode + inferType + Validate (all via
// Parse) then TopologicalSort — and reports the composed accept/reject plus which stage rejected.
// Stage is distinguished by the "parsing TOML:" wrap prefix (parse) vs any other Parse error
// (validate) vs a TopologicalSort error (toposort). It never keys on full message text, which
// diverges across engines and stages (validate.go's "cycle detected involving step" vs sort.go's
// "cycle detected in dependencies" vs the JS cycles lamp).
func composedVerdict(data []byte) (accepted bool, stage string, err error) {
	f, err := Parse(data)
	if err != nil {
		if strings.HasPrefix(err.Error(), "parsing TOML:") {
			return false, "parse", err
		}
		return false, "validate", err
	}
	if _, err := f.TopologicalSort(); err != nil {
		return false, "toposort", err
	}
	return true, "", nil
}

func loadParityManifest(t *testing.T) parityManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(parityDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read parity manifest: %v", err)
	}
	var m parityManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse parity manifest: %v", err)
	}
	if len(m.Fixtures) == 0 {
		t.Fatal("parity manifest has zero fixtures")
	}
	return m
}

func readFixture(t *testing.T, file string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(parityDir, file))
	if err != nil {
		t.Fatalf("read fixture %s: %v", file, err)
	}
	return data
}

// TestParity asserts every manifest fixture's composed verdict (accept/reject) and, for
// rejections, the rejecting stage — the Go half of the dual-consumer interlock.
func TestParity(t *testing.T) {
	m := loadParityManifest(t)
	for _, fx := range m.Fixtures {
		t.Run(fx.File, func(t *testing.T) {
			accepted, stage, gotErr := composedVerdict(readFixture(t, fx.File))
			switch fx.Verdict {
			case "accept":
				if !accepted {
					t.Errorf("composed verdict = reject(stage=%s, err=%v), want accept", stage, gotErr)
				}
			case "reject":
				if accepted {
					t.Errorf("composed verdict = accept, want reject")
				}
				if fx.Stage != "" && stage != fx.Stage {
					t.Errorf("rejected at stage %q, manifest says %q (err=%v)", stage, fx.Stage, gotErr)
				}
			default:
				t.Fatalf("unknown verdict %q in manifest", fx.Verdict)
			}
		})
	}
}

// TestParity_DatetimeDialect pins the ONE intentional Go/JS divergence: a datetime literal on an
// untyped key is Go-valid (BurntSushi discards the unknown key) but a JS parse error. Only the Go
// side is asserted here; the JS parse-error channel is asserted in web/conformance/test-engine.js.
func TestParity_DatetimeDialect(t *testing.T) {
	m := loadParityManifest(t)
	if len(m.ParseDialect) == 0 {
		t.Fatal("parity manifest has zero parseDialect fixtures")
	}
	for _, dx := range m.ParseDialect {
		t.Run(dx.File, func(t *testing.T) {
			accepted, stage, gotErr := composedVerdict(readFixture(t, dx.File))
			switch dx.Go {
			case "accept":
				if !accepted {
					t.Errorf("Go rejected (stage=%s, err=%v), manifest says Go accepts", stage, gotErr)
				}
			case "reject":
				if accepted {
					t.Errorf("Go accepted, manifest says Go rejects")
				}
			default:
				t.Fatalf("unknown go verdict %q in parseDialect", dx.Go)
			}
		})
	}
}

// TestParity_EveryFixtureHasManifestEntry guards against a *.toml fixture landing without a
// recorded verdict — the exact drift this corpus exists to catch.
func TestParity_EveryFixtureHasManifestEntry(t *testing.T) {
	m := loadParityManifest(t)
	recorded := map[string]bool{}
	for _, fx := range m.Fixtures {
		recorded[fx.File] = true
	}
	for _, dx := range m.ParseDialect {
		recorded[dx.File] = true
	}
	matches, err := filepath.Glob(filepath.Join(parityDir, "*.toml"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("no *.toml fixtures found under %s", parityDir)
	}
	for _, p := range matches {
		base := filepath.Base(p)
		if !recorded[base] {
			t.Errorf("fixture %s has no manifest entry", base)
		}
	}
}
