package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/session"
)

// TestSlingStartsWatchdog pins W-2: a factory driven by sling alone still gets the watchdog that
// records windows and recovers occupancy, started after the launch and outside the launch seam so
// a stubbed launch cannot hide it. The fake never marks a created session present, so each
// NewSession op is one creation.
func TestSlingStartsWatchdog(t *testing.T) {
	const agent = "specialist-agent"

	sling := func(t *testing.T, prepare func(fake *fakeTmux)) (*fakeTmux, string) {
		t.Helper()
		installNoopLaunchSession(t)
		root, _ := createTestFormulaFactory(t, "test-specialist-formula", agent)
		fake, _ := setupHermeticSessions(t)
		if prepare != nil {
			prepare(fake)
		}
		data, err := json.Marshal(map[string]any{"agents": map[string]any{
			agent: map[string]any{"type": "autonomous", "description": "Test specialist", "formula": "test-specialist-formula"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
		if err := os.MkdirAll(callerWd, 0o755); err != nil {
			t.Fatal(err)
		}

		origEnsure := ensureSlingWatchdog
		ensureSlingWatchdog = startSlingWatchdog
		t.Cleanup(func() { ensureSlingWatchdog = origEnsure })

		cmd := &cobra.Command{}
		cmd.SetContext(t.Context())
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		if err := dispatchToSpecialist(cmd, root, callerWd, agent, "implement issue #42"); err != nil {
			t.Fatalf("af sling --agent %s: %v\n%s", agent, err, buf.String())
		}
		return fake, buf.String()
	}
	watchdogOps := func(fake *fakeTmux, verb string) []string {
		var got []string
		for _, op := range fake.ops {
			if op == verb+" "+session.WatchdogSessionName() || strings.HasPrefix(op, verb+" "+session.WatchdogSessionName()+" ") {
				got = append(got, op)
			}
		}
		return got
	}

	t.Run("sling --agent creates the watchdog", func(t *testing.T) {
		fake, out := sling(t, nil)
		if got := watchdogOps(fake, "NewSession"); len(got) != 1 {
			t.Errorf("NewSession %s ops = %v, want exactly one; ops=%v\noutput:\n%s", session.WatchdogSessionName(), got, fake.ops, out)
		}
	})

	t.Run("--no-launch starts no watchdog", func(t *testing.T) {
		orig := slingNoLaunch
		slingNoLaunch = true
		t.Cleanup(func() { slingNoLaunch = orig })
		fake, out := sling(t, nil)
		if got := watchdogOps(fake, "NewSession"); len(got) != 0 {
			t.Errorf("--no-launch created the watchdog: %v\noutput:\n%s", got, out)
		}
	})

	t.Run("a healthy watchdog is left alone and nothing is printed", func(t *testing.T) {
		fake, out := sling(t, func(fake *fakeTmux) {
			fake.present[session.WatchdogSessionName()] = true
			fake.running[session.WatchdogSessionName()] = true
		})
		if got := watchdogOps(fake, "NewSession"); len(got) != 0 {
			t.Errorf("a healthy watchdog was recreated: %v", got)
		}
		if got := watchdogOps(fake, "KillSession"); len(got) != 0 {
			t.Errorf("a healthy watchdog was killed: %v", got)
		}
		if strings.Contains(out, "pane monitoring inert") {
			t.Errorf("a healthy watchdog must not print the inert-scope line on every sling:\n%s", out)
		}
	})

	t.Run("an agent never replaces a dead watchdog", func(t *testing.T) {
		t.Setenv("AF_ROLE", "manager")
		fake, out := sling(t, func(fake *fakeTmux) {
			fake.present[session.WatchdogSessionName()] = true
		})
		if got := watchdogOps(fake, "KillSession"); len(got) != 0 {
			t.Errorf("an agent-context sling killed the watchdog session: %v", got)
		}
		if got := watchdogOps(fake, "NewSession"); len(got) != 0 {
			t.Errorf("an agent-context sling recreated the watchdog session: %v", got)
		}
		const want = "warning: the watchdog session is present but not running; an agent cannot replace it — run 'af up' from an operator shell"
		if n := strings.Count(out, "an agent cannot replace it"); n != 1 || !strings.Contains(out, want) {
			t.Errorf("want exactly one line %q, got %d mentions in:\n%s", want, n, out)
		}
		if strings.Contains(out, "teardown refused") {
			t.Errorf("an agent must not be shown the teardown refusal:\n%s", out)
		}
	})
}
