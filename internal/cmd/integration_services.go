package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/tmux"
)

// serviceScope selects which recorded integration services an ensure pass covers. The launch sites ensure
// factory-scope services and every formula-scope service a present pin names, because a pinned instance's
// hooks depend on it; an unpinned formula-scope service is started only by admission, on demand.
type serviceScope int

const (
	serviceScopeFactory serviceScope = iota + 1
	serviceScopeFormula
)

// Service ensure constants (decisions D8).
const (
	integrationServiceGrace          = 120 * time.Second
	integrationServiceStartTimeout   = 10 * time.Second
	integrationServicesWatchdogBound = 30 * time.Second
	integrationServiceBackoffBase    = 30 // seconds; cronRetryBackoff(30, n, time.Hour)
	integrationServiceCap            = 8  // first n where the backoff reaches 1h
)

// integrationServiceState is .runtime/integration_service/<name>.json, the cross-process
// relaunch guard shared by af up, the watchdog and (Phase 3) sling.
type integrationServiceState struct {
	LaunchedAt            string `json:"launched_at"`
	ConsecutiveRelaunches int    `json:"consecutive_relaunches"`
	MailedAtCap           bool   `json:"mailed_at_cap"`
}

// integrationServiceStatePath is the state file for integration name.
func integrationServiceStatePath(root, name string) string {
	return filepath.Join(root, ".runtime", "integration_service", name+".json")
}

// ensureIntegrationServicesFn is the seam af up and the watchdog trigger call through.
var ensureIntegrationServicesFn = ensureIntegrationServices

// detectUnaccountedUserScopeFn is the seam af up calls the detector through.
var detectUnaccountedUserScopeFn = detectUnaccountedUserScope

// integrationMailFn sends the one at-cap mail; a seam because sendHandoffMail is a
// no-op under the test binary.
var integrationMailFn = func(to, subject, body string) error {
	return sendHandoffMail(to, subject, body)
}

// integrationNowFn is the clock the grace window and backoff read.
var integrationNowFn = time.Now

// servicePanePIDFn returns the pane pid of a service session (decisions D23).
var servicePanePIDFn = func(session string) (int, error) {
	return tmux.NewTmux().GetPanePID(session)
}

// integrationServicesGuardInFlight is the watchdog trigger's own single-flight.
var integrationServicesGuardInFlight atomic.Bool

// integrationServiceInFlight holds one *atomic.Bool per integration: the per-service
// single-flight, so racing ensures in one process never launch the same service twice.
var integrationServiceInFlight sync.Map

func (s serviceScope) covers(recorded string) bool {
	switch s {
	case serviceScopeFactory:
		return recorded == config.IntegrationScopeFactory
	case serviceScopeFormula:
		return recorded == config.IntegrationScopeFormula
	}
	return false
}

// ensureFactoryIntegrationServices is the launch-site ensure beside the composer: synchronous, bounded by the
// per-service start timeout, and never a refused launch (reports only).
func ensureFactoryIntegrationServices(ctx context.Context, cmd *cobra.Command, root string, w io.Writer) {
	if root == "" {
		return
	}
	for _, r := range ensureIntegrationServicesFn(ctx, cmd, root, serviceScopeFactory) {
		fmt.Fprintln(w, r)
	}
}

// ensureIntegrationServices starts absent services of the given scope and the formula-scope services a
// present pin names, never kills a live one, and returns reports (INTEGRATION_DEGRADED for a live service
// whose check failed).
func ensureIntegrationServices(ctx context.Context, cmd *cobra.Command, root string, scope serviceScope) []string {
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return []string{fmt.Sprintf("integration services not ensured: %v", err)}
	}
	var reports []string
	pinned := sync.OnceValue(func() map[string]bool {
		names, err := pinnedIntegrationNames(root)
		if err != nil {
			reports = append(reports, fmt.Sprintf("pinned formula-scope integration services not ensured: %v", err))
		}
		return names
	})
	for _, name := range slices.Sorted(maps.Keys(cfg.Plugins)) {
		in := cfg.Plugins[name].Integration
		if in == nil || in.Service == "" {
			continue
		}
		if !scope.covers(in.Scope) && !(serviceScopeFormula.covers(in.Scope) && pinned()[name]) {
			continue
		}
		// runUp tests hand a bare cobra.Command's nil Context through (D50).
		if ctx == nil {
			ctx = context.Background()
		}
		reports = append(reports, ensureIntegrationService(ctx, root, name, in)...)
	}
	return reports
}

// degradedIntegration returns the DEGRADED report and mails it to the manager once per degradation.
func degradedIntegration(root, name, detail string) []string {
	report := fmt.Sprintf("INTEGRATION_DEGRADED %s: %s", name, detail)
	reports := []string{report}
	if err := reportIntegration(root, name, integrationReportDegraded, "INTEGRATION_DEGRADED "+name, report); err != nil {
		reports = append(reports, fmt.Sprintf("integration %s: %v", name, err))
	}
	return reports
}

