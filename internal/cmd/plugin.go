package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/templates"
)

// Agent Plugin Repositories (issue #538, Phase 3). An operator clones a plugin repo
// into .agentfactory/store/plugins/<name>/ (inert acquisition, AC-2) and then runs
// `af plugin install <name>` — the explicit consent verb (C-1/ADR-020: registration
// is consent). There is NO interactive prompt anywhere in this file; the verb IS the
// consent (ADR-014, enforced by plugin_adr014_absence_test.go).

// maxPluginFormulaBytes caps a staged formula file's size BEFORE it is parsed (T7,
// security.md L83). The value is a sane bound, not a spec-pinned number — 1 MiB is
// far above any real formula yet defends against a zip-bomb/absurd TOML. Named so it
// is greppable and test-pinnable.
const maxPluginFormulaBytes = 1 << 20 // 1 MiB

// Per-formula status enum surfaced by `af plugin list` (api.md L25-26 + design-doc K4).
const (
	pluginStatusOK              = "ok"
	pluginStatusCollideStore    = "collides-with-store-formula"
	pluginStatusCollideManual   = "collides-with-manual-agent"
	pluginStatusInvalidName     = "invalid-name"
	pluginStatusMissingSkills   = "missing-skills"
	pluginStatusParseError      = "parse-error"
	pluginStatusOutOfContract   = "out-of-contract"
	pluginStatusCollideEmbedded = "collides-with-embedded-agent"
	pluginStatusNameMismatch    = "name-mismatch"
	pluginStatusChanged         = "changed"
	pluginStatusRemoved         = "removed"
)

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Agent plugin repository management (issue #538)",
	Long: `Manage plugin repositories acquired under .agentfactory/store/plugins/: formula
plugins (agents) and integrations (af-integration.toml: Claude Code plugins, env, services).

Acquisition (git clone, or ` + "`af plugin acquire <name>`" + ` for an integration embedded in af)
is inert — it changes nothing. ` + "`af plugin install <name>`" + ` is the explicit consent verb:
it validates, stages, records, and (for formulas) rebuilds and verifies. check runs an
integration's health check, list and verify report, and remove uninstalls an integration.`,
}

var pluginListCmd = &cobra.Command{
	Use:   "list",
	Short: "Enumerate acquired plugins and their per-formula install status (read-only)",
	Long: `Enumerate acquired plugins and their per-formula install status (read-only).

A plugin directory that cannot be installed (an invalid name, an unreadable dir) is
listed as NOT INSTALLABLE with the reason.

--json prints an array ([] when there is nothing, never null); a plugin that is not
installable carries an "error" field. An infrastructure error prints
{"state":"error","error":"..."} and still exits 0.`,
	RunE: runPluginList,
}

var pluginInstallCmd = &cobra.Command{
	Use:   "install <plugin> [<plugin>...]",
	Short: "Validate, stage, record, rebuild, and verify one or more acquired plugins",
	Args:  cobra.MinimumNArgs(1),
	RunE:  pluginInstallDispatch,
}

var pluginVerifyCmd = &cobra.Command{
	Use:   "verify [<plugin>...]",
	Short: "Check plugin agents (with --all, every formula agent) are registered, embedded, and hash-clean",
	Long: `Check agents are registered in agents.json with a formula field, embedded in this
binary, and (for plugin agents) hash-clean against plugins.json.

  af plugin verify <plugin>...   the named plugins' agents
  af plugin verify               every recorded plugin's agents
  af plugin verify --all         every recorded plugin agent plus every formula agent in
                                 agents.json (non-plugin rows have plugin "" and no hash_clean)

Plugin names and --all are mutually exclusive.

A present but unloadable plugins.json (or, under --all, agents.json) is an error.
Exit status is non-zero on any FAIL row or error. --json adds "state": "ok", "fail" or
"error"; an error is printed as JSON with its message in "error".`,
	RunE: runPluginVerify,
}

func init() {
	pluginListCmd.Flags().Bool("json", false, "Emit JSON output")
	pluginVerifyCmd.Flags().Bool("json", false, "Emit JSON output")
	pluginVerifyCmd.Flags().Bool("all", false, "Verify every recorded plugin agent and every formula agent in agents.json")
	pluginInstallCmd.Flags().Bool("no-build", false, "Skip agent-gen-all.sh's duplicate build (quickstart.sh still rebuilds)")
	pluginInstallCmd.Flags().Bool("factory-wide", false, "Consent to a scope = \"factory\" integration that affects every agent session")
	pluginCheckCmd.Flags().Bool("json", false, "Emit JSON output")
	pluginCheckCmd.Flags().Bool("all", false, "Check every installed integration")

	pluginCmd.AddCommand(pluginAcquireCmd)
	pluginCmd.AddCommand(pluginCheckCmd)
	pluginCmd.AddCommand(pluginListCmd)
	pluginCmd.AddCommand(pluginInstallCmd)
	pluginCmd.AddCommand(pluginRemoveCmd)
	pluginCmd.AddCommand(pluginVerifyCmd)
	pluginCmd.AddCommand(pluginGuardEventCmd)
	rootCmd.AddCommand(pluginCmd)
}

// hashHex is the sha256→hex one-liner (mirrors web/internal/formulas/store.go:249,
// which lives in a separate module and is unexported, so it is copied not imported).
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// currentBinaryPath reports which binary is answering (done.go:394-397 precedent).
func currentBinaryPath() string {
	p, err := os.Executable()
	if err != nil {
		p, _ = exec.LookPath("af")
	}
	return p
}

// runGitProvenance reads best-effort git origin + HEAD from the plugin's clone dir
// (checkpoint.go:153 idiom — shell out, err→empty, never fail the caller). A seam so
// tests need not build a real git repo. Provenance is recorded only when dir is the
// top level of its own repo: otherwise git walks up and would report the enclosing
// factory's origin and HEAD as the plugin's, so both come back empty. Source is also
// empty for an origin-less clone.
var runGitProvenance = func(dir string) (source, commit string) {
	if !isOwnGitToplevel(dir) {
		return "", ""
	}
	return gitOut(dir, "config", "--get", "remote.origin.url"), gitOut(dir, "rev-parse", "HEAD")
}

// pluginProvenance prefers acquire's marker (D7), except in a git clone: a third-party repo
// could ship the marker to pass itself off as embedded in af.
func pluginProvenance(dir string) (source, commit string, embedded bool) {
	if m, ok := readAcquireMarker(dir); ok && !isOwnGitToplevel(dir) {
		return m.Source, m.Commit, true
	}
	source, commit = runGitProvenance(dir)
	return redactRemoteURL(source), commit, false
}

func isOwnGitToplevel(dir string) bool {
	top := gitOut(dir, "rev-parse", "--show-toplevel")
	if top == "" {
		return false
	}
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return false
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return realTop == realDir
}

// gitOutScrubbedEnv names the variables that repoint git at another repository; an
// exported GIT_DIR would make every provenance call answer for that repo instead of dir.
var gitOutScrubbedEnv = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"}

