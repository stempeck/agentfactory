package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/memory"
)

// memoryVerbFlagNames is the reset list resetMemoryFlags walks, keyed by verb. It is a named
// var, not a literal inside the loop, so TestResetMemoryFlags_CoversEveryVerbFlag can prove it
// stayed complete: the helper skips names it cannot find, so a flag added to a memory verb and
// forgotten here leaks its value into sibling tests without any compile error. Eleven verbs share
// one process-global rootCmd, so this is not optional (mail_test.go:157-184 precedent).
var memoryVerbFlagNames = map[string][]string{
	"add":      {"subject", "message", "type", "formula", "evidence"},
	"list":     {"agent", "formula", "all", "json", "type", "all-rows"},
	"show":     {},
	"graduate": {"to"},
	"expire":   {},
	"import":   {"agent"},
	"pin":      {"agent"},
	"unpin":    {"agent"},
	"export":   {},
	"status":   {"nag"},
	"check":    {"inject"},
}

// resetMemoryFlags restores every flag of one memory verb to its default and clears Changed.
// Flag state persists across rootCmd.Execute(), so every Execute must be followed by a reset.
func resetMemoryFlags(t *testing.T, verb string) {
	t.Helper()
	c, _, err := rootCmd.Find([]string{"memory", verb})
	if err != nil {
		t.Fatalf("finding memory %s command: %v", verb, err)
	}
	for _, name := range memoryVerbFlagNames[verb] {
		f := c.Flags().Lookup(name)
		if f == nil {
			continue
		}
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			// A StringArray appends rather than replaces, so Set(DefValue) would grow it.
			if err := sv.Replace(nil); err != nil {
				t.Fatalf("resetting --%s: %v", name, err)
			}
			f.Changed = false
			continue
		}
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatalf("resetting --%s: %v", name, err)
		}
		f.Changed = false
	}
}

// execMemoryOut drives a memory verb through the real cobra root and returns what it printed.
// rootCmd is SilenceErrors/SilenceUsage (root.go:16-26), so an error never reaches the buffer —
// which is what makes a byte-exact "zero bytes" assertion possible for check --inject.
func execMemoryOut(t *testing.T, verb string, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"memory", verb}, args...))
	err := rootCmd.Execute()
	resetMemoryFlags(t, verb)
	return buf.String(), err
}

// setupMemoryFixture builds a two-member factory (alice, bob). Two members are required so the
// scope-isolation and cross-agent rows have a sibling to target. No messaging.json: memory needs
// only the root marker and the agents.json membership gate detectSender-class derivation reads.
func setupMemoryFixture(t *testing.T) (factoryRoot, aliceDir string) {
	t.Helper()
	factoryRoot = t.TempDir()

	afDir := filepath.Join(factoryRoot, ".agentfactory")
	aliceDir = filepath.Join(afDir, "agents", "alice")
	if err := os.MkdirAll(aliceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(afDir, "agents", "bob"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(afDir, "factory.json"), `{}`)
	writeFixtureFile(t, filepath.Join(afDir, "agents.json"),
		`{"agents":{"alice":{"type":"autonomous","description":"test agent"},"bob":{"type":"autonomous","description":"test agent"}}}`)

	return factoryRoot, aliceDir
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedNote writes a note straight through the core, bypassing the CLI, so a test can arrange a
// vault state without depending on the verb it is about to exercise.
func seedNote(t *testing.T, root, agent string, n memory.Note) string {
	t.Helper()
	n.Agent = agent
	if n.Status == "" {
		n.Status = memory.StatusActive
	}
	if n.Created.IsZero() {
		n.Created = time.Now().UTC()
	}
	id, err := memory.Write(root, n)
	if err != nil {
		t.Fatalf("seeding note: %v", err)
	}
	return id
}

// noteFilesUnder returns every .md path beneath dir, index.md excluded.
func noteFilesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // a missing tree is an empty result, not a failure
		}
		if d.IsDir() || filepath.Ext(path) != ".md" || filepath.Base(path) == "index.md" {
			return nil
		}
		found = append(found, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// ---------------------------------------------------------------- AC1: surface

func TestMemoryCmd_ElevenVerbsRegistered(t *testing.T) {
	want := []string{"add", "check", "expire", "export", "graduate", "import", "list", "pin", "show", "status", "unpin"}
	for _, verb := range want {
		c, _, err := rootCmd.Find([]string{"memory", verb})
		if err != nil {
			t.Fatalf("memory %s does not resolve: %v", verb, err)
		}
		if c.Name() != verb {
			t.Errorf("memory %s resolved to %q", verb, c.Name())
		}
	}

	memCmd, _, err := rootCmd.Find([]string{"memory"})
	if err != nil {
		t.Fatalf("finding memory command: %v", err)
	}
	// Exact, so a twelfth verb has to be a deliberate edit here.
	if got := len(memCmd.Commands()); got != len(want) {
		var names []string
		for _, c := range memCmd.Commands() {
			names = append(names, c.Name())
		}
		t.Errorf("memory registers %d verbs %v, want exactly %d %v", got, names, len(want), want)
	}
}

// TestMemoryCmd_HelpNamesNoDeletion mechanizes the shell criterion `af memory --help | grep -c
// 'delete'` == 0 so it fails inside make test rather than at AC-verification time. The ban is a
// SUBSTRING ban over everything the help renders, which the most natural phrasing for expire —
// "the file is never deleted" — would violate while being entirely correct. There is no agent
// deletion path, and the help text has to say so without using the word.
func TestMemoryCmd_HelpNamesNoDeletion(t *testing.T) {
	memCmd, _, err := rootCmd.Find([]string{"memory"})
	if err != nil {
		t.Fatalf("finding memory command: %v", err)
	}
	// memoryCmd is a node in the process-global command tree, so an out writer set here would
	// outlive this test and swallow every later verb's output. Restore it.
	var buf bytes.Buffer
	memCmd.SetOut(&buf)
	memCmd.SetErr(&buf)
	t.Cleanup(func() { memCmd.SetOut(nil); memCmd.SetErr(nil) })
	if err := memCmd.Help(); err != nil {
		t.Fatalf("rendering help: %v", err)
	}
	if strings.Contains(buf.String(), "delete") {
		t.Errorf("`af memory --help` renders the substring \"delete\"; the surface has no deletion "+
			"path and its help text must not spell one. Output:\n%s", buf.String())
	}
}

func TestResetMemoryFlags_CoversEveryVerbFlag(t *testing.T) {
	if len(memoryVerbFlagNames) == 0 {
		t.Fatal("memoryVerbFlagNames is empty, so this scan would pass vacuously")
	}
	for verb, names := range memoryVerbFlagNames {
		c, _, err := rootCmd.Find([]string{"memory", verb})
		if err != nil {
			t.Fatalf("finding memory %s: %v", verb, err)
		}
		registered := map[string]bool{}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			// cobra injects --help into the local set the first time a command runs, so whether
			// it is present depends on test order. It carries no state worth resetting.
			if f.Name == "help" {
				return
			}
			registered[f.Name] = true
			if !slices.Contains(names, f.Name) {
				t.Errorf("flag --%s is registered on `memory %s` but missing from memoryVerbFlagNames — "+
					"its value will leak into sibling tests", f.Name, verb)
			}
		})
		for _, name := range names {
			if !registered[name] {
				t.Errorf("memoryVerbFlagNames lists --%s for `memory %s`, which the verb does not register", name, verb)
			}
		}
	}
}

