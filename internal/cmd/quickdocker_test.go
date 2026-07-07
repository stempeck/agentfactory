package cmd

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

// TestQuickdockerHostportDerivation guards Issue #425 Phase 1B: quickdocker.sh must
// carry a deterministic, stateless host (laptop) listen-port derivation
// (HOSTPORT = 20000 + cksum(name) mod 10000) keyed on the UNIQUE CONTAINER_NAME
// (af_<user>_<repo>), NOT the colliding REPO_NAME basename. Hashing the basename
// would re-open the design's "Gap 9" (alice/myrepo and bob/myrepo collide). These
// assertions run the exact AC#5 grep commands so they match the acceptance criteria
// byte-for-byte, and are negative/structural in the style of the sibling #272 guard:
// the runtime math + errexit-safety of the formula are proven separately by the
// executable todos/ultra-implement/run-verify.sh.
func TestQuickdockerHostportDerivation(t *testing.T) {
	root := findModuleRoot(t)
	scriptPath := filepath.Join(root, "quickdocker.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("reading quickdocker.sh: %v", err)
	}
	content := string(data)

	// AC#5, grep 1: `grep -nE 'cksum|HOSTPORT' quickdocker.sh` must MATCH (derivation present).
	t.Run("derivation_present", func(t *testing.T) {
		cmd := exec.Command("grep", "-nE", "cksum|HOSTPORT", scriptPath)
		out, err := cmd.CombinedOutput()
		if err != nil { // grep exits 1 when there is no match
			t.Errorf("AC#5: expected a cksum/HOSTPORT derivation in quickdocker.sh, found none: %v\n%s", err, out)
		}
	})

	// AC#5, grep 2: `grep -nE 'REPO_NAME[^=].*(cksum|HOSTPORT)|cksum.*REPO_NAME'` must NOT match.
	// REPO_NAME is the colliding basename; it must never be the hash input (Gap 9).
	t.Run("not_keyed_on_repo_name", func(t *testing.T) {
		cmd := exec.Command("grep", "-nE", `REPO_NAME[^=].*(cksum|HOSTPORT)|cksum.*REPO_NAME`, scriptPath)
		out, _ := cmd.CombinedOutput() // exit 1 (no match) is the success case
		if strings.TrimSpace(string(out)) != "" {
			t.Errorf("AC#5: REPO_NAME (basename) must NOT be the cksum/HOSTPORT hash input — re-opens Gap 9:\n%s", out)
		}
	})

	// Positive enforcement of the phase's single most important correctness point: the
	// derivation keys on the UNIQUE CONTAINER_NAME — either by hashing it directly or via
	// the documented Phase-2 call `derive_hostport "$CONTAINER_NAME"`.
	t.Run("keyed_on_container_name", func(t *testing.T) {
		re := regexp.MustCompile(`derive_hostport[^\n]*CONTAINER_NAME|cksum[^\n]*CONTAINER_NAME|CONTAINER_NAME[^\n]*cksum`)
		if !re.MatchString(content) {
			t.Error("AC#5: the HOSTPORT derivation must key on the unique CONTAINER_NAME (af_<user>_<repo>), not the basename")
		}
	})

	// The derivation must use printf '%s' (no trailing newline) as the cksum input — `echo`
	// would append a newline and change the CRC, breaking the AC's pinned ports (22167/21793).
	t.Run("hash_input_has_no_trailing_newline", func(t *testing.T) {
		if strings.Contains(content, "cksum") {
			re := regexp.MustCompile(`printf\s+'%s'[^\n]*\|\s*cksum`)
			if !re.MatchString(content) {
				t.Error("AC#1/AC#3: cksum input must come from printf of the bare string (not echo) — echo's trailing newline changes the CRC and the derived port")
			}
		}
	})

	// The added shell must still parse cleanly under bash.
	t.Run("shell_syntax_valid", func(t *testing.T) {
		cmd := exec.Command("bash", "-n", scriptPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("quickdocker.sh has syntax errors after the Phase 1B change: %s\n%s", err, out)
		}
	})
}

