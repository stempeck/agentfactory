package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func dcReadRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

type dcDocSection struct {
	heading string
	line    int
	body    string
}

func dcHeadingLevel(line string) int {
	n := len(line) - len(strings.TrimLeft(line, "#"))
	if n == 0 || n > 6 || !strings.HasPrefix(line[n:], " ") {
		return 0
	}
	return n
}

// dcSections splits a markdown document into its sections of one heading level. A fenced code
// block never ends a section: a `# comment` in a shell example is not a heading.
func dcSections(doc string, level int) []dcDocSection {
	var out []dcDocSection
	var cur *dcDocSection
	var lines []string
	flush := func() {
		if cur != nil {
			cur.body = strings.Join(lines, "\n")
			out = append(out, *cur)
			cur = nil
		}
	}
	fenced := false
	for i, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if lv := dcHeadingLevel(line); !fenced && lv > 0 && lv <= level {
			flush()
			if lv == level {
				cur = &dcDocSection{heading: line, line: i + 1}
				lines = nil
			}
		}
		if cur != nil {
			lines = append(lines, line)
		}
	}
	flush()
	return out
}

func dcSectionTitled(doc, heading string) (string, bool) {
	for _, s := range dcSections(doc, dcHeadingLevel(heading)) {
		if s.heading == heading {
			return s.body, true
		}
	}
	return "", false
}

