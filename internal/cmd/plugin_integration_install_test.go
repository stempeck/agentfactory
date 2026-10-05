package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// ---- intB helpers: integration fixtures shared by the Phase 2 slice-B verb tests ----
//
// Every fixture factory lives in an exec-capable dir (/tmp is noexec in the container), so the
// [install]/[check] scripts can run. Snapshots are read-only by contract (chmod -R a-w), so each
// factory registers a chmod -R u+w cleanup AFTER tryExecCapableDir: cleanups run LIFO, so the
// chmod runs before the RemoveAll. Nothing here calls t.Parallel: the helpers reassign seams.

const intBName = "acme-int"

// intBFakeCommit is the git provenance the runGitProvenance stub reports for every dir.
const intBFakeCommit = "0123456789abcdef0123456789abcdef01234567"

type intBEnv struct {
	base      string // exec-capable parent of everything below
	root      string // factory root
	ext       string // external_writes land here (outside the factory)
	claudeDir string // CLAUDE_CONFIG_DIR fixture (never the operator's real one)
	seams     *g1Seams
	relinks   *int
	fake      *fakeTmux
}

type intBFile struct {
	body string
	mode os.FileMode
}

// intBManifestOpts shapes one af-integration.toml.
type intBManifestOpts struct {
	scope        string // "" = omitted (defaults to formula, D33)
	shared       bool
	noCheck      bool   // omit the [check] section
	checkRun     string // "" = af/check.sh
	checkTimeout string
	extra        string // raw TOML appended at the end
	// installTimeout overrides the [install] timeout ("" = 30s).
	installTimeout string
	// externalWrites replaces the [install] external_writes list (nil = the one marker file).
	externalWrites []string
}

func intBSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// intBEnvPrefix is the [env] key prefix derived from an integration name (acme-int -> ACME_INT).
func intBEnvPrefix(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func intBExtFile(e *intBEnv, name string) string { return filepath.Join(e.ext, name+".installed") }

func intBManifest(name, extFile string, o intBManifestOpts) string {
	var b strings.Builder
	b.WriteString("name = \"" + name + "\"\n")
	b.WriteString("description = \"" + name + " integration fixture\"\n")
	if o.scope != "" {
		b.WriteString("scope = \"" + o.scope + "\"\n")
	}
	installTimeout := "30s"
	if o.installTimeout != "" {
		installTimeout = o.installTimeout
	}
	b.WriteString("\n[install]\nrun = \"af/install.sh\"\ntimeout = \"" + installTimeout + "\"\n")
	if o.externalWrites == nil {
		b.WriteString("external_writes = [\"" + extFile + "\"]\n")
	} else {
		quoted := make([]string, len(o.externalWrites))
		for i, w := range o.externalWrites {
			quoted[i] = "\"" + w + "\""
		}
		b.WriteString("external_writes = [" + strings.Join(quoted, ", ") + "]\n")
	}
	if o.shared {
		b.WriteString("shared = true\n")
	}
	if o.checkRun != "" {
		b.WriteString("\n[check]\nrun = \"" + o.checkRun + "\"\n")
		if o.checkTimeout != "" {
			b.WriteString("timeout = \"" + o.checkTimeout + "\"\n")
		}
	}
	b.WriteString("\n[service]\nsession = \"" + name + "-svc\"\nrun = \"af/serve.sh\"\nprobe = \"tmux-session\"\n")
	b.WriteString("\n[claude]\nplugins = [\"claude-plugin\"]\n")
	p := intBEnvPrefix(name)
	b.WriteString("\n[env]\n" + p + "_B = \"b\"\n" + p + "_A = \"a\"\n")
	b.WriteString(o.extra)
	return b.String()
}

// intBSource is a complete, valid integration tree: the manifest, its three run scripts and one
// declared [claude] plugin dir with a bin/ tool and a skill. Only declared content, so hashing the
// whole tree equals hashing the declared set under any reading of D14.
func intBSource(e *intBEnv, name string, o intBManifestOpts) map[string]intBFile {
	extFile := intBExtFile(e, name)
	if o.checkRun == "" && !o.noCheck {
		o.checkRun = "af/check.sh"
	}
	return map[string]intBFile{
		config.IntegrationManifestFile: {intBManifest(name, extFile, o), 0o644},
		"af/install.sh":                {"#!/bin/sh\nset -e\nmkdir -p '" + e.ext + "'\nprintf 'installed\\n' > '" + extFile + "'\n", 0o755},
		"af/check.sh":                  {"#!/bin/sh\necho check-ok\n", 0o755},
		"af/serve.sh":                  {"#!/bin/sh\nexec sleep 3600\n", 0o755},
		"claude-plugin/.claude-plugin/plugin.json":   {`{"name":"` + name + `-plugin","version":"0.1.0"}` + "\n", 0o644},
		"claude-plugin/bin/" + name + "-tool":        {"#!/bin/sh\necho tool\n", 0o755},
		"claude-plugin/skills/" + name + "/SKILL.md": {"---\nname: " + name + "\ndescription: fixture skill\n---\nbody\n", 0o644},
	}
}

func intBWriteTree(t *testing.T, dir string, files map[string]intBFile) {
	t.Helper()
	for rel, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.body), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.mode); err != nil { // WriteFile's mode is umask-filtered
			t.Fatal(err)
		}
	}
}

// intBAcquire simulates an inert acquisition (git clone) into store/plugins/<name>/.
func intBAcquire(t *testing.T, e *intBEnv, name string, files map[string]intBFile) string {
	t.Helper()
	dir := filepath.Join(config.PluginsDir(e.root), name)
	intBWriteTree(t, dir, files)
	return dir
}

// intBMapFS renders files as the embed tree `af plugin acquire` reads (<name>/<rel>). embed.FS
// keeps no mode bits, so every file reports 0444 and acquire must apply the exec rule itself.
func intBMapFS(name string, files map[string]intBFile) fstest.MapFS {
	m := fstest.MapFS{}
	for rel, f := range files {
		m[name+"/"+rel] = &fstest.MapFile{Data: []byte(f.body), Mode: 0o444}
	}
	return m
}

func intBSetAcquireFS(t *testing.T, fsys fs.FS) {
	t.Helper()
	orig := acquireEmbeddedFS
	t.Cleanup(func() { acquireEmbeddedFS = orig })
	acquireEmbeddedFS = func() fs.FS { return fsys }
}

// intBChmodTree adds u+w (writable) or removes a-w (read-only) on every dir and file under root.
func intBChmodTree(root string, writable bool) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		mode := info.Mode().Perm()
		if writable {
			mode |= 0o200
		} else {
			mode &^= 0o222
		}
		return os.Chmod(p, mode)
	})
}

// intBCleanupWritable registers the chmod -R u+w that must run before a TempDir/exec-dir RemoveAll.
func intBCleanupWritable(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() { _ = intBChmodTree(dir, true) })
}

