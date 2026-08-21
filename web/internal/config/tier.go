package config

import "sort"

// ---- #620 Phase 2 · T1: the exposure/disposition table ----
//
// One row per config document under .agentfactory/. This table is the web module's SINGLE source of
// truth for "what may the console see, what may it change, and when does a change take effect": it
// is served verbatim in the GET /api/settings payload, the raw read iterates it, both write
// allowlists derive from it, and a 400-on-unwritable carries its Reason string. There is no second
// place to add a file (design-doc.md:73, security.md:66-68).
//
// It is the web-side half of a contract whose root-side half already exists:
// internal/config/paths_disposition_test.go pins the same nine helper-backed files in af-core and
// names TestSettings_DispositionComplete — below, in tier_test.go — as its counterpart.
//
// Adding a config file? Add its paths.go helper in af-core AND a row here, in the same change.
//
// WHY THE RAW TIER IS FAIL-SAFE, AND ITS ONE RESIDUAL RISK. A file is TierRaw only when its ENTIRE
// canonical schema is structurally secret-free — verified per file against af-core, not assumed:
// dispatch.json is labels/agent names/profile NAMES/intervals (internal/config/dispatch.go:18-50);
// startup.json is agent names + gate enums + recovery integers (startup.go:14-59); messaging.json is
// groups of agent names (config.go:79-81); statusline.json is element names + a bool
// (statusline.go:31-34); factory.json is name/version/git identity (config.go:84-98). So an UNKNOWN
// key in a raw-tier file is by definition part of a schema already classified secret-free, and
// nothing leaks by default — which is exactly what lets this module stop enumerating keys.
//
// The residual risk is the inverse: a future canonical change that adds a CREDENTIAL field to a
// raw-tier schema would flow to the browser. That risk is owned here, and the review chokepoint is
// mechanical: a new canonical field changes af-core's committed congruence fixture, which turns the
// root drift test red and forces a human to look at the new key WITH THIS TABLE IN HAND. If the new
// key is a credential, the file moves to TierProjected and grows a decode target. Severity is
// bounded by ADR-006's loopback baseline (security.md:35-86).
//
// The counter-designs were considered and rejected: serving everything raw with a key-name redaction
// denylist is fail-OPEN, and regenerating complete mirrors reopens loss under version skew
// (conflicts.md:93-111). Each file gets exactly one of the two pipelines, chosen by this table.

// Tier is the closed set of exposure dispositions. TierRaw and TierProjected differ in KIND, not in
// degree: a raw file is never decoded into a web-declared type (so no key can be dropped), while a
// projected file is ONLY ever decoded into a narrow secret-free type (so no secret can be held).
// TierExcluded rows carry no path constructor at all, which is why no code path can read them.
type Tier string

const (
	// TierRaw — served as opaque bytes, JSON-syntax-checked only. Writable rows round-trip through
	// `af config <file> set`; the one non-writable raw row (factory.json) is rendered read-only.
	TierRaw Tier = "raw"
	// TierProjected — secret-bearing: served only through a decode target that HAS no field for the
	// secret, so it is structurally impossible to serialize (agents.json → AgentSummary,
	// models.json → profile names).
	TierProjected Tier = "projected"
	// TierExcluded — not read by the web module at all. The Reason records who owns the file instead.
	TierExcluded Tier = "excluded"
)

// Row is one file's disposition. File is the on-disk basename AND the `af config` subcommand noun
// AND the key in the served payload's files map — one vocabulary, so the exec wrapper builds argv
// from the same token the browser addressed (design-doc.md:105-108).
type Row struct {
	File          string
	Tier          Tier
	Writable      bool
	Reason        string
	EffectiveWhen string

	// path locates the document under the factory root. It is nil for every TierExcluded row, so
	// "the console never reads telemetry.json" is enforced by the absence of a way to name it, not
	// by an if-statement somebody could delete.
	path func(root string) string
}

