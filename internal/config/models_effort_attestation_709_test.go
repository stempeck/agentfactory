package config

import (
	"strings"
	"testing"
)

// TestProfileCannotDeclareAttestationKeys pins issue #709 E-b: the launch line is the only carrier of
// the effort attestation, so a profile that could export one of its keys would attest every session
// it launches as reduced without the actuator ever selecting a level.
func TestProfileCannotDeclareAttestationKeys(t *testing.T) {
	for _, key := range []string{"AF_EFFORT_OBJECTIVE", "AF_EFFORT_STEP_LABEL", "AF_EFFORT_FORMULA"} {
		t.Run(key, func(t *testing.T) {
			err := validateModelProfile("forger", map[string]string{"ANTHROPIC_MODEL": "claude-opus-5-5", key: "efficiency"})
			if err == nil || !strings.Contains(err.Error(), "reserved for the session manager") {
				t.Errorf("a profile declaring %s: err = %v, want it rejected as reserved for the session manager", key, err)
			}
		})
	}
}

// The declared level is the one effort key a profile legitimately owns (#707); denylisting it with
// the attestation family would take a fixed level away from every operator who set one.
func TestProfileMayStillDeclareEffortLevel(t *testing.T) {
	if err := validateModelProfile("declared", map[string]string{"ANTHROPIC_MODEL": "claude-opus-5-5", EnvEffortLevel: "medium"}); err != nil {
		t.Errorf("a profile declaring %s=medium was rejected: %v", EnvEffortLevel, err)
	}
}
