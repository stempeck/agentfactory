package cmd

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// K7 (#602 Phase 3) coverage for the two advisories `af statusline status` appends: the
// companion-vs-reported drift join and the live-profile pairing lint.
//
// No test in this file may call t.Parallel. setupConfigFactory calls t.Chdir, which Go 1.24
// panics on in a parallel test, and advisoryRoot reassigns the package-global newCmdTmux seam
// through installFakeTmuxPresent. Note the source scanner will NOT catch a violation here:
// seamReassignPattern (tmux_isolation_enforce_test.go:50) matches the literal assignment, which
// lives in agents_test.go, so this file is never flagged as seam-mutating. The rule is manual.
//
// Every test here must install the tmux fake even when it asserts SILENCE. Under the default
// test build the real client's HasSession returns (false, nil) with no exec (tmux.go:251-253),
// so without the fake the live scope is empty, the join is never entered, and a "no advisory"
// assertion passes for entirely the wrong reason. Each negative test therefore also carries a
// positive control inside the SAME captured stream.

// freshSnapshotAge keeps a planted snapshot inside the 180s default staleness tolerance
// (config.defaultRecoveryConfig, startup.go:92) so ObservedReading classifies it fresh.
const freshSnapshotAge = 10 * time.Second

// advisoryRoot assembles every leg of the three-source join EXCEPT the occupancy snapshots:
// a roster, the statusline gate, a validated models.json, and a live non-foreign tmux session
// per named agent. Callers that want a leg missing omit it deliberately.
func advisoryRoot(t *testing.T, models *config.ModelsConfig, live ...string) (string, *fakeTmux) {
	t.Helper()
	root := setupConfigFactory(t) // roster: debugger (autonomous) + manager (interactive)
	if err := os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write gate: %v", err)
	}
	if models != nil {
		writeValidModels(t, root, models)
	}
	sessions := make([]string, 0, len(live))
	for _, a := range live {
		// Derived, never hardcoded as "af-<agent>": session.SessionName goes through the
		// sessionPrefixFn seam, which setupHermeticSessions rewrites (hermetic_test.go:192).
		sessions = append(sessions, session.SessionName(a))
	}
	return root, installFakeTmuxPresent(t, sessions...)
}

// advisoryFactory is advisoryRoot plus a FRESH occupancy snapshot for each live agent — the
// complete fixture in which either advisory can speak. plantSnapshot hardcodes
// context_tokens_total to 200000 (recovery_test.go:174), which is the host-reported side of
// every drift comparison below.
func advisoryFactory(t *testing.T, models *config.ModelsConfig, live ...string) (string, *fakeTmux) {
	t.Helper()
	root, fake := advisoryRoot(t, models, live...)
	for _, a := range live {
		plantAdvisorySnapshot(t, root, a, freshSnapshotAge)
	}
	return root, fake
}

// plantAdvisorySnapshot writes one snapshot for agent, aged relative to time.Now() because the
// join reads the wall clock (ObservedReading classifies by age, observation.go:192-236).
func plantAdvisorySnapshot(t *testing.T, root, agent string, age time.Duration) {
	t.Helper()
	now := time.Now()
	plantSnapshot(t, root, agent, "sess"+agent, 50, now.Add(-age), now, testRecoveryConfig())
}

// statusStdout drives the status verb end-to-end. The four Phase-3 tests are required to be
// verb-level: the package idiom of calling each note helper directly would satisfy the greps
// but leave the wiring into printStatuslineStatus untested.
func statusStdout(t *testing.T) string {
	t.Helper()
	return captureStdout(t, func() {
		if err := runStatusline(statuslineCmd, []string{"status"}); err != nil {
			t.Fatalf("status must exit 0, got: %v", err)
		}
	})
}

// findLineWith returns the first output line containing every needle, or "".
func findLineWith(out string, needles ...string) string {
	for _, line := range strings.Split(out, "\n") {
		all := true
		for _, n := range needles {
			if !strings.Contains(line, n) {
				all = false
				break
			}
		}
		if all {
			return line
		}
	}
	return ""
}

