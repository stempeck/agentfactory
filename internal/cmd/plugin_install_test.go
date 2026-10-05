package cmd

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// ---- g1 helpers ---------------------------------------------------------------

type g1Seams struct {
	agentGen       int
	quickstart     int
	k9             int
	k15            int
	k9Names        [][]string
	projectDirs    []string
	quickstartArgs [][]string
}

func g1ResetInstallFlags(t *testing.T) {
	t.Helper()
	origAgents := installAgentsFlag
	t.Cleanup(func() { installAgentsFlag = origAgents })
	installAgentsFlag = false
}

// g1StubCountingPipeline layers counting recorders over stubInstallPipeline.
func g1StubCountingPipeline(t *testing.T) *g1Seams {
	t.Helper()
	g1ResetInstallFlags(t)
	stubInstallPipeline(t)
	s := &g1Seams{}
	runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error {
		s.agentGen++
		s.projectDirs = append(s.projectDirs, projectDir)
		return nil
	}
	stubQuickstart(func(projectDir string, extraArgs []string) error {
		s.quickstart++
		s.projectDirs = append(s.projectDirs, projectDir)
		s.quickstartArgs = append(s.quickstartArgs, append([]string{}, extraArgs...))
		return nil
	})
	runPluginVerifyReport = func(cmd *cobra.Command, projectDir string) { s.k15++ }
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error {
		s.k9++
		s.k9Names = append(s.k9Names, append([]string{}, names...))
		return nil
	})
	return s
}

func (s *g1Seams) assertNoneRan(t *testing.T) {
	t.Helper()
	if s.agentGen+s.quickstart+s.k9+s.k15 != 0 {
		t.Errorf("a refused install must run no seam: agentGen=%d quickstart=%d K9=%d K15=%d", s.agentGen, s.quickstart, s.k9, s.k15)
	}
}

func g1AssertZeroWrites(t *testing.T, root string, storeBefore map[string]string, agentsBefore []byte) {
	t.Helper()
	if after := snapshotTree(t, config.StoreDir(root)); !reflect.DeepEqual(storeBefore, after) {
		t.Errorf("store/ mutated on a refused install (K5 zero writes):\n before=%v\n after=%v", storeBefore, after)
	}
	if _, err := os.Stat(filepath.Join(config.FormulasDir(root), "acme-triage.formula.toml")); err == nil {
		t.Error("store/formulas/acme-triage.formula.toml staged before the refusal")
	}
	if _, err := os.Stat(config.PluginsConfigPath(root)); err == nil {
		t.Error("plugins.json written before the refusal")
	}
	if agentsAfter, _ := os.ReadFile(config.AgentsConfigPath(root)); !bytes.Equal(agentsBefore, agentsAfter) {
		t.Error("agents.json mutated on a refused install")
	}
}

func g1AcmeFactory(t *testing.T) string {
	t.Helper()
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})
	return dir
}

func g1ParseFile(t *testing.T, name string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return f
}

func g1FuncDecl(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("func %s not found", name)
	return nil
}

func g1CallsTo(n ast.Node, name string) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(n, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == name {
				calls = append(calls, c)
			}
		}
		return true
	})
	return calls
}

func g1IdentsIn(n ast.Node) map[string]bool {
	ids := map[string]bool{}
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok {
			ids[id.Name] = true
		}
		return true
	})
	return ids
}

// g1PackageFuncBodies maps every package-level func (and func-literal var) in the
// non-test files of internal/cmd to its body.
func g1PackageFuncBodies(t *testing.T) map[string]ast.Node {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	bodies := map[string]ast.Node{}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil && d.Body != nil {
						bodies[d.Name.Name] = d.Body
					}
				case *ast.GenDecl:
					for _, sp := range d.Specs {
						vs, ok := sp.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for i, v := range vs.Values {
							if fl, ok := v.(*ast.FuncLit); ok && i < len(vs.Names) {
								bodies[vs.Names[i].Name] = fl.Body
							}
						}
					}
				}
			}
		}
	}
	return bodies
}

// ---- T1/T6 (D3): relink placement ------------------------------------------

