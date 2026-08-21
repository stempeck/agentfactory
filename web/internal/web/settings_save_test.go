package web

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ---- #620 Phase 3 · AC-5: the settings save is per-panel and fail-fast ----
//
// The defect this pins shut: `save()` used to PUT dispatch and then, in an unconditional `.then`,
// PUT startup — so a rejected first write did not stop the second (app.js:852-856 before this
// phase). The fix is not a guard on the second write; it is that there IS no second write. One
// function, one file, so "no second PUT after a failure" is true by construction rather than by a
// branch a future edit can invert.
//
// WHY THIS IS A SOURCE SCAN AND NOT A BEHAVIOURAL DRIVE. `web/go.mod` has no require block at all,
// so this module has no JS runtime to execute app.js against a stubbed fetch (that is exactly the
// hole telemetry_test.go:35-42 records). The `web-unit` CI lane also bakes no node, so a Go test
// that shelled out to node would t.Skip in CI — vacuous coverage, which this package explicitly
// rejects (settings_canary_test.go). So the invariant is asserted structurally here, in the
// nav_test.go / sling_test.go idiom, and the EXECUTABLE proof over a scripted API double lives in
// web/conformance/test-settings.js, which `make conformance` and CI's toml-conformance job both
// execute — so the two halves of this invariant fail in different lanes rather than one of them
// failing nowhere.

// The settings write route. Whatever helper carries the CAS precondition, it must name this path.
var reSettingsWriteRoute = regexp.MustCompile(`/api/settings/`)

// The success gate the re-read must sit behind.
const settingsOkGate = "if (env && env.ok)"

// The lossy per-row rebuild Phase 3 deletes (AC 3.1). Kept here as a permanent tripwire: the row
// merge is only lossless while nothing reconstructs a mapping from form state.
const settingsRowRebuild = "labels: [label], agent: agent"

// TestSettingsWrite_NoPartialMultiFileWrite asserts the per-panel save writes exactly one file and
// re-reads only on success.
func TestSettingsWrite_NoPartialMultiFileWrite(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	body := funcBody(appJS, "saveSettingsFile(")
	if body == "" {
		t.Fatal("app.js: function saveSettingsFile(file) not found — the per-file save entry point is the unit AC-5 is asserted over")
	}

	assertSingleFileWrite(t, "saveSettingsFile", body)

	// The old chain named its two files literally. Building the path from the panel's own file key is
	// what makes a second noun unreachable from this function.
	for _, lit := range []string{"/api/settings/dispatch", "/api/settings/startup"} {
		if strings.Contains(appJS, lit) {
			t.Errorf("app.js: hardcoded settings route %q is the chained-save shape; the per-panel save must build one path from its file key", lit)
		}
	}

	// AC 3.1 — the per-row rebuild is gone, and no equivalent literal took its place.
	if strings.Contains(appJS, settingsRowRebuild) {
		t.Errorf("app.js: the per-row rebuild %q is still present; a mapping row must be MERGED onto the raw row object it was read with, never reconstructed", settingsRowRebuild)
	}

	// The merge's sharp edge: emitting `labels` must delete a lone `label`, or af-core rejects the
	// mapping as ambiguous (internal/config/dispatch.go:170-172).
	collect := funcBody(appJS, "collectMappings(")
	if collect == "" {
		t.Fatal("app.js: function collectMappings() not found")
	}
	if !strings.Contains(collect, "delete ") || !strings.Contains(collect, ".label") {
		t.Error("app.js: collectMappings must delete a lone `label` when it emits `labels` — af-core rejects a mapping carrying both")
	}
	if strings.Contains(collect, "out.push({") {
		t.Error("app.js: collectMappings pushes a fresh object literal — that is the rebuild this phase removes; push the retained row object")
	}
}

// TestStaticAssets_ContainNoNULBytes keeps the shipped assets TEXT.
//
// This is not hygiene. A single NUL makes the file binary to grep, and every acceptance criterion for
// this view is a grep: `grep -c` prints nothing and exits 1 on a binary file, so `test "$(…)" -eq 0`
// dies with "integer expression expected" and `grep -rn … | wc -l` reports 0 — a criterion that reads
// as FAILING when the content is present, or as PASSING when it is absent, depending on which way the
// check is written. Neither answer is about the code.
//
// It also hides itself: git's binary heuristic only sniffs the first 8000 bytes, so `git diff`,
// `git grep` and every review tool render a NUL as an ordinary character. This phase shipped two of
// them inside a string literal that was load-bearing for correctness, and three readings of the diff
// did not show it. Only `cat -A` did.
func TestStaticAssets_ContainNoNULBytes(t *testing.T) {
	for _, name := range []string{"app.js", "index.html", filepath.Join("styles", "main.css")} {
		src := readAsset(t, filepath.Join(staticDir, name))
		if at := strings.IndexByte(src, 0); at >= 0 {
			line := 1 + strings.Count(src[:at], "\n")
			t.Errorf("%s:%d: NUL byte at offset %d — the file is binary to grep, and every acceptance "+
				"criterion for this view is a grep", name, line, at)
		}
	}
}

