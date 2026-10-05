package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// PR #694 pinning tests (RED pins): T4 (preflightGatewayPort enumerates every file:-token
// profile, not just codex-subscription), T9 (consent-decline refusal, mutation B11), T16 (the K3
// assertQuickstartSupports refusal must precede consent/device-auth).

// writeModelsJSONForPreflight seeds <root>/.agentfactory/models.json with a single api-key gateway
// profile named "codex" (NOT the gatewayAuthProfileName "codex-subscription"). At head
// preflightGatewayPort reads only the codex-subscription profile, so an api-key factory's foreign
// listener is invisible to it — that is the T4 gap.
func writeModelsJSONForPreflight(t *testing.T, root, baseURL string) {
	t.Helper()
	dir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := fmt.Sprintf(`{"models":{"codex":{"ANTHROPIC_BASE_URL":%q,"ANTHROPIC_AUTH_TOKEN":"file:.agentfactory/secrets/openai.key"}}}`, baseURL)
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPreflightGatewayPortEnumeratesFileTokenProfiles (T4). RED at head: preflightGatewayPort reads
// only cfg.Models[gatewayAuthProfileName]["ANTHROPIC_BASE_URL"] (== the codex-subscription profile),
// so a loopback listener declared by the api-key "codex" profile is never dialed and the run
// proceeds against a foreign owner. GREEN once the preflight enumerates every file:-token profile
// the way removeStaleGatewayCoverageRecord already does.
func TestPreflightGatewayPortEnumeratesFileTokenProfiles(t *testing.T) {
	// No "litellm" tmux session is ours; a foreign owner must therefore be refused.
	orig := newCmdTmux
	newCmdTmux = func() cmdTmux { return fakeCmdTmux{} }
	t.Cleanup(func() { newCmdTmux = orig })

	t.Run("foreign_listener_on_apikey_codex_profile_is_refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		port := ln.Addr().(*net.TCPAddr).Port

		root := t.TempDir()
		writeModelsJSONForPreflight(t, root, fmt.Sprintf("http://127.0.0.1:%d", port))

		err = preflightGatewayPort(root)
		if err == nil {
			t.Fatal("preflightGatewayPort must refuse: an api-key 'codex' profile's loopback port is served by a process outside the 'litellm' tmux session, but head reads only the codex-subscription profile and returns nil")
		}
		if !strings.Contains(err.Error(), "served by a process outside the 'litellm' tmux session") {
			t.Errorf("error = %q, want it to name the foreign 'litellm' owner (design-doc.md:120)", err.Error())
		}
	})

	t.Run("free_port_is_noop", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close() // port is now free — nothing to conflict with

		root := t.TempDir()
		writeModelsJSONForPreflight(t, root, fmt.Sprintf("http://127.0.0.1:%d", port))
		if err := preflightGatewayPort(root); err != nil {
			t.Errorf("a free port must be a no-op; got %v", err)
		}
	})

	t.Run("non_loopback_is_noop", func(t *testing.T) {
		root := t.TempDir()
		writeModelsJSONForPreflight(t, root, "http://10.255.255.1:9") // non-loopback, un-dialed
		if err := preflightGatewayPort(root); err != nil {
			t.Errorf("a non-loopback endpoint must be a no-op; got %v", err)
		}
	})

	t.Run("no_models_json_is_noop", func(t *testing.T) {
		root := t.TempDir()
		if err := preflightGatewayPort(root); err != nil {
			t.Errorf("a missing models.json must be a no-op; got %v", err)
		}
	})
}