// ------------------------------------- AC8: worktree durability (peer-review GAP-1, blocking)

// TestMemoryAdd_FromWorktreeCwd_LandsOnOuterRoot is this phase's reason to exist. With the
// symlink bridge declined, the CLI's root resolution is the only surviving mechanism keeping a
// note alive past worktree teardown, and the two resolvers in this tree disagree ONLY inside a
// worktree: config.FindFactoryRoot follows the .factory-root redirect out to the factory,
// config.FindLocalRoot stops at the nearest marker (the worktree). memory.validateRoot accepts
// either, because both are absolute — so a verb wired to the wrong one writes into the ephemeral
// tree, returns a nil error, and prints a path, which is the exact failure this subsystem exists
// to prevent wearing the appearance of having worked. The same defect superseded PR #555
// (.designs/329/design-doc.md:88).
func TestMemoryAdd_FromWorktreeCwd_LandsOnOuterRoot(t *testing.T) {
	factoryRoot, wtAgentDir := setupWorktreeFixture(t, "solver")
	t.Chdir(wtAgentDir)

	out, err := execMemoryOut(t, "add", "-s", "worktree durability", "-m", "this must outlive the worktree")
	if err != nil {
		t.Fatalf("memory add from a worktree cwd failed: %v (out=%q)", err, out)
	}

	vault := config.AgentMemoryDir(factoryRoot, "solver")
	notes := noteFilesUnder(t, vault)
	if len(notes) != 1 {
		t.Fatalf("want exactly 1 note under the outer-root vault %s, found %d: %v", vault, len(notes), notes)
	}

	// The non-vacuity guard. setupWorktreeFixture nests the worktree INSIDE factoryRoot, so
	// "the note path starts with factoryRoot" is true under the WRONG resolver too — an
	// assertion on the prefix alone passes against a FindLocalRoot implementation and proves
	// nothing. Exact-parent equality plus a walk of the worktrees tree is what discriminates.
	if got := filepath.Dir(notes[0]); got != vault {
		t.Errorf("note landed in %s, want exactly %s", got, vault)
	}
	worktrees := filepath.Join(factoryRoot, ".agentfactory", "worktrees")
	if leaked := noteFilesUnder(t, worktrees); len(leaked) != 0 {
		t.Errorf("a note leaked into the worktree tree %v — the factory root was resolved through the "+
			"nearest marker instead of the .factory-root redirect, so teardown would destroy it", leaked)
	}
}

// TestMemoryAdd_NestedFactoryWithoutAffirmation_RefusesBeforeWriting pins the other half of the
// resolver contract: a state-writing verb propagates the enclosing/mismatch refusal rather than
// guessing a root, and refuses BEFORE any bytes land.
func TestMemoryAdd_NestedFactoryWithoutAffirmation_RefusesBeforeWriting(t *testing.T) {
	outer, _ := setupMemoryFixture(t)

	nested := filepath.Join(outer, "clone")
	nestedAgentDir := filepath.Join(nested, ".agentfactory", "agents", "alice")
	if err := os.MkdirAll(nestedAgentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(nested, ".agentfactory", "factory.json"), `{}`)
	writeFixtureFile(t, filepath.Join(nested, ".agentfactory", "agents.json"),
		`{"agents":{"alice":{"type":"autonomous","description":"test agent"}}}`)

	t.Chdir(nestedAgentDir)
	t.Setenv("AF_ROOT", "")

	if _, err := execMemoryOut(t, "add", "-s", "nested", "-m", "body"); err == nil {
		t.Fatal("memory add inside an unaffirmed nested factory must refuse, not guess a root")
	}
	if notes := noteFilesUnder(t, config.MemoryDir(nested)); len(notes) != 0 {
		t.Errorf("the refusal happened after writing %v; it must precede any mutation", notes)
	}
	if notes := noteFilesUnder(t, config.MemoryDir(outer)); len(notes) != 0 {
		t.Errorf("the refusal happened after writing %v; it must precede any mutation", notes)
	}
}

// ---------------------------------------------------------------- AC5 + AC7: the inject contract

// TestMemoryCheckInject_SilentExitZeroOnEveryFailure pins the contract a SessionStart hook
// requires: every failure path returns nil and emits zero bytes. "Zero bytes" is asserted
// byte-exactly rather than through TrimSpace, because a stray newline is not nothing —
// scale.md:44-45 prices the empty case at zero injected bytes.
func TestMemoryCheckInject_SilentExitZeroOnEveryFailure(t *testing.T) {
	t.Run("no factory root above cwd", func(t *testing.T) {
		t.Chdir(t.TempDir())
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})

	t.Run("factory but no resolvable identity", func(t *testing.T) {
		factoryRoot, _ := setupMemoryFixture(t)
		t.Chdir(factoryRoot)
		t.Setenv("AF_ROLE", "")
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})

	t.Run("empty vault", func(t *testing.T) {
		_, aliceDir := setupMemoryFixture(t)
		t.Chdir(aliceDir)
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})

	t.Run("vault holds only a malformed note", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		vault := config.AgentMemoryDir(factoryRoot, "alice")
		if err := os.MkdirAll(vault, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(vault, "broken.md"), "---\nid: broken\nnot a frontmatter line\n")
		t.Chdir(aliceDir)
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})

	t.Run("every note graduated or expired", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "gone-a", Body: "old", Status: memory.StatusGraduated, GraduatedTo: "issue#1"})
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "gone-b", Body: "old", Status: memory.StatusExpired})
		t.Chdir(aliceDir)
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})

	t.Run("vault directory is unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "unreadable", Body: "body"})
		vault := config.AgentMemoryDir(factoryRoot, "alice")
		if err := os.Chmod(vault, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(vault, 0o755) })
		t.Chdir(aliceDir)
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})
}

func assertSilentSuccess(t *testing.T, out string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("check --inject must exit 0 on every failure path, got error: %v", err)
	}
	if out != "" {
		t.Errorf("check --inject must emit zero bytes here, got %d bytes: %q", len(out), out)
	}
}

