package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var storeNow = time.Date(2026, 8, 15, 1, 32, 0, 0, time.UTC)

// vaultDir composes the expected path BY HAND rather than via config.AgentMemoryDir — a test
// that derived its expectation from the constructor under test could not detect a fault inside
// it (the FormulaStorePath rule, internal/config/paths.go).
func vaultDir(root, agent string) string {
	return filepath.Join(root, ".agentfactory", "memory", agent)
}

func newNote(id string) Note {
	return Note{
		ID:      id,
		Agent:   "manager",
		Type:    TypeGotcha,
		Created: storeNow,
		Status:  StatusActive,
		Body:    "the body of " + id + "\n",
	}
}

func TestWrite_RoundTrip(t *testing.T) {
	root := t.TempDir()

	id, err := Write(root, newNote("note-one"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id != "note-one" {
		t.Errorf("returned id: got %q, want %q", id, "note-one")
	}
	if _, err := os.Stat(filepath.Join(vaultDir(root, "manager"), "note-one.md")); err != nil {
		t.Fatalf("note file: %v", err)
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("List: got %d notes, want 1", len(notes))
	}
	if notes[0].ID != "note-one" || notes[0].Body != "the body of note-one\n" {
		t.Errorf("round trip: got id %q body %q", notes[0].ID, notes[0].Body)
	}
	if notes[0].Malformed {
		t.Error("a note this store wrote read back as malformed")
	}
}

// TestWrite_NeverOverwritesAnExistingID is the single assertion that distinguishes the new
// O_EXCL composite from fsutil.WriteFileAtomic, whose documented semantic is last-writer-wins.
// Nothing in the acceptance criteria covers it.
func TestWrite_NeverOverwritesAnExistingID(t *testing.T) {
	root := t.TempDir()
	dir := vaultDir(root, "manager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	sentinel := []byte("PRE-EXISTING BYTES THAT MUST SURVIVE\n")
	target := filepath.Join(dir, "note-one.md")
	if err := os.WriteFile(target, sentinel, 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	id, err := Write(root, newNote("note-one"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id == "note-one" {
		t.Error("Write claimed an id that was already taken")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("re-reading the seeded file: %v", err)
	}
	if string(got) != string(sentinel) {
		t.Errorf("Write clobbered an existing note:\ngot  %q\nwant %q", got, sentinel)
	}
}

func TestWrite_CollisionGetsSuffix(t *testing.T) {
	root := t.TempDir()

	first, err := Write(root, newNote("same-id"))
	if err != nil {
		t.Fatalf("first Write: %v", err)
	}
	second, err := Write(root, newNote("same-id"))
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if first == second {
		t.Fatalf("both writes returned the same id %q", first)
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 2 {
		t.Errorf("List: got %d notes, want 2", len(notes))
	}
}

// TestWrite_ConcurrentSameIDProducesDistinctFiles is a census, not an assertion: the defect
// this guards (a Stat-then-create sequence standing in for an exclusive create) is a filesystem
// TOCTOU that `go test -race` cannot see, and a sequential "write twice" would pass with it
// intact. Modelled on internal/fsutil/atomic_test.go:52.
func TestWrite_ConcurrentSameIDProducesDistinctFiles(t *testing.T) {
	root := t.TempDir()
	const writers = 8

	var wg sync.WaitGroup
	ids := make([]string, writers)
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], errs[i] = Write(root, newNote("racing-id"))
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
		if seen[ids[i]] {
			t.Errorf("two writers were handed the same id %q", ids[i])
		}
		seen[ids[i]] = true
	}

	entries, err := os.ReadDir(vaultDir(root, "manager"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != writers {
		t.Errorf("vault holds %d files, want %d — a write was lost", len(entries), writers)
	}
}

func TestWrite_NoStrayTmpFilesOnSuccess(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("note-one")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := os.ReadDir(vaultDir(root, "manager"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "note-one.md" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("vault contents: got %v, want [note-one.md]", names)
	}
}

func TestWrite_CreatesVaultDirOnDemandWithFileMode(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("note-one")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	di, err := os.Stat(vaultDir(root, "manager"))
	if err != nil {
		t.Fatalf("vault dir: %v", err)
	}
	if !di.IsDir() {
		t.Fatal("vault path is not a directory")
	}
	if perm := di.Mode().Perm(); perm != 0o755 {
		t.Errorf("vault dir mode: got %o, want 755", perm)
	}

	fi, err := os.Stat(filepath.Join(vaultDir(root, "manager"), "note-one.md"))
	if err != nil {
		t.Fatalf("note file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Errorf("note file mode: got %o, want 644", perm)
	}
}

func TestWrite_RejectsInvalidAgentName(t *testing.T) {
	root := t.TempDir()
	for _, agent := range []string{"", "../manager", "man/ager", "1manager", strings.Repeat("a", 65)} {
		n := newNote("note-one")
		n.Agent = agent
		if _, err := Write(root, n); err == nil {
			t.Errorf("Write with agent %q: got nil error, want a refusal", agent)
		}
	}
}

// TestWrite_RefusesAVocabularyItsOwnReaderWouldCondemn closes the loop between the two halves of
// this package. Parse treats an unknown type or status as malformed; if Write does not treat it
// the same way, Write returns success and produces a note that is never served, cannot be marked
// (mark refuses malformed notes), and cannot be repaired through this API — while mark's error
// tells an operator to fix frontmatter no operator ever wrote.
func TestWrite_RefusesAVocabularyItsOwnReaderWouldCondemn(t *testing.T) {
	root := t.TempDir()

	for _, bad := range []Note{
		func() Note { n := newNote("bad-type"); n.Type = "documentation"; return n }(),
		func() Note { n := newNote("bad-type-2"); n.Type = "Gotcha"; return n }(),
		func() Note { n := newNote("bad-status"); n.Status = "archived"; return n }(),
		func() Note { n := newNote("bad-status-2"); n.Status = "Active"; return n }(),
	} {
		if _, err := Write(root, bad); err == nil {
			t.Errorf("Write(type %q, status %q): got nil error, want a refusal", bad.Type, bad.Status)
		}
	}

	// Positive control 1: every value IN the vocabulary writes, reads back clean, and is
	// servable — so the refusals above are about the vocabulary and not about Write.
	for _, noteType := range []string{TypeGotcha, TypeModelBehavior, TypeOps, TypeOutcome, TypeImprovement} {
		n := newNote("ok-" + noteType)
		n.Type = noteType
		if _, err := Write(root, n); err != nil {
			t.Errorf("Write(type %q): %v", noteType, err)
		}
	}

	// Positive control 2: an EMPTY type or status stays legal. It is the documented unset state,
	// and it is exactly what Emit writes for a note whose optional fields were never filled in.
	bare := newNote("bare")
	bare.Type = ""
	bare.Status = ""
	if _, err := Write(root, bare); err != nil {
		t.Fatalf("Write with no type or status: %v", err)
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, n := range notes {
		if n.Malformed {
			t.Errorf("note %q was written by this package and reads back malformed", n.ID)
		}
	}
	if len(notes) != 6 {
		t.Errorf("List: got %d notes, want 6 — a refused write left a file behind", len(notes))
	}
}

// TestWrite_RefusesWhatTheEncoderWouldMangle — every scalar goes through an encoder that
// SUBSTITUTES invalid UTF-8 (U+FFFD) rather than refusing it, so a note carrying any would be
// written with a value that silently differs from the one passed in. Linux argv can carry
// arbitrary bytes, so Phase 2's CLI can hand this package some.
func TestWrite_RefusesWhatTheEncoderWouldMangle(t *testing.T) {
	root := t.TempDir()
	bad := "\xff\xfe"

	cases := []struct {
		field string
		apply func(*Note)
	}{
		{"formula", func(n *Note) { n.Formula = bad }},
		{"run", func(n *Note) { n.Run = bad }},
		{"body", func(n *Note) { n.Body = bad }},
		{"evidence", func(n *Note) { n.Evidence = []string{"issue#1", bad} }},
	}
	for _, tc := range cases {
		n := newNote("invalid-" + tc.field)
		tc.apply(&n)
		if _, err := Write(root, n); err == nil {
			t.Errorf("Write with an invalid-UTF-8 %s: got nil error, want a refusal", tc.field)
		} else if !strings.Contains(err.Error(), tc.field) {
			t.Errorf("Write error names the wrong field: %v", err)
		}
	}

	// Positive control: valid multi-byte UTF-8 is not what is being refused here, and it survives
	// the round trip byte for byte.
	unicode := newNote("unicode")
	unicode.Run = "wt-★/セッション"
	unicode.Body = "…the grep matched ★\n"
	if _, err := Write(root, unicode); err != nil {
		t.Fatalf("Write with valid multi-byte UTF-8: %v", err)
	}
	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("List: got %d notes, want 1 — a refused write left a file behind", len(notes))
	}
	if notes[0].Run != unicode.Run || notes[0].Body != unicode.Body {
		t.Errorf("unicode round trip: got run %q body %q", notes[0].Run, notes[0].Body)
	}
}

// TestWrite_EnforcesTheGraduationVocabularyToo — graduated_to has two writers, Write and
// Graduate, and one restriction. A graduation recorded as free text or as a pointer into derived
// state silently becomes false on the next redeploy, which loses the note AND its successor;
// that is true whichever function wrote the field.
func TestWrite_EnforcesTheGraduationVocabularyToo(t *testing.T) {
	root := t.TempDir()
	present, err := Write(root, newNote("present"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	for _, dest := range []string{"slack message from 2026-08-15", "issue#abc", "formula:rootcause-all"} {
		n := newNote("bypass")
		n.Status = StatusGraduated
		n.GraduatedTo = dest
		if _, err := Write(root, n); err == nil {
			t.Errorf("Write with graduated_to %q: got nil error, want the same refusal Graduate gives", dest)
		}
		// The two writers must agree, so the same destination must be refused on both paths. The
		// id has to name a note that EXISTS: against a missing one the call fails for that reason
		// whatever the vocabulary check does, and the assertion would hold with the check deleted.
		if err := Graduate(root, "manager", present, dest); err == nil {
			t.Errorf("Graduate(%q): got nil error, want a refusal", dest)
		}
	}

	// Positive control: a destination IN the vocabulary is accepted by Write, and an unset one
	// stays legal — Write is a primitive, and an import legitimately produces a graduated note.
	ok := newNote("imported")
	ok.Status = StatusGraduated
	ok.GraduatedTo = "issue#631"
	if _, err := Write(root, ok); err != nil {
		t.Fatalf("Write with a valid destination: %v", err)
	}
	if _, err := Write(root, newNote("plain")); err != nil {
		t.Fatalf("Write with no destination: %v", err)
	}
}

// TestWrite_FailsRatherThanInventingAnID — a note with neither a usable id nor a timestamp has
// no address, and inventing one (the zero time formats as a perfectly plausible stem) would file
// every such note under the same name.
func TestWrite_FailsRatherThanInventingAnID(t *testing.T) {
	root := t.TempDir()
	n := newNote("///")
	n.Created = time.Time{}

	if id, err := Write(root, n); err == nil {
		t.Errorf("Write with no usable id and no timestamp: got id %q, want a refusal", id)
	}
	if entries, err := os.ReadDir(vaultDir(root, "manager")); err == nil && len(entries) != 0 {
		t.Errorf("the refused write left %d files behind", len(entries))
	}
}

// TestTheFilenameIsTheAddress — List overrides Note.ID from the filename, which means a note
// whose frontmatter disagrees is a note the mark operations can still find but an operator
// reading the file cannot. Write and mark are the two places that could introduce the
// disagreement, and neither is covered by List's own guard.
func TestTheFilenameIsTheAddress(t *testing.T) {
	root := t.TempDir()
	dir := vaultDir(root, "manager")

	if _, err := Write(root, newNote("dup")); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	second, err := Write(root, newNote("dup")) // collides, so the claimed id differs
	if err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if second == "dup" {
		t.Fatal("the second write claimed an id that was already taken")
	}

	idInFile := func(id string) string {
		raw, err := os.ReadFile(filepath.Join(dir, id+".md"))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", id, err)
		}
		return Parse(raw).ID
	}
	if got := idInFile(second); got != second {
		t.Errorf("after a collision the file %q says id: %q", second+".md", got)
	}

	if err := Expire(root, "manager", second); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if got := idInFile(second); got != second {
		t.Errorf("after a mark the file %q says id: %q", second+".md", got)
	}

	// mark's half of the claim needs a note that ALREADY disagrees. Marking a note Write produced
	// cannot see it: Write wrote the right id, so mark re-asserting it is a no-op. The repair only
	// has an effect on the state it exists to repair — a hand-edited file — so that is what the
	// fixture has to seed.
	seeded := "---\nid: fake\nagent: manager\ntype: gotcha\nstatus: active\n---\n\nhand written\n"
	if err := os.WriteFile(filepath.Join(dir, "real.md"), []byte(seeded), 0o644); err != nil {
		t.Fatalf("seeding a disagreeing note: %v", err)
	}
	if err := Expire(root, "manager", "real"); err != nil {
		t.Fatalf("Expire on the seeded note: %v", err)
	}
	if got := idInFile("real"); got != "real" {
		t.Errorf("mark left real.md saying id: %q — the frontmatter still disagrees with the filename", got)
	}
}

// TestEveryEntryPointGuardsTheAgentName — the agent name is a path segment, so it is the second
// caller-supplied string (after the note id) that can walk out of the vault. Write's guard has a
// test; the read and mark paths are where an unguarded name is worse, because List would SCAN
// whatever it resolved to and a mark would REWRITE a file inside it.
func TestEveryEntryPointGuardsTheAgentName(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("seed")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Each entry point gets its OWN note for the positive control below: the mark lifecycle is
	// monotone, so a note Expire already touched can no longer be graduated, and the control would
	// fail for a reason that has nothing to do with the agent name.
	entry := []struct {
		name string
		// setup prepares the control note for an entry point that refuses an active note.
		// Without it Reopen's control would fail on the lifecycle rather than on the name.
		setup func(root, id string) error
		call  func(root, agent, id string) error
	}{
		{name: "List", call: func(root, agent, _ string) error { _, err := List(root, agent, Filter{}); return err }},
		{name: "Expire", call: func(root, agent, id string) error { return Expire(root, agent, id) }},
		{name: "Graduate", call: func(root, agent, id string) error { return Graduate(root, agent, id, "issue#631") }},
		{name: "RebuildIndex", call: func(root, agent, _ string) error { return RebuildIndex(root, agent) }},
		{
			name:  "Reopen",
			setup: func(root, id string) error { return Graduate(root, "manager", id, "formula:fx@abcdef01") },
			call:  func(root, agent, id string) error { return Reopen(root, agent, id) },
		},
		{name: "Write", call: func(root, agent, id string) error {
			n := newNote(id)
			n.Agent = agent
			_, err := Write(root, n)
			return err
		}},
	}
	for i, e := range entry {
		for _, agent := range []string{"../../..", "../../../etc", "/etc", "", "a/b", "."} {
			if err := e.call(root, agent, "seed"); err == nil {
				t.Errorf("%s(agent=%q): got nil error, want a refusal", e.name, agent)
			}
		}
		// Positive control: the guard rejects a shape, not every name — a suite where the valid
		// name also errored would pass the loop above while the package was entirely broken.
		fresh := fmt.Sprintf("control%d", i)
		if _, err := Write(root, newNote(fresh)); err != nil {
			t.Fatalf("seeding %s's control note: %v", e.name, err)
		}
		if e.setup != nil {
			if err := e.setup(root, fresh); err != nil {
				t.Fatalf("preparing %s's control note: %v", e.name, err)
			}
		}
		if err := e.call(root, "manager", fresh); err != nil {
			t.Errorf("%s(agent=%q): got error %v, want acceptance", e.name, "manager", err)
		}
	}

	// "It returned an error" is too weak a claim for the mark path. AgentMemoryDir(root, "..")
	// resolves to root/.agentfactory, and resolveNotePath matches against a listing — so with the
	// guard gone the call fails only because it happens not to FIND the id there. Plant the id it
	// would find, and the difference between guarded and unguarded becomes a rewritten file
	// outside the vault. Byte-identity is the assertion; an error return is not.
	outside := filepath.Join(root, ".agentfactory", "victim.md")
	planted := "---\nid: victim\nagent: manager\ntype: gotcha\nstatus: active\n---\n\nnot ours\n"
	if err := os.WriteFile(outside, []byte(planted), 0o644); err != nil {
		t.Fatalf("planting a file outside the vault: %v", err)
	}
	if err := Expire(root, "..", "victim"); err == nil {
		t.Error(`Expire(agent: ".."): got nil error, want a refusal`)
	}
	after, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("re-reading the planted file: %v", err)
	}
	if string(after) != planted {
		t.Errorf("a mark rewrote a file OUTSIDE the vault:\n--- before ---\n%s\n--- after ---\n%s", planted, after)
	}
}

// TestList_SkipsANoteLargerThanTheReadCap — the read cap exists so a corrupt or hostile file
// cannot force an unbounded read on a session-start path. Nothing else in this package produces
// a file that large, so without this fixture the cap is unverified.
func TestList_SkipsANoteLargerThanTheReadCap(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("normal")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := vaultDir(root, "manager")

	body := strings.Repeat("x", maxNoteBytes)
	if err := os.WriteFile(filepath.Join(dir, "huge.md"), []byte("---\nid: huge\n---\n"+body), 0o644); err != nil {
		t.Fatalf("seeding an oversized note: %v", err)
	}
	// Positive control: a note just UNDER the cap is still read, so "huge is absent" is not also
	// true of a List that has stopped reading anything large.
	underCap := newNote("under-cap")
	underCap.Body = strings.Repeat("y", maxNoteBytes/2)
	if _, err := Write(root, underCap); err != nil {
		t.Fatalf("Write under-cap: %v", err)
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := idsOf(notes)
	sort.Strings(got)
	if !equalIDs(got, []string{"normal", "under-cap"}) {
		t.Errorf("List: got %v, want [normal under-cap] — the read cap did not bound the scan", got)
	}
}

// TestWrite_HostileIDCannotEscapeTheVault — the slug sanitizer drops '.' and '/', the two
// characters that turn a filename slug into a traversal vector.
func TestWrite_HostileIDCannotEscapeTheVault(t *testing.T) {
	root := t.TempDir()
	dir := vaultDir(root, "manager")

	for _, id := range []string{"../../etc/passwd", "/etc/passwd", "..", ".", "a\x00b", strings.Repeat("z", 300), "  "} {
		n := newNote(id)
		got, err := Write(root, n)
		if err != nil {
			continue // a refusal is an acceptable outcome
		}
		p := filepath.Join(dir, got+".md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("id %q: written note is not at %q: %v", id, p, err)
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			t.Fatalf("Abs: %v", err)
		}
		if !strings.HasPrefix(abs, dir+string(filepath.Separator)) {
			t.Errorf("id %q escaped the vault: %q", id, abs)
		}
	}
}

// TestWrite_NeverClaimsTheDerivedIndexName — index.md is regenerated by RebuildIndex, so a note
// that claimed that filename would be silently invisible to every read path.
func TestWrite_NeverClaimsTheDerivedIndexName(t *testing.T) {
	root := t.TempDir()

	id, err := Write(root, newNote("index"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id == "index" {
		t.Fatal("a note claimed the derived index filename")
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 {
		t.Errorf("List: got %d notes, want 1 — the note is invisible", len(notes))
	}
}

func TestWrite_FallsBackToATimestampStemWhenTheIDIsUnusable(t *testing.T) {
	root := t.TempDir()
	n := newNote("///")
	id, err := Write(root, n)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id != "2026-08-15T0132Z" {
		t.Errorf("timestamp stem: got %q, want %q", id, "2026-08-15T0132Z")
	}
}

func TestList_MissingDirIsEmptyNotError(t *testing.T) {
	root := t.TempDir()
	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List on a vault that does not exist: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("got %d notes, want 0", len(notes))
	}
}

// TestList_MalformedNoteDoesNotPoisonSiblings is two-sided on purpose: the malformed note must
// be suppressed from nothing and must still SURFACE as malformed, so `af memory status` can
// report it (internal/statusline/observation_test.go:645 model).
func TestList_MalformedNoteDoesNotPoisonSiblings(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("good-one")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := Write(root, newNote("good-two")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := vaultDir(root, "manager")
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("no frontmatter at all\n"), 0o644); err != nil {
		t.Fatalf("seeding a broken note: %v", err)
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 3 {
		t.Fatalf("List: got %d notes, want 3", len(notes))
	}

	var good, bad int
	for _, n := range notes {
		if n.Malformed {
			bad++
			if n.ID != "broken" {
				t.Errorf("malformed note id: got %q, want %q", n.ID, "broken")
			}
			continue
		}
		good++
		if !strings.HasPrefix(n.Body, "the body of good-") {
			t.Errorf("a good note lost its body: %q", n.Body)
		}
	}
	if good != 2 || bad != 1 {
		t.Errorf("got %d good and %d malformed, want 2 and 1", good, bad)
	}

	// The other side of "does not poison": when the broken note is the ONLY file present, the
	// vault reports one malformed note rather than reading as empty. An empty read is what an
	// operator cannot act on — it is indistinguishable from a fresh factory.
	t.Run("when it is the only file present", func(t *testing.T) {
		root := t.TempDir()
		dir := vaultDir(root, "manager")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("no frontmatter at all\n"), 0o644); err != nil {
			t.Fatalf("seeding a broken note: %v", err)
		}

		notes, err := List(root, "manager", Filter{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(notes) != 1 {
			t.Fatalf("List: got %d notes, want 1", len(notes))
		}
		if !notes[0].Malformed || notes[0].ID != "broken" {
			t.Errorf("got id %q malformed=%v, want \"broken\" malformed=true", notes[0].ID, notes[0].Malformed)
		}
		if !strings.Contains(notes[0].Body, "no frontmatter at all") {
			t.Errorf("the operator's text was not preserved: %q", notes[0].Body)
		}
	})
}

func TestList_IgnoresIndexAndNonNoteEntries(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("real-note")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := vaultDir(root, "manager")

	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte("# derived\n"), 0o644); err != nil {
		t.Fatalf("seeding index.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a note\n"), 0o644); err != nil {
		t.Fatalf("seeding notes.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "archive"), 0o755); err != nil {
		t.Fatalf("seeding archive/: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.md"), nil, 0o644); err != nil {
		t.Fatalf("seeding an in-flight reservation: %v", err)
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != "real-note" {
		t.Errorf("List: got %v, want just [real-note]", idsOf(notes))
	}
}

// TestList_DoesNotFollowSymlinksAndCannotWedge runs the scan behind a deadline because the
// failure mode of opening a FIFO is a HANG, which would present as a timed-out CI job rather
// than a red test (internal/statusline/observation_test.go:458 model).
func TestList_DoesNotFollowSymlinksAndCannotWedge(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("real-note")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := vaultDir(root, "manager")

	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("---\nid: outside\nstatus: active\n---\nleaked\n"), 0o644); err != nil {
		t.Fatalf("seeding the symlink target: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.md")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.md"), 0o644); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}

	type result struct {
		notes []Note
		err   error
	}
	done := make(chan result, 1)
	go func() {
		n, err := List(root, "manager", Filter{})
		done <- result{n, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("List: %v", r.err)
		}
		if len(r.notes) != 1 || r.notes[0].ID != "real-note" {
			t.Errorf("List: got %v, want just [real-note]", idsOf(r.notes))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("List blocked on a non-regular file — a regression here hangs rather than failing")
	}
}

func TestList_FilterHonored(t *testing.T) {
	root := t.TempDir()

	a := newNote("a")
	a.Formula = "rootcause-all"
	b := newNote("b")
	b.Type = TypeOps
	c := newNote("c")
	c.Status = StatusGraduated
	c.GraduatedTo = "issue#1"
	for _, n := range []Note{a, b, c} {
		if _, err := Write(root, n); err != nil {
			t.Fatalf("Write %s: %v", n.ID, err)
		}
	}

	cases := []struct {
		name string
		f    Filter
		want []string
	}{
		{"zero value reads everything", Filter{}, []string{"a", "b", "c"}},
		{"by status", Filter{Status: StatusActive}, []string{"a", "b"}},
		{"by type", Filter{Type: TypeOps}, []string{"b"}},
		{"by formula", Filter{Formula: "rootcause-all"}, []string{"a"}},
		{"no match", Filter{Type: "nonexistent"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notes, err := List(root, "manager", tc.f)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := idsOf(notes); !equalIDs(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExpire_LeavesTheFileOnDiskWithTheBodyIntact(t *testing.T) {
	root := t.TempDir()
	id, err := Write(root, newNote("note-one"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	path := filepath.Join(vaultDir(root, "manager"), id+".md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if err := Expire(root, "manager", id); err != nil {
		t.Fatalf("Expire: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file was deleted; notes are never deleted: %v", err)
	}
	if Parse(before).Body != Parse(after).Body {
		t.Error("Expire changed the body; it may only mark frontmatter")
	}
	n := Parse(after)
	if n.Status != StatusExpired {
		t.Errorf("Status: got %q, want %q", n.Status, StatusExpired)
	}
}

func TestGraduate_LeavesTheFileOnDiskAndRecordsTheDestination(t *testing.T) {
	root := t.TempDir()
	id, err := Write(root, newNote("note-one"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	path := filepath.Join(vaultDir(root, "manager"), id+".md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if err := Graduate(root, "manager", id, "issue#631"); err != nil {
		t.Fatalf("Graduate: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file was deleted; notes are never deleted: %v", err)
	}
	if Parse(before).Body != Parse(after).Body {
		t.Error("Graduate changed the body; it may only mark frontmatter")
	}
	n := Parse(after)
	if n.Status != StatusGraduated {
		t.Errorf("Status: got %q, want %q", n.Status, StatusGraduated)
	}
	if n.GraduatedTo != "issue#631" {
		t.Errorf("GraduatedTo: got %q, want %q", n.GraduatedTo, "issue#631")
	}
}

// TestGraduate_DestinationVocabulary pins design-doc.md:85. Free-text graduation to derived
// state silently becomes false on redeploy, which is the double loss the restriction prevents.
func TestGraduate_DestinationVocabulary(t *testing.T) {
	accepted := []string{
		"commit:9f2d04d3",
		"commit:9f2d04d3a1b2c3d4e5f60718293a4b5c6d7e8f90",
		"issue#631",
		"pr#516",
		"doc:docs/architecture/invariants.md",
		"formula:rootcause-all@9f2d04d3",
	}
	rejected := []string{
		"",
		"slack message",
		"http://example.com/x",
		"issue#abc",
		"issue#",
		"pr#",
		"formula:rootcause-all",
		"formula:@abcd",
		"commit:zzz",
		"commit:",
		// The length bounds on a hash, both ends. Without the lower one "commit:a" matches
		// thousands of objects and the reference is not a reference; without the upper one the
		// field is unbounded free text that merely happens to be hex.
		"commit:a",
		"commit:9f2d04d3a1b2c3d4e5f60718293a4b5c6d7e8f909f2d04d3a1b2c3d4e5f6071829",
		"doc:",
		"doc:/etc/passwd",
		"doc:../../etc/passwd",
		"formula:rootcause-all step outline-locate",
		// A formula name is a store filename stem, so a separator in it is traversal by one
		// route and a dot segment is traversal by the other — the same class doc: is guarded
		// against, and "." and ".." are not names anyway.
		"formula:../../etc@abcd",
		"formula:.@abcdef",
		"formula:..@abcdef",
	}

	for _, dest := range accepted {
		root := t.TempDir()
		id, err := Write(root, newNote("n"))
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := Graduate(root, "manager", id, dest); err != nil {
			t.Errorf("Graduate(%q): got error %v, want acceptance", dest, err)
		}
	}
	for _, dest := range rejected {
		root := t.TempDir()
		id, err := Write(root, newNote("n"))
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if err := Graduate(root, "manager", id, dest); err == nil {
			t.Errorf("Graduate(%q): got nil error, want a refusal", dest)
		}
	}
}

// TestMarks_IDIsMatchedAgainstTheListingNeverPathJoinedRaw — design-doc.md:85, MEM-CLI.
//
// Two things make this test able to fail, and both are easy to get wrong. The victim is seeded
// at <root>/victim.md while the vault is THREE directories below <root>, so the id list has to
// contain the depth that actually REACHES it ("../../../victim"); shorter ids only ever prove
// that a miss is a miss. And the assertion is byte-identity, not "the body is still there",
// because a mark preserves bodies by design — an out-of-vault rewrite would leave the victim's
// body in place and a substring check green while replacing all of its frontmatter.
func TestMarks_IDIsMatchedAgainstTheListingNeverPathJoinedRaw(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("note-one")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	victim := filepath.Join(root, "victim.md")
	seeded := []byte("---\nid: victim\nstatus: active\n---\nuntouched\n")
	if err := os.WriteFile(victim, seeded, 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	// The vault's nesting depth is a property of config.AgentMemoryDir, not of this package, so
	// the list walks one id per level up to and past it rather than hard-coding the reach.
	ids := []string{
		"../victim", "../../victim", "../../../victim", "../../../../victim",
		"note-one/../../../../victim", "/etc/passwd", "nonexistent", "note-one.md", "",
	}
	for _, id := range ids {
		if err := Expire(root, "manager", id); err == nil {
			t.Errorf("Expire(%q): got nil error, want a refusal", id)
		}
		if err := Graduate(root, "manager", id, "issue#1"); err == nil {
			t.Errorf("Graduate(%q): got nil error, want a refusal", id)
		}
	}

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(seeded) {
		t.Errorf("a file outside the vault was rewritten:\n--- got ---\n%s\n--- want ---\n%s", got, seeded)
	}

	// Positive control: the same calls on an id that IS in the listing succeed, so the refusals
	// above are about resolution and not about Expire being broken for everything.
	if err := Expire(root, "manager", "note-one"); err != nil {
		t.Errorf("Expire on a real id: %v", err)
	}
}

// TestStatusTransitionsAreMonotone — data.md:73-76. active is the only source state.
func TestStatusTransitionsAreMonotone(t *testing.T) {
	root := t.TempDir()
	id, err := Write(root, newNote("note-one"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Graduate(root, "manager", id, "issue#631"); err != nil {
		t.Fatalf("Graduate: %v", err)
	}
	if err := Graduate(root, "manager", id, "issue#632"); err == nil {
		t.Error("re-graduating a graduated note: got nil error, want a refusal")
	}
	if err := Expire(root, "manager", id); err == nil {
		t.Error("expiring a graduated note: got nil error, want a refusal")
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("List: got %d notes, want 1", len(notes))
	}
	if notes[0].GraduatedTo != "issue#631" || notes[0].Status != StatusGraduated {
		t.Errorf("the refused transitions mutated the note: status %q, graduated_to %q",
			notes[0].Status, notes[0].GraduatedTo)
	}
}

// TestMarks_RefuseToRewriteAMalformedNote — re-emitting canonically from struct fields would
// destroy the operator's frontmatter, and the operator wins on hand-edited content (T5).
func TestMarks_RefuseToRewriteAMalformedNote(t *testing.T) {
	root := t.TempDir()
	dir := vaultDir(root, "manager")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	raw := []byte("---\nid: hand-edited\nthis line has no colon\n---\nprecious body\n")
	if err := os.WriteFile(filepath.Join(dir, "hand-edited.md"), raw, 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if err := Expire(root, "manager", "hand-edited"); err == nil {
		t.Error("Expire on a malformed note: got nil error, want a refusal")
	}
	if err := Graduate(root, "manager", "hand-edited", "issue#1"); err == nil {
		t.Error("Graduate on a malformed note: got nil error, want a refusal")
	}

	got, err := os.ReadFile(filepath.Join(dir, "hand-edited.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("a malformed note was rewritten:\ngot  %q\nwant %q", got, raw)
	}
}

func TestRebuildIndex_IsDerivedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"alpha", "beta"} {
		if _, err := Write(root, newNote(id)); err != nil {
			t.Fatalf("Write %s: %v", id, err)
		}
	}
	dir := vaultDir(root, "manager")
	indexPath := filepath.Join(dir, "index.md")

	// Hand-written, not via Write: Write refuses a vocabulary its own reader condemns, so the only
	// way a malformed note exists is an operator typing one — which is exactly the case the index
	// has to survive.
	broken := "---\nid: broken\nagent: manager\ntype: banana\nstatus: active\n---\n\nhand written\n"
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte(broken), 0o644); err != nil {
		t.Fatalf("seeding malformed note: %v", err)
	}

	if err := RebuildIndex(root, "manager"); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	first, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("index.md: %v", err)
	}
	if !strings.Contains(string(first), "alpha") || !strings.Contains(string(first), "beta") {
		t.Errorf("index.md does not name the notes:\n%s", first)
	}

	if err := os.WriteFile(indexPath, []byte("hand edited\n"), 0o644); err != nil {
		t.Fatalf("hand-editing index.md: %v", err)
	}
	if err := RebuildIndex(root, "manager"); err != nil {
		t.Fatalf("second RebuildIndex: %v", err)
	}
	second, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("index.md: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("RebuildIndex is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 3 {
		t.Errorf("RebuildIndex disturbed the notes: got %d, want 3", len(notes))
	}

	// The malformed note carries status: active, so it is only kept out of the Active section by
	// the !n.Malformed guard on that section's predicate. Listing it once, under Malformed, is
	// the whole point: an operator scanning the index has to be able to find the file that needs
	// hand-fixing, and must not be told a note the reader distrusts is live memory.
	if got := strings.Count(string(first), "[[broken]]"); got != 1 {
		t.Errorf("malformed note is linked %d times, want exactly 1:\n%s", got, first)
	}
	malformed, _, found := strings.Cut(string(first), "[[broken]]")
	if !found {
		t.Fatalf("index.md does not link the malformed note at all:\n%s", first)
	}
	section := malformed[strings.LastIndex(malformed, "## ")+len("## "):]
	if title, _, _ := strings.Cut(section, " "); title != "Malformed" {
		t.Errorf("malformed note is filed under %q, want \"Malformed\":\n%s", title, first)
	}
}

// TestRebuildIndex_OrdersNewestFirstThenByID — idempotency is satisfied by ANY deterministic
// order, so the test above cannot see the order itself. The order is what an operator scans, and
// the id tie-break is what makes this sort total: notes written in the same minute share a
// timestamp, and RebuildIndex deliberately does NOT use a stable sort.
//
// The fixture is 20 notes in two Created buckets, interleaved so that filename order (which is
// how List feeds them, since os.ReadDir sorts and the filename IS the id) differs from the
// expected order. Three notes with one tie would not do: Go's sort falls to insertion sort below
// twelve elements, so a comparator with no tie-break would still come out in input order and the
// assertion would pass with the tie-break deleted.
func TestRebuildIndex_OrdersNewestFirstThenByID(t *testing.T) {
	root := t.TempDir()
	older, newer := storeNow, storeNow.Add(time.Hour)

	var wantNewer, wantOlder []string
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("n%02d", i)
		n := newNote(id)
		if i%2 == 0 {
			n.Created, wantNewer = newer, append(wantNewer, id)
		} else {
			n.Created, wantOlder = older, append(wantOlder, id)
		}
		if _, err := Write(root, n); err != nil {
			t.Fatalf("Write %s: %v", id, err)
		}
	}

	if err := RebuildIndex(root, "manager"); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(vaultDir(root, "manager"), "index.md"))
	if err != nil {
		t.Fatalf("index.md: %v", err)
	}

	var order []string
	for _, line := range strings.Split(string(got), "\n") {
		if _, rest, found := strings.Cut(line, "- [["); found {
			id, _, _ := strings.Cut(rest, "]]")
			order = append(order, id)
		}
	}
	want := append(append([]string{}, wantNewer...), wantOlder...)
	if !equalIDs(order, want) {
		t.Errorf("index order:\ngot  %v\nwant %v  (newest first, ties by id)", order, want)
	}
}

func TestRebuildIndex_EmptyVaultProducesTheEmptyState(t *testing.T) {
	root := t.TempDir()
	if err := RebuildIndex(root, "manager"); err != nil {
		t.Fatalf("RebuildIndex on an empty vault: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(vaultDir(root, "manager"), "index.md"))
	if err != nil {
		t.Fatalf("index.md: %v", err)
	}
	if !strings.Contains(string(got), "no memory recorded yet") {
		t.Errorf("empty-state index.md:\n%s", got)
	}
}

// TestEntryPointsRejectARelativeFactoryRoot — a relative root is resolved against the PROCESS
// working directory, which for an agent is the ephemeral worktree this vault exists to outlive.
// Accepting one would write the note, return no error, and lose it at the next teardown: the
// precise durability failure the subsystem is built to prevent, presenting as success.
//
// The assertion is on the FILESYSTEM rather than on the returned error, because "an error came
// back" is also true of a package that errored for some unrelated reason. For Write that means
// watching for stray files; for Expire and Graduate it means seeding a real vault under the cwd
// first — an agent's worktree genuinely contains a .agentfactory/, so without one the marks fail
// with "no such note" for a reason that has nothing to do with the guard, and the guard could be
// deleted without this test noticing.
func TestEntryPointsRejectARelativeFactoryRoot(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)

	// The vault a relative root WOULD resolve to: the copy inside the worktree, which is the
	// GAP-1 confusion this guard exists to catch.
	localVault := filepath.Join(cwd, ".agentfactory", "memory", "manager")
	if err := os.MkdirAll(localVault, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	localNote := filepath.Join(localVault, "note-one.md")
	seeded := Emit(newNote("note-one"))
	if err := os.WriteFile(localNote, seeded, 0o644); err != nil {
		t.Fatalf("seeding the local vault: %v", err)
	}

	for _, root := range []string{"", ".", ".agentfactory", "./relative", "../elsewhere"} {
		if _, err := Write(root, newNote("note-one")); err == nil {
			t.Errorf("Write(%q): got nil error, want a refusal", root)
		}
		if _, err := List(root, "manager", Filter{}); err == nil {
			t.Errorf("List(%q): got nil error, want a refusal", root)
		}
		if err := Expire(root, "manager", "note-one"); err == nil {
			t.Errorf("Expire(%q): got nil error, want a refusal", root)
		}
		if err := Graduate(root, "manager", "note-one", "issue#1"); err == nil {
			t.Errorf("Graduate(%q): got nil error, want a refusal", root)
		}
		if err := Reopen(root, "manager", "note-one"); err == nil {
			t.Errorf("Reopen(%q): got nil error, want a refusal", root)
		}
		if err := RebuildIndex(root, "manager"); err == nil {
			t.Errorf("RebuildIndex(%q): got nil error, want a refusal", root)
		}
	}

	if got, err := os.ReadFile(localNote); err != nil {
		t.Errorf("re-reading the seeded local note: %v", err)
	} else if string(got) != string(seeded) {
		t.Errorf("a relative factory root rewrote a note inside the process cwd:\n--- got ---\n%s\n--- want ---\n%s",
			got, seeded)
	}

	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != ".agentfactory" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("a relative factory root wrote into the process cwd: %v", names)
	}
	if got, err := os.ReadDir(localVault); err != nil {
		t.Errorf("ReadDir on the local vault: %v", err)
	} else if len(got) != 1 {
		t.Errorf("a relative factory root added files to the local vault: %d entries, want 1", len(got))
	}

	// Positive control: the same calls on an absolute root are accepted.
	abs := t.TempDir()
	if _, err := Write(abs, newNote("note-one")); err != nil {
		t.Fatalf("Write on an absolute root: %v", err)
	}
	if err := RebuildIndex(abs, "manager"); err != nil {
		t.Fatalf("RebuildIndex on an absolute root: %v", err)
	}
}

// TestMemoryPackageNamesTheVaultPathOnlyThroughConfig makes issue #563's one-spelling rule
// mechanical instead of reviewer-enforced, in the source-scanning idiom this repo already uses
// (internal/config/paths_disposition_test.go, internal/cmd/env_hermetic_test.go). A second
// spelling of the vault path inside this package would present as notes written where nothing
// ever reads them — which is indistinguishable from having lost them.
func TestMemoryPackageNamesTheVaultPathOnlyThroughConfig(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		scanned++
		for _, spelling := range []string{".agentfactory", `"memory"`} {
			if strings.Contains(string(data), spelling) {
				t.Errorf("%s spells the vault path itself (%q); compose it with config.AgentMemoryDir", name, spelling)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no source files; the guard proves nothing")
	}
}

func TestSanitizeSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"2026-08-15T0132Z-stale-designs-grep", "2026-08-15T0132Z-stale-designs-grep"},
		{"../../etc/passwd", "etcpasswd"},
		{"a b c", "abc"},
		{"", ""},
		{"...", ""},
		{"a\x00b", "ab"},
		{strings.Repeat("z", 300), strings.Repeat("z", maxSlugLen)},
	}
	for _, tc := range cases {
		if got := sanitizeSlug(tc.in); got != tc.want {
			t.Errorf("sanitizeSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestList_IgnoresAnythingThatIsNotANoteFile — the extension guard is not cosmetic. Ids come from
// filenames with the extension trimmed, so a sidecar named "n1" beside "n1.md" would trim to the
// same id and the vault would list one note twice: the same note, read twice, injected twice,
// and markable by an id that names two files. Editors and sync tools leave exactly these behind.
func TestList_IgnoresAnythingThatIsNotANoteFile(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("n1")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	dir := vaultDir(root, "manager")

	for _, name := range []string{"n1", "n1.md.swp", "n1.txt", ".n1.md.tmp", "README"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not a note\n"), 0o644); err != nil {
			t.Fatalf("planting %q: %v", name, err)
		}
	}

	notes, err := List(root, "manager", Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != "n1" {
		t.Errorf("List: got %v, want exactly one note n1 — a non-note file was read as one", idsOf(notes))
	}
}

// TestReadNoteFile_DoesNotFollowASymlink — List's own listing already excludes symlinks by type,
// so this exercises the window between that check and the open: an entry that WAS a regular file
// can be swapped before it is read. The consequence of following one is a read of an arbitrary
// file on the box, on a session-start path, into a block that gets injected into a prompt.
func TestReadNoteFile_DoesNotFollowASymlink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("not for the prompt\n"), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if data, err := readNoteFile(link); err == nil {
		t.Errorf("readNoteFile followed a symlink and returned %q", data)
	}
	// Positive control: the same call on the real file works, so the assertion above is about
	// symlinks and not about readNoteFile being broken outright.
	if _, err := readNoteFile(secret); err != nil {
		t.Errorf("readNoteFile on a regular file: got error %v, want success", err)
	}
}

// TestReopen_ReturnsAGraduatedNoteToActive is Gap 11's mechanism. Every other transition in this
// package is one-way; this one exists because a formula: graduation points at DERIVED state
// (ADR-015), so the note's own claim can be proven false by comparing bytes.
func TestReopen_ReturnsAGraduatedNoteToActive(t *testing.T) {
	root := t.TempDir()
	n := newNote("reopen-me")
	n.Body = "the body must survive a round trip through two marks"
	if _, err := Write(root, n); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Graduate(root, "manager", "reopen-me", "formula:fx@abcdef01"); err != nil {
		t.Fatalf("Graduate: %v", err)
	}
	if err := Reopen(root, "manager", "reopen-me"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}

	got := onlyNote(t, root, "manager", "reopen-me")
	if got.Status != StatusActive {
		t.Errorf("Status = %q, want %q", got.Status, StatusActive)
	}
	// A reopened note that kept its destination would still be asserting the graduation the
	// reopen exists to withdraw, and the next hygiene pass would re-check and re-reopen it
	// forever.
	if got.GraduatedTo != "" {
		t.Errorf("GraduatedTo = %q, want it cleared", got.GraduatedTo)
	}
	// Trimmed: Emit terminates the body with a newline, so a round trip through any mark gains
	// one. What must not change is the text, which is the half no mark is allowed to touch.
	if strings.TrimSpace(got.Body) != strings.TrimSpace(n.Body) {
		t.Errorf("Reopen rewrote the body: got %q, want %q", got.Body, n.Body)
	}
	if got.Malformed {
		t.Error("a reopened note must still parse")
	}
}

// TestReopen_RefusesEveryTransitionButGraduatedToActive is the narrowness guard. Reopen is the one
// hole in a monotone lifecycle, and the reason it is safe is that it is only reachable from the
// one state whose claim can be mechanically falsified. An expired note's claim cannot: expiry says
// the world moved on, and no content hash disagrees with that.
func TestReopen_RefusesEveryTransitionButGraduatedToActive(t *testing.T) {
	root := t.TempDir()

	for _, tc := range []struct {
		name  string
		id    string
		setup func(t *testing.T, id string)
	}{
		{name: "active", id: "still-active"},
		{
			name: "expired",
			id:   "already-expired",
			setup: func(t *testing.T, id string) {
				if err := Expire(root, "manager", id); err != nil {
					t.Fatalf("Expire: %v", err)
				}
			},
		},
		{
			name: "already reopened",
			id:   "twice",
			setup: func(t *testing.T, id string) {
				if err := Graduate(root, "manager", id, "issue#515"); err != nil {
					t.Fatalf("Graduate: %v", err)
				}
				if err := Reopen(root, "manager", id); err != nil {
					t.Fatalf("first Reopen: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Write(root, newNote(tc.id)); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if tc.setup != nil {
				tc.setup(t, tc.id)
			}
			if err := Reopen(root, "manager", tc.id); err == nil {
				t.Errorf("Reopen from %s: got nil error, want a refusal", tc.name)
			}
		})
	}

	// Positive control: the refusals above are about the SOURCE STATUS, not about Reopen being
	// broken outright.
	if _, err := Write(root, newNote("control")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Graduate(root, "manager", "control", "commit:0123abcd"); err != nil {
		t.Fatalf("Graduate: %v", err)
	}
	if err := Reopen(root, "manager", "control"); err != nil {
		t.Errorf("Reopen from graduated: got %v, want acceptance", err)
	}
}

// TestReopen_EmitsNoNewFrontmatterKey guards the codec golden from the far side. A provenance key
// recorded here would buy one breadcrumb at the cost of the invariant that every note in the vault
// emits the same keys in the same order — and the reopen event already has a surface a human
// actually reads, the hygiene nag mail.
func TestReopen_EmitsNoNewFrontmatterKey(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, newNote("shape")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	before := noteKeys(t, root, "manager", "shape")

	if err := Graduate(root, "manager", "shape", "formula:fx@abcdef01"); err != nil {
		t.Fatalf("Graduate: %v", err)
	}
	if err := Reopen(root, "manager", "shape"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if after := noteKeys(t, root, "manager", "shape"); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("Reopen changed the emitted frontmatter shape:\nbefore %v\nafter  %v", before, after)
	}
}

// onlyNote returns one note from a vault by id.
func onlyNote(t *testing.T, root, agent, id string) Note {
	t.Helper()
	notes, err := List(root, agent, Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, n := range notes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("note %q is not in %s's vault", id, agent)
	return Note{}
}

// noteKeys reads a note's raw frontmatter keys in order.
func noteKeys(t *testing.T, root, agent, id string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".agentfactory", "memory", agent, id+".md"))
	if err != nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	var keys []string
	for _, line := range strings.Split(string(data), "\n")[1:] {
		if line == "---" {
			break
		}
		key, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}
