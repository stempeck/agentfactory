package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTelemetryDocVocabulary is the doc-side twin of TestTelemetryViewsUseOperatorVocabulary:
// the same "what the operator reads says what they want to know, not how the data arrived"
// gate, applied to the USING_AGENTFACTORY.md Telemetry section instead of the dashboards.
//
// Fenced blocks are exempt from the vocabulary scan for the same reason a query body is: the
// shipped config field really is named otlp_http_path_traces and the shipped `af telemetry
// status` output really does say "exporter". Quoting shipped strings is honest; writing them
// in prose is mechanism leaking onto the operator surface.
func TestTelemetryDocVocabulary(t *testing.T) {
	docPath := filepath.Join(findModuleRoot(t), "USING_AGENTFACTORY.md")
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}

	section, ok := telemetryDocSection(string(data))
	if !ok {
		t.Fatalf("USING_AGENTFACTORY.md has no '## Telemetry' section (issue #329 P6a). Every assertion below is an absence check, so without this the test would pass on a doc that says nothing at all")
	}

	// The union of the two ban lists issue #329 P6a carries: the acceptance criterion's shell
	// scan and the design's own list. Banning the union is what keeps the two from disagreeing.
	forbidden := []string{
		"otlp",
		"resourcespans",
		"resource attribute",
		"exporter",
		"signal",
		"half-open",
		"traceid",
		"spanid",
	}
	for i, line := range blankFencedBlocks(section) {
		lowered := strings.ToLower(line)
		for _, bad := range forbidden {
			if strings.Contains(lowered, bad) {
				t.Errorf("## Telemetry line %d contains the mechanism term %q: %q. "+
					"Operator prose says what telemetry gives you and what it costs you; "+
					"mechanism vocabulary belongs inside a fenced block quoting shipped output",
					i+1, bad, strings.TrimSpace(line))
			}
		}
	}

	// Fences are deliberately NOT stripped here: `endpoint` and `headers` are field names an
	// operator meets in the documented telemetry.json block, not in prose.
	whole := strings.Join(section, "\n")
	lowered := strings.ToLower(whole)
	for _, concept := range []string{
		"af telemetry on",
		"endpoint",
		"headers",
		"af telemetry report",
		"af telemetry status",
		"no-telemetry",
		"privacy",
	} {
		if !strings.Contains(lowered, strings.ToLower(concept)) {
			t.Errorf("## Telemetry never mentions %q — every operator-facing telemetry concept must be explained where the operator will look for it", concept)
		}
	}

	for _, gate := range []string{
		"OTEL_LOG_USER_PROMPTS",
		"OTEL_LOG_ASSISTANT_RESPONSES",
		"OTEL_LOG_TOOL_DETAILS",
		"OTEL_LOG_TOOL_CONTENT",
		"OTEL_LOG_RAW_API_BODIES",
	} {
		if !strings.Contains(whole, gate) {
			t.Errorf("## Telemetry does not name the content gate %s. All five are disclosed by name so an operator can verify for themselves that af never sets them", gate)
		}
	}

	// `grep -o 'OTEL_LOG_[A-Z_]*' | sort -u | wc -l` is how the phase's acceptance criterion
	// counts the gates, and a wildcard adds a sixth token that makes the count read 6.
	if strings.Contains(whole, "OTEL_LOG_*") {
		t.Errorf("## Telemetry writes the wildcard OTEL_LOG_* — name the five variables instead, or say \"the five content-gate variables\"")
	}
}

// telemetryDocSection returns the lines of the '## Telemetry' section, from its heading up to
// the next top-level heading.
func telemetryDocSection(doc string) ([]string, bool) {
	var section []string
	found := false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "## ") {
			if found {
				break
			}
			found = strings.TrimSpace(line) == "## Telemetry"
			if !found {
				continue
			}
		}
		if found {
			section = append(section, line)
		}
	}
	return section, found
}

// blankFencedBlocks empties fenced code blocks, including their fence lines, keeping every
// other line at its original index so a violation can be reported where the author will find
// it. Fences are matched unindented, which is also what the phase's acceptance-criterion awk
// does — an indented fence is scanned as prose by both, so the two can never disagree.
func blankFencedBlocks(lines []string) []string {
	out := make([]string, len(lines))
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if !inFence {
			out[i] = line
		}
	}
	return out
}
