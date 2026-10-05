package config

import (
	"strings"
	"testing"
)

// p724cLaunchFamilyKeys are the keys af's launch line exports on its own authority: git identity, the
// trailer hook, co-author, build host, and the effort level. The config package cannot import session,
// so this is a literal copy; session's drift guard ties it back to session.managerOwnedVars.
var p724cLaunchFamilyKeys = []string{
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
	"AF_COAUTHOR_NAME", "AF_COAUTHOR_EMAIL",
	"AF_BUILD_MODE", "AF_BUILD_HOST", "AF_BUILD_USER", "AF_HOST_MOUNT",
	EnvEffortLevel,
}

func p724cWantLaunchKeyRefusal(t *testing.T, key, value string) {
	t.Helper()
	dir := imPluginDir(t, "acme", imHeader+imService+imEnv(key+` = "`+value+`"`))
	m, err := LoadIntegrationManifest(dir)
	if err == nil || m != nil {
		t.Fatalf("[env] %s = %q loaded (manifest %v, err %v); a manifest may not set a key af's launch line owns", key, value, m, err)
	}
	if want := "may not set " + key + " (af launch key)"; !strings.Contains(err.Error(), want) {
		t.Errorf("[env] %s = %q refused with %q, want the clause %q", key, value, err, want)
	}
}

// The denial is by key: a file: reference passes the secret-ref root check and would be dereferenced into
// the pane, so it must be refused exactly like a literal.
func TestPR724_T6_ManifestRefusesAFLaunchKeys(t *testing.T) {
	for _, key := range p724cLaunchFamilyKeys {
		for name, value := range map[string]string{"literal": "x", "file_ref": "file:.agentfactory/secrets/x"} {
			t.Run(key+"/"+name, func(t *testing.T) { p724cWantLaunchKeyRefusal(t, key, value) })
		}
	}
}

// An empty value is refused too: an integration exporting an empty ANTHROPIC_API_KEY would wipe the ambient key a
// default-profile agent authenticates with. The sk- case shows the refusal is the key rule, not the
// credential heuristic.
func TestPR724_T6_ManifestRefusesAnthropicAPIKeyAtAnyValue(t *testing.T) {
	for name, value := range map[string]string{
		"empty":      "",
		"literal":    "abc123",
		"file_ref":   "file:.agentfactory/secrets/anthropic.key",
		"sk_literal": "sk-ant-mallory",
	} {
		t.Run(name, func(t *testing.T) { p724cWantLaunchKeyRefusal(t, "ANTHROPIC_API_KEY", value) })
	}
}

// Command-scope git config outranks af's GIT_CONFIG_COUNT entries, so this key alone can point
// core.hooksPath away from af's trailer hook.
func TestPR724_T6_ManifestRefusesTrailerOverrideViaConfigParameters(t *testing.T) {
	p724cWantLaunchKeyRefusal(t, "GIT_CONFIG_PARAMETERS", "'core.hooksPath'='/dev/null'")
}

// The new category is appended after every existing one, so no message an existing manifest produces is
// reordered or reworded.
func TestPR724_T6_AFLaunchKeyClauseComesLast(t *testing.T) {
	dir := imPluginDir(t, "acme", imHeader+imService+imEnv(
		`GIT_CONFIG_PARAMETERS = "x"`, `PATH = "/opt/bin"`, `GIT_AUTHOR_NAME = "x"`,
		`ANTHROPIC_BASE_URL = "http://127.0.0.1:4000"`, `AF_ROLE = "manager"`))
	_, err := LoadIntegrationManifest(dir)
	want := "af-integration.toml: [env] may not set AF_ROLE (identity key); may not set PATH (toolchain key); " +
		"ANTHROPIC_BASE_URL belongs to models.json profiles; " +
		"may not set GIT_AUTHOR_NAME (af launch key); may not set GIT_CONFIG_PARAMETERS (af launch key)"
	if err == nil || err.Error() != want {
		t.Errorf("err = %v\nwant  %s", err, want)
	}
}

func TestPR724_T6_KeepManifestAcceptsIntegrationOwnKeys(t *testing.T) {
	dir := imPluginDir(t, "acme", imHeader+imService+imEnv(`ACME_MODE = "strict"`, `ACME_TOKEN = "file:.agentfactory/secrets/acme.token"`))
	m, err := LoadIntegrationManifest(dir)
	if err != nil {
		t.Fatalf("an integration's own keys must keep loading: %v", err)
	}
	if m.Env["ACME_MODE"] != "strict" || m.Env["ACME_TOKEN"] != "file:.agentfactory/secrets/acme.token" {
		t.Errorf("[env] = %v, want both ACME keys kept", m.Env)
	}
}

// The denial is manifest-only: a models.json profile may still name these keys (session carves them out of
// the profile-key universe for exactly that case) and may still clear ANTHROPIC_API_KEY with "".
func TestPR724_T6_KeepProfileMayNameAFLaunchKeys(t *testing.T) {
	for _, key := range append([]string{"GIT_CONFIG_PARAMETERS"}, p724cLaunchFamilyKeys...) {
		val := "1"
		if key == EnvEffortLevel {
			val = "high"
		}
		if err := validateModelProfile("p", map[string]string{envModel: "claude-opus-4-8", key: val}); err != nil {
			t.Errorf("a profile naming %s was rejected: %v", key, err)
		}
	}
	if err := validateModelProfile("p", map[string]string{envModel: "claude-opus-4-8", envAPIKey: ""}); err != nil {
		t.Errorf(`a profile clearing %s with "" was rejected: %v`, envAPIKey, err)
	}
}

// The family is the keys af's own launch exports could be overridden through; the wider git channel and the
// design's GIT_* prefix list stay out of this change (D6.2, D6.3), so these still load.
func TestPR724_T6_KeepManifestAcceptsGitKeysOutsideTheLaunchFamily(t *testing.T) {
	for _, key := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_SSH_COMMAND"} {
		dir := imPluginDir(t, "acme", imHeader+imService+imEnv(key+` = "x"`))
		if _, err := LoadIntegrationManifest(dir); err != nil {
			t.Errorf("[env] %s was refused: %v", key, err)
		}
	}
}
