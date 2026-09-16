package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
)

// This file collects the cross-file half of cron validation (issue #610 Phase 2, N4 + N5).
//
// It lives in internal/cmd rather than beside validateCrons in internal/config because
// internal/formula imports internal/config, so internal/config can never parse a formula to ask
// what vars it requires. internal/config/dispatch.go records that constraint above
// ValidateDispatchConfig and names this layer as the sanctioned home — "the dispatch-start and
// config-write callers of ValidateDispatchConfig already import both packages". The split is
// therefore: internal/config owns everything decidable from dispatch.json alone (name, uniqueness,
// `every` grammar, var-key shape); this file owns everything that needs agents.json, models.json,
// or the target formula on disk.
//
// The signature takes the already-loaded agents and models so the caller's postures are preserved
// unchanged: agents.json is FATAL upstream (a dangling reference must never reach the file), while
// models.json is the tolerant non-selecting read that warns and hands back nil.

// ambientCronVarKey is the one var key a schedule may declare that its target formula does not.
// {{default_branch}} is injected straight out of cliVars after ResolveVars precisely because it is
// NOT a declared formula var (internal/cmd/sling.go:569-581); every other undeclared key is
// silently dropped. A second ambient token would have to be added here as well as there.
const ambientCronVarKey = "default_branch"

// defaultDispatchTickSecs mirrors the interval default validateDispatchConfig applies. It is
// restated rather than read from cfg because that default is filled inside SaveDispatchConfig —
// i.e. AFTER the setter's cross-checks run — so at check time cfg.IntervalSecs is still the raw
// decoded value, which is 0 for any document that omits interval_seconds.
const defaultDispatchTickSecs = 300

// cronCoverableSources are the var sources a scheduled sling can only satisfy from the schedule's
// own vars. cli is obvious; the three bead sources are here because a cron fire has no hooked bead
// and creates no assignment bead (that requires a non-empty task, sling.go:505), so they fail at
// ResolveVars exactly like cli does. "env" is deliberately absent — the dispatcher's environment,
// not the operator's shell, is authoritative, so it is checked at fire time only. "deferred" is
// absent because ResolveVars skips it outright, and "literal"/"" resolve from Default and never
// error.
var cronCoverableSources = map[string]bool{
	"cli":              true,
	"hook_bead":        true,
	"bead_title":       true,
	"bead_description": true,
}

// checkCronRefs validates every cron schedule against the artifacts outside dispatch.json: the
// agent roster, the model registry, and the target formula's declared variables. Any failure
// rejects the whole write — a schedule that cannot fire is a configuration error, not a degraded
// mode, and the operator is looking at the document right now.
//
// It must not assume the struct-level validator has already run. On the setter path it has not:
// ValidateDispatchConfig does not call validateDispatchConfig, and SaveDispatchConfig — the only
// caller that does — runs after this. So a cron here may still carry an empty or duplicated name,
// an unparseable `every`, or a malformed var key. Crons missing the two identity fields are
// skipped so validateCrons keeps ownership of those clearer messages; the write is rejected either
// way, the only question is which message the operator reads first.
//
// Errors are PLAIN and never wrap config.ErrMissingField: startDispatch reads that sentinel as
// "dispatch not configured" and friendly-skips the entire dispatcher, which would turn one bad
// schedule into a silent outage of items and crons alike.
//
// Phase 3's dispatcher cron-pass will want the opposite posture from the same facts — a schedule
// that has gone stale since it was written (its agent uninstalled, its formula deleted) must be
// skipped and logged per-schedule, never allowed to abort the tick that also serves labelled items.
// The per-cron helpers below are split out so that caller can reuse the checks without inheriting
// this function's reject-everything return.
func checkCronRefs(crons []config.CronSchedule, agents *config.AgentConfig, models *config.ModelsConfig, root string) error {
	if len(crons) == 0 {
		return nil
	}
	if agents == nil {
		return fmt.Errorf("agents config is nil")
	}

	for _, cron := range crons {
		if cron.Name == "" || cron.Agent == "" {
			continue // validateCrons owns the identity messages, and still rejects the write
		}

		entry, ok := agents.Agents[cron.Agent]
		if !ok {
			return fmt.Errorf("cron %q references unknown agent %q, absent from agents.json — fix the name, or add the agent with `af install <role>` first",
				cron.Name, cron.Agent)
		}
		if entry.Formula == "" {
			return fmt.Errorf("cron %q: agent %q is not a specialist (no formula field in agents.json) — a schedule can only fire a formula-bearing agent",
				cron.Name, cron.Agent)
		}

		formulaPath, err := formula.FindFormulaFile(entry.Formula, root)
		if err != nil {
			return fmt.Errorf("cron %q: cannot find formula %q for agent %q: %v", cron.Name, entry.Formula, cron.Agent, err)
		}
		f, err := formula.ParseFile(formulaPath)
		if err != nil {
			return fmt.Errorf("cron %q: cannot parse formula %q for agent %q: %v", cron.Name, entry.Formula, cron.Agent, err)
		}

		// Tolerant, mirroring the per-mapping model cross-check: with no registry at all a model
		// name is a raw id handed straight to the CLI, so an empty or absent models.json must skip
		// the check rather than reject every factory that never ran `af config models set`.
		if models != nil && len(models.Models) > 0 && cron.Model != "" {
			if _, ok := models.Models[cron.Model]; !ok {
				return fmt.Errorf("cron %q references undefined model %q", cron.Name, cron.Model)
			}
		}

		// The merged set sling itself resolves against. The workflow gate is copied verbatim from
		// sling.go:536-543: for a convoy, aspect or expansion formula sling resolves f.Vars alone
		// and never looks at f.Inputs, so demanding an input here would reject a schedule the fire
		// path would run without complaint.
		merged := f.Vars
		if f.Type == formula.TypeWorkflow {
			merged, err = formula.MergeInputsToVars(f.Inputs, f.Vars)
			if err != nil {
				return fmt.Errorf("cron %q: formula %q: merging inputs to vars: %v", cron.Name, entry.Formula, err)
			}
		}

		if err := checkCronVarCoverage(cron, merged); err != nil {
			return err
		}
		if err := checkCronVarDeclaration(cron, entry.Formula, merged); err != nil {
			return err
		}
	}
	return nil
}

