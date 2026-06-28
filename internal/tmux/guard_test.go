//go:build !integration

package tmux

import (
	"fmt"
	"strings"
	"testing"
)

// capturePanic runs fn and reports whether it panicked along with the recovered
// message rendered as a string.
func capturePanic(fn func()) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			msg = fmt.Sprintf("%v", r)
		}
	}()
	fn()
	return "", false
}

// TestGuard_PanicsOnProductionIdentity asserts that, under the default test
// build, a guarded *Tmux panics on any destructive op against a production
// identity, and that the panic message names the op, the target, and the
// runtime.Stack-derived offending test (AC #1).
func TestGuard_PanicsOnProductionIdentity(t *testing.T) {
	tx := NewTmux()
	const target = "af-manager"

	cases := []struct {
		op   string
		call func()
	}{
		{"kill-session", func() { _ = tx.KillSession(target) }},
		{"new-session", func() { _ = tx.NewSession(target, "/tmp") }},
		{"send-keys", func() { _ = tx.SendKeys(target, "echo hi") }},
		{"attach-session", func() { _ = tx.AttachSession(target) }},
		{"set-option", func() { _ = tx.SetOption(target, "mouse", "on") }},
	}

	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			msg, panicked := capturePanic(tc.call)
			if !panicked {
				t.Fatalf("op %s on %q: expected panic, got none", tc.op, target)
			}
			if !strings.Contains(msg, tc.op) {
				t.Errorf("panic message missing op %q:\n%s", tc.op, msg)
			}
			if !strings.Contains(msg, target) {
				t.Errorf("panic message missing target %q:\n%s", target, msg)
			}
			if !strings.Contains(msg, "TestGuard_PanicsOnProductionIdentity") {
				t.Errorf("panic message missing runtime.Stack test name:\n%s", msg)
			}
		})
	}
}

// TestGuard_NonProductionIsInertNoOp asserts that a destructive op on a
// non-production (af-test-) name is an inert no-op: no panic, nil error, and no
// real tmux exec (third case, H2 / AC #3).
func TestGuard_NonProductionIsInertNoOp(t *testing.T) {
	ResetRealOpCounter()
	tx := NewTmux()
	const target = "af-test-ab12cd34-mgr"

	msg, panicked := capturePanic(func() {
		if err := tx.KillSession(target); err != nil {
			t.Errorf("KillSession(%q) returned err: %v", target, err)
		}
	})
	if panicked {
		t.Fatalf("KillSession(%q) panicked (must be inert no-op):\n%s", target, msg)
	}

	if err := tx.NewSession(target, "/tmp"); err != nil {
		t.Errorf("NewSession(%q) inert no-op returned err: %v", target, err)
	}
	if err := tx.SendKeys(target, "echo hi"); err != nil {
		t.Errorf("SendKeys(%q) inert no-op returned err: %v", target, err)
	}

	if got := ProductionRealOpCount(); got != 0 {
		t.Errorf("ProductionRealOpCount()=%d after non-production ops, want 0", got)
	}
}

// TestGuard_GoroutineOffenderAttributable asserts that a guarded op invoked on a
// spawned goroutine still yields a non-vacuous, attributable failure: the
// recovered panic names the op and target, and carries either a Test-prefixed
// frame or the documented background-goroutine fallback (H1 / AC #4).
func TestGuard_GoroutineOffenderAttributable(t *testing.T) {
	tx := NewTmux()
	const target = "af-watchdog"

	resultCh := make(chan string, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultCh <- fmt.Sprintf("%v", r)
				return
			}
			resultCh <- ""
		}()
		_ = tx.KillSession(target)
	}()

	msg := <-resultCh
	if msg == "" {
		t.Fatal("guarded op in goroutine did not panic")
	}
	if !strings.Contains(msg, "kill-session") || !strings.Contains(msg, target) {
		t.Errorf("goroutine panic not attributable to op/target:\n%s", msg)
	}
	if !strings.Contains(msg, "Test") && !strings.Contains(msg, "background goroutine") {
		t.Errorf("goroutine panic has no attribution (no Test frame, no fallback marker):\n%s", msg)
	}
}

// TestGuard_ZeroProductionRealOps asserts the real-exec counter primitive reads
// zero production real ops under the guard, after exercising non-production
// no-ops, read-only probes, and a production panic (CR2 / AC #2).
func TestGuard_ZeroProductionRealOps(t *testing.T) {
	ResetRealOpCounter()
	tx := NewTmux()

	_ = tx.KillSession("af-test-ab12cd34-x")
	_ = tx.NewSession("af-test-ab12cd34-y", "/tmp")
	_ = tx.SetEnvironment("af-test-ab12cd34-z", "K", "V")

	if _, err := tx.HasSession("af-manager"); err != nil {
		t.Errorf("HasSession read-only probe returned err: %v", err)
	}
	if _, err := tx.ListSessions(); err != nil {
		t.Errorf("ListSessions read-only probe returned err: %v", err)
	}
	if tx.IsAvailable() {
		t.Errorf("IsAvailable() must be false under the guard")
	}

	func() {
		defer func() { _ = recover() }()
		_ = tx.KillSession("af-manager")
	}()

	if got := ProductionRealOpCount(); got != 0 {
		t.Fatalf("ProductionRealOpCount()=%d, want 0 under guard", got)
	}
}
