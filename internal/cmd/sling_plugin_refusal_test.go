//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

type g2SlingRun struct {
	err       error
	out       string
	claudeMD  bool
	launches  int
	beadCount int
}

// g2SlingPluginAgent drives `af sling --formula acme-triage --agent acme-triage` against a
// factory where acme-triage is a registered, non-embedded formula agent. manifest is the
// raw plugins.json body ("" = absent).
func g2SlingPluginAgent(t *testing.T, manifest string, noLaunch bool) g2SlingRun {
	t.Helper()
	store := installMemStore(t)
	launches := 0
	origLaunch := launchAgentSession
	launchAgentSession = func(*cobra.Command, string, string, string, string, string, bool) error {
		launches++
		return nil
	}
	t.Cleanup(func() { launchAgentSession = origLaunch })

	root, agentDir := createTestFormulaFactory(t, "acme-triage", "acme-triage")
	writeAgentsJSON(t, root, `{"agents":{"acme-triage":{"type":"autonomous","description":"plugin agent","formula":"acme-triage"}}}`)
	if manifest != "" {
		g2WriteManifestBytes(t, root, manifest)
	}

	origF, origA, origNL, origReset := slingFormulaName, slingAgent, slingNoLaunch, slingReset
	slingFormulaName, slingAgent, slingNoLaunch, slingReset = "acme-triage", "acme-triage", noLaunch, false
	t.Cleanup(func() { slingFormulaName, slingAgent, slingNoLaunch, slingReset = origF, origA, origNL, origReset })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := runFormulaInstantiation(cmd, root, agentDir, nil)

	_, statErr := os.Stat(filepath.Join(agentDir, "CLAUDE.md"))
	return g2SlingRun{
		err:       err,
		out:       buf.String(),
		claudeMD:  statErr == nil,
		launches:  launches,
		beadCount: g2BeadCount(t, store),
	}
}

func g2BeadCount(t *testing.T, store *memstore.Store) int {
	t.Helper()
	issues, err := store.List(t.Context(), issuestore.Filter{IncludeAllAgents: true})
	if err != nil {
		t.Fatalf("list beads: %v", err)
	}
	return len(issues)
}

const g2ValidAcmeManifest = `{"plugins":{"acme":{"formulas":{"acme-triage":{"sha256":"x"}}}}}`

// PR #539 BODY-1: the K14 refusal precedes worktree setup and is fatal (D17 [A]/[A]).
func TestSlingFormulaRefusesPluginAgentBeforeWorktreeSetup(t *testing.T) {
	r := g2SlingPluginAgent(t, g2ValidAcmeManifest, false)
	if r.err == nil {
		t.Fatalf("sling --formula must exit non-zero for a plugin agent whose template is not embedded; output:\n%s", r.out)
	}
	if !strings.Contains(r.err.Error(), `owned by plugin "acme"`) {
		t.Errorf("the error must be the K14 refusal; got: %v", r.err)
	}
	if !strings.Contains(r.err.Error(), "af up acme-triage") {
		t.Errorf("the beads already exist, so the error must say how to proceed (`af up acme-triage`); got: %v", r.err)
	}
	if r.claudeMD {
		t.Error("a substitute CLAUDE.md was written before the refusal (SetupAgent ran first)")
	}
	if r.launches != 0 {
		t.Errorf("launch reached %d time(s) after the refusal", r.launches)
	}
	if r.beadCount == 0 {
		t.Error("D17 [A]: the guard sits after bead creation, so the formula beads must exist")
	}
}

// PR #539 BODY-1 + T2: the new early site inherits the fail-closed guard.
func TestSlingFormulaRefusesPluginAgentCorruptManifest(t *testing.T) {
	r := g2SlingPluginAgent(t, g2ConflictManifest, false)
	if r.err == nil || !strings.Contains(r.err.Error(), "plugins.json") {
		t.Fatalf("sling --formula must refuse naming plugins.json while it is corrupt; err=%v output:\n%s", r.err, r.out)
	}
	if r.claudeMD {
		t.Error("a substitute CLAUDE.md was written despite the corrupt manifest")
	}
	if r.launches != 0 {
		t.Errorf("launch reached %d time(s) after the refusal", r.launches)
	}
}

// PR #539 BODY-1 D17: --no-launch is unchanged (beads, no worktree setup, no launch, exit 0).
func TestSlingFormulaNoLaunchPluginAgent(t *testing.T) {
	r := g2SlingPluginAgent(t, g2ValidAcmeManifest, true)
	if r.err != nil {
		t.Fatalf("--no-launch must stay unchanged (nil); got: %v", r.err)
	}
	if r.beadCount == 0 {
		t.Error("--no-launch must still create the formula beads")
	}
	if r.claudeMD || r.launches != 0 {
		t.Errorf("--no-launch must not set up or launch; claudeMD=%v launches=%d", r.claudeMD, r.launches)
	}
}

// PR #539 BODY-1 AC-6: without plugins.json the early guard is dormant.
func TestSlingFormulaDormantWithoutManifest(t *testing.T) {
	r := g2SlingPluginAgent(t, "", false)
	if r.err != nil {
		t.Fatalf("no plugins.json ⇒ dormant; got: %v\n%s", r.err, r.out)
	}
	if !r.claudeMD {
		t.Error("dormant path must still set up the agent worktree (CLAUDE.md)")
	}
	if r.launches != 1 {
		t.Errorf("dormant path must launch exactly once; got %d", r.launches)
	}
}
