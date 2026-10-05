package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
)

const refusalStdout = slingRefusedMarker + " class=" + refusalIntegrationMissing + " integration=aws-core\n"

// refusalPastBackoff is older than any cronRetryBackoff window, which is capped at an hour.
const refusalPastBackoff = 61 * time.Minute

func refusalCfg(retryAfterSecs int) *config.DispatchConfig {
	return &config.DispatchConfig{
		Repos:            []string{"owner/repo"},
		TriggerLabel:     "agentic",
		NotifyOnComplete: "manager",
		IntervalSecs:     300,
		RetryAfterSecs:   retryAfterSecs,
		Mappings:         []config.DispatchMapping{{Labels: []string{"bug"}, Source: "issue", Agent: "impl"}},
	}
}

func refusalItem() ghItem {
	return ghItem{Number: 9, URL: "https://github.com/owner/repo/issues/9", Labels: labels("agentic", "bug")}
}

// installRefusingSling makes every dispatchItem call report an admission refusal the way sling prints it.
func installRefusingSling(t *testing.T) *int {
	t.Helper()
	calls := 0
	orig := dispatchItem
	dispatchItem = func(root, agent, itemURL, caller, model string) (string, error) {
		calls++
		return refusalStdout, errors.New("exit status 1")
	}
	t.Cleanup(func() { dispatchItem = orig })
	return &calls
}

func refusalTick(t *testing.T, fake *fakeTmux, root string, state *dispatchState, cfg *config.DispatchConfig) *dispatchCycleStats {
	t.Helper()
	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	dispatchNonWorkflowItem(cmd, root, fake, state, stats, cfg, "owner/repo", refusalItem(), "issue", "impl", "")
	return stats
}

func refusalSlingCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	c := &cobra.Command{}
	c.SetContext(t.Context())
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	return c, &buf
}

func setSlingFlag[T any](t *testing.T, p *T, v T) {
	t.Helper()
	orig := *p
	*p = v
	t.Cleanup(func() { *p = orig })
}

func mailTitled(t *testing.T, store issuestore.Store, title string) int {
	t.Helper()
	all, err := store.List(t.Context(), issuestore.Filter{IncludeAllAgents: true, IncludeClosed: true})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	n := 0
	for _, iss := range all {
		if iss.Title == title {
			n++
		}
	}
	return n
}

