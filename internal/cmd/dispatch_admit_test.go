//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// These tests own the dispatch capacity-admission gate (#672) — the first hook allowed to emit a
// blocking permissionDecision:"deny" under the ADR-007 (2026-08-31) amendment. Phase-8 blind review
// found the owner proven only at its arithmetic (tokenomics.WithinBackendCapacity) and its
// registration (the settings templates), never at the ACT of enforcement: a deny emitted AND an
// un-gated record written for an over-capacity launch. These tests close that gap, plus the
// reservation-ledger algebra the two-in-one-message fix rests on and the deny JSON shape the platform
// contract depends on. Like tokenomics_admission_test.go they must not run in parallel:
// newLifecycleFixture chdirs into the agent work dir.

// TestReservationTokens_AdmitOneRefuseTwo pins the algebra the whole two-in-one-message fix rests on
// (#672 D9a), which before this test was asserted only in a code comment (dispatch_admit.go:63-69).
// One orchestrator at occupancy S launches two sub-agents in one message: the FIRST (ledger empty)
// must be admitted and the SECOND (ledger holding the first admit) refused, for EVERY S with real
// headroom — that is what "orchestrator + one" means and what the k=3/5 scalar buys. The comparison
// mirrors WithinBackendCapacity's own: projected = S + reservation, judged against the ceiling.
func TestReservationTokens_AdmitOneRefuseTwo(t *testing.T) {
	const ceiling = int64(180000) // a 200000-token pool at the shipped 10% margin
	for _, s := range []int64{0, ceiling / 4, ceiling / 2, ceiling * 3 / 4} {
		if first := s + reservationTokens(ceiling, s, 0); first > ceiling {
			t.Errorf("S=%d: first launch projects %d > ceiling %d — the orchestrator's own first child was refused",
				s, first, ceiling)
		}
		if second := s + reservationTokens(ceiling, s, 1); second <= ceiling {
			t.Errorf("S=%d: second launch projects %d <= ceiling %d — the two-in-one-message oversubscription "+
				"the ledger exists to stop was admitted", s, second, ceiling)
		}
	}
}

// TestReservationTokens_ZeroWhenAlreadyFull: at or above the ceiling the measured sum already refuses,
// so the reservation is 0 — a full backend needs no phantom load to push it over, and a negative
// headroom must never wrap into a positive reservation.
func TestReservationTokens_ZeroWhenAlreadyFull(t *testing.T) {
	if r := reservationTokens(180000, 180000, 0); r != 0 {
		t.Errorf("reservation at exactly the ceiling = %d, want 0", r)
	}
	if r := reservationTokens(180000, 250000, 2); r != 0 {
		t.Errorf("reservation above the ceiling = %d, want 0 (no wrap through negative headroom)", r)
	}
}

// TestReservationLedger_MarkerLifecycle proves the on-disk ledger the two-in-one-message fix depends
// on: a marker written by an admit is counted while fresh, so a second call in the same message sees
// ledgerCount >= 1 (dispatch_admit.go:204-205); a marker older than the TTL is swept on read, because
// the child it described has a reading of its own by now and keeping it would double-count.
func TestReservationLedger_MarkerLifecycle(t *testing.T) {
	dir := reservationDir(t.TempDir(), "http://127.0.0.1:1234")
	now := time.Now()

	if n := countLiveReservations(dir, dispatchReservationSafetyTTL, now); n != 0 {
		t.Fatalf("empty ledger counted %d live reservations, want 0", n)
	}

	writeReservationMarker(dir, now)
	writeReservationMarker(dir, now.Add(time.Millisecond)) // a distinct nanotime yields a distinct file
	if n := countLiveReservations(dir, dispatchReservationSafetyTTL, now); n != 2 {
		t.Fatalf("two fresh markers counted %d, want 2 — a second same-message launch would not see the first admit", n)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	aged := filepath.Join(dir, entries[0].Name())
	old := now.Add(-2 * dispatchReservationSafetyTTL)
	if err := os.Chtimes(aged, old, old); err != nil {
		t.Fatal(err)
	}
	if n := countLiveReservations(dir, dispatchReservationSafetyTTL, now); n != 1 {
		t.Fatalf("after ageing one marker the ledger counted %d, want 1", n)
	}
	if _, err := os.Stat(aged); !os.IsNotExist(err) {
		t.Errorf("the expired marker was not swept on read (stat err=%v)", err)
	}
}

// TestClaimSubagentSlot pins the #672 hard cap's atomic semaphore-of-one: the first claim admits and
// holds the single slot, a second is refused while it is held, a stale slot (a child that died without
// signalling) is reclaimed, and once the retire hook removes the slot the next launch admits again. This
// is the guarantee the arithmetic ledger cannot make — never two concurrent sub-agents — so if this
// regresses the Phase-2 fan-out oversubscription returns even with the cap configured.
func TestClaimSubagentSlot(t *testing.T) {
	workDir := t.TempDir()
	dir := reservationDir(workDir, "http://host.docker.internal:11434")
	now := time.Now()

	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, now) {
		t.Fatal("first claim was refused; the single sub-agent must be admitted")
	}
	slot := filepath.Join(dir, sequentialSlotName)
	if _, err := os.Stat(slot); err != nil {
		t.Fatalf("slot file not created on the first claim: %v", err)
	}
	if claimSubagentSlot(dir, dispatchReservationSafetyTTL, now.Add(time.Second)) {
		t.Fatal("second claim was ADMITTED while the slot is held — parallel sub-agents would run")
	}

	// A slot older than the TTL is a child that never signalled completion; it must be reclaimable.
	old := now.Add(-2 * dispatchReservationSafetyTTL)
	if err := os.Chtimes(slot, old, old); err != nil {
		t.Fatal(err)
	}
	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, now) {
		t.Fatal("a stale slot was not reclaimed; a crashed child would wedge the cap forever")
	}

	// The SubagentStop retire hook PROPOSES the slot's release; the claim side disposes of it, and only
	// once the evidence ladder agrees the child went quiet. retire must find the REAL slot living under
	// this launcher's ledger — not a slot the test freed itself. Pointed at workDir,
	// retireOneReservation sweeps the backend subdir and proposes on the only cap file there; the mutant
	// that SKIPS files named sequential.slot (M5b) writes no proposal, the quiet claim below is refused,
	// and this fails.
	retireOneReservation(dispatchRetirePayload{Cwd: workDir, SessionID: "sess-x"}, "", now)

	// A proposal alone must not free the slot — that IS #673. The child is still writing.
	evidence := fakeSubagentQuietEvidence(t)
	evidence.quiet, evidence.measured = 30*time.Second, true
	if claimSubagentSlot(dir, dispatchReservationSafetyTTL, now.Add(time.Minute)) {
		t.Fatal("a claim was ADMITTED on a stop proposal while the child's sidechain was still active " +
			"30s ago; that is the two-children-at-once regression #673 fixed")
	}

	// Once the sidechain has been quiet past the release threshold the proposal is honoured.
	evidence.quiet, evidence.measured = subagentQuietReleaseSecs+time.Minute, true
	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, now.Add(time.Minute)) {
		t.Fatal("after retire proposed and the child went quiet past the release threshold the next " +
			"sub-agent was still refused, so sequential progress would stall")
	}
	// The proposal is CONSUMED by the release it authorised. Leaving it would let the very next stale
	// stop release a freshly-claimed slot without any evidence of its own.
	if _, err := os.Stat(filepath.Join(dir, sequentialStopName)); !os.IsNotExist(err) {
		t.Errorf("the honoured %s proposal outlived the release it authorised (stat err=%v)", sequentialStopName, err)
	}
}

