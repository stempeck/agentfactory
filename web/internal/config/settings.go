// Package config is the C5 curated settings surface for the web module.
//
// It serves two operator needs without ever importing af-core's internal/config (Go's internal
// seal + the separate go.mod make that compiler-impossible — the point of the decoupling, AC#4):
//
//   - READ (GET /api/settings): every config document, served through EXACTLY ONE of two pipelines
//     chosen by the T1 disposition table in tier.go. Raw-tier files (dispatch, startup, messaging,
//     statusline, factory) are returned as OPAQUE BYTES — JSON-syntax-checked, never decoded into a
//     type this module declares. Secret-bearing files are returned only through a decode target that
//     has no field for the secret: agents.json → AgentSummary, models.json → profile names. The rest
//     are not read at all.
//
//   - WRITE (PUT /api/settings/{file}): the edited config is routed, as raw JSON on stdin, through
//     `af config <file> set` (the AfConfig seam). af-core is the SINGLE canonical validator/writer —
//     struct validation + cross-file checks (every referenced agent ∈ agents.json) + atomic
//     temp+rename — so a mapping to a non-existent agent is rejected WITHOUT corrupting the file
//     (#225/#231). The web module does NOT re-declare the config schema nor re-implement validation.
//
// WHY THE READ IS RAW (#620 Phase 2, the frame-lift). This package used to re-declare af-core's
// DispatchConfig/StartupConfig/FactoryConfig as hand-copied mirror structs. encoding/json silently
// drops any on-disk key a mirror does not declare, and the console PUTs back what it read through a
// WHOLE-DOCUMENT setter — so every canonical key the mirror lacked was ERASED from the operator's
// disk on the next save. It fired twice (`improvement`, then `telemetry`), each time "fixed" by
// adding one more field, i.e. by re-arming the trap; a third drift (`workflows`, `mappings[].model`,
// the 14-key `recovery` block) was already live on disk when this was written. Enumerating keys was
// the defect. Nothing here enumerates keys any more, so no key can be dropped — and a regression
// (someone reintroducing a typed decode) fails TestSettingsRoundTrip_CanonicalSchemaCongruence
// against af-core's own machine-generated schema portrait, rather than depending on a reviewer
// noticing. The reasoning is telemetryview.relay()'s, applied to config: a field this module does
// not know about is additive evolution and must still reach the browser.
package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stempeck/agentfactory-web/internal/exec"
)

// dotDir mirrors internal/config/paths.go:10 — the factory's hidden config directory.
const dotDir = ".agentfactory"

// The path helpers are the accepted residue of the frame-lift: rather than add read verbs to af-core
// so the console could ask it for bytes, the console reads the bytes itself. That is four lines of
// path knowledge (a stable, documented layout) instead of a schema copy — knowing WHERE a file is
// cannot go stale the way knowing WHAT IS IN IT did. Each is referenced by exactly one tier row;
// factoryPath is additionally load-bearing for FindFactoryRoot in root.go.
func dispatchPath(root string) string   { return filepath.Join(root, dotDir, "dispatch.json") }
func startupPath(root string) string    { return filepath.Join(root, dotDir, "startup.json") }
func factoryPath(root string) string    { return filepath.Join(root, dotDir, "factory.json") }
func agentsPath(root string) string     { return filepath.Join(root, dotDir, "agents.json") }
func messagingPath(root string) string  { return filepath.Join(root, dotDir, "messaging.json") }
func statuslinePath(root string) string { return filepath.Join(root, dotDir, "statusline.json") }
func modelsPath(root string) string     { return filepath.Join(root, dotDir, "models.json") }

// ErrNotWritable is returned when a write targets a file whose tier row is not Writable (factory.json
// and the secret-bearing files), or a file with no tier row at all. The handler maps it to a 400 and
// surfaces the row's Reason, so the operator learns WHY rather than just "no".
var ErrNotWritable = errors.New("settings file is not writable")

// ErrHashMismatch is returned when a write carries an --if-content-hash precondition that no longer
// matches the file on disk — someone else changed it since the console read it. The handler maps it
// to a 409 (reload and re-apply), never to a 422: nothing was written, and the operator's edit is
// still valid, just based on a stale read.
var ErrHashMismatch = errors.New("settings file changed since it was read")

