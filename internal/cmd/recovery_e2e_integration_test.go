//go:build integration

package cmd

// K16 (issue #596, AC-9): the integration-lane end-to-end proof that the assembled
// context-exhaustion recovery pipeline works on the PRODUCTION path — as opposed to the unit lane,
// which proves each part works with the store, tmux and mail seams substituted.
//
// What the unit lane cannot show. newRecoveryFixture (recovery_test.go:57) deliberately overrides
// sendHandoffMail, newCmdTmux, recoveryOpenStep and doRespawn, because in the default (!integration)
// build the ADR-018 tmux guard panics on any af- pane and the store guard hands back an empty
// memstore. Consequently pollOccupancy's own body — the scope probe, the ReadObservations sweep and
// the evaluateAgent fan-out — has no firing coverage anywhere: its only two unit call sites
// (recovery_funnel_test.go:476, :501) both assert it returns nil. This test drives that body for
// real, against a real Python-backed MCP issue store, and asserts on the durable records the
// production run leaves behind.
//
// THE SUBSTITUTION BOUNDARY (design-doc.md Gap 11; AC-9 "the substitution boundary is declared, not
// hidden"). Exactly ONE hop is substituted, and this is the declaration of it:
//
//   SUBSTITUTED — the final `tmux respawn-pane` hop. doRespawn (recovery.go:1505) is overridden to
//   set RespawnOptions.Tx to a recording mockTmux and then call the REAL respawnSession. The
//   executor builds its own RespawnOptions with no Tx (recovery.go:1478-1497), so respawnSession
//   would otherwise fall through to tmux.NewTmux() (helpers.go:181-183); under -tags=integration
//   guardMode is false (tmux/guard_integration.go:7), so that fallthrough is a LIVE respawn-pane
//   carrying a real `claude … 'af prime'` command line. CI has no model credentials and no claude
//   binary, so executing it could not resume anything — the assertion available there is that the
//   command we would have run is the right one, which is what recording it proves. Session names are
//   additionally forced to the ADR-018 throwaway af-test-* prefix, so no production af-<agent>
//   identity is ever named. The model turn itself is covered by fixture replay in the unit lane plus
//   the production AC-6 records; no CI test can prove a real model resumes real work.
//
//   NOT SUBSTITUTED — everything upstream of that hop, which is the point: the real issue store
//   (storeguard_integration.go sets storeGuardActive=false, so newIssueStore builds an mcpstore
//   against the Python server), the real recoveryOpenStep body (recovery.go:1045), the real
//   pollOccupancy sweep, the real evaluateAgent, the real recoverExhausted, and the real
//   respawnSession funnel INCLUDING the C-1 anchoring — the K6 log write and the K19 fence arm at
//   helpers.go:197. `af prime`'s step re-print runs the real outputFormulaContext against the same
//   store read.
//
// Non-vacuity. Three shapes of this test would pass while proving nothing, so each is asserted
// against explicitly: the scope probe silently drops an agent with no live session and pollOccupancy
// then returns nil (recovery.go:886, :900) — so the decision slice is asserted non-empty; a leaked
// recoveryOpenStep stub would route the sweep into the K23 no-step branch — so the production body
// is reinstalled and its answer is checked BEFORE the sweep runs; and a store that failed to answer
// yields known=false and the same silent divergence — so resumed_step and instance_id are asserted
// to equal the ids actually seeded in the store, which they can only do if the real store was read.
//
// Two-leg shape. AC #2 asserts
//
//	go test -tags=integration ./internal/cmd/ -run 'TestRecovery.*(E2E|Integration)' -v | grep -q '^--- PASS: TestRecovery'
//
// whose grep is anchored at column 0, where only the top-level result line prints. A top-level
// t.Skip would emit a column-0 `--- SKIP` and fail that grep. So the parent body is
// dependency-free and always runs to a PASS, and the store-backed sweep lives in a subtest gated by
// requirePython3WithServerDeps — a friendly skip locally, a hard failure under the CI integration
// job's AF_REQUIRE_REAL_STORE=1. The idiom is statusline_failopen_integration_test.go:19-27.
//
// This file must not call t.Parallel: it reassigns the package-var seams doRespawn, newCmdTmux,
// recoveryOpenStep and sessionPrefixFn, and shares the recoveryTracks map.

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
	"github.com/stempeck/agentfactory/internal/session"
)

