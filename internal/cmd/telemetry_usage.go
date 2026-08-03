package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// af telemetry usage is the only component in this system that asks the telemetry backend a
// question. Everything else writes to it.
//
// Two things about this file are properties rather than habits. Filters are validated BEFORE a
// request body exists, so a rejected value is not escaped on its way into a query — no query is
// built at all. And the credential is dereferenced here, in the root module, at a call site named
// in the rule that governs dereferencing, so the set of processes that can read telemetry.auth is
// exactly what it was before this verb existed.
//
// The gate is not consulted anywhere in this path, deliberately. Recording-off is a statement about
// what will be written from now on; it says nothing about what the backend already holds, and
// hiding historical data behind it would repeat one level down the state-hiding defect this whole
// surface exists to fix — the same reason af telemetry report stays readable after af telemetry off.
const (
	telemetryUsageStateOK                 = "ok"
	telemetryUsageStateNotInstalled       = "not_installed"
	telemetryUsageStateBackendDown        = "backend_down"
	telemetryUsageStateCredentialRejected = "credential_rejected"
	telemetryUsageStateQueryFailed        = "query_failed"
)

const (
	telemetrySearchPath = "/_search"
	telemetryPromQLPath = "/prometheus/api/v1/query"
	// telemetrySchemaPath is the logs-stream schema endpoint the tokens pre-flight
	// queries before ever sending the doomed _search — documented live at
	// internal/telemetry/testdata/openobserve-v0.91.3/README.md:85-90 and captured at
	// internal/telemetry/testdata/recorded-real/logs_schema_response.json. Path only
	// (no "?type=logs") — that query string is appended separately at the call site
	// so this constant matches what a server-side handler sees on r.URL.Path.
	telemetrySchemaPath = "/streams/default/schema"
	// telemetrySeriesPath is the existence-probe endpoint for silent metrics
	// (fable-implement Step 4).
	telemetrySeriesPath = "/prometheus/api/v1/series"

	// usageErrorBodyLimit bounds a failure body that gets quoted back to an operator, matching the
	// exporter's own bound.
	usageErrorBodyLimit = 512

	// usageBodyLimit bounds a SUCCESS body, and is a separate number on purpose. 512 bytes would
	// truncate every real answer — a single captured PromQL response is 11.7 KB — and a truncated
	// success decodes as a parse error, which would report a healthy backend as a failed query.
	usageBodyLimit = 8 << 20

	// usageRowLimit caps rows requested per query. It is echoed in the payload rather than applied
	// silently: a cap an operator cannot see is truncation presented as totality.
	usageRowLimit = 1000

	// usageEpochUS is the start of the queried window, and it is 1 rather than 0 for a reason found
	// by running the real thing: this backend reads a zero start_time as "unset" and answers
	// 400 "[file_list] invalid time range". One microsecond past the epoch is accepted and is
	// indistinguishable from all-time for any record this system can hold, so the window stays
	// genuinely unbounded while being a value the backend will take.
	//
	// Do not "simplify" this to 0. TestTelemetryUsage_WindowStartIsAcceptableToTheBackend fails if
	// anyone does, because no fake backend rejects it and nothing else would notice.
	usageEpochUS = 1

	// minRedactableName is the shortest header NAME worth removing from a quoted backend body. A
	// name is not a secret — it is identity this surface declines to report — so a floor is right
	// here: erasing every "id" in an error message to hide a two-letter header name would destroy
	// the diagnostic to protect nothing. Header VALUES have no such floor; see usageDetail.
	minRedactableName = 3
)

// usageCount decodes a numeric column that the backend may render as an integer, a float, or a
// quoted string.
//
// SQL aggregates are the reason. sum() and count() over this backend's columns have never been
// observed against real token data — the shipped tokens view does not currently execute here — so
// the wire type is not something to assume. A plain int field turns a single `2.0` into an
// unmarshal error for the WHOLE response, which this surface would then report as query_failed:
// a healthy backend rendered as a broken one by a JSON encoding detail.
type usageCount int

func (c *usageCount) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*c = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", s)
	}
	*c = usageCount(int(f))
	return nil
}

func (c usageCount) MarshalJSON() ([]byte, error) { return []byte(strconv.Itoa(int(c))), nil }

// telemetryUsageMetrics are the session-level series read through the PromQL family. The split is
// not a preference: the backend's query surface is stream-type-aware, and SQL _search against a
// metrics stream fails outright.
//
// A set rather than one metric, because no design text names one, and every one below was observed
// carrying the af_agent / af_formula_instance labels these filters need.
var telemetryUsageMetrics = []string{
	"claude_code_token_usage",
	"claude_code_cost_usage",
	"claude_code_session_count",
	"claude_code_active_time_total",
}

