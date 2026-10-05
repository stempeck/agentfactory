package cmd

import (
	"os"
	"path/filepath"
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

// AC #2: grep -c 'has-session -t =litellm' quickstart.sh == 4 — the four K23 sites. A fifth,
// _write_codex_compat_module's changed-hook restart guard (ADR-024), joined later in the same
// exact-match form; the pin moves to 5 so a sixth site is still a deliberate, reviewed change.
func TestQuickstartLitellmHasSessionUsesExactMatchCount(t *testing.T) {
	content := quickstartScriptContent(t)
	got := strings.Count(content, "has-session -t =litellm")
	if got != 5 {
		t.Errorf("has-session -t =litellm count = %d, want 5 (AC #2's four sites plus the hook writer's restart guard)", got)
	}
}

// AC #3: grep -cE 'sudo -n +npm install' quickstart.sh >= 2
func TestQuickstartSudoNpmInstallHardened(t *testing.T) {
	content := quickstartScriptContent(t)
	got := strings.Count(content, "sudo -n npm install")
	if got < 2 {
		t.Errorf("sudo -n npm install count = %d, want >= 2 (AC #3)", got)
	}
}

// Protective DO-NOT-CHANGE (superseded): pre-K7 this asserted the codex-install sudo call stayed
// *unhardened* (`sudo npm install`, no `-n`), because K23 Phase 1 explicitly excluded it ("Phase
// 2b's K7 replaces it wholesale" — this test's own prior comment predicted its own supersession).
// K7 (issue af-6a25a83c, Phase 2b) has now landed and hardened it inside _ensure_codex_cli; this
// test is retargeted to pin THAT invariant instead of the one K7 was chartered to remove.
func TestQuickstartCodexInstallSudoIsHardened(t *testing.T) {
	content := quickstartScriptContent(t)
	if !strings.Contains(content, "sudo -n npm install -g @openai/codex") {
		t.Error("_ensure_codex_cli's codex-install line is not `sudo -n npm install -g @openai/codex`")
	}
	if strings.Contains(content, "sudo npm install -g @openai/codex") {
		t.Error("an unhardened bare `sudo npm install -g @openai/codex` line still exists — K7 was to replace it wholesale")
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
