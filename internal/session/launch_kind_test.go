package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestLaunchKindClearsOnEveryLaunch is the launch-kind twin of TestEffortAttestationClearsOnEveryLaunch.
// A reused pane keeps the environment of the launch before it, so a launch that is not a respawn must
// clear the mark, or the session it starts would read as a respawn and its startup would go unmeasured.
func TestLaunchKindClearsOnEveryLaunch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		modelEnv []config.EnvVar
	}{
		{"no profile", nil},
		{"profile", []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, fake := startMouseAgent(t, nil)
			mgr.c.ModelEnv = tc.modelEnv
			if err := mgr.Start(); err != nil {
				t.Fatalf("Start: unexpected error: %v", err)
			}
			line := sentLine(t, fake.ops, mgr.SessionID())
			assertNoTmuxEnvKey(t, fake.ops, mgr.SessionID(), "AF_LAUNCH_KIND")
			if n := strings.Count(line, " AF_LAUNCH_KIND=''"); n != 1 {
				t.Errorf("Start's launch line exports AF_LAUNCH_KIND='' %d times, want once: %s", n, line)
			}
			if strings.Contains(line, "AF_LAUNCH_KIND='respawn'") {
				t.Errorf("a launch with no LaunchKind carries the respawn mark: %s", line)
			}
		})
	}
}

// TestLaunchKindCarriedOnlyWhenSet: the mark is a field of its own, so it leaves the ModelEnv
// presence gate alone — a no-profile respawn keeps the entry's --model and gains no redirect clear.
func TestLaunchKindCarriedOnlyWhenSet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		model    string
		modelEnv []config.EnvVar
	}{
		{"no profile", "claude-opus-4", nil},
		{"profile", "", []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := config.AgentEntry{Type: "autonomous", Description: "test", Model: tc.model}
			build := func(kind string) string {
				mgr := NewManager("/tmp/factory", "manager", entry)
				mgr.SetLaunchContributions(&LaunchContributions{ModelEnv: tc.modelEnv, LaunchKind: kind})
				mgr.SetInitialPrompt("af prime")
				return startupLine(t, mgr)
			}
			respawn, fresh := build("respawn"), build("")

			if n := strings.Count(respawn, "AF_LAUNCH_KIND='respawn'"); n != 1 {
				t.Errorf("the respawn line carries AF_LAUNCH_KIND='respawn' %d times, want once: %s", n, respawn)
			}
			if strings.Contains(respawn, "AF_LAUNCH_KIND=''") {
				t.Errorf("the respawn line also clears the mark it carries: %s", respawn)
			}
			if want := strings.Replace(fresh, "AF_LAUNCH_KIND=''", "AF_LAUNCH_KIND='respawn'", 1); respawn != want {
				t.Errorf("the mark moved more than its own export:\n got:  %s\n want: %s", respawn, want)
			}
			if !strings.Contains(respawn, " --model 'claude-opus-4'") {
				t.Errorf("the respawn line lost the --model flag: %s", respawn)
			}
			if tc.modelEnv == nil {
				for _, key := range redirectFamilyVars {
					if strings.Contains(respawn, " "+key+"=''") {
						t.Errorf("the mark opened the ModelEnv presence gate: a no-profile respawn clears %s: %s", key, respawn)
					}
				}
			}
		})
	}
}
