package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// AC-3 / Design Defect-3 (Issue #440): the active-tab highlight must be driven from the
// route in JavaScript, not frozen in hardcoded HTML. The highlight is produced by the CSS
// selector `.app .nav a[aria-current="page"]` (main.css), so the fix moves the single
// authoritative write of `aria-current` into app.js (a syncNav helper) and unifies #nav-floor
// into the data-route SPA mechanism.
//
// The web module is pure-Go with no JS/DOM runtime and no third-party deps (web/go.mod has no
// require block), so — following the source-scan precedent in
// web/internal/server/lint_test.go and the asset-co-location of web/internal/web/embed_test.go
// — this is a SOURCE-LEVEL structural assertion over the embedded static assets. It must NOT
// execute a live `af sling`/`--reset`. `go test`'s working directory is the package dir
// (web/internal/web/), so the static/ tree is a direct child.

const staticDir = "static"

// (a) #nav-floor must carry data-route="floor" so syncNav can match it AND so it joins the
// SPA click-wiring (.nav a[data-route]). Attribute order is not guaranteed, so accept the id
// and data-route in either order on the same tag.
var reNavFloorDataRoute = regexp.MustCompile(`id="nav-floor"[^>]*data-route="floor"|data-route="floor"[^>]*id="nav-floor"`)

// #nav-floor keeps an initial aria-current="page" for the pre-JS first paint (JS then syncs it).
var reNavFloorAriaCurrent = regexp.MustCompile(`id="nav-floor"[^>]*aria-current="page"|aria-current="page"[^>]*id="nav-floor"`)

// (b) app.js declares a syncNav(route) helper.
var reSyncNavDecl = regexp.MustCompile(`function\s+syncNav\s*\(`)

// ... whose body toggles aria-current (the only place aria-current is written in app.js) ...
var reSyncNavTogglesAria = regexp.MustCompile(`function\s+syncNav\s*\([^)]*\)\s*\{[\s\S]*?aria-current`)

// ... keyed on data-route.
var reSyncNavKeysDataRoute = regexp.MustCompile(`function\s+syncNav\s*\([^)]*\)\s*\{[\s\S]*?data-route`)

// (c) navigate() invokes syncNav (bound to the navigate body so a stray call elsewhere can't satisfy it).
var reNavigateCallsSyncNav = regexp.MustCompile(`navigate:\s*function\s*\([^)]*\)\s*\{[\s\S]*?syncNav\s*\(`)

// (d) boot() invokes syncNav on first paint.
var reBootCallsSyncNav = regexp.MustCompile(`function\s+boot\s*\(\s*\)\s*\{[\s\S]*?syncNav\s*\(`)

// (e) goHome() invokes syncNav — it is a second currentRoute writer (wired to the brand logo)
// that bypasses navigate(); the design intent (elevation_assessment.md A3/Candidate 3) is a
// single active-nav source of truth, so the brand-logo path must also move the highlight.
var reGoHomeCallsSyncNav = regexp.MustCompile(`goHome:\s*function\s*\([^)]*\)\s*\{[\s\S]*?syncNav\s*\(`)

func readAsset(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestNav_HighlightDrivenByRoute pins the AC-3 invariants that make the highlight follow the
// route: the JS-owned syncNav writer, its wiring into navigate()/boot()/goHome(), and the
// #nav-floor data-route. A green run here means the active-nav highlight has a single,
// JS-authored source of truth instead of a frozen HTML attribute.
func TestNav_HighlightDrivenByRoute(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))

	// (a) #nav-floor is SPA-wired and keeps its pre-JS first-paint highlight.
	if !reNavFloorDataRoute.MatchString(indexHTML) {
		t.Error(`index.html: #nav-floor must carry data-route="floor" so syncNav can match it and it joins the SPA click-wiring`)
	}
	if !reNavFloorAriaCurrent.MatchString(indexHTML) {
		t.Error(`index.html: #nav-floor must keep an initial aria-current="page" for the pre-JS first paint`)
	}

	// (b) syncNav helper exists, toggles aria-current, keyed on data-route, with set + clear paths.
	if !reSyncNavDecl.MatchString(appJS) {
		t.Error("app.js: missing syncNav(route) helper — nothing would write aria-current from the route")
	}
	if n := strings.Count(appJS, "aria-current"); n < 2 {
		t.Errorf("app.js: expected >=2 aria-current occurrences (set + clear paths), got %d — highlight is otherwise frozen", n)
	}
	if !reSyncNavTogglesAria.MatchString(appJS) {
		t.Error("app.js: syncNav body must toggle aria-current")
	}
	if !reSyncNavKeysDataRoute.MatchString(appJS) {
		t.Error("app.js: syncNav must key the highlight on data-route")
	}

	// (c)(d)(e) navigate(), boot(), and goHome() must all drive syncNav (one source of truth).
	if !reNavigateCallsSyncNav.MatchString(appJS) {
		t.Error("app.js: navigate() must call syncNav(route) so the highlight follows every route")
	}
	if !reBootCallsSyncNav.MatchString(appJS) {
		t.Error("app.js: boot() must call syncNav on first paint so JS owns the initial highlight")
	}
	if !reGoHomeCallsSyncNav.MatchString(appJS) {
		t.Error("app.js: goHome() must call syncNav('floor') so the brand-logo path also moves the highlight")
	}
}

