package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped telemetry views are a new artifact class: a directory of JSON that quickstart
// delivers to the observability backend. These tests cover the artifact side — the files exist,
// parse, carry the keys the seeder reads, and speak the operator's vocabulary. The join semantics
// they encode are tested from internal/telemetry, next to the constants and the attribution
// function that define them.

const telemetryViewsDir = "install_telemetry_views"

// The six names are pinned by the design. A rename is a breaking change for anyone whose
// dashboards or bookmarks refer to them, so it should have to be deliberate.
var shippedViewNames = []string{
	"waterfall",
	"agent-model-step-tokens",
	"derived-dollars",
	"overhead-buckets",
	"reconciliation",
	"zero-join-canary",
}

func viewsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(findModuleRoot(t), "internal", "cmd", telemetryViewsDir)
}

func TestTelemetryViewsShipped(t *testing.T) {
	dir := viewsPath(t)

	t.Run("all six views and the price list are present", func(t *testing.T) {
		for _, name := range append(append([]string{}, shippedViewNames...), "pricing") {
			info, err := os.Stat(filepath.Join(dir, name+".json"))
			if err != nil {
				t.Errorf("%s.json is missing: %v", name, err)
				continue
			}
			if info.Size() == 0 {
				t.Errorf("%s.json is empty", name)
			}
		}
	})

	// A file that fails to parse would satisfy an existence check and then be rejected by the
	// backend at seed time, where nobody is watching.
	t.Run("every shipped file is well-formed JSON", func(t *testing.T) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Errorf("reading %s: %v", e.Name(), err)
				continue
			}
			var any interface{}
			if err := json.Unmarshal(raw, &any); err != nil {
				t.Errorf("%s is not valid JSON: %v", e.Name(), err)
			}
		}
	})

	// These are the keys quickstart's seeder actually reads. A view missing dashboard.title is
	// silently skipped by the push loop rather than failing loudly, so it is checked here.
	t.Run("every view carries the keys the seeder reads", func(t *testing.T) {
		for _, name := range shippedViewNames {
			var v struct {
				Name        string          `json:"name"`
				Title       string          `json:"title"`
				Description string          `json:"description"`
				Join        json.RawMessage `json:"af_join"`
				Decision    string          `json:"af_decision_o4"`
				Dashboard   struct {
					Version int    `json:"version"`
					Title   string `json:"title"`
					Tabs    []struct {
						Panels []struct {
							Queries []struct {
								Query string `json:"query"`
							} `json:"queries"`
						} `json:"panels"`
					} `json:"tabs"`
				} `json:"dashboard"`
			}
			raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
			if err != nil {
				t.Errorf("reading %s.json: %v", name, err)
				continue
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Errorf("parsing %s.json: %v", name, err)
				continue
			}
			switch {
			case v.Name == "":
				t.Errorf("%s.json has no name", name)
			case v.Title == "":
				t.Errorf("%s.json has no title", name)
			case v.Description == "":
				t.Errorf("%s.json has no description", name)
			case len(v.Join) == 0:
				t.Errorf("%s.json declares no af_join contract; the direction of its overhead "+
					"predicate would then be untestable", name)
			case v.Decision == "":
				t.Errorf("%s.json does not record operator decision O-4", name)
			case v.Dashboard.Title == "":
				t.Errorf("%s.json has no dashboard.title; the seeder reads that key to decide "+
					"whether the dashboard already exists, and skips the file without it", name)
			}

			// The backend dispatches on this integer and treats an omitted version as the latest,
			// so leaving it out would silently retarget the payload on an upgrade.
			if v.Dashboard.Version == 0 {
				t.Errorf("%s.json does not pin dashboard.version", name)
			}

			var queries int
			for _, tab := range v.Dashboard.Tabs {
				for _, p := range tab.Panels {
					for _, q := range p.Queries {
						if strings.TrimSpace(q.Query) != "" {
							queries++
						}
					}
				}
			}
			if queries == 0 {
				t.Errorf("%s.json ships no panel query; there would be nothing to render", name)
			}
		}
	})
}

