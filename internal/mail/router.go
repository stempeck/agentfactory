package mail

import (
	"context"
	"fmt"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

// Router dispatches messages to agents with group fan-out support.
type Router struct {
	workDir     string
	factoryRoot string
	store       issuestore.Store
	agentsCfg   *config.AgentConfig
	msgCfg      *config.MessagingConfig
}

// NewRouter creates a Router, discovering the factory root from workDir and
// using the injected Store for issue persistence.
func NewRouter(workDir string, store issuestore.Store) (*Router, error) {
	root, err := config.FindFactoryRoot(workDir)
	if err != nil {
		return nil, fmt.Errorf("creating router: %w", err)
	}

	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return nil, fmt.Errorf("creating router: %w", err)
	}

	msgPath := config.MessagingConfigPath(root)
	msgCfg, err := config.LoadMessagingConfig(msgPath, agentsCfg)
	if err != nil {
		return nil, fmt.Errorf("creating router: %w", err)
	}

	return &Router{
		workDir:     workDir,
		factoryRoot: root,
		store:       store,
		agentsCfg:   agentsCfg,
		msgCfg:      msgCfg,
	}, nil
}

// Send dispatches a message. If To starts with "@", resolves the group and fans out.
func (r *Router) Send(ctx context.Context, msg *Message) error {
	if strings.HasPrefix(msg.To, "@") {
		return r.sendToGroup(ctx, msg)
	}
	return r.sendToSingle(ctx, msg)
}

func (r *Router) sendToSingle(ctx context.Context, msg *Message) error {
	msg.To = identityToAddress(msg.To)
	params := messageToCreateParams(msg.From, msg)
	if _, err := r.store.Create(ctx, params); err != nil {
		return fmt.Errorf("sending to %s: %w", msg.To, err)
	}

	// Self-mail guard commented out: a Stop Hook uses "af mail send" to self
	// for efficiency, so self-sends must still trigger tmux notification.
	// This may cause recursive notification loops — the AI/LLM is responsible
	// for preventing recursion.
	// if msg.From != msg.To {
	r.notifyRecipient(msg)
	// }
	return nil
}

func (r *Router) sendToGroup(ctx context.Context, msg *Message) error {
	members, err := r.ResolveGroupAddress(msg.To)
	if err != nil {
		return err
	}

	var errs []string
	for _, member := range members {
		if member == msg.From {
			continue // skip sender
		}
		individual := *msg
		individual.To = member
		if err := r.sendToSingle(ctx, &individual); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", member, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("group send partial failure: %s", strings.Join(errs, "; "))
	}
	return nil
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
		return nil, fmt.Errorf("unknown group: @%s", groupName)
	}
	return members, nil
}

func (r *Router) notifyRecipient(msg *Message) {
	// Best-effort: send notification banner into recipient's tmux session.
	// Uses session.SessionName() for proper "af-<agent>" naming and
	// HasSession guard to skip if no session exists.
	sessionName := session.SessionName(msg.To)
	t := tmux.NewTmux()
	exists, err := t.HasSession(sessionName)
	if err != nil || !exists {
		return
	}
	if !t.IsClaudeRunning(sessionName) {
		return
	}
	_ = t.SendNotificationBanner(sessionName, msg.From, msg.Subject)
}