// ErrBadPrecondition is returned when the precondition is not a sha256 digest at all. Kept distinct
// from ErrHashMismatch so it maps to a 400: garbage in the header is a malformed request, and
// reporting it as a conflict would send the client into a reload-and-retry loop that can never
// converge, because re-reading the file does not fix a client that is sending nonsense.
var ErrBadPrecondition = errors.New("settings write precondition is not a sha256 digest")

// ErrAfTooOld is returned when the af binary on PATH predates #620 and rejects the forwarded
// --if-content-hash precondition it does not register (`unknown flag: --if-content-hash`, exit 1).
// It is NOT a validation failure of the operator's document — the document is fine, the local binary
// is too old — so the handler maps it to a 502 (an infrastructure/version fault) rather than the 422
// an unclassified non-zero exit would land on. It is deliberately NOT returned for a too-old af that
// merely lacks a subcommand this PR introduced (e.g. `messaging`): verified against the real af, that
// case prints usage and exits 0 with no error text (see afTooOldMarker below), so no error-text
// classifier can reach it.
var ErrAfTooOld = errors.New("af binary is too old for this settings write")

// ---- the served document ----

// FileView is one config file's disposition plus its document as served. Doc is nil — and marshals
// to `null` — when the file is absent from disk or when its tier does not serve a document at all;
// the client tells the two apart by the Tier and Reason it always carries. It must NEVER be set to
// an empty-but-non-nil json.RawMessage: that is a hard marshal error, not an empty document.
//
// An ABSENT file is served as absent, deliberately. The old read seeded fabricated defaults for a
// missing startup.json, so the console could not distinguish "the operator never wrote this file"
// from "the operator wrote exactly these values" — and a save then materialized defaults nobody
// chose. Absence is af-core's to interpret, not this module's.
type FileView struct {
	Doc           json.RawMessage `json:"doc"`
	Tier          string          `json:"tier"`
	Writable      bool            `json:"writable"`
	Reason        string          `json:"reason"`
	EffectiveWhen string          `json:"effective_when"`

	// Fingerprint is sha256 of the RAW ON-DISK BYTES, not of the doc as served. The two differ:
	// marshaling a json.RawMessage compacts whitespace and HTML-escapes < > &. af-core's
	// --if-content-hash precondition hashes os.ReadFile(path) (internal/cmd/config_set.go:580-582),
	// so a digest taken over the served bytes would never match and every conditional write would
	// conflict forever.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Settings is the document served by GET /api/settings. The files map is keyed by the one shared
// vocabulary: on-disk basename == `af config` subcommand noun == PUT path segment.
type Settings struct {
	Files    map[string]FileView `json:"files"`
	Agents   []AgentSummary      `json:"agents"`   // agent picker; never any secret
	Profiles []string            `json:"profiles"` // model-profile NAMES for the mapping editor's picker

	// SchemaFingerprint is the running af binary's config-schema digest, from
	// `af config fingerprint --json`. The client renders a version-skew banner from it — carried here
	// so the settings view needs one request, not two. There is NO build-time embedded-fixture hash to
	// compare it against (that ADR-008 Go-side baseline is deferred); the banner fires when this is
	// empty or when it changes between two reads in one tab (see web/static/app.js:1237-1244). Empty
	// when the fingerprint could not be obtained.
	SchemaFingerprint string `json:"schema_fingerprint"`
}

// AgentSummary is the SECRET-FREE projection of one agents.json entry, used to populate the mapping
// editor's agent picker. It deliberately OMITS Model/BaseURL/AuthToken, so those secrets are
// structurally impossible to serialize (AC#3 mechanical interlock).
type AgentSummary struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Formula     string `json:"formula,omitempty"`
}

// agentEntryRead is the secret-free decode target for each agents.json entry. The secret JSON keys
// model/base_url/auth_token have NO matching field, so encoding/json silently drops them — the
// secrets never enter the web module's memory.
type agentEntryRead struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Formula     string `json:"formula,omitempty"`
}

// agentsFileRead mirrors agents.json's top-level shape: {"agents": {name: entry}}.
type agentsFileRead struct {
	Agents map[string]agentEntryRead `json:"agents"`
}

// modelsFileRead is the names-only decode target for models.json — the same mechanical interlock as
// agentEntryRead, applied to the second secret-bearing file. A profile BODY is a map of environment
// exports (ANTHROPIC_AUTH_TOKEN, ANTHROPIC_BASE_URL, …), so the value type is the empty struct: the
// credentials have no field to land in and are discarded at decode, never held. The console needs
// only the names, to populate the dispatch mapping editor's model picker.
type modelsFileRead struct {
	Models map[string]struct{} `json:"models"`
}

