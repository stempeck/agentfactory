package statusline

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRawSnapshot(t *testing.T, dir string, snap sessionSnapshot) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, snap.SessionID+".json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestDailyStore_SumPruneMidnightCorrupt(t *testing.T) {
	aug1 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	aug2 := time.Date(2026, 8, 2, 0, 5, 0, 0, time.UTC)

	newSessionPayload := func(cost float64, in, out int64) Payload {
		var p Payload
		p.SessionID = "s1"
		p.Cost.TotalCostUSD = cost
		p.ContextWindow.TotalInputTokens = in
		p.ContextWindow.TotalOutputTokens = out
		return p
	}

	t.Run("MidnightContributesOnlyPostMidnightDelta", func(t *testing.T) {
		dir := t.TempDir()

		// Aug 1: session s1 spends $5.00 (cumulative), 6000 tokens.
		if err := WriteSnapshot(dir, newSessionPayload(5.00, 5000, 1000), "manager", aug1); err != nil {
			t.Fatalf("aug1 write: %v", err)
		}
		if d := SumDaily(dir, aug1); !approx(d.CostUSD, 5.00) {
			t.Fatalf("aug1 daily should be 5.00, got %v", d.CostUSD)
		}

		// Aug 2 00:05: same session, cumulative now $5.02 — date rollover.
		if err := WriteSnapshot(dir, newSessionPayload(5.02, 5030, 1010), "manager", aug2); err != nil {
			t.Fatalf("aug2 write: %v", err)
		}

		d := SumDaily(dir, aug2)
		// THE C1 ASSERTION: only the post-midnight delta counts, NOT the lifetime $5.02.
		if !approx(d.CostUSD, 0.02) {
			t.Errorf("post-midnight daily cost = %v, want 0.02 (a session must not report lifetime spend as today)", d.CostUSD)
		}
		// Tokens are no longer summed: the payload's token totals are context OCCUPANCY, not
		// cumulative spend, so a daily token delta is not a meaningful quantity and goes
		// negative after compaction (PR #595 T1/F1). The compaction case is pinned in
		// fable_increment_pr595_test.go; this test now asserts only the reliable COST delta.
		if d.Sessions != 1 {
			t.Errorf("sessions = %d, want 1", d.Sessions)
		}
	})

	t.Run("ThrottleAndIdempotence", func(t *testing.T) {
		dir := t.TempDir()
		if err := WriteSnapshot(dir, newSessionPayload(5.00, 5000, 1000), "manager", aug1); err != nil {
			t.Fatal(err)
		}
		if err := WriteSnapshot(dir, newSessionPayload(5.02, 5030, 1010), "manager", aug2); err != nil {
			t.Fatal(err)
		}
		// A write 5s later with a wild value is THROTTLED (≥10s rule) ⇒ ignored.
		if err := WriteSnapshot(dir, newSessionPayload(999.0, 9, 9), "manager", aug2.Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}
		if d := SumDaily(dir, aug2); !approx(d.CostUSD, 0.02) {
			t.Errorf("throttled write leaked through: daily = %v, want 0.02", d.CostUSD)
		}
		// A write 20s later with the SAME cumulative is idempotent (no double count).
		if err := WriteSnapshot(dir, newSessionPayload(5.02, 5030, 1010), "manager", aug2.Add(20*time.Second)); err != nil {
			t.Fatal(err)
		}
		if d := SumDaily(dir, aug2.Add(20*time.Second)); !approx(d.CostUSD, 0.02) {
			t.Errorf("idempotent re-write changed the total: daily = %v, want 0.02", d.CostUSD)
		}
	})

	t.Run("CorruptAndOversizedFilesSkipped", func(t *testing.T) {
		dir := t.TempDir()
		if err := WriteSnapshot(dir, newSessionPayload(5.00, 5000, 1000), "manager", aug1); err != nil {
			t.Fatal(err)
		}
		if err := WriteSnapshot(dir, newSessionPayload(5.02, 5030, 1010), "manager", aug2); err != nil {
			t.Fatal(err)
		}
		// A corrupt file and an oversized file must not blank the whole element.
		if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "huge.json"), []byte("{"+strings.Repeat("\"x\":1,", 2000)+"\"y\":1}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if d := SumDaily(dir, aug2); !approx(d.CostUSD, 0.02) {
			t.Errorf("corrupt/oversized file poisoned the sum: daily = %v, want 0.02", d.CostUSD)
		}
	})

	t.Run("ForeignStaleRolledOverNotDeleted", func(t *testing.T) {
		dir := t.TempDir()
		// A foreign session's yesterday file, updated within 48h.
		writeRawSnapshot(t, dir, sessionSnapshot{
			SessionID: "s2", Date: "2026-08-01",
			CostUSD: 3.00, InputTokens: 3000, OutputTokens: 0,
			UpdatedAt: aug1.Add(11 * time.Hour).UTC().Format(time.RFC3339), // Aug 1 23:00
		})
		Prune(dir, aug2)

		path := filepath.Join(dir, "s2.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("foreign stale file was deleted instead of rolled over: %v", err)
		}
		var got sessionSnapshot
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.Date != "2026-08-02" {
			t.Errorf("rolled-over date = %q, want today 2026-08-02", got.Date)
		}
		if !approx(got.BaselineCostUSD, got.CostUSD) || !approx(got.BaselineCostUSD, 3.00) {
			t.Errorf("rollover must set baseline := cumulative (3.00); got baseline=%v cumulative=%v", got.BaselineCostUSD, got.CostUSD)
		}
		// Contributes 0 to today until it renders again.
		if d := SumDaily(dir, aug2); !approx(d.CostUSD, 0.00) {
			t.Errorf("rolled-over foreign session should contribute 0, daily = %v", d.CostUSD)
		}
	})

	t.Run("Horizon48hHardDelete", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, sessionSnapshot{
			SessionID: "s3", Date: "2026-07-30",
			CostUSD: 9.99, InputTokens: 1, OutputTokens: 1,
			UpdatedAt: "2026-07-30T00:00:00Z", // >48h before Aug 2 00:05
		})
		Prune(dir, aug2)
		if _, err := os.Stat(filepath.Join(dir, "s3.json")); !os.IsNotExist(err) {
			t.Errorf("expected >48h file to be hard-deleted, stat err = %v", err)
		}
	})

	t.Run("EnoentTolerant", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-dir")
		if d := SumDaily(missing, aug2); d.Sessions != 0 || d.CostUSD != 0 {
			t.Errorf("SumDaily on missing dir should be zero, got %+v", d)
		}
		Prune(missing, aug2) // must not panic
	})
}

