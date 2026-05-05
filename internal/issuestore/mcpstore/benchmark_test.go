//go:build integration

package mcpstore_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

const benchIters = 50

// phase0Baseline pins the bdstore p50/p95 numbers from commit 96e5d7e
// (Phase 0, PR #114). The file that produced these numbers was deleted in
// Phase 7 along with bdstore/; the commit message body is the only surviving
// record. AC-12's "no regression vs bdstore baseline" clause asserts that
// mcpstore's measured p50/p95 are <= these values for every one of the 9
// Store methods.
var phase0Baseline = map[string]struct {
	p50 time.Duration
	p95 time.Duration
}{
	"Get":        {33 * time.Millisecond, 50 * time.Millisecond},
	"List":       {34 * time.Millisecond, 50 * time.Millisecond},
	"Ready":      {33 * time.Millisecond, 50 * time.Millisecond},
	"Create":     {76 * time.Millisecond, 95 * time.Millisecond},
	"Patch":      {41 * time.Millisecond, 55 * time.Millisecond},
	"Close":      {41 * time.Millisecond, 55 * time.Millisecond},
	"DepAdd":     {41 * time.Millisecond, 55 * time.Millisecond},
	"Render":     {34 * time.Millisecond, 50 * time.Millisecond},
	"RenderList": {34 * time.Millisecond, 50 * time.Millisecond},
}

