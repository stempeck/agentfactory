package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// Records captured from logs written by a binary that predates #668. They are byte literals
// rather than marshalled structs on purpose: a fixture built by marshalling the CURRENT struct
// re-derives its expectation from the thing under test and would agree with any change at all.
//
// Key order is Go struct declaration order, which is what encoding/json emits.
const (
	// A fully measured step_end: every optional key present, which is what proves a new field
	// cannot displace or reorder an existing one.
	preTokenomicsMeasuredRecord = `{"v":1,"event":"step_end","ts":"2026-07-22T18:31:04.112Z",` +
		`"agent":"design-v7","worktree_id":"wt-03478d","formula":"design-v7",` +
		`"instance_id":"af-329-instance","step_id":"phase2-analysis","step_seq":7,` +
		`"step_title":"Phase 2: analysis","session_id":"5f2c1d90-0000-4000-8000-000000000001",` +
		`"model":"fable-5","model_source":"models_json","verb":"done","verb_ms":42,` +
		`"ctx_used_pct":37.5,"ctx_tokens_used":75000,"ctx_tokens_total":200000,` +
		`"ctx_observed_at":"2026-07-22T18:31:02Z","cum_tokens":412000,` +
		`"duration_ms":6388,"status":"closed","ctx_tokens_start":21000,` +
		`"cum_tokens_delta":54000,"ctx_bound_tokens":200000}`

	// A record from before anything measured occupancy — the shape most of a retained backlog
	// is actually in, and the one a field missing omitempty grows a key on.
	preTokenomicsUnmeasuredRecord = `{"v":1,"event":"step_start","ts":"2026-07-19T09:02:11.004Z",` +
		`"agent":"manager","worktree_id":"","formula":"bootstrap","instance_id":"af-1-instance",` +
		`"step_id":"kickoff","step_seq":1,"step_title":"Kickoff","session_id":"",` +
		`"model":"","model_source":"unknown","verb":"prime","verb_ms":7}`

	// An instance_start, the kind the formula digest rides on.
	preTokenomicsInstanceStartRecord = `{"v":1,"event":"instance_start","ts":"2026-07-19T09:02:10.500Z",` +
		`"agent":"manager","worktree_id":"","formula":"bootstrap","instance_id":"af-1-instance",` +
		`"step_id":"","step_seq":0,"step_title":"","session_id":"","model":"fable-5",` +
		`"model_source":"models_json","verb":"sling","verb_ms":31}`
)

// TestExistingRecordsByteStable is the guard on the digest-bookmark invariant StepEvent's doc states.
//
// recordDigest (store.go) is the export cursor's bookmark, and it is computed from the
// RE-MARSHALLED struct while the value it is compared against was persisted by whichever binary
// wrote it last. So a record written before a field existed has to re-marshal to the same bytes
// after the upgrade. A field added without `omitempty` breaks that silently: it serialises as an
// explicit null or a 0 on every old record, every digest moves, the persisted bookmark matches
// nothing, and store.go's "a bookmark it cannot find can only be older than everything present"
// arm re-exports the entire retained backlog once.
//
// go build and go vet stay silent for that mistake. This test is what does not.
func TestExistingRecordsByteStable(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"a fully measured step_end", preTokenomicsMeasuredRecord},
		{"a step_start that measured nothing", preTokenomicsUnmeasuredRecord},
		{"an instance_start", preTokenomicsInstanceStartRecord},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ev StepEvent
			if err := json.Unmarshal([]byte(tc.raw), &ev); err != nil {
				t.Fatalf("decoding a record this binary must still be able to read: %v", err)
			}
			got, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("re-marshalling: %v", err)
			}
			if string(got) != tc.raw {
				t.Errorf("a record written before this schema addition did not re-marshal to identical "+
					"bytes, so its recordDigest moves and the whole retained backlog re-exports once.\n"+
					"Every new field must be tagged omitempty.\nwant: %s\ngot:  %s", tc.raw, got)
			}

			// The bookmark itself, stated as the invariant rather than as a magic constant: the
			// digest this binary computes from the decoded record must equal the digest of the
			// bytes that are actually on disk.
			sum := sha256.Sum256([]byte(tc.raw))
			if want := hex.EncodeToString(sum[:16]); recordDigest(ev) != want {
				t.Errorf("recordDigest(decoded) = %s, but the on-disk bytes digest to %s; "+
					"an export cursor holding the second value would fail to find its own bookmark",
					recordDigest(ev), want)
			}
		})
	}
}

// preTokenomicsKeys is the record's key set as it stood before #668, spelled out as the historical
// fact it is. It must NOT be derived from allowlist: allowlist grows with the schema, and a
// pre-#668 record is precisely the thing that does not have the new keys.
//
// It overlaps event_test.go's alwaysPresentKeys by fifteen entries and is deliberately not merged
// with it. That list is "keys every record carries whatever its kind"; this one is "the key set as
// of one moment in history". They agree today by coincidence of schema, not by definition, and a
// merge would make the next additive change quietly rewrite history.
var preTokenomicsKeys = []string{
	"v", "event", "ts", "agent", "worktree_id", "formula", "instance_id",
	"step_id", "step_seq", "step_title", "session_id", "model", "model_source",
	"verb", "verb_ms",
	"ctx_used_pct", "ctx_tokens_used", "ctx_tokens_total", "ctx_observed_at", "cum_tokens",
	"duration_ms", "status", "ctx_tokens_start", "cum_tokens_delta", "ctx_bound_tokens",
}

// TestExistingRecordsByteStableIsNotVacuous proves the fixtures above would actually catch the
// mistake they exist for. A byte-stability fixture only proves stability for the keys it contains,
// so one that silently stopped covering the optional fields would keep passing forever.
func TestExistingRecordsByteStableIsNotVacuous(t *testing.T) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(preTokenomicsMeasuredRecord), &keys); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	for _, k := range preTokenomicsKeys {
		if _, present := keys[k]; !present {
			t.Errorf("the measured fixture omits %q, so it cannot prove that key stays byte-stable", k)
		}
	}
	if len(keys) != len(preTokenomicsKeys) {
		t.Errorf("the measured fixture carries %d keys, want the %d that existed before #668",
			len(keys), len(preTokenomicsKeys))
	}
}
