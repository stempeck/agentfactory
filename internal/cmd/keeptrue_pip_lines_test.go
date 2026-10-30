package cmd

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// shellInvocation is one simple command found in a shell-like source (a script, a Dockerfile RUN, a
// Makefile recipe or a workflow `run:` block). Cmd is the command word without its path; Prefix holds
// the wrapper words that ran it (sudo, env, VAR=val, exec, timeout); Args keep their quoting so a lint
// can tell a quoted local path from a bare package name. Redirections are dropped.
type shellInvocation struct {
	Line   int
	Prefix []string
	Cmd    string
	Args   []string
}

func (inv shellInvocation) String() string {
	words := append(append(append([]string{}, inv.Prefix...), inv.Cmd), inv.Args...)
	return fmt.Sprintf("line %d: %s", inv.Line, strings.Join(words, " "))
}

func (inv shellInvocation) hasArg(want string) bool {
	for _, a := range inv.Args {
		if shellUnquote(a) == want {
			return true
		}
	}
	return false
}

// hasFlagValue accepts `--flag value` and `--flag=value`.
func (inv shellInvocation) hasFlagValue(flag, value string) bool {
	for i, a := range inv.Args {
		a = shellUnquote(a)
		if a == flag+"="+value {
			return true
		}
		if a == flag && i+1 < len(inv.Args) && shellUnquote(inv.Args[i+1]) == value {
			return true
		}
	}
	return false
}

// positionals are the arguments that are not flags.
func (inv shellInvocation) positionals() []string {
	var out []string
	for _, a := range inv.Args {
		if !strings.HasPrefix(shellUnquote(a), "-") {
			out = append(out, a)
		}
	}
	return out
}

func shellUnquote(w string) string {
	return strings.NewReplacer(`"`, "", `'`, "").Replace(w)
}

// shellInvocations is a small shell lexer, not a parser: enough structure that a verb lint sees only
// real invocations. Quoted text stays inside its word, so `log_warn "pip install failed"` and a YAML
// `name:` line are text; comments are dropped; `$(…)` and backticks, including inside double quotes,
// are scanned as commands of their own; heredoc bodies are data written somewhere, never commands.
func shellInvocations(src string) []shellInvocation {
	lx := &shellLexer{s: src, line: 1}
	lx.list(0)
	return lx.out
}

type shellLexer struct {
	s        string
	i        int
	line     int
	heredocs []string
	out      []shellInvocation
}

var makeVarRe = regexp.MustCompile(`^\$\([A-Z_][A-Z0-9_]*\)`)

func (lx *shellLexer) peek(off int) byte {
	if lx.i+off < len(lx.s) {
		return lx.s[lx.i+off]
	}
	return 0
}

