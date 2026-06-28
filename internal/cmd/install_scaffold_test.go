package cmd

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// extractScaffoldLiteral returns the backtick-delimited raw-string value that
// follows key (e.g. `"startup.json":`) in the install.go source. The starterConfigs
// scaffold JSON literals contain no internal backticks, so "first backtick after
// key → next backtick" is an exact extraction. Reading the REAL literal (rather
// than a copy) keeps the test drift-proof without exposing any production helper —
// the same hermetic os.ReadFile("install.go") idiom as install_paths_test.go.
func extractScaffoldLiteral(t *testing.T, src, key string) string {
	t.Helper()
	ki := strings.Index(src, key)
	if ki < 0 {
		t.Fatalf("scaffold key %q not found in install.go", key)
	}
	rest := src[ki+len(key):]
	open := strings.IndexByte(rest, '`')
	if open < 0 {
		t.Fatalf("no opening backtick after %q in install.go", key)
	}
	rest = rest[open+1:]
	end := strings.IndexByte(rest, '`')
	if end < 0 {
		t.Fatalf("no closing backtick after %q literal in install.go", key)
	}
	return rest[:end]
}

// TestInstallScaffold_WatchdogAgentsAreSeededAgents pins the N6 parity guarantee
// (issue #408 Phase 4 / AC-4): every name in the install scaffold's startup.json
// watchdog_agents is a real key in the scaffold agents.json — so a fresh
// `af install --init` + `af up` yields a FUNCTIONAL scoped watchdog (no
// unknown-agent warning, no refuse-on-boot). This is the regression guard the
// `mergepatrol`-class bug (a watchdog_agents name that is not a real agent) lacked.
//
// Unit-tier constraint (AC-4): runInstallInit spawns the Python MCP server and is
// gated to the integration tier, so it cannot write the real scaffold in a unit
// test. Instead this source-parses the two scaffold literals straight out of
// install.go (the established os.ReadFile("install.go") idiom), reading the REAL
// literals rather than a copy — hermetic AND drift-proof.
func TestInstallScaffold_WatchdogAgentsAreSeededAgents(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	src := string(data)

	// agents.json is now built by config.DefaultAgentsConfigJSON() (single-source, issue
	// #73 K5) rather than an inline literal, so read the REAL default — still drift-proof,
	// now also covering the seeded specialists. startup.json stays an inline literal.
	startupLiteral := extractScaffoldLiteral(t, src, `"startup.json":`)

	var agents config.AgentConfig
	if err := json.Unmarshal([]byte(config.DefaultAgentsConfigJSON()), &agents); err != nil {
		t.Fatalf("unmarshal DefaultAgentsConfigJSON: %v", err)
	}
	var startup config.StartupConfig
	if err := json.Unmarshal([]byte(startupLiteral), &startup); err != nil {
		t.Fatalf("unmarshal scaffold startup.json literal: %v\nliteral: %s", err, startupLiteral)
	}

	// The scaffold must ship a FUNCTIONAL watchdog, not an empty refuse-on-boot one.
	if len(startup.WatchdogAgents) == 0 {
		t.Fatalf("scaffold startup.json must seed a non-empty watchdog_agents; got %v", startup.WatchdogAgents)
	}

	known := make([]string, 0, len(agents.Agents))
	for name := range agents.Agents {
		known = append(known, name)
	}

	// N6 parity: every watchdog_agents name must be a real seeded agent. Membership
	// is map-key existence in agents.json (production's agentsCfg.Agents[name] idiom),
	// NOT session existence — the boundary R1-L1 / R2-H1 both turn on.
	for _, name := range startup.WatchdogAgents {
		if _, ok := agents.Agents[name]; !ok {
			t.Errorf("scaffold watchdog_agents %q is NOT a seeded agents.json agent (the mergepatrol-class bug); seeded agents = %v",
				name, known)
		}
	}
}

// TestInstallScaffold_StartupAgentsDefaultManagerOnly pins T1/T3 (PR #410): a fresh
// `af install --init` + `af up` must auto-start ONLY the interactive manager, NOT the
// autonomous supervisor. Issue #408 intended ONLY the watchdog_agents widening; the
// scaffold's startup.json `agents` default must stay manager-only (commit 472f64a set
// it deliberately). This source-parses the REAL install.go literal — not a copy — so a
// future re-widening of `agents` cannot slip in silently (the gap that let it in).
func TestInstallScaffold_StartupAgentsDefaultManagerOnly(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	startupLiteral := extractScaffoldLiteral(t, string(data), `"startup.json":`)

	var startup config.StartupConfig
	if err := json.Unmarshal([]byte(startupLiteral), &startup); err != nil {
		t.Fatalf("unmarshal scaffold startup.json literal: %v\nliteral: %s", err, startupLiteral)
	}

	// T1/T3: default startup scope is manager-only.
	if !reflect.DeepEqual(startup.Agents, []string{"manager"}) {
		t.Errorf("scaffold startup.json agents = %v, want [manager] (T1/T3: no unapproved supervisor auto-start)", startup.Agents)
	}
	// Regression guard: the intended #408 watchdog_agents fix must be PRESERVED, not reverted.
	if !reflect.DeepEqual(startup.WatchdogAgents, []string{"manager", "supervisor"}) {
		t.Errorf("scaffold watchdog_agents = %v, want [manager supervisor] (keep the #408 fix)", startup.WatchdogAgents)
	}
}
