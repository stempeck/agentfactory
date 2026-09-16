package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
)

// Fixture formulas for the cron cross-file checks (issue #610 Phase 2).
//
// The names carry an af610- prefix on purpose: FindFormulaFile (internal/formula/discover.go:34-37)
// falls through to $HOME/.agentfactory/store/formulas after the factory root, so a fixture sharing a
// name with a real shipped formula could resolve from the developer's home directory and let the
// test pass for the wrong reason.
const (
	// The product-manager shape (Gotcha 6 / cross-review HIGH-3): `issue` is required, source cli,
	// no default, and a scheduled sling has neither a positional task nor a hooked bead to supply
	// it — so this agent is unschedulable until the formula changes. Deliberately declares NO
	// default_branch var, so it is the fixture that proves the ambient-token allowlist is load-
	// bearing (financial-patrol declares default_branch as an input and would pass vacuously).
	cronPMFormulaTOML = `formula = "af610-pm-formula"
type = "workflow"
version = 1

[vars.issue]
description = "The issue/bead ID assigned to this strategy cycle"
required = true
source = "cli"

[vars.repo]
description = "Target repository"
required = false
default = "o/r"
source = "cli"

[vars.max_issues_per_run]
description = "Cap per cycle"
required = false
default = "5"
source = "cli"

[[steps]]
id = "s1"
title = "work {{issue}}"
`

	// The financial-patrol shape: a workflow whose every input is optional or defaulted, i.e. the
	// bare re-sling AC-1 blessed. Satisfiable with an empty vars map.
	cronPatrolFormulaTOML = `formula = "af610-patrol-formula"
type = "workflow"
version = 1

[inputs.cycle_directive]
description = "Optional directive"
type = "string"
required = false
default = "routine"

[[steps]]
id = "s1"
title = "wake"
`

	// Required vars whose values a scheduled sling could never carry: no hooked bead exists on the
	// cron path, and no assignment bead is auto-created (sling.go:505 requires a non-empty task).
	// All three bead sources are declared because all three fail identically at ResolveVars, and
	// listing a source in the coverage set without a fixture leaves it asserted by inspection only.
	cronBeadFormulaTOML = `formula = "af610-bead-formula"
type = "workflow"
version = 1

[vars.ticket]
description = "Read from the hooked bead"
required = true
source = "hook_bead"

[vars.headline]
description = "Title of the hooked bead"
required = true
source = "bead_title"

[vars.body]
description = "Description of the hooked bead"
required = true
source = "bead_description"

[[steps]]
id = "s1"
title = "wake"
`

	// env-source required vars are the dispatcher's business at fire time, not the operator's shell
	// at set time — deferred, never rejected here.
	cronEnvFormulaTOML = `formula = "af610-env-formula"
type = "workflow"
version = 1

[vars.token]
description = "From the environment"
required = true
source = "env"

[[steps]]
id = "s1"
title = "wake"
`

	// ResolveVars skips deferred vars outright (internal/formula/vars.go:51-53), so demanding one
	// at set time would reject a schedule that fires perfectly well.
	cronDeferredFormulaTOML = `formula = "af610-deferred-formula"
type = "workflow"
version = 1

[vars.later]
description = "Resolved downstream"
required = true
source = "deferred"

[[steps]]
id = "s1"
title = "wake"
`

	// Required, but carrying a default — resolveVar's cli fallthrough returns the default without
	// error, so this is satisfied without a cron var.
	cronDefaultedFormulaTOML = `formula = "af610-defaulted-formula"
type = "workflow"
version = 1

[vars.mode]
description = "Has a default"
required = true
default = "standard"
source = "cli"

[[steps]]
id = "s1"
title = "wake"
`

	// A convoy formula: sling merges inputs into vars for WORKFLOW formulas only
	// (sling.go:536-543), resolving f.Vars alone for every other type. A required input here is
	// never resolved and must therefore never be demanded, or the setter rejects a schedule the
	// fire path would run without complaint.
	cronConvoyFormulaTOML = `formula = "af610-convoy-formula"
type = "convoy"
version = 1

[inputs.target]
description = "Never resolved for a convoy"
type = "string"
required = true

[[legs]]
id = "leg1"
title = "analyze"
`
)

