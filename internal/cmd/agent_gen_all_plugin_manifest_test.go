//go:build !integration

package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

const g4Stem = "acme-agent"

type g4SyncFixture struct {
	afSrc      string
	formulaDir string
	manifest   string
}

type g4SyncResult struct {
	out      string
	exitCode int
}

func g4ExtractSyncBlock(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "agent-gen-all.sh"))
	if err != nil {
		t.Fatalf("reading agent-gen-all.sh: %v", err)
	}
	body := string(data)
	const startMarker = "# --- Sync formulas from source"
	const endMarker = "# --- Regenerate each formula"
	start := strings.Index(body, startMarker)
	if start == -1 {
		t.Fatal("agent-gen-all.sh missing sync block start marker")
	}
	end := strings.Index(body[start:], endMarker)
	if end == -1 {
		t.Fatal("agent-gen-all.sh missing sync block end marker")
	}
	return "set -euo pipefail\n" + body[start:start+end]
}

func g4WriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// g4NewSourceFixture builds a fake AF_SRC with PROJECT == AF_SRC (is_source_repo=true, the
// deleting branch) and a factory whose store holds the plugin formula plus one unrecorded stray.
func g4NewSourceFixture(t *testing.T, stems ...string) g4SyncFixture {
	t.Helper()
	root := t.TempDir()
	afSrc := filepath.Join(root, "src")
	factory := filepath.Join(root, "factory")
	fx := g4SyncFixture{
		afSrc:      afSrc,
		formulaDir: config.FormulasDir(factory),
		manifest:   config.PluginsConfigPath(factory),
	}
	g4WriteFile(t, filepath.Join(afSrc, "internal", "cmd", "install_formulas", "keep.formula.toml"), "# keep\n")
	g4WriteFile(t, filepath.Join(afSrc, "internal", "templates", "roles", "manager.md.tmpl"), "manager\n")
	if len(stems) == 0 {
		stems = []string{g4Stem}
	}
	for _, s := range stems {
		g4WriteFile(t, filepath.Join(fx.formulaDir, s+".formula.toml"), "# plugin formula "+s+"\n")
		g4WriteFile(t, filepath.Join(afSrc, "internal", "templates", "roles", s+".md.tmpl"), "template "+s+"\n")
	}
	g4WriteFile(t, filepath.Join(fx.formulaDir, "stray.formula.toml"), "# unrecorded orphan\n")
	return fx
}

func (fx g4SyncFixture) formulaPath(stem string) string {
	return filepath.Join(fx.formulaDir, stem+".formula.toml")
}

func (fx g4SyncFixture) templatePath(stem string) string {
	return filepath.Join(fx.afSrc, "internal", "templates", "roles", stem+".md.tmpl")
}

func g4Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func g4RequireJQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH; the jq-present arm cannot run")
	}
}

// g4NoJQPath returns a PATH holding only symlinks to the tools the sync block may use, so
// `command -v jq` fails and the grep fallback runs.
func g4NoJQPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"bash", "cp", "rm", "mv", "basename", "dirname", "head", "tail", "grep", "cat",
		"mkdir", "sed", "tr", "wc", "sort", "awk", "env", "ls", "touch"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("symlink %s: %v", tool, err)
		}
	}
	return dir
}

func g4RunSync(t *testing.T, fx g4SyncFixture, path string) g4SyncResult {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	cmd := exec.Command(bash, "-c", g4ExtractSyncBlock(t))
	env := []string{
		"AF_SRC=" + fx.afSrc,
		"FORMULA_DIR=" + fx.formulaDir,
		"PROJECT=" + fx.afSrc,
		"HOME=" + os.Getenv("HOME"),
	}
	if path == "" {
		path = os.Getenv("PATH")
	}
	cmd.Env = append(env, "PATH="+path)
	out, err := cmd.CombinedOutput()
	res := g4SyncResult{out: string(out)}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running sync block: %v", err)
		}
		res.exitCode = ee.ExitCode()
	}
	return res
}

func g4OwnedManifest(owner, key string) string {
	return fmt.Sprintf(`{"plugins":{%q:{"formulas":{%q:{"sha256":"deadbeef"}}}}}`, owner, key) + "\n"
}