// driftProfile is the canonical drift fixture: a foreign model id whose operator-declared real
// context window (220000) disagrees with the 200000 the host reports. Its pairing is
// deliberately COHERENT — the auto-compact window sits at 180000, below the 200000 cap, so
// PairingLintProfile stays silent (models.go:224) and the literal "200000" in the output can
// only have come from the drift advisory, never from the pairing lint's cap text.
func driftProfile() map[string]string {
	return map[string]string{
		"ANTHROPIC_MODEL":                 "gpt-5.6-sol",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "180000",
		"CLAUDE_CODE_MAX_CONTEXT_TOKENS":  "220000",
	}
}

// --- AC-2: the drift advisory -------------------------------------------------------------

// TestStatuslineStatus_DriftAdvisory is the three-source join's contract. The operator declares
// the backend's real window as 220000; the host reports 200000 in manager's snapshot. Those are
// the SAME quantity, so the disagreement is real drift: recovery divides by the host-reported
// figure, so a wrong denominator makes it fire early (churn) or never (blind).
func TestStatuslineStatus_DriftAdvisory(t *testing.T) {
	advisoryFactory(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": driftProfile()},
		Agents: map[string]string{"manager": "codex"},
	}, "manager")

	out := statusStdout(t)

	// Both numbers on ONE line: a blob-level Contains would be satisfied by two unrelated
	// lines. ("220000" does not contain "200000", so this is genuinely two assertions.)
	advisory := findLineWith(out, "220000", "200000")
	if advisory == "" {
		t.Fatalf("status must name BOTH the declared (220000) and the host-reported (200000)\n"+
			"context window on one advisory line; got:\n%s", out)
	}
	for _, want := range []string{"manager", "codex"} {
		if !strings.Contains(advisory, want) {
			t.Errorf("advisory %q must name %q so the operator knows which agent/profile to fix", advisory, want)
		}
	}
	if first := strings.SplitN(out, "\n", 2)[0]; first != "statusline: on" {
		t.Errorf("advisories must append, never precede the gate line; first line = %q", first)
	}
}

// TestStatuslineStatus_MultipleDriftsSorted pins deterministic output. Go map iteration is
// randomised, so an unsorted implementation reorders between runs and every downstream grep
// becomes flaky (the Phase-1 precedent, config_set.go:269-274, sorts for the same reason).
//
// Two design choices make this discriminating rather than merely non-vacuous. EIGHT agents, not
// two: the live map is populated in sorted order, and Go's swissmap iteration is strongly biased
// toward insertion order for tiny maps, so a two-entry fixture comes out sorted by luck most of
// the time even with the sort removed. And the whole verb is re-run repeatedly: one run of an
// unsorted implementation can still look sorted, a run of sorted ones cannot look otherwise.
func TestStatuslineStatus_MultipleDriftsSorted(t *testing.T) {
	agents := []string{"hotel", "alpha", "golf", "bravo", "foxtrot", "charlie", "echo", "delta"}
	want := append([]string(nil), agents...)
	sort.Strings(want)

	root, _ := advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"drift": {"ANTHROPIC_MODEL": "grok-9-fast", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "230000"},
		},
		Agents: map[string]string{}, // the roster below routes every agent through entry.Model
	}, agents...)

	var roster strings.Builder
	roster.WriteString(`{"agents":{`)
	for i, a := range agents {
		if i > 0 {
			roster.WriteString(",")
		}
		fmt.Fprintf(&roster, `%q:{"type":"autonomous","description":"d","model":"drift"}`, a)
	}
	roster.WriteString(`}}`)
	writeAgentsJSON(t, root, roster.String())

	for _, a := range agents {
		plantAdvisorySnapshot(t, root, a, freshSnapshotAge)
	}

	for run := 0; run < 25; run++ {
		out := statusStdout(t)
		var got []string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, `profile "drift"`) {
				got = append(got, strings.TrimSpace(strings.SplitN(line, ":", 2)[0]))
			}
		}
		if len(got) != len(want) {
			t.Fatalf("run %d: want %d drift lines, got %d:\n%s", run, len(want), len(got), out)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("run %d: advisory lines must be sorted by agent name\n got: %v\nwant: %v", run, got, want)
		}
	}
}

// --- AC-3: the CRIT-2 false-positive regression ---------------------------------------------