// intBFactory builds a hermetic, exec-capable factory in operator context with no AF source tree.
func intBFactory(t *testing.T) *intBEnv {
	t.Helper()
	base, err := tryExecCapableDir(t, "af-test-integration")
	if err != nil {
		t.Fatalf("no exec-capable dir for the integration fixture: %v", err)
	}
	intBCleanupWritable(t, base)
	e := &intBEnv{
		base:      base,
		root:      filepath.Join(base, "factory"),
		ext:       filepath.Join(base, "ext"),
		claudeDir: filepath.Join(base, "claude-config"),
	}
	if err := os.MkdirAll(filepath.Join(config.ConfigDir(e.root), "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"factory.json":   `{"type":"factory","version":1,"name":"agentfactory"}`,
		"agents.json":    `{"agents":{"manager":{"type":"interactive","description":"Interactive agent"},"supervisor":{"type":"autonomous","description":"Autonomous agent"}}}`,
		"messaging.json": `{"groups":{"all":["manager","supervisor"]}}`,
	} {
		if err := os.WriteFile(filepath.Join(config.ConfigDir(e.root), name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(e.claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Counting pipeline first: stubInstallPipeline points AF_SOURCE_ROOT at a valid tree, and the
	// integration branch must succeed WITHOUT one, so it is re-pointed at nothing below.
	e.seams = g1StubCountingPipeline(t)
	t.Setenv("AF_SOURCE_ROOT", filepath.Join(base, "no-af-source"))
	agentGenAFSrc, compiledSourceRoot = "", ""

	relinks := 0
	e.relinks = &relinks
	origRelink := relinkSelfForReinstall
	t.Cleanup(func() { relinkSelfForReinstall = origRelink })
	relinkSelfForReinstall = func(cmd *cobra.Command) { relinks++ }

	origProv := runGitProvenance
	t.Cleanup(func() { runGitProvenance = origProv })
	runGitProvenance = func(dir string) (string, string) {
		return "https://example.invalid/" + filepath.Base(dir) + ".git", intBFakeCommit
	}

	e.fake = newFakeTmux()
	origTmux := newCmdTmux
	t.Cleanup(func() { newCmdTmux = origTmux })
	newCmdTmux = func() cmdTmux { return e.fake }

	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	t.Setenv("CLAUDE_CONFIG_DIR", e.claudeDir)
	t.Chdir(e.root)
	t.Setenv("AF_ROOT", e.root)
	return e
}

// intBFallbackHash is the spec-literal content hash (IMPLREADME L262-267, data.md B1): one
// "<relpath> <mode> <sha256>\n" line per file in sorted relpath order, mode 0755 if any exec bit
// else 0644, aggregate = sha256 over the lines. Used only while config.IntegrationContentHash is a
// skeleton stub, so the fixture record is well-formed either way.
func intBFallbackHash(t *testing.T, dir string) (string, map[string]string) {
	t.Helper()
	files := map[string]string{}
	modes := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		files[rel] = intBSHA(b)
		modes[rel] = "0644"
		if info.Mode().Perm()&0o111 != 0 {
			modes[rel] = "0755"
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fallback hash %s: %v", dir, err)
	}
	rels := make([]string, 0, len(files))
	for r := range files {
		rels = append(rels, r)
	}
	sort.Strings(rels)
	var lines strings.Builder
	for _, r := range rels {
		lines.WriteString(r + " " + modes[r] + " " + files[r] + "\n")
	}
	return intBSHA([]byte(lines.String())), files
}

// intBDeclared is the top-level declared set of an intBSource tree.
var intBDeclared = []string{"af", config.IntegrationManifestFile, "claude-plugin"}

// intBRecord stages files as a finished, read-only snapshot at
// store/integrations/<name>/<content_sha256>/ and writes the v2 record entry for it — the state
// a successful `af plugin install` leaves. It returns the snapshot dir.
func intBRecord(t *testing.T, e *intBEnv, name string, files map[string]intBFile, mutate func(*config.PluginEntry)) string {
	t.Helper()
	parent := filepath.Join(config.IntegrationsDir(e.root), name)
	stage := filepath.Join(parent, ".tmp-fixture")
	intBWriteTree(t, stage, files)
	declared := intBDeclared
	// A real install hashes only the manifest's declared paths, so a tree with an undeclared file (a
	// noCheck fixture still ships af/check.sh) must record that narrower hash or every re-hash reads drift.
	if m, err := config.LoadIntegrationManifestNamed(stage, name); err == nil {
		declared = config.IntegrationDeclaredPaths(m)
	}
	sum, hashed, err := config.IntegrationContentHash(stage, declared)
	if err != nil {
		sum, hashed = intBFallbackHash(t, stage)
	}
	snap := filepath.Join(parent, sum)
	if err := os.Rename(stage, snap); err != nil {
		t.Fatal(err)
	}
	if err := intBChmodTree(snap, false); err != nil {
		t.Fatal(err)
	}
	relSnap, err := filepath.Rel(e.root, snap)
	if err != nil {
		t.Fatal(err)
	}
	p := intBEnvPrefix(name)
	extFile := intBExtFile(e, name)
	entry := config.PluginEntry{
		Source:      "https://example.invalid/" + name + ".git",
		Commit:      intBFakeCommit,
		InstalledAt: "2026-09-28T00:00:00Z",
		Integration: &config.PluginIntegration{
			ManifestSHA256:      intBSHA([]byte(files[config.IntegrationManifestFile].body)),
			ContentSHA256:       sum,
			Files:               hashed,
			Scope:               "formula",
			ClaudePlugins:       []string{name + "-plugin"},
			EnvKeys:             []string{p + "_A", p + "_B"},
			Service:             name + "-svc",
			ServiceProbe:        "tmux-session",
			HookFailMode:        "open",
			ExternalWrites:      []string{extFile},
			ExternalWriteHashes: map[string]string{extFile: intBSHA([]byte("installed\n"))},
			Artifacts:           []config.IntegrationArtifact{},
			Source:              "clone",
			SnapshotDir:         relSnap,
			StagedAt:            "2026-09-28T00:00:00Z",
		},
	}
	if mutate != nil {
		mutate(&entry)
	}
	intBSaveEntry(t, e.root, name, entry)
	return snap
}

func intBSaveEntry(t *testing.T, root, name string, entry config.PluginEntry) {
	t.Helper()
	path := config.PluginsConfigPath(root)
	cfg, err := config.LoadPluginsConfig(path)
	if err != nil {
		t.Fatalf("load plugins.json: %v", err)
	}
	if cfg.Plugins == nil {
		cfg.Plugins = map[string]config.PluginEntry{}
	}
	cfg.Plugins[name] = entry
	if err := config.SavePluginsConfig(path, cfg); err != nil {
		t.Fatalf("save plugins.json: %v", err)
	}
}

// intBLoadEntry reads the plugins.json entry for name, failing the test when it is absent.
func intBLoadEntry(t *testing.T, root, name string) config.PluginEntry {
	t.Helper()
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		t.Fatalf("load plugins.json: %v", err)
	}
	entry, ok := cfg.Plugins[name]
	if !ok {
		t.Fatalf("plugins.json has no %q entry; plugins=%v", name, cfg.Plugins)
	}
	return entry
}

func intBReadOptional(t *testing.T, path string) ([]byte, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b, true
}

func intBExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// intBSnapshotDirs lists the finished snapshot dirs of name (the .tmp-* stage dirs excluded).
func intBSnapshotDirs(t *testing.T, root, name string) (snaps, stages []string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(config.IntegrationsDir(root), name))
	if err != nil {
		t.Fatalf("read snapshot parent: %v", err)
	}
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".tmp-") {
			stages = append(stages, en.Name())
			continue
		}
		snaps = append(snaps, en.Name())
	}
	return snaps, stages
}

func intBWantSeamsUntouched(t *testing.T, e *intBEnv) {
	t.Helper()
	if *e.relinks != 0 {
		t.Errorf("the integration-only branch must not relink (B1); relinkSelfForReinstall ran %d time(s)", *e.relinks)
	}
	e.seams.assertNoneRan(t)
}

// ---- AC4: integration-only install ------------------------------------------

func TestPluginInstall_IntegrationOnly(t *testing.T) {
	t.Run("first_install", func(t *testing.T) {
		e := intBFactory(t)
		src := intBSource(e, intBName, intBManifestOpts{})
		intBAcquire(t, e, intBName, src)
		agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(e.root))

		out, err := runPlugin(t, "install", nil, intBName)
		if err != nil {
			t.Fatalf("integration-only install must succeed with no AF source tree and no rebuild pipeline; got: %v\noutput:\n%s", err, out)
		}
		intBWantSeamsUntouched(t, e)
		if !strings.Contains(out, "installing 1 plugin(s):") {
			t.Errorf("the install set must be printed before acting; output:\n%s", out)
		}
		if strings.Contains(out, "nothing reaches an agent session") || !strings.Contains(out, "integrations or integrations_optional") {
			t.Errorf("the install must say how the integration reaches a session, now that delivery exists; output:\n%s", out)
		}

		raw, ok := intBReadOptional(t, config.PluginsConfigPath(e.root))
		if !ok {
			t.Fatal("plugins.json was not written")
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(raw, &top); err != nil {
			t.Fatalf("plugins.json is not a JSON object: %v", err)
		}
		if v := strings.TrimSpace(string(top["version"])); v != "2" {
			t.Errorf(`plugins.json "version" = %s, want 2 (record v2)`, v)
		}
		entry := intBLoadEntry(t, e.root, intBName)
		in := entry.Integration
		if in == nil {
			t.Fatalf("the entry carries no integration record: %+v", entry)
		}
		if len(entry.Formulas) != 0 {
			t.Errorf("an integration-only entry records no formulas; got %v", entry.Formulas)
		}
		if entry.Source != "https://example.invalid/"+intBName+".git" || entry.Commit != intBFakeCommit {
			t.Errorf("entry provenance = (%q, %q), want the acquisition's git provenance", entry.Source, entry.Commit)
		}
		if in.Source != "clone" {
			t.Errorf("integration.source = %q, want clone", in.Source)
		}
		if len(in.ContentSHA256) != 64 {
			t.Fatalf("content_sha256 = %q, want 64 hex chars", in.ContentSHA256)
		}
		snap := filepath.Join(config.IntegrationsDir(e.root), intBName, in.ContentSHA256)
		if wantRel, _ := filepath.Rel(e.root, snap); in.SnapshotDir != wantRel {
			t.Errorf("snapshot_dir = %q, want the root-relative %q (D9)", in.SnapshotDir, wantRel)
		}
		if in.ManifestSHA256 != intBSHA([]byte(src[config.IntegrationManifestFile].body)) {
			t.Errorf("manifest_sha256 = %q does not hash the acquired manifest", in.ManifestSHA256)
		}
		for _, rel := range []string{config.IntegrationManifestFile, "claude-plugin/.claude-plugin/plugin.json", "af/install.sh"} {
			mode := "0644"
			if src[rel].mode&0o111 != 0 {
				mode = "0755"
			}
			if want := mode + " " + intBSHA([]byte(src[rel].body)); in.Files[rel] != want {
				t.Errorf("files[%q] = %q, want %q: the hash line's \"<mode> <sha256>\" token, so verify can name a mode-only change (D36)", rel, in.Files[rel], want)
			}
		}
		if in.Scope != "formula" || in.FactoryWide {
			t.Errorf("scope/factory_wide = %q/%v, want formula/false (D33 default)", in.Scope, in.FactoryWide)
		}
		if in.HookFailMode != "open" {
			t.Errorf("hook_fail_mode = %q, want open (D33 default)", in.HookFailMode)
		}
		if want := []string{"ACME_INT_A", "ACME_INT_B"}; !reflect.DeepEqual(in.EnvKeys, want) {
			t.Errorf("env_keys = %v, want the sorted names %v (names only)", in.EnvKeys, want)
		}
		if want := []string{intBName + "-plugin"}; !reflect.DeepEqual(in.ClaudePlugins, want) {
			t.Errorf("claude_plugins = %v, want the plugin.json names %v", in.ClaudePlugins, want)
		}
		if in.Service != intBName+"-svc" || in.ServiceProbe != "tmux-session" {
			t.Errorf("service/service_probe = %q/%q", in.Service, in.ServiceProbe)
		}
		extFile := intBExtFile(e, intBName)
		if !reflect.DeepEqual(in.ExternalWrites, []string{extFile}) {
			t.Errorf("external_writes = %v, want [%s]", in.ExternalWrites, extFile)
		}
		if got := in.ExternalWriteHashes[extFile]; got != intBSHA([]byte("installed\n")) {
			t.Errorf("external_write_hashes[%s] = %q, want the sha256 of what [install] wrote", extFile, got)
		}
		if ts, perr := time.Parse(time.RFC3339, in.StagedAt); perr != nil || !strings.HasSuffix(in.StagedAt, "Z") {
			t.Errorf("staged_at = %q (%v), want RFC3339 UTC (D33)", in.StagedAt, perr)
		} else if ts.IsZero() {
			t.Errorf("staged_at is the zero time")
		}

		// The snapshot: content-addressed, complete, read-only, exec bits kept on bin/ and run paths.
		snaps, stages := intBSnapshotDirs(t, e.root, intBName)
		if !reflect.DeepEqual(snaps, []string{in.ContentSHA256}) || len(stages) != 0 {
			t.Errorf("store/integrations/%s = snapshots %v, stage dirs %v; want exactly [%s] and no .tmp-*", intBName, snaps, stages, in.ContentSHA256)
		}
		execWant := map[string]bool{
			"af/install.sh": true, "af/check.sh": true, "af/serve.sh": true,
			"claude-plugin/bin/" + intBName + "-tool": true,
		}
		for rel := range src {
			info, serr := os.Stat(filepath.Join(snap, filepath.FromSlash(rel)))
			if serr != nil {
				t.Errorf("snapshot lacks %s: %v", rel, serr)
				continue
			}
			if info.Mode().Perm()&0o111 != 0 != execWant[rel] {
				t.Errorf("snapshot %s mode %v: exec bit wanted=%v", rel, info.Mode().Perm(), execWant[rel])
			}
		}
		_ = filepath.WalkDir(snap, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				t.Errorf("walk snapshot: %v", werr)
				return nil
			}
			if info, ierr := d.Info(); ierr == nil && info.Mode().Perm()&0o222 != 0 {
				t.Errorf("snapshot entry %s is writable (%v); the consent copy is chmod -R a-w", p, info.Mode().Perm())
			}
			return nil
		})
		if sum, _, herr := config.IntegrationContentHash(snap, intBDeclared); herr != nil {
			t.Errorf("IntegrationContentHash(snapshot): %v", herr)
		} else if sum != in.ContentSHA256 {
			t.Errorf("the snapshot re-hashes to %s, but its dir/record say %s (hash must cover the finished modes, D6)", sum, in.ContentSHA256)
		}

		// No formula-side effect.
		if intBExists(config.FormulasDir(e.root)) {
			t.Error("store/formulas/ was created by an integration-only install")
		}
		if agentsAfter, _ := os.ReadFile(config.AgentsConfigPath(e.root)); string(agentsAfter) != string(agentsBefore) {
			t.Error("agents.json changed on an integration-only install")
		}
	})

	t.Run("reinstall_changed_content_keeps_sibling", func(t *testing.T) {
		e := intBFactory(t)
		src := intBSource(e, intBName, intBManifestOpts{})
		dir := intBAcquire(t, e, intBName, src)
		if out, err := runPlugin(t, "install", nil, intBName); err != nil {
			t.Fatalf("first install: %v\n%s", err, out)
		}
		first := intBLoadEntry(t, e.root, intBName).Integration
		if first == nil {
			t.Fatal("first install recorded no integration")
		}
		skill := filepath.Join(dir, "claude-plugin", "skills", intBName, "SKILL.md")
		if err := os.WriteFile(skill, []byte("---\nname: acme-int\ndescription: changed\n---\nv2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runPlugin(t, "install", nil, intBName); err != nil {
			t.Fatalf("re-install of changed content must succeed (collisions self-exclude); got: %v\n%s", err, out)
		}
		second := intBLoadEntry(t, e.root, intBName).Integration
		if second == nil {
			t.Fatal("re-install recorded no integration")
		}
		if second.ContentSHA256 == first.ContentSHA256 {
			t.Fatalf("changed content kept content_sha256 %s", first.ContentSHA256)
		}
		snaps, _ := intBSnapshotDirs(t, e.root, intBName)
		sort.Strings(snaps)
		want := []string{first.ContentSHA256, second.ContentSHA256}
		sort.Strings(want)
		if !reflect.DeepEqual(snaps, want) {
			t.Errorf("snapshots = %v, want both siblings %v (the first is kept)", snaps, want)
		}
		wantRel, _ := filepath.Rel(e.root, filepath.Join(config.IntegrationsDir(e.root), intBName, second.ContentSHA256))
		if second.SnapshotDir != wantRel {
			t.Errorf("record snapshot_dir = %q, want the new snapshot %q", second.SnapshotDir, wantRel)
		}
	})

	t.Run("reinstall_identical_idempotent", func(t *testing.T) {
		e := intBFactory(t)
		intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
		for i := 1; i <= 2; i++ {
			if out, err := runPlugin(t, "install", nil, intBName); err != nil {
				t.Fatalf("install #%d of identical content: %v\n%s", i, err, out)
			}
		}
		in := intBLoadEntry(t, e.root, intBName).Integration
		if in == nil {
			t.Fatal("no integration recorded")
		}
		snaps, stages := intBSnapshotDirs(t, e.root, intBName)
		if !reflect.DeepEqual(snaps, []string{in.ContentSHA256}) || len(stages) != 0 {
			t.Errorf("identical re-install: snapshots %v, stage dirs %v; want only [%s] (B7 idempotent rename)", snaps, stages, in.ContentSHA256)
		}
	})

	t.Run("record_written_last", func(t *testing.T) {
		e := intBFactory(t)
		intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
		// Fault injection: the snapshot parent is a FILE, so staging (step 6) cannot succeed.
		if err := os.MkdirAll(config.IntegrationsDir(e.root), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config.IntegrationsDir(e.root), intBName), []byte("not a dir\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := runPlugin(t, "install", nil, intBName)
		if err == nil {
			t.Fatalf("install must fail when the snapshot cannot be staged; output:\n%s", out)
		}
		if strings.Contains(err.Error(), "contains no *.formula.toml files at its top level") {
			t.Fatalf("an integration plugin must reach the staging step, not the formula zero-top-level refusal; got: %v", err)
		}
		if intBExists(config.PluginsConfigPath(e.root)) {
			t.Error("plugins.json was written although staging failed; the v2 record entry is written LAST (step 10)")
		}
		intBWantSeamsUntouched(t, e)
	})

	t.Run("embedded_provenance_recorded", func(t *testing.T) {
		e := intBFactory(t)
		intBSetAcquireFS(t, intBMapFS(intBName, intBSource(e, intBName, intBManifestOpts{})))
		if out, err := runPlugin(t, "acquire", nil, intBName); err != nil {
			t.Fatalf("acquire: %v\n%s", err, out)
		}
		if out, err := runPlugin(t, "install", nil, intBName); err != nil {
			t.Fatalf("install of an acquired embedded integration: %v\n%s", err, out)
		}
		entry := intBLoadEntry(t, e.root, intBName)
		if entry.Integration == nil {
			t.Fatal("no integration recorded")
		}
		if entry.Integration.Source != "embedded" {
			t.Errorf("integration.source = %q, want embedded (D7 marker wins over git provenance)", entry.Integration.Source)
		}
		if entry.Commit != Version {
			t.Errorf("entry commit = %q, want the af version %q recorded as-is (N14)", entry.Commit, Version)
		}
	})

	t.Run("shared_adopts_existing_external_write", func(t *testing.T) {
		e := intBFactory(t)
		intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{shared: true}))
		extFile := intBExtFile(e, intBName)
		if err := os.MkdirAll(e.ext, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(extFile, []byte("preexisting\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := runPlugin(t, "install", nil, intBName)
		if err != nil {
			t.Fatalf("shared install over an existing host install: %v\n%s", err, out)
		}
		if b, _ := os.ReadFile(extFile); string(b) != "preexisting\n" {
			t.Errorf("shared=true with every external write present must skip [install] run; the file now holds %q", b)
		}
		if !strings.Contains(strings.ToLower(out), "adopt") {
			t.Errorf("the install output must say the host install was adopted; output:\n%s", out)
		}
		in := intBLoadEntry(t, e.root, intBName).Integration
		if in == nil {
			t.Fatal("no integration recorded")
		}
		if got := in.ExternalWriteHashes[extFile]; got != intBSHA([]byte("preexisting\n")) {
			t.Errorf("an adopted external write is hashed and recorded as usual; got %q", got)
		}
	})

	t.Run("shared_empty_declared_set_never_adopts", func(t *testing.T) {
		// Nothing in either set can be hashed, so nothing proves an existing host install.
		for _, row := range []struct {
			name      string
			writes    func(e *intBEnv) []string
			preCreate bool
		}{
			{"declared_empty", func(*intBEnv) []string { return []string{} }, false},
			{"directory_only", func(e *intBEnv) []string { return []string{e.ext + "/"} }, true},
		} {
			t.Run(row.name, func(t *testing.T) {
				e := intBFactory(t)
				if row.preCreate {
					if err := os.MkdirAll(e.ext, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{shared: true, externalWrites: row.writes(e)}))
				out, err := runPlugin(t, "install", nil, intBName)
				if err != nil {
					t.Fatalf("shared install with no declared regular file: %v\n%s", err, out)
				}
				if b, _ := os.ReadFile(intBExtFile(e, intBName)); string(b) != "installed\n" {
					t.Errorf("shared=true with no declared regular file must still run [install]; the marker holds %q", b)
				}
				if strings.Contains(strings.ToLower(out), "adopt") {
					t.Errorf("a shared install with no declared regular file must never adopt; output:\n%s", out)
				}
			})
		}
	})

	t.Run("upstream_nested_formula_not_mixed", func(t *testing.T) {
		// H3-16 / spec L537: appendNestedFormulas skips .upstream/, so an upstream checkout that
		// happens to carry *.formula.toml does not turn an integration-only batch into a formula
		// or mixed one.
		e := intBFactory(t)
		dir := intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
		intBWriteTree(t, filepath.Join(dir, ".upstream"), map[string]intBFile{
			"agents/up-agent.formula.toml": {validPluginFormula("up-agent"), 0o644},
		})
		out, err := runPlugin(t, "install", nil, intBName)
		intBMustReachIntegrationBranch(t, err, "the integration branch")
		if err != nil {
			t.Fatalf("an integration whose .upstream/ carries a *.formula.toml must still install as integration-only; got: %v\n%s", err, out)
		}
		entry := intBLoadEntry(t, e.root, intBName)
		if entry.Integration == nil || len(entry.Formulas) != 0 {
			t.Errorf("want an integration-only record (no formulas); got integration=%v formulas=%v", entry.Integration != nil, entry.Formulas)
		}
		if strings.Contains(out, "up-agent") {
			t.Errorf("the .upstream/ formula must not be surfaced by install:\n%s", out)
		}
		intBWantSeamsUntouched(t, e)
	})
}

// intBMustReachIntegrationBranch fails fast when an integration batch was routed to the formula
// pipeline's zero-top-level refusal (the skeleton) instead of the integration branch.
func intBMustReachIntegrationBranch(t *testing.T, err error, step string) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "contains no *.formula.toml files at its top level") {
		t.Fatalf("an integration plugin must reach %s, not the formula zero-top-level refusal; got: %v", step, err)
	}
}

