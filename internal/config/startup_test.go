package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// writeStartupRoot creates a temp factory root with a startup.json containing data.
func writeStartupRoot(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "startup.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// Case 1: absent file => defaults, no error (the C-4 divergence, highest-value test).
func TestLoadStartupConfig_AbsentFileDefaults(t *testing.T) {
	dir := t.TempDir() // no startup.json

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("expected nil error for absent file, got %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("absent file must NOT return ErrNotFound (C-4 invariant)")
	}
	if cfg == nil {
		t.Fatal("expected non-nil cfg for absent file")
	}
	if cfg.Agents != nil {
		t.Errorf("Agents = %#v, want nil", cfg.Agents)
	}
	if cfg.WatchdogAgents != nil {
		t.Errorf("WatchdogAgents = %#v, want nil", cfg.WatchdogAgents)
	}
	if cfg.Quality != "default" {
		t.Errorf("Quality = %q, want \"default\"", cfg.Quality)
	}
	if cfg.Fidelity != "default" {
		t.Errorf("Fidelity = %q, want \"default\"", cfg.Fidelity)
	}
	if cfg.Improvement != "default" {
		t.Errorf("Improvement = %q, want \"default\"", cfg.Improvement)
	}
	if cfg.Telemetry != "default" {
		t.Errorf("Telemetry = %q, want \"default\"", cfg.Telemetry)
	}
	if cfg.StartDispatch {
		t.Errorf("StartDispatch = true, want false")
	}
}

// G4 (CFG-3 backward-compat trap): an existing startup.json that OMITS the new
// "improvement" key must still load. Without the empty→"default" fill AND the
// defaultStartupConfig seed, the enum loop would reject the unmarshalled "" and
// every pre-existing file would fail to load. This test pins all four edits.
func TestLoadStartupConfig_MissingImprovementFieldDefaults(t *testing.T) {
	dir := writeStartupRoot(t, `{"quality":"on","fidelity":"off","start_dispatch":true}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("existing startup.json without \"improvement\" must still load, got %v", err)
	}
	if cfg.Improvement != "default" {
		t.Errorf("Improvement = %q, want \"default\"", cfg.Improvement)
	}
}

func TestLoadStartupConfig_BadImprovementValue(t *testing.T) {
	dir := writeStartupRoot(t, `{"improvement":"maybe"}`)

	_, err := LoadStartupConfig(dir)
	if !errors.Is(err, ErrInvalidType) {
		t.Fatalf("expected ErrInvalidType for bad improvement value, got %v", err)
	}
}

func TestLoadStartupConfig_ImprovementRoundTrip(t *testing.T) {
	dir := writeStartupRoot(t, `{"improvement":"on"}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Improvement != "on" {
		t.Errorf("Improvement = %q, want \"on\"", cfg.Improvement)
	}
}

// The same backward-compat trap as the improvement key, one gate later: every
// startup.json in existence omits "telemetry", so it unmarshals to "" and the enum
// loop would reject it. Adding the loop entry without the empty→"default" fill breaks
// every pre-existing file. This test pins all four edits.
func TestLoadStartupConfig_MissingTelemetryFieldDefaults(t *testing.T) {
	dir := writeStartupRoot(t, `{"quality":"on","fidelity":"off","improvement":"on","start_dispatch":true}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("existing startup.json without \"telemetry\" must still load, got %v", err)
	}
	if cfg.Telemetry != "default" {
		t.Errorf("Telemetry = %q, want \"default\"", cfg.Telemetry)
	}
}

func TestLoadStartupConfig_BadTelemetryValue(t *testing.T) {
	dir := writeStartupRoot(t, `{"telemetry":"yes"}`)

	_, err := LoadStartupConfig(dir)
	if !errors.Is(err, ErrInvalidType) {
		t.Fatalf("expected ErrInvalidType for bad telemetry value, got %v", err)
	}
}

func TestLoadStartupConfig_TelemetryRoundTrip(t *testing.T) {
	dir := writeStartupRoot(t, `{"telemetry":"on"}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Telemetry != "on" {
		t.Errorf("Telemetry = %q, want \"on\"", cfg.Telemetry)
	}
}

// Case 2: bad gate value => ErrInvalidType.
func TestLoadStartupConfig_BadGateValue(t *testing.T) {
	dir := writeStartupRoot(t, `{"quality":"of"}`)

	_, err := LoadStartupConfig(dir)
	if !errors.Is(err, ErrInvalidType) {
		t.Fatalf("expected ErrInvalidType, got %v", err)
	}
}

// Case 3: valid full file round-trips all five fields.
func TestLoadStartupConfig_FullRoundTrip(t *testing.T) {
	dir := writeStartupRoot(t, `{"agents":["manager","supervisor"],"quality":"on","fidelity":"off","start_dispatch":true,"watchdog_agents":["manager"]}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"manager", "supervisor"}; !reflect.DeepEqual(cfg.Agents, want) {
		t.Errorf("Agents = %#v, want %#v", cfg.Agents, want)
	}
	if cfg.Quality != "on" {
		t.Errorf("Quality = %q, want \"on\"", cfg.Quality)
	}
	if cfg.Fidelity != "off" {
		t.Errorf("Fidelity = %q, want \"off\"", cfg.Fidelity)
	}
	if !cfg.StartDispatch {
		t.Errorf("StartDispatch = false, want true")
	}
	if want := []string{"manager"}; !reflect.DeepEqual(cfg.WatchdogAgents, want) {
		t.Errorf("WatchdogAgents = %#v, want %#v", cfg.WatchdogAgents, want)
	}
}

// Case 4: agents: [] (present-empty) => non-nil empty slice (distinct from absent).
func TestLoadStartupConfig_EmptyAgentsSlice(t *testing.T) {
	dir := writeStartupRoot(t, `{"agents":[]}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents == nil {
		t.Fatal("Agents = nil, want non-nil empty slice")
	}
	if len(cfg.Agents) != 0 {
		t.Errorf("len(Agents) = %d, want 0", len(cfg.Agents))
	}
}

// Case 5: empty {} file => same defaults as absent-field.
func TestLoadStartupConfig_EmptyObjectDefaults(t *testing.T) {
	dir := writeStartupRoot(t, `{}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Quality != "default" {
		t.Errorf("Quality = %q, want \"default\"", cfg.Quality)
	}
	if cfg.Fidelity != "default" {
		t.Errorf("Fidelity = %q, want \"default\"", cfg.Fidelity)
	}
}

// Case 6: malformed JSON => non-nil error.
func TestLoadStartupConfig_MalformedJSON(t *testing.T) {
	dir := writeStartupRoot(t, `{not valid json}`)

	if _, err := LoadStartupConfig(dir); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// Case 7: the install scaffold must parse correctly with its opinionated defaults.
// This test mirrors the live scaffold seed in internal/cmd/install.go:113: the #408
// watchdog-scope fix widened watchdog_agents to ["manager","supervisor"], but the
// startup `agents` default stays manager-only (PR #410 T1/T3 — the autonomous
// supervisor is not auto-started on a fresh install). It MUST stay in lockstep with
// that literal; the drift-proof guard is TestInstallScaffold_StartupAgentsDefaultManagerOnly
// (install_scaffold_test.go), which source-parses the REAL literal.
func TestLoadStartupConfig_ScaffoldLoads(t *testing.T) {
	scaffoldDir := writeStartupRoot(t, `{"agents":["manager"],"quality":"default","fidelity":"default","start_dispatch":true,"watchdog_agents":["manager","supervisor"]}`)

	cfg, err := LoadStartupConfig(scaffoldDir)
	if err != nil {
		t.Fatalf("scaffold load error: %v", err)
	}
	if !reflect.DeepEqual(cfg.Agents, []string{"manager"}) {
		t.Errorf("agents = %v, want [manager]", cfg.Agents)
	}
	if !cfg.StartDispatch {
		t.Error("start_dispatch = false, want true")
	}
	if !reflect.DeepEqual(cfg.WatchdogAgents, []string{"manager", "supervisor"}) {
		t.Errorf("watchdog_agents = %v, want [manager supervisor]", cfg.WatchdogAgents)
	}
}

// assertRecoveryDefaults pins all 14 knobs of design-doc.md:121-136. Both seed
// sites must produce it, so both tests below assert through this one helper.
func assertRecoveryDefaults(t *testing.T, r RecoveryConfig) {
	t.Helper()
	if !r.IsEnabled() {
		t.Errorf("Recovery.IsEnabled() = false, want true (design-doc.md:140 — absent ⇒ enabled)")
	}
	for _, k := range []struct {
		name string
		got  int
		want int
	}{
		{"context_threshold_pct", r.ContextThresholdPct, 85},
		{"context_advisory_pct", r.ContextAdvisoryPct, 70},
		{"confirm_ticks", r.ConfirmTicks, 2},
		// 180, NOT the 90 that appears in the pre-cross-review dimension docs.
		{"staleness_secs", r.StalenessSecs, 180},
		{"dark_grace_secs", r.DarkGraceSecs, 600},
		{"post_recovery_progress_secs", r.PostRecoveryProgressSecs, 900},
		{"progress_backstop_secs", r.ProgressBackstopSecs, 7200},
		{"no_step_escalation_secs", r.NoStepEscalationSecs, 3600},
		{"max_attempts", r.MaxAttempts, 3},
		{"attempt_window_secs", r.AttemptWindowSecs, 1800},
		{"rate_cap_max", r.RateCapMax, 6},
		{"rate_cap_window_secs", r.RateCapWindowSecs, 86400},
	} {
		if k.got != k.want {
			t.Errorf("Recovery.%s = %d, want %d", k.name, k.got, k.want)
		}
	}
	if r.Exclude == nil {
		t.Error("Recovery.exclude = nil, want [] (the design block ships an empty list)")
	}
}

// K3 (#596), the absent-file half of the "pin all four edits" invariant.
// LoadStartupConfig's absent-file branch (startup.go:40-42) returns
// defaultStartupConfig() WITHOUT calling validateStartupConfig, so a recovery
// default seeded only in the validate-fill leaves a factory with no startup.json
// holding an all-zero recovery block — threshold 0, dark_grace 0, rate_cap 0.
// Its sibling TestLoadStartupConfig_MissingRecoveryFieldDefaults covers the
// validated path; the PAIR pins BOTH seeds, neither alone does.
func TestLoadStartupConfig_MissingRecoveryBlockAbsentFileDefaults(t *testing.T) {
	dir := t.TempDir() // no startup.json — the unvalidated path

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("absent file must still load (C-4), got %v", err)
	}
	assertRecoveryDefaults(t, cfg.Recovery)
}

// The validated half of the same invariant: every startup.json in existence omits
// "recovery", so it unmarshals to an all-zero block that the relation checks would
// reject. Without the fill ahead of those checks every pre-existing file stops loading.
func TestLoadStartupConfig_MissingRecoveryFieldDefaults(t *testing.T) {
	dir := writeStartupRoot(t, `{"quality":"on","fidelity":"off","improvement":"on","telemetry":"on","start_dispatch":true}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("existing startup.json without \"recovery\" must still load, got %v", err)
	}
	assertRecoveryDefaults(t, cfg.Recovery)
}

// A partially-stated block keeps what the operator wrote and defaults the rest —
// tuning one knob must not require restating all fourteen.
func TestLoadStartupConfig_MissingRecoveryKeysFillIndividually(t *testing.T) {
	dir := writeStartupRoot(t, `{"recovery":{"context_threshold_pct":90}}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("a partial recovery block must load, got %v", err)
	}
	if cfg.Recovery.ContextThresholdPct != 90 {
		t.Errorf("stated context_threshold_pct = %d, want 90 (operator value must survive)", cfg.Recovery.ContextThresholdPct)
	}
	if cfg.Recovery.ConfirmTicks != 2 {
		t.Errorf("unstated confirm_ticks = %d, want the 2 default", cfg.Recovery.ConfirmTicks)
	}
	if cfg.Recovery.RateCapWindowSecs != 86400 {
		t.Errorf("unstated rate_cap_window_secs = %d, want the 86400 default", cfg.Recovery.RateCapWindowSecs)
	}
}

// design-doc.md:140 fixes absent ⇒ enabled. A plain bool cannot separate absent
// from an explicit false, so this test passes under the broken representation too —
// TestLoadStartupConfig_RecoveryEnabledFalseRoundTrip is the one that catches it.
func TestLoadStartupConfig_MissingRecoveryEnabledDefaultsTrue(t *testing.T) {
	dir := writeStartupRoot(t, `{"recovery":{"context_threshold_pct":85}}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Recovery.IsEnabled() {
		t.Error("a recovery block omitting \"enabled\" must read as enabled (design-doc.md:140)")
	}
}

// The knob that makes recovery opt-out-able. With `Enabled bool`, encoding/json
// decodes absent AND explicit false to the same zero, so the obvious fill
// (`if !Enabled { Enabled = true }`) makes "enabled": false unrepresentable and an
// operator can never turn recovery off. Only this test catches that.
func TestLoadStartupConfig_RecoveryEnabledFalseRoundTrip(t *testing.T) {
	dir := writeStartupRoot(t, `{"recovery":{"enabled":false}}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Recovery.IsEnabled() {
		t.Error("\"enabled\": false must turn recovery OFF; an explicit false must not be re-filled to true")
	}
}

// The design document's own example must be a valid document. Verbatim from
// design-doc.md:119-137 (14 keys).
func TestLoadStartupConfig_RecoveryRoundTrip(t *testing.T) {
	dir := writeStartupRoot(t, `{
  "watchdog_agents": ["manager"],
  "recovery": {
    "enabled": true,
    "context_threshold_pct": 85,
    "context_advisory_pct": 70,
    "confirm_ticks": 2,
    "staleness_secs": 180,
    "dark_grace_secs": 600,
    "post_recovery_progress_secs": 900,
    "progress_backstop_secs": 7200,
    "no_step_escalation_secs": 3600,
    "max_attempts": 3,
    "attempt_window_secs": 1800,
    "rate_cap_max": 6,
    "rate_cap_window_secs": 86400,
    "exclude": []
  }
}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("the design's own recovery block must validate, got %v", err)
	}
	assertRecoveryDefaults(t, cfg.Recovery)
}

// exclude carries no nil-vs-[] sentinel (unlike agents, where nil ⇒ ALL), but a
// stated list must survive decode — it is the operator's opt-out surface.
func TestLoadStartupConfig_RecoveryExcludeRoundTrip(t *testing.T) {
	dir := writeStartupRoot(t, `{"recovery":{"exclude":["manager","supervisor"]}}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg.Recovery.Exclude, []string{"manager", "supervisor"}) {
		t.Errorf("Recovery.exclude = %v, want [manager supervisor]", cfg.Recovery.Exclude)
	}
}

// The eight relation checks of design-doc.md Decision 9, one case per relation.
// Every violation must REJECT loudly — the watchdog.go:337-340 clamp shape is
// deliberately not copied, so nothing here may be silently corrected.
func TestLoadStartupConfig_BadRecoveryRelations(t *testing.T) {
	for _, tc := range []struct {
		name    string
		json    string
		wantKey string
	}{
		{"threshold above 99", `{"recovery":{"context_threshold_pct":100}}`, "context_threshold_pct"},
		{"threshold below 1", `{"recovery":{"context_threshold_pct":-1}}`, "context_threshold_pct"},
		{"advisory equals threshold", `{"recovery":{"context_threshold_pct":70,"context_advisory_pct":70}}`, "context_advisory_pct"},
		{"advisory above threshold", `{"recovery":{"context_threshold_pct":70,"context_advisory_pct":80}}`, "context_advisory_pct"},
		{"confirm_ticks below 1", `{"recovery":{"confirm_ticks":-1}}`, "confirm_ticks"},
		{"staleness below the refresh+tick floor", `{"recovery":{"staleness_secs":1}}`, "staleness_secs"},
		{"dark_grace equals staleness", `{"recovery":{"staleness_secs":180,"dark_grace_secs":180}}`, "dark_grace_secs"},
		{"dark_grace below staleness", `{"recovery":{"staleness_secs":300,"dark_grace_secs":200}}`, "dark_grace_secs"},
		{"no_step_escalation not positive", `{"recovery":{"no_step_escalation_secs":-1}}`, "no_step_escalation_secs"},
		{"rate_cap_max below 1", `{"recovery":{"rate_cap_max":-1}}`, "rate_cap_max"},
		{"rate_cap_window equals attempt_window", `{"recovery":{"attempt_window_secs":1800,"rate_cap_window_secs":1800}}`, "rate_cap_window_secs"},
		{"rate_cap_window below attempt_window", `{"recovery":{"attempt_window_secs":1800,"rate_cap_window_secs":600}}`, "rate_cap_window_secs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeStartupRoot(t, tc.json)

			_, err := LoadStartupConfig(dir)
			if !errors.Is(err, ErrInvalidType) {
				t.Fatalf("expected ErrInvalidType for %s, got %v", tc.name, err)
			}
			// The message must name the on-disk key so the operator can find it.
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Errorf("error must name %q, got %v", tc.wantKey, err)
			}
		})
	}
}

// The staleness floor is 3×(refresh+tick) with both constants pinned at 30, which
// makes the shipped 180 default sit EXACTLY on the floor. Raising either constant
// would make defaultStartupConfig() fail its own validator and every startup.json
// stop loading, so this guard is not optional.
func TestValidateStartupConfig_DefaultsAreSelfConsistent(t *testing.T) {
	if err := validateStartupConfig(defaultStartupConfig()); err != nil {
		t.Fatalf("the shipped defaults must satisfy the shipped validator, got %v", err)
	}
}

// TestRecoveryRefreshInterval_MatchesSettingsTemplates closes the drift hole that made the comment
// above only ASPIRATIONALLY true. recoveryRefreshIntervalSecs claims to be "the statusline
// refreshInterval registered by the settings template", but nothing connected the two: the
// constant lives here and the value lives in internal/claude's embedded JSON, so either could
// change alone and every test in the repo would stay green while the shipped staleness floor
// became a lie (issue #596).
//
// The templates are read from disk by relative path rather than imported: internal/claude imports
// internal/config, so the reverse import would cycle, and recoveryRefreshIntervalSecs is
// unexported so a test over in internal/claude could not see it. This is the only place both
// halves are reachable at once.
//
// The unit is SECONDS, verified against the installed Claude Code binary's own settings schema
// ("Re-run the status line command every N seconds") and confirmed empirically. A millisecond-
// shaped value like 30000 would be accepted silently and yield an ~8-hour cadence; a string "30"
// or a 0 is dropped to undefined and installs no timer at all. Both failure modes are invisible
// at runtime, which is why they are pinned here.
func TestRecoveryRefreshInterval_MatchesSettingsTemplates(t *testing.T) {
	for _, name := range []string{"settings-autonomous.json", "settings-interactive.json"} {
		path := filepath.Join("..", "claude", "config", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", name, err)
		}
		slRaw, ok := doc["statusLine"]
		if !ok {
			t.Fatalf("%s has no top-level statusLine key", name)
		}
		var sl map[string]any
		if err := json.Unmarshal(slRaw, &sl); err != nil {
			t.Fatalf("%s statusLine is not an object: %v", name, err)
		}

		got, present := sl["refreshInterval"]
		if !present {
			t.Fatalf("%s statusLine has no refreshInterval; without it Claude Code installs no "+
				"refresh timer, renders stay purely event-driven, and staleness is meaningless", name)
		}
		secs, isNumber := got.(float64)
		if !isNumber {
			t.Fatalf("%s statusLine.refreshInterval = %#v (%T), want a bare JSON number — a string "+
				"or bool is silently dropped by the host and installs no timer", name, got, got)
		}
		if secs != float64(recoveryRefreshIntervalSecs) {
			t.Errorf("%s statusLine.refreshInterval = %v, want %d to match recoveryRefreshIntervalSecs. "+
				"The staleness floor is 3x(refresh+tick); if the template's real cadence and this "+
				"constant disagree, the shipped staleness_secs default no longer means what its "+
				"derivation says it means.", name, secs, recoveryRefreshIntervalSecs)
		}
	}
}

// assertStepContextDefaults pins the shipped step_context block (#622 C1). Both seed sites must
// produce it — the absent-file literal in defaultStartupConfig and the validated fill — so both
// tests below assert through this one helper, exactly as assertRecoveryDefaults does for K3.
func assertStepContextDefaults(t *testing.T, sc StepContextConfig) {
	t.Helper()
	if sc.BoundTokens != 200000 {
		t.Errorf("StepContext.bound_tokens = %d, want 200000", sc.BoundTokens)
	}
	if sc.HandoffPct != 75 {
		t.Errorf("StepContext.handoff_pct = %d, want 75", sc.HandoffPct)
	}
}

// #622 C1, the "pin all seed sites" invariant. LoadStartupConfig's absent-file branch
// (startup.go:123-124) returns defaultStartupConfig() WITHOUT calling validateStartupConfig, so a
// default seeded only in the fill leaves a factory with no startup.json holding an all-zero
// step_context block — bound 0, handoff 0. The AbsentBlock subtest covers the validated path; the
// PAIR pins BOTH seeds, neither alone does.
func TestStartupStepContextDefaults(t *testing.T) {
	t.Run("AbsentFile", func(t *testing.T) {
		dir := t.TempDir() // no startup.json — the unvalidated path

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("absent file must still load (C-4), got %v", err)
		}
		assertStepContextDefaults(t, cfg.StepContext)
	})

	t.Run("AbsentBlock", func(t *testing.T) {
		// Every startup.json in existence omits "step_context", so it unmarshals to an all-zero
		// block that the ladder would otherwise reject.
		dir := writeStartupRoot(t, `{"quality":"on","telemetry":"on"}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("a startup.json without \"step_context\" must still load, got %v", err)
		}
		assertStepContextDefaults(t, cfg.StepContext)
	})

	t.Run("EmptyBlock", func(t *testing.T) {
		dir := writeStartupRoot(t, `{"step_context":{}}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("an empty step_context block must load, got %v", err)
		}
		assertStepContextDefaults(t, cfg.StepContext)
	})

	t.Run("StatedZerosAreDefaulted", func(t *testing.T) {
		// The house idiom (startup.go:195-203): a zero numeric means "absent", so a stated 0 is
		// defaulted rather than rejected. That is what makes the >=1 half of the ladder reachable
		// only for negatives, and it is stated here so the trade cannot be changed by accident.
		dir := writeStartupRoot(t, `{"step_context":{"bound_tokens":0,"handoff_pct":0}}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("a stated 0 must default, not reject, got %v", err)
		}
		assertStepContextDefaults(t, cfg.StepContext)
	})

	t.Run("ExplicitValuesSurvive", func(t *testing.T) {
		dir := writeStartupRoot(t, `{"step_context":{"bound_tokens":120000,"handoff_pct":80}}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("a valid explicit block must load, got %v", err)
		}
		if cfg.StepContext.BoundTokens != 120000 || cfg.StepContext.HandoffPct != 80 {
			t.Errorf("StepContext = %+v, want {120000 80} — a stated value must not be overwritten", cfg.StepContext)
		}
		if warning, ok := StepContextLint(cfg); ok {
			t.Errorf("a ladder that seats the default must not warn, got %q", warning)
		}
	})

	// The subtest above only shows silence on the DEFAULT ladder, where 75 fits and there is
	// nothing to warn about either way — it would stay green even if the lint ignored provenance
	// entirely. The warning exists to tell an operator that a value THEY DID NOT CHOOSE is not
	// where the documentation says; an operator who chose their own seat on a tight ladder has
	// nothing to learn from it and would carry the line on every invocation forever.
	t.Run("AnExplicitSeatOnATightLadderIsNotWarnedAbout", func(t *testing.T) {
		dir := writeStartupRoot(t, `{"recovery":{"context_threshold_pct":70,"context_advisory_pct":60},"step_context":{"handoff_pct":65}}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("an explicit handoff inside a tightened ladder must load, got %v", err)
		}
		if cfg.StepContext.HandoffPct != 65 {
			t.Fatalf("handoff_pct = %d, want the stated 65", cfg.StepContext.HandoffPct)
		}
		if warning, ok := StepContextLint(cfg); ok {
			t.Errorf("a handoff the operator stated must not be warned about, got %q", warning)
		}
	})
}

// The ladder relations of #622 C1, one case per relation. A value the operator ACTUALLY WROTE is
// rejected loudly, naming the on-disk key and the offending value — the validateRecoveryRelations
// idiom (startup.go:241-263). The clamp-and-warn half is HIGH-1 and applies only to values that
// are absent from disk; TestStartupStepContextUpgradeClamps covers it.
func TestStartupStepContextRejectsExplicitLadderViolations(t *testing.T) {
	// wantValues is asserted separately from wantKeys because the two halves of the message
	// contract fail independently. Naming the key tells the operator WHICH line of startup.json to
	// open; quoting the number tells them the line is the one they are looking at, and which of the
	// two bounds moved. A message that dropped its operands would still name every key, so keys
	// alone cannot pin the contract the Files-to-Modify row states.
	for _, tc := range []struct {
		name       string
		json       string
		wantKeys   []string
		wantValues []string
	}{
		{
			"handoff at the recovery threshold",
			`{"step_context":{"handoff_pct":85}}`,
			[]string{"handoff_pct", "context_threshold_pct"},
			[]string{"85"},
		},
		{
			"handoff above the recovery threshold",
			`{"step_context":{"handoff_pct":90}}`,
			[]string{"handoff_pct", "context_threshold_pct"},
			[]string{"90", "85"},
		},
		{
			"handoff below the recovery advisory",
			`{"step_context":{"handoff_pct":60}}`,
			[]string{"handoff_pct", "context_advisory_pct"},
			[]string{"60", "70"},
		},
		{
			"handoff above 99",
			`{"step_context":{"handoff_pct":100}}`,
			[]string{"handoff_pct"},
			[]string{"100"},
		},
		{
			"handoff below 1",
			`{"step_context":{"handoff_pct":-1}}`,
			[]string{"handoff_pct"},
			[]string{"-1"},
		},
		{
			"bound_tokens below 1",
			`{"step_context":{"bound_tokens":-1}}`,
			[]string{"bound_tokens"},
			[]string{"-1"},
		},
		{
			"explicit handoff that a tightened ladder cannot seat",
			`{"recovery":{"context_threshold_pct":70,"context_advisory_pct":60},"step_context":{"handoff_pct":75}}`,
			[]string{"handoff_pct", "context_threshold_pct"},
			[]string{"75", "70"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeStartupRoot(t, tc.json)

			_, err := LoadStartupConfig(dir)
			if !errors.Is(err, ErrInvalidType) {
				t.Fatalf("expected ErrInvalidType for %s, got %v", tc.name, err)
			}
			for _, k := range tc.wantKeys {
				if !strings.Contains(err.Error(), k) {
					t.Errorf("error must name %q so the operator can find it, got %v", k, err)
				}
			}
			for _, v := range tc.wantValues {
				if !strings.Contains(err.Error(), v) {
					t.Errorf("error must quote the offending value %q, got %v", v, err)
				}
			}
		})
	}
}

// HIGH-1 (#622 G5). An operator who legitimately tightened context_threshold_pct BEFORE upgrading
// has no step_context block on disk, and the shipped default of 75 does not fit under their
// threshold. A hard error here reaches seven consumers of LoadStartupConfig — up.go:112 wraps and
// returns it, blocking ALL agent launch, and watchdog.go:722 degrades to a disabled RecoveryConfig,
// silently turning recovery off. So an IMPLICIT default clamps and warns; it never load-fails.
func TestStartupStepContextUpgradeClamps(t *testing.T) {
	t.Run("ClampsBelowTheTightenedThreshold", func(t *testing.T) {
		dir := writeStartupRoot(t, `{"recovery":{"context_threshold_pct":70,"context_advisory_pct":60}}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("a tightened ladder with no step_context block must load, got %v", err)
		}
		if cfg.StepContext.HandoffPct != 69 {
			t.Errorf("effective handoff_pct = %d, want 69 = min(75, threshold-1)", cfg.StepContext.HandoffPct)
		}
		if cfg.StepContext.BoundTokens != 200000 {
			t.Errorf("bound_tokens = %d, want the 200000 default (the clamp is about the ladder only)", cfg.StepContext.BoundTokens)
		}
		warning, ok := StepContextLint(cfg)
		if !ok {
			t.Fatal("a derived handoff_pct that had to be clamped must warn; HIGH-1 says clamp AND warn")
		}
		for _, k := range []string{"handoff_pct", "context_threshold_pct", "69"} {
			if !strings.Contains(warning, k) {
				t.Errorf("warning must name %q, got %q", k, warning)
			}
		}
	})

	t.Run("FlooredAtTheAdvisory", func(t *testing.T) {
		// min(75, threshold-1) alone would derive 75 here, which is BELOW the advisory and would
		// put the cooperative boundary behind the advisory it is supposed to follow.
		dir := writeStartupRoot(t, `{"recovery":{"context_threshold_pct":85,"context_advisory_pct":80}}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("a raised advisory with no step_context block must load, got %v", err)
		}
		if cfg.StepContext.HandoffPct != 80 {
			t.Errorf("effective handoff_pct = %d, want 80 (floored at context_advisory_pct)", cfg.StepContext.HandoffPct)
		}
		if _, ok := StepContextLint(cfg); !ok {
			t.Error("a derived handoff_pct floored at the advisory must warn")
		}
	})

	t.Run("TouchingThresholdAndAdvisory", func(t *testing.T) {
		dir := writeStartupRoot(t, `{"recovery":{"context_threshold_pct":70,"context_advisory_pct":69}}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("advisory immediately below threshold must load, got %v", err)
		}
		if cfg.StepContext.HandoffPct != 69 {
			t.Errorf("effective handoff_pct = %d, want 69 — the only value the ladder admits", cfg.StepContext.HandoffPct)
		}
	})

	t.Run("DegenerateLadderStillLoads", func(t *testing.T) {
		// A ladder validateRecoveryRelations accepts but that no handoff value can satisfy:
		// threshold 1 leaves nothing below it. This is the strongest form of "a derived value
		// never load-fails" — the derivation has nowhere to put the value, and it still must not
		// take the factory down over a key the operator never wrote. It is also the case that
		// discriminates this ordering from the naive fill-then-validate one, which would reject.
		dir := writeStartupRoot(t, `{"recovery":{"context_threshold_pct":1,"context_advisory_pct":-1}}`)

		cfg, err := LoadStartupConfig(dir)
		if err != nil {
			t.Fatalf("a derived handoff_pct must never load-fail, even on a ladder that cannot seat one: %v", err)
		}
		if cfg.StepContext.HandoffPct < 1 {
			t.Errorf("effective handoff_pct = %d, want a value clamped into 1-99", cfg.StepContext.HandoffPct)
		}
		// B-1: for this degenerate ladder the clamp yields handoff == threshold == 1. It guarantees
		// an IN-RANGE value, NOT the strict handoff < threshold — pin what the code actually
		// guarantees so the reworded derivedHandoffPct comment cannot drift back into over-claiming.
		if cfg.StepContext.HandoffPct != 1 || cfg.StepContext.HandoffPct != cfg.Recovery.ContextThresholdPct {
			t.Errorf("degenerate ladder: handoff_pct = %d, threshold = %d; want handoff == threshold == 1",
				cfg.StepContext.HandoffPct, cfg.Recovery.ContextThresholdPct)
		}
	})

	t.Run("EveryLadderTheRecoveryValidatorAdmitsYieldsALoadableConfig", func(t *testing.T) {
		// The derived value must never be able to load-fail, for ANY recovery ladder that
		// validateRecoveryRelations itself accepts. Enumerating them is what proves the clamp is
		// total rather than tuned to the two fixtures above.
		for threshold := 2; threshold <= 99; threshold++ {
			for _, advisory := range []int{1, threshold / 2, threshold - 1} {
				if advisory < 1 || advisory >= threshold {
					continue
				}
				body := `{"recovery":{"context_threshold_pct":` + strconv.Itoa(threshold) +
					`,"context_advisory_pct":` + strconv.Itoa(advisory) + `}}`
				dir := writeStartupRoot(t, body)
				cfg, err := LoadStartupConfig(dir)
				if err != nil {
					t.Fatalf("threshold=%d advisory=%d: derived step_context must never load-fail, got %v", threshold, advisory, err)
				}
				h := cfg.StepContext.HandoffPct
				if h < advisory || h >= threshold {
					t.Fatalf("threshold=%d advisory=%d: derived handoff_pct = %d, outside [advisory, threshold)", threshold, advisory, h)
				}
			}
		}
	})
}
