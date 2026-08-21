package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// StartupConfig holds the contents of .agentfactory/startup.json. Unlike the
// other config loaders, an ABSENT file yields this struct fully defaulted (never
// a not-found error) — that is the C-4 backward-compat invariant.
type StartupConfig struct {
	Agents         []string          `json:"agents"`
	Quality        string            `json:"quality"`
	Fidelity       string            `json:"fidelity"`
	Improvement    string            `json:"improvement"`
	Telemetry      string            `json:"telemetry"`
	StartDispatch  bool              `json:"start_dispatch"`
	WatchdogAgents []string          `json:"watchdog_agents"`
	Recovery       RecoveryConfig    `json:"recovery"`
	StepContext    StepContextConfig `json:"step_context"`
}

// RecoveryConfig is the factory-invoked context-exhaustion recovery block (#596 K3).
// It is the operator's ONLY surface on the trigger — the production watchdog launch
// is a bare `af watchdog` — so a misconfiguration cannot be caught at the flag layer.
// That is why every relation below is REJECTED LOUDLY rather than clamped: the
// watchdog.go:337-340 clamp shape is deliberately not copied (Decision 9), because a
// silently-corrected threshold or an inverted dark_grace/staleness pair would reshape
// when recovery fires without telling anyone.
//
// Latency contract (Decision 9), which is what the knobs are sized against:
//
//	T ≈ max(throttle, refresh_interval)(≤~60s) + interval×confirm_ticks + respawn + prime ≈ ≤2.5 min
//
// and the dark path plus dark_grace_secs ≈ 12 min.
//
// An absent "recovery" key yields this struct fully defaulted with recovery ENABLED —
// recovery is routine factory behavior (C-4). Enabled is a *bool because encoding/json
// decodes an absent key and an explicit `false` to the same zero value; without the
// pointer, "enabled": false would be unrepresentable and recovery could never be
// turned off. Nil is the "absent on disk" sentinel, mirroring FactoryConfig.GitIdentity.
type RecoveryConfig struct {
	Enabled                  *bool    `json:"enabled"`
	ContextThresholdPct      int      `json:"context_threshold_pct"`
	ContextAdvisoryPct       int      `json:"context_advisory_pct"`
	ConfirmTicks             int      `json:"confirm_ticks"`
	StalenessSecs            int      `json:"staleness_secs"`
	DarkGraceSecs            int      `json:"dark_grace_secs"`
	PostRecoveryProgressSecs int      `json:"post_recovery_progress_secs"`
	ProgressBackstopSecs     int      `json:"progress_backstop_secs"`
	NoStepEscalationSecs     int      `json:"no_step_escalation_secs"`
	MaxAttempts              int      `json:"max_attempts"`
	AttemptWindowSecs        int      `json:"attempt_window_secs"`
	RateCapMax               int      `json:"rate_cap_max"`
	RateCapWindowSecs        int      `json:"rate_cap_window_secs"`
	Exclude                  []string `json:"exclude"`
}

// IsEnabled reads the tri-state Enabled pointer. Absent (nil) ⇒ enabled, so a
// consumer holding a literal-built StartupConfig that never passed through the
// validator reads the same answer the design states instead of nil-dereferencing.
func (r RecoveryConfig) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
}

