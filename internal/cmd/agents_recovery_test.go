package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// K10-cli (#596 Phase 4A) value-level coverage. TestAgentsList_JSON_SchemaSnapshot
// pins the KEY set; only these tests pin what the three new keys actually say, which
// is where every failure mode of this phase lives — a 0 that should have been -1, a
// "malformed" laundered into "none", a latched breaker reading "none".
//
// None of these may call t.Parallel: they reassign newCmdTmux, which
// tmux_isolation_enforce_test.go's seamReassignPattern flags.

// agentRecoveryRow decodes only the fields K10-cli adds, so an unrelated contract
// change elsewhere in agentListItem cannot make these tests fail for the wrong reason.
type agentRecoveryRow struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	Running      bool   `json:"running"`
	ForeignRoot  bool   `json:"foreign_root"`
	ContextPct   int    `json:"context_pct"`
	ContextState string `json:"context_state"`
	Recovery     string `json:"recovery"`
}

func decodeAgentRecoveryRows(t *testing.T, out string) map[string]agentRecoveryRow {
	t.Helper()
	var arr []agentRecoveryRow
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &arr); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	rows := make(map[string]agentRecoveryRow, len(arr))
	for _, r := range arr {
		rows[r.Name] = r
	}
	return rows
}

// plantRawSnapshot writes a snapshot file verbatim, without the schema-v2 field set
// plantSnapshot guarantees. It is the only way to produce the MALFORMED case: a file
// that IS attributable to an agent but carries no usable datum.
func plantRawSnapshot(t *testing.T, root, sessionID, body string) {
	t.Helper()
	dir := config.StatuslineSessionsDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

// TestAgentsList_ContextAndRecoveryFields is the K10-cli value contract: every row
// carries an honest occupancy reading and an honest breaker verdict, and the two
// "no datum" encodings (-1, and the five-literal ChannelState passed through
// unmapped) are never laundered into a healthy-looking zero.
func TestAgentsList_ContextAndRecoveryFields(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{
		"freshworker":{"type":"autonomous","description":"d","formula":"minimalworker"},
		"darkworker":{"type":"autonomous","description":"d","formula":"minimalworker"},
		"neverworker":{"type":"autonomous","description":"d","formula":"minimalworker"},
		"badworker":{"type":"autonomous","description":"d","formula":"minimalworker"},
		"haltedworker":{"type":"autonomous","description":"d","formula":"minimalworker"},
		"recovworker":{"type":"autonomous","description":"d","formula":"minimalworker"},
		"stoppedworker":{"type":"autonomous","description":"d","formula":"minimalworker"}
	}}`)
	installMemStore(t)
	// stoppedworker is deliberately absent from the session list: it is not running,
	// so it is outside the sweep's roster and must read as unknown, not as 0%.
	installFakeTmuxPresent(t,
		session.SessionName("freshworker"),
		session.SessionName("darkworker"),
		session.SessionName("neverworker"),
		session.SessionName("badworker"),
		session.SessionName("haltedworker"),
		session.SessionName("recovworker"),
	)

	// runAgentsList reads the wall clock, so written_at must be relative to time.Now().
	now := time.Now()
	cfg := testRecoveryConfig()
	plantSnapshot(t, realDir, "freshworker", "sess-fresh", 92, now.Add(-10*time.Second), now, cfg)
	plantSnapshot(t, realDir, "darkworker", "sess-dark", 20, now.Add(-2*time.Hour), now, cfg)
	// Attributable to badworker but carrying no context_used_pct ⇒ malformed, never none.
	plantRawSnapshot(t, realDir, "sess-bad", `{"schema":2,"session_id":"sess-bad","agent":"badworker","written_at":"`+
		now.Add(-10*time.Second).UTC().Format(time.RFC3339)+`"}`)

	if err := saveRecoveryState(realDir, "haltedworker",
		recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3}); err != nil {
		t.Fatalf("plant halted breaker: %v", err)
	}
	if err := saveRecoveryState(realDir, "recovworker",
		recoveryState{Attempts: 1, WindowStart: recoveryStamp(now)}); err != nil {
		t.Fatalf("plant recovering breaker: %v", err)
	}

	rows := decodeAgentRecoveryRows(t, invokeAgentsList(t))

	cases := []struct {
		agent        string
		contextPct   int
		contextState string
		recovery     string
		why          string
	}{
		{"freshworker", 92, "fresh", "none", "a validated datum inside the staleness window"},
		{"darkworker", 20, "dark", "none", "a validated datum past dark_grace_secs still exposes its pct"},
		{"neverworker", -1, "none", "none", "no snapshot at all ⇒ -1, never 0"},
		{"badworker", -1, "malformed", "none", "INV-5: malformed must not collapse into none"},
		{"haltedworker", -1, "none", "halted", "a latched breaker is visible on the row"},
		{"recovworker", -1, "none", "recovering", "an in-window attempt count reads recovering"},
		{"stoppedworker", -1, "none", "none", "not running ⇒ outside the sweep roster"},
	}
	for _, c := range cases {
		row, ok := rows[c.agent]
		if !ok {
			t.Fatalf("missing row for %s (rows: %v)", c.agent, rows)
		}
		if row.ContextPct != c.contextPct {
			t.Errorf("%s context_pct = %d, want %d (%s)", c.agent, row.ContextPct, c.contextPct, c.why)
		}
		if row.ContextState != c.contextState {
			t.Errorf("%s context_state = %q, want %q (%s)", c.agent, row.ContextState, c.contextState, c.why)
		}
		if row.Recovery != c.recovery {
			t.Errorf("%s recovery = %q, want %q (%s)", c.agent, row.Recovery, c.recovery, c.why)
		}
	}

	// The load-bearing honesty rule is unchanged: a running agent with no formula is
	// still "idle", and a halted breaker does NOT become a new status enum value.
	if got := rows["haltedworker"].Status; got != "idle" {
		t.Errorf("haltedworker status = %q, want %q — deriveAgentStatus must not learn about the breaker", got, "idle")
	}
}

