package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	configModelsShowAgent string
	configModelsCheckLive bool
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
/v1/messages request per served class; that performs real, billable requests.

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
	configModelsCheckCmd.Flags().BoolVar(&configModelsCheckLive, "live", false, "Also send one minimal /v1/messages request per served class (real, billable requests)")
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

	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "af config models check — transport-level only (necessary, not sufficient for fitness).")

	aliasIDs := directProfileClaudeIDs(cfg)
	hardFailures := 0
	for _, name := range names {
		if checkProfile(out, root, name, cfg.Models[name], aliasIDs) {
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
func checkProfile(out io.Writer, root, name string, profile map[string]string, aliasIDs []string) (hardFailure bool) {
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
		Profile:    name,
		CheckedAt:  time.Now().UTC().Format(time.RFC3339),
		ServedHash: servedListHash(ids),
		Failing:    failing,
		Classes:    verdicts,
	}); err != nil {
		fmt.Fprintf(out, "profile %q: warning: could not record the coverage verdicts: %v\n", name, err)
	}
	return failing > 0
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
			fmt.Fprintf(out, "profile %q: --live %s → %q: NOT SERVED — %v\n", name, row.class, row.model, err)
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

// liveSmokeModel asks the gateway for the smallest possible completion and reports whether it
// answered with a message. max_tokens is 16 rather than 1 because a model routed through OpenAI's
// Responses API rejects a lower max_output_tokens outright, which would read as an unserved class;
// quickstart.sh's own smoke test uses the same floor for the same reason.
func liveSmokeModel(base, secret, model string) error {
	ctx, cancel := context.WithTimeout(context.Background(), modelsProbeTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 16,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := modelsMessagesDo(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST /v1/messages returned %s", resp.Status)
	}
	var answer struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return err
	}
	if answer.Type != "message" {
		return fmt.Errorf("POST /v1/messages answered type %q, want a message", answer.Type)
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
// fitness. Read by the selecting-launch interlock in resolveLaunchModelEnv.
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
// on a gateway that has not come up yet.
//
// ServedHash fingerprints the gateway's advertised model list. It is not read by anything today; it
// is recorded because "the verdicts are the same but the gateway's inventory changed underneath
// them" is the question an operator asks after a litellm reload, and the answer has to have been
// captured at check time or it is gone. Only model ids and their verdicts are stored — never the
// endpoint, never token material.
type modelCoverageRecord struct {
	V          int                    `json:"v"`
	Profile    string                 `json:"profile"`
	CheckedAt  string                 `json:"checked_at"`
	ServedHash string                 `json:"served_hash"`
	Failing    int                    `json:"failing"`
	Classes    []modelCoverageVerdict `json:"classes"`
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

// servedListHash fingerprints a gateway's advertised ids, order-independently so a gateway that
// merely reorders its model_list does not read as a changed inventory.
func servedListHash(ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

// secretRefPath resolves a file: secret reference to a path. A relative path is taken
// against root (the factory root), matching the Phase-2 emission deref; an absolute
// path is used as-is. Shared by the launch preflight (resolveLaunchModelEnv) and
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
