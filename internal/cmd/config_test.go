package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

func setupTestFactoryForConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	return dir
}

func resetConfigFlags() {
	flagMode = ""
	flagHost = ""
	flagUser = ""
	flagKey = ""
	flagMountPath = ""
	flagStatus = false
	flagRemove = false
	flagSkipSSHCheck = false
}

func TestConfigBuildHost_StatusNoConfig(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	flagStatus = true

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "No build-host configuration found") {
		t.Errorf("expected 'No build-host configuration found', got: %q", buf.String())
	}
}

func TestConfigBuildHost_StatusWithConfig(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	root, err := config.FindFactoryRoot(dir)
	if err != nil {
		t.Fatalf("FindFactoryRoot: %v", err)
	}
	bhPath := config.BuildHostConfigPath(root)
	cfg := &config.BuildHostConfig{
		Mode:    "ssh",
		Host:    "mac-mini.local",
		User:    "builder",
		KeyPath: "/tmp/testkey",
	}
	if err := config.SaveBuildHostConfig(bhPath, cfg); err != nil {
		t.Fatalf("SaveBuildHostConfig: %v", err)
	}

	flagStatus = true

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err = runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ssh") {
		t.Errorf("expected output to contain 'ssh', got: %q", out)
	}
	if !strings.Contains(out, "mac-mini.local") {
		t.Errorf("expected output to contain 'mac-mini.local', got: %q", out)
	}
	if !strings.Contains(out, "builder") {
		t.Errorf("expected output to contain 'builder', got: %q", out)
	}
}

func TestConfigBuildHost_RemoveNoConfig(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	flagRemove = true

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "No build-host configuration to remove") {
		t.Errorf("expected 'No build-host configuration to remove', got: %q", buf.String())
	}
}

func TestConfigBuildHost_RemoveExisting(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	root, err := config.FindFactoryRoot(dir)
	if err != nil {
		t.Fatalf("FindFactoryRoot: %v", err)
	}
	bhPath := config.BuildHostConfigPath(root)
	cfg := &config.BuildHostConfig{Mode: "local"}
	if err := config.SaveBuildHostConfig(bhPath, cfg); err != nil {
		t.Fatalf("SaveBuildHostConfig: %v", err)
	}

	flagRemove = true

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err = runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(bhPath); !os.IsNotExist(err) {
		t.Error("expected build-host.json to be deleted")
	}
	if !strings.Contains(buf.String(), "removed") {
		t.Errorf("expected removal confirmation, got: %q", buf.String())
	}
}

func TestConfigBuildHost_SetLocalMode(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	origLocal := localCheckFunc
	localCheckFunc = func() error { return nil }
	t.Cleanup(func() { localCheckFunc = origLocal })

	flagMode = "local"

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	root, err := config.FindFactoryRoot(dir)
	if err != nil {
		t.Fatalf("FindFactoryRoot: %v", err)
	}
	cfg, err := config.LoadBuildHostConfig(config.BuildHostConfigPath(root))
	if err != nil {
		t.Fatalf("LoadBuildHostConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config to be written, got nil")
	}
	if cfg.Mode != "local" {
		t.Errorf("mode = %q, want %q", cfg.Mode, "local")
	}
}

func TestConfigBuildHost_SetSSHMode(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	origSSH := sshCheckFunc
	sshCheckFunc = func(host, user, keyPath string) error { return nil }
	t.Cleanup(func() { sshCheckFunc = origSSH })

	keyFile := filepath.Join(t.TempDir(), "testkey")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	flagMode = "ssh"
	flagHost = "mac-mini.local"
	flagUser = "builder"
	flagKey = keyFile
	flagMountPath = "/Volumes/workspace"

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	root, err := config.FindFactoryRoot(dir)
	if err != nil {
		t.Fatalf("FindFactoryRoot: %v", err)
	}
	cfg, err := config.LoadBuildHostConfig(config.BuildHostConfigPath(root))
	if err != nil {
		t.Fatalf("LoadBuildHostConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config to be written, got nil")
	}
	if cfg.Mode != "ssh" {
		t.Errorf("mode = %q, want %q", cfg.Mode, "ssh")
	}
	if cfg.Host != "mac-mini.local" {
		t.Errorf("host = %q, want %q", cfg.Host, "mac-mini.local")
	}
	if cfg.User != "builder" {
		t.Errorf("user = %q, want %q", cfg.User, "builder")
	}
	if cfg.KeyPath != keyFile {
		t.Errorf("key_path = %q, want %q", cfg.KeyPath, keyFile)
	}
	if cfg.MountPath != "/Volumes/workspace" {
		t.Errorf("mount_path = %q, want %q", cfg.MountPath, "/Volumes/workspace")
	}
}

func TestConfigBuildHost_SSHMissingHost(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	flagMode = "ssh"
	flagUser = "builder"
	flagKey = "/tmp/key"

	cmd := &cobra.Command{}
	err := runConfigBuildHost(cmd, nil)
	if err == nil {
		t.Fatal("expected error for missing --host")
	}
	if !strings.Contains(err.Error(), "--host") {
		t.Errorf("error should mention --host, got: %v", err)
	}
}

func TestConfigBuildHost_SSHMissingUser(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	flagMode = "ssh"
	flagHost = "mac-mini.local"
	flagKey = "/tmp/key"

	cmd := &cobra.Command{}
	err := runConfigBuildHost(cmd, nil)
	if err == nil {
		t.Fatal("expected error for missing --user")
	}
	if !strings.Contains(err.Error(), "--user") {
		t.Errorf("error should mention --user, got: %v", err)
	}
}

