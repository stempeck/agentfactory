package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// ---- AC8: af plugin verify re-hashes integration snapshots (L590-591; D9, D35) ----

// intBVerifyFixture records acme-int as a finished read-only snapshot and returns it.
func intBVerifyFixture(t *testing.T) (*intBEnv, string) {
	t.Helper()
	e := intBFactory(t)
	snap := intBRecord(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}), nil)
	return e, snap
}

func intBCombined(out string, err error) string {
	if err != nil {
		return out + "\n" + err.Error()
	}
	return out
}

func TestPluginVerify_IntegrationSnapshotDrift(t *testing.T) {
	skill := "claude-plugin/skills/" + intBName + "/SKILL.md"

	t.Run("intact_snapshot_ok", func(t *testing.T) {
		intBVerifyFixture(t)
		out, err := runPlugin(t, "verify", nil, intBName)
		if err != nil {
			t.Errorf("an intact snapshot verifies ok; got: %v\n%s", err, out)
		}
		if !strings.Contains(out, intBName) {
			t.Errorf("verify must report the integration %s (it re-hashes the snapshot):\n%s", intBName, out)
		}
	})

	t.Run("edited_file_named", func(t *testing.T) {
		_, snap := intBVerifyFixture(t)
		p := filepath.Join(snap, filepath.FromSlash(skill))
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("tampered\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatal(err)
		}
		out, err := runPlugin(t, "verify", nil, intBName)
		if err == nil {
			t.Errorf("a drifted snapshot must fail verify (non-zero exit):\n%s", out)
		}
		if c := intBCombined(out, err); !strings.Contains(c, skill) {
			t.Errorf("verify must name the edited file %s:\n%s", skill, c)
		}
	})

	t.Run("mode_only_drift_named", func(t *testing.T) {
		_, snap := intBVerifyFixture(t)
		p := filepath.Join(snap, filepath.FromSlash(skill))
		if err := os.Chmod(p, 0o555); err != nil { // content unchanged; normalized mode 0644 -> 0755
			t.Fatal(err)
		}
		out, err := runPlugin(t, "verify", nil, intBName)
		if err == nil {
			t.Errorf("an exec-bit-only change is drift (the hash covers the normalized mode):\n%s", out)
		}
		if c := intBCombined(out, err); !strings.Contains(c, skill) {
			t.Errorf("mode-only drift must still name the file %s (D35):\n%s", skill, c)
		}
	})

	t.Run("missing_snapshot_fails", func(t *testing.T) {
		_, snap := intBVerifyFixture(t)
		if err := intBChmodTree(snap, true); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(snap); err != nil {
			t.Fatal(err)
		}
		out, err := runPlugin(t, "verify", nil, intBName)
		if err == nil {
			t.Errorf("a recorded integration whose snapshot is gone must fail verify:\n%s", out)
		}
		if c := intBCombined(out, err); !strings.Contains(c, intBName) {
			t.Errorf("the failure must name %s:\n%s", intBName, c)
		}
	})

	t.Run("all_warns_unknown_record_key", func(t *testing.T) {
		e, _ := intBVerifyFixture(t)
		path := config.PluginsConfigPath(e.root)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		plugins, _ := doc["plugins"].(map[string]any)
		entry, _ := plugins[intBName].(map[string]any)
		in, _ := entry["integration"].(map[string]any)
		if in == nil {
			t.Fatalf("fixture has no integration object:\n%s", raw)
		}
		in["future_key"] = "from-a-newer-af"
		out, _ := json.MarshalIndent(doc, "", "  ")
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
		vout, verr := runPlugin(t, "verify", map[string]string{"all": "true"})
		if verr != nil {
			t.Errorf("an unknown record key is a warning, not a failure; got: %v\n%s", verr, vout)
		}
		if !strings.Contains(vout, "future_key") {
			t.Errorf("verify --all must warn naming the record key this binary ignores (B10):\n%s", vout)
		}
	})
}
