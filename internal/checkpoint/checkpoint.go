// Package checkpoint provides session context breadcrumbs for agent continuity.
// When an agent session ends (context limit, crash, timeout), the checkpoint
// captures git state and formula context so the next session can see what the
// previous session was working on. Checkpoints are informational — they do not
// drive automated recovery decisions. Recovery is handled by .runtime/hooked_formula
// and bdReadySteps() in the done/prime commands.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// Filename is the checkpoint file name within the agent directory.
const Filename = ".agent-checkpoint.json"

// Checkpoint represents a session recovery checkpoint.
type Checkpoint struct {
	// FormulaID is the current formula being worked.
	FormulaID string `json:"formula_id,omitempty"`

	// CurrentStep is the step ID currently in progress.
	CurrentStep string `json:"current_step,omitempty"`

	// StepTitle is the human-readable title of the current step.
	StepTitle string `json:"step_title,omitempty"`

	// ModifiedFiles lists files modified since the last commit.
	ModifiedFiles []string `json:"modified_files,omitempty"`

	// LastCommit is the SHA of the last commit.
	LastCommit string `json:"last_commit,omitempty"`

	// Branch is the current git branch.
	Branch string `json:"branch,omitempty"`

	// HookedBead is the bead ID on the agent's hook.
	HookedBead string `json:"hooked_bead,omitempty"`

	// Timestamp is when the checkpoint was written.
	Timestamp time.Time `json:"timestamp"`

	// SessionID identifies the session that wrote the checkpoint.
	SessionID string `json:"session_id,omitempty"`

	// Notes contains optional context from the session.
	Notes string `json:"notes,omitempty"`

	// CompactionHandoff marks this checkpoint as written during a compaction-boundary recycle.
	CompactionHandoff bool `json:"compaction_handoff,omitempty"`

	// CompactionAt is when the compaction event occurred.
	CompactionAt time.Time `json:"compaction_at,omitempty"`

	// The handoff interview (#668 K8). A recycling session is the last thing that still knows what
	// it was doing, and today it says so only in Notes — one free-text line the inheriting session
	// has to re-derive everything from. These three fields are that same knowledge in a shape a
	// reader can branch on: which files the work is in, what has already been established, and the
	// one thing to do next. Structure rather than prose is the whole point — prime's resume slimming
	// has to decide whether a brief supersedes a section, and it cannot decide that about a
	// sentence.
	//
	// Every field is omitempty, and that is load-bearing twice over. An agent already in flight has
	// a checkpoint on disk written by a binary that never heard of a brief, and it must keep
	// decoding (Read is tolerant by design, :70-87). And a checkpoint written WITHOUT a brief must
	// not be readable as one carrying an empty brief, because slimming arms on the brief's presence:
	// absent and empty leading to the same behaviour is how a session loses context it needed.
	ResumeArtifacts  []string `json:"resume_artifacts,omitempty"`
	ResumeVerified   string   `json:"resume_verified,omitempty"`
	ResumeNextAction string   `json:"resume_next_action,omitempty"`

	// ResumeNextStepID is which step the brief was written FOR (#678 K8a). Without it the brief's own
	// successor is the one reader that cannot tell the brief is about it: a boundary handoff recycles
	// BETWEEN steps, so prime sees the inheriting session as starting a new step rather than resuming
	// one, and the same-step slim rule cannot arm. It re-receives every section the brief already
	// carries.
	//
	// On today's single write path this holds the same value as CurrentStep — the interview and
	// WithFormula are handed the same ready step by the same caller. That is a fact about the caller,
	// not a property of the field, and the two are read under DIFFERENT rules: CurrentStep answers
	// "which step is this checkpoint about", which every non-brief reader asks, while this answers
	// "which step was this BRIEF written for", which only the successor rule asks and which must go
	// stale with the brief rather than with the formula state. Collapsing them would make the slimming
	// rule read a field that a later WithFormula is free to move underneath it.
	//
	// Written only by the interview, i.e. only where a next action is written too, so it is a brief
	// field rather than a fourth piece of formula state — and omitempty for the reason the three above
	// are: a checkpoint that names no successor must not read as one naming an empty successor.
	ResumeNextStepID string `json:"resume_next_step_id,omitempty"`
}

// HasResumeBrief reports whether an interview was actually conducted. The next action is the
// discriminator rather than any of the three, because it is the only one whose absence makes the
// brief useless: a list of files with nothing to do about them supersedes no section of prime's
// output, and artifacts alone are already in ModifiedFiles.
func (cp *Checkpoint) HasResumeBrief() bool {
	return cp != nil && cp.ResumeNextAction != ""
}

// Path returns the checkpoint file path for a given agent directory.
func Path(agentDir string) string {
	return filepath.Join(agentDir, Filename)
}

// Read loads a checkpoint from the agent directory.
// Returns nil, nil if no checkpoint exists.
func Read(agentDir string) (*Checkpoint, error) {
	path := Path(agentDir)

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is constructed from trusted agentDir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading checkpoint: %w", err)
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("parsing checkpoint: %w", err)
	}

	return &cp, nil
}

