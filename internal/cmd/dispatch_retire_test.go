//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDispatchAdmit_ReservationSurvivesBeyondFiveMinutes is THREAD-1's done-when (a)+(c): an
// unretired reservation marker must OUTLIVE the 5-minute mark, so a still-running first child keeps
// its slot and a second same-launcher dispatch is refused at t+6m (admit-1/refuse-2). Today the
// old 5-minute TTL swept the marker on read, the ledger drops to 0, and the second
// launch is wrongly admitted as if it were the first — the [BAD-1] incident reopening. After the TTL
// is demoted to a 2h crash-only backstop the marker survives, the ledger holds 1, the reservation
// doubles, and the second launch is denied.
func TestDispatchAdmit_ReservationSurvivesBeyondFiveMinutes(t *testing.T) {
	now := time.Now()
	fx := newLifecycleFixture(t)
	armTokenomics(t, fx.root, 10, 1)
	writeDeclaredBackendModels(t, fx.root)
	// A launcher at half the ceiling: ledger 0 admits (the first child), ledger 1 refuses (the
	// second) — the admit-1/refuse-2 band TestReservationTokens_AdmitOneRefuseTwo proves for any S.
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 45, 1000, now.Add(-10*time.Second), now)

	// The first admitted child's reservation marker, aged to six minutes old — past the 5-minute TTL,
	// inside the 2h backstop. Aged via os.Chtimes (dispatch_admit_test.go:83-84) so the mtime, which
	// countLiveReservations trusts, is exactly six minutes behind the second launch's clock.
	dir := reservationDir(fx.workDir, "http://127.0.0.1:1234")
	writeReservationMarker(dir, now)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one reservation marker after writing one, got %d (err=%v)", len(entries), err)
	}
	aged := filepath.Join(dir, entries[0].Name())
	sixMinAgo := now.Add(-6 * time.Minute)
	if err := os.Chtimes(aged, sixMinAgo, sixMinAgo); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
		t.Fatalf("runDispatchAdmitCore: %v", err)
	}
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("the 6-minute-old reservation was swept by the 5-minute TTL and the second launch was admitted — "+
			"the [BAD-1] incident reopened; an unretired marker must survive to a 2h crash-only backstop:\n%s", out.String())
	}
}

