//go:build integration

package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTelemetryReadinessColdStart reproduces the Phase-5a OpenObserve readiness bug
// the CEO hit on PR #567 ("OpenObserve not ready on 127.0.0.1:5080 after 30s") and
// guards the fix. It does NOT assert around the code — it EXECUTES the real
// _await_telemetry_ready() polling loop extracted from quickstart.sh against a health
// endpoint that mimics OpenObserve's first-launch cold start (503 until it finishes
// initializing its metadata store, then 200).
//
// CI runners get no docker/privileged, so a real OpenObserve cannot run here; the fake
// backend stands in for the ONE property that matters — a health endpoint that is slow
// to go green — and the loopback port stands in for :5080. The polling logic, the curl
// probe, and the timeout window under test are the production ones, verbatim.
//
// Red -> green: with the old 30s window the loop gives up before a ~35s cold start
// finishes (subtest reproduces the exact reported failure); with quickstart.sh's
// production TELEMETRY_READY_TIMEOUT the loop waits long enough (subtest is red if that
// window is ever lowered back below the cold start).
func TestTelemetryReadinessColdStart(t *testing.T) {
	requireExec(t, "bash")
	requireExec(t, "curl")

	root := findModuleRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	src := string(content)

	awaitFn := extractShellFunction(src, "_await_telemetry_ready")
	if awaitFn == "" {
		t.Fatal("could not extract _await_telemetry_ready() from quickstart.sh — the readiness poll must be a named, testable function")
	}
	prodTimeout := extractShellIntAssignment(t, src, "TELEMETRY_READY_TIMEOUT")

	// The backend goes healthy AFTER the old 30s window but WELL WITHIN the production
	// one — the gap that made a clean install WARN-and-skip. The two subtests below
	// exercise the real poll against this same gap; production_window_survives_cold_start
	// is what turns from red (window <= cold start, e.g. the old 30) to green (the fix).
	const coldStart = 35 * time.Second

	t.Run("old_30s_window_reproduces_not_ready", func(t *testing.T) {
		t.Parallel()
		url := newColdStartHealthz(t, coldStart)
		if awaitReady(t, awaitFn, url, 30) {
			t.Fatalf("expected the 30s window to time out on a %s cold start (the reported bug); it reported ready", coldStart)
		}
	})

	t.Run("production_window_survives_cold_start", func(t *testing.T) {
		t.Parallel()
		url := newColdStartHealthz(t, coldStart)
		if !awaitReady(t, awaitFn, url, prodTimeout) {
			t.Fatalf("production TELEMETRY_READY_TIMEOUT=%ds failed to detect a backend ready after %s — the readiness window is too short", prodTimeout, coldStart)
		}
	})
}

// newColdStartHealthz starts a loopback health endpoint that answers 503 until
// readyAfter has elapsed since startup, then 200 on /healthz — the behavior of
// OpenObserve still initializing on first launch. Returns the /healthz URL.
func newColdStartHealthz(t *testing.T, readyAfter time.Duration) string {
	t.Helper()
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if time.Since(start) < readyAfter {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/healthz"
}

// awaitReady runs the extracted _await_telemetry_ready() in bash against url with the
// given window and returns whether it reported the backend ready (exit 0).
func awaitReady(t *testing.T, awaitFn, url string, maxSeconds int) bool {
	t.Helper()
	script := awaitFn + "\n_await_telemetry_ready " + shellSingleQuote(url) + " " + strconv.Itoa(maxSeconds) + "\n"
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(maxSeconds+30)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("_await_telemetry_ready did not return within its own %ds window + margin: %v", maxSeconds, ctx.Err())
	}
	return err == nil
}

func requireExec(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s is required to exercise the telemetry readiness path but is not on PATH: %v", name, err)
	}
}

func extractShellIntAssignment(t *testing.T, content, name string) int {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=([0-9]+)\b`).FindStringSubmatch(content)
	if m == nil {
		t.Fatalf("could not find integer assignment %s=<n> in quickstart.sh", name)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	return n
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
