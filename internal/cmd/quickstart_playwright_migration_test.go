package cmd

import (
	"regexp"
	"strings"
	"testing"
)

// TestQuickstart_NeverCallsClaudePlugin: quickstart installs no Claude Code plugin at user scope; the
// Playwright plugin comes from the playwright reference integration, bound per formula.
func TestQuickstart_NeverCallsClaudePlugin(t *testing.T) {
	qs := quickstartScriptContent(t)
	for _, banned := range []string{"claude plugin ", "install_playwright_plugin"} {
		if strings.Contains(qs, banned) {
			t.Errorf("quickstart.sh still contains %q", banned)
		}
	}
}

// TestQuickstart_KeepsInstallPlaywright: the headless-browser tier stays: the function, its single call,
// the codex comments that cite it, and the Phase 3 banner that followed the deleted plugin installer.
func TestQuickstart_KeepsInstallPlaywright(t *testing.T) {
	qs := quickstartScriptContent(t)
	if !strings.Contains(qs, "\ninstall_playwright() {\n") {
		t.Error("quickstart.sh lost install_playwright()")
	}
	if n := len(regexp.MustCompile(`(?m)^    install_playwright$`).FindAllString(qs, -1)); n != 1 {
		t.Errorf("quickstart.sh calls install_playwright %d times, want 1", n)
	}
	for _, keep := range []string{
		"    # Same probe -> sudo -n shape as install_playwright's npm-global-root writability check\n",
		"    # path here fails the run (decline: `exit 1`; install: `return 1` to a caller that exits), never install_playwright's degrade-safe `return 0`.\n",
		"#------------------------------------------------------------------------------\n# Phase 3: Configure\n#------------------------------------------------------------------------------\n",
		"    # Calibrate by rendering, not by version string: an installed-but-unrenderable\n",
	} {
		if !strings.Contains(qs, keep) {
			t.Errorf("quickstart.sh lost %q", keep)
		}
	}
}

// TestQuickstart_PinsPlaywrightNpmInstall: the playwright CLI is installed only through the
// digest-verified _npm_install_pinned, at the K6 pin and with the playwright-core overlay. The pin's
// value is TestQuickstartPinValues' alone (D-61), so no version literal appears here.
func TestQuickstart_PinsPlaywrightNpmInstall(t *testing.T) {
	qs := quickstartScriptContent(t)
	if unpinned := regexp.MustCompile(`(?m)npm install -g playwright( |$)`).FindAllString(qs, -1); len(unpinned) != 0 {
		t.Errorf("quickstart.sh has %d unpinned `npm install -g playwright`", len(unpinned))
	}
	if literal := regexp.MustCompile(`playwright@[0-9]`).FindAllString(qs, -1); len(literal) != 0 {
		t.Errorf("quickstart.sh names %d playwright@<version> literal(s); the version is PLAYWRIGHT_VERSION's alone", len(literal))
	}
	fn := topLevelShellFunction(qs, "install_playwright")
	if fn == "" {
		t.Fatal("could not extract install_playwright()")
	}
	const call = `_npm_install_pinned playwright "$PLAYWRIGHT_VERSION" "$PLAYWRIGHT_SHA512"`
	if n := strings.Count(qs, call); n != 1 {
		t.Errorf("quickstart.sh has %d `%s` calls, want exactly 1", n, call)
	}
	at := strings.Index(fn, call)
	if at < 0 {
		t.Fatalf("install_playwright() does not install through `%s`", call)
	}
	line := fn[at:]
	if end := regexp.MustCompile(`[^\\]\n`).FindStringIndex(line); end != nil {
		line = line[:end[0]+1]
	}
	if !strings.Contains(line, `playwright-core "$PLAYWRIGHT_CORE_SHA512"`) {
		t.Errorf("the playwright install does not overlay the verified playwright-core (`playwright-core \"$PLAYWRIGHT_CORE_SHA512\"`): %s", line)
	}
}