// TestQuickdockerWebShellBridge guards Issue #425 Phase 2: quickdocker.sh must have an
// EARLY `--web`/`--shell` short-circuit that acts on an
// already-running container and exits BEFORE any clone/create/recreate logic (ADR-017: af
// infrastructure must never `docker rm -f` the customer's container). `--shell` runs the
// canonical `docker exec -it -u dev "$CONTAINER_NAME" bash`; `--web` stands up a detached,
// idempotent, 127.0.0.1-only forking bridge (host tool auto-detect socat→python3) piping
// each connection through `docker exec` into an in-container python3 relay reaching webui's
// loopback `address` (read from .runtime/webui_server.json), prints http://127.0.0.1:<HOSTPORT>/,
// and self-exits when the container stops.
//
// These assertions are STRUCTURAL (read-the-script + bash -n + AC grep arms), mirroring the
// Phase-1B precedent TestQuickdockerHostportDerivation. The live bridge (curl /healthz, real
// recreate-id, process-gone-after-stop) is integration-tier and is proven in Phase 5A on a real
// docker host — it cannot be exercised in a docker-less sandbox. Two grep traps the investigation
// surfaced are encoded here: (1) the IMPLREADME's literal AC-4 arm `\-p[ =]|--publish`
// false-positives on `mkdir -p`, so the no-publish check is scoped to the `docker run` block; and
// (2) AC-6's bare `--web` grep already matches a Phase-1B *comment* in quickdocker.sh, so the
// presence check additionally requires a real `--web)`/`--shell)` handling arm.
func TestQuickdockerWebShellBridge(t *testing.T) {
	root := findModuleRoot(t)
	type script struct {
		name string
		path string
	}
	// The bridge/create logic lives ONLY in quickdocker.sh now. quickdocker-pro.sh is a
	// thin wrapper that pulls the registry image and `exec`s quickdocker.sh, so it is
	// asserted separately (TestQuickdockerProThinWrapper, pro-only) rather than as a parallel copy.
	scripts := []script{
		{"quickdocker.sh", filepath.Join(root, "quickdocker.sh")},
	}
	read := func(t *testing.T, path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		return string(data)
	}

	// ── AC-6: both scripts gained REAL handling for --web and --shell (not just a comment) ──
	for _, s := range scripts {
		s := s
		t.Run("flags_present_in_both/"+s.name, func(t *testing.T) {
			// AC-6 grep arm verbatim: must match in BOTH scripts.
			cmd := exec.Command("grep", "-nE", `\-\-web|\-\-shell`, s.path)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("AC-6: expected --web/--shell in %s, found none: %v\n%s", s.name, err, out)
			}
			// Not satisfiable by the Phase-1B comment alone: require real case arms.
			content := read(t, s.path)
			if !strings.Contains(content, "--web)") {
				t.Errorf("AC-6: %s must have a real `--web)` handling arm, not just a comment mention", s.name)
			}
			if !strings.Contains(content, "--shell)") {
				t.Errorf("AC-6: %s must have a real `--shell)` handling arm", s.name)
			}
		})
	}

	// ── AC-4 / ADR-017 (THE #1 RISK): short-circuit BEFORE create/recreate, and it must exit ──
	for _, s := range scripts {
		s := s
		t.Run("short_circuit_before_create_recreate/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			idxWeb := strings.Index(content, "--web)")
			idxShell := strings.Index(content, "--shell)")
			idxFlag := idxWeb
			if idxShell >= 0 && (idxFlag < 0 || idxShell < idxFlag) {
				idxFlag = idxShell
			}
			idxRecreate := strings.Index(content, "Remove and recreate")
			idxRun := strings.Index(content, "docker run -dit")
			if idxFlag < 0 {
				t.Fatalf("AC-4: %s has no --web)/--shell) handling arm", s.name)
			}
			if idxRecreate < 0 || idxRun < 0 {
				t.Fatalf("AC-4: %s missing expected anchors (recreate=%d, docker run=%d)", s.name, idxRecreate, idxRun)
			}
			if !(idxFlag < idxRecreate) {
				t.Errorf("AC-4: %s handles the flags AT/AFTER the recreate prompt (flag@%d, recreate@%d) — would rebuild the customer's container", s.name, idxFlag, idxRecreate)
			}
			if !(idxFlag < idxRun) {
				t.Errorf("AC-4: %s handles the flags AT/AFTER `docker run -dit` (flag@%d, run@%d) — must short-circuit before container creation", s.name, idxFlag, idxRun)
			}
			// The short-circuit must actually leave the script (exit 0 / exec) before the create path.
			region := content[idxFlag:idxRecreate]
			if !strings.Contains(region, "exit 0") && !regexp.MustCompile(`\bexec\b`).MatchString(region) {
				t.Errorf("AC-4: %s short-circuit must `exit 0`/`exec` before the recreate path; none found between the flag arm and the recreate prompt", s.name)
			}
		})
	}

	// ── AC-4 / AC-5: never publish a port (scoped to the docker run block; avoids mkdir -p trap) ──
	for _, s := range scripts {
		s := s
		t.Run("no_published_port/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			block := regexp.MustCompile(`(?s)docker run -dit.*?bash --login`).FindString(content)
			if block == "" {
				t.Fatalf("%s: could not locate the `docker run -dit … bash --login` block", s.name)
			}
			if strings.Contains(block, "--publish") {
				t.Errorf("AC-4/AC-5: %s docker run block must not --publish a port:\n%s", s.name, block)
			}
			if regexp.MustCompile(`\s-p[ =]`).MatchString(block) {
				t.Errorf("AC-4/AC-5: %s docker run block must not add a -p flag:\n%s", s.name, block)
			}
			// Precise AC arm (single-line) must also find nothing.
			if out, err := exec.Command("grep", "-nE", `docker run.*(-p |--publish)`, s.path).CombinedOutput(); err == nil {
				t.Errorf("AC-4/AC-5: %s: `docker run` must never carry -p/--publish:\n%s", s.name, out)
			}
		})
	}

	// ── AC-3: the --shell arm runs the canonical interactive exec ──
	for _, s := range scripts {
		s := s
		t.Run("shell_arm_runs_docker_exec/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			if !regexp.MustCompile(`docker exec -it -u dev[^\n]*"\$CONTAINER_NAME"[^\n]*bash`).MatchString(content) {
				t.Errorf("AC-3: %s must run `docker exec -it -u dev \"$CONTAINER_NAME\" bash`", s.name)
			}
			// That exec must live in the short-circuit (before the recreate path), not only as the closing line.
			idxShell := strings.Index(content, "--shell)")
			idxRecreate := strings.Index(content, "Remove and recreate")
			if idxShell < 0 || idxRecreate < 0 {
				t.Fatalf("%s missing --shell)/recreate anchors", s.name)
			}
			if !strings.Contains(content[idxShell:idxRecreate], "docker exec -it -u dev") {
				t.Errorf("AC-3: %s --shell arm must invoke `docker exec -it -u dev … bash` within the short-circuit", s.name)
			}
		})
	}

	// ── AC-1/AC-2 setup: HOSTPORT derivation is called in quickdocker.sh, still Gap-9-safe ──
	t.Run("hostport_derivation_called_quickdocker", func(t *testing.T) {
		content := read(t, scripts[0].path)
		if !regexp.MustCompile(`derive_hostport[^\n]*"\$CONTAINER_NAME"`).MatchString(content) {
			t.Error("AC-1: quickdocker.sh must CALL derive_hostport \"$CONTAINER_NAME\" (Phase-1B helper, now used)")
		}
	})
	// quickdocker-pro.sh is a THIN WRAPPER over quickdocker.sh; because it is pro-only and not
	// shipped to OSS, its thin-wrapper invariants are asserted in TestQuickdockerProThinWrapper
	// (quickdocker_pro_test.go, SKIPOSS) rather than as a subtest here.
	for _, s := range scripts {
		s := s
		t.Run("hostport_not_keyed_on_repo_name/"+s.name, func(t *testing.T) {
			// Gap-9 guard carries into both scripts: the colliding basename must never be the hash input.
			out, _ := exec.Command("grep", "-nE", `REPO_NAME[^=].*(cksum|HOSTPORT)|cksum.*REPO_NAME`, s.path).CombinedOutput()
			if strings.TrimSpace(string(out)) != "" {
				t.Errorf("AC: %s must not key HOSTPORT on REPO_NAME (basename) — re-opens Gap 9:\n%s", s.name, out)
			}
		})
	}

	// ── AC-1 precondition: require webui installed inside; clear error; never build on the fly ──
	for _, s := range scripts {
		s := s
		t.Run("webui_installed_check/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			if !regexp.MustCompile(`test -x[^\n]*webui`).MatchString(content) {
				t.Errorf("AC-1: %s --web must check `test -x …webui` (require webui installed inside)", s.name)
			}
			if !strings.Contains(content, "webui not installed") {
				t.Errorf("AC-1: %s must error clearly with `webui not installed …` (do not build on the fly)", s.name)
			}
		})
	}

	// ── Host tool auto-detect is socat → python3; the nc branch is DROPPED (Issue #428). ──
	// BSD/Apple nc (macOS) and OpenBSD nc (default Linux) both reject `-e`, so the old
	// `nc -l … -e` arm bound nothing and produced the reported "can't find a page". The
	// old assertion here required the literal "nc" to be PRESENT — it rewarded the broken
	// branch (Concern #5). This replacement asserts the ladder is socat→python3 (in that
	// order) and that NO `nc -l … -e` arm remains, so re-introducing nc fails the gate.
	for _, s := range scripts {
		s := s
		t.Run("host_listener_tool_detection/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			for _, tool := range []string{"socat", "python3"} {
				if !strings.Contains(content, tool) {
					t.Errorf("%s --web host-tool auto-detect must reference %q (socat→python3)", s.name, tool)
				}
			}
			// The detection ladder must rank socat before python3 (socat preferred; python3
			// is the universal fallback — the in-container relay already hard-requires it).
			iSocat := strings.Index(content, "command -v socat")
			iPython := strings.Index(content, "command -v python3")
			if iSocat < 0 || iPython < 0 {
				t.Fatalf("%s: tool-detect ladder must probe `command -v socat` and `command -v python3` (socat@%d python3@%d)", s.name, iSocat, iPython)
			}
			if iSocat >= iPython {
				t.Errorf("%s: tool-detect ladder must rank socat before python3 (socat@%d, python3@%d)", s.name, iSocat, iPython)
			}
			// The broken `nc -l … -e` listener arm must be GONE. Re-introducing it fails here.
			if regexp.MustCompile(`nc -l[^\n]*-e`).MatchString(content) {
				t.Errorf("%s: the broken `nc -l … -e` listener arm must NOT be present (BSD/OpenBSD nc reject -e → nothing binds)", s.name)
			}
			if !strings.Contains(content, "need socat or python3") {
				t.Errorf("%s must error clearly when no host listener tool is present (`need socat or python3 …`)", s.name)
			}
		})
	}

	// ── AC-5: listener binds 127.0.0.1 only; NEVER 0.0.0.0 anywhere ──
	for _, s := range scripts {
		s := s
		t.Run("listener_is_loopback_only/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			if !strings.Contains(content, "127.0.0.1") {
				t.Errorf("AC-5: %s --web must bind/print 127.0.0.1", s.name)
			}
			if out, err := exec.Command("grep", "-nF", "0.0.0.0", s.path).CombinedOutput(); err == nil {
				t.Errorf("AC-5: %s must never reference 0.0.0.0 (no LAN/internet exposure):\n%s", s.name, out)
			}
		})
	}

	// ── AC-2: idempotency via a host pidfile keyed by container + a liveness-guarded reprint ──
	for _, s := range scripts {
		s := s
		t.Run("idempotency_marker_keyed_by_container/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			if !strings.Contains(content, "af-web-bridge") {
				t.Errorf("AC-2: %s must use a host pidfile/marker (e.g. af-web-bridge-<container>.pid) for idempotency", s.name)
			}
			if !strings.Contains(content, "kill -0") {
				t.Errorf("AC-2: %s must liveness-check the existing bridge (kill -0) before reprinting vs relaunching", s.name)
			}
			// Issue #428: the marker must persist the bound PORT ("PID PORT"), and the reuse
			// path must TCP-probe that persisted port. Probing the freshly-derived HOSTPORT
			// would miss the live listener (derive_hostport's free-port scan advances past the
			// bridge's own port → HOSTPORT+1) and spawn a duplicate listener on a shifted URL.
			if !regexp.MustCompile(`printf[^\n]*hostport[^\n]*> "\$marker"`).MatchString(content) {
				t.Errorf(`AC-2: %s must persist the bound port in the marker (printf "%%s %%s" "$$" "$hostport" > "$marker"), not just the PID`, s.name)
			}
			if !strings.Contains(content, "_port_in_use") {
				t.Errorf("AC-2: %s reuse check must TCP-probe the persisted port (_port_in_use) before reprinting, so a dead listener self-heals", s.name)
			}
		})
	}

	// ── async: detached launch, not a foreground session ──
	for _, s := range scripts {
		s := s
		t.Run("detached_launch/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			if !strings.Contains(content, "setsid") && !strings.Contains(content, "nohup") {
				t.Errorf("%s --web must launch the bridge DETACHED (setsid/nohup) and return immediately", s.name)
			}
		})
	}

	// ── AC-5 liveness: the bridge watches the container and self-exits ──
	for _, s := range scripts {
		s := s
		t.Run("self_exit_on_container_stop/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			if !strings.Contains(content, "docker inspect") {
				t.Errorf("AC-5: %s bridge must watch container liveness (docker inspect) and self-exit when it stops", s.name)
			}
		})
	}

	// ── AC-1: read webui's loopback address from the rendezvous file ──
	for _, s := range scripts {
		s := s
		t.Run("rendezvous_address_read/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			if !strings.Contains(content, "webui_server.json") {
				t.Errorf("AC-1: %s --web must read .runtime/webui_server.json", s.name)
			}
			if !strings.Contains(content, "address") {
				t.Errorf("AC-1: %s --web must read the lowercase `address` field from the rendezvous file", s.name)
			}
		})
	}

	// ── AC-1: print the clickable loopback URL ──
	for _, s := range scripts {
		s := s
		t.Run("prints_clickable_link/"+s.name, func(t *testing.T) {
			content := read(t, s.path)
			if !regexp.MustCompile(`http://127\.0\.0\.1:\$\{?HOSTPORT`).MatchString(content) {
				t.Errorf("AC-1: %s must print http://127.0.0.1:${HOSTPORT}/", s.name)
			}
		})
	}

	// ── hygiene: both scripts still parse under bash ──
	for _, s := range scripts {
		s := s
		t.Run("shell_syntax_valid/"+s.name, func(t *testing.T) {
			if out, err := exec.Command("bash", "-n", s.path).CombinedOutput(); err != nil {
				t.Errorf("%s has syntax errors after the Phase 2 change: %s\n%s", s.name, err, out)
			}
		})
	}
}