// productionRecoveryOpenStep pins the shipped recoveryOpenStep body at package-var init time, before
// any test can run. It exists because newRecoveryFixture's stateNoStep (recovery_test.go:101)
// reassigns the seam with NO t.Cleanup, so the stub leaks for the remainder of the binary; a sweep
// that inherited it would report "no open step", take the K23 branch, never recycle, and fail for a
// reason that has nothing to do with the production path. Go initializes package-level variables in
// dependency order, so this captures the real body rather than whatever a test left behind.
var productionRecoveryOpenStep = recoveryOpenStep

func TestRecoveryContextExhaustion_E2E(t *testing.T) {
	// Leg 1 — the boundary's own safety proof, and the column-0 PASS carrier. Under
	// -tags=integration guardMode is false, so a respawnSession that ignored an injected Tx would
	// drive the real tmux server. Asserting the recorder caught the hop is what makes the
	// substitution declared above trustworthy rather than assumed; asserting `af prime` is in the
	// recorded command is AC-9's resume assertion at the funnel level.
	boundary := &mockTmux{}
	if err := respawnSession(RespawnOptions{
		FactoryRoot: t.TempDir(),
		AgentName:   "af-test-boundary",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "af-test-boundary-pane",
		Tx:          boundary,
	}); err != nil {
		t.Fatalf("respawnSession through a substituted Tx: %v", err)
	}
	if len(boundary.respawnPaneCalls) != 1 {
		t.Fatalf("the injected Tx must intercept the pane hop exactly once, got %d calls — "+
			"under -tags=integration the alternative is a live tmux respawn-pane",
			len(boundary.respawnPaneCalls))
	}
	if !strings.Contains(boundary.respawnPaneCalls[0].cmd, "af prime") {
		t.Fatalf("the respawned startup command must carry `af prime`, got %q",
			boundary.respawnPaneCalls[0].cmd)
	}

	t.Run("ProductionSweepFromSeededStall", func(t *testing.T) {
		requirePython3WithServerDeps(t)

		const agent = "supervisor" // agents.json seeds this one autonomous; `manager` is interactive,
		// and shouldAutoRecover (watchdog.go:207) would refuse to recycle it.

		root := setupTestFactoryForDone(t, agent)
		ensurePySymlink(t, root)
		t.Cleanup(func() { terminateMCPServer(root) })

		for _, args := range [][]string{
			{"init", "-q"},
			{"config", "user.email", "test@recovery.test"},
			{"config", "user.name", "Recovery E2E"},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
			}
		}

		// Seed the real store with the shape an exhausted agent holds: an open formula instance with
		// one ready step. Children carry a non-empty Assignee per the Phase-1 data-plane invariant
		// (done_integration_test.go:72-75).
		store, err := mcpstore.New(root, "recovery-e2e")
		if err != nil {
			t.Fatalf("mcpstore.New: %v", err)
		}
		ctx := t.Context()
		epic, err := store.Create(ctx, issuestore.CreateParams{
			Type:     issuestore.TypeEpic,
			Title:    "Formula: recovery e2e",
			Assignee: agent,
		})
		if err != nil {
			t.Fatalf("create epic: %v", err)
		}
		step, err := store.Create(ctx, issuestore.CreateParams{
			Type:     issuestore.TypeTask,
			Parent:   epic.ID,
			Title:    "Step: the work in flight when context ran out",
			Assignee: agent,
		})
		if err != nil {
			t.Fatalf("create step: %v", err)
		}

		agentDir := resolveAgentDir(root, agent)
		writeRuntimeFile(t, agentDir, "hooked_formula", epic.ID)

		// Reinstall the production seam over any stub an earlier test leaked (see the
		// productionRecoveryOpenStep comment).
		origOpenStep := recoveryOpenStep
		recoveryOpenStep = productionRecoveryOpenStep
		t.Cleanup(func() { recoveryOpenStep = origOpenStep })

		// ADR-018: every session this test names carries a throwaway prefix. mk is nil because
		// respawnSession's Manager is only ever asked to BuildStartupCommand — it performs no tmux
		// I/O — so only the name seam needs redirecting. setupHermeticSessions is deliberately NOT
		// reused: it also calls installMemStore (hermetic_test.go:206), which would swap the real
		// store this test exists to exercise for an empty in-memory one.
		t.Cleanup(session.InstallHermeticForTest("af-test-"+hashName(t.Name())+"-", nil))

		// The scope probe (recovery.go:885) drops any agent whose session is not live, and
		// pollOccupancy then returns nil. fakeTmux answers HasSession true and returns "" from
		// GetEnvironment, so sessionForeignRoot (agents.go:257) reads the session as local.
		tmuxFake := newFakeTmux()
		tmuxFake.present[session.SessionName(agent)] = true
		origCmdTmux := newCmdTmux
		newCmdTmux = func() cmdTmux { return tmuxFake }
		t.Cleanup(func() { newCmdTmux = origCmdTmux })

		resetRecoveryTracks()
		t.Cleanup(resetRecoveryTracks)

		// THE SUBSTITUTION BOUNDARY, applied. Everything inside respawnSession still runs.
		recorder := &mockTmux{}
		origRespawn := doRespawn
		doRespawn = func(opts RespawnOptions) error {
			opts.Tx = recorder
			return respawnSession(opts)
		}
		t.Cleanup(func() { doRespawn = origRespawn })

		// Pre-flight: prove the REAL store answers with the REAL seeded step before the sweep runs.
		// Without this, a store that failed to start would yield known=false, the sweep would take a
		// fail-closed branch, and the failure would surface as a confusing absence downstream.
		seededStep, hasStep, known := recoveryOpenStep(agentDir)
		if !known {
			t.Fatal("the real issue store did not answer — the e2e would prove nothing past this point")
		}
		if !hasStep || seededStep != step.ID {
			t.Fatalf("production recoveryOpenStep = (%q, %t), want the seeded step %q — a leaked "+
				"stub or an unseeded store would route the sweep into the K23 no-step branch",
				seededStep, hasStep, step.ID)
		}

		agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
		if err != nil {
			t.Fatalf("LoadAgentConfig: %v", err)
		}
		cfg := testRecoveryConfig()
		now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

		// The seeded stall shape: occupancy well past the 85% threshold, written 10s ago so both
		// passes read it fresh (staleness_secs is 180).
		plantSnapshot(t, root, agent, "sess-e2e", 92, now.Add(-10*time.Second), now, cfg)

		// Pass 1. confirm_ticks is 2, so one high reading must NOT fire. This is also the
		// non-vacuity guard: a sweep that dropped the agent from scope returns nil here.
		first := pollOccupancy(root, agentsCfg, cfg, now)
		firstDecision := decisionFor(t, first, agent)
		if firstDecision.verdict.fire || firstDecision.executed {
			t.Fatalf("one high reading must not recycle — confirm_ticks exists to require a "+
				"sustained one; got fire=%t executed=%t", firstDecision.verdict.fire, firstDecision.executed)
		}
		if got := len(readRecoveryLogLines(t, root)); got != 0 {
			t.Fatalf("nothing may have been recycled yet, got %d recovery log lines", got)
		}
		if got := len(recorder.respawnPaneCalls); got != 0 {
			t.Fatalf("no pane may have been respawned yet, got %d calls", got)
		}

		// Pass 2 — the sustained reading. Real trigger, real executor, real funnel.
		second := pollOccupancy(root, agentsCfg, cfg, now.Add(30*time.Second))
		decision := decisionFor(t, second, agent)
		if decision.err != nil {
			t.Fatalf("recoverExhausted: %v", decision.err)
		}
		if !decision.executed {
			t.Fatalf("sustained high occupancy must recycle through the production path; "+
				"verdict fire=%t halted=%t fenced=%t reason=%q",
				decision.verdict.fire, decision.halted, decision.fenced, decision.verdict.reason)
		}
		if decision.verdict.trigger != triggerContextExhaustion {
			t.Errorf("trigger = %q, want %q", decision.verdict.trigger, triggerContextExhaustion)
		}

		// AC-9: the respawned pane's startup command carries `af prime`.
		if len(recorder.respawnPaneCalls) != 1 {
			t.Fatalf("the funnel must respawn the pane exactly once, got %d calls",
				len(recorder.respawnPaneCalls))
		}
		respawn := recorder.respawnPaneCalls[0]
		if !strings.Contains(respawn.cmd, "af prime") {
			t.Errorf("respawn command = %q, want it to carry `af prime` — that is the resume engine", respawn.cmd)
		}
		if want := session.SessionName(agent); respawn.pane != want {
			t.Errorf("respawned pane = %q, want the throwaway %q (ADR-018)", respawn.pane, want)
		}
		if !strings.HasPrefix(respawn.pane, "af-test-") {
			t.Errorf("respawned pane = %q, want an af-test-* throwaway name (ADR-018)", respawn.pane)
		}

		// AC-6: the durable record. resumed_step and instance_id are the load-bearing assertions —
		// they can only carry the seeded ids if the production recoveryOpenStep queried the REAL
		// store, which is what distinguishes this from a re-run of the unit lane.
		lines := readRecoveryLogLines(t, root)
		if len(lines) != 1 {
			t.Fatalf("the recycle must be logged exactly once at the funnel, got %d lines", len(lines))
		}
		entry := lines[0]
		if entry.Trigger != triggerContextExhaustion {
			t.Errorf("log trigger = %q, want %q", entry.Trigger, triggerContextExhaustion)
		}
		if entry.ThresholdPct != cfg.ContextThresholdPct {
			t.Errorf("log threshold_pct = %d, want %d", entry.ThresholdPct, cfg.ContextThresholdPct)
		}
		if entry.ObservedPct < 91 || entry.ObservedPct > 93 {
			t.Errorf("log observed_pct = %v, want ~92", entry.ObservedPct)
		}
		if entry.SessionID != "sess-e2e" {
			t.Errorf("log session_id = %q, want %q", entry.SessionID, "sess-e2e")
		}
		if entry.ResumedStep != step.ID {
			t.Errorf("log resumed_step = %q, want the step seeded in the real store (%q) — AC-6 (iii)",
				entry.ResumedStep, step.ID)
		}
		if entry.InstanceID != epic.ID {
			t.Errorf("log instance_id = %q, want the epic seeded in the real store (%q)",
				entry.InstanceID, epic.ID)
		}

		// AC-9: `af prime` re-prints the step. outputFormulaContext is what the respawned session's
		// `af prime` runs, and it reads the same store.Ready the trigger read.
		var primed bytes.Buffer
		outputFormulaContext(ctx, &primed, agentDir)
		if !strings.Contains(primed.String(), step.Title) {
			t.Errorf("af prime must re-print the open step %q, got:\n%s", step.Title, primed.String())
		}
		if !strings.Contains(primed.String(), "ready") {
			t.Errorf("af prime must report the step as ready, got:\n%s", primed.String())
		}
	})
}

// decisionFor returns the sweep's decision for one agent, failing when the sweep produced none. The
// failure it exists to catch is silent: pollOccupancy returns nil for an agent it dropped from
// scope, and every assertion downstream of an empty slice would otherwise be skipped rather than
// violated (recovery.go:886, :900).
func decisionFor(t *testing.T, decisions []recoveryDecision, agent string) recoveryDecision {
	t.Helper()
	if len(decisions) == 0 {
		t.Fatal("pollOccupancy returned no decisions — the agent was dropped from scope before it " +
			"was ever evaluated, so nothing below this line would have been tested")
	}
	for _, d := range decisions {
		if d.agent == agent {
			return d
		}
	}
	t.Fatalf("no decision for %q in %d decisions", agent, len(decisions))
	return recoveryDecision{}
}
