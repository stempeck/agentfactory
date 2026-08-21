package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

// runConfigModelsSetSplit runs the command with distinct stdout and stderr buffers. The
// shared runConfigSet helper passes one buffer to both, which cannot show which stream a
// line went to — and the whole point of a warning is that it stays out of stdout.
func runConfigModelsSetSplit(t *testing.T, stdin string) (string, string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader(stdin))
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	err := runConfigModelsSet(cmd, nil)
	return outBuf.String(), errBuf.String(), err
}

// A model id the host does not recognise as its own gets a fixed assumed context window, so a
// larger auto-compact window silently caps unless CLAUDE_CODE_MAX_CONTEXT_TOKENS declares the
// real one (issue #602). That combination is incoherent but legal: the registry saves and the
// command warns.
func TestConfigModelsSet_PairingWarning(t *testing.T) {
	const (
		compKey = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"
		codex   = `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"220000"}}}`
	)

	t.Run("the incoherent pairing saves and warns on stderr", func(t *testing.T) {
		root := setupConfigFactory(t)
		stdout, stderr, err := runConfigModelsSetSplit(t, codex)
		if err != nil {
			t.Fatalf("runConfigModelsSet: %v (stderr=%q)", err, stderr)
		}
		for _, want := range []string{"warning:", "codex", compKey, "200000"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr %q should contain %q", stderr, want)
			}
		}
		if !strings.Contains(stdout, "Models configuration saved.") {
			t.Errorf("stdout %q should confirm the save", stdout)
		}
		if strings.Contains(stdout, "warning") {
			t.Errorf("the warning must not reach stdout, got %q", stdout)
		}

		// Saved means saved: the profile has to survive on disk, warning or not.
		loaded, err := config.LoadModelsConfig(root)
		if err != nil {
			t.Fatalf("LoadModelsConfig: %v", err)
		}
		if got := loaded.Models["codex"]["CLAUDE_CODE_AUTO_COMPACT_WINDOW"]; got != "220000" {
			t.Errorf("window did not survive the write: got %q want %q", got, "220000")
		}
	})

	t.Run("declaring the companion silences it", func(t *testing.T) {
		setupConfigFactory(t)
		body := `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"220000","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"250000"}}}`
		stdout, stderr, err := runConfigModelsSetSplit(t, body)
		if err != nil {
			t.Fatalf("runConfigModelsSet: %v (stderr=%q)", err, stderr)
		}
		if stderr != "" {
			t.Errorf("expected no warning once the companion is declared, got %q", stderr)
		}
		if !strings.Contains(stdout, "Models configuration saved.") {
			t.Errorf("stdout %q should confirm the save", stdout)
		}
	})

	// Each of these is legal AND coherent, so none may warn. Together they pin every
	// conjunct of the lint: an over-eager predicate fails at least one of them.
	silent := []struct {
		name string
		body string
	}{
		{"a claude- model derives its own window", `{"models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"220000"}}}`},
		{"a window below the cap is honoured in full", `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"150000"}}}`},
		{"a window exactly at the cap is not capped", `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"200000"}}}`},
		// The Go zero value for an absent ANTHROPIC_MODEL is "", which is not claude-
		// prefixed — a model-less profile must not be mistaken for a foreign one.
		{"a profile with no model id is not foreign", `{"models":{"nomodel":{"CLAUDE_CODE_AUTO_COMPACT_WINDOW":"220000"}}}`},
		{"a profile with no exports at all", `{"models":{"blank":{}}}`},
		{"a foreign model with no window declared", `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol"}}}`},
		{"an empty window defers to the host", `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":""}}}`},
	}
	for _, tc := range silent {
		t.Run("no warning: "+tc.name, func(t *testing.T) {
			setupConfigFactory(t)
			_, stderr, err := runConfigModelsSetSplit(t, tc.body)
			if err != nil {
				t.Fatalf("runConfigModelsSet: %v (stderr=%q)", err, stderr)
			}
			if stderr != "" {
				t.Errorf("expected no warning, got %q", stderr)
			}
		})
	}

	t.Run("an empty companion counts as absent", func(t *testing.T) {
		setupConfigFactory(t)
		body := `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"220000","CLAUDE_CODE_MAX_CONTEXT_TOKENS":""}}}`
		_, stderr, err := runConfigModelsSetSplit(t, body)
		if err != nil {
			t.Fatalf("runConfigModelsSet: %v", err)
		}
		if !strings.Contains(stderr, compKey) {
			t.Errorf("an empty companion must still warn, got %q", stderr)
		}
	})

	t.Run("warnings are ordered deterministically", func(t *testing.T) {
		setupConfigFactory(t)
		body := `{"models":{` +
			`"zeta":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"220000"},` +
			`"alpha":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"220000"},` +
			`"mike":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"220000"}}}`
		_, first, err := runConfigModelsSetSplit(t, body)
		if err != nil {
			t.Fatalf("runConfigModelsSet: %v", err)
		}
		if n := strings.Count(first, "warning:"); n != 3 {
			t.Fatalf("expected one warning per profile, got %d in %q", n, first)
		}
		if !(strings.Index(first, "alpha") < strings.Index(first, "mike") &&
			strings.Index(first, "mike") < strings.Index(first, "zeta")) {
			t.Errorf("warnings should be sorted by profile name, got %q", first)
		}
		// Map range order varies per run, so a single pass cannot prove determinism.
		for i := 0; i < 20; i++ {
			_, again, err := runConfigModelsSetSplit(t, body)
			if err != nil {
				t.Fatalf("iter %d: %v", i, err)
			}
			if again != first {
				t.Fatalf("iter %d nondeterministic:\n got %q\nfirst %q", i, again, first)
			}
		}
	})

	t.Run("an out-of-range window is rejected and nothing is written", func(t *testing.T) {
		root := setupConfigFactory(t)
		path := config.ModelsConfigPath(root)

		// A fresh factory has no models.json, so the byte-comparison below would be
		// vacuously true against an absent file. Assert nothing is created first.
		bad := `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_AUTO_COMPACT_WINDOW":"2000000"}}}`
		stdout, stderr, err := runConfigModelsSetSplit(t, bad)
		if err == nil {
			t.Fatal("expected an error for an out-of-range window")
		}
		if strings.Contains(stdout, "saved") {
			t.Errorf("a rejected registry must not report a save, got %q", stdout)
		}
		// The lint describes what a saved profile will do, so a registry that was never
		// written must not be linted at all — this pins the emission after the write.
		if strings.Contains(stderr, "warning:") {
			t.Errorf("a rejected registry must not be linted, got %q", stderr)
		}
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("a rejected write must not create models.json (stat err=%v)", statErr)
		}

		// And with a good registry already on disk, a rejected write leaves it byte-identical.
		if _, err := runConfigSet(t, runConfigModelsSet, codex); err != nil {
			t.Fatalf("seeding a valid registry: %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read seeded models.json: %v", err)
		}
		if _, _, err := runConfigModelsSetSplit(t, bad); err == nil {
			t.Error("expected an error for an out-of-range window")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read models.json after the rejected write: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("models.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
		}
	})
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

