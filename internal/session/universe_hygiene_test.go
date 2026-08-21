package session

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// universeTestKey is the operator-added profile key at the heart of issue #602. It is
// deliberately in NEITHER hardcoded hygiene family, which is the whole point: before the
// universe channel existed, such a key was emitted on launch and cleared by nothing.
const universeTestKey = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"

// hasUnsetToken reports whether cmd emits a TRUE `unset key` — key appearing as a
// whitespace-delimited argument of an `unset` segment. It deliberately does not match an
// empty-string clear, so the two hygiene idioms (the families' empty-string assignment and
// the universe's true unset) can be told apart in assertions. Segment-scoped so an exported
// value that merely contains the word "unset" can never satisfy it.
func hasUnsetToken(cmd, key string) bool {
	for _, segment := range strings.Split(cmd, " && ") {
		fields := strings.Fields(segment)
		if len(fields) == 0 || fields[0] != "unset" {
			continue
		}
		for _, arg := range fields[1:] {
			if arg == key {
				return true
			}
		}
	}
	return false
}

// assertShellParses fails unless script is syntactically valid shell. It exists for the
// respawn case: the universe channel emits a true `unset` segment, which cannot ride the
// single `export` statement and therefore changes the command shape — and that new shape
// must still parse after respawnSession prepends `sleep N && ` (handoff.go builds the
// prefix; helpers.go concatenates it). bash -n parses without executing.
func assertShellParses(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH; skipping shell-validity check")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emitted command is not valid shell: %v\n%s\nscript: %s", err, out, script)
	}
}

// respawnPrefix mirrors the sleep prefix handoff.go prepends to a respawned launch line.
const respawnPrefix = "sleep 60 && "

// TestUniverseHygiene_ProfileSwitchClearsWindow is the core issue #602 proof: an agent
// relaunched under a profile that does NOT carry a universe key must leave no stale value
// at EITHER twin. The tmux twin covers a fresh Start on a reused session; the inline twin
// is the only clear a respawn ever emits.
func TestUniverseHygiene_ProfileSwitchClearsWindow(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	// Windowless relaunch: the resolved profile carries a model but not the window, while
	// the universe still knows the key exists because some profile in models.json defines it.
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL", universeTestKey})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	wantUnset := "UnsetEnvironment " + mgr.SessionID() + " " + universeTestKey
	if !hasOp(fake.ops, wantUnset) {
		t.Errorf("profile switch must unset the stale universe key at the tmux twin; want %q, ops=%v", wantUnset, fake.ops)
	}

	inline := mgr.BuildStartupCommand()
	if !hasUnsetToken(inline, universeTestKey) {
		t.Errorf("inline twin must emit a true `unset %s`; got: %s", universeTestKey, inline)
	}
	if strings.Contains(inline, universeTestKey+"=") {
		t.Errorf("inline twin must clear %s by true unset, never by an assignment; got: %s", universeTestKey, inline)
	}
	assertShellParses(t, respawnPrefix+inline)
}

// TestUniverseHygiene_SwitchToNoProfileClears covers the arm the IMPLREADME's scenario calls
// out explicitly ("or under no profile at all"): the resolved model-env set is EMPTY. The
// clearing loops must sit OUTSIDE the len(modelEnv) > 0 gate or this case silently keeps the
// stale value — which is the exact bug for an agent switched off its profile entirely.
func TestUniverseHygiene_SwitchToNoProfileClears(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelKeyUniverse([]string{universeTestKey})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	wantUnset := "UnsetEnvironment " + mgr.SessionID() + " " + universeTestKey
	if !hasOp(fake.ops, wantUnset) {
		t.Errorf("switch to no profile must still unset the stale key at the tmux twin; want %q, ops=%v", wantUnset, fake.ops)
	}

	inline := mgr.BuildStartupCommand()
	if !hasUnsetToken(inline, universeTestKey) {
		t.Errorf("switch to no profile must still emit a true `unset %s` inline; got: %s", universeTestKey, inline)
	}
	assertShellParses(t, respawnPrefix+inline)
}