// TestAgentsList_DegradesOnUnreadableRecoveryState pins Gotcha 14: occupancy and
// breaker trouble degrade the affected ROW, never the whole verb. `af agents list`
// is a cheap machine read whose contract is an array on success and an error
// envelope only for cwd/root/agents.json/store failures.
func TestAgentsList_DegradesOnUnreadableRecoveryState(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{"worker":{"type":"autonomous","description":"d","formula":"minimalworker"}}}`)
	installMemStore(t)
	installFakeTmuxPresent(t, session.SessionName("worker"))

	// An undecodable breaker: loadRecoveryState fails CLOSED, so the honest surface
	// is "halted" — and the verb must still emit an array.
	breakerDir := filepath.Join(realDir, ".runtime", "recovery")
	if err := os.MkdirAll(breakerDir, 0o755); err != nil {
		t.Fatalf("mkdir breaker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(breakerDir, "worker.json"), []byte("{not json at all"), 0o644); err != nil {
		t.Fatalf("write corrupt breaker: %v", err)
	}
	// A statusline sessions path that is a FILE, not a directory: os.ReadDir fails
	// with a non-IsNotExist error, which is the reader's error return.
	sessionsDir := config.StatuslineSessionsDir(realDir)
	if err := os.MkdirAll(filepath.Dir(sessionsDir), 0o755); err != nil {
		t.Fatalf("mkdir statusline dir: %v", err)
	}
	if err := os.WriteFile(sessionsDir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write sessions-as-file: %v", err)
	}

	out := invokeAgentsList(t)
	if strings.Contains(out, `"state":"error"`) {
		t.Fatalf("occupancy/breaker trouble must not tip the verb into the error envelope: %q", out)
	}
	rows := decodeAgentRecoveryRows(t, out)
	row, ok := rows["worker"]
	if !ok {
		t.Fatalf("missing worker row: %q", out)
	}
	if row.ContextPct != -1 {
		t.Errorf("context_pct = %d, want -1 — an unreadable channel is unknown, not empty", row.ContextPct)
	}
	if row.ContextState == "fresh" {
		t.Errorf("context_state = %q, want a non-healthy literal", row.ContextState)
	}
	if row.Recovery != "halted" {
		t.Errorf("recovery = %q, want %q — loadRecoveryState fails closed on undecodable bytes", row.Recovery, "halted")
	}
}

// TestAgentsList_ForeignRootExcludedFromOccupancySweep pins Gotcha 13: excluding
// foreign-root sessions is the CALLER's job (the ADR-004 reader cannot observe tmux
// state), and this is the surface that computes ForeignRoot per row. A local
// snapshot left behind by a session that now belongs to another factory must not be
// reported as this factory's occupancy.
func TestAgentsList_ForeignRootExcludedFromOccupancySweep(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{"worker":{"type":"autonomous","description":"d","formula":"minimalworker"}}}`)
	installMemStore(t)
	fake := installFakeTmuxPresent(t, session.SessionName("worker"))
	fake.env[session.SessionName("worker")] = map[string]string{"AF_ROOT": t.TempDir()}

	now := time.Now()
	plantSnapshot(t, realDir, "worker", "sess-a", 92, now.Add(-10*time.Second), now, testRecoveryConfig())

	rows := decodeAgentRecoveryRows(t, invokeAgentsList(t))
	row := rows["worker"]
	if !row.ForeignRoot {
		t.Fatalf("fixture is wrong: foreign_root = false, want true")
	}
	if row.ContextPct != -1 || row.ContextState != "none" {
		t.Errorf("foreign-root row = (%d, %q), want (-1, %q) — a foreign session's snapshot is not this factory's occupancy",
			row.ContextPct, row.ContextState, "none")
	}
}