// TestTelemetryViewsUseOperatorVocabulary is the review gate on shipped views: a dashboard is read
// by someone who wants to know what a run cost, not by someone who wants to know how the data got
// there. The walk covers every name and title anywhere in the document, including the tab and
// panel titles a line-oriented grep over the file would treat inconsistently.
//
// Mechanism words inside a query body are fine and unavoidable — the column really is called
// af_overhead. The gate applies to what the operator reads.
func TestTelemetryViewsUseOperatorVocabulary(t *testing.T) {
	forbidden := []string{"otlp", "resourcespans", "resource.attribute", "resource_attribute", "exporter"}

	entries, err := os.ReadDir(viewsPath(t))
	if err != nil {
		t.Fatalf("reading views dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(viewsPath(t), e.Name()))
		if err != nil {
			t.Errorf("reading %s: %v", e.Name(), err)
			continue
		}
		var doc interface{}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue // covered by the JSON validity subtest
		}
		for _, found := range operatorFacingStrings(doc) {
			lowered := strings.ToLower(found)
			for _, bad := range forbidden {
				if strings.Contains(lowered, bad) {
					t.Errorf("%s: operator-facing text %q contains the mechanism term %q. "+
						"Dashboard names and titles should say what the operator wants to know "+
						"(\"Tokens per step\", \"Cost per agent\"), not how the data arrived",
						e.Name(), found, bad)
				}
			}
		}
	}
}

// operatorFacingStrings collects every "name" and "title" value anywhere in the document.
func operatorFacingStrings(node interface{}) []string {
	var out []string
	switch typed := node.(type) {
	case map[string]interface{}:
		for key, value := range typed {
			if key == "name" || key == "title" {
				if s, ok := value.(string); ok {
					out = append(out, s)
				}
			}
			out = append(out, operatorFacingStrings(value)...)
		}
	case []interface{}:
		for _, item := range typed {
			out = append(out, operatorFacingStrings(item)...)
		}
	}
	return out
}