// PR #539 T1/T6: runPluginInstall reinstalls the running af, so it must relink first (D3).
func TestPluginInstallRelinkPrecedesStagingSource(t *testing.T) {
	fd := g1FuncDecl(t, g1ParseFile(t, "plugin.go"), "runPluginInstall")
	calls := g1CallsTo(fd.Body, "relinkSelfForReinstall")
	if len(calls) != 1 {
		t.Fatalf("runPluginInstall must call relinkSelfForReinstall exactly once, found %d call(s)", len(calls))
	}
	if len(fd.Body.List) == 0 {
		t.Fatal("runPluginInstall has an empty body")
	}
	first, ok := fd.Body.List[0].(*ast.ExprStmt)
	if !ok || first.X != calls[0] {
		t.Error("relinkSelfForReinstall(cmd) must be the FIRST statement of runPluginInstall (before getWd, output, or any write)")
	}
}

// PR #539 BODY-3/T6: relink stays out of the shared pipeline; each verb relinks once.
func TestInstallAgentsRelinksExactlyOnce(t *testing.T) {
	f := g1ParseFile(t, "install.go")
	ria := g1FuncDecl(t, f, "runInstallAgents")
	relinks := g1CallsTo(ria.Body, "relinkSelfForReinstall")
	if len(relinks) != 1 {
		t.Fatalf("runInstallAgents must call relinkSelfForReinstall exactly once, found %d", len(relinks))
	}
	pipes := g1CallsTo(ria.Body, "installAgentsPipeline")
	if len(pipes) != 1 {
		t.Fatalf("runInstallAgents must call installAgentsPipeline exactly once, found %d", len(pipes))
	}
	if relinks[0].Pos() > pipes[0].Pos() {
		t.Error("runInstallAgents must relink BEFORE installAgentsPipeline")
	}
	if first, ok := ria.Body.List[0].(*ast.ExprStmt); !ok || first.X != relinks[0] {
		t.Error("relinkSelfForReinstall(cmd) must be the FIRST statement of runInstallAgents: it can syscall.Exec and replay os.Args, so a prompt before it would be asked twice")
	}
	if n := len(g1CallsTo(g1FuncDecl(t, f, "installAgentsPipeline").Body, "relinkSelfForReinstall")); n != 0 {
		t.Errorf("installAgentsPipeline must not relink (it can syscall.Exec and replay the caller); found %d call(s)", n)
	}
	for name, body := range g1PackageFuncBodies(t) {
		if name == "runInstallAgents" || name == "runPluginInstall" || name == "relinkSelfForReinstall" {
			continue
		}
		if n := len(g1CallsTo(body, "relinkSelfForReinstall")); n != 0 {
			t.Errorf("%s calls relinkSelfForReinstall (%d); only the two verb entry points may", name, n)
		}
	}
}

// ---- T9 + T12.3 (D11, D12): preflight refusals before any write -------------

