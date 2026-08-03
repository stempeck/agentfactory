package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// worktreeDivergenceFixture simulates the on-disk divergence between the true
// factory-root formula store and a worktree-local git-tracked duplicate, without
// needing a real `git worktree add`. Mirrors the live, empirically-confirmed shape
// (this fable-implement run's own Phase-1/Phase-3 investigation): a worktree's
// `.agentfactory/store/formulas/` is a real, independently-writable directory
// (worktree.go:55-59 does not symlink `store`), not a symlink, and it starts
// byte-identical to the true root's copy.
type worktreeDivergenceFixture struct {
	trueRoot  string // the true factory root — resolveInvokerRoot always lands here
	decoyRoot string // simulates a worktree's own .agentfactory/store/formulas/ tree
	agent     string
	formula   string
}

func setupWorktreeDivergenceFixture(t *testing.T, agent, formulaName string) worktreeDivergenceFixture {
	t.Helper()
	trueRoot := setupTestFactoryForImprovement(t, map[string]bool{agent: true})
	if err := os.WriteFile(improvementHookFile(trueRoot), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write factory toggle: %v", err)
	}
	writeFormulaFile(t, trueRoot, formulaName, true)

	decoyRoot := t.TempDir()
	writeFormulaFile(t, decoyRoot, formulaName, true) // byte-identical at creation, per the live empirical observation

	return worktreeDivergenceFixture{trueRoot: trueRoot, decoyRoot: decoyRoot, agent: agent, formula: formulaName}
}

// --- AC1 / C3 / C4: the marker's recorded edit-target must agree with what
// completion resolution independently reconstructs (fix-neutral consistency check —
// passes regardless of which side of the fix changes, as long as they agree). ---

func TestEvaluateImprovementFire_MarkerFormulaPathMatchesCompletionResolution(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	writeFormulaFile(t, root, "fx", true)

	fired, agent, _, reason := evaluateImprovementFire(root, root, "inst-1", "manager", "Formula: fx", false)
	if !fired {
		t.Fatalf("expected fire, got reason=%q", reason)
	}
	m, err := readImprovementMarkerFile(improvementPendingFile(root, agent))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	completionResolved := filepath.Join(config.FormulasDir(root), m.Formula+".formula.toml")
	if m.FormulaPath != completionResolved {
		t.Errorf("marker.FormulaPath = %q, must resolve to the same file runImprovementCompleteCore reconstructs (%q) — AC1", m.FormulaPath, completionResolved)
	}
}

// --- AC1 / AC7 (code-side half) / C1 / C2: the instruction's edit target must be
// the absolute factory-root path, not a bare relative fragment. ---

func TestImprovementInstruction_EditTargetIsAbsoluteFactoryRoot(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	writeFormulaFile(t, root, "fx", true)

	instruction, f, ok := improvementInstruction(root, "Formula: fx")
	if !ok {
		t.Fatal("expected improvementInstruction to resolve")
	}
	want := "formula at " + f.AbsPath
	if !strings.Contains(instruction, want) {
		t.Errorf("instruction does not name the absolute edit target %q — AC1/AC7:\n%s", want, instruction)
	}
}

// --- AC3 / C1: the verification step must not tell the agent to trust an exit code
// that is always 0 by design. ---

func TestImprovementInstruction_VerificationDoesNotClaimExitCodeAlone(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	writeFormulaFile(t, root, "fx", true)

	instruction, _, ok := improvementInstruction(root, "Formula: fx")
	if !ok {
		t.Fatal("expected improvementInstruction to resolve")
	}
	if strings.Contains(instruction, "(exit 0)") {
		t.Errorf("instruction must not tell the agent to trust exit code alone (af formula show always exits 0 by design) — AC3:\n%s", instruction)
	}
	// The negative guard above pins the ABSENCE of one 2026-07 phrasing; on its own it
	// passes for any reworded exit-code claim (PR #565 review, F-11, demonstrated with a
	// fully green build). The positive guard pins the SEMANTICS the AC actually requires.
	if !strings.Contains(instruction, `"state":"error"`) {
		t.Errorf("instruction must teach the JSON-body check (a %q key means invalid) — AC3:\n%s", `"state":"error"`, instruction)
	}
}

// --- AC4 / C1: the WORKTREE_CONTAINMENT claim must be hedged, not an unconditional
// "will fire" assertion this fix's test surface cannot itself prove deterministically. ---

