package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/mail"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// containment-check is the PreToolUse hook command (Issue #386, Phase 2). It reads
// the hook payload from stdin, resolves the session-fixed worktree boundary from the
// environment (never from $(pwd) — that is the drifted value), computes the command's
// EFFECTIVE TARGET from tool_input (Cross-Review C1 — not the payload cwd), decides
// in/out-of-bounds via the pure worktree.Contains primitive, and on drift delivers
// exactly one deduped self-addressed corrective (an urgent WORKTREE_CONTAINMENT bead
// through the sendContainmentMail seam, plus hookSpecificOutput.additionalContext on
// stdout) with OBSERVABLE delivery failure — and ALWAYS exits 0 (ADR-007: hooks never
// block). It is a containment guard, not a sandbox: undecidable shells (cd $(...),
// eval, ${VAR}) are an accepted, documented residual.
var containmentCheckCmd = &cobra.Command{
	Use:   "containment-check",
	Short: "Detect and report worktree-boundary escapes (PreToolUse hook).",
	Long: `Containment-check intercepts Claude Code's PreToolUse hook to detect when an
agent's command targets a path OUTSIDE its assigned worktree boundary (where it could
contaminate the shared store). On drift it informs the agent (it never blocks — ADR-007)
via a deduped self-addressed urgent bead and same-loop additionalContext. The boundary
is resolved from session env only (AF_WORKTREE else config.FindLocalRoot), never from
the drifted working directory. It always exits 0.`,
	RunE: runContainmentCheck,
}

func init() {
	rootCmd.AddCommand(containmentCheckCmd)
}

// containmentPayload is the subset of the PreToolUse hook JSON this command reads.
// ToolInput stays raw so it can be decoded lazily per tool_name.
type containmentPayload struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Cwd       string          `json:"cwd"`
}

func runContainmentCheck(cmd *cobra.Command, _ []string) error {
	p, ok := readContainmentPayloadFromStdin()
	if !ok {
		// No usable payload (terminal stdin or decode error): nothing to check.
		// Never block (ADR-007).
		return nil
	}
	if p.Cwd == "" {
		if wd, err := getWd(); err == nil {
			p.Cwd = wd
		}
	}
	return runContainmentCheckCore(cmd.OutOrStdout(), p)
}

// readContainmentPayload decodes the hook JSON from r. Mirrors prime.go's
// readHookSessionID: a decode failure yields ok=false rather than an error, because
// a hook must never block on a malformed payload.
func readContainmentPayload(r io.Reader) (containmentPayload, bool) {
	var p containmentPayload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return containmentPayload{}, false
	}
	return p, true
}

// readContainmentPayloadFromStdin reads the payload from stdin, guarding against a
// terminal (os.ModeCharDevice) the same way prime.go does.
func readContainmentPayloadFromStdin() (containmentPayload, bool) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return containmentPayload{}, false
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return containmentPayload{}, false
	}
	return readContainmentPayload(os.Stdin)
}

