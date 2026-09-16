//go:build !integration

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const reclaimClaimEnv = "AF_TEST_RECLAIM_CLAIM_DIR"

// TestReclaimHelperClaim is the child half of the cross-process contention attack.
func TestReclaimHelperClaim(t *testing.T) {
	dir := os.Getenv(reclaimClaimEnv)
	if dir == "" {
		t.Skip("helper")
	}
	if claimSubagentSlot(dir, dispatchReservationSafetyTTL, time.Now()) {
		os.Exit(0)
	}
	os.Exit(3)
}

func scratchArmReleasable(t *testing.T, dir string, stopAgo time.Duration, ageSlot time.Duration) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	slot := filepath.Join(dir, sequentialSlotName)
	if err := os.WriteFile(slot, []byte("dead-claim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ageSlot > 0 {
		at := time.Now().Add(-ageSlot)
		if err := os.Chtimes(slot, at, at); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(slotReleaseProposal{
		V:         sequentialStopVersion,
		SlotStamp: "dead-claim\n",
		StopAt:    time.Now().Add(-stopAgo).UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sequentialStopName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReclaimCrossProcessContention drives real OS processes at a releasable slot. Goroutines share
// ledgerStampSeq; separate processes do not, so this is the deployment shape.
func TestReclaimCrossProcessContention(t *testing.T) {
	root := t.TempDir()
	const racers = 8
	const rounds = 25
	for round := range rounds {
		dir := filepath.Join(root, strconv.Itoa(round))
		// Releasable via the E3 dwell rung (no hints ⇒ ladder unmeasurable) AND past the crash backstop.
		scratchArmReleasable(t, dir, subagentQuietReleaseSecs+time.Minute, 3*dispatchReservationSafetyTTL)

		var wg sync.WaitGroup
		codes := make([]int, racers)
		start := make(chan struct{})
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cmd := exec.Command(os.Args[0], "-test.run=TestReclaimHelperClaim", "-test.timeout=60s")
				cmd.Env = append(os.Environ(), reclaimClaimEnv+"="+dir)
				<-start
				err := cmd.Run()
				if err == nil {
					codes[i] = 0
					return
				}
				if ee, ok := err.(*exec.ExitError); ok {
					codes[i] = ee.ExitCode()
					return
				}
				codes[i] = -1
			}()
		}
		close(start)
		wg.Wait()

		won, weird := 0, 0
		for _, c := range codes {
			switch c {
			case 0:
				won++
			case 3:
			default:
				weird++
			}
		}
		if weird != 0 {
			t.Fatalf("round %d: %d helper processes exited abnormally: %v", round, weird, codes)
		}
		if won != 1 {
			t.Fatalf("round %d: %d of %d PROCESSES admitted to a releasable slot, want exactly 1 (codes %v)",
				round, won, racers, codes)
		}
		if names := ledgerNames(t, dir); !ledgerIsReleasedContended(names) {
			t.Fatalf("round %d: ledger after contention = %v, want just the slot (with an optional "+
				"sequential.audit breadcrumb); the consumed proposal and every corpse must be gone", round, names)
		}
	}
}

// TestReclaimCrossProcessHeldSlotAdmitsNobody is the other direction: a slot that is NOT releasable
// must refuse every one of them, and leave no litter.
func TestReclaimCrossProcessHeldSlotAdmitsNobody(t *testing.T) {
	root := t.TempDir()
	const racers = 8
	for round := range 25 {
		dir := filepath.Join(root, strconv.Itoa(round))
		// Proposal one minute short of the dwell window, slot young ⇒ neither leg fires.
		scratchArmReleasable(t, dir, subagentQuietReleaseSecs-time.Minute, 0)

		var wg sync.WaitGroup
		codes := make([]int, racers)
		start := make(chan struct{})
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cmd := exec.Command(os.Args[0], "-test.run=TestReclaimHelperClaim", "-test.timeout=60s")
				cmd.Env = append(os.Environ(), reclaimClaimEnv+"="+dir)
				<-start
				err := cmd.Run()
				if err == nil {
					codes[i] = 0
					return
				}
				if ee, ok := err.(*exec.ExitError); ok {
					codes[i] = ee.ExitCode()
					return
				}
				codes[i] = -1
			}()
		}
		close(start)
		wg.Wait()
		for _, c := range codes {
			if c != 3 {
				t.Fatalf("round %d: a launcher was admitted to a held, non-releasable slot (codes %v)", round, codes)
			}
		}
		if names := ledgerNames(t, dir); len(names) != 2 {
			t.Fatalf("round %d: refused contention left %v, want just the slot and the proposal", round, names)
		}
	}
}