func (lx *shellLexer) list(closer byte) {
	var words []string
	var cur strings.Builder
	inWord := false
	segLine := lx.line
	flushWord := func() {
		if inWord {
			words = append(words, cur.String())
		}
		cur.Reset()
		inWord = false
	}
	startWord := func() {
		if !inWord {
			if len(words) == 0 {
				segLine = lx.line
			}
			inWord = true
		}
	}
	flushSeg := func() {
		flushWord()
		lx.emit(segLine, words)
		words = nil
	}
	for lx.i < len(lx.s) {
		c := lx.s[lx.i]
		switch {
		case closer != 0 && c == closer:
			lx.i++
			flushSeg()
			return
		case c == '\n':
			flushSeg()
			lx.newline()
		case c == ' ' || c == '\t' || c == '\r':
			flushWord()
			lx.i++
		case c == '\\' && lx.peek(1) == '\n':
			flushWord()
			lx.i += 2
			lx.line++
		case c == '#' && !inWord:
			for lx.i < len(lx.s) && lx.s[lx.i] != '\n' {
				lx.i++
			}
		case c == '\\':
			startWord()
			cur.WriteByte(c)
			if lx.i+1 < len(lx.s) {
				cur.WriteByte(lx.s[lx.i+1])
			}
			lx.i += 2
		case c == '\'':
			startWord()
			lx.single(&cur)
		case c == '"':
			startWord()
			lx.double(&cur)
		case strings.HasPrefix(lx.s[lx.i:], "$(("):
			startWord()
			lx.balanced(&cur, '(', ')')
		case makeVarRe.MatchString(lx.s[lx.i:]):
			startWord()
			m := makeVarRe.FindString(lx.s[lx.i:])
			cur.WriteString(m)
			lx.i += len(m)
		case strings.HasPrefix(lx.s[lx.i:], "$("):
			startWord()
			lx.i += 2
			lx.list(')')
			cur.WriteString("$(…)")
		case c == '`':
			startWord()
			lx.i++
			lx.list('`')
			cur.WriteString("`…`")
		case strings.HasPrefix(lx.s[lx.i:], "${"):
			startWord()
			lx.balanced(&cur, '{', '}')
		case strings.HasPrefix(lx.s[lx.i:], "<<") && !strings.HasPrefix(lx.s[lx.i:], "<<<"):
			flushWord()
			lx.heredoc()
		case c == '>' || c == '<' || (c == '&' && lx.peek(1) == '>'):
			startWord()
			cur.WriteByte(c)
			lx.i++
			if n := lx.peek(0); n == '&' || n == '>' || n == '|' {
				cur.WriteByte(n)
				lx.i++
			}
		case c == ';' || c == '&' || c == '|':
			flushSeg()
			lx.i++
		case c == '(' && lx.peek(1) == ')':
			// `name() {` defines a function; it is not a call of name.
			flushWord()
			words = nil
			lx.i += 2
		case c == '(' || c == ')':
			flushSeg()
			lx.i++
		case (c == '{' || c == '}') && !inWord && shellWordEnd(lx.peek(1)):
			flushSeg()
			lx.i++
		default:
			startWord()
			cur.WriteByte(c)
			lx.i++
		}
	}
	flushSeg()
}

func shellWordEnd(b byte) bool {
	return b == 0 || b == ' ' || b == '\t' || b == '\n' || b == ';' || b == ')'
}

func (lx *shellLexer) newline() {
	lx.i++
	lx.line++
	for _, delim := range lx.heredocs {
		for lx.i < len(lx.s) {
			end := strings.IndexByte(lx.s[lx.i:], '\n')
			var body string
			if end == -1 {
				body, lx.i = lx.s[lx.i:], len(lx.s)
			} else {
				body = lx.s[lx.i : lx.i+end]
				lx.i += end + 1
				lx.line++
			}
			if strings.TrimSpace(body) == delim {
				break
			}
		}
	}
	lx.heredocs = nil
}

func (lx *shellLexer) heredoc() {
	lx.i += 2
	if lx.peek(0) == '-' {
		lx.i++
	}
	for lx.peek(0) == ' ' || lx.peek(0) == '\t' {
		lx.i++
	}
	start := lx.i
	for lx.i < len(lx.s) && !strings.ContainsRune(" \t\n;|&)<>", rune(lx.s[lx.i])) {
		lx.i++
	}
	if delim := shellUnquote(lx.s[start:lx.i]); delim != "" {
		lx.heredocs = append(lx.heredocs, delim)
	}
}

func (lx *shellLexer) single(cur *strings.Builder) {
	stop := len(lx.s)
	if end := strings.IndexByte(lx.s[lx.i+1:], '\''); end != -1 {
		stop = lx.i + 1 + end + 1
	}
	text := lx.s[lx.i:stop]
	lx.line += strings.Count(text, "\n")
	cur.WriteString(text)
	lx.i = stop
}

