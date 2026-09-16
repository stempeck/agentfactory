package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestSubagentHookMatchersCoverAgentToolName pins BROKEN-0 (PR #669): on Claude Code 2.1.236 the
// platform's sub-agent tool is named "Agent" (verified from a live session transcript's tool_use
// JSON), but the dispatch-admit PreToolUse hook and the subagent-observe PostToolUse hook both ship
// with a matcher of "Task". A hook matcher is a regexp tested against the tool name, so a matcher of
// "Task" never fires on an "Agent" launch — the gate and the observer are dead wiring in production.
// This is the control that catches that class: every generated settings.json must run those two hooks
// on a matcher that fires on BOTH the real tool name ("Agent") and the legacy one ("Task").
func TestSubagentHookMatchersCoverAgentToolName(t *testing.T) {
	cases := []struct {
		name     string
		roleType RoleType
	}{
		{"autonomous", Autonomous},
		{"interactive", Interactive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s): %v", tc.name, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}
			var parsed struct {
				Hooks map[string][]struct {
					Matcher string `json:"matcher"`
					Hooks   []struct {
						Command string `json:"command"`
					} `json:"hooks"`
				} `json:"hooks"`
			}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			wants := []struct {
				event   string
				command string
			}{
				{"PreToolUse", "af dispatch-admit"},
				{"PostToolUse", "af subagent-observe"},
			}
			for _, w := range wants {
				matcher, found := "", false
				for _, g := range parsed.Hooks[w.event] {
					for _, h := range g.Hooks {
						if strings.Contains(h.Command, w.command) {
							matcher, found = g.Matcher, true
						}
					}
				}
				if !found {
					t.Fatalf("%s settings.json has no %s hook running %q", tc.name, w.event, w.command)
				}
				re, err := regexp.Compile(matcher)
				if err != nil {
					t.Fatalf("%s %s matcher %q is not a valid regexp: %v", tc.name, w.event, matcher, err)
				}
				for _, tool := range []string{"Agent", "Task"} {
					if !re.MatchString(tool) {
						t.Errorf("%s %s matcher %q does not fire on the %q tool; the platform sub-agent tool "+
							"is named \"Agent\" (Claude Code 2.1.236) — a matcher that misses it is dead wiring",
							tc.name, w.event, matcher, tool)
					}
				}
			}
		})
	}
}

// TestSubagentStopHookRunsDispatchRetire is F2 (r3906601303) guardrail (b): every generated
// settings.json must wire an unconditional SubagentStop hook running `af dispatch-retire` — the child-
// completion signal #669 THREAD-1 rests on — for BOTH role types, so the leg that shipped as 0/43
// SubagentStop cannot ship green again. SubagentStop is not tool-scoped (its matcher is ""), so PRESENCE
// is the correct assertion, not the tool-name regexp the two hooks above ride. This is a GREEN
// regression LOCK: the embedded templates already carry the hook (feca6f53), so the negative control
// proves the presence check has teeth.
func TestSubagentStopHookRunsDispatchRetire(t *testing.T) {
	type hookCmd struct {
		Command string `json:"command"`
	}
	type hookGroup struct {
		Matcher string    `json:"matcher"`
		Hooks   []hookCmd `json:"hooks"`
	}
	type settings struct {
		Hooks map[string][]hookGroup `json:"hooks"`
	}
	// wired reports whether an UNCONDITIONAL (matcher "") SubagentStop hook runs af dispatch-retire.
	wired := func(s settings) bool {
		for _, g := range s.Hooks["SubagentStop"] {
			if g.Matcher != "" {
				continue
			}
			for _, h := range g.Hooks {
				if strings.Contains(h.Command, "af dispatch-retire") {
					return true
				}
			}
		}
		return false
	}

	for _, tc := range []struct {
		name     string
		roleType RoleType
	}{
		{"autonomous", Autonomous},
		{"interactive", Interactive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s): %v", tc.name, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}
			var s settings
			if err := json.Unmarshal(data, &s); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}
			if !wired(s) {
				t.Errorf("%s settings.json does not wire an unconditional SubagentStop hook running "+
					"`af dispatch-retire`; the #669 THREAD-1 completion signal would be dead wiring", tc.name)
			}
		})
	}

	t.Run("negative control: a settings doc without a SubagentStop block is detected", func(t *testing.T) {
		var s settings
		control := `{"hooks":{"PreToolUse":[{"matcher":"Task|Agent","hooks":[{"command":"af dispatch-admit"}]}]}}`
		if err := json.Unmarshal([]byte(control), &s); err != nil {
			t.Fatalf("control settings not valid JSON: %v", err)
		}
		if wired(s) {
			t.Error("the presence check reported a SubagentStop retire hook where there is none; it is toothless")
		}
	})
}