// TestStatuslineStatus_BelowWindowNoAdvisory is the regression test for the comparison that
// cross-review CRIT-2 killed (.designs/602/cross-review/analyst-review-design.md:62-88).
// Declaring a 150000 auto-compact window on a 200000-window Claude model means "compact early
// on purpose" — the lever's PRIMARY legitimate use. Comparing that threshold against the full
// model window would false-positive forever on a correctly configured factory.
//
// `debugger` carries a real companion drift and MUST be named: it proves the join actually ran
// in this very invocation and on this very captured stream. Without that positive control,
// manager's silence would be indistinguishable from an empty tmux scope, an unresolved profile,
// or an advisory written to a stream captureStdout cannot see.
func TestStatuslineStatus_BelowWindowNoAdvisory(t *testing.T) {
	advisoryFactory(t, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"tight": {"ANTHROPIC_MODEL": "claude-opus-4-8", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "150000"},
			"drift": {"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "220000"},
		},
		Agents: map[string]string{"manager": "tight", "debugger": "drift"},
	}, "manager", "debugger")

	out := statusStdout(t)

	if findLineWith(out, "debugger", "220000", "200000") == "" {
		t.Fatalf("positive control failed: the genuinely drifting agent must be named, else this\n"+
			"test's silence proves nothing about the below-window case. got:\n%s", out)
	}
	// Assert the absence of the fixture's OWN data — an advisory could not omit these — rather
	// than the absence of a word like "warning", which any rewording would satisfy.
	for _, forbidden := range []string{"manager", "tight", "150000"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("CRIT-2: a below-window auto-compact declaration must produce SILENCE;\n"+
				"status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// --- AC-5: the pairing advisory ---------------------------------------------------------------

// TestStatuslineStatus_PairingAdvisory surfaces Phase 1's config-static lint for LIVE profiles:
// a foreign model id declaring more than the host's assumed 200000 window without the companion
// key silently caps and does nothing. A hand-edited models.json never passed through
// `af config models set`, so Phase 1's write-boundary warning never ran for it.
//
// The non-live "dormant" profile carries the identical incoherent shape and MUST stay unnamed —
// an implementation that lints every profile in models.json instead of the live ones passes a
// single-agent test identically.
func TestStatuslineStatus_PairingAdvisory(t *testing.T) {
	advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex":   {"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000"},
			"dormant": {"ANTHROPIC_MODEL": "grok-9-fast", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "300000"},
		},
		Agents: map[string]string{"manager": "codex", "debugger": "dormant"},
	}, "manager") // only manager is live; no snapshot planted — the lint needs none

	out := statusStdout(t)

	advisory := findLineWith(out, "codex", "CLAUDE_CODE_AUTO_COMPACT_WINDOW")
	if advisory == "" {
		t.Fatalf("status must surface the pairing lint for the live profile %q; got:\n%s", "codex", out)
	}
	for _, want := range []string{"220000", "gpt-5.6-sol", "200000", "CLAUDE_CODE_MAX_CONTEXT_TOKENS"} {
		if !strings.Contains(advisory, want) {
			t.Errorf("pairing advisory %q must name %q (PairingLintProfile owns the whole sentence)", advisory, want)
		}
	}
	for _, forbidden := range []string{"dormant", "grok-9-fast", "300000"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the pairing lint is scoped to LIVE profiles; status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// TestStatuslineStatus_PairingDedupedPerProfile: several live agents can share one profile. The
// warning describes the PROFILE, so printing it once per agent is noise.
func TestStatuslineStatus_PairingDedupedPerProfile(t *testing.T) {
	advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000"},
		},
		Agents: map[string]string{"manager": "codex", "debugger": "codex"},
	}, "manager", "debugger")

	out := statusStdout(t)

	if n := strings.Count(out, "which the host caps at"); n != 1 {
		t.Errorf("pairing warning must be emitted once per PROFILE, not once per agent; got %d occurrences:\n%s", n, out)
	}
}

