package cmd

import (
	"strings"
	"testing"
)

type gitCall struct {
	name string
	args []string
}

// fakeGit records every command runGitDetect was asked to run, as well as answering them. The
// recording is the point for baseCommit: "never waits on the network" is a claim about what was NOT
// run, and no return value can carry it.
type fakeGit struct {
	calls []gitCall
}

func (f *fakeGit) ran(word string) bool {
	for _, c := range f.calls {
		if c.name == word {
			return true
		}
		for _, a := range c.args {
			if a == word {
				return true
			}
		}
	}
	return false
}

func (f *fakeGit) transcript() string {
	lines := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		lines = append(lines, c.name+" "+strings.Join(c.args, " "))
	}
	return strings.Join(lines, "; ")
}

func installFakeGit(t *testing.T, reply func(name string, args []string) string) *fakeGit {
	t.Helper()
	fake := &fakeGit{}
	orig := runGitDetect
	runGitDetect = func(_, name string, args ...string) string {
		fake.calls = append(fake.calls, gitCall{name: name, args: append([]string(nil), args...)})
		return reply(name, args)
	}
	t.Cleanup(func() { runGitDetect = orig })
	return fake
}

// emptyRepoGit reproduces what git actually does in a repository with no commits: `git rev-parse
// HEAD` prints the literal string "HEAD" on stdout and reports the failure only through its exit
// code, while the --verify form exits non-zero with nothing on stdout. runGitDetect drops the error
// and returns stdout, so the difference between those two argv is the whole difference between
// recording nothing and recording a commit called "HEAD".
func emptyRepoGit(_ string, args []string) string {
	for _, a := range args {
		if a == "--verify" {
			return ""
		}
	}
	if len(args) > 0 && args[0] == "rev-parse" {
		return "HEAD"
	}
	return ""
}

const fakeSHA = "0123456789abcdef0123456789abcdef01234567"

// TestCheckoutCommitRecordsOnlyARealCommit pins the honesty rule this whole file is built on
// (#678 K1): a derivation that cannot answer records "", which reads as "nobody recorded this",
// and never records a plausible-looking substitute, which would read as evidence.
func TestCheckoutCommitRecordsOnlyARealCommit(t *testing.T) {
	t.Run("a repository with no commits has no checkout commit", func(t *testing.T) {
		installFakeGit(t, emptyRepoGit)
		if got := checkoutCommit(t.TempDir()); got != "" {
			t.Errorf("checkout_commit = %q from a repository with no commits, want empty — every "+
				"record of every such run would otherwise share one fictional commit", got)
		}
	})

	t.Run("an abbreviated id is not a commit", func(t *testing.T) {
		installFakeGit(t, func(string, []string) string { return "0123456" })
		if got := checkoutCommit(t.TempDir()); got != "" {
			t.Errorf("checkout_commit = %q, want empty — an abbreviation's length is a property of "+
				"the clone it was made in, so one commit would be two strings across two clones", got)
		}
	})

	t.Run("a real commit is recorded verbatim", func(t *testing.T) {
		installFakeGit(t, func(string, []string) string { return fakeSHA })
		if got := checkoutCommit(t.TempDir()); got != fakeSHA {
			t.Errorf("checkout_commit = %q, want %q", got, fakeSHA)
		}
	})
}

// TestBaseCommitStaysLocalAndSafe pins the two properties baseCommit trades away accuracy for.
//
// It runs on af done, at every step close. detectDefaultBranch would answer more often, but its
// second and third rungs are `git ls-remote` and `gh` at 5s each — so on a factory whose origin/HEAD
// is unset, every step boundary in every run would stall for up to ten seconds to guess at a field
// that is optional. The ref it does find is also interpolated into an argv, which is why it must
// survive isValidBranchName before merge-base ever sees it.
func TestBaseCommitStaysLocalAndSafe(t *testing.T) {
	t.Run("the base commit is the local merge-base", func(t *testing.T) {
		fake := installFakeGit(t, func(_ string, args []string) string {
			switch args[0] {
			case "symbolic-ref":
				return "origin/main"
			case "merge-base":
				return fakeSHA
			}
			return ""
		})
		if got := baseCommit(t.TempDir()); got != fakeSHA {
			t.Errorf("base_commit = %q, want %q", got, fakeSHA)
		}
		if fake.ran("ls-remote") || fake.ran("gh") {
			t.Errorf("a step close reached for the network: %s", fake.transcript())
		}
	})

	t.Run("an unset origin/HEAD is not worth a network round trip", func(t *testing.T) {
		fake := installFakeGit(t, func(name string, args []string) string {
			switch {
			case args[0] == "symbolic-ref":
				return ""
			case args[0] == "ls-remote", name == "gh":
				return "refs/heads/main" // the remote would happily answer; nobody may ask
			case args[0] == "merge-base":
				return fakeSHA
			}
			return ""
		})
		if got := baseCommit(t.TempDir()); got != "" {
			t.Errorf("base_commit = %q, want empty — the answer was only reachable over the network", got)
		}
		if fake.ran("ls-remote") || fake.ran("gh") {
			t.Errorf("a step close reached for the network: %s", fake.transcript())
		}
	})

	t.Run("a flag-like ref never reaches merge-base", func(t *testing.T) {
		fake := installFakeGit(t, func(_ string, args []string) string {
			if args[0] == "symbolic-ref" {
				return "--upload-pack=payload"
			}
			return fakeSHA
		})
		if got := baseCommit(t.TempDir()); got != "" {
			t.Errorf("base_commit = %q, want empty", got)
		}
		if fake.ran("merge-base") {
			t.Errorf("a ref git would read as an option was interpolated into an argv: %s", fake.transcript())
		}
	})

	t.Run("a merge-base that is not a commit is not recorded", func(t *testing.T) {
		installFakeGit(t, func(_ string, args []string) string {
			if args[0] == "symbolic-ref" {
				return "origin/main"
			}
			return "fatal: no merge base found"
		})
		if got := baseCommit(t.TempDir()); got != "" {
			t.Errorf("base_commit = %q, want empty", got)
		}
	})
}
