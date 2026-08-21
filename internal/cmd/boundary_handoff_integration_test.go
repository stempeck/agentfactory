//go:build integration

package cmd

// #622 C5, AC-2/AC-3: the integration-lane proof that a cooperative step-boundary handoff reaches
// the REAL recycle funnel and leaves the durable record AC-3 greps for.
//
// What the unit lane cannot show. TestBoundaryMatrix (boundary_handoff_test.go:210) substitutes
// boundaryHandoffExec wholesale, so it stops one call short of respawnSession — which means the C-1
// anchoring for this trigger (the K6 recovery-log write and the K19 fence arm at helpers.go:197)
// has no firing coverage there at all. It also runs in the default build, where guardMode is true
// (tmux/guard_integration.go) and every tmux op is a fail-closed no-op, so "the funnel would have
// driven tmux correctly" is a claim it is structurally unable to make. This file drives the real
// funnel with the pane hop, and only the pane hop, recorded.
//
// THE SUBSTITUTION BOUNDARY, declared rather than hidden (the idiom is
// recovery_e2e_integration_test.go:18-39):
//
//	SUBSTITUTED — the final `tmux respawn-pane` hop, and nothing else. boundaryHandoffExec is
//	wrapped, not replaced: the wrapper sets RespawnOptions.Tx to a recording mockTmux and then
//	calls the ORIGINAL var, so the checkpoint leg, the mail leg and respawnSession all still run.
//	runBoundaryHandoff builds its RespawnOptions with no Tx (done.go:346-354), so respawnSession
//	would otherwise fall through to tmux.NewTmux() (helpers.go:186-188); under -tags=integration
//	that is a LIVE respawn-pane carrying a real `claude … 'af prime'` command line at whatever pane
//	TMUX_PANE names. Recording it is what proves the command we would have run is the right one.
//
//	NOT SUBSTITUTED — everything else: the real stepContextReading session-keyed snapshot read, the
//	real shouldBoundaryHandoff decision, the real detectRole, the real captureCheckpointWithFormula,
//	the real respawnSession INCLUDING provisionRecycleSettings, ClearHistory and recordRecycle, and
//	in the store-backed leg the real mcpstore. sendHandoffMail needs no substitution: its shipped
//	body is nooped by isTestBinary() (handoff.go:143-146) in every test binary, integration
//	included, so the production var IS the hermetic one here.
//
// Non-vacuity. Two shapes of this test would pass while proving nothing, so each is asserted
// against: a boundary that never fired leaves an empty recovery log, so the log is required to hold
// exactly one line rather than "at least zero"; and a funnel that logged the wrong class would
// still produce one line, so the trigger, the observed percentage and the session id are each
// checked against the values the fixture planted.
//
// Two-leg shape, as recovery_e2e_integration_test.go:49-57 explains: AC-2's grep is anchored at
// column 0, where only the top-level result line prints, so a top-level t.Skip would emit a
// column-0 `--- SKIP`. The parent body is therefore dependency-free and always runs to a PASS, and
// the store-backed leg lives in a subtest gated by requirePython3WithServerDeps.
//
// This file must not call t.Parallel: it reassigns the boundaryHandoffExec package var and shares
// the recoveryTracks map.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
	"github.com/stempeck/agentfactory/internal/session"
)

// realBoundaryHandoffExec captures the production boundaryHandoffExec seam at package-init, before
// any test wraps it. The store-backed subtest resets the seam to this value so the parent leg's
// still-active recordBoundaryPaneHop wrapper (its restore is parent-scoped, hence in force during the
// subtest) cannot re-point opts.Tx at the parent's recorder and leave the subtest's recorder seeing
// zero pane hops.
var realBoundaryHandoffExec = boundaryHandoffExec

// recordBoundaryPaneHop applies the declared substitution: the original boundaryHandoffExec runs in
// full, with only the tmux transport swapped for a recorder.
func recordBoundaryPaneHop(t *testing.T) *mockTmux {
	t.Helper()
	recorder := &mockTmux{}
	orig := boundaryHandoffExec
	boundaryHandoffExec = func(ctx context.Context, cwd string, opts RespawnOptions, subject, message string) error {
		opts.Tx = recorder
		return orig(ctx, cwd, opts, subject, message)
	}
	t.Cleanup(func() { boundaryHandoffExec = orig })
	return recorder
}

