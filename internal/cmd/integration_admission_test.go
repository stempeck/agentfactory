package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

const admAgent = "worker"

type admEnv struct {
	*intBEnv
	agentDir string
	store    *memstore.Store
}

// admFactory is an exec-capable factory with one specialist agent, a hermetic memstore and fake tmux, so
// instantiateFormulaWorkflow can run end to end and a [check] script can execute.
func admFactory(t *testing.T) *admEnv {
	t.Helper()
	e := intBFactory(t)
	origVer := claudeCodeVersionFn
	t.Cleanup(func() { claudeCodeVersionFn = origVer })
	claudeCodeVersionFn = func() string { return "9.9.9 (Claude Code)" }
	origPID := servicePanePIDFn
	t.Cleanup(func() { servicePanePIDFn = origPID })
	servicePanePIDFn = func(string) (int, error) { return os.Getpid(), nil }

	agents := `{"agents":{` +
		`"manager":{"type":"interactive","description":"Interactive agent"},` +
		`"supervisor":{"type":"autonomous","description":"Autonomous agent"},` +
		`"` + admAgent + `":{"type":"autonomous","description":"specialist"}}}`
	if err := os.WriteFile(config.AgentsConfigPath(e.root), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.FormulasDir(e.root), 0o755); err != nil {
		t.Fatal(err)
	}
	agentDir := config.AgentDir(e.root, admAgent)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.ext, 0o755); err != nil {
		t.Fatal(err)
	}
	initTestGitRepo(t, e.root)
	t.Setenv("AF_WORKTREE", e.root)
	t.Setenv("AF_WORKTREE_ID", "wt-test00")
	return &admEnv{intBEnv: e, agentDir: agentDir, store: installMemStore(t)}
}

func admTOMLList(key string, names []string) string {
	if names == nil {
		return ""
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = `"` + n + `"`
	}
	return key + " = [" + strings.Join(q, ", ") + "]\n"
}

func admWriteFormula(t *testing.T, a *admEnv, name string, required, optional, skills []string) {
	t.Helper()
	body := "formula = \"" + name + "\"\ntype = \"workflow\"\nversion = 1\n" +
		admTOMLList("integrations", required) +
		admTOMLList("integrations_optional", optional) +
		admTOMLList("skills", skills) +
		"\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
	path := filepath.Join(config.FormulasDir(a.root), name+".formula.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func admInstantiate(t *testing.T, a *admEnv, formulaName string, withCmd bool) (string, error) {
	t.Helper()
	params := InstantiateParams{
		Ctx:            t.Context(),
		FormulaName:    formulaName,
		AgentName:      admAgent,
		Root:           a.root,
		WorkDir:        a.agentDir,
		CallerIdentity: "manager",
	}
	if withCmd {
		c := &cobra.Command{}
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		params.Cmd = c
	}
	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)
	return buf.String(), err
}

// admBeadCount counts non-mail beads: router mail lands in the same memstore.
func admBeadCount(t *testing.T, a *admEnv) int {
	t.Helper()
	all, err := a.store.List(t.Context(), issuestore.Filter{IncludeAllAgents: true, IncludeClosed: true})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	n := 0
	for _, iss := range all {
		mail := false
		for _, l := range iss.Labels {
			if l == "mail:true" {
				mail = true
			}
		}
		if !mail {
			n++
		}
	}
	return n
}

func admPinPath(agentDir string) string {
	return filepath.Join(agentDir, ".runtime", "integration_bindings")
}