// TestDispatchDenyReason_SequentialOnly pins the hard-cap deny sentence: it names the operator switch and
// the one-already-running condition, and — unlike the arithmetic refusals — carries NO pool-percentage
// figures, because the cap is a semaphore, not a token calculation.
func TestDispatchDenyReason_SequentialOnly(t *testing.T) {
	msg := dispatchDenyReason("http://host.docker.internal:11434",
		tokenomics.BackendVerdict{Verdict: tokenomics.VerdictNoFit, Reason: reasonSequentialOnly, PoolTokens: 262144})
	for _, want := range []string{"sequential", "AF_DISABLE_PARALLEL_SUBAGENTS", "already running"} {
		if !strings.Contains(msg, want) {
			t.Errorf("sequential-only deny reason missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "% projected") {
		t.Errorf("sequential-only deny leaked pool arithmetic it does not compute: %s", msg)
	}
}

// TestEmitDispatchDeny_PlatformContract pins the exact PreToolUse deny JSON the gate's efficacy rests
// on (decision-gating-verification.md): permissionDecision:"deny" on a PreToolUse event, carrying the
// reason. A wrong shape here makes the refusal inert and the Phase-2 fan-out collapse recurs — which is
// why the blind reviewer capped Mechanical Enforcement until this contract was pinned by a test.
func TestEmitDispatchDeny_PlatformContract(t *testing.T) {
	var buf bytes.Buffer
	emitDispatchDeny(&buf, "over capacity")

	var got struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("deny payload is not valid JSON: %v\n%s", err, buf.String())
	}
	if got.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", got.HookSpecificOutput.HookEventName)
	}
	if got.HookSpecificOutput.PermissionDecision != "deny" {
		t.Errorf("permissionDecision = %q, want deny", got.HookSpecificOutput.PermissionDecision)
	}
	if got.HookSpecificOutput.PermissionDecisionReason != "over capacity" {
		t.Errorf("permissionDecisionReason = %q, want the reason passed in", got.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestDispatchDenyReason_CarriesArithmetic: the refused agent must be able to ARGUE with the refusal,
// not merely obey it (ADR-007 amendment condition 4), so the reason carries the backend key and the
// pool-vs-summed arithmetic that justified the deny.
func TestDispatchDenyReason_CarriesArithmetic(t *testing.T) {
	reason := dispatchDenyReason("http://127.0.0.1:1234", tokenomics.BackendVerdict{
		Verdict: tokenomics.VerdictNoFit, PoolTokens: 200000, SummedTokens: 190000,
		ProjectedPct: 95, HeadroomPct: 90,
	})
	for _, want := range []string{"http://127.0.0.1:1234", "190000", "200000"} {
		if !strings.Contains(reason, want) {
			t.Errorf("deny reason does not carry %q:\n%s", want, reason)
		}
	}
}

// writeDeclaredBackendModels installs a models.json whose default profile names a SHARED backend: an
// ANTHROPIC_BASE_URL (so NormalizedEndpoint is non-empty) plus an operator-declared
// AF_BACKEND_POOL_TOKENS — the pool fact the gate divides by since #669 THREAD-2. The
// CLAUDE_CODE_MAX_CONTEXT_TOKENS window is kept alongside it precisely to prove the gate arms on the
// declared pool fact even when a per-request window is also present: the pool operand reads ONLY
// AF_BACKEND_POOL_TOKENS now, never the window.
func writeDeclaredBackendModels(t *testing.T, root string) {
	t.Helper()
	models := `{"default":"decl","models":{"decl":{` +
		`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",` +
		`"ANTHROPIC_AUTH_TOKEN":"tok",` +
		`"AF_BACKEND_POOL_TOKENS":"200000",` +
		`"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"}}}`
	if err := os.WriteFile(config.ModelsConfigPath(root), []byte(models), 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}
}

// writeCapBackendModels installs a models.json whose default profile declares the #672 HARD CAP
// (AF_DISABLE_PARALLEL_SUBAGENTS) beside a shared pool. The pool is 262144 rather than the 200000
// writeDeclaredBackendModels uses so the child floor and the headroom predicate SEPARATE — at
// 200000 the 50000 floor refuses before the headroom divergence is observable (F2), and the cap's
// launcher-at-220000 case needs a pool the launcher is under to reach the floor at all.
func writeCapBackendModels(t *testing.T, root string) {
	t.Helper()
	models := fmt.Sprintf(`{"default":"cap","models":{"cap":{`+
		`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",`+
		`"ANTHROPIC_AUTH_TOKEN":"tok",`+
		`"AF_BACKEND_POOL_TOKENS":"%d",`+
		`"AF_DISABLE_PARALLEL_SUBAGENTS":"1"}}}`, capPoolTokens)
	if err := os.WriteFile(config.ModelsConfigPath(root), []byte(models), 0o644); err != nil {
		t.Fatalf("write cap models.json: %v", err)
	}
}

// capBackendKey is the normalized endpoint writeCapBackendModels declares — the key the reservation
// ledger dir hashes. NormalizedEndpoint("http://127.0.0.1:1234") is the identity, proven by the
// enforcement suite's marker assertions on the same URL.
const capBackendKey = "http://127.0.0.1:1234"

// TestRunDispatchAdmitCore_Enforcement drives the composed act end to end — the owner #672 elevates —
// through its testable core. It is the single highest-value gap the Phase-8 blind review named: the
// arithmetic and the wiring were proven, the enforcement was not.
func TestRunDispatchAdmitCore_Enforcement(t *testing.T) {
	t.Run("over-capacity launch is denied and leaves an un-gated retrievable record (AC-3)", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1) // dispatch defaults on under the umbrella
		writeDeclaredBackendModels(t, fx.root)
		// 95% of the 200000-token window is 190000 used, above the 90% ceiling on the launcher's
		// occupancy ALONE — so the refusal fires on measured load without leaning on the reservation.
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("an over-capacity Task launch was not denied; stdout:\n%s", out.String())
		}

		// Telemetry was never switched on, yet the refusal must be retrievable: corollary 3's "silence
		// never passes" is exactly a run-#1, telemetry-off refusal an operator can still read back.
		recs := dispatchInterventionRecords(t, fx.root, fx.agent)
		if len(recs) != 1 {
			t.Fatalf("one refusal wrote %d dispatch records, want exactly 1", len(recs))
		}
		r := recs[0]
		if r.Action != telemetry.ActionRefuse {
			t.Errorf("record action = %q, want %q", r.Action, telemetry.ActionRefuse)
		}
		if r.PoolTokens == nil || *r.PoolTokens != 200000 {
			t.Errorf("record pool_tokens = %v, want 200000 (the arithmetic that justified the refusal)", r.PoolTokens)
		}
		if r.SummedTokens == nil || *r.SummedTokens < 180000 {
			t.Errorf("record summed_tokens = %v, want the >= ceiling sum that refused", r.SummedTokens)
		}
	})

	t.Run("under-capacity launch admits, writes a reservation marker, no deny, no record", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeDeclaredBackendModels(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 20, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("an under-capacity launch emitted output, want silence:\n%s", out.String())
		}
		if recs := dispatchInterventionRecords(t, fx.root, fx.agent); len(recs) != 0 {
			t.Errorf("an admitted launch wrote %d dispatch records, want 0", len(recs))
		}
		// The admit must leave a ledger marker so a sibling launched moments later in the same message
		// is counted against the pool it just joined (dispatch_admit.go:204-205).
		if n := countLiveReservations(reservationDir(fx.workDir, "http://127.0.0.1:1234"), dispatchReservationSafetyTTL, now); n != 1 {
			t.Errorf("an admitted launch left %d reservation markers, want 1", n)
		}
	})

	t.Run("cloud profile with no base URL is inert: admits, no deny, no record", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		// A cloud profile declares a window but NO ANTHROPIC_BASE_URL, so it names no shared backend to
		// pool against — AC-6 inertness by construction (the declared-window source), not by occupancy:
		// the launcher is at 95% and still nothing fires, because there is no pool to divide.
		models := `{"default":"cloud","models":{"cloud":{"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"}}}`
		if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
			t.Fatal(err)
		}
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("a cloud profile produced gate output, want inert silence:\n%s", out.String())
		}
		if recs := dispatchInterventionRecords(t, fx.root, fx.agent); len(recs) != 0 {
			t.Errorf("a cloud profile wrote %d dispatch records, want 0 (inert, no arithmetic)", len(recs))
		}
	})

	t.Run("a non-Task tool is outside the gate's scope: silent, no record", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeDeclaredBackendModels(t, fx.root)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Bash", Cwd: fx.workDir}, time.Now()); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("a Bash tool call reached the gate; scope is new sub-agent dispatch only:\n%s", out.String())
		}
	})

	t.Run("armed gate with an unresolvable model registry fails open with an observe record (AC-8)", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		// The gate is ARMED but a required enforcement input will not resolve: an unquoted numeric makes
		// models.json fail the strict loader. AC-8 requires the launch be admitted AND an observe record
		// written — a broken gate reproduces today's behavior WITHOUT going silent.
		bad := `{"default":"x","models":{"x":{"CLAUDE_CODE_MAX_CONTEXT_TOKENS":220000}}}`
		if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if strings.Contains(out.String(), "deny") {
			t.Errorf("the gate refused on an input-resolution error; AC-8 requires fail-OPEN:\n%s", out.String())
		}
		recs := dispatchInterventionRecords(t, fx.root, fx.agent)
		if len(recs) != 1 || recs[0].Action != telemetry.ActionObserve {
			t.Fatalf("fail-open wrote %d dispatch records (want exactly 1 with action=observe): %+v", len(recs), recs)
		}
	})
}