func TestPluginInstallAgentContextRefusesBeforeStaging(t *testing.T) {
	dir := g1AcmeFactory(t)
	s := g1StubCountingPipeline(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	t.Chdir(dir)
	storeBefore := snapshotTree(t, config.StoreDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

	_, err := runPlugin(t, "install", nil, "acme")

	g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
	s.assertNoneRan(t)
	assertTeardownRefused(t, err, "af plugin install")
}

func TestPluginInstallFromWorktreeRefusesBeforeStaging(t *testing.T) {
	realRoot := g1AcmeFactory(t)
	s := g1StubCountingPipeline(t)
	wt := t.TempDir()
	if err := os.MkdirAll(config.ConfigDir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.ConfigDir(wt), ".factory-root"), []byte(realRoot+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wt)
	storeBefore := snapshotTree(t, config.StoreDir(realRoot))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(realRoot))

	_, err := runPlugin(t, "install", nil, "acme")

	g1AssertZeroWrites(t, realRoot, storeBefore, agentsBefore)
	s.assertNoneRan(t)
	if err == nil {
		t.Fatal("af plugin install from a worktree must refuse")
	}
	if !strings.Contains(err.Error(), "cannot run af plugin install") || !strings.Contains(err.Error(), "worktree") {
		t.Errorf("worktree refusal must name the af plugin install surface and the worktree; got: %v", err)
	}
}

func TestPluginInstallMissingSourceTreeRefusesBeforeStaging(t *testing.T) {
	dir := g1AcmeFactory(t)
	s := g1StubCountingPipeline(t)
	t.Setenv("AF_SOURCE_ROOT", newAFSourceDir(t, []string{"agent-gen-all.sh"}, nil))
	t.Chdir(dir)
	storeBefore := snapshotTree(t, config.StoreDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

	_, err := runPlugin(t, "install", nil, "acme")

	g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
	s.assertNoneRan(t)
	if err == nil {
		t.Fatal("af plugin install with an incomplete source tree must refuse")
	}
	if !strings.Contains(err.Error(), "quickstart.sh missing") || !strings.Contains(err.Error(), "cannot run af plugin install") {
		t.Errorf("Guard-2 refusal must name the missing script and the af plugin install surface; got: %v", err)
	}
}

// PR #539 T9 (D11 iii): the plugin verb refuses a subdirectory cwd instead of staging at the root and running scripts in the subdir.
func TestPluginInstallFromSubdirTargetsFactoryRoot(t *testing.T) {
	dir := g1AcmeFactory(t)
	s := g1StubCountingPipeline(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	storeBefore := snapshotTree(t, config.StoreDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

	out, err := runPlugin(t, "install", nil, "acme")

	if err == nil {
		t.Errorf("af plugin install from a subdirectory must refuse (D11 iii); got success, scripts ran with projectDirs=%v\n%s", s.projectDirs, out)
	}
	g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
	s.assertNoneRan(t)
}

// ---- T13 + BODY-10 (D13, D14): verb policy stays with af install --agents ----

// g1Closure is every package func reachable from start through identifiers naming another package func.
func g1Closure(bodies map[string]ast.Node, start string) map[string]bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for id := range g1IdentsIn(bodies[n]) {
			if _, ok := bodies[id]; ok && !seen[id] {
				seen[id] = true
				queue = append(queue, id)
			}
		}
	}
	return seen
}

// PR #539 T1/BODY-6/C7: af plugin install reaches installAgentsPipeline, so verb policy anywhere in
// its call closure would override the operator's `af telemetry off` and gateway choices.
func TestInstallAgentsPipelineHasNoVerbPolicy(t *testing.T) {
	bodies := g1PackageFuncBodies(t)
	if _, ok := bodies["installAgentsPipeline"]; !ok {
		t.Fatal("installAgentsPipeline not found")
	}
	verbPolicy := []string{
		"runPluginVerifyReport", "requireOperatorTeardown", "relinkSelfForReinstall",
		"telemetryGateFile", "installNoTelemetryFlag", "installLitellmFlag", "installLitellmAuthFlag",
		"resolveLitellmAuthMode", "assertQuickstartSupports", "promptOpenAIKey",
		"preflightGatewayPort", "preflightCodexSubscription",
	}
	ids := map[string]bool{}
	for fn := range g1Closure(bodies, "installAgentsPipeline") {
		for id := range g1IdentsIn(bodies[fn]) {
			ids[id] = true
		}
	}
	for _, name := range verbPolicy {
		if ids[name] {
			t.Errorf("installAgentsPipeline (or a function it reaches) references %s; that is af install --agents policy and belongs in runInstallAgents", name)
		}
	}

	ria, ok := bodies["runInstallAgents"]
	if !ok {
		t.Fatal("runInstallAgents not found")
	}
	reach := g1IdentsIn(ria)
	for callee := range g1IdentsIn(ria) {
		if b, ok := bodies[callee]; ok && callee != "installAgentsPipeline" {
			for id := range g1IdentsIn(b) {
				reach[id] = true
			}
		}
	}
	for _, name := range []string{
		"runPluginVerifyReport", "telemetryGateFile", "installNoTelemetryFlag", "installLitellmFlag",
		"resolveLitellmAuthMode", "assertQuickstartSupports", "promptOpenAIKey",
		"preflightGatewayPort", "preflightCodexSubscription",
	} {
		if !reach[name] {
			t.Errorf("runInstallAgents (or a helper it calls directly) must own %s so af install --agents keeps main's behaviour", name)
		}
	}
}

// ---- BODY-12 (D24): duplicate plugin arguments -------------------------------

func TestPluginInstallDuplicateArgNotBatchCollision(t *testing.T) {
	dir := g1AcmeFactory(t)
	s := g1StubCountingPipeline(t)
	t.Chdir(dir)
	storeBefore := snapshotTree(t, config.StoreDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

	_, err := runPlugin(t, "install", nil, "acme", "acme")

	if err == nil {
		t.Fatal("af plugin install acme acme must refuse (D24)")
	}
	if strings.Contains(err.Error(), "shipped by both") {
		t.Errorf("duplicate argument misreported as a batch collision: %v", err)
	}
	if !strings.Contains(err.Error(), "named more than once") || !strings.Contains(err.Error(), "acme") {
		t.Errorf("refusal must say plugin \"acme\" is named more than once; got: %v", err)
	}
	g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
	s.assertNoneRan(t)
}

func TestPluginInstallGenuineBatchCollisionStillRefused(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{"x.formula.toml": validPluginFormula("x")})
	writePluginFixture(t, dir, "beta", map[string]string{"x.formula.toml": validPluginFormula("x")})
	s := g1StubCountingPipeline(t)
	t.Chdir(dir)
	storeBefore := snapshotTree(t, config.StoreDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

	_, err := runPlugin(t, "install", nil, "acme", "beta")

	if err == nil || !strings.Contains(err.Error(), `shipped by both "acme" and "beta"`) {
		t.Errorf("a genuine two-plugin collision must refuse as a batch collision; got: %v", err)
	}
	g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
	s.assertNoneRan(t)
}

// ---- BODY-13 (D13): verify once per plugin install ---------------------------

func TestPluginInstallVerifiesOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plugins []string
	}{
		{"single", []string{"acme"}},
		{"batch", []string{"acme", "beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := g1AcmeFactory(t)
			writePluginFixture(t, dir, "beta", map[string]string{"beta-scout.formula.toml": validPluginFormula("beta-scout")})
			s := g1StubCountingPipeline(t)
			t.Chdir(dir)

			out, err := runPlugin(t, "install", nil, tc.plugins...)
			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			if s.k9 != 1 {
				t.Errorf("K9 install-time verify ran %d times, want 1", s.k9)
			}
			if s.k15 != 0 {
				t.Errorf("K15 report-only re-verify ran %d times during af plugin install, want 0 (K9 is the one verify)", s.k15)
			}
		})
	}
}

// ---- BODY-16 (D26): composed preservation + once-per-batch -------------------

func TestPluginInstallPreservesOperatorArtifacts(t *testing.T) {
	dir := setupFactoryDir(t)
	mineBytes := storeFormulaVariant("mine")
	writeProjectFormula(t, dir, "mine.formula.toml", mineBytes)
	g1ResetInstallFlags(t)
	stubInstallPipeline(t)
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })

	if _, stderr, err := runFormulaAgentGenInDir(t, dir, "mine"); err != nil {
		t.Fatalf("seed agent-gen mine: %v\n%s", err, stderr)
	}
	agentsPath := config.AgentsConfigPath(dir)
	cfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	mine := cfg.Agents["mine"]
	mine.Model = "claude-opus-4-8"
	mine.SparsePaths = []string{"internal/", "docs/"}
	mine.BaseURL = "http://localhost:1234/v1/messages"
	mine.AuthToken = "sk-operator-secret"
	mine.ContinuousImprovement = true
	cfg.Agents["mine"] = mine
	cfg.Agents["handmade"] = config.AgentEntry{Type: "autonomous", Description: "hand-authored agent", Directive: "operator directive"}
	if err := config.SaveAgentConfig(agentsPath, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.LoadAgentConfig(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	mineBefore, handmadeBefore := cfg.Agents["mine"], cfg.Agents["handmade"]

	var regenerated []string
	runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error {
		entries, err := os.ReadDir(config.FormulasDir(projectDir))
		if err != nil {
			return err
		}
		for _, e := range entries {
			stem, ok := strings.CutSuffix(e.Name(), ".formula.toml")
			if !ok {
				continue
			}
			if _, stderr, err := runFormulaAgentGenInDir(t, projectDir, stem); err != nil {
				t.Errorf("in-process agent-gen %s: %v\n%s", stem, err, stderr)
				return err
			}
			regenerated = append(regenerated, stem)
		}
		return nil
	}
	writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": validPluginFormula("acme-triage")})
	t.Chdir(dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	sort.Strings(regenerated)
	if !reflect.DeepEqual(regenerated, []string{"acme-triage", "mine"}) {
		t.Fatalf("composed agent-gen did not regenerate the store: %v", regenerated)
	}
	if got, _ := os.ReadFile(filepath.Join(config.FormulasDir(dir), "mine.formula.toml")); string(got) != mineBytes {
		t.Error("operator store formula mine.formula.toml changed across af plugin install")
	}
	after, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Agents["handmade"], handmadeBefore) {
		t.Errorf("manual agent changed: %+v -> %+v", handmadeBefore, after.Agents["handmade"])
	}
	if !reflect.DeepEqual(after.Agents["mine"], mineBefore) {
		t.Errorf("operator fields on formula agent mine changed: %+v -> %+v", mineBefore, after.Agents["mine"])
	}
	if ae, ok := after.Agents["acme-triage"]; !ok || ae.Formula != "acme-triage" {
		t.Errorf("acme-triage not registered with its formula: %+v", ae)
	}
	m, err := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Plugins) != 1 || len(m.Plugins["acme"].Formulas) != 1 {
		t.Errorf("plugins.json must record only acme -> acme-triage: %+v", m.Plugins)
	}
	if _, ok := m.Plugins["acme"].Formulas["acme-triage"]; !ok {
		t.Errorf("plugins.json lacks acme -> acme-triage: %+v", m.Plugins)
	}
}

