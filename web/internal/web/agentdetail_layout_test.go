package web

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Issue af-f4f082bc / #511: the per-agent "View" page rendered the read-only Session Snapshot small
// and left-pinned because #view-agent reused the asymmetric .cols left track (~360px) and the
// Settings-sized shared .readonly-block (240px, 12px muted). The design contract and tests pinned the
// snapshot's HONESTY/BEHAVIOR but never its size/width/prominence, so a cramped-but-correct layout
// shipped and passed CI invisibly.
//
// The web module is pure-Go with no JS/DOM/visual runner (see agentdetail_test.go:10-14), so — matching
// that source-scan precedent — these are SOURCE-LEVEL structural assertions over the embedded static
// assets that raise the snapshot's PRESENTATION from Advisory to Guard. Every assertion carries a
// self-negative proving the check is not vacuous.

// (1) A dedicated rule (#agent-snapshot or .snapshot-block) sizes the snapshot; width/height are
// judged in Go from the captured rule body. `[^{}]` keeps a match inside one rule (no brace crossing).
var reSnapshotSizingRule = regexp.MustCompile(`(?:#agent-snapshot|\.snapshot-block)\b[^{}]*\{([^{}]*)\}`)

// width:100% only — anchored so `max-width:100%` cannot satisfy it.
var reFullWidthDecl = regexp.MustCompile(`(?:^|;)\s*width\s*:\s*100%`)

// A real height (min-height/height) in vh or px — the leading boundary excludes `max-height`.
var reHeightDecl = regexp.MustCompile(`(?:^|[;{\s])(?:min-)?height\s*:\s*([0-9.]+)\s*(vh|px)`)

// (2) index.html wraps #agent-snapshot in a dedicated full-width snapshot-panel, not the .cols left
// track. Proximity keeps the match tied to the pane grab, not its -label/-note/-captured siblings.
var reSnapshotInPanel = regexp.MustCompile(`snapshot-panel[\s\S]{0,600}?id="agent-snapshot"`)

// (3) the #agent-snapshot <pre> open tag — proves it dropped the shared .readonly-block for .snapshot-block.
var reSnapshotPreTag = regexp.MustCompile(`<pre\b[^>]*id="agent-snapshot"[^>]*>`)

// (3b / note a) the shared .readonly-block rule survives untouched for Settings' #set-factory.
var reReadonlyBlockRule = regexp.MustCompile(`\.readonly-block\b[^{}]*\{[^{}]*\}`)

// (4) a scroll-to-bottom assignment (scrollTop <- scrollHeight). Tied to renderAgentSnapshot by
// extracting that function's body (funcBody) so a scroll elsewhere cannot satisfy it — RE2 forbids
// the >1000 bounded window a single regex would need.
var reScrollAssign = regexp.MustCompile(`scrollTop\s*=\s*[^;]*scrollHeight`)

// (facts) legible label/value pairs — a CSS rule (with a `{`) for the .facts/.fact/.fk/.fv family.
var reFactsFamilyRule = regexp.MustCompile(`\.(?:facts|fact|fk|fv)\b[^{}]*\{`)

// funcBody returns the brace-matched body (including the outer braces) of `function <name>` — used to
// tie an assertion to one function without a >1000 bounded regex window (RE2's cap).
func funcBody(src, name string) string {
	idx := strings.Index(src, "function "+name)
	if idx < 0 {
		return ""
	}
	rest := src[idx:]
	open := strings.IndexByte(rest, '{')
	if open < 0 {
		return ""
	}
	depth := 0
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[open : i+1]
			}
		}
	}
	return rest[open:]
}

// snapshotSizing reports whether a dedicated snapshot rule exists and whether the union of such rules
// declares full width and a height taller than the retired 240px cap (vh is inherently taller on any
// real viewport; a px height must exceed 240).
func snapshotSizing(css string) (found, fullWidth, tall bool) {
	rules := reSnapshotSizingRule.FindAllStringSubmatch(css, -1)
	if len(rules) == 0 {
		return false, false, false
	}
	found = true
	for _, m := range rules {
		body := m[1]
		if reFullWidthDecl.MatchString(body) {
			fullWidth = true
		}
		for _, h := range reHeightDecl.FindAllStringSubmatch(body, -1) {
			if h[2] == "vh" {
				tall = true
			} else if v, err := strconv.ParseFloat(h[1], 64); err == nil && v > 240 {
				tall = true
			}
		}
	}
	return found, fullWidth, tall
}

// Scenario: Snapshot has its own dedicated sizing rule that is full-width and taller than the old 240px cap.
func TestSnapshot_DedicatedFullWidthTallSizingRule(t *testing.T) {
	mainCSS := readAsset(t, filepath.Join(staticDir, "styles", "main.css"))
	found, fullWidth, tall := snapshotSizing(mainCSS)
	if !found {
		t.Error("main.css: a rule targeting #agent-snapshot (or a dedicated .snapshot-block) is required so the snapshot is sized as the page's primary element, not by the Settings-sized .readonly-block")
	}
	if !fullWidth {
		t.Error("main.css: the snapshot sizing rule must declare width:100% (full content width), not the narrow ~360px .cols left track")
	}
	if !tall {
		t.Error("main.css: the snapshot sizing rule must declare a height taller than the retired 240px cap (e.g. a vh height), so the newest output is not below a 240px fold")
	}
}