// ---- AC4: refusals before any write -----------------------------------------

type intBRefusalRow struct {
	name  string
	setup func(t *testing.T, e *intBEnv) (args []string, flags map[string]string)
	want  []string
}

// intBMutate returns a fresh valid acme-int source with fn applied.
func intBMutate(e *intBEnv, o intBManifestOpts, fn func(files map[string]intBFile)) map[string]intBFile {
	files := intBSource(e, intBName, o)
	if fn != nil {
		fn(files)
	}
	return files
}

func intBAcquireFormulaPlugin(t *testing.T, e *intBEnv, name, stem string) {
	t.Helper()
	intBWriteTree(t, filepath.Join(config.PluginsDir(e.root), name), map[string]intBFile{
		stem + ".formula.toml": {validPluginFormula(stem), 0o644},
	})
}

// intBOtherRecord records a second installed integration "acme-other" whose collision
// dimensions (env keys, service, plugin names) are set by fn.
func intBOtherRecord(t *testing.T, e *intBEnv, fn func(in *config.PluginIntegration)) {
	t.Helper()
	intBRecord(t, e, "acme-other", intBSource(e, "acme-other", intBManifestOpts{}), func(entry *config.PluginEntry) {
		fn(entry.Integration)
	})
}

func intBRefusalRows() []intBRefusalRow {
	one := func(fn func(files map[string]intBFile)) func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
		return func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
			intBAcquire(t, e, intBName, intBMutate(e, intBManifestOpts{}, fn))
			return []string{intBName}, nil
		}
	}
	binRow := func(tool string) intBRefusalRow {
		return intBRefusalRow{
			name: "bin_" + tool,
			setup: one(func(f map[string]intBFile) {
				f["claude-plugin/bin/"+tool] = intBFile{"#!/bin/sh\nexit 0\n", 0o755}
			}),
			want: []string{"claude-plugin/bin/" + tool + " would shadow the af toolchain; rename it"},
		}
	}
	const pluginJSON = "claude-plugin/.claude-plugin/plugin.json"
	const pluginDirText = "claude-plugin/.claude-plugin/plugin.json: defaultEnabled must be true and every userConfig option needs a default under --plugin-dir"
	return []intBRefusalRow{
		{
			name: "factory_scope_without_flag",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{scope: "factory"}))
				return []string{intBName}, nil
			},
			want: []string{`plugin "acme-int" declares scope = "factory"; binding a guard to every agent needs --factory-wide`},
		},
		{
			name: "factory_wide_on_formula_scope_integration",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				return []string{intBName}, map[string]string{"factory-wide": "true"}
			},
			want: []string{intBName, "--factory-wide"},
		},
		{
			name: "factory_wide_on_manifestless_formula_plugin",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBAcquireFormulaPlugin(t, e, "zeta", "zeta-triage")
				return []string{"zeta"}, map[string]string{"factory-wide": "true"}
			},
			want: []string{"zeta", "--factory-wide"},
		},
		{
			name: "factory_formula_mixed_batch",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBAcquire(t, e, "acme-guard", intBSource(e, "acme-guard", intBManifestOpts{scope: "factory"}))
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				return []string{"acme-guard", intBName}, map[string]string{"factory-wide": "true"}
			},
			want: []string{`cannot install "acme-guard" (factory) and "acme-int" (formula) in one command; run: af plugin install acme-guard --factory-wide; af plugin install acme-int`},
		},
		{
			name: "mixed_integration_and_formula_plugins",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				intBAcquireFormulaPlugin(t, e, "zeta", "zeta-triage")
				return []string{intBName, "zeta"}, nil
			},
			want: []string{intBName, "zeta"},
		},
		{
			name: "manifest_and_formulas_same_plugin",
			setup: one(func(f map[string]intBFile) {
				f["acme-int-agent.formula.toml"] = intBFile{validPluginFormula("acme-int-agent"), 0o644}
			}),
			want: []string{intBName, config.IntegrationManifestFile, "acme-int-agent.formula.toml"},
		},
		{
			name: "formerly_formula_recorded_now_manifest_only",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBSaveEntry(t, e.root, intBName, config.PluginEntry{
					Source: "https://example.invalid/acme-int.git", Commit: intBFakeCommit,
					Formulas: map[string]config.PluginFormula{"acme-old.formula.toml": {SHA256: intBSHA([]byte("old"))}},
				})
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				return []string{intBName}, nil
			},
			want: []string{"acme-old", "af plugin remove"},
		},
		{
			name: "formula_install_over_recorded_integration",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBSaveEntry(t, e.root, "zeta", config.PluginEntry{
					Source: "https://example.invalid/zeta.git", Commit: intBFakeCommit,
					Integration: &config.PluginIntegration{ContentSHA256: intBSHA([]byte("zeta")), Scope: "formula"},
				})
				intBAcquireFormulaPlugin(t, e, "zeta", "zeta-triage")
				return []string{"zeta"}, nil
			},
			want: []string{"zeta", "af plugin remove"},
		},
		binRow("af"), // L552 reserved names af bd claude tmux git, plus S6's python3 (concern_tests AC5)
		binRow("bd"),
		binRow("claude"),
		binRow("tmux"),
		binRow("git"),
		binRow("python3"),
		// Spec Gotchas (IMPLREADME L1221-1224): refuse the UNION of the outline's list and design
		// S6's (af git gh tmux jq claude python3 bash sh), so gh, jq, bash and sh are reserved too.
		binRow("gh"),
		binRow("jq"),
		binRow("bash"),
		binRow("sh"),
		{
			// Designer caps (spec L267, scale.md A1): the hash of the acquisition dir refuses more than
			// 2,000 files before any write (D14), with integration_hash.go's text.
			name: "cap_more_than_2000_files",
			setup: one(func(f map[string]intBFile) {
				for i := 0; i < 2001; i++ {
					f[fmt.Sprintf("claude-plugin/data/f%04d.txt", i)] = intBFile{"x\n", 0o644}
				}
			}),
			want: []string{"more than 2000 files"},
		},
		{
			name: "cap_more_than_16MiB",
			setup: one(func(f map[string]intBFile) {
				f["claude-plugin/data/blob.bin"] = intBFile{strings.Repeat("x", 16<<20+1), 0o644}
			}),
			want: []string{"exceeds 16777216 bytes"},
		},
		{
			name: "symlink_in_declared_dir",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				dir := intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				outside := filepath.Join(e.base, "outside-the-plugin")
				if err := os.WriteFile(outside, []byte("not plugin content\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(dir, "claude-plugin", "skills", "link")); err != nil {
					t.Fatal(err)
				}
				return []string{intBName}, nil
			},
			want: []string{"symlink"},
		},
		{
			name: "duplicate_env_key_cross_integration",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBOtherRecord(t, e, func(in *config.PluginIntegration) { in.EnvKeys = []string{"ACME_INT_A"} })
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				return []string{intBName}, nil
			},
			want: []string{"ACME_INT_A", "acme-other"},
		},
		{
			name: "duplicate_service_session",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBOtherRecord(t, e, func(in *config.PluginIntegration) { in.Service = intBName + "-svc" })
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				return []string{intBName}, nil
			},
			want: []string{intBName + "-svc", "acme-other"},
		},
		{
			name: "duplicate_claude_plugin_name_other_integration",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBOtherRecord(t, e, func(in *config.PluginIntegration) { in.ClaudePlugins = []string{intBName + "-plugin"} })
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				return []string{intBName}, nil
			},
			want: []string{intBName + "-plugin", "acme-other"},
		},
		{
			name: "settings_agent",
			setup: one(func(f map[string]intBFile) {
				f["claude-plugin/settings.json"] = intBFile{`{"agent":"acme-agent"}` + "\n", 0o644}
			}),
			want: []string{`claude-plugin/settings.json sets "agent": a plugin may not replace the session identity (INV-2)`},
		},
		{
			name: "plugin_json_settings_agent",
			setup: one(func(f map[string]intBFile) {
				f[pluginJSON] = intBFile{`{"name":"acme-int-plugin","settings":{"agent":"acme-agent"}}` + "\n", 0o644}
			}),
			want: []string{`"agent"`, "INV-2"},
		},
		{
			name: "default_enabled_false",
			setup: one(func(f map[string]intBFile) {
				f[pluginJSON] = intBFile{`{"name":"acme-int-plugin","defaultEnabled":false}` + "\n", 0o644}
			}),
			want: []string{pluginDirText},
		},
		{
			name: "userconfig_without_default",
			setup: one(func(f map[string]intBFile) {
				f[pluginJSON] = intBFile{`{"name":"acme-int-plugin","userConfig":{"api_key":{"type":"string","description":"k"}}}` + "\n", 0o644}
			}),
			want: []string{pluginDirText},
		},
		{
			name: "user_scope_name_collision",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				settings := `{"enabledPlugins":{"acme-int-plugin@market":true}}` + "\n"
				if err := os.WriteFile(filepath.Join(e.claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
					t.Fatal(err)
				}
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				return []string{intBName}, nil
			},
			want: []string{`plugin name "acme-int-plugin" is already provided by a user-scope plugin; rename it`},
		},
		{
			name: "claude_md_in_claude_dir",
			setup: one(func(f map[string]intBFile) {
				f["claude-plugin/CLAUDE.md"] = intBFile{"# injected instructions\n", 0o644}
			}),
			want: []string{"CLAUDE.md"},
		},
		{
			name: "formula_toml_in_claude_dir",
			setup: one(func(f map[string]intBFile) {
				f["claude-plugin/sneaky.formula.toml"] = intBFile{validPluginFormula("sneaky"), 0o644}
			}),
			want: []string{"sneaky.formula.toml"},
		},
		{
			name: "md_tmpl_in_claude_dir",
			setup: one(func(f map[string]intBFile) {
				f["claude-plugin/sneaky.md.tmpl"] = intBFile{"# injected role template\n", 0o644}
			}),
			want: []string{"sneaky.md.tmpl"},
		},
		{
			name: "manifest_unknown_key",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{extra: "\n[servcie]\nsession = \"x\"\n"}))
				return []string{intBName}, nil
			},
			want: []string{`af-integration.toml: unknown key "servcie" (did you mean "service"?)`},
		},
		{
			name: "worktree_guard1_kept",
			setup: func(t *testing.T, e *intBEnv) ([]string, map[string]string) {
				intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
				wt := filepath.Join(e.base, "wt")
				if err := os.MkdirAll(config.ConfigDir(wt), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(config.ConfigDir(wt), ".factory-root"), []byte(e.root+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Chdir(wt)
				return []string{intBName}, nil
			},
			want: []string{"cannot run af plugin install", "worktree"},
		},
	}
}

