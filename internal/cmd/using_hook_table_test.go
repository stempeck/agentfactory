//go:build !integration

package cmd

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/claude"
)

// sessionStartCommands returns the SessionStart hook commands a settings template installs, in the
// order the settings declare them — not the order they run in, which is unordered because matching
// hooks run in parallel (ADR-023 E6). Each entry's `export PATH=… && ` preamble is stripped so what
// is left is the verb an operator reads in the manual.
func sessionStartCommands(t *testing.T, settings []byte, source string) []string {
	t.Helper()

	var cmds []string
	for _, group := range hookCommandGroups(t, settings, "SessionStart", source) {
		for _, cmd := range group {
			if _, after, found := strings.Cut(cmd, " && "); found {
				cmd = after
			}
			cmds = append(cmds, strings.TrimSpace(cmd))
		}
	}
	return cmds
}

// TestUsingHookTableMatchesEmbeddedSettings holds the operator manual's SessionStart row against the
// JSON the factory actually installs (#675 AC-6). Phase 2 split one `&&`-chained entry into three
// independent ones, one per context writer, and gated the identity render out of hook mode; the row
// went on describing a single chain that injects identity, because a markdown table has no compiler.
//
// The order assertion alone would be vacuous — the pre-#675 row already named all three commands in
// the right order, for the wrong reasons. The absence assertions are what give this test teeth.
func TestUsingHookTableMatchesEmbeddedSettings(t *testing.T) {
	want := sessionStartCommands(t, canonicalSettings(t, claude.Autonomous), "settings-autonomous.json")
	got := sessionStartCommands(t, canonicalSettings(t, claude.Interactive), "settings-interactive.json")

	if len(want) != len(got) {
		t.Fatalf("the two embedded settings templates disagree on SessionStart: autonomous %v, interactive %v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("the two embedded settings templates disagree on SessionStart entry %d: autonomous %q, interactive %q",
				i, want[i], got[i])
		}
	}
	if len(want) != 3 {
		t.Fatalf("SessionStart installs %d hook entries, not the three independent context writers #675 shipped: %v",
			len(want), want)
	}

	row := tableRowFor(readUsingAgentfactoryDoc(t), "SessionStart")
	if row == "" {
		t.Fatal("the hook table has no `SessionStart` row")
	}

	at := -1
	for _, cmd := range want {
		i := strings.Index(row, cmd)
		if i < 0 {
			t.Errorf("the SessionStart row does not name %q, which the embedded settings install:\n%s", cmd, row)
			continue
		}
		if i < at {
			t.Errorf("the SessionStart row names %q out of the order the settings declare it in:\n%s", cmd, row)
		}
		at = i
	}

	// Each writer gets its own entry and both role types get all three, so a row that scopes mail to
	// autonomous agents or credits the hook prime with identity is describing the superseded design.
	for _, gone := range []string{
		"Autonomous agents also run",
		"inject identity",
	} {
		if strings.Contains(row, gone) {
			t.Errorf("the SessionStart row still carries the pre-#675 claim %q:\n%s", gone, row)
		}
	}
}