// runContainmentCheckCore is the testable core. It returns nil on EVERY path
// (ADR-007). out receives the same-loop hookSpecificOutput.additionalContext nudge.
func runContainmentCheckCore(out io.Writer, p containmentPayload) error {
	role := os.Getenv("AF_ROLE")

	// 1. Resolve the session-fixed boundary (CMP-3 / D-4 / D-5). NEVER the factory-root
	//    walk-up resolver and NEVER bare AF_ROOT — both follow .factory-root to the
	//    SHARED PARENT for a worktree agent, whitelisting the very escape we must catch.
	boundary, fromEnv := resolveBoundary(p.Cwd)
	if boundary == "" {
		// H2 missing-boundary = fail-observable. Do NOT treat everything in-bounds.
		failObservable("", fmt.Sprintf("boundary unresolved (AF_WORKTREE empty, FindLocalRoot failed for cwd %q) — guard disabled", p.Cwd))
		return nil
	}

	// State lives under the env-resolved agent dir, never $(pwd) and never a
	// non-existent $AGENT_RUNTIME (which the gate scripts compute as $(pwd)/.runtime).
	agentDir := config.AgentDir(boundary, role)
	runtimeDir := filepath.Join(agentDir, ".runtime")

	// 2. [H3] Env-spoof cross-check (Gap 7). INVARIANT (G3, pinned by AC-7 test): the
	//    env anchor AF_WORKTREE_ID (session.go:293, = m.worktreeID) MUST equal the
	//    on-disk worktree_id (worktree.go:600, = filepath.Base(worktreePath)). A
	//    definite mismatch means the env boundary is untrusted: record it and fall
	//    back to the filesystem-derived boundary (fail-observable, never disarm).
	if fromEnv {
		if onDisk, err := os.ReadFile(filepath.Join(runtimeDir, "worktree_id")); err == nil {
			diskID := strings.TrimSpace(string(onDisk))
			if envID := os.Getenv("AF_WORKTREE_ID"); diskID != "" && diskID != envID {
				failObservable(runtimeDir, fmt.Sprintf(
					"env-spoof: AF_WORKTREE_ID=%q != on-disk worktree_id=%q — distrusting env boundary anchor", envID, diskID))
				if fb, ferr := config.FindLocalRoot(p.Cwd); ferr == nil && fb != "" {
					boundary = fb
					agentDir = config.AgentDir(boundary, role)
					runtimeDir = filepath.Join(agentDir, ".runtime")
				}
			}
		}
	}

	// 3. [C1] Effective target(s) from tool_input (NOT the payload cwd). A compound
	//    command may carry several decidable targets (e.g. `cd <in> && cd <parent>`).
	targets := parseEffectiveTarget(p.ToolName, p.ToolInput, p.Cwd)
	if len(targets) == 0 {
		// No decidable target: EXPECTED-by-scope cases (`git push origin <branch>` and
		// a no-cd `$HOME/.cache/...` write change no cwd) and undecidable residuals
		// (`cd $(...)`/eval) all land here — silent by construction (AC-4).
		return nil
	}

	// 4. Decide via the pure primitive. A compound command ESCAPES if ANY decidable
	//    segment is out of bounds — an in-bounds prefix (`cd <in>`) must never mask a
	//    later escaping segment (`&& cd <parent>`). Report the FIRST out-of-bounds
	//    target. clearDedup runs ONLY when EVERY segment stays in bounds, so an escaping
	//    compound can neither be judged in-bounds nor wipe a prior escape marker.
	for _, target := range targets {
		inBounds, err := worktree.Contains(boundary, target)
		if err != nil {
			failObservable(runtimeDir, fmt.Sprintf("worktree.Contains(%q,%q) error: %v", boundary, target, err))
			return nil
		}
		if !inBounds {
			// 5. Out of bounds → corrective (deduped, PID-locked, observable). Always exit 0.
			deliverCorrective(out, runtimeDir, boundary, role, target)
			return nil
		}
	}

	// All decidable targets are in bounds → return-in-bounds clears the dedup markers
	// so a later re-escape re-notifies.
	clearDedup(runtimeDir)
	return nil
}

// resolveBoundary returns the worktree boundary and whether it came from the session
// env. AF_WORKTREE (session-fixed, drift-independent) is preferred; otherwise the
// nearest local root via config.FindLocalRoot(cwd) (the factory root for a
// non-worktree agent, D-5). The factory-root walk-up resolver and bare AF_ROOT are
// deliberately NOT used (they resolve to the shared parent for a worktree agent).
func resolveBoundary(cwd string) (string, bool) {
	if b := os.Getenv("AF_WORKTREE"); b != "" {
		return b, true
	}
	if cwd != "" {
		if b, err := config.FindLocalRoot(cwd); err == nil {
			return b, false
		}
	}
	return "", false
}

// parseEffectiveTarget derives the command's effective filesystem target(s) from
// tool_input (C1). Bash: every literal cd/pushd/git -C destination across all segments.
// Write/Edit: the single file_path. Anything else (or undecidable) yields no targets →
// no detection. Returning all decidable Bash targets lets the caller flag the FIRST
// out-of-bounds one, so an in-bounds prefix never masks a later escaping segment.
func parseEffectiveTarget(toolName string, toolInput json.RawMessage, cwd string) []string {
	switch toolName {
	case "Write", "Edit":
		var ti struct {
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal(toolInput, &ti); err != nil {
			return nil
		}
		if t := resolveAgainst(cwd, ti.FilePath); t != "" {
			return []string{t}
		}
		return nil
	case "Bash":
		var ti struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(toolInput, &ti); err != nil {
			return nil
		}
		return parseBashTarget(ti.Command, cwd)
	default:
		return nil
	}
}

// parseBashTarget scans a Bash command for ALL LITERAL directory changes
// (cd/pushd/git -C) across every segment, in order. It does NOT stop at the first
// match: a compound command's effective directory is the LAST cd, and for containment
// ANY segment that escapes the boundary is dangerous, so the caller must see every
// decidable target (e.g. both `cd <in>` and `cd <parent>`) to flag the first escaping
// one. Only literal targets are decidable; cd $(...), eval, bash -c, and ${VAR}
// expansions are an accepted residual (Gaps 1/2) — this is a containment guard, not a
// sandbox. Returns nil when no segment carries a decidable directory change.
func parseBashTarget(command, cwd string) []string {
	var targets []string
	for _, seg := range splitShellSegments(command) {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "cd", "pushd":
			if len(fields) >= 2 {
				if t := literalTarget(fields[1]); t != "" {
					targets = append(targets, resolveAgainst(cwd, t))
				}
			}
		case "git":
			// git -C <dir> ...
			for i := 1; i < len(fields)-1; i++ {
				if fields[i] == "-C" {
					if t := literalTarget(fields[i+1]); t != "" {
						targets = append(targets, resolveAgainst(cwd, t))
					}
				}
			}
		}
	}
	return targets
}

