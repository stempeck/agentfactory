package cmd

import (
	"fmt"
	"io"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/statusline"
)

const windowDarkGrace = 10 * time.Minute

type windowCallSite int

const (
	windowAtTick windowCallSite = iota
	windowAtLaunch
	windowAtStatus
)

type windowDetectInput struct {
	Site     windowCallSite
	Now      time.Time
	Live     map[string]liveProfile
	ReadOnly bool
	Warn     io.Writer
}

type windowFinding struct {
	Agent, Profile, ModelID, HarnessVersion string
	Declared, Applied                       int64
	State                                   string
	Notified                                bool
	UnobservedCause                         string
}

type windowReport struct{ Findings []windowFinding }

var detectWindowsFn = detectWindows

// windowPassState is the memo one pass shares: a tick, one af up, one status run. It holds the
// version, one roster load and one ReadObservations, so af up of N agents does not repeat them N
// times. It belongs to one factory root, so a call for another root starts a new pass.
type windowPassState struct {
	root          string
	version       string
	versionRead   bool
	roster        *config.AgentConfig // nil when agents.json failed to load
	rosterRead    bool
	readings      map[string]statusline.ChannelReading
	readingsRead  bool
	launchSwept   bool
	versionWarned bool
}

var windowPass windowPassState

func resetWindowPass() { windowPass = windowPassState{} }

func windowPassFor(root string) {
	if windowPass.root != root {
		resetWindowPass()
		windowPass.root = root
	}
}

func windowHarnessVersion(root string) string {
	windowPassFor(root)
	if !windowPass.versionRead {
		windowPass.version = harnessVersionOf(claudeCodeVersionFn())
		windowPass.versionRead = true
	}
	return windowPass.version
}

func windowRoster(root string) *config.AgentConfig {
	windowPassFor(root)
	if !windowPass.rosterRead {
		if cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root)); err == nil {
			windowPass.roster = cfg
		}
		windowPass.rosterRead = true
	}
	return windowPass.roster
}

// windowReadings is loaded on first use only: item 5 is its one reader, and a pass with no live
// agent lacking a launch fact never needs it. Its scope is the whole roster, because a reading
// depends only on the snapshots of its own agent.
func windowReadings(root string, startupCfg *config.StartupConfig, now time.Time) map[string]statusline.ChannelReading {
	windowPassFor(root)
	if !windowPass.readingsRead {
		scope := map[string]struct{}{}
		if roster := windowRoster(root); roster != nil {
			for agent := range roster.Agents {
				scope[agent] = struct{}{}
			}
		}
		windowPass.readings, _ = statusline.ReadObservations(config.StatuslineSessionsDir(root), statusline.ReadOptions{
			KnownAgents: scope,
			Staleness:   time.Duration(startupCfg.Recovery.StalenessSecs) * time.Second,
			DarkAfter:   time.Duration(startupCfg.Recovery.DarkGraceSecs) * time.Second,
		}, now)
		windowPass.readingsRead = true
	}
	return windowPass.readings
}

func detectWindows(root string, in windowDetectInput) windowReport {
	windowPassFor(root)
	if in.Site == windowAtLaunch {
		if windowPass.launchSwept {
			return windowReport{}
		}
		windowPass.launchSwept = true
	}
	startupCfg, err := config.LoadStartupConfig(root)
	if err != nil {
		return windowReport{}
	}
	known := map[string]bool{}
	for _, a := range listWindowFacts(root) {
		known[a] = true
	}
	if roster := windowRoster(root); roster != nil {
		for a := range roster.Agents {
			known[a] = true
		}
	}
	var rep windowReport
	legacy := map[string]liveProfile{}
	for _, agent := range sortedMapKeys(known) {
		if config.ValidateAgentName(agent) != nil {
			continue
		}
		fact, ok := readWindowLaunchFact(root, agent)
		if !ok {
			if lp, given := in.Live[agent]; given {
				legacy[agent] = lp
			}
			continue
		}
		rep.Findings = append(rep.Findings, detectAgentWindow(root, agent, fact, startupCfg, in)...)
	}
	rep.Findings = append(rep.Findings, legacyWindowDrift(root, legacy, startupCfg, in.Now)...)
	return rep
}

func windowReadOptions(agent string, startupCfg *config.StartupConfig) statusline.ReadOptions {
	return statusline.ReadOptions{
		KnownAgents: map[string]struct{}{agent: {}},
		Staleness:   time.Duration(startupCfg.Recovery.StalenessSecs) * time.Second,
		DarkAfter:   time.Duration(startupCfg.Recovery.DarkGraceSecs) * time.Second,
	}
}