// StepContextConfig is the COOPERATIVE half of the context ladder (#622 C1), sibling to the
// forceful RecoveryConfig above. bound_tokens is the context budget one formula step is expected
// to fit inside; handoff_pct is the occupancy at which a step boundary hands off to a fresh
// session voluntarily, before recovery has to take the session away. The two blocks form one
// ordered ladder — advisory (70) <= handoff (75) < threshold (85) — rather than two knob families
// that drift apart, which is the whole reason handoff_pct is validated against recovery's values
// instead of on its own.
//
// The bound is data, not a constant compiled into evaluation logic: 200000 appears here as a
// default and nowhere else in the decision path, the same posture as foreignModelWindow
// (models.go:71).
//
// Validation is deliberately ASYMMETRIC, and this is the load-bearing part (cross-review HIGH-1).
// A value the operator WROTE is rejected loudly by validateStepContextRelations, exactly like
// every recovery relation. A value that is ABSENT from disk is DERIVED to fit whatever ladder the
// operator already has, and never load-fails. The asymmetry is not a stylistic softening: a
// startup-load error reaches seven consumers, and up.go:112 turns it into "no agent can launch"
// for the whole factory. An operator who legitimately tightened context_threshold_pct to 70
// before upgrading has no step_context block at all, and rejecting the shipped 75 against their
// ladder would brick them on a value they never chose.
//
// A zero means "absent" here, the same trade fillRecoveryDefaults documents at :195-203, so a
// stated 0 is defaulted rather than rejected and the >= 1 halves of the ladder fire only on
// negatives. The consequence to know about: because validation fills in place and
// SaveStartupConfig marshals the filled struct, the derived handoff_pct is materialised onto disk
// by the next `af config startup set` — from then on it is an explicit value and rejects loudly
// like any other. Recovery has behaved this way since K3, so the two blocks stay consistent;
// TestSaveStartupConfig_MissingStepContextFillsDefaults pins it.
type StepContextConfig struct {
	BoundTokens int `json:"bound_tokens"`
	HandoffPct  int `json:"handoff_pct"`
}

// The shipped step_context defaults (#622 C1). defaultStepBoundTokens mirrors the 200000 window
// foreignModelWindow (models.go:71) already names; defaultStepHandoffPct sits between the recovery
// advisory (70) and threshold (85) so the cooperative boundary always fires first.
const (
	defaultStepBoundTokens = 200000
	defaultStepHandoffPct  = 75
)

func defaultStepContextConfig() StepContextConfig {
	return StepContextConfig{BoundTokens: defaultStepBoundTokens, HandoffPct: defaultStepHandoffPct}
}

// derivedHandoffPct seats the shipped default inside whatever recovery ladder this factory has:
// min(default, threshold-1), floored at the advisory. It is total by construction — it always
// returns a value, for any ladder, because the caller has nowhere to report a failure to.
//
// Totality is the whole point, because validateRecoveryRelations is looser than it looks: it
// rejects advisory >= threshold and a threshold outside 1..99, but puts NO lower bound on the
// advisory, so {threshold: 1, advisory: -1} is an admitted ladder with no seat anywhere in 1..99.
// The final 1..99 clamp guarantees only an IN-RANGE value for those ladders, not the strict
// handoff < threshold — for {threshold: 1, advisory: <= 0} it returns handoff == threshold == 1
// (harmless: both boundaries fire at 1% on an already-degenerate config). The derived value is
// never validated against — fillStepContextDefaults runs after the relation check, not before —
// which is what keeps this path incapable of failing a load no matter which ladder it is handed.
func derivedHandoffPct(r RecoveryConfig) int {
	pct := defaultStepHandoffPct
	if pct >= r.ContextThresholdPct {
		pct = r.ContextThresholdPct - 1
	}
	if pct < r.ContextAdvisoryPct {
		pct = r.ContextAdvisoryPct
	}
	if pct < 1 {
		pct = 1
	}
	if pct > 99 {
		pct = 99
	}
	return pct
}

