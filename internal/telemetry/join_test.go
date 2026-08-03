package telemetry

import "testing"

// The three tests below pin the design's three binding join rules by their exact answers.
//
// TestDeriveWindows already exercises the same code, but it asserts inequalities — "not
// step-one" — which a rule that returns the wrong bucket name still satisfies. These assert the
// bucket strings themselves, because the bucket name IS the contract: a view that groups by
// "grader-overhead" and a producer that emits "grader" agree on nothing, and every total still
// balances.

const (
	s1Start = "2026-07-22T18:31:04.112Z"
	s1End   = "2026-07-22T18:31:10.500Z"
	s2Start = "2026-07-22T18:31:15.000Z"
	s2End   = "2026-07-22T18:31:25.000Z"
)

// closedStep is a start/end pair for one step, in the order a real log holds them.
func closedStep(instance, step, start, end string) []StepEvent {
	return []StepEvent{
		{V: SchemaVersion, Event: EventStepStart, TS: start, Agent: "solver", InstanceID: instance, StepID: step},
		{V: SchemaVersion, Event: EventStepEnd, TS: end, Agent: "solver", InstanceID: instance, StepID: step,
			Status: StatusClosed},
	}
}

// TestJoinExcludesOverheadEvents pins rule 1: an event carrying an overhead marker belongs to
// its overhead bucket and to no step, even when it fired in the middle of one.
//
// The rule exists because the wrong answer still balances. A quality gate invoking a model
// mid-step would be counted into that step's "exact" cost, the reconciliation view would agree,
// and the only symptom would be a step that looks more expensive than the work it did.
func TestJoinExcludesOverheadEvents(t *testing.T) {
	windows := DeriveWindows(closedStep("i1", "s1", s1Start, s1End))
	if len(windows) != 1 {
		t.Fatalf("fixture derived %d windows, want 1", len(windows))
	}

	const inside = "2026-07-22T18:31:05.500Z"
	const afterEnd = "2026-07-22T18:31:12.000Z"

	cases := []struct {
		name  string
		ts    string
		attrs map[string]string
		want  string
	}{
		{"control: an unmarked event inside the window is the step's", inside, nil, "s1"},
		{"a grader event inside the window belongs to the grader bucket", inside,
			map[string]string{"af.overhead": "grader"}, "grader-overhead"},
		{"the bucket name is carried through, not hardcoded", inside,
			map[string]string{"af.overhead": "gate"}, "gate-overhead"},
		{"an empty bucket is not a marker", inside,
			map[string]string{"af.overhead": ""}, "s1"},
		{"an unrelated attribute does not trigger the bucket", inside,
			map[string]string{"af.agent": "solver"}, "s1"},
		{"the marker is decided before the timestamp is even parsed", "not-a-timestamp",
			map[string]string{"af.overhead": "grader"}, "grader-overhead"},
		{"overhead outranks the trailing-tail rule", afterEnd,
			map[string]string{"af.overhead": "grader"}, "grader-overhead"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AttributeEvent(windows, tc.ts, tc.attrs); got != tc.want {
				t.Errorf("AttributeEvent(%q, %v) = %q, want %q", tc.ts, tc.attrs, got, tc.want)
			}
		})
	}

	// The sum property is the reason the rule exists: the step's own bucket must not contain
	// the overhead event, not merely label it differently somewhere else.
	t.Run("the step's bucket excludes the overhead event", func(t *testing.T) {
		batch := []struct {
			ts    string
			attrs map[string]string
		}{
			{"2026-07-22T18:31:05.000Z", nil},
			{inside, map[string]string{"af.overhead": "grader"}},
			{"2026-07-22T18:31:06.000Z", nil},
		}
		buckets := map[string]int{}
		for _, e := range batch {
			buckets[AttributeEvent(windows, e.ts, e.attrs)]++
		}
		if buckets["s1"] != 2 {
			t.Errorf("step bucket holds %d events, want 2 (the overhead event must not be in it)", buckets["s1"])
		}
		if buckets["grader-overhead"] != 1 {
			t.Errorf("grader-overhead bucket holds %d events, want 1", buckets["grader-overhead"])
		}
	})
}