// telemetryQueryDo is the transport seam (ADR-009), request-granular on purpose.
//
// It is separate from internal/telemetry's httpDo, which is unexported and belongs to the export
// plane. The seam sits at the request rather than at the query so the status-code-to-state rule
// below stays on this side of it: a coarser seam would move that rule into the substitute and leave
// the thing actually worth testing untested.
var telemetryQueryDo = func(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// telemetryUsageJSON is the Usage DTO. No field here is omitempty, matching the rest of this
// family: a schema snapshot pins a shape, so degradation must always be a difference in VALUE and
// never in key set.
type telemetryUsageJSON struct {
	V     int    `json:"v"`
	State string `json:"state"`
	// Endpoint is display-safe: userinfo is stripped before it is carried. A configured endpoint
	// may embed credentials, and copying it verbatim publishes them.
	Endpoint string                    `json:"endpoint"`
	Window   telemetryUsageWindowJSON  `json:"window"`
	Filters  telemetryUsageFilterJSON  `json:"filters"`
	Tokens   telemetryUsageTokensJSON  `json:"tokens"`
	Metrics  telemetryUsageMetricsJSON `json:"metrics"`
	// Cause states, in the operator's terms, why no query ran. It never names a header: this
	// surface reports credential problems without identifying the credential.
	Cause string `json:"cause"`
}

// telemetryUsageWindowJSON reports the window that was actually queried. It is all-time by design —
// any narrower default would report a subset as though it were the total, which is the honesty bug
// a row cap is also required to avoid — and it is echoed so the choice is visible rather than
// assumed.
type telemetryUsageWindowJSON struct {
	StartUS int64 `json:"start_us"`
	EndUS   int64 `json:"end_us"`
	Limit   int   `json:"limit"`
}

type telemetryUsageFilterJSON struct {
	Agent    string `json:"agent"`
	Instance string `json:"instance"`
}

type telemetryUsageTokensJSON struct {
	State string                  `json:"state"`
	Rows  []telemetryUsageRowJSON `json:"rows"`
	// Total is what the backend says it matched, which is not always what it returned. Truncated
	// says so outright. A row cap that is not visible in the payload is truncation presented as
	// totality — the honesty bug this whole surface exists to remove — and the backend can also
	// answer a partial result of its own accord, which looked identical to a complete one until
	// these two fields existed.
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
	// Detail carries the backend's own error class so a failure is actionable rather than blank.
	// It is bounded and redacted before it lands here.
	Detail string `json:"detail"`
}

// telemetryUsageRowJSON is the shipped view's output shape, column for column. It is a projection
// of that query's result, never a re-shaping of it.
type telemetryUsageRowJSON struct {
	FormulaRun   string     `json:"formula_run"`
	Agent        string     `json:"agent"`
	Model        string     `json:"model"`
	StepBucket   string     `json:"step_bucket"`
	StepOrder    usageCount `json:"step_order"`
	Requests     usageCount `json:"requests"`
	InputTokens  usageCount `json:"input_tokens"`
	OutputTokens usageCount `json:"output_tokens"`
	TotalTokens  usageCount `json:"total_tokens"`
}

type telemetryUsageMetricsJSON struct {
	State string                        `json:"state"`
	Rows  []telemetryUsageMetricRowJSON `json:"rows"`
	// AtUS is the instant these values were evaluated at, and it exists because the metrics half
	// does NOT honour the window above. PromQL instant queries return a series' value at one
	// moment; the window applies to the tokens query only. Reporting a window the metrics half
	// silently ignored would be the same lie as a silent row cap, so the instant is stated.
	AtUS   int64  `json:"at_us"`
	Detail string `json:"detail"`
}

type telemetryUsageMetricRowJSON struct {
	Metric   string `json:"metric"`
	Agent    string `json:"agent"`
	Instance string `json:"instance"`
	// Labels are what makes one row different from the next. Without them a single metric returns
	// several rows for the same agent and run that differ only in value — the live backend answers
	// claude_code_token_usage with eight series for one (agent, instance) pair, split by token type
	// — and a reader cannot tell which number means what, or that they are not duplicates.
	//
	// An ALLOWLIST, not a copy. These series also carry user_email, user_id, session_id and
	// organization_id, and this payload is rendered in a browser: carrying the whole label set
	// would turn a token-count view into an identity disclosure.
	Labels map[string]string `json:"labels"`
	Value  string            `json:"value"`
}

// telemetryUsageMetricLabels is that allowlist. Each entry distinguishes series of the same metric
// for the same run; nothing here identifies a person.
var telemetryUsageMetricLabels = []string{
	"type",
	"model",
	"query_source",
	"decision",
	"language",
	"tool_name",
	"terminal_type",
	"af_worktree_id",
	"af_model_profile",
}

// runTelemetryUsage is the verb; telemetryUsageDTO below is the deref-permitted site.
//
// factoryRoot is a parameter rather than something resolved here: the caller has already resolved it
// through the one invoker seam, and a second resolver in this file would be a second answer to a
// question that must have exactly one.
func runTelemetryUsage(cmd *cobra.Command, factoryRoot string) error {
	agent, _ := cmd.Flags().GetString("agent")
	instance, _ := cmd.Flags().GetString("instance")

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Emitted as JSON on both the flagged and unflagged path. The design specifies only a
	// machine-readable form for this verb, and inventing a second human rendering would mean two
	// surfaces to keep honest where the console and the terminal currently see the same bytes.
	return emitTelemetryJSONDocument(telemetryUsageDTO(ctx, factoryRoot, agent, instance, time.Now().UTC()))
}

// telemetryUsageDTO answers with data in every case, including every failure.
//
// This is the FIFTH deref-permitted site — see the rule in telemetry_secret.go:39-57, whose
// arithmetic counts this one. The deref lives here rather than in the verb because this is the
// function that builds the requests: keeping the resolve next to the send is what makes it
// reviewable as a single act.
//
// now and ctx are parameters rather than reads for the same reason the report producer takes a
// clock: a function that read them itself could not be exercised for determinism, and the deadline
// in particular has to be composable so a test can impose a short one without the production floor
// being lowered to meet it.
func telemetryUsageDTO(ctx context.Context, factoryRoot, agent, instance string, now time.Time) telemetryUsageJSON {
	out := telemetryUsageJSON{V: telemetry.SchemaVersion}
	// Nil slices marshal to null, which a consumer cannot iterate. Empty is a real answer here and
	// has to look like one.
	out.Tokens.Rows = make([]telemetryUsageRowJSON, 0)
	out.Metrics.Rows = make([]telemetryUsageMetricRowJSON, 0)
	// Filters are deliberately NOT echoed yet. They are caller-controlled strings, and a rejected
	// one is exactly the string that must not be handed back out: this payload is rendered in a
	// browser and written to logs, so echoing a refused injection payload would give it a second
	// delivery route. They are assigned below, only after validation has accepted them.
	//
	// All time. The window is a value in the payload rather than a constant in the code because a
	// reader has to be able to tell what was actually asked — a narrower default applied silently
	// would report a subset as though it were the total.
	out.Window = telemetryUsageWindowJSON{StartUS: usageEpochUS, EndUS: now.UnixMicro(), Limit: usageRowLimit}

	cfg, err := config.LoadTelemetryConfig(factoryRoot)
	if err != nil {
		// The loader's error text names the offending header key, so it is reported in the
		// operator's terms instead of being passed through.
		out.State = telemetryUsageStateNotInstalled
		out.Cause = "the telemetry configuration could not be read"
		out.Tokens.State, out.Metrics.State = out.State, out.State
		return out
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		out.State = telemetryUsageStateNotInstalled
		out.Cause = "no endpoint is configured in .agentfactory/telemetry.json — there is nothing to query"
		out.Tokens.State, out.Metrics.State = out.State, out.State
		return out
	}
	out.Endpoint = displaySafeEndpoint(cfg.Endpoint)

	resolved, derefErr := derefTelemetryHeaders(factoryRoot, *cfg)
	if derefErr != nil {
		// Reported through the name-free helper, never by carrying derefTelemetryHeaders' own error
		// text: that text names the header key, and this payload reaches a browser.
		out.State = telemetryUsageStateCredentialRejected
		out.Cause = telemetrySecretCause(derefErr)
		out.Tokens.State, out.Metrics.State = out.State, out.State
		return out
	}

	// Validation happens here, before either request body exists. A rejected value cannot reach the
	// wire because nothing is built for it to reach the wire in.
	sql, buildErr := telemetryTokensSQL(agent, instance)
	if buildErr != nil {
		out.State = telemetryUsageStateQueryFailed
		// buildErr never quotes the rejected value — the validators are written not to — so this is
		// the shape of the rule that was broken, not the input that broke it.
		out.Cause = buildErr.Error()
		out.Tokens.State, out.Metrics.State = out.State, out.State
		return out
	}
	// Accepted, so they are safe to report. From here the payload describes a query that was
	// actually issued.
	out.Filters = telemetryUsageFilterJSON{Agent: agent, Instance: instance}

	// One budget for the whole verb, not one per request. Five sequential requests each holding
	// their own 2s-to-10s envelope would let a dead backend hold the command for the sum of them,
	// which is not a ceiling. Every request below derives from this context, so the earlier
	// deadline always wins and the total cannot exceed the envelope.
	ctx, cancel := context.WithTimeout(ctx, usageTimeout(*cfg))
	defer cancel()

	// Both halves are issued unconditionally. An operator looking at an empty panel needs to know
	// WHICH half is dark, and the first thing to fail is often the less informative one — on the
	// factory this was written against, the tokens query fails a schema check while the metrics
	// query returns live data.
	//
	// The tokens half is schema-pre-flighted first (fable-implement Step 3, Root Cause B): the
	// shipped view reads columns the real backend has never carried under any observed name
	// (internal/telemetry/testdata/recorded-real/), and OpenObserve answers an unknown column with
	// a 400 rather than NULL, so sending the doomed query converts a known, documented gap into raw
	// backend jargon on every operator surface. Checking first turns that into a named-gap sentence
	// and never sends the query at all.
	missing, preState, preDetail := missingTokenColumns(ctx, resolved)
	switch {
	case preState != telemetryUsageStateOK:
		// Transport verdicts pass through unchanged — a schema-fetch failure is exactly as
		// actionable as any other backend_down/credential_rejected/query_failed verdict, and
		// forcing it into a pre-flight-specific shape would be a second classification rule to
		// keep in sync with usageRequest's one (decisions.md D3).
		//
		// This arm deliberately does NOT fall through to queryTokens. Stated explicitly
		// because the original skip was a consequence of arm order rather than a recorded
		// call (PR #585, S4): when the schema fetch fails by transport, the _search POST
		// fails the same way and would spend the shared per-verb budget to produce a second
		// copy of the same verdict; when it fails by status, sending the doomed query is what
		// turns a documented gap into the raw backend 400 this pre-flight exists to prevent.
		// The cost is real and bounded — a backend whose schema route is broken while _search
		// is healthy loses a row it could have returned — and that is the trade accepted here.
		out.Tokens.State, out.Tokens.Detail = preState, preDetail
	case len(missing) > 0:
		out.Tokens.State = telemetryUsageStateQueryFailed
		out.Tokens.Detail = fmt.Sprintf(
			"the backend's logs stream does not carry the per-step token columns (missing: %s); "+
				"per-step token data is not available on this backend — a known gap, not a fault "+
				"in this factory or in the console",
			strings.Join(missing, ", "))
	default:
		out.Tokens = queryTokens(ctx, resolved, sql, out.Window)
	}
	out.Metrics = queryMetrics(ctx, resolved, agent, instance, now)

	out.State = worstUsageState(out.Tokens.State, out.Metrics.State)
	return out
}

// emitTelemetryUsageUnresolved answers the two failures that happen before a factory root is known.
//
// It emits a full Usage DTO rather than the generic error envelope for a reason that is easy to miss:
// the generic envelope carries state "error" and a different key set entirely, which would put a
// sixth value outside the closed enum on exactly the payloads a consumer is least able to interpret,
// and would make the key set vary with the state — the thing this family's no-omitempty rule exists
// to prevent.
func emitTelemetryUsageUnresolved(cause error) error {
	out := telemetryUsageJSON{V: telemetry.SchemaVersion, State: telemetryUsageStateNotInstalled}
	out.Tokens = telemetryUsageTokensJSON{State: telemetryUsageStateNotInstalled, Rows: make([]telemetryUsageRowJSON, 0)}
	out.Metrics = telemetryUsageMetricsJSON{State: telemetryUsageStateNotInstalled, Rows: make([]telemetryUsageMetricRowJSON, 0)}
	out.Cause = cause.Error()
	return emitTelemetryJSONDocument(out)
}

// worstUsageState reduces the two halves to the one verdict the DTO carries.
//
// Pessimistically, and in a fixed severity order. Reporting ok for a half-dark surface would be the
// state-hiding this surface exists to prevent, and the ordering is by how differently an operator
// must react: a rejected credential is not an unreachable backend, and neither is a backend that
// answered and refused the query. The per-half states stay in the payload, so this summary can
// never disagree with the detail it summarises.
func worstUsageState(states ...string) string {
	severity := map[string]int{
		telemetryUsageStateOK:                 0,
		telemetryUsageStateQueryFailed:        1,
		telemetryUsageStateBackendDown:        2,
		telemetryUsageStateCredentialRejected: 3,
		telemetryUsageStateNotInstalled:       4,
	}
	worst := telemetryUsageStateOK
	for _, s := range states {
		if severity[s] > severity[worst] {
			worst = s
		}
	}
	return worst
}

// classifyUsageStatus mirrors the discrimination the probe surface already performs, so the two
// never disagree about the same backend.
//
// The status code decides, and the body is only ever read afterwards. That order is not stylistic:
// a 404 from this backend arrives with an empty body when the org segment is missing and with plain
// text when the org is wrong, so an implementation that decoded before it classified would
// mis-report both as parse failures.
//
// Transport failures never reach here — there is no status to classify when nothing answered — so
// this takes no error parameter. The caller maps those to backend_down at the point they occur.
func classifyUsageStatus(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return telemetryUsageStateCredentialRejected
	case status/100 != 2:
		return telemetryUsageStateQueryFailed
	default:
		return telemetryUsageStateOK
	}
}

