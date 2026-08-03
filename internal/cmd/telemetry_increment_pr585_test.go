package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Pinning tests for PR #585's unresolved review threads T3 (S2), T4 (S4), T5 (C1),
// T6 (N2) and the BODY-3 question.
//
// Phase 5 (RED): none of the fixes has landed, so each assertion below fails for its own
// predicted reason. The tripwire at the bottom is protective — it passes now and must keep
// passing.

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// pr585GoFunc returns the source of a Go function declaration, brace-matched from its
// signature. Mirrors extractShellFunction (quickstart_test.go:12) for Go sources.
func pr585GoFunc(src, name string) string {
	start := strings.Index(src, "func "+name+"(")
	if start < 0 {
		return ""
	}
	depth := 0
	inBody := false
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
			inBody = true
		case '}':
			depth--
			if inBody && depth == 0 {
				return src[start : i+1]
			}
		}
	}
	return src[start:]
}

// --- T3 / S2 — the step-6b interlock must not evaporate with its fixture ---------------

// TestIncrementPR585_T3_FixtureInterlockHardFailsWhenTheFixtureIsMissing pins S2's ask.
//
// Reproduced before writing this test: moving internal/telemetry/testdata/recorded-real/
// aside makes all three tests SKIP — including the non-vacuity control — and the package
// reports ok. A future fixture reorganisation would delete the interlock with CI green.
//
// The mechanism is asserted at the source level because the alternative — actually moving
// the committed fixture during a test run — would race every other test in the package
// that reads it.
func TestIncrementPR585_T3_FixtureInterlockHardFailsWhenTheFixtureIsMissing(t *testing.T) {
	src := repoFile(t, "internal/cmd/telemetry_where_test.go")

	helper := pr585GoFunc(src, "recordedRealFixtureDir")
	if helper == "" {
		t.Fatal("could not locate recordedRealFixtureDir — the shared fixture-path helper is " +
			"the single choke point all four uses funnel through")
	}
	if !strings.Contains(helper, "t.Fatal") {
		t.Errorf("recordedRealFixtureDir returns a path without proving the fixture is there:\n%s\n"+
			"the fixture is COMMITTED, so its absence is a repository defect, not a local "+
			"condition — it must hard-fail the way .github/workflows/test.yml:106 hard-fails a "+
			"missing dependency rather than skipping green (issue #458)", strings.TrimSpace(helper))
	}

	if strings.Contains(src, `t.Skip("internal/telemetry/testdata/recorded-real/`) {
		t.Error("a t.Skip on the missing recorded-real fixture survives in " +
			"telemetry_where_test.go — a skip that reads as caution is exactly how the " +
			"interlock disappears without anyone noticing")
	}
}

// --- T4 / S4 — the pre-flight's failure sentence must name its own request --------------

