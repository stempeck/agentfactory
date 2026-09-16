package cmd

import (
	"embed"
	"encoding/json"
	"path/filepath"
	"testing"
)

// Untagged because both halves of the LIVE-PROBE tier read these fixtures: the default-suite replay
// (dispatch_deny_fixture_replay_test.go) judges the shipped decision code against them, and the
// integration probe re-judges THEM against a live payload. A fixture only one side can read is a
// fixture nothing can falsify.
//
// See testdata/dispatch_README.md for what "captured" means here and what it does not.

const dispatchFixtureCLIVersion = "2.1.258 (Claude Code)"

// Embedded rather than read off disk because the integration probe is also run as a COMPILED test
// binary from outside the repository — that is how the CI-only isolation guard (#389) is honoured
// rather than bypassed — and a fixture found only by walking up to go.mod is not available there.
//
//go:embed testdata/dispatch_stop_payload_2_1_258.json testdata/dispatch_sidechain_timeline_2_1_258.json
var dispatchFixtures embed.FS

type dispatchFixtureFile struct {
	Name       string `json:"name"`
	AgeSeconds int    `json:"age_seconds"`
}

type dispatchSidechainCase struct {
	Name string `json:"name"`
	Why  string `json:"why"`
	// A nil Files is "the directory does not exist", which is a different fact from an empty one and
	// must produce the same retain answer by a different route (Glob's (nil, nil) leg).
	Files        *[]dispatchFixtureFile `json:"files"`
	Measured     bool                   `json:"measured"`
	QuietSeconds int                    `json:"quiet_seconds"`
	Releasable   bool                   `json:"releasable"`
}

type dispatchFixtureStamp struct {
	Fixture       string `json:"fixture"`
	V             int    `json:"v"`
	CLIVersion    string `json:"cli_version"`
	Provenance    string `json:"provenance"`
	CapturedAt    string `json:"captured_at"`
	CaptureMethod string `json:"capture_method"`
}

type dispatchStopFixture struct {
	dispatchFixtureStamp
	Payloads []json.RawMessage `json:"payloads"`
}

type dispatchSidechainFixture struct {
	dispatchFixtureStamp
	SessionID string                  `json:"session_id"`
	Cases     []dispatchSidechainCase `json:"cases"`
}

// readDispatchFixture is deliberately fatal on every failure leg. A committed fixture that has gone
// missing is a broken test, and a test that skips itself when its own evidence disappears is exactly
// the vacuous green #673 is closing.
func readDispatchFixture(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := dispatchFixtures.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("the committed fixture %s is unreadable: %v — it is captured from a real CLI run and "+
			"cannot be regenerated from our own code, so this is a failure, not a skip", name, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("fixture %s does not decode: %v", name, err)
	}
}