func TestDispatchRefusal_RecordedWithBackoffOneMail(t *testing.T) {
	t.Run("dispatcher records the refusal and backs off", func(t *testing.T) {
		fake, _ := setupHermeticSessions(t)
		root := t.TempDir()
		cfg := refusalCfg(1800)
		calls := installRefusingSling(t)
		state := dispatchState{Dispatched: map[string]dispatchEntry{}}
		const key = "owner/repo#9"

		stats := refusalTick(t, fake, root, &state, cfg)
		if *calls != 1 {
			t.Fatalf("tick 1: dispatchItem calls = %d, want 1", *calls)
		}
		entry, ok := state.Dispatched[key]
		if !ok {
			t.Fatal("tick 1: a refused sling left no dispatch record; the next tick re-slings at tick speed")
		}
		if entry.RefusalClass != refusalIntegrationMissing || entry.RefusedAt.IsZero() || entry.ConsecutiveRefusals != 1 {
			t.Errorf("tick 1 record = %+v, want RefusalClass=%q, RefusedAt set, ConsecutiveRefusals=1", entry, refusalIntegrationMissing)
		}
		if stats.errors != 1 || stats.dispatched != 0 {
			t.Errorf("tick 1 stats = %+v, want errors=1 dispatched=0", *stats)
		}

		refusalTick(t, fake, root, &state, cfg)
		if *calls != 1 {
			t.Errorf("tick 2 inside the backoff: dispatchItem calls = %d, want still 1", *calls)
		}

		entry = state.Dispatched[key]
		entry.RefusedAt = entry.RefusedAt.Add(-refusalPastBackoff)
		state.Dispatched[key] = entry
		refusalTick(t, fake, root, &state, cfg)
		if *calls != 2 {
			t.Errorf("tick 3 past the backoff: dispatchItem calls = %d, want 2", *calls)
		}
		if got := state.Dispatched[key].ConsecutiveRefusals; got != 2 {
			t.Errorf("tick 3: ConsecutiveRefusals = %d, want 2", got)
		}
	})

	t.Run("sling mails the refusal once per item and condition", func(t *testing.T) {
		a := admFactory(t)
		admWriteFormula(t, a, "needs-aws", []string{"aws-core"}, nil, nil)
		writeRefusalAgents(t, a.root, "needs-aws")
		recs := installIntegrationReportRecorder(t)
		setSlingFlag(t, &slingNoLaunch, true)
		setSlingFlag(t, &slingCaller, "manager")
		const task = "https://github.com/owner/repo/issues/9"

		var outs []string
		for i := 0; i < 2; i++ {
			c, buf := refusalSlingCmd(t)
			err := dispatchToSpecialist(c, a.root, a.root, admAgent, task)
			if err == nil {
				t.Fatalf("dispatch %d succeeded with aws-core not installed, want a refusal", i+1)
			}
			outs = append(outs, buf.String())
		}
		for i, out := range outs {
			if !strings.Contains(out, slingRefusedMarker+" class="+refusalIntegrationMissing+" integration=aws-core") {
				t.Errorf("dispatch %d stdout lacks the refusal marker:\n%s", i+1, out)
			}
		}
		refused := reportsWithPrefix(*recs, "INTEGRATION_REFUSED ")
		if len(refused) != 1 {
			t.Fatalf("INTEGRATION_REFUSED mails = %d, want exactly 1 across two identical refusals; all=%+v", len(refused), *recs)
		}
		if got := refused[0]; got.to != "manager" || !strings.Contains(got.subject, refusalIntegrationMissing) || !strings.Contains(got.subject, admAgent) {
			t.Errorf("refusal mail = %+v, want to=manager and a subject naming %q and %q", got, refusalIntegrationMissing, admAgent)
		}
		if n := mailTitled(t, a.store, "SKILL_MISSING: "+admAgent); n != 0 {
			t.Errorf("an admission refusal also sent %d SKILL_MISSING mail(s); the refusal is class-specific", n)
		}
	})

	t.Run("the refusal mail replaces SKILL_MISSING for the same recipient", func(t *testing.T) {
		for _, tc := range []struct{ caller, wantTo string }{{"supervisor", "supervisor"}, {"@cli", ""}} {
			t.Run(tc.caller, func(t *testing.T) {
				a := admFactory(t)
				admWriteFormula(t, a, "needs-aws", []string{"aws-core"}, nil, nil)
				writeRefusalAgents(t, a.root, "needs-aws")
				recs := installIntegrationReportRecorder(t)
				setSlingFlag(t, &slingNoLaunch, true)
				setSlingFlag(t, &slingCaller, tc.caller)

				c, buf := refusalSlingCmd(t)
				if err := dispatchToSpecialist(c, a.root, a.root, admAgent, "https://github.com/owner/repo/issues/9"); err == nil {
					t.Fatalf("caller %s: dispatch succeeded with aws-core not installed\n%s", tc.caller, buf)
				}
				refused := reportsWithPrefix(*recs, "INTEGRATION_REFUSED ")
				switch {
				case tc.wantTo == "" && len(refused) != 0:
					t.Errorf("caller %s: refusal mails = %+v, want none (SKILL_MISSING never mails @cli, IR:L639-640)", tc.caller, refused)
				case tc.wantTo != "" && (len(refused) != 1 || refused[0].to != tc.wantTo):
					t.Errorf("caller %s: refusal mails = %+v, want one to %s", tc.caller, refused, tc.wantTo)
				}
			})
		}
	})

	t.Run("other instantiation errors keep SKILL_MISSING", func(t *testing.T) {
		a := admFactory(t)
		admWriteFormula(t, a, "needs-skill", nil, nil, []string{"no-such-skill"})
		writeRefusalAgents(t, a.root, "needs-skill")
		recs := installIntegrationReportRecorder(t)
		setSlingFlag(t, &slingNoLaunch, true)
		setSlingFlag(t, &slingCaller, "manager")

		c, buf := refusalSlingCmd(t)
		if err := dispatchToSpecialist(c, a.root, a.root, admAgent, "task"); err == nil {
			t.Fatalf("dispatch with a missing local skill succeeded, want an error\n%s", buf)
		}
		if n := mailTitled(t, a.store, "SKILL_MISSING: "+admAgent); n != 1 {
			t.Errorf("SKILL_MISSING mails = %d, want 1 (D7)", n)
		}
		if refused := reportsWithPrefix(*recs, "INTEGRATION_REFUSED "); len(refused) != 0 {
			t.Errorf("a non-admission error sent INTEGRATION_REFUSED mail: %+v", refused)
		}
		if strings.Contains(buf.String(), slingRefusedMarker) {
			t.Errorf("a non-admission error printed the refusal marker:\n%s", buf)
		}
	})
}

