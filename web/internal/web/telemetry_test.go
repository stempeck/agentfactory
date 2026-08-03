package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #580 Phase 4: the Telemetry panel. The web module is pure-Go with no JS/DOM runtime
// (web/go.mod has no require block), so — following the source-scan precedent in nav_test.go and
// agentdetail_test.go — these are SOURCE-LEVEL structural assertions over the embedded static
// assets. `readAsset` and `staticDir` are package-level helpers declared in nav_test.go; `funcBody`
// comes from agentdetail_layout_test.go and `vmObjectBody` from contracttrace_test.go. None of them
// is redeclared here.
//
// WHAT THESE TESTS PROVE. Inside the named renderer, on comment-stripped code:
//   - each state's discriminating DTO fields are read (a renderer emitting one line cannot read
//     every leaf's discriminator);
//   - each state's copy and its next step are arguments to the SAME telLine() call, so a literal in
//     a comment, an unused constant, or another state's line cannot satisfy it;
//   - the three axes are branched on in the fixed order installed -> recording -> backend, measured
//     from the PREDICATES rather than from prose;
//   - no state's copy is contained in another's (containment, not inequality — if A's copy is a
//     substring of B's, one render of B satisfies both presence checks);
//   - root-owned sentences are relayed, never re-typed, and the transport-error branch keys on the
//     value probeOne actually leaves;
//   - every surface carries loading / failed / empty copy, and the shell paints before any fetch.
//
// WHAT THEY DO NOT PROVE, stated plainly because the gap is real. These are source assertions, and
// source assertions cannot establish runtime reachability. A guard inserted with a non-constant
// truthy condition — `if (vm) { return; }` at the top of the banner — short-circuits every leaf and
// this suite stays green; verified by mutation, not assumed. The constant-true form IS caught, and
// so are: a blank banner via a deleted fallback, a dropped synchronous paint, a swapped axis order,
// an inverted join verdict, a dead recovery branch, and a re-introduced count fabrication (eight
// mutations run, seven caught). What remains uncovered is execution itself: no branch is taken here,
// no node is appended, no CSS is applied, and nothing observes real ordering. Closing that needs a
// DOM runtime, which web/go.mod deliberately does not have — it is Phase 5b's deliverable, and
// until it lands this file is a strong structural net with a known runtime-shaped hole in it.

// ---------------------------------------------------------------------------
// Literals — every one VERBATIM from the CLI, at its live anchor. ux.md:113-114 makes reuse
// normative: "do not invent new phrasings for states the CLI already words".
// ---------------------------------------------------------------------------

const (
	// internal/cmd/telemetry.go:132 — the not-installed copy.
	copyNotInstalled = "no telemetry.json found"
	nextNotInstalled = "run quickstart.sh with telemetry or create .agentfactory/telemetry.json to export"

	// internal/cmd/telemetry.go:284 — healthy-empty.
	copyHealthyEmpty = "No step records yet. Enable telemetry with 'af telemetry on' and run a formula."

	// internal/cmd/telemetry.go:279-280 — the loss note.
	copyLossSkipped = "unreadable lines skipped"
	copyLossDropped = "never reached a backend"

	// internal/telemetry/probe.go:41-44 — per-signal probe verdicts.
	copyProbe404       = "reachable but the address is not served (HTTP 404) — data sent here is discarded"
	copyProbeCredental = "reachable but the credential was rejected"

	// design-doc.md:148 / D24 — the backend-down recovery next step. fable-implement Step 1
	// (issue #584, decisions.md D6) gave `af up` and the watchdog's periodic tick an autonomous
	// relaunch path, so the login shell is now a MANUAL FALLBACK for the one case they cannot
	// autonomously clear (a session that is alive but wedged, not exited — decisions.md D2) rather
	// than the sole recovery route.
	nextBackendDown = "start a login shell"

	// design-doc.md:149 — the credential-rejected next step.
	nextCredential = ".agentfactory/secrets/telemetry.root"

	// design-doc.md:153 / D1 — join honesty.
	copyJoinless     = "no token data arrived for this step"
	copyUnattributed = "unattributed"

	// design-doc.md:153 / D10 — two-truths honesty, worded to name the ARTIFACT rather than a
	// Settings control, because the Settings page renders no telemetry control at all
	// (renderSettings, app.js — verified: zero `telemetry` occurrences in app.js before this phase).
	copyCurrentRecording = "current recording state"
	copyStartupDefault   = "startup default"
)

// The renderer function names this file ties its assertions to. Extracting a named function's body
// (funcBody) is what lets an assertion be bound to ONE function without the >1000-char bounded
// regex window RE2 forbids (agentdetail_layout_test.go:42-45).
const (
	fnBanner = "renderTelemetryBanner"
	fnSteps  = "renderTelemetrySteps"

	// The shared dark-state sentence generator. Named here because three renderers must route
	// through the SAME one: two did from the start, the steps column did not.
	fnUsageStateText = "telUsageStateText"
)

// ---------------------------------------------------------------------------
// Structural regexes. Each is exercised by a self-negative below.
// ---------------------------------------------------------------------------

// The nav anchor carries data-route="telemetry" (that attribute, not the href, is what syncNav
// matches on and what the click interceptor selects).
var reNavTelemetryDataRoute = regexp.MustCompile(`id="nav-telemetry"[^>]*data-route="telemetry"|data-route="telemetry"[^>]*id="nav-telemetry"`)

// The section ships WITH `hidden`. boot() never calls showView, so first-paint visibility is
// governed solely by this attribute; without it the panel renders stacked under the Floor.
var reViewTelemetryHidden = regexp.MustCompile(`<section[^>]*id="view-telemetry"[^>]*\bhidden\b|<section[^>]*\bhidden\b[^>]*id="view-telemetry"`)

// The telemetry branch in navigate() must sit AFTER syncNav(route), so the highlight moves. Note
// nav_test.go's reNavigateCallsSyncNav cannot catch a violation here: its non-greedy range
// terminates on the earlier syncNav('floor') in the agent/ branch and never reaches syncNav(route).
var reNavigateTelemetryBranch = regexp.MustCompile(`syncNav\(route\)[\s\S]*?route === 'telemetry'[\s\S]*?TelemetryViewModel\.activate`)

// The FALSIFIED join. report.step is a step TITLE when one exists, falling back to the id
// (telemetryStepLabel, internal/cmd/telemetry.go:361-366); usage.step_bucket is always a step ID,
// optionally suffixed "-tail" (install_telemetry_views/agent-model-step-tokens.json:48); and
// ReportRowDTO carries no step id at all (telemetryview.go:134-145). Comparing the two marks EVERY
// titled step joinless, firing the honesty copy universally and falsely — the exact inverse of the
// honesty this renderer exists to provide.
var reNaiveStepJoin = regexp.MustCompile(`\.step\s*===?\s*[A-Za-z_$.]*step_bucket|step_bucket\s*===?\s*[A-Za-z_$.]*\.step\b`)

// A zero rendered where an unknown belongs — the silent fallback decisions.md D5 bans outright.
var reZeroFallback = regexp.MustCompile(`(?:total_tokens|input_tokens|output_tokens|duration_ms)\s*\|\|\s*0`)

// The steps renderer consults the usage payload's own token state, not merely its transport.
//
// A COMPARISON is required, not a mention — and the body is comment-stripped before matching, so
// neither the operator nor the field name can be supplied by prose. The tokens half is
// the correct axis — the top-level usage.state is worstUsageState(tokens, metrics), so a payload
// whose METRICS half is dark carries a dark top-level state while tokens.state is "ok" with rows
// present. Keying on the top-level state would suppress token data that was measured and did
// arrive, trading one false verdict for another.
var reStepsReadsUsageState = regexp.MustCompile(`(?:tok|tokens)\.state\s*(?:===?|!==?)\s*['"]ok['"]`)

// Any timer registration.
var reTimerCall = regexp.MustCompile(`\b(?:window\.)?set(?:Interval|Timeout)\s*\(`)

// ---------------------------------------------------------------------------
// The state catalogue. `detect` entries are the DTO field reads that DISCRIMINATE the leaves —
// this is the layer that defeats "define seven constants, render one of them", because a renderer
// that emits a single line cannot read seven discriminating fields.
// ---------------------------------------------------------------------------

type telemetryState struct {
	id     string
	detect []string
	copy   string
	next   string
}

