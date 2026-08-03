package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func extractShellFunction(content, funcName string) string {
	marker := funcName + "() {"
	start := strings.Index(content, marker)
	if start == -1 {
		return ""
	}
	depth := 0
	inBody := false
	for i := start; i < len(content); i++ {
		if content[i] == '{' {
			depth++
			inBody = true
		} else if content[i] == '}' {
			depth--
			if inBody && depth == 0 {
				return content[start : i+1]
			}
		}
	}
	return content[start:]
}

func TestQuickstartSupplyChainInvariants(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	installClaude := extractShellFunction(content, "install_claude")
	if installClaude == "" {
		t.Fatal("could not extract install_claude() function body")
	}

	configureShell := extractShellFunction(content, "configure_shell")
	if configureShell == "" {
		t.Fatal("could not extract configure_shell() function body")
	}

	t.Run("uses_official_installer", func(t *testing.T) {
		re := regexp.MustCompile(`curl.*https://claude\.ai/install\.sh.*\|.*bash`)
		if !re.MatchString(installClaude) {
			t.Error("install_claude() must use official curl|bash installer as primary method")
		}
	})

	t.Run("has_npm_fallback", func(t *testing.T) {
		if !strings.Contains(installClaude, "npm install -g @anthropic-ai/claude-code") {
			t.Error("install_claude() must have npm global install as fallback")
		}
	})

	t.Run("has_sudo_npm_sub_fallback", func(t *testing.T) {
		re := regexp.MustCompile(`sudo\s+npm\s+install\s+-g`)
		if !re.MatchString(installClaude) {
			t.Error("install_claude() must have sudo npm as sub-fallback")
		}
	})

	t.Run("no_version_pinning", func(t *testing.T) {
		if strings.Contains(installClaude, "claude-code@") {
			t.Error("install_claude() must not pin claude-code version")
		}
	})

	t.Run("no_npm_global_prefix", func(t *testing.T) {
		if strings.Contains(installClaude, "npm-global") || strings.Contains(installClaude, "NPM_PREFIX") {
			t.Error("install_claude() must not use user-local npm prefix")
		}
	})

	t.Run("path_uses_local_bin", func(t *testing.T) {
		if !strings.Contains(installClaude, ".local/bin") {
			t.Error("install_claude() must add $HOME/.local/bin to PATH")
		}
	})

	t.Run("conditional_guard_preserved", func(t *testing.T) {
		if !strings.Contains(installClaude, "command_exists claude") {
			t.Error("install_claude() missing conditional guard (command_exists claude)")
		}
	})

	t.Run("configure_shell_path_no_npm_global", func(t *testing.T) {
		if strings.Contains(configureShell, "npm-global") {
			t.Error("configure_shell() PATH must not contain npm-global")
		}
	})

	t.Run("configure_shell_path_has_local_bin", func(t *testing.T) {
		if !strings.Contains(configureShell, ".local/bin") {
			t.Error("configure_shell() PATH must include .local/bin")
		}
	})

	t.Run("pip_require_hashes", func(t *testing.T) {
		if !strings.Contains(content, "--require-hashes") {
			t.Error("quickstart.sh missing --require-hashes in pip install")
		}
	})
}

