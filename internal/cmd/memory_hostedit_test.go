package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// T-EDIT and T-IMPORT — the operator's half of AC-515-5.
//
// The vault is plain Markdown in a directory an operator can open in Obsidian, and the design
// sells that as the visibility mechanism instead of git. That claim is only worth as much as the
// read path's willingness to honour what the operator did: a store that quietly re-derived its
// answer from an index, a cache, or a copy would leave the operator editing a decoration. So
// these rows edit the FILE and then ask the SessionStart verb what the next session receives.
//
// The round-trip row is the phase's own deliverable read end to end — export out of one container,
// edit on the host, import into the next — which is the only path a vault has across the
// `docker rm` that R1 leaves standing.

// noteFileIn returns the single note file in an agent's vault, failing when the vault does not
// hold exactly one: a helper that silently picked the first of several would let a row assert
// against a note it did not write.
func noteFileIn(t *testing.T, root, agent string) string {
	t.Helper()
	vault := config.AgentMemoryDir(root, agent)
	notes := noteFilesUnder(t, vault)
	if len(notes) != 1 {
		t.Fatalf("want exactly 1 note under %s, found %d: %v", vault, len(notes), notes)
	}
	return notes[0]
}

// editOnTheHost rewrites a note file the way Obsidian does — whole-file replace, no knowledge of
// our codec, no CLI involved.
func editOnTheHost(t *testing.T, path, old, new string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	edited := strings.Replace(string(data), old, new, 1)
	if edited == string(data) {
		t.Fatalf("the edit found nothing to replace (%q) in %s:\n%s", old, path, data)
	}
	writeFixtureFile(t, path, edited)
}

// importAs runs the real `af memory import` from the factory root as an operator.
func importAs(t *testing.T, root, agent, src string) string {
	t.Helper()
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	var out string
	inDir(t, root, func() {
		var err error
		out, err = execMemoryOut(t, "import", src, "--agent", agent)
		if err != nil {
			t.Fatalf("af memory import %s: %v (out=%q)", src, err, out)
		}
	})
	return out
}

// extractVault unpacks an export stream onto disk the way `tar xzf` would, so the round-trip row
// edits the same bytes a host operator would.
func extractVault(t *testing.T, archive []byte, dir string) {
	t.Helper()
	for name, body := range tarMembers(t, archive) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, path, string(body))
	}
}

func TestMemoryHostEdit_ObsidianBodyEditIsServedToTheNextSession(t *testing.T) {
	f := newDurabilityFactory(t, "solver")
	const original = "the retry budget is three"
	f.record(t, original)

	editOnTheHost(t, noteFileIn(t, f.root, f.agent), original, "the retry budget is three, and the fourth is a bug")

	out := f.successorInjection(t)
	if !strings.Contains(out, "and the fourth is a bug") {
		t.Errorf("the operator's edit never reached the next session; injection was:\n%s", out)
	}
	if strings.Contains(out, original+"\n") {
		t.Errorf("the superseded text was served alongside the edit — the read path is answering "+
			"from something other than the file; injection was:\n%s", out)
	}
}

// The curation power the manual promises: an operator can retire a note by hand, and the file
// stays on disk. Marking is the whole lifecycle here — there is no destructive verb — so a status
// the read path ignored would make the vault ungovernable from the host.
func TestMemoryHostEdit_OperatorExpiryMarkStopsTheNoteBeingServed(t *testing.T) {
	f := newDurabilityFactory(t, "solver")
	const marker = "the staging endpoint moved in march"
	f.record(t, marker)

	path := noteFileIn(t, f.root, f.agent)
	editOnTheHost(t, path, "status: active", "status: expired")

	if out := f.successorInjection(t); strings.Contains(out, marker) {
		t.Errorf("a hand-expired note was still served; injection was:\n%s", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expiring a note by hand destroyed the file %s: %v", path, err)
	}
}

// The whole documented loop, end to end: `af memory export > vault.tgz` out of one container,
// `tar xzf` and an edit on the host, `af memory import` into the next container. This is the
// only crossing a vault gets over the container recreation ADR-019 refuses to require, so it is
// asserted as one path rather than as three passing halves.
func TestMemoryHostEdit_ExportEditImportRoundTripReachesTheAgent(t *testing.T) {
	source := newDurabilityFactory(t, "solver")
	const original = "the export marker is armed only after a fully streamed archive"
	source.record(t, original, "--type", "gotcha", "--evidence", "issue#626")

	asOperatorAt(t, source.root)
	stdout, stderr, err := exportToBuffers(t)
	if err != nil {
		t.Fatalf("af memory export: %v (stderr=%q)", err, stderr.String())
	}

	host := t.TempDir()
	extractVault(t, stdout.Bytes(), host)
	extracted := filepath.Join(host, "memory", "solver")
	entries, err := os.ReadDir(extracted)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the archive carried no notes for solver (err=%v)", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" && e.Name() != "index.md" {
			editOnTheHost(t, filepath.Join(extracted, e.Name()), original, original+" — confirmed on the host")
		}
	}

	dest := newDurabilityFactory(t, "solver")
	importAs(t, dest.root, "solver", extracted)

	// The negative half, and the one that matters: the destination gains exactly the notes that
	// left. Asserting only that the edit arrived would stay green while the crossing also seeded
	// junk — and because the junk is then re-exported and re-imported, it compounds on every
	// crossing. The derived index is the concrete case: it is a listing of the other notes, and
	// `af memory import` ingests every .md it is pointed at.
	landed := noteFilesUnder(t, config.AgentMemoryDir(dest.root, "solver"))
	if len(landed) != 1 {
		t.Fatalf("the round trip put %d notes in the destination vault, want exactly 1: %v", len(landed), landed)
	}

	out := dest.successorInjection(t)
	if !strings.Contains(out, "confirmed on the host") {
		t.Errorf("the host edit never made it back into a container; injection was:\n%s", out)
	}
	if strings.Contains(out, "Derived file") {
		t.Errorf("the crossing seeded the derived index as a note, so the next session is served a "+
			"listing of its own memory; injection was:\n%s", out)
	}
	// Attribution has to survive the crossing too: a note that arrives stripped of what it rests
	// on is an unsourced instruction in a channel that already reads as one (security.md T4).
	for _, want := range []string{"type=gotcha", "evidence: issue#626"} {
		if !strings.Contains(out, want) {
			t.Errorf("the round trip lost %q; injection was:\n%s", want, out)
		}
	}
}

