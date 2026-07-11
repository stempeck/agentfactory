package cmd

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// nestedFixture is the shared nested-factory-clone fixture for the #519 Phase-2
// seam tests (T-INT-2/3/5). All paths are EvalSymlinks-canonical so assertions can
// compare against config.FindFactoryRoot's raw return without symlink drift on the
// container's symlinked /tmp.
type nestedFixture struct {
	outer      string // outer factory root (holds .agentfactory/factory.json + agents.json)
	clone      string // nested clone factory root, deep inside outer (its own factory.json)
	worktree   string // worktree agent dir under outer, redirecting to outer via .factory-root
	markerless string // a temp dir with no .agentfactory marker at all
}

// buildNestedFactoryFixture materializes the scale.md fixture:
//
//	outer/                                         (factory.json + agents.json)
//	  .agentfactory/worktrees/wt-x/                (.factory-root -> outer)
//	  .agentfactory/agents/a/tmp/clone/            (factory.json + agents.json — the nested clone)
func buildNestedFactoryFixture(t *testing.T) nestedFixture {
	t.Helper()

	realBase, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks base: %v", err)
	}
	outer := filepath.Join(realBase, "outer")
	writeFactoryMarker(t, outer)
	writeAgentsJSON(t, outer, `{"agents":{"a":{"type":"autonomous","description":"d"}}}`)

	clone := filepath.Join(outer, ".agentfactory", "agents", "a", "tmp", "clone")
	writeFactoryMarker(t, clone)
	writeAgentsJSON(t, clone, `{"agents":{"a":{"type":"autonomous","description":"d"}}}`)

	worktree := filepath.Join(outer, ".agentfactory", "worktrees", "wt-x")
	wtAF := filepath.Join(worktree, ".agentfactory")
	if err := os.MkdirAll(wtAF, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtAF, ".factory-root"), []byte(outer+"\n"), 0o644); err != nil {
		t.Fatalf("write .factory-root: %v", err)
	}

	markerless, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks markerless: %v", err)
	}

	return nestedFixture{outer: outer, clone: clone, worktree: worktree, markerless: markerless}
}