func telemetryStateCatalogue() []telemetryState {
	return []telemetryState{
		{
			id:     "not-installed",
			detect: []string{"installed", "present", "valid", "endpoint"},
			copy:   copyNotInstalled,
			next:   nextNotInstalled,
		},
		{
			id:     "recording-off",
			detect: []string{"recording", "enabled"},
			copy:   copyCurrentRecording,
			next:   "af telemetry on",
		},
		// backend-unreachable is likewise NOT here: its sentence is signal.summary, owned by the
		// root. Keeping it in this catalogue produced a cross-state FALSE PASS — the word
		// "unreachable" was satisfied by an unrelated line's copy elsewhere in the function while
		// the leaf itself was never checked. checkProbeVerdicts covers it at the emission.
		// NOTE: the probe-driven leaves (credential-rejected, address-not-served, refused) are NOT
		// in this catalogue, because their visible sentence is not the panel's to write — it arrives
		// in the payload as signal.summary, already rendered root-side by ProbeResult.Summary().
		// Asserting those sentences as source literals here would MANDATE the anti-pattern:
		// a hardcoded re-derivation that then drifts silently from the root's wording. They are
		// checked by checkProbeVerdicts below, which asserts a strictly stronger property.
		{
			id:     "unprobed-credential-cause",
			detect: []string{"unprobed_cause"},
			copy:   "was not probed",
			next:   nextCredential,
		},
		{
			id:     "external-endpoint",
			detect: []string{"127.0.0.1", "localhost"},
			copy:   "your endpoint is external",
			next:   "use that backend's own UI",
		},
		{
			id:     "healthy-empty",
			detect: []string{"rows"},
			copy:   copyHealthyEmpty,
			next:   "af telemetry on",
		},
		{
			id:     "loss",
			detect: []string{"malformed", "dropped"},
			copy:   copyLossSkipped,
			next:   copyLossDropped,
		},
		{
			id:     "error-envelope",
			detect: []string{"'error'"},
			copy:   "could not read this factory's telemetry",
			next:   "from inside an agentfactory workspace",
		},
	}
}

// stripJSComments removes // and /* */ comments while respecting string literals, so an assertion
// can be bound to CODE rather than to prose. This matters concretely: without it, indexing the
// banner body for "installed"/"recording" finds them inside an explanatory comment ~1400 chars
// above the code that emits either line, and the axis-ORDER assertion silently measures the comment
// instead of the emission. The string-literal handling is not optional — the loopback test contains
// the literal '//127.0.0.1', which a naive stripper would treat as a comment and delete.
func stripJSComments(src string) string {
	var out []byte
	inS, inD, inLine, inBlock := false, false, false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				out = append(out, c)
			}
		case inBlock:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				inBlock = false
				i++
			}
		case inS:
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				i++
				out = append(out, src[i])
			} else if c == '\'' {
				inS = false
			}
		case inD:
			out = append(out, c)
			if c == '\\' && i+1 < len(src) {
				i++
				out = append(out, src[i])
			} else if c == '"' {
				inD = false
			}
		case c == '\'':
			inS = true
			out = append(out, c)
		case c == '"':
			inD = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			inLine = true
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			inBlock = true
			i++
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// callArgs returns the argument text of every `name(` call in src, by paren matching. This is what
// turns "the literal exists somewhere in the function" into "the literal is passed to the emitter" —
// the difference between a dead constant and a rendered line.
func callArgs(src, name string) []string {
	var out []string
	needle := name + "("
	for i := 0; i+len(needle) <= len(src); {
		j := strings.Index(src[i:], needle)
		if j < 0 {
			break
		}
		start := i + j + len(needle)
		depth := 1
		k := start
		for ; k < len(src) && depth > 0; k++ {
			switch src[k] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			break
		}
		out = append(out, src[start:k-1])
		i = k
	}
	return out
}

// checkProbeVerdicts covers the probe-driven backend leaves. Their sentence is owned by the ROOT
// module (ProbeResult.Summary(), internal/telemetry/probe.go:36-50) and travels in the payload, so
// the honest requirement is not "the panel contains this sentence" but "the panel renders the
// payload's sentence and does not invent its own". This checks four things where a source-literal
// assertion would have checked one — and it actively FORBIDS the hardcoding that a literal
// assertion would have required.
func checkProbeVerdicts(body string) []string {
	var v []string

	// (a) the payload's own sentence is what reaches the operator.
	if !strings.Contains(body, ".summary") {
		v = append(v, "the backend axis must render signal.summary verbatim — that sentence is the root module's, and re-wording it here would let the two drift apart")
	}
	// (b) and it is NOT re-derived. A hardcoded copy of a root-owned string is the drift itself.
	for _, hardcoded := range []string{copyProbe404, copyProbeCredental, "reachable but refused the data"} {
		if strings.Contains(body, hardcoded) {
			v = append(v, fmt.Sprintf("the banner hardcodes the probe sentence %q instead of rendering signal.summary — this copy will silently drift from ProbeResult.Summary()", hardcoded))
		}
	}
	// (c0) the transport-error discriminator is `status === 0` specifically. probeOne never assigns
	// res.Status when Err != nil, so 0 is the transport case; changing the constant makes the PRIMARY
	// backend-down recovery dead code while its sentence stays in the file satisfying a presence check.
	if !regexp.MustCompile(`sig\.status\s*===\s*0\b`).MatchString(body) {
		v = append(v, "the transport-error branch must key on `sig.status === 0` — that is the value probeOne leaves when Err != nil, and any other constant makes the login-shell recovery unreachable")
	}
	// (c) the NEXT STEP is the panel's own, and it is keyed on the status the probe discriminates.
	for _, code := range []string{"401", "403", "404"} {
		if !strings.Contains(body, code) {
			v = append(v, fmt.Sprintf("the backend axis must branch on HTTP %s — the probe discriminates these and the recovery differs per class", code))
		}
	}
	// (d) each class names a DISTINCT recovery. A shared next step would collapse leaves the probe
	// went to the trouble of telling apart.
	steps := map[string]string{
		"transport":  nextBackendDown,
		"credential": nextCredential,
		"not-served": "/api/default",
	}
	for id, step := range steps {
		if !strings.Contains(body, step) {
			v = append(v, fmt.Sprintf("backend leaf %q must name its own next step %q", id, step))
		}
	}
	// ... and the summary must be emitted WITH a next step, in one call. The per-class strings are
	// built into a variable first, so the pairing is proved at the telLine call rather than by the
	// three literals merely coexisting in the function.
	paired := false
	for _, c := range callArgs(body, "telLine") {
		if strings.Contains(c, ".summary") && strings.Contains(c, "next") {
			paired = true
		}
	}
	if len(callArgs(body, "telLine")) > 0 && !paired {
		v = append(v, "the backend axis must emit signal.summary together with its per-class next step in one telLine() call")
	}
	for a, sa := range steps {
		for b, sb := range steps {
			if a != b && strings.Contains(sa, sb) {
				v = append(v, fmt.Sprintf("backend leaf %q next step contains leaf %q next step — the two recoveries are not distinguishable", a, b))
			}
		}
	}
	return v
}

