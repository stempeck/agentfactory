//go:build !integration

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// #673 AC-1, clauses (iii) and (iv): "the Token economics contract table and its consistency test
// match the result." Prose is the one surface in this repository with no compiler and no runtime
// behind it — a row can claim the observer computes its own trigger long after it stopped, and every
// other test in the tree stays green. This file is the compiler that prose does not have.
//
// It is a resurrection, not a new idea: a file of this name pinned the contract from #668 K11 until
// a9fdaa88 removed it while relocating the section out of the operator command manual. The objection
// that killed it is the specification for bringing it back — pin PHRASES, not bytes, and read
// USING_TOKENOMICS.md, which is where the contract now lives.

const tokenomicsContractHeading = "## Token economics"

// tokenomicsContractSection returns the `## Token economics` section body — its heading through the
// line before the next H2. Fenced blocks are tracked so a `#` inside an example cannot end it early,
// and only H2s close it, so the `###` subsections below the heading stay part of the contract.
func tokenomicsContractSection(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")

	start := -1
	for i, line := range lines {
		if strings.TrimRight(line, " ") != tokenomicsContractHeading {
			continue
		}
		if start >= 0 {
			t.Fatalf("USING_TOKENOMICS.md has duplicate %q headings (lines %d and %d); the shipped "+
				"pointer names one section, so two of them make it ambiguous which is the contract",
				tokenomicsContractHeading, start+1, i+1)
		}
		start = i
	}
	if start < 0 {
		t.Fatalf("USING_TOKENOMICS.md has no %q section, but af tokenomics status prints %q on every "+
			"run — the verb cites a section a reader cannot find",
			tokenomicsContractHeading, tokenomicsContractPointer)
	}

	inFence := false
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			inFence = !inFence
			continue
		}
		if !inFence && strings.HasPrefix(lines[i], "## ") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// readUsingTokenomicsDoc reads the companion guide. A sibling of readUsingAgentfactoryDoc
// (dispatch_crons_doc_test.go:74) rather than a parameter on it, because the two documents have
// different owners: one is the operator command manual, the other the behavior contract, and a9fdaa88
// separated them deliberately.
func readUsingTokenomicsDoc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "USING_TOKENOMICS.md"))
	if err != nil {
		t.Fatalf("reading USING_TOKENOMICS.md: %v", err)
	}
	return string(data)
}

// tableRowFor returns the markdown table row whose first cell names want, or "".
func tableRowFor(section, want string) string {
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "| `"+want+"`") {
			continue
		}
		return line
	}
	return ""
}

// tableCells splits a markdown table row into its trimmed cells. An escaped pipe is NOT a column
// separator: the `dispatch` row writes the PreToolUse matcher as an escaped `Task|Agent`, and a naive
// split on "|" reads that row one cell wider than the header — which shifts every column index after
// it, so an assertion aimed at the last column silently reads the second-to-last one instead.
func tableCells(row string) []string {
	const sentinel = "\x00"
	parts := strings.Split(strings.Trim(strings.TrimSpace(strings.ReplaceAll(row, `\|`, sentinel)), "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(strings.ReplaceAll(p, sentinel, `\|`)))
	}
	return cells
}

// tableHeader returns the header cells of the table whose first header cell is firstCell, located by
// that cell rather than by line number so a table can move within the section without breaking the
// walk.
func tableHeader(section, firstCell string) []string {
	for _, line := range strings.Split(section, "\n") {
		if cells := tableCells(line); len(cells) > 1 && cells[0] == firstCell {
			return cells
		}
	}
	return nil
}

// tableBodyRows returns the rows belonging to the table headed by header — every line with exactly
// as many cells, minus the header and the `|---|` rule.
//
// Cell count is the membership test on purpose. A row separated from its table by a blank line still
// looks like a row to a reader skimming the source and renders as a literal-pipe PARAGRAPH in every
// GFM viewer, so a walk that keyed on "starts with |" would confirm the contract carries a row the
// operator cannot see.
func tableBodyRows(section string, header []string) []string {
	var rows []string
	seen := false
	for _, line := range strings.Split(section, "\n") {
		cells := tableCells(line)
		if len(cells) != len(header) {
			if seen && strings.TrimSpace(line) == "" {
				break
			}
			continue
		}
		if !seen {
			seen = true
			continue
		}
		if strings.Trim(cells[0], "-") == "" {
			continue
		}
		rows = append(rows, line)
	}
	return rows
}

// declaredConstantValue matches a `Name = "value"` line in a Go const block.
var declaredConstantValue = regexp.MustCompile(`(?m)^\s*([A-Za-z][A-Za-z0-9_]*)\s+=\s+"([a-z0-9_]+)"`)

// declaredConstants returns the values of every constant in a source file whose name carries a prefix,
// sorted. A vocabulary the contract calls CLOSED has to be read out of the declaration that closes it:
// a list re-typed in a test closes over the constants that existed the day it was typed, and the next
// one ships with the promise still on the page.
func declaredConstants(t *testing.T, file, prefix string) []string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var out []string
	for _, m := range declaredConstantValue.FindAllStringSubmatch(string(src), -1) {
		if strings.HasPrefix(m[1], prefix) {
			out = append(out, m[2])
		}
	}
	if out == nil {
		t.Fatalf("%s declares no %s* constants; the vocabulary this walks is written against a "+
			"declaration that has moved", file, prefix)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// recordedActions is the closed action vocabulary, read off telemetry's own const block so a sixth
// Action cannot be added to the schema while the doc still says the vocabulary is closed at five.
func recordedActions(t *testing.T) []string {
	return declaredConstants(t, filepath.Join("..", "telemetry", "event.go"), "Action")
}

var backtickedToken = regexp.MustCompile("`([^`]+)`")

var recordClassCount = regexp.MustCompile(`(?i)\b([a-z]+) record classes\b`)

// backtickedTokens returns every code-spanned token in a cell. The objective vocabulary is closed, so
// the assertion the Objective(s) column can carry is not "one of the two appears" but "nothing else
// is claimed".
func backtickedTokens(cell string) []string {
	var out []string
	for _, m := range backtickedToken.FindAllStringSubmatch(cell, -1) {
		out = append(out, m[1])
	}
	return out
}

var jsonTag = regexp.MustCompile("`json:\"([a-z0-9_]+)\"`")

// jsonColumnsOf returns the json tag names declared by one struct in one source file of this package.
// The residual disclosures tell an operator which verb carries which column, and that is a claim about
// two schemas — so it is read back off the two schemas rather than trusted.
func jsonColumnsOf(t *testing.T, file, typeName string) []string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	open := strings.Index(string(src), "type "+typeName+" struct {")
	if open < 0 {
		t.Fatalf("%s declares no %s; the disclosure is written against a schema that no longer exists",
			file, typeName)
	}
	body := string(src)[open:]
	if end := strings.Index(body, "\n}"); end >= 0 {
		body = body[:end]
	}
	var cols []string
	for _, m := range jsonTag.FindAllStringSubmatch(body, -1) {
		cols = append(cols, m[1])
	}
	slices.Sort(cols)
	return slices.Compact(cols)
}

var metricSum = regexp.MustCompile(`m\.Metric = (.+)`)

// metricAddends returns the accumulators the protocol sums into the pass metric, sorted. The bar is
// only as honest as what it is computed over, and that is a fact about one line of arithmetic.
func metricAddends(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "telemetry", "protocol.go"))
	if err != nil {
		t.Fatalf("read protocol.go: %v", err)
	}
	m := metricSum.FindStringSubmatch(string(src))
	if m == nil {
		t.Fatal("internal/telemetry/protocol.go no longer assigns m.Metric; the contract's bar is " +
			"written about arithmetic that has moved")
	}
	var out []string
	for _, term := range strings.Split(m[1], "+") {
		out = append(out, strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(term), ".total")))
	}
	slices.Sort(out)
	return out
}

