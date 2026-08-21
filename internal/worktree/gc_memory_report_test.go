package worktree

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/memory"
)

// T-REPORT, GC row (AC-626-6). GC is the one teardown path the design downgrades to best-effort:
// it runs inside a stranger's ResolveOrCreate with both return values discarded, so the package
// stderr writer is the only stream it has. These tests hold that it speaks on that stream and
// that speaking never changes what GC does.

func gcFixture(t *testing.T, agent string, noteCount int) (root, wtID string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if err := os.MkdirAll(WorktreesDir(root), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A per-test owner name keeps `tmux has-session -t =af-<owner>` failing, which is what sends
	// GC past the liveness check into the reap branch.
	wtID = "wt-gcmem01"
	meta := &Meta{
		ID:     wtID,
		Owner:  agent,
		Branch: "af/" + agent + "-gcmem01",
		Path:   filepath.Join(".agentfactory", "worktrees", wtID),
		Agents: []string{agent},
	}
	if err := WriteMeta(root, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	for i := 0; i < noteCount; i++ {
		if _, err := memory.Write(root, memory.Note{
			ID:      "gc-note-" + string(rune('a'+i)),
			Agent:   agent,
			Status:  memory.StatusActive,
			Created: time.Now().UTC(),
			Body:    "a learning worth keeping\n",
		}); err != nil {
			t.Fatalf("seeding note: %v", err)
		}
	}
	return root, wtID
}

func captureStderrWriter(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := stderrWriter
	stderrWriter = &buf
	t.Cleanup(func() { stderrWriter = orig })
	return &buf
}

func TestGC_ReportsPreservedMemoryBestEffort(t *testing.T) {
	root, _ := gcFixture(t, "gcsolver", 2)
	buf := captureStderrWriter(t)

	if _, err := GC(root); err != nil {
		t.Fatalf("GC: %v", err)
	}

	out := buf.String()
	want := "memory preserved: " + filepath.Join(".agentfactory", "memory", "gcsolver") + string(filepath.Separator) + " (2 notes)"
	if !strings.Contains(out, want) {
		t.Errorf("GC stderr missing %q, got:\n%s", want, out)
	}
}

func TestGC_SuppressesPreservedMemoryAtZeroNotes(t *testing.T) {
	root, _ := gcFixture(t, "gcsolver", 0)
	buf := captureStderrWriter(t)

	if _, err := GC(root); err != nil {
		t.Fatalf("GC: %v", err)
	}

	if out := buf.String(); strings.Contains(out, "memory preserved:") {
		t.Errorf("GC must stay silent when no note was preserved, got:\n%s", out)
	}
}

// A worktree GC declines to reap says nothing about a vault, because nothing was torn down.
func TestGC_SilentAboutMemoryWhenItReapsNothing(t *testing.T) {
	root, wtID := gcFixture(t, "gcsolver", 2)
	hookedDir := filepath.Join(root, ".agentfactory", "worktrees", wtID,
		".agentfactory", "agents", "gcsolver", ".runtime")
	if err := os.MkdirAll(hookedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hookedDir, "hooked_formula"), []byte("some-formula"), 0o644); err != nil {
		t.Fatalf("writing hooked_formula: %v", err)
	}
	buf := captureStderrWriter(t)

	if _, err := GC(root); err != nil {
		t.Fatalf("GC: %v", err)
	}

	if out := buf.String(); strings.Contains(out, "memory preserved:") {
		t.Errorf("GC must not claim preservation for a worktree it kept, got:\n%s", out)
	}
}
