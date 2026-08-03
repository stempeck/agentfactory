package exec

import (
	"os"
	"strings"
	"testing"
)

// validate.go's header explains why this file re-implements af-core validation rules instead of
// importing them, and credits the duplication with a protection it does not provide:
//
//	"the duplication is the point (a compile error, not a lint, catches this module
//	 drifting from the root module's validation rules)"
//
// The compile error prevents an IMPORT. It detects no DRIFT. Nothing compares these mirrors to
// their sources, and nothing can: the extractability seal is exactly what makes a comparison
// impossible. Widen validAgentName root-side and this module keeps the old rule, rejecting values
// the CLI accepts — with both lanes green, because each tests its own copy.
//
// The file already words this honestly one mirror down, at reservedNames: "Nothing auto-syncs
// this mirror to config.go, so any addition there must be repeated here." The header is the
// outlier within its own file, and this pins the correction.
//
// Scope: this assertion is FILE-scoped on purpose. The same sentence appears in
// web/internal/formulas/store.go, and no review comment asked for that one — a module-wide
// assertion would drag an unrequested edit into this change.
func TestValidateMirrorHeaderDoesNotOverclaimDriftDetection(t *testing.T) {
	raw, err := os.ReadFile("validate.go")
	if err != nil {
		t.Fatalf("read validate.go: %v", err)
	}
	src := string(raw)

	// The header is the COMMENT BLOCK before the first declaration — comment lines only.
	//
	// Slicing the file at the first declaration is not enough: that slice still contains the Go
	// `import (` block, so a check for the word "import" is satisfied by the language keyword no
	// matter what the prose says. Half this assertion was vacuous until it read comments only.
	var comments []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "var ") || strings.HasPrefix(trimmed, "func ") {
			break
		}
		if strings.HasPrefix(trimmed, "//") {
			comments = append(comments, trimmed)
		}
	}
	head := strings.Join(comments, "\n")

	if strings.Contains(head, "catches this module drifting") {
		t.Error("validate.go's header still claims a compile error catches this module DRIFTING " +
			"from the root module's validation rules. It does not: the internal seal and the " +
			"separate go.mod make an IMPORT impossible, which is a different guarantee. No mechanism " +
			"compares these mirrors to their sources — TestValidateTelemetryInstance pins this " +
			"module's rule against hand-written cases, not against telemetry_where.go — so a rule " +
			"widened root-side leaves this copy rejecting values the CLI accepts, with both lanes " +
			"green. A header that credits an absent protection is worse than none, because it is " +
			"believed. The honest wording is already in this file at reservedNames")
	}

	// And the correction has to say what the mechanism DOES do, not merely drop the claim: a
	// header that explains nothing invites the next author to re-derive the wrong reason.
	mentionsImport := strings.Contains(head, "import") || strings.Contains(head, "IMPORT")
	mentionsManual := strings.Contains(head, "auto-sync") || strings.Contains(head, "by hand") ||
		strings.Contains(head, "manually") || strings.Contains(head, "repeated here") ||
		strings.Contains(head, "no mechanism") || strings.Contains(head, "detects no")
	if !mentionsImport || !mentionsManual {
		t.Error("validate.go's header must state what the compile error actually prevents (an " +
			"import) AND that keeping these mirrors in step with their sources is manual. Dropping " +
			"the false claim without replacing it leaves the file explaining a duplication with no " +
			"stated reason, which is how the over-claim got written in the first place")
	}
}