// TestRunDispatchAdmitCore_ChildFloor pins F1 (r3906601296) + BAD-4 (r3906161527): the child-footprint
// FLOOR. Even when the headroom predicate would ADMIT, the gate must REFUSE when
// pool - Σmeasured < childFloor (default 50000) — the first child near the ceiling that reservationTokens
// alone lets through as reservation -> 0. When the launcher's OWN occupancy is what leaves < floor free,
// "launch one at a time" cannot help, so the deny counsels `af handoff` rather than the serialize text.
//
// The effective admission ceiling is the context-threshold breaker (AC-4 clamps margin and breaker
// together: 85% of 200000 = 170000), NOT the margin-only 90% (180000). The floor is therefore the
// DISTINCT refuser only in the narrow band where headroom would admit (live-sum <= 170000) yet
// pool - summedMeasured < 50000, i.e. summedMeasured in (150000, 155000]. Above it the headroom
// predicate refuses first (serialize); at or below 150000 the floor fits.
func TestRunDispatchAdmitCore_ChildFloor(t *testing.T) {
	t.Run("refuses when the launcher's own occupancy leaves < floor free (handoff text)", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeDeclaredBackendModels(t, fx.root) // pool 200000, no child-floor key -> default 50000
		// 76% -> summedMeasured = 152000. Headroom ADMITS (152000 + reservation 16800 = 168800 <= 170000),
		// but pool - summed = 48000 < 50000 -> the floor REFUSES. The launcher is the only occupant, so
		// pool - launcherOwn = 48000 < 50000 -> the handoff case, not sibling contention.
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 76, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("a launch leaving only 48000 < 50000 floor free was not denied; stdout:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "af handoff") {
			t.Errorf("a floor breach caused by the launcher's own occupancy must counsel `af handoff`:\n%s", out.String())
		}
		if strings.Contains(out.String(), "one at a time") {
			t.Errorf("a launcher-own floor breach was mislabeled as the sibling-contention serialize case:\n%s", out.String())
		}

		recs := dispatchInterventionRecords(t, fx.root, fx.agent)
		if len(recs) != 1 {
			t.Fatalf("one floor refusal wrote %d dispatch records, want exactly 1", len(recs))
		}
		if recs[0].Action != telemetry.ActionRefuse {
			t.Errorf("record action = %q, want %q", recs[0].Action, telemetry.ActionRefuse)
		}
		if recs[0].PoolTokens == nil || *recs[0].PoolTokens != 200000 {
			t.Errorf("record pool_tokens = %v, want 200000", recs[0].PoolTokens)
		}
		if recs[0].SummedTokens == nil || *recs[0].SummedTokens != 152000 {
			t.Errorf("record summed_tokens = %v, want 152000 (the measured sum that breached the floor)", recs[0].SummedTokens)
		}
	})

	// BAD-4 done-when clause 2 ("still pass when the floor fits"): admit at exactly the floor. This
	// proves the floor does not over-refuse and pins the strict-`<` threshold, one step below the 76%
	// refusal above (summed 150000 vs 152000).
	t.Run("admits when pool-summed == floor exactly (protective, refuse is strict <)", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeDeclaredBackendModels(t, fx.root)
		// 75% -> summed 150000; pool - summed = 50000, NOT < 50000 -> ADMIT (headroom also admits:
		// 150000 + 18000 = 168000 <= 170000).
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 75, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("a launch leaving exactly the 50000 floor free was refused; the floor must not over-refuse:\n%s", out.String())
		}
		if recs := dispatchInterventionRecords(t, fx.root, fx.agent); len(recs) != 0 {
			t.Errorf("an admitted at-floor launch wrote %d dispatch records, want 0", len(recs))
		}
		if n := countLiveReservations(reservationDir(fx.workDir, "http://127.0.0.1:1234"), dispatchReservationSafetyTTL, now); n != 1 {
			t.Errorf("an admitted launch left %d reservation markers, want 1", n)
		}
	})
}

