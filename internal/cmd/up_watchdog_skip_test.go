package cmd

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestWatchdogPaneScope_EmptyAgentsMap_IsFlaggedInert pins T5 (PR #410) on the `af up` side.
//
// REVISED by #596 Phase 3, deliberately. The predicate this exercised was called
// watchdogLaunchSkip and returned skip=true, meaning `af up` created no watchdog session at
// all. Decision 4 removed that: withholding the process on an empty scope is what left the
// factory's default configuration with no recovery running. The predicate is now
// watchdogPaneScopeState and reports only that the watchdog's PANE surface will be inert —
// `af up` launches either way.
//
// The T5 distinction itself is unchanged and is what this still guards: an agents.json that
// parsed successfully into an EMPTY map is NOT a transient read. Every configured name is
// genuinely unknown, so it must route to the all-unknown branch (inert AND flagged as a
// misconfiguration) rather than the nil-agentsCfg branch, which presumes the configured
// scope is real. Collapsing the two would make a broken agents.json look like a working one.
func TestWatchdogPaneScope_EmptyAgentsMap_IsFlaggedInert(t *testing.T) {
	empty := &config.AgentConfig{Agents: map[string]config.AgentEntry{}}

	pane := watchdogPaneScopeState([]string{"manager", "supervisor"}, empty)

	if !pane.inert {
		t.Fatalf("an empty agents.json map makes every configured name unknown ⇒ the pane surface is inert; got %+v", pane)
	}
	if !pane.misconfigured {
		t.Error("names that do not exist are an operator error, not a configuration choice — this must stay flagged (T5)")
	}
}

// TestWatchdogPaneScope_TransientReadPresumesConfiguredScope is the other half of the T5
// distinction, previously unpinned on the `af up` side: a nil agentsCfg means the read
// FAILED, which is not evidence that the configured names are wrong.
func TestWatchdogPaneScope_TransientReadPresumesConfiguredScope(t *testing.T) {
	pane := watchdogPaneScopeState([]string{"manager"}, nil)

	if pane.inert {
		t.Errorf("an unreadable agents.json must not be treated as all-unknown; got %+v", pane)
	}
}