// TestMemoryCheckInject_WritesNothingToStderrEither closes the hole the shell criterion leaves
// open: it captures only stdout, but resolveInvokerRoot writes warnings to os.Stderr on two
// NIL-ERROR success paths (stale AF_ROOT, nested factory). On a hook that is noise on every
// session start, so the inject path resolves through resolveInvokerRootWarn(wd, io.Discard) +
// silentRootDowngrade — the shape runStatuslineRender already uses (statusline.go:126-140).
func TestMemoryCheckInject_WritesNothingToStderrEither(t *testing.T) {
	outer, _ := setupMemoryFixture(t)

	nested := filepath.Join(outer, "clone")
	nestedAgentDir := filepath.Join(nested, ".agentfactory", "agents", "alice")
	if err := os.MkdirAll(nestedAgentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(nested, ".agentfactory", "factory.json"), `{}`)
	writeFixtureFile(t, filepath.Join(nested, ".agentfactory", "agents.json"),
		`{"agents":{"alice":{"type":"autonomous","description":"test agent"}}}`)
	t.Chdir(nestedAgentDir)
	t.Setenv("AF_ROOT", "")

	stderr := captureStderr(t, func() {
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})
	if stderr != "" {
		t.Errorf("check --inject leaked %d bytes to stderr on the SessionStart path: %q", len(stderr), stderr)
	}
}

// TestMemoryCheckInject_RendersProvenanceFramedBudgetedBlock is the non-empty half of the
// contract: what an agent actually receives at session start.
func TestMemoryCheckInject_RendersProvenanceFramedBudgetedBlock(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	seedNote(t, factoryRoot, "alice", memory.Note{
		ID:       "served-note",
		Type:     memory.TypeGotcha,
		Body:     "the outline-locator grep matches stale designs",
		Evidence: []string{"issue#626"},
		Expires:  time.Now().UTC().Add(48 * time.Hour),
	})
	t.Chdir(aliceDir)

	stdout, err := execMemoryOut(t, "check", "--inject")
	if err != nil {
		t.Fatalf("check --inject: %v", err)
	}
	// The writer's own SessionStart hook entry ships one hookSpecificOutput object; the block the
	// agent reads is its additionalContext. The ceiling below is asserted on the DECODED block,
	// which is the artifact the budget was sized for.
	out := decodeAdditionalContext(t, stdout)
	if ev := hookEventOf(t, stdout); ev != "SessionStart" {
		t.Errorf("memory's envelope should name SessionStart, got %q", ev)
	}
	if !strings.HasPrefix(out, "<system-reminder>") {
		t.Errorf("block must open with <system-reminder>, got:\n%s", out)
	}
	if !strings.HasSuffix(out, "</system-reminder>\n") {
		t.Errorf("block must close with </system-reminder>, got:\n%s", out)
	}
	if strings.Contains(stdout, htmlEscapedFence) {
		t.Error("the envelope HTML-escaped the fence; emitHookContext must disable escaping")
	}
	// Provenance framing (security.md T4 mitigation 1): the block says what these are, and each
	// note travels with its attribution so a downstream session weighs rather than obeys.
	if !strings.Contains(out, "recorded observations") {
		t.Error("block must frame its content as recorded observations, not directives")
	}
	for _, want := range []string{"served-note", memory.TypeGotcha, "issue#626", "the outline-locator grep"} {
		if !strings.Contains(out, want) {
			t.Errorf("block is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "af memory add") {
		t.Error("block must tell the agent how to record a new learning")
	}
	if len(out) > memory.DefaultTotalBytes+memoryInjectFrameBytes {
		t.Errorf("block is %d bytes, over the %d-byte ceiling", len(out), memory.DefaultTotalBytes+memoryInjectFrameBytes)
	}
}

// TestMemoryCheckInject_OverflowCountsOnlyBudgetedOut pins the arithmetic slice.go:126-131 warns
// about: the overflow line counts the notes the BUDGET dropped, not the notes the filter dropped.
// The graduated and expired notes seeded here are what makes the two formulas differ — without
// them, len(notes)-len(result) and the two-Slice difference give the same answer and the
// assertion cannot tell a correct implementation from the over-reporting one.
func TestMemoryCheckInject_OverflowCountsOnlyBudgetedOut(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	base := time.Now().UTC().Add(-time.Hour)
	const extra = 3
	for i := 0; i < memory.DefaultK+extra; i++ {
		seedNote(t, factoryRoot, "alice", memory.Note{
			ID:      "active-" + string(rune('a'+i)),
			Body:    "active note body",
			Created: base.Add(time.Duration(i) * time.Minute),
		})
	}
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad", Body: "b", Status: memory.StatusGraduated, GraduatedTo: "pr#7"})
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "exp", Body: "b", Status: memory.StatusExpired})
	t.Chdir(aliceDir)

	out, err := execMemoryOut(t, "check", "--inject")
	if err != nil {
		t.Fatalf("check --inject: %v", err)
	}
	want := "…and 3 more — `af memory list --type <type>` or `--formula <name>`"
	if !strings.Contains(out, want) {
		t.Errorf("want overflow line %q (the 3 active notes the budget dropped, NOT the 5 the filter "+
			"also excluded); got:\n%s", want, out)
	}
}

// ---------------------------------------------------------------- AC3: scope

// TestMemoryCheckInject_ScopeLadder covers both halves of AC3 in one place: an agent is served
// only its own vault, and the formula rung ranks a match first while degrading silently to
// recency at every rung below it.
func TestMemoryCheckInject_ScopeLadder(t *testing.T) {
	t.Run("a sibling's notes never appear", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "alice-note", Body: "alice body text"})
		seedNote(t, factoryRoot, "bob", memory.Note{ID: "bob-note", Body: "bob body text"})
		t.Chdir(aliceDir)

		out, err := execMemoryOut(t, "check", "--inject")
		if err != nil {
			t.Fatalf("check --inject: %v", err)
		}
		if !strings.Contains(out, "alice-note") {
			t.Errorf("alice was not served her own note:\n%s", out)
		}
		if strings.Contains(out, "bob-note") || strings.Contains(out, "bob body text") {
			t.Errorf("bob's vault leaked into alice's slice:\n%s", out)
		}
	})

	// The four rungs. Only the first may rank a formula match ahead of a newer note; every other
	// rung must degrade to recency WITHOUT an error and WITHOUT a byte on stderr, because the
	// alternative on a hook path is a session that starts with noise or not at all.
	rungs := []struct {
		name        string
		hookedID    string
		lastClosed  string
		wantFirstID string
	}{
		{"hooked and the name resolves", "af-1234", `{"formula":"Formula: scoped-run"}`, "old-match"},
		{"hooked but last_closed_step is absent", "af-1234", "", "new-unmatched"},
		{"hooked but last_closed_step is not JSON", "af-1234", "{{{not json", "new-unmatched"},
		{"unhooked", "", `{"formula":"Formula: scoped-run"}`, "new-unmatched"},
	}
	for _, rung := range rungs {
		t.Run(rung.name, func(t *testing.T) {
			factoryRoot, aliceDir := setupMemoryFixture(t)
			base := time.Now().UTC()
			seedNote(t, factoryRoot, "alice", memory.Note{
				ID: "old-match", Body: "matching but older", Formula: "scoped-run", Created: base.Add(-2 * time.Hour),
			})
			seedNote(t, factoryRoot, "alice", memory.Note{
				ID: "new-unmatched", Body: "newer but unmatched", Created: base.Add(-time.Minute),
			})
			if rung.hookedID != "" {
				writeRuntimeFile(t, aliceDir, "hooked_formula", rung.hookedID)
			}
			if rung.lastClosed != "" {
				writeRuntimeFile(t, aliceDir, "last_closed_step", rung.lastClosed)
			}
			t.Chdir(aliceDir)

			var out string
			stderr := captureStderr(t, func() {
				var err error
				out, err = execMemoryOut(t, "check", "--inject")
				if err != nil {
					t.Fatalf("check --inject: %v", err)
				}
			})
			if stderr != "" {
				t.Errorf("the ladder wrote %q to stderr; every rung degrades silently", stderr)
			}
			if idx, other := strings.Index(out, rung.wantFirstID), strings.Index(out, otherID(rung.wantFirstID)); idx < 0 || (other >= 0 && other < idx) {
				t.Errorf("want %q served first, got:\n%s", rung.wantFirstID, out)
			}
		})
	}

	t.Run("no notes at all is zero bytes", func(t *testing.T) {
		_, aliceDir := setupMemoryFixture(t)
		writeRuntimeFile(t, aliceDir, "hooked_formula", "af-1234")
		writeRuntimeFile(t, aliceDir, "last_closed_step", `{"formula":"Formula: scoped-run"}`)
		t.Chdir(aliceDir)
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})
}