// TestDispatchDenyReason_DistinguishesSerializeFromHandoff pins F1/BAD-4 done-when clause 3: the two
// deny texts are distinguishable in the recorded reason, and both keep the same record shape (backend
// key + pool/summed arithmetic). A floor breach the launcher's own occupancy caused counsels
// `af handoff` (serializing cannot help when one session alone leaves < floor free); a headroom /
// sibling-contention refusal keeps the "one at a time" serialize counsel. Phase 6 defines the
// launcher-floor discriminator as a comparable Reason const on BackendVerdict and makes
// dispatchDenyReason switch on it; today the function ignores Reason and always returns the serialize
// text, so the handoff assertion is a genuine behavioral RED.
func TestDispatchDenyReason_DistinguishesSerializeFromHandoff(t *testing.T) {
	const key = "http://127.0.0.1:1234"
	base := tokenomics.BackendVerdict{
		Verdict: tokenomics.VerdictNoFit, PoolTokens: 200000, SummedTokens: 190000,
		ProjectedPct: 95, HeadroomPct: 90,
	}
	handoffV := base
	handoffV.Reason = "childfloor-launcher" // Phase 6's launcher-floor discriminator value
	serializeV := base                      // empty Reason -> the existing sibling-contention serialize text

	handoff := dispatchDenyReason(key, handoffV)
	serialize := dispatchDenyReason(key, serializeV)

	if !strings.Contains(handoff, "af handoff") {
		t.Errorf("a launcher-own floor breach must counsel `af handoff`:\n%s", handoff)
	}
	if strings.Contains(handoff, "one at a time") {
		t.Errorf("the handoff text must not also carry the serialize counsel:\n%s", handoff)
	}
	if !strings.Contains(serialize, "one at a time") {
		t.Errorf("a sibling-contention refusal must keep the serialize counsel:\n%s", serialize)
	}
	if strings.Contains(serialize, "af handoff") {
		t.Errorf("the serialize text must not counsel handoff:\n%s", serialize)
	}
	if handoff == serialize {
		t.Errorf("the two deny texts are indistinguishable:\n%s", handoff)
	}
	for _, want := range []string{key, "190000", "200000"} {
		if !strings.Contains(handoff, want) {
			t.Errorf("the handoff text dropped the arithmetic %q:\n%s", want, handoff)
		}
		if !strings.Contains(serialize, want) {
			t.Errorf("the serialize text dropped the arithmetic %q:\n%s", want, serialize)
		}
	}
}

// TestClaimSubagentSlot_FailsOpenOnStatError pins N1 (r3906601... claimSubagentSlot fail-open
// contract). The function documents "Every failure fails OPEN (returns true / admits)", yet after an
// EEXIST its os.Stat failure branch fell through to return false — a false REFUSE from an input error,
// exactly the direction the contract forbids. A dangling symlink at the slot path makes it
// deterministic: O_EXCL create returns EEXIST (the link exists as a dirent), and os.Stat FOLLOWS the
// link to a missing target → ENOENT → statErr != nil.
//
// #673 added two evidence consults to this same EEXIST block and this test is what pins them BEHIND the
// unreadable-slot leg. Either one placed ahead of it would answer "not releasable" for a slot that
// cannot be opened, turning an input error back into a refusal and reverting N1 without touching its
// branch.
//
// #673 also changed HOW the leg reaches its answer, and this test is what holds the outcome fixed across
// that change. A dangling symlink and a slot deleted by a concurrent reclaim are indistinguishable in a
// single observation — both report ENOENT — but only one of them persists, so the claim now looks more
// than once and fails open only for the dirent that is still there and still unopenable. That is this
// one; the transient case must NOT reach this answer, or the cap admits alongside the reclaimer.
func TestClaimSubagentSlot_FailsOpenOnStatError(t *testing.T) {
	dir := t.TempDir()
	slot := filepath.Join(dir, sequentialSlotName)
	if err := os.Symlink("/no/such/target", slot); err != nil {
		t.Fatalf("planting a dangling symlink at the slot path: %v", err)
	}
	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, time.Now()) {
		t.Fatal("claimSubagentSlot REFUSED on an os.Stat failure; its documented contract fails OPEN " +
			"(returns true) on any error — a slot it cannot manage must never manufacture a false refusal")
	}
}

