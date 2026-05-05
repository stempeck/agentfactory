package formula

import "testing"

func TestFormulaType_IsValid(t *testing.T) {
	tests := []struct {
		ft   FormulaType
		want bool
	}{
		{TypeConvoy, true},
		{TypeWorkflow, true},
		{TypeExpansion, true},
		{TypeAspect, true},
		{FormulaType("bogus"), false},
		{FormulaType(""), false},
	}
	for _, tt := range tests {
		if got := tt.ft.IsValid(); got != tt.want {
			t.Errorf("FormulaType(%q).IsValid() = %v, want %v", tt.ft, got, tt.want)
		}
	}
}

func TestFormula_GetDependencies_Workflow(t *testing.T) {
	f := &Formula{
		Type: TypeWorkflow,
		Steps: []Step{
			{ID: "build", Needs: []string{"setup"}},
			{ID: "setup"},
		},
	}
	deps := f.GetDependencies("build")
	if len(deps) != 1 || deps[0] != "setup" {
		t.Errorf("GetDependencies(\"build\") = %v, want [\"setup\"]", deps)
	}
	deps = f.GetDependencies("setup")
	if deps != nil {
		t.Errorf("GetDependencies(\"setup\") = %v, want nil", deps)
	}
}

func TestFormula_GetDependencies_Expansion(t *testing.T) {
	f := &Formula{
		Type: TypeExpansion,
		Template: []Template{
			{ID: "tmpl-1", Needs: []string{"tmpl-0"}},
			{ID: "tmpl-0"},
		},
	}
	deps := f.GetDependencies("tmpl-1")
	if len(deps) != 1 || deps[0] != "tmpl-0" {
		t.Errorf("GetDependencies(\"tmpl-1\") = %v, want [\"tmpl-0\"]", deps)
	}
}

func TestFormula_GetDependencies_Convoy(t *testing.T) {
	f := &Formula{
		Type: TypeConvoy,
		Legs: []Leg{
			{ID: "leg-a"},
			{ID: "leg-b"},
		},
		Synthesis: &Synthesis{
			DependsOn: []string{"leg-a", "leg-b"},
		},
	}
	// Legs are parallel — no deps
	deps := f.GetDependencies("leg-a")
	if deps != nil {
		t.Errorf("GetDependencies(\"leg-a\") = %v, want nil", deps)
	}
	// Synthesis depends on legs
	deps = f.GetDependencies("synthesis")
	if len(deps) != 2 {
		t.Errorf("GetDependencies(\"synthesis\") = %v, want [\"leg-a\", \"leg-b\"]", deps)
	}
}

