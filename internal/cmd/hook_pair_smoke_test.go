//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookPair_SequentialSmoke catches lock/env/stdin/trap collisions
// between quality-gate.sh and fidelity-gate.sh by piping a synthetic
// Stop-hook JSON payload into both scripts back-to-back in BOTH orders.
// Both scripts must exit 0 and emit `{"ok": true}` via the silent-exit
// path (`af root` returns empty when cwd is not a factory, so the scripts
// exit early before touching jq, claude, locks, or traps).
//
// This test does NOT prove Claude Code multi-sibling fan-out — that's
// Phase 3b's manual check (AC3.11). It resolves the sequential-execution
// slice of R-INT-10 (Q1 in the design doc) at `make test` time instead
// of post-merge.
//
// Deliberately narrow: it drives the silent-exit path only. The test runs with cwd = t.TempDir()
// so `af root` walks up to an ancestor that has no .agentfactory directory and returns empty,
// guaranteeing EXIT1 before jq, claude, locks or traps are reached — which is what makes it a
// collision check rather than a behavior check. The transcript-driven coverage lives in
// TestHookPair_EvidenceParityOverRealTranscript and TestHookPair_FailurePathsNeverBlock below;
// those run the whole path behind PATH shims and require jq, which CI now provisions
// (.github/workflows/test.yml, unit job).
func TestHookPair_SequentialSmoke(t *testing.T) {
	repoRoot := findRepoRoot(t)
	qualityGate := filepath.Join(repoRoot, "hooks", "quality-gate.sh")
	fidelityGate := filepath.Join(repoRoot, "hooks", "fidelity-gate.sh")

	// Minimal Stop-hook JSON payload. All fields the scripts read
	// (stop_hook_active, last_assistant_message, transcript_path) are
	// present so a parser that reaches them will not crash.
	payload := []byte(`{"stop_hook_active": false, "last_assistant_message": "test", "transcript_path": ""}`)

	tmpDir := t.TempDir()

	// Pair 1: quality then fidelity
	runHookSmoke(t, qualityGate, payload, tmpDir)
	runHookSmoke(t, fidelityGate, payload, tmpDir)

	// Pair 2: fidelity then quality (catches lock-order races and
	// trap/cleanup ordering issues)
	runHookSmoke(t, fidelityGate, payload, tmpDir)
	runHookSmoke(t, qualityGate, payload, tmpDir)
}

// hookE2EParityTranscript is the fixture both gates are graded over. Its first turn uses a tool no
// other turn uses, so an evidence block that names Grep would be reporting the PREVIOUS turn —
// the AC-1 over-report Design 562 exists to remove — and the assertion below would catch it.
//
// AUTHORED, not captured (see hookE2EWriteTranscript for why that distinction is written down).
func hookE2EParityTranscript(t *testing.T, dir string) string {
	t.Helper()
	return hookE2EWriteTranscript(t, dir, "recorded_turn.jsonl",
		turnPrompt("u0", "an earlier prompt"),
		turnCall("a0", "m0", "t0", "Grep", `{"pattern":"previous turn only"}`),
		turnResult("r0", "t0", "previous turn output", false),
		turnPrompt("u1", "do the thing"),
		turnCall("a1", "m1", "t1", "Bash", `{"command":"af mail inbox"}`),
		turnResult("r1", "t1", "no new mail", false),
		turnCall("a2", "m2", "t2", "Read", `{"file_path":"/x/y.md"}`),
		turnResult("r2", "t2", "file contents", false),
	)
}

