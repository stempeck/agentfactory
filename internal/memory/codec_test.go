package memory

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func fullNote() Note {
	return Note{
		ID:          "2026-08-15T0132Z-stale-designs-grep",
		Agent:       "manager",
		Formula:     "rootcause-all",
		Run:         "wt-cfa6f1/4b7c266f",
		Type:        TypeGotcha,
		Created:     time.Date(2026, 8, 15, 1, 32, 0, 0, time.UTC),
		Evidence:    []string{"issue#626", "pr#516", "commit 9f2d04d3"},
		Status:      StatusActive,
		GraduatedTo: "",
		Expires:     time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
		Body:        "The outline-locator grep matches stale `.designs/130/` files when...\n",
	}
}

func TestCodec_RoundTrip(t *testing.T) {
	want := fullNote()
	got := Parse(Emit(want))

	if got.Malformed {
		t.Fatal("a note this codec emitted parsed back as malformed")
	}
	if got.ID != want.ID {
		t.Errorf("ID: got %q, want %q", got.ID, want.ID)
	}
	if got.Agent != want.Agent {
		t.Errorf("Agent: got %q, want %q", got.Agent, want.Agent)
	}
	if got.Formula != want.Formula {
		t.Errorf("Formula: got %q, want %q", got.Formula, want.Formula)
	}
	if got.Run != want.Run {
		t.Errorf("Run: got %q, want %q", got.Run, want.Run)
	}
	if got.Type != want.Type {
		t.Errorf("Type: got %q, want %q", got.Type, want.Type)
	}
	if !got.Created.Equal(want.Created) {
		t.Errorf("Created: got %v, want %v", got.Created, want.Created)
	}
	if strings.Join(got.Evidence, "|") != strings.Join(want.Evidence, "|") {
		t.Errorf("Evidence: got %v, want %v", got.Evidence, want.Evidence)
	}
	if got.Status != want.Status {
		t.Errorf("Status: got %q, want %q", got.Status, want.Status)
	}
	if got.GraduatedTo != want.GraduatedTo {
		t.Errorf("GraduatedTo: got %q, want %q", got.GraduatedTo, want.GraduatedTo)
	}
	if !got.Expires.Equal(want.Expires) {
		t.Errorf("Expires: got %v, want %v", got.Expires, want.Expires)
	}
	if got.Body != want.Body {
		t.Errorf("Body: got %q, want %q", got.Body, want.Body)
	}
}

