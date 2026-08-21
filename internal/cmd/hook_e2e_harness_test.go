//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// PATH-shim harness for driving a real Stop-hook payload through the real gate scripts.
//
// SCOPE (cross-review H-1, design-doc.md:148). The judge behind this harness is a PATH-shim STUB.
// Everything built here proves PLUMBING — extractor -> evidence block -> judge input -> counter ->
// run record -> mail. It proves NOTHING about judge behavior; judge behavior against the rewritten
// prompts is proven only by Phase 6's live-judge validation gate
// (.designs/562/live-judge-validation.md).
//
// Three constraints shape the design, each of which was measured rather than assumed, and each of
// which turns the harness VACUOUSLY GREEN if ignored:
//
//   - `af` may not be a real binary here. storeGuardActive = isTestBinary()
//     (internal/cmd/storeguard_default.go) keys on the process name, so the in-memory store guard
//     does not survive an exec into a compiled `af`: `af mail send` from a test would reach the
//     operator's live issue store, which ADR-018 forbids outright. `af turn evidence` is the sole
//     exception — it constructs no store (turn.go:97-114) — and it is also the one call whose REAL
//     implementation this harness exists to exercise, so the shim delegates that verb and only that
//     verb to a freshly built binary.
//   - /tmp is mounted noexec on some machines, including CI-adjacent containers. A shim planted in
//     a bare t.TempDir() then fails to exec and `command -v af` silently resolves the operator's
//     INSTALLED af instead, which predates `af turn evidence`. hookE2EExecDir probes for an
//     exec-capable directory and fails loudly rather than letting that happen.
//   - The grader runs under `env -i HOME=... PATH=...` (fidelity-gate.sh:244, quality-gate.sh:135),
//     so no custom variable reaches the `claude` stub. It keys off its inherited working directory
//     instead, which is the agent workdir the gate itself uses for .runtime/.
const (
	hookE2ERole      = "gate-e2e-agent"
	hookE2EExecProbe = "hooke2e-exec-ok"
)

type hookE2EGate struct {
	name     string
	script   string
	debugLog string
	// The two gates number the same two exit labels differently (fidelity-gate.sh:211,374 vs
	// quality-gate.sh:115,163), which is precisely the kind of divergence a shared assertion table
	// would paper over.
	noClaudeLabel   string
	completionLabel string
}

func hookE2EGates() []hookE2EGate {
	return []hookE2EGate{
		{
			name: "fidelity", script: "fidelity-gate.sh", debugLog: "fidelity_debug.log",
			noClaudeLabel: "EXIT8: no_claude_binary", completionLabel: "EXIT9: normal_completion",
		},
		{
			name: "quality", script: "quality-gate.sh", debugLog: "quality_debug.log",
			noClaudeLabel: "EXIT7: no_claude_binary", completionLabel: "EXIT8: normal_completion",
		},
	}
}

// hookE2ERig owns everything that must live on an exec-capable filesystem. One rig serves many
// workdirs: the shims resolve all of their per-run state through $(pwd), so they carry no workdir
// of their own.
type hookE2ERig struct {
	repoRoot     string
	shimBoth     string
	shimNoAF     string
	shimNoClaude string
}

// requireHookE2ETools is the honest skip. Both gate scripts drive every branch through jq — the
// recursion guard, the message extraction, the lock PID read, the evidence JSON and the run record
// — so on a machine without it a transcript-driven test exits at EXIT5: no_message while still
// reporting green. CI provisions jq for exactly this reason (.github/workflows/test.yml, unit job).
func requireHookE2ETools(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		hookE2ESkipMissing(t, "bash")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		hookE2ESkipMissing(t, "jq")
	}
}

func hookE2ESkipMissing(t *testing.T, tool string) {
	t.Helper()
	t.Skip(tool + " not on PATH; skipping transcript-driven gate e2e (CI provisions jq — see .github/workflows/test.yml)")
}

