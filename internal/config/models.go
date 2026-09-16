package config

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// ModelsConfig holds the contents of .agentfactory/models.json — the registry of
// per-agent model "export sets" for issue #480. The schema is intentionally
// generic: a profile is a plain map of env exports (name → {ENV_KEY: value}), so
// a new model or export key is a config edit with no code change. Empty values
// are preserved (notably ANTHROPIC_API_KEY:"" is an explicit clear). Like
// startup.json an absent file yields an empty config, never a not-found error.
type ModelsConfig struct {
	Default string                       `json:"default,omitempty"`
	Models  map[string]map[string]string `json:"models"`
	Agents  map[string]string            `json:"agents,omitempty"`
}

// EnvVar is one ordered export. ResolveModelEnv returns a slice of these so the launch chokepoint
// emits them deterministically. An empty value is carried rather than dropped — but the slice is
// not a readout of cfg.Models[name]: for a profile carrying an ANTHROPIC_BASE_URL the resolver
// completes the derivable class keys first, so a key the profile never declared can appear here,
// and a class key it declared empty can arrive filled — in both cases only when the profile
// declares some value for that rung to copy.
type EnvVar struct{ Key, Value string }

const (
	envModel         = "ANTHROPIC_MODEL"
	envAPIKey        = "ANTHROPIC_API_KEY"
	envBaseURL       = "ANTHROPIC_BASE_URL"
	envAuthToken     = "ANTHROPIC_AUTH_TOKEN"
	envCompactWindow = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"
)

// EnvMaxContextTokens is the operator's declaration of a foreign backend's REAL context window —
// the same quantity the host reports back as context_window_size. Exported so the statusline drift
// advisory reads this one doc-cited source of truth instead of a private copy (issue #602 F3).
const EnvMaxContextTokens = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"

// EnvBackendPoolTokens is the operator's declaration of a SHARED backend's total context pool — the
// capacity the dispatch-admit gate divides among the live sessions on one backend (#669 THREAD-2). It
// is deliberately distinct from EnvMaxContextTokens, the PER-REQUEST window: one backend can serve many
// concurrent sessions out of a single pool, and letting the per-request window double as the pool was
// correct only by numeric coincidence. Absent ⇒ the gate is inert for that backend. It rides the
// free-form profile env map — no schema field, no strict-decode change.
const EnvBackendPoolTokens = "AF_BACKEND_POOL_TOKENS"

// EnvBackendChildFloorTokens is the operator's per-profile override of the child-footprint FLOOR: the
// minimum free pool a launch must leave behind so the next child still has room to seat (#669 F1/BAD-4).
// It rides the same free-form profile env map beside AF_BACKEND_POOL_TOKENS. Unlike the pool, an ABSENT
// floor is NOT inert — it falls back to defaultBackendChildFloorTokens, because a declared pool with no
// footprint floor is the exact near-ceiling admit-then-starve the thread reports (reservationTokens
// shrinks toward zero as Σ nears the ceiling and would let a launch through that leaves no room for the
// next). A declared value must be a positive decimal, never zero: a zero floor would silently disable
// the gate the pool still arms.
const EnvBackendChildFloorTokens = "AF_BACKEND_CHILD_FLOOR_TOKENS"

// EnvDisableParallelSubagents is the operator's HARD CAP on sub-agent concurrency for a backend: set to
// "1", the dispatch gate stops treating concurrency as an arithmetic question and enforces a strict
// semaphore of one — a second sub-agent is refused while any sibling is still running, however much pool
// the math believes is free. It exists because on a fixed local backend (#672) parallel sub-agents
// oversubscribe the one shared KV pool no matter how the tokens divide, and the token arithmetic — being
// blind to in-process children that write no occupancy snapshot, and admitting from a lean launcher — can
// green-light a fan-out the pool cannot serve. The only safe policy there is one at a time. Absent, or any
// value other than "1", leaves the arithmetic path unchanged (fail-safe toward existing behavior).
const EnvDisableParallelSubagents = "AF_DISABLE_PARALLEL_SUBAGENTS"

// EnvEffortLevel is how hard the host is asked to think (#668 D16). Exported because the cmd layer
// needs the same spelling twice: to drop the key from a relaunch when the experiment's arm is off,
// and to record which arm the relaunch ran.
//
// It is deliberately in NEITHER EndpointClassKeys NOR session.redirectFamilyVars. The second is the
// live one: that family is cleared unconditionally on every launch, quickstart.sh:567 exports this
// key into every operator shell, and nothing re-derives an effort level the way
// CompleteEndpointProfile re-derives the family's other members — so membership would silently
// downshift every agent in every existing factory. The #602 profile-key universe does the same
// hygiene without the collateral, because it only unsets keys some profile actually declares.
// internal/session/effort_hygiene_test.go states all three halves.
//
// Source: https://code.claude.com/docs/en/env-vars, observed 2026-08-30 against claude 2.1.224.
const EnvEffortLevel = "CLAUDE_CODE_EFFORT_LEVEL"

// effortLevels is the host's vocabulary, and an unrecognised value is DROPPED in favour of the
// host's default rather than rejected. That is why this is validated at the write boundary and not
// merely documented: a profile saved with "maximum" would run at the host's full effort while the
// operator's file and this feature's own records both claimed the arm was reduced — the experiment
// D16 exists to run, silently comparing a thing against itself. Same rule and same pinned source as
// the compaction-window bounds above.
var effortLevels = []string{"low", "medium", "high", "xhigh", "max", "auto"}

