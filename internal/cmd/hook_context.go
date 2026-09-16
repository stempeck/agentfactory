package cmd

import (
	"encoding/json"
	"io"
)

// hookEventSessionStart is the event name a SessionStart writer declares when its own stdin payload
// did not name one — the only event the SessionStart hook array is ever fired for.
const hookEventSessionStart = "SessionStart"

// emitHookContext writes one hookSpecificOutput.additionalContext object: the structured channel
// the harness reads a hook's contribution from, as opposed to plain stdout, which it treats as one
// budgeted string shared by every writer in the array.
//
// An empty body writes ZERO BYTES, never an empty envelope. A writer with nothing to say must cost
// nothing to say it, and `{"hookSpecificOutput":{...,"additionalContext":""}}` is 70-odd bytes of
// noise per silent session.
//
// SetEscapeHTML(false) is a deliberate divergence from the encoder's default: mail and memory blocks
// are fenced in <system-reminder> tags, and the default encoder rewrites every angle bracket as a
// six-character unicode escape. That still decodes to the right string, but it inflates each fenced
// block and leaves the on-the-wire output unreadable to anyone debugging a session by eye.
//
// Both properties are new to the two pre-existing emitters that now delegate here
// (emitAdditionalContext, emitSubagentContext): they hand-rolled an escaping encoder and wrote an
// empty envelope for an empty body. Neither can reach the zero case — containment's body is a
// constant-format Sprintf and the subagent nudge is only emitted after a non-empty relay — so the
// unification changes escaping only.
func emitHookContext(out io.Writer, event, body string) {
	if body == "" {
		return
	}
	var payload struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	payload.HookSpecificOutput.HookEventName = event
	payload.HookSpecificOutput.AdditionalContext = body

	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(&payload)
}
