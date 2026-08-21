package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/formula"
)

// T-REALITY (AC-626-5). Every conformance test in this tree before it was closed-world: text
// checked against text, or text checked against a denylist of phrases someone remembered to
// write down. That shape cannot catch the defect this issue exists for — a capability being
// dropped, renamed, or never built while the instruction naming it ships on. Both halves stay
// internally consistent and nothing turns red.
//
// This test is open-world: it reads the `af …` references out of the instruction text we actually
// ship and resolves each one against the live cobra tree. Delete a command and the prose that
// names it reds here; write prose naming a verb that was never built and it reds here too.
//
// # Extraction scope — this choice IS the test's contract
//
// Templates are scanned as FULL TEXT, prose included, not just backticked code spans. Scoping to
// code spans would be the comfortable choice and it is the wrong one: the two genuinely dead
// references on this tree at the time of writing were both prose ("the af mail inject hook
// delivers it on wake"), and an agent reading its own role template does not know which sentences
// the authors marked as code. If the text tells an agent to run something, the something has to
// exist.
//
// Formulas are scanned as PARSED text rather than raw bytes, because a TOML comment is dropped by
// the parser and reaches no agent. Raw-byte scanning would hold a formula to words it does not
// ship.
//
// install.go and recovery.go are scanned as EXACT LITERALS — the scaffold value and the two
// advisory consts — rather than as whole source files, because those files are Go, and Go
// comments discussing verbs are not instructions delivered to anyone.
//
// # The predicate, and what it deliberately does not catch
//
// A verb path fails when cobra cannot find its head at all, or when a leftover word is being
// offered to a command that has no Run of its own — i.e. a group like `af mail` or `af memory`,
// where a trailing word can only ever have been meant as a subcommand. It PASSES when leftovers
// trail a runnable leaf, because that is what an English sentence looks like: "use af handoff to
// cycle to a fresh session" leaves "to cycle to" behind and means nothing by it. The cost of that
// concession is a blind spot for garbage trailing a runnable leaf; the benefit is that the test
// can be pointed at prose at all, which is where the real defects were.
//
// Flags get no such concession — see resolveAFRef. English does not emit `--inject` by accident,
// so an unknown flag is always a defect, and it is the half of this defect class that verb
// resolution alone cannot see: renaming a flag leaves every surface that names it lying while the
// verb path still resolves.
//
// Reaching them takes some care, because a flag is usually not the token right after the verb.
// scanFlags therefore walks PAST arguments — `<id>`, quoted strings, `commit:<sha>` — and stops
// only where the invocation genuinely ends: a pipe, a separator, a redirect, a closing code span,
// or a `$(…)` that hands the line to some other command. Both stopping rules were once too eager
// and quietly ate real flags: `<id>` was read as a redirect because it contains `>`, and `-M` was
// read as prose because the flag-name spelling rule demanded lowercase. Each blind spot is now
// pinned by its own case in TestInstructionReality_RejectsVerbsThatDoNotExist.
//
// The one thing the gate cannot distinguish is instruction text ABOUT a command that does not
// exist, from instruction text TELLING you to run one. There is exactly one such passage in the
// tree — design-plan-impl's GATE 0, which teaches reviewers that flags are the common miss — and
// it is phrased so its examples are not spelled as invocations. That is the right resolution:
// prose that looks runnable should be runnable, and an allowlist of excused sentences would
// rebuild the closed world this test exists to escape.

const (
	// Deepest real path in the tree is three (`af config models check`), so a fourth word is
	// never a verb and reading one can only manufacture false failures.
	maxVerbDepth = 3

	roleTemplateDir = "../templates/roles"
	formulaEmbedDir = "install_formulas"
)

type afRef struct {
	surface string
	line    int
	path    []string
	flags   []string
}

func (r afRef) String() string {
	s := fmt.Sprintf("%s:%d: af %s", r.surface, r.line, strings.Join(r.path, " "))
	if len(r.flags) > 0 {
		s += " " + strings.Join(r.flags, " ")
	}
	return s
}

func TestInstructionReality_ShippedTextNamesRealVerbs(t *testing.T) {
	refs := shippedInstructionRefs(t)

	for _, ref := range refs {
		if err := resolveAFRef(ref); err != nil {
			t.Errorf("%s — %v\n"+
				"shipped instruction text names a verb the cobra tree does not have. Either the "+
				"command was removed/renamed and the text was left behind, or the text describes "+
				"something that was never built. Fix the text or build the verb; do not relax "+
				"this test.", ref, err)
		}
	}
}

