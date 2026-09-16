package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installSeedDispatchJSON is the body internal/cmd/install.go:193 seeds into a fresh factory.
// Copied verbatim so that a future seed change which accidentally adds crons is caught here.
const installSeedDispatchJSON = `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`

// TestDispatchCron_ConfigLoad covers the crons-only acceptance path and the conditional emptiness
// relaxation (design AC-1; cross-review HIGH-1). The crons-only fixtures deliberately OMIT
// trigger_label as well as repos and mappings: HIGH-1 notes that a fixture which happens to carry a
// trigger_label passes even when the relaxation forgot that field, masking the bug.
func TestDispatchCron_ConfigLoad(t *testing.T) {
	t.Run("crons-only config with no repos, no mappings and no trigger_label is accepted", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{
			"crons": [{"name": "financial-patrol-wake", "agent": "financial-patrol", "every": "4h"}]
		}`)
		cfg, err := LoadDispatchConfig(dir)
		if err != nil {
			t.Fatalf("a crons-only config must be accepted; got: %v", err)
		}
		if len(cfg.Crons) != 1 {
			t.Fatalf("expected 1 cron, got %d", len(cfg.Crons))
		}
		c := cfg.Crons[0]
		if c.Name != "financial-patrol-wake" {
			t.Errorf("Name = %q, want %q", c.Name, "financial-patrol-wake")
		}
		if c.Agent != "financial-patrol" {
			t.Errorf("Agent = %q, want %q", c.Agent, "financial-patrol")
		}
		if c.Every != "4h" {
			t.Errorf("Every = %q, want %q", c.Every, "4h")
		}
		if len(c.Vars) != 0 {
			t.Errorf("absent vars must load empty (the bare re-sling shape), got %v", c.Vars)
		}
		if c.Model != "" {
			t.Errorf("absent model must load empty, got %q", c.Model)
		}
	})

	// The relaxation must be a guard around the emptiness block, never an early return: the
	// interval/retry/notify defaults live further down validateDispatchConfig, and skipping them
	// would hand the Phase-2 dispatcher a zero poll interval.
	t.Run("a crons-only config still receives the dispatcher defaults", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{
			"crons": [{"name": "wake", "agent": "patrol", "every": "1h"}]
		}`)
		cfg, err := LoadDispatchConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.IntervalSecs != 300 {
			t.Errorf("IntervalSecs = %d, want the 300 default — a zero interval polls in a hot loop", cfg.IntervalSecs)
		}
		if cfg.RetryAfterSecs != 1800 {
			t.Errorf("RetryAfterSecs = %d, want the 1800 default", cfg.RetryAfterSecs)
		}
		if cfg.NotifyOnComplete != defaultNotifyAgent {
			t.Errorf("NotifyOnComplete = %q, want the %q default", cfg.NotifyOnComplete, defaultNotifyAgent)
		}
	})

	// The boundary a `cfg.Crons != nil` implementation gets wrong: an empty array declares no
	// schedules, so it must not buy the relaxation.
	t.Run("an empty crons array does not relax the emptiness checks", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{"repos": [], "trigger_label": "", "mappings": [], "crons": []}`)
		_, err := LoadDispatchConfig(dir)
		if err == nil {
			t.Fatal("an empty crons array declares no schedules and must not relax the emptiness checks")
		}
		if !errors.Is(err, ErrMissingField) {
			t.Errorf("the crons-less rejection must keep wrapping ErrMissingField; got: %v", err)
		}
		if !strings.Contains(err.Error(), "at least one repo") {
			t.Errorf("expected the unchanged repos message; got: %v", err)
		}
	})

	t.Run("crons and a full GitHub section coexist", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{
			"repos": ["owner/repo"],
			"trigger_label": "agentic",
			"mappings": [{"labels": ["build"], "agent": "builder"}],
			"crons": [{"name": "wake", "agent": "patrol", "every": "1d"}]
		}`)
		cfg, err := LoadDispatchConfig(dir)
		if err != nil {
			t.Fatalf("crons must not become mutually exclusive with items; got: %v", err)
		}
		if len(cfg.Mappings) != 1 || len(cfg.Crons) != 1 {
			t.Errorf("expected 1 mapping and 1 cron, got %d and %d", len(cfg.Mappings), len(cfg.Crons))
		}
	})

	// The gate is len(Crons) > 0, not "crons-only": a config carrying both crons and a full GitHub
	// section may also omit trigger_label. Pinned so the breadth of the relaxation is a recorded
	// decision rather than an accident.
	t.Run("crons plus repos and mappings but no trigger_label is accepted", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{
			"repos": ["owner/repo"],
			"mappings": [{"labels": ["build"], "agent": "builder"}],
			"crons": [{"name": "wake", "agent": "patrol", "every": "1d"}]
		}`)
		if _, err := LoadDispatchConfig(dir); err != nil {
			t.Fatalf("the relaxation gates on len(Crons) > 0, so trigger_label may be empty; got: %v", err)
		}
	})

	t.Run("absent, empty and populated vars are all valid", func(t *testing.T) {
		bodies := map[string]string{
			"absent vars":     `{"crons": [{"name": "a", "agent": "p", "every": "1h"}]}`,
			"empty vars":      `{"crons": [{"name": "a", "agent": "p", "every": "1h", "vars": {}}]}`,
			"populated vars":  `{"crons": [{"name": "a", "agent": "p", "every": "1h", "vars": {"repo": "owner/repo"}}]}`,
			"underscore keys": `{"crons": [{"name": "a", "agent": "p", "every": "1h", "vars": {"_strategy_file": "todos/STRATEGY.md"}}]}`,
		}
		for name, body := range bodies {
			t.Run(name, func(t *testing.T) {
				if _, err := LoadDispatchConfig(writeDispatchJSON(t, body)); err != nil {
					t.Fatalf("%s must be valid; got: %v", name, err)
				}
			})
		}
	})

	t.Run("an optional per-cron model loads", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{
			"crons": [{"name": "a", "agent": "p", "every": "1h", "model": "codex"}]
		}`)
		cfg, err := LoadDispatchConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Crons[0].Model != "codex" {
			t.Errorf("Model = %q, want %q", cfg.Crons[0].Model, "codex")
		}
	})

	// HIGH-1's other half: a crons-only config that loads but cannot be WRITTEN is still broken,
	// because `af config dispatch set` funnels through the same validator (dispatch.go:78).
	t.Run("a crons-only config can be saved and re-loaded", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{"crons": [{"name": "a", "agent": "p", "every": "1h"}]}`)
		cfg, err := LoadDispatchConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := SaveDispatchConfig(DispatchConfigPath(dir), cfg); err != nil {
			t.Fatalf("a crons-only config must be writable; got: %v", err)
		}
		reloaded, err := LoadDispatchConfig(dir)
		if err != nil {
			t.Fatalf("the written crons-only config must re-load; got: %v", err)
		}
		if len(reloaded.Crons) != 1 || reloaded.Crons[0].Every != "1h" {
			t.Errorf("round trip lost the schedule: %+v", reloaded.Crons)
		}
	})
}

// TestDispatchCron_LegacyConfigUnchanged is the regression half of design AC-5: every config without
// a crons section must behave exactly as it does today, in message text as well as sentinel class.
// The relaxation rewrites the three lines these assertions cover.
func TestDispatchCron_LegacyConfigUnchanged(t *testing.T) {
	t.Run("a legacy config loads with no crons", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{
			"repos": ["owner/repo"],
			"trigger_label": "agentic",
			"mappings": [{"labels": ["build"], "agent": "builder"}]
		}`)
		cfg, err := LoadDispatchConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg.Crons) != 0 {
			t.Errorf("expected no crons (truthful absent default), got %d", len(cfg.Crons))
		}
	})

	// The end state's "every legacy config stays byte-identical" clause. Nothing else in the suite
	// tests it, and it breaks silently the moment someone drops the omitempty tag — no struct-level
	// assertion can see that. save_test.go:206-212 uses the same read-raw-and-grep technique.
	t.Run("saving a legacy config introduces no crons key", func(t *testing.T) {
		dir := writeDispatchJSON(t, `{
			"repos": ["owner/repo"],
			"trigger_label": "agentic",
			"mappings": [{"labels": ["build"], "agent": "builder"}]
		}`)
		cfg, err := LoadDispatchConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := SaveDispatchConfig(DispatchConfigPath(dir), cfg); err != nil {
			t.Fatalf("save: %v", err)
		}
		raw, err := os.ReadFile(DispatchConfigPath(dir))
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if strings.Contains(string(raw), "crons") {
			t.Errorf("an absent crons section must round-trip to an absent key (omitempty); got:\n%s", raw)
		}
	})

	// Each rejection gets its own single-fault body: one body missing all three would only ever
	// reach the first check, so it could not prove the other two survived the refactor.
	t.Run("the crons-less emptiness rejections are unchanged", func(t *testing.T) {
		cases := []struct {
			name string
			body string
			want string
		}{
			{
				name: "no repos",
				body: `{"repos": [], "trigger_label": "agentic", "mappings": [{"labels": ["b"], "agent": "a"}]}`,
				want: "dispatch config must have at least one repo",
			},
			{
				name: "no trigger_label",
				body: `{"repos": ["owner/repo"], "trigger_label": "", "mappings": [{"labels": ["b"], "agent": "a"}]}`,
				want: "dispatch config must have a trigger_label",
			},
			{
				name: "no mappings",
				body: `{"repos": ["owner/repo"], "trigger_label": "agentic", "mappings": []}`,
				want: "dispatch config must have at least one mapping",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := LoadDispatchConfig(writeDispatchJSON(t, tc.body))
				if err == nil {
					t.Fatalf("%s must still be rejected", tc.name)
				}
				if !errors.Is(err, ErrMissingField) {
					t.Errorf("the legacy rejection must keep wrapping ErrMissingField — startDispatch "+
						"friendly-skips on that sentinel and every unconfigured factory depends on it; got: %v", err)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("message must stay %q; got: %v", tc.want, err)
				}
			})
		}
	})

	t.Run("the install seed is still rejected so af up keeps friendly-skipping", func(t *testing.T) {
		_, err := LoadDispatchConfig(writeDispatchJSON(t, installSeedDispatchJSON))
		if err == nil {
			t.Fatal("the install seed must still fail validation, or a fresh factory stops friendly-skipping")
		}
		if !errors.Is(err, ErrMissingField) {
			t.Errorf("the seed rejection must wrap ErrMissingField; got: %v", err)
		}
	})
}

