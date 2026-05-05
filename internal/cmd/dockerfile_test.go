package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDockerfileSupplyChainInvariants(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	content := string(data)
	lines := strings.Split(content, "\n")

	t.Run("base_image_pinned", func(t *testing.T) {
		if len(lines) == 0 {
			t.Fatal("Dockerfile is empty")
		}
		first := strings.TrimSpace(lines[0])
		if first != "FROM ubuntu:24.04" {
			t.Errorf("Dockerfile line 1: want FROM ubuntu:24.04, got %q", first)
		}
	})

	t.Run("go_sha256_env_vars_exist", func(t *testing.T) {
		amd64 := strings.Contains(content, "GO_SHA256_AMD64")
		arm64 := strings.Contains(content, "GO_SHA256_ARM64")
		if !amd64 {
			t.Error("missing GO_SHA256_AMD64 ENV var")
		}
		if !arm64 {
			t.Error("missing GO_SHA256_ARM64 ENV var")
		}
	})

	t.Run("go_sha256_verification", func(t *testing.T) {
		if !strings.Contains(content, "sha256sum --check") {
			t.Error("missing sha256sum --check in Go download block")
		}
	})

	t.Run("nodesource_gpg_keyring", func(t *testing.T) {
		if !strings.Contains(content, "gpg --dearmor -o /usr/share/keyrings/nodesource") {
			t.Error("missing gpg --dearmor keyring pattern for NodeSource")
		}
		if !strings.Contains(content, "signed-by=/usr/share/keyrings/nodesource") {
			t.Error("missing signed-by for NodeSource apt source")
		}
	})

	t.Run("no_pipe_to_bash_nodesource", func(t *testing.T) {
		re := regexp.MustCompile(`nodesource.*setup_lts`)
		if re.MatchString(content) {
			t.Error("NodeSource pipe-to-bash pattern still present (setup_lts)")
		}
	})

	t.Run("pip_require_hashes", func(t *testing.T) {
		if !strings.Contains(content, "--require-hashes") {
			t.Error("missing --require-hashes in pip install line")
		}
	})
}
