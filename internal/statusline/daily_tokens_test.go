package statusline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests live beside daily_test.go rather than inside it so the shipped midnight COST pins stay
// byte-for-byte unmodified (design-doc.md:598): `git diff daily_test.go` must be empty.

// fixedCursor is a TokenAccumulator that reports a known counter, standing in for the cmd-side
// transcript reader. The library never opens a transcript itself.
func fixedCursor(cum, offset int64) TokenAccumulator {
	return func(TranscriptCursor) TranscriptCursor {
		return TranscriptCursor{CumTokens: cum, RederiveTokens: cum, Offset: offset}
	}
}

// writeRawSnapshotJSON writes a snapshot as literal bytes. Marshalling the current struct cannot author a
// LEGACY-era file — once the new fields exist, every struct-marshalled file carries them as zeros,
// which would make the mixed-era test tautological.
func writeRawSnapshotJSON(t *testing.T, dir, sid, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", sid, err)
	}
}

func mustRead(t *testing.T, dir, sid string) *sessionSnapshot {
	t.Helper()
	s, err := readSnapshot(filepath.Join(dir, sid+".json"))
	if err != nil {
		t.Fatalf("readSnapshot %s: %v", sid, err)
	}
	return s
}

// AC-2 clauses (iii)/(iv). Mirrors the cost pin's C1 semantics: on the session's own date rollover
// the baseline becomes the previous cumulative, so the new day's contribution is the delta — never
// the lifetime total, and never negative.
func TestDailyTokens_MidnightRollover_NonNegative(t *testing.T) {
	dir := t.TempDir()
	aug1 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	aug2 := time.Date(2026, 8, 2, 0, 5, 0, 0, time.UTC)

	var p Payload
	p.SessionID = "sess1"
	p.Cost.TotalCostUSD = 5.00

	if err := WriteSnapshotWith(dir, p, "manager", aug1, fixedCursor(1000, 4096)); err != nil {
		t.Fatalf("WriteSnapshotWith: %v", err)
	}
	if d := SumDaily(dir, aug1); d.Tokens != 1000 {
		t.Fatalf("day-1 Tokens = %d, want 1000", d.Tokens)
	}

	p.Cost.TotalCostUSD = 5.02
	if err := WriteSnapshotWith(dir, p, "manager", aug2, fixedCursor(1050, 8192)); err != nil {
		t.Fatalf("WriteSnapshotWith: %v", err)
	}
	d := SumDaily(dir, aug2)
	if d.Tokens != 50 {
		t.Fatalf("day-2 Tokens = %d, want exactly 50 (1050 cumulative − 1000 baseline)", d.Tokens)
	}
	if !approx(d.CostUSD, 0.02) {
		t.Fatalf("day-2 CostUSD = %v, want 0.02 — cost behaviour must be byte-identical to today", d.CostUSD)
	}
	if d.Sessions != 1 {
		t.Fatalf("Sessions = %d, want 1", d.Sessions)
	}

	snap := mustRead(t, dir, "sess1")
	if snap.BaselineCumTokens != 1000 {
		t.Fatalf("baseline_cum_tokens = %d, want 1000 (previous cumulative)", snap.BaselineCumTokens)
	}
	if snap.FirstSeen == "" {
		t.Fatal("first_seen empty: it must be set on the first write and carried across rollover")
	}
	if snap.TranscriptOffset != 8192 {
		t.Fatalf("transcript_offset = %d, want 8192", snap.TranscriptOffset)
	}

	t.Run("WriterNeverSubtracts", func(t *testing.T) {
		// A concurrent render can clobber an advanced counter (fsutil is last-writer-wins). The
		// single writer clamps, so the stored counter can only ever move forward.
		if err := WriteSnapshotWith(dir, p, "manager", aug2.Add(time.Minute), fixedCursor(900, 8192)); err != nil {
			t.Fatalf("WriteSnapshotWith: %v", err)
		}
		if got := mustRead(t, dir, "sess1").CumTokens; got != 1050 {
			t.Fatalf("cum_tokens = %d after a lower reading, want 1050 held", got)
		}
	})

	t.Run("SumClampsNegativeContribution", func(t *testing.T) {
		// Defence in depth: even if a file somehow holds cum < baseline, the day's contribution is
		// zero, never the negative figure PR #595 shipped (D $0.40 · -550000 tok).
		clamped := t.TempDir()
		writeRawSnapshotJSON(t, clamped, "sessN", `{"session_id":"sessN","date":"2026-08-02",`+
			`"baseline_cost_usd":1.0,"cost_usd":1.5,`+
			`"baseline_cum_tokens":1000,"cum_tokens":900,"updated_at":"2026-08-02T00:00:00Z"}`)
		d := SumDaily(clamped, aug2)
		if d.Tokens != 0 {
			t.Fatalf("Tokens = %d, want 0 — a negative contribution must clamp, not propagate", d.Tokens)
		}
		if !approx(d.CostUSD, 0.5) {
			t.Fatalf("CostUSD = %v, want 0.5 — the cost path takes no clamp", d.CostUSD)
		}
	})
}

