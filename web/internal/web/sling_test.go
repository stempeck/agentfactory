package web

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Phase 3 (Issue #440): the Sling page front end must be wired to the server-authoritative
// `Schema.Primary` contract that Phase 2 (commit ca08061) landed server-side. The web module is
// pure-Go with no JS/DOM runtime (web/go.mod has no require block), so — following the
// source-scan precedent in web/internal/web/nav_test.go and web/internal/server/lint_test.go —
// this is a SOURCE-LEVEL structural assertion over the embedded static assets. It must NOT run a
// live `af sling`/`--reset`. `readAsset` and `staticDir` are shared with nav_test.go (same pkg).
//
// These regexes pin the six front-end changes (Changes 1-6 / K3,K5,K6,K7 of design-doc.md):
//   C1 (AC-1b): primaryRequiredKey deleted; the client reads schema.primary.
//   C2 (design-v7): a synthetic task box (sentinel __task__) renders when primary=="" with a hint;
//                   the old `btn.disabled = !!primary` blank-form/enabled-button gate is gone.
//   C3 (AC-2): the POST sends the structured { task, vars } body, not the flat vars map.
//   C4 (AC-4): the comma guard + firstCommaField helper are deleted.
//   C5 (K7): sling failures render into the persistent #sling-error region.
//   C6 (K6): the sling path consults step_state and routes through ConfirmViewModel on a live step.

// C1 — the client reads the server-authoritative schema.primary.
var reReadsSchemaPrimary = regexp.MustCompile(`schema\.primary`)

// C2 — synthetic task box: the __task__ sentinel data-key and the design-v7 discoverability hint.
var reSyntheticTaskKey = regexp.MustCompile(`__task__`)
var reSyntheticHint = regexp.MustCompile(`takes its issue from the task`)

// C3 — the structured {task, vars} POST: `'/sling', { task: …`.
var reStructuredSlingPost = regexp.MustCompile(`/sling'\s*,\s*\{\s*task:`)

// C3 (negative) — the old flat-vars POST `'/sling', vars)` must be gone.
var reFlatSlingPost = regexp.MustCompile(`/sling'\s*,\s*vars\s*\)`)

// C5 — the persistent inline error region is written from the sling failure path.
var reWritesSlingError = regexp.MustCompile(`sling-error`)

// C6 — the reset blast-radius guard consults the live formula step_state.
var reConsultsStepState = regexp.MustCompile(`step_state`)

// index.html — the persistent error region exists and reuses the `.validation` danger class
// (there is no `.error` CSS class). Accept class/id in either attribute order.
var reSlingErrorRegion = regexp.MustCompile(`class="validation"[^>]*id="sling-error"|id="sling-error"[^>]*class="validation"`)

// TestSling_WiredToPrimaryContract pins the Phase-3 contract changes. A green run means app.js
// reads schema.primary, always renders a (synthetic-capable) task box, POSTs {task,vars}, has no
// comma guard, surfaces failures persistently, and guards a live formula step before re-slinging.
func TestSling_WiredToPrimaryContract(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))

	// C1 — primaryRequiredKey removed (AC-1); schema.primary read instead.
	if n := strings.Count(appJS, "primaryRequiredKey"); n != 0 {
		t.Errorf("app.js: primaryRequiredKey must be deleted (read schema.primary instead), found %d occurrence(s)", n)
	}
	if !reReadsSchemaPrimary.MatchString(appJS) {
		t.Error("app.js: the client must read the server-authoritative schema.primary")
	}

	// C2 — synthetic task box + gated button; the old blank-form/enabled-button gate is gone.
	if !reSyntheticTaskKey.MatchString(appJS) {
		t.Error("app.js: a synthetic task box (sentinel __task__) must render when schema.primary is blank (design-v7)")
	}
	if !reSyntheticHint.MatchString(appJS) {
		t.Error("app.js: the synthetic task box must carry the discoverability hint about taking its issue from the pasted task")
	}
	if n := strings.Count(appJS, "!!primary"); n != 0 {
		t.Errorf("app.js: the `btn.disabled = !!primary` gate must be removed (it enables the blank design-v7 form), found %d occurrence(s)", n)
	}

	// C3 — structured {task,vars} POST; no flat-vars POST remains (AC-2/AC-4 wire change).
	if !reStructuredSlingPost.MatchString(appJS) {
		t.Error(`app.js: the sling POST must send the structured { task, vars } body (the Phase-2 server decodes a flat body to task="" silently)`)
	}
	if reFlatSlingPost.MatchString(appJS) {
		t.Error(`app.js: the old flat-vars POST ('/sling', vars) must be gone`)
	}

	// C4 — comma guard + firstCommaField deleted (AC-4).
	if n := strings.Count(appJS, "firstCommaField"); n != 0 {
		t.Errorf("app.js: firstCommaField must be deleted (--var is comma-safe; the task is positional), found %d occurrence(s)", n)
	}
	if n := strings.Count(appJS, "Comma footgun"); n != 0 {
		t.Errorf("app.js: the stale comma-guard block must be deleted, found %d occurrence(s) of its comment", n)
	}

	// C5 — persistent inline error region exists (index.html) and is written by app.js (K7).
	if c := strings.Count(indexHTML, `id="sling-error"`); c != 1 {
		t.Errorf(`index.html: exactly one persistent error region id="sling-error" expected, got %d`, c)
	}
	if !reSlingErrorRegion.MatchString(indexHTML) {
		t.Error(`index.html: #sling-error must reuse the .validation danger class (there is no .error class)`)
	}
	if !reWritesSlingError.MatchString(appJS) {
		t.Error("app.js: a sling failure must render env.message into the persistent #sling-error region, not only a toast")
	}

	// C6 — the reset blast-radius guard consults step_state and routes through ConfirmViewModel (K6).
	if !reConsultsStepState.MatchString(appJS) {
		t.Error("app.js: the sling path must consult the selected agent's step_state to detect a live formula step")
	}
	if n := strings.Count(appJS, "ConfirmViewModel.request"); n < 3 {
		t.Errorf("app.js: the sling path must add a ConfirmViewModel.request guard (expected >=3 callers incl. factory+agent reset), got %d", n)
	}
}

