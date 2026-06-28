// Package proto is the C7 static / prototype server for the web module.
//
// It serves agent-produced HTML prototypes that live ON DISK under the factory root at
// .designs/<id>/prototype-v{N}/ (NOT the embedded Floor tree — that is web/internal/web/embed.go,
// served from a go:embed FS at "/"). Because prototypes are on-disk and operator-reachable, this
// handler is traversal-contained on two independent axes (security.md Decision 4 / 4A):
//
//   - the servable set is ENUMERATED server-side by id; the client never supplies a raw filesystem
//     path, only an enumerated id (validID) plus a relative asset path, and
//   - every asset path is resolved through safeJoin, which rejects absolute paths and any ".."
//     escape with an explicit filepath.Clean + prefix check (belt) on top of Go's own path cleaning
//     (suspenders).
//
// The frontend renders the served prototype inside a sandboxed <iframe> — a third, independent
// layer (ux.md Decision 5A). Defense in depth: a containment bug here is still caged by the iframe.
//
// Stdlib only (the web module has its own go.mod and pulls in zero third-party deps) and read-only:
// this package never execs anything, so it trivially satisfies the module's exec-safety lint.
package proto

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Prototype is one enumerated, servable prototype directory.
type Prototype struct {
	ID      string `json:"id"`      // the .designs/<id> directory name (enumerated; never a raw path)
	Version int    `json:"version"` // the latest prototype-v{N} present on disk
	Path    string `json:"path"`    // display path, e.g. "web-ui/prototype-v1"
	// FeedbackOpen is annotated by the server handler from the C6 gate check (false here).
	FeedbackOpen bool `json:"feedback_open"`
}

// Server serves prototypes rooted at <root>/.designs/<id>/prototype-v{N}/.
type Server struct {
	root string // factory root (the directory that contains .designs/)
}

// New builds a Server rooted at the factory root (mirrors dispatch.New / config.New).
func New(root string) *Server { return &Server{root: root} }

// validID constrains an id to a single safe path segment — no separators, no "..", no leading dot.
// Combined with safeJoin this makes a client-supplied id incapable of escaping .designs/.
var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

var protoVersionRE = regexp.MustCompile(`^prototype-v([0-9]+)$`)

// designsDir is the on-disk root of all design artifacts.
func (s *Server) designsDir() string { return filepath.Join(s.root, ".designs") }

// List enumerates every id under .designs/ that has at least one servable prototype-v{N}/index.html,
// reporting the latest version. It is graceful: a missing .designs/ (the design agents have not run
// yet) yields an empty list, never an error.
func (s *Server) List() []Prototype {
	out := []Prototype{}
	entries, err := os.ReadDir(s.designsDir())
	if err != nil {
		return out // no .designs/ yet → friendly empty state, never error
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if !validID.MatchString(id) {
			continue
		}
		n := latestServableVersion(filepath.Join(s.designsDir(), id))
		if n == 0 {
			continue
		}
		out = append(out, Prototype{ID: id, Version: n, Path: fmt.Sprintf("%s/prototype-v%d", id, n)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// latestServableVersion returns the highest N for which base/prototype-v{N}/index.html exists,
// or 0 when none is present.
func latestServableVersion(base string) int {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	best := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := protoVersionRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= best {
			continue
		}
		if fi, err := os.Stat(filepath.Join(base, e.Name(), "index.html")); err == nil && !fi.IsDir() {
			best = n
		}
	}
	return best
}

// activeDir resolves the directory of an id's latest servable prototype version. ok is false for an
// unknown / invalid id or one with no servable prototype.
func (s *Server) activeDir(id string) (string, bool) {
	if !validID.MatchString(id) {
		return "", false
	}
	base := filepath.Join(s.designsDir(), id)
	n := latestServableVersion(base)
	if n == 0 {
		return "", false
	}
	return filepath.Join(base, fmt.Sprintf("prototype-v%d", n)), true
}

// ServeHTTP serves GET /proto/<id>/<asset...>. It expects the "/proto/" prefix to have already been
// stripped by the mount (http.StripPrefix in the server's routes), so r.URL.Path is "<id>/<asset>".
// It is read-only and traversal-contained: unknown id ⇒ 404, escape attempt ⇒ 400/404, never serves
// anything outside the prototype's own directory.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, asset, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	dir, ok := s.activeDir(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if asset == "" {
		asset = "index.html"
	}
	// Containment is two independent layers (security.md Decision 4 / 4A — defense in depth):
	//   Layer 1 — safeJoin: an explicit filepath.Clean + filepath.Rel prefix check that rejects any
	//             absolute path or ".." escape BEFORE the filesystem is touched.
	if _, err := safeJoin(dir, asset); err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	//   Layer 2 — http.Dir: its Open is independently rooted at the prototype dir and also blocks
	//             "..". We open through it (rather than os.Open) so a containment regression in one
	//             layer is still caught by the other. ServeContent (not http.FileServer) is used to
	//             avoid FileServer's "/index.html → ./" redirect while still setting Content-Type and
	//             honouring conditional/range requests.
	fsys := http.Dir(dir)
	f, err := fsys.Open("/" + asset)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if fi.IsDir() {
		// a sub-directory request → serve its index.html.
		nf, err := fsys.Open("/" + strings.TrimSuffix(asset, "/") + "/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer nf.Close()
		nfi, err := nf.Stat()
		if err != nil || nfi.IsDir() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, nfi.Name(), nfi.ModTime(), nf)
		return
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// safeJoin joins a relative asset path onto base, guaranteeing the result stays within base. It is a
// package-level function (like exec.ValidateAgentName / afArgv) so the containment contract is
// directly unit-testable. It rejects absolute paths and any ".." segment, then re-checks containment
// with filepath.Rel as a final belt (security.md Decision 4 / 4A).
func safeJoin(base, rel string) (string, error) {
	if rel == "" {
		return base, nil
	}
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path not allowed: %q", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path traversal not allowed: %q", rel)
		}
	}
	full := filepath.Join(base, filepath.FromSlash(rel))
	r, err := filepath.Rel(base, full)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %q", rel)
	}
	return full, nil
}