// TestNav_HighlightDrivenByRoute_SelfNegative proves the structural regexes are not vacuous:
// each matches the intended fixed form and rejects the old frozen form. Mirrors the
// self-negative precedent in web/internal/server/lint_test.go.
func TestNav_HighlightDrivenByRoute_SelfNegative(t *testing.T) {
	oldIndexFloor := `<a id="nav-floor" href="/" aria-current="page">Floor</a>`
	newIndexFloor := `<a id="nav-floor" href="/" data-route="floor" aria-current="page">Floor</a>`

	if reNavFloorDataRoute.MatchString(oldIndexFloor) {
		t.Error("reNavFloorDataRoute false-positive on the OLD #nav-floor (no data-route)")
	}
	if !reNavFloorDataRoute.MatchString(newIndexFloor) {
		t.Error("reNavFloorDataRoute failed to match the FIXED #nav-floor")
	}
	// Robust to attribute reordering (data-route before id).
	if !reNavFloorDataRoute.MatchString(`<a data-route="floor" id="nav-floor" href="/">Floor</a>`) {
		t.Error("reNavFloorDataRoute must match regardless of id/data-route attribute order")
	}

	oldAppJS := `
  var AppViewModel = {
    currentRoute: 'floor',
    navigate: function (route) { this.currentRoute = route; if (route === 'floor') { showFloor(); return; } },
    goHome: function () { this.currentRoute = 'floor'; showFloor(); }
  };
  function boot() { wire(); FloorViewModel.refresh(); }`
	newAppJS := `
  function syncNav(route) {
    document.querySelectorAll('.nav a').forEach(function (a) {
      if (a.getAttribute('data-route') === route) a.setAttribute('aria-current', 'page');
      else a.removeAttribute('aria-current');
    });
  }
  var AppViewModel = {
    currentRoute: 'floor',
    navigate: function (route) { this.currentRoute = route; syncNav(route); if (route === 'floor') { showFloor(); return; } },
    goHome: function () { this.currentRoute = 'floor'; syncNav('floor'); showFloor(); }
  };
  function boot() { wire(); syncNav(AppViewModel.currentRoute); FloorViewModel.refresh(); }`

	for _, re := range []struct {
		name string
		re   *regexp.Regexp
	}{
		{"reSyncNavDecl", reSyncNavDecl},
		{"reSyncNavTogglesAria", reSyncNavTogglesAria},
		{"reSyncNavKeysDataRoute", reSyncNavKeysDataRoute},
		{"reNavigateCallsSyncNav", reNavigateCallsSyncNav},
		{"reBootCallsSyncNav", reBootCallsSyncNav},
		{"reGoHomeCallsSyncNav", reGoHomeCallsSyncNav},
	} {
		if re.re.MatchString(oldAppJS) {
			t.Errorf("%s false-positive on the OLD app.js (no syncNav)", re.name)
		}
		if !re.re.MatchString(newAppJS) {
			t.Errorf("%s failed to match the FIXED app.js", re.name)
		}
	}
	if strings.Count(oldAppJS, "aria-current") >= 2 {
		t.Error("fixture invalid: OLD app.js should have <2 aria-current occurrences")
	}
	if strings.Count(newAppJS, "aria-current") < 2 {
		t.Error("fixture invalid: FIXED app.js should have >=2 aria-current occurrences")
	}
}
