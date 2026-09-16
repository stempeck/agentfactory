package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/checkpoint"
)

// TestInterview covers #668 K8: the recycling session is interviewed at the moment it writes its
// checkpoint, and what it says lands in structured fields the inheriting session can branch on.
func TestInterview(t *testing.T) {
	t.Run("the convergence point conducts the interview", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		epic, step := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		dirtyTree(t, fx, 1)

		const subject = "HANDOFF: step context boundary"
		if err := captureCheckpointWithFormula(t.Context(), fx.workDir, subject, nil); err != nil {
			t.Fatalf("captureCheckpointWithFormula: %v", err)
		}

		cp, err := checkpoint.Read(fx.workDir)
		if err != nil || cp == nil {
			t.Fatalf("checkpoint.Read: %v (cp=%v)", err, cp)
		}
		if !cp.HasResumeBrief() {
			t.Fatal("a recycle with a ready step left no resume brief; the interview did not happen")
		}
		if !strings.Contains(cp.ResumeNextAction, step.ID) {
			t.Errorf("next action %q does not name the ready step %s", cp.ResumeNextAction, step.ID)
		}
		if cp.ResumeVerified == "" {
			t.Error("the brief says nothing about what is already established")
		}
		if len(cp.ResumeArtifacts) == 0 {
			t.Error("the brief names no artifacts, but the fixture's working tree is dirty")
		}
		// The interview must not cost the caller its notes: captureCheckpointWithFormula's whole
		// contract with three call sites is that the subject they pass survives to disk.
		if cp.Notes != subject {
			t.Errorf("notes = %q, want %q — the interview clobbered the caller's notes", cp.Notes, subject)
		}
	})

	t.Run("no ready step, no brief", func(t *testing.T) {
		// Non-vacuity control, and the fail-closed half of the mechanism: prime's slimming arms on
		// the brief's presence, so a recycle that learned nothing must leave the fields absent
		// rather than present-and-empty.
		fx := newLifecycleFixture(t)

		if err := captureCheckpointWithFormula(t.Context(), fx.workDir, "notes", nil); err != nil {
			t.Fatalf("captureCheckpointWithFormula: %v", err)
		}
		cp, err := checkpoint.Read(fx.workDir)
		if err != nil || cp == nil {
			t.Fatalf("checkpoint.Read: %v (cp=%v)", err, cp)
		}
		if cp.HasResumeBrief() {
			t.Errorf("no formula was hooked, yet a brief was written: %+v", cp.ResumeNextAction)
		}
		if cp.ResumeVerified != "" || len(cp.ResumeArtifacts) != 0 {
			t.Errorf("partial brief written with no next action: verified=%q artifacts=%v",
				cp.ResumeVerified, cp.ResumeArtifacts)
		}
	})

	t.Run("the artifact list is bounded", func(t *testing.T) {
		// The brief is rendered into the resumed session's prime output, and an unbounded path
		// list there is exactly the token cost K16 exists to cut.
		fx := newLifecycleFixture(t)
		epic, _ := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)

		dirtyTree(t, fx, resumeArtifactCap*2)

		if err := captureCheckpointWithFormula(t.Context(), fx.workDir, "notes", nil); err != nil {
			t.Fatalf("captureCheckpointWithFormula: %v", err)
		}
		cp, err := checkpoint.Read(fx.workDir)
		if err != nil || cp == nil {
			t.Fatalf("checkpoint.Read: %v (cp=%v)", err, cp)
		}
		if len(cp.ModifiedFiles) <= resumeArtifactCap {
			t.Fatalf("fixture is not dirty enough to exercise the cap: %d modified files",
				len(cp.ModifiedFiles))
		}
		if len(cp.ResumeArtifacts) != resumeArtifactCap {
			t.Errorf("ResumeArtifacts = %d entries, want the cap of %d",
				len(cp.ResumeArtifacts), resumeArtifactCap)
		}
	})

	t.Run("all three recycle legs reach the interview", func(t *testing.T) {
		// The interview is conducted in one place precisely so the three legs cannot drift apart,
		// which makes "they all still route through it" the property worth pinning. Asserted
		// structurally because two of the three legs end in a tmux respawn (ADR-018), and a test
		// that reached the respawn to prove the checkpoint would be testing the wrong thing.
		legs := map[string]struct{ file, fn string }{
			"cooperative boundary": {"done.go", "boundaryHandoffExec"},
			"self handoff":         {"handoff.go", "runHandoffCore"},
			"PreCompact":           {"compact_handoff.go", "runCompactHandoffCore"},
		}
		for leg, site := range legs {
			t.Run(leg, func(t *testing.T) {
				if !callsFunc(t, site.file, site.fn, "captureCheckpointWithFormula") {
					t.Errorf("%s (%s) does not call captureCheckpointWithFormula; "+
						"this recycle leg leaves no interview behind", site.fn, site.file)
				}
			})
		}
	})
}

// dirtyTree leaves n untracked files where git will actually see them. The fixture's .gitignore
// covers .agentfactory/, so a scratch file written into the agent's own work dir is invisible to
// git status and would make an artifact assertion pass for no reason.
func dirtyTree(t *testing.T, fx lifecycleFixture, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		path := filepath.Join(fx.root, fmt.Sprintf("scratch%02d.txt", i))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// callsFunc reports whether the named declaration in file calls target. The declaration may be a
// func or a package-var seam holding a func literal, because boundaryHandoffExec is the latter.
func callsFunc(t *testing.T, file, decl, target string) bool {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	var body ast.Node
	ast.Inspect(parsed, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Name.Name == decl {
				body = node.Body
			}
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if name.Name == decl && i < len(node.Values) {
					body = node.Values[i]
				}
			}
		}
		return body == nil
	})
	if body == nil {
		t.Fatalf("%s: no declaration named %s", file, decl)
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == target {
			found = true
		}
		return !found
	})
	return found
}
