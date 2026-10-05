package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func p724bBindFixture(t *testing.T) (root string, in *PluginIntegration, snap string) {
	t.Helper()
	root = t.TempDir()
	e := rbRecord(t, root, rbSpec{"bind-int", "formula", []string{"zeta-plugin", "alpha-plugin"}, map[string]string{"BIND_INT_Z": "z", "BIND_INT_A": "a"}})
	return root, e.Integration, filepath.Join(root, filepath.FromSlash(e.Integration.SnapshotDir))
}

func p724bBindError(t *testing.T, err error) *IntegrationBindError {
	t.Helper()
	var be *IntegrationBindError
	if !errors.As(err, &be) {
		t.Fatalf("want *IntegrationBindError, got %T %v", err, err)
	}
	if be.Name != "bind-int" {
		t.Errorf("IntegrationBindError.Name = %q, want bind-int", be.Name)
	}
	return be
}

// BindIntegration's results and refusal messages are what admission classifies and what reports print, so
// sharing its verification body with the exec sinks must leave every one of them unchanged.
func TestPR724_T5_KeepBindIntegrationResultsAndMessages(t *testing.T) {
	t.Run("intact_binding", func(t *testing.T) {
		root, in, snap := p724bBindFixture(t)
		b, err := BindIntegration(root, "bind-int", in)
		if err != nil {
			t.Fatalf("intact snapshot refused: %v", err)
		}
		want := IntegrationBinding{
			Name: "bind-int", Scope: "formula", SnapshotDir: in.SnapshotDir, ContentSHA256: in.ContentSHA256,
			HookFailMode: IntegrationHookFailOpen,
			PluginDirs:   []string{filepath.Join(snap, "zeta-plugin"), filepath.Join(snap, "alpha-plugin")},
			Env:          []EnvVar{{"BIND_INT_A", "a"}, {"BIND_INT_Z", "z"}},
		}
		if b.Name != want.Name || b.Scope != want.Scope || b.SnapshotDir != want.SnapshotDir || b.ContentSHA256 != want.ContentSHA256 ||
			b.HookFailMode != want.HookFailMode || b.Required || !slices.Equal(b.PluginDirs, want.PluginDirs) ||
			!slices.Equal(b.Env, want.Env) || !slices.Equal(b.ClaudePlugins, in.ClaudePlugins) {
			t.Errorf("binding = %+v\nwant      %+v (ClaudePlugins %v)", b, want, in.ClaudePlugins)
		}
	})

	t.Run("drifted_names_changed_files", func(t *testing.T) {
		root, in, snap := p724bBindFixture(t)
		if err := os.WriteFile(filepath.Join(snap, "af", "serve.sh"), []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := BindIntegration(root, "bind-int", in)
		be := p724bBindError(t, err)
		if !be.Drifted || be.Changed != 1 {
			t.Errorf("Drifted/Changed = %v/%d, want true/1", be.Drifted, be.Changed)
		}
		if want := `integration "bind-int" not bound: content changed since consent (1 files; run af plugin verify bind-int)`; err.Error() != want {
			t.Errorf("message = %q\nwant      %q", err.Error(), want)
		}
	})

	t.Run("drifted_without_per_file_hashes_counts_every_file", func(t *testing.T) {
		root, in, snap := p724bBindFixture(t)
		if err := os.WriteFile(filepath.Join(snap, "af", "serve.sh"), []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		m, err := LoadIntegrationManifestNamed(snap, "bind-int")
		if err != nil {
			t.Fatal(err)
		}
		_, files, err := IntegrationContentHash(snap, IntegrationDeclaredPaths(m))
		if err != nil {
			t.Fatal(err)
		}
		in.Files = nil
		_, err = BindIntegration(root, "bind-int", in)
		be := p724bBindError(t, err)
		if !be.Drifted || be.Changed != len(files) {
			t.Errorf("Drifted/Changed = %v/%d, want true/%d", be.Drifted, be.Changed, len(files))
		}
	})

	for _, tc := range []struct {
		name string
		dir  func(root, snap string) string
	}{
		{"non_content_addressed_snapshot_dir", func(root, _ string) string {
			return filepath.ToSlash(filepath.Join(".agentfactory", "store", "plugins", "bind-int"))
		}},
		{"absolute_snapshot_dir", func(_, snap string) string { return snap }},
		{"empty_snapshot_dir", func(string, string) string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, in, snap := p724bBindFixture(t)
			in.SnapshotDir = tc.dir(root, snap)
			_, err := BindIntegration(root, "bind-int", in)
			be := p724bBindError(t, err)
			if be.Drifted {
				t.Error("an unaddressed snapshot_dir is not drift")
			}
			want := fmt.Sprintf("integration %q not bound: snapshot_dir %q is not its content-addressed snapshot under %s", "bind-int", in.SnapshotDir, IntegrationsDir(root))
			if err.Error() != want {
				t.Errorf("message = %q\nwant      %q", err.Error(), want)
			}
		})
	}

	t.Run("missing_snapshot", func(t *testing.T) {
		root, in, snap := p724bBindFixture(t)
		if err := os.RemoveAll(snap); err != nil {
			t.Fatal(err)
		}
		_, err := BindIntegration(root, "bind-int", in)
		be := p724bBindError(t, err)
		if be.Drifted {
			t.Error("a missing snapshot is not drift")
		}
		msg := err.Error()
		if !strings.HasPrefix(msg, `integration "bind-int" not bound: snapshot missing or unreadable (`) || !strings.HasSuffix(msg, "; run af plugin verify bind-int)") {
			t.Errorf("message = %q, want the snapshot-missing refusal naming af plugin verify bind-int", msg)
		}
	})
}
