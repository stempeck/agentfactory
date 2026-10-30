package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// topLevelShellFunction returns the body of a function defined at column 0. extractShellFunction
// takes the first substring match of `name() {`, so `install_claude() {` would also match inside a
// nested `_install_claude() {` defined earlier in the file.
func topLevelShellFunction(content, name string) string {
	marker := name + "() {"
	if strings.HasPrefix(content, marker) {
		return extractShellFunction(content, name)
	}
	i := strings.Index(content, "\n"+marker)
	if i == -1 {
		return ""
	}
	return extractShellFunction(content[i+1:], name)
}

func readQuickstart(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	return string(data)
}

// quickstartPin reads a K6 constant's literal from quickstart.sh. PATH-stub tests take every pin
// through here: TestQuickstartPinValues is the only test allowed to know a pin's absolute value (D-61).
func quickstartPin(t *testing.T, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `="([^"$]+)"$`)
	m := re.FindAllStringSubmatch(readQuickstart(t), -1)
	if len(m) != 1 {
		t.Fatalf("quickstart.sh must declare %s exactly once as a quoted literal; found %d", name, len(m))
	}
	return m[0][1]
}

var configAssignRe = regexp.MustCompile("^[A-Z][A-Z0-9_]*=(\"[^\"`]*\"|[A-Za-z0-9_.-]*|\\(\\))$")

// quickstartConfigBlock returns the literal assignments of quickstart.sh's Configuration section, so a
// harness runs under the script's real globals instead of hand-seeded copies that a rename would
// silently orphan. SCRIPT_DIR and WORKSPACE_DIR are left to the harness: SCRIPT_DIR reads
// ${BASH_SOURCE[0]}, which is unbound under `bash -c` + `set -u`.
func quickstartConfigBlock(t *testing.T, content string) string {
	t.Helper()
	start := strings.Index(content, "\n# Configuration\n")
	end := strings.Index(content, "\n# Logging Helpers\n")
	if start == -1 || end == -1 || end < start {
		t.Fatal("quickstart.sh lost its `# Configuration` / `# Logging Helpers` banners; the harness reads the globals between them")
	}
	var b strings.Builder
	for _, line := range strings.Split(content[start:end], "\n") {
		if !configAssignRe.MatchString(line) {
			continue
		}
		if strings.HasPrefix(line, "SCRIPT_DIR=") || strings.HasPrefix(line, "WORKSPACE_DIR=") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if !strings.Contains(b.String(), "CHECK_ONLY=") {
		t.Fatal("quickstart.sh's Configuration block yielded no CHECK_ONLY assignment; the parse is broken")
	}
	return b.String()
}

// convergeHarness builds a `set -euo pipefail` script holding quickstart.sh's Configuration globals
// and the named functions (top-level definition first, nested definition otherwise).
func convergeHarness(t *testing.T, scriptDir string, names ...string) string {
	t.Helper()
	content := readQuickstart(t)
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("RED='' GREEN='' YELLOW='' BLUE='' NC=''\n")
	b.WriteString(quickstartConfigBlock(t, content))
	b.WriteString("SCRIPT_DIR='" + scriptDir + "'\n")
	b.WriteString("export PATH=\"$HOME/.local/bin:$PATH\"\n")
	for _, n := range names {
		fn := topLevelShellFunction(content, n)
		if fn == "" {
			fn = extractShellFunction(content, n)
		}
		if fn == "" {
			t.Fatalf("could not extract %s() from quickstart.sh", n)
		}
		b.WriteString(fn)
		b.WriteString("\n")
	}
	return b.String()
}

// hermeticConvergeBinDir links only read-only, portable tools. Digest, archive, privilege, package and
// network tools are never linked: each test stubs exactly the ones it exercises, so a host binary can
// never answer for a stub (and a real apt-get, sudo or curl can never run).
func hermeticConvergeBinDir(t *testing.T, extraReal ...string) string {
	t.Helper()
	dir := t.TempDir()
	tools := append([]string{"grep", "sleep", "tail", "jq", "awk", "cat", "sed", "tr", "head", "cut", "wc",
		"sort", "uniq", "mkdir", "rm", "mv", "ln", "chmod", "mktemp", "date", "dirname", "basename",
		"readlink", "env", "touch", "ls", "find", "cmp"}, extraReal...)
	for _, tool := range tools {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("resolving real %s for the hermetic converge bin dir: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("symlinking %s: %v", tool, err)
		}
	}
	return dir
}

// writeConvergeStub writes (or replaces a linked tool with) a #!/bin/sh stub.
func writeConvergeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	_ = os.Remove(path)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing %s stub: %v", name, err)
	}
}

// hermeticConvergeEnv is built from scratch, never from os.Environ(): agent sessions export AF_ROOT
// as the LIVE factory, and the image exports GO_SHA256_*/GO_VERSION; either leaking in would point
// a harness at real state. Later entries in extra override earlier ones (os/exec keeps the last).
func hermeticConvergeEnv(bin, home, afRoot string, extra ...string) []string {
	tmp := filepath.Join(home, ".tmp")
	_ = os.MkdirAll(tmp, 0o755)
	return append([]string{
		"PATH=" + bin,
		"HOME=" + home,
		"AF_ROOT=" + afRoot,
		"TMPDIR=" + tmp,
		"LANG=C",
		"LC_ALL=C",
	}, extra...)
}

// runConvergeHarness runs call after harness with the `|| rc=$?` capture, so a non-zero return is
// observed instead of aborting under errexit.
func runConvergeHarness(t *testing.T, harness, call string, env []string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", harness+"rc=0\n"+call+" || rc=$?\nexit \"$rc\"\n")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		t.Fatalf("running harness: %v\n%s", err, out)
	}
	return "", -1
}

// ---------------------------------------------------------------------------------------------
// Converge pinning tests (File 22). Every pin is read through quickstartPin (D-61); every dynamic
// case runs in a from-scratch env whose PATH holds stubs only (D22).
// ---------------------------------------------------------------------------------------------

// cvCode blanks full-line comments so an anchor or ban can never be satisfied (or tripped) by prose.
func cvCode(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

type cvAnchor struct {
	label string
	re    *regexp.Regexp
}

func cvLit(s string) cvAnchor { return cvAnchor{s, regexp.MustCompile(regexp.QuoteMeta(s))} }

func cvLine(label, re string) cvAnchor { return cvAnchor{label, regexp.MustCompile("(?m)" + re)} }

// cvFirstOccurrencesAscend requires each anchor's FIRST occurrence to follow the previous anchor's.
func cvFirstOccurrencesAscend(t *testing.T, where, body string, anchors []cvAnchor) {
	t.Helper()
	prev, prevLabel := -1, ""
	for _, a := range anchors {
		loc := a.re.FindStringIndex(body)
		if loc == nil {
			t.Errorf("%s: %q is missing", where, a.label)
			continue
		}
		if prev >= 0 && loc[0] <= prev {
			t.Errorf("%s: %q (offset %d) must come after %q (offset %d)", where, a.label, loc[0], prevLabel, prev)
		}
		prev, prevLabel = loc[0], a.label
	}
}

// cvSequence requires the anchors in order, each searched after the previous match (nested helper
// definitions may legitimately mention a later anchor's text earlier in the body).
func cvSequence(t *testing.T, where, body string, anchors []cvAnchor) {
	t.Helper()
	pos := 0
	for i, a := range anchors {
		loc := a.re.FindStringIndex(body[pos:])
		if loc == nil {
			if i == 0 {
				t.Errorf("%s: %q is missing", where, a.label)
			} else {
				t.Errorf("%s: no %q after %q", where, a.label, anchors[i-1].label)
			}
			return
		}
		pos += loc[1]
	}
}

func cvHasLine(out, line string) bool {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `$`).MatchString(out)
}

var cvANSI = regexp.MustCompile("\x1b\\[[0-9;]*m")

type cvRow struct{ name, pin, installed, integrity, result string }

func cvParseRows(raw string) []cvRow {
	var rows []cvRow
	for _, l := range strings.Split(raw, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 5 {
			continue
		}
		rows = append(rows, cvRow{f[0], f[1], f[2], f[3], f[4]})
	}
	return rows
}

func cvRowsNamed(rows []cvRow, name string) []cvRow {
	var out []cvRow
	for _, r := range rows {
		if r.name == name {
			out = append(out, r)
		}
	}
	return out
}

func cvExpectRow(t *testing.T, rows []cvRow, name, pin, installed, result string) {
	t.Helper()
	got := cvRowsNamed(rows, name)
	if len(got) != 1 {
		t.Errorf("CONVERGE_ROWS must hold exactly one %s row; got %d in %+v", name, len(got), rows)
		return
	}
	r := got[0]
	if r.pin != pin || r.installed != installed || r.result != result {
		t.Errorf("%s row = pin %q installed %q result %q; want pin %q installed %q result %q",
			name, r.pin, r.installed, r.result, pin, installed, result)
	}
}

type cvRun struct {
	out, ledger, log string
	rc               int
	rows             []cvRow
}

// cvSandbox is one hermetic run: its own HOME and AF_ROOT, a stub-only PATH, a call log every stub
// appends to, and the ledger/row store dumped by an EXIT trap (so an `exit` inside the END block
// still reports them).
type cvSandbox struct {
	t                                     *testing.T
	base, home, bin, afRoot, src, logPath string
	ledgerPath, rowsPath                  string
}

