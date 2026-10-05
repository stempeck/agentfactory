package cmd

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

const k14StubClaude = "#!/bin/bash\nenv > \"$K14_OUT/env\"\nprintf '%s\\n' \"$@\" > \"$K14_OUT/args\"\n"

// k14GoldenLine is af up's launch line for the (a) fixture, captured before the composer existed:
// moving where the line's values come from must not move a single byte of it.
const k14GoldenLine = "export AF_ROOT='<ROOT>' AF_ROLE='manager' AF_ACTOR='manager' " +
	"AF_WORKTREE='<ROOT>/.agentfactory/worktrees/wt-k14par' AF_WORKTREE_ID='wt-k14par' " +
	"CLAUDE_CODE_ENABLE_TELEMETRY='' OTEL_METRICS_EXPORTER='' OTEL_LOGS_EXPORTER='' OTEL_EXPORTER_OTLP_PROTOCOL='' " +
	"OTEL_EXPORTER_OTLP_ENDPOINT='' OTEL_EXPORTER_OTLP_HEADERS='' OTEL_RESOURCE_ATTRIBUTES='' " +
	"OPENAI_API_KEY='' CHATGPT_TOKEN_DIR='' CHATGPT_AUTH_FILE='' CHATGPT_API_BASE='' CODEX_HOME='' " +
	"AF_EFFORT_OBJECTIVE='' AF_EFFORT_STEP_LABEL='' AF_EFFORT_FORMULA='' " +
	"GIT_AUTHOR_NAME='agentfactory-cli' GIT_AUTHOR_EMAIL='293373236+agentfactory-cli@users.noreply.github.com' " +
	"GIT_COMMITTER_NAME='agentfactory-cli' GIT_COMMITTER_EMAIL='293373236+agentfactory-cli@users.noreply.github.com' " +
	"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0='core.hooksPath' GIT_CONFIG_VALUE_0='<ROOT>/.agentfactory/githooks' " +
	"AF_COAUTHOR_NAME='agentfactory-cli' AF_COAUTHOR_EMAIL='293373236+agentfactory-cli@users.noreply.github.com' " +
	"&& claude --dangerously-skip-permissions"

type k14Fixture struct {
	root, wtPath, wtID, agentDir, stubDir, home string
}

