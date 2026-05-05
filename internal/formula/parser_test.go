package formula

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse_Workflow(t *testing.T) {
	data := []byte(`
description = "Test workflow"
formula = "test-workflow"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "First Step"
description = "Do the first thing"

[[steps]]
id = "step2"
title = "Second Step"
description = "Do the second thing"
needs = ["step1"]

[[steps]]
id = "step3"
title = "Third Step"
description = "Do the third thing"
needs = ["step2"]

[vars]
[vars.feature]
description = "The feature to implement"
required = true
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if f.Name != "test-workflow" {
		t.Errorf("Name = %q, want %q", f.Name, "test-workflow")
	}
	if f.Type != TypeWorkflow {
		t.Errorf("Type = %q, want %q", f.Type, TypeWorkflow)
	}
	if len(f.Steps) != 3 {
		t.Errorf("len(Steps) = %d, want 3", len(f.Steps))
	}
	if f.Steps[1].Needs[0] != "step1" {
		t.Errorf("step2.Needs[0] = %q, want %q", f.Steps[1].Needs[0], "step1")
	}
}

func TestParse_Convoy(t *testing.T) {
	data := []byte(`
description = "Test convoy"
formula = "test-convoy"
type = "convoy"
version = 1

[[legs]]
id = "leg1"
title = "Leg One"
focus = "Focus area 1"
description = "First leg"

[[legs]]
id = "leg2"
title = "Leg Two"
focus = "Focus area 2"
description = "Second leg"

[synthesis]
title = "Synthesis"
description = "Combine results"
depends_on = ["leg1", "leg2"]
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if f.Name != "test-convoy" {
		t.Errorf("Name = %q, want %q", f.Name, "test-convoy")
	}
	if f.Type != TypeConvoy {
		t.Errorf("Type = %q, want %q", f.Type, TypeConvoy)
	}
	if len(f.Legs) != 2 {
		t.Errorf("len(Legs) = %d, want 2", len(f.Legs))
	}
	if f.Synthesis == nil {
		t.Fatal("Synthesis is nil")
	}
	if len(f.Synthesis.DependsOn) != 2 {
		t.Errorf("len(Synthesis.DependsOn) = %d, want 2", len(f.Synthesis.DependsOn))
	}
}

func TestParse_Expansion(t *testing.T) {
	data := []byte(`
description = "Test expansion"
formula = "test-expansion"
type = "expansion"
version = 1

[[template]]
id = "{target}.draft"
title = "Draft: {target.title}"
description = "Initial draft"

[[template]]
id = "{target}.refine"
title = "Refine"
description = "Refine the draft"
needs = ["{target}.draft"]
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if f.Name != "test-expansion" {
		t.Errorf("Name = %q, want %q", f.Name, "test-expansion")
	}
	if f.Type != TypeExpansion {
		t.Errorf("Type = %q, want %q", f.Type, TypeExpansion)
	}
	if len(f.Template) != 2 {
		t.Errorf("len(Template) = %d, want 2", len(f.Template))
	}
}

func TestValidate_MissingName(t *testing.T) {
	data := []byte(`
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step"
`)

	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for missing formula name")
	}
}

func TestValidate_InvalidType(t *testing.T) {
	data := []byte(`
formula = "test"
type = "invalid"
version = 1
[[steps]]
id = "step1"
`)

	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for invalid type")
	}
}

func TestValidate_DuplicateStepID(t *testing.T) {
	data := []byte(`
formula = "test"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
[[steps]]
id = "step1"
title = "Step 1 duplicate"
`)

	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for duplicate step id")
	}
}

func TestValidate_UnknownDependency(t *testing.T) {
	data := []byte(`
formula = "test"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
needs = ["nonexistent"]
`)

	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for unknown dependency")
	}
}

func TestValidate_Cycle(t *testing.T) {
	data := []byte(`
formula = "test"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
needs = ["step2"]
[[steps]]
id = "step2"
title = "Step 2"
needs = ["step1"]
`)

	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for cycle")
	}
}

func TestTopologicalSort(t *testing.T) {
	data := []byte(`
formula = "test"
type = "workflow"
version = 1
[[steps]]
id = "step3"
title = "Step 3"
needs = ["step2"]
[[steps]]
id = "step1"
title = "Step 1"
[[steps]]
id = "step2"
title = "Step 2"
needs = ["step1"]
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	order, err := f.TopologicalSort()
	if err != nil {
		t.Fatalf("TopologicalSort failed: %v", err)
	}

	indexOf := func(id string) int {
		for i, x := range order {
			if x == id {
				return i
			}
		}
		return -1
	}

	if indexOf("step1") > indexOf("step2") {
		t.Error("step1 should come before step2")
	}
	if indexOf("step2") > indexOf("step3") {
		t.Error("step2 should come before step3")
	}
}

func TestReadySteps(t *testing.T) {
	data := []byte(`
formula = "test"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
[[steps]]
id = "step2"
title = "Step 2"
needs = ["step1"]
[[steps]]
id = "step3"
title = "Step 3"
needs = ["step1"]
[[steps]]
id = "step4"
title = "Step 4"
needs = ["step2", "step3"]
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// Initially only step1 is ready
	ready := f.ReadySteps(map[string]bool{})
	if len(ready) != 1 || ready[0] != "step1" {
		t.Errorf("ReadySteps({}) = %v, want [step1]", ready)
	}

	// After completing step1, step2 and step3 are ready
	ready = f.ReadySteps(map[string]bool{"step1": true})
	if len(ready) != 2 {
		t.Errorf("ReadySteps({step1}) = %v, want [step2, step3]", ready)
	}

	// After completing step1, step2, step3 is still ready
	ready = f.ReadySteps(map[string]bool{"step1": true, "step2": true})
	if len(ready) != 1 || ready[0] != "step3" {
		t.Errorf("ReadySteps({step1, step2}) = %v, want [step3]", ready)
	}

	// After completing step1, step2, step3, only step4 is ready
	ready = f.ReadySteps(map[string]bool{"step1": true, "step2": true, "step3": true})
	if len(ready) != 1 || ready[0] != "step4" {
		t.Errorf("ReadySteps({step1, step2, step3}) = %v, want [step4]", ready)
	}
}

func TestConvoyReadySteps(t *testing.T) {
	data := []byte(`
formula = "test"
type = "convoy"
version = 1
[[legs]]
id = "leg1"
title = "Leg 1"
[[legs]]
id = "leg2"
title = "Leg 2"
[[legs]]
id = "leg3"
title = "Leg 3"
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// All legs are ready initially (parallel)
	ready := f.ReadySteps(map[string]bool{})
	if len(ready) != 3 {
		t.Errorf("ReadySteps({}) = %v, want 3 legs", ready)
	}

	// After completing leg1, leg2 and leg3 still ready
	ready = f.ReadySteps(map[string]bool{"leg1": true})
	if len(ready) != 2 {
		t.Errorf("ReadySteps({leg1}) = %v, want 2 legs", ready)
	}
}

func TestVarStructAcceptsSourceFieldFromTOML(t *testing.T) {
	data := []byte(`
formula = "test-source"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Step 1"

[vars.issue]
description = "The bug ID"
required = true
source = "hook_bead"

[vars.feature]
description = "The feature name"
required = true
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	issueVar, ok := f.Vars["issue"]
	if !ok {
		t.Fatal("expected vars to contain 'issue'")
	}
	if issueVar.Source != "hook_bead" {
		t.Errorf("issue.Source = %q, want %q", issueVar.Source, "hook_bead")
	}
	if !issueVar.Required {
		t.Error("issue.Required should be true")
	}

	featureVar, ok := f.Vars["feature"]
	if !ok {
		t.Fatal("expected vars to contain 'feature'")
	}
	if featureVar.Source != "" {
		t.Errorf("feature.Source = %q, want empty", featureVar.Source)
	}
}

func TestParse_WorkflowWithGate(t *testing.T) {
	data := []byte(`
formula = "test-gate"
type = "workflow"
version = 1

[[steps]]
id = "gated-step"
title = "Gated Step"
description = "Wait for feedback"
gate = { type = "human", id = "feedback-1", timeout = "24h" }

[[steps]]
id = "ungated-step"
title = "Ungated Step"
description = "Normal step"
needs = ["gated-step"]
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(f.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(f.Steps))
	}

	if f.Steps[0].Gate == nil {
		t.Fatal("Steps[0].Gate is nil, want non-nil")
	}
	if f.Steps[0].Gate.Type != "human" {
		t.Errorf("Gate.Type = %q, want %q", f.Steps[0].Gate.Type, "human")
	}
	if f.Steps[0].Gate.ID != "feedback-1" {
		t.Errorf("Gate.ID = %q, want %q", f.Steps[0].Gate.ID, "feedback-1")
	}
	if f.Steps[0].Gate.Timeout != "24h" {
		t.Errorf("Gate.Timeout = %q, want %q", f.Steps[0].Gate.Timeout, "24h")
	}

	if f.Steps[1].Gate != nil {
		t.Errorf("Steps[1].Gate = %v, want nil", f.Steps[1].Gate)
	}
}

func TestParse_WorkflowWithConditionalGate(t *testing.T) {
	data := []byte(`
formula = "test-conditional-gate"
type = "workflow"
version = 1

[[steps]]
id = "conditional-step"
title = "Conditional Step"
description = "Wait for condition"
gate = { type = "conditional", condition = "no_response_1" }
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if f.Steps[0].Gate == nil {
		t.Fatal("Steps[0].Gate is nil, want non-nil")
	}
	if f.Steps[0].Gate.Type != "conditional" {
		t.Errorf("Gate.Type = %q, want %q", f.Steps[0].Gate.Type, "conditional")
	}
	if f.Steps[0].Gate.Condition != "no_response_1" {
		t.Errorf("Gate.Condition = %q, want %q", f.Steps[0].Gate.Condition, "no_response_1")
	}
}

func TestParseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.formula.toml")
	content := []byte(`
formula = "file-test"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
`)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}
	if f.Name != "file-test" {
		t.Errorf("Name = %q, want %q", f.Name, "file-test")
	}
}

func TestValidate_InvalidVarSource(t *testing.T) {
	data := []byte(`
formula = "test"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
[vars.x]
source = "bogus"
`)
	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for invalid variable source 'bogus'")
	}
}

func TestValidate_EmptyVarSource(t *testing.T) {
	data := []byte(`
formula = "test"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
[vars.x]
description = "a placeholder with no source"
`)
	_, err := Parse(data)
	if err != nil {
		t.Errorf("empty source should be valid, got error: %v", err)
	}
}

func TestGetStep(t *testing.T) {
	f := &Formula{
		Type: TypeWorkflow,
		Steps: []Step{
			{ID: "step1", Title: "Step 1"},
			{ID: "step2", Title: "Step 2"},
		},
	}

	s := f.GetStep("step1")
	if s == nil {
		t.Fatal("GetStep(step1) returned nil")
	}
	if s.Title != "Step 1" {
		t.Errorf("Title = %q, want %q", s.Title, "Step 1")
	}

	if f.GetStep("nonexistent") != nil {
		t.Error("GetStep(nonexistent) should return nil")
	}
}

func TestGetLeg(t *testing.T) {
	f := &Formula{
		Type: TypeConvoy,
		Legs: []Leg{
			{ID: "leg1", Title: "Leg 1"},
		},
	}

	l := f.GetLeg("leg1")
	if l == nil {
		t.Fatal("GetLeg(leg1) returned nil")
	}
	if f.GetLeg("nonexistent") != nil {
		t.Error("GetLeg(nonexistent) should return nil")
	}
}

func TestGetTemplate(t *testing.T) {
	f := &Formula{
		Type:     TypeExpansion,
		Template: []Template{{ID: "t1", Title: "T1"}},
	}

	tmpl := f.GetTemplate("t1")
	if tmpl == nil {
		t.Fatal("GetTemplate(t1) returned nil")
	}
	if f.GetTemplate("nonexistent") != nil {
		t.Error("GetTemplate(nonexistent) should return nil")
	}
}

func TestGetAspect(t *testing.T) {
	f := &Formula{
		Type:    TypeAspect,
		Aspects: []Aspect{{ID: "a1", Title: "A1"}},
	}

	a := f.GetAspect("a1")
	if a == nil {
		t.Fatal("GetAspect(a1) returned nil")
	}
	if f.GetAspect("nonexistent") != nil {
		t.Error("GetAspect(nonexistent) should return nil")
	}
}