func TestPluginInstallBatchRunsPipelineOnce(t *testing.T) {
	t.Run("batch_of_two", func(t *testing.T) {
		dir := g1AcmeFactory(t)
		writePluginFixture(t, dir, "beta", map[string]string{"beta-scout.formula.toml": validPluginFormula("beta-scout")})
		s := g1StubCountingPipeline(t)
		t.Chdir(dir)

		out, err := runPlugin(t, "install", nil, "acme", "beta")
		if err != nil {
			t.Fatalf("install: %v\n%s", err, out)
		}
		if s.agentGen != 1 || s.quickstart != 1 || s.k9 != 1 {
			t.Errorf("a batch is one pipeline run: agentGen=%d quickstart=%d K9=%d, want 1/1/1", s.agentGen, s.quickstart, s.k9)
		}
		if len(s.k9Names) == 1 && !reflect.DeepEqual(s.k9Names[0], []string{"acme", "beta"}) {
			t.Errorf("K9 verified %v, want [acme beta]", s.k9Names[0])
		}
		m, err := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := m.Plugins["acme"]; !ok {
			t.Error("plugins.json lacks acme")
		}
		if _, ok := m.Plugins["beta"]; !ok {
			t.Error("plugins.json lacks beta")
		}
	})

	t.Run("batch_validation_failure_runs_nothing", func(t *testing.T) {
		dir := g1AcmeFactory(t)
		writePluginFixture(t, dir, "beta", map[string]string{"beta-scout.formula.toml": "not = [valid toml"})
		s := g1StubCountingPipeline(t)
		t.Chdir(dir)
		storeBefore := snapshotTree(t, config.StoreDir(dir))
		agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

		if _, err := runPlugin(t, "install", nil, "acme", "beta"); err == nil {
			t.Fatal("a batch with an unparseable formula must refuse")
		}
		g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
		s.assertNoneRan(t)
	})
}