// reTopLevelDecl matches a declaration at the IIFE's own indent level — the shared scope every helper
// in app.js lands in.
var reTopLevelDecl = regexp.MustCompile(`(?m)^  (?:function ([A-Za-z0-9_$]+)\s*\(|var ([A-Za-z0-9_$]+)\s*=)`)

// TestStaticApp_NoShadowedTopLevelDeclarations keeps every name in the IIFE unique.
//
// app.js is one function scope holding ~150 declarations. A duplicate `function` declaration is not
// an error in JS — the later one silently replaces the earlier — so the failure is invisible at
// runtime AND worse in the test suite than in the browser: web/conformance/test-settings.js extracts
// functions by name and takes the FIRST match, so it would drive the dead copy to green while the
// console ran the live one. Every behavioural assertion about that function would be vacuous, and
// nothing would say so.
func TestStaticApp_NoShadowedTopLevelDeclarations(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	seen := map[string]int{}
	for _, m := range reTopLevelDecl.FindAllStringSubmatch(appJS, -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		seen[name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("app.js: %q is declared %d times at the IIFE's top level — the later declaration "+
				"silently replaces the earlier one, and the conformance harness extracts the first", name, n)
		}
	}
	if len(seen) == 0 {
		t.Fatal("app.js: no top-level declarations matched — the scan is looking at the wrong shape")
	}
}

// errReporter is the narrow slice of *testing.T the predicate needs, so the self-negative can run
// the SAME predicate against a fixture and observe whether it fired.
type errReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// assertSingleFileWrite is the predicate both the shipped-source check and the self-negative run, so
// the two cannot drift.
func assertSingleFileWrite(t errReporter, name, body string) {
	t.Helper()

	if n := len(reSettingsWriteRoute.FindAllString(body, -1)); n != 1 {
		t.Errorf("%s: %d settings write call sites; exactly 1 is required — a second PUT reachable from one save body is the partial-write defect (AC-5)", name, n)
	}

	reread := strings.Count(body, ".load()")
	if reread != 1 {
		t.Errorf("%s: %d re-read call sites; exactly 1 is required", name, reread)
	}
	gate := strings.Index(body, settingsOkGate)
	if gate < 0 {
		t.Errorf("%s: no %q success gate — the failure arms are distinguishable only through the resolved envelope", name, settingsOkGate)
		return
	}
	if at := strings.Index(body, ".load()"); at >= 0 && at < gate {
		t.Errorf("%s: the re-read is reachable before the %q gate — it must run only after a 2xx", name, settingsOkGate)
	}
}

// TestSettingsWrite_NoPartialMultiFileWrite_SelfNegative proves the predicates above can actually
// fail. Without it a structurally perfect check that is never true would read as coverage.
func TestSettingsWrite_NoPartialMultiFileWrite_SelfNegative(t *testing.T) {
	// The pre-Phase-3 save(), verbatim in shape: two writes, and a re-read in an unconditional tail.
	old := `{
      return API.put('/api/settings/dispatch', disp).then(function (env) {
        if (env && env.ok) { show('set-dispatch-ok'); }
        else { showValidation('set-dispatch-err', (env && env.message) || 'dispatch save failed'); }
        return API.put('/api/settings/startup', st);
      }).then(function (env) {
        return self.load();
      });
    }`

	fixed := `{
      return API.put('/api/settings/' + file, payload, headers).then(function (env) {
        if (env && env.ok) { show('set-' + file + '-ok'); return self.load(); }
        showSettingsError(file, settingsFailureCopy(file, env));
        return null;
      });
    }`

	var got []string
	rec := &recordingT{T: t, out: &got}
	assertSingleFileWrite(rec, "old", old)
	if len(got) == 0 {
		t.Error("the single-file-write predicates did not fire on the pre-Phase-3 chained save — they cannot detect the defect they exist for")
	}

	got = nil
	rec2 := &recordingT{T: t, out: &got}
	assertSingleFileWrite(rec2, "fixed", fixed)
	if len(got) != 0 {
		t.Errorf("the predicates fired on a correct per-file save: %v", got)
	}

	// The row-rebuild tripwire must also be able to see its target.
	if !strings.Contains("out.push({ "+settingsRowRebuild+" });", settingsRowRebuild) {
		t.Error("the row-rebuild literal no longer matches the shape it was written to catch")
	}
}

// recordingT captures Errorf/Error instead of failing, so the self-negative can assert that a
// predicate FIRED. Fatal-free by construction: assertSingleFileWrite never calls Fatal.
type recordingT struct {
	*testing.T
	out *[]string
}

func (r *recordingT) Errorf(format string, args ...any) { *r.out = append(*r.out, format) }
func (r *recordingT) Helper()                           {}
