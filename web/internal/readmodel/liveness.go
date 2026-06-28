package readmodel

import (
	"bytes"
	"context"
	osexec "os/exec"
	"strings"
)

// TmuxLiveness is the real liveness probe. It re-shells `tmux list-sessions -F '#{session_name}'`
// as an argv array (mirroring internal/tmux/tmux.go:248-266) — the web module cannot import
// internal/tmux. "no server running" is treated as zero sessions, not a hard error, so the Floor
// view shows an honest empty skyline rather than an error when tmux simply isn't up.
type TmuxLiveness struct {
	execCommand func(ctx context.Context, name string, args ...string) *osexec.Cmd
}

// NewTmuxLiveness returns a Liveness backed by the real tmux binary on PATH.
func NewTmuxLiveness() *TmuxLiveness {
	return &TmuxLiveness{execCommand: osexec.CommandContext}
}

// Sessions returns the current tmux session names. ErrNoServer (no tmux server running) and
// empty output both yield an empty slice with no error.
func (t *TmuxLiveness) Sessions(ctx context.Context) ([]string, error) {
	cmd := t.execCommand(ctx, "tmux", "list-sessions", "-F", "#{session_name}")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isNoServer(stderr.String()) {
			return nil, nil
		}
		return nil, err
	}
	return splitSessions(stdout.String()), nil
}

// splitSessions parses tmux `list-sessions -F '#{session_name}'` stdout into session names.
// Empty output ⇒ nil (mirrors internal/tmux/tmux.go:248-266, where ErrNoServer/empty⇒nil).
// This is the single parser used by both the live path (above) and its test.
func splitSessions(out string) []string {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// isNoServer classifies the "no tmux server" stderr the same way internal/tmux/tmux.go:183-185
// does (ErrNoServer): treat it as benign (no sessions), never a hard failure.
func isNoServer(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no server running") || strings.Contains(s, "error connecting to")
}
