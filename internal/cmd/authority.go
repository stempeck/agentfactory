package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// teardownRefusalFormat is the ux.md Option U1 (L18-27) contract text every teardown
// gate emits when an agent context attempts a factory-wide teardown (AC-6). Like
// mismatchError it is a raw multi-line const consumed through fmt.Sprintf; %[1]s is the
// refused surface — "af down", "af down --reset", "af install --agents", or
// "af dispatch stop" — the ONLY token that varies. The body is identical across every
// surface so all gates assert one contract: the blast radius (YOU, every sibling agent,
// the interactive manager), the do-not-retry / skip-and-continue directive, and the
// operator-redirect. It deliberately never names the detection mechanism (AF_ROLE, the
// tmux session identity, the session env): ux.md L36-39 forbids handing the agent a
// bypass recipe.
const teardownRefusalFormat = `teardown refused: agent context (%[1]s)
This command stops the whole factory: it would kill YOU (this session), every
sibling agent, and the interactive manager. Factory-wide teardown is an operator
action.
Do NOT retry, do NOT look for another way to stop agents. Skip this step and
continue with your remaining work. If you believe a factory teardown is genuinely
required, tell your operator (af mail send manager -s "teardown request" -m "...")
and move on.`

// teardownRefusal renders the K1 refusal for the given command surface.
func teardownRefusal(surface string) string {
	return fmt.Sprintf(teardownRefusalFormat, surface)
}

// init wires the session-layer K9 interlock's ambient AF_ROLE reader at the cmd boundary.
// internal/session must not read named env directly (env_hermetic_test.go, issue #98), so
// the read lives here in the cmd layer and is injected into the session package.
func init() {
	session.SetAmbientCallerRole(func() string { return os.Getenv("AF_ROLE") })
}

// Authority is the caller class for a teardown-class operation. It is fail-closed:
// AuthorityOperator is returned ONLY when no agent-context signal is observed, so an
// ambiguous or unreadable state resolves toward AuthorityAgent (refusal). A false
// positive (operator misread as agent) would break the operator's regeneration shell
// fleet-wide (AC-3); a false negative (agent misread as operator) would re-open the
// agent-initiated factory-wide teardown the issue closes (AC-1).
type Authority int

const (
	AuthorityOperator Authority = iota // no agent-context signal observed
	AuthorityAgent                     // any agent-context signal observed (fail-closed)
)

// callerAuthority classifies the calling process as Operator or Agent using two
// independent ambient signals (security.md SEC-A2), returning Agent if EITHER fires:
//
//	Signal 1 — AF_ROLE is set. The session manager exports it for every launched agent
//	           (and its children inherit it), so this covers the ordinary case.
//	Signal 2 — the caller's OWN tmux session name is a production af identity. This
//	           survives `env -u AF_ROLE` because the name lives in the tmux server, not
//	           the process env — it is what catches the incident vector (an af down
//	           spawned inside an agent's pane after AF_ROLE was scrubbed).
//
// Signal 2 is gated on $TMUX being set: `display-message -p '#S'` only reflects OUR
// pane's session when we are actually inside one; consulting it from a bare host shell
// that merely has a running af server would echo an unrelated session's name and
// false-positive a real operator. The query is read through the newCmdTmux seam so a
// test can spoof an af-identity vs a non-af name (the real query returns "" under the
// guarded default build).
func callerAuthority() Authority {
	if os.Getenv("AF_ROLE") != "" {
		return AuthorityAgent
	}
	if os.Getenv("TMUX") != "" {
		if name, err := newCmdTmux().CurrentSessionName(); err == nil && isAfProductionSession(name) {
			return AuthorityAgent
		}
	}
	return AuthorityOperator
}

// isAfProductionSession reimplements tmux.isProductionIdentity (tmux.go:731-733), which is
// unexported and unreachable from this package (cmd imports tmux, so importing the symbol
// back would not help — it is not exported). session.Prefix ("af-") is the importable,
// drift-pinned half (TestKillScopeDrift asserts it matches the tmux.go literal); the
// "af-test-" hermetic-namespace exclusion is a raw literal, mirroring tmux.go.
func isAfProductionSession(name string) bool {
	return strings.HasPrefix(name, session.Prefix) && !strings.HasPrefix(name, "af-test-")
}

// requireOperatorTeardown is the single authority gate every teardown-class surface calls.
// It returns nil (zero output — AC-3's byte-for-byte operator path) for an Operator, and a
// non-nil error carrying the K1 refusal text for an Agent (cobra prints it and exits 1 —
// api.md A4 rejected any silent / false-success shape). On the Agent branch it also appends
// a best-effort forensic artifact (K4).
func requireOperatorTeardown(surface string) error {
	if callerAuthority() == AuthorityOperator {
		return nil
	}
	writeTeardownRefusedArtifact(surface)
	return errors.New(teardownRefusal(surface))
}

