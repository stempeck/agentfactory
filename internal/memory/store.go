package memory

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
)

const (
	noteExt = ".md"
	// indexStem names the derived per-agent index. A note may never claim it: index.md is
	// regenerated from the notes, so a note stored under that name would be overwritten by the
	// next rebuild and is skipped by every read in the meantime — invisible rather than lost,
	// which is worse.
	indexStem = "index"
	// idTimeLayout is the fallback filename stem when a note carries no usable id. It is the
	// minute-resolution UTC stamp the design's own ids are built from.
	idTimeLayout = "2006-01-02T1504Z"
	// maxNoteBytes bounds a single note read so a corrupt or hostile file cannot force an
	// unbounded read on a session-start path. A note is a journal entry; 64 KB is generous.
	maxNoteBytes = 64 * 1024
	// maxIDCollisions bounds the suffix search so a pathological directory cannot spin. It is a
	// backstop, not a working limit: reaching it means something is wrong with the vault.
	maxIDCollisions = 1000
)

var (
	errNoteTooLarge = errors.New("memory: note exceeds size cap")
	// ErrNoteNotFound reports that no note with the given id exists in the agent's vault. Ids
	// are resolved against the directory listing, so this is also what a traversal attempt gets.
	ErrNoteNotFound = errors.New("memory: no such note")
)

// Write records a new note and returns the id it was stored under, which may differ from the
// one requested if that id was taken or unusable.
//
// The id is reserved with an EXCLUSIVE create at the final path before any content is written.
// That ordering is the whole point. The obvious alternative — stat the path, and write it if it
// is absent — is a filesystem TOCTOU that `go test -race` cannot see and that this repository
// has already shipped once: eight concurrent writers on one name committed eight times with
// zero conflicts. An in-process mutex is not a fallback either, because the writers here are
// two `af` processes in two worktrees. Only an exclusive create is a real reservation.
//
// Content then lands via temp+rename, so a concurrent reader sees the whole note or no note,
// never a partial one — and because the rename targets a name this process already owns, it
// cannot clobber a note somebody else reserved in between.
func Write(factoryRoot string, n Note) (string, error) {
	if err := validateRoot(factoryRoot); err != nil {
		return "", err
	}
	// The note path is built from a field of the note. Without this check an agent name
	// carrying path separators would place the vault anywhere the process can write.
	if err := config.ValidateAgentName(n.Agent); err != nil {
		return "", fmt.Errorf("memory: refusing to write a note for an invalid agent name: %w", err)
	}
	// Type and Status are closed vocabularies the READER enforces: a note carrying an unknown one
	// parses as malformed. The writer has to enforce the same two, or Write reports success and
	// produces a note that is never served, cannot be marked, and cannot be repaired through this
	// API — with mark() telling an operator to fix frontmatter that no operator ever touched.
	// Graduate already validates its own vocabulary at this same boundary. An EMPTY value stays
	// legal; it is the documented unset state.
	if n.Type != "" && !knownType(n.Type) {
		return "", fmt.Errorf("memory: refusing to write a note with an unknown type %q", n.Type)
	}
	if n.Status != "" && !knownStatus(n.Status) {
		return "", fmt.Errorf("memory: refusing to write a note with an unknown status %q", n.Status)
	}
	// graduated_to has TWO writers — this one and Graduate — and only one of them was checking it.
	// A destination outside the vocabulary is the failure the restriction exists to prevent: a
	// graduation recorded as free text, or as a pointer into derived state, silently becomes false
	// on the next redeploy, and takes the note's successor with it. Write does not otherwise
	// restrict Status: a born-graduated note is what an import or a migration produces, and this
	// is a library primitive.
	if n.GraduatedTo != "" {
		if err := validateDestination(n.GraduatedTo); err != nil {
			return "", err
		}
	}
	// Two different reasons, one guard. The frontmatter scalars go through an encoder that
	// SUBSTITUTES invalid UTF-8 rather than refusing it, so a note carrying any would be silently
	// mangled on the way to disk; refusing here is what keeps that substitution unreachable for
	// notes this package creates. The body never reaches that encoder — Emit writes it raw and
	// Parse reads it back byte-identical — so its reason is the format's own: a vault of Markdown
	// notes an operator reads in Obsidian is UTF-8 by contract, and one stray byte from pasted
	// terminal output should fail loudly at the door rather than land in a file whose renderer
	// will quietly mangle it. id and agent are checked for neither reason (the reserved id
	// replaces the caller's before Emit, and an invalid agent name is already refused above) —
	// they are in the list so that a future caller cannot make them reachable without noticing.
	// Linux argv can carry arbitrary bytes, so Phase 2's CLI can hand us some.
	if field := firstInvalidUTF8Field(n); field != "" {
		return "", fmt.Errorf("memory: refusing to write a note whose %s is not valid UTF-8", field)
	}
	dir := config.AgentMemoryDir(factoryRoot, n.Agent)
	if err := os.MkdirAll(dir, 0o755); err != nil { // there is no fsutil.MkdirAll
		return "", fmt.Errorf("memory: creating vault dir: %w", err)
	}

	stem := sanitizeSlug(n.ID)
	if stem == "" {
		if n.Created.IsZero() {
			return "", fmt.Errorf("memory: note has neither a usable id nor a created timestamp")
		}
		stem = n.Created.UTC().Format(idTimeLayout)
	}

	id, path, err := reserveID(dir, stem)
	if err != nil {
		return "", err
	}

	n.ID = id
	if err := fsutil.WriteFileAtomic(path, Emit(n), 0o644); err != nil {
		os.Remove(path) // release the reservation rather than leave an empty note behind
		return "", fmt.Errorf("memory: writing note: %w", err)
	}
	return id, nil
}