// checkTelemetryStates is a PURE checker over content strings, so the self-negative fixtures drive
// exactly the same code path as the real asset (the traceContract precedent, contracttrace_test.go).
func checkTelemetryStates(appJS string, states []telemetryState) []string {
	var v []string

	raw := funcBody(appJS, fnBanner)
	if raw == "" {
		return []string{fmt.Sprintf("app.js: no function %s — the banner stack has no renderer, so no state can render", fnBanner)}
	}
	// Every assertion below runs on comment-stripped CODE. A guarantee that a comment can satisfy is
	// not a guarantee.
	body := stripJSComments(raw)

	v = append(v, checkProbeVerdicts(body)...)

	// A constant-truth guard is how a renderer gets short-circuited into painting nothing while every
	// literal below survives as dead code. Static analysis cannot prove runtime reachability, but it
	// can refuse the one shape that fakes it.
	if regexp.MustCompile(`if\s*\(\s*(?:true|1)\s*\)`).MatchString(body) {
		v = append(v, "the banner renderer contains a constant-true condition — that is how every leaf becomes dead code while the literals below still satisfy a source scan")
	}

	// Layer A — reachability. The discriminating field reads must be INSIDE the renderer.
	for _, s := range states {
		for _, d := range s.detect {
			if !strings.Contains(body, d) {
				v = append(v, fmt.Sprintf("state %q: %s does not read %q — the leaf is unreachable, so its copy is a dead constant", s.id, fnBanner, d))
			}
		}
	}

	// Layer B — vocabulary and actionability, bound to the EMITTER. `telLine(` is the only thing
	// that puts a line on screen, so requiring the copy to be one of its arguments is what separates
	// "the sentence exists in this function" from "the sentence is rendered". A literal sitting in a
	// dead branch, a comment, or an unused constant no longer satisfies this.
	calls := callArgs(body, "telLine")
	if len(calls) < len(states) {
		v = append(v, fmt.Sprintf("the banner has %d telLine() call sites for %d states — at least one state cannot be emitted", len(calls), len(states)))
	}
	for _, s := range states {
		paired := false
		for _, c := range calls {
			if strings.Contains(c, s.copy) && strings.Contains(c, s.next) {
				paired = true
				break
			}
		}
		if !paired {
			v = append(v, fmt.Sprintf("state %q: no single telLine() call carries BOTH its copy %q and its next step %q — checking them separately lets one state's line satisfy another's check", s.id, s.copy, s.next))
		}
	}

	// Layer C — distinctness as NON-CONTAINMENT. Pairwise inequality is vacuous: the test author
	// writes the table, so inequality is a property of the test. Containment is the real hole — if
	// leaf A's copy is a substring of leaf B's, one render of B satisfies A's presence check too,
	// and the suite claims N distinct states while the code ships one.
	for i := range states {
		for j := range states {
			if i == j {
				continue
			}
			if strings.Contains(states[i].copy, states[j].copy) {
				v = append(v, fmt.Sprintf("state %q copy contains state %q copy — one render satisfies both checks, so these states cannot be distinguished", states[i].id, states[j].id))
			}
		}
	}

	// Layer D — composition order (T-5): the banner is a STACK in the fixed order
	// installed -> recording -> backend. The offsets are taken from the axis PREDICATES in
	// comment-stripped code, not from the bare axis words. Indexing the bare words is what let an
	// explanatory comment satisfy this check while the emission blocks were in the wrong order.
	iInst := strings.Index(body, "degradedInstalled")
	iRec := strings.Index(body, "recording.enabled === false")
	iBack := strings.Index(body, "backend.probed === true")
	switch {
	case iInst < 0:
		v = append(v, "banner must branch on a degraded-installed predicate")
	case iRec < 0:
		v = append(v, "banner must branch on `recording.enabled === false`")
	case iBack < 0:
		v = append(v, "banner must branch on `backend.probed === true`")
	case !(iInst < iRec && iRec < iBack):
		v = append(v, fmt.Sprintf("banner axes must be EMITTED in the fixed order installed -> recording -> backend (predicate offsets %d/%d/%d) — T-5 requires a stack in a fixed order", iInst, iRec, iBack))
	}

	// Healthy-empty must be gated on what the axes actually emitted, not on a proxy. Keying it on a
	// single axis (e.g. `backend.probed === true`) renders "everything is wired" directly beneath a
	// 404 line, and ignoring the filter renders "run a formula" when the operator merely filtered to
	// a run with no rows.
	if !strings.Contains(body, "host.children.length") {
		v = append(v, "healthy-empty must be gated on how many axis lines were emitted (host.children.length), not on a single axis standing in for all three")
	}
	if !strings.Contains(body, "vm.agent") || !strings.Contains(body, "vm.instance") {
		v = append(v, "healthy-empty must be suppressed when a filter is active — otherwise a filter matching nothing reports the factory as empty")
	}
	// The fabrication guard: a stand-in where an integer belongs contradicts the exact counts the
	// steps pane prints from the same payload on the same screen.
	if regexp.MustCompile(`(?:malformed|dropped)\s*\|\|`).MatchString(body) {
		v = append(v, "the loss line must render the integers from stats, never a `||` stand-in — a fabricated count is exactly the confident false statement this panel exists to remove")
	}

	return v
}

// checkJoinlessRenderer pins the JOIN KEY, not merely the presence of the honesty copy. Presence
// alone is the weak half: a renderer using the falsified step-title/step-id comparison emits the
// copy for EVERY titled step, and a presence-only test passes that broken implementation happily.
func checkJoinlessRenderer(body string) []string {
	var v []string
	if body == "" {
		return []string{fmt.Sprintf("app.js: no function %s — nothing renders the step timings, so no step can report a joinless verdict", fnSteps)}
	}
	// EVERY leg below runs on comment-stripped source. There is no JS engine in either module, so
	// these assertions read text — and text that includes comments can be satisfied by prose. That
	// is not hypothetical: with the comments left in, the whole transport-only renderer this
	// checker exists to reject can be restored, with one comment mentioning tok.state and
	// telUsageStateText, and the entire web lane stays green. A structural check that a comment can
	// satisfy is not a check.
	body = stripJSComments(body)
	if reNaiveStepJoin.MatchString(body) {
		v = append(v, "joins report.step to usage.step_bucket — a step TITLE against a step ID, which marks every titled step joinless (decisions.md D1)")
	}
	for _, key := range []string{"instance_id", "formula_run"} {
		if !strings.Contains(body, key) {
			v = append(v, fmt.Sprintf("must join on the only clean key pair instance_id <-> formula_run; missing %q", key))
		}
	}
	if !strings.Contains(body, copyJoinless) {
		v = append(v, fmt.Sprintf("must emit the explicit unknown %q, never a blank and never a zero", copyJoinless))
	}
	if !strings.Contains(body, copyUnattributed) {
		v = append(v, "copy must acknowledge token data may exist UNATTRIBUTED — a joinless step has a second cause besides a dead backend, and an operator must not be sent to chase a backend fault that is an attribution artifact")
	}
	if reZeroFallback.MatchString(body) {
		v = append(v, "a `|| 0` fallback renders a zero where an unknown belongs (decisions.md D5: no silent fallbacks)")
	}
	// The verdict polarity. Inverting this one comparison turns the honesty copy inside out — every
	// step WITH token data would be labelled as having none — while every literal above stays put.
	if !regexp.MustCompile(`runMatch\.length\s*===\s*0`).MatchString(body) {
		v = append(v, "the joinless verdict must fire on an EMPTY run-level match (`runMatch.length === 0`); inverting it labels exactly the steps that do have token data")
	}
	// "Not measured yet" is a third state. Without it, every row asserts the explicit unknown while
	// the usage request is still in flight — an unknown claimed before the measurement exists.
	if !strings.Contains(body, "usageKnown") {
		v = append(v, "the steps renderer must distinguish 'usage not measured yet' from 'usage measured and empty' — the three reads are concurrent and report resolves first")
	}
	// "Measured and empty" is not "never measured". usageKnown above is a TRANSPORT predicate: it is
	// true for every dark payload, because the relay carries degradation as data at HTTP 200 with
	// ok:true and reserves ok:false for a relay failure (server.go, telemetryRelay). So for
	// not_installed, backend_down, credential_rejected and query_failed the rows are empty because
	// nothing was queried — and without this leg every row reports the joinless verdict, a measured
	// negative about a measurement that never happened. The renderer must consult the PAYLOAD's own
	// state, which is exactly what both sibling panes already do before rendering their tables.
	if !reStepsReadsUsageState.MatchString(body) {
		v = append(v, fmt.Sprintf("must compare the usage payload's own token state against 'ok' before reporting %q — usageKnown is a transport predicate and is TRUE for every dark payload, so a state-blind renderer asserts a measured negative for not_installed / backend_down / credential_rejected / query_failed, where nothing was measured at all. The literal matters: `tok.state !== 'zzz'` reads like a state check, is true for every dark payload, and restores the defect intact.", copyJoinless))
	}
	// Ordering, not merely presence. Both arms can exist, in the right words, and still produce the
	// original defect if the joinless arm is reached first: runMatch is empty for every dark payload,
	// so whichever arm tests it first wins and the dark arm becomes unreachable. Presence checks are
	// blind to that; an index comparison is not.
	if iDark, iJoinless := strings.Index(body, fnUsageStateText), strings.Index(body, copyJoinless); iDark >= 0 && iJoinless >= 0 && iDark > iJoinless {
		v = append(v, fmt.Sprintf("the dark-state arm is placed AFTER the joinless arm, which makes it dead code: `runMatch.length === 0` is true for every payload whose query never ran, so the joinless verdict fires first and %q is reported for not_installed / backend_down / credential_rejected / query_failed exactly as before. The state check must come first.", copyJoinless))
	}
	// The dark state must be reported in the operator's terms, not merely detected. telUsageStateText
	// is the sentence generator both sibling panes use (renderTelemetryTokens, renderTelemetryMetrics);
	// minting separate copy here would drift from the two panes an operator reads on the same screen.
	if !strings.Contains(body, fnUsageStateText) {
		v = append(v, fmt.Sprintf("must render the dark-state sentence through %s(), the same generator the Tokens and Metrics panes use — a third wording for the same five states drifts from the panes shown beside it", fnUsageStateText))
	}
	return v
}

// ---------------------------------------------------------------------------
// AC 1 / AC 7 — the view is assimilated into the pinned navigation model.
// ---------------------------------------------------------------------------

// Mirrors the view-agent pin at agentdetail_test.go:57-66. Forgetting the VIEW_IDS entry blanks the
// page: showView hides every id in the list except its target, so a section absent from the list is
// never shown.
func TestTelemetryPanel_ViewRegistered(t *testing.T) {
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	if c := strings.Count(indexHTML, `id="view-telemetry"`); c != 1 {
		t.Errorf(`index.html: exactly one id="view-telemetry" section expected, got %d`, c)
	}
	if c := strings.Count(indexHTML, `data-route="telemetry"`); c != 1 {
		t.Errorf(`index.html: exactly one data-route="telemetry" expected, got %d`, c)
	}
	if !reNavTelemetryDataRoute.MatchString(indexHTML) {
		t.Error(`index.html: #nav-telemetry must carry data-route="telemetry" — syncNav matches on that attribute and the click interceptor selects on it`)
	}
	if !reViewTelemetryHidden.MatchString(indexHTML) {
		t.Error(`index.html: #view-telemetry must ship with the hidden attribute — boot() never calls showView, so first paint is governed by the raw attribute and the panel would otherwise render under the Floor`)
	}
	if !strings.Contains(appJS, `'view-telemetry'`) {
		t.Error("app.js: VIEW_IDS must contain 'view-telemetry' — else showView('view-telemetry') hides every section and the page blanks")
	}
	if !reNavigateTelemetryBranch.MatchString(appJS) {
		t.Error("app.js: navigate() must dispatch the telemetry route AFTER syncNav(route), so the tab highlight moves with the route")
	}
	if !strings.Contains(appJS, "window.TelemetryViewModel") {
		t.Error("app.js: TelemetryViewModel must be exposed on window alongside the other view-models (the logical view-model contract)")
	}
}