// TestQuickstartSeedsTelemetryViews covers the delivery half: the views are only useful if
// something puts them in front of the operator.
func TestQuickstartSeedsTelemetryViews(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	body := extractShellFunction(content, "setup_telemetry")
	if body == "" {
		t.Fatal("could not extract setup_telemetry() function body")
	}

	t.Run("seeds from the in-repo source of truth", func(t *testing.T) {
		if !strings.Contains(body, telemetryViewsDir) {
			t.Errorf("setup_telemetry does not reference %s; the views would be authored and "+
				"never delivered", telemetryViewsDir)
		}
	})

	// Operator decision O-4 — view delivery mechanism. Recorded, not self-adjudicated: the pinned
	// backend has no file-based dashboard provisioning, so a file-only seed ships JSON nothing
	// consumes; a push-only seed loses the artifact whenever the backend never came up. The choice
	// is hybrid, and this subtest exists so the choice cannot be quietly reverted to one half.
	t.Run("O-4 delivery is hybrid: files on disk AND a best-effort push", func(t *testing.T) {
		if !strings.Contains(body, "O-4") {
			t.Error("setup_telemetry does not record operator decision O-4 at the seeding site")
		}
		if !strings.Contains(body, "telemetry/views") {
			t.Error("O-4 (file half): views are not seeded to a location the operator can read " +
				"and edit, so a backend that never started leaves nothing behind")
		}
		if !strings.Contains(body, "/api/default/dashboards") {
			t.Error("O-4 (push half): no dashboard push. The pinned backend cannot read views " +
				"from disk, so without this the seeded files are never rendered by anything")
		}
	})

	// Re-running quickstart is routine. The dashboards API has no upsert, so an unguarded push
	// would add a duplicate set on every run.
	t.Run("the push is guarded against duplicating dashboards", func(t *testing.T) {
		if !strings.Contains(body, "/api/default/dashboards\"") {
			t.Error("no listing call before the push; re-running quickstart would create a " +
				"second copy of every dashboard")
		}
		if !strings.Contains(body, ".title? // empty") {
			t.Error("the existing-dashboard check does not compare reported titles, so an error " +
				"payload echoing a title would read as already-published")
		}
	})

	// The operator's own prices must survive a re-run, matching the litellm.yaml seed idiom.
	t.Run("operator edits to the price list are never overwritten", func(t *testing.T) {
		if !strings.Contains(body, "pricing.json") {
			t.Error("the seeding step does not special-case pricing.json, so an operator's edited " +
				"prices would be overwritten on the next run")
		}
	})

	// setup_telemetry is default-on. Nothing it added may cost the operator a working factory.
	//
	// The check is scoped to the seeding block rather than the whole function because
	// _port_in_use() legitimately returns 1 as a boolean answer, not as a failure.
	t.Run("seeding failures degrade to a warning", func(t *testing.T) {
		marker := "Telemetry views not found"
		if !strings.Contains(body, marker) {
			t.Errorf("expected a warning path when the source of truth is absent (%q)", marker)
		}

		start := strings.Index(body, telemetryViewsDir)
		end := strings.Index(body, "Login-shell relaunch guard")
		if start < 0 || end < 0 || end < start {
			t.Fatalf("could not isolate the seeding block: start=%d end=%d", start, end)
		}
		block := body[start:end]
		for _, fatal := range []string{"return 1", "exit 1", "set -e"} {
			if strings.Contains(block, fatal) {
				t.Errorf("the view-seeding block contains %q; a backend that rejects a dashboard "+
					"must not cost the operator the rest of the install", fatal)
			}
		}
		if !strings.Contains(block, "log_warn") {
			t.Error("the seeding block has no log_warn path; a silent failure here looks " +
				"identical to success")
		}
	})

	// Ordering is what makes the O-4 rationale true rather than merely stated. The copy is the
	// half that has to survive a backend which never came up, so it must run before every early
	// return; the push needs a live backend and the auth header, so it must run after the
	// readiness gate. Getting this backwards leaves the hybrid working like a push-only seed in
	// exactly the failure modes the decision cites.
	t.Run("the copy precedes the early returns and the push follows readiness", func(t *testing.T) {
		copyAt := strings.Index(body, "Seeded telemetry views")
		launch := strings.Index(body, "tmux new-session -d -s telemetry")
		ready := strings.Index(body, "/healthz")
		push := strings.Index(body, "/api/default/dashboards")
		summary := strings.Index(body, "OpenObserve installed at")
		if copyAt < 0 || launch < 0 || ready < 0 || push < 0 || summary < 0 {
			t.Fatalf("could not locate ordering anchors: copy=%d launch=%d ready=%d push=%d summary=%d",
				copyAt, launch, ready, push, summary)
		}
		if copyAt > launch {
			t.Error("the view files are copied after the backend launch, so an occupied port or a " +
				"failed start leaves the operator with nothing on disk — which is the very case " +
				"O-4 cites for not choosing a push-only seed")
		}
		if push < ready {
			t.Error("dashboards are pushed before the backend is known to be ready")
		}
		if push > summary {
			t.Error("dashboards are pushed after the summary; a failure there would print below " +
				"the line the operator is meant to act on")
		}
	})

	// fable-implement Step 2 (Root Cause A, E-6/R5a): the relaunch.sh write and the
	// bash_profile guard append must precede ALL EIGHT setup_telemetry() early
	// returns, not just be present somewhere in the function — an install whose
	// backend fails on first start must still receive the one recovery mechanism
	// the design relies on. Phase 5 (RED): quickstart.sh has NOT been modified
	// yet, so neither marker exists and this subtest fails predictably.
	t.Run("the relaunch script and login guard precede all eight early returns", func(t *testing.T) {
		relaunchWrite := strings.Index(body, ".agentfactory/telemetry/relaunch.sh")
		if relaunchWrite < 0 {
			t.Fatal("setup_telemetry() does not yet write .agentfactory/telemetry/relaunch.sh — " +
				"fable-implement Step 2 has not landed in this file")
		}
		guardAppend := strings.Index(body, "BEGIN agentfactory telemetry login guard")
		if guardAppend < 0 {
			t.Fatal("could not locate the bash_profile login-guard append marker")
		}

		// Anchored by each branch's own unique log line, one per known return-0 site
		// (E-1's corrected count of eight, not six).
		returns := []struct{ name, anchor string }{
			{"unsupported arch (:1088)", "Unsupported arch"},
			{"download failed (:1097)", "OpenObserve download failed"},
			{"checksum verification failed (:1102)", "OpenObserve checksum verification FAILED"},
			{"unpack/install failed (:1109)", "OpenObserve unpack/install failed"},
			{"port already in use (:1209)", "already in use by another process"},
			{"tmux launch failed (:1214)", "Failed to launch OpenObserve tmux session"},
			{"exited during startup (:1240)", "OpenObserve exited during startup"},
			{"not ready in time (:1244)", "OpenObserve not ready on 127.0.0.1"},
		}
		for _, r := range returns {
			idx := strings.Index(body, r.anchor)
			if idx < 0 {
				t.Fatalf("could not locate the %s return-0 anchor %q", r.name, r.anchor)
			}
			if relaunchWrite > idx {
				t.Errorf("relaunch.sh is written AFTER the %s early return — an install that hits "+
					"this path leaves no recovery script on disk", r.name)
			}
			if guardAppend > idx {
				t.Errorf("the login guard is appended AFTER the %s early return — an install that "+
					"hits this path leaves no recovery mechanism on disk at all", r.name)
			}
		}
	})
}
