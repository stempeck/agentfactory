// The operator half of the vault: getting it OUT of a container, and saying how long it has been
// since anyone did. Phases 1-5 gave the agent a durable place to record learnings; every one of
// those phases is undone by a single `docker rm`, because the vault is container-local and
// git-invisible (`.gitignore:48`'s `.agentfactory/*` catch-all, whose re-inclusion allowlist omits
// memory). ADR-019 forbids closing that hole the obvious way — no af change may REQUIRE recreating
// an existing container — so the residual stays open (design R1 / GAP-20) and this file is the
// consolation: a documented way out, plus a loud reminder at the two moments an operator can act.
//
// The export is deliberately not an archive FORMAT. It copies the vault's bytes and nothing else,
// because the vault is hand-editable by design (AC-515-5, Obsidian) and a backup that
// round-tripped notes through Parse/Emit would silently normalize an operator's own edits out of
// their own copy — with T-EDIT then asserting the opposite of what the backup does.
package cmd

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
)

const (
	// vaultArchiveRoot prefixes every member so `tar xzf` lands the whole vault in one directory
	// instead of scattering per-agent folders across whatever the operator's cwd happened to be.
	// It also makes the untarred tree a drop-in for `docker cp memory/. <c>:<root>/.agentfactory/`,
	// which is the return leg USING_MEMORY.md documents.
	vaultArchiveRoot = "memory"

	// memoryIndexFileName is the derived per-agent index the core regenerates on every write. It
	// is spelled here a second time because internal/memory keeps its own `indexStem` unexported
	// and offers no accessor (store.go:20-24); install.go:647 already carries the same literal.
	// TestMemoryExport_OmitsTheDerivedIndex compares this against the ARTIFACT the core writes,
	// so a drift in either copy fails there rather than silently.
	memoryIndexFileName = "index.md"

	// memoryExportStateVersion versions the on-disk marker. Every durable record in this tree
	// carries one (memoryNagStateVersion, recovery.go:267-271).
	memoryExportStateVersion = 1

	// memoryExportStaleAfter is when the staleness line stops merely reporting and starts telling
	// the operator what to do. A week, for the same reason memoryNagRepeatAfter is a week: the
	// thing being asked about is a habit, and a remedy repeated on every single `af up` becomes
	// scenery that the one operator who needed it has already learned to skip.
	memoryExportStaleAfter = 7 * 24 * time.Hour
)

// memoryExportState is the "when did anyone last get this vault out of here" record. One file,
// factory-wide: unlike the nag's cap there is no per-agent dimension, because the tarball is the
// whole vault and a per-agent marker could only ever say something no verb can act on.
type memoryExportState struct {
	V  int    `json:"v"`
	At string `json:"at"`
}

// Factory root, never an agent worktree — the same rule and the same reason as memoryNagStateDir
// (memory_nag.go:98-108) and the recovery breaker (recovery.go:232-234). A worktree-resident
// marker evaporates with the worktree, so the staleness line would reset to "never" after every
// teardown and an operator who exports diligently would be nagged forever. There is no .runtime
// constructor in internal/config by design (that package owns .agentfactory/* paths only), so the
// path is composed here like every sibling.
func memoryExportStatePath(root string) string {
	return filepath.Join(root, ".runtime", "memory_export.json")
}

// lastMemoryExportAt reads the marker. Any failure — absent, unreadable, unparseable, or a
// timestamp in a format this binary does not speak — reads as "never exported". That is the
// permissive direction loadMemoryNagState takes (memory_nag.go:109-113) and the correct one here
// for the same asymmetry: the cost of a false "never" is one extra reminder, while the cost of a
// false "recently" is an operator who believes a backup exists.
func lastMemoryExportAt(root string) (time.Time, bool) {
	data, err := os.ReadFile(memoryExportStatePath(root))
	if err != nil {
		return time.Time{}, false
	}
	var st memoryExportState
	if err := json.Unmarshal(data, &st); err != nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, st.At)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// saveMemoryExportState arms the alarm-silencer. Its caller must invoke it only after a stream
