package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Issue af-2bcfd6ec / #629: the Prototypes view laid its three panes out in a single grid whose
// template was `minmax(0,300px) minmax(0,1fr) minmax(0,300px)`. Two fixed rails plus their gaps
// spent 644px of the 1224px content cap unconditionally, so the sandboxed preview — the primary
// work surface of the view — was mathematically incapable of reaching the majority: measured 43.3%
// at 1196px and 47.2% at 1920px, against a derived ceiling of (1224-644)/1224 = 47.4%. The feedback
// rail held its column even while displaying "Feedback not currently open for this prototype".
//
// THIS IS OCCURRENCE TWO. Occurrence one was af-f4f082bc / #511 (see agentdetail_layout_test.go),
// where the Session Snapshot shipped cramped for the same reason and its post-mortem recorded:
// "the design contract and tests pinned the snapshot's HONESTY/BEHAVIOR but never its
// size/width/prominence, so a cramped-but-correct layout shipped and passed CI invisibly."
// That remedy was written as assertions scoped to #agent-snapshot, so it caught #511 and nothing
// else; the Prototypes view inherited the identical gap untouched. Peer review of the #629 analysis
// refuted the claim that generalizing was impossible: the POSITIVE rule (primary surface -> widest
// track) does need a naming convention the repo lacks, but the NEGATIVE rule does not.
// TestProtoLayout_NoStylesheetGridSpendsMoreThanOneFixedRail is that negative rule, and it is
// deliberately module-wide rather than scoped to .proto-cols — it is what would have caught
// occurrence two before it shipped, and what should catch occurrence three.
//
// The web module is pure-Go with no JS/DOM/visual runner (see agentdetail_test.go:10-14), so these
// are SOURCE-LEVEL structural assertions over the embedded static assets, following that same
// precedent. Every assertion carries a self-negative proving the check is not vacuous.

// A whole `grid-template-columns:<value>` declaration. `[^;}]` stops at the next declaration or the
// end of the rule so one match is exactly one template.
var reGridTemplateDecl = regexp.MustCompile(`grid-template-columns\s*:\s*([^;}]*)`)

// A fixed rail written in the repo's idiom: minmax(0,<N>px). The argument order matters — it is what
// separates a fixed rail from the responsive card grids' minmax(<N>px,1fr), which are flexible.
var reFixedMinmaxRail = regexp.MustCompile(`minmax\(\s*0\s*,\s*[0-9.]+\s*px\s*\)`)

// A bare `<N>px` track. Counted only after function calls are stripped, so a minmax()/repeat()
// minimum is never mistaken for a rail — this closes the evasion where `300px 1fr 300px` would
// otherwise sidestep the minmax(0,...) form.
var reBarePxTrack = regexp.MustCompile(`(?:^|[\s,])[0-9.]+px\b`)

// One innermost function call, e.g. minmax(150px,1fr). Applied repeatedly to strip nesting such as
// repeat(auto-fill,minmax(260px,1fr)).
var reGridFunc = regexp.MustCompile(`[a-zA-Z-]+\([^()]*\)`)

// A flexible track (1fr, 2fr, .5fr). At least one is required so a template cannot be all-fixed.
var reFlexTrack = regexp.MustCompile(`[0-9.]*fr\b`)

// Pane placement declarations. `grid-row:1 / span 2` captures the start line, which is the one the
// row-ordering assertion compares.
var reGridColumnDecl = regexp.MustCompile(`grid-column\s*:\s*([0-9]+)`)
var reGridRowDecl = regexp.MustCompile(`grid-row\s*:\s*([0-9]+)`)

// The #proto-frame iframe open tag — guards the sandbox posture against a careless pane edit.
var reProtoFrameTag = regexp.MustCompile(`<iframe\b[^>]*id="proto-frame"[^>]*>`)

// The viewer pane's <section> open tag. It carried NO class attribute before this fix, so it is the
// one line where a copy-paste would add an unintended .panel background.
var reProtoViewerSection = regexp.MustCompile(`<section\b[^>]*proto-pane-viewer[^>]*>`)

const (
	protoPaneList     = "proto-pane-list"
	protoPaneViewer   = "proto-pane-viewer"
	protoPaneFeedback = "proto-pane-feedback"
)

// stripGridFuncs removes every function call from a track list, innermost first, so that only
// top-level track tokens remain.
func stripGridFuncs(value string) string {
	for {
		stripped := reGridFunc.ReplaceAllString(value, " ")
		if stripped == value {
			return value
		}
		value = stripped
	}
}

