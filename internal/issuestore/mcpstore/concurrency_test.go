//go:build integration

package mcpstore_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// TestConcurrency exercises mcpstore with 8 goroutines x 40 ops (320 ops
// total) using a ~30/25/15/30 Create/Patch/Close/Ready distribution. The
// 8-goroutine width matches the server's SQLAlchemy pool_size+max_overflow
// so both the pool and overflow paths are exercised. Every Create and Patch
// is read back via Get to verify state. Dependency edges created before
// dispatch are verified after via Ready (dependents excluded while blockers
// are open) and Render (blocker IDs appear in the rendered output). The
// whole body runs 3 times against a fresh factory root each iteration for
// flake detection. Covers AC-11 of the #80 bdstore→mcpstore swap.
func TestConcurrency(t *testing.T) {
	requirePython3WithDeps(t)

	const runs = 3
	for run := 0; run < runs; run++ {
		runConcurrencyOnce(t, run)
		if t.Failed() {
			return
		}
	}
}

func runConcurrencyOnce(t *testing.T, run int) {
	const (
		goroutines      = 8
		opsPerGoroutine = 40

		// 12 + 10 + 6 + 12 == 40, hitting AC-11's ~30/25/15/30 mix.
		createOps = 12
		patchOps  = 10
		closeOps  = 6
		readyOps  = 12
	)

	root := newFactoryRoot(t)
	// Terminate this iteration's server before the next iteration spawns its
	// own — otherwise one Python subprocess per iteration survives past test end.
	defer terminateServer(root)

	store, err := mcpstore.New(root, "bench")
	if err != nil {
		t.Fatalf("run %d: mcpstore.New: %v", run, err)
	}

	ctx := context.Background()

	// First RPC pays the ~500ms-1s Python spawn cost. Do it now so the
	// goroutine dispatch sees steady-state server behavior.
	if _, err := store.List(ctx, issuestore.Filter{IncludeAllAgents: true}); err != nil {
		t.Fatalf("run %d: warmup List: %v", run, err)
	}

	// Per-goroutine pools sized to patchOps + closeOps. Disjoint ID pools
	// guarantee no two goroutines write to the same issue, so the
	// read-back-verify assertion can't race a concurrent write.
	patchPool := make([][]string, goroutines)
	closePool := make([][]string, goroutines)
	for g := 0; g < goroutines; g++ {
		patchPool[g] = make([]string, patchOps)
		for i := 0; i < patchOps; i++ {
			iss, err := store.Create(ctx, issuestore.CreateParams{
				Title:    fmt.Sprintf("seed-patch-g%d-%d", g, i),
				Type:     issuestore.TypeTask,
				Priority: issuestore.PriorityNormal,
			})
			if err != nil {
				t.Fatalf("run %d: seed patch g=%d i=%d: %v", run, g, i, err)
			}
			patchPool[g][i] = iss.ID
		}
		closePool[g] = make([]string, closeOps)
		for i := 0; i < closeOps; i++ {
			iss, err := store.Create(ctx, issuestore.CreateParams{
				Title:    fmt.Sprintf("seed-close-g%d-%d", g, i),
				Type:     issuestore.TypeTask,
				Priority: issuestore.PriorityNormal,
			})
			if err != nil {
				t.Fatalf("run %d: seed close g=%d i=%d: %v", run, g, i, err)
			}
			closePool[g][i] = iss.ID
		}
	}

	// Pre-create 5 dependency edges. Each edge pairs a dependent (from) with
	// a blocker (to); the blocker stays open for the duration of this run,
	// so Ready must exclude the dependent and Render(dependent) must mention
	// the blocker ID. These IDs are disjoint from the per-goroutine pools so
	// no goroutine touches them during dispatch.
	const depEdges = 5
	depFrom := make([]string, depEdges)
	depTo := make([]string, depEdges)
	for i := 0; i < depEdges; i++ {
		from, err := store.Create(ctx, issuestore.CreateParams{
			Title:    fmt.Sprintf("dep-from-%d", i),
			Type:     issuestore.TypeTask,
			Priority: issuestore.PriorityNormal,
		})
		if err != nil {
			t.Fatalf("run %d: seed dep-from %d: %v", run, i, err)
		}
		to, err := store.Create(ctx, issuestore.CreateParams{
			Title:    fmt.Sprintf("dep-to-%d", i),
			Type:     issuestore.TypeTask,
			Priority: issuestore.PriorityNormal,
		})
		if err != nil {
			t.Fatalf("run %d: seed dep-to %d: %v", run, i, err)
		}
		if err := store.DepAdd(ctx, from.ID, to.ID); err != nil {
			t.Fatalf("run %d: seed DepAdd %d: %v", run, i, err)
		}
		depFrom[i] = from.ID
		depTo[i] = to.ID
	}

	// Dispatch: each goroutine performs opsPerGoroutine ops in a fixed
	// ordering so op-index partitioning selects the right method:
	//   [0, createOps)                     → Create (+ Get read-back)
	//   [createOps, +patchOps)             → Patch  (+ Get read-back)
	//   [+patchOps, +closeOps)             → Close
	//   [+closeOps, +readyOps)             → Ready
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				switch {
				case i < createOps:
					assignee := fmt.Sprintf("g%d", g)
					want := fmt.Sprintf("op-create-g%d-%d", g, i)
					iss, err := store.Create(ctx, issuestore.CreateParams{
						Title:    want,
						Type:     issuestore.TypeTask,
						Priority: issuestore.PriorityNormal,
						Assignee: assignee,
					})
					if err != nil {
						t.Errorf("run %d g%d op %d: Create: %v", run, g, i, err)
						continue
					}
					got, err := store.Get(ctx, iss.ID)
					if err != nil {
						t.Errorf("run %d g%d op %d: read-back Get(%s): %v", run, g, i, iss.ID, err)
						continue
					}
					if got.Title != want || got.Type != issuestore.TypeTask ||
						got.Priority != issuestore.PriorityNormal || got.Assignee != assignee {
						t.Errorf("run %d g%d op %d: Create read-back mismatch: title=%q type=%q pri=%v asgn=%q",
							run, g, i, got.Title, got.Type, got.Priority, got.Assignee)
					}
				case i < createOps+patchOps:
					pi := i - createOps
					id := patchPool[g][pi]
					notes := fmt.Sprintf("notes-g%d-%d", g, pi)
					if err := store.Patch(ctx, id, issuestore.Patch{Notes: &notes}); err != nil {
						t.Errorf("run %d g%d op %d: Patch(%s): %v", run, g, i, id, err)
						continue
					}
					got, err := store.Get(ctx, id)
					if err != nil {
						t.Errorf("run %d g%d op %d: read-back Get(%s): %v", run, g, i, id, err)
						continue
					}
					if got.Notes != notes {
						t.Errorf("run %d g%d op %d: Patch read-back Notes=%q want %q", run, g, i, got.Notes, notes)
					}
				case i < createOps+patchOps+closeOps:
					ci := i - createOps - patchOps
					id := closePool[g][ci]
					if err := store.Close(ctx, id, "bench"); err != nil {
						t.Errorf("run %d g%d op %d: Close(%s): %v", run, g, i, id, err)
					}
				default:
					if _, err := store.Ready(ctx, issuestore.Filter{IncludeAllAgents: true}); err != nil {
						t.Errorf("run %d g%d op %d: Ready: %v", run, g, i, err)
					}
				}
			}
		}(g)
	}
	wg.Wait()

	if t.Failed() {
		return
	}

	// Dep-graph consistency: every dependent must be absent from Ready.Steps
	// (its blocker is still open), and Render(dependent) must mention the
	// blocker ID. Use IncludeAllAgents=true so actor scoping doesn't hide
	// the dependent for an orthogonal reason.
	readyResult, err := store.Ready(ctx, issuestore.Filter{IncludeAllAgents: true})
	if err != nil {
		t.Fatalf("run %d: final Ready: %v", run, err)
	}
	inReady := make(map[string]bool, len(readyResult.Steps))
	for _, s := range readyResult.Steps {
		inReady[s.ID] = true
	}
	for i, fromID := range depFrom {
		if inReady[fromID] {
			t.Errorf("run %d: dep-from %s (edge %d, blocker %s still open) appears in Ready.Steps",
				run, fromID, i, depTo[i])
		}
		rendered, err := store.Render(ctx, fromID)
		if err != nil {
			t.Errorf("run %d: Render(%s): %v", run, fromID, err)
			continue
		}
		if !strings.Contains(rendered, depTo[i]) {
			t.Errorf("run %d: Render(%s) does not mention blocker %s; got:\n%s",
				run, fromID, depTo[i], rendered)
		}
	}
}
