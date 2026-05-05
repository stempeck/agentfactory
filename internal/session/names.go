// Package session provides agent session lifecycle management for agentfactory.
package session

import "strings"

// Prefix is the common prefix for agentfactory tmux sessions.
const Prefix = "af-"

// SessionName returns the tmux session name for an agent.
func SessionName(agent string) string {
	return Prefix + strings.TrimRight(strings.TrimSpace(agent), "/")
}
