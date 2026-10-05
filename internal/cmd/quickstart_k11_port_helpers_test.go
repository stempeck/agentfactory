package cmd

import (
	"os/exec"
	"strings"
	"testing"
)

// This file pins Phase 1 (K11) of issue #693: hoisting _port_in_use out of setup_telemetry to
// top-level scope (reachable by setup_litellm too), and the new _port_owner_pid helper.
// decisions.md D7: the hoist is location-only — the existing one-line body must be byte-identical,
// no behavior change.

// K11 hoist: _port_in_use must be reachable OUTSIDE setup_telemetry (i.e. defined at top level,
// not nested inside it) — RED today: shellFnsFrom("_port_in_use") extracts nothing because the
// function is defined inside setup_telemetry's body, not as its own top-level function.
func TestQuickstartPortInUseReachableOutsideSetupTelemetry(t *testing.T) {
	script := shellFnsFrom(t, "_port_in_use") + `
if _port_in_use 1; then echo "IN_USE"; else echo "FREE"; fi
`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("_port_in_use is not reachable as a top-level function (K11 hoist not done): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "FREE") {
		t.Errorf("expected FREE for an unbound low port, got: %s", out)
	}
}

// _port_owner_pid must exist as a top-level function and return empty for a port with no listener.
func TestQuickstartPortOwnerPidReturnsEmptyWhenNoListener(t *testing.T) {
	script := shellFnsFrom(t, "_port_owner_pid") + `
pid="$(_port_owner_pid 1)"
echo "PID=[$pid]"
`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("_port_owner_pid does not exist yet (K11): %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PID=[]") {
		t.Errorf("expected _port_owner_pid to return empty for a port with no listener, got: %s", out)
	}
}

// The stale comment locating _port_in_use "near the top of this [setup_telemetry] function" must
// be updated once the hoist moves it out — this comment must no longer describe a location that no
// longer applies.
func TestQuickstartPortInUseStaleCommentUpdated(t *testing.T) {
	content := quickstartScriptContent(t)
	if strings.Contains(content, "after `_port_in_use()`'s definition, near the top of this function") {
		t.Error("stale comment still describes _port_in_use as living \"near the top of this " +
			"function\" (setup_telemetry) — K11's hoist must update this reference")
	}
}
