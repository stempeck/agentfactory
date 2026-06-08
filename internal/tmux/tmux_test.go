package tmux

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func hasTmux() bool {
	return exec.Command("tmux", "-V").Run() == nil
}

func TestIsAvailable(t *testing.T) {
	tx := NewTmux()
	if !tx.IsAvailable() {
		t.Skip("tmux not available")
	}
	// If we got here, IsAvailable returned true — which is correct since tmux is installed
}

func TestHasSessionNoServer(t *testing.T) {
	// This test verifies that HasSession returns false (not error) when
	// checking a non-existent session. It works even when tmux server is running.
	tx := NewTmux()
	exists, err := tx.HasSession("af-test-definitely-does-not-exist")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if exists {
		t.Fatal("HasSession: expected false for non-existent session")
	}
}

func TestWrapError(t *testing.T) {
	tx := NewTmux()

	tests := []struct {
		name     string
		stderr   string
		expected error
	}{
		{"no server", "no server running on /tmp/tmux-501/default", ErrNoServer},
		{"error connecting", "error connecting to /tmp/tmux-501/default", ErrNoServer},
		{"duplicate", "duplicate session: foo", ErrSessionExists},
		{"not found", "session not found: bar", ErrSessionNotFound},
		{"cant find", "can't find session: baz", ErrSessionNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tx.wrapError(errors.New("exit 1"), tt.stderr, []string{"test-cmd"})
			if !errors.Is(err, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, err)
			}
		})
	}
}

func TestSupportedShells(t *testing.T) {
	shells := SupportedShells()
	if len(shells) == 0 {
		t.Fatal("SupportedShells returned empty list")
	}
	// Must contain at least bash and zsh
	hasBash := false
	hasZsh := false
	for _, s := range shells {
		if s == "bash" {
			hasBash = true
		}
		if s == "zsh" {
			hasZsh = true
		}
	}
	if !hasBash {
		t.Fatal("SupportedShells missing bash")
	}
	if !hasZsh {
		t.Fatal("SupportedShells missing zsh")
	}
}

func TestSendNotificationBanner_Injection(t *testing.T) {
	payloads := []struct {
		name    string
		subject string
	}{
		{"single quote breakout", "'; rm -rf / #"},
		{"backtick command sub", "`whoami`"},
		{"dollar command sub", "$(id)"},
		{"semicolon injection", "; echo pwned"},
		{"nested quote payload", "'$(cat /etc/passwd)'"},
	}

	for _, tt := range payloads {
		t.Run(tt.name, func(t *testing.T) {
			quoted := shellQuote(tt.subject)
			if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
				t.Errorf("shellQuote(%q) = %q, want single-quoted wrapper", tt.subject, quoted)
			}
			inner := quoted[1 : len(quoted)-1]
			if strings.Contains(inner, "'") && !strings.Contains(inner, `'\''`) {
				t.Errorf("shellQuote(%q) has unescaped single quote in inner: %q", tt.subject, inner)
			}
		})
	}
}

func TestSendNotificationBanner_Display(t *testing.T) {
	cases := []struct {
		name    string
		subject string
	}{
		{"apostrophe", "it's working"},
		{"double quotes", `error: "not found"`},
		{"percent", "100% complete"},
		{"ampersand", "foo & bar"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			quoted := shellQuote(tt.subject)
			if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
				t.Errorf("shellQuote(%q) = %q, want single-quoted wrapper", tt.subject, quoted)
			}
		})
	}
}

func TestSendNotificationBanner_Normal(t *testing.T) {
	cases := []struct {
		name    string
		subject string
	}{
		{"work done", "WORK_DONE"},
		{"quality gate", "QUALITY_GATE"},
		{"help with colon", "HELP: unclear requirements"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			quoted := shellQuote(tt.subject)
			unquoted := quoted[1 : len(quoted)-1]
			if unquoted != tt.subject {
				t.Errorf("shellQuote(%q) inner = %q, want %q", tt.subject, unquoted, tt.subject)
			}
		})
	}
}

func TestIsProductionIdentity(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"af-manager", true},
		{"af-watchdog", true},
		{"af-test-ab12cd34-mgr", false},
		{"notes", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isProductionIdentity(tt.name); got != tt.want {
				t.Errorf("isProductionIdentity(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
