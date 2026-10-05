package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

// Formula step text is markdown that an agent is told to run as shell exactly as written, and the two
// languages collide on the backtick: a markdown code span inside a double-quoted bash argument is
// command substitution. #710's dispatch-parallel bead blocks ran `af done` and a false completion mail
// in the orchestrator's own shell that way, and nothing noticed because no test ever read step text as
// shell. The blocks are read the way the agent executes them — the raw text between the fences,
// dedented by the opener's indentation — not the way CommonMark renders them: CommonMark also ends a
// fence at a list-item boundary, which changes the picture but not what bash runs.

const stepShellRule = "formula step shell runs exactly as written — keep markdown (code spans, fences, inner double quotes) out of quoted arguments (#710)"

var (
	shellFenceLangs = map[string]bool{"bash": true, "sh": true, "shell": true}
	fenceOpenRe     = regexp.MustCompile("^( *)(`{3,})\\s*([^`\\s]*)[^`]*$")
	fenceCloseRe    = regexp.MustCompile("^ *(`{3,})\\s*$")
	heredocOpRe     = regexp.MustCompile(`^<<(-?)\s*(['"]?)\\?([A-Za-z_][A-Za-z0-9_]*)`)
)

type fencedBlock struct {
	line  int
	lang  string
	ticks int
	body  string
}

func fencedBlocks(text string) []fencedBlock {
	lines := strings.Split(text, "\n")
	var blocks []fencedBlock
	for i := 0; i < len(lines); i++ {
		m := fenceOpenRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		indent, ticks := len(m[1]), len(m[2])
		var body []string
		j := i + 1
		for ; j < len(lines); j++ {
			if c := fenceCloseRe.FindStringSubmatch(lines[j]); c != nil && len(c[1]) >= ticks {
				break
			}
			ln := lines[j]
			body = append(body, ln[min(indent, len(ln)-len(strings.TrimLeft(ln, " "))):])
		}
		blocks = append(blocks, fencedBlock{line: i + 1, lang: m[3], ticks: ticks, body: strings.Join(body, "\n")})
		i = j
	}
	return blocks
}

type scanCtx int

const (
	ctxTop scanCtx = iota
	ctxSingle
	ctxANSI
	ctxDouble
	ctxBacktick
	ctxSubst
	ctxBrace
)

type quotedSpan struct{ start, end int }

type pendingHeredoc struct {
	stripTabs bool
	delim     string
}

type shellScan struct {
	spans        []quotedSpan
	dqBackticks  []int
	placeholders [][2]int
	unterminated bool
}

