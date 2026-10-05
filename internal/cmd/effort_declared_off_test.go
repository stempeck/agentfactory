package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// #707: switching tokenomics off stops the effort actuator; it must not delete the level an operator
// declared in a models.json profile. These tests launch through the REAL legs rather than calling
// withEffortLevel, because the deletion was only visible downstream of it: the #602 universe turns a
// key missing from the launch env into an `unset` on the launch line.
//
// Not parallel: setupHermeticSessions swaps package-global seams.

const profileEffortLevel = "high"

// writeDeclaredEffortProfile gives manager the profile shape the operator's own models.json has on
// every profile (601158fd).
func writeDeclaredEffortProfile(t *testing.T, root string) {
	t.Helper()
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"hi": {"ANTHROPIC_MODEL": "claude-opus-5-5", config.EnvEffortLevel: profileEffortLevel},
		},
		Agents: map[string]string{"manager": "hi"},
	})
}

// plantStaleLaunchEffort is the attestation an earlier arm-on launch left in the environment a
// relaunch reuses. The arm-off leg must export it empty, or af prime records a reduction for a session
// that ran at the operator's own level.
func plantStaleLaunchEffort(t *testing.T) {
	t.Helper()
	plantLaunchEffort(t, "medium", string(tokenomics.ObjectiveEfficiency), "step-2", "offpath")
}

func stageTokenomicsGate(state string) func(*testing.T, string) {
	return func(t *testing.T, root string) {
		if err := writeTokenomicsGate(root, state); err != nil {
			t.Fatal(err)
		}
	}
}

// tokenomicsOffConditions reach withEffortLevel's off branch through different gates — the gate file,
// the umbrella key, the mechanism key, launchPolicy's fail-closed load — so a fix honouring the
// declared level on only one of them fails here.
var tokenomicsOffConditions = []struct {
	name  string
	stage func(t *testing.T, root string)
	// af up refuses a startup.json it cannot load before starting any agent, so there is no af up
	// launch to observe under this condition.
	unloadableStartup bool
}{
	{name: "no .tokenomics file", stage: func(*testing.T, string) {}},
	{name: ".tokenomics is off", stage: stageTokenomicsGate("off")},
	{name: ".tokenomics is the near-miss ON", stage: stageTokenomicsGate("ON")},
	{name: "tokenomics.effort is off", stage: func(t *testing.T, root string) {
		armAdvisoryPolicy(t, root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "off"})
	}},
	{name: "tokenomics.enabled is off", stage: func(t *testing.T, root string) {
		armAdvisoryPolicy(t, root, advisoryMarginPct, advisoryMinRuns, map[string]string{"enabled": "off"})
	}},
	{name: "startup.json is unloadable", unloadableStartup: true, stage: func(t *testing.T, root string) {
		armAdvisoryPolicy(t, root, advisoryMarginPct, advisoryMinRuns, nil)
		if err := os.WriteFile(config.StartupConfigPath(root), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("corrupt startup.json: %v", err)
		}
	}},
}

func respawnLaunchLine(t *testing.T, root, agentDir string) string {
	t.Helper()
	mock := &mockTmux{}
	if err := respawnSession(RespawnOptions{
		FactoryRoot:  root,
		AgentName:    "manager",
		AgentEntry:   config.AgentEntry{Type: "interactive"},
		AgentWorkDir: agentDir,
		PaneID:       "%0",
		Tx:           mock,
	}); err != nil {
		t.Fatalf("respawnSession: %v", err)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Fatalf("RespawnPane called %d times, want 1", len(mock.respawnPaneCalls))
	}
	return mock.respawnPaneCalls[0].cmd
}

func launchCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	c := &cobra.Command{}
	c.SetContext(t.Context())
	c.SetOut(&buf)
	c.SetErr(&buf)
	return c, &buf
}

func launchLineUnsets(line, key string) bool {
	for _, segment := range strings.Split(line, " && ") {
		if fields := strings.Fields(segment); len(fields) > 0 && fields[0] == "unset" && slices.Contains(fields[1:], key) {
			return true
		}
	}
	return false
}