// sentenceContaining returns the sentence of a document holding a phrase, so a negative pin can be
// scoped to the claim it is about instead of to the whole section.
func sentenceContaining(doc, phrase string) string {
	at := strings.Index(doc, phrase)
	if at < 0 {
		return ""
	}
	start := strings.LastIndex(doc[:at], ". ") + 1
	end := strings.Index(doc[at:], ". ")
	if end < 0 {
		return doc[start:]
	}
	return doc[start : at+end+1]
}

// consumptionDerivedFromDelta reports whether the read side still computes over_consumption from
// cum_tokens_delta. The doc's spend/occupancy split rests on that derivation, so it is read rather
// than assumed.
func consumptionDerivedFromDelta(t *testing.T) bool {
	t.Helper()
	src, err := os.ReadFile("telemetry_context_read.go")
	if err != nil {
		t.Fatalf("read telemetry_context_read.go: %v", err)
	}
	at := strings.Index(string(src), "f.overConsumption = ")
	if at < 0 {
		return false
	}
	from := at - 200
	if from < 0 {
		from = 0
	}
	return strings.Contains(string(src)[from:at], "*f.cumTokensDelta >")
}

// The assignment matchers stop at a trailing line comment: the constant matchers below are anchored,
// so a cosmetic `// ...` on a write site would otherwise turn a named mechanism into a computed one
// and silently widen the scan — or turn a named objective into a computed one and fail the row.
var objectiveAssignment = regexp.MustCompile(`\.Objective = ([^/]+)`)

var objectiveConstant = regexp.MustCompile(`^telemetry\.Objective(Capacity|Efficiency)$`)

var mechanismAssignment = regexp.MustCompile(`\.Mechanism = ([^/]+)`)

var mechanismConstant = regexp.MustCompile(`^string\(tokenomics\.Mechanism([A-Za-z]+)\)$`)

// objectivesAtLiteralSites reports, for every write site in this package whose mechanism is a named
// constant, which objective that site's record carries: the constant its own closure assigns, or
// ObjectiveCapacity from writeInterventionRecord's pre-stamp when the closure assigns none.
//
// Sites whose mechanism or objective is computed are omitted rather than guessed. Static text cannot
// say which mechanism `string(eff.mechanism)` resolves to, and a walk that assumed one would pin a
// claim about a record the code may never write — worse than pinning nothing.
func objectivesAtLiteralSites(t *testing.T) map[string][]string {
	t.Helper()
	return literalSiteFields(t, objectiveInClosure)
}

// literalSiteFields walks every write site in this package whose mechanism is a named constant and
// asks read for one field of the record that site writes, keyed by mechanism.
func literalSiteFields(t *testing.T, read func(rest []string) (string, bool)) map[string][]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	found := map[string]map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			m := mechanismAssignment.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			named := mechanismConstant.FindStringSubmatch(strings.TrimSpace(m[1]))
			if named == nil {
				continue
			}
			value, ok := read(lines[i+1:])
			if !ok {
				continue
			}
			for _, mech := range tokenomics.Mechanisms() {
				if !strings.EqualFold(named[1], string(mech)) {
					continue
				}
				if found[string(mech)] == nil {
					found[string(mech)] = map[string]bool{}
				}
				found[string(mech)][value] = true
			}
		}
	}
	out := map[string][]string{}
	for mech, set := range found {
		for value := range set {
			out[mech] = append(out[mech], value)
		}
		slices.Sort(out[mech])
	}
	return out
}

// actionsAtLiteralSites is the same walk over the Action field, and there is no pre-stamp behind it:
// a closure that names no action writes none, so it contributes nothing rather than a default.
func actionsAtLiteralSites(t *testing.T) map[string][]string {
	t.Helper()
	actionSpellings := map[string]string{
		"Advise": telemetry.ActionAdvise, "Handoff": telemetry.ActionHandoff,
		"ReduceEffort": telemetry.ActionReduceEffort, "Refuse": telemetry.ActionRefuse,
		"Observe": telemetry.ActionObserve,
	}
	return literalSiteFields(t, func(rest []string) (string, bool) {
		for _, line := range rest {
			if strings.TrimSpace(line) == "})" || mechanismAssignment.MatchString(line) {
				break
			}
			m := actionAssignment.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			c := actionConstant.FindStringSubmatch(strings.TrimSpace(m[1]))
			if c == nil {
				return "", false
			}
			// The identifier's suffix is not the recorded spelling — ActionReduceEffort records
			// `reduce_effort` — so it is resolved through the const block that declares both.
			spelling, ok := actionSpellings[c[1]]
			return spelling, ok
		}
		return "", false
	})
}

var actionAssignment = regexp.MustCompile(`\.Action = ([^/]+)`)

var actionConstant = regexp.MustCompile(`^telemetry\.Action([A-Za-z]+)$`)

// objectiveInClosure reads forward from a mechanism assignment to the end of its closure. Not-ok means
// the site's objective is computed and so is not a static fact about the record it writes.
func objectiveInClosure(rest []string) (string, bool) {
	for _, line := range rest {
		if strings.TrimSpace(line) == "})" || mechanismAssignment.MatchString(line) {
			break
		}
		m := objectiveAssignment.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		c := objectiveConstant.FindStringSubmatch(strings.TrimSpace(m[1]))
		if c == nil {
			return "", false
		}
		return strings.ToLower(c[1]), true
	}
	return telemetry.ObjectiveCapacity, true
}