// TestUniverseHygiene_EmitsWindowBothTwins is the non-vacuity companion: a launch that DOES
// carry the key must emit it and must never clear it. Without this, a loop that unset the key
// unconditionally would pass the switch test while breaking every window-bearing agent.
func TestUniverseHygiene_EmitsWindowBothTwins(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelEnv([]config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
		{Key: universeTestKey, Value: "220000"},
	})
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL", universeTestKey})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	sessionID := mgr.SessionID()
	wantSet := "SetEnvironment " + sessionID + " " + universeTestKey + "=220000"
	if !hasOp(fake.ops, wantSet) {
		t.Errorf("a carried universe key must be set at the tmux twin; want %q, ops=%v", wantSet, fake.ops)
	}
	if hasOp(fake.ops, "UnsetEnvironment "+sessionID+" "+universeTestKey) {
		t.Errorf("a carried universe key must never be unset at the tmux twin; ops=%v", fake.ops)
	}

	inline := mgr.BuildStartupCommand()
	if !strings.Contains(inline, universeTestKey+"='220000'") {
		t.Errorf("a carried universe key must be exported inline; got: %s", inline)
	}
	if hasUnsetToken(inline, universeTestKey) {
		t.Errorf("a carried universe key must never be inline-unset; got: %s", inline)
	}
	assertShellParses(t, respawnPrefix+inline)
}

// TestUniverseHygiene_NeverClearsAPIKey is the pr509_redirect_test.go guard's twin for the new
// channel. ANTHROPIC_API_KEY is a LEGAL profile key (its "" is the explicit-clear idiom), so it
// WILL appear in the raw union the cmd layer computes — the carve-out that keeps it from being
// swept lives inside this package. security.md I2: it is never auto-cleared, because a
// default-profile agent may legitimately authenticate via an ambient key.
//
// It asserts the true-unset form as well as the empty-string form: the existing pr509 guard greps
// only for the empty-string assignment, which a universe bug emitting a true unset would slip past.
func TestUniverseHygiene_NeverClearsAPIKey(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL", "ANTHROPIC_API_KEY", universeTestKey})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	if hasOp(fake.ops, "UnsetEnvironment "+mgr.SessionID()+" ANTHROPIC_API_KEY") {
		t.Errorf("universe hygiene must never unset ANTHROPIC_API_KEY at the tmux twin (security.md I2); ops=%v", fake.ops)
	}

	inline := mgr.BuildStartupCommand()
	if strings.Contains(inline, "ANTHROPIC_API_KEY=''") {
		t.Errorf("universe hygiene must never emit ANTHROPIC_API_KEY=''; got: %s", inline)
	}
	if hasUnsetToken(inline, "ANTHROPIC_API_KEY") {
		t.Errorf("universe hygiene must never true-unset ANTHROPIC_API_KEY; got: %s", inline)
	}
}

// TestUniverseHygiene_NeverClearsCarvedOutFamilies proves the new channel cannot reach into the
// two pre-existing ones. Each family keeps its own proven empty-string idiom (two idioms for two
// classes, deliberate); a universe loop that true-unset them would silently change behavior the
// #508 and #329 tests were written to pin.
func TestUniverseHygiene_NeverClearsCarvedOutFamilies(t *testing.T) {
	universe := append([]string{universeTestKey}, redirectFamilyVars...)
	universe = append(universe, telemetryFamilyVars...)

	mgr, fake := startMouseAgent(t, nil)
	// A non-family model-env key opens the model-env gate (the redirect family's inline
	// empty-string clears live inside it) while leaving every family key un-carried, so both
	// families' own hygiene is observable in the same command as the universe's.
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_BETA", Value: "context-1m"}})
	mgr.SetModelKeyUniverse(universe)

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	inline := mgr.BuildStartupCommand()
	for _, key := range append(append([]string{}, redirectFamilyVars...), telemetryFamilyVars...) {
		if hasUnsetToken(inline, key) {
			t.Errorf("universe hygiene must not true-unset carved-out family key %q; got: %s", key, inline)
		}
		if !strings.Contains(inline, key+"=''") {
			t.Errorf("carved-out family key %q must keep its own KEY='' clear; got: %s", key, inline)
		}
	}
	// The tmux twin's own family loops still own these keys, so an UnsetEnvironment op is
	// expected there; what must not happen is the universe loop reaching the carve-outs
	// inline, asserted above. Guard the one key the universe does own.
	if !hasOp(fake.ops, "UnsetEnvironment "+mgr.SessionID()+" "+universeTestKey) {
		t.Errorf("universe key must still be unset at the tmux twin; ops=%v", fake.ops)
	}
}