func newHookE2ERig(t *testing.T) *hookE2ERig {
	t.Helper()
	requireHookE2ETools(t)

	repoRoot := findRepoRoot(t)
	execDir := hookE2EExecDir(t)
	afReal := hookE2EBuildAF(t, repoRoot, execDir)

	rig := &hookE2ERig{
		repoRoot:     repoRoot,
		shimBoth:     hookE2EPlantShims(t, execDir, "shim-both", afReal, true, true),
		shimNoAF:     hookE2EPlantShims(t, execDir, "shim-no-af", afReal, false, true),
		shimNoClaude: hookE2EPlantShims(t, execDir, "shim-no-claude", afReal, true, false),
	}
	return rig
}

func (r *hookE2ERig) scriptPath(g hookE2EGate) string {
	return filepath.Join(r.repoRoot, "hooks", g.script)
}

// run pipes payload into one gate script with the shim directory first on PATH.
//
// missing selects a failure-path fixture: "af" and "claude" drop the named binary from BOTH the
// shim directory and the ambient PATH. They do not empty PATH — the scripts need jq, date, stat,
// cat, head, wc, tr, sed, mkdir, rm and printf to reach the branch under test at all, so an empty
// PATH would test something else entirely.
func (r *hookE2ERig) run(t *testing.T, g hookE2EGate, workDir string, payload []byte, missing string) (string, int) {
	t.Helper()

	shim, ambient := r.shimBoth, os.Getenv("PATH")
	switch missing {
	case "":
	case "af":
		shim, ambient = r.shimNoAF, hookE2EPathWithout(t, "af")
	case "claude":
		shim, ambient = r.shimNoClaude, hookE2EPathWithout(t, "claude")
	default:
		t.Fatalf("unknown missing-binary fixture %q", missing)
	}

	// The judge stub runs under `env -i` and cannot be told anything through the environment. It
	// used to infer the calling gate from wording inside the system prompt, which made a prompt
	// REWORDING — the exact edit Phase 6 makes — surface as "the gate never reached the judge",
	// a false report. The harness knows which script it is launching, so it says so here.
	hookE2EWriteRuntime(t, workDir, "expected_gate", g.name+"\n")

	cmd := exec.Command("bash", r.scriptPath(g))
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Dir = workDir
	cmd.Env = hookE2EEnv(workDir, shim+string(os.PathListSeparator)+ambient)

	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s: %v\noutput: %s", g.script, err, out)
		}
		exitCode = exitErr.ExitCode()
	}
	return string(out), exitCode
}

// hookE2EEnv replaces rather than appends the three variables the gates read, because os.Environ()
// may already carry a PATH and glibc resolves the FIRST match.
//
// Passing the rest of os.Environ() through is safe only because this package's TestMain calls
// tmuxisolation.NeutralizeAFEnv (main_test.go:16), which wipes the whole AF_* family before any
// test runs — so an operator's AF_ROOT or AF_ROLE cannot leak into a gate here. That invariant
// lives in another package and is pinned by TestEnvHermetic (env_hermetic_test.go:91-120); without
// it this function would have to build its environment from scratch.
func hookE2EEnv(workDir, path string) []string {
	env := []string{"AF_ROOT=" + workDir, "AF_ROLE=" + hookE2ERole, "PATH=" + path}
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "PATH="), strings.HasPrefix(kv, "AF_ROOT="), strings.HasPrefix(kv, "AF_ROLE="):
			continue
		}
		env = append(env, kv)
	}
	return env
}

// hookE2EPathWithout returns PATH with the named executables removed and NOTHING ELSE removed.
//
// Dropping the whole directory is the obvious implementation and it is wrong: `af` is routinely
// installed beside coreutils (~/.local/bin, /usr/local/bin), so dropping its directory can take
// `jq`, `cat` or `date` with it and the gate then dies at a branch nowhere near the one under test.
// A directory that holds a named binary is replaced by a mirror of itself — symlinks to every OTHER
// entry, in the same position in the list, so precedence is preserved.
//
// The mirror lives in a plain t.TempDir(): the symlinks are not what gets executed, their targets
// are, so a noexec temp mount is irrelevant here (unlike the shim directory, which really does need
// to exec).
func hookE2EPathWithout(t *testing.T, names ...string) string {
	t.Helper()

	excluded := map[string]bool{}
	for _, name := range names {
		excluded[name] = true
	}

	var out []string
	mirrors := ""
	for i, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		holdsExcluded := false
		for name := range excluded {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
				holdsExcluded = true
			}
		}
		if !holdsExcluded {
			out = append(out, dir)
			continue
		}
		if mirrors == "" {
			mirrors = t.TempDir()
		}
		out = append(out, hookE2EMirrorDirExcept(t, mirrors, fmt.Sprintf("mirror%d", i), dir, excluded))
	}
	return strings.Join(out, string(os.PathListSeparator))
}

