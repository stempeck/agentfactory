package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
)

// usingDocSection returns one `####` section of USING_AGENTFACTORY.md, its heading through the line
// before the next ATX heading outside a fence.
func usingDocSection(t *testing.T, content, heading string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if line != heading {
			continue
		}
		if start >= 0 {
			t.Fatalf("USING_AGENTFACTORY.md has duplicate %q headings (lines %d and %d)", heading, start+1, i+1)
		}
		start = i
	}
	if start < 0 {
		t.Fatalf("USING_AGENTFACTORY.md has no %q section", heading)
	}
	inFence := false
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			inFence = !inFence
			continue
		}
		if !inFence && isATXHeading(lines[i]) {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

func strictDispatchConfig(t *testing.T, raw string) config.DispatchConfig {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg config.DispatchConfig
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("the dispatch document does not strict-decode: %v\n%s", err, raw)
	}
	return cfg
}

const advisoryHealAgents = `{"agents":{
  "fable-implement":{"type":"autonomous","description":"implements","formula":"fable-implement"},
  "fable-review":{"type":"autonomous","description":"reviews","formula":"fable-review"},
  "fable-increment":{"type":"autonomous","description":"iterates","formula":"fable-increment"},
  "manager":{"type":"interactive","description":"m"}}}`

// proHealCfg is the healing loop's subset of agentfactory-pro's dispatch config: pr-review and pr-iterate
// keep the pr twins pro's feature-workflow needs, so the final phase consumes the heal's pull request.
func proHealCfg() config.DispatchConfig {
	return config.DispatchConfig{
		Repos: []string{"stempeck/agentfactory-pro"}, TriggerLabel: "agentic", NotifyOnComplete: "manager",
		Mappings: []config.DispatchMapping{
			{Labels: []string{"heal-implement"}, Source: "issue", Agent: "fable-implement"},
			{Labels: []string{"pr-review"}, Source: "issue", Agent: "fable-review"},
			{Labels: []string{"pr-review"}, Source: "pr", Agent: "fable-review"},
			{Labels: []string{"pr-iterate"}, Source: "issue", Agent: "fable-increment"},
			{Labels: []string{"pr-iterate"}, Source: "pr", Agent: "fable-increment"},
		},
		Workflows: []config.Workflow{{Label: "advisory-heal", Phases: []string{"heal-implement", "pr-review", "pr-iterate"}}},
	}
}

func TestWorkflowsDocDescribesThePRConsumingFinalPhase(t *testing.T) {
	workflows := usingDocSection(t, readUsingAgentfactoryDoc(t), "#### Workflows (multi-phase pipelines)")
	if !regexp.MustCompile("(?s)WORKFLOW_COMPLETE.*in-process.*sender `af`").MatchString(workflows) {
		t.Errorf("the Workflows section does not say the completion mail is filed in-process as sender `af`:\n%s", workflows)
	}
	for _, want := range []string{
		"open or merged",
		"closed without merging",
		"closed after `WORKFLOW_COMPLETE` is not seen again",
		"reports closed or merged",
		"`hold <key>: workflow ",
	} {
		if !strings.Contains(workflows, want) {
			t.Errorf("the Workflows section lacks %q (the PR-consuming final phase, the closed-item drop and the hold line)", want)
		}
	}
}

