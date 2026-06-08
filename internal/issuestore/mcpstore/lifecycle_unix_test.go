//go:build linux || darwin

package mcpstore

import (
	"os/exec"
	"testing"
)

// TestSetProcGroupSetsSetpgid guards AC-1: the spawn site must put the Python
// server in its own process group so the whole tree is reapable with a single
// negated-pid signal. This is a structural unit test (no python required) so
// the default `unit` CI job catches accidental removal of the process-group
// wiring even when the integration tier does not run.
func TestSetProcGroupSetsSetpgid(t *testing.T) {
	cmd := exec.Command("true")
	setProcGroup(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("setProcGroup: cmd.SysProcAttr is nil; expected SysProcAttr{Setpgid:true}")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Error("setProcGroup: Setpgid = false, want true (child must lead its own process group so Kill(-pgid) reaps it)")
	}
}
