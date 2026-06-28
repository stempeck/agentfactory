// Package feedback is the C6 gate-aware feedback writer for the web module.
//
// The af-core design-feedback mechanism is a FILE: feedback-form.md inside each prototype-v{N}/
// directory is the SOLE AUTHORITY (mail is notification-only — data.md Decision 3). A design agent
// (web-design / gherkin-design) parks at a human gate design-feedback-{N} and BLOCKS until that form
// has real content; a hard, mechanically-enforced interlock in the formula
// (web-design.formula.toml:578-588) refuses to advance while the form is the blank template. It
// releases only on a ticked [x] box, a "Decision: APPROVED|REJECTED" verdict ON the Decision line,
// or real (non-placeholder) notes.
//
// This writer therefore does two things, in order:
//
//  1. VERIFY the gate. It writes nothing unless the prototype's owning agent is verified parked at
//     the matching gate — state "ready", is_gate true, gate_id == "design-feedback-{N}" where N is
//     the latest prototype version on disk. It learns this from the read-model's per-agent AgentView
//     (the gate state af-core already aggregates with the correct per-agent CWD), NOT by exec-ing
//     `af step current` itself — the web exec Runner now pins the af child's cmd.Dir to the factory
//     root, not to each agent's own CWD, so a direct `af step current` would still mis-report
//     no_formula (IMPLREADME Gotchas, Option a). Off-gate / no owner
//     ⇒ an honest "feedback not currently open for this prototype" and no write.
//
//  2. WRITE the contract. It serializes the operator's submission into feedback-form.md in the shape
//     the gate validates, and refuses to write content that would not release the gate (so the UI
//     never silently produces a blank form the agent will reject).
//
// Stdlib only. The form is written to disk; the agent picks it up on its next dispatch via git pull
// (the writer does not push — it honestly reports "saved — will be picked up next iteration").
package feedback

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/stempeck/agentfactory-web/internal/readmodel"
)

// gatePrefix is the af-core design-feedback gate-id prefix, re-declared locally because the web
// module cannot import internal/… (Go internal seal + separate go.mod). Source: the five gates
// design-feedback-{1..5} in web-design.formula.toml (L420, 656, 798, 923, 1049).
const gatePrefix = "design-feedback-"

// the honest operator-facing messages.
const (
	msgNotOpen = "feedback not currently open for this prototype"
	msgEmpty   = "feedback is empty — add a decision, tick a box, or write notes before sending"
	msgSaved   = "saved — will be picked up by the agent on its next iteration"
)

// AgentSource yields the honest per-agent views (gate state included). readmodel.ReadModel satisfies
// it directly via Assemble; tests inject a canned fake. This is the seam the IMPLREADME's Option (a)
// reuses so the writer never execs.
type AgentSource interface {
	Assemble(ctx context.Context) ([]readmodel.AgentView, error)
}

// Input is the parsed feedback side-panel submission.
type Input struct {
	Decision string   // "APPROVED" | "REJECTED" | "" (blank ⇒ iterate); rendered ON the "- Decision:" line
	Checks   []string // ticked impression items ⇒ rendered as "- [x] <item>"
	Notes    string   // real free-text ⇒ rendered as a "Notes:" labeled line under ## Notes
}

// Result is the honest outcome envelope (parallels server.Envelope semantics).
type Result struct {
	OK      bool
	Message string
	Path    string // the written feedback-form.md (set only on success)
}

// Writer writes gate-verified feedback-form.md files under the factory root.
type Writer struct {
	root   string
	agents AgentSource
}

// New builds a Writer (mirrors config.New(root, …) / dispatch.New(…)).
func New(root string, agents AgentSource) *Writer {
	return &Writer{root: root, agents: agents}
}

// OpenFrom is the PURE (no-exec) gate predicate: it correlates an already-fetched set of agent views
// against prototype id. The server annotates GET /api/prototypes by calling Assemble ONCE and reusing
// the views across every prototype, instead of one `af agents list --json` per prototype.
func (w *Writer) OpenFrom(views []readmodel.AgentView, id string) bool {
	_, version, ok := w.onDisk(id)
	if !ok {
		return false
	}
	return gateOpenFor(views, id, version)
}

