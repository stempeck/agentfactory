package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Untagged, like every other file that defines a helper the untagged tests call. mail_test.go and
// memory_test.go compile in BOTH lanes and reach for hookEventOf and htmlEscapedFence, so a
// `!integration` tag here breaks `go test -tags=integration` while leaving `make test` green — the
// failure class 868723da, 77ba36c7 and 01c7ea47 each fixed before. The tests below are in-process
// and lane-agnostic; the integration lane's own SessionStart proof uses hookEventOf too.

// htmlEscapedFence is what Go's default encoder turns the opening system-reminder tag into. Spelled
// once, as runtime-built bytes rather than a source literal, so a test asserting the escape is
// ABSENT cannot accidentally contain the very string it is looking for.
var htmlEscapedFence = func() string {
	b, _ := json.Marshal("<system-reminder")
	return strings.Trim(string(b), `"`)
}()

func hookEventOf(t *testing.T, stdout string) string {
	t.Helper()
	var out struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout is not valid hook JSON: %v\n%s", err, stdout)
	}
	return out.HookSpecificOutput.HookEventName
}

// TestEmitHookContext_SilentWriterCostsZeroBytes pins the zero case. A SessionStart array runs every
// writer on every session; a writer with nothing to say must not spend an envelope saying so.
func TestEmitHookContext_SilentWriterCostsZeroBytes(t *testing.T) {
	var buf bytes.Buffer
	emitHookContext(&buf, hookEventSessionStart, "")
	if buf.Len() != 0 {
		t.Errorf("an empty body must write zero bytes, got %d: %q", buf.Len(), buf.String())
	}
}

// TestEmitHookContext_PreservesFencedBlockVerbatim is the reason emitHookContext turns HTML escaping
// off: mail and memory both ship system-reminder-fenced blocks, and the default encoder would
// rewrite every angle bracket.
func TestEmitHookContext_PreservesFencedBlockVerbatim(t *testing.T) {
	body := "<system-reminder>\nA & B are <mail>\n</system-reminder>\n"
	var buf bytes.Buffer
	emitHookContext(&buf, hookEventSessionStart, body)

	stdout := buf.String()
	if strings.Contains(stdout, htmlEscapedFence) {
		t.Errorf("the encoder escaped the fence; SetEscapeHTML(false) is missing:\n%s", stdout)
	}
	if got := decodeAdditionalContext(t, stdout); got != body {
		t.Errorf("additionalContext round-trip changed the block.\n want: %q\n got:  %q", body, got)
	}
	if got := hookEventOf(t, stdout); got != hookEventSessionStart {
		t.Errorf("hookEventName = %q, want %q", got, hookEventSessionStart)
	}
	if n := strings.Count(strings.TrimSpace(stdout), "\n"); n != 0 {
		t.Errorf("a writer must emit exactly ONE object; stdout holds %d newlines:\n%s", n+1, stdout)
	}
}

// TestHookEventNameOr_PrefersThePayloadsOwnEvent proves mail names the event the harness actually
// fired. Mail answers both SessionStart and UserPromptSubmit from one code path, so a constant here
// would mislabel every UserPromptSubmit delivery.
func TestHookEventNameOr_PrefersThePayloadsOwnEvent(t *testing.T) {
	if got := hookEventNameOr(hookPayload{HookEventName: "UserPromptSubmit"}, hookEventSessionStart); got != "UserPromptSubmit" {
		t.Errorf("hookEventNameOr with a named event = %q, want UserPromptSubmit", got)
	}
	if got := hookEventNameOr(hookPayload{}, hookEventSessionStart); got != hookEventSessionStart {
		t.Errorf("hookEventNameOr with no named event = %q, want %q", got, hookEventSessionStart)
	}
}
