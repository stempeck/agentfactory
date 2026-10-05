package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// extractGoFunction mirrors extractShellFunction (quickstart_test.go) for Go source: anchor on the
// function's declaration (a top-level `func name(` or a `name = func(` literal assigned to a
// package var), then brace-count to the matching close brace. Anchoring on the declaration — never a
// preceding doc comment — matters here: runInstallAgents's own GoDoc contains "then runs" (would
// false-positive rerunInstructionPattern if the comment were included).
func extractGoFunction(content, funcName string) string {
	start := -1
	for _, marker := range []string{"func " + funcName + "(", funcName + " = func("} {
		if idx := strings.Index(content, marker); idx != -1 {
			start = idx
			break
		}
	}
	if start == -1 {
		return ""
	}
	braceIdx := strings.Index(content[start:], "{")
	if braceIdx == -1 {
		return ""
	}
	braceStart := start + braceIdx
	depth := 0
	inBody := false
	for i := braceStart; i < len(content); i++ {
		switch content[i] {
		case '{':
			depth++
			inBody = true
		case '}':
			depth--
			if inBody && depth == 0 {
				return content[start : i+1]
			}
		}
	}
	return content[start:]
}

// rerunInstructionPattern matches a rerun/re-run INSTRUCTION ("then rerun"/"then re-run"/"then
// run"), deliberately narrower than a bare "rerun"/"re-run" substring: quickstart.sh's own
// master-key comment ("reused on every rerun") and setup_telemetry's "then re-run to republish" both
// use "rerun" legitimately, as ordinary vocabulary, never preceded by "then" as an instruction
// trigger. See todos/fable-implement/decisions.md D2.
var rerunInstructionPattern = regexp.MustCompile(`(?i)then\s+(re-?run|run)`)

// rerunTripwireBashTargets/rerunTripwireGoTargets is the scan-target list settled in
// todos/fable-implement/decisions.md D3: the codex-subscription bootstrap functions this issue
// introduced or rewrote, per the GATE-2-approved consumers.md sweep.
var rerunTripwireBashTargets = []string{
	"setup_litellm",
	"_ensure_codex_cli",
	"_codex_session_valid",
	"_ensure_codex_session",
	"_litellm_auth_mode_read",
	"_litellm_auth_mode_write",
}

var rerunTripwireGoTargets = []string{
	"runInstallAgents",
	"promptCodexInstallConsent",
	"lookPathCodex",
	"codexHomeWritable",
	"codexSessionValid",
	"runCodexDeviceAuth",
	"preflightCodexSubscription",
}

// TestNoRerunInstructionsInBootstrapPaths (T-12) pins AC-12: no bootstrap code path may end by
// instructing the operator to re-run a step. It scans the bodies of the codex-subscription
// bootstrap functions for a rerun-instruction phrase, scoped to function bodies (never docs, never
// the whole file — see decisions.md D1).
func TestNoRerunInstructionsInBootstrapPaths(t *testing.T) {
	root := findModuleRoot(t)
	qs, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	installSrc, err := os.ReadFile(filepath.Join(root, "internal/cmd/install.go"))
	if err != nil {
		t.Fatalf("reading install.go: %v", err)
	}

	for _, name := range rerunTripwireBashTargets {
		body := extractShellFunction(string(qs), name)
		if body == "" {
			t.Fatalf("could not extract %s() from quickstart.sh — has it been renamed? update rerunTripwireBashTargets", name)
		}
		if m := rerunInstructionPattern.FindString(body); m != "" {
			t.Errorf("%s() contains a rerun-instruction phrase (%q) — AC-12 forbids a bootstrap path ending by telling the operator to re-run", name, m)
		}
	}

	for _, name := range rerunTripwireGoTargets {
		body := extractGoFunction(string(installSrc), name)
		if body == "" {
			t.Fatalf("could not extract %s() from install.go — has it been renamed? update rerunTripwireGoTargets", name)
		}
		if m := rerunInstructionPattern.FindString(body); m != "" {
			t.Errorf("%s() contains a rerun-instruction phrase (%q) — AC-12 forbids a bootstrap path ending by telling the operator to re-run", name, m)
		}
	}

	t.Run("master_key_comment_not_reworded", func(t *testing.T) {
		if !strings.Contains(string(qs), "reused on every rerun (regenerating would orphan") {
			t.Error("the setup_litellm master-key comment must survive verbatim — T-12 must be satisfied by regex scoping, not by rewording production comments (DO-NOT-CHANGE)")
		}
	})
}