// TestStatuslineStatus_PairingLinesSorted is the sort pin for the second advisory. It is keyed by
// PROFILE, not by agent, and the profile map is built by ranging the live map — so unlike the
// drift path, its insertion order is already randomised and an unsorted implementation has
// nowhere to hide. The agent→profile mapping is deliberately reversed so that sorting by agent
// would produce the wrong answer too.
func TestStatuslineStatus_PairingLinesSorted(t *testing.T) {
	models := map[string]map[string]string{}
	agentsToProfile := map[string]string{}
	for i := 1; i <= 6; i++ {
		profile := fmt.Sprintf("p%d", i)
		models[profile] = map[string]string{
			"ANTHROPIC_MODEL":                 fmt.Sprintf("grok-9-%d", i),
			"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000", // foreign id, >200k, no companion ⇒ capped no-op
		}
		agentsToProfile[fmt.Sprintf("a%d", i)] = fmt.Sprintf("p%d", 7-i)
	}
	live := []string{"a1", "a2", "a3", "a4", "a5", "a6"}
	want := []string{"p1", "p2", "p3", "p4", "p5", "p6"}

	root, _ := advisoryRoot(t, &config.ModelsConfig{Models: models, Agents: agentsToProfile}, live...)

	var roster strings.Builder
	roster.WriteString(`{"agents":{`)
	for i, a := range live {
		if i > 0 {
			roster.WriteString(",")
		}
		fmt.Fprintf(&roster, `%q:{"type":"autonomous","description":"d"}`, a)
	}
	roster.WriteString(`}}`)
	writeAgentsJSON(t, root, roster.String())

	for run := 0; run < 25; run++ {
		out := statusStdout(t)
		var got []string
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "pairing warning: ") {
				continue
			}
			// PairingLintProfile owns the sentence; its first quoted token is the profile name.
			if parts := strings.SplitN(line, `"`, 3); len(parts) >= 2 {
				got = append(got, parts[1])
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("run %d: pairing warnings must be sorted by profile name\n got: %v\nwant: %v\n%s",
				run, got, want, out)
		}
	}
}

// --- AC-4: nothing pre-existing changes -------------------------------------------------------

// TestStatuslineStatus_ExitCodeUnchanged runs against the FULL advisory fixture (so the new code
// path is genuinely exercised, not skipped) and pins that status still exits 0 and still prints
// every line it printed before. The load-bearing assertion is the first line: an advisory
// accidentally emitted before the gate line is the one realistic regression here.
func TestStatuslineStatus_ExitCodeUnchanged(t *testing.T) {
	root, _ := advisoryFactory(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": driftProfile()},
		Agents: map[string]string{"manager": "codex"},
	}, "manager")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runStatusline(statuslineCmd, []string{"status"})
	})
	if runErr != nil {
		t.Fatalf("status must always exit 0, got: %v", runErr)
	}
	if first := strings.SplitN(out, "\n", 2)[0]; first != "statusline: on" && first != "statusline: off" {
		t.Errorf("first status line = %q, want exactly \"statusline: on\" or \"statusline: off\"", first)
	}
	for _, prefix := range []string{"statusline: ", "elements: ", "self-test: ", "tokens: "} {
		if !strings.Contains(out, prefix) {
			t.Errorf("pre-existing status line %q missing after adding the advisories:\n%s", prefix, out)
		}
	}
	if findLineWith(out, "220000", "200000") == "" {
		t.Fatalf("fixture check: the advisory path must be live in this test, else it re-asserts\n"+
			"only what TestStatuslineStatus_FirstLineGrepContract already covers:\n%s", out)
	}

	// config.LoadAgentConfig returns ErrNotFound for an absent agents.json (config.go:147-152),
	// unlike LoadModelsConfig/LoadStartupConfig which degrade to empty/defaults. Propagating it
	// would make `af statusline status` start FAILING on a roster-less factory.
	t.Run("absent roster still exits 0", func(t *testing.T) {
		if err := os.Remove(config.AgentsConfigPath(root)); err != nil {
			t.Fatalf("remove agents.json: %v", err)
		}
		var err error
		out := captureStdout(t, func() {
			err = runStatusline(statuslineCmd, []string{"status"})
		})
		if err != nil {
			t.Fatalf("status must exit 0 with no roster, got: %v", err)
		}
		for _, prefix := range []string{"statusline: ", "elements: ", "self-test: ", "tokens: "} {
			if !strings.Contains(out, prefix) {
				t.Errorf("pre-existing status line %q missing on a roster-less factory:\n%s", prefix, out)
			}
		}
		if strings.Contains(out, "220000") {
			t.Errorf("no roster ⇒ no live agents ⇒ no advisory; got:\n%s", out)
		}
	})
}