// assertBoundaryFunnelRecord is AC-3 spelled as the acceptance criterion spells it: the raw file is
// grepped for the trigger name before it is decoded, because AC-3's check is a `grep -c` over the
// bytes and a decoder that renamed the field would hide that.
func assertBoundaryFunnelRecord(t *testing.T, root, wantSession string, wantPct float64) recoveryLogEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, ".runtime", "recovery_log.jsonl"))
	if err != nil {
		t.Fatalf("AC-3 reads .runtime/recovery_log.jsonl; it is not there: %v", err)
	}
	if got := strings.Count(string(raw), triggerStepBoundaryHandoff); got < 1 {
		t.Fatalf("AC-3's `grep -c %q` would report %d, want >=1. Log:\n%s",
			triggerStepBoundaryHandoff, got, raw)
	}

	lines := readRecoveryLogLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("the boundary must reach the funnel exactly once, got %d lines:\n%s", len(lines), raw)
	}
	entry := lines[0]
	if entry.Trigger != triggerStepBoundaryHandoff {
		t.Errorf("log trigger = %q, want %q — an exec of `af handoff` would have recorded %q (G1)",
			entry.Trigger, triggerStepBoundaryHandoff, triggerSelfHandoff)
	}
	if entry.ObservedPct != wantPct {
		t.Errorf("log observed_pct = %v, want %v — a zero renders as UNKNOWN on the read surface",
			entry.ObservedPct, wantPct)
	}
	if entry.SessionID != wantSession {
		t.Errorf("log session_id = %q, want the SANITIZED stem %q, which is the spelling the "+
			"funnel's own fence compares against", entry.SessionID, wantSession)
	}
	return entry
}

// assertRespawnsIntoPrime is the resume assertion at the funnel level: a recycled session reaches
// neither af up nor af sling, so `af prime` in the rebuilt startup command is the only thing that
// makes the next step's context appear.
func assertRespawnsIntoPrime(t *testing.T, recorder *mockTmux) {
	t.Helper()
	if len(recorder.respawnPaneCalls) != 1 {
		t.Fatalf("the injected Tx must intercept the pane hop exactly once, got %d calls — "+
			"under -tags=integration the alternative is a live tmux respawn-pane",
			len(recorder.respawnPaneCalls))
	}
	if !strings.Contains(recorder.respawnPaneCalls[0].cmd, "af prime") {
		t.Errorf("respawn command = %q, want it to carry `af prime` — that is the resume engine",
			recorder.respawnPaneCalls[0].cmd)
	}
	if got := recorder.respawnPaneCalls[0].pane; got != os.Getenv("TMUX_PANE") {
		t.Errorf("respawned pane = %q, want the ambient TMUX_PANE %q — the boundary recycles the "+
			"pane it is running in, never a name it derived", got, os.Getenv("TMUX_PANE"))
	}
}

