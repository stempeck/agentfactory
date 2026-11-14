package cmd

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

var wtFirstSeen = time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)

// wtSessionRecord is the DD L221-227 block's own values: manager on fable-5-2, 1000000 declared,
// 200000 applied by 2.1.308, 44 sessions, manager's own figure the one holder.
func wtSessionRecord() windowRecord {
	return windowRecord{
		Profile:        "fable-5-2",
		ModelID:        "claude-fable-5-2",
		HarnessVersion: "2.1.308",
		Declared:       1000000,
		Applied:        200000,
		Agents: map[string]agentWindow{
			"manager": {Applied: 200000, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen.Add(48 * time.Hour)},
		},
		FirstSeen: wtFirstSeen,
		LastSeen:  wtFirstSeen.Add(48 * time.Hour),
		Sessions:  44,
		Source:    "session",
	}
}

func wtProbeRecord() windowRecord {
	rec := wtSessionRecord()
	rec.Agents = nil
	rec.Probed = 200000
	rec.Source = "probe"
	rec.FirstSeen = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	rec.LastSeen = wtFirstSeen
	return rec
}

func wtLines(t *testing.T, s string, want int) []string {
	t.Helper()
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) != want {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), want, s)
	}
	return lines
}

func TestFormatWindowText_RefusalMatchesInterface(t *testing.T) {
	want := strings.Join([]string{
		`agent "manager": profile "fable-5-2" declares a 1000000-token window, but Claude Code 2.1.308`,
		`    applied 200000 to model "claude-fable-5-2" (first seen 2026-11-10, 44 sessions).`,
		`    The session would auto-compact near 170k. Do one of:`,
		`      - install a Claude Code version that knows this model, then launch again (re-measured automatically)`,
		`      - lower the profile's CLAUDE_CODE_MAX_CONTEXT_TOKENS to 200000 (af config models set)`,
		`      - launch anyway: --accept-window`,
		`    Record: .agentfactory/statusline/windows/fable-5-2.json (held by the last report of "manager"; each agent's next session on this identity replaces its own figure; once the cause is fixed, removing this file resets it)`,
	}, "\n")

	text := formatWindowText(t.TempDir(), "manager", wtSessionRecord(), 200000)
	if got := strings.TrimRight(text.Refusal, "\n"); got != want {
		t.Errorf("Refusal is not the seven lines of DD L221-227:\ngot:\n%s\nwant:\n%s", got, want)
	}
	if strings.HasPrefix(text.Refusal, "af: ") {
		t.Errorf("Refusal carries the leading \"af: \" (the caller adds it): %q", text.Refusal)
	}
	wantHead := strings.Join(strings.Split(want, "\n")[:2], "\n")
	if text.Head != wantHead {
		t.Errorf("Head is not the first two refusal lines:\ngot:  %q\nwant: %q", text.Head, wantHead)
	}

	probe := formatWindowText(t.TempDir(), "manager", wtProbeRecord(), 200000)
	lines := wtLines(t, probe.Refusal, 7)
	if lines[6] != "    Record: .agentfactory/statusline/windows/fable-5-2.json (once the cause is fixed, removing this file resets it)" {
		t.Errorf("a probe row's Record: line is the path and its reset, got %q", lines[6])
	}
}

func TestFormatWindowText_ProbeParenthesis(t *testing.T) {
	text := formatWindowText(t.TempDir(), "manager", wtProbeRecord(), 200000)
	lines := wtLines(t, text.Head, 2)
	if want := `    applied 200000 to model "claude-fable-5-2" (measured by probe, 2026-11-10).`; lines[1] != want {
		t.Errorf("probe parenthesis:\ngot:  %q\nwant: %q (the date is last_seen, not first_seen)", lines[1], want)
	}
	if strings.Contains(text.Refusal, "first seen") || strings.Contains(text.Refusal, "sessions)") {
		t.Errorf("a probe row printed the session parenthesis: %q", text.Refusal)
	}
}

