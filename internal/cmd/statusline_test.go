package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestRender_ExitZeroAlways drives the render core through every documented fault. The core
// is the seam (not the RunE) because under `go test` os.Stdin is /dev/null — a character
// device — so the RunE's TTY guard would early-return and mask the malformed/oversized cases.
// The contract is only: nil error + no stderr (stdout may be empty or partial).
func TestRender_ExitZeroAlways(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	valid := `{"session_id":"sess_a","model":{"display_name":"Opus 4.8"},"workspace":{"project_dir":"/p"}}`

	cases := []struct {
		name  string
		setup func(t *testing.T, root string)
		stdin string
	}{
		{"no stdin", nil, ""},
		{"oversized stdin", nil, strings.Repeat(" ", 1<<20) + "junk"},
		{"malformed json", nil, "{not json"},
		{"corrupt config", func(t *testing.T, root string) {
			os.WriteFile(config.StatuslineConfigPath(root), []byte(`{"elements":["bogus-element"]}`), 0o644)
		}, valid},
		{"unwritable snapshot dir", func(t *testing.T, root string) {
			// StatuslineDir becomes a FILE ⇒ WriteSnapshot's MkdirAll(.../statusline/sessions)
			// fails ENOTDIR. The core must swallow that and still return nil.
			os.WriteFile(config.StatuslineDir(root), []byte("x"), 0o644)
		}, valid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := setupConfigFactory(t)
			os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0o644)
			if tc.setup != nil {
				tc.setup(t, root)
			}
			var out bytes.Buffer
			stderr := captureStderr(t, func() {
				if err := runStatuslineRenderCore(&out, root, strings.NewReader(tc.stdin), "manager", now, false); err != nil {
					t.Fatalf("render core must always return nil, got: %v", err)
				}
			})
			if stderr != "" {
				t.Errorf("render must not write to stderr; got %q", stderr)
			}
		})
	}

	t.Run("gate off renders nothing", func(t *testing.T) {
		root := setupConfigFactory(t) // no .statusline-gate written ⇒ off
		var out bytes.Buffer
		if err := runStatuslineRenderCore(&out, root, strings.NewReader(valid), "manager", now, false); err != nil {
			t.Fatalf("gate-off render must return nil, got: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("gate off must render nothing, got %q", out.String())
		}
	})
}

