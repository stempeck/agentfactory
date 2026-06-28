package feedback

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/readmodel"
)

// fakeAgents is the hermetic gate-state source: it returns canned AgentViews, never execs
// `af step current` / tmux / git (ADR-018 / FR-3). Mirrors readmodel.fakeLister / dispatch.fakeStatus.
type fakeAgents struct {
	views []readmodel.AgentView
	err   error
}

func (f fakeAgents) Assemble(ctx context.Context) ([]readmodel.AgentView, error) {
	return f.views, f.err
}

var _ AgentSource = fakeAgents{} // compile-time proof (the package idiom)

// owningView is an agent verified parked at the matching gate for prototype web-ui / iteration N.
func owningView(root string, stepState string, isGate bool, gateID string) readmodel.AgentView {
	return readmodel.AgentView{
		Name:      "web-design",
		Formula:   "web-design",
		StepState: stepState,
		IsGate:    isGate,
		GateID:    gateID,
		Inputs:    map[string]string{"output_dir": filepath.Join(root, ".designs", "web-ui")},
	}
}

// the gate-release regexes re-encoded in Go EXACTLY as the formula's bash interlock
// (web-design.formula.toml:578-588) — independently of production's copy — so the test proves the
// written contract, not just "some bytes". (Named t* to avoid colliding with writer.go's copies.)
var (
	tHasWord  = regexp.MustCompile(`(?im)^[[:space:]]*[-*]?[[:space:]]*Decision:.*(APPROVED|REJECTED)`)
	tNoteLine = regexp.MustCompile(`(?i)(palette|typography|signature|decision|notes?):[[:space:]]*[^[:space:]]`)
	tPlaceh   = regexp.MustCompile(`:[[:space:]]*(_+|\[)`)
)

func satisfiesGateRelease(form string) (check, word, note bool) {
	check = strings.Contains(strings.ToLower(form), "[x]")
	word = tHasWord.MatchString(form)
	for _, ln := range strings.Split(form, "\n") {
		if tNoteLine.MatchString(ln) && !tPlaceh.MatchString(ln) {
			note = true
			break
		}
	}
	return
}

func TestFeedback_WritesFormContract(t *testing.T) {
	root := t.TempDir()
	protoDir := filepath.Join(root, ".designs", "web-ui", "prototype-v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	formPath := filepath.Join(protoDir, "feedback-form.md")

	src := fakeAgents{views: []readmodel.AgentView{
		owningView(root, "ready", true, "design-feedback-1"),
	}}
	w := New(root, src)

	res, err := w.Submit(context.Background(), "web-ui", Input{
		Decision: "APPROVED",
		Notes:    "Palette: the neon teal works; tighten the card gutters.",
	})
	if err != nil {
		t.Fatalf("Submit at gate: %v", err)
	}
	if !res.OK {
		t.Fatalf("at-gate submit should be ok, got %+v", res)
	}

	b, err := os.ReadFile(formPath)
	if err != nil {
		t.Fatalf("feedback-form.md not written: %v", err)
	}
	form := string(b)

	check, word, note := satisfiesGateRelease(form)
	if !(check || word || note) {
		t.Fatalf("written form does NOT satisfy gate-release regex (CHECK=%v WORD=%v NOTE=%v):\n%s", check, word, note, form)
	}
	// APPROVED must land ON the Decision line (the HAS_WORD subtlety).
	if !word {
		t.Errorf("expected HAS_WORD (Decision: APPROVED on the Decision line), got false:\n%s", form)
	}
	// recognizable contract headings preserved.
	for _, h := range []string{"# Design Feedback", "## Decision", "## Notes"} {
		if !strings.Contains(form, h) {
			t.Errorf("written form missing heading %q:\n%s", h, form)
		}
	}
}

func TestFeedback_RejectedWhenNotAtGate(t *testing.T) {
	const blank = "# Design Feedback - Iteration 1\n\n## Decision\n- Decision: ______\n\n## Notes\n[free-form feedback]\n"

	cases := []struct {
		name  string
		views []readmodel.AgentView
	}{
		{"no owning agent", nil},
		{"agent present but not at a gate", []readmodel.AgentView{owningView("", "running", false, "")}},
		{"agent at the wrong gate", []readmodel.AgentView{owningView("", "ready", true, "design-feedback-2")}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			protoDir := filepath.Join(root, ".designs", "web-ui", "prototype-v1")
			if err := os.MkdirAll(protoDir, 0o755); err != nil {
				t.Fatal(err)
			}
			formPath := filepath.Join(protoDir, "feedback-form.md")
			if err := os.WriteFile(formPath, []byte(blank), 0o644); err != nil {
				t.Fatal(err)
			}

			// fix up the fake's output_dir to point at THIS test's root.
			views := make([]readmodel.AgentView, len(c.views))
			for i, v := range c.views {
				v.Inputs = map[string]string{"output_dir": filepath.Join(root, ".designs", "web-ui")}
				views[i] = v
			}
			w := New(root, fakeAgents{views: views})

			res, err := w.Submit(context.Background(), "web-ui", Input{Decision: "APPROVED", Notes: "real notes"})
			if err != nil {
				t.Fatalf("off-gate submit should be an honest value, not an error: %v", err)
			}
			if res.OK {
				t.Fatalf("off-gate submit should be ok:false, got %+v", res)
			}
			if !strings.Contains(strings.ToLower(res.Message), "not currently open") {
				t.Errorf("off-gate message = %q, want it to contain 'not currently open'", res.Message)
			}

			after, err := os.ReadFile(formPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != blank {
				t.Fatalf("off-gate write MUTATED feedback-form.md:\n got: %q\nwant: %q", string(after), blank)
			}
		})
	}
}
