package cmd

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// litellmVersionHeadOneRe matches a `litellm --version … | head -1` site. `litellm --version` emits
// a LEADING BLANK line, so `head -1` yields "" while `grep -m1 .` yields the real version line.
var litellmVersionHeadOneRe = regexp.MustCompile(`litellm --version[^\n]*\| head -1`)

// litellmVersionGuardExprRe matches a `[[ … ]]` test that reads `litellm --version`: the pre-pip
// version guard the hash-locked install replaced (E0: the installer decides, never a bash guard).
var litellmVersionGuardExprRe = regexp.MustCompile(`\[\[[^\n]*litellm --version[^\n]*\]\]`)

// litellmVersionSubstRe isolates each `$(litellm --version 2>/dev/null | grep -m1 . …)` command
// substitution, so the behavioural arm runs the script's own text rather than a copy of it.
var litellmVersionSubstRe = regexp.MustCompile(`\$\(litellm --version 2>/dev/null \| grep -m1 \.[^)\n]*\)`)

// TestLitellmVersionProbeIsBlankLineSafe is T3: every `litellm --version` probe must survive the
// tool's leading blank line. No site may pipe to `head -1` (which captures the blank line), and
// each surviving probe — the log_success report and the gateway-relaunch.sh heredoc's identity
// probe — must yield the version line. The pre-pip `[[ … litellm --version … ]]` guard must be gone.
func TestLitellmVersionProbeIsBlankLineSafe(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	if sites := litellmVersionHeadOneRe.FindAllString(setup, -1); len(sites) > 0 {
		t.Errorf("%d `litellm --version … | head -1` site(s) remain — these capture the leading blank line and yield \"\" (T3); use `grep -m1 .` instead:\n  %s",
			len(sites), strings.Join(sites, "\n  "))
	}

	if expr := litellmVersionGuardExprRe.FindString(setup); expr != "" {
		t.Errorf("setup_litellm() still gates the install on a version comparison (E0: the hash-locked install is unconditional): %s", expr)
	}

	substs := litellmVersionSubstRe.FindAllString(setup, -1)
	if len(substs) < 2 {
		t.Fatalf("found %d `$(litellm --version 2>/dev/null | grep -m1 .…)` probes in setup_litellm(), want >= 2 (the log_success report and the relaunch heredoc's identity probe)", len(substs))
	}
	bin := hermeticConvergeBinDir(t)
	writeConvergeStub(t, bin, "litellm", `if [ "$1" = "--version" ]; then printf '\nLiteLLM: Current Version = 1.93.0\n'; fi
exit 0
`)
	env := hermeticConvergeEnv(bin, t.TempDir(), t.TempDir())
	for _, subst := range substs {
		cmd := exec.Command("bash", "-c", "set -uo pipefail\nv=\""+subst+"\"\nprintf '%s' \"$v\"\n")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("running %s failed: %v\n%s", subst, err, out)
		}
		if got := string(out); got != "LiteLLM: Current Version = 1.93.0" {
			t.Errorf("%s yielded %q against a litellm whose --version starts with a blank line, want the version line", subst, got)
		}
	}
}

// TestLitellmVersionProbeSourceIsNonEmpty guards that the source arm actually examined the
// setup_litellm() function (not an empty string), so a future extraction regression cannot make T3
// pass vacuously.
func TestLitellmVersionProbeSourceIsNonEmpty(t *testing.T) {
	setup := setupLitellmSource(t, findModuleRoot(t))
	if !strings.Contains(setup, "litellm --version") {
		t.Fatalf("setup_litellm() carries no `litellm --version` probe at all — the T3 source scan would be vacuous")
	}
}