// setupCronFactory extends setupConfigFactory with the two artifact classes the cron cross-checks
// need and that it does not provide: agents.json entries carrying a `formula` field, and the
// formula files themselves on disk.
func setupCronFactory(t *testing.T) string {
	t.Helper()
	root := setupConfigFactory(t)

	// The setter probes the dispatcher session to decide whether to warn that nothing will fire.
	// Faking the seam keeps that probe off a real `tmux has-session` subprocess and, more usefully,
	// makes the dispatcher-running arm reachable at all — the guarded real client always reports
	// not-running inside a test binary, so without this every test could only ever see one branch.
	installFakeTmuxPresent(t)

	if err := os.WriteFile(config.AgentsConfigPath(root), []byte(
		`{"agents":{`+
			`"manager":{"type":"interactive","description":"m"},`+
			`"af610-pm":{"type":"autonomous","description":"pm","formula":"af610-pm-formula"},`+
			`"af610-patrol":{"type":"autonomous","description":"p","formula":"af610-patrol-formula"},`+
			`"af610-bead":{"type":"autonomous","description":"b","formula":"af610-bead-formula"},`+
			`"af610-env":{"type":"autonomous","description":"e","formula":"af610-env-formula"},`+
			`"af610-deferred":{"type":"autonomous","description":"d","formula":"af610-deferred-formula"},`+
			`"af610-defaulted":{"type":"autonomous","description":"f","formula":"af610-defaulted-formula"},`+
			`"af610-convoy":{"type":"autonomous","description":"c","formula":"af610-convoy-formula"},`+
			`"af610-noformula":{"type":"autonomous","description":"n"},`+
			`"af610-ghostformula":{"type":"autonomous","description":"g","formula":"af610-does-not-exist"}}}`,
	), 0o644); err != nil {
		t.Fatalf("agents.json: %v", err)
	}

	dir := config.FormulasDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas: %v", err)
	}
	for name, body := range map[string]string{
		"af610-pm-formula":        cronPMFormulaTOML,
		"af610-patrol-formula":    cronPatrolFormulaTOML,
		"af610-bead-formula":      cronBeadFormulaTOML,
		"af610-env-formula":       cronEnvFormulaTOML,
		"af610-deferred-formula":  cronDeferredFormulaTOML,
		"af610-defaulted-formula": cronDefaultedFormulaTOML,
		"af610-convoy-formula":    cronConvoyFormulaTOML,
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".formula.toml"), []byte(body), 0o644); err != nil {
			t.Fatalf("write formula %s: %v", name, err)
		}
	}
	return root
}

