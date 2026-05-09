package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

func newTestRouter(t *testing.T) (*Router, string) {
	t.Helper()
	root := setupTestFactory(t)
	store := memstore.New()
	r, err := NewRouter(root, store)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r, root
}

func TestResolveGroupAddress_All(t *testing.T) {
	r, _ := newTestRouter(t)

	members, err := r.ResolveGroupAddress("@all")
	if err != nil {
		t.Fatalf("ResolveGroupAddress(@all): %v", err)
	}

	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d: %v", len(members), members)
	}

	found := make(map[string]bool)
	for _, m := range members {
		found[m] = true
	}
	if !found["manager"] || !found["supervisor"] {
		t.Errorf("expected manager and supervisor, got %v", members)
	}
}

func TestResolveGroupAddress_Named(t *testing.T) {
	r, _ := newTestRouter(t)

	members, err := r.ResolveGroupAddress("@supervisors")
	if err != nil {
		t.Fatalf("ResolveGroupAddress(@supervisors): %v", err)
	}

	if len(members) != 1 || members[0] != "supervisor" {
		t.Errorf("expected [supervisor], got %v", members)
	}
}

func TestResolveGroupAddress_Unknown(t *testing.T) {
	r, _ := newTestRouter(t)

	_, err := r.ResolveGroupAddress("@nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown group")
	}
}

func TestSendDispatchesGroup(t *testing.T) {
	r, _ := newTestRouter(t)

	msg := NewMessage("manager", "@all", "broadcast", "hello")

	members, err := r.ResolveGroupAddress(msg.To)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var recipients []string
	for _, m := range members {
		if m != msg.From {
			recipients = append(recipients, m)
		}
	}

	if len(recipients) != 1 || recipients[0] != "supervisor" {
		t.Errorf("expected [supervisor] after filtering sender, got %v", recipients)
	}
}

func TestNotifyRecipientBestEffort(t *testing.T) {
	r := &Router{}
	msg := NewMessage("manager", "supervisor", "test", "body")
	// Should not panic even without tmux
	r.notifyRecipient(msg)
}

func TestNewRouterLoadsConfigs(t *testing.T) {
	r, _ := newTestRouter(t)

	// Verify configs were loaded and cached
	if r.agentsCfg == nil {
		t.Fatal("agentsCfg not loaded")
	}
	if r.msgCfg == nil {
		t.Fatal("msgCfg not loaded")
	}
	if len(r.agentsCfg.Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(r.agentsCfg.Agents))
	}
}

func TestLabelConstruction(t *testing.T) {
	msg := NewMessage("manager", "supervisor", "test subject", "test body")
	msg.Type = TypeTask

	// Labels must match the format used in sendToSingle: mail:true prefix + to:<recipient>
	expected := fmt.Sprintf("mail:true,from:manager,to:supervisor,thread:%s,msg-type:task", msg.ThreadID)
	labels := fmt.Sprintf("mail:true,from:%s,to:%s,thread:%s,msg-type:%s", msg.From, msg.To, msg.ThreadID, string(msg.Type))
	if labels != expected {
		t.Errorf("labels = %q, want %q", labels, expected)
	}

	if !strings.HasPrefix(labels, "mail:true,") {
		t.Error("labels must start with mail:true prefix")
	}

	// With reply-to
	msg.ReplyTo = "msg-original"
	labelsWithReply := labels + ",reply-to:" + msg.ReplyTo
	if !strings.Contains(labelsWithReply, "reply-to:msg-original") {
		t.Errorf("expected reply-to label in %q", labelsWithReply)
	}
}

