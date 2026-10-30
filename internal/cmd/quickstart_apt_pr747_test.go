package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// apHarness is newAptHarness hardened for helpers that take a host path as an argument. It runs as
// STUB_UID=0 with real mv/mkdir/rm, so a helper that ignored its argument would rename or write the
// host's /etc: every mutating tool is a guard that refuses an absolute path outside the test's HOME
// (ADR-018), sudo can run only those guarded tools, and the ledger and rows survive the END exit.
type apHarness struct {
	aptHarness
	ledgerPath, rowsPath string
}

type apResult struct {
	out, ledger, log string
	rc               int
	rows             []cvRow
}

func apGuard(tag string) string {
	return `echo "` + tag + ` $*" >>"$LOG"
for a in "$@"; do
  p="${a#--*=}"
  case "$p" in
    "$AP_SANDBOX"|"$AP_SANDBOX"/*) ;;
    /*) echo "MUTATION ` + tag + ` outside the sandbox: $*" >>"$LOG"; echo "` + tag + `: refusing $p outside the test sandbox" >&2; exit 1 ;;
  esac
done
`
}

// Ownership flags are dropped because the test process is not really root; the mode still applies.
const apInstallStripOwner = `skip=0
for a in "$@"; do
  shift
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  case "$a" in
    -o|-g|--owner|--group) skip=1; continue ;;
    -o*|-g*|--owner=*|--group=*) continue ;;
  esac
  set -- "$@" "$a"
done
`

const apSudoStub = `echo "SUDO $*" >>"$LOG"
if [ "$1" != -n ]; then echo "MUTATION sudo without -n: $*" >>"$LOG"; exit 1; fi
shift
[ "$1" = true ] && exit "${STUB_SUDO_RC:-0}"
[ "$1" != "${AP_SUDO_FAIL:-}" ] || exit 1
case "$1" in
  env|apt-get|apt-cache|dpkg-query|mv|install|cp|tee|chmod|chown|chgrp|mkdir|rm|ln|touch|cmp|cat) VIA_SUDO=1; export VIA_SUDO; exec "$@" ;;
esac
echo "MUTATION sudo ran an unexpected command: $*" >>"$LOG"
exit 97
`

const apUnameStub = `case "$1" in -m) echo aarch64 ;; *) echo "${STUB_UNAME_S:-Linux}" ;; esac
`

func apRealTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("resolving real %s for its sandbox guard: %v", name, err)
	}
	return p
}

func newApHarness(t *testing.T, content string, fns ...string) *apHarness {
	t.Helper()
	h := &apHarness{aptHarness: newAptHarness(t, content, fns...)}
	for _, g := range []struct{ tool, tag, before string }{
		{"mv", "MV", `[ -z "${AP_MV_RC:-}" ] || exit "$AP_MV_RC"` + "\n"},
		{"rm", "RM", ""}, {"mkdir", "MKDIR", ""}, {"ln", "LN", ""}, {"chmod", "CHMOD", ""},
		{"touch", "TOUCH", ""}, {"mktemp", "MKTEMP", ""}, {"cp", "CP", ""}, {"tee", "TEE", ""},
	} {
		writeConvergeStub(t, h.bin, g.tool, apGuard(g.tag)+g.before+"exec '"+apRealTool(t, g.tool)+"' \"$@\"\n")
	}
	writeConvergeStub(t, h.bin, "install", apGuard("INSTALL")+apInstallStripOwner+"exec '"+apRealTool(t, "install")+"' \"$@\"\n")
	writeConvergeStub(t, h.bin, "chown", apGuard("CHOWN")+"exit 0\n")
	writeConvergeStub(t, h.bin, "chgrp", apGuard("CHGRP")+"exit 0\n")
	writeConvergeStub(t, h.bin, "sudo", apSudoStub)
	writeConvergeStub(t, h.bin, "uname", apUnameStub)
	dump := t.TempDir()
	h.ledgerPath, h.rowsPath = filepath.Join(dump, "ledger"), filepath.Join(dump, "rows")
	h.script += cvDumpTrap
	h.env = append(h.env, "AP_SANDBOX="+h.home, "CV_LEDGER="+h.ledgerPath, "CV_ROWS="+h.rowsPath)
	return h
}

// withReal appends a function's real body after the harness overrides, so it is the one that runs.
func (h *apHarness) withReal(t *testing.T, content, fn, contract string) {
	t.Helper()
	body := topLevelShellFunction(content, fn)
	if body == "" {
		t.Fatalf("quickstart.sh defines no top-level %s(); %s", fn, contract)
	}
	h.script += body + "\n"
}

var apHostPathRe = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])/(etc/apt|usr/share/keyrings)/`)

// withSysroot runs the real _security_source_present and _ensure_nodesource_list with their fixed
// host paths re-pointed at a sandbox root in the extracted text, so their own operands are
// exercised against fixtures instead of the host's apt configuration.
func (h *apHarness) withSysroot(t *testing.T, content string) string {
	t.Helper()
	sys := filepath.Join(h.home, "sysroot")
	if strings.ContainsAny(sys, " \t'\"()$`\\;&|<>*?[]{}") {
		t.Fatalf("sysroot %q is spliced unquoted into function text; keep the test name free of shell metacharacters", sys)
	}
	for _, fn := range []string{"_security_source_present", "_ensure_nodesource_list"} {
		body := topLevelShellFunction(content, fn)
		if !strings.Contains(body, "/etc/apt/") {
			t.Fatalf("%s() names no /etc/apt/ path; re-point the sysroot substitution before running it as root", fn)
		}
		h.script += body + "\n"
	}
	for _, p := range []string{"/etc/apt/", "/usr/share/keyrings/"} {
		h.script = strings.ReplaceAll(h.script, p, sys+p)
	}
	if m := apHostPathRe.FindString(h.script); m != "" {
		t.Fatalf("the harness still names the host path %q after the sysroot substitution", m)
	}
	return sys
}

func (h *apHarness) run(t *testing.T, call string, env ...string) apResult {
	t.Helper()
	for _, p := range []string{h.logPath, h.ledgerPath, h.rowsPath} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, rc := runConvergeHarness(t, h.script, call, append(append([]string{}, h.env...), env...))
	ledger, _ := os.ReadFile(h.ledgerPath)
	rows, _ := os.ReadFile(h.rowsPath)
	r := apResult{out: out, rc: rc, ledger: string(ledger), log: h.log(t), rows: cvParseRows(string(rows))}
	for _, l := range logLines(r.log, "MUTATION ") {
		t.Errorf("%s", l)
	}
	return r
}

func (h *apHarness) policyPath(pkg string) string { return filepath.Join(h.home, "policy", pkg) }

func (h *apHarness) statePath(pkg string) string { return filepath.Join(h.home, "dpkg-state", pkg) }

func apWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func apRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func apAbsent(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists; %s", path, why)
	}
}

const apUbuntuSources = `Types: deb
URIs: http://ports.ubuntu.com/ubuntu-ports/
Suites: noble noble-updates noble-backports
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
`

const apSecurityStanza = `
Types: deb
URIs: http://ports.ubuntu.com/ubuntu-ports/
Suites: noble-security
Components: main restricted universe multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
`

func apNodeSourceLine(sys, major string) string {
	return "deb [arch=arm64 signed-by=" + sys + "/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_" +
		major + ".x nodistro main\n"
}

