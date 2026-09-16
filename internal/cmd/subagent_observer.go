package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/mail"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// af subagent-observe is #668 K18's interpose leg: the PostToolUse sub-agent-tool hook that carries
// the dispatch gate's refusal forward to the launch the gate could not reach.
//
// As of #673 item 1 it computes NOTHING. It reads the breadcrumb af dispatch-admit wrote when it
// refused (dispatch_admit.go's dispatchLastRefusal) and substitutes that record's own figures into a
// sentence. That is the whole of AC-1: exactly one component computes the dispatch capacity verdict,
// and it is the gate. Until #673 this file summed a second one from the session's occupancy against
// its window — a different operand answering a different question, free to disagree with the gate
// about whether the backend was full, and disagreeing invisibly because nothing compared them.
//
// It fires AFTER the sub-agent has run, which bounds what it can be. It cannot stop the launch it
// observed and it does not try — ADR-007 makes "never block" a rule rather than a preference, and
// ux.md:44 says the same thing from the other side: an async advisory cannot stop an in-flight
// action. Its only lever is the NEXT launch, and the measured spacing is what makes that lever real:
// three Agent launches on one instrumented run went out 8-12 minutes apart, so counsel that lands
// after the first arrives long before the second.
//
// Delivery is an urgent self-addressed bead, the ADR-007 letter, plus a same-loop additionalContext
// nudge. Both, for containment.go:330's reason: the bead is durable and the nudge is immediate, and
// an agent that never checks its mail before the next launch still sees one of them.
var subagentObserveCmd = &cobra.Command{
	Use:   "subagent-observe",
	Short: "Relay a refusal the dispatch-admit gate recorded to the session that earned it (PostToolUse hook).",
	Long: `Subagent-observe intercepts Claude Code's PostToolUse hook on the sub-agent tool (named
Agent on current Claude Code, Task on older builds). It computes no capacity verdict of its own.
When the tokenomics dispatch mechanism is on and the af dispatch-admit gate has recorded a refusal
within the last fan-out latch window, it delivers one urgent self-addressed TOKENOMICS_DISPATCH bead
restating the figures that refusal recorded, counselling that further sub-agents be launched one at
a time, and records the firing as an intervention. A session the gate never refused hears nothing
from it. It never blocks and always exits 0 (ADR-007).`,
	RunE: runSubagentObserveCmd,
}

func init() {
	rootCmd.AddCommand(subagentObserveCmd)
}

// fanOutLatchTTL is how long an observed fan-out holds the watchdog off. It is the measured
// sub-agent launch spacing from design-doc.md's K18 row — three Agent launches went out 8-12 minutes
// apart on a real run — rounded up to the next quarter hour, because the wait this counsel asks for
// is exactly that spacing repeated and a latch shorter than one gap suppresses nothing.
//
// It is a ceiling on the suppression, not a promise of a wait. armInterventionLatch never extends a
// live latch, so a session that fans out again inside the window stays on its original deadline and
// the watchdog gets the agent back on schedule whatever the mechanism does next.
const fanOutLatchTTL = 15 * time.Minute

// subagentObservePayload is the subset of the PostToolUse hook JSON this command reads. There is no
// tool_input here and there must not be: the tool this observes is Task, whose input is the whole
// prompt an operator or agent wrote, and this feature's arithmetic never reads content (AC-2).
type subagentObservePayload struct {
	ToolName string `json:"tool_name"`
	Cwd      string `json:"cwd"`
}

func runSubagentObserveCmd(cmd *cobra.Command, _ []string) error {
	p, ok := readSubagentObservePayloadFromStdin()
	if !ok {
		return nil
	}
	if p.Cwd == "" {
		if wd, err := getWd(); err == nil {
			p.Cwd = wd
		}
	}
	return runSubagentObserveCore(cmd.Context(), cmd.OutOrStdout(), p)
}

func readSubagentObservePayloadFromStdin() (subagentObservePayload, bool) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return subagentObservePayload{}, false
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return subagentObservePayload{}, false
	}
	var p subagentObservePayload
	if err := json.NewDecoder(os.Stdin).Decode(&p); err != nil {
		return subagentObservePayload{}, false
	}
	return p, true
}

