package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/issuestore"
)

// bareFormulaTOML is satisfiable with no task and no vars — the shape a recurring schedule fires.
// It deliberately declares no `task` var: the specialist-dispatch path injects a synthetic
// task=<text> CLI var, and a fixture that declared one would encode that accident rather than test
// the bare contract.
const bareFormulaTOML = `formula = "af610-bare-formula"
type = "workflow"
version = 1

[vars.cadence]
description = "Optional cadence hint"
required = false
default = "routine"
source = "cli"

[[steps]]
id = "s1"
title = "wake and work"
description = "cadence {{cadence}}"
`

// TestSlingBareFlag discharges N7 (design-doc.md:78, verification-report.md row 1).
//
// The design's fire argv for a due schedule is `sling --agent <a> --reset [...]` with NO positional
// task — a scheduled run has no triggering item to describe. That argv is rejected outright by
// validateSlingArgs today, and runSling would then index args[0] on an empty slice. --bare is the
// explicit escape hatch: it waives the task-required rule for --agent slings only, creates no
// assignment bead, and leaves every taskless invocation that does NOT pass it rejected exactly as
// before.
func TestSlingBareFlag(t *testing.T) {
	t.Run("the flag is registered on af sling", func(t *testing.T) {
		f := slingCmd.Flags().Lookup("bare")
		if f == nil {
			t.Fatal("af sling must expose --bare: it is operator-facing surface, not an undocumented dispatcher back door")
		}
		if f.DefValue != "false" {
			t.Errorf("--bare must default to false so no existing invocation changes meaning; got %q", f.DefValue)
		}
		if f.Hidden {
			t.Error("--bare must not be hidden; --caller is the only machine-only sling flag")
		}
		if !strings.Contains(f.Usage, "task") {
			t.Errorf("--bare usage must say what it waives; got %q", f.Usage)
		}
	})

	t.Run("a taskless --agent sling is accepted when bare is set", func(t *testing.T) {
		if err := validateSlingArgs("", "financial-patrol", nil, true); err != nil {
			t.Errorf("a bare sling carries no positional task by construction; got %v", err)
		}
		if err := validateSlingArgs("", "financial-patrol", []string{}, true); err != nil {
			t.Errorf("an empty args slice is the same case; got %v", err)
		}
	})

	t.Run("a taskless --agent sling without bare still rejects", func(t *testing.T) {
		// The regression that matters: sling_web_argv_contract_test.go pins this substring as the
		// cross-module argv contract, and the web console can never send --bare.
		for _, args := range [][]string{nil, {""}, {"   "}} {
			err := validateSlingArgs("", "rootcause-all", args, false)
			if err == nil {
				t.Fatalf("args=%q without --bare must still be rejected", args)
			}
			if !strings.Contains(err.Error(), "task description required") {
				t.Errorf("the existing rejection message must stay byte-identical; got %q", err.Error())
			}
		}
	})

	t.Run("bare does not weaken the formula-or-agent rule", func(t *testing.T) {
		err := validateSlingArgs("", "", nil, true)
		if err == nil {
			t.Fatal("--bare waives the task requirement only; a sling naming neither a formula nor an agent has no target")
		}
		if !strings.Contains(err.Error(), "--formula is required") {
			t.Errorf("got %q, want the unchanged --formula rejection", err.Error())
		}
	})

	t.Run("a bare sling runs end to end and creates no assignment bead", func(t *testing.T) {
		root, _ := createTestFormulaFactoryWithTOML(t, "af610-bare-formula", "af610-bare-agent", bareFormulaTOML)
		setupHermeticSessions(t)
		store := installMemStore(t)
		installNoopLaunchSession(t)
		writeAgentsJSON(t, root, `{"agents":{"af610-bare-agent":{"type":"autonomous","description":"b","formula":"af610-bare-formula"}}}`)
		t.Chdir(root)

		origAgent, origFormula, origNoLaunch, origBare, origVars :=
			slingAgent, slingFormulaName, slingNoLaunch, slingBare, slingVars
		slingAgent, slingFormulaName, slingNoLaunch, slingBare, slingVars =
			"af610-bare-agent", "", true, true, nil
		t.Cleanup(func() {
			slingAgent, slingFormulaName, slingNoLaunch, slingBare, slingVars =
				origAgent, origFormula, origNoLaunch, origBare, origVars
		})

		cmd := &cobra.Command{}
		cmd.SetContext(t.Context())
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)

		// nil args is the exact shape the dispatcher's fire argv produces. Before --bare this
		// panics on args[0].
		if err := runSling(cmd, nil); err != nil {
			t.Fatalf("a bare sling must succeed with no positional task: %v\nstdout:\n%s\nstderr:\n%s",
				err, out.String(), errBuf.String())
		}

		issues, err := store.List(t.Context(), issuestore.Filter{})
		if err != nil {
			t.Fatalf("store.List: %v", err)
		}
		if len(issues) == 0 {
			t.Fatal("the bare sling created no beads at all; it did not instantiate the formula")
		}
		for _, iss := range issues {
			for _, lbl := range iss.Labels {
				if lbl == "assignment" {
					t.Errorf("a bare sling has no task to assign, so it must create no assignment bead; got %+v", iss)
				}
			}
		}
		if strings.Contains(out.String(), "Created assignment bead") {
			t.Errorf("a bare sling must not announce an assignment bead; stdout:\n%s", out.String())
		}
	})

	t.Run("a bare sling does not clobber an operator-supplied task var", func(t *testing.T) {
		// The specialist path appends a synthetic task=<text> LAST, and parseCLIVars is last-wins,
		// so an unconditional append would silently blank a schedule's own --var task=... .
		// design-doc.md:78 requires "no auto-bead, no task var" for exactly this reason.
		root, _ := createTestFormulaFactoryWithTOML(t, "af610-bare-formula", "af610-bare-agent", bareFormulaTOML)
		setupHermeticSessions(t)
		installMemStore(t)
		installNoopLaunchSession(t)
		writeAgentsJSON(t, root, `{"agents":{"af610-bare-agent":{"type":"autonomous","description":"b","formula":"af610-bare-formula"}}}`)
		t.Chdir(root)

		origAgent, origFormula, origNoLaunch, origBare, origVars :=
			slingAgent, slingFormulaName, slingNoLaunch, slingBare, slingVars
		slingAgent, slingFormulaName, slingNoLaunch, slingBare, slingVars =
			"af610-bare-agent", "", true, true, []string{"task=scheduled-work"}
		t.Cleanup(func() {
			slingAgent, slingFormulaName, slingNoLaunch, slingBare, slingVars =
				origAgent, origFormula, origNoLaunch, origBare, origVars
		})

		cmd := &cobra.Command{}
		cmd.SetContext(t.Context())
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)

		if err := runSling(cmd, nil); err != nil {
			t.Fatalf("a bare sling carrying its own task var must succeed: %v\n%s", err, out.String())
		}

		vars, err := parseCLIVars(buildSpecialistCLIVars(slingVars, ""))
		if err != nil {
			t.Fatalf("parseCLIVars: %v", err)
		}
		if got := vars["task"]; got != "scheduled-work" {
			t.Errorf("the schedule's own task var must survive; got %q, want %q", got, "scheduled-work")
		}
	})

	t.Run("a non-bare specialist dispatch still carries its task var", func(t *testing.T) {
		// The other polarity: suppressing the synthetic task= on the bare path must not suppress it
		// on the human path, where the formula's {{task}} expansion depends on it.
		vars, err := parseCLIVars(buildSpecialistCLIVars(nil, "implement issue #42"))
		if err != nil {
			t.Fatalf("parseCLIVars: %v", err)
		}
		if got := vars["task"]; got != "implement issue #42" {
			t.Errorf("a task-bearing dispatch must still inject task=<text>; got %q", got)
		}
	})
}