// TestWebBridgeHostListenerRoundTrip is the host-only behavioral interlock for Issue #428
// (Concern #5 — the executing CI gate the structural-grep tests never provided). It does NOT
// re-implement the tool selection — it extracts the REAL `if command -v socat … fi` ladder and
// the REAL `case "$tool" in … esac` listener launcher verbatim from each script, binds the
// selected listener on a free 127.0.0.1 port with a trivial /bin/cat echo handler, sends one
// byte, half-closes, and asserts it reads the same byte back.
//
// Why this is the interlock: BSD/Apple nc (macOS) and OpenBSD nc (default Linux) reject `-e`,
// so the pre-fix ladder (socat→nc→python3) selected nc on macOS and bound nothing. Driving the
// script's OWN selection + OWN listener means this test FAILS on a macOS runner before the fix
// (nc selected, no bind, round-trip times out) and PASSES after the fix (python3 selected,
// binds and echoes). A re-implementation could pick python3 and pass while the shell stayed
// broken — extraction-from-source closes that false-green hole. No docker; hermetic unit lane.
func TestWebBridgeHostListenerRoundTrip(t *testing.T) {
	root := findModuleRoot(t)
	// Only quickdocker.sh carries the listener now; quickdocker-pro.sh is a thin wrapper
	// that defers to it (see TestQuickdockerProThinWrapper in quickdocker_pro_test.go).
	for _, name := range []string{"quickdocker.sh"} {
		name := name
		t.Run(name, func(t *testing.T) {
			// Issue #435: the ladder exit 1's binding nothing when neither socat nor python3
			// is on PATH (quickdocker.sh:141-147), so without a tool this test can only ever
			// time out on the dial budget below with a misleading message. Skip fast and
			// truthfully instead — the repo's skip-on-absent-tool idiom (worktree_test.go:189-190).
			// CI provisions python3 in the `unit` job (.github/workflows/test.yml), so this skip
			// can only fire on a developer machine, never as the CI outcome (AC#4).
			tool, ok := hostListenerToolPresent()
			if !ok {
				t.Skip("neither socat nor python3 on PATH; cannot exercise quickdocker.sh's host-listener ladder")
			}
			t.Logf("host listener tool present: %s", tool)
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			content := string(data)

			// Extract the script's REAL selection ladder and listener case (verbatim).
			sel := regexp.MustCompile(`(?s)if\s+command -v socat.*?\n\s*fi`).FindString(content)
			if sel == "" {
				t.Fatalf("%s: could not extract the `if command -v socat … fi` tool-selection ladder", name)
			}
			listenerCase := regexp.MustCompile(`(?s)case "\$tool" in.*?\n\s*esac`).FindString(content)
			if listenerCase == "" {
				t.Fatalf("%s: could not extract the `case \"$tool\" in … esac` listener launcher", name)
			}

			// Reserve a free loopback port, then hand it to the script's listener.
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("reserving a free 127.0.0.1 port: %v", err)
			}
			port := l.Addr().(*net.TCPAddr).Port
			_ = l.Close()

			// Harness: run the EXTRACTED selection + EXTRACTED listener case. /bin/cat is the
			// trivial echo handler (works for socat EXEC:, python3 Popen, and nc -e alike).
			harness := "set +e\n" +
				"hostport=\"$1\"\n" +
				"handler=\"$2\"\n" +
				"_tool=\"\"\n" +
				sel + "\n" +
				"tool=\"$_tool\"\n" +
				"printf 'SELECTED=%s\\n' \"$tool\" >&2\n" +
				listenerCase + "\n" +
				"lpid=\"$!\"\n" +
				"wait \"$lpid\" 2>/dev/null\n"

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-c", harness, "_", strconv.Itoa(port), "/bin/cat")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own process group so we can reap fork children
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatalf("starting listener harness: %v", err)
			}
			defer func() {
				if cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // kill the whole group (socat/python3 children)
				}
				_ = cmd.Wait()
			}()

			// The bind is async; dial with a short retry budget. A tool that cannot bind
			// (pre-fix BSD nc) never accepts → this exhausts and the test fails.
			addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			var conn net.Conn
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				if conn, err = net.DialTimeout("tcp", addr, 400*time.Millisecond); err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if conn == nil {
				t.Fatalf(hostListenerNoAcceptFmt, name, addr, stderr.String())
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

			want := []byte("X")
			if _, err := conn.Write(want); err != nil {
				t.Fatalf("%s: write to listener failed: %v\nstderr:\n%s", name, err, stderr.String())
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.CloseWrite() // half-close so /bin/cat sees EOF, flushes, and the relay returns
			}
			got, err := io.ReadAll(conn)
			if err != nil {
				t.Fatalf("%s: reading the echoed byte failed: %v\nstderr:\n%s", name, err, stderr.String())
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s: round-trip mismatch through the selected listener: sent %q, got %q\nstderr:\n%s",
					name, want, got, stderr.String())
			}
		})
	}
}