// usageRequest issues one query through the seam and returns the classification plus the body.
func usageRequest(ctx context.Context, cfg config.TelemetryConfig, method, url string, body []byte) (string, []byte, string) {
	ctx, cancel := context.WithTimeout(ctx, usageTimeout(cfg))
	defer cancel()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return telemetryUsageStateQueryFailed, nil, "the request could not be built"
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, name := range sortedHeaderNames(cfg.Headers) {
		// The same backstop the exporter and the probe carry. An unresolved reference is this
		// caller's bug rather than the backend's, and sending one would surface as an
		// authentication failure that sends an operator to check the wrong thing.
		if strings.HasPrefix(cfg.Headers[name], secretPrefix) {
			return telemetryUsageStateCredentialRejected, nil,
				"a configured credential is still an unresolved secret reference"
		}
		req.Header.Set(name, cfg.Headers[name])
	}

	resp, err := telemetryQueryDo(req)
	if err != nil {
		// The transport error is not carried into the payload: it can quote the request URL, and
		// the URL is derived from an endpoint that may embed userinfo.
		return telemetryUsageStateBackendDown, nil, "the backend could not be reached"
	}
	defer resp.Body.Close()

	state := classifyUsageStatus(resp.StatusCode)
	if state != telemetryUsageStateOK {
		// Read PAST the display bound before redacting, then truncate. Truncating first would cut a
		// credential in half exactly when a backend echoes it late in a long error body: the tail of
		// the quote survives the cut, no longer matches the whole value the redactor looks for, and
		// a prefix of the secret is published. The extra window is the longest header value, so any
		// occurrence beginning inside the displayed region is wholly present when redaction runs.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, usageErrorBodyLimit+int64(longestHeaderValue(cfg.Headers))+1))
		_, _ = io.Copy(io.Discard, resp.Body)
		return state, nil, usageDetail(resp.Status, raw, cfg.Headers)
	}

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, usageBodyLimit))
	if readErr != nil {
		return telemetryUsageStateBackendDown, nil, "the backend's answer could not be read"
	}
	return telemetryUsageStateOK, raw, ""
}