// isSelfSession reports whether target is the caller's own session — the agent-legitimate
// self-scope (an agent stopping its own session, e.g. af done). Identity is the trusted
// AF_ROLE name mapped through the single session-naming authority.
func isSelfSession(target string) bool {
	return target == session.SessionName(os.Getenv("AF_ROLE"))
}

// isSelfSessionID reports whether target matches the caller's own raw .runtime/session_id
// value — the session id the `af done` fallback path (done.go:616) hands to KillSession when
// detectAgentName fails. That value is the Claude Code hook's session id, NOT
// session.SessionName(AF_ROLE), so isSelfSession alone would wrongly refuse a legitimate
// self-terminate (Discrepancy Ledger D9). It is read from the caller's cwd — the same path
// done.go:616 reads (runDone derives its cwd from getWd()). Kept SEPARATE from isSelfSession so
// K11's downSelfScoped equality stays a pure name compare; only the K8 guard consults this
// superset. Best-effort: any read error or an empty file yields false (fail-closed toward
// refusal).
func isSelfSessionID(target string) bool {
	wd, err := getWd()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(wd, ".runtime", "session_id"))
	if err != nil {
		return false
	}
	id := strings.TrimSpace(string(raw))
	return id != "" && target == id
}

// isSpecialistTarget reports whether entry is a formula-backed specialist — the same axis
// resolveSpecialistAgent enforces (sling.go:276-278). This predicate itself keys only on
// Formula; AgentEntry.Type (interactive/autonomous) IS consulted separately by
// scopedStopAllowed's manager tier — a deliberate, ADR-021-recorded #548 extension of the
// original self+dispatcher model, not a silent reversal.
func isSpecialistTarget(entry config.AgentEntry) bool {
	return entry.Formula != ""
}

// scopedStopAllowed reports whether the agent-context caller may stop the single named
// target, and which tier grants it. Three ordered tiers, fail-closed (any resolution error
// or no match ⇒ "", false):
//
//	self       — target is the caller's own session (isSelfSession, unchanged)
//	dispatcher — target is a formula-backed specialist the caller dispatched
//	             (isSpecialistTarget && callerDispatched; predicate unchanged, its provenance
//	             substrate repaired by the #548 dispatch_owner split)
//	manager    — the caller resolves (resolveAgentName + the agents map) to an
//	             interactive-type entry, AND the target entry exists AND
//	             isSpecialistTarget(target) AND the target is autonomous
//
// The manager tier is keyed on the interactive TYPE, never a "manager" name literal, and its
// autonomous-specialist target predicate excludes the manager itself, any interactive agent,
// a non-formula supervisor, and the watchdog/dispatcher (not agents.json entries). Returning
// the granting tier — not a bare bool — lets Phase 2's granted-audit record which authority
// stopped the target. isSelfSession wants a session name (af-<agent>); the entry lookups and
// callerDispatched want the bare agent name.
func scopedStopAllowed(target string, agents map[string]config.AgentEntry) (tier string, ok bool) {
	if isSelfSession(session.SessionName(target)) {
		return "self", true
	}
	entry, exists := agents[target]
	if exists && isSpecialistTarget(entry) && callerDispatched(target) {
		return "dispatcher", true
	}
	if exists && isSpecialistTarget(entry) && entry.Type == "autonomous" && callerIsInteractive(agents) {
		return "manager", true
	}
	return "", false
}

// callerIsInteractive reports whether the agent-context caller resolves to an
// interactive-type entry in the agents map — the manager tier's caller gate. Caller
// identification reuses resolveAgentName (path-derived, agents.json-validated, AF_ROLE
// fallback). Fail-closed: any resolution error, an unknown caller, or a non-interactive type
// ⇒ false.
func callerIsInteractive(agents map[string]config.AgentEntry) bool {
	wd, err := getWd()
	if err != nil {
		return false
	}
	root, err := config.FindLocalRoot(wd)
	if err != nil {
		return false
	}
	caller, err := resolveAgentName(wd, root)
	if err != nil || caller == "" {
		return false
	}
	entry, ok := agents[caller]
	return ok && entry.Type == "interactive"
}