// IsEffortLevel reports whether a value is in the host's vocabulary. Exported for the cmd layer,
// which reads the level out of the launch environment to record which arm a session or step ran
// under (#678 K1) and must not put an unrecognised string on a record: the host would have dropped
// such a value in favour of its default, so recording it would attest to an arm that never ran. The
// validation rule stays declared once, here, beside the vocabulary it validates against.
func IsEffortLevel(v string) bool { return slices.Contains(effortLevels, v) }

// EffortLevelAuto is the one member of the vocabulary above that names no depth of its own.
const EffortLevelAuto = "auto"

// EffortRank orders the GRADED members of the vocabulary — low < medium < high < xhigh < max — so a
// caller holding a ceiling can compare two levels. The vocabulary above is declared in that
// ascending order with auto appended last, so the index is the rank.
//
// It returns -1 for auto and for anything outside the vocabulary, and that refusal is the point.
// Auto is the HOST's own default, and the host does not publish where that default sits in this
// order; a rank invented for it would let a comparison claim a reduction it cannot prove, which is
// the same silent self-comparison IsEffortLevel's doc above exists to prevent. A caller decides what
// an unranked level means for its own decision rather than being handed a number for it.
func EffortRank(level string) int {
	if level == EffortLevelAuto {
		return -1
	}
	return slices.Index(effortLevels, level)
}

// The host derives a real context window for its own models but has to assume one for any
// other gateway's, and it tells them apart by this prefix alone — so the prefix decides
// whether a declared window is meaningful or merely aspirational.
const claudeModelPrefix = "claude-"

// The host does not honour CLAUDE_CODE_AUTO_COMPACT_WINDOW literally. A value it accepts is
// floored at 100000 and then capped at the model's own context window; a value it rejects
// (non-numeric, or outside the documented range) is dropped in favour of the host's default.
// Either way the number that actually takes effect can differ from the one the operator wrote,
// so an out-of-range value is rejected at the write boundary instead of being saved to mean
// something else. The [100000, 1000000] range belongs to the host, not to us — it is pinned
// here so a future host change is a one-line update rather than a silent misconfiguration.
// Source: https://code.claude.com/docs/en/env-vars, observed 2026-08-06 against claude 2.1.223
// (issue #602).
const (
	compactWindowMin = 100000
	compactWindowMax = 1000000
)

// foreignModelWindow is the context window the host assumes for a model id it does not
// recognise as its own, unless CLAUDE_CODE_MAX_CONTEXT_TOKENS declares the real one. It is a
// distinct quantity from compactWindowMax and drives the pairing lint only, never validation.
// Same source and observation date as the bounds above.
const foreignModelWindow = 200000

// afIdentityKeys are the identity vars session.Manager owns (ADR-003/ADR-004). A
// profile that named one would spoof agent identity, so they are denylisted from
// every profile's export keys.
var afIdentityKeys = map[string]bool{
	"AF_ROLE":        true,
	"AF_ACTOR":       true,
	"AF_ROOT":        true,
	"AF_WORKTREE":    true,
	"AF_WORKTREE_ID": true,
}

// afTelemetryKeys are the OTel launch-env family session.Manager owns as the single writer
// (issue #329 K5). A profile that named one could inject telemetry env at the launch chokepoint
// and race that writer — spoofing attribution or overriding the endpoint. They are denylisted
// from every profile's export keys exactly as afIdentityKeys are, so telemetry env has one
// writer. This must stay byte-identical to session.telemetryFamilyVars (the seven-var set).
var afTelemetryKeys = map[string]bool{
	"CLAUDE_CODE_ENABLE_TELEMETRY": true,
	"OTEL_METRICS_EXPORTER":        true,
	"OTEL_LOGS_EXPORTER":           true,
	"OTEL_EXPORTER_OTLP_PROTOCOL":  true,
	"OTEL_EXPORTER_OTLP_ENDPOINT":  true,
	"OTEL_EXPORTER_OTLP_HEADERS":   true,
	"OTEL_RESOURCE_ATTRIBUTES":     true,
}

// LoadModelsConfig loads and validates .agentfactory/models.json. An absent file
// returns an empty config + nil error (NOT a not-found error), mirroring
// LoadStartupConfig.
func LoadModelsConfig(root string) (*ModelsConfig, error) {
	path := ModelsConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ModelsConfig{}, nil
		}
		return nil, fmt.Errorf("reading models config: %w", err)
	}
	var cfg ModelsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing models config (every profile value must be a quoted JSON string, e.g. \"220000\" not 220000): %w", err)
	}
	if err := validateModelsConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveModelsConfig validates then atomically writes the models config to path
// (an absolute path, not a root) via fsutil.WriteFileAtomic. Mirrors
// SaveStartupConfig.
func SaveModelsConfig(path string, cfg *ModelsConfig) error {
	if err := validateModelsConfig(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling models config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0644)
}

