package formula

import (
	"fmt"
	"regexp"
)

// templateVarRegex matches {{variable}} placeholders.
var templateVarRegex = regexp.MustCompile(`\{\{(\w+)\}\}`)

// ExpandTemplateVars replaces {{variable}} placeholders in text using the provided context map.
// Unknown variables are left as-is.
func ExpandTemplateVars(text string, ctx map[string]string) string {
	if ctx == nil {
		return text
	}

	return templateVarRegex.ReplaceAllStringFunc(text, func(match string) string {
		// Extract variable name from {{name}}
		varName := match[2 : len(match)-2]
		if value, ok := ctx[varName]; ok {
			return value
		}
		return match // Leave unknown variables as-is
	})
}

// ResolveContext provides the resolution context for variable expansion.
type ResolveContext struct {
	// CLIArgs contains --var key=val arguments from the command line.
	CLIArgs map[string]string
	// EnvLookup returns the value of a named environment variable.
	// When nil, env-source variables resolve to empty string.
	EnvLookup func(string) string
	// HookedBeadID is the bead ID from the hooked formula instance.
	HookedBeadID string
	// BeadTitle is the title of the hooked bead.
	BeadTitle string
	// BeadDescription is the description of the hooked bead.
	BeadDescription string
}

// ResolveVars resolves a set of variable definitions to concrete values.
// It supports "cli", "env", "literal", "hook_bead", "bead_title", and
// "bead_description" sources. Bead-based sources resolve from ResolveContext
// fields populated by the caller (e.g., af sling).
func ResolveVars(vars map[string]Var, ctx ResolveContext) (map[string]string, error) {
	result := make(map[string]string, len(vars))

	for name, v := range vars {
		if v.Source == "deferred" {
			continue
		}
		val, err := resolveVar(name, v, ctx)
		if err != nil {
			return nil, err
		}
		result[name] = val
	}

	return result, nil
}

func resolveVar(name string, v Var, ctx ResolveContext) (string, error) {
	// Universal CLI override: --var key=val takes precedence regardless of source type (CR-1).
	if ctx.CLIArgs != nil {
		if val, ok := ctx.CLIArgs[name]; ok {
			return val, nil
		}
	}

	switch v.Source {
	case "cli":
		// CLI args handled by universal override above. If we reach here,
		// the variable was not provided via --var.
		if v.Default != "" {
			return v.Default, nil
		}
		if v.Required {
			return "", fmt.Errorf("required variable %q not provided (source: cli)", name)
		}
		return "", nil

	case "env":
		var val string
		if ctx.EnvLookup != nil {
			val = ctx.EnvLookup(name)
		}
		if val != "" {
			return val, nil
		}
		if v.Default != "" {
			return v.Default, nil
		}
		if v.Required {
			return "", fmt.Errorf("required variable %q not set in environment", name)
		}
		return "", nil

	case "", "literal":
		return v.Default, nil

	case "hook_bead":
		if ctx.HookedBeadID != "" {
			return ctx.HookedBeadID, nil
		}
		if v.Default != "" {
			return v.Default, nil
		}
		if v.Required {
			return "", fmt.Errorf("required variable %q: no hooked bead available (source: hook_bead). Use --var %s=<bead-id> to provide manually", name, name)
		}
		return "", nil

	case "bead_title":
		if ctx.BeadTitle != "" {
			return ctx.BeadTitle, nil
		}
		if v.Default != "" {
			return v.Default, nil
		}
		if v.Required {
			return "", fmt.Errorf("required variable %q: no bead title available (source: bead_title). Ensure a bead is hooked or use --var %s=<value>", name, name)
		}
		return "", nil

	case "bead_description":
		if ctx.BeadDescription != "" {
			return ctx.BeadDescription, nil
		}
		if v.Default != "" {
			return v.Default, nil
		}
		if v.Required {
			return "", fmt.Errorf("required variable %q: no bead description available (source: bead_description). Ensure a bead is hooked or use --var %s=<value>", name, name)
		}
		return "", nil

	default:
		return "", fmt.Errorf("unknown variable source %q for variable %q", v.Source, name)
	}
}

// MergeInputsToVars converts formula inputs into vars with Source="cli",
// merging them into a copy of the vars map for resolution.
// Input.Type and Input.RequiredUnless are lost during merge (Var has no corresponding fields).
func MergeInputsToVars(inputs map[string]Input, vars map[string]Var) (map[string]Var, error) {
	if len(inputs) == 0 {
		return vars, nil
	}

	merged := make(map[string]Var, len(vars)+len(inputs))
	for k, v := range vars {
		merged[k] = v
	}

	for name, input := range inputs {
		if _, exists := merged[name]; exists {
			return nil, fmt.Errorf("input %q collides with existing var", name)
		}
		merged[name] = Var{
			Source:      "cli",
			Required:    input.Required,
			Default:     input.Default,
			Description: input.Description,
		}
	}

	return merged, nil
}