// ---- T1 + BODY-6 (decisions D2-D4, D7): plugin install after the merge with main ----

func g1PinVerbFlags(t *testing.T, noTelemetry, litellm bool, litellmAuth string) {
	t.Helper()
	origNT, origL, origLA := installNoTelemetryFlag, installLitellmFlag, installLitellmAuthFlag
	t.Cleanup(func() { installNoTelemetryFlag, installLitellmFlag, installLitellmAuthFlag = origNT, origL, origLA })
	installNoTelemetryFlag, installLitellmFlag, installLitellmAuthFlag = noTelemetry, litellm, litellmAuth
}

// g1CountPrompts counts every interactive stop of the install verb and refuses it. The codex seams
// are pinned too: codex may be on the host's PATH, and a real device-auth login would block the test.
func g1CountPrompts(t *testing.T) *int {
	t.Helper()
	origKey, origConsent := promptOpenAIKey, promptCodexInstallConsent
	origLook, origValid, origAuth := lookPathCodex, codexSessionValid, runCodexDeviceAuth
	t.Cleanup(func() {
		promptOpenAIKey, promptCodexInstallConsent = origKey, origConsent
		lookPathCodex, codexSessionValid, runCodexDeviceAuth = origLook, origValid, origAuth
	})
	n := 0
	promptOpenAIKey = func(errW io.Writer, keyFile string) (string, error) { n++; return "", os.ErrNotExist }
	promptCodexInstallConsent = func(errW io.Writer) (bool, error) { n++; return false, nil }
	lookPathCodex = func() (string, error) { return "/usr/bin/codex", nil }
	codexSessionValid = func() bool { return false }
	runCodexDeviceAuth = func(ctx context.Context, out, errW io.Writer) error {
		n++
		return errors.New("codex device-auth reached (test stub)")
	}
	return &n
}

