package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The shipped views are the only production implementation of the three binding join rules:
// DeriveWindows and AttributeEvent have no caller outside this package's tests, so nothing else
// in the binary turns events into step buckets. That makes these files the place where the
// design's per-step correctness actually lives, and it makes them worth testing from here rather
// than from wherever they happen to be stored.
//
// The acceptance criteria for this phase are greps, and a grep cannot tell an exclusion from an
// inclusion: `af.overhead IS NULL` and `af.overhead IS NOT NULL` both satisfy "mentions
// af.overhead". A view that owns the overhead events it was supposed to drop produces per-step
// totals that are silently too high while the reconciliation view still balances — the exact
// failure the rule exists to prevent. So each view declares its intent in a machine-readable
// contract, and the tests below check that intent against this package's own constants and its
// own attribution function.
//
// Checking against the constants rather than against string literals is deliberate: renaming
// overheadSuffix here must break the views, because the bucket name IS the contract between a
// producer that emits it and a view that groups by it.

const viewsDir = "../cmd/install_telemetry_views"

// Intent values a view may declare for its relationship to overhead-marked events. Exactly three
// views may take a position; the rest declare none.
const (
	overheadExclude   = "exclude"
	overheadOwn       = "own"
	overheadReconcile = "reconcile"
	overheadNone      = "none"
)

type joinContract struct {
	Overhead       string   `json:"overhead"`
	Window         string   `json:"window"`
	StartRule      string   `json:"start_rule"`
	EndRule        string   `json:"end_rule"`
	Tail           string   `json:"tail"`
	InstanceScoped bool     `json:"instance_scoped"`
	Buckets        []string `json:"buckets"`
}

