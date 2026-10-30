package cmd

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The issue-store lock and the LiteLLM lock install into the same interpreter, so a package both
// closures share must resolve to one version with one set of acceptable artifacts; otherwise the
// second install silently replaces what the first one verified.

type lockPin struct {
	name    string
	version string
	marker  string
	hashes  map[string]bool
}

var (
	pep503Sep      = regexp.MustCompile(`[-_.]+`)
	lockHashRe     = regexp.MustCompile(`^--hash=sha256:([0-9a-f]{64})$`)
	lockPinRe      = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(\[[^\]]*\])?==([^\s;]+)\s*(;.*)?$`)
	lockOptionsOK  = map[string]bool{"--only-binary": true}
	sidecarLineRe  = regexp.MustCompile(`^([0-9a-f]{64}) (\S+)$`)
	installLocks   = []string{"py/requirements.txt", "py/litellm-requirements.txt"}
	lockSidecar    = "py/lock-wheels.txt"
	cp312Tags      = `-(?:cp312-cp312|cp3(?:[2-9]|1[0-2])-abi3|py3-none|py2\.py3-none)-`
	wheelArchAarch = regexp.MustCompile(cp312Tags + `manylinux[^-]*_aarch64\.whl$`)
	wheelArchX86   = regexp.MustCompile(cp312Tags + `manylinux[^-]*_x86_64\.whl$`)
	wheelAny       = regexp.MustCompile(`-(?:py3|py2\.py3)-none-any\.whl$`)
)

func normalizePyName(name string) string {
	return pep503Sep.ReplaceAllString(strings.ToLower(name), "-")
}

// parsePipLock reads pip-compile output. Comment lines (header, "# via"), blank lines and the
// option lines pip-compile writes (--only-binary) carry no pin; anything else that is not an
// exact, hashed pin is reported, because an unhashed or ranged line defeats --require-hashes.
func parsePipLock(raw string) (map[string]lockPin, []string) {
	pins := map[string]lockPin{}
	var errs []string
	var logical []string
	var cur strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasSuffix(trimmed, `\`) {
			cur.WriteString(strings.TrimSuffix(trimmed, `\`) + " ")
			continue
		}
		cur.WriteString(trimmed)
		if s := strings.TrimSpace(cur.String()); s != "" {
			logical = append(logical, s)
		}
		cur.Reset()
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		logical = append(logical, s)
	}
	for _, l := range logical {
		if strings.HasPrefix(l, "-") {
			opt := strings.Fields(l)[0]
			if i := strings.Index(opt, "="); i >= 0 {
				opt = opt[:i]
			}
			if !lockOptionsOK[opt] {
				errs = append(errs, fmt.Sprintf("unexpected option line %q", l))
			}
			continue
		}
		var reqPart []string
		hashes := map[string]bool{}
		for _, f := range strings.Fields(l) {
			if strings.HasPrefix(f, "--hash=") {
				m := lockHashRe.FindStringSubmatch(f)
				if m == nil {
					errs = append(errs, fmt.Sprintf("malformed hash %q in %q", f, l))
					continue
				}
				hashes[m[1]] = true
				continue
			}
			reqPart = append(reqPart, f)
		}
		req := strings.Join(reqPart, " ")
		m := lockPinRe.FindStringSubmatch(req)
		if m == nil {
			errs = append(errs, fmt.Sprintf("not an exact == pin: %q", req))
			continue
		}
		if len(hashes) == 0 {
			errs = append(errs, fmt.Sprintf("%s has no --hash", m[1]))
		}
		key := normalizePyName(m[1])
		if _, dup := pins[key]; dup {
			errs = append(errs, fmt.Sprintf("%s pinned twice", key))
		}
		pins[key] = lockPin{name: m[1], version: m[3], marker: strings.TrimSpace(strings.TrimPrefix(m[4], ";")), hashes: hashes}
	}
	return pins, errs
}

func lockParityViolations(a, b map[string]lockPin, aName, bName string) []string {
	var out []string
	for key, pa := range a {
		pb, shared := b[key]
		if !shared {
			continue
		}
		if pa.version != pb.version {
			out = append(out, fmt.Sprintf("%s: %s pins %s, %s pins %s", key, aName, pa.version, bName, pb.version))
		}
		if pa.marker != pb.marker {
			out = append(out, fmt.Sprintf("%s: marker %q in %s, %q in %s", key, pa.marker, aName, pb.marker, bName))
		}
		var onlyA, onlyB []string
		for h := range pa.hashes {
			if !pb.hashes[h] {
				onlyA = append(onlyA, h)
			}
		}
		for h := range pb.hashes {
			if !pa.hashes[h] {
				onlyB = append(onlyB, h)
			}
		}
		if len(onlyA)+len(onlyB) > 0 {
			sort.Strings(onlyA)
			sort.Strings(onlyB)
			out = append(out, fmt.Sprintf("%s==%s: hash sets differ (only in %s: %v; only in %s: %v)", key, pa.version, aName, onlyA, bName, onlyB))
		}
	}
	sort.Strings(out)
	return out
}

func sharedLockKeys(a, b map[string]lockPin) []string {
	var keys []string
	for k := range a {
		if _, ok := b[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

func readInstallLocks(t *testing.T) map[string]map[string]lockPin {
	t.Helper()
	locks := map[string]map[string]lockPin{}
	for _, rel := range installLocks {
		pins, errs := parsePipLock(readRepoFile(t, rel))
		for _, e := range errs {
			t.Errorf("%s: %s", rel, e)
		}
		if len(pins) == 0 {
			t.Fatalf("%s: no pins parsed", rel)
		}
		locks[rel] = pins
	}
	return locks
}

const lockFixtureHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const lockFixtureHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestLocksParity(t *testing.T) {
	t.Run("parser normalises names and ignores via lines", func(t *testing.T) {
		a, errs := parsePipLock("# header\n--only-binary :all:\n\nTyping_Extensions==4.15.0 \\\n    --hash=sha256:" + lockFixtureHashA + "\n    # via -r requirements.in\nzope.interface==7.0 \\\n    --hash=sha256:" + lockFixtureHashB + "\n")
		b, errsB := parsePipLock("typing-extensions==4.15.0 \\\n    --hash=sha256:" + lockFixtureHashA + "\n    # via -r py/requirements.in\nzope-interface==7.0 \\\n    --hash=sha256:" + lockFixtureHashB + "\nlitellm[proxy]==1.93.2 \\\n    --hash=sha256:" + lockFixtureHashA + "\n")
		if len(errs)+len(errsB) != 0 {
			t.Fatalf("well-formed fixtures reported errors: %v %v", errs, errsB)
		}
		if got := sharedLockKeys(a, b); strings.Join(got, ",") != "typing-extensions,zope-interface" {
			t.Errorf("shared keys = %v, want typing-extensions and zope-interface", got)
		}
		if _, ok := b["litellm"]; !ok {
			t.Error("litellm[proxy]==1.93.2 must key as litellm")
		}
		if v := lockParityViolations(a, b, "a", "b"); len(v) != 0 {
			t.Errorf("identical pins reported as differing: %v", v)
		}
	})

	t.Run("parser rejects ranged, unhashed and unknown-option lines", func(t *testing.T) {
		for _, bad := range []string{
			"aiohttp>=3.14.4 \\\n    --hash=sha256:" + lockFixtureHashA + "\n",
			"aiohttp==3.14.4\n",
			"--index-url https://example.invalid/simple\n",
		} {
			if _, errs := parsePipLock(bad); len(errs) == 0 {
				t.Errorf("parsePipLock accepted %q", bad)
			}
		}
	})

	t.Run("a version or hash-set mismatch is reported", func(t *testing.T) {
		a, _ := parsePipLock("attrs==26.1.0 \\\n    --hash=sha256:" + lockFixtureHashA + " \\\n    --hash=sha256:" + lockFixtureHashB + "\n")
		b, _ := parsePipLock("attrs==26.1.0 \\\n    --hash=sha256:" + lockFixtureHashA + "\n")
		c, _ := parsePipLock("attrs==26.2.0 \\\n    --hash=sha256:" + lockFixtureHashA + "\n")
		if v := lockParityViolations(a, b, "a", "b"); len(v) != 1 || !strings.Contains(v[0], lockFixtureHashB) {
			t.Errorf("hash-set mismatch not reported with the differing hash: %v", v)
		}
		if v := lockParityViolations(b, c, "b", "c"); len(v) != 1 || !strings.Contains(v[0], "26.2.0") {
			t.Errorf("version mismatch not reported: %v", v)
		}
	})

	locks := readInstallLocks(t)
	issueStore, gateway := locks[installLocks[0]], locks[installLocks[1]]
	shared := sharedLockKeys(issueStore, gateway)
	for _, must := range []string{"pip", "wheel"} {
		if _, ok := issueStore[must]; !ok {
			t.Errorf("%s has no hashed %s pin (D24: pip and wheel are pinned in both locks)", installLocks[0], must)
		}
		if _, ok := gateway[must]; !ok {
			t.Errorf("%s has no hashed %s pin (D24: pip and wheel are pinned in both locks)", installLocks[1], must)
		}
	}
	if len(shared) == 0 {
		t.Fatal("the two locks share no package; the parity check would be vacuous")
	}
	for _, v := range lockParityViolations(issueStore, gateway, installLocks[0], installLocks[1]) {
		t.Error(v)
	}
}

func parseLockSidecar(raw string) (map[string]string, []string) {
	files := map[string]string{}
	var errs []string
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		m := sidecarLineRe.FindStringSubmatch(line)
		if m == nil {
			errs = append(errs, fmt.Sprintf("malformed sidecar line %q", line))
			continue
		}
		if prev, dup := files[m[1]]; dup && prev != m[2] {
			errs = append(errs, fmt.Sprintf("hash %s names both %s and %s", m[1], prev, m[2]))
		}
		files[m[1]] = m[2]
	}
	return files, errs
}

// A pin only proves both architectures when one of its hashed files actually installs on glibc
// CPython 3.12 there: a musllinux or cp313-only wheel matches "*aarch64*" yet never installs.
func perArchViolations(lockName string, pins map[string]lockPin, sidecar map[string]string) []string {
	var out []string
	keys := make([]string, 0, len(pins))
	for k := range pins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := pins[key]
		var arm, x86 bool
		for h := range p.hashes {
			file, ok := sidecar[h]
			if !ok {
				out = append(out, fmt.Sprintf("%s: %s==%s hash %s has no filename in %s", lockName, p.name, p.version, h, lockSidecar))
				continue
			}
			if wheelAny.MatchString(file) {
				arm, x86 = true, true
			}
			arm = arm || wheelArchAarch.MatchString(file)
			x86 = x86 || wheelArchX86.MatchString(file)
		}
		if !arm {
			out = append(out, fmt.Sprintf("%s: %s==%s has no hashed wheel installable on linux aarch64 (manylinux aarch64 or none-any)", lockName, p.name, p.version))
		}
		if !x86 {
			out = append(out, fmt.Sprintf("%s: %s==%s has no hashed wheel installable on linux x86_64 (manylinux x86_64 or none-any)", lockName, p.name, p.version))
		}
	}
	return out
}

func TestLocksPerArchHashes(t *testing.T) {
	t.Run("only installable wheels satisfy an architecture", func(t *testing.T) {
		h := func(c string) string { return strings.Repeat(c, 64) }
		pins := map[string]lockPin{
			"armonly": {name: "armonly", version: "1.0", hashes: map[string]bool{h("1"): true}},
			"pure":    {name: "pure", version: "1.0", hashes: map[string]bool{h("2"): true}},
			"sdist":   {name: "sdist", version: "1.0", hashes: map[string]bool{h("3"): true}},
			"musl":    {name: "musl", version: "1.0", hashes: map[string]bool{h("4"): true, h("5"): true}},
			"both":    {name: "both", version: "1.0", hashes: map[string]bool{h("6"): true, h("7"): true}},
			"abi3":    {name: "abi3", version: "1.0", hashes: map[string]bool{h("9"): true, h("a"): true}},
			"nofile":  {name: "nofile", version: "1.0", hashes: map[string]bool{h("8"): true}},
		}
		sidecar := map[string]string{
			h("1"): "armonly-1.0-cp312-cp312-manylinux_2_17_aarch64.manylinux2014_aarch64.whl",
			h("2"): "pure-1.0-py3-none-any.whl",
			h("3"): "sdist-1.0.tar.gz",
			h("4"): "musl-1.0-cp312-cp312-musllinux_1_2_aarch64.whl",
			h("5"): "musl-1.0-cp313-cp313-manylinux_2_28_x86_64.whl",
			h("6"): "both-1.0-cp312-cp312-manylinux_2_28_aarch64.whl",
			h("7"): "both-1.0-cp312-cp312-manylinux_2_28_x86_64.whl",
			h("9"): "abi3-1.0-cp311-abi3-manylinux_2_34_aarch64.whl",
			h("a"): "abi3-1.0-cp311-abi3-manylinux_2_17_x86_64.manylinux2014_x86_64.whl",
		}
		got := strings.Join(perArchViolations("fixture", pins, sidecar), "\n")
		for _, want := range []string{
			"armonly==1.0 has no hashed wheel installable on linux x86_64",
			"sdist==1.0 has no hashed wheel installable on linux aarch64",
			"sdist==1.0 has no hashed wheel installable on linux x86_64",
			"musl==1.0 has no hashed wheel installable on linux aarch64",
			"musl==1.0 has no hashed wheel installable on linux x86_64",
			"nofile==1.0 hash " + h("8") + " has no filename",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("violations lack %q:\n%s", want, got)
			}
		}
		for _, clean := range []string{"pure==1.0", "both==1.0", "abi3==1.0", "armonly==1.0 has no hashed wheel installable on linux aarch64"} {
			if strings.Contains(got, clean) {
				t.Errorf("violations wrongly include %q:\n%s", clean, got)
			}
		}
	})

	sidecar, errs := parseLockSidecar(readRepoFile(t, lockSidecar))
	for _, e := range errs {
		t.Errorf("%s: %s", lockSidecar, e)
	}
	if len(sidecar) == 0 {
		t.Fatalf("%s is empty", lockSidecar)
	}
	for rel, pins := range readInstallLocks(t) {
		for _, v := range perArchViolations(rel, pins, sidecar) {
			t.Error(v)
		}
	}
}

// TestLocksSatisfyTheirInputs ties each lock to the .in it is compiled from, so an input edited
// without `make lock` fails here instead of shipping a lock that no longer says what its input says.
func TestLocksSatisfyTheirInputs(t *testing.T) {
	t.Run("the shipped locks", func(t *testing.T) {
		root := findModuleRoot(t)
		read := func(rel string) (string, bool) {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			return string(b), err == nil
		}
		for _, lock := range installLocks {
			for _, v := range adLockInputViolations(read, lock) {
				t.Error(v)
			}
		}
	})

	t.Run("the check rejects each drift", func(t *testing.T) {
		check := func(files map[string]string, lock string) []string {
			return adLockInputViolations(func(rel string) (string, bool) { s, ok := files[rel]; return s, ok }, lock)
		}
		for _, lock := range installLocks {
			if v := check(adLockInputFixture(), lock); len(v) != 0 {
				t.Fatalf("the well-formed fixture was rejected for %s: %v", lock, v)
			}
		}
		const issue, gateway = "py/requirements.txt", "py/litellm-requirements.txt"
		for _, tc := range []struct {
			name, file, old, new, lock string
		}{
			{"a cap the lock no longer meets", "py/requirements.in", "sqlalchemy<2.1", "sqlalchemy<2.0", issue},
			{"a requirement the lock does not pin", "py/requirements.in", "pip==26.2.2\n", "pip==26.2.2\nidna==3.15\n", issue},
			{"an included input's pin that moved", "py/requirements.in", "aiohttp==3.14.4", "aiohttp==3.14.5", gateway},
			{"an operator outside the supported subset", "py/requirements.in", "PyJWT>=2.15.0", "PyJWT~=2.15", issue},
			{"an environment marker", "py/requirements.in", "sqlalchemy<2.1", `sqlalchemy<2.1; python_version >= "3.12"`, issue},
			{"an option outside the supported subset", "py/requirements.in", "pip==26.2.2\n", "pip==26.2.2\n-e .\n", issue},
			{"a single-line via marker its input no longer backs", "py/requirements.in", "PyJWT>=2.15.0\n", "", issue},
			{"a multi-line via marker its input no longer backs", "py/requirements.in", "multidict>=6.9.1,<7\n", "", issue},
			{"a via marker naming an input the lock is not compiled from", issue, "# via -r py/requirements.in\nmultidict", "# via -r py/other.in\nmultidict", issue},
		} {
			files := adLockInputFixture()
			mutated := strings.Replace(files[tc.file], tc.old, tc.new, 1)
			if mutated == files[tc.file] {
				t.Fatalf("%s: the mutation did not apply to the fixture", tc.name)
			}
			files[tc.file] = mutated
			if len(check(files, tc.lock)) == 0 {
				t.Errorf("%s: %s accepted", tc.name, tc.lock)
			}
		}
	})
}

func adLockInputFixture() map[string]string {
	pin := func(name, version string, via ...string) string {
		s := name + "==" + version + " \\\n    --hash=sha256:" + lockFixtureHashA + "\n"
		if len(via) == 1 {
			return s + "    # via " + via[0] + "\n"
		}
		s += "    # via\n"
		for _, v := range via {
			s += "    #   " + v + "\n"
		}
		return s
	}
	header := "#\n# This file is autogenerated by pip-compile with Python 3.12\n#\n--only-binary :all:\n\n"
	unsafe := "\n# The following packages are considered to be unsafe in a requirements file:\n"
	return map[string]string{
		"py/requirements.in": "# Inputs of the issue-store lock.\n" +
			"aiohttp==3.14.4  # lowest fixed\nmultidict>=6.9.1,<7\nsqlalchemy<2.1\nPyJWT>=2.15.0\npip==26.2.2\n",
		"py/litellm-requirements.in": "-r requirements.in  # one interpreter\n-c requirements.txt\nlitellm[proxy]==1.93.2\n",
		"py/requirements.txt": header +
			pin("aiohttp", "3.14.4", "-r py/requirements.in") +
			pin("multidict", "6.9.1", "-r py/requirements.in", "aiohttp") +
			pin("pyjwt", "2.15.0", "-r py/requirements.in") +
			pin("sqlalchemy", "2.0.31", "-r py/requirements.in") +
			pin("typing-extensions", "4.15.0", "sqlalchemy") +
			unsafe + pin("pip", "26.2.2", "-r py/requirements.in"),
		"py/litellm-requirements.txt": header +
			pin("aiohttp", "3.14.4", "-c py/requirements.txt", "-r py/requirements.in", "litellm") +
			pin("litellm", "1.93.2", "-r py/litellm-requirements.in") +
			pin("multidict", "6.9.1", "-c py/requirements.txt", "-r py/requirements.in", "aiohttp") +
			pin("pyjwt", "2.15.0", "-c py/requirements.txt", "-r py/requirements.in") +
			pin("sqlalchemy", "2.0.31", "-c py/requirements.txt", "-r py/requirements.in") +
			pin("typing-extensions", "4.15.0", "sqlalchemy") +
			unsafe + pin("pip", "26.2.2", "-c py/requirements.txt", "-r py/requirements.in"),
	}
}

var (
	adInRequirementRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(\[[^\]]*\])?\s*([^;@]*)$`)
	adSpecifierRe     = regexp.MustCompile(`^(===|==|!=|~=|>=|<=|>|<)\s*(\S+)$`)
	adReleaseRe       = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)
	adLockPinStartRe  = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(\[[^\]]*\])?==`)
)

// adLockInputViolations reads a lock's input from the lock's own name (x.txt is compiled from x.in, as
// `make lock` does) and follows that input's -r includes, because pip-compile writes the included
// file's requirements into the same lock.
func adLockInputViolations(read func(rel string) (string, bool), lockRel string) []string {
	var out []string
	input := strings.TrimSuffix(lockRel, ".txt") + ".in"
	reqs := map[string]map[string]string{}
	var load func(rel string)
	load = func(rel string) {
		if _, done := reqs[rel]; done {
			return
		}
		raw, ok := read(rel)
		if !ok {
			out = append(out, rel+" cannot be read")
			return
		}
		reqs[rel] = map[string]string{}
		for i, line := range strings.Split(raw, "\n") {
			if c := strings.Index(line, "#"); c >= 0 {
				line = line[:c]
			}
			line = strings.TrimSpace(line)
			where := fmt.Sprintf("%s:%d", rel, i+1)
			switch f := strings.Fields(line); {
			case line == "":
			case len(f) == 2 && f[0] == "-r":
				load(path.Join(path.Dir(rel), f[1]))
			case len(f) == 2 && f[0] == "-c":
				// A constraint names the other lock, which TestLocksParity already holds to this one.
			case strings.HasPrefix(line, "-"):
				out = append(out, fmt.Sprintf("%s: option %q is outside what this check understands", where, line))
			default:
				m := adInRequirementRe.FindStringSubmatch(line)
				if m == nil {
					out = append(out, fmt.Sprintf("%s: %q is outside the supported requirement form", where, line))
					continue
				}
				reqs[rel][normalizePyName(m[1])] = strings.TrimSpace(m[3])
			}
		}
	}
	load(input)

	lock, ok := read(lockRel)
	if !ok {
		return append(out, lockRel+" cannot be read")
	}
	pins, errs := parsePipLock(lock)
	out = append(out, errs...)
	checked := 0
	for _, rel := range adSortedKeys(reqs) {
		for _, name := range adSortedKeys(reqs[rel]) {
			checked++
			spec := reqs[rel][name]
			p, pinned := pins[name]
			if !pinned {
				out = append(out, fmt.Sprintf("%s requires %s, which %s does not pin", rel, name, lockRel))
				continue
			}
			ok, err := adSatisfies(p.version, spec)
			if err != nil {
				out = append(out, fmt.Sprintf("%s: %s%s against %s==%s: %v", rel, name, spec, name, p.version, err))
			} else if !ok {
				out = append(out, fmt.Sprintf("%s pins %s==%s, outside %s's %s%s", lockRel, name, p.version, rel, name, spec))
			}
		}
	}
	if checked == 0 {
		out = append(out, lockRel+": its inputs name no requirement, so nothing was checked")
	}

	vias := adViaInputs(lock)
	if len(vias) == 0 {
		out = append(out, lockRel+" carries no `# via -r` marker, so nothing was checked")
	}
	for _, v := range vias {
		names, compiled := reqs[v.input]
		if !compiled {
			out = append(out, fmt.Sprintf("%s: %s is marked via %s, which %s does not include", lockRel, v.pin, v.input, input))
			continue
		}
		if _, ok := names[normalizePyName(v.pin)]; !ok {
			out = append(out, fmt.Sprintf("%s: %s is marked via -r %s, which does not require it", lockRel, v.pin, v.input))
		}
	}
	return out
}