// TestRetireOneReservation_PrefersSequentialSlot pins F5(b) (r3906601... retire prefer sequential.slot)
// as #673 re-expresses it. Two claims live here and only one of them changed.
//
// UNCHANGED (F5(b)): when a cap child's sequential.slot sits beside a LEAKED, OLDER pid+nanotime
// arithmetic marker, retire must act on the SLOT, not on the older marker. Pure mtime FIFO would take
// the leaked marker and leave the slot to rot to its 2h TTL (scenario iii).
//
// INVERTED (#673): what "acting on the slot" means. Retire used to delete it, on the premise that
// SubagentStop meant the child had finished. It does not — measured firing ~7 min into a ~2 h child —
// so retire now leaves the slot RETAINED and writes a sequential.stop proposal beside it, joined to the
// slot's own content so it can only ever release the claim it was made for. RED at head, where the slot
// is gone after this call.
func TestRetireOneReservation_PrefersSequentialSlot(t *testing.T) {
	workDir := t.TempDir()
	dir := reservationDir(workDir, "http://127.0.0.1:1234")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	slot := filepath.Join(dir, sequentialSlotName)
	if err := os.WriteFile(slot, []byte("held\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := os.Stat(slot)
	if err != nil {
		t.Fatal(err)
	}
	// A leaked arithmetic marker OLDER than the slot: pure mtime FIFO would retire THIS first and leave
	// the slot behind.
	marker := filepath.Join(dir, "12345-000000")
	if err := os.WriteFile(marker, []byte("older\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	older := now.Add(-time.Hour)
	if err := os.Chtimes(marker, older, older); err != nil {
		t.Fatal(err)
	}

	retireOneReservation(dispatchRetirePayload{Cwd: workDir, SessionID: "sess-a", TranscriptPath: "/tmp/sess-a.jsonl"}, "", now)

	// The slot is RETAINED: a stop event is a proposal that the child may have finished, not proof.
	after, err := os.Stat(slot)
	if err != nil {
		t.Fatalf("retire REMOVED sequential.slot (stat err=%v); a bare stop event cannot free the cap "+
			"semaphore — that is #673, where a slot freed 7 minutes into a 2-hour child let two run at once", err)
	}
	if !after.ModTime().Equal(held.ModTime()) {
		t.Errorf("retire rewrote sequential.slot (mtime %v -> %v); the slot's mtime is the 2h backstop's "+
			"authority and refreshing it on every stop would push that backstop out indefinitely",
			held.ModTime(), after.ModTime())
	}

	// ...and a proposeSlotRelease sidecar sits beside it, joined to the slot's own content.
	raw, err := os.ReadFile(filepath.Join(dir, sequentialStopName))
	if err != nil {
		t.Fatalf("retire wrote no %s beside the retained slot: %v", sequentialStopName, err)
	}
	var prop slotReleaseProposal
	if err := json.Unmarshal(raw, &prop); err != nil {
		t.Fatalf("the %s proposal is not decodable JSON: %v (%s)", sequentialStopName, err, raw)
	}
	if prop.V != sequentialStopVersion {
		t.Errorf("proposal v = %d, want %d stamped by the writer", prop.V, sequentialStopVersion)
	}
	if prop.SlotStamp != "held\n" {
		t.Errorf("proposal slot_stamp = %q, want the slot's own content %q; without that join a stale "+
			"proposal from a previous child could release the current child's slot", prop.SlotStamp, "held\n")
	}
	if prop.SessionID != "sess-a" || prop.TranscriptPath != "/tmp/sess-a.jsonl" {
		t.Errorf("the proposal dropped its evidence hints: session_id=%q transcript_path=%q; without them "+
			"the ladder degrades to proposal-age dwell for every child", prop.SessionID, prop.TranscriptPath)
	}

	// F5(b), unchanged: the older arithmetic marker must NOT have been taken instead.
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("retire removed the older arithmetic marker instead of proposing on the slot (stat err=%v); "+
			"F5(b) must prefer sequential.slot over the oldest-by-mtime FIFO", err)
	}
}

// TestRunDispatchRetireCmd_OversizedPayloadStillRetires pins that PAYLOAD-CAPTURE's size limit costs
// the CAPTURE and never the retirement. last_assistant_message is unbounded prose riding on the same
// stop payload, so a large one is a reachable input; bounding the decode with it makes the payload
// truncate into a parse error, and this verb answers a parse error by retiring nothing at all — no
// proposal written, and the cap slot held for the full 2h backstop because an instrument overflowed.
func TestRunDispatchRetireCmd_OversizedPayloadStillRetires(t *testing.T) {
	workDir := t.TempDir()
	dir := reservationDir(workDir, "sonnet")
	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, time.Now()) {
		t.Fatal("could not plant a held slot")
	}

	payload := map[string]any{
		"cwd":                    workDir,
		"session_id":             "sess-1",
		"transcript_path":        filepath.Join(workDir, "t.jsonl"),
		"last_assistant_message": strings.Repeat("x", 2*dispatchStopPayloadReadLimit),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	stdinFromBytes(t, raw)

	if err := runDispatchRetireCmd(nil, nil); err != nil {
		t.Fatalf("the hook returned an error; ADR-007 says it exits 0 whatever happens: %v", err)
	}

	prop, ok := readSlotProposal(dir)
	if !ok {
		t.Fatal("an oversized stop payload retired nothing: no sequential.stop was written, so the child's " +
			"slot is held until the 2h crash backstop and every launch until then is falsely refused")
	}
	if prop.SessionID != "sess-1" {
		t.Errorf("proposal carried session_id %q, want the payload's; the evidence hints were lost", prop.SessionID)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".runtime", "dispatch_stop_payload.json")); !os.IsNotExist(err) {
		t.Errorf("the oversized payload was captured anyway (%v); the limit is what stops this verb holding "+
			"an unbounded copy of a hook's stdin", err)
	}
}

// TestReadDispatchRetirePayloadFromStdin_CapturesOnlyTheDecodedValue pins that the raw copy handed to
// PAYLOAD-CAPTURE is the payload OBJECT, not everything the decoder's read-ahead happened to pull in.
// The host is free to write trailing NDJSON, and a capture that included it would fail its own Unmarshal
// and silently record nothing — the one input the instrument most needs to preserve.
func TestReadDispatchRetirePayloadFromStdin_CapturesOnlyTheDecodedValue(t *testing.T) {
	stdinFromBytes(t, []byte(`{"cwd":"/w","session_id":"s"}`+"\n"+`{"trailing":"record"}`+"\n"))

	p, raw, ok := readDispatchRetirePayloadFromStdin()
	if !ok {
		t.Fatal("trailing NDJSON rejected the whole payload; the retirement is lost over bytes it never needed")
	}
	if p.Cwd != "/w" {
		t.Errorf("cwd = %q, want /w", p.Cwd)
	}
	var round map[string]any
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Errorf("the captured bytes are not a decodable object (%v); captureDispatchStopPayload would "+
			"drop them and the instrument would record nothing", err)
	}
}

// stdinFromBytes points os.Stdin at a temp FILE rather than a pipe: the payloads here exceed a pipe's
// buffer, and the reader is the code under test, so a pipe write would deadlock before it ran.
func stdinFromBytes(t *testing.T, raw []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = saved; _ = f.Close() })
}