func newCvSandbox(t *testing.T) *cvSandbox {
	t.Helper()
	base := t.TempDir()
	s := &cvSandbox{
		t:          t,
		base:       base,
		home:       filepath.Join(base, "home"),
		afRoot:     filepath.Join(base, "afroot"),
		src:        filepath.Join(base, "src"),
		logPath:    filepath.Join(base, "calls.log"),
		ledgerPath: filepath.Join(base, "ledger.out"),
		rowsPath:   filepath.Join(base, "rows.out"),
		bin:        hermeticConvergeBinDir(t, "xargs", "od", "comm", "true"),
	}
	for _, d := range []string{s.home, s.afRoot, s.src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(s.logPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *cvSandbox) env(extra ...string) []string {
	return hermeticConvergeEnv(s.bin, s.home, s.afRoot, append([]string{
		"CV_LOG=" + s.logPath, "CV_LEDGER=" + s.ledgerPath, "CV_ROWS=" + s.rowsPath}, extra...)...)
}

func (s *cvSandbox) stub(name, body string) { writeConvergeStub(s.t, s.bin, name, body) }

func (s *cvSandbox) writeFile(rel, content string, mode os.FileMode) string {
	s.t.Helper()
	p := rel
	if !filepath.IsAbs(p) {
		p = filepath.Join(s.home, rel)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		s.t.Fatal(err)
	}
	return p
}

const cvDumpTrap = `trap 'printf "%s" "${CONVERGE_FAILURES-}" >"$CV_LEDGER"; printf "%s" "${CONVERGE_ROWS-}" >"$CV_ROWS"' EXIT` + "\n"

func (s *cvSandbox) run(harness, call string, extra ...string) cvRun {
	s.t.Helper()
	out, rc := runConvergeHarness(s.t, harness+cvDumpTrap, call, s.env(extra...))
	ledger, _ := os.ReadFile(s.ledgerPath)
	rows, _ := os.ReadFile(s.rowsPath)
	log, _ := os.ReadFile(s.logPath)
	return cvRun{out: out, rc: rc, ledger: string(ledger), log: string(log), rows: cvParseRows(string(rows))}
}

func cvCountLines(text, prefix string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------------------------

func TestClaudeCodeVersionFloor(t *testing.T) {
	v := quickstartPin(t, "CLAUDE_CODE_VERSION")
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(v) {
		t.Fatalf("CLAUDE_CODE_VERSION=%q must be a numeric x.y.z release, never a channel word: the download URL and its sha256 pin name exactly one build", v)
	}
	floor := []int{2, 1, 304}
	for i, part := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(part)
		if n > floor[i] {
			return
		}
		if n < floor[i] {
			t.Fatalf("CLAUDE_CODE_VERSION=%s is below 2.1.304, the first build whose arm64 binary no longer embeds 'emSign ECC Root CA - G3' (D30)", v)
		}
	}
}

// ---------------------------------------------------------------------------------------------

var cvDiscoverInstallers = regexp.MustCompile(`(?m)^[[:space:]]*((?:install_|setup_|_ensure_)[A-Za-z_]*)\(\) *\{`)

var cvAnyRoute = regexp.MustCompile(`_converge [a-z]+ "\$[A-Z_]+_VERSION"|_pip_install_locked "\$SCRIPT_DIR/py/|nodejs="\$NODE_APT_VERSION"`)

var cvExpectedRoute = map[string]string{
	"install_af":         `_converge go "$GO_TOOLCHAIN_VERSION"`,
	"install_claude":     `_converge claude "$CLAUDE_CODE_VERSION"`,
	"install_gh":         `_converge gh "$GH_VERSION"`,
	"install_playwright": `_converge playwright "$PLAYWRIGHT_VERSION"`,
	"_ensure_codex_cli":  `_converge codex "$CODEX_VERSION"`,
	"setup_telemetry":    `_converge openobserve "$OPENOBSERVE_VERSION"`,
	"setup_litellm":      `_pip_install_locked "$SCRIPT_DIR/py/litellm-requirements.txt"`,
}

var cvExemptions = map[string]string{
	"_ensure_codex_session":       "verifies the ChatGPT-subscription session; installs nothing",
	"_ensure_mode_yaml":           "seeds the gateway mode YAML; installs nothing",
	"_ensure_codex_compat_wiring": "wires the gateway's codex callback; installs nothing",
	"_ensure_nodesource_list":     "rewrites the NodeSource source list; the pin is the Node apt line",
}

var cvGuardRe = regexp.MustCompile(`--version|pip3? (show|list|freeze)|python3?[.0-9]* -c[^\n]*import|version_gte|\[\[?[^]]*_VERSION|\*"\$[A-Z_]*VERSION"\*`)

var cvBash32Avoid = []*regexp.Regexp{
	regexp.MustCompile(`declare -[Ang]`),
	regexp.MustCompile(`local -n`),
	regexp.MustCompile(`\bmapfile\b`),
	regexp.MustCompile(`\breadarray\b`),
	regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(,,|\^\^)\}`),
	regexp.MustCompile(`wait -n`),
	regexp.MustCompile(`\|&`),
	regexp.MustCompile(`&>>`),
	regexp.MustCompile(`\bcoproc\b`),
	regexp.MustCompile(`\[\[ -v`),
	regexp.MustCompile(`%\([^)]*\)T`),
	regexp.MustCompile(`EPOCHSECONDS`),
	regexp.MustCompile(`(?m)(^|[;&|(]|\bthen|\bdo|\belse|\$\()[ \t]*timeout[ \t]`),
	regexp.MustCompile(`tail --pid`),
	regexp.MustCompile(`(?m)(^|[;&|(]|\bthen|\bdo|\belse|\$\()[ \t]*eval[ \t]`),
}

// cvSimulate marks the --check form of an apt line: `apt-get -s` converges nothing, so E0 does not apply.
var cvSimulate = regexp.MustCompile(`(^|[ \t])-s([ \t]|$)`)

var cvConvergeOrTrue = regexp.MustCompile(`^_converge [^;&|]*\|\|[ \t]*true\b`)

const cvCheckGhGolden = `check_gh() {
    log_step "Checking GitHub CLI installation"

    if ! command_exists gh; then
        log_error "GitHub CLI (gh) is not installed"
        return 1
    fi

    log_success "gh installed: $(gh --version | head -1)"
    return 0
}`

func TestQuickstartOneConvergence(t *testing.T) {
	content := readQuickstart(t)
	code := cvCode(content)

	discovered := map[string]bool{}
	for _, m := range cvDiscoverInstallers.FindAllStringSubmatch(content, -1) {
		discovered[m[1]] = true
	}
	var names []string
	for n := range discovered {
		names = append(names, n)
	}
	sort.Strings(names)

	t.Run("every discovered installer converges through a route", func(t *testing.T) {
		for want := range cvExpectedRoute {
			if !discovered[want] {
				t.Errorf("%s() is not defined; it is a convergence site (E1/E3)", want)
			}
		}
		for _, n := range names {
			if _, ok := cvExemptions[n]; ok {
				continue
			}
			body := topLevelShellFunction(content, n)
			if body == "" {
				body = extractShellFunction(content, n)
			}
			body = cvCode(body)
			if route, ok := cvExpectedRoute[n]; ok {
				if !strings.Contains(body, route) {
					t.Errorf("%s() must converge through `%s` (E1, D-60)", n, route)
				}
				continue
			}
			if !cvAnyRoute.MatchString(body) {
				t.Errorf("%s() installs outside every convergence route; route it through _converge, _pip_install_locked or the Node apt pin line, or add it to the exemption table with a reason (D-60)", n)
			}
		}
	})

	t.Run("an exemption names a live function", func(t *testing.T) {
		for n, reason := range cvExemptions {
			if !discovered[n] {
				t.Errorf("exemption %s (%q) names no function in quickstart.sh; the table must not rot", n, reason)
			}
		}
	})

	t.Run("no version or state guard before an installer-owned call", func(t *testing.T) {
		lines := strings.Split(code, "\n")
		pipCall := regexp.MustCompile(`(^|[^A-Za-z0-9_])_pip_install_locked[ \t]`)
		// The DD:179 loud stop quotes "go env -w" in its message; only an unquoted one is the call.
		quoted := regexp.MustCompile(`"[^"]*"`)
		var pip, node, goenv int
		for i, l := range lines {
			var at int
			switch {
			case strings.Contains(l, "_pip_install_locked()"):
				continue
			case pipCall.MatchString(l):
				pip++
				at = strings.Index(l, "_pip_install_locked")
			case strings.Contains(l, `nodejs="$NODE_APT_VERSION"`) && cvSimulate.MatchString(l):
				continue
			case strings.Contains(l, `nodejs="$NODE_APT_VERSION"`):
				node++
				at = strings.Index(l, "nodejs=")
			case strings.Contains(quoted.ReplaceAllString(l, `""`), "go env -w"):
				goenv++
				at = strings.Index(l, "go env -w")
			default:
				continue
			}
			context := []string{l[:at]}
			for j := i - 1; j >= 0 && len(context) < 4; j-- {
				if strings.TrimSpace(lines[j]) != "" {
					context = append(context, lines[j])
				}
			}
			if m := cvGuardRe.FindString(strings.Join(context, "\n")); m != "" {
				t.Errorf("line %d (%s) is guarded by %q within the three lines before it; the installer is called unconditionally and decides (E0, D-60)", i+1, strings.TrimSpace(l), m)
			}
		}
		if pip < 4 {
			t.Errorf("found %d _pip_install_locked call sites; want >= 4 (K17 user + --system, K17 litellm, setup_litellm)", pip)
		}
		if node < 1 {
			t.Errorf(`no nodejs="$NODE_APT_VERSION" apt pin line`)
		}
		if goenv < 1 {
			t.Errorf("no `go env -w` line")
		}
	})

	t.Run("lock paths are $SCRIPT_DIR-anchored", func(t *testing.T) {
		call := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(_pip_install_locked|_pip_would_change)[ \t]+(\S+)`)
		counts := map[string]int{}
		for i, l := range strings.Split(code, "\n") {
			if strings.Contains(l, "_pip_install_locked()") || strings.Contains(l, "_pip_would_change()") {
				continue
			}
			for _, m := range call.FindAllStringSubmatch(l, -1) {
				counts[m[1]]++
				if !strings.HasPrefix(m[2], `"$SCRIPT_DIR/py/`) {
					t.Errorf("line %d: %s %s — every lock argument must be \"$SCRIPT_DIR/py/…\" (D-65); the cwd is the factory root, not the source tree", i+1, m[1], m[2])
				}
			}
		}
		if counts["_pip_install_locked"] < 4 {
			t.Errorf("found %d _pip_install_locked calls; want >= 4", counts["_pip_install_locked"])
		}
		if counts["_pip_would_change"] < 2 {
			t.Errorf("found %d _pip_would_change calls; want >= 2 (both locks)", counts["_pip_would_change"])
		}
	})

	t.Run("install_claude is a bare call in main", func(t *testing.T) {
		if n := len(regexp.MustCompile(`(?m)^    install_claude$`).FindAllString(content, -1)); n != 1 {
			t.Errorf("want exactly one bare `    install_claude` line; found %d (D-66: the row and the ledger carry the outcome)", n)
		}
		if n := strings.Count(content, "if ! check_claude"); n != 0 {
			t.Errorf("`if ! check_claude` still gates install_claude (%d sites); a present-but-stale claude must reach _converge", n)
		}
	})

	t.Run("return contracts", func(t *testing.T) {
		for _, fn := range []string{"install_af", "install_claude", "install_gh", "install_playwright",
			"setup_telemetry", "converge_present_components", "_converge_check"} {
			body := topLevelShellFunction(content, fn)
			if body == "" {
				t.Errorf("%s() is not defined at top level", fn)
				continue
			}
			sites := 0
			for _, l := range strings.Split(cvCode(body), "\n") {
				i := strings.Index(l, "_converge ")
				if i == -1 {
					continue
				}
				sites++
				if !cvConvergeOrTrue.MatchString(l[i:]) {
					t.Errorf("%s(): `%s` must end with `|| true` — a drifted or failed row returns 1, and under set -e a bare call aborts before the record (D-66)", fn, strings.TrimSpace(l))
				}
			}
			if sites == 0 {
				t.Errorf("%s() has no _converge call", fn)
			}
		}
	})

	t.Run("main runs the steps in K-order", func(t *testing.T) {
		main := cvCode(topLevelShellFunction(content, "main"))
		cvFirstOccurrencesAscend(t, "main", main, []cvAnchor{
			cvLit(`log_step "Phase 1`),
			cvLine("_apt_converge", `^[ \t]+_apt_converge\b`),
			cvLine("    install_af", `^    install_af\b`),
			cvLine("    install_gh", `^    install_gh\b`),
			cvLine("    install_claude", `^    install_claude\b`),
			cvLine("    install_playwright", `^    install_playwright\b`),
			cvLine("_apt_security_upgrade", `^[ \t]+_apt_security_upgrade\b`),
			cvLine("configure_factory", `^[ \t]+configure_factory\b`),
			cvLine("converge_present_components", `^[ \t]+converge_present_components\b`),
			cvLine("_restart_rebuilt_webui", `^[ \t]+_restart_rebuilt_webui\b`),
			cvLit(`if [ -x "$HOME/.local/bin/webui" ]; then`),
			cvLine("setup_litellm", `^[ \t]+setup_litellm$`),
			cvLit("\n        setup_telemetry\n"),
			cvLine("_converge_report", `^[ \t]+_converge_report\b`),
			cvLit("Setup Complete!"),
		})
	})

	t.Run("converge_present_components order and limits", func(t *testing.T) {
		raw := topLevelShellFunction(content, "converge_present_components")
		if raw == "" {
			t.Fatal("converge_present_components() is not defined at top level (K17)")
		}
		body := cvCode(raw)
		cvFirstOccurrencesAscend(t, "converge_present_components", body, []cvAnchor{
			cvLit(`_pip_would_change "$SCRIPT_DIR/py/requirements.txt"`),
			cvLit(`_pip_would_change "$SCRIPT_DIR/py/litellm-requirements.txt"`),
			cvLit(`_gateway_stop`),
			cvLit(`_pip_install_locked "$SCRIPT_DIR/py/requirements.txt"`),
			cvLit(`_pip_install_locked "$SCRIPT_DIR/py/requirements.txt" --system`),
			cvLit(`_pip_install_locked "$SCRIPT_DIR/py/litellm-requirements.txt"`),
			cvLit(`gateway-relaunch.sh`),
			cvLit(`_ensure_codex_cli || true`),
			cvLit(`_converge openobserve "$OPENOBSERVE_VERSION" _probe_openobserve _install_openobserve required || true`),
		})
		if n := strings.Count(content, "_ensure_codex_cli || true"); n != 1 {
			t.Errorf("quickstart.sh has %d `_ensure_codex_cli || true` sites, want 1 (K17's; D-66, AC 2)", n)
		}
		if m := regexp.MustCompile(`(?m)^[ \t]*(setup_telemetry|setup_litellm)[ \t]*$`).FindString(body); m != "" {
			t.Errorf("converge_present_components makes a bare-line %q call; K17 converges components, it never re-runs a setup phase", strings.TrimSpace(m))
		}
		lines := strings.Split(body, "\n")
		prev := ""
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if strings.Contains(l, "_telemetry_stop") && (strings.Contains(l, "WITH_TELEMETRY") || strings.Contains(prev, "WITH_TELEMETRY")) {
				t.Errorf("a _telemetry_stop is keyed on WITH_TELEMETRY (%s); --no-telemetry never stops a running backend (D-47)", strings.TrimSpace(l))
			}
			prev = l
		}
	})

	t.Run("bash-3.2 avoid list over the new top-level code", func(t *testing.T) {
		for _, fn := range []string{"_arch", "_converge", "_record_row", "_quarantine_stale", "_pip_install_locked",
			"_pip_would_change", "_npm_install_pinned", "_apt_get", "_os_codename", "_pending_security",
			"_ensure_nodesource_list", "_apt_converge", "_apt_security_upgrade", "converge_present_components",
			"_gateway_stop", "_probe_openobserve", "_install_openobserve", "_telemetry_stop", "_factory_repo_dir",
			"_converge_check", "_converge_report", "_restart_rebuilt_webui", "install_gh"} {
			body := topLevelShellFunction(content, fn)
			if body == "" {
				t.Errorf("%s() is not defined at top level", fn)
				continue
			}
			for _, re := range cvBash32Avoid {
				if m := re.FindString(cvCode(body)); m != "" {
					t.Errorf("%s() uses %q, which macOS's bash 3.2 lacks (D-11)", fn, strings.TrimSpace(m))
				}
			}
		}
	})

	t.Run("check_gh is unchanged", func(t *testing.T) {
		if got := topLevelShellFunction(content, "check_gh"); got != cvCheckGhGolden {
			t.Errorf("check_gh() changed; it is DO-NOT-CHANGE (1g). got:\n%s", got)
		}
	})

	t.Run("install_gh re-points the credential helper only off /usr/bin/gh", func(t *testing.T) {
		body := topLevelShellFunction(content, "install_gh")
		if body == "" {
			t.Fatal("install_gh() is not defined at top level (1g)")
		}
		for _, want := range []string{"/usr/bin/gh", "credential", "git config --global"} {
			if !strings.Contains(body, want) {
				t.Errorf("install_gh() must re-point a git credential helper that names /usr/bin/gh (D-31, D18); missing %q", want)
			}
		}
	})
}

