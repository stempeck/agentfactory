package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

var downAll bool
var downReset bool

var downCmd = &cobra.Command{
	Use:   "down [agent...]",
	Short: "Stop agent sessions",
	Long: `Stop agent tmux sessions. No args = stop all agents from agents.json.

AUTHORITY: Factory-wide teardown is an operator action. Inside an af-managed
agent session this command refuses and explains why — run it from a host shell.
An agent may still stop itself or a specialist it dispatched; those stay allowed.
An interactive manager may also stop an autonomous specialist it did not dispatch;
factory-wide teardown stays operator-only.`,
	RunE: runDown,
}

func init() {
	downCmd.Flags().BoolVar(&downAll, "all", false, "Also kill orphaned Claude agent processes")
	downCmd.Flags().BoolVar(&downReset, "reset", false,
		"Force-remove worktrees and close formula beads (destructive, cannot be undone)")
	rootCmd.AddCommand(downCmd)
}

func runDown(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}

	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	// K5 + K11 (#541): foreclose agent-initiated factory-wide teardown. In agent
	// context af down would SIGKILL this session, every sibling agent, and the
	// interactive manager, so it is refused UNLESS it stays within the self /
	// own-dispatched-specialist carve-out. The gate sits above every teardown side
	// effect (preResetScan, the roster loop, the watchdog/dispatcher kills) so a
	// refusal is zero-op. requireOperatorTeardown returns nil for an operator, so
	// that path falls through byte-for-byte.
	if callerAuthority() == AuthorityAgent {
		tiers, ok := downSelfScoped(args, agentsCfg.Agents)
		if !ok {
			surface := "af down"
			if downReset {
				surface = "af down --reset"
			}
			return requireOperatorTeardown(surface)
		}
		// Allow path (#548 P2 / C-5): the agent-context stop was granted. Record one forensic
		// breadcrumb per granted target — the counterpart to the K4 refusal artifact — reusing the
		// tier downSelfScoped already computed, so the label costs no second scopedStopAllowed
		// (dispatch_owner) read.
		for _, name := range args {
			writeTeardownGrantedArtifact(tiers[name], name)
		}
	}

	// Resolve agent list
	agents := args
	if len(agents) == 0 {
		for name := range agentsCfg.Agents {
			agents = append(agents, name)
		}
	}

	if downReset {
		preResetScan(cmd, root, agents)
	}

	ctx := context.Background()
	allOK := true
	for _, name := range agents {
		entry, ok := agentsCfg.Agents[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: unknown agent\n", name)
			allOK = false
			continue
		}

		mgr := session.NewManager(root, name, entry)
		if err := mgr.Stop(); err != nil {
			if errors.Is(err, session.ErrNotRunning) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: not running\n", session.SessionName(name))
				if downReset {
					resetAgent(ctx, cmd, root, name)
				} else {
					// Capture the worktree path BEFORE cleanupAgentWorktree may remove it, so the
					// scoped-stop provenance datum (#548 P3) can be cleared at BOTH the main-root
					// and worktree agent dirs — sling writes it to whichever dir the dispatch used.
					_, wtPath := resolveWorktreeMeta(root, name)
					cleanupAgentWorktree(cmd, root, name)
					os.Remove(filepath.Join(config.AgentDir(root, name), ".runtime", "dispatched"))
					os.Remove(filepath.Join(config.AgentDir(root, name), ".runtime", "dispatch_owner"))
					if wtPath != "" {
						os.Remove(filepath.Join(config.AgentDir(wtPath, name), ".runtime", "dispatch_owner"))
					}
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			allOK = false
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", session.SessionName(name))
		if downReset {
			resetAgent(ctx, cmd, root, name)
		} else {
			// Capture the worktree path BEFORE cleanupAgentWorktree may remove it (see the
			// not-running branch above) so dispatch_owner is cleared at BOTH roots (#548 P3).
			_, wtPath := resolveWorktreeMeta(root, name)
			cleanupAgentWorktree(cmd, root, name)
			os.Remove(filepath.Join(config.AgentDir(root, name), ".runtime", "dispatched"))
			os.Remove(filepath.Join(config.AgentDir(root, name), ".runtime", "dispatch_owner"))
			if wtPath != "" {
				os.Remove(filepath.Join(config.AgentDir(wtPath, name), ".runtime", "dispatch_owner"))
			}
		}
	}

	// Kill watchdog + dispatcher sessions when stopping all agents. Mirrors the
	// startup symmetry: `start_dispatch: true` in startup.json has `af up` bring
	// the dispatcher up, so a stop-all `af down` tears it back down (#303). The
	// inline HasSession guard + KillSession (rather than the `af dispatch stop`
	// handler, which errors when the dispatcher is absent) keeps the no-dispatcher
	// case silent.
	if len(args) == 0 {
		watchdogSession := session.WatchdogSessionName()
		tx := newCmdTmux()
		if running, _ := tx.HasSession(watchdogSession); running {
			_ = tx.KillSession(watchdogSession)
			fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", watchdogSession)
		}

		dispatchSession := session.DispatchSessionName()
		if running, _ := tx.HasSession(dispatchSession); running {
			_ = tx.KillSession(dispatchSession)
			fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", dispatchSession)
		}
	}

	if downAll {
		killOrphanedClaudeProcesses()
	}

	if downReset {
		fmt.Fprintf(cmd.OutOrStdout(), "Reset complete.\n")
	}

	if !allOK {
		return fmt.Errorf("some agents failed to stop")
	}
	return nil
}

// downSelfScoped reports whether an agent-context `af down` stays inside the tiered scoped-stop
// carve-out (#548): no --all, at least one explicit target, and every target is granted by
// scopedStopAllowed — the caller's own session (self), a formula-backed specialist the caller
// dispatched (dispatcher), or, for an interactive manager, an autonomous specialist it did not
// dispatch (manager). A granted tier covers --reset on its targets too: the identical stop +
// state-reclamation authority is already tier-granted via `af sling --agent <target> --reset`
// (sling.go), so refusing it here would only force that awkward spelling. Anything else — bare,
// --all, a no-target --reset, the interactive manager as a target, a supervisor, or a
// non-dispatched sibling from a non-manager caller — is a factory-wide teardown reserved for the
// operator. The --all/no-args disqualifiers are evaluated BEFORE the per-target tier logic, so
// the manager tier structurally cannot leak into a factory-wide shape.
//
// On success it also returns the per-target granting tier (name→tier), so the allow-path caller can
// record the teardown_granted breadcrumb without a second scopedStopAllowed (dispatch_owner) read
// (#548 N-1). The tiers map is nil when ok is false.
func downSelfScoped(args []string, agents map[string]config.AgentEntry) (tiers map[string]string, ok bool) {
	if downAll || len(args) == 0 {
		return nil, false
	}
	tiers = make(map[string]string, len(args))
	for _, name := range args {
		tier, granted := scopedStopAllowed(name, agents)
		if !granted {
			return nil, false
		}
		tiers[name] = tier
	}
	return tiers, true
}

// cleanupAgentWorktree removes a worktree owned by the named agent.
// Deregisters the agent first via RemoveAgent, then removes the worktree only
// if no co-tenant agents remain (R-INT-3 safety).
// Non-fatal: logs warnings on errors, never returns an error.
func cleanupAgentWorktree(cmd *cobra.Command, factoryRoot, agentName string) {
	meta, err := worktree.FindByOwner(factoryRoot, agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: worktree lookup: %v\n", agentName, err)
		return
	}
	if meta == nil {
		return
	}
	// Preserve an in-flight worktree (issue #392). This guard MUST run here, on the
	// original meta, before the agent is deregistered below: deregistration narrows
	// and persists meta.Agents, so for a single-agent worktree a check placed after
	// it would iterate an empty list, miss the in-flight formula, and the worktree
	// would be destroyed. Skipping both the deregistration and the worktree teardown
	// also leaves the agent registered in meta.Agents on disk, so the next reboot's
	// GC guard still sees the formula. `af down --reset` (resetAgent → ForceRemove)
	// stays unguarded and force-removes.
	if worktree.HasUnfinishedFormula(factoryRoot, meta) {
		fmt.Fprintf(cmd.OutOrStdout(),
			"%s: kept worktree %s — in-flight formula present; use 'af down %s --reset' to force-remove\n",
			agentName, meta.ID, agentName)
		return
	}
	updated, empty, err := worktree.RemoveAgent(factoryRoot, meta.ID, agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: worktree RemoveAgent: %v\n", agentName, err)
		return
	}
	if empty {
		if rmErr := worktree.Remove(factoryRoot, updated); rmErr != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: worktree cleanup: %v\n", agentName, rmErr)
			fmt.Fprintf(os.Stderr, "  hint: use 'af down %s --reset' to force-remove worktrees with uncommitted changes\n", agentName)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: cleaned up worktree %s\n", agentName, meta.ID)
		}
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: deregistered from worktree %s (%d agents remain)\n", agentName, meta.ID, len(updated.Agents))
	}
}