var g4UnreadableManifests = []struct {
	name  string
	bytes string
	dir   bool
}{
	{name: "conflict_markers", bytes: "<<<<<<< HEAD\n" + g4OwnedManifest("acme", g4Stem) + "=======\n" + g4OwnedManifest("acme", g4Stem+".formula.toml") + ">>>>>>> origin/main\n"},
	{name: "zero_byte", bytes: ""},
	{name: "truncated", bytes: `{"plugins":{"acme":{"formulas":{"` + g4Stem + `":`},
	{name: "empty_object", bytes: "{}\n"},
	{name: "no_plugins_key", bytes: `{"x":1}` + "\n"},
	{name: "plugins_null", bytes: `{"plugins":null}` + "\n"},
	{name: "plugins_array", bytes: `{"plugins":[]}` + "\n"},
	{name: "top_level_array", bytes: "[1]\n"},
	{name: "top_level_null", bytes: "null\n"},
	{name: "directory", dir: true},
	{name: "formulas_array", bytes: `{"plugins":{"acme":{"formulas":["` + g4Stem + `"]}}}` + "\n"},
	{name: "plugin_entry_string", bytes: `{"plugins":{"acme":"` + g4Stem + `"}}` + "\n"},
	{name: "plugin_entry_array", bytes: `{"plugins":{"acme":[]}}` + "\n"},
	{name: "formula_value_string", bytes: `{"plugins":{"acme":{"formulas":{"` + g4Stem + `":"deadbeef"}}}}` + "\n"},
	{name: "sha256_number", bytes: `{"plugins":{"acme":{"formulas":{"` + g4Stem + `":{"sha256":7}}}}}` + "\n"},
	{name: "source_number", bytes: `{"plugins":{"acme":{"source":7,"formulas":{"` + g4Stem + `":{}}}}}` + "\n"},
	{name: "version_newer", bytes: g4VersionedManifest(fmt.Sprint(config.CurrentPluginsVersion + 1))},
	{name: "version_0", bytes: g4VersionedManifest("0")},
	{name: "version_negative", bytes: g4VersionedManifest("-1")},
	{name: "version_fraction", bytes: g4VersionedManifest("1.5")},
	{name: "version_string", bytes: g4VersionedManifest(`"1"`)},
}

func g4VersionedManifest(version string) string {
	return `{"version":` + version + `,"plugins":{"acme":{"formulas":{"` + g4Stem + `":{"sha256":"deadbeef"}}}}}` + "\n"
}

func g4PlaceManifest(t *testing.T, fx g4SyncFixture, content string, dir bool) {
	t.Helper()
	if dir {
		if err := os.MkdirAll(fx.manifest, 0o755); err != nil {
			t.Fatalf("mkdir manifest dir: %v", err)
		}
		return
	}
	g4WriteFile(t, fx.manifest, content)
}

// 3a (T3, T8, D7-i/ii/iii): a present-but-unreadable plugins.json preserves every orphan
// candidate and the regen proceeds (exit 0).
func TestAgentGenAllOrphanPassesPreserveOnUnreadableManifest(t *testing.T) {
	g4RequireJQ(t)
	for _, tc := range g4UnreadableManifests {
		t.Run(tc.name, func(t *testing.T) {
			fx := g4NewSourceFixture(t)
			g4PlaceManifest(t, fx, tc.bytes, tc.dir)
			res := g4RunSync(t, fx, "")
			if res.exitCode != 0 {
				t.Errorf("exit %d, want 0 (D7: preserve and proceed, not abort)\n%s", res.exitCode, res.out)
			}
			if !g4Exists(fx.formulaPath(g4Stem)) {
				t.Errorf("plugin formula %s deleted with an unreadable manifest\n%s", g4Stem, res.out)
			}
			if !g4Exists(fx.templatePath(g4Stem)) {
				t.Errorf("plugin template %s deleted with an unreadable manifest\n%s", g4Stem, res.out)
			}
			if !g4Exists(fx.formulaPath("stray")) {
				t.Errorf("orphan candidate stray.formula.toml deleted with an unreadable manifest (D7: preserve every candidate)\n%s", res.out)
			}
			if strings.Contains(res.out, "removing local formula not in source tree") || strings.Contains(res.out, "removing orphan template") {
				t.Errorf("sync block reaped orphans with an unreadable manifest\n%s", res.out)
			}
			if !g4Exists(filepath.Join(fx.formulaDir, "keep.formula.toml")) {
				t.Errorf("source formula keep.formula.toml not synced\n%s", res.out)
			}
		})
	}
}

