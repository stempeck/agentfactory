package cmd

import (
	"regexp"
	"strings"
	"testing"
)

// TestBackendCommentsCiteNoQuickstartLineNumbers pins PR #747 BODY-7 (L-12): a `quickstart.sh:<N>` cite
// in these comments goes stale on every quickstart.sh edit and nothing checks it, so the comments name the
// function they mean instead.
func TestBackendCommentsCiteNoQuickstartLineNumbers(t *testing.T) {
	lineCite := regexp.MustCompile(`quickstart\.sh:\d`)
	for _, file := range []string{"gateway_backend.go", "telemetry_backend.go"} {
		t.Run(file, func(t *testing.T) {
			for i, line := range strings.Split(dcReadRepoFile(t, "internal/cmd/"+file), "\n") {
				if lineCite.MatchString(line) {
					t.Errorf("internal/cmd/%s:%d cites quickstart.sh by line number; name the function instead (BODY-7): %s", file, i+1, strings.TrimSpace(line))
				}
				if strings.Contains(line, "concern_locus.md") {
					t.Errorf("internal/cmd/%s:%d cites concern_locus.md, an agent work file the repo does not carry (BODY-7)", file, i+1)
				}
			}
		})
	}

	t.Run("the marker comment keeps its test-name cite", func(t *testing.T) {
		if !strings.Contains(dcReadRepoFile(t, "internal/cmd/gateway_backend.go"), "pinned by TestGatewayReconcileMarker_FieldNamesMatchWriterShape") {
			t.Error("gateway_backend.go lost `pinned by TestGatewayReconcileMarker_FieldNamesMatchWriterShape`, the one cite a test keeps true")
		}
	})
}
