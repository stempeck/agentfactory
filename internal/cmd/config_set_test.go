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

// TestConfigDispatchSet_TolerantOfIncompleteModelsJson (PR #482): af config dispatch
// set is NOT a profile-selecting path, so a validation-failing models.json (here an
// incomplete endpoint — base_url without auth_token) must NOT hard-fail an
// otherwise-valid dispatch write. Mirrors the launch path's warn-and-fall-through
// policy: warn, drop the model cross-check, proceed.
func TestConfigDispatchSet_TolerantOfIncompleteModelsJson(t *testing.T) {
	root := setupConfigFactory(t)

	// Incomplete endpoint: base_url without auth_token — LoadModelsConfig rejects it.
	if err := os.WriteFile(config.ModelsConfigPath(root),
		[]byte(`{"models":{"local":{"ANTHROPIC_BASE_URL":"http://x:1"}}}`), 0o644); err != nil {
		t.Fatalf("seed models.json: %v", err)
	}

	// A dispatch doc with NO model-bearing mapping — models.json is irrelevant to it.
	body := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"debugger"}]}`
	out, err := runConfigSet(t, runConfigDispatchSet, body)
	if err != nil {
		t.Fatalf("an incomplete models.json must not block an unrelated dispatch write; err=%v out=%q", err, out)
	}
	if !strings.Contains(out, "models.json") {
		t.Errorf("the fall-through must warn about ignoring models.json; out=%q", out)
	}
	if _, e := config.LoadDispatchConfig(root); e != nil {
		t.Fatalf("dispatch.json should have been written despite the bad models.json: %v", e)
	}
}

// TestConfigDispatchSet_ValidModelsJson_UndefinedModel_StillRejected is the
// no-regression guard: when models.json loads cleanly, the per-mapping model
// cross-check must STILL reject a mapping naming an undefined model. The
// tolerance added for a BROKEN models.json must not weaken validation of a GOOD one.
func TestConfigDispatchSet_ValidModelsJson_UndefinedModel_StillRejected(t *testing.T) {
	root := setupConfigFactory(t)

	if err := os.WriteFile(config.ModelsConfigPath(root),
		[]byte(`{"models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8"}}}`), 0o644); err != nil {
		t.Fatalf("seed models.json: %v", err)
	}

	body := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"debugger","model":"ghost-model"}]}`
	_, err := runConfigSet(t, runConfigDispatchSet, body)
	if err == nil {
		t.Fatal("a mapping naming an undefined model must still be rejected when models.json is valid")
	}
	if !strings.Contains(err.Error(), "ghost-model") {
		t.Errorf("rejection should name the undefined model; got %v", err)
	}
}

// TestLoadModelsConfigForCrossCheck_IncompleteEndpoint_WarnsReturnsNil unit-tests the
// shared helper both af dispatch and af config dispatch set use: a validation-failing
// models.json (incomplete endpoint) must warn and fall through to nil (so
// ValidateDispatchConfig skips the model cross-check), never a hard error.
func TestLoadModelsConfigForCrossCheck_IncompleteEndpoint_WarnsReturnsNil(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755)
	os.WriteFile(config.ModelsConfigPath(root),
		[]byte(`{"models":{"local":{"ANTHROPIC_BASE_URL":"http://x:1"}}}`), 0o644)

	var warn bytes.Buffer
	if got := loadModelsConfigForCrossCheck(root, &warn); got != nil {
		t.Errorf("a validation-failing models.json must fall through to nil; got %+v", got)
	}
	if !strings.Contains(warn.String(), "models.json") {
		t.Errorf("must warn about ignoring models.json; got %q", warn.String())
	}
}

