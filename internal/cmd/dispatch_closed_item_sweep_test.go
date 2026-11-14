package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// #755 D4: a workflow record whose item GitHub reports CLOSED or MERGED is dropped by a sweep that
// reads each absent item's state. Absence from the open-only, agentic-filtered, 50-capped query is
// never enough on its own, so a record is kept unless GitHub itself says the item is closed.

func sweepRecord(source string) dispatchEntry {
	kind := "issues"
	if source == "pr" {
		kind = "pull"
	}
	return dispatchEntry{Agent: "iterate", ItemURL: "https://github.com/owner/repo/" + kind + "/0", Source: source,
		Workflow: "feature-workflow", Phase: "pr-iterate"}
}

func TestSweepClosedWorkflowItems(t *testing.T) {
	seeded := func() dispatchState {
		return dispatchState{Dispatched: map[string]dispatchEntry{
			"owner/repo#1": sweepRecord("issue"), // CLOSED → dropped
			"owner/repo#2": sweepRecord("pr"),    // MERGED → dropped by the pr sweep
			"owner/repo#3": sweepRecord("issue"), // OPEN (agentic removed by hand) → kept
			"owner/repo#4": sweepRecord("issue"), // gh error → kept
			"owner/repo#5": sweepRecord("issue"), // returned by this cycle's query → never read
			"owner/repo#6": {Agent: "impl", ItemURL: "https://github.com/owner/repo/issues/6", Source: "issue",
				DispatchedAt: time.Now()}, // not a workflow record → never read
			"other/repo#1":  sweepRecord("issue"), // another repo → never read
			"owner/repo#9":  sweepRecord("issue"), // an unknown state → kept
			"owner/repo#10": sweepRecord("pr"),    // OPEN PR → kept
		}}
	}
	states := map[string]string{"owner/repo#1": "CLOSED", "owner/repo#2": "MERGED", "owner/repo#3": "OPEN",
		"owner/repo#9": "LIMBO", "owner/repo#10": "OPEN"}
	stub := func(t *testing.T, reads *[]string) {
		stubItemStates(t, func(repo string, number int, source string) (string, error) {
			key := fmt.Sprintf("%s#%d", repo, number)
			*reads = append(*reads, key+"/"+source)
			if key == "owner/repo#4" {
				return "", errors.New("HTTP 502")
			}
			return states[key], nil
		})
	}
	sweepBoth := func(state *dispatchState) (string, string) {
		cmd, out, errBuf := phase3Cmd()
		sweepClosedWorkflowItems(cmd, state, "owner/repo", "issue", map[string]bool{"owner/repo#5": true})
		sweepClosedWorkflowItems(cmd, state, "owner/repo", "pr", map[string]bool{"owner/repo#5": true})
		return out.String(), errBuf.String()
	}

	t.Run("drops only what GitHub reports closed or merged", func(t *testing.T) {
		var reads []string
		stub(t, &reads)
		state := seeded()
		stdout, stderr := sweepBoth(&state)

		sort.Strings(reads)
		want := []string{"owner/repo#1/issue", "owner/repo#2/pr", "owner/repo#3/issue", "owner/repo#4/issue", "owner/repo#9/issue", "owner/repo#10/pr"}
		sort.Strings(want)
		if !equalStrs(reads, want) {
			t.Errorf("state reads = %v, want %v", reads, want)
		}
		var kept []string
		for k := range state.Dispatched {
			kept = append(kept, k)
		}
		sort.Strings(kept)
		if want := []string{"other/repo#1", "owner/repo#10", "owner/repo#3", "owner/repo#4", "owner/repo#5", "owner/repo#6", "owner/repo#9"}; !equalStrs(kept, want) {
			t.Errorf("records kept = %v, want %v", kept, want)
		}
		for _, line := range []string{
			`drop owner/repo#1: workflow "feature-workflow" record dropped; GitHub reports it CLOSED`,
			`drop owner/repo#2: workflow "feature-workflow" record dropped; GitHub reports it MERGED`,
		} {
			if !strings.Contains(stdout, line) {
				t.Errorf("stdout = %q, want the line %q", stdout, line)
			}
		}
		for _, open := range []string{"owner/repo#3", "owner/repo#10"} {
			for _, line := range strings.Split(stdout+stderr, "\n") {
				if strings.Contains(line, open+":") || strings.Contains(line, open+" ") {
					t.Errorf("an OPEN item is kept silently, but %s was named: %q", open, line)
				}
			}
		}
		for _, frag := range []string{"owner/repo#4", "HTTP 502", "owner/repo#9", `"LIMBO"`} {
			if !strings.Contains(stderr, "warning: ") || !strings.Contains(stderr, frag) {
				t.Errorf("stderr = %q, want a warning naming %q (a record is kept, never silently)", stderr, frag)
			}
		}
	})

	t.Run("a dry run reports and keeps every record", func(t *testing.T) {
		setDispatchDryRun(t)
		var reads []string
		stub(t, &reads)
		state := seeded()
		stdout, _ := sweepBoth(&state)
		if len(state.Dispatched) != len(seeded().Dispatched) {
			t.Errorf("a dry run dropped records: %d left of %d", len(state.Dispatched), len(seeded().Dispatched))
		}
		if !strings.Contains(stdout, `would drop owner/repo#1: workflow "feature-workflow" record; GitHub reports it CLOSED`) {
			t.Errorf("stdout = %q, want the would-drop line for owner/repo#1", stdout)
		}
	})

	t.Run("the drop survives the end-of-cycle save and the next reload", func(t *testing.T) {
		var reads []string
		stub(t, &reads)
		root := t.TempDir()
		state := seedStallState(t, root, seeded().Dispatched)
		sweepBoth(&state)
		pruneDispatchState(&state)
		if err := saveDispatchState(root, &state); err != nil {
			t.Fatal(err)
		}
		reloaded := loadDispatchState(root)
		for _, gone := range []string{"owner/repo#1", "owner/repo#2"} {
			if _, ok := reloaded.Dispatched[gone]; ok {
				t.Errorf("%s came back after save and reload", gone)
			}
		}
		if _, ok := reloaded.Dispatched["owner/repo#3"]; !ok {
			t.Error("the OPEN record owner/repo#3 was lost across save and reload")
		}
	})
}

