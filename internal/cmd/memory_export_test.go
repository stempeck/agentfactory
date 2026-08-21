package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/memory"
)

// AC-515-5, operator half: `af memory export` is the only way a vault leaves a container that has
// no bind mount, so what this suite pins is not "a tarball appeared" but the four properties that
// make the tarball a BACKUP rather than a report of one — every agent present, bytes verbatim,
// nothing reachable from outside the vault, and a stream that either completes or errors.
//
// Why these tests drive runMemoryExport with their own *cobra.Command instead of execMemoryOut:
// execMemoryOut (memory_test.go:65-77) points SetOut and SetErr at the SAME buffer, so any byte
// the verb writes to stderr would land inside the "tarball" gzip.NewReader is then handed. The
// authority row below still goes through execMemoryOut, because there the merged buffer is the
// point — it proves a refusal emits zero bytes on either stream.

// exportToBuffers runs the real verb with separated streams and returns both.
func exportToBuffers(t *testing.T) (stdout, stderr *bytes.Buffer, err error) {
	t.Helper()
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return stdout, stderr, runMemoryExport(cmd, nil)
}

// tarMembers unpacks an export stream into name -> bytes, failing on any malformed layer. It
// deliberately does NOT tolerate a truncated stream: a tarball that unpacks "mostly" is the
// silent-partial-backup failure the export stub's own comment was written to prevent.
func tarMembers(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("export output is not a gzip stream (%d bytes): %v", len(archive), err)
	}
	defer gz.Close()

	members := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading the tar stream: %v", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading member %s: %v", hdr.Name, err)
		}
		members[hdr.Name] = body
	}
	return members
}

func asOperatorAt(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
}

func TestMemoryExport_WritesAReadableGzipTarballToStdout(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	id := seedNote(t, factoryRoot, "alice", memory.Note{
		ID:   "20260815-first-learning",
		Type: "gotcha",
		Body: "the tarball must carry this body verbatim",
	})
	asOperatorAt(t, factoryRoot)

	stdout, stderr, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("the success path wrote to stderr, which would corrupt a `af memory export > x.tgz` "+
			"round-trip for any caller merging the streams: %q", stderr.String())
	}

	members := tarMembers(t, stdout.Bytes())
	if len(members) == 0 {
		t.Fatal("the archive holds no members at all")
	}

	want := filepath.Join(config.AgentMemoryDir(factoryRoot, "alice"), id+".md")
	onDisk, readErr := os.ReadFile(want)
	if readErr != nil {
		t.Fatalf("reading the seeded note: %v", readErr)
	}
	var found bool
	for name, body := range members {
		if !strings.HasSuffix(name, id+".md") {
			continue
		}
		found = true
		// Byte-identity, not "contains the body": the vault is hand-editable by design
		// (AC-515-5), so an export that round-tripped through Parse/Emit would silently
		// normalize an operator's Obsidian edits out of their own backup.
		if !bytes.Equal(body, onDisk) {
			t.Errorf("member %s is not byte-identical to the file on disk\n got: %q\nwant: %q",
				name, body, onDisk)
		}
	}
	if !found {
		t.Errorf("the seeded note %s is absent from the archive; members: %v", id, memberNames(members))
	}
}

func TestMemoryExport_CoversEveryAgentVault(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "alice-note", Body: "alice learned a thing"})
	seedNote(t, factoryRoot, "bob", memory.Note{ID: "bob-note", Body: "bob learned a thing"})
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	members := tarMembers(t, stdout.Bytes())

	// export takes no --agent selector (memoryVerbFlagNames["export"] is empty) because the unit
	// of backup is the vault, not one agent's corner of it. A per-agent archive would mean an
	// operator who added an agent after their last backup silently loses it.
	for _, agent := range []string{"alice", "bob"} {
		if !slicesContainsSubstring(memberNames(members), agent+"/") {
			t.Errorf("%s's vault is missing from the archive; members: %v", agent, memberNames(members))
		}
	}
}

func TestMemoryExport_EmptyVaultProducesAValidEmptyArchive(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("an unwritten-to vault is a normal condition on a fresh factory, not an error: %v", err)
	}
	// Zero bytes would be indistinguishable from a crashed process to `tar tz`, and an error
	// would make the documented backup one-liner fail on exactly the factories that most need
	// an operator to establish the habit early.
	if members := tarMembers(t, stdout.Bytes()); len(members) != 0 {
		t.Errorf("an empty vault produced members: %v", memberNames(members))
	}
}

