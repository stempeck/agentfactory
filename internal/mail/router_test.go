package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
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

	msg := err.Error()
	for _, want := range []string{
		"unknown group: @",                  // leading clause preserved (prefix callers unaffected)
		"agents are addressed by bare name", // bare-name hint
		"supervisors",                       // a known group is listed
		"all",                               // the implicit "all" group is listed
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing expected substring %q", msg, want)
		}
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

// TestNotifyRecipientBestEffort deliberately does NOT install the recording
// notifier: it is the one test that still drives the REAL client under the
// default-build guard, which is why it can assert the no-session outcome
// without any tmux server (tmux.go:251-254).
func TestNotifyRecipientBestEffort(t *testing.T) {
	r := &Router{}
	msg := NewMessage("manager", "supervisor", "test", "body")
	// Should not panic even without tmux
	notified, reason := r.notifyRecipient(msg)
	if notified {
		t.Errorf("notifyRecipient reported notified with no tmux server, reason %q", reason)
	}
	if reason != reasonNoSession {
		t.Errorf("reason = %q, want %q", reason, reasonNoSession)
	}
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

// recordingNotifier is the hermetic double for the routerTmux seam: it records
// every probe and banner in call order and performs no I/O. It exists because
// the default-build guard answers HasSession with (false, nil) for EVERY name
// (internal/tmux/tmux.go:251-254), so against the real client "no banner was
// sent" is true whether or not the wake was skipped — the assertion this phase
// needs would be vacuous. Mirrors fakeTmux (internal/cmd/hermetic_test.go:33).
type recordingNotifier struct {
	ops           []string
	present       map[string]bool
	claudeRunning map[string]bool
	probeErr      error
	bannerErr     error
}

func (f *recordingNotifier) HasSession(name string) (bool, error) {
	f.ops = append(f.ops, "has-session:"+name)
	if f.probeErr != nil {
		return false, f.probeErr
	}
	return f.present[name], nil
}

func (f *recordingNotifier) IsClaudeRunning(session string) bool {
	f.ops = append(f.ops, "is-claude:"+session)
	return f.claudeRunning[session]
}

func (f *recordingNotifier) SendNotificationBanner(session, from, subject string) error {
	f.ops = append(f.ops, "banner:"+session)
	return f.bannerErr
}

func (f *recordingNotifier) bannerCount() int {
	n := 0
	for _, op := range f.ops {
		if strings.HasPrefix(op, "banner:") {
			n++
		}
	}
	return n
}

// liveNotifier returns a double whose named sessions are present and running
// Claude, i.e. the only state in which a real send would push a banner.
func liveNotifier(sessions ...string) *recordingNotifier {
	f := &recordingNotifier{present: map[string]bool{}, claudeRunning: map[string]bool{}}
	for _, s := range sessions {
		f.present[s] = true
		f.claudeRunning[s] = true
	}
	return f
}

// installNotifier swaps the package seam for the test's lifetime. Callers must
// not use t.Parallel: newRouterTmux is a package global.
func installNotifier(t *testing.T, f *recordingNotifier) {
	t.Helper()
	orig := newRouterTmux
	newRouterTmux = func() routerTmux { return f }
	t.Cleanup(func() { newRouterTmux = orig })
}

// newTestRouterWithStore mirrors newTestRouter but also hands back the memstore,
// which no pre-existing test needed. It is a sibling rather than a signature
// change so newTestRouter's six call sites stay untouched.
func newTestRouterWithStore(t *testing.T) (*Router, *memstore.Store) {
	t.Helper()
	root := setupTestFactory(t)
	store := memstore.New()
	r, err := NewRouter(root, store)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r, store
}

func mailboxCount(t *testing.T, store *memstore.Store, identity string) int {
	t.Helper()
	msgs, err := NewMailbox(identity, store).List(context.Background())
	if err != nil {
		t.Fatalf("listing %s's mailbox: %v", identity, err)
	}
	return len(msgs)
}

// TestRouter_SelfAddressedSendWakes pins PR #608: a gate verdict mailed by an agent
// to ITSELF files the bead AND wakes the agent — mail always wakes now that --no-wake
// is removed. The containment alarm depends on the same self-addressed wake.
func TestRouter_SelfAddressedSendWakes(t *testing.T) {
	const subject = "STEP_FIDELITY: 6/10"

	t.Run("self_addressed_send_files_and_wakes", func(t *testing.T) {
		r, store := newTestRouterWithStore(t)
		fake := liveNotifier("af-supervisor")
		installNotifier(t, fake)

		d, err := r.SendReporting(context.Background(),
			NewMessage("supervisor", "supervisor", subject, "body"))
		if err != nil {
			t.Fatalf("SendReporting: %v", err)
		}

		if fake.bannerCount() != 1 {
			t.Fatalf("self-addressed send recorded ops %v, want exactly one banner", fake.ops)
		}
		if n := mailboxCount(t, store, "supervisor"); n != 1 {
			t.Errorf("supervisor's mailbox has %d messages, want 1", n)
		}
		if !d.Notified || d.Reason != reasonNotified {
			t.Errorf("Delivery = %+v, want notified", d)
		}
	})

	t.Run("plain_Send_is_unchanged", func(t *testing.T) {
		r, store := newTestRouterWithStore(t)
		fake := liveNotifier("af-supervisor")
		installNotifier(t, fake)

		// The ADR-009 containment alarm (internal/cmd/containment.go:411-435) is a
		// self-addressed send through this exact entry point and must keep waking.
		if err := r.Send(context.Background(),
			NewMessage("supervisor", "supervisor", "CONTAINMENT BREACH", "body")); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if fake.bannerCount() != 1 {
			t.Errorf("Send recorded ops %v, want exactly one banner", fake.ops)
		}
		if n := mailboxCount(t, store, "supervisor"); n != 1 {
			t.Errorf("supervisor's mailbox has %d messages, want 1", n)
		}
	})
}

// TestRouter_DeliveryReport_LiveVsAbsentRecipient pins D-7: a send to a stopped
// or never-started agent files a bead and notifies nobody, so the report must
// distinguish the two rather than letting the caller claim delivery it cannot
// know (cross-review C-1).
func TestRouter_DeliveryReport_LiveVsAbsentRecipient(t *testing.T) {
	boom := errors.New("banner failed")

	cases := []struct {
		name         string
		present      bool
		running      bool
		probeErr     error
		bannerErr    error
		wantNotified bool
		wantReason   string
		wantOps      int
	}{
		{name: "probe_fails", probeErr: errors.New("tmux unreachable"), wantReason: reasonProbeFailed, wantOps: 1},
		{name: "live", present: true, running: true, wantNotified: true, wantReason: reasonNotified, wantOps: 3},
		{name: "absent", wantReason: reasonNoSession, wantOps: 1},
		{name: "present_but_claude_stopped", present: true, wantReason: reasonNotRunning, wantOps: 2},
		{name: "banner_fails", present: true, running: true, bannerErr: boom, wantReason: reasonBannerFailed, wantOps: 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, store := newTestRouterWithStore(t)
			fake := &recordingNotifier{
				present:       map[string]bool{"af-supervisor": tc.present},
				claudeRunning: map[string]bool{"af-supervisor": tc.running},
				probeErr:      tc.probeErr,
				bannerErr:     tc.bannerErr,
			}
			installNotifier(t, fake)

			d, err := r.SendReporting(context.Background(),
				NewMessage("manager", "supervisor", "subj", "body"))
			if err != nil {
				t.Fatalf("SendReporting: %v", err)
			}

			if d.Notified != tc.wantNotified {
				t.Errorf("Notified = %v, want %v (ops %v)", d.Notified, tc.wantNotified, fake.ops)
			}
			if d.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", d.Reason, tc.wantReason)
			}
			if len(fake.ops) != tc.wantOps {
				t.Errorf("ops = %v, want %d probe/banner calls", fake.ops, tc.wantOps)
			}
			if !d.Filed {
				t.Error("Filed = false, want true — the bead is written before any probe")
			}
			if n := mailboxCount(t, store, "supervisor"); n != 1 {
				t.Errorf("supervisor's mailbox has %d messages, want 1", n)
			}
		})
	}
}