// firstInvalidUTF8Field names the first field of a note that is not valid UTF-8, or "" if the
// note is clean. It names the field rather than reporting a bool because the caller's error is
// the only place an operator will find out which one of nine it was.
func firstInvalidUTF8Field(n Note) string {
	for _, f := range []struct{ name, value string }{
		{"id", n.ID}, {"agent", n.Agent}, {"formula", n.Formula}, {"run", n.Run},
		{"type", n.Type}, {"status", n.Status}, {"graduated_to", n.GraduatedTo}, {"body", n.Body},
	} {
		if !utf8.ValidString(f.value) {
			return f.name
		}
	}
	for _, e := range n.Evidence {
		if !utf8.ValidString(e) {
			return "evidence"
		}
	}
	return ""
}

// validateRoot refuses a factory root that is not absolute. This is VALIDATION, not resolution:
// the package still derives no root of its own (that stays the caller's job, and Phase 2's), it
// only declines one that the OS would resolve against the process working directory. For an
// agent that directory IS the ephemeral worktree the vault exists to outlive, so accepting a
// relative root would write the note, report success, and lose it at the next teardown — the
// failure this whole subsystem is built to prevent, wearing the appearance of having worked.
func validateRoot(factoryRoot string) error {
	if !filepath.IsAbs(factoryRoot) {
		return fmt.Errorf("memory: factory root must be an absolute path, got %q", factoryRoot)
	}
	return nil
}

// reserveID claims a filename exclusively, bumping a numeric suffix on collision, and returns
// the claimed id and path. The zero-byte placeholder it leaves behind is what List skips as an
// in-flight write.
func reserveID(dir, stem string) (string, string, error) {
	for i := 0; i <= maxIDCollisions; i++ {
		id := stem
		if i > 0 {
			id = fmt.Sprintf("%s-%d", stem, i)
		}
		if id == indexStem {
			continue
		}
		path := filepath.Join(dir, id+noteExt)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("memory: reserving note id: %w", err)
		}
		if err := f.Close(); err != nil {
			os.Remove(path)
			return "", "", fmt.Errorf("memory: reserving note id: %w", err)
		}
		return id, path, nil
	}
	return "", "", fmt.Errorf("memory: could not find a free id for %q: the bare id and %d suffixed variants are all taken", stem, maxIDCollisions)
}

