//go:build !integration

package cmd

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// TestStoreGuard asserts the STORE-GUARD is active in the default
// (non-integration) test build: storeGuardActive is true (driven by
// isTestBinary()), so newIssueStore returns an in-memory store and never
// constructs the real Python-backed mcpstore. Mirrors the tmux guardMode split.
func TestStoreGuard(t *testing.T) {
	if !storeGuardActive {
		t.Fatal("storeGuardActive must be true in the default test build (isTestBinary())")
	}
}

// TestNewIssueStore asserts that under the default test build newIssueStore
// returns a *memstore.Store with a nil error WITHOUT requiring a factory root.
// A bare temp dir is not a factory root, so the real mcpstore branch would fail
// config.FindFactoryRoot; the guarded memstore branch must short-circuit before
// that (and before any Python contact) and succeed.
func TestNewIssueStore(t *testing.T) {
	store, err := newIssueStore(t.TempDir(), "tester")
	if err != nil {
		t.Fatalf("newIssueStore returned error in default build: %v", err)
	}
	if _, ok := store.(*memstore.Store); !ok {
		t.Fatalf("newIssueStore returned %T, want *memstore.Store under the default build", store)
	}
}
