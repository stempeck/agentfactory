package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// This file is the machine-readable half of af telemetry, and it deliberately does NOT reuse the
// human formatters' layering.
//
// Those formatters report a layered truth: the gate is answered first and, when it reads off, the
// configuration beneath it is never opened and the backend is never contacted. That layering is
// right for a terminal — an operator who has switched telemetry off does not need three more
// lines — and a test pins it. It is wrong for a consumer. Under it, "off but installed" and "off
// but never installed" are the same answer, and reachability is only ever known in the fully
// healthy case, so the states an operator most needs named are exactly the ones that cannot be
// represented.
//
// Here every axis is computed every time. The gate never gates the configuration, and neither
// gates the probe. Degradation travels as data — in the payload's own fields — so these verbs
// exit 0 in every state and a consumer branches on `state` rather than on an exit code, matching
// af dispatch status --json. The three axes are independent objects rather than one collapsed
// enum, which is what makes their whole combination space representable by construction.
const (
	telemetryStateOK       = "ok"
	telemetryStateDegraded = "degraded"
	telemetryStateError    = "error"
)

// telemetryStateJSON is the State DTO. Carrying `state` on a SUCCESS payload diverges from the
// house idiom, where `.state` discriminates errors only and success payloads omit it (see
// dispatchStatusJSON and formulaShowOutput). The divergence is deliberate and matches af step
// current, the other command whose subject is a genuine state machine: here the whole point of
// the payload is which of several healthy-looking-but-dark states the factory is in, so a
// consumer needs the summary verdict on every response, not just failing ones.
//
// No field is omitempty, anywhere in this file. A schema snapshot pins a shape; if degradation
// removed keys, the shape would vary with the state and the pin would mean nothing. Degradation
// is always a difference in VALUE.
type telemetryStateJSON struct {
	V         int    `json:"v"`
	State     string `json:"state"`
	Installed struct {
		Present  bool   `json:"present"`
		Valid    bool   `json:"valid"`
		Endpoint string `json:"endpoint"`
		// The count, never the names and never the values. A header value is a credential or a
		// reference to one, and an error message is the easiest place to spill one into a log.
		HeaderCount int `json:"header_count"`
	} `json:"installed"`
	Recording struct {
		Enabled bool `json:"enabled"`
	} `json:"recording"`
	Backend struct {
		Probed  bool                  `json:"probed"`
		Signals []telemetrySignalJSON `json:"signals"`
	} `json:"backend"`
	UnprobedCause string `json:"unprobed_cause"`
}

// telemetrySignalJSON reuses ProbeResult's own rendering rather than re-describing a verdict, so
// the console and the terminal can never disagree about the same backend. The probed URL is
// deliberately absent: it is derived from the endpoint the payload already carries, and repeating
// it per signal would multiply the places an endpoint can leak into a screenshot.
type telemetrySignalJSON struct {
	Label   string `json:"label"`
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Summary string `json:"summary"`
}

// telemetryReportJSON is the Report DTO. Its rows are projected from the raw step records rather
// than from the table renderer's row type, whose every field is a pre-formatted display string —
// a consumer needs durations it can compare, not durations it must re-parse.
type telemetryReportJSON struct {
	V     int                      `json:"v"`
	State string                   `json:"state"`
	Rows  []telemetryReportRowJSON `json:"rows"`
	Stats telemetryReadStatsJSON   `json:"stats"`
}

type telemetryReportRowJSON struct {
	Agent string `json:"agent"`
	Step  string `json:"step"`
	// "open" is a DERIVED status with no on-disk constant: the record schema knows only closed,
	// skipped and gate-waiting. It marks a step whose start was recorded and whose end was not —
	// the row an operator most needs to see — so do not "correct" it against the event constants.
	Status string `json:"status"`
	// Elapsed-so-far for an open step, the recorded duration for a closed one.
	DurationMS int `json:"duration_ms"`
	// Empty rather than the table's "-" sentinel: a dash is a thing to print, not a thing to
	// parse, and copying it here would make the consumer strip display syntax back out.
	Started    string `json:"started"`
	Model      string `json:"model"`
	VerbMS     int    `json:"verb_ms"`
	InstanceID string `json:"instance_id"`
}

type telemetryReadStatsJSON struct {
	Malformed         int `json:"malformed"`
	Dropped           int `json:"dropped"`
	DroppedUnexported int `json:"dropped_unexported"`
}

// telemetryJSONError is the infrastructure-failure envelope. It carries `v` so a consumer can
// version-check before it branches, on every payload it can receive rather than on most of them.
type telemetryJSONError struct {
	V     int    `json:"v"`
	State string `json:"state"`
	Error string `json:"error"`
}

