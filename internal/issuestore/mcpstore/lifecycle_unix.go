//go:build linux || darwin

package mcpstore

import (
	"os/exec"
	"syscall"
)

// setProcGroup puts the spawned server in its own process group (pgid == its
// pid, since no explicit Pgid is set). This lets killProcGroup reap the server
// AND any children it spawns with a single negated-pid signal, so an
// interrupted run cannot leak the detached subprocess. Must run before
// cmd.Start().
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcGroup SIGTERMs the entire process group led by pid. The negated pid
// addresses the whole group (valid because setProcGroup made the child its own
// group leader, so pgid == pid). Best-effort: a server that already exited
// yields ESRCH, which is not an error worth surfacing.
func killProcGroup(pid int) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
