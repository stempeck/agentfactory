package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

func pinArgPluginDirs(args []string) []string {
	var dirs []string
	for i, a := range args {
		if a == "--plugin-dir" && i+1 < len(args) {
			dirs = append(dirs, args[i+1])
		}
	}
	return dirs
}

func pinWantBinds(t *testing.T, root string, pin integrationPin, name, snapAbs string) {
	t.Helper()
	rel, err := filepath.Rel(root, snapAbs)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range pin.Bindings {
		if b.Name == name {
			if b.SnapshotDir != filepath.ToSlash(rel) || b.ContentSHA256 != filepath.Base(snapAbs) {
				t.Errorf("pin binds %s at %s (%s), want %s (%s)", name, b.SnapshotDir, b.ContentSHA256, rel, filepath.Base(snapAbs))
			}
			return
		}
	}
	t.Errorf("pin %+v does not bind %s", pin, name)
}

func TestIntegrationPin_RespawnBindsOldSnapshot(t *testing.T) {
	t.Run("no-launch instantiation writes the pin beside hooked_formula", func(t *testing.T) {
		a := admFactory(t)
		snap := admRecordHealthy(t, a, "acme-int", nil)
		admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
		setSlingFlag(t, &slingFormulaName, "needs-acme")
		setSlingFlag(t, &slingAgent, admAgent)
		setSlingFlag(t, &slingNoLaunch, true)

		c, buf := refusalSlingCmd(t)
		if err := runFormulaInstantiation(c, a.root, a.agentDir, nil); err != nil {
			t.Fatalf("runFormulaInstantiation --no-launch: %v\n%s", err, buf)
		}
		if !intBExists(filepath.Join(a.agentDir, ".runtime", "hooked_formula")) {
			t.Fatal("fixture: no hooked_formula written")
		}
		pin, ok := pinFixtureRead(t, a.agentDir)
		if !ok {
			t.Fatal("--no-launch instantiation wrote no .runtime/integration_bindings beside hooked_formula")
		}
		if pin.Formula != "needs-acme" {
			t.Errorf("pin formula = %q, want needs-acme", pin.Formula)
		}
		pinWantBinds(t, a.root, pin, "acme-int", snap)
	})

	t.Run("dispatch writes the pin in the worktree agent dir", func(t *testing.T) {
		a := admFactory(t)
		snap := admRecordHealthy(t, a, "acme-int", nil)
		admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
		writeRefusalAgents(t, a.root, "needs-acme")
		installNoopLaunchSession(t)
		setSlingFlag(t, &slingNoLaunch, true)
		setSlingFlag(t, &slingCaller, "manager")
		t.Setenv("AF_WORKTREE", "")
		t.Setenv("AF_WORKTREE_ID", "")

		c, buf := refusalSlingCmd(t)
		if err := dispatchToSpecialist(c, a.root, a.root, admAgent, "task"); err != nil {
			t.Fatalf("dispatchToSpecialist: %v\n%s", err, buf)
		}
		matches, _ := filepath.Glob(filepath.Join(worktree.WorktreesDir(a.root), "*", ".agentfactory", "agents", admAgent))
		if len(matches) != 1 {
			t.Fatalf("worktree agent dirs = %v, want exactly one\n%s", matches, buf)
		}
		pin, ok := pinFixtureRead(t, matches[0])
		if !ok {
			t.Fatalf("dispatch wrote no pin in the worktree agent dir %s", matches[0])
		}
		pinWantBinds(t, a.root, pin, "acme-int", snap)
	})

	fx := newK14Fixture(t)
	old := recordFactoryIntegration(t, fx.root, "acme-int", "ACME_TOKEN", "one")
	cur := recordFactoryIntegration(t, fx.root, "acme-int", "ACME_TOKEN", "two")
	fake, _ := setupHermeticSessions(t)
	binding := pinFixtureBinding(t, fx.root, "acme-int", old, false)
	binding.EnvKeys = []string{"ACME_TOKEN"}
	pinPath := pinFixtureWrite(t, fx.agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{binding}})
	pinBytes, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("respawn binds the pinned snapshot after a re-install", func(t *testing.T) {
		run := fx.exec(t, fx.respawn(t, false).line)
		if dirs := pinArgPluginDirs(run.args); len(dirs) != 1 || dirs[0] != filepath.Join(old, "claude-plugin") {
			t.Errorf("respawn --plugin-dir = %q, want exactly the pinned %s", dirs, filepath.Join(old, "claude-plugin"))
		}
		if run.env["ACME_TOKEN"] != "one" {
			t.Errorf("respawn ACME_TOKEN = %q, want the pinned snapshot's %q", run.env["ACME_TOKEN"], "one")
		}
	})

	t.Run("af up keeps an existing pin", func(t *testing.T) {
		l := fx.up(t, fake)
		if l.line == "" {
			t.Fatalf("af up launched nothing: %v\n%s", l.err, l.output)
		}
		if dirs := pinArgPluginDirs(fx.exec(t, l.line).args); len(dirs) != 1 || dirs[0] != filepath.Join(old, "claude-plugin") {
			t.Errorf("af up --plugin-dir = %q, want the pinned %s", dirs, filepath.Join(old, "claude-plugin"))
		}
		got, err := os.ReadFile(pinPath)
		if err != nil || !bytes.Equal(got, pinBytes) {
			t.Errorf("af up rewrote the existing pin (err=%v):\n got: %s\nwant: %s", err, got, pinBytes)
		}
	})

	t.Run("without a pin the launch binds the current snapshot and writes none", func(t *testing.T) {
		if err := os.Remove(pinPath); err != nil {
			t.Fatal(err)
		}
		l := fx.up(t, fake)
		if l.line == "" {
			t.Fatalf("af up launched nothing: %v\n%s", l.err, l.output)
		}
		if dirs := pinArgPluginDirs(fx.exec(t, l.line).args); len(dirs) != 1 || dirs[0] != filepath.Join(cur, "claude-plugin") {
			t.Errorf("unpinned af up --plugin-dir = %q, want the current %s", dirs, filepath.Join(cur, "claude-plugin"))
		}
		if intBExists(pinPath) {
			t.Error("a bare af up with no pin wrote one (IR:L449)")
		}
	})

	t.Run("respawn after a no-launch instantiation binds the snapshot it pinned", func(t *testing.T) {
		fx := newK14Fixture(t)
		old := recordFactoryIntegration(t, fx.root, "acme-int", "ACME_TOKEN", "one")
		setupHermeticSessions(t)
		formulaTOML := "formula = \"needs-acme\"\ntype = \"workflow\"\nversion = 1\nintegrations = [\"acme-int\"]\n" +
			"\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
		if err := os.MkdirAll(config.FormulasDir(fx.root), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config.FormulasDir(fx.root), "needs-acme.formula.toml"), []byte(formulaTOML), 0o644); err != nil {
			t.Fatal(err)
		}
		setSlingFlag(t, &slingFormulaName, "needs-acme")
		setSlingFlag(t, &slingAgent, "manager")
		setSlingFlag(t, &slingNoLaunch, true)
		c, buf := refusalSlingCmd(t)
		if err := runFormulaInstantiation(c, fx.root, fx.agentDir, nil); err != nil {
			t.Fatalf("runFormulaInstantiation --no-launch: %v\n%s", err, buf)
		}

		recordFactoryIntegration(t, fx.root, "acme-int", "ACME_TOKEN", "two")
		l := fx.respawn(t, false)
		if l.line == "" {
			t.Fatalf("respawn launched nothing: %v\n%s", l.err, l.output)
		}
		run := fx.exec(t, l.line)
		if dirs := pinArgPluginDirs(run.args); len(dirs) != 1 || dirs[0] != filepath.Join(old, "claude-plugin") {
			t.Errorf("respawn --plugin-dir = %q, want exactly the no-launch pin's %s", dirs, filepath.Join(old, "claude-plugin"))
		}
		if run.env["ACME_TOKEN"] != "one" {
			t.Errorf("respawn ACME_TOKEN = %q, want the no-launch pin's %q", run.env["ACME_TOKEN"], "one")
		}

		cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(fx.root))
		if err != nil {
			t.Fatal(err)
		}
		delete(cfg.Plugins, "acme-int")
		if err := config.SavePluginsConfig(config.PluginsConfigPath(fx.root), cfg); err != nil {
			t.Fatal(err)
		}
		l = fx.respawn(t, false)
		if l.line == "" {
			t.Fatalf("respawn after removing the last integration launched nothing: %v\n%s", l.err, l.output)
		}
		if dirs := pinArgPluginDirs(fx.exec(t, l.line).args); len(dirs) != 1 || dirs[0] != filepath.Join(old, "claude-plugin") {
			t.Errorf("respawn after removing the last integration --plugin-dir = %q, want the pinned %s", dirs, filepath.Join(old, "claude-plugin"))
		}
	})
}