// hostListenerNoAcceptFmt is the failure message emitted when the script-selected host
// listener never accepts within the dial budget. Args: script name, dial addr, harness
// stderr. (Reworded for Issue #435 to name the true condition; see the guard test.)
const hostListenerNoAcceptFmt = "%s: the script-selected host listener never accepted on %s within the dial budget — " +
	"the ladder selected a tool that failed to bind (see SELECTED=<tool> in the harness stderr below). harness stderr:\n%s"

// hostListenerToolPresent reports the first quickdocker.sh-ladder listener tool on PATH
// (socat preferred, then python3) and whether any was found. The behavioral round-trip
// test cannot bind a listener without one — the ladder exit 1's binding nothing
// (quickdocker.sh:141-147) — so it skips fast and truthfully instead of burning the full
// dial budget on a misleading failure. Mirrors the repo's skip-on-absent-tool idiom
// (internal/worktree/worktree_test.go:189-190 LookPath+Skip). Issue #435.
func hostListenerToolPresent() (string, bool) {
	for _, tool := range []string{"socat", "python3"} {
		if _, err := exec.LookPath(tool); err == nil {
			return tool, true
		}
	}
	return "", false
}

// TestWebBridgeHostListenerToolGuard is the host-INDEPENDENT guard for Issue #435: it
// asserts the round-trip test's honesty contract (a tool-detection helper for the fast
// skip; an accurate no-accept message) and the unit lane's tool provisioning — without
// binding any socket. Like TestQuickdockerWebShellBridge (:427-459) it reads source and
// constants, so it stays green on a bare runner and pins the fix against regression.
func TestWebBridgeHostListenerToolGuard(t *testing.T) {
	// Scenario: "Round-trip skips fast and truthfully when no listener tool is present"
	// — the helper that decides that skip.
	t.Run("detects_present_tool", func(t *testing.T) {
		tool, ok := hostListenerToolPresent()
		if !ok {
			t.Skip("neither socat nor python3 on PATH; cannot assert tool detection here")
		}
		if tool != "socat" && tool != "python3" {
			t.Fatalf("hostListenerToolPresent returned %q; want socat or python3 (the only ladder tools)", tool)
		}
	})

	// Scenario: "The no-accept failure message names the true condition, not dropped nc."
	// Assert the CONSTANT's value (not the source text) so this guard cannot self-match.
	t.Run("failure_message_names_true_condition", func(t *testing.T) {
		for _, banned := range []string{"BSD", "macOS", "pre-fix"} {
			if strings.Contains(hostListenerNoAcceptFmt, banned) {
				t.Errorf("no-accept message must not blame the dropped nc / pre-fix macOS case (nc left the ladder in #428); found %q in: %q", banned, hostListenerNoAcceptFmt)
			}
		}
		if !strings.Contains(hostListenerNoAcceptFmt, "failed to bind") {
			t.Errorf("no-accept message must name the true condition (the selected tool failed to bind); got: %q", hostListenerNoAcceptFmt)
		}
	})

	// Scenario: "The unit CI lane provisions a host-listener tool." Pins the PRIMARY
	// interlock — the `unit` job must guarantee python3 (setup-python@v5), so the round-trip
	// is always exercised in CI and the skip can only ever fire on a dev laptop.
	t.Run("unit_lane_provisions_listener_tool", func(t *testing.T) {
		root := findModuleRoot(t)
		data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "test.yml"))
		if err != nil {
			t.Fatalf("reading .github/workflows/test.yml: %v", err)
		}
		content := string(data)
		unitStart := strings.Index(content, "\n  unit:")
		webStart := strings.Index(content, "\n  web-unit:")
		if unitStart < 0 || webStart <= unitStart {
			t.Fatalf("could not locate the `unit:` job block in test.yml (unit@%d web-unit@%d)", unitStart, webStart)
		}
		unitBlock := content[unitStart:webStart]
		if !strings.Contains(unitBlock, "actions/setup-python@v5") {
			t.Errorf("the `unit` job must provision a host-listener tool via actions/setup-python@v5 "+
				"(mirroring the integration job at :96-99) so TestWebBridgeHostListenerRoundTrip is "+
				"exercised, not skipped, in CI (Issue #435). unit job block:\n%s", unitBlock)
		}
	})
}