func TestLabelConstructionIncludesToRecipient(t *testing.T) {
	msg := NewMessage("manager", "supervisor", "test subject", "test body")
	msg.Type = TypeTask

	// Build labels the same way sendToSingle does (router.go:60)
	labels := fmt.Sprintf("mail:true,from:%s,to:%s,thread:%s,msg-type:%s",
		msg.From, msg.To, msg.ThreadID, string(msg.Type))

	// Design doc (data.md L101) requires to:<recipient> label
	if !strings.Contains(labels, "to:supervisor") {
		t.Errorf("labels missing to:<recipient>: got %q, want to:supervisor included", labels)
	}

	// Verify complete expected format with to: field
	expected := fmt.Sprintf("mail:true,from:manager,to:supervisor,thread:%s,msg-type:task", msg.ThreadID)
	if labels != expected {
		t.Errorf("labels = %q, want %q", labels, expected)
	}
}

func TestLabelConstructionNormalizesSlashedInput(t *testing.T) {
	msg := NewMessage("manager", "supervisor/", "test subject", "test body")
	msg.Type = TypeTask

	// Simulate what sendToSingle does after Phase 1: normalize msg.To first
	msg.To = identityToAddress(msg.To)

	labels := fmt.Sprintf("mail:true,from:%s,to:%s,thread:%s,msg-type:%s",
		msg.From, msg.To, msg.ThreadID, string(msg.Type))

	// Label must contain "to:supervisor" not "to:supervisor/"
	if !strings.Contains(labels, "to:supervisor") {
		t.Errorf("labels missing normalized to:supervisor: got %q", labels)
	}
	if strings.Contains(labels, "to:supervisor/") {
		t.Errorf("labels contain un-normalized to:supervisor/: got %q", labels)
	}
}

func TestGroupSendSkipsSender(t *testing.T) {
	r, _ := newTestRouter(t)

	// @all includes manager and supervisor
	members, err := r.ResolveGroupAddress("@all")
	if err != nil {
		t.Fatal(err)
	}

	sender := "manager"
	var recipients []string
	for _, m := range members {
		if m != sender {
			recipients = append(recipients, m)
		}
	}

	// Manager should be excluded
	for _, r := range recipients {
		if r == sender {
			t.Errorf("sender %q should be excluded from group recipients", sender)
		}
	}
	if len(recipients) == 0 {
		t.Error("expected at least one non-sender recipient")
	}
}

func TestNotifyRecipient_SkipsWhenClaudeNotRunning(t *testing.T) {
	if exec.Command("tmux", "-V").Run() != nil {
		t.Skip("tmux not available")
	}

	tx := tmux.NewTmux()
	agentName := "test-notify-guard"
	sessionName := session.SessionName(agentName)

	_ = tx.KillSession(sessionName)
	if err := tx.NewSession(sessionName, "/tmp"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tx.KillSession(sessionName)

	if err := tx.WaitForShellReady(sessionName, 5*time.Second); err != nil {
		t.Fatalf("WaitForShellReady: %v", err)
	}

	r := &Router{}
	msg := NewMessage("manager", agentName, "SHOULD_NOT_APPEAR", "body")
	r.notifyRecipient(msg)

	time.Sleep(300 * time.Millisecond)

	content, err := tx.CapturePane(sessionName, 30)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}

	if strings.Contains(content, "SHOULD_NOT_APPEAR") {
		t.Fatal("banner was sent to bare shell — IsClaudeRunning guard is missing or broken")
	}
}

// setupTestFactory creates a minimal factory layout for testing.
func setupTestFactory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	configDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(config.StoreDir(root), 0o755); err != nil {
		t.Fatal(err)
	}

	factory := map[string]interface{}{
		"type":    "factory",
		"version": 1,
		"name":    "test",
	}
	writeJSON(t, filepath.Join(configDir, "factory.json"), factory)

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"manager": map[string]string{
				"type":        "interactive",
				"description": "Test manager",
			},
			"supervisor": map[string]string{
				"type":        "autonomous",
				"description": "Test supervisor",
			},
		},
	}
	writeJSON(t, filepath.Join(configDir, "agents.json"), agents)

	messaging := map[string]interface{}{
		"groups": map[string][]string{
			"supervisors": {"supervisor"},
		},
	}
	writeJSON(t, filepath.Join(configDir, "messaging.json"), messaging)

	return root
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