// fixedRailCount reports how many fixed-width rails a grid template reserves.
func fixedRailCount(value string) int {
	return len(reFixedMinmaxRail.FindAllString(value, -1)) +
		len(reBarePxTrack.FindAllString(stripGridFuncs(value), -1))
}

type gridTemplate struct {
	line  int
	value string
	rails int
	flex  bool
}

func gridTemplates(css string) []gridTemplate {
	var out []gridTemplate
	for _, loc := range reGridTemplateDecl.FindAllStringSubmatchIndex(css, -1) {
		value := strings.TrimSpace(css[loc[2]:loc[3]])
		out = append(out, gridTemplate{
			line:  strings.Count(css[:loc[0]], "\n") + 1,
			value: value,
			rails: fixedRailCount(value),
			flex:  reFlexTrack.MatchString(stripGridFuncs(value)) || reFlexTrack.MatchString(value),
		})
	}
	return out
}

// styleSheets returns every stylesheet shipped in static/styles, so the budget rule covers a view
// added in a new file rather than only the ones that exist today.
func styleSheets(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join(staticDir, "styles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	sheets := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".css") {
			sheets[e.Name()] = readAsset(t, filepath.Join(dir, e.Name()))
		}
	}
	if len(sheets) == 0 {
		t.Fatalf("no stylesheets found under %s — the budget rule would be vacuous", dir)
	}
	return sheets
}

// paneRuleBody returns the declaration body of the rule targeting the given pane class. Multi-
// selector rules are matched too: `[^{}]` spans the commas and newlines between selectors without
// ever crossing into a neighbouring rule.
func paneRuleBody(css, pane string) string {
	re := regexp.MustCompile(`\.` + regexp.QuoteMeta(pane) + `\b[^{}]*\{([^{}]*)\}`)
	m := re.FindStringSubmatch(css)
	if m == nil {
		return ""
	}
	return m[1]
}

// mediaBlockBody returns the brace-matched body of the @media block whose condition contains cond.
// Brace matching (rather than a bounded regex) is required because the block nests rules — RE2 has
// no recursion and the window would exceed its bound.
func mediaBlockBody(css, cond string) string {
	for _, at := range regexp.MustCompile(`@media[^{]*\{`).FindAllStringIndex(css, -1) {
		header := css[at[0]:at[1]]
		if !strings.Contains(strings.ReplaceAll(header, " ", ""), strings.ReplaceAll(cond, " ", "")) {
			continue
		}
		depth := 0
		for i := at[1] - 1; i < len(css); i++ {
			switch css[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return css[at[1]:i]
				}
			}
		}
		return css[at[1]:]
	}
	return ""
}

// baseCSS is the stylesheet with every @media block removed, so an assertion about the desktop
// cascade cannot be satisfied by a rule that only applies at a breakpoint. Every block is stripped
// rather than a known list of breakpoints, so adding a new one cannot silently weaken the checks.
func baseCSS(css string) string {
	reAtMedia := regexp.MustCompile(`@media[^{]*\{`)
	for {
		at := reAtMedia.FindStringIndex(css)
		if at == nil {
			return css
		}
		depth, end := 0, len(css)
		for i := at[1] - 1; i < len(css); i++ {
			switch css[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = i + 1
				}
			}
			if depth == 0 {
				break
			}
		}
		css = css[:at[0]] + " " + css[end:]
	}
}

// Scenario: No stylesheet grid spends more than one fixed rail.
//
// This is the class-level rule that #511 stopped short of. It is a budget, not a naming convention:
// a template may reserve at most one fixed rail and must keep at least one flexible track, which
// leaves the primary surface structurally able to hold the majority no matter which view it is in.
func TestProtoLayout_NoStylesheetGridSpendsMoreThanOneFixedRail(t *testing.T) {
	for name, css := range styleSheets(t) {
		for _, g := range gridTemplates(css) {
			if g.rails > 1 {
				t.Errorf("styles/%s:%d: grid-template-columns reserves %d fixed rails (%q) — a view may spend at most ONE, so its primary surface can still hold the majority of the content width. Move the secondary pane into an existing column (see .proto-cols) instead of buying it a rail.",
					name, g.line, g.rails, g.value)
			}
			if !g.flex {
				t.Errorf("styles/%s:%d: grid-template-columns declares no flexible (fr) track (%q) — an all-fixed template cannot give any pane the remaining width.",
					name, g.line, g.value)
			}
		}
	}
}

