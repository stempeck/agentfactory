package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func p724bCheckSnapshot(t *testing.T, root, name string) (string, *config.PluginIntegration) {
	t.Helper()
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	in := cfg.Plugins[name].Integration
	return filepath.Join(root, filepath.FromSlash(in.SnapshotDir)), in
}

func p724bMarkerCheck(marker string) string {
	return "#!/bin/sh\ntouch '" + marker + "'\nexit 0\n"
}

// p724bTamperCheck rewrites the sealed snapshot's [check] script into one that leaves marker when it runs.
func p724bTamperCheck(t *testing.T, snap, marker string) {
	t.Helper()
	if err := intBChmodTree(snap, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "af", "check.sh"), []byte(p724bMarkerCheck(marker)), 0o755); err != nil {
		t.Fatal(err)
	}
}

func p724bRequireNotRun(t *testing.T, marker, out string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("af plugin check executed a [check] run from content that does not bind (marker written); output:\n%s", out)
	}
}

func TestPR724_T5_PluginCheckDriftedSnapshotNotRun(t *testing.T) {
	e := intBCheckEnv(t)
	intBCheckFixture(t, e, "acme-drift", "#!/bin/sh\nexit 0\n", intBManifestOpts{})
	snap, in := p724bCheckSnapshot(t, e.root, "acme-drift")
	marker := filepath.Join(e.base, "TAMPERED_CHECK_RAN")
	p724bTamperCheck(t, snap, marker)

	out, err := runPlugin(t, "check", nil, "acme-drift")

	p724bRequireNotRun(t, marker, out)
	if err == nil {
		t.Errorf("a drifted snapshot is not a passing check: af plugin check must exit non-zero; output:\n%s", out)
	}
	if !strings.Contains(out, "acme-drift: error") || !strings.Contains(out, "content changed since consent") {
		t.Errorf("the drift must be reported as an error row carrying the bind refusal; output:\n%s", out)
	}
	rec := intBReadCheckRecord(t, e.root, "acme-drift")
	if got := intBJSONString(t, rec["state"]); got != integrationCheckError {
		t.Errorf("record state = %q, want %q (the check did not run, its outcome is unknown)", got, integrationCheckError)
	}
	if got := intBJSONInt(t, rec["exit"]); got != -1 {
		t.Errorf("record exit = %d, want -1", got)
	}
	if got := intBJSONString(t, rec["content_sha256"]); got != in.ContentSHA256 {
		t.Errorf("record content_sha256 = %q, want the consented %q", got, in.ContentSHA256)
	}
	if got := intBJSONString(t, rec["output"]); !strings.Contains(got, "content changed since consent") {
		t.Errorf("record output = %q, want the bind refusal", got)
	}
}

func TestPR724_T5_PluginCheckAllRunsIntactSkipsDrifted(t *testing.T) {
	e := intBCheckEnv(t)
	intBCheckFixture(t, e, "acme-ok", intBCheckOKBody, intBManifestOpts{})
	intBCheckFixture(t, e, "acme-drift", "#!/bin/sh\nexit 0\n", intBManifestOpts{})
	snap, _ := p724bCheckSnapshot(t, e.root, "acme-drift")
	marker := filepath.Join(e.base, "TAMPERED_CHECK_RAN")
	p724bTamperCheck(t, snap, marker)

	out, err := runPlugin(t, "check", map[string]string{"all": "true"})

	p724bRequireNotRun(t, marker, out)
	if err == nil {
		t.Errorf("--all with a drifted integration must exit non-zero; output:\n%s", out)
	}
	if !strings.Contains(out, "acme-drift: error") {
		t.Errorf("the drifted integration gets its own error row; output:\n%s", out)
	}
	if !strings.Contains(out, "acme-ok: ok") {
		t.Errorf("the bind refusal is per integration: the intact sibling still runs and passes; output:\n%s", out)
	}
}

func TestPR724_T5_PluginCheckDriftedManifestWithoutCheckIsError(t *testing.T) {
	e := intBCheckEnv(t)
	intBCheckFixture(t, e, "acme-nocheck", "#!/bin/sh\nexit 0\n", intBManifestOpts{})
	snap, _ := p724bCheckSnapshot(t, e.root, "acme-nocheck")
	if err := intBChmodTree(snap, true); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(snap, config.IntegrationManifestFile)
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	const checkTable = "\n[check]\nrun = \"af/check.sh\"\n"
	if !strings.Contains(string(b), checkTable) {
		t.Fatalf("fixture: manifest has no %q table to drop:\n%s", checkTable, b)
	}
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(b), checkTable, "\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runPlugin(t, "check", nil, "acme-nocheck")

	if err == nil {
		t.Errorf("a manifest that dropped its [check] since consent is drift, not a pass: exit must be non-zero; output:\n%s", out)
	}
	if strings.Contains(out, "no [check] declared") {
		t.Errorf("the no-[check] verdict must not come from unverified bytes; output:\n%s", out)
	}
	if !strings.Contains(out, "acme-nocheck: error") || !strings.Contains(out, "content changed since consent") {
		t.Errorf("the drift must be reported as an error row carrying the bind refusal; output:\n%s", out)
	}
}

func TestPR724_T5_PluginCheckNonContentAddressedSnapshotDirNotRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  func(root, snap string) string
	}{
		{"absolute_snapshot_dir", func(_, snap string) string { return snap }},
		{"acquisition_decoy_snapshot_dir", func(root, _ string) string {
			rel, _ := filepath.Rel(root, filepath.Join(config.PluginsDir(root), "acme-addr"))
			return filepath.ToSlash(rel)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := intBCheckEnv(t)
			marker := filepath.Join(e.base, "UNBOUND_CHECK_RAN")
			intBCheckFixture(t, e, "acme-addr", p724bMarkerCheck(marker), intBManifestOpts{})
			snap, _ := p724bCheckSnapshot(t, e.root, "acme-addr")
			cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(e.root))
			if err != nil {
				t.Fatal(err)
			}
			dir := tc.dir(e.root, snap)
			cfg.Plugins["acme-addr"].Integration.SnapshotDir = dir
			if err := config.SavePluginsConfig(config.PluginsConfigPath(e.root), cfg); err != nil {
				t.Fatal(err)
			}
			abs := dir
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(e.root, filepath.FromSlash(dir))
			}
			if _, err := os.Stat(filepath.Join(abs, "af", "check.sh")); err != nil {
				t.Fatalf("fixture: %s must hold the [check] script a raw join would run: %v", dir, err)
			}

			out, err := runPlugin(t, "check", nil, "acme-addr")

			p724bRequireNotRun(t, marker, out)
			if err == nil {
				t.Errorf("a snapshot_dir that is not the content-addressed snapshot must exit non-zero; output:\n%s", out)
			}
			if !strings.Contains(out, "is not its content-addressed snapshot") {
				t.Errorf("the refusal must carry the bind message; output:\n%s", out)
			}
		})
	}
}