// telemetryStateDTO answers all three axes unconditionally.
//
// The order below is not a layering: no axis is allowed to suppress another. The gate is read
// first only because it is the cheapest, and its value is never consulted again.
func telemetryStateDTO(factoryRoot string) telemetryStateJSON {
	st := telemetryStateJSON{V: telemetry.SchemaVersion}
	// A nil slice marshals to null, which a consumer cannot iterate. Empty is a real answer here
	// and must look like one.
	st.Backend.Signals = make([]telemetrySignalJSON, 0)

	st.Recording.Enabled = telemetryFactoryEnabled(factoryRoot)

	// The loader cannot answer this on its own: an absent telemetry.json yields an empty config
	// and a nil error, which is byte-identical to a present-but-empty one. Distinguishing "never
	// installed" from "installed and blank" is half of what this surface exists for, so presence
	// is established by its own stat, exactly as the human formatter does it.
	_, statErr := os.Stat(config.TelemetryConfigPath(factoryRoot))
	st.Installed.Present = !os.IsNotExist(statErr)

	cfg, loadErr := config.LoadTelemetryConfig(factoryRoot)
	switch {
	case !st.Installed.Present:
		// Nothing to validate. valid stays false so a consumer branching on it alone never reads
		// "the configuration is fine" for a factory that has none.
	case loadErr != nil:
		// The loader's error text names the offending header key, so it is not carried into any
		// field of this payload. valid:false with present:true is the whole report — it says the
		// file exists and could not be used, which is the actionable half, without turning an
		// error string into a way for a header name to reach a browser.
	default:
		st.Installed.Valid = true
		st.Installed.Endpoint = cfg.Endpoint
		st.Installed.HeaderCount = len(cfg.Headers)
	}

	// The backend axis, computed regardless of the gate. Probing only when recording is on would
	// leave reachability unknowable in precisely the dark combinations this payload exists to
	// name — an operator who has switched recording off still needs to know whether turning it
	// back on would achieve anything.
	//
	// Credentials are resolved first: probing with an unresolved reference reports an
	// authentication failure for what is really a configuration problem, and the resulting error
	// names the header. A resolution failure is itself the answer, stated in the operator's terms
	// by a helper whose contract is never to name the header.
	if st.Installed.Valid && st.Installed.Endpoint != "" {
		resolved, derefErr := derefTelemetryHeaders(factoryRoot, *cfg)
		if derefErr != nil {
			st.UnprobedCause = telemetrySecretCause(derefErr)
		} else {
			// Set before the call, not inferred from the result: the probe returns nothing at all
			// for an unreachable-by-configuration endpoint, and "we asked and got no signals" must
			// stay distinguishable from "we never asked".
			st.Backend.Probed = true
			for _, r := range telemetry.Probe(resolved) {
				st.Backend.Signals = append(st.Backend.Signals, telemetrySignalJSON{
					Label:   r.Label,
					OK:      r.OK(),
					Status:  r.Status,
					Summary: r.Summary(),
				})
			}
		}
	}

	st.State = telemetryStateVerdict(st)
	return st
}

// telemetryStateVerdict summarises the axes without replacing them. It is a pure function of the
// payload's own fields, so the summary can never disagree with the detail it summarises.
func telemetryStateVerdict(st telemetryStateJSON) string {
	if !st.Recording.Enabled ||
		!st.Installed.Present || !st.Installed.Valid || st.Installed.Endpoint == "" ||
		!st.Backend.Probed {
		return telemetryStateDegraded
	}
	for _, s := range st.Backend.Signals {
		if !s.OK {
			return telemetryStateDegraded
		}
	}
	return telemetryStateOK
}

// telemetryReportDTO projects the local step records.
//
// now is a parameter rather than a clock read, for the same reason the table renderer takes one:
// an open step's duration is elapsed-so-far, and a producer that read the clock itself could not
// be tested for determinism.
func telemetryReportDTO(factoryRoot, agentFilter, instanceFilter string, now time.Time) (telemetryReportJSON, error) {
	out := telemetryReportJSON{V: telemetry.SchemaVersion}
	out.Rows = make([]telemetryReportRowJSON, 0)

	// Reused rather than re-derived: this is where an --agent value is validated before it
	// becomes a path segment, and where the roster is sorted so two runs over the same data
	// describe it in the same order. A second enumeration here would be a second, unguarded way
	// to turn a name into a file path.
	agents, err := telemetryReportAgents(factoryRoot, agentFilter)
	if err != nil {
		return out, err
	}

	var stats telemetry.ReadStats
	for _, agent := range agents {
		records, agentStats, readErr := telemetry.ReadEvents(config.TelemetryDir(factoryRoot),
			telemetry.Filter{Agent: agent, InstanceID: instanceFilter})
		if readErr != nil {
			return out, fmt.Errorf("reading %s step records: %w", agent, readErr)
		}
		stats.Malformed += agentStats.Malformed
		stats.Dropped += agentStats.Dropped
		stats.DroppedUnexported += agentStats.DroppedUnexported
		out.Rows = append(out.Rows, telemetryJSONRows(agent, records, now)...)
	}

	out.Stats = telemetryReadStatsJSON{
		Malformed:         stats.Malformed,
		Dropped:           stats.Dropped,
		DroppedUnexported: stats.DroppedUnexported,
	}
	// Accounted for independently of whether any rows survived. A log whose every line is corrupt
	// produces no rows, and reporting that as a plain success would tell an operator "no records
	// yet" about records that exist and cannot be read.
	if stats.Malformed > 0 || stats.Dropped > 0 {
		out.State = telemetryStateDegraded
	} else {
		out.State = telemetryStateOK
	}
	return out, nil
}