// Write saves a checkpoint to the agent directory.
func Write(agentDir string, cp *Checkpoint) error {
	// Set timestamp if not already set
	if cp.Timestamp.IsZero() {
		cp.Timestamp = time.Now()
	}

	if cp.SessionID == "" {
		cp.SessionID = fmt.Sprintf("pid-%d", os.Getpid())
	}

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling checkpoint: %w", err)
	}

	path := Path(agentDir)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing checkpoint: %w", err)
	}

	return nil
}

// Remove deletes the checkpoint file.
func Remove(agentDir string) error {
	path := Path(agentDir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing checkpoint: %w", err)
	}
	return nil
}

// Capture creates a checkpoint by capturing current git and work state.
func Capture(agentDir string) (*Checkpoint, error) {
	cp := &Checkpoint{
		Timestamp: time.Now(),
	}

	// Resolve git root: use FindLocalRoot for explicit worktree-aware root,
	// fall back to agentDir (git traverses up to find .git from there).
	gitDir := agentDir
	if lr, err := config.FindLocalRoot(agentDir); err == nil {
		gitDir = lr
	}

	// Get modified files from git status
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = gitDir
	output, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		for _, line := range lines {
			if len(line) > 3 {
				// Format: XY filename
				file := strings.TrimSpace(line[3:])
				if file != "" {
					cp.ModifiedFiles = append(cp.ModifiedFiles, file)
				}
			}
		}
	}

	// Get last commit SHA
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = gitDir
	output, err = cmd.Output()
	if err == nil {
		cp.LastCommit = strings.TrimSpace(string(output))
	}

	// Get current branch
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = gitDir
	output, err = cmd.Output()
	if err == nil {
		cp.Branch = strings.TrimSpace(string(output))
	}

	return cp, nil
}

// WithFormula adds formula context to a checkpoint.
func (cp *Checkpoint) WithFormula(formulaID, stepID, stepTitle string) *Checkpoint {
	cp.FormulaID = formulaID
	cp.CurrentStep = stepID
	cp.StepTitle = stepTitle
	return cp
}

// WithHookedBead adds hooked bead context to a checkpoint.
func (cp *Checkpoint) WithHookedBead(beadID string) *Checkpoint {
	cp.HookedBead = beadID
	return cp
}

// WithNotes adds context notes to a checkpoint.
func (cp *Checkpoint) WithNotes(notes string) *Checkpoint {
	cp.Notes = notes
	return cp
}

// WithResumeBrief adds the handoff interview to a checkpoint (#668 K8).
//
// It is a separate builder from WithNotes rather than an extension of it because the two have
// different owners and different lifetimes: Notes is the recycle's subject line, written by whoever
// triggered the handoff, and captureCheckpointWithFormula overwrites it on every capture. A brief
// written into Notes would be destroyed by that same call.
// nextAction stays the final parameter even though nextStepID was added after it: HasResumeBrief
// keys on the next action, so it is the field whose write arms every reader of the brief, and a
// caller building the argument list left to right should reach it last.
func (cp *Checkpoint) WithResumeBrief(artifacts []string, verified, nextStepID, nextAction string) *Checkpoint {
	cp.ResumeArtifacts = artifacts
	cp.ResumeVerified = verified
	cp.ResumeNextStepID = nextStepID
	cp.ResumeNextAction = nextAction
	return cp
}

// Age returns how long ago the checkpoint was written.
func (cp *Checkpoint) Age() time.Duration {
	return time.Since(cp.Timestamp)
}

// IsStale returns true if the checkpoint is older than the threshold.
func (cp *Checkpoint) IsStale(threshold time.Duration) bool {
	return cp.Age() > threshold
}

// Summary returns a concise summary of the checkpoint.
func (cp *Checkpoint) Summary() string {
	var parts []string

	if cp.FormulaID != "" {
		if cp.CurrentStep != "" {
			parts = append(parts, fmt.Sprintf("formula %s, step %s", cp.FormulaID, cp.CurrentStep))
		} else {
			parts = append(parts, fmt.Sprintf("formula %s", cp.FormulaID))
		}
	}

	if cp.HookedBead != "" {
		parts = append(parts, fmt.Sprintf("hooked: %s", cp.HookedBead))
	}

	if len(cp.ModifiedFiles) > 0 {
		parts = append(parts, fmt.Sprintf("%d modified files", len(cp.ModifiedFiles)))
	}

	if cp.Branch != "" {
		parts = append(parts, fmt.Sprintf("branch: %s", cp.Branch))
	}

	// The brief's next action goes last because it is the only clause a reader acts on, and the
	// four above it are what that action is about. Only the next action is summarised: artifacts
	// are already counted by the modified-files clause, and the verified statement is a paragraph,
	// not a fact a one-line summary can carry without becoming the brief itself.
	if cp.ResumeNextAction != "" {
		parts = append(parts, fmt.Sprintf("next: %s", cp.ResumeNextAction))
	}

	if len(parts) == 0 {
		return "no significant state"
	}

	return strings.Join(parts, ", ")
}
