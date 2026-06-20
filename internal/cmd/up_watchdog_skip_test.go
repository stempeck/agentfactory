package cmd

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestWatchdogLaunchSkip_EmptyAgentsMap_Skips pins T5 (PR #410) on the `af up` side:
// when agents.json parsed to an EMPTY map, every configured watchdog_agents name is
// unknown, so watchdogLaunchSkip must SKIP the launch — an empty map is NOT a transient
// read. The nil-agentsCfg transient-read path (genuine read failure) is unchanged: it
// still launches the configured scope unvalidated.
func TestWatchdogLaunchSkip_EmptyAgentsMap_Skips(t *testing.T) {
	empty := &config.AgentConfig{Agents: map[string]config.AgentEntry{}}
	skip, reason := watchdogLaunchSkip([]string{"manager", "supervisor"}, empty)
	if !skip {
		t.Fatalf("empty agents.json map must SKIP launch (all names unknown), got skip=false reason=%q", reason)
	}
}
