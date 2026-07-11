package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/lock"
)

var improvementCmd = &cobra.Command{
	Use:   "improvement [on|off]",
	Short: "Toggle or show the continuous-improvement hook status",
	Long: `Toggle the continuous-improvement hook on or off, or show current status.

The improvement hook is AND-gated: it fires for an agent only when BOTH the
factory-level toggle (.agentfactory/.improvement-hook) is "on" AND that agent's
continuous_improvement flag is true. Mirrors af quality on the file-toggle side —
enabling writes "on\n", disabling writes "off\n" to .agentfactory/.improvement-hook.
Unlike af fidelity it is NEVER seeded by af install --init (absent ⇒ off).

Promotion is the operator's. A fired hook has the agent edit its OWN store formula
(.agentfactory/store/formulas/<agent>.formula.toml), but that store copy is derived:
make sync-formulas is an unconditional cp and af install overwrites it on any byte-diff,
so any redeploy or re-init REVERTS an un-promoted edit. To keep it, copy the change into
internal/cmd/install_formulas/<agent>.formula.toml and rebuild (ADR-015).

Trust boundary: the /improve-agent instruction is static — only the finishing formula's
own name is substituted (operator provenance, never task-derived text). The self-edit is
validated in-process by af improvement complete and surfaced by its outcome mail, so a
human sees a changed/unchanged + passed/FAILED verdict before deciding whether to promote.

  af improvement                    show factory line + per-agent effective (AND) table + pending sessions
  af improvement on|off             toggle the factory-level hook
  af improvement on|off --agent <a> set a single agent's continuous_improvement flag`,
	Args: cobra.MaximumNArgs(1),
	RunE: runImprovement,
}

// improvementCompleteCmd is the two-level sub-verb (mirrors config.go's
// configCmd.AddCommand(configBuildHostCmd)). On a surviving improvement session it
// consumes the pending marker, validates the edited formula in-process, mails the
// outcome verdict, releases the deferred lock, and replays the deferred teardown.
var improvementCompleteCmd = &cobra.Command{
	Use:   "complete",
	Short: "Finish a pending improvement session: validate, mail the verdict, tear down",
	Long: `Consume the .runtime/improvement_pending marker the improvement hook wrote,
validate the edited formula (in-process), mail a changed/unchanged + passed/FAILED
verdict to the caller (supervisor fallback), release the identity lock af done
deferred, and replay the dispatched-session teardown iff the marker recorded
terminate_on_complete. Fail-open: a broken formula still exits 0 and still tears
down (the verdict says FAILED). The marker is consumed atomically (rename to
.consumed) so a watchdog reap and the agent's own run cannot both tear down.

  --reap        watchdog reap mode: relabels the outcome mail IMPROVEMENT_REAPED
  --dir <path>  operate on an explicit agent dir (required under --reap: the
                watchdog's cwd is the factory root, not the agent's)`,
	RunE: runImprovementComplete,
}

func init() {
	improvementCmd.Flags().String("agent", "", "target a single agent's continuous_improvement flag")
	rootCmd.AddCommand(improvementCmd)

	improvementCmd.AddCommand(improvementCompleteCmd)
	improvementCompleteCmd.Flags().Bool("reap", false, "watchdog reap mode (relabels the outcome mail IMPROVEMENT_REAPED)")
	improvementCompleteCmd.Flags().String("dir", "", "explicit agent dir (required with --reap; overrides getwd)")
}

// improvementHookFile is the factory-level state file. Absent ⇒ off; it is
// deliberately NEVER seeded by af install --init (the quality-gate side of the
// seeding asymmetry, not fidelity's seeded-on).
func improvementHookFile(factoryRoot string) string {
	return filepath.Join(factoryRoot, ".agentfactory", ".improvement-hook")
}

// improvementFactoryEnabled reports whether the factory-level hook file reads "on".
func improvementFactoryEnabled(factoryRoot string) bool {
	data, err := os.ReadFile(improvementHookFile(factoryRoot))
	return err == nil && strings.TrimSpace(string(data)) == "on"
}

