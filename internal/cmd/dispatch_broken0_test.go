//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// TestDispatchAdmit_FiresOnAgentToolName pins BROKEN-0 (PR #669): the dispatch gate compares the hook
// payload's tool_name to the literal "Task", but the platform's sub-agent tool is named "Agent"
// (Claude Code 2.1.236, verified from a live transcript). A real sub-agent launch — tool_name "Agent"
// — hits `p.ToolName != "Task"` and the gate returns nil before any pool arithmetic, so the mechanism
// is dead in production. A backend over its declared pool ceiling must refuse a launch named "Agent"
// exactly as it would one named "Task".
func TestDispatchAdmit_FiresOnAgentToolName(t *testing.T) {
	now := time.Now()
	fx := newLifecycleFixture(t)
	armTokenomics(t, fx.root, 10, 1)
	models := `{"default":"lmstudio","models":{"lmstudio":{` +
		`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",` +
		`"ANTHROPIC_AUTH_TOKEN":"tok",` +
		`"AF_BACKEND_POOL_TOKENS":"200000"}}}`
	if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

	var out bytes.Buffer
	if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Agent", Cwd: fx.workDir}, now); err != nil {
		t.Fatalf("runDispatchAdmitCore: %v", err)
	}
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("a sub-agent launch named \"Agent\" over the declared pool was NOT refused; the gate compares "+
			"tool_name to \"Task\" only and never fires on the real platform tool name:\n%s", out.String())
	}
	recs := dispatchInterventionRecords(t, fx.root, fx.agent)
	if len(recs) != 1 || recs[0].Action != telemetry.ActionRefuse {
		t.Fatalf("an Agent-named pool-breaching launch wrote %d dispatch records (want exactly 1 refuse): %+v", len(recs), recs)
	}
}

// TestSubagentObserve_FiresOnAgentToolName pins BROKEN-0's corollary: the fan-out observer carries the
// identical tool_name gate (`p.ToolName != "Task"`, subagent_observer.go) and has therefore been dead
// since it shipped. With the mechanism armed and a refusal recorded by the gate, a completion of the
// "Agent" tool must counsel exactly as a "Task" completion does.
func TestSubagentObserve_FiresOnAgentToolName(t *testing.T) {
	fx, _, _ := primedFixture(t, 90)
	gateOn(t, fx.root)
	armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"dispatch": "on"})
	seedRecordedRefusal(t, fx.workDir, "", time.Now())
	sends := captureSubagentMail(t)

	out := runSubagentObserve(t, "Agent")

	if len(*sends) != 1 {
		t.Fatalf("an \"Agent\" completion after a recorded refusal produced %d counsels, want 1; the observer "+
			"compares tool_name to \"Task\" only and never fires on the real platform tool name", len(*sends))
	}
	if (*sends)[0].subject != "TOKENOMICS_DISPATCH" {
		t.Errorf("subject = %q, want TOKENOMICS_DISPATCH", (*sends)[0].subject)
	}
	assertNoBlockingDecision(t, out)
}