// StepContextLint reports when this factory's recovery ladder cannot seat the shipped
// step_context.handoff_pct default, so the operator learns that the cooperative boundary is not
// where the documentation says it is. It is the "warn" half of HIGH-1's clamp-and-warn.
//
// It returns the warning rather than printing it, mirroring PairingLintProfile (models.go:256) and
// for the same reason: internal/config is pure and its validation chain is error-only, so warnings
// belong to the caller that owns a writer (config_set.go:275-278 states this as policy). Phase 1
// exposes the sentence; the command surfaces that print it are Phase 2's business, which is why
// nothing in this phase changes what any command emits.
//
// It asks derivedHandoffPct whether the shipped default was seated rather than re-deciding the
// seating rule here. A second copy of that rule would be a copy that drifts: widen the derivation
// later — floor at advisory+1, say, or leave a margin under the threshold — and the lint would go
// on judging by the old rule, so exactly the factories whose handoff WAS clamped would stop being
// told, with no test going red because no test pairs the two.
//
// By the time the fill has run, provenance is no longer recorded anywhere — HandoffPct is a plain
// int and a derived 69 is indistinguishable from a written 69. Matching the effective value against
// the derivation is the closest honest proxy: an operator who wrote a DIFFERENT value chose their
// own seat and does not need to be told the shipped default missed, and warning them anyway would
// put a permanent, unactionable line on every invocation. The residual is that an operator who
// writes exactly the value the derivation would have picked is warned as if it had been derived,
// which is a sentence that is still true about their factory.
func StepContextLint(cfg *StartupConfig) (warning string, hasWarning bool) {
	if cfg == nil {
		return "", false
	}
	seated := derivedHandoffPct(cfg.Recovery)
	if seated == defaultStepHandoffPct || cfg.StepContext.HandoffPct != seated {
		return "", false
	}
	return fmt.Sprintf("startup step_context.handoff_pct defaults to %d, which this factory's recovery ladder "+
		"(context_advisory_pct %d, context_threshold_pct %d) cannot seat; the effective handoff_pct is %d",
		defaultStepHandoffPct, cfg.Recovery.ContextAdvisoryPct, cfg.Recovery.ContextThresholdPct, cfg.StepContext.HandoffPct), true
}

// The staleness floor is 3×(refresh_interval + tick). Neither term lives in
// startup.json and internal/config may not read them from disk (ADR-004), so both
// are pinned here as documented constants.
//
// recoveryRefreshIntervalSecs is the statusline refreshInterval registered by the
// settings template — the low end of the design's 30–60s range. recoveryWatchdogTickSecs
// mirrors the `af watchdog --interval` default (watchdog.go:46).
//
// Load-bearing: 3×(30+30) = 180 is EXACTLY the shipped staleness_secs default, so the
// default sits on the floor with zero margin. Raising either constant makes
// defaultStartupConfig() fail its own validator and every startup.json stop loading.
// TestValidateStartupConfig_DefaultsAreSelfConsistent guards this.
const (
	recoveryRefreshIntervalSecs = 30
	recoveryWatchdogTickSecs    = 30
)

func defaultRecoveryConfig() RecoveryConfig {
	enabled := true
	return RecoveryConfig{
		Enabled:                  &enabled,
		ContextThresholdPct:      85,
		ContextAdvisoryPct:       70,
		ConfirmTicks:             2,
		StalenessSecs:            180,
		DarkGraceSecs:            600,
		PostRecoveryProgressSecs: 900,
		ProgressBackstopSecs:     7200,
		NoStepEscalationSecs:     3600,
		MaxAttempts:              3,
		AttemptWindowSecs:        1800,
		RateCapMax:               6,
		RateCapWindowSecs:        86400,
		Exclude:                  []string{},
	}
}

func defaultStartupConfig() *StartupConfig {
	// Agents nil ⇒ "ALL" (the nil-vs-[] sentinel). WatchdogAgents nil/empty ⇒ the
	// watchdog's PANE surface monitors nothing (never "ALL") — issue #408 inverted that
	// sentinel at the cmd layer. It does NOT stop the watchdog: #596 Decision 4 revised
	// that, so the process always starts and its occupancy-recovery surface covers every
	// live agent regardless. gates default ⇒ no-op. The absent-file load
	// path returns this struct WITHOUT running validateStartupConfig, so Improvement,
	// Telemetry, Recovery and StepContext must be seeded here too (backward-compat).
	return &StartupConfig{Quality: "default", Fidelity: "default", Improvement: "default", Telemetry: "default", Recovery: defaultRecoveryConfig(), StepContext: defaultStepContextConfig()}
}

