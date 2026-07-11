package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// loadModelsConfigForCrossCheck loads models.json for a NON-selecting cross-check
// caller (`af dispatch`, `af config dispatch set`). Neither path launches from a
// profile, so a models.json validation error must NOT be fatal — mirroring the launch
// path's non-selecting tolerance (resolveLaunchModelEnv's fall-through): it warns and
// returns nil, which ValidateDispatchConfig treats as "skip the per-mapping model
// cross-check". The profile-WRITING path (`af config models set` / SaveModelsConfig)
// stays strict — this tolerance is only for the read/cross-check callers.
func loadModelsConfigForCrossCheck(root string, warn io.Writer) *config.ModelsConfig {
	cfg, err := config.LoadModelsConfig(root)
	if err != nil {
		fmt.Fprintf(warn, "warning: ignoring models.json for the dispatch model cross-check (%v); proceeding without it\n", err)
		return nil
	}
	return cfg
}

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
	GetEnvironment(session, key string) (string, error)
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
	// AgentWorkDir is the agent's actual working dir — where the
	// .runtime/model_override marker lives (issue #480). The three respawn call
	// sites disagree on WorktreePath (handoff/compact pass none but run from the
	// worktree agent dir; watchdog sets WorktreePath), so each passes its known
	// agent dir here to make the respawn marker-read match the launch marker-write.
	AgentWorkDir string
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

	// Re-resolve the FULL model precedence chain on every respawn (issue #480):
	// BOTH a --model override (captured by the .runtime/model_override marker) AND a
	// durable models.json.agents default must survive handoff/compact/watchdog —
	// reading only the marker would silently revert a durable-default agent to the
	// global model on the first handoff. A respawn carries no explicit flag (cliModel
	// ""), so a broken models.json warns + falls through to the global default
	// rather than failing. Emission is structural: BuildStartupCommand() re-emits the
	// set, so no second emission path is added here (handoff_test transitivity guard).
	if _, env, _ := resolveLaunchModelEnv(opts.FactoryRoot, opts.AgentName, respawnAgentDir(opts), "", opts.AgentEntry.Model, false, os.Stderr); len(env) > 0 {
		mgr.SetModelEnv(env)
	}

	respawnCmd := opts.CmdPrefix + mgr.BuildStartupCommand()
	tx := opts.Tx
	if tx == nil {
		tx = tmux.NewTmux()
	}
	_ = tx.ClearHistory(opts.PaneID)
	return tx.RespawnPane(opts.PaneID, respawnCmd)
}

// respawnAgentDir derives the agent working dir holding the .runtime/model_override
// marker so the respawn read matches the launch write (issue #480). It
// prefers the explicit AgentWorkDir (handoff/compact pass their cwd; watchdog passes
// resolveAgentDir), then the worktree-derived dir, then the factory-root dir.
func respawnAgentDir(opts RespawnOptions) string {
	if opts.AgentWorkDir != "" {
		return opts.AgentWorkDir
	}
	if opts.WorktreePath != "" {
		return config.AgentDir(opts.WorktreePath, opts.AgentName)
	}
	return config.AgentDir(opts.FactoryRoot, opts.AgentName)
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
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return nil, err
	}
	return newIssueStoreAt(root, actor)
}