// The gate is only worth its runtime if it can fail, and a text-scanning test is exactly the kind
// that quietly stops matching anything and passes forever. This runs the real extractor and the
// real resolver over text carrying one bogus verb per shape the predicate claims to catch.
func TestInstructionReality_RejectsVerbsThatDoNotExist(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"unknown root verb", "Run `af frobnicate` when you are done."},
		{"unknown subcommand of a group", "Externalize with `af memory frobnicate`."},
		{"unknown subcommand in prose", "the af mail inject hook delivers it on wake"},
		{"renamed verb still named in prose", "call af config frobnicate to set it"},
		{"renamed long flag on a real verb", "Your notes are served by `af memory check --inject-all`."},
		{"unknown long flag on a nested verb", "Run `af config models show --frobnicate`."},
		{"unknown shorthand on a real verb", "Record it with `af memory add -z \"subject\"`."},
		{"flag survived a verb rename", "Regenerate with `af formula agent-gen --frobnicate`."},
		{"uppercase shorthand", "Record it with `af memory add -M \"<what you learned>\"`."},
		{"flag behind a placeholder", "Close it with `af memory graduate <id> --frobnicate commit:<sha>`."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs := extractAFRefs("synthetic", tc.text)
			if len(refs) == 0 {
				t.Fatalf("extractor found no af reference in %q — it would be blind to this "+
					"defect in real text", tc.text)
			}
			var failed bool
			for _, ref := range refs {
				if resolveAFRef(ref) != nil {
					failed = true
				}
			}
			if !failed {
				t.Errorf("gate accepted %q; T-REALITY cannot catch the class it exists for", tc.text)
			}
		})
	}
}

// The mirror of the negative test: prose that mentions a real verb and then keeps talking must
// not red. These are verbatim lines from the shipped templates — the false positives an
// over-eager extractor would produce, and the reason the predicate is written the way it is.
func TestInstructionReality_AcceptsProseAroundRealVerbs(t *testing.T) {
	lines := []string{
		"| Context filling up | Use af handoff to cycle to fresh session |",
		"or af down commands. The Agent tool produces ephemeral sub-agents",
		"never calls af done until step 10",
		"premature af done detection",
		"The human will `af up scenario` + `af attach scenario`",
		"Read your memory (af memory list) and docs, and prove it.",
		"Record it with `af memory add -s \"<subject>\" -m \"<body>\" --type gotcha`.",
		"`af memory check --inject` already serves your own notes at session start.",
		"Check your inbox once: `af mail inbox --json`.",
		"a `--root` flag on `af install`, a `--dry-run` on `af up`: none exist",
		"Close the loop: `af memory graduate <id> --to commit:<sha>`.",
		"Check quietly with `af mail inbox --json 2>/dev/null | jq '.[].id'`.",
	}

	for _, line := range lines {
		refs := extractAFRefs("synthetic", line)
		if len(refs) == 0 {
			t.Errorf("extractor found nothing in %q — it should still see the verb", line)
			continue
		}
		for _, ref := range refs {
			if err := resolveAFRef(ref); err != nil {
				t.Errorf("false positive on shipped prose %q: %s — %v", line, ref, err)
			}
		}
	}
}

// shippedInstructionRefs collects the five surfaces and refuses to return a thin haul. A scanner
// that silently stops matching is the failure mode this whole file is guarding against, so the
// floors are asserted per surface rather than on the total: a total-only guard stays green while
// one surface goes dark.
//
// Each floor is a non-vacuity floor, deliberately well under the measured count, not the count
// itself. A floor pinned to today's number is a second, undeclared drift test — it fails on a
// legitimate reword with a message accusing the extractor, which sends the next reader hunting a
// bug that is not there. What matters here is only that the surface is still being read at all.
func shippedInstructionRefs(t *testing.T) []afRef {
	t.Helper()

	surfaces := []struct {
		name  string
		refs  []afRef
		floor int
	}{
		{"install.go agents.json directive", extractAFRefs("install.go", scaffoldDirectiveText(t)), 1},
		{"recovery.go CONTEXT ADVISORY", extractAFRefs("recovery.go",
			contextAdvisorySubject+"\n"+contextAdvisoryBody), 1},
		{"role templates", roleTemplateRefs(t), 100},
		{"formula TOMLs", formulaRefs(t), 100},
		// The improvement hook's instruction (#483, wired to the vault by #515 Phase 5). It is
		// the only surface here that is FORMATTED before it ships, so it is registered rendered:
		// scanning the raw template would hand the resolver `af formula show %s --json` and prove
		// nothing about the text an agent actually receives.
		{"improvement instruction", extractAFRefs("improvement.go", renderedImprovementInstruction()), 3},
	}

	var all []afRef
	for _, s := range surfaces {
		if len(s.refs) < s.floor {
			t.Fatalf("surface %q yielded %d af references, expected at least %d — either this "+
				"surface stopped naming any af command at all, or the extractor has gone blind "+
				"to it and this gate is now vacuous", s.name, len(s.refs), s.floor)
		}
		all = append(all, s.refs...)
	}
	return all
}

