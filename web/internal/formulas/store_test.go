package formulas

// ADR-018 discipline: every fixture below is a t.TempDir() sandbox. These tests NEVER read or write
// the real store at <root>/.agentfactory/store/formulas — a synthesized temp store is the only way to
// exercise the read-only fault-card path (all 29 live names already conform, so it has zero production
// triggers). Mirrors web/internal/feedback/writer_test.go's hermetic-fixture doctrine.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var _ = New // compile-time proof the constructor exists (the package idiom, cf. writer_test.go:25)

// sha256hex re-computes the CAS base hash INDEPENDENTLY of the store's own helper, so a matching-base
// overwrite test also proves the store uses lowercase hex sha256 — not just that it is self-consistent.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newStore returns a Store over a fresh temp root with the store dir pre-created (0o755), plus that dir.
func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".agentfactory", "store", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	return New(root), dir
}

func storeFile(dir, name string) string { return filepath.Join(dir, name+".formula.toml") }

// ---- Rung 1: create-name rule ^[a-z][a-z0-9-]{0,63}$ ----

func TestWrite_AcceptsWellFormedName(t *testing.T) {
	s, dir := newStore(t)
	body := []byte("name = \"ok\"\n")
	if err := s.Write("my-formula", body, ""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(storeFile(dir, "my-formula"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("stored %q, want %q", got, body)
	}
}

func TestWrite_NameRuleRejectsBadShape(t *testing.T) {
	longName := strings.Repeat("a", 65) // 65 chars: exceeds the {0,63} cap (1 + 63 max)
	cases := []struct{ name, arg string }{
		{"empty", ""},
		{"leading_digit", "1abc"},
		{"uppercase", "A-Cap"},
		{"underscore", "has_underscore"},
		{"too_long", longName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, dir := newStore(t)
			err := s.Write(c.arg, []byte("x = 1"), "")
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("Write(%q) err = %v, want ErrInvalidName", c.arg, err)
			}
			assertStoreEmpty(t, dir)
		})
	}
}

func TestWrite_AcceptsBoundaryLengthName(t *testing.T) {
	s, dir := newStore(t)
	name := "a" + strings.Repeat("b", 63) // exactly 64 chars: the {0,63} boundary, ACCEPTED
	if err := s.Write(name, []byte("x = 1"), ""); err != nil {
		t.Fatalf("Write(64-char name): %v", err)
	}
	if _, err := os.Stat(storeFile(dir, name)); err != nil {
		t.Fatalf("64-char name not written: %v", err)
	}
}

// ---- Rung 1/3: traversal containment (T1 path escape) ----

func TestWrite_TraversalNameRefused(t *testing.T) {
	// Each of these must be refused before any byte lands. ".." / "../evil" / "foo/../bar" carry the
	// literal dot-dot the ladder is built to defeat; "/etc/passwd" is an absolute escape; "a/b" an
	// embedded separator. None satisfy ^[a-z][a-z0-9-]{0,63}$ (rung 1) and none survive the store-prefix
	// re-verify (rung 3).
	for _, name := range []string{"..", "/etc/passwd", "a/b", "../evil", "foo/../bar"} {
		t.Run(name, func(t *testing.T) {
			s, dir := newStore(t)
			if err := s.Write(name, []byte("x = 1"), ""); err == nil {
				t.Fatalf("Write(%q) = nil, want refusal", name)
			}
			assertStoreEmpty(t, dir)
		})
	}
}

// ---- Rung 2: server pins the .formula.toml suffix (client sends a BARE name) ----