// TestQuickdockerWebRevealAtCompletion guards Issue #479 Phase 1 (commit 85c465fd) against
// silent regression. Phase 1 made three coordinated edits with no test:
//
//  1. the completion-path web reveal `if ! ( _web_bridge "$CONTAINER_NAME" … )` runs AFTER
//     the `Setup complete!` banner and BEFORE the final `docker exec … bash`, so the URL is
//     revealed before the user's single landing shell (quickdocker.sh:702 → :705);
//  2. Step-8's `docker exec` exports `-e AF_QUICKDOCKER_DRIVEN=1` (quickdocker.sh:641); and
//  3. quickstart.sh's redundant `exec bash` is guarded by `-z "${AF_QUICKDOCKER_DRIVEN:-}"`
//     (quickstart.sh:719 → :721), so the driven install lands ONE shell, not two.
//
// (2) and (3) are the two halves of a single coordinated edit (K6): dropping EITHER reopens
// the double-shell hop, so BOTH are asserted (peer review's single enforcement gap).
//
// These are true mutation guards, not presence checks — the ordering subtests use
// strings.Index ORDER comparisons so moving/dropping any edit goes RED (AC#3). The search is
// anchored from `Setup complete!` because both `_web_bridge "$CONTAINER_NAME"` (the --web arm
// at :381) and `docker exec -it -u dev "$CONTAINER_NAME" bash` (the --shell arm at :375)
// appear a SECOND time before the banner; a whole-file Index would bind to the wrong lines.
// The export is asserted with a whole-file Contains, not a `[^\n]*…quickstart.sh` regex —
// Step-8 spans two physical lines (the `\` continuation on :641, `./quickstart.sh` on :642),
// so `[^\n]*` cannot cross the newline and that regex would be a false-green.
//
// The isolation subtests run a docker-free bash harness (the structure mirrors the relay
// wrapper, with `_web_bridge` stubbed) to pin the central safety property: the mandatory
// subshell `( … )` confines `_web_bridge`'s `exit 1` under `set -euo pipefail`, so the shell
// always lands. No docker — they run in the CI `unit` lane (bash only).
func TestQuickdockerWebRevealAtCompletion(t *testing.T) {
	root := findModuleRoot(t)
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return string(data)
	}

	// ── AC#3 mutation guard: completion reveal runs after the banner and BEFORE the shell ──
	t.Run("completion_reveal_then_shell", func(t *testing.T) {
		content := read("quickdocker.sh")
		idxBanner := strings.Index(content, "Setup complete!")
		if idxBanner < 0 {
			t.Fatal("AC: quickdocker.sh missing the `Setup complete!` banner anchor")
		}
		// Anchor from the banner so we bind to the COMPLETION call (:702) and the FINAL shell
		// (:705), not the pre-banner --web (:381) / --shell (:375) arms that share the literals.
		tail := content[idxBanner:]
		idxWeb := strings.Index(tail, `_web_bridge "$CONTAINER_NAME"`)
		idxShell := strings.Index(tail, `docker exec -it -u dev "$CONTAINER_NAME" bash`)
		if idxWeb < 0 || idxShell < 0 || idxWeb >= idxShell {
			t.Errorf("AC#2/#3: completion _web_bridge (idx=%d) must run AFTER `Setup complete!` and "+
				"BEFORE the final `docker exec … bash` (idx=%d); -1 means the line was moved or removed",
				idxWeb, idxShell)
		}
		// The reveal must stay subshell-wrapped: `_web_bridge` has five `exit 1` paths under
		// `set -euo pipefail`, so only `if ! ( … )` confines them and lets the shell below land.
		if !regexp.MustCompile(`if ! \( _web_bridge "\$CONTAINER_NAME"`).MatchString(tail) {
			t.Error(`AC#3: completion reveal must be wrapped ` + "`if ! ( _web_bridge \"$CONTAINER_NAME\" … )`" +
				` so a bridge exit 1 is confined and the shell still lands (do not tidy the subshell away)`)
		}
	})

	// ── K6 export half: Step-8 must carry the coordination var (whole-file; occurs once) ──
	t.Run("step8_exports_driven_var", func(t *testing.T) {
		if !strings.Contains(read("quickdocker.sh"), "-e AF_QUICKDOCKER_DRIVEN=1") {
			t.Error("K6/AC#3: Step-8 `docker exec` must export `-e AF_QUICKDOCKER_DRIVEN=1` so quickstart " +
				"can suppress its redundant in-container `exec bash` (dropping it reopens the double-shell hop)")
		}
	})

	// ── K6 guard half (REQUIRED — peer review's single enforcement gap): quickstart's
	//    `exec bash` must be gated by the var, ordered guard-before-exec so dropping the
	//    guard alone goes RED even if the export half is still present. ──
	t.Run("quickstart_exec_bash_guarded", func(t *testing.T) {
		qs := read("quickstart.sh")
		idxGuard := strings.Index(qs, `-z "${AF_QUICKDOCKER_DRIVEN:-}"`)
		idxExec := strings.Index(qs, "exec bash")
		if idxGuard < 0 || idxExec < 0 || idxGuard >= idxExec {
			t.Errorf(`K6/AC#3: quickstart.sh `+"`exec bash`"+` (idx=%d) must be guarded by `+
				"`-z \"${AF_QUICKDOCKER_DRIVEN:-}\"`"+` (idx=%d) — ship both halves of K6 or neither`,
				idxExec, idxGuard)
		}
	})

	// ── Central safety property, docker-free: the subshell confines a failing bridge ──
	// Mirrors the relay wrapper shape at quickdocker.sh:702-705 with `_web_bridge` stubbed to
	// fail; the "shell that lands" is modelled by `echo LANDED` (no docker in the unit lane).
	t.Run("isolation_subshell_confines_exit", func(t *testing.T) {
		harness := "set -euo pipefail\n" +
			"_web_bridge(){ echo boom >&2; exit 1; }\n" +
			`if ! ( _web_bridge "x" "y" ); then echo NOTE >&2; fi` + "\n" +
			"echo LANDED\n"
		var stdout, stderr bytes.Buffer
		cmd := exec.Command("bash", "-c", harness)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("AC#4: the `if ! ( _web_bridge … )` wrapper must NOT fail fatally when the bridge "+
				"exit 1's — the subshell should confine it: %v\nstderr:\n%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "LANDED") {
			t.Errorf("AC#4: the shell must always land — stdout missing LANDED:\nstdout:\n%s", stdout.String())
		}
		if !strings.Contains(stderr.String(), "NOTE") {
			t.Errorf("AC#4: the non-fatal note branch must run when the bridge fails — stderr missing NOTE:\nstderr:\n%s", stderr.String())
		}
	})

	// ── Negative control: PROVE the subshell is load-bearing. Without `( … )`, `_web_bridge`'s
	//    `exit 1` exits the whole script (exit, not return), so LANDED is never reached. This is
	//    why quickdocker.sh:702 must keep the subshell — it pins the mechanism, not just the
	//    current outcome. ──
	t.Run("isolation_without_subshell_aborts", func(t *testing.T) {
		harness := "set -euo pipefail\n" +
			"_web_bridge(){ echo boom >&2; exit 1; }\n" +
			`if ! _web_bridge "x" "y"; then echo NOTE >&2; fi` + "\n" +
			"echo LANDED\n"
		var stdout bytes.Buffer
		cmd := exec.Command("bash", "-c", harness)
		cmd.Stdout = &stdout
		cmd.Stderr = io.Discard
		err := cmd.Run()
		if err == nil {
			t.Error("control: without the subshell, `_web_bridge`'s `exit 1` should abort the script (non-zero exit)")
		}
		if strings.Contains(stdout.String(), "LANDED") {
			t.Errorf("control: without the subshell the shell must NOT land — got LANDED, which means the "+
				"subshell at quickdocker.sh:702 is not what confines the exit:\nstdout:\n%s", stdout.String())
		}
	})
}
