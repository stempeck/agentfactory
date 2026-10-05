package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Phase 5 (K14, issue #693) pinning test for the Required-change AC "Add a --first flag
// ... to liveSmokeModel/liveSmokeRows" (IMPLREADME_PHASE5.md). Mirrors
// TestConfigModelsCheck_ModelCoverage_LiveFlagRegistered's own shape for the sibling flag.
func TestConfigModelsCheck_FirstFlagRegistered(t *testing.T) {
	f := configModelsCheckCmd.Flags().Lookup("first")
	if f == nil {
		t.Fatal("`af config models check` must register --first for the single-shot, 30s-deadline classified probe")
	}
	if f.DefValue != "false" {
		t.Errorf("--first must default to false so no default-suite run performs one; got %q", f.DefValue)
	}
}

// enableFirstProbe substitutes modelsMessagesDo (the same ADR-009 seam enableLiveSmoke uses) so
// firstProbe's classification runs with no network. respond is called once per request sent
// (including the 429 retry); the returned slice records how many requests were actually made.
func enableFirstProbe(t *testing.T, respond func(attempt int) (*http.Response, error)) *int {
	t.Helper()
	orig := modelsMessagesDo
	calls := 0
	modelsMessagesDo = func(req *http.Request) (*http.Response, error) {
		calls++
		return respond(calls)
	}
	t.Cleanup(func() { modelsMessagesDo = orig })
	return &calls
}

// TestFirstProbeClassification pins the AC's classification matrix — "distinct verdicts for
// 401/403, 404, 429 (after one backoff retry), 5xx and timeout" — behaviorally, through the same
// modelsMessagesDo seam liveSmokeModel already uses, exactly the seam the DO-NOT-CHANGE section
// names as built for no-network unit testing.
func TestFirstProbeClassification(t *testing.T) {
	cases := []struct {
		name        string
		respond     func(attempt int) (*http.Response, error)
		wantVerdict firstProbeVerdict
		wantCalls   int
		wantErrNil  bool
	}{
		{"401 unauthorized", func(int) (*http.Response, error) { return liveAnswer(401, `{"type":"error"}`) }, firstProbeAuth, 1, false},
		{"403 forbidden", func(int) (*http.Response, error) { return liveAnswer(403, `{"type":"error"}`) }, firstProbeAuth, 1, false},
		{"404 not found", func(int) (*http.Response, error) { return liveAnswer(404, `{"type":"error"}`) }, firstProbeNotFound, 1, false},
		{"500 server error", func(int) (*http.Response, error) { return liveAnswer(500, `{"type":"error"}`) }, firstProbeServerError, 1, false},
		{"503 server error", func(int) (*http.Response, error) { return liveAnswer(503, `{"type":"error"}`) }, firstProbeServerError, 1, false},
		{"200 ok", func(int) (*http.Response, error) { return liveAnswer(200, streamedMessageBody) }, firstProbeOK, 1, true},
		{"200 unstreamed json message is unexpected", func(int) (*http.Response, error) { return liveAnswer(200, `{"type":"message"}`) }, firstProbeUnexpected, 1, false},
		{"200 streamed error event is unexpected", func(int) (*http.Response, error) { return liveAnswer(200, streamedErrorBody) }, firstProbeUnexpected, 1, false},
		{"400 unexpected", func(int) (*http.Response, error) { return liveAnswer(400, `{"type":"error"}`) }, firstProbeUnexpected, 1, false},
		{
			"429 then ok on retry", func(attempt int) (*http.Response, error) {
				if attempt == 1 {
					return liveAnswer(429, `{"type":"error"}`)
				}
				return liveAnswer(200, streamedMessageBody)
			}, firstProbeOK, 2, true,
		},
		{
			"429 twice stays rate-limited (D2: not a distinct bucket)", func(int) (*http.Response, error) {
				return liveAnswer(429, `{"type":"error"}`)
			}, firstProbeRateLimited, 2, false,
		},
		{
			"transport error resolves to timeout", func(int) (*http.Response, error) {
				return nil, http.ErrHandlerTimeout
			}, firstProbeTimeout, 1, false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := enableFirstProbe(t, tc.respond)
			verdict, err := firstProbe("https://gw.example:4000", "sk-test", "gpt-5.6-sol")
			if verdict != tc.wantVerdict {
				t.Errorf("firstProbe verdict = %q, want %q", verdict, tc.wantVerdict)
			}
			if (err == nil) != tc.wantErrNil {
				t.Errorf("firstProbe err = %v, want nil=%t", err, tc.wantErrNil)
			}
			if *calls != tc.wantCalls {
				t.Errorf("modelsMessagesDo called %d time(s), want %d (429 retries exactly once, every other status sends exactly one request)", *calls, tc.wantCalls)
			}
		})
	}
}