// renderedImprovementInstruction substitutes the template exactly as improvementInstruction does,
// with a formula name and an absolute store path standing in for a real one.
func renderedImprovementInstruction() string {
	return fmt.Sprintf(improvementInstructionTemplate,
		"example", "/factory/.agentfactory/store/formulas/example.formula.toml", "example", "example")
}

// scaffoldDirectiveText reads the REAL seeded agents.json literal out of install.go rather than a
// copy of it, so a rewrite of the directive is scanned on the next run without anyone updating
// this test.
func scaffoldDirectiveText(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	return extractScaffoldLiteral(t, string(src), `"agents.json":`)
}

func roleTemplateRefs(t *testing.T) []afRef {
	t.Helper()
	entries, err := os.ReadDir(roleTemplateDir)
	if err != nil {
		t.Fatalf("read %s: %v", roleTemplateDir, err)
	}
	var refs []afRef
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md.tmpl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(roleTemplateDir, e.Name()))
		if err != nil {
			t.Fatalf("read template %s: %v", e.Name(), err)
		}
		refs = append(refs, extractAFRefs(e.Name(), string(data))...)
	}
	return refs
}

// formulaRefs scans each formula's PARSED text, not its raw bytes. The difference is not cosmetic:
// a TOML comment is dropped by the parser and reaches no agent, so it is not instruction text, and
// scanning it produces failures on prose that is deliberately ABOUT commands that do not exist —
// design-plan-impl.formula.toml has a comment listing `af install --root`, `af up --dry-run` and
// `af config models show --json` as examples of flags to watch out for. Holding a formula to what
// it actually ships is both stricter and quieter.
func formulaRefs(t *testing.T) []afRef {
	t.Helper()
	entries, err := formulasFS.ReadDir(formulaEmbedDir)
	if err != nil {
		t.Fatalf("read embedded %s: %v", formulaEmbedDir, err)
	}
	var refs []afRef
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
			continue
		}
		data, err := formulasFS.ReadFile(filepath.Join(formulaEmbedDir, e.Name()))
		if err != nil {
			t.Fatalf("read embedded formula %s: %v", e.Name(), err)
		}
		f, err := formula.Parse(data)
		if err != nil {
			t.Fatalf("parse embedded formula %s: %v", e.Name(), err)
		}
		refs = append(refs, extractAFRefs(e.Name(), formulaInstructionText(f))...)
	}
	return refs
}

// formulaInstructionText joins every string a formula carries, found by reflection rather than by
// naming the fields. A hand-written field list is a roster, and a roster stops covering the next
// field someone adds — the same reason the template sweep globs instead of listing 42 names.
func formulaInstructionText(f *formula.Formula) string {
	var parts []string
	collectStrings(reflect.ValueOf(f), &parts)
	return strings.Join(parts, "\n")
}

func collectStrings(v reflect.Value, out *[]string) {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !v.IsNil() {
			collectStrings(v.Elem(), out)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			collectStrings(v.Field(i), out)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			collectStrings(v.Index(i), out)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			collectStrings(v.MapIndex(k), out)
		}
	case reflect.String:
		if s := v.String(); s != "" {
			*out = append(*out, s)
		}
	}
}