// scanShell tracks bash quoting just far enough to find where each quoted span starts and ends. An
// unquoted <word …> is an agent-filled placeholder, not a redirection: left in, its apostrophes and
// angle brackets would make every block that uses one fail to parse.
func scanShell(src string) shellScan {
	type frame struct {
		ctx   scanCtx
		start int
		depth int
	}
	var sc shellScan
	var heredocs []pendingHeredoc
	stack := []frame{{ctx: ctxTop}}
	push := func(ctx scanCtx, start int) { stack = append(stack, frame{ctx: ctx, start: start, depth: 1}) }
	pop := func() { stack = stack[:len(stack)-1] }

	for i := 0; i < len(src); {
		f := &stack[len(stack)-1]
		c := src[i]
		switch f.ctx {
		case ctxSingle:
			if c == '\'' {
				sc.spans = append(sc.spans, quotedSpan{f.start, i})
				pop()
			}
			i++
			continue
		case ctxANSI:
			if c == '\\' {
				i += 2
				continue
			}
			if c == '\'' {
				pop()
			}
			i++
			continue
		}
		if c == '\\' {
			i += 2
			continue
		}
		switch f.ctx {
		case ctxDouble:
			switch {
			case c == '"':
				sc.spans = append(sc.spans, quotedSpan{f.start, i})
				pop()
			case c == '`':
				sc.dqBackticks = append(sc.dqBackticks, i)
				push(ctxBacktick, i)
			case strings.HasPrefix(src[i:], "$("):
				push(ctxSubst, i)
				i++
			case strings.HasPrefix(src[i:], "${"):
				push(ctxBrace, i)
				i++
			}
			i++
			continue
		case ctxBacktick:
			if c == '`' {
				pop()
			}
			i++
			continue
		case ctxBrace:
			if c == '}' {
				pop()
				i++
				continue
			}
		case ctxSubst:
			if c == '(' {
				f.depth++
			} else if c == ')' {
				if f.depth--; f.depth == 0 {
					pop()
					i++
					continue
				}
			}
		}

		switch {
		case c == '\'':
			if i > 0 && src[i-1] == '$' {
				push(ctxANSI, i)
			} else {
				push(ctxSingle, i)
			}
		case c == '"':
			push(ctxDouble, i)
		case c == '`':
			push(ctxBacktick, i)
		case strings.HasPrefix(src[i:], "$("):
			push(ctxSubst, i)
			i++
		case strings.HasPrefix(src[i:], "${"):
			push(ctxBrace, i)
			i++
		case c == '<' && i+1 < len(src) && isLetterByte(src[i+1]):
			if end := placeholderEnd(src, i); end > 0 {
				sc.placeholders = append(sc.placeholders, [2]int{i, end})
				i = end
				continue
			}
		case c == '#' && f.ctx != ctxBrace && (i == 0 || strings.IndexByte(" \t\n;&|()", src[i-1]) >= 0):
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case strings.HasPrefix(src[i:], "<<") && !strings.HasPrefix(src[i:], "<<<"):
			if m := heredocOpRe.FindStringSubmatch(src[i:]); m != nil && strings.HasPrefix(src[i+len(m[0]):], m[2]) {
				heredocs = append(heredocs, pendingHeredoc{stripTabs: m[1] == "-", delim: m[3]})
				i += len(m[0]) + len(m[2])
				continue
			}
			i++
		case c == '\n' && len(heredocs) > 0:
			i = skipHeredocBodies(src, i+1, heredocs)
			heredocs = nil
			continue
		}
		i++
	}
	for _, f := range stack[1:] {
		if f.ctx == ctxDouble || f.ctx == ctxSingle {
			sc.spans = append(sc.spans, quotedSpan{f.start, len(src)})
			sc.unterminated = true
		}
	}
	return sc
}

func placeholderEnd(src string, i int) int {
	depth := 0
	for k := i; k < len(src) && src[k] != '\n'; k++ {
		switch src[k] {
		case '<':
			depth++
		case '>':
			if depth--; depth == 0 {
				return k + 1
			}
		}
	}
	return -1
}

func skipHeredocBodies(src string, i int, heredocs []pendingHeredoc) int {
	for _, h := range heredocs {
		for i < len(src) {
			line := src[i:]
			if e := strings.IndexByte(line, '\n'); e >= 0 {
				line = line[:e]
				i += e + 1
			} else {
				i = len(src)
			}
			if h.stripTabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.delim {
				break
			}
		}
	}
	return i
}

// checkShellText returns one finding per rule a fenced block in text breaks. F applies to every block,
// whatever its language: a template block desynchronised by its own inner fence hides the shell block
// after it from the other rules. P, S and G apply to shell blocks:
//
//	F  a nested opening fence: the next bare fence closes the OUTER block, so the rest of the block
//	   renders as prose and a stray fence opens a new block further down.
//	P  a block with a multi-line (or unterminated) quoted span that fails `bash -n`.
//	S  a backtick inside "…": always a markdown code span, never an intended substitution. `$(` is
//	   allowed — formulas use it deliberately inside multi-line arguments.
//	G  a multi-line quoted span whose closing quote is glued to a word: an inner quote ended the
//	   argument early. This one parses, so `bash -n` alone never sees it.
func checkShellText(text string) []string {
	var findings []string
	for _, b := range fencedBlocks(text) {
		if f := nestedFenceFinding(b); f != "" {
			findings = append(findings, fmt.Sprintf("%q block at line %d: %s", b.lang, b.line, f))
		}
		if !shellFenceLangs[b.lang] {
			continue
		}
		for _, f := range checkShellBlock(b) {
			findings = append(findings, fmt.Sprintf("shell block at line %d: %s", b.line, f))
		}
	}
	return findings
}

