package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestADR029PredictionAndObservation pins the decision record for #754: a declared window is a
// prediction, a host-applied window is an observation, and neither the record nor an operator pin
// is an authority. docs/architecture ships to the OSS repository and .designs/ does not, so a design
// path is honest only as text that says so (ADR-027 and ADR-028's convention).
func TestADR029PredictionAndObservation(t *testing.T) {
	adrDir := filepath.Join(findModuleRoot(t), "docs", "architecture", "adrs")

	t.Run("exactly one ADR-029, Proposed, naming prediction, observation and not an authority", func(t *testing.T) {
		matches, err := filepath.Glob(filepath.Join(adrDir, "ADR-029-*.md"))
		if err != nil {
			t.Fatalf("glob ADR-029: %v", err)
		}
		if len(matches) != 1 {
			t.Fatalf("want exactly one docs/architecture/adrs/ADR-029-*.md, got %d: %v", len(matches), matches)
		}
		data, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatalf("read %s: %v", matches[0], err)
		}
		raw := string(data)
		if !regexp.MustCompile(`(?m)^\*\*Status:\*\* Proposed`).MatchString(raw) {
			t.Error("ADR-029 must carry a `**Status:** Proposed` line")
		}
		lower := strings.ToLower(raw)
		for _, want := range []string{"prediction", "observation", "not an authority"} {
			if !strings.Contains(lower, want) {
				t.Errorf("ADR-029 never says %q", want)
			}
		}

		// A paragraph is the unit a citation sits in; the disclaimer may wrap across its lines.
		const disclaimer = "the oss repository does not carry `.designs/`"
		for _, para := range regexp.MustCompile(`\n[ \t]*\n`).Split(raw, -1) {
			if !strings.Contains(para, ".designs/") {
				continue
			}
			flat := strings.ToLower(strings.Join(strings.Fields(para), " "))
			if !strings.Contains(flat, disclaimer) {
				t.Errorf("ADR-029 cites .designs/ without saying, beside it, that the OSS repository does not carry `.designs/`:\n%s", para)
			}
		}
	})

	t.Run("the ADR index lists ADR-029 as Proposed", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
		if err != nil {
			t.Fatalf("read adrs/README.md: %v", err)
		}
		var row string
		for _, l := range strings.Split(string(data), "\n") {
			if strings.Contains(l, "](ADR-029-") {
				row = l
				break
			}
		}
		if row == "" {
			t.Fatal("docs/architecture/adrs/README.md has no `](ADR-029-` index row")
		}
		if !strings.Contains(row, "Proposed") {
			t.Errorf("the ADR-029 index row does not say Proposed: %q", row)
		}
	})

	t.Run("the ADR-029 index row anchors the window record", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
		if err != nil {
			t.Fatalf("read adrs/README.md: %v", err)
		}
		var row string
		for _, l := range strings.Split(string(data), "\n") {
			if strings.Contains(l, "](ADR-029-") {
				row = l
				break
			}
		}
		if row == "" {
			t.Fatal("docs/architecture/adrs/README.md has no `](ADR-029-` index row")
		}
		for _, anchor := range []string{"internal/cmd/window_facts.go", "internal/config/models.go"} {
			if !strings.Contains(row, anchor) {
				t.Errorf("the ADR-029 index row anchors a window ADR at memory files only; it lacks %s: %q", anchor, row)
			}
		}
	})
}