func gitOut(dir string, args ...string) string {
	c := exec.Command("git", args...)
	c.Dir = dir
	for _, kv := range os.Environ() {
		if !slices.ContainsFunc(gitOutScrubbedEnv, func(k string) bool { return strings.HasPrefix(kv, k+"=") }) {
			c.Env = append(c.Env, kv)
		}
	}
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// redactRemoteURL strips credentials from a git remote before it is recorded in the
// git-tracked plugins.json or printed. scheme:// remotes drop userinfo, query and
// fragment (tokens ride in either), and one that does not parse becomes "" rather than
// being recorded verbatim. scp-like remotes (user[:pw]@host:path) drop the userinfo
// prefix; url.Parse cannot be used for them because it rejects git@host:path outright.
// Local paths pass through unchanged.
func redactRemoteURL(remote string) string {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return ""
		}
		u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
		return u.String()
	}
	colon := strings.Index(remote, ":")
	slash := strings.Index(remote, "/")
	if colon < 0 || (slash >= 0 && slash < colon) {
		return remote
	}
	hostPathColon := strings.LastIndex(remote, ":")
	if at := strings.LastIndex(remote[:hostPathColon], "@"); at >= 0 {
		return remote[at+1:]
	}
	return remote
}

// ---- enumeration + validation (K3) ------------------------------------------

type pluginFormulaInfo struct {
	Stem   string // agent name (formula filename stem)
	File   string // <stem>.formula.toml, or a nested rel-path for out-of-contract
	SHA256 string
	Status string
	Detail string
	// MissingIntegrations is informational only: installing the formula is fine, running it is refused by
	// admission until they are installed, so it never becomes a Status.
	MissingIntegrations []string
	nonRegular          bool   // a top-level *.formula.toml that is a symlink/dir/device (T5)
	bytes               []byte // read-once content (top-level regular files only), for staging (K5)
}

type pluginInfo struct {
	Name     string
	Dir      string
	Source   string
	Commit   string
	Embedded bool                        // provenance came from acquire's marker (D7)
	Manifest *config.IntegrationManifest // nil unless the plugin ships af-integration.toml
	Formulas []pluginFormulaInfo
	// Removed stays out of Formulas: every Formulas consumer stages, counts, or commits what
	// it sees, and a removed stem has no bytes to stage.
	Removed []string
}

// collisionUniverse is the four-way collision-sweep source set (security.md step 4):
// store formulas ∪ embedded-shipped set ∪ ALL agents.json entries (manual AND
// formula-backed) ∪ (the batch union, handled per-install by the caller). Built once
// per list/install so per-formula classification is a set lookup.
type collisionUniverse struct {
	storeStems    map[string]bool
	storeBytes    map[string][]byte
	shippedStems  map[string]bool
	manualAgents  map[string]bool
	formulaAgents map[string]bool
	manifest      *config.PluginsConfig
}

// builtinIdentities are the roles af embeds without shipping a formula for them — the set
// agent-gen-all.sh's orphan pass skips. Embedded-ness alone cannot mark an identity as taken:
// every factory on a host shares one af binary, which also embeds the plugins its siblings installed.
var builtinIdentities = map[string]bool{"manager": true, "supervisor": true}

func buildCollisionUniverse(root string) (*collisionUniverse, error) {
	u := &collisionUniverse{
		storeStems:    map[string]bool{},
		storeBytes:    map[string][]byte{},
		shippedStems:  map[string]bool{},
		manualAgents:  map[string]bool{},
		formulaAgents: map[string]bool{},
	}

	storeDir := config.FormulasDir(root)
	if entries, err := os.ReadDir(storeDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".formula.toml") {
				continue
			}
			stem := strings.TrimSuffix(e.Name(), ".formula.toml")
			u.storeStems[stem] = true
			if b, rerr := os.ReadFile(filepath.Join(storeDir, e.Name())); rerr == nil {
				u.storeBytes[stem] = b
			}
		}
	}

	if entries, err := formulasFS.ReadDir("install_formulas"); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".formula.toml") {
				continue // install.go:350 clone-detector idiom
			}
			u.shippedStems[strings.TrimSuffix(e.Name(), ".formula.toml")] = true
		}
	}

	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil && !errors.Is(err, config.ErrNotFound) {
		return nil, fmt.Errorf("loading %s: %w", agentsPath, err)
	}
	if agentsCfg != nil {
		for name, entry := range agentsCfg.Agents {
			if entry.Formula == "" {
				u.manualAgents[name] = true
			} else {
				u.formulaAgents[name] = true
			}
		}
	}

	manifest, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return nil, err
	}
	u.manifest = manifest
	return u, nil
}

// pluginOwnsFormula reports whether the manifest already records stem under plugin,
// and its recorded sha256 (the three-valued manifest side).
func (u *collisionUniverse) pluginOwnsFormula(plugin, stem string) (string, bool) {
	entry, ok := u.manifest.Plugins[plugin]
	if !ok {
		return "", false
	}
	for key, f := range entry.Formulas {
		if strings.TrimSuffix(key, ".formula.toml") == stem {
			return f.SHA256, true
		}
	}
	return "", false
}

// droppedStems returns, sorted, the stems plugins.json records under plugin that the
// clone no longer ships at its top level.
func (u *collisionUniverse) droppedStems(plugin string, shipped map[string]bool) []string {
	var dropped []string
	for key := range u.manifest.Plugins[plugin].Formulas {
		if stem := strings.TrimSuffix(key, ".formula.toml"); !shipped[stem] {
			dropped = append(dropped, stem)
		}
	}
	sort.Strings(dropped)
	return dropped
}

// otherPluginOwners returns, sorted and quoted, every recorded plugin other than plugin
// whose manifest entry records stem.
func (u *collisionUniverse) otherPluginOwners(plugin, stem string) []string {
	var owners []string
	for _, name := range slices.Sorted(maps.Keys(u.manifest.Plugins)) {
		if name == plugin {
			continue
		}
		if _, owned := u.pluginOwnsFormula(name, stem); owned {
			owners = append(owners, strconv.Quote(name))
		}
	}
	return owners
}

// enumeratePlugin lists a plugin's formulas and classifies each. It errors only when
// the plugin dir itself is absent (api.md L87). Per-formula problems become a Status.
func enumeratePlugin(root, pluginName string, u *collisionUniverse) (pluginInfo, error) {
	if err := config.ValidateAgentName(pluginName); err != nil {
		return pluginInfo{}, fmt.Errorf("plugin name %q is not a valid agent name (must match [a-zA-Z][a-zA-Z0-9_-]*)", pluginName)
	}
	dir := filepath.Join(config.PluginsDir(root), pluginName)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return pluginInfo{}, fmt.Errorf("plugin %q not found under %s; acquire it first: git clone <url> %s/%s",
			pluginName, config.PluginsDir(root), config.PluginsDir(root), pluginName)
	}

	pi := pluginInfo{Name: pluginName, Dir: dir}
	pi.Source, pi.Commit, pi.Embedded = pluginProvenance(dir)
	if _, err := os.Lstat(filepath.Join(dir, config.IntegrationManifestFile)); err == nil {
		m, merr := config.LoadIntegrationManifestNamed(dir, pluginName)
		if merr != nil {
			return pluginInfo{}, fmt.Errorf("plugin %q: %w", pluginName, merr)
		}
		pi.Manifest = m
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return pluginInfo{}, fmt.Errorf("reading plugin %q: %w", pluginName, err)
	}
	shipped := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".formula.toml") {
			continue
		}
		f := pluginFormulaInfo{File: e.Name(), Stem: strings.TrimSuffix(e.Name(), ".formula.toml")}
		shipped[f.Stem] = true
		if !e.Type().IsRegular() { // T5: a non-regular file (symlink/dir/device) — install refuses it
			f.nonRegular = true
			f.Status = pluginStatusOutOfContract
			f.Detail = "not a regular file (symlink/dir/device) — refused (T5)"
			pi.Formulas = append(pi.Formulas, f)
			continue
		}
		classifyPluginFormula(root, pluginName, dir, &f, u)
		pi.Formulas = append(pi.Formulas, f)
	}
	appendNestedFormulas(dir, &pi)
	pi.Removed = u.droppedStems(pluginName, shipped)

	sort.Slice(pi.Formulas, func(i, j int) bool { return pi.Formulas[i].File < pi.Formulas[j].File })
	return pi, nil
}