// usageDetail renders a failure so it is actionable, bounded, and carries nothing it should not.
// A backend that quotes the credential it rejected is common, and this text reaches terminals,
// logs and a browser.
func usageDetail(status string, body []byte, headers map[string]string) string {
	text := strings.TrimSpace(string(body))

	// Header NAMES go too, not just their values. This surface's standing rule is that it reports
	// credential problems without identifying the credential, and a backend that echoes the header
	// it objected to would otherwise put the name here and make that rule false. A floor applies
	// here because a name is not itself a secret — over-redacting on a two-letter name would erase
	// the backend's message to protect nothing.
	for name := range headers {
		if len(name) < minRedactableName {
			continue
		}
		text = strings.ReplaceAll(text, name, "[header]")
	}

	// Values get NO length floor, and that asymmetry is deliberate. "No credential material in any
	// output path, in any state" is absolute, so a short credential must be removed even though
	// removing it may also swallow innocent text — a mangled diagnostic is recoverable, a published
	// credential is not.
	//
	// Each value is removed in every encoding a backend might echo it in, not just verbatim. A
	// service that quotes the token it rejected renders it into ITS response format: JSON escaping
	// turns `/` into `\/` and `"` into `\"`, and a URL-echoing error percent-encodes. A byte-exact
	// match sees none of those and publishes the credential while appearing to have redacted it.
	for _, value := range headers {
		if value == "" {
			continue
		}
		for _, form := range secretEncodings(value) {
			text = strings.ReplaceAll(text, form, "[redacted]")
		}
	}
	// Redaction ran on a longer read than gets displayed, so the cut happens here — after every
	// whole-value occurrence is already gone.
	if len(text) > int(usageErrorBodyLimit) {
		text = text[:usageErrorBodyLimit]
	}
	// The cut itself can manufacture a partial secret: a value that began just before the bound is
	// now a truncated prefix that no longer matches anything the loop above looked for. Drop such a
	// tail rather than publish it.
	text = stripTrailingSecretPrefix(text, headers)
	if text == "" {
		return status
	}
	return status + ": " + text
}

