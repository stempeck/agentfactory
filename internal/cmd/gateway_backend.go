package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// ensureGatewayBackend relaunches the litellm gateway when nothing owns it, mirroring
// ensureTelemetryBackend's contract (fable-implement PR #694 Phase 7 / K19,
// investigation_report.md "Agreed fix approach"). Best-effort by contract: every exit
// path is a warn or a silent return, and it never kills (DO-NOT-CHANGE, intake.md).
//
// Ordering is load-bearing (decisions.md D4): the reconciling-marker interlock runs
// FIRST, before HasSession, because a live marker means a kill is in flight right now
// and HasSession's answer is not timing-independent during that window.
func ensureGatewayBackend(ctx context.Context, cmd *cobra.Command, root string) {
	markerPath := gatewayReconcilingMarkerPath(root)
	if data, err := os.ReadFile(markerPath); err == nil {
		var marker gatewayReconcileMarker
		switch unmarshalErr := json.Unmarshal(data, &marker); {
		case unmarshalErr != nil:
			// decisions.md D3: an unparseable marker folds into "stale", worded
			// distinctly from the dead-pid/timeout case so an operator can tell a
			// genuine timeout from a write race.
			if removeErr := os.Remove(markerPath); removeErr == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "gateway backend: removed unreadable/corrupt reconciling marker")
			}
		case gatewayReconcileMarkerStale(marker, time.Now()):
			if removeErr := os.Remove(markerPath); removeErr == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "gateway backend: removed stale reconciling marker (pid=%d, since=%d)\n", marker.PID, marker.Since)
			}
		default:
			return // live reconcile in flight — never launch underneath it
		}
	}

	if present, err := newCmdTmux().HasSession("litellm"); err == nil && present {
		return
	}

	scriptPath := gatewayRelaunchScriptPath(root)
	if _, statErr := os.Stat(scriptPath); statErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: gateway relaunch script not found at %s; gateway will not be relaunched\n", scriptPath)
		return
	}

	if _, _, modeErr := gatewayAuthMode(root); modeErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: gateway auth mode unresolved: %v; gateway will not be relaunched\n", modeErr)
		return
	}

	out, relaunchErr := gatewayRelaunchDo(ctx, scriptPath)
	switch trimmed := strings.TrimSpace(out); {
	case trimmed != "":
		// decisions.md D2: the script's own captured output already carries its
		// "warning: ..." convention (quickstart.sh:1355-1373) — relay it as-is,
		// unconditionally, regardless of exit code, rather than gating on relaunchErr
		// the way telemetry's template does (a literal mirror would silently drop
		// exactly the warning AC1 requires).
		fmt.Fprintln(cmd.ErrOrStderr(), trimmed)
	case relaunchErr != nil:
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: gateway backend relaunch failed: %v\n", relaunchErr)
	}
	if relaunchErr != nil {
		return
	}
	// T18/decisions.md D6: the script can exit 0 without ever starting a session (a missing handle or
	// key makes it warn-and-return 0), so gate the success line on a post-relaunch HasSession re-check
	// — exactly as quickstart.sh's own caller does (quickstart.sh:1079). The relayed script warning
	// above already tells the operator nothing came up, so a false-negative HasSession prints nothing
	// extra rather than a second, contradictory "did not start" line.
	if present, err := newCmdTmux().HasSession("litellm"); err != nil || !present {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "gateway backend: relaunch attempted (tmux session 'litellm')")
}

// ensureGatewayBackendFn is the seam both call sites (up.go cold-start, the
// watchdog's periodic trigger in watchdog.go — investigation_report.md "Files to
// Modify") invoke through, and the one tests override — mirroring
// ensureTelemetryBackendFn's package-var shape (telemetry_backend.go:72).
var ensureGatewayBackendFn = ensureGatewayBackend

// gatewayReconcileMarker is the Go-side decode target for the bash-owned
// .runtime/gateway/reconciling marker (quickstart.sh:1029-1039, DO-NOT-CHANGE: K19
// only reads/parses it). Field names/types must match the writer's shape exactly —
// pinned by TestGatewayReconcileMarker_FieldNamesMatchWriterShape.
type gatewayReconcileMarker struct {
	PID   int   `json:"pid"`
	Since int64 `json:"since"`
}

// gatewayReconcileMarkerStaleAfter is the intake's "older than 15 minutes" bound
// (investigation_report.md Files to Modify: age>900s stale, age<=900s live).
const gatewayReconcileMarkerStaleAfter = 900 * time.Second

// gatewayReconcilingMarkerPath resolves the bash-owned marker path. Deliberately NOT
// config.ConfigDir-relative — that resolves .agentfactory, a different root; the
// marker lives under .runtime (quickstart.sh:1029, concern_locus.md §4).
func gatewayReconcilingMarkerPath(root string) string {
	return filepath.Join(root, ".runtime", "gateway", "reconciling")
}

// gatewayRelaunchScriptPath resolves the install-authored relaunch script path
// quickstart.sh's setup_litellm() writes (quickstart.sh:1306-1407).
func gatewayRelaunchScriptPath(root string) string {
	return filepath.Join(root, ".agentfactory", "gateway-relaunch.sh")
}

// gatewayPidAlive mirrors the codebase's one settled dead-pid idiom (decisions.md D3
// judgment call 3; internal/lock/lock.go:115-121, internal/issuestore/mcpstore/
// lifecycle.go:260-266) rather than inventing a third variant — internal/lock's
// processExists is unexported and unreachable from this package.
func gatewayPidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// gatewayReconcileMarkerStale reports whether a decoded marker should be treated as
// stale: a dead owner pid, or an age past the 15-minute bound (decisions.md D3/D4).
func gatewayReconcileMarkerStale(m gatewayReconcileMarker, now time.Time) bool {
	if !gatewayPidAlive(m.PID) {
		return true
	}
	age := now.Unix() - m.Since
	return age > int64(gatewayReconcileMarkerStaleAfter.Seconds())
}

// runGatewayRelaunchScript is the ungated exec, split out from gatewayRelaunchDo so a
// test can exercise real exec.CommandContext timeout behavior directly — mirrors
// runRelaunchScript (telemetry_backend.go:126-131) including its WaitDelay rationale:
// a grandchild the script backgrounds (the tmux new-session) can hold the output pipe
// open past the parent's exit.
func runGatewayRelaunchScript(ctx context.Context, scriptPath string) (string, error) {
	c := exec.CommandContext(ctx, "bash", scriptPath)
	c.WaitDelay = 2 * time.Second
	out, err := c.CombinedOutput()
	return string(out), err
}

// gatewayRelaunchDo is the subprocess-invocation seam tests override, mirroring
// telemetryRelaunchDo's isTestBinary()-guarded shape (telemetry_backend.go:135-140).
var gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
	if isTestBinary() {
		return "", nil
	}
	return runGatewayRelaunchScript(ctx, scriptPath)
}