// TestRunDispatchAdmitCore_HardCap is the F1/F4 central thread: after F1 the sequential cap is no
// longer a pre-arithmetic short-circuit but the LAST predicate on the admit arm — headroom, then the
// child floor, then the slot claim — so a cap backend still pays the pool arithmetic and refuses a
// launcher that breaches the floor. It also drives the retire hook against a real sequential.slot
// (F4, killing mutation M5b end to end) and pins decision #1 (the cap-admit path writes ONLY the
// slot, never also an arithmetic marker).
func TestRunDispatchAdmitCore_HardCap(t *testing.T) {
	capMarkers := func(t *testing.T, workDir string) []string {
		t.Helper()
		return ledgerNames(t, reservationDir(workDir, capBackendKey))
	}

	// Subtests 1-3 chain one fixture through the slot's lifecycle: a lean launcher admits and claims
	// the slot, a second launch is refused while it is held, and the retire hook proposes its release so
	// a third admits again — but only once the evidence ladder says the child actually went quiet.
	t.Run("lifecycle: admit writes only the slot, second refuses sequential (nil summed), retire proposes and evidence frees it", func(t *testing.T) {
		now := time.Now()
		evidence := fakeSubagentQuietEvidence(t)
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeCapBackendModels(t, fx.root)
		// A lean launcher (pct=10 -> 20000): headroom admits and pool-20000 clears the 50000 floor, so
		// the slot claim is the only thing that can refuse.
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 10, 1000, now.Add(-10*time.Second), now)

		// (1) first launch admits and the SOLE marker it leaves is sequential.slot (decision #1).
		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore (first): %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("the first sub-agent under a cap backend was not admitted silently:\n%s", out.String())
		}
		if recs := dispatchInterventionRecords(t, fx.root, fx.agent); len(recs) != 0 {
			t.Fatalf("an admitted first launch wrote %d dispatch records, want 0", len(recs))
		}
		if names := capMarkers(t, fx.workDir); len(names) != 1 || names[0] != sequentialSlotName {
			t.Fatalf("the cap admit left markers %v, want exactly [%s] — the cap path is a semaphore of one "+
				"and must not also write an arithmetic marker (decision #1)", names, sequentialSlotName)
		}

		// (2) a second launch, slot still held, is refused with the sequential-only counsel and a
		// refuse record that carries NO summed_occupancy_tokens (C1: nil = "not a token-driven refusal").
		out.Reset()
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now.Add(time.Second)); err != nil {
			t.Fatalf("runDispatchAdmitCore (second): %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("a second concurrent sub-agent under the cap was not denied:\n%s", out.String())
		}
		for _, want := range []string{"AF_DISABLE_PARALLEL_SUBAGENTS", "already running"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("the cap refusal is missing %q:\n%s", want, out.String())
			}
		}
		if strings.Contains(out.String(), "% projected") {
			t.Errorf("the cap refusal leaked pool arithmetic it does not compute:\n%s", out.String())
		}
		recs := dispatchInterventionRecords(t, fx.root, fx.agent)
		if len(recs) != 1 {
			t.Fatalf("one cap refusal wrote %d dispatch records, want exactly 1", len(recs))
		}
		if recs[0].Action != telemetry.ActionRefuse {
			t.Errorf("record action = %q, want %q", recs[0].Action, telemetry.ActionRefuse)
		}
		if recs[0].SummedTokens != nil {
			t.Errorf("the sequential-only refusal recorded summed_occupancy_tokens = %v; the cap is a "+
				"semaphore, not token arithmetic, so it must be nil (C1: not the ptr-to-0 'measured zero')", *recs[0].SummedTokens)
		}

		// (3) the retire hook proposes release on the real sequential.slot. A mutant that skips files
		// named sequential.slot writes no proposal at all and step (5) below refuses (kills M5b end to
		// end). The proposal by itself changes NOTHING: while the child is still writing, the fourth
		// launch is still denied — the whole-of-#673 assertion, driven end to end through the admit
		// command rather than through claimSubagentSlot directly.
		retireOneReservation(dispatchRetirePayload{Cwd: fx.workDir, SessionID: "sessa"}, "", now.Add(30*time.Second))
		evidence.quiet, evidence.measured = 30*time.Second, true
		out.Reset()
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now.Add(time.Minute)); err != nil {
			t.Fatalf("runDispatchAdmitCore (third): %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("a stop proposal alone freed the cap slot and a second child was admitted while the "+
				"first was still writing — the #673 concurrency regression:\n%s", out.String())
		}

		// (4) the child goes quiet past the release threshold; the next launch admits and the ledger is
		// back to exactly the slot — the proposal consumed, no reclaim corpse left behind.
		evidence.quiet, evidence.measured = subagentQuietReleaseSecs+time.Minute, true
		out.Reset()
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now.Add(2*time.Minute)); err != nil {
			t.Fatalf("runDispatchAdmitCore (fourth): %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("after the child went quiet past the release threshold the next sub-agent was still "+
				"refused; sequential progress stalled:\n%s", out.String())
		}
		if names := capMarkers(t, fx.workDir); !ledgerIs(names, releasedLedger()) {
			t.Fatalf("the re-admit left markers %v, want the slot re-created beside its release-audit "+
				"breadcrumb as %v — the proposal consumed and no reclaim corpse left behind", names, releasedLedger())
		}
	})

	// Subtest 4 is the AC1 pinning RED: a cap launcher already carrying 220000 of the 262144 pool
	// leaves 42144 < the 50000 floor free, so — once the cap honors the arithmetic — it is refused with
	// the launcher-own `af handoff` counsel. Margin 16 (raw ceiling == breaker-clamped ceiling) keeps
	// this a pure F1 pin, independent of the F2 ceiling fix.
	t.Run("cap launcher at 220000 gets the child-floor handoff refusal (AC1, RED at head)", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 16, 1)
		writeCapBackendModels(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 110, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("a cap launcher leaving 42144 < 50000 floor free was ADMITTED; the cap short-circuited "+
				"the arithmetic instead of composing with it (F1):\n%s", out.String())
		}
		if !strings.Contains(out.String(), "af handoff") {
			t.Errorf("a launcher-own floor breach under the cap must counsel `af handoff`, not the serialize text:\n%s", out.String())
		}
		recs := dispatchInterventionRecords(t, fx.root, fx.agent)
		if len(recs) != 1 {
			t.Fatalf("one floor refusal wrote %d dispatch records, want exactly 1", len(recs))
		}
		if recs[0].PoolTokens == nil || *recs[0].PoolTokens != 262144 {
			t.Errorf("record pool_tokens = %v, want 262144", recs[0].PoolTokens)
		}
		if recs[0].SummedTokens == nil || *recs[0].SummedTokens != 220000 {
			t.Errorf("record summed_tokens = %v, want 220000 (the measured sum that breached the floor)", recs[0].SummedTokens)
		}
	})
}

