package worktree

import (
	"path/filepath"
	"testing"
)

// TestAbsWorktreePath pins the relocation-safety guarantee of the exported
// helper (issue #392 K1/K8): a relative meta.Path is joined to the factory
// root, while an absolute (relocated) meta.Path is returned verbatim so the
// join cannot silently corrupt it. This is the helper the watchdog now uses
// at both former relocation-unsafe join sites (Phase 4 Change 1).
func TestAbsWorktreePath(t *testing.T) {
	const factoryRoot = "/factory/root"

	t.Run("relative path joins to factory root", func(t *testing.T) {
		meta := &Meta{Path: "worktrees/wt-abc123"}
		got := AbsWorktreePath(factoryRoot, meta)
		want := filepath.Join(factoryRoot, "worktrees/wt-abc123")
		if got != want {
			t.Fatalf("AbsWorktreePath(relative) = %q, want %q", got, want)
		}
	})

	t.Run("absolute path returned verbatim, ignoring factoryRoot", func(t *testing.T) {
		const relocated = "/somewhere/else/relocated-wt"
		meta := &Meta{Path: relocated}
		got := AbsWorktreePath(factoryRoot, meta)
		if got != relocated {
			t.Fatalf("AbsWorktreePath(absolute) = %q, want %q (verbatim)", got, relocated)
		}
		// Guard against the relocation-unsafe behavior the helper replaces.
		if got == filepath.Join(factoryRoot, relocated) {
			t.Fatalf("AbsWorktreePath must NOT join an absolute meta.Path under factoryRoot")
		}
	})
}
