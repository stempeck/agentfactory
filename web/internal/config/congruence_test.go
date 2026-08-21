package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/exec"
)

// ---- #620 Phase 2 · the frame-lift's enforcing test ----
//
// Every guard this module had before was NOMINATIVE: one test named `improvement`, another named
// `telemetry`, and each was written AFTER that field had already been erased from an operator's disk.
// A test that names a field cannot fire for a field nobody has thought of yet, which is the entire
// failure mode.
//
// This one names nothing. It takes af-core's own machine-generated portrait of its schema — the
// congruence fixtures, which by construction cannot forget a field — pushes it through the console's
// read → edit → write path, and requires every key to come back. Reintroduce a typed decode anywhere
// on that path and this fails, listing the keys it would have erased.
//
// Its scope is bounded in one known way, and the bound is stated rather than papered over:
// dispatch.mappings[].label is pinned to "" with omitempty in the generator
// (internal/config/congruence_gen.go:92-97), so it is absent from the fixture and a consumer that
// dropped `label` would not be caught here.

// rootConfigDir locates af-core's internal/config directory from inside the WEB module. It is the
// package's SINGLE repo-relative resolver: the congruence fixtures and paths.go both live there, and
// two near-identical walk-ups in one package would be two places to fix when the layout moves.
//
// Deliberately a cwd-relative walk-up, and deliberately keyed on two markers. `go test` pins the test
// binary's working directory to the package source directory, so the walk always starts from
// web/internal/config. A go.mod walk-up would stop at web/go.mod — web/ is its own module, one level
// short of the repo root (`go env GOMOD` from here reports .../web/go.mod). runtime.Caller, the root
// module's idiom, degrades to a MODULE-RELATIVE path under -trimpath and would then read nothing.
// `git rev-parse --show-toplevel` is correct even in a worktree but needs the git binary and a .git
// directory, neither guaranteed in a container image. Keying on the artifacts needs none of it. Two
// markers rather than one, so the walk cannot be captured a level early by this very package: at
// dir=web/ the candidate IS web/internal/config, which has neither paths.go nor the generator.
//
// Reading these files is legal where IMPORTING the package is not: Go's module system and internal
// seal gate imports, not os.ReadFile (extractability_test.go asserts the import never appears).
//
// It never t.Skips. These tests ARE the enforcement of the frame-lift; a resolver that skipped would
// let them silently vanish from CI, which is the precise failure this phase was written to end.
func rootConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	start := dir
	for {
		cand := filepath.Join(dir, "internal", "config")
		_, perr := os.Stat(filepath.Join(cand, "paths.go"))
		_, gerr := os.Stat(filepath.Join(cand, "congruence_gen.go"))
		if perr == nil && gerr == nil {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate af-core's internal/config walking up from %s — its congruence "+
				"fixtures and paths.go are the SPEC for these tests. A missing root module means it is "+
				"absent or has moved; it never means these tests may be skipped.", start)
		}
		dir = parent
	}
}

// congruenceFixtureDir is the ROOT module's Phase-1 fixture directory.
func congruenceFixtureDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(rootConfigDir(t), "testdata", "congruence")
}

// congruenceFixture reads one fixture by name, failing loud. The decode probe is an anti-vacuity
// guard: an empty or unparseable fixture would make every key-survival assertion below trivially true.
func congruenceFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(congruenceFixtureDir(t), name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		t.Fatalf("fixture %s is not a JSON object: %v", path, err)
	}
	if len(probe) == 0 {
		t.Fatalf("fixture %s decoded to zero keys — every assertion over it would pass vacuously", path)
	}
	return data
}

// fixtureFactory materialises a hermetic factory root seeded with the named congruence fixtures.
func fixtureFactory(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, dotDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		mustWrite(t, filepath.Join(dir, name+".json"), string(congruenceFixture(t, name)))
	}
	return root
}

// writeServiceAt is writeService with an explicit factory root, so a test can Read a POPULATED
// factory and then assert on the payload the write path piped to af's stdin. The REAL exec.Wrapper
// is in the path, so a captured payload also proves the wrapper's own allowlist accepted the file.
func writeServiceAt(t *testing.T, root string) (*Service, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{}
	return New(root, exec.NewWrapper(fr, "")), fr
}