func TestProtoLayout_NoStylesheetGridSpendsMoreThanOneFixedRail_SelfNegative(t *testing.T) {
	// The pre-fix .proto-cols template — the violation this rule exists to catch.
	if n := fixedRailCount("minmax(0,300px) minmax(0,1fr) minmax(0,300px)"); n != 2 {
		t.Errorf("fixedRailCount: the PRE-FIX 3-track template must count 2 rails, got %d", n)
	}
	// The post-fix template, and the one-rail .cols template that must keep passing.
	if n := fixedRailCount("minmax(0,300px) minmax(0,1fr)"); n != 1 {
		t.Errorf("fixedRailCount: the FIXED 2-track template must count 1 rail, got %d", n)
	}
	if n := fixedRailCount("minmax(0,360px) minmax(0,1fr)"); n != 1 {
		t.Errorf("fixedRailCount: the .cols template must count 1 rail, got %d", n)
	}
	// Responsive card grids are flexible: minmax(<N>px,1fr) is a minimum, not a rail. Argument
	// order is the whole distinction, so a false positive here would make the rule unusable.
	for _, ok := range []string{
		"repeat(auto-fill,minmax(260px,1fr))",
		"repeat(auto-fit,minmax(150px,1fr))",
		"minmax(0,1fr) minmax(0,1fr)",
		"1fr auto 1fr 1fr auto",
		"1fr",
	} {
		if n := fixedRailCount(ok); n != 0 {
			t.Errorf("fixedRailCount false-positive: %q is rail-free but counted %d", ok, n)
		}
	}
	// A bare-px template must not evade the budget by skipping the minmax(0,...) idiom.
	if n := fixedRailCount("300px minmax(0,1fr) 300px"); n != 2 {
		t.Errorf("fixedRailCount: bare-px rails must be counted (evasion hole), got %d for `300px minmax(0,1fr) 300px`", n)
	}
	// The flex-track clause must actually reject an all-fixed template.
	allFixed := gridTemplates(`.x{ grid-template-columns:minmax(0,300px); }`)
	if len(allFixed) != 1 || allFixed[0].flex {
		t.Errorf("gridTemplates false-positive: an all-fixed template must report flex=false, got %+v", allFixed)
	}
	if flexed := gridTemplates(`.x{ grid-template-columns:minmax(0,300px) minmax(0,1fr); }`); len(flexed) != 1 || !flexed[0].flex {
		t.Errorf("gridTemplates: a template with an fr track must report flex=true, got %+v", flexed)
	}
	// Line reporting must be real, or a failure message cannot be acted on.
	if got := gridTemplates("a\nb\n.x{ grid-template-columns:1fr; }"); len(got) != 1 || got[0].line != 3 {
		t.Errorf("gridTemplates: expected the declaration reported at line 3, got %+v", got)
	}
}

// Scenario: The feedback panel sits under "Built by agents" in the left column.
func TestProtoLayout_FeedbackPanelSitsUnderBuiltByAgentsInLeftColumn(t *testing.T) {
	css := baseCSS(readAsset(t, filepath.Join(staticDir, "styles", "main.css")))

	listCol, listRow, ok := panePlacement(css, protoPaneList)
	if !ok {
		t.Fatalf("main.css: no rule places .%s — the list pane needs an explicit grid-column/grid-row so the feedback pane can be stacked beneath it", protoPaneList)
	}
	fbCol, fbRow, ok := panePlacement(css, protoPaneFeedback)
	if !ok {
		t.Fatalf("main.css: no rule places .%s — issue #629 requires the feedback pane in the left column beneath the prototype list, not in a third rail", protoPaneFeedback)
	}
	if fbCol != listCol {
		t.Errorf("main.css: the feedback pane must share the list pane's column (got feedback in column %d, list in column %d) — a column of its own is the 300px rail that starved the preview", fbCol, listCol)
	}
	if fbRow <= listRow {
		t.Errorf("main.css: the feedback pane must sit in a row BELOW the list pane (got feedback row %d, list row %d) so it renders under \"Built by agents\"", fbRow, listRow)
	}
}