// improvementEnabled is the AND of the factory-level toggle and the per-agent
// continuous_improvement flag. Any read/resolve error ⇒ false: the hook
// fails safe-off (it does not fire when state cannot be determined), never true.
func improvementEnabled(factoryRoot, agent string) bool {
	if !improvementFactoryEnabled(factoryRoot) {
		return false
	}
	cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(factoryRoot))
	if err != nil {
		return false
	}
	entry, ok := cfg.Agents[agent]
	if !ok {
		return false
	}
	return entry.ContinuousImprovement
}

// improvementPendingFile is the per-agent pending marker path, resolved via
// resolveAgentDir so the writer (the af done hook), the reader (status), and the
// reaper all agree on ONE location — the live worktree's agent
// dir when a worktree exists, else the factory-root agent dir.
func improvementPendingFile(factoryRoot, agent string) string {
	return filepath.Join(resolveAgentDir(factoryRoot, agent), ".runtime", "improvement_pending")
}

// improvementMarker is the on-disk pending marker (.runtime/improvement_pending,
// JSON, lastClosedStepRecord style — done.go). It carries every fact the
// completion verb needs, because cleanupRuntimeArtifacts deletes formula_caller and
// dispatched in the same `af done` invocation. terminate_on_complete is the ORIGINAL
// shouldTerminate at fire time.
type improvementMarker struct {
	InstanceID          string `json:"instance_id"`
	Formula             string `json:"formula"`
	FormulaPath         string `json:"formula_path"`
	Caller              string `json:"caller"`
	TerminateOnComplete bool   `json:"terminate_on_complete"`
	FormulaSHA256       string `json:"formula_sha256"`
	FiredAt             string `json:"fired_at"`
}

// writeImprovementMarker writes the JSON marker to the resolved agent dir
// (json.MarshalIndent + trailing newline, mirroring writeLastClosedStep).
func writeImprovementMarker(factoryRoot, agent string, m improvementMarker) error {
	path := improvementPendingFile(factoryRoot, agent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// readImprovementPending returns the marker's fired_at stamp and whether it exists.
// The marker is JSON; a legacy bare-stamp file (or any non-JSON content) falls back
// to the trimmed raw content so a half-migrated marker still renders. A
// missing/unreadable marker ⇒ ("", false).
func readImprovementPending(factoryRoot, agent string) (string, bool) {
	data, err := os.ReadFile(improvementPendingFile(factoryRoot, agent))
	if err != nil {
		return "", false
	}
	var m improvementMarker
	if err := json.Unmarshal(data, &m); err == nil && m.FiredAt != "" {
		return m.FiredAt, true
	}
	return strings.TrimSpace(string(data)), true
}

// writeImprovementPending records a fired_at-only pending marker for the agent (the
// convenience surface: a thin wrapper over the JSON writer so callers that only have
// a timestamp — and the status tests — keep working against the JSON shape).
func writeImprovementPending(factoryRoot, agent, firedAt string) error {
	return writeImprovementMarker(factoryRoot, agent, improvementMarker{FiredAt: firedAt})
}

// recordImprovementSkip writes a skip reason to .runtime/improvement_skipped in
// the resolved agent dir, so a decision to NOT fire the hook is observable.
// Same directory as the pending marker, by design.
func recordImprovementSkip(factoryRoot, agent, reason string) error {
	path := filepath.Join(resolveAgentDir(factoryRoot, agent), ".runtime", "improvement_skipped")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(reason+"\n"), 0o644)
}

// improvementInstructionTemplate is the STATIC /improve-agent instruction (design
// #483). Only the formula name is substituted — twice: once into the store path, once
// into the `af formula show` verification command. No task-derived text ever enters it.
const improvementInstructionTemplate = `IMPROVEMENT HOOK: use the Skill tool to load /improve-agent and improve the
formula at .agentfactory/store/formulas/%s.formula.toml so that
future runs can leverage learnings from this session. Derive the evidence from
this session's own context; apply the improvements that pass the skill's
validation checklist without asking for confirmation. Do not commit or
regenerate the agent — leave promotion to the human operator. Note: editing
this path from a worktree session will produce a WORKTREE_CONTAINMENT advisory;
it is expected for this sanctioned edit. After editing, verify with:
af formula show %s --json (exit 0). When finished, run:
af improvement complete`

// improvementFormula holds the prefix-stripped formula facts the hook needs: the
// bare name, the operator-facing relative path (used in the marker and the
// instruction text), and the absolute path (used to stat and hash the file).
type improvementFormula struct {
	Name    string
	RelPath string
	AbsPath string
}

// improvementInstruction strips the "Formula: " instance-bead title prefix
// (agents.go:161 idiom), resolves the formula file under FormulasDir, exists-guards
// it, and returns the STATIC instruction text. ok==false when the name is empty or
// the file is absent (the "resolved formula file exists" fire conjunct).
func improvementInstruction(root, formulaTitle string) (string, improvementFormula, bool) {
	name := strings.TrimPrefix(formulaTitle, "Formula: ")
	if name == "" {
		return "", improvementFormula{}, false
	}
	f := improvementFormula{
		Name:    name,
		RelPath: ".agentfactory/store/formulas/" + name + ".formula.toml",
		AbsPath: filepath.Join(config.FormulasDir(root), name+".formula.toml"),
	}
	if _, err := os.Stat(f.AbsPath); err != nil {
		return "", improvementFormula{}, false
	}
	return fmt.Sprintf(improvementInstructionTemplate, name, name), f, true
}

// formulaSHA256 returns the full-hex sha256 of the formula file's bytes, recorded in
// the marker at fire time so the completion verb can report changed/unchanged.
func formulaSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:]), nil
}