func hookE2EMirrorDirExcept(t *testing.T, base, name, src string, excluded map[string]bool) string {
	t.Helper()
	dst := filepath.Join(base, name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir PATH mirror: %v", err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read PATH dir %s: %v", src, err)
	}
	for _, entry := range entries {
		if excluded[entry.Name()] {
			continue
		}
		if err := os.Symlink(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil && !os.IsExist(err) {
			t.Fatalf("mirror %s into %s: %v", entry.Name(), dst, err)
		}
	}
	return dst
}

// hookE2EExecDir returns a directory proven to permit fork/exec, by writing a script there and
// running it.
//
// Deliberately fatal, not a skip. A shim that cannot execute does not make the gate fail — it makes
// `command -v af` resolve the operator's installed binary, so the suite would keep reporting green
// while testing a completely different program.
func hookE2EExecDir(t *testing.T) string {
	t.Helper()
	dir, err := hookE2ETryExecDir(t)
	if err != nil {
		t.Fatalf("no exec-capable directory for the gate shims (is /tmp mounted noexec?): %v", err)
	}
	return dir
}

// hookE2ETryExecDir is the non-fatal form, for callers that only want to HARDEN an existing test
// rather than depend on the shims.
//
// The repo tree is the LAST candidate, so `make test` — which redirects both TMPDIR and GOTMPDIR to
// $HOME/.cache/af-test (Makefile:55-59) — never reaches it. A bare `go test` on a machine with a
// noexec /tmp does, and then builds the af binary under internal/cmd/testdata/hooke2e*; that path is
// gitignored and removed by the Cleanup below, so it only outlives a hard kill.
func hookE2ETryExecDir(t *testing.T) (string, error) {
	t.Helper()

	var tried []string
	candidates := []string{
		os.Getenv("GOTMPDIR"),
		os.Getenv("TMPDIR"),
		filepath.Join(findRepoRoot(t), "internal", "cmd", "testdata"),
	}
	for _, base := range candidates {
		if base == "" {
			continue
		}
		if err := os.MkdirAll(base, 0o755); err != nil {
			tried = append(tried, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		dir, err := os.MkdirTemp(base, "hooke2e")
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		if err := hookE2EProbeExec(dir); err != nil {
			_ = os.RemoveAll(dir)
			tried = append(tried, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir, nil
	}
	return "", fmt.Errorf("every candidate rejected: %s", strings.Join(tried, "; "))
}

// hookE2EHermeticShimDir plants the shim pair WITHOUT building af, for callers whose subject is a
// branch the gate reaches before the evidence call.
//
// It exists because the pre-existing lock tests run both gate scripts with the ambient PATH, and
// quality-gate.sh has no step-ready guard in front of its grader: on any developer machine with the
// `claude` CLI installed those tests reach the real haiku judge over the network (measured at ~18 s,
// roughly half of this package's runtime), and a verdict of ok:false would then drive a real
// `af mail send` into the operator's issue store — the outcome ADR-018 forbids outright. Shimming
// both binaries closes that path.
//
// It returns "" rather than failing when no exec-capable directory exists, because a machine
// without one is no worse off than it is today.
func hookE2EHermeticShimDir(t *testing.T) string {
	t.Helper()
	dir, err := hookE2ETryExecDir(t)
	if err != nil {
		t.Logf("gate hooks will run against the ambient PATH: %v", err)
		return ""
	}
	return hookE2EPlantShims(t, dir, "shim-hermetic", filepath.Join(dir, "af-real-absent"), true, true)
}

func hookE2EProbeExec(dir string) error {
	probe := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/bash\necho "+hookE2EExecProbe+"\n"), 0o755); err != nil {
		return err
	}
	out, err := exec.Command(probe).CombinedOutput()
	if err != nil {
		return fmt.Errorf("exec probe failed: %v (%s)", err, bytes.TrimSpace(out))
	}
	if !bytes.Contains(out, []byte(hookE2EExecProbe)) {
		return fmt.Errorf("exec probe produced %q", out)
	}
	return nil
}

func hookE2EBuildAF(t *testing.T, repoRoot, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, "af-real")
	build := exec.Command("go", "build", "-o", binary, "./cmd/af")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building af for the `turn evidence` delegation: %v\n%s", err, out)
	}
	return binary
}

func hookE2EPlantShims(t *testing.T, execDir, name, afReal string, withAF, withClaude bool) string {
	t.Helper()
	dir := filepath.Join(execDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir shim dir: %v", err)
	}
	if withAF {
		hookE2EWriteScript(t, filepath.Join(dir, "af"),
			strings.ReplaceAll(hookE2EAfShim, "__AF_REAL__", afReal))
	}
	if withClaude {
		hookE2EWriteScript(t, filepath.Join(dir, "claude"), hookE2EClaudeShim)
	}
	return dir
}

func hookE2EWriteScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// hookE2EAfShim answers every `af` verb the gates call. Only `turn evidence` reaches real code;
// the rest are recorded and faked, because a real `af` in this tier would construct the operator's
// issue store (ADR-018).
//
// The ledger is the mechanism behind two assertions that have no other observable: "a compliant run
// sends no STEP_FIDELITY mail" and "a verdict backlog collapses to one bead". Arguments are escaped
// onto one tab-separated line each, because a verdict body legitimately contains newlines.
//
// The delivery lines are the ones Phase 4 froze (internal/cmd/mail.go:184, :195-204). A shim that
// printed anything else would silently push the escalation down the "Supervisor copy filed" branch
// (fidelity-gate.sh:319-329) and the test would be asserting the wrong path.
const hookE2EAfShim = `#!/bin/bash
RUNTIME="$(pwd)/.runtime"
LEDGER="$RUNTIME/af_calls.log"
INBOX="$RUNTIME/stub_inbox.json"
STEP="$RUNTIME/stub_step.json"
AF_REAL="__AF_REAL__"

mkdir -p "$RUNTIME" 2>/dev/null

log_call() {
    local line="" arg
    for arg in "$@"; do
        arg=${arg//$'\n'/\\n}
        arg=${arg//$'\t'/ }
        line="$line$arg"$'\t'
    done
    printf '%s\n' "$line" >> "$LEDGER"
}
log_call "$@"

if [ "$1" = "turn" ] && [ "$2" = "evidence" ]; then
    shift 2
    # An absent af-real is the deliberately degraded mode the lock tests run in: they only need the
    # gate to stop calling the network, not to derive evidence. Every test that DOES care asserts
    # the evidence block is non-empty, so this can never pass for a broken build.
    if [ -x "$AF_REAL" ]; then
        exec "$AF_REAL" turn evidence "$@"
    fi
    exit 0
fi

case "$1" in
root)
    printf '%s\n' "$AF_ROOT"
    ;;
step)
    [ -f "$STEP" ] && cat "$STEP"
    ;;
mail)
    verb="$2"
    shift 2
    case "$verb" in
    inbox)
        if [ -f "$INBOX" ]; then cat "$INBOX"; else printf '[]\n'; fi
        ;;
    delete)
        if [ -f "$INBOX" ]; then
            jq --arg id "$1" 'map(select(.id != $id))' "$INBOX" > "$INBOX.tmp" && mv "$INBOX.tmp" "$INBOX"
        fi
        ;;
    send)
        to="$1"
        shift
        subject=""
        body=""
        report="false"
        while [ $# -gt 0 ]; do
            case "$1" in
            -s) subject="$2"; shift 2 ;;
            -m) body="$2"; shift 2 ;;
            --priority) shift 2 ;;
            --report-delivery) report="true"; shift ;;
            *) shift ;;
            esac
        done
        if [ -f "$INBOX" ]; then
            jq --arg id "stub-$$-$RANDOM" --arg to "$to" --arg s "$subject" --arg b "$body" \
                '. + [{id: $id, from: "gate", to: $to, subject: $s, body: $b, read: false}]' \
                "$INBOX" > "$INBOX.tmp" && mv "$INBOX.tmp" "$INBOX"
        fi
        if [ "$report" = "true" ]; then
            printf 'Notified %s: %s\n' "$to" "$subject"
        else
            printf 'Sent to %s: %s\n' "$to" "$subject"
        fi
        ;;
    esac
    ;;
esac
exit 0
`

// hookE2EClaudeShim stands in for the haiku judge. `env -i` strips every variable the test could
// use to configure it, so it reaches all of its per-run state — the gate name the harness declared
// and the verdict it must return — through the working directory it inherited.
const hookE2EClaudeShim = `#!/bin/bash
RUNTIME="$(pwd)/.runtime"
mkdir -p "$RUNTIME" 2>/dev/null

gate=$(cat "$RUNTIME/expected_gate" 2>/dev/null | tr -d '[:space:]')
[ -n "$gate" ] || gate="unknown"

printf '%s' "${@: -1}" > "$RUNTIME/judge_input_$gate.txt"

if [ -f "$RUNTIME/stub_verdict" ]; then
    cat "$RUNTIME/stub_verdict"
else
    printf '{"ok": true}\n'
fi
exit 0
`

// ---------------------------------------------------------------------------
// Per-run state the shims read, and the artifacts the gates leave behind.
// ---------------------------------------------------------------------------

func hookE2ERuntimePath(workDir string, name string) string {
	return filepath.Join(workDir, ".runtime", name)
}

// hookE2ESetStep rewrites what `af step current --json` answers. A multi-step run must actually
// change this between turns: the counter and the escalation latch are cleared on a step change
// (fidelity-gate.sh:167-173), so a run that reuses one id tests one step three times.
func hookE2ESetStep(t *testing.T, workDir, stepID, title string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"state": "ready", "id": stepID, "title": title,
		"description": "Execute step " + stepID + " exactly as written.",
		"is_gate":     false, "gate_id": "", "formula": "hook-e2e",
	})
	if err != nil {
		t.Fatalf("marshal step json: %v", err)
	}
	hookE2EWriteRuntime(t, workDir, "stub_step.json", string(payload)+"\n")
}