// TestStatuslineRender_AgentComesFromAFRoleAndReachesDisk pins the ADR-004 wiring for the
// occupancy channel's `agent` field (issue #596 K1), end to end: process env → cmd layer →
// WriteSnapshot → the snapshot file.
//
// It drives the RunE wrapper `runStatuslineRender`, NOT the core, because the wrapper is the only
// place AF_ROLE is read — every other test in this file calls the core with a literal agent, so
// deleting the os.Getenv line or forgetting to thread it leaves the whole suite green while every
// snapshot becomes unattributable and K2's per-agent map silently empties.
//
// The wrapper's TTY guard early-returns when os.Stdin is a character device, which under `go test`
// it is (/dev/null). Swapping in a pipe is what makes the wrapper reachable at all.
func TestStatuslineRender_AgentComesFromAFRoleAndReachesDisk(t *testing.T) {
	root := setupConfigFactory(t)
	if err := os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("enabling the gate: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved })
	if _, err := w.WriteString("{}"); err != nil {
		t.Fatalf("seeding stdin: %v", err)
	}
	w.Close()

	t.Setenv("AF_ROLE", "manager")

	payload := `{"session_id":"sessAF","model":{"display_name":"Opus 5"},` +
		`"context_window":{"total_input_tokens":10,"total_output_tokens":5,` +
		`"context_window_size":200000,"used_percentage":33}}`
	var out bytes.Buffer
	statuslineCmd.SetIn(strings.NewReader(payload))
	statuslineCmd.SetOut(&out)
	t.Cleanup(func() { statuslineCmd.SetOut(nil); statuslineCmd.SetIn(nil) })

	if err := runStatuslineRender(statuslineCmd); err != nil {
		t.Fatalf("render must never hard-error, got: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(config.StatuslineSessionsDir(root), "sessAF.json"))
	if err != nil {
		t.Fatalf("render wrote no snapshot at the sessions dir: %v", err)
	}
	var snap map[string]any
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v", err)
	}
	if got, _ := snap["agent"].(string); got != "manager" {
		t.Errorf("snapshot agent = %q, want %q from AF_ROLE — without it the occupancy reader "+
			"cannot attribute this session to any agent and the channel reads dark", got, "manager")
	}
	if got, _ := snap["context_used_pct"].(float64); got != 33 {
		t.Errorf("snapshot context_used_pct = %v, want 33", snap["context_used_pct"])
	}
}

// TestStatusSelfTest_ReportsFailure pins the render/status contrast: on a broken statusline.json
// render stays blank (exit 0) while status NAMES the failure in its self-test line.
func TestStatusSelfTest_ReportsFailure(t *testing.T) {
	root := setupConfigFactory(t)
	os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0o644)
	os.WriteFile(config.StatuslineConfigPath(root), []byte(`{"elements":["bogus-element"]}`), 0o644)

	// render: silent (blank + nil) on the broken config.
	var rout bytes.Buffer
	if err := runStatuslineRenderCore(&rout, root,
		strings.NewReader(`{"model":{"display_name":"X"}}`), "manager", time.Now(), false); err != nil {
		t.Fatalf("render must stay silent+nil on a broken config, got: %v", err)
	}
	if rout.Len() != 0 {
		t.Errorf("render must be blank on a broken config, got %q", rout.String())
	}

	// status: NAMES the failure (writes via fmt.Println ⇒ captureStdout).
	statusOut := captureStdout(t, func() {
		if err := runStatusline(statuslineCmd, []string{"status"}); err != nil {
			t.Fatalf("status must exit 0, got: %v", err)
		}
	})
	if !strings.Contains(statusOut, "bogus-element") && !strings.Contains(strings.ToLower(statusOut), "self-test: failed") {
		t.Errorf("status must NAME the broken pipeline in its self-test line; got %q", statusOut)
	}
}

// TestStatuslineStatus_FirstLineGrepContract asserts the stable grep contract: the FIRST
// status line is exactly "statusline: on" or "statusline: off".
func TestStatuslineStatus_FirstLineGrepContract(t *testing.T) {
	root := setupConfigFactory(t)
	os.WriteFile(statuslineGateFile(root), []byte("off\n"), 0o644)
	out := captureStdout(t, func() {
		if err := runStatusline(statuslineCmd, []string{"status"}); err != nil {
			t.Fatalf("status must exit 0, got: %v", err)
		}
	})
	first := strings.SplitN(out, "\n", 2)[0]
	if first != "statusline: off" && first != "statusline: on" {
		t.Errorf("first status line = %q, want exactly \"statusline: on\" or \"statusline: off\"", first)
	}
}

// TestStatuslineOnOff_WritesExactGateBytes proves on/off write the gate the exact bytes.
func TestStatuslineOnOff_WritesExactGateBytes(t *testing.T) {
	root := setupConfigFactory(t)

	if err := runStatusline(statuslineCmd, []string{"on"}); err != nil {
		t.Fatalf("on: %v", err)
	}
	if got, _ := os.ReadFile(statuslineGateFile(root)); string(got) != "on\n" {
		t.Errorf("gate after on = %q, want %q", got, "on\n")
	}
	if !statuslineFactoryEnabled(root) {
		t.Error("statuslineFactoryEnabled must be true after on")
	}

	if err := runStatusline(statuslineCmd, []string{"off"}); err != nil {
		t.Fatalf("off: %v", err)
	}
	if got, _ := os.ReadFile(statuslineGateFile(root)); string(got) != "off\n" {
		t.Errorf("gate after off = %q, want %q", got, "off\n")
	}
	if statuslineFactoryEnabled(root) {
		t.Error("statuslineFactoryEnabled must be false after off")
	}
}

// TestRender_RootMismatchDowngrades drives the FULL RunE so resolveInvokerRoot +
// downgradeRootMismatch run: a cwd/AF_ROOT mismatch must downgrade, never hard-error. This
// path DOES write a mismatch warning to os.Stderr, so it asserts only err==nil (NOT no-stderr).
func TestRender_RootMismatchDowngrades(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	os.WriteFile(filepath.Join(fx.clone, ".agentfactory", ".statusline-gate"), []byte("on\n"), 0o644)
	t.Chdir(fx.clone)
	t.Setenv("AF_ROOT", fx.outer)

	statuslineCmd.SetContext(t.Context())
	statuslineCmd.SetIn(strings.NewReader(`{"model":{"display_name":"X"}}`))
	var out bytes.Buffer
	statuslineCmd.SetOut(&out)
	t.Cleanup(func() { statuslineCmd.SetOut(nil); statuslineCmd.SetIn(nil) })

	_ = captureStderr(t, func() {
		if err := runStatusline(statuslineCmd, []string{"render"}); err != nil {
			t.Fatalf("render must downgrade a root mismatch, never hard-error; got: %v", err)
		}
	})
}

// TestRender_BranchFromPayloadDir pins AC-3(iii): the branch shown is the one being WORKED ON
// (the payload's working directory), not the factory root's — critical for worktree agents,
// whose resolveInvokerRoot resolves to the shared factory via .factory-root, not the worktree.
func TestRender_BranchFromPayloadDir(t *testing.T) {
	root := setupConfigFactory(t)
	os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0o644)

	// A repo dir with a known branch, distinct from the (non-repo) factory root. ReadBranch
	// parses .git/HEAD directly, so no `git init` is needed.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/af/agent-branch\n"), 0o644)

	payload := `{"session_id":"s","model":{"display_name":"M"},"workspace":{"project_dir":"` + repo + `"}}`
	var out bytes.Buffer
	if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", time.Now(), false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out.String(), "af/agent-branch") {
		t.Errorf("statusline must show the payload working dir's branch (af/agent-branch), got %q", out.String())
	}
}

