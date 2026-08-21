package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// These commands give external (non-Go) consumers a write path for the curated
// config files, symmetric with the `af … --json` read commands: the consumer
// sends a JSON config on stdin and af-core validates + writes it atomically. This
// keeps such consumers off the internal config struct layout (they never import
// internal/config), closing the H-1 coupling on the write side. They are
// registered under the EXISTING `config` parent command (config.go), alongside
// `config build-host`.

var configDispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Manage dispatch configuration (dispatch.json)",
}

var configDispatchSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Replace dispatch.json from a JSON document on stdin",
	Long: `Read a complete DispatchConfig as JSON on stdin, validate it (struct-level
plus a cross-file check that every referenced agent exists in agents.json), and
write it atomically to dispatch.json. On any validation failure the command exits
non-zero and leaves the existing file untouched.`,
	RunE: runConfigDispatchSet,
}

var configStartupCmd = &cobra.Command{
	Use:   "startup",
	Short: "Manage startup configuration (startup.json)",
}

var configStartupSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Replace startup.json from a JSON document on stdin",
	Long: `Read a complete StartupConfig as JSON on stdin, validate it, and write it
atomically to startup.json. On validation failure the command exits non-zero and
leaves the existing file untouched.`,
	RunE: runConfigStartupSet,
}

var configMessagingCmd = &cobra.Command{
	Use:   "messaging",
	Short: "Manage messaging configuration (messaging.json)",
}

var configMessagingSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Replace messaging.json from a JSON document on stdin",
	Long: `Read a complete MessagingConfig as JSON on stdin, validate it (a cross-file
check that every group member exists in agents.json), and write it atomically to
messaging.json. On any validation failure the command exits non-zero and leaves the
existing file untouched.`,
	RunE: runConfigMessagingSet,
}

var configStatuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "Manage statusline configuration (statusline.json)",
}

var configStatuslineSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Replace statusline.json from a JSON document on stdin",
	Long: `Read a complete StatuslineConfig as JSON on stdin, validate it (rejecting any
unknown element name), and write it atomically to statusline.json. On validation failure
the command exits non-zero and leaves the existing file untouched.`,
	RunE: runConfigStatuslineSet,
}

var configStatuslineGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Print the effective statusline configuration as JSON",
	Long: `Print the EFFECTIVE statusline configuration as JSON on stdout — the values that
actually render, with defaults filled in, not the raw file contents. A factory with no
statusline.json prints the full default rather than an empty document, so the output is
always a complete document that can be edited and piped straight back into
` + "`af config statusline set`" + `.`,
	RunE: runConfigStatuslineGet,
}

var configModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Manage model configuration (models.json)",
}

var configModelsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Replace models.json from a JSON document on stdin",
	Long: `Read a complete ModelsConfig as JSON on stdin, validate it, and write it
atomically to models.json. On validation failure the command exits non-zero and
leaves the existing file untouched.`,
	RunE: runConfigModelsSet,
}

func init() {
	// No --json flag: these commands only ever read JSON from stdin, so a --json flag
	// would be a dead control (never consulted) that misrepresents the CLI contract.
	configDispatchCmd.AddCommand(configDispatchSetCmd)
	configCmd.AddCommand(configDispatchCmd)

	configStartupCmd.AddCommand(configStartupSetCmd)
	configCmd.AddCommand(configStartupCmd)

	configMessagingCmd.AddCommand(configMessagingSetCmd)
	configCmd.AddCommand(configMessagingCmd)

	configModelsCmd.AddCommand(configModelsSetCmd)
	configCmd.AddCommand(configModelsCmd)

	configStatuslineCmd.AddCommand(configStatuslineSetCmd)
	configStatuslineCmd.AddCommand(configStatuslineGetCmd)
	configCmd.AddCommand(configStatuslineCmd)

	// The four document setters an external consumer round-trips (read → edit → write) carry
	// the optional content precondition. `models set` is deliberately absent: registering a
	// flag its handler never consults is the dead control the comment above forbids, and the
	// console's models surface does not round-trip a whole document the way these four do.
	for _, c := range []*cobra.Command{configDispatchSetCmd, configStartupSetCmd, configMessagingSetCmd, configStatuslineSetCmd} {
		c.Flags().String(ifContentHashFlag, "", "Write only if the file's current SHA-256 matches this digest (compare-and-set)")
	}
}

