package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// ensureTelemetryBackend relaunches the bundled loopback backend when the recording
// gate is on and nothing answers healthz. The gate is durable state re-asserted by
// every af up; without this guard the backend it points every session at is a
// single-shot process whose only other relaunch trigger is a login shell the
// autonomous flow never starts (issue #584). Best-effort by contract: every exit
// path is a warn or a silent return — observability never blocks work
// (TestTelemetryFailuresNeverFailTheVerb's posture, extended to this guard).
func ensureTelemetryBackend(ctx context.Context, cmd *cobra.Command, root string) {
	if !telemetryFactoryEnabled(root) {
		return
	}
	cfg, err := config.LoadTelemetryConfig(root)
	if err != nil || !config.IsLoopbackEndpoint(cfg.Endpoint) {
		// Never supervise a remote backend; an invalid config already warns elsewhere.
		return
	}

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, telemetryHealthzURL(cfg.Endpoint), nil)
	if err == nil {
		if resp, doErr := telemetryHealthzDo(req); doErr == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return // healthy — nothing to do
			}
		}
	}

	scriptPath := telemetryRelaunchScriptPath(root)
	if _, statErr := os.Stat(scriptPath); statErr != nil {
		// Recreated container without install artifacts — mirror the login guard's
		// own no-op rather than fabricate a script Go has no ZO_*/credential
		// knowledge to author correctly.
		return
	}

	relaunchCtx, relaunchCancel := context.WithTimeout(ctx, 10*time.Second)
	defer relaunchCancel()
	out, err := telemetryRelaunchDo(relaunchCtx, scriptPath)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: telemetry backend relaunch failed: %v (%s)\n",
			err, strings.TrimSpace(out))
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(),
		"telemetry backend: relaunch attempted (tmux session 'telemetry'; cold start may take up to 90s)")
}

// ensureTelemetryBackendFn is the seam both call sites (up.go cold-start, the
// watchdog's periodic trigger in watchdog.go) invoke through, and the one tests
// override — mirroring newWatchdogTmux's package-var shape (watchdog.go:78) — so a
// watchdog-tick test can assert the guard fired without needing a real gate-on
// factory root or a live HTTP probe.
var ensureTelemetryBackendFn = ensureTelemetryBackend

// telemetryHealthzURL derives the bare healthz probe URL from the configured
// endpoint. It must NOT reuse usageURL's append idiom: cfg.Endpoint already carries
// the "/api/default" org segment (e.g. "http://127.0.0.1:5080/api/default"), but
// OpenObserve's liveness route is the bare "http://127.0.0.1:5080/healthz" — no org
// segment — confirmed live at quickstart.sh:1241's readiness poll. Appending onto
// the full endpoint would build ".../api/default/healthz", which 404s against a
// perfectly healthy backend and permanently mis-arms the guard. A malformed endpoint
// falls back to the naive join — the loopback check in ensureTelemetryBackend already
// refuses anything url.Parse can't make sense of, so this path is unreachable in
// practice; it exists only so a parse failure here can never panic.
func telemetryHealthzURL(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return usageURL(endpoint, "/healthz")
	}
	u.Path = "/healthz"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// telemetryHealthzDo is the HTTP probe seam tests override, mirroring
// telemetryQueryDo's request-granular shape (telemetry_usage.go:121-123).
var telemetryHealthzDo = func(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// telemetryRelaunchScriptPath resolves the install-authored relaunch script path
// quickstart.sh's setup_telemetry() writes (fable-implement Step 2).
func telemetryRelaunchScriptPath(root string) string {
	return filepath.Join(root, ".agentfactory", "telemetry", "relaunch.sh")
}

// runRelaunchScript is the ungated exec — split out from telemetryRelaunchDo so a
// test can exercise real exec.CommandContext timeout behavior (context-cancellation
// killing the process) directly, the way isTestBinary()'s guard on the outer seam
// otherwise makes impossible under `go test` (reapImprovementSession's real body is
// unreachable the same way — no existing test in this codebase exercises it; this
// split is new territory, not a copied idiom). It invokes the script as an opaque
// file — never reconstructing the tmux command inline — so the
// credential-resolves-inside-the-pane-shell property (quickstart.sh:1198) survives
// unbroken (decisions.md D2, concern_blast.md §2.3).
//
// WaitDelay matters here and is not decorative. exec.CommandContext kills only the
// DIRECT child (bash) when ctx is done — a grandchild the script spawned (e.g. the
// backgrounded openobserve process, or this file's own timeout test double) can
// keep holding the stdout/stderr pipe's write end open after bash itself is dead,
// which makes CombinedOutput() block past the context deadline waiting for a pipe
// that will never close on its own (TestRunRelaunchScript_TimeoutEnforced pins
// this). WaitDelay bounds that second wait and forces the pipes closed once it
// elapses, so the context deadline is an upper bound in practice, not just in
// intent.
func runRelaunchScript(ctx context.Context, scriptPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// telemetryRelaunchDo is the subprocess-invocation seam tests override, mirroring
// reapImprovementSession's isTestBinary()-guarded shape (improvement.go:634-658).
var telemetryRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
	if isTestBinary() {
		return "", nil
	}
	return runRelaunchScript(ctx, scriptPath)
}