func otherID(id string) string {
	if id == "old-match" {
		return "new-unmatched"
	}
	return "old-match"
}

// ---------------------------------------------------------------- AC6: lifecycle

func TestMemoryLifecycle_GraduateExpireStopServing(t *testing.T) {
	t.Run("graduate records the destination, keeps the file, stops serving", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad-me", Body: "graduate this"})
		t.Chdir(aliceDir)

		if _, err := execMemoryOut(t, "graduate", id, "--to", "issue#631"); err != nil {
			t.Fatalf("graduate: %v", err)
		}
		n := readBackNote(t, factoryRoot, "alice", id)
		if n.Status != memory.StatusGraduated {
			t.Errorf("status is %q, want %q", n.Status, memory.StatusGraduated)
		}
		if n.GraduatedTo != "issue#631" {
			t.Errorf("graduated_to is %q, want issue#631", n.GraduatedTo)
		}
		// T-PROT: the lifecycle is mark-only. The file stays on disk; only serving stops.
		if _, err := os.Stat(filepath.Join(config.AgentMemoryDir(factoryRoot, "alice"), id+".md")); err != nil {
			t.Errorf("the note file was removed from disk: %v", err)
		}
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})

	t.Run("expire stops serving, keeps the file", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "expire-me", Body: "expire this"})
		t.Chdir(aliceDir)

		if _, err := execMemoryOut(t, "expire", id); err != nil {
			t.Fatalf("expire: %v", err)
		}
		n := readBackNote(t, factoryRoot, "alice", id)
		if n.Status != memory.StatusExpired {
			t.Errorf("status is %q, want %q", n.Status, memory.StatusExpired)
		}
		if n.Body == "" {
			t.Error("expiry lost the body; it is a frontmatter-only rewrite")
		}
		if _, err := os.Stat(filepath.Join(config.AgentMemoryDir(factoryRoot, "alice"), id+".md")); err != nil {
			t.Errorf("the note file was removed from disk: %v", err)
		}
		out, err := execMemoryOut(t, "check", "--inject")
		assertSilentSuccess(t, out, err)
	})

	t.Run("an invalid destination is refused and changes nothing", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "keep-me", Body: "unchanged"})
		t.Chdir(aliceDir)

		if _, err := execMemoryOut(t, "graduate", id, "--to", "just some prose"); err == nil {
			t.Fatal("a free-text graduation destination must be refused")
		}
		n := readBackNote(t, factoryRoot, "alice", id)
		if n.Status != memory.StatusActive || n.GraduatedTo != "" {
			t.Errorf("the refused graduation still mutated the note: status=%q graduated_to=%q", n.Status, n.GraduatedTo)
		}
	})

	t.Run("an unknown id is a loud not-found", func(t *testing.T) {
		_, aliceDir := setupMemoryFixture(t)
		t.Chdir(aliceDir)
		_, err := execMemoryOut(t, "expire", "../../../etc/passwd")
		if err == nil {
			t.Fatal("expiring an id that is not in the vault listing must fail loudly")
		}
		if !strings.Contains(err.Error(), "no such note") {
			t.Errorf("error should report that no such note exists, got: %v", err)
		}
	})

	t.Run("a note leaves active exactly once", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "once-only", Body: "body"})
		t.Chdir(aliceDir)

		if _, err := execMemoryOut(t, "expire", id); err != nil {
			t.Fatalf("first expire: %v", err)
		}
		if _, err := execMemoryOut(t, "graduate", id, "--to", "pr#12"); err == nil {
			t.Error("a note that already left active must not be re-marked")
		}
	})
}

func readBackNote(t *testing.T, root, agent, id string) memory.Note {
	t.Helper()
	notes, err := memory.List(root, agent, memory.Filter{})
	if err != nil {
		t.Fatalf("listing vault: %v", err)
	}
	for _, n := range notes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("note %q is not in %s's vault", id, agent)
	return memory.Note{}
}

// ---------------------------------------------------------------- AC4: authority

