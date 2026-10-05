package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// ---- AC6: af plugin remove (L645-652; decisions D26) ------------------------

type intBRemoveFixture struct {
	e         *intBEnv
	extFile   string
	runtime   []string // af's own .runtime records for acme-int (D26 deletes them)
	zetaBytes json.RawMessage
}

func intBPluginsRaw(t *testing.T, root string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(config.PluginsConfigPath(root))
	if err != nil {
		t.Fatalf("read plugins.json: %v", err)
	}
	var doc struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("plugins.json: %v", err)
	}
	return doc.Plugins
}

// intBRemoveSetup installs acme-int twice (two read-only sibling snapshots), leaves its external
// write and af's runtime records on disk, marks its service live, and records a formula-only
// plugin zeta beside it.
func intBRemoveSetup(t *testing.T) *intBRemoveFixture {
	t.Helper()
	e := intBFactory(t)
	src := intBSource(e, intBName, intBManifestOpts{})
	intBRecord(t, e, intBName, src, nil)
	src["claude-plugin/skills/"+intBName+"/SKILL.md"] = intBFile{"---\nname: acme-int\ndescription: v2\n---\nv2\n", 0o644}
	intBRecord(t, e, intBName, src, nil)
	if snaps, _ := intBSnapshotDirs(t, e.root, intBName); len(snaps) != 2 {
		t.Fatalf("fixture: want 2 sibling snapshots, got %v", snaps)
	}

	f := &intBRemoveFixture{e: e, extFile: intBExtFile(e, intBName)}
	if err := os.MkdirAll(e.ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.extFile, []byte("installed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"integration_check", "integration_service"} {
		p := filepath.Join(e.root, ".runtime", kind, intBName+".json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f.runtime = append(f.runtime, p)
	}
	intBSaveEntry(t, e.root, "zeta", config.PluginEntry{
		Source: "https://example.invalid/zeta.git", Commit: intBFakeCommit, InstalledAt: "2026-09-28T00:00:00Z",
		Formulas: map[string]config.PluginFormula{"zeta-triage.formula.toml": {SHA256: intBSHA([]byte("zeta"))}},
	})
	f.zetaBytes = intBPluginsRaw(t, e.root)["zeta"]
	e.fake.present[intBName+"-svc"] = true
	return f
}

func TestPluginRemove_ListsExternalWritesDeletesNone(t *testing.T) {
	t.Run("removes_record_and_snapshots_keeps_external_writes", func(t *testing.T) {
		f := intBRemoveSetup(t)
		e := f.e

		out, err := runPlugin(t, "remove", nil, intBName)
		if err != nil {
			t.Fatalf("operator remove of an installed integration must succeed; got: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(out, f.extFile) {
			t.Errorf("remove must print every external_writes path (%s):\n%s", f.extFile, out)
		}
		if b, rerr := os.ReadFile(f.extFile); rerr != nil || string(b) != "installed\n" {
			t.Errorf("remove deleted or changed the external write %s (it deletes none, ADR-017): %v", f.extFile, rerr)
		}
		if !strings.Contains(out, intBName+"-svc") {
			t.Errorf("remove must say the service session %s keeps running (D26):\n%s", intBName+"-svc", out)
		}
		if intBExists(filepath.Join(config.IntegrationsDir(e.root), intBName)) {
			t.Error("remove must garbage-collect every snapshot of the integration (chmod -R u+w, then RemoveAll)")
		}
		plugins := intBPluginsRaw(t, e.root)
		if _, ok := plugins[intBName]; ok {
			t.Error("remove left the record entry in plugins.json")
		}
		if got := plugins["zeta"]; string(got) != string(f.zetaBytes) {
			t.Errorf("remove changed another entry:\n before=%s\n after=%s", f.zetaBytes, got)
		}
		for _, p := range f.runtime {
			if intBExists(p) {
				t.Errorf("remove must delete af's own runtime record %s (D26)", p)
			}
		}
		for _, op := range e.fake.ops {
			if strings.HasPrefix(op, "KillSession") || strings.HasPrefix(op, "NewSession") {
				t.Errorf("remove must never stop or start a session; recorded op %q", op)
			}
		}
	})

	t.Run("directory_external_write_survives", func(t *testing.T) {
		// concern_tests §2: a DIRECTORY-type external write (a ~/-relative path with a trailing /,
		// D34) survives remove too; a RemoveAll on a dir entry is the likelier bug than on a file.
		e := intBFactory(t)
		home := filepath.Join(e.base, "home")
		t.Setenv("HOME", home)
		const dirWrite = "~/.acme-int-state/"
		stateDir := filepath.Join(home, ".acme-int-state")
		kept := map[string]string{
			filepath.Join(stateDir, "keep.txt"):        "state\n",
			filepath.Join(stateDir, "sub", "deep.txt"): "deep\n",
		}
		for p, body := range kept {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		intBRecord(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}), func(entry *config.PluginEntry) {
			entry.Integration.ExternalWrites = append(entry.Integration.ExternalWrites, dirWrite)
		})

		out, err := runPlugin(t, "remove", nil, intBName)
		if err != nil {
			t.Fatalf("operator remove of an installed integration must succeed; got: %v\noutput:\n%s", err, out)
		}
		if !strings.Contains(out, dirWrite) && !strings.Contains(out, stateDir) {
			t.Errorf("remove must print the directory external write %s:\n%s", dirWrite, out)
		}
		for p, body := range kept {
			if b, rerr := os.ReadFile(p); rerr != nil || string(b) != body {
				t.Errorf("remove deleted or changed %s inside the directory external write %s (it deletes none, ADR-017): %v", p, dirWrite, rerr)
			}
		}
		if intBExists(filepath.Join(config.IntegrationsDir(e.root), intBName)) {
			t.Error("remove must still garbage-collect the integration's snapshots")
		}
	})

	t.Run("formula_plugin_refused", func(t *testing.T) {
		f := intBRemoveSetup(t)
		before, _ := os.ReadFile(config.PluginsConfigPath(f.e.root))
		_, err := runPlugin(t, "remove", nil, "zeta")
		if err == nil || !strings.Contains(err.Error(), "zeta") || !strings.Contains(err.Error(), "integration") {
			t.Errorf("remove handles integrations only; a formula plugin must be refused naming it; got: %v", err)
		}
		if after, _ := os.ReadFile(config.PluginsConfigPath(f.e.root)); string(after) != string(before) {
			t.Error("a refused remove changed plugins.json")
		}
	})

	t.Run("unknown_name_refused", func(t *testing.T) {
		f := intBRemoveSetup(t)
		before, _ := os.ReadFile(config.PluginsConfigPath(f.e.root))
		_, err := runPlugin(t, "remove", nil, "no-such-integration")
		if err == nil || !strings.Contains(err.Error(), "no-such-integration") {
			t.Errorf("remove of an unrecorded name must be refused naming it; got: %v", err)
		}
		if after, _ := os.ReadFile(config.PluginsConfigPath(f.e.root)); string(after) != string(before) {
			t.Error("a refused remove changed plugins.json")
		}
	})
}
