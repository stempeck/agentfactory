// Package tmux provides a wrapper for tmux session operations via subprocess.
// Ported from internal/tmux/tmux.go with agentfactory simplifications.
package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
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

// Tmux wraps tmux operations. When guard is set (default test build only, via
// guardMode), the client is fail-closed: it reaches no real tmux and panics on
// any destructive op against a production identity (see guardOp).
type Tmux struct {
	guard bool
}

// NewTmux creates a new Tmux wrapper. The guard flag is taken from the
// build-split guardMode (true only in the default, non-integration test build),
// so this single constructor chokepoint guards every construction site at once.
func NewTmux() *Tmux {
	return &Tmux{guard: guardMode}
}

// productionRealOps counts real tmux subprocess execs against production-identity
// sessions. It is incremented immediately before the real cmd.Run() in both
// run() and AttachSession (the latter bypasses run()). Under the guarded default
// build it stays 0 — the guard panics (production) or no-ops (non-production)
// before any exec — making "zero production real ops" an observed, non-vacuous
// fact for the Phase 5 SENTINEL / Phase 3 META.
var productionRealOps atomic.Int64

// recordRealExec records a real tmux subprocess exec of op against name. op is
// the tmux subcommand; only production-identity targets bump the counter.
func recordRealExec(op, name string) {
	if isProductionIdentity(name) {
		productionRealOps.Add(1)
	}
}

// ProductionRealOpCount returns the number of real tmux execs that targeted a
// production identity. Always 0 under the guarded default build.
func ProductionRealOpCount() int64 {
	return productionRealOps.Load()
}

// ResetRealOpCounter resets the production real-exec counter (test-visible).
func ResetRealOpCounter() {
	productionRealOps.Store(0)
}

// targetFromArgs extracts the session/pane target from tmux args — the token
// after "-t" or "-s", with a leading "=" exact-match prefix stripped. Returns
// "" when no target is present (e.g. list-sessions).
func targetFromArgs(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-t" || args[i] == "-s" {
			return strings.TrimPrefix(args[i+1], "=")
		}
	}
	return ""
}

// guardOp applies the fail-closed test guard to a destructive/lifecycle op. It
// returns true when the guard has handled the op and the caller MUST return its
// benign zero value without touching real tmux. It panics (with the named
// failure MSG) when the guard is active and name is a production identity. In
// unguarded builds it returns false and the caller proceeds normally.
func (t *Tmux) guardOp(op, name string) bool {
	if !t.guard {
		return false
	}
	if isProductionIdentity(name) {
		panic(guardMessage(op, name))
	}
	return true
}

// guardMessage builds the single-line named-failure panic message for a
// production-identity op under the guard, attributing the offender via
// runtime.Stack (finding H1; there is no *testing.T in internal/tmux).
func guardMessage(op, name string) string {
	return fmt.Sprintf(
		"af test isolation: production tmux op %q on production-identity session %q (offending test: %s, from runtime.Stack). Default-suite tests must not touch af-* sessions. Either (a) let names resolve to the af-test-<hex>- namespace (the default — do nothing), or (b) move this test behind //go:build integration (runs only under make test-integration).",
		op, name, offendingTestName(),
	)
}

// offendingTestName scans the current goroutine's stack for the first Go test
// function frame (a function whose short name begins with "Test") and returns
// that name. When no test frame is on the stack (e.g. the op ran on a
// background goroutine spawned by non-test code) it returns the documented
// fallback marker; the test runner still reports the in-flight test.
func offendingTestName() string {
	buf := make([]byte, 4096)
	for {
		n := runtime.Stack(buf, false)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}

	for _, line := range strings.Split(string(buf), "\n") {
		if line == "" || line[0] == '\t' || line[0] == ' ' {
			continue // file:line frames are indented
		}
		if strings.HasPrefix(line, "goroutine ") {
			continue
		}
		frame := line
		if i := strings.LastIndex(frame, "/"); i >= 0 {
			frame = frame[i+1:] // drop the package path
		}
		if i := strings.Index(frame, "."); i >= 0 {
			frame = frame[i+1:] // drop the package name, keep the function
		}
		end := len(frame)
		for j := 0; j < len(frame); j++ {
			c := frame[j]
			if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
				end = j
				break
			}
		}
		if strings.HasPrefix(frame[:end], "Test") {
			return frame[:end]
		}
	}
	return "<background goroutine — see runner attribution>"
}

