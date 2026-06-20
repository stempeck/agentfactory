//go:build integration

package cmd

// End-to-end production-path proof for the #386 worktree-containment interlock (Phase 4).
//
// This file is compiled ONLY under -tags=integration, so it freely reuses the
// (non-build-tagged) helpers in containment_test.go and mail_test.go —
// installContainmentRecorder, setupWorktreeContainmentEnv, bashPayload, writePayload,
// decodeAdditionalContext, and the recordedBead type. Re-declaring any of them here would
// be a duplicate-symbol compile error.
//
// What this proves that the Phase-2 unit tests do not:
//   1. The corrective the test drives is the SAME command production settings wire — we call
//      claude.EnsureSettings(agentDir, claude.Autonomous) and read `af containment-check`
//      back out of the written settings.json (production-path provenance).
//   2. The real incident shape: a prose-free formula that has ALREADY drifted and then runs a
//      subsequent command from the drifted location (persisted drift, no literal `cd` in the
//      call), not just the easy single-`cd` case.
//
// Isolation note: under -tags=integration storeGuardActive == false
// (storeguard_integration.go:9), so the real store path is live for the suite. What isolates
// THIS test from a real `af mail send` is the sendContainmentMail seam reassignment (via
// installContainmentRecorder), reassigned BEFORE the core is driven — not the store guard.
//
// Delivery contract (AC-8): the corrective lands in the agent's OWN inbox with no supervisor
// and no watchdog in the path. Those processes are never launched here; their absence is the
// default. The `-run Containment` filter selects these tests (and re-runs the Phase-2
// TestContainment_* unit tests under the integration build — expected and harmless).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/claude"
)

// settingsHasContainmentHook parses a written settings.json and reports whether any PreToolUse
// hook command invokes `af containment-check`. It walks the real production template structure
// rather than a raw substring match, proving the hook is wired under PreToolUse specifically.
func settingsHasContainmentHook(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings.json %q: %v", path, err)
	}
	var parsed struct {
		Hooks struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings.json %q is not valid JSON: %v", path, err)
	}
	for _, group := range parsed.Hooks.PreToolUse {
		for _, h := range group.Hooks {
			if strings.Contains(h.Command, "af containment-check") {
				return true
			}
		}
	}
	return false
}

// TestContainment_EndToEnd_ProductionPath drives runContainmentCheckCore exactly as the wired
// settings.json invokes `af containment-check`, through the real detect→resolve→dedup path with
// only the mail egress faked. It proves the wired-command provenance, then proves a drift yields
// exactly one self-addressed corrective in the agent's OWN inbox; no supervisor and no watchdog
// is launched (AC-8).
func TestContainment_EndToEnd_ProductionPath(t *testing.T) {
	worktreeRoot, agentDir := setupWorktreeContainmentEnv(t, "solver")

	// Provenance: the production pipeline (claude.EnsureSettings) writes the hook the test drives.
	if err := claude.EnsureSettings(agentDir, claude.Autonomous); err != nil {
		t.Fatalf("EnsureSettings(autonomous): %v", err)
	}
	settingsPath := filepath.Join(agentDir, ".claude", "settings.json")
	if !settingsHasContainmentHook(t, settingsPath) {
		t.Fatalf("written settings.json must wire `af containment-check` under PreToolUse: %s", settingsPath)
	}

	// Seam isolation MUST precede driving the core, or the production mail.NewRouter+Send runs.
	recs := installContainmentRecorder(t)

	// Drift → exactly one self-addressed corrective. No supervisor and no watchdog are started;
	// delivery is via the agent's own inbox seam, never an escalation to an absent supervisor.
	var stdout bytes.Buffer
	p := bashPayload("cd /parent && touch x", worktreeRoot)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("runContainmentCheckCore must always exit 0 (ADR-007): %v", err)
	}

	if len(*recs) != 1 {
		t.Fatalf("drift must deliver exactly one corrective bead, got %d", len(*recs))
	}
	b := (*recs)[0]
	if b.role != "solver" {
		t.Errorf("corrective must be self-addressed to the agent's own role (AC-8); role = %q, want solver", b.role)
	}
	if b.subject != "WORKTREE_CONTAINMENT" {
		t.Errorf("subject = %q, want WORKTREE_CONTAINMENT", b.subject)
	}
	if !strings.Contains(b.body, worktreeRoot) {
		t.Errorf("body must name the agent's OWN boundary %q; got: %s", worktreeRoot, b.body)
	}
	if !strings.Contains(b.body, "/parent") {
		t.Errorf("body must name the offending path /parent; got: %s", b.body)
	}
	if ac := decodeAdditionalContext(t, stdout.String()); ac == "" {
		t.Error("expected a same-loop hookSpecificOutput.additionalContext nudge on stdout, got none")
	}
}

