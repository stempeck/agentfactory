//go:build integration

package cmd

import (
	"testing"
)

// TestStoreGuard_IntegrationUsesRealStore asserts AC-6: under -tags=integration
// the STORE-GUARD is INACTIVE (storeGuardActive == false), so newIssueStore takes
// the real *mcpstore branch rather than memstore.
//
// The branch is verified deterministically and WITHOUT spawning Python or
// contacting any MCP server: with the guard off, newIssueStore first resolves the
// factory root, and a bare temp dir is not a factory root, so the real-store branch
// returns (nil, err) from config.FindFactoryRoot — BEFORE mcpstore.New /
// discoverOrStart. The memstore branch, by contrast, would return a usable store
// and a nil error regardless of factory root. So nil-store + non-nil-error here
// proves the production (mcpstore) path is selected under the integration build.
func TestStoreGuard_IntegrationUsesRealStore(t *testing.T) {
	if storeGuardActive {
		t.Fatal("storeGuardActive must be false under -tags=integration (AC-6): the STORE-GUARD must not force memstore on the integration suite")
	}

	store, err := newIssueStore(t.TempDir(), "tester")
	if err == nil {
		t.Fatalf("expected the real-store branch to fail FindFactoryRoot on a non-factory dir; got store=%T err=nil (guard appears to have forced memstore)", store)
	}
	if store != nil {
		t.Fatalf("expected nil store on FindFactoryRoot failure, got %T", store)
	}
}
