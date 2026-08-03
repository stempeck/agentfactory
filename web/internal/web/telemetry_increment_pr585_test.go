package web

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reAffirmativeRelaunch matches a claim that something IS relaunched — "relaunches it",
// "relaunch the backend" — while leaving negated statements ("relaunches nothing", "does
// not relaunch") alone.
var reAffirmativeRelaunch = regexp.MustCompile(`relaunch(?:es)?\s+(?:it|the backend|that)`)

// Pinning tests for PR #585's unresolved review threads T1 (S1) and T2 (S3 + S1 cont'd).
//
// Phase 5 (RED): app.js and checkBannerCollapse have NOT been modified yet, so every
// assertion below fails for its own predicted reason. The protective assertions at the
// bottom pass now and must keep passing.

// bannerBody returns the comment-stripped body of the banner renderer, the same
// normalisation every checker in telemetry_test.go applies. A guarantee a comment can
// satisfy is not a guarantee.
func bannerBody(t *testing.T) string {
	t.Helper()
	body := stripJSComments(funcBody(readAsset(t, filepath.Join(staticDir, "app.js")), fnBanner))
	if body == "" {
		t.Fatalf("app.js: no function %s — nothing to pin", fnBanner)
	}
	return body
}

// statementAt returns the single statement beginning at the first occurrence of needle,
// bounded by the next ";" that is NOT inside a string literal. The quote-awareness is
// load-bearing, not defensive: both arms of the statement this is used on are operator
// prose, and a semicolon in the copy would otherwise truncate the statement mid-sentence
// and make the polarity checks below report "not a conditional" for a correct renderer.
func statementAt(t *testing.T, body, needle string) string {
	t.Helper()
	i := strings.Index(body, needle)
	if i < 0 {
		t.Fatalf("could not locate %q in %s", needle, fnBanner)
	}
	rest := body[i:]
	var quote byte
	for j := 0; j < len(rest); j++ {
		ch := rest[j]
		if quote != 0 {
			if ch == '\\' {
				j++
			} else if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case ';':
			return rest[:j+1]
		}
	}
	return rest
}

// blockAt returns the brace-delimited block that opens at or after the first occurrence of
// needle.
func blockAt(t *testing.T, body, needle string) string {
	t.Helper()
	i := strings.Index(body, needle)
	if i < 0 {
		t.Fatalf("could not locate %q in %s — the collapse arm cannot be bound", needle, fnBanner)
	}
	open := strings.Index(body[i:], "{")
	if open < 0 {
		t.Fatalf("no block opens after %q", needle)
	}
	start := i + open
	depth := 0
	for k := start; k < len(body); k++ {
		switch body[k] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[start : k+1]
			}
		}
	}
	t.Fatalf("unbalanced block after %q", needle)
	return ""
}

// --- T1 / S1 — the auto-recovery promise must be gated on the loopback predicate --------

// TestIncrementPR585_T1_RecoveryPromiseIsLoopbackGated pins S1's ask: the sentence must not
// promise a recovery ensureTelemetryBackend provably declines to attempt. The guard returns
// early for a non-loopback endpoint (internal/cmd/telemetry_backend.go:29-33), so the
// promise is true only there.
//
// Bound structurally rather than by copy: the promise's WORDING is the panel's to choose,
// but the CONDITION it renders under is not.
func TestIncrementPR585_T1_RecoveryPromiseIsLoopbackGated(t *testing.T) {
	body := bannerBody(t)

	loopbackAt := strings.Index(body, "127.0.0.1")
	promiseAt := strings.Index(body, "var backendDownNext")
	if loopbackAt < 0 {
		t.Fatal("the banner no longer computes a loopback predicate (no 127.0.0.1 literal) — " +
			"T1 requires the panel to gate on the same fact the guard gates on")
	}
	if promiseAt < 0 {
		t.Fatal("could not locate the backendDownNext assignment")
	}

	if loopbackAt > promiseAt {
		t.Errorf("the loopback predicate is computed AFTER the backend-down next step is built "+
			"(predicate at %d, promise at %d) — it cannot gate a sentence that was already "+
			"decided; hoist it above the backend axis", loopbackAt, promiseAt)
	}

	stmt := statementAt(t, body, "var backendDownNext")
	if !strings.Contains(stmt, "loopback") {
		t.Errorf("the backend-down next step is built unconditionally:\n\t%s\n"+
			"ensureTelemetryBackend relaunches nothing unless the endpoint is loopback "+
			"(telemetry_backend.go:29-33), so on a company collector this sentence tells the "+
			"operator to wait ~30s for a recovery that is never attempted", strings.TrimSpace(stmt))
	}
}