// validateModelsConfig is pure (no env reads) and fail-loud. It rejects, across
// EVERY profile: identity-var keys, a non-empty ANTHROPIC_API_KEY, a malformed
// ANTHROPIC_BASE_URL, and an incomplete endpoint (base_url without auth_token).
// It also rejects an agents/default entry naming a model absent from Models.
func validateModelsConfig(cfg *ModelsConfig) error {
	for name, profile := range cfg.Models {
		if err := validateModelProfile(name, profile); err != nil {
			return err
		}
	}
	for agent, model := range cfg.Agents {
		if _, ok := cfg.Models[model]; !ok {
			return fmt.Errorf("%w: agent %q references undefined model %q", ErrMissingField, agent, model)
		}
	}
	if cfg.Default != "" {
		if _, ok := cfg.Models[cfg.Default]; !ok {
			return fmt.Errorf("%w: default references undefined model %q", ErrMissingField, cfg.Default)
		}
	}
	return nil
}

func validateModelProfile(name string, profile map[string]string) error {
	for key, val := range profile {
		if !IsValidEnvKeyName(key) {
			return fmt.Errorf("%w: model %q sets key %q, which is not a valid environment-variable name (must match [A-Za-z_][A-Za-z0-9_]*); profile keys ride unquoted into the launch line", ErrInvalidType, name, key)
		}
		if afIdentityKeys[key] {
			return fmt.Errorf("%w: model %q sets identity var %q reserved for the session manager", ErrInvalidType, name, key)
		}
		if afTelemetryKeys[key] {
			return fmt.Errorf("%w: model %q sets telemetry var %q reserved for the session manager (telemetry env has one writer)", ErrInvalidType, name, key)
		}
		if key == envAPIKey && val != "" {
			return fmt.Errorf("%w: model %q sets a non-empty %s; a real key must not appear in a launch line (use \"\" to clear)", ErrInvalidType, name, envAPIKey)
		}
		if key == envBaseURL && val != "" {
			u, err := url.Parse(val)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("model %q has invalid base_url %q: must start with http:// or https://", name, val)
			}
		}
	}

	// The ANTHROPIC_AUTH_TOKEN and ANTHROPIC_BASE_URL keys are coupled, so read them
	// directly (map iteration above is random-order). A file: value is a secret
	// reference (shape-checked here, dereferenced at launch in Phase 2); anything
	// else that looks like a real credential is rejected on a non-loopback endpoint.
	// The file: convention plus the Phase-2 dereference is the actual secret-exposure
	// guarantee — the sk- guard below is a defense-in-depth backstop, not the barrier.
	if tok := profile[envAuthToken]; isSecretRef(tok) {
		if err := validateSecretRefShape(name, tok); err != nil {
			return err
		}
	} else if looksLikeCredential(tok) && !IsLoopbackEndpoint(profile[envBaseURL]) {
		return fmt.Errorf("%w: model %q sets %s to what looks like a literal credential for a non-loopback endpoint; store it in .agentfactory/secrets/ and use a \"file:<path>\" reference", ErrInvalidType, name, envAuthToken)
	}

	// The compaction keys are read directly for the same reason the endpoint pair above is:
	// map iteration is random-order. Only their values are constrained — the keys themselves
	// are ordinary operator-facing exports, and "" defers to the host exactly as it does for
	// ANTHROPIC_API_KEY.
	if val := profile[envCompactWindow]; val != "" {
		n, ok := DecimalTokenCount(val)
		if !ok || n < compactWindowMin || n > compactWindowMax {
			return fmt.Errorf("%w: model %q sets %s to %q; must be a decimal token count in [%d, %d] or \"\"", ErrInvalidType, name, envCompactWindow, val, compactWindowMin, compactWindowMax)
		}
	}
	if val := profile[EnvMaxContextTokens]; val != "" {
		if n, ok := DecimalTokenCount(val); !ok || n == 0 {
			return fmt.Errorf("%w: model %q sets %s to %q; must be a positive decimal token count or \"\"", ErrInvalidType, name, EnvMaxContextTokens, val)
		}
	}
	if val := profile[EnvBackendPoolTokens]; val != "" {
		if n, ok := DecimalTokenCount(val); !ok || n == 0 {
			return fmt.Errorf("%w: model %q sets %s to %q; must be a positive decimal token count or \"\"", ErrInvalidType, name, EnvBackendPoolTokens, val)
		}
	}
	// Same numeric rule as the pool key, and for the sharper reason: "never zero" is enforced HERE, at
	// load, so a hand-edited "0" is a loud LOAD ERROR rather than a floor the accessor silently defaults
	// away — the two layers agree that a declared floor is a positive fact or it is absent.
	if val := profile[EnvBackendChildFloorTokens]; val != "" {
		if n, ok := DecimalTokenCount(val); !ok || n == 0 {
			return fmt.Errorf("%w: model %q sets %s to %q; must be a positive decimal token count or \"\"", ErrInvalidType, name, EnvBackendChildFloorTokens, val)
		}
	}
	// The hard cap is a boolean the accessor reads as exact-"1"; reject any other non-empty value at
	// load with the pool/floor clauses' shape, so a typo (AF_DISABLE_PARALLEL_SUBAGENTS="true") is a
	// loud LOAD ERROR rather than a cap the operator believes is armed but the accessor silently ignores (#669 F6).
	if val := profile[EnvDisableParallelSubagents]; val != "" && val != "0" && val != "1" {
		return fmt.Errorf("%w: model %q sets %s to %q; must be \"0\", \"1\" or \"\"", ErrInvalidType, name, EnvDisableParallelSubagents, val)
	}
	// Read directly and compared verbatim, for the same two reasons the keys above are: map
	// iteration is random-order, and trimming or case-folding would save a value the host then
	// reads differently. "" defers to the host, exactly as it does for ANTHROPIC_API_KEY.
	if val := profile[EnvEffortLevel]; val != "" && !IsEffortLevel(val) {
		return fmt.Errorf("%w: model %q sets %s to %q; must be one of %s or \"\"",
			ErrInvalidType, name, EnvEffortLevel, val, strings.Join(effortLevels, ", "))
	}

	return checkEndpointComplete(name, profile)
}