// LoadStartupConfig loads and validates .agentfactory/startup.json. An absent
// file returns defaults + nil error (NOT a not-found error) — the deliberate
// C-4 divergence from LoadDispatchConfig.
func LoadStartupConfig(root string) (*StartupConfig, error) {
	path := StartupConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultStartupConfig(), nil
		}
		return nil, fmt.Errorf("reading startup config: %w", err)
	}
	var cfg StartupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing startup config: %w", err)
	}
	if err := validateStartupConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveStartupConfig validates then atomically writes the startup config to path
// via fsutil.WriteFileAtomic. It does not assume the file pre-exists — the
// absent-file-⇒-defaults invariant (C-4) lives in LoadStartupConfig, and Save is
// free to create the file. Mirrors SaveBuildHostConfig (config.go).
func SaveStartupConfig(path string, cfg *StartupConfig) error {
	if err := validateStartupConfig(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling startup config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0644)
}

// validateStartupConfig fills gate and recovery defaults in place, rejects bad gate
// enums, then rejects recovery relation violations.
func validateStartupConfig(cfg *StartupConfig) error {
	fillRecoveryDefaults(&cfg.Recovery)
	if cfg.Quality == "" {
		cfg.Quality = "default"
	}
	if cfg.Fidelity == "" {
		cfg.Fidelity = "default"
	}
	if cfg.Improvement == "" {
		cfg.Improvement = "default"
	}
	// Load-bearing: every startup.json written before telemetry existed omits the key,
	// so it unmarshals to "" and the enum loop below would reject it. Without this fill
	// every pre-existing file stops loading.
	if cfg.Telemetry == "" {
		cfg.Telemetry = "default"
	}
	for _, g := range []struct{ name, val string }{{"quality", cfg.Quality}, {"fidelity", cfg.Fidelity}, {"improvement", cfg.Improvement}, {"telemetry", cfg.Telemetry}} {
		switch g.val {
		case "on", "off", "default":
		default:
			return fmt.Errorf("%w: startup %s must be \"on\", \"off\", or \"default\", got %q", ErrInvalidType, g.name, g.val)
		}
	}
	if err := validateRecoveryRelations(&cfg.Recovery); err != nil {
		return err
	}
	// The step_context block inverts the recovery ordering — relations BEFORE the fill — and the
	// inversion is the mechanism, not a preference. A zero means "absent" for both blocks, so once
	// the fill has run there is no way left to tell a value the operator wrote from one this code
	// supplied. Recovery does not care, because it rejects both alike. step_context must care
	// (HIGH-1), so the relations run while "absent" is still legible as a zero and simply skip it,
	// and the fill then derives a value that fits.
	//
	// Running both AFTER validateRecoveryRelations is the other half: the derivation reads the
	// threshold and the advisory, and those are only trustworthy once the recovery ladder itself
	// has been proven sane. It also keeps the recovery relations the first thing an operator hears
	// about when their recovery block is the thing that is wrong.
	if err := validateStepContextRelations(cfg.StepContext, cfg.Recovery); err != nil {
		return err
	}
	fillStepContextDefaults(&cfg.StepContext, cfg.Recovery)
	// Agents / WatchdogAgents / Recovery.Exclude: nil-vs-[] preserved by json.Unmarshal;
	// NO membership check here — internal/config stays pure/decoupled (ADR-004). The
	// agents.json cross-check lives in the cmd layer, in runConfigStartupSet
	// (internal/cmd/config_set.go), alongside the dispatch and messaging ones.
	return nil
}