// callerDispatched reports whether the caller dispatched target — i.e. the dispatcher identity
// recorded for target equals the caller's own identity. This binds a sibling-stop right to
// dispatch ownership (H-1). A missing/unreadable datum or an unresolvable caller ⇒ false
// (fail-closed toward refusal).
//
// The read is topology-robust (#548 P4). It reads target's provenance — primary
// .runtime/dispatch_owner (session-scoped, survives formula completion), compat
// .runtime/formula_caller for a pre-#548 dispatch — from target's resolved agent dir, which
// resolveAgentDir locates via worktree.FindByAgent from the local root. When the local root
// cannot see target (a worktree caller whose target is registered under the enclosing
// factory), it retries under FindEnclosingRoot. This is a fallback read LOCATION only, not a
// resolver change (T-INT-4): FindLocalRoot stays primary.
func callerDispatched(target string) bool {
	wd, err := getWd()
	if err != nil {
		return false
	}
	localRoot, err := config.FindLocalRoot(wd)
	if err != nil {
		return false
	}
	caller, err := resolveAgentName(wd, localRoot)
	if err != nil || caller == "" {
		return false
	}
	if owner := readDispatchProvenance(resolveAgentDir(localRoot, target)); owner != "" {
		return owner == caller
	}
	if enclosing, err := config.FindEnclosingRoot(localRoot); err == nil && enclosing != "" {
		if owner := readDispatchProvenance(resolveAgentDir(enclosing, target)); owner != "" {
			return owner == caller
		}
	}
	return false
}

// readDispatchProvenance returns the dispatcher identity recorded for an agent dir: the
// primary .runtime/dispatch_owner (session-scoped — #548 P3), falling back to the legacy
// .runtime/formula_caller for a dispatch minted by a pre-#548 binary (the compat pin
// TestCallerDispatched_Provenance depends on this fallback). Empty ⇒ no provenance found;
// the caller treats that as fail-closed.
func readDispatchProvenance(workDir string) string {
	if owner := readDispatchOwner(workDir); owner != "" {
		return owner
	}
	return readFormulaCaller(workDir)
}

// writeTeardownRefusedArtifact appends one forensic line to the caller's own
// <agent-dir>/.runtime/teardown_refused (data.md DA3a). It is BEST-EFFORT — every error is
// swallowed, because a refusal must never fail just because the breadcrumb could not be
// written, and af never reads this file. It intentionally uses O_APPEND (not the truncating
// done.go:639-640 idiom) so repeated refusals accumulate, and resolves the agent dir via the
// worktree-aware resolveAgentDir. (The agent-dir .runtime is a REAL directory even inside a
// worktree — worktree.go:598-602; only the worktree-ROOT .runtime is a symlink — so the
// resolution matters for locating the right agent dir, not for symlink-following.)
func writeTeardownRefusedArtifact(surface string) {
	wd, err := getWd()
	if err != nil {
		return
	}
	root, err := config.FindLocalRoot(wd)
	if err != nil {
		return
	}
	name, err := resolveAgentName(wd, root)
	if err != nil || name == "" {
		return
	}
	runtimeDir := filepath.Join(resolveAgentDir(root, name), ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(runtimeDir, "teardown_refused"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s surface=%s argv=%q signals=%s\n",
		time.Now().UTC().Format(time.RFC3339), surface, strings.Join(os.Args[1:], " "), observedTeardownSignals())
}

// writeTeardownGrantedArtifact is the allow-path mirror of writeTeardownRefusedArtifact (#548 P2 /
// C-5): when an agent-context scoped stop is GRANTED, it appends one forensic line to the caller's
// own <agent-dir>/.runtime/teardown_granted recording which tier (self/dispatcher/manager) granted
// the stop and the target. It shares the refusal writer's caller-dir resolution
// (getWd → FindLocalRoot → resolveAgentName → resolveAgentDir), its best-effort O_APPEND idiom
// (every error swallowed; af never reads the file), the same RFC3339-UTC stamp, and the same argv
// TAIL (os.Args[1:], not the full argv). It lives at the af down gate allow-path (down.go), NOT
// inside scopedStopAllowed — P5 also calls scopedStopAllowed per-target, so a write there would
// misfire on a partially-allowed multi-target command (refused as a whole).
func writeTeardownGrantedArtifact(tier, target string) {
	wd, err := getWd()
	if err != nil {
		return
	}
	root, err := config.FindLocalRoot(wd)
	if err != nil {
		return
	}
	name, err := resolveAgentName(wd, root)
	if err != nil || name == "" {
		return
	}
	runtimeDir := filepath.Join(resolveAgentDir(root, name), ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(runtimeDir, "teardown_granted"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s tier=%s target=%s argv=%q signals=%s\n",
		time.Now().UTC().Format(time.RFC3339), tier, target, strings.Join(os.Args[1:], " "), observedTeardownSignals())
}

// observedTeardownSignals renders which agent-context signals fired, for the K4 breadcrumb
// (e.g. "AF_ROLE=soldesign-engineer"). It mirrors callerAuthority's reads so the forensic
// record reflects the same decision inputs.
func observedTeardownSignals() string {
	var parts []string
	if role := os.Getenv("AF_ROLE"); role != "" {
		parts = append(parts, "AF_ROLE="+role)
	}
	if os.Getenv("TMUX") != "" {
		if name, err := newCmdTmux().CurrentSessionName(); err == nil && isAfProductionSession(name) {
			parts = append(parts, "tmux="+name)
		}
	}
	return strings.Join(parts, ",")
}
