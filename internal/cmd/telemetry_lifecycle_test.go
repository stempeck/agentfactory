package cmd

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// twoStepTOML is a formula with two steps and no dependency between them, so that closing the
// first leaves the second open and `af done` never reaches the completion path. That matters
// for the off-path test: the WORK_DONE mail seam is legitimately called on a FINAL af done, so
// a panic-on-call stub is only honest while a step remains.
const twoStepTOML = `
formula = "offpath"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
description = "First"
[[steps]]
id = "step2"
title = "Step 2"
description = "Second"
`

// lifecycleFixture is one factory root wired for all three instrumented verbs: a git repo with
// factory.json, agents.json, a two-step formula, an issue store, and the agent's work dir.
type lifecycleFixture struct {
	root    string
	agent   string
	workDir string
	mem     *memstore.Store
}

// newLifecycleFixture chdirs into the agent's work dir and resolves the factory root exactly
// the way the verbs will, so that assertions and production code name the same paths even when
// the temporary directory sits behind a symlink.
func newLifecycleFixture(t *testing.T) lifecycleFixture {
	t.Helper()
	t.Setenv("AF_ACTOR", "manager")
	root, agentDir := createTestFormulaFactoryWithTOML(t, "offpath", "manager", twoStepTOML)
	if err := os.MkdirAll(config.StoreDir(root), 0o755); err != nil {
		t.Fatalf("mkdir store dir: %v", err)
	}

	t.Chdir(agentDir)
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	resolvedRoot, err := resolveInvokerRoot(workDir)
	if err != nil {
		t.Fatalf("resolveInvokerRoot: %v", err)
	}

	return lifecycleFixture{root: resolvedRoot, agent: "manager", workDir: workDir, mem: installMemStore(t)}
}

// runLifecycleVerbs drives af sling, af prime and af done once each against the fixture, in the
// order a real run performs them. It returns everything written to stderr, because the
// warn-once discipline this phase adds is a stderr behaviour.
//
// The two cobra verbs are driven through their RunE entry points rather than through the inner
// functions the emission sites live in. That is deliberate: the per-invocation clock and the
// gate reading are established at those entry points, so an implementation that recorded but
// never wired them would pass a test that called the inner functions directly.
func runLifecycleVerbs(t *testing.T, fx lifecycleFixture) string {
	t.Helper()

	setSlingFlagsForTest(t, "offpath", fx.agent)

	return captureStderr(t, func() {
		var out bytes.Buffer
		newCmd := func() *cobra.Command {
			c := &cobra.Command{}
			c.SetContext(t.Context())
			c.SetOut(&out)
			c.SetErr(&out)
			return c
		}

		if err := runSling(newCmd(), nil); err != nil {
			t.Fatalf("af sling: %v", err)
		}
		if readHookedFormulaID(fx.workDir) == "" {
			t.Fatal("af sling did not persist .runtime/hooked_formula; the fixture is not driving a real run")
		}

		if err := runPrime(newCmd(), nil); err != nil {
			t.Fatalf("af prime: %v", err)
		}
		if _, err := os.Stat(filepath.Join(fx.workDir, ".runtime", "step_primed")); err != nil {
			t.Fatalf("af prime did not write .runtime/step_primed: %v", err)
		}

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
	})
}

// setSlingFlagsForTest sets the sling package globals for one test and restores them, the
// save/restore idiom sling_reset_gate_test.go uses.
func setSlingFlagsForTest(t *testing.T, formulaName, agent string) {
	t.Helper()
	origName, origAgent, origNoLaunch, origReset, origModel :=
		slingFormulaName, slingAgent, slingNoLaunch, slingReset, slingModel
	slingFormulaName, slingAgent, slingNoLaunch, slingReset, slingModel = formulaName, agent, true, false, ""
	t.Cleanup(func() {
		slingFormulaName, slingAgent, slingNoLaunch, slingReset, slingModel =
			origName, origAgent, origNoLaunch, origReset, origModel
	})
}