// Submit writes feedback-form.md for prototype id IF its owning agent is verified at the matching
// gate, with content that satisfies the gate-release contract. Off-gate ⇒ ok:false honest message,
// nothing written. A transport failure reading gate state is returned as an error (handler ⇒ 502).
func (w *Writer) Submit(ctx context.Context, id string, in Input) (Result, error) {
	protoDir, version, ok := w.onDisk(id)
	if !ok {
		// no such design dir / no prototype version yet ⇒ feedback simply is not open.
		return Result{OK: false, Message: msgNotOpen}, nil
	}
	views, err := w.agents.Assemble(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read gate state: %w", err)
	}
	if !gateOpenFor(views, id, version) {
		return Result{OK: false, Message: msgNotOpen}, nil
	}
	content := renderForm(version, in)
	if !releasesGate(content) {
		// never write a blank/placeholder form the formula interlock would reject.
		return Result{OK: false, Message: msgEmpty}, nil
	}
	formPath := filepath.Join(protoDir, "feedback-form.md")
	if err := os.WriteFile(formPath, []byte(content), 0o644); err != nil {
		return Result{}, fmt.Errorf("write feedback-form.md: %w", err)
	}
	return Result{OK: true, Message: msgSaved, Path: formPath}, nil
}

// onDisk resolves the active prototype dir + version for id from DISK ONLY (no exec). ok=false for an
// invalid id, a missing design dir, or no prototype version present.
func (w *Writer) onDisk(id string) (protoDir string, version int, ok bool) {
	if !validID(id) {
		return "", 0, false
	}
	base := filepath.Join(w.root, ".designs", id)
	if fi, e := os.Stat(base); e != nil || !fi.IsDir() {
		return "", 0, false
	}
	version = latestVersion(base)
	if version == 0 {
		return "", 0, false
	}
	return filepath.Join(base, fmt.Sprintf("prototype-v%d", version)), version, true
}

// gateOpenFor reports whether some agent owns prototype id AND is parked at gate
// design-feedback-{version}. The version comes from DISK and the gate must match it EXACTLY — so a
// wrong-gate agent (e.g. still at design-feedback-1 while v2 is on disk) is refused. That is the safe
// failure direction: during a brief disk/read-model skew we refuse rather than write the wrong
// iteration's form. gate_id "" cleanly distinguishes a non-gate ready step (the read-model normalizes
// IsGate := is_gate || gate_id != "", readmodel.go:138).
func gateOpenFor(views []readmodel.AgentView, id string, version int) bool {
	wantGate := fmt.Sprintf("%s%d", gatePrefix, version)
	for _, v := range views {
		if ownsPrototype(v, id) && v.StepState == "ready" && v.IsGate && v.GateID == wantGate {
			return true
		}
	}
	return false
}

// ownsPrototype reports whether an agent owns prototype id, by correlating its resolved output_dir
// (a configurable formula input — default .designs/web-ui for web-design, .designs/gherkin-ui for
// gherkin-design) to the id. No stored id→agent map exists, so this is the correlation (IMPLREADME
// Gotchas). The basename of the cleaned output_dir is the design id.
func ownsPrototype(v readmodel.AgentView, id string) bool {
	od := strings.TrimSpace(v.Inputs["output_dir"])
	if od == "" {
		return false
	}
	return filepath.Base(filepath.Clean(od)) == id
}

var protoVersionRE = regexp.MustCompile(`^prototype-v([0-9]+)$`)

// latestVersion returns the highest N for which base/prototype-v{N}/ exists. Unlike the proto
// server's enumeration it does NOT require an index.html — the feedback target is the directory that
// holds the form, which exists once the agent parked (it writes the blank form there).
func latestVersion(base string) int {
	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}
	best := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := protoVersionRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n > best {
			best = n
		}
	}
	return best
}

var idRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func validID(id string) bool { return idRE.MatchString(id) }

// ---- form serialization ----

