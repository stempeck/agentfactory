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
//     bare "Manager.Stop interlock" prose at session.go:185, and comment-skipping excludes the
//     "Manager.Stop() call" comment at session.go:157. ticker.Stop() never matches.
//   - pgrep / pkill  — the K10 runPkill orphan-sweep seam (down.go). "\b" keeps it off the
//     "runPkill" identifier (capital P) and off pkill inside _test.go (not scanned here).
var teardownCallPattern = regexp.MustCompile(`KillSession\(|\bmgr\.Stop\(|Manager\.Stop\(|\b(pgrep|pkill)\b`)

// allowedTeardownSites is the class-tagged inventory of every production teardown call/decl
// site of an audited shape, re-verified 2026-07-18 for #548 Phase 2 (gate wiring atop c9dcf98e).
// The done.go and sling.go anchors were re-measured on 2026-07-23 when #329 Phase 3 added
// telemetry recording above them; both are the same call sites, moved, and neither changed
// class — this inventory pins line numbers, so an edit anywhere above a site re-anchors it.
// Re-anchored again on 2026-07-23 for #329 Phase 4a (telemetry launch-env injection added lines
// above the session.go/helpers.go/up.go KillSession sites); same sites, moved, same classes.
// Re-anchored again on 2026-07-28 for #561 (continuation directive added above the done.go
// site); same site, moved, same class.
// A matched line whose "relpath:line" key is absent here fails the scan — so adding a new audited
// call site REQUIRES appending a classified entry in the same diff (design constraint C-1,
// review-time). Classes:
//
//	decl        — interface/method declaration (matches the token but is not a call)
//	self        — permitted self-scope (guarded self-forward / af done self-terminate)
//	restorative — kill-and-recreate / failure cleanup during Start()/respawn (not teardown)
//	gated       — teardown guarded by an authority gate routed through scopedStopAllowed: the
//	              af down per-agent Stop loop (K5/K9) and the af sling --reset stop (#548 P2/P5)
//	dispatch    — dispatcher-session teardown / orphan sweep (K5/K7/K10 down --all path)
//
// Per integration.md I4 (inventory rows 1-11) + design-doc.md Authority Matrix (L188-213).
var allowedTeardownSites = map[string]string{
	// --- KillSession( : interface / method DECLARATIONS (token match, not a call) ---
	"internal/cmd/helpers.go:76":      "decl", // cmdTmux interface method decl
	"internal/cmd/helpers.go:104":     "decl", // K8 authKillGuard.KillSession override decl (Phase 4)
	"internal/session/session.go:188": "decl", // session-tmux interface method decl
	"internal/tmux/tmux.go:266":       "decl", // *Tmux.KillSession method decl
	// --- KillSession( : real calls ---
	"internal/cmd/helpers.go:108":     "self",        // K8 guarded self-forward g.cmdTmux.KillSession (Phase 4)
	"internal/session/session.go:394": "restorative", // Start() zombie kill-and-recreate
	"internal/session/session.go:587": "restorative", // Start() shell-ready failure cleanup
	"internal/session/session.go:594": "restorative", // Start() memory-check failure cleanup
	"internal/session/session.go:603": "restorative", // Start() send-keys failure cleanup
	"internal/session/session.go:825": "gated",       // Manager.Stop() teardown (K9 backstop)
	"internal/cmd/up.go:552":          "restorative", // watchdog respawn
	"internal/cmd/down.go:155":        "dispatch",    // watchdog-session teardown (K5)
	"internal/cmd/down.go:161":        "dispatch",    // dispatch-session teardown (K5)
	"internal/cmd/done.go:716":        "self",        // af done self-terminate
	"internal/cmd/dispatch.go:1549":   "dispatch",    // af dispatch stop teardown (K7)
	// --- Manager.Stop (mgr.Stop() calls) ---
	"internal/cmd/down.go:106":  "gated", // K5 per-agent Stop loop (refusal proven by loop-never-entered)
	"internal/cmd/sling.go:202": "gated", // --reset stop, gated by scopedStopAllowed (#548 P5): self/dispatcher/manager tiers with not-running + dispatch-daemon carve-outs, consistent with the af down per-agent Stop site (down.go:106)
	// --- pgrep / pkill : K10 runPkill orphan-sweep seam ---
	"internal/cmd/down.go:310": "dispatch", // exec.Command("pgrep", ...) orphan-sweep seam (K10)
	"internal/cmd/down.go:313": "dispatch", // exec.Command("pkill", ...) orphan-sweep seam (K10)
}

// scanTeardownCallSites walks root for PRODUCTION (non-_test.go) .go files and returns
// "relpath:line: reason" findings for any audited teardown call SHAPE whose relpath:line key
// (relative to repoRoot, slash-normalized) is not in allowedTeardownSites. //go:build
// integration files and comment-only lines are skipped. Factored out so the non-vacuous
// "catches planted" proof can run the SAME logic over a t.TempDir() fixture tree.
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
			key := fmt.Sprintf("%s:%d", rel, i+1)
			if _, ok := allowedTeardownSites[key]; !ok {
				findings = append(findings, fmt.Sprintf("%s: unclassified teardown call site: %s",
					key, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	return findings
}

// TestTeardownScannerAllowlistedCallSites is the K13 clean-tree gate (AC-1): every audited
// teardown call site under internal/ must be present in the class-tagged allowlist. A new,
// unclassified KillSession(/Manager.Stop(/pkill site fails the build here.
func TestTeardownScannerAllowlistedCallSites(t *testing.T) {
	root := findRepoRoot(t)
	internalDir := filepath.Join(root, "internal")
	findings := scanTeardownCallSites(internalDir, root)
	if len(findings) > 0 {
		t.Errorf("found %d unclassified production teardown call site(s) of an audited shape "+
			"(KillSession(/mgr.Stop(/pgrep|pkill).\nEach new site MUST be classified: append a "+
			"\"relpath:line\": \"<class>\" entry to allowedTeardownSites in this file, choosing the "+
			"Authority-Matrix class (design-doc.md L188-213). (#541 K13 / constraint C-1)\nFindings:\n  %s",
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
