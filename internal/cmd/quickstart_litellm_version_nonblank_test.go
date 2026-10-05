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

// litellmVersionGuardExprRe isolates the pre-pip install guard's `[[ … ]]` test expression
// (quickstart.sh:1077) — the only `[[ ]]` in setup_litellm() that reads `litellm --version`.
var litellmVersionGuardExprRe = regexp.MustCompile(`\[\[[^\n]*litellm --version[^\n]*\]\]`)

// TestLitellmVersionProbeIsBlankLineSafe is T3 (red_predictions.md T3 / concern_tests.md §T3):
// every `litellm --version` probe must survive the tool's leading blank line — the pre-pip guard
// must NOT re-pip an already-pinned build, and no site may keep piping to `head -1` (which captures
// the blank line and makes the guard's mismatch test permanently true).
//
// RED at head: four sites (quickstart.sh:1077,1088,1329,1377) use `| head -1`, so the captured
// version is "" and the guard always enters the reinstall/`_gateway_stop` branch. GREEN once every
// site uses `2>/dev/null | grep -m1 .`.
func TestLitellmVersionProbeIsBlankLineSafe(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	// Source arm: no litellm-version site may pipe to `head -1`.
	if sites := litellmVersionHeadOneRe.FindAllString(setup, -1); len(sites) > 0 {
		t.Errorf("%d `litellm --version … | head -1` site(s) remain — these capture the leading blank line and yield \"\" (T3); use `grep -m1 .` instead:\n  %s",
			len(sites), strings.Join(sites, "\n  "))
	}

	// Behavioral arm: run the REAL guard expression against a litellm whose --version prints a
	// leading blank line then the pinned version. The guard must decide the build is up to date.
	expr := litellmVersionGuardExprRe.FindString(setup)
	if expr == "" {
		t.Fatal("could not find the litellm version-guard `[[ … ]]` expression in setup_litellm()")
	}

	bin := hermeticGatewayBinDir(t)
	writeCodexStub(t, bin, "litellm", `if [ "$1" = "--version" ]; then printf '\nLiteLLM: Current Version = 1.93.0\n'; fi
exit 0
`)

	guard := "set -uo pipefail\n" +
		"LITELLM_VERSION=\"1.93.0\"\n" +
		"if " + expr + "; then echo INSTALL; else echo SKIP; fi\n"
	cmd := exec.Command("bash", "-c", guard)
	cmd.Env = hermeticCodexEnv(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the litellm version guard failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "SKIP" {
		t.Errorf("with an already-pinned litellm (leading blank line, then version 1.93.0) the guard must SKIP the reinstall, got %q (T3: `head -1` captured the blank line)", got)
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