// Gap 7 / data.md B2.1. During the 48h upgrade window a sessions directory holds files written by
// BOTH binaries. The legacy occupancy fields must be structurally invisible to the token sum: an
// exact equality is required, because ">= 0" passes even if occupancy were being summed as spend.
func TestSumDaily_MixedEraSnapshots_NonNegative(t *testing.T) {
	dir := t.TempDir()
	today := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)

	// LEGACY era: raw bytes, only the nine keys the pre-#600 binary wrote. The occupancy values are
	// deliberately huge, so any accidental reinterpretation shows up as an enormous token figure.
	writeRawSnapshotJSON(t, dir, "legacy", `{"session_id":"legacy","date":"2026-08-02",`+
		`"baseline_cost_usd":1.00,"baseline_input_tokens":340000,"baseline_output_tokens":12000,`+
		`"cost_usd":1.25,"input_tokens":890000,"output_tokens":40000,`+
		`"updated_at":"2026-08-02T08:00:00Z"}`)

	// NEW era: carries the additive cumulative-token fields.
	writeRawSnapshotJSON(t, dir, "modern", `{"session_id":"modern","date":"2026-08-02",`+
		`"baseline_cost_usd":2.00,"cost_usd":2.50,`+
		`"baseline_cum_tokens":7000,"cum_tokens":9500,"transcript_offset":65536,`+
		`"first_seen":"2026-08-02T07:00:00Z","updated_at":"2026-08-02T08:30:00Z"}`)

	d := SumDaily(dir, today)
	if d.Tokens != 2500 {
		t.Fatalf("Tokens = %d, want exactly 2500 (the new-era file's 9500−7000); the legacy file must contribute exactly 0", d.Tokens)
	}
	if d.Sessions != 2 {
		t.Fatalf("Sessions = %d, want 2", d.Sessions)
	}
	if !approx(d.CostUSD, 0.75) {
		t.Fatalf("CostUSD = %v, want 0.75 (0.25 + 0.50) — legacy cost keeps summing exactly as today", d.CostUSD)
	}
}

// Prune's rollover is the file's only field-enumerating struct literal, so a forgotten field is
// silent. Dropping transcript_offset would force a full transcript re-derive from byte 0 on every
// post-midnight render; dropping cum_tokens would zero the counter outright.
func TestPrune_RolloverCarriesTokenFields(t *testing.T) {
	dir := t.TempDir()
	today := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)

	writeRawSnapshotJSON(t, dir, "stale", `{"session_id":"stale","date":"2026-08-01",`+
		`"baseline_cost_usd":1.00,"cost_usd":3.00,`+
		`"baseline_cum_tokens":500,"cum_tokens":4200,"transcript_offset":131072,`+
		`"first_seen":"2026-08-01T06:00:00Z","updated_at":"2026-08-01T23:50:00Z"}`)

	Prune(dir, today)

	s := mustRead(t, dir, "stale")
	if s.Date != "2026-08-02" {
		t.Fatalf("date = %q, want the rolled-over day", s.Date)
	}
	if s.CumTokens != 4200 {
		t.Fatalf("cum_tokens = %d, want 4200 preserved", s.CumTokens)
	}
	if s.BaselineCumTokens != 4200 {
		t.Fatalf("baseline_cum_tokens = %d, want 4200 (baseline := cumulative), so the session contributes 0 until it renders again", s.BaselineCumTokens)
	}
	if s.TranscriptOffset != 131072 {
		t.Fatalf("transcript_offset = %d, want 131072 preserved — dropping it re-derives the whole transcript every render", s.TranscriptOffset)
	}
	if s.FirstSeen != "2026-08-01T06:00:00Z" {
		t.Fatalf("first_seen = %q, want the original preserved", s.FirstSeen)
	}
	if d := SumDaily(dir, today); d.Tokens != 0 {
		t.Fatalf("Tokens = %d immediately after rollover, want 0", d.Tokens)
	}
}

// S3.4: transcript work is bound to the throttled write window, so a burst of renders cannot
// multiply transcript I/O. The accumulator must not even be invoked on a throttled render.
func TestWriteSnapshotWith_AccumulatorGatedToThrottleWindow(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)

	var p Payload
	p.SessionID = "sessT"

	calls := 0
	counting := func(prev TranscriptCursor) TranscriptCursor {
		calls++
		return TranscriptCursor{CumTokens: prev.CumTokens + 10, RederiveTokens: prev.RederiveTokens + 10}
	}

	if err := WriteSnapshotWith(dir, p, "manager", base, counting); err != nil {
		t.Fatalf("WriteSnapshotWith: %v", err)
	}
	if calls != 1 {
		t.Fatalf("accumulator called %d times on the first write, want 1", calls)
	}

	// Inside the 10s throttle window: the write is skipped, so no transcript work may happen.
	if err := WriteSnapshotWith(dir, p, "manager", base.Add(3*time.Second), counting); err != nil {
		t.Fatalf("WriteSnapshotWith: %v", err)
	}
	if calls != 1 {
		t.Fatalf("accumulator called %d times, want 1: transcript work must not run on a throttled render", calls)
	}

	// Past the window: admitted again.
	if err := WriteSnapshotWith(dir, p, "manager", base.Add(11*time.Second), counting); err != nil {
		t.Fatalf("WriteSnapshotWith: %v", err)
	}
	if calls != 2 {
		t.Fatalf("accumulator called %d times, want 2", calls)
	}
	if got := mustRead(t, dir, "sessT").CumTokens; got != 20 {
		t.Fatalf("cum_tokens = %d, want 20", got)
	}
}

// WriteSnapshot (the accumulator-less wrapper) must stay safe for every non-render caller: it
// records cost normally and, with no accumulator supplied, contributes zero tokens rather than
// fabricating or disturbing a counter.
func TestWriteSnapshot_NilAccumulatorContributesNoTokens(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)

	var p Payload
	p.SessionID = "sessLegacy"
	p.Cost.TotalCostUSD = 1.5

	if err := WriteSnapshot(dir, p, "manager", now); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	d := SumDaily(dir, now)
	if !approx(d.CostUSD, 1.5) {
		t.Fatalf("CostUSD = %v, want 1.5", d.CostUSD)
	}
	if d.Tokens != 0 {
		t.Fatalf("Tokens = %d, want 0 when no accumulator is supplied", d.Tokens)
	}
}
