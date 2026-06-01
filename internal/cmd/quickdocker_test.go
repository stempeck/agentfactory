package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestQuickdockerIOSSupport(t *testing.T) {
	root := findModuleRoot(t)
	scriptPath := filepath.Join(root, "quickdocker.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading quickdocker.sh: %v", err)
	}
	content := string(data)

	t.Run("platform_flag_recognized", func(t *testing.T) {
		if !strings.Contains(content, "platform") {
			t.Error("quickdocker.sh must contain 'platform' for --platform flag parsing")
		}
	})

	t.Run("build_host_flag_exists", func(t *testing.T) {
		if !strings.Contains(content, "build-host") {
			t.Error("quickdocker.sh must contain 'build-host' for --build-host flag parsing")
		}
	})

	t.Run("ssh_agent_sock_volume_mount", func(t *testing.T) {
		if !strings.Contains(content, "ssh-agent.sock") {
			t.Error("quickdocker.sh must contain 'ssh-agent.sock' for SSH agent socket volume mount")
		}
	})

	t.Run("macos_ssh_socket_path", func(t *testing.T) {
		if !strings.Contains(content, "run/host-services/ssh-auth.sock") {
			t.Error("quickdocker.sh must contain macOS Docker Desktop SSH socket path")
		}
	})

	t.Run("af_config_build_host_called", func(t *testing.T) {
		if !strings.Contains(content, "af config build-host --mode ssh") {
			t.Error("quickdocker.sh must call 'af config build-host --mode ssh' post-quickstart")
		}
	})

	t.Run("ssh_connectivity_verification", func(t *testing.T) {
		if !strings.Contains(content, "BatchMode=yes") {
			t.Error("quickdocker.sh must contain 'BatchMode=yes' for SSH connectivity verification")
		}
	})

	t.Run("non_ios_path_unaffected", func(t *testing.T) {
		re := regexp.MustCompile(`PLATFORM.*ios`)
		matches := re.FindAllString(content, -1)
		if len(matches) < 4 {
			t.Errorf("quickdocker.sh must have >= 4 PLATFORM ios conditionals, found %d", len(matches))
		}
	})

	t.Run("ssh_auth_sock_preflight", func(t *testing.T) {
		if !strings.Contains(content, "SSH_AUTH_SOCK") {
			t.Error("quickdocker.sh must check SSH_AUTH_SOCK for pre-flight validation")
		}
	})

	t.Run("bashrc_runtime_guard", func(t *testing.T) {
		if !strings.Contains(content, "SSH agent socket") {
			t.Error("quickdocker.sh must contain SSH agent socket runtime guard in bashrc")
		}
	})

	t.Run("skip_ssh_check_flag", func(t *testing.T) {
		if !strings.Contains(content, "skip-ssh-check") {
			t.Error("quickdocker.sh must use --skip-ssh-check in af config build-host call")
		}
	})

	t.Run("mount_path_flag", func(t *testing.T) {
		if !strings.Contains(content, "mount-path") {
			t.Error("quickdocker.sh must pass --mount-path to af config build-host")
		}
	})

	t.Run("error_handling_build_host", func(t *testing.T) {
		hasErrorHandling := strings.Contains(content, "af config build-host") &&
			(strings.Contains(content, "|| {") || strings.Contains(content, "|| exit"))
		if !hasErrorHandling {
			t.Error("quickdocker.sh must have error handling for af config build-host failure")
		}
	})

	t.Run("help_text_updated", func(t *testing.T) {
		if !strings.Contains(content, "--platform") {
			t.Error("quickdocker.sh help text must document --platform flag")
		}
	})

	t.Run("shell_syntax_valid", func(t *testing.T) {
		cmd := exec.Command("bash", "-n", scriptPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("quickdocker.sh has syntax errors: %s\n%s", err, output)
		}
	})

	t.Run("help_shows_platform_flag", func(t *testing.T) {
		cmd := exec.Command("bash", scriptPath, "--help")
		output, err := cmd.CombinedOutput()
		if err != nil && cmd.ProcessState.ExitCode() != 0 && cmd.ProcessState.ExitCode() != 1 {
			t.Logf("help command output: %s", output)
		}
		if !strings.Contains(string(output), "--platform") {
			t.Error("quickdocker.sh --help output must contain '--platform'")
		}
	})
}