// hookE2ESetVerdict fixes what the judge stub returns next. A verdict that is neither ok:true nor
// ok:false is the third state the gate distinguishes (fidelity-gate.sh:272-279): non-empty so the
// grader-unavailable notice does not fire, unparseable so the counter must be left alone.
func hookE2ESetVerdict(t *testing.T, workDir, verdict string) {
	t.Helper()
	hookE2EWriteRuntime(t, workDir, "stub_verdict", verdict+"\n")
}

func hookE2ESetInbox(t *testing.T, workDir string, messages []map[string]any) {
	t.Helper()
	payload, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		t.Fatalf("marshal inbox: %v", err)
	}
	hookE2EWriteRuntime(t, workDir, "stub_inbox.json", string(payload)+"\n")
}

func hookE2EReadInbox(t *testing.T, workDir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(hookE2ERuntimePath(workDir, "stub_inbox.json"))
	if err != nil {
		t.Fatalf("read stub inbox: %v", err)
	}
	var messages []map[string]any
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatalf("stub inbox does not parse: %v\n%s", err, data)
	}
	return messages
}

func hookE2EWriteRuntime(t *testing.T, workDir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(workDir, ".runtime"), 0o755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}
	if err := os.WriteFile(hookE2ERuntimePath(workDir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write .runtime/%s: %v", name, err)
	}
}

