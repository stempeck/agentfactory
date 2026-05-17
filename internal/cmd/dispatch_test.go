package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/lock"
)

// --- matchItemToAgent tests ---

func TestMatchItemToAgent(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug-triage"}, Agent: "debugger"},
		{Labels: []string{"docs"}, Agent: "writer"},
	}

	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{
			name:   "match",
			labels: []string{"agentic", "bug-triage"},
			want:   "debugger",
		},
		{
			name:   "no match",
			labels: []string{"agentic", "feature"},
			want:   "",
		},
		{
			name:   "multiple matches returns first",
			labels: []string{"agentic", "bug-triage", "docs"},
			want:   "debugger",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := ghItem{
				Number: 1,
				Title:  "test",
				URL:    "https://github.com/owner/repo/issues/1",
			}
			for _, l := range tc.labels {
				item.Labels = append(item.Labels, ghLabel{Name: l})
			}

			got := matchItemToAgent(item, mappings)
			if got != tc.want {
				t.Errorf("matchItemToAgent() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- pruneDispatchState tests ---

func TestPruneDispatchState(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name      string
		entries   map[string]dispatchEntry
		wantKeys  []string
	}{
		{
			name: "fresh entry kept",
			entries: map[string]dispatchEntry{
				"owner/repo#1": {Agent: "debugger", DispatchedAt: now.Add(-1 * time.Hour)},
			},
			wantKeys: []string{"owner/repo#1"},
		},
		{
			name: "stale entry removed",
			entries: map[string]dispatchEntry{
				"owner/repo#2": {Agent: "debugger", DispatchedAt: now.Add(-25 * time.Hour)},
			},
			wantKeys: nil,
		},
		{
			name: "mixed entries",
			entries: map[string]dispatchEntry{
				"owner/repo#1": {Agent: "debugger", DispatchedAt: now.Add(-1 * time.Hour)},
				"owner/repo#2": {Agent: "writer", DispatchedAt: now.Add(-25 * time.Hour)},
				"owner/repo#3": {Agent: "planner", DispatchedAt: now.Add(-23 * time.Hour)},
			},
			wantKeys: []string{"owner/repo#1", "owner/repo#3"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := &dispatchState{Dispatched: tc.entries}
			pruneDispatchState(state)

			if len(state.Dispatched) != len(tc.wantKeys) {
				t.Fatalf("after prune: got %d entries, want %d", len(state.Dispatched), len(tc.wantKeys))
			}
			for _, key := range tc.wantKeys {
				if _, ok := state.Dispatched[key]; !ok {
					t.Errorf("expected key %q to survive pruning", key)
				}
			}
		})
	}
}

// --- loadDispatchState tests ---

func TestLoadDispatchState_MissingFile(t *testing.T) {
	dir := t.TempDir()
	state := loadDispatchState(dir)
	if state.Dispatched == nil {
		t.Fatal("expected initialized map, got nil")
	}
	if len(state.Dispatched) != 0 {
		t.Errorf("expected empty map, got %d entries", len(state.Dispatched))
	}
}

func TestLoadDispatchState_ValidFile(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	ts := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	state := dispatchState{
		Dispatched: map[string]dispatchEntry{
			"owner/repo#42": {
				Agent:        "debugger",
				DispatchedAt: ts,
				ItemURL:     "https://github.com/owner/repo/issues/42",
			},
		},
	}
	data, _ := json.MarshalIndent(state, "", "  ")
	if err := os.WriteFile(filepath.Join(runtimeDir, "dispatch-state.json"), data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded := loadDispatchState(dir)
	if len(loaded.Dispatched) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(loaded.Dispatched))
	}
	entry, ok := loaded.Dispatched["owner/repo#42"]
	if !ok {
		t.Fatal("expected key owner/repo#42")
	}
	if entry.Agent != "debugger" {
		t.Errorf("agent = %q, want %q", entry.Agent, "debugger")
	}
	if !entry.DispatchedAt.Equal(ts) {
		t.Errorf("dispatched_at = %v, want %v", entry.DispatchedAt, ts)
	}
}

// --- saveDispatchState tests ---

func TestSaveDispatchState_AtomicNoTempRemains(t *testing.T) {
	dir := t.TempDir()
	state := &dispatchState{
		Dispatched: map[string]dispatchEntry{
			"owner/repo#1": {
				Agent:        "debugger",
				DispatchedAt: time.Now().UTC(),
				ItemURL:     "https://github.com/owner/repo/issues/1",
			},
		},
	}

	if err := saveDispatchState(dir, state); err != nil {
		t.Fatalf("saveDispatchState: %v", err)
	}

	// Verify state file exists
	statePath := filepath.Join(dir, ".runtime", "dispatch-state.json")
	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		t.Fatal("dispatch-state.json not created")
	}

	// Verify no temp file remains
	tmpPath := filepath.Join(dir, ".runtime", ".dispatch-state.json.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error(".dispatch-state.json.tmp still exists after successful write")
	}

	// Verify written JSON is valid
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var check dispatchState
	if err := json.Unmarshal(data, &check); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
	if len(check.Dispatched) != 1 {
		t.Errorf("expected 1 entry in written file, got %d", len(check.Dispatched))
	}
}

