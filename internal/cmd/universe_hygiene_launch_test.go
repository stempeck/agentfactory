package cmd

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// universeLaunchTestKey is the operator-added profile key issue #602 is about. It is in
// neither hardcoded hygiene family, which is why nothing used to clear it.
const universeLaunchTestKey = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"

// TestRespawnUniverseSweepsStaleProfileKey is the end-to-end proof through the REAL respawn
// path: a factory whose models.json defines a window-bearing profile, respawning an agent that
// resolves no profile at all. The rebuilt launch line must shed the key. This exercises the
// wiring (K2 union → unconditional setter → inline twin), not just the session-package unit.
func TestRespawnUniverseSweepsStaleProfileKey(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"bigwindow": {"ANTHROPIC_MODEL": "claude-opus-4", universeLaunchTestKey: "220000"},
		},
	})

	mock := &mockTmux{}
	if err := respawnSession(RespawnOptions{
		FactoryRoot:  dir,
		AgentName:    "manager",
		AgentEntry:   config.AgentEntry{Type: "interactive"},
		AgentWorkDir: config.AgentDir(dir, "manager"),
		PaneID:       "%0",
		CmdPrefix:    "sleep 60 && ",
		Tx:           mock,
	}); err != nil {
		t.Fatalf("respawnSession: %v", err)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Fatalf("RespawnPane should be called once, got %d", len(mock.respawnPaneCalls))
	}
	cmd := mock.respawnPaneCalls[0].cmd

	if !strings.Contains(cmd, "unset "+universeLaunchTestKey) {
		t.Errorf("a respawn resolving no profile must unset the key a window-bearing profile defines; got: %s", cmd)
	}
	if strings.Contains(cmd, universeLaunchTestKey+"=") {
		t.Errorf("the universe clears by true unset, never by an assignment; got: %s", cmd)
	}
	if !strings.HasPrefix(cmd, "sleep 60 && ") {
		t.Errorf("the respawn prefix must survive the new command shape; got: %s", cmd)
	}
	assertRespawnCommandParses(t, cmd)
}

// TestRespawnMalformedModelsUniverseEmptyNeverBricks pins the never-brick posture on the path
// that matters most: a models.json that cannot be parsed must leave the respawn working, with
// an empty universe and therefore a launch line shaped exactly as before. A registry read that
// failed closed here would take down every watchdog and handoff respawn in the fleet.
func TestRespawnMalformedModelsUniverseEmptyNeverBricks(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeRawModels(t, dir, "this is not json{{{")

	mock := &mockTmux{}
	if err := respawnSession(RespawnOptions{
		FactoryRoot:  dir,
		AgentName:    "manager",
		AgentEntry:   config.AgentEntry{Type: "interactive"},
		AgentWorkDir: config.AgentDir(dir, "manager"),
		PaneID:       "%0",
		CmdPrefix:    "sleep 60 && ",
		Tx:           mock,
	}); err != nil {
		t.Fatalf("a malformed models.json must warn, not brick a respawn; got: %v", err)
	}
	cmd := mock.respawnPaneCalls[0].cmd

	if strings.Contains(cmd, "unset ") {
		t.Errorf("an unloadable registry must yield an empty universe and emit no unset segment; got: %s", cmd)
	}
	if !strings.Contains(cmd, " && claude --dangerously-skip-permissions") {
		t.Errorf("an empty universe must leave the launch line shape untouched; got: %s", cmd)
	}
	assertRespawnCommandParses(t, cmd)
}

// TestLaunchModelKeyUniverseUnionIsSortedAndComplete pins K2 itself: the union spans ALL
// profiles (a key defined in only one still has to be clearable when another is selected) and
// is sorted, because this slice reaches the launch line verbatim as `unset K1 K2 …` and Go
// randomizes map iteration per range statement.
func TestLaunchModelKeyUniverseUnionIsSortedAndComplete(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"bigwindow": {"ANTHROPIC_MODEL": "claude-opus-4", universeLaunchTestKey: "220000"},
			"plain":     {"ANTHROPIC_MODEL": "claude-sonnet-4"},
			"beta":      {"ANTHROPIC_MODEL": "claude-opus-4", "ANTHROPIC_BETA": "context-1m"},
		},
	})

	got := launchModelKeyUniverse(dir)
	want := []string{"ANTHROPIC_BETA", "ANTHROPIC_MODEL", universeLaunchTestKey}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("universe must be the sorted union across every profile.\ngot:  %v\nwant: %v", got, want)
	}
}

// TestLaunchModelKeyUniverseEmptyWhenUnreadable covers both degrade paths in one place: a
// registry that cannot be parsed, and a factory that has no models.json at all. Both must
// yield an empty universe — the first so a broken file cannot start clearing arbitrary keys,
// the second so a stock factory's launch line stays byte-identical.
func TestLaunchModelKeyUniverseEmptyWhenUnreadable(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		dir := setupTestFactoryForDone(t, "manager")
		writeRawModels(t, dir, "{{{not json")
		if got := launchModelKeyUniverse(dir); len(got) != 0 {
			t.Errorf("an unloadable registry must yield an empty universe; got: %v", got)
		}
	})
	t.Run("absent", func(t *testing.T) {
		dir := setupTestFactoryForDone(t, "manager")
		if got := launchModelKeyUniverse(dir); len(got) != 0 {
			t.Errorf("a factory with no models.json must yield an empty universe; got: %v", got)
		}
	})
}

// assertRespawnCommandParses fails unless the respawn-prefixed launch line is valid shell. The
// universe channel emits a true `unset`, which cannot ride the export statement and so adds a
// command segment; that new shape has to survive the `sleep N && ` prefix respawnSession
// prepends. bash -n parses without executing.
func assertRespawnCommandParses(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH; skipping shell-validity check")
	}
	check := exec.Command("bash", "-n")
	check.Stdin = strings.NewReader(script)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("respawn command is not valid shell: %v\n%s\nscript: %s", err, out, script)
	}
}