func TestPluginInstall_IntegrationRefusals(t *testing.T) {
	for _, row := range intBRefusalRows() {
		t.Run(row.name, func(t *testing.T) {
			e := intBFactory(t)
			args, flags := row.setup(t, e)
			storeBefore := snapshotTree(t, config.StoreDir(e.root))
			manifestBefore, hadManifest := intBReadOptional(t, config.PluginsConfigPath(e.root))
			agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(e.root))

			out, err := runPlugin(t, "install", flags, args...)

			if err == nil {
				t.Errorf("install must refuse; output:\n%s", out)
			} else {
				for _, w := range row.want {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("refusal must contain %q; got: %v", w, err)
					}
				}
			}
			if after := snapshotTree(t, config.StoreDir(e.root)); !reflect.DeepEqual(storeBefore, after) {
				t.Errorf("store/ changed on a refused install:\n before=%v\n after=%v", storeBefore, after)
			}
			if manifestAfter, hasManifest := intBReadOptional(t, config.PluginsConfigPath(e.root)); hasManifest != hadManifest || string(manifestAfter) != string(manifestBefore) {
				t.Errorf("plugins.json changed on a refused install:\n before=%s\n after=%s", manifestBefore, manifestAfter)
			}
			if agentsAfter, _ := os.ReadFile(config.AgentsConfigPath(e.root)); string(agentsAfter) != string(agentsBefore) {
				t.Error("agents.json changed on a refused install")
			}
			if intBExists(e.ext) {
				t.Errorf("[install] run executed before the refusal (external write dir %s exists)", e.ext)
			}
			e.seams.assertNoneRan(t)
		})
	}
}

