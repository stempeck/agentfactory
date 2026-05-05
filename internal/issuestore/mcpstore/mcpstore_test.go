//go:build integration

package mcpstore_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// TestMCPStoreContract runs the cross-adapter behavioral contract against
// the MCP-backed adapter.
//
// Each factory(actor) call provisions its own factoryRoot tempdir, symlinks
// the repo's py/ package into it so the Python subprocess can import
// py.issuestore.server, and spawns a dedicated server. Servers are
// SIGTERM'd at test end by reading their endpoint file's PID.
//
// Skips if python3 is not on PATH.
func TestMCPStoreContract(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	// Phase 4's server imports aiohttp and sqlalchemy. If the active
	// python3 cannot import them, there's no point spawning — skip and
	// let CI (which installs py/requirements.txt) run the real check.
	if out, err := exec.Command("python3", "-c", "import aiohttp, sqlalchemy").CombinedOutput(); err != nil {
		t.Skipf("python3 missing server deps (aiohttp/sqlalchemy): %s", out)
	}

	repoRoot := findRepoRoot(t)
	var spawnedFactoryRoots []string

	factory := func(actor string) issuestore.Store {
		factoryRoot := t.TempDir()
		if err := os.Symlink(
			filepath.Join(repoRoot, "py"),
			filepath.Join(factoryRoot, "py"),
		); err != nil {
			t.Fatalf("symlink py/ into %s: %v", factoryRoot, err)
		}
		spawnedFactoryRoots = append(spawnedFactoryRoots, factoryRoot)

		store, err := mcpstore.New(factoryRoot, actor)
		if err != nil {
			t.Fatalf("mcpstore.New(%s, %q): %v", factoryRoot, actor, err)
		}
		return store
	}

	setStatus := func(s issuestore.Store, id string, status issuestore.Status) error {
		return s.(*mcpstore.MCPStore).SetStatus(context.Background(), id, status)
	}

	t.Cleanup(func() {
		for _, root := range spawnedFactoryRoots {
			terminateServer(root)
		}
	})

	issuestore.RunStoreContract(t, factory, setStatus)
}

// findRepoRoot walks up from the current working directory to the directory
// containing go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("findRepoRoot: no go.mod above %s", dir)
		}
		dir = parent
	}
}

// terminateServer reads factoryRoot/.runtime/mcp_server.json and sends
// SIGTERM to the recorded PID. Swallows all errors — best-effort cleanup.
func terminateServer(factoryRoot string) {
	epFile := filepath.Join(factoryRoot, ".runtime", "mcp_server.json")
	data, err := os.ReadFile(epFile)
	if err != nil {
		return
	}
	var info struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(data, &info); err != nil || info.PID <= 0 {
		return
	}
	_ = syscall.Kill(info.PID, syscall.SIGTERM)
}