// TestMemoryAuthority pins refusal behaviour under HONEST identity derivation only. The tiers
// bind to a claimed identity (design R12): a process that sets the env or cd's elsewhere IS that
// agent to this derivation, and nothing here claims otherwise. What the derivation cannot do is
// ESCALATE — callerAuthority is fail-closed, so operator tier requires the ABSENCE of every
// signal (peer-review Falsification #3). Each refusal row carries its operator-tier twin;
// without one, a verb that always errors would satisfy "refused".
func TestMemoryAuthority(t *testing.T) {
	seedDir := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		writeFixtureFile(t, filepath.Join(dir, "seed.md"), "a learning copied in by the operator\n")
		return dir
	}

	t.Run("import is refused under an agent-context signal", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		t.Chdir(aliceDir)
		t.Setenv("AF_ROLE", "alice")

		_, err := execMemoryOut(t, "import", seedDir(t))
		if err == nil {
			t.Fatal("import is operator-tier and must be refused for an agent caller")
		}
		// ux.md L36-39: a refusal never names the signal it read, because that is a bypass recipe.
		for _, forbidden := range []string{"AF_ROLE", "TMUX", "tmux session"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Errorf("refusal names the detection mechanism %q: %v", forbidden, err)
			}
		}
		if notes := noteFilesUnder(t, config.MemoryDir(factoryRoot)); len(notes) != 0 {
			t.Errorf("the refusal happened after importing %v; it must precede any write", notes)
		}
	})

	t.Run("import succeeds for an operator", func(t *testing.T) {
		factoryRoot, _ := setupMemoryFixture(t)
		t.Chdir(factoryRoot)
		t.Setenv("AF_ROLE", "")
		t.Setenv("TMUX", "")

		if _, err := execMemoryOut(t, "import", seedDir(t), "--agent", "bob"); err != nil {
			t.Fatalf("import as operator: %v", err)
		}
		if notes := noteFilesUnder(t, config.AgentMemoryDir(factoryRoot, "bob")); len(notes) != 1 {
			t.Errorf("want 1 imported note in bob's vault, got %d", len(notes))
		}
	})

	t.Run("export is refused under an agent-context signal", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "not-yours-to-take", Body: "a learning"})
		t.Chdir(aliceDir)
		t.Setenv("AF_ROLE", "alice")

		out, err := execMemoryOut(t, "export")
		if err == nil {
			t.Fatal("export is operator-tier and must be refused for an agent caller")
		}
		for _, forbidden := range []string{"AF_ROLE", "TMUX", "tmux session"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Errorf("refusal names the detection mechanism %q: %v", forbidden, err)
			}
		}
		// The refusal must precede the stream, not truncate it: a caller redirecting stdout to a
		// file would otherwise be left holding a partial archive that `tar tz` reports as corrupt
		// rather than as refused.
		if out != "" {
			t.Errorf("the refused export emitted %q; it must produce no archive bytes at all", out)
		}
	})

	t.Run("export succeeds for an operator", func(t *testing.T) {
		factoryRoot, _ := setupMemoryFixture(t)
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "exportable", Body: "a learning"})
		t.Chdir(factoryRoot)
		t.Setenv("AF_ROLE", "")
		t.Setenv("TMUX", "")

		// The operator-tier twin the header comment above requires: without it a verb that always
		// errored would satisfy "refused". Byte-level assertions on the archive live in
		// memory_export_test.go, which drives the verb with separated streams.
		out, err := execMemoryOut(t, "export")
		if err != nil {
			t.Fatalf("export as operator: %v", err)
		}
		if out == "" {
			t.Error("an operator-tier export of a non-empty vault produced no output at all")
		}
	})

	t.Run("status --nag is refused under an agent-context signal", func(t *testing.T) {
		_, aliceDir := setupMemoryFixture(t)
		t.Chdir(aliceDir)
		t.Setenv("AF_ROLE", "alice")

		_, err := execMemoryOut(t, "status", "--nag")
		if err == nil {
			t.Fatal("the hygiene pass is operator-tier and must be refused for an agent caller")
		}
		for _, forbidden := range []string{"AF_ROLE", "TMUX", "tmux session"} {
			if strings.Contains(err.Error(), forbidden) {
				t.Errorf("refusal names the detection mechanism %q: %v", forbidden, err)
			}
		}
	})

	t.Run("status --nag succeeds for an operator", func(t *testing.T) {
		factoryRoot, _ := setupMemoryFixture(t)
		t.Chdir(factoryRoot)
		t.Setenv("AF_ROLE", "")
		t.Setenv("TMUX", "")

		if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
			t.Fatalf("the hygiene pass as operator: %v", err)
		}
	})

	t.Run("an agent cannot target a sibling's vault through add", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		t.Chdir(aliceDir)
		t.Setenv("AF_ROLE", "alice")

		// Structural: the sanctioned write verb registers no cross-agent selector at all, so
		// there is no agent-reachable path to a sibling's vault to gate in the first place.
		addCmd, _, err := rootCmd.Find([]string{"memory", "add"})
		if err != nil {
			t.Fatalf("finding memory add: %v", err)
		}
		if f := addCmd.Flags().Lookup("agent"); f != nil {
			t.Error("`memory add` registers --agent; cross-agent writes belong to the operator-tier import verb")
		}
		if _, err := execMemoryOut(t, "add", "--agent", "bob", "-s", "x", "-m", "y"); err == nil {
			t.Fatal("add --agent must not be accepted")
		}
		if notes := noteFilesUnder(t, config.AgentMemoryDir(factoryRoot, "bob")); len(notes) != 0 {
			t.Errorf("bob's vault was written: %v", notes)
		}
	})

	t.Run("an agent CAN read a sibling's vault", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedNote(t, factoryRoot, "bob", memory.Note{ID: "bob-visible", Body: "bob learned something"})
		t.Chdir(aliceDir)
		t.Setenv("AF_ROLE", "alice")

		// Memory is not secret between agents; scoping is relevance, not confidentiality
		// (security.md:29-34). The default SERVING path is still self-only — that is AC3.
		out, err := execMemoryOut(t, "list", "--agent", "bob")
		if err != nil {
			t.Fatalf("cross-agent read must be allowed at agent tier: %v", err)
		}
		if !strings.Contains(out, "bob-visible") {
			t.Errorf("cross-agent read did not show bob's note:\n%s", out)
		}
	})
}

// ---------------------------------------------------------------- AC2: containment

// TestMemoryAdd_ContainmentSilent_RawVaultWriteIsNot pins that the sanctioned CLI path is quiet
// while the unsanctioned one leaves an audit trail. The exemption is STRUCTURAL, not a name
// allowlist: parseEffectiveTarget recognises only cd/pushd/git -C as target-bearing, so an
// `af memory add` command yields zero decidable targets and runContainmentCheckCore returns nil
// at containment.go:139-144. The positive control is mandatory — without it the first assertion
// passes against literally any Bash string, including "hello world".
func TestMemoryAdd_ContainmentSilent_RawVaultWriteIsNot(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	factoryRoot := filepath.Dir(filepath.Dir(filepath.Dir(worktreeRoot)))

	t.Run("the sanctioned CLI write produces no advisory", func(t *testing.T) {
		recs := installContainmentRecorder(t)
		var buf bytes.Buffer
		if err := runContainmentCheckCore(&buf, bashPayload("af memory add -s subject -m body", worktreeRoot)); err != nil {
			t.Fatalf("containment check: %v", err)
		}
		if len(*recs) != 0 {
			t.Errorf("want 0 corrective mails for the sanctioned path, got %d: %+v", len(*recs), *recs)
		}
		if buf.Len() != 0 {
			t.Errorf("want no advisory output, got %q", buf.String())
		}
	})

	t.Run("a raw write to the vault does produce one", func(t *testing.T) {
		recs := installContainmentRecorder(t)
		vaultNote := filepath.Join(config.AgentMemoryDir(factoryRoot, "solver"), "hand-written.md")
		var buf bytes.Buffer
		if err := runContainmentCheckCore(&buf, writePayload("Write", vaultNote, worktreeRoot)); err != nil {
			t.Fatalf("containment check: %v", err)
		}
		if len(*recs) != 1 {
			t.Fatalf("want exactly 1 corrective mail for the raw vault write, got %d: %+v", len(*recs), *recs)
		}
		if !strings.Contains((*recs)[0].body, vaultNote) {
			t.Errorf("the corrective does not name the path it is about: %q", (*recs)[0].body)
		}
	})
}

// ---------------------------------------------------------------- empty state + list/status

