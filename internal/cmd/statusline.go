package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/statusline"
)

// statuslineCmd is the top-level operator/session surface for the factory-configurable
// statusline (issue #591, K4). It mirrors telemetry's single-command + switch-on-args[0]
// shape rather than cobra subcommands (D4), and is a SIBLING of `config` — the pre-existing
// `af config statusline set` (P1) edits the element list; this command enables/disables,
// inspects, and renders. The env-dependent decisions (root resolution, ANTHROPIC_BASE_URL
// presence) live here in the cmd layer; the internal/statusline library stays env-free
// (ADR-004).
var statuslineCmd = &cobra.Command{
	Use: "statusline [on|off|status|render]",

	Short: "Toggle, inspect, or render the Claude Code session statusline",
	Long: `Control and inspect the factory-configurable session statusline (issue #591).

  af statusline on|off      switch the factory-wide statusline gate
  af statusline status      gate state, configured elements, a live self-test, any agents
                            whose settings.json still lacks the statusLine key, and any live
                            agent whose declared context window disagrees with the host's
  af statusline render      the settings.json-invoked renderer: reads Claude Code's
                            statusline payload on stdin and prints the two-line status

render rides Claude Code's per-session hot path, so it ALWAYS exits 0 and is silent on
every failure (blank or partial output, never an error, never stderr noise). status runs
the same pipeline loudly, so a silent render failure is still diagnosable.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStatusline,
}

func init() {
	rootCmd.AddCommand(statuslineCmd)
}

// statuslineGateFile is the factory-level state file. Absent ⇒ off, matching telemetry and
// the improvement hook. (Unlike those, install --init seeds this on — but that seeding lives in
// runInstallInit, not here; creating nothing still reads as off.)
func statuslineGateFile(factoryRoot string) string {
	return filepath.Join(factoryRoot, ".agentfactory", ".statusline-gate")
}

// statuslineFactoryEnabled reports whether the factory-level gate file reads "on". Anything
// else — absent, unreadable, "off", or a near-miss — is off.
func statuslineFactoryEnabled(factoryRoot string) bool {
	data, err := os.ReadFile(statuslineGateFile(factoryRoot))
	return err == nil && strings.TrimSpace(string(data)) == "on"
}

// runStatusline dispatches the arg-switch. render is peeled off first: it owns its own root
// resolution and a panic backstop, and its exit-0 contract is absolute. status is read-only
// and downgrades a factory-root mismatch (dispatch-status posture, D3); on and off write
// state and report a mismatch instead, matching telemetry — a gate written to the wrong
// root is worse than an error.
func runStatusline(cmd *cobra.Command, args []string) error {
	if len(args) > 0 && args[0] == "render" {
		return runStatuslineRender(cmd)
	}

	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		if len(args) == 0 || args[0] == "status" {
			if r, ok := downgradeRootMismatch(err); ok {
				root = r
			} else {
				return err
			}
		} else {
			return err
		}
	}

	if len(args) == 0 || args[0] == "status" {
		return printStatuslineStatus(root)
	}

	switch args[0] {
	case "on":
		if err := os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("enabling statusline: %w", err)
		}
		fmt.Println("statusline: on")
	case "off":
		if err := os.WriteFile(statuslineGateFile(root), []byte("off\n"), 0644); err != nil {
			return fmt.Errorf("disabling statusline: %w", err)
		}
		fmt.Println("statusline: off")
	default:
		return fmt.Errorf("usage: af statusline [on|off|status|render]")
	}
	return nil
}

// runStatuslineRender is the settings.json-invoked renderer's RunE wrapper. It resolves the
// env-dependent inputs (root, the ANTHROPIC_BASE_URL-presence redirect boolean) and guards a
// terminal stdin, then hands a plain reader to the testable core. It NEVER hard-errors: a
// panic is recovered to nil (the only RunE-level recover in the tree — render is on Claude
// Code's hot path, so even an unexpected panic must not surface as noise or a non-zero exit),
// and a non-downgradable root error still returns nil.
func runStatuslineRender(cmd *cobra.Command) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = nil
		}
	}()

	wd, err := getWd()
	if err != nil {
		return nil
	}
	// io.Discard silences the two NIL-error warning paths inside the resolver (stale AF_ROOT,
	// nested-factory); silentRootDowngrade silences the error-RETURNING mismatch/enclosing
	// paths. Together they make the render root resolution emit NO stderr (C-5, PR #595 T9).
	root, err := resolveInvokerRootWarn(wd, io.Discard)
	if err != nil {
		if r, ok := silentRootDowngrade(err); ok {
			root = r
		} else {
			return nil // residual not-found ⇒ still exit 0; render never hard-errors (D3)
		}
	}

	// A statusline invoked on an interactive terminal (no piped payload) has nothing to
	// render and must never block reading a TTY (containment.go:85-93). The guard is applied
	// AFTER root resolution so the downgrade path stays reachable under `go test`, where
	// os.Stdin is /dev/null (itself a character device).
	if stat, statErr := os.Stdin.Stat(); statErr != nil || (stat.Mode()&os.ModeCharDevice) != 0 {
		return nil
	}

	redirect := os.Getenv(baseURLKey) != "" // presence only; the value is never read or printed (SEC-5)
	// AF_ROLE is read HERE, at the ADR-004 boundary, and flows down as a parameter so the
	// statusline library stays env-free (TestNoEnvReadsInLibraryPackages).
	//
	// Reading AF_ROLE directly for identity is the deviation idioms.md:146 names — the house rule
	// is resolveAgentName(cwd, factoryRoot). It is taken deliberately and narrowly: resolveAgentName
	// loads agents.json and can return an error, and render rides Claude Code's per-session hot path
	// where it must stay silent and do no config I/O (C-5). So the renderer stamps an UNVALIDATED
	// ambient claim, and the roster check moves to the occupancy reader — the trust boundary over
	// the agent-writable snapshot directory, which is where design-doc.md:179 puts validation
	// anyway. An empty or forged value costs at most one agent's channel reading dark, never healthy.
	agent := os.Getenv("AF_ROLE")
	return runStatuslineRenderCore(cmd.OutOrStdout(), root, cmd.InOrStdin(), agent, time.Now(), redirect)
}

// silentRootDowngrade is the render hot path's SILENT counterpart to downgradeRootMismatch
// (helpers.go). It recognizes the SAME factory-root-mismatch and enclosing-root errors and
// returns the same cwd/nested-resolved root to proceed on, but writes NOTHING to stderr. The
// render command is contractually silent on every failure (C-5: no error text reaches the pane),
// so it must not emit the diagnostic warning that the interactive read-only verbs — status,
// agents list, dispatch status, attach, formula show — rely on and keep (PR #595 T9). Both
// error branches are covered: silencing only the mismatch would leave the enclosing-root warning
// leaking to the pane.
func silentRootDowngrade(err error) (root string, ok bool) {
	var mm *rootMismatchError
	if errors.As(err, &mm) {
		return mm.resolved, true
	}
	var enc *enclosingRootError
	if errors.As(err, &enc) {
		return enc.resolved, true
	}
	return "", false
}

// statuslineBranchDir picks the directory whose git branch the statusline shows: the session's
// working directory from the payload (project_dir, then current_dir, then cwd — the same
// precedence the render lib's `dir` element uses), falling back to the factory root. For a
// worktree agent this is the worktree checkout, so the branch is the agent's WORKING branch —
// not the shared factory's, which resolveInvokerRoot resolves to via the .factory-root redirect.
func statuslineBranchDir(p statusline.Payload, root string) string {
	switch {
	case p.Workspace.ProjectDir != "":
		return p.Workspace.ProjectDir
	case p.Workspace.CurrentDir != "":
		return p.Workspace.CurrentDir
	case p.Cwd != "":
		return p.Cwd
	default:
		return root
	}
}

// runStatuslineRenderCore is the testable render core. It returns nil on EVERY path
// (containment.go:96-98, ADR-007): gate off, empty/oversized/malformed stdin, corrupt config,
// or an unwritable snapshot dir each degrade to a blank (or partial) render with no error and
// no stderr. The stdin cap is applied here (SEC-6) so a caller — or a test — that hands in an
// oversized reader is still bounded.
func runStatuslineRenderCore(out io.Writer, root string, in io.Reader, agent string, now time.Time, redirect bool) error {
	if !statuslineFactoryEnabled(root) {
		return nil
	}

	p, err := statusline.ParsePayload(io.LimitReader(in, 1<<20))
	if err != nil {
		return nil // empty / malformed / oversized-truncated ⇒ silent blank
	}

	branch := statusline.ReadBranch(statuslineBranchDir(p, root))
	sessionsDir := config.StatuslineSessionsDir(root)
	// The accumulator runs only if this write is admitted by the throttle, so transcript reads are
	// bound to the ≥10s write window rather than the render rate (scale.md S3.4).
	_ = statusline.WriteSnapshotWith(sessionsDir, p, agent, now, transcriptAccumulator(p.TranscriptPath)) // throttled + self-MkdirAll; an unwritable dir is non-fatal
	statusline.Prune(sessionsDir, now)
	daily := statusline.SumDaily(sessionsDir, now)
	sessionTokens := statusline.SessionTokens(sessionsDir, p.SessionID, now) // this session's cumulative counter (K2), just persisted above

	cfg, err := config.LoadStatuslineConfig(root)
	if err != nil {
		return nil // corrupt / unknown-element config ⇒ silent blank (status reports it loudly)
	}

	opts := statusline.RenderOpts{
		SessionTokens: sessionTokens,
		Color:         cfg.ColorEnabled() && os.Getenv(noColorKey) == "", // env-free library; the effective decision is computed HERE (redirect precedent)
		Redirect:      redirect,
	}
	if line := markStatuslineLines(scrubWatchdogNeedles(statusline.RenderWith(cfg, p, branch, daily, opts))); line != "" {
		fmt.Fprintln(out, line)
	}
	return nil
}

// noColorKey is the standard env kill-switch for a broken terminal: its PRESENCE (any value, per the
// no-color.org convention) forces colour off. Only presence is read, never the value — the redirect/
// ANTHROPIC_BASE_URL precedent. Read at the cmd layer so the render library stays env-free (SEC-5).
const noColorKey = "NO_COLOR"

// markStatuslineLines appends the watchdog sentinel to the END of every non-empty rendered line (K6),
// so checkSilence strips the whole statusline before hashing and a statusline-only pane change still
// trips silence. End-of-line, not line-start: tmux discards a zero-width rune written at column 0
// (watchdog.go EMISSION CONSTRAINT), and stripStatuslineLines matches by Contains so position is free.
// Added AFTER sanitize/scrub/colorize — the sentinel is two Cf runes that sanitize's Cf strip would
// otherwise eat. An empty render stays empty so the caller drops it.
func markStatuslineLines(rendered string) string {
	if rendered == "" {
		return ""
	}
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = line + statuslineSentinel
		}
	}
	return strings.Join(lines, "\n")
}

// zeroWidthSpace is inserted inside a watchdog needle to defeat a substring match while leaving
// the rendered token visually identical (U+200B is not a control rune, so it survives sanitize).
const zeroWidthSpace = "​"

// scrubWatchdogNeedles defuses any watchdog endpoint-failure needle that appears in LEGITIMATE
// rendered content — a branch or path literally named e.g. "unsupported_api_for_model", or a
// directory containing "504 Gateway Timeout". detectErrorPattern (watchdog.go) scans the whole
// 50-line pane with strings.Contains, so such content would trigger a FALSE endpoint-failure
// respawn of a healthy agent. It reuses the watchdog's OWN endpointFailureSignatures — the same
// package, so there is no second copy of the needle list to drift (ADR-004: the render CORE is
// package cmd, unlike the env-/cmd-free statusline library). Env-free (SEC-5). (PR #595 T4/F8.)
func scrubWatchdogNeedles(line string) string {
	if line == "" {
		return line
	}
	for _, sig := range endpointFailureSignatures {
		line = defuseNeedle(line, sig.needle)
	}
	return line
}

// defuseNeedle inserts a zero-width space after the first byte of every occurrence of needle
// (all watchdog needles are ASCII), so strings.Contains(line, needle) no longer holds while the
// pane still reads the same.
func defuseNeedle(line, needle string) string {
	if len(needle) < 2 || !strings.Contains(line, needle) {
		return line
	}
	return strings.ReplaceAll(line, needle, needle[:1]+zeroWidthSpace+needle[1:])
}

// printStatuslineStatus is the loud diagnostic counterpart to render. The FIRST line is the
// stable grep contract (statusline: on|off) and is printed BEFORE any load can fail
// (improvement.go:374-387). Unlike Pattern-7's early return, a config/pipeline failure is NOT
// swallowed here — the self-test line is where it is caught and NAMED, so the exact breakage
// that render hides is visible. "Return nil" governs only the exit code.
func printStatuslineStatus(root string) error {
	if statuslineFactoryEnabled(root) {
		fmt.Println("statusline: on")
	} else {
		fmt.Println("statusline: off")
	}

	cfg, cfgErr := config.LoadStatuslineConfig(root)
	if cfgErr == nil {
		fmt.Printf("elements: %s\n", strings.Join(cfg.Elements, ", "))
	} else {
		fmt.Println("elements: (unavailable — see self-test)")
	}

	fmt.Println(statuslineSelfTest(root, cfg, cfgErr))
	fmt.Println(statuslineTokenNote(root, time.Now()))

	if note := statuslineStalenessNote(root); note != "" {
		fmt.Print(note)
	}

	// K7 (#602). Appended LAST so the first-line grep contract above is untouched, and to
	// stdout like every other status line — a misconfiguration this verb diagnoses is
	// diagnostic content, not a failure of the verb, so it must not change the exit code.
	live := statuslineLiveProfiles(root)
	if note := statuslineDriftNote(root, live); note != "" {
		fmt.Print(note)
	}
	if note := statuslinePairingNote(live); note != "" {
		fmt.Print(note)
	}
	return nil
}

// statuslineSelfTestPayload is the built-in fixture the status self-test renders. It carries a
// value for every element family so a healthy pipeline yields a non-empty two-line render.
const statuslineSelfTestPayload = `{"session_id":"self-test","model":{"display_name":"self-test"},` +
	`"workspace":{"project_dir":"/self/test"},` +
	`"context_window":{"total_input_tokens":1000,"total_output_tokens":500,"context_window_size":200000,"used_percentage":25},` +
	`"cost":{"total_cost_usd":1.23,"total_duration_ms":65000,"total_lines_added":10,"total_lines_removed":2}}`

// statuslineSelfTest runs the REAL render pipeline over the built-in fixture with the real
// config and real snapshots, and NAMES any failure — the deliberate contrast with render,
// which stays silent on the same breakage (pinned by TestStatusSelfTest_ReportsFailure). A
// panic is caught and named too, so status can never itself become the silent failure.
func statuslineSelfTest(root string, cfg *config.StatuslineConfig, cfgErr error) (line string) {
	defer func() {
		if r := recover(); r != nil {
			line = fmt.Sprintf("self-test: FAILED — render panicked: %v", r)
		}
	}()

	if cfgErr != nil {
		return fmt.Sprintf("self-test: FAILED — config: %v", cfgErr)
	}
	p, err := statusline.ParsePayload(strings.NewReader(statuslineSelfTestPayload))
	if err != nil {
		return fmt.Sprintf("self-test: FAILED — built-in fixture parse: %v", err)
	}
	redirect := os.Getenv(baseURLKey) != ""
	branch := statusline.ReadBranch(statuslineBranchDir(p, root))
	sessionsDir := config.StatuslineSessionsDir(root)
	daily := statusline.SumDaily(sessionsDir, time.Now())
	if strings.TrimSpace(statusline.Render(cfg, p, branch, daily, redirect)) == "" {
		return "self-test: FAILED — render produced no output for the built-in fixture"
	}
	return "self-test: ok"
}

// statuslineTokenNote NAMES the state of the transcript token counter, which the render path is
// required to keep silent about (Gap 14): every transcript failure there — an absent transcript_path,
// a deleted or unreadable file, a schema this binary cannot parse — degrades to "the counter did not
// advance", which is indistinguishable from "nothing happened" on the pane.
//
// It reports the OBSERVABLE state rather than a stored error, because persisting a failure string
// would put transcript-derived text in the snapshot (security.md E2.1 is numbers-only) and unbounded
// data under the 4KB read cap. The distinction that matters to an operator is the one this makes:
// snapshots exist for today, but none of them counted a single token.
func statuslineTokenNote(root string, now time.Time) string {
	sessionsDir := config.StatuslineSessionsDir(root)
	daily := statusline.SumDaily(sessionsDir, now)
	switch {
	case daily.Sessions == 0:
		return "tokens: no session snapshots for today — nothing counted yet"
	case daily.Tokens == 0:
		return "tokens: FAILED — today's snapshots counted no transcript tokens; " +
			"the payload carried no transcript_path, or the transcript was unreadable"
	default:
		return fmt.Sprintf("tokens: ok (%d today across %d session(s))", daily.Tokens, daily.Sessions)
	}
}

// statuslineStalenessNote is the K8 staleness scan. It scans the provisioned agent dirs under
// .agentfactory/agents/* and names any whose .claude/settings.json lacks the statusLine key —
// an agent provisioned by an older binary before the settings templates carried the key (a
// binary swap without a re-init). Live worktrees self-heal on their next dispatch/recreation
// rather than via `af install --init`, which sweeps only .agentfactory/agents/* (D11).
func statuslineStalenessNote(root string) string {
	entries, err := os.ReadDir(config.AgentsDir(root))
	if err != nil {
		return "" // no agents dir yet ⇒ nothing to scan
	}
	var stale []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !agentSettingsHasStatusLine(config.AgentDir(root, e.Name())) {
			stale = append(stale, e.Name())
		}
	}
	if len(stale) == 0 {
		return ""
	}
	sort.Strings(stale)
	var b strings.Builder
	fmt.Fprintf(&b, "stale agents (settings.json lacks the statusLine key): %s\n", strings.Join(stale, ", "))
	fmt.Fprintln(&b, "  run `af install --init` to reprovision agent settings; live worktrees self-heal on their next dispatch/recreation.")
	return b.String()
}

// liveProfile is one live agent's config-only model resolution: the profile name
// ResolveModelEnv selected, and that profile's resolved exports folded into the map shape
// config.PairingLintProfile takes.
type liveProfile struct {
	name    string
	exports map[string]string
}

// statuslineLiveProfiles joins the roster against the live tmux scope and resolves each surviving
// agent's model profile — the shared first leg of both K7 advisories, computed once so the tmux
// sweep and the models.json load are not paid for twice.
//
// It is best-effort in EVERY direction: an unreadable roster, an unreadable models.json, or an
// agent whose profile does not resolve each yield nothing to advise rather than an error. That
// matters most for the roster — config.LoadAgentConfig returns ErrNotFound for an absent
// agents.json (config.go:147-152), unlike the other loaders which degrade to empty/defaults, and
// propagating it would make this read-only diagnostic verb start FAILING on a roster-less factory.
//
// The resolution is deliberately config-only: it passes no cliModel and no .runtime/model_override
// marker, so an agent launched with `af sling --model X` is reported against the profile its
// configuration names rather than the one it is running. That divergence is recorded in the phase
// plan as a follow-up; widening it here would be a scope change, not a fix.
func statuslineLiveProfiles(root string) map[string]liveProfile {
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return nil
	}
	modelsCfg, err := config.LoadModelsConfig(root)
	if err != nil {
		return nil
	}

	tmuxClient := newCmdTmux()
	live := make(map[string]liveProfile, len(agentsCfg.Agents))
	for _, agent := range sortedMapKeys(agentsCfg.Agents) {
		sessionName := session.SessionName(agent)
		running, _ := tmuxClient.HasSession(sessionName)
		if !running || sessionForeignRoot(tmuxClient, sessionName, root) {
			continue
		}
		profile, env, ok, err := config.ResolveModelEnv(modelsCfg, agent, "", "", agentsCfg.Agents[agent].Model)
		if err != nil || !ok {
			continue
		}
		// The fold to a map is what PairingLintProfile's signature requires; indexing it is why
		// the drift check needs no second pass over the ordered []EnvVar.
		exports := make(map[string]string, len(env))
		for _, e := range env {
			exports[e.Key] = e.Value
		}
		live[agent] = liveProfile{name: profile, exports: exports}
	}
	return live
}

// statuslineDriftNote is K7 advisory #1: the operator's declared context window versus the one
// the host actually reports. Both sides are the SAME quantity — the model's full context window,
// one declared in models.json and one read back out of that agent's occupancy snapshot
// (payload.go:35-40 → observation.go:126) — which is the only reason the comparison is sound.
//
// It deliberately does NOT compare the declared AUTO-COMPACT window against the reported model
// window. Those are incommensurable: the auto-compact window is a threshold BELOW the full window
// and the host's percentage always measures against the full one, so declaring 150000 on a 200k
// model — compacting earlier on purpose, this lever's primary legitimate use — would warn forever
// on a correctly configured factory (cross-review CRIT-2).
//
// Why an operator should care: recovery triggers on the host-reported percentage, so the reported
// window is the denominator. A declaration that disagrees with it means recovery is firing against
// arithmetic the operator did not intend.
func statuslineDriftNote(root string, live map[string]liveProfile) string {
	if len(live) == 0 {
		return ""
	}
	scope := make(map[string]struct{}, len(live))
	for agent := range live {
		scope[agent] = struct{}{}
	}

	// This threshold derivation duplicates readAgentOccupancy (agents.go:232-251) almost
	// token-for-token, deliberately: the phase's acceptance criterion greps THIS file for the
	// ReadObservations call, and hoisting a shared helper would move it back out. If that
	// constraint is ever lifted, collapse the two — they are the same computation, and the
	// staleness/darkAfter semantics must not be allowed to diverge between them.
	startupCfg, err := config.LoadStartupConfig(root)
	if err != nil {
		return ""
	}
	staleness := time.Duration(startupCfg.Recovery.StalenessSecs) * time.Second
	darkAfter := time.Duration(startupCfg.Recovery.DarkGraceSecs) * time.Second
	if staleness <= 0 || darkAfter <= 0 {
		return ""
	}
	readings, _ := statusline.ReadObservations(config.StatuslineSessionsDir(root), statusline.ReadOptions{
		KnownAgents: scope,
		Staleness:   staleness,
		DarkAfter:   darkAfter,
	}, time.Now())

	var drifted []string
	for _, agent := range sortedMapKeys(live) {
		declared, ok := statuslineDeclaredWindow(live[agent])
		if !ok {
			continue
		}
		// IsHealthy, not the Observation() bool: a stale and even a dark reading still carries a
		// datum (observation.go:225, :235), so gating on the bool alone would advise an operator
		// using a number the host last reported hours ago.
		reading := readings[agent]
		if !reading.IsHealthy() {
			continue
		}
		obs, hasObs := reading.Observation()
		if !hasObs {
			continue
		}
		// The writer only records this field when the host reported a positive window
		// (daily.go:225-235), but the reader does not clamp it — so a hand-planted file must not
		// be able to manufacture a warning out of a zero.
		reported := obs.TokensTotal()
		if reported <= 0 || uint64(reported) == declared {
			continue
		}
		drifted = append(drifted, fmt.Sprintf("  %s: profile %q declares %d, host reports %d\n",
			agent, live[agent].name, declared, reported))
	}
	if len(drifted) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "context-window drift (declared %s vs the window the host reports):\n", config.EnvMaxContextTokens)
	for _, line := range drifted {
		b.WriteString(line)
	}
	fmt.Fprintln(&b, "  the reported window is what context recovery divides by; align the declaration with the "+
		"backend's real window, or recovery fires early (churn) or never (blind).")
	return b.String()
}

// statuslineDeclaredWindow reads the declared real context window out of a resolved profile,
// reusing config.DecimalTokenCount — the same digits-only rule the write boundary enforces — so
// the two cannot drift (issue #602 F3). An absent, empty, non-decimal or zero value is not a
// declaration at all — silence, not a warning; the zero reject is statusline-specific (the parser
// accepts "0"), so it stays here.
func statuslineDeclaredWindow(lp liveProfile) (uint64, bool) {
	declared, ok := config.DecimalTokenCount(lp.exports[config.EnvMaxContextTokens])
	if !ok || declared == 0 {
		return 0, false
	}
	return declared, true
}

// statuslinePairingNote is K7 advisory #2: Phase 1's pairing lint, applied to the profiles that
// are actually live. `af config models set` already runs this predicate at the write boundary
// (config_set.go:269-274) — this catches the file that never went through it, where a foreign
// model id declaring more than the host's assumed 200000 window without the companion key caps
// silently and does nothing at all.
//
// Config-static: no snapshot, no clock, no I/O. Keyed by profile name rather than agent so a
// profile shared by several live agents is named once — the warning describes the profile.
func statuslinePairingNote(live map[string]liveProfile) string {
	profiles := make(map[string]map[string]string, len(live))
	for _, lp := range live {
		profiles[lp.name] = lp.exports
	}
	var b strings.Builder
	for _, name := range sortedMapKeys(profiles) {
		// The predicate owns the whole sentence, exactly as it does at the write boundary, so the
		// two surfaces cannot drift into wording the operator has to reconcile.
		if warning, ok := config.PairingLintProfile(name, profiles[name]); ok {
			fmt.Fprintf(&b, "pairing warning: %s\n", warning)
		}
	}
	return b.String()
}

// agentSettingsHasStatusLine reports whether an agent's .claude/settings.json carries a
// top-level statusLine key. An absent or unparseable file reads as "no" (i.e. stale).
func agentSettingsHasStatusLine(agentDir string) bool {
	data, err := os.ReadFile(filepath.Join(agentDir, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	_, ok := m["statusLine"]
	return ok
}