// Scenario: The sandboxed preview takes the majority of the content width.
func TestProtoLayout_SandboxedPreviewTakesMajorityOfContentWidth(t *testing.T) {
	css := baseCSS(readAsset(t, filepath.Join(staticDir, "styles", "main.css")))

	viewerCol, _, ok := panePlacement(css, protoPaneViewer)
	if !ok {
		t.Fatalf("main.css: no rule places .%s — the viewer needs the flexible column explicitly, or it falls back to auto-placement", protoPaneViewer)
	}
	if viewerCol != 2 {
		t.Errorf("main.css: the viewer pane must occupy column 2, the minmax(0,1fr) track (got column %d)", viewerCol)
	}
	body := paneRuleBody(css, protoPaneList)
	listCol, _, _ := panePlacement(css, protoPaneList)
	if viewerCol == listCol {
		t.Errorf("main.css: the viewer must not share the list pane's fixed rail (both in column %d; list rule %q)", viewerCol, body)
	}
	// The template itself must have shed the third track, or the placements above are moot.
	proto := paneRuleBody(css, "proto-cols")
	tpl := reGridTemplateDecl.FindStringSubmatch(proto)
	if tpl == nil {
		t.Fatal("main.css: .proto-cols declares no grid-template-columns")
	}
	if n := fixedRailCount(tpl[1]); n != 1 {
		t.Errorf("main.css: .proto-cols must reserve exactly one fixed rail, got %d (%q) — two rails put the preview's ceiling at 47.4%% of the content width", n, strings.TrimSpace(tpl[1]))
	}
}

// panePlacement reads the column and row a pane is explicitly placed at.
func panePlacement(css, pane string) (col, row int, ok bool) {
	body := paneRuleBody(css, pane)
	if body == "" {
		return 0, 0, false
	}
	c := reGridColumnDecl.FindStringSubmatch(body)
	r := reGridRowDecl.FindStringSubmatch(body)
	if c == nil || r == nil {
		return 0, 0, false
	}
	col, _ = strconv.Atoi(c[1])
	row, _ = strconv.Atoi(r[1])
	return col, row, true
}

func TestProtoLayout_PanePlacement_SelfNegative(t *testing.T) {
	fixed := `.app .proto-cols{ display:grid; grid-template-columns:minmax(0,300px) minmax(0,1fr); }
.app .proto-cols > .proto-pane-list{ grid-column:1; grid-row:1; }
.app .proto-cols > .proto-pane-viewer{ grid-column:2; grid-row:1 / span 2; }
.app .proto-cols > .proto-pane-feedback{ grid-column:1; grid-row:2; }`

	if c, r, ok := panePlacement(fixed, protoPaneFeedback); !ok || c != 1 || r != 2 {
		t.Errorf("fixture invalid: the FIXED feedback placement must read column 1 row 2 (got %d/%d ok=%v)", c, r, ok)
	}
	if c, r, ok := panePlacement(fixed, protoPaneViewer); !ok || c != 2 || r != 1 {
		t.Errorf("fixture invalid: the FIXED viewer placement must read column 2 row 1 (got %d/%d ok=%v)", c, r, ok)
	}
	// The pre-fix stylesheet has no placement rules at all — the check must report not-found rather
	// than silently passing.
	old := `.app .proto-cols{ display:grid; gap:var(--space-7); grid-template-columns:minmax(0,300px) minmax(0,1fr) minmax(0,300px); align-items:start; }`
	if _, _, ok := panePlacement(old, protoPaneFeedback); ok {
		t.Error("panePlacement false-positive: the PRE-FIX stylesheet places no panes and must report not-found")
	}
	// A feedback pane parked in a third rail must be read as such, so the column comparison fails.
	thirdRail := `.app .proto-cols > .proto-pane-feedback{ grid-column:3; grid-row:1; }`
	if c, r, ok := panePlacement(thirdRail, protoPaneFeedback); !ok || c != 3 || r != 1 {
		t.Errorf("panePlacement must read a third-rail feedback pane as column 3 row 1 (got %d/%d ok=%v)", c, r, ok)
	}
	// paneRuleBody must not cross a brace boundary into a neighbouring rule.
	if body := paneRuleBody(fixed, protoPaneList); strings.Contains(body, "grid-column:2") {
		t.Errorf("paneRuleBody leaked past the list rule into the viewer rule: %q", body)
	}
	// baseCSS must remove the breakpoint, or a media-only rule could satisfy a desktop assertion.
	withMedia := fixed + "\n@media (max-width:860px){ .app .proto-cols > .proto-pane-feedback{ grid-column:9; grid-row:9; } }"
	if strings.Contains(baseCSS(withMedia), "grid-column:9") {
		t.Error("baseCSS false-positive: the @media block must be stripped so a breakpoint rule cannot satisfy a desktop placement assertion")
	}
	// ANY breakpoint, not a known list — a newly added one must not weaken the desktop checks.
	twoBlocks := fixed +
		"\n@media (max-width:860px){ .app .x{ grid-column:9; } }" +
		"\n@media (min-width:1600px){ .app .proto-cols > .proto-pane-feedback{ grid-column:7; grid-row:7; } }"
	stripped := baseCSS(twoBlocks)
	if strings.Contains(stripped, "grid-column:9") || strings.Contains(stripped, "grid-column:7") {
		t.Errorf("baseCSS must strip EVERY @media block, not a hard-coded set: %q", stripped)
	}
	// ...while leaving the desktop rules it is supposed to preserve intact.
	if !strings.Contains(stripped, "grid-column:2") {
		t.Errorf("baseCSS over-stripped: the non-media viewer placement must survive: %q", stripped)
	}
}

