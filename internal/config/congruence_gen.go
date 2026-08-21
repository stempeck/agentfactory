package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// The congruence fixtures are a machine-generated portrait of the canonical config structs:
// one JSON document per struct, every exported field populated. They exist because af-core is
// the single validator and writer for all config while its consumers — notably the web console,
// a SEPARATE Go module that never imports this package — must speak the same schema. Without an
// artifact both sides can point at, a field added here and forgotten there drifts silently, and
// the only symptom is an operator's setting quietly failing to persist.
//
// Generated rather than hand-written for one reason: a hand-written fixture records what its
// author remembered, and stays passing when a struct grows a field. A reflection walk cannot
// forget, so byte-comparing it against the committed copy (TestConfigCongruence_NoDrift) turns
// "keep the schemas in sync" from a review instruction into a CI failure — the ADR-008 pattern.
//
// This file is deliberately NOT a _test.go file. `af config fingerprint` hashes this same walk at
// runtime, which is what lets the running binary report its OWN schema rather than a build-time
// constant (design-doc.md:194). A test-only generator would force the fingerprint verb to
// reimplement the walk, and two copies of it are precisely the drift the fixtures exist to catch.

// canaryPrefix marks every synthetic value in a SECRET-tier fixture. The tier split is what lets
// a reviewer tell at a glance whether a fixture is safe to paste into an issue: raw-tier
// documents carry ordinary values, secret-tier documents carry nothing but obvious canaries. A
// credential-shaped field landing in a raw-tier schema therefore shows up as fixture churn in
// the drift test's diff, which is the review chokepoint the risk registry assigns to this phase.
const canaryPrefix = "CANARY-"

const (
	tierRaw    = "raw"
	tierSecret = "secret"
)

// congruenceInt is the synthetic value for any integer the validators do not constrain. The
// exact number carries no meaning; what matters is that it is NON-ZERO, because several loaders
// (fillRecoveryDefaults most consequentially) rewrite a stated 0 into a default in place, so a
// zero-valued fixture field would be indistinguishable from one that was never stated.
const congruenceInt = 11

// congruenceMaxDepth bounds the walk. The config structs are all shallow and none is recursive
// today, so this never fires — it is here so that a future self-referential type produces a
// truncated fixture and a failing drift test rather than a stack overflow in a shipped binary,
// since this walk runs inside `af config fingerprint`, not only under `go test`.
const congruenceMaxDepth = 12

// congruenceDocument is one canonical config document in the fixture set.
type congruenceDocument struct {
	name string     // fixture base name; the file is testdata/congruence/<name>.json
	tier string     // tierRaw or tierSecret — decides whether values carry the canary prefix
	zero func() any // a POINTER to a fresh zero struct, so each call gets its own to fill
}

// congruenceDocuments is the canonical struct roster. Adding a config document? Add it here and
// regenerate; the drift test fails in both directions until the committed fixtures agree.
func congruenceDocuments() []congruenceDocument {
	return []congruenceDocument{
		{"dispatch", tierRaw, func() any { return &DispatchConfig{} }},
		{"startup", tierRaw, func() any { return &StartupConfig{} }},
		{"messaging", tierRaw, func() any { return &MessagingConfig{} }},
		{"statusline", tierRaw, func() any { return &StatuslineConfig{} }},
		{"factory", tierRaw, func() any { return &FactoryConfig{} }},
		{"agents", tierSecret, func() any { return &AgentConfig{} }},
		{"models", tierSecret, func() any { return &ModelsConfig{} }},
		{"telemetry", tierSecret, func() any { return &TelemetryConfig{} }},
	}
}

