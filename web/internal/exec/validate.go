package exec

import (
	"fmt"
	"regexp"
	"strings"
)

// This file re-implements the small af-core slices the web module needs. It deliberately
// does NOT import internal/… — Go's internal seal plus the separate go.mod make that
// compiler-impossible, and the duplication is the point (compiler-enforced C-2 decoupling).
//
// Sources (copied, not imported):
//   - validAgentName / reservedNames / ValidateAgentName: internal/config/config.go:57,62-64,294-309
//   - verb set: the Phase-1 allowlist from the design (IMPLREADME File 1)
//   - --var key rule: design-doc C2

// validAgentName mirrors internal/config/config.go:57.
var validAgentName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// reservedNames mirrors internal/config/config.go:62-64 ("dispatch" collides with the
// af-dispatch infra session).
var reservedNames = map[string]bool{"dispatch": true}

// ValidateAgentName mirrors internal/config/config.go:294-309 in full — the regex ALONE is
// insufficient; the empty / length / reserved checks are load-bearing.
func ValidateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("agent name too long (max 64 characters)")
	}
	if reservedNames[name] {
		return fmt.Errorf("agent name %q is reserved", name)
	}
	if !validAgentName.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: must match [a-zA-Z][a-zA-Z0-9_-]*", name)
	}
	return nil
}

// allowedVerbs is the Phase-1 verb allowlist (IMPLREADME File 1). Any verb outside this set
// is refused before reaching exec — there is no generic command passthrough (C-6).
var allowedVerbs = map[string]bool{
	"up":       true,
	"down":     true,
	"sling":    true,
	"agents":   true,
	"formula":  true,
	"dispatch": true,
	"step":     true,
	"config":   true, // Phase 3: `af config <dispatch|startup> set` — the curated settings write path.
}

// ValidateVerb refuses any verb outside the allowlist.
func ValidateVerb(verb string) error {
	if !allowedVerbs[verb] {
		return fmt.Errorf("verb %q is not allowed", verb)
	}
	return nil
}

// validVarKey constrains --var keys to a safe identifier shape (design-doc C2).
var validVarKey = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// validateVar checks a single --var key/value pair. The key must be an identifier; the value
// may contain arbitrary printable text (it travels as ONE literal argv element, so shell
// metacharacters are harmless) but must NOT contain control characters or newlines, which
// could corrupt logs or terminals downstream.
func validateVar(key, value string) error {
	if !validVarKey.MatchString(key) {
		return fmt.Errorf("invalid --var key %q: must match [A-Za-z0-9_]+", key)
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') || r == 0x7f {
			return fmt.Errorf("invalid --var value for %q: control characters are not allowed", key)
		}
	}
	return nil
}

// validateTask checks the operator's positional task value. It applies the SAME value rule as
// validateVar's value loop — reject newlines, carriage returns, other C0 control characters, and
// 0x7f (tab is allowed) — but NOT the validVarKey identifier regex: the task is free text, so
// spaces, commas, and a leading dash are all allowed. The leading dash is safe because Wrapper.Sling
// emits the task after a `--` terminator (so af-core's pflag never misparses it as a flag), and the
// argv-array exec (no shell) means shell metacharacters in the task are inert (one literal element).
func validateTask(task string) error {
	for _, r := range task {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') || r == 0x7f {
			return fmt.Errorf("invalid task: control characters are not allowed")
		}
	}
	return nil
}

// varArgs builds the deterministic, validated --var argv tail (sorted by key for stable output).
func varArgs(vars map[string]string) ([]string, error) {
	if len(vars) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	// stable order
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	out := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		if err := validateVar(k, vars[k]); err != nil {
			return nil, err
		}
		out = append(out, "--var", fmt.Sprintf("%s=%s", k, vars[k]))
	}
	return out, nil
}

// trimAgent mirrors the trimming session.SessionName applies (internal/session/names.go:15-17):
// surrounding whitespace and trailing slashes are stripped before validation/use.
func trimAgent(name string) string {
	return strings.TrimRight(strings.TrimSpace(name), "/")
}