// TestMemoryList_EmptyStateSentenceIsVerbatim pins the CLI's copy of the sentence against the
// core's. RebuildIndex writes it as an inline Fprintf (store.go:455) and exports no accessor, so
// the CLI necessarily re-spells it; this test compares the two artifacts rather than two
// literals, so a drift in either one fails here. Note the em-dash, which a paraphrase loses.
func TestMemoryList_EmptyStateSentenceIsVerbatim(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	t.Chdir(aliceDir)

	out, err := execMemoryOut(t, "list")
	if err != nil {
		t.Fatalf("list on a fresh factory must succeed, got: %v", err)
	}

	if err := memory.RebuildIndex(factoryRoot, "alice"); err != nil {
		t.Fatalf("rebuilding index: %v", err)
	}
	indexBytes, err := os.ReadFile(filepath.Join(config.AgentMemoryDir(factoryRoot, "alice"), "index.md"))
	if err != nil {
		t.Fatalf("reading index: %v", err)
	}
	want := "no memory recorded yet for alice — this is a normal state on a fresh factory."
	if !strings.Contains(string(indexBytes), want) {
		t.Fatalf("the core no longer writes the pinned sentence; update both copies together")
	}
	if !strings.Contains(out, want) {
		t.Errorf("list must print the empty state verbatim, got:\n%s", out)
	}
}

func TestMemoryStatus_ReportsCountsAndVaultPath(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "s-active", Body: "active"})
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "s-expired", Body: "old", Status: memory.StatusExpired})
	vault := config.AgentMemoryDir(factoryRoot, "alice")
	writeFixtureFile(t, filepath.Join(vault, "s-broken.md"), "---\nid: s-broken\nnot frontmatter\n")
	t.Chdir(aliceDir)

	out, err := execMemoryOut(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{"alice", config.MemoryDir(factoryRoot)} {
		if !strings.Contains(out, want) {
			t.Errorf("status output is missing %q:\n%s", want, out)
		}
	}
	// A hand-edited note that no verb can subsequently mark is exactly what an operator needs
	// status to find, so assert the COUNT, not merely that the word appears in a header.
	row := statusRowFor(t, out, "alice")
	if len(row) < 6 {
		t.Fatalf("alice's status row is malformed: %q", row)
	}
	if row[1] != "1" || row[3] != "1" || row[4] != "1" {
		t.Errorf("want alice at 1 active / 1 expired / 1 malformed, got active=%s expired=%s malformed=%s (row %q)",
			row[1], row[3], row[4], row)
	}
	if !strings.Contains(strings.ToLower(out), "malformed") {
		t.Errorf("status must name the malformed column:\n%s", out)
	}
}

// statusRowFor returns the whitespace-split cells of one agent's `memory status` row, with the
// "(you)" marker folded away so the column indices are the same for every agent.
func statusRowFor(t *testing.T, out, agent string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), agent) {
			continue
		}
		return strings.Fields(strings.Replace(line, "(you)", "", 1))
	}
	t.Fatalf("no status row for %s in:\n%s", agent, out)
	return nil
}

func TestMemoryShow_RendersOneNote(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "show-me", Body: "the body to show", Evidence: []string{"pr#9"}})
	t.Chdir(aliceDir)

	out, err := execMemoryOut(t, "show", id)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	for _, want := range []string{id, "the body to show", "pr#9"} {
		if !strings.Contains(out, want) {
			t.Errorf("show output is missing %q:\n%s", want, out)
		}
	}

	if _, err := execMemoryOut(t, "show", "no-such-note"); err == nil {
		t.Error("showing an id that is not in the vault must fail loudly")
	}
}

func TestMemoryAdd_StampsProvenanceAndTTL(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	writeRuntimeFile(t, aliceDir, "worktree_id", "wt-abc123\n")
	writeRuntimeFile(t, aliceDir, "session_id", "sess-42\n")
	t.Chdir(aliceDir)

	out, err := execMemoryOut(t, "add",
		"-s", "Stale designs grep!", "-m", "body text",
		"--type", memory.TypeOps, "--formula", "rootcause-all", "--evidence", "issue#626", "--evidence", "pr#516")
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	notes, err := memory.List(factoryRoot, "alice", memory.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("want 1 note, got %d", len(notes))
	}
	n := notes[0]
	if n.Agent != "alice" {
		t.Errorf("agent is %q, want alice", n.Agent)
	}
	if n.Run != "wt-abc123/sess-42" {
		t.Errorf("run attribution is %q, want wt-abc123/sess-42", n.Run)
	}
	if n.Formula != "rootcause-all" {
		t.Errorf("formula is %q", n.Formula)
	}
	if !slices.Equal(n.Evidence, []string{"issue#626", "pr#516"}) {
		t.Errorf("evidence is %v", n.Evidence)
	}
	// The CLI stamps the TTL: memory.Write derives neither Created nor Expires.
	if n.Expires.IsZero() {
		t.Error("no TTL was stamped; an ops note expires in 180 days")
	}
	if n.Status != memory.StatusActive {
		t.Errorf("status is %q, want active", n.Status)
	}
	// The id is slugified from the subject, so the vault is browsable in Obsidian by name.
	if !strings.Contains(n.ID, "stale-designs-grep") {
		t.Errorf("id %q does not carry a slug of the subject", n.ID)
	}
	if !strings.Contains(out, n.ID) {
		t.Errorf("the confirmation line does not name the note id:\n%s", out)
	}

	if _, err := execMemoryOut(t, "add", "-s", "x", "-m", "y", "--type", "not-a-type"); err == nil {
		t.Error("an unknown note type must be refused")
	}
}

func TestMemoryAdd_ReadsBodyFromStdin(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	t.Chdir(aliceDir)

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetIn(strings.NewReader("piped body text\n"))
	rootCmd.SetArgs([]string{"memory", "add", "-s", "piped"})
	err := rootCmd.Execute()
	resetMemoryFlags(t, "add")
	rootCmd.SetIn(nil)
	if err != nil {
		t.Fatalf("add with a piped body: %v", err)
	}

	notes, err := memory.List(factoryRoot, "alice", memory.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Body, "piped body text") {
		t.Errorf("the piped body did not reach the note: %+v", notes)
	}
}

// ---------------------------------------------------------------- list cap, order and flags (C6)

const memoryListCap = 8192

func memoryListOverflow(n int) string {
	return fmt.Sprintf("… and %d more (--all-rows, --type, --formula)", n)
}

// listVaultNote is the i-th note of a large vault: a timestamp-free id, a subject long enough that
// a few dozen rows fill the text cap, and a distinct Created, newest first by index.
func listVaultNote(i int, now time.Time) memory.Note {
	return memory.Note{
		ID:      fmt.Sprintf("list-note-%04d", i),
		Type:    memory.TypeOps,
		Created: now.Add(-time.Duration(i+1) * time.Minute),
		Body:    "# " + paddedSubject(fmt.Sprintf("list vault learning %04d", i), 160) + "\n\nbody\n",
	}
}

