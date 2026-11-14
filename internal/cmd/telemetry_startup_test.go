package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/worktree"
)

type scStartupJSON struct {
	V      int    `json:"v"`
	State  string `json:"state"`
	Error  string `json:"error"`
	Agents []struct {
		Agent          string `json:"agent"`
		State          string `json:"state"`
		SessionID      string `json:"session_id"`
		Tokens         int64  `json:"tokens"`
		Budget         int64  `json:"budget"`
		OverBudget     *bool  `json:"over_budget"`
		HarnessVersion string `json:"harness_version"`
		RulingsBlock   string `json:"rulings_block"`
		At             string `json:"at"`
		History        []struct {
			SessionID string `json:"session_id"`
			Tokens    int64  `json:"tokens"`
		} `json:"history"`
	} `json:"agents"`
}

func scDecodeStartup(t *testing.T, out string) scStartupJSON {
	t.Helper()
	var doc scStartupJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("af telemetry startup --json is not one JSON document: %v\n%s", err, out)
	}
	return doc
}

func TestTelemetryStartup_NoTranscriptTextCanary(t *testing.T) {
	const canary = "CANARY zq7 transcript text"
	f := newScFactory(t, true)
	agentDir := f.agentDir("manager")
	transcript := scLines(
		`{"type":"user","isSidechain":false,"version":"`+canary+`","message":{"role":"user","content":"`+canary+`"}}`,
		`{"type":"assistant","isSidechain":false,"version":"`+canary+`","message":{"id":"msg_1","model":"claude-opus-5","role":"assistant","content":[{"type":"text","text":"`+canary+`"},{"type":"thinking","thinking":"`+canary+`"}],"stop_reason":"`+canary+`","usage":{"input_tokens":500,"cache_read_input_tokens":4500,"output_tokens":10}}}`,
		`{"type":"system","subtype":"stop_hook_summary","isSidechain":false,"version":"`+canary+`","content":"`+canary+`"}`,
	)
	scArm(t, agentDir, "sess-canary", transcript)

	text, err := scRunStartup(t, f.root, "", false)
	if err != nil {
		t.Fatalf("af telemetry startup: %v", err)
	}
	jsonOut, err := scRunStartup(t, f.root, "", true)
	if err != nil {
		t.Fatalf("af telemetry startup --json: %v", err)
	}
	measurement, readErr := os.ReadFile(scPath(t, f.root, "manager"))
	if readErr != nil {
		t.Fatalf("the verb did not measure the pending turn: %v", readErr)
	}
	turn, _ := scMeasurement(t, f.root, "manager")
	if turn.Tokens != 5010 || turn.HarnessVersion != "unknown" {
		t.Errorf("measurement = %+v, want 5010 tokens and harness unknown (the version held a space)", turn)
	}

	for name, s := range map[string]string{"the measurement": string(measurement), "the text output": text, "the --json output": jsonOut} {
		for _, needle := range []string{canary, "CANARY", "zq7"} {
			if strings.Contains(s, needle) {
				t.Errorf("%s carries transcript text %q:\n%s", name, needle, s)
			}
		}
	}
	for _, ev := range scStartupEnds(t, f.root, "manager") {
		data, _ := json.Marshal(ev)
		if strings.Contains(string(data), "CANARY") {
			t.Errorf("the startup_end record carries transcript text: %s", data)
		}
	}
}

func TestTelemetryStartup_JSONStates(t *testing.T) {
	f := newScFactory(t, false)
	scWrite(t, scPath(t, f.root, "manager"),
		[]byte(`{"session_id":"sess-m","tokens":104233,"budget":90000,"harness_version":"2.1.322","rulings_block":"present","at":"2026-11-11T10:02:03Z"}`))

	out, err := scRunStartup(t, f.root, "", true)
	if err != nil {
		t.Errorf("af telemetry startup --json must exit 0: %v", err)
	}
	doc := scDecodeStartup(t, out)
	if doc.State != "ok" || len(doc.Agents) != 2 {
		t.Fatalf("doc = %+v, want state ok with manager and supervisor", doc)
	}
	if doc.V != telemetry.SchemaVersion {
		t.Errorf("the success document's v = %d, want %d like the sibling verbs and this verb's error path", doc.V, telemetry.SchemaVersion)
	}
	if prefix := fmt.Sprintf(`{"v":%d,"state":"ok"`, telemetry.SchemaVersion); !strings.HasPrefix(strings.TrimSpace(out), prefix) {
		t.Errorf("the success document does not start %s: %s", prefix, out)
	}
	m, s := doc.Agents[0], doc.Agents[1]
	if m.Agent != "manager" || m.State != "measured" || m.SessionID != "sess-m" || m.Tokens != 104233 || m.Budget != 90000 ||
		m.OverBudget == nil || !*m.OverBudget || m.HarnessVersion != "2.1.322" || m.RulingsBlock != "present" || m.At != "2026-11-11T10:02:03Z" {
		t.Errorf("manager = %+v, want the planted measurement, over budget", m)
	}
	if m.History != nil {
		t.Errorf("history must be omitted when telemetry is off: %+v", m.History)
	}
	if s.Agent != "supervisor" || s.State != "no measurement" {
		t.Errorf("supervisor = %+v, want state \"no measurement\"", s)
	}
	if !strings.Contains(out, `{"agent":"supervisor","state":"no measurement"}`) {
		t.Errorf("an unmeasured agent carries nothing but its name and state: %s", out)
	}

	text, err := scRunStartup(t, f.root, "", false)
	if err != nil {
		t.Errorf("af telemetry startup must exit 0: %v", err)
	}
	for _, want := range []string{
		"manager: 104233 tokens at the first Stop, budget 90000, over budget; harness 2.1.322; rulings_block: present; session sess-m; at 2026-11-11T10:02:03Z\n",
		"supervisor: state: no measurement\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text output lacks %q:\n%s", want, text)
		}
	}

	for _, agent := range []string{"../manager", "alice"} {
		t.Run("error "+agent, func(t *testing.T) {
			out, err := scRunStartup(t, f.root, agent, true)
			if err != nil {
				t.Errorf("--agent %s must exit 0: %v", agent, err)
			}
			if doc := scDecodeStartup(t, out); doc.State != "error" || doc.Error == "" || doc.V != telemetry.SchemaVersion {
				t.Errorf("--agent %s --json = %s, want v %d, state error and the error named", agent, out, telemetry.SchemaVersion)
			}
			text, err := scRunStartup(t, f.root, agent, false)
			if err != nil {
				t.Errorf("--agent %s must exit 0: %v", agent, err)
			}
			if !strings.HasPrefix(text, "telemetry startup: state: error (") {
				t.Errorf("--agent %s text = %q, want the error state", agent, text)
			}
			entries, _ := os.ReadDir(filepath.Dir(scPath(t, f.root, "manager")))
			for _, e := range entries {
				if e.Name() != "manager.json" {
					t.Errorf("--agent %s created %s under .runtime/startup_turn/", agent, e.Name())
				}
			}
			if _, err := os.Stat(filepath.Join(f.root, ".runtime", "manager.json")); err == nil {
				t.Errorf("--agent %s built a path out of the value", agent)
			}
		})
	}
}

