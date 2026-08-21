package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PR #630, review round 2 (2026-08-18). These guards pin the two actionable threads of this round.
// ITEM-2 (are the role templates regenerated from source?) is reply-only and already proven by the
// existing internal/templates drift tests, so it gets no new guard here.

// TestFableIncrPr630R2_ITEM1_ADR022DocumentsMemoryUsageBoundary pins ITEM-1
// (thread PRRT_kwDORt0n_M6aRzVi): the reviewer asked for an ADR describing WHEN to use the memory
// vault. The vault holds durable, cross-task learnings; ephemeral per-task/step/handoff state stays
// in mail and checkpoints. This asserts the ADR records that boundary (substance, not just a
// filename) and that the ADR index lists it. It is ADR-022-specific, so it does not fail on the
// pre-existing, unrelated ADR-021 index gap.
func TestFableIncrPr630R2_ITEM1_ADR022DocumentsMemoryUsageBoundary(t *testing.T) {
	adrDir := filepath.Join("..", "..", "docs", "architecture", "adrs")

	matches, err := filepath.Glob(filepath.Join(adrDir, "ADR-022-*.md"))
	if err != nil {
		t.Fatalf("glob ADR-022: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one docs/architecture/adrs/ADR-022-*.md (the WHEN-to-use-memory ADR), got %d: %v", len(matches), matches)
	}

	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	body := strings.ToLower(string(raw))

	// The decision's substance: the durable-vs-ephemeral boundary, naming both the vault side
	// (durable learnings) and the ephemeral side (mail / checkpoints).
	for _, want := range []string{"durable", "ephemeral", "mail", "checkpoint", "learning"} {
		if !strings.Contains(body, want) {
			t.Errorf("ADR-022 must document the memory-usage boundary; missing %q in %s", want, matches[0])
		}
	}
	if !strings.Contains(body, "**status:** accepted") && !strings.Contains(body, "status: accepted") {
		t.Errorf("ADR-022 must carry an Accepted status line (it records a code-live decision)")
	}

	readme, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
	if err != nil {
		t.Fatalf("read adrs/README.md: %v", err)
	}
	if !strings.Contains(string(readme), "](ADR-022-") {
		t.Errorf("docs/architecture/adrs/README.md Index must list ADR-022 (a `](ADR-022-...)` link row)")
	}
}
