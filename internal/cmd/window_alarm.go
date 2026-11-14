package cmd

import (
	"fmt"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

const (
	windowReasonMismatch   = "mismatch"
	windowReasonUnobserved = "unobserved"
	windowReasonAccepted   = "accepted"
	windowReasonForeign    = "foreign"
)

func windowJoin(root, agent string, now time.Time) (fact windowLaunchFact, rec windowRecord, ok bool) {
	fact, ok = readWindowLaunchFact(root, agent)
	if !ok {
		return windowLaunchFact{}, windowRecord{}, false
	}
	rec, ok = readWindowRecord(root, fact.identity(), now)
	return fact, rec, ok
}

func windowAlarms(root string, now time.Time) []recoveryAlarm {
	var kept []string
	for _, agent := range listWindowFacts(root) {
		if config.ValidateAgentName(agent) == nil {
			kept = append(kept, agent)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	if cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root)); err == nil {
		roster := kept[:0]
		for _, agent := range kept {
			if _, ok := cfg.Agents[agent]; ok {
				roster = append(roster, agent)
			}
		}
		kept = roster
	}
	var raised []recoveryAlarm
	for _, agent := range kept {
		fact, rec, ok := windowJoin(root, agent, now)
		if !ok {
			continue
		}
		fig, has := rec.launchFigure(agent, fact.LaunchedAt)
		switch {
		case has && fig > 0 && fact.Declared > 0 && fig != fact.Declared:
			reason := windowReasonMismatch
			if fact.Class == "foreign" {
				reason = windowReasonForeign
			} else if fact.AcceptWindow {
				reason = windowReasonAccepted
			}
			raised = append(raised, recoveryAlarm{class: alarmClassWindow, agent: agent, reason: reason,
				detail: formatWindowText(root, agent, rec, fig).Head, above: fig > fact.Declared})
		case rec.UnobservedNotified && !has && now.Sub(fact.LaunchedAt) > windowDarkGrace && agentSessionLive(root, agent):
			raised = append(raised, recoveryAlarm{class: alarmClassWindow, agent: agent, reason: windowReasonUnobserved,
				detail: fmt.Sprintf("profile %q declares a %d-token window on Claude Code %s, and the session has reported no window since launch.",
					fact.Profile, fact.Declared, fact.HarnessVersion)})
		}
	}
	return raised
}

func windowAgentFigures(root, agent string, now time.Time) (declared, applied int64, ok bool) {
	fact, rec, joined := windowJoin(root, agent, now)
	if !joined {
		return 0, 0, false
	}
	fig, has := rec.launchFigure(agent, fact.LaunchedAt)
	if !has || fact.Declared <= 0 || fig <= 0 || fig == fact.Declared {
		return 0, 0, false
	}
	return fact.Declared, fig, true
}

func windowClearingAct(reason, agent string, above bool) string {
	sess := session.SessionName(agent)
	switch reason {
	case windowReasonMismatch, windowReasonAccepted:
		if above {
			return fmt.Sprintf("Clears when this agent's session reports the declared window, or at its next launch on another identity: change the profile's CLAUDE_CODE_MAX_CONTEXT_TOKENS to the window the host applies (af config models set), then relaunch it from an operator shell with 'tmux kill-session -t =%s' and 'af up %s'; editing models.json alone ends nothing.", sess, agent)
		}
		return fmt.Sprintf("Clears when this agent's session reports the declared window, or at its next launch on another identity: install a Claude Code version that knows the model or change the profile's CLAUDE_CODE_MAX_CONTEXT_TOKENS (af config models set), then relaunch it from an operator shell with 'tmux kill-session -t =%s' and 'af up %s'; editing models.json alone ends nothing.", sess, agent)
	case windowReasonForeign:
		return fmt.Sprintf("Clears when this agent's session reports the declared window, or at its next launch on another identity: correct the profile's CLAUDE_CODE_MAX_CONTEXT_TOKENS (af config models set), then relaunch it from an operator shell with 'tmux kill-session -t =%s' and 'af up %s'.", sess, agent)
	case windowReasonUnobserved:
		return "Clears when this agent's session reports a window (turn the statusline on with 'af statusline on' if it is off), when its session ends, or at its next launch on another identity."
	}
	return ""
}