func (lx *shellLexer) double(cur *strings.Builder) {
	cur.WriteByte('"')
	lx.i++
	for lx.i < len(lx.s) {
		c := lx.s[lx.i]
		switch {
		case c == '"':
			cur.WriteByte(c)
			lx.i++
			return
		case c == '\\' && lx.i+1 < len(lx.s):
			if lx.s[lx.i+1] == '\n' {
				lx.line++
			}
			cur.WriteString(lx.s[lx.i : lx.i+2])
			lx.i += 2
		case strings.HasPrefix(lx.s[lx.i:], "$(("):
			lx.balanced(cur, '(', ')')
		case strings.HasPrefix(lx.s[lx.i:], "$("):
			lx.i += 2
			lx.list(')')
			cur.WriteString("$(…)")
		case c == '`':
			lx.i++
			lx.list('`')
			cur.WriteString("`…`")
		default:
			if c == '\n' {
				lx.line++
			}
			cur.WriteByte(c)
			lx.i++
		}
	}
}

// balanced copies `$((…))` or `${…}` into the current word, as data.
func (lx *shellLexer) balanced(cur *strings.Builder, open, close byte) {
	depth := 0
	for lx.i < len(lx.s) {
		c := lx.s[lx.i]
		cur.WriteByte(c)
		lx.i++
		if c == '\n' {
			lx.line++
		}
		if c == open {
			depth++
		} else if c == close {
			depth--
			if depth == 0 {
				if open == '(' && lx.peek(0) == ')' {
					cur.WriteByte(')')
					lx.i++
				}
				return
			}
		}
	}
}

var (
	shellKeywords   = map[string]bool{"if": true, "then": true, "do": true, "else": true, "elif": true, "while": true, "until": true, "!": true, "time": true, "RUN": true, "run:": true, "-": true}
	shellAssignRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	shellRedirectRe = regexp.MustCompile(`^[0-9]*(<<<|<>|>>|>\||>&|<&|&>>|&>|>|<)`)
)

func (lx *shellLexer) emit(line int, words []string) {
	var kept []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "<<") && !strings.HasPrefix(w, "<<<") {
			continue
		}
		if op := shellRedirectRe.FindString(w); op != "" {
			if op == w {
				i++
			}
			continue
		}
		kept = append(kept, w)
	}
	for len(kept) > 0 && shellKeywords[kept[0]] {
		kept = kept[1:]
	}
	var prefix []string
	for len(kept) > 0 {
		w := kept[0]
		switch {
		case w == "sudo" || w == "env":
			prefix = append(prefix, w)
			kept = kept[1:]
			for len(kept) > 0 && (strings.HasPrefix(kept[0], "-") || (w == "env" && shellAssignRe.MatchString(kept[0]))) {
				prefix = append(prefix, kept[0])
				kept = kept[1:]
			}
			continue
		case w == "exec" || w == "nohup":
			prefix = append(prefix, w)
			kept = kept[1:]
			continue
		case w == "timeout":
			prefix = append(prefix, w)
			kept = kept[1:]
			for len(kept) > 0 && strings.HasPrefix(kept[0], "-") {
				prefix = append(prefix, kept[0])
				kept = kept[1:]
			}
			if len(kept) > 0 {
				prefix = append(prefix, kept[0])
				kept = kept[1:]
			}
			continue
		case shellAssignRe.MatchString(w):
			prefix = append(prefix, w)
			kept = kept[1:]
			continue
		}
		break
	}
	if len(kept) == 0 {
		return
	}
	cmd := strings.TrimLeft(shellUnquote(kept[0]), "@+-")
	if cmd == "" {
		return
	}
	if i := strings.LastIndex(cmd, "/"); i != -1 {
		cmd = cmd[i+1:]
	}
	lx.out = append(lx.out, shellInvocation{Line: line, Prefix: prefix, Cmd: cmd, Args: kept[1:]})
}

var pipCmdRe = regexp.MustCompile(`^pip3?(\.[0-9]+)?$`)
var pythonCmdRe = regexp.MustCompile(`^python3?(\.[0-9]+)?$`)