// TestStatusline_RedirectPresenceOnly_ValueNeverEchoed pins SEC-5: the ANTHROPIC_BASE_URL
// value is read for PRESENCE only (to add a "~" cost estimate prefix) and is never echoed to
// stdout by any verb.
func TestStatusline_RedirectPresenceOnly_ValueNeverEchoed(t *testing.T) {
	root := setupConfigFactory(t)
	os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0o644)
	const secret = "https://secret.proxy.internal:9999/v1"
	t.Setenv("ANTHROPIC_BASE_URL", secret)

	statusOut := captureStdout(t, func() {
		if err := runStatusline(statuslineCmd, []string{"status"}); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if strings.Contains(statusOut, secret) || strings.Contains(statusOut, "secret.proxy.internal") {
		t.Errorf("SEC-5: status must never echo the ANTHROPIC_BASE_URL value; got %q", statusOut)
	}

	// redirect=true is the presence-derived bool; the "~" cost prefix appears while the value
	// itself is structurally unavailable to the renderer.
	var rout bytes.Buffer
	payload := `{"session_id":"s","context_window":{"total_input_tokens":100,"total_output_tokens":50},"cost":{"total_cost_usd":1.5}}`
	if err := runStatuslineRenderCore(&rout, root, strings.NewReader(payload), "manager", time.Now(), true); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(rout.String(), secret) {
		t.Errorf("SEC-5: render must never contain the base URL value; got %q", rout.String())
	}
	if rout.Len() > 0 && !strings.Contains(rout.String(), "~") {
		t.Errorf("redirect=true should apply the ~ cost prefix; got %q", rout.String())
	}
}

// TestStatusStalenessSkeleton_NamesAgentsMissingStatusLine exercises the K8 scan: an agent dir
// whose settings.json lacks the statusLine key is named stale; one that carries it is not.
func TestStatusStalenessSkeleton_NamesAgentsMissingStatusLine(t *testing.T) {
	root := setupConfigFactory(t)

	writeAgentSettings := func(name, settings string) {
		dir := filepath.Join(config.AgentDir(root, name), ".claude")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatalf("write settings for %s: %v", name, err)
		}
	}
	writeAgentSettings("stalebot", `{"hooks":{}}`)
	writeAgentSettings("healthy", `{"statusLine":{"type":"command","command":"af statusline render"}}`)

	note := statuslineStalenessNote(root)
	if !strings.Contains(note, "stalebot") {
		t.Errorf("staleness note must name the agent missing statusLine; got %q", note)
	}
	if strings.Contains(note, "healthy") {
		t.Errorf("staleness note must NOT name an agent that carries statusLine; got %q", note)
	}
}

