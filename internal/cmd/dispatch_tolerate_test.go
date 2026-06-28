package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// K6: the dispatch loop tolerates a partial factory — mappings whose agent is not in
// agents.json are partitioned out (to be skipped-and-warned), while mappings with a
// provisioned agent are kept and dispatched. Order is preserved.
func TestPartitionDispatchMappings(t *testing.T) {
	agents := &config.AgentConfig{Agents: map[string]config.AgentEntry{
		"known": {Type: "autonomous", Description: "k"},
	}}
	disp := &config.DispatchConfig{Mappings: []config.DispatchMapping{
		{Labels: []string{"a"}, Agent: "known"},
		{Labels: []string{"b"}, Agent: "ghost"},
		{Labels: []string{"c"}, Agent: "phantom"},
	}}

	known, unknown := partitionDispatchMappings(disp, agents)
	if len(known) != 1 || known[0].Agent != "known" {
		t.Errorf("known mappings = %v, want one mapping for agent 'known'", known)
	}
	if len(unknown) != 2 || unknown[0] != "ghost" || unknown[1] != "phantom" {
		t.Errorf("unknown agents = %v, want [ghost phantom] in mapping order", unknown)
	}
}

// K6: an all-provisioned config partitions to all-known, no unknowns (the common path
// after K5 seeds the specialists — identical to today's behavior).
func TestPartitionDispatchMappings_AllKnown(t *testing.T) {
	agents := &config.AgentConfig{Agents: map[string]config.AgentEntry{
		"a1": {Type: "autonomous", Description: "x"},
		"a2": {Type: "autonomous", Description: "y"},
	}}
	disp := &config.DispatchConfig{Mappings: []config.DispatchMapping{
		{Labels: []string{"l1"}, Agent: "a1"},
		{Labels: []string{"l2"}, Agent: "a2"},
	}}
	known, unknown := partitionDispatchMappings(disp, agents)
	if len(known) != 2 || len(unknown) != 0 {
		t.Errorf("all-known config = (%d known, %d unknown), want (2, 0)", len(known), len(unknown))
	}
}

// K8: the config-state classifier distinguishes the observable states the dispatcher
// can be in, so a "running-but-dispatching-nothing" loop is never silent.
func TestDispatchConfigState(t *testing.T) {
	agents := &config.AgentConfig{Agents: map[string]config.AgentEntry{
		"mgr": {Type: "autonomous", Description: "m"},
	}}
	okDisp := &config.DispatchConfig{
		Repos: []string{"o/r"}, TriggerLabel: "agentic",
		Mappings: []config.DispatchMapping{{Labels: []string{"x"}, Agent: "mgr"}},
	}
	unprovisioned := &config.DispatchConfig{
		Repos: []string{"o/r"}, TriggerLabel: "agentic",
		Mappings: []config.DispatchMapping{{Labels: []string{"x"}, Agent: "ghost"}},
	}

	cases := []struct {
		name    string
		disp    *config.DispatchConfig
		loadErr error
		want    string
	}{
		{"not configured (empty default)", nil, config.ErrMissingField, "not_configured"},
		{"not configured (absent)", nil, config.ErrNotFound, "not_configured"},
		{"ok", okDisp, nil, "ok"},
		{"references unprovisioned agents", unprovisioned, nil, "references_unprovisioned_agents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchConfigState(tc.disp, agents, tc.loadErr); got != tc.want {
				t.Errorf("dispatchConfigState = %q, want %q", got, tc.want)
			}
		})
	}
}

// K8: `af dispatch status --json` surfaces the config_state so the fresh-install
// "references unprovisioned agents" condition is observable (cross-review H2).
func TestDispatchStatus_JSON_ConfigState_Unprovisioned(t *testing.T) {
	root := t.TempDir()
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"manager":{"type":"interactive","description":"m"}}}`)
	writeAFFile(t, root, "dispatch.json",
		`{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["x"],"agent":"ghost"}],"interval_seconds":300}`)

	setupHermeticSessions(t)
	t.Chdir(root)

	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	_ = cmd.Flags().Set("json", "true")
	var buf strings.Builder
	cmd.SetOut(&buf)
	if err := runDispatchStatus(cmd, nil); err != nil {
		t.Fatalf("runDispatchStatus: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &top); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	raw, ok := top["config_state"]
	if !ok {
		t.Fatalf("status --json must include config_state; got %q", buf.String())
	}
	var state string
	_ = json.Unmarshal(raw, &state)
	if state != "references_unprovisioned_agents" {
		t.Errorf("config_state = %q, want references_unprovisioned_agents", state)
	}
}
