package cmd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// #678 K10, the improvement loop's efficiency half: what a formula DECLARED before a self-edit,
// what this run spent generating, and what the self-edit session itself cost.
//
// The three live together because they answer one question in three parts — did the loop's edit
// make the formula cheaper without making it mean something else — and because all three are
// derived from the same two sources: the marker's fire-time fingerprint and this instance's step
// records. None of them can fail the verb: every one degrades to silence, because a completion that
// refused to finish over an unreadable measurement would cost the operator a live session to
// preserve a sentence.

// improvementStepLabel is the step_label the improvement session's own record carries (Gap 7 i).
// It is deliberately not a formula step id: no formula declares it, and a reader joining on
// step_label sees the loop's spend as its own row rather than as some step's.
const improvementStepLabel = "improvement"

// improvementSemantics is a formula's declared shape reduced to the four facts design-doc.md:210
// says a self-edit may never silently change: which steps exist, which of them gate, which artifact
// paths the step text requires, and how many protected capture directives survive.
//
// StepDigests rather than a step count, because a count cannot answer either of the two questions
// asked of it. It cannot see a rename at equal cardinality — three steps before, three after, one
// of them a different step — and it cannot say whether the edit TOUCHED a given step, which the
// waste ranking is required to report about every step it names. One digest per step costs a few KB
// on a marker readImprovementPending unmarshals on the watchdog's per-tick path; the formula body
// itself reaches ~80 KB and would not be affordable there.
type improvementSemantics struct {
	StepDigests   map[string]string `json:"step_digests"`
	GateSteps     int               `json:"gate_steps"`
	ArtifactPaths int               `json:"artifact_paths"`
	Directives    int               `json:"directives"`
}

// artifactPathRE matches a path-shaped token in step text: at least one slash, ending in a short
// extension. Deliberately narrower than "anything containing a slash" — formula prose is full of
// `af mail send`-style fragments and "A/B" constructions, and counting those would make the lint
// report deltas no edit caused, which is the one failure that would teach an operator to ignore it.
var artifactPathRE = regexp.MustCompile(`[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+\.[A-Za-z0-9]{1,8}`)

// protectedDirectiveRE matches the capture directives design-doc.md:210 names as protected.
//
// Case-insensitive on purpose: a lowercased directive is still a directive, and the lint compares a
// count before against a count after. A rule that over-matches does so symmetrically and stays
// correct; a rule that misses a spelling loses the deletion it exists to catch.
var protectedDirectiveRE = regexp.MustCompile(`(?i)verbatim|re-copy|byte-for-byte|read it top to bottom`)

// improvementSemanticsOf fingerprints the formula at path, or returns nil if it will not parse.
// nil is the honest answer and it is not an error: the improvement hook exists partly to let an
// agent repair a broken formula, so a fire must not depend on the fingerprint succeeding.
func improvementSemanticsOf(path string) *improvementSemantics {
	f, err := formula.ParseFile(path)
	if err != nil {
		return nil
	}
	return semanticsOfFormula(f)
}

func semanticsOfFormula(f *formula.Formula) *improvementSemantics {
	s := improvementSemantics{StepDigests: make(map[string]string, len(f.Steps))}
	paths := map[string]struct{}{}
	for _, step := range f.Steps {
		body := step.Title + "\n" + step.Description
		sum := sha256.Sum256([]byte(body))
		s.StepDigests[step.ID] = fmt.Sprintf("%x", sum[:])
		// stepHasGate, not a second heuristic of this file's own (formula.go:797-806). Two
		// definitions of "gate step" in one package would eventually disagree, and the one that
		// disagreed quietly would be this one.
		if stepHasGate(step) {
			s.GateSteps++
		}
		for _, p := range artifactPathRE.FindAllString(body, -1) {
			paths[p] = struct{}{}
		}
		s.Directives += len(protectedDirectiveRE.FindAllString(body, -1))
	}
	// Distinct across the formula, not summed per step: an artifact named by three steps is one
	// artifact, and a step split in two would otherwise read as a new requirement appearing.
	s.ArtifactPaths = len(paths)
	return &s
}