// checkCronVarCoverage is N5 direction (a): every var the formula requires and cannot supply for
// itself must be present in the schedule's vars. Without this, a schedule whose formula declares a
// required cli var with no default is accepted at write time and then hard-errors at ResolveVars on
// every single fire — for a 14d cadence, a brick that stays broken for months.
func checkCronVarCoverage(cron config.CronSchedule, merged map[string]formula.Var) error {
	var missing []string
	for _, name := range sortedMapKeys(merged) { // sorted: one message, not a coin flip
		v := merged[name]
		if !v.Required || v.Default != "" || !cronCoverableSources[v.Source] {
			continue
		}
		if _, ok := cron.Vars[name]; ok {
			continue
		}
		missing = append(missing, fmt.Sprintf("var %q (source %s, no default)", name, v.Source))
	}
	if len(missing) == 0 {
		return nil
	}
	// The single-var form is byte-identical to the design's model message (data.md:148). The
	// trailing "it" is the agent, not the var, so it stays singular when several vars are listed.
	pronoun := "it"
	if len(missing) > 1 {
		pronoun = "them"
	}
	return fmt.Errorf("cron %q: agent %q formula requires %s — declare %s in vars or it cannot be slung on a schedule",
		cron.Name, cron.Agent, strings.Join(missing, ", "), pronoun)
}

// checkCronVarDeclaration is N5 direction (b): every key the schedule declares must be a var the
// formula actually declares. ResolveVars iterates the formula's DECLARED vars only, so a key the
// formula never declared is silently discarded — an operator typo like max_issue_per_run for
// max_issues_per_run is accepted, does nothing at every fire, and says nothing about it, which is
// the longest-lived failure mode in the feature.
//
// security.md §A3 concluded "warn, don't reject" for this class; design-doc.md's N5 row and
// cross-review HIGH-2 both supersede it with REJECT, listing the formula's declared names so the
// remedy is visible in the message.
func checkCronVarDeclaration(cron config.CronSchedule, formulaName string, merged map[string]formula.Var) error {
	var undeclared []string
	for _, key := range sortedMapKeys(cron.Vars) { // sorted: the same offender is named every run
		if key == ambientCronVarKey {
			continue
		}
		if _, ok := merged[key]; !ok {
			undeclared = append(undeclared, fmt.Sprintf("%q", key))
		}
	}
	if len(undeclared) == 0 {
		return nil
	}
	noun, verb := "var key", "is"
	if len(undeclared) > 1 {
		noun, verb = "var keys", "are"
	}
	declared := "(none)"
	if names := sortedMapKeys(merged); len(names) > 0 {
		declared = strings.Join(names, ", ")
	}
	return fmt.Errorf("cron %q: %s %s %s not declared by formula %q — an undeclared key is silently dropped at every fire; declared vars are: %s",
		cron.Name, noun, strings.Join(undeclared, ", "), verb, formulaName, declared)
}

// warnSubTickCrons reports schedules whose cadence is finer than the dispatcher's tick. It is
// advisory, not an error: the schedule still fires, just no more often than one tick, and an
// operator may well have meant "as often as possible".
//
// The effective tick is computed here because cfg.IntervalSecs is not defaulted yet at cross-check
// time — see defaultDispatchTickSecs. An unparseable `every` is skipped silently rather than
// reported: validateCrons owns that message and rejects the write moments later.
func warnSubTickCrons(w io.Writer, cfg *config.DispatchConfig) {
	interval := cfg.IntervalSecs
	if interval <= 0 {
		interval = defaultDispatchTickSecs
	}
	tick := time.Duration(interval) * time.Second
	for _, cron := range cfg.Crons {
		d, err := config.ParseCompactDuration(cron.Every)
		if err != nil || d >= tick {
			continue
		}
		fmt.Fprintf(w, "warning: cron %q fires every %s but the dispatcher ticks every %ds, so it can fire no more often than once per tick — raise the cadence, or lower interval_seconds\n",
			cron.Name, cron.Every, interval)
	}
}

// cronsWontFireWarning is the single spelling of the carrier-down advisory, shared by the setter's
// write-time check below and by `af dispatch status`' schedules block (issue #610 N11). Two
// spellings of one fact would drift and teach operators two different remedies for one problem —
// the discipline haltedStallReason already applies to the halted stall.
const cronsWontFireWarning = "warning: crons are configured but the dispatcher is not running, so no schedule will fire — start it with `af up` (or `af dispatch start`)"

// warnDispatcherNotRunning reports that schedules have been configured into a factory whose
// dispatcher is down. Nothing is wrong with the document — the crons simply will not fire until
// someone starts the daemon, and saying so at the moment of writing is cheaper than the operator
// discovering it a cadence later.
func warnDispatcherNotRunning(w io.Writer) {
	// Ignoring the error matches every other liveness probe in the cmd layer: an unreadable tmux
	// state is treated as not-running, and the consequence here is one extra advisory line.
	if running, _ := newCmdTmux().HasSession(dispatchSessionName); running {
		return
	}
	fmt.Fprintln(w, cronsWontFireWarning)
}