func TestIntegrationPin_ClearedByResetAndCompletion(t *testing.T) {
	const agent, formulaName = "worker", "plain"
	seed := func(t *testing.T, dir string) string {
		t.Helper()
		return pinFixtureWrite(t, dir, integrationPin{Formula: formulaName})
	}

	t.Run("dispatch clears a stale pin", func(t *testing.T) {
		root, agentDir := createTestFormulaFactory(t, formulaName, agent)
		installMemStore(t)
		writeSpecialistAgentsJSON(t, root, agent, formulaName)
		setSlingFlag(t, &slingNoLaunch, true)
		p := seed(t, agentDir)
		c, buf := refusalSlingCmd(t)
		if err := dispatchToSpecialist(c, root, root, agent, "task"); err != nil {
			t.Fatalf("dispatchToSpecialist: %v\n%s", err, buf)
		}
		if intBExists(p) {
			t.Error("dispatch left the previous instance's pin in place")
		}
	})

	t.Run("sling --formula --reset clears the pin", func(t *testing.T) {
		root, agentDir := createTestFormulaFactory(t, formulaName, agent)
		installMemStore(t)
		setSlingFlag(t, &slingFormulaName, formulaName)
		setSlingFlag(t, &slingAgent, agent)
		setSlingFlag(t, &slingNoLaunch, true)
		setSlingFlag(t, &slingReset, true)
		p := seed(t, agentDir)
		c, buf := refusalSlingCmd(t)
		if err := runFormulaInstantiation(c, root, agentDir, nil); err != nil {
			t.Fatalf("runFormulaInstantiation --reset: %v\n%s", err, buf)
		}
		if intBExists(p) {
			t.Error("sling --reset left the pin in place")
		}
	})

	t.Run("reset clears the worktree pin and spares a co-tenant", func(t *testing.T) {
		root, _ := createTestFormulaFactory(t, formulaName, agent)
		installMemStore(t)
		const wtID = "wt-cotnt"
		wtPath := filepath.Join(worktree.WorktreesDir(root), wtID)
		if err := worktree.WriteMeta(root, &worktree.Meta{
			ID: wtID, Owner: "peer", Branch: "af/peer-" + wtID,
			Path: filepath.ToSlash(filepath.Join(".agentfactory", "worktrees", wtID)), Agents: []string{"peer", agent},
		}); err != nil {
			t.Fatal(err)
		}
		mine := seed(t, config.AgentDir(wtPath, agent))
		peers := seed(t, config.AgentDir(wtPath, "peer"))
		var out bytes.Buffer
		if err := resetAgentState(t.Context(), &out, root, agent, config.CloseReasonResetSling); err != nil {
			t.Fatalf("resetAgentState: %v\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), "co-tenants remain") {
			t.Fatalf("fixture: reset did not take the co-tenant path:\n%s", out.String())
		}
		if intBExists(mine) {
			t.Error("reset left the agent's pin in its worktree agent dir while a co-tenant keeps the worktree")
		}
		if !intBExists(peers) {
			t.Error("reset removed the co-tenant's pin")
		}
	})

	t.Run("formula completion clears the pin", func(t *testing.T) {
		dir := t.TempDir()
		p := seed(t, dir)
		cleanupRuntimeArtifacts(dir)
		if intBExists(p) {
			t.Error("cleanupRuntimeArtifacts left .runtime/integration_bindings behind")
		}
	})
}

func TestFormulaFixture_OptionalIntegrationWithoutPluginsJSON(t *testing.T) {
	const agent, formulaName = "worker", "optfix"
	toml := "formula = \"" + formulaName + "\"\ntype = \"workflow\"\nversion = 1\nintegrations_optional = [\"defenseclaw\"]\n" +
		"\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
	root, agentDir := createTestFormulaFactoryWithTOML(t, formulaName, agent, toml)
	installMemStore(t)
	installNoopLaunchSession(t)
	if intBExists(config.PluginsConfigPath(root)) {
		t.Fatal("fixture: plugins.json exists")
	}
	setSlingFlag(t, &slingFormulaName, formulaName)
	setSlingFlag(t, &slingAgent, agent)
	setSlingFlag(t, &slingNoLaunch, true)

	c, buf := refusalSlingCmd(t)
	if err := runFormulaInstantiation(c, root, agentDir, nil); err != nil {
		t.Fatalf("sling of a formula whose only integration is optional and absent: %v\n%s", err, buf)
	}
	if !strings.Contains(buf.String(), "INTEGRATION_SKIPPED defenseclaw") {
		t.Errorf("sling output does not report the skipped optional integration:\n%s", buf)
	}

	t.Setenv("AF_ROLE", "")
	t.Chdir(agentDir)
	out := runPrimeCapturing(t)
	var named bool
	for _, l := range primeLinesNaming(out, "defenseclaw") {
		if strings.Contains(l, "skipped (optional)") {
			named = true
		}
	}
	if !named {
		t.Errorf("af prime does not name defenseclaw as skipped (optional):\n%s", out)
	}
}

// A relocated worktree records an absolute meta.Path; the reset must still find the agent's pin inside it.
func TestIntegrationPin_ResetClearsRelocatedWorktreePin(t *testing.T) {
	const agent, formulaName = "worker", "plain"
	root, _ := createTestFormulaFactory(t, formulaName, agent)
	installMemStore(t)
	const wtID = "wt-reloc"
	wtPath := filepath.Join(t.TempDir(), "relocated", wtID)
	if err := worktree.WriteMeta(root, &worktree.Meta{
		ID: wtID, Owner: "peer", Branch: "af/peer-" + wtID,
		Path: wtPath, Agents: []string{"peer", agent},
	}); err != nil {
		t.Fatal(err)
	}
	mine := pinFixtureWrite(t, config.AgentDir(wtPath, agent), integrationPin{Formula: formulaName})
	var out bytes.Buffer
	if err := resetAgentState(t.Context(), &out, root, agent, config.CloseReasonResetSling); err != nil {
		t.Fatalf("resetAgentState: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "co-tenants remain") {
		t.Fatalf("fixture: reset did not take the co-tenant path:\n%s", out.String())
	}
	if intBExists(mine) {
		t.Errorf("reset left the pin in the relocated worktree %s", wtPath)
	}
}

// With no pin, af up of an agent whose formula declares an integration admits it and pins the admitted set,
// so the session it launches is guarded like a slung one (IR:L441).
func TestIntegrationPin_UpWritesPinForDeclaringFormula(t *testing.T) {
	a := admFactory(t)
	snap := admRecordHealthy(t, a, "acme-int", nil)
	admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
	writeRefusalAgents(t, a.root, "needs-acme")
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	t.Chdir(a.root)
	fake, _ := setupHermeticSessions(t)
	fake.present["acme-int-svc"] = true
	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)
	stubIntegrationServicesEnsure(t, nil, nil)

	cmd, buf := launchCmd(t)
	if err := runUp(cmd, []string{admAgent}); err != nil {
		t.Fatalf("af up %s: %v\n%s", admAgent, err, buf)
	}
	matches, _ := filepath.Glob(filepath.Join(worktree.WorktreesDir(a.root), "*", ".agentfactory", "agents", admAgent))
	if len(matches) != 1 {
		t.Fatalf("worktree agent dirs = %v, want exactly one\n%s", matches, buf)
	}
	pin, ok := pinFixtureRead(t, matches[0])
	if !ok {
		t.Fatalf("af up wrote no pin in %s\n%s", matches[0], buf)
	}
	if pin.Formula != "needs-acme" {
		t.Errorf("pin formula = %q, want needs-acme", pin.Formula)
	}
	pinWantBinds(t, a.root, pin, "acme-int", snap)
}

func TestIntegrationPin_UpKeepsPresentPinForDeclaringFormula(t *testing.T) {
	a := admFactory(t)
	admRecordHealthy(t, a, "acme-int", nil)
	admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
	writeRefusalAgents(t, a.root, "needs-acme")
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	t.Chdir(a.root)
	fake, _ := setupHermeticSessions(t)
	fake.present["acme-int-svc"] = true
	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)
	stubIntegrationServicesEnsure(t, nil, nil)

	cmd, buf := launchCmd(t)
	if err := runUp(cmd, []string{admAgent}); err != nil {
		t.Fatalf("first af up %s: %v\n%s", admAgent, err, buf)
	}
	matches, _ := filepath.Glob(filepath.Join(worktree.WorktreesDir(a.root), "*", ".agentfactory", "agents", admAgent))
	if len(matches) != 1 {
		t.Fatalf("worktree agent dirs = %v, want exactly one\n%s", matches, buf)
	}
	pin, ok := pinFixtureRead(t, matches[0])
	if !ok {
		t.Fatalf("fixture: first af up wrote no pin in %s\n%s", matches[0], buf)
	}
	// The pin writer is deterministic, so only a pin naming a formula it would never stamp shows a rewrite in its bytes.
	pin.Formula = "in-flight-instance"
	pinPath := pinFixtureWrite(t, matches[0], pin)
	want, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatal(err)
	}

	relaunchCmd, relaunchOut := launchCmd(t)
	if err := runUp(relaunchCmd, []string{admAgent}); err != nil {
		t.Fatalf("second af up %s: %v\n%s", admAgent, err, relaunchOut)
	}
	out := relaunchOut.String()
	if !strings.Contains(out, "Started "+session.SessionName(admAgent)) || strings.Contains(out, "already running") {
		t.Errorf("second af up did not relaunch %s past the live-session skip and admission:\n%s", admAgent, out)
	}
	again, _ := filepath.Glob(filepath.Join(worktree.WorktreesDir(a.root), "*", ".agentfactory", "agents", admAgent))
	if len(again) != 1 || again[0] != matches[0] {
		t.Errorf("worktree agent dirs after the second af up = %v, want the re-attached %s\n%s", again, matches[0], out)
	}
	got, err := os.ReadFile(pinPath)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("af up replaced the in-flight instance's pin (err=%v):\n got: %s\nwant: %s", err, got, want)
	}
}

func TestIntegrationAdmission_RefusedUpLeavesNoWorktree(t *testing.T) {
	a := admFactory(t)
	admWriteFormula(t, a, "needs-absent", []string{"absent-int"}, nil, nil)
	writeRefusalAgents(t, a.root, "needs-absent")
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	t.Chdir(a.root)
	setupHermeticSessions(t)
	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)
	stubIntegrationServicesEnsure(t, nil, nil)

	cmd, buf := launchCmd(t)
	_ = runUp(cmd, []string{admAgent})
	if !strings.Contains(buf.String(), "absent-int") {
		t.Fatalf("af up did not refuse on the missing integration:\n%s", buf)
	}
	if matches, _ := filepath.Glob(filepath.Join(worktree.WorktreesDir(a.root), "*")); len(matches) != 0 {
		t.Errorf("a refused af up left worktree residue %v\n%s", matches, buf)
	}
}