// TestInstallConsentDeclineRefuses (T9, mutation B11). Answering the codex-install consent prompt
// with "no" must refuse subscription mode and touch no agents. This PASSES against real code today
// (install.go:1245 returns the "declined" error) — it is the coverage that mutation B11
// (`if false && !ok`) currently escapes; recorded RED-under-mutation per red_predictions.md.
func TestInstallConsentDeclineRefuses(t *testing.T) {
	installAgentsFlag = false
	installLitellmFlag = false
	installLitellmAuthFlag = ""
	t.Cleanup(func() { installAgentsFlag = false; installLitellmFlag = false; installLitellmAuthFlag = "" })

	origAgentGen := runAgentGenScript
	origQuickstart := runQuickstartScript
	origLookPath := lookPathCodex
	origSudo := sudoNonInteractiveOK
	origNpm := npmGlobalRootWritable
	origConsent := promptCodexInstallConsent
	t.Cleanup(func() {
		runAgentGenScript = origAgentGen
		runQuickstartScript = origQuickstart
		lookPathCodex = origLookPath
		sudoNonInteractiveOK = origSudo
		npmGlobalRootWritable = origNpm
		promptCodexInstallConsent = origConsent
	})

	// Codex absent but install-feasible, isolating the consent gate.
	lookPathCodex = func() (string, error) { return "", fmt.Errorf("codex not found") }
	sudoNonInteractiveOK = func() bool { return true }
	npmGlobalRootWritable = func() bool { return true }
	promptCodexInstallConsent = func(io.Writer) (bool, error) { return false, nil } // operator declines
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("AF_CODEX_INSTALL_CONSENT", "")

	var agentGenCalled, quickstartCalled bool
	runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error {
		agentGenCalled = true
		return nil
	}
	runQuickstartScript = func(cmd *cobra.Command, afSrc, projectDir string, extraArgs []string) error {
		quickstartCalled = true
		return nil
	}

	afSrc := newAFSourceDir(t, []string{"agent-gen-all.sh", "quickstart.sh"}, nil)
	t.Setenv("AF_SOURCE_ROOT", afSrc)

	dir := setupFactoryDir(t)
	_, err := runInstallInDir(t, dir, "--agents", "--litellm", "--litellm-auth=codex-subscription")
	if err == nil {
		t.Fatal("declining the codex-install consent prompt must refuse subscription mode, got success")
	}
	if !strings.Contains(err.Error(), "declined") {
		t.Errorf("error = %q, want it to name the declined consent (install.go:1245)", err.Error())
	}
	if agentGenCalled {
		t.Error("agent-gen-all.sh ran despite the consent decline — nothing must be touched")
	}
	if quickstartCalled {
		t.Error("quickstart.sh ran despite the consent decline")
	}
}

// TestInstallStaleScriptRefusalPrecedesConsentAndLogin (T16). With a stale quickstart.sh lacking the
// --litellm-auth marker, the K3 assertQuickstartSupports refusal must fire BEFORE any
// consent prompt or device-auth login. RED at head: install.go runs preflightCodexSubscription
// (consent + up-to-15-min login) at :828, and the assertQuickstartSupports checks only later
// (:875+), so consent is prompted before the stale-script defect is caught.
func TestInstallStaleScriptRefusalPrecedesConsentAndLogin(t *testing.T) {
	installAgentsFlag = false
	installLitellmFlag = false
	installLitellmAuthFlag = ""
	t.Cleanup(func() { installAgentsFlag = false; installLitellmFlag = false; installLitellmAuthFlag = "" })

	origAgentGen := runAgentGenScript
	origQuickstart := runQuickstartScript
	origLookPath := lookPathCodex
	origSudo := sudoNonInteractiveOK
	origNpm := npmGlobalRootWritable
	origConsent := promptCodexInstallConsent
	origDeviceAuth := runCodexDeviceAuth
	origSessionValid := codexSessionValid
	t.Cleanup(func() {
		runAgentGenScript = origAgentGen
		runQuickstartScript = origQuickstart
		lookPathCodex = origLookPath
		sudoNonInteractiveOK = origSudo
		npmGlobalRootWritable = origNpm
		promptCodexInstallConsent = origConsent
		runCodexDeviceAuth = origDeviceAuth
		codexSessionValid = origSessionValid
	})

	var consentCalls, deviceAuthCalls int
	lookPathCodex = func() (string, error) { return "", fmt.Errorf("codex not found") } // install needed → consent path
	sudoNonInteractiveOK = func() bool { return true }
	npmGlobalRootWritable = func() bool { return true }
	codexSessionValid = func() bool { return false }
	promptCodexInstallConsent = func(io.Writer) (bool, error) { consentCalls++; return true, nil }
	runCodexDeviceAuth = func(ctx context.Context, out, errW io.Writer) error { deviceAuthCalls++; return nil }
	runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error { return nil }
	runQuickstartScript = func(cmd *cobra.Command, afSrc, projectDir string, extraArgs []string) error { return nil }
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("AF_CODEX_INSTALL_CONSENT", "")

	afSrc := newAFSourceDir(t, []string{"agent-gen-all.sh", "quickstart.sh"}, nil)
	// Make quickstart.sh STALE: strip every marker the three assertQuickstartSupports checks look for.
	if err := os.WriteFile(filepath.Join(afSrc, "quickstart.sh"), []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_SOURCE_ROOT", afSrc)

	dir := setupFactoryDir(t)
	_, err := runInstallInDir(t, dir, "--agents", "--litellm", "--litellm-auth=codex-subscription")
	if err == nil {
		t.Fatal("a stale quickstart.sh missing --litellm-auth must make the run refuse, got success")
	}
	if consentCalls != 0 {
		t.Errorf("the stale-script refusal must precede the consent prompt; promptCodexInstallConsent fired %d time(s)", consentCalls)
	}
	if deviceAuthCalls != 0 {
		t.Errorf("the stale-script refusal must precede the device-auth login; runCodexDeviceAuth fired %d time(s)", deviceAuthCalls)
	}
}