// apOtherMajor is a NodeSource major no pin will return to.
const apOtherMajor = "16"

type apSysroot struct {
	sys, major, sources, nodeList, keyring string
}

// apDefaultSysroot is a stock agentfactory image: the noble-security pocket configured, and the
// signed NodeSource source on NODE_MAJOR.
func apDefaultSysroot(t *testing.T, h *apHarness, sys string) apSysroot {
	t.Helper()
	s := apSysroot{
		sys:      sys,
		major:    h.nodeMajor,
		sources:  filepath.Join(sys, "etc/apt/sources.list.d/ubuntu.sources"),
		nodeList: filepath.Join(sys, "etc/apt/sources.list.d/nodesource.list"),
		keyring:  filepath.Join(sys, "usr/share/keyrings/nodesource.gpg"),
	}
	apWrite(t, s.sources, apUbuntuSources+apSecurityStanza)
	apWrite(t, s.nodeList, apNodeSourceLine(sys, h.nodeMajor))
	apWrite(t, s.keyring, "not a real keyring\n")
	return s
}

func apExpectRow(t *testing.T, rows []cvRow, name, pin, installed, result string, checkSuffixOK bool) {
	t.Helper()
	got := cvRowsNamed(rows, name)
	if len(got) != 1 {
		t.Errorf("CONVERGE_ROWS must hold exactly one %s row; got %d in %+v", name, len(got), rows)
		return
	}
	r := got[0]
	inst := r.installed == installed || (checkSuffixOK && strings.HasPrefix(r.installed, installed+" (apt lists "))
	if r.pin != pin || !inst || r.result != result {
		t.Errorf("%s row = pin %q installed %q result %q; want pin %q installed %q result %q",
			name, r.pin, r.installed, r.result, pin, installed, result)
	}
}

func apRealAptLines(log string) []string {
	var out []string
	for _, l := range logLines(log, "APT ") {
		if !containsWord(strings.Fields(l), "-s") {
			out = append(out, l)
		}
	}
	return out
}

const (
	apPass1Report      = "_apt_converge\n_converge_report"
	apBothPassesReport = "_apt_converge\n_apt_security_upgrade\n_converge_report"
	apCheckReport      = "CHECK_ONLY=true\n_apt_converge\n_converge_report"
)