// classifyPluginFormula runs the per-file order (security.md step 3 + step 4) on a
// top-level regular formula file, filling Status/Detail/SHA256/bytes.
func classifyPluginFormula(root, pluginName, dir string, f *pluginFormulaInfo, u *collisionUniverse) {
	if err := config.ValidateAgentName(f.Stem); err != nil { // stem regex (T4)
		f.Status, f.Detail = pluginStatusInvalidName, err.Error()
		return
	}
	path := filepath.Join(dir, f.File)
	if info, serr := os.Stat(path); serr == nil && info.Size() > maxPluginFormulaBytes {
		f.Status = pluginStatusParseError // size cap (T7) BEFORE the full read — never slurp a zip-bomb
		f.Detail = fmt.Sprintf("formula file too large: %d bytes exceeds cap %d", info.Size(), maxPluginFormulaBytes)
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		f.Status, f.Detail = pluginStatusParseError, err.Error()
		return
	}
	if len(b) > maxPluginFormulaBytes { // authoritative cap (guards a grow-after-stat race)
		f.Status = pluginStatusParseError
		f.Detail = fmt.Sprintf("formula file too large: %d bytes exceeds cap %d", len(b), maxPluginFormulaBytes)
		return
	}
	f.bytes = b
	f.SHA256 = hashHex(b)

	parsed, perr := formula.Parse(b) // ParseFile+Validate, from the read-once bytes
	if perr != nil {
		f.Status, f.Detail = pluginStatusParseError, perr.Error()
		return
	}
	if parsed.Name != f.Stem { // agent-gen registers the agent to run parsed.Name, not the file stem
		f.Status = pluginStatusNameMismatch
		f.Detail = fmt.Sprintf("formula field %q does not match the file stem %q; the agent would run formula %q; set formula = %q", parsed.Name, f.Stem, parsed.Name, f.Stem)
		return
	}
	skillsDir := filepath.Join(root, ".claude", "skills")
	if serr := parsed.ValidateSkills(skillsDir); serr != nil { // existence stat only
		f.Status, f.Detail = pluginStatusMissingSkills, serr.Error()
		return
	}
	for _, name := range parsed.Integrations {
		if e, ok := u.manifest.Plugins[name]; !ok || e.Integration == nil {
			f.MissingIntegrations = append(f.MissingIntegrations, name)
		}
	}

	f.Status, f.Detail = classifyCollision(u, pluginName, f.Stem, f.bytes)
}

// classifyCollision applies the four-way sweep (T3), exempting THIS plugin's own
// already-recorded formulas via the three-valued check (design-doc K5 / H1): store
// hash == manifest hash → normal re-install; == incoming bytes → resumable; neither →
// operator-edited refusal. Byte-identity never transfers ownership: a stem another
// plugin records is refused (owners named in sorted order), and an unrecorded store
// copy with identical bytes is accepted only as the H1 crash heal — when agents.json
// does not register the stem, since a registered one was authored or deployed by the
// operator.
func classifyCollision(u *collisionUniverse, plugin, stem string, incoming []byte) (status, detail string) {
	manifestHash, ownedByThis := u.pluginOwnsFormula(plugin, stem)

	// A plugin may NEVER take a built-in shipped formula name — refuse regardless of
	// byte-identity (T3: squatting a high-value built-in identity). The embedded
	// install_formulas set is a distinct collision class from the on-disk store: in a
	// fresh / cleaned-store / source-repo factory the built-in is not yet in
	// store/formulas, so storeStems would miss it. No legitimate plugin owns a shipped
	// name (such an install would have been refused here), so this is unconditional.
	if u.shippedStems[stem] {
		return pluginStatusCollideStore, fmt.Sprintf(
			"plugin formula %q collides with a built-in shipped formula %s.formula.toml; refusing to overwrite (ADR-017); rename in the plugin repo", stem+".formula.toml", stem)
	}

	if owners := u.otherPluginOwners(plugin, stem); len(owners) > 0 {
		return pluginStatusCollideStore, fmt.Sprintf(
			"plugin formula %q is already owned by plugin(s) %s in plugins.json; refusing (ADR-017); rename in the plugin repo", stem+".formula.toml", strings.Join(owners, ", "))
	}

	// Checked before the store so the H1 heal below cannot accept a built-in identity; a
	// registered agent of that name keeps its own manual/formula collision class.
	if !ownedByThis && builtinIdentities[stem] && !u.manualAgents[stem] && !u.formulaAgents[stem] {
		return pluginStatusCollideEmbedded, fmt.Sprintf(
			"plugin formula %q names agent %q, a built-in af identity whose role template is embedded in this binary; refusing (ADR-017); rename in the plugin repo", stem+".formula.toml", stem)
	}

	if u.storeStems[stem] {
		storeHash := hashHex(u.storeBytes[stem])
		identical := storeHash == hashHex(incoming)
		switch {
		case ownedByThis && (identical || storeHash == manifestHash):
			return ownedFormulaStatus(manifestHash, incoming) // resumable, or a re-install of our own recorded formula
		case ownedByThis:
			return pluginStatusCollideStore, fmt.Sprintf(
				"store copy of %s.formula.toml was modified since install (operator-edited: hash matches neither the manifest nor the plugin); refusing to overwrite (ADR-017)", stem)
		case identical && !u.formulaAgents[stem] && !u.manualAgents[stem]:
			return pluginStatusOK, "" // H1 heal: staged by a crashed install before agents.json was written
		default:
			return pluginStatusCollideStore, "" // a DIFFERENT store formula owns this name (T3)
		}
	}

	if u.manualAgents[stem] {
		return pluginStatusCollideManual, ""
	}
	if u.formulaAgents[stem] && !ownedByThis {
		return pluginStatusCollideStore, ""
	}
	if ownedByThis {
		return ownedFormulaStatus(manifestHash, incoming)
	}
	return pluginStatusOK, ""
}

// ownedFormulaStatus is the status of this plugin's own recorded stem once no collision
// refuses it: an update is still installable, but the operator sees the hash move.
func ownedFormulaStatus(manifestHash string, incoming []byte) (status, detail string) {
	if newHash := hashHex(incoming); newHash != manifestHash {
		return pluginStatusChanged, fmt.Sprintf("plugins.json records sha256 %s; the plugin now ships %s", manifestHash, newHash)
	}
	return pluginStatusOK, ""
}

// removedDetail is the remedy for a recorded stem the plugin stopped shipping.
func removedDetail(stem string) string {
	return fmt.Sprintf("recorded in plugins.json but no longer shipped by the plugin; install drops the record and leaves the agent registered — retire it with af formula agent-gen %s --delete", stem)
}