// seedGoodDispatch writes a valid dispatch.json so a rejection can be proven to leave it
// byte-for-byte unchanged, and returns its bytes.
func seedGoodDispatch(t *testing.T, root string) []byte {
	t.Helper()
	good := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"manager"}],"notify_on_complete":"manager"}`
	if err := os.WriteFile(config.DispatchConfigPath(root), []byte(good), 0o644); err != nil {
		t.Fatalf("seed dispatch.json: %v", err)
	}
	before, err := os.ReadFile(config.DispatchConfigPath(root))
	if err != nil {
		t.Fatalf("read seeded dispatch.json: %v", err)
	}
	return before
}

// TestConfigSet_CronSatisfiability discharges AC-5 (cross-file rejection classes) and AC-6's
// set-time arm (design-doc.md:252-266, N4 + N5(a)).
//
// A cron schedule fires with no triggering issue, no positional task and no hooked bead. Every
// class below is accepted by the setter today and then fails — silently or loudly — at every
// single fire, forever: for a 14d cadence that is a misconfiguration window measured in months.
// The whole point of the check is to move that discovery to the one moment the operator is
// looking at the document.
func TestConfigSet_CronSatisfiability(t *testing.T) {
	// Positive controls first: without these, a checkCronRefs that rejected everything would
	// score 100% on the negative cases.
	t.Run("a formula whose inputs are all defaulted is schedulable with no vars", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h"}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("the bare re-sling shape must be accepted; err=%v out=%q", err, out)
		}
	})

	t.Run("declaring the required var makes the schedule acceptable", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"weekly-pm","agent":"af610-pm","every":"14d","vars":{"issue":"bd-1"}}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("a satisfied schedule must be accepted; err=%v out=%q", err, out)
		}
	})

	t.Run("a required cli var with no default must be declared in the cron vars", func(t *testing.T) {
		root := setupCronFactory(t)
		before := seedGoodDispatch(t, root)

		body := `{"crons":[{"name":"weekly-pm","agent":"af610-pm","every":"14d"}]}`
		_, err := runConfigSet(t, runConfigDispatchSet, body)
		if err == nil {
			t.Fatal("a schedule whose formula requires an undeclarable cli var must be rejected at " +
				"write time — it would hard-error at ResolveVars on every single fire, forever")
		}
		// Actionable means: which schedule, which agent, which var, where the value comes from.
		for _, want := range []string{"weekly-pm", "af610-pm", `"issue"`, "cli"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("rejection must name %q so the operator can act on it; got: %v", want, err)
			}
		}

		after, _ := os.ReadFile(config.DispatchConfigPath(root))
		if !bytes.Equal(before, after) {
			t.Errorf("dispatch.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
		}

		// The seeded arm alone would still pass if the check ran AFTER a write that happened to
		// reproduce identical bytes. On a fresh factory a rejection must create nothing at all.
		fresh := setupCronFactory(t)
		if _, err := runConfigSet(t, runConfigDispatchSet, body); err == nil {
			t.Fatal("expected the same rejection on a fresh factory")
		}
		if _, err := os.Stat(config.DispatchConfigPath(fresh)); !os.IsNotExist(err) {
			t.Errorf("a rejected write created dispatch.json (stat err = %v)", err)
		}
	})

	t.Run("required bead-source vars are rejected like cli-source ones", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"bead-cron","agent":"af610-bead","every":"4h"}]}`
		_, err := runConfigSet(t, runConfigDispatchSet, body)
		if err == nil {
			t.Fatal("a scheduled sling has no hooked bead and creates no assignment bead, so a " +
				"required bead-source var fails at every fire exactly like a cli one")
		}
		// All three bead sources must be named: each is separately listed in the coverage set, and
		// each would otherwise be a source that ships unasserted.
		for _, want := range []string{"ticket", "hook_bead", "headline", "bead_title", "body", "bead_description"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("rejection must name %q; got: %v", want, err)
			}
		}
	})

	t.Run("a required env-source var is deferred to fire time, not rejected", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"env-cron","agent":"af610-env","every":"4h"}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("the dispatcher's environment, not the operator's shell, is authoritative for "+
				"env-source vars; err=%v out=%q", err, out)
		}
	})

	t.Run("a required deferred-source var is skipped", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"deferred-cron","agent":"af610-deferred","every":"4h"}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("ResolveVars skips deferred vars outright, so demanding one would reject a "+
				"schedule that fires fine; err=%v out=%q", err, out)
		}
	})

	t.Run("a required var carrying a default needs no declaration", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"defaulted-cron","agent":"af610-defaulted","every":"4h"}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("a default satisfies a required var without a cron var; err=%v out=%q", err, out)
		}
	})

	t.Run("a non-workflow formula does not have its inputs demanded", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"convoy-cron","agent":"af610-convoy","every":"4h"}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("sling merges inputs into vars for workflow formulas only (sling.go:536-543); "+
				"demanding a convoy's input rejects a schedule the fire path would run; err=%v out=%q", err, out)
		}
	})

	t.Run("an agent absent from agents.json is rejected", func(t *testing.T) {
		root := setupCronFactory(t)
		before := seedGoodDispatch(t, root)

		body := `{"crons":[{"name":"ghost-cron","agent":"ghost","every":"4h"}]}`
		_, err := runConfigSet(t, runConfigDispatchSet, body)
		if err == nil {
			t.Fatal("a schedule naming an agent that does not exist can never fire")
		}
		if !strings.Contains(err.Error(), "ghost") {
			t.Errorf("rejection must name the unknown agent; got: %v", err)
		}
		after, _ := os.ReadFile(config.DispatchConfigPath(root))
		if !bytes.Equal(before, after) {
			t.Error("dispatch.json was modified on a rejected write")
		}
	})

	t.Run("an agent with no formula field is rejected", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"noformula-cron","agent":"af610-noformula","every":"4h"}]}`
		_, err := runConfigSet(t, runConfigDispatchSet, body)
		if err == nil {
			t.Fatal("a schedule can only fire a formula-bearing agent")
		}
		if !strings.Contains(err.Error(), "af610-noformula") {
			t.Errorf("rejection must name the agent; got: %v", err)
		}
	})

	t.Run("an agent whose formula does not resolve is rejected", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"ghostformula-cron","agent":"af610-ghostformula","every":"4h"}]}`
		_, err := runConfigSet(t, runConfigDispatchSet, body)
		if err == nil {
			t.Fatal("an unresolvable formula brings the schedule down at every fire")
		}
		if !strings.Contains(err.Error(), "af610-does-not-exist") {
			t.Errorf("rejection must name the formula that could not be found; got: %v", err)
		}
	})

	t.Run("a model absent from a populated registry is rejected", func(t *testing.T) {
		root := setupCronFactory(t)
		if err := os.WriteFile(config.ModelsConfigPath(root),
			[]byte(`{"models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8"}}}`), 0o644); err != nil {
			t.Fatalf("seed models.json: %v", err)
		}
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h","model":"ghost-model"}]}`
		_, err := runConfigSet(t, runConfigDispatchSet, body)
		if err == nil {
			t.Fatal("a cron pinning a model the registry does not define must be rejected, matching " +
				"the per-mapping model cross-check")
		}
		if !strings.Contains(err.Error(), "ghost-model") {
			t.Errorf("rejection must name the undefined model; got: %v", err)
		}
	})

	t.Run("the model check is skipped when no registry is present", func(t *testing.T) {
		setupCronFactory(t)
		// No models.json at all: with no registry a model name is a raw id handed straight to the
		// CLI, so rejecting it would break every factory that never ran `af config models set`.
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h","model":"claude-opus-4-8"}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("the non-selecting model cross-check must stay tolerant; err=%v out=%q", err, out)
		}
	})

	t.Run("the rejection message is byte-identical to the design's model message", func(t *testing.T) {
		// data.md:148 quotes the exact string an operator is meant to read. Substring assertions
		// elsewhere in this file would survive a rewording that silently drops the source or the
		// remedy, so the model message is pinned whole, once, at the unit boundary.
		err := checkCronVarCoverage(
			config.CronSchedule{Name: "weekly-pm", Agent: "product-manager"},
			map[string]formula.Var{"issue": {Required: true, Source: "cli"}},
		)
		want := `cron "weekly-pm": agent "product-manager" formula requires var "issue" (source cli, no default) — declare it in vars or it cannot be slung on a schedule`
		if err == nil {
			t.Fatal("a required cli var with no default and no cron var must be rejected")
		}
		if err.Error() != want {
			t.Errorf("\n got: %s\nwant: %s", err.Error(), want)
		}
	})
}