// TestQuickstartRootLayerLedger pins every root-layer failure branch (T12): the exact ledger names and
// their multiplicity, the row labels (kept is never converged, no source is never kept) and the END
// exit. Each case names the mutant it kills.
func TestQuickstartRootLayerLedger(t *testing.T) {
	content := readQuickstart(t)
	pin := quickstartPin(t, "NODE_APT_VERSION")
	const sec, old = "noble-security", "22.22.2-1nodesource1"
	c7 := `[ERROR] cannot pin nodejs=` + pin + `: apt said "`
	nothingPending := func(t *testing.T, h *apHarness, _ apSysroot) {
		if err := os.Remove(h.policyPath("libkrb5-3")); err != nil {
			t.Fatal(err)
		}
	}
	nodePinned := func(t *testing.T, h *apHarness, _ apSysroot) { apWrite(t, h.statePath("nodejs"), pin) }
	noSecurity := func(t *testing.T, _ *apHarness, s apSysroot) { apWrite(t, s.sources, apUbuntuSources) }
	noNodeList := func(t *testing.T, _ *apHarness, s apSysroot) {
		if err := os.Remove(s.nodeList); err != nil {
			t.Fatal(err)
		}
	}
	nodeListElsewhere := func(t *testing.T, _ *apHarness, s apSysroot) {
		apWrite(t, s.nodeList, apNodeSourceLine(s.sys, apOtherMajor))
	}
	noKeyring := func(t *testing.T, _ *apHarness, s apSysroot) {
		if err := os.Remove(s.keyring); err != nil {
			t.Fatal(err)
		}
	}
	nodeSourceWarn := func(s apSysroot) string {
		return "[WARN] no NodeSource apt source (" + s.nodeList + " or " + s.keyring + " is absent); nodejs=" + pin +
			" can resolve only from another configured source"
	}

	cases := []struct {
		name, kills string
		setup       []func(*testing.T, *apHarness, apSysroot)
		call        string
		env         []string
		ledger      string
		security    [2]string
		node        [2]string
		rc          int
		check       bool
		outHas      func(apSysroot) []string
		outLacks    []string
		after       func(*testing.T, apSysroot, apResult)
	}{
		{
			name: "apt-get update fails: both rows FAILED, both names ledgered once", kills: "Q67",
			call: apPass1Report, env: []string{"APT_FAIL=update"},
			ledger: "ubuntu-security\nnodejs\n", security: [2]string{"update failed", "FAILED"}, node: [2]string{old, "FAILED"}, rc: 1,
			outHas: func(apSysroot) []string {
				return []string{"[ERROR] apt-get update failed: E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/dists/noble-security/InRelease",
					c7 + "E: Failed to fetch", "[ERROR] convergence failed for: ubuntu-security nodejs (see the table above)"}
			},
			after: func(t *testing.T, _ apSysroot, r apResult) {
				for _, l := range apRealAptLines(r.log) {
					if strings.Contains(l, "--only-upgrade") || strings.Contains(l, "nodejs=") {
						t.Errorf("a failed update must stop the root layer before any install: %s", l)
					}
				}
			},
		},
		{
			name: "pocket upgrade fails: ubuntu-security alone is ledgered and nodejs still converges", kills: "Q68",
			call: apPass1Report, env: []string{"APT_FAIL=only-upgrade"},
			ledger: "ubuntu-security\n", security: [2]string{"1 pending", "FAILED"}, node: [2]string{pin, "converged"}, rc: 1,
			outHas: func(apSysroot) []string {
				return []string{"[ERROR] ubuntu-security: the noble-security upgrade failed: E: Sub-process /usr/bin/dpkg returned an error code (1)",
					"[OK] nodejs: " + pin + " (converged from " + old + ")",
					"[ERROR] convergence failed for: ubuntu-security (see the table above)"}
			},
		},
		{
			name: "nodejs pin does not resolve: nodejs is ledgered and the END prints the C-7 hint", kills: "Q69, Q47",
			call: apPass1Report, env: []string{"APT_FAIL=nodejs"},
			ledger: "nodejs\n", security: [2]string{sec, "converged"}, node: [2]string{old, "FAILED"}, rc: 1,
			outHas: func(apSysroot) []string {
				return []string{"[OK] ubuntu-security: noble-security (converged from 1 pending)",
					"[ERROR] nodejs: " + old + " != pinned " + pin,
					c7 + "E: Version '" + pin + "' for 'nodejs' was not found\"",
					"[ERROR] convergence failed for: nodejs (see the table above)"}
			},
		},
		{
			name: "no security source and nothing pending: no source is FAILED and ledgered, never kept", kills: "Q70, Q54/Q62 (real-run half)",
			setup:  []func(*testing.T, *apHarness, apSysroot){nothingPending, noSecurity},
			call:   apPass1Report,
			ledger: "ubuntu-security\n", security: [2]string{"no source", "FAILED"}, node: [2]string{pin, "converged"}, rc: 1,
			outHas: func(apSysroot) []string {
				return []string{"[ERROR] security updates cannot be applied: no apt source for the noble-security pocket (checked",
					"[ERROR] convergence failed for: ubuntu-security (see the table above)"}
			},
		},
		{
			name: "security source present and nothing pending: kept, never converged, exit 0", kills: "Q55/Q65 (real-run half)",
			setup:  []func(*testing.T, *apHarness, apSysroot){nothingPending, nodePinned},
			call:   apPass1Report,
			ledger: "", security: [2]string{sec, "kept"}, node: [2]string{pin, "kept"}, rc: 0,
			outHas: func(apSysroot) []string {
				return []string{"[OK] ubuntu-security: noble-security (kept)", "[OK] nodejs: " + pin + " (kept)"}
			},
			outLacks: []string{"af down && af up", "convergence failed"},
		},
		{
			name: "no apt-get on PATH: both rows FAILED, each name ledgered once across both passes", kills: "Q71",
			setup: []func(*testing.T, *apHarness, apSysroot){func(t *testing.T, h *apHarness, _ apSysroot) {
				if err := os.Remove(filepath.Join(h.bin, "apt-get")); err != nil {
					t.Fatal(err)
				}
			}},
			call:   apBothPassesReport,
			ledger: "ubuntu-security\nnodejs\n", security: [2]string{"no apt-get", "FAILED"}, node: [2]string{"no apt-get", "FAILED"}, rc: 1,
			outHas: func(apSysroot) []string {
				return []string{"[ERROR] security updates cannot be applied: no apt-get on PATH.",
					"[ERROR] convergence failed for: ubuntu-security nodejs (see the table above)"}
			},
			after: func(t *testing.T, _ apSysroot, r apResult) {
				if l := logLines(r.log, "APT "); len(l) != 0 {
					t.Errorf("no apt-get is on PATH, yet apt-get ran: %q", l)
				}
			},
		},
		{
			name: "pass-2 failure replaces the converged pass-1 row and is ledgered", kills: "Q76",
			call:   "_apt_converge\nAPT_FAIL=only-upgrade\nexport APT_FAIL\n_apt_security_upgrade\n_converge_report",
			ledger: "ubuntu-security\n", security: [2]string{"1 pending", "FAILED"}, node: [2]string{pin, "converged"}, rc: 1,
			outHas: func(apSysroot) []string {
				return []string{"[OK] ubuntu-security: noble-security (converged from 1 pending)",
					"[ERROR] ubuntu-security: the post-Playwright noble-security upgrade failed: E: Sub-process",
					"[ERROR] convergence failed for: ubuntu-security (see the table above)"}
			},
		},
		{
			name: "both passes succeed: converged rows, empty ledger, exit 0, NodeSource list untouched", kills: "control",
			call:   apBothPassesReport,
			ledger: "", security: [2]string{sec, "converged"}, node: [2]string{pin, "converged"}, rc: 0,
			outHas: func(apSysroot) []string {
				return []string{"[WARN] python packages or system libraries changed: run 'af down && af up' so sessions pick them up"}
			},
			outLacks: []string{"convergence failed", "no NodeSource apt source", "now tracks"},
			after: func(t *testing.T, s apSysroot, _ apResult) {
				if got := apRead(t, s.nodeList); got != apNodeSourceLine(s.sys, s.major) {
					t.Errorf("a NodeSource list already on NODE_MAJOR was rewritten: %q", got)
				}
			},
		},
		{
			name: "NodeSource list absent with nodejs already pinned: warned, kept, not ledgered", kills: "Q80-Q82 (absent-list warn)",
			setup:  []func(*testing.T, *apHarness, apSysroot){noNodeList, nodePinned},
			call:   apPass1Report,
			ledger: "", security: [2]string{sec, "converged"}, node: [2]string{pin, "kept"}, rc: 0,
			outHas: func(s apSysroot) []string { return []string{nodeSourceWarn(s)} },
			after: func(t *testing.T, s apSysroot, _ apResult) {
				apAbsent(t, s.nodeList, "an absent NodeSource list is reported, never bootstrapped (C-7)")
			},
		},
		{
			name: "NodeSource list absent and the pin unresolvable: nodejs ledgered with the C-7 hint", kills: "Q80-Q82, Q69",
			setup: []func(*testing.T, *apHarness, apSysroot){noNodeList},
			call:  apPass1Report, env: []string{"APT_FAIL=nodejs"},
			ledger: "nodejs\n", security: [2]string{sec, "converged"}, node: [2]string{old, "FAILED"}, rc: 1,
			outHas: func(s apSysroot) []string {
				return []string{nodeSourceWarn(s), c7 + "E: Version '" + pin + "' for 'nodejs' was not found\""}
			},
		},
		{
			name: "NodeSource keyring absent: warned, and a list on another major is not rewritten", kills: "Q80-Q82 (keyring operand)",
			setup:  []func(*testing.T, *apHarness, apSysroot){noKeyring, nodeListElsewhere},
			call:   apPass1Report,
			ledger: "", security: [2]string{sec, "converged"}, node: [2]string{pin, "converged"}, rc: 0,
			outHas:   func(s apSysroot) []string { return []string{nodeSourceWarn(s)} },
			outLacks: []string{"now tracks"},
			after: func(t *testing.T, s apSysroot, _ apResult) {
				if got := apRead(t, s.nodeList); got != apNodeSourceLine(s.sys, apOtherMajor) {
					t.Errorf("the NodeSource list was rewritten although its keyring is absent: %q", got)
				}
			},
		},
		{
			name: "NodeSource list on another major is re-pointed at NODE_MAJOR", kills: "Q80-Q82 (rewrite)",
			setup:  []func(*testing.T, *apHarness, apSysroot){nodeListElsewhere},
			call:   apPass1Report,
			ledger: "", security: [2]string{sec, "converged"}, node: [2]string{pin, "converged"}, rc: 0,
			outHas: func(s apSysroot) []string {
				return []string{"[INFO] NodeSource apt source now tracks node_" + s.major + ".x"}
			},
			outLacks: []string{"no NodeSource apt source"},
			after: func(t *testing.T, s apSysroot, _ apResult) {
				if got, want := apRead(t, s.nodeList), apNodeSourceLine(s.sys, s.major); got != want {
					t.Errorf("NodeSource list = %q, want %q", got, want)
				}
			},
		},
		{
			name: "check: no security source reads would-fail and FAILED, never kept", kills: "Q54/Q62 (check half)",
			setup: []func(*testing.T, *apHarness, apSysroot){nothingPending, noSecurity, nodePinned},
			call:  apCheckReport, check: true,
			ledger: "", security: [2]string{"no source", "FAILED"}, node: [2]string{pin, "kept"}, rc: 1,
			outHas: func(apSysroot) []string {
				return []string{"[WARN] ubuntu-security: no apt source for the noble-security pocket (would fail)",
					"[WARN] drift found: the next ./quickstart.sh or af install --agents converges the rows above"}
			},
		},
		{
			name: "check: source present and nothing pending reads kept, and every component matches", kills: "Q55/Q65 (check half)",
			setup: []func(*testing.T, *apHarness, apSysroot){nothingPending, nodePinned},
			call:  apCheckReport, check: true,
			ledger: "", security: [2]string{sec, "kept"}, node: [2]string{pin, "kept"}, rc: 0,
			outHas: func(apSysroot) []string {
				return []string{"[OK] ubuntu-security: noble-security (kept)", "[OK] nodejs: " + pin + " (kept)",
					"[OK] every component matches its pin"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newApHarness(t, content, "log_step", "_apt_converge", "_apt_security_upgrade", "_apt_get",
				"_pending_security", "_record_row", "_factory_repo_dir", "_converge_report")
			s := apDefaultSysroot(t, h, h.withSysroot(t, content))
			for _, f := range tc.setup {
				f(t, h, s)
			}
			r := h.run(t, tc.call, tc.env...)
			if r.ledger != tc.ledger {
				t.Errorf("CONVERGE_FAILURES = %q, want exactly %q (kills %s)", r.ledger, tc.ledger, tc.kills)
			}
			apExpectRow(t, r.rows, "ubuntu-security", sec, tc.security[0], tc.security[1], tc.check)
			apExpectRow(t, r.rows, "nodejs", pin, tc.node[0], tc.node[1], false)
			if r.rc != tc.rc {
				t.Errorf("END exit = %d, want %d (kills %s)", r.rc, tc.rc, tc.kills)
			}
			if tc.outHas != nil {
				for _, want := range tc.outHas(s) {
					if !strings.Contains(r.out, want) {
						t.Errorf("output lacks %q (kills %s)", want, tc.kills)
					}
				}
			}
			for _, banned := range tc.outLacks {
				if strings.Contains(r.out, banned) {
					t.Errorf("output must not contain %q", banned)
				}
			}
			if rec, ok := cvReadRecord(t, filepath.Join(h.home, "factory")); ok {
				want := map[string]string{"ubuntu-security": tc.security[1], "nodejs": tc.node[1]}
				for _, c := range rec.Components {
					if w, named := want[c.Name]; named {
						if c.Result != w {
							t.Errorf("convergence.json %s result %q, want %q", c.Name, c.Result, w)
						}
						delete(want, c.Name)
					}
				}
				for n := range want {
					t.Errorf("convergence.json lacks the %s row", n)
				}
			}
			if tc.after != nil {
				tc.after(t, s, r)
			}
			if t.Failed() {
				t.Logf("stdout:\n%s\ncalls:\n%s", r.out, r.log)
			}
		})
	}
}

// TestQuickstartCheckOnlySecurityUpgrade pins the --check guard of root-layer pass 2 (T13). No entry
// point reaches it under --check today (main exits after _converge_check), so it is pinned as a
// function-level contract with root and a pending selection, the only gates left standing.
func TestQuickstartCheckOnlySecurityUpgrade(t *testing.T) {
	content := readQuickstart(t)
	fns := append(append([]string{}, aptConvergeFns...), "_apt_security_upgrade")

	t.Run("a real run reaches the pocket install (fixture control)", func(t *testing.T) {
		h := newApHarness(t, content, fns...)
		r := h.run(t, `_apt_security_upgrade; echo "APT_CHANGED=$APT_CHANGED"`)
		installs := apRealAptLines(r.log)
		if len(installs) != 1 || !strings.Contains(installs[0], "install -y --only-upgrade libkrb5-3") {
			t.Errorf("as root with libkrb5-3 pending, pass 2 must run one `install -y --only-upgrade libkrb5-3`; got %q", installs)
		}
		if !strings.Contains(r.out, "APT_CHANGED=1") {
			t.Errorf("the control install changed the dpkg database; want APT_CHANGED=1\n%s", r.out)
		}
	})

	t.Run("CHECK_ONLY installs nothing", func(t *testing.T) {
		h := newApHarness(t, content, fns...)
		r := h.run(t, `CHECK_ONLY=true; _apt_security_upgrade; echo "APT_CHANGED=$APT_CHANGED"`)
		if r.rc != 0 {
			t.Errorf("_apt_security_upgrade under CHECK_ONLY must return 0; rc=%d\n%s", r.rc, r.out)
		}
		if l := logLines(r.log, "APT "); len(l) != 0 {
			t.Errorf("CHECK_ONLY reached apt-get from pass 2 (kills Q73): %q", l)
		}
		if l := logLines(r.log, "SUDO "); len(l) != 0 {
			t.Errorf("CHECK_ONLY must not touch sudo: %q", l)
		}
		if !strings.Contains(r.out, "APT_CHANGED=0") {
			t.Errorf("CHECK_ONLY must leave APT_CHANGED=0\n%s", r.out)
		}
		if r.ledger != "" || len(r.rows) != 0 {
			t.Errorf("CHECK_ONLY pass 2 must record nothing; ledger %q rows %+v", r.ledger, r.rows)
		}
	})
}

func apHelperDefined(t *testing.T, content, fn, contract string) {
	t.Helper()
	body := topLevelShellFunction(content, fn)
	if body == "" {
		t.Fatalf("quickstart.sh defines no top-level %s(); %s", fn, contract)
	}
	for _, re := range cvBash32Avoid {
		if m := re.FindString(cvCode(body)); m != "" {
			t.Errorf("%s() uses %q, which macOS's bash 3.2 lacks (D-11)", fn, strings.TrimSpace(m))
		}
	}
}

const apGhListLine = "deb [arch=arm64 signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\n"

// apGhSourcesDir holds two sources that name cli.github.com (a classic .list and a deb822 .sources
// under a customer's own name) next to sources and files that must never move.
func apGhSourcesDir(t *testing.T, h *apHarness) string {
	t.Helper()
	dir := filepath.Join(h.home, "sources.list.d")
	for name, body := range map[string]string{
		"github-cli.list":      apGhListLine,
		"gh-mirror.sources":    "Types: deb\nURIs: https://cli.github.com/packages\nSuites: stable\nComponents: main\n",
		"ubuntu.sources":       apUbuntuSources + apSecurityStanza,
		"nodesource.list":      "deb [arch=arm64 signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main\n",
		"gh-notes.txt":         "packages come from https://cli.github.com/packages\n",
		"gh-old.list.disabled": apGhListLine,
	} {
		apWrite(t, filepath.Join(dir, name), body)
	}
	return dir
}

var apGhRetired = map[string]string{"github-cli.list": "github-cli.list.disabled", "gh-mirror.sources": "gh-mirror.sources.disabled"}

func apRenamed(before map[string]string, renames map[string]string) map[string]string {
	want := map[string]string{}
	for k, v := range before {
		if to, ok := renames[k]; ok {
			k = to
		}
		want[k] = v
	}
	return want
}

func apSnapshotMatches(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	if d := cvSnapshotDiff(want, got); len(d) > 0 {
		t.Errorf("%s:\n%s", what, strings.Join(d, "\n"))
	}
}

// TestQuickstartGhAptSourceRetired pins the github-cli apt source's retirement (T15): gh converges from
// its release tarball, so the cli.github.com index is dead weight that Error-Mode=any turns into a
// required failure. Matching is by content, and every guard of the --check and no-root paths holds.
func TestQuickstartGhAptSourceRetired(t *testing.T) {
	content := readQuickstart(t)
	const contract = "T15 retires every apt source naming cli.github.com through `_retire_gh_apt_source SOURCES_DIR`"
	retire := `_retire_gh_apt_source "$AP_DIR"; echo "APT_CHANGED=$APT_CHANGED"`

	t.Run("helper is top-level and bash-3.2-safe", func(t *testing.T) {
		apHelperDefined(t, content, "_retire_gh_apt_source", contract)
	})

	t.Run("_apt_converge retires after the NodeSource list and before the update", func(t *testing.T) {
		h := newApHarness(t, content, aptConvergeFns...)
		r := h.run(t, `_apt_converge`)
		nsl, retireAt, update := -1, -1, -1
		var retires []string
		for i, l := range strings.Split(r.log, "\n") {
			switch {
			case strings.HasPrefix(l, "NSL ") && nsl == -1:
				nsl = i
			case strings.HasPrefix(l, "RETIRE "):
				retires = append(retires, l)
				retireAt = i
			case strings.HasPrefix(l, "APT ") && containsWord(strings.Fields(l), "update") && update == -1:
				update = i
			}
		}
		if len(retires) != 1 || retires[0] != "RETIRE /etc/apt/sources.list.d" {
			t.Fatalf("_apt_converge logged %d RETIRE lines %q, want exactly \"RETIRE /etc/apt/sources.list.d\"\nlog:\n%s",
				len(retires), retires, r.log)
		}
		if !(nsl != -1 && nsl < retireAt && retireAt < update) {
			t.Errorf("want NodeSource list < retire < apt-get update; got lines %d, %d, %d\nlog:\n%s", nsl, retireAt, update, r.log)
		}
	})

	t.Run("root renames every source naming cli.github.com", func(t *testing.T) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_retire_gh_apt_source", contract)
		dir := apGhSourcesDir(t, h)
		before := cvSnapshot(t, dir)
		r := h.run(t, retire, "AP_DIR="+dir)
		if r.rc != 0 {
			t.Errorf("rc=%d, want 0\n%s", r.rc, r.out)
		}
		apSnapshotMatches(t, "the retired sources directory", cvSnapshot(t, dir), apRenamed(before, apGhRetired))
		for name := range apGhRetired {
			if !strings.Contains(r.out, name) {
				t.Errorf("the output does not name the retired %s\n%s", name, r.out)
			}
		}
		if l := logLines(r.log, "SUDO "); len(l) != 0 {
			t.Errorf("as root the rename runs directly, never through sudo: %q", l)
		}
		if !strings.Contains(r.out, "APT_CHANGED=0") {
			t.Errorf("a source rename is not a package change; APT_CHANGED must stay 0\n%s", r.out)
		}
		if r.ledger != "" || len(r.rows) != 0 {
			t.Errorf("the retirement records nothing; ledger %q rows %+v", r.ledger, r.rows)
		}
	})

	t.Run("non-root with sudo renames through sudo -n mv", func(t *testing.T) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_retire_gh_apt_source", contract)
		dir := apGhSourcesDir(t, h)
		before := cvSnapshot(t, dir)
		r := h.run(t, retire, "AP_DIR="+dir, "STUB_UID=1000", "STUB_SUDO_RC=0")
		apSnapshotMatches(t, "the retired sources directory", cvSnapshot(t, dir), apRenamed(before, apGhRetired))
		for _, to := range apGhRetired {
			n := 0
			for _, l := range logLines(r.log, "SUDO -n mv ") {
				if strings.Contains(l, filepath.Join(dir, to)) {
					n++
				}
			}
			if n != 1 {
				t.Errorf("want one `sudo -n mv … %s`; got %d\nlog:\n%s", to, n, r.log)
			}
		}
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc=%d ledger %q; want 0 and empty", r.rc, r.ledger)
		}
	})

	t.Run("a source that also names another host is left enabled and named", func(t *testing.T) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_retire_gh_apt_source", contract)
		dir := filepath.Join(h.home, "sources.list.d")
		mixed := map[string]string{
			"shared.list":      apGhListLine + "deb [signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_22.x nodistro main\n",
			"shared.sources":   "Types: deb\nURIs: https://cli.github.com/packages\nSuites: stable\nComponents: main\n\n" + apUbuntuSources,
			"two-uris.sources": "Types: deb\nURIs: https://cli.github.com/packages https://mirror.example.invalid/gh\nSuites: stable\nComponents: main\n",
		}
		for name, body := range mixed {
			apWrite(t, filepath.Join(dir, name), body)
		}
		apWrite(t, filepath.Join(dir, "github-cli.list"), apGhListLine)
		apWrite(t, filepath.Join(dir, "commented.list"), "# "+apGhListLine+"deb https://deb.nodesource.com/node_22.x nodistro main\n")
		before := cvSnapshot(t, dir)
		r := h.run(t, retire, "AP_DIR="+dir)
		if r.rc != 0 {
			t.Errorf("rc=%d, want 0\n%s", r.rc, r.out)
		}
		apSnapshotMatches(t, "a sources directory whose shared files must stay enabled", cvSnapshot(t, dir),
			apRenamed(before, map[string]string{"github-cli.list": "github-cli.list.disabled"}))
		for name := range mixed {
			if !strings.Contains(r.out, name) {
				t.Errorf("the output does not warn about %s, which names cli.github.com beside another source\n%s", name, r.out)
			}
		}
		if strings.Contains(r.out, "commented.list") {
			t.Errorf("commented.list names cli.github.com only in a comment, so it is no github-cli source and needs no warning\n%s", r.out)
		}
	})

	t.Run("no root and no sudo touches nothing", func(t *testing.T) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_retire_gh_apt_source", contract)
		dir := apGhSourcesDir(t, h)
		before := cvSnapshot(t, dir)
		r := h.run(t, retire, "AP_DIR="+dir, "STUB_UID=1000", "STUB_SUDO_RC=1")
		apSnapshotMatches(t, "without root the sources directory must not change", cvSnapshot(t, dir), before)
		if l := logLines(r.log, "SUDO -n mv"); len(l) != 0 {
			t.Errorf("no sudo is available; no rename may be attempted through it: %q", l)
		}
		if r.rc != 0 || r.ledger != "" || len(r.rows) != 0 {
			t.Errorf("rc=%d ledger %q rows %+v; want 0, empty, none (the C-6 root-layer entry owns no-root)", r.rc, r.ledger, r.rows)
		}
	})

	t.Run("CHECK_ONLY touches nothing", func(t *testing.T) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_retire_gh_apt_source", contract)
		dir := apGhSourcesDir(t, h)
		before := cvSnapshot(t, dir)
		r := h.run(t, "CHECK_ONLY=true; "+retire, "AP_DIR="+dir)
		apSnapshotMatches(t, "--check must not change the sources directory (K16)", cvSnapshot(t, dir), before)
		if l := append(logLines(r.log, "SUDO "), logLines(r.log, "MV ")...); len(l) != 0 {
			t.Errorf("--check must not rename or reach sudo: %q", l)
		}
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc=%d ledger %q; want 0 and empty", r.rc, r.ledger)
		}
	})

	t.Run("a failed rename warns and returns 0", func(t *testing.T) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_retire_gh_apt_source", contract)
		dir := apGhSourcesDir(t, h)
		before := cvSnapshot(t, dir)
		r := h.run(t, retire, "AP_DIR="+dir, "AP_MV_RC=1")
		if r.rc != 0 {
			t.Errorf("a failed rename must not fail the run; rc=%d\n%s", r.rc, r.out)
		}
		apSnapshotMatches(t, "the failed rename", cvSnapshot(t, dir), before)
		for name := range apGhRetired {
			warned := false
			for _, l := range strings.Split(r.out, "\n") {
				warned = warned || (strings.HasPrefix(l, "[WARN] ") && strings.Contains(l, name))
			}
			if !warned {
				t.Errorf("no [WARN] line names %s after its rename failed\n%s", name, r.out)
			}
		}
		if r.ledger != "" || len(r.rows) != 0 || !strings.Contains(r.out, "APT_CHANGED=0") {
			t.Errorf("a failed retirement warns only; ledger %q rows %+v\n%s", r.ledger, r.rows, r.out)
		}
	})

	t.Run("an absent sources dir is a no-op", func(t *testing.T) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_retire_gh_apt_source", contract)
		dir := filepath.Join(h.home, "no-sources.d")
		r := h.run(t, retire, "AP_DIR="+dir)
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc=%d ledger %q; want 0 and empty\n%s", r.rc, r.ledger, r.out)
		}
		apAbsent(t, dir, "an absent sources directory is never created")
	})

	t.Run("CHECK_ONLY and no-root runs of _apt_converge never retire", func(t *testing.T) {
		h := newApHarness(t, content, aptConvergeFns...)
		if r := h.run(t, `CHECK_ONLY=true; _apt_converge`); len(logLines(r.log, "RETIRE ")) != 0 {
			t.Errorf("--check reached the retirement: %q", logLines(r.log, "RETIRE "))
		}
		r := h.run(t, `_apt_converge`, "STUB_UID=1000", "STUB_SUDO_RC=1")
		if l := logLines(r.log, "RETIRE "); len(l) != 0 {
			t.Errorf("without root the retirement must not run: %q", l)
		}
		if r.ledger != "root-layer\n" {
			t.Errorf("CONVERGE_FAILURES = %q, want exactly the one root-layer entry", r.ledger)
		}
	})

	t.Run("K5: the only apt update keeps Error-Mode=any", func(t *testing.T) {
		var updates []string
		for _, l := range strings.Split(cvCode(content), "\n") {
			if strings.Contains(l, "_apt_get update") {
				updates = append(updates, strings.TrimSpace(l))
			}
		}
		if len(updates) != 1 || !strings.Contains(updates[0], "_apt_get update -o APT::Update::Error-Mode=any") {
			t.Errorf("want exactly one `_apt_get update -o APT::Update::Error-Mode=any` (spec K5, by design); got %q", updates)
		}
		if !strings.Contains(topLevelShellFunction(content, "_apt_converge"), "_apt_get update -o APT::Update::Error-Mode=any") {
			t.Error("the Error-Mode=any update must stay inside _apt_converge")
		}
	})
}

