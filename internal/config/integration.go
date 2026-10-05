package config

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

const IntegrationManifestFile = "af-integration.toml"

// IntegrationManifest mirrors the design's manifest sample (design L282-317); it is decoded strictly.
// LoadIntegrationManifest fills the sample's defaults, so Scope, the two timeouts, FreshFor and
// HookFailMode are never empty on a loaded manifest.
type IntegrationManifest struct {
	Name        string               `toml:"name"`
	Description string               `toml:"description"`
	Scope       string               `toml:"scope"`
	Upstream    *IntegrationUpstream `toml:"upstream"`
	Install     *IntegrationInstall  `toml:"install"`
	Check       *IntegrationCheck    `toml:"check"`
	Service     *IntegrationService  `toml:"service"`
	Claude      *IntegrationClaude   `toml:"claude"`
	Env         map[string]string    `toml:"env"`
}

type IntegrationUpstream struct {
	Repo   string `toml:"repo"`
	Commit string `toml:"commit"`
}

type IntegrationInstall struct {
	Run            string                `toml:"run"`
	Timeout        string                `toml:"timeout"`
	ExternalWrites []string              `toml:"external_writes"`
	Shared         bool                  `toml:"shared"`
	Artifacts      []IntegrationArtifact `toml:"artifacts"`
}

type IntegrationArtifact struct {
	URL    string `toml:"url" json:"url"`
	SHA256 string `toml:"sha256" json:"sha256"`
}

type IntegrationCheck struct {
	Run      string `toml:"run"`
	Timeout  string `toml:"timeout"`
	FreshFor string `toml:"fresh_for"`
}

type IntegrationService struct {
	Session    string `toml:"session"`
	Run        string `toml:"run"`
	Probe      string `toml:"probe"`
	HealthzURL string `toml:"healthz_url"`
}

type IntegrationClaude struct {
	Plugins      []string `toml:"plugins"`
	HookFailMode string   `toml:"hook_fail_mode"`
}

const (
	IntegrationScopeFormula = "formula"
	IntegrationScopeFactory = "factory"

	IntegrationProbeTmuxSession = "tmux-session"
	IntegrationProbeHTTPHealthz = "http-healthz"

	IntegrationHookFailOpen   = "open"
	IntegrationHookFailClosed = "closed"
)

// The design sample's defaults (design L282-317; D5 for the timeouts).
const (
	DefaultIntegrationInstallTimeout = "10m"
	defaultIntegrationCheckTimeout   = "30s"
	defaultIntegrationFreshFor       = "0"
)

// maxIntegrationClaudePlugins is the designer cap on Claude Code plugin dirs per integration (scale.md A1).
const maxIntegrationClaudePlugins = 8

// reservedServicePrefixes are refused as service-session prefixes (spec L206-208, B14): af- is the
// factory's own namespace (the tmux test guard panics on it), and telemetry/litellm are the backends
// af launches itself.
var reservedServicePrefixes = []string{"af-", "telemetry", "litellm"}

var upstreamCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

var artifactSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// integrationToolchainKeys are the [env] names that would change the toolchain an agent runs
// under. Derived from the config-owned shell-critical list plus the plugin channel's keys (Lift B).
var integrationToolchainKeys = func() map[string]bool {
	keys := map[string]bool{envClaudePluginDirs: true, envClaudeConfigDir: true}
	for _, k := range ShellCriticalEnvVars {
		keys[k] = true
	}
	return keys
}()

var integrationRedirectKeys = func() map[string]bool {
	keys := map[string]bool{}
	for _, k := range RedirectFamilyEnvVars {
		keys[k] = true
	}
	return keys
}()

// LoadIntegrationManifest reads <pluginDir>/af-integration.toml; the manifest name must equal filepath.Base(pluginDir).
func LoadIntegrationManifest(pluginDir string) (*IntegrationManifest, error) {
	return loadIntegrationManifest(pluginDir, filepath.Base(pluginDir), "its directory name")
}

// LoadIntegrationManifestNamed validates dir's manifest against wantName, for staged
// (.tmp-*) and snapshot (<sha>) dirs whose base name is not the integration name.
func LoadIntegrationManifestNamed(dir, wantName string) (*IntegrationManifest, error) {
	return loadIntegrationManifest(dir, wantName, "the expected integration")
}