// TestLoadModelsConfigForCrossCheck_Valid_ReturnsConfig is the companion: a clean
// models.json loads, is returned (so the cross-check still runs), and does not warn.
func TestLoadModelsConfigForCrossCheck_Valid_ReturnsConfig(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755)
	os.WriteFile(config.ModelsConfigPath(root),
		[]byte(`{"models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8"}}}`), 0o644)

	var warn bytes.Buffer
	got := loadModelsConfigForCrossCheck(root, &warn)
	if got == nil {
		t.Fatal("a valid models.json must be returned, not nil")
	}
	if _, ok := got.Models["opus"]; !ok {
		t.Errorf("returned config missing the 'opus' profile: %+v", got)
	}
	if warn.Len() != 0 {
		t.Errorf("a valid models.json must not warn; got %q", warn.String())
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

// TestConfigModelsSet_RoundTrip (issue #480): `af config models set` reads a
// ModelsConfig on stdin, validates it (via SaveModelsConfig's internal
// validateModelsConfig), and writes models.json atomically — and rejects malformed
// input without touching the file. It also asserts the command is reachable under the
// EXISTING config parent.
func TestConfigModelsSet_RoundTrip(t *testing.T) {
	root := setupConfigFactory(t)
	afDir := filepath.Join(root, ".agentfactory")

	// A valid registry persists and reloads identically.
	body := `{"default":"opus","models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8"},"lmstudio":{"ANTHROPIC_BASE_URL":"http://localhost:1234","ANTHROPIC_AUTH_TOKEN":"lm-studio","ANTHROPIC_MODEL":"local","ANTHROPIC_API_KEY":""}},"agents":{"manager":"opus"}}`
	out, err := runConfigSet(t, runConfigModelsSet, body)
	if err != nil {
		t.Fatalf("runConfigModelsSet: %v (out=%q)", err, out)
	}
	for _, e := range mustReadDir(t, afDir) {
		if strings.HasSuffix(e, ".tmp") {
			t.Errorf("temp residue after atomic write: %s", e)
		}
	}
	loaded, err := config.LoadModelsConfig(root)
	if err != nil {
		t.Fatalf("reload models.json: %v", err)
	}
	if loaded.Default != "opus" {
		t.Errorf("default round-trip = %q, want \"opus\"", loaded.Default)
	}
	if loaded.Agents["manager"] != "opus" {
		t.Errorf("agents map round-trip = %v, want manager->opus", loaded.Agents)
	}
	if loaded.Models["opus"]["ANTHROPIC_MODEL"] != "claude-opus-4-8" {
		t.Errorf("opus profile round-trip mismatch: %+v", loaded.Models["opus"])
	}
	if v, ok := loaded.Models["lmstudio"]["ANTHROPIC_API_KEY"]; !ok || v != "" {
		t.Errorf("empty ANTHROPIC_API_KEY must survive round-trip: present=%v val=%q", ok, v)
	}

	// An invalid registry (incomplete endpoint: base_url without auth_token) is rejected
	// by the on-write validator and leaves the existing file untouched.
	before, _ := os.ReadFile(config.ModelsConfigPath(root))
	bad := `{"models":{"broken":{"ANTHROPIC_BASE_URL":"http://localhost:9999"}}}`
	if _, err := runConfigSet(t, runConfigModelsSet, bad); err == nil {
		t.Error("expected error for an incomplete-endpoint registry")
	}
	after, _ := os.ReadFile(config.ModelsConfigPath(root))
	if !bytes.Equal(before, after) {
		t.Errorf("models.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
	}

	// Malformed JSON on stdin is rejected non-zero.
	if _, err := runConfigSet(t, runConfigModelsSet, `{not json`); err == nil {
		t.Error("expected error for malformed JSON stdin")
	}
}

func TestConfigModelsSet_RegisteredUnderConfig(t *testing.T) {
	findChild := func(parent *cobra.Command, name string) *cobra.Command {
		for _, c := range parent.Commands() {
			if c.Name() == name {
				return c
			}
		}
		return nil
	}
	models := findChild(configCmd, "models")
	if models == nil || findChild(models, "set") == nil {
		t.Error("`config models set` is not registered under configCmd")
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