// runSubagentObserveCore is the testable core, and it returns nil on EVERY path. Every resolution
// below can fail on a perfectly healthy host — an agent working outside a factory, a session with no
// snapshot yet, a factory whose config will not load — and each of those is a reason to say nothing,
// never a reason to fail a hook.
func runSubagentObserveCore(ctx context.Context, out io.Writer, p subagentObservePayload) error {
	if !isSubagentTool(p.ToolName) || p.Cwd == "" {
		return nil
	}
	factoryRoot, err := resolveInvokerRoot(p.Cwd)
	if err != nil {
		return nil
	}
	agent, err := resolveAgentName(p.Cwd, factoryRoot)
	if err != nil || agent == "" {
		return nil
	}
	startupCfg, err := config.LoadStartupConfig(factoryRoot)
	if err != nil {
		return nil
	}
	policy := tokenomics.ResolvePolicy(
		tokenomicsFactoryEnabled(factoryRoot) && startupCfg.Tokenomics.Enabled != "off",
		startupCfg.Tokenomics)
	// Asked BEFORE the reading, unlike the pure Advisories path, and for the reason
	// tokenomics_admission.go states for the hot verbs: this runs once per Task completion in every
	// factory, and a factory that has never turned the mechanism on should not pay a snapshot read
	// and a breadcrumb read to be told nothing. Until #673 it was PURELY a cost decision, because the
	// pure predicate downstream re-asked the same question and deleting this line changed nothing that
	// fired. It is now load-bearing as well: the gate that writes the breadcrumb is itself
	// policy-gated, so a factory with the mechanism off writes no new ones, but a breadcrumb left in
	// the minutes before an operator turned it off is still inside its freshness window. This line is
	// what makes turning the mechanism off take effect on THIS hook immediately rather than one latch
	// window later.
	if !policy.On(tokenomics.MechanismDispatch) {
		return nil
	}

	now := time.Now()

	// The counsel is the gate's, not this hook's. No breadcrumb, a breadcrumb this binary cannot read,
	// or one older than a single fan-out window all mean the same thing — the gate has not refused
	// this session recently — and all three lead here, to silence.
	//
	// fanOutLatchTTL is the freshness window because it is already the episode window: a refusal older
	// than the latch it would arm describes an episode that has closed, and relaying it would counsel
	// a session about a wall it is no longer standing at.
	refusal, ok := readLastRefusal(p.Cwd)
	if !ok || sinceNotBefore(now, refusal.TS) >= fanOutLatchTTL {
		return nil
	}
	text := dispatchRefusalRelay(refusal)
	if text == "" {
		return nil
	}

	// Read only now that the hook is certainly firing. Before #673 it had to come first, because the
	// trigger was computed FROM it; the demotion turns it into record-keeping for a decision already
	// made, and one stat of a small JSON file is a cheaper question than a snapshot read for the
	// overwhelmingly common answer of "the gate never refused this session".
	reading := stepContextReading(factoryRoot, p.Cwd, agent, startupCfg.Recovery, now)

	// The K17 latch IS the episode discriminator, and reusing it here rather than adding a second
	// marker is the point: a fan-out is many Task completions, one advisory. armInterventionLatch
	// returns false when a latch is already live (recovery.go:555-560), so the second completion of
	// the same episode is silent — and the same call has already declared the wait the counsel asks
	// for, which is what keeps the watchdog from reading a serialized fan-out as a stall.
	//
	// This is the ONLY site that arms it for MechanismDispatch. refuseLaunch writes the breadcrumb and
	// deliberately does not arm, so one refusal followed by many completions is still one episode.
	if !armInterventionLatch(factoryRoot, agent, string(tokenomics.MechanismDispatch), fanOutLatchTTL, now) {
		return nil
	}

	emitSubagentContext(out, text)
	// The subject grammar is ux.md:52's: TOKENOMICS_<MECHANISM>, upper-cased from the closed
	// vocabulary so no operator- or agent-supplied string can reach a bead subject.
	if err := sendSubagentMail(p.Cwd, agent, "TOKENOMICS_"+strings.ToUpper(string(tokenomics.MechanismDispatch)), text); err != nil {
		// Observable rather than swallowed, containment.go:340's rule: a counsel channel that has
		// quietly stopped delivering looks exactly like a factory that never needed counsel. Written
		// here rather than through failObservable, whose line is unconditionally prefixed
		// "containment-check:" — an operator grepping stderr for #386 boundary trouble should not
		// find tokenomics mail failures filed under it.
		fmt.Fprintf(os.Stderr, "subagent-observe: dispatch counsel send failed: %v\n", err)
	}

	ctx = withVerbTelemetry(ctx, verbTelemetry{
		verb: "subagent-observe", agent: agent, start: now,
		enabled: telemetryFactoryEnabled(factoryRoot),
	})
	instanceID := readHookedFormulaID(p.Cwd)
	stepID, _, _ := strings.Cut(readStepPrimed(p.Cwd), ":")
	recordIntervention(ctx, factoryRoot, p.Cwd, agent, instanceID, func(ev *telemetry.StepEvent) {
		ev.Formula = instanceFormulaName(ctx, p.Cwd, instanceID)
		ev.StepID = stepID
		ev.Mechanism = string(tokenomics.MechanismDispatch)
		ev.Action = telemetry.ActionAdvise
		attachStepOccupancy(ev, reading, factoryRoot, now)
	})
	return nil
}

// instanceFormulaName resolves the label the rest of the record surface files interventions under,
// or "" when it cannot. Every other recording site already has it in hand — af prime primed the
// step, af done just closed one — and this hook has neither, only the instance id.
//
// The store read is why this is called from the firing path and nowhere else. Task completions are
// frequent; firings are not (one per fan-out episode, bounded by the K17 latch's TTL), and this path
// already opens a store to deliver the bead. An unreadable store costs the label and nothing more:
// the record still carries instance_id, which is what the label is derived FROM, so a report can
// recover it — losing the join entirely is what would not be acceptable.
func instanceFormulaName(ctx context.Context, workDir, instanceID string) string {
	if instanceID == "" {
		return ""
	}
	store, err := newIssueStore(workDir, os.Getenv("AF_ACTOR"))
	if err != nil {
		return ""
	}
	iss, err := store.Get(ctx, instanceID)
	if err != nil {
		return ""
	}
	return telemetryFormulaName(iss.Title)
}

// emitSubagentContext writes the same-loop nudge. PostToolUse's additionalContext is the ONLY field
// this command may ever write: `continue: false` and a permissionDecision other than "allow" both
// stop the agent, and TestInterposeNonBlocking reads this output back to pin that neither appears.
func emitSubagentContext(out io.Writer, body string) {
	emitHookContext(out, "PostToolUse", body)
}

// sendSubagentMail is the ADR-009 mail-send seam, sendContainmentMail's sibling. It is a separate
// var rather than a reuse because the two carry different subjects to different readers, and a test
// that swapped one would silence the other.
var sendSubagentMail = func(wd, role, subject, body string) error {
	msg := mail.NewMessage(role, role, subject, body)
	msg.Priority = issuestore.PriorityUrgent
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