// TestStatuslineGate_ToggleAndSeeding pins the K7 seed-if-absent contract (issue #591) and the
// pre-existing P3 on/off byte contract. It SIMULATES the install-time seed (mirroring
// TestFidelity_OnByDefaultAfterInstall, fidelity_test.go:64-81) rather than invoking
// runInstallInit, which would spawn the Python 3.12 MCP server and is not unit-tested. The
// load-bearing assertion is that a second --init must NOT clobber an operator's deliberate
// "off": seed-if-absent means the guard writes "on\n" only when the gate file is absent.
func TestStatuslineGate_ToggleAndSeeding(t *testing.T) {
	root := setupConfigFactory(t)
	gate := statuslineGateFile(root)

	// The exact seed-if-absent guard install.go runs at --init (install.go:279-284 idiom).
	seedIfAbsent := func() {
		if _, err := os.Stat(gate); os.IsNotExist(err) {
			if err := os.WriteFile(gate, []byte("on\n"), 0o644); err != nil {
				t.Fatalf("seed .statusline-gate: %v", err)
			}
		}
	}

	// First --init on a fresh factory: gate absent ⇒ seeded "on\n".
	seedIfAbsent()
	if got, _ := os.ReadFile(gate); string(got) != "on\n" {
		t.Fatalf("after first --init seed, gate = %q, want %q", got, "on\n")
	}

	// Operator deliberately turns it off (exercises the real P3 write path).
	if err := runStatusline(statuslineCmd, []string{"off"}); err != nil {
		t.Fatalf("statusline off: %v", err)
	}
	if got, _ := os.ReadFile(gate); string(got) != "off\n" {
		t.Fatalf("after off, gate = %q, want %q", got, "off\n")
	}

	// Second --init: gate present ⇒ seed-if-absent leaves the operator's "off\n" untouched.
	seedIfAbsent()
	if got, _ := os.ReadFile(gate); string(got) != "off\n" {
		t.Errorf("second --init clobbered the operator's off; gate = %q, want %q", got, "off\n")
	}

	// on/off write the gate exactly (pre-existing P3 behavior, asserted here alongside seeding).
	if err := runStatusline(statuslineCmd, []string{"on"}); err != nil {
		t.Fatalf("statusline on: %v", err)
	}
	if got, _ := os.ReadFile(gate); string(got) != "on\n" {
		t.Errorf("after on, gate = %q, want %q", got, "on\n")
	}
	if err := runStatusline(statuslineCmd, []string{"off"}); err != nil {
		t.Fatalf("statusline off: %v", err)
	}
	if got, _ := os.ReadFile(gate); string(got) != "off\n" {
		t.Errorf("after off, gate = %q, want %q", got, "off\n")
	}
}