// ---------------------------------------------------------------------------------------------

type cvProbeSpec struct {
	name, pinVar, probe, class, stale, toolRel, script, installExtra string
}

const cvPlaywrightStub = `case "$1" in
--version) printf 'Version %s\n' "$(cat "$CV_STATE")" ;;
install)
	case "$*" in
	*--dry-run*)
		rev="${CV_PW_REV:-1248}"
		for b in chromium:chromium-$rev chromium-headless-shell:chromium_headless_shell-$rev ffmpeg:ffmpeg-1011; do
			printf 'browser: %s\n  Install location:    %s\n  Download url:        https://cdn.playwright.dev/builds/%s.zip\n\n' "${b%%:*}" "$HOME/.cache/ms-playwright/${b#*:}" "${b#*:}"
		done ;;
	*) echo "MUTATION playwright $*" >>"$CV_LOG"; exit 1 ;;
	esac ;;
*) exit 1 ;;
esac
`

// cvGoPrefixStub models H-3: without GOTOOLCHAIN=local the prefix go reports the toolchain it
// would switch to, never its own version.
const cvGoPrefixStub = `case "$1" in
version)
	if [ "${GOTOOLCHAIN-}" = local ]; then printf 'go version %s linux/arm64\n' "$(cat "$CV_STATE")"
	else printf 'go version go1.99.0 linux/arm64\n'; fi ;;
*) exit 1 ;;
esac
`

var cvPlaywrightDirs = []string{"chromium-1248", "chromium_headless_shell-1248", "ffmpeg-1011"}

func cvProbeSpecs() []cvProbeSpec {
	return []cvProbeSpec{
		{name: "claude", pinVar: "CLAUDE_CODE_VERSION", probe: "_probe_claude", class: "required", stale: "2.1.280",
			toolRel: ".local/bin/claude",
			script:  `case "$1" in --version) printf '%s (Claude Code)\n' "$(cat "$CV_STATE")" ;; *) exit 1 ;; esac` + "\n"},
		{name: "gh", pinVar: "GH_VERSION", probe: "_probe_gh", class: "required", stale: "2.90.0",
			toolRel: ".local/bin/gh",
			script:  `case "$1" in --version) v="$(cat "$CV_STATE")"; printf 'gh version %s (2026-04-16)\nhttps://github.com/cli/cli/releases/tag/v%s\n' "$v" "$v" ;; *) exit 1 ;; esac` + "\n"},
		{name: "playwright", pinVar: "PLAYWRIGHT_VERSION", probe: "_probe_playwright", class: "optional", stale: "1.61.1",
			toolRel: ".local/bin/playwright", script: cvPlaywrightStub,
			installExtra: `    mkdir -p "$HOME/.cache/ms-playwright/chromium-1248" "$HOME/.cache/ms-playwright/chromium_headless_shell-1248" "$HOME/.cache/ms-playwright/ffmpeg-1011"` + "\n"},
		{name: "codex", pinVar: "CODEX_VERSION", probe: "_probe_codex", class: "required", stale: "0.155.1",
			toolRel: ".local/bin/codex",
			script:  `case "$1" in --version) printf 'codex-cli %s\n' "$(cat "$CV_STATE")" ;; *) exit 1 ;; esac` + "\n"},
		{name: "openobserve", pinVar: "OPENOBSERVE_VERSION", probe: "_probe_openobserve", class: "required", stale: "v0.90.0",
			toolRel: ".local/bin/openobserve",
			script:  `case "$1" in --version) printf 'openobserve %s\n' "$(cat "$CV_STATE")" ;; *) exit 1 ;; esac` + "\n"},
		{name: "go", pinVar: "GO_TOOLCHAIN_VERSION", probe: "_probe_go", class: "required", stale: "go1.24.2",
			toolRel: ".local/share/agentfactory/go/bin/go", script: cvGoPrefixStub},
	}
}

func cvInstallFn(extra string) string {
	return `_t_install() {
    echo INSTALL >>"$CV_LOG"
    mkdir -p "$(dirname "$CV_TOOL")"
    cat "$CV_TEMPLATE" >"$CV_TOOL"
    chmod 0755 "$CV_TOOL"
    printf '%s\n' "$CV_PIN" >"$CV_STATE"
` + extra + `}
`
}

func TestConvergeProbeDecisions(t *testing.T) {
	content := readQuickstart(t)

	t.Run("openobserve probe is hoisted out of setup_telemetry", func(t *testing.T) {
		if topLevelShellFunction(content, "_probe_openobserve") == "" {
			t.Error("_probe_openobserve() must be top level: --check and K17 run it outside setup_telemetry (D-22, D-67)")
		}
		if strings.Contains(topLevelShellFunction(content, "setup_telemetry"), "_probe_openobserve() {") {
			t.Error("setup_telemetry() still defines _probe_openobserve; it is hoisted (D-22)")
		}
	})

	type probeCase struct {
		label     string
		seed      string // "pin", "stale" or "" (absent)
		check     bool
		dirs      []string
		wantRC    int
		installs  int
		wantLine  func(pin, stale string) string
		wantRow   string
		installed func(pin, stale string) string
	}
	allDirs := cvPlaywrightDirs
	cases := []probeCase{
		{label: "match_is_kept", seed: "pin", dirs: allDirs, installs: 0, wantRow: "kept",
			wantLine:  func(p, s string) string { return "[OK] %s: " + p + " (kept)" },
			installed: func(p, s string) string { return p }},
		{label: "mismatch_converges", seed: "stale", dirs: allDirs, installs: 1, wantRow: "converged",
			wantLine:  func(p, s string) string { return "[OK] %s: " + p + " (converged from " + s + ")" },
			installed: func(p, s string) string { return p }},
		{label: "absent_converges", seed: "", installs: 1, wantRow: "converged",
			wantLine:  func(p, s string) string { return "[OK] %s: " + p + " (converged from absent)" },
			installed: func(p, s string) string { return p }},
		{label: "check_mismatch_reports_drift", seed: "stale", dirs: allDirs, check: true, wantRC: 1, installs: 0, wantRow: "drift",
			wantLine:  func(p, s string) string { return "[WARN] %s: " + s + " != pinned " + p + " (would converge)" },
			installed: func(p, s string) string { return s }},
		{label: "check_absent_reports_drift", seed: "", check: true, wantRC: 1, installs: 0, wantRow: "drift",
			wantLine:  func(p, s string) string { return "[WARN] %s: absent != pinned " + p + " (would converge)" },
			installed: func(p, s string) string { return "absent" }},
	}

	for _, spec := range cvProbeSpecs() {
		spec := spec
		specCases := cases
		if spec.name == "playwright" {
			specCases = append(append([]probeCase{}, cases...), probeCase{
				label: "match_without_chromium_converges", seed: "pin", dirs: cvPlaywrightDirs[1:], installs: 1, wantRow: "converged",
				wantLine:  func(p, s string) string { return "[OK] %s: " + p + " (converged from " + p + "+no-chromium)" },
				installed: func(p, s string) string { return p }})
		}
		for _, tc := range specCases {
			tc := tc
			t.Run(spec.name+"/"+tc.label, func(t *testing.T) {
				s := newCvSandbox(t)
				harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error",
					"command_exists", "_record_row", "_quarantine_stale", "_converge", spec.probe)
				pin := quickstartPin(t, spec.pinVar)
				if spec.stale == pin {
					t.Fatalf("the stale fixture %s equals the pin; pick another", spec.stale)
				}
				state := filepath.Join(s.base, "state")
				tool := filepath.Join(s.home, spec.toolRel)
				template := s.writeFile(filepath.Join(s.base, "template"), "#!/bin/sh\n"+spec.script, 0o755)
				switch tc.seed {
				case "pin":
					s.writeFile(state, pin+"\n", 0o644)
				case "stale":
					s.writeFile(state, spec.stale+"\n", 0o644)
				}
				if tc.seed != "" {
					s.writeFile(tool, "#!/bin/sh\n"+spec.script, 0o755)
				}
				if spec.name == "playwright" {
					for _, d := range tc.dirs {
						if err := os.MkdirAll(filepath.Join(s.home, ".cache/ms-playwright", d), 0o755); err != nil {
							t.Fatal(err)
						}
					}
				}
				if spec.name == "go" {
					// A PATH go that already reports the pin: the probe must read the prefix binary.
					s.stub("go", `case "$1" in version) printf 'go version %s linux/arm64\n' "$CV_PIN" ;; *) exit 1 ;; esac`+"\n")
				}
				call := fmt.Sprintf(`_converge %s "$%s" %s _t_install %s`, spec.name, spec.pinVar, spec.probe, spec.class)
				if tc.check {
					call = "CHECK_ONLY=true; " + call
				}
				r := s.run(harness+cvInstallFn(spec.installExtra), call,
					"CV_STATE="+state, "CV_TOOL="+tool, "CV_TEMPLATE="+template, "CV_PIN="+pin)
				if r.rc != tc.wantRC {
					t.Errorf("rc = %d, want %d\n%s", r.rc, tc.wantRC, r.out)
				}
				if got := strings.Count(r.log, "INSTALL\n"); got != tc.installs {
					t.Errorf("INSTALL_FN ran %d times, want %d\n%s", got, tc.installs, r.out)
				}
				if line := fmt.Sprintf(tc.wantLine(pin, spec.stale), spec.name); !cvHasLine(r.out, line) {
					t.Errorf("stdout lacks the line %q\n%s", line, r.out)
				}
				cvExpectRow(t, r.rows, spec.name, pin, tc.installed(pin, spec.stale), tc.wantRow)
			})
		}
	}
}

// ---------------------------------------------------------------------------------------------

const cvClassFns = `_p() { cat "$CV_STATE" 2>/dev/null; }
_i_fail() { echo I >>"$CV_LOG"; return 1; }
_i_wrong() { echo I >>"$CV_LOG"; echo other >"$CV_STATE"; }
_i_ok() { echo I >>"$CV_LOG"; echo 1.0.0 >"$CV_STATE"; }
`

