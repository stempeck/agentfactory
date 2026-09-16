package cmd

import (
	"regexp"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// This file answers ONE question for the record log: what was this run made of (#678 K1)? A step's
// token figures say what a run cost; without the binary, the checkout and the inputs beside them,
// two runs that cost differently are indistinguishable from two runs that measured the same thing.
//
// Every derivation here is READ-ONLY git (ADR-017 / SEC-2) and every one degrades to "" rather than
// to a plausible-looking substitute. An omitempty empty string reads as "nobody recorded this",
// which is the only honest answer when the tree cannot say; a fabricated commit would read as
// evidence.

// commitPattern is a full git object id and nothing shorter. Abbreviated ids are rejected because
// their length is a property of the repository they were abbreviated in, so the same run recorded on
// two clones would carry two different strings for one commit.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// checkoutCommit is the commit the working tree was on when the run was instantiated.
//
// --verify and the 40-hex check are both load-bearing, and neither is redundant. Without --verify,
// `git rev-parse HEAD` in a repository with no commits prints the literal string "HEAD" to stdout
// and fails only via its exit code; runGitDetect drops the error and returns stdout, so the pair
// below is what stops "HEAD" being recorded as this run's checkout.
func checkoutCommit(workDir string) string {
	sha := runGitDetect(workDir, "git", "rev-parse", "--verify", "HEAD")
	if !commitPattern.MatchString(sha) {
		return ""
	}
	return sha
}

// baseCommit is the commit the run's branch diverged from — the point a diff of the run's work
// should be taken against, resolved at CLOSE because that is the first moment the branch has
// stopped moving.
//
// The default branch is resolved by the LOCAL rung only, deliberately NOT detectDefaultBranch: that
// chain falls through to `git ls-remote` and `gh`, each bounded at 5s, and this runs on `af done` —
// a hot verb on every step close. A base commit is worth having, not worth stalling a step boundary
// for ten seconds to guess at, and a factory whose origin/HEAD is unset already gets a loud warning
// from sling's own detection.
func baseCommit(workDir string) string {
	ref := runGitDetect(workDir, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if !isValidBranchName(ref) {
		return ""
	}
	sha := runGitDetect(workDir, "git", "merge-base", "HEAD", ref)
	if !commitPattern.MatchString(sha) {
		return ""
	}
	return sha
}

// tokenomicsState reports the umbrella as the run saw it, in the record's closed on/off vocabulary.
//
// The conjunction is resolvedPolicy's (tokenomics_admission.go): two switches in two places, and a
// site that read only the toggle file would report a posture an operator had switched off in
// startup.json. This deliberately RE-DERIVES that conjunction rather than calling resolvedPolicy,
// because the record field is an on/off string and resolvedPolicy answers in a Policy — the two
// vocabularies do not merge. An unreadable startup.json is "off" here rather than an omission, because
// a factory whose startup config does not load is a factory that will not launch the mechanisms —
// which is what "off" means to every reader of this field.
func tokenomicsState(factoryRoot string) string {
	if !tokenomicsFactoryEnabled(factoryRoot) {
		return telemetry.TokenomicsStateOff
	}
	startup, err := config.LoadStartupConfig(factoryRoot)
	if err != nil || startup.Tokenomics.Enabled == "off" {
		return telemetry.TokenomicsStateOff
	}
	return telemetry.TokenomicsStateOn
}
