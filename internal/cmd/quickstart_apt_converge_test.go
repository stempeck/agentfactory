package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var aptAllowedPrefixes = map[string]bool{
	"env DEBIAN_FRONTEND=noninteractive":         true,
	"sudo -n env DEBIAN_FRONTEND=noninteractive": true,
}

// aptInterlockErrors is the static apt-line interlock (K5): every literal apt-get runs
// non-interactively, as root or under a non-prompting sudo, and keeps the operator's conffiles
// wherever it can install. A bare apt-get added later fails it.
func aptInterlockErrors(content string) (errs []string, count int) {
	for _, inv := range shellInvocations(content) {
		switch inv.Cmd {
		case "apt", "aptitude":
			errs = append(errs, fmt.Sprintf("%s: use the _apt_get wrapper, never %s", inv, inv.Cmd))
			continue
		case "apt-get":
		default:
			continue
		}
		count++
		if !aptAllowedPrefixes[strings.Join(inv.Prefix, " ")] {
			errs = append(errs, fmt.Sprintf("%s: apt-get must run as `env DEBIAN_FRONTEND=noninteractive apt-get` or "+
				"`sudo -n env DEBIAN_FRONTEND=noninteractive apt-get`", inv))
		}
		if aptMayInstall(inv) && (!inv.hasFlagValue("-o", "Dpkg::Options::=--force-confdef") ||
			!inv.hasFlagValue("-o", "Dpkg::Options::=--force-confold")) {
			errs = append(errs, fmt.Sprintf("%s: an installing apt-get must carry -o Dpkg::Options::=--force-confdef "+
				"-o Dpkg::Options::=--force-confold", inv))
		}
	}
	return errs, count
}

// aptMayInstall: the wrapper forwards "$@", so its literal lines install whatever a caller asks.
func aptMayInstall(inv shellInvocation) bool {
	for _, a := range inv.Args {
		switch shellUnquote(a) {
		case "install", "upgrade", "dist-upgrade", "full-upgrade", "$@":
			return true
		}
	}
	return false
}

func aptSimulates(inv shellInvocation) bool {
	return inv.hasArg("-s") || inv.hasArg("--simulate") || inv.hasArg("--dry-run")
}

