package exec

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// This file re-implements the small af-core slices the web module needs. It deliberately
// does NOT import internal/… — Go's internal seal plus the separate go.mod make that
// compiler-impossible. What that compile error prevents is an IMPORT; it detects no DRIFT.
// Nothing auto-syncs these mirrors to their sources and nothing can compare them, because
// the seal that makes the copy necessary is the same thing that puts the original out of
// reach: each side's tests pin its own copy, so a rule widened in the root module leaves
// this one rejecting values the CLI accepts, with both lanes green. Keeping them in step
// is manual — a change to a source below must be repeated here in the same commit.
//
// Sources (copied, not imported):
//   - validAgentName / reservedNames / ValidateAgentName: internal/config/config.go:57,65-68,299-313
//   - verb set: the CLI verb allowlist
//   - --var key rule: identifier-shape validation
//   - validTelemetryInstance / ValidateTelemetryInstance: internal/cmd/telemetry_where.go:42-66 (#580)

// validAgentName mirrors internal/config/config.go:57.
var validAgentName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// reservedNames mirrors internal/config/config.go's map: "dispatch" collides with the
// af-dispatch infra session; "operator" collides with the web console's synthetic mail
// sender identity, which would otherwise be indistinguishable from a real agent of that name;
// "watchdog" collides with the af-watchdog control-plane session (#548 H-2). Nothing
// auto-syncs this mirror to config.go, so any addition there must be repeated here.
var reservedNames = map[string]bool{"dispatch": true, "operator": true, "watchdog": true}

// ValidateAgentName mirrors internal/config/config.go:299-313 in full — the regex ALONE is
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

// allowedVerbs is the verb allowlist. Any verb outside this set is refused before reaching
// exec — there is no generic command passthrough.
var allowedVerbs = map[string]bool{
	"up":       true,
	"down":     true,
	"sling":    true,
	"agents":   true,
	"formula":  true,
	"dispatch": true,
	"step":     true,
	"config":   true, // Phase 3: `af config <dispatch|startup> set` — the curated settings write path.
	"mail":     true, // Phase 1 (#500): the wrapper fixes the subcommand to `send` — the web mail composer's write path.
	"install":  true, // #502 Phase 1d: allow-lists the install verb for the exec.Wrapper path. NB: the production "Generate All Agents" regeneration does NOT flow through here — genjob (web/internal/genjob/job.go) spawns `af install --agents` via its own os/exec, bypassing this allowlist; Wrapper.GenerateAgents is currently test-only. Kept so any future Wrapper-routed install stays allow-listed; no generic install passthrough.
	// #580: the wrapper fixes the subcommand to one of the three READ verbs — status, report, usage.
	// The gate verbs and the backlog-drain flag live on this same af command but are not
	// constructible from web code, because every wrapper method spells its subcommand as a literal
	// and appends only validated filter flags. Pinned by the argv tests in runner_test.go. The gate
	// file stays operator- and CLI-owned.
	"telemetry": true,
}

// ValidateVerb refuses any verb outside the allowlist.
func ValidateVerb(verb string) error {
	if !allowedVerbs[verb] {
		return fmt.Errorf("verb %q is not allowed", verb)
	}
	return nil
}

// validVarKey constrains --var keys to a safe identifier shape.
var validVarKey = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// validTelemetryInstance is the shape rule for the telemetry surface's instance filter, mirroring
// internal/cmd/telemetry_where.go:49.
//
// It is ValidateAgentName's shape rather than validVarKey's above, and that is not a relaxation:
// every real formula-instance id is af-<8hex>, so validVarKey's ^[A-Za-z0-9_]+$ would reject all of
// them on the hyphen. What matters is what the class EXCLUDES — quotes, whitespace, semicolons,
// control characters, dots and slashes — because that is what makes the value unable to change the
// meaning of a query it is spliced into on the far side.
var validTelemetryInstance = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// maxTelemetryInstanceLen matches the root rule's bound. A length rule belongs next to a shape rule:
// without it, a megabyte of 'a' is a valid identifier.
const maxTelemetryInstanceLen = 64

// ValidateTelemetryInstance refuses an instance filter that is empty, over-long, or outside the
// identifier shape.
//
// No message quotes the value it rejected. The root rule gives the reason — echoing a hostile
// payload is how an injection attempt gets a second delivery route — and it binds harder here: this
// text becomes a field of a JSON payload delivered to a browser, not a line in a terminal.
func ValidateTelemetryInstance(id string) error {
	if id == "" {
		return fmt.Errorf("instance id cannot be empty")
	}
	if len(id) > maxTelemetryInstanceLen {
		return fmt.Errorf("instance id too long (max %d characters)", maxTelemetryInstanceLen)
	}
	if !validTelemetryInstance.MatchString(id) {
		return fmt.Errorf("invalid instance id: must match [a-zA-Z][a-zA-Z0-9_-]*")
	}
	return nil
}

// ValidateTelemetryAgent refuses an agent filter for the telemetry surface.
//
// It applies ValidateAgentName — the mirrored af-core rule the design names — but does NOT return
// its message: that one quotes the rejected name, and this error reaches a browser. Two mirrored
// rules genuinely disagree here (the agent rule echoes, the instance rule above deliberately does
// not); this resolves the disagreement by keeping the mirrored PREDICATE and the non-echoing
// MESSAGE, so neither rule is weakened.
func ValidateTelemetryAgent(name string) error {
	if err := ValidateAgentName(name); err != nil {
		return fmt.Errorf("invalid agent name: must match [a-zA-Z][a-zA-Z0-9_-]* and not be reserved")
	}
	return nil
}

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

// validateMailSubject checks the web composer's subject line: non-empty, at most 200 runes,
// and validateVar's value predicate copied verbatim (so a literal tab IS allowed, exactly as
// in --var values). The cap counts RUNES, not bytes: byte len() would unfairly reject short
// multibyte-unicode subjects.
func validateMailSubject(s string) error {
	if s == "" {
		return fmt.Errorf("mail subject cannot be empty")
	}
	if utf8.RuneCountInString(s) > 200 {
		return fmt.Errorf("mail subject too long (max 200 characters)")
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') || r == 0x7f {
			return fmt.Errorf("invalid mail subject: control characters are not allowed")
		}
	}
	return nil
}

// validateMailBody checks the web composer's body: non-empty, at most 10000 runes, multi-line
// allowed. This is a deliberately NEW rule, NOT a validateVar reuse: mail bodies are
// legitimately multi-line, so \n joins \t as permitted while \r, the other C0 controls, and
// 0x7f stay rejected.
func validateMailBody(b string) error {
	if b == "" {
		return fmt.Errorf("mail body cannot be empty")
	}
	if utf8.RuneCountInString(b) > 10000 {
		return fmt.Errorf("mail body too long (max 10000 characters)")
	}
	for _, r := range b {
		if r == '\r' || (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f {
			return fmt.Errorf("invalid mail body: control characters other than newline and tab are not allowed")
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
