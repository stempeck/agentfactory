package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/tmux"
)

var (
	handoffSubject string
	handoffMessage string
	handoffCollect bool
	handoffIdle    bool
	handoffDryRun  bool
)

var handoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Recycle the current session, preserving context for the next",
	Long: `Handoff writes a checkpoint, sends a handoff mail to self, clears tmux
scrollback, and respawns the pane with a fresh Claude session. The new session
auto-primes with the checkpoint context and handoff mail.

Must be run from inside a tmux agent session.`,
	RunE: runHandoff,
}

func init() {
	handoffCmd.Flags().StringVarP(&handoffSubject, "subject", "s", "HANDOFF: Session cycling", "Mail subject")
	handoffCmd.Flags().StringVarP(&handoffMessage, "message", "m", "Context cycling. Run af prime for current step.", "Mail body")
	handoffCmd.Flags().BoolVarP(&handoffCollect, "collect", "c", false, "Collect and append current state to message")
	handoffCmd.Flags().BoolVar(&handoffIdle, "idle", false, "Signal that this cycle found no work (increments back-off counter)")
	handoffCmd.Flags().BoolVarP(&handoffDryRun, "dry-run", "n", false, "Show what would happen without executing")
	rootCmd.AddCommand(handoffCmd)
}

func runHandoff(cmd *cobra.Command, args []string) error {
	cwd, err := getWd()
	if err != nil {
		return err
	}
	return runHandoffCore(cmd.Context(), cwd, handoffSubject, handoffMessage, handoffCollect, handoffIdle, handoffDryRun)
}

// runHandoffCore contains the core logic for af handoff, separated from cobra for testability.
func runHandoffCore(ctx context.Context, cwd, subject, message string, collect, idle, dryRun bool) error {
	// 1. Validate tmux environment
	if !tmux.IsInsideTmux(os.Getenv("TMUX")) {
		return fmt.Errorf("af handoff must be run inside a tmux session")
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return fmt.Errorf("TMUX_PANE not set — cannot identify current pane")
	}

	// 2. Find factory root
	factoryRoot, err := resolveInvokerRoot(cwd)
	if err != nil {
		return fmt.Errorf("finding factory root: %w", err)
	}

	// 3. Detect agent
	agentName, agentEntry, err := detectRole(cwd, factoryRoot)
	if err != nil {
		return fmt.Errorf("detecting agent: %w", err)
	}

	// 4. Collect state if requested
	if collect {
		collected := collectHandoffState(ctx, cwd, factoryRoot)
		if collected != "" {
			message = message + "\n\n" + collected
		}
	}

	// 5. Write checkpoint
	if dryRun {
		fmt.Printf("[dry-run] Would write checkpoint with notes: %s\n", subject)
	} else {
		if err := captureCheckpointWithFormula(ctx, cwd, subject, nil); err != nil {
			fmt.Fprintf(os.Stderr, "warning: checkpoint write failed: %v\n", err)
		} else {
			fmt.Println("Checkpoint written")
		}
	}

	// 6. Send mail to self
	if dryRun {
		fmt.Printf("[dry-run] Would send handoff mail to %s\n", agentName)
	} else {
		if err := sendHandoffMail(agentName, subject, message); err != nil {
			fmt.Fprintf(os.Stderr, "warning: mail send failed: %v\n", err)
		} else {
			fmt.Println("Handoff mail sent to self")
		}
	}

	// 7. Handle idle back-off counter
	sleepPrefix := ""
	if idle {
		cycles := readIdleCycles(cwd)
		cycles++
		writeIdleCycles(cwd, cycles)
		delay := idleBackoffSeconds(cycles)
		sleepPrefix = fmt.Sprintf("sleep %d && ", delay)
	} else {
		removeIdleCycles(cwd)
	}

	// 8. Build respawn command and recycle
	if dryRun {
		fmt.Printf("[dry-run] Would clear tmux history for pane %s\n", pane)
		fmt.Printf("[dry-run] Would respawn pane with startup command\n")
		return nil
	}

	fmt.Printf("Handing off %s...\n", agentName)
	return respawnSession(RespawnOptions{
		FactoryRoot:  factoryRoot,
		AgentName:    agentName,
		AgentEntry:   *agentEntry,
		PaneID:       pane,
		CmdPrefix:    sleepPrefix,
		AgentWorkDir: cwd,
		Trigger:      triggerSelfHandoff,
	})
}

// sendHandoffMail shells out to `af mail send` to deliver the handoff message.
// Declared as a var so tests can observe recipient/subject/body with a recording
// fake (seam pattern, mirrors sendWorkDoneMail): the isTestBinary() no-op below
// keeps unit tests hermetic, which also means a test could otherwise only prove
// "it did not error", never WHAT was sent.
var sendHandoffMail = func(agentName, subject, body string) error {
	if isTestBinary() {
		return nil
	}

	afPath, err := os.Executable()
	if err != nil {
		afPath, _ = exec.LookPath("af")
	}
	if afPath == "" {
		return fmt.Errorf("cannot find af binary")
	}

	cmd := exec.Command(afPath, "mail", "send", agentName, "-s", subject, "-m", body)
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("mail send to %s failed: %w\nsubprocess stderr: %s", agentName, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("mail send to %s: %w", agentName, err)
	}
	return nil
}