// TestHookPair_EvidenceParityOverRealTranscript is AC-8 (design-doc.md:283): both gates must hand
// the judge the identical evidence block for the identical transcript. It is also the first test in
// this repo to drive a non-empty transcript_path through a gate at all — every prior hook assertion
// is a text pin over the script source, or a smoke run that exits at the first branch.
//
// SCOPE (H-1, design-doc.md:148): the judge here is a PATH-shim stub. This proves PLUMBING —
// extractor -> evidence block -> judge input. It proves nothing about judge behavior, which is
// covered only by Phase 6's live-judge validation gate (.designs/562/live-judge-validation.md).
func TestHookPair_EvidenceParityOverRealTranscript(t *testing.T) {
	rig := newHookE2ERig(t)
	workDir := setupGateLockTestEnv(t)
	hookE2ESetStep(t, workDir, "bd-p7-parity", "Drive a recorded turn through both gates")

	transcript := hookE2EParityTranscript(t, t.TempDir())
	payload := hookE2EPayload(t, "Checked the mailbox and read the file.", transcript)

	blocks := map[string]string{}
	for _, gate := range hookE2EGates() {
		out, exitCode := rig.run(t, gate, workDir, payload, "")
		if exitCode != 0 {
			t.Fatalf("%s: exit %d, want 0\noutput: %s", gate.script, exitCode, out)
		}
		if !strings.Contains(out, `{"ok": true}`) {
			t.Fatalf("%s did not emit `{\"ok\": true}`:\n%s", gate.script, out)
		}
		if log := hookE2EDebugLog(t, workDir, gate); !strings.Contains(log, gate.completionLabel) {
			t.Fatalf("%s: debug log missing %q — the run did not reach the end:\n%s", gate.script, gate.completionLabel, log)
		}

		block := hookE2EEvidenceBlock(t, hookE2EJudgeInput(t, workDir, gate.name), gate.name)
		hookE2ERequireEvidence(t, block, gate.name)
		blocks[gate.name] = block
	}

	if blocks["fidelity"] != blocks["quality"] {
		t.Errorf("AC-8 parity: the gates handed the judge different evidence\n--- fidelity ---\n%s\n--- quality ---\n%s",
			blocks["fidelity"], blocks["quality"])
	}

	block := blocks["fidelity"]
	bash := strings.Index(block, `1. Bash(command="af mail inbox")`)
	read := strings.Index(block, `2. Read(file_path="/x/y.md")`)
	if bash < 0 || read < 0 || bash > read {
		t.Errorf("evidence block does not carry the turn's calls oldest-first:\n%s", block)
	}
	if strings.Contains(block, "Grep") {
		t.Errorf("evidence block leaked a call from the PREVIOUS turn (AC-1 over-report):\n%s", block)
	}
}

// TestHookPair_FailurePathsNeverBlock is AC-10 (design-doc.md:284) and ADR-007's never-block rule:
// no gate-infrastructure failure may stop an agent. Every case asserts exit 0, `{"ok": true}` AND
// the debug-log label, so a pass is attributable to the branch under test rather than to some other
// early exit that happens to be quiet.
func TestHookPair_FailurePathsNeverBlock(t *testing.T) {
	rig := newHookE2ERig(t)

	cases := []struct {
		name    string
		missing string
		// label resolves per gate because the two scripts number no_claude_binary differently.
		label func(hookE2EGate) string
		// transcript returns the path the Stop payload advertises.
		transcript func(t *testing.T, dir string) string
	}{
		{
			name:  "transcript path does not exist",
			label: func(g hookE2EGate) string { return g.completionLabel },
			transcript: func(t *testing.T, dir string) string {
				return filepath.Join(dir, "no_such_session.jsonl")
			},
		},
		{
			name:  "transcript is malformed JSONL",
			label: func(g hookE2EGate) string { return g.completionLabel },
			transcript: func(t *testing.T, dir string) string {
				return hookE2EWriteTranscript(t, dir, "malformed.jsonl",
					`{"type":"user","uuid":`, `not json at all`, `{"type":`)
			},
		},
		{
			name:    "af is absent from PATH",
			missing: "af",
			label:   func(hookE2EGate) string { return "EXIT6: no_af_binary" },
			transcript: func(t *testing.T, dir string) string {
				return hookE2EParityTranscript(t, dir)
			},
		},
		{
			name:    "claude is absent from PATH",
			missing: "claude",
			label:   func(g hookE2EGate) string { return g.noClaudeLabel },
			transcript: func(t *testing.T, dir string) string {
				return hookE2EParityTranscript(t, dir)
			},
		},
	}

	for _, tc := range cases {
		for _, gate := range hookE2EGates() {
			t.Run(tc.name+"/"+gate.name, func(t *testing.T) {
				workDir := setupGateLockTestEnv(t)
				hookE2ESetStep(t, workDir, "bd-p7-failopen", "Fail open, never block")

				payload := hookE2EPayload(t, "a response the gate must not block", tc.transcript(t, t.TempDir()))
				out, exitCode := rig.run(t, gate, workDir, payload, tc.missing)

				if exitCode != 0 {
					t.Fatalf("exit %d, want 0 (ADR-007: a gate never blocks)\noutput: %s", exitCode, out)
				}
				if !strings.Contains(out, `{"ok": true}`) {
					t.Fatalf("output does not carry `{\"ok\": true}`:\n%s", out)
				}
				if log := hookE2EDebugLog(t, workDir, gate); !strings.Contains(log, tc.label(gate)) {
					t.Fatalf("debug log missing %q — the run exited somewhere else:\n%s", tc.label(gate), log)
				}
			})
		}
	}

	// A transcript that is present but unreadable is the one degraded state the gate cannot tell
	// apart from a genuinely tool-free turn, so the fidelity gate says so once
	// (fidelity-gate.sh:199-203). An absent transcript is NOT that state and must stay silent.
	t.Run("only a present-but-unreadable transcript raises the extraction notice", func(t *testing.T) {
		gate := hookE2EGates()[0]

		malformed := setupGateLockTestEnv(t)
		hookE2ESetStep(t, malformed, "bd-p7-notice", "Notice once, not every turn")
		rig.run(t, gate, malformed, hookE2EPayload(t, "response",
			hookE2EWriteTranscript(t, t.TempDir(), "malformed.jsonl", `{"type":`)), "")
		if !hookE2EMarkerExists(malformed, "grader_notice_extraction_unavailable") {
			t.Errorf("a present-but-unreadable transcript did not raise the extraction notice")
		}

		absent := setupGateLockTestEnv(t)
		hookE2ESetStep(t, absent, "bd-p7-notice", "Notice once, not every turn")
		rig.run(t, gate, absent, hookE2EPayload(t, "response",
			filepath.Join(t.TempDir(), "gone.jsonl")), "")
		if hookE2EMarkerExists(absent, "grader_notice_extraction_unavailable") {
			t.Errorf("an absent transcript raised the extraction notice; it is reserved for a transcript that exists")
		}
	})
}