func admReadPin(t *testing.T, agentDir string) (map[string]json.RawMessage, bool) {
	t.Helper()
	b, err := os.ReadFile(admPinPath(agentDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("read pin: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("pin is not a JSON object: %v\n%s", err, b)
	}
	return m, true
}

func admWantNoSideEffects(t *testing.T, a *admEnv) {
	t.Helper()
	if n := admBeadCount(t, a); n != 0 {
		t.Errorf("refused instantiation left %d non-mail beads, want 0", n)
	}
	if intBExists(filepath.Join(a.agentDir, ".runtime", "hooked_formula")) {
		t.Error("refused instantiation wrote .runtime/hooked_formula")
	}
	if intBExists(admPinPath(a.agentDir)) {
		t.Error("refused instantiation wrote .runtime/integration_bindings")
	}
}

func admWantRefusalClass(t *testing.T, err error, class string) {
	t.Helper()
	var r *integrationRefusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want an *integrationRefusal of class %q", err, class)
	}
	if r.Class != class {
		t.Errorf("refusal class = %q, want %q (err: %v)", r.Class, class, err)
	}
}

// admRecordHealthy records an installed integration with no [check] whose service session is up.
func admRecordHealthy(t *testing.T, a *admEnv, name string, mutate func(*config.PluginEntry)) string {
	t.Helper()
	snap := intBRecord(t, a.intBEnv, name, intBSource(a.intBEnv, name, intBManifestOpts{noCheck: true}), mutate)
	a.fake.present[name+"-svc"] = true
	return snap
}

func admCounterScript(counter, stdout string, exit string) string {
	return "#!/bin/sh\nprintf x >> '" + counter + "'\n" + stdout + "exit " + exit + "\n"
}

func admCount(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func TestAdmission_RequiredMissingRefusedBeforeAnyBead(t *testing.T) {
	a := admFactory(t)
	admWriteFormula(t, a, "needs-aws", []string{"aws-core"}, nil, nil)

	t.Run("refused before any bead", func(t *testing.T) {
		_, err := admInstantiate(t, a, "needs-aws", true)
		if err == nil {
			t.Fatal("instantiateFormulaWorkflow succeeded with required integration aws-core not installed, want a refusal")
		}
		for _, want := range []string{
			`1 integration required by formula "needs-aws" not installed: aws-core`,
			"af plugin install aws-core",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to contain %q", err, want)
			}
		}
		admWantRefusalClass(t, err, refusalIntegrationMissing)
		admWantNoSideEffects(t, a)
	})

	t.Run("recorded then admitted without reset", func(t *testing.T) {
		if id := readHookedFormulaID(a.agentDir); id != "" {
			t.Fatalf("the refusal left hooked_formula %q behind; succession would demand --reset", id)
		}
		admRecordHealthy(t, a, "aws-core", nil)
		out, err := admInstantiate(t, a, "needs-aws", true)
		if err != nil {
			t.Fatalf("instantiate after recording aws-core: %v\n%s", err, out)
		}
		if n := admBeadCount(t, a); n == 0 {
			t.Error("admitted instantiation created no beads")
		}
		pin, ok := admReadPin(t, a.agentDir)
		if !ok {
			t.Fatal("admitted instantiation wrote no .runtime/integration_bindings pin")
		}
		if !strings.Contains(string(pin["bindings"]), `"aws-core"`) {
			t.Errorf("pin bindings = %s, want aws-core bound", pin["bindings"])
		}
	})

	t.Run("nil cmd is a no-op when nothing is declared", func(t *testing.T) {
		if err := os.RemoveAll(filepath.Join(a.agentDir, ".runtime")); err != nil {
			t.Fatal(err)
		}
		admWriteFormula(t, a, "plain", nil, nil, nil)
		if out, err := admInstantiate(t, a, "plain", false); err != nil {
			t.Fatalf("instantiate without Cmd and without integrations: %v\n%s", err, out)
		}
		if intBExists(admPinPath(a.agentDir)) {
			t.Error("a formula declaring no integration wrote a pin (D39)")
		}
	})
}

var admCheckFailedAt = regexp.MustCompile(`integration "acme-int" check failed at \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z: `)

func TestAdmission_RequiredCheckFailsTwiceRefused(t *testing.T) {
	a := admFactory(t)
	counter := filepath.Join(a.ext, "check.count")
	files := intBSource(a.intBEnv, "acme-int", intBManifestOpts{})
	files["af/check.sh"] = intBFile{admCounterScript(counter, "echo 'upstream: boom' >&2\n", "3"), 0o755}
	intBRecord(t, a.intBEnv, "acme-int", files, nil)
	a.fake.present["acme-int-svc"] = true
	admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)

	_, err := admInstantiate(t, a, "needs-acme", true)
	if err == nil {
		t.Fatalf("instantiateFormulaWorkflow succeeded with a failing required [check], want a refusal (check ran %d times)", admCount(t, counter))
	}
	if got := admCount(t, counter); got != 2 {
		t.Errorf("[check] ran %d times, want 2 (one retry)", got)
	}
	if !admCheckFailedAt.MatchString(err.Error()) {
		t.Errorf("err = %q, want the design L348 text %q", err, admCheckFailedAt)
	}
	if !strings.Contains(err.Error(), "upstream: boom") {
		t.Errorf("err = %q, want the upstream output verbatim", err)
	}
	admWantRefusalClass(t, err, refusalIntegrationCheckFailed)
	admWantNoSideEffects(t, a)
	rec := intBReadCheckRecord(t, a.root, "acme-int")
	if got := intBJSONString(t, rec["state"]); got != "fail" {
		t.Errorf("check record state = %q, want fail (admission writes the record)", got)
	}
}