func TestSnapshot_DedicatedFullWidthTallSizingRule_SelfNegative(t *testing.T) {
	good := `.app .snapshot-block{ background:var(--panel-2); width:100%; min-height:60vh; font-size:13px; }`
	if f, w, tall := snapshotSizing(good); !f || !w || !tall {
		t.Errorf("fixture invalid: the FIXED rule must read found+fullWidth+tall (got %v/%v/%v)", f, w, tall)
	}
	noRule := `.app .readonly-block{ font-size:12px; color:var(--muted); max-height:240px; }`
	if f, _, _ := snapshotSizing(noRule); f {
		t.Error("snapshotSizing false-positive: a stylesheet with no snapshot rule must report not-found")
	}
	capped := `.app .snapshot-block{ width:100%; height:240px; }`
	if _, _, tall := snapshotSizing(capped); tall {
		t.Error("snapshotSizing false-positive: a snapshot still capped at 240px must NOT count as tall")
	}
	maxOnly := `.app .snapshot-block{ max-width:100%; max-height:600px; }`
	if _, w, tall := snapshotSizing(maxOnly); w || tall {
		t.Errorf("snapshotSizing false-positive: max-width/max-height must not satisfy full-width/tall (got w=%v tall=%v)", w, tall)
	}
}

// Scenario: Snapshot sits in a full-width container rather than the narrow asymmetric .cols left track.
func TestSnapshot_SitsInFullWidthContainerNotNarrowColsTrack(t *testing.T) {
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	if !reSnapshotInPanel.MatchString(indexHTML) {
		t.Error("index.html: #agent-snapshot must live in a dedicated full-width snapshot-panel container, not the asymmetric .cols left track")
	}
	if c := strings.Count(indexHTML, `id="view-agent"`); c != 1 {
		t.Errorf(`index.html: exactly one id="view-agent" section expected after the re-layout, got %d`, c)
	}
	// AC / contract: no input element in the snapshot's zone — never a raw terminal.
	if m := reSnapshotInPanel.FindString(indexHTML); m != "" {
		zone := snapshotZone(indexHTML)
		for _, bad := range []string{"<input", "<textarea", "<form"} {
			if strings.Contains(zone, bad) {
				t.Errorf("index.html: the snapshot zone must contain no %q — it is read-only, never a raw terminal", bad)
			}
		}
	}
}

// snapshotZone returns the markup of the snapshot-panel container (from its class to the pane's own
// closing </pre> plus a small tail), used to prove no input element shares the read-only zone.
func snapshotZone(html string) string {
	i := strings.Index(html, "snapshot-panel")
	if i < 0 {
		return ""
	}
	j := strings.Index(html[i:], "</pre>")
	if j < 0 {
		return html[i:]
	}
	return html[i : i+j+len("</pre>")]
}

func TestSnapshot_SitsInFullWidthContainer_SelfNegative(t *testing.T) {
	fixed := `<section class="panel snapshot-panel" aria-label="Agent session"><pre class="snapshot-block mono" id="agent-snapshot"></pre></section>`
	if !reSnapshotInPanel.MatchString(fixed) {
		t.Error("reSnapshotInPanel failed to match the FIXED full-width snapshot-panel layout")
	}
	old := `<div class="cols"><section class="panel"><pre class="readonly-block mono" id="agent-snapshot"></pre></section></div>`
	if reSnapshotInPanel.MatchString(old) {
		t.Error("reSnapshotInPanel false-positive on the OLD narrow .cols left-track layout")
	}
}

// Scenario: Snapshot no longer inherits the shared Settings-sized .readonly-block; Settings stays intact.
func TestSnapshot_DoesNotInheritReadonlyBlock(t *testing.T) {
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	mainCSS := readAsset(t, filepath.Join(staticDir, "styles", "main.css"))

	tag := reSnapshotPreTag.FindString(indexHTML)
	if tag == "" {
		t.Fatal("index.html: the #agent-snapshot <pre> element is missing")
	}
	if strings.Contains(tag, "readonly-block") {
		t.Errorf("index.html: #agent-snapshot must NOT carry the shared .readonly-block class (the 240px/12px Settings sizing leaks in): %s", tag)
	}
	if !strings.Contains(tag, "snapshot-block") {
		t.Errorf("index.html: #agent-snapshot must carry its own dedicated .snapshot-block class: %s", tag)
	}
	// note (a): the shared class survives untouched so Settings' #set-factory stays presentable.
	if !reReadonlyBlockRule.MatchString(mainCSS) {
		t.Error("main.css: the shared .readonly-block rule must remain (still used by Settings' #set-factory) — do not delete it, only stop the snapshot from sharing it")
	}
	if !strings.Contains(mainCSS, "240px") {
		t.Error("main.css: the .readonly-block 240px cap must remain untouched for Settings (the fix scopes AROUND the shared class, it does not resize it)")
	}
	setFactoryTag := regexp.MustCompile(`<[^>]*id="set-factory"[^>]*>`).FindString(indexHTML)
	if setFactoryTag == "" || !strings.Contains(setFactoryTag, "readonly-block") {
		t.Errorf("index.html: Settings' #set-factory must still use .readonly-block (unchanged): %q", setFactoryTag)
	}
}