func exportedValue(line, key string) string {
	if m := regexp.MustCompile(regexp.QuoteMeta(key) + `='([^']*)'`).FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// assertLaunchLineCarriesDeclaredEffort checks both halves because either alone lets the bug through:
// an unset segment runs after the export, so a line with both still starts the host at its default.
func assertLaunchLineCarriesDeclaredEffort(t *testing.T, line string) {
	t.Helper()
	if got := exportedValue(line, config.EnvEffortLevel); got != profileEffortLevel {
		t.Errorf("the launch exports %s=%q, want the profile's %q: %s", config.EnvEffortLevel, got, profileEffortLevel, line)
	}
	if launchLineUnsets(line, config.EnvEffortLevel) {
		t.Errorf("the launch line unsets the %s the profile declared: %s", config.EnvEffortLevel, line)
	}
}

// assertSessionCarriesDeclaredEffort covers the Start() legs. The launch line is the level's only
// carrier: a copy in the tmux session env would outlive this launch into every later respawn.
func assertSessionCarriesDeclaredEffort(t *testing.T, ops []string) {
	t.Helper()
	var launch string
	for _, op := range ops {
		switch {
		case strings.HasPrefix(op, "SetEnvironment ") && strings.Contains(op, " "+config.EnvEffortLevel+"="):
			t.Errorf("Start wrote the effort level into the tmux session env: %s", op)
		case strings.HasPrefix(op, "SendKeysDelayed ") && strings.Contains(op, " && claude "):
			launch = op
		}
	}
	if launch == "" {
		t.Fatalf("no claude launch was sent; the leg never reached mgr.Start. ops=%v", ops)
	}
	assertLaunchLineCarriesDeclaredEffort(t, launch)
}

var effortAttestationKeys = []string{config.EnvEffortObjective, config.EnvEffortStepLabel, config.EnvEffortFormula}

// assertLaunchLineAttestsNothing requires the clear to be EMITTED: a line that merely omitted the keys
// would leave a reused pane carrying the previous launch's attestation.
func assertLaunchLineAttestsNothing(t *testing.T, line string) {
	t.Helper()
	for _, key := range effortAttestationKeys {
		if !strings.Contains(line, " "+key+"=''") {
			t.Errorf("a launch that selected nothing does not export %s empty: %s", key, line)
		}
	}
}

// assertEnvAttestsNothing is the same check on the model env a launch leg composes.
func assertEnvAttestsNothing(t *testing.T, env []config.EnvVar) {
	t.Helper()
	for _, key := range effortAttestationKeys {
		if !modelEnvHasKey(env, key) || modelEnvValue(env, key) != "" {
			t.Errorf("a launch that selected nothing does not export %s empty; a reused pane would go on "+
				"attesting the previous session's reduction: %v", key, env)
		}
	}
}

// attestationOf reads a launch env back the way the session it starts will see it.
func attestationOf(env []config.EnvVar) launchEffort {
	return launchEffort{
		Level:     modelEnvValue(env, config.EnvEffortLevel),
		Objective: modelEnvValue(env, config.EnvEffortObjective),
		StepLabel: modelEnvValue(env, config.EnvEffortStepLabel),
		Formula:   modelEnvValue(env, config.EnvEffortFormula),
	}
}

// inheritLaunch is the launched session's side: it starts with whatever its launch env exported.
func inheritLaunch(t *testing.T, env []config.EnvVar) {
	t.Helper()
	for _, key := range append([]string{config.EnvEffortLevel}, effortAttestationKeys...) {
		t.Setenv(key, modelEnvValue(env, key))
	}
}

// assertSessionAttestsNothing covers the Start() legs: the launch line clears the attestation, and the
// tmux session env never holds one for a later respawn to inherit.
func assertSessionAttestsNothing(t *testing.T, ops []string) {
	t.Helper()
	var launch string
	for _, op := range ops {
		if strings.HasPrefix(op, "SendKeysDelayed ") && strings.Contains(op, " && claude ") {
			launch = op
		}
	}
	for _, key := range effortAttestationKeys {
		if slices.ContainsFunc(ops, func(op string) bool {
			return strings.HasPrefix(op, "SetEnvironment ") && strings.Contains(op, " "+key+"=")
		}) {
			t.Errorf("Start wrote %s into the tmux session env. ops=%v", key, ops)
		}
	}
	assertLaunchLineAttestsNothing(t, launch)
}

func TestDeclaredEffortLevelReachesSessionWhenTokenomicsOff_Respawn(t *testing.T) {
	for _, c := range tokenomicsOffConditions {
		t.Run(c.name, func(t *testing.T) {
			root := setupTestFactoryForDone(t, "manager")
			writeDeclaredEffortProfile(t, root)
			c.stage(t, root)
			agentDir := config.AgentDir(root, "manager")
			plantStaleLaunchEffort(t)

			line := respawnLaunchLine(t, root, agentDir)
			assertLaunchLineCarriesDeclaredEffort(t, line)
			assertLaunchLineAttestsNothing(t, line)
		})
	}
}

func TestDeclaredEffortLevelReachesSessionWhenTokenomicsOff_Up(t *testing.T) {
	for _, c := range tokenomicsOffConditions {
		if c.unloadableStartup {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			root := setupTestFactoryForDone(t, "manager")
			initTestGitRepo(t, root)
			writeDeclaredEffortProfile(t, root)
			c.stage(t, root)
			const wtID = "wt-707up"
			wtPath := filepath.Join(root, ".agentfactory", "worktrees", wtID)
			if err := os.MkdirAll(config.AgentDir(wtPath, "manager"), 0o755); err != nil {
				t.Fatal(err)
			}
			plantStaleLaunchEffort(t)
			t.Setenv("AF_WORKTREE", wtPath)
			t.Setenv("AF_WORKTREE_ID", wtID)
			t.Chdir(root)
			fake, _ := setupHermeticSessions(t)

			cmd, out := launchCmd(t)
			err := runUp(cmd, []string{"manager"})
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("af up: err=%v\n%s", err, out)
				}
			})

			assertSessionCarriesDeclaredEffort(t, fake.ops)
			assertSessionAttestsNothing(t, fake.ops)
		})
	}
}