// notBoundIntegration reports a snapshot that no longer binds and mails it under the composer's marker, so
// one drift is mailed once whether the composer or the ensure meets it first. Only the composer clears it.
func notBoundIntegration(root, name string, refusal error) []string {
	report := refusal.Error()
	reports := []string{report}
	if err := reportIntegration(root, name, integrationReportNotBound, "INTEGRATION_NOT_BOUND "+name, report); err != nil {
		reports = append(reports, fmt.Sprintf("integration %s: %v", name, err))
	}
	return reports
}

func ensureIntegrationService(ctx context.Context, root, name string, in *config.PluginIntegration) []string {
	v, _ := integrationServiceInFlight.LoadOrStore(name, new(atomic.Bool))
	inFlight := v.(*atomic.Bool)
	if !inFlight.CompareAndSwap(false, true) {
		return nil
	}
	defer inFlight.Store(false)

	snap, m, err := config.VerifiedIntegrationSnapshot(root, name, in)
	if err != nil {
		return notBoundIntegration(root, name, err)
	}
	if m.Service == nil {
		return []string{fmt.Sprintf("integration %s: plugins.json records service %q but the snapshot %s declares no [service]", name, in.Service, snap)}
	}

	t := newCmdTmux()
	live, err := integrationServiceLive(ctx, t, m.Service)
	if err != nil {
		return []string{fmt.Sprintf("integration %s: probing service %s: %v", name, m.Service.Session, err)}
	}

	var reports []string
	st, err := readIntegrationServiceState(root, name)
	if err != nil {
		reports = append(reports, fmt.Sprintf("integration %s: ignoring unreadable service state %s: %v", name, integrationServiceStatePath(root, name), err))
	}
	now := integrationNowFn()
	launchedAt, launched := parseIntegrationServiceTime(st.LaunchedAt)

	if live {
		if (st.ConsecutiveRelaunches != 0 || st.MailedAtCap) && (!launched || now.Sub(launchedAt) >= integrationServiceGrace) {
			st.ConsecutiveRelaunches, st.MailedAtCap = 0, false
			if err := writeIntegrationServiceState(root, name, st); err != nil {
				reports = append(reports, fmt.Sprintf("integration %s: %v", name, err))
			}
		}
		rec, err := readIntegrationCheckRecord(root, name)
		switch {
		case err != nil:
			reports = append(reports, fmt.Sprintf("integration %s: %v", name, err))
		case rec != nil && rec.State == "fail":
			reports = append(reports, degradedIntegration(root, name, rec.Output)...)
		default:
			// Live with no failing record: neither degradation cause holds, so the condition has cleared (D69).
			clearIntegrationReport(root, name, integrationReportDegraded)
		}
		return reports
	}

	if m.Service.Probe == config.IntegrationProbeHTTPHealthz {
		// A running session with a failing healthz cannot be relaunched without a kill, which af
		// never does; counting it toward the cap would end in a false "keeps dying" mail (D64).
		present, err := t.HasSession(m.Service.Session)
		if err != nil {
			return append(reports, fmt.Sprintf("integration %s: probing service session %s: %v", name, m.Service.Session, err))
		}
		if present {
			if !launched || now.Sub(launchedAt) >= integrationServiceGrace {
				reports = append(reports, degradedIntegration(root, name, fmt.Sprintf("healthz %s not OK (session %s is running)", m.Service.HealthzURL, m.Service.Session))...)
			}
			return reports
		}
	}

	if integrationServiceRelaunchDeferred(st, now) {
		return reports
	}

	run := filepath.Join(snap, filepath.FromSlash(m.Service.Run))
	// tmux hands the command to sh -c, so the path must reach it as one shell word (D48).
	if err := launchIntegrationService(ctx, t, m.Service.Session, snap, shellQuote(run)); err != nil && !errors.Is(err, tmux.ErrSessionExists) {
		reports = append(reports, fmt.Sprintf("integration %s: starting service session %s: %v", name, m.Service.Session, err))
	}
	// Every attempt counts toward the backoff, including a lost race (ErrSessionExists, D25).
	st.LaunchedAt = now.UTC().Format(time.RFC3339Nano)
	st.ConsecutiveRelaunches++
	if st.ConsecutiveRelaunches >= integrationServiceCap && !st.MailedAtCap {
		subject := fmt.Sprintf("Integration service %s keeps dying", name)
		body := fmt.Sprintf("Integration %s: service session %s has been relaunched %d times in a row; af now retries it only hourly. It is never killed by af. Inspect it with: af plugin check %s", name, m.Service.Session, st.ConsecutiveRelaunches, name)
		if err := integrationMailFn(escalationTarget, subject, body); err != nil {
			reports = append(reports, fmt.Sprintf("integration %s: mailing %s at the relaunch cap: %v", name, escalationTarget, err))
		} else {
			st.MailedAtCap = true
		}
	}
	if err := writeIntegrationServiceState(root, name, st); err != nil {
		reports = append(reports, fmt.Sprintf("integration %s: %v", name, err))
	}
	return reports
}