func TestWrite_SuffixGamesServerAppends(t *testing.T) {
	// A client that supplies its own suffix/extension is not trusted: the whole string is the bare name,
	// so any embedded dot fails the name rule. The server alone appends ".formula.toml".
	for _, name := range []string{"foo.formula.toml", "foo.formula", "foo.toml", "foo.evil"} {
		t.Run(name, func(t *testing.T) {
			s, dir := newStore(t)
			if err := s.Write(name, []byte("x = 1"), ""); !errors.Is(err, ErrInvalidName) {
				t.Fatalf("Write(%q) err = %v, want ErrInvalidName", name, err)
			}
			assertStoreEmpty(t, dir)
		})
	}
	t.Run("bare_name_gets_server_suffix", func(t *testing.T) {
		s, dir := newStore(t)
		if err := s.Write("foo", []byte("x = 1"), ""); err != nil {
			t.Fatalf("Write(foo): %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "foo.formula.toml")); err != nil {
			t.Fatalf("server did not append .formula.toml: %v", err)
		}
	})
}

// ---- Rung 4: os.Lstat symlink rejection (net-new) ----

func TestWrite_SymlinkTargetRefused(t *testing.T) {
	s, dir := newStore(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Plant a symlink where the write would land; the os.Lstat rung must detect it and refuse, so the
	// planted symlink cannot redirect the write outside the store.
	link := storeFile(dir, "evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("os.Symlink: %v", err)
	}
	if err := s.Write("evil", []byte("HOSTILE"), ""); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Write over symlink err = %v, want ErrSymlink", err)
	}
	after, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "ORIGINAL" {
		t.Fatalf("symlink target was written through: got %q", after)
	}
}

func TestWrite_SymlinkedParentDirRefused(t *testing.T) {
	// A leaf-only Lstat would miss this: the store's `formulas` dir itself is a symlink to an outside
	// directory. Without the resolved-component check, Write("victim") lands its bytes OUTSIDE the store
	// (a live T1 escape). The os.Lstat walk over parent components must refuse it.
	root := t.TempDir()
	storeParent := filepath.Join(root, ".agentfactory", "store")
	if err := os.MkdirAll(storeParent, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(storeParent, "formulas")); err != nil {
		t.Fatalf("os.Symlink: %v", err)
	}
	s := New(root)
	if err := s.Write("victim", []byte("PWNED"), ""); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Write through symlinked parent err = %v, want ErrSymlink", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "victim.formula.toml")); !os.IsNotExist(err) {
		t.Fatalf("write escaped into the symlinked parent dir: %v", err)
	}
}

func TestRead_SymlinkRefused(t *testing.T) {
	s, dir := newStore(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, storeFile(dir, "peek")); err != nil {
		t.Fatalf("os.Symlink: %v", err)
	}
	if _, err := s.Read("peek"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Read via symlink err = %v, want ErrSymlink", err)
	}
}

// ---- Rung 5: body bounds + encoding (T5), byte-transparent ----

func TestWrite_OversizedBodyRefused(t *testing.T) {
	// 1 MiB cap is the store-side half of T5 (the handler adds http.MaxBytesReader in Phase 3).
	s, _ := newStore(t)
	over := bytes.Repeat([]byte("a"), (1<<20)+1)
	if err := s.Write("big", over, ""); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Write(1MiB+1) err = %v, want ErrTooLarge", err)
	}
	exact := bytes.Repeat([]byte("a"), 1<<20)
	if err := s.Write("big", exact, ""); err != nil {
		t.Fatalf("Write(exactly 1MiB) = %v, want accepted", err)
	}
}

func TestWrite_NonUTF8BodyRefused(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Write("bad", []byte{0xff, 0xfe, 0xfd}, ""); !errors.Is(err, ErrNotUTF8) {
		t.Fatalf("Write(non-utf8) err = %v, want ErrNotUTF8", err)
	}
	assertStoreEmpty(t, dir)
}

func TestWrite_NULByteBodyRefused(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Write("nul", []byte("a\x00b"), ""); !errors.Is(err, ErrHasNUL) {
		t.Fatalf("Write(NUL) err = %v, want ErrHasNUL", err)
	}
	assertStoreEmpty(t, dir)
}