// TestIncrementPR585_T4_SchemaPreflightTransportFailureNamesTheRequest pins the half of S4
// that no existing test covers: PreFlightDetailNeverLeaksBackendJargon exercises only the
// missing-columns path, and PreFlightSchemaCheckFailureDegradesGracefully asserts the STATE
// but never reads the sentence the operator is shown.
func TestIncrementPR585_T4_SchemaPreflightTransportFailureNamesTheRequest(t *testing.T) {
	resetReportFlags(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // nothing is listening: the schema fetch fails by transport
	fx := newUsageFixture(t, srv.URL+"/api/default")

	dto := usageDTO(t, fx.root, "", "")

	if dto.Tokens.State != telemetryUsageStateBackendDown {
		t.Errorf("tokens state = %q, want %q — DO-NOT-CHANGE: the transport verdict must still "+
			"pass through unchanged (decisions.md D3)", dto.Tokens.State, telemetryUsageStateBackendDown)
	}
	if !strings.Contains(strings.ToLower(dto.Tokens.Detail), "schema") {
		t.Errorf("tokens detail = %q\nThe operator is shown a failure with nothing indicating it "+
			"refers to a schema pre-flight they were never told exists — not to the tokens query "+
			"the pane is about. The sentence must name its own request, the way the same "+
			"function's other two returns already do (telemetry_usage.go:611,626).", dto.Tokens.Detail)
	}
}

// TestIncrementPR585_T4_SchemaPreflightHTTPFailureNamesTheRequest is the arm the reviewer
// actually reproduced: a schema endpoint answering 404 while _search is healthy. The raw
// backend 404 reached the operator verbatim, and the query that would have worked was never
// attempted.
func TestIncrementPR585_T4_SchemaPreflightHTTPFailureNamesTheRequest(t *testing.T) {
	resetReportFlags(t)

	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, telemetrySchemaPath) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"message":"Not Found"}`))
			return
		}
		okBackend(w, r)
	})
	fx := newUsageFixture(t, endpoint)

	dto := usageDTO(t, fx.root, "", "")

	if !strings.Contains(strings.ToLower(dto.Tokens.Detail), "schema") {
		t.Errorf("tokens detail = %q\nRoot Cause B's stated harm is \"raw backend jargon on every "+
			"operator surface\"; step 3 removes it for the missing-columns case and reintroduces "+
			"it on its own failure path.", dto.Tokens.Detail)
	}
}

// TestIncrementPR585_T4_FallThroughDecisionIsStated pins the second half of S4's ask —
// "whether to fall through to queryTokens on a schema-fetch failure is a design call worth
// stating explicitly either way" — and the reviewer's question, whose honest answer is that
// it was NOT REACHED (decisions.md D10).
func TestIncrementPR585_T4_FallThroughDecisionIsStated(t *testing.T) {
	src := repoFile(t, "internal/cmd/telemetry_usage.go")

	fn := pr585GoFunc(src, "telemetryUsageDTO")
	if fn == "" {
		// The switch may live in a differently named builder; fall back to the whole file so
		// this test pins the decision's presence, never its address.
		fn = src
	}
	lowered := strings.ToLower(fn)
	if !strings.Contains(lowered, "fall through") && !strings.Contains(lowered, "fall-through") &&
		!strings.Contains(lowered, "falls through") {
		t.Error("the pre-flight's switch never states whether a schema-fetch failure should " +
			"fall through to queryTokens. Skipping the query is currently a consequence of " +
			"switch-arm order, not a recorded decision — S4 asks for the call to be stated " +
			"explicitly either way")
	}
}

// --- T5 / C1 — the deliberate tmux flag order must be documented -----------------------

// TestIncrementPR585_T5_RelaunchLaunchFlagOrderIsDocumented pins C1's ask.
//
// quickstart.sh writes `tmux new-session -s telemetry -d` while every other launch in the
// file uses `-d -s`. Normalising it (verified: bash -n stays clean, behaviour identical)
// makes telemetry_views_test.go:303's first-match strings.Index bind to this line instead of
// the real launch, and TestQuickstartSeedsTelemetryViews fails with a message about view
// seeding that points nowhere near the edit.
func TestIncrementPR585_T5_RelaunchLaunchFlagOrderIsDocumented(t *testing.T) {
	content := repoFile(t, "quickstart.sh")

	launch := strings.Index(content, "tmux new-session -s telemetry -d")
	if launch < 0 {
		t.Fatal("the relaunch script no longer writes `tmux new-session -s telemetry -d` — if " +
			"the order was normalised, TestQuickstartSeedsTelemetryViews is now anchored to the " +
			"wrong line")
	}

	// The explanation must be the comment block DIRECTLY above the line it protects — an
	// editor normalising this flag order is reading here, not seventeen lines up. Bound by
	// the block itself rather than a byte window: a window has an arbitrary edge, and a
	// test that fails because an explanation grew by one word is the same
	// failure-far-from-the-edit pathology this thread is about.
	head := content[:launch]
	lines := strings.Split(head, "\n")
	var block []string
	for k := len(lines) - 2; k >= 0 && strings.HasPrefix(strings.TrimSpace(lines[k]), "#"); k-- {
		block = append(block, lines[k])
	}
	window := strings.ToLower(strings.Join(block, "\n"))
	if !strings.Contains(window, "deliberate") && !strings.Contains(window, "do not normalise") &&
		!strings.Contains(window, "do not normalize") {
		t.Error("nothing above the relaunch launch line says its flag order is deliberate. The " +
			"next person to tidy `-s telemetry -d` into `-d -s` re-breaks " +
			"TestQuickstartSeedsTelemetryViews, and the failure message talks about view " +
			"seeding, not about their edit")
	}
}

// TestIncrementPR585_T5_TheOrderingAnchorStaysUnique is the self-protecting half: the
// comment C1 asks for must not contain the literal the ordering test binds to, or the fix
// reproduces the exact failure it documents.
func TestIncrementPR585_T5_TheOrderingAnchorStaysUnique(t *testing.T) {
	content := repoFile(t, "quickstart.sh")

	const anchor = "tmux new-session -d -s telemetry"
	if n := strings.Count(content, anchor); n != 1 {
		t.Errorf("the literal %q occurs %d times in quickstart.sh, want exactly 1 — "+
			"telemetry_views_test.go:303 takes the FIRST match as the backend launch, so a "+
			"second occurrence (even inside a comment) silently re-anchors the ordering test",
			anchor, n)
	}
}

// --- T6 / N2 — the single-flight comment must state its real scope ---------------------

// TestIncrementPR585_T6_SingleFlightScopeIsStatedHonestly pins N2's ask. The guarantee is a
// package-level atomic.Bool (watchdog.go:367), so "at most one in-flight attempt" holds
// within one process. Two `af up` runs against a factory whose watchdog is already ticking
// is the common case the claim does not cover.
func TestIncrementPR585_T6_SingleFlightScopeIsStatedHonestly(t *testing.T) {
	src := repoFile(t, "internal/cmd/watchdog.go")

	i := strings.Index(src, "func triggerTelemetryBackendGuard")
	if i < 0 {
		t.Fatal("could not locate triggerTelemetryBackendGuard")
	}
	// The doc comment is the contiguous // block immediately above the declaration.
	head := src[:i]
	lines := strings.Split(head, "\n")
	var doc []string
	for k := len(lines) - 2; k >= 0 && strings.HasPrefix(strings.TrimSpace(lines[k]), "//"); k-- {
		doc = append(doc, lines[k])
	}
	comment := strings.ToLower(strings.Join(doc, "\n"))
	if comment == "" {
		t.Fatal("triggerTelemetryBackendGuard has no doc comment to qualify")
	}

	if !strings.Contains(comment, "process") {
		t.Errorf("the single-flight comment advertises \"at most one in-flight attempt\" without "+
			"saying the guarantee is process-local:\n%s\n"+
			"telemetryBackendGuardInFlight is a package-level atomic.Bool (watchdog.go:367); a "+
			"re-run of `af up` against an already-ticking watchdog is a second process, and "+
			"serialisation there rests on relaunch.sh's `tmux has-session ||` check-then-act",
			strings.Join(doc, "\n"))
	}
}

// --- BODY-3 — the ninth early return (protective tripwire) -----------------------------

// TestIncrementPR585_BODY3_SetupTelemetryEarlyReturnCountTripwire answers the reviewer's
// third question with a mechanism rather than a promise.
//
// The proposed "no `return 0` precedes the write" scan FAILS against today's CORRECT file:
// setup_telemetry contains nine `return 0`, and the first — _port_in_use's, at the top of
// the function — is a nested helper's BOOLEAN answer, not an early-exit failure path, and it
// legitimately precedes the recovery writes. The repo already carved this exact exception
// for the seeding block (telemetry_views_test.go:270-271).
//
// So the enumerate-and-check contract stays, and this tripwire closes the gap it cannot see:
// the eight-anchor table catches a DELETED branch; this catches an ADDED one. A ninth
// early-exit path can still be introduced — it just cannot be introduced silently.
func TestIncrementPR585_BODY3_SetupTelemetryEarlyReturnCountTripwire(t *testing.T) {
	body := extractShellFunction(repoFile(t, "quickstart.sh"), "setup_telemetry")
	if body == "" {
		t.Fatal("could not extract setup_telemetry()")
	}

	// Inventory at PR head bc88aeeb: 1 boolean helper answer + 8 early-exit failure paths.
	const wantReturnZero = 9
	if got := strings.Count(body, "return 0"); got != wantReturnZero {
		t.Errorf("setup_telemetry has %d `return 0` sites, want %d.\n"+
			"If a NINTH early-exit path was added: the relaunch.sh write and the login-guard "+
			"append must precede it, and it must be added to the anchor table in "+
			"telemetry_views_test.go — that table enumerates eight, so a new branch would "+
			"otherwise ship unguarded (the install-path interlock is Root Cause A's permanent "+
			"half).\nIf a path was REMOVED: drop its anchor from the table and lower this count.",
			got, wantReturnZero)
	}
}