var g4WarningNamesManifest = regexp.MustCompile(`(?m)^.*WARNING.*plugins\.json.*$`)
var g4UnreadableWording = regexp.MustCompile(`(?i)unreadable|parse|invalid|corrupt`)

// 3b (T3, T8): an unreadable manifest is loud: exactly one WARNING naming plugins.json.
func TestAgentGenAllOrphanPassesUnreadableManifestIsLoud(t *testing.T) {
	g4RequireJQ(t)
	for _, tc := range g4UnreadableManifests {
		t.Run(tc.name, func(t *testing.T) {
			fx := g4NewSourceFixture(t)
			g4PlaceManifest(t, fx, tc.bytes, tc.dir)
			res := g4RunSync(t, fx, "")
			lines := g4WarningNamesManifest.FindAllString(res.out, -1)
			if len(lines) != 1 {
				t.Fatalf("got %d WARNING line(s) naming plugins.json, want exactly 1 (D7: one warning, computed once)\n%s", len(lines), res.out)
			}
			if !g4UnreadableWording.MatchString(lines[0]) {
				t.Errorf("WARNING line %q does not say the manifest is unreadable/invalid", lines[0])
			}
		})
	}
}

// 3c (AC-6 / K10 selectivity, protective).
func TestAgentGenAllOrphanPassesSelectiveOnReadableManifest(t *testing.T) {
	g4RequireJQ(t)
	cases := []struct {
		name      string
		manifest  *string
		preserved bool
		owner     string
	}{
		{name: "absent", manifest: nil, preserved: false},
		{name: "valid_empty_plugins", manifest: g4Ptr(`{"plugins":{}}` + "\n"), preserved: false},
		{name: "valid_other_stem", manifest: g4Ptr(g4OwnedManifest("acme", "other-agent")), preserved: false},
		{name: "valid_records_stem", manifest: g4Ptr(g4OwnedManifest("acme", g4Stem)), preserved: true, owner: "acme"},
		// 20c (BODY-8, D22): a versioned manifest still reads as the same ownership. Record v2
		// (#695 Phase 2, spec L398): the version af now writes.
		{name: "versioned_records_stem", manifest: g4Ptr(`{"version":2,"plugins":{"acme":{"formulas":{"` + g4Stem + `":{"sha256":"deadbeef"}}}}}` + "\n"), preserved: true, owner: "acme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := g4NewSourceFixture(t)
			if tc.manifest != nil {
				g4WriteFile(t, fx.manifest, *tc.manifest)
			}
			res := g4RunSync(t, fx, "")
			if res.exitCode != 0 {
				t.Fatalf("exit %d\n%s", res.exitCode, res.out)
			}
			if g4Exists(fx.formulaPath("stray")) {
				t.Errorf("unrecorded stray.formula.toml survived a readable manifest: the exemption must be selective\n%s", res.out)
			}
			if got := g4Exists(fx.formulaPath(g4Stem)); got != tc.preserved {
				t.Errorf("formula %s present=%v, want %v\n%s", g4Stem, got, tc.preserved, res.out)
			}
			if got := g4Exists(fx.templatePath(g4Stem)); got != tc.preserved {
				t.Errorf("template %s present=%v, want %v\n%s", g4Stem, got, tc.preserved, res.out)
			}
			if tc.preserved {
				for _, want := range []string{
					"preserving plugin formula: " + g4Stem + ".formula.toml (installed by plugin " + tc.owner + ")",
					"preserving plugin template: " + g4Stem + " (installed by plugin " + tc.owner + ")",
				} {
					if !strings.Contains(res.out, want) {
						t.Errorf("output missing %q\n%s", want, res.out)
					}
				}
			}
			if lines := g4WarningNamesManifest.FindAllString(res.out, -1); len(lines) != 0 {
				t.Errorf("readable/absent manifest reported as a problem: %q", lines)
			}
		})
	}
}

