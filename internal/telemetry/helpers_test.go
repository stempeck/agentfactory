package telemetry

import "testing"

// shrinkCaps lowers the rotation thresholds for the duration of one test. The caps are
// unexported vars for exactly this reason: the rotation contract is only observable by
// crossing it, and crossing the real ten-megabyte cap would mean writing ten megabytes into
// the test temp dir on every run.
func shrinkCaps(t *testing.T, rotateAt, hardCap int64) func() {
	t.Helper()
	origRotate, origHard := rotateAtBytes, hardCapBytes
	rotateAtBytes, hardCapBytes = rotateAt, hardCap
	restore := func() { rotateAtBytes, hardCapBytes = origRotate, origHard }
	t.Cleanup(restore)
	return restore
}

// markExported simulates a successful drain of the given records without going near the
// network, so rotation's cursor-driven behaviour can be exercised on its own.
func markExported(telemetryDir, agent string, exported []StepEvent) error {
	return advanceCursor(telemetryDir, agent, exported)
}
