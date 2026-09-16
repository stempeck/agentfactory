package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

var tokenomicsCmd = &cobra.Command{
	Use:   "tokenomics [on|off|status]",
	Short: "Toggle the token-economics policy surface or show why it is inert",
	Long: `Toggle the token-economics umbrella on or off, or run its liveness self-test.

The umbrella has two inputs and both must permit it: the factory toggle file
.agentfactory/.tokenomics that this verb writes, and the tokenomics block in
startup.json, whose per-mechanism enums resolve underneath it. Switching the
umbrella off silences every mechanism, including one an operator set explicitly
to "on" — that is the point of having an umbrella rather than six keys.

The accounting serves two objectives, and every intervention record names which of
them fired. capacity is window-driven: it asks whether the next step fits what is
left, and it is structurally inert wherever no capacity fact is declared, which is
every cloud profile. efficiency is baseline-driven and applies on every profile,
roomy ones included: it asks what a step has historically GENERATED — output
tokens, thinking tokens, sub-agent tokens, and how often it re-read a file it had
already read. The predicate behind it is handed no window, no occupancy and no
pool, so a roomy profile cannot switch that half off.

The toggle half follows af fidelity: switching OFF is an operator action, because
an agent silencing the accounting of its own token spend is the outcome the
refusal exists to prevent, while switching ON is never gated — oversight fails
toward being on. Every write is recorded in .agentfactory/.tokenomics.log as one
'ts actor source state' line, which is what answers "who turned it back on".

status is a liveness self-test, not a dashboard. The policy surface depends on a
chain of other gates, and a status that printed zeros for a chain that was simply
dark would be indistinguishable from one that had measured and found nothing. So
it names each reason it is inert, prints the resolved context window TOGETHER
with where that number came from, and prints the two arithmetic inputs the
admission predicate actually divides with. Every figure is read from disk, and a
figure that is empty says why it is empty rather than printing a bare zero.

status also prints the intervention ledger: per mechanism, a tally split by the
closed action vocabulary and by which objective the firing served, plus how many
learned aggregates the digest holds. observe records are tallied but counted as
firings for neither objective — a gate that admitted a launch it could not judge
must not read as one that decided.

status always exits 0 under --json; branch on .state.

What the harness guarantees to a run, what each mechanism is permitted to do
to it, the audit record every firing leaves, and the failure modes that fail
open: see the "Token economics" section of USING_TOKENOMICS.md.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTokenomics,
}

func init() {
	tokenomicsCmd.Flags().Bool("json", false, "Emit the machine-readable status envelope instead of the human self-test")
	rootCmd.AddCommand(tokenomicsCmd)
}

// tokenomicsOffRefusal follows fidelity's grammar, not the teardown refusal's. authority_test.go
// enumerates the teardown surfaces as a closed set, and the claims that body makes — "stops the
// whole factory", "would kill YOU" — are simply false for a policy toggle. Like every refusal in
// this package it never names the signal the caller was classified by, because handing an agent the
// detection mechanism hands it the bypass.
const tokenomicsOffRefusal = `tokenomics off refused: agent context (af tokenomics off)
The tokenomics surface is the accounting of what your own turns cost, so
switching it off is an operator action — an agent silencing its own cost
accounting is the exact outcome this refusal exists to prevent.
Do NOT retry and do NOT disable it another way. If you believe a mechanism is
misfiring on you, tell your operator
(af mail send manager -s "tokenomics misfiring" -m "...") and continue with
your remaining work.`

// The provenance source field. Only the CLI writes this toggle today; the constant exists rather
// than a bare literal because fidelity's three writers proved that a log without a source cannot
// distinguish an operator's decision from a startup path's blanket write.
const tokenomicsSourceCLI = "cli"

// tokenomicsSchemaVersion is deliberately local rather than telemetry.SchemaVersion. This payload
// is a different contract with a different consumer, and borrowing telemetry's version would tie
// two schemas that have no reason to move together — a telemetry field change would bump a
// tokenomics document that had not changed.
const tokenomicsSchemaVersion = 1

const (
	tokenomicsStateOK       = "ok"
	tokenomicsStateDegraded = "degraded"
	tokenomicsStateError    = "error"
)

// The two fields that can honestly read zero. Both data sources have shipped — the standing digest
// is written by af telemetry rebuild and every mechanism firing writes one intervention record — so
// these no longer name an unbuilt phase. They still exist, and are still carried WITH the zero,
// because that is the only thing separating "nothing has happened here yet" from "the reader ran
// and found nothing", and the two lead an operator to opposite actions.
//
// Each is non-empty exactly when its figure is empty. A factory that has learned something reports
// the count and no reason, the same rule tokenomicsPolicyJSON.UnavailableBecause follows.
const (
	// The pointer is the digest FILES rather than af telemetry rebuild, which would answer the same
	// question by rewriting the cache — an expensive write is the wrong thing to recommend to
	// someone who only asked what it holds.
	tokenomicsCoverageUnavailable = "this factory has learned nothing yet: no digest under the " +
		"telemetry digest directory holds an aggregate, which is the cold-start state until enough " +
		"steps have closed for af telemetry rebuild to fold"
	// tokenomicsCoverageStructurallyCold is the OTHER zero, and it must read differently from the
	// cold-start above: the digest holds aggregates, but none has reached the trust floor a learned
	// read joins on, so no admission or band read will ever hit them. It is the B1 symptom made
	// visible — runs that file one single-run key each instead of accumulating — and it points an
	// operator at why the runs are not joining rather than at running more of them.
	tokenomicsCoverageStructurallyCold = "this factory's digest holds only keys below the learned " +
		"trust floor: steps have closed, but none has accumulated enough runs for a learned read to " +
		"join, so the coverage a read can use is zero (run the same formula-steps again, or check " +
		"that runs are accumulating under one key rather than splitting per instance)"
	tokenomicsInterventionsUnavailable = "no mechanism has recorded an intervention in this factory " +
		"yet; a firing writes one telemetry.EventIntervention record, and none is on disk"
)

// tokenomicsInterventionTailLines is how many recent firings `status` renders, mirroring the
// provenance tail above and bounded for the same reason: a self-test answers "is anything acting on
// my runs", which the last few lines settle, and a status surface that grew with the log would stop
// being readable on the factory that needed it most.
const tokenomicsInterventionTailLines = 5

// tokenomicsProvenanceTailLines is how much of the audit log `status` renders: enough to answer
// "who turned it back on" without turning a status surface into a log viewer. It mirrors
// fidelityProvenanceTailLines, and it exists for a reason beyond symmetry — the Long help above
// advertises the log as the thing that answers that question, and a log with no reader is not an
// answer.
const tokenomicsProvenanceTailLines = 5

// tokenomicsContractPointer names the behavior contract, in the shape af watchdog --help uses for
// USING_RECOVERY.md (watchdog.go:83-84). The contract lives in its own companion guide,
// USING_TOKENOMICS.md. The constant stays single because a pointer and its destination spelled as
// two independent literals is how the two drift apart while both look right in isolation.
const tokenomicsContractPointer = `see the "Token economics" section of USING_TOKENOMICS.md`

func tokenomicsGateFile(factoryRoot string) string {
	return filepath.Join(config.ConfigDir(factoryRoot), ".tokenomics")
}

// tokenomicsGateLogFile is the toggle's SIBLING audit log. Provenance never goes into the toggle
// file itself: every gate reader in this package compares the trimmed contents against "on", so an
// extra byte there is a silent disable.
func tokenomicsGateLogFile(factoryRoot string) string {
	return tokenomicsGateFile(factoryRoot) + ".log"
}

// tokenomicsFactoryEnabled reads the toggle with the same absent/unreadable/near-miss ⇒ off shape
// telemetryFactoryEnabled and statuslineFactoryEnabled use.
func tokenomicsFactoryEnabled(factoryRoot string) bool {
	data, err := os.ReadFile(tokenomicsGateFile(factoryRoot))
	return err == nil && strings.TrimSpace(string(data)) == "on"
}

func runTokenomics(cmd *cobra.Command, args []string) error {
	// Read --json before anything can fail, so the machine-readable surface honors its "always
	// exit 0, branch on .state" contract even when the factory root cannot be resolved. A consumer
	// that got a non-zero exit and an empty stdout could not tell a broken factory from a broken
	// binary. The human path keeps its non-zero exits.
	jsonOut, _ := cmd.Flags().GetBool("json")

	cwd, err := os.Getwd()
	if err != nil {
		if jsonOut {
			return emitTokenomicsJSONError(err)
		}
		return err
	}
	// resolveInvokerRoot, never a root walk of this command's own: inside a worktree the two
	// answers differ, and a gate written to the wrong one is worse than an error.
	factoryRoot, err := resolveInvokerRoot(cwd)
	if err != nil {
		if jsonOut {
			return emitTokenomicsJSONError(err)
		}
		return err
	}

	if jsonOut && (len(args) == 0 || args[0] == "status") {
		return emitTokenomicsStatusJSON(factoryRoot)
	}
	if len(args) == 0 || args[0] == "status" {
		return printTokenomicsStatus(factoryRoot)
	}

	switch args[0] {
	case "on":
		// Never authority-gated, the deliberate inverse of "off" below. Refusing to re-enable
		// oversight would leave an agent that reached a disabled factory unable to restore it.
		warnTokenomicsChainDark(factoryRoot)
		if err := writeTokenomicsGate(factoryRoot, "on"); err != nil {
			return err
		}
		appendTokenomicsProvenance(factoryRoot, tokenomicsSourceCLI, "on")
		fmt.Println("tokenomics: on")
	case "off":
		// Authority decides FIRST, before any path is composed or any byte is written, so a
		// refused call leaves the toggle and the provenance log untouched.
		if callerAuthority() != AuthorityOperator {
			return errors.New(tokenomicsOffRefusal)
		}
		if err := writeTokenomicsGate(factoryRoot, "off"); err != nil {
			return err
		}
		appendTokenomicsProvenance(factoryRoot, tokenomicsSourceCLI, "off")
		fmt.Println("tokenomics: off")
	default:
		return fmt.Errorf("usage: af tokenomics [on|off|status]")
	}
	return nil
}

func writeTokenomicsGate(factoryRoot, state string) error {
	if err := os.MkdirAll(config.ConfigDir(factoryRoot), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.WriteFile(tokenomicsGateFile(factoryRoot), []byte(state+"\n"), 0o644); err != nil {
		return fmt.Errorf("switching tokenomics %s: %w", state, err)
	}
	return nil
}

// warnTokenomicsChainDark is advisory and never blocks the write the operator asked for. Its
// stream is the load-bearing detail: os.Stderr rather than cmd.ErrOrStderr(), because cobra
// resolves the child's writer through rootCmd and three test files in this package leave that
// pointing at a bytes.Buffer — and because the status assertions read stdout, where warnings
// collide (warnFidelityProvenanceLost documents the same hazard). Matches improvement.go:373-381
// down to the two-line remediation shape.
// It warns about EVERY dark leg, not the first. The status self-test treats the two gates as equal
// members of one chain, and an operator told only about telemetry would fix it, switch the surface
// on again, and still be inert — which is the same two-round-trip failure the inert reason list
// avoids, moved to the toggle path.
func warnTokenomicsChainDark(factoryRoot string) {
	if !telemetryFactoryEnabled(factoryRoot) {
		fmt.Fprintln(os.Stderr, "warning: the telemetry gate is off, so no step records carry the "+
			"context figures the tokenomics mechanisms reason about — the surface will be on but inert")
		fmt.Fprintln(os.Stderr, "  remediation: run `af telemetry on`")
	}
	if !statuslineFactoryEnabled(factoryRoot) {
		fmt.Fprintln(os.Stderr, "warning: the session statusline is off, so no live occupancy reading "+
			"reaches a decision — the surface will be on but inert")
		fmt.Fprintln(os.Stderr, "  remediation: run `af statusline on`")
	}
}

// appendTokenomicsProvenance records one `ts actor source state` line per toggle write, under
// O_APPEND so a single Fprintf is atomic with respect to the offset. It never fails the toggle,
// but it does say so on stderr when it cannot record: a log whose whole purpose is answering "who
// turned it back on" must not grow silent holes.
func appendTokenomicsProvenance(root, source, state string) {
	if err := os.MkdirAll(config.ConfigDir(root), 0o755); err != nil {
		warnTokenomicsProvenanceLost(err)
		return
	}
	f, err := os.OpenFile(tokenomicsGateLogFile(root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		warnTokenomicsProvenanceLost(err)
		return
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s %s %s %s\n",
		time.Now().UTC().Format(time.RFC3339), tokenomicsActor(root), source, state); err != nil {
		warnTokenomicsProvenanceLost(err)
	}
}

func warnTokenomicsProvenanceLost(err error) {
	fmt.Fprintf(os.Stderr, "warning: tokenomics toggled but not recorded in the provenance log: %v\n", err)
}

// tokenomicsActor names who flipped the toggle, from trusted context only. INV-2 forbids a
// caller-supplied identity, so there is no --actor flag: an operator shell is the ABSENCE of any
// agent signal, and anything else resolves through the mandated resolveAgentName.
func tokenomicsActor(root string) string {
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

// --- the liveness self-test ---------------------------------------------------------------

// tokenomicsLiveness is everything both renderings need, computed once. Gathering it in one place
// rather than in each formatter is what keeps the human surface and the JSON surface from
// answering the same question differently — the layering divergence telemetry_json.go opens by
// documenting.
type tokenomicsLiveness struct {
	enabled  bool
	umbrella string
	policy   tokenomics.Policy
	window   tokenomics.Window

	// dispatchPool is the declared backend-pool fact (AF_BACKEND_POOL_TOKENS) on the default
	// profile — the operand the dispatch gate divides by (#669 THREAD-2). poolDeclared distinguishes
	// an elastic backend that declares no fact (the gate is inert) from a declared pool that happens
	// to be small, so the surface can say WHY rather than print a bare 0.
	dispatchPool int64
	poolDeclared bool

	// The two other dispatch-gate operator facts that ride the same default profile beside the pool: the
	// child-footprint floor that guards the first child near the ceiling, and the hard cap that serializes
	// sub-agents entirely. Shown only when a pool is declared (the gate is otherwise inert), so an operator
	// can tell an armed floor/cap from an absent one (#669 F3). childFloorDeclared distinguishes an operator
	// override from the ~50k default the accessor falls back to.
	childFloor         int64
	childFloorDeclared bool
	parallelDisabled   bool

	// policyUnavailable is why the four policy fields above say nothing, empty when they say
	// something. It exists for the same reason tokenomicsCoverageUnavailable does: a config that
	// could not be read leaves this surface with no policy at all, and printing the zero value's
	// resolution — margin 0%, min runs 1, five mechanisms on — would be reporting numbers no
	// factory will ever run under as though they were the operator's.
	policyUnavailable string

	inert      []string
	provenance []string

	// What the surface has actually observed, as opposed to how it is configured. Both are read
	// fresh on every status and neither is stored anywhere: the digest is a cache of records and
	// the interventions are the records themselves.
	coverage                 int
	coverageUnavailable      string
	interventions            []string
	interventionsUnavailable string

	// The counters and the measurement self-test (#678 K9). Both answer the question the blocks
	// above cannot: not "is this factory configured to act" but "has it, and could anyone tell".
	// Gathered as the payload shape both renderings use, for the reason this struct exists.
	counters            []tokenomicsCounterJSON
	countersUnavailable string
	measurement         tokenomicsMeasurementJSON
}

func gatherTokenomicsLiveness(factoryRoot string) tokenomicsLiveness {
	l := tokenomicsLiveness{
		enabled: tokenomicsFactoryEnabled(factoryRoot),
		inert:   []string{},
	}
	// These two read disk — the digest directory, then every roster agent's record log — which is a
	// real cost on a verb that used to be a constant-time config read. It is paid here rather than
	// lazily because a self-test that skipped the expensive half would be reporting on a factory it
	// had not looked at. The bound is the roster times one log plus one rotation (store.go's read
	// window), and nothing invokes this on a hot path: not the hooks, not the statusline, not the
	// console.
	l.coverage, l.coverageUnavailable = readTokenomicsCoverage(factoryRoot)
	var firings map[string]tokenomicsFirings
	l.interventions, l.interventionsUnavailable, firings = readTokenomicsInterventionTail(factoryRoot)
	l.measurement = readTokenomicsMeasurement()

	// Every reason is collected, never just the first. An operator told only about telemetry
	// would fix it, re-run, and be told about the statusline — which is two round trips to learn
	// one thing.
	if !l.enabled {
		l.inert = append(l.inert, "inert because the tokenomics umbrella is off (af tokenomics on)")
	}
	if !telemetryFactoryEnabled(factoryRoot) {
		l.inert = append(l.inert, "inert because the telemetry gate is off, so no step record carries "+
			"the context figures every mechanism reasons about (af telemetry on)")
	}
	if !statuslineFactoryEnabled(factoryRoot) {
		l.inert = append(l.inert, "inert because the session statusline is off, so no live occupancy "+
			"reading reaches a decision (af statusline on)")
	}

	cfg := config.TokenomicsConfig{}
	startup, err := config.LoadStartupConfig(factoryRoot)
	if err != nil {
		l.inert = append(l.inert, "inert because the startup config could not be read: "+err.Error())
		// A factory whose startup.json does not load is a factory that will not launch, so there is
		// no policy to report — not a default one, and certainly not the zero value's. Every reason
		// below is skipped for the same cause: they are all statements about a file this surface
		// has just said it cannot read.
		l.policyUnavailable = "startup.json could not be read, so no policy resolves: " + err.Error()
		l.window = resolveTokenomicsWindow(factoryRoot)
		l.provenance = readTokenomicsProvenanceTail(factoryRoot)
		// The dark floor is learned_min_runs x 3 and the policy that carries it did not resolve, so
		// the counters can report what fired but never whether the silence is a dark actuator.
		l.counters, l.countersUnavailable = tokenomicsCounters(firings, l.coverage, 0), l.policyUnavailable
		return l
	}
	cfg = startup.Tokenomics
	l.umbrella = cfg.Enabled

	// The umbrella has two inputs living in two places — the toggle file and the enum — and only
	// this layer can see both. ResolvePolicy takes the conjunction rather than reading either,
	// which is why it is pure.
	umbrellaOn := l.enabled && cfg.Enabled != "off"
	l.policy = tokenomics.ResolvePolicy(umbrellaOn, cfg)

	// The enum is the umbrella's OTHER input, and a surface that reported only the toggle file
	// would call itself live while this leg silenced every mechanism — the same "prints zeros for
	// a chain that is simply dark" failure the gate reasons above exist to prevent, just one
	// config layer down.
	if cfg.Enabled == "off" {
		l.inert = append(l.inert, "inert because startup.json sets tokenomics.enabled=off, which is "+
			"the umbrella's other input and vetoes every mechanism regardless of the toggle")
	}
	// The general form of the same lie. Six per-mechanism enums set to off leave an umbrella that
	// is on with nothing underneath it that can ever fire, and no reason above would notice. The
	// umbrellaOn guard keeps this from restating a cause already named.
	if umbrellaOn && !tokenomicsAnyMechanismOn(l.policy) {
		l.inert = append(l.inert, "inert because every mechanism in startup.json's tokenomics block "+
			"resolved off, so the umbrella is on with nothing underneath it")
	}

	// Only the eligible leg can be missing: a mechanism that fired left a record, and one that did
	// not legitimately reads zero. Without the trusted-key count, though, that zero says nothing —
	// which is the difference between a dark actuator and a factory with nothing to act on.
	l.counters = tokenomicsCounters(firings, l.coverage, l.policy.LearnedMinRuns)
	l.countersUnavailable = l.coverageUnavailable

	l.window = resolveTokenomicsWindow(factoryRoot)
	l.dispatchPool, l.poolDeclared = resolveTokenomicsDispatchPool(factoryRoot)
	l.childFloor, l.childFloorDeclared = resolveTokenomicsChildFloor(factoryRoot)
	l.parallelDisabled = resolveTokenomicsParallelDisabled(factoryRoot)
	l.provenance = readTokenomicsProvenanceTail(factoryRoot)
	return l
}

// readTokenomicsProvenanceTail returns the last few audit-log lines, oldest first. An absent or
// unreadable log yields an empty slice rather than an error: a factory whose toggle has never been
// moved has nothing to answer with, and that is not a failure of the status surface.
func readTokenomicsProvenanceTail(factoryRoot string) []string {
	tail := []string{}
	data, err := os.ReadFile(tokenomicsGateLogFile(factoryRoot))
	if err != nil {
		return tail
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			tail = append(tail, line)
		}
	}
	if len(tail) > tokenomicsProvenanceTailLines {
		tail = tail[len(tail)-tokenomicsProvenanceTailLines:]
	}
	return tail
}

// readTokenomicsCoverage counts what the factory has learned, over the digest DIRECTORY rather
// than one named formula — status knows no formula to ask about. It reuses the band report's
// enumerator (telemetry_band.go:99) so the two surfaces cannot disagree about what a digest is.
//
// It counts only JOIN-ELIGIBLE keys — those at or above the trust floor a learned read applies —
// not every aggregate. A digest full of single-run keys that no admission or band read will ever
// hit is not rising coverage; counting it would report learning the factory cannot use. The floor
// is resolved through the band's own path (bandMinRuns) so status and band agree on what a joinable
// key is.
//
// The reason is returned beside the count and is non-empty exactly when the count is zero, and the
// three zeros are kept distinct because they lead an operator to different actions: a corrupt digest
// says delete-and-rebuild, a digest of only below-floor keys says the runs are not accumulating (the
// B1 symptom), and no digest at all is the cold start that says run more steps.
func readTokenomicsCoverage(factoryRoot string) (int, string) {
	digests, unreadable := loadLearnedDigests(config.TelemetryDir(factoryRoot))
	minRuns := bandMinRuns(factoryRoot)
	joinEligible, rawTotal := 0, 0
	for _, d := range digests {
		joinEligible += tokenomics.CoverageJoinEligible(d, minRuns)
		rawTotal += tokenomics.Coverage(d)
	}
	if joinEligible > 0 {
		return joinEligible, ""
	}
	// A corrupt digest is NOT a cold start, and reporting it as one is the same collapse this whole
	// field exists to prevent — one leads an operator to run more steps, the other to delete a cache.
	// af telemetry band already degrades its state over the same directory, so a status that called
	// it a cold start would have the two surfaces disagree about one disk.
	if unreadable > 0 {
		return 0, fmt.Sprintf("%d digest file(s) under the telemetry digest directory could not be "+
			"read, so this factory's learned coverage is unknown rather than empty; delete the digest "+
			"directory and re-run af telemetry rebuild", unreadable)
	}
	// The digest holds aggregates but none a read can join: structurally cold, not a cold start.
	if rawTotal > 0 {
		return 0, tokenomicsCoverageStructurallyCold
	}
	return 0, tokenomicsCoverageUnavailable
}

// readTokenomicsInterventionTail renders the last few firings, oldest first, factory-wide.
//
// `af turn interventions` answers the same question for ONE agent since ONE turn boundary, which is
// what a grader needs and not what a self-test does: an operator asking whether anything is acting
// on their runs has no agent and no boundary in mind. So this walks the roster and takes a tail,
// while the two surfaces keep the same line grammar — mechanism, action, step — so an operator
// reading both does not have to learn two.
//
// Every failure yields an empty tail rather than an error. A roster that will not load or a record
// file that will not open is already reported by other surfaces, and a status self-test that
// refused to print because one agent's log was unreadable would withhold the seven other lines it
// could still answer with.
// The third return is the per-mechanism firing tally (#678 K9), accumulated in this SAME walk. It
// is not a second reader for the same reason the walk itself is bounded above: the tail and the
// counters are two questions about one set of records, and a factory whose two status blocks
// disagreed because they read the log at different moments would be worse than one that printed
// neither.
func readTokenomicsInterventionTail(factoryRoot string) ([]string, string, map[string]tokenomicsFirings) {
	events := []string{}
	firings := map[string]tokenomicsFirings{}

	agents, err := telemetryReportAgents(factoryRoot, "")
	if err != nil {
		return events, tokenomicsInterventionsUnavailable, firings
	}
	dir := config.TelemetryDir(factoryRoot)
	for _, agent := range agents {
		records, _, readErr := telemetry.ReadEvents(dir, telemetry.Filter{Agent: agent})
		if readErr != nil {
			continue
		}
		for _, r := range records {
			if r.Event != telemetry.EventIntervention || r.Mechanism == "" {
				continue
			}
			firings[r.Mechanism] = firings[r.Mechanism].observing(r.Action, r.Objective)
			line := fmt.Sprintf("%s %s %s: %s", r.TS, agent, r.Mechanism, r.Action)
			if r.StepID != "" {
				line += fmt.Sprintf(" [step %s]", r.StepID)
			}
			events = append(events, line)
		}
	}
	if len(events) == 0 {
		return events, tokenomicsInterventionsUnavailable, firings
	}

	// Sorted before truncation, because the roster walk visits one agent's whole log before the
	// next's — a tail taken off that order would be "the last agent's last firings", not the
	// factory's. The timestamp layout is RFC3339 with fixed-width fields, so it sorts lexically.
	sort.Strings(events)
	if len(events) > tokenomicsInterventionTailLines {
		events = events[len(events)-tokenomicsInterventionTailLines:]
	}
	return events, "", firings
}

// --- the efficiency counters (#678 K9) ----------------------------------------------------

// tokenomicsFirings is one mechanism's record tally, split by the closed action vocabulary.
//
// The split IS the design's "declined-by-reason": no intervention record carries a free-text
// reason — deliberately, the schema says so — so the reason a mechanism did not act is the action
// it took instead, and that vocabulary is closed at five.
type tokenomicsFirings struct {
	advise       int
	handoff      int
	reduceEffort int
	refuse       int
	observe      int

	forEfficiency int
	forCapacity   int
}

// observing returns the tally with one record folded in. A value receiver returning a copy, because
// the caller holds these in a map and Go will not let a map element be addressed.
func (f tokenomicsFirings) observing(action, objective string) tokenomicsFirings {
	switch action {
	case telemetry.ActionAdvise:
		f.advise++
	case telemetry.ActionHandoff:
		f.handoff++
	case telemetry.ActionReduceEffort:
		f.reduceEffort++
	case telemetry.ActionRefuse:
		f.refuse++
	case telemetry.ActionObserve:
		f.observe++
	}
	// The objective split counts firings only. An observe record is the gate saying it could not
	// judge, and attributing that to an objective would credit efficiency with a decision nobody
	// made — which is the direction #678's own experiment must never be biased in.
	if f.acted(action) {
		switch objective {
		case telemetry.ObjectiveEfficiency:
			f.forEfficiency++
		case telemetry.ObjectiveCapacity:
			f.forCapacity++
		}
	}
	return f
}

// acted separates the four actions that CHANGED what happened from the one that did not.
// ActionObserve is the armed gate admitting a launch it could not judge: nothing was done to the
// session, so counting it as a firing would report a broken gate as a working one — the precise
// inversion of what the observe record was added to make legible.
func (f tokenomicsFirings) acted(action string) bool {
	return action == telemetry.ActionAdvise || action == telemetry.ActionHandoff ||
		action == telemetry.ActionReduceEffort || action == telemetry.ActionRefuse
}

func (f tokenomicsFirings) fired() int {
	return f.advise + f.handoff + f.reduceEffort + f.refuse
}

// tokenomicsDarkFloorMultiple is Gap 12's threshold: a mechanism is only called dark once the
// factory has learned at least three times the trust floor and it has still never acted. Below
// that, silence is under-information rather than a broken actuator, and a surface that cried dark
// on a young factory would train an operator to ignore the word by the time it meant something.
const tokenomicsDarkFloorMultiple = 3

// tokenomicsCounters builds one row per mechanism, always the full roster and always in
// Mechanisms() order — a mechanism that has never fired is exactly the row an operator is looking
// for, so dropping the empty ones would hide the only rows that matter.
//
// eligible is the same number on every row by construction: it is the count of learned keys ANY
// read can join on, and no mechanism has a private digest. It is repeated per row rather than
// hoisted because `dark` is the pair (eligible, fired), and a reader checking one row should not
// have to look somewhere else for half of the verdict.
func tokenomicsCounters(firings map[string]tokenomicsFirings, eligible, minRuns int) []tokenomicsCounterJSON {
	rows := []tokenomicsCounterJSON{}
	for _, m := range tokenomics.Mechanisms() {
		f := firings[string(m)]
		row := tokenomicsCounterJSON{
			Mechanism:          string(m),
			Eligible:           eligible,
			Fired:              f.fired(),
			FiredForEfficiency: f.forEfficiency,
			FiredForCapacity:   f.forCapacity,
			Advise:             f.advise,
			Handoff:            f.handoff,
			ReduceEffort:       f.reduceEffort,
			Refuse:             f.refuse,
			Observe:            f.observe,
		}
		// minRuns 0 means no policy resolved, so there is no floor to compare against and the
		// verdict is withheld rather than guessed at a default nobody configured.
		row.Dark = minRuns > 0 && eligible >= minRuns*tokenomicsDarkFloorMultiple && row.Fired == 0
		rows = append(rows, row)
	}
	return rows
}

// --- the measurement self-test (#678 Gap 22) ----------------------------------------------

// readTokenomicsMeasurement answers whether this session's own figures could be measured at all.
//
// Every generation figure the band judges and every metric compare sums comes from one place: the
// host's transcript. When it is unreachable, or reachable but written by a host generation that
// records no usage object, every one of those figures is silently absent — and a status that
// reported a policy without reporting that would be describing a measurement apparatus nobody had
// checked was plugged in.
//
// It probes rather than parses: the three answers are existence, shape and one field, and no
// scalar is derived. A transcript that carries thinking_tokens is settled by the first record that
// does; proving their ABSENCE needs the whole file, so a host generation that omits them pays a
// full scan. That is the same bound transcriptGenerationScalars already pays on every close, and
// nothing invokes this verb on a hot path.
func readTokenomicsMeasurement() tokenomicsMeasurementJSON {
	m := tokenomicsMeasurementJSON{}

	wd, err := getWd()
	if err != nil {
		m.UnavailableBecause = "the working directory could not be resolved, so there is no " +
			"workspace to look for a transcript marker in: " + err.Error()
		return m
	}
	// The marker, never the derived path. sessionTranscriptPath falls back to composing a location
	// under the host's projects directory, which would have this verb reporting on whatever
	// transcript happens to sit there — and status has no session id to check it against.
	raw, err := os.ReadFile(filepath.Join(wd, ".runtime", "transcript_path"))
	if err != nil {
		m.UnavailableBecause = "no transcript marker in this workspace, so the host's own record " +
			"of this session cannot be located (af prime writes it at session start)"
		return m
	}
	sessionID, path, ok := strings.Cut(strings.TrimSpace(string(raw)), "\t")
	if !ok || path == "" {
		m.UnavailableBecause = "the transcript marker is in the pre-#678 bare-path format and " +
			"names no session, so a transcript found through it could belong to another session"
		return m
	}
	m.SessionID = sessionID

	f, err := os.Open(path)
	if err != nil {
		m.UnavailableBecause = "the transcript the marker names could not be opened, so every " +
			"generation figure for this session is unmeasured: " + err.Error()
		return m
	}
	defer f.Close()
	m.TranscriptReachable = true
	m.CarriesUsage, m.CarriesThinking = probeTranscriptUsageShape(f)

	if !m.CarriesUsage {
		m.UnavailableBecause = "the transcript carries no usage object, so this host generation " +
			"reports no token figures at all and every band figure will read unmeasurable"
	} else if !m.CarriesThinking {
		// Not a failure: think_tokens_est exists precisely for this host generation. It is still
		// reported, because an exact count and an estimate are not comparable across runs and a
		// reader pooling both is pooling series that mean different things.
		m.UnavailableBecause = "the transcript's usage object carries no thinking_tokens, so the " +
			"thinking figure is the derived estimate rather than the host's own count"
	}
	return m
}

// probeTranscriptUsageShape reads until it has both answers or the file ends. It uses statusline's
// line reader rather than a bufio.Scanner for deriveGenerationScalars' reason: a Scanner ABANDONS
// the rest of a file on a token above its cap, so one oversized tool result would have this report
// a usage-bearing transcript as carrying nothing.
func probeTranscriptUsageShape(r io.Reader) (carriesUsage, carriesThinking bool) {
	br := statusline.NewTranscriptReader(r)
	for {
		line, _, ok := statusline.ReadTranscriptLine(br)
		if !ok {
			return carriesUsage, carriesThinking
		}
		var rec struct {
			Message struct {
				Usage map[string]json.RawMessage `json:"usage"`
			} `json:"message"`
		}
		if line == nil || json.Unmarshal(line, &rec) != nil || len(rec.Message.Usage) == 0 {
			continue
		}
		carriesUsage = true
		// The host emits the thinking count ONLY nested under output_tokens_details
		// (telemetry_generation.go:92-94). A top-level usage.thinking_tokens is a shape nothing
		// writes, so probing for it (#679 T3) only ever matched a fabricated fixture and reported
		// a flat-only transcript as carrying a host count it does not have.
		if details, ok := rec.Message.Usage["output_tokens_details"]; ok {
			var d struct {
				ThinkingTokens *int64 `json:"thinking_tokens"`
			}
			if json.Unmarshal(details, &d) == nil && d.ThinkingTokens != nil {
				return true, true
			}
		}
	}
}

func tokenomicsAnyMechanismOn(p tokenomics.Policy) bool {
	for _, m := range tokenomics.Mechanisms() {
		if p.On(m) {
			return true
		}
	}
	return false
}

// resolveTokenomicsWindow answers with the DEFAULT profile's window, because status is a
// factory-wide surface and has no agent to ask about. The host reading is 0 for the same reason:
// no agent process is being observed here, so the only honest inputs are the declaration and the
// fallback — and the source field is what tells the operator which of the two they got.
func resolveTokenomicsWindow(factoryRoot string) tokenomics.Window {
	cfg, err := config.LoadModelsConfig(factoryRoot)
	if err != nil || cfg == nil {
		return tokenomics.ResolveWindow(nil, 0)
	}
	return tokenomics.ResolveWindow(cfg.Models[cfg.Default], 0)
}

// resolveTokenomicsDispatchPool answers with the DEFAULT profile's declared backend-pool fact
// (AF_BACKEND_POOL_TOKENS) — the operand the dispatch gate divides by (#669 THREAD-2). Like
// resolveTokenomicsWindow it speaks for the factory default, having no agent to ask; absence is
// reported as an inert backend, never as "0 tokens", so an elastic profile is not confused with a
// tiny declared pool.
func resolveTokenomicsDispatchPool(factoryRoot string) (int64, bool) {
	cfg, err := config.LoadModelsConfig(factoryRoot)
	if err != nil || cfg == nil {
		return 0, false
	}
	return config.BackendPoolTokens(cfg.Models[cfg.Default])
}

// resolveTokenomicsChildFloor answers with the DEFAULT profile's child-footprint floor and whether the
// operator declared it (as opposed to the ~50k default the accessor falls back to). Unlike the pool the
// floor is never absent once a pool is declared, so the bool reports declared-vs-default rather than
// present-vs-inert (#669 F3).
func resolveTokenomicsChildFloor(factoryRoot string) (int64, bool) {
	cfg, err := config.LoadModelsConfig(factoryRoot)
	if err != nil || cfg == nil {
		return 0, false
	}
	profile := cfg.Models[cfg.Default]
	n, ok := config.DecimalTokenCount(profile[config.EnvBackendChildFloorTokens])
	return config.BackendChildFloorTokens(profile), ok && n > 0
}

// resolveTokenomicsParallelDisabled reports whether the DEFAULT profile hard-caps sub-agent concurrency
// at one (#672/#669 F3) — the sequential-only cap the dispatch gate enforces as its last predicate.
func resolveTokenomicsParallelDisabled(factoryRoot string) bool {
	cfg, err := config.LoadModelsConfig(factoryRoot)
	if err != nil || cfg == nil {
		return false
	}
	return config.ParallelSubagentsDisabled(cfg.Models[cfg.Default])
}

func printTokenomicsStatus(factoryRoot string) error {
	l := gatherTokenomicsLiveness(factoryRoot)

	// The first line is the stable grep contract every gate verb in this package opens with, and
	// it is printed before anything downstream can fail (statusline.go:299-305).
	if l.enabled {
		fmt.Println("tokenomics: on")
	} else {
		fmt.Println("tokenomics: off")
	}

	// The window is printed either way: it comes from models.json and does not depend on the block
	// that failed to load, so withholding it would be its own kind of dishonesty.
	if l.policyUnavailable != "" {
		fmt.Printf("umbrella: unknown (unavailable — %s)\n", l.policyUnavailable)
		fmt.Println("mechanisms: unknown")
		fmt.Printf("window: %d tokens (source: %s)\n", l.window.Tokens, l.window.Source)
		fmt.Println("admission margin: unknown")
		fmt.Println("learned min runs: unknown")
	} else {
		fmt.Printf("umbrella: startup.json tokenomics.enabled=%s\n", tokenomicsDisplay(l.umbrella))
		fmt.Printf("mechanisms: %s\n", tokenomicsMechanismLine(l.policy))
		fmt.Printf("window: %d tokens (source: %s)\n", l.window.Tokens, l.window.Source)
		// The mechanisms line above always reports the dispatch gate's state, so its pool operand is
		// shown alongside — with a source label, and saying "none (inert)" for an elastic backend
		// rather than a bare 0 (#669 THREAD-2 pin 4).
		if l.poolDeclared {
			fmt.Printf("dispatch pool: %d tokens (source: declared AF_BACKEND_POOL_TOKENS)\n", l.dispatchPool)
			// The floor and cap only guard a launch once a pool is declared (the gate is otherwise inert),
			// so they ride beside the pool line and are withheld when it is inert (#669 F3).
			floorSource := "default"
			if l.childFloorDeclared {
				floorSource = "declared AF_BACKEND_CHILD_FLOOR_TOKENS"
			}
			fmt.Printf("child floor: %d tokens (source: %s)\n", l.childFloor, floorSource)
			if l.parallelDisabled {
				fmt.Println("sequential cap: on (AF_DISABLE_PARALLEL_SUBAGENTS — one sub-agent at a time; the rest wait)")
			} else {
				fmt.Println("sequential cap: off (AF_DISABLE_PARALLEL_SUBAGENTS unset — parallel sub-agents allowed)")
			}
		} else {
			fmt.Println("dispatch pool: none (inert — no AF_BACKEND_POOL_TOKENS on the default profile)")
		}
		fmt.Printf("admission margin: %d%% (a step is admitted up to %d%% projected occupancy)\n",
			l.policy.AdmissionMarginPct, 100-l.policy.AdmissionMarginPct)
		fmt.Printf("learned min runs: %d\n", l.policy.LearnedMinRuns)

		// The objective block (#678 K9). The two objectives are printed as a pair because the same
		// mechanism fires for either reason, and an operator reading one number without the other
		// cannot tell which of the two is acting on their runs.
		fmt.Printf("objective: efficiency=%s effort_level=%s thinking_share_pct=%d "+
			"repeat_read_floor=%d max_relaunches=%d\n",
			tokenomicsOnOff(l.policy.EfficiencyOn), tokenomicsDisplay(l.policy.EfficiencyEffortLevel),
			l.policy.EfficiencyThinkingSharePct, l.policy.EfficiencyRepeatReadFloor,
			l.policy.EfficiencyMaxRelaunches)
		fmt.Printf("capacity: mechanisms=%d/%d admission_margin_pct=%d window_tokens=%d\n",
			tokenomicsMechanismsOn(l.policy), len(tokenomics.Mechanisms()),
			l.policy.AdmissionMarginPct, l.window.Tokens)
	}
	if l.coverageUnavailable != "" {
		fmt.Printf("learned coverage: 0 aggregates (%s)\n", l.coverageUnavailable)
	} else {
		fmt.Printf("learned coverage: %d aggregates\n", l.coverage)
	}
	// The counters go between coverage and the intervention tail, which is the order the three
	// questions are asked in: what could have acted, what did, and what were the last few things
	// it did. `dark` is the verdict over the first two.
	if l.countersUnavailable != "" {
		fmt.Printf("counters: eligible unknown (%s)\n", l.countersUnavailable)
	} else {
		fmt.Println("counters (eligible = trusted keys any mechanism can join on):")
	}
	for _, c := range l.counters {
		// "unknown" rather than 0 when the digest could not be read: the sibling reason retracts
		// the zero on the machine surface, but a human row printing a bare 0 carries no retraction
		// of its own and reads as a measured count of nothing.
		eligible := strconv.Itoa(c.Eligible)
		if l.countersUnavailable != "" {
			eligible = "unknown"
		}
		fmt.Printf("  %s: eligible=%s fired=%d (efficiency=%d capacity=%d) "+
			"advise=%d handoff=%d reduce_effort=%d refuse=%d observe=%d%s\n",
			c.Mechanism, eligible, c.Fired, c.FiredForEfficiency, c.FiredForCapacity,
			c.Advise, c.Handoff, c.ReduceEffort, c.Refuse, c.Observe, tokenomicsDarkSuffix(c.Dark))
	}

	if l.interventionsUnavailable != "" {
		fmt.Printf("recent interventions: none (%s)\n", l.interventionsUnavailable)
	} else {
		fmt.Printf("recent interventions (last %d):\n", len(l.interventions))
		for _, line := range l.interventions {
			fmt.Printf("  %s\n", line)
		}
	}

	if len(l.inert) == 0 {
		fmt.Println("self-test: live — every gate this surface depends on is on")
	}
	for _, reason := range l.inert {
		fmt.Printf("self-test: %s\n", reason)
	}

	// A SEPARATE prefix from the liveness self-test above, and not only to keep another test's
	// negative assertion true: the two answer different questions. That one asks whether the gates
	// are on, this one whether anything could have been measured through them.
	fmt.Printf("measurement: %s\n", tokenomicsMeasurementDisplay(l.measurement))

	tail := l.provenance
	if len(tail) == 0 {
		fmt.Println("provenance: no toggle has been recorded")
	} else {
		fmt.Printf("provenance (last %d):\n", len(tail))
		for _, line := range tail {
			fmt.Printf("  %s\n", line)
		}
	}

	fmt.Printf("contract: %s\n", tokenomicsContractPointer)
	return nil
}

func tokenomicsDisplay(v string) string {
	if v == "" {
		return "default"
	}
	return v
}

func tokenomicsOnOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func tokenomicsMechanismsOn(p tokenomics.Policy) int {
	n := 0
	for _, m := range tokenomics.Mechanisms() {
		if p.On(m) {
			n++
		}
	}
	return n
}

func tokenomicsDarkSuffix(dark bool) string {
	if dark {
		return " — dark"
	}
	return ""
}

func tokenomicsMeasurementDisplay(m tokenomicsMeasurementJSON) string {
	if !m.TranscriptReachable {
		return "no transcript for this session (" + m.UnavailableBecause + ")"
	}
	line := fmt.Sprintf("transcript reachable, usage=%s thinking_tokens=%s",
		tokenomicsOnOff(m.CarriesUsage), tokenomicsOnOff(m.CarriesThinking))
	if m.UnavailableBecause != "" {
		line += " (" + m.UnavailableBecause + ")"
	}
	return line
}

func tokenomicsMechanismLine(p tokenomics.Policy) string {
	var parts []string
	for _, m := range tokenomics.Mechanisms() {
		state := "off"
		if p.On(m) {
			state = "on"
		}
		parts = append(parts, string(m)+"="+state)
	}
	return strings.Join(parts, " ")
}

// --- the machine-readable surface ---------------------------------------------------------

// tokenomicsStatusJSON is the state DTO, following telemetry_json.go rather than af step current:
// `state` rides on EVERY payload including success, because the whole point of this document is
// which of several healthy-LOOKING states the factory is in, and a consumer needs the summary
// verdict every time rather than only on failures.
//
// No field is omitempty, anywhere in this file. A degraded state is always a difference in VALUE;
// if degradation removed keys, the shape would vary with the state and a consumer could not write
// one parser for it.
type tokenomicsStatusJSON struct {
	V       int                  `json:"v"`
	State   string               `json:"state"`
	Enabled bool                 `json:"enabled"`
	Policy  tokenomicsPolicyJSON `json:"policy"`
	Window  tokenomicsWindowJSON `json:"window"`
	// InertBecause is the "say why, do not print zeros" field, and it carries EVERY cause rather
	// than the first one found.
	InertBecause        []string                    `json:"inert_because"`
	LearnedCoverage     tokenomicsCoverageJSON      `json:"learned_coverage"`
	RecentInterventions tokenomicsInterventionsJSON `json:"recent_interventions"`
	// Counters is one row per mechanism, always the full roster (#678 K9). It is a sibling of
	// policy rather than a field on it because policy says what this factory would do and these
	// say what it has done, and the dark-actuator failure is exactly the two disagreeing.
	Counters                   []tokenomicsCounterJSON   `json:"counters"`
	CountersUnavailableBecause string                    `json:"counters_unavailable_because"`
	Measurement                tokenomicsMeasurementJSON `json:"measurement"`
	// Provenance is the audit-log tail, oldest first. It rides on the machine-readable payload as
	// well as the human one because "who turned it back on" is a question a console asks too, and
	// answering it only in a terminal would make the console's account of the same factory
	// strictly poorer.
	Provenance []string `json:"provenance"`
	Contract   string   `json:"contract"`
}

type tokenomicsPolicyJSON struct {
	Umbrella   string                    `json:"umbrella"`
	Mechanisms []tokenomicsMechanismJSON `json:"mechanisms"`
	// Efficiency is the eighth switch and deliberately NOT a seventh entry in Mechanisms: every
	// member of that vocabulary answers "does this step fit its window?", and efficiency answers
	// "is this step generating more than its work needs?". A consumer walking the mechanism list
	// to render a capacity posture must not pick it up.
	Efficiency         tokenomicsEfficiencyJSON `json:"efficiency"`
	AdmissionMarginPct int                      `json:"admission_margin_pct"`
	LearnedMinRuns     int                      `json:"learned_min_runs"`
	// UnavailableBecause is non-empty exactly when the four fields above are meaningless — the same
	// idiom the coverage and interventions blocks use, applied to the one input that can fail to
	// load. Without it a consumer reads margin 0 and five mechanisms on and cannot tell that from a
	// factory an operator actually configured that way.
	UnavailableBecause string `json:"unavailable_because"`
}

type tokenomicsMechanismJSON struct {
	Name string `json:"name"`
	On   bool   `json:"on"`
}

// tokenomicsEfficiencyJSON is the objective block: the switch and the four operands it decides
// with, exactly as ResolvePolicy clamped them. The operands ride beside the switch because
// "efficiency=on" alone says nothing about what it will do — a thinking share of 80% and one of 5%
// are different policies wearing the same word.
type tokenomicsEfficiencyJSON struct {
	On               bool   `json:"on"`
	EffortLevel      string `json:"effort_level"`
	ThinkingSharePct int    `json:"thinking_share_pct"`
	RepeatReadFloor  int    `json:"repeat_read_floor"`
	MaxRelaunches    int    `json:"max_relaunches"`
}

// tokenomicsCounterJSON is one mechanism's evidence. Eligible is what it could have acted on,
// Fired is what it did, and the five action legs are what it did instead — the closed vocabulary
// standing in for a reason string the record schema deliberately does not carry.
//
// Dark is the verdict over the first two: enough learned data to act on, and never once acted.
type tokenomicsCounterJSON struct {
	Mechanism          string `json:"mechanism"`
	Eligible           int    `json:"eligible"`
	Fired              int    `json:"fired"`
	FiredForEfficiency int    `json:"fired_for_efficiency"`
	FiredForCapacity   int    `json:"fired_for_capacity"`
	Advise             int    `json:"advise"`
	Handoff            int    `json:"handoff"`
	ReduceEffort       int    `json:"reduce_effort"`
	Refuse             int    `json:"refuse"`
	Observe            int    `json:"observe"`
	Dark               bool   `json:"dark"`
}

// tokenomicsMeasurementJSON reports on the apparatus rather than on the factory: whether this
// session's own figures could be read at all. Three answers because they fail independently and
// lead to different actions — a missing transcript is a launch problem, a missing usage object is a
// host generation, and a missing thinking_tokens is a figure that will be an estimate.
type tokenomicsMeasurementJSON struct {
	SessionID           string `json:"session_id"`
	TranscriptReachable bool   `json:"transcript_reachable"`
	CarriesUsage        bool   `json:"carries_usage"`
	CarriesThinking     bool   `json:"carries_thinking"`
	UnavailableBecause  string `json:"unavailable_because"`
}

type tokenomicsWindowJSON struct {
	Tokens int64  `json:"tokens"`
	Source string `json:"source"`
}

type tokenomicsCoverageJSON struct {
	Aggregates         int    `json:"aggregates"`
	UnavailableBecause string `json:"unavailable_because"`
}

type tokenomicsInterventionsJSON struct {
	Events             []string `json:"events"`
	UnavailableBecause string   `json:"unavailable_because"`
}

// tokenomicsJSONErrorDoc is the infrastructure-failure envelope. It keeps its own narrow shape
// rather than emitting a mostly-zero status document: a payload full of zeroed policy and a zero
// window would be a claim about a factory this process could not even locate.
type tokenomicsJSONErrorDoc struct {
	V     int    `json:"v"`
	State string `json:"state"`
	Error string `json:"error"`
}

func emitTokenomicsStatusJSON(factoryRoot string) error {
	l := gatherTokenomicsLiveness(factoryRoot)

	// "default" is a claim about what the file says. It is the right claim for an absent key, which
	// the loader fills, and the wrong one for a file that did not load at all.
	umbrella := tokenomicsDisplay(l.umbrella)
	if l.policyUnavailable != "" {
		umbrella = "unknown"
	}

	doc := tokenomicsStatusJSON{
		V:       tokenomicsSchemaVersion,
		State:   tokenomicsStateOK,
		Enabled: l.enabled,
		Policy: tokenomicsPolicyJSON{
			Umbrella:   umbrella,
			Mechanisms: []tokenomicsMechanismJSON{},
			Efficiency: tokenomicsEfficiencyJSON{
				On:               l.policy.EfficiencyOn,
				EffortLevel:      l.policy.EfficiencyEffortLevel,
				ThinkingSharePct: l.policy.EfficiencyThinkingSharePct,
				RepeatReadFloor:  l.policy.EfficiencyRepeatReadFloor,
				MaxRelaunches:    l.policy.EfficiencyMaxRelaunches,
			},
			AdmissionMarginPct: l.policy.AdmissionMarginPct,
			LearnedMinRuns:     l.policy.LearnedMinRuns,
			UnavailableBecause: l.policyUnavailable,
		},
		Counters:                   l.counters,
		CountersUnavailableBecause: l.countersUnavailable,
		Measurement:                l.measurement,
		Window:                     tokenomicsWindowJSON{Tokens: l.window.Tokens, Source: l.window.Source},
		InertBecause:               l.inert,
		LearnedCoverage: tokenomicsCoverageJSON{
			Aggregates:         l.coverage,
			UnavailableBecause: l.coverageUnavailable,
		},
		RecentInterventions: tokenomicsInterventionsJSON{
			// An empty list, never null: a consumer ranges over this, and null is a different
			// shape from "nothing to report".
			Events:             l.interventions,
			UnavailableBecause: l.interventionsUnavailable,
		},
		Provenance: l.provenance,
		Contract:   tokenomicsContractPointer,
	}
	for _, m := range tokenomics.Mechanisms() {
		doc.Policy.Mechanisms = append(doc.Policy.Mechanisms,
			tokenomicsMechanismJSON{Name: string(m), On: l.policy.On(m)})
	}
	if len(l.inert) > 0 {
		doc.State = tokenomicsStateDegraded
	}
	return emitTokenomicsJSONDocument(doc)
}

// emitTokenomicsJSONError reports an infrastructure failure as DATA and returns nil, so a consumer
// never sees a non-zero exit paired with an empty stdout.
func emitTokenomicsJSONError(e error) error {
	return emitTokenomicsJSONDocument(tokenomicsJSONErrorDoc{
		V:     tokenomicsSchemaVersion,
		State: tokenomicsStateError,
		Error: e.Error(),
	})
}

// emitTokenomicsJSONDocument emits one compact document and a newline.
//
// It writes to os.Stdout rather than through the cobra output seam, and that is not a stylistic
// choice — it was measured. The seam resolves to the ROOT command's writer when this command has
// none, and several test files in this package redirect rootCmd's writer to a bytes.Buffer and
// never restore it. Written through the seam, every assertion in tokenomics_test.go passed when
// run alone and failed under `go test ./internal/cmd/` with "unexpected end of JSON input",
// because the payload had gone into another test's stale buffer. emitTelemetryJSONDocument
// carries the same note for the same reason.
func emitTokenomicsJSONDocument(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Println(`{"v":1,"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Println(string(data))
	return nil
}
