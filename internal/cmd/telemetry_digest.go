package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// learnedDigestResult is what one rebuild wrote, so an operator surface can report it without
// re-reading the files it just produced. failed is carried beside formulas because a rebuild that
// wrote nine of ten digests is a different answer from one that wrote nine, and an operator
// recovering a cache needs to be able to tell them apart.
type learnedDigestResult struct {
	formulas   int
	aggregates int
	failed     int
	malformed  int
	repaired   int
	unreadable int
}

// writeLearnedDigests rebuilds the learned-data cache in scope and writes one file per formula
// (#668 K6).
//
// Scope is the ONLY difference between the two callers: af done names the formula whose step it
// just closed, the operator's rebuild verb names none and gets every formula in the store.
// Everything else — the roster walk, the fold, the merge, the path, the atomic write — is shared,
// and has to be. A median cannot be maintained incrementally, so a hook that took a shortcut here
// would leave a file no rebuild could reproduce, and a cache that disagrees with the records behind
// it is worse than no cache. The merge is shared for a sharper reason still: a rebuild that skipped
// it would DELETE the history the records no longer hold, turning the operator's recovery verb into
// the one command that destroys what the cache exists to keep.
//
// Each formula's file is read, merged and written — B2's read-modify-write. Deriving alone would
// leave the digest's memory equal to the rotation horizon, which is the flaw that makes a pure
// derivation insufficient on its own; MergeDigests documents how the two sides reconcile.
//
// A stored file that cannot be read is treated as absent and counted, never raised. It is a cache:
// the write that follows repairs it, and refusing to proceed would let one corrupt file stop the
// factory from learning anything at all.
//
// The roster is always the WHOLE roster, never the --agent filter this command surface offers
// elsewhere. The same formula step is closed by different agents on different runs, and a digest
// built from one agent's log would silently drop every other agent's contribution to the row it
// overwrote.
//
// Formulas are written in sorted order so that a run interrupted by anything outside this loop has
// written a prefix an operator can reason about, rather than an arbitrary subset.
//
// A formula whose file cannot be written does NOT stop the ones after it. The artifact is a cache
// and owes no atomicity across formulas, so one unwritable name — say one long enough to hit
// ENAMETOOLONG, which safeDigestSegment does not screen for because it screens for traversal —
// must not cost every alphabetically later formula its digest. The failures are counted and joined
// so the caller can still report and still fail.
//
// STATED RESIDUAL — the unlocked read-modify-write race. The digest is keyed by FORMULA, so two
// agents closing steps of the same formula target the same file, and there is no mutex or lock
// anywhere in this path: fsutil.WriteFileAtomic is documented last-writer-wins, which addresses
// byte-level corruption and not lost updates. #668 accepts this as Low. What is worth recording is
// that re-deriving instead of load-modify-saving makes it SELF-HEALING: a lost update omits only
// the records appended between the loser's read and the winner's write, and the next close of that
// formula — or any af telemetry rebuild — re-derives them from the store. The mitigation is
// stronger than the atomic-write-plus-rebuildability the design assumed, and it comes free with the
// same choice that costs the close-path scan updateLearnedDigest records below.
func writeLearnedDigests(factoryRoot, formula, updatedAt string) (learnedDigestResult, error) {
	var out learnedDigestResult

	agents, err := telemetryReportAgents(factoryRoot, "")
	if err != nil {
		return out, err
	}
	dir := config.TelemetryDir(factoryRoot)
	digests, stats := telemetry.RebuildLearnedDigests(dir, agents, formula, updatedAt)
	out.malformed = stats.Malformed
	out.unreadable = stats.UnreadableAgents

	names := make([]string, 0, len(digests))
	for name := range digests {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		path := telemetry.LearnedDigestPath(dir, name)
		stored, err := tokenomics.LoadDigest(path)
		unreadable := err != nil
		if unreadable {
			stored = tokenomics.NewDigest()
		}
		merged := tokenomics.MergeDigests(stored, digests[name])
		if err := tokenomics.SaveDigest(path, merged); err != nil {
			errs = append(errs, err)
			out.failed++
			continue
		}
		out.formulas++
		out.aggregates += tokenomics.Coverage(merged)
		// Counted only once the replacement is on disk. A file that could not be read AND could not
		// be written was not replaced by anything, and reporting it as replaced would tell an
		// operator their history is gone on the one path where it is still there.
		if unreadable {
			out.repaired++
		}
	}
	return out, errors.Join(errs...)
}