// Both ends of the JS<->HTML contract. Every renderer opens with `var host = byId('…'); if (!host)
// return;` — the file's universal idiom — so a typo'd id makes the pane SILENTLY no-op.
// TestIndexHTML_DarkGroupHostPresent (agentdetail_test.go:124-135) exists for exactly this reason
// and is the template here.
func TestTelemetryPanel_HostsPresent(t *testing.T) {
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	for _, id := range []string{
		"tel-banner", "tel-gate", "tel-endpoint",
		"tel-agent", "tel-instance", "tel-filter-err",
		"tel-fresh", "tel-refresh",
		"tel-steps", "tel-steps-note",
		"tel-tokens", "tel-tokens-note", "tel-window",
		"tel-metrics", "tel-metrics-note",
	} {
		if c := strings.Count(indexHTML, `id="`+id+`"`); c != 1 {
			t.Errorf(`index.html: exactly one id=%q expected, got %d`, id, c)
		}
		if !strings.Contains(appJS, `byId('`+id+`')`) {
			t.Errorf("app.js: must bind #%s by name — an unbound host makes its pane silently no-op", id)
		}
	}
}

// ---------------------------------------------------------------------------
// AC 2 — no hash/popstate router was introduced.
// ---------------------------------------------------------------------------

// The console has NO hash router: every nav click is preventDefault()-ed into
// AppViewModel.navigate() and the URL never changes. Three design artifacts (ux.md:8,
// codebase-snapshot.md:272, source.md:288) describe navigation as "hash-routed" and are wrong; the
// design-doc (L99) is the correct source. Introducing a router would be a navigation-model
// departure landing outside every pinned test.
func TestTelemetryPanel_NoHashRouterIntroduced(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	re := regexp.MustCompile(`hashchange|popstate|location\.hash|window\.location`)
	if n := len(re.FindAllString(appJS, -1)); n != 1 {
		t.Errorf("app.js: expected exactly 1 hash/popstate occurrence (the pre-existing comment denying the router's existence), got %d — no routing machinery may be introduced", n)
	}
}

// ---------------------------------------------------------------------------
// AC 3b — the telemetry view-model registers no timer.
// ---------------------------------------------------------------------------

// Scoping note. The outline originally proposed
//
//	awk '/TelemetryViewModel/,/^  };/' app.js | grep -c 'setInterval|setTimeout'
//
// and peer review falsified it. The falsification's CONCLUSION is right and its stated mechanism is
// wrong: the view-model literals do close with a 2-space `};` (twelve such terminators exist), so
// the end anchor is fine. The real defect is that an awk range RE-TRIGGERS on every later mention of
// the start pattern, and the last mention has no following terminator, so the range runs to EOF and
// sweeps in boot()'s timers. An ANCHORED extraction has no such behaviour, which is why this test
// uses vmObjectBody — already package-level, already documented, already used by the contract trace.
func TestTelemetryPanel_NoTimerRegistered(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	body, ok := vmObjectBody(appJS, "TelemetryViewModel")
	if !ok {
		t.Fatal("app.js: TelemetryViewModel is not declared as a top-level `var TelemetryViewModel = {` object literal")
	}
	if hits := reTimerCall.FindAllString(body, -1); len(hits) != 0 {
		t.Errorf("TelemetryViewModel must register no timer, found %v — Decision 9: on-demand plus Refresh, no auto-poll and no auto-retry against a rendered dark state", hits)
	}
	for _, fn := range []string{fnBanner, fnSteps} {
		if hits := reTimerCall.FindAllString(funcBody(appJS, fn), -1); len(hits) != 0 {
			t.Errorf("%s must register no timer, found %v", fn, hits)
		}
	}

	// Repo-wide PINS, not bans: both counts are legitimate pre-existing call sites (boot()'s 5s
	// Floor poll and the 1s staleness clock; the toast dismissal and the boot-class removal). A ban
	// would false-fail on all four; a pin catches any telemetry addition anywhere in the file,
	// including in helpers outside the view-model literal.
	if n := strings.Count(appJS, "setInterval"); n != 2 {
		t.Errorf("app.js: setInterval count must stay at 2 (the 5s Floor poll and the 1s staleness clock), got %d", n)
	}
	if n := strings.Count(appJS, "setTimeout"); n != 2 {
		t.Errorf("app.js: setTimeout count must stay at 2 (the toast dismissal and the boot-class removal), got %d", n)
	}
}

// ---------------------------------------------------------------------------
// AC 5 — every state renders distinctly, none renders an empty pane.
// ---------------------------------------------------------------------------

func TestTelemetryPanel_EveryStateRendersDistinctly(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	for _, v := range checkTelemetryStates(appJS, telemetryStateCatalogue()) {
		t.Error(v)
	}
}

// ---------------------------------------------------------------------------
// AC 6 — a joinless step renders an explicit unknown, not a zero.
// ---------------------------------------------------------------------------

func TestTelemetryPanel_JoinlessStepRendersUnknown(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	for _, v := range checkJoinlessRenderer(funcBody(appJS, fnSteps)) {
		t.Error(v)
	}
}

// ---------------------------------------------------------------------------
// AC 5, filter axis — an all-empty metrics answer is not an idle factory.
// ---------------------------------------------------------------------------

// The three axes AC-5 names — installed, recording, reachable — are all honest. A fourth way to
// read nothing is not: every metric can be queried successfully and return an empty vector,
// because the metric NAMES this factory queries are Claude Code's and Claude Code versions
// independently of this repo. queryMetrics reports that as state "ok" with zero rows, which the
// pane renders identically to a factory that simply did no work.
//
// The renderer must not paint the idle sentence over it. The root DTO carries the distinction in
// `detail` (the field the metrics half already uses for its failure classes), and the console can
// also derive it unaided: queryMetrics appends one state per metric QUERIED, so `state === 'ok'`
// with zero rows means every query succeeded and none returned a series.
func TestTelemetryPanel_MetricsEmptyIsNotRenderedAsIdle(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	// Comment-stripped for the same reason as checkJoinlessRenderer: otherwise restoring the
	// misleading idle sentence and leaving a comment that mentions m.detail passes.
	body := stripJSComments(funcBody(appJS, "renderTelemetryMetrics"))
	if body == "" {
		t.Fatal("app.js: no function renderTelemetryMetrics — nothing renders the session metrics")
	}

	// The empty branch must consult `detail` — and the assertion has to be bound to THAT branch.
	//
	// Searching the whole function body for `detail` passes today without any change at all: the
	// dark-state arm two lines earlier already renders telUsageStateText(m.state, u.cause ||
	// m.detail). The first draft of this test did exactly that and reported success against the
	// unfixed renderer. What matters is the zero-rows path, which is the one that prints the idle
	// sentence over a successful query that returned nothing.
	empty := strings.Index(body, "rows.length === 0")
	if empty < 0 {
		t.Fatal("renderTelemetryMetrics no longer branches on an empty row set; the assertions below cannot be bound to that path")
	}
	tail := body[empty:]
	if !regexp.MustCompile(`\bm\.detail\b|\bmetrics\.detail\b`).MatchString(tail) {
		t.Error("renderTelemetryMetrics never reads the metrics half's `detail` on the ZERO-ROWS path. A successful query of every session metric that returns no series is reported as state \"ok\" with rows [], and printing the plain idle sentence there tells an operator the factory did nothing — when what actually happened is that none of the names this factory asks for exists any more. Recording is on, the backend is healthy, all five dark states are clean, and the pane is silently empty")
	}

	// The sentence that caused the finding must be gone from that branch, not merely joined by a
	// better one. Requiring `detail` alone leaves "No session metrics in this window." free to come
	// back beside it — and that sentence is what an operator reads as "your factory did nothing".
	if strings.Contains(tail, "No session metrics in this window") {
		t.Error("the zero-rows branch still renders \"No session metrics in this window.\" — the " +
			"sentence reports an idle factory for a state that is also produced by every metric name " +
			"having moved, which is the conflation this assertion exists to remove. The replacement " +
			"must state both possibilities rather than sit beside the old claim")
	}

	// And it must not do so as a fallback that reinstates the false sentence when `detail` is
	// absent — an older console fronting a newer af (rendezvous.Ensure reuses a healthy running
	// webui, so that pairing is routine) would fall straight back into the defect.
	if regexp.MustCompile(`(?:m|metrics)\.detail\s*\|\|\s*['"]No session metrics`).MatchString(body) {
		t.Error("`m.detail || 'No session metrics…'` is a silent fallback (decisions.md D24/F-2): under version skew it prints exactly the false sentence this assertion exists to remove — derive the condition from state and row count instead, and append detail when it is present")
	}
}

