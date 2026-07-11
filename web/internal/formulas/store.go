// Package formulas is the web module's server-mediated read/write boundary over the live formula store
// at <root>/.agentfactory/store/formulas/*.formula.toml. It is the SINGLE home of the write-path
// containment ladder, sha256 compare-and-swap (CAS), and atomic temp+fsync+rename — the enforcement
// point for the #502 design's write-path threats T1 (path escape via formula name) and T5
// (oversized/hostile body). Files are the entire persistent model, so writes are byte-transparent (no
// trailing-newline fixup, no normalization), atomic (never a torn read), and create+conditional-overwrite
// only — no delete (ADR-017: store formulas are customer data).
//
// This file re-implements the small af-core slices the web module needs. It deliberately does NOT import
// internal/… — Go's internal seal plus the separate go.mod (web/go.mod has zero require directives) make
// that compiler-impossible, and the duplication is the point (a compile error, not a lint, catches this
// module drifting from the root module's rules). Same doctrine as web/internal/exec/validate.go:10-18,
// web/internal/rendezvous/rendezvous.go, web/internal/config/root.go.
package formulas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	dotDir     = ".agentfactory" // mirrors web/internal/config/settings.go:35
	suffix     = ".formula.toml" // server APPENDS this; the client sends a BARE name (rung 2)
	maxBodyLen = 1 << 20         // 1 MiB body cap (rung 5); the handler adds http.MaxBytesReader in P3
)

// nameRE is the CREATE name rule (rung 1): lowercase-only, hyphen-only, with the 64-char cap folded into
// the regex via {0,63}. It is DELIBERATELY stricter than every existing precedent and reuses none of
// them: feedback.idRE (`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`, writer.go:196) permits uppercase and a dot (a
// traversal foothold) and is unbounded; exec.ValidateAgentName (`^[a-zA-Z][a-zA-Z0-9_-]*$` with a
// separate len>64 guard, validate.go:21/34) permits uppercase and underscore. The design folds the cap
// into a lowercase rule (design-doc.md:171; the prototype rule roster.html:305 is the same shape but
// UNBOUNDED — the {0,63} cap is the design's addition).
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// Sentinel errors. A future HTTP handler (Phase 3) maps these to status codes with errors.Is, exactly as
// web/internal/config exposes ErrNotWritable (settings.go). ErrConflict/ErrExists are the 409-class CAS
// outcomes; the containment rungs each have a distinct sentinel so a caller can tell WHY a write refused.
var (
	ErrInvalidName = errors.New("invalid formula name")                      // rung 1
	ErrOutOfStore  = errors.New("resolved path escapes the store directory") // rung 3
	ErrSymlink     = errors.New("formula path is a symlink")                 // rung 4
	ErrTooLarge    = errors.New("formula body exceeds 1 MiB")                // rung 5
	ErrNotUTF8     = errors.New("formula body is not valid UTF-8")           // rung 5
	ErrHasNUL      = errors.New("formula body contains a NUL byte")          // rung 5
	ErrNotFound    = errors.New("formula not found")                         // rung 7 (non-empty base, absent)
	ErrExists      = errors.New("formula already exists")                    // rung 7 (empty base ⇒ create-only)
	ErrConflict    = errors.New("formula changed on disk since it was read") // rung 7 (base-hash mismatch ⇒ 409)
)

// Store reads and writes formula TOMLs under <root>/.agentfactory/store/formulas. It carries the
// factory root, resolved server-side by the caller (mirrors feedback.New(root, …) / config.New(root, …)),
// and the mutex that makes Write's compare-and-swap atomic.
//
// mu serializes Write's read-compare-commit. net/http serves every PUT on its own goroutine and the
// Server holds ONE *Store for the process (cmd/afweb/main.go), so without it two PUTs carrying the
// same base_sha256 both pass the precondition and both commit — the second silently reverting the
// first. Reads need no lock: the commit is an atomic rename, so a reader sees old bytes or new, never
// a torn file.
//
// Scope, stated honestly: this is an IN-PROCESS interlock. It cannot serialize another OS process —
// `af install` regenerating agents, or the browser's File System Access save path — both of which
// write these same files. Those remain bounded by the CAS precondition alone.
type Store struct {
	mu   sync.Mutex
	root string
}

// New builds a Store over the factory root.
func New(root string) *Store { return &Store{root: root} }

// Entry is one row of List: the bare name plus a read-only fault flag. A filename that does not conform
// to nameRE is surfaced ReadOnly (design-doc.md:171, :229-231) — listed but never editable, never
// rewritten, never decorated.
type Entry struct {
	Name     string
	ReadOnly bool
}

// storeDir builds the store path LOCALLY: the web config package exposes no StoreDir/FormulasDir helper
// (its path helpers are unexported, settings.go:35-40), and the root module's are behind the seal.
func (s *Store) storeDir() string { return filepath.Join(s.root, dotDir, "store", "formulas") }

// List enumerates *.formula.toml, strips the suffix, and flags non-conforming names read-only. A missing
// store dir lists cleanly as empty (mirrors feedback.latestVersion's graceful os.ReadDir handling).
func (s *Store) List() ([]Entry, error) {
	entries, err := os.ReadDir(s.storeDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		name := strings.TrimSuffix(e.Name(), suffix)
		out = append(out, Entry{Name: name, ReadOnly: !nameRE.MatchString(name)})
	}
	return out, nil
}