func TestConvergeClassPolicy(t *testing.T) {
	seedPlaywrightCache := func(t *testing.T, home string) string {
		cache := filepath.Join(home, ".cache/ms-playwright")
		for _, d := range []string{"chromium-1228", "chromium_headless_shell-1228", "chromium-1243",
			"chromium_headless_shell-1243", "ffmpeg-1011", "chromium-1100.stale-20260101T000000Z"} {
			if err := os.MkdirAll(filepath.Join(cache, d), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cache, d, "marker"), []byte(d), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return cache
	}
	entries := func(t *testing.T, dir string) []string {
		des, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, de := range des {
			names = append(names, de.Name())
		}
		return names
	}
	untouched := []string{"chromium-1243", "chromium_headless_shell-1243", "ffmpeg-1011", "chromium-1100.stale-20260101T000000Z"}
	assertQuarantined := func(t *testing.T, cache string) {
		t.Helper()
		names := entries(t, cache)
		if len(names) != 6 {
			t.Errorf("quarantine must rename, never delete (ADR-017): want 6 entries, got %v", names)
		}
		live := regexp.MustCompile(`^chromium(_headless_shell)?-[0-9]+$`)
		for _, n := range names {
			if live.MatchString(n) && !strings.HasSuffix(n, "-1243") {
				t.Errorf("%s survived un-suffixed; only revision 1243 (the integration's linked build) stays (D16)", n)
			}
		}
		for _, old := range []string{"chromium-1228", "chromium_headless_shell-1228"} {
			re := regexp.MustCompile(`^` + regexp.QuoteMeta(old) + `\.stale-[0-9]{8}T[0-9]{6}Z$`)
			found := ""
			for _, n := range names {
				if re.MatchString(n) {
					found = n
				}
			}
			if found == "" {
				t.Errorf("no %s.stale-<UTC %%Y%%m%%dT%%H%%M%%SZ> entry in %v", old, names)
				continue
			}
			if b, _ := os.ReadFile(filepath.Join(cache, found, "marker")); string(b) != old {
				t.Errorf("%s does not hold %s's contents; a quarantine is a rename", found, old)
			}
		}
		for _, keep := range untouched {
			if b, _ := os.ReadFile(filepath.Join(cache, keep, "marker")); string(b) != keep {
				t.Errorf("%s was touched", keep)
			}
		}
	}
	assertCacheUntouched := func(t *testing.T, cache string) {
		t.Helper()
		names := entries(t, cache)
		for _, n := range names {
			if strings.Contains(n, ".stale-") && n != "chromium-1100.stale-20260101T000000Z" {
				t.Errorf("%s was quarantined; quarantine happens only after a failed optional install", n)
			}
		}
		if len(names) != 6 {
			t.Errorf("cache changed: %v", names)
		}
	}

	type classCase struct {
		label, call, stateSeed string
		playwrightCache        bool
		wantRC                 int
		wantLedger             string
		installs               int
		wantLine, banned       string
		row                    cvRow
		after                  func(t *testing.T, cache string)
	}
	cases := []classCase{
		{label: "required_install_failure_is_ledgered", call: `_converge x 1.0.0 _p _i_fail required`, stateSeed: "0.9",
			wantRC: 1, wantLedger: "x\n", installs: 1,
			wantLine: "[ERROR] x: 0.9 != pinned 1.0.0 — install failed (reason above)", banned: "after install",
			row: cvRow{name: "x", pin: "1.0.0", installed: "0.9", result: "FAILED"}},
		{label: "required_wrong_build_is_ledgered_once", call: `_converge x 1.0.0 _p _i_wrong required`, stateSeed: "0.9",
			wantRC: 1, wantLedger: "x\n", installs: 1,
			wantLine: "[ERROR] x: other != pinned 1.0.0 after install — the installer put a different build on PATH (none)",
			row:      cvRow{name: "x", pin: "1.0.0", installed: "other", result: "FAILED"}},
		{label: "required_success_converges", call: `_converge x 1.0.0 _p _i_ok required`, stateSeed: "0.9",
			wantRC: 0, wantLedger: "", installs: 1,
			wantLine: "[OK] x: 1.0.0 (converged from 0.9)",
			row:      cvRow{name: "x", pin: "1.0.0", installed: "1.0.0", result: "converged"}},
		{label: "optional_install_failure_quarantines", call: `_converge playwright 1.0.0 _p _i_fail optional`, stateSeed: "0.9",
			playwrightCache: true, wantRC: 0, wantLedger: "", installs: 1,
			wantLine: "[WARN] playwright: 0.9 != pinned 1.0.0 — install failed (reason above) — visual checks will escalate to the human gate",
			row:      cvRow{name: "playwright", pin: "1.0.0", installed: "0.9", result: "skipped-optional"},
			after:    assertQuarantined},
		{label: "optional_wrong_build_quarantines", call: `_converge playwright 1.0.0 _p _i_wrong optional`, stateSeed: "0.9",
			playwrightCache: true, wantRC: 0, wantLedger: "", installs: 1,
			wantLine: "[WARN] playwright: other != pinned 1.0.0 after install — the installer put a different build on PATH (none) — visual checks will escalate to the human gate",
			row:      cvRow{name: "playwright", pin: "1.0.0", installed: "other", result: "skipped-optional"},
			after:    assertQuarantined},
		{label: "optional_kept_never_quarantines", call: `_converge playwright 1.0.0 _p _i_fail optional`, stateSeed: "1.0.0",
			playwrightCache: true, wantRC: 0, wantLedger: "", installs: 0,
			wantLine: "[OK] playwright: 1.0.0 (kept)",
			row:      cvRow{name: "playwright", pin: "1.0.0", installed: "1.0.0", result: "kept"},
			after:    assertCacheUntouched},
		{label: "check_mode_never_installs_or_quarantines", call: `CHECK_ONLY=true; _converge playwright 1.0.0 _p _i_fail optional`, stateSeed: "0.9",
			playwrightCache: true, wantRC: 1, wantLedger: "", installs: 0,
			wantLine: "[WARN] playwright: 0.9 != pinned 1.0.0 (would converge)",
			row:      cvRow{name: "playwright", pin: "1.0.0", installed: "0.9", result: "drift"},
			after:    assertCacheUntouched},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			s := newCvSandbox(t)
			harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error",
				"command_exists", "_record_row", "_quarantine_stale", "_converge")
			state := s.writeFile(filepath.Join(s.base, "state"), tc.stateSeed+"\n", 0o644)
			cache := ""
			if tc.playwrightCache {
				cache = seedPlaywrightCache(t, s.home)
			}
			r := s.run(harness+cvClassFns, tc.call, "CV_STATE="+state)
			if r.rc != tc.wantRC {
				t.Errorf("rc = %d, want %d\n%s", r.rc, tc.wantRC, r.out)
			}
			if r.ledger != tc.wantLedger {
				t.Errorf("CONVERGE_FAILURES = %q, want %q", r.ledger, tc.wantLedger)
			}
			if got := cvCountLines(r.log, "I"); got != tc.installs {
				t.Errorf("INSTALL_FN ran %d times, want %d (a post-install mismatch never loops, DD:184)", got, tc.installs)
			}
			if !cvHasLine(r.out, tc.wantLine) {
				t.Errorf("stdout lacks the line %q\n%s", tc.wantLine, r.out)
			}
			if tc.banned != "" && strings.Contains(r.out, tc.banned) {
				t.Errorf("stdout says %q, but nothing was installed (D3)\n%s", tc.banned, r.out)
			}
			cvExpectRow(t, r.rows, tc.row.name, tc.row.pin, tc.row.installed, tc.row.result)
			if tc.after != nil {
				tc.after(t, cache)
			}
		})
	}

	t.Run("row store replaces by name and kept never masks an outcome", func(t *testing.T) {
		s := newCvSandbox(t)
		harness := convergeHarness(t, s.src, "_record_row")
		r := s.run(harness, `_record_row x 1 0.9 FAILED; _record_row x 1 1 kept; `+
			`_record_row y 1 0.9 drift; _record_row y 1 1 converged; `+
			`_record_row z 1 1 kept; _record_row z 1 1 converged`)
		if r.rc != 0 {
			t.Fatalf("rc = %d\n%s", r.rc, r.out)
		}
		cvExpectRow(t, r.rows, "x", "1", "0.9", "FAILED")
		cvExpectRow(t, r.rows, "y", "1", "1", "converged")
		cvExpectRow(t, r.rows, "z", "1", "1", "converged")
		if len(r.rows) != 3 {
			t.Errorf("want 3 rows, got %+v", r.rows)
		}
	})
}

// ---------------------------------------------------------------------------------------------

const cvClaudeCurlStub = `echo "curl $*" >>"$CV_LOG"
out=""; url=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out="$2"; shift 2 ;;
	-*) shift ;;
	*) url="$1"; shift ;;
	esac
done
case "$url" in
https://downloads.claude.ai/claude-code-releases/*)
	[ "${CV_DIRECT-}" = ok ] && [ -n "$out" ] || exit 22
	printf '#!/bin/sh\n: > "$HOME/RAN"\necho "%s (Claude Code)"\n' "$CV_PIN" >"$out" ;;
*) exit 22 ;;
esac
`

// cvSha256CheckStub verifies `HASH  FILE` lines against $CV_EXPECT_SHA256 only, so a pass proves the
// caller handed it the arch's pinned constant.
const cvSha256CheckStub = `echo "sha256sum $*" >>"$CV_LOG"
check=0; src=-
for a in "$@"; do
	case "$a" in
	-c|--check) check=1 ;;
	-*) ;;
	*) src="$a" ;;
	esac
done
[ "$check" = 1 ] || { echo "sha256sum without --check" >>"$CV_LOG"; exit 1; }
if [ "$src" = - ]; then input="$(cat)"; else input="$(cat "$src")"; fi
printf '%s\n' "$input" | {
	rc=0
	while read -r h f; do
		[ -n "$h" ] || continue
		f="${f#\*}"
		if [ "$h" = "${CV_EXPECT_SHA256-}" ] && [ -f "$f" ]; then echo "$f: OK"; else echo "$f: FAILED"; rc=1; fi
	done
	exit $rc
}
exit $?
`