// TestRunDispatchAdmitCore_CeilingClampParity pins F2 (r3906601303): the ceiling used to size the
// launch reservation (dispatch_admit.go:235) must be the SAME breaker-clamped ceiling the verdict
// judges against, not the raw margin-only ceiling. At the shipped margin 16 the two coincide (84 <=
// the 85 breaker), so a lone launcher at ~80% admits; at margin 10 the raw ceiling is 90 while the
// verdict clamps to 85, so the head reservation is oversized and the SAME launcher is wrongly
// refused. Pool 262144 is required — at the default 200000 the 50000 child floor refuses first and
// masks the divergence. RED at head for the margin-10 leg; GREEN once line 235 reads the effective ceiling.
func TestRunDispatchAdmitCore_CeilingClampParity(t *testing.T) {
	writePool262144 := func(t *testing.T, root string) {
		t.Helper()
		models := `{"default":"decl","models":{"decl":{` +
			`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",` +
			`"ANTHROPIC_AUTH_TOKEN":"tok",` +
			`"AF_BACKEND_POOL_TOKENS":"262144"}}}`
		if err := os.WriteFile(config.ModelsConfigPath(root), []byte(models), 0o644); err != nil {
			t.Fatalf("write pool-262144 models.json: %v", err)
		}
	}

	// A lone launcher at pct=105 -> 210000 (~80% of 262144); pool-210000 = 52144 >= the 50000 floor, so
	// the headroom predicate is the sole decider and the F2 divergence is observable.
	admitsAtMargin := func(t *testing.T, margin int) bool {
		t.Helper()
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, margin, 1)
		writePool262144(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 105, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		return out.Len() == 0
	}

	t.Run("margin 16 admits a lone launcher at ~80% (control, raw ceiling == clamped)", func(t *testing.T) {
		if !admitsAtMargin(t, 16) {
			t.Fatal("a lone launcher at ~80% of the pool was refused at margin 16; the control must admit")
		}
	})
	t.Run("margin 10 admits identically (RED at head: raw 90% ceiling oversizes the reservation)", func(t *testing.T) {
		if !admitsAtMargin(t, 10) {
			t.Fatal("the SAME lone launcher at ~80% was refused at margin 10 but admitted at margin 16; the " +
				"reservation ceiling used the raw margin instead of the breaker-clamped verdict ceiling (F2)")
		}
	})
}

// TestClearDispatchReservations_FreesLeakedSlot pins the F5 scenario-(i) cleanup (r3906601... clear
// the reservation ledger at af up / cleanupRuntimeArtifacts): a sequential.slot left by a child whose
// session was torn down before SubagentStop is reaped by nothing but the 2h TTL, so a fresh session's
// FIRST launch is falsely refused "one sub-agent is already running". clearDispatchReservations — the
// helper wired into both cleanup sites — must remove the whole ledger so the next claim admits. RED
// before the F5 cleanup wiring lands (the leaked slot survives and the next claim is refused).
func TestClearDispatchReservations_FreesLeakedSlot(t *testing.T) {
	workDir := t.TempDir()
	dir := reservationDir(workDir, "http://127.0.0.1:1234")
	now := time.Now()

	// A sibling genuinely holds the slot; while it lives a second concurrent claim is (correctly) refused.
	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, now) {
		t.Fatal("precondition: the first claim should hold the slot")
	}
	if claimSubagentSlot(dir, dispatchReservationSafetyTTL, now.Add(time.Second)) {
		t.Fatal("precondition: a second concurrent claim must be refused while the slot is genuinely held")
	}

	// The prior session was torn down without a SubagentStop; the next session clears the ledger.
	clearDispatchReservations(workDir)

	if _, err := os.Stat(filepath.Join(dir, sequentialSlotName)); !os.IsNotExist(err) {
		t.Fatalf("clearDispatchReservations left the leaked slot behind (stat err=%v)", err)
	}
	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, now.Add(2*time.Second)) {
		t.Fatal("after the ledger was cleared the next session's first launch was still refused; the false " +
			"'one sub-agent is already running' of #669 F5 scenario (i) persists")
	}
}

// TestCleanupRuntimeArtifacts_ClearsDispatchReservations pins the completion-path half of the F5
// cleanup wiring: a formula that completes holding a leaked sequential.slot must not carry it into the
// next formula. cleanupRuntimeArtifacts clears the whole reservation ledger under the agent's .runtime.
func TestCleanupRuntimeArtifacts_ClearsDispatchReservations(t *testing.T) {
	cwd := t.TempDir()
	dir := reservationDir(cwd, "http://127.0.0.1:1234")
	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, time.Now()) {
		t.Fatal("precondition: the claim should hold the slot")
	}

	cleanupRuntimeArtifacts(cwd)

	root := filepath.Join(cwd, ".runtime", "dispatch_admit_reservations")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("cleanupRuntimeArtifacts left the reservation ledger behind (stat err=%v); a completed "+
			"formula must not carry a leaked sequential.slot into the next (#669 F5)", err)
	}
}

