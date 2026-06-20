package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

type respawnTmux interface {
	ClearHistory(pane string) error
	RespawnPane(pane, command string) error
}

// cmdTmux is the full union of *tmux.Tmux methods the cmd layer drives across
// up.go, down.go, done.go, and dispatch.go. The last three (GetPaneCommand,
// IsAgentRunning, SetEnvironment) are not called yet — they are declared up
// front because Phase 4's watchdog health-gate + AF_ROOT export will need them,
// so the seam surface is fixed once (Round-2 HIGH-1). It exists so those command
// paths can be driven with a fake in tests.
type cmdTmux interface {
	IsAvailable() bool
	HasSession(name string) (bool, error)
	NewSession(name, workDir string) error
	KillSession(name string) error
	SendKeys(session, keys string) error
	SendKeysDelayed(session, keys string, delayMs int) error
	GetPaneCommand(session string) (string, error)
	IsAgentRunning(session string, expectedPaneCommands ...string) bool
	SetEnvironment(session, key, value string) error
}

// Compile-time check: the real *tmux.Tmux must satisfy cmdTmux (R-4 discipline).
var _ cmdTmux = (*tmux.Tmux)(nil)

// newCmdTmux is the seam tests override to inject a fake tmux client into the
// cmd-layer watchdog/dispatch/terminate paths. Production default returns the
// real *tmux.Tmux.
var newCmdTmux = func() cmdTmux { return tmux.NewTmux() }

type RespawnOptions struct {
	FactoryRoot  string
	AgentName    string
	AgentEntry   config.AgentEntry
	PaneID       string
	CmdPrefix    string
	WorktreePath string
	WorktreeID   string
	Tx           respawnTmux
}

func respawnSession(opts RespawnOptions) error {
	mgr := session.NewManager(opts.FactoryRoot, opts.AgentName, opts.AgentEntry)
	mgr.SetInitialPrompt("af prime")
	if opts.WorktreePath != "" {
		_ = mgr.SetWorktree(opts.WorktreePath, opts.WorktreeID)
	}
	// Carry the git identity fallback + centralized trailer across respawns
	// (handoff/compact-handoff/watchdog) — without this, respawned agents would
	// commit without them (issue #371 G-B sibling-entrypoint).
	wireGitIdentity(mgr, opts.FactoryRoot, opts.WorktreePath)
	respawnCmd := opts.CmdPrefix + mgr.BuildStartupCommand()
	tx := opts.Tx
	if tx == nil {
		tx = tmux.NewTmux()
	}
	_ = tx.ClearHistory(opts.PaneID)
	return tx.RespawnPane(opts.PaneID, respawnCmd)
}

// detectGitIdentity reads the ambient git identity (user.name/user.email) as
// resolved from dir — the cmd-layer I/O that feeds the pure config.ResolveIdentity
// (ADR-004: the library does no shell-out). Either value is "" when unset.
func detectGitIdentity(dir string) (name, email string) {
	return gitConfigGet(dir, "user.name"), gitConfigGet(dir, "user.email")
}

func gitConfigGet(dir, key string) string {
	c := exec.Command("git", "config", "--get", key)
	if dir != "" {
		c.Dir = dir
	}
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// wireGitIdentity configures a session Manager's git identity fallback and the
// centralized Co-authored-by trailer (issue #371). The default identity is drawn
// from factory.json (default-filled to the C-3 constants); the author fallback is
// applied ONLY when no ambient identity resolves (C-4 presence-gate), while the
// trailer channel is always activated (centralized, AC-4/AC-5). Shared by every
// Start-capable launch path so no entrypoint is missed (G-B).
//
// The presence-gate is checked at workDir — the directory where the agent will
// actually commit (its worktree) — NOT the factory root: GIT_AUTHOR_* overrides
// even a repo-local user.name unconditionally, so checking the wrong directory
// could silently re-author a commit whose repo already has an identity (C-4). An
// empty workDir falls back to the factory root.
func wireGitIdentity(mgr *session.Manager, factoryRoot, workDir string) {
	def := config.DefaultGitIdentity()
	if cfg, err := config.LoadFactoryConfig(config.FactoryConfigPath(factoryRoot)); err == nil && cfg.GitIdentity != nil {
		def = cfg.GitIdentity
	}
	if workDir == "" {
		workDir = factoryRoot
	}
	ambientName, ambientEmail := detectGitIdentity(workDir)
	if name, email, apply := config.ResolveIdentity(def, ambientName, ambientEmail); apply {
		mgr.SetGitIdentity(name, email)
	}
	mgr.SetGitTrailer(config.GitHooksDir(factoryRoot), def.Name, def.Email)
}

func captureCheckpointWithFormula(ctx context.Context, cwd, notes string, mutate func(*checkpoint.Checkpoint)) error {
	cp, err := checkpoint.Capture(cwd)
	if err != nil {
		return err
	}

	if mutate != nil {
		mutate(cp)
	}

	formulaID := readHookedFormulaID(cwd)
	if formulaID != "" {
		actor := os.Getenv("AF_ACTOR")
		if store, storeErr := newIssueStore(cwd, actor); storeErr == nil {
			result, _ := store.Ready(ctx, issuestore.Filter{MoleculeID: formulaID})
			if len(result.Steps) > 0 {
				cp.WithFormula(formulaID, result.Steps[0].ID, result.Steps[0].Title)
			}
		}
	}

	cp.WithNotes(notes)
	if cp.SessionID == "" {
		cp.SessionID = os.Getenv("CLAUDE_SESSION_ID")
	}
	return checkpoint.Write(cwd, cp)
}

// newIssueStore is the seam tests override to substitute an in-memory store.
// Production default constructs an mcpstore backed by the in-tree Python MCP
// server; the adapter discovers/spawns the server in New().
//
// STORE-GUARD: in the default (non-integration) test build, storeGuardActive is
// true (storeguard_default.go), so this short-circuits to an in-memory store
// BEFORE resolving the factory root or contacting Python — a default-suite test
// can never spawn or mutate the operator's py.issuestore.server (#317). The
// integration build sets storeGuardActive false (storeguard_integration.go), so
// `-tags=integration` and production keep the real mcpstore (AC-6). The
// installMemStore opt-in (sling_test.go) still works and is now redundant.
var newIssueStore = func(wd, actor string) (issuestore.Store, error) {
	if storeGuardActive {
		return memstore.NewWithActor(actor), nil
	}
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