// fillRecoveryDefaults seeds absent knobs in place. It runs BEFORE the relation
// checks because four real paths hand the validator a zero-value block: a
// startup.json with no "recovery" key (every file in existence), `af config startup
// set` decoding stdin into a fresh struct, SaveStartupConfig on a caller-built
// literal, and the install scaffold. Ordering the checks first would reject all four.
//
// A zero numeric means "absent" here, exactly as the empty string does for the gate
// enums above. The consequence is that a STATED 0 is defaulted rather than rejected,
// so the four >=1-style relations below only ever fire on negatives. That is the
// accepted trade for keeping partial blocks usable, and it is stricter than the house
// idiom (dispatch.go:193-198 and telemetry.go fill on <= 0, swallowing negatives too).
// The "0 ⇒ use the default" rule needs stating in the operator docs when K17 documents
// this block, because progress_backstop_secs and context_advisory_pct carry no relation
// check and would otherwise accept a deliberate 0.
// Stated-but-invalid values are a different matter and reject loudly below.
func fillRecoveryDefaults(r *RecoveryConfig) {
	d := defaultRecoveryConfig()
	if r.Enabled == nil {
		r.Enabled = d.Enabled
	}
	for _, f := range []struct {
		got  *int
		want int
	}{
		{&r.ContextThresholdPct, d.ContextThresholdPct},
		{&r.ContextAdvisoryPct, d.ContextAdvisoryPct},
		{&r.ConfirmTicks, d.ConfirmTicks},
		{&r.StalenessSecs, d.StalenessSecs},
		{&r.DarkGraceSecs, d.DarkGraceSecs},
		{&r.PostRecoveryProgressSecs, d.PostRecoveryProgressSecs},
		{&r.ProgressBackstopSecs, d.ProgressBackstopSecs},
		{&r.NoStepEscalationSecs, d.NoStepEscalationSecs},
		{&r.MaxAttempts, d.MaxAttempts},
		{&r.AttemptWindowSecs, d.AttemptWindowSecs},
		{&r.RateCapMax, d.RateCapMax},
		{&r.RateCapWindowSecs, d.RateCapWindowSecs},
	} {
		if *f.got == 0 {
			*f.got = f.want
		}
	}
	// exclude carries no nil-vs-[] sentinel (unlike agents, where nil ⇒ ALL): absent
	// and empty both mean "exclude nobody", so normalizing keeps the two seed paths
	// producing identical structs.
	if r.Exclude == nil {
		r.Exclude = []string{}
	}
}

// fillStepContextDefaults seeds absent knobs in place, deriving handoff_pct from the recovery
// ladder rather than from a constant table — which is why it takes the filled RecoveryConfig and
// cannot simply mirror fillRecoveryDefaults' shape. It runs AFTER validateStepContextRelations, so
// by the time it overwrites a zero, that zero has already been seen and deliberately skipped.
//
// Nothing here can fail. A derived value is one this code chose, and there is no operator to
// reject it to; the loud rejection is reserved for the values the operator actually wrote.
func fillStepContextDefaults(sc *StepContextConfig, r RecoveryConfig) {
	if sc.BoundTokens == 0 {
		sc.BoundTokens = defaultStepBoundTokens
	}
	if sc.HandoffPct == 0 {
		sc.HandoffPct = derivedHandoffPct(r)
	}
}

