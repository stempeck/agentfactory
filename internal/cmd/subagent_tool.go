package cmd

// isSubagentTool reports whether a PreToolUse/PostToolUse hook payload's tool_name is the platform's
// sub-agent launcher. Claude Code names it "Agent" (observed in a live 2.1.224 session transcript's
// tool_use JSON; the r3904822316 comment cited 2.1.236 — see .designs/672/decision-gating-verification.md);
// older builds named it "Task". The dispatch-admit gate and the subagent-observe hook
// must each fire on either, so both read this one predicate — the defect that motivated it (#669
// BROKEN-0) was two sites drifting onto the same wrong literal. The hook matchers in the settings
// templates carry the matching regexp "Task|Agent".
func isSubagentTool(name string) bool {
	return name == "Task" || name == "Agent"
}
