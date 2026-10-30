package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// PR #747 review gates: the Go env write (T02), the Playwright Linux gate (T16), retiring the apt
// gh (BODY-2), the banner after the report (BODY-4) and the setup_telemetry header (BODY-9).
// Every dynamic case runs in a cvSandbox: a from-scratch env whose PATH holds stubs only.

func qgLines(log, prefix, substr string) []string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, prefix) && strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return out
}

func qgLineIndex(log, prefix, substr string, last bool) int {
	at := -1
	for i, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, prefix) && strings.Contains(l, substr) {
			at = i
			if !last {
				break
			}
		}
	}
	return at
}

func qgReadFile(t *testing.T, p string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b), true
}

func qgRealTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("resolving real %s: %v", name, err)
	}
	return p
}

// ---------------------------------------------------------------------------------------------
// T02: `go env -w GOTOOLCHAIN=<pin>+auto` is written only once the go on PATH is the
// converged prefix, and the `*)` arm removes only a value af itself persisted earlier.

// qgGoStub stands in for every go binary of the T02 cases. It keeps a real env file at
// $HOME/.config/go/env, so what `go env -w|-u` did is read back from the file rather than inferred
// from the call log.
const qgGoStub = `echo "go GOTOOLCHAIN=${GOTOOLCHAIN-} $*" >>"$CV_LOG"
envf="$HOME/.config/go/env"
case "$1" in
version) echo "go version $QG_GO_VERSION linux/arm64" ;;
env)
	case "${2-}" in
	GOENV) echo "$envf" ;;
	-w)
		mkdir -p "${envf%/*}"
		{ [ -f "$envf" ] && grep -v "^${3%%=*}=" "$envf"; echo "$3"; } >"$envf.new"
		mv "$envf.new" "$envf" ;;
	-u)
		[ -f "$envf" ] || exit 0
		grep -v "^$3=" "$envf" >"$envf.new"
		mv "$envf.new" "$envf" ;;
	*) echo "" ;;
	esac ;;
esac
`

// install_af runs bare, as main runs it, so a new unguarded failure inside it aborts under errexit
// instead of being absorbed by the harness's `|| rc=$?`.
const qgInstallAfBare = `install_af; echo QG_INSTALL_AF_RETURNED`

func qgGoSandbox(t *testing.T) (*cvSandbox, string) {
	t.Helper()
	s := newCvSandbox(t)
	harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error", "log_step",
		"command_exists", "_arch", "_record_row", "_quarantine_stale", "_converge", "install_af")
	s.writeFile(filepath.Join(s.src, "go.mod"), "module github.com/stempeck/agentfactory\n", 0o644)
	s.stub("uname", `case "$1" in -m) echo aarch64 ;; *) echo Linux ;; esac`+"\n")
	s.stub("make", `echo "make $* GOTOOLCHAIN=${GOTOOLCHAIN-}" >>"$CV_LOG"`+"\n")
	s.stub("cp", `echo "cp $*" >>"$CV_LOG"`+"\n")
	s.stub("af", "echo af test\n")
	s.stub("curl", `echo "curl $*" >>"$CV_LOG"; exit 22`+"\n")
	return s, harness
}

func qgExpectInstallAfReturned(t *testing.T, r cvRun) {
	t.Helper()
	if r.rc != 0 || !strings.Contains(r.out, "QG_INSTALL_AF_RETURNED") {
		t.Errorf("install_af must return 0 under errexit (D-66: the ledger carries the outcome); rc = %d\n%s", r.rc, r.out)
	}
}

// qgExpectEveryGoCallLocal: on the `*)` arm the go on PATH is the image's go1.24.2, whose cmd/go
// must never fetch a toolchain itself (H-3).
func qgExpectEveryGoCallLocal(t *testing.T, log string) {
	t.Helper()
	for _, l := range qgLines(log, "go ", "") {
		if !strings.HasPrefix(l, "go GOTOOLCHAIN=local ") {
			t.Errorf("the image go ran without GOTOOLCHAIN=local (H-3): %s", l)
		}
	}
	builds := 0
	for _, l := range qgLines(log, "make ", "") {
		builds++
		if !strings.HasSuffix(l, " GOTOOLCHAIN=local") {
			t.Errorf("af must build with GOTOOLCHAIN=local when the prefix toolchain is not on PATH: %s", l)
		}
	}
	if builds == 0 {
		t.Errorf("install_af ran no make\n%s", log)
	}
}