func newK14Fixture(t *testing.T) *k14Fixture {
	t.Helper()
	stubDir, err := tryExecCapableDir(t, "af-test-k14parity")
	if err != nil {
		t.Fatalf("no exec-capable directory for the stub claude: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stubDir, "claude"), []byte(k14StubClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	// The developer's own git identity would otherwise decide whether GIT_AUTHOR_* is exported.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)

	root := setupTestFactoryForDone(t, "manager")
	k14Git(t, root, "init", "-q")
	const wtID = "wt-k14par"
	wtPath := filepath.Join(root, ".agentfactory", "worktrees", wtID)
	agentDir := config.AgentDir(wtPath, "manager")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &k14Fixture{root: root, wtPath: wtPath, wtID: wtID, agentDir: agentDir, stubDir: stubDir, home: home}
}

func k14Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (fx *k14Fixture) entry(t *testing.T) config.AgentEntry {
	t.Helper()
	cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(fx.root))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Agents["manager"]
}

type k14Launch struct {
	line   string
	ops    []string
	output string
	err    error
}

func k14LineFromOps(ops []string, sess string) string {
	prefix := "SendKeysDelayed " + sess + " "
	for _, op := range ops {
		if rest, ok := strings.CutPrefix(op, prefix); ok {
			return rest[:strings.LastIndex(rest, " ")]
		}
	}
	return ""
}

func k14SessionOps(ops []string, sess string) (set, unset []string) {
	for _, op := range ops {
		if rest, ok := strings.CutPrefix(op, "SetEnvironment "+sess+" "); ok {
			set = append(set, rest)
		}
		if rest, ok := strings.CutPrefix(op, "UnsetEnvironment "+sess+" "); ok {
			unset = append(unset, rest)
		}
	}
	return set, unset
}

func (fx *k14Fixture) up(t *testing.T, fake *fakeTmux) k14Launch {
	t.Helper()
	t.Setenv("AF_WORKTREE", fx.wtPath)
	t.Setenv("AF_WORKTREE_ID", fx.wtID)
	t.Setenv("AF_ROOT", fx.root)
	t.Chdir(fx.root)
	start := len(fake.ops)
	cmd, buf := launchCmd(t)
	var err error
	stderr := captureStderr(t, func() { err = runUp(cmd, []string{"manager"}) })
	ops := append([]string(nil), fake.ops[start:]...)
	return k14Launch{line: k14LineFromOps(ops, session.SessionName("manager")), ops: ops, output: buf.String() + stderr, err: err}
}

func (fx *k14Fixture) sling(t *testing.T, fake *fakeTmux, cliModel string) k14Launch {
	t.Helper()
	start := len(fake.ops)
	cmd, buf := launchCmd(t)
	var err error
	stderr := captureStderr(t, func() {
		err = launchAgentSession(cmd, fx.root, "manager", fx.wtPath, fx.wtID, cliModel, false)
	})
	ops := append([]string(nil), fake.ops[start:]...)
	return k14Launch{line: k14LineFromOps(ops, session.SessionName("manager")), ops: ops, output: buf.String() + stderr, err: err}
}

// respawn drives the recycle leg. The watchdog shape (WorktreePath set) is the one whose line can match
// af up's byte for byte; the handoff shape passes no WorktreePath, so its line never carries AF_WORKTREE*.
func (fx *k14Fixture) respawn(t *testing.T, handoffShaped bool) k14Launch {
	t.Helper()
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot:  fx.root,
		AgentName:    "manager",
		AgentEntry:   fx.entry(t),
		AgentWorkDir: fx.agentDir,
		PaneID:       "%0",
		Tx:           mock,
	}
	if !handoffShaped {
		opts.WorktreePath, opts.WorktreeID = fx.wtPath, fx.wtID
	}
	var err error
	stderr := captureStderr(t, func() { err = respawnSession(opts) })
	var line string
	if len(mock.respawnPaneCalls) == 1 {
		line = mock.respawnPaneCalls[0].cmd
	}
	return k14Launch{line: line, output: stderr, err: err}
}

type k14Run struct {
	env  map[string]string
	args []string
}

// exec runs the line the way tmux would, against a stub claude, so the assertions read the
// environment the launched process actually received rather than the text of the line.
func (fx *k14Fixture) exec(t *testing.T, line string) k14Run {
	t.Helper()
	if line == "" {
		t.Fatal("no launch line was captured; the path never reached the emitter")
	}
	out := t.TempDir()
	c := exec.Command("bash", "-c", line)
	c.Dir = fx.agentDir
	c.Env = []string{"PATH=" + fx.stubDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + fx.home, "K14_OUT=" + out}
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("bash -c <line>: %v\n%s\nline: %s", err, b, line)
	}
	envData, err := os.ReadFile(filepath.Join(out, "env"))
	if err != nil {
		t.Fatalf("the stub claude never ran (%v); line: %s", err, line)
	}
	argData, _ := os.ReadFile(filepath.Join(out, "args"))
	env := map[string]string{}
	for _, kv := range strings.Split(strings.TrimRight(string(envData), "\n"), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return k14Run{env: env, args: strings.Split(strings.TrimRight(string(argData), "\n"), "\n")}
}

func k14AssertParity(t *testing.T, up, sling, respawn string) {
	t.Helper()
	if up != sling {
		t.Errorf("af up and af sling lines differ:\n up:   %s\n sling: %s", up, sling)
	}
	if got := strings.TrimSuffix(respawn, " 'af prime'"); got != up {
		t.Errorf("respawn line differs from af up beyond the 'af prime' prompt:\n up:      %s\n respawn: %s", up, respawn)
	}
}

var k14ResAttrModel = regexp.MustCompile(`af\.model_profile=([^,]*)`)