// TestLatencyTargets seeds 100 issues into a fresh mcpstore + Python server
// (mirroring the Phase 0 bdstore fixture exactly) and measures p50/p95
// latency for all 9 issuestore.Store methods over 50 iterations each. It
// asserts:
//
//   - AC-12 hard targets: p50<20ms Get/Create, p50<50ms Ready. The p95
//     targets (p95<50ms Get/Create, p95<100ms Ready) are a design-plan
//     extension of AC-12 — AC-12 itself specifies only p50 there. See the
//     comment on the hard-target block below.
//   - No regression vs phase0Baseline for any of the 9 methods (measured
//     p50 <= baseline p50 and measured p95 <= baseline p95).
//
// Output lines are emitted via t.Logf in a PR-parseable format so the PR
// body can transcribe them verbatim.
func TestLatencyTargets(t *testing.T) {
	requirePython3WithDeps(t)

	root := newFactoryRoot(t)
	t.Cleanup(func() { terminateServer(root) })

	store, err := mcpstore.New(root, "bench")
	if err != nil {
		t.Fatalf("mcpstore.New: %v", err)
	}

	ctx := context.Background()

	// --- Seed phase (not timed) ---
	// Mirror Phase 0 exactly so this is a like-for-like comparison against
	// the bdstore baseline. Phase 0 rotated across 4 types (Task, Bug,
	// Feature, Task) because bd v0.49.1 rejected TypeGate; we keep the
	// same rotation so the seed distributions match, even though the
	// Python server may accept TypeGate.
	types := []issuestore.IssueType{
		issuestore.TypeTask, issuestore.TypeBug,
		issuestore.TypeFeature, issuestore.TypeTask,
	}
	priorities := []issuestore.Priority{
		issuestore.PriorityUrgent, issuestore.PriorityHigh,
		issuestore.PriorityNormal, issuestore.PriorityLow,
	}

	var epicIDs []string
	for i := 0; i < 5; i++ {
		iss, err := store.Create(ctx, issuestore.CreateParams{
			Title:    fmt.Sprintf("epic-%d", i),
			Type:     issuestore.TypeEpic,
			Priority: priorities[i%len(priorities)],
		})
		if err != nil {
			t.Fatalf("seed epic %d: %v", i, err)
		}
		epicIDs = append(epicIDs, iss.ID)
	}

	var allIDs []string
	allIDs = append(allIDs, epicIDs...)
	for i := 0; i < 95; i++ {
		params := issuestore.CreateParams{
			Title:    fmt.Sprintf("issue-%d", i),
			Type:     types[i%len(types)],
			Priority: priorities[i%len(priorities)],
		}
		if i < 20 {
			params.Parent = epicIDs[i%len(epicIDs)]
			params.Assignee = "bench"
		}
		iss, err := store.Create(ctx, params)
		if err != nil {
			t.Fatalf("seed issue %d: %v", i, err)
		}
		allIDs = append(allIDs, iss.ID)
	}

	// Status transitions mirror Phase 0: 10 in_progress, 10 hooked, 5
	// pinned, 5 closed. SetStatus is a test-only method on *MCPStore (not
	// on the Store interface) — Phase 0's bdstore.Store also exposed it as
	// a direct method, so this is the analogous call.
	for i := 5; i < 15; i++ {
		if err := store.SetStatus(ctx, allIDs[i], issuestore.StatusInProgress); err != nil {
			t.Fatalf("set in_progress %s: %v", allIDs[i], err)
		}
	}
	for i := 15; i < 25; i++ {
		if err := store.SetStatus(ctx, allIDs[i], issuestore.StatusHooked); err != nil {
			t.Fatalf("set hooked %s: %v", allIDs[i], err)
		}
	}
	for i := 25; i < 30; i++ {
		if err := store.SetStatus(ctx, allIDs[i], issuestore.StatusPinned); err != nil {
			t.Fatalf("set pinned %s: %v", allIDs[i], err)
		}
	}
	for i := 30; i < 35; i++ {
		if err := store.Close(ctx, allIDs[i], ""); err != nil {
			t.Fatalf("close %s: %v", allIDs[i], err)
		}
	}

	// 12 dep edges over non-epic issues starting at index 35 (24 IDs
	// consumed: 35..58), matching Phase 0's pair layout.
	for i := 0; i < 12; i++ {
		from := allIDs[35+i*2]
		to := allIDs[35+i*2+1]
		if err := store.DepAdd(ctx, from, to); err != nil {
			t.Fatalf("dep-add %s->%s: %v", from, to, err)
		}
	}

	// Close and DepAdd are one-shot operations — each timed iteration needs
	// a fresh target. Pre-allocate 50 close-targets and 100 dep-targets (50
	// from-IDs + 50 to-IDs) so the measurement phase is pure method calls
	// with no seeding cost.
	closeIDs := make([]string, benchIters)
	for i := 0; i < benchIters; i++ {
		iss, err := store.Create(ctx, issuestore.CreateParams{
			Title:    fmt.Sprintf("close-target-%d", i),
			Type:     issuestore.TypeTask,
			Priority: issuestore.PriorityNormal,
		})
		if err != nil {
			t.Fatalf("seed close-target %d: %v", i, err)
		}
		closeIDs[i] = iss.ID
	}

	depFromIDs := make([]string, 0, benchIters)
	depToIDs := make([]string, 0, benchIters)
	for i := 0; i < benchIters*2; i++ {
		iss, err := store.Create(ctx, issuestore.CreateParams{
			Title:    fmt.Sprintf("dep-target-%d", i),
			Type:     issuestore.TypeTask,
			Priority: issuestore.PriorityNormal,
		})
		if err != nil {
			t.Fatalf("seed dep-target %d: %v", i, err)
		}
		if i%2 == 0 {
			depFromIDs = append(depFromIDs, iss.ID)
		} else {
			depToIDs = append(depToIDs, iss.ID)
		}
	}

	t.Logf("seed complete: %d base + %d close-targets + %d dep-targets",
		len(allIDs), len(closeIDs), benchIters*2)

	// Warmup: pay the ~500ms-1s Python spawn cost before any timed call.
	// Without this the first Get measurement includes spawn and blows the
	// p50 budget.
	if _, err := store.List(ctx, issuestore.Filter{IncludeAllAgents: true}); err != nil {
		t.Fatalf("warmup List: %v", err)
	}

	// --- Measurement phase ---
	results := make(map[string]struct{ p50, p95 time.Duration }, len(phase0Baseline))

	measure := func(t *testing.T, name string, fn func(i int) error) {
		t.Helper()
		durations := make([]time.Duration, benchIters)
		for i := 0; i < benchIters; i++ {
			start := time.Now()
			if err := fn(i); err != nil {
				t.Fatalf("%s iteration %d: %v", name, i, err)
			}
			durations[i] = time.Since(start)
		}
		sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })
		p50 := percentile(durations, 0.50)
		p95 := percentile(durations, 0.95)
		results[name] = struct{ p50, p95 time.Duration }{p50, p95}
		t.Logf("%-12s  p50=%v  p95=%v", name, p50, p95)
	}

	t.Run("Get", func(t *testing.T) {
		measure(t, "Get", func(i int) error {
			_, err := store.Get(ctx, allIDs[i%len(allIDs)])
			return err
		})
	})

	t.Run("List", func(t *testing.T) {
		measure(t, "List", func(i int) error {
			_, err := store.List(ctx, issuestore.Filter{IncludeAllAgents: true})
			return err
		})
	})

	t.Run("Ready", func(t *testing.T) {
		measure(t, "Ready", func(i int) error {
			_, err := store.Ready(ctx, issuestore.Filter{})
			return err
		})
	})

	t.Run("Create", func(t *testing.T) {
		measure(t, "Create", func(i int) error {
			_, err := store.Create(ctx, issuestore.CreateParams{
				Title:    fmt.Sprintf("bench-create-%d", i),
				Type:     issuestore.TypeTask,
				Priority: issuestore.PriorityNormal,
			})
			return err
		})
	})

	t.Run("Patch", func(t *testing.T) {
		notes := "benchmark patch notes"
		measure(t, "Patch", func(i int) error {
			return store.Patch(ctx, allIDs[i%len(allIDs)], issuestore.Patch{Notes: &notes})
		})
	})

	t.Run("Close", func(t *testing.T) {
		measure(t, "Close", func(i int) error {
			return store.Close(ctx, closeIDs[i], "")
		})
	})

	t.Run("DepAdd", func(t *testing.T) {
		measure(t, "DepAdd", func(i int) error {
			return store.DepAdd(ctx, depFromIDs[i], depToIDs[i])
		})
	})

	t.Run("Render", func(t *testing.T) {
		measure(t, "Render", func(i int) error {
			_, err := store.Render(ctx, allIDs[i%len(allIDs)])
			return err
		})
	})

	t.Run("RenderList", func(t *testing.T) {
		measure(t, "RenderList", func(i int) error {
			_, err := store.RenderList(ctx, issuestore.Filter{IncludeAllAgents: true})
			return err
		})
	})

	// --- AC-12 hard targets ---
	// AC-12 (source.md L58-59) specifies p50<20ms for Get/Create and
	// p50<50ms for Ready. The p95 numbers below (p95<50ms Get/Create,
	// p95<100ms Ready) are a design-plan extension of AC-12 — AC-12 itself
	// specifies only p50 there; the p95 limits are a tail-latency floor
	// pulled from the outline. The authoritative p95 check for all 9
	// methods is the baseline-regression assertion further down.
	type hard struct {
		name     string
		p50Limit time.Duration
		p95Limit time.Duration
	}
	hardTargets := []hard{
		{"Get", 20 * time.Millisecond, 50 * time.Millisecond},
		{"Create", 20 * time.Millisecond, 50 * time.Millisecond},
		{"Ready", 50 * time.Millisecond, 100 * time.Millisecond},
	}
	for _, h := range hardTargets {
		got, ok := results[h.name]
		if !ok {
			t.Errorf("AC-12 hard target: no measurement recorded for %s", h.name)
			continue
		}
		if got.p50 > h.p50Limit {
			t.Errorf("AC-12 hard target: %s p50=%v exceeds limit %v", h.name, got.p50, h.p50Limit)
		}
		if got.p95 > h.p95Limit {
			t.Errorf("AC-12 hard target (plan-extension): %s p95=%v exceeds limit %v", h.name, got.p95, h.p95Limit)
		}
	}

	// --- No-regression vs Phase 0 bdstore baseline (AC-12) ---
	for name, baseline := range phase0Baseline {
		got, ok := results[name]
		if !ok {
			t.Errorf("baseline regression: no measurement recorded for %s", name)
			continue
		}
		if got.p50 > baseline.p50 {
			t.Errorf("baseline regression: %s p50=%v exceeds Phase 0 baseline %v",
				name, got.p50, baseline.p50)
		}
		if got.p95 > baseline.p95 {
			t.Errorf("baseline regression: %s p95=%v exceeds Phase 0 baseline %v",
				name, got.p95, baseline.p95)
		}
	}
}

// percentile returns the sorted[int(len*pct)] entry, clamping to the last
// element when the index would overflow. Matches 96e5d7e:benchmark_test.go
// lines 244-252 exactly so this percentile is the same percentile Phase 0
// used.
func percentile(sorted []time.Duration, pct float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)) * pct)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
