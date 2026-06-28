package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// setupConfigFactory creates a temp factory root with factory.json + agents.json
// (debugger + manager) and chdirs into it.
func setupConfigFactory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"debugger":{"type":"autonomous","description":"d"},"manager":{"type":"interactive","description":"m"}}}`), 0o644)
	t.Chdir(root)
	return root
}

func runConfigSet(t *testing.T, fn func(*cobra.Command, []string) error, stdin string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader(stdin))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := fn(cmd, nil)
	return buf.String(), err
}

func TestConfigDispatchSet_RejectsUnknownAgent(t *testing.T) {
	root := setupConfigFactory(t)

	// Pre-existing valid file so we can prove it is left untouched on failure.
	good := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"debugger"}],"notify_on_complete":"manager"}`
	if err := os.WriteFile(config.DispatchConfigPath(root), []byte(good), 0o644); err != nil {
		t.Fatalf("seed dispatch.json: %v", err)
	}
	before, _ := os.ReadFile(config.DispatchConfigPath(root))

	body := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"ghost"}]}`
	_, err := runConfigSet(t, runConfigDispatchSet, body)
	if err == nil {
		t.Fatal("expected non-zero (error) for a mapping to an unknown agent")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should name the unknown agent", err.Error())
	}

	// The on-disk file must be byte-for-byte unchanged.
	after, _ := os.ReadFile(config.DispatchConfigPath(root))
	if !bytes.Equal(before, after) {
		t.Errorf("dispatch.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestConfigSet_AtomicValidatedWrite(t *testing.T) {
	root := setupConfigFactory(t)
	afDir := filepath.Join(root, ".agentfactory")

	// Dispatch: a valid edit persists and reloads identically.
	body := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"debugger"}],"notify_on_complete":"manager","interval_seconds":600}`
	out, err := runConfigSet(t, runConfigDispatchSet, body)
	if err != nil {
		t.Fatalf("runConfigDispatchSet: %v (out=%q)", err, out)
	}
	for _, e := range mustReadDir(t, afDir) {
		if strings.HasSuffix(e, ".tmp") {
			t.Errorf("temp residue after atomic write: %s", e)
		}
	}
	disp, err := config.LoadDispatchConfig(root)
	if err != nil {
		t.Fatalf("reload dispatch.json: %v", err)
	}
	if len(disp.Mappings) != 1 || disp.Mappings[0].Agent != "debugger" || disp.NotifyOnComplete != "manager" {
		t.Errorf("dispatch round-trip mismatch: %+v", disp)
	}

	// Startup: a valid edit persists and reloads identically.
	startupBody := `{"agents":["manager"],"quality":"on","fidelity":"default","start_dispatch":true,"watchdog_agents":["manager"]}`
	if _, err := runConfigSet(t, runConfigStartupSet, startupBody); err != nil {
		t.Fatalf("runConfigStartupSet: %v", err)
	}
	st, err := config.LoadStartupConfig(root)
	if err != nil {
		t.Fatalf("reload startup.json: %v", err)
	}
	if st.Quality != "on" || !st.StartDispatch {
		t.Errorf("startup round-trip mismatch: %+v", st)
	}

	// Invalid JSON is rejected (exit non-zero), file path not corrupted.
	if _, err := runConfigSet(t, runConfigStartupSet, `{not json`); err == nil {
		t.Error("expected error for malformed JSON stdin")
	}
}

func TestConfigSet_CommandsRegisteredUnderConfig(t *testing.T) {
	// `af config dispatch set` and `af config startup set` must be reachable
	// under the EXISTING config parent (no duplicate parent).
	findChild := func(parent *cobra.Command, name string) *cobra.Command {
		for _, c := range parent.Commands() {
			if c.Name() == name {
				return c
			}
		}
		return nil
	}
	dispatch := findChild(configCmd, "dispatch")
	if dispatch == nil || findChild(dispatch, "set") == nil {
		t.Error("`config dispatch set` is not registered under configCmd")
	}
	startup := findChild(configCmd, "startup")
	if startup == nil || findChild(startup, "set") == nil {
		t.Error("`config startup set` is not registered under configCmd")
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