// TestJoinEarliestStartWins pins rule 2: window derivation takes the EARLIEST step_start per
// (instance_id, step_id), so a re-prime after a handoff, a respawn, or a cleared runtime
// directory can never shrink a window.
//
// Taking the latest start would move the window's left edge forward past events that were
// recorded during the original run; those events then fall outside every window and are
// attributed elsewhere without anything reporting a discrepancy.
func TestJoinEarliestStartWins(t *testing.T) {
	const rePrime = "2026-07-22T18:31:08.900Z"

	// Deliberately listed with the re-prime FIRST, so the rule cannot be satisfied by
	// accidentally keeping whichever start was seen first.
	records := []StepEvent{
		{V: SchemaVersion, Event: EventStepStart, TS: rePrime, Agent: "solver", InstanceID: "i1", StepID: "s1"},
		{V: SchemaVersion, Event: EventStepStart, TS: s1Start, Agent: "solver", InstanceID: "i1", StepID: "s1"},
		{V: SchemaVersion, Event: EventStepEnd, TS: s1End, Agent: "solver", InstanceID: "i1", StepID: "s1",
			Status: StatusClosed},
	}

	windows := DeriveWindows(records)
	if len(windows) != 1 {
		t.Fatalf("derived %d windows from one step, want 1: %+v", len(windows), windows)
	}
	if windows[0].Start != s1Start {
		t.Errorf("window start = %q, want the EARLIEST start %q", windows[0].Start, s1Start)
	}
	if windows[0].End != s1End {
		t.Errorf("window end = %q, want %q", windows[0].End, s1End)
	}

	// The load-bearing assertion. Under a latest-start-wins bug this event sits before the
	// window's start and after no window's end, so it comes back "unattributed" — the silent
	// orphaning the rule exists to prevent.
	t.Run("an event between the earliest start and the re-prime stays with the step", func(t *testing.T) {
		if got := AttributeEvent(windows, "2026-07-22T18:31:05.000Z", nil); got != "s1" {
			t.Errorf("event between the first start and the re-prime attributed to %q, want %q", got, "s1")
		}
	})

	t.Run("record order does not change the window", func(t *testing.T) {
		reversed := make([]StepEvent, len(records))
		for i, r := range records {
			reversed[len(records)-1-i] = r
		}
		again := DeriveWindows(reversed)
		if len(again) != 1 || again[0] != windows[0] {
			t.Errorf("reversing the records changed the window: %+v vs %+v", again, windows)
		}
	})

	t.Run("the latest end wins", func(t *testing.T) {
		early := append(append([]StepEvent{}, records...), StepEvent{
			V: SchemaVersion, Event: EventStepEnd, TS: "2026-07-22T18:31:09.000Z",
			Agent: "solver", InstanceID: "i1", StepID: "s1", Status: StatusClosed,
		})
		got := DeriveWindows(early)
		if len(got) != 1 {
			t.Fatalf("derived %d windows, want 1", len(got))
		}
		if got[0].End != s1End {
			t.Errorf("window end = %q, want the LATEST end %q", got[0].End, s1End)
		}
	})

	t.Run("a step that never ended yields no window", func(t *testing.T) {
		withOpen := append(append([]StepEvent{}, records...), StepEvent{
			V: SchemaVersion, Event: EventStepStart, TS: s2Start,
			Agent: "solver", InstanceID: "i1", StepID: "s2",
		})
		if got := DeriveWindows(withOpen); len(got) != 1 {
			t.Errorf("derived %d windows, want 1 — an unclosed step must not produce one: %+v", len(got), got)
		}
	})

	t.Run("the same step id in another instance is a separate window", func(t *testing.T) {
		twoRuns := append(append([]StepEvent{}, records...), closedStep("i2", "s1", s2Start, s2End)...)
		got := DeriveWindows(twoRuns)
		if len(got) != 2 {
			t.Fatalf("derived %d windows from the same step in two instances, want 2: %+v", len(got), got)
		}
		if got[0].InstanceID == got[1].InstanceID {
			t.Errorf("both windows carry instance %q; re-running a formula merged its windows", got[0].InstanceID)
		}
	})
}

// TestJoinTrailingTailToPrecedingStep pins rule 3 and the half-open boundary it rests on: the
// start instant belongs to the step, the end instant does not, and an event after a step ended
// is attributed BACKWARDS to that step with a -tail suffix.
//
// Boundary spend is not symmetric noise. The turn that ran the closing command keeps working
// after the call returns, so a step's closing tail reliably lands after its end. Splitting the
// difference would show phantom cost between steps that belongs to neither.
func TestJoinTrailingTailToPrecedingStep(t *testing.T) {
	records := append(closedStep("i1", "s1", s1Start, s1End), closedStep("i1", "s2", s2Start, s2End)...)
	windows := DeriveWindows(records)
	if len(windows) != 2 {
		t.Fatalf("derived %d windows, want 2: %+v", len(windows), windows)
	}

	cases := []struct {
		name string
		ts   string
		want string
	}{
		{"the start instant is inside the step", s1Start, "s1"},
		{"the last instant before the end is inside", "2026-07-22T18:31:10.499Z", "s1"},
		{"the end instant is the tail, not the step", s1End, "s1-tail"},
		{"an event between two steps belongs to the one that just closed", "2026-07-22T18:31:11.400Z", "s1-tail"},
		{"still the tail right up to the next start", "2026-07-22T18:31:14.999Z", "s1-tail"},
		{"the next step's start instant is its own", s2Start, "s2"},
		{"after everything, the LATEST-ending window owns the tail", "2026-07-22T18:31:30.000Z", "s2-tail"},
		{"an event before every step is never attributed forwards", "2026-07-22T18:31:01.000Z", BucketUnattributed},
		{"an unparseable timestamp is unattributed", "garbage", BucketUnattributed},
		{"an empty timestamp is unattributed", "", BucketUnattributed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AttributeEvent(windows, tc.ts, nil); got != tc.want {
				t.Errorf("AttributeEvent(%q) = %q, want %q", tc.ts, got, tc.want)
			}
		})
	}

	t.Run("with no windows at all nothing is attributed", func(t *testing.T) {
		if got := AttributeEvent(nil, s1End, nil); got != BucketUnattributed {
			t.Errorf("AttributeEvent(nil windows) = %q, want %q", got, BucketUnattributed)
		}
	})
}
