package mail

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

// Router dispatches messages to agents with group fan-out support.
type Router struct {
	factoryRoot string
	store       issuestore.Store
	agentsCfg   *config.AgentConfig
	msgCfg      *config.MessagingConfig
}

// NewRouter creates a Router rooted at an ALREADY-VALIDATED factory root and using
// the injected Store for issue persistence. It does NOT resolve the root from a
// working directory: ambient cwd→root resolution here would launder around the
// internal/cmd resolveInvokerRoot seam (the #519 cross-check), so the cmd layer —
// which already holds the validated root at every call site — must pass it in
// (issue #519 review follow-up, thread 7a). The internal/cmd drift guard enforces
// that this package never reintroduces config.FindFactoryRoot.
func NewRouter(factoryRoot string, store issuestore.Store) (*Router, error) {
	agentsPath := config.AgentsConfigPath(factoryRoot)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return nil, fmt.Errorf("creating router: %w", err)
	}

	msgPath := config.MessagingConfigPath(factoryRoot)
	msgCfg, err := config.LoadMessagingConfig(msgPath, agentsCfg)
	if err != nil {
		return nil, fmt.Errorf("creating router: %w", err)
	}

	return &Router{
		factoryRoot: factoryRoot,
		store:       store,
		agentsCfg:   agentsCfg,
		msgCfg:      msgCfg,
	}, nil
}

// Delivery reports what a send actually did. Filing the bead and notifying a
// live session are SEPARATE facts, not one "sent" flag: a send to a stopped or
// never-started agent writes the bead and tells nobody, so a caller that
// reported "sent" would be claiming something it cannot know (D-7,
// .designs/562/design-doc.md:163; cross-review C-1).
type Delivery struct {
	Filed    bool   // the mail bead was persisted
	Notified bool   // a live Claude session received the tmux banner
	Reason   string // why Notified is what it is, for callers that report it
}

// Reasons a Delivery gives for its Notified value. They are part of what
// `af mail send --report-delivery` prints, so they are phrased for a reader.
const (
	reasonNotified     = "notified"
	reasonNoSession    = "no session"
	reasonProbeFailed  = "session probe failed"
	reasonNotRunning   = "not running"
	reasonBannerFailed = "banner failed"
	reasonNoRecipients = "no recipients"
)

// Send dispatches a message. Its signature is deliberately unchanged: sling's
// dispatch-failure mail and the containment alarm call it in positions that a
// second return value would break at compile time.
func (r *Router) Send(ctx context.Context, msg *Message) error {
	_, err := r.SendReporting(ctx, msg)
	return err
}

// SendReporting dispatches a message and reports what happened. If To starts
// with "@", resolves the group and fans out.
func (r *Router) SendReporting(ctx context.Context, msg *Message) (Delivery, error) {
	if strings.HasPrefix(msg.To, "@") {
		return r.sendToGroup(ctx, msg)
	}
	return r.sendToSingle(ctx, msg)
}

func (r *Router) sendToSingle(ctx context.Context, msg *Message) (Delivery, error) {
	msg.To = identityToAddress(msg.To)
	params := messageToCreateParams(msg.From, msg)
	if _, err := r.store.Create(ctx, params); err != nil {
		return Delivery{}, fmt.Errorf("sending to %s: %w", msg.To, err)
	}

	// A send always wakes its recipient: the mail system exists to notify a live
	// session that it has mail. The containment alarm at
	// internal/cmd/containment.go:411-435 is a deliberate self-addressed wake and
	// relies on this.
	notified, reason := r.notifyRecipient(msg)
	return Delivery{Filed: true, Notified: notified, Reason: reason}, nil
}

