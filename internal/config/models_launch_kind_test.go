package config

import (
	"strings"
	"testing"
)

// TestProfileCannotDeclareLaunchKind: the session's own launch line is the only carrier of the launch
// kind, so a profile that exported "respawn" would keep every startup it launches from being measured.
func TestProfileCannotDeclareLaunchKind(t *testing.T) {
	for _, value := range []string{"respawn", ""} {
		err := validateModelProfile("forger", map[string]string{"ANTHROPIC_MODEL": "claude-opus-5-5", "AF_LAUNCH_KIND": value})
		if err == nil || !strings.Contains(err.Error(), "reserved for the session manager") {
			t.Errorf("a profile declaring AF_LAUNCH_KIND=%q: err = %v, want it rejected as reserved for the session manager", value, err)
		}
	}
}
