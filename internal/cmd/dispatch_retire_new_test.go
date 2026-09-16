//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRetireOneReservation_RemovesExactlyOneOldestFIFO pins THREAD-1's done-when (b): one verified
// child-completion retires EXACTLY one reservation marker, the oldest by mtime (FIFO per launcher
// backend). It targets retireOneReservation(p dispatchRetirePayload, backendKey, now) — the retirement
// leg the OPERATOR DECISION names.
func TestRetireOneReservation_RemovesExactlyOneOldestFIFO(t *testing.T) {
	now := time.Now()
	wd := t.TempDir()
	key := "http://127.0.0.1:1234"
	dir := reservationDir(wd, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Three markers with DISTINCT mtimes so "oldest" is unambiguous. writeReservationMarker stamps
	// mtime at the wall clock, not at its now arg (dispatch_admit.go:400-406), so each file is aged
	// with os.Chtimes after writing — the same discipline dispatch_admit_test.go:83-84 uses. The
	// just-created file is found by diffing the dir listing, so the test never couples to the
	// pid-nanotime naming scheme.
	seen := map[string]bool{}
	mark := func(stamp time.Time) string {
		writeReservationMarker(dir, stamp)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var created string
		for _, e := range entries {
			if !seen[e.Name()] {
				created = e.Name()
				seen[e.Name()] = true
			}
		}
		if created == "" {
			t.Fatalf("writeReservationMarker created no new file (had %d entries)", len(entries))
		}
		p := filepath.Join(dir, created)
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldest := mark(now.Add(-3 * time.Minute))
	mark(now.Add(-2 * time.Minute))
	mark(now.Add(-1 * time.Minute))

	// A stop carrying no evidence hints: the arithmetic FIFO leg does not consult them, and #673 left
	// that leg deliberately unchanged (a marker is a soft accounting hold, not a safety semaphore).
	retireOneReservation(dispatchRetirePayload{Cwd: wd}, key, now)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("after one completion retired one of three markers, %d remain, want exactly 2", len(entries))
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Errorf("retireOneReservation removed the wrong marker; the oldest-by-mtime must be the one gone (stat err=%v)", err)
	}
}
