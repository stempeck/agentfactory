package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
)

// This file adds the read-only model diagnostics (`show`/`check`, issue #508) and
// the fitness-attestation write path (`attest`) under the existing `af config
// models` group (config_set.go). They never mutate models.json — `show`/`check` load
// it read-only and `attest` writes only the factory-root fitness marker. Secret
// material is never printed: a literal ANTHROPIC_AUTH_TOKEN renders as `****`, a
// `file:` reference prints as-is, and `check` passes the resolved token into the
// probe seam but never into the output. All three commands live in the cmd layer,
// which may do I/O and read env (ADR-004 confines only the library).

const (
	authTokenKey = "ANTHROPIC_AUTH_TOKEN"
	baseURLKey   = "ANTHROPIC_BASE_URL"
	modelKey     = "ANTHROPIC_MODEL"
	secretPrefix = "file:"

	// redactedToken is what a literal auth token prints as. It is named rather than repeated
	// because it now has two readers: displayModelValue writes it, and the models setter rejects
	// it — a redaction and its inverse that drifted apart would silently persist the placeholder
	// as the secret.
	redactedToken = "****"

	modelsProbeTimeout = 5 * time.Second

	// liveSmokeDeadline is the --live smoke's own deadline (issue #686 K3), decoupled from
	// modelsProbeTimeout: a real agentic /v1/messages turn can run far longer than the 5s
	// /v1/models probe budget, so reusing modelsProbeTimeout there was a correctness bug, not a
	// simplification. The checked-in .agentfactory/litellm.yaml's router_settings.timeout must not
	// undercut it (TestLiveSmokeUsesItsOwnDeadline).
	liveSmokeDeadline = 300 * time.Second

	// firstProbeDeadline is --first's own budget (issue #693 K14): a single classified
	// /v1/messages request, deliberately far shorter than liveSmokeDeadline because --first is
	// a fast auth pre-flight run from quickstart.sh's bootstrap path, not a per-class coverage
	// sweep. Kept distinct on purpose — DO NOT repurpose or shrink liveSmokeDeadline for this.
	firstProbeDeadline = 30 * time.Second

	// firstProbeRetryBackoff is the fixed sleep before --first's single 429 retry. No spec
	// text or existing codebase convention pins an exact duration (the spec says only "one
	// backoff retry then classify"); 1s is a default that leaves ample room inside
	// firstProbeDeadline for both the original request and the retry.
	firstProbeRetryBackoff = 1 * time.Second

	// claudeIDPrefix marks an id the host asks for by its own name rather than by anything the
	// profile declares. fableClassPrefix is the sub-case with no env key at all: the class inventory
	// has no ANTHROPIC_DEFAULT_FABLE_MODEL rung, so a Fable request leaves the host as claude-fable-*
	// whatever the profile says, and only a gateway alias can answer it.
	claudeIDPrefix   = "claude-"
	fableClassPrefix = "claude-fable-"

	// aliasRemedy is runtime-fixable on purpose: aliasing an id on the gateway and reloading it needs
	// no container recreation (ADR-019). It names LiteLLM parenthetically rather than as the whole
	// remedy because an endpoint profile can point at any gateway — the seeded registry ships an
	// lmstudio profile — and telling an LM Studio operator to edit litellm.yaml would be a dead end
	// on a verdict that is otherwise correct.
	aliasRemedy = "alias the id on the gateway (LiteLLM: add a model_name entry to litellm.yaml, see USING_LITELLM.md)"

	// aliasRemedyDirect is the direct-endpoint counterpart to aliasRemedy (#607/F3). A direct endpoint
	// — one whose ANTHROPIC_AUTH_TOKEN is a literal rather than a file: gateway secret — cannot alias a
	// claude-* id the way a gateway does: model_name indirection is a LiteLLM feature, so sending its
	// operator to litellm.yaml is the dead end F3 flags. The honest remedy is to register the id on the
	// endpoint's own server; the row is surfaced (recorded NOT SERVED) but advisory, not a hard failure.
	aliasRemedyDirect = "register the id on the endpoint's own server (a direct endpoint cannot alias — only a gateway such as LiteLLM can)"

	// modelCoverageVersion stamps the on-disk record. A reader that does not recognise it treats the
	// record as absent rather than guessing at an older shape's meaning — same posture as
	// recoveryStateVersion.
	modelCoverageVersion = 1
)

var (
	configModelsShowAgent  string
	configModelsCheckLive  bool
	configModelsCheckFirst bool
)

var configModelsShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Print the model registry with secrets redacted",
	Long: `Load models.json read-only and print the effective registry: each profile's
env exports (a literal ANTHROPIC_AUTH_TOKEN redacted to ****, a file: reference shown
as-is), the agents assignment map, and the default. With --agent <name>, also explain
which profile that agent resolves to.`,
	RunE: runConfigModelsShow,
}

var configModelsCheckCmd = &cobra.Command{
	Use:   "check [profile]",
	Short: "Transport-level probe of endpoint-bearing model profiles",
	Long: `Probe endpoint-bearing profiles (or a single named profile): resolve the
file: secret (exists / non-empty / mode), reach ANTHROPIC_BASE_URL, and report a per-class
verdict for every model class a session can request — including the claude-* ids the host
asks for by name, which only a gateway alias can serve. A class the gateway does not serve
FAILS the check. The profile's own ANTHROPIC_MODEL not appearing in GET /v1/models is
reported but does not fail on its own: every class derived from that id already has its own
verdict. The verdicts are recorded under .runtime/model_coverage/ so a later launch can
report them without probing anything. With --live, additionally send one minimal
streamed /v1/messages turn per served class, shaped like a session's turn (block-array
system prompt); that performs real, billable requests. A failing probe prints the
gateway's own error text verbatim.

This is transport-level only — necessary, not sufficient, for fitness. Never prints
token material.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigModelsCheck,
}

var configModelsAttestCmd = &cobra.Command{
	Use:   "attest <profile>",
	Short: "Record a fitness attestation for a model profile",
	Long: `Write a factory-root .runtime/model_fitness/<profile>.json attestation (who,
when, stage results). The selecting-launch interlock refuses an unattested
non-loopback profile until this is recorded, unless --skip-fitness is passed. The
attestation lives under .runtime, so an environment reset requires re-attestation.`,
	Args: cobra.ExactArgs(1),
	RunE: runConfigModelsAttest,
}

func init() {
	configModelsShowCmd.Flags().StringVar(&configModelsShowAgent, "agent", "", "Explain which profile the named agent resolves to")
	configModelsCheckCmd.Flags().BoolVar(&configModelsCheckLive, "live", false, "Also send one minimal streamed /v1/messages turn, shaped like a session's, per served class (real, billable requests; spends ChatGPT-subscription plan quota against a subscription profile)")
	configModelsCheckCmd.Flags().BoolVar(&configModelsCheckFirst, "first", false, "Send exactly one minimal streamed /v1/messages turn, shaped like a session's, against the named profile with a 30s deadline and print one classified, grep-able verdict line carrying the gateway's own error text (requires a profile argument)")
	configModelsCmd.AddCommand(configModelsShowCmd)
	configModelsCmd.AddCommand(configModelsCheckCmd)
	configModelsCmd.AddCommand(configModelsAttestCmd)
}

// httpProbe fetches the gateway's advertised model ids via GET <baseURL>/v1/models
// under a bounded timeout. Declared as a package-level var (ADR-009 seam) so tests
// substitute a canned response with no real network (ADR-018: no network in
// `make test`) — this is the FIRST http client in internal/cmd, so the seam is
// mandatory. It receives the resolved token to authenticate the request; the token
// is never returned or logged (T-2 redaction stays at the output layer). Modelled on
// runGitDetect's context.WithTimeout bound and ghPRStatus's (T, error) inline-decode.
var httpProbe = func(baseURL, authToken string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/models returned %s", resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// modelsMessagesDo is the transport seam (ADR-009) for the --live per-class smoke. It is
// request-granular rather than result-granular like httpProbe above, so the response-to-verdict
// rule stays on this side of the seam where it can be read — the shape telemetryQueryDo already
// uses for the same reason. httpProbe cannot serve here: it is fixed to GET /v1/models and has
// nowhere to put a request body.
//
// Substituting it is how the smoke gets tested without a network (ADR-018): --live sends real,
// billable requests, so nothing under `make test` may reach the default transport.
var modelsMessagesDo = func(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// modelInfoProbe is the routing cross-check's transport seam (issue #686 A11), modeled on
// httpProbe/modelsMessagesDo (package-level var, ADR-009) so tests substitute it with no real
// network (ADR-018). It reports GET <baseURL>/model/info's advertised routing: each model_name
// LiteLLM serves mapped to its litellm_params.model backend (e.g. "chatgpt/gpt-5.6-sol" or
// "openai/gpt-5.6-sol") — the value the subscription-mode routing cross-check compares against a
// required chatgpt/ prefix.
var modelInfoProbe = func(baseURL, authToken string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/model/info", nil)
	if err != nil {
		return nil, err
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /model/info returned %s", resp.Status)
	}
	var body struct {
		Data []struct {
			ModelName     string `json:"model_name"`
			LitellmParams struct {
				Model string `json:"model"`
			} `json:"litellm_params"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	routes := make(map[string]string, len(body.Data))
	for _, m := range body.Data {
		routes[m.ModelName] = m.LitellmParams.Model
	}
	return routes, nil
}