func TestWrite_ByteTransparency_NoNewlineFixup(t *testing.T) {
	s, dir := newStore(t)
	// Distinct conforming names (the store rule forbids underscores) carrying deliberately different
	// trailing bytes — a "helpful" writer that appended or normalized a newline would corrupt at least one.
	cases := []struct {
		name string
		body []byte
	}{
		{"bt-no-trailing", []byte("x = 1")},
		{"bt-single-lf", []byte("x = 1\n")},
		{"bt-crlf", []byte("x = 1\r\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := s.Write(c.name, c.body, ""); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got, err := os.ReadFile(storeFile(dir, c.name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, c.body) {
				t.Fatalf("byte-transparency violated: stored %q, want %q (no newline fixup/normalization)", got, c.body)
			}
		})
	}
}

// ---- Rung 6: atomic temp + fsync + rename, mode 0o644 ----

func TestAtomicWrite_ConcurrentWritesProduceValidFile(t *testing.T) {
	// Drive the store's INTERNAL atomic writer directly (mirror internal/fsutil/atomic_test.go:52-83):
	// a CAS-gated Write would 409 concurrent same-base writers, but the atomicity guarantee under test
	// is the temp+fsync+rename primitive itself. Distinct byte-values/lengths make a torn write visible.
	dir := t.TempDir()
	path := filepath.Join(dir, "f.formula.toml")
	payloads := [][]byte{
		bytes.Repeat([]byte("a"), 100),
		bytes.Repeat([]byte("b"), 200),
		bytes.Repeat([]byte("c"), 150),
		bytes.Repeat([]byte("d"), 300),
		bytes.Repeat([]byte("e"), 250),
	}
	var wg sync.WaitGroup
	for _, p := range payloads {
		wg.Add(1)
		go func(data []byte) {
			defer wg.Done()
			if err := atomicWrite(path, data, 0o644); err != nil {
				t.Errorf("atomicWrite: %v", err)
			}
		}(p)
	}
	wg.Wait()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, p := range payloads {
		if bytes.Equal(got, p) {
			return
		}
	}
	t.Errorf("final file matches no single payload: %d bytes", len(got))
}

func TestWrite_NoStrayTmpFilesOnSuccess(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Write("clean", []byte("x = 1"), ""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "clean.formula.toml" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected only clean.formula.toml, got %v (stray temp?)", names)
	}
}

func TestWrite_ModeIs0o644(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Write("perm", []byte("x = 1"), ""); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(storeFile(dir, "perm"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %o, want 0o644", info.Mode().Perm())
	}
}

// ---- Rung 7: sha256 compare-and-swap ----

func TestWrite_CAS_CreateSucceedsWhenAbsent(t *testing.T) {
	s, dir := newStore(t)
	body := []byte("x = 1")
	if err := s.Write("new", body, ""); err != nil {
		t.Fatalf("create with empty base: %v", err)
	}
	got, _ := os.ReadFile(storeFile(dir, "new"))
	if !bytes.Equal(got, body) {
		t.Errorf("stored %q, want %q", got, body)
	}
}

func TestWrite_CAS_CreateOnlyRefusesExisting(t *testing.T) {
	s, dir := newStore(t)
	seed := []byte("original = true")
	if err := os.WriteFile(storeFile(dir, "dup"), seed, 0o644); err != nil {
		t.Fatal(err)
	}
	// Empty base => create-only => must fail because the file already exists, and must NOT clobber it.
	if err := s.Write("dup", []byte("clobber = true"), ""); !errors.Is(err, ErrExists) {
		t.Fatalf("create over existing err = %v, want ErrExists", err)
	}
	after, _ := os.ReadFile(storeFile(dir, "dup"))
	if !bytes.Equal(after, seed) {
		t.Fatalf("create-only clobbered the file: got %q", after)
	}
}

func TestWrite_CAS_StaleBaseConflictDiskUntouched(t *testing.T) {
	s, dir := newStore(t)
	payloadA := []byte("version = 1")
	base := sha256hex(payloadA) // the hash the caller read the file at (their load point)
	if err := os.WriteFile(storeFile(dir, "race"), payloadA, 0o644); err != nil {
		t.Fatal(err)
	}
	// A concurrent writer changed the file between the caller's load and save.
	payloadB := []byte("version = 2 (someone else)")
	if err := os.WriteFile(storeFile(dir, "race"), payloadB, 0o644); err != nil {
		t.Fatal(err)
	}
	// Our save with the STALE base must 409 and leave payloadB untouched.
	err := s.Write("race", []byte("version = 3 (mine)"), base)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale-base Write err = %v, want ErrConflict", err)
	}
	after, _ := os.ReadFile(storeFile(dir, "race"))
	if !bytes.Equal(after, payloadB) {
		t.Fatalf("conflicting Write mutated disk: got %q, want %q (untouched)", after, payloadB)
	}
}

