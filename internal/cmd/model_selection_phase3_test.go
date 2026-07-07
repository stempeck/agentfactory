package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// writeSecretFile stages a secret file at <root>/<rel> so the W3 preflight's os.Stat
// succeeds (issue #508). Returns the absolute path.
func writeSecretFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir secret dir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	return p
}

// writeAttestationFixture stages a valid factory-root fitness attestation so the W13
// interlock reads it as attested. Written directly (not via `attest`) to keep the
// resolver test decoupled from the command's writer.
func writeAttestationFixture(t *testing.T, root, profile string) {
	t.Helper()
	dir := filepath.Join(root, ".runtime", "model_fitness")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir model_fitness: %v", err)
	}
	body := fmt.Sprintf(`{"profile":%q,"attested_by":"tester","attested_at":"2026-07-06T00:00:00Z","stages":{"transport":"pass"}}`, profile)
	if err := os.WriteFile(filepath.Join(dir, profile+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write attestation: %v", err)
	}
}

// codexProfile is a non-loopback remote profile whose auth token is a file: ref.
func codexModels() *config.ModelsConfig {
	return &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	}
}

// --- W3: launch preflight (AC-3) ---

func TestResolveLaunchModelEnv_MissingSecretFile_SelectingFailsFast(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, codexModels())
	// deliberately do NOT create secrets/codex.key

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn)
	if err == nil {
		t.Fatalf("expected fail-fast for a missing file: secret on a selecting launch; got name=%q env=%v", name, env)
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("error must name the profile 'codex'; got: %v", err)
	}
	wantPath := filepath.Join(dir, "secrets", "codex.key")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("error must name the resolved secret path %q; got: %v", wantPath, err)
	}
	if env != nil {
		t.Errorf("no export set must be produced on fail-fast; got: %v", env)
	}
}

func TestResolveLaunchModelEnv_MissingSecretFile_RespawnWarnsFallsThrough(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	cfg := codexModels()
	cfg.Agents = map[string]string{"manager": "codex"}
	writeValidModels(t, dir, cfg)
	// secret file absent

	var warn bytes.Buffer
	// cliModel "" => respawn / non-selecting.
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", "", false, &warn)
	if err != nil {
		t.Fatalf("a respawn must NOT fail on a missing secret; got err: %v", err)
	}
	if name != "" || len(env) != 0 {
		t.Errorf("respawn with a missing secret must fall through to the global default (\"\", nil); got name=%q env=%v", name, env)
	}
	w := warn.String()
	if !strings.Contains(w, "codex") {
		t.Errorf("warn must name the abandoned model 'codex'; got: %q", w)
	}
	if !strings.Contains(w, filepath.Join(dir, "secrets", "codex.key")) {
		t.Errorf("warn must name the missing secret path; got: %q", w)
	}
	if !strings.Contains(w, "global default") {
		t.Errorf("warn must say it is falling back to the global default; got: %q", w)
	}
}

// --- W13: fitness attestation interlock (AC-5) ---

func TestResolveLaunchModelEnv_UnattestedNonLoopback_SelectingRefused(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, codexModels())
	writeSecretFile(t, dir, "secrets/codex.key", "sk-real-value") // W3 passes; W13 is the gate
	// no attestation

	var warn bytes.Buffer
	_, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn)
	if err == nil {
		t.Fatal("a selecting launch of an unattested non-loopback profile must be refused")
	}
	if !strings.Contains(err.Error(), "af config models attest") {
		t.Errorf("refusal must name `af config models attest`; got: %v", err)
	}
	if !strings.Contains(err.Error(), "--skip-fitness") {
		t.Errorf("refusal must name `--skip-fitness`; got: %v", err)
	}
	if env != nil {
		t.Errorf("no export set must be produced on refusal; got: %v", env)
	}
}

func TestResolveLaunchModelEnv_LoopbackProfile_LaunchesUnaffected(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"lmstudio": {
				"ANTHROPIC_MODEL":      "local",
				"ANTHROPIC_BASE_URL":   "http://localhost:1234",
				"ANTHROPIC_AUTH_TOKEN": "lm-studio",
				"ANTHROPIC_API_KEY":    "",
			},
		},
	})

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "lmstudio", "", false, &warn)
	if err != nil {
		t.Fatalf("a loopback profile must be exempt from the attestation interlock; got err: %v", err)
	}
	if name != "lmstudio" || len(env) == 0 {
		t.Errorf("loopback selecting launch must succeed with an export set; got name=%q env=%v", name, env)
	}
}