// loadIntegrationManifest decodes strictly and validates the whole manifest (spec L186-231). It
// returns nil on every refusal, never a partially checked manifest.
func loadIntegrationManifest(dir, wantName, wantWhat string) (*IntegrationManifest, error) {
	var m IntegrationManifest
	md, err := toml.DecodeFile(filepath.Join(dir, IntegrationManifestFile), &m)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", IntegrationManifestFile, err)
	}
	if err := unknownManifestKeys(md.Undecoded()); err != nil {
		return nil, err
	}
	if err := validateIntegrationManifest(&m, wantName, wantWhat); err != nil {
		return nil, fmt.Errorf("%s: %w", IntegrationManifestFile, err)
	}
	return &m, nil
}

func validateIntegrationManifest(m *IntegrationManifest, wantName, wantWhat string) error {
	if err := ValidateAgentName(m.Name); err != nil {
		return fmt.Errorf("name: %w", err)
	}
	if m.Name != wantName {
		return fmt.Errorf("name %q does not match %s %q", m.Name, wantWhat, wantName)
	}

	if m.Scope == "" {
		m.Scope = IntegrationScopeFormula
	}
	if m.Scope != IntegrationScopeFormula && m.Scope != IntegrationScopeFactory {
		return fmt.Errorf("scope %q must be %q or %q", m.Scope, IntegrationScopeFormula, IntegrationScopeFactory)
	}

	if u := m.Upstream; u != nil {
		if u.Repo == "" {
			return errors.New("[upstream] needs repo")
		}
		if !upstreamCommit.MatchString(u.Commit) {
			return fmt.Errorf("[upstream] commit %q must be a 40-character lowercase hex commit, never a branch or tag", u.Commit)
		}
	}

	if in := m.Install; in != nil {
		if err := validateRunPath("[install]", in.Run); err != nil {
			return err
		}
		if in.Timeout == "" {
			in.Timeout = DefaultIntegrationInstallTimeout
		}
		if err := validateIntegrationTimeout("[install]", in.Timeout); err != nil {
			return err
		}
		for _, w := range in.ExternalWrites {
			if err := validateExternalWrite(w); err != nil {
				return err
			}
		}
		for _, a := range in.Artifacts {
			if a.URL == "" {
				return errors.New("[[install.artifacts]] needs url")
			}
			if !artifactSHA256.MatchString(a.SHA256) {
				return fmt.Errorf("[[install.artifacts]] sha256 %q for %s must be 64 lowercase hex characters", a.SHA256, a.URL)
			}
		}
	}

	if c := m.Check; c != nil {
		if err := validateRunPath("[check]", c.Run); err != nil {
			return err
		}
		if c.Timeout == "" {
			c.Timeout = defaultIntegrationCheckTimeout
		}
		if err := validateIntegrationTimeout("[check]", c.Timeout); err != nil {
			return err
		}
		if c.FreshFor == "" {
			c.FreshFor = defaultIntegrationFreshFor
		}
		if _, err := ParseIntegrationDuration(c.FreshFor); err != nil {
			return fmt.Errorf("[check] fresh_for: %w", err)
		}
	}

	if s := m.Service; s != nil {
		if err := validateIntegrationService(s); err != nil {
			return err
		}
	}

	if c := m.Claude; c != nil {
		if len(c.Plugins) > maxIntegrationClaudePlugins {
			return fmt.Errorf("[claude] plugins declares %d dirs; at most %d are allowed", len(c.Plugins), maxIntegrationClaudePlugins)
		}
		for _, p := range c.Plugins {
			if err := validateDeclaredPath("[claude] plugins", p); err != nil {
				return err
			}
		}
		if c.HookFailMode == "" {
			c.HookFailMode = IntegrationHookFailOpen
		}
		if c.HookFailMode != IntegrationHookFailOpen && c.HookFailMode != IntegrationHookFailClosed {
			return fmt.Errorf("[claude] hook_fail_mode %q must be %q or %q", c.HookFailMode, IntegrationHookFailOpen, IntegrationHookFailClosed)
		}
	}

	return validateIntegrationEnv(m.Name, m.Env)
}

