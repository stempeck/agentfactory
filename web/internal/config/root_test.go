package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The factory-root resolver tests are hermetic over t.TempDir() — they NEVER spawn a real `af`
// process (Phase 1 wires nothing). They assert behavioural PARITY with af-core's
// internal/config.FindFactoryRoot (root.go:10-40) and resolveWatchdogRoot (watchdog.go:232-243):
// the af-core package is unimportable (internal seal + separate web go.mod), so parity is asserted
// against the documented behaviour — plain factory, worktree .factory-root redirect, stale-redirect
// non-follow, old-layout migration hint, and the EXACT no-factory error string.

// evalTempDir returns a symlink-resolved t.TempDir(). FindFactoryRoot returns paths verbatim, so the
// fixture root must be canonicalised (e.g. macOS /var→/private/var) or equality assertions flake.
func evalTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// mkFactory materialises a valid factory at root: <root>/.agentfactory/factory.json. It reuses the
// package-private dotDir / factoryPath / mustWrite that settings.go and settings_test.go already
// provide (no redeclaration — same package).
func mkFactory(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, factoryPath(root), `{"type":"factory","version":1,"name":"demo"}`)
}

// mkRedirect writes a worktree redirect: <worktree>/.agentfactory/.factory-root containing target.
// A trailing newline is deliberately appended to prove the resolver applies strings.TrimSpace.
func mkRedirect(t *testing.T, worktree, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(worktree, dotDir), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(worktree, dotDir, ".factory-root"), target+"\n")
}

// mustMkdirAll is a tiny fatal-on-error MkdirAll for fixture subdirectories.
func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// noFactoryErr is the EXACT terminal error string af-core returns (internal/config/root.go:36). The
// near-miss decoy at paths.go:82 (FindLocalRoot) lacks the parenthetical; this constant guards parity.
const noFactoryErr = "not in an agentfactory workspace (no .agentfactory/factory.json found)"

func TestFindFactoryRoot(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T) (startDir, wantRoot string)
		wantErr   bool
		errEquals string // assert exact error string when set
		errSubstr string // assert error substring when set
	}{
		{
			name: "plain factory resolves from a nested cwd",
			setup: func(t *testing.T) (string, string) {
				root := evalTempDir(t)
				mkFactory(t, root)
				nested := filepath.Join(root, "a", "b", "c")
				mustMkdirAll(t, nested)
				return nested, root
			},
		},
		{
			name: "factory at startDir itself",
			setup: func(t *testing.T) (string, string) {
				root := evalTempDir(t)
				mkFactory(t, root)
				return root, root
			},
		},
		{
			name: "worktree .factory-root redirect to a separate factory root",
			setup: func(t *testing.T) (string, string) {
				base := evalTempDir(t)
				realRoot := filepath.Join(base, "real")
				mkFactory(t, realRoot)
				worktree := filepath.Join(base, "wt")
				mkRedirect(t, worktree, realRoot) // worktree holds no factory.json of its own
				return worktree, realRoot
			},
		},
		{
			name: "stale redirect is not followed; walk-up continues to the real factory above",
			setup: func(t *testing.T) (string, string) {
				root := evalTempDir(t)
				mkFactory(t, root) // the real factory lives at the top
				dead := filepath.Join(root, "dead")
				mustMkdirAll(t, dead) // a directory with NO factory.json
				worktree := filepath.Join(root, "sub", "wt")
				mkRedirect(t, worktree, dead) // redirect points at the non-factory dir
				return worktree, root
			},
		},
		{
			name: "no factory anywhere fails loud with the exact af-core error string",
			setup: func(t *testing.T) (string, string) {
				dir := evalTempDir(t)
				nested := filepath.Join(dir, "x", "y")
				mustMkdirAll(t, nested)
				return nested, ""
			},
			wantErr:   true,
			errEquals: noFactoryErr,
		},
		{
			name: "old-layout config/factory.json yields a migration hint",
			setup: func(t *testing.T) (string, string) {
				dir := evalTempDir(t)
				mustMkdirAll(t, filepath.Join(dir, "config"))
				mustWrite(t, filepath.Join(dir, "config", "factory.json"), `{"type":"factory"}`)
				return dir, ""
			},
			wantErr:   true,
			errSubstr: "old-layout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startDir, wantRoot := tt.setup(t)
			got, err := FindFactoryRoot(startDir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got root %q", got)
				}
				if tt.errEquals != "" && err.Error() != tt.errEquals {
					t.Fatalf("error = %q, want exactly %q", err.Error(), tt.errEquals)
				}
				if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != wantRoot {
				t.Fatalf("FindFactoryRoot = %q, want %q", got, wantRoot)
			}
		})
	}
}

