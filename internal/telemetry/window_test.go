package telemetry

import (
	"encoding/json"
	"os"
	"testing"
)

// The three join rules below are the design's binding rules for turning records into step
// windows. They are enforced here rather than left as prose because each one exists to prevent
// a specific miscount that still balances: overhead double-counted into a step, a re-prime
// shrinking a window and orphaning the events inside it, and a closing turn's tail landing in
// an inter-step limbo that no view owns.

type syntheticFixture struct {
	Provenance string `json:"provenance"`
	Events     []struct {
		Case       string            `json:"_case"`
		TS         string            `json:"ts"`
		Attributes map[string]string `json:"attributes"`
		Expect     string            `json:"expect_attributed_to"`
	} `json:"events"`
	Records         []StepEvent `json:"af_side_records"`
	ExpectedWindows []struct {
		StepID string `json:"step_id"`
		Start  string `json:"start"`
		End    string `json:"end"`
	} `json:"expected_windows"`
}

func loadSynthetic(t *testing.T) syntheticFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/synthetic/api_request_events.json")
	if err != nil {
		t.Fatalf("reading synthetic fixture: %v", err)
	}
	var f syntheticFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parsing synthetic fixture: %v", err)
	}
	if f.Provenance != "synthetic" {
		t.Fatalf("fixture provenance = %q; this file must stay marked synthetic until a real "+
			"capture replaces it, so nobody mistakes a design hypothesis for evidence", f.Provenance)
	}
	if len(f.Records) == 0 || len(f.Events) == 0 {
		t.Fatal("synthetic fixture carries no records or no events")
	}
	return f
}

func TestDeriveWindows(t *testing.T) {
	f := loadSynthetic(t)

	t.Run("earliest start wins", func(t *testing.T) {
		windows := DeriveWindows(f.Records)
		if len(windows) != len(f.ExpectedWindows) {
			t.Fatalf("derived %d windows, want %d", len(windows), len(f.ExpectedWindows))
		}
		for i, want := range f.ExpectedWindows {
			got := windows[i]
			if got.StepID != want.StepID {
				t.Errorf("window %d step = %q, want %q", i, got.StepID, want.StepID)
			}
			// The fixture carries a duplicate step_start for step-one, standing in for a
			// re-prime after a handoff. Taking the later one would shrink the window and
			// silently orphan every event recorded before it.
			if got.Start != want.Start {
				t.Errorf("window %s start = %q, want the EARLIEST start %q", got.StepID, got.Start, want.Start)
			}
			if got.End != want.End {
				t.Errorf("window %s end = %q, want %q", got.StepID, got.End, want.End)
			}
		}
	})

	t.Run("a re-prime cannot shrink a window", func(t *testing.T) {
		windows := DeriveWindows(f.Records)
		if len(windows) == 0 {
			t.Fatal("no windows derived")
		}
		reversed := make([]StepEvent, len(f.Records))
		for i, r := range f.Records {
			reversed[len(f.Records)-1-i] = r
		}
		again := DeriveWindows(reversed)
		if len(again) != len(windows) {
			t.Fatalf("record order changed the window count: %d vs %d", len(again), len(windows))
		}
		for i := range windows {
			if windows[i] != again[i] {
				t.Errorf("window %d differs when records arrive in a different order: %+v vs %+v",
					i, windows[i], again[i])
			}
		}
	})

	t.Run("overhead-marked events are excluded and tails attribute backwards", func(t *testing.T) {
		windows := DeriveWindows(f.Records)
		for _, e := range f.Events {
			got := AttributeEvent(windows, e.TS, e.Attributes)
			if got != e.Expect {
				t.Errorf("%s\n  event at %s attributed to %q, want %q", e.Case, e.TS, got, e.Expect)
			}
		}
	})

	t.Run("an overhead marker wins even when the event sits inside a window", func(t *testing.T) {
		windows := DeriveWindows(f.Records)
		inside := "2026-07-22T18:31:05.500Z"
		if plain := AttributeEvent(windows, inside, map[string]string{}); plain != "step-one" {
			t.Fatalf("control: an unmarked event inside step-one attributed to %q", plain)
		}
		// Without this rule a grader firing mid-step inflates the step's "exact" cost while
		// the reconciliation view still balances, so nothing reports the error.
		if marked := AttributeEvent(windows, inside, map[string]string{"af.overhead": "grader"}); marked == "step-one" {
			t.Error("an overhead-marked event was attributed to the step it fired inside")
		}
	})

	t.Run("the window boundary is half-open", func(t *testing.T) {
		windows := DeriveWindows(f.Records)
		// The start instant belongs to the step; the end instant does not — it belongs to the
		// tail, because the turn that ran the closing verb keeps producing output afterwards.
		if got := AttributeEvent(windows, "2026-07-22T18:31:04.112Z", nil); got != "step-one" {
			t.Errorf("an event exactly at the start instant attributed to %q, want step-one", got)
		}
		if got := AttributeEvent(windows, "2026-07-22T18:31:10.500Z", nil); got == "step-one" {
			t.Error("an event exactly at the end instant was counted inside the step; the window is half-open")
		}
	})

	t.Run("an event before any step belongs to no step", func(t *testing.T) {
		windows := DeriveWindows(f.Records)
		if got := AttributeEvent(windows, "2026-07-22T18:31:01.000Z", nil); got == "step-one" {
			t.Error("an event predating the first step_start was attributed to that step")
		}
	})

	t.Run("no window is derived from an unclosed step", func(t *testing.T) {
		open := []StepEvent{{
			V: SchemaVersion, Event: EventStepStart, TS: "2026-07-22T18:31:04.112Z",
			Agent: "a", InstanceID: "i", StepID: "still-running",
		}}
		if w := DeriveWindows(open); len(w) != 0 {
			t.Errorf("derived %d windows from a step that never closed: %+v", len(w), w)
		}
	})
}