// newIssueStoreAt opens a store on an ALREADY-VALIDATED factory root, WITHOUT
// re-running resolveInvokerRoot. A read-only verb that has downgraded a
// factory-root mismatch to a warning (agents list, dispatch status) must build
// its store on that downgraded root through THIS seam: routing back through
// newIssueStore would re-resolve the same cwd and re-raise the very mismatch
// just downgraded, dropping the verb into its error envelope (issue #519 review
// follow-up). It is a seam so tests can substitute an in-memory store on the
// production path (storeGuardActive=false) without contacting Python.
var newIssueStoreAt = func(root, actor string) (issuestore.Store, error) {
	if storeGuardActive {
		return memstore.NewWithActor(actor), nil
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

// mismatchError is the ux.md-owned contract text for a factory-root mismatch
// (design-doc.md L169-175). It uses explicit argument indices because the
// cwd-resolved (clone) root appears twice: %[1]s is that root, %[2]s is the
// AF_ROOT-resolved session root. The "Error: " prefix is cobra's, not ours.
const mismatchError = `factory root mismatch: cwd resolves to %[1]s
but this session belongs to %[2]s  (AF_ROOT).
A nested factory checkout is shadowing your real factory.
cd back under your factory before re-running, or set AF_ROOT=%[1]s
to affirm you really intend to operate on the nested checkout.`

// rootMismatchError is returned by resolveInvokerRoot when the cwd-resolved root
// disagrees with the AF_ROOT-resolved session root. It carries the cwd-resolved
// root so read-only/status verbs can downgrade the refusal to a warning and
// proceed on that root (downgradeRootMismatch), while state-writing verbs let it
// propagate as a hard, pre-mutation refusal naming both roots (issue #519).
type rootMismatchError struct {
	resolved string // cwd-resolved (nested clone) root
	envRoot  string // AF_ROOT-resolved session root
}

func (e *rootMismatchError) Error() string {
	return fmt.Sprintf(mismatchError, e.resolved, e.envRoot)
}

// enclosingError is the refusal text for an env-less shell inside a nested factory
// checkout that is enclosed by a DIFFERENT factory (K5, #519 Phase 3). Like
// mismatchError it names both roots; %[1]s is the cwd-resolved (nested) root that
// the attributable AF_ROOT hatch affirms (design-doc K5, ADR-003-compatible env
// hatch), %[2]s is the enclosing factory above it.
const enclosingError = `refusing to dispatch: cwd resolves to nested factory %[1]s
which is enclosed by %[2]s.
An env-less shell inside a nested checkout would silently capture this
state-writing command into the wrong factory.
cd back under %[2]s before re-running, or set AF_ROOT=%[1]s
to affirm dispatching into the nested factory.`

// enclosingRootError is returned by resolveInvokerRoot when the cwd-resolved root is
// enclosed by a distinct factory AND no AF_ROOT affirms the intent (K5, #519). It
// mirrors rootMismatchError: it carries the resolved (nested) root so the
// CHECK-AS-WARNING verbs can downgrade the refusal to a warning and proceed on that
// root (downgradeRootMismatch), while state-writing verbs let it propagate.
type enclosingRootError struct {
	resolved  string // cwd-resolved (nested) root
	enclosing string // the enclosing factory root above it
}

func (e *enclosingRootError) Error() string {
	return fmt.Sprintf(enclosingError, e.resolved, e.enclosing)
}

// warnEnclosingRoot emits the in-session (gen-0) enclosing signal on stderr: a
// clone-born session whose AF_ROOT cross-check passes still learns, on every
// state-writing verb, that its factory is nested inside another. A no-op when the
// resolved root is not enclosed.
func warnEnclosingRoot(resolved, enclosing string) {
	if enclosing == "" {
		return
	}
	fmt.Fprintf(os.Stderr,
		"warning: factory %s is nested inside enclosing factory %s; "+
			"proceeding on the nested root (set AF_ROOT to affirm or cd back to override)\n",
		resolved, enclosing)
}

// resolveInvokerRoot resolves the factory root from wd and, when the session
// carries AF_ROOT, cross-checks the two; a mismatch is a hard error naming both
// roots (issue #519). AF_ROOT is read here — not in internal/config — per ADR-004
// (resolveWatchdogRoot precedent). This is the fourth deliberate resolver; do NOT
// unify it with the nearest-marker walk (config.FindFactoryRoot), the watchdog
// AF_ROOT-first resolver, or the containment AF_ROOT-shunning resolver — T-INT-4
// encodes the carve-outs.
func resolveInvokerRoot(wd string) (string, error) {
	resolved, err := config.FindFactoryRoot(wd)
	if err != nil {
		return "", err // propagate the verbatim not-found string (root.go:36)
	}
	// K5 (#519 Phase 3): scan for an enclosing factory UNCONDITIONALLY (H3), on the
	// resolved root, BEFORE every success return below. Best-effort — a scan error
	// leaves enclosing empty and changes nothing (observability, not a gate).
	enclosing, _ := config.FindEnclosingRoot(resolved)
	afRoot := os.Getenv("AF_ROOT")
	if afRoot == "" {
		if enclosing != "" {
			// Env-less nested shell: refuse the state-writing verb (CHECK-AS-WARNING
			// verbs downgrade via downgradeRootMismatch). The AF_ROOT hatch affirms intent.
			return "", &enclosingRootError{resolved: resolved, enclosing: enclosing}
		}
		return resolved, nil // operator shells / CI / install --init: identical to pre-seam behavior
	}
	// FULL resolve AF_ROOT, not a shallow stat: it may itself carry a .factory-root
	// redirect (afweb rationale, web/internal/config/root.go:65-68).
	envRoot, envErr := config.FindFactoryRoot(afRoot)
	if envErr != nil {
		fmt.Fprintf(os.Stderr, "warning: AF_ROOT=%q does not resolve to a factory; using cwd-resolved root %s\n", afRoot, resolved)
		warnEnclosingRoot(resolved, enclosing)
		return resolved, nil // warn-and-proceed (watchdog fall-through posture)
	}
	if config.SameResolvedRoot(resolved, envRoot) {
		warnEnclosingRoot(resolved, enclosing) // gen-0 in-session signal (H3)
		return resolved, nil
	}
	return "", &rootMismatchError{resolved: resolved, envRoot: envRoot}
}

// downgradeRootMismatch inspects an error from resolveInvokerRoot. If it is a
// factory-root mismatch, it warns on stderr naming both roots and returns the
// cwd-resolved root with ok=true, so a read-only/status verb can proceed on that
// root and keep its output contract (agents list stays a valid array; dispatch
// status stays exit-0). Any other error (e.g. not-found) yields ok=false, and the
// caller propagates err exactly as before.
func downgradeRootMismatch(err error) (root string, ok bool) {
	var mm *rootMismatchError
	if errors.As(err, &mm) {
		fmt.Fprintf(os.Stderr,
			"warning: factory root mismatch: cwd resolves to %s but this session's AF_ROOT is %s; "+
				"proceeding on the cwd-resolved root for this read-only command\n",
			mm.resolved, mm.envRoot)
		return mm.resolved, true
	}
	// K5 (#519 Phase 3): the enclosing-refusal is the same verb-class seam — the
	// CHECK-AS-WARNING verbs downgrade it identically, proceeding on the nested root.
	var enc *enclosingRootError
	if errors.As(err, &enc) {
		fmt.Fprintf(os.Stderr,
			"warning: factory %s is nested inside enclosing factory %s; "+
				"proceeding on the nested root for this read-only command\n",
			enc.resolved, enc.enclosing)
		return enc.resolved, true
	}
	return "", false
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