func cvClaudeSandbox(t *testing.T) *cvSandbox {
	s := newCvSandbox(t)
	s.stub("uname", `case "$1" in -s) echo Linux ;; -m) echo "$CV_UNAME_M" ;; *) echo Linux ;; esac`+"\n")
	s.stub("dpkg", `case "$1" in --print-architecture) case "$CV_UNAME_M" in aarch64) echo arm64 ;; *) echo amd64 ;; esac ;; *) exit 1 ;; esac`+"\n")
	s.stub("curl", cvClaudeCurlStub)
	s.stub("bash", `echo "bash $*" >>"$CV_LOG"; cat >/dev/null; exit 1`+"\n")
	s.stub("sha256sum", cvSha256CheckStub)
	s.stub("sha512sum", `echo "sha512sum $*" >>"$CV_LOG"; cat >/dev/null; exit 1`+"\n")
	s.stub("tar", `echo "tar $*" >>"$CV_LOG"; exit 1`+"\n")
	s.stub("npm", `echo "npm $*" >>"$CV_LOG"
case "$1" in
root) echo "$HOME/npmroot" ;;
prefix) echo "$HOME/npmprefix" ;;
*) exit 1 ;;
esac
`)
	s.stub("sudo", `echo "sudo $*" >>"$CV_LOG"; exit 1`+"\n")
	s.stub("id", `case "$1" in -u) echo 1000 ;; *) echo dev ;; esac`+"\n")
	if err := os.MkdirAll(filepath.Join(s.home, "npmroot"), 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestQuickstartClaudePresentStaleReachesInstall(t *testing.T) {
	fns := []string{"log_info", "log_success", "log_warn", "log_error", "log_step", "command_exists", "_arch",
		"_record_row", "_quarantine_stale", "_converge", "_npm_install_pinned", "install_claude"}

	t.Run("stale present reaches the installer", func(t *testing.T) {
		s := cvClaudeSandbox(t)
		harness := convergeHarness(t, s.src, fns...)
		v := quickstartPin(t, "CLAUDE_CODE_VERSION")
		s.stub("claude", `case "$1" in --version) echo "2.1.280 (Claude Code)" ;; *) exit 1 ;; esac`+"\n")
		r := s.run(harness, "install_claude", "CV_UNAME_M=aarch64", "CV_PIN="+v)
		if r.rc != 0 {
			t.Errorf("install_claude rc = %d, want 0 (D-66: the row and the ledger carry the outcome)\n%s", r.rc, r.out)
		}
		if url := "https://downloads.claude.ai/claude-code-releases/" + v + "/linux-arm64/claude"; !strings.Contains(r.log, url) {
			t.Errorf("a working-but-stale claude never reached the direct download %s\ncalls:\n%s\nstdout:\n%s", url, r.log, r.out)
		}
		if !strings.Contains(r.log, "npm pack @anthropic-ai/claude-code@"+v) {
			t.Errorf("the npm last resort (channel 3) was not attempted after channels 1-2 failed\n%s", r.log)
		}
		if r.ledger != "claude\n" {
			t.Errorf("CONVERGE_FAILURES = %q, want \"claude\\n\"", r.ledger)
		}
		if line := "[ERROR] claude: 2.1.280 != pinned " + v + " — install failed (reason above)"; !cvHasLine(r.out, line) {
			t.Errorf("stdout lacks %q\n%s", line, r.out)
		}
	})

	for _, arch := range []struct{ unameM, platform, suffix string }{
		{"aarch64", "linux-arm64", "ARM64"},
		{"x86_64", "linux-x64", "AMD64"},
	} {
		arch := arch
		setup := func(t *testing.T) (*cvSandbox, string, string, string, string) {
			s := cvClaudeSandbox(t)
			harness := convergeHarness(t, s.src, fns...)
			v := quickstartPin(t, "CLAUDE_CODE_VERSION")
			versions := filepath.Join(s.home, ".local/share/claude/versions")
			old := s.writeFile(filepath.Join(versions, "2.1.280"), "#!/bin/sh\necho \"2.1.280 (Claude Code)\"\n", 0o755)
			if err := os.MkdirAll(filepath.Join(s.home, ".local/bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(old, filepath.Join(s.home, ".local/bin/claude")); err != nil {
				t.Fatal(err)
			}
			return s, harness, v, versions, old
		}

		t.Run("direct download lays out versions and links it/"+arch.unameM, func(t *testing.T) {
			s, harness, v, versions, old := setup(t)
			sum := quickstartPin(t, "CLAUDE_CODE_SHA256_"+arch.suffix)
			r := s.run(harness, "install_claude", "CV_UNAME_M="+arch.unameM, "CV_PIN="+v, "CV_DIRECT=ok", "CV_EXPECT_SHA256="+sum)
			if r.rc != 0 || r.ledger != "" {
				t.Errorf("rc = %d, ledger %q; want 0 and empty\n%s\n%s", r.rc, r.ledger, r.out, r.log)
			}
			want := filepath.Join(versions, v)
			info, err := os.Lstat(want)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
				t.Errorf("versions/%s must be a regular 0755 file; stat = %v, %v", v, info, err)
			}
			if _, err := os.Lstat(want + ".partial"); err == nil {
				t.Errorf("versions/%s.partial was left behind", v)
			}
			link := filepath.Join(s.home, ".local/bin/claude")
			if li, err := os.Lstat(link); err != nil || li.Mode()&fs.ModeSymlink == 0 {
				t.Errorf("~/.local/bin/claude must stay a symlink into versions/ (never cp/install onto it); lstat = %v, %v", li, err)
			}
			gotT, _ := filepath.EvalSymlinks(link)
			wantT, _ := filepath.EvalSymlinks(want)
			if gotT == "" || gotT != wantT {
				t.Errorf("~/.local/bin/claude resolves to %q, want %q", gotT, wantT)
			}
			if b, _ := os.ReadFile(old); string(b) != "#!/bin/sh\necho \"2.1.280 (Claude Code)\"\n" {
				t.Errorf("the old version's bytes were overwritten through the link: %q", b)
			}
			if !strings.Contains(r.log, "/claude-code-releases/"+v+"/"+arch.platform+"/claude") || strings.Contains(r.log, "linux-amd64") {
				t.Errorf("the download URL must use the %s platform segment (never linux-amd64)\n%s", arch.platform, r.log)
			}
			if strings.Contains(r.log, "https://claude.ai/install.sh") {
				t.Errorf("the official installer ran although the direct download verified\n%s", r.log)
			}
			if line := "[OK] claude: " + v + " (converged from 2.1.280)"; !cvHasLine(r.out, line) {
				t.Errorf("stdout lacks %q\n%s", line, r.out)
			}
		})

		t.Run("digest mismatch never runs the binary/"+arch.unameM, func(t *testing.T) {
			s, harness, v, versions, old := setup(t)
			r := s.run(harness, "install_claude", "CV_UNAME_M="+arch.unameM, "CV_PIN="+v, "CV_DIRECT=ok", "CV_EXPECT_SHA256=0000")
			if r.rc != 0 {
				t.Errorf("install_claude rc = %d, want 0\n%s", r.rc, r.out)
			}
			if _, err := os.Stat(filepath.Join(s.home, "RAN")); err == nil {
				t.Error("the unverified download was executed")
			}
			for _, p := range []string{filepath.Join(versions, v), filepath.Join(versions, v+".partial")} {
				if _, err := os.Lstat(p); err == nil {
					t.Errorf("%s exists after a digest mismatch; it must be removed", p)
				}
			}
			if !strings.Contains(r.log, "https://claude.ai/install.sh") {
				t.Errorf("the official installer channel was not tried after the direct download failed verification\n%s", r.log)
			}
			if r.ledger != "claude\n" {
				t.Errorf("CONVERGE_FAILURES = %q, want \"claude\\n\"", r.ledger)
			}
			if gotT, _ := filepath.EvalSymlinks(filepath.Join(s.home, ".local/bin/claude")); gotT != mustEval(old) {
				t.Errorf("~/.local/bin/claude now resolves to %q; nothing unverified may be linked", gotT)
			}
		})
	}
}

func mustEval(p string) string {
	r, _ := filepath.EvalSymlinks(p)
	return r
}

// ---------------------------------------------------------------------------------------------

func cvHostCodename() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	get := func(key string) string {
		m := regexp.MustCompile(`(?m)^` + key + `=["']?([^"'\n]*)`).FindStringSubmatch(string(data))
		if m == nil {
			return ""
		}
		return m[1]
	}
	if c := get("VERSION_CODENAME"); c != "" {
		return c
	}
	return get("UBUNTU_CODENAME")
}

func cvHostHasSecurityPocket(codename string) bool {
	files, _ := filepath.Glob("/etc/apt/sources.list.d/*")
	files = append(files, "/etc/apt/sources.list")
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), codename+"-security") {
			return true
		}
	}
	return false
}

const cvDpkgQueryStub = `echo "dpkg-query $*" >>"$CV_LOG"
fmt=""; pkgs=""; next=0
for a in "$@"; do
	if [ "$next" = 1 ]; then fmt="$a"; next=0; continue; fi
	case "$a" in
	-f|--showformat) next=1 ;;
	-f=*|--showformat=*) fmt="${a#*=}" ;;
	-*) ;;
	*) pkgs="$pkgs $a" ;;
	esac
done
[ -n "$pkgs" ] || pkgs="bash libkrb5-3 libssl3t64 nodejs"
for p in $pkgs; do
	case "$p" in
	bash) v=5.2.21-2ubuntu4 ;;
	libkrb5-3) v=1.20.1-6ubuntu2.5 ;;
	libssl3t64) v=3.0.13-0ubuntu3.4 ;;
	nodejs) v="${CV_NODE_INSTALLED:-22.22.2-1nodesource1}" ;;
	*) echo "dpkg-query: no packages found matching $p" >&2; exit 1 ;;
	esac
	if [ -z "$fmt" ]; then printf '%s\t%s\n' "$p" "$v"; continue; fi
	out="$(printf '%s' "$fmt" | sed -e 's/[$]{db:Status-Abbrev}/ii /g' -e 's/[$]{db:Status-Status}/installed/g' \
		-e 's/[$]{Status}/install ok installed/g' -e "s/[$]{binary:Package}/$p/g" -e "s/[$]{Package}/$p/g" \
		-e "s/[$]{Version}/$v/g" -e 's/[$]{Architecture}/arm64/g')"
	printf '%b' "$out"
done
`

const cvAptCacheStub = `echo "apt-cache $*" >>"$CV_LOG"
[ "$1" = policy ] || exit 0
shift
c="${CV_CODENAME:-noble}"
for p in "$@"; do
	case "$p" in
	libkrb5-3|libssl3t64)
		case "$p" in libkrb5-3) cur=1.20.1-6ubuntu2.5; new=1.20.1-6ubuntu2.6 ;; *) cur=3.0.13-0ubuntu3.4; new=3.0.13-0ubuntu3.5 ;; esac
		printf '%s:\n  Installed: %s\n  Candidate: %s\n  Version table:\n     %s 500\n        500 http://ports.ubuntu.com/ubuntu-ports %s-updates/main arm64 Packages\n        500 http://ports.ubuntu.com/ubuntu-ports %s-security/main arm64 Packages\n *** %s 100\n        100 /var/lib/dpkg/status\n' "$p" "$cur" "$new" "$new" "$c" "$c" "$cur" ;;
	bash)
		printf 'bash:\n  Installed: 5.2.21-2ubuntu4\n  Candidate: 5.2.21-2ubuntu4\n  Version table:\n *** 5.2.21-2ubuntu4 500\n        500 http://ports.ubuntu.com/ubuntu-ports %s/main arm64 Packages\n        100 /var/lib/dpkg/status\n' "$c" ;;
	nodejs)
		printf 'nodejs:\n  Installed: 22.22.2-1nodesource1\n  Candidate: 22.23.4-1nodesource1\n  Version table:\n     22.23.4-1nodesource1 500\n        500 https://deb.nodesource.com/node_22.x nodistro/main arm64 Packages\n *** 22.22.2-1nodesource1 100\n        100 /var/lib/dpkg/status\n' ;;
	esac
done
`

// cvGuardedStub passes through to the real tool only when every path argument is under $AF_ROOT
// (the convergence record is the one write --check may make).
func cvGuardedStub(name, real string) string {
	return `echo "` + name + ` $*" >>"$CV_LOG"
for a in "$@"; do
	case "$a" in
	-*) ;;
	"$AF_ROOT"/*) ;;
	*) echo "MUTATION ` + name + ` $*" >>"$CV_LOG"; exit 1 ;;
	esac
done
exec '` + real + `' "$@"
`
}

func cvMutationStub(name string) string {
	return `echo "MUTATION ` + name + ` $*" >>"$CV_LOG"` + "\nexit 1\n"
}

const cvCheckGoStub = `echo "go GOTOOLCHAIN=${GOTOOLCHAIN-} $*" >>"$CV_LOG"
[ "${GOTOOLCHAIN-}" = local ] || echo "H3 go $*" >>"$CV_LOG"
case "$1" in
version) echo "go version go1.24.2 linux/arm64" ;;
env)
	case "${2-}" in
	-w|-u) echo "MUTATION go $*" >>"$CV_LOG"; exit 1 ;;
	GOTOOLCHAIN) echo auto ;;
	GOSUMDB) echo sum.golang.org ;;
	*) echo "" ;;
	esac ;;
*) echo "MUTATION go $*" >>"$CV_LOG"; exit 1 ;;
esac
`

const cvPipStub = `echo "pip $*" >>"$CV_LOG"
case "$*" in
*install*)
	case "$*" in
	*--dry-run*)
		report=""; prev=""
		for a in "$@"; do
			[ "$prev" = --report ] && report="$a"
			case "$a" in --report=*) report="${a#--report=}" ;; esac
			prev="$a"
		done
		json='{"install":[{"metadata":{"name":"aiohttp","version":"3.14.4"}}]}'
		if [ -z "$report" ] || [ "$report" = - ]; then echo "$json"; else echo "$json" >"$report"; fi ;;
	*) echo "MUTATION pip $*" >>"$CV_LOG" ;;
	esac ;;
*) exit 1 ;;
esac
`

type cvRecord struct {
	RunAt string `json:"run_at"`
	Mode  struct {
		Litellm   bool `json:"litellm"`
		CheckOnly bool `json:"check_only"`
	} `json:"mode"`
	Components []struct {
		Name      string `json:"name"`
		Pin       string `json:"pin"`
		Installed string `json:"installed"`
		Integrity string `json:"integrity"`
		Result    string `json:"result"`
		At        string `json:"at"`
	} `json:"components"`
	AptChanged    bool `json:"apt_changed"`
	PythonChanged bool `json:"python_changed"`
}

func cvReadRecord(t *testing.T, afRoot string) (*cvRecord, bool) {
	t.Helper()
	p := filepath.Join(afRoot, ".runtime/bootstrap/convergence.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Errorf("no convergence record at $AF_ROOT/.runtime/bootstrap/convergence.json: %v", err)
		return nil, false
	}
	var rec cvRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Errorf("convergence.json does not match the DD:210 schema: %v\n%s", err, data)
		return nil, false
	}
	return &rec, true
}