func TestBoundaryHandoffRespawnsIntoPrimeIntegration(t *testing.T) {
	// Leg 1 — the dependency-free proof, and the column-0 PASS carrier. Memstore-backed, so it
	// needs no Python, but the funnel it drives is the shipped one.
	now := boundaryTestNow()
	fx := newLifecycleFixture(t)
	step := armBoundaryFixture(t, fx)

	// ADR-018: respawnSession's Manager is only ever asked to BuildStartupCommand, so only the name
	// seam needs redirecting — but under -tags=integration nothing else would stop a production
	// af-<agent> identity being named, so it is redirected anyway.
	t.Cleanup(session.InstallHermeticForTest("af-test-"+hashName(t.Name())+"-", nil))
	resetRecoveryTracks()
	t.Cleanup(resetRecoveryTracks)

	writeRuntimeFile(t, fx.workDir, "session_id", "sess.boundary\n")
	plantSessionSnapshot(t, fx.root, fx.agent, "sessboundary", 88, 1000, now.Add(-10*time.Second), now)

	tmuxPaneEnv(t)
	recorder := recordBoundaryPaneHop(t)

	if got := len(readRecoveryLogLines(t, fx.root)); got != 0 {
		t.Fatalf("nothing may have been recycled before af done ran, got %d log lines", got)
	}

	out := captureStdout(t, func() {
		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
	})
	if !strings.Contains(out, "handing off for a clean session") || !strings.Contains(out, step.ID) {
		t.Errorf("stdout = %q, want the boundary line naming step %s", out, step.ID)
	}

	assertRespawnsIntoPrime(t, recorder)
	entry := assertBoundaryFunnelRecord(t, fx.root, "sessboundary", 88)
	if entry.ThresholdPct != 75 {
		t.Errorf("log threshold_pct = %d, want the shipped default 75 — the fixture writes no "+
			"startup.json, so the value must be derived rather than assumed present", entry.ThresholdPct)
	}

	t.Run("ProductionStoreFromASeededTwoStepFormula", func(t *testing.T) {
		requirePython3WithServerDeps(t)

		// supervisor rather than manager: agents.json seeds it autonomous, which is the shape an
		// unattended step boundary actually has.
		const agent = "supervisor"

		root := setupTestFactoryForDone(t, agent)
		ensurePySymlink(t, root)
		t.Cleanup(func() { terminateMCPServer(root) })

		for _, args := range [][]string{
			{"init", "-q"},
			{"config", "user.email", "test@boundary.test"},
			{"config", "user.name", "Boundary E2E"},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
			}
		}

		store, err := mcpstore.New(root, agent)
		if err != nil {
			t.Fatalf("mcpstore.New: %v", err)
		}
		ctx := t.Context()
		epic, err := store.Create(ctx, issuestore.CreateParams{
			Type:     issuestore.TypeEpic,
			Title:    "Formula: boundary handoff",
			Assignee: agent,
		})
		if err != nil {
			t.Fatalf("create epic: %v", err)
		}
		// TWO steps, because the boundary only fires when steps remain: a one-step formula takes
		// the completion branch and would make this test pass by never reaching the decision.
		for _, title := range []string{"Step: the one that closes", "Step: the one still to come"} {
			if _, err := store.Create(ctx, issuestore.CreateParams{
				Type:     issuestore.TypeTask,
				Parent:   epic.ID,
				Title:    title,
				Assignee: agent,
			}); err != nil {
				t.Fatalf("create %q: %v", title, err)
			}
		}

		agentDir := resolveAgentDir(root, agent)
		writeRuntimeFile(t, agentDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, agentDir, "session_id", "sess.prod\n")

		// Leg 1's newLifecycleFixture installed a memstore over the newIssueStore seam
		// (installMemStore) and set AF_ACTOR=manager; both are t-scoped to the PARENT, so their
		// restores run only AFTER this subtest returns and are still in force here. Left as-is,
		// runDoneCore would query that empty memstore as "manager", find no open step, take the
		// completion branch, and never fire the boundary — the very funnel this leg exists to
		// prove would go unexercised (recovery_e2e_integration_test.go:168-170 sidesteps the same
		// installMemStore trap by construction). Reinstate the production seam (resolve → real
		// mcpstore) and realign the actor to the supervisor the seeded steps are assigned to, both
		// scoped to this subtest.
		t.Setenv("AF_ACTOR", agent)
		origNewIssueStore, origNewIssueStoreAt := newIssueStore, newIssueStoreAt
		newIssueStore = func(wd, actor string) (issuestore.Store, error) {
			r, rerr := resolveInvokerRoot(wd)
			if rerr != nil {
				return nil, rerr
			}
			return mcpstore.New(r, actor)
		}
		newIssueStoreAt = func(r, actor string) (issuestore.Store, error) { return mcpstore.New(r, actor) }
		t.Cleanup(func() { newIssueStore, newIssueStoreAt = origNewIssueStore, origNewIssueStoreAt })

		t.Cleanup(session.InstallHermeticForTest("af-test-"+hashName(t.Name())+"-", nil))
		resetRecoveryTracks()
		t.Cleanup(resetRecoveryTracks)

		now := boundaryTestNow()
		plantSessionSnapshot(t, root, agent, "sessprod", 91, 4242, now.Add(-10*time.Second), now)

		tmuxPaneEnv(t)
		// Reset the seam to production first: Leg 1's recordBoundaryPaneHop wrapper is still installed
		// (its restore is parent-scoped), and stacking on it would let Leg 1's wrapper overwrite
		// opts.Tx with Leg 1's recorder — this leg's recorder would then observe zero pane hops.
		boundaryHandoffExec = realBoundaryHandoffExec
		recorder := recordBoundaryPaneHop(t)

		// The production flow: af prime runs before af done.
		outputFormulaContext(ctx, io.Discard, agentDir)

		if err := runDoneCore(ctx, agentDir, false, ""); err != nil {
			t.Fatalf("af done against the real store: %v", err)
		}

		assertRespawnsIntoPrime(t, recorder)
		assertBoundaryFunnelRecord(t, root, "sessprod", 91)

		// The step really closed. Without this the whole run could be a boundary that fired on a
		// verb that did no work — the one shape where a green funnel record means nothing.
		open, err := store.List(ctx, issuestore.Filter{
			Parent:           epic.ID,
			Statuses:         []issuestore.Status{issuestore.StatusOpen},
			IncludeAllAgents: true,
		})
		if err != nil {
			t.Fatalf("list open children: %v", err)
		}
		if len(open) != 1 {
			t.Fatalf("expected exactly 1 step still open after the close, got %d: %+v", len(open), open)
		}
	})
}
