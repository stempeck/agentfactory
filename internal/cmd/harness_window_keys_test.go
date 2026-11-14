package cmd

import (
	"reflect"
	"regexp"
	"slices"
	"sort"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// A harness release that starts reading a new window key would be probed without it while its
// sessions run with it. Every key the pinned binary names that could bear on the window is therefore
// sorted into config.WindowDecidingEnvKeys or this list by measurement, and an unsorted one fails the
// pin move (TestHarnessWindowRows_Regenerate) and the CI lane (TestShippedClaudeProfilesWindowAgreement).
// The binary reads window keys outside CLAUDE_CODE_ too, so the pattern takes any CLAUDE_ or DISABLE_
// name. It is anchored at word boundaries, so a constant that merely contains such a name stays out,
// and WINDOW must be followed by _ or end the name, so the operating-system WINDOWS keys stay out.
var harnessWindowKeyPattern = regexp.MustCompile(`\b(?:CLAUDE|DISABLE)_[A-Z0-9_]*(?:(?:CONTEXT|COMPACT|1M)[A-Z0-9_]*|WINDOW(?:_[A-Z0-9_]*)?)\b`)

// Measured on Claude Code 2.1.322 against the loopback probe, each exported alone for
// claude-fable-5-2, claude-opus-5-5, claude-haiku-5-5 and two ids it does not recognise, with and
// without a declaration of 1000000: none moves contextWindow.
var notWindowDecidingEnvKeys = []string{
	"CLAUDE_AFTER_LAST_COMPACT",
	"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE",
	"CLAUDE_CODE_AUTO_MODE_COMPACT_THREAD_RULE",
	"CLAUDE_CODE_AUTO_MODE_FULL_CONTEXT",
	"CLAUDE_CODE_AUTO_MODE_PRIOR_ASSISTANT_CONTEXT",
	"CLAUDE_CODE_COLD_COMPACT",
	"CLAUDE_CODE_DISABLE_PRECOMPACT_SKIP",
	"CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT",
	"CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS",
	"CLAUDE_CODE_MEMORY_CONTEXT_NO_WAIT",
	"CLAUDE_CODE_RESUME_TOLERATES_CONTEXT_APPENDS",
	"CLAUDE_CODE_RESUME_TOLERATES_CONTEXT_SEEDS",
	"CLAUDE_CODE_SLEEP_COMPACT",
	"CLAUDE_CODE_SLEEP_COMPACT_AFTER_MS",
	"CLAUDE_HELPER_CONTEXT",
	"DISABLE_AUTO_COMPACT",
}

func harnessWindowKeyViolations(bin []byte) []string {
	known := map[string]bool{}
	for _, k := range config.WindowDecidingEnvKeys {
		known[k] = true
	}
	for _, k := range notWindowDecidingEnvKeys {
		known[k] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range harnessWindowKeyPattern.FindAll(bin, -1) {
		k := string(m)
		if !known[k] && !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestHarnessWindowKeyViolations(t *testing.T) {
	if len(config.WindowDecidingEnvKeys) == 0 || len(notWindowDecidingEnvKeys) == 0 {
		t.Fatalf("both key lists must hold the measured keys: config.WindowDecidingEnvKeys %q, notWindowDecidingEnvKeys %q", config.WindowDecidingEnvKeys, notWindowDecidingEnvKeys)
	}
	for _, k := range notWindowDecidingEnvKeys {
		for _, w := range config.WindowDecidingEnvKeys {
			if k == w {
				t.Errorf("%s is in both config.WindowDecidingEnvKeys and notWindowDecidingEnvKeys", k)
			}
		}
	}
	for _, k := range append(append([]string(nil), config.WindowDecidingEnvKeys...), notWindowDecidingEnvKeys...) {
		if !harnessWindowKeyPattern.MatchString(k) {
			t.Errorf("listed key %s does not match the scan's pattern, so no scan can ever report or clear it", k)
		}
	}

	bin := []byte("\x00" + config.WindowDecidingEnvKeys[0] + "\x00process.env.CLAUDE_CODE_FOO_CONTEXT_BAR\x00" +
		notWindowDecidingEnvKeys[0] + "\x00CLAUDE_CODE_FOO_CONTEXT_BAR\x00")
	if got, want := harnessWindowKeyViolations(bin), []string{"CLAUDE_CODE_FOO_CONTEXT_BAR"}; !reflect.DeepEqual(got, want) {
		t.Errorf("harnessWindowKeyViolations = %q, want %q (each unknown key once, listed keys never)", got, want)
	}
	if got := harnessWindowKeyViolations([]byte("CLAUDE_CODE_EFFORT_LEVEL ANTHROPIC_MODEL CLAUDE_CODE_USE_BEDROCK")); len(got) != 0 {
		t.Errorf("bytes with no window-shaped key gave %q, want none", got)
	}

	widened := []byte("\x00process.env.DISABLE_FOO_COMPACT\x00process.env.CLAUDE_FOO_CONTEXT\x00" +
		"CLAUDE_CODE_FOO_WINDOW_BAR\x00process.env.DISABLE_BAR_WINDOW\x00")
	if got, want := harnessWindowKeyViolations(widened), []string{"CLAUDE_CODE_FOO_WINDOW_BAR", "CLAUDE_FOO_CONTEXT", "DISABLE_BAR_WINDOW", "DISABLE_FOO_COMPACT"}; !reflect.DeepEqual(got, want) {
		t.Errorf("harnessWindowKeyViolations = %q, want %q (a window key outside CLAUDE_CODE_, or named for WINDOW, is still reported)", got, want)
	}
	// The pinned binary carries DISABLE_LITERAL_CONTEXT_MODELING only as the tail of this brotli constant.
	if got := harnessWindowKeyViolations([]byte("\x00CLAUDE_CODE_DISABLE_WINDOWS_SHELL_LAUNCHER\x00CLAUDE_CODE_FORCE_WINDOWS_CREDMAN\x00" +
		"BROTLI_PARAM_DISABLE_LITERAL_CONTEXT_MODELING\x00")); len(got) != 0 {
		t.Errorf("operating-system WINDOWS keys and a mid-identifier DISABLE_ gave %q, want none", got)
	}
}

// The pinned binary groups these seven exports as one family.
var t3GroupedWindowFamily = []string{
	"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW",
	"CLAUDE_CODE_DISABLE_UNKNOWN_MODEL_WINDOW_ENFORCEMENT",
	"CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS",
	"CLAUDE_CODE_MAX_CONTEXT_TOKENS",
	"DISABLE_AUTO_COMPACT",
	"DISABLE_COMPACT",
}

func TestHarnessWindowKeyPattern_CoversTheGroupedFamily(t *testing.T) {
	for _, k := range t3GroupedWindowFamily {
		if got := harnessWindowKeyPattern.FindString(k); got != k {
			t.Errorf("the scan's pattern finds %q in %s, want the whole name: a key it cannot match is never reported", got, k)
		}
		if v := harnessWindowKeyViolations([]byte("\x00process.env." + k + "\x00")); len(v) != 0 {
			t.Errorf("%s is in the family the pinned binary groups but in neither key list: violations %q", k, v)
		}
	}
	for _, k := range []string{"CLAUDE_CODE_DISABLE_WINDOWS_SHELL_LAUNCHER", "CLAUDE_CODE_FORCE_WINDOWS_CREDMAN"} {
		if m := harnessWindowKeyPattern.FindString(k); m != "" {
			t.Errorf("the scan's pattern matches %q in %s, an operating-system key: WINDOW must not match WINDOWS", m, k)
		}
	}
	if !slices.Contains(config.WindowDecidingEnvKeys, "DISABLE_COMPACT") {
		t.Errorf("config.WindowDecidingEnvKeys = %q lacks DISABLE_COMPACT, which moves an unrecognised model's window on the pin", config.WindowDecidingEnvKeys)
	}
}