func TestSnapshot_DoesNotInheritReadonlyBlock_SelfNegative(t *testing.T) {
	oldTag := `<pre class="readonly-block mono" id="agent-snapshot" aria-label="Session snapshot (read-only)"></pre>`
	if tag := reSnapshotPreTag.FindString(oldTag); !strings.Contains(tag, "readonly-block") {
		t.Error("fixture invalid: the OLD pre tag must contain readonly-block (the leak the fix removes)")
	}
	newTag := `<pre class="snapshot-block mono" id="agent-snapshot" aria-label="Session snapshot (read-only)"></pre>`
	tag := reSnapshotPreTag.FindString(newTag)
	if strings.Contains(tag, "readonly-block") {
		t.Error("fixture invalid: the FIXED pre tag must not contain readonly-block")
	}
	if !strings.Contains(tag, "snapshot-block") {
		t.Error("fixture invalid: the FIXED pre tag must contain the dedicated snapshot-block class")
	}
}

// Scenario: Facts render as legible label/value pairs.
func TestFacts_RenderAsLegiblePairs(t *testing.T) {
	mainCSS := readAsset(t, filepath.Join(staticDir, "styles", "main.css"))
	if !reFactsFamilyRule.MatchString(mainCSS) {
		t.Error("main.css: a rule for the .facts/.fact/.fk/.fv family is required so renderAgentFacts' label/value pairs render legibly (not run-together like 'statusIdle')")
	}
}

func TestFacts_RenderAsLegiblePairs_SelfNegative(t *testing.T) {
	with := `.app .fact{ display:flex; gap:12px; } .app .fk{ color:var(--muted); } .app .fv{ font-weight:600; }`
	if !reFactsFamilyRule.MatchString(with) {
		t.Error("reFactsFamilyRule failed to match a valid .fact/.fk/.fv rule block")
	}
	without := `.app .factory-note{ color:red; } .app .something{ font-size:12px; }`
	if reFactsFamilyRule.MatchString(without) {
		t.Error("reFactsFamilyRule false-positive on CSS with no .facts-family rule (.factory-note must not count)")
	}
}

// Scenario: The snapshot scrolls the newest output into view on refresh.
func TestSnapshot_ScrollsNewestOutputIntoView(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	body := funcBody(appJS, "renderAgentSnapshot")
	if body == "" {
		t.Fatal("app.js: renderAgentSnapshot function not found")
	}
	if !reScrollAssign.MatchString(body) {
		t.Error("app.js: renderAgentSnapshot must scroll the newest output into view (set scrollTop from scrollHeight) so the latest bytes are visible on open and after each 5s tick")
	}
	// The scroll edit must not violate the textContent-proximity tripwire (agentdetail_test.go:26).
	if !rePaneTextContent.MatchString(appJS) {
		t.Error("app.js: the scroll edit must preserve the byId('agent-snapshot')->textContent proximity (AC-3, still filled via textContent)")
	}
}

func TestSnapshot_ScrollsNewestOutputIntoView_SelfNegative(t *testing.T) {
	fixed := `function renderAgentSnapshot(d){ var pre = byId('agent-snapshot'); if(!pre) return; var atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 4; pre.textContent = d.tail.output || ''; if (atBottom) pre.scrollTop = pre.scrollHeight; }`
	if body := funcBody(fixed, "renderAgentSnapshot"); !reScrollAssign.MatchString(body) {
		t.Error("reScrollAssign failed to match the FIXED renderAgentSnapshot body")
	}
	old := `function renderAgentSnapshot(d){ var pre = byId('agent-snapshot'); if(!pre) return; pre.textContent = d.tail.output || ''; }`
	if body := funcBody(old, "renderAgentSnapshot"); reScrollAssign.MatchString(body) {
		t.Error("reScrollAssign false-positive on the OLD renderAgentSnapshot (no scroll-to-bottom)")
	}
	// funcBody must isolate the function — a scroll in a LATER function must not leak in.
	twoFns := `function renderAgentSnapshot(d){ var pre = byId('agent-snapshot'); pre.textContent = ''; }` + "\n" +
		`function other(){ el.scrollTop = el.scrollHeight; }`
	if body := funcBody(twoFns, "renderAgentSnapshot"); reScrollAssign.MatchString(body) {
		t.Error("funcBody leaked past renderAgentSnapshot into a later function's scroll code")
	}
}