// splitShellSegments splits a command on the shell separators that begin a new
// simple command (&&, ||, ;, |, newline), so each segment can be inspected for a
// leading verb. Two-char operators are listed before their one-char prefixes so the
// replacer matches them whole.
func splitShellSegments(command string) []string {
	repl := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	return strings.Split(repl.Replace(command), "\n")
}

// literalTarget returns tok as a literal path, or "" if it is not decidable as one:
// it strips a single layer of matched surrounding quotes, then rejects any token
// carrying shell expansion/substitution ($ ` * ? ~) or that looks like a flag.
func literalTarget(tok string) string {
	tok = strings.TrimSpace(tok)
	if len(tok) >= 2 {
		if (tok[0] == '"' && tok[len(tok)-1] == '"') || (tok[0] == '\'' && tok[len(tok)-1] == '\'') {
			tok = tok[1 : len(tok)-1]
		}
	}
	if tok == "" {
		return ""
	}
	if strings.ContainsAny(tok, "$`*?~") {
		return "" // undecidable expansion (residual)
	}
	if strings.HasPrefix(tok, "-") {
		return "" // a flag (e.g. `cd -`), not a directory literal
	}
	return tok
}

// resolveAgainst makes p absolute. An absolute p is returned as-is; a relative p is
// joined onto cwd (the shell's resolution base) when cwd is known. Using cwd only as
// the resolution BASE — never as the effective target itself — is consistent with C1.
func resolveAgainst(cwd, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	if cwd == "" {
		return p
	}
	return filepath.Join(cwd, p)
}

// deliverCorrective emits the same-loop additionalContext nudge and sends exactly one
// deduped urgent self-addressed bead through the sendContainmentMail seam, inspecting
// the result (AC-10 — the send error is never swallowed). A PID-lock serialises concurrent
// invocations; a one-shot marker keyed by (target|boundary) enforces AC-9.
func deliverCorrective(out io.Writer, runtimeDir, boundary, role, target string) {
	markerDir := filepath.Join(runtimeDir, "containment_seen")
	markerPath := filepath.Join(markerDir, dedupKey(target, boundary))
	if _, err := os.Stat(markerPath); err == nil {
		return // this exact escape was already corrected (AC-9)
	}

	// PID-lock idiom (fidelity-gate.sh:48-61, in Go via internal/lock): a live lock
	// (ErrLocked) means a concurrent invocation is mid-correction — bail silently
	// rather than double-send. Any OTHER acquire failure (e.g. the lock file cannot
	// be written) is a guard malfunction, so make it observable (AC-10 / CMP-9).
	l := lock.NewWithPath(filepath.Join(runtimeDir, "containment.lock"))
	if err := l.Acquire(os.Getenv("CLAUDE_SESSION_ID")); err != nil {
		if !errors.Is(err, lock.ErrLocked) {
			failObservable(runtimeDir, fmt.Sprintf("containment lock acquire failed: %v", err))
		}
		return
	}
	defer func() { _ = l.Release() }()
	if _, err := os.Stat(markerPath); err == nil {
		return // a concurrent holder corrected it between our check and the lock
	}

	body := correctiveBody(boundary, target)

	// Same-loop nudge (Gap 4) — emitted regardless of the durable bead's fate so the
	// agent is informed this turn.
	emitAdditionalContext(out, body)

	if role == "" {
		failObservable(runtimeDir, "AF_ROLE empty — cannot self-address WORKTREE_CONTAINMENT bead")
		return
	}

	if err := sendContainmentMail(boundary, role, "WORKTREE_CONTAINMENT", body); err != nil {
		// Inspect the result; a silently-broken guard looks healthy (AC-10 / CMP-9).
		failObservable(runtimeDir, fmt.Sprintf("corrective send failed for target=%q boundary=%q: %v", target, boundary, err))
		return // no marker on failure → retry on a later turn
	}

	// Success → write the one-shot marker so identical repeats dedup (AC-9).
	if err := os.MkdirAll(markerDir, 0o755); err == nil {
		_ = os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644)
	}
}