// TestDispatchRefusalBreadcrumb owns #673 item 1's writer half: the record that makes the observer's
// counsel a RELAY rather than a second opinion. Before this, the gate refused and said nothing that
// outlived the process, so the PostToolUse observer had to reach its own verdict from its own operand
// — two components answering "is this backend full?", free to disagree with nothing comparing them.
//
// Everything asserted here is about what the gate LEAVES BEHIND. What the observer does with it is
// TestInterposeNonBlocking's business, and the split is deliberate: the two halves must be able to
// fail independently or the pair proves only that they agree with each other.
func TestDispatchRefusalBreadcrumb(t *testing.T) {
	breadcrumbPath := func(workDir string) string {
		return filepath.Join(workDir, ".runtime", dispatchLastRefusalName)
	}

	t.Run("a refusal records the verdict where a second process can read it", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeDeclaredBackendModels(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("fixture: the launch was not refused, so there is no breadcrumb to assert on:\n%s", out.String())
		}

		rec, ok := readLastRefusal(fx.workDir)
		if !ok {
			t.Fatal("a refusal left no readable breadcrumb; the observer has nothing to relay and the " +
				"counsel channel is silently dead (#673 AC-1)")
		}
		if rec.V != dispatchLastRefusalVersion {
			t.Errorf("record v = %d, want %d stamped by the writer", rec.V, dispatchLastRefusalVersion)
		}
		if rec.Backend != "http://127.0.0.1:1234" {
			t.Errorf("record backend = %q, want the refusing endpoint", rec.Backend)
		}
		if rec.Reason != "" {
			t.Errorf("record reason = %q, want the empty headroom reason", rec.Reason)
		}
		if rec.PoolTokens != 200000 {
			t.Errorf("record pool_tokens = %d, want 200000", rec.PoolTokens)
		}
		if rec.SummedTokens == nil || *rec.SummedTokens < 180000 {
			t.Errorf("record summed_tokens = %v, want the >= ceiling sum that refused", rec.SummedTokens)
		}
		// The gate's two records must agree, because an operator reading one and an agent counselled
		// from the other would otherwise be told different numbers about the same refusal.
		recs := dispatchInterventionRecords(t, fx.root, fx.agent)
		if len(recs) != 1 {
			t.Fatalf("one refusal wrote %d enforcement records, want exactly 1", len(recs))
		}
		if recs[0].PoolTokens == nil || *recs[0].PoolTokens != rec.PoolTokens {
			t.Errorf("the enforcement record says pool %v and the breadcrumb says %d", recs[0].PoolTokens, rec.PoolTokens)
		}
		if recs[0].SummedTokens == nil || rec.SummedTokens == nil || *recs[0].SummedTokens != *rec.SummedTokens {
			t.Errorf("the enforcement record says summed %v and the breadcrumb says %v", recs[0].SummedTokens, rec.SummedTokens)
		}

		// WriteFileAtomic's temp file must not survive. A reader globbing this directory would
		// otherwise meet a half-written sibling of the record it wants.
		entries, err := os.ReadDir(filepath.Dir(breadcrumbPath(fx.workDir)))
		if err != nil {
			t.Fatalf("read .runtime: %v", err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("the atomic write left %q behind", e.Name())
			}
		}
	})

	// The single-armer invariant, asserted at the surface that would break it. The latch is the
	// observer's episode discriminator; a gate that armed it would consume the episode before the
	// observer ever ran, and the counsel would then never be delivered at all.
	t.Run("the refusing gate does not arm the K17 fan-out latch", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeDeclaredBackendModels(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("fixture: the launch was not refused:\n%s", out.String())
		}
		if st := loadRecoveryState(fx.root, fx.agent); interventionLatchHolds(st, now) {
			t.Errorf("the gate armed the intervention latch (reason=%q); only the observer may arm it, "+
				"and a second armer makes one fan-out episode look like two", st.InterventionLatchReason)
		}
	})

	// #669 C1, carried into the breadcrumb. The cap is a semaphore of one: it refuses without
	// measuring tokens at all, so the field must be ABSENT rather than a ptr-to-0 that reads as
	// "measured, and the answer was zero". The relay depends on the distinction — it prints figures
	// only when there are figures.
	t.Run("a sequential-cap refusal records not-token-measured, not measured-zero", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeCapBackendModels(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 10, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore (first): %v", err)
		}
		if _, ok := readLastRefusal(fx.workDir); ok {
			t.Fatal("the ADMITTED first launch under the cap left a refusal breadcrumb; the observer " +
				"would counsel a session the gate never refused")
		}

		out.Reset()
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now.Add(time.Second)); err != nil {
			t.Fatalf("runDispatchAdmitCore (second): %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("fixture: the second launch under the cap was not refused:\n%s", out.String())
		}

		rec, ok := readLastRefusal(fx.workDir)
		if !ok {
			t.Fatal("the cap refusal left no readable breadcrumb")
		}
		if rec.Reason != reasonSequentialOnly {
			t.Errorf("record reason = %q, want %q", rec.Reason, reasonSequentialOnly)
		}
		if rec.SummedTokens != nil {
			t.Errorf("record summed_tokens = %d, want absent: the cap refused without measuring tokens",
				*rec.SummedTokens)
		}
		// Asserted on the bytes as well as the struct, because `null` and `0` decode into different
		// pointers but a reader of the FILE is what this distinction is for.
		raw, err := os.ReadFile(breadcrumbPath(fx.workDir))
		if err != nil {
			t.Fatalf("read breadcrumb: %v", err)
		}
		if !strings.Contains(string(raw), `"summed_tokens": null`) {
			t.Errorf("the on-disk record does not carry a null summed_tokens:\n%s", raw)
		}
	})

	// design-doc.md:104's swallowed write, stated as behaviour. The breadcrumb is the courtesy; the
	// deny and the enforcement record are the contract. A directory sitting on the record's path makes
	// every write fail, and the two halves that matter must be untouched by it.
	t.Run("a breadcrumb that cannot be written costs counsel, never correctness", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeDeclaredBackendModels(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)
		if err := os.MkdirAll(breadcrumbPath(fx.workDir), 0o755); err != nil {
			t.Fatalf("planting the obstruction: %v", err)
		}

		var out bytes.Buffer
		err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now)

		if err != nil {
			t.Errorf("an unwritable breadcrumb failed the hook: %v; ADR-007 says it exits 0", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Errorf("an unwritable breadcrumb suppressed the DENY, which is the half that stops the "+
				"launch:\n%s", out.String())
		}
		if recs := dispatchInterventionRecords(t, fx.root, fx.agent); len(recs) != 1 {
			t.Errorf("an unwritable breadcrumb cost the enforcement record too (%d written, want 1)", len(recs))
		}
		if _, ok := readLastRefusal(fx.workDir); ok {
			t.Error("readLastRefusal accepted a directory as a record")
		}
	})

	// The breadcrumb is scoped to the formula that earned it, exactly like the advisory ledger it
	// sits beside. Surviving formula completion would not merely be stale counsel — the observer
	// records the relay against the NEW formula's step id, so a refusal from the last minutes of one
	// formula would arrive misfiled against the next.
	t.Run("formula completion clears the refusal", func(t *testing.T) {
		dir := t.TempDir()
		writeLastRefusal(dir, "http://127.0.0.1:1234", tokenomics.BackendVerdict{
			Verdict: tokenomics.VerdictNoFit, PoolTokens: 400000, SummedTokens: 380000,
		}, time.Now())
		if _, ok := readLastRefusal(dir); !ok {
			t.Fatal("fixture: the breadcrumb did not land")
		}

		cleanupRuntimeArtifacts(dir)

		if _, ok := readLastRefusal(dir); ok {
			t.Error("a completed formula carried its refusal breadcrumb into the next one; the observer " +
				"would relay it against a step that never earned it")
		}
	})

	t.Run("an admitted launch leaves no refusal to relay", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeDeclaredBackendModels(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 20, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("fixture: the launch was refused:\n%s", out.String())
		}
		if _, ok := readLastRefusal(fx.workDir); ok {
			t.Error("an admitted launch left a refusal breadcrumb")
		}
	})
}