func TestQuickstartAptConverge(t *testing.T) {
	content := readQuickstart(t)

	t.Run("every apt-get line is non-interactive and keeps conffiles", func(t *testing.T) {
		errs, n := aptInterlockErrors(content)
		for _, e := range errs {
			t.Error(e)
		}
		if n < 2 {
			t.Errorf("quickstart.sh has %d literal apt-get invocation(s); the _apt_get wrapper holds the root and the "+
				"sudo -n line (K5) — the root layer is missing", n)
		}
	})

	t.Run("call sites update with Error-Mode=any and pin nodejs on its own line", func(t *testing.T) {
		var updates, pockets, nodeLines int
		for _, inv := range shellInvocations(content) {
			if inv.Cmd != "_apt_get" && inv.Cmd != "apt-get" {
				continue
			}
			pos := inv.positionals()
			if len(pos) == 0 {
				continue
			}
			verb := shellUnquote(pos[0])
			nodePin := inv.hasArg(`nodejs=$NODE_APT_VERSION`)
			switch {
			case verb == "update":
				updates++
				if !inv.hasFlagValue("-o", "APT::Update::Error-Mode=any") {
					t.Errorf("%s: update must carry -o APT::Update::Error-Mode=any", inv)
				}
			case verb == "install" && !aptSimulates(inv):
				only := inv.hasArg("--only-upgrade")
				switch {
				case only && nodePin:
					t.Errorf("%s: the nodejs pin must be its own line, never inside the pocket install", inv)
				case only:
					pockets++
				case nodePin && inv.hasArg("--allow-downgrades"):
					nodeLines++
				default:
					t.Errorf("%s: an install is either the --only-upgrade pocket install or the "+
						"--allow-downgrades nodejs=\"$NODE_APT_VERSION\" line", inv)
				}
			}
		}
		if updates < 1 {
			t.Error("no `_apt_get update -o APT::Update::Error-Mode=any` call; the pocket and NodeSource indexes are never refreshed")
		}
		if pockets < 1 {
			t.Error("no `_apt_get install -y --only-upgrade <selected…>` pocket install")
		}
		if nodeLines != 1 {
			t.Errorf("found %d `--allow-downgrades nodejs=\"$NODE_APT_VERSION\"` install lines, want exactly 1", nodeLines)
		}
	})

	t.Run("root is decided before any apt update", func(t *testing.T) {
		body := topLevelShellFunction(content, "_apt_converge")
		if body == "" {
			t.Fatal("quickstart.sh defines no top-level _apt_converge(); the root layer is missing")
		}
		idU, sudoN, update := strings.Index(body, "id -u"), strings.Index(body, "sudo -n true"), strings.Index(body, "_apt_get update")
		if idU == -1 || sudoN == -1 || update == -1 || !(idU < sudoN && sudoN < update) {
			t.Errorf("_apt_converge must test `id -u`, then `sudo -n true`, before `_apt_get update` (offsets %d, %d, %d)",
				idU, sudoN, update)
		}
	})

	t.Run("the Node-only C-7 branch is present and gh stays off apt", func(t *testing.T) {
		// The source escapes the quote that follows (`apt said \"…`), so anchor on the text before it.
		if !strings.Contains(content, `cannot pin nodejs=$NODE_APT_VERSION: apt said `) || !strings.Contains(content, "nodesource.list") {
			t.Error("quickstart.sh lacks the Node-only C-7 text (`cannot pin nodejs=$NODE_APT_VERSION: apt said \"…` naming nodesource.list)")
		}
		for _, banned := range []string{"GH_APT_VERSION", "github-cli.list"} {
			if strings.Contains(content, banned) {
				t.Errorf("quickstart.sh mentions %s; gh converges from its release tarball, never apt (D-3)", banned)
			}
		}
	})

	t.Run("synthetic bare apt-get is reported", func(t *testing.T) {
		src := "if command_exists apt-get; then\n  sudo apt-get install -y jq\nfi\n" +
			"DEBIAN_FRONTEND=noninteractive apt-get -y upgrade\n" +
			"env DEBIAN_FRONTEND=noninteractive apt-get install -y curl\n"
		errs, n := aptInterlockErrors(src)
		if n != 3 || len(errs) != 5 {
			t.Fatalf("two wrongly wrapped installing lines (2 reports each) and one missing the Dpkg options must yield 5 reports; got count=%d errs=%q", n, errs)
		}
	})

	t.Run("synthetic wrapper lines are accepted and mentions ignored", func(t *testing.T) {
		src := "_apt_get() {\n" +
			"    if [ \"$(id -u)\" = 0 ]; then\n" +
			"        env DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold \"$@\"\n" +
			"    else\n" +
			"        sudo -n env DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef \\\n" +
			"            -o Dpkg::Options::=--force-confold \"$@\"\n" +
			"    fi\n}\n" +
			"command_exists apt-get || log_warn \"apt-get install failed\"\n# apt-get install foo\n"
		errs, n := aptInterlockErrors(src)
		if n != 2 || len(errs) != 0 {
			t.Fatalf("the two wrapper lines must count and pass, the mentions must not count; got count=%d errs=%q", n, errs)
		}
	})

	t.Run("CHECK_ONLY runs apt-get only with -s", func(t *testing.T) {
		h := newAptHarness(t, content, aptConvergeFns...)
		out, rc := runConvergeHarness(t, h.script, `CHECK_ONLY=true; _apt_converge; echo "APT_CHANGED=$APT_CHANGED"`, h.env)
		if rc != 0 {
			t.Fatalf("_apt_converge under CHECK_ONLY must return 0; rc=%d\n%s", rc, out)
		}
		log := h.log(t)
		apt := logLines(log, "APT ")
		if len(apt) == 0 {
			t.Errorf("CHECK_ONLY made no `apt-get -s` call; the root rows come from a simulation\nlog:\n%s", log)
		}
		for _, l := range apt {
			words := strings.Fields(l)
			if !containsWord(words, "-s") || containsWord(words, "update") {
				t.Errorf("CHECK_ONLY ran a mutating apt-get: %s", l)
			}
		}
		if s := logLines(log, "SUDO "); len(s) != 0 {
			t.Errorf("CHECK_ONLY must not touch sudo: %q", s)
		}
		if n := logLines(log, "NSL "); len(n) != 0 {
			t.Errorf("CHECK_ONLY must not rewrite the NodeSource list: %q", n)
		}
		if !strings.Contains(out, "APT_CHANGED=0") {
			t.Errorf("CHECK_ONLY must leave APT_CHANGED=0\n%s", out)
		}
		for _, want := range []string{
			"nodejs: 22.22.2-1nodesource1 != pinned " + h.nodePin + " (would converge)",
			"ubuntu-security: 1 pending != pinned noble-security (would converge)",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("CHECK_ONLY output lacks %q\n%s", want, out)
			}
		}
	})

	t.Run("normal run as root updates, upgrades the pocket, then pins nodejs", func(t *testing.T) {
		h := newAptHarness(t, content, aptConvergeFns...)
		out, rc := runConvergeHarness(t, h.script, `_apt_converge; echo "APT_CHANGED=$APT_CHANGED"`, h.env)
		if rc != 0 {
			t.Fatalf("_apt_converge must return 0; rc=%d\n%s", rc, out)
		}
		log := h.log(t)
		nsl, update, pocket, node := -1, -1, -1, -1
		for i, l := range strings.Split(log, "\n") {
			words := strings.Fields(l)
			switch {
			case strings.HasPrefix(l, "NSL "+h.nodeMajor) && nsl == -1:
				nsl = i
			case !strings.HasPrefix(l, "APT ") || containsWord(words, "-s"):
				continue
			case containsWord(words, "update"):
				update = i
				if !strings.Contains(l, "-o APT::Update::Error-Mode=any") {
					t.Errorf("update lacks -o APT::Update::Error-Mode=any: %s", l)
				}
			case containsWord(words, "--only-upgrade"):
				pocket = i
				for _, notSelected := range []string{"nodejs", "openssl", "curl", "zlib1g"} {
					if strings.Contains(l, notSelected) {
						t.Errorf("the pocket install must not carry %s: %s", notSelected, l)
					}
				}
				if !containsWord(words, "libkrb5-3") || !containsWord(words, "-y") {
					t.Errorf("the pocket install must be `install -y --only-upgrade libkrb5-3`: %s", l)
				}
			case containsWord(words, "nodejs="+h.nodePin):
				node = i
				if !containsWord(words, "--allow-downgrades") || !containsWord(words, "-y") {
					t.Errorf("the nodejs line must be `install -y --allow-downgrades nodejs=<pin>`: %s", l)
				}
			}
			if strings.HasPrefix(l, "APT ") && (!strings.Contains(l, "fe=noninteractive sudo=0 ") ||
				!strings.Contains(l, "-o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold")) {
				t.Errorf("as root, apt-get runs directly, non-interactively, with both Dpkg options: %s", l)
			}
		}
		if !(nsl != -1 && nsl < update && update < pocket && pocket < node) {
			t.Errorf("want NodeSource list rewrite < update < pocket install < nodejs pin; got lines %d, %d, %d, %d\nlog:\n%s",
				nsl, update, pocket, node, log)
		}
		if !strings.Contains(out, "APT_CHANGED=1") {
			t.Errorf("an install that changed the dpkg database must set APT_CHANGED=1\n%s", out)
		}
	})
}