// correctiveBody is the least-disclosure message: it names ONLY the agent's OWN
// boundary and the offending path plus remediation — never a sibling or parent path
// (Security × UX resolution).
func correctiveBody(boundary, target string) string {
	return fmt.Sprintf(
		"WORKTREE CONTAINMENT: you operated on %q, which is OUTSIDE your worktree boundary %q. "+
			"git, go, and make all work from your agent directory inside that boundary — return there; "+
			"do not cd out. Operating in the shared parent can contaminate the shared store.",
		target, boundary)
}

// emitAdditionalContext writes the PreToolUse hookSpecificOutput.additionalContext
// JSON to out (the same-loop correction channel).
func emitAdditionalContext(out io.Writer, body string) {
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	payload.HookSpecificOutput.HookEventName = "PreToolUse"
	payload.HookSpecificOutput.AdditionalContext = body
	_ = json.NewEncoder(out).Encode(&payload)
}

// dedupKey is the sha256 of "target|boundary" (sha256 idiom: prime.go:268).
func dedupKey(target, boundary string) string {
	h := sha256.Sum256([]byte(target + "|" + boundary))
	return fmt.Sprintf("%x", h[:])
}

// clearDedup removes the one-shot markers so a return-in-bounds resets dedup state.
func clearDedup(runtimeDir string) {
	_ = os.RemoveAll(filepath.Join(runtimeDir, "containment_seen"))
}

// failObservable records a guard malfunction to the append-only containment_debug.log
// (when the runtime dir is known) AND to stderr. It deliberately DIVERGES from the
// gate scripts' stderr-discarding send (fidelity-gate.sh:197, quality-gate.sh:121): a
// silently-broken guard must never look healthy (AC-10 / CMP-9).
func failObservable(runtimeDir, msg string) {
	if runtimeDir != "" {
		if err := os.MkdirAll(runtimeDir, 0o755); err == nil {
			if f, ferr := os.OpenFile(
				filepath.Join(runtimeDir, "containment_debug.log"),
				os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); ferr == nil {
				_, _ = fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), msg)
				_ = f.Close()
			}
		}
	}
	fmt.Fprintf(os.Stderr, "containment-check: %s\n", msg)
}

// sendContainmentMail is the ADR-009 mail-send seam (none existed before #386 — mail.go
// calls router.Send directly). Production sends an in-process urgent self-addressed
// bead via mail.NewRouter+Send; tests reassign it to record/force-fail the send while
// the real detect→resolve→dedup path runs (CMP-8 / AC-11). It deliberately has NO
// isTestBinary() short-circuit — that would silently no-op the production path under
// test; isolation is the test reassigning this var, not a binary check.
var sendContainmentMail = func(wd, role, subject, body string) error {
	msg := mail.NewMessage(role, role, subject, body)
	msg.Priority = issuestore.PriorityUrgent
	// Route the notification by SESSION IDENTITY (AF_ROOT / home factory), NOT the
	// offending boundary cwd (wd) that triggered the alarm (issue #519 review
	// follow-up, thread 7b). The containment hook fires precisely when an agent has
	// strayed, so routing by the stray cwd would post the warning into — or, under
	// the post-#519 cross-check, be refused by — the very wrong factory it is warning
	// about, silently losing the alarm exactly when it is needed. This routes the
	// notification only; it does NOT change how the escape boundary is decided (that
	// stays AF_ROOT-shunning, containment.go's job).
	root, err := containmentRoutingRoot(wd)
	if err != nil {
		return err
	}
	store, err := newIssueStoreAt(root, os.Getenv("AF_ACTOR"))
	if err != nil {
		return err
	}
	router, err := mail.NewRouter(root, store)
	if err != nil {
		return err
	}
	return router.Send(context.Background(), msg)
}

// containmentRoutingRoot picks the factory a containment notification is delivered
// to by SESSION IDENTITY: the launcher-baked AF_ROOT (the agent's home factory)
// wins, so the stray-agent alarm reaches the REAL factory even when the agent's cwd
// has wandered into a nested checkout. It falls back to the passed boundary only
// when AF_ROOT is unset (a bare operator shell) — the watchdog AF_ROOT-first idiom
// (resolveWatchdogRoot). It NEVER consults AF_ROOT to decide the escape boundary
// (that remains runContainmentCheckCore's job); it only chooses the delivery root.
func containmentRoutingRoot(boundary string) (string, error) {
	if afRoot := os.Getenv("AF_ROOT"); afRoot != "" {
		if root, err := config.FindFactoryRoot(afRoot); err == nil {
			return root, nil
		}
	}
	return config.FindFactoryRoot(boundary)
}
