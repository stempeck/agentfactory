package mail

import (
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

func TestNewMessage(t *testing.T) {
	msg := NewMessage("manager", "supervisor", "task update", "work done")

	if msg == nil {
		t.Fatal("NewMessage returned nil")
	}
	if !strings.HasPrefix(msg.ID, "msg-") {
		t.Errorf("ID should start with msg-, got %q", msg.ID)
	}
	if !strings.HasPrefix(msg.ThreadID, "thread-") {
		t.Errorf("ThreadID should start with thread-, got %q", msg.ThreadID)
	}
	if msg.From != "manager" {
		t.Errorf("From = %q, want %q", msg.From, "manager")
	}
	if msg.To != "supervisor" {
		t.Errorf("To = %q, want %q", msg.To, "supervisor")
	}
	if msg.Subject != "task update" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "task update")
	}
	if msg.Body != "work done" {
		t.Errorf("Body = %q, want %q", msg.Body, "work done")
	}
	if msg.Read {
		t.Error("new message should be unread")
	}
	if msg.Priority != issuestore.PriorityNormal {
		t.Errorf("Priority = %q, want %q", msg.Priority, issuestore.PriorityNormal)
	}
	if msg.Type != TypeNotification {
		t.Errorf("Type = %q, want %q", msg.Type, TypeNotification)
	}
	if time.Since(msg.Timestamp) > time.Second {
		t.Errorf("Timestamp should be approximately now, got %v", msg.Timestamp)
	}
}

func TestNewReplyMessage(t *testing.T) {
	original := NewMessage("supervisor", "manager", "question", "how?")
	reply := NewReplyMessage("manager", "supervisor", "Re: question", "like this", original)

	if reply == nil {
		t.Fatal("NewReplyMessage returned nil")
	}
	if !strings.HasPrefix(reply.ID, "msg-") {
		t.Errorf("reply ID should start with msg-, got %q", reply.ID)
	}
	if reply.ID == original.ID {
		t.Error("reply should have different ID from original")
	}
	if reply.ThreadID != original.ThreadID {
		t.Errorf("reply ThreadID = %q, want %q (inherited)", reply.ThreadID, original.ThreadID)
	}
	if reply.ReplyTo != original.ID {
		t.Errorf("ReplyTo = %q, want %q", reply.ReplyTo, original.ID)
	}
	if reply.Type != TypeReply {
		t.Errorf("Type = %q, want %q", reply.Type, TypeReply)
	}
}

func TestParsePriority(t *testing.T) {
	tests := []struct {
		input string
		want  issuestore.Priority
	}{
		{"urgent", issuestore.PriorityUrgent},
		{"high", issuestore.PriorityHigh},
		{"normal", issuestore.PriorityNormal},
		{"low", issuestore.PriorityLow},
		{"URGENT", issuestore.PriorityUrgent},
		{"", issuestore.PriorityNormal},
		{"unknown", issuestore.PriorityNormal},
	}
	for _, tt := range tests {
		got := ParsePriority(tt.input)
		if got != tt.want {
			t.Errorf("ParsePriority(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseMessageType(t *testing.T) {
	tests := []struct {
		input string
		want  MessageType
	}{
		{"task", TypeTask},
		{"notification", TypeNotification},
		{"reply", TypeReply},
		{"TASK", TypeTask},
		{"", TypeNotification},
		{"unknown", TypeNotification},
	}
	for _, tt := range tests {
		got := ParseMessageType(tt.input)
		if got != tt.want {
			t.Errorf("ParseMessageType(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAddressToIdentity(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"manager", "manager"},
		{"supervisor", "supervisor"},
		{"", ""},
		{"agent/", "agent"},
		{"agent//", "agent"},
		{" agent ", "agent"},
		{" agent/ ", "agent"},
	}
	for _, tt := range tests {
		got := addressToIdentity(tt.input)
		if got != tt.want {
			t.Errorf("addressToIdentity(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIdentityToAddress(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"manager", "manager"},
		{"supervisor", "supervisor"},
		{"", ""},
		{"agent/", "agent"},
		{"agent//", "agent"},
		{" agent ", "agent"},
		{" agent/ ", "agent"},
	}
	for _, tt := range tests {
		got := identityToAddress(tt.input)
		if got != tt.want {
			t.Errorf("identityToAddress(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGenerateIDUniqueness(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateID()
		if ids[id] {
			t.Fatalf("duplicate ID generated: %s", id)
		}
		ids[id] = true
	}
}