// congruencePinned holds the synthetic value for every field a validator constrains. A generic
// filler knows a field's TYPE but not its meaning, so left alone it would produce documents that
// no validator accepts — and Phase 2 pushes the raw-tier fixtures through the real validating
// write path, where that would surface as a failure a phase away from its cause.
//
// Keys are "<document>.<json path>", with "[]" for a slice element, ".key" for a map key and
// "{}" for a map value.
//
// The recovery block is the reason this table cannot be a per-type default: its twelve integers
// are RELATED, not independently bounded. validateRecoveryRelations (startup.go:241-263) rejects
// a block whose advisory is not below its threshold, whose staleness is under the 180s floor,
// whose dark grace does not exceed its staleness, or whose rate-cap window does not exceed its
// attempt window — so a uniform fill violates four arms at once. Every value below is also
// deliberately DIFFERENT from defaultRecoveryConfig(), so the fixture proves the fields were
// stated rather than defaulted.
var congruencePinned = map[string]any{
	// dispatch: label and labels are mutually exclusive — dispatch.go:169-172 rejects a mapping
	// carrying both, and that reject is the fail-loud backstop the design tells us not to
	// weaken. A "maximal" document therefore cannot state both; labels wins because it is the
	// normalized form the loader rewrites a lone label into. Consequence worth knowing: with
	// `json:"label,omitempty"` the field is absent from the fixture, so a consumer mirror that
	// drops Label is the one drift these artifacts cannot catch.
	"dispatch.mappings[].label":  "",
	"dispatch.mappings[].source": "issue",

	// A workflow phase must name a label that resolves to exactly one mapping
	// (validateWorkflows, dispatch.go:205-...), so the phase cannot be a free-form synthetic
	// string — it has to be the very value the mapping's labels[] element was filled with.
	"dispatch.workflows[].phases": []string{"mappings[].labels[]"},

	// startup: the four gates are a closed enum.
	"startup.quality":     "on",
	"startup.fidelity":    "off",
	"startup.improvement": "on",
	"startup.telemetry":   "off",

	"startup.recovery.context_threshold_pct":       91,
	"startup.recovery.context_advisory_pct":        61,
	"startup.recovery.confirm_ticks":               4,
	"startup.recovery.staleness_secs":              240,
	"startup.recovery.dark_grace_secs":             780,
	"startup.recovery.post_recovery_progress_secs": 960,
	"startup.recovery.progress_backstop_secs":      7260,
	"startup.recovery.no_step_escalation_secs":     3660,
	"startup.recovery.max_attempts":                5,
	"startup.recovery.attempt_window_secs":         1860,
	"startup.recovery.rate_cap_max":                7,
	"startup.recovery.rate_cap_window_secs":        86460,

	// step_context (#622 C1) is RELATED to the recovery block, so like recovery it cannot take the
	// per-type fill: validateStepContextRelations rejects a handoff_pct outside
	// [recovery.context_advisory_pct, recovery.context_threshold_pct), i.e. [61, 91) here, and a
	// bound_tokens below 1. Both values are deliberately DIFFERENT from the shipped defaults
	// (200000 / 75) so the fixture proves the fields were stated rather than defaulted.
	"startup.step_context.bound_tokens": 180000,
	"startup.step_context.handoff_pct":  80,

	// statusline: elements is a closed whitelist, and pinning the WHOLE roster rather than one
	// name means adding an element changes this fixture — which is the drift signal we want.
	"statusline.elements": StatuslineElementNames(),

	// factory: type is a literal and version is bounded by CurrentFactoryVersion.
	"factory.type":    "factory",
	"factory.version": 1,

	// agents: the map key is an agent NAME (ValidateAgentName's charset, and not reserved), the
	// type is a closed enum, and base_url must parse as http(s).
	"agents.agents.key":        canaryPrefix + "agent",
	"agents.agents{}.type":     "autonomous",
	"agents.agents{}.base_url": "https://canary.invalid",

	// models: default and every agents value must name a profile the registry defines, so all
	// three share one literal. Inner profile keys ride unquoted into the launch line and must be
	// valid environment-variable names.
	"models.default":      canaryPrefix + "profile",
	"models.models.key":   canaryPrefix + "profile",
	"models.models{}.key": "ANTHROPIC_MODEL",
	"models.agents.key":   canaryPrefix + "agent",
	"models.agents{}":     canaryPrefix + "profile",

	// telemetry: endpoint must parse as http(s), protocol is the single supported wire format,
	// and the timeout is bounded above by maxExportTimeoutMS.
	"telemetry.endpoint":          "https://canary.invalid",
	"telemetry.protocol":          telemetryProtocolHTTPJSON,
	"telemetry.export_timeout_ms": 750,
}

// CongruenceFixtures builds the canonical fixture set in memory: fixture name → the exact bytes
// committed under internal/config/testdata/congruence/. The byte shape matches what the setters
// write (two-space indent, trailing newline) so a consumer can compare a fixture against a real
// on-disk document without normalizing first.
//
// Deterministic by construction: struct fields marshal in declaration order, encoding/json sorts
// map keys, and every synthetic value is derived from the field's path rather than from the
// clock or the map iteration order. The drift test's byte-comparison depends on that.
func CongruenceFixtures() (map[string][]byte, error) {
	docs := congruenceDocuments()
	out := make(map[string][]byte, len(docs))
	var badPins []string
	for _, doc := range docs {
		v := doc.zero()
		fillCongruence(reflect.ValueOf(v).Elem(), doc, doc.name, 0, &badPins)
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshaling %s congruence fixture: %w", doc.name, err)
		}
		out[doc.name] = append(data, '\n')
	}
	if len(badPins) > 0 {
		sort.Strings(badPins)
		return nil, fmt.Errorf("congruencePinned holds values of the wrong type for: %s", strings.Join(badPins, ", "))
	}
	return out, nil
}