// resolveAFRef asks the live command tree whether the reference is reachable — first the verb
// path, then every flag spelled on it. See the predicate note in the file header for why leftover
// WORDS are tolerated behind a runnable leaf and not behind a group.
//
// Flags get no such tolerance. A flag token is unambiguous — English does not produce `--inject`
// by accident — so an unknown one is always a defect, and it is the defect this gate would
// otherwise miss entirely: rename `af memory check --inject` to `--serve` and, without this check,
// 42 templates plus the advisory plus the SessionStart hook keep naming a flag that is gone while
// the verb path still resolves and the gate stays green.
func resolveAFRef(ref afRef) error {
	c, rest, err := rootCmd.Find(ref.path)
	if err != nil {
		return err
	}
	if len(rest) > 0 && !c.Runnable() {
		return fmt.Errorf("%q is not a subcommand of %q, which has no action of its own",
			rest[0], c.CommandPath())
	}
	if len(rest) > 0 {
		// The verb path trailed off into words cobra did not recognize, so this was a sentence
		// mentioning a command, not an invocation of one — "af sling failed for the analyst"
		// leaves "failed for" behind. Anything flag-shaped further along that line belongs to
		// whatever the sentence went on to talk about, and holding af to it manufactures
		// failures out of English.
		return nil
	}
	for _, f := range ref.flags {
		if !hasFlag(c, f) {
			return fmt.Errorf("%q is not a flag of %q", f, c.CommandPath())
		}
	}
	return nil
}

// hasFlag consults the command's own flags plus everything it inherits, because a template is
// free to spell a persistent flag on the leaf that accepts it.
func hasFlag(c *cobra.Command, flag string) bool {
	name := strings.TrimLeft(flag, "-")
	if strings.HasPrefix(flag, "--") {
		return c.Flags().Lookup(name) != nil || c.InheritedFlags().Lookup(name) != nil
	}
	return c.Flags().ShorthandLookup(name) != nil || c.InheritedFlags().ShorthandLookup(name) != nil
}

// extractAFRefs finds every `af` token in text and reads the verb path that follows it. The walk
// stops at the first word that cannot be a verb — a flag, a <placeholder>, a path, a capitalized
// English word — and at any word carrying trailing punctuation, since punctuation is where a
// command being quoted inside a sentence ends.
func extractAFRefs(surface, text string) []afRef {
	var refs []afRef
	line := 1
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			line++
			continue
		}
		if text[i] != 'a' || i+1 >= len(text) || text[i+1] != 'f' {
			continue
		}
		if i > 0 && isWordByte(text[i-1]) {
			continue
		}
		j := i + 2
		if j >= len(text) || !isSpaceByte(text[j]) {
			continue
		}
		if path, flags := walkVerbPath(text[j:]); len(path) > 0 {
			refs = append(refs, afRef{surface: surface, line: line, path: path, flags: flags})
		}
		i = j - 1
	}
	return refs
}

// walkVerbPath reads the verb path, then the flags spelled anywhere after it on the same line.
//
// The two phases are separate because a real invocation interleaves flags with positional
// arguments: `af memory add -s "<subject>" -m "<body>" --type gotcha` puts two arguments between
// three flags. A single walk that gave up at the first non-flag token would collect `-s` and stop,
// leaving `-m` and `--type` unchecked — which is exactly the half-guarded state a reader of the
// file header would not expect.
//
// The line is the boundary. A command is written on one line; a paragraph that happens to mention
// a flag three sentences later is not part of the invocation.
func walkVerbPath(s string) (path, flags []string) {
	i := 0
	for len(path) < maxVerbDepth {
		tok, next, ok := nextLineToken(s, i)
		if !ok {
			return path, nil
		}
		trimmed, closed := trimTokenTail(tok)
		if !isVerbWord(trimmed) {
			break
		}
		path = append(path, trimmed)
		i = next
		if closed {
			return path, nil
		}
	}
	if len(path) == 0 {
		return nil, nil
	}
	return path, scanFlags(s, i)
}