func TestWrite_CAS_MatchingBaseOverwrites(t *testing.T) {
	s, dir := newStore(t)
	payloadA := []byte("count = 1")
	if err := os.WriteFile(storeFile(dir, "cas"), payloadA, 0o644); err != nil {
		t.Fatal(err)
	}
	payloadB := []byte("count = 2")
	if err := s.Write("cas", payloadB, sha256hex(payloadA)); err != nil {
		t.Fatalf("matching-base overwrite: %v", err)
	}
	after, _ := os.ReadFile(storeFile(dir, "cas"))
	if !bytes.Equal(after, payloadB) {
		t.Fatalf("overwrite stored %q, want %q", after, payloadB)
	}
}

func TestWrite_CAS_BaseOnMissingFileIsNotFound(t *testing.T) {
	s, _ := newStore(t)
	if err := s.Write("ghost", []byte("x = 1"), sha256hex([]byte("whatever"))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-empty base on absent file err = %v, want ErrNotFound", err)
	}
}

// ---- List / Read ----

func TestList_SynthesizedNonConformingFault(t *testing.T) {
	s, dir := newStore(t)
	if err := os.WriteFile(storeFile(dir, "good"), []byte("ok = 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A synthesized non-conforming filename (uppercase + underscore) — this cannot occur in the live
	// store (all 29 names conform), so a temp fixture is the ONLY way to exercise the fault-card path.
	badBody := []byte("legacy = true")
	if err := os.WriteFile(filepath.Join(dir, "Bad_Name.formula.toml"), badBody, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byName := map[string]bool{}
	for _, e := range entries {
		byName[e.Name] = e.ReadOnly
	}
	ro, ok := byName["good"]
	if !ok || ro {
		t.Errorf("good: got readOnly=%v present=%v, want editable", ro, ok)
	}
	ro, ok = byName["Bad_Name"]
	if !ok || !ro {
		t.Errorf("Bad_Name: got readOnly=%v present=%v, want read-only fault", ro, ok)
	}
	// The fault file must never be rewritten or decorated.
	after, _ := os.ReadFile(filepath.Join(dir, "Bad_Name.formula.toml"))
	if !bytes.Equal(after, badBody) {
		t.Errorf("List rewrote the non-conforming file: got %q", after)
	}
}

func TestList_StripsSuffixAndSkipsNonFormula(t *testing.T) {
	s, dir := newStore(t)
	for _, n := range []string{"alpha", "beta"} {
		if err := os.WriteFile(storeFile(dir, n), []byte("x = 1"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("not a formula"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name] = true
	}
	if !got["alpha"] || !got["beta"] {
		t.Errorf("expected alpha and beta listed, got %v", got)
	}
	if got["readme"] || got["readme.md"] {
		t.Errorf("non-formula file leaked into List: %v", got)
	}
}

func TestList_MissingStoreDirIsEmpty(t *testing.T) {
	// A root with no store dir yet lists cleanly as empty (no error), mirroring feedback.latestVersion's
	// graceful os.ReadDir-error handling.
	s := New(t.TempDir())
	entries, err := s.List()
	if err != nil {
		t.Fatalf("List on missing store dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want empty list, got %v", entries)
	}
}

func TestRead_RoundTripByteIdentical(t *testing.T) {
	s, dir := newStore(t)
	body := []byte("name = \"x\"\nsteps = 3")
	if err := os.WriteFile(storeFile(dir, "rt"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read("rt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("Read returned %q, want %q", got, body)
	}
}

func TestRead_AbsentIsNotFound(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Read("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read(absent) err = %v, want ErrNotFound", err)
	}
}

func TestRead_InvalidNameRefused(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Read("../etc/passwd"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("Read(traversal) err = %v, want ErrInvalidName", err)
	}
}

// assertStoreEmpty fails if any regular file leaked into the store dir after a refused write.
func assertStoreEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("refused write leaked files into store: %v", names)
	}
}

// ---- T2 (PRRT_kwDORt0n_M6Pw23V): the sha256 CAS must be an atomic compare-and-swap ----

// TestWrite_ConcurrentSameBase_ExactlyOneWinner pins the guarantee the code advertises and the
// design states: C-8 (design-doc.md:77) "sha256 compare-and-swap PREVENTS silent reversion of
// hand-edits"; Decision 2 (:298) "CAS is what makes AC-2 true under concurrent hand-edits";
// Phase-1c AC (:428) "write between load and save → 409, disk file untouched".
//
// Write reads the file, compares hashHex(cur)==baseHash, then renames — with no interlock. N writers
// holding the SAME baseHash therefore all pass the precondition and all commit; the last silently
// clobbers the rest. Exactly one must win; the others must get ErrConflict.
//
// This is a filesystem TOCTOU, NOT a shared-memory data race: `go test -race` will never flag it.
// The only honest detector is the success/conflict census below.
func TestWrite_ConcurrentSameBase_ExactlyOneWinner(t *testing.T) {
	const writers = 8
	const iterations = 50

	for i := 0; i < iterations; i++ {
		s, _ := newStore(t)
		base := []byte("v = 0\n")
		if err := s.Write("race", base, ""); err != nil {
			t.Fatalf("seed write: %v", err)
		}
		baseHash := hashHex(base)

		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		successes, conflicts, others := 0, 0, 0

		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				<-start // every writer races from the same baseHash
				err := s.Write("race", []byte{byte('a' + w), '\n'}, baseHash)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					successes++
				case errors.Is(err, ErrConflict):
					conflicts++
				default:
					others++
					t.Errorf("iteration %d writer %d: unexpected error %v", i, w, err)
				}
			}(w)
		}
		close(start)
		wg.Wait()

		if successes != 1 || conflicts != writers-1 || others != 0 {
			t.Fatalf("iteration %d: successes=%d conflicts=%d other=%d; want exactly 1 success and %d ErrConflict — "+
				"a non-atomic read-compare-write let concurrent same-base writers both commit (lost update)",
				i, successes, conflicts, others, writers-1)
		}

		// The survivor's bytes must be on disk intact — never a torn or interleaved write.
		got, err := os.ReadFile(storeFile(dirOf(t, s), "race"))
		if err != nil {
			t.Fatalf("iteration %d: ReadFile: %v", i, err)
		}
		if len(got) != 2 || got[1] != '\n' || got[0] < 'a' || got[0] >= 'a'+writers {
			t.Fatalf("iteration %d: on-disk bytes %q match no single writer's payload", i, got)
		}
	}
}

// dirOf recovers the store's formulas dir for on-disk assertions (newStore returns it, but the
// concurrency loop above rebuilds a store per iteration).
func dirOf(t *testing.T, s *Store) string {
	t.Helper()
	return s.storeDir()
}