// decodeJSONStdin decodes a single JSON document from the command's input (stdin
// in production; overridable via cmd.SetIn in tests). Empty input is an error.
//
// The decode is STRICT: an unknown top-level key is REJECTED, not dropped. Loads stay
// tolerant, and that asymmetry is the point. On the load path tolerance is what makes config
// evolution additive — a file written by a newer af keeps working on an older one. On the write
// path the same tolerance means something else entirely: the setter writes the DECODED struct
// back, so a key it did not recognize is not ignored, it is ERASED FROM DISK. A console that
// reads, edits and PUTs a document would silently strip every key its af-core predates.
//
// This generalizes the reject runConfigStatuslineSet already performs for one key (below):
// there the argument was that a misspelled "elements" blanks the statusline; here it is the same
// argument for every field of every document. Rejecting a newer-schema document is the DESIRED
// fail-loud, not a defect — the operator learns their af is older than their config, which is
// exactly what a silent erase hid.
//
// Strictness is a STRUCT-FIELD rule only: it does not police keys inside map-typed fields, so a
// models profile stays "a plain map of env exports" and a messaging group name stays whatever
// the operator chose.
//
// The %w wrap is load-bearing beyond convention: runConfigModelsSet unwraps a
// *json.UnmarshalTypeError from this error to name the profile-quoting rule. Restructure this
// function freely, but keep the error chain intact.
func decodeJSONStdin(cmd *cobra.Command, v any) error {
	dec := json.NewDecoder(cmd.InOrStdin())
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}
	return nil
}

// isUnknownFieldError reports whether err is encoding/json's unknown-field rejection.
//
// Matched on the message rather than a type: encoding/json returns a bare errors.New for this
// case, so unlike *json.UnmarshalTypeError there is nothing to match with errors.As. Only
// runConfigStatuslineSet needs to tell this error apart, and only to keep its own rejection text
// intact; every other setter is content to surface the decoder's message as-is.
func isUnknownFieldError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "json: unknown field ")
}

func runConfigDispatchSet(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	var cfg config.DispatchConfig
	if err := decodeJSONStdin(cmd, &cfg); err != nil {
		return err
	}

	// Cross-file validation (L-1): every referenced agent must exist in
	// agents.json. Runs BEFORE the write, so a dangling reference never touches
	// the file.
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return fmt.Errorf("loading agents.json for cross-file validation: %w", err)
	}
	// models.json feeds the per-mapping model cross-check. This is a NON-selecting
	// read (af config dispatch set writes dispatch.json, never launches a profile), so
	// a validation error in a half-edited models.json must not block an otherwise-valid
	// dispatch write: it warns and falls through with a nil config, skipping the
	// cross-check rather than hard-failing (PR #482).
	models := loadModelsConfigForCrossCheck(root, cmd.ErrOrStderr())
	if err := config.ValidateDispatchConfig(&cfg, agents, models); err != nil {
		return err
	}

	if err := checkContentPrecondition(cmd, config.DispatchConfigPath(root)); err != nil {
		return err
	}

	// SaveDispatchConfig re-runs struct validation, then writes atomically.
	if err := config.SaveDispatchConfig(config.DispatchConfigPath(root), &cfg); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Dispatch configuration saved.")
	return nil
}

func runConfigStartupSet(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	var cfg config.StartupConfig
	if err := decodeJSONStdin(cmd, &cfg); err != nil {
		return err
	}

	// Cross-file validation, the half runConfigDispatchSet has always done and this handler
	// never did: every name in agents, watchdog_agents and recovery.exclude must exist in
	// agents.json. Runs BEFORE the write, so a dangling reference never touches the file.
	//
	// A load failure is FATAL, matching dispatch (above): without agents.json there is nothing
	// to check against, and writing anyway would be the silent accept this closes.
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return fmt.Errorf("loading agents.json for cross-file validation: %w", err)
	}
	if err := rejectUnknownStartupAgents(&cfg, agents); err != nil {
		return err
	}

	if err := checkContentPrecondition(cmd, config.StartupConfigPath(root)); err != nil {
		return err
	}

	if err := config.SaveStartupConfig(config.StartupConfigPath(root), &cfg); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Startup configuration saved.")
	return nil
}