// AfConfig is the af seam this package drives: the write verb it pipes complete documents to, and
// the read verb reporting the running binary's config-schema fingerprint. exec.Wrapper satisfies it;
// tests inject a fake one layer lower, at exec.Runner, so the real Wrapper's argv construction and
// allowlist stay in the path under test.
type AfConfig interface {
	ConfigSet(ctx context.Context, file string, payload []byte, ifContentHash string) (exec.Result, error)
	ConfigFingerprintJSON(ctx context.Context) (string, error)
}

// Service reads settings from disk and routes writes through the af command. root is the factory
// root (where .agentfactory/ lives); af is the exec seam. af may be nil on a pure read path, in
// which case the schema fingerprint is simply unavailable.
type Service struct {
	root string
	af   AfConfig
}

// New builds a Service over the factory root and the af seam (production: an *exec.Wrapper).
func New(root string, af AfConfig) *Service {
	return &Service{root: root, af: af}
}

// Read assembles the settings document by walking the T1 disposition table — there is no second
// list of files, so a row added to the table is served and a file with no row is not reachable.
//
// Raw-tier files are read as bytes and syntax-checked ONLY; they are never unmarshaled into a type
// this module declares, which is the whole point (see the package doc). An absent file yields a nil
// doc and its disposition, never fabricated defaults. A MALFORMED file is a hard error: serving it
// as absent would invite the client to save over a document it could not read, which is the erasure
// this phase exists to prevent. That includes a zero-byte file — json.Valid rejects it.
func (s *Service) Read(ctx context.Context) (Settings, error) {
	out := Settings{
		Files:    make(map[string]FileView, len(tierRows)),
		Agents:   []AgentSummary{},
		Profiles: []string{},
	}

	for _, row := range tierRows {
		view := FileView{
			Tier:          string(row.Tier),
			Writable:      row.Writable,
			Reason:        row.Reason,
			EffectiveWhen: row.EffectiveWhen,
		}
		switch row.Tier {
		case TierRaw:
			raw, found, err := readRawJSONFile(row.path(s.root))
			if err != nil {
				return Settings{}, fmt.Errorf("reading %s.json: %w", row.File, err)
			}
			if found {
				view.Doc = raw
				view.Fingerprint = hashHex(raw)
			}
		case TierProjected:
			// The projections populate the pickers at the top level, NOT this row's doc, which stays
			// nil: a projection is not the document, and serving it as one would invite a client to
			// PUT it back — the erasure this phase exists to prevent, in miniature.
			if err := s.project(row, &out); err != nil {
				return Settings{}, err
			}
		}
		out.Files[row.File] = view
	}

	out.SchemaFingerprint = s.schemaFingerprint(ctx)

	return out, nil
}

// project reads one secret-bearing file through its narrow, secret-free decode target. The switch is
// on the tier row rather than on a second list of filenames, so the table stays the ONLY enumeration
// of config files in this module — and the default arm means a projected row added without a decode
// target fails loudly instead of quietly serving an empty picker.
func (s *Service) project(row Row, out *Settings) error {
	switch row.File {
	case "agents":
		var af agentsFileRead
		if _, err := readJSONFile(row.path(s.root), &af); err != nil {
			return fmt.Errorf("reading agents.json: %w", err)
		}
		out.Agents = summaries(af.Agents)
	case "models":
		var mf modelsFileRead
		if _, err := readJSONFile(row.path(s.root), &mf); err != nil {
			return fmt.Errorf("reading models.json: %w", err)
		}
		out.Profiles = profileNames(mf.Models)
	default:
		return fmt.Errorf("no projection for %q: a projected row must name a decode target that has no field for the file's credentials", row.File)
	}
	return nil
}

// AgentFormula returns the DECLARED formula configured for an agent in agents.json
// (bead-free static config). It reads ONLY agents.json — it deliberately does NOT
// read the other config files (cf. Read), so the Sling form's availability is
// not coupled to unrelated config health (design-doc H2 / Option B).
//
//	found=false  -> agent not present in agents.json (caller returns 404)
//	formula==""  -> agent present but no configured formula (caller returns 422)
//	err != nil   -> agents.json decode failure (caller returns 502)
//
// A MISSING agents.json makes readJSONFile return (false, nil) — so af.Agents is empty,
// name is absent ⇒ found=false ⇒ 404 at the caller. Only a genuine decode error yields err.
// ctx is accepted for interface symmetry / future use; like Read, the body does file I/O
// without threading it today. Keep the parameter.
func (s *Service) AgentFormula(ctx context.Context, name string) (formula string, found bool, err error) {
	var af agentsFileRead
	if _, err := readJSONFile(agentsPath(s.root), &af); err != nil {
		return "", false, fmt.Errorf("reading agents.json: %w", err)
	}
	entry, ok := af.Agents[name]
	if !ok {
		return "", false, nil
	}
	return entry.Formula, true, nil
}

