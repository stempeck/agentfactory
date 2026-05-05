package formula

import "fmt"

// TopologicalSort returns items in dependency order (dependencies before dependents).
// Uses Kahn's algorithm (BFS-based). Returns an error if there are cycles.
func (f *Formula) TopologicalSort() ([]string, error) {
	var items []string
	var deps map[string][]string

	switch f.Type {
	case TypeWorkflow:
		for _, step := range f.Steps {
			items = append(items, step.ID)
		}
		deps = make(map[string][]string)
		for _, step := range f.Steps {
			deps[step.ID] = step.Needs
		}
	case TypeExpansion:
		for _, tmpl := range f.Template {
			items = append(items, tmpl.ID)
		}
		deps = make(map[string][]string)
		for _, tmpl := range f.Template {
			deps[tmpl.ID] = tmpl.Needs
		}
	case TypeConvoy:
		for _, leg := range f.Legs {
			items = append(items, leg.ID)
		}
		return items, nil
	case TypeAspect:
		for _, aspect := range f.Aspects {
			items = append(items, aspect.ID)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("unsupported formula type for topological sort")
	}

	// Kahn's algorithm
	inDegree := make(map[string]int)
	for _, id := range items {
		inDegree[id] = 0
	}
	for _, id := range items {
		for range deps[id] {
			inDegree[id]++
		}
	}

	var queue []string
	for _, id := range items {
		if inDegree[id] == 0 {
			queue = append(queue, id)
		}
	}

	// Build reverse adjacency (who depends on me)
	dependents := make(map[string][]string)
	for _, id := range items {
		for _, dep := range deps[id] {
			dependents[dep] = append(dependents[dep], id)
		}
	}

	var result []string
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		result = append(result, id)

		for _, dependent := range dependents[id] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(result) != len(items) {
		return nil, fmt.Errorf("cycle detected in dependencies")
	}

	return result, nil
}

// ReadySteps returns items that have no unmet dependencies.
// completed is a set of item IDs that have been completed.
func (f *Formula) ReadySteps(completed map[string]bool) []string {
	var ready []string

	switch f.Type {
	case TypeWorkflow:
		for _, step := range f.Steps {
			if completed[step.ID] {
				continue
			}
			allMet := true
			for _, need := range step.Needs {
				if !completed[need] {
					allMet = false
					break
				}
			}
			if allMet {
				ready = append(ready, step.ID)
			}
		}
	case TypeExpansion:
		for _, tmpl := range f.Template {
			if completed[tmpl.ID] {
				continue
			}
			allMet := true
			for _, need := range tmpl.Needs {
				if !completed[need] {
					allMet = false
					break
				}
			}
			if allMet {
				ready = append(ready, tmpl.ID)
			}
		}
	case TypeConvoy:
		for _, leg := range f.Legs {
			if !completed[leg.ID] {
				ready = append(ready, leg.ID)
			}
		}
	case TypeAspect:
		for _, aspect := range f.Aspects {
			if !completed[aspect.ID] {
				ready = append(ready, aspect.ID)
			}
		}
	}

	return ready
}

// GetStep returns a step by ID, or nil if not found.
func (f *Formula) GetStep(id string) *Step {
	for i := range f.Steps {
		if f.Steps[i].ID == id {
			return &f.Steps[i]
		}
	}
	return nil
}

// GetLeg returns a leg by ID, or nil if not found.
func (f *Formula) GetLeg(id string) *Leg {
	for i := range f.Legs {
		if f.Legs[i].ID == id {
			return &f.Legs[i]
		}
	}
	return nil
}

// GetTemplate returns a template by ID, or nil if not found.
func (f *Formula) GetTemplate(id string) *Template {
	for i := range f.Template {
		if f.Template[i].ID == id {
			return &f.Template[i]
		}
	}
	return nil
}

// GetAspect returns an aspect by ID, or nil if not found.
func (f *Formula) GetAspect(id string) *Aspect {
	for i := range f.Aspects {
		if f.Aspects[i].ID == id {
			return &f.Aspects[i]
		}
	}
	return nil
}