// List reads an agent's vault. A vault that does not exist yet is an empty result, not an
// error — it is the normal state of a fresh factory. Anything in the directory that is not a
// readable note file is skipped rather than fatal, so one bad entry cannot cost an agent every
// learning it ever recorded.
func List(factoryRoot, agent string, f Filter) ([]Note, error) {
	if err := validateRoot(factoryRoot); err != nil {
		return nil, err
	}
	if err := config.ValidateAgentName(agent); err != nil {
		return nil, fmt.Errorf("memory: refusing to read a vault for an invalid agent name: %w", err)
	}
	dir := config.AgentMemoryDir(factoryRoot, agent)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: reading vault: %w", err)
	}

	var notes []Note
	for _, e := range entries {
		id, ok := noteIDOf(e)
		if !ok {
			continue
		}
		data, err := readNoteFile(filepath.Join(dir, id+noteExt))
		if err != nil || len(data) == 0 {
			// A zero-byte file is a reservation whose content write has not landed yet; an
			// unreadable one is somebody else's problem. Either way, skip rather than trust.
			continue
		}
		n := Parse(data)
		// The filename is the address. A frontmatter id that disagrees is not authoritative,
		// because every mark operation resolves ids against this same listing.
		n.ID = id
		if f.matches(n) {
			notes = append(notes, n)
		}
	}
	return notes, nil
}

// noteIDOf reports the note id for a directory entry, or !ok if the entry is not a note.
// DirEntry.Type() carries lstat semantics, so a symlink reports as a symlink here rather than
// as whatever it points at — os.Stat would follow it straight out of the vault.
func noteIDOf(e fs.DirEntry) (string, bool) {
	if !e.Type().IsRegular() { // excludes symlinks, directories, FIFOs, sockets, devices
		return "", false
	}
	name := e.Name()
	if filepath.Ext(name) != noteExt {
		return "", false
	}
	id := strings.TrimSuffix(name, noteExt)
	if id == "" || id == indexStem {
		return "", false
	}
	return id, true
}

// readNoteFile reads one note with a size cap. Following the house idiom, an oversized or
// unreadable file comes back as an error so the caller skips it rather than trusting it.
func readNoteFile(path string) ([]byte, error) {
	// O_NOFOLLOW closes the gap between the ReadDir that said "regular file" and this open: the
	// entry could have been replaced by a symlink in between. O_NONBLOCK is its partner — a FIFO
	// swapped in the same window would otherwise block this open forever, and this runs on a
	// session-start path where hanging is the worst available failure.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: path is composed from a vault directory listing
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxNoteBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxNoteBytes {
		return nil, errNoteTooLarge
	}
	return data, nil
}

// Graduate marks a note as having become something durable. The destination vocabulary is
// restricted because a graduation recorded as free text — or as a pointer into derived state —
// silently becomes false on the next redeploy, which loses the note AND its successor.
//
// The file is not deleted, and neither is its body: only frontmatter changes.
func Graduate(factoryRoot, agent, id, dest string) error {
	if err := validateDestination(dest); err != nil {
		return err
	}
	return mark(factoryRoot, agent, id, StatusActive, func(n *Note) {
		n.Status = StatusGraduated
		n.GraduatedTo = dest
	})
}

// Expire marks a note as stale. THE FILE REMAINS ON DISK — expiry stops a note being served,
// it does not destroy it, and no agent-reachable path in this package deletes a note.
func Expire(factoryRoot, agent, id string) error {
	return mark(factoryRoot, agent, id, StatusActive, func(n *Note) {
		n.Status = StatusExpired
	})
}

