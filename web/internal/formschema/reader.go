// Package formschema is the C4 form-schema reader for the web module.
//
// It derives the operator-facing form for a formula from Phase 0's `af formula show <name>
// --json` contract (read through the exec wrapper, never by importing internal/…). The raw
// payload carries every declared [inputs.*]/[vars.*] block with its Type/Required/RequiredUnless
// preserved (the merged Vars path drops those — internal/formula/vars.go MergeInputsToVars — so
// this reader depends on the raw read, not the merged one).
//
// The one governing rule is INV-2: the UI must never let the operator override an auto-sourced
// (identity-bearing) variable. So the reader surfaces ONLY user-providable fields —
//   - every input (raw inputs carry no source; formula_show.go hardcodes source=""), and
//   - vars whose source ∈ {cli, env, ""}
// and HIDES vars sourced deferred | literal | hook_bead | bead_title | bead_description, which af
// injects from trusted context. Surfacing a deferred var (e.g. `orchestrator` in minimalworker)
// as a form field would let the UI rewrite who dispatched a worker — the identity coupling the
// design forbids.
//
// Like every af read command, `af formula show --json` ALWAYS exits 0 and encodes failure as
// {"state":"error","error":"…"}; the reader branches on that .state shape, never on the exit
// code (the same honesty invariant the read-model holds).
package formschema

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// FormulaShower yields the raw stdout of `af formula show <formula> --json`. exec.Wrapper
// satisfies it (via FormulaShowJSON); tests inject a hermetic fake. This is the only seam between
// the reader and the af binary — the reader never spawns a process itself.
type FormulaShower interface {
	FormulaShowJSON(ctx context.Context, formula string) (string, error)
}

// Field is one user-providable form field. Type and RequiredUnless are carried through from the
// raw TOML read so the client can render the right control and validate conditional-required
// groups. Kind ("input"|"var") lets the client map the task box to the formula's required input.
type Field struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Type           string   `json:"type"`
	Required       bool     `json:"required"`
	RequiredUnless []string `json:"required_unless"`
	Default        string   `json:"default"`
	Source         string   `json:"source"`
	Kind           string   `json:"kind"`
}

// Schema is the generated form for one formula: the user-providable fields, required-first.
//
// Primary is the server-authoritative answer to "which field is the task" — the EFFECTIVE CLI bind
// target the operator's primary text should bind to when slung as the positional `af sling [task]`
// argument (computed by computePrimary). It is "" when no single field binds the positional, in
// which case the frontend renders a synthetic task box (#440 mechanism 3).
type Schema struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Type        string  `json:"type"`
	Primary     string  `json:"primary"`
	Fields      []Field `json:"fields"`
}

// formulaField re-declares the per-field JSON shape of `af formula show --json`
// (internal/cmd/formula_show.go formulaField). Re-declared, not imported — the web module cannot
// reach internal/… (Go's internal seal + the separate go.mod; compiler-enforced C-2 decoupling).
type formulaField struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Type           string   `json:"type"`
	Required       bool     `json:"required"`
	RequiredUnless []string `json:"required_unless"`
	Default        string   `json:"default"`
	Source         string   `json:"source"`
}