// TestDispatchCron_Rejections pins the struct-level rejection classes AND the error-class contract
// (cross-review CRITICAL-1, design AC-5).
//
// The load-bearing assertion is the NEGATIVE one: no cron-validation error may satisfy
// errors.Is(err, ErrMissingField). startDispatch (internal/cmd/dispatch.go:1632-1637) reads that
// sentinel as "dispatch not configured" and friendly-skips the WHOLE dispatcher, so a
// sentinel-classed cron error would turn one hand-edited `every` value into a silent, total dispatch
// outage. The sibling validateWorkflows wraps its empty-label check (dispatch.go:214) and emits a
// plain error three lines later (:217); :217 is the model.
//
// Cross-file classes (unknown agent, agent without a formula, unknown model) are NOT covered here:
// the struct-level validator has no agents.json, and dependencies.md:61 build step 3 assigns those
// to Phase 2's checkCronRefs in internal/cmd (the formula-bearing half cannot live in this package
// at all — internal/formula imports internal/config; see the note at dispatch.go:148-154).
func TestDispatchCron_Rejections(t *testing.T) {
	cases := []struct {
		name    string
		crons   string
		wantErr bool
		want    string // required substring of the error message
	}{
		// Positive controls — without these a validateCrons that rejects everything scores 100%.
		{
			name:  "accepts a bare valid schedule",
			crons: `{"name": "wake", "agent": "patrol", "every": "4h"}`,
		},
		{
			name:  "accepts two distinctly-named schedules on one agent",
			crons: `{"name": "fast", "agent": "patrol", "every": "1h"}, {"name": "slow", "agent": "patrol", "every": "14d"}`,
		},
		{
			name:  "accepts legal var keys",
			crons: `{"name": "wake", "agent": "patrol", "every": "1h", "vars": {"repo": "o/r", "_x": "1", "a1_B2": "2"}}`,
		},

		// name
		{
			name:    "rejects an empty name",
			crons:   `{"name": "", "agent": "patrol", "every": "1h"}`,
			wantErr: true, want: "name",
		},
		{
			name:    "rejects a duplicate name",
			crons:   `{"name": "wake", "agent": "patrol", "every": "1h"}, {"name": "wake", "agent": "other", "every": "2h"}`,
			wantErr: true, want: `"wake"`,
		},

		// agent
		{
			name:    "rejects an empty agent",
			crons:   `{"name": "wake", "agent": "", "every": "1h"}`,
			wantErr: true, want: "agent",
		},

		// every
		{
			name:    "rejects an unparseable every unit",
			crons:   `{"name": "weekly-pm", "agent": "patrol", "every": "1w"}`,
			wantErr: true, want: `cron "weekly-pm": invalid every "1w": expected <integer><unit> with unit m, h, or d (e.g. "4h", "14d")`,
		},
		{
			name:    "rejects an absent every",
			crons:   `{"name": "wake", "agent": "patrol"}`,
			wantErr: true, want: "m, h, or d",
		},
		{
			name:    "rejects a zero every",
			crons:   `{"name": "wake", "agent": "patrol", "every": "0m"}`,
			wantErr: true, want: "m, h, or d",
		},
		{
			name:    "rejects a fractional every",
			crons:   `{"name": "wake", "agent": "patrol", "every": "1.5h"}`,
			wantErr: true, want: "m, h, or d",
		},
		{
			name:    "rejects an overflowing every",
			crons:   `{"name": "wake", "agent": "patrol", "every": "9223372036854775807m"}`,
			wantErr: true, want: "too large",
		},

		// var keys — data.md:31-35: keys ride into `--var k=v` argv, which parseCLIVars
		// (internal/cmd/sling.go:648-658) splits on the FIRST '='.
		{
			name:    "rejects a var key with a space",
			crons:   `{"name": "wake", "agent": "patrol", "every": "1h", "vars": {"BAD KEY": "v"}}`,
			wantErr: true, want: `"BAD KEY"`,
		},
		{
			name:    "rejects a var key with a hyphen",
			crons:   `{"name": "wake", "agent": "patrol", "every": "1h", "vars": {"BAD-KEY": "v"}}`,
			wantErr: true, want: `"BAD-KEY"`,
		},
		{
			name:    "rejects a var key with an equals sign",
			crons:   `{"name": "wake", "agent": "patrol", "every": "1h", "vars": {"BAD=KEY": "v"}}`,
			wantErr: true, want: `"BAD=KEY"`,
		},
		{
			name:    "rejects a var key with a leading digit",
			crons:   `{"name": "wake", "agent": "patrol", "every": "1h", "vars": {"1BAD": "v"}}`,
			wantErr: true, want: `"1BAD"`,
		},
		{
			name:    "rejects an empty var key",
			crons:   `{"name": "wake", "agent": "patrol", "every": "1h", "vars": {"": "v"}}`,
			wantErr: true, want: "var key",
		},
		{
			name:    "rejects a cron whose vars are all malformed",
			crons:   `{"name": "wake", "agent": "patrol", "every": "1h", "vars": {"ZZ-BAD": "v", "BAD KEY": "v"}}`,
			wantErr: true, want: "invalid var key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every fixture is crons-only, so reaching validateCrons at all proves the relaxation
			// works; and a broken relaxation would surface as the repos/ErrMissingField error, which
			// both assertions below catch.
			body := `{"crons": [` + tc.crons + `]}`
			_, err := LoadDispatchConfig(writeDispatchJSON(t, body))

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("this schedule is well-formed and must be accepted; got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("this schedule must be rejected: %s", tc.crons)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error must name the offending value and the violated rule (%q); got: %v", tc.want, err)
			}
			if errors.Is(err, ErrMissingField) {
				t.Errorf("cron validation errors must NOT wrap ErrMissingField — startDispatch "+
					"(internal/cmd/dispatch.go:1632-1637) reads that sentinel as \"dispatch not configured\" "+
					"and friendly-skips the WHOLE dispatcher, so one bad schedule would silently kill items "+
					"and crons alike; got: %v", err)
			}
		})
	}
}