// TestPendingSecuritySelectsShadowedFix pins H-6: libkrb5-3's security fix is shadowed by a newer
// noble-updates candidate, so `apt-get upgrade -s` never shows it as a security upgrade. The
// selection is any version above the installed (***) one that a <codename>-security source carries.
func TestPendingSecuritySelectsShadowedFix(t *testing.T) {
	content := readQuickstart(t)
	if topLevelShellFunction(content, "_pending_security") == "" {
		t.Fatal("quickstart.sh defines no top-level _pending_security(); the H-6 pocket selection is missing")
	}
	h := newAptHarness(t, content, "_pending_security")
	out, rc := runConvergeHarness(t, h.script, `_pending_security noble >"$HOME/selection"`, h.env)
	if rc != 0 {
		t.Fatalf("_pending_security noble must return 0; rc=%d\n%s", rc, out)
	}
	data, err := os.ReadFile(filepath.Join(h.home, "selection"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(data))
	sort.Strings(got)
	if strings.Join(got, " ") != "libkrb5-3" {
		t.Errorf("_pending_security noble selected %q, want exactly [libkrb5-3]: openssl's security version is older "+
			"than the installed one, curl is on its security version and only noble-updates is newer, zlib1g's fix is "+
			"for another codename, nodejs is pinned on its own line", got)
	}
	if calls := logLines(h.log(t), "APTCACHE policy"); len(calls) != 1 {
		t.Errorf("_pending_security must read the version tables with ONE `apt-cache policy` call; got %d: %q", len(calls), calls)
	}
}

type aptHarness struct {
	script, home, bin, logPath, nodePin, nodeMajor string
	env                                            []string
}

func (h aptHarness) log(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(h.logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

var aptInstalledVersions = map[string]string{
	"libkrb5-3": "1.20.1-6ubuntu2.1",
	"openssl":   "3.0.13-0ubuntu3.5",
	"curl":      "8.5.0-2ubuntu10.6",
	"zlib1g":    "1:1.3.dfsg-3.1ubuntu2",
	"nodejs":    "22.22.2-1nodesource1",
}

// aptPolicies is real `apt-cache policy` output: candidates first, the installed version marked ***.
var aptPolicies = map[string]string{
	"libkrb5-3": `libkrb5-3:
  Installed: 1.20.1-6ubuntu2.1
  Candidate: 1.20.1-6ubuntu2.6
  Version table:
     1.20.1-6ubuntu2.6 500
        500 http://ports.ubuntu.com/ubuntu-ports noble-updates/main arm64 Packages
     1.20.1-6ubuntu2.5 500
        500 http://ports.ubuntu.com/ubuntu-ports noble-security/main arm64 Packages
 *** 1.20.1-6ubuntu2.1 100
        100 /var/lib/dpkg/status
`,
	"openssl": `openssl:
  Installed: 3.0.13-0ubuntu3.5
  Candidate: 3.0.13-0ubuntu3.5
  Version table:
 *** 3.0.13-0ubuntu3.5 500
        500 http://ports.ubuntu.com/ubuntu-ports noble-updates/main arm64 Packages
        100 /var/lib/dpkg/status
     3.0.13-0ubuntu3.4 500
        500 http://ports.ubuntu.com/ubuntu-ports noble-security/main arm64 Packages
     3.0.13-0ubuntu3 500
        500 http://ports.ubuntu.com/ubuntu-ports noble/main arm64 Packages
`,
	"curl": `curl:
  Installed: 8.5.0-2ubuntu10.6
  Candidate: 8.5.0-2ubuntu10.7
  Version table:
     8.5.0-2ubuntu10.7 500
        500 http://ports.ubuntu.com/ubuntu-ports noble-updates/main arm64 Packages
 *** 8.5.0-2ubuntu10.6 500
        500 http://ports.ubuntu.com/ubuntu-ports noble-security/main arm64 Packages
        100 /var/lib/dpkg/status
`,
	"zlib1g": `zlib1g:
  Installed: 1:1.3.dfsg-3.1ubuntu2
  Candidate: 1:1.3.dfsg-3.1ubuntu2
  Version table:
     1:1.3.dfsg-3.1ubuntu2.9 500
        500 http://ports.ubuntu.com/ubuntu-ports jammy-security/main arm64 Packages
 *** 1:1.3.dfsg-3.1ubuntu2 500
        500 http://ports.ubuntu.com/ubuntu-ports noble/main arm64 Packages
        100 /var/lib/dpkg/status
`,
	"nodejs": `nodejs:
  Installed: 22.22.2-1nodesource1
  Candidate: 22.22.2-1nodesource1
  Version table:
     22.99.0-1ubuntu1 500
        500 http://ports.ubuntu.com/ubuntu-ports noble-security/universe arm64 Packages
 *** 22.22.2-1nodesource1 600
        600 https://deb.nodesource.com/node_22.x nodistro/main arm64 Packages
        100 /var/lib/dpkg/status
`,
}

// The stubs below model dpkg-query, apt-cache, dpkg, apt-get, sudo, id and uname from a state dir,
// so no real package tool can run (PATH is the stub dir alone) and either selection strategy the
// seam allows (version-table order or dpkg --compare-versions) gets the same answers.
const aptDpkgQueryStub = `echo "DPKGQ $*" >>"$LOG"
fmt='${Package}\t${Version}\n'; pkgs=""; next_fmt=0
for a in "$@"; do
  if [ "$next_fmt" = 1 ]; then fmt="$a"; next_fmt=0; continue; fi
  case "$a" in
    -f|--showformat) next_fmt=1 ;;
    -f*) fmt="${a#-f}"; fmt="${fmt#=}" ;;
    --showformat=*) fmt="${a#*=}" ;;
    -*) ;;
    *) pkgs="$pkgs $a" ;;
  esac
done
[ -n "$pkgs" ] || pkgs="$(ls "$STATE")"
rc=0
for p in $pkgs; do
  if [ ! -f "$STATE/$p" ]; then echo "dpkg-query: no packages found matching $p" >&2; rc=1; continue; fi
  v="$(cat "$STATE/$p")"
  line="$(printf '%s' "$fmt" | sed -e 's/[$]{db:Status-Abbrev}/ii /g' -e 's/[$]{Status}/install ok installed/g' \
    -e "s/[$]{binary:Package}/$p/g" -e "s/[$]{Package}/$p/g" -e "s/[$]{Version}/$v/g")"
  printf '%b' "$line"
done
exit $rc
`

const aptCacheStub = `echo "APTCACHE $*" >>"$LOG"
[ "$1" = policy ] || exit 0
shift
for p in "$@"; do [ -f "$POLICY/$p" ] && cat "$POLICY/$p"; done
exit 0
`

const aptDpkgStub = `echo "DPKG $*" >>"$LOG"
case "$1" in
  --print-architecture) echo arm64 ;;
  --compare-versions)
    a="$2"; op="$3"; b="$4"
    hi="$(printf '%s\n%s\n' "$a" "$b" | sort -V | tail -n 1)"
    case "$op" in
      gt|'>>') [ "$a" != "$b" ] && [ "$hi" = "$a" ] ;;
      ge|'>=') [ "$a" = "$b" ] || [ "$hi" = "$a" ] ;;
      lt|'<<') [ "$a" != "$b" ] && [ "$hi" = "$b" ] ;;
      le|'<=') [ "$a" = "$b" ] || [ "$hi" = "$b" ] ;;
      eq) [ "$a" = "$b" ] ;;
      ne) [ "$a" != "$b" ] ;;
      *) exit 2 ;;
    esac ;;
  *) exit 0 ;;
esac
`

const aptGetStub = `echo "APT fe=${DEBIAN_FRONTEND:-unset} sudo=${VIA_SUDO:-0} $*" >>"$LOG"
sim=0
for a in "$@"; do case "$a" in -s|--simulate|--dry-run|--just-print|--no-act) sim=1 ;; esac; done
if [ "$sim" = 1 ]; then
  named=0
  for a in "$@"; do
    case "$a" in
      -*|install|upgrade|dist-upgrade|Dpkg::*|APT::*) ;;
      *) p="${a%%=*}"; [ -f "$STATE/$p" ] && { echo "Inst $p [$(cat "$STATE/$p")] (${a#*=} Ubuntu:24.04/noble-security [arm64])"; named=1; } ;;
    esac
  done
  [ "$named" = 1 ] || echo "Inst libkrb5-3 [1.20.1-6ubuntu2.1] (1.20.1-6ubuntu2.5 Ubuntu:24.04/noble-security [arm64])"
  exit 0
fi
for a in "$@"; do
  case "$a:${APT_FAIL:-}" in
    update:update) echo "E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/dists/noble-security/InRelease  Temporary failure resolving 'ports.ubuntu.com'"; exit 100 ;;
    --only-upgrade:only-upgrade) echo "E: Sub-process /usr/bin/dpkg returned an error code (1)"; exit 100 ;;
    nodejs=*:nodejs) echo "E: Version '${a#nodejs=}' for 'nodejs' was not found"; exit 100 ;;
  esac
done
for a in "$@"; do
  case "$a" in
    nodejs=*) printf '%s' "${a#nodejs=}" >"$STATE/nodejs" ;;
    libkrb5-3) printf '1.20.1-6ubuntu2.5' >"$STATE/libkrb5-3" ;;
  esac
done
echo "Reading package lists... Done"
exit 0
`

const aptSudoStub = `echo "SUDO $*" >>"$LOG"
[ "$1" = -n ] && shift
[ "$1" = true ] && exit "${STUB_SUDO_RC:-0}"
case "$1" in env|apt-get|apt-cache|dpkg-query) VIA_SUDO=1; export VIA_SUDO; exec "$@" ;; esac
exit 97
`

// The digest stubs only need to change when their input changes (APT_CHANGED compares a dpkg digest).
const aptDigestStub = `case "$1" in -c|--check) exit 1 ;; esac
cat "$@" | tr '\n' ' '; echo "  -"
`

var aptConvergeFns = []string{"_apt_converge", "_apt_get", "_pending_security", "_record_row"}

func newAptHarness(t *testing.T, content string, fns ...string) aptHarness {
	t.Helper()
	for _, fn := range fns {
		if topLevelShellFunction(content, fn) == "" {
			t.Fatalf("quickstart.sh defines no top-level %s(); the root layer (K5) is missing", fn)
		}
	}
	h := aptHarness{home: t.TempDir(), nodePin: quickstartPin(t, "NODE_APT_VERSION"), nodeMajor: quickstartPin(t, "NODE_MAJOR")}
	h.logPath = filepath.Join(h.home, "calls.log")
	state, policy := filepath.Join(h.home, "dpkg-state"), filepath.Join(h.home, "policy")
	for _, d := range []string{state, policy} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, v := range aptInstalledVersions {
		if err := os.WriteFile(filepath.Join(state, p), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range aptPolicies {
		if err := os.WriteFile(filepath.Join(policy, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	bin := hermeticConvergeBinDir(t, "xargs")
	h.bin = bin
	writeConvergeStub(t, bin, "dpkg-query", aptDpkgQueryStub)
	writeConvergeStub(t, bin, "apt-cache", aptCacheStub)
	writeConvergeStub(t, bin, "dpkg", aptDpkgStub)
	writeConvergeStub(t, bin, "apt-get", aptGetStub)
	writeConvergeStub(t, bin, "sudo", aptSudoStub)
	writeConvergeStub(t, bin, "id", `case "$1" in -u) echo "${STUB_UID:-1000}" ;; -un) [ "${STUB_UID:-1000}" = 0 ] && echo root || echo dev ;; *) echo "uid=${STUB_UID:-1000}" ;; esac
`)
	writeConvergeStub(t, bin, "uname", `case "$1" in -m) echo aarch64 ;; *) echo Linux ;; esac
`)
	for _, d := range []string{"sha256sum", "sha1sum", "md5sum", "cksum"} {
		writeConvergeStub(t, bin, d, aptDigestStub)
	}

	// The helpers _apt_converge hands a host path (/etc/apt/sources.list.d, /var/lib/apt/lists) are
	// replaced here, so no root-layer case can rename or read the host's apt state (ADR-018); a test
	// that exercises one appends its real body after these lines.
	h.script = convergeHarness(t, h.home, append([]string{"log_info", "log_success", "log_warn", "log_error",
		"command_exists"}, fns...)...) +
		"_os_codename() { echo noble; }\n" +
		"_ensure_nodesource_list() { echo \"NSL $*\" >>\"$LOG\"; }\n" +
		"_retire_gh_apt_source() { echo \"RETIRE $*\" >>\"$LOG\"; }\n" +
		"_apt_lists_date() { echo \"LISTS $*\" >>\"$LOG\"; printf '%s\\n' \"${STUB_APT_LISTS:-never refreshed}\"; }\n"
	h.env = hermeticConvergeEnv(bin, h.home, filepath.Join(h.home, "factory"),
		"LOG="+h.logPath, "STATE="+state, "POLICY="+policy, "STUB_UID=0")
	return h
}

func logLines(log, prefix string) []string {
	var out []string
	for _, l := range strings.Split(log, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func containsWord(words []string, w string) bool {
	for _, x := range words {
		if x == w {
			return true
		}
	}
	return false
}