// Scenario: The narrow-screen collapse survives explicit pane placement.
//
// Measured during analysis and confirmed in review: WITHOUT this reset the grid does not collapse at
// all — the explicit placements resurrect a second implicit column and the viewer is squeezed to
// ~340px, which is WORSE than the defect being fixed. `grid-template-columns:1fr` alone looks like it
// still works, which is exactly what makes this trap expensive.
func TestProtoLayout_NarrowScreenCollapseSurvivesExplicitPanePlacement(t *testing.T) {
	css := readAsset(t, filepath.Join(staticDir, "styles", "main.css"))
	block := mediaBlockBody(css, "max-width:860px")
	if block == "" {
		t.Fatal("main.css: the @media (max-width:860px) block is missing — the Prototypes grid has no narrow-screen collapse")
	}
	if !strings.Contains(block, "grid-template-columns:1fr") {
		t.Error("main.css: the @media (max-width:860px) block must still collapse .proto-cols to a single column")
	}
	for _, pane := range []string{protoPaneList, protoPaneViewer, protoPaneFeedback} {
		body := paneRuleBody(block, pane)
		if body == "" {
			t.Errorf("main.css @media (max-width:860px): .%s has no placement reset — without it the explicit desktop grid-column/grid-row survive the breakpoint, the grid keeps a second implicit column and the viewer is squeezed to ~340px", pane)
			continue
		}
		if !strings.Contains(strings.ReplaceAll(body, " ", ""), "grid-column:auto") {
			t.Errorf("main.css @media (max-width:860px): .%s must reset grid-column to auto (got %q)", pane, body)
		}
		if !strings.Contains(strings.ReplaceAll(body, " ", ""), "grid-row:auto") {
			t.Errorf("main.css @media (max-width:860px): .%s must reset grid-row to auto (got %q)", pane, body)
		}
	}
}

func TestProtoLayout_NarrowScreenCollapse_SelfNegative(t *testing.T) {
	withReset := `@media (max-width:860px){
  .app .cols, .app .proto-cols{ grid-template-columns:1fr; }
  .app .proto-cols > .proto-pane-list,
  .app .proto-cols > .proto-pane-viewer,
  .app .proto-cols > .proto-pane-feedback{ grid-column:auto; grid-row:auto; }
}`
	block := mediaBlockBody(withReset, "max-width:860px")
	if block == "" {
		t.Fatal("mediaBlockBody failed to extract the FIXED @media block")
	}
	for _, pane := range []string{protoPaneList, protoPaneViewer, protoPaneFeedback} {
		if body := paneRuleBody(block, pane); !strings.Contains(body, "grid-column:auto") {
			t.Errorf("fixture invalid: the FIXED @media block must reset .%s (got %q)", pane, body)
		}
	}
	// The measured regression: collapse rule present, resets absent. This MUST be detected.
	withoutReset := `@media (max-width:860px){
  .app .cols, .app .proto-cols{ grid-template-columns:1fr; }
}`
	if body := paneRuleBody(mediaBlockBody(withoutReset, "max-width:860px"), protoPaneFeedback); body != "" {
		t.Errorf("false-positive: an @media block with NO placement reset must report the pane missing, got %q", body)
	}
	// Brace matching must stop at the block's own closing brace.
	trailing := withReset + "\n.app .after{ grid-column:99; }"
	if strings.Contains(mediaBlockBody(trailing, "max-width:860px"), "grid-column:99") {
		t.Error("mediaBlockBody leaked past the @media block's closing brace into the following rule")
	}
	// A different breakpoint must not answer for this one.
	if mediaBlockBody(withReset, "max-width:640px") != "" {
		t.Error("mediaBlockBody false-positive: matched a breakpoint condition that is not present")
	}
}

