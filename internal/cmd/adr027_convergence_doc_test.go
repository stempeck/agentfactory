package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestADR027ConvergenceContract pins PR #747 BODY-3 (L-8): ADR-026 lets a control be weakened only
// against a decision record that names it, so the bootstrap convergence controls need one before the
// next change can relax them unopposed.
func TestADR027ConvergenceContract(t *testing.T) {
	adrDir := filepath.Join(findModuleRoot(t), "docs", "architecture", "adrs")
	adrExists := func(t *testing.T, name string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(adrDir, name)); err != nil {
			t.Errorf("the link target docs/architecture/adrs/%s does not exist: %v", name, err)
		}
	}

	t.Run("ADR-027 records the convergence control set", func(t *testing.T) {
		matches, err := filepath.Glob(filepath.Join(adrDir, "ADR-027-*.md"))
		if err != nil {
			t.Fatalf("glob ADR-027: %v", err)
		}
		if len(matches) != 1 {
			t.Fatalf("want exactly one docs/architecture/adrs/ADR-027-*.md (the bootstrap convergence contract), got %d: %v", len(matches), matches)
		}
		data, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatalf("read %s: %v", matches[0], err)
		}
		raw := string(data)
		if !regexp.MustCompile(`(?m)^\*\*Status:\*\* Proposed`).MatchString(raw) {
			t.Error("ADR-027 must carry a `**Status:** Proposed` line: the operator has not ratified it")
		}
		for _, h := range []string{"## Context", "## Decision", "## Consequences"} {
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(h) + `\s*$`).MatchString(raw) {
				t.Errorf("ADR-027 has no %q heading (the Nygard shape every ADR here uses)", h)
			}
		}
		for _, want := range []string{"_converge", "CONVERGE_FAILURES", "convergence.json", "advisory-exceptions.json", "advisory-decay", "--check", "ADR-026"} {
			if !strings.Contains(raw, want) {
				t.Errorf("ADR-027 never names %q (BODY-3)", want)
			}
		}
		if !regexp.MustCompile(`(?i)\bhash`).MatchString(raw) {
			t.Error("ADR-027 never names the hash locks")
		}
		if !regexp.MustCompile(`(?i)strengthen`).MatchString(raw) {
			t.Error("ADR-027 never says the controls can only be strengthened, ADR-026's rule")
		}
		if link := regexp.MustCompile(`\]\([^)]*\.designs/[^)]*\)`).FindString(raw); link != "" {
			t.Errorf("ADR-027 links into .designs/, which the OSS repo does not carry; cite the design by path text instead: %q", link)
		}
	})

	t.Run("the ADR index lists ADR-027 as Proposed", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
		if err != nil {
			t.Fatalf("read adrs/README.md: %v", err)
		}
		var row string
		for _, l := range strings.Split(string(data), "\n") {
			if strings.Contains(l, "](ADR-027-") {
				row = l
				break
			}
		}
		if row == "" {
			t.Fatal("docs/architecture/adrs/README.md has no `](ADR-027-` index row (BODY-3)")
		}
		if !strings.Contains(row, "Proposed") {
			t.Errorf("the ADR-027 index row does not say Proposed: %q", row)
		}
		if m := regexp.MustCompile(`\]\((ADR-027-[^)\s]+\.md)\)`).FindStringSubmatch(row); m != nil {
			adrExists(t, m[1])
		} else {
			t.Errorf("the ADR-027 index row has no `](ADR-027-….md)` link: %q", row)
		}
	})

	t.Run("trust-boundaries.md links ADR-027", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(adrDir, "..", "trust-boundaries.md"))
		if err != nil {
			t.Fatalf("read trust-boundaries.md: %v", err)
		}
		m := regexp.MustCompile(`\]\(adrs/(ADR-027-[^)\s]+\.md)\)`).FindStringSubmatch(string(data))
		if m == nil {
			t.Fatal("docs/architecture/trust-boundaries.md has no markdown link to adrs/ADR-027-*.md, as it has for ADR-026 (BODY-3)")
		}
		adrExists(t, m[1])
	})

	// The anchors are found by symbol, never by number: when quickstart.sh shifts, only the docs'
	// cites need re-anchoring, and this test names the line each symbol moved to.
	t.Run("every quickstart.sh line cite lands on the symbol it names", func(t *testing.T) {
		script, err := os.ReadFile(filepath.Join(findModuleRoot(t), "quickstart.sh"))
		if err != nil {
			t.Fatalf("read quickstart.sh: %v", err)
		}
		symbolAt := adr027AnchorCites(t, strings.Split(string(script), "\n"))

		matches, err := filepath.Glob(filepath.Join(adrDir, "ADR-027-*.md"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("want exactly one docs/architecture/adrs/ADR-027-*.md, got %v (err %v)", matches, err)
		}
		adr, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatalf("read %s: %v", matches[0], err)
		}
		index, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
		if err != nil {
			t.Fatalf("read adrs/README.md: %v", err)
		}
		var row string
		for _, l := range strings.Split(string(index), "\n") {
			if strings.Contains(l, "](ADR-027-") {
				row = l
				break
			}
		}

		cited := map[string]bool{}
		for _, doc := range []struct{ name, text string }{{"ADR-027", string(adr)}, {"the ADR-027 index row", row}} {
			for _, c := range adr027QuickstartCites(doc.text) {
				cited[c] = true
				if _, ok := symbolAt[c]; !ok {
					t.Errorf("%s cites quickstart.sh:%s, which is the line of none of the symbols it names", doc.name, c)
				}
			}
		}
		var uncited []string
		for c := range symbolAt {
			if !cited[c] {
				uncited = append(uncited, c)
			}
		}
		sort.Strings(uncited)
		for _, c := range uncited {
			t.Errorf("%s is at quickstart.sh:%s, which neither ADR-027 nor its index row cites", symbolAt[c], c)
		}
	})
}

