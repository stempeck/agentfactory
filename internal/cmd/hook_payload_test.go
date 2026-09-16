package cmd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// t1t12_runWithTimeout runs fn in a goroutine and reports whether it returned within d. A false
// return is the whole point of these tests: readHookPayloadFromCmd (and the prime path that shares
// its decoder) must not block on an open pipe/socket whose writer is silent but still attached, so a
// hang shows up as a timeout rather than a wedged test binary. The prefix keeps the helper from
// colliding with sibling test helpers other threads add to package cmd concurrently.
func t1t12_runWithTimeout(t *testing.T, d time.Duration, fn func()) bool {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestReadHookPayloadFromCmd_OpenPipeNoDataDoesNotBlock pins T2-a: a real pipe fd whose writer is
// still OPEN and has produced zero bytes must yield the zero payload promptly, not block on Read.
// RED at head — the char-device-only guard falls through to json.Decode, which blocks. The write end
// is intentionally left open (never closed) to reproduce the reported hang.
func TestReadHookPayloadFromCmd_OpenPipeNoDataDoesNotBlock(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })

	cmd := &cobra.Command{}
	cmd.SetIn(r)

	var got hookPayload
	if !t1t12_runWithTimeout(t, 3*time.Second, func() { got = readHookPayloadFromCmd(cmd) }) {
		t.Fatal("readHookPayloadFromCmd blocked on an open pipe with no data")
	}
	if got != (hookPayload{}) {
		t.Errorf("open pipe with no data must yield the zero payload, got %+v", got)
	}
}

// TestReadHookPayloadFromCmd_OpenSocketNoDataDoesNotBlock pins T2-b: the observed Bash-tool SOCKET
// stdin. A socketpair end wrapped as *os.File is not a char device, so at head control falls to the
// blocking decode. The peer end is left open and silent to reproduce the hang. RED at head.
func TestReadHookPayloadFromCmd_OpenSocketNoDataDoesNotBlock(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	near := os.NewFile(uintptr(fds[0]), "sock-near")
	peer := os.NewFile(uintptr(fds[1]), "sock-peer")
	if near == nil || peer == nil {
		t.Fatal("os.NewFile returned nil for a socketpair fd")
	}
	t.Cleanup(func() { near.Close(); peer.Close() })

	cmd := &cobra.Command{}
	cmd.SetIn(near)

	var got hookPayload
	if !t1t12_runWithTimeout(t, 3*time.Second, func() { got = readHookPayloadFromCmd(cmd) }) {
		t.Fatal("readHookPayloadFromCmd blocked on an open socket with no data")
	}
	if got != (hookPayload{}) {
		t.Errorf("open socket with no data must yield the zero payload, got %+v", got)
	}
}

// TestReadHookPayloadFromCmd_RegularFileWithPayloadDecodes is protective (T2-c): a genuine hook
// payload sitting on a REGULAR FILE fd must still decode after the fix. Passes now; guards a fix that
// throws out every non-terminal reader.
func TestReadHookPayloadFromCmd_RegularFileWithPayloadDecodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte(`{"session_id":"sess-X","transcript_path":"/t","source":"startup"}`), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open payload: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	cmd := &cobra.Command{}
	cmd.SetIn(f)

	got := readHookPayloadFromCmd(cmd)
	if got.SessionID != "sess-X" {
		t.Errorf("regular-file payload did not decode: session_id = %q, want %q", got.SessionID, "sess-X")
	}
}

// TestReadHookPayloadFromCmd_PipeWithDataThenEOFDecodes is protective (T2-d): the production hook
// path — a pipe carrying a payload whose writer then closes — must still decode, mirroring what
// primeHookCapturing stages. Passes now; guards a fix that refuses ALL pipes rather than only ones
// that would block.
func TestReadHookPayloadFromCmd_PipeWithDataThenEOFDecodes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(`{"session_id":"sess-pipe","source":"startup"}`); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	w.Close()
	t.Cleanup(func() { r.Close() })

	cmd := &cobra.Command{}
	cmd.SetIn(r)

	var got hookPayload
	if !t1t12_runWithTimeout(t, 3*time.Second, func() { got = readHookPayloadFromCmd(cmd) }) {
		t.Fatal("readHookPayloadFromCmd blocked on a pipe with data then EOF")
	}
	if got.SessionID != "sess-pipe" {
		t.Errorf("pipe-with-data payload did not decode: session_id = %q, want %q", got.SessionID, "sess-pipe")
	}
}
