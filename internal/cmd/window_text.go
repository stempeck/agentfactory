package cmd

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

type windowText struct {
	Subject string
	Head    string
	Body    string
	Refusal string
	Summary string
}

func windowRecordRelPath(profile string) string {
	return ".agentfactory/statusline/windows/" + profile + ".json"
}

func windowHolders(rec windowRecord) string {
	var names []string
	for agent, fig := range rec.Agents {
		if fig.Applied == rec.Applied && config.ValidateAgentName(agent) == nil {
			names = append(names, agent)
		}
	}
	sort.Strings(names)
	var quoted []string
	for i, n := range names {
		if i == 3 {
			break
		}
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	s := strings.Join(quoted, ", ")
	if len(names) > 3 {
		s += fmt.Sprintf(" and %d more", len(names)-3)
	}
	return s
}

func formatWindowText(root, agent string, rec windowRecord, applied int64) windowText {
	t := windowText{Subject: fmt.Sprintf("WINDOW_MISMATCH: %s on profile %s", agent, rec.Profile)}
	probe := rec.Source == "probe"
	lead := ""
	if agent != "" {
		lead = fmt.Sprintf("agent %q: ", agent)
	}
	paren := fmt.Sprintf("(first seen %s, %d sessions)", rec.FirstSeen.Format("2006-01-02"), rec.Sessions)
	if rec.Sessions == 1 {
		paren = fmt.Sprintf("(first seen %s, 1 session)", rec.FirstSeen.Format("2006-01-02"))
	}
	if probe {
		paren = fmt.Sprintf("(measured by probe, %s)", rec.LastSeen.Format("2006-01-02"))
	}
	t.Head = lead + fmt.Sprintf("profile %q declares a %d-token window, but Claude Code %s\n    applied %d to model %q %s.",
		rec.Profile, rec.Declared, rec.HarnessVersion, applied, rec.ModelID, paren)

	if rec.Declared == 0 {
		t.Body = fmt.Sprintf("profile %q declares no window; Claude Code %s applied %d to model %q", rec.Profile, rec.HarnessVersion, applied, rec.ModelID)
	} else {
		body := []string{t.Head}
		if rec.Class == "foreign" {
			body = append(body, "af keeps using the declared window for this profile; verify the gateway applies it.")
		}
		if rec.Probed > applied {
			body = append(body, fmt.Sprintf("The probe measured %d on Claude Code %s for this model, so the harness is not the cause: check the env block of the user-scope Claude Code settings.json, which af up compares with the profile.", rec.Probed, rec.HarnessVersion))
		}
		t.Body = strings.Join(body, "\n")
	}

	third := "    Do one of:"
	if applied == 200000 {
		third = "    The session would auto-compact near 170k. Do one of:"
	}
	path := windowRecordRelPath(rec.Profile)
	record := fmt.Sprintf("    Record: %s (once the cause is fixed, removing this file resets it)", path)
	holders := windowHolders(rec)
	if !probe {
		record = fmt.Sprintf("    Record: %s (held by the last report of %s; each agent's next session on this identity replaces its own figure; once the cause is fixed, removing this file resets it)", path, holders)
	}
	t.Refusal = strings.Join([]string{
		t.Head,
		third,
		"      - install a Claude Code version that knows this model, then launch again (re-measured automatically)",
		fmt.Sprintf("      - lower the profile's CLAUDE_CODE_MAX_CONTEXT_TOKENS to %d (af config models set)", applied),
		"      - launch anyway: --accept-window",
		record,
	}, "\n")
	if probe {
		t.Summary = fmt.Sprintf("agent %q: profile %q declares a %d-token window, but Claude Code %s applied %d to model %q; refused (launch anyway: --accept-window; record: %s)",
			agent, rec.Profile, rec.Declared, rec.HarnessVersion, applied, rec.ModelID, path)
	} else {
		t.Summary = fmt.Sprintf("agent %q: profile %q declares a %d-token window, but Claude Code %s applied %d to model %q; refused (held by the last report of %s, replaced at each one's next session on this identity; launch anyway: --accept-window; record: %s)",
			agent, rec.Profile, rec.Declared, rec.HarnessVersion, applied, rec.ModelID, holders, path)
	}
	return t
}

func formatWindowUnobserved(agent string, fact windowLaunchFact, cause string) windowText {
	return windowText{
		Subject: fmt.Sprintf("WINDOW_MISMATCH: %s on profile %s", agent, fact.Profile),
		Body: fmt.Sprintf("agent %q: profile %q declares a %d-token window on Claude Code %s, and the session launched at %s has reported no window (%s).",
			agent, fact.Profile, fact.Declared, fact.HarnessVersion, fact.LaunchedAt.Format(time.RFC3339), cause),
	}
}

func formatWindowBound(root, agent string, rec windowRecord, bound, applied int64) windowText {
	return windowText{
		Subject: fmt.Sprintf("WINDOW_MISMATCH: %s on profile %s", agent, rec.Profile),
		Body: fmt.Sprintf("agent %q: step_context.bound_tokens is %d, above the %d-token window Claude Code %s applied to model %q on profile %q; steps are judged against %d.",
			agent, bound, applied, rec.HarnessVersion, rec.ModelID, rec.Profile, applied),
	}
}