// installPanicSeams replaces every ADR-009 seam that reaches a subprocess, a mailbox or the
// network with a stub that fails the test if it is ever called.
//
// The four done.go mail seams each begin with an isTestBinary() early return, which replacing
// the whole var bypasses — so these stubs genuinely observe every call, which is why the caller
// must drive a NON-FINAL af done.
func installPanicSeams(t *testing.T) {
	t.Helper()
	origExport := telemetry.Export
	origLaunch := launchAgentSession
	origWorkDone := sendWorkDoneMail
	origEscalation := sendEscalationMail
	origImprovement := sendImprovementMail
	origNudge := deliverImprovementNudge

	telemetry.Export = func(config.TelemetryConfig, []byte) error {
		t.Errorf("telemetry.Export was called with the gate off")
		return nil
	}
	launchAgentSession = func(*cobra.Command, string, string, string, string, string, bool) error {
		t.Errorf("launchAgentSession was called")
		return nil
	}
	sendWorkDoneMail = func(string, string, string, int) error {
		t.Errorf("sendWorkDoneMail was called")
		return nil
	}
	sendEscalationMail = func(string, string, string, string) error {
		t.Errorf("sendEscalationMail was called")
		return nil
	}
	sendImprovementMail = func(string, string, string) error {
		t.Errorf("sendImprovementMail was called")
		return nil
	}
	deliverImprovementNudge = func(string, string) error {
		t.Errorf("deliverImprovementNudge was called")
		return nil
	}

	t.Cleanup(func() {
		telemetry.Export = origExport
		launchAgentSession = origLaunch
		sendWorkDoneMail = origWorkDone
		sendEscalationMail = origEscalation
		sendImprovementMail = origImprovement
		deliverImprovementNudge = origNudge
	})
}

// TestTelemetryOffPathBudget pins the stated off-path contract for the three instrumented verbs:
// when the gate is off, each performs at most root resolution plus one gate-file read, and
// nothing else — no telemetry write, no config read, no network, no subprocess.
//
// The clause "at most one gate-file read" is not counted at runtime: this repository has no
// syscall counter and building one would be new infrastructure for a single assertion. It is
// instead enforced statically, by parsing each verb's file and counting the calls to the gate
// reader — a check that fails on the drift it exists to catch (a second reader appearing) while
// costing nothing to run. Everything else below is a runtime observation.
//
// Deliberately scoped to sling/prime/done. The genuinely hot verb is `af step current --json`,
// fired by the fidelity hook on essentially every turn, and this phase does not instrument it —
// claiming a budget there would imply a cost that was never measured where it matters.
func TestTelemetryOffPathBudget(t *testing.T) {
	t.Run("gate off: the three verbs do no telemetry work", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		installNoopLaunchSession(t)
		installPanicSeams(t)

		if _, err := os.Stat(telemetryGateFile(fx.root)); !os.IsNotExist(err) {
			t.Fatalf("fixture must start with no gate file; stat err = %v", err)
		}

		// Poison both surfaces an emitting implementation would have to touch. The config is
		// unparseable, and a plain file occupies the directory path AppendEvent would create,
		// so any emission attempt both fails and warns.
		writeTelemetryJSON(t, fx.root, "{ not json")
		telemetryDir := config.TelemetryDir(fx.root)
		if err := os.WriteFile(telemetryDir, []byte("x"), 0o644); err != nil {
			t.Fatalf("poison telemetry dir path: %v", err)
		}

		stderr := runLifecycleVerbs(t, fx)

		if got, err := os.ReadFile(telemetryDir); err != nil || string(got) != "x" {
			t.Errorf("the telemetry directory path changed: contents = %q, err = %v; "+
				"a verb tried to record with the gate off", string(got), err)
		}
		// Any error is acceptable here: the poison above occupies the directory path with a
		// plain file, so a records directory could only exist if something removed it.
		if _, err := os.Stat(filepath.Join(telemetryDir, "steps")); err == nil {
			t.Error("a records directory was created with the gate off")
		}
		if _, err := os.Stat(telemetryGateFile(fx.root)); !os.IsNotExist(err) {
			t.Errorf("a verb created the gate file; stat err = %v", err)
		}
		if strings.Contains(strings.ToLower(stderr), "telemetry") {
			t.Errorf("stderr mentions telemetry with the gate off:\n%s", stderr)
		}

		// Factory-root placement is the real protection for the records, so nothing
		// telemetry-shaped may appear under .runtime/ where a reset would take it.
		strays, _ := filepath.Glob(filepath.Join(fx.workDir, ".runtime", "*telemetry*"))
		if len(strays) > 0 {
			t.Errorf("telemetry files written under .runtime/: %v", strays)
		}
	})

	t.Run("each instrumented verb reads the gate at most once", func(t *testing.T) {
		for _, file := range []string{"sling.go", "prime.go", "done.go"} {
			got := countCallsTo(t, filepath.Join(".", file), "telemetryFactoryEnabled")
			if got > 1 {
				t.Errorf("%s calls telemetryFactoryEnabled %d times, want at most 1: "+
					"the off-path budget is one gate-file read per verb, and a second reader is "+
					"also a second place for the exact-match rule to drift", file, got)
			}
		}
	})

	// Without this arm the whole test passes against an implementation that emits nothing at
	// all — which is exactly the state of the tree before this phase.
	t.Run("non-vacuity control: with the gate on, records do appear", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		installNoopLaunchSession(t)
		if err := os.WriteFile(telemetryGateFile(fx.root), []byte("on\n"), 0o644); err != nil {
			t.Fatalf("write gate file: %v", err)
		}

		runLifecycleVerbs(t, fx)

		records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root),
			telemetry.Filter{Agent: fx.agent})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		kinds := map[string]int{}
		for _, r := range records {
			kinds[r.Event]++
			if r.Verb == "" {
				t.Errorf("record %+v carries no verb", r)
			}
			if _, perr := time.Parse(telemetry.TimestampLayout, r.TS); perr != nil {
				t.Errorf("record ts %q does not parse with the record layout: %v", r.TS, perr)
			}
		}
		for _, want := range []string{
			telemetry.EventInstanceStart, telemetry.EventStepStart, telemetry.EventStepEnd,
		} {
			if kinds[want] != 1 {
				t.Errorf("recorded %d %s events, want exactly 1 (all kinds: %v)", kinds[want], want, kinds)
			}
		}

		// af sling records BEFORE it persists .runtime/hooked_formula, so the instance id has
		// to come from the in-process value. Without this the site could pass nothing and every
		// other assertion would still hold.
		instance := readHookedFormulaID(fx.workDir)
		if instance == "" {
			t.Fatal("no hooked_formula after the run; the fixture is not driving a real sling")
		}
		for _, r := range records {
			if r.InstanceID != instance {
				t.Errorf("%s record carries instance_id %q, want the run's instance %q",
					r.Event, r.InstanceID, instance)
			}
		}
	})
}

