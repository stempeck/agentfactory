package proto

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildProto lays down a realistic prototype tree under root (mirrors the real shape:
// .designs/<id>/prototype-v{N}/ with index.html + styles/ + screens/) and returns root.
func buildProto(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".designs", "web-ui", "prototype-v1")
	mustMkdir(t, filepath.Join(dir, "styles"))
	mustMkdir(t, filepath.Join(dir, "screens"))
	mustWrite(t, filepath.Join(dir, "index.html"),
		`<!doctype html><link rel="stylesheet" href="styles/x.css"><a href="screens/y.html">y</a>INDEX_MARKER`)
	mustWrite(t, filepath.Join(dir, "styles", "x.css"), `body{color:#0ff}/*CSS_MARKER*/`)
	mustWrite(t, filepath.Join(dir, "screens", "y.html"), `<h1>SCREEN_MARKER</h1>`)
	return root
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// get drives the handler exactly as the server mounts it: under StripPrefix("/proto/").
func get(s *Server, target string) *httptest.ResponseRecorder {
	h := http.StripPrefix("/proto/", s)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	h.ServeHTTP(rec, req)
	return rec
}

func TestProto_ServesRelativeAssets_NoTraversal(t *testing.T) {
	root := buildProto(t)
	s := New(root)

	// ---- relative assets render rooted at the prototype dir ----
	positive := []struct {
		path, marker string
	}{
		{"/proto/web-ui/index.html", "INDEX_MARKER"},
		{"/proto/web-ui/", "INDEX_MARKER"}, // directory → index.html
		{"/proto/web-ui/styles/x.css", "CSS_MARKER"},
		{"/proto/web-ui/screens/y.html", "SCREEN_MARKER"},
	}
	for _, p := range positive {
		rec := get(s, p.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: code=%d, want 200", p.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), p.marker) {
			t.Fatalf("GET %s: body missing %q:\n%s", p.path, p.marker, rec.Body.String())
		}
	}

	// css carries the right content type
	if ct := get(s, "/proto/web-ui/styles/x.css").Header().Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Errorf("css content-type = %q, want text/css", ct)
	}

	// ---- traversal is REFUSED (never serves /etc/passwd, never 200) ----
	traversal := []string{
		"/proto/web-ui/../../../../etc/passwd",        // dot-dot climb
		"/proto/web-ui/..%2f..%2f..%2fetc%2fpasswd",   // url-encoded dot-dot
		"/proto/web-ui/styles/../../../../etc/passwd", // climb from a subdir
		"/proto/../web-ui/index.html",                 // climb on the id segment
		"/proto/ghost/index.html",                     // unknown / unenumerated id
	}
	for _, p := range traversal {
		rec := get(s, p)
		if rec.Code == http.StatusOK {
			t.Fatalf("traversal %q was SERVED (code 200): %s", p, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "root:") {
			t.Fatalf("served /etc/passwd for %q", p)
		}
	}
}

// TestProto_SafeJoin proves the containment helper directly, independent of mux quirks
// (security.md Decision 4/4A: filepath.Clean + prefix check).
func TestProto_SafeJoin(t *testing.T) {
	base := filepath.Join(t.TempDir(), "root")
	ok := []struct{ rel, wantSuffix string }{
		{"", ""},
		{"index.html", "index.html"},
		{"styles/x.css", filepath.Join("styles", "x.css")},
	}
	for _, c := range ok {
		got, err := safeJoin(base, c.rel)
		if err != nil {
			t.Fatalf("safeJoin(%q,%q) err=%v, want ok", base, c.rel, err)
		}
		if !strings.HasPrefix(got, base) {
			t.Fatalf("safeJoin(%q,%q)=%q escaped base", base, c.rel, got)
		}
	}
	bad := []string{"../../etc/passwd", "../secret", "styles/../../escape", "/etc/passwd"}
	for _, rel := range bad {
		if got, err := safeJoin(base, rel); err == nil {
			t.Fatalf("safeJoin(%q,%q)=%q, want error (traversal/absolute)", base, rel, got)
		}
	}
}

// TestProto_ListGraceful proves enumeration never errors when no prototypes exist, and lists
// real ones with their latest version.
func TestProto_ListGraceful(t *testing.T) {
	// empty .designs (no prototype dirs) → empty list, no panic/error
	empty := New(t.TempDir())
	if got := empty.List(); len(got) != 0 {
		t.Fatalf("List() on empty tree = %v, want empty", got)
	}

	root := buildProto(t)
	// add a second, higher version so "latest" wins
	mustMkdir(t, filepath.Join(root, ".designs", "web-ui", "prototype-v2"))
	mustWrite(t, filepath.Join(root, ".designs", "web-ui", "prototype-v2", "index.html"), "V2")
	list := New(root).List()
	if len(list) != 1 {
		t.Fatalf("List() = %d entries, want 1: %+v", len(list), list)
	}
	if list[0].ID != "web-ui" || list[0].Version != 2 {
		t.Fatalf("List()[0] = %+v, want id=web-ui version=2", list[0])
	}
}