// that closed cleanly: recording an export that failed halfway is precisely the "reports a backup
// that does not exist, which is worse than no backup" failure the export stub was written to
// avoid, moved from the verb into the warning that reads it.
func saveMemoryExportState(root string, at time.Time) error {
	if err := os.MkdirAll(filepath.Dir(memoryExportStatePath(root)), 0o755); err != nil {
		return fmt.Errorf("creating the memory export state dir: %w", err)
	}
	data, err := json.MarshalIndent(memoryExportState{
		V:  memoryExportStateVersion,
		At: at.UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(memoryExportStatePath(root), data, 0o644)
}

// writeVaultTarball streams the whole vault to w as gzip-wrapped tar and reports which paths it
// declined to archive. stdlib only: ADR-013 freezes go.mod at two direct requires and this is the
// first archive producer in the tree, so the choice is between archive/tar and a dependency an
// agent is not permitted to add.
//
// The two error classes are separated on purpose. A file this cannot READ is skipped and named —
// one note with hostile permissions must not cost an operator the other forty. A failure to WRITE
// aborts, because a truncated tarball that exits 0 is the silent-partial-backup this whole file
// exists to prevent.
func writeVaultTarball(w io.Writer, root string) (skipped []string, err error) {
	vault, err := walkableVaultRoot(root)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	walkErr := filepath.WalkDir(vault, func(path string, d fs.DirEntry, entryErr error) error {
		if entryErr != nil {
			if path == vault {
				// A vault directory that was never created is an empty archive, not a failure: a
				// factory installed before Phase 3 has none, and the fresh-factory case is the one
				// where establishing the backup habit matters most. Every OTHER root failure —
				// unreadable, a bad mount, an I/O error — aborts, because those are the cases where
				// the vault holds notes this cannot see, and an empty archive with a clean exit
				// would arm the staleness marker and silence the warning that names them.
				if errors.Is(entryErr, fs.ErrNotExist) {
					return nil
				}
				return entryErr
			}
			skipped = append(skipped, path)
			return nil
		}

		rel, relErr := filepath.Rel(vault, path)
		if relErr != nil || rel == "." {
			return nil
		}
		// Producer-side Zip-Slip guard. Nothing under a WalkDir root can reach it today; it is
		// here because the alternative to a two-line check is trusting that forever.
		if strings.HasPrefix(rel, "..") {
			skipped = append(skipped, path)
			return nil
		}
		// index.md is DERIVED — RebuildIndex rewrites it on every write, and countVaultNotes
		// already excludes it for that reason. It is left out of the archive because the archive's
		// documented destination is `af memory import`, which ingests every .md it is pointed at:
		// shipping the index would seed a note whose body is a listing of the other notes, served
		// to the next session as a recorded observation and re-indexed on the way, so every
		// crossing would compound. Nothing is lost — the first write on the far side rebuilds it.
		if filepath.Base(rel) == memoryIndexFileName {
			return nil
		}
		name := vaultArchiveRoot + "/" + filepath.ToSlash(rel)

		info, infoErr := d.Info()
		if infoErr != nil {
			skipped = append(skipped, path)
			return nil
		}

		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{
				Name:     name + "/",
				Mode:     int64(info.Mode().Perm()),
				ModTime:  info.ModTime(),
				Typeflag: tar.TypeDir,
			})
		}
		// d.Type() carries lstat semantics, so a symlink reports as a symlink rather than as
		// whatever it points at — the same guard and the same reason as store.go:238-251. The
		// vault is operator-editable by design, so a link inside it is an ordinary input; an
		// archiver that followed one would pack arbitrary host files into a file the operator
		// reasonably believes holds their agents' learnings.
		if !d.Type().IsRegular() {
			skipped = append(skipped, path)
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			skipped = append(skipped, path)
			return nil
		}
		// Headers are built by hand rather than from tar.FileInfoHeader because that helper fills
		// Uid/Gid/Uname/Gname from the container's passwd on Linux, which both leaks the build
		// identity and makes extraction on the host depend on ids that do not exist there.
		if hdrErr := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     int64(info.Mode().Perm()),
			Size:     int64(len(data)),
			ModTime:  info.ModTime(),
			Typeflag: tar.TypeReg,
		}); hdrErr != nil {
			return hdrErr
		}
		_, writeErr := tw.Write(data)
		return writeErr
	})
	if walkErr != nil {
		// Closed on the abort path too, and their errors deliberately discarded: the walk error is
		// the one the operator needs, and a close error here would only describe the same broken
		// stream a second time.
		_ = tw.Close()
		_ = gz.Close()
		return skipped, fmt.Errorf("writing the vault archive: %w", walkErr)
	}
	// Reverse order, and both errors checked: a swallowed gzip Close drops the trailer, and the
	// resulting stream fails at `tar tz` time on the host rather than here where it can be said.
	if closeErr := tw.Close(); closeErr != nil {
		return skipped, fmt.Errorf("closing the vault archive: %w", closeErr)
	}
	if closeErr := gz.Close(); closeErr != nil {
		return skipped, fmt.Errorf("closing the vault archive: %w", closeErr)
	}
	return skipped, nil
}