func cvSnapshot(t *testing.T, root string, skip ...string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		for _, s := range skip {
			if rel == s {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			m[rel] = "link:" + target
		case info.IsDir():
			m[rel] = "dir:" + info.Mode().Perm().String()
		default:
			data, _ := os.ReadFile(p)
			m[rel] = fmt.Sprintf("file:%s:%x", info.Mode().Perm(), sha256.Sum256(data))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return m
}

func cvSnapshotDiff(before, after map[string]string) []string {
	var diff []string
	for k, v := range before {
		if a, ok := after[k]; !ok {
			diff = append(diff, "removed "+k)
		} else if a != v {
			diff = append(diff, "changed "+k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diff = append(diff, "created "+k)
		}
	}
	sort.Strings(diff)
	return diff
}

var cvFixedTmpPaths = []string{"/tmp/af-codex-install.log", "/tmp/af-playwright-install.log", "/tmp/af-playwright-probe.png", "/tmp/webui.log"}

func cvStatPaths(paths []string) map[string]string {
	m := map[string]string{}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			m[p] = "absent"
			continue
		}
		m[p] = fmt.Sprintf("%d:%d", fi.Size(), fi.ModTime().UnixNano())
	}
	return m
}

func copyIntoSrc(t *testing.T, root, src, rel string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	dst := filepath.Join(src, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		t.Fatal(err)
	}
}

func TestQuickstartCheckModeIsReadOnly(t *testing.T) {
	content := readQuickstart(t)

	t.Run("check_python verifies the interpreter only", func(t *testing.T) {
		body := cvCode(topLevelShellFunction(content, "check_python"))
		for _, banned := range []string{"pip3 install", "_pip_install_locked", "import aiohttp"} {
			if strings.Contains(body, banned) {
				t.Errorf("check_python() contains %q; run_all_checks runs under --check, which must change nothing (1d, P2-5)", banned)
			}
		}
	})

	t.Run("show_help documents the read-only drift report", func(t *testing.T) {
		body := topLevelShellFunction(content, "show_help")
		if !strings.Contains(body, "--check") || !strings.Contains(body, "drift") {
			t.Error("show_help() must describe --check as a read-only drift report (1k)")
		}
		if strings.Contains(body, "installs bd") {
			t.Error("show_help() still claims the script installs bd (1k)")
		}
		for _, keep := range []string{"--litellm-auth", "AF_CODEX_INSTALL_CONSENT"} {
			if !strings.Contains(body, keep) {
				t.Errorf("show_help() lost %q", keep)
			}
		}
	})

	t.Run("main routes --check through _converge_check", func(t *testing.T) {
		main := cvCode(topLevelShellFunction(content, "main"))
		if !regexp.MustCompile(`(?m)^[ \t]+_converge_check\b`).MatchString(main) {
			t.Error("main's CHECK_ONLY block must call _converge_check (D6)")
		}
		body := topLevelShellFunction(content, "_converge_check")
		if body == "" {
			t.Fatal("_converge_check() is not defined at top level (D6)")
		}
		cvFirstOccurrencesAscend(t, "_converge_check", cvCode(body), []cvAnchor{
			cvLit("run_all_checks"),
			cvLine("_apt_converge", `^[ \t]+_apt_converge\b`),
			cvLine("install_af (>= 8-space indent)", `^ {8,}install_af\b`),
			cvLine("install_gh (>= 8-space indent)", `^ {8,}install_gh\b`),
			cvLine("install_claude (>= 8-space indent)", `^ {8,}install_claude\b`),
			cvLine("install_playwright (>= 8-space indent)", `^ {8,}install_playwright\b`),
			cvLine("_ensure_codex_cli (>= 12-space indent, inside the `|| true` group)", `^ {12,}_ensure_codex_cli$`),
			cvLine("the group's `} || true`", `^    \} \|\| true$`),
			cvLit(`_converge openobserve "$OPENOBSERVE_VERSION"`),
			cvLit(`_pip_would_change "$SCRIPT_DIR/py/requirements.txt"`),
			cvLine("_converge_report", `^[ \t]+_converge_report\b`),
		})
	})

	t.Run("whole script --check changes nothing and reports every drifted row", func(t *testing.T) {
		root := findModuleRoot(t)
		s := newCvSandbox(t)
		for _, f := range []struct {
			rel  string
			mode os.FileMode
		}{{"quickstart.sh", 0o755}, {"py/requirements.txt", 0o644}, {"py/litellm-requirements.txt", 0o644}, {"go.mod", 0o644}} {
			copyIntoSrc(t, root, s.src, f.rel, f.mode)
		}
		real := func(name string) string {
			p, err := exec.LookPath(name)
			if err != nil {
				t.Fatalf("resolving real %s: %v", name, err)
			}
			return p
		}
		for _, n := range []string{"mv", "ln", "chmod"} {
			s.stub(n, cvGuardedStub(n, real(n)))
		}
		for _, n := range []string{"curl", "wget", "tar", "install", "cp", "make", "tee", "nohup", "setsid",
			"killall", "systemctl", "unzip", "gzip", "rsync", "useradd"} {
			s.stub(n, cvMutationStub(n))
		}
		s.stub("sha256sum", `echo "sha256sum $*" >>"$CV_LOG"; exit 1`+"\n")
		s.stub("sha512sum", `echo "sha512sum $*" >>"$CV_LOG"; exit 1`+"\n")
		s.stub("uname", `case "$1" in -s) echo Linux ;; -m) echo aarch64 ;; -r) echo 6.0.0 ;; *) echo Linux ;; esac`+"\n")
		s.stub("id", `case "$1" in -u) echo 1000 ;; -g) echo 1000 ;; -un|-nu) echo dev ;; *) echo "uid=1000(dev) gid=1000(dev)" ;; esac`+"\n")
		s.stub("whoami", "echo dev\n")
		s.stub("hostname", "echo test\n")
		s.stub("sudo", `echo "sudo $*" >>"$CV_LOG"
[ "$1" = -n ] && shift
case "${1-}" in
true) exit 0 ;;
env|apt-get|apt-cache|dpkg-query|pip3|npm|playwright) exec "$@" ;;
*) echo "MUTATION sudo $*" >>"$CV_LOG"; exit 1 ;;
esac
`)
		s.stub("go", cvCheckGoStub)
		s.writeFile("go/bin/go", "#!/bin/sh\n"+cvCheckGoStub, 0o755)
		s.stub("git", `echo "git $*" >>"$CV_LOG"
case "$1" in
--version) echo "git version 2.43.0" ;;
config) case "$*" in *--get*|*--list*|*" -l"*) exit 1 ;; *) echo "MUTATION git $*" >>"$CV_LOG"; exit 1 ;; esac ;;
*) exit 1 ;;
esac
`)
		s.stub("gh", `echo "gh $*" >>"$CV_LOG"
case "$1" in
--version) printf 'gh version 2.90.0 (2026-04-16)\nhttps://github.com/cli/cli/releases/tag/v2.90.0\n' ;;
*) echo "MUTATION gh $*" >>"$CV_LOG"; exit 1 ;;
esac
`)
		s.stub("tmux", `echo "tmux $*" >>"$CV_LOG"
case "$1" in
-V) echo "tmux 3.4" ;;
has-session|list-panes|list-sessions|list-windows|display-message|show-options) exit 1 ;;
*) echo "MUTATION tmux $*" >>"$CV_LOG"; exit 1 ;;
esac
`)
		s.stub("node", `case "$1" in --version) echo v22.22.2 ;; *) echo "MUTATION node $*" >>"$CV_LOG"; exit 1 ;; esac`+"\n")
		s.stub("npm", `echo "npm $*" >>"$CV_LOG"
case "$*" in
"root -g"|"root --global"|"-g root") echo /usr/lib/node_modules ;;
*) echo "MUTATION npm $*" >>"$CV_LOG"; exit 1 ;;
esac
`)
		s.stub("python3.12", `echo "python3.12 $*" >>"$CV_LOG"; case "$1" in --version) echo "Python 3.12.3" ;; *) exit 1 ;; esac`+"\n")
		s.stub("python3", `echo "python3 $*" >>"$CV_LOG"
case "$*" in *pip*install*) case "$*" in *--dry-run*) ;; *) echo "MUTATION python3 $*" >>"$CV_LOG" ;; esac ;; esac
exit 1
`)
		for _, n := range []string{"pip3", "pip", "pip3.12"} {
			s.stub(n, cvPipStub)
		}
		s.stub("af", `echo "af $*" >>"$CV_LOG"; case "$1" in version) echo "af v0.0.0-test" ;; *) echo "MUTATION af $*" >>"$CV_LOG"; exit 1 ;; esac`+"\n")
		s.stub("claude", `case "$1" in --version) echo "2.1.280 (Claude Code)" ;; *) echo "MUTATION claude $*" >>"$CV_LOG"; exit 1 ;; esac`+"\n")
		s.stub("playwright", `case "$1" in
--version) echo "Version 1.61.1" ;;
install)
	case "$*" in
	*--dry-run*)
		for b in chromium:chromium-1228 chromium-headless-shell:chromium_headless_shell-1228 ffmpeg:ffmpeg-1011; do
			printf 'browser: %s\n  Install location:    %s\n  Download url:        https://cdn.playwright.dev/builds/%s.zip\n\n' "${b%%:*}" "$HOME/.cache/ms-playwright/${b#*:}" "${b#*:}"
		done ;;
	*) echo "MUTATION playwright $*" >>"$CV_LOG"; exit 1 ;;
	esac ;;
*) echo "MUTATION playwright $*" >>"$CV_LOG"; exit 1 ;;
esac
`)
		s.stub("codex", `case "$1" in --version) echo "codex-cli 0.155.1" ;; *) echo "MUTATION codex $*" >>"$CV_LOG"; exit 1 ;; esac`+"\n")
		s.stub("litellm", `case "$1" in --version) printf '\nLiteLLM: Current Version = 1.93.0\n' ;; *) echo "MUTATION litellm $*" >>"$CV_LOG"; exit 1 ;; esac`+"\n")
		s.writeFile(".local/bin/openobserve", `#!/bin/sh
case "$1" in --version) echo "openobserve v0.90.0" ;; *) echo "MUTATION openobserve $*" >>"$CV_LOG"; exit 1 ;; esac
`, 0o755)
		s.stub("dpkg-query", cvDpkgQueryStub)
		s.stub("apt-cache", cvAptCacheStub)
		s.stub("apt-get", `echo "apt-get $*" >>"$CV_LOG"
sim=0
for a in "$@"; do
	case "$a" in
	update) echo "MUTATION apt-get $*" >>"$CV_LOG"; exit 1 ;;
	-s|--simulate) sim=1 ;;
	esac
done
[ "$sim" = 1 ] || { echo "MUTATION apt-get $*" >>"$CV_LOG"; exit 1; }
echo "Inst libkrb5-3 [1.20.1-6ubuntu2.5] (1.20.1-6ubuntu2.6 Ubuntu:24.04/${CV_CODENAME:-noble}-security [arm64])"
echo "Inst libssl3t64 [3.0.13-0ubuntu3.4] (3.0.13-0ubuntu3.5 Ubuntu:24.04/${CV_CODENAME:-noble}-security [arm64])"
`)
		s.stub("dpkg", `echo "dpkg $*" >>"$CV_LOG"
case "$1" in
--print-architecture) echo arm64 ;;
--compare-versions)
	lo="$(printf '%s\n%s\n' "$2" "$4" | sort -V | head -1)"
	case "$3" in
	lt|lt-nl) [ "$2" != "$4" ] && [ "$lo" = "$2" ] ;;
	le|le-nl) [ "$lo" = "$2" ] ;;
	gt|gt-nl) [ "$2" != "$4" ] && [ "$lo" = "$4" ] ;;
	ge|ge-nl) [ "$lo" = "$4" ] ;;
	eq) [ "$2" = "$4" ] ;;
	*) exit 2 ;;
	esac ;;
*) echo "MUTATION dpkg $*" >>"$CV_LOG"; exit 1 ;;
esac
`)

		codename := cvHostCodename()
		rootLayer := runtime.GOOS == "linux" && codename != ""
		workdir := filepath.Join(s.base, "cwd")
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			t.Fatal(err)
		}
		homeBefore := cvSnapshot(t, s.home, ".tmp")
		srcBefore := cvSnapshot(t, s.src)
		tmpBefore := cvStatPaths(cvFixedTmpPaths)

		cmd := exec.Command("bash", filepath.Join(s.src, "quickstart.sh"), "--check")
		cmd.Dir = workdir
		cmd.Env = s.env("CV_CODENAME=" + codename)
		raw, err := cmd.CombinedOutput()
		rc := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			rc = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("running quickstart.sh --check: %v", err)
		}
		out := cvANSI.ReplaceAllString(string(raw), "")
		logBytes, _ := os.ReadFile(s.logPath)
		calls := string(logBytes)

		if rc != 1 {
			t.Errorf("--check exit code = %d, want 1 (every component drifted, D-66)", rc)
		}
		if n := cvCountLines(calls, "MUTATION "); n != 0 {
			var muts []string
			for _, l := range strings.Split(calls, "\n") {
				if strings.HasPrefix(l, "MUTATION ") {
					muts = append(muts, l)
				}
			}
			t.Errorf("--check made %d mutating calls:\n%s", n, strings.Join(muts, "\n"))
		}
		for _, l := range strings.Split(calls, "\n") {
			switch {
			case strings.HasPrefix(l, "apt-get ") && !regexp.MustCompile(` (-s|--simulate)( |$)`).MatchString(l):
				t.Errorf("apt-get ran without -s under --check: %s", l)
			case strings.HasPrefix(l, "pip ") && strings.Contains(l, "install") && !strings.Contains(l, "--dry-run"):
				t.Errorf("pip install ran without --dry-run under --check: %s", l)
			case strings.HasPrefix(l, "H3 "):
				t.Errorf("a go command ran without GOTOOLCHAIN=local (D1, H-3): %s", l)
			case strings.HasPrefix(l, "npm ") && strings.Contains(l, "pack"), strings.HasPrefix(l, "MUTATION curl"), strings.HasPrefix(l, "MUTATION tar"):
				t.Errorf("an INSTALL_FN ran under --check: %s", l)
			}
		}
		if d := cvSnapshotDiff(homeBefore, cvSnapshot(t, s.home, ".tmp")); len(d) > 0 {
			t.Errorf("--check changed $HOME:\n%s", strings.Join(d, "\n"))
		}
		if d := cvSnapshotDiff(srcBefore, cvSnapshot(t, s.src)); len(d) > 0 {
			t.Errorf("--check wrote into the source tree:\n%s", strings.Join(d, "\n"))
		}
		tmpAfter := cvStatPaths(cvFixedTmpPaths)
		for _, p := range cvFixedTmpPaths {
			if tmpBefore[p] != tmpAfter[p] {
				t.Errorf("--check wrote %s", p)
			}
		}
		afterRoot := cvSnapshot(t, s.afRoot)
		var files []string
		for k, v := range afterRoot {
			if strings.HasPrefix(v, "file:") || strings.HasPrefix(v, "link:") {
				files = append(files, k)
			}
		}
		if len(files) != 1 || files[0] != filepath.Join(".runtime", "bootstrap", "convergence.json") {
			t.Errorf("--check may write only $AF_ROOT/.runtime/bootstrap/convergence.json; files under $AF_ROOT: %v", files)
		}

		wantRows := []string{"claude", "gh", "playwright", "go", "python-lock", "codex", "openobserve", "litellm-lock"}
		if rootLayer {
			wantRows = append(wantRows, "nodejs")
			if cvHostHasSecurityPocket(codename) {
				wantRows = append(wantRows, "ubuntu-security")
			}
		}
		rec, ok := cvReadRecord(t, s.afRoot)
		if ok {
			if !rec.Mode.CheckOnly {
				t.Error("convergence.json .mode.check_only must be true under --check")
			}
			byName := map[string]string{}
			for _, c := range rec.Components {
				byName[c.Name] = c.Result
			}
			for _, n := range wantRows {
				if got, present := byName[n]; !present || got != "drift" {
					t.Errorf("convergence.json component %s: result %q (present %v), want drift", n, got, present)
				}
			}
			drift := 0
			for _, c := range rec.Components {
				if c.Result == "drift" {
					drift++
				}
			}
			if n := strings.Count(out, "(would converge)"); n != drift {
				t.Errorf("stdout has %d `(would converge)` lines for %d drifted rows; want one per row\n%s", n, drift, out)
			}
		}

		pinOf := map[string]string{
			"claude": quickstartPin(t, "CLAUDE_CODE_VERSION"), "gh": quickstartPin(t, "GH_VERSION"),
			"playwright": quickstartPin(t, "PLAYWRIGHT_VERSION"), "codex": quickstartPin(t, "CODEX_VERSION"),
			"openobserve": quickstartPin(t, "OPENOBSERVE_VERSION"), "go": quickstartPin(t, "GO_TOOLCHAIN_VERSION"),
		}
		installed := map[string]string{"claude": "2.1.280", "gh": "2.90.0", "playwright": "1.61.1+no-chromium",
			"codex": "0.155.1", "openobserve": "v0.90.0", "go": "absent"}
		for name, pin := range pinOf {
			if line := "[WARN] " + name + ": " + installed[name] + " != pinned " + pin + " (would converge)"; !cvHasLine(out, line) {
				t.Errorf("stdout lacks %q", line)
			}
			if ok {
				for _, c := range rec.Components {
					if c.Name == name && (c.Pin != pin || c.Installed != installed[name]) {
						t.Errorf("convergence.json %s: pin %q installed %q; want %q %q", name, c.Pin, c.Installed, pin, installed[name])
					}
				}
			}
		}
		for _, want := range []string{
			"python-lock: 1 would change != pinned py/requirements.txt (would converge)",
			"litellm-lock: 1 would change != pinned py/litellm-requirements.txt (would converge)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q", want)
			}
		}
		if rootLayer {
			if want := "nodejs: 22.22.2-1nodesource1 != pinned " + quickstartPin(t, "NODE_APT_VERSION") + " (would converge)"; !strings.Contains(out, want) {
				t.Errorf("stdout lacks %q", want)
			}
			if cvHostHasSecurityPocket(codename) {
				re := regexp.MustCompile(`ubuntu-security: [1-9][0-9]* pending != pinned ` + regexp.QuoteMeta(codename) + `-security \(would converge\)`)
				if !re.MatchString(out) {
					t.Errorf("stdout lacks the ubuntu-security drift line for %s-security", codename)
				}
			}
		}
		if t.Failed() {
			t.Logf("stdout:\n%s\ncalls:\n%s", out, calls)
		}
	})
}