func TestLaunchContributionsParity(t *testing.T) {
	t.Run("a_golden_no_build_host", func(t *testing.T) {
		fx := newK14Fixture(t)
		fake, _ := setupHermeticSessions(t)
		u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, false)
		if want := strings.ReplaceAll(k14GoldenLine, "<ROOT>", fx.root); u.line != want {
			t.Errorf("af up's launch line drifted from the golden:\n got:  %s\n want: %s", u.line, want)
		}
		k14AssertParity(t, u.line, s.line, r.line)
		for name, l := range map[string]string{"up": u.line, "sling": s.line, "respawn": r.line} {
			if strings.Contains(l, "AF_BUILD_") {
				t.Errorf("%s: no build-host.json, yet the line carries AF_BUILD_*: %s", name, l)
			}
			fx.exec(t, l)
		}
	})

	t.Run("b_build_host_on_every_path", func(t *testing.T) {
		fx := newK14Fixture(t)
		bh := &config.BuildHostConfig{Mode: "ssh", Host: "mac.lan", User: "builder", MountPath: "/Volumes/src"}
		if err := config.SaveBuildHostConfig(config.BuildHostConfigPath(fx.root), bh); err != nil {
			t.Fatal(err)
		}
		fake, _ := setupHermeticSessions(t)
		u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, false)
		k14AssertParity(t, u.line, s.line, r.line)
		want := map[string]string{"AF_BUILD_MODE": "ssh", "AF_BUILD_HOST": "mac.lan", "AF_BUILD_USER": "builder", "AF_HOST_MOUNT": "/Volumes/src"}
		for name, l := range map[string]string{"up": u.line, "sling": s.line, "respawn": r.line} {
			if l == "" {
				t.Errorf("%s: no line (err=%v)", name, map[string]k14Launch{"up": u, "sling": s, "respawn": r}[name].err)
				continue
			}
			run := fx.exec(t, l)
			for k, v := range want {
				if run.env[k] != v {
					t.Errorf("%s: the launched claude saw %s=%q, want %q", name, k, run.env[k], v)
				}
			}
		}
	})

	t.Run("c_invalid_build_host_same_warning", func(t *testing.T) {
		fx := newK14Fixture(t)
		path := config.BuildHostConfigPath(fx.root)
		if err := os.WriteFile(path, []byte(`{"mode":"bogus"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, loadErr := config.LoadBuildHostConfig(path)
		if loadErr == nil {
			t.Fatal("fixture: the bogus build-host.json must fail to load")
		}
		want := "warning: ignoring build-host.json (" + loadErr.Error() + "); launching manager without build-host env\n"
		fake, _ := setupHermeticSessions(t)
		u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, false)
		for name, l := range map[string]k14Launch{"up": u, "sling": s, "respawn": r} {
			if n := strings.Count(l.output, want); n != 1 {
				t.Errorf("%s: the build-host warning appears %d times, want exactly 1 (err=%v)\n%s", name, n, l.err, l.output)
			}
			if l.line == "" {
				t.Errorf("%s: nothing launched; an invalid build-host.json must warn and launch (err=%v)", name, l.err)
				continue
			}
			if strings.Contains(l.line, "AF_BUILD_") {
				t.Errorf("%s: the line carries AF_BUILD_* from an invalid file: %s", name, l.line)
			}
		}
		k14AssertParity(t, u.line, s.line, r.line)
	})

	t.Run("d_telemetry_attribution_uses_resolved_model", func(t *testing.T) {
		fx := newK14Fixture(t)
		writeTestJSON(t, config.AgentsConfigPath(fx.root), map[string]any{"agents": map[string]any{
			"manager": map[string]string{"type": "interactive", "description": "Factory coordinator", "model": "claude-legacy-4"},
		}})
		writeValidModels(t, fx.root, &config.ModelsConfig{
			Models: map[string]map[string]string{"hi": {"ANTHROPIC_MODEL": "claude-opus-5-5"}},
			Agents: map[string]string{"manager": "hi"},
		})
		if err := os.WriteFile(telemetryGateFile(fx.root), []byte("on\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fake, _ := setupHermeticSessions(t)
		u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, false)
		for name, l := range map[string]string{"up": u.line, "sling": s.line, "respawn": r.line} {
			if l == "" {
				t.Errorf("%s: no line", name)
				continue
			}
			run := fx.exec(t, l)
			m := k14ResAttrModel.FindStringSubmatch(run.env["OTEL_RESOURCE_ATTRIBUTES"])
			if m == nil || m[1] != "hi" {
				t.Errorf("%s: telemetry attributes the session to %v, want the resolved profile %q (attrs=%q)", name, m, "hi", run.env["OTEL_RESOURCE_ATTRIBUTES"])
			}
		}
		k14AssertParity(t, u.line, s.line, r.line)
	})

	t.Run("e_handoff_respawn_resolves_git_identity_in_agent_dir", func(t *testing.T) {
		fx := newK14Fixture(t)
		// The root carries no identity, so only a probe of the agent's own repo finds one.
		k14Git(t, fx.wtPath, "init", "-q")
		k14Git(t, fx.wtPath, "config", "user.name", "Worktree Local")
		k14Git(t, fx.wtPath, "config", "user.email", "local@wt.test")
		fake, _ := setupHermeticSessions(t)
		u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, true)
		for name, l := range map[string]string{"up": u.line, "sling": s.line, "respawn": r.line} {
			if l == "" {
				t.Errorf("%s: no line", name)
				continue
			}
			run := fx.exec(t, l)
			for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
				if v, ok := run.env[k]; ok {
					t.Errorf("%s: %s=%q exported over the worktree's repo-local identity (C-4)", name, k, v)
				}
			}
		}
	})

	t.Run("f_broken_models_json_carries_legacy_model", func(t *testing.T) {
		fx := newK14Fixture(t)
		writeTestJSON(t, config.AgentsConfigPath(fx.root), map[string]any{"agents": map[string]any{
			"manager": map[string]string{"type": "interactive", "description": "Factory coordinator", "model": "claude-legacy-4"},
		}})
		writeRawModels(t, fx.root, "{not json")
		fake, _ := setupHermeticSessions(t)
		u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, false)
		k14AssertParity(t, u.line, s.line, r.line)
		for name, l := range map[string]string{"up": u.line, "sling": s.line, "respawn": r.line} {
			if !strings.Contains(l, " --model 'claude-legacy-4'") {
				t.Errorf("%s: line lacks --model 'claude-legacy-4': %s", name, l)
				continue
			}
			run := fx.exec(t, l)
			if !strings.Contains(strings.Join(run.args, "\x00"), "--model\x00claude-legacy-4") {
				t.Errorf("%s: the stub claude got args %q", name, run.args)
			}
		}
	})

	t.Run("g_start_writes_only_the_identity_quintet", func(t *testing.T) {
		fx := newK14Fixture(t)
		fake, _ := setupHermeticSessions(t)
		sess := session.SessionName("manager")
		want := []string{"AF_ACTOR", "AF_ROLE", "AF_ROOT", "AF_WORKTREE", "AF_WORKTREE_ID"}
		for name, l := range map[string]k14Launch{"up": fx.up(t, fake), "sling": fx.sling(t, fake, "")} {
			set, unset := k14SessionOps(l.ops, sess)
			var keys []string
			for _, kv := range set {
				k, _, _ := strings.Cut(kv, "=")
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if strings.Join(keys, " ") != strings.Join(want, " ") {
				t.Errorf("%s: Start() set tmux keys %v, want exactly %v", name, keys, want)
			}
			if len(unset) != 0 {
				t.Errorf("%s: Start() wrote %d UnsetEnvironment ops, want 0: %v", name, len(unset), unset)
			}
		}
	})

	t.Run("nil_contributions_refused", func(t *testing.T) {
		fx := newK14Fixture(t)
		fake, _ := setupHermeticSessions(t)

		bare := session.NewManager(fx.root, "manager", fx.entry(t))
		if err := bare.Start(); !errors.Is(err, session.ErrWorktreeNotSet) {
			t.Errorf("Start() with neither a worktree nor contributions = %v, want ErrWorktreeNotSet", err)
		}

		mgr := session.NewManager(fx.root, "manager", fx.entry(t))
		if err := mgr.SetWorktree(fx.wtPath, fx.wtID); err != nil {
			t.Fatal(err)
		}
		start := len(fake.ops)
		if err := mgr.Start(); !errors.Is(err, session.ErrLaunchContributionsMissing) {
			t.Errorf("Start() without contributions = %v, want ErrLaunchContributionsMissing", err)
		}
		if line, err := mgr.BuildStartupCommand(); !errors.Is(err, session.ErrLaunchContributionsMissing) || line != "" {
			t.Errorf("BuildStartupCommand() without contributions = (%q, %v), want (\"\", ErrLaunchContributionsMissing)", line, err)
		}
		if ops := fake.ops[start:]; len(ops) != 0 {
			t.Errorf("a refused launch still reached tmux: %v", ops)
		}
	})
}

// TestLaunchContributionsComposedAtEveryLaunchSite holds the composer's reason to exist: a launch site that
// hands a Manager contributions it built itself is the per-site wiring #715 drifted on. A source read,
// because the claim is a universal over call sites that no single launch can witness.
//
// The text clauses alone admit `var c session.LaunchContributions` and `new(session.LaunchContributions)`
// in a file that also calls the composer elsewhere, so every call is also checked in the AST: its argument
// must be &x, with x assigned from launchContributions in the same function.
func TestLaunchContributionsComposedAtEveryLaunchSite(t *testing.T) {
	mod := findModuleRoot(t)
	const composer = "internal/cmd/launch_contributions.go"
	var setterFiles []string
	sites := 0
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(mod, top), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(mod, path)
			rel = filepath.ToSlash(rel)
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(body)
			if rel != composer && strings.Contains(src, "LaunchContributions{") {
				t.Errorf("%s builds a LaunchContributions literal; only %s may compose one", rel, composer)
			}
			if strings.Contains(src, ".SetLaunchContributions(") {
				setterFiles = append(setterFiles, rel)
				if !strings.Contains(src, "launchContributions(") {
					t.Errorf("%s hands a Manager contributions without calling launchContributions(", rel)
				}
			}
			sites += k14ComposedSetterCalls(t, rel, body)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(setterFiles) == 0 || sites == 0 {
		t.Fatal("no production file calls .SetLaunchContributions(; the interlock is scanning the wrong tree")
	}
}

// k14ComposedSetterCalls counts the SetLaunchContributions calls in one file and fails each whose argument
// is not &x for an x the same function assigned from launchContributions.
func k14ComposedSetterCalls(t *testing.T, rel string, src []byte) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		t.Errorf("%s: %v", rel, err)
		return 0
	}
	sites := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		composed := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 || len(as.Lhs) == 0 {
				return true
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "launchContributions" {
				if lhs, ok := as.Lhs[0].(*ast.Ident); ok {
					composed[lhs.Name] = true
				}
			}
			return true
		})
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "SetLaunchContributions" {
				return true
			}
			sites++
			if len(call.Args) == 1 {
				if u, ok := call.Args[0].(*ast.UnaryExpr); ok && u.Op == token.AND {
					if id, ok := u.X.(*ast.Ident); ok && composed[id.Name] {
						return true
					}
				}
			}
			t.Errorf("%s: %s hands a Manager contributions that did not come from launchContributions in the same function",
				fset.Position(call.Pos()), fn.Name.Name)
			return true
		})
	}
	return sites
}

func TestLaunchAgentSession_CLIModelWritesResolvedOverrideMarker(t *testing.T) {
	models := &config.ModelsConfig{Models: map[string]map[string]string{"hi": {"ANTHROPIC_MODEL": "claude-opus-5-5"}}}

	fx := newK14Fixture(t)
	writeValidModels(t, fx.root, models)
	fake, _ := setupHermeticSessions(t)
	if l := fx.sling(t, fake, "hi"); l.err != nil || l.line == "" {
		t.Fatalf("sling --model hi did not launch: err=%v\n%s", l.err, l.output)
	}
	if got := readModelOverride(fx.agentDir); got != "hi" {
		t.Errorf("sling --model hi left marker %q in the agent dir, want %q", got, "hi")
	}

	fx = newK14Fixture(t)
	writeValidModels(t, fx.root, models)
	fake, _ = setupHermeticSessions(t)
	if l := fx.sling(t, fake, ""); l.err != nil || l.line == "" {
		t.Fatalf("sling without --model did not launch: err=%v\n%s", l.err, l.output)
	}
	if got := readModelOverride(fx.agentDir); got != "" {
		t.Errorf("sling without --model wrote marker %q; only an explicit --model may persist", got)
	}
}