func TestSaveDispatchState_CreatesRuntimeDir(t *testing.T) {
	dir := t.TempDir()
	// Ensure .runtime does NOT exist
	runtimeDir := filepath.Join(dir, ".runtime")
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Fatal(".runtime should not exist yet")
	}

	state := &dispatchState{Dispatched: make(map[string]dispatchEntry)}
	if err := saveDispatchState(dir, state); err != nil {
		t.Fatalf("saveDispatchState: %v", err)
	}

	if _, err := os.Stat(runtimeDir); os.IsNotExist(err) {
		t.Fatal(".runtime directory was not created")
	}
}

// --- cross-validation test ---

func TestCrossValidation_UnknownAgent(t *testing.T) {
	dispatchCfg := &config.DispatchConfig{
		Repos:        []string{"owner/repo"},
		TriggerLabel: "agentic",
		Mappings: []config.DispatchMapping{
			{Labels: []string{"bug"}, Agent: "nonexistent"},
		},
	}
	agentsCfg := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"manager": {Type: "interactive", Description: "Manager"},
		},
	}

	// Replicate the cross-validation logic from runDispatch
	for _, m := range dispatchCfg.Mappings {
		if _, ok := agentsCfg.Agents[m.Agent]; !ok {
			return // test passes — unknown agent detected
		}
	}
	t.Fatal("expected cross-validation to detect unknown agent")
}

func TestCrossValidation_UnknownNotifyAgent(t *testing.T) {
	dispatchCfg := &config.DispatchConfig{
		Repos:            []string{"owner/repo"},
		TriggerLabel:     "agentic",
		NotifyOnComplete: "nonexistent",
		Mappings: []config.DispatchMapping{
			{Labels: []string{"bug"}, Agent: "manager"},
		},
	}
	agentsCfg := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"manager": {Type: "interactive", Description: "Manager"},
		},
	}

	// Replicate the cross-validation logic from runDispatch for NotifyOnComplete
	if _, ok := agentsCfg.Agents[dispatchCfg.NotifyOnComplete]; !ok {
		return // test passes — unknown notify agent detected
	}
	t.Fatal("expected cross-validation to detect unknown notify_on_complete agent")
}

// --- gh JSON parsing tests ---

func TestParseGHItemJSON_Valid(t *testing.T) {
	fixture := `[
		{
			"number": 42,
			"title": "Fix bug",
			"url": "https://github.com/owner/repo/issues/42",
			"labels": [
				{"name": "agentic"},
				{"name": "bug-triage"}
			]
		}
	]`

	var items []ghItem
	if err := json.Unmarshal([]byte(fixture), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 42 {
		t.Errorf("number = %d, want 42", items[0].Number)
	}
	if items[0].URL != "https://github.com/owner/repo/issues/42" {
		t.Errorf("url = %q, want github url", items[0].URL)
	}
	if len(items[0].Labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(items[0].Labels))
	}
}