func TestQuickstartGoEnvWriteFollowsTheConvergedPrefix(t *testing.T) {
	pin := quickstartPin(t, "GO_TOOLCHAIN_VERSION")

	t.Run("a failed Go converge never persists the +auto switch (H-3)", func(t *testing.T) {
		s, harness := qgGoSandbox(t)
		s.stub("go", qgGoStub)
		r := s.run(harness, qgInstallAfBare, "QG_GO_VERSION=go1.24.2")
		qgExpectInstallAfReturned(t, r)
		if r.ledger != "go\n" {
			t.Errorf("CONVERGE_FAILURES = %q, want %q", r.ledger, "go\n")
		}
		cvExpectRow(t, r.rows, "go", pin, "absent", "FAILED")
		for _, l := range qgLines(r.log, "go ", " env -w ") {
			t.Errorf("the Go converge failed and the image go is still on PATH, yet install_af ran `%s`; the +auto write belongs to the converged prefix arm only (AC-3.3 (i))", l)
		}
		if got, ok := qgReadFile(t, filepath.Join(s.home, ".config/go/env")); ok {
			t.Errorf("a failed Go converge wrote the go env file:\n%s", got)
		}
		qgExpectEveryGoCallLocal(t, r.log)
		if t.Failed() {
			t.Logf("stdout:\n%s\ncalls:\n%s", r.out, r.log)
		}
	})

	t.Run("a converged prefix that is not the go on PATH gets no +auto", func(t *testing.T) {
		s, harness := qgGoSandbox(t)
		s.stub("go", qgGoStub)
		s.writeFile(".local/share/agentfactory/go/bin/go", "#!/bin/sh\nQG_GO_VERSION="+pin+"\n"+qgGoStub, 0o755)
		r := s.run(harness, qgInstallAfBare, "QG_GO_VERSION=go1.24.2")
		qgExpectInstallAfReturned(t, r)
		if rows := cvRowsNamed(r.rows, "go"); len(rows) != 1 || rows[0].result != "FAILED" ||
			!strings.HasPrefix(rows[0].installed, "PATH go is ") || strings.Contains(rows[0].installed, "/.local/share/agentfactory/go/") {
			t.Errorf("want one FAILED go row whose installed names the go on PATH, not the prefix: the run exits 1 over go, so its row cannot read kept (T08); got %+v", rows)
		}
		if r.ledger != "go\n" {
			t.Errorf("CONVERGE_FAILURES = %q, want %q (the go on PATH is not the prefix)", r.ledger, "go\n")
		}
		for _, l := range qgLines(r.log, "go ", " env -w ") {
			t.Errorf("the prefix probes at the pin but the image go answers on PATH, yet install_af ran `%s`; a probe check is not the arm", l)
		}
		if got, ok := qgReadFile(t, filepath.Join(s.home, ".config/go/env")); ok {
			t.Errorf("the go env file was written:\n%s", got)
		}
		qgExpectEveryGoCallLocal(t, r.log)
		if t.Failed() {
			t.Logf("stdout:\n%s\ncalls:\n%s", r.out, r.log)
		}
	})

	for _, tc := range []struct {
		name, value string
		afWritten   bool
	}{
		{"the *) arm unsets only an af-written value/" + pin + "+auto", pin + "+auto", true},
		{"the *) arm unsets only an af-written value/go1.25.0+auto", "go1.25.0+auto", true},
		{"the *) arm leaves an operator value/local", "local", false},
		{"the *) arm leaves an operator value/auto", "auto", false},
		{"the *) arm leaves an operator value/go1.25.0+path", "go1.25.0+path", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, harness := qgGoSandbox(t)
			s.stub("go", qgGoStub)
			const other = "GOPROXY=direct\n"
			seed := other + "GOTOOLCHAIN=" + tc.value + "\n"
			envFile := s.writeFile(".config/go/env", seed, 0o644)
			r := s.run(harness, qgInstallAfBare, "QG_GO_VERSION=go1.24.2")
			qgExpectInstallAfReturned(t, r)
			if r.ledger != "go\n" {
				t.Errorf("CONVERGE_FAILURES = %q, want %q", r.ledger, "go\n")
			}
			for _, l := range qgLines(r.log, "go ", " env -w ") {
				t.Errorf("the go on PATH is not the converged prefix, yet install_af ran `%s`", l)
			}
			got, _ := qgReadFile(t, envFile)
			unsets := qgLines(r.log, "go ", " env -u GOTOOLCHAIN")
			if tc.afWritten {
				if got != other {
					t.Errorf("go env file = %q, want %q: an af-written GOTOOLCHAIN=%s left behind makes the image go fetch a toolchain itself", got, other, tc.value)
				}
				if len(unsets) != 1 {
					t.Errorf("want exactly one `GOTOOLCHAIN=local go env -u GOTOOLCHAIN`, got %q", unsets)
				}
				if !regexp.MustCompile(`(?m)^\[ERROR\] .*` + regexp.QuoteMeta(tc.value)).MatchString(r.out) {
					t.Errorf("the `*)` arm's [ERROR] line must name the removed GOTOOLCHAIN=%s\n%s", tc.value, r.out)
				}
			} else {
				if got != seed {
					t.Errorf("go env file = %q, want the operator's own %q untouched", got, seed)
				}
				if len(unsets) != 0 {
					t.Errorf("an operator GOTOOLCHAIN=%s must stay; got %q", tc.value, unsets)
				}
			}
			qgExpectEveryGoCallLocal(t, r.log)
			if t.Failed() {
				t.Logf("stdout:\n%s\ncalls:\n%s", r.out, r.log)
			}
		})
	}

	t.Run("the converged prefix keeps the +auto belt before its version check", func(t *testing.T) {
		s, harness := qgGoSandbox(t)
		prefixGo := s.writeFile(".local/share/agentfactory/go/bin/go", "#!/bin/sh\n"+qgGoStub, 0o755)
		if err := os.MkdirAll(filepath.Join(s.home, ".local/bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(prefixGo, filepath.Join(s.home, ".local/bin/go")); err != nil {
			t.Fatal(err)
		}
		r := s.run(harness, qgInstallAfBare, "QG_GO_VERSION="+pin)
		qgExpectInstallAfReturned(t, r)
		if r.ledger != "" {
			t.Errorf("CONVERGE_FAILURES = %q, want empty", r.ledger)
		}
		cvExpectRow(t, r.rows, "go", pin, pin, "kept")
		want := "go GOTOOLCHAIN=local env -w GOTOOLCHAIN=" + pin + "+auto"
		if writes := qgLines(r.log, "go ", " env -w "); len(writes) != 1 || writes[0] != want {
			t.Errorf("the converged prefix arm must run exactly `%s` (AC-3.3 (i)); got %q", want, writes)
		}
		if got, _ := qgReadFile(t, filepath.Join(s.home, ".config/go/env")); got != "GOTOOLCHAIN="+pin+"+auto\n" {
			t.Errorf("go env file = %q, want the +auto belt", got)
		}
		if unsets := qgLines(r.log, "go ", " env -u "); len(unsets) != 0 {
			t.Errorf("the converged arm unset the go env: %q", unsets)
		}
		w, v := qgLineIndex(r.log, "go ", " env -w ", false), qgLineIndex(r.log, "go ", " version", true)
		if w == -1 || v == -1 || w > v {
			t.Errorf("the +auto write (call %d) must precede the arm's `go version` check (call %d), which is what detects an exported GOTOOLCHAIN overriding it", w, v)
		}
		if t.Failed() {
			t.Logf("stdout:\n%s\ncalls:\n%s", r.out, r.log)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// T16: the Playwright converge runs on Linux only, gated at its call inside install_playwright
// (so `_converge_check` is gated too), with no row off Linux — Go's shape.

func qgSeedPlaywrightCache(t *testing.T, home string) string {
	t.Helper()
	cache := filepath.Join(home, ".cache/ms-playwright")
	for _, d := range []string{"chromium-1228", "chromium_headless_shell-1228", "chromium-1243"} {
		if err := os.MkdirAll(filepath.Join(cache, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cache, d, "marker"), []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cache
}

func TestQuickstartPlaywrightConvergesOnlyOnLinux(t *testing.T) {
	pin := quickstartPin(t, "PLAYWRIGHT_VERSION")
	sandbox := func(t *testing.T, kernel, machine string) (*cvSandbox, string, string) {
		s := newCvSandbox(t)
		harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error", "log_step",
			"command_exists", "_arch", "_record_row", "_quarantine_stale", "_converge", "install_playwright")
		s.stub("uname", `case "$1" in -m) echo `+machine+` ;; *) echo `+kernel+` ;; esac`+"\n")
		return s, harness, qgSeedPlaywrightCache(t, s.home)
	}

	for _, tc := range []struct {
		name, call string
	}{
		{"off Linux nothing converges, installs or quarantines", "install_playwright"},
		{"--check off Linux reports no playwright row", "CHECK_ONLY=true; install_playwright"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, harness, cache := sandbox(t, "Darwin", "arm64")
			before := cvSnapshot(t, cache)
			r := s.run(harness, tc.call)
			if r.rc != 0 {
				t.Errorf("install_playwright rc = %d, want 0\n%s", r.rc, r.out)
			}
			if r.ledger != "" {
				t.Errorf("CONVERGE_FAILURES = %q, want empty", r.ledger)
			}
			if rows := cvRowsNamed(r.rows, "playwright"); len(rows) != 0 {
				t.Errorf("off Linux there is no playwright row, matching go's; got %+v", rows)
			}
			if d := cvSnapshotDiff(before, cvSnapshot(t, cache)); len(d) > 0 {
				t.Errorf("off Linux the browser cache must stay as it is:\n%s", strings.Join(d, "\n"))
			}
			for _, banned := range []string{"npm not found", "(would converge)", "visual checks will escalate"} {
				if strings.Contains(r.out, banned) {
					t.Errorf("off Linux the Linux-only converge ran: stdout says %q", banned)
				}
			}
			if !regexp.MustCompile(`(?m)^\[INFO\] playwright: not Linux`).MatchString(r.out) {
				t.Errorf("stdout lacks the `[INFO] playwright: not Linux…` line that says why nothing converged (like `go: not Linux`)\n%s", r.out)
			}
		})
	}

	t.Run("on Linux the optional converge still runs and quarantines", func(t *testing.T) {
		s, harness, cache := sandbox(t, "Linux", "aarch64")
		r := s.run(harness, "install_playwright")
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc = %d, ledger %q; an optional row never fails the run\n%s", r.rc, r.ledger, r.out)
		}
		cvExpectRow(t, r.rows, "playwright", pin, "absent", "skipped-optional")
		if !cvHasLine(r.out, "[WARN] npm not found; cannot install Playwright") {
			t.Errorf("on Linux the installer must run\n%s", r.out)
		}
		after := cvSnapshot(t, cache)
		if _, ok := after["chromium-1228"]; ok {
			t.Error("on Linux a failed optional install still quarantines the stale chromium-1228")
		}
		if _, ok := after["chromium-1243"]; !ok {
			t.Error("chromium-1243 (the integration's linked build) must stay")
		}
	})
}

// ---------------------------------------------------------------------------------------------
// BODY-2: once ~/.local/bin/gh is at the pin, install_gh retires the apt build
// (/usr/bin/gh 2.90.0): git's credential helpers are re-pointed, /usr/local/bin/gh links to the
// pinned binary for image-PATH callers, and the apt package is removed through _apt_get.

// qgSandboxedStub passes through to the real tool only when every absolute path argument lies in
// the sandbox, so a write aimed at the host (its /usr/local/bin) is logged as a MUTATION and refused.
func qgSandboxedStub(name, real string) string {
	return `echo "` + name + ` $*" >>"$CV_LOG"
for a in "$@"; do
	case "$a" in
	"$QG_BASE"/*) ;;
	/*) echo "MUTATION ` + name + ` $*" >>"$CV_LOG"; exit 1 ;;
	esac
done
exec '` + real + `' "$@"
`
}

const qgSudoStub = `echo "sudo $*" >>"$CV_LOG"
[ "${1-}" = -n ] && shift
[ "${QG_SUDO_RC:-0}" = 0 ] || exit 1
[ "${1-}" = true ] && exit 0
export VIA_SUDO=1
exec "$@"
`

const qgAptGetStub = `echo "apt-get fe=${DEBIAN_FRONTEND-} sudo=${VIA_SUDO:-0} $*" >>"$CV_LOG"
for a in "$@"; do
	case "$a" in
	remove|purge)
		if [ "${QG_APT_REMOVE_RC:-0}" != 0 ]; then
			echo "E: Could not get lock /var/lib/dpkg/lock-frontend" >&2
			exit "$QG_APT_REMOVE_RC"
		fi
		[ -f "$QG_APT_GH" ] && echo config-files >"$QG_APT_GH"
		exit 0 ;;
	esac
done
echo "MUTATION apt-get $*" >>"$CV_LOG"
exit 1
`

// qgDpkgQueryGhStub models the apt gh as dpkg reports it: "installed", or the config-files state a
// plain `apt-get remove` leaves behind (still listed, no longer installed), or never installed.
const qgDpkgQueryGhStub = `echo "dpkg-query $*" >>"$CV_LOG"
fmt=""; next=0; mode=W; gh=0
for a in "$@"; do
	if [ "$next" = 1 ]; then fmt="$a"; next=0; continue; fi
	case "$a" in
	-f|--showformat) next=1 ;;
	-f=*|--showformat=*) fmt="${a#*=}" ;;
	-s|--status) mode=s ;;
	-l|--list) mode=l ;;
	gh) gh=1 ;;
	esac
done
state="$(cat "$QG_APT_GH" 2>/dev/null)" || state=""
if [ "$gh" != 1 ] || [ -z "$state" ]; then echo "dpkg-query: no packages found matching gh" >&2; exit 1; fi
case "$state" in
installed) st="install ok installed"; ab="ii" ;;
*) st="deinstall ok config-files"; ab="rc" ;;
esac
case "$mode" in
s) printf 'Package: gh\nStatus: %s\nVersion: 2.90.0\n' "$st"; exit 0 ;;
l) printf '||/ Name Version Architecture Description\n%s  gh  2.90.0  arm64  GitHub CLI\n' "$ab"; exit 0 ;;
esac
[ -n "$fmt" ] || fmt='${binary:Package}\t${Version}\n'
out="$(printf '%s' "$fmt" | sed -e "s/[$]{db:Status-Abbrev}/$ab /g" -e "s/[$]{db:Status-Status}/${st##* }/g" \
	-e "s/[$]{Status}/$st/g" -e 's/[$]{binary:Package}/gh/g' -e 's/[$]{Package}/gh/g' -e 's/[$]{Version}/2.90.0/g')"
printf '%b' "$out"
`

const qgDpkgGhStub = `case "$1" in
-s|--status|-l|--list) exec dpkg-query "$@" ;;
*) echo "dpkg $*" >>"$CV_LOG"; exit 1 ;;
esac
`

// qgGitCredentialStub keeps git's global credential helpers as `key value` lines in $QG_GITCFG, so
// a re-point is read back from the file.
const qgGitCredentialStub = `echo "git $*" >>"$CV_LOG"
[ "${1-}" = config ] || exit 1
shift
[ "${1-}" = --global ] && shift
case "${1-}" in
--get-regexp) cat "$QG_GITCFG" ;;
--replace-all)
	awk -v k="$2" -v n="$3" -v p="${4-}" '{
		sp = index($0, " "); key = sp ? substr($0, 1, sp - 1) : $0; val = sp ? substr($0, sp + 1) : ""
		if (key == k && (p == "" || index(val, p) > 0)) print k " " n; else print
	}' "$QG_GITCFG" >"$QG_GITCFG.new" && mv "$QG_GITCFG.new" "$QG_GITCFG" ;;
*) echo "MUTATION git config $*" >>"$CV_LOG"; exit 1 ;;
esac
`

const qgAptGitCfg = "credential.https://github.com.helper \n" +
	"credential.https://github.com.helper !/usr/bin/gh auth git-credential\n" +
	"credential.https://gist.github.com.helper \n" +
	"credential.https://gist.github.com.helper !/usr/bin/gh auth git-credential\n"

const qgGhCurlStub = `echo "curl $*" >>"$CV_LOG"
[ "${QG_GH_DOWNLOAD-}" = ok ] || exit 22
out=""
while [ $# -gt 0 ]; do case "$1" in -o) out="$2"; shift 2 ;; *) shift ;; esac; done
echo tarball >"$out"
`

func qgGhScript(version string) string {
	return "#!/bin/sh\n" + `case "$1" in
--version) printf 'gh version %s (2026-04-16)\nhttps://github.com/cli/cli/releases/tag/v%s\n' "` + version + `" "` + version + `" ;;
*) echo "gh $*" >>"$CV_LOG"; exit 1 ;;
esac
`
}

// qgRetireUnderSandbox renames the real _retire_apt_gh and puts a recorder in its place, so
// install_gh runs unmodified while the link lands in the sandbox's stand-in for /usr/local/bin.
// The recorder logs the directory install_gh passed, so the production literal is asserted.
func qgRetireUnderSandbox(content string) string {
	fn := topLevelShellFunction(content, "_retire_apt_gh")
	if fn == "" {
		return ""
	}
	return strings.Replace(fn, "_retire_apt_gh() {", "qg_retire_apt_gh_real() {", 1) + "\n" +
		`_retire_apt_gh() { echo "RETIRE $*" >>"$CV_LOG"; qg_retire_apt_gh_real "$QG_LINK_DIR"; }` + "\n"
}

type qgGh struct {
	s                                    *cvSandbox
	harness, pin, linkDir, gitcfg, aptGh string
	localGh                              string
}

func newQgGh(t *testing.T) *qgGh {
	t.Helper()
	s := newCvSandbox(t)
	g := &qgGh{
		s:       s,
		pin:     quickstartPin(t, "GH_VERSION"),
		linkDir: filepath.Join(s.base, "usr-local-bin"),
		gitcfg:  filepath.Join(s.base, "gitconfig"),
		aptGh:   filepath.Join(s.base, "apt-gh"),
		localGh: filepath.Join(s.home, ".local/bin/gh"),
	}
	g.harness = convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error", "log_step",
		"command_exists", "_arch", "_record_row", "_quarantine_stale", "_converge", "_apt_get", "install_gh") +
		qgRetireUnderSandbox(readQuickstart(t))
	if err := os.MkdirAll(g.linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ln", "mv", "rm", "chmod", "mkdir", "touch"} {
		s.stub(n, qgSandboxedStub(n, qgRealTool(t, n)))
	}
	s.stub("uname", `case "$1" in -m) echo aarch64 ;; *) echo Linux ;; esac`+"\n")
	s.stub("id", `case "$1" in -u) echo "${QG_UID:-1000}" ;; *) echo "uid=${QG_UID:-1000}" ;; esac`+"\n")
	s.stub("sudo", qgSudoStub)
	s.stub("apt-get", qgAptGetStub)
	s.stub("dpkg-query", qgDpkgQueryGhStub)
	s.stub("dpkg", qgDpkgGhStub)
	s.stub("git", qgGitCredentialStub)
	s.stub("curl", qgGhCurlStub)
	s.stub("sha256sum", cvSha256CheckStub)
	s.stub("tar", `echo "tar $*" >>"$CV_LOG"
dir=""
while [ $# -gt 0 ]; do case "$1" in -C) dir="$2"; shift 2 ;; *) shift ;; esac; done
d="$dir/gh_${QG_GH_PIN}_linux_arm64/bin"
mkdir -p "$d" && cat "$QG_GH_TEMPLATE" >"$d/gh" && chmod 0755 "$d/gh"
`)
	s.stub("install", `echo "install $*" >>"$CV_LOG"
src=""; dst=""
while [ $# -gt 0 ]; do
	case "$1" in
	-m) shift 2 ;;
	*) if [ -z "$src" ]; then src="$1"; else dst="$1"; fi; shift ;;
	esac
done
cat "$src" >"$dst" && chmod 0755 "$dst"
`)
	s.writeFile(g.gitcfg, qgAptGitCfg, 0o644)
	s.writeFile(filepath.Join(s.base, "gh-template"), qgGhScript(g.pin), 0o755)
	return g
}

func (g *qgGh) seedLocalGh()           { g.s.writeFile(g.localGh, qgGhScript(g.pin), 0o755) }
func (g *qgGh) seedPathGh(v string)    { g.s.stub("gh", strings.TrimPrefix(qgGhScript(v), "#!/bin/sh\n")) }
func (g *qgGh) seedAptGh(state string) { g.s.writeFile(g.aptGh, state+"\n", 0o644) }
func (g *qgGh) link() string           { return filepath.Join(g.linkDir, "gh") }

// run calls install_gh bare, as main does, from inside the sandbox so a relative path can never
// land in the repository; the APT_CHANGED line doubles as the proof that install_gh returned.
func (g *qgGh) run(t *testing.T, call string, extra ...string) cvRun {
	t.Helper()
	sha := quickstartPin(t, "GH_SHA256_ARM64")
	r := g.s.run(g.harness, `cd "$QG_BASE"; `+call+`; echo "APT_CHANGED=$APT_CHANGED"`, append([]string{
		"QG_BASE=" + g.s.base, "QG_LINK_DIR=" + g.linkDir, "QG_GITCFG=" + g.gitcfg, "QG_APT_GH=" + g.aptGh,
		"QG_GH_PIN=" + g.pin, "QG_GH_TEMPLATE=" + filepath.Join(g.s.base, "gh-template"), "CV_EXPECT_SHA256=" + sha,
	}, extra...)...)
	if r.rc != 0 || !cvHasLine(r.out, "APT_CHANGED=0") {
		t.Errorf("install_gh must return 0 under errexit with APT_CHANGED untouched (D-66; a gh-only removal is not a library change); rc = %d\n%s", r.rc, r.out)
	}
	for _, l := range qgLines(r.log, "MUTATION ", "") {
		t.Errorf("a write outside the sandbox (the host's /usr/local/bin?): %s", l)
	}
	return r
}

func (g *qgGh) removals(log string) []string { return qgLines(log, "apt-get ", " remove -y gh") }

func (g *qgGh) expectRepointed(t *testing.T) {
	t.Helper()
	cfg, _ := qgReadFile(t, g.gitcfg)
	if strings.Contains(cfg, "/usr/bin/gh") {
		t.Errorf("git still authenticates through the apt gh it is about to lose:\n%s", cfg)
	}
	if n := strings.Count(cfg, " !"+g.localGh+" auth git-credential\n"); n != 2 {
		t.Errorf("want both credential helpers on %s; got %d:\n%s", g.localGh, n, cfg)
	}
	if n := strings.Count(cfg, ".helper \n"); n != 2 {
		t.Errorf("each key's empty reset entry must stay; got %d:\n%s", n, cfg)
	}
}

func (g *qgGh) expectLinked(t *testing.T) {
	t.Helper()
	if target, err := os.Readlink(g.link()); err != nil || target != g.localGh {
		t.Errorf("<link dir>/gh must link to %s for image-PATH callers; readlink = %q, %v", g.localGh, target, err)
	}
}

func (g *qgGh) expectRetired(t *testing.T, r cvRun, viaSudo string) {
	t.Helper()
	if calls := qgLines(r.log, "RETIRE ", ""); len(calls) != 1 || calls[0] != "RETIRE /usr/local/bin" {
		t.Errorf("install_gh must call `_retire_apt_gh /usr/local/bin` once; got %q", calls)
	}
	want := "apt-get fe=noninteractive sudo=" + viaSudo + " -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold remove -y gh"
	if removes := g.removals(r.log); len(removes) != 1 || removes[0] != want {
		t.Errorf("want exactly one removal through _apt_get, `%s`; got %q", want, removes)
	}
	if st, _ := qgReadFile(t, g.aptGh); st != "config-files\n" {
		t.Errorf("the apt gh is still installed (dpkg state %q)", st)
	}
	g.expectRepointed(t)
	g.expectLinked(t)
	if remove := qgLineIndex(r.log, "apt-get ", " remove -y gh", false); remove != -1 {
		if repoint := qgLineIndex(r.log, "git ", "--replace-all", true); repoint == -1 || repoint > remove {
			t.Errorf("the credential helpers must be re-pointed (call %d) before the apt gh is removed (call %d)", repoint, remove)
		}
		if ln := qgLineIndex(r.log, "ln ", g.link(), false); ln == -1 || ln > remove {
			t.Errorf("the link (call %d) must exist before the apt gh is removed (call %d)", ln, remove)
		}
	}
	if r.ledger != "" {
		t.Errorf("CONVERGE_FAILURES = %q, want empty", r.ledger)
	}
}

func (g *qgGh) expectNothingRetired(t *testing.T, r cvRun) {
	t.Helper()
	if removes := g.removals(r.log); len(removes) != 0 {
		t.Errorf("the apt gh must stay: %q", removes)
	}
	if _, err := os.Lstat(g.link()); err == nil {
		t.Errorf("<link dir>/gh was created")
	}
	if cfg, _ := qgReadFile(t, g.gitcfg); cfg != qgAptGitCfg {
		t.Errorf("git's credential helpers changed:\n%s", cfg)
	}
}

func TestQuickstartInstallGhRetiresTheAptBuild(t *testing.T) {
	content := readQuickstart(t)

	t.Run("install_gh hands /usr/local/bin to a top-level _retire_apt_gh", func(t *testing.T) {
		helper := topLevelShellFunction(content, "_retire_apt_gh")
		if helper == "" {
			t.Fatal("_retire_apt_gh() is not defined at top level (tests call it with a sandbox path)")
		}
		body := cvCode(helper)
		if !strings.Contains(body, "_apt_get remove -y gh") {
			t.Error("_retire_apt_gh() must remove the package with `_apt_get remove -y gh` (the H-5 env passthrough)")
		}
		if strings.Contains(body, "/usr/local/bin") {
			t.Error("_retire_apt_gh() names /usr/local/bin; the caller passes the directory")
		}
		for _, re := range cvBash32Avoid {
			if m := re.FindString(body); m != "" {
				t.Errorf("_retire_apt_gh() uses %q, which macOS's bash 3.2 lacks (D-11)", strings.TrimSpace(m))
			}
		}
		install := cvCode(topLevelShellFunction(content, "install_gh"))
		calls := regexp.MustCompile(`(?m)^[ \t]*_retire_apt_gh "?/usr/local/bin"?[ \t]*(\|\|[ \t]*true)?$`).FindAllStringIndex(install, -1)
		if len(calls) != 1 {
			t.Fatalf("install_gh() must call `_retire_apt_gh /usr/local/bin` exactly once; found %d", len(calls))
		}
		if c := strings.Index(install, `_converge gh "$GH_VERSION"`); c == -1 || c > calls[0][0] {
			t.Error("install_gh() must retire the apt gh only after `_converge gh` has run")
		}
	})

	t.Run("kept pinned gh: re-pointed, linked, then the apt build removed", func(t *testing.T) {
		g := newQgGh(t)
		g.seedLocalGh()
		g.seedAptGh("installed")
		r := g.run(t, "install_gh")
		g.expectRetired(t, r, "1")
		cvExpectRow(t, r.rows, "gh", g.pin, g.pin, "kept")
		if strings.Contains(r.out, "retiring the apt gh as root") {
			t.Errorf("a sudo run is not a root run, yet it warned as one\n%s", r.out)
		}
	})

	t.Run("a converged gh is retired the same way", func(t *testing.T) {
		g := newQgGh(t)
		g.seedPathGh("2.90.0")
		g.seedAptGh("installed")
		r := g.run(t, "install_gh", "QG_GH_DOWNLOAD=ok")
		g.expectRetired(t, r, "1")
		cvExpectRow(t, r.rows, "gh", g.pin, g.pin, "converged")
	})

	t.Run("as root the removal runs directly through _apt_get", func(t *testing.T) {
		g := newQgGh(t)
		g.seedLocalGh()
		g.seedAptGh("installed")
		r := g.run(t, "install_gh", "QG_UID=0")
		g.expectRetired(t, r, "0")
		// The pinned gh is root's, so other users lose /usr/bin/gh with nothing they can run in its place.
		if !strings.Contains(r.out, "retiring the apt gh as root") || !strings.Contains(r.out, "/usr/bin/gh must switch to") {
			t.Errorf("a root run retires the apt gh without warning that other users lose /usr/bin/gh\n%s", r.out)
		}
	})

	for _, tc := range []struct {
		name string
		seed func(t *testing.T, g *qgGh) string
		read func(p string) (string, error)
	}{
		{"an operator gh in the link dir is never clobbered/regular file", func(t *testing.T, g *qgGh) string {
			g.s.writeFile(g.link(), "#!/bin/sh\necho operator gh\n", 0o755)
			return "#!/bin/sh\necho operator gh\n"
		}, func(p string) (string, error) { b, err := os.ReadFile(p); return string(b), err }},
		{"an operator gh in the link dir is never clobbered/foreign link", func(t *testing.T, g *qgGh) string {
			if err := os.Symlink("/opt/operator/gh", g.link()); err != nil {
				t.Fatal(err)
			}
			return "/opt/operator/gh"
		}, os.Readlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newQgGh(t)
			g.seedLocalGh()
			g.seedAptGh("installed")
			want := tc.seed(t, g)
			r := g.run(t, "install_gh")
			if got, err := tc.read(g.link()); err != nil || got != want {
				t.Errorf("<link dir>/gh = %q, %v; an operator's own file stays", got, err)
			}
			if removes := g.removals(r.log); len(removes) != 1 {
				t.Errorf("the apt gh is still removed when the link is the operator's; got %q", removes)
			}
			cvExpectRow(t, r.rows, "gh", g.pin, g.pin, "kept")
		})
	}

	t.Run("a failed apt removal fails the gh row and ledgers gh", func(t *testing.T) {
		g := newQgGh(t)
		g.seedLocalGh()
		g.seedAptGh("installed")
		r := g.run(t, "install_gh", "QG_APT_REMOVE_RC=100")
		if removes := g.removals(r.log); len(removes) != 1 {
			t.Errorf("want one removal attempt, got %q", removes)
		}
		if r.ledger != "gh\n" {
			t.Errorf("CONVERGE_FAILURES = %q, want %q: a reachable vulnerable gh is what a required row fails on", r.ledger, "gh\n")
		}
		rows := cvRowsNamed(r.rows, "gh")
		if len(rows) != 1 || rows[0].result != "FAILED" || rows[0].pin != g.pin ||
			!strings.Contains(rows[0].installed, "apt gh") || !strings.Contains(rows[0].installed, "still installed") {
			t.Errorf("want one gh row: pin %s, installed naming `apt gh … still installed`, FAILED; got %+v", g.pin, rows)
		}
	})

	t.Run("without root: a warning, no sudo, no ledger", func(t *testing.T) {
		g := newQgGh(t)
		g.seedLocalGh()
		g.seedAptGh("installed")
		r := g.run(t, "install_gh", "QG_SUDO_RC=1")
		if calls := qgLines(r.log, "apt-get ", ""); len(calls) != 0 {
			t.Errorf("no root, yet apt-get ran: %q", calls)
		}
		for _, l := range qgLines(r.log, "sudo ", "") {
			if l != "sudo -n true" {
				t.Errorf("without root the only sudo is the `sudo -n true` probe: %s", l)
			}
		}
		if _, err := os.Lstat(g.link()); err == nil {
			t.Error("<link dir>/gh was created without root")
		}
		if r.ledger != "" {
			t.Errorf("CONVERGE_FAILURES = %q, want empty: no root is a warning", r.ledger)
		}
		cvExpectRow(t, r.rows, "gh", g.pin, g.pin, "kept")
		if !regexp.MustCompile(`(?m)^\[WARN\] .*apt gh`).MatchString(r.out) {
			t.Errorf("stdout lacks a [WARN] line naming the `apt gh` that stays installed\n%s", r.out)
		}
	})

	t.Run("--check retires nothing", func(t *testing.T) {
		g := newQgGh(t)
		g.seedLocalGh()
		g.seedAptGh("installed")
		r := g.run(t, "CHECK_ONLY=true; install_gh")
		g.expectNothingRetired(t, r)
		if calls := append(qgLines(r.log, "apt-get ", ""), qgLines(r.log, "sudo ", "")...); len(calls) != 0 {
			t.Errorf("--check changes nothing: %q", calls)
		}
		if r.ledger != "" {
			t.Errorf("CONVERGE_FAILURES = %q, want empty", r.ledger)
		}
		cvExpectRow(t, r.rows, "gh", g.pin, g.pin, "kept")
	})

	t.Run("a failed gh converge keeps the apt build", func(t *testing.T) {
		g := newQgGh(t)
		g.seedPathGh("2.90.0")
		g.seedAptGh("installed")
		r := g.run(t, "install_gh")
		g.expectNothingRetired(t, r)
		if r.ledger != "gh\n" {
			t.Errorf("CONVERGE_FAILURES = %q, want %q", r.ledger, "gh\n")
		}
		cvExpectRow(t, r.rows, "gh", g.pin, "2.90.0", "FAILED")
	})

	t.Run("a kept row answered by the apt build itself keeps it", func(t *testing.T) {
		g := newQgGh(t)
		g.seedPathGh(g.pin)
		g.seedAptGh("installed")
		r := g.run(t, "install_gh")
		g.expectNothingRetired(t, r)
		cvExpectRow(t, r.rows, "gh", g.pin, g.pin, "kept")
	})

	t.Run("a re-run after the retirement changes nothing", func(t *testing.T) {
		g := newQgGh(t)
		g.seedLocalGh()
		g.seedAptGh("config-files")
		repointed := strings.ReplaceAll(qgAptGitCfg, "!/usr/bin/gh", "!"+g.localGh)
		g.s.writeFile(g.gitcfg, repointed, 0o644)
		if err := os.Symlink(g.localGh, g.link()); err != nil {
			t.Fatal(err)
		}
		r := g.run(t, "install_gh")
		if removes := g.removals(r.log); len(removes) != 0 {
			t.Errorf("dpkg reports gh deinstalled (config-files), yet it was removed again: %q", removes)
		}
		g.expectLinked(t)
		if cfg, _ := qgReadFile(t, g.gitcfg); cfg != repointed {
			t.Errorf("git's credential helpers changed:\n%s", cfg)
		}
		if r.ledger != "" {
			t.Errorf("CONVERGE_FAILURES = %q, want empty", r.ledger)
		}
		cvExpectRow(t, r.rows, "gh", g.pin, g.pin, "kept")
	})
}

// ---------------------------------------------------------------------------------------------
// BODY-4 (K16): "Setup Complete!" prints only after _converge_report returns 0. The static
// order is pinned by the K-order subtest and TestQuickstartBannerAfterLitellmTelemetryAndConvergeReport;
// these cases keep the banner out of the report itself and out of --check.

func TestQuickstartBannerFollowsAPassingReport(t *testing.T) {
	content := readQuickstart(t)

	for _, tc := range []struct{ name, call string }{
		{"_converge_report never prints the banner/run", "_converge_report"},
		{"_converge_report never prints the banner/check", "CHECK_ONLY=true; _converge_report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCvSandbox(t)
			harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error", "log_step",
				"_os_codename", "_factory_repo_dir", "_converge_report")
			r := s.run(harness, tc.call)
			if r.rc != 0 || !strings.Contains(r.out, "Convergence summary") {
				t.Fatalf("an empty ledger reports and returns 0; rc = %d\n%s", r.rc, r.out)
			}
			if strings.Contains(r.out, "Setup Complete!") {
				t.Errorf("_converge_report printed the banner; --check calls it too, and main prints the banner after it\n%s", r.out)
			}
		})
	}

	t.Run("main never ends on an && list", func(t *testing.T) {
		var last string
		for _, l := range strings.Split(cvCode(topLevelShellFunction(content, "main")), "\n") {
			if tl := strings.TrimSpace(l); tl != "" && tl != "}" {
				last = tl
			}
		}
		if strings.Contains(last, "&&") || strings.Contains(last, "||") {
			t.Errorf("main() ends on `%s`; its status is the script's exit status under set -e, so a missing tool would fail a converged run", last)
		}
	})

	t.Run("whole script --check prints no banner and retires nothing", func(t *testing.T) {
		root := findModuleRoot(t)
		s := newCvSandbox(t)
		for _, f := range []struct {
			rel  string
			mode os.FileMode
		}{{"quickstart.sh", 0o755}, {"py/requirements.txt", 0o644}, {"py/litellm-requirements.txt", 0o644}, {"go.mod", 0o644}} {
			copyIntoSrc(t, root, s.src, f.rel, f.mode)
		}
		for _, n := range []string{"mv", "ln", "chmod"} {
			s.stub(n, cvGuardedStub(n, qgRealTool(t, n)))
		}
		for _, n := range []string{"curl", "wget", "tar", "install", "cp", "make", "tee", "nohup", "setsid"} {
			s.stub(n, cvMutationStub(n))
		}
		s.stub("uname", `case "$1" in -m) echo aarch64 ;; *) echo Linux ;; esac`+"\n")
		s.stub("id", `case "$1" in -u|-g) echo 1000 ;; *) echo "uid=1000(dev) gid=1000(dev)" ;; esac`+"\n")
		s.stub("sudo", `echo "sudo $*" >>"$CV_LOG"
[ "$*" = "-n true" ] && exit 0
echo "MUTATION sudo $*" >>"$CV_LOG"; exit 1
`)
		s.stub("git", `echo "git $*" >>"$CV_LOG"
case "$1" in
--version) echo "git version 2.43.0" ;;
config) case "$*" in *--get*|*--list*|*" -l"*) exit 1 ;; *) echo "MUTATION git $*" >>"$CV_LOG"; exit 1 ;; esac ;;
*) exit 1 ;;
esac
`)
		// main puts $HOME/go/bin ahead of the host's /usr/local/go/bin on PATH.
		s.writeFile("go/bin/go", "#!/bin/sh\n"+cvCheckGoStub, 0o755)
		pin := quickstartPin(t, "GH_VERSION")
		s.writeFile(".local/bin/gh", qgGhScript(pin), 0o755)
		aptGh := s.writeFile(filepath.Join(s.base, "apt-gh"), "installed\n", 0o644)
		s.stub("dpkg-query", qgDpkgQueryGhStub)
		workdir := filepath.Join(s.base, "cwd")
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			t.Fatal(err)
		}
		homeBefore := cvSnapshot(t, s.home, ".tmp")

		cmd := exec.Command("bash", filepath.Join(s.src, "quickstart.sh"), "--check")
		cmd.Dir = workdir
		cmd.Env = s.env("QG_APT_GH="+aptGh, "QG_BASE="+s.base)
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

		if rc != 1 || !strings.Contains(out, "Convergence summary") {
			t.Errorf("--check must reach the report and exit 1 on drift; rc = %d", rc)
		}
		if strings.Contains(out, "Setup Complete!") {
			t.Error("--check printed the completion banner")
		}
		for _, l := range strings.Split(calls, "\n") {
			switch {
			case strings.HasPrefix(l, "MUTATION "):
				t.Errorf("--check made a mutating call: %s", l)
			case strings.Contains(l, "remove"):
				t.Errorf("--check reached a removal: %s", l)
			case strings.HasPrefix(l, "sudo ") && l != "sudo -n true":
				t.Errorf("--check ran sudo: %s", l)
			}
		}
		if d := cvSnapshotDiff(homeBefore, cvSnapshot(t, s.home, ".tmp")); len(d) > 0 {
			t.Errorf("--check changed $HOME:\n%s", strings.Join(d, "\n"))
		}
		if rec, ok := cvReadRecord(t, s.afRoot); ok {
			found := false
			for _, c := range rec.Components {
				if c.Name == "gh" {
					found = true
					if c.Result != "kept" || c.Installed != pin {
						t.Errorf("convergence.json gh = %s/%s, want kept/%s", c.Result, c.Installed, pin)
					}
				}
			}
			if !found {
				t.Error("convergence.json has no gh row")
			}
		}
		if t.Failed() {
			t.Logf("stdout:\n%s\ncalls:\n%s", out, calls)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// BODY-9: the setup_telemetry header states the mixed policy the code runs — a REQUIRED binary
// converge, launch hiccups that warn, and an installed backend that converges whatever
// --no-telemetry says (D-47).

func qgCommentAbove(t *testing.T, content, fn string) string {
	t.Helper()
	at := strings.Index(content, "\n"+fn+"() {")
	if at == -1 {
		t.Fatalf("%s() is not defined at top level", fn)
	}
	lines := strings.Split(content[:at], "\n")
	var block []string
	for i := len(lines) - 1; i >= 0 && strings.HasPrefix(lines[i], "#"); i-- {
		block = append([]string{lines[i]}, block...)
	}
	return strings.Join(block, "\n")
}

func TestQuickstartSetupTelemetryHeaderStatesTheRequiredClass(t *testing.T) {
	header := qgCommentAbove(t, readQuickstart(t), "setup_telemetry")
	if header == "" {
		t.Fatal("setup_telemetry() has no header comment")
	}
	lower := strings.ToLower(header)
	for _, stale := range []string{"--no-telemetry to skip", "must never fail the whole bootstrap"} {
		if strings.Contains(lower, stale) {
			t.Errorf("the setup_telemetry header still says %q; an installed backend converges as a required component", stale)
		}
	}
	for _, want := range []string{"required", "launch", "warn", "--no-telemetry", "installed", "d-47", "af telemetry on"} {
		if !strings.Contains(lower, want) {
			t.Errorf("the setup_telemetry header lacks %q (required binary converge, launch hiccups warn, an installed backend converges regardless of --no-telemetry per D-47, installing is not `af telemetry on`)", want)
		}
	}
	if t.Failed() {
		t.Logf("header:\n%s", header)
	}
}