// run executes a tmux command and returns stdout.
func (t *Tmux) run(args ...string) (string, error) {
	cmd := exec.Command("tmux", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	recordRealExec(args[0], targetFromArgs(args))
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
	if t.guard {
		return false // read-only probe: benign zero-value, no real exec (own exec.Command bypasses run())
	}
	cmd := exec.Command("tmux", "-V")
	return cmd.Run() == nil
}

// NewSession creates a new detached tmux session.
func (t *Tmux) NewSession(name, workDir string) error {
	if t.guardOp("new-session", name) {
		return nil
	}
	// Raise scrollback globally BEFORE new-session so the initial agent pane gets a
	// deep buffer — history-limit only sizes windows created after it is set, and
	// new-session creates the first window from the global value (Issue #412, Fix A).
	// Best-effort: a failed apply must never abort session creation. PLACEMENT PIN:
	// this target-less, guard-blind set-option -g is safe ONLY here, after the
	// guardOp("new-session") early-return above and before the new-session exec
	// below — so it inherits NewSession's guard and is never reached under the
	// default-build test guard (F1 / AC #9).
	_ = t.SetGlobalOption("history-limit", "50000")
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
	if t.guard {
		return false, nil // read-only probe: benign zero-value regardless of name, no real exec
	}
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
	if t.guardOp("kill-session", name) {
		return nil
	}
	_, err := t.run("kill-session", "-t", name)
	return err
}

// ListSessions returns all session names.
func (t *Tmux) ListSessions() ([]string, error) {
	if t.guard {
		return nil, nil // read-only probe: benign zero-value, no real exec
	}
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
	if t.guardOp("attach-session", name) {
		return nil
	}
	cmd := exec.Command("tmux", "attach-session", "-t", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	recordRealExec("attach-session", name) // bypasses run(); count here (Gap 10)
	return cmd.Run()
}

// SwitchClient switches the current tmux client to a different session.
// Use this when already inside a tmux session.
func (t *Tmux) SwitchClient(name string) error {
	if t.guardOp("switch-client", name) {
		return nil
	}
	_, err := t.run("switch-client", "-t", name)
	return err
}

// SendKeys sends keystrokes to a session and presses Enter.
// Uses literal mode and a debounce delay between paste and Enter.
func (t *Tmux) SendKeys(session, keys string) error {
	if t.guardOp("send-keys", session) {
		return nil
	}
	return t.SendKeysDebounced(session, keys, debounceMs)
}

// Drop a latched pane out of any tmux mode (copy-mode, view-mode, search) before
// a real injection, so a scrolled/searching pane never silently swallows the
// keystrokes that follow (Issue #412 Fix B — the C-CRIT-2 autonomy trap that
// Phase 2's `mouse on` makes trivial to trigger). It is a STRICT no-op when the
// pane is not in a mode: it gates on #{pane_in_mode} because an unconditional
// `send-keys -X cancel` errors ("not in a mode") on a live pane and could inject
// spurious input. Best-effort — callers ignore the returned error so a failed
// read or cancel never aborts the injection it precedes. It self-guards via
// guardOp because it WRITES (`send-keys -X cancel`), unlike the read-only
// GetPaneCommand which uses the lighter t.guard probe; this keeps it inside the
// ADR-018 fail-closed contract even at the unguarded AcceptBypassPermissionsWarning
// call site.
func (t *Tmux) exitCopyMode(session string) error {
	if t.guardOp("send-keys", session) {
		return nil
	}
	out, err := t.run("list-panes", "-t", session, "-F", "#{pane_in_mode}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "1" {
		return nil // pane is not in a mode: send nothing (strict no-op)
	}
	_, err = t.run("send-keys", "-t", session, "-X", "cancel")
	return err
}

// SendKeysDebounced sends keystrokes with a configurable delay before Enter.
func (t *Tmux) SendKeysDebounced(session, keys string, debounceMillis int) error {
	if t.guardOp("send-keys", session) {
		return nil
	}
	_ = t.exitCopyMode(session) // best-effort: drop a latched pane out of copy-mode first
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
	if t.guardOp("send-keys", session) {
		return nil
	}
	_, err := t.run("send-keys", "-t", session, keys)
	return err
}

// SendKeysDelayed sends keystrokes after a delay (in milliseconds).
func (t *Tmux) SendKeysDelayed(session, keys string, delayMs int) error {
	if t.guardOp("send-keys", session) {
		return nil
	}
	time.Sleep(time.Duration(delayMs) * time.Millisecond)
	return t.SendKeys(session, keys)
}

// NudgeSession sends a message to a Claude Code session reliably.
// Uses: literal mode + 500ms debounce + separate Enter with 3x retry.
func (t *Tmux) NudgeSession(session, message string) error {
	if t.guardOp("send-keys", session) {
		return nil
	}
	_ = t.exitCopyMode(session) // best-effort: drop a latched pane out of copy-mode first
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
	if t.guardOp("send-keys", session) {
		return nil
	}
	// Best-effort copy-mode cancel. This is defense-in-depth: the banner path
	// delegates to SendKeys -> SendKeysDebounced, which already cancels — but the
	// explicit call keeps every injection chokepoint independently resilient.
	_ = t.exitCopyMode(session)
	from = strings.NewReplacer("\n", " ", "\r", " ").Replace(from)
	subject = strings.NewReplacer("\n", " ", "\r", " ").Replace(subject)
	content := fmt.Sprintf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\nNEW MAIL from %s\nSubject: %s\nRun: af mail inbox\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n", from, subject)
	banner := "echo " + shellQuote(content)
	return t.SendKeys(session, banner)
}

// GetPaneCommand returns the current command running in a pane.
func (t *Tmux) GetPaneCommand(session string) (string, error) {
	if t.guard {
		return "", nil // read-only probe: benign zero-value, no real exec
	}
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
	if t.guard {
		return false // read-only probe: benign zero-value, no real exec
	}
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
	if t.guard {
		return false // read-only probe: benign zero-value, no real exec
	}
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
	if t.guard {
		return "", nil // read-only probe: benign zero-value, no real exec
	}
	return t.run("capture-pane", "-p", "-t", session, "-S", fmt.Sprintf("-%d", lines))
}

// ClearHistory clears the scrollback history for a pane.
func (t *Tmux) ClearHistory(pane string) error {
	if t.guardOp("clear-history", pane) {
		return nil
	}
	_, err := t.run("clear-history", "-t", pane)
	return err
}

// RespawnPane kills the current process in a pane and starts a new command.
func (t *Tmux) RespawnPane(pane, command string) error {
	if t.guardOp("respawn-pane", pane) {
		return nil
	}
	_, err := t.run("respawn-pane", "-k", "-t", pane, command)
	return err
}

// SetEnvironment sets an environment variable in the session.
func (t *Tmux) SetEnvironment(session, key, value string) error {
	if t.guardOp("set-environment", session) {
		return nil
	}
	_, err := t.run("set-environment", "-t", session, key, value)
	return err
}

// SetOption sets a session-scoped tmux option (set-option -t). It mirrors
// SetEnvironment: the op carries a -t target, so the ADR-018 guard can protect it
// (the op-string literal "set-option" names the real op in the guard panic).
func (t *Tmux) SetOption(session, name, value string) error {
	if t.guardOp("set-option", session) {
		return nil
	}
	_, err := t.run("set-option", "-t", session, name, value)
	return err
}

// ShowOption reads a session-scoped tmux option value (show-options -t <session>
// -v <name>) and returns it trimmed. Like the other read-only probes
// (GetPaneCommand, CapturePane) it uses the lighter `t.guard` gate rather than
// guardOp: a read carries no destructive risk, so under the test guard it is a
// benign no-op returning the zero value instead of shelling out to real tmux.
// Best-effort by contract — callers treat a read error as "unknown" and must
// never abort on it (used for the Issue #412 mouse read-back, Gap 7).
func (t *Tmux) ShowOption(session, name string) (string, error) {
	if t.guard {
		return "", nil // read-only probe: benign zero-value, no real exec
	}
	out, err := t.run("show-options", "-t", session, "-v", name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SetGlobalOption sets a server-global tmux option (set-option -g). Unlike
// SetOption it is TARGET-LESS and therefore GUARD-BLIND: it carries no -t, so
// guardOp/targetFromArgs cannot protect it. Callers MUST invoke it ONLY from
// within an already-guarded method (e.g. NewSession, after its guardOp
// early-return) — never from an unguarded standalone path — or it would mutate
// the operator's real tmux server under `make test`. (ADR-018 invariant; F1.)
func (t *Tmux) SetGlobalOption(name, value string) error {
	_, err := t.run("set-option", "-g", name, value)
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

	// Best-effort: drop a latched pane out of copy-mode so the Down/Enter below
	// reach the warning dialog. The helper self-guards, so it is safe even though
	// this method has no top-level guardOp (it relies on CapturePane's guard).
	_ = t.exitCopyMode(session)

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

// isProductionIdentity reports whether name is a live factory session identity
// (e.g. "af-manager", "af-watchdog") rather than a hermetic test session
// ("af-test-…") or an unrelated name. It is pure and env-free: a name is a
// production identity iff it carries the "af-" prefix but not the "af-test-"
// test prefix. Fail-closed by construction — anything outside the "af-" family
// is treated as non-production.
func isProductionIdentity(name string) bool {
	return strings.HasPrefix(name, "af-") && !strings.HasPrefix(name, "af-test-")
}

// isTestBinary reports whether the current process is a Go test binary.
// Go test binaries are named "<pkg>.test". Mirrors internal/cmd/prime.go's
// isTestBinary; duplicated here because internal/cmd imports internal/tmux, so
// importing it back would create a cycle. Env-free (os.Executable reads no
// named environment variable).
func isTestBinary() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.HasSuffix(filepath.Base(exe), ".test")
}