// TestConfigSet_CronDeclaration discharges the declaration direction of N5 (cross-review HIGH-2,
// design-doc.md:76).
//
// ResolveVars iterates the formula's DECLARED vars only (internal/formula/vars.go:50), so a --var
// whose key the formula never declared is silently dropped. An operator typo is therefore accepted
// at set time and does nothing at every fire — the failure mode with the longest half-life in the
// whole feature, because nothing anywhere ever says a word about it.
//
// security.md §A3 concluded "warn, don't reject" for this class; it is superseded by
// design-doc.md:76 and cross-review HIGH-2, both of which require REJECT with the formula's
// declared var names listed.
func TestConfigSet_CronDeclaration(t *testing.T) {
	t.Run("a declared var key is accepted", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"weekly-pm","agent":"af610-pm","every":"14d","vars":{"issue":"bd-1","repo":"o/r"}}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("a key the formula declares must be accepted; err=%v out=%q", err, out)
		}
	})

	t.Run("a key declared only as a workflow input is accepted", func(t *testing.T) {
		setupCronFactory(t)
		// MergeInputsToVars promotes an input to a declared var for workflow formulas, and sling
		// resolves against exactly that merged map — so the setter must too.
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h","vars":{"cycle_directive":"deep"}}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("a workflow input is a declared var after the merge; err=%v out=%q", err, out)
		}
	})

	t.Run("the ambient token default_branch is not rejected", func(t *testing.T) {
		setupCronFactory(t)
		// af610-pm-formula deliberately does NOT declare default_branch: sling consumes it out of
		// cliVars undeclared (sling.go:569-581), so it is the single allowlisted exception and this
		// assertion is not vacuous.
		body := `{"crons":[{"name":"weekly-pm","agent":"af610-pm","every":"14d","vars":{"issue":"bd-1","default_branch":"main"}}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("default_branch is the sole ambient-token allowlist key; err=%v out=%q", err, out)
		}
	})

	t.Run("a typo'd var key is rejected and the declared names are listed", func(t *testing.T) {
		root := setupCronFactory(t)
		before := seedGoodDispatch(t, root)

		// max_issue_per_run for max_issues_per_run — POSIX-legal, so Phase 1's shape check passes it.
		body := `{"crons":[{"name":"weekly-pm","agent":"af610-pm","every":"14d","vars":{"issue":"bd-1","max_issue_per_run":"5"}}]}`
		_, err := runConfigSet(t, runConfigDispatchSet, body)
		if err == nil {
			t.Fatal("an undeclared cron var key is silently dropped by ResolveVars at every fire; " +
				"it must be rejected at write time")
		}
		if !strings.Contains(err.Error(), "max_issue_per_run") {
			t.Errorf("rejection must name the offending key; got: %v", err)
		}
		// The remedy is only actionable if the operator can see what the formula DOES declare.
		if !strings.Contains(err.Error(), "max_issues_per_run") {
			t.Errorf("rejection must list the formula's declared var names; got: %v", err)
		}

		after, _ := os.ReadFile(config.DispatchConfigPath(root))
		if !bytes.Equal(before, after) {
			t.Error("dispatch.json was modified on a rejected write")
		}
	})

	t.Run("a var key naming a deferred-source formula var is accepted", func(t *testing.T) {
		setupCronFactory(t)
		// A deliberate, documented carve-out. ResolveVars skips deferred vars BEFORE the universal
		// CLI override (internal/formula/vars.go:51-53), so this key is in fact discarded at every
		// fire — the very class direction (b) exists to close. It is accepted anyway because the
		// key IS declared, which is the rule design-doc.md:76 states, and tightening it would be a
		// scope change rather than a bug fix. Pinned here so the carve-out is a decision on the
		// record instead of an accident, and so narrowing it later is a visible test change.
		body := `{"crons":[{"name":"deferred-cron","agent":"af610-deferred","every":"4h","vars":{"later":"x"}}]}`
		if out, err := runConfigSet(t, runConfigDispatchSet, body); err != nil {
			t.Fatalf("a declared deferred-source var key is accepted by direction (b); err=%v out=%q", err, out)
		}
	})

	t.Run("multiple undeclared keys are reported deterministically", func(t *testing.T) {
		setupCronFactory(t)
		body := `{"crons":[{"name":"weekly-pm","agent":"af610-pm","every":"14d","vars":` +
			`{"issue":"bd-1","zeta":"1","alpha":"2","mid":"3","beta":"4"}}]}`

		var first string
		for i := 0; i < 20; i++ {
			_, err := runConfigSet(t, runConfigDispatchSet, body)
			if err == nil {
				t.Fatal("expected a rejection for the undeclared keys")
			}
			if i == 0 {
				first = err.Error()
				continue
			}
			if err.Error() != first {
				t.Fatalf("map iteration order leaked into the message; run %d gave\n  %q\nrun 0 gave\n  %q",
					i, err.Error(), first)
			}
		}
	})
}

// TestConfigSet_CronWarnings pins the two non-fatal advisories the setter emits alongside the
// cron cross-checks. Both describe a document that is valid but will not behave as the operator
// likely intends, so neither may change the exit code — and neither may fire for a cron-free
// document, or ~20 pre-#610 setter tests would start seeing new output on a shared buffer.
func TestConfigSet_CronWarnings(t *testing.T) {
	t.Run("a cadence finer than the dispatcher tick warns", func(t *testing.T) {
		setupCronFactory(t)
		// No interval_seconds in the document: the effective tick must still be the 300s default.
		// validateDispatchConfig does not apply that default until SaveDispatchConfig, i.e. after
		// this check, so a naive read of cfg.IntervalSecs would compare against 0 and never warn.
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"1m"}]}`
		out, err := runConfigSet(t, runConfigDispatchSet, body)
		if err != nil {
			t.Fatalf("a sub-tick cadence is an advisory, not an error; err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "warning:") || !strings.Contains(out, "patrol") {
			t.Errorf("a cadence finer than the tick must warn and name the schedule; out=%q", out)
		}
		if !strings.Contains(out, "300") {
			t.Errorf("the warning must name the effective tick so the operator can act; out=%q", out)
		}
	})

	t.Run("an explicit interval_seconds sets the threshold", func(t *testing.T) {
		setupCronFactory(t)
		// 4h against a 60s tick is far above it — no warning. This is the arm that fails if the
		// comparison is ever inverted.
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h"}],"interval_seconds":60}`
		out, err := runConfigSet(t, runConfigDispatchSet, body)
		if err != nil {
			t.Fatalf("unexpected error; err=%v out=%q", err, out)
		}
		if strings.Contains(out, "fires every") {
			t.Errorf("a cadence coarser than the tick must not warn; out=%q", out)
		}
	})

	t.Run("crons configured while the dispatcher is down warns", func(t *testing.T) {
		setupCronFactory(t) // installs a fake tmux with no sessions present
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h"}]}`
		out, err := runConfigSet(t, runConfigDispatchSet, body)
		if err != nil {
			t.Fatalf("a stopped dispatcher is an advisory, not an error; err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "dispatcher is not running") {
			t.Errorf("schedules written into a factory with no dispatcher must say so; out=%q", out)
		}
	})

	t.Run("a running dispatcher produces no warning", func(t *testing.T) {
		setupCronFactory(t)
		installFakeTmuxPresent(t, dispatchSessionName) // overrides the fixture's empty fake
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h"}]}`
		out, err := runConfigSet(t, runConfigDispatchSet, body)
		if err != nil {
			t.Fatalf("unexpected error; err=%v out=%q", err, out)
		}
		if strings.Contains(out, "dispatcher is not running") {
			t.Errorf("the warning must be suppressed when the dispatcher is up; out=%q", out)
		}
	})

	t.Run("a failed compare-and-set write emits no advisory", func(t *testing.T) {
		// The advisories describe a document that was written. Every path before the write can
		// still abort — a lost CAS race being the one an operator hits in practice — and warning
		// "your crons will not fire" about a document that was never written is worse than silent.
		root := setupCronFactory(t)
		seedGoodDispatch(t, root)
		body := `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"1m"}]}`
		out, err := runConfigSetWithHash(t, runConfigDispatchSet, body, strings.Repeat("0", 64))
		if err == nil {
			t.Fatalf("a stale content hash must reject the write; out=%q", out)
		}
		if strings.Contains(out, "warning:") {
			t.Errorf("a write that did not happen must not be described; out=%q", out)
		}
	})

	t.Run("a cron-free document emits no cron warning at all", func(t *testing.T) {
		setupCronFactory(t)
		// Every pre-#610 caller looks like this. runConfigSet merges stdout and stderr into one
		// buffer, so any ungated advisory would land in assertions across the whole setter suite.
		body := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"manager"}]}`
		out, err := runConfigSet(t, runConfigDispatchSet, body)
		if err != nil {
			t.Fatalf("unexpected error; err=%v out=%q", err, out)
		}
		if strings.Contains(out, "warning:") {
			t.Errorf("a document with no crons must produce byte-identical output to before; out=%q", out)
		}
	})
}
