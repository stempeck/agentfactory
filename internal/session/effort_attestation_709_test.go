package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

var effortAttestationTestKeys = []string{"AF_EFFORT_OBJECTIVE", "AF_EFFORT_STEP_LABEL", "AF_EFFORT_FORMULA"}

// TestEffortAttestationClearsOnEveryLaunch is issue #709 K2's cross-launch hygiene, the attestation
// twin of TestEffortLevelClearsOnProfileSwitch. A session reused by a relaunch inherits its previous
// environment, so a relaunch whose leg selected nothing — including one under no profile at all,
// which never reaches withEffortLevel — would otherwise keep attesting the previous session's
// reduction to the grader.
func TestEffortAttestationClearsOnEveryLaunch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		modelEnv []config.EnvVar
	}{
		{"no profile", nil},
		{"profile, nothing selected", []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, fake := startMouseAgent(t, nil)
			mgr.c.ModelEnv = tc.modelEnv
			if err := mgr.Start(); err != nil {
				t.Fatalf("Start: unexpected error: %v", err)
			}
			inline := startupLine(t, mgr)
			for _, key := range effortAttestationTestKeys {
				assertNoTmuxEnvKey(t, fake.ops, mgr.SessionID(), key)
				if !strings.Contains(inline, " "+key+"=''") {
					t.Errorf("the launch line does not export %s='': %s", key, inline)
				}
			}
		})
	}
}

// A selecting launch carries the attestation in its model env; the clear must not undo it.
func TestEffortAttestationCarriedBySelectingLaunchIsNotCleared(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.c.ModelEnv = []config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
		{Key: config.EnvEffortLevel, Value: "medium"},
		{Key: "AF_EFFORT_OBJECTIVE", Value: "efficiency"},
		{Key: "AF_EFFORT_STEP_LABEL", Value: "step-1"},
		{Key: "AF_EFFORT_FORMULA", Value: "offpath"},
	}
	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}
	inline := startupLine(t, mgr)
	for _, key := range effortAttestationTestKeys {
		assertNoTmuxEnvKey(t, fake.ops, mgr.SessionID(), key)
		if strings.Contains(inline, " "+key+"=''") {
			t.Errorf("the launch line clears %s after exporting it: %s", key, inline)
		}
	}
	if !strings.Contains(inline, "AF_EFFORT_OBJECTIVE='efficiency'") {
		t.Errorf("the launch line does not carry the selected objective: %s", inline)
	}
}
