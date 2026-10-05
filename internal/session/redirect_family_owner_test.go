package session

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestRedirectFamilyVarsOrderPinned pins Lift B's session half (spec L217-224, L322; AC 3; D13):
// the launch chokepoint's redirect family and shell-critical set are DERIVED from the config-owned
// lists, in the exact order the launch line emits its clears today, from a private copy, and session.go
// no longer carries its own literals for the moved names.
func TestRedirectFamilyVarsOrderPinned(t *testing.T) {
	// session.go:82-98's order before the lift; the launch-line bytes depend on it.
	wantOrder := []string{
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_FABLE_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
	}

	t.Run("redirect_order", func(t *testing.T) {
		if !slices.Equal(redirectFamilyVars, wantOrder) {
			t.Errorf("redirectFamilyVars order changed (launch-line bytes):\n got %v\nwant %v", redirectFamilyVars, wantOrder)
		}
		if len(config.RedirectFamilyEnvVars) == 0 {
			t.Fatal("config.RedirectFamilyEnvVars is empty: the redirect family is not config-owned yet")
		}
		if !slices.Equal(redirectFamilyVars, config.RedirectFamilyEnvVars) {
			t.Errorf("session.redirectFamilyVars is not derived from config.RedirectFamilyEnvVars:\n session %v\n config  %v", redirectFamilyVars, config.RedirectFamilyEnvVars)
		}
	})

	t.Run("shell_critical_set", func(t *testing.T) {
		var got []string
		for k, v := range shellCriticalVars {
			if v {
				got = append(got, k)
			}
		}
		sort.Strings(got)
		want := slices.Clone(config.ShellCriticalEnvVars)
		sort.Strings(want)
		if len(want) == 0 {
			t.Fatal("config.ShellCriticalEnvVars is empty: the shell-critical set is not config-owned yet")
		}
		if !slices.Equal(got, want) {
			t.Errorf("session.shellCriticalVars %v != config.ShellCriticalEnvVars %v", got, want)
		}
	})

	t.Run("no_aliasing", func(t *testing.T) {
		// D13: session holds slices.Clone(config.RedirectFamilyEnvVars), so a write through one can
		// never reorder or blank the other. Compared by address, never by mutation, so this subtest
		// cannot disturb any other test that reads either list.
		if len(redirectFamilyVars) == 0 || len(config.RedirectFamilyEnvVars) == 0 {
			t.Fatalf("both lists must be non-empty to compare backing arrays: session=%d config=%d", len(redirectFamilyVars), len(config.RedirectFamilyEnvVars))
		}
		if &redirectFamilyVars[0] == &config.RedirectFamilyEnvVars[0] {
			t.Error("session.redirectFamilyVars shares config.RedirectFamilyEnvVars' backing array; derive it with slices.Clone (D13)")
		}
	})

	t.Run("session_go_no_moved_literals", func(t *testing.T) {
		src, err := os.ReadFile("session.go")
		if err != nil {
			t.Fatalf("read session.go: %v", err)
		}
		re := regexp.MustCompile(`"(IFS|LD_PRELOAD|LD_AUDIT|LD_LIBRARY_PATH|ANTHROPIC_SMALL_FAST_MODEL|CLAUDE_CODE_SUBAGENT_MODEL)"`)
		if hits := re.FindAllString(string(src), -1); len(hits) != 0 {
			t.Errorf("session.go still spells %d moved literal(s) %v; they are owned by internal/config now (AC 3: one owner)", len(hits), hits)
		}
	})
}
