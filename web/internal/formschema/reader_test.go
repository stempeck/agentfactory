package formschema

import (
	"context"
	"testing"
)

// fakeShower returns canned `af formula show <name> --json` output. Hermetic: no real af is
// ever spawned (mirrors the exec-package recording-fake idiom, runner_test.go:22-51).
type fakeShower struct {
	out string
	err error
}

func (f fakeShower) FormulaShowJSON(ctx context.Context, formula string) (string, error) {
	return f.out, f.err
}

var _ FormulaShower = fakeShower{}

// Captured verbatim from the live af-core contract (Dependency Inspector ground truth):
//   ./af formula show minimalworker --json  — one required input `task`, one deferred var.
const minimalworkerJSON = `{"name":"minimalworker","description":"Minimal directive executor.","type":"workflow","inputs":[{"name":"task","description":"The directive to execute","type":"string","required":true,"required_unless":null,"default":"","source":""}],"vars":[{"name":"orchestrator","description":"The agent that dispatched this worker (auto-injected by af sling)","type":"","required":false,"required_unless":null,"default":"","source":"deferred"}]}`

//   ./af formula show investigate --json  — no inputs, one required cli var `issue`.
const investigateJSON = `{"name":"investigate","description":"Investigate a codebase question","type":"workflow","inputs":[],"vars":[{"name":"issue","description":"What to investigate","type":"","required":true,"required_unless":null,"default":"","source":"cli"}]}`

// Synthetic payload exercising EVERY hidden source plus an optional input that carries Type +
// RequiredUnless — so the AC's "deferred/hook_bead/bead_* absent" wording is fully covered and
// the type/required_unless carry-through is proven.
const syntheticJSON = `{"name":"synthetic","description":"hidden-source coverage","type":"workflow","inputs":[{"name":"task","description":"the task","type":"string","required":true,"required_unless":null,"default":"","source":""},{"name":"alt","description":"alt input","type":"string","required":false,"required_unless":["task"],"default":"","source":""}],"vars":[{"name":"orchestrator","description":"d","type":"","required":false,"required_unless":null,"default":"","source":"deferred"},{"name":"litvar","description":"l","type":"","required":false,"required_unless":null,"default":"x","source":"literal"},{"name":"hookvar","description":"h","type":"","required":false,"required_unless":null,"default":"","source":"hook_bead"},{"name":"btitle","description":"bt","type":"","required":false,"required_unless":null,"default":"","source":"bead_title"},{"name":"bdesc","description":"bd","type":"","required":false,"required_unless":null,"default":"","source":"bead_description"},{"name":"clivar","description":"c","type":"","required":false,"required_unless":null,"default":"","source":"cli"},{"name":"envvar","description":"e","type":"","required":false,"required_unless":null,"default":"","source":"env"}]}`

// --- Primary (AC-1) fixtures, shaped after the three real agents the AC names. Each mirrors the
// live `af formula show <name> --json` contract (formulaField JSON shape) for one of the three
// effective-bind mechanisms. ---

// rapid-soldesign-plan: a workflow with FOUR required inputs, but only `issue_uri` lacks a default
// (the others default to downstream formula names). NO field named `issue`. ⇒ mechanism 2 (input
// bridge): the single unsatisfied required input is `issue_uri`. Primary == "issue_uri".
const rapidSoldesignJSON = `{"name":"rapid-soldesign-plan","description":"plan","type":"workflow","inputs":[{"name":"issue_uri","description":"the issue","type":"string","required":true,"required_unless":null,"default":"","source":""},{"name":"analyst_name","description":"a","type":"string","required":true,"required_unless":null,"default":"rootcause-all","source":""},{"name":"designer_name","description":"d","type":"string","required":true,"required_unless":null,"default":"design-v7","source":""},{"name":"impl_name","description":"i","type":"string","required":true,"required_unless":null,"default":"design-plan-impl","source":""}],"vars":[{"name":"orchestrator","description":"o","type":"","required":false,"required_unless":null,"default":"","source":"deferred"}]}`