// countCallsTo parses one Go source file and counts calls to the named function.
func countCallsTo(t *testing.T, path, name string) int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	count := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == name {
			count++
		}
		return true
	})
	return count
}

// TestPrimeNoNetworkIO pins the rule that af prime records but never exports.
//
// af prime runs from Claude Code's SessionStart hook, on the path that must not block, and an
// export there would put a network round trip in front of every session start. Records are
// appended and the backlog is left for af done, which is the only verb that drains.
func TestPrimeNoNetworkIO(t *testing.T) {
	t.Run("prime records a step_start and exports nothing", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		if err := os.WriteFile(telemetryGateFile(fx.root), []byte("on\n"), 0o644); err != nil {
			t.Fatalf("write gate file: %v", err)
		}
		// A fully valid, reachable-looking endpoint, so an implementation that wanted to
		// export could.
		writeTelemetryJSON(t, fx.root, `{"endpoint":"http://127.0.0.1:5080","export_timeout_ms":500}`)

		orig := telemetry.Export
		telemetry.Export = func(config.TelemetryConfig, []byte) error {
			t.Errorf("af prime performed network I/O: telemetry.Export was called")
			return nil
		}
		t.Cleanup(func() { telemetry.Export = orig })

		epic, step := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)

		if err := runPrimeInFixture(t); err != nil {
			t.Fatalf("af prime: %v", err)
		}

		records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root),
			telemetry.Filter{Agent: fx.agent})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		var starts int
		for _, r := range records {
			if r.Event == telemetry.EventStepStart {
				starts++
				if r.StepID != step.ID {
					t.Errorf("step_start step_id = %q, want %q", r.StepID, step.ID)
				}
				if r.Verb != "prime" {
					t.Errorf("step_start verb = %q, want %q", r.Verb, "prime")
				}
			}
		}
		if starts != 1 {
			t.Fatalf("recorded %d step_start events, want exactly 1 (records: %+v)", starts, records)
		}

		// The sharpest single assertion: prime recorded, and moved no cursor. A drain would
		// have written one.
		if _, err := os.Stat(filepath.Join(config.TelemetryDir(fx.root), ".cursor-"+fx.agent)); !os.IsNotExist(err) {
			t.Errorf("af prime advanced the export cursor; stat err = %v", err)
		}
		stats, err := telemetry.Stats(config.TelemetryDir(fx.root), fx.agent)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if stats.Backlog < 1 {
			t.Errorf("backlog = %d, want the records left for af done to drain", stats.Backlog)
		}
	})

	t.Run("re-priming the same step records nothing further", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		if err := os.WriteFile(telemetryGateFile(fx.root), []byte("on\n"), 0o644); err != nil {
			t.Fatalf("write gate file: %v", err)
		}
		epic, _ := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)

		for i := 0; i < 3; i++ {
			if err := runPrimeInFixture(t); err != nil {
				t.Fatalf("af prime %d: %v", i, err)
			}
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventStepStart); got != 1 {
			t.Errorf("three primes of the same step recorded %d step_start events, want 1", got)
		}
	})

	t.Run("prime.go reaches no network package", func(t *testing.T) {
		// A package-wide check is impossible: config_models.go already imports net/http. The
		// scan is scoped to the file whose zero-network-I/O contract is being pinned.
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(".", "prime.go"), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing prime.go: %v", err)
		}
		for _, imp := range file.Imports {
			path, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				continue
			}
			if path == "net" || strings.HasPrefix(path, "net/") || path == "crypto/tls" {
				t.Errorf("prime.go imports %q; af prime must perform zero network I/O", path)
			}
		}
	})
}