func seedListVault(t *testing.T, root, agent string, count int, now time.Time, adjust func(i int, n *memory.Note)) {
	t.Helper()
	for i := 0; i < count; i++ {
		n := listVaultNote(i, now)
		if adjust != nil {
			adjust(i, &n)
		}
		n.ID = strings.Replace(n.ID, "list-note", agent+"-note", 1)
		seedNote(t, root, agent, n)
	}
}

func listTextLines(out string) []string {
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
}

func listRowID(line string) string {
	f := strings.Fields(line)
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

func listRowIDs(rows []string) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, listRowID(r))
	}
	return ids
}

func TestMemoryList_CappedPinnedFirst(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	now := time.Now().UTC()
	pinned := map[string]bool{}
	// Five pins spread across the ages, the last of them the oldest note in the vault, so a list
	// that kept recency order would cut every one of them but the first.
	seedListVault(t, factoryRoot, "alice", 1000, now, func(i int, n *memory.Note) {
		if i%200 == 199 {
			n.Standing = true
			pinned["alice-note-"+strings.TrimPrefix(n.ID, "list-note-")] = true
		}
	})
	asAgentAt(t, aliceDir, "")

	out, err := execMemoryOut(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) > memoryListCap {
		t.Errorf("the text output is %d B, over the %d B cap", len(out), memoryListCap)
	}
	lines := listTextLines(out)
	if len(lines) < 3 {
		t.Fatalf("want a header, rows and the overflow line, got:\n%s", out)
	}
	rows := lines[1 : len(lines)-1]
	seenOther, pinsShown := false, 0
	for _, r := range rows {
		if pinned[listRowID(r)] {
			pinsShown++
			if seenOther {
				t.Errorf("pinned row %s comes after an unpinned one", listRowID(r))
			}
			if !strings.Contains(r, "active (pinned)") {
				t.Errorf("a pinned row's STATUS must read `active (pinned)`: %q", r)
			}
			continue
		}
		seenOther = true
		if strings.Contains(r, "(pinned)") {
			t.Errorf("an unpinned row is marked pinned: %q", r)
		}
	}
	if pinsShown != len(pinned) {
		t.Errorf("the capped text shows %d of the %d pinned rows", pinsShown, len(pinned))
	}
	if want := memoryListOverflow(1000 - len(rows)); lines[len(lines)-1] != want {
		t.Errorf("the last line is %q, want %q", lines[len(lines)-1], want)
	}

	full, err := execMemoryOut(t, "list", "--all-rows")
	if err != nil {
		t.Fatalf("list --all-rows: %v", err)
	}
	fullLines := listTextLines(full)
	if len(fullLines) != 1001 {
		t.Fatalf("--all-rows prints %d lines, want the header and 1000 rows", len(fullLines))
	}
	if lines[0] != fullLines[0] {
		t.Errorf("the capped header %q is not the full table's %q", lines[0], fullLines[0])
	}
	for i, r := range rows {
		if r != fullLines[i+1] {
			t.Fatalf("capped row %d is %q, the full table's is %q: the cut is not a line prefix of one rendering", i, r, fullLines[i+1])
		}
	}
	k := len(rows)
	oneMore := strings.Join(fullLines[:k+2], "\n") + "\n" + memoryListOverflow(1000-k-1) + "\n"
	if len(oneMore) <= memoryListCap {
		t.Errorf("one more row and its overflow line still fit (%d B): the cut is not the longest prefix", len(oneMore))
	}
}

func TestMemoryList_OrderPinnedActiveNonActive(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	now := time.Now().UTC()
	day := 24 * time.Hour
	for _, n := range []memory.Note{
		{ID: "2026-01-01T0000Z-old-pin", Created: now.Add(-300 * day), Body: "# an old ruling\n", Standing: true},
		{ID: "zz-new-pin", Created: now.Add(-2 * day), Body: "# a new ruling\n", Standing: true},
		{ID: "lapsed-pin", Created: now.Add(-20 * day), Expires: now.Add(-day), Body: "# a ruling past its TTL\n", Standing: true},
		{ID: "a-graduated-pin", Created: now.Add(-3 * day), Status: memory.StatusGraduated, GraduatedTo: "pr#1", Body: "# a graduated ruling\n", Standing: true},
		// The imported id carries no timestamp and sorts ahead of every dated id, yet it is the
		// oldest active note: only an order by Created puts it last among them (D-18).
		{ID: "imported-ruling-note", Created: now.Add(-200 * day), Body: "# an imported learning\n"},
		{ID: "2026-10-01T0000Z-older", Created: now.Add(-43 * day), Body: "# an older learning\n"},
		{ID: "2026-11-12T0000Z-newer", Created: now.Add(-day), Body: "# a newer learning\n"},
		{ID: "b-graduated", Created: now.Add(-5 * day), Status: memory.StatusGraduated, GraduatedTo: "pr#2", Body: "# graduated\n"},
		{ID: "c-expired", Created: now.Add(-10 * day), Status: memory.StatusExpired, Body: "# expired\n"},
	} {
		seedNote(t, factoryRoot, "alice", n)
	}
	asAgentAt(t, aliceDir, "")

	out, err := execMemoryOut(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	lines := listTextLines(out)
	if len(lines) < 2 {
		t.Fatalf("want a header and rows, got:\n%s", out)
	}
	want := []string{
		"zz-new-pin", "lapsed-pin", "2026-01-01T0000Z-old-pin",
		"2026-11-12T0000Z-newer", "2026-10-01T0000Z-older", "imported-ruling-note",
		"a-graduated-pin", "b-graduated", "c-expired",
	}
	if got := listRowIDs(lines[1:]); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("row order:\n got %q\nwant %q", got, want)
	}
	for _, r := range lines[1:] {
		switch id := listRowID(r); id {
		case "zz-new-pin", "2026-01-01T0000Z-old-pin":
			if !strings.Contains(r, "active (pinned)") {
				t.Errorf("pinned row %s must read `active (pinned)`: %q", id, r)
			}
		case "lapsed-pin":
			if !strings.Contains(r, "expired (ttl, pinned)") || strings.Contains(r, "active (pinned)") {
				t.Errorf("pinned row %s is past its TTL, so it must read `expired (ttl, pinned)`: %q", id, r)
			}
		case "a-graduated-pin":
			if strings.Contains(r, "(pinned)") || !strings.Contains(r, memory.StatusGraduated) {
				t.Errorf("a graduated standing note is not served, so it is not pinned: %q", r)
			}
		}
	}
}

