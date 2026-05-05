// Package tmux provides a wrapper for tmux session operations via subprocess.
// Ported from internal/tmux/tmux.go with agentfactory simplifications.
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Common errors classified from tmux stderr.
var (
	ErrNoServer        = errors.New("no tmux server running")
	ErrSessionExists   = errors.New("session already exists")
	ErrSessionNotFound = errors.New("session not found")
)

// Constants (inlined from internal/constants).
const (
	debounceMs         = 500
	pollInterval       = 100 * time.Millisecond
	claudeStartTimeout = 60 * time.Second
)

var supportedShells = []string{"bash", "zsh", "sh", "fish"}

// IsInsideTmux returns true if the current process is running inside a tmux session.
func IsInsideTmux(tmuxEnv string) bool {
	return tmuxEnv != ""
}

// Tmux wraps tmux operations.
type Tmux struct{}

// NewTmux creates a new Tmux wrapper.
func NewTmux() *Tmux {
	return &Tmux{}
}

// run executes a tmux command and returns stdout.
func (t *Tmux) run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", t.wrapError(err, stderr.String(), args)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// wrapError wraps tmux errors with context and classifies known error types.
func (t *Tmux) wrapError(err error, stderr string, args []string) error {
	stderr = strings.TrimSpace(stderr)

	if strings.Contains(stderr, "no server running") ||
		strings.Contains(stderr, "error connecting to") {
		return ErrNoServer
	}
	if strings.Contains(stderr, "duplicate session") {
		return ErrSessionExists
	}
	if strings.Contains(stderr, "session not found") ||
		strings.Contains(stderr, "can't find session") {
		return ErrSessionNotFound
	}

	if stderr != "" {
		return fmt.Errorf("tmux %s: %s", args[0], stderr)
	}
	return fmt.Errorf("tmux %s: %w", args[0], err)
}

// IsAvailable checks if tmux is installed and can be invoked.
func (t *Tmux) IsAvailable() bool {
	cmd := exec.Command("tmux", "-V")
	return cmd.Run() == nil
}

// NewSession creates a new detached tmux session.
func (t *Tmux) NewSession(name, workDir string) error {
	args := []string{"new-session", "-d", "-s", name}
	if workDir != "" {
		args = append(args, "-c", workDir)
	}
	_, err := t.run(args...)
	return err
}

// HasSession checks if a session exists (exact match).
// Uses "=" prefix for exact matching, preventing prefix matches.
func (t *Tmux) HasSession(name string) (bool, error) {
	_, err := t.run("has-session", "-t", "="+name)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrNoServer) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// KillSession terminates a tmux session.
func (t *Tmux) KillSession(name string) error {
	_, err := t.run("kill-session", "-t", name)
	return err
}

// ListSessions returns all session names.
func (t *Tmux) ListSessions() ([]string, error) {
	out, err := t.run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		if errors.Is(err, ErrNoServer) {
			return nil, nil
		}
		return nil, err
	}

	if out == "" {
		return nil, nil
	}

	return strings.Split(out, "\n"), nil
}