// SchemaFingerprint is the digest of the canonical fixture set — the value `af config
// fingerprint` reports and Phase 2's GET payload carries so a client can tell that the console
// it is talking to and the af-core behind it disagree about the schema.
func SchemaFingerprint() (string, error) {
	fixtures, err := CongruenceFixtures()
	if err != nil {
		return "", err
	}
	return FingerprintOf(fixtures), nil
}

// FingerprintOf hashes a fixture set into one digest, independent of map iteration order.
//
// Each entry is fed to the hash as name, length, then bytes. The length prefix is what makes the
// encoding unambiguous: without it, a fixture named "ab" holding "c" and one named "a" holding
// "bc" would produce the same byte stream, so a schema change that merely moved content between
// documents could leave the fingerprint unchanged.
func FingerprintOf(fixtures map[string][]byte) string {
	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(fixtures[name]))
		h.Write(fixtures[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fillCongruence populates v in place, recursing through pointers, slices, maps and structs.
// path is the dotted json path reached so far, and is both the pinned-value key and the source
// of every unpinned string value — deriving a string from its own location makes each field
// self-identifying, so a consumer that swaps two fields on the way through shows up as a diff
// rather than as a document that still happens to validate.
// badPins collects paths whose pinned value does not fit the field it names. reflect.Convert
// PANICS on an inconvertible pair, and this file is deliberately non-test so `af config
// fingerprint` shares the walk — an unguarded Convert would put that panic in an operator's
// binary. Reporting instead keeps the failure in the error channel, where CongruenceFixtures
// turns it into a named error and the drift test turns it into a red build.
func fillCongruence(v reflect.Value, doc congruenceDocument, path string, depth int, badPins *[]string) {
	if depth > congruenceMaxDepth || !v.CanSet() {
		return
	}
	if pinned, ok := congruencePinned[path]; ok {
		pv := reflect.ValueOf(pinned)
		if !pv.Type().ConvertibleTo(v.Type()) {
			*badPins = append(*badPins, fmt.Sprintf("%s (%s pinned to %T)", path, v.Type(), pinned))
			return
		}
		v.Set(pv.Convert(v.Type()))
		return
	}

	switch v.Kind() {
	case reflect.String:
		v.SetString(congruenceValue(doc, path))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(congruenceInt)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(congruenceInt)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(congruenceInt)
	case reflect.Pointer:
		// A nil pointer is several config files' "absent on disk" sentinel (GitIdentity,
		// RecoveryConfig.Enabled, StatuslineConfig.Color). The fixture must state the value, not
		// the sentinel, or it would pin the absent case and prove nothing about the present one.
		v.Set(reflect.New(v.Type().Elem()))
		fillCongruence(v.Elem(), doc, path, depth+1, badPins)
	case reflect.Slice:
		elem := reflect.New(v.Type().Elem()).Elem()
		fillCongruence(elem, doc, path+"[]", depth+1, badPins)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), elem))
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fillCongruence(key, doc, path+".key", depth+1, badPins)
		val := reflect.New(v.Type().Elem()).Elem()
		fillCongruence(val, doc, path+"{}", depth+1, badPins)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := jsonFieldName(f)
			if name == "" {
				continue
			}
			fillCongruence(v.Field(i), doc, path+"."+name, depth+1, badPins)
		}
	}
}

// congruenceValue is the synthetic string for an unpinned field: its own path, minus the
// document prefix, so every value in every fixture is unique and names where it came from.
// Secret-tier documents carry the canary prefix as well.
func congruenceValue(doc congruenceDocument, path string) string {
	leaf := strings.TrimPrefix(path, doc.name+".")
	if doc.tier == tierSecret {
		return canaryPrefix + leaf
	}
	return leaf
}

// jsonFieldName returns the on-disk name of a struct field, or "" for one the encoder skips.
// Options after the comma (omitempty, string) do not change the name.
func jsonFieldName(f reflect.StructField) string {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return f.Name
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return ""
	}
	if name == "" {
		return f.Name
	}
	return name
}