func TestImprovementInstruction_ContainmentClaimIsHedgedNotUnconditional(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	writeFormulaFile(t, root, "fx", true)

	instruction, _, ok := improvementInstruction(root, "Formula: fx")
	if !ok {
		t.Fatal("expected improvementInstruction to resolve")
	}
	if strings.Contains(instruction, "will produce a WORKTREE_CONTAINMENT advisory") {
		t.Errorf("instruction must not make an unhedged containment claim — AC4:\n%s", instruction)
	}
	// As with AC3, absence of one phrasing is not the property. AC4 permits an accurate
	// claim OR silence ("or are not made"), so the positive guard is a disjunction: either
	// the containment sentence is hedged, or there is no containment claim at all. Written
	// as a disjunction rather than a literal so it survives either outcome of the
	// hedge-vs-unconditional question (PR #565 review, F-11 and F-13).
	mentionsContainment := strings.Contains(instruction, "WORKTREE_CONTAINMENT")
	if mentionsContainment && !strings.Contains(instruction, "may trigger") {
		t.Errorf("instruction mentions WORKTREE_CONTAINMENT but does not hedge it; AC4 requires an accurate claim or none:\n%s", instruction)
	}
}

// --- AC6: from the verdict mail alone, the operator must be able to tell WHERE to
// act — the bare formula name is not enough given the whole bug was location
// ambiguity. Drives runImprovementCompleteCore (signature unchanged by this fix) so
// this test compiles regardless of how improvementOutcomeMessage's own signature
// changes internally. ---

func TestImprovementComplete_OutcomeMessage_NamesArtifactLocation(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	absFormula := writeFormulaFile(t, root, "fx", true)
	_, _, subject, body := stubTeardownAndMail(t)

	m := improvementMarker{Formula: "fx", Caller: "manager", TerminateOnComplete: false, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, root, false); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !strings.Contains(*subject+*body, absFormula) {
		t.Errorf("verdict mail must name the artifact's location so the operator knows where to act (AC6); got subject=%q body=%q, want to contain %q", *subject, *body, absFormula)
	}
}

// --- AC6 continued: the verdict must not assert an event that did not happen. The test
// above records a marker with no FormulaSHA256, so `changed` is always true there and the
// unchanged branch goes unexercised (PR #565 review, F-7). This pins the unchanged case
// with a REAL baseline hash, so no edit occurred and the verdict must not say one did. ---

func TestImprovementComplete_UnchangedVerdictDoesNotAssertAnEdit(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	absFormula := writeFormulaFile(t, root, "fx", true)
	_, _, subject, body := stubTeardownAndMail(t)

	// Real hash of the untouched file ⇒ changed == false ⇒ the "unchanged" verdict.
	baselineSHA, err := formulaSHA256(absFormula)
	if err != nil {
		t.Fatal(err)
	}
	m := improvementMarker{
		Formula: "fx", Caller: "manager", TerminateOnComplete: false,
		FormulaSHA256: baselineSHA,
		FiredAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, root, false); err != nil {
		t.Fatalf("complete: %v", err)
	}

	if !strings.Contains(*subject, "unchanged") {
		t.Fatalf("fixture must produce an unchanged verdict, got subject=%q", *subject)
	}
	if strings.Contains(*body, "Edited at") {
		t.Errorf("verdict says %q while reporting an unchanged formula — no edit occurred anywhere; state the location without asserting an event (AC6): body=%q", "Edited at", *body)
	}
	// The location must still be present — F-7 fixes the wording, it does not remove AC6.
	if !strings.Contains(*body, absFormula) {
		t.Errorf("verdict body must still name the artifact location (AC6), got %q", *body)
	}
}

// --- AC6/AC5: the path must reach a surface a human actually reads. runImprovementCompleteCore
// prints only the subject, and the subject carries no path, so the operator-facing pane never
// shows where to act; the mail goes to marker.Caller (an agent) or supervisor (an agent).
// (PR #565 review, F-8.) ---

func TestImprovementComplete_VerdictPathReachesPrintedSurface(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	absFormula := writeFormulaFile(t, root, "fx", true)
	stubTeardownAndMail(t)

	m := improvementMarker{Formula: "fx", Caller: "manager", TerminateOnComplete: false, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runImprovementCompleteCore(agentDir, root, false); err != nil {
			t.Fatalf("complete: %v", err)
		}
	})

	if !strings.Contains(out, absFormula) {
		t.Errorf("the printed verdict must name the formula path so an operator reading the pane can tell WHERE to act; the mail recipient is always another agent (AC6). got:\n%s\nwant to contain: %s", out, absFormula)
	}
}