// gateHookFiles are the four copies of the two Stop-hook gate scripts: the two
// top-level runtime copies and their two embedded install_hooks twins. All four
// must carry the identical grader construction (TestInstallHooks_NoDrift enforces
// byte-identity per pair; the tests below enforce the intended content).
func gateHookFiles(repoRoot string) []string {
	return []string{
		filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"),
		filepath.Join(repoRoot, "hooks", "quality-gate.sh"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "fidelity-gate.sh"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "quality-gate.sh"),
	}
}

// graderEnvBlock returns the grader's env-construction block: the lines from the
// `env -i` allowlist opener through the line that invokes `claude -p --model
// haiku`. Phase 4b spreads the allowlist across several ${VAR:+...} OTel-forwarding
// continuation lines, so prefix and decoy-var assertions must scan this whole
// block rather than the single line that names claude.
func graderEnvBlock(t *testing.T, content, file string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, "env -i HOME=") {
			start = i
			break
		}
	}
	if start == -1 {
		t.Fatalf("%s: no grader env block (`env -i HOME=`) found", file)
	}
	for i := start; i < len(lines); i++ {
		if strings.Contains(lines[i], "claude -p --model haiku") {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("%s: grader env block opened but no `claude -p --model haiku` terminator found", file)
	return ""
}

// TestHookGrader_UsesAllowlistNotDenylist pins Issue #508 W10 (AC-1): each hook's
// haiku grader is built from an ALLOWLIST (`env -i HOME=… PATH=…`) rather than the
// old six-var `env -u` DENYLIST. The allowlist is structurally immune to every
// current and future redirect var a per-agent local-model profile may inject —
// only HOME (for ~/.claude creds) and PATH (to find the binary) survive into the
// grader's environment, so it always reaches the ambient Anthropic endpoint.
//
// Asserting the allowlist form across all four copies also locks the source and
// embedded mirrors together at the grader line; TestInstallHooks_NoDrift enforces
// byte-identity, this test enforces the intended content.
func TestHookGrader_UsesAllowlistNotDenylist(t *testing.T) {
	repoRoot := findRepoRoot(t)

	// The allowlist base — env -i carrying ONLY HOME and PATH — opens the grader
	// invocation. Phase 4b inserts ${VAR:+...} OTel-forwarding tokens between this
	// base and `claude`, so the base and the grader command are asserted separately
	// rather than as one contiguous substring (which the inserted tokens destroy).
	const allowlistBase = `env -i HOME="$HOME" PATH="$PATH"`
	const graderCmd = `claude -p --model haiku`

	for _, f := range gateHookFiles(repoRoot) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		content := string(data)

		if !strings.Contains(content, allowlistBase) {
			t.Errorf("%s: grader is not allowlist-constructed; expected the env -i HOME/PATH allowlist base:\n  %s", f, allowlistBase)
		}
		if !strings.Contains(content, graderCmd) {
			t.Errorf("%s: grader invocation %q not found", f, graderCmd)
		}

		// The env -u denylist must be gone entirely (it is drift-prone: it omits at
		// least ANTHROPIC_MODEL/SMALL_FAST_MODEL/DEFAULT_HAIKU_MODEL/SUBAGENT_MODEL).
		if strings.Contains(content, "env -u ") {
			t.Errorf("%s: an env -u denylist remains — W10 replaces it with the env -i allowlist", f)
		}

		// No bare (unwrapped) grader invocation.
		if strings.Contains(content, "$(claude -p --model haiku") {
			t.Errorf("%s: bare grader invocation remains (must be env -i wrapped)", f)
		}
	}
}