func TestAdmission_OptionalMissingSkipped(t *testing.T) {
	a := admFactory(t)
	admWriteFormula(t, a, "maybe-claw", nil, []string{"defenseclaw"}, nil)

	out, err := admInstantiate(t, a, "maybe-claw", true)
	if err != nil {
		t.Fatalf("an unrecorded optional integration refused instantiation: %v\n%s", err, out)
	}
	if n := admBeadCount(t, a); n == 0 {
		t.Error("instantiation created no beads")
	}
	if !strings.Contains(out, "INTEGRATION_SKIPPED defenseclaw: ") {
		t.Errorf("output = %q, want an INTEGRATION_SKIPPED defenseclaw report", out)
	}
	pin, ok := admReadPin(t, a.agentDir)
	if !ok {
		t.Fatal("no .runtime/integration_bindings pin: the skipped optional must reach prime through the pin")
	}
	if string(pin["v"]) != "1" {
		t.Errorf("pin v = %s, want 1", pin["v"])
	}
	if string(pin["formula"]) != `"maybe-claw"` {
		t.Errorf("pin formula = %s, want \"maybe-claw\"", pin["formula"])
	}
	if strings.TrimSpace(string(pin["bindings"])) != "[]" {
		t.Errorf("pin bindings = %s, want []", pin["bindings"])
	}
	var skipped []struct{ Name, Reason string }
	if err := json.Unmarshal(pin["skipped"], &skipped); err != nil {
		t.Fatalf("pin skipped: %v (%s)", err, pin["skipped"])
	}
	if len(skipped) != 1 || skipped[0].Name != "defenseclaw" || skipped[0].Reason == "" {
		t.Errorf("pin skipped = %+v, want one {defenseclaw, <reason>}", skipped)
	}
}