// improvementSemanticsNote is the lint's sentence in the outcome mail: what the self-edit changed
// about the formula's declared shape, as COUNTS.
//
// Counts, never formula prose (design-doc.md:210). This mail lands in a peer's mailbox, and a step
// title or description quoted into it would publish formula content on a surface the improvement
// loop has no business publishing to. The waste ranking next door names step IDS, which are
// identifiers rather than content; nothing else crosses.
//
// Silent when either side is missing, and the two absences mean different things: a nil before is a
// marker from a binary that captured none, a nil after is a formula that no longer parses. Both are
// already reported elsewhere in the verdict, and a delta invented from one side would be a finding
// about nothing.
func improvementSemanticsNote(before, after *improvementSemantics) string {
	if before == nil || after == nil {
		return ""
	}
	idsUnchanged := sameStepIDs(before.StepDigests, after.StepDigests)
	verdict := "CHANGED"
	if idsUnchanged &&
		before.GateSteps == after.GateSteps &&
		before.ArtifactPaths == after.ArtifactPaths &&
		before.Directives == after.Directives {
		verdict = "preserved"
	}
	ids := "step ids unchanged"
	if !idsUnchanged {
		ids = "step ids CHANGED"
	}
	return fmt.Sprintf(" Semantics lint: %s — steps %d -> %d, gate steps %d -> %d,"+
		" artifact paths %d -> %d, directives %d -> %d, %s.",
		verdict, len(before.StepDigests), len(after.StepDigests),
		before.GateSteps, after.GateSteps,
		before.ArtifactPaths, after.ArtifactPaths,
		before.Directives, after.Directives, ids)
}

func sameStepIDs(before, after map[string]string) bool {
	if len(before) != len(after) {
		return false
	}
	for id := range before {
		if _, ok := after[id]; !ok {
			return false
		}
	}
	return true
}

// improvementWasteNote ranks this run's steps by what they GENERATED and says whether the self-edit
// went anywhere near the expensive ones — the join the loop needs to tell an edit that saved
// something from one that rearranged the cheap end of the formula.
//
// It reads records rather than telemetryReportDTO because the report row labels a step from its
// TITLE, falling back to the bead id (telemetry.go:552-557), and neither can be matched against a
// fingerprint keyed by the formula's own step ids. StepLabel is that id, which is what makes this
// join possible at all.
//
// An empty instance id is never a selector: telemetry.ReadEvents only filters on a non-empty one,
// so passing "" would rank an unrelated run's steps as this run's. The roster is walked rather than
// narrowed to one agent for the reason improvementContextNote gives: a formula instance can span
// agents, and the instance id is the run's identity while the agent is not.
func improvementWasteNote(factoryRoot, instanceID string, before, after *improvementSemantics) string {
	if instanceID == "" {
		return ""
	}
	agents, err := telemetryReportAgents(factoryRoot, "")
	if err != nil {
		return ""
	}

	spend := map[string]int64{}
	for _, agent := range agents {
		records, _, readErr := telemetry.ReadEvents(config.TelemetryDir(factoryRoot),
			telemetry.Filter{Agent: agent, InstanceID: instanceID})
		if readErr != nil {
			continue
		}
		for _, r := range records {
			if r.Event != telemetry.EventStepEnd || r.StepLabel == "" || r.StepLabel == improvementStepLabel {
				continue
			}
			total, measured := int64(0), false
			if r.OutTokens != nil {
				total, measured = total+*r.OutTokens, true
			}
			// Sub-agent spend is the larger half of the corpus and belongs in the same ranking: a
			// step that generated little itself and launched five sub-agents is not a cheap step.
			if r.SubagentTokens != nil {
				total, measured = total+*r.SubagentTokens, true
			}
			// A later close for the same step supersedes an earlier one — a re-primed step has one
			// current cost, not two.
			if measured {
				spend[r.StepLabel] = total
			}
		}
	}
	if len(spend) == 0 {
		return ""
	}

	labels := make([]string, 0, len(spend))
	for label := range spend {
		labels = append(labels, label)
	}
	// Ties broken by label so two runs over the same figures describe them in the same order.
	sort.Slice(labels, func(i, j int) bool {
		if spend[labels[i]] != spend[labels[j]] {
			return spend[labels[i]] > spend[labels[j]]
		}
		return labels[i] < labels[j]
	})
	if len(labels) > 3 {
		labels = labels[:3]
	}

	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		entry := fmt.Sprintf("%s %d", label, spend[label])
		if touched, known := stepTouched(before, after, label); known {
			if touched {
				entry += " touched"
			} else {
				entry += " untouched"
			}
		}
		parts = append(parts, entry)
	}
	return " Waste ranking (generated tokens): " + strings.Join(parts, ", ") + "."
}

