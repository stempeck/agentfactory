// Package telemetryview is the read projection of the `af telemetry` JSON surface for the web
// module.
//
// It mirrors the dispatch reader's idiom: one seam to the af binary (the three Telemetry* methods,
// read through the exec wrapper — never by importing internal/…), mirror structs re-declared rather
// than imported, and a branch on the payload's .state rather than on the process exit code (af read
// commands always exit 0 and encode failure as data).
//
// Two things here depart from that idiom deliberately, and both are load-bearing:
//
//  1. A payload carrying state:"error" is RELAYED, not converted into a Go error. The dispatch
//     reader turns state=="error" into an error, which its handler renders as a 502. For telemetry
//     that would be wrong: the error envelope is how the CLI reports a factory condition an
//     operator needs to read ("not in an agentfactory workspace"), and it is a committed,
//     fixture-backed shape. Degradation is data. Only a failure of the RELAY ITSELF is an error.
//
//  2. The read path validates only the ENVELOPE — the schema version and the state's membership in
//     the route's closed enum — and then returns the CLI's ORIGINAL bytes. It deliberately does NOT
//     decode into the mirror structs below.
//
//     The design asks for two things that pull apart: the payload relayed byte-faithfully, and a
//     mirror package that cannot drift silently. Relaying the original bytes means a field added by
//     a later root version still reaches the browser instead of being dropped on re-encode. The
//     mirror earns its keep in the CONTRACT TESTS, which round-trip it against the committed
//     goldens — that is where drift is caught, and it is caught at build time rather than by
//     rejecting a live payload. Decoding the full mirror here as well would buy nothing and would
//     risk exactly the failure it appears to prevent: a mirror stricter than its producer turns a
//     healthy response into a relay failure.
package telemetryview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SchemaVersion is the integer schema version this mirror understands. It matches the root's
// telemetry.SchemaVersion. Evolution is additive, so a reader accepts the version it knows and
// refuses one it does not — a version it cannot interpret is skew, not data.
const SchemaVersion = 1

// The status/report state enum, closed at three values (mirrors internal/cmd/telemetry_json.go:31-33).
// The fixture corpus states it as binding: "state is ok, degraded, or error. Nothing else."
const (
	StateOK       = "ok"
	StateDegraded = "degraded"
	StateError    = "error"
)

// The usage state enum, closed at five values and DISJOINT from the one above beyond "ok"
// (mirrors internal/cmd/telemetry_usage.go:35-39). It is a separate enum because it carries only
// conditions that actually prevented or degraded a backend query; recording-off is deliberately not
// among them, because the gate never short-circuits the query.
const (
	UsageOK                 = "ok"
	UsageNotInstalled       = "not_installed"
	UsageBackendDown        = "backend_down"
	UsageCredentialRejected = "credential_rejected"
	UsageQueryFailed        = "query_failed"
)

// ErrRelay reports that the relay itself failed, as distinct from any verdict the telemetry surface
// reached. Every caller must keep the two apart: a relay failure means nothing was measured, so
// rendering it as a telemetry state would assert a measurement that never happened — the silent
// fallback this whole surface exists to remove, reintroduced one layer up.
var ErrRelay = errors.New("telemetry relay failed")

// Reader yields the raw stdout of the three `af telemetry` reads. exec.Wrapper satisfies it; tests
// inject a hermetic fake. This is the only seam between this package and the af binary — the
// service never spawns a process itself.
type Reader interface {
	TelemetryStatus(ctx context.Context) (string, error)
	TelemetryReport(ctx context.Context, agent, instance string) (string, error)
	TelemetryUsage(ctx context.Context, agent, instance string) (string, error)
}

// --- mirror structs (re-declared for the READ projection, NOT imported from internal/cmd) ---
//
// No field anywhere below carries ,omitempty. That is the root family's rule and the reason for it
// transfers exactly: a schema snapshot pins a SHAPE, so if degradation removed keys the shape would
// vary with the state and the pin would mean nothing. Degradation is always a difference in VALUE.

