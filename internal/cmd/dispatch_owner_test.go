package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// #548 Phase 1 — authorization substrate: dispatch_owner lifecycle, the tiered scoped-stop
// decision, the topology-robust callerDispatched read path, and the watchdog reservation.
// The internal/cmd TestMain scrubs ambient AF_ROLE/TMUX, so every case sets them explicitly.

// --- H-2: watchdog reservation (AC-4/AC-5) ---

// TestValidateAgentName_RejectsReservedWatchdog pins the H-2 interlock from the cmd package so
// AC-5's `go test ./internal/cmd/` invocation produces a hit. session.SessionName("watchdog")
// == "af-watchdog" aliases the control-plane watchdog session, so the name must be reserved.
func TestValidateAgentName_RejectsReservedWatchdog(t *testing.T) {
	if got := session.SessionName("watchdog"); got != "af-watchdog" {
		t.Fatalf("precondition: SessionName(watchdog) = %q, want af-watchdog", got)
	}
	err := config.ValidateAgentName("watchdog")
	if err == nil {
		t.Fatal("ValidateAgentName(watchdog) = nil, want reserved error")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error = %q, want it to contain 'reserved'", err.Error())
	}
}

// --- P3: dispatch_owner datum write/read helpers ---

// TestDispatchOwner_WriteReadRoundtrip proves the helpers round-trip and trim.
func TestDispatchOwner_WriteReadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	writeDispatchOwner(dir, "manager")
	if got := readDispatchOwner(dir); got != "manager" {
		t.Errorf("readDispatchOwner = %q, want manager", got)
	}
}

// TestDispatchOwner_OverwritesStaleOwner proves writeDispatchOwner has NO no-overwrite guard,
// so a --persistent re-dispatch replaces (not inherits) the previous owner — the
// persistFormulaCaller trap this split avoids.
func TestDispatchOwner_OverwritesStaleOwner(t *testing.T) {
	dir := t.TempDir()
	writeDispatchOwner(dir, "old-dispatcher")
	writeDispatchOwner(dir, "new-dispatcher")
	if got := readDispatchOwner(dir); got != "new-dispatcher" {
		t.Errorf("readDispatchOwner = %q, want new-dispatcher (overwrite, not no-overwrite)", got)
	}
}

// TestDispatchOwner_ReadMissingIsEmpty confirms a missing datum yields "" (fail-closed input).
func TestDispatchOwner_ReadMissingIsEmpty(t *testing.T) {
	if got := readDispatchOwner(t.TempDir()); got != "" {
		t.Errorf("readDispatchOwner(missing) = %q, want empty", got)
	}
}

// --- P3: dispatch_owner survives formula completion; formula_caller lifecycle unchanged ---

// TestFormulaCaller_LifecycleUnchanged pins Gap A's repair: cleanupRuntimeArtifacts still
// deletes formula_caller at formula completion, but dispatch_owner SURVIVES (it is NOT added
// to cleanupRuntimeArtifacts) — that survival IS AC-1.
func TestFormulaCaller_LifecycleUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "formula_caller", "manager")
	writeRuntimeFile(t, dir, "dispatch_owner", "manager")

	cleanupRuntimeArtifacts(dir)

	if _, err := os.Stat(filepath.Join(dir, ".runtime", "formula_caller")); !os.IsNotExist(err) {
		t.Error("formula_caller must still be removed by cleanupRuntimeArtifacts (lifecycle byte-identical)")
	}
	if _, err := os.Stat(filepath.Join(dir, ".runtime", "dispatch_owner")); err != nil {
		t.Error("dispatch_owner must SURVIVE formula completion (AC-1) — must NOT be in cleanupRuntimeArtifacts")
	}
}

// TestDispatchOwner_RemovedByReset confirms resetAgentState's wholesale .runtime removal takes
// dispatch_owner with it (no reset.go code change — AC per the IMPLREADME reset.go section).
func TestDispatchOwner_RemovedByReset(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	installMemStore(t)
	if err := os.MkdirAll(worktree.WorktreesDir(realDir), 0o755); err != nil {
		t.Fatal(err)
	}
	ownerPath := filepath.Join(config.AgentDir(realDir, "solver"), ".runtime", "dispatch_owner")
	writeRuntimeFile(t, config.AgentDir(realDir, "solver"), "dispatch_owner", "manager")

	var buf bytes.Buffer
	if err := resetAgentState(context.Background(), &buf, realDir, "solver", "test-reason"); err != nil {
		t.Fatalf("resetAgentState: %v", err)
	}
	if _, err := os.Stat(ownerPath); !os.IsNotExist(err) {
		t.Error("dispatch_owner must be removed by resetAgentState (os.RemoveAll of .runtime)")
	}
}

