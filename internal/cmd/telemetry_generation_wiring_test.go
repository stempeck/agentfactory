package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/telemetry"
)

// runLifecycleVerbsWithSession drives af sling, af prime and af done the way runLifecycleVerbs does,
// with two additions the generation figures require: a persisted session id, and a hook that runs
// between prime and done. The session id must be on disk before prime, because both the opening and
// the closing record read it from there and the derivation refuses unless the two agree.
func runLifecycleVerbsWithSession(t *testing.T, fx lifecycleFixture, sessionID string, betweenPrimeAndDone func()) {
	t.Helper()
	setSlingFlagsForTest(t, "offpath", fx.agent)

	captureStderr(t, func() {
		var out bytes.Buffer
		newCmd := func() *cobra.Command {
			c := &cobra.Command{}
			c.SetContext(t.Context())
			c.SetOut(&out)
			c.SetErr(&out)
			return c
		}

		if err := runSling(newCmd(), nil); err != nil {
			t.Fatalf("af sling: %v", err)
		}
		writeRuntimeFile(t, fx.workDir, "session_id", sessionID)
		if err := runPrime(newCmd(), nil); err != nil {
			t.Fatalf("af prime: %v", err)
		}
		if betweenPrimeAndDone != nil {
			betweenPrimeAndDone()
		}
		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
	})
}

// TestDoneWiresGenerationScalars is the interlock the function-level tests cannot be. Every rule the
// derivation follows is pinned next door — the dedup, the window, the clamp, the path — and all of
// it stays green if af done simply never calls it. Deleting the attachGenerationScalars call, or
// dropping startTS from the span it is handed, would lose the figures permanently with no test to
// say so, which is the shape of failure this feature's other interlocks exist to make impossible.
//
// It drives the verbs through their cobra entry points for the reason runLifecycleVerbs gives: the
// per-invocation clock and the gate reading are established there, so an implementation that
// derived but never wired the figures would pass a test calling the inner functions directly.
func TestDoneWiresGenerationScalars(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, t.TempDir())
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	const sessionID = "sess-wiring"
	runLifecycleVerbsWithSession(t, fx, sessionID, func() {
		// Stamped at the step_start record's own timestamp, read back rather than guessed: the
		// window's edges are two real wall-clock instants a few milliseconds apart, and the open
		// edge is the only one a test can name. [start, end) is half-open, so a record exactly at
		// the open is in.
		startTS := firstStepStart(t, fx.root, fx.agent).TS
		seedTranscript(t, fx.workDir, sessionID,
			transcriptLine(startTS, "msg_A", "text", strings.Repeat("z", 40), 1000, 100, 10, 1, 60),
			transcriptLine(startTS, "msg_A", "tool_use", "", 1000, 100, 10, 1, 60),
		)
	})

	end := lastStepEnd(t, fx.root, fx.agent)
	if end.OutTokens == nil {
		t.Fatal("step_end carries no out_tokens: af done never called the derivation")
	}
	if *end.OutTokens != 100 {
		t.Errorf("out_tokens = %d, want 100 — the message's two records must count once", *end.OutTokens)
	}
	if end.ThinkTokensEst == nil || *end.ThinkTokensEst != 90 {
		t.Errorf("think_tokens_est = %v, want 90 (100 - 40 runes / 4)", end.ThinkTokensEst)
	}
	if end.PeakCtxTokens == nil || *end.PeakCtxTokens != 1111 {
		t.Errorf("peak_ctx_tokens = %v, want 1111 (1000 + 10 + 1 + 100)", end.PeakCtxTokens)
	}

	// #678 K1: the exact leg the host reports, wired BESIDE the estimate and not over it. 60 against
	// an estimate of 90 is the ~20% over-attribution D17 documents, visible in one record — which is
	// the whole point of carrying both. A change that "improved" the estimator to agree with the
	// exact figure would silently redefine every historical think_tokens_est it is compared against.
	if end.ThinkTokens == nil {
		t.Fatal("step_end carries no think_tokens: the host reported the exact figure and af done did not wire it")
	}
	if *end.ThinkTokens != 60 {
		t.Errorf("think_tokens = %d, want 60 — the message's two records must count once, at their maximum", *end.ThinkTokens)
	}
	if end.ThinkTokensEst == nil || *end.ThinkTokensEst != 90 {
		t.Errorf("think_tokens_est = %v after the exact leg landed, want 90 — the pinned estimator "+
			"must be untouched by it", end.ThinkTokensEst)
	}

	// The diagnostic legs and the counters ride the same call. They are asserted here for the reason
	// the doc above gives: each is pinned next door, and all of it stays green if af done stops
	// wiring them.
	if end.InTokens == nil || *end.InTokens != 1000 {
		t.Errorf("in_tokens = %v, want 1000", end.InTokens)
	}
	if end.CacheReadTokens == nil || *end.CacheReadTokens != 10 {
		t.Errorf("cache_read_tokens = %v, want 10", end.CacheReadTokens)
	}
	if end.CacheCreationTokens == nil || *end.CacheCreationTokens != 1 {
		t.Errorf("cache_creation_tokens = %v, want 1", end.CacheCreationTokens)
	}
	// Zero and nil differ here, deliberately: the transcript was read, so "this step launched no
	// sub-agents" is a measurement. Only an unreadable transcript leaves these absent.
	if end.SubagentLaunches == nil || *end.SubagentLaunches != 0 {
		t.Errorf("subagent_launches = %v, want a measured 0", end.SubagentLaunches)
	}
	if end.WorkflowLaunches == nil || *end.WorkflowLaunches != 0 {
		t.Errorf("workflow_launches = %v, want a measured 0", end.WorkflowLaunches)
	}
	if end.RepeatReads == nil || *end.RepeatReads != 0 {
		t.Errorf("repeat_reads = %v, want a measured 0 — one Read of one path is not a re-read", end.RepeatReads)
	}
}