func g1SeedFile(path func(root string) string, content string) func(t *testing.T, root string) {
	return func(t *testing.T, root string) {
		t.Helper()
		p := path(root)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// PR #539 T1/BODY-6: "A plugin install must not silently override `af telemetry off`" — the gate is
// left byte-identical, an absent gate stays absent, and the af install --agents gate line is not printed.
func TestPluginInstallLeavesTelemetryGateByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed []byte
	}{{"absent", nil}, {"off", []byte("off\n")}, {"on", []byte("on\n")}, {"garbage", []byte("yes\n")}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := g1AcmeFactory(t)
			s := g1StubCountingPipeline(t)
			g1PinVerbFlags(t, false, false, "")
			t.Chdir(dir)
			gate := telemetryGateFile(dir)
			if tc.seed != nil {
				if err := os.WriteFile(gate, tc.seed, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			out, err := runPlugin(t, "install", nil, "acme")

			if err != nil || s.quickstart != 1 {
				t.Fatalf("install must run the pipeline once: err=%v quickstart=%d\n%s", err, s.quickstart, out)
			}
			after, rerr := os.ReadFile(gate)
			switch {
			case tc.seed == nil && !os.IsNotExist(rerr):
				t.Errorf("af plugin install created the telemetry gate (%q, err=%v); an absent gate must stay absent", after, rerr)
			case tc.seed != nil && !bytes.Equal(after, tc.seed):
				t.Errorf("af plugin install rewrote the telemetry gate %q -> %q (err=%v)", tc.seed, after, rerr)
			}
			if strings.Contains(out, "telemetry: ") {
				t.Errorf("af plugin install printed the af install --agents gate line:\n%s", out)
			}
		})
	}
}

// PR #539 T1/BODY-6 (D2): a plugin install carries no operator telemetry choice, so quickstart always
// skips provisioning the telemetry backend, whatever the gate says and whatever the verb globals hold.
func TestPluginInstallQuickstartTelemetryArg(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		seed                 []byte
		noTelemetry, litellm bool
		litellmAuth          string
	}{
		{"gate_absent", nil, false, false, ""},
		{"gate_off", []byte("off\n"), false, false, ""},
		{"gate_on", []byte("on\n"), false, false, ""},
		{"verb_globals_poisoned", []byte("on\n"), false, true, "codex-subscription"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := g1AcmeFactory(t)
			s := g1StubCountingPipeline(t)
			g1PinVerbFlags(t, tc.noTelemetry, tc.litellm, tc.litellmAuth)
			prompts := g1CountPrompts(t)
			t.Chdir(dir)
			if tc.seed != nil {
				if err := os.WriteFile(telemetryGateFile(dir), tc.seed, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			out, err := runPlugin(t, "install", nil, "acme")

			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			if *prompts != 0 {
				t.Errorf("af plugin install reached an interactive prompt %d time(s) (C-4, AC-7)", *prompts)
			}
			if !reflect.DeepEqual(s.quickstartArgs, [][]string{{"--no-telemetry"}}) {
				t.Errorf("af plugin install ran quickstart with %q, want exactly one run with [--no-telemetry]", s.quickstartArgs)
			}
		})
	}
}