// ternaryParts splits `cond ? consequent : alternate` at the ? and : that are NOT inside a
// string literal. Both arms here are quoted sentences containing colons ("Next: af up ..."),
// so a naive split on ":" lands inside the copy and compares the wrong halves.
func ternaryParts(t *testing.T, stmt string) (cond, consequent, alternate string) {
	t.Helper()
	var quote byte
	q, c := -1, -1
	for i := 0; i < len(stmt); i++ {
		ch := stmt[i]
		if quote != 0 {
			if ch == '\\' {
				i++
			} else if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case '?':
			if q < 0 {
				q = i
			}
		case ':':
			if q >= 0 && c < 0 {
				c = i
			}
		}
	}
	if q < 0 || c < 0 {
		t.Fatalf("the backend-down next step is not a conditional — no top-level `? :` found in:\n\t%s",
			strings.TrimSpace(stmt))
	}
	return stmt[:q], stmt[q+1 : c], stmt[c+1:]
}

// TestIncrementPR585_T1_TheLoopbackArmIsTheOneThatPromises is the polarity pin, and it is the
// assertion that makes T1 mechanically enforced rather than merely present.
//
// Gating on the right fact is worth nothing if the branches are the wrong way round: inverting
// the condition to `!loopback` hands the auto-recovery promise to exactly the configuration
// that cannot receive it — the original defect, restored — while a test that only checks "the
// statement mentions loopback" stays green. This binds the promise to the loopback arm and
// forbids the claim in the other one.
func TestIncrementPR585_T1_TheLoopbackArmIsTheOneThatPromises(t *testing.T) {
	stmt := statementAt(t, bannerBody(t), "var backendDownNext")
	cond, consequent, alternate := ternaryParts(t, stmt)

	// The condition must be the bare predicate. `!loopback`, `loopback === false`, and any
	// other negation inverts which endpoint is promised a recovery.
	condExpr := strings.TrimSpace(cond)
	if eq := strings.Index(condExpr, "="); eq >= 0 {
		condExpr = strings.TrimSpace(condExpr[eq+1:])
	}
	if condExpr != "loopback" {
		t.Errorf("the backend-down conditional tests %q, want the bare predicate `loopback`.\n"+
			"A negated or compared condition swaps which endpoint is told a relaunch is coming: "+
			"the remote collector — the one ensureTelemetryBackend refuses to supervise — would "+
			"get the promise, which is the defect S1 reported.", condExpr)
	}

	if !strings.Contains(strings.ToLower(consequent), "relaunches") {
		t.Errorf("the loopback arm does not say anything is relaunched:\n\t%s\n"+
			"this is the one configuration where the guard DOES act; withholding the recovery "+
			"here is as wrong as promising it elsewhere", strings.TrimSpace(consequent))
	}
	// Only an affirmative promise is forbidden. "relaunches nothing" / "does not relaunch"
	// are honest ways to say the same thing this test exists to require, and a bare
	// substring ban would reject them.
	if reAffirmativeRelaunch.MatchString(strings.ToLower(alternate)) {
		t.Errorf("the non-loopback arm still claims a relaunch:\n\t%s\n"+
			"ensureTelemetryBackend returns at telemetry_backend.go:29-33 for this endpoint on "+
			"every af up and every watchdog tick — nothing is ever attempted",
			strings.TrimSpace(alternate))
	}
}

// --- T2 / S1 cont'd — the collapsed line must carry the payload's own cause -------------

// TestIncrementPR585_T2_CollapsedLineCarriesPayloadCause pins the diagnostic-loss half.
// probeOne never assigns res.Status on a transport error (internal/telemetry/probe.go:145-148),
// so status === 0 covers DNS failure, TLS rejection and i/o timeout as well as connection
// refused — while ProbeResult.Summary() names the true cause for each.
func TestIncrementPR585_T2_CollapsedLineCarriesPayloadCause(t *testing.T) {
	arm := blockAt(t, bannerBody(t), "if (allRefused)")

	if strings.Contains(arm, "got connection refused") {
		t.Error("the collapsed line asserts \"got connection refused\" for every status === 0 " +
			"signal, but that value also covers DNS failure, i/o timeout and TLS rejection — " +
			"the panel is naming a cause it did not measure")
	}
	if !strings.Contains(arm, ".summary") {
		t.Error("the collapsed line does not render any signal's .summary — the three " +
			"Summary() strings naming the true cause (\"no such host\", \"x509: ...\", " +
			"\"i/o timeout\") are discarded, making this path a net loss of diagnostic " +
			"information versus main; design-doc.md:148 grounds this copy on " +
			"ProbeResult.Summary() verbatim")
	}
}

