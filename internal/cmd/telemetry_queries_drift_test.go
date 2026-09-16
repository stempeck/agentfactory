package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/transcript"
)

// TestQueriesDrift is #668 K12's other half: AC-6 says the three reproduction queries recorded
// verbatim at .designs/668/source.md:196-208 must still work UNMODIFIED after this change, and a
// query written in jq against a JSON surface breaks silently — a renamed key yields `null`, and jq
// prints null without complaint. Nothing else in the tree would notice.
//
// The three are not the same kind of artifact and this test does not pretend they are. Query #1
// reads the report surface, so it is EXECUTED here against a seeded store. Queries #2 and #3 read
// Claude Code session transcripts, which are the host's files and cannot be manufactured into a
// verdict — so what is pinned for them is the METHOD: the field names they dereference and the
// counting rule they depend on, each asserted through the shipped code that implements it rather
// than through a string literal restating it.
func TestQueriesDrift(t *testing.T) {
	t.Run("query 1 still reads every key it dereferences", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		enableTelemetryJSON(t)
		seedTelemetryGate(t, root)

		// Two profiles, because query #1's `select(.model=="lmstudio")` is only meaningful if there is
		// something for it to exclude. A single-row fixture would pass the filter vacuously and the
		// test would go on passing after `model` stopped distinguishing anything.
		//
		// The agent name is immaterial to what query #1 dereferences — but it must be on the roster,
		// because the report enumerates agents.json rather than the record files, and a record filed
		// under an unregistered agent is never read.
		for _, ev := range []telemetry.StepEvent{
			{
				V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
				TS: "2026-08-31T10:00:00.000Z", Agent: "manager", Formula: "offpath",
				InstanceID: "af-668-1", StepID: "s-1", StepSeq: 1, StepTitle: "Phase 1 — local",
				Model: "lmstudio", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "prime", VerbMS: 9,
				SessionID: "sess-668-a", CtxTokensUsed: i64p(42_000), CtxTokensTotal: i64p(262_144),
				CtxUsedPct: f64p(16), CtxObservedAt: "2026-08-31T10:00:00.000Z", CumTokens: i64p(42_000),
			},
			{
				V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
				TS: "2026-08-31T10:06:00.000Z", Agent: "manager", Formula: "offpath",
				InstanceID: "af-668-1", StepID: "s-1", StepSeq: 1, StepTitle: "Phase 1 — local",
				Model: "lmstudio", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "done", VerbMS: 11,
				DurationMS: 360_000, Status: telemetry.StatusClosed,
				SessionID: "sess-668-a", CtxTokensUsed: i64p(151_000), CtxTokensTotal: i64p(262_144),
				CtxUsedPct: f64p(57), CtxObservedAt: "2026-08-31T10:06:00.000Z", CumTokens: i64p(151_000),
				CtxTokensStart: i64p(42_000), CumTokensDelta: i64p(109_000),
			},
			{
				V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
				TS: "2026-08-31T11:00:00.000Z", Agent: "manager", Formula: "offpath",
				InstanceID: "af-668-2", StepID: "s-2", StepSeq: 2, StepTitle: "Phase 2 — cloud",
				Model: "claude-opus-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "prime", VerbMS: 7,
				SessionID: "sess-668-b", CtxTokensUsed: i64p(30_000), CtxTokensTotal: i64p(1_000_000),
				CtxUsedPct: f64p(3), CtxObservedAt: "2026-08-31T11:00:00.000Z", CumTokens: i64p(30_000),
			},
			{
				V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
				TS: "2026-08-31T11:04:00.000Z", Agent: "manager", Formula: "offpath",
				InstanceID: "af-668-2", StepID: "s-2", StepSeq: 2, StepTitle: "Phase 2 — cloud",
				Model: "claude-opus-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "done", VerbMS: 6,
				DurationMS: 240_000, Status: telemetry.StatusClosed,
				SessionID: "sess-668-b", CtxTokensUsed: i64p(88_000), CtxTokensTotal: i64p(1_000_000),
				CtxUsedPct: f64p(9), CtxObservedAt: "2026-08-31T11:04:00.000Z", CumTokens: i64p(88_000),
				CtxTokensStart: i64p(30_000), CumTokensDelta: i64p(58_000),
			},
		} {
			if err := telemetry.AppendEvent(config.TelemetryDir(root), ev); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}

		out, err := runTelemetryJSON(t, "report")
		if err != nil {
			t.Fatalf("report --json: %v", err)
		}

		var doc struct {
			Rows []map[string]json.RawMessage `json:"rows"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
			t.Fatalf("unmarshal report: %v\n%s", err, out)
		}
		if len(doc.Rows) != 2 {
			t.Fatalf("len(rows) = %d, want 2", len(doc.Rows))
		}

		// Verbatim from the query's object constructor plus its select and its arithmetic. A rename
		// anywhere in this list turns a jq field access into null and the query into an object of
		// nulls that still exits 0.
		for i, row := range doc.Rows {
			for _, key := range []string{
				"model", "instance_id", "status", "step", "started",
				"duration_ms", "ctx_tokens_start", "ctx_tokens_end",
			} {
				if _, ok := row[key]; !ok {
					t.Errorf("rows[%d] has no %q; reproduction query #1 (.designs/668/source.md:196-200) "+
						"dereferences it and jq would yield null rather than fail", i, key)
				}
			}
		}

		// `.duration_ms/60000` is arithmetic. A duration rendered as the human table's display string
		// would make jq abort with "string and number cannot be divided" — the one failure mode of
		// query #1 that is loud, and still a failure.
		for i, row := range doc.Rows {
			var n json.Number
			dec := json.NewDecoder(strings.NewReader(string(row["duration_ms"])))
			dec.UseNumber()
			if err := dec.Decode(&n); err != nil {
				t.Errorf("rows[%d].duration_ms = %s, which is not a JSON number; query #1 divides it "+
					"by 60000", i, row["duration_ms"])
				continue
			}
			if _, err := n.Int64(); err != nil {
				t.Errorf("rows[%d].duration_ms = %s is not an integer: %v", i, n, err)
			}
		}

		// The select itself: a STRICT subset, so the query separates the local profile from the rest.
		var selected int
		for _, row := range doc.Rows {
			var model string
			if err := json.Unmarshal(row["model"], &model); err != nil {
				t.Fatalf("model is not a string: %s", row["model"])
			}
			if model == "lmstudio" {
				selected++
			}
		}
		if selected != 1 {
			t.Errorf(`select(.model=="lmstudio") matched %d of %d rows, want 1 — the model field must `+
				`still carry the PROFILE name for the local run and something else for the others`,
				selected, len(doc.Rows))
		}
	})

	t.Run("queries 2 and 3 are pinned as a method, not executed", func(t *testing.T) {
		// Said in as many words, because the distinction is the honest part of this test: #2 and #3
		// read Claude Code session transcripts under the host's ~/.claude, which no test may
		// manufacture into a verdict about a real run. What CAN be pinned is that the field names
		// they dereference and the counting rule they were derived under are still the ones the
		// shipped readers implement — so the two sub-assertions below drive real code over a fixture
		// transcript rather than asserting a string that describes them.

		t.Run("query 2: usage field names and the message-dedup counting rule", func(t *testing.T) {
			// One message.id across two records — the shape Claude Code actually writes, one record
			// per content block with the whole message's usage stamped on every one.
			const line = `{"timestamp":%q,"message":{"id":%q,"role":"assistant","content":[{"type":"text","text":%q}],` +
				`"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`
			fixture := strings.Join([]string{
				fmt.Sprintf(line, "2026-08-31T10:01:00Z", "msg_1", "hello", 100, 50, 10, 5),
				fmt.Sprintf(line, "2026-08-31T10:02:00Z", "msg_1", "world!", 100, 900, 10, 5),
			}, "\n") + "\n"

			g := deriveGenerationScalars(strings.NewReader(fixture), "2026-08-31T10:00:00Z", "2026-08-31T10:06:00Z")
			if !g.measured {
				t.Fatal("the fixture measured nothing: one of input_tokens / output_tokens / " +
					"cache_read_input_tokens / cache_creation_input_tokens / message.id has been renamed, " +
					"and reproduction query #2 (.designs/668/source.md:202-205) reads all five")
			}
			// MAX per field, keyed on message.id: 900, not 950 (summed) and not 50 (first-wins).
			// Summing is the arithmetic that produced this feature's superseded headline figures.
			if *g.out != 900 {
				t.Errorf("out_tokens = %d, want 900. 950 means records were SUMMED (over-counts by "+
					"~2.2x); 50 means first-wins kept an in-flight partial", *g.out)
			}
			// input + cache_read + cache_creation + output, which is query #2's stated context depth.
			// Only reachable if all four wire names still decode.
			if *g.peak != 1015 {
				t.Errorf("peak occupancy = %d, want 1015 (100 in + 10 cache-read + 5 cache-creation + "+
					"900 out); query #2 defines context depth as exactly that sum", *g.peak)
			}
			// out - visible-runes/4: "hello" + "world!" is 11 runes, so 900 - 2.
			if *g.thinkEst != 898 {
				t.Errorf("think_tokens_est = %d, want 898; query #2's estimate is "+
					"output - (visible text chars)/4", *g.thinkEst)
			}
		})

		t.Run("query 3: Read tool_use file_path is still addressable", func(t *testing.T) {
			// Query #3 counts artifact re-reads as "Read tool_use file_path frequency". Three names
			// have to survive for that to be countable: the tool name, the block type, and the input key.
			fixture := strings.Join([]string{
				`{"parentUuid":null,"isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"go"}]},` +
					`"promptId":"p_1","type":"user","uuid":"u-1","timestamp":"2026-08-31T10:00:00Z","sessionId":"s","cwd":"/repo"}`,
				`{"parentUuid":null,"isSidechain":false,"message":{"id":"msg_1","role":"assistant","content":[` +
					`{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"/repo/a.go"},"caller":"direct"}]},` +
					`"requestId":"req_1","type":"assistant","uuid":"a-1","timestamp":"2026-08-31T10:00:01Z","sessionId":"s","cwd":"/repo"}`,
			}, "\n") + "\n"

			ev := transcript.Derive(strings.NewReader(fixture), transcript.DefaultOptions())
			if len(ev.Calls) != 1 {
				t.Fatalf("derived %d calls, want 1; the tool_use block shape query #3 counts over has drifted",
					len(ev.Calls))
			}
			if ev.Calls[0].Tool != "Read" {
				t.Errorf("tool = %q, want %q — query #3 counts Read calls by name", ev.Calls[0].Tool, "Read")
			}
			if got := ev.Calls[0].Input["file_path"]; got != "/repo/a.go" {
				t.Errorf("input[file_path] = %q, want %q; query #3's re-read frequency is keyed on that "+
					"input field and an unkeyed one counts every Read as the same artifact",
					got, "/repo/a.go")
			}
		})
	})
}