// runConfigMessagingSet writes messaging.json, the seam messaging.json lacked entirely until
// issue #620 — it was load-only, so an operator could edit groups by hand but a console could
// not edit them at all.
//
// The body is runConfigStartupSet's, with runConfigDispatchSet's cross-check spliced in: a
// group's members ARE agent references, so the dispatch precedent governs, including that an
// unreadable agents.json is fatal. api.md:130-134 settles the question the other way round
// deliberately — a broken agents.json blocking an unrelated messaging save is the correct and
// consistent outcome, because the alternative is writing a group that references nothing.
func runConfigMessagingSet(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	var cfg config.MessagingConfig
	if err := decodeJSONStdin(cmd, &cfg); err != nil {
		return err
	}

	// Same reasoning as the statusline nil-"elements" reject: an absent "groups" key decodes to
	// nil, and on the WRITE path nil is not "empty", it is "erase every group this operator
	// has". An operator who genuinely wants no groups sends an explicit {}. SaveMessagingConfig
	// cannot normalize this for us — validateMessagingConfig, where the normalization lives,
	// needs an *AgentConfig that ADR-004 keeps out of internal/config.
	if cfg.Groups == nil {
		return fmt.Errorf(`%w: messaging config has no "groups" key — a missing or misspelled key would erase every group; send a complete document, e.g. {"groups":{"all":["manager"]}} (use "groups":{} to deliberately define none)`,
			config.ErrInvalidType)
	}

	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return fmt.Errorf("loading agents.json for cross-file validation: %w", err)
	}
	if err := rejectUnknownGroupMembers(&cfg, agents); err != nil {
		return err
	}

	if err := checkContentPrecondition(cmd, config.MessagingConfigPath(root)); err != nil {
		return err
	}

	if err := config.SaveMessagingConfig(config.MessagingConfigPath(root), &cfg); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Messaging configuration saved.")
	return nil
}

func runConfigStatuslineSet(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	var cfg config.StatuslineConfig
	if err := decodeJSONStdin(cmd, &cfg); err != nil {
		// A misspelled key now fails at DECODE rather than reaching the nil-Elements guard
		// below. The rejection moved one step earlier; the operator-facing contract must not.
		// From where they stand the two are the same mistake — a document that does not say
		// what to render — so both answer with the same schema-echoing guidance.
		if isUnknownFieldError(err) {
			return statuslineSchemaReject(err.Error())
		}
		return err
	}

	// An ABSENT "elements" key decodes to nil without any unknown field to catch it, so strict
	// decode does not subsume this guard: `{}` and `{"color":true}` both reach here clean. A nil
	// slice would validate and write {"elements": null}, blanking the statusline. So on the
	// WRITE path null is a REJECT, never an empty list — an operator who genuinely wants to
	// render nothing sends an explicit []. The fail-open load path is deliberately left alone.
	if cfg.Elements == nil {
		return statuslineSchemaReject(`statusline config has no "elements" key`)
	}

	if err := checkContentPrecondition(cmd, config.StatuslineConfigPath(root)); err != nil {
		return err
	}

	// SaveStatuslineConfig re-runs validateStatuslineConfig before the atomic write, so an
	// unknown element is rejected and the existing file is left untouched.
	if err := config.SaveStatuslineConfig(config.StatuslineConfigPath(root), &cfg); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Statusline configuration saved.")
	return nil
}

