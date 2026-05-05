package session

import "testing"

func TestSessionName(t *testing.T) {
	tests := []struct {
		agent    string
		expected string
	}{
		{"manager", "af-manager"},
		{"supervisor", "af-supervisor"},
		{"worker", "af-worker"},
		{"", "af-"},
		{"agent/", "af-agent"},
		{"agent//", "af-agent"},
		{" agent ", "af-agent"},
		{" agent/ ", "af-agent"},
	}

	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			got := SessionName(tt.agent)
			if got != tt.expected {
				t.Errorf("SessionName(%q) = %q, want %q", tt.agent, got, tt.expected)
			}
		})
	}
}

func TestPrefix(t *testing.T) {
	if Prefix != "af-" {
		t.Errorf("Prefix = %q, want %q", Prefix, "af-")
	}
}
