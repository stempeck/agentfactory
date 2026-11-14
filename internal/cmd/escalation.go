package cmd

import (
	"context"
	"os"
)

// escalationTarget is the single source of truth for the agent the operator
// escalations go to: recovery's escalations, the window mail (K5) and the startup
// mail (S-D) reach it through escalateOperator, and the watchdog and
// compaction-rate alerts still through sendHandoffMail. It is intentionally a
// constant rather than configuration: the af-up omission warning reads
// escalationTargets(), so a new escalation sink cannot be added without that
// warning extending to cover it (#303).
const escalationTarget = "supervisor"

const escalationManager = "manager"

// escalationTargets returns the agent(s) that are reachable as escalation sinks
// independently of any messaging.json group or dispatch notify target. The af-up
// startup omission check unions this with the messaging-group members and the
// dispatch NotifyOnComplete target.
func escalationTargets() []string { return []string{escalationTarget, escalationManager} }

const escalationClassRecovery = "RECOVERY HALTED"

// escalationMailFn files in-process as af on the root the caller already validated.
// Re-resolving the root from the cwd would re-raise a factory-root mismatch that
// `af statusline status` has already downgraded, and a subprocess send from the
// watchdog session detects no sender and files nothing.
var escalationMailFn = func(root, to, subject, body string) error {
	if isTestBinary() {
		return nil
	}
	store, err := newIssueStoreAt(root, os.Getenv("AF_ACTOR"))
	if err != nil {
		return err
	}
	return fileDispatcherMail(context.Background(), store, root, to, subject, body)
}
