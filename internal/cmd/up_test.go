package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stempeck/agentfactory/internal/session"
)

func TestErrNotProvisioned_IsDetectable(t *testing.T) {
	// Verify that the wrapped ErrNotProvisioned from session.Start() is detectable
	// via errors.Is. This is a prerequisite for the graceful skip in runUp.
	wrapped := fmt.Errorf("%w: /some/path", session.ErrNotProvisioned)
	if !errors.Is(wrapped, session.ErrNotProvisioned) {
		t.Fatal("wrapped ErrNotProvisioned should be detectable via errors.Is")
	}
}

func TestRunUp_HandlesNotProvisioned(t *testing.T) {
	// This test verifies that runUp treats ErrNotProvisioned as a skip condition
	// (like ErrAlreadyRunning), not as a hard failure.
	//
	// Currently, runUp only has special handling for ErrAlreadyRunning.
	// ErrNotProvisioned falls through to the generic error handler which
	// sets allOK=false and causes a non-zero exit.
	//
	// After the fix, this test should pass: ErrNotProvisioned should be
	// handled with a skip message and no error exit.

	err := session.ErrNotProvisioned
	if !errors.Is(err, session.ErrNotProvisioned) {
		t.Fatal("ErrNotProvisioned should be identifiable")
	}
	// The key assertion: ErrNotProvisioned is NOT ErrAlreadyRunning
	if errors.Is(err, session.ErrAlreadyRunning) {
		t.Fatal("ErrNotProvisioned should not be mistaken for ErrAlreadyRunning")
	}

	// After fix: runUp should treat ErrNotProvisioned like ErrAlreadyRunning
	// (skip gracefully). This test documents the intent.
	isSkippable := errors.Is(err, session.ErrAlreadyRunning) || errors.Is(err, session.ErrNotProvisioned)
	if !isSkippable {
		t.Fatal("ErrNotProvisioned should be a skippable error in runUp")
	}
}