// Corruption must never render as "no records yet". This guards a defect that the structural
// assertions above did NOT catch and that only executing the renderer against report-corrupt.json
// revealed: the pane emitted a bare "No step records yet" for a payload with zero rows but
// malformed=2 / dropped=7, which is a false statement to an operator whose records exist and cannot
// be read. The CLI is explicit that loss is "reported before the empty check, not after it"
// (internal/cmd/telemetry.go:277-280), so the ordering is the assertion.
func TestTelemetryPanel_CorruptionNeverReadsAsEmpty(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	body := funcBody(appJS, fnSteps)
	if body == "" {
		t.Fatalf("app.js: no function %s", fnSteps)
	}
	for _, counter := range []string{"malformed", "dropped", "dropped_unexported"} {
		if !strings.Contains(body, counter) {
			t.Errorf("%s must read stats.%s — all three counters count, including the never-exported one the CLI's own degraded predicate omits", fnSteps, counter)
		}
	}
	iLoss := strings.Index(body, "malformed")
	iEmpty := strings.Index(body, copyHealthyEmpty)
	if iEmpty >= 0 && iLoss >= 0 && iLoss > iEmpty {
		t.Error("the loss check must precede the empty-state copy — a log whose every line is corrupt renders no rows, and 'no records yet' is exactly the wrong thing to tell that operator")
	}
	if iEmpty < 0 {
		t.Errorf("%s must carry the pinned unfiltered-empty copy verbatim", fnSteps)
	}
}

// "Never a blank pane" is a guarantee about ALL FOUR surfaces, not just the one with an acceptance
// criterion. Each pane renderer must carry an empty-state sentence, a not-yet-loaded sentence, and a
// read-failure sentence, and must paint its shell before any fetch resolves.
func TestTelemetryPanel_NoSurfaceRendersBlank(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	for _, fn := range []string{"renderTelemetrySteps", "renderTelemetryTokens", "renderTelemetryMetrics"} {
		body := stripJSComments(funcBody(appJS, fn))
		if body == "" {
			t.Errorf("app.js: no function %s", fn)
			continue
		}
		// Every early exit must have painted something first: an empty pane is the failure this
		// feature exists to remove, so a bare `return` before any appendChild is the shape to refuse.
		if !strings.Contains(body, "appendChild") {
			t.Errorf("%s never appends a node — it can only render blank", fn)
		}
		if n := len(callArgs(body, "el")); n < 3 {
			t.Errorf("%s builds only %d elements; it cannot cover loading / failed / empty / populated", fn, n)
		}
		for _, needed := range []string{"Fail", "still loading", "could not"} {
			if !strings.Contains(body, needed) {
				t.Errorf("%s must render a distinct state for %q rather than falling through to an empty pane", fn, needed)
			}
		}
		// The zero-row case is the one that actually renders nothing if its guard is deleted, so it
		// is pinned per surface rather than inferred from the presence of other states.
		if !regexp.MustCompile(`(?:rows\.length === 0|length === 0)`).MatchString(body) {
			t.Errorf("%s must branch on an empty row set — without that guard the pane renders nothing at all", fn)
		}
		if !strings.Contains(body, "No ") {
			t.Errorf("%s must carry an explicit empty-state sentence beginning \"No \" — a pane with rows deleted must still say something", fn)
		}
	}

	// The banner's own all-green fallback: when no axis emitted a line, something must still be said.
	banner := stripJSComments(funcBody(appJS, fnBanner))
	if !strings.Contains(banner, "!host.firstChild") {
		t.Error("the banner must carry an all-green fallback keyed on an empty host — otherwise a fully healthy factory renders a blank banner, which is the blank pane this feature exists to remove")
	}

	// The shell is painted SYNCHRONOUSLY on activate, before any fetch resolves — otherwise the
	// panel is blank for up to 35 seconds on exactly the dead-backend path it exists to serve.
	act := stripJSComments(funcBody(appJS, "") + appJS)
	if !regexp.MustCompile(`activate:\s*function[\s\S]{0,400}?renderTelemetry\(\)[\s\S]{0,200}?refresh\(\)`).MatchString(act) {
		t.Error("TelemetryViewModel.activate must call renderTelemetry() synchronously BEFORE refresh() — otherwise nothing paints until the slowest fetch returns")
	}
}

// The two-truths half that names the STARTUP DEFAULT lives in the chrome renderer, not the banner.
// Asserting it against the banner would silently never fire, which is how an unreferenced constant
// leaves a required guarantee unenforced.
func TestTelemetryPanel_TwoTruthsLabelled(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	body := stripJSComments(funcBody(appJS, "renderTelemetryChrome"))
	if body == "" {
		t.Fatal("app.js: no function renderTelemetryChrome")
	}
	if !strings.Contains(strings.ToLower(body), copyCurrentRecording) {
		t.Errorf("the gate must be labelled %q — never a bare on/off", copyCurrentRecording)
	}
	if !strings.Contains(body, copyStartupDefault) {
		t.Errorf("the chrome must name the %q as a separate artifact — the two can legitimately disagree and an operator reads one as a bug otherwise", copyStartupDefault)
	}
	if !strings.Contains(body, "startup.json") {
		t.Error("the cross-reference must name startup.json — the Settings page renders no telemetry control, so pointing at it would be a dead pointer")
	}
}

// ---------------------------------------------------------------------------
// Protective assertions — the DO-NOT-CHANGE list. These pass BEFORE the implementation and must
// keep passing; they exist to prove the new panel did not disturb what it sits beside.
// ---------------------------------------------------------------------------

func TestTelemetryPanel_ProtectsPinnedInvariants(t *testing.T) {
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	varsCSS := readAsset(t, filepath.Join(staticDir, "styles", "variables.css"))

	// #nav-floor keeps BOTH frozen attributes; it is the one anchor that does.
	if !regexp.MustCompile(`id="nav-floor"[^>]*href="/"`).MatchString(indexHTML) {
		t.Error(`index.html: #nav-floor must keep href="/"`)
	}
	if !regexp.MustCompile(`id="nav-floor"[^>]*aria-current="page"`).MatchString(indexHTML) {
		t.Error(`index.html: #nav-floor must keep its initial aria-current="page" for the pre-JS first paint`)
	}
	// The new anchor must NOT carry a second frozen aria-current — syncNav owns the attribute.
	if regexp.MustCompile(`id="nav-telemetry"[^>]*aria-current`).MatchString(indexHTML) {
		t.Error(`index.html: #nav-telemetry must not hardcode aria-current — JS owns the highlight`)
	}

	// variables.css pins (palette_audit_test.go) survive: additive tokens only.
	if !strings.Contains(varsCSS, "@import") {
		t.Error("variables.css: the @import must stay (identical-stacks fallback, Decision 7)")
	}
	if strings.Contains(varsCSS, "@font-face") {
		t.Error("variables.css: no @font-face this phase")
	}
	for _, tok := range []string{"--font-display:", "--font-body:", "--font-mono:", "--rarity-legendary:", "--rarity-epic:", "--rarity-rare:", "--rarity-common:"} {
		if !strings.Contains(varsCSS, tok) {
			t.Errorf("variables.css: token %q must remain declared", tok)
		}
	}

	// The shared refresh strip stays Floor-bound; the telemetry section carries its OWN controls.
	if !strings.Contains(appJS, "refresh: function () { return FloorViewModel.refresh(); }") {
		t.Error("app.js: AppViewModel.refresh must stay bound to the Floor — the telemetry section owns a separate Refresh, and reworking the shared strip is out of scope")
	}

	// No rarity class token may enter via the new markup (palette_audit_test.go:67-80).
	reRarity := regexp.MustCompile(`r-(legendary|epic|rare|common)`)
	for name, src := range map[string]string{"index.html": indexHTML, "app.js": appJS} {
		if hits := reRarity.FindAllString(src, -1); len(hits) != 0 {
			t.Errorf("%s must not carry a literal rarity class token, found %v", name, hits)
		}
	}
}

// The silent-fallback ban (decisions.md D5) applied to the telemetry path specifically: coercing a
// missing DTO value to zero or to an empty string converts an unknown into a confident false
// statement, which is the precise defect this whole feature exists to remove.
func TestTelemetryPanel_NoSilentFallbacks(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	body, ok := vmObjectBody(appJS, "TelemetryViewModel")
	if !ok {
		t.Fatal("app.js: TelemetryViewModel is not declared")
	}
	scopes := []string{body, funcBody(appJS, fnBanner), funcBody(appJS, fnSteps)}
	for _, sc := range scopes {
		if hits := reZeroFallback.FindAllString(sc, -1); len(hits) != 0 {
			t.Errorf("telemetry path must not coerce an unknown to zero, found %v", hits)
		}
	}
}