// rootcause-all: the majority vars-only `issue` shape — `[vars.issue] required source=cli`, NO
// inputs. ⇒ mechanism 1 (assignment-bead path): a user-providable required field named `issue`.
// Primary == "issue". (This is the C1 trap: an inputs-only scan would return "" and orphan the task.)
const rootcauseAllJSON = `{"name":"rootcause-all","description":"rca","type":"workflow","inputs":[],"vars":[{"name":"issue","description":"what to fix","type":"","required":true,"required_unless":null,"default":"","source":"cli"}]}`

// design-v7: `[vars.issue] required source=hook_bead`, NO inputs. The hook_bead source is HIDDEN by
// isUserProvidableSource, so `issue` never enters the user-providable set ⇒ mechanism 1 cannot fire
// and there is no input to bridge ⇒ mechanism 3. Primary == "" (frontend renders a synthetic box).
const designV7JSON = `{"name":"design-v7","description":"design","type":"workflow","inputs":[],"vars":[{"name":"issue","description":"the issue","type":"","required":true,"required_unless":null,"default":"","source":"hook_bead"}]}`

func fieldByName(fields []Field, name string) (Field, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// TestForm_FromRawTOML_PreservesRequired — AC-1. Required inputs are marked required; every
// auto-sourced (identity-bearing) var is absent from the generated form (INV-2).
func TestForm_FromRawTOML_PreservesRequired(t *testing.T) {
	ctx := context.Background()

	// --- minimalworker: required input surfaced; deferred var hidden ---
	sc, err := New(fakeShower{out: minimalworkerJSON}).Read(ctx, "minimalworker")
	if err != nil {
		t.Fatalf("Read(minimalworker): %v", err)
	}
	task, ok := fieldByName(sc.Fields, "task")
	if !ok {
		t.Fatalf("minimalworker form is missing the required input 'task': %+v", sc.Fields)
	}
	if !task.Required {
		t.Errorf("'task' should be Required (it is `[inputs.task] required=true`)")
	}
	if task.Type != "string" {
		t.Errorf("'task' Type = %q, want \"string\" (Type must be carried through)", task.Type)
	}
	if _, leaked := fieldByName(sc.Fields, "orchestrator"); leaked {
		t.Errorf("INV-2: 'orchestrator' (source=deferred) must be hidden, but it is present")
	}

	// --- synthetic: keep source ∈ {cli,env,""}; hide the other five; carry Type/RequiredUnless ---
	sc2, err := New(fakeShower{out: syntheticJSON}).Read(ctx, "synthetic")
	if err != nil {
		t.Fatalf("Read(synthetic): %v", err)
	}
	for _, keep := range []string{"task", "alt", "clivar", "envvar"} {
		if _, ok := fieldByName(sc2.Fields, keep); !ok {
			t.Errorf("user-providable field %q should be surfaced, but it is absent", keep)
		}
	}
	for _, hide := range []string{"orchestrator", "litvar", "hookvar", "btitle", "bdesc"} {
		if _, ok := fieldByName(sc2.Fields, hide); ok {
			t.Errorf("auto-sourced field %q must be hidden, but it is present", hide)
		}
	}
	alt, _ := fieldByName(sc2.Fields, "alt")
	if len(alt.RequiredUnless) != 1 || alt.RequiredUnless[0] != "task" {
		t.Errorf("'alt' RequiredUnless = %v, want [task] (RequiredUnless must be carried through)", alt.RequiredUnless)
	}
	if alt.Type != "string" {
		t.Errorf("'alt' Type = %q, want \"string\"", alt.Type)
	}

	// --- investigate: a user-providable cli VAR surfaces and preserves required ---
	sc3, err := New(fakeShower{out: investigateJSON}).Read(ctx, "investigate")
	if err != nil {
		t.Fatalf("Read(investigate): %v", err)
	}
	issue, ok := fieldByName(sc3.Fields, "issue")
	if !ok {
		t.Fatalf("investigate form is missing the user-providable cli var 'issue': %+v", sc3.Fields)
	}
	if !issue.Required {
		t.Errorf("'issue' should be Required (`[vars.issue] required=true source=cli`)")
	}

	// --- required-first ordering: a required field never follows an optional one ---
	seenOptional := false
	for _, f := range sc2.Fields {
		if !f.Required {
			seenOptional = true
		} else if seenOptional {
			t.Errorf("required-first violated: required field %q follows an optional field", f.Name)
		}
	}
}

// TestForm_Primary_EffectiveBind — AC-1. Schema.Primary is the EFFECTIVE CLI bind target — the
// single field the operator's primary text should bind to — computed server-side over the
// user-providable field set via the effective-bind 3-way rule (NOT inputs-only). Mechanism 1
// (assignment-bead path): a user-providable REQUIRED field literally named "issue" (var OR input)
// ⇒ Primary = "issue". Mechanism 2 (input bridge, workflow optimization): else the SINGLE
// unsatisfied required INPUT (Kind=="input" && Required && Default=="" && len(RequiredUnless)==0)
// ⇒ that input's name. Mechanism 3: else Primary = "" (the frontend renders a synthetic task box).
func TestForm_Primary_EffectiveBind(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		payload string
		want    string
		reason  string
	}{
		// The three agents the AC names explicitly:
		{"rapid-soldesign-plan", rapidSoldesignJSON, "issue_uri", "mechanism 2: single unsatisfied required input"},
		{"rootcause-all", rootcauseAllJSON, "issue", "mechanism 1: vars-only required cli `issue`"},
		{"design-v7", designV7JSON, "", "mechanism 3: hook-sourced `issue` hidden -> synthetic"},
		// Existing fixtures cover the same mechanisms from another angle:
		{"investigate", investigateJSON, "issue", "mechanism 1: required cli `issue` var"},
		{"minimalworker", minimalworkerJSON, "task", "mechanism 2: single required input `task`"},
		{"synthetic", syntheticJSON, "task", "mechanism 2: only `task` is required, default-less, unconditional"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := New(fakeShower{out: tc.payload}).Read(ctx, tc.name)
			if err != nil {
				t.Fatalf("Read(%s): %v", tc.name, err)
			}
			if sc.Primary != tc.want {
				t.Errorf("Primary = %q, want %q (%s)", sc.Primary, tc.want, tc.reason)
			}
		})
	}

	// design-v7 corollary: the hook-sourced `issue` must NOT surface as a field (INV-2) — that
	// hiding is exactly WHY mechanism 1 cannot fire and Primary == "".
	sc, err := New(fakeShower{out: designV7JSON}).Read(ctx, "design-v7")
	if err != nil {
		t.Fatalf("Read(design-v7): %v", err)
	}
	if _, leaked := fieldByName(sc.Fields, "issue"); leaked {
		t.Errorf("design-v7: hook_bead `issue` must be hidden (INV-2), but it surfaced; Primary=%q", sc.Primary)
	}
}

// The reader branches on the .state error envelope, never on the (always-0) exit code.
func TestForm_ErrorEnvelope_IsAnError(t *testing.T) {
	_, err := New(fakeShower{out: `{"state":"error","error":"formula \"x\" not found in search paths"}`}).
		Read(context.Background(), "x")
	if err == nil {
		t.Fatalf("an {\"state\":\"error\"} envelope must yield an error, not a valid schema")
	}
}

// An upstream transport error from the shower is surfaced, not swallowed.
func TestForm_ShowerError_IsSurfaced(t *testing.T) {
	_, err := New(fakeShower{err: context.DeadlineExceeded}).Read(context.Background(), "x")
	if err == nil {
		t.Fatalf("a shower transport error must be surfaced")
	}
}