// ---- AC4 / L456-461: operator gate and the D27 dispatcher -------------------

const intBOperatorOnly = " is operator-only: it changes what third-party code agents run"

func TestPluginInstall_IntegrationOperatorGate(t *testing.T) {
	agentRefusal := func(t *testing.T, e *intBEnv, sub string, args ...string) {
		t.Helper()
		t.Setenv("AF_ROLE", "x")
		before := snapshotTree(t, config.ConfigDir(e.root))
		out, err := runPlugin(t, sub, nil, args...)
		want := "af plugin " + sub + intBOperatorOnly
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("agent-context %s must be refused with %q; got err=%v\noutput:\n%s", sub, want, err, out)
		}
		if err != nil && strings.Contains(err.Error(), "teardown refused") {
			t.Errorf("an integration %s is gated by requireOperator, not the teardown gate; got: %v", sub, err)
		}
		if after := snapshotTree(t, config.ConfigDir(e.root)); !reflect.DeepEqual(before, after) {
			t.Errorf("agent-context %s wrote under the factory config dir (incl. any teardown_refused artifact):\n before=%v\n after=%v", sub, before, after)
		}
		if intBExists(e.ext) {
			t.Errorf("agent-context %s ran [install]", sub)
		}
	}

	t.Run("install_agent_refused", func(t *testing.T) {
		e := intBFactory(t)
		intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))
		agentRefusal(t, e, "install", intBName)
		e.seams.assertNoneRan(t)
	})

	t.Run("acquire_agent_refused", func(t *testing.T) {
		e := intBFactory(t)
		intBSetAcquireFS(t, intBMapFS(intBName, intBSource(e, intBName, intBManifestOpts{})))
		agentRefusal(t, e, "acquire", intBName)
		if intBExists(filepath.Join(config.PluginsDir(e.root), intBName)) {
			t.Error("agent-context acquire created the acquisition dir")
		}
	})

	t.Run("remove_agent_refused", func(t *testing.T) {
		e := intBFactory(t)
		intBRecord(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}), nil)
		agentRefusal(t, e, "remove", intBName)
	})

	t.Run("requireOperator_text", func(t *testing.T) {
		e := intBFactory(t)
		t.Setenv("AF_ROLE", "")
		t.Setenv("TMUX", "")
		if err := requireOperator("plugin install"); err != nil {
			t.Errorf("operator context (AF_ROLE and TMUX empty) must pass requireOperator; got: %v", err)
		}
		t.Setenv("AF_ROLE", "x")
		err := requireOperator("plugin install")
		if err == nil || !strings.Contains(err.Error(), "plugin install"+intBOperatorOnly) {
			t.Errorf("AF_ROLE=x must be refused with %q; got: %v", "plugin install"+intBOperatorOnly, err)
		}
		t.Setenv("AF_ROLE", "")
		t.Setenv("TMUX", "/tmp/tmux-fake,1,0")
		e.fake.currentSession = "af-manager"
		if err := requireOperator("plugin install"); err == nil || !strings.Contains(err.Error(), intBOperatorOnly) {
			t.Errorf("a production af- tmux session is agent context (callerAuthority); got: %v", err)
		}
	})

	t.Run("dispatcher_is_install_RunE", func(t *testing.T) {
		got := reflect.ValueOf(pluginInstallCmd.RunE).Pointer()
		want := reflect.ValueOf(pluginInstallDispatch).Pointer()
		if got != want {
			t.Error("pluginInstallCmd.RunE must be the read-only classifying dispatcher pluginInstallDispatch (D27), which routes to runPluginInstall or runIntegrationInstall")
		}
	})

	t.Run("dispatcher_routes_both_branches", func(t *testing.T) {
		bodies := g1PackageFuncBodies(t)
		body, ok := bodies["pluginInstallDispatch"]
		if !ok {
			t.Fatal("pluginInstallDispatch not found")
		}
		for _, callee := range []string{"runPluginInstall", "runIntegrationInstall"} {
			if len(g1CallsTo(body, callee)) == 0 {
				t.Errorf("pluginInstallDispatch must call %s", callee)
			}
		}
		if n := len(g1CallsTo(body, "relinkSelfForReinstall")); n != 0 {
			t.Errorf("the dispatcher must not relink (runPluginInstall relinks as its first statement); found %d call(s)", n)
		}
	})

	t.Run("integration_branch_skips_formula_guards", func(t *testing.T) {
		bodies := g1PackageFuncBodies(t)
		if _, ok := bodies["runIntegrationInstall"]; !ok {
			t.Fatal("runIntegrationInstall not found")
		}
		reach := g1Closure(bodies, "runIntegrationInstall")
		for _, banned := range []string{"relinkSelfForReinstall", "installAgentsPipeline", "pluginInstallVerify"} {
			if n := len(g1CallsTo(bodies["runIntegrationInstall"], banned)); n != 0 {
				t.Errorf("runIntegrationInstall calls %s (%d); the integration branch skips it (B1)", banned, n)
			}
			if reach[banned] {
				t.Errorf("runIntegrationInstall reaches %s through its call closure; the integration branch skips it (B1)", banned)
			}
		}
	})
}

