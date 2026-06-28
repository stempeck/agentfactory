package cmd

import (
	"regexp"
	"strings"
)

// repoSlugAllowlist constrains an accepted owner/name slug to a conservative set:
// letters, digits, dot, underscore, hyphen in each of exactly two slash-separated
// segments. The discovered repo is written into dispatch.json, echoed in the install
// banner, and later passed to `gh --repo <slug>`, so every candidate MUST pass this
// allowlist — it blocks shell-meta / terminal-escape and (with the leading-'-' guard
// in isValidRepoSlug) gh flag-injection. Mirrors branchNameAllowlist
// (detect_default_branch.go, security.md SEC-1).
var repoSlugAllowlist = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// isValidRepoSlug reports whether s is a safe owner/name to store and shell out with:
// non-empty, not flag-like (no leading '-'), exactly owner/name, allowlist charset.
func isValidRepoSlug(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && repoSlugAllowlist.MatchString(s)
}

// repoSlugFromRemote normalizes a `git remote get-url origin` value — in any of the
// common shapes (scp-like git@host:owner/repo.git, https://host/owner/repo[.git],
// ssh://host/owner/repo.git) — to the canonical owner/name slug. The result is NOT
// validated here; the caller (discoverRepo) runs isValidRepoSlug. Returns "" when it
// cannot extract two trailing path segments.
func repoSlugFromRemote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		// scheme://host/owner/repo → strip "scheme://host".
		rest := s[i+3:]
		j := strings.IndexByte(rest, '/')
		if j < 0 {
			return ""
		}
		s = rest[j+1:]
	} else if i := strings.LastIndex(s, ":"); i >= 0 {
		// scp-like user@host:owner/repo → keep the part after ':'.
		s = s[i+1:]
	}
	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// discoverRepo resolves the home repository's owner/name non-interactively at install
// time (issue #73 K2 — the architecture-elevation Frame-lift). It prefers `gh repo view`
// (canonical nameWithOwner, no URL parsing) and falls back to `git remote get-url origin`
// normalization (works without gh auth). Read-only and timeout-bounded via the
// runGitDetect seam (ADR-014 non-interactive, ADR-017 read-only). Returns the VALIDATED
// slug, or "" when discovery fails or yields an unsafe value — the caller degrades to
// empty repos and warns; install never aborts (A3.1 warn-don't-abort).
func discoverRepo(workDir string) string {
	if slug := runGitDetect(workDir, "gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"); isValidRepoSlug(slug) {
		return slug
	}
	if slug := repoSlugFromRemote(runGitDetect(workDir, "git", "remote", "get-url", "origin")); isValidRepoSlug(slug) {
		return slug
	}
	return ""
}
