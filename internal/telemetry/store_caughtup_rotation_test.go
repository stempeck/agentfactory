package telemetry

import (
	"fmt"
	"os"
	"testing"
)

// TestRotationInTheHealthySteadyState pins the case the existing rotation suite never
// reaches: an exporter that is KEEPING UP. Every other rotation test lets the cursor lag,
// which is the direction maybeRotate was written for.
//
// The distinction matters because the cursor bookmarks the NEWEST record of each exported
// batch, and records only ever append to the live log. So a caught-up exporter's bookmark
// always sits in the live generation, never in the kept one — and countUnexported, asked
// about the kept generation ALONE, cannot find it there. Its "bookmark is not in this file"
// branch answers "treat these as unexported", which is the right answer for the full-span
// reads it was written for and exactly the wrong one for this subset read.
//
// Two consequences follow, and this test asserts both, because either alone would let a
// half-fix pass: rotation is refused until the hard cap (so rotateAtBytes is dead and the
// live log grows to four times its intended ceiling), and at the cap the records are booked
// as permanent loss although the backend already has every one of them.
func TestRotationInTheHealthySteadyState(t *testing.T) {
	// Caps low enough to cross both thresholds with a few hundred small records, and a hard
	// cap only four times the rotation point, mirroring the shipped 10MB/40MB ratio. A hard
	// cap out of reach — the shape the existing subtest uses — is what hides this defect.
	shrinkCaps(t, 800, 3200)

	dir := mkStore(t)
	const agent = "a"

	// The exporter is never behind: every record is marked exported the moment it lands.
	// Nothing here is pending, so nothing here can honestly be called lost.
	for i := 1; i <= 400; i++ {
		e := ev(agent, "i1", fmt.Sprintf("s%d", i), i)
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
		if err := markExported(dir, agent, []StepEvent{e}); err != nil {
			t.Fatalf("markExported %d: %v", i, err)
		}
	}

	st, err := Stats(dir, agent)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	// The contract, stated at store.go:180-182: discarding records the cursor has passed is
	// "retention working as intended, and counting it as loss would bury the real losses in
	// noise." A fully-exported generation must therefore book nothing.
	if st.DroppedUnexported != 0 {
		t.Errorf("DroppedUnexported = %d, want 0: every record was exported before the next was "+
			"appended, so no record can have failed to reach a backend. A non-zero count here is "+
			"the operator's only signal for real telemetry loss reporting a false positive",
			st.DroppedUnexported)
	}
	if st.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0: rotation of a fully-exported generation is retention "+
			"working as designed and must not be counted as loss", st.Dropped)
	}

	// The pair that cannot both be right. Backlog counts over the full kept+live span and is
	// correct today; the drop counters count over the kept generation alone and are not.
	// Asserting them together is what makes the contradiction visible rather than arguable.
	if st.Backlog != 0 {
		t.Errorf("Backlog = %d, want 0 (sanity: the exporter is caught up by construction)", st.Backlog)
	}
	if st.Backlog == 0 && st.DroppedUnexported > 0 {
		t.Errorf("nothing is pending (backlog=0) yet %d records are reported as never having "+
			"reached a backend — these two cannot both be true of the same store",
			st.DroppedUnexported)
	}

	// The positive control, and the half the existing subtest gets wrong. os.Stat(kept) alone
	// is satisfied by the FIRST rotation, which is taken before the kept generation exists and
	// therefore before the predicate is ever consulted. Requiring the live log to have stayed
	// under the ROTATION threshold proves the predicate ran and permitted rotation, not merely
	// that a rename happened once.
	info, err := os.Stat(stepsPath(dir, agent))
	if err != nil {
		t.Fatalf("stat live log: %v", err)
	}
	if info.Size() > rotateAtBytes {
		t.Errorf("live log is %d bytes, above the %d rotation threshold: rotation is being "+
			"refused in the healthy steady state, so the log grows to the hard cap (%d) instead "+
			"of rotating at its intended size", info.Size(), rotateAtBytes, hardCapBytes)
	}

	if _, err := os.Stat(stepsPath(dir, agent) + rotatedSuffix); err != nil {
		t.Errorf("no kept generation exists after 400 records across a %d-byte rotation "+
			"threshold: rotation never happened at all (%v)", rotateAtBytes, err)
	}
}

// TestCountUnexportedFullSpanSemanticsAreUnchanged is the regression guard on the fix for
// the test above. countUnexported is shared by three callers; only ONE of them asks a subset
// question. The other two — ReadEvents' pending selection and Stats' backlog — pass the full
// kept+live span, where "the bookmark is absent, so treat everything as unexported" is the
// correct and safe answer.
//
// A fix that changes countUnexported's own semantics instead of scoping the rotation caller
// would make ReadEvents return an empty pending slice, Drain send nothing and report no
// error, and the cursor never advance — an exporter that has silently stopped, which is
// strictly worse than the defect being fixed. This test fails if that happens.
func TestCountUnexportedFullSpanSemanticsAreUnchanged(t *testing.T) {
	dir := mkStore(t)
	const agent = "a"

	for i := 1; i <= 5; i++ {
		if err := AppendEvent(dir, ev(agent, "i1", fmt.Sprintf("s%d", i), i)); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	t.Run("an absent bookmark means everything is pending", func(t *testing.T) {
		st, err := Stats(dir, agent)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if st.Backlog != 5 {
			t.Errorf("Backlog = %d with no cursor written, want 5: a store that has never "+
				"exported has everything pending", st.Backlog)
		}
		pending, _, err := ReadEvents(dir, Filter{Agent: agent, Unexported: true})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(pending) != 5 {
			t.Errorf("pending = %d records, want 5: an empty pending set with an absent "+
				"bookmark is an exporter that has silently stopped", len(pending))
		}
	})

	t.Run("a bookmark mid-span leaves only what follows it pending", func(t *testing.T) {
		all, _ := readAll(t, dir, agent)
		if err := markExported(dir, agent, all[:3]); err != nil {
			t.Fatalf("markExported: %v", err)
		}
		st, err := Stats(dir, agent)
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if st.Backlog != 2 {
			t.Errorf("Backlog = %d after exporting 3 of 5, want 2", st.Backlog)
		}
		pending, _, err := ReadEvents(dir, Filter{Agent: agent, Unexported: true})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(pending) != 2 {
			t.Errorf("pending = %d after exporting 3 of 5, want 2", len(pending))
		}
	})
}