func nestedFenceFinding(b fencedBlock) string {
	for n, ln := range strings.Split(b.body, "\n") {
		if m := fenceOpenRe.FindStringSubmatch(ln); m != nil && m[3] != "" && len(m[2]) >= b.ticks {
			return fmt.Sprintf("F: nested opening fence %q at block line %d", strings.TrimSpace(ln), n+1)
		}
	}
	return ""
}

func checkShellBlock(b fencedBlock) []string {
	var findings []string
	sc := scanShell(b.body)
	var multiline []quotedSpan
	for _, s := range sc.spans {
		if strings.Contains(b.body[s.start:s.end], "\n") {
			multiline = append(multiline, s)
		}
	}
	if len(multiline) > 0 || sc.unterminated {
		if msg := bashSyntaxError(neutralisePlaceholders(b.body, sc.placeholders)); msg != "" {
			findings = append(findings, "P: bash -n: "+msg)
		}
	}
	for _, s := range multiline {
		if s.end+1 < len(b.body) && isWordByte(b.body[s.end+1]) {
			findings = append(findings, fmt.Sprintf("G: multi-line quoted argument closes mid-word at block line %d: %q",
				blockLine(b.body, s.end), excerpt(b.body, s.end)))
			break
		}
	}
	if len(sc.dqBackticks) > 0 {
		off := sc.dqBackticks[0]
		findings = append(findings, fmt.Sprintf("S: backtick inside \"…\" at block line %d runs as command substitution: %q",
			blockLine(b.body, off), excerpt(b.body, off)))
	}
	return findings
}

func neutralisePlaceholders(src string, placeholders [][2]int) string {
	for k := len(placeholders) - 1; k >= 0; k-- {
		p := placeholders[k]
		src = src[:p[0]] + "PLACEHOLDER" + src[p[1]:]
	}
	return src
}

func bashSyntaxError(src string) string {
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(src)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); first != "" {
			return first
		}
		return err.Error()
	}
	return ""
}

func blockLine(body string, off int) int { return strings.Count(body[:off], "\n") + 1 }

func excerpt(body string, off int) string { return body[max(0, off-20):min(len(body), off+30)] }

type formulaShellText struct{ where, text string }

func formulaShellTexts(f *formula.Formula) []formulaShellText {
	texts := []formulaShellText{{"(formula description)", f.Description}}
	for _, s := range f.Steps {
		texts = append(texts, formulaShellText{"step " + s.ID, s.Description})
	}
	for _, s := range f.Template {
		texts = append(texts, formulaShellText{"template " + s.ID, s.Description})
	}
	for _, l := range f.Legs {
		texts = append(texts, formulaShellText{"leg " + l.ID, l.Description})
	}
	for _, a := range f.Aspects {
		texts = append(texts, formulaShellText{"aspect " + a.ID, a.Description})
	}
	if f.Synthesis != nil {
		texts = append(texts, formulaShellText{"synthesis", f.Synthesis.Description})
	}
	return texts
}

func embeddedFormula(t *testing.T, name string) *formula.Formula {
	t.Helper()
	data, err := formulasFS.ReadFile(filepath.Join(formulaEmbedDir, name))
	if err != nil {
		t.Fatalf("read embedded formula %s: %v", name, err)
	}
	f, err := formula.Parse(data)
	if err != nil {
		t.Fatalf("parse embedded formula %s: %v", name, err)
	}
	return f
}

func TestFormulaStepShell_RejectsDefectClass(t *testing.T) {
	t.Run("every shipped formula is clean", func(t *testing.T) {
		entries, err := formulasFS.ReadDir(formulaEmbedDir)
		if err != nil {
			t.Fatalf("read embedded %s: %v", formulaEmbedDir, err)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
				continue
			}
			for _, tx := range formulaShellTexts(embeddedFormula(t, e.Name())) {
				for _, finding := range checkShellText(tx.text) {
					t.Errorf("%s [%s] %s\n\t%s", e.Name(), tx.where, finding, stepShellRule)
				}
			}
		}
	})

	fixtures, _ := filepath.Glob("testdata/formula_step_shell/must_flag/*.md")
	if len(fixtures) == 0 {
		t.Fatal("no must_flag fixtures found — the check would be proven against nothing")
	}
	expectRe := regexp.MustCompile(`^<!-- expect: ([A-Z ]+) -->`)
	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			m := expectRe.FindStringSubmatch(string(data))
			if m == nil {
				t.Fatalf("fixture must start with <!-- expect: RULES -->")
			}
			findings := checkShellText(string(data))
			for _, rule := range strings.Fields(m[1]) {
				if !hasRuleFinding(findings, rule) {
					t.Errorf("rule %s did not fire (the check is blind to this shape); findings: %q", rule, findings)
				}
			}
		})
	}
}