// validateStepContextRelations rejects a WRITTEN step_context value loudly, naming the on-disk key
// and the offending value, mirroring validateRecoveryRelations below. It must run before
// fillStepContextDefaults: a zero is "absent on disk" and is skipped here, which is the entire
// mechanism behind HIGH-1's derived-values-never-load-fail rule.
//
// The ladder binds the two blocks into one ordering — advisory <= handoff < threshold — so the
// cooperative boundary handoff always fires BEFORE the watchdog's forceful context-exhaustion
// recovery. A handoff at or above the threshold would be dead configuration: recovery would take
// the session away before the step boundary ever got the chance to hand it over.
func validateStepContextRelations(sc StepContextConfig, r RecoveryConfig) error {
	// `!= 0 && < 1` reduces to `< 0`, and is spelled the long way on purpose: the two clauses are
	// two different rules that happen to meet here. The first is "zero means absent, skip it" —
	// the same clause the HandoffPct check below states explicitly — and the second is the bound
	// the message quotes. Collapsing them to `< 1` would delete the absent case silently.
	if sc.BoundTokens != 0 && sc.BoundTokens < 1 {
		return fmt.Errorf("%w: startup step_context.bound_tokens must be >= 1, got %d", ErrInvalidType, sc.BoundTokens)
	}
	if sc.HandoffPct == 0 {
		return nil
	}
	switch {
	case sc.HandoffPct < 1 || sc.HandoffPct > 99:
		return fmt.Errorf("%w: startup step_context.handoff_pct must be 1-99, got %d", ErrInvalidType, sc.HandoffPct)
	case sc.HandoffPct < r.ContextAdvisoryPct:
		return fmt.Errorf("%w: startup step_context.handoff_pct (%d) must be at or above recovery.context_advisory_pct (%d)", ErrInvalidType, sc.HandoffPct, r.ContextAdvisoryPct)
	case sc.HandoffPct >= r.ContextThresholdPct:
		return fmt.Errorf("%w: startup step_context.handoff_pct (%d) must be below recovery.context_threshold_pct (%d)", ErrInvalidType, sc.HandoffPct, r.ContextThresholdPct)
	}
	return nil
}

// validateRecoveryRelations rejects every relation of Decision 9 loudly, naming the
// on-disk key and the offending value. Nothing here corrects a value — a silent clamp
// is the Gap 16 failure this block exists to close.
func validateRecoveryRelations(r *RecoveryConfig) error {
	stalenessFloor := 3 * (recoveryRefreshIntervalSecs + recoveryWatchdogTickSecs)

	switch {
	case r.ContextThresholdPct < 1 || r.ContextThresholdPct > 99:
		return fmt.Errorf("%w: startup recovery.context_threshold_pct must be 1-99, got %d", ErrInvalidType, r.ContextThresholdPct)
	case r.ContextAdvisoryPct >= r.ContextThresholdPct:
		return fmt.Errorf("%w: startup recovery.context_advisory_pct (%d) must be below context_threshold_pct (%d)", ErrInvalidType, r.ContextAdvisoryPct, r.ContextThresholdPct)
	case r.ConfirmTicks < 1:
		return fmt.Errorf("%w: startup recovery.confirm_ticks must be >= 1, got %d", ErrInvalidType, r.ConfirmTicks)
	case r.StalenessSecs < stalenessFloor:
		return fmt.Errorf("%w: startup recovery.staleness_secs must be >= %d (3 x (refresh %d + tick %d)), got %d", ErrInvalidType, stalenessFloor, recoveryRefreshIntervalSecs, recoveryWatchdogTickSecs, r.StalenessSecs)
	case r.DarkGraceSecs <= r.StalenessSecs:
		return fmt.Errorf("%w: startup recovery.dark_grace_secs (%d) must be above staleness_secs (%d)", ErrInvalidType, r.DarkGraceSecs, r.StalenessSecs)
	case r.NoStepEscalationSecs < 1:
		return fmt.Errorf("%w: startup recovery.no_step_escalation_secs must be > 0, got %d", ErrInvalidType, r.NoStepEscalationSecs)
	case r.RateCapMax < 1:
		return fmt.Errorf("%w: startup recovery.rate_cap_max must be >= 1, got %d", ErrInvalidType, r.RateCapMax)
	case r.RateCapWindowSecs <= r.AttemptWindowSecs:
		return fmt.Errorf("%w: startup recovery.rate_cap_window_secs (%d) must be above attempt_window_secs (%d)", ErrInvalidType, r.RateCapWindowSecs, r.AttemptWindowSecs)
	}
	return nil
}