// Go randomizes map iteration, so a cron carrying several malformed var keys would report a
// different one on every load — the operator fixes the named key, re-runs, and is handed another.
// validateCrons sorts before reporting; this pins that the message is stable. Five bad keys over
// twenty runs makes an unsorted implementation fail with probability ~1-5^-19.
func TestDispatchCron_MultipleBadVarKeysReportDeterministically(t *testing.T) {
	body := `{"crons": [{"name": "wake", "agent": "patrol", "every": "1h",
		"vars": {"e-bad": "v", "d bad": "v", "c=bad": "v", "b.bad": "v", "9bad": "v"}}]}`
	dir := writeDispatchJSON(t, body)

	var first string
	for i := 0; i < 20; i++ {
		_, err := LoadDispatchConfig(dir)
		if err == nil {
			t.Fatal("a cron with five malformed var keys must be rejected")
		}
		if i == 0 {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("the reported var key must not depend on map iteration order;\n run 1: %s\n run %d: %s", first, i+1, err)
		}
	}
	if !strings.Contains(first, `"9bad"`) {
		t.Errorf("expected the lexicographically first bad key to be reported; got: %s", first)
	}
}

// A malformed schedule must be rejected on the WRITE path too, not only on load: `af config dispatch
// set` funnels through the same struct-level validator (dispatch.go:78), so a bad schedule can never
// be persisted for a later load to trip over.
func TestDispatchCron_SaveRejectsMalformed(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &DispatchConfig{Crons: []CronSchedule{{Name: "wake", Agent: "patrol", Every: "1w"}}}
	err := SaveDispatchConfig(DispatchConfigPath(dir), cfg)
	if err == nil {
		t.Fatal("SaveDispatchConfig must reject an unparseable every")
	}
	if errors.Is(err, ErrMissingField) {
		t.Errorf("the write-path rejection must not be sentinel-classed either; got: %v", err)
	}
	if _, statErr := os.Stat(DispatchConfigPath(dir)); !os.IsNotExist(statErr) {
		t.Error("a rejected write must not create the file")
	}
}