// PR #539 T1/BODY-6 (D3): on a gateway factory the plugin verb leaves the gateway as deployed and says
// so. It never forwards --litellm, which can restart or reinstall the live gateway.
func TestPluginInstallGatewayFactoryLitellmMode(t *testing.T) {
	const reconcile = "af install --agents --litellm"
	litellmYAML := func(root string) string { return filepath.Join(config.ConfigDir(root), "litellm.yaml") }
	for _, tc := range []struct {
		name    string
		seed    func(t *testing.T, root string)
		gateway bool
	}{
		{"no_gateway", func(*testing.T, string) {}, false},
		{"recorded_api_key", g1SeedFile(gatewayAuthModeRecordPath, "api-key\n"), true},
		{"recorded_codex_subscription", g1SeedFile(gatewayAuthModeRecordPath, "codex-subscription\n"), true},
		{"relaunch_script_only", g1SeedFile(gatewayRelaunchScriptPath, "#!/bin/sh\n"), true},
		{"litellm_yaml_only", g1SeedFile(litellmYAML, "model_list: []\n"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := g1AcmeFactory(t)
			s := g1StubCountingPipeline(t)
			g1PinVerbFlags(t, false, false, "")
			prompts := g1CountPrompts(t)
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Chdir(dir)
			tc.seed(t, dir)

			out, err := runPlugin(t, "install", nil, "acme")

			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			for _, run := range s.quickstartArgs {
				for _, a := range run {
					if strings.HasPrefix(a, "--litellm") {
						t.Errorf("af plugin install forwarded %s to quickstart; the gateway must be left as deployed", a)
					}
				}
			}
			if got := strings.Contains(out, reconcile); got != tc.gateway {
				t.Errorf("gateway note naming %q present=%v, want %v:\n%s", reconcile, got, tc.gateway, out)
			}
			if *prompts != 0 {
				t.Errorf("af plugin install reached an interactive prompt %d time(s) (C-4, AC-7)", *prompts)
			}
		})
	}
}

// PR #539 T1: af install --agents still announces the gate it wrote; the plugin gate test relies on that
// line being verb-only.
func TestInstallAgentsPrintsGateLine(t *testing.T) {
	for _, tc := range []struct {
		name        string
		noTelemetry bool
		want        string
	}{{"default", false, "telemetry: on"}, {"no_telemetry", true, "telemetry: off"}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupFactoryDir(t)
			g1StubCountingPipeline(t)
			g1PinVerbFlags(t, tc.noTelemetry, false, "")
			t.Chdir(dir)
			cmd := &cobra.Command{}
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			if err := runInstallAgents(cmd); err != nil {
				t.Fatalf("runInstallAgents: %v\n%s", err, buf.String())
			}
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("af install --agents output lacks %q:\n%s", tc.want, buf.String())
			}
		})
	}
}

// PR #539 T1: main's AC-12 tripwire scans only the functions it names, so install verb policy that
// lands in any other function silently leaves its coverage.
func TestRerunTripwireCoversInstallVerbPolicy(t *testing.T) {
	targets := map[string]bool{}
	for _, n := range rerunTripwireGoTargets {
		targets[n] = true
	}
	watched := []string{"resolveLitellmAuthMode", "promptOpenAIKey", "assertQuickstartSupports",
		"preflightGatewayPort", "preflightCodexSubscription", "telemetryGateFile"}
	for _, d := range g1ParseFile(t, "install.go").Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		for _, w := range watched {
			if fd.Name.Name != w && len(g1CallsTo(fd.Body, w)) > 0 && !targets[fd.Name.Name] {
				t.Errorf("%s calls %s but is not in rerunTripwireGoTargets; the AC-12 tripwire no longer scans that install policy", fd.Name.Name, w)
			}
		}
	}
}

// PR #539 BODY-4 hunk 1 (D7): main's --litellm preflight paragraph documents the function that runs the
// preflights. On the shared pipeline it would tell a reader that af plugin install can reach device-auth.
func TestInstallPreflightParagraphFollowsItsCode(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "install.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	preflights := []string{"preflightGatewayPort", "preflightCodexSubscription"}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Doc == nil || fd.Body == nil {
			continue
		}
		for _, callee := range preflights {
			if fd.Name.Name != callee && strings.Contains(fd.Doc.Text(), callee) && len(g1CallsTo(fd.Body, callee)) == 0 {
				t.Errorf("%s's doc comment names %s, but its body does not call it", fd.Name.Name, callee)
			}
		}
	}
	ria := g1FuncDecl(t, f, "runInstallAgents")
	for _, callee := range preflights {
		if ria.Doc == nil || !strings.Contains(ria.Doc.Text(), callee) {
			t.Errorf("runInstallAgents' doc comment must carry main's --litellm preflight paragraph naming %s", callee)
		}
	}
}