func dcFlat(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

const dcUsingBatchHeading = "### Batch regeneration with `af install --agents`"

// TestUsingAgentfactoryDocConvergenceContract pins PR #747 T04 (L-7) and BODY-5 (L-16): the operator
// guide must say what every redeploy now does to the container — converge, record, exit 1 at the end —
// and which hosts it must reach, so an offline re-run that exits 1 is a documented outcome, not a surprise.
func TestUsingAgentfactoryDocConvergenceContract(t *testing.T) {
	doc := dcReadRepoFile(t, "USING_AGENTFACTORY.md")
	raw, ok := dcSectionTitled(doc, dcUsingBatchHeading)
	if !ok {
		t.Fatalf("USING_AGENTFACTORY.md has no %q heading; the convergence contract is documented under it", dcUsingBatchHeading)
	}
	sec := dcFlat(raw)
	wantMatch := func(t *testing.T, pattern, why string) {
		t.Helper()
		if !regexp.MustCompile(pattern).MatchString(sec) {
			t.Errorf("USING_AGENTFACTORY.md's batch-regeneration section %s", why)
		}
	}
	// A sentence ends at punctuation followed by a space, so `./quickstart.sh` or `convergence.json`
	// inside one does not split it.
	sentences := regexp.MustCompile(`[.!?](\s+|$)`).Split(sec, -1)
	wantSentence := func(t *testing.T, why string, patterns ...string) {
		t.Helper()
	next:
		for _, s := range sentences {
			for _, p := range patterns {
				if !regexp.MustCompile(p).MatchString(s) {
					continue next
				}
			}
			return
		}
		t.Errorf("USING_AGENTFACTORY.md's batch-regeneration section %s", why)
	}
	const exits = `(?i)exits?( with)?( (status|code))? (1|non-zero|nonzero)\b`

	t.Run("converges installed components to their pins on every run", func(t *testing.T) {
		// One sentence carries all three: the section already says "Every redeploy resets recording", and its
		// codex sentence already pairs "pinned" with "converged".
		wantSentence(t, "never says, in one sentence, that every run converges the installed components to their pins (T04)",
			`(?i)converg`, `(?i)\bpin(s|ned)?\b`, `(?i)\b(every|each) (run|redeploy|re-?run)\b`)
	})

	t.Run("names the components it converges", func(t *testing.T) {
		for _, c := range []struct{ name, pattern string }{
			{"gh", `\bgh\b`},
			{"Go", "\\bGo\\b|`go`"},
			{"Claude Code", "(?i)claude code|`claude`"},
			{"Playwright", `(?i)\bplaywright\b`},
			{"OpenObserve", `(?i)(^|[^.\w])openobserve($|[^.\w])`},
			{"Node.js", `(?i)\bnode(\.js)?\b`},
			{"the Ubuntu security pocket", `(?i)security (pocket|updates?|packages?)|-security\b`},
			{"the Python locks", `(?i)\bpython\b`},
		} {
			wantMatch(t, c.pattern, "never names "+c.name+", which every run now converges to its pin (T04)")
		}
	})

	t.Run("writes the convergence record", func(t *testing.T) {
		wantMatch(t, `\.runtime/bootstrap/convergence\.json`, "does not name .runtime/bootstrap/convergence.json, the record every run writes (T04)")
	})

	t.Run("a failed required component exits 1 at the end", func(t *testing.T) {
		wantMatch(t, `(?i)\brequired\b`, "never says which components are `required` — the ones whose failure fails the run (T04)")
		wantSentence(t, "never says the run exits 1 at the end when a required component did not converge (T04)", exits, `(?i)\bend\b`)
	})

	t.Run("--check is a read-only drift report", func(t *testing.T) {
		wantSentence(t, "never says `--check` is read-only (T04)", `--check\b`, `(?i)read-only|changes nothing|changing nothing|without changing`)
		wantMatch(t, `(?i)\bdrift\b`, "never says `--check` reports drift (T04)")
	})

	t.Run("af down && af up after a run that changed packages", func(t *testing.T) {
		wantMatch(t, `af down && af up`, "never says `af down && af up` — running agents keep the pre-converged code until they restart (T04)")
		wantSentence(t, "never ties `af down && af up` to a run that changed packages (T04)", `af down && af up`, `(?i)\bchang`)
	})

	t.Run("names the egress hosts", func(t *testing.T) {
		for _, h := range []struct{ name, pattern string }{
			{"deb.nodesource.com", `deb\.nodesource\.com`},
			{"downloads.claude.ai", `downloads\.claude\.ai`},
			{"claude.ai", `(^|[^.\w])claude\.ai\b`},
			{"go.dev", `(^|[^.\w])go\.dev\b`},
			{"github.com", `(^|[^.\w])github\.com\b`},
			{"downloads.openobserve.ai", `downloads\.openobserve\.ai`},
			{"PyPI", `(?i)\bpypi\b`},
			{"the Ubuntu mirror configured in /etc/apt/sources", `/etc/apt/sources`},
			{"the npm registry", `(?i)\bnpm\b`},
			{"the Go module proxy", `(?i)module proxy`},
		} {
			wantMatch(t, h.pattern, "never names "+h.name+", which a converging run must reach (T04, BODY-5)")
		}
	})

	t.Run("an offline re-run exits 1 and the recovery is restore egress and re-run", func(t *testing.T) {
		const offline = `(?i)offline|without (network|egress)|no (network|egress)`
		wantMatch(t, `(?i)\begress\b`, "never states the egress requirement (BODY-5)")
		wantSentence(t, "never says a re-run without network exits 1 (BODY-5)", offline, exits)
		wantSentence(t, "never gives the recovery \"restore egress and re-run\" (BODY-5)", `(?i)restore egress`, `(?i)\bre-?run\b`)
	})

	t.Run("names what the root layer changes outside the user's home", func(t *testing.T) {
		wantSentence(t, "never says the apt gh is removed and /usr/local/bin/gh links the pinned build (T04, BODY-2)",
			`(?i)\bapt\b`, `\bgh\b`, `(?i)remov`, `/usr/local/bin/gh`)
		wantSentence(t, "never says a root run takes /usr/bin/gh from every user (T04, BODY-2)",
			`(?i)\broot\b`, `/usr/bin/gh`, `(?i)every user|other users|all users`)
		wantSentence(t, "never says the github-cli apt source is renamed .disabled (T04, T15)",
			`(?i)github-cli`, `\.disabled\b`)
		wantSentence(t, "never says a managed-settings drop-in turns Claude Code auto-update off machine-wide (T04, BODY-1)",
			`/etc/claude-code/managed-settings\.d`, `(?i)auto-?updat`)
	})

	t.Run("never names cli.github.com", func(t *testing.T) {
		// The github-cli apt source is disabled, so naming its host would document egress nothing uses.
		if strings.Contains(sec, "cli.github.com") {
			t.Error("USING_AGENTFACTORY.md's batch-regeneration section names cli.github.com, a source the converging run no longer reads")
		}
	})
}