// hookE2EMarkerExists reports whether a .runtime marker file is present. notify_once truncates its
// markers to zero bytes (fidelity-gate.sh:31), so presence — not content — is the signal.
func hookE2EMarkerExists(workDir, name string) bool {
	_, err := os.Stat(hookE2ERuntimePath(workDir, name))
	return err == nil
}

func hookE2EReadRuntime(t *testing.T, workDir, name string) string {
	t.Helper()
	data, err := os.ReadFile(hookE2ERuntimePath(workDir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// hookE2ELedger returns one tab-split argv per recorded `af` invocation.
func hookE2ELedger(t *testing.T, workDir string) [][]string {
	t.Helper()
	raw := hookE2EReadRuntime(t, workDir, "af_calls.log")
	var calls [][]string
	for _, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		args := strings.Split(strings.TrimSuffix(line, "\t"), "\t")
		calls = append(calls, args)
	}
	return calls
}

// hookE2EMailSends returns the (recipient, subject) of every `af mail send` the gate performed.
func hookE2EMailSends(t *testing.T, workDir string) [][2]string {
	t.Helper()
	var sends [][2]string
	for _, args := range hookE2ELedger(t, workDir) {
		if len(args) < 4 || args[0] != "mail" || args[1] != "send" {
			continue
		}
		subject := ""
		for i := 3; i < len(args)-1; i++ {
			if args[i] == "-s" {
				subject = args[i+1]
			}
		}
		sends = append(sends, [2]string{args[2], subject})
	}
	return sends
}

func hookE2EMailDeletes(t *testing.T, workDir string) []string {
	t.Helper()
	var ids []string
	for _, args := range hookE2ELedger(t, workDir) {
		if len(args) >= 3 && args[0] == "mail" && args[1] == "delete" {
			ids = append(ids, args[2])
		}
	}
	return ids
}

func hookE2ERunRecord(t *testing.T, workDir string) []map[string]json.RawMessage {
	t.Helper()
	raw := hookE2EReadRuntime(t, workDir, "fidelity_log.jsonl")
	var records []map[string]json.RawMessage
	for i, line := range strings.Split(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		records = append(records, turnJSONObject(t, []byte(line), fmt.Sprintf("fidelity_log.jsonl line %d", i+1)))
	}
	return records
}

func hookE2EDebugLog(t *testing.T, workDir string, g hookE2EGate) string {
	t.Helper()
	return hookE2EReadRuntime(t, workDir, g.debugLog)
}

// hookE2EJudgeInput returns the whole prompt the gate handed the judge stub.
func hookE2EJudgeInput(t *testing.T, workDir, gate string) string {
	t.Helper()
	data, err := os.ReadFile(hookE2ERuntimePath(workDir, "judge_input_"+gate+".txt"))
	if err != nil {
		t.Fatalf("the %s gate never reached the judge (no captured input): %v", gate, err)
	}
	return string(data)
}

// hookE2EEvidenceBlock slices the evidence out of a judge prompt. Both gates append it after the
// final separator (fidelity-gate.sh:222-237, quality-gate.sh:124-128), which is what makes the two
// prompts comparable at all despite the fidelity gate's extra step header.
func hookE2EEvidenceBlock(t *testing.T, judgeInput, what string) string {
	t.Helper()
	const sep = "\n\n---\n\n"
	i := strings.LastIndex(judgeInput, sep)
	if i < 0 {
		t.Fatalf("%s: no %q separator in the judge input:\n%s", what, sep, judgeInput)
	}
	return judgeInput[i+len(sep):]
}

// hookE2ERequireEvidence guards every comparison against the failure that would otherwise pass
// silently: two gates agreeing because NEITHER of them produced evidence.
func hookE2ERequireEvidence(t *testing.T, block, what string) {
	t.Helper()
	if !strings.HasPrefix(block, evidenceHeader) {
		t.Fatalf("%s: no evidence block reached the judge — got %q, want a block opening with the %s renderer's header",
			what, block, "`af turn evidence`")
	}
}

// hookE2EPayload builds the Stop-hook event JSON. transcriptPath is interpolated by the marshaller
// rather than by string concatenation so a Windows-style or space-bearing temp path cannot produce
// a payload jq silently reads as null.
func hookE2EPayload(t *testing.T, message, transcriptPath string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"stop_hook_active":       false,
		"last_assistant_message": message,
		"transcript_path":        transcriptPath,
	})
	if err != nil {
		t.Fatalf("marshal stop payload: %v", err)
	}
	return payload
}

// hookE2EWriteTranscript materialises a synthetic session transcript.
//
// Provenance, stated rather than implied: these fixtures are AUTHORED, built from the record
// builders turn_test.go:33-70 already keys to the live claude-2.1.224 field census. They are NOT
// captured, and nothing here is written into internal/transcript/testdata/recorded-real/ — D-10
// (design-doc.md:166) rules that fixtures there are captured, not authored, and
// TestRecordedRealFixtureSeam would then be running against a false provenance claim. turn_test.go:24-26
// records the same boundary: D-10 "does not reach synthetic JSONL written to t.TempDir()".
func hookE2EWriteTranscript(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	writeTranscript(t, path, lines...)
	return path
}
