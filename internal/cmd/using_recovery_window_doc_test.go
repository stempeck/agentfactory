package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// t5RecoverySentences reads USING_RECOVERY.md as prose: blockquote markers dropped, every run of
// whitespace one space, then split after each sentence-ending period. A sentence wrapped across
// lines of the manual is one string here.
func t5RecoverySentences(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "USING_RECOVERY.md"))
	if err != nil {
		t.Fatalf("reading USING_RECOVERY.md: %v", err)
	}
	return t5Sentences(string(data))
}

func t5Sentences(markdown string) []string {
	unquoted := regexp.MustCompile(`(?m)^>[ \t]?`).ReplaceAllString(markdown, "")
	flat := strings.Join(strings.Fields(unquoted), " ")
	return regexp.MustCompile(`[.!?]\s+`).Split(flat, -1)
}

// TestUsingRecoveryWindowReset holds the operator's exit from a stuck window figure (PR #756 T5): the
// record keeps an agent's old figure until that agent reports again, and a refused agent cannot
// report, so the manual must say what clears it.
func TestUsingRecoveryWindowReset(t *testing.T) {
	sentences := t5RecoverySentences(t)

	t.Run("removing the profile's record is the reset", func(t *testing.T) {
		for _, s := range sentences {
			lower := strings.ToLower(s)
			if strings.Contains(s, "<profile>.json") && strings.Contains(lower, "remov") && strings.Contains(lower, "reset") {
				return
			}
		}
		t.Error("no sentence of USING_RECOVERY.md says that removing `.agentfactory/statusline/windows/<profile>.json` " +
			"is the reset of a window figure the record still holds")
	})

	t.Run("the drift advisory is for agents with no launch fact", func(t *testing.T) {
		var drift []string
		for _, s := range sentences {
			if strings.Contains(s, "context-window drift") {
				drift = append(drift, s)
			}
		}
		if len(drift) == 0 {
			t.Fatal("USING_RECOVERY.md no longer names the `context-window drift` advisory")
		}
		for _, s := range drift {
			if !strings.Contains(s, "launch fact") && !strings.Contains(s, "claude --version") {
				t.Errorf("the `context-window drift` sentence does not say the advisory lists only agents with no "+
					"launch fact (started by an older af, or launched while `claude --version` returned nothing):\n%s", s)
			}
		}
	})
}
