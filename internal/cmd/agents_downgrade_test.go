package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// TestAgentsList_ProductionPath_DowngradesMismatch_NotErrorEnvelope pins the #519
// review follow-up (unresolved thread 1, agents.go:106). With the store guard OFF
// (the PRODUCTION path), `af agents list` from a shadowed cwd — cwd resolves to a
// factory different from the session's AF_ROOT — must DOWNGRADE the mismatch to a
// stderr warning and still emit a JSON array. It must NOT re-resolve the root when
// building its store and thereby re-raise the very error it just downgraded,
// landing in the {"state":"error"} envelope.
//
// Every existing agents-list test is blind to this because BOTH installMemStore
// (which stubs newIssueStore whole) AND the default-build guard (storeGuardActive
// == true) short-circuit resolveInvokerRoot before it can fail. This test flips the
// guard off and stubs only the LEAF constructor (newIssueStoreAt), so the real
// resolve-then-construct branch runs without contacting Python — the exact seam the
// fix must route through.
func TestAgentsList_ProductionPath_DowngradesMismatch_NotErrorEnvelope(t *testing.T) {
	fx := buildNestedFactoryFixture(t)

	// Production path: the guard is what hides the bug. Off ⇒ newIssueStore takes
	// the real resolve-then-construct branch that re-raises the mismatch.
	origGuard := storeGuardActive
	storeGuardActive = false
	t.Cleanup(func() { storeGuardActive = origGuard })

	// Stub only the leaf so the guard-off path never spawns the Python server, and
	// record the root it is handed to prove the store is built WITHOUT a second
	// resolveInvokerRoot pass.
	var gotRoot string
	origAt := newIssueStoreAt
	newIssueStoreAt = func(root, actor string) (issuestore.Store, error) {
		gotRoot = root
		return memstore.NewWithActor(actor), nil
	}
	t.Cleanup(func() { newIssueStoreAt = origAt })

	installFakeTmuxPresent(t) // hermetic tmux; no live sessions needed

	// cwd = nested clone, AF_ROOT = outer ⇒ a factory-root mismatch that the
	// read-only verb must downgrade, not refuse.
	t.Chdir(fx.clone)
	t.Setenv("AF_ROOT", fx.outer)

	var buf bytes.Buffer
	agentsListCmd.SetContext(t.Context())
	agentsListCmd.SetOut(&buf)
	t.Cleanup(func() { agentsListCmd.SetOut(nil) })

	stderr := captureStderr(t, func() {
		if err := runAgentsList(agentsListCmd, nil); err != nil {
			t.Fatalf("runAgentsList returned a non-nil RunE error: %v", err)
		}
	})

	out := strings.TrimSpace(buf.String())
	if strings.Contains(out, `"state":"error"`) {
		t.Fatalf("agents list landed in the error envelope instead of warn-and-proceed (thread 1 dead-code downgrade): %s", out)
	}
	var items []agentListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("output is not a JSON agent array: %q (%v)", out, err)
	}
	if gotRoot != fx.clone {
		t.Fatalf("store built on %q, want the downgraded clone root %q — a second resolveInvokerRoot pass happened", gotRoot, fx.clone)
	}
	if !strings.Contains(stderr, "mismatch") {
		t.Errorf("expected a factory-root-mismatch downgrade warning on stderr, got: %q", stderr)
	}
}
