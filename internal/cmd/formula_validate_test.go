package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// invokeFormulaValidate pipes toml to formulaValidateCmd's stdin, runs runFormulaValidate, and
// returns the parsed envelope. runFormulaValidate must ALWAYS return nil (the read-verb exit-0
// contract, mirrors runFormulaShow): a rejecting verdict is encoded in the payload, never the exit
// code, so Phase 3's PUT handler can tell a 422 (validate reject) from a 503 (busy) by the body.
func invokeFormulaValidate(t *testing.T, toml string) validateOutput {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		formulaValidateCmd.SetContext(t.Context())
		formulaValidateCmd.SetIn(strings.NewReader(toml))
		runErr = runFormulaValidate(formulaValidateCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("runFormulaValidate returned %v, want nil (always exit 0)", runErr)
	}
	var got validateOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	return got
}

// TestRunFormulaValidate_Lamps is the composed-verdict table: the classifier maps each engine-of-record
// failure class to its lamp (parse/ids/needs/cycles) and every case exits 0. This is the tightest guard
// on the lamp classifier the IMPLREADME leaves open (Gotcha 6) and on the success {ok:true,findings:[]}
// shape (Gotcha 2). No factory on disk is needed — the TOML is piped on stdin.
func TestRunFormulaValidate_Lamps(t *testing.T) {
	cases := []struct {
		name     string
		toml     string
		wantOK   bool
		wantLamp string // "" when wantOK
	}{
		{
			name:     "cyclic expansion -> cycles (AC #1)",
			toml:     "formula = \"x\"\ntype = \"expansion\"\n[[template]]\nid=\"a\"\nneeds=[\"b\"]\n[[template]]\nid=\"b\"\nneeds=[\"a\"]\n",
			wantOK:   false,
			wantLamp: "cycles",
		},
		{
			name:   "valid explicit DAG -> ok (AC #2)",
			toml:   "formula = \"x\"\ntype = \"expansion\"\n[[template]]\nid=\"a\"\n[[template]]\nid=\"b\"\nneeds=[\"a\"]\n",
			wantOK: true,
		},
		{
			name:     "template missing id -> ids",
			toml:     "formula = \"x\"\ntype = \"expansion\"\n[[template]]\ntitle=\"no id\"\n",
			wantOK:   false,
			wantLamp: "ids",
		},
		{
			name:     "needs unknown template -> needs",
			toml:     "formula = \"x\"\ntype = \"expansion\"\n[[template]]\nid=\"a\"\nneeds=[\"ghost\"]\n",
			wantOK:   false,
			wantLamp: "needs",
		},
		{
			name:     "undecodable TOML -> parse",
			toml:     "[[[[not valid toml",
			wantOK:   false,
			wantLamp: "parse",
		},
		{
			name:   "omitted type is inferred as expansion -> ok (store-formula contract)",
			toml:   "formula = \"x\"\n[[template]]\nid=\"a\"\n[[template]]\nid=\"b\"\nneeds=[\"a\"]\n",
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := invokeFormulaValidate(t, tc.toml)
			if got.OK != tc.wantOK {
				t.Fatalf("ok = %v, want %v (findings=%+v)", got.OK, tc.wantOK, got.Findings)
			}
			if tc.wantOK {
				if len(got.Findings) != 0 {
					t.Fatalf("a valid formula must have empty findings, got %+v", got.Findings)
				}
				return
			}
			if len(got.Findings) == 0 {
				t.Fatalf("an invalid formula must carry >=1 finding")
			}
			if got.Findings[0].Lamp != tc.wantLamp {
				t.Fatalf("lamp = %q, want %q (msg=%q)", got.Findings[0].Lamp, tc.wantLamp, got.Findings[0].Message)
			}
			if got.Findings[0].Message == "" {
				t.Fatalf("finding message must be non-empty")
			}
		})
	}
}