func hasRuleFinding(findings []string, rule string) bool {
	for _, f := range findings {
		if strings.Contains(f, ": "+rule+": ") {
			return true
		}
	}
	return false
}

func TestFormulaStepShell_NoFalsePositivesOnIntentionalConstructs(t *testing.T) {
	fixtures, _ := filepath.Glob("testdata/formula_step_shell/must_not_flag/*.md")
	if len(fixtures) == 0 {
		t.Fatal("no must_not_flag fixtures found")
	}
	for _, path := range fixtures {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if findings := checkShellText(string(data)); len(findings) > 0 {
				t.Errorf("false positive on an intentional construct: %q", findings)
			}
		})
	}
}

const (
	stepShellIssueURI     = "https://github.com/o/r/issues/710"
	stepShellIssueID      = "710"
	stepShellOrchestrator = "orch-agent"
	stepShellHostileTitle = "Fix `af done` and $(af mail send x -s y) in \"quoted\" dispatch it's broken"
)

// slingExpandedStep returns a step's text as af sling hands it to the agent: inputs merged into vars,
// resolved, the orchestrator injected, one {{name}} pass. Deferred vars stay literal. It omits sling's
// {{default_branch}} injection, which none of the steps read here use.
func slingExpandedStep(t *testing.T, file, stepID string) string {
	t.Helper()
	f := embeddedFormula(t, file)
	merged, err := formula.MergeInputsToVars(f.Inputs, f.Vars)
	if err != nil {
		t.Fatal(err)
	}
	vars, err := formula.ResolveVars(merged, formula.ResolveContext{CLIArgs: map[string]string{"issue_uri": stepShellIssueURI}})
	if err != nil {
		t.Fatal(err)
	}
	vars["orchestrator"] = stepShellOrchestrator
	for _, s := range f.Steps {
		if s.ID == stepID {
			return formula.ExpandTemplateVars(s.Description, vars)
		}
	}
	t.Fatalf("%s has no step %q", file, stepID)
	return ""
}

func shellBlockContaining(t *testing.T, text, marker string) string {
	t.Helper()
	var found []string
	for _, b := range fencedBlocks(text) {
		if shellFenceLangs[b.lang] && strings.Contains(b.body, marker) {
			found = append(found, b.body)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one shell block containing %q, found %d", marker, len(found))
	}
	return found[0]
}

type stepShellSandbox struct {
	root string
	log  string
	env  []string
}

type shellRun struct {
	stdout, stderr string
	exitCode       int
	calls          [][]string
}

const (
	afStub = "#!/bin/bash\nprintf '%s\\0' \"$@\" >>\"$AF_STUB_LOG\"\nprintf '\\036' >>\"$AF_STUB_LOG\"\nprintf '{\"id\":\"bd-stub\"}\\n'\n"
	jqStub = "#!/bin/bash\ncat >/dev/null\necho bd-stub\n"
)

// newStepShellSandbox runs step shell against a recording af stub. The stub must be the af bash finds:
// on a noexec TMPDIR a stub cannot run, and a PATH that still reached the real af would let the very
// `af done` this test hunts for fire for real.
func newStepShellSandbox(t *testing.T) *stepShellSandbox {
	t.Helper()
	base, err := tryExecCapableDir(t, "af-test-stepshell")
	if err != nil {
		t.Fatalf("no exec-capable directory for the af stub: %v", err)
	}
	bin, root := filepath.Join(base, "bin"), filepath.Join(base, "root")
	for _, d := range []string{bin, root} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"af": afStub, "jq": jqStub} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &stepShellSandbox{root: root, log: filepath.Join(base, "af.log")}
	s.env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + base,
		"AF_ROOT=" + root,
		"AF_WORKTREE=" + root,
		"AF_ACTOR=" + stepShellOrchestrator,
		"AF_STUB_LOG=" + s.log,
	}
	if got := strings.TrimSpace(s.run(t, "type -p af").stdout); got != filepath.Join(bin, "af") {
		t.Fatalf("af resolves to %q, not the test stub in %s — refusing to run step shell that may call the real af", got, bin)
	}
	return s
}

