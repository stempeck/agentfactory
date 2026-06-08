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

	t.Run("shared_volume_mount", func(t *testing.T) {
		if !strings.Contains(content, "af-containers") {
			t.Error("quickdocker.sh must contain 'af-containers' for shared volume mount")
		}
	})

	t.Run("key_generation", func(t *testing.T) {
		if !strings.Contains(content, "af_container_ed25519") {
			t.Error("quickdocker.sh must contain 'af_container_ed25519' for SSH key generation")
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

	t.Run("key_copy_into_container", func(t *testing.T) {
		if !strings.Contains(content, "docker cp") || !strings.Contains(content, "id_ed25519") {
			t.Error("quickdocker.sh must copy SSH key into container via docker cp")
		}
	})

	t.Run("key_permissions", func(t *testing.T) {
		if !strings.Contains(content, "chmod 600") {
			t.Error("quickdocker.sh must set chmod 600 on SSH private key")
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

// TestQuickdockerIOSKeyAuthIsLocal guards issue #272: the iOS container's public
// key must be authorized by appending it to the LOCAL ~/.ssh/authorized_keys (no
// SSH to any remote), gated on macOS — matching the gastown reference. The
// operator-supplied build host (e.g. host.docker.internal) only resolves INSIDE a
// container, so authorizing it by SSHing from the Mac host can never succeed.
// These assertions are negative/structural — the presence-only suite above passed
// against the buggy remote-authorize script, which is why the bug shipped 5x.
func TestQuickdockerIOSKeyAuthIsLocal(t *testing.T) {
	root := findModuleRoot(t)
	scriptPath := filepath.Join(root, "quickdocker.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading quickdocker.sh: %v", err)
	}
	content := string(data)

	// T1 (PRIMARY, AC-1/AC-6): host-side authorization must not SSH to the build host.
	t.Run("ios_key_auth_does_not_ssh_to_build_host", func(t *testing.T) {
		if strings.Contains(content, "ssh-copy-id") {
			t.Error("iOS key auth must NOT use ssh-copy-id; gastown appends the pubkey to the LOCAL authorized_keys with no SSH")
		}
		pipeToSSH := regexp.MustCompile(`cat[^\n]*\.pub[^\n]*\|[^\n]*ssh`)
		if pipeToSSH.MatchString(content) {
			t.Error("iOS key auth must NOT pipe the pubkey over ssh to the build host (host.docker.internal won't resolve on the Mac)")
		}
	})

	// T2 (POSITIVE, AC-1/AC-6): pubkey is appended to the LOCAL authorized_keys, idempotently.
	t.Run("ios_key_authorized_locally", func(t *testing.T) {
		if !strings.Contains(content, "authorized_keys") {
			t.Error("iOS path must reference ~/.ssh/authorized_keys")
		}
		localAppend := regexp.MustCompile(`cat[^\n]*\.pub[^\n]*>>[^\n]*authorized_keys`)
		if !localAppend.MatchString(content) {
			t.Error("iOS path must 'cat <pubkey>.pub >> ~/.ssh/authorized_keys' locally (gastown pattern)")
		}
		if !strings.Contains(content, "grep -qF") && !strings.Contains(content, "grep -q") {
			t.Error("iOS key authorization must be idempotent (grep guard before append)")
		}
	})

	// T4 (AC-5/AC-6): host-side iOS setup is gated on macOS ($OSTYPE == darwin*) per gastown.
	t.Run("ios_host_side_setup_gated_on_macos", func(t *testing.T) {
		if !strings.Contains(content, "OSTYPE") && !strings.Contains(content, "darwin") {
			t.Error("iOS host-side key authorization should be gated on macOS ($OSTYPE == darwin*) per gastown")
		}
	})
}
