package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// ---- AC6: af plugin acquire (L603-608; decisions D7, D22) -------------------

func TestPluginAcquire_CopiesWithExecRuleAndMarker(t *testing.T) {
	e := intBFactory(t)
	src := intBSource(e, intBName, intBManifestOpts{})
	intBSetAcquireFS(t, intBMapFS(intBName, src))
	if err := os.MkdirAll(config.StoreDir(e.root), 0o755); err != nil { // an initialised factory has store/
		t.Fatal(err)
	}
	storeBefore := snapshotTree(t, config.StoreDir(e.root))

	out, err := runPlugin(t, "acquire", nil, intBName)
	if err != nil {
		t.Fatalf("operator acquire of an embedded integration must succeed; got: %v\noutput:\n%s", err, out)
	}

	dir := filepath.Join(config.PluginsDir(e.root), intBName)
	execPaths := map[string]bool{ // bin/* and the manifest-declared run paths (N12)
		"af/install.sh": true, "af/check.sh": true, "af/serve.sh": true,
		"claude-plugin/bin/" + intBName + "-tool": true,
	}
	for rel, f := range src {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Errorf("acquire did not copy %s: %v", rel, rerr)
			continue
		}
		if string(b) != f.body {
			t.Errorf("%s content differs from the embedded copy", rel)
		}
		info, serr := os.Stat(p)
		if serr != nil {
			t.Errorf("stat %s: %v", rel, serr)
			continue
		}
		want := os.FileMode(0o644)
		if execPaths[rel] {
			want = 0o755
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %v, want %v (exec rule: bin/* and run paths 0755, else 0644)", rel, got, want)
		}
	}

	// D7: provenance comes from acquire's marker, not git, and never from plugins.json.
	u, uerr := buildCollisionUniverse(e.root)
	if uerr != nil {
		t.Fatalf("buildCollisionUniverse: %v", uerr)
	}
	pi, perr := enumeratePlugin(e.root, intBName, u)
	if perr != nil {
		t.Fatalf("enumeratePlugin(acquired): %v", perr)
	}
	if pi.Source != "embedded" || pi.Commit != Version {
		t.Errorf("acquired provenance = (%q, %q), want (embedded, %q): the acquire marker replaces runGitProvenance (D7)", pi.Source, pi.Commit, Version)
	}

	// Acquisition is inert: no record, no snapshot, no formula staged.
	if intBExists(config.PluginsConfigPath(e.root)) {
		t.Error("acquire wrote plugins.json; its writers are install and remove only (design L363)")
	}
	if intBExists(config.IntegrationsDir(e.root)) {
		t.Error("acquire created store/integrations/; only install stages snapshots")
	}
	if intBExists(config.FormulasDir(e.root)) {
		t.Error("acquire created store/formulas/")
	}
	after := snapshotTree(t, config.StoreDir(e.root))
	for rel := range after {
		if _, had := storeBefore[rel]; had {
			continue
		}
		if !strings.HasPrefix(rel, filepath.Join("plugins", intBName)+string(filepath.Separator)) {
			t.Errorf("acquire wrote %s outside store/plugins/%s/", rel, intBName)
		}
	}
	intBWantSeamsUntouched(t, e)
}

func TestPluginAcquire_RefusesExistingDir(t *testing.T) {
	e := intBFactory(t)
	intBSetAcquireFS(t, intBMapFS(intBName, intBSource(e, intBName, intBManifestOpts{})))
	dir := filepath.Join(config.PluginsDir(e.root), intBName)
	intBWriteTree(t, dir, map[string]intBFile{"keep.txt": {"operator's own clone\n", 0o644}})
	before := snapshotTree(t, config.StoreDir(e.root))

	out, err := runPlugin(t, "acquire", nil, intBName)
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Errorf("acquire onto an existing store/plugins/%s must be refused naming the path %s (D22); got err=%v\noutput:\n%s", intBName, dir, err, out)
	}
	if after := snapshotTree(t, config.StoreDir(e.root)); !reflect.DeepEqual(before, after) {
		t.Errorf("a refused acquire changed the store:\n before=%v\n after=%v", before, after)
	}
}

func TestPluginAcquire_UnknownNameRefused(t *testing.T) {
	e := intBFactory(t)
	intBSetAcquireFS(t, intBMapFS(intBName, intBSource(e, intBName, intBManifestOpts{})))

	out, err := runPlugin(t, "acquire", nil, "no-such-integration")
	if err == nil || !strings.Contains(err.Error(), "no-such-integration") {
		t.Errorf("acquire of a name af does not embed must be refused naming it; got err=%v\noutput:\n%s", err, out)
	}
	if intBExists(filepath.Join(config.PluginsDir(e.root), "no-such-integration")) {
		t.Error("a refused acquire created the acquisition dir")
	}
}

func TestPluginAcquire_OperatorOnly(t *testing.T) {
	e := intBFactory(t)
	intBSetAcquireFS(t, intBMapFS(intBName, intBSource(e, intBName, intBManifestOpts{})))
	t.Setenv("AF_ROLE", "x")
	before := snapshotTree(t, config.ConfigDir(e.root))

	_, err := runPlugin(t, "acquire", nil, intBName)
	want := "af plugin acquire" + intBOperatorOnly
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("agent-context acquire must be refused with %q; got: %v", want, err)
	}
	if after := snapshotTree(t, config.ConfigDir(e.root)); !reflect.DeepEqual(before, after) {
		t.Errorf("agent-context acquire wrote under the factory config dir:\n before=%v\n after=%v", before, after)
	}
}

// embed.FS carries no modes, so acquire derives the exec bit from a #! first line over the whole tree.
func TestPR724_T10_AcquireMarksShebangFilesExecutable(t *testing.T) {
	e := intBFactory(t)
	src := p724e1HookSource(e, 0o755)
	src["af/lib/helper.sh"] = intBFile{"#!/bin/sh\necho helper\n", 0o755}
	intBSetAcquireFS(t, intBMapFS(intBName, src))
	if out, err := runPlugin(t, "acquire", nil, intBName); err != nil {
		t.Fatalf("acquire: %v\n%s", err, out)
	}
	dir := filepath.Join(config.PluginsDir(e.root), intBName)
	for rel, want := range map[string]os.FileMode{
		p724e1Guard:        0o755,
		"af/lib/helper.sh": 0o755,
		p724e1HooksJSON:    0o644,
	} {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("acquire did not copy %s: %v", rel, err)
			continue
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("acquired %s mode = %v, want %v (a file starting with #! is executable, anything else outside the exec rule 0644)", rel, got, want)
		}
	}
}
