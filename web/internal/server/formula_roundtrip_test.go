package server

// #502 Phase 4 — AC-2 production round-trip + the two Error-Surface HANDLER halves (mid-save I/O 400,
// store-listing 502). These reuse the Phase-3 formula-handler harness in server_test.go
// (formulaServer/tokPUT/serve/wtoken/okVerdict/fakeGenerator/fakeFormulaStore) — no new harness.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/formulas"
)

// roundtripRepoRoot walks up from the package dir (the test CWD) to the worktree root that holds
// .agentfactory/store/formulas — the same walk-up precedent as entrypoint/guard_test.go. It lets the
// AC-2 test edit a GENUINE live formula's bytes while writing only into a t.TempDir() sandbox (ADR-018:
// the live store is never touched). Distinct name from the package's integration-tagged repoRoot
// (bridge_integration_test.go) — this file is untagged, so both compile together under -tags integration.
func roundtripRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".agentfactory", "store", "formulas")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate .agentfactory/store/formulas by walking up from %q", dir)
	return ""
}

// putJSON builds the PUT body {text, base_sha256} (json.Marshal handles TOML escaping).
func putJSON(text, baseSHA string) string {
	b, _ := json.Marshal(map[string]string{"text": text, "base_sha256": baseSHA})
	return string(b)
}

// TestFormula_ProductionRoundTrip_ByteDiff is AC-2: over a REAL formulas.New(root) store seeded from a
// genuine live formula, GET a formula through the handler, mutate exactly ONE field, PUT it back with the
// CAS base + write token, re-GET, and prove the round-trip is byte-transparent AND the diff is limited to
// the intended change (reversing the single edit reconstructs the original). Table-driven over
// ultra-review and the 22-station web-design convoy formula for multi-line breadth. Closes with a CAS
// interlock: a re-PUT carrying the now-stale base is refused 409 — proving the real sha256 compare-and-
// swap guards the production path (the fake-store handler tests only prove the sentinel→status map).
func TestFormula_ProductionRoundTrip_ByteDiff(t *testing.T) {
	cases := []struct{ name, oldSpan, newSpan string }{
		{"ultra-review", "version = 1", "version = 2"},
		{"web-design", "version = 2", "version = 3"}, // 22-station convoy formula (multi-line breadth)
	}
	srcDir := filepath.Join(roundtripRepoRoot(t), ".agentfactory", "store", "formulas")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".agentfactory", "store", "formulas")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			realBytes, err := os.ReadFile(filepath.Join(srcDir, tc.name+".formula.toml"))
			if err != nil {
				t.Fatalf("read real formula %q: %v", tc.name, err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.name+".formula.toml"), realBytes, 0o644); err != nil {
				t.Fatal(err)
			}

			store := formulas.New(root)
			s, _ := formulaServer(t, store, &fakeGenerator{}, okVerdict)

			// 1) GET → {text, sha256}; text must be byte-identical to the seeded file.
			getRec := serve(s, httptest.NewRequest(http.MethodGet, "/api/formulas/"+tc.name, nil))
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET: code=%d body=%s", getRec.Code, getRec.Body.String())
			}
			var getEnv struct {
				Data struct{ Name, Text, SHA256 string } `json:"data"`
			}
			if err := json.Unmarshal(getRec.Body.Bytes(), &getEnv); err != nil {
				t.Fatalf("GET env: %v", err)
			}
			original := getEnv.Data.Text
			if original != string(realBytes) {
				t.Fatalf("GET text is not byte-identical to the on-disk formula")
			}

			// 2) mutate exactly ONE field — the single version line (guaranteed one occurrence).
			if n := strings.Count(original, tc.oldSpan); n != 1 {
				t.Fatalf("fixture invalid: %q must occur exactly once in %s, got %d", tc.oldSpan, tc.name, n)
			}
			mutated := strings.Replace(original, tc.oldSpan, tc.newSpan, 1)
			if mutated == original {
				t.Fatal("fixture invalid: the one-field mutation did not change the text")
			}

			// 3) PUT with the CAS base + token.
			putRec := serve(s, tokPUT("/api/formulas/"+tc.name, putJSON(mutated, getEnv.Data.SHA256)))
			if putRec.Code != http.StatusOK {
				t.Fatalf("PUT: code=%d body=%s", putRec.Code, putRec.Body.String())
			}

			// 4) re-GET and byte-diff.
			reRec := serve(s, httptest.NewRequest(http.MethodGet, "/api/formulas/"+tc.name, nil))
			var reEnv struct {
				Data struct{ Text, SHA256 string } `json:"data"`
			}
			if err := json.Unmarshal(reRec.Body.Bytes(), &reEnv); err != nil {
				t.Fatalf("re-GET env: %v", err)
			}
			if reEnv.Data.Text != mutated {
				t.Fatalf("round-trip not byte-transparent (no newline fixup / normalization):\n got: %q\nwant: %q",
					reEnv.Data.Text, mutated)
			}
			// diff limited to the intended change: reversing the single edit reconstructs the original.
			if strings.Replace(reEnv.Data.Text, tc.newSpan, tc.oldSpan, 1) != original {
				t.Fatal("diff not limited to the intended field: reversing the one edit did not reconstruct the original")
			}

			// 5) CAS interlock: re-PUT with the now-stale base → 409, disk untouched.
			staleRec := serve(s, tokPUT("/api/formulas/"+tc.name, putJSON(original, getEnv.Data.SHA256)))
			if staleRec.Code != http.StatusConflict {
				t.Fatalf("stale-base re-PUT: code=%d, want 409 (CAS interlock); body=%s", staleRec.Code, staleRec.Body.String())
			}
		})
	}
}

// TestFormulaSave_MidSaveIOFailure_400Message is the Error-Surface row-10 HANDLER half: a store Write
// I/O failure (disk full / read-only) surfaces the store's verbatim message in the envelope at the save
// path (the default sentinel branch → 400), and never a torn success.
func TestFormulaSave_MidSaveIOFailure_400Message(t *testing.T) {
	fs := &fakeFormulaStore{writeErr: errors.New("disk full")}
	s, _ := formulaServer(t, fs, &fakeGenerator{}, okVerdict)
	rec := serve(s, tokPUT("/api/formulas/foo", putJSON("[meta]\nname='foo'\n", "")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mid-save I/O failure: code=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "disk full") {
		t.Fatalf("400 must carry the store's verbatim I/O message: %s", rec.Body.String())
	}
}

// TestFormulaList_ListingFailure_502 is the Error-Surface row-11 HANDLER half: a store List failure is an
// honest 502, NEVER an empty 200 "no formulas".
func TestFormulaList_ListingFailure_502(t *testing.T) {
	fs := &fakeFormulaStore{listErr: errors.New("permission denied")}
	s, _ := formulaServer(t, fs, &fakeGenerator{}, okVerdict)
	rec := serve(s, httptest.NewRequest(http.MethodGet, "/api/formulas", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("store-listing failure: code=%d, want 502 (honest error); body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"formulas"`) {
		t.Fatalf("listing failure must be an honest 502, not an empty formulas list: %s", rec.Body.String())
	}
}