// appendNestedFormulas reports *.formula.toml files below the plugin top level as
// out-of-contract (data.md layout contract: top level only).
func appendNestedFormulas(dir string, pi *pluginInfo) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// An integration's upstream checkout is its install input, never plugin formulas (H3-16).
		if d.IsDir() && path == filepath.Join(dir, config.IntegrationUpstreamDir) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".formula.toml") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if !strings.Contains(rel, string(filepath.Separator)) {
			return nil // top-level, already handled
		}
		pi.Formulas = append(pi.Formulas, pluginFormulaInfo{
			File:   rel,
			Stem:   strings.TrimSuffix(d.Name(), ".formula.toml"),
			Status: pluginStatusOutOfContract,
			Detail: "nested formula (not at plugin top level) — ignored by install",
		})
		return nil
	})
}

// ---- list (K4) --------------------------------------------------------------

type pluginListFormulaJSON struct {
	File   string `json:"file"`
	Agent  string `json:"agent"`
	SHA256 string `json:"sha256,omitempty"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`

	MissingIntegrations []string `json:"missing_integrations,omitempty"`
}

type pluginListJSON struct {
	Name        string                     `json:"name"`
	Source      string                     `json:"source,omitempty"`
	Commit      string                     `json:"commit,omitempty"`
	Error       string                     `json:"error,omitempty"`
	Formulas    []pluginListFormulaJSON    `json:"formulas"`
	Integration *pluginListIntegrationJSON `json:"integration"`
}

type pluginListIntegrationJSON struct {
	Kinds        []string                 `json:"kinds"`
	Scope        string                   `json:"scope"`
	Probe        string                   `json:"probe"`
	HookFailMode string                   `json:"hook_fail_mode"`
	SnapshotDir  string                   `json:"snapshot_dir"`
	Check        pluginListCheckStateJSON `json:"check"`
}

type pluginListCheckStateJSON struct {
	State string `json:"state"`
	At    string `json:"at"`
}

func runPluginList(cmd *cobra.Command, _ []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	err := listPlugins(cmd, jsonMode)
	if err != nil && jsonMode {
		data, merr := json.Marshal(struct {
			State string `json:"state"`
			Error string `json:"error"`
		}{State: "error", Error: err.Error()})
		if merr != nil {
			fmt.Fprintln(cmd.OutOrStdout(), `{"state":"error","error":"json marshal failed"}`)
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}
	return err
}

func listPlugins(cmd *cobra.Command, jsonMode bool) error {
	cwd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}

	// Loaded before the absent-dir return so a corrupt record fails the same way either way (D20),
	// and so installed integrations stay listed after their acquisition dir is gone (B10).
	record, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return err
	}
	var installed []string
	for _, name := range slices.Sorted(maps.Keys(record.Plugins)) {
		if record.Plugins[name].Integration != nil {
			installed = append(installed, name)
		}
	}

	pluginsDir := config.PluginsDir(root)
	dirEntries, derr := os.ReadDir(pluginsDir)
	if derr != nil && len(installed) == 0 { // absent plugins dir ⇒ zero-state teaching message, exit 0 (AC-6)
		if jsonMode {
			fmt.Fprintln(cmd.OutOrStdout(), "[]")
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(),
			"no plugins acquired — clone a plugin repo into %s/<name>/ then run `af plugin install <name>`\n", pluginsDir)
		return nil
	}

	u, err := buildCollisionUniverse(root)
	if err != nil {
		return err
	}

	var names []string
	acquired := map[string]bool{}
	for _, e := range dirEntries {
		if e.IsDir() {
			names = append(names, e.Name())
			acquired[e.Name()] = true
		}
	}
	for _, name := range installed {
		if !acquired[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	out := []pluginListJSON{}
	for _, name := range names {
		recorded := record.Plugins[name].Integration
		if !acquired[name] {
			lj := pluginListJSON{Name: name, Source: record.Plugins[name].Source, Commit: record.Plugins[name].Commit, Formulas: []pluginListFormulaJSON{}}
			lj.Integration, lj.Error = listIntegration(root, name, recorded, nil)
			out = append(out, lj)
			continue
		}
		pi, perr := enumeratePlugin(root, name, u)
		if perr != nil {
			// Shown, not skipped: install would name this problem, so list must too.
			lj := pluginListJSON{Name: name, Error: perr.Error(), Formulas: []pluginListFormulaJSON{}}
			lj.Integration, _ = listIntegration(root, name, recorded, nil)
			out = append(out, lj)
			continue
		}
		lj := pluginListJSON{Name: pi.Name, Source: pi.Source, Commit: pi.Commit, Formulas: []pluginListFormulaJSON{}}
		lj.Integration, lj.Error = listIntegration(root, name, recorded, pi.Manifest)
		for _, f := range pi.Formulas {
			lj.Formulas = append(lj.Formulas, pluginListFormulaJSON{
				File: f.File, Agent: f.Stem, SHA256: f.SHA256, Status: f.Status, Detail: f.Detail,
				MissingIntegrations: f.MissingIntegrations,
			})
		}
		for _, stem := range pi.Removed {
			lj.Formulas = append(lj.Formulas, pluginListFormulaJSON{
				File: stem + ".formula.toml", Agent: stem, Status: pluginStatusRemoved, Detail: removedDetail(stem),
			})
		}
		out = append(out, lj)
	}

	if jsonMode {
		data, merr := json.Marshal(out)
		if merr != nil {
			fmt.Fprintln(cmd.OutOrStdout(), `{"state":"error","error":"json marshal failed"}`)
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}

	if len(out) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(),
			"no plugins acquired — clone a plugin repo into %s/<name>/ then run `af plugin install <name>`\n", pluginsDir)
	}
	for _, lj := range out {
		if lj.Error != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "plugin %s NOT INSTALLABLE: %s\n", displaySafe(lj.Name), displaySafe(lj.Error))
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "plugin %s", displaySafe(lj.Name))
		if lj.Source != "" || lj.Commit != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " (source: %s commit: %s)", displaySafe(lj.Source), shortCommit(lj.Commit))
		}
		fmt.Fprintln(cmd.OutOrStdout())
		if in := lj.Integration; in != nil {
			state := in.Check.State
			if state == "" {
				state = "not run"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  integration [%s] scope %s, check %s\n", strings.Join(in.Kinds, " "), displaySafe(in.Scope), displaySafe(state))
			if in.SnapshotDir == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "      acquired, not installed")
			} else if !acquired[lj.Name] {
				fmt.Fprintf(cmd.OutOrStdout(), "      installed; acquisition dir %s is gone\n", filepath.Join(pluginsDir, lj.Name))
			}
		}
		for _, f := range lj.Formulas {
			fmt.Fprintf(cmd.OutOrStdout(), "  %-32s → agent %-24s [%s]\n", displaySafe(f.File), displaySafe(f.Agent), f.Status)
			if f.Detail != "" {
				// Line by line keeps multi-line hints readable while every line stays
				// indented, so an embedded newline cannot forge a top-level list line.
				for _, line := range strings.Split(f.Detail, "\n") {
					fmt.Fprintf(cmd.OutOrStdout(), "      %s\n", displaySafe(line))
				}
			}
			if len(f.MissingIntegrations) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "      needs integrations not installed: %s (af plugin install %s)\n",
					displaySafe(strings.Join(f.MissingIntegrations, ", ")), displaySafe(strings.Join(f.MissingIntegrations, " ")))
			}
		}
	}
	detectMisplacedRepos(root, cmd)
	if len(out) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), thirdPartyLabel)
	}
	return nil
}

// thirdPartyLabel is the Risk-Registry T2 overtrust mitigation (wording only, NO gate).
const thirdPartyLabel = "note: plugin formulas are third-party content — review the formulas before installing (they will direct agents in this repo)"