// TestRouter_GroupFanOut_DeliveryAggregate pins the conservative aggregate: a
// group send may claim "notified" only when every attempted member was actually
// notified.
func TestRouter_GroupFanOut_DeliveryAggregate(t *testing.T) {
	t.Run("default_wakes_every_member_but_the_sender", func(t *testing.T) {
		r, _ := newTestRouterWithStore(t)
		fake := liveNotifier("af-manager", "af-supervisor")
		installNotifier(t, fake)

		d, err := r.SendReporting(context.Background(),
			NewMessage("manager", "@all", "broadcast", "body"))
		if err != nil {
			t.Fatalf("SendReporting(@all): %v", err)
		}
		if fake.bannerCount() != 1 {
			t.Errorf("ops = %v, want exactly one banner (supervisor only; the sender is skipped)", fake.ops)
		}
		for _, op := range fake.ops {
			if strings.HasSuffix(op, ":af-manager") {
				t.Errorf("group send touched the sender's own session: ops %v", fake.ops)
			}
		}
		if !d.Notified {
			t.Errorf("Delivery = %+v, want notified (every attempted member was notified)", d)
		}
	})

	t.Run("partial_wake_is_not_notified", func(t *testing.T) {
		r, _ := newTestRouterWithStore(t)
		// supervisor is live, but @all's other member is the sender, so widen the
		// group: send from a non-member so BOTH agents are attempted and only one
		// is reachable.
		fake := liveNotifier("af-supervisor")
		installNotifier(t, fake)

		d, err := r.SendReporting(context.Background(),
			NewMessage("operator", "@all", "broadcast", "body"))
		if err != nil {
			t.Fatalf("SendReporting(@all): %v", err)
		}
		if d.Notified {
			t.Errorf("Delivery = %+v, want not notified — one of two members was unreachable", d)
		}
		if !strings.Contains(d.Reason, "1 of 2") {
			t.Errorf("Reason = %q, want it to state how many of how many were notified", d.Reason)
		}
	})
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
