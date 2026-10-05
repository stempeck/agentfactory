package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func firstOpIndex(ops []string, prefix string) int {
	for i, op := range ops {
		if strings.HasPrefix(op, prefix) {
			return i
		}
	}
	return -1
}

// TestLiveAgreesWithStart pins that Live() and Start() read liveness the same way:
// af up's guard (Live) and Start's own refusal must never disagree on absent,
// present+Claude, or present-only (zombie) sessions.
func TestLiveAgreesWithStart(t *testing.T) {
	cases := []struct {
		name          string
		present       bool
		claudeRunning bool
		wantLive      bool
	}{
		{name: "absent", present: false, claudeRunning: false, wantLive: false},
		{name: "present+claude", present: true, claudeRunning: true, wantLive: true},
		{name: "present-only zombie", present: true, claudeRunning: false, wantLive: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, fake := startMouseAgent(t, nil)
			id := mgr.SessionID()
			fake.present[id] = tc.present
			fake.claudeRunning[id] = tc.claudeRunning

			live := mgr.Live()
			startErr := mgr.Start()

			if live != tc.wantLive {
				t.Fatalf("Live() = %v, want %v", live, tc.wantLive)
			}
			if live != errors.Is(startErr, ErrAlreadyRunning) {
				t.Fatalf("Live() = %v but Start() = %v: the guard and Start disagree", live, startErr)
			}

			kill := firstOpIndex(fake.ops, "KillSession "+id)
			create := firstOpIndex(fake.ops, "NewSession "+id+" ")
			switch tc.name {
			case "absent":
				if startErr != nil {
					t.Fatalf("Start() = %v, want nil", startErr)
				}
				if create < 0 || kill >= 0 {
					t.Fatalf("absent session: want NewSession and no KillSession, ops=%v", fake.ops)
				}
			case "present+claude":
				if create >= 0 || kill >= 0 {
					t.Fatalf("live session: want no NewSession/KillSession, ops=%v", fake.ops)
				}
			case "present-only zombie":
				if startErr != nil {
					t.Fatalf("Start() = %v, want nil (zombie relaunch)", startErr)
				}
				if kill < 0 || create < 0 || kill > create {
					t.Fatalf("zombie: want KillSession before NewSession, ops=%v", fake.ops)
				}
				running, err := mgr.IsRunning()
				if err != nil || !running {
					t.Fatalf("IsRunning() = (%v, %v), want (true, nil): a zombie's tmux session exists", running, err)
				}
			}
		})
	}

	t.Run("worktree check precedes the probe", func(t *testing.T) {
		fake := installHermeticSession(t)
		mgr := NewManager(t.TempDir(), "orderagent", config.AgentEntry{Type: "interactive"})
		fake.present[mgr.SessionID()] = true
		fake.claudeRunning[mgr.SessionID()] = true

		if err := mgr.Start(); !errors.Is(err, ErrWorktreeNotSet) {
			t.Fatalf("Start() = %v, want ErrWorktreeNotSet", err)
		}
	})

	t.Run("probe precedes the provisioning check", func(t *testing.T) {
		fake := installHermeticSession(t)
		mgr := newTestManager(t.TempDir(), "orderagent", config.AgentEntry{Type: "interactive"})
		if err := mgr.SetWorktree("/nonexistent/wt-missing", "wt-missing"); err != nil {
			t.Fatalf("SetWorktree: %v", err)
		}
		fake.present[mgr.SessionID()] = true
		fake.claudeRunning[mgr.SessionID()] = true

		if err := mgr.Start(); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("live session on an unprovisioned worktree: Start() = %v, want ErrAlreadyRunning", err)
		}

		fake.present[mgr.SessionID()] = false
		if err := mgr.Start(); !errors.Is(err, ErrNotProvisioned) {
			t.Fatalf("absent session on an unprovisioned worktree: Start() = %v, want ErrNotProvisioned", err)
		}
	})
}
