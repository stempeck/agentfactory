package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #678 K1 — the pane guard's precondition, pinned at review time.
//
// hookRunsInAgentPane (prime.go) declines to claim the session identity when TMUX_PANE is absent,
// and the ONLY reason that separates a grader from the agent is that the graders launch `claude`
// under `env -i`, which forwards nothing the harness did not name. That is a claim about a shell
// script an operator can edit. Adding TMUX_PANE to the forwarded allowlist — or dropping `env -i`
// for a plain invocation — silently restores the original defect: the grader's session takes the
// agent's identity, every generation figure for the graded step goes nil, and SessionsPerStep
// inflates on exactly the steps that were graded hardest. Nothing in the suite would notice,
// because the guard itself still works; its assumption is what stopped being true.
//
// So the assumption is asserted here, in the same shape as the #309 and K13 scanners: read the
// production scripts, not a fixture, and prove the predicate is non-vacuous against a planted
// counter-example.
func TestGradersLaunchClaudeWithoutTheAgentsPane(t *testing.T) {
	root := repoRootFromCmdPackage(t)
	scripts := []string{
		"hooks/quality-gate.sh",
		"hooks/fidelity-gate.sh",
		"internal/cmd/install_hooks/quality-gate.sh",
		"internal/cmd/install_hooks/fidelity-gate.sh",
	}

	for _, rel := range scripts {
		t.Run(rel, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("reading %s: %v", rel, err)
			}
			block := graderLaunchBlock(string(body))
			if block == "" {
				t.Fatalf("%s: no `claude -p` launch found. Either the grader stopped launching a "+
					"session — in which case delete this subtest — or the launch moved and this "+
					"scanner is now asserting nothing", rel)
			}
			if reason := graderLaunchIsScrubbed(block); reason != "" {
				t.Errorf("%s: %s\n\nThe launch block was:\n%s\n\n"+
					"af prime's pane guard (#678 K1) tells the agent's session apart from this "+
					"grader's by TMUX_PANE presence alone. A grader that inherits the agent's pane "+
					"takes the agent's session identity, which nils the step's generation figures "+
					"and counts the grading run as a session the step crossed.", rel, reason, block)
			}
		})
	}

	// Non-vacuity: the predicate has to reject the edit it exists to catch, or the subtests above
	// pass because they assert nothing.
	t.Run("the scanner rejects a grader that forwards the pane", func(t *testing.T) {
		planted := "VERDICT=$(env -i HOME=\"$HOME\" PATH=\"$PATH\" \\\n" +
			"    TMUX_PANE=\"$TMUX_PANE\" \\\n" +
			"    claude -p --model haiku --max-turns 1 \\\n" +
			"    \"$EVAL_INPUT\")\n"
		block := graderLaunchBlock(planted)
		if block == "" {
			t.Fatal("the block extractor found no launch in the planted script")
		}
		if graderLaunchIsScrubbed(block) == "" {
			t.Error("a grader forwarding TMUX_PANE was accepted; this scanner would not have caught " +
				"the edit that re-opens the defect")
		}
	})

	t.Run("the scanner rejects a grader launched without env -i", func(t *testing.T) {
		planted := "VERDICT=$(claude -p --model haiku --max-turns 1 \"$EVAL_INPUT\")\n"
		block := graderLaunchBlock(planted)
		if block == "" {
			t.Fatal("the block extractor found no launch in the planted script")
		}
		if graderLaunchIsScrubbed(block) == "" {
			t.Error("a grader launched with the harness's whole environment was accepted")
		}
	})
}

// graderLaunchBlock returns the shell command that launches `claude -p`, joined from its backslash
// continuations. Continuations are followed BACKWARDS from the launch line so the `env -i` prefix —
// which is what this scanner is really about — is inside the block rather than above it.
func graderLaunchBlock(script string) string {
	lines := strings.Split(script, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "claude -p") || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		start := i
		for start > 0 && strings.HasSuffix(strings.TrimSpace(lines[start-1]), "\\") {
			start--
		}
		end := i
		for end < len(lines)-1 && strings.HasSuffix(strings.TrimSpace(lines[end]), "\\") {
			end++
		}
		return strings.Join(lines[start:end+1], "\n")
	}
	return ""
}

// graderLaunchIsScrubbed returns "" when the launch cannot carry the agent's pane, or the reason it
// can. TMUX is checked beside TMUX_PANE because the pair travels together and a guard that later
// reads either one should not be undermined by a forward of the other.
func graderLaunchIsScrubbed(block string) string {
	if !strings.Contains(block, "env -i") {
		return "the grader launches `claude` without `env -i`, so it inherits the agent's whole environment"
	}
	if strings.Contains(block, "TMUX") {
		return "the grader's `env -i` allowlist forwards TMUX/TMUX_PANE"
	}
	return ""
}

func repoRootFromCmdPackage(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Dir(filepath.Dir(wd))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("expected the module root two levels above %s, found no go.mod: %v", wd, err)
	}
	return root
}