func TestFormatWindowText_ForeignBody(t *testing.T) {
	const sentence = "af keeps using the declared window for this profile; verify the gateway applies it."
	rec := windowRecord{
		Profile: "sol", ModelID: "gpt-5.6-sol", HarnessVersion: "2.1.322", Class: "foreign",
		Declared: 400000, Applied: 272000, FirstSeen: wtFirstSeen, LastSeen: wtFirstSeen, Sessions: 2, Source: "session",
		Agents: map[string]agentWindow{"worker": {Applied: 272000, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen}},
	}
	text := formatWindowText(t.TempDir(), "worker", rec, 272000)
	if text.Head == "" || !strings.HasPrefix(text.Body, text.Head) {
		t.Fatalf("the Body must open with the Head: Head=%q Body=%q", text.Head, text.Body)
	}
	if !strings.HasSuffix(strings.TrimRight(text.Body, "\n"), sentence) {
		t.Errorf("a foreign-class Body must end with %q, got %q", sentence, text.Body)
	}
	if text.Subject != "WINDOW_MISMATCH: worker on profile sol" {
		t.Errorf("Subject = %q", text.Subject)
	}

	rec.Class, rec.ModelID = "", "claude-sol"
	if body := formatWindowText(t.TempDir(), "worker", rec, 272000).Body; strings.Contains(body, sentence) {
		t.Errorf("a not-foreign Body carries the foreign sentence: %q", body)
	}
}

func TestFormatWindowText_FirstObservationWithoutDeclaration(t *testing.T) {
	rec := windowRecord{
		Profile: "local", ModelID: "claude-opus-5", HarnessVersion: "2.1.322", Applied: 262144,
		FirstSeen: wtFirstSeen, LastSeen: wtFirstSeen, Sessions: 1, Source: "session",
		Agents: map[string]agentWindow{"worker": {Applied: 262144, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen}},
	}
	text := formatWindowText(t.TempDir(), "worker", rec, 262144)
	if want := `profile "local" declares no window; Claude Code 2.1.322 applied 262144 to model "claude-opus-5"`; text.Body != want {
		t.Errorf("declared 0: the Body is the DD L115 sentence alone\ngot:  %q\nwant: %q", text.Body, want)
	}
}

func TestFormatWindowText_CompactionFigureOnlyAt200000(t *testing.T) {
	rec := wtSessionRecord()
	rec.Applied = 262144
	rec.Agents["manager"] = agentWindow{Applied: 262144, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen}
	text := formatWindowText(t.TempDir(), "manager", rec, 262144)
	lines := wtLines(t, text.Refusal, 7)
	if lines[2] != "    Do one of:" {
		t.Errorf("applied 262144: third line = %q, want %q", lines[2], "    Do one of:")
	}
	for name, s := range map[string]string{"Head": text.Head, "Body": text.Body, "Refusal": text.Refusal, "Summary": text.Summary} {
		if strings.Contains(s, "170k") {
			t.Errorf("%s names the 200000-only compaction figure for an applied 262144: %q", name, s)
		}
	}
	if lines[4] != "      - lower the profile's CLAUDE_CODE_MAX_CONTEXT_TOKENS to 262144 (af config models set)" {
		t.Errorf("the lower exit prints the applied figure passed in, got %q", lines[4])
	}

	at200k := wtLines(t, formatWindowText(t.TempDir(), "manager", wtSessionRecord(), 200000).Refusal, 7)
	if at200k[2] != "    The session would auto-compact near 170k. Do one of:" {
		t.Errorf("applied 200000: third line = %q", at200k[2])
	}
}

func TestFormatWindowText_HeadWithoutAgent(t *testing.T) {
	text := formatWindowText(t.TempDir(), "", wtSessionRecord(), 200000)
	if !strings.HasPrefix(text.Head, `profile "fable-5-2" declares a 1000000-token window, but Claude Code 2.1.308`) {
		t.Errorf("with no agent the Head starts at `profile %%q declares`, got %q", text.Head)
	}
	if strings.Contains(text.Head, "agent ") {
		t.Errorf("an agent-less Head names an agent: %q", text.Head)
	}
}