func validateIntegrationService(s *IntegrationService) error {
	if s.Session == "" {
		return errors.New("[service] needs session")
	}
	for _, prefix := range reservedServicePrefixes {
		if strings.HasPrefix(s.Session, prefix) {
			return fmt.Errorf("[service] session %q may not start with %q (reserved for af's own sessions)", s.Session, prefix)
		}
	}
	// D29: tmux rewrites '.' and ':' in session names, so an exact has-session would never match.
	if err := ValidateAgentName(s.Session); err != nil {
		return fmt.Errorf("[service] session %q: %w", s.Session, err)
	}
	if err := validateRunPath("[service]", s.Run); err != nil {
		return err
	}
	switch s.Probe {
	case IntegrationProbeTmuxSession:
		if s.HealthzURL != "" {
			return fmt.Errorf("[service] healthz_url is only valid with probe = %q", IntegrationProbeHTTPHealthz)
		}
	case IntegrationProbeHTTPHealthz:
		if s.HealthzURL == "" {
			return fmt.Errorf("[service] probe = %q needs healthz_url", IntegrationProbeHTTPHealthz)
		}
		u, err := url.Parse(s.HealthzURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !IsLoopbackEndpoint(s.HealthzURL) {
			return fmt.Errorf("[service] healthz_url %q must be an http(s) URL on a loopback host", s.HealthzURL)
		}
	case "":
		return fmt.Errorf("[service] needs probe = %q or %q", IntegrationProbeTmuxSession, IntegrationProbeHTTPHealthz)
	default:
		return fmt.Errorf("[service] needs probe = %q or %q, got %q", IntegrationProbeTmuxSession, IntegrationProbeHTTPHealthz, s.Probe)
	}
	return nil
}

func validateRunPath(table, run string) error {
	if run == "" {
		return fmt.Errorf("%s needs run", table)
	}
	return validateDeclaredPath(table+" run", run)
}

// validateDeclaredPath is the lexical half of declared-path containment; install resolves
// symlinks against the real plugin dir (spec L556-557).
func validateDeclaredPath(field, p string) error {
	if _, err := cleanDeclaredPath(p); err != nil {
		return fmt.Errorf("%s %q must be relative and stay inside the plugin dir", field, p)
	}
	return nil
}

// validateIntegrationTimeout refuses "0" (D5): an unbounded install or check is the failure the
// timeout exists to prevent.
func validateIntegrationTimeout(table, s string) error {
	d, err := ParseIntegrationDuration(s)
	if err != nil {
		return fmt.Errorf("%s timeout: %w", table, err)
	}
	if d == 0 {
		return fmt.Errorf("%s timeout %q must be positive", table, s)
	}
	return nil
}

// validateExternalWrite accepts a ~/-relative or absolute path with no glob; a trailing / marks a dir (D34).
func validateExternalWrite(w string) error {
	if !strings.HasPrefix(w, "~/") && !strings.HasPrefix(w, "/") {
		return fmt.Errorf("[install] external_writes %q must be an absolute or ~/-relative path", w)
	}
	if strings.ContainsAny(w, "*?[") {
		return fmt.Errorf("[install] external_writes %q may not be a glob", w)
	}
	return nil
}

// validateIntegrationEnv applies the [env] rules (spec L211-231). Denied keys are all named at once,
// sorted within each category, in the category order identity, telemetry, gateway, toolchain,
// redirect, af launch key: design L345's two-key text names the toolchain key before the redirect key (D68).
func validateIntegrationEnv(name string, env map[string]string) error {
	keys := slices.Sorted(maps.Keys(env))
	for _, k := range keys {
		if !IsValidEnvKeyName(k) {
			return fmt.Errorf("[env] key %q is not a valid environment-variable name (must match [A-Za-z_][A-Za-z0-9_]*)", k)
		}
	}

	categories := []struct {
		denied func(string) bool
		clause func(string) string
	}{
		{func(k string) bool { return afIdentityKeys[k] }, deniedEnvClause("identity key")},
		{func(k string) bool { return afTelemetryKeys[k] }, deniedEnvClause("telemetry key")},
		{func(k string) bool { return afGatewayUpstreamKeys[k] }, deniedEnvClause("gateway credential")},
		{func(k string) bool { return integrationToolchainKeys[k] }, deniedEnvClause("toolchain key")},
		{func(k string) bool { return integrationRedirectKeys[k] }, func(k string) string { return k + " belongs to models.json profiles" }},
		{IsAFLaunchKey, deniedEnvClause("af launch key")},
	}
	var clauses []string
	for _, c := range categories {
		for _, k := range keys {
			if c.denied(k) {
				clauses = append(clauses, c.clause(k))
			}
		}
	}
	if len(clauses) > 0 {
		return errors.New("[env] " + strings.Join(clauses, "; "))
	}

	for _, k := range keys {
		v := env[k]
		if isSecretRef(v) {
			if err := validateSecretRefShape("[env]", k, v); err != nil {
				return err
			}
			if !integrationSecretRefAllowed(name, strings.TrimPrefix(v, secretRefPrefix)) {
				return fmt.Errorf("[env] %s: a file: reference must stay under %s/, the plugin dir %s/ or %s/ (resolved against the factory root)", k, secretsRefRoot(), pluginRefRoot(name), integrationRefRoot(name))
			}
			continue
		}
		if looksLikeCredential(v) {
			return fmt.Errorf("[env] %s looks like a literal credential; store it under %s/ and use a \"file:<path>\" reference", k, secretsRefRoot())
		}
	}
	return nil
}

func deniedEnvClause(category string) func(string) string {
	return func(k string) string { return "may not set " + k + " (" + category + ")" }
}

// integrationSecretRefAllowed reports whether a file: ref (resolved against the factory root, D10)
// stays inside the factory secrets dir, this plugin's dir (spec L227-228), or its consumed snapshots.
func integrationSecretRefAllowed(name, ref string) bool {
	rel, err := cleanDeclaredPath(ref)
	if err != nil {
		return false
	}
	return strings.HasPrefix(rel, secretsRefRoot()+"/") || strings.HasPrefix(rel, pluginRefRoot(name)+"/") || strings.HasPrefix(rel, integrationRefRoot(name)+"/")
}

func secretsRefRoot() string { return path.Join(dotDir, "secrets") }

func pluginRefRoot(name string) string {
	return filepath.ToSlash(PluginsDir("")) + "/" + name
}

func integrationRefRoot(name string) string {
	return filepath.ToSlash(IntegrationsDir("")) + "/" + name
}

// unknownManifestKeys reports every undecoded key once: a key under an unknown table is covered by
// the table's own report. Each report suggests the nearest known key at its level (edit distance ≤ 2).
func unknownManifestKeys(undecoded []toml.Key) error {
	var reports []string
	for _, k := range undecoded {
		if slices.ContainsFunc(undecoded, func(o toml.Key) bool { return len(o) < len(k) && slices.Equal(o, k[:len(o)]) }) {
			continue
		}
		leaf, parent := k[len(k)-1], k[:len(k)-1]
		r := fmt.Sprintf("unknown key %q", leaf)
		if len(parent) > 0 {
			r += fmt.Sprintf(" in [%s]", strings.Join(parent, "."))
		}
		if s := nearestKey(leaf, manifestKeysAt(parent)); s != "" {
			r += fmt.Sprintf(" (did you mean %q?)", s)
		}
		reports = append(reports, r)
	}
	if len(reports) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", IntegrationManifestFile, strings.Join(reports, "; "))
}

// manifestKeysAt lists the toml keys the manifest schema accepts under the table at parent.
func manifestKeysAt(parent []string) []string {
	t := reflect.TypeOf(IntegrationManifest{})
	for _, name := range parent {
		f, ok := tomlField(t, name)
		if !ok {
			return nil
		}
		t = f.Type
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return nil
		}
	}
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		keys = append(keys, t.Field(i).Tag.Get("toml"))
	}
	return keys
}

func tomlField(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Tag.Get("toml") == name {
			return t.Field(i), true
		}
	}
	return reflect.StructField{}, false
}

func nearestKey(key string, known []string) string {
	best, bestDist := "", 3
	for _, k := range known {
		if d := editDistance(key, k); d < bestDist {
			best, bestDist = k, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b, by bytes (manifest keys are ASCII).
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