// integrationServiceRelaunchDeferred reports whether a down service is left alone this call: it was launched
// inside its grace window or its relaunch backoff.
func integrationServiceRelaunchDeferred(st integrationServiceState, now time.Time) bool {
	launchedAt, launched := parseIntegrationServiceTime(st.LaunchedAt)
	if !launched {
		return false
	}
	since := now.Sub(launchedAt)
	return since < integrationServiceGrace || since < cronRetryBackoff(integrationServiceBackoffBase, st.ConsecutiveRelaunches, time.Hour)
}

// integrationHealthzClient never follows a redirect: the manifest's healthz URL is loopback-only,
// and a redirect would let the service point the probe anywhere.
var integrationHealthzClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func integrationServiceLive(ctx context.Context, t cmdTmux, svc *config.IntegrationService) (bool, error) {
	switch svc.Probe {
	case config.IntegrationProbeTmuxSession:
		return t.HasSession(svc.Session)
	case config.IntegrationProbeHTTPHealthz:
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, svc.HealthzURL, nil)
		if err != nil {
			return false, err
		}
		resp, err := integrationHealthzClient.Do(req)
		if err != nil {
			return false, nil
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK, nil
	}
	return false, fmt.Errorf("unknown probe %q", svc.Probe)
}

// launchIntegrationService bounds the tmux call itself (B16): a wedged tmux server must not
// hang af up or the watchdog, so the call runs on its own goroutine and is abandoned at the bound.
func launchIntegrationService(ctx context.Context, t cmdTmux, session, dir, command string) error {
	ctx, cancel := context.WithTimeout(ctx, integrationServiceStartTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- t.NewSessionWithCommand(session, dir, command) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("tmux did not create the session: %w", ctx.Err())
	}
}

func parseIntegrationServiceTime(s string) (time.Time, bool) {
	at, err := time.Parse(time.RFC3339Nano, s)
	return at, err == nil
}

// readIntegrationServiceState returns the zero state for an absent file; a corrupt one is
// returned as the zero state plus an error the caller reports (D42).
func readIntegrationServiceState(root, name string) (integrationServiceState, error) {
	var st integrationServiceState
	b, err := os.ReadFile(integrationServiceStatePath(root, name))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return integrationServiceState{}, err
	}
	return st, nil
}

func writeIntegrationServiceState(root, name string, st integrationServiceState) error {
	p := integrationServiceStatePath(root, name)
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("writing service state: %w", err)
	}
	if err := fsutil.WriteFileAtomic(p, b, 0o644); err != nil {
		return fmt.Errorf("writing service state %s: %w", p, err)
	}
	return nil
}

// readIntegrationCheckRecord returns nil when no check has run yet.
func readIntegrationCheckRecord(root, name string) (*integrationCheckRecord, error) {
	p := integrationCheckRecordPath(root, name)
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading check record %s: %w", p, err)
	}
	var rec integrationCheckRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("parsing check record %s: %w", p, err)
	}
	return &rec, nil
}

// detectUnaccountedUserScope lists enabled user-scope Claude Code plugins and hooks that
// no installed integration's ClaudePlugins accounts for. It is read-only: af cannot remove a
// user-scope channel, only name it.
func detectUnaccountedUserScope(root string) []string {
	scope, err := readClaudeUserScope()
	if err != nil {
		return []string{err.Error()}
	}
	var rows []string
	accounted := map[string]bool{}
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		rows = append(rows, fmt.Sprintf("%v (no plugin is treated as accounted for)", err))
	} else {
		for _, entry := range cfg.Plugins {
			if entry.Integration == nil {
				continue
			}
			for _, p := range entry.Integration.ClaudePlugins {
				accounted[p] = true
			}
		}
	}
	for _, p := range scope.Plugins {
		if p.Enabled && !accounted[p.Name] {
			rows = append(rows, p.Key)
		}
	}
	if scope.HasHooks {
		rows = append(rows, "hooks in "+scope.Path)
	}
	return rows
}

// quickstartUserScopePlugins are installed at user scope by quickstart.sh itself, before any integration can
// account for them, so warning about them on every af up names nothing the operator chose. A test holds this
// set equal to quickstart's user-scope installs: it empties together with that installer.
var quickstartUserScopePlugins = map[string]bool{"playwright@claude-plugins-official": true}

// withoutQuickstartUserScope drops quickstart's own user-scope plugins from the detector's rows for the af up
// warning only; af plugin check still lists them.
func withoutQuickstartUserScope(rows []string) []string {
	var kept []string
	for _, r := range rows {
		if !quickstartUserScopePlugins[r] {
			kept = append(kept, r)
		}
	}
	return kept
}