// TestHookGrader_DecoyVarsNeutralizedByAllowlist is the HARD decoy-var test (AC-3).
// A decoy profile may set EVERY known redirect/model-shaping var; under `env -i`
// none of them can reach the grader's environment because only HOME and PATH are
// passed through. This asserts structurally on the constructed grader command form
// — no real network and no real claude binary are needed. It is the drift-proof
// property the allowlist inversion buys over extending the denylist.
func TestHookGrader_DecoyVarsNeutralizedByAllowlist(t *testing.T) {
	repoRoot := findRepoRoot(t)

	decoyVars := []string{
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"CLAUDE_CODE_EFFORT_LEVEL",
	}

	for _, f := range gateHookFiles(repoRoot) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		block := graderEnvBlock(t, string(data), f)

		// The grader's env opens with an allowlist carrying ONLY HOME and PATH; Phase 4b
		// appends ${VAR:+...} OTel-forwarding tokens after this base, so the assertion
		// pins the opener rather than a whole-line prefix through `claude`.
		if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(block), "VERDICT=$(")), `env -i HOME="$HOME" PATH="$PATH"`) {
			t.Errorf("%s: grader env does not open with the env -i HOME/PATH allowlist base:\n  %s", f, block)
		}

		// No decoy var name may appear anywhere in the constructed env block — under
		// env -i the allowlist cannot enumerate any of them, so a decoy profile setting
		// all of them is inert. Scanning the whole block (not just the line naming
		// claude) is a strictly stronger guarantee than the pre-Phase-4b check.
		for _, v := range decoyVars {
			if strings.Contains(block, v) {
				t.Errorf("%s: decoy var %q appears in the grader env block — it could reach the grader env:\n  %s", f, v, block)
			}
		}
	}
}

func runHookSmoke(t *testing.T, scriptPath string, payload []byte, workDir string) {
	t.Helper()
	cmd := exec.Command("bash", scriptPath)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s exit non-zero: %v\noutput: %s", scriptPath, err, out)
	}
	if !bytes.Contains(out, []byte(`{"ok": true}`)) {
		t.Fatalf("%s did not emit `{\"ok\": true}`:\n%s", scriptPath, out)
	}
}

// findRepoRoot walks up from the test binary's working directory until
// it finds go.mod. Avoids hard-coded relative paths that break under
// different `go test` invocations.
//
// NOTE: an identical helper exists at internal/cmd/integration_test.go:15
// but that file is gated by //go:build integration, so its symbols are
// not visible in the unit build. Phase 3's install_hooks_drift_test.go
// (when it lands) is also in the unit build and should REUSE this
// helper rather than redefining it — both tests live in package cmd.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod walking up from test cwd")
		}
		dir = parent
	}
}
