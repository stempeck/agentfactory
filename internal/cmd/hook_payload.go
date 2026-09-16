package cmd

import (
	"encoding/json"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// hookPayloadReadTimeout bounds the wait for a hook payload on a pipe or socket stdin. A real hook
// has already written its whole payload before this process reads, so the decode returns at once;
// the deadline only bites when the writer is open with nothing to deliver (a non-hook caller, or the
// Bash-tool socket stdin), where without it json.Decode would block forever (#681 T2).
const hookPayloadReadTimeout = 250 * time.Millisecond

// hookPayload is the part of a hook's stdin JSON af reads. Three fields out of a larger object:
// the host is free to add more, and a decoder that had to know about them would break on a host
// upgrade for no gain. It lives here rather than beside one verb because more than one hook now
// decodes it — prime for session identity, mail for delivered-state.
type hookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Source         string `json:"source"`
	// HookEventName is the event the harness fired. A writer wired into more than one event (mail
	// answers both SessionStart and UserPromptSubmit) must name the RIGHT one back in its
	// hookSpecificOutput, and its own payload is the only place that fact exists.
	HookEventName string `json:"hook_event_name"`
}

// readHookPayload parses the hook payload from a JSON reader.
func readHookPayload(r io.Reader) hookPayload {
	var payload hookPayload
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return hookPayload{}
	}
	return payload
}

// readHookPayloadFromCmd reads this command's own hook payload. A terminal stdin yields the zero
// payload rather than a read: that is ADR-014's permitted defensive shape, where TTY detection
// selects "nothing to read" and never a prompt a human does not know to answer.
//
// The guard interrogates the reader cobra hands us rather than os.Stdin directly, the way
// readMemoryBody does (memory.go:409-414). They are the same object in production, but under
// `go test` os.Stdin is /dev/null — itself a character device — so a check against os.Stdin would
// make the piped path unreachable from a test and leave delivered-state pinned by nothing.
func readHookPayloadFromCmd(cmd *cobra.Command) hookPayload {
	in := cmd.InOrStdin()
	f, isFile := in.(*os.File)
	if !isFile {
		return readHookPayload(in)
	}
	stat, err := f.Stat()
	if err != nil || (stat.Mode()&os.ModeCharDevice) != 0 {
		return hookPayload{}
	}
	// A pipe or socket whose writer is open but silent would block json.Decode forever (#681 T2). A
	// pollable fd (a pipe) gets a read deadline; a non-pollable one (the Bash-tool socket stdin, where
	// SetReadDeadline is unsupported) gets a readability probe. Either way a reader with nothing to
	// deliver yields the zero payload instead of hanging, while a data-carrying reader still decodes.
	// Regular files support neither path and never block, so they fall through to a direct decode.
	if err := f.SetReadDeadline(time.Now().Add(hookPayloadReadTimeout)); err == nil {
		defer f.SetReadDeadline(time.Time{})
	} else if stat.Mode()&os.ModeNamedPipe != 0 || stat.Mode()&os.ModeSocket != 0 {
		if !fdReadableWithin(int(f.Fd()), hookPayloadReadTimeout) {
			return hookPayload{}
		}
	}
	return readHookPayload(in)
}

// fdReadableWithin reports whether fd has data (or EOF) available within d, via a select(2) that does
// not consume anything. It exists for fds that SetReadDeadline cannot bound — a socket wrapped with
// os.NewFile is not registered with the runtime poller — so the decode above only runs once a read
// will not block. An indeterminate result (EINTR-looped, or select itself failing) falls open toward
// reading: the same fail-open direction the rest of this decoder takes.
func fdReadableWithin(fd int, d time.Duration) bool {
	if fd < 0 {
		return true
	}
	tv := syscall.NsecToTimeval(int64(d))
	for {
		ready, err := selectReadable(fd, &tv)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return true
		}
		return ready
	}
}