func TestAdvisoryHealDispatchConfig(t *testing.T) {
	// setupConfigFactory chdirs into a bare temp root, after which the module root is unreachable.
	moduleRoot := findModuleRoot(t)
	trackedPath := filepath.Join(moduleRoot, ".agentfactory", "dispatch.json")
	tracked, err := os.ReadFile(trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	heal := proHealCfg()
	// pro's tracked scalars: the setter fills the first two when zero, so without them the round trip differs.
	heal.IntervalSecs, heal.RetryAfterSecs, heal.RemoveTriggerAfterDispatch = 300, 1800, true
	healJSON, err := json.Marshal(heal)
	if err != nil {
		t.Fatal(err)
	}

	healFactory := func(t *testing.T, agents string) string {
		t.Helper()
		root := setupConfigFactory(t)
		if err := os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), []byte(agents), 0o644); err != nil {
			t.Fatal(err)
		}
		return root
	}

	t.Run("the heal config passes the strict setter", func(t *testing.T) {
		root := healFactory(t, advisoryHealAgents)
		if out, err := runConfigSet(t, runConfigDispatchSet, string(healJSON)); err != nil {
			t.Fatalf("af config dispatch set rejected the heal config: %v\n%s", err, out)
		}
		written, err := os.ReadFile(config.DispatchConfigPath(root))
		if err != nil {
			t.Fatal(err)
		}
		if got := strictDispatchConfig(t, string(written)); !reflect.DeepEqual(got, heal) {
			t.Errorf("the setter wrote\n%+v\nwant the heal config\n%+v", got, heal)
		}
		if after, _ := os.ReadFile(trackedPath); !bytes.Equal(after, tracked) {
			t.Error("the setter changed the tracked .agentfactory/dispatch.json; it must write only the factory it runs in")
		}

		t.Run("a phase agent without a formula is refused", func(t *testing.T) {
			healFactory(t, strings.Replace(advisoryHealAgents, `"description":"iterates","formula":"fable-increment"`, `"description":"iterates"`, 1))
			_, err := runConfigSet(t, runConfigDispatchSet, string(healJSON))
			if err == nil || !strings.Contains(err.Error(), "has no formula") {
				t.Errorf("err = %v, want the formula-bearing refusal (the agents file is load-bearing)", err)
			}
		})
	})

	const (
		key      = "stempeck/agentfactory-pro#7"
		issueURL = "https://github.com/stempeck/agentfactory-pro/issues/7"
		pullURL  = "https://github.com/stempeck/agentfactory-pro/pull/42"
	)
	type driven struct {
		slung, urls, mails []string
		live               map[string]bool
		state              dispatchState
		stats              *dispatchCycleStats
		stderr             string
	}
	drive := func(t *testing.T) driven {
		t.Helper()
		fake, store := setupHermeticSessions(t)
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(config.DispatchConfigPath(root)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.DispatchConfigPath(root), healJSON, 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.LoadDispatchConfig(root)
		if err != nil {
			t.Fatalf("the dispatcher's loader rejects the heal config: %v", err)
		}
		if len(cfg.Workflows) != 1 || len(cfg.Repos) != 1 {
			t.Fatalf("the heal config has %d workflows and %d repos, want 1 and 1", len(cfg.Workflows), len(cfg.Repos))
		}
		stubLinkedPRs(t, []int{42})
		d := driven{live: map[string]bool{"agentic": true, "advisory-heal": true}, state: dispatchState{Dispatched: map[string]dispatchEntry{}},
			stats: &dispatchCycleStats{start: time.Now()}}
		var ids []string
		recordSlingsWithURL(t, store, &d.slung, &d.urls, &ids)
		recordWorkflowMail(t, &d.mails)
		origEdit := editItemLabels
		editItemLabels = func(repo string, number int, source string, add, remove []string) error {
			for _, l := range remove {
				delete(d.live, l)
			}
			for _, l := range add {
				d.live[l] = true
			}
			return nil
		}
		t.Cleanup(func() { editItemLabels = origEdit })
		itemFromLive := func() ghItem {
			var ls []ghLabel
			for n := range d.live {
				ls = append(ls, ghLabel{Name: n})
			}
			return ghItem{Number: 7, URL: issueURL, Labels: ls}
		}
		cmd, _, errBuf := phase3Cmd()
		for cycle := 0; cycle < 10 && d.live["agentic"]; cycle++ {
			handleWorkflowItem(cmd, root, fake, &d.state, d.stats, cfg, cfg.Repos[0], itemFromLive(), "issue", &cfg.Workflows[0])
			if e, ok := d.state.Dispatched[key]; ok && e.PhaseInstanceID != "" {
				_ = store.Close(context.Background(), e.PhaseInstanceID, config.CloseReasonFormulaComplete)
			}
		}
		d.stderr = errBuf.String()
		if d.live["agentic"] {
			t.Fatalf("the pipeline did not reach terminal: live=%v slung=%v\nstderr=%s", d.live, d.slung, d.stderr)
		}
		return d
	}

	t.Run("the heal config carries both dual-source twins", func(t *testing.T) {
		for _, phase := range []string{"pr-review", "pr-iterate"} {
			var sources []string
			for _, m := range heal.Mappings {
				if len(m.Labels) == 1 && m.Labels[0] == phase {
					sources = append(sources, m.Source)
				}
			}
			if !equalStrs(sources, []string{"issue", "pr"}) {
				t.Errorf("%s mappings have sources %v, want [issue pr] (pro's feature-workflow needs the pr twin)", phase, sources)
			}
		}
	})

	t.Run("routes the issue through the three agents in order", func(t *testing.T) {
		stubItemState(t, "OPEN")
		d := drive(t)
		if want := []string{"fable-implement", "fable-review", "fable-increment"}; !equalStrs(d.slung, want) {
			t.Errorf("slung = %v, want %v", d.slung, want)
		}
		if want := []string{issueURL, pullURL, pullURL}; !equalStrs(d.urls, want) {
			t.Errorf("sling URLs = %v, want %v (pr-review and pr-iterate both consume the PR)", d.urls, want)
		}
		if !d.live["advisory-heal"] || d.live["agentic"] || d.live["pr-iterate"] {
			t.Errorf("final labels = %v, want advisory-heal kept and agentic and pr-iterate removed", d.live)
		}
		if want := []string{"manager|advisory-heal|" + key}; !equalStrs(d.mails, want) {
			t.Errorf("workflow-complete mails = %v, want %v", d.mails, want)
		}
		if _, ok := d.state.Dispatched[key]; ok {
			t.Errorf("the correlation record survived terminal completion: %+v", d.state.Dispatched)
		}
		if d.stats.errors != 0 {
			t.Errorf("stats.errors = %d, want 0; stderr=%s", d.stats.errors, d.stderr)
		}
	})

	t.Run("the terminal reads only the linked PR's state", func(t *testing.T) {
		var asked []string
		stubItemStates(t, func(repo string, number int, source string) (string, error) {
			asked = append(asked, fmt.Sprintf("%s#%d/%s", repo, number, source))
			return "OPEN", nil
		})
		if d := drive(t); len(d.mails) != 1 {
			t.Errorf("workflow-complete mails = %v, want exactly 1", d.mails)
		}
		if want := []string{"stempeck/agentfactory-pro#42/pr"}; !equalStrs(asked, want) {
			t.Errorf("ghItemState calls = %v, want %v: an open PR, unapproved and red, completes the heal", asked, want)
		}
	})

	t.Run("three heals share one session and each completes", func(t *testing.T) {
		fake, store := setupHermeticSessions(t)
		root := t.TempDir()
		cfg := proHealCfg()
		stubItemState(t, "OPEN")
		origLinked := ghLinkedPRs
		ghLinkedPRs = func(repo string, issue int) ([]int, error) { return []int{issue + 100}, nil }
		t.Cleanup(func() { ghLinkedPRs = origLinked })
		var mails []string
		recordWorkflowMail(t, &mails)

		live := map[int]map[string]bool{}
		for _, n := range []int{7, 8, 9} {
			live[n] = map[string]bool{"agentic": true, "advisory-heal": true}
		}
		origEdit := editItemLabels
		editItemLabels = func(repo string, number int, source string, add, remove []string) error {
			for _, l := range remove {
				delete(live[number], l)
			}
			for _, l := range add {
				live[number][l] = true
			}
			return nil
		}
		t.Cleanup(func() { editItemLabels = origEdit })
		slings := map[string]int{}
		origSling := dispatchItem
		dispatchItem = func(root, agent, itemURL, caller, model string) (string, error) {
			if fake.present[session.SessionName(agent)] {
				t.Errorf("slung %s onto its busy session for %s", agent, itemURL)
			}
			fake.present[session.SessionName(agent)] = true
			slings[agent]++
			iss, err := store.Create(context.Background(), issuestore.CreateParams{
				Title: "Formula: " + agent, Type: issuestore.TypeEpic, Labels: []string{"formula-instance"}, Assignee: "mgr",
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf(`Formula %q instantiated: %s (3 steps)`, agent, iss.ID), nil
		}
		t.Cleanup(func() { dispatchItem = origSling })

		state := dispatchState{Dispatched: map[string]dispatchEntry{}}
		stats := &dispatchCycleStats{start: time.Now()}
		cmd, _, errBuf := phase3Cmd()
		for cycle := 0; cycle < 30; cycle++ {
			for _, n := range []int{7, 8, 9} {
				if !live[n]["agentic"] {
					continue
				}
				var ls []ghLabel
				for l := range live[n] {
					ls = append(ls, ghLabel{Name: l})
				}
				item := ghItem{Number: n, URL: fmt.Sprintf("https://github.com/stempeck/agentfactory-pro/issues/%d", n), Labels: ls}
				handleWorkflowItem(cmd, root, fake, &state, stats, &cfg, cfg.Repos[0], item, "issue", &cfg.Workflows[0])
			}
			for _, e := range state.Dispatched {
				if e.PhaseInstanceID != "" && fake.present[session.SessionName(e.Agent)] {
					_ = store.Close(context.Background(), e.PhaseInstanceID, config.CloseReasonFormulaComplete)
					fake.present[session.SessionName(e.Agent)] = false
				}
			}
		}
		for _, n := range []int{7, 8, 9} {
			if live[n]["agentic"] {
				t.Errorf("heal #%d never completed: labels %v", n, live[n])
			}
		}
		if len(mails) != 3 {
			t.Errorf("completion mails = %v, want 3", mails)
		}
		for _, agent := range []string{"fable-implement", "fable-review", "fable-increment"} {
			if slings[agent] != 3 {
				t.Errorf("%s slung %d times, want once per heal (3)", agent, slings[agent])
			}
		}
		if stats.errors != 0 {
			t.Errorf("stats.errors = %d, want 0; stderr=%s", stats.errors, errBuf.String())
		}
	})

	t.Run("reordered pr review twin is rejected", func(t *testing.T) {
		reordered := heal
		reordered.Mappings = append([]config.DispatchMapping(nil), heal.Mappings...)
		first, twin := -1, -1
		for i, m := range reordered.Mappings {
			if len(m.Labels) == 1 && m.Labels[0] == "pr-review" {
				if m.Source == "issue" && first == -1 {
					first = i
				}
				if m.Source == "pr" {
					twin = i
				}
			}
		}
		if first == -1 || twin == -1 {
			t.Fatalf("the heal config lacks the pr-review issue mapping or its pr twin: %+v", heal.Mappings)
		}
		reordered.Mappings[first], reordered.Mappings[twin] = reordered.Mappings[twin], reordered.Mappings[first]
		if err := config.ValidateDispatchStructure(&reordered); err == nil || !strings.Contains(err.Error(), "mixed source") {
			t.Errorf("ValidateDispatchStructure = %v, want the mixed-source refusal", err)
		}
		raw, err := json.Marshal(reordered)
		if err != nil {
			t.Fatal(err)
		}
		root := healFactory(t, advisoryHealAgents)
		if _, err := runConfigSet(t, runConfigDispatchSet, string(raw)); err == nil || !strings.Contains(err.Error(), "mixed source") {
			t.Errorf("af config dispatch set = %v, want the mixed-source refusal", err)
		}
		if _, err := os.Stat(config.DispatchConfigPath(root)); !os.IsNotExist(err) {
			t.Errorf("a refused document was written: %v", err)
		}
	})
}
