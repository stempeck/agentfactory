package cmd

import (
	"strings"
	"testing"
)

// TestConfigModelsSet_UnquotedNumber_NamesQuotingRule pins PR #605 T-F5: the JSON-string quoting
// hint lives only on the file-load path (LoadModelsConfig), but `af config models set` decodes
// stdin via decodeJSONStdin and never calls it — so the primary entry point where the unquoted
// `220000` trap actually happens returns a bare `cannot unmarshal number … type string` with no
// guidance. The set path must name the quoting rule for a number→string type mismatch.
func TestConfigModelsSet_UnquotedNumber_NamesQuotingRule(t *testing.T) {
	setupConfigFactory(t)
	unquoted := `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":220000}}}`
	_, err := runConfigSet(t, runConfigModelsSet, unquoted)
	if err == nil {
		t.Fatal("an unquoted numeric profile value must be rejected by `af config models set`")
	}
	for _, want := range []string{"quoted", "string"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the set-path error must name the JSON-string quoting rule (missing %q); got: %v", want, err)
		}
	}
}
