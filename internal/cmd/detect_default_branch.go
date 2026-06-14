package cmd

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// detectBranchTimeout bounds each detection callout so a slow or unreachable
// remote cannot stall `af sling`. Methods 2 and 3 hit the network; method 1 is
// local but shares the bound harmlessly.
const detectBranchTimeout = 5 * time.Second

// branchNameAllowlist constrains accepted branch names to a conservative set
// (letters, digits, dot, underscore, slash, hyphen). ExpandTemplateVars performs
// NO shell escaping (internal/formula/vars.go) and the detected branch is baked
// verbatim into agent-executed step text, so every detection candidate MUST pass
// this allowlist before it is accepted (security.md SEC-1).
var branchNameAllowlist = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// isValidBranchName reports whether s is a safe branch name to inject: non-empty,
// not flag-like (no leading '-'), and limited to the allowlist character set.
func isValidBranchName(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && branchNameAllowlist.MatchString(s)
}

// runGitDetect runs a read-only git/gh command in workDir under a bounded timeout
// and returns its trimmed stdout, or "" on any error. Declared as a package-level
// var (ADR-009 seam) so unit tests can inject canned command output without a real
// git/gh on PATH or a live network. Mirrors getCurrentGitBranch (prime.go) for the
// err->"" convention and prime.go's context.WithTimeout/exec.CommandContext idiom.
var runGitDetect = func(workDir, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), detectBranchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectDefaultBranch resolves the repository's actual default branch via a
// layered, READ-ONLY chain. Declared as a package-level var (ADR-009 seam) so
// callers' tests can stub it (like newIssueStore / launchAgentSession). Returns ""
// when every method fails or yields an invalid name; the caller (sling) owns the
// fail-loud fallback and must never silently substitute "main". The chain never
// writes to or repairs origin/HEAD — it is strictly read-only (ADR-017 / SEC-2).
var detectDefaultBranch = func(workDir string) string {
	// 1. Local, offline: origin/HEAD symbolic ref (e.g. "origin/master").
	if out := runGitDetect(workDir, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); out != "" {
		if b := strings.TrimPrefix(out, "origin/"); isValidBranchName(b) {
			return b
		}
	}
	// 2. Read-only, host-agnostic (network): ls-remote --symref → "ref: refs/heads/<X>".
	if out := runGitDetect(workDir, "git", "ls-remote", "--symref", "origin", "HEAD"); out != "" {
		if b := parseLsRemoteSymref(out); isValidBranchName(b) {
			return b
		}
	}
	// 3. GitHub-only (network): gh repo view. gh has no -C flag, so runGitDetect
	//    sets cmd.Dir = workDir.
	if out := runGitDetect(workDir, "gh", "repo", "view", "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name"); out != "" {
		if isValidBranchName(out) {
			return out
		}
	}
	return ""
}

// parseLsRemoteSymref extracts <X> from `git ls-remote --symref origin HEAD`
// output, whose symref line looks like "ref: refs/heads/<X>\tHEAD". Returns "" if
// no such line is present.
func parseLsRemoteSymref(out string) string {
	const prefix = "ref: refs/heads/"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		// rest is "<X>\tHEAD" (or "<X> HEAD"); take everything before the first
		// whitespace.
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			rest = rest[:i]
		}
		return rest
	}
	return ""
}