// evaluateImprovementFire decides whether the continuous-improvement hook fires for
// the just-finished formula and, on fire, writes the pending marker and returns the
// delivery instruction. The factory-level toggle and a non-empty caller are the
// caller's precondition (checked in done.go); this decomposes the per-agent AND gate
// so a toggles-on no-fire can report WHY.
//
// fired==true only when both toggles are on for the agent, the formula file exists,
// no marker pre-exists, and the marker was written. When fired==false and reason!="",
// the caller records a skip and warns; reason=="" is a silent, non-error no-fire (the
// agent opted out, or a marker already exists).
func evaluateImprovementFire(cwd, factoryRoot, instanceID, caller, formulaTitle string, terminateOnComplete bool) (fired bool, agent, instruction, reason string) {
	agent, err := detectAgentName(cwd, factoryRoot)
	if err != nil {
		return false, "", "", fmt.Sprintf("agent name unresolved: %v", err)
	}
	cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(factoryRoot))
	if err != nil {
		return false, agent, "", fmt.Sprintf("agents.json unreadable: %v", err)
	}
	entry, ok := cfg.Agents[agent]
	if !ok || !entry.ContinuousImprovement {
		return false, agent, "", "" // agent opted out — a normal skip, not an error
	}
	if _, exists := readImprovementPending(factoryRoot, agent); exists {
		return false, agent, "", "" // idempotence: a marker already exists
	}
	instruction, formula, ok := improvementInstruction(factoryRoot, formulaTitle)
	if !ok {
		return false, agent, "", fmt.Sprintf("formula file not found for %q", formulaTitle)
	}
	sha, err := formulaSHA256(formula.AbsPath)
	if err != nil {
		return false, agent, "", fmt.Sprintf("formula sha256: %v", err)
	}
	marker := improvementMarker{
		InstanceID:          instanceID,
		Formula:             formula.Name,
		FormulaPath:         formula.RelPath,
		Caller:              caller,
		TerminateOnComplete: terminateOnComplete,
		FormulaSHA256:       sha,
		FiredAt:             time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeImprovementMarker(factoryRoot, agent, marker); err != nil {
		return false, agent, "", fmt.Sprintf("write marker: %v", err)
	}
	return true, agent, instruction, ""
}

func runImprovement(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	factoryRoot, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}

	agentName, _ := cmd.Flags().GetString("agent")

	if len(args) == 0 {
		return printImprovementStatus(factoryRoot)
	}

	switch args[0] {
	case "on", "off":
	default:
		return fmt.Errorf("usage: af improvement [on|off]")
	}

	if agentName != "" {
		return setAgentImprovement(factoryRoot, agentName, args[0])
	}

	hookFile := improvementHookFile(factoryRoot)
	if err := os.WriteFile(hookFile, []byte(args[0]+"\n"), 0o644); err != nil {
		verb := "enabling"
		if args[0] == "off" {
			verb = "disabling"
		}
		return fmt.Errorf("%s improvement hook: %w", verb, err)
	}
	fmt.Printf("improvement hook: %s\n", args[0])
	return nil
}

