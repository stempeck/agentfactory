package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

// snapshotTree returns a path->sha256 map of every regular file under root, for
// proving a subtree is byte-unchanged across an operation.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(root, path)
		m[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return m
}

// TestT_INT_3_NestedCloneRefusal (#519 AC-1/AC-3) is the canonical incident repro at
// the verb level: a session whose cwd is a nested factory clone, carrying
// AF_ROOT=<outer>, must have af sling / af mail / af done REFUSE pre-mutation — exit
// non-zero, name BOTH roots, write nothing to the clone subtree or the outer store,
// and stop no tmux session (cross-review H2).
func TestT_INT_3_NestedCloneRefusal(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	t.Setenv("AF_ROOT", fx.outer)

	store := installMemStore(t)
	fake := installFakeTmuxPresent(t) // records every would-be tmux op; none should fire

	before := snapshotTree(t, fx.outer)

	assertNamesBothRoots := func(t *testing.T, verb string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected refusal (non-nil error), got nil", verb)
		}
		msg := err.Error()
		if !strings.Contains(msg, "factory root mismatch") {
			t.Errorf("%s: error missing %q head: %s", verb, "factory root mismatch", msg)
		}
		if !strings.Contains(msg, fx.clone) {
			t.Errorf("%s: error must name the clone root %q: %s", verb, fx.clone, msg)
		}
		if !strings.Contains(msg, fx.outer) {
			t.Errorf("%s: error must name the outer/session root %q: %s", verb, fx.outer, msg)
		}
	}

	t.Run("sling", func(t *testing.T) {
		origAgent, origFormula := slingAgent, slingFormulaName
		slingAgent, slingFormulaName = "a", ""
		t.Cleanup(func() { slingAgent, slingFormulaName = origAgent, origFormula })

		t.Chdir(fx.clone)
		slingCmd.SetContext(t.Context())
		err := runSling(slingCmd, []string{"a task the clone must never capture"})
		assertNamesBothRoots(t, "af sling", err)
	})

	t.Run("done", func(t *testing.T) {
		err := runDoneCore(t.Context(), fx.clone, false, "")
		assertNamesBothRoots(t, "af done", err)
	})

	t.Run("mail", func(t *testing.T) {
		// detectSender is the sender-resolution core of `af mail send`; its swapped
		// resolveInvokerRoot call is the refusal point. The mismatch propagates wrapped.
		_, err := detectSender(fx.clone)
		assertNamesBothRoots(t, "af mail send", err)
	})

	// Clone subtree (and the whole outer tree) must be byte-identical: refusal is
	// pre-mutation and wrote nothing under either root.
	after := snapshotTree(t, fx.outer)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("outer/clone subtree changed across refusals — a mutation leaked.\nbefore=%v\nafter=%v", before, after)
	}

	// The outer store must be untouched (refusal != silent redirect).
	if issues, err := store.List(t.Context(), issuestore.Filter{}); err != nil {
		t.Fatalf("store.List: %v", err)
	} else if len(issues) != 0 {
		t.Errorf("outer store must be untouched on refusal; found %d issue(s)", len(issues))
	}

	// No tmux session outside the fixture (or any at all) was stopped.
	for _, op := range fake.ops {
		if strings.HasPrefix(op, "KillSession") || strings.HasPrefix(op, "NewSession") {
			t.Errorf("refusal must not touch tmux sessions; recorded op %q", op)
		}
	}
}