// --- Silence rules ------------------------------------------------------------------------------

// TestStatuslineStatus_StaleSnapshotSilent is the gotcha none of the four required tests catch.
// A stale — and even a dark — ChannelReading STILL carries a datum, so reading.Observation()
// returns ok == true for it (observation.go:225, :235). An implementation gated on that bool
// instead of reading.IsHealthy() (observation.go:148) advises on a dead agent using numbers the
// host last reported minutes or hours ago.
func TestStatuslineStatus_StaleSnapshotSilent(t *testing.T) {
	root, _ := advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": driftProfile(), "drift": {
			"ANTHROPIC_MODEL": "grok-9-fast", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "230000",
		}},
		Agents: map[string]string{"manager": "codex", "debugger": "drift"},
	}, "manager", "debugger")

	plantAdvisorySnapshot(t, root, "manager", 5*time.Minute) // past the 180s staleness tolerance
	plantAdvisorySnapshot(t, root, "debugger", freshSnapshotAge)

	out := statusStdout(t)

	if findLineWith(out, "debugger", "230000", "200000") == "" {
		t.Fatalf("positive control failed: the fresh drifting agent must be named; got:\n%s", out)
	}
	for _, forbidden := range []string{"manager", "codex", "220000"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a stale snapshot must produce SILENCE (gate on IsHealthy, not Observation's bool);\n"+
				"status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// TestStatuslineStatus_AbsentSnapshotSilent: a live agent with a declaration but no snapshot at
// all reads StateNone — there is nothing to compare against, so there is nothing to say.
func TestStatuslineStatus_AbsentSnapshotSilent(t *testing.T) {
	root, _ := advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": driftProfile(), "drift": {
			"ANTHROPIC_MODEL": "grok-9-fast", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "230000",
		}},
		Agents: map[string]string{"manager": "codex", "debugger": "drift"},
	}, "manager", "debugger")

	plantAdvisorySnapshot(t, root, "debugger", freshSnapshotAge) // manager gets none

	out := statusStdout(t)

	if findLineWith(out, "debugger", "230000", "200000") == "" {
		t.Fatalf("positive control failed: the snapshot-bearing drifting agent must be named; got:\n%s", out)
	}
	for _, forbidden := range []string{"manager", "codex", "220000"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("an absent snapshot must produce SILENCE; status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// TestStatuslineStatus_DeclaredMatchesReportedSilent is the trivially-important negative: an
// implementation that warns whenever a companion merely EXISTS passes both required advisory
// tests, because the below-window fixture has no companion at all.
func TestStatuslineStatus_DeclaredMatchesReportedSilent(t *testing.T) {
	advisoryFactory(t, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"aligned": {"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "200000"},
			"drift":   {"ANTHROPIC_MODEL": "grok-9-fast", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "230000"},
		},
		Agents: map[string]string{"manager": "aligned", "debugger": "drift"},
	}, "manager", "debugger")

	out := statusStdout(t)

	if findLineWith(out, "debugger", "230000", "200000") == "" {
		t.Fatalf("positive control failed: the drifting agent must be named; got:\n%s", out)
	}
	for _, forbidden := range []string{"manager", "aligned"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("declared == reported is the CORRECT state and must be silent; status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// TestStatuslineStatus_NotLiveAgentSilent: the roster is not the scope. An agent configured but
// not running has no session the operator could act on.
func TestStatuslineStatus_NotLiveAgentSilent(t *testing.T) {
	root, _ := advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": driftProfile(), "drift": {
			"ANTHROPIC_MODEL": "grok-9-fast", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "230000",
		}},
		Agents: map[string]string{"manager": "codex", "debugger": "drift"},
	}, "debugger") // manager is NOT live

	plantAdvisorySnapshot(t, root, "manager", freshSnapshotAge)
	plantAdvisorySnapshot(t, root, "debugger", freshSnapshotAge)

	out := statusStdout(t)

	if findLineWith(out, "debugger", "230000", "200000") == "" {
		t.Fatalf("positive control failed: the live drifting agent must be named; got:\n%s", out)
	}
	for _, forbidden := range []string{"manager", "codex", "220000"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("an agent with no tmux session is not live and must be silent; status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// TestStatuslineStatus_ForeignRootExcluded: a session running against a DIFFERENT factory root
// belongs to that factory's operator, not this one (sessionForeignRoot, agents.go:257-263).
func TestStatuslineStatus_ForeignRootExcluded(t *testing.T) {
	root, fake := advisoryFactory(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": driftProfile(), "drift": {
			"ANTHROPIC_MODEL": "grok-9-fast", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "230000",
		}},
		Agents: map[string]string{"manager": "codex", "debugger": "drift"},
	}, "manager", "debugger")
	_ = root

	fake.env[session.SessionName("manager")] = map[string]string{"AF_ROOT": "/somewhere/else"}

	out := statusStdout(t)

	if findLineWith(out, "debugger", "230000", "200000") == "" {
		t.Fatalf("positive control failed: the same-root drifting agent must be named; got:\n%s", out)
	}
	for _, forbidden := range []string{"manager", "codex", "220000"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a foreign-rooted session must be out of scope; status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// --- Never-brick posture ------------------------------------------------------------------------

// TestStatuslineStatus_MalformedModelsSilent: a broken models.json degrades to silence, never to
// a failing status verb — the same never-brick posture as the respawn path
// (universe_hygiene_launch_test.go:60).
func TestStatuslineStatus_MalformedModelsSilent(t *testing.T) {
	root, _ := advisoryRoot(t, nil, "manager")
	writeRawModels(t, root, "{{{ not json")
	plantAdvisorySnapshot(t, root, "manager", freshSnapshotAge)

	var err error
	out := captureStdout(t, func() {
		err = runStatusline(statuslineCmd, []string{"status"})
	})
	if err != nil {
		t.Fatalf("a malformed models.json must not make status fail, got: %v", err)
	}
	for _, prefix := range []string{"statusline: ", "elements: ", "self-test: ", "tokens: "} {
		if !strings.Contains(out, prefix) {
			t.Errorf("pre-existing status line %q missing with a malformed models.json:\n%s", prefix, out)
		}
	}
}

// TestStatuslineStatus_InvalidCompanionSilent: a hand-edited companion value that
// LoadModelsConfig rejects (validateModelProfile, models.go:196-199) must also degrade to
// silence rather than surfacing a bogus "0 vs 200000" drift.
func TestStatuslineStatus_InvalidCompanionSilent(t *testing.T) {
	root, _ := advisoryRoot(t, nil, "manager")
	writeRawModels(t, root, `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol",`+
		`"CLAUDE_CODE_MAX_CONTEXT_TOKENS":" 220000"}},"agents":{"manager":"codex"}}`)
	plantAdvisorySnapshot(t, root, "manager", freshSnapshotAge)

	var err error
	out := captureStdout(t, func() {
		err = runStatusline(statuslineCmd, []string{"status"})
	})
	if err != nil {
		t.Fatalf("an invalid companion value must not make status fail, got: %v", err)
	}
	// This is the one silence test that cannot carry a positive control — a models.json rejected
	// at load leaves NO profile resolvable, so nothing in the run could speak. The four
	// pre-existing prefixes stand in for it: they prove status ran to completion rather than
	// dying somewhere in the join.
	for _, prefix := range []string{"statusline: ", "elements: ", "self-test: ", "tokens: "} {
		if !strings.Contains(out, prefix) {
			t.Errorf("pre-existing status line %q missing with an invalid companion value:\n%s", prefix, out)
		}
	}
	for _, forbidden := range []string{"220000", "codex"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("an unusable declaration must produce SILENCE; status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// TestStatuslineStatus_ForgedZeroTotalSilent guards the one branch the required four leave
// unprotected. The snapshot writer only records context_tokens_total when the host reported a
// positive window (daily.go:225-235), but the READER only checks the field is present, never that
// it is positive (observation.go:405-407, :428) — so a hand-planted or truncated file yields a
// valid Observation reporting 0. Without the reported > 0 guard, status would tell the operator
// their backend's window is zero.
func TestStatuslineStatus_ForgedZeroTotalSilent(t *testing.T) {
	root, _ := advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": driftProfile(), "drift": {
			"ANTHROPIC_MODEL": "grok-9-fast", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": "230000",
		}},
		Agents: map[string]string{"manager": "codex", "debugger": "drift"},
	}, "manager", "debugger")

	plantAdvisorySnapshot(t, root, "debugger", freshSnapshotAge) // control
	plantRawSnapshot(t, root, "sessmanager", `{"schema":2,"session_id":"sessmanager",`+
		`"agent":"manager","written_at":"`+time.Now().Add(-freshSnapshotAge).UTC().Format(time.RFC3339)+
		`","context_used_pct":50,"context_tokens_used":100000,"context_tokens_total":0}`)

	out := statusStdout(t)

	if findLineWith(out, "debugger", "230000", "200000") == "" {
		t.Fatalf("positive control failed: the genuinely drifting agent must be named; got:\n%s", out)
	}
	for _, forbidden := range []string{"manager", "codex", "220000", "reports 0"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("a non-positive reported window is not a reading to compare against;\n"+
				"status mentioned %q:\n%s", forbidden, out)
		}
	}
}