// Reopen returns a GRADUATED note to active and drops the destination it no longer claims. It is
// the one exception to the monotone lifecycle, and it exists because a formula:<name>@<hash>
// graduation points at DERIVED state: make sync-formulas is an unconditional cp and af install
// overwrites the store formula on any byte-diff (ADR-015), so the target can revert and leave the
// note asserting something the store can prove false — Gap 11, .designs/515/design-doc.md:157.
//
// That proof is the whole justification. Reopening is not a transition anyone can request; it is a
// correction the hygiene pass applies when it has the recorded hash and the current bytes in hand
// and they disagree. Which is why expired -> active stays refused: expiry is a claim about the
// world going stale, and nothing here can falsify it the way a content hash can.
//
// It writes NO new frontmatter. A reopened note is indistinguishable from one that never
// graduated, deliberately: the emitted shape is a golden (codec_test.go), and a provenance key
// here would buy one breadcrumb at the cost of the invariant that every note in the vault emits
// the same keys in the same order. The event is recorded where a human will see it — the nag mail.
func Reopen(factoryRoot, agent, id string) error {
	return mark(factoryRoot, agent, id, StatusGraduated, func(n *Note) {
		n.Status = StatusActive
		n.GraduatedTo = ""
	})
}

// mark applies a frontmatter-only rewrite. Last-writer-wins is the correct semantic here: the
// rewrite targets an existing file, and the design accepts that two marks racing on one note
// resolve to one of them rather than to a merge. What it must never do is lose the body or the
// file, which is why the note is re-emitted from a parse of its current contents.
//
// from is the ONLY status the rewrite accepts, passed by each caller rather than assumed, so that
// adding Reopen could not silently legalise the transitions nobody asked for — expired to
// graduated most of all.
func mark(factoryRoot, agent, id, from string, apply func(*Note)) error {
	if err := validateRoot(factoryRoot); err != nil {
		return err
	}
	if err := config.ValidateAgentName(agent); err != nil {
		return fmt.Errorf("memory: refusing to mark a note for an invalid agent name: %w", err)
	}
	dir := config.AgentMemoryDir(factoryRoot, agent)
	path, err := resolveNotePath(dir, id)
	if err != nil {
		return err
	}
	data, err := readNoteFile(path)
	if err != nil {
		return fmt.Errorf("memory: reading note %q: %w", id, err)
	}

	n := Parse(data)
	if n.Malformed {
		// Re-emitting canonically would drop whatever the codec could not read, and the operator
		// owns hand-edited content. Refuse loudly instead of silently rewriting their file.
		return fmt.Errorf("memory: refusing to rewrite note %q: its frontmatter is malformed — fix it first", id)
	}
	if n.Status != from {
		// Transitions are monotone with exactly one exception: a note leaves active once, and the
		// only route back is Reopen, which the store takes only against a graduation whose content
		// hash it can prove stale.
		return fmt.Errorf("memory: note %q is already %s", id, n.Status)
	}
	n.ID = id
	apply(&n)

	if err := fsutil.WriteFileAtomic(path, Emit(n), 0o644); err != nil {
		return fmt.Errorf("memory: rewriting note %q: %w", id, err)
	}
	return nil
}

// resolveNotePath matches an id against the directory listing rather than joining it onto the
// vault path. A traversal attempt therefore fails by simply not being in the listing — there is
// no path to sanitize because no path was ever built from caller input.
func resolveNotePath(dir, id string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %q", ErrNoteNotFound, id)
		}
		return "", fmt.Errorf("memory: reading vault: %w", err)
	}
	for _, e := range entries {
		if got, ok := noteIDOf(e); ok && got == id {
			return filepath.Join(dir, got+noteExt), nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrNoteNotFound, id)
}