// renderForm produces a feedback-form.md in the shape web-design.formula.toml:431-468 validates.
// It is intentionally a faithful SUBSET: the recognizable headings plus exactly the fields the
// operator supplied, serialized so that any non-empty submission satisfies the gate-release regex.
//
// Decision mapping (the critical pitfall, Pattern-Matcher PART A): "APPROVED" finalizes, "REJECTED"
// halts — these go ON the "- Decision:" line (HAS_WORD). A blank Decision (the "request changes" /
// iterate path) leaves the placeholder and relies on the notes (HAS_NOTE) so the agent loops.
func renderForm(version int, in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Design Feedback - Iteration %d\n\n", version)
	b.WriteString("Submitted via the AgentFactory web console.\n\n")

	b.WriteString("## Decision\n")
	if d := normalizeDecision(in.Decision); d != "" {
		fmt.Fprintf(&b, "- Decision: %s\n", d)
		b.WriteString("  (APPROVED finalizes; REJECTED halts; blank iterates on the notes below.)\n\n")
	} else {
		b.WriteString("- Decision: ______\n")
		b.WriteString("  (Blank ⇒ iterate on the feedback below. Write APPROVED to finalize or REJECTED to halt.)\n\n")
	}

	b.WriteString("## Overall Impressions\n")
	if len(in.Checks) > 0 {
		for _, c := range in.Checks {
			c = collapse(c)
			if c == "" {
				continue
			}
			fmt.Fprintf(&b, "- [x] %s\n", c)
		}
	} else {
		b.WriteString("- [ ] (no impressions ticked)\n")
	}
	b.WriteString("\n")

	b.WriteString("## Notes\n")
	if n := collapse(in.Notes); n != "" {
		// a "Notes:" labeled line with real text after the colon satisfies HAS_NOTE (and is NOT a
		// "_"/"[" placeholder). Keep it on one line so the anchored label→value match is reliable.
		fmt.Fprintf(&b, "Notes: %s\n", n)
	} else {
		b.WriteString("[free-form feedback]\n")
	}
	return b.String()
}

// normalizeDecision maps an operator verdict to the exact gate keyword, or "" for anything that
// should iterate. "request changes" / "changes" deliberately do NOT map to REJECTED (that halts the
// workflow) — they fall through to "" so the loop continues on the notes.
func normalizeDecision(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "approved", "approve":
		return "APPROVED"
	case "rejected", "reject":
		return "REJECTED"
	default:
		return ""
	}
}

// collapse trims and flattens internal newlines to single spaces so a value sits on one line.
func collapse(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }), " ")
}

// ---- gate-release verification (mirror web-design.formula.toml:578-588 in Go) ----

var (
	// HAS_WORD: APPROVED|REJECTED ON the "- Decision:" line only (case-insensitive, per-line).
	reHasWord = regexp.MustCompile(`(?im)^[[:space:]]*[-*]?[[:space:]]*Decision:.*(APPROVED|REJECTED)`)
	// HAS_NOTE stage 1: a labeled line with a real value after the colon.
	reNoteLabel = regexp.MustCompile(`(?i)(palette|typography|signature|decision|notes?):[[:space:]]*[^[:space:]]`)
	// HAS_NOTE stage 2 (exclusion): the value is a placeholder ("_…" or "[").
	rePlaceholder = regexp.MustCompile(`:[[:space:]]*(_+|\[)`)
)

// releasesGate reports whether form content would release the formula's design-feedback gate —
// i.e. at least one of HAS_CHECK / HAS_WORD / HAS_NOTE is true. This guards against ever writing a
// form the agent's interlock would reject.
func releasesGate(form string) bool {
	if strings.Contains(strings.ToLower(form), "[x]") { // HAS_CHECK
		return true
	}
	if reHasWord.MatchString(form) { // HAS_WORD
		return true
	}
	for _, ln := range strings.Split(form, "\n") { // HAS_NOTE
		if reNoteLabel.MatchString(ln) && !rePlaceholder.MatchString(ln) {
			return true
		}
	}
	return false
}