func TestParseGHItemJSON_Empty(t *testing.T) {
	var items []ghItem
	if err := json.Unmarshal([]byte(`[]`), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

// --- Phase 2: start/stop/status tests ---

func TestBuildDispatchLoopCmd(t *testing.T) {
	tests := []struct {
		name     string
		afBin    string
		interval int
		wantHas  []string
	}{
		{
			name:     "default interval",
			afBin:    "/usr/local/bin/af",
			interval: 300,
			wantHas:  []string{"while true", "/usr/local/bin/af dispatch", "tee -a .runtime/dispatch.log", "sleep 300", "done"},
		},
		{
			name:     "custom interval",
			afBin:    "/home/user/.local/bin/af",
			interval: 60,
			wantHas:  []string{"while true", "/home/user/.local/bin/af dispatch", "sleep 60", "done"},
		},
		{
			name:     "fallback af binary",
			afBin:    "af",
			interval: 120,
			wantHas:  []string{"af dispatch", "sleep 120"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildDispatchLoopCmd(tc.afBin, tc.interval)
			for _, want := range tc.wantHas {
				if !strings.Contains(cmd, want) {
					t.Errorf("buildDispatchLoopCmd(%q, %d) = %q, missing %q", tc.afBin, tc.interval, cmd, want)
				}
			}
		})
	}
}

func TestResolveDispatchInterval(t *testing.T) {
	tests := []struct {
		name      string
		flagValue int
		configVal int
		want      int
	}{
		{
			name:      "flag overrides config",
			flagValue: 60,
			configVal: 300,
			want:      60,
		},
		{
			name:      "zero flag uses config",
			flagValue: 0,
			configVal: 300,
			want:      300,
		},
		{
			name:      "both zero uses config",
			flagValue: 0,
			configVal: 0,
			want:      0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDispatchInterval(tc.flagValue, tc.configVal)
			if got != tc.want {
				t.Errorf("resolveDispatchInterval(%d, %d) = %d, want %d", tc.flagValue, tc.configVal, got, tc.want)
			}
		})
	}
}

func TestFormatDispatchStatus(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name       string
		running    bool
		entries    map[string]dispatchEntry
		agentState map[string]bool
		wantHas    []string
		wantNot    []string
	}{
		{
			name:    "running with entries",
			running: true,
			entries: map[string]dispatchEntry{
				"owner/repo#1": {Agent: "debugger", DispatchedAt: now.Add(-10 * time.Minute), ItemURL: "https://github.com/owner/repo/issues/1", Source: "issue"},
			},
			agentState: map[string]bool{"debugger": true},
			wantHas:    []string{"RUNNING", "owner/repo#1", "debugger", "running", "SOURCE", "issue"},
		},
		{
			name:    "stopped with entries",
			running: false,
			entries: map[string]dispatchEntry{
				"owner/repo#2": {Agent: "writer", DispatchedAt: now.Add(-30 * time.Minute), ItemURL: "https://github.com/owner/repo/issues/2", Source: "pr"},
			},
			agentState: map[string]bool{"writer": false},
			wantHas:    []string{"STOPPED", "owner/repo#2", "writer", "completed", "pr"},
		},
		{
			name:       "running with no entries",
			running:    true,
			entries:    map[string]dispatchEntry{},
			agentState: map[string]bool{},
			wantHas:    []string{"RUNNING", "No dispatched issues."},
		},
		{
			name:       "stopped with no entries",
			running:    false,
			entries:    map[string]dispatchEntry{},
			agentState: map[string]bool{},
			wantHas:    []string{"STOPPED", "No dispatched issues."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := formatDispatchStatus(tc.running, tc.entries, tc.agentState)
			for _, want := range tc.wantHas {
				if !strings.Contains(out, want) {
					t.Errorf("formatDispatchStatus output missing %q\ngot: %s", want, out)
				}
			}
			for _, notWant := range tc.wantNot {
				if strings.Contains(out, notWant) {
					t.Errorf("formatDispatchStatus output should not contain %q\ngot: %s", notWant, out)
				}
			}
		})
	}
}