func TestDeclaredEffortLevelReachesSessionWhenTokenomicsOff_Sling(t *testing.T) {
	const wtID = "wt-707aaa"
	for _, c := range tokenomicsOffConditions {
		t.Run(c.name, func(t *testing.T) {
			root := setupTestFactoryForDone(t, "manager")
			writeDeclaredEffortProfile(t, root)
			c.stage(t, root)
			// A launch without a worktree is refused, and so is one whose agent workspace was never
			// provisioned inside it.
			wtPath := filepath.Join(root, ".agentfactory", "worktrees", wtID)
			agentDir := config.AgentDir(wtPath, "manager")
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatalf("provision worktree agent dir: %v", err)
			}
			plantStaleLaunchEffort(t)
			fake, _ := setupHermeticSessions(t)

			cmd, out := launchCmd(t)
			if err := launchAgentSession(cmd, root, "manager", wtPath, wtID, "", false); err != nil {
				t.Fatalf("af sling launch: %v\n%s", err, out)
			}

			assertSessionCarriesDeclaredEffort(t, fake.ops)
			assertSessionAttestsNothing(t, fake.ops)
		})
	}
}

func TestStaleEffortAttestationClearedWhenArmOffUnderDeclaringProfile(t *testing.T) {
	fx := newLifecycleFixture(t)
	writeDeclaredEffortProfile(t, fx.root)
	armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "off"})
	plantStaleLaunchEffort(t)

	got := withEffortLevel(fx.root, fx.workDir, launchEnv(profileEffortLevel), "step-2", "")

	assertEnvAttestsNothing(t, got)
	if lvl := effortLevelIn(got); lvl != profileEffortLevel {
		t.Errorf("%s = %q, want the declared %q", config.EnvEffortLevel, lvl, profileEffortLevel)
	}
}

// TestTelemetryReportsDeclaredEffortLevelWhenTokenomicsOff follows the level from the launch line into
// the records: the session inherits what its launch exported, and prime and done echo that.
func TestTelemetryReportsDeclaredEffortLevelWhenTokenomicsOff(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, t.TempDir())
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)
	writeDeclaredEffortProfile(t, fx.root)
	plantStaleLaunchEffort(t)

	if err := slingOnce(t, fx); err != nil {
		t.Fatalf("af sling: %v", err)
	}
	launchedAttestation(t, respawnLaunchLine(t, fx.root, fx.workDir))

	origHook := primeHookMode
	primeHookMode = true
	t.Cleanup(func() { primeHookMode = origHook })
	primeWithHookSession(t, "sess-707")
	if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
		t.Fatalf("af done: %v", err)
	}

	if start := findRecord(t, fx, telemetry.EventSessionStart); start.EffortLevel != profileEffortLevel {
		t.Errorf("session_start effort_level = %q, want the declared %q", start.EffortLevel, profileEffortLevel)
	}
	for _, ev := range interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismEffort)] {
		if ev.Action == telemetry.ActionReduceEffort {
			t.Errorf("a reduce_effort record (level %q) was written for a session that ran at the "+
				"operator's declared level", ev.EffortLevel)
		}
	}
	if end := lastStepEnd(t, fx.root, fx.agent); end.EffortLevel != profileEffortLevel {
		t.Errorf("step_end effort_level = %q, want the declared %q", end.EffortLevel, profileEffortLevel)
	}
}

// turnInterventionsFrom runs the verb the way the fidelity gate does: from the session's own
// directory, under the session's AF_ROOT, always naming the agent. Only this entry point reads the
// attestation the way the gate does, from the verb's own environment.
func turnInterventionsFrom(t *testing.T, root, cwd, agent, since string) string {
	t.Helper()
	t.Chdir(cwd)
	t.Setenv("AF_ROOT", root)
	c := &cobra.Command{}
	c.Flags().String("since", since, "")
	c.Flags().String("agent", agent, "")
	var out bytes.Buffer
	c.SetOut(&out)
	if err := runTurnInterventionsCmd(c, nil); err != nil {
		t.Fatalf("af turn interventions: %v", err)
	}
	return out.String()
}