// DecimalTokenCount parses an ASCII-decimal token count. ParseUint with an explicit base
// rejects a sign, underscores and radix prefixes, which strconv.Atoi accepts — so this is the
// digits-only test the host's stricter local policy needs, and an overflowing digit string
// fails rather than saturating.
func DecimalTokenCount(val string) (uint64, bool) {
	n, err := strconv.ParseUint(val, 10, 64)
	return n, err == nil
}

// IsValidEnvKeyName reports whether name is a safe POSIX environment-variable identifier:
// ^[A-Za-z_][A-Za-z0-9_]*$. Profile key NAMES are joined raw into the launch line's `export`
// and `unset` segments (issue #602), so a name carrying a space or a shell metacharacter would
// corrupt — or inject into — that command. It is scanned by hand rather than with a regexp for
// the same reason DecimalTokenCount uses ParseUint: an explicit rule with no dependency. Shared
// by the write-boundary reject (validateModelProfile), the cleanup-side filter
// (session.staleUniverseKeys), and the cron var-key reject (validateCrons, issue #610 — those keys
// ride raw into `--var k=v` argv, the same join hazard) so the three can never disagree about which
// names are safe to emit.
func IsValidEnvKeyName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
			// always allowed
		case c >= '0' && c <= '9':
			if i == 0 {
				return false // a leading digit is not a valid identifier
			}
		default:
			return false
		}
	}
	return true
}

// Provenance of the number ResolveContextWindow returned. A window figure alone cannot be
// argued with — "200000" is indistinguishable from a real measurement, a stale declaration, and
// a guess — so the source travels with it and a caller can say WHY it divided by what it did.
const (
	WindowSourceDeclared = "declared"
	WindowSourceHost     = "host"
	WindowSourceFallback = "fallback"
)

// ResolveContextWindow answers "how large is this agent's context window", which is the
// denominator every occupancy fraction is taken against. Precedence is declaration, then the
// host's report, then the assumption the host itself makes (#668 K1/D7):
//
//   - The operator's CLAUDE_CODE_MAX_CONTEXT_TOKENS is first because it is the only operand
//     that can be RIGHT about a gateway the host does not recognise. The host reports what it
//     ASSUMES for a foreign model id; the operator reports what the backend actually has.
//   - The host's report is next, and it is authoritative for the host's own models.
//   - foreignModelWindow last, because a wrong denominator still beats no denominator: every
//     consumer of this divides, and there is no sensible behavior for zero.
//
// It is pure, like PairingLintProfile above and for the same reason (ADR-004). The temptation
// here is sharper than usual — the first operand's name IS an environment variable — but the
// window resolves PER AGENT at read time while a process environment is one value for the whole
// process, so reading it here would give every agent the launching shell's answer. The caller
// knows which agent it is asking about and supplies that agent's profile map and the host
// reading it already took.
//
// Nothing factory-global participates. In particular startup.json's bound_tokens does not: it is
// a telemetry annotation (see StepContextConfig) and routing it here would collapse a per-agent
// quantity into one number for the whole factory, which is exactly what D7 forbids.
func ResolveContextWindow(profile map[string]string, hostReported int64) (window int64, source string) {
	// DecimalTokenCount accepts the full unsigned range, so the narrowing is the hazard: a
	// declaration above MaxInt64 would wrap to a NEGATIVE window, and a negative denominator is
	// worse than an assumed one. Out-of-range is treated as undeclared.
	if declared, ok := DecimalTokenCount(profile[EnvMaxContextTokens]); ok && declared > 0 && declared <= math.MaxInt64 {
		return int64(declared), WindowSourceDeclared
	}
	if hostReported > 0 {
		return hostReported, WindowSourceHost
	}
	return foreignModelWindow, WindowSourceFallback
}

// BackendPoolTokens returns the operator-declared shared pool for a profile and whether one is
// declared. It mirrors ResolveContextWindow's narrowing guard — a value above MaxInt64 would wrap to a
// negative pool, worse than an undeclared one, so out-of-range is treated as undeclared — but unlike
// the window it has NO host-reported fallback: a pool is an operator fact or it is absent, and an
// absent pool leaves the dispatch gate inert by construction rather than dividing by a guess.
func BackendPoolTokens(profile map[string]string) (tokens int64, declared bool) {
	if n, ok := DecimalTokenCount(profile[EnvBackendPoolTokens]); ok && n > 0 && n <= math.MaxInt64 {
		return int64(n), true
	}
	return 0, false
}

// defaultBackendChildFloorTokens is the conservative child-footprint floor applied when a profile
// declares a pool but no AF_BACKEND_CHILD_FLOOR_TOKENS override (#669 F1/BAD-4). It is a pinned constant
// like the compaction-window bounds above so the one doc-cited number lives in exactly one place.
const defaultBackendChildFloorTokens int64 = 50_000

