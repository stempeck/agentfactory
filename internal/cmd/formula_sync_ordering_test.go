package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestQuickstartSyncsBeforeBuild(t *testing.T) {
	data, err := os.ReadFile("../../quickstart.sh")
	if err != nil {
		t.Fatalf("read quickstart.sh: %v", err)
	}
	body := string(data)

	syncIdx := strings.Index(body, "make sync-formulas")
	if syncIdx == -1 {
		t.Fatal("quickstart.sh does not contain 'make sync-formulas'")
	}

	buildIdx := strings.Index(body, "make build")
	if buildIdx == -1 {
		t.Fatal("quickstart.sh does not contain 'make build'")
	}

	if syncIdx >= buildIdx {
		t.Errorf("quickstart.sh: 'make sync-formulas' (offset %d) must appear before 'make build' (offset %d)", syncIdx, buildIdx)
	}
}

func TestAgentGenAllSyncsBeforeLoop(t *testing.T) {
	data, err := os.ReadFile("../../agent-gen-all.sh")
	if err != nil {
		t.Fatalf("read agent-gen-all.sh: %v", err)
	}
	body := string(data)

	syncIdx := strings.Index(body, "syncing formulas from source")
	if syncIdx == -1 {
		t.Fatal("agent-gen-all.sh does not contain a formula sync block ('syncing formulas from source')")
	}

	regenIdx := strings.Index(body, "af formula agent-gen")
	if regenIdx == -1 {
		t.Fatal("agent-gen-all.sh does not contain the formula regeneration call ('af formula agent-gen')")
	}

	if syncIdx >= regenIdx {
		t.Errorf("agent-gen-all.sh: sync block (offset %d) must appear before regeneration (offset %d)", syncIdx, regenIdx)
	}

	if !strings.Contains(body, "removed orphan:") {
		t.Error("agent-gen-all.sh sync block does not remove orphan formulas (missing orphan removal loop)")
	}
}

func TestSyncFormulasIncrementalCopy(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	body := string(data)

	targetIdx := strings.Index(body, "sync-formulas:")
	if targetIdx == -1 {
		t.Fatal("Makefile does not contain 'sync-formulas:' target")
	}

	targetBody := body[targetIdx:]
	nextTarget := strings.Index(targetBody[1:], "\n\n")
	if nextTarget > 0 {
		targetBody = targetBody[:nextTarget+1]
	}

	if !strings.Contains(targetBody, "basename") {
		t.Error("sync-formulas target does not use incremental per-file copy (missing 'basename')")
	}
}

// TestAgentGenAllRegenLoopDoesNotDeleteExistingAgent pins issue #527's fix at the
// shell-script layer: agent-gen-all.sh:134-153's per-formula regen loop must not
// call `af formula agent-gen ... --delete` before regenerating a formula that
// still exists in FORMULA_DIR. The loop's domain is always a still-existing
// formula (`for f in "$FORMULA_DIR"/*.formula.toml`), so that delete call never
// performed genuine orphan cleanup — it only ever destroyed the on-disk
// AgentEntry the immediately-following regen's operator-field merge
// (formula.go:245-249) depends on, silently wiping continuous_improvement,
// model, sparse_paths, base_url, and auth_token on every `af install --agents`
// redeploy. See https://github.com/stempeck/agentfactory-pro/issues/527.
//
// This is a source-content assertion, not a behavioral end-to-end run, because
// the behavioral equivalent (TestAgentGenAllRegenLoop_PreservesOperatorFields,
// internal/cmd/agent_gen_redeploy_integration_test.go, //go:build integration)
// requires a real `af` subprocess and is refused by the CI-only containment guard
// (internal/testsupport/tmuxisolation/ciguard.go) when run from inside a live
// agent factory worktree, as this development session is. This test is the
// locally-runnable half of the pin; the integration test is the CI-runnable half.
func TestAgentGenAllRegenLoopDoesNotDeleteExistingAgent(t *testing.T) {
	data, err := os.ReadFile("../../agent-gen-all.sh")
	if err != nil {
		t.Fatalf("read agent-gen-all.sh: %v", err)
	}
	body := string(data)

	const startMarker = "# --- Regenerate each formula"
	const endMarker = "# --- Rebuild af"
	startIdx := strings.Index(body, startMarker)
	if startIdx == -1 {
		t.Fatal("agent-gen-all.sh missing regen-loop start marker")
	}
	endIdx := strings.Index(body[startIdx:], endMarker)
	if endIdx == -1 {
		t.Fatal("agent-gen-all.sh missing regen-loop end marker")
	}
	loopBlock := body[startIdx : startIdx+endIdx]

	// A reintroduced field-wipe is caught regardless of flag order (e.g. a
	// `--delete` moved after `--af-src`), while the loop's own comments — which
	// legitimately mention the flag by name — are ignored. See
	// regenLoopReintroducesDelete.
	if regenLoopReintroducesDelete(loopBlock) {
		t.Errorf("agent-gen-all.sh's per-formula regen loop still calls --delete before regenerating a still-existing formula — this reintroduces issue #527's operator-field wipe on every redeploy:\n%s", loopBlock)
	}
}

// regenDeletePattern matches an `agent-gen` invocation that also carries a
// `--delete`, in any flag order (the `.*` spans intervening flags but not a
// newline — Go's `.` does not match `\n` — so a match is always same-command).
var regenDeletePattern = regexp.MustCompile(`agent-gen\b.*--delete`)

// regenLoopReintroducesDelete reports whether the regen loop block reintroduces
// issue #527's operator-field wipe: a `--delete` on the same `af formula
// agent-gen` command as the regeneration. Matching is order-independent (so a
// `--delete` moved after `--af-src` is still caught, not just the exact
// historical `agent-gen "$name" --delete` form) and comment lines are skipped,
// so the block's own documentation of the standalone `--delete` — which is
// unaffected by the loop — never trips the guard.
func regenLoopReintroducesDelete(loopBlock string) bool {
	for _, line := range strings.Split(loopBlock, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if regenDeletePattern.MatchString(line) {
			return true
		}
	}
	return false
}

func TestRegenLoopReintroducesDelete_Detects(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  bool
	}{
		{"correct-current-script", `    if af formula agent-gen "$name" --af-src "$AF_SRC"; then`, false},
		{"original-bug-delete-after-name", `    if ! af formula agent-gen "$name" --delete --af-src "$AF_SRC" 2>&1; then`, true},
		{"reordered-delete-after-afsrc", `    if ! af formula agent-gen "$name" --af-src "$AF_SRC" --delete 2>&1; then`, true},
		{"reordered-delete-before-name", `    af formula agent-gen --delete "$name" --af-src "$AF_SRC"`, true},
		{"comment-only-mention", "# standalone `af formula agent-gen <name> --delete`, unaffected by this loop", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := regenLoopReintroducesDelete(tc.block); got != tc.want {
				t.Errorf("regenLoopReintroducesDelete(%q) = %v, want %v", tc.block, got, tc.want)
			}
		})
	}
}