// writeFactoryMarker drops <dir>/.agentfactory/factory.json + the store dir.
func writeFactoryMarker(t *testing.T, dir string) {
	t.Helper()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1}`+"\n"), 0o644); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	if err := os.MkdirAll(config.StoreDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
}

// captureStderr redirects os.Stderr through a pipe while fn runs and returns
// everything written. resolveInvokerRoot's invalid-env warning is written directly
// to os.Stderr (not the cobra seam), so degraded-loud assertions need this.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = orig
	return <-done
}

// TestT_INT_2_ResolveInvokerRoot is the scale.md 10-row unit table for the
// resolveInvokerRoot seam (#519 Phase 2). Verb-level rows (install --init) live in
// T-INT-5; this table pins the resolver's own match/mismatch/unset/invalid/symlink
// semantics.
func TestT_INT_2_ResolveInvokerRoot(t *testing.T) {
	fx := buildNestedFactoryFixture(t)

	t.Run("clone_interior/AF_ROOT=outer => refuse naming both roots", func(t *testing.T) {
		t.Setenv("AF_ROOT", fx.outer)
		got, err := resolveInvokerRoot(fx.clone)
		if err == nil {
			t.Fatalf("expected mismatch error, got root %q", got)
		}
		var mm *rootMismatchError
		if !errors.As(err, &mm) {
			t.Fatalf("expected *rootMismatchError, got %T: %v", err, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "factory root mismatch") {
			t.Errorf("error missing head %q: %s", "factory root mismatch", msg)
		}
		if !strings.Contains(msg, fx.clone) || !strings.Contains(msg, fx.outer) {
			t.Errorf("error must name BOTH roots (clone %q, outer %q): %s", fx.clone, fx.outer, msg)
		}
		if mm.resolved != fx.clone {
			t.Errorf("mismatch.resolved = %q, want clone %q (needed by warn-and-proceed sites)", mm.resolved, fx.clone)
		}
	})

	t.Run("clone_interior/AF_ROOT unset => REFUSE (enclosing)", func(t *testing.T) {
		// K5 (#519 Phase 3): an env-less shell inside the nested clone can no longer
		// silently capture a state-writing verb — the enclosing scan on the resolved
		// clone finds outer and refuses. (Pre-K5 this row resolved the clone silently.)
		got, err := resolveInvokerRoot(fx.clone)
		if err == nil {
			t.Fatalf("expected enclosing refusal, got root %q", got)
		}
		var enc *enclosingRootError
		if !errors.As(err, &enc) {
			t.Fatalf("expected *enclosingRootError, got %T: %v", err, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, fx.clone) || !strings.Contains(msg, fx.outer) {
			t.Errorf("error must name BOTH roots (clone %q, outer %q): %s", fx.clone, fx.outer, msg)
		}
		if !strings.Contains(msg, "set AF_ROOT="+fx.clone) {
			t.Errorf("error must offer the affirm hatch 'set AF_ROOT=%s': %s", fx.clone, msg)
		}
		if enc.resolved != fx.clone {
			t.Errorf("enc.resolved = %q, want clone %q (needed by warn-and-proceed sites)", enc.resolved, fx.clone)
		}
	})

	t.Run("worktree_agent_dir/AF_ROOT=outer => resolves outer via redirect, no enclosing warning", func(t *testing.T) {
		t.Setenv("AF_ROOT", fx.outer)
		var got string
		var err error
		stderr := captureStderr(t, func() { got, err = resolveInvokerRoot(fx.worktree) })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != fx.outer {
			t.Errorf("got %q, want outer %q (redirect resolves cwd to outer, matches AF_ROOT)", got, fx.outer)
		}
		// Dedupe-by-resolved-root: the redirect + outer factory resolve to the same
		// root, so normal worktree geometry must stay quiet.
		if strings.Contains(stderr, "nested inside") {
			t.Errorf("worktree geometry must not fire an enclosing warning, got: %q", stderr)
		}
	})

	t.Run("outer_root/AF_ROOT=outer => resolves outer", func(t *testing.T) {
		t.Setenv("AF_ROOT", fx.outer)
		got, err := resolveInvokerRoot(fx.outer)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != fx.outer {
			t.Errorf("got %q, want outer %q", got, fx.outer)
		}
	})

	t.Run("outer_root/AF_ROOT unset => resolves outer", func(t *testing.T) {
		got, err := resolveInvokerRoot(fx.outer)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != fx.outer {
			t.Errorf("got %q, want outer %q", got, fx.outer)
		}
	})

	t.Run("marker-less_dir/AF_ROOT unset => verbatim not-found", func(t *testing.T) {
		_, err := resolveInvokerRoot(fx.markerless)
		if err == nil {
			t.Fatal("expected not-found error, got nil")
		}
		if !strings.Contains(err.Error(), "not in an agentfactory workspace") {
			t.Errorf("not-found error must propagate verbatim (root.go:36), got: %v", err)
		}
		var mm *rootMismatchError
		if errors.As(err, &mm) {
			t.Errorf("not-found must NOT be a rootMismatchError")
		}
	})

	t.Run("clone_interior/AF_ROOT=clone => WARN (enclosing) but proceeds", func(t *testing.T) {
		// H3 (#519 Phase 3): the enclosing scan runs UNCONDITIONALLY, including on the
		// env-set-matching path. A clone-born gen-0 session (AF_ROOT baked to the clone,
		// cross-check passes) still sees the enclosing warning on every state-writing verb.
		t.Setenv("AF_ROOT", fx.clone)
		var got string
		var err error
		stderr := captureStderr(t, func() { got, err = resolveInvokerRoot(fx.clone) })
		if err != nil {
			t.Fatalf("gen-0 clone-born session must warn-and-proceed, got error: %v", err)
		}
		if got != fx.clone {
			t.Errorf("got %q, want clone %q (clone-born session is internally consistent)", got, fx.clone)
		}
		if !strings.Contains(stderr, fx.outer) || !strings.Contains(stderr, "nested inside") {
			t.Errorf("expected enclosing warning naming outer %q on stderr, got: %q", fx.outer, stderr)
		}
	})

	t.Run("symlinked_outer/AF_ROOT=real outer => EvalSymlinks equates, proceeds", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "outer-link")
		if err := os.Symlink(fx.outer, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		t.Setenv("AF_ROOT", fx.outer)
		got, err := resolveInvokerRoot(link)
		if err != nil {
			t.Fatalf("unexpected error (EvalSymlinks should equate link to real outer): %v", err)
		}
		if got != link {
			t.Errorf("got %q, want the cwd-resolved symlink path %q", got, link)
		}
	})

	t.Run("clone_interior/AF_ROOT=invalid => warn and resolve clone", func(t *testing.T) {
		bogus := filepath.Join(fx.markerless, "nope", "not-a-factory")
		t.Setenv("AF_ROOT", bogus)
		var got string
		var err error
		stderr := captureStderr(t, func() { got, err = resolveInvokerRoot(fx.clone) })
		if err != nil {
			t.Fatalf("invalid AF_ROOT must warn-and-proceed, got error: %v", err)
		}
		if got != fx.clone {
			t.Errorf("got %q, want clone %q (degraded-loud fall-through)", got, fx.clone)
		}
		if !strings.Contains(stderr, "does not resolve to a factory") {
			t.Errorf("expected invalid-env warning on stderr, got: %q", stderr)
		}
	})
}
