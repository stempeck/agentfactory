package mail

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

func TestIssueToMessage_ReadFromIsTerminal(t *testing.T) {
	// C-1 (D11 / R-DATA-3): Read must be true for closed AND done, false
	// for open / hooked / pinned / in_progress. A naive implementation
	// compared against "closed" only and silently re-surfaced mail in
	// done/in_progress/hooked/pinned as unread.
	tests := []struct {
		status   issuestore.Status
		wantRead bool
	}{
		{issuestore.StatusOpen, false},
		{issuestore.StatusHooked, false},
		{issuestore.StatusPinned, false},
		{issuestore.StatusInProgress, false},
		{issuestore.StatusClosed, true},
		{issuestore.StatusDone, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			iss := issuestore.Issue{
				ID:        "test-1",
				Title:     "hello",
				Status:    tt.status,
				CreatedAt: time.Now(),
				Labels: []string{
					"mail:true", "from:sender", "to:recipient",
					"thread:t", "msg-type:task",
				},
			}
			msg := issueToMessage(iss)
			if msg.Read != tt.wantRead {
				t.Errorf("status=%q Read=%v want %v",
					tt.status, msg.Read, tt.wantRead)
			}
		})
	}
}

func TestParseLabels(t *testing.T) {
	tests := []struct {
		name                                          string
		labels                                        []string
		wantSender, wantThread, wantReplyTo, wantType string
	}{
		{
			name:        "full_set",
			labels:      []string{"mail:true", "from:manager", "to:supervisor", "thread:t-1", "msg-type:task", "reply-to:msg-xyz"},
			wantSender:  "manager",
			wantThread:  "t-1",
			wantReplyTo: "msg-xyz",
			wantType:    "task",
		},
		{
			name:       "no_reply_to",
			labels:     []string{"from:manager", "thread:t-2", "msg-type:notification"},
			wantSender: "manager",
			wantThread: "t-2",
			wantType:   "notification",
		},
		{
			name:   "empty",
			labels: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, th, rt, mt := parseLabels(tt.labels)
			if s != tt.wantSender || th != tt.wantThread ||
				rt != tt.wantReplyTo || mt != tt.wantType {
				t.Errorf("got (%q,%q,%q,%q), want (%q,%q,%q,%q)",
					s, th, rt, mt,
					tt.wantSender, tt.wantThread, tt.wantReplyTo, tt.wantType)
			}
		})
	}
}

func TestBuildLabels_WireFormat(t *testing.T) {
	got := buildLabels("manager", "supervisor", "t-1", "task", "")
	want := []string{
		"mail:true",
		"from:manager",
		"to:supervisor",
		"thread:t-1",
		"msg-type:task",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildLabels = %v, want %v", got, want)
	}

	// With reply-to
	gotReply := buildLabels("manager", "supervisor", "t-1", "reply", "msg-orig")
	if gotReply[len(gotReply)-1] != "reply-to:msg-orig" {
		t.Errorf("buildLabels with reply-to: tail = %q, want %q",
			gotReply[len(gotReply)-1], "reply-to:msg-orig")
	}
}

func TestBuildLabels_NormalizesRecipient(t *testing.T) {
	// identityToAddress strips trailing slashes; buildLabels must go through
	// it so the label stays "to:supervisor" not "to:supervisor/".
	got := buildLabels("manager", "supervisor/", "t-1", "task", "")
	for _, l := range got {
		if l == "to:supervisor/" {
			t.Errorf("to: label not normalized, got %q", l)
		}
		if strings.HasPrefix(l, "to:") && l != "to:supervisor" {
			t.Errorf("to: label = %q, want %q", l, "to:supervisor")
		}
	}
}

func TestMessageToCreateParams_RoundTripsThroughParseLabels(t *testing.T) {
	msg := NewMessage("manager", "supervisor", "subject", "body")
	msg.Type = TypeTask
	params := messageToCreateParams(msg.From, msg)

	sender, threadID, _, msgType := parseLabels(params.Labels)
	if sender != msg.From {
		t.Errorf("sender round-trip: got %q, want %q", sender, msg.From)
	}
	if threadID != msg.ThreadID {
		t.Errorf("thread round-trip: got %q, want %q", threadID, msg.ThreadID)
	}
	if msgType != string(msg.Type) {
		t.Errorf("type round-trip: got %q, want %q", msgType, string(msg.Type))
	}
	if params.Title != msg.Subject {
		t.Errorf("title: got %q, want %q", params.Title, msg.Subject)
	}
	if params.Description != msg.Body {
		t.Errorf("description: got %q, want %q", params.Description, msg.Body)
	}
	if params.Type != issuestore.TypeTask {
		t.Errorf("type: got %q, want %q", params.Type, issuestore.TypeTask)
	}
	if params.Assignee != msg.To {
		t.Errorf("assignee: got %q, want %q", params.Assignee, msg.To)
	}
	if params.Actor != msg.From {
		t.Errorf("actor: got %q, want %q", params.Actor, msg.From)
	}
}
