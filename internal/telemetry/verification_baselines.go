package telemetry

// #668 K12 — the AC-1 verification baselines, registered BEFORE the run they judge.
//
// Phase 7 is a single operator-executed run on a physical backend: n≈1, and permanently
// non-CI-able. "Did the harness work" is therefore only a computable question if the numbers were
// written down first, because a threshold chosen after seeing the measurement is not a threshold.
// That ordering is the entire reason this phase precedes Phase 7.
//
// This is a NON-test file on purpose. A figure that lives only inside a _test.go is pinned but not
// REGISTERED: nothing outside the test binary can cite it, and the operator running Phase 7 has to
// go reading assertions to find out what they are being measured against.
//
// Every figure carries the .analysis/668 file and line it was re-derived at. .designs/668/scale.md
// is deliberately not a source anywhere below: scale.md:17 still says "~93%" and :18/:93 still say
// "339K", which are precisely the line-summing artifacts these numbers correct. It remains the
// reference for band SEMANTICS and for nothing numeric.

// The units a figure can be stated in. A ratio and a token count are not comparable and a reader
// joining on these figures has to be able to tell them apart without parsing the ID.
const (
	UnitTokens = "tokens"
	UnitRatio  = "ratio"
	UnitTurns  = "turns"
)

// Figure kinds. share and wall_cost are separate kinds rather than two spellings of one quantity,
// which is H-R1 stated as a type: the thinking SHARE is an authoring artifact and barely moves,
// while the thinking WALL COST falls with turn count. A harness change that halves the number of
// turns is a success that leaves the share flat, and one figure standing for both would report that
// success as no change at all — or hide a regression in the other behind it.
const (
	FigureKindWallCost = "wall_cost"
	FigureKindShare    = "share"
	FigureKindPeak     = "peak"
	FigureKindCount    = "count"
)

// VerificationCountingMethod is how every token figure below was counted. Without it the numbers
// are unreproducible: summing usage records instead of deduping them over-counts by ~2.2×, which is
// the arithmetic that produced the superseded figures this table replaces.
const VerificationCountingMethod = "one usage record per message.id, reduced with MAX per field — " +
	"not first-wins and not a sum. Claude Code writes one record per content block and stamps the " +
	"whole message's usage on every one. The shipped implementation of this method is ScanUsage " +
	"(internal/statusline/tokens.go:110-131); the per-step derivation under the same rule is " +
	"deriveGenerationScalars (internal/cmd/telemetry_generation.go:100-172)."

// VerificationDigestProcedure is how a Phase 7 run proves its artifacts are the ones it reports.
//
// It is a procedure rather than a test because the inputs are one operator's records on one host.
// What makes it checkable at all is that the learned cache is a pure function of those records:
// internal/tokenomics holds no clock, updatedAt is a parameter, and EncodeDigest sorts its keys, so
// the same records must produce the same bytes — and any difference is either a real new run or
// drift worth explaining.
const VerificationDigestProcedure = `Byte-identity procedure for a measured run:

 1. Before the run, record the baseline: sha256sum <telemetryDir>/digest/*.json
 2. Run the formula. Keep the raw records; do not delete the digest directory.
 3. Re-derive without merging history: copy the records aside, run af telemetry rebuild against
    the copy, and sha256sum its digest directory. Identical records must yield identical bytes —
    EncodeDigest sorts keys and updatedAt is passed in rather than read from a clock.
 4. A difference between (1) and (3) that the run's new records do not explain is drift, not
    learning. Diff the two digest files to see which (formula, step, model) row moved.
 5. Record identity is independent of formatting: recordDigest (internal/telemetry/store.go:96-105)
    is sha256 over the canonically marshalled StepEvent, so a reformatted log holds the same
    records and the export cursor agrees with both.`