// runPrimeInFixture drives af prime through its RunE entry point, where the per-invocation
// clock and the single gate read are established.
func runPrimeInFixture(t *testing.T) error {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return runPrime(cmd, nil)
}

// seedFormulaBeads creates a formula-instance epic with one open step in the fixture's store.
func seedFormulaBeads(t *testing.T, fx lifecycleFixture) (issuestore.Issue, issuestore.Issue) {
	t.Helper()
	ctx := t.Context()
	epic, err := fx.mem.Create(ctx, issuestore.CreateParams{
		Title: "Formula: offpath", Type: issuestore.TypeEpic,
		Labels: []string{"formula-instance"}, Assignee: fx.agent,
	})
	if err != nil {
		t.Fatalf("seed epic: %v", err)
	}
	step, err := fx.mem.Create(ctx, issuestore.CreateParams{
		Title: "Step 1", Parent: epic.ID, Type: issuestore.TypeTask,
		Labels: []string{"formula-step"}, Assignee: fx.agent, Description: "First",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}
	return epic, step
}

func countEvents(t *testing.T, root, agent, kind string) int {
	t.Helper()
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	n := 0
	for _, r := range records {
		if r.Event == kind {
			n++
		}
	}
	return n
}

// TestTelemetryRecordsSurviveResetBothPaths pins the placement decision that makes the records
// worth keeping: they live at the factory root, not under .runtime/, so the three things that
// wipe an agent's runtime state cannot take them.
//
// af sling --reset is two different code paths — resetAgentState for --agent alone, and the
// inline branch for --formula plus --agent — and formula completion is a third. A prior attempt
// at this feature stored its step-start anchor in .runtime/ and added its removal to the
// completion cleanup, destroying the record at the exact moment it became answerable.
func TestTelemetryRecordsSurviveResetBothPaths(t *testing.T) {
	seed := func(t *testing.T, root, agent string) []byte {
		t.Helper()
		ev := telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-07-22T18:31:10.500Z", Agent: agent, InstanceID: "i1", StepID: "s1",
			Verb: "done", VerbMS: 12, DurationMS: 1000, Status: telemetry.StatusClosed,
		}
		if err := telemetry.AppendEvent(config.TelemetryDir(root), ev); err != nil {
			t.Fatalf("seed telemetry record: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(config.TelemetryDir(root), "steps", agent+".jsonl"))
		if err != nil {
			t.Fatalf("read seeded records: %v", err)
		}
		return raw
	}

	assertIntact := func(t *testing.T, root, agent string, want []byte) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(config.TelemetryDir(root), "steps", agent+".jsonl"))
		if err != nil {
			t.Fatalf("records file gone after the reset: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("records changed:\n got %s\nwant %s", got, want)
		}
		records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
		if err != nil {
			t.Fatalf("ReadEvents after reset: %v", err)
		}
		if len(records) != 1 || records[0].StepID != "s1" {
			t.Errorf("records after reset = %+v, want the one seeded s1 record", records)
		}
	}

	assertRuntimeWiped := func(t *testing.T, workDir string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(workDir, ".runtime", "hooked_formula")); !os.IsNotExist(err) {
			t.Errorf("the reset did not actually wipe .runtime/hooked_formula (stat err = %v); "+
				"the survival assertion above would pass vacuously", err)
		}
	}

	t.Run("path A: resetAgentState (--agent alone)", func(t *testing.T) {
		dir := t.TempDir()
		root, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		installMemStore(t)
		workDir := config.AgentDir(root, "solver")
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatalf("mkdir agent dir: %v", err)
		}
		writeRuntimeFile(t, workDir, "hooked_formula", "af-inst-01")
		want := seed(t, root, "solver")

		var buf bytes.Buffer
		if err := resetAgentState(t.Context(), &buf, root, "solver", "test-reset"); err != nil {
			t.Fatalf("resetAgentState: %v", err)
		}

		assertRuntimeWiped(t, workDir)
		assertIntact(t, root, "solver", want)
	})

	t.Run("path B: the inline --formula + --agent branch", func(t *testing.T) {
		root, agentDir := createTestFormulaFactoryWithTOML(t, "survive", "solver", strings.Replace(twoStepTOML, `"offpath"`, `"survive"`, 1))
		if err := os.MkdirAll(config.StoreDir(root), 0o755); err != nil {
			t.Fatalf("mkdir store dir: %v", err)
		}
		installMemStore(t)
		installNoopLaunchSession(t)
		writeRuntimeFile(t, agentDir, "hooked_formula", "af-inst-01")
		want := seed(t, root, "solver")

		origReset := slingReset
		slingReset = true
		t.Cleanup(func() { slingReset = origReset })
		setSlingFlagsForTest(t, "survive", "solver")
		slingReset = true

		cmd := &cobra.Command{}
		cmd.SetContext(t.Context())
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		// The reset block runs unconditionally before instantiation, so an error afterwards
		// still exercises the path under test.
		_ = runFormulaInstantiation(cmd, root, agentDir, nil)

		// This path cannot use assertRuntimeWiped: the same verb re-instantiates immediately
		// after the wipe and legitimately writes a fresh hooked_formula. What proves the reset
		// really ran is that the marker no longer names the instance that was seeded before it.
		if got := readHookedFormulaID(agentDir); got == "af-inst-01" {
			t.Error("the reset did not wipe .runtime/hooked_formula; the survival assertion below " +
				"would pass vacuously")
		}
		assertIntact(t, root, "solver", want)
	})

	t.Run("path C: formula-completion cleanup", func(t *testing.T) {
		root := t.TempDir()
		workDir := config.AgentDir(root, "solver")
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatalf("mkdir agent dir: %v", err)
		}
		writeRuntimeFile(t, workDir, "hooked_formula", "af-inst-01")
		want := seed(t, root, "solver")

		cleanupRuntimeArtifacts(workDir)

		assertRuntimeWiped(t, workDir)
		assertIntact(t, root, "solver", want)
	})
}

