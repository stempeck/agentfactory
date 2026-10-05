package session

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// An older af wrote every launch family into the tmux SESSION env; a respawn reuses that session,
// so the recycle must unset those copies. These tests pin which keys that unset covers.

var p724d1Quintet = []string{"AF_ROOT", "AF_ROLE", "AF_ACTOR", "AF_WORKTREE", "AF_WORKTREE_ID"}

// p724d1Universe mixes a profile-only key with keys a family already owns (duplicates), the API key
// security.md I2 never auto-clears, a shell-critical name and a name that is not an identifier.
var p724d1Universe = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "D1_PROFILE_ONLY_KEY", envGitAuthorName, "PATH", "BAD-NAME"}

const p724d1IntegrationOnlyKey = "D1_INTEGRATION_ONLY_KEY"

func p724d1UniverseOnly() LaunchContributions {
	return LaunchContributions{
		ModelKeyUniverse:       slices.Clone(p724d1Universe),
		IntegrationKeyUniverse: []string{p724d1IntegrationOnlyKey},
	}
}

func p724d1ScrubKeys(t *testing.T, c LaunchContributions) []string {
	t.Helper()
	m := newTestManager(t.TempDir(), "scrubber", config.AgentEntry{Type: "autonomous"})
	m.SetLaunchContributions(&c)
	keys := m.StaleTmuxEnvKeys()
	for _, k := range keys {
		if !config.IsValidEnvKeyName(k) {
			t.Fatalf("StaleTmuxEnvKeys returned %q, which is not an env key name", k)
		}
	}
	return keys
}

func TestPR724_T9_ScrubKeysCoverStaleFamilies(t *testing.T) {
	keys := p724d1ScrubKeys(t, p724d1UniverseOnly())

	named := []string{
		"AF_BUILD_HOST", "AF_BUILD_MODE", "GIT_AUTHOR_NAME", "GIT_CONFIG_COUNT", "AF_COAUTHOR_NAME",
		config.EnvEffortLevel, "ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN",
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OPENAI_API_KEY", config.EnvEffortObjective, "D1_PROFILE_ONLY_KEY",
	}
	want := slices.Concat(named, managerOwnedVars, redirectFamilyVars, telemetryFamilyVars, afGatewayUpstreamAuthVars, effortAttestationVars)
	for _, k := range want {
		if !slices.Contains(keys, k) {
			t.Errorf("scrub set lacks %s, so a stale session-scope copy of it survives every recycle\nset: %v", k, keys)
		}
	}
}

func TestPR724_T9_ScrubKeysExcludeQuintetAPIKeyShellCriticalAndIntegrationKeys(t *testing.T) {
	keys := p724d1ScrubKeys(t, p724d1UniverseOnly())
	if !slices.Contains(keys, "AF_BUILD_HOST") {
		t.Fatalf("scrub set lacks AF_BUILD_HOST; exclusions cannot be judged on it: %v", keys)
	}

	excluded := slices.Concat(p724d1Quintet, []string{"ANTHROPIC_API_KEY", "BAD-NAME", p724d1IntegrationOnlyKey}, config.ShellCriticalEnvVars)
	for _, k := range excluded {
		if slices.Contains(keys, k) {
			t.Errorf("scrub set must not contain %s\nset: %v", k, keys)
		}
	}
}

func TestPR724_T9_ScrubKeysDeduplicatedAndDeterministic(t *testing.T) {
	first := p724d1ScrubKeys(t, p724d1UniverseOnly())
	if !slices.Contains(first, "AF_BUILD_HOST") {
		t.Fatalf("scrub set lacks AF_BUILD_HOST; cannot judge its shape: %v", first)
	}

	seen := map[string]bool{}
	for _, k := range first {
		if seen[k] {
			t.Errorf("scrub set lists %s twice: %v", k, first)
		}
		seen[k] = true
	}
	if second := p724d1ScrubKeys(t, p724d1UniverseOnly()); !slices.Equal(first, second) {
		t.Errorf("scrub set order is not deterministic:\nfirst:  %v\nsecond: %v", first, second)
	}
}

// The launch line's exports override anything inherited, so the scrub does not need to know what
// this launch carries; a carried key is unset at session scope all the same.
func TestPR724_T9_ScrubKeysIgnoreWhatTheLaunchCarries(t *testing.T) {
	bare := p724d1ScrubKeys(t, p724d1UniverseOnly())

	carrying := p724d1UniverseOnly()
	carrying.ModelEnv = []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-carried"}, {Key: config.EnvEffortLevel, Value: "high"}}
	carrying.TelemetryEnv = []config.EnvVar{{Key: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://127.0.0.1:4318"}}
	carrying.GitAuthorName, carrying.GitAuthorEmail = "Carried", "carried@example.com"
	carrying.GitHooksDir, carrying.CoauthorName, carrying.CoauthorEmail = "/hooks", "Co", "co@example.com"
	carrying.BuildHost = &config.BuildHostConfig{Mode: "local"}
	carried := p724d1ScrubKeys(t, carrying)

	for _, k := range []string{"AF_BUILD_HOST", "AF_BUILD_MODE", "ANTHROPIC_MODEL", "GIT_AUTHOR_NAME", config.EnvEffortLevel, "OTEL_EXPORTER_OTLP_ENDPOINT"} {
		if !slices.Contains(carried, k) {
			t.Errorf("a launch that carries %s must still scrub its session copy; set: %v", k, carried)
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(bare)), slices.Sorted(slices.Values(carried))) {
		t.Errorf("scrub set depends on what the launch carries:\nbare:     %v\ncarrying: %v", bare, carried)
	}
}

// Start() runs only on a fresh session, so the unset primitive stays off its seam: a Start that
// cannot reach UnsetEnvironment cannot break the zero-unset K14 contract.
func TestPR724_T9_KeepStartTmuxSeamFreeOfUnset(t *testing.T) {
	if _, ok := reflect.TypeOf((*tmuxClient)(nil)).Elem().MethodByName("UnsetEnvironment"); ok {
		t.Error("tmuxClient (Manager.Start/Stop seam) must not carry UnsetEnvironment; the scrub belongs to the respawn seam only")
	}
}