func TestAdmission_NamespacedSkillResolvesUnderBoundDir(t *testing.T) {
	t.Run("bound plugin namespace resolves", func(t *testing.T) {
		a := admFactory(t)
		admRecordHealthy(t, a, "acme-int", nil)
		admWriteFormula(t, a, "ns-skill", []string{"acme-int"}, nil, []string{"acme-int-plugin:acme-int"})
		if out, err := admInstantiate(t, a, "ns-skill", true); err != nil {
			t.Fatalf("namespaced skill under a bound plugin dir refused: %v\n%s", err, out)
		}
	})
	t.Run("unbound namespace refused before any bead", func(t *testing.T) {
		a := admFactory(t)
		admRecordHealthy(t, a, "acme-int", nil)
		admWriteFormula(t, a, "ns-other", []string{"acme-int"}, nil, []string{"other-plugin:acme-int"})
		_, err := admInstantiate(t, a, "ns-other", true)
		admWantRefusalClass(t, err, refusalNamespacedSkillUnresolved)
		admWantNoSideEffects(t, a)
	})
	t.Run("integration name is not a namespace when it differs from the plugin name", func(t *testing.T) {
		a := admFactory(t)
		admRecordHealthy(t, a, "acme-int", nil)
		admWriteFormula(t, a, "ns-intname", []string{"acme-int"}, nil, []string{"acme-int:acme-int"})
		_, err := admInstantiate(t, a, "ns-intname", true)
		admWantRefusalClass(t, err, refusalNamespacedSkillUnresolved)
	})
	t.Run("integration name is a namespace when it equals the plugin name", func(t *testing.T) {
		a := admFactory(t)
		files := intBSource(a.intBEnv, "acme-int", intBManifestOpts{noCheck: true})
		files["claude-plugin/.claude-plugin/plugin.json"] = intBFile{`{"name":"acme-int","version":"0.1.0"}` + "\n", 0o644}
		intBRecord(t, a.intBEnv, "acme-int", files, func(e *config.PluginEntry) {
			e.Integration.ClaudePlugins = []string{"acme-int"}
		})
		a.fake.present["acme-int-svc"] = true
		admWriteFormula(t, a, "ns-same", []string{"acme-int"}, nil, []string{"acme-int:acme-int"})
		if out, err := admInstantiate(t, a, "ns-same", true); err != nil {
			t.Fatalf("namespace equal to the plugin name refused: %v\n%s", err, out)
		}
	})
	t.Run("skipped optional namespace is reported not refused", func(t *testing.T) {
		a := admFactory(t)
		admRecordHealthy(t, a, "acme-int", func(e *config.PluginEntry) {
			e.Integration.ContentSHA256 = strings.Repeat("0", 64)
		})
		admWriteFormula(t, a, "ns-optional", nil, []string{"acme-int"}, []string{"acme-int-plugin:acme-int"})
		out, err := admInstantiate(t, a, "ns-optional", true)
		if err != nil {
			t.Fatalf("namespaced skill of a skipped optional integration refused: %v\n%s", err, out)
		}
		if !strings.Contains(out, "INTEGRATION_SKIPPED acme-int: ") {
			t.Errorf("output = %q, want INTEGRATION_SKIPPED acme-int (content drifted)", out)
		}
		if !strings.Contains(out, "acme-int-plugin:acme-int") {
			t.Errorf("output = %q, want the unresolved skill named in a report", out)
		}
	})
	t.Run("unnamespaced skill keeps the SKILL.md stat", func(t *testing.T) {
		a := admFactory(t)
		admWriteFormula(t, a, "local-skill", nil, nil, []string{"local"})
		_, err := admInstantiate(t, a, "local-skill", true)
		if err == nil || !strings.Contains(err.Error(), "no SKILL.md") {
			t.Fatalf("err = %v, want today's missing SKILL.md refusal (D13)", err)
		}
	})
}

// admWriteCheckRecord plants an ok check record as runIntegrationCheck would, plus the D1 content hash.
func admWriteCheckRecord(t *testing.T, root, name, contentSHA string, at time.Time) {
	t.Helper()
	admWriteCheckRecordState(t, root, name, integrationCheckOK, contentSHA, at)
}