// TestRunFormulaValidate_SuccessEnvelopeShape pins the exact success payload {"ok":true,"findings":[]}
// — findings must marshal as an empty ARRAY, not null (make([]..., 0), not a nil slice).
func TestRunFormulaValidate_SuccessEnvelopeShape(t *testing.T) {
	toml := "formula = \"x\"\ntype = \"expansion\"\n[[template]]\nid=\"a\"\n"
	var out string
	captured := captureStdout(t, func() {
		formulaValidateCmd.SetContext(t.Context())
		formulaValidateCmd.SetIn(strings.NewReader(toml))
		if err := runFormulaValidate(formulaValidateCmd, nil); err != nil {
			t.Fatalf("runFormulaValidate: %v", err)
		}
	})
	out = strings.TrimSpace(captured)
	if out != `{"ok":true,"findings":[]}` {
		t.Fatalf("success envelope = %q, want %q", out, `{"ok":true,"findings":[]}`)
	}
}

// ---- T4 (PRRT_kwDORt0n_M6Pw23Y): the verb must not hand-copy the engine's type inference ----

// TestFormulaValidate_NoLocalInferenceCopy — `af formula validate` is the engine of record at the
// write boundary (design-doc.md:169). A private copy of the engine's unexported inference means a
// 5th formula type added to internal/formula would silently not be inferred by the verb, and thus
// not by the console's save gate. Nothing catches that today: parity_test.go's composedVerdict runs
// the engine's own Parse from inside package formula and structurally cannot reach this copy.
//
// The remedy (decision D13) is a single source of truth — formula.InferType — not a drift alarm.
// This source-scan pins the copy's absence so a new one cannot be reintroduced. See D14 (amended)
// for why the RED is the copy's presence rather than a compile failure.
func TestFormulaValidate_NoLocalInferenceCopy(t *testing.T) {
	src, err := os.ReadFile("formula_validate.go")
	if err != nil {
		t.Fatalf("read formula_validate.go: %v", err)
	}
	if strings.Contains(string(src), "func inferFormulaType") {
		t.Error("formula_validate.go still declares func inferFormulaType — the engine's exported inference " +
			"(formula.InferType) must be the single source of truth, or a 5th formula type will be inferred " +
			"by the engine and silently missed by the verb")
	}
	if !strings.Contains(string(src), "InferType()") {
		t.Error("formula_validate.go does not call the engine's exported InferType() — the composed verdict " +
			"must infer the type the same way formula.Parse does")
	}
}

// TestFormulaValidate_InfersEveryTypeLikeTheEngine — T4 (PRRT_kwDORt0n_M6Pw23Y), the drift guard.
//
// Each fixture OMITS `type` on purpose: 28 of the 29 store formulas do, and an explicit `type` would
// make InferType early-return, leaving the inference untested. If the verb ever stops using the
// engine's inference, Type stays "" and Validate rejects the formula — so ok:false here means the
// verb and the engine have drifted apart.
//
// This is the future-facing half of T4. The structural half is that exactly one implementation of the
// inference now exists (formula.InferType); TestFormulaValidate_NoLocalInferenceCopy pins that a
// second one is not reintroduced.
func TestFormulaValidate_InfersEveryTypeLikeTheEngine(t *testing.T) {
	cases := []struct {
		name string
		toml string
	}{
		{"workflow (inferred from [[steps]])", "formula = \"infer-workflow\"\n\n[[steps]]\nid = \"plan\"\ntitle = \"Plan\"\n"},
		{"convoy (inferred from [[legs]])", "formula = \"infer-convoy\"\n\n[[legs]]\nid = \"left\"\ntitle = \"Left\"\n\n[[legs]]\nid = \"right\"\ntitle = \"Right\"\n"},
		{"expansion (inferred from [[template]])", "formula = \"infer-expansion\"\n\n[[template]]\nid = \"seed\"\ntitle = \"Seed\"\n"},
		{"aspect (inferred from [[aspects]])", "formula = \"infer-aspect\"\n\n[[aspects]]\nid = \"security\"\ntitle = \"Security\"\n\n[[aspects]]\nid = \"perf\"\ntitle = \"Perf\"\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := invokeFormulaValidate(t, c.toml)
			if !out.OK {
				t.Errorf("verb rejected a type-inferrable formula (findings=%+v) — the verb's inference has "+
					"drifted from formula.InferType, so a formula omitting `type` no longer validates", out.Findings)
			}
		})
	}
}