func TestMemoryExport_MemberPathsAreVaultRelativeAndCannotEscape(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "escape-check", Body: "body"})
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	for _, name := range memberNames(tarMembers(t, stdout.Bytes())) {
		if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
			t.Errorf("member %q is absolute; extracting the archive would write outside the operator's cwd", name)
		}
		if name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			t.Errorf("member %q traverses upward (Zip-Slip on the producer side)", name)
		}
	}
}

func TestMemoryExport_DoesNotFollowASymlinkOutOfTheVault(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "real-note", Body: "a real note"})

	// The vault is deliberately operator-editable (AC-515-5), so a symlink inside it is a
	// designed input rather than an attack. Following one would pack arbitrary host files into
	// something the operator reasonably believes is "my agents' learnings" — the same reason
	// store.go:238-251 refuses non-regular entries on the read side.
	secret := filepath.Join(t.TempDir(), "outside.md")
	writeFixtureFile(t, secret, "PRIVATE-HOST-CONTENT")
	link := filepath.Join(config.AgentMemoryDir(factoryRoot, "alice"), "escaped.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for name, body := range tarMembers(t, stdout.Bytes()) {
		if bytes.Contains(body, []byte("PRIVATE-HOST-CONTENT")) {
			t.Errorf("member %q carries the symlink target's contents from outside the vault", name)
		}
	}
}

func TestMemoryExport_UnreadableNoteDoesNotAbortTheArchive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	factoryRoot, _ := setupMemoryFixture(t)
	blocked := seedNote(t, factoryRoot, "alice", memory.Note{ID: "unreadable", Body: "cannot be read"})
	seedNote(t, factoryRoot, "bob", memory.Note{ID: "readable", Body: "bob's note must still be backed up"})

	blockedPath := filepath.Join(config.AgentMemoryDir(factoryRoot, "alice"), blocked+".md")
	if err := os.Chmod(blockedPath, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blockedPath, 0o644) })
	asOperatorAt(t, factoryRoot)

	stdout, stderr, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("one unreadable note must not cost the whole backup: %v", err)
	}
	members := tarMembers(t, stdout.Bytes())
	if !slicesContainsSubstring(memberNames(members), "readable.md") {
		t.Errorf("bob's readable note was lost with alice's unreadable one; members: %v", memberNames(members))
	}
	// Losing a note silently is the failure that makes a backup worse than none, so the skip
	// must be said out loud — on stderr, where it cannot enter the archive.
	if !strings.Contains(stderr.String(), "unreadable") && !strings.Contains(stderr.String(), blocked) {
		t.Errorf("the skipped note was not reported on stderr: %q", stderr.String())
	}
}

// The archive's documented destination is `af memory import`, which ingests every .md it is
// pointed at. index.md is derived, so shipping it would seed a note whose body is a listing of
// the other notes — served to the next session as a recorded observation, and re-indexed on the
// way in, so the junk compounds on every crossing.
//
// The index is produced here by the real verb and then read off disk, so this compares the
// ARTIFACT the core writes against what the archiver omitted rather than two copies of a literal.
func TestMemoryExport_OmitsTheDerivedIndex(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	t.Chdir(aliceDir)
	t.Setenv("AF_ROLE", "alice")
	t.Setenv("TMUX", "")
	if out, err := execMemoryOut(t, "add", "-s", "indexing", "-m", "a learning"); err != nil {
		t.Fatalf("af memory add: %v (out=%q)", err, out)
	}

	indexPath := filepath.Join(config.AgentMemoryDir(factoryRoot, "alice"), memoryIndexFileName)
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("the core wrote no %s, so this test would pass vacuously: %v", memoryIndexFileName, err)
	}
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	names := memberNames(tarMembers(t, stdout.Bytes()))
	if slicesContainsSubstring(names, memoryIndexFileName) {
		t.Errorf("the derived index is in the archive, so re-importing it would seed a note that "+
			"lists the other notes; members: %v", names)
	}
	if len(names) == 0 {
		t.Errorf("the archive lost the notes along with the index; members: %v", names)
	}
	// The staleness line reads through its own walker, and this is the only fixture in the suite
	// where an index actually exists — seedNote goes through memory.Write, which does not rebuild
	// one. Counting it here would tell the operator they have twice the notes they recorded, and
	// the number is the whole claim that line makes.
	if n := countVaultNotes(factoryRoot); n != 1 {
		t.Errorf("countVaultNotes read %d notes from a vault holding one note and its derived "+
			"index, want 1", n)
	}
}