// TestQuickstartTelemetryProvisioning locks in the Phase-5a setup_telemetry() contract as
// static text analysis (the same style as TestQuickstartSupplyChainInvariants — quickstart.sh
// installs software and mutates the factory, so it is never executed here; live provisioning is
// P6b's manual runbook). Every subtest maps to a design acceptance criterion. It is polarity-
// agnostic: it asserts a real telemetry case arm exists in parse_args without pre-judging whether
// the operator chose --no-telemetry (opt-out) or --telemetry (opt-in) — operator decision O-1.
func TestQuickstartTelemetryProvisioning(t *testing.T) {
	root := findModuleRoot(t)
	scriptPath := filepath.Join(root, "quickstart.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	setupTelemetry := extractShellFunction(content, "setup_telemetry")
	if setupTelemetry == "" {
		t.Fatal("could not extract setup_telemetry() function body")
	}

	// AC #3 — first checksummed download in this file (Dockerfile:28 idiom).
	t.Run("sha256_checksum_verification", func(t *testing.T) {
		if !strings.Contains(setupTelemetry, "sha256sum --check") {
			t.Error("setup_telemetry() must checksum-verify the downloaded binary (sha256sum --check)")
		}
	})

	// AC #8 — secrets-dir + secret-file permissions (litellm idiom).
	t.Run("secrets_dir_chmod_700", func(t *testing.T) {
		if !strings.Contains(setupTelemetry, "chmod 700") {
			t.Error("setup_telemetry() must chmod 700 the secrets dir")
		}
	})
	t.Run("secret_file_chmod_600", func(t *testing.T) {
		if !strings.Contains(setupTelemetry, "chmod 600") {
			t.Error("setup_telemetry() must chmod 600 the telemetry.auth secret")
		}
	})

	// AC #4 — loopback only, never a wildcard bind.
	t.Run("loopback_bind_never_wildcard", func(t *testing.T) {
		if !strings.Contains(setupTelemetry, "127.0.0.1") {
			t.Error("setup_telemetry() must bind loopback (127.0.0.1) explicitly")
		}
		if strings.Contains(content, "0.0.0.0") {
			t.Error("quickstart.sh must never contain the 0.0.0.0 wildcard bind")
		}
	})

	// AC #5 — port-occupancy probe transplanted from quickdocker.sh, no new tool dependency.
	t.Run("port_probe_no_new_tool", func(t *testing.T) {
		if !strings.Contains(setupTelemetry, "/dev/tcp/") {
			t.Error("setup_telemetry() must use the pure-bash /dev/tcp port probe (quickdocker.sh:92)")
		}
		if regexp.MustCompile(`\b(lsof|netstat|nc -z)\b`).MatchString(setupTelemetry) {
			t.Error("setup_telemetry() must not introduce lsof/netstat/nc — none is a repo dependency")
		}
	})

	// telemetry.json seed: one place, only-when-absent, api.md:116-125 shape, no enabled field.
	t.Run("seeds_telemetry_json_when_absent", func(t *testing.T) {
		if !strings.Contains(setupTelemetry, ".agentfactory/telemetry.json") {
			t.Error("setup_telemetry() must seed .agentfactory/telemetry.json")
		}
		if !strings.Contains(setupTelemetry, `[ ! -f ".agentfactory/telemetry.json" ]`) {
			t.Error("setup_telemetry() must seed telemetry.json only when absent (never clobber operator edits)")
		}
	})
	t.Run("writes_auth_secret_file", func(t *testing.T) {
		if !strings.Contains(setupTelemetry, ".agentfactory/secrets/telemetry.auth") {
			t.Error("setup_telemetry() must write the OTLP auth secret to .agentfactory/secrets/telemetry.auth")
		}
	})
	t.Run("no_enabled_field_in_seed", func(t *testing.T) {
		if strings.Contains(setupTelemetry, `"enabled"`) {
			t.Error("telemetry.json seed must NOT carry an enabled field — enablement is the gate file")
		}
	})

	// Installed != enabled: never touch the gate; no interactive prompt (ADR-014).
	t.Run("does_not_touch_gate_file", func(t *testing.T) {
		if strings.Contains(setupTelemetry, ".telemetry-gate") {
			t.Error("setup_telemetry() must NOT touch .telemetry-gate (installed != enabled)")
		}
	})
	// AC #6 — regex, not Contains: a bare Contains("read") false-positives on "already"/"readiness".
	t.Run("no_interactive_prompt", func(t *testing.T) {
		if regexp.MustCompile(`(?m)^[[:space:]]*(read|select)[[:space:]]`).MatchString(setupTelemetry) {
			t.Error("setup_telemetry() must not add an interactive read/select prompt (ADR-014)")
		}
	})

	// AC #9 — a third login-shell restart guard (webui, litellm, telemetry).
	t.Run("third_login_guard", func(t *testing.T) {
		if !strings.Contains(setupTelemetry, "# BEGIN agentfactory telemetry login guard") {
			t.Error("setup_telemetry() must install a third login-shell restart guard")
		}
		if got := strings.Count(content, "BEGIN agentfactory "); got < 3 {
			t.Errorf("expected 3 BEGIN login-guard markers (webui, litellm, telemetry), got %d", got)
		}
	})

	// AC #1 — invoked LAST in main(), after setup_litellm.
	t.Run("invoked_last_in_main", func(t *testing.T) {
		callRe := regexp.MustCompile(`(?m)^[[:space:]]*setup_telemetry[[:space:]]*$`)
		litellmRe := regexp.MustCompile(`(?m)^[[:space:]]*setup_litellm[[:space:]]*$`)
		tl := callRe.FindStringIndex(content)
		ll := litellmRe.FindStringIndex(content)
		if tl == nil {
			t.Fatal("main() must invoke setup_telemetry as a bare call line")
		}
		if ll == nil || tl[0] <= ll[0] {
			t.Error("setup_telemetry must be invoked after setup_litellm (the new last setup call)")
		}
	})

	// AC #2 — the flag is a REAL parse_args case arm, not swallowed by the *) unknown-flag warning.
	t.Run("flag_recognized_in_parse_args", func(t *testing.T) {
		parseArgs := extractShellFunction(content, "parse_args")
		if parseArgs == "" {
			t.Fatal("could not extract parse_args() function body")
		}
		if !strings.Contains(parseArgs, "telemetry)") {
			t.Error("parse_args() must have a real telemetry case arm (not swallowed by the *) unknown-flag warning)")
		}
	})

	// AC #10 — script still parses.
	t.Run("shell_syntax_valid", func(t *testing.T) {
		cmd := exec.Command("bash", "-n", scriptPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("quickstart.sh has syntax errors: %s\n%s", err, output)
		}
	})

	// O-3 — the seeded telemetry.json / secret are NOT tracked artifacts (design default: ignored).
	t.Run("seeded_config_not_committed", func(t *testing.T) {
		for _, p := range []string{".agentfactory/telemetry.json", ".agentfactory/secrets/telemetry.auth"} {
			if _, err := os.Stat(filepath.Join(root, p)); err == nil {
				t.Errorf("%s must not be a committed artifact (O-3: left in the ignored models.json tier)", p)
			}
		}
	})
}

// TestQuickstartConfigureFactoryDiscovery locks in tech-stack-agnostic repo
// discovery: configure_factory must find the cloned target repo by its .git
// directory alone, never by go.mod, so non-Go customer repos are selected
// (issue af-8b4ee574 / GitHub #336). The install_af AF-source go.mod check is
// separate and must be preserved.
func TestQuickstartConfigureFactoryDiscovery(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	configureFactory := extractShellFunction(content, "configure_factory")
	if configureFactory == "" {
		t.Fatal("could not extract configure_factory() function body")
	}

	installAf := extractShellFunction(content, "install_af")
	if installAf == "" {
		t.Fatal("could not extract install_af() function body")
	}

	// Scenario: configure_factory discovery is stack-agnostic
	t.Run("configure_factory_discovery_is_stack_agnostic", func(t *testing.T) {
		if strings.Contains(configureFactory, "go.mod") {
			t.Error("configure_factory() must not reference go.mod: discovery must be tech-stack-agnostic (no go.mod in the loop, comment, or error message)")
		}
	})

	// Scenario: configure_factory keeps the .git filter
	t.Run("configure_factory_keeps_git_filter", func(t *testing.T) {
		if !strings.Contains(configureFactory, `[ -d "$d/.git" ]`) {
			t.Error("configure_factory() must keep the [ -d \"$d/.git\" ] filter so non-git scratch dirs (e.g. aftmp) are excluded")
		}
	})

	// Scenario: configure_factory error message no longer names go.mod
	t.Run("configure_factory_error_no_longer_names_go_mod", func(t *testing.T) {
		if !strings.Contains(configureFactory, "No git repository") {
			t.Error("configure_factory() discovery-failure error must say 'No git repository'")
		}
		if strings.Contains(configureFactory, "go.mod") {
			t.Error("configure_factory() error message must not name go.mod")
		}
	})

	// Scenario: install_af still verifies the agentfactory source go.mod
	t.Run("install_af_preserves_source_go_mod_check", func(t *testing.T) {
		if !strings.Contains(installAf, "$SCRIPT_DIR/go.mod") {
			t.Error("install_af() must keep its $SCRIPT_DIR/go.mod AF-source check")
		}
		if !strings.Contains(installAf, "agentfactory") {
			t.Error("install_af() must keep grepping the source go.mod for agentfactory")
		}
	})
}
