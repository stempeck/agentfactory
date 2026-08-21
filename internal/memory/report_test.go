package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// PreservedLine is the one authored copy of the AC-626-6 teardown report line. Four call sites
// across two packages render it (done/down/reset in internal/cmd, GC in internal/worktree), so
// these tests pin the string shape here rather than four times over.

func seedVaultNote(t *testing.T, root, agent, id string) {
	t.Helper()
	if _, err := Write(root, Note{
		ID:      id,
		Agent:   agent,
		Status:  StatusActive,
		Created: time.Now().UTC(),
		Body:    "a learning worth keeping\n",
	}); err != nil {
		t.Fatalf("seeding note %q: %v", id, err)
	}
}

func TestPreservedLine_ReportsCountAndVaultPath(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"n1", "n2", "n3"} {
		seedVaultNote(t, root, "solver", id)
	}

	got := PreservedLine(root, "solver")

	want := "memory preserved: " + filepath.Join(".agentfactory", "memory", "solver") + string(filepath.Separator) + " (3 notes)"
	if got != want {
		t.Errorf("PreservedLine = %q, want %q", got, want)
	}
}

// The path is reported relative to the factory root because that is the form the operator reads
// in every other teardown line; an absolute temp path would leak the harness into the output.
func TestPreservedLine_PathIsRelativeToFactoryRoot(t *testing.T) {
	root := t.TempDir()
	seedVaultNote(t, root, "solver", "n1")

	got := PreservedLine(root, "solver")

	if strings.Contains(got, root) {
		t.Errorf("PreservedLine leaked the absolute factory root: %q", got)
	}
	if !strings.Contains(got, ".agentfactory") {
		t.Errorf("PreservedLine = %q, want it to name the vault under .agentfactory", got)
	}
}

// N=0 is suppressed: nothing was preserved and nothing was destroyed, so a teardown that never
// held a note produces no visible delta at all.
func TestPreservedLine_SilentWhenVaultIsEmpty(t *testing.T) {
	root := t.TempDir()

	if got := PreservedLine(root, "solver"); got != "" {
		t.Errorf("PreservedLine on an absent vault = %q, want \"\"", got)
	}

	if err := os.MkdirAll(filepath.Join(root, ".agentfactory", "memory", "solver"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := PreservedLine(root, "solver"); got != "" {
		t.Errorf("PreservedLine on an empty vault = %q, want \"\"", got)
	}
}

// A count that cannot be taken must never speak and must never abort the teardown that asked
// for it — the vault survives whether or not anyone managed to describe it.
func TestPreservedLine_SilentWhenCountFails(t *testing.T) {
	cases := map[string]struct{ root, agent string }{
		"relative root":      {"relative/root", "solver"},
		"invalid agent name": {t.TempDir(), "../escape"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := PreservedLine(tc.root, tc.agent); got != "" {
				t.Errorf("PreservedLine(%q, %q) = %q, want \"\"", tc.root, tc.agent, got)
			}
		})
	}
}

// Graduated and expired notes still occupy the vault, so a teardown report counts them: the
// line describes what survives on disk, not what an injector would serve.
func TestPreservedLine_CountsEveryStatus(t *testing.T) {
	root := t.TempDir()
	seedVaultNote(t, root, "solver", "active-one")
	if _, err := Write(root, Note{
		ID:      "graduated-one",
		Agent:   "solver",
		Status:  StatusGraduated,
		Created: time.Now().UTC(),
		Body:    "landed in a formula\n",
	}); err != nil {
		t.Fatalf("seeding graduated note: %v", err)
	}

	if got := PreservedLine(root, "solver"); !strings.Contains(got, "(2 notes)") {
		t.Errorf("PreservedLine = %q, want it to count both statuses", got)
	}
}

// Vaults are per-agent: one agent's teardown never reports another's notes.
func TestPreservedLine_IsScopedToOneAgent(t *testing.T) {
	root := t.TempDir()
	seedVaultNote(t, root, "alice", "a1")
	seedVaultNote(t, root, "alice", "a2")
	seedVaultNote(t, root, "bob", "b1")

	if got := PreservedLine(root, "bob"); !strings.Contains(got, "(1 notes)") {
		t.Errorf("PreservedLine for bob = %q, want a count of 1", got)
	}
}