// secretEncodings lists the renderings of one credential a backend might echo.
//
// Verbatim, JSON-escaped, and percent-encoded. It cannot be exhaustive — a backend is free to
// invent an encoding — which is why this is a second line of defence behind never putting the
// credential where it can be echoed at all, and why credential_rejected bodies get the extra
// treatment they do.
func secretEncodings(value string) []string {
	forms := []string{value}
	if encoded, err := json.Marshal(value); err == nil {
		if inner := strings.Trim(string(encoded), `"`); inner != value && inner != "" {
			forms = append(forms, inner)
			// Go's encoder does not escape the solidus; plenty of others do, and a Basic credential
			// is base64 so it contains them routinely. Without this the most likely re-encoding in
			// practice is the one that slips through.
			if slashed := strings.ReplaceAll(inner, "/", `\/`); slashed != inner {
				forms = append(forms, slashed)
			}
		}
	}
	if slashed := strings.ReplaceAll(value, "/", `\/`); slashed != value {
		forms = append(forms, slashed)
	}
	if escaped := url.QueryEscape(value); escaped != value {
		forms = append(forms, escaped)
	}
	if escaped := url.PathEscape(value); escaped != value {
		forms = append(forms, escaped)
	}
	return forms
}

// stripTrailingSecretPrefix removes a tail that is the beginning of a configured header value.
//
// The cut in usageDetail can manufacture one: a value that began just before the bound survives as
// a prefix that no whole-value replacement matches. It descends to a single character on purpose —
// a one-character tail of a credential is still a character of a credential, and this only ever
// removes text at the very END of an already-truncated diagnostic, so the cost of being strict here
// is close to nothing.
func stripTrailingSecretPrefix(text string, headers map[string]string) string {
	for _, value := range headers {
		for _, form := range secretEncodings(value) {
			if form == "" {
				continue
			}
			for n := len(form) - 1; n >= 1; n-- {
				if strings.HasSuffix(text, form[:n]) {
					return strings.TrimRight(text[:len(text)-n], " ") + "[truncated]"
				}
			}
		}
	}
	return text
}