// ---------------------------------------------------------------------------------------------

func TestQuickstartRootLayerFailsLoudly(t *testing.T) {
	s := newCvSandbox(t)
	harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error", "log_step",
		"command_exists", "_arch", "_record_row", "_quarantine_stale", "_converge", "_os_codename", "_apt_get",
		"_pending_security", "_ensure_nodesource_list", "_apt_converge", "_apt_security_upgrade",
		"_factory_repo_dir", "_converge_report")
	harness += `WORKSPACE_DIR="$HOME/af"
_ensure_nodesource_list() { echo "NSL $*" >>"$CV_LOG"; }
_kept() { printf '%s\n' "$CLAUDE_CODE_VERSION"; }
_noinst() { echo NOINST >>"$CV_LOG"; return 1; }
`
	s.stub("id", `case "$1" in -u) echo 1000 ;; -g) echo 1000 ;; *) echo dev ;; esac`+"\n")
	s.stub("sudo", `echo "sudo $*" >>"$CV_LOG"; exit 1`+"\n")
	s.stub("uname", `case "$1" in -s) echo Linux ;; -m) echo aarch64 ;; *) echo Linux ;; esac`+"\n")
	s.stub("apt-get", `echo "apt-get $*" >>"$CV_LOG"; exit 100`+"\n")
	s.stub("apt-cache", cvAptCacheStub)
	s.stub("dpkg-query", cvDpkgQueryStub)
	s.stub("dpkg", `case "$1" in --print-architecture) echo arm64 ;; *) exit 1 ;; esac`+"\n")

	r := s.run(harness, `_apt_converge || true
_converge claude "$CLAUDE_CODE_VERSION" _kept _noinst required || true
_apt_security_upgrade || true
_converge_report`, "CV_CODENAME="+cvHostCodename())

	if r.rc != 1 {
		t.Errorf("exit code = %d, want 1 (a ledgered required failure)\n%s", r.rc, r.out)
	}
	for _, l := range strings.Split(r.log, "\n") {
		if strings.HasPrefix(l, "apt-get ") {
			t.Errorf("apt-get ran without root: %s", l)
		}
		if strings.HasPrefix(l, "NSL ") {
			t.Errorf("the NodeSource list was rewritten without root: %s", l)
		}
		if l == "NOINST" {
			t.Error("the kept claude row ran its INSTALL_FN")
		}
	}
	if r.ledger != "root-layer\n" {
		t.Errorf("CONVERGE_FAILURES = %q, want exactly one root-layer entry although two root-layer functions ran", r.ledger)
	}
	const c6 = "security updates cannot be applied: no root in this container"
	if n := strings.Count(r.out, c6); n != 1 {
		t.Errorf("the C-6 message appears %d times, want 1 (DD:176)\n%s", n, r.out)
	}
	lines := strings.Split(r.out, "\n")
	c6At, tableAt := -1, -1
	for i, l := range lines {
		if strings.Contains(l, c6) {
			c6At = i
			if !strings.HasPrefix(l, "[ERROR] ") {
				t.Errorf("the C-6 message must be a log_error line: %q", l)
			}
			for _, want := range []string{"(id -u = 1000; sudo -n true failed)", "the Node.js pin did not converge; everything else did (see the table above)"} {
				if !strings.Contains(l, want) {
					t.Errorf("the C-6 message drifted from DD:176; missing %q in %q", want, l)
				}
			}
			if m := regexp.MustCompile(`--[a-z]`).FindString(l); m != "" || strings.Contains(l, "docker exec") {
				t.Errorf("the C-6 message names a flag or a host-side step (DD:288): %q", l)
			}
		}
		if !strings.HasPrefix(l, "[OK]") && strings.Contains(l, "claude") && strings.Contains(l, "kept") && c6At == -1 {
			tableAt = i
		}
	}
	if c6At >= 0 && (tableAt == -1 || tableAt > c6At) {
		t.Errorf("the C-6 message must follow the summary table (no table row for claude/kept precedes it)\n%s", r.out)
	}
	if rec, ok := cvReadRecord(t, s.afRoot); ok {
		if rec.Mode.CheckOnly {
			t.Error("convergence.json .mode.check_only must be false outside --check")
		}
		want := map[string]string{"claude": "kept", "ubuntu-security": "FAILED", "nodejs": "FAILED"}
		for _, c := range rec.Components {
			if w, ok := want[c.Name]; ok {
				if c.Result != w {
					t.Errorf("convergence.json %s result %q, want %q", c.Name, c.Result, w)
				}
				delete(want, c.Name)
			}
		}
		for n := range want {
			t.Errorf("convergence.json lacks the %s row; every other row is still recorded", n)
		}
	}
}

// ---------------------------------------------------------------------------------------------