// validateDestination enforces the graduation vocabulary: commit:<sha>, issue#N, pr#N,
// doc:<committed path>, formula:<name>@<content-hash>.
func validateDestination(dest string) error {
	invalid := fmt.Errorf("memory: invalid graduation destination %q: want commit:<sha>, issue#N, pr#N, doc:<path> or formula:<name>@<hash>", dest)

	switch {
	case strings.HasPrefix(dest, "commit:"):
		if !isHex(strings.TrimPrefix(dest, "commit:"), 7, 64) {
			return invalid
		}
	case strings.HasPrefix(dest, "issue#"):
		if !isDigits(strings.TrimPrefix(dest, "issue#")) {
			return invalid
		}
	case strings.HasPrefix(dest, "pr#"):
		if !isDigits(strings.TrimPrefix(dest, "pr#")) {
			return invalid
		}
	case strings.HasPrefix(dest, "doc:"):
		// A committed path: relative, inside the repo, and free of traversal — the reference has
		// to still resolve on a machine that is not this one.
		p := strings.TrimPrefix(dest, "doc:")
		if p == "" || strings.ContainsAny(p, " \t") || filepath.IsAbs(p) || p != filepath.Clean(p) || strings.HasPrefix(p, "..") {
			return invalid
		}
	case strings.HasPrefix(dest, "formula:"):
		// The content hash is what makes this survivable: a store formula can be replaced or
		// reverted, and without the hash the graduation would quietly become a lie.
		// A formula name is a store filename stem, so it is guarded like the doc: path above: a
		// separator is traversal by one route and a dot segment is traversal by the other, and
		// "." and ".." are not names anyway.
		name, hash, found := strings.Cut(strings.TrimPrefix(dest, "formula:"), "@")
		if !found || name == "" || name == "." || name == ".." || strings.ContainsAny(name, " \t/") || !isHex(hash, 4, 64) {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isHex(s string, min, max int) bool {
	if len(s) < min || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') && !(c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// RebuildIndex regenerates the agent's index.md — the human and Obsidian entry point into the
// vault. The file is DERIVED: it is rebuilt from the notes every time, so it is safe to delete,
// safe to overwrite, and never the source of truth for anything. Last-writer-wins is correct
// for exactly that reason.
func RebuildIndex(factoryRoot, agent string) error {
	if err := validateRoot(factoryRoot); err != nil {
		return err
	}
	notes, err := List(factoryRoot, agent, Filter{})
	if err != nil {
		return err
	}
	dir := config.AgentMemoryDir(factoryRoot, agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("memory: creating vault dir: %w", err)
	}

	// Deterministic order is what makes a rebuild idempotent: newest first, ties broken by id.
	// Ids are unique within a vault (they are filenames), so the comparator is already a total
	// order and a stable sort would buy nothing here — unlike in Slice, where ties are real.
	sort.Slice(notes, func(i, j int) bool {
		if !notes[i].Created.Equal(notes[j].Created) {
			return notes[j].Created.Before(notes[i].Created)
		}
		return notes[i].ID < notes[j].ID
	})

	var b strings.Builder
	fmt.Fprintf(&b, "# %s memory\n\n", agent)
	b.WriteString("Derived file — regenerated by `af memory`. Edits here are overwritten; edit the notes instead.\n\n")

	if len(notes) == 0 {
		fmt.Fprintf(&b, "no memory recorded yet for %s — this is a normal state on a fresh factory.\n", agent)
		return fsutil.WriteFileAtomic(filepath.Join(dir, indexStem+noteExt), []byte(b.String()), 0o644)
	}

	sections := []struct {
		title  string
		belong func(Note) bool
	}{
		{"Active", func(n Note) bool { return !n.Malformed && n.Status == StatusActive }},
		{"Graduated", func(n Note) bool { return !n.Malformed && n.Status == StatusGraduated }},
		{"Expired", func(n Note) bool { return !n.Malformed && n.Status == StatusExpired }},
		{"Malformed", func(n Note) bool { return n.Malformed }},
	}
	for _, s := range sections {
		var in []Note
		for _, n := range notes {
			if s.belong(n) {
				in = append(in, n)
			}
		}
		if len(in) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s (%d)\n\n", s.title, len(in))
		for _, n := range in {
			fmt.Fprintf(&b, "- [[%s]] — %s\n", n.ID, indexSummary(n))
		}
		b.WriteString("\n")
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, indexStem+noteExt), []byte(b.String()), 0o644)
}

// indexSummary is the one-line description beside a wikilink. It reads the note rather than
// re-rendering it: type, date, and the first line of the body, capped.
func indexSummary(n Note) string {
	parts := make([]string, 0, 3)
	if n.Type != "" {
		parts = append(parts, n.Type)
	}
	if !n.Created.IsZero() {
		parts = append(parts, n.Created.UTC().Format(dateLayout))
	}
	if first, _, _ := strings.Cut(strings.TrimSpace(n.Body), "\n"); first != "" {
		parts = append(parts, excerpt(first, 80))
	}
	return strings.Join(parts, " — ")
}