// formulaShowOutput re-declares the nested success shape of `af formula show --json`
// (internal/cmd/formula_show.go formulaShowOutput) plus the {state,error} envelope keys, so one
// decode covers both the success object and the error envelope. inputs[].source is always "".
type formulaShowOutput struct {
	State       string         `json:"state"`
	Error       string         `json:"error"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Type        string         `json:"type"`
	Inputs      []formulaField `json:"inputs"`
	Vars        []formulaField `json:"vars"`
}

// Reader reads form schemas through a FormulaShower.
type Reader struct {
	src FormulaShower
}

// New builds a Reader over the given source (production: an *exec.Wrapper).
func New(src FormulaShower) *Reader {
	return &Reader{src: src}
}

// Read returns the user-providable form schema for a formula by name. It surfaces all inputs and
// the user-providable vars (source ∈ {cli, env, ""}), hides auto-sourced vars (INV-2), orders
// required-first, and carries Type + RequiredUnless through to the client.
func (r *Reader) Read(ctx context.Context, formula string) (Schema, error) {
	raw, err := r.src.FormulaShowJSON(ctx, formula)
	if err != nil {
		return Schema{}, fmt.Errorf("formula show: %w", err)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Schema{}, fmt.Errorf("empty formula show payload for %q", formula)
	}

	var out formulaShowOutput
	if jerr := json.Unmarshal([]byte(trimmed), &out); jerr != nil {
		return Schema{}, fmt.Errorf("decode formula show for %q: %w", formula, jerr)
	}
	// Branch on .state (the error envelope), never on the exit code.
	if out.State == "error" {
		return Schema{}, fmt.Errorf("formula show for %q failed: %s", formula, out.Error)
	}

	// Required-first: collect required fields, then optional ones, preserving inputs-before-vars
	// and the af payload's sorted-by-name order within each group.
	var required, optional []Field
	collect := func(f formulaField, kind string) {
		nf := Field{
			Name:           f.Name,
			Description:    f.Description,
			Type:           f.Type,
			Required:       f.Required,
			RequiredUnless: f.RequiredUnless,
			Default:        f.Default,
			Source:         f.Source,
			Kind:           kind,
		}
		if f.Required {
			required = append(required, nf)
		} else {
			optional = append(optional, nf)
		}
	}

	for _, in := range out.Inputs {
		// Inputs are always user-providable (their source is always "").
		collect(in, "input")
	}
	for _, v := range out.Vars {
		if isUserProvidableSource(v.Source) {
			collect(v, "var")
		}
		// else: deferred|literal|hook_bead|bead_title|bead_description — af injects it (INV-2).
	}

	fields := make([]Field, 0, len(required)+len(optional))
	fields = append(fields, required...)
	fields = append(fields, optional...)

	return Schema{
		Name:        out.Name,
		Description: out.Description,
		Type:        out.Type,
		Primary:     computePrimary(fields),
		Fields:      fields,
	}, nil
}

// computePrimary returns the EFFECTIVE CLI bind target — the single user-providable field the
// operator's primary text binds to when slung as the positional `af sling [task]` argument. It
// mirrors af-core's binder over the user-providable field set (NOT inputs-only — the #440 C1 trap),
// applying the effective-bind 3-way rule:
//
//  1. assignment-bead path (primary/load-bearing): a user-providable REQUIRED field literally named
//     "issue" (var OR input) ⇒ "issue". af-core's assignment-bead binder
//     (internal/cmd/sling.go:431-444) is the SOLE binder for the majority vars-only `issue` shape
//     (e.g. rootcause-all), where an inputs-only scan would wrongly return "" and orphan the task.
//  2. input bridge (secondary, workflow-only optimization): else the SINGLE unsatisfied required
//     INPUT — Kind=="input" && Required && Default=="" && len(RequiredUnless)==0 — ⇒ that input's
//     name (af-core sling.go:472-484 + findUnsatisfiedRequiredInputs at :869-895). Restricting to
//     Kind=="input" keeps a required var from being miscounted as a bridge target.
//  3. else ⇒ "" — the frontend renders a synthetic task box.
//
// Because it operates over the already-filtered user-providable fields (post-isUserProvidableSource),
// a hidden (hook_bead/deferred-sourced) `issue` never satisfies rule 1 — design-v7 ⇒ "".
func computePrimary(fields []Field) string {
	// Mechanism 1: the load-bearing assignment-bead target.
	for _, f := range fields {
		if f.Required && f.Name == "issue" {
			return "issue"
		}
	}
	// Mechanism 2: the single unsatisfied required input (the workflow-only input bridge).
	bridge, count := "", 0
	for _, f := range fields {
		if f.Kind == "input" && f.Required && f.Default == "" && len(f.RequiredUnless) == 0 {
			bridge = f.Name
			count++
		}
	}
	if count == 1 {
		return bridge
	}
	// Mechanism 3: no single positional bind target — the frontend renders a synthetic task box.
	return ""
}

// FieldNames returns the set of user-providable field names in the schema — used by the sling
// handler to reject any submitted key that is not a user-providable field (INV-2).
func (s Schema) FieldNames() map[string]bool {
	set := make(map[string]bool, len(s.Fields))
	for _, f := range s.Fields {
		set[f.Name] = true
	}
	return set
}

// isUserProvidableSource reports whether a var with this source may be surfaced as a form field.
// The full valid source set is {"", cli, env, literal, hook_bead, bead_title, bead_description,
// deferred} (internal/formula/validate.go); only the first three are user-providable.
func isUserProvidableSource(source string) bool {
	switch source {
	case "", "cli", "env":
		return true
	default:
		return false
	}
}