func TestFormatWindowText_SummaryIsOneSelfContainedLine(t *testing.T) {
	text := formatWindowText(t.TempDir(), "manager", wtSessionRecord(), 200000)
	want := `agent "manager": profile "fable-5-2" declares a 1000000-token window, but Claude Code 2.1.308 applied 200000 to model "claude-fable-5-2"; refused (held by the last report of "manager", replaced at each one's next session on this identity; launch anyway: --accept-window; record: .agentfactory/statusline/windows/fable-5-2.json)`
	if text.Summary != want {
		t.Errorf("session-row Summary:\ngot:  %q\nwant: %q", text.Summary, want)
	}
	for _, needle := range []string{`"manager"`, `"fable-5-2"`, "1000000", "200000", "2.1.308", "--accept-window",
		".agentfactory/statusline/windows/fable-5-2.json", "held by the last report of"} {
		if !strings.Contains(text.Summary, needle) {
			t.Errorf("Summary lacks %q: %q", needle, text.Summary)
		}
	}
	if strings.Contains(text.Summary, "\n") {
		t.Errorf("Summary spans lines: %q", text.Summary)
	}

	t.Run("a holder read off disk that is not an agent name is never printed", func(t *testing.T) {
		rec := wtSessionRecord()
		planted := strings.Repeat("x", 4096)
		rec.Agents = map[string]agentWindow{
			"alpha":        {Applied: 200000, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen},
			planted:        {Applied: 200000, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen},
			"../escape":    {Applied: 200000, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen},
			"bad\x1bname":  {Applied: 200000, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen},
			"with a space": {Applied: 200000, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen},
		}
		txt := formatWindowText(t.TempDir(), "alpha", rec, 200000)
		for _, line := range []string{txt.Summary, txt.Refusal} {
			if !strings.Contains(line, `"alpha"`) {
				t.Errorf("the valid holder is missing: %q", line)
			}
			for _, bad := range []string{planted[:64], "escape", "name", "with a space", " more"} {
				if strings.Contains(line, bad) {
					t.Errorf("an invalid holder name reached the text (%q): %q", bad, line)
				}
			}
		}
	})

	t.Run("five holders print the first three sorted and the rest as a count", func(t *testing.T) {
		rec := wtSessionRecord()
		rec.Agents = map[string]agentWindow{}
		for _, a := range []string{"echo", "bravo", "delta", "alpha", "charlie"} {
			rec.Agents[a] = agentWindow{Applied: 200000, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen}
		}
		rec.Agents["zulu"] = agentWindow{Applied: 262144, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen}
		s := formatWindowText(t.TempDir(), "alpha", rec, 200000).Summary
		held := strings.Index(s, "held by the last report of")
		if held < 0 {
			t.Fatalf("no holder clause: %q", s)
		}
		clause := s[held:]
		i, j, k := strings.Index(clause, `"alpha"`), strings.Index(clause, `"bravo"`), strings.Index(clause, `"charlie"`)
		if i < 0 || !(i < j && j < k) {
			t.Errorf("the holders are not the first three, sorted, after the holder clause: %q", s)
		}
		if strings.Contains(s, `"delta"`) || strings.Contains(s, `"echo"`) {
			t.Errorf("more than three holders printed: %q", s)
		}
		if !strings.Contains(s, " and 2 more") {
			t.Errorf("the two further holders are not counted as ` and 2 more`: %q", s)
		}
		if strings.Contains(s, `"zulu"`) {
			t.Errorf("an agent whose figure is not the judged minimum is named a holder: %q", s)
		}
		if strings.Contains(s, "\n") {
			t.Errorf("Summary spans lines: %q", s)
		}
	})

	t.Run("a probe row names no holder", func(t *testing.T) {
		s := formatWindowText(t.TempDir(), "manager", wtProbeRecord(), 200000).Summary
		want := `agent "manager": profile "fable-5-2" declares a 1000000-token window, but Claude Code 2.1.308 applied 200000 to model "claude-fable-5-2"; refused (launch anyway: --accept-window; record: .agentfactory/statusline/windows/fable-5-2.json)`
		if s != want {
			t.Errorf("probe-row Summary:\ngot:  %q\nwant: %q", s, want)
		}
	})
}

func TestFormatWindowText_UnobservedAndBoundBodies(t *testing.T) {
	launched := time.Date(2026, 11, 12, 8, 15, 0, 0, time.UTC)
	fact := windowLaunchFact{Profile: "fable-5-2", ModelID: "claude-fable-5-2", Declared: 1000000, HarnessVersion: "2.1.308", LaunchedAt: launched}
	for _, cause := range []string{"statusline gate off", "no snapshot yet", "snapshot without a window"} {
		t.Run(cause, func(t *testing.T) {
			text := formatWindowUnobserved("manager", fact, cause)
			want := fmt.Sprintf(`agent "manager": profile "fable-5-2" declares a 1000000-token window on Claude Code 2.1.308, and the session launched at 2026-11-12T08:15:00Z has reported no window (%s).`, cause)
			if text.Body != want {
				t.Errorf("unobserved body:\ngot:  %q\nwant: %q", text.Body, want)
			}
			if strings.Contains(text.Body, "applied 0") {
				t.Errorf("the unobserved body reports a zero window: %q", text.Body)
			}
			if text.Subject != "WINDOW_MISMATCH: manager on profile fable-5-2" {
				t.Errorf("Subject = %q", text.Subject)
			}
			if text.Head != "" || text.Refusal != "" {
				t.Errorf("item 4's mail has no Head or Refusal: %+v", text)
			}
		})
	}

	t.Run("bound", func(t *testing.T) {
		text := formatWindowBound(t.TempDir(), "manager", wtSessionRecord(), 1000000, 200000)
		want := `agent "manager": step_context.bound_tokens is 1000000, above the 200000-token window Claude Code 2.1.308 applied to model "claude-fable-5-2" on profile "fable-5-2"; steps are judged against 200000.`
		if text.Body != want {
			t.Errorf("K6 body:\ngot:  %q\nwant: %q", text.Body, want)
		}
		if text.Subject != "WINDOW_MISMATCH: manager on profile fable-5-2" {
			t.Errorf("Subject = %q", text.Subject)
		}
	})
}

