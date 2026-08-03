package cmd

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
)

// authoredTokensView is the shipped per-step tokens view, embedded so the query the binary sends is
// the query that was authored and contract-tested — not a copy of it.
//
// Until this file existed, install_telemetry_views/ had no //go:embed and no non-test Go reader: it
// reached a factory only as a file copy performed by quickstart.sh, into a directory the quickstart
// itself treats as operator-editable. That left "derive the query from the authored source" with no
// mechanism at all, and the two obvious substitutes are both worse. A hand-copied string is the
// divergent duplicate the view's contract tests exist to prevent, and they cannot see it: they read
// this JSON file, not whatever the command sends. Reading the seeded copy at runtime is worse still
// — it turns an operator-editable file into the SQL a credentialed client executes, which is the
// operator-supplied-query shape the design rejected outright.
//
//go:embed install_telemetry_views/agent-model-step-tokens.json
var authoredTokensView []byte

// The two predicates the filter splices into, quoted exactly as authored. Exact anchors are the
// point: if the view is re-authored and either predicate moves, the splice fails loudly here rather
// than silently producing a query that filters nothing.
const (
	tracesPredicate = "WHERE service_name = 'agentfactory'\n      AND af_step_id <> ''"

	// A bare disjunction, and the reason every injected conjunct is bracketed. Appending
	// "AND af_agent = 'x'" to this binds to the right-hand side only: the filter stops applying to
	// every row where af_overhead IS NULL — which is most of them — and quality-gate spend is
	// counted back into the step totals it is supposed to be excluded from. There is no error and
	// no failing test, only a step that looks more expensive than the work it did.
	logsPredicate = "WHERE af_overhead IS NULL OR af_overhead = ''"
)

// validTelemetryInstance is the shape rule for --instance.
//
// It is deliberately ValidateAgentName's shape rather than the web module's stricter --var key rule
// (^[A-Za-z0-9_]+$): every real formula-instance id is af-<8hex>, so the narrower rule would reject
// all of them on the hyphen. What matters for this use is what the class EXCLUDES — quotes,
// whitespace, semicolons, control characters, dots and slashes — because that is what makes the
// value unable to change the meaning of a query it is spliced into.
var validTelemetryInstance = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// maxTelemetryInstanceLen matches ValidateAgentName's bound. A length rule belongs next to a shape
// rule: without it, a megabyte of 'a' is a valid identifier.
const maxTelemetryInstanceLen = 64

func validateTelemetryInstance(id string) error {
	if id == "" {
		return fmt.Errorf("instance id cannot be empty")
	}
	if len(id) > maxTelemetryInstanceLen {
		return fmt.Errorf("instance id too long (max %d characters)", maxTelemetryInstanceLen)
	}
	if !validTelemetryInstance.MatchString(id) {
		// The rejected value is deliberately not echoed. This error reaches a terminal and a log,
		// and echoing a hostile payload is how an injection attempt gets a second delivery route.
		return fmt.Errorf("invalid instance id: must match [a-zA-Z][a-zA-Z0-9_-]*")
	}
	return nil
}

// telemetryWhere carries the two spellings of the same filter. They are separate fields rather than
// one string because the same attribute is named differently on the two sides of the join:
// attributes are rewritten on ingest, trace resource attributes gain a service_ prefix, and OTLP
// logs do not. Verified against both live stream schemas, not just the view's assumptions block.
type telemetryWhere struct {
	Traces string
	Logs   string
}

// buildTelemetryWhere validates first and builds second — never the reverse.
//
// That order is the whole mechanism. Escaping is a promise about a transformation; refusing to
// construct is a property of the code. A caller that gets an error from this function is holding
// nothing that could be sent, because nothing was assembled.
func buildTelemetryWhere(agent, instance string) (telemetryWhere, error) {
	if agent != "" {
		// The shared validator makes the DECISION — one shape rule for one value, rather than a
		// second copy here that can drift from the one guarding --agent where it becomes a path
		// segment. Its MESSAGE is not reused: it quotes the offending name, which is right for a
		// local CLI error and wrong here, because this error becomes a field of a JSON payload that
		// reaches a browser. Echoing a rejected injection payload back out is a second delivery
		// route for it.
		if err := config.ValidateAgentName(agent); err != nil {
			return telemetryWhere{}, fmt.Errorf("--agent: invalid agent name: must match [a-zA-Z][a-zA-Z0-9_-]* and be at most 64 characters")
		}
	}
	if instance != "" {
		if err := validateTelemetryInstance(instance); err != nil {
			return telemetryWhere{}, fmt.Errorf("--instance: %w", err)
		}
	}
	if err := assertSpliceable(agent, instance); err != nil {
		return telemetryWhere{}, err
	}

	var traces, logs []string
	if agent != "" {
		traces = append(traces, fmt.Sprintf("(service_af_agent = '%s')", agent))
		logs = append(logs, fmt.Sprintf("(af_agent = '%s')", agent))
	}
	if instance != "" {
		traces = append(traces, fmt.Sprintf("(service_af_formula_instance = '%s')", instance))
		logs = append(logs, fmt.Sprintf("(af_formula_instance = '%s')", instance))
	}
	if len(traces) == 0 {
		return telemetryWhere{}, nil
	}

	return telemetryWhere{
		Traces: " AND " + strings.Join(traces, " AND "),
		Logs:   " AND " + strings.Join(logs, " AND "),
	}, nil
}