// windowObservation is invariant 2: the agent's observation counts only when both its session id
// file and the snapshot that session wrote are later than the launch the fact describes.
func windowObservation(root, agent string, fact windowLaunchFact, startupCfg *config.StartupConfig, now time.Time) (sid string, total int64, ok bool) {
	sid, writtenAt, hasSID := readCurrentSessionID(resolveAgentDir(root, agent))
	if !hasSID || !writtenAt.After(fact.LaunchedAt) {
		return sid, 0, false
	}
	reading := statusline.SessionObservation(config.StatuslineSessionsDir(root), sid, windowReadOptions(agent, startupCfg), now)
	obs, has := reading.Observation()
	if !has || !obs.WrittenAt().After(fact.LaunchedAt) || obs.TokensTotal() <= 0 {
		return sid, 0, false
	}
	return sid, obs.TokensTotal(), true
}

func windowUnobservedCause(root, agent, sid string, startupCfg *config.StartupConfig, now time.Time) string {
	if !statuslineFactoryEnabled(root) || !agentSettingsHasStatusLine(resolveAgentDir(root, agent)) {
		return "statusline gate off"
	}
	if sid != "" {
		if statusline.SessionObservation(config.StatuslineSessionsDir(root), sid, windowReadOptions(agent, startupCfg), now).State() == statusline.StateMalformed {
			return "snapshot without a window"
		}
	}
	return "no snapshot yet"
}

func hasWindowPair(pairs []windowPair, declared, applied int64) bool {
	for _, p := range pairs {
		if p.Declared == declared && p.Applied == applied {
			return true
		}
	}
	return false
}

func hasBoundPair(pairs []boundPair, bound, applied int64) bool {
	for _, p := range pairs {
		if p.Bound == bound && p.Applied == applied {
			return true
		}
	}
	return false
}

func detectAgentWindow(root, agent string, fact windowLaunchFact, startupCfg *config.StartupConfig, in windowDetectInput) []windowFinding {
	id := fact.identity()
	if id.key() == "" {
		return nil
	}
	base := windowFinding{Agent: agent, Profile: fact.Profile, ModelID: fact.ModelID, HarnessVersion: fact.HarnessVersion, Declared: fact.Declared}
	sid, h, observed := windowObservation(root, agent, fact, startupCfg, in.Now)
	if observed {
		return detectObservedWindow(root, agent, fact, id, h, int64(startupCfg.StepContext.BoundTokens), base, in)
	}
	if fact.Class == "foreign" || fact.Declared <= 0 || in.Now.Sub(fact.LaunchedAt) <= windowDarkGrace || !agentSessionLive(root, agent) {
		return nil
	}
	cause := windowUnobservedCause(root, agent, sid, startupCfg, in.Now)
	f := base
	f.State, f.UnobservedCause = "unobserved", cause
	if in.ReadOnly {
		rec, exists := readWindowRecord(root, id, in.Now)
		if exists {
			if _, has := rec.launchFigure(agent, fact.LaunchedAt); has {
				return nil
			}
		}
		f.Notified = exists && rec.UnobservedNotified
		return []windowFinding{f}
	}
	figured, send := false, false
	updateWindowRecordOrWarn(root, id, in.Now, in.Warn, func(rec *windowRecord, exists bool) bool {
		if _, has := rec.launchFigure(agent, fact.LaunchedAt); has {
			figured = true
			return false
		}
		f.Notified = true
		send = !rec.UnobservedNotified
		return !send && in.Now.Sub(rec.LastSeen) > windowLastSeenRefresh
	})
	if figured {
		return nil
	}
	// The mark here and the pairs in detectObservedWindow are recorded under a second claim, after
	// the send: one recorded first would outlive a process killed before its mail went out, while two
	// passes deciding at once cost at most a duplicate mail.
	if send {
		sendWindowMail(root, agent, formatWindowUnobserved(agent, fact, cause))
		updateWindowRecordOrWarn(root, id, in.Now, in.Warn, func(rec *windowRecord, exists bool) bool {
			if rec.UnobservedNotified {
				return false
			}
			rec.UnobservedNotified = true
			return true
		})
	}
	return []windowFinding{f}
}