func preResetScan(cmd *cobra.Command, factoryRoot string, agents []string) {
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "WARNING: --reset will permanently destroy agent worktrees and close formula beads.\n")
	fmt.Fprintf(w, "  The following will be affected:\n")
	for _, name := range agents {
		wtLabel := "no worktree"
		meta, err := worktree.FindByAgent(factoryRoot, name)
		if err == nil && meta != nil {
			wtLabel = fmt.Sprintf("worktree %s", meta.ID)
		}

		beadLabel := "0 open beads"
		store, err := newIssueStore(factoryRoot, name)
		if err != nil {
			beadLabel = "beads: unavailable"
		} else {
			beads, lErr := store.List(context.Background(), issuestore.Filter{Assignee: name})
			if lErr != nil {
				beadLabel = "beads: unavailable"
			} else {
				beadLabel = fmt.Sprintf("%d open beads", len(beads))
			}
		}
		fmt.Fprintf(w, "    %-20s %s, %s\n", name+":", wtLabel, beadLabel)
	}
	fmt.Fprintf(w, "  This action cannot be undone.\n")
}

func resetAgent(ctx context.Context, cmd *cobra.Command, factoryRoot, agentName string) error {
	return resetAgentState(ctx, cmd.OutOrStdout(), factoryRoot, agentName, config.CloseReasonResetDown)
}