func writeRefusalAgents(t *testing.T, root, formulaName string) {
	t.Helper()
	agents := `{"agents":{` +
		`"manager":{"type":"interactive","description":"Interactive agent"},` +
		`"supervisor":{"type":"autonomous","description":"Autonomous agent"},` +
		`"` + admAgent + `":{"type":"autonomous","description":"specialist","formula":"` + formulaName + `"}}}`
	if err := os.WriteFile(config.AgentsConfigPath(root), []byte(agents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchRefusal_NotCountedAsSuccess(t *testing.T) {
	const key = "owner/repo#9"
	refused := func(at time.Time) dispatchEntry {
		return dispatchEntry{
			Agent: "impl", DispatchedAt: at, ItemURL: refusalItem().URL, Source: "issue",
			RefusalClass: refusalIntegrationMissing, RefusedAt: at, ConsecutiveRefusals: 1,
		}
	}

	t.Run("retry window does not hold a refusal past its backoff", func(t *testing.T) {
		fake, _ := setupHermeticSessions(t)
		calls := installRefusingSling(t)
		state := dispatchState{Dispatched: map[string]dispatchEntry{key: refused(time.Now().Add(-refusalPastBackoff))}}
		stats := refusalTick(t, fake, t.TempDir(), &state, refusalCfg(86400))
		if *calls != 1 {
			t.Errorf("dispatchItem calls = %d, want 1: a refused item is not 'recently dispatched' (stats %+v)", *calls, *stats)
		}
	})

	t.Run("phase completion ignores a refused record", func(t *testing.T) {
		_, store := setupHermeticSessions(t)
		closed := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
		e := refused(time.Now())
		e.Workflow, e.Phase, e.PhaseInstanceID = "feature-workflow", "enhancement", closed.ID
		got := computePhaseCompletion(context.Background(), t.TempDir(), map[string]dispatchEntry{key: e})
		if got[key] {
			t.Error("computePhaseCompletion reported a refused phase complete")
		}
	})

	t.Run("a refused bootstrap sling records the refusal and backs off", func(t *testing.T) {
		fake, _ := setupHermeticSessions(t)
		cfg := crossSourceCfg()
		wf := &cfg.Workflows[0]
		var edits []labelEdit
		recordLabelEdits(t, &edits)
		calls := installRefusingSling(t)
		const wfKey = "owner/repo#7"
		state := dispatchState{Dispatched: map[string]dispatchEntry{}}
		item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7", Labels: labels("agentic", "feature-workflow")}
		root := t.TempDir()

		cmd, _, _ := phase3Cmd()
		handleWorkflowItem(cmd, root, fake, &state, &dispatchCycleStats{start: time.Now()}, cfg, "owner/repo", item, "issue", wf)
		entry, ok := state.Dispatched[wfKey]
		if !ok || entry.RefusalClass != refusalIntegrationMissing || entry.Phase != "enhancement" || entry.PhaseInstanceID != "" {
			t.Fatalf("record after a refused bootstrap = %+v (present=%v), want a refused enhancement record with no instance", entry, ok)
		}

		item.Labels = labels("agentic", "feature-workflow", "enhancement")
		cmd, _, _ = phase3Cmd()
		handleWorkflowItem(cmd, root, fake, &state, &dispatchCycleStats{start: time.Now()}, cfg, "owner/repo", item, "issue", wf)
		if *calls != 1 {
			t.Errorf("dispatchItem calls = %d after the next tick, want 1: the refusal must back off, not re-sling as a lost record", *calls)
		}
	})

	t.Run("a refused re-sling restores a refused record", func(t *testing.T) {
		fake, store := setupHermeticSessions(t)
		cfg := crossSourceCfg()
		wf := &cfg.Workflows[0]
		open := seedOpenEpic(t, store)
		installRefusingSling(t)
		const wfKey = "owner/repo#7"
		state := dispatchState{Dispatched: map[string]dispatchEntry{wfKey: {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID: open.ID, PhaseDispatchedAt: time.Now().Add(-100 * time.Hour), Attempts: 2,
		}}}
		item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7", Labels: labels("agentic", "feature-workflow", "enhancement")}

		cmd, _, _ := phase3Cmd()
		handleWorkflowItem(cmd, t.TempDir(), fake, &state, &dispatchCycleStats{start: time.Now()}, cfg, "owner/repo", item, "issue", wf)
		entry := state.Dispatched[wfKey]
		if entry.RefusalClass != refusalIntegrationMissing || entry.RefusedAt.IsZero() || entry.ConsecutiveRefusals != 1 {
			t.Errorf("record after a refused re-sling = %+v, want the refusal recorded", entry)
		}
	})

	t.Run("agent recovery ignores a refused record", func(t *testing.T) {
		got := computeAgentRecovery(t.TempDir(), map[string]dispatchEntry{key: refused(time.Now())})
		if status, ok := got["impl"]; ok {
			t.Errorf("computeAgentRecovery reported %q for an agent whose only record is a refusal; a refused dispatch never ran it", status)
		}
	})

	t.Run("prune ages a refusal on RefusedAt", func(t *testing.T) {
		young := refused(time.Now().Add(-25 * time.Hour))
		young.RefusedAt = time.Now().Add(-time.Minute)
		old := refused(time.Now().Add(-25 * time.Hour))
		state := dispatchState{Dispatched: map[string]dispatchEntry{"young": young, "old": old}}
		pruneDispatchState(&state)
		if _, ok := state.Dispatched["young"]; !ok {
			t.Error("prune dropped a refusal younger than a day because its first refusal is older")
		}
		if _, ok := state.Dispatched["old"]; ok {
			t.Error("prune kept a refusal whose last refusal is older than a day")
		}
	})

	t.Run("status marks the row refused", func(t *testing.T) {
		entries := map[string]dispatchEntry{key: refused(time.Now())}
		text := formatDispatchStatus(false, entries, map[string]bool{}, map[string]bool{}, nil, time.Now())
		if !strings.Contains(text, "refused") || !strings.Contains(text, refusalIntegrationMissing) {
			t.Errorf("status text does not mark the refused row with its class:\n%s", text)
		}

		cmd, out, _ := phase3Cmd()
		if err := emitDispatchStatusJSON(cmd, false, entries, map[string]bool{}, map[string]bool{}, map[string]string{}, nil); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Entries []map[string]json.RawMessage `json:"entries"`
		}
		if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
			t.Fatalf("status JSON: %v\n%s", err, out)
		}
		if len(doc.Entries) != 1 {
			t.Fatalf("entries = %d, want 1", len(doc.Entries))
		}
		for _, k := range []string{"refusal_class", "refused_at", "consecutive_refusals"} {
			if _, ok := doc.Entries[0][k]; !ok {
				t.Errorf("refused status row lacks %q: %s", k, out)
			}
		}
	})
}

// precheckRecordAWSCore records aws-core in root as an install would; with a [check], its record stays fresh
// for an hour. It returns the recorded content hash.
func precheckRecordAWSCore(t *testing.T, root string, fake *fakeTmux, withCheck bool) string {
	t.Helper()
	intBCleanupWritable(t, root)
	e := &intBEnv{root: root, ext: t.TempDir(), fake: fake}
	files := intBSource(e, "aws-core", intBManifestOpts{noCheck: !withCheck})
	if withCheck {
		m := files[config.IntegrationManifestFile]
		m.body = strings.Replace(m.body, "[check]\nrun = \"af/check.sh\"\n", "[check]\nrun = \"af/check.sh\"\nfresh_for = \"1h\"\n", 1)
		files[config.IntegrationManifestFile] = m
	}
	intBRecord(t, e, "aws-core", files, nil)
	return intBLoadEntry(t, root, "aws-core").Integration.ContentSHA256
}

func precheckServiceLaunchedJustNow(t *testing.T, root string) {
	t.Helper()
	st := integrationServiceState{LaunchedAt: integrationNowFn().UTC().Format(time.RFC3339Nano), ConsecutiveRelaunches: 1}
	if err := writeIntegrationServiceState(root, "aws-core", st); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchRefusal_NoWorktreeResidue(t *testing.T) {
	for _, tc := range []struct {
		name, class string
		skills      string
		install     func(t *testing.T, root string, fake *fakeTmux)
	}{
		{name: "missing", class: refusalIntegrationMissing, install: func(*testing.T, string, *fakeTmux) {}},
		{name: "check_failed", class: refusalIntegrationCheckFailed, install: func(t *testing.T, root string, fake *fakeTmux) {
			sha := precheckRecordAWSCore(t, root, fake, true)
			fake.present["aws-core-svc"] = true
			admWriteCheckRecordState(t, root, "aws-core", integrationCheckFail, sha, time.Now())
		}},
		{name: "service_down", class: refusalIntegrationServiceDown, install: func(t *testing.T, root string, fake *fakeTmux) {
			precheckRecordAWSCore(t, root, fake, false)
			precheckServiceLaunchedJustNow(t, root)
		}},
		{name: "namespaced_skill_unresolved", class: refusalNamespacedSkillUnresolved, skills: "skills = [\"other-plugin:foo\"]\n",
			install: func(t *testing.T, root string, fake *fakeTmux) {
				precheckRecordAWSCore(t, root, fake, false)
				fake.present["aws-core-svc"] = true
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const agent, formulaName = "specialist-agent", "needs-aws"
			toml := "formula = \"" + formulaName + "\"\ntype = \"workflow\"\nversion = 1\nintegrations = [\"aws-core\"]\n" + tc.skills +
				"\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
			root, agentDir := createTestFormulaFactoryWithTOML(t, formulaName, agent, toml)
			fake, store := setupHermeticSessions(t)
			tc.install(t, root, fake)
			writeSpecialistAgentsJSON(t, root, agent, formulaName)
			installNoopLaunchSession(t)
			t.Setenv("AF_WORKTREE", "")
			t.Setenv("AF_WORKTREE_ID", "")
			t.Setenv("AF_ROLE", "")
			t.Setenv("TMUX", "1")
			fake.currentSession = session.DispatchSessionName()
			fake.present[session.SessionName(agent)] = true
			setSlingFlag(t, &slingReset, true)
			setSlingFlag(t, &slingCaller, "manager")

			runtimeDir := filepath.Join(agentDir, ".runtime")
			if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(runtimeDir, "session_id"), []byte("live-session\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
			if err := os.MkdirAll(callerWd, 0o755); err != nil {
				t.Fatal(err)
			}
			opsBefore := len(fake.ops)

			c, buf := refusalSlingCmd(t)
			err := dispatchToSpecialist(c, root, callerWd, agent, "https://github.com/owner/repo/issues/9")
			assertNotTeardownRefused(t, err)
			var r *integrationRefusal
			if !errors.As(err, &r) || r.Class != tc.class {
				t.Fatalf("err = %v, want an %q integration refusal\n%s", err, tc.class, buf)
			}

			for _, op := range fake.ops[opsBefore:] {
				if strings.HasPrefix(op, "KillSession ") || strings.HasPrefix(op, "SendKeysRaw ") {
					t.Errorf("a refused dispatch touched the running agent: %s", op)
				}
			}
			wts, _ := os.ReadDir(filepath.Join(root, ".agentfactory", "worktrees"))
			if len(wts) != 0 {
				var names []string
				for _, e := range wts {
					names = append(names, e.Name())
				}
				t.Errorf("a refused dispatch left worktree residue: %v", names)
			}
			for _, f := range []string{"formula_caller", "dispatch_owner", "dispatched", "hooked_formula", integrationPinFile} {
				if intBExists(filepath.Join(runtimeDir, f)) {
					t.Errorf("a refused dispatch wrote .runtime/%s", f)
				}
			}
			if !intBExists(filepath.Join(runtimeDir, "session_id")) {
				t.Error("a refused dispatch reset the agent: .runtime/session_id is gone")
			}
			all, lerr := store.List(t.Context(), issuestore.Filter{IncludeAllAgents: true, IncludeClosed: true})
			if lerr != nil {
				t.Fatal(lerr)
			}
			for _, iss := range all {
				if iss.Type == issuestore.TypeEpic {
					t.Errorf("a refused dispatch created formula epic %s", iss.ID)
				}
			}
		})
	}
}

// The pre-check refuses only on a check record it can read, so a first-ever failing [check] is refused by
// admission inside instantiation, after the dispatch created the agent's worktree. The refusal must take
// that worktree back.
func TestDispatchRefusal_FirstCheckFailureLeavesNoWorktree(t *testing.T) {
	const agent, formulaName = "specialist-agent", "needs-aws"
	toml := "formula = \"" + formulaName + "\"\ntype = \"workflow\"\nversion = 1\nintegrations = [\"aws-core\"]\n" +
		"\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
	root, _ := createTestFormulaFactoryWithTOML(t, formulaName, agent, toml)
	fake, _ := setupHermeticSessions(t)
	intBCleanupWritable(t, root)
	e := &intBEnv{root: root, ext: t.TempDir(), fake: fake}
	files := intBSource(e, "aws-core", intBManifestOpts{})
	files["af/check.sh"] = intBFile{"#!/bin/sh\necho check-down >&2\nexit 1\n", 0o755}
	intBRecord(t, e, "aws-core", files, nil)
	fake.present["aws-core-svc"] = true
	writeSpecialistAgentsJSON(t, root, agent, formulaName)
	installNoopLaunchSession(t)
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "1")
	fake.currentSession = session.DispatchSessionName()
	setSlingFlag(t, &slingCaller, "manager")
	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	if err := os.MkdirAll(callerWd, 0o755); err != nil {
		t.Fatal(err)
	}

	c, buf := refusalSlingCmd(t)
	err := dispatchToSpecialist(c, root, callerWd, agent, "https://github.com/owner/repo/issues/9")
	var r *integrationRefusal
	if !errors.As(err, &r) || r.Class != refusalIntegrationCheckFailed {
		t.Fatalf("err = %v, want an %q integration refusal\n%s", err, refusalIntegrationCheckFailed, buf)
	}
	if !strings.Contains(buf.String(), "Created worktree") {
		t.Fatalf("fixture: the dispatch did not create a worktree before admission refused, so this case proves nothing\n%s", buf)
	}
	if wts, _ := os.ReadDir(filepath.Join(root, ".agentfactory", "worktrees")); len(wts) != 0 {
		var names []string
		for _, e := range wts {
			names = append(names, e.Name())
		}
		t.Errorf("a dispatch refused inside instantiation left worktree residue: %v\n%s", names, buf)
	}
}

// TestPrecheck_LeavesRepairableStateToAdmission pins the other half of the pre-check's contract: what
// admission could still repair (re-run a stale check, start a down service, bind an optional) is not refused.
func TestPrecheck_LeavesRepairableStateToAdmission(t *testing.T) {
	precheck := func(t *testing.T, root string, skills, optional []string) error {
		t.Helper()
		f := &formula.Formula{Name: "needs-aws", Integrations: []string{"aws-core"}, IntegrationsOptional: optional, Skills: skills}
		return precheckFormulaIntegrations(root, f)
	}
	setup := func(t *testing.T, withCheck bool) (string, *fakeTmux, string) {
		t.Helper()
		root := t.TempDir()
		if err := os.MkdirAll(config.ConfigDir(root), 0o755); err != nil {
			t.Fatal(err)
		}
		fake, _ := setupHermeticSessions(t)
		return root, fake, precheckRecordAWSCore(t, root, fake, withCheck)
	}

	t.Run("service_down_outside_backoff_is_started_by_admission", func(t *testing.T) {
		root, _, _ := setup(t, false)
		if err := precheck(t, root, nil, nil); err != nil {
			t.Errorf("precheck = %v, want nil: the ensure would launch a service that was never started", err)
		}
	})
	t.Run("failing_check_record_that_is_stale", func(t *testing.T) {
		root, fake, sha := setup(t, true)
		fake.present["aws-core-svc"] = true
		admWriteCheckRecordState(t, root, "aws-core", integrationCheckFail, sha, time.Now().Add(-2*time.Hour))
		if err := precheck(t, root, nil, nil); err != nil {
			t.Errorf("precheck = %v, want nil: admission re-runs a check whose record is past fresh_for", err)
		}
	})
	t.Run("failing_check_record_of_other_content", func(t *testing.T) {
		root, fake, _ := setup(t, true)
		fake.present["aws-core-svc"] = true
		admWriteCheckRecordState(t, root, "aws-core", integrationCheckFail, strings.Repeat("f", 64), time.Now())
		if err := precheck(t, root, nil, nil); err != nil {
			t.Errorf("precheck = %v, want nil: a record of other content says nothing about the consented one (D1)", err)
		}
	})
	t.Run("namespaced_skill_a_bound_plugin_provides", func(t *testing.T) {
		root, fake, _ := setup(t, false)
		fake.present["aws-core-svc"] = true
		if err := precheck(t, root, []string{"aws-core-plugin:aws-core"}, nil); err != nil {
			t.Errorf("precheck = %v, want nil: the required binding provides the skill", err)
		}
	})
	t.Run("namespaced_skill_an_installed_optional_may_provide", func(t *testing.T) {
		root, fake, _ := setup(t, false)
		fake.present["aws-core-svc"] = true
		e := &intBEnv{root: root, ext: t.TempDir(), fake: fake}
		intBRecord(t, e, "gcp-core", intBSource(e, "gcp-core", intBManifestOpts{noCheck: true}), nil)
		if err := precheck(t, root, []string{"gcp-core-plugin:gcp-core"}, []string{"gcp-core"}); err != nil {
			t.Errorf("precheck = %v, want nil: whether the optional binds is admission's call", err)
		}
	})
}
