package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/worktree"
)

func resetAgentState(ctx context.Context, w io.Writer, factoryRoot, agentName, reason string) error {
	// closedCount is function-scoped because the reset report below names it: the operator's
	// question on a --reset is what it destroyed, and beads-closed is the honest answer to set
	// beside the vault it did not touch.
	var closedCount int
	store, err := newIssueStore(factoryRoot, agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: cannot initialize store for bead cleanup: %v\n", agentName, err)
	} else {
		closedCount = closeAgentBeads(ctx, store, agentName, reason)
		if closedCount > 0 {
			fmt.Fprintf(w, "  %s: closed %d formula beads\n", agentName, closedCount)
		}
	}

	// Both facts, named before the removal below: what --reset destroyed, and what it left
	// alone. Reset is the loudest teardown the operator has, so it is the one most likely to be
	// read as having taken the vault with it.
	if line := memory.PreservedLine(factoryRoot, agentName); line != "" {
		fmt.Fprintf(w, "  %s: %s\n", agentName, line)
		fmt.Fprintf(w, "  %s: memory vault NOT touched by --reset (beads closed: %d)\n", agentName, closedCount)
	}

	meta, err := worktree.FindByAgent(factoryRoot, agentName)
	if err == nil && meta != nil {
		updated, empty, rmErr := worktree.RemoveAgent(factoryRoot, meta.ID, agentName)
		if rmErr != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: worktree RemoveAgent: %v\n", agentName, rmErr)
		} else if empty {
			if fErr := worktree.ForceRemove(factoryRoot, updated); fErr != nil {
				fmt.Fprintf(os.Stderr, "%s: error: force-removing worktree %s: %v\n", agentName, meta.ID, fErr)
				// Gap 1 fix: ForceRemove may fail without cleaning the meta file
				// (worktree.go returns early when both git remove and os.RemoveAll fail).
				// Manually remove the meta file to prevent zombie self-adoption.
				metaFile := filepath.Join(worktree.WorktreesDir(factoryRoot), meta.ID+".meta.json")
				if rmMetaErr := os.Remove(metaFile); rmMetaErr != nil && !os.IsNotExist(rmMetaErr) {
					return fmt.Errorf("%s: failed to clean meta file after ForceRemove failure: %w", agentName, rmMetaErr)
				}
			} else {
				fmt.Fprintf(w, "  %s: force-removed worktree %s\n", agentName, meta.ID)
			}
		} else {
			fmt.Fprintf(w, "  %s: deregistered from worktree %s (%d co-tenants remain)\n",
				agentName, meta.ID, len(updated.Agents))
		}
	}

	agentDir := config.AgentDir(factoryRoot, agentName)
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.RemoveAll(runtimeDir)
	if cpErr := checkpoint.Remove(agentDir); cpErr != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: removing checkpoint: %v\n", agentName, cpErr)
	}

	// The #596 K5 breaker lives at the FACTORY root, not under the agent directory, so the
	// wipe above never reached it and a reset agent came back still latched (review L-1). It
	// is REMOVED rather than rewritten because saveRecoveryState refuses to overwrite an
	// undecodable latch — the one most in need of clearing (recovery.go's runRecoveryReset
	// carries the same reasoning). Warn-and-continue like every other step here.
	if rmErr := os.Remove(recoveryStatePath(factoryRoot, agentName)); rmErr != nil && !os.IsNotExist(rmErr) {
		fmt.Fprintf(os.Stderr, "%s: warning: clearing recovery breaker state: %v\n", agentName, rmErr)
	}

	return nil
}