// idleBackoffSeconds computes the back-off delay for a given number of idle cycles.
// Linear: 60s per cycle, capped at 1800s (30 minutes).
func idleBackoffSeconds(cycles int) int {
	delay := cycles * 60
	if delay > 1800 {
		return 1800
	}
	return delay
}

// readIdleCycles reads the idle counter from .runtime/idle_cycles, returning 0 if absent.
func readIdleCycles(cwd string) int {
	data, err := os.ReadFile(filepath.Join(cwd, ".runtime", "idle_cycles"))
	if err != nil {
		return 0
	}
	n := 0
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &n)
	return n
}

// writeIdleCycles writes the idle counter to .runtime/idle_cycles.
func writeIdleCycles(cwd string, cycles int) {
	runtimeDir := filepath.Join(cwd, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "idle_cycles"), []byte(fmt.Sprintf("%d", cycles)), 0o644)
}

// removeIdleCycles deletes the idle counter file, resetting back-off state.
func removeIdleCycles(cwd string) {
	os.Remove(filepath.Join(cwd, ".runtime", "idle_cycles"))
}

// resumeArtifactCap bounds the artifact leg of the brief. The brief is rendered into the resumed
// session's prime output, and a working tree with two hundred dirty paths would put two hundred
// lines in front of every session that inherits it — the exact cost K16 exists to cut. The full
// list is not lost: it stays in ModifiedFiles on the same checkpoint, which is where a reader who
// wants all of it should look.
const resumeArtifactCap = 12

// conductResumeInterview asks the recycling session the three questions scale.md:105-109 names —
// which artifacts the work is in, what is already established, and the one thing to do next — and
// records the answers in fields rather than prose.
//
// collectHandoffState (below) has assembled the same knowledge since long before this, and it is
// left alone: its product is the mail body a human reads, and free text is the right shape for
// that. This is the same interview conducted for a machine. prime's slimming has to decide whether
// the brief supersedes a section of its own output, and it cannot decide that about a sentence.
//
// Every answer is derived from what the store and the checkpoint already hold. Nothing here asks
// the session what it thinks it accomplished: a recycling session is recycling because its window
// is full, and the least reliable thing in the room is its own account of itself.
//
// The next action is written LAST and only when a ready step exists, because HasResumeBrief keys
// on it. A brief that is half-written is worse than none — slimming would arm on it and drop
// sections whose content the brief never carried.
func conductResumeInterview(ctx context.Context, store issuestore.Store, cp *checkpoint.Checkpoint,
	formulaID string, ready issuestore.ReadyResult) {

	if cp == nil || len(ready.Steps) == 0 {
		return
	}

	var artifacts []string
	if n := len(cp.ModifiedFiles); n > 0 {
		if n > resumeArtifactCap {
			n = resumeArtifactCap
		}
		artifacts = append(artifacts, cp.ModifiedFiles[:n]...)
	}

	verified := ""
	closed, err := store.List(ctx, issuestore.Filter{Parent: formulaID, Statuses: []issuestore.Status{issuestore.StatusClosed}})
	if err == nil {
		total := ready.TotalSteps
		if total < len(closed) {
			total = len(closed)
		}
		verified = fmt.Sprintf("%d of %d formula steps closed", len(closed), total)
	}
	if cp.LastCommit != "" {
		commit := cp.LastCommit
		if len(commit) > 8 {
			commit = commit[:8]
		}
		if verified != "" {
			verified += "; "
		}
		verified += "last commit " + commit
	}

	step := ready.Steps[0]
	cp.WithResumeBrief(artifacts, verified, step.ID, fmt.Sprintf("continue step %s: %s", step.ID, step.Title))
}

// collectHandoffState gathers formula progress, inbox count, and modified files.
func collectHandoffState(ctx context.Context, cwd, factoryRoot string) string {
	var parts []string

	// Formula progress
	formulaID := readHookedFormulaID(cwd)
	if formulaID != "" {
		actor := os.Getenv("AF_ACTOR")
		if store, err := newIssueStore(cwd, actor); err == nil {
			result, _ := store.Ready(ctx, issuestore.Filter{MoleculeID: formulaID})
			if len(result.Steps) > 0 {
				parts = append(parts, fmt.Sprintf("Formula: %s, next step: %s (%s)", formulaID, result.Steps[0].ID, result.Steps[0].Title))
			} else {
				parts = append(parts, fmt.Sprintf("Formula: %s (no ready steps)", formulaID))
			}
		} else {
			parts = append(parts, fmt.Sprintf("Formula: %s (store init failed)", formulaID))
		}
	}

	// Modified files from checkpoint capture
	cp, err := checkpoint.Capture(cwd)
	if err == nil && len(cp.ModifiedFiles) > 0 {
		parts = append(parts, fmt.Sprintf("Modified files: %d", len(cp.ModifiedFiles)))
		for _, f := range cp.ModifiedFiles {
			parts = append(parts, fmt.Sprintf("  %s", f))
		}
	}

	if len(parts) == 0 {
		return ""
	}

	result := "--- Collected State ---\n"
	for _, p := range parts {
		result += p + "\n"
	}
	return result
}