// TestFrontmatterCanonicalShape pins the on-disk dialect literally. The dialect is ours to
// define (design-doc.md:82 escape hatch E2) and Obsidian must render it as Properties, so the
// shape is a contract and not an implementation detail — a re-spelling here must fail loudly.
func TestFrontmatterCanonicalShape(t *testing.T) {
	got := string(Emit(fullNote()))
	want := "---\n" +
		"id: 2026-08-15T0132Z-stale-designs-grep\n" +
		"agent: manager\n" +
		"formula: rootcause-all\n" +
		"run: wt-cfa6f1/4b7c266f\n" +
		"type: gotcha\n" +
		"created: 2026-08-15T01:32:00Z\n" +
		"evidence: [\"issue#626\",\"pr#516\",\"commit 9f2d04d3\"]\n" +
		"status: active\n" +
		"graduated_to: \"\"\n" +
		"expires: 2026-11-15\n" +
		"---\n" +
		"The outline-locator grep matches stale `.designs/130/` files when...\n"
	if got != want {
		t.Errorf("canonical emit:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The spelling of a value CONTAINING '#' is part of the same contract: it is emitted bare,
	// because the reader only treats '#' as a comment opener where it starts the value or follows
	// whitespace. The design's own note spells graduated_to exactly this way, and quoting it
	// instead would change every file on disk without changing a single parsed value — the kind
	// of drift a canonical format exists to make loud.
	graduated := fullNote()
	graduated.Status = StatusGraduated
	graduated.GraduatedTo = "issue#631"
	if out := string(Emit(graduated)); !strings.Contains(out, "\ngraduated_to: issue#631\n") {
		t.Errorf("a value containing '#' must be emitted bare:\n%s", out)
	}
}

// TestCodec_CanonicalEmitIsStable proves emit is a fixpoint of emit∘parse. This is what
// "canonical" buys: one spelling, fixed key order, and a Graduate/Expire rewrite that cannot
// drift the file's shape.
func TestCodec_CanonicalEmitIsStable(t *testing.T) {
	once := Emit(fullNote())
	twice := Emit(Parse(once))
	if string(once) != string(twice) {
		t.Errorf("emit is not a fixpoint:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// TestCodec_EmitsFromStructFieldsOnly pins the deliberate consequence of "emit canonically from
// struct fields only" (IMPLREADME): an unknown frontmatter key an operator added in Obsidian is
// READ tolerantly but is DROPPED on the next agent rewrite. It is a decision, not a surprise.
func TestCodec_EmitsFromStructFieldsOnly(t *testing.T) {
	raw := "---\n" +
		"id: n1\n" +
		"agent: manager\n" +
		"type: gotcha\n" +
		"created: 2026-08-15T01:32:00Z\n" +
		"status: active\n" +
		"obsidian_tag: someone-added-this\n" +
		"---\n" +
		"body\n"

	n := Parse([]byte(raw))
	if n.Malformed {
		t.Error("an unknown key must be tolerated, not treated as malformed")
	}
	if n.ID != "n1" {
		t.Errorf("ID: got %q, want %q", n.ID, "n1")
	}
	if out := string(Emit(n)); strings.Contains(out, "obsidian_tag") {
		t.Errorf("emit echoed an unknown key back out:\n%s", out)
	}
}

// TestCodec_KeyOrderOfInputDoesNotChangeOutput — canonical means the OUTPUT order is fixed.
func TestCodec_KeyOrderOfInputDoesNotChangeOutput(t *testing.T) {
	scrambled := "---\n" +
		"status: active\n" +
		"created: 2026-08-15T01:32:00Z\n" +
		"agent: manager\n" +
		"type: gotcha\n" +
		"id: n1\n" +
		"---\n" +
		"body\n"
	inOrder := "---\n" +
		"id: n1\n" +
		"agent: manager\n" +
		"type: gotcha\n" +
		"created: 2026-08-15T01:32:00Z\n" +
		"status: active\n" +
		"---\n" +
		"body\n"

	if a, b := string(Emit(Parse([]byte(scrambled)))), string(Emit(Parse([]byte(inOrder)))); a != b {
		t.Errorf("input key order leaked into the output:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}

// TestCodec_MalformedDegradesToBodyOnly is the R3 mitigation. Every one of these is a plausible
// hand edit in Obsidian, and not one of them may be an error: Parse feeds a hook path where
// ADR-007 requires silence, so "malformed" has to be a value, not a failure.
func TestCodec_MalformedDegradesToBodyOnly(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"no opening delimiter", "id: n1\nagent: manager\n---\nthe body\n"},
		{"no closing delimiter", "---\nid: n1\nagent: manager\nthe body\n"},
		{"line with no colon", "---\nid: n1\nthis line has no colon\nstatus: active\n---\nthe body\n"},
		{"unparseable created", "---\nid: n1\ncreated: last tuesday\n---\nthe body\n"},
		{"unparseable expires", "---\nid: n1\nexpires: soon-ish\n---\nthe body\n"},
		{"unquoted evidence array", "---\nid: n1\nevidence: [issue#626, pr#516]\n---\nthe body\n"},
		{"single-quoted evidence array", "---\nid: n1\nevidence: ['issue#626']\n---\nthe body\n"},
		{"non-string evidence element", "---\nid: n1\nevidence: [1, 2]\n---\nthe body\n"},
		{"trailing comma in evidence", "---\nid: n1\nevidence: [\"a\",]\n---\nthe body\n"},
		{"empty file", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := Parse([]byte(tc.raw))
			if !n.Malformed {
				t.Errorf("Parse(%q): Malformed = false, want true", tc.raw)
			}
			if !strings.Contains(n.Body, "the body") && tc.raw != "" {
				t.Errorf("Parse(%q): body not preserved, got %q", tc.raw, n.Body)
			}
		})
	}
}

// TestMalformedNoteIsNeverInjected closes the intersection the acceptance criteria leave open:
// AC3 covers malformed PARSING and AC4 covers expired/graduated EXCLUSION, but nothing covers
// a malformed note reaching injection — and Slice is the only thing that feeds it
// (api.md:148, "served body-only to status, never to injection").
func TestMalformedNoteIsNeverInjected(t *testing.T) {
	n := Parse([]byte("---\nid: n1\nthis line has no colon\n---\nsecret body\n"))
	if !n.Malformed {
		t.Fatal("fixture is not malformed; the test proves nothing")
	}
	n.Status = StatusActive
	n.Created = sliceNow

	b := DefaultBudget()
	b.Now = sliceNow

	if got := Slice([]Note{n}, b); len(got) != 0 {
		t.Errorf("Slice served a malformed note: got %d notes, want 0", len(got))
	}
}

// TestCodec_BodyDashesCannotFakeFrontmatter — the parser stops at the FIRST closing delimiter
// and never re-scans a body, so a note whose body quotes a frontmatter block cannot promote
// itself to graduated.
func TestCodec_BodyDashesCannotFakeFrontmatter(t *testing.T) {
	body := "Here is what a graduated note looks like:\n\n" +
		"---\n" +
		"status: graduated\n" +
		"graduated_to: issue#1\n" +
		"---\n\n" +
		"...and that is why.\n"
	raw := "---\n" +
		"id: n1\n" +
		"agent: manager\n" +
		"type: gotcha\n" +
		"created: 2026-08-15T01:32:00Z\n" +
		"status: active\n" +
		"---\n" +
		body

	n := Parse([]byte(raw))
	if n.Malformed {
		t.Fatal("a well-formed note with dashes in its body parsed as malformed")
	}
	if n.Status != StatusActive {
		t.Errorf("Status: got %q, want %q — the body forged a frontmatter field", n.Status, StatusActive)
	}
	if n.GraduatedTo != "" {
		t.Errorf("GraduatedTo: got %q, want empty — the body forged a frontmatter field", n.GraduatedTo)
	}
	if n.Body != body {
		t.Errorf("Body: got %q, want %q", n.Body, body)
	}
	if again := Parse(Emit(n)); again.Body != body || again.Status != StatusActive {
		t.Errorf("round trip changed the body or status: body %q, status %q", again.Body, again.Status)
	}

	// The harder shape, and the one the fixture above cannot reach: a body that BEGINS with a
	// fence. Here the body is itself a syntactically valid document, so a parser that re-scanned
	// what it had already decided was body would promote this block — the "first closing fence
	// ends the frontmatter, permanently" rule is the only thing standing between an agent's
	// prose and a forged graduation.
	leading := "---\nstatus: graduated\ngraduated_to: issue#1\n---\n\n...and that is why.\n"
	fromZero := Parse([]byte("---\nid: n2\nagent: manager\ntype: gotcha\nstatus: active\n---\n" + leading))
	if fromZero.Malformed {
		t.Fatal("a note whose body opens with a fence parsed as malformed")
	}
	if fromZero.Status != StatusActive || fromZero.GraduatedTo != "" {
		t.Errorf("the body forged a frontmatter field: status %q, graduated_to %q",
			fromZero.Status, fromZero.GraduatedTo)
	}
	if fromZero.Body != leading {
		t.Errorf("Body: got %q, want %q", fromZero.Body, leading)
	}
}

// TestFrontmatter_ZeroValueOptionalFieldsRoundTrip pins data.md:60-61 — graduated_to renders as
// an explicit empty string and an empty expires means "no TTL, review-nagged" rather than
// "expired at the epoch".
func TestFrontmatter_ZeroValueOptionalFieldsRoundTrip(t *testing.T) {
	n := fullNote()
	n.Formula = ""
	n.GraduatedTo = ""
	n.Expires = time.Time{}
	n.Evidence = nil

	got := Parse(Emit(n))
	if got.Malformed {
		t.Fatal("a note with empty optional fields parsed as malformed")
	}
	if got.Formula != "" {
		t.Errorf("Formula: got %q, want empty", got.Formula)
	}
	if got.GraduatedTo != "" {
		t.Errorf("GraduatedTo: got %q, want empty", got.GraduatedTo)
	}
	if !got.Expires.IsZero() {
		t.Errorf("Expires: got %v, want the zero time", got.Expires)
	}
	if len(got.Evidence) != 0 {
		t.Errorf("Evidence: got %v, want empty", got.Evidence)
	}
}

// TestCodec_ValuesNeedingQuotesRoundTrip — the emitter quotes anything that is not a bare-safe
// scalar so that emit∘parse stays closed for free-form Phase-2 input.
func TestCodec_ValuesNeedingQuotesRoundTrip(t *testing.T) {
	n := fullNote()
	n.Run = `wt-1 "quoted" / spaced`
	n.GraduatedTo = `doc:docs/a b.md`
	n.Formula = "has spaces"

	got := Parse(Emit(n))
	if got.Malformed {
		t.Fatal("a note with quoted values parsed as malformed")
	}
	if got.Run != n.Run {
		t.Errorf("Run: got %q, want %q", got.Run, n.Run)
	}
	if got.GraduatedTo != n.GraduatedTo {
		t.Errorf("GraduatedTo: got %q, want %q", got.GraduatedTo, n.GraduatedTo)
	}
	if got.Formula != n.Formula {
		t.Errorf("Formula: got %q, want %q", got.Formula, n.Formula)
	}
	if second := Emit(got); string(second) != string(Emit(n)) {
		t.Error("emit is not a fixpoint for quoted values")
	}
}

// TestCodec_AbsentStatusReadsAsActive — an operator who deleted the status line still wants the
// note. Treating a missing status as unservable would silently drop it from every read path.
func TestCodec_AbsentStatusReadsAsActive(t *testing.T) {
	n := Parse([]byte("---\nid: n1\nagent: manager\ncreated: 2026-08-15T01:32:00Z\n---\nbody\n"))
	if n.Malformed {
		t.Fatal("a note without a status line parsed as malformed")
	}
	if n.Status != StatusActive {
		t.Errorf("Status: got %q, want %q", n.Status, StatusActive)
	}
}

// TestCodec_ClosedVocabulariesFlagRatherThanGoDark closes the gap between "unreadable" and
// "unservable". status and type are CLOSED vocabularies (note.go:8-23): a value outside them is
// one this package cannot act on — Slice drops the note and TTLForType keys it wrong — so the
// only honest outcomes are "served" or "flagged". Assigning the value verbatim and returning
// Malformed=false is neither: the note disappears from injection and every surface reports the
// vault as healthy, which is the exact silent darkness design-doc.md:112 requires degradation
// to prevent.
func TestCodec_ClosedVocabulariesFlagRatherThanGoDark(t *testing.T) {
	cases := []struct{ name, line string }{
		{"typo in status", "status: activ"},
		{"wrong case status", "status: Active"},
		{"invented status", "status: archived"},
		{"typo in type", "type: gotchas"},
		{"invented type", "type: documentation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := Parse([]byte("---\nid: n1\nagent: manager\n" + tc.line + "\n---\nthe body\n"))
			if !n.Malformed {
				t.Errorf("Parse(%q): Malformed = false — the note is unservable and nothing says so", tc.line)
			}
			if n.Body != "the body\n" {
				t.Errorf("Parse(%q): body not preserved, got %q", tc.line, n.Body)
			}
		})
	}

	// Positive control: the vocabularies themselves are never flagged. Without it, a Parse that
	// marked EVERY note malformed would satisfy the table above.
	for _, status := range []string{StatusActive, StatusGraduated, StatusExpired} {
		for _, noteType := range []string{TypeGotcha, TypeModelBehavior, TypeOps, TypeOutcome, TypeImprovement} {
			raw := "---\nid: n1\nagent: manager\ntype: " + noteType + "\nstatus: " + status + "\n---\nthe body\n"
			if n := Parse([]byte(raw)); n.Malformed {
				t.Errorf("Parse(type %q, status %q): Malformed = true, want false", noteType, status)
			}
		}
	}
}

// TestCodec_InlineCommentsAreDialect parses the design's OWN canonical note (data.md:48-64,
// reproduced verbatim in IMPLREADME_PHASE1.md), annotations and all. The dialect is ours to
// define (design-doc.md:82, escape hatch E2) and it has to accept the shape the design ships,
// because YAML and Obsidian both do. Absorbing the annotation into the value instead is not a
// cosmetic loss: `type: improvement  # nagged` becomes an unknown type, and the one note class
// the design says must NEVER expire silently acquires a 90-day TTL.
func TestCodec_InlineCommentsAreDialect(t *testing.T) {
	raw := "---\n" +
		"id: 2026-08-15T0132Z-stale-designs-grep\n" +
		"agent: manager\n" +
		"formula: rootcause-all          # optional; scoping key\n" +
		"run: wt-cfa6f1/4b7c266f         # attribution: worktree/session\n" +
		"type: improvement               # gotcha | model-behavior | ops | outcome | improvement\n" +
		"created: 2026-08-15T01:32:00Z\n" +
		"evidence: [\"issue#626\", \"pr#516\"]\n" +
		"status: active                  # active | graduated | expired\n" +
		"graduated_to: \"\"                # e.g. \"issue#631\"\n" +
		"expires:                        # empty = review-nagged\n" +
		"---\n" +
		"the body\n"

	n := Parse([]byte(raw))
	if n.Malformed {
		t.Fatal("the design's own canonical note parsed as malformed")
	}
	if n.Formula != "rootcause-all" {
		t.Errorf("Formula: got %q, want %q", n.Formula, "rootcause-all")
	}
	if n.Run != "wt-cfa6f1/4b7c266f" {
		t.Errorf("Run: got %q, want %q", n.Run, "wt-cfa6f1/4b7c266f")
	}
	if n.Type != TypeImprovement {
		t.Errorf("Type: got %q, want %q", n.Type, TypeImprovement)
	}
	if n.Status != StatusActive {
		t.Errorf("Status: got %q, want %q", n.Status, StatusActive)
	}
	if n.GraduatedTo != "" {
		t.Errorf("GraduatedTo: got %q, want empty", n.GraduatedTo)
	}
	if !n.Expires.IsZero() {
		t.Errorf("Expires: got %v, want the zero time", n.Expires)
	}
	if strings.Join(n.Evidence, "|") != "issue#626|pr#516" {
		t.Errorf("Evidence: got %v, want [issue#626 pr#516]", n.Evidence)
	}
	// The consequence, stated as an assertion: this note has no TTL.
	if ttl := TTLForType(n.Type); ttl != 0 {
		t.Errorf("TTLForType(%q) = %v, want 0 — an improvement note must not expire", n.Type, ttl)
	}

	// A TAB opens a comment too. Obsidian and hand-editing both produce them, and the parser
	// handles '\t' explicitly, so the behaviour is a decision rather than an accident.
	tabbed := Parse([]byte("---\nid: n1\nformula: rootcause-all\t# tab-separated annotation\n---\nbody\n"))
	if tabbed.Malformed {
		t.Fatal("a tab-separated annotation parsed as malformed")
	}
	if tabbed.Formula != "rootcause-all" {
		t.Errorf("Formula after a tabbed comment: got %q, want %q", tabbed.Formula, "rootcause-all")
	}

	// The other side: a '#' that is CONTENT survives. Only a whitespace-preceded '#' opens a
	// comment, and a quoted value is only cut where what remains is still well-formed.
	kept := Parse([]byte("---\nid: n1\ngraduated_to: issue#631\nevidence: [\"a #b\"]\nrun: \"wt-1 #2\"\n---\nbody\n"))
	if kept.Malformed {
		t.Fatal("a note whose values contain '#' parsed as malformed")
	}
	if kept.GraduatedTo != "issue#631" {
		t.Errorf("GraduatedTo: got %q, want %q", kept.GraduatedTo, "issue#631")
	}
	if strings.Join(kept.Evidence, "|") != "a #b" {
		t.Errorf("Evidence: got %v, want [a #b]", kept.Evidence)
	}
	if kept.Run != "wt-1 #2" {
		t.Errorf("Run: got %q, want %q", kept.Run, "wt-1 #2")
	}
}

// TestCodec_EmitParseIsClosedForHostileScalars is the test that would have caught the emitter
// and the comment-stripper disagreeing about a leading '#'. Emit's quoting policy and Parse's
// comment rule are two halves of one contract, and nothing else in this file round-trips a value
// through the emitter's own choice of bare-versus-quoted. The fields are Formula and Run because
// they are the two the CLI fills from free text: ID is sanitized, Agent is validated,
// GraduatedTo is vocabulary-checked, Evidence goes through JSON.
//
// The consequence when this breaks is not cosmetic: run is a note's only provenance once its
// worktree is gone, and formula is the scope key ranking uses.
func TestCodec_EmitParseIsClosedForHostileScalars(t *testing.T) {
	values := []string{
		"#x", "#", "#631", "#nightly",
		"x #y", "a#b", "issue#631", "a\t#b", "-#no",
		"", " ", "  spaced  ", `"quoted"`, `a "q" b`,
		"a: b", "---", "[not json]", `["a"]`, "a\nb", "…unicode", "a\r\nb",
		"@nightly", "@", "run@2026-08-15", "`tick", "!bang", "&anchor", "*alias", "%directive",
	}
	for _, v := range values {
		t.Run(strconv.Quote(v), func(t *testing.T) {
			n := fullNote()
			n.Formula = v
			n.Run = v

			emitted := Emit(n)
			got := Parse(emitted)
			if got.Malformed {
				t.Fatalf("Parse(Emit(%q)) is malformed; emitted:\n%s", v, emitted)
			}
			// Byte-identical, with no exceptions: the reader trims the raw LINE, and every value
			// whose own whitespace matters is one the emitter has already quoted.
			if got.Formula != v {
				t.Errorf("Formula: got %q, want %q; emitted:\n%s", got.Formula, v, emitted)
			}
			if got.Run != v {
				t.Errorf("Run: got %q, want %q; emitted:\n%s", got.Run, v, emitted)
			}
			// Emit must be a fixpoint of Emit∘Parse, or a Graduate on this note would drift the
			// file's shape a second time.
			if again := Emit(got); string(again) != string(Emit(Parse(again))) {
				t.Errorf("emit is not a fixpoint for %q:\n--- once ---\n%s\n--- twice ---\n%s",
					v, again, Emit(Parse(again)))
			}
		})
	}
}

// TestCodec_EmitTerminatesTheBody — the one normalisation Emit applies to a body. These are
// files, and a file that does not end in a newline makes every downstream `cat`, diff and
// Obsidian render worse. It is documented on Emit, so it is pinned here.
func TestCodec_EmitTerminatesTheBody(t *testing.T) {
	n := fullNote()
	n.Body = "no trailing newline"

	out := string(Emit(n))
	if !strings.HasSuffix(out, "no trailing newline\n") {
		t.Errorf("Emit did not terminate the body:\n%q", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Errorf("Emit added a newline to a body that already ended in one:\n%q", out)
	}

	n.Body = "already terminated\n"
	if out := string(Emit(n)); !strings.HasSuffix(out, "already terminated\n") || strings.HasSuffix(out, "\n\n") {
		t.Errorf("Emit changed a body that was already terminated:\n%q", out)
	}
}

// TestFrontmatter_TheEmptyNoteHasACanonicalShapeToo pins what Emit writes for UNSET fields. The
// fully-populated shape is pinned above; this is the shape Write actually produces for a note
// whose optional fields are empty, and it is the one a "an empty value is not a violation"
// branch has to keep accepting — without it, tightening that branch would make every note
// written without a type malformed, unservable and unmarkable, and no test would notice.
func TestFrontmatter_TheEmptyNoteHasACanonicalShapeToo(t *testing.T) {
	got := string(Emit(Note{}))
	want := "---\n" +
		"id: \"\"\n" +
		"agent: \"\"\n" +
		"formula: \"\"\n" +
		"run: \"\"\n" +
		"type: \"\"\n" +
		"created: \n" +
		"evidence: \n" +
		"status: \"\"\n" +
		"graduated_to: \"\"\n" +
		"expires: \n" +
		"---\n"
	if got != want {
		t.Errorf("canonical emit of the zero note:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}

	n := Parse([]byte(got))
	if n.Malformed {
		t.Error("the note Emit produces for unset fields parses back as malformed")
	}
	if n.Status != StatusActive {
		t.Errorf("Status: got %q, want %q — an unset status reads as active", n.Status, StatusActive)
	}
	if n.Type != "" {
		t.Errorf("Type: got %q, want empty", n.Type)
	}
}

func TestCodec_CRLFFrontmatterIsAccepted(t *testing.T) {
	raw := "---\r\nid: n1\r\nagent: manager\r\ntype: gotcha\r\ncreated: 2026-08-15T01:32:00Z\r\nstatus: active\r\n---\r\nbody\r\n"
	n := Parse([]byte(raw))
	if n.Malformed {
		t.Fatal("a CRLF note parsed as malformed")
	}
	if n.ID != "n1" || n.Agent != "manager" {
		t.Errorf("CRLF parse: got ID %q agent %q", n.ID, n.Agent)
	}
}

// TestCodec_EmitNeverOpensAPlainScalarWithAYAMLIndicator — the round-trip test above cannot see
// this class of bug, because it round-trips through OUR parser and ours is happy either way. The
// contract this pins is EXTERNAL: data.md requires these files to render as Obsidian Properties,
// which means the frontmatter has to be valid YAML to a real parser, not merely to us. A plain
// (unquoted) YAML scalar may not BEGIN with an indicator character — a parser rejects the line
// outright, and the whole note's Properties fail to render, not just the offending field.
//
// The check is structural rather than a call into a YAML library because ADR-013 permits exactly
// two direct requires and neither is one. The emitter's own allowlist is what keeps most of this
// set unreachable; '@' and '#' are the two it admits as content, so they are the two that have to
// be excluded in the LEADING position specifically.
func TestCodec_EmitNeverOpensAPlainScalarWithAYAMLIndicator(t *testing.T) {
	const indicators = "#&*!|>'\"%@`,[]{}"

	hostile := []string{"@nightly", "@", "#x", "`tick", "!bang", "&anchor", "*alias", "%directive", "ok"}
	for _, v := range hostile {
		n := fullNote()
		n.Formula, n.Run, n.GraduatedTo = v, v, v
		n.Evidence = []string{v}

		emitted := string(Emit(n))
		fm, _, found := strings.Cut(strings.TrimPrefix(emitted, fence+"\n"), "\n"+fence)
		if !found {
			t.Fatalf("Emit produced no frontmatter block for %q:\n%s", v, emitted)
		}
		for _, line := range strings.Split(fm, "\n") {
			_, value, isPair := strings.Cut(line, ":")
			value = strings.TrimSpace(value)
			if !isPair || value == "" || strings.HasPrefix(value, `"`) || strings.HasPrefix(value, "[") {
				continue // absent, or quoted/JSON, which is exactly the escape hatch
			}
			if strings.ContainsRune(indicators, rune(value[0])) {
				t.Errorf("for %q, Emit wrote a bare scalar opening with the YAML indicator %q:\n\t%s\nfull note:\n%s",
					v, value[0], line, emitted)
			}
		}
	}
}
