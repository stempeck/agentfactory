package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// Issue #541 Phase 6 — K13 STRUCTURAL SCANNER + L-4 control-plane-type invariant.
//
// This is the REVIEW-TIME layer of the teardown-foreclosure design (constraint C-1:
// "gate omission non-fatal for future surfaces"). The K8 decorator (helpers.go) enforces it at
// runtime for cmd-layer KillSession; K9 protects Manager.Stop; K3 classifies at the authority
// layer. K13 adds the COMPILE-OF-THE-TEST-SUITE gate: it walks PRODUCTION (non-_test.go) .go
// files under internal/ and fails the build if a call site of an audited teardown SHAPE appears
// that is NOT present in an explicit, class-tagged allowlist. So a new factory-teardown surface
// cannot be added without an accompanying classification decision in the same diff.
//
// It mirrors the #309 scanner (tmux_isolation_enforce_test.go) idiom — filepath.WalkDir, the
// //go:build integration skip (reused isIntegrationOnlyFile), findRepoRoot, and a non-vacuous
// planted-fixture proof — but INVERTS the file filter: #309 bans raw destructive tmux in
// *tests*; K13 enforces classification of every production teardown call site. The two scanners
// are complementary. This file self-exempts from the #309 pkill scan via skipIsolationSelfFiles.
//
// SCOPE (Gap #1, shape-bounded): the scanner matches only the three AUDITED call shapes below
// (KillSession(, mgr.Stop(/Manager.Stop(, pgrep/pkill). A novel teardown primitive — e.g. a raw
// exec.Command("tmux","kill-server"), a pkill outside the runPkill seam, or a new
// run("kill-session",…) inside internal/tmux — would NOT match and would pass silently. That is
// a bounded, owned residual (design-doc R2); widening to the teardown-verb family is future work.

// teardownCallPattern matches the three audited teardown call SHAPES:
//   - KillSession(  — anchored to the capital-K method/call token; it deliberately does NOT
//     match tmux.go's lowercase "kill-session" string literals in guardOp/run.
//   - mgr.Stop( / Manager.Stop(  — the Manager teardown call; the trailing "(" excludes the
//     bare "Manager.Stop interlock" prose at session.go:289, and comment-skipping excludes the
//     "Manager.Stop() call" comment at session.go:261. ticker.Stop() never matches.
//   - pgrep / pkill  — the K10 runPkill orphan-sweep seam (down.go). "\b" keeps it off the
//     "runPkill" identifier (capital P) and off pkill inside _test.go (not scanned here).
var teardownCallPattern = regexp.MustCompile(`KillSession\(|\bmgr\.Stop\(|Manager\.Stop\(|\b(pgrep|pkill)\b`)

// teardownAuditSentinel is the load-bearing marker every production teardown call/decl site of an
// audited shape MUST carry on its own line: a trailing `//af:teardown:<class>` naming its
// Authority-Matrix class. It is part of the scanner MECHANISM, not explanatory prose — the scan keys
// on it — which is why it lives in the source at the site rather than in an inventory here.
//
// It REPLACES the former line-number-keyed allowlist (#679 T9). That map pinned "relpath:line",
// so an edit ANYWHERE above a site re-anchored it, and the fix each time was to re-measure the number
// and append a history line — a maintenance burden that diverged from the code and a guard kept green
// by bookkeeping rather than by structure. The sentinel travels WITH the call: a site that moves
// keeps its classification for free, and a NEW audited call with no sentinel still fails the scan, so
// the C-1 guarantee holds unchanged — no teardown surface may be added without a classification
// decision in the same diff. Keyed per LINE, it also gives each of the three byte-identical
// `KillSession(sessionID)` calls in Manager.Start() its own marker, which a function+call-expression
// key could not: those three collapse to one key and a planted fourth identical call would match the
// allowlisted key and pass, defeating the planted-call non-vacuity proof.
//
// Classes (design-doc.md Authority Matrix L188-213):
//
//	decl        — interface/method declaration (matches the token but is not a call)
//	self        — permitted self-scope (guarded self-forward / af done self-terminate)
//	restorative — kill-and-recreate / failure cleanup during Start()/respawn (not teardown)
//	gated       — teardown gated by scopedStopAllowed (an authority gate): the af down per-agent
//	              Stop loop (K5/K9) and the af sling --reset stop (#548 P2/P5)
//	dispatch    — dispatcher-session teardown / orphan sweep (K5/K7/K10 down --all path)
var teardownAuditSentinel = regexp.MustCompile(`//af:teardown:(decl|self|restorative|gated|dispatch)\b`)