// TestContainment_EndToEnd_InBoundsSilent: a tool call inside the boundary stays silent on the
// wired path — zero beads, empty stdout.
func TestContainment_EndToEnd_InBoundsSilent(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	recs := installContainmentRecorder(t)

	var stdout bytes.Buffer
	p := writePayload("Write", filepath.Join(worktreeRoot, "notes.txt"), worktreeRoot)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("must exit 0: %v", err)
	}
	if len(*recs) != 0 {
		t.Fatalf("in-bounds must be silent; got %d beads", len(*recs))
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("in-bounds must emit no additionalContext; got: %s", stdout.String())
	}
}

// TestContainment_EndToEnd_BareFormulaPush: the UNCHANGED rapid-implement `git push origin main`
// (no cd, the L270 control command) is EXPECTED-by-effect and raises no violation (AC-2/AC-7).
func TestContainment_EndToEnd_BareFormulaPush(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	recs := installContainmentRecorder(t)

	var stdout bytes.Buffer
	if err := runContainmentCheckCore(&stdout, bashPayload("git push origin main", worktreeRoot)); err != nil {
		t.Fatalf("must exit 0: %v", err)
	}
	if len(*recs) != 0 {
		t.Fatalf("bare `git push origin main` (no cd) must not flag; got %d beads", len(*recs))
	}
}

// TestContainment_EndToEnd_PersistedDrift asserts the load-bearing persisted-drift case — the real
// incident shape — and NOT just the easy cd-in-one-call. The agent has ALREADY drifted to the
// out-of-bounds parent; a subsequent command runs FROM that drifted location with no containment
// prose and no literal cd in this call. `git -C .` carries a path resolvable against the drifted
// payload cwd, so the effective target resolves to the drifted (out-of-bounds) location and the
// guard still delivers exactly one corrective.
func TestContainment_EndToEnd_PersistedDrift(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	// The drifted location: the parent of the worktree boundary, which is out of bounds.
	driftedParent := filepath.Dir(worktreeRoot)
	recs := installContainmentRecorder(t)

	var stdout bytes.Buffer
	// Subsequent command from the drifted location; no literal cd in this call.
	p := bashPayload("git -C . status", driftedParent)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("must exit 0: %v", err)
	}
	if len(*recs) != 1 {
		t.Fatalf("persisted drift (subsequent command from a drifted location) must be detected; got %d beads", len(*recs))
	}
	b := (*recs)[0]
	if b.role != "solver" {
		t.Errorf("corrective must be self-addressed (AC-8); role = %q, want solver", b.role)
	}
	if b.subject != "WORKTREE_CONTAINMENT" {
		t.Errorf("subject = %q, want WORKTREE_CONTAINMENT", b.subject)
	}
	if !strings.Contains(b.body, driftedParent) {
		t.Errorf("body must name the drifted location %q; got: %s", driftedParent, b.body)
	}
}

// TestContainment_EndToEnd_PathlessDriftResidual pins the H1 residual (gaps.md GAP-18 item f): a
// truly path-less subsequent command from a drifted location yields an empty effective target and
// is therefore silent. This is the documented, accepted coverage gap — asserted here to lock the
// as-implemented behavior, NOT a defect to fix by editing containment.go (out of Phase 4 scope).
func TestContainment_EndToEnd_PathlessDriftResidual(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	driftedParent := filepath.Dir(worktreeRoot)
	recs := installContainmentRecorder(t)

	var stdout bytes.Buffer
	// Bare `git status` carries no -C and no cd → empty effective target → silent (residual f).
	if err := runContainmentCheckCore(&stdout, bashPayload("git status", driftedParent)); err != nil {
		t.Fatalf("must exit 0: %v", err)
	}
	if len(*recs) != 0 {
		t.Fatalf("path-less subsequent command is the documented H1 residual (silent); got %d beads", len(*recs))
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("path-less drift must be silent (residual f); got: %s", stdout.String())
	}
}
