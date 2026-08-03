package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The operator documentation asserts a capability the repository's own capture shows failing, and
// documents none of the states an operator actually meets when it does.
//
// The Telemetry section says token usage is what `af telemetry usage` returns from the backend
// "broken down by agent, model, and step" — the per-step half that returns query_failed against
// the captured backend. Below it, a table documents eight banner states and gives each a next
// step. But that table covers the STATUS axes; the usage pane renders a different, disjoint set
// of five sentences, and not one of them appears anywhere in the document.
//
// So an operator following this documentation opens the panel, reads a sentence that appears in
// no table, describing the one capability the docs single out as working, with no documented next
// step — and files a bug against the console.

func telemetrySection(t *testing.T) string {
	t.Helper()
	docPath := filepath.Join(findModuleRoot(t), "USING_AGENTFACTORY.md")
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	section, ok := telemetryDocSection(string(data))
	if !ok {
		t.Fatal("USING_AGENTFACTORY.md has no '## Telemetry' section")
	}
	return strings.Join(section, "\n")
}

// TestTelemetryDocQualifiesTheTokenBreakdown pins the capability claim against the evidence.
//
// It does not require the claim to be deleted: the command and the payload really do carry a
// per-agent/model/step shape. What it requires is that the sentence stop asserting the breakdown
// unconditionally, when the same repository holds a capture of that query being rejected and the
// command's own source concedes the view "does not currently execute here".
func TestTelemetryDocQualifiesTheTokenBreakdown(t *testing.T) {
	section := telemetrySection(t)

	const claim = "broken down by agent, model, and step"
	idx := strings.Index(section, claim)
	if idx < 0 {
		// Rewording the claim away also satisfies the ask — what must not survive is the
		// unconditional assertion.
		return
	}

	// The qualification must live in the CLAIM'S OWN SENTENCE, not merely somewhere nearby.
	//
	// A proximity window is not good enough here and the first draft of this test proved it: the
	// very next sentence says session metrics "does not honour the time-window control", which
	// satisfied a search for "does not" while saying nothing whatever about the token breakdown.
	// The assertion passed against the exact prose it was written to reject.
	sentence := claimSentence(section, idx, len(claim))

	qualifiers := []string{
		"if ", "when ", "unless", "provided", "requires", "precondition",
		"may not", "does not", "cannot", "will not", "is not", "currently",
	}
	found := false
	lowered := strings.ToLower(sentence)
	for _, q := range qualifiers {
		if strings.Contains(lowered, q) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("USING_AGENTFACTORY.md asserts the per-step token breakdown unconditionally.\n"+
			"sentence: %q\n"+
			"That breakdown is the half this repository has captured the backend rejecting "+
			"(internal/telemetry/testdata/openobserve-v0.91.3/tokens_search_response_query_failed.json), "+
			"and telemetry_usage.go concedes in its own comment that the shipped view \"does not "+
			"currently execute here\". Documentation that promises a capability the tree records as "+
			"failing sends the operator to debug the console instead of the query. The qualification "+
			"has to be in this sentence — a caveat elsewhere is not read with the claim.",
			strings.TrimSpace(sentence))
	}
}

// claimSentence returns the sentence containing the claim, with newlines flattened so a claim
// wrapped across markdown lines is still read as one sentence.
func claimSentence(section string, idx, length int) string {
	flat := strings.ReplaceAll(section, "\n", " ")

	start := strings.LastIndex(flat[:idx], ". ")
	if start < 0 {
		start = 0
	} else {
		start += 2
	}
	end := idx + length
	if rel := strings.Index(flat[end:], ". "); rel >= 0 {
		end += rel + 1
	} else if end > len(flat) {
		end = len(flat)
	}
	return flat[start:end]
}

// TestTelemetryDocDocumentsEveryUsagePaneState is the missing-table pin.
//
// The five sentences below are what telUsageStateText renders. They are asserted here by their
// distinguishing PHRASE rather than verbatim, so the doc may word them naturally for a reader
// while still being required to cover every state — a doc that quotes four and omits one leaves
// exactly one operator with no next step, which is the situation being fixed.
func TestTelemetryDocDocumentsEveryUsagePaneState(t *testing.T) {
	section := strings.ToLower(telemetrySection(t))

	// Each entry: the state, and the phrases any honest documentation of it must contain.
	for _, state := range []struct {
		name    string
		phrases []string
	}{
		{"not_installed", []string{"nothing was queried"}},
		{"backend_down", []string{"did not answer the query"}},
		{"credential_rejected", []string{"credential was rejected"}},
		{"query_failed", []string{"reached the backend and failed"}},
		{"unknown", []string{"did not complete"}},
	} {
		covered := false
		for _, p := range state.phrases {
			if strings.Contains(section, strings.ToLower(p)) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("the Telemetry section documents no state matching %q (looked for %v). The "+
				"usage panes render this sentence to operators; a state that appears in no table has "+
				"no documented next step, so the operator's only move is to report the console as "+
				"broken", state.name, state.phrases)
		}
	}
}

