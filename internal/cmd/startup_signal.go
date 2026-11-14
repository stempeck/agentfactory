package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

const startupSignalSubject = "STARTUP: %s %s (harness %s)"

var pollStartupSignalFn = pollStartupSignal

var startupMeasuredAgents = []string{"manager", "supervisor"}

func startupSignalReasons(t startupTurn) []string {
	var reasons []string
	if t.Budget > 0 && t.Tokens > t.Budget {
		reasons = append(reasons, fmt.Sprintf("%d > %d tokens", t.Tokens, t.Budget))
	}
	switch t.RulingsBlock {
	case rulingsBlockAbsent, rulingsBlockPartial:
		reasons = append(reasons, "rulings block "+t.RulingsBlock)
	}
	return reasons
}

func startupSignalRecipient(agent string) string {
	if agent == escalationTarget {
		return escalationManager
	}
	return escalationTarget
}

func startupSignalDir(root string) string { return filepath.Join(root, ".runtime", "startup_signal") }

func startupSignalLatchPath(root, agent, harness string) string {
	return filepath.Join(startupSignalDir(root), agent+"@"+startupHarnessToken(harness)+".sent")
}

// removeStartupSignalLatches runs wherever the measurement is removed: a reset ends the alarm
// episode, and a latch kept past it would leave the next over-budget startup on the same harness
// raising STARTUP with nobody mailed.
func removeStartupSignalLatches(root, agent string) error {
	dir := startupSignalDir(root)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), agent+"@") || !strings.HasSuffix(e.Name(), ".sent") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func startupAlarms(root string) []recoveryAlarm {
	var raised []recoveryAlarm
	for _, name := range startupMeasuredAgents {
		t, ok := readStartupTurn(root, name)
		if !ok {
			continue
		}
		if reasons := startupSignalReasons(t); len(reasons) > 0 {
			raised = append(raised, recoveryAlarm{
				class:  alarmClassStartup,
				agent:  name,
				detail: "startup turn " + strings.Join(reasons, ", ") + " (harness " + t.HarnessVersion + ").",
			})
		}
	}
	return raised
}

// pollStartupSignal keeps the latch whatever escalateOperator returns: every outcome is a send or
// the shared breadcrumb, so a retry could only send the same mail twice.
func pollStartupSignal(root string, now time.Time) {
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return
	}
	for _, a := range startupMeasuredAgents {
		if _, ok := agentsCfg.Agents[a]; !ok {
			continue
		}
		if measureStartupTurn(root, a) {
			fmt.Fprintf(os.Stderr, startupScanCapWarning, a, startupTranscriptScanCap)
		}
		t, ok := readStartupTurn(root, a)
		if !ok {
			continue
		}
		reasons := startupSignalReasons(t)
		if len(reasons) == 0 {
			continue
		}
		latch := startupSignalLatchPath(root, a, t.HarnessVersion)
		if os.MkdirAll(filepath.Dir(latch), 0o755) != nil {
			continue
		}
		f, err := os.OpenFile(latch, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			// Not a claim either: mailing without the latch would mail on every tick. The alarm
			// stands meanwhile and the next tick tries again.
			continue
		}
		f.Close()
		subject := fmt.Sprintf(startupSignalSubject, a, strings.Join(reasons, ", "), t.HarnessVersion)
		body := fmt.Sprintf("agent: %s\nsession: %s\nharness: %s\ntokens: %d\nbudget: %d\nrulings_block: %s\n\n"+
			"Details: af telemetry startup --agent %s\nThis mail is sent once per agent and harness version.\n",
			a, t.SessionID, t.HarnessVersion, t.Tokens, t.Budget, t.RulingsBlock, a)
		escalateOperator(root, startupSignalRecipient(a), alarmClassStartup, subject, body)
	}
}
