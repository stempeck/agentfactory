package session

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// paneEffortEnv is what a pane respawned into sessionID runs under: the tmux session env Start()
// wrote, overlaid by the respawn line's inline exports (respawn-pane merges the session env in and
// the launch line's own KEY='v' tokens win).
func paneEffortEnv(ops []string, sessionID, line string, keys ...string) map[string]string {
	env := map[string]string{}
	for _, op := range ops {
		if rest, ok := strings.CutPrefix(op, "SetEnvironment "+sessionID+" "); ok {
			if k, v, ok := strings.Cut(rest, "="); ok {
				env[k] = v
			}
		}
	}
	for _, k := range keys {
		if m := regexp.MustCompile(` ` + regexp.QuoteMeta(k) + `='([^']*)'`).FindStringSubmatch(line); m != nil {
			env[k] = m[1]
		}
	}
	return env
}

var effortUniverses711 = map[string][]string{
	"universe without the level": {"ANTHROPIC_MODEL"},
	"universe with the level":    {"ANTHROPIC_MODEL", config.EnvEffortLevel},
}

// A level the launch SELECTED is attested only on its own launch line. Persisted into the tmux
// session env, a later non-selecting respawn inherits it and attests nothing: a reduced session
// graded as unreduced.
func TestSelectedEffortLevelDoesNotOutliveItsAttestation(t *testing.T) {
	for name, universe := range effortUniverses711 {
		t.Run(name, func(t *testing.T) {
			mgr, fake := startMouseAgent(t, nil)
			mgr.c.ModelEnv = []config.EnvVar{
				{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
				{Key: config.EnvEffortLevel, Value: "medium"},
				{Key: config.EnvEffortObjective, Value: "efficiency"},
				{Key: config.EnvEffortStepLabel, Value: "step-1"},
				{Key: config.EnvEffortFormula, Value: "offpath"},
			}
			mgr.c.ModelKeyUniverse = universe
			if err := mgr.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if first := startupLine(t, mgr); !strings.Contains(first, " "+config.EnvEffortLevel+"='medium'") {
				t.Fatalf("the selecting launch line does not carry the selected level inline: %s", first)
			}
			assertNoTmuxEnvKey(t, fake.ops, mgr.SessionID(), config.EnvEffortLevel)

			mgr.c.ModelEnv = []config.EnvVar{
				{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
				{Key: config.EnvEffortObjective, Value: ""},
				{Key: config.EnvEffortStepLabel, Value: ""},
				{Key: config.EnvEffortFormula, Value: ""},
			}
			respawn := startupLine(t, mgr)
			pane := paneEffortEnv(fake.ops, mgr.SessionID(), respawn, config.EnvEffortLevel, config.EnvEffortObjective)
			if pane[config.EnvEffortLevel] == "medium" && pane[config.EnvEffortObjective] == "" {
				t.Errorf("the non-selecting respawn runs at the previous launch's selected level with no "+
					"attestation:\nsession-env ops=%v\nrespawn line=%s", fake.ops, respawn)
			}
		})
	}
}

// The #707 protection is for a DECLARED level: a non-selecting Start (empty attestation) still
// exports it on the launch line, and never clears it there.
func TestDeclaredEffortLevelStillPersistedAtStart(t *testing.T) {
	for name, universe := range effortUniverses711 {
		t.Run(name, func(t *testing.T) {
			mgr, fake := startMouseAgent(t, nil)
			mgr.c.ModelEnv = []config.EnvVar{
				{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
				{Key: config.EnvEffortLevel, Value: "high"},
				{Key: config.EnvEffortObjective, Value: ""},
				{Key: config.EnvEffortStepLabel, Value: ""},
				{Key: config.EnvEffortFormula, Value: ""},
			}
			mgr.c.ModelKeyUniverse = universe
			if err := mgr.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			line := startupLine(t, mgr)
			if !strings.Contains(line, " "+config.EnvEffortLevel+"='high'") || hasUnsetToken(line, config.EnvEffortLevel) {
				t.Errorf("a declared level is not exported on the launch line, or is cleared there: %s", line)
			}
			assertNoTmuxEnvKey(t, fake.ops, mgr.SessionID(), config.EnvEffortLevel)
		})
	}
}
