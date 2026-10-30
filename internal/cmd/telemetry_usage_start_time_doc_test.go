package cmd

import (
	"regexp"
	"strings"
	"testing"
)

// TestUsingTelemetryDocUsageLabelsNameStartTime pins PR #747 T24 (L-13): `af telemetry usage --json`
// metric rows carry a `labels.start_time` key, and a consumer that does not know why will read two rows
// that differ only there as a duplicate.
func TestUsingTelemetryDocUsageLabelsNameStartTime(t *testing.T) {
	var usage string
	for _, s := range dcSections(dcReadRepoFile(t, "USING_TELEMETRY.md"), 3) {
		if strings.Contains(s.body, "\naf telemetry usage\n") {
			usage = s.body
			break
		}
	}
	if usage == "" {
		t.Fatal("USING_TELEMETRY.md has no `###` section holding the `af telemetry usage` example")
	}
	var para string
	for _, p := range strings.Split(usage, "\n\n") {
		if strings.Contains(p, "start_time") {
			para = dcFlat(p)
			break
		}
	}
	if para == "" {
		t.Fatal("no paragraph of USING_TELEMETRY.md's `af telemetry usage` section names the `start_time` label that `--json` metric rows carry (T24)")
	}
	if !strings.Contains(para, "labels") {
		t.Errorf("the `start_time` paragraph never says it is a key of the rows' `labels` map (T24): %q", para)
	}
	if !regexp.MustCompile(`(?i)separat|distinguish|differ only|tell apart|twin`).MatchString(para) {
		t.Errorf("the `start_time` paragraph never says the label separates otherwise-identical rows (T24): %q", para)
	}
	if regexp.MustCompile(`(?i)de-?dup`).MatchString(para) {
		t.Errorf("the `start_time` paragraph tells the reader to de-duplicate rows, but it should only document the label: %q", para)
	}
}
