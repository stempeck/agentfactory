package cmd

import "testing"

// K3: the owner/name validator accepts canonical slugs and rejects the empty,
// slashless, flag-like, multi-segment, and shell-meta forms — guarding gh
// flag-injection and terminal-escape before the value reaches disk/banner/gh.
func TestIsValidRepoSlug(t *testing.T) {
	valid := []string{"acme/widget", "org-name/repo.name_1", "a/b", "A1/B2"}
	for _, s := range valid {
		if !isValidRepoSlug(s) {
			t.Errorf("isValidRepoSlug(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"",                    // empty
		"noslash",             // no owner/name separator
		"-evil/x",             // leading dash → gh flag-injection
		"a/b/c",               // too many segments
		"acme/",               // empty repo
		"/widget",             // empty owner
		"acme widget/x",       // space
		"acme/wid get",        // space in repo
		"https://x/y",         // scheme chars (':')
		"$(touch pwned)/x",    // shell meta
		"acme/$(touch pwned)", // shell meta in repo
		"a/b\n",               // newline
	}
	for _, s := range invalid {
		if isValidRepoSlug(s) {
			t.Errorf("isValidRepoSlug(%q) = true, want false", s)
		}
	}
}

// K2: git remote URLs in all three shapes (scp-like, https, https-no-.git) plus
// ssh:// normalize to the canonical owner/name slug.
func TestRepoSlugFromRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/widget.git":       "acme/widget",
		"https://github.com/acme/widget.git":   "acme/widget",
		"https://github.com/acme/widget":       "acme/widget",
		"ssh://git@github.com/acme/widget.git": "acme/widget",
		"git@github.com:acme/widget.git\n":     "acme/widget",
	}
	for raw, want := range cases {
		if got := repoSlugFromRemote(raw); got != want {
			t.Errorf("repoSlugFromRemote(%q) = %q, want %q", raw, got, want)
		}
	}
}

// K2: discovery prefers `gh repo view` and returns the validated slug.
func TestDiscoverRepo_GHPrimary(t *testing.T) {
	orig := runGitDetect
	runGitDetect = func(workDir, name string, args ...string) string {
		if name == "gh" {
			return "acme/widget"
		}
		return ""
	}
	t.Cleanup(func() { runGitDetect = orig })

	if got := discoverRepo("/x"); got != "acme/widget" {
		t.Errorf("discoverRepo = %q, want acme/widget", got)
	}
}

// K2: when gh fails (no auth), discovery falls back to `git remote get-url origin`
// and normalizes it.
func TestDiscoverRepo_GitFallback(t *testing.T) {
	orig := runGitDetect
	runGitDetect = func(workDir, name string, args ...string) string {
		if name == "git" {
			return "git@github.com:acme/widget.git"
		}
		return "" // gh returns nothing
	}
	t.Cleanup(func() { runGitDetect = orig })

	if got := discoverRepo("/x"); got != "acme/widget" {
		t.Errorf("discoverRepo = %q, want acme/widget (git fallback)", got)
	}
}

// K2+K3: a crafted/garbage remote must be rejected by the validator → empty result
// (warn-don't-abort happens in the caller), never a malformed write.
func TestDiscoverRepo_CraftedRejected(t *testing.T) {
	orig := runGitDetect
	runGitDetect = func(workDir, name string, args ...string) string {
		if name == "git" {
			return "https://x/--evil/$(touch pwned)"
		}
		return ""
	}
	t.Cleanup(func() { runGitDetect = orig })

	if got := discoverRepo("/x"); got != "" {
		t.Errorf("discoverRepo on a crafted remote = %q, want \"\" (rejected)", got)
	}
}

// K2: no remote at all (both methods fail) → empty result, no panic.
func TestDiscoverRepo_NoRemote(t *testing.T) {
	orig := runGitDetect
	runGitDetect = func(workDir, name string, args ...string) string { return "" }
	t.Cleanup(func() { runGitDetect = orig })

	if got := discoverRepo("/x"); got != "" {
		t.Errorf("discoverRepo with no remote = %q, want \"\"", got)
	}
}