func (s *stepShellSandbox) run(t *testing.T, script string, env ...string) shellRun {
	t.Helper()
	if err := os.WriteFile(s.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = s.root
	cmd.Env = append(append([]string{}, s.env...), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	r := shellRun{}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run bash: %v", err)
		}
		r.exitCode = exitErr.ExitCode()
	}
	r.stdout, r.stderr = stdout.String(), stderr.String()
	data, err := os.ReadFile(s.log)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range strings.Split(string(data), "\x1e") {
		if rec != "" {
			r.calls = append(r.calls, strings.Split(strings.TrimSuffix(rec, "\x00"), "\x00"))
		}
	}
	return r
}

func (r shellRun) afCalls() string {
	var out []string
	for _, c := range r.calls {
		out = append(out, "af "+strings.Join(c[:min(len(c), 4)], " "))
	}
	return strings.Join(out, "; ")
}

func (s *stepShellSandbox) problemSummary() string {
	return filepath.Join(s.root, ".designs", stepShellIssueID, "problem-summary-"+stepShellIssueID+".md")
}

// seedRapidLedger writes formula-vars.env through parse-issue's OWN writer. A hand-written benign
// ledger is exactly what let the title-injection path escape the #710 analysis.
func (s *stepShellSandbox) seedRapidLedger(t *testing.T, issueTitle string) {
	t.Helper()
	parse := slingExpandedStep(t, "rapid-soldesign-plan.formula.toml", "parse-issue")
	script := shellBlockContaining(t, parse, `PROBLEM_SUMMARY="${AF_WORKTREE`) + "\n" +
		`printf 'problem summary\n' > "$PROBLEM_SUMMARY"` + "\n" +
		shellBlockContaining(t, parse, `'^issue_title='`)
	r := s.run(t, script, "ISSUE_NUMBER="+stepShellIssueID, "ISSUE_TITLE="+issueTitle)
	if r.exitCode != 0 || len(r.calls) != 0 {
		t.Fatalf("seeding the ledger via parse-issue: exit %d, af calls [%s], stderr %q", r.exitCode, r.afCalls(), r.stderr)
	}
}

type beadBlockCase struct {
	name        string
	file        string
	marker      string
	afterAction bool
	want        []string
}

var dispatchBeadBlocks = []beadBlockCase{
	{
		name: "rapid-soldesign-plan analyst", file: "rapid-soldesign-plan.formula.toml",
		marker: "ANALYST_BEAD=$(af bead create", afterAction: true,
		want: []string{
			"Use the load skill tool to load and run /rootcause-all",
			"af mail send orch-agent -s 'RAPIDSOL: ANALYSIS COMPLETE [710]' -m 'Analysis complete. Results at <absolute path to your rootcause_analysis.md>'",
			"including af done",
		},
	},
	{
		name: "rapid-soldesign-plan designer", file: "rapid-soldesign-plan.formula.toml",
		marker: "DESIGNER_BEAD=$(af bead create", afterAction: true,
		want: []string{
			"Use the load skill tool to load and run /design-v7",
			"document at .designs/710/design-doc.md",
			"af mail send orch-agent -s 'RAPIDSOL: DESIGN COMPLETE [710]' -m 'Design complete. Results at .designs/710/design-doc.md'",
			"run af done when instructed",
		},
	},
}

// rapidAction0Marker finds rapid's action 0, which resolves the problem summary both bead descriptions
// embed; the formula runs it in the same shell as the bead blocks.
const rapidAction0Marker = `PROBLEM_SUMMARY="${problem_summary:-}"`