// TestSling_WiredToPrimaryContract_SelfNegative proves the structural regexes are not vacuous:
// each matches the intended FIXED form and rejects the OLD pre-Phase-3 form. Mirrors the
// self-negative precedent in nav_test.go / web/internal/server/lint_test.go.
func TestSling_WiredToPrimaryContract_SelfNegative(t *testing.T) {
	oldAppJS := `
  function primaryRequiredKey(schema) { return ''; }
  var SlingViewModel = {
    sling: function () {
      var primary = primaryRequiredKey(this.schema);
      var vars = collectFormValues(this.schema);
      // Comma footgun: af's --var is a comma-splitting StringSliceVar...
      var bad = firstCommaField(vars);
      return API.post('/api/agents/' + encodeURIComponent(name) + '/sling', vars).then(function (env) {
        toast((env && env.message) || 'sling failed');
      });
    }
  };
  btn.disabled = !!primary;`
	newAppJS := `
  var SlingViewModel = {
    sling: function () {
      var primary = this.schema.primary || '';
      var taskKey = primary || '__task__';
      var task = (byId('sling-field-' + taskKey) || {}).value;
      var vars = collectFormValues(this.schema);
      if (primary) delete vars[primary];
      var sel = this._agentByName(name);
      if (sel && sel.step_id && !isTerminalStep(sel.step_state)) {
        ConfirmViewModel.request(name, 'agent', dispatch); return;
      }
      return API.post('/api/agents/' + encodeURIComponent(name) + '/sling', { task: task, vars: vars });
    }
  };
  function taskBox(f) { /* hint: takes its issue from the task you paste here */ }
  function showSlingError(m) { byId('sling-error').textContent = m; }
  // plus two pre-existing callers:
  ConfirmViewModel.request('the entire factory', 'factory', f);
  ConfirmViewModel.request(name, 'agent', f);`

	// Positive regexes: match the FIXED form, reject the OLD form.
	for _, c := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"reReadsSchemaPrimary", reReadsSchemaPrimary},
		{"reSyntheticTaskKey", reSyntheticTaskKey},
		{"reSyntheticHint", reSyntheticHint},
		{"reStructuredSlingPost", reStructuredSlingPost},
		{"reWritesSlingError", reWritesSlingError},
		{"reConsultsStepState", reConsultsStepState},
	} {
		if c.re.MatchString(oldAppJS) {
			t.Errorf("%s false-positive on the OLD app.js", c.name)
		}
		if !c.re.MatchString(newAppJS) {
			t.Errorf("%s failed to match the FIXED app.js", c.name)
		}
	}

	// Negative regex: the flat POST is present in OLD, absent in FIXED.
	if !reFlatSlingPost.MatchString(oldAppJS) {
		t.Error("reFlatSlingPost should match the OLD flat-vars POST")
	}
	if reFlatSlingPost.MatchString(newAppJS) {
		t.Error("reFlatSlingPost false-positive on the FIXED structured POST")
	}

	// Count-based guards: present-then-removed substrings.
	if strings.Count(oldAppJS, "primaryRequiredKey") == 0 {
		t.Error("fixture invalid: OLD app.js should contain primaryRequiredKey")
	}
	if strings.Count(newAppJS, "primaryRequiredKey") != 0 {
		t.Error("fixture invalid: FIXED app.js should not contain primaryRequiredKey")
	}
	if strings.Count(oldAppJS, "firstCommaField") == 0 {
		t.Error("fixture invalid: OLD app.js should contain firstCommaField")
	}
	if strings.Count(newAppJS, "firstCommaField") != 0 {
		t.Error("fixture invalid: FIXED app.js should not contain firstCommaField")
	}
	if strings.Count(oldAppJS, "!!primary") == 0 {
		t.Error("fixture invalid: OLD app.js should contain the !!primary gate")
	}
	if strings.Count(newAppJS, "!!primary") != 0 {
		t.Error("fixture invalid: FIXED app.js should not contain the !!primary gate")
	}
	if n := strings.Count(newAppJS, "ConfirmViewModel.request"); n < 3 {
		t.Errorf("fixture invalid: FIXED app.js should have >=3 ConfirmViewModel.request callers, got %d", n)
	}
}
