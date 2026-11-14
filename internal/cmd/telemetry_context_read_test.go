package cmd

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

func TestStepBoundJudgedAgainstSessionWindow(t *testing.T) {
	t.Run("telemetry judges min of bound and window", func(t *testing.T) {
		i64 := func(v int64) *int64 { return &v }
		pair := stepPair{
			start: &telemetry.StepEvent{SessionID: "sess-1", CumTokens: i64(10000)},
			end: &telemetry.StepEvent{
				SessionID:      "sess-1",
				CumTokens:      i64(260000),
				CtxTokensUsed:  i64(150000),
				CtxTokensTotal: i64(200000),
				CtxBoundTokens: 1000000,
			},
		}
		f := deriveStepContext(pair, 180)
		if f.cumTokensDelta == nil || *f.cumTokensDelta != 250000 {
			t.Fatalf("fixture: cum_tokens_delta = %v, want 250000", f.cumTokensDelta)
		}
		if f.overConsumption == nil || !*f.overConsumption {
			t.Errorf("a 250000-token step in a 200000 session window is over its bound whatever the operator's 1000000; overConsumption = %v", f.overConsumption)
		}
		if f.overOccupancy == nil || *f.overOccupancy {
			t.Errorf("150000 held of a 200000 window is not over; overOccupancy = %v", f.overOccupancy)
		}
		if f.ctxBoundTokens == nil || *f.ctxBoundTokens != 1000000 {
			t.Errorf("the stored bound must stay the operator's absolute 1000000, got %v", f.ctxBoundTokens)
		}
		if f.boundDrift == nil || !*f.boundDrift {
			t.Errorf("a bound above the window still reports drift, got %v", f.boundDrift)
		}
	})

	t.Run("detector announces bound once", func(t *testing.T) {
		f := newWDFixture(t, "alpha")
		wdWriteJSON(t, config.StartupConfigPath(f.root), map[string]any{"step_context": map[string]any{"bound_tokens": 1000000}})
		launched := time.Now().Add(-time.Hour).Truncate(time.Second)
		wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, 200000, wdOld, launched))
		wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)

		f.pass(time.Now(), "alpha")
		f.requireMailCount(t, 1, "bound 1000000 above the applied 200000")
		want := fmt.Sprintf(`agent "alpha": step_context.bound_tokens is 1000000, above the 200000-token window Claude Code %s applied to model %q on profile %q; steps are judged against 200000.`, wdOld, wdModel, wdProfile)
		for _, m := range f.mail.sent {
			if strings.TrimSpace(m.body) != want {
				t.Errorf("K6 body = %q, want %q", m.body, want)
			}
			if m.subject != "WINDOW_MISMATCH: alpha on profile "+wdProfile {
				t.Errorf("K6 subject = %q, want the one K5 subject", m.subject)
			}
		}
		rec, ok := wdEntry(t, f.root, wdProfile, wdKey(wdOld, 200000, wdModel))
		found := false
		for _, p := range rec.BoundNotifiedFor {
			found = found || p.Bound == 1000000 && p.Applied == 200000
		}
		if !ok || !found {
			t.Errorf("bound_notified_for must hold (1000000, 200000); exists=%v rec=%+v", ok, rec)
		}

		f.pass(time.Now().Add(time.Minute), "alpha")
		f.requireMailCount(t, 1, "a second run over the same (bound, applied) pair")
	})
}