func (c beadBlockCase) run(t *testing.T, s *stepShellSandbox) (string, shellRun) {
	t.Helper()
	step := slingExpandedStep(t, c.file, "dispatch-parallel")
	block := shellBlockContaining(t, step, c.marker)
	script := block
	if c.afterAction {
		s.seedRapidLedger(t, "Widget title")
		script = shellBlockContaining(t, step, rapidAction0Marker) + "\n" + block
	}
	return block, s.run(t, script)
}

func afFlagValue(call []string, flag string) string {
	for i, a := range call {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
		if a == flag && i+1 < len(call) {
			return call[i+1]
		}
	}
	return ""
}

func isBeadCreate(call []string) bool {
	return len(call) >= 2 && call[0] == "bead" && call[1] == "create"
}

func TestDispatchParallel_BeadBlockCreatesExactlyOneBeadAndNothingElse(t *testing.T) {
	for _, c := range dispatchBeadBlocks {
		t.Run(c.name, func(t *testing.T) {
			block, r := c.run(t, newStepShellSandbox(t))
			if msg := bashSyntaxError(block); msg != "" {
				t.Errorf("bash -n: %s", msg)
			}
			if r.exitCode != 0 || r.stderr != "" {
				t.Errorf("exit %d, stderr %q — the block executed part of its own text", r.exitCode, r.stderr)
			}
			if len(r.calls) != 1 || !isBeadCreate(r.calls[0]) {
				t.Errorf("want exactly one `af bead create`, got %d af calls: [%s]", len(r.calls), r.afCalls())
			}
		})
	}
}

func TestDispatchParallel_BeadCarriesCompleteSubAgentInstructions(t *testing.T) {
	for _, c := range dispatchBeadBlocks {
		t.Run(c.name, func(t *testing.T) {
			s := newStepShellSandbox(t)
			_, r := c.run(t, s)
			if len(r.calls) == 0 || !isBeadCreate(r.calls[len(r.calls)-1]) {
				t.Fatalf("no bead was created; af calls [%s], stderr %q", r.afCalls(), r.stderr)
			}
			desc := afFlagValue(r.calls[len(r.calls)-1], "--description")
			want := append([]string{}, c.want...)
			if c.afterAction {
				want = append(want, s.problemSummary())
			}
			for _, w := range want {
				if !strings.Contains(desc, w) {
					t.Errorf("description lacks %q", w)
				}
			}
			for _, bad := range []string{"$AF_ACTOR", "{{"} {
				if strings.Contains(desc, bad) {
					t.Errorf("description carries unexpanded %q — the sub-agent would resolve it itself", bad)
				}
			}
			if t.Failed() {
				t.Logf("stored description:\n%s", desc)
			}
		})
	}
}

func TestDispatchParallel_HostileIssueTitleInLedgerIsDataNeverCode(t *testing.T) {
	s := newStepShellSandbox(t)
	s.seedRapidLedger(t, stepShellHostileTitle)
	step := slingExpandedStep(t, "rapid-soldesign-plan.formula.toml", "dispatch-parallel")
	script := strings.Join([]string{
		shellBlockContaining(t, step, rapidAction0Marker),
		shellBlockContaining(t, step, "ANALYST_BEAD=$(af bead create"),
		shellBlockContaining(t, step, "DESIGNER_BEAD=$(af bead create"),
	}, "\n")
	r := s.run(t, script)
	if r.exitCode != 0 {
		t.Errorf("actions 0+1+2 exited %d, stderr %q", r.exitCode, r.stderr)
	}
	if len(r.calls) != 2 || !isBeadCreate(r.calls[0]) || !isBeadCreate(r.calls[1]) {
		t.Fatalf("want exactly two `af bead create` calls, got [%s] — the issue title ran as shell", r.afCalls())
	}
	for i, prefix := range []string{"Analyst: rootcause-all for ", "Designer: design-v7 for "} {
		if got := afFlagValue(r.calls[i], "--title"); got != prefix+stepShellHostileTitle {
			t.Errorf("bead title = %q, want the issue title verbatim: %q", got, prefix+stepShellHostileTitle)
		}
	}
}