// TestReclaimCrashedMidReclaimLitter plants every artifact a process death mid-reclaim can leave and
// checks the next claim still resolves.
func TestReclaimCrashedMidReclaimLitter(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, dir string)
		want  bool // admitted?
	}{
		{"guard left behind FRESH (owner died just now)", func(t *testing.T, dir string) {
			scratchArmReleasable(t, dir, subagentQuietReleaseSecs+time.Minute, 3*dispatchReservationSafetyTTL)
			if err := os.WriteFile(filepath.Join(dir, sequentialReclaimName), []byte("someone\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"guard left behind STALE (owner died long ago)", func(t *testing.T, dir string) {
			scratchArmReleasable(t, dir, subagentQuietReleaseSecs+time.Minute, 3*dispatchReservationSafetyTTL)
			g := filepath.Join(dir, sequentialReclaimName)
			if err := os.WriteFile(g, []byte("someone\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-10 * sequentialReclaimGuardTTL)
			if err := os.Chtimes(g, at, at); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"corpse left behind, slot gone (died after rename)", func(t *testing.T, dir string) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, sequentialSlotName+".reclaim-1-2-3"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"slot is a DIRECTORY", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, sequentialSlotName), 0o755); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"slot is a zero-byte file, no proposal, young", func(t *testing.T, dir string) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, sequentialSlotName), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"guard is a DIRECTORY, slot releasable", func(t *testing.T, dir string) {
			scratchArmReleasable(t, dir, subagentQuietReleaseSecs+time.Minute, 3*dispatchReservationSafetyTTL)
			if err := os.MkdirAll(filepath.Join(dir, sequentialReclaimName), 0o755); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"proposal is a dangling symlink, slot ancient", func(t *testing.T, dir string) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			slot := filepath.Join(dir, sequentialSlotName)
			if err := os.WriteFile(slot, []byte("dead\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-3 * dispatchReservationSafetyTTL)
			if err := os.Chtimes(slot, at, at); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(dir, "nope"), filepath.Join(dir, sequentialStopName)); err != nil {
				t.Fatal(err)
			}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeSubagentQuietEvidence(t) // cannot tell
			dir := t.TempDir()
			tc.plant(t, dir)
			got := claimSubagentSlot(dir, dispatchReservationSafetyTTL, time.Now())
			if got != tc.want {
				t.Errorf("claimSubagentSlot = %v, want %v; ledger now %v", got, tc.want, ledgerNames(t, dir))
			}
			t.Logf("ledger after: %v", ledgerNames(t, dir))
		})
	}
}

// TestReclaimWedgeProbe: can a hot session sidechain hold a slot past the 2h backstop forever?
func TestReclaimWedgeProbe(t *testing.T) {
	evidence := fakeSubagentQuietEvidence(t)
	dir := t.TempDir()
	now := time.Now()
	if !claimSubagentSlot(dir, dispatchReservationSafetyTTL, now) {
		t.Fatal("could not claim")
	}
	// The holder crashed; something else in the SAME session keeps writing agent-*.jsonl.
	evidence.quiet, evidence.measured = time.Second, true
	for _, d := range []time.Duration{2*time.Hour + time.Minute, 24 * time.Hour, 30 * 24 * time.Hour} {
		if claimSubagentSlot(dir, dispatchReservationSafetyTTL, now.Add(d)) {
			t.Fatalf("slot released at +%v", d)
		}
	}
	t.Log("slot still held 30 days past the crash backstop while session-level evidence stays fresh")
}
