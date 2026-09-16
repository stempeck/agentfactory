//go:build linux

package cmd

import "syscall"

// selectReadable is split per platform because syscall.Select is not portable: linux's FdSet holds
// int64 words and its Select returns the ready count directly.
func selectReadable(fd int, tv *syscall.Timeval) (bool, error) {
	var rset syscall.FdSet
	rset.Bits[fd/64] |= int64(1) << (uint(fd) % 64)
	n, err := syscall.Select(fd+1, &rset, nil, nil, tv)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