// TestConfigStatuslineSet_RoundTrip (issue #591): `af config statusline set` reads a
// StatuslineConfig on stdin, validates it (via SaveStatuslineConfig's internal
// validateStatuslineConfig), and writes statusline.json atomically — and rejects an
// unknown element without touching the file. Mirrors TestConfigModelsSet_RoundTrip.
func TestConfigStatuslineSet_RoundTrip(t *testing.T) {
	root := setupConfigFactory(t)
	afDir := filepath.Join(root, ".agentfactory")

	// A valid config persists and reloads identically (element order preserved).
	body := `{"elements":["model","dir"]}`
	out, err := runConfigSet(t, runConfigStatuslineSet, body)
	if err != nil {
		t.Fatalf("runConfigStatuslineSet: %v (out=%q)", err, out)
	}
	for _, e := range mustReadDir(t, afDir) {
		if strings.HasSuffix(e, ".tmp") {
			t.Errorf("temp residue after atomic write: %s", e)
		}
	}
	loaded, err := config.LoadStatuslineConfig(root)
	if err != nil {
		t.Fatalf("reload statusline.json: %v", err)
	}
	if len(loaded.Elements) != 2 || loaded.Elements[0] != "model" || loaded.Elements[1] != "dir" {
		t.Errorf("statusline round-trip mismatch: %+v", loaded)
	}

	// An unknown element is rejected (exit non-zero) and leaves the existing file untouched.
	before, _ := os.ReadFile(config.StatuslineConfigPath(root))
	if _, err := runConfigSet(t, runConfigStatuslineSet, `{"elements":["kost"]}`); err == nil {
		t.Error("expected error for an unknown statusline element")
	}
	after, _ := os.ReadFile(config.StatuslineConfigPath(root))
	if !bytes.Equal(before, after) {
		t.Errorf("statusline.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
	}

	// Malformed JSON on stdin is rejected non-zero.
	if _, err := runConfigSet(t, runConfigStatuslineSet, `{not json`); err == nil {
		t.Error("expected error for malformed JSON stdin")
	}
}

// TestConfigStatuslineSet_WrongKeyRejectedNothingWritten is the H-R1 regression. A document with
// a misspelled or wrong top-level key used to decode cleanly to a nil Elements, validate, write
// {"elements": null}, and print success — blanking the operator's statusline on a typo. The
// document must be REJECTED, and the rejection must echo the schema.
//
// Since issue #620 Phase 1 the setters decode strictly, so THIS input is now caught a step
// earlier, at decode. The assertions below are unchanged and still hold, because both paths
// raise the same text (statuslineSchemaReject). The nil-Elements guard is still the one that
// catches a document like `{}`, which carries no unknown key — see
// TestConfigStatuslineSet_UnknownKeyStillEchoesSchema.
func TestConfigStatuslineSet_WrongKeyRejectedNothingWritten(t *testing.T) {
	root := setupConfigFactory(t)

	// Seed a good file so "nothing was written" is provable rather than vacuous.
	if _, err := runConfigSet(t, runConfigStatuslineSet, `{"elements":["model","dir"]}`); err != nil {
		t.Fatalf("seeding a valid config: %v", err)
	}
	before, err := os.ReadFile(config.StatuslineConfigPath(root))
	if err != nil {
		t.Fatalf("read seeded config: %v", err)
	}

	out, err := runConfigSet(t, runConfigStatuslineSet, `{"statusline":["model","branch"]}`)
	if err == nil {
		t.Fatal("a wrong-key document must be rejected, never treated as an empty element list")
	}
	// The error has to echo the schema: name the real key AND carry a usable example, or the
	// operator learns nothing from a rejection they did not expect.
	for _, want := range []string{"elements", `{"elements":`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("rejection error must contain %q; got %q", want, err.Error())
		}
	}
	if strings.Contains(out, "saved") {
		t.Errorf("a rejected write must not print a success message; got %q", out)
	}
	after, _ := os.ReadFile(config.StatuslineConfigPath(root))
	if !bytes.Equal(before, after) {
		t.Errorf("statusline.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
	}

	// On an ABSENT file the reject must create nothing. The seeded case alone would still pass
	// if the guard ran AFTER a write that happened to reproduce the same bytes.
	fresh := setupConfigFactory(t)
	if _, err := runConfigSet(t, runConfigStatuslineSet, `{"statusline":["model"]}`); err == nil {
		t.Fatal("a wrong-key document must be rejected on a fresh factory too")
	}
	if _, err := os.Stat(config.StatuslineConfigPath(fresh)); !os.IsNotExist(err) {
		t.Errorf("a rejected write created statusline.json on a fresh factory (stat err = %v)", err)
	}

	// An explicit empty list stays legal: an operator CAN ask to render nothing. Only null is
	// ambiguous, and only null is rejected.
	if _, err := runConfigSet(t, runConfigStatuslineSet, `{"elements":[]}`); err != nil {
		t.Errorf("an explicit empty element list must remain valid; got %v", err)
	}
}

// TestConfigStatuslineSet_LoadPathStaysFailOpen pins the deliberate asymmetry: the nil guard is
// on the loud editing surface only. An on-disk {"elements":null} — however it got there — still
// loads without error and renders nothing. Reviewers should not "fix" this to match `set`.
func TestConfigStatuslineSet_LoadPathStaysFailOpen(t *testing.T) {
	root := setupConfigFactory(t)
	if err := os.WriteFile(config.StatuslineConfigPath(root), []byte(`{"elements":null}`), 0o644); err != nil {
		t.Fatalf("write nulled config: %v", err)
	}
	cfg, err := config.LoadStatuslineConfig(root)
	if err != nil {
		t.Fatalf("the load path must stay fail-open on a nulled elements key; got %v", err)
	}
	if len(cfg.Elements) != 0 {
		t.Errorf("a nulled elements key must load as an empty render, not the default; got %v", cfg.Elements)
	}
}

// TestConfigStatuslineGet_RoundTrip pins the AC that `af config statusline get |
// af config statusline set` round-trips. The fixed-point clause is the load-bearing one: it is
// what catches an effective-vs-stored mismatch, such as a nil color printed as absent and
// therefore silently discarded on the way back in.
func TestConfigStatuslineGet_RoundTrip(t *testing.T) {
	setupConfigFactory(t) // no statusline.json ⇒ get must print the EFFECTIVE default

	doc, err := runConfigSet(t, runConfigStatuslineGet, "")
	if err != nil {
		t.Fatalf("runConfigStatuslineGet: %v", err)
	}

	var got config.StatuslineConfig
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("get did not print a JSON document (%v): %q", err, doc)
	}
	if !reflect.DeepEqual(got.Elements, config.DefaultStatuslineElements()) {
		t.Errorf("get on a fresh factory = %v, want the effective default %v",
			got.Elements, config.DefaultStatuslineElements())
	}
	// Effective, not stored: color is absent on disk but ON in effect, so it must be printed.
	if got.Color == nil {
		t.Error("get must materialize color rather than printing it absent; piping the output " +
			"back through set would otherwise discard the effective value")
	} else if !*got.Color {
		t.Error("color defaults to ON when unset")
	}

	// The document `get` printed is accepted verbatim by `set`...
	if out, err := runConfigSet(t, runConfigStatuslineSet, doc); err != nil {
		t.Fatalf("set rejected the document get produced: %v (out=%q)", err, out)
	}
	// ...and a second get returns the same bytes. That is the actual round-trip.
	doc2, err := runConfigSet(t, runConfigStatuslineGet, "")
	if err != nil {
		t.Fatalf("second runConfigStatuslineGet: %v", err)
	}
	if doc2 != doc {
		t.Errorf("get|set|get is not a fixed point:\nfirst =%q\nsecond=%q", doc, doc2)
	}
}

func TestConfigStatuslineSet_RegisteredUnderConfig(t *testing.T) {
	findChild := func(parent *cobra.Command, name string) *cobra.Command {
		for _, c := range parent.Commands() {
			if c.Name() == name {
				return c
			}
		}
		return nil
	}
	statusline := findChild(configCmd, "statusline")
	if statusline == nil || findChild(statusline, "set") == nil {
		t.Error("`config statusline set` is not registered under configCmd")
	}
	if statusline == nil || findChild(statusline, "get") == nil {
		t.Error("`config statusline get` is not registered under configCmd")
	}
	// No --json flag on either verb: set only ever reads JSON and get only ever writes it, so a
	// flag would be a dead control (config_set.go's init comment).
	if get := findChild(statusline, "get"); get != nil && get.Flags().Lookup("json") != nil {
		t.Error("`config statusline get` must not take a --json flag; it always emits JSON")
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