// tierRows is the table. Order is the panel order the console renders and the order Read walks;
// keep the writable raw files first so the payload's files map is assembled predictably.
var tierRows = []Row{
	{
		File:          "dispatch",
		Tier:          TierRaw,
		Writable:      true,
		Reason:        "editable — routed through `af config dispatch set`, which validates every mapping against agents.json before writing",
		EffectiveWhen: "mappings and trigger_label: next dispatcher cycle; interval_seconds: dispatcher restart",
		path:          dispatchPath,
	},
	{
		File:          "startup",
		Tier:          TierRaw,
		Writable:      true,
		Reason:        "editable — routed through `af config startup set`, which cross-checks every named agent against agents.json",
		EffectiveWhen: "agents and start_dispatch: next `af up`; recovery: next watchdog tick",
		path:          startupPath,
	},
	{
		File:          "messaging",
		Tier:          TierRaw,
		Writable:      true,
		Reason:        "editable — routed through `af config messaging set`",
		EffectiveWhen: "next mail routing evaluation",
		path:          messagingPath,
	},
	{
		File:          "statusline",
		Tier:          TierRaw,
		Writable:      true,
		Reason:        "editable — routed through `af config statusline set`",
		EffectiveWhen: "next statusline render",
		path:          statuslinePath,
	},
	{
		File:     "factory",
		Tier:     TierRaw,
		Writable: false,
		// C-9, a recorded decision rather than an oversight: factory.json is the root MARKER and the
		// factory's identity. af-core ships no `af config factory set`, and inventing a console-only
		// writer would make the web module a second writer of the one file that defines what a
		// factory root IS. Shown, never edited.
		Reason:        "read-only — factory.json is the root marker and factory identity; af-core ships no `af config factory set` (recorded decision C-9)",
		EffectiveWhen: "n/a",
		path:          factoryPath,
	},
	{
		File:     "agents",
		Tier:     TierProjected,
		Writable: false,
		// Not writable because a whole-document replace and a secret-free read are jointly
		// unsatisfiable: the console cannot send back what it was never allowed to see. A
		// merge-semantics setter is the recorded alternative if per-agent editing is ever demanded.
		Reason:        "secret-free summary only — agents.json carries per-agent model/base_url/auth_token overrides, so the console sees name, type, description and formula and nothing else. Not writable: a whole-document replace cannot be built from a redacted read (recorded decision). Edit via `af install <role>` / agent-gen",
		EffectiveWhen: "next session launch",
		path:          agentsPath,
	},
	{
		File:          "models",
		Tier:          TierProjected,
		Writable:      false,
		Reason:        "profile NAMES only — every profile body is a map of env exports carrying gateway credentials (C-2). Not writable from the console in v1; edit via `af config models set`, which additionally enforces the redaction-sentinel, attestation and dispatch reverse-pin checks",
		EffectiveWhen: "next session launch",
		path:          modelsPath,
	},
	{
		File:     "telemetry",
		Tier:     TierExcluded,
		Writable: false,
		// The absence of a CLI writer is the load-bearing half of this reason: an operator who reads
		// "not writable here" would otherwise go looking for `af telemetry on` and be surprised that
		// it writes the .telemetry-gate FILE, not this document.
		Reason:        "not read — telemetry.json's headers are either literal credentials or references to secret files. No console write, and no CLI writer exists either: `af telemetry on|off` writes the .telemetry-gate file, not telemetry.json",
		EffectiveWhen: "next telemetry emission",
	},
	{
		File:          "build-host",
		Tier:          TierExcluded,
		Writable:      false,
		Reason:        "not read — machine-local build configuration, unmanaged by the console; edit via `af config build-host`",
		EffectiveWhen: "build time",
	},
	{
		File:          "litellm.yaml",
		Tier:          TierExcluded,
		Writable:      false,
		Reason:        "not read — secret-bearing YAML owned by the gateway, not by af; there is no af seam for it",
		EffectiveWhen: "gateway-defined",
	},
	{
		File:          ".agentfactory/secrets/",
		Tier:          TierExcluded,
		Writable:      false,
		Reason:        "never read, listed, or named by the console (C-1)",
		EffectiveWhen: "n/a",
	},
}

// tierRowsWithoutPathHelper records the two rows af-core's paths.go cannot discover, mirroring
// internal/config/paths_disposition_test.go:37-41. Neither litellm.yaml nor the secrets directory
// has a paths.go helper, so no path-derived enumeration can find them and TestSettings_Disposition-
// Complete must assert CONTAINMENT of the helper set rather than equality with it. Recorded here so
// a future reader does not "fix" the apparent asymmetry — and asserted, so it cannot be quietly
// deleted as unused.
var tierRowsWithoutPathHelper = []string{"litellm.yaml", ".agentfactory/secrets/"}

// rowFor returns the disposition row for a file noun.
func rowFor(file string) (Row, bool) {
	for _, r := range tierRows {
		if r.File == file {
			return r, true
		}
	}
	return Row{}, false
}

// WritableFiles is the write allowlist, DERIVED from the table rather than re-typed as a literal.
// web/internal/exec keeps its own copy of the same truth (exec cannot import this package — config
// imports exec, so the dependency only runs one way), and TestSettings_AllowlistsEqual pins the two
// copies and this table to identical verdicts. Deriving is what makes that pin meaningful instead of
// decorative: growing the table grows both allowlists, and growing only one is a test failure.
func WritableFiles() []string {
	out := make([]string, 0, len(tierRows))
	for _, r := range tierRows {
		if r.Writable {
			out = append(out, r.File)
		}
	}
	sort.Strings(out)
	return out
}