// TestIncrementPR585_T2_CollapsedLineDoesNotHardcodeTheSignalCount pins the latent half:
// the sentence says "all three signals" while the predicate gates on length > 0, and Probe
// returns 1 + len(nativeSignalPaths) (probe.go:21,73). A fourth entry makes the console
// assert three over four.
func TestIncrementPR585_T2_CollapsedLineDoesNotHardcodeTheSignalCount(t *testing.T) {
	arm := blockAt(t, bannerBody(t), "if (allRefused)")

	if strings.Contains(arm, "all three signals") {
		t.Error("the collapsed line hardcodes \"all three signals\" while the predicate gates " +
			"on length > 0 — the count must come from the payload, not from the copy")
	}
	for _, label := range []string{"step timings", "token usage", "session metrics"} {
		if strings.Contains(arm, label) {
			t.Errorf("the collapsed line hardcodes the root-owned signal label %q; labels "+
				"travel in the payload (probe.go:74,81-86) and a copy of them here is the "+
				"same drift checkProbeVerdicts forbids for the sentences", label)
		}
	}
}

// --- T2 / S3 — the collapse predicate itself must be pinned ----------------------------

// The two mutations the reviewer ran, verbatim in shape. Each carries the per-signal else
// arm: a fixture written WITHOUT it is rejected by today's checker for the wrong reason
// ("no status === 0 test found"), which would make this test pass while proving nothing.

const mutationConstantFalseGuard = `function renderTelemetryBanner(vm) {
    if (backend.probed === true) {
      var allRefused = false && (backend.signals || []).length > 0
        && (backend.signals || []).every(function (sig) { return sig.status === 0; });
      if (allRefused) {
        telLine(host, 'The backend is unreachable.', backendDownNext);
      } else {
        (backend.signals || []).forEach(function (sig) {
          if (sig.ok) { return; }
          var next;
          if (sig.status === 0) { next = backendDownNext; }
          telLine(host, sig.summary, next);
        });
      }
    }
  }`

const mutationWrongTransportConstant = `function renderTelemetryBanner(vm) {
    if (backend.probed === true) {
      var allRefused = (backend.signals || []).length > 0
        && (backend.signals || []).every(function (sig) { return sig.status === 999; });
      if (allRefused) {
        telLine(host, 'The backend is unreachable.', backendDownNext);
      } else {
        (backend.signals || []).forEach(function (sig) {
          if (sig.ok) { return; }
          var next;
          if (sig.status === 0) { next = backendDownNext; }
          telLine(host, sig.summary, next);
        });
      }
    }
  }`

// TestIncrementPR585_T2_CheckerRejectsAConstantFalseCollapseGuard proves the checker
// catches the first mutation. Reproduced before writing this test: the whole web suite
// stays green under it today, because .every( survives as a dead operand and the
// containment test cannot tell a live predicate from an unreachable one.
func TestIncrementPR585_T2_CheckerRejectsAConstantFalseCollapseGuard(t *testing.T) {
	v := checkBannerCollapse(funcBody(mutationConstantFalseGuard, fnBanner))
	if len(v) == 0 {
		t.Error("checkBannerCollapse accepted `var allRefused = false && ...` — the collapse is " +
			"permanently unreachable and the console silently reverts to the three-identical-" +
			"remedies behaviour gap #7 exists to remove, with CI green")
	}
}

// TestIncrementPR585_T2_CheckerRejectsAWrongTransportConstant proves the checker catches
// the second mutation. status === 0 is the value probeOne leaves when Err != nil; any other
// constant makes the collapse dead while the literal survives elsewhere in the body.
func TestIncrementPR585_T2_CheckerRejectsAWrongTransportConstant(t *testing.T) {
	v := checkBannerCollapse(funcBody(mutationWrongTransportConstant, fnBanner))
	if len(v) == 0 {
		t.Error("checkBannerCollapse accepted a collapse predicate keyed on sig.status === 999 " +
			"— it found the `status === 0` literal in the per-signal loop instead, which is " +
			"exactly the arm the collapse replaces")
	}
}

// --- Protective: these pass today and must keep passing --------------------------------

// TestIncrementPR585_T2_CheckerStillAcceptsTheShippedShape is the non-vacuity control for
// the two tests above. A checker that rejects everything would satisfy them and ship a
// permanently red suite.
func TestIncrementPR585_T2_CheckerStillAcceptsTheShippedShape(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	if v := checkBannerCollapse(funcBody(appJS, fnBanner)); len(v) != 0 {
		t.Errorf("checkBannerCollapse rejects the shipped banner: %v", v)
	}
}

// TestIncrementPR585_T2_MixedStatusPathStaysReachable is the DO-NOT-CHANGE assertion: the
// dedup applies ONLY to the all-refused case, and the per-signal loop is the only path for
// a MIXED combination — the case the per-signal design was built for.
func TestIncrementPR585_T2_MixedStatusPathStaysReachable(t *testing.T) {
	body := bannerBody(t)
	if !reBackendSignalsForEach.MatchString(body) {
		t.Error("the per-signal (backend.signals || []).forEach path is gone — MIXED-status " +
			"failures now have no renderer, and the collapse would swallow states the probe " +
			"went to the trouble of telling apart")
	}
	if !strings.Contains(body, "sig.status === 0") {
		t.Error("the per-signal transport branch no longer keys on sig.status === 0")
	}
}