// Multi-owner stem (SIGPIPE hazard under pipefail): a valid manifest where many plugins record
// the stem preserves, names the sorted-first owner, and is not reported as unreadable.
func TestAgentGenAllMultiOwnerStemPreservesFirstOwner(t *testing.T) {
	g4RequireJQ(t)
	var b strings.Builder
	b.WriteString(`{"plugins":{`)
	for i := 0; i < 2000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"p%04d":{"formulas":{%q:{"sha256":"x"}}}`, i, g4Stem)
	}
	b.WriteString("}}\n")
	fx := g4NewSourceFixture(t)
	g4WriteFile(t, fx.manifest, b.String())
	res := g4RunSync(t, fx, "")
	if res.exitCode != 0 {
		t.Fatalf("exit %d\n%s", res.exitCode, res.out)
	}
	if !g4Exists(fx.formulaPath(g4Stem)) || !g4Exists(fx.templatePath(g4Stem)) {
		t.Errorf("multi-owner plugin artifacts deleted\n%s", res.out)
	}
	want := "preserving plugin formula: " + g4Stem + ".formula.toml (installed by plugin p0000)"
	if !strings.Contains(res.out, want) {
		t.Errorf("output missing %q\n%s", want, res.out)
	}
	if lines := g4WarningNamesManifest.FindAllString(res.out, -1); len(lines) != 0 {
		t.Errorf("valid multi-owner manifest reported as a problem: %q", lines)
	}
}

// 3d (BODY-5, D7-iv): with jq absent the grep fallback must accept both key forms.
func TestAgentGenAllNoJQFallback(t *testing.T) {
	cases := []struct {
		name      string
		manifest  *string
		preserved bool
	}{
		{name: "suffixed_key", manifest: g4Ptr(g4OwnedManifest("vendor", g4Stem+".formula.toml")), preserved: true},
		{name: "truncated_suffixed_key", manifest: g4Ptr(`{"plugins":{"vendor":{"formulas":{"` + g4Stem + `.formula.toml":`), preserved: true},
		{name: "bare_key", manifest: g4Ptr(g4OwnedManifest("vendor", g4Stem)), preserved: true},
		{name: "conflict_markers_bare_key", manifest: g4Ptr("<<<<<<< HEAD\n" + g4OwnedManifest("vendor", g4Stem) + "=======\n{}\n>>>>>>> b\n"), preserved: true},
		// 20c (BODY-8): the extra version key does not disturb the fallback (record v2, spec L398).
		{name: "versioned_bare_key", manifest: g4Ptr(`{"version":2,"plugins":{"vendor":{"formulas":{"` + g4Stem + `":{}}}}}` + "\n"), preserved: true},
		{name: "absent", manifest: nil, preserved: false},
		{name: "valid_other_stem", manifest: g4Ptr(g4OwnedManifest("vendor", "other-agent")), preserved: false},
	}
	shim := g4NoJQPath(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := g4NewSourceFixture(t)
			if tc.manifest != nil {
				g4WriteFile(t, fx.manifest, *tc.manifest)
			}
			res := g4RunSync(t, fx, shim)
			if res.exitCode != 0 {
				t.Fatalf("exit %d\n%s", res.exitCode, res.out)
			}
			if got := g4Exists(fx.formulaPath(g4Stem)); got != tc.preserved {
				t.Errorf("jq absent: formula %s present=%v, want %v\n%s", g4Stem, got, tc.preserved, res.out)
			}
			if got := g4Exists(fx.templatePath(g4Stem)); got != tc.preserved {
				t.Errorf("jq absent: template %s present=%v, want %v\n%s", g4Stem, got, tc.preserved, res.out)
			}
		})
	}
}

// 3f (BODY-5, D7): Go OwnsAgent, the jq query and the grep fallback agree on shared fixtures.
func TestPluginOwnershipPredicateParity(t *testing.T) {
	rows := []struct {
		name     string
		manifest string
		stem     string
	}{
		{name: "bare_key", manifest: g4OwnedManifest("acme", g4Stem), stem: g4Stem},
		{name: "suffixed_key", manifest: g4OwnedManifest("vendor", g4Stem+".formula.toml"), stem: g4Stem},
		{name: "formulas_absent", manifest: `{"plugins":{"vendor":{"source":"https://example.invalid/v.git"}}}` + "\n", stem: g4Stem},
		{name: "stem_is_key_prefix", manifest: g4OwnedManifest("vendor", g4Stem), stem: "acme"},
		{name: "other_stem", manifest: g4OwnedManifest("vendor", "other-agent"), stem: g4Stem},
		{name: "valid_empty", manifest: `{"plugins":{}}` + "\n", stem: g4Stem},
		{name: "versioned", manifest: `{"version":2,"plugins":{"vendor":{"formulas":{"` + g4Stem + `.formula.toml":{}}}}}` + "\n", stem: g4Stem},
	}
	preserveRe := func(stem string) *regexp.Regexp {
		return regexp.MustCompile(`preserving plugin formula: ` + regexp.QuoteMeta(stem) + `\.formula\.toml \(installed by plugin (.*)\)`)
	}
	shellOwner := func(t *testing.T, manifest, stem, path string) (string, bool) {
		fx := g4NewSourceFixture(t, stem)
		g4WriteFile(t, fx.manifest, manifest)
		res := g4RunSync(t, fx, path)
		if res.exitCode != 0 {
			t.Fatalf("exit %d\n%s", res.exitCode, res.out)
		}
		owned := g4Exists(fx.formulaPath(stem))
		m := preserveRe(stem).FindStringSubmatch(res.out)
		if owned != (m != nil) {
			t.Fatalf("formula present=%v but preserve line matched=%v\n%s", owned, m != nil, res.out)
		}
		if m == nil {
			return "", false
		}
		return m[1], true
	}
	goOwner := func(t *testing.T, manifest, stem string) (string, bool) {
		p := filepath.Join(t.TempDir(), "plugins.json")
		g4WriteFile(t, p, manifest)
		cfg, err := config.LoadPluginsConfig(p)
		if err != nil {
			t.Fatalf("LoadPluginsConfig: %v", err)
		}
		return cfg.OwnsAgent(stem)
	}

	t.Run("jq", func(t *testing.T) {
		g4RequireJQ(t)
		for _, r := range rows {
			t.Run(r.name, func(t *testing.T) {
				wantOwner, wantOwned := goOwner(t, r.manifest, r.stem)
				gotOwner, gotOwned := shellOwner(t, r.manifest, r.stem, "")
				if gotOwned != wantOwned || gotOwner != wantOwner {
					t.Errorf("jq says (%q, %v), Go OwnsAgent says (%q, %v)", gotOwner, gotOwned, wantOwner, wantOwned)
				}
			})
		}
	})
	t.Run("grep", func(t *testing.T) {
		shim := g4NoJQPath(t)
		for _, r := range rows {
			t.Run(r.name, func(t *testing.T) {
				_, wantOwned := goOwner(t, r.manifest, r.stem)
				_, gotOwned := shellOwner(t, r.manifest, r.stem, shim)
				if gotOwned != wantOwned {
					t.Errorf("grep fallback owned=%v, Go OwnsAgent owned=%v", gotOwned, wantOwned)
				}
			})
		}
	})
}

// ADR-025 (feedback #2 on PR 539): the shell reaps only when the manifest has the shape
// LoadPluginsConfig accepts. Every row must land on the same side of that line in both readers:
// Go err != nil <=> the shell prints the unreadable WARNING and preserves the unrecorded stray.
// Integral version spellings such as 1.0 and 1e0 are left out: jq normalizes number literals, so
// the shell cannot see the spelling Go's strict int decoding rejects.
func TestPluginManifestShapeParity(t *testing.T) {
	g4RequireJQ(t)
	type row struct {
		name  string
		bytes string
		dir   bool
	}
	var rows []row
	for _, u := range g4UnreadableManifests {
		rows = append(rows, row{name: "unreadable_" + u.name, bytes: u.bytes, dir: u.dir})
	}
	for _, v := range []row{
		{name: "empty_plugins", bytes: `{"plugins":{}}`},
		{name: "plugin_entry_null", bytes: `{"plugins":{"acme":null}}`},
		{name: "formulas_null", bytes: `{"plugins":{"acme":{"formulas":null}}}`},
		{name: "formula_value_null", bytes: `{"plugins":{"acme":{"formulas":{"` + g4Stem + `":null}}}}`},
		{name: "sha256_null", bytes: `{"plugins":{"acme":{"formulas":{"` + g4Stem + `":{"sha256":null}}}}}`},
		{name: "version_null", bytes: `{"version":null,"plugins":{}}`},
		{name: "version_current", bytes: g4VersionedManifest(fmt.Sprint(config.CurrentPluginsVersion))},
		{name: "all_fields", bytes: `{"version":2,"plugins":{"acme":{"source":"https://example.invalid/a.git","commit":"abc","installed_at":"2026-09-25T00:00:00Z","formulas":{"` + g4Stem + `":{"sha256":"deadbeef"}}}}}`},
		{name: "unknown_fields", bytes: `{"extra":[1],"plugins":{"acme":{"extra":{},"formulas":{"` + g4Stem + `":{"extra":7}}}}}`},
		// Record v2 (#695 Phase 2, spec L398-401): the integration block is an object or null, and
		// a v1 file stays readable after the bump.
		{name: "integration_object", bytes: `{"version":2,"plugins":{"acme":{"integration":{},"formulas":{"` + g4Stem + `":{"sha256":"deadbeef"}}}}}`},
		{name: "integration_null", bytes: `{"version":2,"plugins":{"acme":{"integration":null,"formulas":{"` + g4Stem + `":{"sha256":"deadbeef"}}}}}`},
		{name: "version_1_still_readable", bytes: g4VersionedManifest("1")},
	} {
		v.name = "readable_" + v.name
		rows = append(rows, v)
	}
	// A non-object integration block is a shape Go's decode refuses, so the shell must refuse it
	// too, with or without a version key.
	for _, u := range []row{
		{name: "integration_7_versionless", bytes: `{"plugins":{"acme":{"integration":7,"formulas":{"` + g4Stem + `":{"sha256":"deadbeef"}}}}}`},
		{name: "integration_7_v2", bytes: `{"version":2,"plugins":{"acme":{"integration":7,"formulas":{"` + g4Stem + `":{"sha256":"deadbeef"}}}}}`},
	} {
		u.name = "unreadable_" + u.name
		rows = append(rows, u)
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			fx := g4NewSourceFixture(t)
			g4PlaceManifest(t, fx, r.bytes, r.dir)
			_, goErr := config.LoadPluginsConfig(fx.manifest)
			goRejects := goErr != nil
			// Each row also pins which side of the line it is on, so a row cannot pass by both
			// readers drifting to the same wrong verdict.
			if wantRejects := strings.HasPrefix(r.name, "unreadable_"); goRejects != wantRejects {
				t.Errorf("Go LoadPluginsConfig rejects=%v, want %v for row %s (err: %v)", goRejects, wantRejects, r.name, goErr)
			}
			res := g4RunSync(t, fx, "")
			if res.exitCode != 0 {
				t.Fatalf("exit %d\n%s", res.exitCode, res.out)
			}
			shellWarned := len(g4WarningNamesManifest.FindAllString(res.out, -1)) == 1
			if shellWarned != goRejects {
				t.Errorf("shell unreadable WARNING=%v, Go LoadPluginsConfig rejects=%v (err: %v)\n%s", shellWarned, goRejects, goErr, res.out)
			}
			if strayKept := g4Exists(fx.formulaPath("stray")); strayKept != goRejects {
				t.Errorf("shell kept unrecorded stray=%v, Go LoadPluginsConfig rejects=%v (err: %v)\n%s", strayKept, goRejects, goErr, res.out)
			}
		})
	}
}

func g4Ptr(s string) *string { return &s }
