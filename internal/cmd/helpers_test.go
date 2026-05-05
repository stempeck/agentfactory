package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveAgentName_WrongButNoError_HonorsAF_ROLE pins GitHub issue #88.
//
// DetectAgentFromCwd returns parts[2] with no agents.json validation, so a cwd
// at .agentfactory/agents/<typo>/ returns ("typo", nil) — no error. The old
// resolveAgentName AND-gated AF_ROLE behind err != nil, silently ignoring
// AF_ROLE even when set correctly by session.Manager. The fix validates the
// path-derived name against agents.json and consults AF_ROLE on membership
// failure.
func TestResolveAgentName_WrongButNoError_HonorsAF_ROLE(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	// Create a typo directory on disk. "typo" is NOT in agents.json.
	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "solver")

	got, err := resolveAgentName(typoDir, factoryRoot)
	if err != nil {
		t.Fatalf("resolveAgentName: %v", err)
	}
	if got != "solver" {
		t.Errorf("resolveAgentName = %q, want %q (AF_ROLE must override wrong path-derived name)", got, "solver")
	}
}

// TestResolveAgentName_WrongButNoError_NoAF_ROLE_Errors is the negative
// companion. With AF_ROLE empty and the path-derived name failing membership,
// resolveAgentName must return an error rather than silently returning the
// wrong name. This protects detectCreatingAgent and detectAgentName — the two
// callers that currently accept whatever resolveAgentName returns.
func TestResolveAgentName_WrongButNoError_NoAF_ROLE_Errors(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "")

	got, err := resolveAgentName(typoDir, factoryRoot)
	if err == nil {
		t.Fatalf("resolveAgentName should error for unknown agent, got %q", got)
	}
	if got == "typo" {
		t.Errorf("resolveAgentName must not return wrong path-derived name %q silently", got)
	}
}

// TestResolveAgentName_HappyPath_NoAF_ROLE verifies the fix doesn't regress
// legitimate path resolution: a cwd under a real agent directory returns the
// agent name without consulting AF_ROLE.
func TestResolveAgentName_HappyPath_NoAF_ROLE(t *testing.T) {
	factoryRoot, agentDir := setupFactoryFixture(t, "solver")

	t.Setenv("AF_ROLE", "")

	got, err := resolveAgentName(agentDir, factoryRoot)
	if err != nil {
		t.Fatalf("resolveAgentName: %v", err)
	}
	if got != "solver" {
		t.Errorf("resolveAgentName = %q, want %q", got, "solver")
	}
}

// TestResolveAgentName_CorruptAgentsJSON_NoAF_ROLE_Errors pins a silent-skip
// bug in the membership gate at helpers.go:60-66. When LoadAgentConfig fails
// (missing, unreadable, or malformed agents.json), the `if cfgErr == nil`
// branch is skipped, err stays nil, and the wrong-but-no-error path-derived
// name is returned without error.
//
// This is the same silent-fallback-to-buggy-behavior pattern issue #88 is
// meant to eliminate — just at a different layer. If the function cannot
// validate the path-derived name, it must not trust it.
func TestResolveAgentName_CorruptAgentsJSON_NoAF_ROLE_Errors(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	if err := os.WriteFile(
		filepath.Join(factoryRoot, ".agentfactory", "agents.json"),
		[]byte("this is not json{{{"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "")

	got, err := resolveAgentName(typoDir, factoryRoot)
	if err == nil {
		t.Fatalf("resolveAgentName silently returned %q — membership gate was skipped because agents.json failed to load", got)
	}
	if got == "typo" {
		t.Errorf("resolveAgentName must not return wrong path-derived name %q when membership cannot be validated", got)
	}
}

// TestResolveAgentName_CorruptAgentsJSON_WithAF_ROLE_HonorsEnv is the companion
// case. With a corrupt agents.json AND AF_ROLE set to a legitimate name by
// session.Manager, the function should honor AF_ROLE — the whole point of
// AF_ROLE is to be the trusted fallback when path-derived identity cannot be
// validated. The silent-skip bug currently swallows AF_ROLE entirely by
// leaving err==nil and returning the (wrong) path-derived name.
func TestResolveAgentName_CorruptAgentsJSON_WithAF_ROLE_HonorsEnv(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	if err := os.WriteFile(
		filepath.Join(factoryRoot, ".agentfactory", "agents.json"),
		[]byte("this is not json{{{"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "solver")

	got, err := resolveAgentName(typoDir, factoryRoot)
	if err != nil {
		t.Fatalf("resolveAgentName: %v", err)
	}
	if got != "solver" {
		t.Errorf("resolveAgentName = %q, want %q — AF_ROLE must be honored when agents.json cannot be loaded (session.Manager's trusted value is the whole point of AF_ROLE)", got, "solver")
	}
}