// telemetryJSONRows pairs one agent's start and end records.
//
// The pairing is re-derived here rather than shared with the table renderer because that renderer
// produces display strings — a duration already rounded and suffixed, a timestamp already
// formatted — and this payload owes its consumer comparable numbers. The BEHAVIOUR is deliberately
// identical: an unfinished step becomes an open row with elapsed-so-far rather than being dropped,
// a re-primed step does not become a second row, and a close whose start was never recorded is
// shown with an unknown start rather than discarded.
func telemetryJSONRows(agent string, records []telemetry.StepEvent, now time.Time) []telemetryReportRowJSON {
	type key struct{ instance, step string }
	index := map[key]int{}
	rows := make([]telemetryReportRowJSON, 0, len(records))

	for _, r := range records {
		k := key{r.InstanceID, r.StepID}
		switch r.Event {
		case telemetry.EventStepStart:
			if _, seen := index[k]; seen {
				// A re-prime. Records arrive oldest-first, so the row already open for this step
				// was created from the earliest start — the one the window join uses.
				continue
			}
			index[k] = len(rows)
			rows = append(rows, telemetryReportRowJSON{
				Agent:      agent,
				Step:       telemetryStepLabel(r),
				Status:     "open",
				DurationMS: elapsedMSSinceRecord(r.TS, now),
				Started:    startedRFC3339(r.TS),
				Model:      r.Model,
				VerbMS:     r.VerbMS,
				InstanceID: r.InstanceID,
			})
		case telemetry.EventStepEnd:
			i, seen := index[k]
			if !seen {
				// Telemetry switched on mid-formula: the close was recorded but the start never
				// was. Shown with an unknown start rather than dropped.
				i = len(rows)
				index[k] = i
				rows = append(rows, telemetryReportRowJSON{
					Agent:      agent,
					Step:       telemetryStepLabel(r),
					InstanceID: r.InstanceID,
				})
			}
			rows[i].Status = r.Status
			if rows[i].Status == "" {
				rows[i].Status = telemetry.StatusClosed
			}
			rows[i].DurationMS = r.DurationMS
			if r.Model != "" {
				rows[i].Model = r.Model
			}
			rows[i].VerbMS = r.VerbMS
		}
	}
	return rows
}

// elapsedMSSinceRecord degrades to zero rather than to a sentinel, because this field is a
// number. An unparseable or future timestamp is already visible through `started`, which is empty
// in exactly those cases.
func elapsedMSSinceRecord(ts string, now time.Time) int {
	began, err := time.Parse(telemetry.TimestampLayout, ts)
	if err != nil {
		return 0
	}
	elapsed := now.Sub(began)
	if elapsed < 0 {
		return 0
	}
	return int(elapsed.Milliseconds())
}

func startedRFC3339(ts string) string {
	began, err := time.Parse(telemetry.TimestampLayout, ts)
	if err != nil {
		return ""
	}
	return began.Format(time.RFC3339)
}

func emitTelemetryStateJSON(factoryRoot string) error {
	return emitTelemetryJSONDocument(telemetryStateDTO(factoryRoot))
}

// emitTelemetryReportJSON honours --agent and --instance, which are the two filters the console
// relays. It deliberately does NOT honour --export: draining the backlog prints a line of its own
// to stdout, which would put prose in front of the payload and produce output no parser accepts.
// This is a read surface; the export verb remains available on the human path.
func emitTelemetryReportJSON(cmd *cobra.Command, factoryRoot string) error {
	agentFilter, _ := cmd.Flags().GetString("agent")
	instanceFilter, _ := cmd.Flags().GetString("instance")

	dto, err := telemetryReportDTO(factoryRoot, agentFilter, instanceFilter, time.Now().UTC())
	if err != nil {
		return emitTelemetryJSONError(err)
	}
	return emitTelemetryJSONDocument(dto)
}

// emitTelemetryJSONError reports an infrastructure failure as data and returns nil, so a consumer
// never sees a non-zero exit with an empty stdout.
func emitTelemetryJSONError(e error) error {
	data, err := json.Marshal(telemetryJSONError{
		V:     telemetry.SchemaVersion,
		State: telemetryStateError,
		Error: e.Error(),
	})
	if err != nil {
		fmt.Println(`{"v":1,"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Println(string(data))
	return nil
}

// emitTelemetryJSONDocument emits one compact document and a newline.
//
// It writes to os.Stdout rather than through the cobra output seam, matching every other line of
// this command. The seam would be wrong here for a concrete reason: it resolves to the ROOT
// command's writer when this command has none, and tests elsewhere in this package redirect that
// writer and never restore it — so a payload written through the seam would vanish into a stale
// buffer during a full-package run while still passing when the test is run alone.
func emitTelemetryJSONDocument(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return emitTelemetryJSONError(err)
	}
	fmt.Println(string(data))
	return nil
}