// ---------------------------------------------------------------------------
// Self-negatives — each proves its checker matches the FIXED form and rejects the OLD one, so a
// green run cannot be vacuous. Templates: nav_test.go:108-168, agentdetail_test.go:100-119.
// ---------------------------------------------------------------------------

func TestTelemetryPanelSelfNegative_ViewRegistration(t *testing.T) {
	oldNav := `<a id="nav-settings" href="#settings" data-route="settings">Settings</a>`
	newNav := `<a id="nav-telemetry" href="#telemetry" data-route="telemetry">Telemetry</a>`
	if reNavTelemetryDataRoute.MatchString(oldNav) {
		t.Error("reNavTelemetryDataRoute false-positive on the OLD nav block (no telemetry anchor)")
	}
	if !reNavTelemetryDataRoute.MatchString(newNav) {
		t.Error("reNavTelemetryDataRoute failed to match the FIXED nav anchor")
	}
	if !reNavTelemetryDataRoute.MatchString(`<a data-route="telemetry" id="nav-telemetry">Telemetry</a>`) {
		t.Error("reNavTelemetryDataRoute must match regardless of id/data-route attribute order")
	}

	if reViewTelemetryHidden.MatchString(`<section id="view-telemetry">`) {
		t.Error("reViewTelemetryHidden false-positive on a section shipped WITHOUT hidden")
	}
	if !reViewTelemetryHidden.MatchString(`<section id="view-telemetry" hidden>`) {
		t.Error("reViewTelemetryHidden failed to match the FIXED hidden section")
	}

	// Placement: a branch ABOVE syncNav(route) must be rejected. nav_test.go cannot catch this —
	// its range terminates on the earlier syncNav('floor') — so this self-negative is the only
	// thing standing between the correct placement and a tab whose highlight never moves.
	badPlacement := `navigate: function (route) {
      if (route === 'telemetry') { TelemetryViewModel.activate(); return; }
      this.currentRoute = route;
      syncNav(route);
    }`
	goodPlacement := `navigate: function (route) {
      this.currentRoute = route;
      syncNav(route);
      if (route === 'telemetry') { TelemetryViewModel.activate(); return; }
      this.goHome();
    }`
	if reNavigateTelemetryBranch.MatchString(badPlacement) {
		t.Error("reNavigateTelemetryBranch false-positive on a branch placed ABOVE syncNav(route) — that ships a tab whose highlight never moves")
	}
	if !reNavigateTelemetryBranch.MatchString(goodPlacement) {
		t.Error("reNavigateTelemetryBranch failed to match the correct placement below syncNav(route)")
	}
}

func TestTelemetryPanelSelfNegative_TimerScoping(t *testing.T) {
	// The anchored extraction is BOUNDED — this is the property the awk range lacked.
	fixture := `  var TelemetryViewModel = {
    data: null,
    refresh: function () { return API.get('/api/telemetry'); }
  };

  function boot() {
    setInterval(tickStale, 1000);
    window.setTimeout(function () {}, 1400);
  }`
	body, ok := vmObjectBody(fixture, "TelemetryViewModel")
	if !ok {
		t.Fatal("vmObjectBody failed to find the simulated TelemetryViewModel")
	}
	if strings.Contains(body, "boot()") || reTimerCall.MatchString(body) {
		t.Errorf("vmObjectBody over-ran the literal into boot() — the extraction is not bounded; got:\n%s", body)
	}

	planted := `  var TelemetryViewModel = {
    refresh: function () { setInterval(this.poll, 5000); }
  };`
	pbody, ok := vmObjectBody(planted, "TelemetryViewModel")
	if !ok {
		t.Fatal("vmObjectBody failed on the planted fixture")
	}
	if !reTimerCall.MatchString(pbody) {
		t.Error("reTimerCall failed to catch a timer planted inside the view-model — the check would be vacuous")
	}
}

func TestTelemetryPanelSelfNegative_StateDistinctness(t *testing.T) {
	// A collapsed renderer: one message for every degraded state. It must read RED.
	collapsed := `function renderTelemetryBanner(status) {
    var host = byId('tel-banner');
    host.innerHTML = '';
    host.appendChild(el('p', '', 'Telemetry is unavailable.'));
  }`
	if len(checkTelemetryStates(collapsed, telemetryStateCatalogue())) == 0 {
		t.Error("checkTelemetryStates passed a COLLAPSED renderer that emits one message for every state — the checker is vacuous")
	}

	// A renderer missing only the banner function entirely.
	if len(checkTelemetryStates(`function somethingElse() {}`, telemetryStateCatalogue())) == 0 {
		t.Error("checkTelemetryStates passed a source with no banner renderer at all")
	}

	// Containment: leaf A's copy is a substring of leaf B's, so one render satisfies both presence
	// checks. This is the vacuity hole that pairwise INEQUALITY would miss entirely.
	contained := []telemetryState{
		{id: "a", detect: []string{"x"}, copy: "backend unreachable", next: "n"},
		{id: "b", detect: []string{"x"}, copy: "backend unreachable — credential rejected", next: "n"},
	}
	src := `function renderTelemetryBanner() { x; 'backend unreachable — credential rejected'; 'n'; installed; recording; probed; }`
	vs := checkTelemetryStates(src, contained)
	found := false
	for _, v := range vs {
		if strings.Contains(v, "contains state") {
			found = true
		}
	}
	if !found {
		t.Errorf("checkTelemetryStates failed to flag CONTAINMENT between two states' copy — got %v", vs)
	}

	// Axis order: recording emitted before installed must read RED (T-5 fixes the order).
	misordered := `function renderTelemetryBanner() { if (recording.enabled === false) {} if (degradedInstalled) {} if (backend.probed === true) {} }`
	ordered := false
	for _, v := range checkTelemetryStates(misordered, nil) {
		if strings.Contains(v, "fixed order") {
			ordered = true
		}
	}
	if !ordered {
		t.Error("checkTelemetryStates failed to flag a misordered axis emission — the stack order is normative (T-5)")
	}
}

// The probe-verdict checker replaces two source-literal rows that would have MANDATED hardcoding a
// root-owned sentence. These fixtures prove the replacement is stricter, not looser: it rejects the
// hardcoding the old rows would have required, and it rejects a renderer that drops the payload's
// sentence entirely.
func TestTelemetryPanelSelfNegative_ProbeVerdicts(t *testing.T) {
	hardcoded := `{
    if (sig.status === 404) { line('token usage: reachable but the address is not served (HTTP 404) — data sent here is discarded'); }
  }`
	sawHardcode := false
	for _, v := range checkProbeVerdicts(hardcoded) {
		if strings.Contains(v, "hardcodes the probe sentence") {
			sawHardcode = true
		}
	}
	if !sawHardcode {
		t.Error("checkProbeVerdicts failed to reject a renderer that hardcodes the root's probe sentence — that copy would drift from ProbeResult.Summary() unnoticed")
	}

	dropped := `{ if (sig.status === 401) { line('something went wrong'); } }`
	sawDropped := false
	for _, v := range checkProbeVerdicts(dropped) {
		if strings.Contains(v, "render signal.summary verbatim") {
			sawDropped = true
		}
	}
	if !sawDropped {
		t.Error("checkProbeVerdicts failed to reject a renderer that discards the payload's own verdict sentence")
	}

	good := `{
    if (sig.status === 401 || sig.status === 403) { next = 'Check .agentfactory/secrets/telemetry.root, or re-run the quickstart credential step.'; }
    else if (sig.status === 404) { next = 'Check that the configured endpoint ends in /api/default.'; }
    else if (sig.status === 0) { next = 'Next: start a login shell (bash -l).'; }
    telLine(host, sig.summary, next);
  }`
	if v := checkProbeVerdicts(good); len(v) != 0 {
		t.Errorf("checkProbeVerdicts must accept a renderer that shows signal.summary verbatim with per-class next steps; got %v", v)
	}
}

