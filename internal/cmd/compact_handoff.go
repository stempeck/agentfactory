package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/tmux"
)

var compactHandoffInteractive bool

var compactHandoffCmd = &cobra.Command{
	Use:   "compact-handoff",
	Short: "Intercept PreCompact hook to prevent thinking-block corruption",
	Long: `Compact-handoff intercepts Claude Code's PreCompact hook to prevent context
compaction from corrupting extended-thinking block signatures. It checkpoints
the current session state with compaction markers, sends a handoff mail, and
recycles the session via tmux respawn-pane.

When run outside tmux or on any error, exits 0 (ADR-007: hooks never block).
With --interactive, logs a warning instead of recycling the session.`,
	RunE: runCompactHandoff,
}

func init() {
	compactHandoffCmd.Flags().BoolVar(&compactHandoffInteractive, "interactive", false,
		"Log warning and allow compaction instead of recycling (for interactive agents)")
	rootCmd.AddCommand(compactHandoffCmd)
}

func runCompactHandoff(cmd *cobra.Command, args []string) error {
	cwd, err := getWd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "compact-handoff: cannot get working directory, allowing compaction")
		return nil
	}
	return runCompactHandoffCore(cmd.Context(), cwd, compactHandoffInteractive)
}

const (
	compactHandoffThreshold = 3
	compactHandoffWindow    = 10 * time.Minute
	compactHandoffCountFile = "compact_handoff_count"
)

type compactHandoffCounter struct {
	Count   int       `json:"count"`
	FirstAt time.Time `json:"first_at"`
	LastAt  time.Time `json:"last_at"`
}

func runCompactHandoffCore(ctx context.Context, cwd string, interactive bool) error {
	// Step 1 — Tmux validation with graceful fallback
	if !tmux.IsInsideTmux(os.Getenv("TMUX")) {
		fmt.Fprintln(os.Stderr, "compact-handoff: not inside tmux, allowing compaction")
		return nil
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		fmt.Fprintln(os.Stderr, "compact-handoff: TMUX_PANE not set, allowing compaction")
		return nil
	}

	// Interactive agents should not be silently recycled
	if interactive {
		fmt.Fprintln(os.Stderr, "compact-handoff: interactive mode, allowing compaction (session not recycled)")
		return nil
	}

	// Step 2 — Context discovery
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compact-handoff: finding factory root: %v, allowing compaction\n", err)
		return nil
	}

	agentName, agentEntry, err := detectRole(cwd, factoryRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compact-handoff: detecting agent: %v, allowing compaction\n", err)
		return nil
	}

	// Step 3 — Checkpoint with compaction marker
	if cpErr := captureCheckpointWithFormula(ctx, cwd,
		"COMPACTION: Session recycled at context pressure boundary",
		func(cp *checkpoint.Checkpoint) {
			cp.CompactionHandoff = true
			cp.CompactionAt = time.Now()
		}); cpErr != nil {
		fmt.Fprintf(os.Stderr, "compact-handoff: checkpoint: %v, continuing\n", cpErr)
	}

	// Step 4 — Rate limiter
	checkCompactHandoffRate(cwd, agentName)

	// Step 5 — Send compaction-specific handoff mail
	if err := sendHandoffMail(agentName,
		"COMPACTION: Session recycled at context pressure boundary",
		"Context pressure triggered compaction boundary. Session recycled to prevent "+
			"thinking-block signature corruption. Run af prime for current step."); err != nil {
		fmt.Fprintf(os.Stderr, "compact-handoff: mail send: %v, continuing\n", err)
	}

	// Step 6 — Recycle via shared respawn
	return respawnSession(RespawnOptions{
		FactoryRoot: factoryRoot,
		AgentName:   agentName,
		AgentEntry:  *agentEntry,
		PaneID:      pane,
	})
}

func checkCompactHandoffRate(cwd, agentName string) {
	runtimeDir := filepath.Join(cwd, ".runtime")
	counterPath := filepath.Join(runtimeDir, compactHandoffCountFile)

	var counter compactHandoffCounter
	if data, err := os.ReadFile(counterPath); err == nil {
		_ = json.Unmarshal(data, &counter)
	}

	now := time.Now()

	// Reset if time window has expired
	if !counter.FirstAt.IsZero() && now.Sub(counter.FirstAt) > compactHandoffWindow {
		counter = compactHandoffCounter{}
	}

	counter.Count++
	counter.LastAt = now
	if counter.FirstAt.IsZero() {
		counter.FirstAt = now
	}

	// Escalate to supervisor if threshold exceeded
	if counter.Count > compactHandoffThreshold {
		_ = sendHandoffMail(escalationTarget,
			"COMPACTION RATE ALERT: Rapid compaction cycling detected",
			fmt.Sprintf("Agent %s has triggered %d compaction handoffs within %v. "+
				"This may indicate a context pressure loop. Please investigate.",
				agentName, counter.Count, compactHandoffWindow))
	}

	// Write updated counter
	_ = os.MkdirAll(runtimeDir, 0o755)
	if data, err := json.MarshalIndent(counter, "", "  "); err == nil {
		data = append(data, '\n')
		_ = os.WriteFile(counterPath, data, 0o644)
	}
}