func TestQuickstartGoEnvBeforeSyncFormulas(t *testing.T) {
	content := readQuickstart(t)
	code := cvCode(content)
	const convergeGo = `_converge go "$GO_TOOLCHAIN_VERSION" _probe_go _install_go required || true`
	const goEnv = `go env -w GOTOOLCHAIN="${GO_TOOLCHAIN_VERSION}+auto"`

	t.Run("install_af converges Go before the first make", func(t *testing.T) {
		body := cvCode(topLevelShellFunction(content, "install_af"))
		cvSequence(t, "install_af", body, []cvAnchor{
			cvLit("uname -s"),
			cvLit(convergeGo),
			cvLine(`readlink -f "$(command -v go`, `readlink -f "\$\(command -v go`),
			cvLit(goEnv),
			cvLit("make sync-formulas"),
		})
		sync := strings.Index(body, "make sync-formulas")
		for _, a := range []string{convergeGo, goEnv} {
			if i := strings.Index(body, a); i == -1 || (sync != -1 && sync < i) {
				t.Errorf("install_af: the first `make sync-formulas` must come after `%s` (E4)", a)
			}
		}
	})

	t.Run("file-wide the Go converge precedes the first make sync-formulas", func(t *testing.T) {
		c, m := strings.Index(code, "_converge go "), strings.Index(code, "make sync-formulas")
		if c == -1 || m == -1 || c > m {
			t.Errorf("first `_converge go` at %d, first `make sync-formulas` at %d; the toolchain converges first", c, m)
		}
	})

	t.Run("an exported GOTOOLCHAIN that overrides go env -w is a loud stop (DD:179)", func(t *testing.T) {
		pin := quickstartPin(t, "GO_TOOLCHAIN_VERSION")
		stop := "[ERROR] go version reports go1.25.5, not " + pin + " — an exported GOTOOLCHAIN overrides go env -w"
		for _, tc := range []struct {
			name   string
			extra  []string
			loud   bool
			ledger string
		}{
			{"no export", nil, false, ""},
			{"GOTOOLCHAIN=local", []string{"GOTOOLCHAIN=local"}, false, ""},
			{"GOTOOLCHAIN=go1.25.5", []string{"GOTOOLCHAIN=go1.25.5"}, true, "go\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s := newCvSandbox(t)
				harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error", "log_step",
					"command_exists", "_arch", "_record_row", "_quarantine_stale", "_converge", "install_af")
				s.writeFile(filepath.Join(s.src, "go.mod"), "module github.com/stempeck/agentfactory\n", 0o644)
				// The prefix binary reports the toolchain it would run: the pin, unless an exported
				// GOTOOLCHAIN names another release.
				prefixGo := s.writeFile(".local/share/agentfactory/go/bin/go", `#!/bin/sh
case "$1" in
version) case "${GOTOOLCHAIN-}" in go1.25.5) v=go1.25.5 ;; *) v=`+pin+` ;; esac; echo "go version $v linux/arm64" ;;
env) echo "go $* GOTOOLCHAIN=${GOTOOLCHAIN-}" >>"$CV_LOG" ;;
esac
`, 0o755)
				if err := os.MkdirAll(filepath.Join(s.home, ".local/bin"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(prefixGo, filepath.Join(s.home, ".local/bin/go")); err != nil {
					t.Fatal(err)
				}
				s.stub("uname", `case "$1" in -m) echo aarch64 ;; *) echo Linux ;; esac`+"\n")
				s.stub("make", `echo "make $* GOTOOLCHAIN=${GOTOOLCHAIN-}" >>"$CV_LOG"`+"\n")
				s.stub("cp", `echo "cp $*" >>"$CV_LOG"`+"\n")
				s.stub("af", "echo af test\n")

				r := s.run(harness, "install_af", tc.extra...)
				if r.rc != 0 {
					t.Errorf("install_af rc = %d, want 0 (D-66: the ledger carries the outcome)\n%s", r.rc, r.out)
				}
				if got := strings.Contains(r.out, stop); got != tc.loud {
					t.Errorf("loud stop printed = %v, want %v (exact DD:179 shape %q)\n%s", got, tc.loud, stop, r.out)
				}
				if r.ledger != tc.ledger {
					t.Errorf("CONVERGE_FAILURES = %q, want %q", r.ledger, tc.ledger)
				}
				if tc.loud {
					cvExpectRow(t, r.rows, "go", pin, "go version reports go1.25.5", "FAILED")
				} else {
					cvExpectRow(t, r.rows, "go", pin, pin, "kept")
				}
				builds := 0
				for _, l := range strings.Split(r.log, "\n") {
					if !strings.HasPrefix(l, "make ") {
						continue
					}
					builds++
					if tc.loud && !strings.HasSuffix(l, " GOTOOLCHAIN=local") {
						t.Errorf("after the loud stop af must build with the pinned prefix toolchain (GOTOOLCHAIN=local): %s", l)
					}
				}
				if builds == 0 {
					t.Errorf("install_af ran no make\n%s", r.log)
				}
			})
		}
	})

	t.Run("_install_go verifies, unpacks into the prefix and links", func(t *testing.T) {
		body := extractShellFunction(content, "_install_go")
		if body == "" {
			t.Fatal("_install_go() is not defined (nested in install_af, 1e)")
		}
		body = cvCode(body)
		for _, want := range []string{"sha256sum --check --strict", "GO_SHA256_", ".local/share/agentfactory/go", "ln -sfn"} {
			if !strings.Contains(body, want) {
				t.Errorf("_install_go() lacks %q", want)
			}
		}
		step := regexp.MustCompile(`(^|[^A-Za-z0-9_-])(curl|wget|tar|sha256sum|ln -sfn|mv)\s`)
		guarded := regexp.MustCompile(`(\|\|\s*return 1|\|\|\s*\{[^}]*return 1;?\s*\})\s*$`)
		logical := strings.ReplaceAll(body, "\\\n", " ")
		for _, l := range strings.Split(logical, "\n") {
			tl := strings.TrimSpace(l)
			if !step.MatchString(tl) || strings.HasPrefix(tl, "if !") {
				continue
			}
			if !guarded.MatchString(tl) {
				t.Errorf("_install_go(): `%s` must end with `|| return 1` — `_converge … || true` disables errexit inside the INSTALL_FN", tl)
			}
		}
	})
}

// ---------------------------------------------------------------------------------------------

func cvFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestQuickstartTelemetryStopSigtermThenKill(t *testing.T) {
	content := readQuickstart(t)
	raw := topLevelShellFunction(content, "_telemetry_stop")

	t.Run("SIGTERM, then a bounded kill -0 poll, then kill-session", func(t *testing.T) {
		if raw == "" {
			t.Fatal("_telemetry_stop() is not defined at top level (D-22)")
		}
		body := cvCode(raw)
		term := `kill (-TERM|-s TERM|-15)\b`
		cvSequence(t, "_telemetry_stop", body, []cvAnchor{
			cvLine("kill -TERM", term),
			cvLit("kill -0"),
			cvLit("tmux kill-session -t telemetry"),
		})
		if ks, tm := strings.Index(body, "kill-session"), regexp.MustCompile(term).FindStringIndex(body); tm == nil || (ks != -1 && ks < tm[0]) {
			t.Error("_telemetry_stop: no kill-session may precede the SIGTERM")
		}
		if !strings.Contains(body, `list-panes -t telemetry -F '#{pane_pid}'`) {
			t.Error("_telemetry_stop must find the pane with `tmux list-panes -t telemetry -F '#{pane_pid}'` (keeps has-session -t telemetry at 3)")
		}
		if !strings.Contains(body, "sleep 1") {
			t.Error("_telemetry_stop must poll with `sleep 1` ticks")
		}
		bounded := false
		for _, m := range regexp.MustCompile(`-(lt|le)[ \t]+"?([0-9]+)"?`).FindAllStringSubmatch(body, -1) {
			if n, _ := strconv.Atoi(m[2]); n > 0 && n <= 30 {
				bounded = true
			}
		}
		if !bounded {
			t.Error("_telemetry_stop's kill -0 poll must be bounded by a literal <= 30")
		}
		for _, re := range []*regexp.Regexp{
			regexp.MustCompile(`(?m)(^|[;&|(]|\bthen|\bdo|\belse)[ \t]*wait\b`),
			regexp.MustCompile(`(?m)(^|[;&|(]|\bthen|\bdo|\belse|\$\()[ \t]*timeout[ \t]`),
			regexp.MustCompile(`tail --pid`),
		} {
			if m := re.FindString(body); m != "" {
				t.Errorf("_telemetry_stop uses %q; OpenObserve belongs to the tmux server, so only a kill -0 poll works (and macOS has no timeout)", strings.TrimSpace(m))
			}
		}
	})

	t.Run("both launch lines bind gRPC to loopback", func(t *testing.T) {
		var launch []string
		for _, l := range strings.Split(content, "\n") {
			if strings.Contains(l, `ZO_HTTP_ADDR="127.0.0.1"`) {
				launch = append(launch, l)
			}
		}
		if len(launch) != 2 {
			t.Errorf("want the relaunch heredoc and the setup_telemetry launch line (2 lines with ZO_HTTP_ADDR); got %d", len(launch))
		}
		for _, l := range launch {
			if !strings.Contains(l, `ZO_GRPC_ADDR="127.0.0.1"`) {
				t.Errorf("launch line lacks ZO_GRPC_ADDR=\"127.0.0.1\" (OpenObserve's gRPC listener defaults to the wildcard address): %s", strings.TrimSpace(l))
			}
		}
	})

	t.Run("no wildcard address anywhere", func(t *testing.T) {
		if n := strings.Count(content, "0.0.0.0"); n != 0 {
			t.Errorf("quickstart.sh mentions 0.0.0.0 %d times", n)
		}
	})

	t.Run("the binary swap precedes the stop and the marker is written last", func(t *testing.T) {
		body := topLevelShellFunction(content, "_install_openobserve")
		if body == "" {
			t.Fatal("_install_openobserve() is not defined at top level (D-22)")
		}
		body = cvCode(body)
		cvSequence(t, "_install_openobserve", body, []cvAnchor{
			cvLit(`"$OPENOBSERVE_BIN.new"`),
			cvLit("mv -f"),
			cvLit("_telemetry_stop"),
			cvLit("openobserve.version"),
		})
		if st, mv := strings.Index(body, "_telemetry_stop"), strings.Index(body, "mv -f"); st != -1 && mv != -1 && st < mv {
			t.Error("_install_openobserve stops the backend before the atomic swap; a watchdog relaunch in between would start the OLD binary (D-58)")
		}
	})

	t.Run("dynamic", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("the pane process is observed through /proc")
		}
		if raw == "" {
			t.Fatal("_telemetry_stop() is not defined at top level (D-22)")
		}
		realSleep, err := exec.LookPath("sleep")
		if err != nil {
			t.Fatal(err)
		}
		realBash, err := exec.LookPath("bash")
		if err != nil {
			t.Fatal(err)
		}
		setup := func(t *testing.T) (*cvSandbox, string) {
			s := newCvSandbox(t)
			harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error",
				"command_exists", "_port_in_use", "_port_owner_pid", "_telemetry_stop")
			harness += fmt.Sprintf("TELEMETRY_PORT=%d\n", cvFreePort(t))
			s.stub("tmux", `echo "tmux $*" >>"$CV_LOG"
case "$1" in
list-panes|has-session) [ -n "${CV_PANE_PID-}" ] || exit 1; [ "$1" = list-panes ] && echo "$CV_PANE_PID"; exit 0 ;;
kill-session) [ -n "${CV_PANE_PID-}" ] && kill -9 "$CV_PANE_PID" 2>/dev/null; exit 0 ;;
*) exit 0 ;;
esac
`)
			s.stub("sleep", "echo \"sleep $*\" >>\"$CV_LOG\"\nexec '"+realSleep+"' 0.05\n")
			return s, harness
		}
		start := func(t *testing.T, name string, args ...string) (*exec.Cmd, chan *os.ProcessState) {
			cmd := exec.Command(name, args...)
			cmd.Env = []string{"PATH=/usr/bin:/bin"}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan *os.ProcessState, 1)
			go func() { _ = cmd.Wait(); done <- cmd.ProcessState }()
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", cmd.Process.Pid)); strings.TrimSpace(string(b)) == "sleep" {
					return cmd, done
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("pane process never reached exec sleep")
			return nil, nil
		}
		waitState := func(t *testing.T, done chan *os.ProcessState) *os.ProcessState {
			select {
			case st := <-done:
				return st
			case <-time.After(10 * time.Second):
				t.Fatal("pane process still alive after _telemetry_stop returned")
				return nil
			}
		}

		t.Run("a backend that honours SIGTERM drains without SIGKILL", func(t *testing.T) {
			s, harness := setup(t)
			cmd, done := start(t, realSleep, "600")
			r := s.run(harness, "_telemetry_stop", fmt.Sprintf("CV_PANE_PID=%d", cmd.Process.Pid))
			if r.rc != 0 {
				t.Errorf("rc = %d\n%s", r.rc, r.out)
			}
			st := waitState(t, done)
			if ws, ok := st.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
				t.Errorf("the pane process ended with %v; it must receive SIGTERM first", st)
			}
			if n := cvCountLines(r.log, "sleep "); n >= 30 {
				t.Errorf("the poll ran %d ticks although the process exited on SIGTERM", n)
			}
		})

		t.Run("a backend that ignores SIGTERM is killed after at most 30 ticks", func(t *testing.T) {
			s, harness := setup(t)
			cmd, done := start(t, realBash, "-c", `trap "" TERM; exec `+realSleep+` 600`)
			r := s.run(harness, "_telemetry_stop", fmt.Sprintf("CV_PANE_PID=%d", cmd.Process.Pid))
			if r.rc != 0 {
				t.Errorf("rc = %d\n%s", r.rc, r.out)
			}
			waitState(t, done)
			ticks := cvCountLines(r.log, "sleep ")
			if ticks < 1 || ticks > 30 {
				t.Errorf("the kill -0 poll ran %d ticks; want 1..30", ticks)
			}
			lastSleep, kill := strings.LastIndex(r.log, "sleep "), strings.Index(r.log, "tmux kill-session -t telemetry")
			if kill == -1 || kill < lastSleep {
				t.Errorf("tmux kill-session -t telemetry must follow the poll\n%s", r.log)
			}
		})

		t.Run("no telemetry session is a no-op", func(t *testing.T) {
			s, harness := setup(t)
			r := s.run(harness, "_telemetry_stop", "CV_PANE_PID=")
			if r.rc != 0 {
				t.Errorf("rc = %d, want 0 when no telemetry session exists\n%s", r.rc, r.out)
			}
			if strings.Contains(r.log, "sleep ") || strings.Contains(r.log, "kill-session") {
				t.Errorf("no session, yet the stop polled or killed:\n%s", r.log)
			}
		})
	})
}