// apListsFixture is a lists dir whose only noble-security Release files are older than the files
// around them, so "newest" can only be satisfied by honouring the pocket, the codename and *Release.
func apListsFixture(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"ports.ubuntu.com_ubuntu-ports_dists_noble-security_InRelease":                      "2026-10-20T08:00:00Z",
		"archive.ubuntu.com_ubuntu_dists_noble-security_Release":                            "2026-10-19T00:00:00Z",
		"ports.ubuntu.com_ubuntu-ports_dists_noble-security_Release.gpg":                    "2026-10-27T02:00:00Z",
		"ports.ubuntu.com_ubuntu-ports_dists_noble-updates_InRelease":                       "2026-10-25T00:00:00Z",
		"ports.ubuntu.com_ubuntu-ports_dists_jammy-security_InRelease":                      "2026-10-26T00:00:00Z",
		"ports.ubuntu.com_ubuntu-ports_dists_noble-security_main_binary-arm64_Packages.lz4": "2026-10-27T00:00:00Z",
		"cli.github.com_packages_dists_stable_InRelease":                                    "2026-10-27T01:00:00Z",
		"lock": "2026-10-27T03:00:00Z",
	}
	for name, at := range files {
		p := filepath.Join(dir, name)
		apWrite(t, p, name+"\n")
		ts, err := time.Parse(time.RFC3339, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "partial"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func apMtimes(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	m := map[string]time.Time{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = info.ModTime()
	}
	return m
}

// TestQuickstartAptListsFreshness pins T20: --check decides the security pocket against whatever
// lists the last real run left, so the check-mode ubuntu-security rows name the lists' age; the
// helper only reads, and --check still refreshes nothing (K16).
func TestQuickstartAptListsFreshness(t *testing.T) {
	content := readQuickstart(t)
	const contract = "T20 dates the check-mode ubuntu-security rows through `_apt_lists_date LISTS_DIR CODENAME`"
	const at = "2026-10-20T08:00:00Z"
	dated := " (apt lists from " + at + ")"

	t.Run("helper is top-level and bash-3.2-safe", func(t *testing.T) {
		apHelperDefined(t, content, "_apt_lists_date", contract)
	})

	date := func(t *testing.T, dir string) (string, apResult) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_apt_lists_date", contract)
		outFile := filepath.Join(h.home, "lists.out")
		r := h.run(t, `_apt_lists_date "$AP_DIR" noble >"$AP_OUT"`, "AP_DIR="+dir, "AP_OUT="+outFile, "TZ=EST5EDT")
		if r.rc != 0 {
			t.Errorf("_apt_lists_date must return 0; rc=%d\n%s", r.rc, r.out)
		}
		if l := logLines(r.log, "APT "); len(l) != 0 {
			t.Errorf("dating the lists must never refresh them: %q", l)
		}
		data, _ := os.ReadFile(outFile)
		return string(data), r
	}

	t.Run("the newest codename-security Release wins, in UTC", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lists")
		apListsFixture(t, dir)
		before, mtimes := cvSnapshot(t, dir), apMtimes(t, dir)
		got, _ := date(t, dir)
		if got != at+"\n" {
			t.Errorf("_apt_lists_date printed %q, want %q (newest *_dists_noble-security_*Release, UTC)", got, at+"\n")
		}
		apSnapshotMatches(t, "dating the lists must not change them", cvSnapshot(t, dir), before)
		for name, m := range apMtimes(t, dir) {
			if !m.Equal(mtimes[name]) {
				t.Errorf("%s mtime moved from %s to %s", name, mtimes[name], m)
			}
		}
	})

	t.Run("empty lists say never refreshed", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lists")
		apWrite(t, filepath.Join(dir, "lock"), "")
		if err := os.MkdirAll(filepath.Join(dir, "partial"), 0o755); err != nil {
			t.Fatal(err)
		}
		if got, _ := date(t, dir); got != "never refreshed\n" {
			t.Errorf("an image whose lists were removed (Dockerfile rm -rf /var/lib/apt/lists/*) printed %q, want \"never refreshed\"", got)
		}
	})

	t.Run("an absent lists dir says never refreshed", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lists")
		if got, _ := date(t, dir); got != "never refreshed\n" {
			t.Errorf("printed %q, want \"never refreshed\"", got)
		}
		apAbsent(t, dir, "dating the lists never creates them")
	})

	check := func(t *testing.T, setup func(*apHarness, apSysroot), env ...string) apResult {
		h := newApHarness(t, content, aptConvergeFns...)
		s := apDefaultSysroot(t, h, h.withSysroot(t, content))
		if setup != nil {
			setup(h, s)
		}
		r := h.run(t, `CHECK_ONLY=true; _apt_converge`, env...)
		if l := logLines(r.log, "LISTS "); len(l) != 1 || l[0] != "LISTS /var/lib/apt/lists noble" {
			t.Errorf("--check must date the lists once as `_apt_lists_date /var/lib/apt/lists <codename>`; got %q", l)
		}
		for _, l := range logLines(r.log, "APT ") {
			if !containsWord(strings.Fields(l), "-s") || containsWord(strings.Fields(l), "update") {
				t.Errorf("--check ran a mutating apt-get (K16): %s", l)
			}
		}
		if l := logLines(r.log, "SUDO "); len(l) != 0 {
			t.Errorf("--check must not touch sudo: %q", l)
		}
		return r
	}
	nothingPending := func(h *apHarness, _ apSysroot) { _ = os.Remove(h.policyPath("libkrb5-3")) }

	t.Run("check drift row names the lists' date", func(t *testing.T) {
		r := check(t, nil, "STUB_APT_LISTS="+at)
		if line := "[WARN] ubuntu-security: 1 pending != pinned noble-security (would converge)" + dated; !cvHasLine(r.out, line) {
			t.Errorf("stdout lacks the line %q\n%s", line, r.out)
		}
		cvExpectRow(t, r.rows, "ubuntu-security", "noble-security", "1 pending"+dated, "drift")
		pin := quickstartPin(t, "NODE_APT_VERSION")
		if line := "[WARN] nodejs: 22.22.2-1nodesource1 != pinned " + pin + " (would converge)"; !cvHasLine(r.out, line) {
			t.Errorf("the nodejs line must stay undated; stdout lacks %q\n%s", line, r.out)
		}
		cvExpectRow(t, r.rows, "nodejs", pin, "22.22.2-1nodesource1", "drift")
		if n := strings.Count(r.out, "(would converge)"); n != 2 {
			t.Errorf("want one `(would converge)` per drifted row (2); got %d", n)
		}
	})

	t.Run("check kept row names the lists' date", func(t *testing.T) {
		r := check(t, nothingPending, "STUB_APT_LISTS="+at)
		if line := "[OK] ubuntu-security: noble-security (kept)" + dated; !cvHasLine(r.out, line) {
			t.Errorf("stdout lacks the line %q\n%s", line, r.out)
		}
		cvExpectRow(t, r.rows, "ubuntu-security", "noble-security", "noble-security"+dated, "kept")
	})

	t.Run("check no-source row names the lists' date", func(t *testing.T) {
		r := check(t, func(h *apHarness, s apSysroot) {
			nothingPending(h, s)
			apWrite(t, s.sources, apUbuntuSources)
		}, "STUB_APT_LISTS="+at)
		if line := "[WARN] ubuntu-security: no apt source for the noble-security pocket (would fail)" + dated; !cvHasLine(r.out, line) {
			t.Errorf("stdout lacks the line %q\n%s", line, r.out)
		}
		cvExpectRow(t, r.rows, "ubuntu-security", "noble-security", "no source"+dated, "FAILED")
	})

	t.Run("never-refreshed lists say so", func(t *testing.T) {
		r := check(t, nil)
		const never = " (apt lists never refreshed)"
		if line := "[WARN] ubuntu-security: 1 pending != pinned noble-security (would converge)" + never; !cvHasLine(r.out, line) {
			t.Errorf("stdout lacks the line %q\n%s", line, r.out)
		}
		cvExpectRow(t, r.rows, "ubuntu-security", "noble-security", "1 pending"+never, "drift")
	})

	t.Run("a real run's rows carry no lists date", func(t *testing.T) {
		h := newApHarness(t, content, aptConvergeFns...)
		r := h.run(t, `_apt_converge`, "STUB_APT_LISTS="+at)
		cvExpectRow(t, r.rows, "ubuntu-security", "noble-security", "noble-security", "converged")
		if strings.Contains(r.out, "apt lists") {
			t.Errorf("a real run refreshes the lists itself; it names no lists date\n%s", r.out)
		}
	})
}

