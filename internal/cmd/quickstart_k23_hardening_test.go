package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This file pins Phase 1 (K23) of issue #693: sudo -n hardening at the two npm-install sibling
// sites, and -t =litellm exact-match migration at the 4 litellm has-session sites — mirroring
// AC #2/#3 verbatim (IMPLREADME_PHASE1.md). It also protects the DO-NOT-CHANGE sites (intake.md):
// the codex-install sudo at :892 and the 3 telemetry has-session sites must stay untouched.

func quickstartScriptContent(t *testing.T) string {
	t.Helper()
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	return string(data)
}

// has-session -t =litellm sites: _write_codex_compat_module's restart guard, _reconcile_gateway's
// two probes, and the gateway-relaunch.sh heredoc. The pre-pip version guard's fifth site went
// with the guard itself (the lock install is unconditional; K3 decides the stop). Pinned exactly so
// a new site is a deliberate, reviewed change.
func TestQuickstartLitellmHasSessionUsesExactMatchCount(t *testing.T) {
	content := quickstartScriptContent(t)
	got := strings.Count(content, "has-session -t =litellm")
	if got != 4 {
		t.Errorf("has-session -t =litellm count = %d, want 4 (the compat-module restart guard, _reconcile_gateway's two probes, the relaunch heredoc)", got)
	}
}

// Every privileged npm global install goes through _npm_install_pinned, which installs only a
// digest-verified local tarball and uses `sudo -n` (never a password prompt) for a root-owned
// prefix.
func TestQuickstartSudoNpmInstallHardened(t *testing.T) {
	content := quickstartScriptContent(t)
	pinned := topLevelShellFunction(content, "_npm_install_pinned")
	if pinned == "" {
		t.Fatal("_npm_install_pinned() is not defined at top level")
	}
	if got := strings.Count(pinned, "sudo -n npm install"); got < 1 {
		t.Errorf("_npm_install_pinned() has %d `sudo -n npm install` lines, want >= 1", got)
	}
	if got, inside := strings.Count(content, "sudo -n npm install"), strings.Count(pinned, "sudo -n npm install"); got != inside {
		t.Errorf("%d `sudo -n npm install` occurrence(s) sit outside _npm_install_pinned()", got-inside)
	}
	if m := regexp.MustCompile(`sudo\s+npm\b`).FindAllString(content, -1); len(m) != 0 {
		t.Errorf("quickstart.sh has %d bare `sudo npm` call(s); sudo must always be `sudo -n`", len(m))
	}
	outside := strings.Replace(content, pinned, "", 1)
	for _, line := range strings.Split(outside, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if regexp.MustCompile(`(^|[\s;&|(])npm\s+install\s+-g\b`).MatchString(line) {
			t.Errorf("an `npm install -g` invocation sits outside _npm_install_pinned(): %s", strings.TrimSpace(line))
		}
	}
}

// The codex CLI is installed through _npm_install_pinned, never by a registry-spec `npm install -g
// @openai/codex` (sudo'd or not). _npm_install_pinned decides the sudo route with `sudo -n true`
// BEFORE it reaches `sudo -n npm install -g`, so an infeasible route never prompts.
func TestQuickstartCodexInstallSudoIsHardened(t *testing.T) {
	content := quickstartScriptContent(t)
	for _, banned := range []string{"sudo -n npm install -g @openai/codex", "sudo npm install -g @openai/codex"} {
		if strings.Contains(content, banned) {
			t.Errorf("quickstart.sh still installs codex from the registry by name: %q", banned)
		}
	}
	install := extractShellFunction(content, "_install_codex")
	if install == "" {
		t.Fatal("could not extract the nested _install_codex() from quickstart.sh")
	}
	if !strings.Contains(install, "_npm_install_pinned @openai/codex") {
		t.Errorf("_install_codex() does not call `_npm_install_pinned @openai/codex`; body:\n%s", install)
	}
	pinned := topLevelShellFunction(content, "_npm_install_pinned")
	probe, sudoInstall := strings.Index(pinned, "sudo -n true"), strings.Index(pinned, "sudo -n npm install -g")
	if probe < 0 || sudoInstall < 0 || probe > sudoInstall {
		t.Errorf("_npm_install_pinned() must probe `sudo -n true` before its `sudo -n npm install -g` (probe at %d, install at %d)", probe, sudoInstall)
	}
}

// Protective DO-NOT-CHANGE: the 3 telemetry has-session sites are explicitly out of Phase-1 scope
// and must keep using -t telemetry (not -t =telemetry) — migrating them would not move AC #2's
// count and is named out of scope in the Gotchas section.
func TestQuickstartTelemetryHasSessionSitesUnchanged(t *testing.T) {
	content := quickstartScriptContent(t)
	got := strings.Count(content, "has-session -t telemetry")
	if got != 3 {
		t.Errorf("has-session -t telemetry (unmigrated telemetry sites) count = %d, want 3 "+
			"(migrating them out of scope; see intake.md DO-NOT-CHANGE)", got)
	}
	if strings.Count(content, "has-session -t =telemetry") != 0 {
		t.Error("found a has-session -t =telemetry site — migrating telemetry sessions is explicitly " +
			"out of Phase-1 scope (intake.md DO-NOT-CHANGE)")
	}
}

// AC #4 (bash -n stays clean) is already pinned by TestQuickstartScriptIsSyntacticallyValidBash
// (quickstart_provisioning_shape_test.go) — that existing test IS this phase's protective
// assertion for AC #4 and must keep passing unmodified.