func closeAgentBeads(ctx context.Context, store issuestore.Store, agentName, reason string) int {
	beads, err := store.List(ctx, issuestore.Filter{Assignee: agentName})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: listing beads: %v\n", agentName, err)
		return 0
	}
	closed := 0
	for _, bead := range beads {
		if err := store.Close(ctx, bead.ID, reason); err != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: closing bead %s: %v\n", agentName, bead.ID, err)
			continue
		}
		closed++
	}
	return closed
}

// runPkill is the K10 (#541 Phase 4) ADR-009 package-var seam over the orphan-sweep
// pgrep/pkill execs, mirroring runAgentGenScript. It exists so the default suite can override
// it and assert zero real host-process execs (AC-7 recording point): unlike production tmux
// (unconstructable under the ADR-018 build guard), a raw exec.Command("pkill", ...) WOULD shell
// out against the operator's real Claude processes in a default-suite test, so the seam is the
// guard. It returns nil when pgrep finds no orphans.
var runPkill = func(pattern string) error {
	if err := exec.Command("pgrep", "-f", pattern).Run(); err != nil {
		return nil // no orphaned processes
	}
	return exec.Command("pkill", "-9", "-f", pattern).Run()
}

func killOrphanedClaudeProcesses() {
	// K10 backstop (#541): an agent context never sweeps host processes. K5 already refuses the
	// `af down --all` that reaches this call; this redundant interlock makes that gate's omission
	// non-fatal and is the recording point proving zero real pkill in the default suite.
	if callerAuthority() == AuthorityAgent {
		return
	}
	// All af-managed agents are launched with --dangerously-skip-permissions, so this pattern
	// matches all agentfactory Claude processes regardless of type. Kept as a `pattern :=`
	// binding so TestKillScopeDrift can assert it stays in lockstep with the session.go launch
	// command (six_sigma_gaps.md Gap 8).
	pattern := "claude.*--dangerously-skip-permissions"
	_ = runPkill(pattern)
}
