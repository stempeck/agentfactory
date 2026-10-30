package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The K17 pass (converge_present_components), the python-lock install path (_pip_install_locked,
// _pip_would_change, the lock row), setup_litellm's lock row and the END report (_converge_report),
// exercised through PATH stubs only (ADR-018): no real pip, sudo, tmux, network or factory state.

// k17Functions is quickstart.sh from its Logging Helpers banner up to the closing `main "$@"`:
// every function the script defines and nothing it runs. Loading all of them pins what K17 and
// the report do rather than which helpers they call; the text is sourced from a file because it is
// larger than one `bash -c` argument may be.
func k17Functions(t *testing.T, content string) string {
	t.Helper()
	start := strings.Index(content, "\n# Logging Helpers\n")
	end := strings.LastIndex(content, "\nmain \"$@\"")
	if start == -1 || end < start {
		t.Fatal("quickstart.sh lost its `# Logging Helpers` banner or its closing `main \"$@\"`; the K17 harness sources the definitions between them")
	}
	return content[start+1 : end+1]
}

// The real _port_in_use dials 127.0.0.1:$LITELLM_PORT, where this host's own factory gateway may
// answer; the real _telemetry_stop SIGTERMs whatever pid the telemetry pane reports; the real
// _install_openobserve downloads; the real _os_codename reads the host's /etc/os-release.
const k17Overrides = `_port_in_use() { return 1; }
_telemetry_stop() { echo TSTOP >>"$CV_LOG"; rm -f "$K17_TELE_UP"; }
_install_openobserve() { echo INSTALL_OO >>"$CV_LOG"; return 1; }
_os_codename() { echo noble; }
`

// cvDumpTrap plus PYTHON_CHANGED: setup_litellm and the END report leave through `exit`, so the
// flag is only observable from the EXIT trap.
const k17DumpTrap = `trap 'printf "%s" "${CONVERGE_FAILURES-}" >"$CV_LEDGER"; printf "%s" "${CONVERGE_ROWS-}" >"$CV_ROWS"; echo "PYTHON_CHANGED=${PYTHON_CHANGED-}"' EXIT` + "\n"

const k17PrestepMark = "=== k17 ignore-installed requirements"

// k17PipStub emulates pip3 on a Debian image. A dry run answers K17_DRY_N (K17_LITE_DRY_N for the
// gateway lock) installs or exits K17_DRY_RC. A real install writes a --report of K17_REAL_N
// installs on the user site and K17_SYS_REAL_N on the system site (every requirement when it
// ignores what is installed), unless K17_NO_REPORT. With K17_DEBIAN=1 the system site holds
// Debian's RECORD-less pip/wheel: a system-site install that would upgrade them is refused until a
// --ignore-installed install of a pip/wheel-only file has put them in /usr/local ($K17_LOCAL_PIP).
// Every --ignore-installed requirements file is copied to $K17_PRESTEP, because the caller may
// delete it as soon as pip returns.
const k17PipStub = `echo "pip3 VIA_SUDO=${VIA_SUDO:-0} $*" >>"$CV_LOG"
req="" report="" prev="" dry=0 ii=0
for a in "$@"; do
	case "$prev" in
	-r|--requirement) req="$a" ;;
	--report) report="$a" ;;
	esac
	case "$a" in
	--dry-run) dry=1 ;;
	--ignore-installed) ii=1 ;;
	--report=*) report="${a#--report=}" ;;
	esac
	prev="$a"
done
lite=0
case "$req" in *litellm-requirements.txt) lite=1 ;; esac
items() {
	i=0 s=""
	while [ "$i" -lt "$1" ]; do
		s="$s${s:+,}{\"metadata\":{\"name\":\"p$i\",\"version\":\"1\"}}"
		i=$((i + 1))
	done
	printf '{"install":[%s]}\n' "$s"
}
if [ "$dry" = 1 ]; then
	[ "${K17_DRY_RC:-0}" = 0 ] || exit "$K17_DRY_RC"
	n="${K17_DRY_N:-0}"
	[ "$lite" = 0 ] || n="${K17_LITE_DRY_N:-0}"
	items "$n" >"$report"
	exit 0
fi
system=0
[ "${VIA_SUDO:-0}" = 1 ] && system=1
[ "${K17_UID:-1000}" = 0 ] && system=1
reqs="$(grep -E '^[A-Za-z0-9]' "$req")"
if [ "$ii" = 1 ]; then
	{ echo "` + k17PrestepMark + `"; cat "$req"; } >>"$K17_PRESTEP"
fi
[ -z "${K17_INSTALL_RC-}" ] || exit "$K17_INSTALL_RC"
[ "$system" = 0 ] || [ -z "${K17_SYS_RC-}" ] || exit "$K17_SYS_RC"
[ "$lite" = 0 ] || [ -z "${K17_LITE_INSTALL_RC-}" ] || exit "$K17_LITE_INSTALL_RC"
if [ "$system" = 1 ] && [ "${K17_DEBIAN:-0}" = 1 ]; then
	if [ "$ii" = 1 ]; then
		printf '%s\n' "$reqs" | grep -Evq '^(pip|wheel)==' || touch "$K17_LOCAL_PIP"
	elif printf '%s\n' "$reqs" | grep -Eq '^(pip|wheel)==' && [ ! -f "$K17_LOCAL_PIP" ]; then
		echo "error: uninstall-no-record-file: Cannot uninstall wheel 0.42.0, RECORD file not found. Hint: The package was installed by debian." >&2
		exit 1
	fi
fi
n="${K17_REAL_N:-0}"
[ "$system" = 0 ] || n="${K17_SYS_REAL_N:-0}"
[ "$ii" = 0 ] || n="$(printf '%s\n' "$reqs" | grep -c .)"
[ -z "$report" ] || [ -n "${K17_NO_REPORT-}" ] || items "$n" >"$report"
exit 0
`

const k17TmuxStub = `echo "tmux $*" >>"$CV_LOG"
case "$1" in
list-sessions) [ "${K17_LITELLM:-0}" = 1 ] && echo litellm ;;
list-panes) [ -f "$K17_TELE_UP" ] || exit 1; echo 4242 ;;
esac
exit 0
`

type k17Box struct {
	*cvSandbox
	harness, content, reqLock, liteLock, prestepLog string
}

func k17NewBox(t *testing.T) *k17Box {
	t.Helper()
	s := newCvSandbox(t)
	content := readQuickstart(t)
	b := &k17Box{
		cvSandbox:  s,
		content:    content,
		reqLock:    filepath.Join(s.src, "py", "requirements.txt"),
		liteLock:   filepath.Join(s.src, "py", "litellm-requirements.txt"),
		prestepLog: filepath.Join(s.base, "prestep.log"),
	}
	root := findModuleRoot(t)
	for _, lock := range []string{b.reqLock, b.liteLock} {
		data, err := os.ReadFile(filepath.Join(root, "py", filepath.Base(lock)))
		if err != nil {
			t.Fatalf("reading the real lock for the sandbox: %v", err)
		}
		s.writeFile(lock, string(data), 0o644)
	}
	fns := s.writeFile(filepath.Join(s.base, "quickstart-functions.sh"), k17Functions(t, content), 0o644)

	var h strings.Builder
	h.WriteString("set -euo pipefail\n")
	h.WriteString("RED='' GREEN='' YELLOW='' BLUE='' NC=''\n")
	h.WriteString(quickstartConfigBlock(t, content))
	h.WriteString("SCRIPT_DIR='" + s.src + "'\n")
	h.WriteString("WORKSPACE_DIR=\"$HOME/af\"\n")
	h.WriteString("export PATH=\"$HOME/.local/bin:$PATH\"\n")
	h.WriteString("CLEANUP_DIRS=()\n")
	h.WriteString(". '" + fns + "'\n")
	h.WriteString("RED='' GREEN='' YELLOW='' BLUE='' NC=''\n")
	h.WriteString(k17Overrides)
	b.harness = h.String()

	s.stub("pip3", k17PipStub)
	s.stub("tmux", k17TmuxStub)
	s.stub("id", `case "$1" in -u) echo "${K17_UID:-1000}" ;; -g) echo 1000 ;; *) echo dev ;; esac`+"\n")
	s.stub("sudo", `echo "sudo $*" >>"$CV_LOG"
[ "$1" = -n ] && shift
[ "$1" = true ] && exit "${K17_SUDO_RC:-0}"
VIA_SUDO=1
export VIA_SUDO
exec "$@"
`)
	s.stub("uname", `case "$1" in -s) echo Linux ;; *) echo aarch64 ;; esac`+"\n")
	return b
}

// call runs the snippet from the sandbox factory root, the cwd K17 and setup_litellm assume.
func (b *k17Box) call(snippet string, env ...string) cvRun {
	b.t.Helper()
	return b.run(b.harness, k17DumpTrap+"cd \"$AF_ROOT\"\n"+snippet, append([]string{
		"K17_TELE_UP=" + filepath.Join(b.base, "telemetry.up"),
		"K17_LOCAL_PIP=" + filepath.Join(b.base, "usr-local-pip"),
		"K17_PRESTEP=" + b.prestepLog,
	}, env...)...)
}

const k17Pass = "converge_present_components\n_converge_report"

func (b *k17Box) withLitellm() { b.stub("litellm", "echo 1.93.2\n") }

func (b *k17Box) relaunchScript(rel, label string) {
	b.writeFile(filepath.Join(b.afRoot, rel), "#!/bin/sh\necho \"RELAUNCH "+label+"\" >>\"$CV_LOG\"\n", 0o755)
}

type k17Pip struct {
	sudo, dry bool
	args, req string
}

func (p k17Pip) has(flag string) bool { return strings.Contains(" "+p.args+" ", " "+flag+" ") }

func k17PipCalls(log string) []k17Pip {
	var calls []k17Pip
	for _, l := range strings.Split(log, "\n") {
		rest, ok := strings.CutPrefix(l, "pip3 VIA_SUDO=")
		if !ok || len(rest) < 2 {
			continue
		}
		p := k17Pip{sudo: rest[0] == '1', args: rest[2:]}
		f := strings.Fields(p.args)
		for i, a := range f {
			if a == "-r" && i+1 < len(f) {
				p.req = f[i+1]
			}
		}
		p.dry = p.has("--dry-run")
		calls = append(calls, p)
	}
	return calls
}

func k17Installs(log, req string) []k17Pip {
	var out []k17Pip
	for _, p := range k17PipCalls(log) {
		if !p.dry && (req == "" || p.req == req) {
			out = append(out, p)
		}
	}
	return out
}

// k17Requirements reads a requirements file as pip does: continuation lines joined, comment lines
// dropped, whitespace collapsed.
func k17Requirements(text string) []string {
	var out []string
	var cur strings.Builder
	for _, l := range strings.Split(text, "\n") {
		tl := strings.TrimSpace(l)
		if tl == "" || strings.HasPrefix(tl, "#") {
			continue
		}
		cur.WriteString(strings.TrimSuffix(tl, "\\") + " ")
		if !strings.HasSuffix(tl, "\\") {
			out = append(out, strings.Join(strings.Fields(cur.String()), " "))
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, strings.Join(strings.Fields(cur.String()), " "))
	}
	sort.Strings(out)
	return out
}

func (b *k17Box) pipWheelBlocks(t *testing.T, lock string) []string {
	t.Helper()
	data, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range k17Requirements(string(data)) {
		if strings.HasPrefix(r, "pip==") || strings.HasPrefix(r, "wheel==") {
			out = append(out, r)
		}
	}
	if len(out) != 2 {
		t.Fatalf("%s pins %d of pip/wheel; the Debian case needs a lock that pins both", lock, len(out))
	}
	return out
}

var k17LockFlags = []string{"--break-system-packages", "--require-hashes", "--only-binary :all:", "--disable-pip-version-check"}

// expectPrestep: on the system site, ONLY the lock's pip/wheel hash blocks go in with
// --ignore-installed --no-deps, then the full lock goes in without --ignore-installed.
func (b *k17Box) expectPrestep(t *testing.T, r cvRun, root bool, locks ...string) {
	t.Helper()
	isLock := map[string]bool{b.reqLock: true, b.liteLock: true}
	sawPre, sawFull := 0, map[string]bool{}
	for _, p := range k17Installs(r.log, "") {
		system := p.sudo || root
		switch {
		case p.has("--ignore-installed") && !system:
			t.Errorf("a user-site install carries --ignore-installed (only the system site holds Debian's pip/wheel): pip3 %s", p.args)
		case p.has("--ignore-installed") && isLock[p.req]:
			t.Errorf("the full lock is installed with --ignore-installed (only its pip/wheel blocks may bypass what is installed): pip3 %s", p.args)
		case p.has("--ignore-installed"):
			sawPre++
			for _, f := range append([]string{"--no-deps"}, k17LockFlags...) {
				if !p.has(f) {
					t.Errorf("the pip/wheel pre-step lacks %s: pip3 %s", f, p.args)
				}
			}
		case system && isLock[p.req]:
			sawFull[p.req] = true
		}
	}
	if sawPre == 0 {
		t.Errorf("no system-site pip3 install carries --ignore-installed --no-deps for the lock's pip/wheel blocks\n%s", r.log)
	}
	raw, _ := os.ReadFile(b.prestepLog)
	var files [][]string
	for _, f := range strings.Split(string(raw), k17PrestepMark+"\n") {
		if strings.TrimSpace(f) != "" {
			files = append(files, k17Requirements(f))
		}
	}
	want := map[string][]string{}
	for _, lock := range locks {
		want[lock] = b.pipWheelBlocks(t, lock)
		if !sawFull[lock] {
			t.Errorf("no system-site install of the full lock %s follows the pip/wheel pre-step\n%s", lock, r.log)
		}
	}
	for _, got := range files {
		ok := false
		for _, w := range want {
			ok = ok || strings.Join(got, "\n") == strings.Join(w, "\n")
		}
		if !ok {
			t.Errorf("a --ignore-installed requirements file holds %q; want exactly one lock's pip/wheel blocks with their hashes", got)
		}
	}
	for lock, w := range want {
		found := false
		for _, got := range files {
			found = found || strings.Join(got, "\n") == strings.Join(w, "\n")
		}
		if !found {
			t.Errorf("no --ignore-installed requirements file holds %s's pip/wheel blocks %q", filepath.Base(lock), w)
		}
	}
}

// k17ExpectNoBlanketBypass: a dry run with --ignore-installed reports every package as a change, and a
// non-root user-site install with it reinstalls the whole lock into ~/.local on every run.
func k17ExpectNoBlanketBypass(t *testing.T, r cvRun, root bool) {
	t.Helper()
	for _, p := range k17PipCalls(r.log) {
		if !p.has("--ignore-installed") {
			continue
		}
		if p.dry {
			t.Errorf("a dry run carries --ignore-installed: pip3 %s", p.args)
		} else if !p.sudo && !root {
			t.Errorf("a non-root user-site install carries --ignore-installed: pip3 %s", p.args)
		}
	}
}

func k17Row(t *testing.T, rows []cvRow, name string) cvRow {
	t.Helper()
	got := cvRowsNamed(rows, name)
	if len(got) != 1 {
		t.Fatalf("CONVERGE_ROWS must hold exactly one %s row; got %+v", name, rows)
	}
	return got[0]
}

func k17ExpectFlag(t *testing.T, r cvRun, value string) {
	t.Helper()
	if !cvHasLine(r.out, "PYTHON_CHANGED="+value) {
		t.Errorf("PYTHON_CHANGED at exit is not %s\n%s", value, r.out)
	}
}

const k17Advice = "[WARN] python packages or system libraries changed: run 'af down && af up' so sessions pick them up"

func k17ExpectRecordFlags(t *testing.T, afRoot string, pythonChanged bool) {
	t.Helper()
	if rec, ok := cvReadRecord(t, afRoot); ok && rec.PythonChanged != pythonChanged {
		t.Errorf("convergence.json python_changed = %v, want %v", rec.PythonChanged, pythonChanged)
	}
}

func k17RecordResult(t *testing.T, afRoot, name string) string {
	t.Helper()
	rec, ok := cvReadRecord(t, afRoot)
	if !ok {
		return ""
	}
	for _, c := range rec.Components {
		if c.Name == name {
			return c.Result
		}
	}
	return ""
}

// ---------------------------------------------------------------------------------------------
// The system site holds Debian's RECORD-less pip/wheel, which pip cannot uninstall: the lock's
// pip/wheel reach /usr/local first without uninstalling them, then the full lock installs normally,
// through sudo -n for a non-root user and plain pip3 under uid 0.
// ---------------------------------------------------------------------------------------------

func TestQuickstartK17DebianPipConverges(t *testing.T) {
	t.Run("sudo_route_converges_past_debian_pip", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass, "K17_DEBIAN=1")
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "py/requirements.txt", "kept")
		if r.ledger != "" || r.rc != 0 {
			t.Errorf("ledger %q rc %d; want an empty ledger and exit 0", r.ledger, r.rc)
		}
		if strings.Contains(r.out, "Cannot uninstall") {
			t.Errorf("pip still met the Debian uninstall refusal\n%s", r.out)
		}
		if full := k17Installs(r.log, b.reqLock); len(full) == 0 || !full[len(full)-1].sudo {
			t.Errorf("the system-site lock install must go through sudo -n for a non-root user\n%s", r.log)
		}
		b.expectPrestep(t, r, false, b.reqLock)
		k17ExpectNoBlanketBypass(t, r, false)
	})

	t.Run("root_route_converges_past_debian_pip", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass, "K17_DEBIAN=1", "K17_UID=0")
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "py/requirements.txt", "kept")
		if r.ledger != "" || r.rc != 0 {
			t.Errorf("ledger %q rc %d; want an empty ledger and exit 0", r.ledger, r.rc)
		}
		if strings.Contains(r.log, "sudo ") {
			t.Errorf("uid 0 must install with plain pip3, never sudo\n%s", r.log)
		}
		b.expectPrestep(t, r, true, b.reqLock)
	})

	t.Run("root_route_converges_the_gateway_lock_past_debian_pip", func(t *testing.T) {
		b := k17NewBox(t)
		b.withLitellm()
		r := b.call(k17Pass, "K17_DEBIAN=1", "K17_UID=0")
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "py/requirements.txt", "kept")
		cvExpectRow(t, r.rows, "litellm-lock", "py/litellm-requirements.txt", "py/litellm-requirements.txt", "kept")
		if r.ledger != "" || r.rc != 0 {
			t.Errorf("ledger %q rc %d; want an empty ledger and exit 0", r.ledger, r.rc)
		}
		b.expectPrestep(t, r, true, b.reqLock, b.liteLock)
	})

	t.Run("an_always_failing_pip3_is_FAILED_ledgered_and_exits_1", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass, "K17_INSTALL_RC=1")
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "install failed", "FAILED")
		if r.ledger != "python-lock\n" {
			t.Errorf("ledger = %q, want exactly python-lock", r.ledger)
		}
		if r.rc != 1 || !cvHasLine(r.out, "[ERROR] convergence failed for: python-lock (see the table above)") {
			t.Errorf("rc %d; want the END report to exit 1 naming python-lock\n%s", r.rc, r.out)
		}
		k17ExpectFlag(t, r, "0")
		if got := k17RecordResult(t, b.afRoot, "python-lock"); got != "FAILED" {
			t.Errorf("convergence.json python-lock result %q, want FAILED", got)
		}
		k17ExpectRecordFlags(t, b.afRoot, false)
	})

	t.Run("a_failed_system_site_install_is_FAILED_and_ledgered", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass, "K17_SYS_RC=1")
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "install failed", "FAILED")
		if r.ledger != "python-lock\n" || r.rc != 1 {
			t.Errorf("ledger %q rc %d; want python-lock and exit 1 when only the system site fails", r.ledger, r.rc)
		}
	})

	t.Run("no_dry_run_or_non_root_user_site_install_ignores_installed", func(t *testing.T) {
		b := k17NewBox(t)
		b.withLitellm()
		r := b.call(k17Pass, "K17_DRY_N=1", "K17_REAL_N=1", "K17_LITE_DRY_N=1")
		if n := len(k17Installs(r.log, "")); n == 0 {
			t.Fatalf("no real pip3 install ran\n%s", r.log)
		}
		k17ExpectNoBlanketBypass(t, r, false)
		for _, p := range k17Installs(r.log, "") {
			for _, f := range k17LockFlags {
				if !p.has(f) {
					t.Errorf("a lock install lacks %s: pip3 %s", f, p.args)
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------------------------
// The END verdict: a later success clears an earlier FAILED of the same name; `kept` never does;
// ledger names without a row, and a FAILED that nothing converged, still exit 1.
// ---------------------------------------------------------------------------------------------

func TestQuickstartK17ConvergeReportVerdict(t *testing.T) {
	fixture := func(t *testing.T) (*k17Box, string) {
		b := k17NewBox(t)
		b.harness += cvClassFns
		return b, "CV_STATE=" + b.writeFile(filepath.Join(b.base, "state"), "0.9\n", 0o644)
	}
	const failed = "[ERROR] convergence failed for: "

	cleared := []struct{ name, call string }{
		{"a_later_converge_of_the_same_component_clears_its_failure", `_converge x 1.0.0 _p _i_fail required || true
_converge x 1.0.0 _p _i_ok required || true
_converge_report`},
		{"every_ledgered_copy_of_the_name_clears", `_converge x 1.0.0 _p _i_fail required || true
_converge x 1.0.0 _p _i_fail required || true
_converge x 1.0.0 _p _i_ok required || true
_converge_report`},
	}
	for _, tc := range cleared {
		t.Run(tc.name, func(t *testing.T) {
			b, state := fixture(t)
			r := b.call(tc.call, state)
			cvExpectRow(t, r.rows, "x", "1.0.0", "1.0.0", "converged")
			if r.rc != 0 || r.ledger != "" || strings.Contains(r.out, failed) {
				t.Errorf("rc %d ledger %q; a converged table must not exit 1 on the failure it replaced\n%s", r.rc, r.ledger, r.out)
			}
			if got := k17RecordResult(t, b.afRoot, "x"); got != "converged" {
				t.Errorf("convergence.json x result %q, want converged", got)
			}
		})
	}

	t.Run("litellm_lock_failed_in_K17_then_converged_by_setup_litellm", func(t *testing.T) {
		b := k17NewBox(t)
		b.withLitellm()
		r := b.call(`converge_present_components
_record_row litellm-lock py/litellm-requirements.txt py/litellm-requirements.txt converged
_converge_report`, "K17_LITE_INSTALL_RC=1")
		cvExpectRow(t, r.rows, "litellm-lock", "py/litellm-requirements.txt", "py/litellm-requirements.txt", "converged")
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc %d ledger %q; setup_litellm's converged row must clear K17's litellm-lock failure\n%s", r.rc, r.ledger, r.out)
		}
	})

	t.Run("kept_never_clears_a_failure", func(t *testing.T) {
		b, state := fixture(t)
		r := b.call(`_converge x 1.0.0 _p _i_fail required || true
echo 1.0.0 >"$CV_STATE"
_converge x 1.0.0 _p _i_fail required || true
_converge_report`, state)
		cvExpectRow(t, r.rows, "x", "1.0.0", "0.9", "FAILED")
		if r.rc != 1 || r.ledger != "x\n" || !cvHasLine(r.out, failed+"x (see the table above)") {
			t.Errorf("rc %d ledger %q; a later kept must leave the failure standing\n%s", r.rc, r.ledger, r.out)
		}
	})

	t.Run("a_converge_of_another_component_leaves_the_failure", func(t *testing.T) {
		b, state := fixture(t)
		r := b.call(`_converge x 1.0.0 _p _i_fail required || true
_converge y 1.0.0 _p _i_ok required || true
_converge_report`, state)
		if r.rc != 1 || r.ledger != "x\n" || !cvHasLine(r.out, failed+"x (see the table above)") {
			t.Errorf("rc %d ledger %q; y converging must not clear x\n%s", r.rc, r.ledger, r.out)
		}
	})

	t.Run("a_failed_required_row_exits_1", func(t *testing.T) {
		b, state := fixture(t)
		r := b.call(`_converge x 1.0.0 _p _i_fail required || true
_converge_report`, state)
		cvExpectRow(t, r.rows, "x", "1.0.0", "0.9", "FAILED")
		if r.rc != 1 || r.ledger != "x\n" || !cvHasLine(r.out, failed+"x (see the table above)") {
			t.Errorf("rc %d ledger %q; a FAILED required row must exit 1 at END\n%s", r.rc, r.ledger, r.out)
		}
	})

	rowless := []struct{ name, call, want string }{
		{"webui", `_record_row x 1.0.0 1.0.0 converged
CONVERGE_FAILURES="webui"$'\n'
_converge_report`, failed + "webui (see the table above)"},
		{"prerequisites", `_record_row x 1.0.0 1.0.0 converged
CONVERGE_FAILURES="prerequisites"$'\n'
_converge_report`, failed + "prerequisites (see the table above)"},
		{"root-layer", `_record_row x 1.0.0 1.0.0 converged
CONVERGE_FAILURES="root-layer"$'\n'
_converge_report`, failed + "root-layer (see the table above)"},
	}
	for _, tc := range rowless {
		t.Run("rowless_ledger_name_still_exits_1/"+tc.name, func(t *testing.T) {
			b := k17NewBox(t)
			r := b.call(tc.call)
			if r.rc != 1 || !cvHasLine(r.out, tc.want) {
				t.Errorf("rc %d; a ledger entry with no FAILED row of its own must still exit 1 (%q)\n%s", r.rc, tc.want, r.out)
			}
		})
	}

	// go always has a row by the time _ledger_go runs, so its ledger entry must come with a FAILED
	// row: a run that exits 1 over a kept or converged go row is the disagreement T08 reports.
	const pathGo = "PATH go is /usr/local/go/bin/go"
	for _, tc := range []struct{ name, prior, wantInstalled string }{
		{"a_kept_go_row_becomes_FAILED", "kept", pathGo},
		{"a_converged_go_row_becomes_FAILED", "converged", pathGo},
		{"a_FAILED_go_row_keeps_its_own_reason", "FAILED", "absent"},
	} {
		t.Run("_ledger_go/"+tc.name, func(t *testing.T) {
			b := k17NewBox(t)
			b.harness += extractShellFunction(b.content, "_ledger_go") + "\n"
			installed := `"$GO_TOOLCHAIN_VERSION"`
			seed := ""
			if tc.prior == "FAILED" {
				installed, seed = "absent", `CONVERGE_FAILURES="go"$'\n'`
			}
			r := b.call(`_record_row go "$GO_TOOLCHAIN_VERSION" ` + installed + ` ` + tc.prior + `
` + seed + `
_ledger_go "` + pathGo + `"
_converge_report`)
			cvExpectRow(t, r.rows, "go", quickstartPin(t, "GO_TOOLCHAIN_VERSION"), tc.wantInstalled, "FAILED")
			if r.rc != 1 || r.ledger != "go\n" || !cvHasLine(r.out, failed+"go (see the table above)") {
				t.Errorf("rc %d ledger %q; _ledger_go must exit 1 over a FAILED go row, ledgered once\n%s", r.rc, r.ledger, r.out)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// The python-lock row, the ledger and PYTHON_CHANGED follow the dry run and the install.
// ---------------------------------------------------------------------------------------------

func TestQuickstartK17PythonLockResult(t *testing.T) {
	t.Run("a_failed_user_site_install_is_FAILED_and_ledgered", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass, "K17_SUDO_RC=1", "K17_INSTALL_RC=1")
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "install failed", "FAILED")
		if r.ledger != "python-lock\n" || r.rc != 1 {
			t.Errorf("ledger %q rc %d; want python-lock and exit 1", r.ledger, r.rc)
		}
		k17ExpectFlag(t, r, "0")
		if !strings.Contains(r.out, "[ERROR] python lock "+b.reqLock+" failed") {
			t.Errorf("the failed install names no lock\n%s", r.out)
		}
	})

	t.Run("a_dry_run_that_reports_changes_converges_and_sets_PYTHON_CHANGED", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass, "K17_DRY_N=2", "K17_REAL_N=2")
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "py/requirements.txt", "converged")
		if r.ledger != "" || r.rc != 0 {
			t.Errorf("ledger %q rc %d; want none and 0", r.ledger, r.rc)
		}
		k17ExpectFlag(t, r, "1")
		k17ExpectRecordFlags(t, b.afRoot, true)
		if !cvHasLine(r.out, k17Advice) {
			t.Errorf("changed packages print no restart advice\n%s", r.out)
		}
	})

	t.Run("no_pending_change_keeps", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass)
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "py/requirements.txt", "kept")
		k17ExpectFlag(t, r, "0")
		k17ExpectRecordFlags(t, b.afRoot, false)
		if cvHasLine(r.out, k17Advice) {
			t.Errorf("an unchanged lock prints restart advice\n%s", r.out)
		}
		if strings.Contains(r.log, "kill-session") {
			t.Errorf("an unchanged lock stopped the gateway\n%s", r.log)
		}
	})

	gateway := []struct {
		name                      string
		env                       []string
		installed, result, ledger string
		changed                   string
	}{
		{"install_fails", []string{"K17_LITE_INSTALL_RC=1"}, "install failed", "FAILED", "litellm-lock\n", "0"},
		{"dry_run_reports_changes", []string{"K17_LITE_DRY_N=3", "K17_REAL_N=3"}, "py/litellm-requirements.txt", "converged", "", "1"},
		{"no_change", nil, "py/litellm-requirements.txt", "kept", "", "0"},
	}
	for _, tc := range gateway {
		t.Run("the_gateway_lock_follows_the_same_rules/"+tc.name, func(t *testing.T) {
			b := k17NewBox(t)
			b.withLitellm()
			r := b.call(k17Pass, tc.env...)
			cvExpectRow(t, r.rows, "litellm-lock", "py/litellm-requirements.txt", tc.installed, tc.result)
			if r.ledger != tc.ledger {
				t.Errorf("ledger = %q, want %q", r.ledger, tc.ledger)
			}
			k17ExpectFlag(t, r, tc.changed)
		})
	}

	drift := []struct {
		name, env, line, installed, result string
	}{
		{"no_change_is_kept", "K17_DRY_N=0", "[OK] python-lock: py/requirements.txt (kept)", "py/requirements.txt", "kept"},
		{"pending_changes_are_drift", "K17_DRY_N=3", "[WARN] python-lock: 3 would change != pinned py/requirements.txt (would converge)", "3 would change", "drift"},
		{"a_failed_dry_run_is_unknown_drift", "K17_DRY_RC=1", "[WARN] python-lock: unknown (pip dry-run failed) != pinned py/requirements.txt (would converge)", "unknown (pip dry-run failed)", "drift"},
	}
	for _, tc := range drift {
		t.Run("lock_drift_under_check/"+tc.name, func(t *testing.T) {
			b := k17NewBox(t)
			r := b.call(`CHECK_ONLY=true
k3=0
n="$(_pip_would_change "$SCRIPT_DIR/py/requirements.txt")" || k3=$?
_lock_drift python-lock py/requirements.txt "$k3" "$n"`, tc.env)
			cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", tc.installed, tc.result)
			if !cvHasLine(r.out, tc.line) {
				t.Errorf("missing %q\n%s", tc.line, r.out)
			}
			if n := len(k17Installs(r.log, "")); n != 0 {
				t.Errorf("--check ran %d real pip3 installs\n%s", n, r.log)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// A dry run that errors (rc 2) says nothing about what changed: the user-site install's own
// --report decides the row, and the system site's report never does.
// ---------------------------------------------------------------------------------------------

func TestQuickstartK17DryRunErrorRow(t *testing.T) {
	cases := []struct {
		name                string
		env                 []string
		result, changed     string
		unknown, wantAdvice bool
	}{
		{"an_error_dry_run_whose_install_changed_nothing_is_kept", []string{"K17_DRY_RC=1", "K17_REAL_N=0"}, "kept", "0", false, false},
		{"an_error_dry_run_whose_install_changed_packages_converges", []string{"K17_DRY_RC=1", "K17_REAL_N=2"}, "converged", "1", false, true},
		{"the_system_site_report_never_decides", []string{"K17_DRY_RC=1", "K17_REAL_N=0", "K17_SYS_REAL_N=10"}, "kept", "0", false, false},
		{"an_unreadable_install_report_converges_as_unknown", []string{"K17_DRY_RC=1", "K17_NO_REPORT=1"}, "converged", "1", true, true},
		{"root_route_reports_from_the_full_lock_pass", []string{"K17_DRY_RC=1", "K17_UID=0", "K17_SYS_REAL_N=0"}, "kept", "0", false, false},
		{"root_route_full_lock_changes_converge", []string{"K17_DRY_RC=1", "K17_UID=0", "K17_SYS_REAL_N=3"}, "converged", "1", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := k17NewBox(t)
			r := b.call(k17Pass, tc.env...)
			row := k17Row(t, r.rows, "python-lock")
			if row.pin != "py/requirements.txt" || row.result != tc.result {
				t.Errorf("python-lock row = pin %q installed %q result %q; want result %q", row.pin, row.installed, row.result, tc.result)
			}
			if tc.unknown != strings.Contains(row.installed, "unknown (no install report)") {
				t.Errorf("python-lock installed = %q; an unreadable report must (and only it may) read \"unknown (no install report)\"", row.installed)
			}
			if !tc.unknown && row.installed != "py/requirements.txt" {
				t.Errorf("python-lock installed = %q, want py/requirements.txt", row.installed)
			}
			k17ExpectFlag(t, r, tc.changed)
			k17ExpectRecordFlags(t, b.afRoot, tc.changed == "1")
			if cvHasLine(r.out, k17Advice) != tc.wantAdvice {
				t.Errorf("restart advice printed = %v, want %v\n%s", !tc.wantAdvice, tc.wantAdvice, r.out)
			}
			if r.ledger != "" || r.rc != 0 {
				t.Errorf("ledger %q rc %d; a successful install is never a failure", r.ledger, r.rc)
			}
		})
	}

	t.Run("an_error_dry_run_still_stops_the_gateway", func(t *testing.T) {
		b := k17NewBox(t)
		b.relaunchScript(".agentfactory/gateway-relaunch.sh", "gateway")
		r := b.call(k17Pass, "K17_DRY_RC=1", "K17_LITELLM=1")
		k17ExpectGatewayStop(t, b, r, true)
	})
}

func k17ExpectGatewayStop(t *testing.T, b *k17Box, r cvRun, want bool) {
	t.Helper()
	lines := strings.Split(r.log, "\n")
	kill, install := -1, -1
	for i, l := range lines {
		if l == "tmux kill-session -t =litellm" && kill == -1 {
			kill = i
		}
		if strings.HasPrefix(l, "pip3 ") && !strings.Contains(l, "--dry-run") && install == -1 {
			install = i
		}
	}
	if (kill >= 0) != want {
		t.Errorf("gateway stopped = %v, want %v\n%s", kill >= 0, want, r.log)
	}
	if want && install >= 0 && kill > install {
		t.Errorf("the gateway is stopped after the lock install began (line %d > %d)\n%s", kill, install, r.log)
	}
	_, err := os.Stat(filepath.Join(b.afRoot, ".runtime/gateway/reconciling"))
	if (err == nil) != want {
		t.Errorf("reconcile marker written = %v, want %v", err == nil, want)
	}
}

// setup_litellm's own lock row is recorded before its `import litellm.proxy` check; a failing
// python3 stub ends the function right there, so nothing after the row touches the sandbox.
func TestQuickstartK17SetupLitellmLockRow(t *testing.T) {
	box := func(t *testing.T) *k17Box {
		b := k17NewBox(t)
		b.withLitellm()
		b.stub("python3", `echo "python3 $*" >>"$CV_LOG"`+"\nexit 1\n")
		b.writeFile(filepath.Join(b.afRoot, ".agentfactory/models.json"), "{}\n", 0o644)
		return b
	}
	const reachedImport = "litellm.proxy failed to import after install"

	cases := []struct {
		name         string
		env          []string
		result, flag string
		root         bool
	}{
		{"no_change_keeps", []string{"K17_LITE_DRY_N=0"}, "kept", "0", false},
		{"changes_converge", []string{"K17_LITE_DRY_N=2", "K17_REAL_N=2"}, "converged", "1", false},
		{"an_error_dry_run_whose_install_changed_nothing_is_kept", []string{"K17_DRY_RC=1", "K17_REAL_N=0"}, "kept", "0", false},
		{"an_error_dry_run_whose_install_changed_packages_converges", []string{"K17_DRY_RC=1", "K17_REAL_N=2"}, "converged", "1", false},
		{"root_route_survives_debian_pip", []string{"K17_UID=0", "K17_DEBIAN=1"}, "kept", "0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := box(t)
			r := b.call("setup_litellm", tc.env...)
			if !strings.Contains(r.out, reachedImport) {
				t.Fatalf("setup_litellm did not get past its lock install (rc %d)\n%s", r.rc, r.out)
			}
			cvExpectRow(t, r.rows, "litellm-lock", "py/litellm-requirements.txt", "py/litellm-requirements.txt", tc.result)
			k17ExpectFlag(t, r, tc.flag)
			if tc.root {
				b.expectPrestep(t, r, true, b.liteLock)
			}
		})
	}

	t.Run("an_unreadable_install_report_converges_as_unknown", func(t *testing.T) {
		b := box(t)
		r := b.call("setup_litellm", "K17_DRY_RC=1", "K17_NO_REPORT=1")
		if !strings.Contains(r.out, reachedImport) {
			t.Fatalf("setup_litellm did not get past its lock install (rc %d)\n%s", r.rc, r.out)
		}
		row := k17Row(t, r.rows, "litellm-lock")
		if row.result != "converged" || !strings.Contains(row.installed, "unknown (no install report)") {
			t.Errorf("litellm-lock row = installed %q result %q; want converged with \"unknown (no install report)\"", row.installed, row.result)
		}
		k17ExpectFlag(t, r, "1")
	})

	t.Run("an_error_dry_run_still_stops_the_gateway_before_the_install", func(t *testing.T) {
		b := box(t)
		r := b.call("setup_litellm", "K17_DRY_RC=1", "K17_LITELLM=1")
		k17ExpectGatewayStop(t, b, r, true)
	})
}

// ---------------------------------------------------------------------------------------------
// The K17 conditions — gateway stop and relaunch, the system-site guard, the telemetry block — and
// the END report's conditions.
// ---------------------------------------------------------------------------------------------

func TestQuickstartK17GatewayStop(t *testing.T) {
	for _, session := range []bool{false, true} {
		for _, changed := range []bool{false, true} {
			for _, apt := range []bool{false, true} {
				name := "session_" + map[bool]string{false: "down", true: "up"}[session] +
					"_lock_" + map[bool]string{false: "unchanged", true: "changed"}[changed] +
					"_apt_" + map[bool]string{false: "0", true: "1"}[apt]
				t.Run(name, func(t *testing.T) {
					b := k17NewBox(t)
					b.relaunchScript(".agentfactory/gateway-relaunch.sh", "gateway")
					env := []string{"K17_LITELLM=" + map[bool]string{false: "0", true: "1"}[session]}
					if changed {
						env = append(env, "K17_DRY_N=1", "K17_REAL_N=1")
					}
					call := "converge_present_components"
					if apt {
						call = "APT_CHANGED=1\n" + call
					}
					r := b.call(call, env...)
					stop := session && (changed || apt)
					k17ExpectGatewayStop(t, b, r, stop)
					if got := strings.Contains(r.log, "RELAUNCH gateway"); got != stop {
						t.Errorf("gateway relaunched = %v, want %v\n%s", got, stop, r.log)
					}
				})
			}
		}
	}

	t.Run("a_gateway_lock_change_alone_stops_the_gateway", func(t *testing.T) {
		b := k17NewBox(t)
		b.withLitellm()
		b.relaunchScript(".agentfactory/gateway-relaunch.sh", "gateway")
		r := b.call("converge_present_components", "K17_LITELLM=1", "K17_LITE_DRY_N=1", "K17_REAL_N=1")
		k17ExpectGatewayStop(t, b, r, true)
		if !strings.Contains(r.log, "RELAUNCH gateway") {
			t.Errorf("the stopped gateway was not relaunched\n%s", r.log)
		}
	})

	t.Run("with_litellm_leaves_the_relaunch_to_setup_litellm", func(t *testing.T) {
		b := k17NewBox(t)
		b.relaunchScript(".agentfactory/gateway-relaunch.sh", "gateway")
		r := b.call("WITH_LITELLM=true\nconverge_present_components", "K17_LITELLM=1", "K17_DRY_N=1", "K17_REAL_N=1")
		k17ExpectGatewayStop(t, b, r, true)
		if strings.Contains(r.log, "RELAUNCH gateway") {
			t.Errorf("K17 relaunched the gateway although setup_litellm reconciles it later in a --litellm run\n%s", r.log)
		}
	})

	t.Run("no_relaunch_script_no_relaunch", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call("converge_present_components", "K17_LITELLM=1", "K17_DRY_N=1", "K17_REAL_N=1")
		k17ExpectGatewayStop(t, b, r, true)
		if r.rc != 0 || strings.Contains(r.out, "gateway-relaunch.sh did not restart") {
			t.Errorf("rc %d; a missing gateway-relaunch.sh must be skipped quietly\n%s", r.rc, r.out)
		}
	})

	t.Run("a_failed_relaunch_warns_and_continues", func(t *testing.T) {
		b := k17NewBox(t)
		b.writeFile(filepath.Join(b.afRoot, ".agentfactory/gateway-relaunch.sh"), "#!/bin/sh\nexit 1\n", 0o755)
		r := b.call("converge_present_components", "K17_LITELLM=1", "K17_DRY_N=1", "K17_REAL_N=1")
		if r.rc != 0 || !cvHasLine(r.out, "[WARN] gateway-relaunch.sh did not restart the gateway; see USING_LITELLM.md") {
			t.Errorf("rc %d; a failed relaunch must warn and let K17 finish\n%s", r.rc, r.out)
		}
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "py/requirements.txt", "converged")
	})
}

func TestQuickstartK17SystemSiteGuard(t *testing.T) {
	const noRoot = "[WARN] python-lock: no root, so the system-site copy of py/requirements.txt cannot converge (the root layer reports why)"
	t.Run("sudo_refused", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass, "K17_SUDO_RC=1")
		if strings.Contains(r.log, "sudo -n pip3") || !cvHasLine(r.out, noRoot) {
			t.Errorf("without passwordless sudo K17 must skip the system site and say why\nlog:\n%s\nout:\n%s", r.log, r.out)
		}
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "py/requirements.txt", "kept")
		if r.ledger != "" || r.rc != 0 {
			t.Errorf("ledger %q rc %d; the root layer, not K17, ledgers a missing root", r.ledger, r.rc)
		}
	})

	t.Run("no_sudo", func(t *testing.T) {
		b := k17NewBox(t)
		if err := os.Remove(filepath.Join(b.bin, "sudo")); err != nil {
			t.Fatal(err)
		}
		r := b.call(k17Pass)
		if !cvHasLine(r.out, noRoot) || len(k17Installs(r.log, b.reqLock)) != 1 {
			t.Errorf("without sudo K17 must install the user site only and say why\nlog:\n%s\nout:\n%s", r.log, r.out)
		}
		cvExpectRow(t, r.rows, "python-lock", "py/requirements.txt", "py/requirements.txt", "kept")
	})

	t.Run("root_installs_both_sites_with_plain_pip3", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass, "K17_UID=0")
		full := k17Installs(r.log, b.reqLock)
		if len(full) != 2 || full[0].sudo || full[1].sudo || strings.Contains(r.log, "sudo ") || cvHasLine(r.out, noRoot) {
			t.Errorf("uid 0 must install the lock twice with plain pip3; got %+v\n%s", full, r.log)
		}
	})

	t.Run("passwordless_sudo_installs_the_system_site_via_sudo", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call(k17Pass)
		full := k17Installs(r.log, b.reqLock)
		if len(full) != 2 || full[0].sudo || !full[1].sudo || cvHasLine(r.out, noRoot) {
			t.Errorf("a non-root user with passwordless sudo installs the user site, then the system site via sudo -n; got %+v\n%s", full, r.log)
		}
	})
}