// scanTeardownCallSites walks root for PRODUCTION (non-_test.go) .go files and returns
// "relpath:line: reason" findings for any audited teardown call SHAPE whose own line does NOT
// carry a teardownAuditSentinel classifying it. //go:build integration files and comment-only
// lines are skipped. Factored out so the non-vacuous "catches planted" proof can run the SAME
// logic over a t.TempDir() fixture tree.
func scanTeardownCallSites(root, repoRoot string) []string {
	var findings []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(data)
		if isIntegrationOnlyFile(content) {
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if !teardownCallPattern.MatchString(line) {
				continue
			}
			if teardownAuditSentinel.MatchString(line) {
				continue
			}
			key := fmt.Sprintf("%s:%d", rel, i+1)
			findings = append(findings, fmt.Sprintf("%s: unclassified teardown call site: %s",
				key, strings.TrimSpace(line)))
		}
		return nil
	})
	return findings
}

// TestTeardownScannerAllowlistedCallSites is the K13 clean-tree gate (AC-1): every audited
// teardown call site under internal/ must carry a teardownAuditSentinel classifying it. A new,
// unclassified KillSession(/Manager.Stop(/pkill site fails the build here.
func TestTeardownScannerAllowlistedCallSites(t *testing.T) {
	root := findRepoRoot(t)
	internalDir := filepath.Join(root, "internal")
	findings := scanTeardownCallSites(internalDir, root)
	if len(findings) > 0 {
		t.Errorf("found %d unclassified production teardown call site(s) of an audited shape "+
			"(KillSession(/mgr.Stop(/pgrep|pkill).\nEach new site MUST be classified: add a trailing "+
			"//af:teardown:<class> sentinel on the site's own line, choosing the Authority-Matrix "+
			"class (design-doc.md L188-213). (#541 K13 / constraint C-1)\nFindings:\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}
}

// TestTeardownScannerCatchesPlantedCallSite is the non-vacuity proof (AC-2): the scan must
// REPORT an unclassified KillSession( planted in a temp production-.go fixture. It is a
// TOP-LEVEL test (not a subtest) because AC-2's `-run 'Scanner.*Planted|Scanner.*Catches'` is
// slash-less and therefore selects only top-level function names. The fixture is a NON-_test.go
// .go file because the scanner (correctly) skips _test.go.
func TestTeardownScannerCatchesPlantedCallSite(t *testing.T) {
	dir := t.TempDir()
	// Written as source TEXT under t.TempDir(); never compiled — only read+regex-scanned.
	fixture := "package planted\n\n" +
		"type fakeTmux struct{}\n\n" +
		"func (fakeTmux) KillSession(name string) error { return nil }\n\n" +
		"func teardownEverything(x fakeTmux) {\n" +
		"\t_ = x.KillSession(\"af-manager\")\n" +
		"}\n"
	if err := os.WriteFile(filepath.Join(dir, "planted.go"), []byte(fixture), 0o644); err != nil {
		t.Fatalf("writing planted fixture: %v", err)
	}
	got := scanTeardownCallSites(dir, dir)
	if len(got) == 0 {
		t.Fatal("K13 scan returned ZERO findings for a planted unclassified KillSession( " +
			"call site — the scan is vacuous")
	}
}

// TestControlPlaneTypeManagerInteractive is the L-4 invariant (AC-3): `manager` is the SOLE
// interactive-typed agent in the CANONICAL .agentfactory/agents.json. This locks K9's "the
// interactive control plane is manager" assumption so it cannot silently lapse. It loads the
// exact canonical path (config.AgentsConfigPath) and NEVER globs — a stale mirror exists at
// todos/incident_evidence_20260610/factory-config/agents.json (peer-review MUST-FIX #1).
func TestControlPlaneTypeManagerInteractive(t *testing.T) {
	root := findRepoRoot(t)
	path := config.AgentsConfigPath(root)
	cfg, err := config.LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("loading canonical agents.json (%s): %v", path, err)
	}
	mgr, ok := cfg.Agents["manager"]
	if !ok {
		t.Fatalf("canonical agents.json has no \"manager\" agent")
	}
	if mgr.Type != "interactive" {
		t.Errorf("manager.Type = %q, want \"interactive\" (L-4)", mgr.Type)
	}
	var interactive []string
	for name, entry := range cfg.Agents {
		if entry.Type == "interactive" {
			interactive = append(interactive, name)
		}
	}
	sort.Strings(interactive)
	if len(interactive) != 1 || interactive[0] != "manager" {
		t.Errorf("interactive-typed agents = %v, want exactly [manager] — K9's manager "+
			"protection assumes manager is the only interactive control plane (L-4)", interactive)
	}
}