// --- AC9: with two divergent on-disk copies of the same formula name, prove (a) an
// edit at the decoy (worktree-shaped) location stays invisible to the verdict — this
// was already true before this fix and must stay true — and (b) the instruction's
// own claimed edit target is the SAME file the verdict just proved it reads, not the
// decoy. ---

func TestImprovementComplete_WorktreeDivergence_InstructionTargetsSameFileVerdictReads(t *testing.T) {
	fx := setupWorktreeDivergenceFixture(t, "alpha", "fx")

	// (a) DO-NOT-CHANGE protective half.
	decoyPath := filepath.Join(config.FormulasDir(fx.decoyRoot), fx.formula+".formula.toml")
	if err := os.WriteFile(decoyPath, []byte("formula = \"fx\"\n\n[[steps]]\nid = \"decoy-edit\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	agentDir := config.AgentDir(fx.trueRoot, fx.agent)
	_, _, subject, _ := stubTeardownAndMail(t)
	trueAbsFormula := filepath.Join(config.FormulasDir(fx.trueRoot), fx.formula+".formula.toml")
	trueSHA, err := formulaSHA256(trueAbsFormula)
	if err != nil {
		t.Fatal(err)
	}
	m := improvementMarker{Formula: fx.formula, Caller: "manager", TerminateOnComplete: false, FormulaSHA256: trueSHA, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(fx.trueRoot, fx.agent, m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, fx.trueRoot, false); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !strings.Contains(*subject, "unchanged") {
		t.Errorf("a decoy-only edit must be invisible to the verdict (still 'unchanged'), got %q", *subject)
	}

	// (b) The AC9/AC1 assertion this test exists for.
	instruction, f, ok := improvementInstruction(fx.trueRoot, "Formula: "+fx.formula)
	if !ok {
		t.Fatal("expected improvementInstruction to resolve")
	}
	if f.AbsPath != trueAbsFormula {
		t.Fatalf("f.AbsPath = %q, want the true-root path %q (fixture self-check)", f.AbsPath, trueAbsFormula)
	}
	if !strings.Contains(instruction, "formula at "+f.AbsPath) {
		t.Errorf("instruction must target the SAME file the verdict reads (%q), not a decoy-compatible relative fragment — AC9:\n%s", f.AbsPath, instruction)
	}
}

// --- AC8 non-worktree regression guard (DO-NOT-CHANGE item 1): a structural,
// wording-independent pin on the resolved location for an agent whose cwd already
// equals the factory root. ---

func TestImprovementInstruction_NonWorktree_TargetUnchanged(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	writeFormulaFile(t, root, "fx", true)

	_, f, ok := improvementInstruction(root, "Formula: fx")
	if !ok {
		t.Fatal("expected improvementInstruction to resolve")
	}
	want := filepath.Join(root, ".agentfactory", "store", "formulas", "fx.formula.toml")
	if f.AbsPath != want {
		t.Errorf("f.AbsPath = %q, want %q — AC8 non-worktree regression guard", f.AbsPath, want)
	}
}

// --- AC8 fail-open regression guard (DO-NOT-CHANGE item 2), worktree variant: an
// invalid edit at the (post-fix) resolved target must still tear down cleanly. ---

func TestImprovementComplete_WorktreeInvalidEdit_StillTearsDown(t *testing.T) {
	fx := setupWorktreeDivergenceFixture(t, "alpha", "fx")
	trueAbsFormula := filepath.Join(config.FormulasDir(fx.trueRoot), fx.formula+".formula.toml")
	if err := os.WriteFile(trueAbsFormula, []byte("this is not valid toml {{{ ]]] ===\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	agentDir := config.AgentDir(fx.trueRoot, fx.agent)
	teardowns, _, subject, _ := stubTeardownAndMail(t)
	m := improvementMarker{Formula: fx.formula, Caller: "manager", TerminateOnComplete: true, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(fx.trueRoot, fx.agent, m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, fx.trueRoot, false); err != nil {
		t.Fatalf("broken formula must fail open (exit 0), got err %v", err)
	}
	if !strings.Contains(*subject, "validation FAILED") {
		t.Errorf("verdict must say validation FAILED, got %q", *subject)
	}
	if *teardowns != 1 {
		t.Errorf("fail-open must still tear down, got %d — AC8/DO-NOT-CHANGE 2", *teardowns)
	}
}

// --- ADR-015 boundary regression guard (DO-NOT-CHANGE item 4): the instruction must
// never point the agent at the promotion path itself. ---

func TestImprovementInstructionTemplate_NeverAutoPromotes(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	writeFormulaFile(t, root, "fx", true)

	instruction, _, ok := improvementInstruction(root, "Formula: fx")
	if !ok {
		t.Fatal("expected improvementInstruction to resolve")
	}
	if !strings.Contains(instruction, "leave promotion to the human operator") {
		t.Errorf("instruction must keep the operator-promotion disclaimer — ADR-015 DO-NOT-CHANGE:\n%s", instruction)
	}
	for _, forbidden := range []string{"install_formulas", "sync-formulas"} {
		if strings.Contains(instruction, forbidden) {
			t.Errorf("instruction must not mention %q — the agent must never touch the promotion path itself (ADR-015)", forbidden)
		}
	}
}

// --- AC5: an improvement produced by a dispatched agent must remain recoverable
// even when its worktree is reclaimed by a force path. Simulates ForceRemove's
// filesystem effect (worktree.go:771-780: `git worktree remove --force` + an
// os.RemoveAll fallback) on the decoy root, then checks whether the agent's edit —
// placed wherever the CURRENT instruction text actually names — survives. ---

func TestImprovementComplete_WorktreeForceReclaimed_EditSurvives(t *testing.T) {
	fx := setupWorktreeDivergenceFixture(t, "alpha", "fx")

	instruction, _, ok := improvementInstruction(fx.trueRoot, "Formula: "+fx.formula)
	if !ok {
		t.Fatal("expected improvementInstruction to resolve")
	}

	editTarget := resolveWriteTargetFromInstruction(t, instruction, fx)
	editedContent := []byte("formula = \"fx\"\n\n[[steps]]\nid = \"real-improvement-edit\"\n")
	if err := os.WriteFile(editTarget, editedContent, 0o644); err != nil {
		t.Fatalf("simulate agent edit at %s: %v", editTarget, err)
	}

	if err := os.RemoveAll(fx.decoyRoot); err != nil {
		t.Fatalf("simulate ForceRemove: %v", err)
	}

	trueAbsFormula := filepath.Join(config.FormulasDir(fx.trueRoot), fx.formula+".formula.toml")
	got, err := os.ReadFile(trueAbsFormula)
	if err != nil {
		t.Fatalf("true-root formula must survive worktree reclamation regardless: %v", err)
	}
	if string(got) != string(editedContent) {
		t.Errorf("AC5: the agent's real edit did not survive a force-reclaimed worktree — got %q, want %q (edit landed at %q, which was destroyed)", got, editedContent, editTarget)
	}
}

// resolveWriteTargetFromInstruction extracts where an agent literally following the
// hook's instruction text would write. Today's (pre-fix) instruction embeds a bare
// relative fragment with no root anchor, which this helper resolves against the
// fixture's decoy (worktree-shaped) root — exactly what an agent whose own cwd is
// inside a worktree would naturally do with an unanchored relative path. Once the
// fix lands, the instruction embeds the absolute true-root path directly and this
// helper's relative-fallback branch is never taken.
func resolveWriteTargetFromInstruction(t *testing.T, instruction string, fx worktreeDivergenceFixture) string {
	t.Helper()
	trueAbsFormula := filepath.Join(config.FormulasDir(fx.trueRoot), fx.formula+".formula.toml")
	if strings.Contains(instruction, "formula at "+trueAbsFormula) {
		return trueAbsFormula
	}
	relFragment := ".agentfactory/store/formulas/" + fx.formula + ".formula.toml"
	if strings.Contains(instruction, "formula at "+relFragment) {
		return filepath.Join(fx.decoyRoot, relFragment)
	}
	t.Fatalf("instruction names neither the true-root absolute path nor the known relative fragment — cannot determine edit target:\n%s", instruction)
	return ""
}

// initGitRepoForImprovementTest initializes a real git repo with an initial commit —
// `git worktree add` requires at least one commit to branch from. Mirrors
// internal/worktree/worktree_test.go's initGitRepo (unexported there, package
// worktree; duplicated here since internal/cmd cannot import an unexported helper
// from a different package's test binary).
func initGitRepoForImprovementTest(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	readmePath := filepath.Join(dir, "README")
	if err := os.WriteFile(readmePath, []byte("init"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	for _, args := range [][]string{
		{"add", "README"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestImprovementComplete_RealGitWorktree_SurvivesRealForceRemove closes the two
// sharpest gaps a blind review of this fix's diff found: (1) every other
// worktree-shaped test in this file simulates the divergence with a hand-built decoy
// directory, never a real `git worktree add`; (2)
// TestImprovementComplete_WorktreeForceReclaimed_EditSurvives simulates ForceRemove's
// effect with a bare os.RemoveAll rather than exercising the real
// worktree.ForceRemove (worktree.go:771-795) and its real `git worktree remove
// --force` + fallback logic. This test uses worktree.Create (the exact function
// worktree.go:459-544 the issue cites for the git-tracked duplicate store) to make a
// genuine git worktree — carrying a REAL .factory-root redirect file written by
// production code, not by the test — resolves the instruction from inside it exactly
// as a dispatched agent's session would, writes the "agent's edit" wherever that real
// instruction names, tears the worktree down with the real worktree.ForceRemove, and
// drives the real completion path afterward to confirm the verdict sees the survived
// edit end to end.
func TestImprovementComplete_RealGitWorktree_SurvivesRealForceRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	trueRoot := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	initGitRepoForImprovementTest(t, trueRoot)
	if err := os.WriteFile(improvementHookFile(trueRoot), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write factory toggle: %v", err)
	}
	writeFormulaFile(t, trueRoot, "fx", true)

	wtPath, meta, err := worktree.Create(trueRoot, "alpha", worktree.CreateOpts{})
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}

	// Sanity-check the REAL redirect file worktree.Create wrote (worktree.go:510-518)
	// resolves back to trueRoot exactly as resolveInvokerRoot would from inside it —
	// this is the real mechanism, not an assumption about it.
	redirect, err := os.ReadFile(filepath.Join(wtPath, ".agentfactory", ".factory-root"))
	if err != nil {
		t.Fatalf("read real .factory-root redirect: %v", err)
	}
	if strings.TrimSpace(string(redirect)) != trueRoot {
		t.Fatalf(".factory-root redirect = %q, want %q", strings.TrimSpace(string(redirect)), trueRoot)
	}

	instruction, _, ok := improvementInstruction(trueRoot, "Formula: fx")
	if !ok {
		t.Fatal("expected improvementInstruction to resolve")
	}

	// Determine the edit target purely from the real instruction text, exactly as a
	// dispatched agent inside this real worktree would have to.
	trueAbsFormula := filepath.Join(config.FormulasDir(trueRoot), "fx.formula.toml")
	relFragment := ".agentfactory/store/formulas/fx.formula.toml"
	var editTarget string
	switch {
	case strings.Contains(instruction, "formula at "+trueAbsFormula):
		editTarget = trueAbsFormula
	case strings.Contains(instruction, "formula at "+relFragment):
		editTarget = filepath.Join(wtPath, relFragment) // today's bug: resolves against the real worktree root
	default:
		t.Fatalf("instruction names neither the true-root path nor the known relative fragment:\n%s", instruction)
	}

	editedContent := []byte("formula = \"fx\"\n\n[[steps]]\nid = \"real-git-worktree-edit\"\n")
	if err := os.WriteFile(editTarget, editedContent, 0o644); err != nil {
		t.Fatalf("simulate agent edit at %s: %v", editTarget, err)
	}

	// The REAL teardown function the issue cites (worktree.go:771-795), not a
	// simulation of its effect.
	if err := worktree.ForceRemove(trueRoot, meta); err != nil {
		t.Fatalf("worktree.ForceRemove: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree dir should be gone after ForceRemove, stat err=%v", err)
	}

	got, err := os.ReadFile(trueAbsFormula)
	if err != nil {
		t.Fatalf("true-root formula must survive real worktree ForceRemove regardless: %v", err)
	}
	if string(got) != string(editedContent) {
		t.Errorf("AC5: the agent's real edit did not survive a real ForceRemove — got %q, want %q (edit landed at %q)", got, editedContent, editTarget)
	}

	// Drive the REAL completion path from the (now-deleted) worktree's would-be agent
	// dir, exactly as `af improvement complete` does — proving the verdict reads the
	// survived true-root file, not the destroyed worktree copy, end to end.
	agentDir := config.AgentDir(trueRoot, "alpha")
	_, _, subject, _ := stubTeardownAndMail(t)
	m := improvementMarker{Formula: "fx", Caller: "manager", TerminateOnComplete: false, FormulaSHA256: "0000000000000000000000000000000000000000000000000000000000000000", FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(trueRoot, "alpha", m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, trueRoot, false); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !strings.Contains(*subject, "changed") || !strings.Contains(*subject, "validation passed") {
		t.Errorf("verdict after real worktree teardown = %q, want it to see the survived, valid edit", *subject)
	}
}
