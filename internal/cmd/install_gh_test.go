package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const installGhRel = ".github/install-gh.sh"

// ghReleaseTarball builds a release tarball in the cli/cli layout whose gh prints reported as its version.
func ghReleaseTarball(t *testing.T, version, arch, reported string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/bash\necho \"gh version " + reported + " (2099-01-01)\"\n")
	dir := "gh_" + version + "_linux_" + arch
	for _, h := range []*tar.Header{
		{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: dir + "/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: dir + "/bin/gh", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

type installGhRun struct {
	refRunResult
	runnerTemp, githubPath, fake string
}

func (r installGhRun) curlURLs(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.fake, "curl.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

func (r installGhRun) curlArgv(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.fake, "curl.argv"))
	if err != nil {
		t.Fatalf("curl was never run: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// quickstartGhURL expands quickstart.sh's own gh download URL, so the install script and the
// operator's installer are held to one URL rather than each to a copy of it.
func quickstartGhURL(t *testing.T, version, arch string) string {
	t.Helper()
	fn := topLevelShellFunction(readQuickstart(t), "install_gh")
	tarball := regexp.MustCompile(`tarball="([^"]+)"`).FindStringSubmatch(fn)
	url := regexp.MustCompile(`curl [^"\n]*"(https://[^"]+/\$\{tarball\})"`).FindStringSubmatch(fn)
	if tarball == nil || url == nil {
		t.Fatal("quickstart.sh's install_gh has no gh download URL")
	}
	pins := strings.NewReplacer("${GH_VERSION}", version, "${arch}", arch)
	got := strings.Replace(pins.Replace(url[1]), "${tarball}", pins.Replace(tarball[1]), 1)
	if strings.Contains(got, "$") {
		t.Fatalf("quickstart.sh's gh URL %q expands to %q, which still names a variable", url[1], got)
	}
	return got
}

func (r installGhRun) githubPathContent(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(r.githubPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

// runInstallGh runs the real .github/install-gh.sh from a tree whose quickstart.sh carries pins, with
// curl serving the fixture tarballs in fake/ and uname reporting machine.
func runInstallGh(t *testing.T, pins, machine string, tarballs map[string][]byte, dropEnv ...string) installGhRun {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(findModuleRoot(t), installGhRel))
	if err != nil {
		t.Fatalf("read %s: %v", installGhRel, err)
	}
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(tree, installGhRel)
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "quickstart.sh"), []byte("#!/bin/bash\n"+pins+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fake := t.TempDir()
	for name, body := range tarballs {
		if err := os.WriteFile(filepath.Join(fake, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := refFakeBin(t, map[string]string{
		"curl": `
printf '%s\n' "$*" >> "$FAKE/curl.argv"
out="" url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2; continue ;;
    https://*) url="$1" ;;
  esac
  shift
done
printf '%s\n' "$url" >> "$FAKE/curl.log"
[ -e "$FAKE/${url##*/}" ] || exit 22
cp "$FAKE/${url##*/}" "$out"
`,
		"uname": `case "$1" in -m) echo "$FAKE_MACHINE" ;; *) echo Linux ;; esac` + "\n",
	})
	home := t.TempDir()
	r := installGhRun{runnerTemp: filepath.Join(home, "runner-temp"), githubPath: filepath.Join(home, "github_path"), fake: fake}
	if err := os.MkdirAll(r.runnerTemp, 0o755); err != nil {
		t.Fatal(err)
	}
	extra := []string{"FAKE=" + fake, "FAKE_MACHINE=" + machine}
	for _, kv := range []string{"RUNNER_TEMP=" + r.runnerTemp, "GITHUB_PATH=" + r.githubPath} {
		keep := true
		for _, d := range dropEnv {
			if strings.HasPrefix(kv, d+"=") {
				keep = false
			}
		}
		if keep {
			extra = append(extra, kv)
		}
	}
	r.refRunResult = refRun(t, tree, refEnv(home, bin, extra...), "", script)
	return r
}

func TestInstallGh(t *testing.T) {
	const v = "2.99.1"
	arm, armSum := ghReleaseTarball(t, v, "arm64", v)
	amd, amdSum := ghReleaseTarball(t, v, "amd64", v)
	tarballs := map[string][]byte{"gh_" + v + "_linux_arm64.tar.gz": arm, "gh_" + v + "_linux_amd64.tar.gz": amd}
	pins := func(version, armSha, amdSha string) string {
		return `GH_VERSION="` + version + `"` + "\n" + `GH_SHA256_ARM64="` + armSha + `"` + "\n" + `GH_SHA256_AMD64="` + amdSha + `"`
	}
	good := pins(v, armSum, amdSum)
	binDir := func(r installGhRun) string { return filepath.Join(r.runnerTemp, "gh-"+v, "bin") }
	assertNothingInstalled := func(t *testing.T, r installGhRun) {
		t.Helper()
		if r.exit == 0 {
			t.Errorf("exit 0, want non-zero\nstdout=%s\nstderr=%s", r.stdout, r.stderr)
		}
		if _, err := os.Stat(filepath.Join(binDir(r), "gh")); err == nil {
			t.Errorf("gh was installed at %s", binDir(r))
		}
		if got := r.githubPathContent(t); got != "" {
			t.Errorf("GITHUB_PATH = %q, want nothing appended", got)
		}
	}

	for _, c := range []struct{ machine, arch string }{{"aarch64", "arm64"}, {"arm64", "arm64"}, {"x86_64", "amd64"}, {"amd64", "amd64"}} {
		t.Run("installs the pinned release on "+c.machine, func(t *testing.T) {
			r := runInstallGh(t, good, c.machine, tarballs)
			if r.exit != 0 {
				t.Fatalf("exit %d\nstdout=%s\nstderr=%s", r.exit, r.stdout, r.stderr)
			}
			want := quickstartGhURL(t, v, c.arch)
			if got := r.curlURLs(t); !equalStrs(got, []string{want}) {
				t.Errorf("curl URLs = %q, want [%q] (quickstart's install_gh URL)", got, want)
			}
			for _, argv := range r.curlArgv(t) {
				for _, flag := range []string{"--connect-timeout", "--max-time"} {
					if !regexp.MustCompile(`(^| )` + flag + ` [1-9][0-9]*( |$)`).MatchString(argv) {
						t.Errorf("curl %s has no positive %s: a stalled download would hold the job until GitHub's six-hour limit", argv, flag)
					}
				}
			}
			info, err := os.Stat(filepath.Join(binDir(r), "gh"))
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Errorf("installed gh = %v, %v; want mode 0755 at %s", info, err, binDir(r))
			}
			if got := r.githubPathContent(t); got != binDir(r)+"\n" {
				t.Errorf("GITHUB_PATH = %q, want %q", got, binDir(r)+"\n")
			}
			for _, frag := range []string{v, c.arch, binDir(r)} {
				if !strings.Contains(r.stdout, frag) {
					t.Errorf("stdout = %q, want it to name %q", r.stdout, frag)
				}
			}
		})
	}

	t.Run("a checksum mismatch installs nothing", func(t *testing.T) {
		r := runInstallGh(t, pins(v, strings.Repeat("0", 64), amdSum), "aarch64", tarballs)
		assertNothingInstalled(t, r)
	})

	t.Run("a binary that reports another version installs nothing", func(t *testing.T) {
		wrong, wrongSum := ghReleaseTarball(t, v, "arm64", "2.99.0")
		r := runInstallGh(t, pins(v, wrongSum, amdSum), "aarch64", map[string][]byte{"gh_" + v + "_linux_arm64.tar.gz": wrong})
		assertNothingInstalled(t, r)
	})

	t.Run("a failed download installs nothing", func(t *testing.T) {
		r := runInstallGh(t, good, "aarch64", nil)
		assertNothingInstalled(t, r)
	})

	t.Run("an unknown architecture fails before any download", func(t *testing.T) {
		r := runInstallGh(t, good, "riscv64", tarballs)
		assertNothingInstalled(t, r)
		if got := r.curlURLs(t); got != nil {
			t.Errorf("curl ran for an unknown architecture: %q", got)
		}
	})

	for _, missing := range []string{"RUNNER_TEMP", "GITHUB_PATH"} {
		t.Run("fails without "+missing, func(t *testing.T) {
			r := runInstallGh(t, good, "aarch64", tarballs, missing)
			if r.exit == 0 || !strings.Contains(r.stderr, missing) {
				t.Errorf("exit %d, stderr %q; want a non-zero exit naming %s", r.exit, r.stderr, missing)
			}
			if got := r.curlURLs(t); got != nil {
				t.Errorf("curl ran without %s: %q", missing, got)
			}
		})
	}

	for name, bad := range map[string]string{
		"no GH_VERSION":            `GH_SHA256_ARM64="` + armSum + `"` + "\n" + `GH_SHA256_AMD64="` + amdSum + `"`,
		"a non-numeric GH_VERSION": pins("latest", armSum, amdSum),
		"a short sha":              pins(v, armSum, amdSum[:63]),
		"an uppercase sha":         pins(v, strings.ToUpper(armSum), amdSum),
		"no GH_SHA256_AMD64":       `GH_VERSION="` + v + `"` + "\n" + `GH_SHA256_ARM64="` + armSum + `"`,
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			r := runInstallGh(t, bad, "aarch64", tarballs)
			assertNothingInstalled(t, r)
			if got := r.curlURLs(t); got != nil {
				t.Errorf("curl ran although the pins are unusable: %q", got)
			}
		})
	}

	t.Run("static", func(t *testing.T) {
		info, err := os.Stat(filepath.Join(findModuleRoot(t), installGhRel))
		if err != nil {
			t.Fatalf("stat %s: %v", installGhRel, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("%s must be executable", installGhRel)
		}
		src := readRepoFile(t, installGhRel)
		for desc, re := range map[string]string{
			"an uncommented self-hosted (the OSS publish guard scans .github)": `(?m)^[^#\n]*self-hosted`,
			"a pip line (the CI pip lint demands require-hashes)":              `(^|[^A-Za-z])pip3?\s`,
			"ubuntu-latest (hosted runners are not used)":                      `ubuntu-latest`,
			"a bash >= 4 construct":                                            `declare -A|mapfile|readarray|\$\{[A-Za-z_]+,,\}|\[\[ -v|EPOCHSECONDS|(^|[;&| ])timeout `,
			"a hardcoded gh version (the pin lives in quickstart.sh)":          `[0-9]+\.[0-9]+\.[0-9]+`,
			"an apt install (gh converges from its release tarball)":           `(^|[^A-Za-z])apt(-get)?\s`,
		} {
			if regexp.MustCompile(re).MatchString(src) {
				t.Errorf("%s contains %s", installGhRel, desc)
			}
		}
		for _, want := range []string{"quickstart.sh", "GH_VERSION", "GH_SHA256_ARM64", "GH_SHA256_AMD64", "sha256sum --check --strict"} {
			if !strings.Contains(src, want) {
				t.Errorf("%s does not name %q", installGhRel, want)
			}
		}
	})
}