func isPipInstall(inv shellInvocation) bool {
	args := inv.positionals()
	if pipCmdRe.MatchString(inv.Cmd) {
		return len(args) > 0 && shellUnquote(args[0]) == "install"
	}
	if pythonCmdRe.MatchString(inv.Cmd) {
		for i := 0; i+2 < len(inv.Args); i++ {
			if shellUnquote(inv.Args[i]) == "-m" && shellUnquote(inv.Args[i+1]) == "pip" && shellUnquote(inv.Args[i+2]) == "install" {
				return true
			}
		}
	}
	return false
}

// pipLineErrors holds every real pip install invocation to the hash-lock rule (D-39, E2). The
// system-site sources also need --break-system-packages, which a venv line must not carry.
func pipLineErrors(name, content string, systemSite bool) (errs []string, count int) {
	for _, inv := range shellInvocations(content) {
		if !isPipInstall(inv) {
			continue
		}
		count++
		var missing []string
		if !inv.hasArg("--require-hashes") {
			missing = append(missing, "--require-hashes")
		}
		if !inv.hasFlagValue("--only-binary", ":all:") {
			missing = append(missing, "--only-binary :all:")
		}
		if !inv.hasArg("--disable-pip-version-check") {
			missing = append(missing, "--disable-pip-version-check")
		}
		if systemSite && !inv.hasArg("--break-system-packages") {
			missing = append(missing, "--break-system-packages")
		}
		if len(missing) > 0 {
			errs = append(errs, fmt.Sprintf("%s %s: pip install lacks %s", name, inv, strings.Join(missing, ", ")))
		}
	}
	return errs, count
}

// repoShellScripts lists every *.sh outside dot-dirs (.scratch-p2/ holds a Go distribution during
// AC 10) and node_modules.
func repoShellScripts(t *testing.T) []string {
	t.Helper()
	root := findModuleRoot(t)
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".sh") {
			rel, _ := filepath.Rel(root, path)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the repo for *.sh: %v", err)
	}
	return out
}