func TestTelemetryPanelSelfNegative_JoinKey(t *testing.T) {
	// The broken implementation the verified join problem predicts: a step TITLE compared to a step
	// ID, with a zero fallback. It must produce several violations.
	oldRenderer := `function renderTelemetrySteps(rep, use) {
    rep.rows.forEach(function (r) {
      var u = pick(use.tokens.rows, function (x) { return x.step_bucket === r.step; });
      host.appendChild(el('td', '', String((u && u.total_tokens) || 0)));
    });
  }`
	oldV := checkJoinlessRenderer(funcBody(oldRenderer, fnSteps))
	if len(oldV) < 3 {
		t.Errorf("checkJoinlessRenderer must reject the naive step-title/step-id join with a zero fallback; got %d violations: %v", len(oldV), oldV)
	}
	sawNaive := false
	for _, v := range oldV {
		if strings.Contains(v, "step TITLE against a step ID") {
			sawNaive = true
		}
	}
	if !sawNaive {
		t.Errorf("checkJoinlessRenderer did not identify the naive join specifically; got %v", oldV)
	}

	// The correct implementation: a run-level join, an explicit unknown, the unattributed note, and a
	// dark-state arm ahead of the joinless one so a payload that measured nothing never reports a
	// measured negative.
	newRenderer := `function renderTelemetrySteps(rep, use) {
    rep.rows.forEach(function (r) {
      var usageKnown = use !== null;
      var tok = (use && use.tokens) || {};
      var runMatch = pick(use.tokens.rows, function (x) {
        return x.formula_run !== '' && x.formula_run === r.instance_id && x.agent === r.agent;
      });
      if (usageKnown && tok.state !== 'ok') {
        host.appendChild(el('td', 'tel-unknown', telUsageStateText(tok.state, use.cause || tok.detail)));
        return;
      }
      if (runMatch.length === 0) {
        host.appendChild(el('td', 'tel-unknown', 'no token data arrived for this step'));
        host.appendChild(el('p', 'tel-note', 'token data for this run may exist unattributed'));
        return;
      }
      host.appendChild(el('td', '', String(runMatch[0].total_tokens)));
    });
  }`
	if v := checkJoinlessRenderer(funcBody(newRenderer, fnSteps)); len(v) != 0 {
		t.Errorf("checkJoinlessRenderer must accept the run-level join renderer; got %v", v)
	}

	// The shape that SHIPPED, and that this checker used to accept: a correct run-level join, the
	// right copy, the right polarity, the in-flight guard — and no reading of the payload's own
	// state anywhere. Every literal the older legs look for is present, which is precisely why the
	// defect passed review and CI. Without this literal the new legs have no proof they can fail.
	transportOnlyRenderer := `function renderTelemetrySteps(rep, use) {
    var usageKnown = use !== null && useFail === '';
    var tokenRows = usageKnown && use.tokens ? (use.tokens.rows || []) : [];
    rep.rows.forEach(function (r) {
      var runMatch = pick(tokenRows, function (x) {
        return x.formula_run !== '' && x.formula_run === r.instance_id && x.agent === r.agent;
      });
      if (!usageKnown) {
        host.appendChild(el('td', 'tel-pending', 'token usage still loading'));
      } else if (runMatch.length === 0) {
        host.appendChild(el('td', 'tel-unknown', 'no token data arrived for this step'));
        host.appendChild(el('p', 'tel-note', 'token data for this run may exist unattributed'));
      }
    });
  }`
	transportV := checkJoinlessRenderer(funcBody(transportOnlyRenderer, fnSteps))
	if len(transportV) == 0 {
		t.Error("checkJoinlessRenderer accepted a renderer that decides the token verdict from TRANSPORT alone — this is the shape that shipped, and accepting it is what let every dark payload report 'no token data arrived for this step' when nothing had been queried at all")
	}
	sawStateBlind := false
	for _, v := range transportV {
		if strings.Contains(v, "usageKnown is a transport predicate") {
			sawStateBlind = true
		}
	}
	if !sawStateBlind {
		t.Errorf("checkJoinlessRenderer rejected the transport-only renderer, but not for being state-blind — the diagnostic must name the defect or the next reader will fix the wrong thing; got %v", transportV)
	}

	// An empty body must not pass silently.
	if len(checkJoinlessRenderer("")) == 0 {
		t.Error("checkJoinlessRenderer passed an absent renderer")
	}
}

// ---------------------------------------------------------------------------
// fable-implement Step 5 (contributing gap #7): correlated-failure dedup.
// Phase 5 (RED): app.js has NOT been modified yet — checkStepsDarkStateOnceNotN
// and checkBannerCollapse both fail against the real, unmodified renderer bodies,
// for the predicted reason (the once/collapse shape does not exist yet).
// ---------------------------------------------------------------------------

// reBackendSignalsForEach anchors the per-signal loop checkBannerCollapse protects
// — it must remain reachable for the MIXED-status case (DO-NOT-CHANGE).
var reBackendSignalsForEach = regexp.MustCompile(`\(backend\.signals\s*\|\|\s*\[\]\)\.forEach`)

// checkStepsDarkStateOnceNotN pins the "once, not N times" shape Step 5a's rowSpan
// change requires. checkJoinlessRenderer already pins that the dark-state sentence
// is rendered THROUGH telUsageStateText (presence) — that check is satisfied
// identically by today's per-row shape and the target once-per-table shape, so it
// provides zero protection against a regression to (or failure to leave) the old
// shape. This checker distinguishes them the way checkJoinlessRenderer's own
// ordering leg (iDark vs iJoinless) distinguishes correct from inverted branch
// order: by textual POSITION relative to the loop, not by presence alone.
func checkStepsDarkStateOnceNotN(body string) []string {
	var v []string
	if body == "" {
		return []string{fmt.Sprintf("app.js: no function %s — cannot check the dark-state stamp shape", fnSteps)}
	}
	body = stripJSComments(body)

	loopIdx := strings.Index(body, "rows.forEach(")
	if loopIdx < 0 {
		return []string{"the steps renderer no longer iterates rows via rows.forEach(...); this checker cannot bind to the per-row loop"}
	}
	darkIdx := strings.Index(body, fnUsageStateText+"(tok.state")
	if darkIdx < 0 {
		return []string{fmt.Sprintf("must render the dark-state sentence via %s(tok.state, ...) somewhere in the steps renderer", fnUsageStateText)}
	}
	if darkIdx > loopIdx {
		v = append(v, fmt.Sprintf("%s(tok.state, ...) is called AFTER rows.forEach begins — it fires once PER ROW instead of once for the whole table; the decision (and its single DOM append) must be made once, before or around the loop, never inside it", fnUsageStateText))
	}
	if !strings.Contains(body, "rowSpan") {
		v = append(v, "the dark-state 'Token data' cell must use rowSpan to span every row with a single stamp — no `rowSpan` token found in the steps renderer")
	}
	return v
}

// reBannerConstantOperand catches a boolean literal used as an operand of && or ||.
// `length > 0 &&` is a comparison and does not match; `false &&` does.
//
// It is applied to the collapse predicate's own statement, with comparisons against boolean
// literals removed first (reBannerBoolComparison). Both narrowings are deliberate: a bare
// whole-body scan fires on a perfectly ordinary `backend.probed === true && …` elsewhere in
// the renderer, and reports it as a broken collapse predicate — a test failing far from the
// edit with a message pointing somewhere else, which is the exact pathology thread T5 exists
// to document.
var reBannerConstantOperand = regexp.MustCompile(`\b(?:true|false)\s*(?:&&|\|\|)|(?:&&|\|\|)\s*(?:true|false)\b`)

// reBannerBoolComparison matches `=== true`, `!== false`, `== true`, … — a comparison whose
// right-hand side is a boolean literal, which is not a constant operand.
var reBannerBoolComparison = regexp.MustCompile(`[=!]==?\s*(?:true|false)\b`)

// bannerCollapsePredicate returns the statement that assigns the collapse predicate, scoping
// the constant-operand check to it rather than to the whole renderer.
//
// It anchors on the `.every(` whose callback tests the transport status — not merely the
// first `.every(` in the body — so an unrelated `.every(` added earlier cannot unbind the
// check and let a constant operand back in. The statement's end is found with quote- and
// brace-awareness: the `;` inside the callback body (`return sig.status === 0;`) is NOT the
// end of the statement, and stopping there would leave any trailing operand unscanned.
func bannerCollapsePredicate(body string) string {
	at := -1
	for _, idx := range everyCallOffsets(body) {
		if arg := callArgAt(body, idx); strings.Contains(arg, "status ===") {
			at = idx
			break
		}
	}
	if at < 0 {
		return ""
	}
	start := strings.LastIndex(body[:at], ";")
	if start < 0 {
		start = 0
	} else {
		start++
	}
	depth, inString := 0, byte(0)
	for i := at; i < len(body); i++ {
		ch := body[i]
		if inString != 0 {
			if ch == '\\' {
				i++
			} else if ch == inString {
				inString = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			inString = ch
		case '{', '(':
			depth++
		case '}', ')':
			depth--
		case ';':
			if depth <= 0 {
				return body[start : i+1]
			}
		}
	}
	return body[start:]
}

// everyCallOffsets lists the byte offsets of each `.every(` in body.
func everyCallOffsets(body string) []int {
	var out []int
	for i := 0; ; {
		j := strings.Index(body[i:], ".every(")
		if j < 0 {
			return out
		}
		out = append(out, i+j)
		i += j + len(".every(")
	}
}

// callArgAt returns the parenthesised argument text of the call beginning at off.
func callArgAt(body string, off int) string {
	open := strings.Index(body[off:], "(")
	if open < 0 {
		return ""
	}
	start := off + open + 1
	depth := 1
	for i := start; i < len(body); i++ {
		switch body[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return body[start:i]
			}
		}
	}
	return ""
}

