package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// assertNoFactoryLine fails if any line of out starts with the additive
// dispatch-verb "factory:" prefix. The JSON-contract verbs are exempt from the
// #519 observability line so their always-exit-0 JSON payload stays pure.
func assertNoFactoryLine(t *testing.T, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "factory:") {
			t.Errorf("JSON-contract verb emitted a forbidden %q line:\n%s", line, out)
		}
	}
}

// TestFactoryLine_SlingLeadsStdout pins T-INT-6 (part 1) / AC-1: `af sling` must
// print `factory: <resolved-root>` as its FIRST stdout line, before any
// Created/Dispatched/Launched line, so a mis-rooting (#519) is visible within the
// first second of a dispatch. The line is purely additive and byte-preserving.
func TestFactoryLine_SlingLeadsStdout(t *testing.T) {
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")
	// Hermetic tmux + memstore so the specialist dispatch runs its happy path
	// (and emits Created/Dispatched lines) without touching real tmux or a live store.
	setupHermeticSessions(t)

	writeAgentsJSON(t, root, `{"agents":{"specialist-agent":{"type":"autonomous","description":"Test specialist","formula":"test-specialist-formula"}}}`)

	// Drive runSling end-to-end from inside the factory so getWd→FindFactoryRoot
	// resolves the same root the print emits.
	t.Chdir(root)

	origAgent, origFormula, origNoLaunch := slingAgent, slingFormulaName, slingNoLaunch
	slingAgent = "specialist-agent"
	slingFormulaName = ""
	slingNoLaunch = true
	t.Cleanup(func() {
		slingAgent, slingFormulaName, slingNoLaunch = origAgent, origFormula, origNoLaunch
	})

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := runSling(cmd, []string{"implement issue #42"}); err != nil {
		t.Logf("runSling returned err (tolerated; factory line ordering still asserted): %v", err)
	}
	t.Logf("sling stdout:\n%s", out.String())
	t.Logf("sling stderr:\n%s", errBuf.String())

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "factory: ") {
		t.Fatalf("first stdout line must be the additive `factory: <root>` line, got:\n%s", out.String())
	}

	wd, _ := os.Getwd()
	wantRoot, ferr := config.FindFactoryRoot(wd)
	if ferr != nil {
		t.Fatalf("FindFactoryRoot(%q): %v", wd, ferr)
	}
	if got := strings.TrimPrefix(lines[0], "factory: "); got != wantRoot {
		t.Errorf("factory line root = %q, want the resolved factory root %q", got, wantRoot)
	}

	// The factory line must LEAD: it precedes every mutation-echo line.
	mutIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "Created") || strings.Contains(line, "Dispatched") || strings.Contains(line, "Launched") {
			mutIdx = i
			break
		}
	}
	if mutIdx == 0 {
		t.Fatalf("a Created/Dispatched/Launched line preceded the factory line:\n%s", out.String())
	}
}

// TestFactoryLine_DispatchStatusJSONStaysPure pins T-INT-6 (part 2) / AC-2:
// `dispatch status --json` is a CHECK-AS-WARNING JSON-contract verb — it resolves
// a root but must NOT emit the `factory:` line, and its stdout must stay valid JSON.
func TestFactoryLine_DispatchStatusJSONStaysPure(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	installMemStore(t)
	installFakeTmuxPresent(t)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.Flags().Bool("json", false, "")
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	if err := runDispatchStatus(cmd, nil); err != nil {
		t.Fatalf("runDispatchStatus --json must return nil (errors go in the envelope), got %v", err)
	}

	assertNoFactoryLine(t, buf.String())

	var v any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &v); err != nil {
		t.Fatalf("dispatch status --json output is not valid JSON: %q (%v)", buf.String(), err)
	}
}

// TestFactoryLine_AgentsListJSONStaysPure pins T-INT-6 (part 2) / AC-2:
// `agents list --json` is a machine-readable-contract verb — no `factory:` line,
// output stays a valid JSON array.
func TestFactoryLine_AgentsListJSONStaysPure(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{"worker":{"type":"autonomous","description":"d"}}}`)
	installMemStore(t)
	installFakeTmuxPresent(t)

	out := invokeAgentsList(t)

	assertNoFactoryLine(t, out)

	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &arr); err != nil {
		t.Fatalf("agents list --json output is not a valid JSON array: %q (%v)", out, err)
	}
}

// TestFactoryLine_PrimeMetadataCarriesFactory pins AC-3: `af prime` metadata
// carries a `factory:<root>` field. This is the ux.md middle-ground — a field on
// the per-agent [AGENT FACTORY] metadata line, not a leading `factory:` line,
// since prime is context injection, not a dispatch verb.
func TestFactoryLine_PrimeMetadataCarriesFactory(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	var buf bytes.Buffer

	agentDir := filepath.Join(root, ".agentfactory", "agents", "manager")
	if err := primeAgent(t.Context(), &buf, root, "manager", agentDir); err != nil {
		t.Fatalf("primeAgent: %v", err)
	}

	out := buf.String()
	var metaLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "[AGENT FACTORY]") {
			metaLine = line
			break
		}
	}
	if metaLine == "" {
		t.Fatalf("no [AGENT FACTORY] metadata line in prime output:\n%s", out)
	}
	if !strings.Contains(metaLine, "factory:"+root) {
		t.Errorf("prime metadata line missing factory:%s field, got: %q", root, metaLine)
	}
}
