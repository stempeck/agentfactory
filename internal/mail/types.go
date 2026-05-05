package mail

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

// MessageType classifies the message.
type MessageType string

const (
	TypeTask         MessageType = "task"
	TypeNotification MessageType = "notification"
	TypeReply        MessageType = "reply"
)

// Message represents an inter-agent message with 11 fields.
type Message struct {
	ID        string               `json:"id"`
	From      string               `json:"from"`
	To        string               `json:"to"`
	Subject   string               `json:"subject"`
	Body      string               `json:"body"`
	Timestamp time.Time            `json:"timestamp"`
	Read      bool                 `json:"read"`
	Priority  issuestore.Priority  `json:"priority"`
	Type      MessageType          `json:"type"`
	ThreadID  string               `json:"thread_id,omitempty"`
	ReplyTo   string               `json:"reply_to,omitempty"`
}

// NewMessage creates a new message with generated ID and ThreadID.
func NewMessage(from, to, subject, body string) *Message {
	return &Message{
		ID:        generateID(),
		From:      from,
		To:        to,
		Subject:   subject,
		Body:      body,
		Timestamp: time.Now(),
		Read:      false,
		Priority:  issuestore.PriorityNormal,
		Type:      TypeNotification,
		ThreadID:  generateThreadID(),
	}
}

// NewReplyMessage creates a reply that inherits the original's ThreadID.
func NewReplyMessage(from, to, subject, body string, original *Message) *Message {
	return &Message{
		ID:        generateID(),
		From:      from,
		To:        to,
		Subject:   subject,
		Body:      body,
		Timestamp: time.Now(),
		Read:      false,
		Priority:  issuestore.PriorityNormal,
		Type:      TypeReply,
		ThreadID:  original.ThreadID,
		ReplyTo:   original.ID,
	}
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "msg-" + hex.EncodeToString(b)
}

func generateThreadID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "thread-" + hex.EncodeToString(b)
}

// ParsePriority parses a string to Priority, defaulting to Normal.
func ParsePriority(s string) issuestore.Priority {
	switch strings.ToLower(s) {
	case "urgent":
		return issuestore.PriorityUrgent
	case "high":
		return issuestore.PriorityHigh
	case "normal":
		return issuestore.PriorityNormal
	case "low":
		return issuestore.PriorityLow
	default:
		return issuestore.PriorityNormal
	}
}

// ParseMessageType parses a string to MessageType, defaulting to Notification.
func ParseMessageType(s string) MessageType {
	switch strings.ToLower(s) {
	case "task":
		return TypeTask
	case "notification":
		return TypeNotification
	case "reply":
		return TypeReply
	default:
		return TypeNotification
	}
}

// addressToIdentity converts an address to an identity.
// Normalizes by stripping whitespace and trailing slashes.
func addressToIdentity(address string) string {
	return strings.TrimRight(strings.TrimSpace(address), "/")
}

// identityToAddress converts an identity to an address.
// Normalizes by stripping whitespace and trailing slashes.
func identityToAddress(identity string) string {
	return strings.TrimRight(strings.TrimSpace(identity), "/")
}