// A vault that IS a symlink is the shape a host-mounted vault takes (AF_MEMORY_HOST_DIR), and
// filepath.WalkDir will not descend one. Unresolved, the export would produce a valid, EMPTY
// archive, exit 0, and arm the marker — silencing the very warning that exists to catch it.
func TestMemoryExport_SymlinkedVaultRootIsArchivedNotSilentlySkipped(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	real := t.TempDir()
	vault := config.MemoryDir(factoryRoot)
	if err := os.RemoveAll(vault); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, vault); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "mounted-note", Body: "recorded onto the host mount"})
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("export through a symlinked vault: %v", err)
	}
	names := memberNames(tarMembers(t, stdout.Bytes()))
	if !slicesContainsSubstring(names, id) {
		t.Errorf("a symlinked vault produced an archive without its notes; members: %v", names)
	}
	// The staleness line reads through the same resolution, and a zero count makes it silent
	// altogether — the failure would be an operator told nothing rather than told wrong.
	if n := countVaultNotes(factoryRoot); n != 1 {
		t.Errorf("countVaultNotes read %d notes through the symlinked vault, want 1", n)
	}
}

// The tail of the same class, and the one the walk cannot see: WalkDir lstats a dangling symlink
// successfully and matches nothing, so a detached mount would otherwise be reported as a factory
// that has recorded nothing.
func TestMemoryExport_DanglingVaultSymlinkIsAnErrorNotAnEmptyArchive(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	mount := filepath.Join(t.TempDir(), "mount")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	vault := config.MemoryDir(factoryRoot)
	if err := os.RemoveAll(vault); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mount, vault); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "on-the-mount", Body: "recorded onto the host mount"})
	if err := os.Rename(mount, mount+".detached"); err != nil {
		t.Fatal(err)
	}
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err == nil {
		t.Fatalf("a detached vault mount produced a clean exit and %d bytes — the notes are "+
			"somewhere the export cannot follow and it said nothing", stdout.Len())
	}
	if _, ok := lastMemoryExportAt(factoryRoot); ok {
		t.Error("a refused export armed the marker, so the staleness warning would go quiet")
	}
}

func TestMemoryExport_NonDirectoryVaultIsAnErrorNotAnEmptyArchive(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	vault := config.MemoryDir(factoryRoot)
	if err := os.MkdirAll(filepath.Dir(vault), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, vault, "not a vault")
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err == nil {
		t.Fatalf("a vault that is not a directory produced a clean exit and %d bytes — "+
			"indistinguishable from a factory that has recorded nothing", stdout.Len())
	}
	if _, ok := lastMemoryExportAt(factoryRoot); ok {
		t.Error("a refused export armed the marker, so the staleness warning would go quiet")
	}
}

// The sibling case, and the more dangerous one: here the notes EXIST, so the staleness line goes
// on reporting a non-zero count. An export that swallowed the read failure would hand the operator
// a 32-byte archive, exit 0, and then have `af up` tell them their notes were backed up today.
func TestMemoryExport_UnreadableVaultIsAnErrorNotAnEmptyArchive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions, so mode 000 cannot make the vault unreadable")
	}
	factoryRoot, _ := setupMemoryFixture(t)
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "unreachable", Body: "body"})
	vault := config.MemoryDir(factoryRoot)
	if err := os.Chmod(vault, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(vault, 0o755) })
	asOperatorAt(t, factoryRoot)

	stdout, _, err := exportToBuffers(t)
	if err == nil {
		t.Fatalf("an unreadable vault produced a clean exit and %d bytes — the operator is told "+
			"their notes are backed up when none of them were read", stdout.Len())
	}
	if _, ok := lastMemoryExportAt(factoryRoot); ok {
		t.Error("a refused export armed the marker, so the staleness warning would go quiet")
	}
}