// TestUniverseHygiene_NeverClearsManagerOwnedVars guards a silent-clobber class the plan did not
// name. validateModelProfile denylists only the AF_* identity and OTel keys, so a profile may
// legally name a var the Manager itself exports (git identity, trailer, build host). Inline, the
// unset segment follows the export statement, so an un-carved Manager-owned key would be exported
// and then immediately wiped — a respawned agent would silently lose its git identity. This is the
// same failure mode as PR #509's auth-token clobber, one class wider.
func TestUniverseHygiene_NeverClearsManagerOwnedVars(t *testing.T) {
	managerOwned := []string{
		envGitAuthorName, envGitAuthorEmail, envGitCommitterName, envGitCommitterEmail,
		envGitConfigCount, envGitConfigKey0, envGitConfigValue0,
		envCoauthorName, envCoauthorEmail,
		"AF_BUILD_MODE", "AF_BUILD_HOST", "AF_BUILD_USER", "AF_HOST_MOUNT",
	}

	mgr, _ := startMouseAgent(t, nil)
	mgr.SetGitIdentity("Agent Factory", "agentfactory@example.com")
	mgr.SetGitTrailer("/tmp/githooks", "Claude", "noreply@example.com")
	mgr.SetBuildHost(&config.BuildHostConfig{Mode: "remote", Host: "buildbox", User: "dev", MountPath: "/mnt/af"})
	mgr.SetModelKeyUniverse(append([]string{universeTestKey}, managerOwned...))

	inline := mgr.BuildStartupCommand()
	for _, key := range managerOwned {
		if hasUnsetToken(inline, key) {
			t.Errorf("universe hygiene must not unset Manager-owned var %q it just exported; got: %s", key, inline)
		}
	}
	if !strings.Contains(inline, envGitAuthorName+"='Agent Factory'") {
		t.Errorf("git identity must survive a universe sweep; got: %s", inline)
	}
	assertShellParses(t, respawnPrefix+inline)
}

// TestUniverseHygiene_EmptyUniverseZeroDelta is the C-5 proof stated positively: a Manager with no
// universe (and one given an explicitly empty universe) must produce the byte-identical pre-change
// launch line. The two pre-existing "…_Unchanged" baselines cover the never-called case; this pins
// that calling the setter with nothing to clear is inert too, and that no `unset` segment appears.
func TestUniverseHygiene_EmptyUniverseZeroDelta(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	expected := "export AF_ROOT='/tmp/factory' AF_ROLE='ultraimplement' AF_ACTOR='ultraimplement'" +
		telemetryOffClears + " && claude --dangerously-skip-permissions"

	for _, tc := range []struct {
		name     string
		universe []string
	}{
		{"nil universe", nil},
		{"empty universe", []string{}},
		{"universe of only carved-out keys", []string{"ANTHROPIC_API_KEY", envBaseURL, envOTelHeaders}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewManager("/tmp/factory", "ultraimplement", entry)
			mgr.SetModelKeyUniverse(tc.universe)

			cmd := mgr.BuildStartupCommand()
			if cmd != expected {
				t.Errorf("empty-universe launch line must be byte-identical to the baseline.\ngot:  %s\nwant: %s", cmd, expected)
			}
			if strings.Contains(cmd, "unset ") {
				t.Errorf("an empty universe must emit no unset segment; got: %s", cmd)
			}
		})
	}
}

// TestUniverseHygiene_UnsetOrderIsDeterministic pins the emitted order. The universe is derived
// from a double map-key iteration in the cmd layer, and Go randomises map iteration per range
// statement, so an unsorted union would make the launch line differ between processes. The
// existing determinism guard calls BuildStartupCommand twice in ONE process against ONE Manager,
// so it cannot see that class; this asserts the session package preserves the order it is handed
// (the cmd layer sorts) rather than re-deriving one.
func TestUniverseHygiene_UnsetOrderIsDeterministic(t *testing.T) {
	universe := []string{"AAA_KEY", "MMM_KEY", "ZZZ_KEY"}

	var first string
	for i := 0; i < 5; i++ {
		mgr := NewManager("/tmp/factory", "ultraimplement", config.AgentEntry{Type: "autonomous"})
		mgr.SetModelKeyUniverse(universe)
		cmd := mgr.BuildStartupCommand()
		if i == 0 {
			first = cmd
			continue
		}
		if cmd != first {
			t.Fatalf("launch line must be deterministic for a fixed universe.\nrun 0: %s\nrun %d: %s", first, i, cmd)
		}
	}
	if !strings.Contains(first, "unset AAA_KEY MMM_KEY ZZZ_KEY") {
		t.Errorf("universe unset segment must preserve the order it was handed; got: %s", first)
	}
	assertShellParses(t, respawnPrefix+first)
}