// TestTelemetryDocRecordsTheConsoleRelaunchRule pins the operator-visible consequence of an
// idempotent relaunch: rendezvous.Ensure reuses a healthy running webui and the newly built binary
// exits with "nothing to do", so an operator who pulls and rebuilds keeps loading the OLD console
// — sees no Telemetry entry — and concludes the feature did not ship.
func TestTelemetryDocRecordsTheConsoleRelaunchRule(t *testing.T) {
	section := strings.ToLower(telemetrySection(t))

	// Both halves of the rule must appear TOGETHER. Searching the whole section for each half
	// independently passes on prose that means something else entirely: this section already tells
	// operators to restart AGENTS after `af telemetry on`, already says nothing restarts the
	// BACKEND, and already mentions re-running a step that "rebuilds the pair" of dashboard files.
	// The first draft of this test was satisfied by that unrelated vocabulary and never noticed the
	// console rule was missing.
	consequences := []string{
		"already running", "does not replace", "keeps serving", "still serving",
		"old build", "previous build", "earlier build",
	}

	for _, hit := range allIndexes(section, "rebuil") {
		window := section[max(0, hit-400):min(hit+400, len(section))]
		if !strings.Contains(window, "console") && !strings.Contains(window, "web") {
			continue // a rebuild of something else — dashboards, fixtures
		}
		for _, c := range consequences {
			if strings.Contains(window, c) {
				return
			}
		}
	}

	t.Errorf("the Telemetry section never tells an operator that a rebuilt console does not "+
		"replace one that is already running. A newly built binary finds the healthy endpoint the "+
		"previous process published, logs that the web UI is already running, and exits without "+
		"serving — so the browser keeps loading the OLD build and the panel this section documents "+
		"is simply absent, with no error anywhere to explain it. The note must pair the rebuild "+
		"with its consequence (one of %v) in the same passage; this section's existing restart "+
		"advice is about agents and the backend, and does not cover the console.", consequences)
}

// --- fable-implement Documentation lockstep (Root Cause A + B). Phase 5 (RED):
// USING_AGENTFACTORY.md has NOT been edited yet, so all three tests below fail for
// the predicted reason (the new/corrected wording is absent).

// TestTelemetryDocDocumentsSilentMetricSentences pins Step 4's two new
// differentiated sentences into the Telemetry section, replacing the flat
// "no series" phrasing the section documents today.
func TestTelemetryDocDocumentsSilentMetricSentences(t *testing.T) {
	section := strings.ToLower(telemetrySection(t))

	if !strings.Contains(section, "idle") || !strings.Contains(section, "history exists") {
		t.Error("the Telemetry section does not document the idle-with-history sentence Step 4 " +
			"introduces for a silent metric the series API proves has existed before")
	}
	if !strings.Contains(section, "never recorded") && !strings.Contains(section, "names have moved") {
		t.Error("the Telemetry section does not document the never-existed/renamed sentence Step 4 " +
			"introduces for a silent metric the series API proves has never existed")
	}
}

// TestTelemetryDocDocumentsTokenPreFlightGap pins Step 3's honest, named-gap
// wording (the pane now states the gap itself, in place of the raw backend 400).
func TestTelemetryDocDocumentsTokenPreFlightGap(t *testing.T) {
	section := strings.ToLower(telemetrySection(t))

	if !strings.Contains(section, "query_failed") {
		t.Error("the Telemetry section's known-gap row does not name the query_failed state the " +
			"honest pre-flight degradation now uses")
	}
	if strings.Contains(section, "search field not found") {
		t.Error("the Telemetry section still quotes the raw backend jargon " +
			"\"Search field not found\" — the pre-flight replaces it with a named-gap sentence in " +
			"the pane's own words, and the doc must not perpetuate the old wording")
	}
}

// TestTelemetryDocRecordsAutomaticRelaunchAsASecondRecoveryPath pins decisions.md
// D6: once Step 1 ships, the login shell is no longer "effectively only" the
// recovery path — af up and the watchdog also relaunch the backend autonomously.
// The doc's remedy rows must say so, not merely restate the pre-Step-1 claim.
func TestTelemetryDocRecordsAutomaticRelaunchAsASecondRecoveryPath(t *testing.T) {
	section := strings.ToLower(telemetrySection(t))

	if strings.Contains(section, "effectively only there") {
		t.Error("the Telemetry section still says the login-shell guard fires " +
			"\"effectively only there\" — Step 1 makes this false: af up (cold start) and the " +
			"watchdog (periodic) both also relaunch the backend autonomously now")
	}
	if !strings.Contains(section, "af up") || !strings.Contains(section, "watchdog") {
		t.Error("the Telemetry section's dead-backend remedy does not mention the new autonomous " +
			"recovery paths (af up, the watchdog) — bash -l should read as a manual fallback, not " +
			"the primary/sole remedy")
	}
}

func allIndexes(s, sub string) []int {
	var out []int
	for i := 0; ; {
		rel := strings.Index(s[i:], sub)
		if rel < 0 {
			return out
		}
		out = append(out, i+rel)
		i += rel + len(sub)
	}
}