func TestQuickstartK17Telemetry(t *testing.T) {
	box := func(t *testing.T, version string, up bool) *k17Box {
		b := k17NewBox(t)
		if version != "" {
			b.writeFile(".local/bin/openobserve", "#!/bin/sh\necho 'openobserve "+version+"'\n", 0o755)
		}
		if up {
			b.writeFile(filepath.Join(b.base, "telemetry.up"), "", 0o644)
		}
		b.relaunchScript(".agentfactory/telemetry/relaunch.sh", "telemetry")
		return b
	}
	pin := quickstartPin(t, "OPENOBSERVE_VERSION")

	for _, tele := range []string{"false", "true"} {
		t.Run("a_fresh_box_installs_nothing/WITH_TELEMETRY="+tele, func(t *testing.T) {
			b := box(t, "", false)
			r := b.call("WITH_TELEMETRY=" + tele + "\nconverge_present_components")
			if strings.Contains(r.log, "INSTALL_OO") || len(cvRowsNamed(r.rows, "openobserve")) != 0 || r.ledger != "" {
				t.Errorf("K17 installed an absent backend (it only converges an installed one)\nlog:\n%s\nrows %+v ledger %q", r.log, r.rows, r.ledger)
			}
		})
	}

	t.Run("an_installed_stale_backend_converges_under_no_telemetry", func(t *testing.T) {
		b := box(t, "v0.0.1", false)
		r := b.call("WITH_TELEMETRY=false\nconverge_present_components")
		if !strings.Contains(r.log, "INSTALL_OO") {
			t.Errorf("--no-telemetry skipped converging an installed backend (D-47)\n%s", r.log)
		}
		cvExpectRow(t, r.rows, "openobserve", pin, "v0.0.1", "FAILED")
	})

	stops := []struct {
		name     string
		up, apt  bool
		tele     string
		stop     bool
		relaunch bool
	}{
		{"apt_change_stops_a_running_backend", true, true, "true", true, false},
		{"no_apt_change_no_stop", true, false, "true", false, false},
		{"a_backend_that_is_not_running_is_neither_stopped_nor_relaunched", false, true, "false", false, false},
		{"no_telemetry_relaunches_a_backend_it_stopped", true, true, "false", true, true},
		{"no_telemetry_leaves_a_running_backend_alone", true, false, "false", false, false},
	}
	for _, tc := range stops {
		t.Run(tc.name, func(t *testing.T) {
			b := box(t, pin, tc.up)
			call := "WITH_TELEMETRY=" + tc.tele + "\nconverge_present_components"
			if tc.apt {
				call = "APT_CHANGED=1\n" + call
			}
			r := b.call(call)
			cvExpectRow(t, r.rows, "openobserve", pin, pin, "kept")
			if n := cvCountLines(r.log, "TSTOP"); n != map[bool]int{false: 0, true: 1}[tc.stop] {
				t.Errorf("telemetry stopped %d times, want stop=%v\n%s", n, tc.stop, r.log)
			}
			if got := strings.Contains(r.log, "RELAUNCH telemetry"); got != tc.relaunch {
				t.Errorf("telemetry relaunched = %v, want %v\n%s", got, tc.relaunch, r.log)
			}
			if strings.Contains(r.log, "INSTALL_OO") {
				t.Errorf("a backend at its pin was reinstalled\n%s", r.log)
			}
		})
	}
}

