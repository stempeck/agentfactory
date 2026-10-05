package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// pinFixtureWrite writes a v1 integration pin straight to disk so a reader-side test does not depend on the
// production writer it is not about.
func pinFixtureWrite(t *testing.T, agentDir string, pin integrationPin) string {
	t.Helper()
	if pin.V == 0 {
		pin.V = 1
	}
	if pin.Bindings == nil {
		pin.Bindings = []integrationPinBinding{}
	}
	if pin.Skipped == nil {
		pin.Skipped = []integrationPinSkipped{}
	}
	b, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(agentDir, ".runtime", integrationPinFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// pinFixtureBinding describes the recorded snapshot snapAbs of integration name the way the pin stores it:
// root-relative, with the snapshot's content hash (its directory name).
func pinFixtureBinding(t *testing.T, root, name, snapAbs string, required bool) integrationPinBinding {
	t.Helper()
	rel, err := filepath.Rel(root, snapAbs)
	if err != nil {
		t.Fatal(err)
	}
	return integrationPinBinding{
		Name:          name,
		SnapshotDir:   filepath.ToSlash(rel),
		ContentSHA256: filepath.Base(snapAbs),
		EnvKeys:       []string{},
		Required:      required,
		ClaudePlugins: []string{name + "-plugin"},
	}
}

func pinFixturePath(agentDir string) string {
	return filepath.Join(agentDir, ".runtime", integrationPinFile)
}

func pinFixtureRead(t *testing.T, agentDir string) (integrationPin, bool) {
	t.Helper()
	b, err := os.ReadFile(pinFixturePath(agentDir))
	if os.IsNotExist(err) {
		return integrationPin{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var pin integrationPin
	if err := json.Unmarshal(b, &pin); err != nil {
		t.Fatalf("pin %s is not JSON: %v\n%s", pinFixturePath(agentDir), err, b)
	}
	return pin, true
}

func snapshotParent(root, name string) string {
	return filepath.Join(config.IntegrationsDir(root), name)
}
