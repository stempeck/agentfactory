package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	modelsProbeTimeout = 5 * time.Second
)

var configModelsShowAgent string

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
file: secret (exists / non-empty / mode), reach ANTHROPIC_BASE_URL, and confirm the
profile's model id appears in GET /v1/models (warn, not block). This is transport-level
only — necessary, not sufficient, for fitness. Never prints token material.`,
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

	hardFailures := 0
	for _, name := range names {
		if checkProfile(out, root, name, cfg.Models[name]) {
			hardFailures++
		}
	}
	if hardFailures > 0 {
		return fmt.Errorf("%d transport check(s) failed", hardFailures)
	}
	return nil
}

// checkProfile probes one profile and returns true if a HARD failure (missing secret
// or unreachable endpoint) occurred. Soft issues (permissive mode, git-tracked secret,
// model id absent) are warned but not counted. Never prints token material.
func checkProfile(out io.Writer, root, name string, profile map[string]string) (hardFailure bool) {
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
			return true
		}
		secret = strings.TrimSpace(string(data))
	}

	ids, probeErr := httpProbe(base, secret)
	if probeErr != nil {
		fmt.Fprintf(out, "profile %q: ANTHROPIC_BASE_URL %s unreachable: %v\n", name, base, probeErr)
		return true
	}
	if want := profile[modelKey]; want != "" && !modelIDPresent(ids, want) {
		fmt.Fprintf(out, "profile %q: model %q not in GET /v1/models response (gateway may need registration)\n", name, want)
		return false
	}
	fmt.Fprintf(out, "profile %q: reachable; model %q present at %s\n", name, profile[modelKey], base)
	return false
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
		return "****"
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
