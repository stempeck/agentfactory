package telemetry

import (
	"sort"
	"time"
)

// A Window is one step's execution span, derived from the records rather than measured
// directly. Attribution of a backend's own events to a step is a join over these windows,
// because the one thing a launched session cannot stamp onto its signals is which step is
// currently running: identity attributes are fixed when the process starts, and steps change
// many times inside one long-lived session.
type Window struct {
	InstanceID string
	StepID     string
	Start      string
	End        string
}

// Bucket names for events that fall outside every step.
const (
	// BucketUnattributed is spend before the first step of an instance began — session
	// startup, a formula that never got going. Named rather than discarded, so the difference
	// between a session's total and the sum of its steps stays visible.
	BucketUnattributed = "unattributed"

	// tailSuffix marks spend attributed backwards to the step that caused it.
	tailSuffix = "-tail"

	// overheadSuffix marks spend owned by an overhead bucket rather than by any step.
	overheadSuffix = "-overhead"

	// overheadAttr is the marker a signal carries when it belongs to an overhead bucket.
	overheadAttr = "af.overhead"
)

// DeriveWindows turns records into step windows, taking the EARLIEST start for each step.
//
// A step can legitimately be started more than once — a handoff, a respawn, or a cleared
// runtime directory all cause the start to be recorded again. Taking the latest would shrink
// the window, and every event that arrived during the original run would fall outside it and
// be silently attributed elsewhere. Taking the earliest can only ever widen it, and a window
// that is too wide is visible in a reconciliation view while one that is too narrow is not.
//
// A step with no recorded end yields no window. It is still running (or it crashed), and the
// local report shows it as open; inventing an end would put a fabricated duration into a
// backend where nothing could later distinguish it from a measurement.
func DeriveWindows(records []StepEvent) []Window {
	type key struct{ instance, step string }
	starts := map[key]string{}
	ends := map[key]string{}
	var order []key

	for _, r := range records {
		k := key{r.InstanceID, r.StepID}
		switch r.Event {
		case EventStepStart:
			if prev, seen := starts[k]; !seen || r.TS < prev {
				starts[k] = r.TS
			}
			if _, seen := ends[k]; !seen {
				if !containsKey(order, k) {
					order = append(order, k)
				}
			}
		case EventStepEnd:
			if prev, seen := ends[k]; !seen || r.TS > prev {
				ends[k] = r.TS
			}
			if !containsKey(order, k) {
				order = append(order, k)
			}
		}
	}

	out := make([]Window, 0, len(order))
	for _, k := range order {
		start, hasStart := starts[k]
		end, hasEnd := ends[k]
		if !hasStart || !hasEnd {
			continue
		}
		out = append(out, Window{InstanceID: k.instance, StepID: k.step, Start: start, End: end})
	}
	// Sorted rather than left in encounter order, so a report and a re-read of the same log in
	// a different order describe the same timeline.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		if out[i].InstanceID != out[j].InstanceID {
			return out[i].InstanceID < out[j].InstanceID
		}
		return out[i].StepID < out[j].StepID
	})
	return out
}

func containsKey[T comparable](haystack []T, needle T) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// AttributeEvent decides which bucket one backend event belongs to.
//
// Three rules, each of which exists because the obvious alternative produces a wrong answer
// that still balances:
//
//  1. An event marked as overhead belongs to its overhead bucket and to no step, even when it
//     fired in the middle of one. A quality gate invoking a model mid-step would otherwise be
//     counted into that step's "exact" cost, and the totals would still add up.
//
//  2. The window is half-open. The start instant belongs to the step; the end instant does
//     not.
//
//  3. An event after a step ended and before the next one began is attributed BACKWARDS, to
//     the step that just closed. Boundary spend is not symmetric noise: the turn that ran the
//     closing command keeps working after the call returns, so a step's closing tail reliably
//     lands after its end. Splitting the difference would show phantom cost between steps that
//     belongs to neither.
func AttributeEvent(windows []Window, ts string, attrs map[string]string) string {
	if bucket, marked := attrs[overheadAttr]; marked && bucket != "" {
		return bucket + overheadSuffix
	}

	at, err := parseRecordTime(ts)
	if err != nil {
		return BucketUnattributed
	}

	var preceding *Window
	for i := range windows {
		w := windows[i]
		start, serr := parseRecordTime(w.Start)
		end, eerr := parseRecordTime(w.End)
		if serr != nil || eerr != nil {
			continue
		}
		if !at.Before(start) && at.Before(end) {
			return w.StepID
		}
		if !at.Before(end) {
			if preceding == nil || end.After(mustTime(preceding.End)) {
				preceding = &windows[i]
			}
		}
	}
	if preceding != nil {
		return preceding.StepID + tailSuffix
	}
	return BucketUnattributed
}

func mustTime(ts string) time.Time {
	t, err := parseRecordTime(ts)
	if err != nil {
		return time.Time{}
	}
	return t
}
