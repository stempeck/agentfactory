package cmd

import (
	"strings"
	"testing"
)

// 5b (T10, D9): the pure redaction choke point. scheme:// forms drop userinfo, query and
// fragment; a scheme:// form that does not parse becomes ""; scp-like forms drop the
// user[:pw]@ prefix; local paths are unchanged.
func TestRedactRemoteURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://oauth2:ghp_X@github.com/acme/plugin.git", "https://github.com/acme/plugin.git"},
		{"https://ghp_X@github.com/acme/plugin.git", "https://github.com/acme/plugin.git"},
		{"https://github.com/acme/plugin.git", "https://github.com/acme/plugin.git"},
		{"https://github.com/acme/plugin.git?access_token=ghp_X#frag", "https://github.com/acme/plugin.git"},
		{"ssh://git@github.com/acme/plugin.git", "ssh://github.com/acme/plugin.git"},
		{"ssh://git:pw_X@github.com/acme/plugin.git", "ssh://github.com/acme/plugin.git"},
		{"https://oauth2:ghp_X@github.com:notaport/acme/plugin.git", ""},
		{"git@github.com:acme/plugin.git", "github.com:acme/plugin.git"},
		{"user:pw_X@host.example:acme/plugin.git", "host.example:acme/plugin.git"},
		{"/home/dev/repos/plugin", "/home/dev/repos/plugin"},
		{"file:///srv/git/plugin.git", "file:///srv/git/plugin.git"},
		{"", ""},
	}
	for _, tc := range cases {
		got := redactRemoteURL(tc.in)
		if got != tc.want {
			t.Errorf("redactRemoteURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
		for _, secret := range []string{"ghp_X", "pw_X"} {
			if strings.Contains(got, secret) {
				t.Errorf("redactRemoteURL(%q) = %q leaks %q", tc.in, got, secret)
			}
		}
	}
}