func plantEffortRecord(t *testing.T, root, agent, ts, level string) {
	t.Helper()
	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
		TS: ts, Agent: agent, Verb: "prime", Formula: "offpath", StepID: "bd-708",
		Mechanism: string(tokenomics.MechanismEffort), Action: telemetry.ActionReduceEffort,
		Objective: telemetry.ObjectiveEfficiency, EffortLevel: level,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
}

const (
	turnBoundary       = "2026-08-09T10:00:00Z"
	turnBeforeBoundary = "2026-08-09T09:59:59.000Z"
	turnInWindow       = "2026-08-09T10:00:05.000Z"
)

// TestTurnInterventionsOmitsDeclaredEffortWhenTokenomicsOff: the session inherits the declared level
// its arm-off launch exported, and an earlier session left a reduce_effort record in the log. Neither
// is a reduction of this session, so the grader must not be told of one.
func TestTurnInterventionsOmitsDeclaredEffortWhenTokenomicsOff(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	writeDeclaredEffortProfile(t, root)
	agentDir := config.AgentDir(root, "manager")
	plantStaleLaunchEffort(t)
	line := respawnLaunchLine(t, root, agentDir)
	exported := exportedValue(line, config.EnvEffortLevel)
	if exported != profileEffortLevel {
		t.Fatalf("the arm-off launch exported %q, want the declared %q; the test would prove nothing", exported, profileEffortLevel)
	}
	launchedAttestation(t, line)
	plantEffortRecord(t, root, "manager", turnBeforeBoundary, "medium")

	if got := turnInterventionsFrom(t, root, agentDir, "manager", turnBoundary); strings.Contains(got, string(tokenomics.MechanismEffort)) {
		t.Errorf("tokenomics is off and the session runs at the profile's declared %q, yet the grader "+
			"was told the harness reduced it:\n%q", exported, got)
	}
}

func TestTokenomicsOnDeclaredEffortUnchanged(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	writeDeclaredEffortProfile(t, root)
	armAdvisoryPolicy(t, root, advisoryMarginPct, advisoryMinRuns, nil)

	assertLaunchLineCarriesDeclaredEffort(t, respawnLaunchLine(t, root, config.AgentDir(root, "manager")))
}

// TestProfileSwitchHygieneUnsetsUndeclaredEffortLevel pins the #602 half this fix must not disturb: an
// agent whose own profile declares no level still sheds one another profile declares.
func TestProfileSwitchHygieneUnsetsUndeclaredEffortLevel(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"hi":    {"ANTHROPIC_MODEL": "claude-opus-5-5", config.EnvEffortLevel: profileEffortLevel},
			"plain": {"ANTHROPIC_MODEL": "claude-opus-5-5"},
		},
		Agents: map[string]string{"manager": "plain"},
	})

	line := respawnLaunchLine(t, root, config.AgentDir(root, "manager"))

	if !launchLineUnsets(line, config.EnvEffortLevel) {
		t.Errorf("the agent's profile declares no %s and another does, so the launch must unset it: %s",
			config.EnvEffortLevel, line)
	}
	if got := exportedValue(line, config.EnvEffortLevel); got != "" {
		t.Errorf("the launch exports %s=%q from a profile that does not declare it: %s", config.EnvEffortLevel, got, line)
	}
}

// launchedAttestation simulates a session launched with `line`: it sets the four env vars a launch
// line may export to whatever value THIS line carries for each, so a reader consulting its own
// process env (post-fix) sees exactly what a process launched by this line would see. On the base
// tree, before these names exist on any launch line, every one of the four is absent from `line` and
// every t.Setenv below is a harmless no-op — the base tree's attestation instead comes from the FILE
// that the real, unmocked withEffortLevel call wrote as a side effect of resolving the same launch.
func launchedAttestation(t *testing.T, line string) {
	t.Helper()
	for _, key := range []string{
		"CLAUDE_CODE_EFFORT_LEVEL", "AF_EFFORT_OBJECTIVE", "AF_EFFORT_STEP_LABEL", "AF_EFFORT_FORMULA",
	} {
		t.Setenv(key, exportedValue(line, key))
	}
}

// modelEnvHasKey is the presence half modelEnvValue cannot answer: it returns "" both for an absent
// key and for one exported empty, and a clear is only a clear when it is emitted.
func modelEnvHasKey(env []config.EnvVar, key string) bool {
	for _, e := range env {
		if e.Key == key {
			return true
		}
	}
	return false
}
