package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Behavioural pins for the install functions' refusal guards (PR #747 T06, T10, T18). Every digest
// is the host's real sha256sum over fixture bytes; a positive path trusts a fixture by overriding
// the digest variable with the fixture's own digest, so no test here knows a pin (D-61).

type igArch struct{ unameM, goarch, suffix, platform string }

var igArches = []igArch{{"aarch64", "arm64", "ARM64", "linux-arm64"}, {"x86_64", "amd64", "AMD64", "linux-x64"}}

func igSha256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// igGnuSha256sum and igGnuFind give the host's own sha256sum and find the GNU-only options the
// Linux install path calls (`sha256sum --check --strict` on stdin, `find -printf '%P\n'`), which
// macOS's BSD tools reject. Any other shape exits 2, so an unemulated call can never pass.
const igGnuSha256sum = `case "$*" in
"") exec @HOST@ ;;
"--check --strict")
	n=0; rc=0
	while read -r want f; do
		n=$((n + 1)); got=""
		[ -f "$f" ] && got="$(@HOST@ <"$f" | cut -d' ' -f1)"
		if [ -n "$got" ] && [ "$got" = "$want" ]; then echo "$f: OK"; else echo "$f: FAILED"; rc=1; fi
	done
	[ "$n" -gt 0 ] || rc=1
	exit "$rc" ;;
*) echo "sha256sum stub: unemulated arguments: $*" >&2; exit 2 ;;
esac
`

const igGnuFind = `fmt=""; n=$#
while [ "$n" -gt 0 ]; do
	a="$1"; shift; n=$((n - 1))
	if [ "$a" = -printf ]; then fmt="$1"; shift; n=$((n - 1)); continue; fi
	set -- "$@" "$a"
done
case "$fmt" in
"") exec @HOST@ "$@" ;;
'%P\n')
	[ "$1" = . ] || { echo "find stub: -printf %P is emulated only from ." >&2; exit 2; }
	out="$(@HOST@ "$@" -print)" || exit
	[ -z "$out" ] || printf '%s\n' "$out" | sed 's|^\./||' ;;
*) echo "find stub: unemulated -printf $fmt" >&2; exit 2 ;;
esac
`

func igSandbox(t *testing.T) *cvSandbox {
	t.Helper()
	s := newCvSandbox(t)
	s.stub("sha256sum", strings.ReplaceAll(igGnuSha256sum, "@HOST@", igHostTool(t, "sha256sum")))
	s.stub("find", strings.ReplaceAll(igGnuFind, "@HOST@", igHostTool(t, "find")))
	return s
}

// igHostTool is called by its own path, never relinked under another name: macOS sha256sum is
// md5(1), which picks its digest from the name it was invoked as.
func igHostTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("resolving real %s: %v", name, err)
	}
	return "'" + p + "'"
}

func igTrust(harness, name, digest string) string {
	return harness + name + "='" + digest + "'\n"
}

func igWantLine(t *testing.T, out, line string) {
	t.Helper()
	if !cvHasLine(out, line) {
		t.Errorf("stdout lacks %q\n%s", line, out)
	}
}

func igWantAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s exists; a refused build must leave nothing behind", p)
		}
	}
}

func igWantNoLogLine(t *testing.T, log, prefix, why string) {
	t.Helper()
	if n := cvCountLines(log, prefix); n != 0 {
		t.Errorf("%s: %d %q call(s)\n%s", why, n, prefix, log)
	}
}

// ---------------------------------------------------------------------------------------------
// Claude Code: three channels (direct release download, official installer, npm last resort).

const igClaudeCurlStub = `echo "curl $*" >>"$CV_LOG"
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
	[ -n "${IG_DIRECT_PAYLOAD-}" ] && [ -n "$out" ] || exit 22
	cat "$IG_DIRECT_PAYLOAD" >"$out" ;;
https://claude.ai/install.sh)
	[ -n "${IG_INSTALLER-}" ] || exit 22
	printf '#!/bin/bash\necho "official installer"\n' ;;
*) exit 22 ;;
esac
`

// igInstallerStub plays the official installer the way the real one behaves: it re-points
// ~/.local/bin/claude at the build it laid down BEFORE quickstart verifies anything.
// IG_INSTALLER=ok exits 0; any other value re-points and then fails.
const igInstallerStub = `echo "bash $*" >>"$CV_LOG"
cat >/dev/null
[ -n "${IG_INSTALLER-}" ] || exit 1
for v; do :; done
v="${IG_INSTALLER_VERSION:-$v}"
dir="$HOME/.local/share/claude/versions"
mkdir -p "$dir" "$HOME/.local/bin"
cat "$IG_INSTALLER_PAYLOAD" >"$dir/$v"
chmod 0755 "$dir/$v"
ln -sfn "$dir/$v" "$HOME/.local/bin/claude"
[ "$IG_INSTALLER" = ok ]
`