type shippedView struct {
	Name        string       `json:"name"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Join        joinContract `json:"af_join"`
	Dashboard   struct {
		Version int `json:"version"`
		Tabs    []struct {
			Panels []struct {
				Queries []struct {
					Query string `json:"query"`
				} `json:"queries"`
			} `json:"panels"`
		} `json:"tabs"`
	} `json:"dashboard"`
}

// sql returns every panel query in the view, lowercased and joined, for predicate inspection.
func (v shippedView) sql() string {
	var b strings.Builder
	for _, tab := range v.Dashboard.Tabs {
		for _, p := range tab.Panels {
			for _, q := range p.Queries {
				b.WriteString(q.Query)
				b.WriteString("\n")
			}
		}
	}
	return strings.ToLower(b.String())
}

func loadView(t *testing.T, name string) shippedView {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(viewsDir, name+".json"))
	if err != nil {
		t.Fatalf("reading shipped view %s: %v", name, err)
	}
	var v shippedView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsing shipped view %s: %v", name, err)
	}
	return v
}

// TestShippedViewsDeclareOverheadDirection pins rule 1 by DIRECTION, which is the half of the rule
// no acceptance grep can reach.
//
// Three views pivot on the same attribute with opposite intent: the per-step token view must drop
// overhead events, the bucket view must own them, and the reconciliation view must reference both
// sides to make the difference visible. Getting any one of them backwards still produces a
// dashboard that renders and totals that add up.
func TestShippedViewsDeclareOverheadDirection(t *testing.T) {
	cases := []struct {
		view string
		want string
	}{
		{"agent-model-step-tokens", overheadExclude},
		{"overhead-buckets", overheadOwn},
		{"reconciliation", overheadReconcile},
	}
	for _, tc := range cases {
		t.Run(tc.view, func(t *testing.T) {
			v := loadView(t, tc.view)
			if v.Join.Overhead != tc.want {
				t.Errorf("%s declares af_join.overhead = %q, want %q — the direction of this "+
					"predicate is the rule; a grep for the attribute name cannot tell them apart",
					tc.view, v.Join.Overhead, tc.want)
			}
			if !strings.Contains(v.sql(), overheadAttr) &&
				!strings.Contains(v.sql(), strings.ReplaceAll(overheadAttr, ".", "_")) {
				t.Errorf("%s declares an overhead position but no panel query mentions %q in "+
					"either its wire or its ingested spelling", tc.view, overheadAttr)
			}
		})
	}

	// The predicate is checked against the overhead column by name rather than by looking for a
	// bare "is not null" anywhere, because other columns are legitimately null-checked in the same
	// query and an over-broad assertion would reject correct SQL.
	t.Run("the token view's query drops overhead rows rather than selecting them", func(t *testing.T) {
		col := strings.ReplaceAll(overheadAttr, ".", "_")
		q := normalizeSpace(loadView(t, "agent-model-step-tokens").sql())

		if !strings.Contains(q, col+" is null") {
			t.Errorf("the per-step token query must EXCLUDE overhead-marked events; expected the "+
				"predicate %q. Query:\n%s", col+" IS NULL", q)
		}
		// Both the direct flip and its negated spelling are rejected. `NOT (x IS NULL)` is the
		// same predicate wearing a different hat, and checking only the obvious form would leave
		// the cheapest way to reintroduce the bug undetected.
		for _, flipped := range []string{
			col + " is not null",
			"not (" + col + " is null)",
			"not(" + col + " is null)",
		} {
			if strings.Contains(q, flipped) {
				t.Errorf("the per-step token query SELECTS overhead rows (found %q). This is the "+
					"flipped predicate: every per-step total is inflated by grader spend while "+
					"the reconciliation view still balances. Query:\n%s", flipped, q)
			}
		}
	})

	// The mirror image: the bucket view must claim those rows, not drop them.
	t.Run("the overhead view selects the rows the token view dropped", func(t *testing.T) {
		col := strings.ReplaceAll(overheadAttr, ".", "_")
		q := normalizeSpace(loadView(t, "overhead-buckets").sql())
		if !strings.Contains(q, col+" is not null") {
			t.Errorf("the overhead view must OWN overhead-marked events; expected the predicate "+
				"%q. Without it these events are dropped by both views and vanish. Query:\n%s",
				col+" IS NOT NULL", q)
		}
	})
}

// TestShippedViewsUseTheProducersBucketVocabulary pins the bucket names against the constants this
// package actually emits.
//
// A view that groups by "grader" and a producer that emits "grader-overhead" agree on nothing, and
// every total still balances — the failure join_test.go names. Deriving the expected strings from
// the constants means a rename here cannot land without updating the views.
func TestShippedViewsUseTheProducersBucketVocabulary(t *testing.T) {
	buckets := loadView(t, "overhead-buckets")

	t.Run("the grader bucket carries the suffix the producer appends", func(t *testing.T) {
		want := "grader" + overheadSuffix
		if !containsString(buckets.Join.Buckets, want) {
			t.Errorf("overhead-buckets declares %v, missing the bucket AttributeEvent actually "+
				"produces for the grader hooks: %q", buckets.Join.Buckets, want)
		}
	})

	t.Run("spend before the first step is named, not discarded", func(t *testing.T) {
		if !containsString(buckets.Join.Buckets, BucketUnattributed) {
			t.Errorf("overhead-buckets declares %v, missing %q — spend before the first step must "+
				"be a named bucket or the reconciliation view cannot balance",
				buckets.Join.Buckets, BucketUnattributed)
		}
	})

	t.Run("the trailing tail is labelled with the producer's suffix", func(t *testing.T) {
		tokens := loadView(t, "agent-model-step-tokens")
		if !strings.Contains(strings.ToLower(tokens.sql()), tailSuffix) {
			t.Errorf("the per-step token view never mentions %q; trailing spend must be a "+
				"distinguishable component of the preceding step, not folded in silently", tailSuffix)
		}
	})
}

// TestShippedViewsPartitionTheSyntheticFixture is the behavioural half: it runs the committed
// fixture through this package's own attribution function and checks that the two views' declared
// intents route every event to exactly one owner.
//
// This is what makes a flipped predicate fail. If the token view claimed "own", overhead events
// would have two owners and ordinary events none; if the bucket view claimed "exclude", the
// overhead events would have no owner at all. Either way the partition breaks here, where a grep
// over the same files sees nothing wrong.
func TestShippedViewsPartitionTheSyntheticFixture(t *testing.T) {
	f := loadSynthetic(t)
	windows := DeriveWindows(f.Records)

	tokens := loadView(t, "agent-model-step-tokens")
	buckets := loadView(t, "overhead-buckets")

	owns := func(intent, bucket string) bool {
		isOverhead := strings.HasSuffix(bucket, overheadSuffix)
		switch intent {
		case overheadExclude:
			return !isOverhead
		case overheadOwn:
			return isOverhead
		}
		return false
	}

	var inTokens, inBuckets int
	for _, ev := range f.Events {
		bucket := AttributeEvent(windows, ev.TS, ev.Attributes)

		if bucket != ev.Expect {
			t.Errorf("%s: AttributeEvent returned %q but the fixture expects %q — the oracle and "+
				"the recorded contract disagree", ev.Case, bucket, ev.Expect)
			continue
		}

		byTokens := owns(tokens.Join.Overhead, bucket)
		byBuckets := owns(buckets.Join.Overhead, bucket)

		switch {
		case byTokens && byBuckets:
			t.Errorf("%s (bucket %q) is claimed by BOTH the per-step view and the overhead view — "+
				"it would be counted twice", ev.Case, bucket)
		case !byTokens && !byBuckets:
			t.Errorf("%s (bucket %q) is claimed by NEITHER view — this spend would vanish from "+
				"every dashboard while the totals still looked plausible", ev.Case, bucket)
		case byTokens:
			inTokens++
		default:
			inBuckets++
		}
	}

	if inBuckets == 0 {
		t.Error("no fixture event routed to the overhead view; the fixture carries a grader-marked " +
			"event specifically so this rule is exercised")
	}
	if inTokens == 0 {
		t.Error("no fixture event routed to the per-step view; the exclusion rule has swallowed " +
			"everything")
	}
}

// TestShippedViewsEncodeTheWindowRules pins rules 2 and 3 as declared intent plus a query-shape
// cross-check, so a view cannot claim a rule its SQL does not attempt.
func TestShippedViewsEncodeTheWindowRules(t *testing.T) {
	v := loadView(t, "agent-model-step-tokens")

	t.Run("earliest start and latest end", func(t *testing.T) {
		if v.Join.StartRule != "earliest" {
			t.Errorf("start_rule = %q, want \"earliest\" — a re-prime must never shrink a window", v.Join.StartRule)
		}
		if v.Join.EndRule != "latest" {
			t.Errorf("end_rule = %q, want \"latest\" — DeriveWindows keeps the latest end (window.go:67)", v.Join.EndRule)
		}
		q := v.sql()
		if !strings.Contains(q, "min(") || !strings.Contains(q, "max(") {
			t.Errorf("the window derivation must take MIN(start) AND MAX(end); query:\n%s", q)
		}
	})

	t.Run("the window is half-open", func(t *testing.T) {
		if v.Join.Window != "half-open" {
			t.Errorf("window = %q, want \"half-open\" — the start instant belongs to the step, the end instant does not", v.Join.Window)
		}
	})

	t.Run("trailing spend attributes backwards", func(t *testing.T) {
		if v.Join.Tail != "preceding-step" {
			t.Errorf("tail = %q, want \"preceding-step\" — a closing turn's tail lands after step_end "+
				"and belongs to the step that caused it", v.Join.Tail)
		}
	})

	t.Run("the join is scoped to one formula instance", func(t *testing.T) {
		if !v.Join.InstanceScoped {
			t.Error("instance_scoped = false. AttributeEvent does not filter by instance " +
				"(window.go:136) because it is called per instance; SQL over a shared stream has no " +
				"such caller and must add the predicate itself, or two concurrent runs of the same " +
				"formula will attribute each other's events")
		}
	})
}

// TestDerivedDollarsNeverInventZero pins the NULL-not-zero rule more tightly than the acceptance
// grep does.
//
// The acceptance criterion forbids one spelling, COALESCE(x, 0). An unknown model priced at zero
// is the worst kind of wrong cost data because it is plausible: it reads as "this model is free"
// rather than "we do not know". Every spelling that reaches that outcome is checked here.
func TestDerivedDollarsNeverInventZero(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(viewsDir, "derived-dollars.json"))
	if err != nil {
		t.Fatalf("reading derived-dollars.json: %v", err)
	}
	lowered := strings.ToLower(string(raw))

	forbidden := []struct {
		pattern string
		why     string
	}{
		{"coalesce", "COALESCE to a literal turns an unknown price into a free model"},
		{"ifnull", "IFNULL to a literal turns an unknown price into a free model"},
		{"nvl(", "NVL to a literal turns an unknown price into a free model"},
	}
	for _, f := range forbidden {
		if strings.Contains(lowered, f.pattern) {
			t.Errorf("derived-dollars.json contains %q: %s. Unknown models must render NULL so the "+
				"gap is visible", f.pattern, f.why)
		}
	}

	if !strings.Contains(lowered, "null") {
		t.Error("derived-dollars.json never mentions NULL; the unknown-model path must be explicit")
	}
	if !strings.Contains(lowered, "cost_usd") {
		t.Error("derived-dollars.json must take the event's own cost_usd as the primary source, " +
			"with the pricing table as cross-check and gateway fallback")
	}
	if !strings.Contains(lowered, "derived") {
		t.Error("values obtained from the pricing table rather than from the event must be labelled derived")
	}
}

// TestPricingTableHasNoCatchAllZero guards the same failure one layer down: a default row priced at
// zero would make every unknown model free without any view mentioning COALESCE at all.
func TestPricingTableHasNoCatchAllZero(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(viewsDir, "pricing.json"))
	if err != nil {
		t.Fatalf("reading pricing.json: %v", err)
	}

	table := loadPricing(t, raw)

	// A wildcard row would price every unrecognised profile. Note "default" is NOT one of these:
	// it is a literal models.json profile name that the join matches by equality like any other,
	// not a fallback.
	for _, catchAll := range []string{"*", "unknown", "catch_all", "fallback"} {
		if _, found := table[catchAll]; found {
			t.Errorf("pricing.json carries a %q row. A catch-all price makes every unrecognised "+
				"profile resolve to a number instead of NULL, which is the failure the "+
				"NULL-not-zero rule exists to prevent", catchAll)
		}
	}

	for name, m := range table {
		if m.InputPerMTokUSD == nil || m.OutputPerMTokUSD == nil {
			t.Errorf("pricing entry %q omits a rate; an absent rate must be an absent ROW, so the "+
				"join misses and the dollar renders NULL", name)
			continue
		}
		if *m.InputPerMTokUSD == 0 && *m.OutputPerMTokUSD == 0 {
			t.Errorf("pricing entry %q is priced at zero on both axes; delete the row instead so "+
				"the profile reads as unknown rather than free", name)
		}
	}
}

type pricingEntry struct {
	ModelID          string   `json:"model_id"`
	InputPerMTokUSD  *float64 `json:"input_per_mtok_usd"`
	OutputPerMTokUSD *float64 `json:"output_per_mtok_usd"`
}

func loadPricing(t *testing.T, raw []byte) map[string]pricingEntry {
	t.Helper()
	var table struct {
		Models map[string]pricingEntry `json:"models"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("parsing pricing.json: %v", err)
	}
	if len(table.Models) == 0 {
		t.Fatal("pricing.json carries no models; it is the operator's editable cross-check")
	}
	return table.Models
}

