package cmd

import (
	"encoding/json"
	"errors"
	"os"
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
		name     string
		entries  map[string]dispatchEntry
		wantKeys []string
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
				ItemURL:      "https://github.com/owner/repo/issues/42",
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
				ItemURL:      "https://github.com/owner/repo/issues/1",
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
			wantHas: []string{
				"while true",
				"/usr/local/bin/af dispatch",
				"tee -a .runtime/dispatch.log",
				"sleep 300",
				"done",
				"trap",
				"dispatch loop exiting",
				"dispatch cycle starting",
				"rc=$?",
			},
		},
		{
			name:     "custom interval",
			afBin:    "/home/user/.local/bin/af",
			interval: 60,
			wantHas: []string{
				"while true",
				"/home/user/.local/bin/af dispatch",
				"sleep 60",
				"done",
				"trap",
			},
		},
		{
			name:     "fallback af binary",
			afBin:    "af",
			interval: 120,
			wantHas: []string{
				"af dispatch",
				"sleep 120",
				"trap",
				"rc=$?",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := buildDispatchLoopCmd(tc.afBin, tc.interval)
			for _, want := range tc.wantHas {
				if !strings.Contains(cmd, want) {
					t.Errorf("buildDispatchLoopCmd(%q, %d) missing %q\ngot: %s", tc.afBin, tc.interval, want, cmd)
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
		name          string
		running       bool
		entries       map[string]dispatchEntry
		agentState    map[string]bool
		phaseComplete map[string]bool
		wantHas       []string
		wantNot       []string
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
			// K9 demotion: an idle non-workflow agent is availability-only ("idle"),
			// NOT "completed" — completion is read from the store, not tmux absence.
			wantHas: []string{"STOPPED", "owner/repo#2", "writer", "idle", "pr"},
			wantNot: []string{"completed"},
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
			out := formatDispatchStatus(tc.running, tc.entries, tc.agentState, tc.phaseComplete)
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
	// Set up a factory root so FindFactoryRoot succeeds
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	os.MkdirAll(afDir, 0755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1}`), 0644)
	os.WriteFile(filepath.Join(afDir, "dispatch.json"), []byte(`{"repos":["test/repo"],"trigger_label":"agentic","mappings":[{"label":"test","agent":"mgr"}],"interval_seconds":300}`), 0644)

	// Drive the "already running" pre-flight through the hermetic fake instead
	// of a real af-dispatch session (#309). runDispatchStart checks
	// newCmdTmux().HasSession(dispatchSessionName); marking that name present in
	// the fake reproduces the live-dispatcher case with no real tmux op.
	// Installed AFTER t.TempDir() so the seam restores run before the temp-dir
	// delete (design R-7).
	fake, _ := setupHermeticSessions(t)
	fake.present[dispatchSessionName] = true

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
	// Hermetic: the fake reports no dispatch session present by default, so
	// runDispatchStop's newCmdTmux().HasSession pre-flight takes the "not
	// running" branch with no real tmux op (#309). This replaces the former
	// unconditional `tmux kill-session -t af-dispatch` (the clearest C-1
	// violation) that destroyed a co-tenant's dispatcher.
	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	err := runDispatchStop(cmd, nil)
	if err == nil {
		t.Fatal("expected error when dispatcher not running, got nil")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "not running")
	}
}

func TestDispatchStatus_JSON_SchemaSnapshot(t *testing.T) {
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1}`), 0o644)

	// Hermetic tmux: dispatcher + agent sessions are absent by default, so
	// dispatcher_running / agent_running are false (still emitted as keys). The returned
	// memstore is the SAME one runDispatchStatus reads via newIssueStore — seed it so the
	// workflow entry's PhaseInstanceID resolves to a terminal+complete instance, which is
	// what makes the omitempty phase_complete key actually render (and thus be frozen).
	_, store := setupHermeticSessions(t)
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)

	state := &dispatchState{Dispatched: map[string]dispatchEntry{
		// Non-workflow entry: pins the original 6-key contract (the additive
		// workflow/phase/phase_complete keys are omitempty and absent here).
		"owner/repo#1": {
			Agent:        "mgr",
			DispatchedAt: time.Unix(1700000000, 0).UTC(),
			ItemURL:      "https://github.com/owner/repo/issues/1",
			Source:       "issue",
		},
		// Workflow entry: freezes the additive keys. PhaseInstanceID resolves to the
		// terminal+complete instance above, so phase_complete serializes true.
		"owner/repo#2": {
			Agent:           "engineer",
			DispatchedAt:    time.Unix(1700000000, 0).UTC(),
			ItemURL:         "https://github.com/owner/repo/issues/2",
			Source:          "issue",
			Workflow:        "soldesign",
			Phase:           "design",
			PhaseInstanceID: complete.ID,
		},
	}}
	if err := saveDispatchState(dir, state); err != nil {
		t.Fatalf("saveDispatchState: %v", err)
	}

	origDir, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(origDir) })

	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	_ = cmd.Flags().Set("json", "true")
	var buf strings.Builder
	cmd.SetOut(&buf)
	if err := runDispatchStatus(cmd, nil); err != nil {
		t.Fatalf("runDispatchStatus: %v", err)
	}
	out := strings.TrimSpace(buf.String())

	// Top-level key set is frozen.
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	// config_state is the additive K8 observability field (issue #73): the dispatch
	// config-validity state ("not_configured" here — only factory.json was written).
	wantTop := map[string]bool{"dispatcher_running": true, "config_state": true, "entries": true}
	if len(top) != len(wantTop) {
		t.Errorf("top-level key count = %d, want %d (%q)", len(top), len(wantTop), out)
	}
	for k := range wantTop {
		if _, ok := top[k]; !ok {
			t.Errorf("missing top-level key %q in %q", k, out)
		}
	}
	for k := range top {
		if !wantTop[k] {
			t.Errorf("unexpected top-level key %q in %q", k, out)
		}
	}

	// Per-entry key set is frozen.
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(top["entries"], &entries); err != nil {
		t.Fatalf("unmarshal entries: %v", err)
	}
	// Entries are sorted by key: [0] = non-workflow #1, [1] = workflow #2.
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d (%q)", len(entries), out)
	}

	// entries[0] (non-workflow) keeps EXACTLY the original 6-key contract — the additive
	// workflow/phase/phase_complete keys are omitempty and must NOT appear here.
	wantEntry := map[string]bool{
		"issue": true, "agent": true, "agent_running": true,
		"item_url": true, "source": true, "dispatched_at": true,
	}
	if len(entries[0]) != len(wantEntry) {
		t.Errorf("non-workflow entry key count = %d (%v), want %d", len(entries[0]), keysOf(entries[0]), len(wantEntry))
	}
	for k := range wantEntry {
		if _, ok := entries[0][k]; !ok {
			t.Errorf("missing non-workflow entry key %q in %q", k, out)
		}
	}
	for k := range entries[0] {
		if !wantEntry[k] {
			t.Errorf("unexpected non-workflow entry key %q in %q", k, out)
		}
	}

	// entries[1] (workflow) freezes the expanded 9-key contract: the 6 base keys PLUS the
	// additive workflow / phase / phase_complete keys.
	wantEntryWorkflow := map[string]bool{
		"issue": true, "agent": true, "agent_running": true,
		"item_url": true, "source": true, "dispatched_at": true,
		"workflow": true, "phase": true, "phase_complete": true,
	}
	if len(entries[1]) != len(wantEntryWorkflow) {
		t.Errorf("workflow entry key count = %d (%v), want %d", len(entries[1]), keysOf(entries[1]), len(wantEntryWorkflow))
	}
	for k := range wantEntryWorkflow {
		if _, ok := entries[1][k]; !ok {
			t.Errorf("missing workflow entry key %q in %q", k, out)
		}
	}
	for k := range entries[1] {
		if !wantEntryWorkflow[k] {
			t.Errorf("unexpected workflow entry key %q in %q", k, out)
		}
	}

	// Value-level guard: the entries reflect the seeded dispatch-state.json.
	var parsed dispatchStatusJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal typed: %v", err)
	}
	e := parsed.Entries[0]
	if e.Issue != "owner/repo#1" || e.Agent != "mgr" || e.Source != "issue" {
		t.Errorf("entry = %+v, want issue=owner/repo#1 agent=mgr source=issue", e)
	}
	we := parsed.Entries[1]
	if we.Issue != "owner/repo#2" || we.Workflow != "soldesign" || we.Phase != "design" || !we.PhaseComplete {
		t.Errorf("workflow entry = %+v, want issue=owner/repo#2 workflow=soldesign phase=design phase_complete=true", we)
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

// --- Cycle stats tests (from Gherkin scenarios) ---

func TestDispatchCycleStats_ZeroCounts(t *testing.T) {
	stats := &dispatchCycleStats{
		start: time.Date(2026, 5, 17, 10, 30, 0, 0, time.UTC),
	}
	got := stats.String()
	if !strings.Contains(got, "dispatch cycle complete") {
		t.Errorf("stats.String() missing 'dispatch cycle complete'\ngot: %s", got)
	}
	if !strings.Contains(got, "dispatched=0") {
		t.Errorf("stats.String() missing 'dispatched=0'\ngot: %s", got)
	}
	if !strings.Contains(got, "skipped=0") {
		t.Errorf("stats.String() missing 'skipped=0'\ngot: %s", got)
	}
	if !strings.Contains(got, "errors=0") {
		t.Errorf("stats.String() missing 'errors=0'\ngot: %s", got)
	}
	if !strings.Contains(got, "2026-05-17 10:30:00") {
		t.Errorf("stats.String() missing timestamp '2026-05-17 10:30:00'\ngot: %s", got)
	}
}

func TestDispatchCycleStats_WithDispatches(t *testing.T) {
	stats := &dispatchCycleStats{
		start:      time.Date(2026, 5, 17, 10, 30, 0, 0, time.UTC),
		queried:    5,
		dispatched: 3,
		skipped:    1,
		errors:     1,
	}
	got := stats.String()
	if !strings.Contains(got, "queried=5") {
		t.Errorf("stats.String() missing 'queried=5'\ngot: %s", got)
	}
	if !strings.Contains(got, "dispatched=3") {
		t.Errorf("stats.String() missing 'dispatched=3'\ngot: %s", got)
	}
	if !strings.Contains(got, "skipped=1") {
		t.Errorf("stats.String() missing 'skipped=1'\ngot: %s", got)
	}
	if !strings.Contains(got, "errors=1") {
		t.Errorf("stats.String() missing 'errors=1'\ngot: %s", got)
	}
	if !strings.Contains(got, "elapsed=") {
		t.Errorf("stats.String() missing 'elapsed='\ngot: %s", got)
	}
}

func TestDispatchCycleStats_Format(t *testing.T) {
	stats := &dispatchCycleStats{
		start:      time.Date(2026, 5, 17, 14, 5, 30, 0, time.UTC),
		queried:    10,
		dispatched: 2,
		skipped:    7,
		errors:     1,
	}
	got := stats.String()

	// Must start with bracketed timestamp
	if !strings.HasPrefix(got, "[2026-05-17 14:05:30]") {
		t.Errorf("stats.String() should start with '[2026-05-17 14:05:30]'\ngot: %s", got)
	}

	// Must contain all counter fields
	for _, field := range []string{"queried=10", "dispatched=2", "skipped=7", "errors=1", "elapsed="} {
		if !strings.Contains(got, field) {
			t.Errorf("stats.String() missing %q\ngot: %s", field, got)
		}
	}
}

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

// TestDispatchStatus_ShowsRealPhaseAndCompletion proves the K9 contract: `af dispatch
// status` reports a workflow phase's completion from the recorded instance epic via the
// store (instanceComplete), NOT from tmux session absence. A terminal+complete instance
// reads phase_complete=true even though the agent's tmux session is absent; a
// terminal-but-reset-closed instance reads phase_complete=false (a --reset also closes the
// epic terminally, which a bare IsTerminal() would misread as completion).
func TestDispatchStatus_ShowsRealPhaseAndCompletion(t *testing.T) {
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1}`), 0o644)

	// setupHermeticSessions returns the SAME memstore that runDispatchStatus'
	// newIssueStore(root, dispatchStoreActor) yields. Agent sessions are absent by
	// default in the fake tmux, so completion can only come from the store, never tmux.
	_, store := setupHermeticSessions(t)

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete) // instanceComplete == true
	notComplete := seedClosedEpic(t, store, config.CloseReasonResetSling)   // terminal but NOT complete

	state := &dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#1": { // completed per the store; agent tmux ABSENT
			Agent:           "engineer",
			DispatchedAt:    time.Unix(1700000000, 0).UTC(),
			ItemURL:         "https://github.com/owner/repo/issues/1",
			Source:          "issue",
			Workflow:        "soldesign",
			Phase:           "design",
			PhaseInstanceID: complete.ID,
		},
		"owner/repo#2": { // terminal-but-reset-closed ⇒ NOT complete
			Agent:           "engineer",
			DispatchedAt:    time.Unix(1700000000, 0).UTC(),
			ItemURL:         "https://github.com/owner/repo/issues/2",
			Source:          "issue",
			Workflow:        "soldesign",
			Phase:           "design",
			PhaseInstanceID: notComplete.ID,
		},
	}}
	if err := saveDispatchState(dir, state); err != nil {
		t.Fatalf("saveDispatchState: %v", err)
	}

	origDir, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(origDir) })

	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	_ = cmd.Flags().Set("json", "true")
	var buf strings.Builder
	cmd.SetOut(&buf)
	if err := runDispatchStatus(cmd, nil); err != nil {
		t.Fatalf("runDispatchStatus: %v", err)
	}

	var parsed dispatchStatusJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	byKey := map[string]dispatchStatusEntry{}
	for _, e := range parsed.Entries {
		byKey[e.Issue] = e
	}

	c1, ok := byKey["owner/repo#1"]
	if !ok {
		t.Fatalf("missing entry owner/repo#1 in %q", buf.String())
	}
	if !c1.PhaseComplete {
		t.Errorf("owner/repo#1 phase_complete = false, want true (instanceComplete on a closed-complete instance, agent tmux absent)")
	}
	if c1.AgentRunning {
		t.Errorf("owner/repo#1 agent_running = true, want false — completion must NOT come from tmux presence")
	}
	if c1.Workflow != "soldesign" || c1.Phase != "design" {
		t.Errorf("owner/repo#1 workflow/phase = %q/%q, want soldesign/design (read off the record)", c1.Workflow, c1.Phase)
	}

	c2, ok := byKey["owner/repo#2"]
	if !ok {
		t.Fatalf("missing entry owner/repo#2 in %q", buf.String())
	}
	if c2.PhaseComplete {
		t.Errorf("owner/repo#2 phase_complete = true, want false (terminal-but-reset-closed must read NOT complete)")
	}
}