const igClaudeNpmStub = `echo "npm $*" >>"$CV_LOG"
echo "npm sees ~/.local/bin/claude -> $(readlink "$HOME/.local/bin/claude" || echo none)" >>"$CV_LOG"
case "$1" in
root) echo "$HOME/npmroot" ;;
prefix) echo "$HOME/npmprefix" ;;
*) exit 1 ;;
esac
`

// igNpmRealLayout replaces _npm_install_pinned with the layout claude-code's own postinstall leaves
// (install.cjs placeBinary): the verified platform binary is HARD-LINKED over the package's
// bin/claude.exe stub, and npm's bin link points at bin/claude.exe.
const igNpmRealLayout = `_npm_install_pinned() {
    echo "NPM_INSTALL $1@$2 $4" >>"$CV_LOG"
    local pkg="$HOME/npmroot/$1" plat
    plat="$pkg/node_modules/$4"
    mkdir -p "$plat" "$pkg/bin" "$HOME/npmprefix/bin"
    cat "$IG_NPM_PAYLOAD" >"$plat/claude"
    chmod 0755 "$plat/claude"
    printf '#!/bin/sh\necho "platform binary not placed" >&2\nexit 1\n' >"$pkg/bin/claude.exe"
    ln -f "$plat/claude" "$pkg/bin/claude.exe"
    ln -sfn "$pkg/bin/claude.exe" "$HOME/npmprefix/bin/claude"
}
`

type igClaude struct {
	*cvSandbox
	harness, pin, versions, link, prev string
}