// decodeRawSnapshot decodes a snapshot file into a generic map so a key that is ABSENT is
// distinguishable from a key that decoded to its zero value. Decoding into sessionSnapshot
// cannot see the difference, which is exactly how a dropped schema-v2 field would ship green
// (issue #596 Gotcha 7; the same defect class already shipped once in commit 2e27bf98).
func decodeRawSnapshot(t *testing.T, dir, sessionID string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, sessionID+".json"))
	if err != nil {
		t.Fatalf("reading snapshot %s: %v", sessionID, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decoding snapshot %s: %v", sessionID, err)
	}
	return m
}

// mustPresent fails unless key is present AND non-null. Presence is asserted separately from
// value because the whole K2 contract rests on "absent ⇒ malformed, never a zero-value 0%".
func mustPresent(t *testing.T, m map[string]any, key string) any {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("snapshot is missing the %q key entirely", key)
	}
	if v == nil {
		t.Fatalf("snapshot has %q = null, want a value", key)
	}
	return v
}

// TestDailyStore_WritesSchemaV2Fields pins K1: the renderer persists occupancy alongside cost
// (design-doc.md:310, IMPLREADME Required Change File 2). It decodes into map[string]any so a
// writer that never emits a field fails here rather than passing on a zero value.
func TestDailyStore_WritesSchemaV2Fields(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 4, 9, 30, 0, 0, time.UTC)

	pct := 35.0
	var p Payload
	p.SessionID = "sv2"
	p.Cost.TotalCostUSD = 1.25
	p.ContextWindow.TotalInputTokens = 340000
	p.ContextWindow.TotalOutputTokens = 12000
	p.ContextWindow.ContextWindowSize = 1000000
	p.ContextWindow.UsedPercentage = &pct

	if err := WriteSnapshot(dir, p, "manager", now); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := decodeRawSnapshot(t, dir, "sv2")

	if got := mustPresent(t, m, "schema"); got != float64(schemaVersion) {
		t.Errorf("schema = %v, want %d", got, schemaVersion)
	}
	if got := mustPresent(t, m, "agent"); got != "manager" {
		t.Errorf("agent = %v, want %q (the AF_ROLE the cmd layer resolved)", got, "manager")
	}
	writtenAt, _ := mustPresent(t, m, "written_at").(string)
	if writtenAt != now.UTC().Format(time.RFC3339) {
		t.Errorf("written_at = %q, want the render clock %q", writtenAt, now.UTC().Format(time.RFC3339))
	}
	if _, err := time.Parse(time.RFC3339, writtenAt); err != nil {
		t.Errorf("written_at %q does not parse as RFC3339: %v", writtenAt, err)
	}
	if got, _ := mustPresent(t, m, "context_used_pct").(float64); !approx(got, 35.0) {
		t.Errorf("context_used_pct = %v, want 35.0", got)
	}
	// The payload's in-context input and output totals summed: occupancy, not spend
	// (daily.go's DailyTotals comment / PR #595 T1/F1).
	if got, _ := mustPresent(t, m, "context_tokens_used").(float64); !approx(got, 352000) {
		t.Errorf("context_tokens_used = %v, want 352000 (340000 in + 12000 out)", got)
	}
	if got, _ := mustPresent(t, m, "context_tokens_total").(float64); !approx(got, 1000000) {
		t.Errorf("context_tokens_total = %v, want 1000000", got)
	}

	// The v1 cost contract is untouched — this phase is additive.
	if got, _ := m["cost_usd"].(float64); !approx(got, 1.25) {
		t.Errorf("cost_usd = %v, want 1.25 (v2 must not disturb the cost fields)", got)
	}
	if got, _ := m["updated_at"].(string); got != now.UTC().Format(time.RFC3339) {
		t.Errorf("updated_at = %q, want %q", got, now.UTC().Format(time.RFC3339))
	}
}