func TestMemoryExport_FromWorktreeCwdArchivesTheOuterRootVault(t *testing.T) {
	factoryRoot, wtAgentDir := setupWorktreeFixture(t, "solver")
	seedNote(t, factoryRoot, "solver", memory.Note{ID: "outer-note", Body: "recorded on the outer root"})
	// The whole subsystem exists because the ephemeral worktree is the wrong root (memory.go:1-21).
	// A backup taken from a worktree cwd that archived the worktree's own (empty) vault would
	// report success and hand the operator nothing.
	asOperatorAt(t, wtAgentDir)

	stdout, _, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("export from a worktree cwd: %v", err)
	}
	if !slicesContainsSubstring(memberNames(tarMembers(t, stdout.Bytes())), "outer-note") {
		t.Errorf("export from a worktree cwd did not archive the outer root's vault; members: %v",
			memberNames(tarMembers(t, stdout.Bytes())))
	}
}

// ---------------------------------------------------------------- the last-export marker

// Where "last export" is recorded was left unspecified by the plan; this suite is what pins the
// choice. The marker is the alarm-silencer the two operator chokepoints read, so the only
// property that actually matters is the ARMING ORDER: it may be written only after a stream that
// completed, or `af up` would go quiet about a backup that does not exist.
func TestMemoryExportStaleness_MarkerIsWrittenByExport(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "marker-note", Body: "body"})
	asOperatorAt(t, factoryRoot)

	if _, ok := lastMemoryExportAt(factoryRoot); ok {
		t.Fatal("a fresh factory must read as never exported")
	}

	if _, _, err := exportToBuffers(t); err != nil {
		t.Fatalf("export: %v", err)
	}

	at, ok := lastMemoryExportAt(factoryRoot)
	if !ok {
		t.Fatal("a completed export did not record the marker the staleness lines read")
	}
	if d := time.Since(at); d < 0 || d > time.Hour {
		t.Errorf("marker timestamp %v is not the export that just ran", at)
	}
	// Factory root, never a worktree, and beside the nag's own record: a worktree-resident
	// marker would evaporate at the next teardown and silently reset to "never".
	if _, err := os.Stat(filepath.Join(factoryRoot, ".runtime", "memory_export.json")); err != nil {
		t.Errorf("marker is not at <root>/.runtime/memory_export.json: %v", err)
	}
}

// failingWriter fails after n bytes — a full disk, a closed pipe, a `> /mnt/full/vault.tgz`.
type failingWriter struct {
	budget int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if len(p) > f.budget {
		f.budget = 0
		return 0, fmt.Errorf("no space left on device")
	}
	f.budget -= len(p)
	return len(p), nil
}

// The arming order, asserted rather than asserted-in-a-comment. Moving saveMemoryExportState
// above writeVaultTarball leaves every other test in this package green, and the result is an
// operator whose `af up` says "last export: today" about a truncated file — the exact
// "reports a backup that does not exist" failure the export stub was written to avoid.
func TestMemoryExportStaleness_MarkerIsNotArmedWhenTheStreamFails(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	for i := 0; i < 40; i++ {
		seedNote(t, factoryRoot, "alice", memory.Note{
			ID:   fmt.Sprintf("bulk-note-%02d", i),
			Body: strings.Repeat("a learning worth not losing\n", 64),
		})
	}
	asOperatorAt(t, factoryRoot)

	cmd := &cobra.Command{}
	var stderr bytes.Buffer
	cmd.SetOut(&failingWriter{budget: 512})
	cmd.SetErr(&stderr)

	if err := runMemoryExport(cmd, nil); err == nil {
		t.Fatal("a stream that could not be written must fail loudly, not exit 0 with a truncated tarball")
	}
	if _, ok := lastMemoryExportAt(factoryRoot); ok {
		t.Error("a failed export armed the last-export marker; the staleness warning would then go " +
			"quiet about a backup that does not exist")
	}
}

func TestMemoryExportStaleness_UnreadableMarkerReadsAsNeverExported(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	path := filepath.Join(factoryRoot, ".runtime", "memory_export.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, path, "{ this is not json")

	// The permissive direction, matching loadMemoryNagState (memory_nag.go:110-125): a corrupt
	// record costs one extra reminder to export. The strict direction would silence the R1
	// warning forever on the factories whose state is already damaged.
	if _, ok := lastMemoryExportAt(factoryRoot); ok {
		t.Error("an unparseable marker must read as never exported, not as a recent backup")
	}
}

// ---------------------------------------------------------------- helpers

func memberNames(members map[string][]byte) []string {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	return names
}

func slicesContainsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
