package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two first-hand observations of the SAME query contradict each other inside this repository, and
// nothing in the tree says which one governs.
//
//   - The shipped per-step tokens view records that during the review of PR #567 a pinned
//     OpenObserve v0.91.3 was stood up, both planes were ingested, and this view's query ran
//     verbatim, reproducing the token totals column spelling for column spelling.
//   - The capture committed by THIS work records the same query returning HTTP 400 code 20004,
//     "No field named af_overhead", against a live backend.
//
// A reader who meets one of those records has no way to learn the other exists. That is the
// defect: not that the query is broken, but that the repository holds two contradicting
// first-hand observations and reconciles neither.
//
// These tests pin the reconciliation, not a verdict. The evidence that settles it is already in
// the tree — install_telemetry_views/README.md states in one paragraph both that #567 "ingested
// both planes" AND that "the native event shape remains the synthetic fixture's hypothesis until
// a real capture replaces it" — so the two observations were never symmetric. But a future
// re-capture could change which half governs, so the assertions below require a RECORD that names
// both observations and states the relationship, and deliberately admit an explicitly unresolved
// verdict. A test that demanded a settled answer would force the next author to guess, which is
// the failure mode this record exists to prevent.

func readTokensView(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join(findModuleRoot(t), "internal", "cmd", "install_telemetry_views",
		"agent-model-step-tokens.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc
}

func viewAssumptions(t *testing.T) string {
	t.Helper()
	doc := readTokensView(t)
	raw, ok := doc["af_assumptions"]
	if !ok {
		t.Fatal("the tokens view carries no af_assumptions block. That block is where this view " +
			"records what it has NOT verified; without it every assumption the query makes is " +
			"invisible to the next reader")
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("af_assumptions is %T, want a list", raw)
	}
	var b strings.Builder
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			t.Fatalf("af_assumptions entry is %T, want a string", it)
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	return b.String()
}

// TestTokensViewRecordsWhichObservationGoverns is the reviewer's ask, mechanically.
func TestTokensViewRecordsWhichObservationGoverns(t *testing.T) {
	assumptions := viewAssumptions(t)

	// The capture must be named, by its error code — the one token that unambiguously identifies
	// the observation rather than gesturing at it.
	if !strings.Contains(assumptions, "20004") {
		t.Error("af_assumptions does not mention the captured 20004. The view asserts that its query " +
			"was verified against a live backend; the same repository holds a capture of that query " +
			"being rejected. Recording only the success is a record that reads as settled and is not")
	}

	// The capture must be locatable, not merely alluded to.
	if !strings.Contains(assumptions, "openobserve-v0.91.3") {
		t.Error("af_assumptions does not point at internal/telemetry/testdata/openobserve-v0.91.3/ — " +
			"a reader told that a contradicting observation exists, but not where, cannot weigh it")
	}

	// And the relationship must be stated. Any of these forms is acceptable; what is not
	// acceptable is naming both observations and leaving the reader to guess which one to trust.
	verdicts := []string{"GOVERNS", "governs", "UNRESOLVED", "unresolved"}
	found := false
	for _, v := range verdicts {
		if strings.Contains(assumptions, v) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("af_assumptions names two contradicting observations without saying how they "+
			"relate. State which observation governs which half of the query, or record the "+
			"relationship as explicitly unresolved — one of %v must appear. An explicitly "+
			"unresolved record is an honest answer; silence is not, because silence reads as "+
			"agreement", verdicts)
	}
}

// TestCaptureAndTokensViewPointAtEachOther closes the other direction. The capture README already
// states that the shipped view's SQL fails on that backend; without a link back, the two records
// remain two islands and a reader arriving at either one still leaves misinformed.
func TestCaptureAndTokensViewPointAtEachOther(t *testing.T) {
	capturePath := filepath.Join(findModuleRoot(t), "internal", "telemetry", "testdata",
		"openobserve-v0.91.3", "README.md")
	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read %s: %v", capturePath, err)
	}
	capture := string(raw)

	if !strings.Contains(capture, "#567") && !strings.Contains(capture, "af_assumptions") {
		t.Error("the capture README records that the shipped view's SQL fails here, but never " +
			"mentions the view's own contradicting record (its af_assumptions block, and the PR #567 " +
			"run it cites). A reader who starts at the capture concludes the query is simply broken; " +
			"a reader who starts at the view concludes it is verified. Both are reading half the " +
			"evidence")
	}

	// The two records disagree about the logs schema itself: this README enumerates six fields
	// while the 20004 captured the same day enumerates four and adds one the README omits. That
	// discrepancy is part of what a reader has to weigh, so it must not be left silent.
	if !strings.Contains(capture, "service_name") {
		t.Error("the capture README's logs-schema list omits service_name, which the captured 20004 " +
			"response — taken the same day, against the same backend — reports as a valid field. " +
			"Two records of one schema disagree; the README must carry the discrepancy rather than " +
			"present its own list as complete")
	}
}