// updateLearnedDigest refreshes the closing formula's learned-data cache at step close.
//
// It re-derives the closing formula's rows from the record store rather than folding this one
// record into the loaded digest, for the reason writeLearnedDigests states: the rebuild verb has to
// reproduce this file byte for byte, and only the same code over the same records can. What the
// loaded digest contributes is the history the records no longer hold.
//
// ev.TS stamps the aggregates rather than a fresh clock read, so the record and the cache it
// updated describe the same instant and this frame acquires no second time source.
//
// The failure is warned and dropped, like every other observability failure on a lifecycle verb
// (ADR-007). The digest is a CACHE: an af done that could not update it has still closed the step,
// and the next close — or af telemetry rebuild — reconstructs everything this one missed.
//
// STATED RESIDUAL — close-path cost. Re-deriving means reading and unmarshalling the whole roster's
// records on every close, and the formula scope narrows the OUTPUT, not the read. Measured at 1.98s
// for a five-agent roster holding 50MB; rotateAtBytes is 10MB per agent and ReadEvents spans two
// generations, so a large factory at steady state pays seconds per close for a cache nothing reads
// until Phase 4. The placement is the design's — heavy computation belongs at step close, off the
// session-start path, which is one of the reasons the standing digest was chosen over computing at
// read time (data.md, B2's trade-offs). What is residual is the absence of a bound. Filter.Limit is
// the only lever and it is not a free one: a bounded read yields fewer runs than the digest already
// holds, and MergeDigests would then keep the stored row and stop learning. Bounding this needs a
// merge rule that survives it, which is a policy decision this phase has no mandate to make — the
// same posture writeLearnedDigests carries the read-modify-write race with, above.
func updateLearnedDigest(factoryRoot string, ev telemetry.StepEvent) {
	// An empty formula would mean "every formula" to writeLearnedDigests, turning one step's close
	// into a full-store rewrite. It cannot happen today — runDoneCore refuses a missing
	// hooked_formula — but the sentinel is shared with the operator verb, and a scope that
	// silently widens on an absent name is the wrong direction for the caller on the hot path.
	if ev.Formula == "" {
		return
	}
	// A named formula the cache cannot file under is a different fact from an unnamed one, and the
	// engine's silence about it is right for a bulk rebuild and wrong here: this close had exactly
	// one thing to learn and learned nothing. Said out loud, because a factory that never learns
	// while reporting that it does is the failure this cache is least able to detect on its own.
	if !telemetry.SafeDigestSegment(ev.Formula) {
		fmt.Fprintf(os.Stderr, "warning: formula %q cannot name a digest file; learned nothing from this step\n", ev.Formula)
		return
	}
	if _, err := writeLearnedDigests(factoryRoot, ev.Formula, ev.TS); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not update the learned digest: %v\n", err)
	}
}

// runTelemetryRebuild reconstructs the whole learned-data cache from the raw records.
//
// The cache is derivable, which makes this verb the operator's answer to every question about it:
// delete the directory and rebuild. Deliberately NOT gated on the telemetry switch, for the reason
// runTelemetryReport already argues — records on disk stay readable after recording is switched
// off, and refusing to aggregate them would make "disable mid-formula" a loss of data rather than
// a loss of further visibility.
//
// Note what "delete and rebuild" means precisely: rebuilding IN PLACE keeps whatever the surviving
// records can no longer prove, because this shares writeLearnedDigests' merge. Deleting first is
// what makes it a from-scratch rebuild. Both are useful and they are different, so the destructive
// one stays the operator's explicit act rather than a side effect of asking for a refresh.
func runTelemetryRebuild(factoryRoot string) error {
	result, err := writeLearnedDigests(factoryRoot, "", telemetryTimestamp())
	// Reported BEFORE the error is returned. A partial rebuild is the interesting case for the
	// operator who ran this to recover a cache: an error alone says something did not land, and
	// leaves them no way to tell which formulas did.
	fmt.Printf("rebuilt %d formula digests (%d aggregates)\n", result.formulas, result.aggregates)
	if result.failed > 0 {
		fmt.Printf("could not write %d formula digests\n", result.failed)
	}
	if result.malformed > 0 {
		fmt.Printf("skipped %d unparseable record lines\n", result.malformed)
	}
	if result.unreadable > 0 {
		fmt.Printf("skipped %d unreadable agent logs\n", result.unreadable)
	}
	// Reported because the repair is silent otherwise: the file was replaced, and the history it
	// held is gone with it. That is defined-safe for a cache and still the kind of thing an
	// operator watching a factory forget what it knew deserves to be told once.
	if result.repaired > 0 {
		fmt.Printf("replaced %d unreadable formula digests\n", result.repaired)
	}
	if err != nil {
		return fmt.Errorf("rebuilding the learned digest: %w", err)
	}
	return nil
}