// AttachSession attaches to an existing session with stdio wired directly.
// This replaces the current terminal with the tmux session.
func (t *Tmux) AttachSession(name string) error {
	cmd := exec.Command("tmux", "attach-session", "-t", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// SwitchClient switches the current tmux client to a different session.
// Use this when already inside a tmux session.
func (t *Tmux) SwitchClient(name string) error {
	_, err := t.run("switch-client", "-t", name)
	return err
}

// SendKeys sends keystrokes to a session and presses Enter.
// Uses literal mode and a debounce delay between paste and Enter.
func (t *Tmux) SendKeys(session, keys string) error {
	return t.SendKeysDebounced(session, keys, debounceMs)
}

// SendKeysDebounced sends keystrokes with a configurable delay before Enter.
func (t *Tmux) SendKeysDebounced(session, keys string, debounceMillis int) error {
	if _, err := t.run("send-keys", "-t", session, "-l", keys); err != nil {
		return err
	}
	if debounceMillis > 0 {
		time.Sleep(time.Duration(debounceMillis) * time.Millisecond)
	}
	_, err := t.run("send-keys", "-t", session, "Enter")
	return err
}

// SendKeysRaw sends keystrokes without adding Enter.
func (t *Tmux) SendKeysRaw(session, keys string) error {
	_, err := t.run("send-keys", "-t", session, keys)
	return err
}

// SendKeysDelayed sends keystrokes after a delay (in milliseconds).
func (t *Tmux) SendKeysDelayed(session, keys string, delayMs int) error {
	time.Sleep(time.Duration(delayMs) * time.Millisecond)
	return t.SendKeys(session, keys)
}

// NudgeSession sends a message to a Claude Code session reliably.
// Uses: literal mode + 500ms debounce + separate Enter with 3x retry.
func (t *Tmux) NudgeSession(session, message string) error {
	if _, err := t.run("send-keys", "-t", session, "-l", message); err != nil {
		return err
	}

	time.Sleep(500 * time.Millisecond)

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("failed to send Enter after 3 attempts: %w", lastErr)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// SendNotificationBanner sends a visible notification banner to a tmux session.
func (t *Tmux) SendNotificationBanner(session, from, subject string) error {
	from = strings.NewReplacer("\n", " ", "\r", " ").Replace(from)
	subject = strings.NewReplacer("\n", " ", "\r", " ").Replace(subject)
	content := fmt.Sprintf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\nNEW MAIL from %s\nSubject: %s\nRun: af mail inbox\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n", from, subject)
	banner := "echo " + shellQuote(content)
	return t.SendKeys(session, banner)
}

// GetPaneCommand returns the current command running in a pane.
func (t *Tmux) GetPaneCommand(session string) (string, error) {
	out, err := t.run("list-panes", "-t", session, "-F", "#{pane_current_command}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsAgentRunning checks if an agent appears to be running in the session.
// If expectedPaneCommands is non-empty, the pane command must match one of them.
// If empty, any non-shell command counts as "agent running".
func (t *Tmux) IsAgentRunning(session string, expectedPaneCommands ...string) bool {
	cmd, err := t.GetPaneCommand(session)
	if err != nil {
		return false
	}

	if len(expectedPaneCommands) > 0 {
		for _, expected := range expectedPaneCommands {
			if expected != "" && cmd == expected {
				return true
			}
		}
		return false
	}

	for _, shell := range supportedShells {
		if cmd == shell {
			return false
		}
	}
	return cmd != ""
}

// IsClaudeRunning checks if Claude appears to be running in the session.
// Claude can report as "node", "claude", or a version number like "2.0.76".
func (t *Tmux) IsClaudeRunning(session string) bool {
	if t.IsAgentRunning(session, "node", "claude") {
		return true
	}
	cmd, err := t.GetPaneCommand(session)
	if err != nil {
		return false
	}
	matched, _ := regexp.MatchString(`^\d+\.\d+\.\d+`, cmd)
	return matched
}

// CapturePane captures the visible content of a pane.
func (t *Tmux) CapturePane(session string, lines int) (string, error) {
	return t.run("capture-pane", "-p", "-t", session, "-S", fmt.Sprintf("-%d", lines))
}

// ClearHistory clears the scrollback history for a pane.
func (t *Tmux) ClearHistory(pane string) error {
	_, err := t.run("clear-history", "-t", pane)
	return err
}

// RespawnPane kills the current process in a pane and starts a new command.
func (t *Tmux) RespawnPane(pane, command string) error {
	_, err := t.run("respawn-pane", "-k", "-t", pane, command)
	return err
}

// SetEnvironment sets an environment variable in the session.
func (t *Tmux) SetEnvironment(session, key, value string) error {
	_, err := t.run("set-environment", "-t", session, key, value)
	return err
}

// WaitForShellReady polls until the pane is running a shell command.
func (t *Tmux) WaitForShellReady(session string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd, err := t.GetPaneCommand(session)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		for _, shell := range supportedShells {
			if cmd == shell {
				return nil
			}
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("timeout waiting for shell")
}

// WaitForCommand polls until the pane is NOT running one of the excluded commands.
func (t *Tmux) WaitForCommand(session string, excludeCommands []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd, err := t.GetPaneCommand(session)
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}
		excluded := false
		for _, exc := range excludeCommands {
			if cmd == exc {
				excluded = true
				break
			}
		}
		if !excluded {
			return nil
		}
		time.Sleep(pollInterval)
	}
	return fmt.Errorf("timeout waiting for command (still running excluded command)")
}

// AcceptBypassPermissionsWarning dismisses the Claude Code bypass permissions warning.
func (t *Tmux) AcceptBypassPermissionsWarning(session string) error {
	time.Sleep(5 * time.Second)

	content, err := t.CapturePane(session, 30)
	if err != nil {
		return err
	}

	if !strings.Contains(content, "Bypass Permissions mode") {
		return nil
	}

	if _, err := t.run("send-keys", "-t", session, "Down"); err != nil {
		return err
	}

	time.Sleep(200 * time.Millisecond)

	if _, err := t.run("send-keys", "-t", session, "Enter"); err != nil {
		return err
	}

	return nil
}

// ClaudeStartTimeout returns the timeout for waiting for Claude to start.
func ClaudeStartTimeout() time.Duration {
	return claudeStartTimeout
}

// SupportedShells returns the list of recognized shell commands.
func SupportedShells() []string {
	return supportedShells
}