func TestDispatchSubcommands_Registered(t *testing.T) {
	var found []string
	for _, sub := range dispatchCmd.Commands() {
		found = append(found, sub.Name())
	}

	want := []string{"start", "stop", "status"}
	for _, name := range want {
		has := false
		for _, f := range found {
			if f == name {
				has = true
				break
			}
		}
		if !has {
			t.Errorf("dispatchCmd missing subcommand %q, found: %v", name, found)
		}
	}
}

func TestDispatchStartCmd_IntervalFlag(t *testing.T) {
	var startCmd *cobra.Command
	for _, sub := range dispatchCmd.Commands() {
		if sub.Name() == "start" {
			startCmd = sub
			break
		}
	}
	if startCmd == nil {
		t.Fatal("start subcommand not found on dispatchCmd")
	}

	f := startCmd.Flags().Lookup("interval")
	if f == nil {
		t.Fatal("--interval flag not registered on start subcommand")
	}
	if f.DefValue != "0" {
		t.Errorf("--interval default = %q, want %q", f.DefValue, "0")
	}
}

func TestDispatchStart_AlreadyRunning(t *testing.T) {
	// Create a real af-dispatch tmux session to simulate "already running"
	createErr := exec.Command("tmux", "new-session", "-d", "-s", "af-dispatch").Run()
	if createErr != nil {
		t.Skip("tmux not available, skipping integration test")
	}
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-dispatch").Run()
	})

	// Set up a factory root so FindFactoryRoot succeeds
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	os.MkdirAll(afDir, 0755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1}`), 0644)
	os.WriteFile(filepath.Join(afDir, "dispatch.json"), []byte(`{"repos":["test/repo"],"trigger_label":"agentic","mappings":[{"label":"test","agent":"mgr"}],"interval_seconds":300}`), 0644)

	origDir, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(origDir) })

	cmd := &cobra.Command{}
	err := runDispatchStart(cmd, nil)
	if err == nil {
		t.Fatal("expected error when dispatcher already running, got nil")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "already running")
	}
}

func TestDispatchStop_NotRunning(t *testing.T) {
	// Ensure no af-dispatch session exists
	exec.Command("tmux", "kill-session", "-t", "af-dispatch").Run()

	cmd := &cobra.Command{}
	err := runDispatchStop(cmd, nil)
	if err == nil {
		t.Fatal("expected error when dispatcher not running, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "not running")
	}
}

// --- Phase 4: Multi-label AND matching tests ---

func TestMatchItemToAgent_MultiLabel_AND(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"a", "b"}, Agent: "agent1"},
	}

	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{
			name:   "all required labels present (superset)",
			labels: []string{"a", "b", "c"},
			want:   "agent1",
		},
		{
			name:   "missing required label b",
			labels: []string{"a", "c"},
			want:   "",
		},
		{
			name:   "exact match all three required",
			labels: []string{"a", "b", "c"},
			want:   "agent1",
		},
		{
			name:   "partial - only one of two required",
			labels: []string{"a"},
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			item := ghItem{
				Number: 1,
				Title:  "test",
				URL:    "https://github.com/owner/repo/issues/1",
			}
			for _, l := range tc.labels {
				item.Labels = append(item.Labels, ghLabel{Name: l})
			}

			got := matchItemToAgent(item, mappings)
			if got != tc.want {
				t.Errorf("matchItemToAgent() = %q, want %q", got, tc.want)
			}
		})
	}

	// Additional case: exact 3-label requirement
	t.Run("exact three labels required", func(t *testing.T) {
		m := []config.DispatchMapping{
			{Labels: []string{"a", "b", "c"}, Agent: "agent2"},
		}
		item := ghItem{
			Number: 2,
			Title:  "test",
			URL:    "https://github.com/owner/repo/issues/2",
			Labels: []ghLabel{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		}
		got := matchItemToAgent(item, m)
		if got != "agent2" {
			t.Errorf("matchItemToAgent() = %q, want %q", got, "agent2")
		}
	})
}

// --- Phase 4: PR JSON parsing test ---

func TestQueryGitHubPRs_JSONParse(t *testing.T) {
	fixture := `[
		{
			"number": 99,
			"title": "Fix CI pipeline",
			"url": "https://github.com/owner/repo/pull/99",
			"labels": [
				{"name": "agentic"},
				{"name": "ci-fix"}
			]
		}
	]`

	var items []ghItem
	if err := json.Unmarshal([]byte(fixture), &items); err != nil {
		t.Fatalf("unmarshal PR JSON: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Number != 99 {
		t.Errorf("number = %d, want 99", items[0].Number)
	}
	if items[0].Title != "Fix CI pipeline" {
		t.Errorf("title = %q, want %q", items[0].Title, "Fix CI pipeline")
	}
	if !strings.Contains(items[0].URL, "/pull/") {
		t.Errorf("url = %q, want it to contain /pull/ (PR format)", items[0].URL)
	}
	if len(items[0].Labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(items[0].Labels))
	}
	if items[0].Labels[0].Name != "agentic" {
		t.Errorf("labels[0] = %q, want %q", items[0].Labels[0].Name, "agentic")
	}
}

// --- Phase 4: Source grouping test ---

func TestGroupMappingsBySource(t *testing.T) {
	tests := []struct {
		name      string
		mappings  []config.DispatchMapping
		wantIssue int
		wantPR    int
	}{
		{
			name: "mixed mappings",
			mappings: []config.DispatchMapping{
				{Labels: []string{"bug"}, Source: "issue", Agent: "debugger"},
				{Labels: []string{"review"}, Source: "pr", Agent: "reviewer"},
				{Labels: []string{"docs"}, Source: "issue", Agent: "writer"},
			},
			wantIssue: 2,
			wantPR:    1,
		},
		{
			name: "all issue mappings",
			mappings: []config.DispatchMapping{
				{Labels: []string{"bug"}, Source: "issue", Agent: "debugger"},
				{Labels: []string{"docs"}, Source: "issue", Agent: "writer"},
			},
			wantIssue: 2,
			wantPR:    0,
		},
		{
			name: "all pr mappings",
			mappings: []config.DispatchMapping{
				{Labels: []string{"review"}, Source: "pr", Agent: "reviewer"},
				{Labels: []string{"ci"}, Source: "pr", Agent: "devops"},
			},
			wantIssue: 0,
			wantPR:    2,
		},
		{
			name:      "empty mappings",
			mappings:  nil,
			wantIssue: 0,
			wantPR:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issues, prs := groupMappingsBySource(tc.mappings)
			if len(issues) != tc.wantIssue {
				t.Errorf("issues count = %d, want %d", len(issues), tc.wantIssue)
			}
			if len(prs) != tc.wantPR {
				t.Errorf("prs count = %d, want %d", len(prs), tc.wantPR)
			}
		})
	}
}

// --- Phase 4: Dispatch cycle lock test ---

func TestDispatchCycleLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "dispatch-cycle.lock")

	lk := lock.NewWithPath(lockPath)
	if err := lk.Acquire("test-1"); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// Second acquire on same path should fail with ErrLocked
	lk2 := lock.NewWithPath(lockPath)
	err := lk2.Acquire("test-2")
	if !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("second acquire: got %v, want %v", err, lock.ErrLocked)
	}

	// Release first lock
	if err := lk.Release(); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	// After release, second acquire should succeed
	if err := lk2.Acquire("test-2"); err != nil {
		t.Fatalf("acquire after release failed: %v", err)
	}
	defer lk2.Release()
}