func TestFormatWindowText_HarnessNotTheCause(t *testing.T) {
	const sentence = "The probe measured 1000000 on Claude Code 2.1.308 for this model, so the harness is not the cause: check the env block of the user-scope Claude Code settings.json, which af up compares with the profile."
	rec := wtSessionRecord()
	rec.Probed, rec.Applied = 1000000, 262144
	rec.Agents["manager"] = agentWindow{Applied: 262144, LaunchedAt: wtFirstSeen, LastSeen: wtFirstSeen}
	body := formatWindowText(t.TempDir(), "manager", rec, 262144).Body
	if !strings.HasSuffix(strings.TrimRight(body, "\n"), sentence) {
		t.Errorf("Probed 1000000 above a 262144 figure: the Body must end with\n%q\ngot %q", sentence, body)
	}

	rec.Probed = 262144
	if body := formatWindowText(t.TempDir(), "manager", rec, 262144).Body; body == "" || strings.Contains(body, "harness is not the cause") {
		t.Errorf("Probed equal to the figure: want a Body without the harness sentence, got %q", body)
	}
}

func TestFormatWindowText_RefusalNamesTheReset(t *testing.T) {
	const recordPath = ".agentfactory/statusline/windows/fable-5-2.json"
	for name, rec := range map[string]windowRecord{"session row": wtSessionRecord(), "probe row": wtProbeRecord()} {
		t.Run(name, func(t *testing.T) {
			lines := wtLines(t, formatWindowText(t.TempDir(), "manager", rec, 200000).Refusal, 7)
			last := lines[6]
			if !strings.HasPrefix(last, "    Record: ") {
				t.Fatalf("the seventh refusal line is no longer the Record: line: %q", last)
			}
			if !strings.Contains(last, recordPath) {
				t.Errorf("the Record: line must name the profile's own record %s: %q", recordPath, last)
			}
			if !strings.Contains(strings.ToLower(last), "remov") {
				t.Errorf("the Record: line must say that removing the record is the reset once the cause is fixed: %q", last)
			}
			if strings.Contains(last, "<profile>") {
				t.Errorf("the Record: line names a placeholder instead of the profile: %q", last)
			}
		})
	}
}

var t20SessionCount = regexp.MustCompile(`\d+ sessions?\)`)

func TestFormatWindowText_SessionCountIsPluralised(t *testing.T) {
	one := wtSessionRecord()
	one.Sessions = 1
	text := formatWindowText(t.TempDir(), "manager", one, 200000)
	if !strings.Contains(text.Head, "(first seen 2026-11-10, 1 session).") {
		t.Errorf("one session must read `1 session`: %q", text.Head)
	}
	for name, s := range map[string]string{"Head": text.Head, "Body": text.Body, "Refusal": text.Refusal} {
		if strings.Contains(s, "1 sessions") {
			t.Errorf("%s prints `1 sessions`: %q", name, s)
		}
	}

	if many := formatWindowText(t.TempDir(), "manager", wtSessionRecord(), 200000); !strings.Contains(many.Head, "(first seen 2026-11-10, 44 sessions).") {
		t.Errorf("44 sessions must stay plural: %q", many.Head)
	}

	probe := wtProbeRecord()
	probe.Sessions = 1
	if r := formatWindowText(t.TempDir(), "manager", probe, 200000).Refusal; t20SessionCount.MatchString(r) {
		t.Errorf("a probe row printed a session count: %q", r)
	}
}