func TestPipLines(t *testing.T) {
	t.Run("real install sites carry the hash-lock flags", func(t *testing.T) {
		sources := []struct {
			path       string
			systemSite bool
			minCount   int
			maxCount   int
		}{
			{"quickstart.sh", true, 2, -1},
			{"Dockerfile", true, 1, 1},
			{"Makefile", false, 2, -1},
			{".github/workflows/test.yml", false, 1, -1},
		}
		seen := map[string]bool{}
		for _, src := range sources {
			seen[src.path] = true
			errs, n := pipLineErrors(src.path, readRepoFile(t, src.path), src.systemSite)
			for _, e := range errs {
				t.Error(e)
			}
			if n < src.minCount || (src.maxCount >= 0 && n > src.maxCount) {
				want := fmt.Sprintf("at least %d", src.minCount)
				if src.maxCount >= 0 {
					want = fmt.Sprintf("%d..%d", src.minCount, src.maxCount)
				}
				t.Errorf("%s: found %d pip install invocation(s), want %s — the lint must see the real install sites",
					src.path, n, want)
			}
		}
		for _, rel := range repoShellScripts(t) {
			if seen[rel] {
				continue
			}
			errs, _ := pipLineErrors(rel, readRepoFile(t, rel), false)
			for _, e := range errs {
				t.Error(e)
			}
		}
	})

	// The image's python3-pip and python3-wheel are dpkg-owned with no RECORD, so pip cannot
	// uninstall them to apply the lock's pip and wheel pins and the build fails at this line.
	t.Run("the Dockerfile's system-site lock install ignores the Debian-owned pip and wheel", func(t *testing.T) {
		n := 0
		for _, inv := range shellInvocations(readRepoFile(t, "Dockerfile")) {
			if !isPipInstall(inv) {
				continue
			}
			n++
			if !inv.hasArg("--ignore-installed") {
				t.Errorf("Dockerfile %s: pip install lacks --ignore-installed", inv)
			}
		}
		if n == 0 {
			t.Fatal("the Dockerfile has no pip install; the lock install this pins is gone")
		}
	})

	t.Run("quickstart.sh does not export PIP_REQUIRE_VIRTUALENV", func(t *testing.T) {
		if regexp.MustCompile(`(?m)^[^#\n]*PIP_REQUIRE_VIRTUALENV`).MatchString(readQuickstart(t)) {
			t.Error("quickstart.sh sets PIP_REQUIRE_VIRTUALENV; that is out of this phase's scope (H-10)")
		}
	})

	t.Run("synthetic unhashed pip line is reported", func(t *testing.T) {
		errs, n := pipLineErrors("synthetic.sh", "pip3 install --break-system-packages x==1\n", true)
		if n != 1 || len(errs) != 1 || !strings.Contains(errs[0], "--require-hashes") {
			t.Fatalf("an unhashed pip3 install must be reported; got count=%d errs=%q", n, errs)
		}
	})

	t.Run("synthetic python -m pip and venv pip lines are reported", func(t *testing.T) {
		src := "python3 -m pip install foo\n" +
			"\t$(VENV)/bin/pip install --quiet -r $(PY_REQS)\n" +
			"      - run: .venv/bin/pip install -r py/requirements.txt\n"
		errs, n := pipLineErrors("synthetic", src, false)
		if n != 3 || len(errs) != 3 {
			t.Fatalf("each of the three pip install forms must be seen and reported; got count=%d errs=%q", n, errs)
		}
	})

	t.Run("synthetic flags split across a continuation are accepted", func(t *testing.T) {
		src := "if ! pip3 install --break-system-packages --require-hashes \\\n" +
			"    --only-binary :all: \\\n" +
			"    --disable-pip-version-check -r \"$SCRIPT_DIR/py/requirements.txt\"; then\n" +
			"  log_error \"lock install failed\"\nfi\n"
		errs, n := pipLineErrors("synthetic.sh", src, true)
		if n != 1 || len(errs) != 0 {
			t.Fatalf("a fully flagged pip line split by `\\` must count once and pass; got count=%d errs=%q", n, errs)
		}
	})

	t.Run("synthetic comment echo and log mentions are ignored", func(t *testing.T) {
		src := "# pip install foo\n" +
			"log_warn \"pip install failed\"\n" +
			"echo \"ERROR: $f has pip install without --require-hashes\"\n" +
			"x=1 # then pip install bar\n" +
			"cat <<'EOF'\npip install from-a-heredoc\nEOF\n"
		if errs, n := pipLineErrors("synthetic.sh", src, true); n != 0 || len(errs) != 0 {
			t.Fatalf("mentions of pip install in comments, echo/log text and heredocs are not invocations; got count=%d errs=%q", n, errs)
		}
	})

	t.Run("synthetic workflow step names and the lint job's grep are ignored", func(t *testing.T) {
		src := "      - name: verify pip installs use --require-hashes\n" +
			"        run: |\n" +
			"          for f in $(grep -rl 'pip.*install' --include='*.sh' .) Dockerfile Makefile; do\n" +
			"            if [ -f \"$f\" ] && grep -q 'pip.*install' \"$f\"; then echo bad; fi\n" +
			"          done\n"
		if errs, n := pipLineErrors("synthetic.yml", src, false); n != 0 || len(errs) != 0 {
			t.Fatalf("a YAML step name and grep patterns are not pip invocations; got count=%d errs=%q", n, errs)
		}
	})
}

var exactSemverRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
var npmSpecRe = regexp.MustCompile(`^(@[a-z0-9._-]+/)?[a-z0-9._-]+@(.+)$`)
var npmConstRefRe = regexp.MustCompile(`^\$\{?([A-Z][A-Z0-9_]*)\}?$`)