// TestReportRendersOpenStep pins the local report surface — the answer to "which step is slow"
// that an operator can get in one command with no backend provisioned at all.
//
// The open row is the point. A step_start with no matching step_end yields NO window from
// DeriveWindows, deliberately: inventing an end would put a fabricated duration into a backend.
// The report is therefore the only surface that can show a stuck or in-flight step, and it has
// to pair the records itself rather than reuse the window join.
func TestReportRendersOpenStep(t *testing.T) {
	setup := func(t *testing.T) string {
		t.Helper()
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
			t.Fatalf("write gate file: %v", err)
		}
		resetReportFlags(t)
		return root
	}

	appendRecord := func(t *testing.T, root string, ev telemetry.StepEvent) {
		t.Helper()
		if err := telemetry.AppendEvent(config.TelemetryDir(root), ev); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	t.Run("an open step renders with elapsed-so-far", func(t *testing.T) {
		root := setup(t)
		openedAt := time.Now().UTC().Add(-90 * time.Second).Format(telemetry.TimestampLayout)

		appendRecord(t, root, telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: "2026-07-22T18:31:04.112Z", Agent: "manager", Formula: "offpath",
			InstanceID: "i1", StepID: "s-closed", StepSeq: 1, StepTitle: "Phase 1",
			Model: "fable-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "prime", VerbMS: 12,
		})
		appendRecord(t, root, telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-07-22T18:31:10.500Z", Agent: "manager", Formula: "offpath",
			InstanceID: "i1", StepID: "s-closed", StepSeq: 1, StepTitle: "Phase 1",
			Model: "fable-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "done", VerbMS: 77,
			DurationMS: 6388, Status: telemetry.StatusClosed,
		})
		appendRecord(t, root, telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: openedAt, Agent: "manager", Formula: "offpath",
			InstanceID: "i1", StepID: "s-open", StepSeq: 2, StepTitle: "Phase 2",
			Model: "fable-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "prime", VerbMS: 9,
		})

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"report"}); err != nil {
				t.Fatalf("af telemetry report: %v", err)
			}
		})

		openLine := lineContaining(t, out, "Phase 2")
		if !strings.Contains(openLine, "open") {
			t.Errorf("the open step's row does not say open:\n%s", openLine)
		}
		if !strings.Contains(openLine, "1m") && !strings.Contains(openLine, "90s") {
			t.Errorf("the open step's row carries no elapsed-so-far (seeded 90s ago):\n%s", openLine)
		}

		closedLine := lineContaining(t, out, "Phase 1")
		if strings.Contains(closedLine, "open") {
			t.Errorf("a closed step was rendered as open:\n%s", closedLine)
		}
		if !strings.Contains(closedLine, "6.388s") && !strings.Contains(closedLine, "6388") {
			t.Errorf("the closed step's row carries no recorded duration:\n%s", closedLine)
		}

		lower := strings.ToLower(out)
		for _, col := range []string{"agent", "step", "status", "duration", "started", "model", "verb_ms"} {
			if !strings.Contains(lower, col) {
				t.Errorf("report header is missing the %q column:\n%s", col, out)
			}
		}
		if !strings.Contains(lower, "backend") {
			t.Errorf("report does not state that token data lives in the backend:\n%s", out)
		}
	})

	t.Run("output is deterministic and agents are enumerated in sorted order", func(t *testing.T) {
		root := setup(t)
		// Seeded for BOTH fixture agents, and for "supervisor" first, so that a renderer which
		// dropped the sort and ranged the agents map directly would produce a different order
		// on some run. With only one agent holding records the sort is unobservable.
		for _, agent := range []string{"supervisor", "manager"} {
			for _, id := range []string{"s1", "s2"} {
				appendRecord(t, root, telemetry.StepEvent{
					V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
					TS: "2026-07-22T18:31:04.112Z", Agent: agent, InstanceID: "i1", StepID: id,
					StepTitle: agent + "-" + id, Verb: "prime",
				})
				appendRecord(t, root, telemetry.StepEvent{
					V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
					TS: "2026-07-22T18:31:10.500Z", Agent: agent, InstanceID: "i1", StepID: id,
					StepTitle: agent + "-" + id, Verb: "done", DurationMS: 6388, Status: telemetry.StatusClosed,
				})
			}
		}
		render := func() string {
			return captureStdout(t, func() {
				if err := runTelemetry(telemetryCmd, []string{"report"}); err != nil {
					t.Fatalf("af telemetry report: %v", err)
				}
			})
		}
		first, second := render(), render()
		if first != second {
			t.Errorf("report output is not deterministic:\n--- first ---\n%s\n--- second ---\n%s", first, second)
		}
		// Read the agent column off every rendered row rather than comparing substring
		// positions: with a map-ordered renderer the latter only fails on some runs.
		var seen []string
		for _, line := range strings.Split(first, "\n") {
			for _, agent := range []string{"manager", "supervisor"} {
				if strings.HasPrefix(line, agent) {
					if len(seen) == 0 || seen[len(seen)-1] != agent {
						seen = append(seen, agent)
					}
				}
			}
		}
		want := []string{"manager", "supervisor"}
		if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
			t.Errorf("agents rendered in order %v, want %v (sorted):\n%s", seen, want, first)
		}
	})

	t.Run("a log that cannot be read says so instead of reporting no records", func(t *testing.T) {
		root := setup(t)
		stepsDir := filepath.Join(config.TelemetryDir(root), "steps")
		if err := os.MkdirAll(stepsDir, 0o755); err != nil {
			t.Fatalf("mkdir steps: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stepsDir, "manager.jsonl"), []byte("{ not json\n"), 0o644); err != nil {
			t.Fatalf("seed corrupt log: %v", err)
		}

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"report"}); err != nil {
				t.Fatalf("af telemetry report: %v", err)
			}
		})
		// "No step records yet" is a false statement about a factory whose records exist and
		// cannot be parsed, which is precisely when an operator needs to be told.
		if !strings.Contains(out, "unreadable lines skipped") {
			t.Errorf("a log of only unparseable lines reported no loss:\n%s", out)
		}
	})

	t.Run("a factory with no records reports cleanly", func(t *testing.T) {
		setup(t)
		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"report"}); err != nil {
				t.Fatalf("af telemetry report with no records: %v", err)
			}
		})
		if strings.Contains(strings.ToLower(out), "warning") {
			t.Errorf("an empty report warned:\n%s", out)
		}
	})

	t.Run("report does not export unless asked", func(t *testing.T) {
		root := setup(t)
		writeTelemetryJSON(t, root, `{"endpoint":"http://127.0.0.1:5080","export_timeout_ms":500}`)
		appendRecord(t, root, telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-07-22T18:31:10.500Z", Agent: "manager", InstanceID: "i1", StepID: "s1",
			StepTitle: "Phase 1", Verb: "done", DurationMS: 6388, Status: telemetry.StatusClosed,
		})

		calls := 0
		orig := telemetry.Export
		telemetry.Export = func(config.TelemetryConfig, []byte) error { calls++; return nil }
		t.Cleanup(func() { telemetry.Export = orig })

		captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"report"}); err != nil {
				t.Fatalf("report: %v", err)
			}
		})
		if calls != 0 {
			t.Errorf("a plain report exported %d times, want 0", calls)
		}

		if err := telemetryCmd.Flags().Set("export", "true"); err != nil {
			t.Fatalf("set --export: %v", err)
		}
		captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"report"}); err != nil {
				t.Fatalf("report --export: %v", err)
			}
		})
		if calls != 1 {
			t.Errorf("report --export exported %d times, want exactly 1", calls)
		}
		if _, err := os.Stat(filepath.Join(config.TelemetryDir(root), ".cursor-manager")); err != nil {
			t.Errorf("report --export did not advance the export cursor: %v", err)
		}
	})

	t.Run("a traversing --agent is refused, not followed", func(t *testing.T) {
		setup(t)
		// The filter becomes a path segment under the records directory. The write side
		// validates for this reason; so must the read side.
		for _, bad := range []string{"../../../../etc/passwd", "a/b", ".."} {
			err := runTelemetry(telemetryCmd, []string{"report"})
			if err != nil {
				t.Fatalf("baseline report failed: %v", err)
			}
			if setErr := telemetryCmd.Flags().Set("agent", bad); setErr != nil {
				t.Fatalf("set --agent %q: %v", bad, setErr)
			}
			if err := runTelemetry(telemetryCmd, []string{"report"}); err == nil {
				t.Errorf("--agent %q was accepted; a report must not read a traversed path", bad)
			}
			if setErr := telemetryCmd.Flags().Set("agent", ""); setErr != nil {
				t.Fatalf("reset --agent: %v", setErr)
			}
		}
	})

	t.Run("an unknown verb still errors", func(t *testing.T) {
		root := setup(t)
		if err := os.Remove(telemetryGateFile(root)); err != nil {
			t.Fatalf("remove gate file: %v", err)
		}
		err := runTelemetry(telemetryCmd, []string{"weird"})
		if err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("unknown verb error = %v, want one mentioning usage", err)
		}
		if _, statErr := os.Stat(telemetryGateFile(root)); !os.IsNotExist(statErr) {
			t.Errorf("an unknown verb wrote the gate file; stat err = %v", statErr)
		}
	})
}