// ---- Phase 5 addendum: install pipeline steps 3-5 and containment (concern_tests §5) ----

// intBSeedPluginsJSON records a formula-only plugin so a failed integration install can be
// checked for a byte-identical plugins.json (not merely an absent one).
func intBSeedPluginsJSON(t *testing.T, e *intBEnv) []byte {
	t.Helper()
	intBSaveEntry(t, e.root, "zeta", config.PluginEntry{
		Source: "https://example.invalid/zeta.git", Commit: intBFakeCommit, InstalledAt: "2026-09-28T00:00:00Z",
		Formulas: map[string]config.PluginFormula{"zeta-triage.formula.toml": {SHA256: intBSHA([]byte("zeta"))}},
	})
	b, err := os.ReadFile(config.PluginsConfigPath(e.root))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// intBWantNoStoreWrite asserts a failed integration install left no record change, no snapshot and
// no .tmp-* stage for name.
func intBWantNoStoreWrite(t *testing.T, e *intBEnv, name string, pluginsBefore []byte) {
	t.Helper()
	if after, _ := os.ReadFile(config.PluginsConfigPath(e.root)); string(after) != string(pluginsBefore) {
		t.Errorf("plugins.json changed on a failed install (the record is written last, step 10):\n before=%s\n after=%s", pluginsBefore, after)
	}
	entries, err := os.ReadDir(filepath.Join(config.IntegrationsDir(e.root), name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read snapshot parent: %v", err)
	}
	for _, en := range entries {
		t.Errorf("a failed install left store/integrations/%s/%s (no snapshot, no .tmp-* stage may remain)", name, en.Name())
	}
}

// intBMustLoadManifest guards a fixture: the manifest itself is valid, so a refusal must come from
// the install step under test, not from manifest validation.
func intBMustLoadManifest(t *testing.T, dir string) *config.IntegrationManifest {
	t.Helper()
	m, err := config.LoadIntegrationManifest(dir)
	if err != nil {
		t.Fatalf("fixture manifest must be valid: %v", err)
	}
	return m
}

// ---- step 3: .upstream/ fetched at the pinned commit (spec L568; data.md D1; D18) ----

const (
	// intBUpstreamCredURL carries userinfo that must never be printed or recorded (redactRemoteURL).
	intBUpstreamCredURL     = "https://af-user:s3cr3t-token@example.invalid/acme-upstream.git"
	intBUpstreamRedactedURL = "https://example.invalid/acme-upstream.git"
)

func intBGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=af-test", "GIT_AUTHOR_EMAIL=af-test@example.invalid",
		"GIT_COMMITTER_NAME=af-test", "GIT_COMMITTER_EMAIL=af-test@example.invalid", "GIT_TERMINAL_PROMPT=0")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// intBUpstreamRepo builds a local upstream repo with commits c1 <- c2 and an ANNOTATED tag on c1,
// and routes intBUpstreamCredURL to it with a git url.insteadOf (no network). tagObj is the tag
// OBJECT's own sha: a well-formed 40-hex pin whose checkout peels to c1, so `git rev-parse HEAD`
// after fetching it yields c1 != the pin — a genuine rev-parse mismatch under any fetch strategy.
func intBUpstreamRepo(t *testing.T, e *intBEnv) (c1, tagObj string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required for the upstream fixture: %v", err)
	}
	repo := filepath.Join(e.base, "upstream-src")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	intBGit(t, repo, "init", "-q")
	intBGit(t, repo, "config", "uploadpack.allowAnySHA1InWant", "true")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("upstream v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intBGit(t, repo, "add", "-A")
	intBGit(t, repo, "commit", "-q", "-m", "c1")
	c1 = intBGit(t, repo, "rev-parse", "HEAD")
	intBGit(t, repo, "tag", "-a", "-m", "v1", "v1")
	tagObj = intBGit(t, repo, "rev-parse", "v1")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("upstream v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intBGit(t, repo, "commit", "-q", "-am", "c2")
	if tip := intBGit(t, repo, "rev-parse", "HEAD"); tip == c1 || tagObj == c1 {
		t.Fatalf("fixture: want distinct c1/tag/tip, got c1=%s tag=%s tip=%s", c1, tagObj, tip)
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", intBUpstreamCredURL)
	return c1, tagObj
}

func intBUpstreamManifest(commit string) intBManifestOpts {
	return intBManifestOpts{extra: "\n[upstream]\nrepo = \"" + intBUpstreamCredURL + "\"\ncommit = \"" + commit + "\"\n"}
}

func TestPluginInstall_IntegrationUpstreamPin(t *testing.T) {
	t.Run("pinned_commit_fetched_and_recorded", func(t *testing.T) {
		e := intBFactory(t)
		c1, _ := intBUpstreamRepo(t, e)
		dir := intBAcquire(t, e, intBName, intBSource(e, intBName, intBUpstreamManifest(c1)))
		intBMustLoadManifest(t, dir)

		out, err := runPlugin(t, "install", nil, intBName)
		intBMustReachIntegrationBranch(t, err, "the .upstream/ fetch (step 3)")
		if err != nil {
			t.Fatalf("install with [upstream] pinned to an existing commit must succeed; got: %v\n%s", err, out)
		}
		// data.md D1: the checkout lives at <plugin>/.upstream/ (the acquisition dir), fetched at
		// exactly the pin — a non-tip commit, so a branch-tip clone would not satisfy it.
		up := filepath.Join(dir, ".upstream")
		if !intBExists(up) {
			t.Fatalf("no .upstream/ checkout at %s", up)
		}
		if head := intBGit(t, up, "rev-parse", "HEAD"); head != c1 {
			t.Errorf(".upstream/ HEAD = %s, want the pinned commit %s", head, c1)
		}
		in := intBLoadEntry(t, e.root, intBName).Integration
		if in == nil {
			t.Fatal("no integration recorded")
		}
		if in.UpstreamCommit != c1 {
			t.Errorf("upstream_commit = %q, want %s", in.UpstreamCommit, c1)
		}
		if in.UpstreamRepo != intBUpstreamRedactedURL {
			t.Errorf("upstream_repo = %q, want the redacted %q (redactRemoteURL, D33)", in.UpstreamRepo, intBUpstreamRedactedURL)
		}
		raw, _ := os.ReadFile(config.PluginsConfigPath(e.root))
		for _, secret := range []string{"s3cr3t-token", "af-user"} {
			if strings.Contains(string(raw), secret) || strings.Contains(out, secret) {
				t.Errorf("the upstream URL's userinfo %q leaked into plugins.json or the output", secret)
			}
		}
		intBWantSeamsUntouched(t, e)
	})

	t.Run("rev_parse_mismatch_refused_naming_both_shas", func(t *testing.T) {
		e := intBFactory(t)
		c1, tagObj := intBUpstreamRepo(t, e)
		dir := intBAcquire(t, e, intBName, intBSource(e, intBName, intBUpstreamManifest(tagObj)))
		intBMustLoadManifest(t, dir)
		pluginsBefore := intBSeedPluginsJSON(t, e)

		out, err := runPlugin(t, "install", nil, intBName)
		intBMustReachIntegrationBranch(t, err, "the .upstream/ fetch (step 3)")
		if err == nil {
			t.Fatalf("a pin whose checkout rev-parses to another sha must be refused; output:\n%s", out)
		}
		for _, want := range []string{tagObj, c1, intBUpstreamRedactedURL} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the mismatch refusal must name the pinned sha, the rev-parse HEAD sha and the redacted URL; missing %q in: %v", want, err)
			}
		}
		if c := intBCombined(out, err); strings.Contains(c, "s3cr3t-token") || strings.Contains(c, "af-user") {
			t.Errorf("the upstream URL's userinfo leaked into the refusal:\n%s", c)
		}
		intBWantNoStoreWrite(t, e, intBName, pluginsBefore)
		if intBExists(e.ext) {
			t.Error("[install] run executed although the upstream pin failed (step 3 precedes step 4)")
		}
		intBWantSeamsUntouched(t, e)
	})
}

// ---- step 5: declared artifacts verified against the external writes (D3: every, fail-closed) ----

const intBArtifactURL = "https://example.invalid/acme-int-v1.tar.gz"

func intBArtifactManifest(sha string) intBManifestOpts {
	return intBManifestOpts{extra: "\n[[install.artifacts]]\nurl = \"" + intBArtifactURL + "\"\nsha256 = \"" + sha + "\"\n"}
}

func TestPluginInstall_IntegrationArtifactVerify(t *testing.T) {
	t.Run("sha256_mismatch_refused_before_store_write", func(t *testing.T) {
		e := intBFactory(t)
		bad := intBSHA([]byte("not what [install] wrote\n"))
		dir := intBAcquire(t, e, intBName, intBSource(e, intBName, intBArtifactManifest(bad)))
		if m := intBMustLoadManifest(t, dir); len(m.Install.Artifacts) != 1 {
			t.Fatalf("fixture: want 1 declared artifact, got %+v", m.Install.Artifacts)
		}
		pluginsBefore := intBSeedPluginsJSON(t, e)

		out, err := runPlugin(t, "install", nil, intBName)
		intBMustReachIntegrationBranch(t, err, "artifact verification (step 5)")
		if err == nil {
			t.Fatalf("a declared artifact whose sha256 matches no external write must be refused; output:\n%s", out)
		}
		for _, want := range []string{bad, intBArtifactURL} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the artifact refusal must name the declared artifact (%q); got: %v", want, err)
			}
		}
		intBWantNoStoreWrite(t, e, intBName, pluginsBefore)
		intBWantSeamsUntouched(t, e)
	})

	t.Run("matching_artifact_recorded", func(t *testing.T) {
		e := intBFactory(t)
		good := intBSHA([]byte("installed\n")) // what af/install.sh writes to the external write
		intBAcquire(t, e, intBName, intBSource(e, intBName, intBArtifactManifest(good)))

		out, err := runPlugin(t, "install", nil, intBName)
		intBMustReachIntegrationBranch(t, err, "artifact verification (step 5)")
		if err != nil {
			t.Fatalf("an artifact whose sha256 equals an external write's hash must verify; got: %v\n%s", err, out)
		}
		in := intBLoadEntry(t, e.root, intBName).Integration
		if in == nil {
			t.Fatal("no integration recorded")
		}
		if want := []config.IntegrationArtifact{{URL: intBArtifactURL, SHA256: good}}; !reflect.DeepEqual(in.Artifacts, want) {
			t.Errorf("artifacts = %+v, want %+v", in.Artifacts, want)
		}
	})
}