// TestFindFactoryRoot_Parity asserts behavioural parity with af-core: the EXACT terminal error string
// and the redirect-target-wins positive case. (af-core cannot be imported; parity is against the
// documented behaviour — closes Gap 8 / R5.)
func TestFindFactoryRoot_Parity(t *testing.T) {
	// No-factory error string parity (af-core internal/config/root.go:36).
	dir := evalTempDir(t)
	if _, err := FindFactoryRoot(dir); err == nil {
		t.Fatalf("expected an error for a non-factory directory")
	} else if err.Error() != noFactoryErr {
		t.Fatalf("no-factory error parity:\n got %q\nwant %q", err.Error(), noFactoryErr)
	}

	// Redirect-target-wins parity: a worktree redirect to a separate valid factory resolves to that
	// target, exactly as af-core does.
	base := evalTempDir(t)
	realRoot := filepath.Join(base, "real")
	mkFactory(t, realRoot)
	worktree := filepath.Join(base, "wt")
	mkRedirect(t, worktree, realRoot)
	got, err := FindFactoryRoot(worktree)
	if err != nil {
		t.Fatalf("redirect parity: unexpected error %v", err)
	}
	if got != realRoot {
		t.Fatalf("redirect parity: FindFactoryRoot = %q, want %q", got, realRoot)
	}
}

// TestResolveFactoryRoot_AFRootFirst proves the AF_ROOT-first-then-cwd precedence mirroring
// resolveWatchdogRoot (watchdog.go:232-243): AF_ROOT wins when it FULLY resolves; otherwise the cwd
// is used. AF_ROOT validation is a full FindFactoryRoot (G3), not a shallow stat.
func TestResolveFactoryRoot_AFRootFirst(t *testing.T) {
	t.Run("AF_ROOT set to a valid factory is used regardless of cwd", func(t *testing.T) {
		afRoot := evalTempDir(t)
		mkFactory(t, afRoot)
		cwd := evalTempDir(t) // a directory with no factory
		t.Chdir(cwd)
		t.Setenv("AF_ROOT", afRoot)
		got, err := ResolveFactoryRoot()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != afRoot {
			t.Fatalf("ResolveFactoryRoot = %q, want %q (AF_ROOT)", got, afRoot)
		}
	})

	t.Run("AF_ROOT is validated by full resolution (worktree redirect followed)", func(t *testing.T) {
		base := evalTempDir(t)
		realRoot := filepath.Join(base, "real")
		mkFactory(t, realRoot)
		wt := filepath.Join(base, "wt")
		mkRedirect(t, wt, realRoot)
		t.Setenv("AF_ROOT", wt) // AF_ROOT itself is a worktree dir; full resolution follows the redirect
		got, err := ResolveFactoryRoot()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != realRoot {
			t.Fatalf("ResolveFactoryRoot = %q, want %q (redirect target)", got, realRoot)
		}
	})

	t.Run("AF_ROOT unset falls back to the working directory", func(t *testing.T) {
		root := evalTempDir(t)
		mkFactory(t, root)
		sub := filepath.Join(root, "sub")
		mustMkdirAll(t, sub)
		t.Chdir(sub)
		t.Setenv("AF_ROOT", "") // empty ⇒ treated as unset by the != "" guard
		got, err := ResolveFactoryRoot()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != root {
			t.Fatalf("ResolveFactoryRoot = %q, want %q (cwd walk-up)", got, root)
		}
	})

	t.Run("AF_ROOT pointing at a non-factory falls back to the working directory", func(t *testing.T) {
		root := evalTempDir(t)
		mkFactory(t, root)
		sub := filepath.Join(root, "sub")
		mustMkdirAll(t, sub)
		bogus := evalTempDir(t) // a directory with no factory
		t.Chdir(sub)
		t.Setenv("AF_ROOT", bogus)
		got, err := ResolveFactoryRoot()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != root {
			t.Fatalf("ResolveFactoryRoot = %q, want %q (cwd fallback)", got, root)
		}
	})

	t.Run("neither AF_ROOT nor cwd resolves a factory fails loud", func(t *testing.T) {
		cwd := evalTempDir(t)
		t.Chdir(cwd)
		t.Setenv("AF_ROOT", "")
		if _, err := ResolveFactoryRoot(); err == nil {
			t.Fatalf("expected an error when no factory resolves anywhere")
		} else if err.Error() != noFactoryErr {
			t.Fatalf("fail-loud parity:\n got %q\nwant %q", err.Error(), noFactoryErr)
		}
	})
}