// StateDTO mirrors internal/cmd/telemetry_json.go telemetryStateJSON. The three axes stay
// independent — installed, recording, and backend are separately readable — because the states an
// operator most needs to distinguish ("recording is on but nothing is installed") are precisely the
// ones a single collapsed health field would erase.
type StateDTO struct {
	V         int    `json:"v"`
	State     string `json:"state"`
	Installed struct {
		Present bool `json:"present"`
		Valid   bool `json:"valid"`
		// Endpoint is display-safe: the root strips userinfo before carrying it.
		Endpoint string `json:"endpoint"`
		// HeaderCount is a COUNT, never an identity. The DTO reports how many credentials are
		// configured and never which.
		HeaderCount int `json:"header_count"`
	} `json:"installed"`
	Recording struct {
		Enabled bool `json:"enabled"`
	} `json:"recording"`
	Backend struct {
		Probed  bool        `json:"probed"`
		Signals []SignalDTO `json:"signals"`
	} `json:"backend"`
	// UnprobedCause says why no probe ran, in the operator's terms. It never names a header.
	UnprobedCause string `json:"unprobed_cause"`
}

// SignalDTO mirrors internal/cmd/telemetry_json.go telemetrySignalJSON. One entry per signal rather
// than one verdict for the backend, because "reachable on traces, 404 on logs" is a real and common
// state that a single boolean would render as simply "up".
type SignalDTO struct {
	Label   string `json:"label"`
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Summary string `json:"summary"`
}

// ReportDTO mirrors internal/cmd/telemetry_json.go telemetryReportJSON.
type ReportDTO struct {
	V     int            `json:"v"`
	State string         `json:"state"`
	Rows  []ReportRowDTO `json:"rows"`
	// Stats is why corruption is never rendered as "no records yet": zero rows WITH non-zero
	// malformed/dropped counts is a different fact from zero rows alone.
	Stats ReadStatsDTO `json:"stats"`
}

// ReportRowDTO mirrors internal/cmd/telemetry_json.go telemetryReportRowJSON.
type ReportRowDTO struct {
	Agent string `json:"agent"`
	Step  string `json:"step"`
	// Status includes the DERIVED value "open" for a step that started and has not ended.
	Status     string `json:"status"`
	DurationMS int    `json:"duration_ms"`
	// Started is RFC3339, or "" when the recorded value could not be parsed.
	Started    string `json:"started"`
	Model      string `json:"model"`
	VerbMS     int    `json:"verb_ms"`
	InstanceID string `json:"instance_id"`
}

// ReadStatsDTO mirrors internal/cmd/telemetry_json.go telemetryReadStatsJSON.
type ReadStatsDTO struct {
	Malformed         int `json:"malformed"`
	Dropped           int `json:"dropped"`
	DroppedUnexported int `json:"dropped_unexported"`
}

// ErrorDTO mirrors internal/cmd/telemetry_json.go telemetryJSONError.
//
// It is a SEPARATE type, not folded into StateDTO the way the dispatch reader folds its own
// envelope. The corpus proves why: status-error-infra.json has exactly three keys {v,state,error},
// while the other status goldens carry installed/recording/backend. Folding them would add an
// "error" key to all of those on re-encode, and since ,omitempty is banned family-wide that
// difference cannot be hidden.
type ErrorDTO struct {
	V     int    `json:"v"`
	State string `json:"state"`
	Error string `json:"error"`
}

// UsageDTO mirrors internal/cmd/telemetry_usage.go telemetryUsageJSON.
type UsageDTO struct {
	V     int    `json:"v"`
	State string `json:"state"`
	// Endpoint is display-safe: userinfo is stripped root-side before it is carried.
	Endpoint string          `json:"endpoint"`
	Window   UsageWindowDTO  `json:"window"`
	Filters  UsageFiltersDTO `json:"filters"`
	Tokens   UsageTokensDTO  `json:"tokens"`
	Metrics  UsageMetricsDTO `json:"metrics"`
	// Cause states why no query ran. It never names a header.
	Cause string `json:"cause"`
}

// UsageWindowDTO mirrors telemetryUsageWindowJSON. The window is echoed so the choice is visible
// rather than assumed.
type UsageWindowDTO struct {
	StartUS int64 `json:"start_us"`
	EndUS   int64 `json:"end_us"`
	Limit   int   `json:"limit"`
}

// UsageFiltersDTO mirrors telemetryUsageFilterJSON — the filters actually applied, echoed back so a
// panel cannot show rows under a filter the query did not use.
type UsageFiltersDTO struct {
	Agent    string `json:"agent"`
	Instance string `json:"instance"`
}