// ---- step 4: [install] run failure — last 20 lines, redacted, nothing staged or recorded ----

// intBNoisyInstall prints 30 numbered lines (line 25 carries an sk- token), then ends with ending.
func intBNoisyInstall(ending string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for i := 1; i <= 30; i++ {
		line := fmt.Sprintf("tail-line-%02d", i)
		if i == 25 {
			line += " token=sk-abcdefghijklmnop1234"
		}
		b.WriteString("echo '" + line + "'\n")
	}
	b.WriteString(ending + "\n")
	return b.String()
}

func TestPluginInstall_IntegrationInstallRunFailure(t *testing.T) {
	for _, tc := range []struct {
		name, ending, timeout string
	}{
		{"nonzero_exit", "exit 3", ""},
		{"timeout", "exec sleep 30", "1s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := intBFactory(t)
			files := intBSource(e, intBName, intBManifestOpts{installTimeout: tc.timeout})
			files["af/install.sh"] = intBFile{intBNoisyInstall(tc.ending), 0o755}
			dir := intBAcquire(t, e, intBName, files)
			intBMustLoadManifest(t, dir)
			pluginsBefore := intBSeedPluginsJSON(t, e)

			start := time.Now()
			out, err := runPlugin(t, "install", nil, intBName)
			elapsed := time.Since(start)
			intBMustReachIntegrationBranch(t, err, "[install] run (step 4)")
			if err == nil {
				t.Fatalf("a failing [install] run must fail the install; output:\n%s", out)
			}
			if elapsed > 20*time.Second {
				t.Errorf("install took %v; [install] run is bounded by its timeout plus WaitDelay 2s", elapsed)
			}
			c := intBCombined(out, err)
			for i := 12; i <= 30; i++ {
				if want := fmt.Sprintf("tail-line-%02d", i); !strings.Contains(c, want) {
					t.Errorf("the failure must show the last 20 lines of [install] output; missing %q:\n%s", want, c)
				}
			}
			for i := 1; i <= 10; i++ {
				if bad := fmt.Sprintf("tail-line-%02d", i); strings.Contains(c, bad) {
					t.Errorf("only the last 20 lines are shown; %q is line %d of 30:\n%s", bad, i, c)
				}
			}
			if strings.Contains(c, "sk-abcdefghijklmnop1234") || strings.Contains(c, "abcdefghijklmnop1234") {
				t.Errorf("the [install] output tail must pass through redactIntegrationOutput (sk- token shown):\n%s", c)
			}
			if tc.name == "timeout" {
				if l := strings.ToLower(c); !strings.Contains(l, "timed out") && !strings.Contains(l, "timeout") {
					t.Errorf("a timed-out [install] run must say it timed out:\n%s", c)
				}
			}
			intBWantNoStoreWrite(t, e, intBName, pluginsBefore)
			intBWantSeamsUntouched(t, e)
		})
	}
}