// Scenario: The layout still collapses to a single column on narrow screens (reading order half).
//
// The fix moves the feedback pane with CSS placement, NOT by moving the element. Source order is
// what mobile and screen readers follow, so a future "just move the div" edit must fail here.
func TestProtoLayout_PanesKeepSourceOrderListViewerFeedback(t *testing.T) {
	html := readAsset(t, filepath.Join(staticDir, "index.html"))
	list := strings.Index(html, protoPaneList)
	viewer := strings.Index(html, protoPaneViewer)
	feedback := strings.Index(html, protoPaneFeedback)
	if list < 0 || viewer < 0 || feedback < 0 {
		t.Fatalf("index.html: the three pane classes must all be present (list=%d viewer=%d feedback=%d)", list, viewer, feedback)
	}
	if !(list < viewer && viewer < feedback) {
		t.Errorf("index.html: source order must stay list -> viewer -> feedback (got offsets %d/%d/%d) — the feedback pane is relocated by CSS placement, never by moving the element, so that narrow screens and screen readers still reach the preview before the feedback form", list, viewer, feedback)
	}
}

// Scenario: Moving the panel does not weaken the preview sandbox.
func TestProtoLayout_MovingThePanelDoesNotWeakenThePreviewSandbox(t *testing.T) {
	html := readAsset(t, filepath.Join(staticDir, "index.html"))
	tag := reProtoFrameTag.FindString(html)
	if tag == "" {
		t.Fatal("index.html: the #proto-frame iframe is missing")
	}
	if !strings.Contains(tag, "sandbox") {
		t.Errorf("index.html: #proto-frame must keep its sandbox attribute: %s", tag)
	}
	if strings.Contains(tag, "allow-") {
		t.Errorf("index.html: #proto-frame must keep a BARE sandbox (no allow-scripts / allow-same-origin) so prototype JS cannot reach the app shell: %s", tag)
	}
}

// Scenario: The three pane classes tie index.html and main.css together, so renaming one side fails
// loudly instead of silently dropping the placement.
func TestProtoLayout_PaneClassesTieHTMLAndCSSTogether(t *testing.T) {
	html := readAsset(t, filepath.Join(staticDir, "index.html"))
	css := readAsset(t, filepath.Join(staticDir, "styles", "main.css"))
	for _, pane := range []string{protoPaneList, protoPaneViewer, protoPaneFeedback} {
		if !strings.Contains(html, pane) {
			t.Errorf("index.html: the .%s class is missing — main.css places panes by this class", pane)
		}
		if !strings.Contains(css, pane) {
			t.Errorf("main.css: the .%s class is never referenced — index.html carries it, so the placement was dropped or renamed", pane)
		}
	}
	// The viewer <section> carried no class attribute before this fix; it must gain the pane class
	// WITHOUT gaining .panel, which would paint an unintended panel background behind the iframe.
	tag := reProtoViewerSection.FindString(html)
	if tag == "" {
		t.Fatalf("index.html: no <section> carries the .%s class", protoPaneViewer)
	}
	if regexp.MustCompile(`\bpanel\b`).MatchString(tag) {
		t.Errorf("index.html: the viewer <section> must NOT gain the .panel class — it had no class attribute before and .panel would add a background behind the sandboxed preview: %s", tag)
	}
}

func TestProtoLayout_PaneClassesTieHTMLAndCSSTogether_SelfNegative(t *testing.T) {
	good := `<section class="proto-pane-viewer" aria-label="Prototype viewer">`
	if tag := reProtoViewerSection.FindString(good); tag == "" {
		t.Error("reProtoViewerSection failed to match a correctly classed viewer section")
	}
	bad := `<section class="panel proto-pane-viewer" aria-label="Prototype viewer">`
	if tag := reProtoViewerSection.FindString(bad); !regexp.MustCompile(`\bpanel\b`).MatchString(tag) {
		t.Error("the .panel guard would not detect a viewer section that wrongly carries .panel")
	}
	// The list and feedback panes legitimately keep .panel, so the guard must be viewer-scoped.
	listSection := `<section class="panel proto-pane-list" aria-label="Prototype directories">`
	if reProtoViewerSection.MatchString(listSection) {
		t.Error("reProtoViewerSection false-positive: it matched the list pane, whose .panel class is correct")
	}
}