// Read returns the on-disk bytes for a bare name, byte-transparent (no transformation). It applies the
// same name/containment/symlink rungs as Write before touching disk.
func (s *Store) Read(name string) ([]byte, error) {
	path, err := s.resolve(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// Write is a sha256 compare-and-swap. baseHash == "" ⇒ create-only (the file must not already exist);
// a non-empty baseHash ⇒ overwrite ONLY if the current on-disk content hashes to baseHash. A mismatch is
// ErrConflict with the disk file untouched. No delete, no rename-of-existing.
//
// The ordered containment ladder runs cheapest-decisive-first, and every rejection precedes the atomic
// commit: name rule (1) → server-appended suffix (2) → store-prefix re-verify (3) → os.Lstat symlink
// reject (4) → body bounds/encoding (5) → sha256 CAS precondition (7) → atomic temp+fsync+rename (6, the
// commit). Rungs 1–5 are pure; s.mu is taken for rung 7 + the commit, which together must be atomic or
// the CAS is a read-compare-write and concurrent same-base writers lose updates.
func (s *Store) Write(name string, content []byte, baseHash string) error {
	path, err := s.resolve(name) // rungs 1–4
	if err != nil {
		return err
	}
	if err := validateBody(content); err != nil { // rung 5
		return err
	}

	// Everything below is the compare-and-swap. It is one critical section: the precondition is only
	// meaningful if no other goroutine can commit between the compare and this write.
	s.mu.Lock()
	defer s.mu.Unlock()

	// rung 7 — compare the current on-disk content against baseHash BEFORE any write.
	cur, statErr := os.ReadFile(path)
	if baseHash == "" {
		// create-only: the file must not already exist.
		if statErr == nil {
			return ErrExists
		}
		if !os.IsNotExist(statErr) {
			return statErr
		}
	} else {
		if statErr != nil {
			if os.IsNotExist(statErr) {
				return ErrNotFound
			}
			return statErr
		}
		if hashHex(cur) != baseHash {
			return ErrConflict // 409-class; disk untouched
		}
	}
	return atomicWrite(path, content, 0o644) // rung 6
}

// resolve runs rungs 1–4: validate the name, append the server-pinned suffix (2), join under storeDir and
// re-verify the cleaned path is still store-prefixed (3, defeats `..`), then os.Lstat symlink-reject (4).
func (s *Store) resolve(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	dir := s.storeDir()
	clean := filepath.Clean(filepath.Join(dir, name+suffix)) // rung 2: server appends the suffix
	if clean != dir && !strings.HasPrefix(clean, dir+string(os.PathSeparator)) {
		return "", ErrOutOfStore // rung 3: unreachable given rung 1, kept as defense-in-depth
	}
	if err := s.rejectSymlinkComponents(clean); err != nil { // rung 4 (net-new)
		return "", err
	}
	return clean, nil
}

// rejectSymlinkComponents implements rung 4: reject if the target OR any resolved path component below
// the factory root is a symlink (IMPLREADME rung 4 — "the target or a resolved component"). A leaf-only
// os.Lstat would miss a planted symlinked parent dir (e.g. a `formulas` symlink), which redirects the
// write outside the store — the exact T1 escape this ladder exists to defeat. The walk starts BELOW
// s.root: the factory root is caller-supplied trusted infrastructure and may legitimately traverse
// system symlinks, whereas everything under it (.agentfactory/store/formulas/…) is store territory an
// attacker could plant in. A component that does not yet exist stops the walk cleanly — atomicWrite's
// MkdirAll then materializes the remainder as real directories, and os.Rename replaces (never writes
// through) whatever name sits at the leaf.
func (s *Store) rejectSymlinkComponents(target string) error {
	rel, err := filepath.Rel(s.root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ErrOutOfStore
	}
	prefix := s.root
	for _, comp := range strings.Split(rel, string(os.PathSeparator)) {
		prefix = filepath.Join(prefix, comp)
		fi, err := os.Lstat(prefix)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // nothing planted at or below here; the rest is created as real dirs
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return ErrSymlink
		}
	}
	return nil
}

func validateName(name string) error {
	if !nameRE.MatchString(name) { // the empty string is rejected here — no separate empty guard needed
		return fmt.Errorf("%w: %q must match [a-z][a-z0-9-]{0,63}", ErrInvalidName, name)
	}
	return nil
}

// validateBody enforces rung 5 and NOTHING else: size cap, UTF-8, no NUL. The bytes are otherwise sacred
// — no trailing-newline fixup, no normalization, no TOML round-trip (AC-2 byte-transparency).
func validateBody(content []byte) error {
	if len(content) > maxBodyLen {
		return ErrTooLarge
	}
	if !utf8.Valid(content) {
		return ErrNotUTF8
	}
	for _, b := range content {
		if b == 0x00 {
			return ErrHasNUL
		}
	}
	return nil
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// atomicWrite is fsutil.WriteFileAtomic (atomic.go:18-43) + rendezvous.WriteEndpoint (rendezvous.go:127)
// re-implemented under the module seal, with the NET-NEW f.Sync() (fsync). fsync is stricter than every
// precedent (all three skip it) and is intentional: it flushes the temp file's bytes to stable storage
// BEFORE the rename, so a crash between rename and the kernel's writeback cannot leave a formula
// (customer data) as a zero-length or torn file. Same-dir temp keeps the rename atomic.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil { // net-new durability rung
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