func runConfigStatuslineGet(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	cfg, err := config.LoadStatuslineConfig(root)
	if err != nil {
		return err
	}

	// Materialize color rather than emitting the absent key. Absent means ON, so printing
	// nothing would tell an operator "unset" and — piped back through `set` — would keep it
	// unset: a lossless-looking round-trip that quietly discards what the value actually IS.
	if cfg.Color == nil {
		on := true
		cfg.Color = &on
	}

	// Bare JSON, indented to match exactly what `set` writes to disk, and nothing else on
	// stdout: `af config statusline get | af config statusline set` has to round-trip.
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling statusline config: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

func runConfigModelsSet(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	var cfg config.ModelsConfig
	if err := decodeJSONStdin(cmd, &cfg); err != nil {
		// The profile map is map[string]map[string]string, so an unquoted numeric value (the
		// issue's `"220000"` written as 220000) fails here as a number→string type error.
		// decodeJSONStdin wraps with %w, so the typed error survives errors.As — name the same
		// quoting rule LoadModelsConfig names, on the primary entry point where the trap actually
		// happens (issue #602 F5). The shared decodeJSONStdin stays generic for the other setters.
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Type != nil && typeErr.Type.String() == "string" {
			return fmt.Errorf("%w (every profile value must be a quoted JSON string, e.g. \"220000\" not 220000)", err)
		}
		return err
	}

	// The next two checks are what validateModelsConfig cannot be: it judges the document
	// against itself, and these two invariants tie it to something outside it. Both run BEFORE
	// the write, so a rejected save leaves models.json byte-for-byte untouched — the same
	// ordering runConfigDispatchSet uses for its agents.json cross-check.
	if err := rejectRedactedTokens(&cfg); err != nil {
		return err
	}
	if err := rejectOrphanedModelPins(root, &cfg, cmd.ErrOrStderr()); err != nil {
		return err
	}

	// Read for the attestation sweep below. It has to happen before the write, because after it
	// there is nothing left to compare against.
	profilesBefore := readModelProfilesOnDisk(root)

	// SaveModelsConfig re-runs validateModelsConfig before the atomic write, so an
	// invalid registry (AF_* key, real api key, incomplete endpoint, undefined
	// agent/default model) is rejected and the existing file is left untouched.
	if err := config.SaveModelsConfig(config.ModelsConfigPath(root), &cfg); err != nil {
		return err
	}

	// A fitness attestation is matched by profile NAME at launch, so it outlives the export set
	// it was earned on unless something clears it when that set changes. This is the only guard
	// that runs after the write: it acts on what was actually persisted.
	invalidateStaleFitnessAttestations(root, profilesBefore, cfg.Models, cmd.ErrOrStderr())

	// Both lints fire on a profile that is incoherent or incomplete, not invalid, so they run
	// after the write and change neither the return value nor the exit code. The validation
	// chain is error-only, which is why the warnings are emitted here rather than in the
	// validator. Sorted so repeated runs of the same registry print the same lines.
	//
	// The coverage lint is the write-time half of issue #598: it says which model classes the
	// launch env will derive on the operator's behalf while they still have the document in hand,
	// long before an unserved class kills a spawn. It never rejects, because a derived class is
	// legal — only worth knowing about — and whether the gateway actually serves the derived id is
	// a question only `af config models check` can answer.
	for _, name := range sortedMapKeys(cfg.Models) {
		if warning, ok := config.PairingLintProfile(name, cfg.Models[name]); ok {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
		}
		if warning, ok := config.CoverageLintProfile(name, cfg.Models[name]); ok {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Models configuration saved.")
	return nil
}

// statuslineSchemaReject is the single home of the H-R1 rejection text. Both paths that can
// produce a document which does not say what to render — a misspelled key (caught by the strict
// decoder) and an absent one (caught by the nil-Elements guard) — answer through it, because an
// operator cannot act on a rejection that does not name the real key and show a usable example,
// and two copies of that sentence would drift.
func statuslineSchemaReject(cause string) error {
	return fmt.Errorf(`%w: %s — a missing or misspelled key would blank the statusline; send a complete document, e.g. {"elements":["%s"]} (use "elements":[] to deliberately render nothing)`,
		config.ErrInvalidType, cause, strings.Join(config.DefaultStatuslineElements(), `","`))
}

// rejectUnknownStartupAgents is the cmd-layer membership check validateStartupConfig
// deliberately omits (startup.go:183-186, ADR-004). It covers all three name lists.
//
// recovery.exclude is the one that motivated the policy (design-doc.md:191): a typo there does
// not merely name a ghost, it silently UNPROTECTS a real agent — and because fillRecoveryDefaults
// rewrites absent values in place, nothing downstream can later tell the typo from an intended
// omission. Rejecting at the write boundary is the last point where the two readings are still
// distinguishable.
//
// Every unknown name is collected rather than returning on the first, so an operator repairing a
// mistyped roster does it in one edit instead of one round trip per name. Sorted for a
// deterministic message. A nil list is the "ALL" sentinel and an empty list means "none" — both
// name nobody, and ranging over either is already a no-op, so neither needs a special case.
func rejectUnknownStartupAgents(cfg *config.StartupConfig, agents *config.AgentConfig) error {
	var unknown []string
	for _, l := range []struct {
		field string
		names []string
	}{
		{"agents", cfg.Agents},
		{"watchdog_agents", cfg.WatchdogAgents},
		{"recovery.exclude", cfg.Recovery.Exclude},
	} {
		for _, name := range l.names {
			if _, ok := agents.Agents[name]; !ok {
				unknown = append(unknown, fmt.Sprintf("%q (in %s)", name, l.field))
			}
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("%w: startup config references %s, absent from agents.json — fix the name, or add the agent with `af install <role>` first",
		config.ErrMissingField, strings.Join(unknown, ", "))
}

// rejectUnknownGroupMembers is the messaging half of the same cmd-layer cross-check. The message
// mirrors validateMessagingConfig's (config.go) so the setter and the loader tell an operator
// the same thing about the same document, and collects every offender for the same reason
// rejectUnknownStartupAgents does.
func rejectUnknownGroupMembers(cfg *config.MessagingConfig, agents *config.AgentConfig) error {
	var unknown []string
	for group, members := range cfg.Groups {
		for _, member := range members {
			if _, ok := agents.Agents[member]; !ok {
				unknown = append(unknown, fmt.Sprintf("group %q references unknown agent %q", group, member))
			}
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("%w: %s — fix the name, or add the agent with `af install <role>` first",
		config.ErrMissingField, strings.Join(unknown, ", "))
}

// ifContentHashFlag names the optional compare-and-set precondition. Declared once because it is
// read in two places (registration in init(), lookup in checkContentPrecondition) and a
// hand-typed second spelling would register a flag no handler consults — the dead control
// TestConfigSet_NoJSONFlag exists to forbid.
const ifContentHashFlag = "if-content-hash"

// checkContentPrecondition enforces --if-content-hash: the write proceeds only if the file's
// current SHA-256 still matches what the caller last read. Absent flag, or empty value, means no
// precondition and today's unconditional behavior.
//
// HONEST SCOPE. This is a read-compare-write across processes with no lock. fsutil.WriteFileAtomic
// already states the baseline it inherits — "Last-writer-wins semantics still apply — this helper
// addresses byte-level corruption, not read-modify-write logical races." The precondition narrows
// the lost-update window from "the whole time an operator had the form open" to the microseconds
// between this comparison and the rename. It does not close it, and it is not a mutual-exclusion
// primitive. That is a real improvement over an unconditional write, and claiming more would be
// worse than claiming nothing.
//
// The flag is looked up rather than read with Flags().GetString because handlers are also driven
// directly by tests through a bare &cobra.Command{} that registers no flags at all; GetString on
// such a command returns "flag accessed but not defined" rather than an empty string, which would
// turn "no precondition" into a hard error at every one of those call sites.
func checkContentPrecondition(cmd *cobra.Command, path string) error {
	f := cmd.Flags().Lookup(ifContentHashFlag)
	if f == nil {
		return nil
	}
	want := f.Value.String()
	if want == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// The caller claims to have read content that is not there. Treating that as a create
		// would grant exactly the unconditional write the flag was passed to prevent, so it is a
		// refusal — and the remedy is named, because "create this file" is a legitimate intent
		// that is simply spelled by omitting the flag.
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: --%s was given but %s does not exist; omit the flag to create it",
				config.ErrNotFound, ifContentHashFlag, filepath.Base(path))
		}
		return fmt.Errorf("reading %s for the --%s precondition: %w", filepath.Base(path), ifContentHashFlag, err)
	}

	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
		// Both digests are quoted: a caller that cannot see what it expected against what is
		// actually there cannot tell a stale read from a mistyped flag.
		return fmt.Errorf("%w: --%s is %s but %s is currently %s — it changed since you read it; re-read the file, re-apply your edit, and retry",
			config.ErrInvalidType, ifContentHashFlag, want, filepath.Base(path), got)
	}
	return nil
}

// rejectRedactedTokens refuses a registry whose auth token is the placeholder `af config models
// show` prints in place of a secret. The redaction is lossy and has no inverse, so nothing
// downstream can tell an operator who typed **** from a shown profile pasted back — and the
// existing value rules cannot catch it either, since **** is neither a file: reference nor a
// credential shape and so validates clean on any endpoint. Rejecting at the boundary is the
// only place the two readings can still be told apart. The display side stays lossy on purpose.
func rejectRedactedTokens(cfg *config.ModelsConfig) error {
	for _, name := range sortedMapKeys(cfg.Models) {
		if cfg.Models[name][authTokenKey] == redactedToken {
			return fmt.Errorf("%w: model %q sets %s to %s, which is what `af config models show` prints INSTEAD of a secret, not a token — send the real value or a %q reference",
				config.ErrInvalidType, name, authTokenKey, redactedToken, secretPrefix)
		}
	}
	return nil
}

// rejectOrphanedModelPins refuses a registry that would leave a dispatch.json mapping pinning a
// profile the registry no longer defines. ValidateDispatchConfig already rejects that state when
// dispatch.json is the document being written; without the same check here the pair of writers
// disagreed, and a save that dropped a pinned profile exited 0 having made the byte-identical
// dispatch.json unsaveable.
//
// The check mirrors the forward one exactly, including its gate: with an EMPTY registry a
// mapping's model is a raw id passed straight to the launch, so emptying models.json restores
// that meaning rather than orphaning anything. Rejecting it here would refuse a set the dispatch
// setter accepts.
func rejectOrphanedModelPins(root string, cfg *config.ModelsConfig, warn io.Writer) error {
	if len(cfg.Models) == 0 {
		return nil
	}
	disp := loadDispatchConfigForPinCheck(root, warn)
	if disp == nil {
		return nil
	}

	var orphaned []string
	for _, m := range disp.Mappings {
		if m.Model == "" {
			continue
		}
		if _, ok := cfg.Models[m.Model]; !ok {
			// Identify the mapping by the loaded struct: the loader normalizes a lone `label`
			// into `labels`, so raw bytes and struct disagree about how a mapping is named.
			orphaned = append(orphaned, fmt.Sprintf("%q (pinned by the dispatch mapping for agent %q, labels %v)", m.Model, m.Agent, m.Labels))
		}
	}
	if len(orphaned) > 0 {
		return fmt.Errorf("%w: this registry does not define %s; define it, or drop the pin with `af config dispatch set` first",
			config.ErrMissingField, strings.Join(orphaned, ", "))
	}
	return nil
}

// loadDispatchConfigForPinCheck loads dispatch.json for the pin cross-check above. It is the
// mirror of loadModelsConfigForCrossCheck (helpers.go), and a nil return means the same thing:
// nothing to check against, so skip.
//
// The two absent-file cases differ, though, so they are branched apart. An absent models.json
// loads as an empty config, but an absent dispatch.json is an ErrNotFound error — and a factory
// that never configured dispatch is the ordinary case, not an anomaly, so it must skip in
// SILENCE. Only a dispatch.json that exists and cannot be loaded warns: it is half-edited or
// hand-written, which is the same situation in which a broken models.json must not block a
// dispatch write. LoadDispatchConfig validates as well as parses, so "cannot be loaded" also
// covers a document that is structurally fine but incomplete; both belong on this branch.
func loadDispatchConfigForPinCheck(root string, warn io.Writer) *config.DispatchConfig {
	cfg, err := config.LoadDispatchConfig(root)
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			fmt.Fprintf(warn, "warning: ignoring dispatch.json for the model pin cross-check (%v); proceeding without it\n", err)
		}
		return nil
	}
	return cfg
}