// listIntegration builds a row's integration object: from the record for an installed
// integration (the snapshot's manifest supplies only kinds, D21), from the acquired manifest for
// one not yet installed, nil for a formula plugin. A non-empty problem is the row's error.
func listIntegration(root, name string, recorded *config.PluginIntegration, acquired *config.IntegrationManifest) (*pluginListIntegrationJSON, string) {
	var obj pluginListIntegrationJSON
	var problem string
	switch {
	case recorded != nil:
		snap := recorded.SnapshotDir
		if !filepath.IsAbs(snap) {
			snap = filepath.Join(root, snap)
		}
		obj = pluginListIntegrationJSON{Scope: recorded.Scope, Probe: recorded.ServiceProbe, HookFailMode: recorded.HookFailMode, SnapshotDir: snap}
		m, err := config.LoadIntegrationManifestNamed(snap, name)
		if err != nil {
			problem = fmt.Sprintf("installed snapshot unreadable: %v", err)
			obj.Kinds = []string{}
		} else {
			obj.Kinds = integrationKinds(m)
		}
	case acquired != nil:
		obj = pluginListIntegrationJSON{Kinds: integrationKinds(acquired), Scope: acquired.Scope}
		if acquired.Service != nil {
			obj.Probe = acquired.Service.Probe
		}
		if acquired.Claude != nil {
			obj.HookFailMode = acquired.Claude.HookFailMode
		}
	default:
		return nil, ""
	}
	rec, err := readIntegrationCheckRecord(root, name)
	switch {
	case err != nil:
		obj.Check.State = integrationCheckError
	case rec != nil:
		obj.Check = pluginListCheckStateJSON{State: rec.State, At: rec.At}
	}
	return &obj, problem
}

// integrationKinds lists the manifest's kind sections, sorted (D21).
func integrationKinds(m *config.IntegrationManifest) []string {
	kinds := []string{}
	for _, k := range []struct {
		name    string
		present bool
	}{
		{"check", m.Check != nil},
		{"claude", m.Claude != nil},
		{"env", len(m.Env) > 0},
		{"install", m.Install != nil},
		{"service", m.Service != nil},
	} {
		if k.present {
			kinds = append(kinds, k.name)
		}
	}
	return kinds
}

// displaySafe renders a third-party string (plugin, file and agent names, source,
// details) for a terminal. A plugin repo is untrusted until reviewed, and these strings
// appear on the very surface the operator reviews it on, so a string carrying control
// characters or invalid UTF-8 is shown quoted — visible and inert — instead of being
// written raw to the terminal. Ordinary strings render unchanged.
func displaySafe(s string) string {
	if !utf8.ValidString(s) {
		return strconv.Quote(s)
	}
	for _, r := range s {
		if !strconv.IsPrint(r) {
			return strconv.Quote(s)
		}
	}
	return s
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// detectMisplacedRepos warns when a directory (likely a plugin repo cloned to the
// wrong place) sits under store/formulas/ (K4 mv remediation).
func detectMisplacedRepos(root string, cmd *cobra.Command) {
	storeDir := config.FormulasDir(root)
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(cmd.OutOrStdout(),
				"note: %q is a directory under store/formulas/ — if it is a cloned plugin repo, move it: mv %s %s\n",
				e.Name(), filepath.Join(storeDir, e.Name()), filepath.Join(config.PluginsDir(root), e.Name()))
		}
	}
}

// ---- install (K5, K7, K9) ---------------------------------------------------

// gatewayDeployed reports whether this factory has a LiteLLM gateway set up. gatewayAuthMode
// cannot answer it: it reports api-key on a factory that never had a gateway.
func gatewayDeployed(root string) bool {
	for _, p := range []string{
		gatewayAuthModeRecordPath(root),
		gatewayRelaunchScriptPath(root),
		filepath.Join(config.ConfigDir(root), "litellm.yaml"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// runPluginInstall implements `af plugin install`. It relinks FIRST (it reinstalls the
// af it runs as, like `af install --agents`), refuses a plugin named twice, validates the
// whole batch, then runs preflightInstallAgents and refuses a subdirectory cwd — all
// before its first write — and only then stages, records, runs the shared
// installAgentsPipeline body with only --no-telemetry, and verifies (K9). A plugin
// install carries no operator telemetry or gateway choice, so it provisions no telemetry
// backend, never passes --litellm, and leaves the telemetry gate as it was; the gate
// write and the K15 report are `af install --agents` policy and never run here.
func runPluginInstall(cmd *cobra.Command, args []string) error {
	relinkSelfForReinstall(cmd)

	seen := map[string]bool{}
	for _, name := range args {
		if seen[name] {
			return fmt.Errorf("plugin %q is named more than once in this install; name each plugin once", name)
		}
		seen[name] = true
	}

	cwd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}
	noBuild, _ := cmd.Flags().GetBool("no-build")

	u, err := buildCollisionUniverse(root)
	if err != nil {
		return err
	}

	// 1. Enumerate + validate ALL plugins BEFORE any store write (security.md L79-91).
	var plugins []pluginInfo
	for _, name := range args {
		pi, perr := enumeratePlugin(root, name, u)
		if perr != nil {
			return perr // plugin-not-found / invalid-name of the dir itself
		}
		plugins = append(plugins, pi)
	}

	// A refused batch shows the statuses that refused it. A valid batch is narrated only
	// once the operator gate has passed, so agent context never sees it.
	if err := validateInstallBatch(plugins); err != nil {
		printInstallSet(cmd, plugins)
		return err
	}

	// Refusals run after validation (so validation errors keep their precedence) and
	// before the first write, so a refused install leaves the store untouched (K5).
	_, _, afSrc, err := preflightInstallAgents(cmd, "af plugin install")
	if err != nil {
		return err
	}
	// The scripts run in cwd while staging targets root, so a subdirectory cwd would
	// regenerate the wrong tree.
	if !sameDir(cwd, root) {
		return fmt.Errorf("cannot run af plugin install from %s: run it from the factory root %s", cwd, root)
	}

	// AC-3: print the EXACT set + the third-party label, in-band, before acting.
	printInstallSet(cmd, plugins)

	// 2. Stage flat (copy-from-memory) — validation is complete, so a write can only
	// fail on I/O, never on policy (all-or-nothing per security.md L89-91).
	storeDir := config.FormulasDir(root)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return err
	}
	manifest, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, pi := range plugins {
		entry := config.PluginEntry{
			Source: pi.Source, Commit: pi.Commit, InstalledAt: now,
			Formulas: map[string]config.PluginFormula{},
		}
		for _, f := range pi.Formulas {
			if f.Status == pluginStatusOutOfContract {
				continue
			}
			dest := filepath.Join(storeDir, f.Stem+".formula.toml")
			if werr := os.WriteFile(dest, f.bytes, 0o644); werr != nil {
				return fmt.Errorf("staging %s: %w", f.File, werr)
			}
			entry.Formulas[f.Stem] = config.PluginFormula{SHA256: f.SHA256}
			fmt.Fprintf(cmd.OutOrStdout(), "staged %s → %s\n", f.File, dest)
		}
		manifest.Plugins[pi.Name] = entry
	}

	// 3. Record the manifest BEFORE the pipeline — K10's orphan-pass exemptions (Phase 4)
	// must see it during THIS install; the three-valued check heals a crash in this window.
	if err := config.SavePluginsConfig(config.PluginsConfigPath(root), manifest); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "recorded %d plugin(s) in %s\n", len(plugins), config.PluginsConfigPath(root))

	// 4. Rebuild via the SHARED pipeline body (K6); its refusals already ran in the
	// preflight above. Only --no-telemetry is forwarded: without an operator telemetry
	// choice quickstart must not provision the backend. --no-build passes through the
	// existing package flag.
	prevNoBuild := installNoBuildFlag
	installNoBuildFlag = noBuild
	defer func() { installNoBuildFlag = prevNoBuild }()
	if err := installAgentsPipeline(cmd, cwd, afSrc, []string{"--no-telemetry"}); err != nil {
		return fmt.Errorf("plugin install rebuild failed (store staged + manifest recorded; re-run `af plugin install %s` after fixing): %w", strings.Join(args, " "), err)
	}

	// 5. K9 — verify embed against the FRESHLY REBUILT binary (XR-6). Non-zero verify ⇒
	// non-zero install with a per-agent report.
	if err := pluginInstallVerify(cmd, root, args); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "plugin install verified.")
	// Repeated here because the pre-write narration scrolls away under the pipeline transcript.
	for _, pi := range plugins {
		for _, stem := range pi.Removed {
			fmt.Fprintf(cmd.OutOrStdout(), "plugin %s no longer ships %s; the agent is still registered — retire it with af formula agent-gen %s --delete\n", displaySafe(pi.Name), displaySafe(stem), displaySafe(stem))
		}
	}
	if gatewayDeployed(root) {
		fmt.Fprintln(cmd.OutOrStdout(), "note: the LiteLLM gateway was left as deployed (plugin install never passes --litellm); `af install --agents --litellm` reconciles it")
	}

	// Source-repo commit guidance (K10 companion): in a source-repo factory (this repo's
	// dogfood shape), the staged store formula, its generated role template, agents.json, and
	// plugins.json are all git-tracked (XR-1) and MUST be committed — K10's orphan-pass
	// exemption preserves them across the next `af install --agents` only while plugins.json is
	// present in the tree. Detect the source repo the same way agent-gen-all.sh:63-71 does:
	// resolveAFSource resolving (not falling back) to the same directory as root. A
	// no-source-tree operator (fallback) cannot rebuild `af` anyway, so skip the guidance.
	if resolved, fallback := resolveAFSource(root); !fallback && sameDir(resolved, root) {
		var paths []string
		for _, pi := range plugins {
			for _, f := range pi.Formulas {
				if f.Status == pluginStatusOutOfContract {
					continue
				}
				paths = append(paths,
					fmt.Sprintf(".agentfactory/store/formulas/%s.formula.toml", f.Stem),
					fmt.Sprintf("internal/templates/roles/%s.md.tmpl", f.Stem),
				)
			}
		}
		paths = append(paths, ".agentfactory/agents.json", ".agentfactory/plugins.json")
		fmt.Fprintln(cmd.OutOrStdout(), "\nsource-repo factory: commit these files so the install survives the next `af install --agents` redeploy:")
		for _, p := range paths {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", p)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  git add %s && git commit\n", strings.Join(paths, " "))
	}
	return nil
}

