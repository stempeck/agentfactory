package mail

import (
	"strings"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

// issueToMessage converts an issuestore.Issue (as returned by mailbox
// operations that go through the Store) into a mail.Message.
//
// C-1 (D11): Read is set from Status.IsTerminal(), NOT from a sentinel
// comparison like `== StatusClosed`. Without this, mail in `done`,
// `in_progress`, `hooked`, or `pinned` status silently re-surfaces as
// unread — the R-DATA-3 high-severity risk.
func issueToMessage(iss issuestore.Issue) *Message {
	sender, threadID, replyTo, msgType := parseLabels(iss.Labels)
	return &Message{
		ID:        iss.ID,
		From:      sender,
		To:        addressToIdentity(iss.Assignee),
		Subject:   iss.Title,
		Body:      iss.Description,
		Timestamp: iss.CreatedAt,
		Read:      iss.Status.IsTerminal(), // C-1 fix
		Priority:  iss.Priority,
		Type:      ParseMessageType(msgType),
		ThreadID:  threadID,
		ReplyTo:   replyTo,
	}
}

// messageToCreateParams builds the CreateParams used by Router.sendToSingle
// when calling store.Create. It encapsulates the mail wire format (labels
// and --actor mapping) so router.go no longer needs to know about bd flags.
func messageToCreateParams(from string, msg *Message) issuestore.CreateParams {
	return issuestore.CreateParams{
		Title:       msg.Subject,
		Description: msg.Body,
		Assignee:    identityToAddress(msg.To),
		Type:        issuestore.TypeTask,
		Actor:       from,
		Priority:    msg.Priority,
		Labels:      buildLabels(msg.From, msg.To, msg.ThreadID, string(msg.Type), msg.ReplyTo),
	}
}

// parseLabels extracts sender, threadID, replyTo, and msgType from the
// labels slice. Label wire format (data.md): "from:<x>", "thread:<x>",
// "reply-to:<x>", "msg-type:<x>". The leading "mail:true" and "to:<x>"
// entries are not consumed by this function but must be preserved in the
// list.
func parseLabels(labels []string) (sender, threadID, replyTo, msgType string) {
	for _, label := range labels {
		parts := strings.SplitN(label, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]
		switch key {
		case "from":
			sender = val
		case "thread":
			threadID = val
		case "reply-to":
			replyTo = val
		case "msg-type":
			msgType = val
		}
	}
	return
}

// buildLabels produces the exact wire format used by router.sendToSingle
// today: mail:true,from:<from>,to:<to>,thread:<thread>,msg-type:<type>
// with an optional trailing reply-to:<id>. Set-equality round-trip is
// required to keep R-INT-1 green; bd sorts labels alphabetically at
// storage, so wire-order preservation is tested against buildLabels'
// output, not against bd's stored form.
func buildLabels(from, to, threadID, msgType, replyTo string) []string {
	labels := []string{
		"mail:true",
		"from:" + from,
		"to:" + identityToAddress(to),
		"thread:" + threadID,
		"msg-type:" + msgType,
	}
	if replyTo != "" {
		labels = append(labels, "reply-to:"+replyTo)
	}
	return labels
}
