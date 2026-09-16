//go:build darwin

package cmd

import "syscall"

// selectReadable is split per platform because syscall.Select is not portable: darwin's FdSet holds
// int32 words and its Select returns no ready count, so readiness is read back from the set the
// kernel rewrote in place.
func selectReadable(fd int, tv *syscall.Timeval) (bool, error) {
	var rset syscall.FdSet
	word, bit := fd/32, int32(1)<<(uint(fd)%32)
	rset.Bits[word] |= bit
	if err := syscall.Select(fd+1, &rset, nil, nil, tv); err != nil {
		return false, err
	}
	return rset.Bits[word]&bit != 0, nil
}
