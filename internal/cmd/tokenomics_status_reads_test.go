package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// TestTokenomicsStatusReadsRealFigures pins the two readers #668 K10 wired under `af tokenomics
// status` — learned coverage and the intervention tail. Both replaced hardcoded zeroes, and the
// comment above them now claims the zeroes are MEASURED. Nothing else in the tree ever seeds a
// digest or an intervention record and then asks status about it, so without this the whole surface
// reverts to its placeholders with the suite green — and the claim that it is measured would be the
// falsehood the contract section exists to make impossible.
//
// The four properties below are the ones a reversion breaks, in the order they would break: the
// count is real, the empty case is honest about WHY, the tail is sorted ACROSS agents before it is
// truncated, and it keeps the newest firings rather than the oldest.
func TestTokenomicsStatusReadsRealFigures(t *testing.T) {
	const formula = "offpath"

	statusJSON := func(t *testing.T) tokenomicsStatusJSON {
		t.Helper()
		enableTokenomicsJSON(t)
		out, err := runTokenomicsArgs(t)
		if err != nil {
			t.Fatalf("tokenomics --json: %v", err)
		}
		var dto tokenomicsStatusJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		return dto
	}

	// Written through the SHIPPED writer, so the fixture cannot describe a digest the factory would
	// never produce — the same rule TestBandReport's seeder follows.
	seedDigest := func(t *testing.T, root string, keys ...tokenomics.DigestKey) {
		t.Helper()
		d := tokenomics.NewDigest()
		for i, k := range keys {
			d.Put(k, tokenomics.Aggregate{Runs: 3 + i, MedianPeakCtxTokens: int64(90_000 + i)})
		}
		dir := config.TelemetryDir(root)
		if err := os.MkdirAll(telemetry.LearnedDigestDir(dir), 0o755); err != nil {
			t.Fatalf("mkdir digest dir: %v", err)
		}
		if err := tokenomics.SaveDigest(telemetry.LearnedDigestPath(dir, formula), d); err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}
	}

	seedIntervention := func(t *testing.T, root, agent, ts, mechanism, action, step string) {
		t.Helper()
		err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
			TS: ts, Agent: agent, Formula: formula, InstanceID: "af-668-1",
			StepID: step, Mechanism: mechanism, Action: action,
		})
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	t.Run("learned coverage counts the aggregates the digest actually holds", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedDigest(t, root,
			tokenomics.DigestKey{Formula: formula, StepID: "s-1", Model: "lmstudio"},
			tokenomics.DigestKey{Formula: formula, StepID: "s-2", Model: "lmstudio"},
			tokenomics.DigestKey{Formula: formula, StepID: "s-2", Model: "claude-opus-5"},
		)

		dto := statusJSON(t)
		if dto.LearnedCoverage.Aggregates != 3 {
			t.Errorf("aggregates = %d, want 3. A hardcoded zero here reports a factory that has "+
				"learned three keys as one that has learned nothing, which is the collapse the "+
				"unavailable_because field exists to prevent", dto.LearnedCoverage.Aggregates)
		}
		if dto.LearnedCoverage.UnavailableBecause != "" {
			t.Errorf("unavailable_because = %q on a factory WITH learned data; the reason is carried "+
				"exactly when the count is zero", dto.LearnedCoverage.UnavailableBecause)
		}
	})

	t.Run("a cold start reports zero WITH a reason, not a bare zero", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)

		dto := statusJSON(t)
		if dto.LearnedCoverage.Aggregates != 0 {
			t.Errorf("aggregates = %d on a factory with no digest, want 0", dto.LearnedCoverage.Aggregates)
		}
		if dto.LearnedCoverage.UnavailableBecause == "" {
			t.Error("a zero with no reason reads as measured-and-empty, which is indistinguishable " +
				"from a factory that was never asked")
		}
		if dto.RecentInterventions.UnavailableBecause == "" {
			t.Error("the intervention tail is empty and says nothing about why")
		}
		if len(dto.RecentInterventions.Events) != 0 {
			t.Errorf("events = %v on a factory where nothing has fired", dto.RecentInterventions.Events)
		}
	})

	t.Run("the tail is the factory's newest firings, not the last agent's", func(t *testing.T) {
		// The roster walk visits one agent's whole log before the next's, so a tail taken off that
		// order is "the last agent's last firings". manager's records are all NEWER than supervisor's
		// and are seeded FIRST, so an unsorted truncation keeps supervisor's — every line wrong.
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		for _, s := range []struct{ agent, ts, mechanism, action, step string }{
			{"manager", "2026-08-31T09:00:06Z", "thrift", telemetry.ActionAdvise, "s-6"},
			{"manager", "2026-08-31T09:00:07Z", "dispatch", telemetry.ActionAdvise, "s-7"},
			{"manager", "2026-08-31T09:00:08Z", "budget", telemetry.ActionHandoff, "s-8"},
			{"supervisor", "2026-08-31T09:00:01Z", "effort", telemetry.ActionAdvise, "s-1"},
			{"supervisor", "2026-08-31T09:00:02Z", "effort", telemetry.ActionReduceEffort, "s-2"},
			{"supervisor", "2026-08-31T09:00:03Z", "thrift", telemetry.ActionAdvise, "s-3"},
			{"supervisor", "2026-08-31T09:00:04Z", "budget", telemetry.ActionAdvise, "s-4"},
		} {
			seedIntervention(t, root, s.agent, s.ts, s.mechanism, s.action, s.step)
		}

		got := statusJSON(t).RecentInterventions.Events
		want := []string{
			"2026-08-31T09:00:03Z supervisor thrift: advise [step s-3]",
			"2026-08-31T09:00:04Z supervisor budget: advise [step s-4]",
			"2026-08-31T09:00:06Z manager thrift: advise [step s-6]",
			"2026-08-31T09:00:07Z manager dispatch: advise [step s-7]",
			"2026-08-31T09:00:08Z manager budget: handoff [step s-8]",
		}
		if len(got) != tokenomicsInterventionTailLines {
			t.Fatalf("len(events) = %d, want %d — seven records were seeded and the tail is capped",
				len(got), tokenomicsInterventionTailLines)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("events[%d] = %q, want %q. Oldest-first across the WHOLE roster: an unsorted "+
					"tail keeps whichever agent the walk visited last, and a head-truncation keeps the "+
					"firings the operator has already seen", i, got[i], want[i])
			}
		}
	})

	t.Run("a record that is not an intervention is not a firing", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		// Mechanism is stamped on this step_end deliberately. It lives on the shared StepEvent, so a
		// record can carry one without being a firing — and a tail that keyed only on the field being
		// non-empty would report a closed step as an intervention the day any writer starts setting it.
		if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-08-31T09:00:09Z", Agent: "manager", Formula: formula,
			InstanceID: "af-668-1", StepID: "s-9", Status: telemetry.StatusClosed,
			Mechanism: string(tokenomics.MechanismBudget),
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		seedIntervention(t, root, "manager", "2026-08-31T09:00:10Z", "thrift", telemetry.ActionAdvise, "")

		got := statusJSON(t).RecentInterventions.Events
		if len(got) != 1 {
			t.Fatalf("events = %v, want exactly the one intervention; a closed step is not a firing "+
				"and counting it would inflate every quiet factory's tail", got)
		}
		// No step suffix: the step id is genuinely absent, and printing "[step ]" would spell a fact
		// the record does not carry.
		if want := "2026-08-31T09:00:10Z manager thrift: advise"; got[0] != want {
			t.Errorf("events[0] = %q, want %q", got[0], want)
		}
	})

	t.Run("the human surface renders the same two figures", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedDigest(t, root, tokenomics.DigestKey{Formula: formula, StepID: "s-1", Model: "lmstudio"})
		seedIntervention(t, root, "manager", "2026-08-31T09:00:11Z", "thrift", telemetry.ActionAdvise, "s-1")

		out, err := runTokenomicsArgs(t)
		if err != nil {
			t.Fatalf("tokenomics status: %v", err)
		}
		if !strings.Contains(out, "learned coverage: 1 aggregates") {
			t.Errorf("status does not render the real coverage figure; got:\n%s", out)
		}
		if !strings.Contains(out, "2026-08-31T09:00:11Z manager thrift: advise [step s-1]") {
			t.Errorf("status does not render the intervention tail; got:\n%s", out)
		}
	})
}