// setAgentImprovement sets a single agent's continuous_improvement flag. Because
// SaveAgentConfig validates nothing, this MUST guard the write: ValidateAgentName
// first (so "../evil" is rejected as a bad name, not a missing agent), then a
// membership check against the loaded Agents map (so a typo cannot orphan a flag
// onto a nonexistent agent). AgentEntry is a map value type, so it is copied out,
// mutated, and reassigned.
func setAgentImprovement(factoryRoot, agent, state string) error {
	if err := config.ValidateAgentName(agent); err != nil {
		return err
	}
	path := config.AgentsConfigPath(factoryRoot)
	cfg, err := config.LoadAgentConfig(path)
	if err != nil {
		return err
	}
	entry, ok := cfg.Agents[agent]
	if !ok {
		return fmt.Errorf("agent %q not found in agents.json", agent)
	}
	entry.ContinuousImprovement = state == "on"
	cfg.Agents[agent] = entry
	if err := config.SaveAgentConfig(path, cfg); err != nil {
		return err
	}
	fmt.Printf("improvement hook: %s (agent %s)\n", state, agent)
	return nil
}

// printImprovementStatus renders the rich status: the factory line (always, and
// format-compatible with the quality/fidelity one-liners), then a per-agent
// effective (AND) table, then any pending improvement sessions. The factory line
// is printed BEFORE loading agents.json so a fresh factory with no agents.json
// still emits the stable first line.
func printImprovementStatus(factoryRoot string) error {
	factoryOn := improvementFactoryEnabled(factoryRoot)
	if factoryOn {
		fmt.Println("improvement hook: on")
	} else {
		fmt.Println("improvement hook: off")
	}

	cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(factoryRoot))
	if err != nil {
		// Fresh factory (no agents.json yet): the first line is the contract; there
		// is nothing further to show. quality/fidelity never load agents.json either.
		return nil
	}

	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	if len(names) > 0 {
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  agent\tcontinuous-improvement\teffective")
		for _, name := range names {
			ci := "off"
			if cfg.Agents[name].ContinuousImprovement {
				ci = "on"
			}
			effective := "skipped"
			if factoryOn && cfg.Agents[name].ContinuousImprovement {
				effective = "fires"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", name, ci, effective)
		}
		_ = tw.Flush()
	}

	var pending []string
	for _, name := range names {
		if firedAt, ok := readImprovementPending(factoryRoot, name); ok {
			pending = append(pending, fmt.Sprintf("  %s (fired %s)", name, firedAt))
		}
	}
	if len(pending) > 0 {
		fmt.Println("pending improvement sessions:")
		for _, line := range pending {
			fmt.Println(line)
		}
	}
	return nil
}

// readImprovementMarkerFile reads the FULL 7-field marker from an explicit path
// (the completion verb renames the pending marker to .consumed first, then reads
// it). The existing readImprovementPending returns only fired_at, which is
// insufficient for the completion verb — it needs Formula, Caller, FormulaSHA256,
// and TerminateOnComplete.
func readImprovementMarkerFile(path string) (improvementMarker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return improvementMarker{}, err
	}
	var m improvementMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return improvementMarker{}, err
	}
	return m, nil
}

// finishDispatchedSessionFn is a seam over finishDispatchedSession (done.go) so the
// completion verb's deferred teardown is observable in tests (the underlying
// selfTerminate no-ops under isTestBinary, which would otherwise hide whether
// terminate_on_complete was honored). The af done path calls finishDispatchedSession
// directly and is unchanged.
var finishDispatchedSessionFn = finishDispatchedSession