func TestDispatchParallel_Actions0To5InOneShellDispatchAgainstRealIssueID(t *testing.T) {
	s := newStepShellSandbox(t)
	s.seedRapidLedger(t, stepShellHostileTitle)
	step := slingExpandedStep(t, "rapid-soldesign-plan.formula.toml", "dispatch-parallel")
	script := strings.Join([]string{
		shellBlockContaining(t, step, rapidAction0Marker),
		shellBlockContaining(t, step, "ANALYST_BEAD=$(af bead create"),
		shellBlockContaining(t, step, "DESIGNER_BEAD=$(af bead create"),
		shellBlockContaining(t, step, "GATE_BEAD=$(af bead create"),
		shellBlockContaining(t, step, `af bead dep "$ANALYST_BEAD" "$GATE_BEAD"`),
		shellBlockContaining(t, step, "af sling --agent"),
	}, "\n")
	r := s.run(t, script)
	if r.exitCode != 0 || r.stderr != "" {
		t.Errorf("actions 0-5 exited %d, stderr %q", r.exitCode, r.stderr)
	}
	slung := 0
	for _, c := range r.calls {
		switch c[0] {
		case "done", "mail":
			t.Errorf("dispatch-parallel ran `af %s` in the orchestrator's shell: [%s]", c[0], r.afCalls())
		case "sling":
			slung++
			if got := afFlagValue(c, "--var"); got != "issue="+stepShellIssueID {
				t.Errorf("af sling --var = %q, want issue=%s", got, stepShellIssueID)
			}
		}
	}
	if slung != 2 {
		t.Errorf("want both agents slung, got %d sling calls: [%s]", slung, r.afCalls())
	}
}

// A fresh shell is the hazard: action 4 there runs `af bead dep "" ""`, whose failure handler mails the
// orchestrator a false ABORT, and action 5 slings agents with an empty task.
func TestDispatchParallel_ActionRunOutsideTheOneShellRefuses(t *testing.T) {
	step := slingExpandedStep(t, "rapid-soldesign-plan.formula.toml", "dispatch-parallel")
	for _, c := range []struct {
		name, marker string
		env          []string
	}{
		{"action 1 in a fresh shell", "ANALYST_BEAD=$(af bead create", nil},
		{"action 2 in a fresh shell", "DESIGNER_BEAD=$(af bead create", nil},
		{"action 4 in a fresh shell", `af bead dep "$ANALYST_BEAD" "$GATE_BEAD"`, nil},
		{"action 4 without action 3", `af bead dep "$ANALYST_BEAD" "$GATE_BEAD"`, []string{"ANALYST_BEAD=bd-a"}},
		{"action 5 in a fresh shell", "af sling --agent", nil},
		{"action 5 without action 0", "af sling --agent", []string{"ANALYST_BEAD=bd-a", "DESIGNER_BEAD=bd-d"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newStepShellSandbox(t).run(t, shellBlockContaining(t, step, c.marker), c.env...)
			if r.exitCode == 0 || !strings.Contains(r.stdout, "CRITICAL") {
				t.Errorf("want a non-zero CRITICAL stop; exit %d, stdout %q, stderr %q", r.exitCode, r.stdout, r.stderr)
			}
			if len(r.calls) != 0 {
				t.Errorf("af must not run without the variables earlier actions set, got [%s]", r.afCalls())
			}
		})
	}
}

type formulaStep struct{ file, step string }

var commandBlockSteps = []formulaStep{
	{"rapid-soldesign-plan.formula.toml", "dispatch-parallel"},
}

func TestDispatchParallel_EachCommandBlockStaysIntact(t *testing.T) {
	for _, st := range commandBlockSteps {
		for _, b := range fencedBlocks(slingExpandedStep(t, st.file, st.step)) {
			if !shellFenceLangs[b.lang] {
				t.Errorf("%s [%s] line %d: a stray fence opened a %q block — a broken fence upstream split a command block", st.file, st.step, b.line, b.lang)
				continue
			}
			for _, ln := range strings.Split(b.body, "\n") {
				if m := fenceOpenRe.FindStringSubmatch(ln); m != nil && m[3] != "" {
					t.Errorf("%s [%s] line %d: nested opening fence %q inside a command block", st.file, st.step, b.line, strings.TrimSpace(ln))
				}
			}
		}
	}
}