// walkableVaultRoot returns the directory a walk of the vault must start from.
//
// filepath.WalkDir lstats its root and refuses to descend a symlink, so a vault that IS a symlink
// walks exactly once, matches no file, and yields a valid, EMPTY archive with a nil error — a
// silent no-backup that then arms the staleness marker and silences the very warning that would
// have caught it. That shape is not hypothetical: a host-mounted vault (AF_MEMORY_HOST_DIR in
// quickdocker.sh) is a symlink to the mount point. Resolving the root here keeps every member name
// relative to it, so the archive layout is unchanged.
//
// A vault that exists but is not a directory is an ERROR rather than an empty archive, for the
// asymmetry this whole file turns on: an empty tarball and a clean exit is indistinguishable from
// a factory that has recorded nothing.
func walkableVaultRoot(root string) (string, error) {
	vault := config.MemoryDir(root)
	resolved, err := filepath.EvalSymlinks(vault)
	if err != nil {
		// Absent is the fresh-factory case and stays an empty archive; the walk below reports it.
		// A vault that EXISTS but does not resolve is the opposite — a detached mount or a broken
		// link — and it has to be caught HERE rather than by the walk: WalkDir lstats a dangling
		// symlink successfully, matches nothing, and returns nil, so the root-error branch below
		// never fires and the archive comes back clean and empty.
		if _, lstatErr := os.Lstat(vault); lstatErr == nil {
			return "", fmt.Errorf("the vault path %s exists but does not resolve — refusing to write "+
				"an archive that would silently contain nothing", vault)
		}
		return vault, nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return vault, nil
	}
	if !info.IsDir() {
		return "", fmt.Errorf("the vault path %s is not a directory — refusing to write an archive "+
			"that would silently contain nothing", vault)
	}
	return resolved, nil
}

// countVaultNotes counts note files across every agent's vault, index.md excluded because it is
// derived (store.go RebuildIndex) and counting it would overstate what is at risk by one per agent.
//
// It walks the directory rather than iterating agents.json on purpose. The number answers "how
// much do I lose if this container dies", and a roster-scoped count would silently omit the vault
// of an agent that has since been removed from the roster — which is exactly the vault whose loss
// nobody would notice. Any failure counts as zero, for PreservedLine's reason (report.go:25-27): a
// vault that cannot be counted is still a vault that survived, and no counting failure may cost
// the launch or the teardown that called this.
func countVaultNotes(root string) int {
	// Same resolution as the archiver, and load-bearing for the same reason: a symlinked vault
	// counted through an unresolved root reports zero, and zero is the one value that makes this
	// warning silent altogether.
	vault, err := walkableVaultRoot(root)
	if err != nil {
		return 0
	}
	count := 0
	_ = filepath.WalkDir(vault, func(path string, d fs.DirEntry, entryErr error) error {
		if entryErr != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if filepath.Ext(path) != ".md" || filepath.Base(path) == memoryIndexFileName {
			return nil
		}
		count++
		return nil
	})
	return count
}

func pluralNotes(n int) string {
	if n == 1 {
		return "note"
	}
	return "notes"
}

// warnVaultExportStaleness is the R1 guard at the two moments an operator can act on it: the
// preflight of `af up`, and factory-wide `af down --all`. It joins the warn-to-stderr preflight
// cluster (checkGitHooksExecutable, warnUnknownWatchdogAgents, warnUnobservableAgents) and shares
// its two rules — it returns nothing, and no failure inside it may block a launch or a teardown.
//
// Silent at zero notes, for PreservedLine's reason (report.go:23-24): with nothing recorded there
// is nothing at risk, so a factory that has never used memory sees no new output at all.
func warnVaultExportStaleness(w io.Writer, root string, now time.Time) {
	notes := countVaultNotes(root)
	if notes == 0 {
		return
	}

	when := "never"
	stale := true
	if at, ok := lastMemoryExportAt(root); ok {
		// int(hours/24) and an injected clock, matching memoryAttribution (memory.go:950-956):
		// a test moves the clock rather than sleeping, which ADR-018 hermeticity requires.
		switch days := int(now.Sub(at).Hours() / 24); {
		case days <= 0:
			when = "today"
		case days == 1:
			when = "1 day ago"
		default:
			when = fmt.Sprintf("%d days ago", days)
		}
		stale = now.Sub(at) >= memoryExportStaleAfter
	}

	fmt.Fprintf(w, "vault: %d %s; last export: %s\n", notes, pluralNotes(notes), when)
	if stale {
		fmt.Fprintln(w, "  remediation: the vault is container-local — back it up with "+
			"`af memory export > vault.tgz` (see USING_MEMORY.md, \"Getting the vault onto the host\")")
	}
}