// Write routes the complete edited config to `af config <file> set` (raw JSON on stdin). The
// writable set is DERIVED from the tier table, so it cannot drift from what the GET payload told the
// client was editable, and the refusal carries the row's Reason rather than a hardcoded sentence
// that goes stale the moment the table changes.
//
// ifContentHash, when non-empty, is the fingerprint the console read. It is checked HERE against the
// current on-disk bytes and ALSO forwarded to af-core, deliberately twice. The LOCAL check is the
// robust one: it needs no knowledge of af-core's error text and it holds even against an older af that
// lacks the flag. The FORWARDED flag is best-effort — it keeps af-core the single authority for the
// narrow race between this check and its own write, but a pre-#620 af rejects the flag with a
// non-zero exit; that failure is classified ErrAfTooOld and surfaced as a 502 (the af binary is too
// old), NOT the 422 a real validation earns.
func (s *Service) Write(ctx context.Context, file string, payload []byte, ifContentHash string) (exec.Result, error) {
	row, ok := rowFor(file)
	switch {
	case !ok:
		return exec.Result{}, fmt.Errorf("%w: %q (no config file by that name)", ErrNotWritable, file)
	case !row.Writable:
		return exec.Result{}, fmt.Errorf("%w: %q (%s)", ErrNotWritable, file, row.Reason)
	}

	if ifContentHash != "" {
		// Shape first, then comparison: an unparseable precondition would otherwise compare unequal to
		// every real digest and be reported as a conflict.
		if !exec.IsContentHash(ifContentHash) {
			return exec.Result{}, fmt.Errorf("%w", ErrBadPrecondition)
		}
		if err := s.checkPrecondition(row, ifContentHash); err != nil {
			return exec.Result{}, err
		}
	}
	if s.af == nil {
		return exec.Result{}, fmt.Errorf("settings write seam not configured")
	}

	res, err := s.af.ConfigSet(ctx, file, payload, ifContentHash)
	if err != nil && isAfHashMismatch(err) {
		return res, fmt.Errorf("%w: %v", ErrHashMismatch, err)
	}
	if err != nil && isAfTooOld(err) {
		return res, fmt.Errorf("%w: %v", ErrAfTooOld, err)
	}
	return res, err
}

// checkPrecondition compares the caller's fingerprint against the file on disk. An absent file with
// a precondition is also a conflict: the console read SOMETHING (it had a hash), so the file
// disappearing under it is the same class of surprise as it changing, and creating it from a stale
// edit would silently resurrect deleted configuration.
func (s *Service) checkPrecondition(row Row, ifContentHash string) error {
	data, err := os.ReadFile(row.path(s.root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s.json no longer exists; re-read the settings and re-apply your edit", ErrHashMismatch, row.File)
		}
		return fmt.Errorf("reading %s.json for the write precondition: %w", row.File, err)
	}
	if cur := hashHex(data); !strings.EqualFold(cur, ifContentHash) {
		return fmt.Errorf("%w: %s.json is now %s, not the %s you read; re-read the settings, re-apply your edit, and retry",
			ErrHashMismatch, row.File, cur, ifContentHash)
	}
	return nil
}

// afHashMismatchMarker is the one sentence af-core prints when ITS --if-content-hash precondition
// fails (internal/cmd/config_set.go:585). Matching on error TEXT is the module's least favourite
// idiom, and it is confined to this one function for a reason that cannot be designed away: af exits
// 1 for every failure (internal/cmd/root.go:29-39), so the exit code cannot tell a stale-read
// conflict from a validation rejection, and this module cannot import af-core's sentinel across the
// C-2 boundary. It is DEFENCE IN DEPTH only — checkPrecondition above catches the ordinary case
// without any string knowledge, and this covers just the race window.
//
// Matching the bare flag NAME instead would be a trap: cobra prints `unknown flag:
// --if-content-hash` on an af older than #620 Phase 1, which would turn every save on an
// un-upgraded factory into a spurious conflict-and-reload loop. That case is not a conflict — it is
// an af-too-old failure, classified by isAfTooOld below and surfaced by the handler as a 502, kept
// distinct from this conflict marker.
const afHashMismatchMarker = "it changed since you read it"