func detectObservedWindow(root, agent string, fact windowLaunchFact, id windowIdentity, h, bound int64, base windowFinding, in windowDetectInput) []windowFinding {
	f := base
	f.Applied = h
	switch {
	case fact.Declared == 0:
		f.State = "first-observation"
	case h != fact.Declared:
		f.State = "mismatch"
	}
	if in.ReadOnly {
		if f.State == "" {
			return nil
		}
		rec, exists := readWindowRecord(root, id, in.Now)
		f.Notified = exists && hasWindowPair(rec.NotifiedFor, fact.Declared, h)
		return []windowFinding{f}
	}
	var mails []windowText
	mismatchMailed, boundMailed := false, false
	updateWindowRecordOrWarn(root, id, in.Now, in.Warn, func(rec *windowRecord, exists bool) bool {
		write := false
		prev, had := rec.Agents[agent]
		sameLaunch := had && prev.LaunchedAt.Equal(fact.LaunchedAt)
		if !sameLaunch {
			rec.Sessions++
		}
		if !sameLaunch || prev.Applied != h || in.Now.Sub(prev.LastSeen) > windowLastSeenRefresh {
			write = true
		}
		if rec.Agents == nil {
			rec.Agents = map[string]agentWindow{}
		}
		rec.Agents[agent] = agentWindow{Applied: h, LaunchedAt: fact.LaunchedAt, LastSeen: in.Now}
		if rec.Class == "" && fact.Class != "" {
			rec.Class, write = fact.Class, true
		}
		rec.ModelID, rec.HarnessVersion, rec.Declared = id.ModelID, id.HarnessVersion, id.Declared
		if !exists {
			rec.FirstSeen = in.Now
		}
		recomputeWindowRecord(rec)
		if f.State != "" && !hasWindowPair(rec.NotifiedFor, fact.Declared, h) {
			mails = append(mails, formatWindowText(root, agent, *rec, h))
			mismatchMailed = true
		}
		f.Notified = true
		if bound > h && !hasBoundPair(rec.BoundNotifiedFor, bound, h) {
			mails = append(mails, formatWindowBound(root, agent, *rec, bound, h))
			boundMailed = true
		}
		return write
	})
	for _, t := range mails {
		sendWindowMail(root, agent, t)
	}
	if len(mails) > 0 {
		updateWindowRecordOrWarn(root, id, in.Now, in.Warn, func(rec *windowRecord, exists bool) bool {
			if !exists {
				return false
			}
			write := false
			if mismatchMailed && !hasWindowPair(rec.NotifiedFor, fact.Declared, h) {
				rec.NotifiedFor = append(rec.NotifiedFor, windowPair{Declared: fact.Declared, Applied: h})
				write = true
			}
			if boundMailed && !hasBoundPair(rec.BoundNotifiedFor, bound, h) {
				rec.BoundNotifiedFor = append(rec.BoundNotifiedFor, boundPair{Bound: bound, Applied: h})
				write = true
			}
			return write
		})
	}
	if f.State == "" {
		return nil
	}
	return []windowFinding{f}
}

// legacyWindowDrift is item 5: a live agent with no launch fact is compared with the mapping as it
// stands now, healthy readings only, and is only ever printed.
func legacyWindowDrift(root string, live map[string]liveProfile, startupCfg *config.StartupConfig, now time.Time) []windowFinding {
	if len(live) == 0 {
		return nil
	}
	if startupCfg.Recovery.StalenessSecs <= 0 || startupCfg.Recovery.DarkGraceSecs <= 0 {
		return nil
	}
	readings := windowReadings(root, startupCfg, now)
	var out []windowFinding
	for _, agent := range sortedMapKeys(live) {
		declared, ok := statuslineDeclaredWindow(live[agent])
		if !ok {
			continue
		}
		// IsHealthy, not the Observation() bool: a stale and even a dark reading still carries a
		// datum, so gating on the bool alone would advise using a number the host last reported
		// hours ago.
		reading := readings[agent]
		if !reading.IsHealthy() {
			continue
		}
		obs, has := reading.Observation()
		if !has {
			continue
		}
		// The reader does not clamp this field, so a hand-planted file must not be able to
		// manufacture a warning out of a zero.
		reported := obs.TokensTotal()
		if reported <= 0 || uint64(reported) == declared {
			continue
		}
		out = append(out, windowFinding{Agent: agent, Profile: live[agent].name, Declared: int64(declared), Applied: reported, State: "legacy-drift"})
	}
	return out
}

// updateWindowRecordOrWarn says when the write fails: an unwritable record hides the divergence from
// every reader, so the pass's mail and this line are all that report it.
func updateWindowRecordOrWarn(root string, id windowIdentity, now time.Time, warn io.Writer, mutate func(rec *windowRecord, exists bool) bool) {
	if err := updateWindowRecord(root, id, now, mutate); err != nil && warn != nil {
		fmt.Fprintf(warn, "warning: window record for profile %q could not be written (%v)\n", id.Profile, err)
	}
}

func sendWindowMail(root, agent string, t windowText) {
	seen := map[string]bool{}
	for _, to := range []string{escalationManager, escalationTarget} {
		if seen[to] {
			continue
		}
		seen[to] = true
		escalateOperator(root, to, alarmClassWindow, t.Subject, t.Body)
	}
}

// agentSessionLive asks tmux alone, so an agent whose model resolution selects no profile still
// counts as running.
func agentSessionLive(root, agent string) bool {
	tmux := newCmdTmux()
	name := session.SessionName(agent)
	running, _ := tmux.HasSession(name)
	return running && !sessionForeignRoot(tmux, name, root)
}