func (r *Router) sendToGroup(ctx context.Context, msg *Message) (Delivery, error) {
	members, err := r.ResolveGroupAddress(msg.To)
	if err != nil {
		return Delivery{}, err
	}

	var errs []string
	var attempted, filed, notified int
	for _, member := range members {
		if member == msg.From {
			continue // skip sender
		}
		individual := *msg
		individual.To = member
		attempted++
		d, err := r.sendToSingle(ctx, &individual)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", member, err))
			continue
		}
		if d.Filed {
			filed++
		}
		if d.Notified {
			notified++
		}
	}

	if len(errs) > 0 {
		return Delivery{Filed: filed > 0}, fmt.Errorf("group send partial failure: %s", strings.Join(errs, "; "))
	}
	return groupDelivery(attempted, filed, notified), nil
}

// groupDelivery aggregates a fan-out conservatively: the send may claim
// "notified" only when EVERY attempted member was notified. Reporting a
// three-member broadcast as notified because one member happened to be awake is
// the same over-claim --report-delivery exists to stop.
func groupDelivery(attempted, filed, notified int) Delivery {
	d := Delivery{Filed: attempted > 0 && filed == attempted}
	switch {
	case attempted == 0:
		d.Reason = reasonNoRecipients
	case notified == attempted:
		d.Notified = true
		d.Reason = reasonNotified
	default:
		d.Reason = fmt.Sprintf("notified %d of %d recipients", notified, attempted)
	}
	return d
}

// ResolveGroupAddress resolves a group address to individual agent names.
func (r *Router) ResolveGroupAddress(addr string) ([]string, error) {
	groupName := strings.TrimPrefix(addr, "@")

	if groupName == "all" {
		names := make([]string, 0, len(r.agentsCfg.Agents))
		for name := range r.agentsCfg.Agents {
			names = append(names, name)
		}
		return names, nil
	}

	members, ok := r.msgCfg.Groups[groupName]
	if !ok {
		known := make([]string, 0, len(r.msgCfg.Groups)+1)
		known = append(known, "all")
		for g := range r.msgCfg.Groups {
			known = append(known, g)
		}
		sort.Strings(known)
		return nil, fmt.Errorf("unknown group: @%s (known groups: %s; agents are addressed by bare name, e.g. \"manager\")", groupName, strings.Join(known, ", "))
	}
	return members, nil
}

// routerTmux is the subset of *tmux.Tmux that notifyRecipient drives.
type routerTmux interface {
	HasSession(name string) (bool, error)
	IsClaudeRunning(session string) bool
	SendNotificationBanner(session, from, subject string) error
}

var _ routerTmux = (*tmux.Tmux)(nil)

// newRouterTmux is the ADR-009 seam over this package's one tmux callout. It
// exists so a test can observe whether the banner was pushed: the default-build
// guard answers HasSession with (false, nil) for every name, so against the real
// client "no banner was sent" is true either way, and --report-delivery's
// "notified vs filed" assertion would be vacuous. .designs/317/design-doc.md:420
// declined a seam here as a SAFETY control — that decision stands and is
// unaffected; this one is an observability seam and deliberately has no
// isTestBinary() branch, which would no-op the production path under test
// (internal/cmd/containment.go:405-411).
var newRouterTmux = func() routerTmux { return tmux.NewTmux() }

// notifyRecipient pushes a notification banner into the recipient's tmux
// session and reports whether it landed. It is still best-effort — no failure
// here fails the send — but the outcome is no longer swallowed, because
// --report-delivery has to distinguish "told someone" from "told nobody".
func (r *Router) notifyRecipient(msg *Message) (bool, string) {
	sessionName := session.SessionName(msg.To)
	t := newRouterTmux()
	exists, err := t.HasSession(sessionName)
	if err != nil {
		// Same outcome as no session — nobody was told — but a failed probe and a
		// confirmed absence are different facts, and reporting one as the other is
		// the over-claim this whole path exists to prevent.
		return false, reasonProbeFailed
	}
	if !exists {
		return false, reasonNoSession
	}
	if !t.IsClaudeRunning(sessionName) {
		return false, reasonNotRunning
	}
	if err := t.SendNotificationBanner(sessionName, msg.From, msg.Subject); err != nil {
		return false, reasonBannerFailed
	}
	return true, reasonNotified
}