// TestDailyStore_AbsentUsedPercentageIsNotWrittenAsZero is the writer half of AC-5. A non-pointer
// context_used_pct would persist 0 for a payload that reported NOTHING, and the reader would then
// see a present, valid, 0% datum — a wedged agent looking maximally healthy. No amount of
// reader-side rigor can recover from that, because the lie is already on disk.
func TestDailyStore_AbsentUsedPercentageIsNotWrittenAsZero(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 4, 9, 30, 0, 0, time.UTC)

	var p Payload
	p.SessionID = "nopct"
	p.Cost.TotalCostUSD = 0.5
	p.ContextWindow.TotalInputTokens = 100
	p.ContextWindow.TotalOutputTokens = 10
	p.ContextWindow.ContextWindowSize = 200000
	p.ContextWindow.UsedPercentage = nil // the host reported no occupancy (payload.go:17-18)

	if err := WriteSnapshot(dir, p, "manager", now); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := decodeRawSnapshot(t, dir, "nopct")

	if v, ok := m["context_used_pct"]; ok && v != nil {
		t.Errorf("context_used_pct = %v; a payload with null used_percentage must never be persisted as a value (0%% would read as healthy)", v)
	}
	// All-or-nothing: a half-populated occupancy block is not a datum the trigger may act on.
	if v, ok := m["context_tokens_total"]; ok && v != nil {
		t.Errorf("context_tokens_total = %v; occupancy fields are written as a set or not at all", v)
	}
	// Cost accounting is independent and must still be recorded.
	if got, _ := m["cost_usd"].(float64); !approx(got, 0.5) {
		t.Errorf("cost_usd = %v, want 0.5 (absent occupancy must not suppress the cost snapshot)", got)
	}
}