// adr027QuickstartAnchors lists what ADR-027 and its index row cite in quickstart.sh. within names
// the function whose body holds the line, for a pattern that also occurs elsewhere in the script;
// end, when set, is a range's last line: its first match after start.
var adr027QuickstartAnchors = []struct{ symbol, within, start, end string }{
	{symbol: "CONVERGE_FAILURES", start: `^CONVERGE_FAILURES=""$`},
	{symbol: "_record_row's integrity column", within: "_record_row", start: `^\s*case "\$name" in$`, end: `^\s*esac$`},
	{symbol: "_converge", start: `^_converge\(\) \{$`},
	{symbol: "_converge's optional class", within: "_converge", start: `^\s*if \[ "\$class" = optional \]; then$`},
	{symbol: "_converge's ledger append", within: "_converge", start: `^\s*CONVERGE_FAILURES="\$CONVERGE_FAILURES\$name"`},
	{symbol: "_pip_install_locked", start: `^_pip_install_locked\(\) \{$`},
	{symbol: "_apt_converge", start: `^_apt_converge\(\) \{$`},
	{symbol: "_apt_converge's apt-get update", within: "_apt_converge", start: `_apt_get update -o APT::Update::Error-Mode=any`, end: `^\s*fi$`},
	{symbol: "_converge go", start: `^\s*_converge go `},
	{symbol: "_converge gh", start: `^\s*_converge gh `},
	{symbol: "_converge claude", start: `^\s*_converge claude `},
	{symbol: "_converge playwright", start: `^\s*_converge playwright `},
	{symbol: "_converge codex", start: `^\s*_converge codex `},
	{symbol: "_converge openobserve", within: "setup_telemetry", start: `^\s*_converge openobserve `},
	{symbol: "--check", start: `^\s*--check\)$`, end: `^\s*CHECK_ONLY=true$`},
	{symbol: "_lock_result", start: `^_lock_result\(\) \{$`},
	{symbol: "_converge_check", start: `^_converge_check\(\) \{$`},
	{symbol: "_converge_report", start: `^_converge_report\(\) \{$`},
	{symbol: "convergence.json", within: "_converge_report", start: `record="\$root/\.runtime/bootstrap/convergence\.json"`},
	{symbol: "_converge_report's exit", within: "_converge_report", start: `^\s*if \[ -n "\$CONVERGE_FAILURES" \]; then$`, end: `^\s*fi$`},
}

// adr027AnchorCites resolves every anchor to the cite it should carry ("N" or "N-M", 1-based) and
// maps that cite to the anchor's symbol.
func adr027AnchorCites(t *testing.T, lines []string) map[string]string {
	t.Helper()
	symbolAt := map[string]string{}
	for _, a := range adr027QuickstartAnchors {
		lo, hi := 0, len(lines)
		if a.within != "" {
			head := regexp.MustCompile(`^` + regexp.QuoteMeta(a.within) + `\(\) \{$`)
			lo = -1
			for i, l := range lines {
				if head.MatchString(l) {
					lo = i
					break
				}
			}
			if lo < 0 {
				t.Fatalf("quickstart.sh has no %s() function, which holds %s", a.within, a.symbol)
			}
			for hi = lo + 1; hi < len(lines) && lines[hi] != "}"; hi++ {
			}
		}
		start := regexp.MustCompile(a.start)
		at := -1
		for i := lo; i < hi; i++ {
			if start.MatchString(lines[i]) {
				if at >= 0 {
					t.Fatalf("%s matches quickstart.sh lines %d and %d; an anchor must match exactly one line", a.symbol, at+1, i+1)
				}
				at = i
			}
		}
		if at < 0 {
			t.Fatalf("%s (%s) matches no line of quickstart.sh", a.symbol, a.start)
		}
		cite := strconv.Itoa(at + 1)
		if a.end != "" {
			end := regexp.MustCompile(a.end)
			last := -1
			for i := at + 1; i < hi; i++ {
				if end.MatchString(lines[i]) {
					last = i
					break
				}
			}
			if last < 0 {
				t.Fatalf("%s's range has no line matching %s after quickstart.sh:%d", a.symbol, a.end, at+1)
			}
			cite += "-" + strconv.Itoa(last+1)
		}
		symbolAt[cite] = a.symbol
	}
	return symbolAt
}

// A bare `:N` continues the file of the cite before it, as in (`quickstart.sh:1104`, `:1235`).
// A cite followed by "at `<sha>`" quotes an older revision, so the current script cannot check it.
var adr027CiteRe = regexp.MustCompile("`([A-Za-z0-9_./-]*):([0-9]+(?:-[0-9]+)?)`(\\s+at\\s+`[0-9a-f]{7,40}`)?")

func adr027QuickstartCites(text string) []string {
	var cites []string
	file := ""
	for _, m := range adr027CiteRe.FindAllStringSubmatch(text, -1) {
		if m[1] != "" {
			file = m[1]
		}
		if file == "quickstart.sh" && m[3] == "" {
			cites = append(cites, m[2])
		}
	}
	return cites
}