func runConfigModelsShow(cmd *cobra.Command, _ []string) error {
	_, cfg, err := loadModelsForRead()
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if cfg.Default != "" {
		fmt.Fprintf(&buf, "default: %s\n", cfg.Default)
	} else {
		fmt.Fprintln(&buf, "default: (none — global default model)")
	}

	profiles := sortedMapKeys(cfg.Models)
	if len(profiles) == 0 {
		fmt.Fprintln(&buf, "\n(no model profiles defined)")
	}
	for _, name := range profiles {
		fmt.Fprintf(&buf, "\nprofile %s:\n", name)
		tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
		for _, k := range sortedMapKeys(cfg.Models[name]) {
			fmt.Fprintf(tw, "  %s\t%s\n", k, displayModelValue(k, cfg.Models[name][k]))
		}
		tw.Flush()
	}

	if len(cfg.Agents) > 0 {
		fmt.Fprintln(&buf, "\nagents:")
		tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
		for _, a := range sortedMapKeys(cfg.Agents) {
			fmt.Fprintf(tw, "  %s\t%s\n", a, cfg.Agents[a])
		}
		tw.Flush()
	}

	if configModelsShowAgent != "" {
		fmt.Fprintf(&buf, "\n%s\n", explainAgentPrecedence(cfg, configModelsShowAgent))
	}

	fmt.Fprint(cmd.OutOrStdout(), buf.String())
	return nil
}

func runConfigModelsCheck(cmd *cobra.Command, args []string) error {
	root, cfg, err := loadModelsForRead()
	if err != nil {
		return err
	}

	if configModelsCheckFirst {
		if len(args) == 0 || args[0] == "" {
			return fmt.Errorf("--first requires a profile name: af config models check <profile> --first")
		}
		return runFirstProbe(cmd, root, cfg, args[0])
	}

	var names []string
	if len(args) > 0 && args[0] != "" {
		if _, ok := cfg.Models[args[0]]; !ok {
			return fmt.Errorf("unknown model profile %q: not defined in models.json", args[0])
		}
		names = []string{args[0]}
	} else {
		for name, profile := range cfg.Models {
			if profile[baseURLKey] != "" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
	}

	// Swept over EVERY profile, not the endpoint-filtered `names` above, and before the transport
	// probes rather than beside them. A capacity misconfiguration is a fact about the document, not
	// about a network: it is equally wrong on a profile this run is not probing, and reporting it
	// only for the probed subset would make `check <one-profile>` quietly narrower than `check`.
	//
	// Safe to read here because loadModelsForRead already succeeded — the lint speaks only about
	// files that load, which is what makes its narrow warn band the whole of what survives validation.
	for _, name := range sortedMapKeys(cfg.Models) {
		if warning, ok := config.CapacityLintProfile(name, cfg.Models[name]); ok {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
		}
	}

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "af config models check — transport-level only (necessary, not sufficient for fitness).")

	aliasIDs := directProfileClaudeIDs(cfg)
	dispatchCfg := loadDispatchConfigForPinCheck(root, out)
	hardFailures := 0
	for _, name := range names {
		if checkProfile(out, root, name, cfg.Models[name], aliasIDs, name == cfg.Default, dispatchCfg) {
			hardFailures++
		}
	}
	if hardFailures > 0 {
		return fmt.Errorf("%d transport check(s) failed", hardFailures)
	}
	return nil
}

