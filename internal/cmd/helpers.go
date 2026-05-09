package cmd

import (
	"fmt"
	"os"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// newIssueStore is the seam tests override to substitute an in-memory store.
// Production default constructs an mcpstore backed by the in-tree Python MCP
// server; the adapter lazy-starts the server on first use. Tests override
// this to return memstore.New() (or memstore.NewWithActor for actor-scoped
// behavior) so they don't require Python 3.12 on the test host.
var newIssueStore = func(wd, actor string) (issuestore.Store, error) {
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return nil, err
	}
	return mcpstore.New(root, actor)
}

func getWd() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("cannot get working directory: %w", err)
	}
	return wd, nil
}

// resolveAgentName determines the agent name from cwd using three-tier resolution:
//  1. Try FindLocalRoot(cwd) + DetectAgentFromCwd — handles worktree agents
//  2. Fall back to DetectAgentFromCwd(cwd, factoryRoot) — handles factory-root agents
//  3. Fall back to AF_ROLE env var — handles cases where path detection fails
//     OR returns a name that is not a member of agents.json (wrong-but-no-error,
//     e.g. a stale or typo directory under .agentfactory/agents/).
//
// The AF_ROLE fallback is consulted on BOTH path-detection error AND membership
// failure. AF_ROLE is set by session.Manager from a trusted source (validated
// against agents.json before the session is launched), so its value is honored
// unconditionally when consulted.
//
// Contract: resolveAgentName REQUIRES a loadable agents.json under factoryRoot
// to authoritatively return a path-derived name. If agents.json is missing,
// unreadable, or malformed, the path-derived name cannot be validated and is
// treated as a membership miss — AF_ROLE is consulted, otherwise an error is
// returned. This closes the silent-skip described in GitHub issue #89.
//
// This is the single source of truth for agent identity resolution. All
// command-level agent detection (detectSender, detectAgentName,
// detectCreatingAgent, detectRole) should delegate to this function.
func resolveAgentName(cwd, factoryRoot string) (string, error) {
	localRoot, err := config.FindLocalRoot(cwd)
	if err != nil {
		localRoot = factoryRoot
	}

	// Try localRoot first (handles worktree agent dirs)
	agentName, err := config.DetectAgentFromCwd(cwd, localRoot)
	if err != nil && localRoot != factoryRoot {
		// Fall back to factoryRoot
		agentName, err = config.DetectAgentFromCwd(cwd, factoryRoot)
	}

	// Membership gate: a path-derived name is only authoritative if it is a
	// member of agents.json. DetectAgentFromCwd returns parts[2] of the path
	// split without any agents.json check, so a cwd at
	// .agentfactory/agents/<typo>/ yields ("typo", nil). Treat that as a
	// detection failure and consult AF_ROLE — see GitHub issue #88.
	//
	// If agents.json cannot be loaded at all (missing, unreadable, malformed),
	// the path-derived name cannot be validated — treat the same as a
	// membership miss so the AF_ROLE fallback is consulted rather than
	// silently returning an unverified name. See GitHub issue #89.
	if err == nil {
		agentsCfg, cfgErr := config.LoadAgentConfig(config.AgentsConfigPath(factoryRoot))
		switch {
		case cfgErr != nil:
			err = fmt.Errorf("cannot validate agent %q: loading agents.json: %w", agentName, cfgErr)
		default:
			if _, ok := agentsCfg.Agents[agentName]; !ok {
				err = fmt.Errorf("agent %q not found in agents.json", agentName)
			}
		}
	}

	if err != nil {
		// Fallback: AF_ROLE env var (set by session.Manager from a trusted
		// name — already validated against agents.json at the source).
		if envRole := os.Getenv("AF_ROLE"); envRole != "" {
			return envRole, nil
		}
		return "", err
	}
	return agentName, nil
}
