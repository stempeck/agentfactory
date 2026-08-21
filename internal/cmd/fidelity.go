package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/lock"
)

var fidelityCmd = &cobra.Command{
	Use:   "fidelity [on|off|status]",
	Short: "Toggle or show fidelity gate status",
	Long: `Toggle the fidelity gate hook on or off, or show current status.

The fidelity gate is a runtime grader that evaluates each agent turn against
the current formula step's contract via a Haiku model. It fires after every
Stop event when a formula is hooked; on by default (af install --init creates
.agentfactory/.fidelity-gate with "on"). The factory-wide toggle file mirrors
af quality on the file-toggle side — enabling writes "on\n", disabling writes
"off\n" to .agentfactory/.fidelity-gate — but the command around it does not:
disabling is an operator action, --agent scopes the change to one agent instead
of the whole factory, and every write is recorded in .agentfactory/.fidelity-gate.log.

status is the read side, and it always covers the whole factory: the toggle state,
the agents that currently have an override recorded, a per-agent table of
evaluations, failures, the last recorded violation count and the last graded step —
read from that agent's own .runtime/fidelity_log.jsonl — plus the escalated step id
from its separate .runtime/fidelity_escalated_step latch, and the last few lines of
the provenance log, which answer who last moved the switch. Evaluation and failure
counts are a floor rather than a total: the reader tail-reads a bounded 64 KiB window
of each run record, so earlier evaluations are not counted, and a row that was
truncated is marked with a trailing +. Because --agent scopes a write, af fidelity
status --agent <name> is refused rather than silently narrowed. An override is a
recorded operator decision that status lists and af fidelity on --agent <name>
clears; no hook reads that directory today, so recording one does not by itself
stop the gate firing for that agent.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFidelity,
}

func init() {
	fidelityCmd.Flags().String("agent", "",
		"scope the toggle to one agent's override instead of the whole factory")
	rootCmd.AddCommand(fidelityCmd)
}

// fidelityOffRefusal is deliberately NOT the K1 teardown refusal. That body claims the command
// "stops the whole factory ... would kill YOU", which is false for a gate toggle, and reusing it
// would add a fifth surface to a set authority_test.go:13-21 enumerates as four. The precedent it
// follows is recoveryResetRefusal — the other operator-only verb that is not teardown-class. Like
// both of them it never names the detection mechanism: ux.md L36-39 forbids handing the agent a
// bypass recipe.
const fidelityOffRefusal = `fidelity off refused: agent context (af fidelity off)
The fidelity gate is the oversight that grades your turns, so switching it off is
an operator action — an agent silencing its own grader is the exact outcome this
refusal exists to prevent. Do NOT retry and do NOT disable it another way. If you
believe the gate is misfiring on you, tell your operator
(af mail send manager -s "fidelity gate misfiring" -m "...") and continue with
your remaining work.`

// The provenance source field: which of the three writers flipped the toggle. The callers are
// distinguishable only by their call sites, so each one names itself.
const (
	fidelitySourceCLI     = "cli"
	fidelitySourceStartup = "af-up"
	fidelitySourceInstall = "install"
)

// fidelityTailWindow bounds the status reader. An agent appends one run-record line per turn and
// nothing rotates the file, so a long-lived session's log must not decide how much memory or time
// `af fidelity status` spends. It is deliberately its own constant rather than an alias of another
// subsystem's 64 KiB limit.
const fidelityTailWindow = 64 << 10

// fidelityProvenanceTailLines is how much of the audit log `status` renders: enough to answer "who
// turned it back on" without turning a status surface into a log viewer.
const fidelityProvenanceTailLines = 5

func fidelityGateFile(factoryRoot string) string {
	return filepath.Join(config.ConfigDir(factoryRoot), ".fidelity-gate")
}

// fidelityGateLogFile is the toggle's sibling audit log. Provenance never goes into the toggle file
// itself: the hook compares that file with `cat … != "on"` (hooks/fidelity-gate.sh:48-49), so an
// extra byte there disables the gate.
func fidelityGateLogFile(factoryRoot string) string {
	return fidelityGateFile(factoryRoot) + ".log"
}

// fidelityOverridesDir holds the per-agent disable markers. It is factory-level on purpose: an
// agent must not be able to silence its own grader by writing its own workspace (security.md T2b,
// data.md:112).
func fidelityOverridesDir(factoryRoot string) string {
	return filepath.Join(config.ConfigDir(factoryRoot), "fidelity-overrides")
}

func runFidelity(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	factoryRoot, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}

	agent, _ := cmd.Flags().GetString("agent")

	// An explicitly empty --agent= must not fall through to the factory-wide branch. `off --agent=`
	// reads as "disable one agent" and would instead disable the gate for everyone — the exact
	// all-or-nothing outcome the scoped override exists to replace.
	if cmd.Flags().Changed("agent") && agent == "" {
		return errors.New("--agent requires an agent name")
	}

	// --agent scopes a WRITE. Accepting it on a read surface and quietly ignoring it would tell an
	// operator chasing a storm that they had looked at one agent when they had looked at all of them.
	if agent != "" && (len(args) == 0 || args[0] == "status") {
		return fmt.Errorf("--agent scopes `af fidelity on|off`; run `af fidelity status` for the whole factory")
	}

	if len(args) == 0 {
		return printFidelityToggle(factoryRoot, cwd)
	}

	switch args[0] {
	case "status":
		return printFidelityStatus(factoryRoot, cwd)
	case "on":
		if agent != "" {
			return clearFidelityOverride(factoryRoot, agent)
		}
		if err := applyFidelityGate(factoryRoot, cwd, "on"); err != nil {
			return err
		}
		appendFidelityProvenance(factoryRoot, fidelitySourceCLI, "on")
		fmt.Println("fidelity gate: on")
	case "off":
		// The primary control (security.md T2a): the cwd-scoped active-formula guard inside
		// applyFidelityGate only ever asked where the caller was standing, so an agent in any
		// other directory could switch off the oversight grading it. That guard becomes
		// secondary — it still refuses a mid-formula disable — and authority decides first.
		// Scoped disables are gated here too, before the agent name is even read.
		if callerAuthority() != AuthorityOperator {
			return errors.New(fidelityOffRefusal)
		}
		if agent != "" {
			return writeFidelityOverride(factoryRoot, agent)
		}
		if err := applyFidelityGate(factoryRoot, cwd, "off"); err != nil {
			return err
		}
		appendFidelityProvenance(factoryRoot, fidelitySourceCLI, "off")
		fmt.Println("fidelity gate: off")
	default:
		return fmt.Errorf("usage: af fidelity [on|off|status]")
	}

	return nil
}

// writeFidelityOverride silences one agent's gate and leaves every other agent's oversight in
// place — D-6's surgical answer to a verdict storm, which previously forced a fleet-wide disable.
func writeFidelityOverride(root, agent string) error {
	// The name becomes a path component, so it is validated before any path is composed — the same
	// reason runRecoveryReset validates before touching the breaker (recovery.go:171-176).
	if err := config.ValidateAgentName(agent); err != nil {
		return fmt.Errorf("refusing to scope the fidelity gate to an invalid agent name: %w", err)
	}
	dir := fidelityOverridesDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating fidelity override dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, agent), []byte("off\n"), 0644); err != nil {
		return fmt.Errorf("disabling fidelity gate for %s: %w", agent, err)
	}
	appendFidelityProvenance(root, fidelitySourceCLI, "off:"+agent)
	fmt.Printf("fidelity gate: off for %s only (recorded; factory-wide gate unchanged) — no hook reads the override dir today, so the gate still grades %s\n", agent, agent)
	return nil
}

func clearFidelityOverride(root, agent string) error {
	if err := config.ValidateAgentName(agent); err != nil {
		return fmt.Errorf("refusing to scope the fidelity gate to an invalid agent name: %w", err)
	}
	if err := os.Remove(filepath.Join(fidelityOverridesDir(root), agent)); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("re-enabling fidelity gate for %s: %w", agent, err)
		}
		fmt.Printf("fidelity gate: on for %s (no override to clear)\n", agent)
		return nil
	}
	appendFidelityProvenance(root, fidelitySourceCLI, "on:"+agent)
	fmt.Printf("fidelity gate: on for %s\n", agent)
	return nil
}

// appendFidelityProvenance records one `ts actor source state` line per toggle write. Two recorded
// incidents ended with nobody able to say who had re-enabled the gate (ux.md:35); this log is the
// answer. It never fails the toggle the operator asked for — but it does say so on stderr when it
// cannot record, because a log whose whole purpose is answering "who turned it back on" must not
// grow silent holes (containment.go:391-403 takes the same position). The line is emitted with a
// single Fprintf: three writers append here from separate processes, and one write under O_APPEND
// is atomic with respect to the offset (recovery.go:326-329).
func appendFidelityProvenance(root, source, state string) {
	if err := os.MkdirAll(config.ConfigDir(root), 0o755); err != nil {
		warnFidelityProvenanceLost(err)
		return
	}
	f, err := os.OpenFile(fidelityGateLogFile(root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		warnFidelityProvenanceLost(err)
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %s %s %s\n",
		time.Now().UTC().Format(time.RFC3339), fidelityActor(root), source, state); err != nil {
		warnFidelityProvenanceLost(err)
	}
}

// warnFidelityProvenanceLost is lowercase and on stderr on purpose: the six status tests assert on
// stdout, and one of them fails on the substring "WARNING".
func warnFidelityProvenanceLost(err error) {
	fmt.Fprintf(os.Stderr, "warning: fidelity gate toggled but not recorded in the provenance log: %v\n", err)
}

// fidelityActor names who flipped the toggle, from trusted context only. INV-2 forbids a
// caller-supplied identity, so there is no --actor flag and nothing here reads an identity
// override: an operator shell is the absence of any agent signal, and anything else resolves
// through the mandated resolveAgentName.
func fidelityActor(root string) string {
	if callerAuthority() == AuthorityOperator {
		return "operator"
	}
	if wd, err := getWd(); err == nil {
		if name, err := resolveAgentName(wd, root); err == nil && name != "" {
			return "agent:" + name
		}
	}
	return "agent"
}

// printFidelityToggle is the stable first line every fidelity surface opens with: the toggle byte,
// plus the caller's own stale gate lock if there is one. An unreadable toggle prints "off" because
// that is what the hook does with it (hooks/fidelity-gate.sh:48-53) — the master toggle fails
// toward off, the deliberate inverse of the per-agent override's fail-toward-oversight.
func printFidelityToggle(factoryRoot, cwd string) error {
	data, err := os.ReadFile(fidelityGateFile(factoryRoot))
	if err != nil || strings.TrimSpace(string(data)) != "on" {
		fmt.Println("fidelity gate: off")
		return nil
	}
	lockPath := filepath.Join(cwd, ".runtime", "fidelity-gate.lock")
	if info, err := lock.NewWithPath(lockPath).Read(); err == nil && info.IsStale() {
		fmt.Printf("fidelity gate: on (WARNING: stale lock at .runtime/fidelity-gate.lock, PID %d dead)\n", info.PID)
		return nil
	}
	fmt.Println("fidelity gate: on")
	return nil
}

// printFidelityStatus is the operator's storm-response surface (ux.md U-3a): which agents are being
// graded and how they are doing, which agents are currently exempt, and who last moved the switch.
// Every section is omitted when it has nothing to say, so a fresh factory still prints exactly the
// one line the six no-arg status tests pin.
func printFidelityStatus(factoryRoot, cwd string) error {
	if err := printFidelityToggle(factoryRoot, cwd); err != nil {
		return err
	}

	if overrides := activeFidelityOverrides(factoryRoot); len(overrides) > 0 {
		fmt.Printf("overrides (gate off for): %s\n", strings.Join(overrides, ", "))
	}

	if reports := fidelityAgentReports(factoryRoot); len(reports) > 0 {
		tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "  agent\tevaluations\tfailures\tviolations\tlast step\tescalated")
		anyPartial := false
		for _, r := range reports {
			floor := ""
			if r.partial {
				floor, anyPartial = "+", true
			}
			fmt.Fprintf(tw, "  %s\t%d%s\t%d%s\t%d\t%s\t%s\n",
				r.name, r.evaluations, floor, r.failures, floor, r.violations,
				dashIfEmpty(r.lastStep), dashIfEmpty(r.escalatedStep))
		}
		tw.Flush()
		if anyPartial {
			fmt.Printf("  (+ counted from the last %d KiB of the run record; earlier evaluations are not included)\n",
				fidelityTailWindow>>10)
		}
	}

	if tail := fidelityProvenanceTail(factoryRoot); len(tail) > 0 {
		fmt.Println("recent toggle writes (ts actor source state):")
		for _, line := range tail {
			fmt.Printf("  %s\n", line)
		}
	}
	return nil
}

// fidelityRecord is the frozen K7 run-record line (design-doc.md:76, data.md:107). Phase 6 writes
// it from the hook; this phase only reads, so the schema is honored here before any writer exists.
type fidelityRecord struct {
	TS              string `json:"ts"`
	StepID          string `json:"step_id"`
	VerdictOK       bool   `json:"verdict_ok"`
	CallsTotal      int    `json:"calls_total"`
	CallsShown      int    `json:"calls_shown"`
	ViolationsAfter int    `json:"violations_after"`
	Escalated       bool   `json:"escalated"`
}

type fidelityAgentReport struct {
	name          string
	evaluations   int
	failures      int
	violations    int
	lastStep      string
	escalatedStep string
	// partial marks counts derived from a bounded read of a longer log, so status can render them
	// as floors. violations and lastStep are last-value fields and stay exact either way.
	partial bool
}

// fidelityAgentReports walks the provisioned agent dirs. The run record lives under each agent's
// own .runtime/ (hooks/fidelity-gate.sh:15 roots it at the agent's cwd), so a factory-wide status
// has to visit them one by one — and via resolveAgentDir, because a dispatched agent's .runtime is
// in its worktree, not under the factory root (the resolution writeTeardownRefusedArtifact uses for
// the same reason).
func fidelityAgentReports(factoryRoot string) []fidelityAgentReport {
	entries, err := os.ReadDir(config.AgentsDir(factoryRoot))
	if err != nil {
		return nil // no agents dir yet ⇒ nothing to report
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	reports := make([]fidelityAgentReport, 0, len(names))
	for _, name := range names {
		reports = append(reports, readFidelityAgentReport(factoryRoot, name))
	}
	return reports
}

func readFidelityAgentReport(factoryRoot, name string) fidelityAgentReport {
	report := fidelityAgentReport{name: name}
	runtimeDir := filepath.Join(resolveAgentDir(factoryRoot, name), ".runtime")

	records, truncated := readBoundedTail(filepath.Join(runtimeDir, "fidelity_log.jsonl"))
	report.partial = truncated
	for _, line := range records {
		var rec fidelityRecord
		// One malformed line costs its own record and nothing else: a status surface that aborted
		// on a torn append would go dark exactly when the gate was busiest.
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		report.evaluations++
		if !rec.VerdictOK {
			report.failures++
		}
		report.violations = rec.ViolationsAfter
		report.lastStep = rec.StepID
	}

	if data, err := os.ReadFile(filepath.Join(runtimeDir, "fidelity_escalated_step")); err == nil {
		report.escalatedStep = strings.TrimSpace(string(data))
	}
	return report
}

// fidelityProvenanceTail ignores the truncation flag: it renders a fixed-size tail by design, so a
// window that dropped older lines has dropped nothing this caller would have shown.
func fidelityProvenanceTail(factoryRoot string) []string {
	lines, _ := readBoundedTail(fidelityGateLogFile(factoryRoot))
	if len(lines) > fidelityProvenanceTailLines {
		lines = lines[len(lines)-fidelityProvenanceTailLines:]
	}
	return lines
}

// readBoundedTail returns the whole lines within the last fidelityTailWindow bytes of path, or nil
// if it cannot be read — an absent run record is the normal case, not an error. A line straddling
// the window boundary is dropped rather than counted: half a JSON object is not a record, and
// counting it would corrupt the very numbers status reports. The second return reports that the
// caller is holding a suffix rather than the whole file, so a count derived from it can be
// presented as the floor it is instead of as a total.
func readBoundedTail(path string) (lines []string, truncated bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	dropFragment := false
	if info.Size() > fidelityTailWindow {
		truncated = true
		// Open one byte before the window so the reader can tell what it landed on: a newline
		// there means the window starts on a record boundary and its first line is whole, while
		// anything else means the first line is the tail half of an older record. Probing is what
		// keeps a boundary-aligned window from discarding a real record.
		if _, err := f.Seek(-(fidelityTailWindow + 1), io.SeekEnd); err != nil {
			return nil, false
		}
		var probe [1]byte
		if _, err := io.ReadFull(f, probe[:]); err != nil {
			return nil, false
		}
		dropFragment = probe[0] != '\n'
	}

	var scanned []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		scanned = append(scanned, sc.Text())
	}
	// A read fault stops the walk early, leaving a suffix of the truth that must be labelled as one
	// rather than reported as a complete count. The line cap cannot be what trips this: no token
	// can exceed the window, and a file large enough to hold one has already set the flag above.
	if sc.Err() != nil {
		truncated = true
	}
	// The fragment is dropped by position, BEFORE blanks are filtered: a window opening one byte
	// short of a newline yields a zero-length fragment, and filtering first would make the drop
	// land on the following whole record instead.
	if dropFragment && len(scanned) > 0 {
		scanned = scanned[1:]
	}

	for _, line := range scanned {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, truncated
}

func activeFidelityOverrides(factoryRoot string) []string {
	entries, err := os.ReadDir(fidelityOverridesDir(factoryRoot))
	if err != nil {
		return nil // no override dir ⇒ nothing is scoped off
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// applyFidelityGate writes the fidelity gate, honoring the active-formula guard
// for "off". root locates the gate file (.agentfactory/.fidelity-gate);
// formulaDir is where .runtime/hooked_formula is checked (cwd for the CLI today,
// the af-up-resolved root for the Phase-3 startup path). Write errors are wrapped
// with the same messages runFidelity used historically; the guard refusal is
// returned verbatim so CLI behavior is unchanged.
//
// Authority is NOT checked here: the refusal is scoped to `af fidelity off`
// (design-doc.md:248), and this function is also the blanket `af up` path, which
// has no authority gate of its own. Provenance is likewise the caller's job —
// logging here would double-log every write that arrives via applyGate's
// delegation — so a new production caller must append its own line.
func applyFidelityGate(root, formulaDir, state string) error {
	gateFile := fidelityGateFile(root)
	switch state {
	case "on":
		if err := os.WriteFile(gateFile, []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("enabling fidelity gate: %w", err)
		}
		return nil
	case "off":
		hookedFormula := filepath.Join(formulaDir, ".runtime", "hooked_formula")
		if _, err := os.Stat(hookedFormula); err == nil {
			return fmt.Errorf("cannot disable fidelity gate while a formula is active (found .runtime/hooked_formula)")
		}
		if err := os.WriteFile(gateFile, []byte("off\n"), 0644); err != nil {
			return fmt.Errorf("disabling fidelity gate: %w", err)
		}
		return nil
	}
	return fmt.Errorf("usage: af fidelity [on|off]")
}

// applyGate applies a quality/fidelity gate state using the af-up-resolved root
// (R-7: never re-derived from cwd). "default"/"" is a no-op (C-4 invariant).
// quality has no active-formula guard and is a direct write; fidelity routes
// through applyFidelityGate so the guard is honored. Its only caller is Phase 3's
// runUp (the SC8 mechanism).
func applyGate(root, formulaDir, gate, state string) error {
	if state == "" || state == "default" {
		return nil
	}
	switch gate {
	case "quality":
		return os.WriteFile(qualityGateFile(root), []byte(state+"\n"), 0644)
	case "fidelity":
		if err := applyFidelityGate(root, formulaDir, state); err != nil {
			return err
		}
		// The blanket writer records itself here rather than at its call site so up.go stays
		// byte-identical — its line numbers are pinned by teardown_scanner_enforce_test.go's
		// allowedTeardownSites, which would re-anchor on any inserted line.
		appendFidelityProvenance(root, fidelitySourceStartup, state)
		return nil
	case "improvement":
		// No active-formula guard: the improvement hook is advisory/fail-open, so
		// disabling it never needs to block on .runtime/hooked_formula (unlike fidelity).
		return os.WriteFile(improvementHookFile(root), []byte(state+"\n"), 0644)
	case "telemetry":
		// No active-formula guard either: disabling telemetry mid-formula loses
		// visibility, not correctness, so it never needs to block on an active formula.
		return os.WriteFile(telemetryGateFile(root), []byte(state+"\n"), 0644)
	}
	return nil
}
