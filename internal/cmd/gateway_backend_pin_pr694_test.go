package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

// PR #694 pinning tests (RED pins): T18 (the "relaunch attempted" line must be gated on a
// post-relaunch HasSession re-check), T10 (af up must fire the gateway backend guard, mutation B24).

// TestEnsureGatewayBackend_RelaunchAttemptedGatedOnPostRelaunchSession (T18). When the relaunch
// script exits 0 but no "litellm" session actually comes up (missing handle/key case), the success
// line "gateway backend: relaunch attempted" must NOT print — only the relayed script warning does.
// RED at head: gateway_backend.go:76 prints the line unconditionally when relaunchErr == nil, with
// no post-relaunch HasSession re-check.
func TestEnsureGatewayBackend_RelaunchAttemptedGatedOnPostRelaunchSession(t *testing.T) {
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false) // session absent before AND after the relaunch

	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		// exit 0, but the script warns it could not start a session and none comes up
		return "warning: gateway relaunch could not start the 'litellm' session (missing credential)", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	ensureGatewayBackend(context.Background(), cmd, root)

	if bytes.Contains(out.Bytes(), []byte("relaunch attempted")) {
		t.Errorf("must not claim 'relaunch attempted' when no 'litellm' session came up post-relaunch (gateway_backend.go:76); out=%q", out.String())
	}
	if !bytes.Contains(errBuf.Bytes(), []byte("warning")) {
		t.Errorf("the relayed script warning must still surface even when the success line is suppressed; err=%q", errBuf.String())
	}
}

// TestRunUpFiresGatewayBackendGuardOnce (T10, mutation B24). `af up` must drive the gateway backend
// guard exactly once — the symmetric half of the watchdog wiring the reviewer named (up.go:457
// wires it); FAILS against mutation B24 (`_ = ensureGatewayBackendFn`). Design 695 Phase 2 (K10,
// IMPLREADME L816-818) extends it: right after the gateway guard the blanket branch calls
// ensureIntegrationServicesFn and then the user-scope detector, printing the ensure reports once
// and the detector's findings as one warning line, both to stderr — and the gateway count stays 1.
func TestRunUpFiresGatewayBackendGuardOnce(t *testing.T) {
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1,"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)
	setupHermeticSessions(t)

	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude-config-empty"))

	var order []string
	calls := stubGatewayBackendGuard(t, func() { order = append(order, "gateway") })
	const report = "INTEGRATION_DEGRADED svcpin: check failed"
	ensureCalls, scope := stubIntegrationServicesEnsure(t, &order, []string{report})
	detectCalls := stubUserScopeDetector(t, &order, []string{"frontend-design@x", "playwright@x"})

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	_ = runUp(cmd, nil) // the aggregate agent-start result is irrelevant; the gateway guard wiring is the pin

	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("af up must fire the gateway backend guard exactly once (up.go:457); fired %d", got)
	}
	if want := []string{"gateway", "integrations", "detector"}; strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("blanket af up must run gateway guard → integration ensure → user-scope detector, each once; got %v", order)
	}
	if *ensureCalls == 1 && *scope != serviceScopeFactory {
		t.Errorf("af up ensures factory-scope services (IMPLREADME L791); got scope %v", *scope)
	}
	if got := strings.Count(errBuf.String(), report); got != 1 {
		t.Errorf("af up must print each ensure report exactly once to stderr; printed %d times\nstderr=%s", got, errBuf.String())
	}
	var detLines int
	for _, l := range strings.Split(errBuf.String(), "\n") {
		if strings.Contains(l, "frontend-design") || strings.Contains(l, "playwright") {
			detLines++
			if !strings.Contains(l, "frontend-design") || !strings.Contains(l, "playwright") {
				t.Errorf("the detector warning must be one line naming every unaccounted plugin: %q", l)
			}
		}
	}
	if *detectCalls == 1 && detLines != 1 {
		t.Errorf("af up must print the detector findings as exactly one stderr line; got %d\nstderr=%s", detLines, errBuf.String())
	}
}
