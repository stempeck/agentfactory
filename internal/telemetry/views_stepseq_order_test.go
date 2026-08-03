package telemetry

import (
	"strings"
	"testing"
)

// TestOrderingViewsCastStepSeqNumerically pins the SQL half of the step-ordering fix.
//
// The encoder now emits af.step_seq as an OTLP integer, but OTLP/JSON carries 64-bit integers
// as decimal strings, so whether the backend materialises the column as a numeric type is a
// property of its ingest that this repository cannot observe — the pinned binary is not
// installed in the test environment and no CI lane can stand one up.
//
// The cast removes that dependency. It is correct under either ingest typing, it is verifiable
// here by inspection, and it is the only half that fixes the emitted VALUE rather than just the
// row order: in the per-step token view the ordering key is produced by max() in a CTE and
// consumed by min() in the outer select, and over strings those two move in opposite directions.
//
// Only the two views that actually order on the column are asserted. Three others compute
// max(af_step_seq) inside their step_windows CTE but never order on it or emit it, so the defect
// is latent there and casting them would be a change no review comment asked for.
// Expectations are written lowercase because shippedView.sql() lowercases every query before
// returning it, which is the convention the sibling contract tests in this package rely on.
func TestOrderingViewsCastStepSeqNumerically(t *testing.T) {
	for _, tc := range []struct {
		view        string
		mustHave    []string
		mustNotHave []string
	}{
		{
			view: "waterfall",
			mustHave: []string{
				"cast(af_step_seq as bigint) as step_order",
				"order by service_af_formula_instance, cast(af_step_seq as bigint)",
			},
			// The bare column must not survive as the ordering key. Matching the ORDER BY
			// clause specifically, because the CAST expression legitimately contains the
			// column name.
			mustNotHave: []string{"order by service_af_formula_instance, af_step_seq"},
		},
		{
			view:        "agent-model-step-tokens",
			mustHave:    []string{"max(cast(af_step_seq as bigint)) as step_order"},
			mustNotHave: []string{"max(af_step_seq)"},
		},
	} {
		t.Run(tc.view, func(t *testing.T) {
			sql := loadView(t, tc.view).sql()
			for _, want := range tc.mustHave {
				if !strings.Contains(sql, want) {
					t.Errorf("%s.json does not order on a numeric step sequence: expected to find\n  %s\n"+
						"Without the cast a string column sorts lexicographically and step 10 renders "+
						"before step 2 for any formula with ten or more steps", tc.view, want)
				}
			}
			for _, bad := range tc.mustNotHave {
				if strings.Contains(sql, bad) {
					t.Errorf("%s.json still uses the uncast column:\n  %s", tc.view, bad)
				}
			}
		})
	}
}

// TestLatentStepSeqViewsStillParse is the deliberate counterpart: the three views that were NOT
// changed must still load and must still carry their step_windows CTE. An assertion that they
// are untouched is what makes "latent, left alone" a checked claim rather than an assumption.
func TestLatentStepSeqViewsStillParse(t *testing.T) {
	for _, name := range []string{"reconciliation", "overhead-buckets", "zero-join-canary"} {
		t.Run(name, func(t *testing.T) {
			sql := loadView(t, name).sql()
			if !strings.Contains(sql, "max(af_step_seq)") {
				t.Errorf("%s.json no longer computes max(af_step_seq): this view was intended to "+
					"be left unchanged, since it never orders on or emits the value. If it was "+
					"changed deliberately, the change needs a review thread to serve", name)
			}
			if !strings.Contains(sql, "step_windows") {
				t.Errorf("%s.json lost its step_windows CTE", name)
			}
		})
	}
}