// objectivesStampedBy reads a source file of this package and reports which objective constants it
// assigns FOR ONE MECHANISM, and whether any of those assignments is computed rather than constant.
// It reads the source because that is the only place the answer exists: the assignments sit inside
// closures handed to recordIntervention, so nothing exported carries the file each objective is
// written from.
//
// Each objective is attributed to the nearest preceding mechanism assignment, which is the same
// closure in every write site in this package. Scoping matters: a whole-file scan lets an unrelated
// record donate an objective the mechanism under test never writes, and the donated spelling is
// exactly what would hide a writer that stopped computing its objective and started stamping one.
// A computed mechanism (`string(eff.mechanism)`) is counted in, because the alternative is to drop
// a record class the row does name.
func objectivesStampedBy(t *testing.T, file string, want tokenomics.Mechanism) (stamped []string, dynamic bool) {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	scoped := false
	for _, line := range strings.Split(string(src), "\n") {
		if m := mechanismAssignment.FindStringSubmatch(line); m != nil {
			named := mechanismConstant.FindStringSubmatch(strings.TrimSpace(m[1]))
			scoped = named == nil || strings.EqualFold(named[1], string(want))
			continue
		}
		m := objectiveAssignment.FindStringSubmatch(line)
		if m == nil || !scoped {
			continue
		}
		c := objectiveConstant.FindStringSubmatch(strings.TrimSpace(m[1]))
		if c == nil {
			dynamic = true
			continue
		}
		stamped = append(stamped, strings.ToLower(c[1]))
	}
	slices.Sort(stamped)
	stamped = slices.Compact(stamped)
	if stamped == nil && !dynamic {
		t.Fatalf("%s stamps no objective for `%s`; the row's attribution is written against a file "+
			"that no longer writes that mechanism's records", file, want)
	}
	return stamped, dynamic
}

// auditClauseFor returns the span of an Audit-record cell that belongs to one source file: from that
// file's own mention to the next file's. The cell has to name the file BEFORE the claim it owns, and
// that ordering is what makes per-writer attribution checkable at all — a claim trailing its file
// reference cannot be told apart from the previous writer's.
func auditClauseFor(t *testing.T, cell, file string) string {
	t.Helper()
	start := strings.Index(cell, "`"+file+"`")
	if start < 0 {
		return ""
	}
	rest := cell[start+len("`"+file+"`"):]
	end := len(rest)
	for _, other := range goFileMention.FindAllStringIndex(rest, -1) {
		end = other[0]
		break
	}
	return cell[start : start+len("`"+file+"`")+end]
}

var goFileMention = regexp.MustCompile("`[A-Za-z0-9_]+\\.go`")

// efficiencySignature returns the parameter list of tokenomics.Efficiency. Guarantee 7 is a promise
// about what that function is NOT handed, and the only honest way to check an absence is to read the
// declaration back.
func efficiencySignature(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "tokenomics", "efficiency.go"))
	if err != nil {
		t.Fatalf("read efficiency.go: %v", err)
	}
	const decl = "func Efficiency("
	open := strings.Index(string(src), decl)
	if open < 0 {
		t.Fatal("internal/tokenomics/efficiency.go declares no Efficiency; guarantee 7 is written " +
			"about a function that no longer exists")
	}
	rest := string(src)[open+len(decl):]
	close := strings.Index(rest, ")")
	if close < 0 {
		t.Fatalf("could not read Efficiency's parameter list: %.80s", rest)
	}
	return rest[:close]
}