// directProfileClaudeIDs collects the claude-* ids the registry's DIRECT (non-endpoint) profiles
// name. They are the ids this factory has said out loud that it wants, and the host asks for a
// claude-* id by that name and no other — so on a gateway meant to serve this registry they are
// precisely the aliases that have to exist (design-doc.md:181).
//
// It is computed once per run and handed to checkProfile as a parameter because checkProfile is
// given one profile and cannot see the registry it came from.
func directProfileClaudeIDs(cfg *config.ModelsConfig) []string {
	seen := map[string]bool{}
	var ids []string
	for _, name := range sortedMapKeys(cfg.Models) {
		profile := cfg.Models[name]
		if profile[baseURLKey] != "" {
			continue
		}
		for _, key := range append([]string{modelKey}, config.EndpointClassKeys...) {
			id := profile[key]
			if !strings.HasPrefix(id, claudeIDPrefix) || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// coverageRow is one verdict: what was asked for, whether the gateway serves it, and the operator
// line that says so. The line is built where the reason is known — a class with no id at all needs a
// different remedy from a class whose id is simply not registered — rather than reconstructed later
// from the data.
type coverageRow struct {
	class  string
	model  string
	served bool
	// soft marks a row that is recorded and PRINTED as NOT SERVED but excluded from the exit-code
	// count: a direct endpoint's claude-* alias rows, which it structurally cannot answer, so a
	// hard-fail on them would be a dead end (#607/F3). The launch record keeps Served=false, so the
	// launch still surfaces them loudly (design-doc Decision 9); only the check's exit code softens.
	soft bool
	line string
}

// endpointCoverageRows computes one verdict per model class an endpoint profile's sessions can
// request, given what GET /v1/models reported.
//
// The class rows walk config.DerivedEndpointClassKeys(), not the wider EndpointClassKeys inventory:
// CLAUDE_CODE_SUBAGENT_MODEL is an inventory member derivation deliberately never fills, so a row
// over it would report an empty id as unserved on every endpoint profile that does not declare the
// key by hand — and leaving it undeclared is the norm that exclusion exists to protect, so the row
// would fail the profiles it was meant to help. Each row names the EFFECTIVE post-derivation id from
// config.CompleteEndpointProfile, the value the launch actually exports, never the raw declaration;
// reporting the declaration would tell the operator about a config file they already have instead of
// about the request the gateway will receive.
//
// The alias rows carry the half of issue #598 no env key reaches: the claude-* ids the host asks for
// by name, which only a gateway alias can answer. Fable is the class with no key at all, so it gets
// a row of its own even when the registry names no fable id.
//
// A gateway that has not been given those aliases therefore fails this check — deliberately, and
// including the seeded litellm.yaml a fresh bootstrap installs today. The alias seeds and this
// factory's own checked-in gateway config ship in the same PR as this surface, so the failure is
// self-catching: it cannot survive the merge that introduces it.
//
// gateway discriminates how the claude-* alias rows are treated. A gateway that lacks an alias is a
// HARD failure with the LiteLLM remedy (#598). A direct endpoint cannot alias at all, so its alias +
// fable rows are SOFT — printed and recorded NOT SERVED, pointed at the operator's own server, but
// excluded from the exit code (#607/F3). The DERIVED-class rows stay hard on both kinds.
func endpointCoverageRows(profile map[string]string, aliasIDs, served []string, gateway bool) []coverageRow {
	completed := config.CompleteEndpointProfile(profile)
	rows := make([]coverageRow, 0, len(config.DerivedEndpointClassKeys())+len(aliasIDs)+1)

	for _, key := range config.DerivedEndpointClassKeys() {
		label, _ := config.EndpointClassLabel(key)
		id := completed[key]
		switch {
		case id == "":
			rows = append(rows, coverageRow{
				class: label,
				line: fmt.Sprintf("class %s → (no id): NOT SERVED — declare %s in the profile so the class derives, or declare %s directly (af config models set)",
					label, modelKey, key),
			})
		case modelIDPresent(served, id):
			rows = append(rows, coverageRow{
				class:  label,
				model:  id,
				served: true,
				line:   fmt.Sprintf("class %s → %q (%s): served", label, id, coverageSource(profile, key)),
			})
		default:
			rows = append(rows, coverageRow{
				class: label,
				model: id,
				line: fmt.Sprintf("class %s → %q (%s): NOT SERVED — %s",
					label, id, coverageSource(profile, key), aliasRemedy),
			})
		}
	}

	aliasRemedyText, aliasSoft := aliasRemedy, false
	if !gateway {
		aliasRemedyText, aliasSoft = aliasRemedyDirect, true
	}

	fableCount := 0
	for _, id := range aliasIDs {
		if !strings.HasPrefix(id, fableClassPrefix) {
			rows = append(rows, directAliasRow(id, modelIDPresent(served, id), aliasRemedyText, aliasSoft))
			continue
		}
		// One row per fable id the registry names, each demanding that EXACT alias: a claude-fable-4
		// alias does not answer a claude-fable-5 request, so checking a representative one would leave
		// the others as the silent gap this whole surface exists to close.
		fableCount++
		rows = append(rows, fableAliasRow(id, modelIDPresent(served, id), aliasRemedyText, aliasSoft))
	}
	if fableCount == 0 {
		rows = append(rows, unverifiableFableRow(served, aliasRemedyText, aliasSoft))
	}
	return rows
}

func fableAliasRow(id string, served bool, remedy string, soft bool) coverageRow {
	if served {
		return coverageRow{class: "fable-class", model: id, served: true,
			line: fmt.Sprintf("fable-class → gateway alias for %q: served", id)}
	}
	return coverageRow{class: "fable-class", model: id, soft: soft,
		line: fmt.Sprintf("fable-class → no gateway alias for %q: NOT SERVED — %s", id, remedy)}
}

// unverifiableFableRow covers a registry that names no fable id at all.
//
// af cannot read the id the host will ask for — that is a property of the installed CLI. A registry
// that names one IS the operator answering that question, which is why fableAliasRow can demand an
// exact alias and report served when it exists. With no such answer there is nothing to match
// against, and falling back to the claude-fable-* PREFIX would be the failure this whole surface
// exists to prevent: a gateway aliasing some other fable generation would report served, the launch
// would stay silent, and the spawn would still die. The row therefore never passes on a prefix, and
// says which of the two things is missing — the alias, or the id to demand it for.
func unverifiableFableRow(served []string, remedy string, soft bool) coverageRow {
	row := coverageRow{class: "fable-class", model: fableClassPrefix + "*", soft: soft}
	if anyServedWithPrefix(served, fableClassPrefix) {
		row.line = fmt.Sprintf("fable-class → the gateway aliases a %s id but the registry names none to check it against: NOT SERVED — add the fable model as a profile (af config models set) so check can demand that exact alias", fableClassPrefix+"*")
		return row
	}
	row.line = fmt.Sprintf("fable-class → no gateway alias for %s: NOT SERVED — %s", fableClassPrefix+"*", remedy)
	return row
}

// directAliasRow reports a claude-* id one of the registry's direct profiles names. The id is part
// of the class rather than only of the line, because the launch warning replays class names and
// "alias NOT SERVED" would not tell the operator which alias to add.
func directAliasRow(id string, served bool, remedy string, soft bool) coverageRow {
	class := "alias " + id
	if served {
		return coverageRow{class: class, model: id, served: true,
			line: fmt.Sprintf("%s → served", class)}
	}
	return coverageRow{class: class, model: id, soft: soft,
		line: fmt.Sprintf("%s → NOT SERVED — %s", class, remedy)}
}

// coverageSource says where a class's effective id came from, so a NOT SERVED line points at the key
// the operator has to edit rather than at a value they never typed. The answer comes from the ladder
// itself: the haiku and small/background rungs copy from each other before falling back to the main
// model, so assuming ANTHROPIC_MODEL here would name the wrong key on every profile that declares
// only one of that pair — and on every profile with no main model at all.
func coverageSource(profile map[string]string, key string) string {
	source, known := config.EndpointClassSource(profile, key)
	switch {
	case !known:
		return "no source"
	case source == key:
		return "declared"
	default:
		return "derived from " + source
	}
}

func anyServedWithPrefix(served []string, prefix string) bool {
	for _, id := range served {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// checkProfile probes one profile and returns true if a HARD failure occurred: a missing secret, an
// unreachable endpoint, or a model class the gateway does not serve. A class is hard because an
// unserved one kills a spawn at the moment the session reaches for it, which is exactly the silent
// failure issue #598 is about.
//
// The older ANTHROPIC_MODEL line stays SOFT, not because an unregistered main model is harmless, but
// because it is not a separate problem: every class that derives from that id already has its own
// hard row, so hardening the line too would count one gap twice. Permissive secret mode and a
// git-tracked secret stay soft on their own merits. Never prints token material.
//
// aliasIDs are the registry's direct-profile claude-* ids (directProfileClaudeIDs); a caller holding
// one profile passes them in because coverage is a property of the registry, not of the profile.
//
// isDefault and dispatchCfg feed the fleet-scale advisory (issue #686 D11/D15): whether this
// profile is models.json's own .default, or named by a dispatch.json mapping/cron, is a fact about
// the registry checkProfile cannot see from one profile map, so the caller resolves it once per run
// and hands it in.
func checkProfile(out io.Writer, root, name string, profile map[string]string, aliasIDs []string, isDefault bool, dispatchCfg *config.DispatchConfig) (hardFailure bool) {
	base := profile[baseURLKey]
	if base == "" {
		fmt.Fprintf(out, "profile %q: no ANTHROPIC_BASE_URL — nothing to probe (local/default model)\n", name)
		return false
	}

	secret := profile[authTokenKey]
	if strings.HasPrefix(secret, secretPrefix) {
		path := secretRefPath(root, secret)
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() || info.Size() == 0 {
			fmt.Fprintf(out, "profile %q: secret reference ANTHROPIC_AUTH_TOKEN → %s: file not found\n", name, path)
			recordNoMeasurement(out, root, name)
			return true
		}
		if info.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(out, "profile %q: warning: secret %s is group/other-accessible (mode %04o); tighten to 0600\n", name, path, info.Mode().Perm())
		}
		if tracked := runGitDetect(root, "git", "ls-files", "--error-unmatch", path); tracked != "" {
			fmt.Fprintf(out, "profile %q: warning: secret %s is tracked by git; move it out of version control\n", name, path)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			fmt.Fprintf(out, "profile %q: secret reference ANTHROPIC_AUTH_TOKEN → %s: %v\n", name, path, readErr)
			recordNoMeasurement(out, root, name)
			return true
		}
		secret = strings.TrimSpace(string(data))
	}

	ids, probeErr := httpProbe(base, secret)
	if probeErr != nil {
		fmt.Fprintf(out, "profile %q: ANTHROPIC_BASE_URL %s unreachable: %v\n", name, base, probeErr)
		// No served list means no class verdict is possible, and inventing one would blame coverage
		// for a transport problem.
		recordNoMeasurement(out, root, name)
		return true
	}
	if want := profile[modelKey]; want != "" && !modelIDPresent(ids, want) {
		fmt.Fprintf(out, "profile %q: model %q not in GET /v1/models response (gateway may need registration)\n", name, want)
	} else {
		fmt.Fprintf(out, "profile %q: reachable; model %q present at %s\n", name, profile[modelKey], base)
	}

	// The discriminator is the auth-token SHAPE (decision D3): a file: secret is how an af-managed
	// gateway authenticates, so it keeps the #598 hard-fail on missing aliases; any other token
	// (a literal, as the shipped lmstudio profile carries) marks a direct endpoint whose alias rows
	// soften. It is read from the RAW profile value, before the file: ref above is resolved to its
	// contents. The classification is PRINTED for a direct endpoint so the softening is never silent.
	gateway := strings.HasPrefix(profile[authTokenKey], secretPrefix)
	if !gateway {
		fmt.Fprintf(out, "profile %q: direct endpoint (ANTHROPIC_AUTH_TOKEN is not a file: gateway secret) — claude-* alias rows are advisory, not hard failures\n", name)
	}

	// Derived-mode upstream-auth stage + /model/info routing cross-check (issue #686 Phase 2,
	// F9/F10/D2; issue #693 Phase 6 K16/D3/D4/D9). Mode is DERIVED from the persisted
	// gatewayAuthMode(root) record, never profile-carried (INV-1/INV-2): both secret handles present
	// on disk is no longer a case that skips the cross-check — it prints an informational line (never
	// refusing to decide) and the persisted record still decides which cross-check direction runs.
	// An unresolvable mode (gatewayAuthMode error: no record, both handles present, no other signal)
	// is a hard failure here — `af config models check` is the surface responsible for reporting
	// auth-mode ambiguity loudly (decisions.md D3), unlike the launch/watchdog paths which degrade
	// silently to avoid bricking a running agent. The A11 routing cross-check runs in BOTH modes
	// (F9): subscription ⇒ an openai/ lane is a hard fail; key mode ⇒ a chatgpt/ lane with no handle
	// is a hard fail. modelInfoProbe is fired for every gateway profile now, so setupConfigFactory
	// stubs it (config_set_test.go) to keep pre-existing gateway tests hermetic (ADR-018).
	if gateway {
		subHandle := subscriptionHandlePresent(root)
		if subHandle && apiKeySecretPresent(root) {
			fmt.Fprintf(out, "profile %q: both an OpenAI API key and a ChatGPT-subscription handle are present on disk; the persisted gateway auth mode selects which credential is audited\n", name)
		}
		mode, _, modeErr := gatewayAuthMode(root)
		if modeErr != nil {
			fmt.Fprintf(out, "profile %q: gateway auth mode: %v\n", name, modeErr)
			recordNoMeasurement(out, root, name)
			return true
		}
		routes, routesErr := modelInfoProbe(base, secret)
		if routesErr != nil {
			fmt.Fprintf(out, "profile %q: routing not verified — /model/info unavailable: %v\n", name, routesErr)
		}
		subscription := mode == gatewayAuthProfileName
		hardFail := false
		if subscription {
			if checkUpstreamAuthStage(out, root, name) {
				hardFail = true
			}
			if routesErr == nil && subscriptionRoutingCrossCheck(out, name, routes, profile, aliasIDs) {
				hardFail = true
			}
			fleetScaleAdvisory(out, name, isDefault, dispatchCfg)
		} else if routesErr == nil && keyModeRoutingCrossCheck(out, name, routes, profile, aliasIDs) {
			hardFail = true
		}
		if hardFail {
			recordNoMeasurement(out, root, name)
			return true
		}
	}

	rows := endpointCoverageRows(profile, aliasIDs, ids, gateway)
	for _, row := range rows {
		fmt.Fprintf(out, "profile %q: %s\n", name, row.line)
	}

	// --live is a second, behavioural stage over the rows that listed: a gateway can advertise an id
	// in /v1/models and still refuse a request for it. It runs BEFORE the verdicts are counted so a
	// refusal lands in the exit code and in the record alike — a run that found every class refused
	// must not leave a record saying nothing was wrong.
	if configModelsCheckLive {
		liveSmokeRows(out, name, base, secret, rows)
	}

	verdicts := make([]modelCoverageVerdict, 0, len(rows))
	failing := 0
	for _, row := range rows {
		// A soft row (a direct endpoint's alias row) is recorded NOT SERVED — verdicts keep
		// Served=row.served so the launch still surfaces it — but excluded from the exit code (#607/F3).
		if !row.served && !row.soft {
			failing++
		}
		verdicts = append(verdicts, modelCoverageVerdict{Class: row.class, Model: row.model, Served: row.served})
	}

	// Written on the failing path too: a launch wants to report what the last check found, and
	// "found three classes unserved" is more use than "no record at all". A write failure is a
	// warning, not a hard one — the verdicts above already reached the operator, and refusing the
	// exit code over a .runtime write would turn a reporting problem into a coverage problem.
	if err := writeModelCoverageRecord(root, modelCoverageRecord{
		Profile:   name,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Failing:   failing,
		Classes:   verdicts,
	}); err != nil {
		fmt.Fprintf(out, "profile %q: warning: could not record the coverage verdicts: %v\n", name, err)
	}
	return failing > 0
}

// subscriptionHandlePresent reports whether the Phase-1 subscription handle exists on disk — the
// on-disk artifact that IS "derived mode == subscription" (issue #686, gateway_auth.go's own
// doc comment on gatewayAuthHandlePath).
func subscriptionHandlePresent(root string) bool {
	return gatewayHandleNonEmpty(root)
}

// apiKeySecretPresent reports whether a non-empty OpenAI API-key secret exists at the ladder's own
// path (install.go:935) — the api-key half of INV-2's mode derivation. Non-empty, matching the
// subscription handle's own predicate, so a 0-byte file counts on neither side.
func apiKeySecretPresent(root string) bool {
	fi, err := os.Stat(filepath.Join(config.ConfigDir(root), "secrets", "openai.key"))
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

// gatewayHandleExpiresAt reads the live handle's own exp (F1/D7): the authoritative expiry source,
// since LiteLLM refreshes the handle in place while the .runtime state-record mirror goes stale. A
// missing/unparseable handle, or one carrying no exp claim, returns (0, false) — judged "unknown",
// never expired and never silently backfilled from the mirror.
func gatewayHandleExpiresAt(root string) (int64, bool) {
	data, err := os.ReadFile(gatewayAuthHandlePath(root))
	if err != nil {
		return 0, false
	}
	var h gatewayAuthHandle
	if json.Unmarshal(data, &h) != nil || h.ExpiresAt == 0 {
		return 0, false
	}
	return h.ExpiresAt, true
}

// subscriptionHardFailState is the enum-state hard-fail set checkProfile's upstream-auth stage and
// sling's selecting-launch E5 refusal both key on (D13): a revoked/no-refresh-token credential must
// refuse identically on both surfaces, or `af config models check` and a launch would disagree
// about the very record they both read. An absent (`missing`) or unparseable/wrong-version
// (`corrupt`) record is NOT in the set (F4/F27/D1, design E3 L175): it yields a loud `unverified`
// warning, never a hard fail — a wiped `.runtime/` must not refuse launches on a healthy gateway,
// and a wedged gateway with no state record must not be re-keyed to a credential cause at the
// watchdog.
func subscriptionHardFailState(state string) bool {
	switch state {
	case gatewayAuthStateRevoked, gatewayAuthStateNoRefreshToken:
		return true
	}
	return false
}

// checkUpstreamAuthStage audits the Phase-1 state record for a derived-mode subscription profile
// and reports a HARD failure for the closed state set (D13) or a derived past-exp credential
// (D3/D5). A healthy, current credential is reported and its LastVerifiedAt stamped via a
// read-modify-write (D4): writeGatewayAuthState is a full-struct overwrite, so mutating anything
// less than the just-read record would silently erase Phase 1's ImportedAt/AuthDir/Mode/etc. on the
// very first post-Phase-2 `check` run. An unverified state (the record itself unreadable — a
// non-ENOENT error) is reported loudly but is neither a hard fail nor written back, since there is
// nothing trustworthy to persist.
func checkUpstreamAuthStage(out io.Writer, root, name string) (hardFail bool) {
	st, state := readGatewayAuthState(root)
	if subscriptionHardFailState(state) {
		fmt.Fprintf(out, "profile %q: subscription credential state %q — run `af gateway auth import` after `codex login`\n", name, state)
		return true
	}
	if state != gatewayAuthStateOK {
		fmt.Fprintf(out, "profile %q: subscription credential state unverified — run `af config models check` again\n", name)
		return false
	}
	exp, known := gatewayHandleExpiresAt(root)
	if !known {
		// F1/D7: the handle is the sole expiry source; a handle carrying no decodable exp is
		// "unknown" — surfaced, never silently backfilled from the .runtime mirror, never judged
		// expired.
		fmt.Fprintf(out, "profile %q: subscription credential expiry unknown — the handle carries no decodable exp; run `af gateway auth import` after `codex login`\n", name)
		return false
	}
	if !time.Now().UTC().Before(time.Unix(exp, 0).UTC()) {
		fmt.Fprintf(out, "profile %q: subscription credential has expired — run `af gateway auth import` after `codex login`\n", name)
		return true
	}
	fmt.Fprintf(out, "profile %q: subscription credential verified (state=%s)\n", name, state)
	st.LastVerifiedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeGatewayAuthState(root, st); err != nil {
		fmt.Fprintf(out, "profile %q: warning: could not record subscription verification: %v\n", name, err)
	}
	return false
}

// effectiveModelIDs is the routing cross-check's own universe: this profile's effective
// (post-derivation) class ids plus the registry's demanded claude-* aliases — the same two
// universes endpointCoverageRows checks for SERVED, checked here for chatgpt/ ROUTING instead.
func effectiveModelIDs(profile map[string]string, aliasIDs []string) []string {
	completed := config.CompleteEndpointProfile(profile)
	seen := map[string]bool{}
	var ids []string
	for _, key := range config.DerivedEndpointClassKeys() {
		id := completed[key]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, id := range aliasIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// subscriptionRoutingCrossCheck is A11's subscription direction: every effective class id and
// demanded claude-* alias must resolve, via /model/info, to a chatgpt/ litellm_params.model backend —
// a subscription-mode profile routed to an openai/ lane is a HARD failure naming the id and the wrong
// lane (D8: an independent axis, never folded into coverageRow.served). Routes are probed once by the
// caller so the same observation drives mode derivation and both cross-check directions; an
// unavailable /model/info is reported once at the probe site and this check is skipped (D7: uniform
// soft treatment, never a definitive wrong answer).
func subscriptionRoutingCrossCheck(out io.Writer, name string, routes map[string]string, profile map[string]string, aliasIDs []string) (hardFail bool) {
	for _, id := range effectiveModelIDs(profile, aliasIDs) {
		backend, ok := routes[id]
		if !ok || strings.HasPrefix(backend, "chatgpt/") {
			continue
		}
		fmt.Fprintf(out, "profile %q: routing cross-check: %q resolves to %q, not a chatgpt/ lane — subscription mode requires chatgpt/ routing\n", name, id, backend)
		hardFail = true
	}
	return hardFail
}

// keyModeRoutingCrossCheck is A11's symmetric key-mode direction (F9/K3; issue #693 Phase 6
// K16/AC-11(iv)): the caller only reaches this function when the persisted gatewayAuthMode record
// says api-key, so a gateway advertising a chatgpt/ lane for this profile is a HARD failure naming
// the id and lane — unconditionally. A stray subscription handle sitting on disk from a prior mode
// (D10) must never suppress this: AC-11(iv) is exactly "the routing cross-check ... is never
// silently skipped merely because the prior mode's credential exists." openai/ lanes are correct
// for key mode and pass silently, keeping a clean api-key profile a true no-op for this stage.
func keyModeRoutingCrossCheck(out io.Writer, name string, routes map[string]string, profile map[string]string, aliasIDs []string) (hardFail bool) {
	for _, id := range effectiveModelIDs(profile, aliasIDs) {
		backend, ok := routes[id]
		if !ok || !strings.HasPrefix(backend, "chatgpt/") {
			continue
		}
		fmt.Fprintf(out, "profile %q: routing cross-check: %q routes to %q, a subscription lane, but the persisted gateway auth mode is api-key — a subscription handle alone does not authorize this routing; run `af gateway auth import` after `codex login` and switch modes, or reroute the gateway through an openai/ lane\n", name, id, backend)
		hardFail = true
	}
	return hardFail
}

// anyLaneHasPrefix reports whether any of ids resolves, in the observed /model/info routing, to a
// backend under prefix — the online lane signal in INV-2's mode derivation.
func anyLaneHasPrefix(routes map[string]string, ids []string, prefix string) bool {
	for _, id := range ids {
		if backend, ok := routes[id]; ok && strings.HasPrefix(backend, prefix) {
			return true
		}
	}
	return false
}

// dispatchMapsModel reports whether profile is named by any dispatch.json mapping or cron (D15:
// both slices count — the advisory is informational, so a false positive from a cron-only mapping
// costs nothing while a false negative defeats its purpose).
func dispatchMapsModel(cfg *config.DispatchConfig, name string) bool {
	if cfg == nil {
		return false
	}
	for _, m := range cfg.Mappings {
		if m.Model == name {
			return true
		}
	}
	for _, c := range cfg.Crons {
		if c.Model == name {
			return true
		}
	}
	return false
}

// fleetScaleAdvisory prints one informational line (D11: per-profile, never deduplicated across
// reasons) when a subscription profile is scaled across a fleet — models.json's own .default or a
// dispatch.json mapping/cron — so an operator sees the shared-plan-quota cost before dispatching
// broadly. Advisory only: it never affects checkProfile's hard-failure count or the check's exit
// code (D19).
func fleetScaleAdvisory(out io.Writer, name string, isDefault bool, dispatchCfg *config.DispatchConfig) {
	if !isDefault && !dispatchMapsModel(dispatchCfg, name) {
		return
	}
	fmt.Fprintf(out, "profile %q: fleet-scale advisory: this ChatGPT-subscription profile is scaled across the fleet (default or dispatch-mapped) — sessions against it spend shared plan quota; monitor usage before dispatching broadly\n", name)
}

// liveSmokeRows sends one minimal request per row that listed and demotes any row the gateway
// refuses, so the caller counts and records one verdict per class whichever stages ran. Rows that
// already failed structurally are skipped — there is nothing to ask for — as is the fable family row
// when no concrete id is known, since a wildcard is not a model name.
func liveSmokeRows(out io.Writer, name, base, secret string, rows []coverageRow) {
	for i := range rows {
		row := &rows[i]
		if !row.served || row.model == "" || strings.Contains(row.model, "*") {
			continue
		}
		if err := liveSmokeModel(base, secret, row.model); err != nil {
			// A deadline exceeded is a DISTINCT verdict from NOT SERVED (issue #686 K3/AC-3): the
			// gateway may be reachable and correctly configured but simply slower than
			// liveSmokeDeadline on this turn, which "not served" would misreport as a routing
			// problem.
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Fprintf(out, "profile %q: --live %s → %q: timed out after %s — %v\n", name, row.class, row.model, liveSmokeDeadline, err)
			} else {
				fmt.Fprintf(out, "profile %q: --live %s → %q: NOT SERVED — %v\n", name, row.class, row.model, err)
			}
			row.served = false
			continue
		}
		fmt.Fprintf(out, "profile %q: --live %s → %q: answered\n", name, row.class, row.model)
	}
}

// recordNoMeasurement replaces any prior record with one carrying no verdicts. A check that never
// got a served list measured nothing, and leaving the last successful run's record standing would
// have every later launch keep reporting coverage for a gateway this run could not even reach.
func recordNoMeasurement(out io.Writer, root, name string) {
	if err := writeModelCoverageRecord(root, modelCoverageRecord{
		Profile:   name,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		fmt.Fprintf(out, "profile %q: warning: could not clear the superseded coverage record: %v\n", name, err)
	}
}

// smokeSystemPrompt rides along as a block-array system prompt because that is the shape Claude
// Code sends on every turn. A bare user message is the one shape a broken translation layer still
// answers: LiteLLM 1.93.0's ChatGPT/Codex route answers "ping" and rejects every real session with
// "System messages are not allowed", so a probe without it certified a gateway no agent could use.
const smokeSystemPrompt = "You are a connectivity probe. Answer with one word."

// smokeBodyLimit caps how much of a probe response is read for its verdict and error text.
const smokeBodyLimit = 64 << 10

// doLiveSmokeRequest builds and sends one streamed /v1/messages turn in the shape a Claude Code
// session sends, shared by liveSmokeModel's per-class sweep and firstProbe's single-shot pre-flight
// (issue #693 K14) so both send byte-identical request shapes through the one modelsMessagesDo
// seam. It streams because sessions stream, and a gateway's non-streaming path can fail where its
// streaming path works (LiteLLM #37039). max_tokens is 16 rather than 1 because a model routed
// through OpenAI's Responses API rejects a lower max_output_tokens outright, which would read as an
// unserved class.
func doLiveSmokeRequest(ctx context.Context, base, secret, model string) (*http.Response, error) {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 16,
		"stream":     true,
		"system":     []map[string]string{{"type": "text", "text": smokeSystemPrompt}},
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	return modelsMessagesDo(req)
}

// liveSmokeModel asks the gateway for the smallest possible completion and reports whether it
// answered with a message. Kept as a thin wrapper over doLiveSmokeRequest, supplying
// liveSmokeDeadline, so --live's existing generic-error return and every STAYS test pinning its
// wording (config_models_subscription_test.go, model_coverage_test.go) stay untouched.
func liveSmokeModel(base, secret, model string) error {
	ctx, cancel := context.WithTimeout(context.Background(), liveSmokeDeadline)
	defer cancel()
	resp, err := doLiveSmokeRequest(ctx, base, secret, model)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := readSmokeBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /v1/messages returned %s%s", resp.Status, gatewayErrorSuffix(body))
	}
	return checkStreamedMessage(body)
}

func readSmokeBody(resp *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, smokeBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("POST /v1/messages returned %s but its body could not be read: %w", resp.Status, err)
	}
	return body, nil
}

// checkStreamedMessage accepts a body only when it is a completed streamed Anthropic message
// (message_start through message_stop). A JSON message with no stream is refused too: a session
// asks to stream, so a gateway that ignores that would not serve it either. An error event carries
// the gateway's own words into the verdict, since that text is what names an upstream defect.
func checkStreamedMessage(body []byte) error {
	sawStart, sawStop := false, false
	for _, line := range strings.Split(string(body), "\n") {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var event struct {
			Type  string `json:"type"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &event) != nil {
			continue
		}
		switch event.Type {
		case "message_start":
			sawStart = true
		case "message_stop":
			sawStop = true
		case "error":
			if event.Error.Message == "" {
				event.Error.Message = strings.TrimSpace(payload)
			}
			return fmt.Errorf("POST /v1/messages streamed an error: %s", event.Error.Message)
		}
	}
	if sawStart && sawStop {
		return nil
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Type != "" {
		return fmt.Errorf("POST /v1/messages answered type %q, want a streamed message%s", envelope.Type, gatewayErrorSuffix(body))
	}
	return fmt.Errorf("POST /v1/messages answered 200 without a completed streamed message (message_start=%t, message_stop=%t)", sawStart, sawStop)
}

// gatewayErrorSuffix pulls the gateway's own error text out of a response body so the verdict
// carries it: the status line alone said "500" for a translation bug whose message (LiteLLM's
// "Unknown items in responses API response") is the searchable key to the upstream issue. The
// JSON envelope's error.message is preferred; any other non-empty body is quoted on one line.
func gatewayErrorSuffix(body []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	text := ""
	if json.Unmarshal(body, &envelope) == nil {
		text = envelope.Error.Message
		if text == "" {
			text = envelope.Message
		}
	}
	if text == "" {
		text = strings.Join(strings.Fields(string(body)), " ")
	}
	if text == "" {
		return ""
	}
	if runes := []rune(text); len(runes) > 600 {
		text = string(runes[:600]) + "…"
	}
	return ": " + text
}

// firstProbeVerdict is --first's classified outcome (issue #693 K14, decisions D1-D3): a distinct,
// grep-able bucket per HTTP status class so a bash caller can branch on stdout text — Execute()
// (root.go:29-39) collapses every command failure to exit code 1, so the verdict cannot travel
// through the exit code. quickstart.sh's forced-relogin ladder anchors on the printed verdict
// token — `grep -qE -- '--first → (AUTH|TIMEOUT)'` — so only these two buckets drive a re-login and
// an unrelated "auth" substring (e.g. ANTHROPIC_AUTH_TOKEN in an error) cannot; keep the AUTH and
// TIMEOUT names and runFirstProbe's `--first → ` prefix exactly as printed.
type firstProbeVerdict string

const (
	firstProbeOK          firstProbeVerdict = "OK"
	firstProbeAuth        firstProbeVerdict = "AUTH"
	firstProbeNotFound    firstProbeVerdict = "NOT FOUND"
	firstProbeRateLimited firstProbeVerdict = "RATE LIMITED"
	firstProbeServerError firstProbeVerdict = "SERVER ERROR"
	firstProbeTimeout     firstProbeVerdict = "TIMEOUT"
	firstProbeUnexpected  firstProbeVerdict = "UNEXPECTED"
)

// firstProbe sends exactly one classified /v1/messages request against model, bounded by
// firstProbeDeadline (D1: a distinct 30s budget, never liveSmokeDeadline). A 429 gets exactly one
// backoff-and-retry (D1/D2/decision D1 backoff=1s); the retry's own response is classified by the
// same rule as a first-attempt response — a second 429 resolves to firstProbeRateLimited again
// (D2), not a distinct bucket. Any transport-level error (connect refused, DNS, the 30s deadline
// itself) — on the initial attempt or the retry — resolves to firstProbeTimeout.
//
// The classification deliberately lives ONLY here, not inside liveSmokeModel/liveSmokeRows:
// liveSmokeRows' printed wording ("timed out"/"NOT SERVED") is pinned verbatim by
// TestLiveSmokeTimeoutIsNotNotServed and the model-coverage 400/os.ErrDeadlineExceeded tests — giving
// liveSmokeModel's non-200 branch a classified return would change what those tests observe. --live's
// per-class sweep and --first's single pre-flight probe answer different questions ("which classes
// does the gateway serve" vs. "is this endpoint reachable and authenticated at all"), so they keep
// distinct verdict vocabularies over the one shared doLiveSmokeRequest transport.
func firstProbe(base, secret, model string) (firstProbeVerdict, error) {
	ctx, cancel := context.WithTimeout(context.Background(), firstProbeDeadline)
	defer cancel()

	resp, err := doLiveSmokeRequest(ctx, base, secret, model)
	if err == nil && resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		time.Sleep(firstProbeRetryBackoff)
		resp, err = doLiveSmokeRequest(ctx, base, secret, model)
	}
	if err != nil {
		return firstProbeTimeout, err
	}
	defer resp.Body.Close()
	body, err := readSmokeBody(resp)
	if err != nil {
		return firstProbeUnexpected, err
	}
	statusErr := fmt.Errorf("POST /v1/messages returned %s%s", resp.Status, gatewayErrorSuffix(body))
	switch {
	case resp.StatusCode == http.StatusOK:
		if err := checkStreamedMessage(body); err != nil {
			return firstProbeUnexpected, err
		}
		return firstProbeOK, nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return firstProbeAuth, statusErr
	case resp.StatusCode == http.StatusNotFound:
		return firstProbeNotFound, statusErr
	case resp.StatusCode == http.StatusTooManyRequests:
		return firstProbeRateLimited, statusErr
	case resp.StatusCode >= http.StatusInternalServerError:
		return firstProbeServerError, statusErr
	default:
		return firstProbeUnexpected, statusErr
	}
}

// runFirstProbe is --first's dispatch path (D3/D4): a fast auth-shaped pre-check that bypasses
// checkProfile's httpProbe/routing-cross-check/per-class-row machinery entirely, since its whole
// purpose is "exactly one request," not a coverage sweep. Secret resolution mirrors checkProfile's
// own block (config_models.go's checkProfile) but stays local — --first never records a coverage
// verdict or writes to .runtime, it only prints one classified line and returns an error for a
// non-OK verdict so the CLI's binary exit code still fails the run.
func runFirstProbe(cmd *cobra.Command, root string, cfg *config.ModelsConfig, name string) error {
	profile, ok := cfg.Models[name]
	if !ok {
		return fmt.Errorf("unknown model profile %q: not defined in models.json", name)
	}
	base := profile[baseURLKey]
	if base == "" {
		return fmt.Errorf("profile %q: no ANTHROPIC_BASE_URL — nothing to probe", name)
	}
	secret := profile[authTokenKey]
	if strings.HasPrefix(secret, secretPrefix) {
		data, err := os.ReadFile(secretRefPath(root, secret))
		if err != nil {
			return fmt.Errorf("profile %q: secret reference ANTHROPIC_AUTH_TOKEN: %w", name, err)
		}
		secret = strings.TrimSpace(string(data))
	}

	verdict, probeErr := firstProbe(base, secret, profile[modelKey])
	out := cmd.OutOrStdout()
	if probeErr != nil {
		fmt.Fprintf(out, "profile %q: --first → %s — %v\n", name, verdict, probeErr)
	} else {
		fmt.Fprintf(out, "profile %q: --first → %s\n", name, verdict)
	}
	if verdict != firstProbeOK {
		return fmt.Errorf("profile %q: --first %s", name, verdict)
	}
	return nil
}

func runConfigModelsAttest(cmd *cobra.Command, args []string) error {
	if len(args) == 0 || args[0] == "" {
		return fmt.Errorf("usage: af config models attest <profile>")
	}
	profile := args[0]
	root, cfg, err := loadModelsForRead()
	if err != nil {
		return err
	}
	if _, ok := cfg.Models[profile]; !ok {
		return fmt.Errorf("unknown model profile %q: not defined in models.json", profile)
	}

	att := modelFitnessAttestation{
		Profile:    profile,
		AttestedBy: attestedBy(),
		AttestedAt: time.Now().UTC().Format(time.RFC3339),
		Stages:     map[string]string{"transport": "attested"},
	}
	if err := writeModelFitnessAttestation(root, att); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Attested model profile %q at %s\n", profile, modelFitnessPath(root, profile))
	return nil
}

// modelFitnessAttestation is the factory-root fitness marker. Its shape is
// implementer-defined; the design pins only "who / when / stage results". The
// launch interlock (hasFitnessAttestation) treats any file whose Profile matches as
// valid, so the record must at least carry the profile name.
type modelFitnessAttestation struct {
	Profile    string            `json:"profile"`
	AttestedBy string            `json:"attested_by"`
	AttestedAt string            `json:"attested_at"`
	Stages     map[string]string `json:"stages,omitempty"`
}

// hasFitnessAttestation reports whether a valid attestation exists for profile at the
// factory-root .runtime/model_fitness/<profile>.json. Fail-closed: any read/parse
// error or a profile mismatch counts as UNATTESTED, so a corrupt marker never grants
// fitness. Read by the selecting-launch interlock in resolveModelEnvForSession.
func hasFitnessAttestation(root, profile string) bool {
	data, err := os.ReadFile(modelFitnessPath(root, profile))
	if err != nil {
		return false
	}
	var att modelFitnessAttestation
	if err := json.Unmarshal(data, &att); err != nil {
		return false
	}
	return att.Profile == profile
}

// writeModelFitnessAttestation writes the attestation atomically to the factory-root
// .runtime/model_fitness dir (mirrors writeUpLastRun's MkdirAll + SaveModelsConfig's
// MarshalIndent + fsutil.WriteFileAtomic). The path is built inline — there is no
// RuntimeDir helper in internal/config.
func writeModelFitnessAttestation(root string, att modelFitnessAttestation) error {
	dir := filepath.Join(root, ".runtime", "model_fitness")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(att, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling attestation: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(modelFitnessPath(root, att.Profile), data, 0o644)
}

func modelFitnessPath(root, profile string) string {
	return filepath.Join(root, ".runtime", "model_fitness", profile+".json")
}

// readModelProfilesOnDisk returns the profile sets currently in models.json, or nil when the
// file is absent, unreadable, or not decodable.
//
// It deliberately does NOT go through LoadModelsConfig: the question is what the bytes on disk
// say, not whether they are valid. A hand-edited registry that fails validation is exactly what
// an operator is usually replacing, and treating it as unreadable there would make every save
// look like a wholesale rewrite.
//
// A nil return makes the caller treat every SUBMITTED profile as changed, which is the
// fail-closed answer for everything the write can vouch for. What it cannot see is a profile the
// write REMOVED — with no snapshot there is nothing to notice its absence against, so its
// attestation survives. That residue is bounded: a name no longer in the registry resolves to no
// launch, and the next save that reintroduces it reads as new and clears the attestation then.
func readModelProfilesOnDisk(root string) map[string]map[string]string {
	data, err := os.ReadFile(config.ModelsConfigPath(root))
	if err != nil {
		return nil
	}
	var cfg config.ModelsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return cfg.Models
}

// invalidateStaleFitnessAttestations removes the fitness attestation of every profile whose
// export set is not byte-for-byte what it was before the save — changed, newly added, or
// dropped. An attestation records that a profile's transport was verified fit, but the launch
// interlock matches it by NAME, and a name outlives the bytes it was earned on: without this a
// profile repointed at another gateway (or dropped and recreated) would launch on somebody
// else's verification. The coverage record already states the same rule for its own marker —
// a profile edited after it was measured is no longer the profile it measured.
//
// Called only after a successful write, so a rejected save leaves every attestation standing.
// A removal failure warns rather than failing the command: models.json is already written, and
// reporting a non-zero exit for it would misdescribe what happened. Not-exist is the normal
// case (most profiles were never attested) and is silent.
func invalidateStaleFitnessAttestations(root string, before, after map[string]map[string]string, warn io.Writer) {
	stale := make([]string, 0, len(before)+len(after))
	for name, profile := range after {
		if old, ok := before[name]; !ok || !maps.Equal(old, profile) {
			stale = append(stale, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)

	for _, name := range stale {
		// Profile names are not validated anywhere and are joined straight into a path, so a
		// name like "../x" resolves outside the attestation directory — and a sweep that runs
		// on every save would turn a stdin document into an arbitrary-file delete. Only a plain
		// file name may be removed. Such a profile CAN be attested (attest joins the same path),
		// so this leaves it permanently attested rather than merely unattestable; that is the
		// lesser harm, and the real fix is validating profile names where they are accepted.
		if name != filepath.Base(name) || name == "." || name == ".." {
			continue
		}
		if err := os.Remove(modelFitnessPath(root, name)); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(warn, "warning: model %q changed but its fitness attestation could not be cleared (%v); re-run `af config models attest %s` or remove %s by hand\n",
				name, err, name, modelFitnessPath(root, name))
		}
	}
}

// modelCoverageVerdict is one class's outcome. Model is the effective id the class would have
// requested — empty when the profile derives none, which is itself the unserved reason.
type modelCoverageVerdict struct {
	Class  string `json:"class"`
	Model  string `json:"model"`
	Served bool   `json:"served"`
}

// modelCoverageRecord carries what `af config models check` last observed for one endpoint profile,
// so a launch can report class coverage without probing anything — the launch path must never wait
// on a gateway that has not come up yet. Only model ids and their verdicts are stored — never the
// endpoint, never token material.
type modelCoverageRecord struct {
	V         int                    `json:"v"`
	Profile   string                 `json:"profile"`
	CheckedAt string                 `json:"checked_at"`
	Failing   int                    `json:"failing"`
	Classes   []modelCoverageVerdict `json:"classes"`
}

// readModelCoverageRecord returns the recorded verdicts for a profile. Fail-closed in the same sense
// as hasFitnessAttestation: an absent, unreadable, corrupt, mismatched or unrecognised-version
// record all mean "nothing was measured", which the launch reports as a warning. A forged record can
// therefore at most suppress a warning it could not have earned — it can never assert coverage the
// gateway does not have, because nothing downstream trusts the record for anything but reporting.
func readModelCoverageRecord(root, profile string) (modelCoverageRecord, bool) {
	data, err := os.ReadFile(modelCoveragePath(root, profile))
	if err != nil {
		return modelCoverageRecord{}, false
	}
	var rec modelCoverageRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return modelCoverageRecord{}, false
	}
	if rec.V != modelCoverageVersion || rec.Profile != profile {
		return modelCoverageRecord{}, false
	}
	return rec, true
}

// writeModelCoverageRecord stamps the schema version and writes atomically, mirroring
// writeModelFitnessAttestation. The version is stamped here rather than by the caller so no writer
// can record a shape it did not produce.
func writeModelCoverageRecord(root string, rec modelCoverageRecord) error {
	dir := filepath.Join(root, ".runtime", "model_coverage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rec.V = modelCoverageVersion
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling coverage record: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(modelCoveragePath(root, rec.Profile), data, 0o644)
}

func modelCoveragePath(root, profile string) string {
	return filepath.Join(root, ".runtime", "model_coverage", profile+".json")
}

// secretRefPath resolves a file: secret reference to a path. A relative path is taken
// against root (the factory root), matching the Phase-2 emission deref; an absolute
// path is used as-is. Shared by the launch preflight (resolveModelEnvForSession) and
// `check`.
func secretRefPath(root, ref string) string {
	path := strings.TrimPrefix(ref, secretPrefix)
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

// displayModelValue applies the T-2 redaction contract to a single export value: a
// literal ANTHROPIC_AUTH_TOKEN becomes ****, a file: reference prints as-is, and every
// other key prints verbatim (ANTHROPIC_API_KEY is validated to always be empty).
func displayModelValue(key, val string) string {
	if key == authTokenKey && val != "" && !strings.HasPrefix(val, secretPrefix) {
		return redactedToken
	}
	return val
}

// explainAgentPrecedence describes which models.json level an agent resolves through
// (agents map > default). A per-launch --model or a .runtime/model_override marker can
// still override at launch time — noted so the explanation is not read as absolute.
func explainAgentPrecedence(cfg *config.ModelsConfig, agent string) string {
	if m, ok := cfg.Agents[agent]; ok {
		return fmt.Sprintf("agent %q resolves to profile %q (via the agents map; a per-launch --model still overrides)", agent, m)
	}
	if cfg.Default != "" {
		return fmt.Sprintf("agent %q resolves to profile %q (via default; a per-launch --model still overrides)", agent, cfg.Default)
	}
	return fmt.Sprintf("agent %q has no models.json selection (falls back to the global default model)", agent)
}

func attestedBy() string {
	for _, k := range []string{"AF_ACTOR", "USER", "LOGNAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return "unknown"
}

// loadModelsForRead is the read-only prologue shared by show/check/attest: resolve the
// factory root from the cwd, then LoadModelsConfig (NO stdin-decode, NO Save — that is
// the write path runConfigModelsSet uses).
func loadModelsForRead() (string, *config.ModelsConfig, error) {
	wd, err := getWd()
	if err != nil {
		return "", nil, err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return "", nil, err
	}
	cfg, err := config.LoadModelsConfig(root)
	if err != nil {
		return "", nil, err
	}
	return root, cfg, nil
}

func modelIDPresent(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// sortedMapKeys returns the map keys sorted, for deterministic output.
func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