// --- Resolution routes and secrets ----------------------------------------------------------------

// TestStatuslineStatus_LegacyEntryModelResolves pins the discriminating resolution route. The
// outline specifies config.ResolveModelEnv(cfg, agent, "", "", entry.Model) — an implementation
// that drops entry.Model (agents.json's own "model" key, the legacyEntryModel argument) passes
// every models.json-"agents"-mapped test and fails this one.
func TestStatuslineStatus_LegacyEntryModelResolves(t *testing.T) {
	root, _ := advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": driftProfile()},
	}, "manager") // NOTE: no models.json "agents" mapping at all
	writeAgentsJSON(t, root, `{"agents":{`+
		`"debugger":{"type":"autonomous","description":"d"},`+
		`"manager":{"type":"interactive","description":"m","model":"codex"}}}`)
	plantAdvisorySnapshot(t, root, "manager", freshSnapshotAge)

	out := statusStdout(t)

	if findLineWith(out, "manager", "codex", "220000", "200000") == "" {
		t.Fatalf("the profile must resolve through agents.json's own \"model\" key\n"+
			"(ResolveModelEnv's legacyEntryModel argument); got:\n%s", out)
	}
}

// TestStatuslineStatus_NoProfileSecretsEchoed extends SEC-5
// (TestStatusline_RedirectPresenceOnly_ValueNeverEchoed, statusline_test.go:251) to the new
// advisories. A resolved profile map also carries ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN;
// the advisory must print names and the two token counts, never the profile itself.
func TestStatuslineStatus_NoProfileSecretsEchoed(t *testing.T) {
	advisoryFactory(t, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": {
			"ANTHROPIC_MODEL":                "gpt-5.6-sol",
			"ANTHROPIC_BASE_URL":             "http://127.0.0.1:4319",
			"ANTHROPIC_AUTH_TOKEN":           "sk-must-never-be-echoed",
			"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "220000",
		}},
		Agents: map[string]string{"manager": "codex"},
	}, "manager")

	out := statusStdout(t)

	if findLineWith(out, "manager", "codex", "220000", "200000") == "" {
		t.Fatalf("positive control failed: the drift advisory must still fire; got:\n%s", out)
	}
	for _, secret := range []string{"sk-must-never-be-echoed", "127.0.0.1:4319"} {
		if strings.Contains(out, secret) {
			t.Errorf("SEC-5: no statusline verb may echo a profile value; status leaked %q:\n%s", secret, out)
		}
	}
}