func TestResolveLaunchModelEnv_NoEndpointProfile_NeedsNoAttestation(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{"sonnet": {"ANTHROPIC_MODEL": "claude-sonnet-4-6"}},
	})

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "sonnet", "", false, &warn)
	if err != nil {
		t.Fatalf("a plain model-id profile (no endpoint) must NOT require attestation (AC-1 no-regression); got err: %v", err)
	}
	if name != "sonnet" || modelEnvValue(env, "ANTHROPIC_MODEL") != "claude-sonnet-4-6" {
		t.Errorf("no-endpoint selecting launch must succeed; got name=%q env=%v", name, env)
	}
}

func TestResolveLaunchModelEnv_SkipFitness_ProceedsWithLog(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, codexModels())
	writeSecretFile(t, dir, "secrets/codex.key", "sk-real-value")

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", true, &warn)
	if err != nil {
		t.Fatalf("--skip-fitness must let the launch proceed; got err: %v", err)
	}
	if name != "codex" || len(env) == 0 {
		t.Errorf("skip-fitness launch must return the export set; got name=%q env=%v", name, env)
	}
	if !strings.Contains(warn.String(), "skip-fitness") {
		t.Errorf("skip-fitness must emit a loud override log line; got: %q", warn.String())
	}
}

func TestResolveLaunchModelEnv_Respawn_NonLoopback_NeverBricks(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	cfg := codexModels()
	cfg.Agents = map[string]string{"manager": "codex"}
	writeValidModels(t, dir, cfg)
	writeSecretFile(t, dir, "secrets/codex.key", "sk-real-value")
	// no attestation

	var warn bytes.Buffer
	// cliModel "" => respawn: must never reach the refuse branch.
	_, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", "", false, &warn)
	if err != nil {
		t.Fatalf("a respawn of an unattested non-loopback profile must never brick; got err: %v", err)
	}
}

func TestResolveLaunchModelEnv_Attested_NonLoopback_Launches(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, codexModels())
	writeSecretFile(t, dir, "secrets/codex.key", "sk-real-value")
	writeAttestationFixture(t, dir, "codex")

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn)
	if err != nil {
		t.Fatalf("an attested non-loopback profile must launch; got err: %v", err)
	}
	if name != "codex" || len(env) == 0 {
		t.Errorf("attested selecting launch must return the export set; got name=%q env=%v", name, env)
	}
}

// --- W3/W13 edge paths (present in the code; pinned per code-review suggestion) ---

func TestResolveLaunchModelEnv_EmptySecretFile_SelectingFailsFast(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, codexModels())
	writeSecretFile(t, dir, "secrets/codex.key", "") // exists but zero-length

	var warn bytes.Buffer
	_, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn)
	if err == nil {
		t.Fatal("an empty (zero-length) secret file must fail the preflight on a selecting launch")
	}
	if env != nil {
		t.Errorf("no export set must be produced on fail-fast; got %v", env)
	}
}

func TestResolveLaunchModelEnv_AbsoluteSecretPath_MissingFailsFast(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	absPath := filepath.Join(t.TempDir(), "abs-codex.key") // absolute, deliberately not created
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:" + absPath,
			},
		},
	})

	var warn bytes.Buffer
	_, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn)
	if err == nil {
		t.Fatal("a missing absolute file: secret path must fail fast")
	}
	if !strings.Contains(err.Error(), absPath) {
		t.Errorf("error must name the absolute secret path %q (used as-is, not joined to root); got %v", absPath, err)
	}
}

func TestResolveLaunchModelEnv_CorruptAttestation_FailsClosed(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, codexModels())
	writeSecretFile(t, dir, "secrets/codex.key", "sk-real-value") // W3 passes

	// A corrupt (non-JSON) attestation must be treated as UNATTESTED (fail-closed).
	fitDir := filepath.Join(dir, ".runtime", "model_fitness")
	if err := os.MkdirAll(fitDir, 0o755); err != nil {
		t.Fatalf("mkdir model_fitness: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fitDir, "codex.json"), []byte("not json{{{"), 0o644); err != nil {
		t.Fatalf("write corrupt attestation: %v", err)
	}

	var warn bytes.Buffer
	_, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn)
	if err == nil {
		t.Fatal("a corrupt attestation must be treated as unattested (fail-closed) and refused")
	}
	if !strings.Contains(err.Error(), "af config models attest") {
		t.Errorf("refusal must name the attest command; got %v", err)
	}
}