func TestMemoryList_JSONUnchanged(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	now := time.Now().UTC()
	seedListVault(t, factoryRoot, "alice", 1000, now, func(i int, n *memory.Note) {
		if i%2 == 0 {
			n.Formula, n.Run = "fable-increment", "run-1"
		}
		if i%250 == 0 {
			n.Standing = true
		}
	})
	seedNote(t, factoryRoot, "alice", memory.Note{
		ID: "lapsed-pin", Created: now.Add(-20 * 24 * time.Hour), Expires: now.Add(-time.Hour),
		Body: "# a ruling past its TTL\n", Standing: true,
	})
	asAgentAt(t, aliceDir, "")

	out, err := execMemoryOut(t, "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--json output does not decode: %v", err)
	}
	if len(rows) != 1001 {
		t.Fatalf("--json carries %d rows, want all 1001: the text cap must not reach it", len(rows))
	}
	notes, err := memory.List(factoryRoot, "alice", memory.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"agent": true, "id": true, "type": true, "formula": true, "status": true, "created": true, "subject": true, "run": true}
	required := []string{"agent", "id", "type", "status", "created", "subject"}
	for i, row := range rows {
		var id string
		_ = json.Unmarshal(row["id"], &id)
		if i < len(notes) && id != notes[i].ID {
			t.Fatalf("row %d is %q, memory.List order has %q there", i, id, notes[i].ID)
		}
		for key := range row {
			if !allowed[key] {
				t.Errorf("row %s carries key %q, which is not one of today's eight", id, key)
			}
		}
		for _, key := range required {
			if _, ok := row[key]; !ok {
				t.Errorf("row %s lacks key %q", id, key)
			}
		}
		switch id {
		case "alice-note-0001":
			if _, ok := row["formula"]; ok {
				t.Errorf("a note with no formula must omit the key: %s", row["formula"])
			}
			if _, ok := row["run"]; ok {
				t.Errorf("a note with no run must omit the key: %s", row["run"])
			}
		case "lapsed-pin":
			var status string
			_ = json.Unmarshal(row["status"], &status)
			if status != "expired (ttl)" {
				t.Errorf("a TTL-passed pinned note reads %q in --json, want %q as memoryStatusOf reports it", status, "expired (ttl)")
			}
		}
	}

	t.Run("--all-rows changes nothing", func(t *testing.T) {
		again, err := execMemoryOut(t, "list", "--json", "--all-rows")
		if err != nil {
			t.Fatalf("list --json --all-rows: %v", err)
		}
		if again != out {
			t.Errorf("--all-rows changed the --json output (%d B against %d B)", len(again), len(out))
		}
	})
}

func TestMemoryList_TypeAndAllRowsFlags(t *testing.T) {
	t.Run("flags registered with their help", func(t *testing.T) {
		c, _, err := rootCmd.Find([]string{"memory", "list"})
		if err != nil {
			t.Fatalf("finding memory list: %v", err)
		}
		for name, usage := range map[string]string{
			"type":     "Only notes of this type: gotcha, model-behavior, ops, outcome, improvement",
			"all-rows": "Print every row; without it the text stops at 8,192 bytes (--all, which reads every agent's vault, does not lift it)",
		} {
			f := c.Flags().Lookup(name)
			if f == nil {
				t.Errorf("memory list registers no --%s", name)
				continue
			}
			if f.Usage != usage {
				t.Errorf("--%s help is %q, want %q", name, f.Usage, usage)
			}
		}
	})

	t.Run("--type filters text and json", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "g-one", Type: memory.TypeGotcha, Formula: "f-a", Body: "# gotcha one\n"})
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "g-two", Type: memory.TypeGotcha, Formula: "f-b", Body: "# gotcha two\n"})
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "o-one", Type: memory.TypeOps, Formula: "f-a", Body: "# ops one\n"})
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "u-one", Type: memory.TypeOutcome, Body: "# outcome one\n"})
		asAgentAt(t, aliceDir, "")

		out, err := execMemoryOut(t, "list", "--type", "gotcha")
		if err != nil {
			t.Fatalf("list --type gotcha: %v", err)
		}
		if got := slices.Sorted(slices.Values(listRowIDs(listTextLines(out)[1:]))); strings.Join(got, " ") != "g-one g-two" {
			t.Errorf("--type gotcha lists %q, want [g-one g-two]", got)
		}

		jsonOut, err := execMemoryOut(t, "list", "--type", "gotcha", "--json")
		if err != nil {
			t.Fatalf("list --type gotcha --json: %v", err)
		}
		var rows []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(jsonOut), &rows); err != nil {
			t.Fatalf("decoding: %v\n%s", err, jsonOut)
		}
		if len(rows) != 2 {
			t.Errorf("--type gotcha --json carries %d rows, want 2: %s", len(rows), jsonOut)
		}

		both, err := execMemoryOut(t, "list", "--type", "gotcha", "--formula", "f-a")
		if err != nil {
			t.Fatalf("list --type gotcha --formula f-a: %v", err)
		}
		if got := listRowIDs(listTextLines(both)[1:]); strings.Join(got, " ") != "g-one" {
			t.Errorf("--type with --formula lists %q, want [g-one]", got)
		}
	})

	t.Run("--type refuses an unknown type naming the five", func(t *testing.T) {
		_, aliceDir := setupMemoryFixture(t)
		asAgentAt(t, aliceDir, "")
		_, err := execMemoryOut(t, "list", "--type", "habit")
		if err == nil {
			t.Fatal("an unknown --type must be refused")
		}
		for _, typ := range []string{"gotcha", "model-behavior", "ops", "outcome", "improvement"} {
			if !strings.Contains(err.Error(), typ) {
				t.Errorf("the refusal does not name %q: %v", typ, err)
			}
		}
	})

	t.Run("--all-rows lifts the cap", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedListVault(t, factoryRoot, "alice", 120, time.Now().UTC(), nil)
		asAgentAt(t, aliceDir, "")

		capped, err := execMemoryOut(t, "list")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(capped) > memoryListCap || !strings.HasSuffix(capped, " more (--all-rows, --type, --formula)\n") {
			t.Errorf("without --all-rows a 120-row vault must stop at the cap with its overflow line (%d B)", len(capped))
		}
		out, err := execMemoryOut(t, "list", "--all-rows")
		if err != nil {
			t.Fatalf("list --all-rows: %v", err)
		}
		if got := len(listTextLines(out)) - 1; got != 120 {
			t.Errorf("--all-rows prints %d rows, want 120", got)
		}
		if strings.Contains(out, "more (--all-rows") {
			t.Errorf("--all-rows still prints the overflow line")
		}
	})

	t.Run("--all does not lift the cap", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedListVault(t, factoryRoot, "alice", 60, time.Now().UTC(), nil)
		seedListVault(t, factoryRoot, "bob", 60, time.Now().UTC(), nil)
		asAgentAt(t, aliceDir, "")

		out, err := execMemoryOut(t, "list", "--all")
		if err != nil {
			t.Fatalf("list --all: %v", err)
		}
		if len(out) > memoryListCap || !strings.HasSuffix(out, " more (--all-rows, --type, --formula)\n") {
			t.Errorf("--all reads every vault but keeps the cap and its overflow line (%d B)", len(out))
		}
		every, err := execMemoryOut(t, "list", "--all", "--all-rows")
		if err != nil {
			t.Fatalf("list --all --all-rows: %v", err)
		}
		if got := len(listTextLines(every)) - 1; got != 120 {
			t.Errorf("--all --all-rows prints %d rows, want both vaults' 120", got)
		}
	})
}