// TestDerivedDollarsMirrorsThePricingTable keeps the two copies of the price list identical.
//
// A dashboard cannot read a file, so the rates have to be inlined in the view's query — which
// means the file the operator is told to edit is not the one the query uses. Enforcing equality
// here is what makes that instruction true: the copies cannot drift without this failing, and
// re-running quickstart republishes.
func TestDerivedDollarsMirrorsThePricingTable(t *testing.T) {
	pricingRaw, err := os.ReadFile(filepath.Join(viewsDir, "pricing.json"))
	if err != nil {
		t.Fatalf("reading pricing.json: %v", err)
	}
	table := loadPricing(t, pricingRaw)

	viewRaw, err := os.ReadFile(filepath.Join(viewsDir, "derived-dollars.json"))
	if err != nil {
		t.Fatalf("reading derived-dollars.json: %v", err)
	}

	rowPattern := regexp.MustCompile(`\('([^']+)',\s*([0-9.]+),\s*([0-9.]+)\)`)
	matches := rowPattern.FindAllStringSubmatch(string(viewRaw), -1)
	if len(matches) == 0 {
		t.Fatal("derived-dollars.json carries no inline price rows; the pricing fallback has no data")
	}

	inQuery := map[string][2]string{}
	for _, m := range matches {
		inQuery[m[1]] = [2]string{m[2], m[3]}
	}

	if len(inQuery) != len(table) {
		t.Errorf("the query carries %d priced profiles and pricing.json carries %d; the operator "+
			"edits pricing.json, so a row that exists in only one of them is a rate nobody applied",
			len(inQuery), len(table))
	}

	for profile, entry := range table {
		rates, found := inQuery[profile]
		if !found {
			t.Errorf("profile %q is priced in pricing.json but absent from the view's query, so "+
				"editing its rate changes nothing", profile)
			continue
		}
		wantIn := strconv.FormatFloat(*entry.InputPerMTokUSD, 'f', 2, 64)
		wantOut := strconv.FormatFloat(*entry.OutputPerMTokUSD, 'f', 2, 64)
		if rates[0] != wantIn || rates[1] != wantOut {
			t.Errorf("profile %q is priced %s/%s in the query but %s/%s in pricing.json",
				profile, rates[0], rates[1], wantIn, wantOut)
		}
	}
	for profile := range inQuery {
		if _, found := table[profile]; !found {
			t.Errorf("profile %q is priced in the view's query but absent from pricing.json, so "+
				"the operator cannot see or change the rate being applied", profile)
		}
	}
}

// normalizeSpace collapses runs of whitespace so a predicate stays greppable across the line
// breaks and indentation a readable multi-line query uses.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