// assertSpliceable is the backstop for the case the validators above are ever widened without this
// splice being revisited.
//
// It examines the INPUT values, not the built clause — checking the clause would be checking a
// string this function itself just filled with quote delimiters, which is a test that can only ever
// report what it put there. Checked before any clause is assembled, so a widened validator produces
// a refusal rather than a query.
func assertSpliceable(values ...string) error {
	// Covers BOTH splice sites, not just the SQL one. The same validated values also become PromQL
	// label selectors, where the delimiters are braces, commas and equals rather than quotes — a
	// guard that only knew about SQL would read as protecting both while protecting one.
	for _, v := range values {
		if strings.ContainsAny(v, "'\"\\`;{},=()\x00\n\r\t ") {
			return fmt.Errorf("refusing to splice a value containing a quote, backslash, brace, " +
				"comma, equals, semicolon, whitespace or control character into a query")
		}
	}
	return nil
}

// authoredTokensViewSQL pulls the query out of the embedded view exactly as authored.
func authoredTokensViewSQL() (string, error) {
	var view struct {
		Dashboard struct {
			Tabs []struct {
				Panels []struct {
					Queries []struct {
						Query string `json:"query"`
					} `json:"queries"`
				} `json:"panels"`
			} `json:"tabs"`
		} `json:"dashboard"`
	}
	if err := json.Unmarshal(authoredTokensView, &view); err != nil {
		return "", fmt.Errorf("parsing the embedded tokens view: %w", err)
	}
	if len(view.Dashboard.Tabs) == 0 ||
		len(view.Dashboard.Tabs[0].Panels) == 0 ||
		len(view.Dashboard.Tabs[0].Panels[0].Queries) == 0 {
		return "", fmt.Errorf("the embedded tokens view holds no query")
	}
	sql := view.Dashboard.Tabs[0].Panels[0].Queries[0].Query
	if strings.TrimSpace(sql) == "" {
		return "", fmt.Errorf("the embedded tokens view's query is empty")
	}
	return sql, nil
}

// telemetryTokensSQL returns the authored query narrowed by the validated filter.
//
// With no filter it returns the authored query byte for byte, so the unfiltered case cannot drift
// from the source at all. With a filter it brackets the existing predicate before conjoining —
// see logsPredicate for why that bracket is load-bearing rather than tidy.
// tokensViewLogsPlaneColumns extracts the column names the authored view SQL
// references from the "logs"."default" relation only — the billable_requests CTE —
// never the traces-plane columns from step_windows. Reading directly from the
// embedded SQL (not a recorded-real capture) is what lets step 3's schema
// pre-flight land ahead of step 6b's fixture (fable-implement decisions.md D-none,
// investigation_report.md finding #7/R5): its only input is authoredTokensViewSQL(),
// already available; it must never wait on the P6b capture.
//
// fable-implement Phase 5 (RED): stubbed to return nothing. Phase 6 implements the
// real extraction (locate the SELECT list feeding "logs"."default" plus the WHERE
// clause immediately after it; read out the bare source-column identifiers from
// both, never an AS alias).
func tokensViewLogsPlaneColumns() ([]string, error) {
	sql, err := authoredTokensViewSQL()
	if err != nil {
		return nil, err
	}

	body, err := cteBody(sql, "billable_requests")
	if err != nil {
		return nil, err
	}
	selectList, whereClause, err := splitSelectWhere(body)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var columns []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			columns = append(columns, name)
		}
	}
	for _, item := range splitTopLevel(selectList, ',') {
		add(leadingIdentifier(item))
	}
	for _, ident := range sqlIdentifierRefs(whereClause) {
		add(ident)
	}
	return columns, nil
}