func isAfHashMismatch(err error) bool {
	return strings.Contains(err.Error(), afHashMismatchMarker)
}

// afTooOldMarker is the pflag phrase a pre-#620 af prints when the console forwards the
// --if-content-hash precondition to a setter (dispatch/startup/statusline) whose binary does not
// register the flag: `unknown flag: --if-content-hash`, exit 1. VERIFIED against the real af CLI
// (Phase-7 sideways probe: `af config dispatch set --if-content-hash=…` → exactly this, exit 1). It is
// matched as a FULL phrase, never a bare "unknown", so a genuine validation rejection (`unknown agent
// "ghost"`) is NOT swallowed and still earns its 422. Same C-2 rationale as afHashMismatchMarker: af
// exits 1 for every failure, so only the text can tell "this af is too old" from "your document is
// invalid", and that text-matching is confined here.
//
// A too-old af lacking the NEW `messaging` subcommand is deliberately NOT matched: verified against
// the real af, a missing subcommand prints `af config` usage and exits 0 (no error text, no non-zero
// exit), so a messaging save silently reports success rather than failing with `unknown command`. That
// deployment-skew no-op is a distinct pre-existing defect no unresolved thread asks to fix (see
// out_of_scope.md); an error-text classifier cannot catch a case that produces no error.
const afTooOldMarker = "unknown flag: --if-content-hash"

func isAfTooOld(err error) bool {
	return strings.Contains(err.Error(), afTooOldMarker)
}

// schemaFingerprint asks the running af binary for its own config-schema digest. Every failure —
// the binary missing, an af older than the verb (which exits NON-zero with `unknown flag: --json`,
// unlike the documented always-exit-0 read contract), or an {"state":"error"} envelope — is the same
// outcome here: the fingerprint is unavailable, so the skew banner simply does not render and the
// rest of the settings page still loads. A console that 502'd the whole page because it could not
// compute a version-comparison would be broken for exactly the operators who most need to see their
// settings.
//
// The underlying error is deliberately DISCARDED rather than wrapped (the telemetryview.relayErr
// discipline): ExecRunner embeds the child's stderr in its error string, and stderr is not a channel
// this module forwards into a browser-bound payload.
func (s *Service) schemaFingerprint(ctx context.Context) string {
	if s.af == nil {
		return ""
	}
	out, err := s.af.ConfigFingerprintJSON(ctx)
	if err != nil {
		return ""
	}
	var env struct {
		State       string `json:"state"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.State != "ok" {
		return ""
	}
	return env.Fingerprint
}

// summaries projects the agents map into a deterministic (name-sorted) slice of secret-free
// summaries.
func summaries(m map[string]agentEntryRead) []AgentSummary {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]AgentSummary, 0, len(names))
	for _, n := range names {
		e := m[n]
		out = append(out, AgentSummary{Name: n, Type: e.Type, Description: e.Description, Formula: e.Formula})
	}
	return out
}

// profileNames returns the model-profile names, sorted. Only the map's KEYS exist by this point —
// modelsFileRead's empty-struct value type discarded every profile body at decode.
func profileNames(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// readRawJSONFile returns a config document as opaque bytes. It validates that the file IS JSON and
// nothing more: no unmarshal, no schema, no field names — so there is nothing here that can fall
// behind af-core. Returns (nil, false, nil) when the file does not exist.
func readRawJSONFile(path string) (json.RawMessage, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if !json.Valid(data) {
		return nil, false, fmt.Errorf("%s is not valid JSON", filepath.Base(path))
	}
	return json.RawMessage(data), true, nil
}

// readJSONFile reads and unmarshals a JSON file into v. It is used ONLY for the projected tier,
// where dropping unknown keys is the point rather than the bug: v is always a decode target with no
// field for any secret. It returns (false, nil) when the file does not exist, and an error only on a
// read failure or malformed JSON.
func readJSONFile(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("decoding %s: %w", filepath.Base(path), err)
	}
	return true, nil
}

// hashHex is the content digest of a config document, over its raw on-disk bytes — the same formula
// af-core's --if-content-hash precondition uses (internal/cmd/config_set.go:580-582), so a
// fingerprint this module serves is one af-core will accept.
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