func TestConfigBuildHost_SSHMissingKey(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	flagMode = "ssh"
	flagHost = "mac-mini.local"
	flagUser = "builder"

	cmd := &cobra.Command{}
	err := runConfigBuildHost(cmd, nil)
	if err == nil {
		t.Fatal("expected error for missing --key")
	}
	if !strings.Contains(err.Error(), "--key") {
		t.Errorf("error should mention --key, got: %v", err)
	}
}

func TestConfigBuildHost_SSHKeyNotFound(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	flagMode = "ssh"
	flagHost = "mac-mini.local"
	flagUser = "builder"
	flagKey = "/nonexistent/key"

	cmd := &cobra.Command{}
	err := runConfigBuildHost(cmd, nil)
	if err == nil {
		t.Fatal("expected error for non-existent key file")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestConfigBuildHost_SSHKeyPermissionWarning(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	origSSH := sshCheckFunc
	sshCheckFunc = func(host, user, keyPath string) error { return nil }
	t.Cleanup(func() { sshCheckFunc = origSSH })

	keyFile := filepath.Join(t.TempDir(), "testkey")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}

	flagMode = "ssh"
	flagHost = "mac-mini.local"
	flagUser = "builder"
	flagKey = keyFile

	cmd := &cobra.Command{}
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)

	err := runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(errBuf.String(), "permissions") {
		t.Errorf("expected permission warning on stderr, got: %q", errBuf.String())
	}

	root, err := config.FindFactoryRoot(dir)
	if err != nil {
		t.Fatalf("FindFactoryRoot: %v", err)
	}
	cfg, err := config.LoadBuildHostConfig(config.BuildHostConfigPath(root))
	if err != nil {
		t.Fatalf("LoadBuildHostConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config to be written despite permission warning")
	}
}

func TestConfigBuildHost_SkipSSHCheck(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	sshCalled := false
	origSSH := sshCheckFunc
	sshCheckFunc = func(host, user, keyPath string) error {
		sshCalled = true
		return fmt.Errorf("connection refused")
	}
	t.Cleanup(func() { sshCheckFunc = origSSH })

	keyFile := filepath.Join(t.TempDir(), "testkey")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	flagMode = "ssh"
	flagHost = "mac-mini.local"
	flagUser = "builder"
	flagKey = keyFile
	flagSkipSSHCheck = true

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sshCalled {
		t.Error("SSH check should not have been called with --skip-ssh-check")
	}

	root, err := config.FindFactoryRoot(dir)
	if err != nil {
		t.Fatalf("FindFactoryRoot: %v", err)
	}
	cfg, err := config.LoadBuildHostConfig(config.BuildHostConfigPath(root))
	if err != nil {
		t.Fatalf("LoadBuildHostConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config to be written")
	}
}

func TestConfigBuildHost_SSHCheckFails(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	origSSH := sshCheckFunc
	sshCheckFunc = func(host, user, keyPath string) error {
		return fmt.Errorf("connection refused")
	}
	t.Cleanup(func() { sshCheckFunc = origSSH })

	keyFile := filepath.Join(t.TempDir(), "testkey")
	if err := os.WriteFile(keyFile, []byte("fake-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	flagMode = "ssh"
	flagHost = "mac-mini.local"
	flagUser = "builder"
	flagKey = keyFile

	cmd := &cobra.Command{}
	err := runConfigBuildHost(cmd, nil)
	if err == nil {
		t.Fatal("expected error when SSH check fails")
	}
	if !strings.Contains(err.Error(), "SSH connectivity check failed") {
		t.Errorf("error should mention SSH check failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--skip-ssh-check") {
		t.Errorf("error should suggest --skip-ssh-check, got: %v", err)
	}
}

func TestConfigBuildHost_LocalXcodeWarning(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	origLocal := localCheckFunc
	localCheckFunc = func() error { return fmt.Errorf("not found") }
	t.Cleanup(func() { localCheckFunc = origLocal })

	flagMode = "local"

	cmd := &cobra.Command{}
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)

	err := runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(errBuf.String(), "xcodebuild") {
		t.Errorf("expected xcodebuild warning on stderr, got: %q", errBuf.String())
	}

	root, err := config.FindFactoryRoot(dir)
	if err != nil {
		t.Fatalf("FindFactoryRoot: %v", err)
	}
	cfg, err := config.LoadBuildHostConfig(config.BuildHostConfigPath(root))
	if err != nil {
		t.Fatalf("LoadBuildHostConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected config to be written despite xcodebuild failure")
	}
	if cfg.Mode != "local" {
		t.Errorf("mode = %q, want %q", cfg.Mode, "local")
	}
}

func TestConfigBuildHost_InvalidMode(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	flagMode = "invalid"

	cmd := &cobra.Command{}
	err := runConfigBuildHost(cmd, nil)
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("error should mention 'invalid mode', got: %v", err)
	}
}

func TestConfigBuildHost_NoFlags(t *testing.T) {
	dir := setupTestFactoryForConfig(t)
	t.Chdir(dir)
	t.Cleanup(resetConfigFlags)

	cmd := configBuildHostCmd
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := runConfigBuildHost(cmd, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "build-host") {
		t.Errorf("expected help output containing 'build-host', got: %q", out)
	}
}