// TestDailyStore_PruneRolloverPreservesV2Fields is the Gotcha-7 guard. Prune runs on EVERY render
// and rebuilds each stale-dated file from a field-by-field literal (daily.go), so any v2 field the
// literal omits is silently zeroed at the first local midnight after deploy — manufacturing the
// "absent field" the reader must call malformed, on a file a HEALTHY agent owns.
//
// written_at is deliberately NOT advanced: a rollover is a bookkeeping rewrite, not a new
// observation. Advancing it would let Prune — driven by ANY session's render — refresh a dead
// agent's channel to fresh, which is precisely the AC-5 failure this phase exists to prevent.
func TestDailyStore_PruneRolloverPreservesV2Fields(t *testing.T) {
	dir := t.TempDir()
	aug1 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	aug2 := time.Date(2026, 8, 2, 0, 5, 0, 0, time.UTC)
	stamp := aug1.Add(11 * time.Hour).UTC().Format(time.RFC3339)

	pct := 61.5
	used := int64(123000)
	total := int64(200000)
	writeRawSnapshot(t, dir, sessionSnapshot{
		Schema: schemaVersion, SessionID: "roll", Date: "2026-08-01",
		Agent: "manager", WrittenAt: stamp,
		ContextUsedPct: &pct, ContextTokensUsed: &used, ContextTokensTotal: &total,
		CostUSD: 3.00, InputTokens: 3000, OutputTokens: 0,
		UpdatedAt: stamp,
	})

	Prune(dir, aug2)
	m := decodeRawSnapshot(t, dir, "roll")

	if got := mustPresent(t, m, "schema"); got != float64(schemaVersion) {
		t.Errorf("rollover dropped/changed schema: %v", got)
	}
	if got := mustPresent(t, m, "agent"); got != "manager" {
		t.Errorf("rollover dropped agent: %v", got)
	}
	if got := mustPresent(t, m, "written_at"); got != stamp {
		t.Errorf("written_at = %v, want the ORIGINAL %q — a rollover is not a new observation and must never refresh the channel", got, stamp)
	}
	if got, _ := mustPresent(t, m, "context_used_pct").(float64); !approx(got, 61.5) {
		t.Errorf("rollover dropped context_used_pct: %v", got)
	}
	if got, _ := mustPresent(t, m, "context_tokens_used").(float64); !approx(got, 123000) {
		t.Errorf("rollover dropped context_tokens_used: %v", got)
	}
	if got, _ := mustPresent(t, m, "context_tokens_total").(float64); !approx(got, 200000) {
		t.Errorf("rollover dropped context_tokens_total: %v", got)
	}

	// The pre-existing C1 invariant must still hold alongside the new fields.
	if got, _ := m["date"].(string); got != "2026-08-02" {
		t.Errorf("rolled-over date = %q, want 2026-08-02", got)
	}
	base, _ := m["baseline_cost_usd"].(float64)
	cum, _ := m["cost_usd"].(float64)
	if !approx(base, cum) || !approx(base, 3.00) {
		t.Errorf("rollover must set baseline := cumulative (3.00); got baseline=%v cumulative=%v", base, cum)
	}
	if got, _ := m["updated_at"].(string); got != stamp {
		t.Errorf("updated_at = %q, want %q preserved for the 48h reaper", got, stamp)
	}
}

// TestDailyStore_ThrottleAlsoSkipsOccupancyUpdate characterizes Gotcha 8: the 10s throttle skips
// the WHOLE write, so occupancy is never fresher than throttleInterval. This is the first term of
// the design's latency formula, max(throttle, refresh_interval) (design-doc.md:238). Pinned so a
// later "optimization" that exempts occupancy from the throttle is a visible decision, not a drift.
func TestDailyStore_ThrottleAlsoSkipsOccupancyUpdate(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 4, 9, 30, 0, 0, time.UTC)

	mk := func(pct float64) Payload {
		var p Payload
		p.SessionID = "thr"
		p.ContextWindow.ContextWindowSize = 200000
		p.ContextWindow.TotalInputTokens = 1000
		p.ContextWindow.UsedPercentage = &pct
		return p
	}

	if err := WriteSnapshot(dir, mk(10), "manager", now); err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshot(dir, mk(90), "manager", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := decodeRawSnapshot(t, dir, "thr")["context_used_pct"].(float64); !approx(got, 10) {
		t.Errorf("context_used_pct = %v, want 10 — a sub-throttle write must not land", got)
	}
	if err := WriteSnapshot(dir, mk(90), "manager", now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := decodeRawSnapshot(t, dir, "thr")["context_used_pct"].(float64); !approx(got, 90) {
		t.Errorf("context_used_pct = %v, want 90 once the throttle window has passed", got)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("init\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")
}

func TestDailyStore_CleanWorktree(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)

	// The snapshot store lives OUTSIDE the agent's worktree (under the factory root).
	store := t.TempDir()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	var p Payload
	p.SessionID = "clean-session"
	p.Cost.TotalCostUSD = 1.23
	p.ContextWindow.TotalInputTokens = 100
	p.ContextWindow.TotalOutputTokens = 20

	// Drive the full store + branch pipeline against the repo as cwd.
	if b := ReadBranch(repo); b == "" {
		t.Errorf("ReadBranch returned empty for a real repo")
	}
	if err := WriteSnapshot(store, p, "manager", now); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	_ = SumDaily(store, now)
	Prune(store, now)

	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("renderer dirtied the worktree; git status --porcelain:\n%s", out)
	}
}