// fakeGHForDispatch puts a gh on PATH that logs its argv and answers the dispatcher's queries from
// files in dir: issues.json for `issue list` and prs.json for `pr list` (fail-issue-list and
// fail-pr-list make them fail), and state.<N> for `issue view N` / `pr view N`.
func fakeGHForDispatch(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := refFakeBin(t, map[string]string{"gh": `
printf '%s\n' "$*" >> "$GH_FAKE/argv"
case "$1 $2" in
  "issue list") [ -e "$GH_FAKE/fail-issue-list" ] && { echo "HTTP 502" >&2; exit 1; }
                if [ -e "$GH_FAKE/issues.json" ]; then cat "$GH_FAKE/issues.json"; else echo '[]'; fi ;;
  "pr list") [ -e "$GH_FAKE/fail-pr-list" ] && { echo "HTTP 502" >&2; exit 1; }
                if [ -e "$GH_FAKE/prs.json" ]; then cat "$GH_FAKE/prs.json"; else echo '[]'; fi ;;
  "issue view"|"pr view") [ -e "$GH_FAKE/state.$3" ] || { echo "no such item $3" >&2; exit 1; }
                cat "$GH_FAKE/state.$3" ;;
  *) echo "unrouted gh $*" >&2; exit 2 ;;
esac
`})
	t.Setenv("GH_FAKE", dir)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func ghArgv(t *testing.T, dir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "argv"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func TestRunDispatch_SweepsClosedWorkflowRecordsOnlyAfterACompleteQuery(t *testing.T) {
	setup := func(t *testing.T) (root, gh string) {
		t.Helper()
		root = setupCronFactory(t)
		writeDispatchJSON(t, root, `{"repos":["o/r"],"trigger_label":"agentic","notify_on_complete":"manager",`+
			`"mappings":[{"labels":["bug"],"source":"issue","agent":"af610-patrol"}]}`)
		origAuth := checkGHAuth
		checkGHAuth = func() error { return nil }
		t.Cleanup(func() { checkGHAuth = origAuth })
		gh = fakeGHForDispatch(t)
		for n, st := range map[string]string{"5": "CLOSED", "6": "MERGED"} {
			if err := os.WriteFile(filepath.Join(gh, "state."+n), []byte(`{"state":"`+st+`"}`), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		seedStallState(t, root, map[string]dispatchEntry{
			"o/r#5": {Agent: "af610-patrol", ItemURL: "https://github.com/o/r/issues/5", Source: "issue",
				Workflow: "feature-workflow", Phase: "pr-iterate"},
			"o/r#6": {Agent: "af610-patrol", ItemURL: "https://github.com/o/r/pull/6", Source: "pr",
				Workflow: "feature-workflow", Phase: "pr-iterate"},
		})
		return root, gh
	}
	run := func(t *testing.T) string {
		t.Helper()
		cmd, out, errBuf := phase3Cmd()
		if err := runDispatch(cmd, nil); err != nil {
			t.Fatalf("runDispatch: %v\nstderr=%s", err, errBuf.String())
		}
		return out.String()
	}

	t.Run("a successful issue query sweeps the closed issue record", func(t *testing.T) {
		root, gh := setup(t)
		stdout := run(t)
		state := loadDispatchState(root)
		if _, ok := state.Dispatched["o/r#5"]; ok {
			t.Errorf("o/r#5 (CLOSED) survived the cycle: %+v", state.Dispatched)
		}
		if !strings.Contains(stdout, "drop o/r#5: ") {
			t.Errorf("stdout = %q, want a drop line for o/r#5", stdout)
		}
		if _, ok := state.Dispatched["o/r#6"]; !ok {
			t.Error("o/r#6 was dropped although no PR query ran (no pr mapping is configured)")
		}
		argv := ghArgv(t, gh)
		if !containsStr(argv, "issue view 5 --repo o/r --json state") {
			t.Errorf("gh argv = %q, want `issue view 5 --repo o/r --json state`", argv)
		}
		for _, a := range argv {
			if strings.HasPrefix(a, "pr view") {
				t.Errorf("gh argv %q: a PR record was read although no PR query ran", a)
			}
		}
	})

	t.Run("a failed issue query sweeps nothing", func(t *testing.T) {
		root, gh := setup(t)
		if err := os.WriteFile(filepath.Join(gh, "fail-issue-list"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		run(t)
		if _, ok := loadDispatchState(root).Dispatched["o/r#5"]; !ok {
			t.Error("o/r#5 was dropped after a failed query: a transient outage must not touch in-flight records")
		}
		for _, a := range ghArgv(t, gh) {
			if strings.Contains(a, " view ") {
				t.Errorf("gh argv %q: an item was read after a failed query", a)
			}
		}
	})

	t.Run("a query that hit the 50-item cap sweeps nothing", func(t *testing.T) {
		root, gh := setup(t)
		var items []string
		for n := 100; n < 150; n++ {
			items = append(items, fmt.Sprintf(`{"number":%d,"title":"t","url":"https://github.com/o/r/issues/%d","labels":[{"name":"agentic"}]}`, n, n))
		}
		if err := os.WriteFile(filepath.Join(gh, "issues.json"), []byte("["+strings.Join(items, ",")+"]"), 0o644); err != nil {
			t.Fatal(err)
		}
		run(t)
		if _, ok := loadDispatchState(root).Dispatched["o/r#5"]; !ok {
			t.Error("o/r#5 was dropped after a truncated query")
		}
		for _, a := range ghArgv(t, gh) {
			if strings.Contains(a, " view ") {
				t.Errorf("gh argv %q: an item was read after a truncated query", a)
			}
		}
	})
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestGhItemState_ReadsTheItemsStateBySource(t *testing.T) {
	gh := fakeGHForDispatch(t)
	for n, body := range map[string]string{"5": `{"state":"CLOSED"}`, "42": `{"state":"OPEN"}`, "7": `{"state":""}`, "8": `not json`} {
		if err := os.WriteFile(filepath.Join(gh, "state."+n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ghItemState("o/r", 5, "issue"); err != nil || got != "CLOSED" {
		t.Errorf(`ghItemState(issue 5) = (%q, %v), want ("CLOSED", nil)`, got, err)
	}
	if got, err := ghItemState("o/r", 42, "pr"); err != nil || got != "OPEN" {
		t.Errorf(`ghItemState(pr 42) = (%q, %v), want ("OPEN", nil)`, got, err)
	}
	if want := []string{"issue view 5 --repo o/r --json state", "pr view 42 --repo o/r --json state"}; !equalStrs(ghArgv(t, gh), want) {
		t.Errorf("gh argv = %q, want %q", ghArgv(t, gh), want)
	}
	for _, c := range []struct {
		name   string
		number int
		source string
	}{
		{"gh fails", 99, "issue"},
		{"empty state", 7, "issue"},
		{"unparseable answer", 8, "pr"},
		{"unknown source", 5, "discussion"},
	} {
		if got, err := ghItemState("o/r", c.number, c.source); err == nil {
			t.Errorf("%s: ghItemState = (%q, nil), want an error (no silent fallback)", c.name, got)
		}
	}
}

// DNC-5: the operator approves a pull request by merging it, so the dispatcher reads PR state and
// never acts on it.
func TestDispatcherNeverApprovesOrMerges(t *testing.T) {
	src := readRepoFile(t, "internal/cmd/dispatch.go")
	for _, banned := range []*regexp.Regexp{
		regexp.MustCompile(`"pr",\s*"merge"`),
		regexp.MustCompile(`"pr",\s*"review"`),
		regexp.MustCompile(`"--approve"`),
		regexp.MustCompile(`"--admin"`),
	} {
		if m := banned.FindString(src); m != "" {
			t.Errorf("internal/cmd/dispatch.go runs %s: no agent or dispatcher approves or merges a pull request", m)
		}
	}
}

func TestRunDispatch_SweepsClosedPRRecordsOnlyAfterACompletePRQuery(t *testing.T) {
	setup := func(t *testing.T) (root, gh string) {
		t.Helper()
		root = setupCronFactory(t)
		writeDispatchJSON(t, root, `{"repos":["o/r"],"trigger_label":"agentic","notify_on_complete":"manager",`+
			`"mappings":[{"labels":["review"],"source":"pr","agent":"af610-patrol"}]}`)
		origAuth := checkGHAuth
		checkGHAuth = func() error { return nil }
		t.Cleanup(func() { checkGHAuth = origAuth })
		gh = fakeGHForDispatch(t)
		if err := os.WriteFile(filepath.Join(gh, "state.6"), []byte(`{"state":"MERGED"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		seedStallState(t, root, map[string]dispatchEntry{
			"o/r#6": {Agent: "af610-patrol", ItemURL: "https://github.com/o/r/pull/6", Source: "pr",
				Workflow: "feature-workflow", Phase: "pr-iterate"},
		})
		return root, gh
	}
	run := func(t *testing.T) string {
		t.Helper()
		cmd, out, errBuf := phase3Cmd()
		if err := runDispatch(cmd, nil); err != nil {
			t.Fatalf("runDispatch: %v\nstderr=%s", err, errBuf.String())
		}
		return out.String()
	}
	t.Run("a successful PR query sweeps the merged PR record", func(t *testing.T) {
		root, gh := setup(t)
		stdout := run(t)
		if _, ok := loadDispatchState(root).Dispatched["o/r#6"]; ok {
			t.Error("o/r#6 (MERGED) survived a complete PR query")
		}
		if !strings.Contains(stdout, "drop o/r#6: ") {
			t.Errorf("stdout = %q, want a drop line for o/r#6", stdout)
		}
		if argv := ghArgv(t, gh); !containsStr(argv, "pr view 6 --repo o/r --json state") {
			t.Errorf("gh argv = %q, want `pr view 6 --repo o/r --json state`", argv)
		}
	})
	t.Run("a PR query that hit the 50-item cap sweeps nothing", func(t *testing.T) {
		root, gh := setup(t)
		var items []string
		for n := 100; n < 150; n++ {
			items = append(items, fmt.Sprintf(`{"number":%d,"title":"t","url":"https://github.com/o/r/pull/%d","labels":[{"name":"agentic"}]}`, n, n))
		}
		if err := os.WriteFile(filepath.Join(gh, "prs.json"), []byte("["+strings.Join(items, ",")+"]"), 0o644); err != nil {
			t.Fatal(err)
		}
		run(t)
		if _, ok := loadDispatchState(root).Dispatched["o/r#6"]; !ok {
			t.Error("o/r#6 was dropped after a truncated PR query")
		}
		for _, a := range ghArgv(t, gh) {
			if strings.Contains(a, " view ") {
				t.Errorf("gh argv %q: an item was read after a truncated PR query", a)
			}
		}
	})
	t.Run("a failed PR query sweeps nothing", func(t *testing.T) {
		root, gh := setup(t)
		if err := os.WriteFile(filepath.Join(gh, "fail-pr-list"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		run(t)
		if _, ok := loadDispatchState(root).Dispatched["o/r#6"]; !ok {
			t.Error("o/r#6 was dropped after a failed PR query")
		}
	})
}
