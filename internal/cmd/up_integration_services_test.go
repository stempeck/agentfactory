package cmd

// Phase 2 (design 695 K10) pins for af up's wiring of the integration service ensure and
// the user-scope detector. These tests swap package seams, so none may call t.Parallel.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
)

// integrationUpRoot is a minimal factory (one agent, alpha) chdir'd into, with hermetic
// tmux/session seams, an empty CLAUDE_CONFIG_DIR fixture and operator context.
func integrationUpRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1,"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude-config-empty"))
	t.Chdir(root)
	setupHermeticSessions(t)
	return root
}

// stubIntegrationServicesEnsure replaces ensureIntegrationServicesFn with a recorder that
// appends "integrations" to order (when non-nil) and returns reports.
func stubIntegrationServicesEnsure(t *testing.T, order *[]string, reports []string) (*int, *serviceScope) {
	t.Helper()
	calls := 0
	var gotScope serviceScope
	orig := ensureIntegrationServicesFn
	ensureIntegrationServicesFn = func(ctx context.Context, cmd *cobra.Command, root string, scope serviceScope) []string {
		calls++
		gotScope = scope
		if order != nil {
			*order = append(*order, "integrations")
		}
		return reports
	}
	t.Cleanup(func() { ensureIntegrationServicesFn = orig })
	return &calls, &gotScope
}

// stubUserScopeDetector replaces detectUnaccountedUserScopeFn with a recorder.
func stubUserScopeDetector(t *testing.T, order *[]string, rows []string) *int {
	t.Helper()
	calls := 0
	orig := detectUnaccountedUserScopeFn
	detectUnaccountedUserScopeFn = func(root string) []string {
		calls++
		if order != nil {
			*order = append(*order, "detector")
		}
		return rows
	}
	t.Cleanup(func() { detectUnaccountedUserScopeFn = orig })
	return &calls
}

// TestRunUpEnsuresFactoryServicesOnEveryPath: Phase 3 C21 has the factory-scope service ensure run "once per
// af up", so `af up <names>` reaches it too (D63). Gates, dispatch and the user-scope detector stay
// blanket-only (C-4).
func TestRunUpEnsuresFactoryServicesOnEveryPath(t *testing.T) {
	integrationUpRoot(t)
	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)
	ensureCalls, scope := stubIntegrationServicesEnsure(t, nil, nil)
	detectCalls := stubUserScopeDetector(t, nil, nil)

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	_ = runUp(cmd, []string{"alpha"})

	if *ensureCalls != 1 {
		t.Errorf("af up <names> must call ensureIntegrationServicesFn exactly once; called %d", *ensureCalls)
	} else if *scope != serviceScopeFactory {
		t.Errorf("af up <names> ensures factory-scope services; got scope %v", *scope)
	}
	if *detectCalls != 0 {
		t.Errorf("af up <names> must not reach the user-scope detector; called %d", *detectCalls)
	}

	cmd2 := &cobra.Command{}
	cmd2.SetOut(&out)
	cmd2.SetErr(&errBuf)
	_ = runUp(cmd2, nil)

	if *ensureCalls != 2 {
		t.Errorf("bare af up must call ensureIntegrationServicesFn exactly once more; total calls %d, want 2", *ensureCalls)
	} else if *scope != serviceScopeFactory {
		t.Errorf("bare af up ensures factory-scope services (spec L791); got scope %v", *scope)
	}
	if *detectCalls != 1 {
		t.Errorf("bare af up must call the user-scope detector exactly once; called %d", *detectCalls)
	}
}

// With no integration installed the ensure has nothing to start or report, so moving it onto the positional
// path leaves `af up <names>` output unchanged in such a factory.
func TestRunUpNamedWithoutIntegrationsPrintsNoEnsureLine(t *testing.T) {
	root := integrationUpRoot(t)
	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)
	if _, err := os.Stat(config.PluginsConfigPath(root)); !os.IsNotExist(err) {
		t.Fatalf("fixture: plugins.json must be absent (stat err %v)", err)
	}

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	_ = runUp(cmd, []string{"alpha"})

	printed := strings.ReplaceAll(out.String()+errBuf.String(), root, "<root>")
	if strings.Contains(strings.ToLower(printed), "integration") {
		t.Errorf("af up <names> without integrations must print nothing about them:\nstdout=%s\nstderr=%s", out.String(), errBuf.String())
	}
}
