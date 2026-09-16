package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// #668 D16's hygiene half: what happens to CLAUDE_CODE_EFFORT_LEVEL when an agent is relaunched
// under a profile that does not declare one.
//
// The arm needs this and cannot state it as "add the key to redirectFamilyVars", which is what the
// outline asked for and what a first pass did. That family is cleared UNCONDITIONALLY — every
// member absent from a launch's effective set is emitted as KEY='' (session.go:823-826) — and
// quickstart.sh:567 writes `export CLAUDE_CODE_EFFORT_LEVEL="${CLAUDE_CODE_EFFORT_LEVEL:-xhigh}"`
// into the operator's shell rc. Family membership would therefore have downshifted every agent in
// every factory that has never heard of this experiment, silently, from xhigh to the host default.
// The family can afford that rule because its other members are EndpointClassKeys, which
// CompleteEndpointProfile derives for any profile that leaves them empty; nothing derives an effort
// level, so the clear would have had nothing to put back.
//
// The profile-key universe (#602) is the mechanism that fits: it clears exactly the keys SOME
// profile in models.json declares, so the hygiene appears when an operator configures the arm and
// is absent when nobody has. The first two tests are those two halves.

// TestEffortLevelClearsOnProfileSwitch is the half the arm needs. Two arms of an experiment are
// only distinguishable if the "no reduction" arm actually runs without one: a session relaunched
// from a low-effort profile onto a profile that declares nothing must not keep running at low while
// the records say the mechanism was off.
func TestEffortLevelClearsOnProfileSwitch(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})
	// The universe as launchModelKeyUniverse builds it in a factory where one profile declares an
	// effort level and the profile being launched does not.
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL", config.EnvEffortLevel})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	wantUnset := "UnsetEnvironment " + mgr.SessionID() + " " + config.EnvEffortLevel
	if !hasOp(fake.ops, wantUnset) {
		t.Errorf("a switch to a profile declaring no effort level must clear the previous one at the "+
			"tmux twin; want %q, ops=%v", wantUnset, fake.ops)
	}
	inline := mgr.BuildStartupCommand()
	if !hasUnsetToken(inline, config.EnvEffortLevel) {
		t.Errorf("the inline twin must emit a true `unset %s` — it is the only clear a respawn ever "+
			"emits, and the boundary relaunch this arm rides is a respawn; got: %s",
			config.EnvEffortLevel, inline)
	}
}

// TestEffortLevelUntouchedWhenNoProfileDeclaresIt is the regression guard, and it is the one that
// caught the first pass. A factory that has never configured the experiment must launch exactly as
// it does today, inheriting whatever the operator's shell exports.
func TestEffortLevelUntouchedWhenNoProfileDeclaresIt(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL"})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	for _, op := range fake.ops {
		if strings.Contains(op, config.EnvEffortLevel) {
			t.Errorf("a factory where no profile declares an effort level touched %s at the tmux twin "+
				"(%q); quickstart.sh:567 exports xhigh into every operator shell, so touching it here "+
				"downshifts every agent in every existing factory", config.EnvEffortLevel, op)
		}
	}
	inline := mgr.BuildStartupCommand()
	if strings.Contains(inline, config.EnvEffortLevel) {
		t.Errorf("the launch line mentions %s in a factory that never configured it: %s",
			config.EnvEffortLevel, inline)
	}
}

// TestEffortLevelIsNotAFamilyMember states the placement decision as an interlock, because the
// three lists look interchangeable and are not.
func TestEffortLevelIsNotAFamilyMember(t *testing.T) {
	for _, key := range config.EndpointClassKeys {
		if key == config.EnvEffortLevel {
			t.Errorf("%s is an EndpointClassKeys member; class-key derivation would then invent an "+
				"effort level for every profile that declares none", config.EnvEffortLevel)
		}
	}
	for _, key := range redirectFamilyVars {
		if key == config.EnvEffortLevel {
			t.Errorf("%s is in redirectFamilyVars, whose members are cleared unconditionally; that "+
				"clears the value quickstart.sh:567 exports into every operator shell, on every "+
				"launch, in every factory. The profile-key universe is where per-profile keys clear.",
				config.EnvEffortLevel)
		}
	}
}