// validateInstallBatch applies install's per-formula refusals and the batch union (the
// fourth collision class); the first violation wins.
func validateInstallBatch(plugins []pluginInfo) error {
	batchStems := map[string]string{}
	for _, pi := range plugins {
		topLevel := 0
		for _, f := range pi.Formulas {
			if f.nonRegular {
				return fmt.Errorf("plugin formula %q is not a regular file (symlink/dir/device); refusing (T5); remove it from the plugin repo", f.File)
			}
			if f.Status == pluginStatusOutOfContract {
				continue // nested files are ignored, not installed
			}
			topLevel++
			if f.Status != pluginStatusOK && f.Status != pluginStatusChanged {
				return installValidationError(pi.Name, f)
			}
			if other, dup := batchStems[f.Stem]; dup {
				return fmt.Errorf("plugin formula %q is shipped by both %q and %q in this install; refusing (batch collision)", f.File, other, pi.Name)
			}
			batchStems[f.Stem] = pi.Name
		}
		if topLevel == 0 {
			return fmt.Errorf("plugin %q contains no *.formula.toml files at its top level", pi.Name)
		}
	}
	return nil
}

func printInstallSet(cmd *cobra.Command, plugins []pluginInfo) {
	fmt.Fprintf(cmd.OutOrStdout(), "installing %d plugin(s):\n", len(plugins))
	for _, pi := range plugins {
		fmt.Fprintf(cmd.OutOrStdout(), "  plugin %s", displaySafe(pi.Name))
		if pi.Source != "" || pi.Commit != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " (source: %s commit: %s)", displaySafe(pi.Source), shortCommit(pi.Commit))
		}
		fmt.Fprintln(cmd.OutOrStdout())
		for _, f := range pi.Formulas {
			if f.Status == pluginStatusOutOfContract {
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "    %-32s → agent %-24s [%s]\n", displaySafe(f.File), displaySafe(f.Stem), f.Status)
		}
		for _, stem := range pi.Removed {
			fmt.Fprintf(cmd.OutOrStdout(), "    %-32s → agent %-24s [%s]\n", displaySafe(stem+".formula.toml"), displaySafe(stem), pluginStatusRemoved)
			fmt.Fprintf(cmd.OutOrStdout(), "      %s\n", displaySafe(removedDetail(stem)))
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), thirdPartyLabel)
}

// installValidationError maps a refused status to the exact api.md L87-92 message.
func installValidationError(pluginName string, f pluginFormulaInfo) error {
	switch f.Status {
	case pluginStatusInvalidName:
		return fmt.Errorf("plugin formula filename %q is not a valid agent name (must match [a-zA-Z][a-zA-Z0-9_-]*)", f.File)
	case pluginStatusMissingSkills:
		return fmt.Errorf("%s", f.Detail) // pass through ValidateSkills' hint text (validate.go:353-356)
	case pluginStatusParseError:
		return fmt.Errorf("plugin formula %q failed to parse/validate: %s", f.File, f.Detail)
	case pluginStatusCollideStore:
		if f.Detail != "" {
			return fmt.Errorf("%s", f.Detail) // operator-edited store copy
		}
		return fmt.Errorf("plugin formula %q collides with existing store formula %s.formula.toml; refusing to overwrite (ADR-017); rename in the plugin repo or remove the existing formula first", f.File, f.Stem)
	case pluginStatusCollideManual:
		return fmt.Errorf("plugin formula %q collides with hand-authored agent %q in agents.json; refusing (ADR-017)", f.File, f.Stem)
	case pluginStatusCollideEmbedded:
		return fmt.Errorf("%s", f.Detail)
	case pluginStatusNameMismatch:
		return fmt.Errorf("plugin formula %q: %s", f.File, f.Detail)
	default:
		return fmt.Errorf("plugin formula %q has status %s: %s", f.File, f.Status, f.Detail)
	}
}