// checkBannerCollapse pins Step 5b's all-refused collapse: exactly one line, carrying every
// signal's own cause, when every backend signal reports status === 0 — while the per-signal
// path stays unconditionally reachable for any MIXED-status combination (DO-NOT-CHANGE —
// the dedup applies ONLY to the all-refused case).
//
// The constant is checked INSIDE the .every( callback, not anywhere in the body. That
// distinction is the whole protection: `status === 0` appears twice in this renderer — once
// in the collapse predicate and once in the per-signal transport branch — so a whole-body
// containment test is satisfied by the arm the collapse REPLACES. Two mutations were
// reproduced walking straight through the containment version with the suite green
// (PR #585, S3): `var allRefused = false && …`, and the predicate's constant changed to 999.
// `.some(` vs `.every(` was caught, so the shape was pinned and the value was not.
func checkBannerCollapse(body string) []string {
	var v []string
	if body == "" {
		return []string{fmt.Sprintf("app.js: no function %s — cannot check the banner collapse shape", fnBanner)}
	}
	body = stripJSComments(body)

	if !strings.Contains(body, ".every(") {
		v = append(v, "no `.every(` predicate found — the banner must test whether ALL backend signals report status === 0 before it can collapse them into one line instead of iterating per-signal (issue #584: connection refused on all three probes)")
	}

	// The predicate's own constant, read out of the .every( callback's source.
	pinned := false
	for _, arg := range callArgs(body, ".every") {
		if strings.Contains(arg, "status === 0") {
			pinned = true
			break
		}
	}
	if !pinned {
		v = append(v, "the collapse predicate does not test `status === 0` inside its own `.every(` callback — status === 0 is the value probeOne leaves when the request never completed (probe.go:145-148), and any other constant makes the collapsed line unreachable while the literal survives in the per-signal branch, satisfying a whole-body scan")
	}

	// A constant operand short-circuits the predicate while every literal below survives as
	// dead code — the assignment-shaped sibling of the constant-true guard checked above.
	// Scoped to the predicate's own statement, and comparisons against booleans are removed
	// first, so only a genuine constant operand is reported.
	predicate := reBannerBoolComparison.ReplaceAllString(bannerCollapsePredicate(body), "")
	if loc := reBannerConstantOperand.FindString(predicate); loc != "" {
		v = append(v, fmt.Sprintf("the collapse predicate contains the constant boolean operand %q — a predicate that can never be true renders the per-signal lines forever while the collapsed branch stays in the file satisfying every source scan", strings.TrimSpace(loc)))
	}

	if !reBackendSignalsForEach.MatchString(body) {
		v = append(v, "the per-signal (backend.signals || []).forEach(...) path must remain present and unconditionally reachable — it is the only path for MIXED-status failures, which the collapse must never swallow")
	}
	return v
}

func TestTelemetryPanel_StepsDarkStateStampedOnceViaRowSpan(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	for _, v := range checkStepsDarkStateOnceNotN(funcBody(appJS, fnSteps)) {
		t.Error(v)
	}
}

// TestTelemetryPanelSelfNegative_StepsRowSpanOnce proves checkStepsDarkStateOnceNotN
// actually distinguishes the two shapes, not merely accepts everything.
func TestTelemetryPanelSelfNegative_StepsRowSpanOnce(t *testing.T) {
	perRow := `function renderTelemetrySteps(vm) {
    rows.forEach(function (r) {
      if (!usageUsable) {
        tr.appendChild(el('td', 'tel-unknown', telUsageStateText(tok.state, vm.usage.cause || tok.detail)));
      }
    });
  }`
	v := checkStepsDarkStateOnceNotN(funcBody(perRow, fnSteps))
	if len(v) == 0 {
		t.Error("checkStepsDarkStateOnceNotN accepted a renderer that calls telUsageStateText(tok.state INSIDE rows.forEach — this is the exact per-row shape Step 5a must replace")
	}

	onceWithSpan := `function renderTelemetrySteps(vm) {
    var dark = !usageUsable;
    var sentence = dark ? telUsageStateText(tok.state, vm.usage.cause || tok.detail) : '';
    rows.forEach(function (r, i) {
      if (dark) {
        if (i === 0) { var cell = el('td', 'tel-unknown', sentence); cell.rowSpan = rows.length; tr.appendChild(cell); }
      }
    });
  }`
	if v := checkStepsDarkStateOnceNotN(funcBody(onceWithSpan, fnSteps)); len(v) != 0 {
		t.Errorf("checkStepsDarkStateOnceNotN rejected a correct once-before-loop + rowSpan shape: %v", v)
	}

	if v := checkStepsDarkStateOnceNotN(""); len(v) == 0 {
		t.Error("checkStepsDarkStateOnceNotN passed an absent renderer")
	}
}

func TestTelemetryPanel_BannerCollapsesAllThreeStatusZero(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	for _, v := range checkBannerCollapse(funcBody(appJS, fnBanner)) {
		t.Error(v)
	}
}

// TestTelemetryPanelSelfNegative_BannerCollapse proves checkBannerCollapse
// distinguishes today's always-N-lines shape from a correct collapsed one.
func TestTelemetryPanelSelfNegative_BannerCollapse(t *testing.T) {
	current := `function renderTelemetryBanner(vm) {
    if (backend.probed === true) {
      (backend.signals || []).forEach(function (sig) {
        if (sig.ok) { return; }
        telLine(host, sig.summary, next);
      });
    }
  }`
	v := checkBannerCollapse(funcBody(current, fnBanner))
	if len(v) == 0 {
		t.Error("checkBannerCollapse accepted the current always-per-signal shape, which has no .every( collapse at all")
	}

	fixed := `function renderTelemetryBanner(vm) {
    if (backend.probed === true) {
      var allZero = (backend.signals || []).length === 3 && (backend.signals || []).every(function (s) { return s.status === 0; });
      if (allZero) {
        telLine(host, 'backend unreachable — all three signals', 'Next: start a login shell, or wait for the next af up/watchdog tick.');
      } else {
        (backend.signals || []).forEach(function (sig) {
          if (sig.ok) { return; }
          telLine(host, sig.summary, next);
        });
      }
    }
  }`
	if v := checkBannerCollapse(funcBody(fixed, fnBanner)); len(v) != 0 {
		t.Errorf("checkBannerCollapse rejected a correct collapsed shape: %v", v)
	}

	if v := checkBannerCollapse(""); len(v) == 0 {
		t.Error("checkBannerCollapse passed an absent renderer")
	}
}

// TestTelemetryPanel_BannerMixedStatusStillPerSignal is the negative half of the
// DO-NOT-CHANGE protection: an OVER-EAGER implementation that collapses on 1-of-3
// or 2-of-3 failures too must be rejected, not merely one that collapses correctly
// on 3-of-3. checkBannerCollapse's structural checks (above) cannot by themselves
// tell "collapses only on all-three" from "collapses too eagerly" — both contain
// .every(, status === 0, and the forEach fallback — so this is recorded as a
// documented LIMIT of the structural checker rather than papered over: closing it
// requires either an execution-level check (which this DOM-runtime-less module
// cannot do) or a stricter regex demanding the exact `sigs.length === 3` guard,
// which Phase 6 is not yet obligated to spell exactly this way. Tracked here so a
// reviewer sees the gap rather than assuming the checker already covers it.
func TestTelemetryPanel_BannerMixedStatusStillPerSignal(t *testing.T) {
	t.Skip("structural (non-DOM) checking cannot distinguish 'collapses only on all-three' " +
		"from 'collapses too eagerly' — see the doc comment on this test for the limitation " +
		"and what would close it; the forEach-survives assertion inside checkBannerCollapse is " +
		"the partial protection available at this module's level")
}

// --- DO-NOT-CHANGE protective goldens: Step 5 must not touch these three
// functions (AC7: "Tokens/Metrics panes unchanged"). Content-hash goldens rather
// than a hand-copied literal, since the bodies are large; each hash is computed
// over the comment-stripped source, matching this file's own normalization
// convention (stripJSComments) so a comment-only edit does not spuriously fail.

func TestTelemetryPanel_TokensAndMetricsPanesUnchangedByDedup(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	assertFuncBodyHash(t, appJS, "renderTelemetryTokens", "577dd11da506df99df831a061f8482c07b674bcce3b5adb4b8e5e4924cb7aafe")
	assertFuncBodyHash(t, appJS, "renderTelemetryMetrics", "0893eebf8c2764f4d834d23dc17ab8a316d1c4cac505f3fa245f5c92b2107241")
}

func TestTelemetryPanel_TelUsageStateTextUnchanged(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	assertFuncBodyHash(t, appJS, fnUsageStateText, "37006f27d1e507a533bb682ba40c9631e8277b7e3f7044e39b3779e874cb7ac0")
}

func assertFuncBodyHash(t *testing.T, src, name, wantHex string) {
	t.Helper()
	body := funcBody(src, name)
	if body == "" {
		t.Fatalf("app.js: no function %s", name)
	}
	sum := sha256.Sum256([]byte(stripJSComments(body)))
	if got := hex.EncodeToString(sum[:]); got != wantHex {
		t.Errorf("%s changed (comment-stripped sha256 = %s, want %s) — Step 5 must not touch this "+
			"function; the dedup change is scoped to renderTelemetrySteps and renderTelemetryBanner only",
			name, got, wantHex)
	}
}
