package cmd

import (
	"os"
	"strings"
	"testing"
)

// TestAgentGenAllPluginExemptScriptContent pins Phase 4's K10 change (issue #538):
// agent-gen-all.sh's two source-repo orphan passes must consult .agentfactory/plugins.json
// and PRESERVE a plugin-recorded formula/template instead of orphan-deleting it. It is a
// script-content assertion (the idiom of TestNoStaleBeadsRefsInShellScripts / the Gap-15
// pins) because the script runs `af down --all` + `af formula agent-gen` and needs af on
// PATH — the behavioral end-to-end lives in the //go:build integration lane
// (TestFormulaSyncBehavior).
//
// It asserts BOTH exemption branches exist, each consults the manifest, jq is guarded so
// `set -euo pipefail` never aborts (XR-4), and — the load-bearing pin — each preserve
// happens BEFORE the corresponding `rm`, so a recorded artifact is exempted, not deleted.
func TestAgentGenAllPluginExemptScriptContent(t *testing.T) {
	data, err := os.ReadFile("../../agent-gen-all.sh")
	if err != nil {
		t.Fatalf("read agent-gen-all.sh: %v", err)
	}
	body := string(data)

	// Both passes consult the manifest (AC#1: grep -c 'plugins.json' >= 2).
	if got := strings.Count(body, "plugins.json"); got < 2 {
		t.Errorf("agent-gen-all.sh references plugins.json %d time(s), want >= 2 (formula pass + template pass must each consult the manifest)", got)
	}

	// Both preserve messages exist (AC#1: grep -c 'preserving plugin' >= 2).
	if got := strings.Count(body, "preserving plugin"); got < 2 {
		t.Errorf("agent-gen-all.sh has %d 'preserving plugin' message(s), want >= 2 (formula + template preserve messages)", got)
	}
	if !strings.Contains(body, "preserving plugin formula") {
		t.Error("agent-gen-all.sh missing the FORMULA-pass preserve message ('preserving plugin formula')")
	}
	if !strings.Contains(body, "preserving plugin template") {
		t.Error("agent-gen-all.sh missing the TEMPLATE-pass preserve message ('preserving plugin template')")
	}

	// jq is guarded so an absent jq never aborts the set -e regen (AC#3, XR-4). The literal
	// `command -v jq` has no pre-feature occurrence (the script used no jq), so it is the
	// tight gate (the outline's looser grep also matched the pre-existing `... 2>/dev/null || true`).
	if !strings.Contains(body, "command -v jq") {
		t.Error("agent-gen-all.sh does not guard jq with 'command -v jq' — an absent jq would abort the regen under set -euo pipefail (XR-4)")
	}

	// Insertion order (the irreversible-within-run pin): each preserve must appear BEFORE its
	// `rm`, else a recorded plugin artifact is deleted before the exemption can fire.
	formulaPreserveIdx := strings.Index(body, "preserving plugin formula")
	formulaRmIdx := strings.Index(body, `rm "$f"`)
	if formulaPreserveIdx == -1 || formulaRmIdx == -1 {
		t.Fatalf("agent-gen-all.sh missing formula-pass landmarks (preserve idx=%d, rm idx=%d)", formulaPreserveIdx, formulaRmIdx)
	}
	if formulaPreserveIdx >= formulaRmIdx {
		t.Errorf("agent-gen-all.sh: FORMULA-pass preserve (offset %d) must precede `rm \"$f\"` (offset %d) — else a recorded plugin formula is deleted before the exemption fires", formulaPreserveIdx, formulaRmIdx)
	}

	templatePreserveIdx := strings.Index(body, "preserving plugin template")
	templateRmIdx := strings.Index(body, `rm "$tmpl_file"`)
	if templatePreserveIdx == -1 || templateRmIdx == -1 {
		t.Fatalf("agent-gen-all.sh missing template-pass landmarks (preserve idx=%d, rm idx=%d)", templatePreserveIdx, templateRmIdx)
	}
	if templatePreserveIdx >= templateRmIdx {
		t.Errorf("agent-gen-all.sh: TEMPLATE-pass preserve (offset %d) must precede `rm \"$tmpl_file\"` (offset %d) — else a recorded plugin template is deleted before the exemption fires", templatePreserveIdx, templateRmIdx)
	}

	// The consult must stay gated so an absent manifest is byte-identical to today (AC-6):
	// the `[ -f ` gate on the manifest path must be present.
	if !strings.Contains(body, `plugins_manifest`) {
		t.Error("agent-gen-all.sh does not define a plugins_manifest path variable — the consult must be gated on `[ -f \"$plugins_manifest\" ]` so an absent manifest is byte-identical (AC-6)")
	}
}