// pluginInstallVerify is the K9 gate: exec `af plugin verify <names> --json` in the
// FRESHLY REBUILT binary (exec.LookPath("af") — the running process's embedded FS is
// stale post-rebuild, XR-6). It is a package-var seam so unit tests exec nothing (the
// noexec-/tmp trap) and can inject an embed-fail outcome. If af is not on PATH it falls
// back to an in-process check against the RUNNING binary, printing which answered.
var pluginInstallVerify = func(cmd *cobra.Command, root string, pluginNames []string) error {
	afPath, err := exec.LookPath("af")
	if err != nil {
		report, ok, verr := verifyPluginsReport(root, pluginNames, false)
		if verr != nil {
			return fmt.Errorf("plugin verify after install (in-process, running binary %s — af not on PATH): %w", currentBinaryPath(), verr)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "verify (in-process, running binary %s — af not on PATH):\n%s", currentBinaryPath(), report)
		if !ok {
			return fmt.Errorf("plugin verify failed after install (see report above)")
		}
		return nil
	}
	verifyArgs := append([]string{"plugin", "verify", "--json"}, pluginNames...)
	c := exec.Command(afPath, verifyArgs...)
	c.Dir = root
	out, runErr := c.CombinedOutput()
	fmt.Fprintf(cmd.OutOrStdout(), "verify (%s):\n%s\n", afPath, out)
	if runErr != nil {
		if isNotAPluginVerifyReport(out) {
			afSrc, _ := resolveAFSource(root)
			return fmt.Errorf("plugin verify failed after install: %s on PATH does not answer `af plugin verify` (no verify report). Either the agentfactory source tree %s predates the plugin verb, so the rebuild installed an af without it, or a stale af earlier on PATH shadows the rebuilt one; update the source tree or fix PATH, then re-run `af plugin install %s`", afPath, afSrc, strings.Join(pluginNames, " "))
		}
		return fmt.Errorf("plugin verify failed after install against %s (see report above)", afPath)
	}
	return nil
}

// isNotAPluginVerifyReport reports whether the output of `af plugin verify --json`
// came from an af that does not know the verb (cobra's unknown-command error, or no
// JSON report carrying "results"), as opposed to a genuine FAIL report.
func isNotAPluginVerifyReport(out []byte) bool {
	if bytes.Contains(out, []byte(`unknown command "plugin"`)) {
		return true
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var report struct {
			Results json.RawMessage `json:"results"`
		}
		if json.Unmarshal([]byte(line), &report) == nil && report.Results != nil {
			return false
		}
	}
	return true
}

// ---- verify (K8) ------------------------------------------------------------

type pluginVerifyResult struct {
	Plugin     string `json:"plugin"`
	Agent      string `json:"agent"`
	Registered bool   `json:"registered"`
	Embedded   bool   `json:"embedded"`
	// HashClean is nil for a non-plugin agent: only a plugins.json record has a hash
	// to compare against.
	HashClean *bool  `json:"hash_clean,omitempty"`
	OK        bool   `json:"ok"`
	Message   string `json:"message,omitempty"`
}

type pluginVerifyJSON struct {
	Binary  string               `json:"binary"`
	OK      bool                 `json:"ok"`
	State   string               `json:"state"`
	Error   string               `json:"error,omitempty"`
	Results []pluginVerifyResult `json:"results"`
}

func runPluginVerify(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	all, _ := cmd.Flags().GetBool("all")
	binPath := currentBinaryPath()

	root, results, ok, err := verifyPluginsAt(args, all)
	if err != nil {
		if jsonMode {
			printPluginVerifyJSON(cmd, pluginVerifyJSON{Binary: binPath, State: "error", Error: err.Error(), Results: []pluginVerifyResult{}})
		}
		return fmt.Errorf("plugin verify: %w", err)
	}

	if jsonMode {
		state := "ok"
		if !ok {
			state = "fail"
		}
		printPluginVerifyJSON(cmd, pluginVerifyJSON{Binary: binPath, OK: ok, State: state, Results: results})
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "verify (binary: %s)\n", binPath)
		for _, r := range results {
			state := "OK"
			if !r.OK {
				state = "FAIL"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %s\n", r.label(), state)
			if r.Message != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    %s\n", r.Message)
			}
		}
	}
	if all {
		warnOut := cmd.OutOrStdout()
		if jsonMode {
			warnOut = cmd.ErrOrStderr()
		}
		ignored, kerr := ignoredRecordKeys(root)
		if kerr != nil {
			fmt.Fprintf(warnOut, "warning: cannot check plugins.json for keys this af ignores: %v\n", kerr)
		}
		for _, k := range ignored {
			fmt.Fprintf(warnOut, "warning: plugins.json key %s is ignored by this af (a newer af wrote it?); the next install or remove drops it\n", displaySafe(k))
		}
	}
	if !ok {
		return fmt.Errorf("plugin verify failed: one or more agents are not registered ∧ embedded ∧ hash-clean, or an integration snapshot drifted")
	}
	return nil
}

// label names a result row: the agent, or the plugin for an integration or unrecorded row.
func (r pluginVerifyResult) label() string {
	if r.Agent == "" {
		return r.Plugin
	}
	return r.Agent
}

func verifyPluginsAt(args []string, all bool) (string, []pluginVerifyResult, bool, error) {
	cwd, err := getWd()
	if err != nil {
		return "", nil, false, err
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		return "", nil, false, err
	}
	results, ok, err := verifyPlugins(root, args, all)
	return root, results, ok, err
}