// The boundary of the round trip above, pinned so the manual cannot drift back across it.
//
// `import` seeds — it mints a fresh ID for every file it is given and never reconciles against
// what the destination already holds. That is right for the case it exists to serve (a
// replacement container, a new factory), and wrong for the one an operator will reach for first:
// export, edit, import back into the SAME container. There the notes double, and a copy the
// operator retired on the host lands beside an original that goes on being served — the opposite
// of what they asked for. The round-trip row cannot catch this because its destination is a fresh
// factory, which is the one case that behaves.
//
// This row characterises the behaviour rather than demanding a different one: `import` is
// Phase-2 code and out of scope here. Its job is to fail the day the manual claims a merge.
func TestMemoryImport_SeedsRatherThanMergesIntoANonEmptyVault(t *testing.T) {
	f := newDurabilityFactory(t, "solver")
	const marker = "the canary bakes for ten minutes"
	f.record(t, marker)

	staging := t.TempDir()
	data, err := os.ReadFile(noteFileIn(t, f.root, f.agent))
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(staging, "edited.md"),
		strings.Replace(string(data), "status: active", "status: expired", 1))

	importAs(t, f.root, f.agent, staging)

	landed := noteFilesUnder(t, config.AgentMemoryDir(f.root, f.agent))
	if len(landed) != 2 {
		t.Fatalf("import into a non-empty vault left %d notes, want 2 — if it now merges, "+
			"USING_MEMORY.md's \"import seeds; it does not merge\" is stale: %v", len(landed), landed)
	}
	if out := f.successorInjection(t); !strings.Contains(out, marker) {
		t.Errorf("importing an expired copy retired the original, so import now merges and the "+
			"manual is stale; injection was:\n%s", out)
	}
}

func TestMemoryImport_PlainClaudeCodeFilesAreStampedAndServed(t *testing.T) {
	f := newDurabilityFactory(t, "solver")

	src := t.TempDir()
	writeFixtureFile(t, filepath.Join(src, "deploys.md"), "# Deploys\n\nthe canary bakes for ten minutes\n")
	writeFixtureFile(t, filepath.Join(src, "flakes.md"), "# Flakes\n\nthe worktree probe needs GOTMPDIR\n")
	// Not Markdown, so not a note: an import that swept it up would put a file in the vault that
	// no verb can subsequently mark.
	writeFixtureFile(t, filepath.Join(src, "notes.txt"), "scratch")

	importAs(t, f.root, "solver", src)

	vault := config.AgentMemoryDir(f.root, "solver")
	notes := noteFilesUnder(t, vault)
	if len(notes) != 2 {
		t.Fatalf("want 2 imported notes under %s, found %d: %v", vault, len(notes), notes)
	}
	for _, path := range notes {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// A Claude Code memory file carries no frontmatter, so import must mint it — otherwise
		// the note lands as permanently malformed and no lifecycle verb can touch it.
		for _, want := range []string{"agent: solver", "status: active", "run: import/"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s was imported without %q:\n%s", filepath.Base(path), want, data)
			}
		}
	}

	out := f.successorInjection(t)
	for _, want := range []string{"the canary bakes for ten minutes", "the worktree probe needs GOTMPDIR"} {
		if !strings.Contains(out, want) {
			t.Errorf("an imported note was not served to the next session (%q); injection was:\n%s", want, out)
		}
	}
}

// A file that already carries our dialect keeps its fields. This is what makes export → import a
// round trip rather than a re-seed: without it every crossing would relabel the vault as freshly
// observed and a poisoning triage could no longer tell an import from an agent's own note.
func TestMemoryImport_PreservesTheFrontmatterAFileAlreadyCarries(t *testing.T) {
	f := newDurabilityFactory(t, "solver")
	const marker = "the index is derived, never a source of truth"
	f.record(t, marker, "--type", "ops", "--formula", "scenario", "--evidence", "pr#555")

	exported := noteFileIn(t, f.root, f.agent)
	staging := t.TempDir()
	data, err := os.ReadFile(exported)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(staging, "carried.md"), string(data))

	dest := newDurabilityFactory(t, "solver")
	importAs(t, dest.root, "solver", staging)

	out := dest.successorInjection(t)
	for _, want := range []string{marker, "type=ops", "formula=scenario", "evidence: pr#555"} {
		if !strings.Contains(out, want) {
			t.Errorf("import dropped %q from a note that already carried it; injection was:\n%s", want, out)
		}
	}
}