func admWriteCheckRecordState(t *testing.T, root, name, state, contentSHA string, at time.Time) {
	t.Helper()
	exit, output := 0, "check-ok"
	if state != integrationCheckOK {
		exit, output = 1, "check-broken"
	}
	rec := map[string]any{
		"v": 1, "plugin": name, "state": state, "exit": exit, "at": at.UTC().Format(time.RFC3339),
		"duration_ms": 1, "output": output, "hook_fail_mode": "open", "service_rss_kb": 0,
		"claude_code_version": "9.9.9 (Claude Code)", "content_sha256": contentSHA,
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	p := integrationCheckRecordPath(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAdmission_CheckRecordHashMismatchReruns(t *testing.T) {
	setup := func(t *testing.T) (*admEnv, string, string) {
		a := admFactory(t)
		counter := filepath.Join(a.ext, "check.count")
		files := intBSource(a.intBEnv, "acme-int", intBManifestOpts{})
		files["af/check.sh"] = intBFile{admCounterScript(counter, "echo check-ok\n", "0"), 0o755}
		m := files[config.IntegrationManifestFile]
		m.body = strings.Replace(m.body, "[check]\nrun = \"af/check.sh\"\n", "[check]\nrun = \"af/check.sh\"\nfresh_for = \"1h\"\n", 1)
		files[config.IntegrationManifestFile] = m
		intBRecord(t, a.intBEnv, "acme-int", files, nil)
		a.fake.present["acme-int-svc"] = true
		admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
		return a, counter, intBLoadEntry(t, a.root, "acme-int").Integration.ContentSHA256
	}

	t.Run("fresh ok record with a different hash re-runs the check", func(t *testing.T) {
		a, counter, _ := setup(t)
		admWriteCheckRecord(t, a.root, "acme-int", strings.Repeat("f", 64), time.Now())
		if out, err := admInstantiate(t, a, "needs-acme", true); err != nil {
			t.Fatalf("instantiate: %v\n%s", err, out)
		}
		if got := admCount(t, counter); got != 1 {
			t.Errorf("[check] ran %d times, want 1: an ok record whose content_sha256 differs from the entry is stale (D1)", got)
		}
	})
	t.Run("fresh ok record with the matching hash is reused", func(t *testing.T) {
		a, counter, sha := setup(t)
		admWriteCheckRecord(t, a.root, "acme-int", sha, time.Now())
		if out, err := admInstantiate(t, a, "needs-acme", true); err != nil {
			t.Fatalf("instantiate: %v\n%s", err, out)
		}
		if got := admCount(t, counter); got != 0 {
			t.Errorf("[check] ran %d times, want 0: the record is ok, fresh and hash-matching", got)
		}
	})
}

func TestAdmission_RequiredServiceDownRefused(t *testing.T) {
	t.Run("required", func(t *testing.T) {
		a := admFactory(t)
		intBRecord(t, a.intBEnv, "acme-int", intBSource(a.intBEnv, "acme-int", intBManifestOpts{noCheck: true}), nil)
		admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
		_, err := admInstantiate(t, a, "needs-acme", true)
		if err == nil {
			t.Fatal("instantiateFormulaWorkflow succeeded with the required service session absent after the ensure, want a refusal (D2)")
		}
		admWantRefusalClass(t, err, refusalIntegrationServiceDown)
		admWantNoSideEffects(t, a)
	})
	t.Run("optional", func(t *testing.T) {
		a := admFactory(t)
		intBRecord(t, a.intBEnv, "acme-int", intBSource(a.intBEnv, "acme-int", intBManifestOpts{noCheck: true}), nil)
		admWriteFormula(t, a, "maybe-acme", nil, []string{"acme-int"}, nil)
		out, err := admInstantiate(t, a, "maybe-acme", true)
		if err != nil {
			t.Fatalf("optional integration with its service down refused: %v\n%s", err, out)
		}
		if !strings.Contains(out, "INTEGRATION_SKIPPED acme-int: ") {
			t.Errorf("output = %q, want INTEGRATION_SKIPPED acme-int", out)
		}
	})
}

func TestAdmission_FactoryScopeBindingPinnedAndGuarded(t *testing.T) {
	a := admFactory(t)
	recs := installIntegrationReportRecorder(t)
	snap := intBRecord(t, a.intBEnv, "fleet-int", intBSource(a.intBEnv, "fleet-int", intBManifestOpts{noCheck: true, scope: "factory"}), func(p *config.PluginEntry) {
		p.Integration.Scope = config.IntegrationScopeFactory
	})
	a.fake.present["fleet-int-svc"] = true
	admWriteFormula(t, a, "plain", nil, nil, nil)

	if out, err := admInstantiate(t, a, "plain", true); err != nil {
		t.Fatalf("instantiate: %v\n%s", err, out)
	}
	pin, ok := pinFixtureRead(t, a.agentDir)
	if !ok {
		t.Fatal("no pin: factory-scope fleet-int binds this session, and the pin is the full bound set (IR:L407, L436)")
	}
	pinWantBinds(t, a.root, pin, "fleet-int", snap)

	t.Chdir(a.agentDir)
	out, err := invokeGuardEvent(t, "permission", guardPayload("PermissionRequest", "mcp__plugin_x_srv__run", a.agentDir))
	if err != nil {
		t.Fatalf("guard-event: %v", err)
	}
	if d := decodeGuardDecision(t, out); d.HookSpecificOutput.Decision.Behavior != "deny" {
		t.Errorf("guard decision = %+v, want a deny in a session bound only to a factory-scope integration", d)
	}
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_GUARD_DENIED ")); n != 1 {
		t.Errorf("INTEGRATION_GUARD_DENIED mails = %d, want 1", n)
	}
}

func TestAdmission_OptionalSkippedMailedOnce(t *testing.T) {
	a := admFactory(t)
	recs := installIntegrationReportRecorder(t)
	admWriteFormula(t, a, "maybe-claw", nil, []string{"defenseclaw"}, nil)
	f, err := formula.ParseFile(filepath.Join(config.FormulasDir(a.root), "maybe-claw.formula.toml"))
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if _, _, err := admitFormulaIntegrations(t.Context(), nil, a.root, f); err != nil {
			t.Fatalf("admission %d: %v", i+1, err)
		}
	}
	skipped := reportsWithPrefix(*recs, "INTEGRATION_SKIPPED defenseclaw")
	if len(skipped) != 1 {
		t.Fatalf("INTEGRATION_SKIPPED defenseclaw mails = %d across two admissions, want 1 (IR:L592); all=%+v", len(skipped), *recs)
	}
	if skipped[0].to != "manager" || !strings.Contains(skipped[0].body, "not installed") {
		t.Errorf("skipped mail = %+v, want to=manager with the reason", skipped[0])
	}

	admRecordHealthy(t, a, "defenseclaw", nil)
	if bound, _, err := admitFormulaIntegrations(t.Context(), nil, a.root, f); err != nil || len(bound) != 1 {
		t.Fatalf("admission with defenseclaw installed: bound=%d err=%v", len(bound), err)
	}
	if intBExists(integrationReportMarker(a.root, "defenseclaw", integrationReportSkipped)) {
		t.Error("the skipped condition was not re-armed once defenseclaw bound")
	}
}

func TestAdmission_PinWriteFailureLeavesNoHookedFormula(t *testing.T) {
	a := admFactory(t)
	admRecordHealthy(t, a, "acme-int", nil)
	admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
	blocker := filepath.Join(admPinPath(a.agentDir), "occupied")
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := admInstantiate(t, a, "needs-acme", true)
	if err == nil || !strings.Contains(err.Error(), "integration pin") {
		t.Fatalf("err = %v, want the pin write failure\n%s", err, out)
	}
	if intBExists(filepath.Join(a.agentDir, ".runtime", "hooked_formula")) {
		t.Error("hooked_formula was written for an instance whose pin could not be: af prime would run it unguarded")
	}
}

// The pin is written before the first bead (D72): a pin that cannot be written refuses the instantiation
// before any step bead exists, so nothing is left for succession to trip over.
func TestAdmission_PinWriteFailureCreatesNoBead(t *testing.T) {
	a := admFactory(t)
	admRecordHealthy(t, a, "acme-int", nil)
	admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
	blocker := filepath.Join(admPinPath(a.agentDir), "occupied")
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := admInstantiate(t, a, "needs-acme", true)
	if err == nil || !strings.Contains(err.Error(), "integration pin") {
		t.Fatalf("err = %v, want the pin write failure\n%s", err, out)
	}
	if n := admBeadCount(t, a); n != 0 {
		t.Errorf("a pin write failure left %d bead(s) behind; the pin must be written before the first bead", n)
	}

	if err := os.RemoveAll(admPinPath(a.agentDir)); err != nil {
		t.Fatal(err)
	}
	if out, err := admInstantiate(t, a, "needs-acme", true); err != nil {
		t.Fatalf("once the pin path is writable the next instantiation must succeed without --reset: %v\n%s", err, out)
	}
}

// A pin with no instance behind it turns the guard on for a session that runs no formula, so an
// instantiation that fails after admission pinned must take its pin with it.
func TestAdmission_InstantiationFailureAfterPinLeavesNoPin(t *testing.T) {
	a := admFactory(t)
	admRecordHealthy(t, a, "acme-int", nil)
	body := "formula = \"needs-acme-var\"\ntype = \"workflow\"\nversion = 1\nintegrations = [\"acme-int\"]\n" +
		"\n[vars.issue]\nrequired = true\nsource = \"cli\"\n" +
		"\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
	if err := os.WriteFile(filepath.Join(config.FormulasDir(a.root), "needs-acme-var.formula.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := admInstantiate(t, a, "needs-acme-var", true)
	if err == nil || !strings.Contains(err.Error(), "resolving variables") {
		t.Fatalf("err = %v, want the missing required var to fail after admission\n%s", err, out)
	}
	if _, ok := admReadPin(t, a.agentDir); ok {
		t.Error("the pin outlived a failed instantiation: the guard would switch on for a session with no instance")
	}
	if intBExists(filepath.Join(a.agentDir, ".runtime", "hooked_formula")) {
		t.Error("hooked_formula was written for a failed instantiation")
	}
}
