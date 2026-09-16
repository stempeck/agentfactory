package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestTokenomicsStatus_PrintsDispatchPool pins #669 THREAD-2 pin 4's second clause: `af tokenomics
// status` reports the dispatch gate's pool operand with a source label whenever it reports the
// dispatch mechanism at all. Before the fix the surface printed the per-request window but never the
// declared backend pool the gate actually divides by, so an operator could not tell an armed pool
// from an elastic one from the status the gate itself points them to.
//
// The two shapes are the two the gate distinguishes: a default profile that DECLARES
// AF_BACKEND_POOL_TOKENS (armed) versus a codex-shaped one that declares only a window (elastic,
// inert). The elastic case asserts the surface says WHY it is inert rather than printing a bare
// "0 tokens" — the same "say why, do not print zeros" contract the window line follows.
func TestTokenomicsStatus_PrintsDispatchPool(t *testing.T) {
	writeModels := func(t *testing.T, root, models string) {
		t.Helper()
		if err := os.WriteFile(config.ModelsConfigPath(root), []byte(models), 0o644); err != nil {
			t.Fatalf("write models.json: %v", err)
		}
	}

	t.Run("declared pool prints with its source label", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		writeModels(t, root, `{"default":"decl","models":{"decl":{`+
			`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",`+
			`"ANTHROPIC_AUTH_TOKEN":"tok",`+
			`"AF_BACKEND_POOL_TOKENS":"262144",`+
			`"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"262144"}}}`)

		out, err := runTokenomicsArgs(t)
		if err != nil {
			t.Fatalf("tokenomics status: %v", err)
		}
		if !strings.Contains(out, "dispatch pool: 262144 tokens (source: declared AF_BACKEND_POOL_TOKENS)") {
			t.Errorf("status does not report the declared dispatch pool the gate divides by; got:\n%s", out)
		}
	})

	t.Run("an elastic (codex-shaped) default says why it is inert, not a bare zero", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		// codex shape: a base URL and a per-request window, but NO pool fact — the elastic backend the
		// operator forbade the gate from refusing on. THREAD-2's whole point is that this reads inert.
		writeModels(t, root, `{"default":"codex","models":{"codex":{`+
			`"ANTHROPIC_BASE_URL":"http://127.0.0.1:4000",`+
			`"ANTHROPIC_AUTH_TOKEN":"tok",`+
			`"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"1050000"}}}`)

		out, err := runTokenomicsArgs(t)
		if err != nil {
			t.Fatalf("tokenomics status: %v", err)
		}
		if !strings.Contains(out, "dispatch pool: none (inert") {
			t.Errorf("an elastic default must report the pool as inert with a reason; got:\n%s", out)
		}
		if strings.Contains(out, "dispatch pool: 0 tokens") {
			t.Errorf("elastic backend printed a bare '0 tokens', the exact zeros-for-a-dark-chain "+
				"dishonesty the surface's contract forbids; got:\n%s", out)
		}
	})

	// F3 (r3906601... undocumented child floor + sequential cap): the two other operator facts the
	// gate now enforces — the child-footprint floor and the AF_DISABLE_PARALLEL_SUBAGENTS hard cap —
	// must appear on the same status surface the pool line does, or an operator cannot tell an armed
	// cap/floor from an absent one. RED at head (printTokenomicsStatus prints only the pool line).
	t.Run("declared child floor and the sequential cap print their own status lines", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		writeModels(t, root, `{"default":"cap","models":{"cap":{`+
			`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",`+
			`"ANTHROPIC_AUTH_TOKEN":"tok",`+
			`"AF_BACKEND_POOL_TOKENS":"262144",`+
			`"AF_BACKEND_CHILD_FLOOR_TOKENS":"70000",`+
			`"AF_DISABLE_PARALLEL_SUBAGENTS":"1"}}}`)

		out, err := runTokenomicsArgs(t)
		if err != nil {
			t.Fatalf("tokenomics status: %v", err)
		}
		if !strings.Contains(out, "child floor") || !strings.Contains(out, "70000") {
			t.Errorf("status does not report the declared child floor the gate guards the first child with; got:\n%s", out)
		}
		if !strings.Contains(out, "AF_DISABLE_PARALLEL_SUBAGENTS") {
			t.Errorf("status does not report the sequential-only hard cap the gate enforces; got:\n%s", out)
		}
	})
}