// --- P1: scopedStopAllowed three-tier fail-closed decision (returns the granting tier) ---

func TestScopedStopAllowed_Tiers(t *testing.T) {
	root, _ := setupDownGateFactory(t)
	cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		t.Fatalf("load agents: %v", err)
	}
	agents := cfg.Agents

	t.Run("self", func(t *testing.T) {
		t.Setenv("AF_ROLE", "solver")
		t.Setenv("TMUX", "")
		if tier, ok := scopedStopAllowed("solver", agents); !ok || tier != "self" {
			t.Errorf("scopedStopAllowed(solver) by solver = (%q,%v), want (self,true)", tier, ok)
		}
	})

	t.Run("dispatcher", func(t *testing.T) {
		t.Setenv("AF_ROLE", "solver")
		t.Setenv("TMUX", "")
		writeRuntimeFile(t, config.AgentDir(root, "sibling"), "dispatch_owner", "solver")
		if tier, ok := scopedStopAllowed("sibling", agents); !ok || tier != "dispatcher" {
			t.Errorf("scopedStopAllowed(sibling) by dispatcher solver = (%q,%v), want (dispatcher,true)", tier, ok)
		}
	})

	t.Run("manager", func(t *testing.T) {
		t.Setenv("AF_ROLE", "manager")
		t.Setenv("TMUX", "")
		if tier, ok := scopedStopAllowed("solver", agents); !ok || tier != "manager" {
			t.Errorf("scopedStopAllowed(solver) by interactive manager = (%q,%v), want (manager,true)", tier, ok)
		}
	})

	t.Run("refused_non_interactive_non_dispatcher", func(t *testing.T) {
		t.Setenv("AF_ROLE", "solver")
		t.Setenv("TMUX", "")
		// "loner" is a fresh autonomous specialist solver never dispatched (no dispatch_owner
		// on disk), so the dispatcher tier fails; solver is not interactive, so the manager
		// tier fails too. (Uses a name distinct from "sibling", whose dispatch_owner the
		// dispatcher subtest above wrote to the shared root.)
		fresh := map[string]config.AgentEntry{
			"solver": {Type: "autonomous", Formula: "factoryworker"},
			"loner":  {Type: "autonomous", Formula: "factoryworker"},
		}
		if tier, ok := scopedStopAllowed("loner", fresh); ok || tier != "" {
			t.Errorf("scopedStopAllowed(loner) by non-dispatcher solver = (%q,%v), want (\"\",false)", tier, ok)
		}
	})

	t.Run("refused_manager_cannot_stop_interactive_specialist", func(t *testing.T) {
		t.Setenv("AF_ROLE", "manager")
		t.Setenv("TMUX", "")
		custom := map[string]config.AgentEntry{
			"manager":  {Type: "interactive"},
			"reviewer": {Type: "interactive", Formula: "somewf"}, // interactive specialist excluded by C-4
		}
		if tier, ok := scopedStopAllowed("reviewer", custom); ok || tier != "" {
			t.Errorf("manager stopping interactive specialist = (%q,%v), want refused", tier, ok)
		}
	})

	t.Run("refused_manager_cannot_stop_non_specialist", func(t *testing.T) {
		t.Setenv("AF_ROLE", "manager")
		t.Setenv("TMUX", "")
		custom := map[string]config.AgentEntry{
			"manager": {Type: "interactive"},
			"worker":  {Type: "autonomous"}, // no formula → not a specialist
		}
		if tier, ok := scopedStopAllowed("worker", custom); ok || tier != "" {
			t.Errorf("manager stopping non-specialist autonomous worker = (%q,%v), want refused", tier, ok)
		}
	})
}

// --- P4: callerDispatched topology-robust read path ---