// runImprovementComplete resolves the agent dir and factory root, then runs the
// completion core. On the normal (agent-run) path the dir is getwd; on the watchdog
// reap path (--dir) it is explicit, because the watchdog's cwd is the factory root.
func runImprovementComplete(cmd *cobra.Command, args []string) error {
	reap, _ := cmd.Flags().GetBool("reap")
	dir, _ := cmd.Flags().GetString("dir")

	// --reap is the watchdog path, whose cwd is the factory root (not the agent's), so
	// getwd would resolve the wrong marker. Require the explicit dir rather than
	// silently degrading to a "no pending improvement" miss.
	if reap && dir == "" {
		return fmt.Errorf("--reap requires --dir <agentDir>")
	}

	agentDir := dir
	if agentDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		agentDir = wd
	}
	factoryRoot, err := resolveInvokerRoot(agentDir)
	if err != nil {
		return err
	}
	return runImprovementCompleteCore(agentDir, factoryRoot, reap)
}

// runImprovementCompleteCore is the in-process completion path:
// atomic marker consume → in-process formula validation → outcome mail →
// lock release → finishDispatchedSession iff terminate_on_complete. It is fail-open
// toward teardown: a validation failure does NOT abort (the verdict carries FAILED,
// the command still exits 0 and still tears down). Split from runImprovementComplete
// so tests can drive it with an explicit agentDir (os.Getwd is not redirectable).
func runImprovementCompleteCore(agentDir, factoryRoot string, reap bool) error {
	// Atomic consume FIRST: rename the pending marker to .consumed so exactly one
	// actor (the agent's own `complete` or the watchdog reap) proceeds under a race
	// — there is no cross-process lock (lock.Acquire is advisory/TOCTOU).
	pending := filepath.Join(agentDir, ".runtime", "improvement_pending")
	consumed := pending + ".consumed"
	if err := os.Rename(pending, consumed); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no pending improvement (missing .runtime/improvement_pending)")
		}
		return err
	}

	marker, err := readImprovementMarkerFile(consumed)
	if err != nil {
		return fmt.Errorf("reading consumed improvement marker: %w", err)
	}

	// marker.FormulaPath is RELATIVE (.agentfactory/store/formulas/<name>.formula.toml);
	// os.ReadFile resolves it against the process cwd, which is the worktree on the
	// agent-run path. Reconstruct the abs path against the factory root instead
	// (mirrors improvementInstruction's AbsPath at improvement.go).
	absFormula := filepath.Join(config.FormulasDir(factoryRoot), marker.Formula+".formula.toml")

	// In-process validation (ADR-014-safe; formula.ParseFile is pure Go). Branch on
	// the RETURNED error, NOT `af formula show`'s exit code (which is always 0).
	_, valErr := formula.ParseFile(absFormula)
	validationPassed := valErr == nil

	// sha256 changed/unchanged verdict against the marker's recorded hash.
	// A recompute error ⇒ treat as changed (the safe, visible verdict).
	changed := true
	if sum, err := formulaSHA256(absFormula); err == nil {
		changed = sum != marker.FormulaSHA256
	}

	recipient := marker.Caller
	if recipient == "" {
		recipient = escalationTarget
	}
	subject, body := improvementOutcomeMessage(marker.Formula, changed, validationPassed, reap)
	fmt.Println(subject)
	if err := sendImprovementOutcomeMail(recipient, subject, body); err != nil {
		fmt.Fprintf(os.Stderr, "warning: improvement outcome mail to %s failed: %v\n", recipient, err)
	}

	// Release the identity lock af done deferred (PID-agnostic file removal).
	_ = lock.New(agentDir).Release()

	// Replay the deferred teardown iff af done recorded terminate_on_complete.
	if marker.TerminateOnComplete {
		finishDispatchedSessionFn(agentDir, factoryRoot)
	}
	return nil
}

// improvementOutcomeMessage builds the verdict subject/body: the formula name,
// a changed/unchanged word (sha256 delta), and a passed/FAILED word (validation).
// Under reap it relabels the subject IMPROVEMENT_REAPED so a watchdog-forced
// completion surfaces loudly to the caller.
func improvementOutcomeMessage(formulaName string, changed, validationPassed, reap bool) (subject, body string) {
	changeWord := "unchanged"
	if changed {
		changeWord = "changed"
	}
	valWord := "passed"
	if !validationPassed {
		valWord = "FAILED"
	}
	label := "IMPROVEMENT"
	if reap {
		label = "IMPROVEMENT_REAPED"
	}
	subject = fmt.Sprintf("%s: %s — %s, validation %s", label, formulaName, changeWord, valWord)
	body = fmt.Sprintf("Continuous-improvement self-edit of formula %s: %s, in-process validation %s.",
		formulaName, changeWord, valWord)
	return subject, body
}