// cteBody isolates one CTE's body — the text between its "AS (" and the matching
// closing paren — by paren-depth tracking, so a nested expression cannot fool the
// boundary the way a naive strings.Index(closingParen) would.
func cteBody(sql, name string) (string, error) {
	marker := name + " AS ("
	start := strings.Index(sql, marker)
	if start < 0 {
		return "", fmt.Errorf("CTE %q not found in the authored view", name)
	}
	rest := sql[start+len(marker):]
	depth := 1
	for i, r := range rest {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return rest[:i], nil
			}
		}
	}
	return "", fmt.Errorf("CTE %q has no matching closing paren", name)
}

// splitSelectWhere pulls the SELECT list and the top-level WHERE predicate out of a
// single-level CTE body (no subqueries between SELECT and WHERE — true of
// billable_requests today; a CTE with a subquery there would need a depth-aware
// split, which stripCTEComments plus the paren-balanced cteBody above intentionally
// keep out of scope until a CTE actually needs it).
func splitSelectWhere(body string) (selectList, whereClause string, err error) {
	body = stripSQLLineComments(body)
	selectIdx := strings.Index(body, "SELECT")
	fromIdx := strings.Index(body, "FROM")
	if selectIdx < 0 || fromIdx < 0 || fromIdx < selectIdx {
		return "", "", fmt.Errorf("CTE body has no SELECT...FROM to parse")
	}
	selectList = body[selectIdx+len("SELECT") : fromIdx]
	whereIdx := strings.Index(body[fromIdx:], "WHERE")
	if whereIdx < 0 {
		return selectList, "", nil // a WHERE-less CTE is valid; just no predicate columns to add
	}
	whereClause = body[fromIdx+whereIdx+len("WHERE"):]
	return selectList, whereClause, nil
}

// stripSQLLineComments removes "-- ..." comments so their prose (which can contain
// stray identifier-shaped words, e.g. "AttributeEvent") never leaks into the
// extracted column set.
func stripSQLLineComments(sql string) string {
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// splitTopLevel splits on sep, but never inside parens — none of this view's SELECT
// items need it today (no function calls in the logs-plane list), kept anyway so a
// future column expression with a function call doesn't silently mis-split.
func splitTopLevel(s string, sep rune) []string {
	var parts []string
	depth := 0
	start := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

var sqlIdentifierRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// leadingIdentifier returns a SELECT item's source column — the first identifier
// token, before any "AS alias" or arithmetic ("_timestamp * 1000 AS at_ns" ->
// "_timestamp"; "input_tokens" -> "input_tokens").
func leadingIdentifier(item string) string {
	return sqlIdentifierRE.FindString(item)
}

// sqlIdentifierKeywords excludes SQL keywords a WHERE-clause regex scan would
// otherwise mistake for column references.
var sqlIdentifierKeywords = map[string]bool{
	"IS": true, "NULL": true, "OR": true, "AND": true, "NOT": true,
	"IN": true, "LIKE": true, "TRUE": true, "FALSE": true,
}

// sqlIdentifierRefs extracts every non-keyword identifier a WHERE predicate
// references, deduplicated in first-seen order.
func sqlIdentifierRefs(clause string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range sqlIdentifierRE.FindAllString(clause, -1) {
		if sqlIdentifierKeywords[strings.ToUpper(m)] || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func telemetryTokensSQL(agent, instance string) (string, error) {
	sql, err := authoredTokensViewSQL()
	if err != nil {
		return "", err
	}

	where, err := buildTelemetryWhere(agent, instance)
	if err != nil {
		return "", err
	}
	if where.Traces == "" {
		return sql, nil
	}

	for _, splice := range []struct {
		anchor, filter string
	}{
		{tracesPredicate, where.Traces},
		{logsPredicate, where.Logs},
	} {
		if !strings.Contains(sql, splice.anchor) {
			return "", fmt.Errorf("the authored tokens view no longer contains the predicate this "+
				"filter splices into (%q); re-check the view before widening the anchor, because a "+
				"silent miss here produces a query that filters nothing", splice.anchor)
		}
		bracketed := "WHERE (" + strings.TrimPrefix(splice.anchor, "WHERE ") + ")" + splice.filter
		sql = strings.Replace(sql, splice.anchor, bracketed, 1)
	}
	return sql, nil
}
