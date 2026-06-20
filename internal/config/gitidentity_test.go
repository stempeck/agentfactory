package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultGitIdentityConstants pins the exact C-3 literals (issue #371 AC-3).
// A typo in the noreply email silently breaks GitHub co-author linkage, so this
// equality test is mandatory, not optional (Gap 6 / data.md risk).
func TestDefaultGitIdentityConstants(t *testing.T) {
	if DefaultGitUserName != "agentfactory-cli" {
		t.Errorf("DefaultGitUserName = %q, want %q", DefaultGitUserName, "agentfactory-cli")
	}
	if DefaultGitUserEmail != "293373236+agentfactory-cli@users.noreply.github.com" {
		t.Errorf("DefaultGitUserEmail = %q, want %q", DefaultGitUserEmail, "293373236+agentfactory-cli@users.noreply.github.com")
	}
}

// TestLoadFactoryConfig_GitIdentityMissing_FillsDefaults verifies a factory.json
// with no git_identity loads and resolves to the exact C-3 literals (AC-3 (ii)).
// Mirrors the max_worktrees-missing default-fill precedent.
func TestLoadFactoryConfig_GitIdentityMissing_FillsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	data := `{"type":"factory","version":1,"name":"test"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadFactoryConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitIdentity == nil {
		t.Fatal("GitIdentity is nil, want default-filled")
	}
	if cfg.GitIdentity.Name != DefaultGitUserName {
		t.Errorf("GitIdentity.Name = %q, want %q", cfg.GitIdentity.Name, DefaultGitUserName)
	}
	if cfg.GitIdentity.Email != DefaultGitUserEmail {
		t.Errorf("GitIdentity.Email = %q, want %q", cfg.GitIdentity.Email, DefaultGitUserEmail)
	}
}

// TestLoadFactoryConfig_GitIdentityPresent_RoundTrips verifies an operator-set
// git_identity round-trips unchanged (C-4: do not override a present identity).
func TestLoadFactoryConfig_GitIdentityPresent_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	data := `{"type":"factory","version":1,"name":"test","git_identity":{"name":"custom","email":"custom@example.com"}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadFactoryConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitIdentity == nil {
		t.Fatal("GitIdentity is nil, want operator value preserved")
	}
	if cfg.GitIdentity.Name != "custom" || cfg.GitIdentity.Email != "custom@example.com" {
		t.Errorf("GitIdentity = %+v, want {custom custom@example.com}", *cfg.GitIdentity)
	}
}

// TestResolveIdentity covers the IFF presence-gate (AC-2 / C-4 / D-GATE): apply
// the default identity ONLY when name OR email is absent; never override a
// present ambient identity.
func TestResolveIdentity(t *testing.T) {
	def := &GitIdentity{Name: DefaultGitUserName, Email: DefaultGitUserEmail}
	tests := []struct {
		name         string
		ambientName  string
		ambientEmail string
		wantApply    bool
	}{
		{"both present -> do not override", "alice", "alice@example.com", false},
		{"name absent -> apply default", "", "alice@example.com", true},
		{"email absent -> apply default", "alice", "", true},
		{"both absent -> apply default", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotEmail, gotApply := ResolveIdentity(def, tt.ambientName, tt.ambientEmail)
			if gotApply != tt.wantApply {
				t.Errorf("apply = %v, want %v", gotApply, tt.wantApply)
			}
			if tt.wantApply {
				if gotName != DefaultGitUserName || gotEmail != DefaultGitUserEmail {
					t.Errorf("got (%q,%q), want defaults (%q,%q)", gotName, gotEmail, DefaultGitUserName, DefaultGitUserEmail)
				}
			}
		})
	}
}

// TestResolveIdentity_NilDefault guards against a nil default (only reachable on
// a hand-edited factory.json): no defaults to apply -> apply must be false.
func TestResolveIdentity_NilDefault(t *testing.T) {
	if _, _, apply := ResolveIdentity(nil, "", ""); apply {
		t.Error("apply = true with nil default, want false (nothing to apply)")
	}
}

// TestGitHooksDir verifies the af-managed git hooks dir is distinct from HooksDir
// (Phase 3 G-C: collision risk with .agentfactory/hooks).
func TestGitHooksDir(t *testing.T) {
	got := GitHooksDir("/root")
	want := filepath.Join("/root", ".agentfactory", "githooks")
	if got != want {
		t.Errorf("GitHooksDir = %q, want %q", got, want)
	}
	if got == HooksDir("/root") {
		t.Error("GitHooksDir must be distinct from HooksDir")
	}
}

// TestDefaultFactoryConfigJSON_ContainsGitIdentity verifies the installer's
// fresh-install factory.json (a single source built from the constants) carries
// the exact git_identity block (AC-3 (i)/(iii)/(iv); Gap 6 no-drift).
func TestDefaultFactoryConfigJSON_ContainsGitIdentity(t *testing.T) {
	jsonStr := DefaultFactoryConfigJSON()

	// raw bytes must contain the exact C-3 email literal
	if !strings.Contains(jsonStr, DefaultGitUserEmail) {
		t.Errorf("factory.json default does not contain the C-3 email literal:\n%s", jsonStr)
	}

	// and it must parse back to a config whose GitIdentity equals the constants
	var cfg FactoryConfig
	if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
		t.Fatalf("default factory.json does not parse: %v", err)
	}
	if cfg.GitIdentity == nil {
		t.Fatal("default factory.json has no git_identity")
	}
	if cfg.GitIdentity.Name != DefaultGitUserName || cfg.GitIdentity.Email != DefaultGitUserEmail {
		t.Errorf("git_identity = %+v, want defaults", *cfg.GitIdentity)
	}
}
