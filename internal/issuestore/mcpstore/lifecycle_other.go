//go:build !linux && !darwin

package mcpstore

import "os/exec"

// Non-unix platforms have no Setpgid / process-group semantics in syscall, so
// the process-group backstop is a no-op there. The repo only targets
// linux/darwin (CI is ubuntu); these stubs keep a hypothetical Windows build
// compiling.
func setProcGroup(cmd *exec.Cmd) {}

func killProcGroup(pid int) error { return nil }