// VerificationFigure is one pre-registered baseline.
//
// Every figure is a closed interval. A point measurement has Low == High, which is not a special
// case to write around — it is the same comparison — and it keeps a band like the thinking share
// from needing a second type.
type VerificationFigure struct {
	ID   string
	Low  float64
	High float64
	Unit string
	Kind string
	// Definition states WHAT was counted, in words. A number whose quantity is not written down
	// cannot be re-derived, and an unre-derivable baseline is an opinion with a decimal point.
	Definition string
	// Source is the analysis file and line the figure was re-derived at.
	Source string
}

// VerificationBaselines returns the registered figures in canonical order.
//
// The values are the CORRECTED, message-dedup ones. Three of them supersede figures that circulated
// widely during design and are still quoted in places: S5 output is 112,217 and not 373,416, the
// thinking share is 68.3–77.4% and not ~93%, and the peak simultaneous fan-out sum is 334,350 and
// not 339K. The superseded three all came from summing usage records line by line.
func VerificationBaselines() []VerificationFigure {
	return []VerificationFigure{
		{
			ID:   "s1_s6_out_tokens",
			Low:  186_282,
			High: 186_282,
			Unit: UnitTokens,
			Kind: FigureKindWallCost,
			Definition: "output tokens generated across steps S1–S6 of the design-v7 run, deduped " +
				"per message.id",
			Source: ".analysis/668/rootcause_concern_3.md:120",
		},
		{
			ID:   "s1_s6_think_tokens_est",
			Low:  144_219,
			High: 144_219,
			Unit: UnitTokens,
			Kind: FigureKindWallCost,
			Definition: "the thinking WALL COST across S1–S6: output tokens minus visible text " +
				"chars/4. Registered apart from the share below because it falls with turn count " +
				"while the share does not (H-R1)",
			Source: ".analysis/668/rootcause_concern_3.md:120",
		},
		{
			ID:   "s5_out_tokens",
			Low:  112_217,
			High: 112_217,
			Unit: UnitTokens,
			Kind: FigureKindWallCost,
			Definition: "output tokens for step S5 alone, the heaviest step of the run. Supersedes " +
				"373,416, which summed one message's usage once per content block",
			Source: ".analysis/668/rootcause_concern_3.md:120",
		},
		{
			ID:   "thinking_share",
			Low:  0.683,
			High: 0.774,
			Unit: UnitRatio,
			Kind: FigureKindShare,
			Definition: "thinking as a fraction of output across S1–S6, as a band: 68.3% counted " +
				"from direct visible characters, 77.4% from the chars/4 estimate. Supersedes the " +
				"~93% figure, which was a line-summing artifact",
			Source: ".analysis/668/rootcause_concern_3.md:120",
		},
		{
			ID:   "peak_simultaneous_fanout_sum",
			Low:  334_350,
			High: 334_350,
			Unit: UnitTokens,
			Kind: FigureKindPeak,
			Definition: "the summed occupancy of every session live at the same instant, at its " +
				"maximum (00:17:04). This is the figure a fan-out serialization mechanism moves, and " +
				"it supersedes scale.md's 339K",
			Source: ".analysis/668/rootcause_concern_4.md:29,65,87",
		},
		{
			ID:   "reprefill_proxy_turns",
			Low:  82,
			High: 82,
			Unit: UnitTurns,
			Kind: FigureKindCount,
			Definition: "turns whose prefill exceeded 20,000 tokens — the client-side proxy for " +
				"re-prefill cost, which is not directly observable from the client",
			Source: ".analysis/668/rootcause_concern_4.md:31,71,90",
		},
		{
			ID:   "fable5_main_session_peak",
			Low:  417_526,
			High: 417_526,
			Unit: UnitTokens,
			Kind: FigureKindPeak,
			Definition: "peak context occupancy of the fable-5 main session — the cloud-profile " +
				"comparison point, above the local profile's whole 262,144-token pool",
			Source: ".analysis/668/rootcause_concern_1.md:11,59,157",
		},
	}
}
