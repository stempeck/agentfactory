package config

import "testing"

// TestCloseReasonFormulaComplete_Value pins the exact completion sentinel. This
// string is the universal "formula genuinely done" signal closed by af done
// (done.go) and read by the dispatcher's completion query (K6). It MUST remain
// "formula complete" so old instance epics closed before this constant existed
// still read as complete.
func TestCloseReasonFormulaComplete_Value(t *testing.T) {
	if CloseReasonFormulaComplete != "formula complete" {
		t.Errorf("CloseReasonFormulaComplete = %q, want %q", CloseReasonFormulaComplete, "formula complete")
	}
}

// TestResetCloseReasons_NeverEqualCompletion is the HIGH-1 provenance guard: a
// --reset also closes an epic, so if any reset reason collided with the
// completion reason the dispatcher could misread a reset as completion (the
// #378/#413 CRIT-2 false-advance). Each reset constant must be non-empty and
// provably distinct from the completion constant, and must match the literals
// the reset paths historically used (sling.go:143/294, down.go:207).
func TestResetCloseReasons_NeverEqualCompletion(t *testing.T) {
	resets := map[string]string{
		"CloseReasonResetSling":        CloseReasonResetSling,
		"CloseReasonResetFormulaSling": CloseReasonResetFormulaSling,
		"CloseReasonResetDown":         CloseReasonResetDown,
	}
	for name, r := range resets {
		if r == "" {
			t.Errorf("%s is empty", name)
		}
		if r == CloseReasonFormulaComplete {
			t.Errorf("%s == CloseReasonFormulaComplete (%q) — false-advance risk (HIGH-1)", name, r)
		}
	}
	if CloseReasonResetSling != "reset by af sling --reset" {
		t.Errorf("CloseReasonResetSling = %q, want %q", CloseReasonResetSling, "reset by af sling --reset")
	}
	if CloseReasonResetFormulaSling != "reset by af sling --formula --reset" {
		t.Errorf("CloseReasonResetFormulaSling = %q, want %q", CloseReasonResetFormulaSling, "reset by af sling --formula --reset")
	}
	if CloseReasonResetDown != "reset by af down --reset" {
		t.Errorf("CloseReasonResetDown = %q, want %q", CloseReasonResetDown, "reset by af down --reset")
	}
}
