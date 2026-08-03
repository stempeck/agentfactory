package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

var telemetryCmd = &cobra.Command{
	Use:   "telemetry [on|off|status|report|usage]",
	Short: "Toggle telemetry, show its status, or report per-step timing and token usage",
	Long: `Toggle telemetry recording and export on or off, show current status,
render the local per-step timing table, or query the backend for token usage.

  af telemetry on|off               switch factory-wide recording
  af telemetry status               gate state, config, and export posture
  af telemetry report               per-step latency for every agent
  af telemetry report --agent NAME  limit the table to one agent
  af telemetry report --instance ID limit the table to one formula instance
  af telemetry report --export      drain the local backlog to the backend first
  af telemetry usage                token usage and session metrics from the backend
  af telemetry usage --agent NAME   limit the query to one agent
  af telemetry usage --instance ID  limit the query to one formula instance

Timing comes from local records; usage comes from the backend, so usage is the
one verb that needs a reachable endpoint. It always exits 0 — read .state.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTelemetry,
}

func init() {
	telemetryCmd.Flags().String("instance", "", "Limit the report to one formula instance")
	telemetryCmd.Flags().String("agent", "", "Limit the report to one agent")
	telemetryCmd.Flags().Bool("export", false, "Drain the local backlog to the configured backend before rendering")
	telemetryCmd.Flags().Bool("json", false, "Emit machine-readable JSON instead of the human table")
	rootCmd.AddCommand(telemetryCmd)
}

// telemetryGateFile is the factory-level state file. Absent ⇒ off; it is deliberately
// NEVER seeded by af install --init, matching quality and the improvement hook rather
// than fidelity's seeded-on exception. Creating nothing IS the off default.
func telemetryGateFile(factoryRoot string) string {
	return filepath.Join(factoryRoot, ".agentfactory", ".telemetry-gate")
}

// telemetryFactoryEnabled reports whether the factory-level gate file reads "on".
// Anything else — absent, unreadable, "off", or a near-miss like "onn" — is off.
func telemetryFactoryEnabled(factoryRoot string) bool {
	data, err := os.ReadFile(telemetryGateFile(factoryRoot))
	return err == nil && strings.TrimSpace(string(data)) == "on"
}

// runTelemetry resolves the factory root through the one invoker seam, never through a
// root walk of its own. Inside a worktree those two answers differ — the seam follows
// the .factory-root redirect to the outer factory, while a nearest-marker walk stops at
// the worktree — and telemetry read through the wrong one silently yields no data for
// exactly the dispatched agents this feature exists to observe.
func runTelemetry(cmd *cobra.Command, args []string) error {
	// Read --json before anything can fail, so the machine-readable surface can honor its
	// "always exit 0, branch on state" contract even when the factory root cannot be resolved.
	// The human path keeps its non-zero exits.
	jsonOut, _ := cmd.Flags().GetBool("json")
	// usage belongs to that surface whether or not the flag is set: it has no human rendering, so
	// there is no reader for whom a non-zero exit is the better answer. Without this the contract
	// holds everywhere except the two failures that happen BEFORE dispatch, which is precisely
	// where a consumer is least able to tell an empty result from a broken one.
	usageOut := len(args) > 0 && args[0] == "usage"

	cwd, err := os.Getwd()
	if err != nil {
		if usageOut {
			// usage keeps its own DTO shape here rather than borrowing the generic error envelope,
			// which carries a state outside usage's closed enum and a different key set.
			return emitTelemetryUsageUnresolved(err)
		}
		if jsonOut {
			return emitTelemetryJSONError(err)
		}
		return err
	}
	factoryRoot, err := resolveInvokerRoot(cwd)
	if err != nil {
		// Reported rather than downgraded to the cwd-resolved root, unlike dispatch status:
		// reading telemetry from the wrong root yields an empty, healthy-looking answer for a
		// factory that has data, which is the exact failure this surface exists to prevent.
		if usageOut {
			// usage keeps its own DTO shape here rather than borrowing the generic error envelope,
			// which carries a state outside usage's closed enum and a different key set.
			return emitTelemetryUsageUnresolved(err)
		}
		if jsonOut {
			return emitTelemetryJSONError(err)
		}
		return err
	}

	gateFile := telemetryGateFile(factoryRoot)

	// status and report have both a human and a machine-readable form, and this is where --json
	// picks between them; usage has only the machine-readable one and routes through the switch
	// below, which is why it is absent here. on and off stay human-only, and the console can never
	// invoke them.
	if jsonOut {
		if len(args) == 0 || args[0] == "status" {
			return emitTelemetryStateJSON(factoryRoot)
		}
		if len(args) > 0 && args[0] == "report" {
			return emitTelemetryReportJSON(cmd, factoryRoot)
		}
	}

	if len(args) == 0 || args[0] == "status" {
		return printTelemetryStatus(factoryRoot)
	}

	switch args[0] {
	case "on":
		if err := os.WriteFile(gateFile, []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("enabling telemetry: %w", err)
		}
		if _, statErr := os.Stat(config.TelemetryConfigPath(factoryRoot)); os.IsNotExist(statErr) {
			fmt.Println("telemetry: on (no telemetry.json found — step timing will be recorded locally; run quickstart.sh with telemetry or create .agentfactory/telemetry.json to export)")
		} else {
			fmt.Println("telemetry: on")
		}
	case "off":
		if err := os.WriteFile(gateFile, []byte("off\n"), 0644); err != nil {
			return fmt.Errorf("disabling telemetry: %w", err)
		}
		fmt.Println("telemetry: off")
	case "report":
		return runTelemetryReport(cmd, factoryRoot)
	case "usage":
		return runTelemetryUsage(cmd, factoryRoot)
	default:
		return fmt.Errorf("usage: af telemetry [on|off|status|report|usage]")
	}

	return nil
}

// runTelemetryReport renders the local per-step table, optionally draining the backlog first.
//
// The table is deliberately not gated: records already on disk stay readable after an operator
// switches telemetry off, and refusing to show them would make "disable mid-formula" a loss of
// data rather than a loss of further visibility.
func runTelemetryReport(cmd *cobra.Command, factoryRoot string) error {
	agentFilter, _ := cmd.Flags().GetString("agent")
	instanceFilter, _ := cmd.Flags().GetString("instance")
	exportNow, _ := cmd.Flags().GetBool("export")

	if exportNow {
		if err := exportTelemetryBacklog(factoryRoot, agentFilter); err != nil {
			// Reported, never fatal: the operator asked for a table, and an unreachable
			// backend is not a reason to withhold the data that is already local.
			fmt.Fprintf(os.Stderr, "warning: telemetry export failed: %v\n", err)
		}
	}

	table, err := formatTelemetryReport(factoryRoot, agentFilter, instanceFilter, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Print(table)
	return nil
}

// exportTelemetryBacklog drains every selected agent's backlog. Unlike the export inside af
// done this one is unbounded in batches, because the operator asked for it explicitly and it is
// not on a hook path.
func exportTelemetryBacklog(factoryRoot, agentFilter string) error {
	cfg, err := config.LoadTelemetryConfig(factoryRoot)
	if err != nil {
		return err
	}
	if cfg.Endpoint == "" {
		return fmt.Errorf("no endpoint configured in .agentfactory/telemetry.json")
	}
	agents, err := telemetryReportAgents(factoryRoot, agentFilter)
	if err != nil {
		return err
	}
	// Resolved once, before the fan-out: every agent shares the same credential, and reading the
	// secret file per agent would multiply the failure surface without changing the result. The
	// error is returned rather than warned in place so the caller owns the reporting decision;
	// ADR-007 keeps the verb itself exiting 0, so runTelemetryReport prints it and still renders
	// the table.
	resolved, err := derefTelemetryHeaders(factoryRoot, *cfg)
	if err != nil {
		return err
	}
	total := 0
	for _, agent := range agents {
		sent, drainErr := drainTelemetryFully(factoryRoot, agent, resolved)
		total += sent
		if drainErr != nil {
			return drainErr
		}
	}
	fmt.Printf("exported %d records\n", total)
	return nil
}

// telemetryReportAgents decides whose records to read. Reading refuses an empty agent name — a
// per-agent log has no meaning without one — so a report with no --agent enumerates the roster
// the way af prime fans out from the factory root, sorted so that two runs over the same data
// describe it in the same order.
func telemetryReportAgents(factoryRoot, agentFilter string) ([]string, error) {
	if agentFilter != "" {
		// The name becomes a path segment under the records directory, exactly as it does on
		// the write side — which validates for this reason. An unchecked name here reads a
		// traversed path instead of erroring.
		if err := config.ValidateAgentName(agentFilter); err != nil {
			return nil, fmt.Errorf("--agent: %w", err)
		}
		return []string{agentFilter}, nil
	}
	cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(factoryRoot))
	if err != nil {
		return nil, fmt.Errorf("loading agents config: %w", err)
	}
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// telemetryReportRow is one rendered line: one step of one formula instance.
type telemetryReportRow struct {
	agent, step, status, duration, started, model, verbMS string
}

// formatTelemetryReport builds the whole table as a string so the renderer can be exercised
// without a terminal, following the dispatch-status formatter it is modelled on.
//
// now is passed in rather than read, because an open step's duration is elapsed-so-far and a
// formatter that read the clock itself could not be tested for determinism.
func formatTelemetryReport(factoryRoot, agentFilter, instanceFilter string, now time.Time) (string, error) {
	agents, err := telemetryReportAgents(factoryRoot, agentFilter)
	if err != nil {
		return "", err
	}

	var (
		rows  []telemetryReportRow
		stats telemetry.ReadStats
	)
	for _, agent := range agents {
		records, agentStats, readErr := telemetry.ReadEvents(config.TelemetryDir(factoryRoot),
			telemetry.Filter{Agent: agent, InstanceID: instanceFilter})
		if readErr != nil {
			return "", fmt.Errorf("reading %s step records: %w", agent, readErr)
		}
		stats.Malformed += agentStats.Malformed
		stats.Dropped += agentStats.Dropped
		stats.DroppedUnexported += agentStats.DroppedUnexported
		rows = append(rows, telemetryReportRows(agent, records, now)...)
	}

	var buf bytes.Buffer

	// Reported before the empty check, not after it. A log whose every line is corrupt renders
	// no rows, and "no records yet" is exactly the wrong thing to tell an operator whose records
	// exist but cannot be read.
	lossNote := ""
	if stats.Malformed > 0 || stats.Dropped > 0 {
		lossNote = fmt.Sprintf("%d unreadable lines skipped; %d records dropped (%d never reached a backend).\n",
			stats.Malformed, stats.Dropped, stats.DroppedUnexported)
	}

	if len(rows) == 0 {
		fmt.Fprintln(&buf, "No step records yet. Enable telemetry with 'af telemetry on' and run a formula.")
		fmt.Fprint(&buf, lossNote)
		return buf.String(), nil
	}

	fmt.Fprintln(&buf)
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tSTEP\tSTATUS\tDURATION\tSTARTED\tMODEL\tVERB_MS")
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.agent, r.step, r.status, r.duration, r.started, dashIfEmpty(r.model), r.verbMS)
	}
	w.Flush()

	fmt.Fprintln(&buf)
	fmt.Fprintln(&buf, "Latency only. Token and cost figures live in the telemetry backend; af records step windows, never tokens.")
	fmt.Fprint(&buf, lossNote)
	return buf.String(), nil
}

// telemetryReportRows pairs one agent's start and end records into rows.
//
// The pairing is done here rather than by reusing the window join because the join yields no
// window at all for a step that never ended — inventing an end would put a fabricated duration
// into a backend. That is exactly the step an operator most needs to see, so the report tracks
// it as an open row with elapsed-so-far.
func telemetryReportRows(agent string, records []telemetry.StepEvent, now time.Time) []telemetryReportRow {
	type key struct{ instance, step string }
	index := map[key]int{}
	rows := make([]telemetryReportRow, 0, len(records))

	for _, r := range records {
		k := key{r.InstanceID, r.StepID}
		switch r.Event {
		case telemetry.EventStepStart:
			if _, seen := index[k]; seen {
				// A re-prime. Records arrive oldest-first, so the row already open for this
				// step was created from the earliest start — the one the window join uses.
				continue
			}
			index[k] = len(rows)
			rows = append(rows, telemetryReportRow{
				agent:    agent,
				step:     telemetryStepLabel(r),
				status:   "open",
				duration: elapsedSinceRecord(r.TS, now),
				started:  startedLabel(r.TS),
				model:    r.Model,
				verbMS:   strconv.Itoa(r.VerbMS),
			})
		case telemetry.EventStepEnd:
			i, seen := index[k]
			if !seen {
				// Telemetry switched on mid-formula: the close was recorded but the start
				// never was. Shown with an unknown start rather than dropped.
				i = len(rows)
				index[k] = i
				rows = append(rows, telemetryReportRow{
					agent: agent, step: telemetryStepLabel(r), started: "-",
				})
			}
			rows[i].status = r.Status
			if rows[i].status == "" {
				rows[i].status = telemetry.StatusClosed
			}
			rows[i].duration = formatDurationMS(r.DurationMS)
			if r.Model != "" {
				rows[i].model = r.Model
			}
			rows[i].verbMS = strconv.Itoa(r.VerbMS)
		}
	}
	return rows
}

// telemetryStepLabel names a step the way an operator thinks of it. The id is the fallback
// rather than the default: "which step is slow" is a question about the work, not about a bead.
func telemetryStepLabel(r telemetry.StepEvent) string {
	if r.StepTitle != "" {
		return r.StepTitle
	}
	return r.StepID
}

func formatDurationMS(ms int) string {
	return (time.Duration(ms) * time.Millisecond).String()
}

func elapsedSinceRecord(ts string, now time.Time) string {
	began, err := time.Parse(telemetry.TimestampLayout, ts)
	if err != nil {
		return "-"
	}
	elapsed := now.Sub(began)
	if elapsed < 0 {
		return "-"
	}
	return elapsed.Round(time.Second).String()
}

// startedLabel degrades the same way elapsedSinceRecord does — echoing an unparseable value
// into a column of timestamps invites it to be read as one.
func startedLabel(ts string) string {
	began, err := time.Parse(telemetry.TimestampLayout, ts)
	if err != nil {
		return "-"
	}
	return began.Format(time.RFC3339)
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printTelemetryStatus reports the layered truth: the gate first, then the config
// beneath it. Quality and fidelity decorate their "on" branch with a stale-lock
// warning; telemetry has no lock file, so it reports the config layer instead.
//
// Header values are secret references, so neither a value nor a header name is ever
// printed — only how many are configured.
func printTelemetryStatus(factoryRoot string) error {
	if !telemetryFactoryEnabled(factoryRoot) {
		fmt.Println("telemetry: off")
		return nil
	}
	fmt.Println("telemetry: on")

	if _, err := os.Stat(config.TelemetryConfigPath(factoryRoot)); os.IsNotExist(err) {
		fmt.Println("config: none (.agentfactory/telemetry.json absent — step records stay local)")
		return nil
	}

	cfg, err := config.LoadTelemetryConfig(factoryRoot)
	if err != nil {
		return fmt.Errorf("telemetry: config invalid (%w): telemetry disabled for this run", err)
	}

	if cfg.Endpoint == "" {
		fmt.Printf("config: .agentfactory/telemetry.json (no endpoint set — step records stay local, %d configured headers)\n", len(cfg.Headers))
		return nil
	}
	fmt.Printf("config: .agentfactory/telemetry.json (endpoint %s, %d configured headers)\n", cfg.Endpoint, len(cfg.Headers))

	// The reachability layer. Without it an operator looking at an empty dashboard cannot tell
	// "no data yet" from "no data can ever arrive" — and the three ways this feature can be dark
	// are each invisible where they happen: the export warning goes to an autonomous agent's tmux
	// pane, the session-side failure happens inside a subprocess this binary never sees, and a
	// provisioning warning scrolls past once in the middle of a long bootstrap.
	//
	// Credentials are resolved first, because probing with an unresolved reference would report an
	// authentication failure for what is really a configuration problem. A resolution failure is
	// itself the answer, so it is printed rather than returned.
	// The underlying error names the offending header key, which is right for the export path's
	// warning but not here: this surface reports header COUNTS, never header identity, and a test
	// pins that. So the cause is stated in the operator's terms without echoing the name.
	resolved, err := derefTelemetryHeaders(factoryRoot, *cfg)
	if err != nil {
		fmt.Printf("endpoint: not probed — %s\n", telemetrySecretCause(err))
		return nil
	}
	for _, r := range telemetry.Probe(resolved) {
		fmt.Printf("endpoint: %s\n", r.Summary())
	}
	return nil
}