func TestFormula_AgentFor(t *testing.T) {
	tests := []struct {
		name string
		f    Formula
		id   string
		want string
	}{
		{
			name: "per-step Agent wins over formula-level",
			f: Formula{
				Agent: "manager",
				Steps: []Step{{ID: "impl", Agent: "formulist"}},
			},
			id:   "impl",
			want: "formulist",
		},
		{
			name: "per-leg Agent wins over formula-level",
			f: Formula{
				Agent: "manager",
				Legs:  []Leg{{ID: "audit-A", Agent: "archaeologist"}},
			},
			id:   "audit-A",
			want: "archaeologist",
		},
		{
			name: "per-template Agent wins over formula-level",
			f: Formula{
				Agent:    "manager",
				Template: []Template{{ID: "phase-N", Agent: "implementer"}},
			},
			id:   "phase-N",
			want: "implementer",
		},
		{
			name: "step with empty Agent falls back to formula-level",
			f: Formula{
				Agent: "manager",
				Steps: []Step{{ID: "impl"}},
			},
			id:   "impl",
			want: "manager",
		},
		{
			name: "unknown id falls back to formula-level (permissive)",
			f: Formula{
				Agent: "manager",
				Steps: []Step{{ID: "impl"}},
			},
			id:   "missing",
			want: "manager",
		},
		{
			name: "no Agent anywhere returns empty string",
			f:    Formula{Steps: []Step{{ID: "impl"}}},
			id:   "impl",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.f.AgentFor(tt.id)
			if got != tt.want {
				t.Errorf("AgentFor(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestParse_AgentOnStep(t *testing.T) {
	data := []byte(`
description = "Agent-on-step fixture"
formula = "test-agent-step"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "First Step"
description = "Has a declared agent"
agent = "manager"

[[steps]]
id = "step2"
title = "Second Step"
description = "No agent"
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(f.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(f.Steps))
	}
	if f.Steps[0].Agent != "manager" {
		t.Errorf("Steps[0].Agent = %q, want %q", f.Steps[0].Agent, "manager")
	}
	if f.Steps[1].Agent != "" {
		t.Errorf("Steps[1].Agent = %q, want %q", f.Steps[1].Agent, "")
	}
}

func TestParse_AgentOnLeg(t *testing.T) {
	data := []byte(`
description = "Agent-on-leg fixture"
formula = "test-agent-leg"
type = "convoy"
version = 1

[[legs]]
id = "leg1"
title = "Leg One"
focus = "Focus area 1"
description = "First leg with declared agent"
agent = "refinery"

[synthesis]
title = "Synthesis"
description = "Combine results"
depends_on = ["leg1"]
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(f.Legs) != 1 {
		t.Fatalf("len(Legs) = %d, want 1", len(f.Legs))
	}
	if f.Legs[0].Agent != "refinery" {
		t.Errorf("Legs[0].Agent = %q, want %q", f.Legs[0].Agent, "refinery")
	}
}

func TestParse_AgentOnTemplate(t *testing.T) {
	data := []byte(`
description = "Agent-on-template fixture"
formula = "test-agent-template"
type = "expansion"
version = 1

[[template]]
id = "{target}.draft"
title = "Draft"
description = "Initial draft with declared agent"
agent = "manager"
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(f.Template) != 1 {
		t.Fatalf("len(Template) = %d, want 1", len(f.Template))
	}
	if f.Template[0].Agent != "manager" {
		t.Errorf("Template[0].Agent = %q, want %q", f.Template[0].Agent, "manager")
	}
}

func TestParse_AgentOnFormula(t *testing.T) {
	data := []byte(`
description = "Agent-on-formula fixture"
formula = "test-agent-formula"
type = "workflow"
version = 1
agent = "manager"

[[steps]]
id = "step1"
title = "Step 1"
description = "Inherits formula-level agent"
`)

	f, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if f.Agent != "manager" {
		t.Errorf("f.Agent = %q, want %q", f.Agent, "manager")
	}
}

func TestFormula_GetAllIDs(t *testing.T) {
	tests := []struct {
		name string
		f    Formula
		want []string
	}{
		{
			name: "workflow",
			f:    Formula{Type: TypeWorkflow, Steps: []Step{{ID: "s1"}, {ID: "s2"}}},
			want: []string{"s1", "s2"},
		},
		{
			name: "expansion",
			f:    Formula{Type: TypeExpansion, Template: []Template{{ID: "t1"}, {ID: "t2"}}},
			want: []string{"t1", "t2"},
		},
		{
			name: "convoy",
			f:    Formula{Type: TypeConvoy, Legs: []Leg{{ID: "l1"}, {ID: "l2"}}},
			want: []string{"l1", "l2"},
		},
		{
			name: "aspect",
			f:    Formula{Type: TypeAspect, Aspects: []Aspect{{ID: "a1"}, {ID: "a2"}}},
			want: []string{"a1", "a2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.f.GetAllIDs()
			if len(got) != len(tt.want) {
				t.Fatalf("GetAllIDs() = %v, want %v", got, tt.want)
			}
			for i, id := range got {
				if id != tt.want[i] {
					t.Errorf("GetAllIDs()[%d] = %q, want %q", i, id, tt.want[i])
				}
			}
		})
	}
}