// TestStatuslineLiveProfiles_EndpointProfile_DerivedKeysLeaveAdvisoriesAlone covers the one
// resolver consumer that bypasses resolveLaunchModelEnv entirely (statusline.go:464). Its exports
// map gains the derived keys like every other consumer; what must NOT change is either advisory,
// because both read keys disjoint from the derived class set.
func TestStatuslineLiveProfiles_EndpointProfile_DerivedKeysLeaveAdvisoriesAlone(t *testing.T) {
	// One fixture cannot exercise both advisories: PairingLintProfile short-circuits to "" the
	// moment CLAUDE_CODE_MAX_CONTEXT_TOKENS is set (models.go:264-266), which is the very key
	// statuslineDeclaredWindow needs in order to report anything. Comparing derived-against-declared
	// on a single fixture therefore proves nothing for one of the two — it compares "" with "".
	// Each advisory gets the profile shape that makes it FIRE, and a positive control that fails
	// the test if it did not, so neither equality can pass vacuously.
	base := map[string]string{
		"ANTHROPIC_MODEL":                 "gpt-5.6-sol",
		"ANTHROPIC_BASE_URL":              "http://127.0.0.1:4319",
		"ANTHROPIC_AUTH_TOKEN":            "sk-must-never-be-echoed",
		"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000",
	}
	withWindow := map[string]string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "220000"}

	cases := []struct {
		name  string
		extra map[string]string
		// check runs both the positive control and the derived-vs-declared comparison.
		check func(t *testing.T, derived, declared liveProfile)
	}{
		{
			name:  "the declared-window advisory is unmoved by derived keys",
			extra: withWindow,
			check: func(t *testing.T, derived, declared liveProfile) {
				gotWin, gotOK := statuslineDeclaredWindow(derived)
				if !gotOK || gotWin != 220000 {
					t.Fatalf("positive control: this fixture must produce a declared window; got (%d,%v)", gotWin, gotOK)
				}
				wantWin, wantOK := statuslineDeclaredWindow(declared)
				if gotWin != wantWin || gotOK != wantOK {
					t.Errorf("the declared-window advisory reads %s only; derived class keys must not move it: got (%d,%v) want (%d,%v)", config.EnvMaxContextTokens, gotWin, gotOK, wantWin, wantOK)
				}
			},
		},
		{
			name:  "the pairing note is unmoved by derived keys",
			extra: nil, // no MAX_CONTEXT_TOKENS, so the lint actually fires
			check: func(t *testing.T, derived, declared liveProfile) {
				gotNote := statuslinePairingNote(map[string]liveProfile{"manager": derived})
				if gotNote == "" {
					t.Fatalf("positive control: this fixture must produce a pairing note, else the comparison below is \"\" == \"\"")
				}
				wantNote := statuslinePairingNote(map[string]liveProfile{"manager": declared})
				if gotNote != wantNote {
					t.Errorf("PairingLintProfile reads three non-class keys; a derived map must produce the same note:\n got  %q\n want %q", gotNote, wantNote)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := map[string]string{}
			for k, v := range base {
				profile[k] = v
			}
			for k, v := range tc.extra {
				profile[k] = v
			}
			root, _ := advisoryFactory(t, &config.ModelsConfig{
				Models: map[string]map[string]string{"codex": profile},
				Agents: map[string]string{"manager": "codex"},
			}, "manager")

			live := statuslineLiveProfiles(root)
			lp, ok := live["manager"]
			if !ok {
				t.Fatalf("fixture must produce a live profile for manager; got %v", live)
			}

			for _, key := range []string{
				"ANTHROPIC_SMALL_FAST_MODEL",
				"ANTHROPIC_DEFAULT_OPUS_MODEL",
				"ANTHROPIC_DEFAULT_SONNET_MODEL",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL",
			} {
				if lp.exports[key] != "gpt-5.6-sol" {
					t.Errorf("the statusline resolves the SAME set the launch does, so its exports map gains %s too; got %q", key, lp.exports[key])
				}
			}

			// The advisories read keys disjoint from the derived class set. Pin that by comparing
			// against the same map with the derived keys removed: the two must agree.
			declared := liveProfile{name: lp.name, exports: map[string]string{}}
			for k, v := range lp.exports {
				declared.exports[k] = v
			}
			for _, key := range []string{
				"ANTHROPIC_SMALL_FAST_MODEL",
				"ANTHROPIC_DEFAULT_OPUS_MODEL",
				"ANTHROPIC_DEFAULT_SONNET_MODEL",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL",
			} {
				delete(declared.exports, key)
			}
			tc.check(t, lp, declared)
		})
	}
}