// leafPaths flattens a decoded JSON document to path→value, recursing through objects AND arrays.
// Recursing through arrays is the point: mappings[].model and the 14 recovery.* keys are both
// nested, and a top-level key-count comparison would pass while every one of them was dropped.
func leafPaths(prefix string, v any, out map[string]string) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(t) {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			leafPaths(p, t[k], out)
		}
	case []any:
		for i, e := range t {
			leafPaths(fmt.Sprintf("%s[%d]", prefix, i), e, out)
		}
	default:
		out[prefix] = fmt.Sprintf("%v", v)
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func decodeAny(t *testing.T, what string, data []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s is not decodable JSON: %v (%q)", what, err, data)
	}
	return v
}

// TestSettingsRoundTrip_CanonicalSchemaCongruence asserts that every key af-core's schema carries
// survives Read → unrelated-field merge → captured Write payload, recursively.
func TestSettingsRoundTrip_CanonicalSchemaCongruence(t *testing.T) {
	// The four raw-tier files the console can write. factory.json is raw but read-only and is
	// covered by the read-fidelity subtest below; the secret-tier files are covered by
	// TestSettings_RawTierExcludesSecretFiles.
	cases := []struct {
		file      string
		editKey   string
		editValue string
	}{
		{"dispatch", "trigger_label", "edited-by-console"},
		{"startup", "quality", "off"},
		{"messaging", "edited_by_console", "marker"},
		{"statusline", "edited_by_console", "marker"},
	}

	// Anti-vacuity, before anything else: the fixtures must still carry the keys the old mirror
	// dropped, or this test would pass because there is nothing left to lose.
	t.Run("fixtures still exhibit the drift this test exists to catch", func(t *testing.T) {
		disp := decodeAny(t, "dispatch fixture", congruenceFixture(t, "dispatch")).(map[string]any)
		wf, ok := disp["workflows"].([]any)
		if !ok || len(wf) == 0 {
			t.Fatalf("dispatch.json fixture has no non-empty `workflows` array — the fixture no longer "+
				"exhibits the canonical keys the web mirror used to drop, so this test guards nothing. "+
				"Regenerate with: go test ./internal/config/ -run TestConfigCongruence_Regenerate -update. Got: %v", disp["workflows"])
		}
		if lbl, _ := wf[0].(map[string]any)["label"].(string); lbl == "" {
			t.Fatal("dispatch.json fixture: workflows[0].label is empty — anti-vacuity gate failed")
		}
		maps, ok := disp["mappings"].([]any)
		if !ok || len(maps) == 0 {
			t.Fatal("dispatch.json fixture has no `mappings` — anti-vacuity gate failed")
		}
		if m, _ := maps[0].(map[string]any)["model"].(string); m == "" {
			t.Fatal("dispatch.json fixture: mappings[0].model is empty — anti-vacuity gate failed")
		}

		start := decodeAny(t, "startup fixture", congruenceFixture(t, "startup")).(map[string]any)
		rec, ok := start["recovery"].(map[string]any)
		if !ok {
			t.Fatal("startup.json fixture has no `recovery` object — anti-vacuity gate failed")
		}
		if len(rec) != 14 {
			t.Fatalf("startup.json fixture recovery has %d keys, want 14 (pinned by "+
				"internal/config/congruence_drift_test.go:110) — the fixture has drifted from the schema it portrays", len(rec))
		}
		for k, v := range rec {
			switch tv := v.(type) {
			case string:
				if tv == "" {
					t.Errorf("recovery.%s is the empty string — a zero value cannot prove survival", k)
				}
			case float64:
				if tv == 0 {
					t.Errorf("recovery.%s is 0 — a zero value cannot prove survival", k)
				}
			case nil:
				t.Errorf("recovery.%s is null — a zero value cannot prove survival", k)
			}
		}
	})

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			fixture := congruenceFixture(t, tc.file)
			root := fixtureFactory(t, tc.file)
			svc, fr := writeServiceAt(t, root)

			view, err := svc.Read(context.Background())
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			fv, ok := view.Files[tc.file]
			if !ok {
				t.Fatalf("payload has no files[%q] entry at all", tc.file)
			}
			if len(fv.Doc) == 0 {
				t.Fatalf("files[%q].doc is empty — the document on disk was not served", tc.file)
			}

			// Read fidelity. Compared as decoded documents, not as bytes: marshaling a
			// json.RawMessage compacts whitespace and HTML-escapes < > &, so byte equality would
			// fail on a pretty-printed fixture even when every key survived. Key SET and VALUES are
			// what this phase promises; byte-for-byte formatting is af-core's to own.
			wantDoc := decodeAny(t, "fixture", fixture)
			gotDoc := decodeAny(t, "served doc", fv.Doc)
			if !reflect.DeepEqual(wantDoc, gotDoc) {
				wantLeaves, gotLeaves := map[string]string{}, map[string]string{}
				leafPaths("", wantDoc, wantLeaves)
				leafPaths("", gotDoc, gotLeaves)
				for _, p := range missingPaths(wantLeaves, gotLeaves) {
					t.Errorf("READ dropped %s.json key %q — a typed decode is back on the raw path", tc.file, p)
				}
				t.Fatalf("served %s doc is not the document on disk", tc.file)
			}

			// The per-file fingerprint must digest the ON-DISK bytes: it is echoed back as af-core's
			// --if-content-hash, which hashes os.ReadFile(path).
			if want := hashHex(fixture); fv.Fingerprint != want {
				t.Errorf("files[%q].fingerprint = %q, want sha256 of the on-disk bytes %q", tc.file, fv.Fingerprint, want)
			}

			// Simulate the client edit: retain the document as read, change exactly one unrelated
			// scalar, send the whole thing back. This is what makes it a round-trip rather than a
			// read test — the erasure happened on the WRITE leg.
			merged := map[string]any{}
			if err := json.Unmarshal(fv.Doc, &merged); err != nil {
				t.Fatal(err)
			}
			merged[tc.editKey] = tc.editValue
			payload, err := json.Marshal(merged)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Write(context.Background(), tc.file, payload, ""); err != nil {
				t.Fatalf("Write(%s): %v", tc.file, err)
			}

			// The payload actually reached the af seam — and via the real Wrapper, so allowlist copy
			// #2 accepted this file too.
			if fr.writes != 1 {
				t.Fatalf("af write invocations = %d, want exactly 1 (a test that never wrote proves nothing)", fr.writes)
			}
			if fr.verb != "config" || len(fr.args) != 2 || fr.args[0] != tc.file || fr.args[1] != "set" {
				t.Fatalf("argv = %s %v, want config [%s set]", fr.verb, fr.args, tc.file)
			}

			// The load-bearing assertion: recursive key survival through the whole round-trip.
			wantLeaves, gotLeaves := map[string]string{}, map[string]string{}
			leafPaths("", decodeAny(t, "fixture", fixture), wantLeaves)
			leafPaths("", decodeAny(t, "captured write payload", fr.stdin), gotLeaves)

			var dropped []string
			for _, p := range missingPaths(wantLeaves, gotLeaves) {
				dropped = append(dropped, p)
				t.Errorf("the console ERASED %s.json key %q on save: it was on disk, it is absent from "+
					"the document sent to `af config %s set`, and that setter replaces the whole file",
					tc.file, p, tc.file)
			}
			if len(dropped) == 0 {
				// Values must survive too, not just key names — an edit must not smear its neighbours.
				for p, want := range wantLeaves {
					if p == tc.editKey {
						continue
					}
					if got := gotLeaves[p]; got != want {
						t.Errorf("%s.json key %q changed value across the round-trip: %q → %q", tc.file, p, want, got)
					}
				}
				if got := gotLeaves[tc.editKey]; got != tc.editValue {
					t.Errorf("the simulated edit did not land: %s = %q, want %q", tc.editKey, got, tc.editValue)
				}
			}
		})
	}

	// factory.json is raw but read-only: served faithfully, refused on write.
	t.Run("factory is served raw and refused on write", func(t *testing.T) {
		fixture := congruenceFixture(t, "factory")
		root := fixtureFactory(t, "factory")
		svc, fr := writeServiceAt(t, root)

		view, err := svc.Read(context.Background())
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		fv := view.Files["factory"]
		if !reflect.DeepEqual(decodeAny(t, "fixture", fixture), decodeAny(t, "served doc", fv.Doc)) {
			t.Errorf("factory.json is not served faithfully")
		}
		if fv.Writable {
			t.Error("factory.json is marked writable — C-9 says it is not")
		}
		if _, err := svc.Write(context.Background(), "factory", fixture, ""); err == nil || fr.writes != 0 {
			t.Errorf("Write(factory) err=%v writes=%d, want a refusal before any exec", err, fr.writes)
		}
	})
}

// missingPaths returns the sorted leaf paths present in want but absent from got.
func missingPaths(want, got map[string]string) []string {
	var out []string
	for p := range want {
		if _, ok := got[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
