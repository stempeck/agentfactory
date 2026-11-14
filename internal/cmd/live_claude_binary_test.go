package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// resolveLiveClaudeBinary is liveClaudeBinary's rule, untagged so the unit lane executes it (the CI
// binary is literally named claude, so the mismatch branch never fires there). LookPath finds only a
// file named claude, and quickstart stores each release under its version number, so a binary the
// caller names otherwise would be passed over for whatever claude comes next on PATH.
func resolveLiveClaudeBinary(named string, lookPath func(string) (string, error)) (path string, found bool, err error) {
	resolved, lookErr := lookPath("claude")
	if named == "" {
		if lookErr != nil {
			return "", false, nil
		}
		return resolved, true, nil
	}
	if lookErr != nil {
		return "", false, fmt.Errorf("%s is named, but no claude resolves on PATH (%v)", named, lookErr)
	}
	want, wantErr := filepath.EvalSymlinks(named)
	got, gotErr := filepath.EvalSymlinks(resolved)
	if wantErr != nil || gotErr != nil || want != got {
		return "", false, fmt.Errorf("%s is named, but claude resolves to %s, another file", named, resolved)
	}
	return resolved, true, nil
}

func TestResolveLiveClaudeBinary(t *testing.T) {
	binDir := func(t *testing.T) string {
		t.Helper()
		dir, err := tryExecCapableDir(t, "af-test-harnesswindow")
		if err != nil {
			t.Fatalf("no exec-capable directory: %v", err)
		}
		t.Setenv("PATH", dir)
		return dir
	}
	plant := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho 2.1.308\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("file named 2.1.308 is refused", func(t *testing.T) {
		dir := binDir(t)
		named := filepath.Join(dir, "2.1.308")
		plant(t, named)
		if path, found, err := resolveLiveClaudeBinary(named, exec.LookPath); err == nil {
			t.Errorf("a named binary no claude resolves to = (%q, %v, nil), want an error", path, found)
		}
	})

	t.Run("claude symlink to it passes", func(t *testing.T) {
		dir := binDir(t)
		target := filepath.Join(dir, "2.1.308")
		plant(t, target)
		link := filepath.Join(dir, "claude")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		path, found, err := resolveLiveClaudeBinary(link, exec.LookPath)
		if err != nil || !found || path != link {
			t.Errorf("resolveLiveClaudeBinary(%q) = (%q, %v, %v), want (%q, true, nil)", link, path, found, err, link)
		}
	})

	t.Run("unset variable falls back to LookPath", func(t *testing.T) {
		dir := binDir(t)
		if path, found, err := resolveLiveClaudeBinary("", exec.LookPath); err != nil || found {
			t.Errorf("no claude on PATH = (%q, %v, %v), want not found and no error", path, found, err)
		}
		claude := filepath.Join(dir, "claude")
		plant(t, claude)
		if path, found, err := resolveLiveClaudeBinary("", exec.LookPath); err != nil || !found || path != claude {
			t.Errorf("a claude on PATH = (%q, %v, %v), want (%q, true, nil)", path, found, err, claude)
		}
	})
}