// sendImprovementOutcomeMail shells `af mail send` to deliver the verdict to
// the caller (supervisor fallback). A distinct seam from sendImprovementMail (the
// self-mail): this is a different message. Declared as a var + isTestBinary
// no-op so tests observe delivery without shelling out (mirrors sendWorkDoneMail).
var sendImprovementOutcomeMail = func(recipient, subject, body string) error {
	if isTestBinary() {
		return nil
	}

	afPath, err := os.Executable()
	if err != nil {
		afPath, _ = exec.LookPath("af")
	}
	if afPath == "" {
		return fmt.Errorf("cannot find af binary")
	}
	cmd := exec.Command(afPath, "mail", "send", recipient, "-s", subject, "-m", body)
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("outcome mail to %s failed: %w\nsubprocess stderr: %s", recipient, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("outcome mail to %s: %w", recipient, err)
	}
	return nil
}

// improvementReapCeiling is the age past which a lingering improvement_pending marker
// is reaped: 30 minutes by default, overridable via AF_IMPROVEMENT_REAP_AFTER
// (interpreted as MINUTES — the house int-env idiom, mirroring
// AF_DONE_VELOCITY_THRESHOLD; time.ParseDuration is unused in this codebase).
func improvementReapCeiling() time.Duration {
	minutes := 30
	if v := os.Getenv("AF_IMPROVEMENT_REAP_AFTER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			minutes = n
		}
	}
	return time.Duration(minutes) * time.Minute
}

// reapImprovementSession shells `af improvement complete --reap --dir <agentDir>` —
// the SAME atomic-consume + teardown path an agent's own `complete` takes, run in a
// child process so the teardown (which kills a tmux session) is isolated from the
// watchdog. A var seam + isTestBinary no-op so the poll-loop reap matrix is hermetic
// (mirrors triggerHandoffRespawn).
//
// AF_ROLE is injected into the child env: the watchdog session carries only AF_ROOT
// (up.go), but finishDispatchedSession reads AF_ROLE to remove the git worktree
// (done.go). Without it the reap kills the tmux session but silently skips worktree
// removal — leaving the MaxWorktrees slot the reaper exists to reclaim
// still held. agentName is the agents.json key, which is exactly the AF_ROLE value an
// agent session exports.
var reapImprovementSession = func(agentDir, agentName string) error {
	if isTestBinary() {
		return nil
	}

	afPath, err := os.Executable()
	if err != nil {
		afPath, _ = exec.LookPath("af")
	}
	if afPath == "" {
		return fmt.Errorf("cannot find af binary")
	}
	cmd := exec.Command(afPath, "improvement", "complete", "--reap", "--dir", agentDir)
	cmd.Env = append(os.Environ(), "AF_ROLE="+agentName)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("reap %s failed: %w\nsubprocess stderr: %s", agentDir, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("reap %s: %w", agentDir, err)
	}
	return nil
}

// maybeReapImprovement is the watchdog reap check: a monitored
// agent whose improvement_pending marker is older than the ceiling is a zombie
// improvement session that never ran `complete` — reap it (shell the same completion
// path) so it stops holding a worktree slot. Returns true when a reap was triggered;
// a missing, young, or unparseable-stamp marker ⇒ false (no-op). Called from
// pollAgents BEFORE the HasSession early-continue — the reap target may already be a
// dead session.
func maybeReapImprovement(root, agentName string) bool {
	firedAt, ok := readImprovementPending(root, agentName)
	if !ok {
		return false
	}
	fired, err := time.Parse(time.RFC3339, firedAt)
	if err != nil {
		return false
	}
	if time.Since(fired) < improvementReapCeiling() {
		return false
	}
	agentDir := resolveAgentDir(root, agentName)
	if err := reapImprovementSession(agentDir, agentName); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: %s: improvement reap failed: %v\n", agentName, err)
	}
	return true
}
