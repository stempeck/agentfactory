package config

import (
	"strings"
	"testing"
)

// Pinning tests for PR #595 unresolved review comments (fable-increment), config layer.

// BODY-1/F2 INVERTED (issue #600, K9 ledger item 2). The original pin asserted `daily` was NOT
// in the default: it sums over ALL sessions, so any agent's spend moved it, resetting the
// watchdog's silence hash and masking a hung agent. That was a config-layer workaround for a
// watchdog-layer defect, and #600 relocated it — the watchdog now strips sentinel-marked
// statusline lines before hashing, so a moving `daily` (or a ticking `elapsed`) can no longer
// reset anything.
//
// The requirement the original pin was protecting has therefore MOVED, not disappeared:
// internal/cmd/watchdog_test.go's TestCheckSilence_StatuslineOnlyChange_StillTrips now carries
// it, with a cross-session-moved daily figure in its fixture. Renamed because
// "DailyNotInDefaultSet" is the opposite of the requirement it now guards.
func TestFableIncr_BODY1_DailyAndElapsedInDefaultSet(t *testing.T) {
	cfg, err := LoadStatuslineConfig(t.TempDir()) // absent file ⇒ the real default set
	if err != nil {
		t.Fatalf("LoadStatuslineConfig: %v", err)
	}
	has := func(name string) bool {
		for _, e := range cfg.Elements {
			if e == name {
				return true
			}
		}
		return false
	}

	// Both formerly-excluded elements must now be present — that IS the #600 requirement.
	for _, must := range []string{"daily", "elapsed"} {
		if !has(must) {
			t.Errorf("BODY-1 inverted: %q must be in the default statusline set now that the watchdog "+
				"is statusline-blind; default = %v", must, cfg.Elements)
		}
	}
	// Guard: restoring them must not drop anything that was already there. `diff` is included
	// where the original guard list omitted it.
	for _, must := range []string{"model", "dir", "branch", "diff", "context", "session"} {
		if !has(must) {
			t.Errorf("BODY-1 guard: default must still contain %q; got %v", must, cfg.Elements)
		}
	}
}

// T5/F6 (Consider) — the valid-element registry is duplicated across sites. Chosen fix
// (decisions.md D6): partial consolidation on the config side (whitelist + validate-error string
// derived from one canonical list). This is a protective/characterization guard: it passes NOW
// and must keep passing after consolidation — it catches any drift the refactor might introduce
// (a default element not on the whitelist, or an error message that stops listing a valid name).
func TestFableIncr_T5_ElementRegistryConsistency(t *testing.T) {
	// Every default element must be a valid (whitelisted) element. Read through the exported
	// accessor rather than the canonical var: after issue #600 collapsed the two lists, ranging
	// over statuslineElementNames would compare the whitelist against the list it is derived
	// from and assert nothing. Going through DefaultStatuslineElements keeps the check honest
	// if the two ever diverge again, and exercises its defensive-copy contract.
	for _, e := range DefaultStatuslineElements() {
		if !validStatuslineElements[e] {
			t.Errorf("T5: default element %q is not on the validity whitelist — registry drift", e)
		}
	}

	// The validate-error string must NAME every whitelisted element (the human-facing "valid:
	// ..." list is one of the duplicated sites; consolidation must keep it complete).
	err := validateStatuslineConfig(&StatuslineConfig{Elements: []string{"definitely-not-an-element"}})
	if err == nil {
		t.Fatal("T5: an unknown element must be rejected")
	}
	msg := err.Error()
	for name := range validStatuslineElements {
		if !strings.Contains(msg, name) {
			t.Errorf("T5: the validation error must list every valid element name; %q is missing from %q", name, msg)
		}
	}

	// The whitelist AND the error's "valid: ..." list are now DERIVED from the single canonical
	// statuslineElementNames (D6). Assert the derivation actually holds, so a future hand-edit that
	// reintroduces an independent literal is caught: the whitelist must be exactly the canonical set,
	// and the error string must name each canonical element in order.
	if len(validStatuslineElements) != len(statuslineElementNames) {
		t.Errorf("T5: derived whitelist size %d != canonical list size %d — a second literal drifted in", len(validStatuslineElements), len(statuslineElementNames))
	}
	for _, name := range statuslineElementNames {
		if !validStatuslineElements[name] {
			t.Errorf("T5: canonical element %q missing from the derived whitelist", name)
		}
		if !strings.Contains(msg, name) {
			t.Errorf("T5: canonical element %q missing from the derived validate-error list %q", name, msg)
		}
	}
}
