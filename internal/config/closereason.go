package config

// Close-reason constants are the shared, typed vocabulary for why an issue-store
// epic was closed (issue #378, HIGH-1). They live in internal/config so that
// every producer (af done, af sling --reset, af down --reset) and the autonomous
// dispatcher's completion query import one definition — the string can no longer
// be typoed independently, and the "exactly-one completion reason" allow-list is
// provably distinct from every reset reason.
//
// Why this matters: a --reset ALSO closes a formula-instance epic. If a reset
// reason ever collided with the completion reason, the dispatcher's K6 completion
// query (Status.IsTerminal() && CloseReason == CloseReasonFormulaComplete) would
// misread a reset as genuine completion and falsely advance a workflow — the
// #378/#413 CRIT-2 false-advance. Keeping these as distinct, test-guarded
// constants makes that collision impossible by construction.
const (
	// CloseReasonFormulaComplete is the universal "formula genuinely done" signal.
	// af done closes the durable formula-instance epic with exactly this reason
	// once every step is closed (done.go), and it is the ONLY reason the dispatcher
	// treats as completion. The literal value is frozen: instance epics closed
	// before this constant existed must still read as complete.
	CloseReasonFormulaComplete = "formula complete"

	// CloseReasonResetSling is the reason af sling --reset uses when it closes an
	// agent's beads before re-dispatching (sling.go).
	CloseReasonResetSling = "reset by af sling --reset"

	// CloseReasonResetFormulaSling is the reason af sling --formula --reset uses
	// when it closes an agent's formula beads (sling.go).
	CloseReasonResetFormulaSling = "reset by af sling --formula --reset"

	// CloseReasonResetDown is the reason af down --reset uses when it closes an
	// agent's beads on teardown (down.go).
	CloseReasonResetDown = "reset by af down --reset"
)