// BackendChildFloorTokens returns the child-footprint floor for a profile: the operator's declared
// AF_BACKEND_CHILD_FLOOR_TOKENS when it is a positive in-range decimal, otherwise
// defaultBackendChildFloorTokens. It mirrors BackendPoolTokens' narrowing guard but its contract is the
// OPPOSITE at the boundary — where an absent pool leaves the gate inert, an absent floor DEFAULTS,
// because a declared pool with no footprint floor is the near-ceiling admit-then-starve #669 reports, and
// an inert floor would be the proportionality knob the doctrine forbids. Never zero: a hand-edited "0"
// (already rejected at load by validateModelsConfig) falls back to the default here as defense in depth.
func BackendChildFloorTokens(profile map[string]string) int64 {
	if n, ok := DecimalTokenCount(profile[EnvBackendChildFloorTokens]); ok && n > 0 && n <= math.MaxInt64 {
		return int64(n)
	}
	return defaultBackendChildFloorTokens
}

// ParallelSubagentsDisabled reports whether the profile hard-caps sub-agent concurrency at one (#672).
// Only the exact value "1" enables it; absent or anything else leaves the arithmetic path in force, so a
// typo fails safe toward existing behavior rather than silently disabling parallelism.
func ParallelSubagentsDisabled(profile map[string]string) bool {
	return profile[EnvDisableParallelSubagents] == "1"
}

// CapacityLintProfile reports the capacity declarations the loader ACCEPTS and the runtime then
// ignores — the shapes that leave an operator believing a cap is armed when it is not (#673
// CONFIG-LINT). It is the third member of this file's lint family and follows their contract exactly:
// pure (ADR-004), warn-only, and called after the write so it changes no return value and no exit code.
//
// Its band is narrow ON PURPOSE, because validateModelProfile already turns most capacity typos into
// loud LOAD ERRORS. AF_DISABLE_PARALLEL_SUBAGENTS="true" and a non-numeric AF_BACKEND_POOL_TOKENS are
// both rejected at load, so linting them would be unreachable code that reads like coverage. What
// survives validation is exactly what is warned about here:
//
//   - "0" — a legal value that arms nothing. ParallelSubagentsDisabled is exact-"1" by design, so
//     "0" is indistinguishable from absent at runtime while looking, in the file, like a decision.
//   - a pool below the child-footprint floor — numeric, positive, and therefore valid, but it refuses
//     the FIRST child of every launch forever. The gate would be doing exactly what it was told; the
//     operator would see a factory that cannot dispatch and no reason why.
//
// The strict runtime predicates are untouched: this reports, it never reinterprets.
func CapacityLintProfile(name string, profile map[string]string) (warning string, hasWarning bool) {
	if profile[EnvDisableParallelSubagents] == "0" {
		return fmt.Sprintf("model %q sets %s to \"0\", which arms nothing — only the exact value \"1\" caps sub-agents at one; remove the key or set it to \"1\"",
			name, EnvDisableParallelSubagents), true
	}
	pool, declared := BackendPoolTokens(profile)
	if !declared {
		return "", false
	}
	// Asked through the same accessor the gate uses, so an operator-set floor moves the warning with
	// it and a lint that disagreed with the runtime is not representable.
	floor := BackendChildFloorTokens(profile)
	if pool >= floor {
		return "", false
	}
	// The consequence differs by profile shape and the message has to say which, or its remedy is
	// wrong. The dispatch gate resolves a backend from ANTHROPIC_BASE_URL and is inert without one, so
	// on a profile that declares no endpoint a below-floor pool refuses nothing — the pool itself is
	// the inert declaration, and telling that operator to "raise the pool" would have them change a
	// number that was never read.
	if profile[envBaseURL] == "" {
		return fmt.Sprintf("model %q sets %s to %d, below the %d-token child footprint floor, but declares no %s — the dispatch gate resolves a backend from that key and is inert without it, so this pool is never read; remove it or declare the endpoint it belongs to",
			name, EnvBackendPoolTokens, pool, floor, envBaseURL), true
	}
	return fmt.Sprintf("model %q sets %s to %d, below the %d-token child footprint floor, so every sub-agent launch on this backend will be refused; raise the pool or lower %s",
		name, EnvBackendPoolTokens, pool, floor, EnvBackendChildFloorTokens), true
}

// PairingLintProfile reports the one profile shape that is legal but incoherent: a model id
// the host does not recognise as its own, declaring an auto-compact window larger than the
// window the host will assume for it, with no CLAUDE_CODE_MAX_CONTEXT_TOKENS to raise that
// assumption. The declared window silently caps, so the profile is still saved and the caller
// warns — this never rejects. It is pure like the rest of this package: no environment reads
// and no file reads (ADR-004), so any command can call it.
func PairingLintProfile(name string, profile map[string]string) (warning string, hasWarning bool) {
	model := profile[envModel]
	if model == "" || strings.HasPrefix(model, claudeModelPrefix) {
		return "", false
	}
	// The cap this lint warns about IS the resolver's fallback — one host assumption, asked for
	// once rather than open-coded twice, so the two cannot drift. The silencing rule stays the
	// lint's own: any non-empty companion silences, because the write boundary (:205-209) has
	// already refused every companion value except "" and a positive decimal count.
	assumed, _ := ResolveContextWindow(nil, 0)
	declared, ok := DecimalTokenCount(profile[envCompactWindow])
	if !ok || declared <= uint64(assumed) {
		return "", false
	}
	if profile[EnvMaxContextTokens] != "" {
		return "", false
	}
	return fmt.Sprintf("model %q sets %s to %s for model id %q, which the host caps at %d; set %s to the model's real context window to raise it",
		name, envCompactWindow, profile[envCompactWindow], model, assumed, EnvMaxContextTokens), true
}

