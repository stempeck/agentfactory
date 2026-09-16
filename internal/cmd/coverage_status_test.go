package cmd

import (
	"fmt"
	"os"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// TestStatusCoverageSeparatesStructurallyColdFromColdStart is S2-b (thread T6): af tokenomics status
// must count only the keys a learned read can actually JOIN — those at or above the trust floor — so
// a digest full of single-run keys no read will ever hit reads as structurally cold, distinct from a
// factory that has learned nothing at all. Under B1 every close files one single-run key per
// per-instance id, so a coverage count over len(Entries) climbs while no read joins: false health.
func TestStatusCoverageSeparatesStructurallyColdFromColdStart(t *testing.T) {
	const (
		formula = "offpath"
		model   = "lmstudio"
	)

	// seedDigest writes one file whose keys carry the given run counts, through the shipped writer's
	// path helpers so the fixture cannot describe a digest the factory would never produce.
	seedDigest := func(t *testing.T, root string, runs ...int) {
		t.Helper()
		d := tokenomics.NewDigest()
		for i, r := range runs {
			d.Put(
				tokenomics.DigestKey{Formula: formula, StepID: fmt.Sprintf("s-%d", i), Model: model},
				tokenomics.Aggregate{Runs: r, MedianPeakCtxTokens: int64(40_000 + i)},
			)
		}
		dir := config.TelemetryDir(root)
		if err := os.MkdirAll(telemetry.LearnedDigestDir(dir), 0o755); err != nil {
			t.Fatalf("mkdir digest dir: %v", err)
		}
		if err := tokenomics.SaveDigest(telemetry.LearnedDigestPath(dir, formula), d); err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}
	}

	t.Run("a digest of only single-run keys is structurally cold, not rising coverage", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		armAdvisoryPolicy(t, root, 10, 2, nil) // trust floor = 2, so single-run keys join nothing
		seedDigest(t, root, 1, 1)

		count, reason := readTokenomicsCoverage(root)
		if count != 0 {
			t.Errorf("coverage = %d over a digest whose every key has Runs:1, want 0 — a read applies "+
				"the trust floor, so none of these keys can be joined and counting them reports learning "+
				"that no admission or band read will ever use", count)
		}
		if reason == "" {
			t.Error("a structurally-cold digest reported zero with no reason; a bare zero reads as " +
				"measured-and-empty")
		}
		if reason == tokenomicsCoverageUnavailable {
			t.Error("the structurally-cold reason is the SAME string as the cold-start (no-digest) " +
				"reason; the two zeros must be distinguishable — one says run more steps, the other says " +
				"the runs are not accumulating (the B1 symptom this field exists to surface)")
		}
	})

	t.Run("no digest at all is the cold-start zero, with its own reason", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		armAdvisoryPolicy(t, root, 10, 2, nil)

		count, reason := readTokenomicsCoverage(root)
		if count != 0 {
			t.Errorf("coverage = %d with no digest, want 0", count)
		}
		if reason != tokenomicsCoverageUnavailable {
			t.Errorf("cold-start reason = %q, want the no-digest constant", reason)
		}
	})

	t.Run("keys at or above the trust floor are counted", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		armAdvisoryPolicy(t, root, 10, 2, nil)
		seedDigest(t, root, 2, 3)

		count, reason := readTokenomicsCoverage(root)
		if count != 2 {
			t.Errorf("coverage = %d over two keys with Runs>=2, want 2 — join-eligible keys are real "+
				"coverage", count)
		}
		if reason != "" {
			t.Errorf("unavailable_because = %q with join-eligible coverage, want empty", reason)
		}
	})
}
