package memory

import (
	"fmt"
	"path/filepath"

	"github.com/stempeck/agentfactory/internal/config"
)

// PreservedLine describes the vault a teardown is leaving standing, or returns "" when there is
// nothing to say.
//
// The vault survives every teardown path by construction — it is a different subtree from
// worktrees/ and agents/, so none of them can reach it. What that construction does NOT give the
// operator is any way to KNOW it survived: a teardown scrolls past and the durable state it
// spared is indistinguishable from state it destroyed. This line is the difference, and it lives
// here rather than at the four call sites because those sites span two packages (done/down/reset
// in internal/cmd, GC in internal/worktree) and a copy per site is a drift a later test would
// have to police.
//
// Two rules are encoded, and both are deliberate:
//
//   - An empty vault says nothing. At zero notes nothing was preserved and nothing was
//     destroyed, so a factory that never recorded a learning shows no new output at all.
//   - A count that cannot be taken says nothing either, and never reports the failure upward.
//     Callers invoke this while dismantling a session; a vault that could not be counted is
//     still a vault that survived, and no describing failure may cost the teardown itself.
//
// The path is rendered relative to the factory root because that is the form every other
// teardown line uses, and it is composed through config.AgentMemoryDir rather than spelled here
// — the same one-constructor rule that AgentMemoryDir itself exists to enforce.
func PreservedLine(factoryRoot, agent string) string {
	notes, err := List(factoryRoot, agent, Filter{})
	if err != nil || len(notes) == 0 {
		return ""
	}
	dir := config.AgentMemoryDir(factoryRoot, agent)
	if rel, relErr := filepath.Rel(factoryRoot, dir); relErr == nil {
		dir = rel
	}
	return fmt.Sprintf("memory preserved: %s%c (%d notes)", dir, filepath.Separator, len(notes))
}