// igClaudeSandbox lays down a working-but-stale 2.1.280 behind ~/.local/bin/claude when withPrev.
func igClaudeSandbox(t *testing.T, withPrev bool) *igClaude {
	t.Helper()
	s := igSandbox(t)
	s.stub("uname", `case "$1" in -s) echo Linux ;; -m) echo "$CV_UNAME_M" ;; *) echo Linux ;; esac`+"\n")
	s.stub("curl", igClaudeCurlStub)
	s.stub("bash", igInstallerStub)
	s.stub("npm", igClaudeNpmStub)
	if err := os.MkdirAll(filepath.Join(s.home, "npmroot"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &igClaude{
		cvSandbox: s,
		harness: convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error", "log_step",
			"command_exists", "_arch", "_record_row", "_quarantine_stale", "_converge", "_npm_install_pinned",
			"install_claude"),
		pin:      quickstartPin(t, "CLAUDE_CODE_VERSION"),
		versions: filepath.Join(s.home, ".local/share/claude/versions"),
		link:     filepath.Join(s.home, ".local/bin/claude"),
	}
	if withPrev {
		c.prev = s.writeFile(filepath.Join(c.versions, "2.1.280"), "#!/bin/sh\necho \"2.1.280 (Claude Code)\"\n", 0o755)
		if err := os.MkdirAll(filepath.Dir(c.link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(c.prev, c.link); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// build writes a claude build answering --version with version; running it leaves $HOME/RAN.
func (c *igClaude) build(name, version string, extra ...string) (string, []byte) {
	b := "#!/bin/sh\n: >\"$HOME/RAN\"\necho \"" + version + " (Claude Code)\"\n"
	for _, l := range extra {
		b += "# " + l + "\n"
	}
	return c.writeFile(filepath.Join(c.base, name), b, 0o644), []byte(b)
}

func (c *igClaude) trust(a igArch, build []byte) {
	c.harness = igTrust(c.harness, "CLAUDE_CODE_SHA256_"+a.suffix, igSha256(build))
}

func (c *igClaude) run(a igArch, env ...string) cvRun {
	c.t.Helper()
	return c.cvSandbox.run(c.harness, "install_claude", append([]string{"CV_UNAME_M=" + a.unameM}, env...)...)
}

func (c *igClaude) wantRefused(t *testing.T, r cvRun, installed string) {
	t.Helper()
	if r.rc != 0 {
		t.Errorf("install_claude rc = %d, want 0 (the row and the ledger carry the outcome)\n%s", r.rc, r.out)
	}
	if r.ledger != "claude\n" {
		t.Errorf("CONVERGE_FAILURES = %q, want \"claude\\n\"\n%s", r.ledger, r.out)
	}
	cvExpectRow(t, r.rows, "claude", c.pin, installed, "FAILED")
}

func (c *igClaude) wantConverged(t *testing.T, r cvRun) {
	t.Helper()
	if r.rc != 0 || r.ledger != "" {
		t.Errorf("rc = %d, ledger %q; want 0 and empty\n%s\n%s", r.rc, r.ledger, r.out, r.log)
	}
	cvExpectRow(t, r.rows, "claude", c.pin, c.pin, "converged")
}

func (c *igClaude) wantLinkTo(t *testing.T, want string) {
	t.Helper()
	got, err := filepath.EvalSymlinks(c.link)
	if err != nil || got != mustEval(want) {
		t.Errorf("~/.local/bin/claude resolves to %q (%v), want %q", got, err, mustEval(want))
	}
}

func (c *igClaude) wantNotRan(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(c.home, "RAN")); err == nil {
		t.Error("an unverified or refused claude build was executed")
	}
}

func (c *igClaude) emsignRefusal() string {
	return "[ERROR] claude " + c.pin + " embeds 'emSign ECC Root CA - G3' (1 matches) — refusing to accept this build; bump CLAUDE_CODE_VERSION"
}

// wantNpmSaw requires the npm last resort to have run, and to have found ~/.local/bin/claude already
// pointing at want ("none" = no link at all) every time it looked.
func (c *igClaude) wantNpmSaw(t *testing.T, log, want string) {
	t.Helper()
	if !strings.Contains(log, "npm pack @anthropic-ai/claude-code@"+c.pin) {
		t.Errorf("the npm last resort was not attempted after the installer was rejected\n%s", log)
	}
	n := 0
	for _, l := range strings.Split(log, "\n") {
		if seen, ok := strings.CutPrefix(l, "npm sees ~/.local/bin/claude -> "); ok {
			n++
			if seen != want {
				t.Errorf("npm ran while ~/.local/bin/claude -> %s; the rejected installer build must be unlinked first (want %s)", seen, want)
			}
		}
	}
	if n == 0 {
		t.Errorf("npm never looked at ~/.local/bin/claude\n%s", log)
	}
}

// TestQuickstartInstallerGuardsClaudeRestoresLink pins T06: the official installer re-points
// ~/.local/bin/claude before quickstart verifies its build, so every rejection of that build must put
// the previous link back (or remove a link that would dangle) before the npm last resort runs.
func TestQuickstartInstallerGuardsClaudeRestoresLink(t *testing.T) {
	bodyProbe := quickstartPin(t, "EMSIGN_G3_BODY_PROBE")
	for _, a := range igArches {
		a := a
		t.Run("installer build failing sha256 restores the previous link/"+a.unameM, func(t *testing.T) {
			c := igClaudeSandbox(t, true)
			build, _ := c.build("installer.bin", c.pin)
			r := c.run(a, "IG_INSTALLER=ok", "IG_INSTALLER_PAYLOAD="+build)
			c.wantRefused(t, r, "2.1.280")
			igWantLine(t, r.out, "[ERROR] claude "+c.pin+": installed binary sha256 does not match CLAUDE_CODE_SHA256_"+a.suffix+" — removed")
			igWantAbsent(t, filepath.Join(c.versions, c.pin))
			c.wantLinkTo(t, c.prev)
			c.wantNpmSaw(t, r.log, c.prev)
		})

		for _, f := range []struct{ name, line string }{
			{"the emSign label", "emSign ECC Root CA - G3"},
			{"only the emSign body", bodyProbe},
		} {
			f := f
			t.Run("installer build carrying "+f.name+" restores the previous link/"+a.unameM, func(t *testing.T) {
				c := igClaudeSandbox(t, true)
				build, bytes := c.build("installer.bin", c.pin, f.line)
				c.trust(a, bytes)
				r := c.run(a, "IG_INSTALLER=ok", "IG_INSTALLER_PAYLOAD="+build)
				c.wantRefused(t, r, "2.1.280")
				igWantLine(t, r.out, c.emsignRefusal())
				igWantAbsent(t, filepath.Join(c.versions, c.pin))
				c.wantLinkTo(t, c.prev)
				c.wantNotRan(t)
				igWantNoLogLine(t, r.log, "npm ", "an emSign refusal is final; no other channel may be tried")
			})
		}

		t.Run("installer laying down another version restores the previous link/"+a.unameM, func(t *testing.T) {
			c := igClaudeSandbox(t, true)
			build, _ := c.build("installer.bin", "2.1.999")
			r := c.run(a, "IG_INSTALLER=ok", "IG_INSTALLER_VERSION=2.1.999", "IG_INSTALLER_PAYLOAD="+build)
			c.wantRefused(t, r, "2.1.280")
			igWantLine(t, r.out, "[ERROR] claude "+c.pin+": the official installer did not lay down versions/"+c.pin+
				" (~/.local/bin/claude -> "+mustEval(filepath.Join(c.versions, "2.1.999"))+")")
			c.wantLinkTo(t, c.prev)
			c.wantNotRan(t)
			c.wantNpmSaw(t, r.log, c.prev)
		})

		t.Run("installer failing after it re-pointed the link restores it/"+a.unameM, func(t *testing.T) {
			c := igClaudeSandbox(t, true)
			build, _ := c.build("installer.bin", c.pin)
			r := c.run(a, "IG_INSTALLER=fail", "IG_INSTALLER_PAYLOAD="+build)
			c.wantRefused(t, r, "2.1.280")
			c.wantLinkTo(t, c.prev)
			c.wantNotRan(t)
			c.wantNpmSaw(t, r.log, c.prev)
		})

		// An earlier npm-channel run left ~/.local/bin/claude -> npm's bin link -> bin/claude.exe; only
		// the one-hop target restores that chain as it was.
		t.Run("a previous npm-chain link is restored verbatim/"+a.unameM, func(t *testing.T) {
			c := igClaudeSandbox(t, false)
			exe := c.writeFile("npmroot/@anthropic-ai/claude-code/bin/claude.exe", "#!/bin/sh\necho \"2.1.280 (Claude Code)\"\n", 0o755)
			c.prev = filepath.Join(c.home, "npmprefix/bin/claude")
			for _, l := range [][2]string{{exe, c.prev}, {c.prev, c.link}} {
				if err := os.MkdirAll(filepath.Dir(l[1]), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(l[0], l[1]); err != nil {
					t.Fatal(err)
				}
			}
			build, _ := c.build("installer.bin", c.pin)
			r := c.run(a, "IG_INSTALLER=ok", "IG_INSTALLER_PAYLOAD="+build)
			c.wantRefused(t, r, "2.1.280")
			if got, _ := os.Readlink(c.link); got != c.prev {
				t.Errorf("~/.local/bin/claude -> %q; the previous one-hop target %q must come back verbatim", got, c.prev)
			}
			c.wantNpmSaw(t, r.log, c.prev)
		})

		// A relative target resolves against the link's own directory, never the cwd quickstart runs in.
		t.Run("a relative previous link is restored as written/"+a.unameM, func(t *testing.T) {
			c := igClaudeSandbox(t, true)
			c.prev = "../share/claude/versions/2.1.280"
			if err := os.Remove(c.link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(c.prev, c.link); err != nil {
				t.Fatal(err)
			}
			build, _ := c.build("installer.bin", c.pin)
			r := c.run(a, "IG_INSTALLER=ok", "IG_INSTALLER_PAYLOAD="+build)
			c.wantRefused(t, r, "2.1.280")
			if got, _ := os.Readlink(c.link); got != c.prev {
				t.Errorf("~/.local/bin/claude -> %q; the working relative link %q must come back as written, not be dropped", got, c.prev)
			}
			c.wantNpmSaw(t, r.log, c.prev)
		})

		for _, p := range []struct{ name, setup string }{
			{"no previous link", ""},
			{"a previous regular file", "#!/bin/sh\necho \"2.1.280 (Claude Code)\"\n"},
		} {
			p := p
			t.Run(p.name+": a rejected installer build leaves no dangling link/"+a.unameM, func(t *testing.T) {
				c := igClaudeSandbox(t, false)
				if p.setup != "" {
					c.writeFile(".local/bin/claude", p.setup, 0o755)
				}
				build, _ := c.build("installer.bin", c.pin)
				r := c.run(a, "IG_INSTALLER=ok", "IG_INSTALLER_PAYLOAD="+build)
				c.wantRefused(t, r, "absent")
				igWantLine(t, r.out, "[ERROR] claude "+c.pin+": installed binary sha256 does not match CLAUDE_CODE_SHA256_"+a.suffix+" — removed")
				if _, err := os.Lstat(c.link); !os.IsNotExist(err) {
					target, _ := os.Readlink(c.link)
					t.Errorf("~/.local/bin/claude is left behind (-> %q, lstat err %v); with no restorable previous build it must not exist", target, err)
				}
				c.wantNpmSaw(t, r.log, "none")
			})
		}
	}
}

// TestQuickstartInstallerGuardsClaudeChannels pins T10's emSign guard on the direct and npm channels
// with a label-only and a body-only fixture each (either operand alone must refuse), and T18's
// verified premise: on npm's real layout the check already reads the hard-linked platform binary.
// The installer channel's refusals are pinned by TestQuickstartInstallerGuardsClaudeRestoresLink.
func TestQuickstartInstallerGuardsClaudeChannels(t *testing.T) {
	fixtures := []struct{ name, line string }{
		{"the emSign label", "emSign ECC Root CA - G3"},
		{"only the emSign body", quickstartPin(t, "EMSIGN_G3_BODY_PROBE")},
	}
	for _, a := range igArches {
		a := a
		t.Run("direct download on its sha256 converges/"+a.unameM, func(t *testing.T) {
			c := igClaudeSandbox(t, true)
			build, bytes := c.build("direct.bin", c.pin)
			c.trust(a, bytes)
			r := c.run(a, "IG_DIRECT_PAYLOAD="+build)
			c.wantConverged(t, r)
			c.wantLinkTo(t, filepath.Join(c.versions, c.pin))
			igWantNoLogLine(t, r.log, "bash ", "the official installer ran although the direct download verified")
		})

		for _, f := range fixtures {
			f := f
			t.Run("direct download carrying "+f.name+" is refused/"+a.unameM, func(t *testing.T) {
				c := igClaudeSandbox(t, true)
				build, bytes := c.build("direct.bin", c.pin, f.line)
				c.trust(a, bytes)
				r := c.run(a, "IG_DIRECT_PAYLOAD="+build)
				c.wantRefused(t, r, "2.1.280")
				igWantLine(t, r.out, c.emsignRefusal())
				igWantAbsent(t, filepath.Join(c.versions, c.pin), filepath.Join(c.versions, c.pin+".partial"))
				c.wantLinkTo(t, c.prev)
				c.wantNotRan(t)
				igWantNoLogLine(t, r.log, "bash ", "an emSign refusal is final; the installer may not be tried")
				igWantNoLogLine(t, r.log, "npm ", "an emSign refusal is final; npm may not be tried")
			})
		}

		t.Run("installer build on its sha256 converges/"+a.unameM, func(t *testing.T) {
			c := igClaudeSandbox(t, true)
			build, bytes := c.build("installer.bin", c.pin)
			c.trust(a, bytes)
			r := c.run(a, "IG_INSTALLER=ok", "IG_INSTALLER_PAYLOAD="+build)
			c.wantConverged(t, r)
			c.wantLinkTo(t, filepath.Join(c.versions, c.pin))
			igWantNoLogLine(t, r.log, "npm ", "npm ran although the installer build verified")
		})

		npm := func(t *testing.T, extra ...string) (*igClaude, cvRun) {
			c := igClaudeSandbox(t, true)
			c.harness += igNpmRealLayout
			build, _ := c.build("platform.bin", c.pin, extra...)
			r := c.run(a, "IG_NPM_PAYLOAD="+build)
			pkg := filepath.Join(c.home, "npmroot/@anthropic-ai/claude-code")
			plat, err1 := os.Stat(filepath.Join(pkg, "node_modules/@anthropic-ai/claude-code-"+a.platform, "claude"))
			exe, err2 := os.Stat(filepath.Join(pkg, "bin/claude.exe"))
			if err1 != nil || err2 != nil || !os.SameFile(plat, exe) {
				t.Fatalf("fixture: bin/claude.exe must be the platform binary's hard link (%v, %v)\n%s\n%s", err1, err2, r.out, r.log)
			}
			return c, r
		}

		t.Run("npm real layout with a clean platform binary converges/"+a.unameM, func(t *testing.T) {
			c, r := npm(t)
			c.wantConverged(t, r)
			if got, _ := os.Readlink(c.link); got != filepath.Join(c.home, "npmprefix/bin/claude") {
				t.Errorf("~/.local/bin/claude -> %q, want npm's bin link", got)
			}
		})

		for _, f := range fixtures {
			f := f
			t.Run("npm real layout: platform binary carrying "+f.name+" is refused/"+a.unameM, func(t *testing.T) {
				c, r := npm(t, f.line)
				c.wantRefused(t, r, "2.1.280")
				igWantLine(t, r.out, c.emsignRefusal())
				if got, _ := os.Readlink(c.link); got != c.prev {
					t.Errorf("~/.local/bin/claude -> %q after an npm emSign refusal; it must still point at %q", got, c.prev)
				}
				c.wantNotRan(t)
			})
		}
	}
}

// ---------------------------------------------------------------------------------------------
// gh and Go release tarballs: wrong bytes are never unpacked.

const igDownloadCurlStub = `echo "curl $*" >>"$CV_LOG"
out=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out="$2"; shift 2 ;;
	*) shift ;;
	esac
done
[ -n "$out" ] && [ -n "${IG_PAYLOAD-}" ] || exit 22
cat "$IG_PAYLOAD" >"$out"
`

// igTarStub unpacks any tarball as a release whose single binary answers --version/version with
// $IG_VERSION: <tarball name>/bin/gh for gh, go/bin/go for Go.
const igTarStub = `echo "tar $*" >>"$CV_LOG"
dir=""; f=""
while [ $# -gt 0 ]; do
	case "$1" in
	-C) dir="$2"; shift 2 ;;
	-xzf) f="$2"; shift 2 ;;
	*) shift ;;
	esac
done
n="${f##*/}"; n="${n%.tar.gz}"
case "$n" in
gh_*) mkdir -p "$dir/$n/bin"; printf '#!/bin/sh\necho "gh version %s (2026-01-01)"\n' "$IG_VERSION" >"$dir/$n/bin/gh" ;;
go*) mkdir -p "$dir/go/bin"; printf '#!/bin/sh\necho "go version %s linux/arm64"\n' "$IG_VERSION" >"$dir/go/bin/go"; chmod 0755 "$dir/go/bin/go" ;;
*) exit 2 ;;
esac
`

func igReleaseSandbox(t *testing.T, fns ...string) (*cvSandbox, string, string) {
	t.Helper()
	s := igSandbox(t)
	s.stub("uname", `case "$1" in -s) echo Linux ;; -m) echo "$CV_UNAME_M" ;; *) echo Linux ;; esac`+"\n")
	s.stub("curl", igDownloadCurlStub)
	s.stub("tar", igTarStub)
	harness := convergeHarness(t, s.src, append([]string{"log_info", "log_success", "log_warn", "log_error",
		"log_step", "command_exists", "_arch", "_record_row", "_quarantine_stale", "_converge"}, fns...)...)
	payload := s.writeFile(filepath.Join(s.base, "release.tar.gz"), "release tarball fixture\n", 0o644)
	return s, harness, payload
}

func TestQuickstartInstallerGuardsGh(t *testing.T) {
	pin := quickstartPin(t, "GH_VERSION")
	setup := func(t *testing.T) (*cvSandbox, string, string) {
		s, harness, payload := igReleaseSandbox(t, "install_gh")
		s.stub("install", `echo "install $*" >>"$CV_LOG"; [ "$1" = -m ] || exit 1; cat "$3" >"$4" && chmod "$2" "$4"`+"\n")
		s.stub("git", `echo "git $*" >>"$CV_LOG"; exit 1`+"\n")
		return s, harness, payload
	}
	for _, a := range igArches {
		a := a
		tarball := "gh_" + pin + "_linux_" + a.goarch + ".tar.gz"

		t.Run("a tarball off its pinned sha256 is never unpacked/"+a.unameM, func(t *testing.T) {
			s, harness, payload := setup(t)
			r := s.run(harness, "install_gh", "CV_UNAME_M="+a.unameM, "IG_PAYLOAD="+payload, "IG_VERSION="+pin)
			if r.rc != 0 {
				t.Errorf("install_gh rc = %d, want 0 (the row and the ledger carry the outcome)\n%s", r.rc, r.out)
			}
			if !strings.Contains(r.log, "/v"+pin+"/"+tarball) {
				t.Errorf("the pinned release tarball was never downloaded\n%s", r.log)
			}
			igWantLine(t, r.out, "[ERROR] gh "+pin+": "+tarball+" does not match its pinned sha256 — NOT installing")
			igWantNoLogLine(t, r.log, "tar ", "an unverified tarball was unpacked")
			igWantNoLogLine(t, r.log, "install ", "an unverified gh was installed")
			igWantAbsent(t, filepath.Join(s.home, ".local/bin/gh"), filepath.Join(s.home, ".local/bin/gh.new"))
			if r.ledger != "gh\n" {
				t.Errorf("CONVERGE_FAILURES = %q, want \"gh\\n\"", r.ledger)
			}
			cvExpectRow(t, r.rows, "gh", pin, "absent", "FAILED")
		})

		t.Run("a tarball on its sha256 is unpacked and converges/"+a.unameM, func(t *testing.T) {
			s, harness, payload := setup(t)
			harness = igTrust(harness, "GH_SHA256_"+a.suffix, igSha256([]byte("release tarball fixture\n")))
			r := s.run(harness, "install_gh", "CV_UNAME_M="+a.unameM, "IG_PAYLOAD="+payload, "IG_VERSION="+pin)
			if r.rc != 0 || r.ledger != "" {
				t.Errorf("rc = %d, ledger %q; want 0 and empty\n%s\n%s", r.rc, r.ledger, r.out, r.log)
			}
			cvExpectRow(t, r.rows, "gh", pin, pin, "converged")
		})
	}
}

func TestQuickstartInstallerGuardsGo(t *testing.T) {
	pin := quickstartPin(t, "GO_TOOLCHAIN_VERSION")
	// install_af's route to this call is pinned by cvExpectedRoute; driving install_af whole would
	// also build af.
	const call = `_converge go "$GO_TOOLCHAIN_VERSION" _probe_go _install_go required`
	for _, a := range igArches {
		a := a
		tarball := "go" + strings.TrimPrefix(pin, "go") + ".linux-" + a.goarch + ".tar.gz"

		t.Run("a Go tarball off its pinned sha256 is never unpacked/"+a.unameM, func(t *testing.T) {
			s, harness, payload := igReleaseSandbox(t, "_probe_go", "_install_go")
			r := s.run(harness, call, "CV_UNAME_M="+a.unameM, "IG_PAYLOAD="+payload, "IG_VERSION="+pin)
			if r.rc != 1 {
				t.Errorf("_converge go rc = %d, want 1 (a failed required install)\n%s", r.rc, r.out)
			}
			if !strings.Contains(r.log, "https://go.dev/dl/"+tarball) {
				t.Errorf("the pinned Go tarball was never downloaded\n%s", r.log)
			}
			igWantLine(t, r.out, "[ERROR] go toolchain "+pin+": "+tarball+" does not match its pinned sha256 — NOT installing")
			igWantNoLogLine(t, r.log, "tar ", "an unverified Go tarball was unpacked")
			igWantAbsent(t, filepath.Join(s.home, ".local/share/agentfactory/go"), filepath.Join(s.home, ".local/bin/go"))
			if r.ledger != "go\n" {
				t.Errorf("CONVERGE_FAILURES = %q, want \"go\\n\"", r.ledger)
			}
			cvExpectRow(t, r.rows, "go", pin, "absent", "FAILED")
		})

		t.Run("a Go tarball on its sha256 is unpacked and converges/"+a.unameM, func(t *testing.T) {
			s, harness, payload := igReleaseSandbox(t, "_probe_go", "_install_go")
			harness = igTrust(harness, "GO_SHA256_"+a.suffix, igSha256([]byte("release tarball fixture\n")))
			r := s.run(harness, call, "CV_UNAME_M="+a.unameM, "IG_PAYLOAD="+payload, "IG_VERSION="+pin)
			if r.rc != 0 || r.ledger != "" {
				t.Errorf("rc = %d, ledger %q; want 0 and empty\n%s\n%s", r.rc, r.ledger, r.out, r.log)
			}
			cvExpectRow(t, r.rows, "go", pin, pin, "converged")
			if got, _ := filepath.EvalSymlinks(filepath.Join(s.home, ".local/bin/go")); got != mustEval(filepath.Join(s.home, ".local/share/agentfactory/go/bin/go")) {
				t.Errorf("~/.local/bin/go resolves to %q, want the prefix toolchain", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// Playwright: the browser trees the CLI downloads are held to their pinned digests.

const igPlaywrightStub = `echo "playwright $*" >>"$CV_LOG"
case "$*" in
--version) if [ -f "$HOME/.pw-installed" ]; then echo "Version $IG_VERSION"; else echo "Version 1.50.0"; fi ;;
"install --dry-run chromium")
	for d in chromium-1300 chromium_headless_shell-1300; do echo "  Install location:    $HOME/.cache/ms-playwright/$d"; done ;;
"install chromium"|"install --with-deps chromium") : >"$HOME/.pw-installed" ;;
*) exit 1 ;;
esac
`

// igTreeDigest derives pin-data.md's tree digest independently of the script: one `F <sha256> <path>`
// or `L <target> <path>` line per entry, in byte order of path.
func igTreeDigest(t *testing.T, root string) string {
	t.Helper()
	lines := map[string]string{}
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		paths = append(paths, rel)
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			lines[rel] = "L " + target + " " + rel + "\n"
			return err
		}
		b, err := os.ReadFile(p)
		lines[rel] = "F " + igSha256(b) + " " + rel + "\n"
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var all strings.Builder
	for _, p := range paths {
		all.WriteString(lines[p])
	}
	return igSha256([]byte(all.String()))
}

var igPwLogPath = regexp.MustCompile(`["'= ]/tmp/`)

// TestQuickstartInstallerGuardsPlaywright: subtest names carry no spaces because they reach $HOME
// through t.TempDir, and _install_playwright word-splits the install locations.
func TestQuickstartInstallerGuardsPlaywright(t *testing.T) {
	pin := quickstartPin(t, "PLAYWRIGHT_VERSION")
	setup := func(t *testing.T) (*cvSandbox, string, string, string) {
		s := igSandbox(t)
		s.stub("uname", `case "$1" in -s) echo Linux ;; -m) echo "$CV_UNAME_M" ;; *) echo Linux ;; esac`+"\n")
		s.stub("npm", `echo "npm $*" >>"$CV_LOG"; exit 1`+"\n")
		s.stub("sudo", `echo "sudo $*" >>"$CV_LOG"; exit 1`+"\n")
		s.stub("playwright", igPlaywrightStub)
		harness := convergeHarness(t, s.src, "log_info", "log_success", "log_warn", "log_error", "log_step",
			"command_exists", "_arch", "_record_row", "_quarantine_stale", "_converge", "_probe_playwright",
			"_playwright_tree_digest", "_install_playwright")
		// The install log path is hard-coded under the host's /tmp (out of scope for this PR); the
		// hermetic run keeps it in the sandbox's TMPDIR.
		harness = strings.Replace(harness, `"/tmp/af-playwright-install.log"`, `"$TMPDIR/af-playwright-install.log"`, 1)
		if loc := igPwLogPath.FindString(harness); loc != "" {
			t.Fatalf("the playwright harness still writes under the host's /tmp (%q)", loc)
		}
		harness += `_npm_install_pinned() { echo "NPM_INSTALL $1@$2 $4" >>"$CV_LOG"; }` + "\n"
		cache := filepath.Join(s.home, ".cache/ms-playwright")
		s.writeFile(filepath.Join(cache, "chromium-1300/chrome-linux/chrome"), "chromium build\n", 0o755)
		s.writeFile(filepath.Join(cache, "chromium-1300/chrome-linux/locales/en-US.pak"), "locale\n", 0o644)
		if err := os.Symlink("chrome", filepath.Join(cache, "chromium-1300/chrome-linux/chrome-wrapper")); err != nil {
			t.Fatal(err)
		}
		s.writeFile(filepath.Join(cache, "chromium_headless_shell-1300/chrome-headless-shell-linux/chrome-headless-shell"), "headless shell build\n", 0o755)
		return s, harness,
			igTreeDigest(t, filepath.Join(cache, "chromium-1300/chrome-linux")),
			igTreeDigest(t, filepath.Join(cache, "chromium_headless_shell-1300/chrome-headless-shell-linux"))
	}
	const call = `_converge playwright "$PLAYWRIGHT_VERSION" _probe_playwright _install_playwright optional`
	wantQuarantined := func(t *testing.T, s *cvSandbox) {
		t.Helper()
		for _, d := range []string{"chromium-1300", "chromium_headless_shell-1300"} {
			cache := filepath.Join(s.home, ".cache/ms-playwright")
			stale, _ := filepath.Glob(filepath.Join(cache, d+".stale-*"))
			if _, err := os.Stat(filepath.Join(cache, d)); err == nil || len(stale) != 1 {
				t.Errorf("%s must be quarantined as %s.stale-<ts> after a refused tree; stale copies %v", d, d, stale)
			}
		}
	}
	wantSkipped := func(t *testing.T, r cvRun) {
		t.Helper()
		if r.rc != 0 || r.ledger != "" {
			t.Errorf("rc = %d, ledger %q; want 0 and EMPTY (playwright is optional)\n%s", r.rc, r.ledger, r.out)
		}
		got := cvRowsNamed(r.rows, "playwright")
		if len(got) != 1 || got[0].pin != pin || got[0].result != "skipped-optional" {
			t.Errorf("want one playwright row, pin %q, result skipped-optional; got %+v", pin, got)
		}
	}
	for _, a := range igArches {
		a := a
		chromiumVar := "PLAYWRIGHT_CHROMIUM_TREE_SHA256_" + a.suffix
		shellVar := "PLAYWRIGHT_HEADLESS_SHELL_TREE_SHA256_" + a.suffix

		t.Run("trees-on-their-digests-converge/"+a.unameM, func(t *testing.T) {
			s, harness, chromium, shell := setup(t)
			harness = igTrust(igTrust(harness, chromiumVar, chromium), shellVar, shell)
			r := s.run(harness, call, "CV_UNAME_M="+a.unameM, "IG_VERSION="+pin)
			if r.rc != 0 || r.ledger != "" {
				t.Errorf("rc = %d, ledger %q; want 0 and empty\n%s", r.rc, r.ledger, r.out)
			}
			cvExpectRow(t, r.rows, "playwright", pin, pin, "converged")
		})

		t.Run("chromium-tree-off-its-digest-is-refused/"+a.unameM, func(t *testing.T) {
			s, harness, chromium, shell := setup(t)
			harness = igTrust(harness, shellVar, shell)
			r := s.run(harness, call, "CV_UNAME_M="+a.unameM, "IG_VERSION="+pin)
			igWantLine(t, r.out, "[WARN] playwright: chromium-1300 tree digest "+chromium+" does not match the pinned "+quickstartPin(t, chromiumVar))
			wantSkipped(t, r)
			wantQuarantined(t, s)
		})

		t.Run("headless-shell-tree-off-its-digest-is-refused/"+a.unameM, func(t *testing.T) {
			s, harness, chromium, shell := setup(t)
			harness = igTrust(harness, chromiumVar, chromium)
			r := s.run(harness, call, "CV_UNAME_M="+a.unameM, "IG_VERSION="+pin)
			igWantLine(t, r.out, "[WARN] playwright: chromium_headless_shell-1300 tree digest "+shell+" does not match the pinned "+quickstartPin(t, shellVar))
			wantSkipped(t, r)
			wantQuarantined(t, s)
		})
	}
}