// UsageTokensDTO mirrors telemetryUsageTokensJSON. Total and Truncated travel together because a
// row cap that is not visible in the payload is truncation presented as totality.
type UsageTokensDTO struct {
	State     string        `json:"state"`
	Rows      []UsageRowDTO `json:"rows"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
	// Detail carries the backend's own error class, bounded and redacted root-side.
	Detail string `json:"detail"`
}

// UsageRowDTO mirrors telemetryUsageRowJSON — the shipped view's output shape, column for column.
type UsageRowDTO struct {
	FormulaRun   string `json:"formula_run"`
	Agent        string `json:"agent"`
	Model        string `json:"model"`
	StepBucket   string `json:"step_bucket"`
	StepOrder    Count  `json:"step_order"`
	Requests     Count  `json:"requests"`
	InputTokens  Count  `json:"input_tokens"`
	OutputTokens Count  `json:"output_tokens"`
	TotalTokens  Count  `json:"total_tokens"`
}

// UsageMetricsDTO mirrors telemetryUsageMetricsJSON. AtUS is stated separately because the metrics
// half does NOT honour the window above — reporting a window it ignored would be the same kind of
// lie as a silent row cap.
type UsageMetricsDTO struct {
	State  string              `json:"state"`
	Rows   []UsageMetricRowDTO `json:"rows"`
	AtUS   int64               `json:"at_us"`
	Detail string              `json:"detail"`
}

// UsageMetricRowDTO mirrors telemetryUsageMetricRowJSON. Labels is what makes one row different
// from the next; the root fills it from an ALLOWLIST, never a copy of the series' labels, because
// the raw set carries identity fields and this payload is rendered in a browser.
type UsageMetricRowDTO struct {
	Metric   string            `json:"metric"`
	Agent    string            `json:"agent"`
	Instance string            `json:"instance"`
	Labels   map[string]string `json:"labels"`
	Value    string            `json:"value"`
}

// Count mirrors internal/cmd/telemetry_usage.go usageCount, including its tolerance.
//
// The tolerance is the point, not an accident: the root accepts a quoted string or a float because
// the backend's wire type "is not something to assume", and a plain int field would turn a single
// 2.0 into an unmarshal error for the WHOLE response — a healthy backend rendered as a broken one by
// a JSON encoding detail. A mirror STRICTER than its producer would re-introduce exactly that bug
// one layer up, where it would surface as a relay failure.
type Count int

func (c *Count) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*c = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", s)
	}
	*c = Count(int(f))
	return nil
}

func (c Count) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(c))), nil }

// --- the not-installed payloads ---
//
// These are what the routes render when the console has no telemetry reader wired. Each is its OWN
// route's DTO family with a complete key set and a visibly not-ok state.
//
// The alternative — a 500, which every other Convention-A handler in the server does on a nil seam —
// would render a state the operator needs to SEE as a fault of the console. The other alternative,
// an empty-but-ok payload, is worse: it is byte-identical to a healthy factory with no data yet.

// notInstalledCause names the CONSOLE's own fault. It deliberately does not claim the factory has no
// telemetry configured, because that measurement was never taken — the reader was simply never
// wired. Asserting the stronger claim would be a fabrication of exactly the kind this surface exists
// to eliminate.
const notInstalledCause = "the console's telemetry reader is not configured"

// NotInstalledStatus is the status payload for an unwired seam: nothing read, nothing probed, and a
// cause naming why.
func NotInstalledStatus() StateDTO {
	var dto StateDTO
	dto.V = SchemaVersion
	dto.State = StateDegraded
	dto.Backend.Signals = []SignalDTO{} // an array, never null — consumers iterate without a guard
	dto.UnprobedCause = notInstalledCause
	return dto
}

// NotInstalledReport is the report payload for an unwired seam.
//
// state is "degraded", not "ok". The report enum has no "not installed" value — it is closed at
// {ok, degraded, error} and the corpus pins that — so "ok" with an empty row set was the tempting
// choice, and it is precisely the defect this feature exists to remove: it is byte-identical to a
// healthy factory that simply has no records yet, leaving the operator no way to tell "nothing
// happened" from "nothing is wired". "degraded" costs this route a second meaning for that value
// (its other one is "records were lost"), which is a documentation burden rather than a correctness
// one, and it is the only in-enum value that is visibly not-ok.
func NotInstalledReport() ReportDTO {
	return ReportDTO{
		V:     SchemaVersion,
		State: StateDegraded,
		Rows:  []ReportRowDTO{},
	}
}

// NotInstalledUsage is the usage payload for an unwired seam. Unlike the other two this enum has the
// exact value for the situation, and the root itself takes this shape in the analogous case: it
// emits a FULL Usage DTO with not_installed plus a cause rather than the generic error envelope,
// because the envelope would put a sixth value outside a closed enum and make the key set vary with
// the state.
func NotInstalledUsage() UsageDTO {
	return UsageDTO{
		V:     SchemaVersion,
		State: UsageNotInstalled,
		Tokens: UsageTokensDTO{
			State: UsageNotInstalled,
			Rows:  []UsageRowDTO{},
		},
		Metrics: UsageMetricsDTO{
			State: UsageNotInstalled,
			Rows:  []UsageMetricRowDTO{},
		},
		Cause: notInstalledCause,
	}
}

// Service reads the telemetry surface through a Reader.
type Service struct {
	src Reader
}

// New builds a Service over the given source (production: an *exec.Wrapper).
func New(src Reader) *Service {
	return &Service{src: src}
}

// Status relays `af telemetry status --json`.
func (s *Service) Status(ctx context.Context) (json.RawMessage, error) {
	raw, err := s.src.TelemetryStatus(ctx)
	if err != nil {
		return nil, relayErr(err)
	}
	return relay(raw, func(state string) bool { return isStatusState(state) })
}

// Report relays `af telemetry report --json`. Filters are validated by the wrapper before exec; an
// empty filter means unfiltered.
func (s *Service) Report(ctx context.Context, agent, instance string) (json.RawMessage, error) {
	raw, err := s.src.TelemetryReport(ctx, agent, instance)
	if err != nil {
		return nil, relayErr(err)
	}
	return relay(raw, func(state string) bool { return isStatusState(state) })
}

// Usage relays `af telemetry usage --json`. Its state enum is the five-value one, not the three.
//
// StateError is accepted in ADDITION to those five. That is not the usage enum leaking into the
// status enum: the infra-failure envelope {v,state:"error",error} is emitted by the command layer
// BEFORE any verb-specific code runs — it is how "not in an agentfactory workspace" is reported —
// so it can precede any of the three reads. Rejecting it here would turn a designed, committed
// payload into a relay failure on exactly the path where the operator most needs to be told why.
func (s *Service) Usage(ctx context.Context, agent, instance string) (json.RawMessage, error) {
	raw, err := s.src.TelemetryUsage(ctx, agent, instance)
	if err != nil {
		return nil, relayErr(err)
	}
	return relay(raw, func(state string) bool { return isUsageState(state) || state == StateError })
}

// relay decodes just enough of the payload to be sure it IS a payload, then returns the original
// bytes.
//
// Only the envelope is checked — the schema version and the state's membership in the route's closed
// enum. Individual fields are not validated, because a field this mirror does not know about is
// additive evolution and must still reach the browser.
//
// A state outside the enum is treated as a relay failure rather than passed through. The root goes
// to the trouble of pinning both enums closed at the command boundary, so an unknown value means
// genuine skew between the af binary and this console — the failure mode where both lanes stay green
// and the console's panel is quietly wrong. Failing loudly is the intent.
func relay(raw string, stateOK func(string) bool) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: the telemetry command produced no output", ErrRelay)
	}

	var env struct {
		V     *int    `json:"v"`
		State *string `json:"state"`
	}
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return nil, fmt.Errorf("%w: the telemetry command's output could not be read as JSON", ErrRelay)
	}
	if env.V == nil {
		return nil, fmt.Errorf("%w: the payload carries no schema version", ErrRelay)
	}
	if *env.V != SchemaVersion {
		return nil, fmt.Errorf("%w: the payload's schema version is not one this console understands", ErrRelay)
	}
	if env.State == nil {
		return nil, fmt.Errorf("%w: the payload carries no state", ErrRelay)
	}
	if !stateOK(*env.State) {
		return nil, fmt.Errorf("%w: the payload's state is not one this console understands", ErrRelay)
	}
	return json.RawMessage(trimmed), nil
}

// relayErr wraps a seam failure without carrying its text.
//
// The underlying error is deliberately DISCARDED rather than wrapped for display: ExecRunner embeds
// the child's stderr in its error string, and a credential failure names the header there. Anything
// derived from it would travel into a JSON payload delivered to a browser.
func relayErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: the telemetry command could not be run", ErrRelay)
}

func isStatusState(s string) bool {
	switch s {
	case StateOK, StateDegraded, StateError:
		return true
	}
	return false
}

func isUsageState(s string) bool {
	switch s {
	case UsageOK, UsageNotInstalled, UsageBackendDown, UsageCredentialRejected, UsageQueryFailed:
		return true
	}
	return false
}
