package formula

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
)

// Validate checks that the formula has all required fields and valid structure.
func (f *Formula) Validate() error {
	if f.Name == "" {
		return fmt.Errorf("formula field is required")
	}

	if !f.Type.IsValid() {
		return fmt.Errorf("invalid formula type %q (must be convoy, workflow, expansion, or aspect)", f.Type)
	}

	if len(f.Vars) > 0 {
		if err := f.validateVars(); err != nil {
			return err
		}
	}

	if err := f.validateInputVarCollision(); err != nil {
		return err
	}

	switch f.Type {
	case TypeConvoy:
		return f.validateConvoy()
	case TypeWorkflow:
		return f.validateWorkflow()
	case TypeExpansion:
		return f.validateExpansion()
	case TypeAspect:
		return f.validateAspect()
	}

	return nil
}

func (f *Formula) validateConvoy() error {
	if len(f.Legs) == 0 {
		return fmt.Errorf("convoy formula requires at least one leg")
	}

	seen := make(map[string]bool)
	for _, leg := range f.Legs {
		if leg.ID == "" {
			return fmt.Errorf("leg missing required id field")
		}
		if seen[leg.ID] {
			return fmt.Errorf("duplicate leg id: %s", leg.ID)
		}
		seen[leg.ID] = true
	}

	if f.Synthesis != nil {
		for _, dep := range f.Synthesis.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("synthesis depends_on references unknown leg: %s", dep)
			}
		}
	}

	return nil
}

func (f *Formula) validateWorkflow() error {
	if len(f.Steps) == 0 {
		return fmt.Errorf("workflow formula requires at least one step")
	}

	seen := make(map[string]bool)
	for _, step := range f.Steps {
		if step.ID == "" {
			return fmt.Errorf("step missing required id field")
		}
		if seen[step.ID] {
			return fmt.Errorf("duplicate step id: %s", step.ID)
		}
		seen[step.ID] = true
	}

	for _, step := range f.Steps {
		for _, need := range step.Needs {
			if !seen[need] {
				return fmt.Errorf("step %q needs unknown step: %s", step.ID, need)
			}
		}
	}

	if err := f.checkCycles(); err != nil {
		return err
	}

	return nil
}

func (f *Formula) validateExpansion() error {
	if len(f.Template) == 0 {
		return fmt.Errorf("expansion formula requires at least one template")
	}

	seen := make(map[string]bool)
	for _, tmpl := range f.Template {
		if tmpl.ID == "" {
			return fmt.Errorf("template missing required id field")
		}
		if seen[tmpl.ID] {
			return fmt.Errorf("duplicate template id: %s", tmpl.ID)
		}
		seen[tmpl.ID] = true
	}

	for _, tmpl := range f.Template {
		for _, need := range tmpl.Needs {
			if !seen[need] {
				return fmt.Errorf("template %q needs unknown template: %s", tmpl.ID, need)
			}
		}
	}

	return nil
}

func (f *Formula) validateAspect() error {
	if len(f.Aspects) == 0 {
		return fmt.Errorf("aspect formula requires at least one aspect")
	}

	seen := make(map[string]bool)
	for _, aspect := range f.Aspects {
		if aspect.ID == "" {
			return fmt.Errorf("aspect missing required id field")
		}
		if seen[aspect.ID] {
			return fmt.Errorf("duplicate aspect id: %s", aspect.ID)
		}
		seen[aspect.ID] = true
	}

	return nil
}

// checkCycles detects circular dependencies in steps using DFS.
func (f *Formula) checkCycles() error {
	deps := make(map[string][]string)
	for _, step := range f.Steps {
		deps[step.ID] = step.Needs
	}

	visited := make(map[string]bool)
	inStack := make(map[string]bool)

	var visit func(id string) error
	visit = func(id string) error {
		if inStack[id] {
			return fmt.Errorf("cycle detected involving step: %s", id)
		}
		if visited[id] {
			return nil
		}
		visited[id] = true
		inStack[id] = true

		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}

		inStack[id] = false
		return nil
	}

	for _, step := range f.Steps {
		if err := visit(step.ID); err != nil {
			return err
		}
	}

	return nil
}

func (f *Formula) validateVars() error {
	validSources := map[string]bool{
		"": true, "cli": true, "env": true, "literal": true,
		"hook_bead": true, "bead_title": true, "bead_description": true,
		"deferred": true,
	}
	for name, v := range f.Vars {
		if !validSources[v.Source] {
			return fmt.Errorf("variable %q has invalid source %q; valid sources: cli, env, literal, hook_bead, bead_title, bead_description, deferred (or omit for implicit literal)", name, v.Source)
		}
	}
	return nil
}

func (f *Formula) validateInputVarCollision() error {
	if len(f.Inputs) == 0 || len(f.Vars) == 0 {
		return nil
	}
	for name := range f.Inputs {
		if _, exists := f.Vars[name]; exists {
			return fmt.Errorf("input %q collides with var of the same name", name)
		}
	}
	return nil
}

// ValidateAgents rejects the formula if any declared agent (formula-level
// or per-unit) is not a key in registry.Agents. An empty declared agent
// is skipped (AC-4 documented default). A nil registry is rejected —
// callers must supply one explicitly.
func (f *Formula) ValidateAgents(registry *config.AgentConfig) error {
	if registry == nil {
		return fmt.Errorf("ValidateAgents requires a non-nil agent registry")
	}
	known := registry.Agents

	check := func(unitKind, unitID, agentName string) error {
		if agentName == "" {
			return nil
		}
		if _, ok := known[agentName]; ok {
			return nil
		}
		return fmt.Errorf("%s %q: unknown agent %q (%s)", unitKind, unitID, agentName, describeKnown(known))
	}

	if err := check("formula", f.Name, f.Agent); err != nil {
		return err
	}
	for _, s := range f.Steps {
		if err := check("step", s.ID, s.Agent); err != nil {
			return err
		}
	}
	for _, l := range f.Legs {
		if err := check("leg", l.ID, l.Agent); err != nil {
			return err
		}
	}
	for _, t := range f.Template {
		if err := check("template", t.ID, t.Agent); err != nil {
			return err
		}
	}
	return nil
}

// describeKnown renders a human-readable hint of known agents for error
// messages. Caps at 10 names to avoid dumping large registries.
func describeKnown(known map[string]config.AgentEntry) string {
	if len(known) == 0 {
		return "no agents configured in agents.json"
	}
	names := make([]string, 0, len(known))
	for name := range known {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 10 {
		return fmt.Sprintf("%d known agents in agents.json", len(names))
	}
	return "known agents: " + strings.Join(names, ", ")
}