// stepTouched answers whether the self-edit changed the named step, and whether that is knowable at
// all. known==false without both fingerprints, because "the edit did not touch this step" and "no
// baseline exists to compare against" are different statements and only one of them is evidence.
func stepTouched(before, after *improvementSemantics, label string) (touched, known bool) {
	if before == nil || after == nil {
		return false, false
	}
	was, hadBefore := before.StepDigests[label]
	is, hasAfter := after.StepDigests[label]
	if !hadBefore || !hasAfter {
		return true, true
	}
	return was != is, true
}

// recordImprovementSession measures the improvement session itself (Gap 7 i): one step_end whose
// step_label is "improvement", carrying what the self-edit session generated between the hook
// firing and this verb running.
//
// It carries its own gate rather than riding af done's, because it runs in a different process from
// the close that fired the hook — the session that received the instruction is the one completing
// it, and nothing from that earlier frame survives into this one.
//
// The span's session guard is satisfied by construction: the improvement session IS the session
// running this verb, which is the whole reason af done kept it alive. What that cannot see is a
// session recycled between the fire and the completion; the marker records no session id, so the
// window would then cover only the post-recycle transcript. That is a known narrowing, not a
// silent one — the figures would under-report rather than describe the wrong session.
//
// The learned digest is deliberately NOT updated from this record. That cache accumulates per-step
// medians a later run is judged against, and an improvement session is not a formula step: folding
// it in would move every median by a figure no step produced.
func recordImprovementSession(factoryRoot, agentDir string, m improvementMarker) {
	if !telemetryFactoryEnabled(factoryRoot) {
		return
	}
	agent, err := resolveAgentName(agentDir, factoryRoot)
	if err != nil {
		return
	}
	started := improvementFiredAt(m.FiredAt)
	ctx := withVerbTelemetry(context.Background(), verbTelemetry{
		verb: improvementStepLabel, agent: agent, start: started, enabled: true,
	})
	ev := telemetryRecordFor(ctx, factoryRoot, agentDir, agent, m.InstanceID, "")
	ev.Event = telemetry.EventStepEnd
	ev.Formula = m.Formula
	ev.StepLabel = improvementStepLabel
	// The report labels a row from StepTitle, falling back to StepID (telemetry.go:552-557). This
	// record has no bead behind it, so without a title it would render as a nameless row.
	ev.StepTitle = improvementStepLabel
	ev.Status = telemetry.StatusClosed
	if !started.IsZero() {
		ev.DurationMS = int(time.Since(started).Milliseconds())
	}
	attachGenerationScalars(&ev, stepSpan{startTS: m.FiredAt, sessionID: ev.SessionID}, agentDir)
	appendTelemetryRecord(factoryRoot, ev)
}

// improvementFiredAt parses the marker's RFC3339 stamp, or returns the zero time. The zero time is
// what stops a malformed marker inventing a duration measured from the epoch.
func improvementFiredAt(firedAt string) time.Time {
	t, err := time.Parse(time.RFC3339, firedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}