func TestQuickstartK17ConvergeReport(t *testing.T) {
	const driftFound = "[WARN] drift found: the next ./quickstart.sh or af install --agents converges the rows above"
	checks := []struct {
		name, call string
		rc         int
	}{
		{"check_mode_exits_1_on_a_FAILED_row", "_record_row x 1.0.0 0.9 FAILED", 1},
		{"check_mode_exits_1_on_a_drift_row", "_record_row x 1.0.0 0.9 drift", 1},
		{"check_mode_exits_1_on_a_ledger_only_failure", "_record_row x 1.0.0 1.0.0 kept\nCONVERGE_FAILURES=\"prerequisites\"$'\\n'", 1},
		{"check_mode_all_kept_exits_0", "_record_row x 1.0.0 1.0.0 kept", 0},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			b := k17NewBox(t)
			r := b.call("CHECK_ONLY=true\n" + tc.call + "\n_converge_report")
			if r.rc != tc.rc {
				t.Errorf("rc = %d, want %d\n%s", r.rc, tc.rc, r.out)
			}
			if cvHasLine(r.out, driftFound) != (tc.rc == 1) || cvHasLine(r.out, "[OK] every component matches its pin") != (tc.rc == 0) {
				t.Errorf("the --check verdict line does not match exit %d\n%s", tc.rc, r.out)
			}
			if rec, ok := cvReadRecord(t, b.afRoot); ok && !rec.Mode.CheckOnly {
				t.Error("convergence.json .mode.check_only must be true under --check")
			}
		})
	}

	advice := []struct {
		name, set string
		want      bool
	}{
		{"apt_changed", "APT_CHANGED=1", true},
		{"python_changed", "PYTHON_CHANGED=1", true},
		{"neither", ":", false},
	}
	for _, tc := range advice {
		t.Run("restart_advice/"+tc.name, func(t *testing.T) {
			b := k17NewBox(t)
			r := b.call(tc.set + "\n_record_row x 1.0.0 1.0.0 kept\n_converge_report")
			if r.rc != 0 || cvHasLine(r.out, k17Advice) != tc.want {
				t.Errorf("rc %d; restart advice printed = %v, want %v\n%s", r.rc, !tc.want, tc.want, r.out)
			}
		})
	}

	const aptSaid = "E: Version 'x' for 'nodejs' was not found"
	nodePin := quickstartPin(t, "NODE_APT_VERSION")
	t.Run("nodejs_pin_hint", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call("CONVERGE_FAILURES=\"nodejs\"$'\\n'\nNODE_APT_ERROR=\"" + aptSaid + "\"\n_converge_report")
		if r.rc != 1 || !strings.Contains(r.out, "[ERROR] cannot pin nodejs="+nodePin+": apt said \""+aptSaid+"\".") {
			t.Errorf("rc %d; the unresolved nodejs pin must be named with apt's words\n%s", r.rc, r.out)
		}
	})

	t.Run("root_layer_message_replaces_the_nodejs_hint", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call("CONVERGE_FAILURES=\"root-layer\"$'\\n'\nNODE_APT_ERROR=\"" + aptSaid + "\"\n_converge_report")
		if r.rc != 1 || !strings.Contains(r.out, "[ERROR] security updates cannot be applied: no root in this container (id -u = 1000; sudo -n true failed). Ubuntu noble-security updates") {
			t.Errorf("rc %d; a root-layer failure must print the C-6 message\n%s", r.rc, r.out)
		}
		if strings.Contains(r.out, "cannot pin nodejs=") {
			t.Errorf("the root-layer message must replace the nodejs hint\n%s", r.out)
		}
	})

	t.Run("no_AF_ROOT_falls_back_to_the_workspace_repo", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call("unset AF_ROOT\nmkdir -p \"$HOME/af/repo/.git\"\n_record_row x 1.0.0 1.0.0 kept\n_converge_report")
		record := filepath.Join(b.home, "af/repo/.runtime/bootstrap/convergence.json")
		if _, err := os.Stat(record); err != nil || !cvHasLine(r.out, "[INFO] Convergence record: "+record) {
			t.Errorf("without AF_ROOT the record must land in the workspace's git repo (%v)\n%s", err, r.out)
		}
	})

	t.Run("no_AF_ROOT_and_no_repo_writes_nothing", func(t *testing.T) {
		b := k17NewBox(t)
		r := b.call("unset AF_ROOT\n_record_row x 1.0.0 1.0.0 kept\n_converge_report")
		want := "[WARN] no factory root (AF_ROOT is unset and no git repository is under " + filepath.Join(b.home, "af") + "); convergence.json not written"
		if r.rc != 0 || !cvHasLine(r.out, want) {
			t.Errorf("rc %d; missing %q\n%s", r.rc, want, r.out)
		}
		_ = filepath.WalkDir(b.base, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.Name() == "convergence.json" {
				t.Errorf("a convergence record was written with no factory root: %s", p)
			}
			return nil
		})
	})
}
