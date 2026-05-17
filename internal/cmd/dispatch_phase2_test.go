package cmd

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func TestMatchItemToAgent_ANDSemantics_AllLabelsMatch(t *testing.T) {
	item := ghItem{
		Number: 1,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/1",
		Labels: []ghLabel{{Name: "bug"}, {Name: "backend"}, {Name: "agentic"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug", "backend"}, Agent: "debugger"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "debugger" {
		t.Errorf("matchItemToAgent() = %q, want %q", got, "debugger")
	}
}

func TestMatchItemToAgent_ANDSemantics_PartialMatchFails(t *testing.T) {
	item := ghItem{
		Number: 2,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/2",
		Labels: []ghLabel{{Name: "bug"}, {Name: "agentic"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug", "backend"}, Agent: "debugger"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "" {
		t.Errorf("matchItemToAgent() = %q, want empty string (partial match should fail)", got)
	}
}

func TestMatchItemToAgent_ANDSemantics_SingleLabelBackwardCompat(t *testing.T) {
	item := ghItem{
		Number: 3,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/3",
		Labels: []ghLabel{{Name: "bug-triage"}, {Name: "agentic"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug-triage"}, Agent: "debugger"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "debugger" {
		t.Errorf("matchItemToAgent() = %q, want %q", got, "debugger")
	}
}

func TestMatchItemToAgent_ANDSemantics_FirstMatchWins(t *testing.T) {
	item := ghItem{
		Number: 4,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/4",
		Labels: []ghLabel{{Name: "bug"}, {Name: "backend"}, {Name: "docs"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug", "backend"}, Agent: "debugger"},
		{Labels: []string{"docs"}, Agent: "writer"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "debugger" {
		t.Errorf("matchItemToAgent() = %q, want %q (first match wins)", got, "debugger")
	}
}

func TestMatchItemToAgent_ANDSemantics_NoMatch(t *testing.T) {
	item := ghItem{
		Number: 5,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/5",
		Labels: []ghLabel{{Name: "agentic"}, {Name: "feature"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug", "backend"}, Agent: "debugger"},
		{Labels: []string{"docs"}, Agent: "writer"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "" {
		t.Errorf("matchItemToAgent() = %q, want empty string", got)
	}
}

func TestGroupMappingsBySource_SplitsCorrectly(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug"}, Source: "issue", Agent: "debugger"},
		{Labels: []string{"feat"}, Source: "pr", Agent: "builder"},
		{Labels: []string{"docs"}, Source: "issue", Agent: "writer"},
	}

	issues, prs := groupMappingsBySource(mappings)

	if len(issues) != 2 {
		t.Fatalf("issues count = %d, want 2", len(issues))
	}
	if len(prs) != 1 {
		t.Fatalf("prs count = %d, want 1", len(prs))
	}
	if issues[0].Agent != "debugger" {
		t.Errorf("issues[0].Agent = %q, want %q", issues[0].Agent, "debugger")
	}
	if issues[1].Agent != "writer" {
		t.Errorf("issues[1].Agent = %q, want %q", issues[1].Agent, "writer")
	}
	if prs[0].Agent != "builder" {
		t.Errorf("prs[0].Agent = %q, want %q", prs[0].Agent, "builder")
	}
}

func TestGroupMappingsBySource_DefaultsToIssues(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug"}, Source: "", Agent: "debugger"},
		{Labels: []string{"feat"}, Source: "issue", Agent: "writer"},
	}

	issues, prs := groupMappingsBySource(mappings)

	if len(issues) != 2 {
		t.Fatalf("issues count = %d, want 2 (empty source defaults to issues)", len(issues))
	}
	if len(prs) != 0 {
		t.Fatalf("prs count = %d, want 0", len(prs))
	}
}

func TestGroupMappingsBySource_AllPRs(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"review"}, Source: "pr", Agent: "reviewer"},
		{Labels: []string{"ci-fix"}, Source: "pr", Agent: "devops"},
	}

	issues, prs := groupMappingsBySource(mappings)

	if len(issues) != 0 {
		t.Fatalf("issues count = %d, want 0", len(issues))
	}
	if len(prs) != 2 {
		t.Fatalf("prs count = %d, want 2", len(prs))
	}
}

func TestGroupMappingsBySource_Empty(t *testing.T) {
	issues, prs := groupMappingsBySource(nil)

	if issues != nil {
		t.Errorf("issues = %v, want nil", issues)
	}
	if prs != nil {
		t.Errorf("prs = %v, want nil", prs)
	}
}
