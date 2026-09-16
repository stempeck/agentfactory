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

// TestDispatchAdmit_InertWithoutPoolFact is THREAD-2's headline regression: a codex-shaped profile —
// an ANTHROPIC_BASE_URL and a CLAUDE_CODE_MAX_CONTEXT_TOKENS window but NO AF_BACKEND_POOL_TOKENS —
// must stay INERT even at 95% launcher occupancy. The pool operand is the operator-declared pool
// fact, not the per-request window, so a backend that declares no pool has nothing to divide and
// admits with zero arithmetic.
//
// RED today: the gate reads the 200000 window as WindowSourceDeclared, arms, and a 95% launcher
// (190000 measured) breaches the 180000 ceiling — a false-refusal deny. GREEN after: the pool operand
// reads AF_BACKEND_POOL_TOKENS, which is absent (<= 0), so the gate returns before any arithmetic.
func TestDispatchAdmit_InertWithoutPoolFact(t *testing.T) {
	now := time.Now()
	fx := newLifecycleFixture(t)
	armTokenomics(t, fx.root, 10, 1)
	// codex shape: base URL + auth + window, and deliberately NO AF_BACKEND_POOL_TOKENS.
	models := `{"default":"codex","models":{"codex":{` +
		`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",` +
		`"ANTHROPIC_AUTH_TOKEN":"tok",` +
		`"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"}}}`
	if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

	var out bytes.Buffer
	if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
		t.Fatalf("runDispatchAdmitCore: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a codex-shaped profile (window, no pool fact) armed on the window and produced gate output; "+
			"the pool operand must read AF_BACKEND_POOL_TOKENS, which is absent here => inert admit:\n%s", out.String())
	}
	if recs := dispatchInterventionRecords(t, fx.root, fx.agent); len(recs) != 0 {
		t.Errorf("an inert codex profile wrote %d dispatch records, want 0 (no arithmetic ran)", len(recs))
	}
}

// TestDispatchAdmit_RefusesViaDeclaredPoolNotWindow is THREAD-2's other half: an lmstudio-shaped
// profile — base URL + auth + AF_BACKEND_POOL_TOKENS but NO CLAUDE_CODE_MAX_CONTEXT_TOKENS window —
// must refuse via the declared pool alone. With no window declared, a refusal here can only have come
// from the pool fact, which is exactly "refuses ONLY via the declared pool" and the record's
// pool_tokens proves it.
//
// RED today: with no declared window ResolveWindow yields WindowSourceHost/Fallback, the gate's
// `pool.Source != WindowSourceDeclared` short-circuit returns nil, and nothing is denied. GREEN
// after: the pool operand reads AF_BACKEND_POOL_TOKENS=200000, arms on it, and a 95% launcher
// (190000) breaches the 180000 ceiling — a deny whose record carries pool_tokens=200000.
func TestDispatchAdmit_RefusesViaDeclaredPoolNotWindow(t *testing.T) {
	now := time.Now()
	fx := newLifecycleFixture(t)
	armTokenomics(t, fx.root, 10, 1)
	// lmstudio shape: base URL + auth + declared pool, and deliberately NO window key.
	models := `{"default":"lmstudio","models":{"lmstudio":{` +
		`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",` +
		`"ANTHROPIC_AUTH_TOKEN":"tok",` +
		`"AF_BACKEND_POOL_TOKENS":"200000"}}}`
	if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

	var out bytes.Buffer
	if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
		t.Fatalf("runDispatchAdmitCore: %v", err)
	}
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("a profile declaring AF_BACKEND_POOL_TOKENS but no window was not refused; the gate must arm on "+
			"the declared pool fact, not require a per-request window:\n%s", out.String())
	}
	recs := dispatchInterventionRecords(t, fx.root, fx.agent)
	if len(recs) != 1 || recs[0].Action != telemetry.ActionRefuse {
		t.Fatalf("a pool-breaching launch wrote %d dispatch records (want exactly 1 refuse): %+v", len(recs), recs)
	}
	if recs[0].PoolTokens == nil || *recs[0].PoolTokens != 200000 {
		t.Errorf("refusal pool_tokens = %v, want 200000 — the refusal arithmetic must come from the declared "+
			"pool fact, since no window is declared to supply it", recs[0].PoolTokens)
	}
}
