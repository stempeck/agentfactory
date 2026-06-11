package cmd

// escalationTarget is the single source of truth for the agent the watchdog and
// compaction-rate alerts escalate to via sendHandoffMail. It is intentionally a
// constant rather than configuration: the af-up omission warning reads
// escalationTargets(), so a new escalation sink cannot be added without that
// warning extending to cover it (#303).
const escalationTarget = "supervisor"

// escalationTargets returns the agent(s) that are reachable as escalation sinks
// independently of any messaging.json group or dispatch notify target. The af-up
// startup omission check unions this with the messaging-group members and the
// dispatch NotifyOnComplete target.
func escalationTargets() []string { return []string{escalationTarget} }