// TestCallerDispatched_DispatchOwnerPrimary proves the primary dispatch_owner read authorizes
// a release even after formula completion deleted formula_caller (Gap A repair).
func TestCallerDispatched_DispatchOwnerPrimary(t *testing.T) {
	root, _ := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	targetDir := config.AgentDir(root, "solver")

	writeRuntimeFile(t, targetDir, "dispatch_owner", "manager")
	if !callerDispatched("solver") {
		t.Error("callerDispatched(solver) = false, want true when dispatch_owner matches caller")
	}

	writeRuntimeFile(t, targetDir, "dispatch_owner", "someoneelse")
	if callerDispatched("solver") {
		t.Error("callerDispatched(solver) = true, want false when dispatch_owner differs from caller")
	}
}

// TestCallerDispatched_EnclosingRootFallback proves topology row 3: a caller whose local root
// cannot see the target falls back to the enclosing factory root to resolve provenance.
func TestCallerDispatched_EnclosingRootFallback(t *testing.T) {
	outer := t.TempDir()
	writeAFFile(t, outer, "factory.json", `{"type":"factory","version":1,"name":"outer"}`)
	writeRuntimeFile(t, config.AgentDir(outer, "solver"), "dispatch_owner", "manager")

	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAFFile(t, inner, "factory.json", `{"type":"factory","version":1,"name":"inner"}`)

	t.Chdir(inner)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")

	if !callerDispatched("solver") {
		t.Error("callerDispatched(solver) from inner root = false, want true via enclosing-root fallback")
	}
}

// TestCallerDispatched_TopologyMatrix groups the callerDispatched decision over
// dispatch_owner-registered targets on a single main-root factory: the owner match authorizes,
// an owner mismatch refuses, and a target with no registered owner fails closed. It complements
// TestCallerDispatched_DispatchOwnerPrimary (inline match/mismatch) by packaging the matrix and
// adding the fail-closed missing-owner input cell. Each row uses a distinct target so the shared
// factory root does not leak provenance between subtests.
func TestCallerDispatched_TopologyMatrix(t *testing.T) {
	root, _ := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "solver")
	t.Setenv("TMUX", "")

	matrix := []struct {
		name   string
		target string
		owner  string // "" => leave dispatch_owner unregistered on this target
		want   bool
	}{
		{"owner_matches_caller", "sibling", "solver", true},
		{"owner_mismatches_caller", "manager", "someoneelse", false},
		{"owner_unregistered_fail_closed", "undispatched", "", false},
	}
	for _, tc := range matrix {
		t.Run(tc.name, func(t *testing.T) {
			if tc.owner != "" {
				writeRuntimeFile(t, config.AgentDir(root, tc.target), "dispatch_owner", tc.owner)
			}
			if got := callerDispatched(tc.target); got != tc.want {
				t.Errorf("callerDispatched(%q) with dispatch_owner=%q = %v, want %v", tc.target, tc.owner, got, tc.want)
			}
		})
	}
}

// TestCallerDispatched_EnclosingRootFallback_FailClosed is the negative of
// TestCallerDispatched_EnclosingRootFallback: when neither the inner (local) root nor the
// enclosing root registers a matching owner, the topology-robust read must fail closed
// (refuse), never fall through to allow. Both a no-owner-anywhere case and a
// mismatched-owner-at-the-enclosing-root case are covered so the fallback is proven to READ
// and REJECT, not merely to short-circuit on absence.
func TestCallerDispatched_EnclosingRootFallback_FailClosed(t *testing.T) {
	build := func(t *testing.T, enclosingOwner string) {
		outer := t.TempDir()
		writeAFFile(t, outer, "factory.json", `{"type":"factory","version":1,"name":"outer"}`)
		if enclosingOwner != "" {
			writeRuntimeFile(t, config.AgentDir(outer, "solver"), "dispatch_owner", enclosingOwner)
		}
		inner := filepath.Join(outer, "inner")
		if err := os.MkdirAll(inner, 0o755); err != nil {
			t.Fatal(err)
		}
		writeAFFile(t, inner, "factory.json", `{"type":"factory","version":1,"name":"inner"}`)
		t.Chdir(inner)
		t.Setenv("AF_ROLE", "manager")
		t.Setenv("TMUX", "")
	}

	t.Run("no_owner_at_either_root", func(t *testing.T) {
		build(t, "")
		if callerDispatched("solver") {
			t.Error("callerDispatched(solver) with no owner at inner or enclosing root = true, want false (fail closed)")
		}
	})

	t.Run("mismatched_owner_at_enclosing_root", func(t *testing.T) {
		build(t, "someoneelse")
		if callerDispatched("solver") {
			t.Error("callerDispatched(solver) when enclosing-root owner != caller = true, want false")
		}
	})
}
