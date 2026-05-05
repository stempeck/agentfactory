package issuestore_test

import (
	"encoding/json"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

// TestStatusIsTerminal verifies the D11 / C-1 policy: closed AND done are both
// terminal; no other status is terminal. This is the foundational guarantee
// that drives Filter semantics across all adapters (memstore and mcpstore).
//
// Source: GHERKINSCENARIOS.feature → Feature "Status terminal classification"
func TestStatusIsTerminal(t *testing.T) {
	tests := []struct {
		name string
		s    issuestore.Status
		want bool
	}{
		{"closed is terminal", issuestore.StatusClosed, true},
		{"done is terminal", issuestore.StatusDone, true},
		{"open is not terminal", issuestore.StatusOpen, false},
		{"hooked is not terminal", issuestore.StatusHooked, false},
		{"pinned is not terminal", issuestore.StatusPinned, false},
		{"in_progress is not terminal", issuestore.StatusInProgress, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.s.IsTerminal(); got != tt.want {
				t.Errorf("Status(%q).IsTerminal() = %v, want %v", string(tt.s), got, tt.want)
			}
		})
	}
}

// TestIssueNotesAndCloseReasonJSONRoundTrip verifies that the Issue struct
// has Notes and CloseReason fields with correct JSON tags (no omitempty).
// Source: IMPLREADME_PHASE1.md acceptance criteria 1-2.
func TestIssueNotesAndCloseReasonJSONRoundTrip(t *testing.T) {
	original := issuestore.Issue{
		ID:          "test-1",
		Notes:       "some notes",
		CloseReason: "completed successfully",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Verify JSON keys exist by unmarshaling to map.
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal to map: %v", err)
	}

	if _, ok := m["notes"]; !ok {
		t.Error("JSON output missing \"notes\" key")
	}
	if _, ok := m["close_reason"]; !ok {
		t.Error("JSON output missing \"close_reason\" key")
	}

	// Round-trip: unmarshal back to Issue and verify field values.
	var roundTripped issuestore.Issue
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("json.Unmarshal to Issue: %v", err)
	}

	if roundTripped.Notes != "some notes" {
		t.Errorf("Notes = %q, want %q", roundTripped.Notes, "some notes")
	}
	if roundTripped.CloseReason != "completed successfully" {
		t.Errorf("CloseReason = %q, want %q", roundTripped.CloseReason, "completed successfully")
	}
}

// TestIssueJSONEmptyFieldsPresent verifies that Notes and CloseReason appear
// in JSON output even when empty (no omitempty tag), matching the design's
// dedicated-columns approach (C-10).
func TestIssueJSONEmptyFieldsPresent(t *testing.T) {
	iss := issuestore.Issue{ID: "test-2"}

	data, err := json.Marshal(iss)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if _, ok := m["notes"]; !ok {
		t.Error("JSON output missing \"notes\" key for zero-valued Issue (omitempty not wanted)")
	}
	if _, ok := m["close_reason"]; !ok {
		t.Error("JSON output missing \"close_reason\" key for zero-valued Issue (omitempty not wanted)")
	}
}

// TestStatusValuesMatchBDWireFormat pins the literal string values that the
// pre-#80 bd wire format used. The Python MCP server persists the same string
// values (see py/issuestore); if these constants change, every adapter's
// translation layer breaks silently. This test is the cheapest possible
// canary for that.
func TestStatusValuesMatchBDWireFormat(t *testing.T) {
	tests := []struct {
		s    issuestore.Status
		want string
	}{
		{issuestore.StatusOpen, "open"},
		{issuestore.StatusHooked, "hooked"},
		{issuestore.StatusPinned, "pinned"},
		{issuestore.StatusInProgress, "in_progress"},
		{issuestore.StatusClosed, "closed"},
		{issuestore.StatusDone, "done"},
	}
	for _, tt := range tests {
		if string(tt.s) != tt.want {
			t.Errorf("Status %v = %q, want %q", tt.s, string(tt.s), tt.want)
		}
	}
}
