// Package session provides agent session lifecycle management for agentfactory.
package session

import "strings"

// Prefix is the common prefix for agentfactory tmux sessions.
const Prefix = "af-"

// sessionPrefixFn is the seam tests override to redirect every session name to a
// per-test namespace. In production it returns Prefix, so SessionName output is
// byte-identical to the previous direct use of Prefix.
var sessionPrefixFn = func() string { return Prefix }

// SessionName returns the tmux session name for an agent.
func SessionName(agent string) string {
	return sessionPrefixFn() + strings.TrimRight(strings.TrimSpace(agent), "/")
}

// WatchdogSessionName is the single naming authority for the watchdog session.
// It inherits the prefix seam; in production it returns "af-watchdog".
func WatchdogSessionName() string { return SessionName("watchdog") }

// DispatchSessionName is the single naming authority for the dispatcher session.
// It inherits the prefix seam; in production it returns "af-dispatch", matching
// the config.reservedNames reservation of the "dispatch" agent name.
func DispatchSessionName() string { return SessionName("dispatch") }