const apAutoupdaterJSON = `{"env":{"DISABLE_AUTOUPDATER":"1"}}`

// apDropIn returns the one agentfactory drop-in in dir, failing unless exactly one exists.
func apDropIn(t *testing.T, dir string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(dir, "*agentfactory*.json"))
	if len(matches) != 1 {
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("want exactly one *agentfactory*.json drop-in in %s; found %v (directory holds %v)", dir, matches, names)
	}
	return matches[0]
}

func apExpectAutoupdaterPinned(t *testing.T, path string) {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(apRead(t, path)), &doc); err != nil {
		t.Fatalf("%s is not JSON: %v", path, err)
	}
	if got, _ := json.Marshal(doc); string(got) != apAutoupdaterJSON {
		t.Errorf("%s = %s, want exactly %s", path, got, apAutoupdaterJSON)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("%s mode %v, want 0644 (world-readable: every session's claude reads managed settings)", path, info.Mode().Perm())
	}
}

func apSudoWrites(log string) []string {
	var out []string
	for _, l := range logLines(log, "SUDO ") {
		if l != "SUDO -n true" {
			out = append(out, l)
		}
	}
	return out
}

// TestQuickstartManagedAutoupdaterDropIn pins the third DISABLE_AUTOUPDATER site (BODY-1): a managed
// drop-in written by the root layer, which no user settings file can override. It is defence in
// depth, so a failure warns and never ledgers; it never rewrites an operator's managed settings.
func TestQuickstartManagedAutoupdaterDropIn(t *testing.T) {
	content := readQuickstart(t)
	const contract = "BODY-1 writes the managed-scope DISABLE_AUTOUPDATER drop-in through `_pin_managed_autoupdater SETTINGS_DIR`"
	// Not the harness's last line: main calls the helper bare under errexit, and the last line runs
	// inside `|| rc=$?`, where errexit is off.
	pinCall := `_pin_managed_autoupdater "$AP_DIR"; echo "returned"`

	t.Run("helper is top-level and bash-3.2-safe", func(t *testing.T) {
		apHelperDefined(t, content, "_pin_managed_autoupdater", contract)
	})

	t.Run("main pins the managed setting after the root layer", func(t *testing.T) {
		main := cvCode(topLevelShellFunction(content, "main"))
		cvFirstOccurrencesAscend(t, "main", main, []cvAnchor{
			cvLine("_apt_converge", `^[ \t]+_apt_converge$`),
			cvLine("_pin_managed_autoupdater /etc/claude-code/managed-settings.d",
				`^[ \t]+_pin_managed_autoupdater /etc/claude-code/managed-settings\.d$`),
		})
		calls := regexp.MustCompile(`(?m)^[ \t]*[^#\n]*\b_pin_managed_autoupdater [^(]`).FindAllString(cvCode(content), -1)
		if len(calls) != 1 {
			t.Errorf("want exactly one _pin_managed_autoupdater call site (main's); got %q", calls)
		}
		if strings.Contains(topLevelShellFunction(content, "_converge_check"), "_pin_managed_autoupdater") {
			t.Error("_converge_check (--check) must never reach the managed-settings write (K16)")
		}
	})

	sandbox := func(t *testing.T) (*apHarness, string) {
		h := newApHarness(t, content)
		h.withReal(t, content, "_pin_managed_autoupdater", contract)
		return h, filepath.Join(h.home, "etc", "claude-code", "managed-settings.d")
	}

	t.Run("root writes the drop-in", func(t *testing.T) {
		h, dir := sandbox(t)
		r := h.run(t, pinCall, "AP_DIR="+dir)
		if r.rc != 0 {
			t.Errorf("rc=%d, want 0\n%s", r.rc, r.out)
		}
		apExpectAutoupdaterPinned(t, apDropIn(t, dir))
		if l := logLines(r.log, "SUDO "); len(l) != 0 {
			t.Errorf("as root the write runs directly, never through sudo: %q", l)
		}
		if r.ledger != "" || len(r.rows) != 0 {
			t.Errorf("the managed setting is not a pinned component; ledger %q rows %+v", r.ledger, r.rows)
		}
		if info, err := os.Stat(filepath.Join(h.home, ".cache", "agentfactory")); err != nil || !info.IsDir() {
			t.Errorf("the drop-in must be staged under $HOME/.cache/agentfactory: %v", err)
		}
		if entries, _ := os.ReadDir(filepath.Join(h.home, ".tmp")); len(entries) != 0 {
			t.Errorf("staging must never use TMPDIR; it holds %d entries", len(entries))
		}
	})

	t.Run("operator files are never rewritten", func(t *testing.T) {
		h, dir := sandbox(t)
		operator := map[string]string{
			filepath.Join(filepath.Dir(dir), "managed-settings.json"): `{"env":{"DISABLE_AUTOUPDATER":"0"},"permissions":{"deny":["Bash(curl:*)"]}}` + "\n",
			filepath.Join(dir, "10-operator.json"):                    `{"model":"operator-choice"}` + "\n",
		}
		old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		for p, body := range operator {
			apWrite(t, p, body)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
		r := h.run(t, pinCall, "AP_DIR="+dir)
		for p, body := range operator {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatalf("operator file %s disappeared: %v", p, err)
			}
			if got := apRead(t, p); got != body || !info.ModTime().Equal(old) {
				t.Errorf("operator file %s was rewritten (content %q, mtime %s)", p, got, info.ModTime())
			}
		}
		apExpectAutoupdaterPinned(t, apDropIn(t, dir))
		if r.rc != 0 {
			t.Errorf("rc=%d, want 0", r.rc)
		}
	})

	t.Run("a second run writes nothing", func(t *testing.T) {
		h, dir := sandbox(t)
		h.run(t, pinCall, "AP_DIR="+dir)
		f := apDropIn(t, dir)
		old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
		first, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		r := h.run(t, pinCall, "AP_DIR="+dir)
		second, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(first, second) || !second.ModTime().Equal(old) {
			t.Errorf("an unchanged drop-in was rewritten (same inode %v, mtime %s)\nlog:\n%s", os.SameFile(first, second), second.ModTime(), r.log)
		}
		apExpectAutoupdaterPinned(t, f)
	})

	t.Run("stale content is rewritten", func(t *testing.T) {
		h, dir := sandbox(t)
		h.run(t, pinCall, "AP_DIR="+dir)
		f := apDropIn(t, dir)
		apWrite(t, f, `{"env":{"DISABLE_AUTOUPDATER":"0"}}`+"\n")
		r := h.run(t, pinCall, "AP_DIR="+dir)
		apExpectAutoupdaterPinned(t, apDropIn(t, dir))
		if r.rc != 0 {
			t.Errorf("rc=%d, want 0", r.rc)
		}
	})

	t.Run("non-root with sudo writes through sudo -n", func(t *testing.T) {
		h, dir := sandbox(t)
		r := h.run(t, pinCall, "AP_DIR="+dir, "STUB_UID=1000", "STUB_SUDO_RC=0")
		apExpectAutoupdaterPinned(t, apDropIn(t, dir))
		named := false
		for _, l := range apSudoWrites(r.log) {
			named = named || strings.Contains(l, dir)
		}
		if !named {
			t.Errorf("without root the drop-in must be placed through `sudo -n` (root:root); no sudo line names %s\nlog:\n%s", dir, r.log)
		}
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc=%d ledger %q; want 0 and empty", r.rc, r.ledger)
		}
	})

	t.Run("no root and no sudo writes nothing", func(t *testing.T) {
		h, dir := sandbox(t)
		r := h.run(t, pinCall, "AP_DIR="+dir, "STUB_UID=1000", "STUB_SUDO_RC=1")
		apAbsent(t, filepath.Join(h.home, "etc"), "without root nothing is written")
		if l := apSudoWrites(r.log); len(l) != 0 {
			t.Errorf("no sudo is available; nothing may run through it: %q", l)
		}
		if r.rc != 0 || r.ledger != "" || len(r.rows) != 0 {
			t.Errorf("rc=%d ledger %q rows %+v; want 0, empty, none", r.rc, r.ledger, r.rows)
		}
	})

	t.Run("CHECK_ONLY writes nothing", func(t *testing.T) {
		h, dir := sandbox(t)
		r := h.run(t, "CHECK_ONLY=true; "+pinCall, "AP_DIR="+dir)
		apAbsent(t, filepath.Join(h.home, "etc"), "--check writes nothing (K16)")
		apAbsent(t, filepath.Join(h.home, ".cache", "agentfactory"), "--check stages nothing (K16)")
		if l := logLines(r.log, "SUDO "); len(l) != 0 {
			t.Errorf("--check must not touch sudo: %q", l)
		}
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc=%d ledger %q; want 0 and empty", r.rc, r.ledger)
		}
	})

	t.Run("not Linux writes nothing", func(t *testing.T) {
		h, dir := sandbox(t)
		r := h.run(t, pinCall, "AP_DIR="+dir, "STUB_UNAME_S=Darwin")
		apAbsent(t, filepath.Join(h.home, "etc"), "the managed drop-in is a Linux path")
		if l := logLines(r.log, "SUDO "); len(l) != 0 {
			t.Errorf("off Linux nothing may reach sudo: %q", l)
		}
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc=%d ledger %q; want 0 and empty", r.rc, r.ledger)
		}
	})

	t.Run("a failed write warns and returns 0", func(t *testing.T) {
		h, dir := sandbox(t)
		blocker := filepath.Dir(dir)
		apWrite(t, blocker, "a file where the settings directory's parent should be\n")
		r := h.run(t, pinCall, "AP_DIR="+dir)
		if r.rc != 0 || !strings.Contains(r.out, "returned") {
			t.Errorf("a failed managed-settings write must not fail the run under errexit; rc=%d\n%s", r.rc, r.out)
		}
		if !strings.Contains(r.out, "[WARN] ") {
			t.Errorf("a failed managed-settings write must log_warn its reason\n%s", r.out)
		}
		if r.ledger != "" || len(r.rows) != 0 {
			t.Errorf("a failed managed-settings write warns only; ledger %q rows %+v", r.ledger, r.rows)
		}
		if got := apRead(t, blocker); got != "a file where the settings directory's parent should be\n" {
			t.Errorf("the blocking file was modified: %q", got)
		}
	})

	t.Run("staging never names /tmp", func(t *testing.T) {
		body := topLevelShellFunction(content, "_pin_managed_autoupdater")
		if body == "" {
			t.Fatalf("quickstart.sh defines no top-level _pin_managed_autoupdater(); %s", contract)
		}
		if !regexp.MustCompile(`\$\{?HOME\}?/\.cache/agentfactory`).MatchString(body) {
			t.Error("_pin_managed_autoupdater must stage its file under $HOME/.cache/agentfactory")
		}
		if m := regexp.MustCompile(`(^|[^A-Za-z0-9_.])/tmp\b`).FindString(cvCode(body)); m != "" {
			t.Errorf("_pin_managed_autoupdater names /tmp (%q); a root-written file is never staged in a shared directory", m)
		}
	})
}