// TestDoneRecordsNoExactThinkTokensFromAnOlderHost is the absence half of the exact leg (#678 K1).
//
// A host that predates output_tokens_details reports nothing about thinking on any record. Summing
// its silence would produce a confident 0 — indistinguishable from a step that genuinely did no
// thinking — and the efficiency predicate reads exactly this field to decide whether a baseline
// exists. A wrong zero there does not degrade the decision, it inverts it.
func TestDoneRecordsNoExactThinkTokensFromAnOlderHost(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, t.TempDir())
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	const sessionID = "sess-older-host"
	runLifecycleVerbsWithSession(t, fx, sessionID, func() {
		startTS := firstStepStart(t, fx.root, fx.agent).TS
		seedTranscript(t, fx.workDir, sessionID,
			transcriptLine(startTS, "msg_A", "text", strings.Repeat("z", 40), 1000, 100, 10, 1))
	})

	end := lastStepEnd(t, fx.root, fx.agent)
	if end.OutTokens == nil {
		t.Fatal("fixture: the step was not measured at all, so its silence about thinking proves nothing")
	}
	if end.ThinkTokens != nil {
		t.Errorf("think_tokens = %d from a host that reported no thinking detail; absent and zero are "+
			"different facts and only one of them is a baseline", *end.ThinkTokens)
	}
	if end.ThinkTokensEst == nil {
		t.Error("the estimate went missing too; it is the fallback that exists precisely for this host")
	}
}

// TestDoneRecordsNoGenerationScalarsWithoutATranscript is the other half of the wiring, and the
// reason the fields are pointers. The host owns the transcript tree and may expire or move it; that
// must cost the figures and nothing else. A close that failed, or one that recorded zeroes a reader
// could not tell apart from a step that generated nothing, would both be worse than the silence.
func TestDoneRecordsNoGenerationScalarsWithoutATranscript(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, t.TempDir())
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	runLifecycleVerbsWithSession(t, fx, "sess-absent", nil)

	end := lastStepEnd(t, fx.root, fx.agent)
	if end.OutTokens != nil || end.ThinkTokensEst != nil || end.PeakCtxTokens != nil {
		t.Errorf("no transcript existed, yet the record carries figures: out=%v think=%v peak=%v",
			end.OutTokens, end.ThinkTokensEst, end.PeakCtxTokens)
	}
	// Silence is not enough: a nil trio says only that the derivation declined, and the whole point
	// of the closed reason vocabulary (#678 K1) is that a reader can tell WHICH of the three ways it
	// declined. This is the one branch no other test reaches — the other two are exercised in
	// telemetry_generation_test.go — so without this assertion the branch could regress to "" or to
	// either of its neighbours and every suite would stay green.
	if end.GenerationUnmeasuredReason != telemetry.ReasonTranscriptMissing {
		t.Errorf("generation_unmeasured_reason = %q, want %q; the transcript was never written, which "+
			"is not a session recycle and not an empty window",
			end.GenerationUnmeasuredReason, telemetry.ReasonTranscriptMissing)
	}
}