func TestTokenomicsContractDoc(t *testing.T) {
	// Every doc subtest resolves the module root by walking up from the working directory
	// (findModuleRoot, env_hermetic_test.go:314). The behavioral subtest at the bottom builds a
	// lifecycle fixture, which t.Chdir's into a temp dir where that walk finds no go.mod. They are
	// separate subtests for exactly that reason: t.Chdir restores the cwd when its subtest ends.
	content := readUsingTokenomicsDoc(t)
	section := tokenomicsContractSection(t, content)

	t.Run("the shipped pointer resolves to this heading", func(t *testing.T) {
		// Derived from the CONSTANT rather than re-typed. Two independent literals is how a pointer
		// and its destination drift apart while both look right in isolation.
		quoted := regexp.MustCompile(`"([^"]+)"`).FindStringSubmatch(tokenomicsContractPointer)
		if quoted == nil {
			t.Fatalf("tokenomicsContractPointer names no quoted section title: %q", tokenomicsContractPointer)
		}
		if want := "## " + quoted[1]; !strings.HasPrefix(section, want+"\n") {
			t.Errorf("the pointer names the %q section and the heading is %q", quoted[1],
				strings.SplitN(section, "\n", 2)[0])
		}
		if !strings.Contains(tokenomicsContractPointer, "USING_TOKENOMICS.md") {
			t.Errorf("the pointer does not name the document: %q", tokenomicsContractPointer)
		}
	})

	t.Run("every mechanism is named by its code constant", func(t *testing.T) {
		// Walked from tokenomics.Mechanisms() rather than from a list re-typed here: the vocabulary is
		// closed and single-sourced (policy.go:40-45), so a seventh mechanism added without a contract
		// row fails here on the day it is added.
		for _, m := range tokenomics.Mechanisms() {
			if tableRowFor(section, string(m)) == "" {
				t.Errorf("the contract has no table row for mechanism %q; a mechanism an operator can "+
					"switch on and cannot look up is an unwritten contract", m)
			}
		}
	})

	t.Run("the dispatch row names one verdict computer and derives the observer from it", func(t *testing.T) {
		// Asserted ROW BY ROW rather than by substring over the section. A bare Contains for
		// "dispatch-admit" is satisfied by the `refuse` mapping row two screens up, so the whole
		// per-mechanism row could be deleted with the check still green — which is precisely the
		// artifact it exists to pin.
		row := tableRowFor(section, "dispatch")
		if row == "" {
			t.Fatal("USING_TOKENOMICS.md has no `dispatch` row in the per-mechanism contract")
		}

		// AC-1 clause (iii): the table must say which single site computes, and that the observer
		// derives rather than computes.
		for _, want := range []string{
			"one verdict",
			"af subagent-observe",
			"recorded refusal",
			"relays",
			"computes no capacity verdict",
			// design-doc.md:106's L-4 timing sentence — the beads changed direction in time.
			"FOLLOW recorded refusals",
			// design-doc.md:106 puts Phase 1's release mechanism in this row too.
			"SubagentStop proposes; release follows verified sidechain quiet",
		} {
			if !strings.Contains(row, want) {
				t.Errorf("the `dispatch` row is missing %q — AC-1 requires the table to record that "+
					"exactly one component computes the capacity verdict and that the observer's "+
					"output derives from it:\n%s", want, row)
			}
		}

		// The falsified triggers. Each of these described the observer computing its own verdict from
		// the session's occupancy, which is the thing #673 item 1 deleted.
		for _, gone := range []string{
			"when occupancy is at or above the ceiling",
			"verified completion event",
		} {
			if strings.Contains(row, gone) {
				t.Errorf("the `dispatch` row still carries the pre-demotion claim %q; the observer no "+
					"longer computes a verdict and no platform event marks background completion:\n%s",
					gone, row)
			}
		}
	})

	t.Run("the serialize row credits the gate, not the observer's own trigger", func(t *testing.T) {
		row := tableRowFor(section, "serialize")
		if row == "" {
			t.Fatal("USING_TOKENOMICS.md has no `serialize` row in the permitted-actions mapping")
		}
		if !strings.Contains(row, "already recorded") {
			t.Errorf("the `serialize` row does not say the bead relays a refusal the gate already "+
				"recorded; under demotion the observer has no trigger of its own:\n%s", row)
		}
		if strings.Contains(row, "bead from the `Task` observer") {
			t.Errorf("the `serialize` row still attributes the bead to the observer's own trigger:\n%s", row)
		}
	})

	t.Run("the refusal row still promises an un-gated record", func(t *testing.T) {
		// The phrase the behavioral subtest below re-verifies. Pinned here so the promise and its
		// proof cannot drift apart: deleting the sentence would otherwise leave the proof orphaned.
		if !strings.Contains(section, "recorded regardless of the telemetry toggle") {
			t.Error("the contract no longer promises that a refusal is recorded regardless of the " +
				"telemetry toggle; that promise is what makes a run-#1 refusal retrievable in a " +
				"factory that never enabled telemetry")
		}
	})

	t.Run("the multi-site framing does not imply a second verdict", func(t *testing.T) {
		// :95-96. Naming more than one site is still true; what became false is the reading that more
		// than one site DECIDES. The framing has to carry that distinction or the per-mechanism table
		// below it reads as licensing what AC-1 forbids.
		for _, want := range []string{
			"Naming a second site is never a second verdict",
		} {
			if !strings.Contains(section, want) {
				t.Errorf("the framing above the per-mechanism table is missing %q", want)
			}
		}
	})

	t.Run("the contract's subsections stay in reading order", func(t *testing.T) {
		actions := strings.Index(content, "\n### The permitted actions")
		perMech := strings.Index(content, "\n### Per-mechanism contract")
		next := strings.Index(content, "\n### Residual ceilings and disclosures")
		if !(actions >= 0 && actions < perMech && perMech < next) {
			t.Errorf("`### Per-mechanism contract` must sit between `### The permitted actions` and "+
				"`### Residual ceilings and disclosures` (offsets: actions=%d per-mechanism=%d next=%d)",
				actions, perMech, next)
		}
	})

	t.Run("the command manual describes the hook by the gate's refusal", func(t *testing.T) {
		// Surfaces 4 and 5. readUsingAgentfactoryDoc already exists (dispatch_crons_doc_test.go:74).
		doc := readUsingAgentfactoryDoc(t)
		if strings.Contains(doc, "at or above the operator's occupancy") {
			t.Error("USING_AGENTFACTORY.md still describes `af subagent-observe` as firing on an " +
				"occupancy ceiling it computes for itself")
		}
		if !strings.Contains(doc, "when the `af dispatch-admit` gate has recorded a refusal") {
			t.Error("USING_AGENTFACTORY.md does not describe the observer's trigger as the gate's " +
				"recorded refusal")
		}
	})

	t.Run("the observer's own help agrees with the contract", func(t *testing.T) {
		// Surface 6. The cobra text is the description an operator meets first, and it is the one doc
		// surface that ships inside the binary.
		help := subagentObserveCmd.Short + "\n" + subagentObserveCmd.Long
		for _, want := range []string{"dispatch-admit", "recorded", "ADR-007"} {
			if !strings.Contains(help, want) {
				t.Errorf("subagentObserveCmd help is missing %q:\n%s", want, help)
			}
		}
		if strings.Contains(help, "occupancy") {
			t.Errorf("subagentObserveCmd help still names an occupancy trigger it no longer "+
				"computes:\n%s", help)
		}
	})

	t.Run("the refusal record's 'regardless of the telemetry toggle' claim is true", func(t *testing.T) {
		// The behavioral half (design-doc.md:106). A doc test that only reads prose proves the prose
		// is self-consistent, not that it is TRUE; this subtest drives the real gate and reads the
		// record back.
		now := time.Now()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		// Deliberately NO gateOn(t, fx.root). The telemetry toggle is default-off, and default-off is
		// the condition the contract row makes its promise about.
		writeDeclaredBackendModels(t, fx.root)
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("an over-capacity Task launch was not denied; stdout:\n%s", out.String())
		}

		recs := dispatchInterventionRecords(t, fx.root, fx.agent)
		if len(recs) != 1 || recs[0].Action != telemetry.ActionRefuse {
			t.Fatalf("the contract promises the refusal is recorded regardless of the telemetry "+
				"toggle; with the toggle off it wrote %d dispatch records: %+v", len(recs), recs)
		}

		// The contrast that makes the promise load-bearing. "Regardless of the toggle" is a RELATIVE
		// claim, and without this half, deleting recordIntervention's telemetry guard
		// (tokenomics_admission.go:234-236) would leave every assertion above green.
		ctx := withVerbTelemetry(t.Context(), verbTelemetry{
			verb: "tokenomics-contract", agent: fx.agent, start: now, enabled: false,
		})
		recordIntervention(ctx, fx.root, fx.workDir, fx.agent, "", func(ev *telemetry.StepEvent) {
			ev.Mechanism = string(tokenomics.MechanismThrift)
			ev.Action = telemetry.ActionAdvise
		})
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismThrift)]); got != 0 {
			t.Errorf("recordIntervention wrote %d records with the telemetry toggle off; the row's "+
				"'regardless' is only meaningful because the measurement path IS gated", got)
		}
	})

	// #678 K11. Everything above pins the single-objective contract #673 left behind. The subtests
	// below pin the SECOND objective: what tokenomics.Efficiency promises by the operands it refuses
	// to take, what the efficiency arm may never touch, which objective each mechanism serves, and the
	// verb that decides whether an improvement happened. Phases 3-5 shipped all of it and every prose
	// surface stayed green describing one objective — which is the drift these assertions exist for.

	t.Run("the frame lift is a written guarantee, not an implementation detail", func(t *testing.T) {
		// tokenomics.Efficiency(Aggregate, bool, Policy) takes no Window, no Occupancy and no pool
		// operand, and that ABSENCE is the single property that lets the efficiency arm fire on a roomy
		// cloud profile where capacity is structurally inert. An absence is exactly what a later change
		// removes without anyone noticing, so it is written down as a promise and pinned here.
		if !strings.Contains(section, "Efficiency actuators read no window operand") {
			t.Error("the contract does not carry guarantee 7, `Efficiency actuators read no window " +
				"operand`; without it the one property that lets efficiency fire where capacity cannot " +
				"is an undocumented implementation detail a refactor may delete in good faith")
		}
		// The headline alone is not the guarantee — the operand list under it is, and a headline can
		// survive a paragraph that lists the operands the function no longer lacks. Both halves are
		// read back off the signature.
		params := efficiencySignature(t)
		for _, forbidden := range []string{"Window", "Occupancy", "Pool"} {
			if strings.Contains(params, forbidden) {
				t.Errorf("tokenomics.Efficiency now takes a %s operand (%q); guarantee 7 promises it is "+
					"handed none, and a roomy profile can now silence the arm the guarantee exists to "+
					"keep alive", forbidden, params)
			}
		}
		for _, operand := range []string{"Aggregate", "bool", "Policy"} {
			if !strings.Contains(params, operand) {
				t.Errorf("guarantee 7 enumerates the three operands `tokenomics.Efficiency` does take, "+
					"and %s is no longer one of them (%q)", operand, params)
			}
		}
		// The absence is the guarantee, so the prose that states it is checked against the signature in
		// both directions: with no such operand in the declaration, the paragraph has to keep saying so.
		flat := strings.Join(strings.Fields(section), " ")
		if !strings.Contains(flat, "It is handed no context window, no live occupancy, no declared pool") {
			t.Error("`tokenomics.Efficiency` takes no window, occupancy or pool operand, and guarantee 7 " +
				"no longer says it is handed none. The headline alone survives a paragraph that lists " +
				"the operands the function was supposed to lack")
		}
	})

	t.Run("no efficiency intervention edits a formula", func(t *testing.T) {
		// Guarantee 8, and the boundary between two umbrellas. The efficiency mechanisms counsel,
		// relaunch and re-level a SESSION; nothing under tokenomics writes a formula. Without the line,
		// "the factory optimises itself" reads as a licence this surface does not have.
		if !strings.Contains(section, "No efficiency intervention edits a formula") {
			t.Error("the contract does not carry guarantee 8, `No efficiency intervention edits a " +
				"formula`; the efficiency arm may change how a session runs, never what it runs")
		}
		if !strings.Contains(section, "af improvement complete") {
			t.Error("guarantee 8 does not name `af improvement complete` as the verb that closes the " +
				"one loop which does change a formula, so the exception it carves out is unfalsifiable")
		}
	})

	t.Run("every mechanism row names its objective from the closed pair", func(t *testing.T) {
		// Walked from the telemetry constants (event.go:91-92) rather than from literals re-typed here,
		// for the reason the Mechanisms() walk above gives: two copies drift, and the copy that drops
		// out of the document is the one nobody notices.
		//
		// Read from the row's Objective(s) CELL, located by the header's column index. A bare Contains
		// over the row is satisfied by the word "capacity" appearing in the `dispatch` row's prose,
		// which would pin nothing at all.
		header := tableHeader(section, "Mechanism")
		if header == nil {
			t.Fatal("the per-mechanism table has no `Mechanism` header row")
		}
		objCol := -1
		for i, h := range header {
			if h == "Objective(s)" {
				objCol = i
			}
		}
		if objCol <= 0 {
			t.Fatalf("the per-mechanism header carries no `Objective(s)` column after `Mechanism` "+
				"(header: %q); it must not be the first column, because tableRowFor keys every row on a "+
				"leading backticked mechanism cell and moving Mechanism out of position one unfinds all "+
				"six rows at once", header)
		}

		// The EXACT set each row must name, not merely "one of the two". Membership alone is satisfied by
		// a row that names the wrong objective, which is the drift with teeth: `budget` re-labelled
		// `efficiency` would tell an operator a cloud profile still runs the capacity arm.
		//
		// Derived by exhausting the ev.Objective / ev.Mechanism write sites, and true because
		// writeInterventionRecord pre-stamps ObjectiveCapacity on every record — a site reaches
		// `efficiency` only by overwriting it.
		want := map[tokenomics.Mechanism][]string{
			tokenomics.MechanismBudget:    {telemetry.ObjectiveCapacity},
			tokenomics.MechanismThrift:    {telemetry.ObjectiveCapacity, telemetry.ObjectiveEfficiency},
			tokenomics.MechanismDispatch:  {telemetry.ObjectiveCapacity},
			tokenomics.MechanismInterview: {telemetry.ObjectiveEfficiency},
			tokenomics.MechanismEffort:    {telemetry.ObjectiveCapacity, telemetry.ObjectiveEfficiency},
			// escalate is deferred (K14). Nothing writes mechanism=escalate, so an objective in its cell
			// would document a mechanism that can act. This is the one row where NAMING one is the failure.
			tokenomics.MechanismEscalate: nil,
		}
		fromLiteralSites := objectivesAtLiteralSites(t)

		for _, m := range tokenomics.Mechanisms() {
			row := tableRowFor(section, string(m))
			if row == "" {
				continue // already reported by "every mechanism is named by its code constant"
			}
			cells := tableCells(row)
			if len(cells) != len(header) {
				t.Errorf("the `%s` row has %d cells against a %d-cell header; a ragged row makes every "+
					"column after the ragged one unreadable:\n%s", m, len(cells), len(header), row)
				continue
			}

			got := backtickedTokens(cells[objCol])
			slices.Sort(got)
			expect := slices.Clone(want[m])
			slices.Sort(expect)
			if !slices.Equal(got, expect) {
				t.Errorf("the `%s` row's Objective(s) cell is %q, naming %q; the record sites write %q. "+
					"A row that names an objective no site writes — or omits one that fires — is the "+
					"column's whole failure mode, because an operator reads it instead of grepping the "+
					"records", m, cells[objCol], got, expect)
				continue
			}

			// `want` above is hand-written, so on its own it pins "the doc matches this map" and a new
			// write site makes the row false with the suite still green. Every site whose mechanism is a
			// LITERAL is therefore read back out of the package: its objective is the constant its own
			// closure assigns, or `capacity` from the pre-stamp when the closure assigns none. Sites
			// whose mechanism or objective is computed are skipped rather than guessed — the four of
			// them are covered by the effort row's per-file attribution instead.
			for _, objective := range fromLiteralSites[string(m)] {
				if !slices.Contains(got, objective) {
					t.Errorf("a write site stamps `objective=%s` on `mechanism=%s`, and that row names "+
						"only %q. An operator who filters the records by the objective the row promises "+
						"will not see this firing at all", objective, m, got)
				}
			}
		}
	})

	t.Run("every recorded action has a row of its own", func(t *testing.T) {
		// Walked from telemetry's Action constants, so a sixth action cannot ship with the mapping table
		// silent about it — the failure that let `observe` sit in the vocabulary while the section still
		// said the verbs were "closed at four".
		//
		// The row must be a row: tableBodyRows keys on cell count and stops at the blank line that ENDS
		// the table, so an entry stranded below that blank — which renders as a literal-pipe paragraph
		// rather than a table row — is not found. Prose that only looks like a table is the same defect
		// as prose that is missing.
		header := tableHeader(section, "Intervention (design language)")
		if header == nil {
			t.Fatal("the permitted-actions table has no `Intervention (design language)` header row")
		}
		// Counted per row, not gathered into one set: a table collapsed to a single row naming all five
		// actions satisfies "every action appears" while saying nothing about any of them. The mapping
		// is one design-language intervention to one recorded action, and the table has to keep that
		// shape to be a mapping at all.
		rowsFor := map[string]int{}
		for _, row := range tableBodyRows(section, header) {
			for _, tok := range backtickedTokens(tableCells(row)[1]) {
				rowsFor[tok]++
			}
		}
		actions := recordedActions(t)
		for _, action := range actions {
			if rowsFor[action] == 0 {
				t.Errorf("the permitted-actions table has no row recording %q (it names %v); a record an "+
					"operator will find in the log and cannot look up is a record with no contract",
					action, rowsFor)
			}
		}
		// One action per ROW, which is the direction that matters. Three design interventions legitimately
		// map onto `advise`, so an action may appear in several rows; a row naming several actions is the
		// degenerate table — collapse the six into one and "every action appears" is satisfied by a
		// mapping that maps nothing.
		for _, row := range tableBodyRows(section, header) {
			if named := backtickedTokens(tableCells(row)[1]); len(named) != 1 {
				t.Errorf("a row names %d recorded actions (%q); the column is one action per row, and a "+
					"row naming several is a row that maps nothing:\n%s", len(named), named, row)
			}
		}

		// "closed at four" counts the actions that CHANGED what happened, taken from the same predicate
		// af tokenomics status splits its ledger on. observe is the fifth constant and not a fifth act.
		// This clause has already been falsified once by a constant landing without it, which is the
		// argument for deriving the number instead of typing it.
		acting := 0
		for _, a := range actions {
			if (tokenomicsFirings{}).acted(a) {
				acting++
			}
		}
		if want := "closed at " + numberWord(acting); !strings.Contains(section, want) {
			t.Errorf("the contract does not say the vocabulary of things the harness may DO is %q; %d of "+
				"the %d Action constants change what happened, and the count in the prose has to move "+
				"with them", want, acting, len(actions))
		}
	})

	t.Run("every mechanism row names the actions its records actually carry", func(t *testing.T) {
		// The Audit-record column's real content, walked from the (mechanism, action) write sites. Without
		// this the column can be rewritten to name any subset and stay green — which is how the `dispatch`
		// row went years without mentioning the fail-open `observe` that carries its mechanism.
		header := tableHeader(section, "Mechanism")
		if header == nil {
			t.Fatal("the per-mechanism table has no `Mechanism` header row")
		}
		auditCol := slices.Index(header, "Audit record")
		if auditCol < 0 {
			t.Fatalf("the per-mechanism header carries no `Audit record` column (header: %q)", header)
		}

		want := map[tokenomics.Mechanism][]string{
			tokenomics.MechanismBudget:   {telemetry.ActionAdvise, telemetry.ActionHandoff},
			tokenomics.MechanismThrift:   {telemetry.ActionAdvise},
			tokenomics.MechanismDispatch: {telemetry.ActionAdvise, telemetry.ActionRefuse, telemetry.ActionObserve},
			tokenomics.MechanismInterview: {telemetry.ActionAdvise, telemetry.ActionHandoff,
				telemetry.ActionObserve},
			tokenomics.MechanismEffort: {telemetry.ActionAdvise, telemetry.ActionHandoff,
				telemetry.ActionReduceEffort, telemetry.ActionObserve},
			tokenomics.MechanismEscalate: nil,
		}
		fromLiteralSites := actionsAtLiteralSites(t)

		for _, m := range tokenomics.Mechanisms() {
			row := tableRowFor(section, string(m))
			if row == "" {
				continue // already reported by "every mechanism is named by its code constant"
			}
			cells := tableCells(row)
			if len(cells) != len(header) {
				continue // already reported by the Objective(s) walk
			}
			var got []string
			for _, tok := range backtickedTokens(cells[auditCol]) {
				if spelling, ok := strings.CutPrefix(tok, "action="); ok {
					got = append(got, spelling)
				}
			}
			slices.Sort(got)
			got = slices.Compact(got)
			expect := slices.Clone(want[m])
			slices.Sort(expect)
			if !slices.Equal(got, expect) {
				t.Errorf("the `%s` row's Audit record cell names the actions %q; the record sites write %q. "+
					"A row that omits a record class an operator will find in the log sends them looking "+
					"for a mechanism that did not write it:\n%s", m, got, expect, cells[auditCol])
				continue
			}
			// A cell that counts its classes must count the ones it goes on to name. The two drift in one
			// direction — a class is added to the enumeration and the numeral in front of it is left — and
			// the contradiction then sits inside a single sentence, where it reads as authority.
			if counted := recordClassCount.FindStringSubmatch(cells[auditCol]); counted != nil &&
				!strings.EqualFold(counted[1], numberWord(len(got))) {
				t.Errorf("the `%s` row says %q record classes and names %d (%q); the count and the "+
					"enumeration are one claim and have to move together:\n%s",
					m, counted[1], len(got), got, cells[auditCol])
			}
			// As with the objective walk: `want` above is hand-written, so every site whose mechanism is
			// a named constant is also read back out of the package. A new record class added to the
			// binary must reach the row rather than only the map.
			for _, action := range fromLiteralSites[string(m)] {
				if !slices.Contains(got, action) {
					t.Errorf("a write site records `action=%s` under `mechanism=%s`, and that row names "+
						"only %q. The row is what an operator reads instead of grepping the log",
						action, m, got)
				}
			}
		}
	})

	t.Run("the residual disclosure sends the reader to a verb that carries the column", func(t *testing.T) {
		// The disclosure exists because the pass metric cannot see input-side savings, so it tells an
		// operator where the input-side figures actually are. That is a claim about two JSON schemas,
		// and it has already been wrong once in each direction — first by sending readers to
		// `report --json` for columns only `compare --json` has, then by denying that the one column
		// `report --json` does have is a cost figure at all.
		report := jsonColumnsOf(t, "telemetry_json.go", "telemetryReportRowJSON")
		compare := jsonColumnsOf(t, "telemetry_compare.go", "compareRunJSON")

		for _, col := range []string{"in_tokens", "cache_read_tokens"} {
			if !strings.Contains(section, "`"+col+"`") {
				t.Errorf("the disclosure never names `%s`, the column it exists to point at", col)
			}
			if !slices.Contains(compare, col) {
				t.Errorf("the disclosure sends readers to `af telemetry compare --json` for `%s`, which "+
					"compareRunJSON does not carry", col)
			}
			if slices.Contains(report, col) {
				t.Errorf("the disclosure says `af telemetry report --json` carries neither `in_tokens` "+
					"nor `cache_read_tokens`; telemetryReportRowJSON now carries `%s`, so the reader "+
					"is being sent the long way round", col)
			}
		}
		for _, col := range []string{"cum_tokens_delta", "ctx_tokens_start", "over_consumption"} {
			if !strings.Contains(section, "`"+col+"`") {
				t.Errorf("the contract never names `%s`", col)
			}
			if !slices.Contains(report, col) {
				t.Errorf("the contract offers `%s` as the per-step consolation for the two columns "+
					"`af telemetry report --json` lacks; that verb's row does not carry it either", col)
			}
		}

		// Polarity, derived rather than transcribed. Naming the right columns in the wrong sentence is
		// how this disclosure was wrong the first two times — once by claiming `report --json` had them,
		// once by denying the column it does have is a cost figure.
		absent := 0
		for _, col := range []string{"in_tokens", "cache_read_tokens"} {
			if !slices.Contains(report, col) {
				absent++
			}
		}
		switch absent {
		case 2:
			if !strings.Contains(section, "carries neither of those two columns") {
				t.Error("`telemetryReportRowJSON` carries neither `in_tokens` nor `cache_read_tokens`, " +
					"and the disclosure no longer says so")
			}
		case 0:
			if !strings.Contains(section, "carries both of those two columns") {
				t.Error("`telemetryReportRowJSON` now carries both `in_tokens` and `cache_read_tokens`; " +
					"the disclosure still sends readers to `compare --json` for them")
			}
		default:
			t.Error("`telemetryReportRowJSON` carries exactly one of `in_tokens` / `cache_read_tokens`; " +
				"the disclosure is written all-or-nothing and has to be rewritten to say which")
		}
		// over_consumption is derived FROM cum_tokens_delta, which is what makes that column a spend
		// figure and not an occupancy one (over_occupancy is derived from the ctx figures instead).
		if !consumptionDerivedFromDelta(t) {
			t.Error("`over_consumption` is no longer derived from `cum_tokens_delta`; the section's " +
				"spend-versus-occupancy split is written against a derivation that has moved")
		} else if !strings.Contains(section, "`cum_tokens_delta`, which *is* a spend figure") {
			t.Error("the disclosure no longer says `cum_tokens_delta` IS a spend figure. It is the one " +
				"cost column that verb carries, and a bullet about where to find cost evidence that " +
				"denies it is worse than a bullet that omits it")
		}
	})

	t.Run("the effort row attributes each objective to the file that stamps it", func(t *testing.T) {
		// The two walks above are blind to this: the objective walk reads the Objective(s) column and
		// the action walk extracts only `action=` tokens, so the Audit-record cell can hand an
		// objective to the wrong writer and stay green. That is the exact shape of the claim this row
		// carried before — `effort` spans both objectives, so getting the attribution backwards is
		// invisible to any check that only asks WHICH objectives the mechanism has.
		header := tableHeader(section, "Mechanism")
		if header == nil {
			t.Fatal("the per-mechanism table has no `Mechanism` header row")
		}
		auditCol := slices.Index(header, "Audit record")
		if auditCol < 0 {
			t.Fatalf("the per-mechanism header carries no `Audit record` column (header: %q)", header)
		}
		row := tableRowFor(section, string(tokenomics.MechanismEffort))
		if row == "" {
			t.Skip("no effort row; already reported by \"every mechanism is named by its code constant\"")
		}
		cells := tableCells(row)
		if len(cells) != len(header) {
			t.Skipf("the effort row has %d cells against a %d-column header", len(cells), len(header))
		}
		audit := cells[auditCol]

		for _, file := range []string{"done.go", "prime_economics.go", "prime.go"} {
			stamped, dynamic := objectivesStampedBy(t, file, tokenomics.MechanismEffort)
			clause := auditClauseFor(t, audit, file)
			if clause == "" {
				t.Errorf("the effort row's Audit record cell never names `%s`, which stamps %q; the "+
					"attribution has to say which file writes which objective or it cannot be "+
					"checked:\n%s", file, stamped, audit)
				continue
			}
			// The guard is a conjunction, and a clause that names one conjunct reads as a sufficient
			// condition for a record an operator will then go looking for and not find.
			if file == "prime_economics.go" {
				for _, conjunct := range []string{
					"failed admission", "would not fit a fresh session", "a level was already chosen",
				} {
					if !strings.Contains(clause, conjunct) {
						t.Errorf("the `prime_economics.go` clause does not name %q; that record is written "+
							"under three conditions and naming fewer states a sufficient condition the "+
							"code does not honour:\n%s", conjunct, clause)
					}
				}
			}
			for _, objective := range []string{telemetry.ObjectiveCapacity, telemetry.ObjectiveEfficiency} {
				named := strings.Contains(clause, "`"+objective+"`")
				// A file whose objective is computed genuinely writes both, so its clause has to say so.
				// A file that assigns a constant writes one, and naming the other there is the
				// misattribution.
				want := dynamic || slices.Contains(stamped, objective)
				if named == want {
					continue
				}
				if want {
					t.Errorf("`%s` stamps `%s` but the effort row's clause for it does not say so:\n%s",
						file, objective, clause)
				} else {
					t.Errorf("the effort row credits `%s` with `%s`; that file assigns only %q. An "+
						"operator filtering the log by objective would look for it in the wrong "+
						"file:\n%s", file, objective, stamped, clause)
				}
			}
		}
	})

	t.Run("the contract names the verb that proves an improvement", func(t *testing.T) {
		// #678 K12. Phase 7 is graded against a documented bar, and a bar that lives only in a design
		// directory is not a contract. `af telemetry compare` shipped in Phase 3
		// (telemetry_compare.go); until this section named it, the verb existed in the binary and in
		// .designs/ and nowhere an operator reads.
		// The two figures are DERIVED from the arithmetic that produces them, not re-typed. Both are
		// statements about a 5 + 5 split: change ProtocolArmSize and a doc still quoting 21 of 252 is
		// describing odds that no longer apply to the verdict it sits beside.
		favourable, total := tokenomics.ProtocolNullFalsePassOdds(
			tokenomics.ProtocolArmSize, tokenomics.ProtocolArmSize)

		arms := []string{compareArmBefore, compareArmAfter}

		for _, want := range []string{
			"### How improvement is proven",
			"af telemetry compare",
			"median(after) < min(before)",
			fmt.Sprintf("%s arms of %d runs each", numberWord(len(arms)), tokenomics.ProtocolArmSize),
			fmt.Sprintf("%d of %d splits", favourable, total),
			fmt.Sprintf("%.1f %%", float64(favourable)*100/float64(total)),
			// The metric. Naming the bar without naming what it is computed over lets a reader assume it
			// counts everything a run spent, which would make an input-side change look like a failure of
			// the intervention rather than of the metric's reach.
			"output tokens plus sub-agent tokens",
			// Direction. `void` is a claim about the comparison and `fail` a claim about the
			// intervention, and a contract that swapped them would have the verb disproving changes it
			// never measured.
			"voids rather than fails",
			// The verdict vocabulary, spelled exactly (docs/architecture/vocabulary.md).
			"`pass`", "`fail`", "`void`",
		} {
			if !strings.Contains(section, want) {
				t.Errorf("the contract is missing %q; a pass criterion nobody can quote is not a bar, "+
					"and the measurement protocol has no home in the repository until this section "+
					"carries it", want)
			}
		}

		// The metric's addends, read off the accumulation itself. "output tokens plus sub-agent tokens"
		// is a two-term claim, and the sentence stays quotable while a third term is smuggled into it —
		// which would tell an operator the bar rewards a reduction it does not measure.
		if addends := metricAddends(t); !slices.Equal(addends, []string{"out", "subagent"}) {
			t.Errorf("the pass metric now sums %q; the contract says it is output tokens plus sub-agent "+
				"tokens and nothing else", addends)
		} else {
			if sentence := sentenceContaining(section, "The metric is"); sentence != "" {
				for _, absent := range []string{"think", "cache", "input"} {
					if strings.Contains(strings.ToLower(sentence), absent) {
						t.Errorf("the metric sentence names %q, which the accumulation does not add in; a "+
							"bar credited with a reduction it cannot see is unfalsifiable:\n%s", absent, sentence)
					}
				}
			}
			// The paragraph goes on to say what thinking volume is NOT, which the sentence pin above
			// cannot reach — and an inverted "is summed in" one sentence later contradicts the metric
			// while quoting it correctly.
			if !strings.Contains(section, "deliberately not summed") {
				t.Error("`m.Metric` sums only output and sub-agent tokens, and the contract no longer " +
					"says thinking volume is deliberately not summed in")
			}
		}

		// Every reason a comparison can be void, read out of the constant block the verb prints from
		// rather than re-typed here — a list re-typed here closes over the checks that existed the day
		// it was written, and a seventeenth check would ship with the section still claiming the
		// vocabulary is closed. A void naming a reason the contract never mentions is the unfalsifiable
		// refusal the named checks exist to replace.
		for _, check := range declaredConstants(t, "telemetry_compare.go", "compareCheck") {
			if !strings.Contains(section, "`"+check+"`") {
				t.Errorf("the contract's void vocabulary omits the check %q; the section claims the list "+
					"is closed, so an operator holding a void naming this check cannot look it up", check)
			}
		}

		// The two surfaces are told apart by what each arm holds fixed, and their switches are not
		// interchangeable: comparePostureCheck applies to surface b alone, and the protocol rule for
		// surface a is that both arms run with the improvement loop off. A document that swapped the two
		// bullets would send an operator to measure the control as the treatment.
		for _, tc := range []struct{ surface, must string }{
			{compareSurfaceB, "af tokenomics off"},
			{compareSurfaceA, "af improvement off"},
		} {
			label := "**Surface " + strings.ToUpper(tc.surface) + "**"
			// Gathered across its continuation lines: the switch each arm moves is named where the bullet
			// gets specific, which is never the first line.
			lines := strings.Split(section, "\n")
			var bullet string
			for i, line := range lines {
				if !strings.HasPrefix(strings.TrimSpace(line), "- "+label) {
					continue
				}
				bullet = line
				for _, cont := range lines[i+1:] {
					if !strings.HasPrefix(cont, "  ") || strings.HasPrefix(strings.TrimSpace(cont), "- ") {
						break
					}
					bullet += "\n" + cont
				}
				break
			}
			if bullet == "" {
				t.Errorf("the contract defines no %s; --surface takes %q and %q, and a value the document "+
					"never defines is one an operator has to guess", label, compareSurfaceA, compareSurfaceB)
				continue
			}
			if !strings.Contains(bullet, tc.must) {
				t.Errorf("the %s bullet does not name %q, the switch that arm moves; the surfaces differ "+
					"only in what is held fixed, so a bullet naming the other one's switch describes the "+
					"other experiment:\n%s", label, tc.must, bullet)
			}
		}
	})

	t.Run("the rewritten rows no longer carry their falsified claims", func(t *testing.T) {
		// The class of bug this file exists for, one table below the rows #673 pinned. Each string here
		// described behaviour that has been DELETED from the code, so pinning its ABSENCE is what makes
		// the rewrite unrevertable — the same positive-and-negative shape the dispatch row uses.
		for _, tc := range []struct{ mechanism, gone, why string }{
			{"effort", "free < appetite",
				"the prime appetite band was deleted from internal/tokenomics/advisory.go; the level is " +
					"now selected at the launch legs from the efficiency predicate"},
			{"interview", "none today",
				"prime.go writes interview/advise and done.go writes interview/handoff, both under " +
					"objective=efficiency"},
		} {
			row := tableRowFor(section, tc.mechanism)
			if row == "" {
				t.Errorf("the contract has no `%s` row", tc.mechanism)
				continue
			}
			if strings.Contains(row, tc.gone) {
				t.Errorf("the `%s` row still claims %q — %s; an operator grepping for that trigger finds "+
					"a mechanism no code computes:\n%s", tc.mechanism, tc.gone, tc.why, row)
			}
		}

		// thrift gained a SECOND trigger, keyed on learned generation history with no window operand in
		// it. A row describing one trigger describes a mechanism that no longer exists.
		if row := tableRowFor(section, "thrift"); row == "" {
			t.Error("the contract has no `thrift` row")
		} else if !strings.Contains(row, "repeat_reads") {
			t.Errorf("the `thrift` row does not name the efficiency trigger's operand, `repeat_reads`; "+
				"without it the row still reads as the single occupancy trigger it used to be:\n%s", row)
		}

		// Guarantee 3 promises the table says "none today" where a mechanism leaves no record. Removing
		// the phrase from `interview` is correct; removing it from the section would falsify the
		// guarantee, because `escalate` is now its only referent.
		if !strings.Contains(section, "none today") {
			t.Error("guarantee 3 promises the table says \"none today\" where a mechanism leaves no " +
				"record, and no row says it any more; the guarantee has lost its referent")
		}
	})
}
