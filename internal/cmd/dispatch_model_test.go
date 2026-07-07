package cmd

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestDispatch_MappingModel_ThreadsArgv (issue #480): a dispatch mapping's
// Model is surfaced from the matched mapping (matchItemToAgent) and threaded into the
// `af sling` argv as `--model <name>` (buildSlingArgs) — and omitted when the mapping
// names no model. These are the two links in the chain dispatch.go wires together at
// the call site (matchItemToAgent -> dispatchItem).
func TestDispatch_MappingModel_ThreadsArgv(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug"}, Agent: "debugger", Model: "opus"},
		{Labels: []string{"chore"}, Agent: "janitor"}, // no model
	}

	// 1. The matched mapping's Model is surfaced alongside the agent (no re-match).
	item := ghItem{Labels: []ghLabel{{Name: "bug"}}}
	agent, model := matchItemToAgent(item, mappings)
	if agent != "debugger" {
		t.Fatalf("matchItemToAgent agent = %q, want \"debugger\"", agent)
	}
	if model != "opus" {
		t.Fatalf("matchItemToAgent must surface the mapping model; got %q, want \"opus\"", model)
	}

	// 2. buildSlingArgs threads `--model opus` (immediately followed by the value) into
	//    the sling argv, before the positional itemURL.
	args := buildSlingArgs(agent, "manager", model, "https://example/issues/1")
	if !containsAdjacent(args, "--model", "opus") {
		t.Errorf("argv must contain \"--model\" immediately followed by \"opus\"; got %v", args)
	}
	if args[len(args)-1] != "https://example/issues/1" {
		t.Errorf("itemURL must remain the final positional arg; got %v", args)
	}

	// 3. A mapping with no model must NOT emit --model.
	choreItem := ghItem{Labels: []ghLabel{{Name: "chore"}}}
	cAgent, cModel := matchItemToAgent(choreItem, mappings)
	if cAgent != "janitor" || cModel != "" {
		t.Fatalf("no-model mapping: agent=%q model=%q, want janitor/\"\"", cAgent, cModel)
	}
	noModelArgs := buildSlingArgs(cAgent, "manager", cModel, "https://example/issues/2")
	for _, a := range noModelArgs {
		if a == "--model" {
			t.Errorf("no --model must be emitted when the mapping names no model; got %v", noModelArgs)
		}
	}
}

// containsAdjacent reports whether want is immediately followed by val in args.
func containsAdjacent(args []string, want, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == want && args[i+1] == val {
			return true
		}
	}
	return false
}
