package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestDefaultSuiteIssuesNoRealTmux is the behavioral complement to the
// structural ban in TestNoRawDestructiveTmuxInUntaggedTests (#309 AC-6). It
// drives a REAL command path (`af down manager`) through the hermetic fake and
// proves two things at once:
//
//   - the under-test code records its would-be tmux ops against the per-test
//     namespace af-test-<hex>-manager, and
//   - NO recorded op targets the literal production session name "af-manager".
//
// The agent name "manager" is deliberately production-class (Round-2 MED-2 /
// AC-6 clause iv): if the prefix seam ever regressed, `af down manager` run in
// the default suite beside a live factory would issue `tmux ... af-manager` and
// kill the operator's real manager session. This test makes that regression a
// red build. (TestSetupHermeticSessions proves the namespace mechanism; this
// proves it for a real command path with a collision-prone name.)
func TestDefaultSuiteIssuesNoRealTmux(t *testing.T) {
	// Production-class name: collides with the real "af-manager" session if
	// isolation regresses.
	const agentName = "manager"

	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1,"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"manager":{"type":"interactive","description":"orchestrator"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Install the fake AFTER t.TempDir() so the seam restores run before the
	// temp-dir delete (design R-7).
	fake, _ := setupHermeticSessions(t)

	t.Chdir(root)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// `af down manager` resolves the manager agent and calls mgr.Stop(), whose
	// HasSession pre-flight runs through the fake against session.SessionName("manager").
	_ = runDown(cmd, []string{agentName})

	if len(fake.ops) == 0 {
		t.Fatal("the command path issued no tmux ops through the fake; cannot prove isolation")
	}

	// The hermetic name the namespaced "manager" session resolves to.
	wantNS := "af-test-" + hashName(t.Name()) + "-" + agentName

	sawNamespaced := false
	for _, op := range fake.ops {
		// The literal production session name MUST NOT appear in any op.
		if strings.Contains(op, "af-manager") {
			t.Errorf("recorded op targeted the literal production session name af-manager: %q", op)
		}
		if strings.Contains(op, wantNS) {
			sawNamespaced = true
		}
	}
	if !sawNamespaced {
		t.Errorf("no recorded op targeted the per-test hermetic name %q; ops=%v", wantNS, fake.ops)
	}
}