type adVia struct{ pin, input string }

// adViaInputs reads pip-compile's annotations in both shapes it writes: `# via -r x.in` on one line,
// and a bare `# via` followed by one `#   item` line per source.
func adViaInputs(lock string) []adVia {
	var out []adVia
	pin, inVia := "", false
	for _, line := range strings.Split(lock, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(line, " ") {
			pin, inVia = "", false
			if m := adLockPinStartRe.FindStringSubmatch(line); m != nil {
				pin = m[1]
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		switch {
		case item == "via":
			inVia = true
			continue
		case strings.HasPrefix(item, "via "):
			item, inVia = strings.TrimPrefix(item, "via "), false
		case !inVia:
			continue
		}
		if input, ok := strings.CutPrefix(item, "-r "); ok && pin != "" {
			out = append(out, adVia{pin, strings.TrimSpace(input)})
		}
	}
	return out
}

// adSatisfies evaluates the PEP 440 subset the inputs use (==, >=, < over dotted release numbers, comma
// joined). Any other operator or version form is an error, never a guess, so a specifier this cannot
// read fails the test instead of passing it.
func adSatisfies(version, specs string) (bool, error) {
	v, ok := adRelease(version)
	if !ok {
		return false, fmt.Errorf("pinned version %q is outside the dotted-release subset", version)
	}
	if specs == "" {
		return true, nil
	}
	for _, clause := range strings.Split(specs, ",") {
		m := adSpecifierRe.FindStringSubmatch(strings.TrimSpace(clause))
		if m == nil {
			return false, fmt.Errorf("specifier %q is outside the supported subset (==, >=, <)", clause)
		}
		bound, ok := adRelease(m[2])
		if !ok {
			return false, fmt.Errorf("version %q in %q is outside the dotted-release subset", m[2], clause)
		}
		c := adCompareRelease(v, bound)
		switch m[1] {
		case "==":
			if c != 0 {
				return false, nil
			}
		case ">=":
			if c < 0 {
				return false, nil
			}
		case "<":
			if c >= 0 {
				return false, nil
			}
		default:
			return false, fmt.Errorf("operator %s in %q is outside the supported subset (==, >=, <)", m[1], clause)
		}
	}
	return true, nil
}

func adRelease(v string) ([]int, bool) {
	if !adReleaseRe.MatchString(v) {
		return nil, false
	}
	var out []int
	for _, part := range strings.Split(v, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

// adCompareRelease pads the shorter release with zeros, as PEP 440 does: 2.1 == 2.1.0.
func adCompareRelease(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func adSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