func TestTelemetryStartup_MeasuresPendingTurnOnRead(t *testing.T) {
	f := newScFactory(t, true)
	mail := (&mailRecorder{}).install(t)
	scArm(t, f.agentDir("manager"), "sess-read", scLines(scAssistant("msg_1", 4000, 100000), scStop))
	scArm(t, f.agentDir("supervisor"), "sess-busy", scLines(scAssistant("msg_1", 10, 10)))

	text, err := scRunStartup(t, f.root, "", false)
	if err != nil {
		t.Fatalf("af telemetry startup: %v", err)
	}
	scWantMeasured(t, f, "manager", "sess-read", 104000)
	if _, ok := scMarker(f.agentDir("manager")); ok {
		t.Error("the verb measured the turn but left its marker")
	}
	if n := len(scStartupEnds(t, f.root, "manager")); n != 1 {
		t.Errorf("startup_end records = %d, want 1", n)
	}
	if !strings.Contains(text, "manager: 104000 tokens at the first Stop") {
		t.Errorf("the verb did not report what it measured:\n%s", text)
	}
	if !strings.Contains(text, "supervisor: state: no measurement\n") {
		t.Errorf("a session with no Stop yet must report no measurement:\n%s", text)
	}
	if marker, ok := scMarker(f.agentDir("supervisor")); !ok || marker != "sess-busy" {
		t.Errorf("the supervisor's marker must stay (present=%v content=%q)", ok, marker)
	}

	jsonOut, err := scRunStartup(t, f.root, "", true)
	if err != nil {
		t.Fatalf("af telemetry startup --json: %v", err)
	}
	if n := len(scStartupEnds(t, f.root, "manager")); n != 1 {
		t.Errorf("a second run appended a record: %d startup_end records, want 1", n)
	}
	if doc := scDecodeStartup(t, jsonOut); len(doc.Agents) == 0 || len(doc.Agents[0].History) != 1 || doc.Agents[0].History[0].Tokens != 104000 {
		t.Errorf("with telemetry on the manager's history holds its one startup_end record: %s", jsonOut)
	}
	if len(mail.sent) != 0 {
		t.Errorf("af telemetry startup sends no mail, sent %+v", mail.sent)
	}
}

func TestTelemetryStartup_FromWorktreeUsesFactoryRoot(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	rel, err := filepath.Rel(fx.outer, fx.worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.WriteMeta(fx.outer, &worktree.Meta{ID: "wt-x", Path: rel, Agents: []string{"manager"}}); err != nil {
		t.Fatal(err)
	}
	gateOn(t, fx.outer)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("AF_ROOT", fx.outer)
	installMemStore(t)
	agentDir := config.AgentDir(fx.worktree, "manager")
	scArm(t, agentDir, "sess-wt", scLines(scAssistant("msg_1", 300, 700), scStop))

	if _, err := scRunStartup(t, agentDir, "", false); err != nil {
		t.Fatalf("af telemetry startup from the worktree agent dir: %v", err)
	}
	if _, ok := scMarker(agentDir); ok {
		t.Error("the verb did not take the marker from the worktree agent dir")
	}
	turn, ok := scMeasurement(t, fx.outer, "manager")
	if !ok || turn.SessionID != "sess-wt" || turn.Tokens != 1000 {
		t.Errorf("outer measurement = %+v (present %v), want sess-wt at 1000 tokens", turn, ok)
	}
	if _, err := os.Stat(filepath.Join(fx.worktree, ".runtime", "startup_turn")); err == nil {
		t.Error("the measurement leaked under the worktree root")
	}
	if n := len(scStartupEnds(t, fx.outer, "manager")); n != 1 {
		t.Errorf("startup_end records under the outer root = %d, want 1", n)
	}
	if _, err := os.Stat(config.TelemetryDir(fx.worktree)); err == nil {
		if n := len(scStartupEnds(t, fx.worktree, "manager")); n != 0 {
			t.Errorf("startup_end records under the worktree's telemetry dir = %d, want 0", n)
		}
	}
}