// resetReportFlags restores the report flags after a test. They are registered on the shared
// telemetryCmd, and every telemetry test calls runTelemetry directly rather than through cobra
// parsing, so a value set in one test would otherwise leak into the next.
func resetReportFlags(t *testing.T) {
	t.Helper()
	set := func(name, value string) {
		if err := telemetryCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("reset --%s: %v", name, err)
		}
	}
	set("instance", "")
	set("agent", "")
	set("export", "false")
	set("json", "false")
	t.Cleanup(func() {
		set("instance", "")
		set("agent", "")
		set("export", "false")
		set("json", "false")
	})
}

func lineContaining(t *testing.T, out, needle string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line containing %q in:\n%s", needle, out)
	return ""
}

// TestCorrelationKeysDerivablePerVerb pins that every identity key a record and a launched
// session share can actually be derived in each of the three verb contexts — and that where a
// value is genuinely unavailable the answer is the empty string, never a fabricated one.
//
// The sling row is the sharp one. At the instance_start site the worktree has not been created
// and .runtime/hooked_formula has not been written yet, so the instance id must come from the
// in-process value; a derivation that read the marker would report the PREVIOUS run's instance.
func TestCorrelationKeysDerivablePerVerb(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	workDir := filepath.Join(root, ".agentfactory", "agents", "manager")
	writeRuntimeFile(t, workDir, "worktree_id", "wt-abc123")
	writeRuntimeFile(t, workDir, "hooked_formula", "af-inst-01")
	writeRuntimeFile(t, workDir, "model_override", "fable-5")

	t.Run("prime and done contexts read every key from .runtime/", func(t *testing.T) {
		keys, _ := telemetryIdentity(root, workDir, "manager", "", "")
		if keys.Agent != "manager" {
			t.Errorf("Agent = %q, want %q", keys.Agent, "manager")
		}
		if keys.WorktreeID != "wt-abc123" {
			t.Errorf("WorktreeID = %q, want %q", keys.WorktreeID, "wt-abc123")
		}
		if keys.FormulaInstance != "af-inst-01" {
			t.Errorf("FormulaInstance = %q, want %q", keys.FormulaInstance, "af-inst-01")
		}
		if keys.ModelProfile != "fable-5" {
			t.Errorf("ModelProfile = %q, want %q", keys.ModelProfile, "fable-5")
		}
		// Several factories can share a host and a backend; without a factory id their data
		// mingles into one indistinguishable stream.
		if keys.FactoryID != "test-factory" {
			t.Errorf("FactoryID = %q, want the factory.json name %q", keys.FactoryID, "test-factory")
		}
	})

	t.Run("sling context prefers the in-process instance id over a stale marker", func(t *testing.T) {
		writeRuntimeFile(t, workDir, "hooked_formula", "STALE-DO-NOT-USE")
		keys, _ := telemetryIdentity(root, workDir, "manager", "af-inst-fresh", "")
		if keys.FormulaInstance != "af-inst-fresh" {
			t.Errorf("FormulaInstance = %q, want the fresh in-process id %q — af sling writes "+
				"hooked_formula AFTER the instance_start site, so the marker holds the previous run",
				keys.FormulaInstance, "af-inst-fresh")
		}
		writeRuntimeFile(t, workDir, "hooked_formula", "af-inst-01")
	})

	t.Run("an empty .runtime yields empty strings, never fabricated values", func(t *testing.T) {
		bare := setupTestFactoryForPrime(t)
		bareWork := filepath.Join(bare, ".agentfactory", "agents", "supervisor")
		keys, _ := telemetryIdentity(bare, bareWork, "supervisor", "", "")
		if keys.WorktreeID != "" || keys.FormulaInstance != "" || keys.ModelProfile != "" {
			t.Errorf("keys from an empty .runtime = %+v, want empty strings for the three markers", keys)
		}
		if keys.Agent != "supervisor" {
			t.Errorf("Agent = %q, want %q", keys.Agent, "supervisor")
		}
		for name, v := range map[string]string{
			"FactoryID": keys.FactoryID, "Agent": keys.Agent, "WorktreeID": keys.WorktreeID,
			"FormulaInstance": keys.FormulaInstance, "ModelProfile": keys.ModelProfile,
		} {
			if strings.ContainsAny(v, `/\`) || strings.Contains(v, "..") {
				t.Errorf("%s = %q carries a path separator; these values build file paths", name, v)
			}
		}
	})

	t.Run("a factory with no name falls back to the root's base name", func(t *testing.T) {
		bare := t.TempDir()
		if err := os.MkdirAll(filepath.Join(bare, ".agentfactory"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		keys, _ := telemetryIdentity(bare, bare, "manager", "", "")
		if keys.FactoryID != filepath.Base(bare) {
			t.Errorf("FactoryID = %q, want the root base name %q", keys.FactoryID, filepath.Base(bare))
		}
	})
}