// TestFirstProbeSendsFirstProbeDeadlineNotLiveSmokeDeadline pins the AC's other headline claim:
// --first's request carries the distinct 30s budget, never liveSmokeDeadline's 300s.
func TestFirstProbeSendsFirstProbeDeadlineNotLiveSmokeDeadline(t *testing.T) {
	var sawDeadline bool
	orig := modelsMessagesDo
	modelsMessagesDo = func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Error("firstProbe's request must carry a context deadline")
		}
		remaining := time.Until(deadline)
		// A generous upper bound: well under liveSmokeDeadline (300s), consistent with
		// firstProbeDeadline (30s) having just been applied rather than liveSmokeDeadline.
		if remaining > liveSmokeDeadline {
			t.Errorf("firstProbe's request deadline (%v remaining) must not use liveSmokeDeadline's 300s budget", remaining)
		}
		sawDeadline = true
		return liveAnswer(200, streamedMessageBody)
	}
	t.Cleanup(func() { modelsMessagesDo = orig })
	if _, err := firstProbe("https://gw.example:4000", "sk-test", "gpt-5.6-sol"); err != nil {
		t.Fatalf("firstProbe: %v", err)
	}
	if !sawDeadline {
		t.Fatal("modelsMessagesDo was never called")
	}
}

// TestFirstProbeSurfacesTheGatewayErrorText: the verdict line is what an operator reads and what
// quickstart.sh prints on a non-auth failure, so it must carry the gateway's own error message —
// the searchable key to an upstream defect — never the status line alone.
func TestFirstProbeSurfacesTheGatewayErrorText(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"litellm 500 envelope", 500, `{"error":{"message":"litellm.APIConnectionError: ChatgptException - Unknown items in responses API response: []. Received Model Group=gpt-5.6-sol","type":null,"code":"500"}}`, "Unknown items in responses API response"},
		{"litellm 400 envelope", 400, `{"error":{"message":"litellm.BadRequestError: ChatgptException - {\"detail\":\"System messages are not allowed\"}","type":null,"code":"400"}}`, "System messages are not allowed"},
		{"anthropic-style envelope", 404, `{"type":"error","error":{"type":"not_found_error","message":"model: nope"}}`, "model: nope"},
		{"plain text body", 502, "upstream connect error\n", "upstream connect error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enableFirstProbe(t, func(int) (*http.Response, error) { return liveAnswer(tc.status, tc.body) })
			_, err := firstProbe("https://gw.example:4000", "sk-test", "gpt-5.6-sol")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("verdict error = %v, want it to carry %q", err, tc.want)
			}
		})
	}
}

// TestFirstProbeSendsASessionShapedTurn pins the request to the shape a Claude Code session sends:
// streamed, with a block-array system prompt. A bare user message was answered by a gateway that
// then refused every real turn ("System messages are not allowed"), so the probe certified nothing.
func TestFirstProbeSendsASessionShapedTurn(t *testing.T) {
	var sent []byte
	orig := modelsMessagesDo
	modelsMessagesDo = func(req *http.Request) (*http.Response, error) {
		var err error
		if sent, err = io.ReadAll(req.Body); err != nil {
			t.Fatalf("reading the probe body: %v", err)
		}
		return liveAnswer(200, streamedMessageBody)
	}
	t.Cleanup(func() { modelsMessagesDo = orig })
	if _, err := firstProbe("https://gw.example:4000", "sk-test", "gpt-5.6-sol"); err != nil {
		t.Fatalf("firstProbe: %v", err)
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		System    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("probe body is not JSON: %v (%s)", err, sent)
	}
	if body.Model != "gpt-5.6-sol" || body.MaxTokens != 16 || !body.Stream {
		t.Errorf("probe must stream a 16-token turn for the named model; got %s", sent)
	}
	if len(body.System) != 1 || body.System[0].Type != "text" || body.System[0].Text == "" {
		t.Errorf("probe must carry one text block as its system prompt, the shape a session sends; got %s", sent)
	}
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content == "" {
		t.Errorf("probe must carry exactly one user message; got %s", sent)
	}
}