// scanFlags reads the flags of one invocation: it keeps flag tokens, steps over the arguments
// between them, and stops the moment the invocation does.
//
// Knowing where an invocation ENDS is the whole difficulty, and getting it wrong in the greedy
// direction is what makes a text gate unusable. Three rules do it, each earned from real shipped
// text in this tree:
//
//   - Quoted arguments are skipped whole. `af mail send -s "BLOCKED: $(git branch
//     --show-current)" -m "…"` must yield -s and -m, not --show-current: the words inside a quoted
//     message belong to the message.
//   - A shell operator ends it. `af config build-host --status | awk '/^host:/{print $2}'` must
//     not attribute awk's arguments, nor `grep -q`'s, to af.
//   - A plain English word ends it, because a sentence has resumed: "Use af handoff to cycle to a
//     fresh session" is prose, and anything flag-shaped later in that sentence belongs to
//     something else.
func scanFlags(s string, i int) []string {
	var flags []string
	var quote byte
	for {
		tok, next, ok := nextLineToken(s, i)
		if !ok {
			return flags
		}
		i = next

		if quote != 0 {
			if tok[len(tok)-1] == quote {
				quote = 0
			}
			continue
		}
		// A command substitution hands the rest of the line to an inner command, and the inner
		// command's flags are not this one's: `af sling --agent X --reset "$(af bead show B
		// --json)"` must yield --reset, not --json. The inner invocation is not lost — the
		// extractor finds its own `af` and checks it as a reference in its own right. This is
		// tested before the quote check on purpose, and the quote check before it in the loop,
		// so a $( ) INSIDE an already-open quoted argument stays part of that argument.
		if strings.Contains(tok, "$(") {
			return flags
		}
		if q := tok[0]; q == '"' || q == '\'' {
			if len(tok) == 1 || tok[len(tok)-1] != q {
				quote = q
			}
			continue
		}
		if endsInvocation(tok) {
			return flags
		}

		trimmed, _ := trimTokenTail(tok)
		if flag, isFlag := asFlagToken(trimmed); isFlag {
			flags = append(flags, flag)
			if strings.ContainsRune(tok, '`') {
				return flags
			}
			continue
		}
		if isVerbWord(trimmed) || strings.ContainsRune(tok, '`') {
			return flags
		}
	}
}

// endsInvocation reports whether a token hands the rest of the line to something other than af —
// a pipeline, a separator, or a redirect.
//
// The `<` exemption is load-bearing: a redirect and a placeholder both carry `>`, and `<id>`,
// `<sha>` and `<subject>` are how every command in this tree is written down. Treating them as
// redirects ended the scan before the flags that follow them, which is precisely how `--to` went
// unchecked while the file header claimed otherwise.
func endsInvocation(tok string) bool {
	if tok[0] == '<' {
		return false
	}
	return strings.ContainsAny(tok, "|;>&")
}

// nextLineToken returns the next whitespace-delimited token at or after i, refusing to cross a
// newline.
func nextLineToken(s string, i int) (tok string, next int, ok bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	if i >= len(s) || s[i] == '\n' {
		return "", i, false
	}
	start := i
	for i < len(s) && !isSpaceByte(s[i]) {
		i++
	}
	return s[start:i], i, true
}

// trimTokenTail strips the punctuation an author puts after a command inside a sentence, and
// reports whether any was found — trailing punctuation is where a quoted command ends.
func trimTokenTail(tok string) (trimmed string, closed bool) {
	trimmed = strings.TrimRight(tok, "`,;:.?!)]}\"'*")
	return trimmed, len(trimmed) != len(tok)
}

// asFlagToken normalizes a `--long=value`, `--long`, or `-s` token to the bare flag name. It
// rejects anything that is not spelled like a flag, including the bare `-` and `--` separators
// and the `-1`-style negative numbers that turn up in prose.
func asFlagToken(tok string) (string, bool) {
	if len(tok) < 2 || tok[0] != '-' {
		return "", false
	}
	name := strings.TrimLeft(tok, "-")
	if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	if !isFlagName(name) {
		return "", false
	}
	if strings.HasPrefix(tok, "--") {
		return "--" + name, true
	}
	// Shorthands are single runes; `-abc` in prose is not a flag this tree spells.
	if len(name) != 1 {
		return "", false
	}
	return "-" + name, true
}

// isFlagName is deliberately looser than isVerbWord: a flag that does not exist is exactly what
// this test is here to catch, so an odd spelling must reach the lookup and be REPORTED rather than
// be quietly filtered out as prose. Holding flags to the verb spelling silently excused every
// uppercase shorthand — `-M` and `-S` failed isVerbWord's lowercase-first-byte rule and so were
// skipped as ordinary words, which is how renaming `-m` and `-s` in a shipped template left the
// suite green.
func isFlagName(s string) bool {
	if s == "" || !isLetterByte(s[0]) {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isLetterByte(c) && !(c >= '0' && c <= '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func isLetterByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isVerbWord matches how cobra verbs are actually spelled in this tree: lowercase, hyphenated,
// nothing else. It is the filter that keeps English out — "Use", "commands.", "--json",
// "<agent>", and "~/.local/bin" all fail it.
func isVerbWord(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			continue
		}
		return false
	}
	return true
}

func isWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '_' || c == '-'
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