// TestDispatchRefusalRelay pins the observer's whole vocabulary as a pure function of the record. It
// is the arithmetic-free half of #673 AC-1: given the gate's own integers, the relay may only put them
// back in a sentence. A percentage, a ratio or a figure the record does not carry all mean a second
// verdict has been computed somewhere.
func TestDispatchRefusalRelay(t *testing.T) {
	summed := int64(380000)
	base := dispatchLastRefusal{
		V: dispatchLastRefusalVersion, TS: time.Now(), Backend: "http://127.0.0.1:1234",
		PoolTokens: 400000, SummedTokens: &summed,
	}

	t.Run("the headroom refusal restates the recorded figures", func(t *testing.T) {
		got := dispatchRefusalRelay(base)
		for _, want := range []string{"http://127.0.0.1:1234", "380000 of 400000 pool tokens", "one at a time"} {
			if !strings.Contains(got, want) {
				t.Errorf("relay missing %q: %s", want, got)
			}
		}
	})

	t.Run("the launcher's own floor breach keeps its distinct counsel", func(t *testing.T) {
		rec := base
		rec.Reason = reasonChildFloorHandoff
		got := dispatchRefusalRelay(rec)
		if !strings.Contains(got, "af handoff") {
			t.Errorf("a floor-breach relay must counsel `af handoff`: %s", got)
		}
		if strings.Contains(got, "one at a time") {
			t.Errorf("a floor breach was relayed as the sibling-contention serialize case: %s", got)
		}
	})

	t.Run("the sequential cap relays the switch and no figures it never measured", func(t *testing.T) {
		rec := base
		rec.Reason, rec.SummedTokens = reasonSequentialOnly, nil
		got := dispatchRefusalRelay(rec)
		if !strings.Contains(got, "AF_DISABLE_PARALLEL_SUBAGENTS") {
			t.Errorf("the cap relay does not name the operator switch that caused it: %s", got)
		}
		if strings.Contains(got, "400000") || strings.Contains(got, "pool tokens") {
			t.Errorf("the cap relay invented pool figures for a semaphore refusal: %s", got)
		}
	})

	// The vocabulary is CLOSED by this switch rather than trusted to its one writer. A record whose
	// reason this binary does not recognise is a record it cannot faithfully restate, and restating it
	// as the default headroom case would attach real-looking figures to an unknown verdict.
	t.Run("a reason outside the vocabulary is not relayed", func(t *testing.T) {
		rec := base
		rec.Reason = "some-future-reason"
		if got := dispatchRefusalRelay(rec); got != "" {
			t.Errorf("an unrecognised reason was relayed as %q, want silence", got)
		}
	})

	t.Run("a token-shaped reason with no tokens recorded is not relayed", func(t *testing.T) {
		for _, reason := range []string{"", reasonChildFloorHandoff} {
			rec := base
			rec.Reason, rec.SummedTokens = reason, nil
			if got := dispatchRefusalRelay(rec); got != "" {
				t.Errorf("reason %q with no summed_tokens was relayed as %q, want silence", reason, got)
			}
		}
	})

	// The mechanical form of "zero arithmetic" (design-doc.md:104): every figure the relay prints must
	// appear verbatim in the record it was given. A derived one would not.
	t.Run("nothing the relay prints is computed", func(t *testing.T) {
		got := dispatchRefusalRelay(base)
		for _, digits := range regexp.MustCompile(`[0-9]+`).FindAllString(got, -1) {
			if digits != "380000" && digits != "400000" && !strings.Contains(base.Backend, digits) {
				t.Errorf("the relay printed %q, which is in neither the record nor the backend key: %s", digits, got)
			}
		}
		if strings.Contains(got, "%") {
			t.Errorf("the relay printed a percentage, so something divided: %s", got)
		}
	})
}

// TestDispatchRefusalVocabularyIsClosed makes "closed vocabulary" a fact the compiler's test suite
// checks rather than a claim in a comment. Two halves, and both are needed:
//
//   - The vocabulary list must name every reason const declared in dispatch_admit.go. This half reads
//     the SOURCE, because a const added without a matching entry is exactly the change a reviewer
//     waves through — it compiles, every existing test passes, and the only symptom is that one class
//     of refusal silently stops producing counsel.
//   - Every member must render on BOTH surfaces. The arithmetic duplication #673 deleted is gone, but
//     the change left two hand-written prose renderings of one verdict; nothing else pins that they
//     stay in step.
func TestDispatchRefusalVocabularyIsClosed(t *testing.T) {
	t.Run("the vocabulary names every reason const in the file", func(t *testing.T) {
		src, err := os.ReadFile("dispatch_admit.go")
		if err != nil {
			t.Fatalf("read dispatch_admit.go: %v", err)
		}
		declared := regexp.MustCompile(`(?m)^const reason[A-Za-z]+ = "([^"]+)"`).FindAllStringSubmatch(string(src), -1)
		if len(declared) == 0 {
			t.Fatal("the const scan found nothing; the pattern has drifted from the source and this " +
				"test is now vacuous")
		}
		for _, m := range declared {
			if !slices.Contains(dispatchRefusalReasons, m[1]) {
				t.Errorf("reason %q is declared in dispatch_admit.go but absent from dispatchRefusalReasons; "+
					"add it there and give it a case in BOTH dispatchDenyReason and dispatchRefusalRelay, or "+
					"the observer will go silent for that class of refusal with nothing failing", m[1])
			}
		}
		// +1 for the headroom default, which is the empty string and so has no const to scan.
		if want := len(declared) + 1; len(dispatchRefusalReasons) != want {
			t.Errorf("dispatchRefusalReasons has %d members for %d declared reasons (want %d); a member "+
				"with no const behind it is a vocabulary entry nothing can produce",
				len(dispatchRefusalReasons), len(declared), want)
		}
	})

	t.Run("every reason renders on both surfaces", func(t *testing.T) {
		summed := int64(380000)
		for _, reason := range dispatchRefusalReasons {
			t.Run("reason "+strconv.Quote(reason), func(t *testing.T) {
				v := tokenomics.BackendVerdict{
					Verdict: tokenomics.VerdictNoFit, Reason: reason,
					PoolTokens: 400000, SummedTokens: 380000, ProjectedPct: 95, HeadroomPct: 90,
				}
				rec := dispatchLastRefusal{
					V: dispatchLastRefusalVersion, TS: time.Now(), Backend: "http://127.0.0.1:1234",
					Reason: reason, PoolTokens: 400000, SummedTokens: &summed,
				}
				if reason == reasonSequentialOnly {
					rec.SummedTokens = nil
				}
				deny, relay := dispatchDenyReason(rec.Backend, v), dispatchRefusalRelay(rec)
				if deny == "" {
					t.Fatal("dispatchDenyReason produced nothing for a reason in the vocabulary")
				}
				if relay == "" {
					t.Fatal("dispatchRefusalRelay produced nothing for a reason in the vocabulary; the " +
						"agent is refused and the next launch is never counselled about it")
				}
				// The two texts differ in tense and audience by design — one refuses a launch in
				// flight, the other reports a refusal already made — so they are pinned on the facts
				// an operator would compare, not on their wording.
				if strings.Contains(deny, "af handoff") != strings.Contains(relay, "af handoff") {
					t.Errorf("the deny and the relay disagree about whether to counsel `af handoff`:\ndeny:  %s\nrelay: %s", deny, relay)
				}
				if strings.Contains(deny, "AF_DISABLE_PARALLEL_SUBAGENTS") != strings.Contains(relay, "AF_DISABLE_PARALLEL_SUBAGENTS") {
					t.Errorf("the deny and the relay disagree about naming the cap switch:\ndeny:  %s\nrelay: %s", deny, relay)
				}
				// The #669 C1 distinction, asserted as agreement: whichever refusal carries no measured
				// tokens must carry none on EITHER surface.
				if strings.Contains(deny, "400000") != strings.Contains(relay, "400000") {
					t.Errorf("the deny and the relay disagree about whether this refusal measured tokens:\ndeny:  %s\nrelay: %s", deny, relay)
				}
			})
		}
	})
}
