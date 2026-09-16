package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/memory"
)

// The PATH export every SessionStart entry must carry, and the UserPromptSubmit command both
// templates carry and must keep carrying unchanged. Since #675 K3 the three SessionStart writers are
// separate entries, so what this file pins is the memory entry: its exact command and the PATH export
// in front of it. Entry count, order and the absence of `&&` chaining across the other two are pinned
// by assertSessionStartWriters (internal/claude) and assertProvisionedSessionStart (install_test.go);
// frozenUserPromptSubmit remains byte-exact here because UserPromptSubmit is still one command.
const (
	sessionStartHookPrefix = `export PATH="$HOME/go/bin:$HOME/.local/bin:$HOME/bin:$PATH" && `
	frozenUserPromptSubmit = `export PATH="$HOME/go/bin:$HOME/.local/bin:$HOME/bin:$PATH" && af mail check --inject`
	memoryHookSegment      = `af memory check --inject`
)

// hookCommandGroups decodes hooks.<event> from a settings document into one command slice per
// matcher group. Every hook-command reader in this package needs exactly this shape, and each
// re-declaration of it is another place to get the JSON wrong while still compiling. `source` names
// the document in failure messages, since callers supply either a path or an embedded template.
func hookCommandGroups(t *testing.T, settings []byte, event, source string) [][]string {
	t.Helper()
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(settings, &parsed); err != nil {
		t.Fatalf("%s is not valid JSON: %v", source, err)
	}
	entries := parsed.Hooks[event]
	if len(entries) == 0 {
		t.Fatalf("no %s hook in %s", event, source)
	}
	groups := make([][]string, 0, len(entries))
	for _, e := range entries {
		cmds := make([]string, 0, len(e.Hooks))
		for _, h := range e.Hooks {
			cmds = append(cmds, h.Command)
		}
		groups = append(groups, cmds)
	}
	return groups
}

// provisionedHookGroups reads a provisioned settings.json from disk. Reading the PROVISIONED
// artifact rather than the embedded template is what makes these assertions about what an agent
// actually receives.
func provisionedHookGroups(t *testing.T, settingsPath, event string) [][]string {
	t.Helper()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading %s: %v", settingsPath, err)
	}
	return hookCommandGroups(t, data, event, settingsPath)
}

// provisionedHookCommand returns hooks.<event>[0].hooks[0].command.
func provisionedHookCommand(t *testing.T, settingsPath, event string) string {
	t.Helper()
	groups := provisionedHookGroups(t, settingsPath, event)
	if len(groups[0]) == 0 {
		t.Fatalf("no %s hook in %s", event, settingsPath)
	}
	return groups[0][0]
}

// provisionedHookCommands returns every hooks.<event>[0].hooks[*].command. SessionStart now holds
// one entry per writer, so a caller that reads only [0] can no longer see what an agent receives.
func provisionedHookCommands(t *testing.T, settingsPath, event string) []string {
	t.Helper()
	return provisionedHookGroups(t, settingsPath, event)[0]
}