// npmSpecPinned accepts a verified local tarball path, name@<exact semver>, or name@"$CONST" where
// CONST is a top-level literal exact version in the same file. A bare name, a dist-tag or a range
// lets the registry pick the bytes.
func npmSpecPinned(raw, content string) bool {
	v := shellUnquote(raw)
	if strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") || strings.HasPrefix(v, "/") ||
		strings.HasPrefix(v, "~/") || strings.HasSuffix(v, ".tgz") ||
		(strings.HasPrefix(v, "$") && strings.Contains(v, "/")) {
		return true
	}
	m := npmSpecRe.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	if exactSemverRe.MatchString(m[2]) {
		return true
	}
	if c := npmConstRefRe.FindStringSubmatch(m[2]); c != nil {
		lit := regexp.MustCompile(`(?m)^` + c[1] + `="([^"$]+)"$`).FindStringSubmatch(content)
		return lit != nil && exactSemverRe.MatchString(lit[1])
	}
	return false
}

func npmGlobalPinErrors(name, content string) (errs []string, count int) {
	for _, inv := range shellInvocations(content) {
		var specs []string
		switch inv.Cmd {
		case "npm":
			pos := inv.positionals()
			global := inv.hasArg("-g") || inv.hasArg("--global")
			if len(pos) == 0 || !global {
				continue
			}
			if verb := shellUnquote(pos[0]); verb != "install" && verb != "i" {
				continue
			}
			specs = pos[1:]
		case "npx":
			if !inv.hasArg("-y") && !inv.hasArg("--yes") {
				continue
			}
			if pos := inv.positionals(); len(pos) > 0 {
				specs = pos[:1]
			}
		default:
			continue
		}
		count++
		if len(specs) == 0 {
			errs = append(errs, fmt.Sprintf("%s %s: names no package", name, inv))
		}
		for _, s := range specs {
			if !npmSpecPinned(s, content) {
				errs = append(errs, fmt.Sprintf("%s %s: %s is not a verified tarball, an exact version or a pinned constant",
					name, inv, s))
			}
		}
	}
	return errs, count
}

func TestNpmGlobalPins(t *testing.T) {
	t.Run("real global installs are pinned", func(t *testing.T) {
		for _, path := range []string{"quickstart.sh", "internal/cmd/integrations/playwright/af/install.sh",
			"Dockerfile", "Makefile", ".github/workflows/test.yml"} {
			errs, n := npmGlobalPinErrors(path, readRepoFile(t, path))
			for _, e := range errs {
				t.Error(e)
			}
			if path == "quickstart.sh" && n < 1 {
				t.Errorf("quickstart.sh: found no npm global install; the lint must see _npm_install_pinned's install line")
			}
		}
	})

	cases := []struct {
		name     string
		src      string
		count    int
		reported bool
	}{
		{"synthetic unpinned global install is reported", "npm install -g @openai/codex >>\"$log\" 2>&1 || true\n", 1, true},
		{"synthetic npx latest is reported", "npx -y @playwright/mcp@latest --headless\n", 1, true},
		{"synthetic range is reported", "npm i -g playwright@^1.64\n", 1, true},
		{"synthetic unassigned constant is reported", "npm install -g \"playwright@$PLAYWRIGHT_VERSION\"\n", 1, true},
		{"synthetic quoted scoped name is reported", "sudo -n npm install -g \"@anthropic-ai/claude-code\"\n", 1, true},
		{"synthetic sudo tarball install is accepted", "sudo -n npm install -g --ignore-scripts \"$work/$tgz\" || return 1\n", 1, false},
		{"synthetic exact version is accepted", "npm install -g playwright@1.64.1\n", 1, false},
		{"synthetic pinned constant is accepted", "PLAYWRIGHT_VERSION=\"1.64.1\"\nnpm install -g \"playwright@$PLAYWRIGHT_VERSION\"\n", 1, false},
		{"synthetic continuation split argument is accepted", "npm install -g \\\n    --ignore-scripts \\\n    \"$work/$tgz\"\n", 1, false},
		{"synthetic log mention is ignored", "log_warn \"npm install playwright failed; skipping\"\n# npm install -g foo\n", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs, n := npmGlobalPinErrors("synthetic.sh", tc.src)
			if n != tc.count || (len(errs) > 0) != tc.reported {
				t.Fatalf("got count=%d errs=%q; want count=%d reported=%v", n, errs, tc.count, tc.reported)
			}
		})
	}
}
