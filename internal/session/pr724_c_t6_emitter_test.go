package session

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// p724cAFLaunchKeys is every key this Manager exports on its own authority, plus the effort level and the
// Anthropic API key, which no integration may set either.
func p724cAFLaunchKeys() []string {
	return slices.Concat(managerOwnedVars, []string{config.EnvEffortLevel, envAPIKey})
}

// p724cHostileIntegrationEnv carries every af launch key, plus the git config channel that overrides af's
// trailer hook, plus one ordinary integration key that must still be exported.
func p724cHostileIntegrationEnv() []config.EnvVar {
	var env []config.EnvVar
	for _, k := range append(p724cAFLaunchKeys(), "GIT_CONFIG_PARAMETERS") {
		v := "mallory-" + strings.ToLower(k)
		if k == envAPIKey {
			v = "file:.agentfactory/secrets/stolen"
		}
		env = append(env, config.EnvVar{Key: k, Value: v})
	}
	return append(env, config.EnvVar{Key: "ACME_INT_A", Value: "v"})
}

func p724cAssignments(line, key string) int {
	return len(regexp.MustCompile(`(^| )`+regexp.QuoteMeta(key)+`=`).FindAllStringIndex(line, -1))
}

func TestPR724_T6_IntegrationEnvNeverOverridesAFEmittedKeys(t *testing.T) {
	m := integrationTestManager()
	m.c.ModelEnv = []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}, {Key: config.EnvEffortLevel, Value: "high"}}
	m.c.GitAuthorName, m.c.GitAuthorEmail = "agentfactory-cli", "af@example"
	m.c.GitHooksDir, m.c.CoauthorName, m.c.CoauthorEmail = "/f/githooks", "Claude", "c@example"
	m.c.BuildHost = &config.BuildHostConfig{Mode: "ssh", Host: "mac.local", User: "builder", MountPath: "/mnt"}
	m.c.IntegrationEnv = p724cHostileIntegrationEnv()

	line := startupLine(t, m)

	if strings.Contains(line, "mallory") || strings.Contains(line, "stolen") {
		t.Errorf("an integration value for an af launch key reached the launch line:\n%s", line)
	}
	for _, k := range append(slices.Clone(managerOwnedVars), config.EnvEffortLevel) {
		if n := p724cAssignments(line, k); n != 1 {
			t.Errorf("%s assigned %d times, want exactly once (af's own):\n%s", k, n, line)
		}
	}
	for _, k := range []string{envAPIKey, "GIT_CONFIG_PARAMETERS"} {
		if n := p724cAssignments(line, k); n != 0 {
			t.Errorf("%s assigned %d times; an integration may never export it:\n%s", k, n, line)
		}
	}
	indexOrFail(t, line, " ACME_INT_A='v'")
	assertShellParses(t, line)
}

// git identity, the trailer and the build host are only emitted when af has a value for them, so the skip
// must be by membership: on an ambient-identity factory a manifest key would otherwise be the only
// assignment on the line.
func TestPR724_T6_IntegrationEnvAFOwnedKeySkippedWhenAFEmitsNone(t *testing.T) {
	m := integrationTestManager()
	m.c.IntegrationEnv = p724cHostileIntegrationEnv()

	line := startupLine(t, m)

	for _, k := range append(p724cAFLaunchKeys(), "GIT_CONFIG_PARAMETERS") {
		if n := p724cAssignments(line, k); n != 0 {
			t.Errorf("%s assigned %d times from an integration; af launch keys are never integration-exported:\n%s", k, n, line)
		}
	}
	indexOrFail(t, line, " ACME_INT_A='v'")
}

// A skipped key must leave no trace. Recording it as emitted would also suppress the profile-switch
// `unset CLAUDE_CODE_EFFORT_LEVEL`, because the universe pass skips every key it believes was emitted.
func TestPR724_T6_SkippedAFOwnedKeysLeaveLineByteIdentical(t *testing.T) {
	build := func(env []config.EnvVar) string {
		m := integrationTestManager()
		m.c.ModelEnv = []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}}
		m.c.ModelKeyUniverse = []string{"ANTHROPIC_MODEL", config.EnvEffortLevel}
		m.c.IntegrationEnv = env
		m.c.IntegrationKeyUniverse = []string{"ACME_INT_A"}
		return startupLine(t, m)
	}
	clean := build([]config.EnvVar{{Key: "ACME_INT_A", Value: "v"}})
	hostile := build(p724cHostileIntegrationEnv())
	if !hasUnsetToken(clean, config.EnvEffortLevel) || !hasUnsetToken(hostile, config.EnvEffortLevel) {
		t.Errorf("the profile switch must still unset %s:\nclean:   %s\nhostile: %s", config.EnvEffortLevel, clean, hostile)
	}
	if hostile != clean {
		t.Errorf("af launch keys from an integration changed the launch line.\n got: %s\nwant: %s", hostile, clean)
	}
}

// A key added to managerOwnedVars without a matching manifest denial fails here, so the validator and the
// emitter cannot drift apart.
func TestPR724_T6_EveryAFOwnedLaunchKeyIsDeniedInManifests(t *testing.T) {
	for _, k := range p724cAFLaunchKeys() {
		t.Run(k, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "acme")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := "name = \"acme\"\ndescription = \"x\"\n\n[env]\n" + k + " = \"file:.agentfactory/secrets/x\"\n"
			if err := os.WriteFile(filepath.Join(dir, config.IntegrationManifestFile), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := config.LoadIntegrationManifest(dir)
			if want := "may not set " + k + " (af launch key)"; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("manifest [env] %s: err = %v, want the clause %q", k, err, want)
			}
		})
	}
}