func longestHeaderValue(headers map[string]string) int {
	longest := 0
	for _, v := range headers {
		if len(v) > longest {
			longest = len(v)
		}
	}
	return longest
}

// missingTokenColumns answers whether the backend's logs stream carries every column
// the shipped tokens view's billable_requests CTE reads. The required-column list is
// DERIVED from the embedded view SQL (tokensViewLogsPlaneColumns, telemetry_where.go)
// rather than hand-maintained, so the pre-flight and the step-6b interlock read one
// source of truth and cannot drift apart from each other or from the SQL that ships.
//
// The schema fetch is issued through the same usageRequest seam every other query in
// this file uses, so a transport-level failure (timeout, connection refused, 401/403)
// is classified by the existing rule, never a bespoke one (decisions.md D3) — only a
// SUCCESSFUL fetch that then enumerates the schema and finds columns absent reaches
// the "columns missing" branch below.
func missingTokenColumns(ctx context.Context, cfg config.TelemetryConfig) (missing []string, state string, detail string) {
	required, err := tokensViewLogsPlaneColumns()
	if err != nil {
		return nil, telemetryUsageStateQueryFailed, "the shipped tokens view could not be read: " + err.Error()
	}

	schemaState, raw, schemaDetail := usageRequest(ctx, cfg, http.MethodGet,
		usageURL(cfg.Endpoint, telemetrySchemaPath)+"?type=logs", nil)
	if schemaState != telemetryUsageStateOK {
		// Name the request this verdict came from, as this function's other two failure
		// returns already do ("the shipped tokens view could not be read: …" and "the
		// backend's schema answer could not be parsed"). Unprefixed, the operator is shown a
		// bare transport failure or a raw backend status line on a pane about token usage —
		// with nothing indicating it refers to a schema pre-flight they were never told
		// exists, and not to the tokens query at all.
		//
		// The separator is an em dash rather than a colon deliberately: detailBody in
		// telemetry_usage_test.go cuts on the FIRST ": " to strip the HTTP status line before
		// scanning for credential leakage, so a colon here would push "400 Bad Request" into
		// the scanned body, where "Ba" matches the fixture credential's own prefix and reports
		// a leak that never happened.
		return nil, schemaState, "could not read the backend's logs-stream schema — " + schemaDetail
	}

	var answer struct {
		Schema []struct {
			Name string `json:"name"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, telemetryUsageStateQueryFailed, "the backend's schema answer could not be parsed"
	}
	present := make(map[string]bool, len(answer.Schema))
	for _, f := range answer.Schema {
		present[f.Name] = true
	}

	for _, col := range required {
		if !present[col] {
			missing = append(missing, col)
		}
	}
	return missing, telemetryUsageStateOK, ""
}

func queryTokens(ctx context.Context, cfg config.TelemetryConfig, sql string, window telemetryUsageWindowJSON) telemetryUsageTokensJSON {
	out := telemetryUsageTokensJSON{Rows: make([]telemetryUsageRowJSON, 0)}

	body, err := json.Marshal(map[string]any{
		"query": map[string]any{
			"sql":        sql,
			"start_time": window.StartUS,
			"end_time":   window.EndUS,
			"from":       0,
			"size":       window.Limit,
		},
	})
	if err != nil {
		out.State, out.Detail = telemetryUsageStateQueryFailed, "the query could not be encoded"
		return out
	}

	state, raw, detail := usageRequest(ctx, cfg, http.MethodPost,
		usageURL(cfg.Endpoint, telemetrySearchPath), body)
	out.State, out.Detail = state, detail
	if state != telemetryUsageStateOK {
		return out
	}

	var answer struct {
		Hits  []telemetryUsageRowJSON `json:"hits"`
		Total int                     `json:"total"`
		// The backend sets this when it answered from an incomplete scan. It arrives inside a 200,
		// so nothing about the status code reveals it.
		IsPartial bool `json:"is_partial"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		out.State, out.Detail = telemetryUsageStateQueryFailed, "the backend's answer could not be parsed"
		return out
	}
	if answer.Hits != nil {
		out.Rows = answer.Hits
	}
	out.Total = answer.Total
	out.Truncated = answer.IsPartial || answer.Total > len(out.Rows)
	return out
}

func queryMetrics(ctx context.Context, cfg config.TelemetryConfig, agent, instance string, now time.Time) telemetryUsageMetricsJSON {
	out := telemetryUsageMetricsJSON{Rows: make([]telemetryUsageMetricRowJSON, 0), AtUS: now.UnixMicro()}

	// Re-validated here rather than trusted to have been validated by the tokens path that runs
	// first. The dependency is invisible, cheap to remove, and exactly the kind a later change
	// reorders without noticing; this way the label selector is safe because of code in this
	// function, not because of the order two calls happen to appear in.
	if err := assertSpliceable(agent, instance); err != nil {
		out.State, out.Detail = telemetryUsageStateQueryFailed, err.Error()
		return out
	}

	selector := promQLSelector(agent, instance)

	states := make([]string, 0, len(telemetryUsageMetrics))
	// Which metrics answered with a series, tracked per metric rather than by counting rows at the
	// end. A rename does not have to be total: one name going stale leaves the other three
	// answering, so the row set is non-empty and every check keyed on "no rows" is blind to a
	// whole category of measurement having disappeared.
	var silent []string
	for _, metric := range telemetryUsageMetrics {
		q := url.Values{}
		q.Set("query", metric+selector)
		// Evaluated at a stated instant rather than at "whenever the request arrives", so the
		// at_us the payload reports is the instant the backend actually used. PromQL takes this in
		// seconds.
		q.Set("time", strconv.FormatInt(now.Unix(), 10))
		state, raw, detail := usageRequest(ctx, cfg, http.MethodGet,
			usageURL(cfg.Endpoint, telemetryPromQLPath)+"?"+q.Encode(), nil)
		states = append(states, state)
		if state != telemetryUsageStateOK {
			if out.Detail == "" {
				out.Detail = detail
			}
			continue
		}

		var answer struct {
			Status string `json:"status"`
			// Populated only on failure, and only inside an otherwise-successful HTTP response.
			ErrorType string `json:"errorType"`
			Error     string `json:"error"`
			Data      struct {
				Result []struct {
					Metric map[string]string `json:"metric"`
					Value  []any             `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil {
			states[len(states)-1] = telemetryUsageStateQueryFailed
			if out.Detail == "" {
				out.Detail = "the backend's answer could not be parsed"
			}
			continue
		}
		// The PromQL family reports its own failures inside a 200, so the envelope's status is read
		// rather than assumed from the HTTP code. Its error text is carried for the same reason the
		// SQL half's is: a failure with no stated cause is the blank this surface exists to remove.
		if answer.Status != "success" {
			states[len(states)-1] = telemetryUsageStateQueryFailed
			if out.Detail == "" {
				out.Detail = usageDetail(strings.TrimSpace(answer.ErrorType+" "+answer.Error),
					nil, cfg.Headers)
			}
			continue
		}
		if len(answer.Data.Result) == 0 {
			silent = append(silent, metric)
		}
		for _, r := range answer.Data.Result {
			value := ""
			if len(r.Value) == 2 {
				value = fmt.Sprintf("%v", r.Value[1])
			}
			out.Rows = append(out.Rows, metricRowFromLabels(r.Metric, metric, value))
		}
	}

	// Preserved across the loop: worstUsageState over an empty slice is ok, which would report a
	// healthy metrics half for a configuration that queried nothing at all.
	if len(states) == 0 {
		out.State = telemetryUsageStateQueryFailed
		out.Detail = "no session metrics are configured to be queried"
		return out
	}
	out.State = worstUsageState(states...)

	// The fourth way to read nothing, and the only one with no state of its own.
	//
	// The metric names above are Claude Code's, and Claude Code versions independently of this
	// repository. When one is renamed the query still succeeds — HTTP 200, status "success", an
	// empty vector — so the state stays ok and the rows simply stop arriving. Recording is on, the
	// backend is healthy, every dark state is clean, and a whole category of measurement is gone
	// with nothing anywhere to say so.
	//
	// It is not a sixth state: the enum carries conditions that prevented or degraded a query, and
	// nothing here was prevented or degraded. It rides detail, which the mirror already relays.
	//
	// The empty-detail guard is load-bearing rather than defensive: detail already carries the
	// first failing metric's error class, and overwriting that with a sentence about metric names
	// would replace a diagnosis the backend supplied with a guess about why it is absent.
	if out.State == telemetryUsageStateOK && out.Detail == "" && len(silent) > 0 {
		out.Detail = silentMetricsDetail(ctx, cfg, silent, selector, now)
	}
	return out
}

// silentMetricsDetail answers the idle-vs-renamed ambiguity a bare "returned no
// series" sentence cannot (fable-implement Step 4, contributing gap #5): the code
// issues one bounded existence question per silent metric against the series API,
// rather than pushing the determination onto an operator who, under the autonomy
// criterion, does not exist. Bounded to len(silent) requests, silent being at most
// len(telemetryUsageMetrics) by construction (it only grows inside that loop above).
//
// A probe failure keeps today's hedged sentence VERBATIM — the explicit no-regression
// requirement — rather than a partial or degraded differentiated answer.
func silentMetricsDetail(ctx context.Context, cfg config.TelemetryConfig, silent []string, selector string, now time.Time) string {
	hedged := "queried and returned no series: " + strings.Join(silent, ", ")

	var everExisted, neverExisted []string
	for _, metric := range silent {
		q := url.Values{}
		q.Set("match[]", metric+selector)
		q.Set("start", strconv.FormatInt(usageEpochUS/1_000_000, 10))
		q.Set("end", strconv.FormatInt(now.Unix(), 10))
		state, raw, _ := usageRequest(ctx, cfg, http.MethodGet,
			usageURL(cfg.Endpoint, telemetrySeriesPath)+"?"+q.Encode(), nil)
		if state != telemetryUsageStateOK {
			return hedged
		}
		var answer struct {
			Status string           `json:"status"`
			Data   []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(raw, &answer); err != nil || answer.Status != "success" {
			return hedged
		}
		if len(answer.Data) > 0 {
			everExisted = append(everExisted, metric)
		} else {
			neverExisted = append(neverExisted, metric)
		}
	}

	var parts []string
	if len(everExisted) > 0 {
		parts = append(parts, "queried and returned no series at this instant (idle — history exists): "+
			strings.Join(everExisted, ", "))
	}
	if len(neverExisted) > 0 {
		parts = append(parts, "queried and returned no series, and no series has ever existed under: "+
			strings.Join(neverExisted, ", ")+" — never recorded here, or the names have moved")
	}
	return strings.Join(parts, "; ")
}

// metricRowFromLabels projects one PromQL series onto a row.
//
// The read side matters as much as the selector, and fails more quietly. A selector naming a label
// the backend does not carry returns an empty vector — visible as no rows. A label READ back under
// a stale spelling returns rows whose agent and run are blank: attribution silently lost on data
// that did arrive, which is worse, because it looks like an answer.
//
// Named so a test can hand it a series and check the projection, rather than grepping this file for
// label names that also appear in a comment above and in the allowlist below.
func metricRowFromLabels(labels map[string]string, metric, value string) telemetryUsageMetricRowJSON {
	row := telemetryUsageMetricRowJSON{
		Metric:   metric,
		Agent:    labels["af_agent"],
		Instance: labels["af_formula_instance"],
		Labels:   map[string]string{},
		Value:    value,
	}
	for _, name := range telemetryUsageMetricLabels {
		if v := labels[name]; v != "" {
			row.Labels[name] = v
		}
	}
	return row
}

// promQLSelector builds the label selector the metrics queries are narrowed by.
//
// It is a named function rather than four lines inside queryMetrics so that a test can compare the
// string it produces against the spelling derived from the emitted attribute names. Reading these
// two label names out of the file as text does not couple them to anything: the same tokens appear
// in a comment a few lines up and in the label allowlist, so a grep passes while the selector
// itself says something else entirely — a rename here to af_agentZZ left the whole root suite green
// until this seam existed.
//
// The values are already validated by the caller before they reach a splice site.
func promQLSelector(agent, instance string) string {
	var parts []string
	if agent != "" {
		parts = append(parts, fmt.Sprintf(`af_agent="%s"`, agent))
	}
	if instance != "" {
		parts = append(parts, fmt.Sprintf(`af_formula_instance="%s"`, instance))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// usageURL joins onto the configured base rather than rebuilding it.
//
// The base already carries the org segment, and an earlier shape in this codebase that
// reconstructed scheme://host:port dropped it and made every request 404 in silence. Appending
// cannot lose a segment that is inside the string being appended to.
func usageURL(endpoint, path string) string {
	return strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(path, "/")
}

// usageTimeout is the probe surface's envelope, re-expressed here because that one is unexported.
//
// The configured budget is tuned for a hook path where half a second is the point; a query an
// operator is waiting on needs long enough to distinguish slow from absent, and a ceiling so the
// verb cannot hang on an operator-set value. The bounds are applied to a context derived from the
// caller's, so an earlier deadline still wins and the floor is never a minimum wait.
func usageTimeout(cfg config.TelemetryConfig) time.Duration {
	const floor, ceiling = 2 * time.Second, 10 * time.Second
	d := time.Duration(cfg.ExportTimeoutMS) * time.Millisecond
	if d < floor {
		d = floor
	}
	if d > ceiling {
		d = ceiling
	}
	return d
}

// displaySafeEndpoint strips userinfo before an endpoint is carried in a payload. A configured
// endpoint is allowed to embed credentials, and echoing one verbatim publishes a password to
// every reader of this surface.
// It reports the host and path only when it can prove there is no userinfo to strip. An endpoint
// this cannot parse is described rather than blanked: an unexplained empty field reads as "not
// configured", which is a different problem with a different fix.
func displaySafeEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "(the configured endpoint could not be parsed)"
	}
	if u.User != nil {
		u.User = nil
	}
	return u.String()
}

// sortedHeaderNames keeps header application deterministic, so a configuration with several
// unresolved references always names the same one and an operator chasing it is not sent somewhere
// different on each run.
func sortedHeaderNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