// EndpointClassKeys is the inventory of requestable-model-class env keys an endpoint profile has to
// cover. The host asks for models by CLASS, not only by id, and any class key it finds unset falls
// back to the host's own built-in claude-* id — which a gateway with a closed model_list refuses,
// killing the spawn that asked for it (issue #598).
//
// Exactly FIVE members. ANTHROPIC_MODEL is deliberately absent: it is the derivation SOURCE, not a
// target, so listing it here would report the key everything else is derived from as missing.
// ANTHROPIC_DEFAULT_FABLE_MODEL is also absent, pending a live observation of whether the deployed
// CLI honors it; it is nonetheless a member of session.redirectFamilyVars, because clearing an
// ambient value is correct either way. CLAUDE_CODE_SUBAGENT_MODEL is a member here but is NOT
// derived — see derivedEndpointClassKeys.
//
// Every member must also be a session.redirectFamilyVars member, or a value derived under one
// profile would survive a switch to another on a reused session. TestEndpointClassKeysSubsetOfRedirectFamilyVars
// enforces that against session.go's source, since internal/session imports this package and so
// cannot be imported back.
//
// Source: the class-key set documented for Claude Code at code.claude.com/docs/en/model-config,
// recorded in .designs/598 from a fetch on 2026-08-07. Same pinned-comment idiom as the
// compact-window bounds above.
var EndpointClassKeys = []string{
	"ANTHROPIC_SMALL_FAST_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// derivedEndpointClassKeys are the inventory members derivation may fill, in the order operator
// messages list them. CLAUDE_CODE_SUBAGENT_MODEL is excluded: the host documents it as overriding
// the per-invocation model parameter AND a sub-agent definition's own model frontmatter, so filling
// it would route every deliberate cheap-tier spawn to the main model — a cost and quality inversion
// the operator never chose, and it would make the per-class keys dead letters for sub-agent traffic.
//
// The exclusion is expressed as data rather than as a filter inside the helpers, so revisiting it
// (or admitting the fable key once its behavior is pinned) amends one list instead of control flow.
var derivedEndpointClassKeys = []string{
	"ANTHROPIC_SMALL_FAST_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
}

// endpointClassLabels maps a class key to the operator-facing word for it. One vocabulary spans the
// lint text, check's output and the docs, so the label lives with the inventory rather than being
// respelled at each surface.
var endpointClassLabels = map[string]string{
	"ANTHROPIC_SMALL_FAST_MODEL":     "small/background",
	"ANTHROPIC_DEFAULT_OPUS_MODEL":   "opus",
	"ANTHROPIC_DEFAULT_SONNET_MODEL": "sonnet",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "haiku",
}

// DerivedEndpointClassKeys returns the class keys derivation may fill, in the order operator
// messages list them.
//
// It exists because a caller reporting per-class coverage needs THIS list and not the exported
// EndpointClassKeys inventory: CLAUDE_CODE_SUBAGENT_MODEL is an inventory member derivation
// deliberately never fills, so a verdict over the inventory would report an empty id for it on every
// endpoint profile that does not declare the key by hand — and not declaring it is the norm the
// exclusion exists to protect. The copy is defensive for the same reason CompleteEndpointProfile
// always returns a fresh map: a caller's reslice must not reorder or truncate the inventory the
// resolver shares process-wide.
func DerivedEndpointClassKeys() []string {
	return append([]string(nil), derivedEndpointClassKeys...)
}

// EndpointClassLabel returns the operator-facing word for a class key. Callers outside this package
// reach the vocabulary through here rather than respelling it, which is the whole reason
// endpointClassLabels lives beside the inventory.
func EndpointClassLabel(key string) (label string, known bool) {
	label, known = endpointClassLabels[key]
	return label, known
}

// isEndpointProfile reports whether a profile sends its traffic somewhere other than Anthropic.
// Every class-coverage helper gates on it, so a profile with no base URL is untouched — the five
// direct Anthropic profiles resolve byte-identically to before. It is deliberately the same
// expression checkEndpointComplete uses; the two must never disagree about what an endpoint is.
func isEndpointProfile(profile map[string]string) bool {
	return profile[envBaseURL] != ""
}

// CompleteEndpointProfile returns a copy of profile with every derivable class key that is unset or
// empty filled from what the profile already declares, so an endpoint profile's launch env never
// leaves a class for the host to answer with a claude-* id. A profile with no ANTHROPIC_BASE_URL
// comes back unchanged.
//
// The ladder copies declared values only and never invents an id:
//
//	ANTHROPIC_DEFAULT_HAIKU_MODEL  <- declared HAIKU | SMALL_FAST | main
//	ANTHROPIC_SMALL_FAST_MODEL     <- declared SMALL_FAST | HAIKU | main
//	ANTHROPIC_DEFAULT_OPUS_MODEL   <- main
//	ANTHROPIC_DEFAULT_SONNET_MODEL <- main
//
// Every rung reads the ORIGINAL profile, never the copy being built, so the result cannot depend on
// the order the keys are visited — the two mutually referential rungs above would otherwise become
// order-sensitive the moment a third one is added.
//
// A declared empty class value is filled rather than preserved. Empty means "defer to the host"
// here exactly as it does for ANTHROPIC_API_KEY, and on a gateway deferring to the host is issue
// #598 itself; the launch line already clears the key to an empty string for declared-empty and
// absent alike, so filling preserves an indistinguishability rather than inventing a new one.
// (The clear idiom is spelled out rather than written literally because gofmt rewrites a bare
// two-apostrophe pair in a doc comment into a typographic quote.) Non-class keys are copied
// verbatim, empty values included, so an explicit ANTHROPIC_API_KEY:"" clear still means what it
// always did.
//
// The returned map is always a fresh one, including on the no-op path: callers receive
// cfg.Models[name], a map the loaded registry shares process-wide, and handing it straight back
// would let a caller's write corrupt the registry for every later resolve.
//
// A class with no declared source is left as it is — there is nothing to copy, and inventing an id
// is worse than the gap. An endpoint that declares no ANTHROPIC_MODEL is therefore not untouched:
// the two mutually referential rungs still copy a declared SMALL_FAST into HAIKU and back, while
// OPUS and SONNET, whose only source is the main model, stay unset. That profile launches with
// those classes empty — full issue #598, silently. That is why the gap is reported rather than
// repaired here: CoverageLintProfile names it at write time, and `af config models check` turns it
// into a per-class verdict against what the gateway actually serves.
//
// Pure: no environment reads, no file reads, no network (ADR-004), so any command can call it.
func CompleteEndpointProfile(profile map[string]string) map[string]string {
	out := make(map[string]string, len(profile)+len(derivedEndpointClassKeys))
	for k, v := range profile {
		out[k] = v
	}
	if !isEndpointProfile(profile) {
		return out
	}

	for _, key := range derivedEndpointClassKeys {
		if profile[key] != "" {
			continue
		}
		source := endpointClassSourceKey(profile, key)
		if source == "" {
			continue
		}
		out[key] = profile[source]
	}
	return out
}

// endpointClassSourceKey names the key a class's effective value comes from, or "" when nothing
// declares one. It IS the ladder — CompleteEndpointProfile copies whatever it points at — so the
// rungs are written once and a report of where a value came from can never drift from where it
// actually came from.
//
// Every rung reads the ORIGINAL profile, never a partially built copy, which is what keeps the two
// mutually referential rungs order-independent.
func endpointClassSourceKey(profile map[string]string, key string) string {
	if profile[key] != "" {
		return key
	}
	var ladder []string
	switch key {
	case "ANTHROPIC_DEFAULT_HAIKU_MODEL":
		ladder = []string{"ANTHROPIC_SMALL_FAST_MODEL", envModel}
	case "ANTHROPIC_SMALL_FAST_MODEL":
		ladder = []string{"ANTHROPIC_DEFAULT_HAIKU_MODEL", envModel}
	default:
		ladder = []string{envModel}
	}
	for _, source := range ladder {
		if profile[source] != "" {
			return source
		}
	}
	return ""
}

// EndpointClassSource names the profile key a class's effective id comes from — the class's own key
// when it is declared, otherwise the rung the ladder copied from — and reports false when nothing
// declares one, which is the class CompleteEndpointProfile leaves empty.
//
// It exists so a caller reporting coverage can say which key to edit. Deriving the answer from the
// ladder rather than assuming ANTHROPIC_MODEL matters: the haiku and small/background rungs copy
// from each other first, so a profile declaring only a haiku id covers small/background from HAIKU
// and not from the main model — and a profile with no main model at all still covers those two.
//
// Pure like the rest of this surface (ADR-004).
func EndpointClassSource(profile map[string]string, key string) (source string, known bool) {
	if !isEndpointProfile(profile) {
		return "", false
	}
	source = endpointClassSourceKey(profile, key)
	return source, source != ""
}

// MissingEndpointClasses lists the class keys an endpoint profile leaves to derivation, in the
// order operator messages name them. It reports what the operator has not DECLARED, which is not
// the same as what CompleteEndpointProfile manages to fill: an endpoint profile with no
// ANTHROPIC_MODEL fills nothing and is reported entirely, and that is the profile that most needs
// telling. Returns nothing for a profile with no ANTHROPIC_BASE_URL, since derivation never runs
// there.
//
// Pure like the rest of this surface (ADR-004).
func MissingEndpointClasses(profile map[string]string) []string {
	if !isEndpointProfile(profile) {
		return nil
	}
	var missing []string
	for _, key := range derivedEndpointClassKeys {
		if profile[key] == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

// CoverageLintProfile reports an endpoint profile that will have classes derived for it, so the
// operator learns at write time what the launch env will silently invent on their behalf — and that
// the fable class is the one nothing profile-side can map. Like PairingLintProfile it warns and
// never rejects: the profile is still saved, because a derived class is legal, merely worth
// knowing about. Silent for profiles with no endpoint and for endpoint profiles that declare every
// derivable class, so a fully-configured gateway does not warn on every save forever.
//
// It reads its class list from MissingEndpointClasses so coverage policy lives in exactly one
// place, and distinguishes the two shapes that list can describe: classes that WILL be derived from
// a declared main model, and classes that will not be covered at all because no main model was
// declared. Pure (ADR-004), which is what lets the write path call it.
func CoverageLintProfile(name string, profile map[string]string) (warning string, hasWarning bool) {
	missing := MissingEndpointClasses(profile)
	if len(missing) == 0 {
		return "", false
	}
	labels := make([]string, 0, len(missing))
	for _, key := range missing {
		labels = append(labels, endpointClassLabels[key])
	}
	joined := strings.Join(labels, ", ")

	// Derivation needs a source. Without a declared main model CompleteEndpointProfile fills
	// NOTHING, so the derivation wording would promise the operator a coverage that never happens —
	// on exactly the profile where every class falls through to a claude-* id, which is issue #598's
	// failure. Two shapes, because one message cannot be true of both.
	if profile[envModel] == "" {
		return fmt.Sprintf("model %q: endpoint profile declares no %s, so %s are left uncovered — nothing derives them and requests for those classes reach the gateway as claude-* ids; fable-class requests are served only if the gateway aliases claude-fable-* ids",
			name, envModel, joined), true
	}
	return fmt.Sprintf("model %q: endpoint profile leaves %s to derivation from %s; fable-class requests are served only if the gateway aliases claude-fable-* ids",
		name, joined, envModel), true
}

// checkEndpointComplete rejects an incomplete endpoint: a profile that sets ANTHROPIC_BASE_URL must
// also set a non-empty ANTHROPIC_AUTH_TOKEN, else the launched agent cannot
// authenticate. Shared by the load-time validator and the resolver.
func checkEndpointComplete(name string, profile map[string]string) error {
	if profile[envBaseURL] != "" && profile[envAuthToken] == "" {
		return fmt.Errorf("%w: model %q sets %s without %s (incomplete endpoint)", ErrMissingField, name, envBaseURL, envAuthToken)
	}
	return nil
}

// ResolveModelEnv is the pure deterministic resolver. It picks a selection by
// precedence (cliModel > marker > cfg.Agents[agent] > legacyEntryModel >
// cfg.Default), then:
//   - empty selection           ⇒ ok=false (inherit today's global default)
//   - selection names a profile ⇒ ordered []EnvVar (ANTHROPIC_MODEL first, then
//     remaining keys sorted; empty values kept, except that an endpoint profile's
//     derivable class keys are filled — see below), or an err if that profile's
//     endpoint is incomplete
//   - selection is a raw id     ⇒ emit ANTHROPIC_MODEL only (passthrough)
//
// The profile branch resolves CompleteEndpointProfile(profile), not the profile itself: one
// carrying an ANTHROPIC_BASE_URL gains those derivable class keys it left unset or empty FOR WHICH
// it declares a source to copy, so the emitted set is no longer a faithful readout of
// cfg.Models[name] — though a profile declaring no source keeps its gaps and gains nothing. That
// is the point — a class key the launch line leaves empty is answered by the host's built-in
// claude-* id, which a gateway with a closed model_list refuses (#598). A profile with no base URL
// resolves byte-identically to before, and the completion is a copy, so the registry map the loaded
// config shares process-wide is never written back to.
//
// The marker value is supplied by the cmd layer; the resolver never reads a file
// or the environment (ADR-004), and CompleteEndpointProfile is pure for the same reason. The
// returned name is the lookup key (profile name or raw id), kept distinct from the model id carried
// in ANTHROPIC_MODEL.
func ResolveModelEnv(cfg *ModelsConfig, agent, cliModel, marker, legacyEntryModel string) (string, []EnvVar, bool, error) {
	selection := firstNonEmpty(cliModel, marker, agentModel(cfg, agent), legacyEntryModel, defaultModel(cfg))
	if selection == "" {
		return "", nil, false, nil
	}

	profile := lookupProfile(cfg, selection)
	if profile == nil {
		return selection, []EnvVar{{Key: envModel, Value: selection}}, true, nil
	}

	if err := checkEndpointComplete(selection, profile); err != nil {
		return selection, nil, false, err
	}
	return selection, orderedEnv(CompleteEndpointProfile(profile)), true, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func agentModel(cfg *ModelsConfig, agent string) string {
	if cfg == nil || agent == "" {
		return ""
	}
	return cfg.Agents[agent]
}

func defaultModel(cfg *ModelsConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.Default
}

func lookupProfile(cfg *ModelsConfig, name string) map[string]string {
	if cfg == nil || cfg.Models == nil {
		return nil
	}
	return cfg.Models[name]
}

// orderedEnv flattens a profile into a deterministic slice: ANTHROPIC_MODEL first
// (if present), then the remaining keys in sorted order. Empty values are kept.
//
// Endpoint class derivation does not change it and it is unaware of derivation: on that path
// ResolveModelEnv hands it the COMPLETED profile, so a derived class key arrives as an ordinary
// entry and lands in sorted position among the rest. Nothing here can tell a derived key from a
// declared one, which is why the sort remains the only ordering rule a caller has to know.
func orderedEnv(profile map[string]string) []EnvVar {
	env := make([]EnvVar, 0, len(profile))
	if v, ok := profile[envModel]; ok {
		env = append(env, EnvVar{Key: envModel, Value: v})
	}
	keys := make([]string, 0, len(profile))
	for k := range profile {
		if k == envModel {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, EnvVar{Key: k, Value: profile[k]})
	}
	return env
}
