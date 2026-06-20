package tmuxisolation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// # CI-only guard for the integration suite (issue #389)
//
// The integration suite (//go:build integration) drives REAL tmux sessions, git
// worktrees, and branches (that is its purpose — see the package doc and
// internal/cmd/main_integration_test.go's "Do not call Setup" note). Launched from
// inside a LIVE agent factory, those real resources collide with running agents and
// recursively peg CPU / exhaust memory until the container is hard-stopped (incident:
// an agent ran `make test-integration` from its own worktree and crashed the host).
//
// GuardCIOnly makes the integration suite FAIL FAST — before any Test* body runs, so
// zero real resources are created — when it detects a live factory, printing the
// CEO-specified line and exiting non-zero. It is the inverse polarity of the existing
// build-split guards (internal/tmux/guard_default.go, internal/cmd/storeguard_default.go),
// which DISARM under -tags=integration to let the integration suite reach real
// resources: this guard is instead ARMED only in the integration build, by virtue of
// being called from the //go:build integration TestMain wiring. There is no companion
// !integration file; the build tag on the callers is what scopes it.
//
// ## Detection signal — what is SAFE and what is NOT
//
// A live factory is detected by the working directory being under a factory worktree
// (".agentfactory/worktrees/"). The worktrees directory is git-ignored (.gitignore), so
// it is absent from a clean CI checkout or a human clone, and a test binary inside a
// factory always runs with its CWD under it: an agent session cannot start without a
// worktree (internal/session/session.go returns ErrWorktreeNotSet otherwise) and its CWD
// is always the agent dir inside that worktree. This signal touches no env, so it is
// immune to NeutralizeAFEnv and process-startup ordering can never silently defeat it.
//
// History (#390): an earlier SECONDARY signal — "any live-session AF_* env var set" —
// was removed. It false-blocked TestEnvFamilyDifferentialProbe (#327), which re-execs the
// integration binary with the whole AF_* family set to junk to verify NeutralizeAFEnv;
// that child tripped the guard before NeutralizeAFEnv could run. Because every real agent
// invocation is worktree-backed, the CWD signal already covers it, so the env signal added
// no coverage and only produced that false positive.
//
// Deliberately NOT used as a signal: config.FindFactoryRoot success, or the existence
// of .agentfactory/ or .agentfactory/factory.json. factory.json is COMMITTED/tracked
// (.gitignore force-includes it), so it is present in a clean CI checkout too — keying
// on it would wrongly block legitimate CI integration runs. The detection here stays
// stdlib-only (CWD substring), preserving this package's stdlib-only invariant.

// blockedMessage is the single source of truth for the CEO-specified wording (the
// apostrophe in "it's" is load-bearing). checkCIOnly, errBlocked, GuardCIOnly, and the
// unit test all reference THIS constant — never a re-typed literal — so it cannot drift.
const blockedMessage = "agents are not allowed to run this, it's CI only"

// errBlocked carries blockedMessage. checkCIOnly returns it (or nil) so the isolated
// unit test asserts on an error value rather than on stdout/exit code.
var errBlocked = errors.New(blockedMessage)

// checkCIOnly is the PURE decision core: it reads the working directory only through its
// argument, so the unit test drives it with synthetic inputs and never touches real
// process state. It returns errBlocked when the live-factory signal (a CWD under a factory
// worktree) is present, nil otherwise (the CI / clean-human-checkout case).
//
//	cwd — the working directory (the real caller passes os.Getwd()).
func checkCIOnly(cwd string) error {
	// A CWD under a factory worktree marks a live factory. Touches no env, so it
	// survives NeutralizeAFEnv and is immune to startup ordering.
	if strings.Contains(filepath.ToSlash(cwd), "/.agentfactory/worktrees/") {
		return errBlocked
	}
	return nil
}

// GuardCIOnly fails the process fast if it is running inside a live agent factory.
// It is called as the FIRST statement of each integration package's TestMain — before
// m.Run(), so no Test* body (hence no real tmux/worktree/git resource) can run. On
// detection it writes blockedMessage to stderr and exits non-zero; otherwise it returns
// and the suite proceeds unchanged (the CI path). It is a no-op outside a factory, so it
// never affects CI or a human's clean-checkout run.
func GuardCIOnly() {
	cwd, _ := os.Getwd()
	if err := checkCIOnly(cwd); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