// TestMemoryFreshFactory_InstallSeedsVaultAndInjectStaysSilent is T-FRESH (issue #515 Phase 3
// AC4). It crosses the install↔memory seam, which is the whole point: every other memory test
// hand-builds its factory and so is blind by construction to what `af install` creates. Driving
// runInstallRole is what makes this test fail without the Phase 3 seeding code — a version built
// on setupMemoryFixture would be a rename of the existing "empty vault" subtest and would pass
// against an empty tree.
//
//	Scenario: Provisioning an agent seeds its vault index
//	  Given a factory whose agents.json names "manager" and no .agentfactory/memory/
//	  When `af install manager` runs
//	  Then the vault and its index.md exist, the index says an empty vault is normal,
//	  And `af memory check --inject` still emits zero bytes while `af memory list` says the same,
//	  And a second install never overwrites the index.
func TestMemoryFreshFactory_InstallSeedsVaultAndInjectStaysSilent(t *testing.T) {
	dir := setupFactoryDir(t)
	if _, err := runInstallInDir(t, dir, "manager"); err != nil {
		t.Fatalf("af install manager: %v", err)
	}

	vault := config.AgentMemoryDir(dir, "manager")
	info, err := os.Stat(vault)
	if err != nil {
		t.Fatalf("provisioning an agent must create its vault at %s: %v", vault, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s must be a directory", vault)
	}

	indexPath := filepath.Join(vault, "index.md")
	seeded, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("provisioning an agent must seed its vault index: %v", err)
	}

	// Compare the seed against the ARTIFACT the core produces for an empty vault, never against a
	// literal: the empty-state sentence exists exactly twice in the tree (store.go:455 and
	// memory.go:47) and a third hand-written copy in install.go would be unguarded by anything.
	// RebuildIndex's empty-vault output is deterministic (no timestamps), so this is byte-exact.
	throwaway := t.TempDir()
	if err := memory.RebuildIndex(throwaway, "manager"); err != nil {
		t.Fatalf("rebuilding the canonical empty index: %v", err)
	}
	canonical, err := os.ReadFile(filepath.Join(config.AgentMemoryDir(throwaway, "manager"), "index.md"))
	if err != nil {
		t.Fatalf("reading the canonical empty index: %v", err)
	}
	if string(seeded) != string(canonical) {
		t.Errorf("the seeded index is not the core's empty-vault artifact.\n seeded:\n%s\n canonical:\n%s", seeded, canonical)
	}

	want := "no memory recorded yet for manager — this is a normal state on a fresh factory."
	if !strings.Contains(string(canonical), want) {
		t.Fatalf("the core no longer writes the pinned sentence; update every copy together")
	}

	t.Chdir(filepath.Join(dir, ".agentfactory", "agents", "manager"))

	// The seed put a real file inside the vault, so this is not a restatement of the existing
	// empty-vault subtest: it proves the seed did not become an injectable or malformed note.
	out, err := execMemoryOut(t, "check", "--inject")
	assertSilentSuccess(t, out, err)

	listOut, err := execMemoryOut(t, "list")
	if err != nil {
		t.Fatalf("list on a freshly installed factory must succeed, got: %v", err)
	}
	if !strings.Contains(listOut, want) {
		t.Errorf("list must print the explicit empty state on a fresh factory, got:\n%s", listOut)
	}

	// Non-vacuity guard for the zero-byte assertion above: it must hold because the vault is
	// empty, not because listing errored.
	if notes := noteFilesUnder(t, vault); len(notes) != 0 {
		t.Errorf("seeding must create zero notes, got %v", notes)
	}

	statusOut, err := execMemoryOut(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	row := statusRowFor(t, statusOut, "manager")
	if len(row) < 6 {
		t.Fatalf("manager's status row is malformed: %q", row)
	}
	if row[4] != "0" {
		t.Errorf("a seeded index.md must never count as a malformed note; got malformed=%s (row %q)", row[4], row)
	}

	// Seed-if-absent. index.md is regenerated from the notes by RebuildIndex and may already
	// describe a populated vault, so a re-install must never rewrite it. Nothing else in the tree
	// pins this: TestInstallRole_Idempotent only checks the second run does not error, which a
	// clobbering implementation passes.
	sentinel := "OPERATOR CONTENT — must survive a re-install\n"
	if err := os.WriteFile(indexPath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runInstallInDir(t, dir, "manager"); err != nil {
		t.Fatalf("re-running af install manager: %v", err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("reading index after re-install: %v", err)
	}
	if string(after) != sentinel {
		t.Errorf("re-installing an agent overwrote an existing index.md; the seed must be seed-if-absent.\n want: %q\n got:  %q", sentinel, after)
	}
}

// TestMemoryHookNoRegression_SessionStartAppendsOnlySilentSegment is T-NOREG (issue #515 Phase 3
// AC5): with an empty store the hook's stdout is byte-identical to pre-change.
//
// That claim cannot be measured by diffing against a binary that no longer exists, so it decomposes
// into two halves that ARE verifiable in process, and both are asserted here:
//
//	Half A (structural): the memory writer occupies its own SessionStart entry and appears exactly
//	once, so it cannot cost the prime or mail writers a byte — and UserPromptSubmit is untouched.
//	Half B (behavioral): on an empty store the appended segment contributes zero bytes to stdout
//	AND stderr, so concatenating it changes no byte of the hook's output.
//	Half C: a control proving Half B is not vacuous.
func TestMemoryHookNoRegression_SessionStartAppendsOnlySilentSegment(t *testing.T) {
	for role, roleType := range map[string]string{"supervisor": "autonomous", "manager": "interactive"} {
		t.Run(roleType+" SessionStart carries the memory writer in its own entry", func(t *testing.T) {
			dir := setupFactoryDir(t)
			if _, err := runInstallInDir(t, dir, role); err != nil {
				t.Fatalf("af install %s: %v", role, err)
			}
			settings := filepath.Join(dir, ".agentfactory", "agents", role, ".claude", "settings.json")

			cmds := provisionedHookCommands(t, settings, "SessionStart")
			if want := sessionStartHookPrefix + memoryHookSegment; cmds[len(cmds)-1] != want {
				t.Errorf("%s SessionStart's memory writer is not its own last entry.\n want: %q\n got:  %q", roleType, want, cmds[len(cmds)-1])
			}
			n := 0
			for _, c := range cmds {
				n += strings.Count(c, memoryHookSegment)
			}
			if n != 1 {
				t.Errorf("the memory segment must appear exactly once across SessionStart, got %d", n)
			}
		})
	}

	// Injection is SessionStart-only (ux.md U-A). Nothing else in the tree asserts anything about
	// UserPromptSubmit in either template, so this is the only mechanized guard against the clause
	// landing on the wrong hook — the failure the phase's shell criterion describes as "a count of
	// 2 in a file".
	t.Run("UserPromptSubmit is untouched in both templates", func(t *testing.T) {
		dir := setupFactoryDir(t)
		for role := range map[string]struct{}{"manager": {}, "supervisor": {}} {
			if _, err := runInstallInDir(t, dir, role); err != nil {
				t.Fatalf("af install %s: %v", role, err)
			}
			settings := filepath.Join(dir, ".agentfactory", "agents", role, ".claude", "settings.json")
			cmd := provisionedHookCommand(t, settings, "UserPromptSubmit")
			if cmd != frozenUserPromptSubmit {
				t.Errorf("%s UserPromptSubmit changed; memory injection is SessionStart-only.\n want: %q\n got:  %q", role, frozenUserPromptSubmit, cmd)
			}
		}
	})

	t.Run("empty store contributes zero bytes to stdout and stderr", func(t *testing.T) {
		_, aliceDir := setupMemoryFixture(t)
		t.Chdir(aliceDir)

		var out string
		var err error
		stderr := captureStderr(t, func() {
			out, err = execMemoryOut(t, "check", "--inject")
		})
		assertSilentSuccess(t, out, err)
		if stderr != "" {
			t.Errorf("the appended hook segment leaked %d bytes to stderr: %q", len(stderr), stderr)
		}
		// The hook concatenates its segments, so a segment that emits nothing leaves the output
		// byte-identical to what it was before that segment existed.
		if n := len(out) + len(stderr); n != 0 {
			t.Errorf("the appended segment contributed %d bytes; SessionStart output is no longer byte-identical to pre-change", n)
		}
	})

	t.Run("control: a seeded note does produce bytes", func(t *testing.T) {
		factoryRoot, aliceDir := setupMemoryFixture(t)
		seedNote(t, factoryRoot, "alice", memory.Note{ID: "noreg-control", Body: "a real learning"})
		t.Chdir(aliceDir)

		out, err := execMemoryOut(t, "check", "--inject")
		if err != nil {
			t.Fatalf("check --inject with an active note: %v", err)
		}
		if out == "" {
			t.Fatal("the zero-byte assertions above are vacuous: this harness emits nothing even with an active note")
		}
	})
}