// ---- B9 / H3-9: declared-path containment resolves symlinks first ----------

// intBTreeState is a symlink-aware snapshot (snapshotTree would read through a dir symlink and
// fail): files map to their sha256, symlinks to their target.
func intBTreeState(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return filepath.SkipDir
			}
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, lerr := os.Readlink(p)
			if lerr != nil {
				return lerr
			}
			m[rel] = "link:" + target
		case d.IsDir():
			m[rel+"/"] = "dir"
		default:
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			m[rel] = intBSHA(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tree state %s: %v", root, err)
	}
	return m
}

// intBSplitOutside writes every file under prefix/ to outside/<target>/ instead of the plugin dir
// and returns the remaining (in-plugin) files.
func intBSplitOutside(t *testing.T, files map[string]intBFile, prefix, outside string) map[string]intBFile {
	t.Helper()
	moved := map[string]intBFile{}
	for rel, f := range files {
		if strings.HasPrefix(rel, prefix+"/") {
			moved[strings.TrimPrefix(rel, prefix+"/")] = f
			delete(files, rel)
		}
	}
	intBWriteTree(t, outside, moved)
	return files
}

func TestPluginInstall_IntegrationDeclaredPathContainment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared string // the [claude] plugins entry, which is itself a symlink escaping the plugin root
	}{
		{"declared_dir_symlink_escapes_root", "claude-plugin"},
		// H3-9: worktree.Contains allows .runtime BEFORE resolving symlinks; the install check must
		// resolve first, so a declared .runtime symlink escaping the root gets NO carve-out.
		{"runtime_carveout_not_honoured", ".runtime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := intBFactory(t)
			files := intBSource(e, intBName, intBManifestOpts{})
			m := files[config.IntegrationManifestFile]
			m.body = strings.Replace(m.body, `plugins = ["claude-plugin"]`, `plugins = ["`+tc.declared+`"]`, 1)
			files[config.IntegrationManifestFile] = m
			outside := filepath.Join(e.base, "outside", "plugin-tree")
			files = intBSplitOutside(t, files, "claude-plugin", outside)
			dir := intBAcquire(t, e, intBName, files)
			if err := os.Symlink(outside, filepath.Join(dir, tc.declared)); err != nil {
				t.Fatal(err)
			}
			intBMustLoadManifest(t, dir) // lexically in bounds: only symlink resolution can catch it

			storeBefore := intBTreeState(t, config.StoreDir(e.root))
			outsideBefore := intBTreeState(t, outside)
			pluginsBefore, hadPlugins := intBReadOptional(t, config.PluginsConfigPath(e.root))

			out, err := runPlugin(t, "install", nil, intBName)
			if err == nil {
				t.Errorf("a declared dir that is a symlink escaping the plugin root must be refused; output:\n%s", out)
			} else if !strings.Contains(err.Error(), tc.declared) {
				t.Errorf("refusal must contain %q; got: %v", tc.declared, err)
			}
			if after := intBTreeState(t, config.StoreDir(e.root)); !reflect.DeepEqual(storeBefore, after) {
				t.Errorf("store/ changed on a refused install:\n before=%v\n after=%v", storeBefore, after)
			}
			if after := intBTreeState(t, outside); !reflect.DeepEqual(outsideBefore, after) {
				t.Errorf("the symlink target outside the plugin root changed:\n before=%v\n after=%v", outsideBefore, after)
			}
			if after, has := intBReadOptional(t, config.PluginsConfigPath(e.root)); has != hadPlugins || string(after) != string(pluginsBefore) {
				t.Errorf("plugins.json changed on a refused install")
			}
			if intBExists(e.ext) {
				t.Errorf("[install] run executed before the refusal (external write dir %s exists)", e.ext)
			}
			e.seams.assertNoneRan(t)
		})
	}
}

// ---- PR #724 T10: staging and acquire keep a file's exec bit -----------------

const (
	p724e1Guard     = "claude-plugin/hooks/guard.sh"
	p724e1GuardBody = "#!/bin/sh\necho guard-ok\n"
	p724e1HooksJSON = "claude-plugin/hooks/hooks.json"
)

// p724e1HookSource is intBSource plus a [claude] hook script and a non-script beside it.
func p724e1HookSource(e *intBEnv, guardMode os.FileMode) map[string]intBFile {
	return intBMutate(e, intBManifestOpts{}, func(files map[string]intBFile) {
		files[p724e1Guard] = intBFile{p724e1GuardBody, guardMode}
		files[p724e1HooksJSON] = intBFile{"{}\n", 0o664}
	})
}

func p724e1Install(t *testing.T, e *intBEnv) (*config.PluginIntegration, string) {
	t.Helper()
	if out, err := runPlugin(t, "install", nil, intBName); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	in := intBLoadEntry(t, e.root, intBName).Integration
	if in == nil {
		t.Fatal("install recorded no integration")
	}
	return in, filepath.Join(config.IntegrationsDir(e.root), intBName, in.ContentSHA256)
}

// p724e1WantSnapshotFile asserts a snapshot file's exact sealed mode and its record "<mode> <sha256>" token.
func p724e1WantSnapshotFile(t *testing.T, in *config.PluginIntegration, snap, rel, body string, wantPerm os.FileMode, wantMode string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(snap, filepath.FromSlash(rel)))
	if err != nil {
		t.Errorf("snapshot lacks %s: %v", rel, err)
		return
	}
	if got := info.Mode().Perm(); got != wantPerm {
		t.Errorf("snapshot %s mode = %v, want %v", rel, got, wantPerm)
	}
	if want := wantMode + " " + intBSHA([]byte(body)); in.Files[rel] != want {
		t.Errorf("record files[%q] = %q, want %q", rel, in.Files[rel], want)
	}
}

func TestPR724_T10_StagedHookKeepsSourceExecBit(t *testing.T) {
	e := intBFactory(t)
	dir := intBAcquire(t, e, intBName, p724e1HookSource(e, 0o755))
	in, snap := p724e1Install(t, e)

	p724e1WantSnapshotFile(t, in, snap, p724e1Guard, p724e1GuardBody, 0o555, "0755")
	p724e1WantSnapshotFile(t, in, snap, p724e1HooksJSON, "{}\n", 0o444, "0644")
	if out, err := exec.Command(filepath.Join(snap, filepath.FromSlash(p724e1Guard))).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "guard-ok" {
		t.Errorf("Claude Code execs a --plugin-dir hook directly, so the snapshot hook must run; err=%v output=%q", err, out)
	}
	_, consented, err := config.IntegrationContentHash(dir, config.IntegrationDeclaredPaths(intBMustLoadManifest(t, dir)))
	if err != nil {
		t.Fatal(err)
	}
	for rel, tok := range consented {
		if in.Files[rel] != tok {
			t.Errorf("staged files[%q] = %q differs from the consented %q", rel, in.Files[rel], tok)
		}
	}
}

func TestPR724_T10_StagedExecBitNormalisedLikeContentHash(t *testing.T) {
	for _, srcMode := range []os.FileMode{0o750, 0o700} {
		t.Run(fmt.Sprintf("%#o", srcMode), func(t *testing.T) {
			e := intBFactory(t)
			intBAcquire(t, e, intBName, p724e1HookSource(e, srcMode))
			in, snap := p724e1Install(t, e)
			p724e1WantSnapshotFile(t, in, snap, p724e1Guard, p724e1GuardBody, 0o555, "0755")
		})
	}
}

func TestPR724_T10_KeepExecFloorOn0644RunScriptAndBin(t *testing.T) {
	e := intBFactory(t)
	floor := []string{"af/serve.sh", "claude-plugin/bin/" + intBName + "-tool"}
	src := intBMutate(e, intBManifestOpts{}, func(files map[string]intBFile) {
		for _, rel := range floor {
			f := files[rel]
			f.mode = 0o644
			files[rel] = f
		}
	})
	intBAcquire(t, e, intBName, src)
	in, snap := p724e1Install(t, e)
	for _, rel := range floor {
		p724e1WantSnapshotFile(t, in, snap, rel, src[rel].body, 0o555, "0755")
	}
}

func TestPR724_T10_AcquireThenInstallKeepsHookExecutable(t *testing.T) {
	e := intBFactory(t)
	intBSetAcquireFS(t, intBMapFS(intBName, p724e1HookSource(e, 0o755)))
	if out, err := runPlugin(t, "acquire", nil, intBName); err != nil {
		t.Fatalf("acquire: %v\n%s", err, out)
	}
	in, snap := p724e1Install(t, e)
	p724e1WantSnapshotFile(t, in, snap, p724e1Guard, p724e1GuardBody, 0o555, "0755")
	p724e1WantSnapshotFile(t, in, snap, p724e1HooksJSON, "{}\n", 0o444, "0644")
}