func printPluginVerifyJSON(cmd *cobra.Command, payload pluginVerifyJSON) {
	data, merr := json.Marshal(payload)
	if merr != nil {
		fmt.Fprintln(cmd.OutOrStdout(), `{"state":"error","error":"json marshal failed"}`)
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
}

// verifyPlugins checks agents for registered (agents.json entry with a formula field) ∧
// embedded (templates.New().HasRole against THIS binary) ∧, for plugin agents,
// hash-clean (store sha256 == manifest). It has three modes: named plugins; bare (no
// names, all false) = every recorded plugin; all = every recorded plugin agent plus
// every agents.json formula agent, deduped by stem with the plugin row kept, so an
// unembedded agent of any origin is caught. A plugins.json that is present but cannot
// be loaded is an error, never "zero plugins"; under all, so is such an agents.json.
func verifyPlugins(root string, pluginNames []string, all bool) ([]pluginVerifyResult, bool, error) {
	if all && len(pluginNames) > 0 {
		return nil, false, errors.New("plugin names and --all are mutually exclusive")
	}
	manifest, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return nil, false, err
	}
	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, agentsErr := config.LoadAgentConfig(agentsPath)
	if all && agentsErr != nil && !errors.Is(agentsErr, config.ErrNotFound) {
		return nil, false, fmt.Errorf("loading %s: %w", agentsPath, agentsErr)
	}
	tmpl := templates.New()
	storeDir := config.FormulasDir(root)

	type target struct {
		plugin, stem, manifestHash string
		nonPlugin                  bool
	}
	var targets []target
	integrations := map[string]*config.PluginIntegration{}
	add := func(plugin string, entry config.PluginEntry) {
		if entry.Integration != nil {
			integrations[plugin] = entry.Integration
		}
		for key, f := range entry.Formulas {
			targets = append(targets, target{plugin: plugin, stem: strings.TrimSuffix(key, ".formula.toml"), manifestHash: f.SHA256})
		}
	}
	if all || len(pluginNames) == 0 {
		for p, e := range manifest.Plugins {
			add(p, e)
		}
	} else {
		for _, p := range pluginNames {
			e, ok := manifest.Plugins[p]
			if !ok {
				targets = append(targets, target{plugin: p})
				continue
			}
			add(p, e)
		}
	}
	if all && agentsCfg != nil {
		recorded := map[string]bool{}
		for _, t := range targets {
			recorded[t.stem] = true
		}
		for name, entry := range agentsCfg.Agents {
			if entry.Formula != "" && !recorded[name] {
				targets = append(targets, target{stem: name, nonPlugin: true})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].plugin != targets[j].plugin {
			return targets[i].plugin < targets[j].plugin
		}
		return targets[i].stem < targets[j].stem
	})

	allOK := true
	results := make([]pluginVerifyResult, 0, len(targets))
	for _, t := range targets {
		if t.stem == "" {
			results = append(results, pluginVerifyResult{Plugin: t.plugin, HashClean: new(bool), Message: fmt.Sprintf("plugin %q not recorded in plugins.json", t.plugin)})
			allOK = false
			continue
		}
		var registeredFormula string
		if agentsCfg != nil {
			registeredFormula = agentsCfg.Agents[t.stem].Formula
		}
		registered := registeredFormula != ""
		if !t.nonPlugin {
			// agent-gen --name legitimately runs another formula, but a plugin agent must run its own.
			registered = registeredFormula == t.stem
		}
		embedded := tmpl.HasRole(t.stem)
		r := pluginVerifyResult{Plugin: t.plugin, Agent: t.stem, Registered: registered, Embedded: embedded}
		if t.nonPlugin {
			r.OK = registered && embedded
			if !embedded {
				r.Message = fmt.Sprintf("agent %q template is NOT embedded in this binary — re-run 'af install --agents' from the main checkout; do not 'af up %s' until verify passes", t.stem, t.stem)
			}
		} else {
			storePath := filepath.Join(storeDir, t.stem+".formula.toml")
			b, rerr := os.ReadFile(storePath)
			hashClean := rerr == nil && t.manifestHash != "" && hashHex(b) == t.manifestHash
			r.HashClean = &hashClean
			r.OK = registered && embedded && hashClean
			switch {
			case !embedded:
				r.Message = fmt.Sprintf("plugin %q installed but agent %q template is NOT embedded in the rebuilt binary — re-run 'af plugin install %s' from the main checkout; do not 'af up %s' until verify passes", t.plugin, t.stem, t.plugin, t.stem)
			case !registered && registeredFormula != "":
				r.Message = fmt.Sprintf("plugin %q agent %q is registered in agents.json with formula %q instead of its own %q", t.plugin, t.stem, registeredFormula, t.stem)
			case !registered:
				r.Message = fmt.Sprintf("plugin %q agent %q is not registered with a formula field in agents.json", t.plugin, t.stem)
			case errors.Is(rerr, fs.ErrNotExist):
				r.Message = fmt.Sprintf("plugin %q agent %q store copy %s is missing — re-run 'af plugin install %s'", t.plugin, t.stem, storePath, t.plugin)
			case rerr != nil:
				r.Message = fmt.Sprintf("plugin %q agent %q store copy %s cannot be read (%v) — re-run 'af plugin install %s'", t.plugin, t.stem, storePath, rerr, t.plugin)
			case !hashClean:
				r.Message = fmt.Sprintf("plugin %q agent %q store formula hash does not match the plugins.json record (drift)", t.plugin, t.stem)
			}
		}
		if !r.OK {
			allOK = false
		}
		results = append(results, r)
	}
	for _, name := range slices.Sorted(maps.Keys(integrations)) {
		clean, msg := verifyIntegrationSnapshot(root, name, integrations[name])
		results = append(results, pluginVerifyResult{Plugin: name, HashClean: &clean, OK: clean, Message: msg})
		if !clean {
			allOK = false
		}
	}
	return results, allOK, nil
}

// verifyIntegrationSnapshot re-hashes an installed integration's consumed snapshot against its
// record and names every file whose content or normalized mode differs (D35).
func verifyIntegrationSnapshot(root, name string, in *config.PluginIntegration) (bool, string) {
	want := filepath.Join(config.IntegrationsDir(root), name, in.ContentSHA256)
	if filepath.IsAbs(in.SnapshotDir) || filepath.Join(root, in.SnapshotDir) != want {
		return false, fmt.Sprintf("integration %q records snapshot_dir %q, not its content-addressed snapshot (D9) — re-run 'af plugin install %s'", name, in.SnapshotDir, name)
	}
	snap := filepath.Join(root, in.SnapshotDir)
	m, err := config.LoadIntegrationManifestNamed(snap, name)
	if err != nil {
		return false, fmt.Sprintf("integration %q snapshot %s is missing or unreadable (%v) — re-run 'af plugin install %s'", name, snap, err, name)
	}
	sum, files, err := config.IntegrationContentHash(snap, integrationDeclaredPaths(m))
	if err != nil {
		return false, fmt.Sprintf("integration %q snapshot %s cannot be hashed: %v", name, snap, err)
	}
	if sum == in.ContentSHA256 {
		return true, ""
	}
	var changed, added, missing []string
	for rel, line := range files {
		recorded, ok := in.Files[rel]
		switch {
		case !ok:
			added = append(added, rel)
		case recorded != line:
			changed = append(changed, rel)
		}
	}
	for rel := range in.Files {
		if _, ok := files[rel]; !ok {
			missing = append(missing, rel)
		}
	}
	var parts []string
	for _, d := range []struct {
		label string
		rels  []string
	}{{"changed", changed}, {"added", added}, {"missing", missing}} {
		if len(d.rels) > 0 {
			slices.Sort(d.rels)
			parts = append(parts, d.label+" "+strings.Join(d.rels, ", "))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "the aggregate hash differs although every recorded file matches")
	}
	return false, fmt.Sprintf("integration %q snapshot %s drifted from its record: %s", name, snap, strings.Join(parts, "; "))
}

// ignoredRecordKeys names plugins.json keys this binary does not know. A newer af may have
// written them; they are dropped on this binary's next save (D62), so the operator is told.
func ignoredRecordKeys(root string) ([]string, error) {
	raw, err := os.ReadFile(config.PluginsConfigPath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var ignored []string
	ignored = append(ignored, unknownJSONKeys("", doc, config.PluginsConfig{})...)
	var plugins map[string]map[string]json.RawMessage
	if json.Unmarshal(doc["plugins"], &plugins) != nil {
		return ignored, nil
	}
	for _, name := range slices.Sorted(maps.Keys(plugins)) {
		entry := plugins[name]
		prefix := "plugins." + name + "."
		ignored = append(ignored, unknownJSONKeys(prefix, entry, config.PluginEntry{})...)
		var in map[string]json.RawMessage
		if json.Unmarshal(entry["integration"], &in) == nil {
			ignored = append(ignored, unknownJSONKeys(prefix+"integration.", in, config.PluginIntegration{})...)
		}
	}
	return ignored, nil
}

func unknownJSONKeys(prefix string, obj map[string]json.RawMessage, known any) []string {
	names := map[string]bool{}
	t := reflect.TypeOf(known)
	for i := 0; i < t.NumField(); i++ {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		names[tag] = true
	}
	var out []string
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		if !names[k] {
			out = append(out, prefix+k)
		}
	}
	return out
}

// verifyPluginsReport renders verifyPlugins as human text + overall ok (used by the
// pluginInstallVerify in-process fallback and by tests).
func verifyPluginsReport(root string, pluginNames []string, all bool) (string, bool, error) {
	results, ok, err := verifyPlugins(root, pluginNames, all)
	if err != nil {
		return "", false, err
	}
	var b strings.Builder
	for _, r := range results {
		state := "OK"
		if !r.OK {
			state = "FAIL"
		}
		fmt.Fprintf(&b, "  %-24s %s\n", r.label(), state)
		if r.Message != "" {
			fmt.Fprintf(&b, "    %s\n", r.Message)
		}
	}
	return b.String(), ok, nil
}
