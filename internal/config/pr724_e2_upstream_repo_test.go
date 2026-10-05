package config

import (
	"fmt"
	"testing"
)

// The ssh prompt is closed in the fetch, not by narrowing what [upstream] repo may name: every manifest
// load (each bind included) runs this validator, so a scheme rule here would refuse installed snapshots.
func TestPR724_T15_KeepNonHTTPSUpstreamRepoAccepted(t *testing.T) {
	for _, repo := range []string{
		"https://github.com/acme/up",
		"ssh://git@github.com/acme/up.git",
		"git@github.com:acme/up.git",
		"file:///srv/git/up.git",
		"/srv/git/up.git",
	} {
		t.Run(repo, func(t *testing.T) {
			manifest := imHeader + fmt.Sprintf("\n[upstream]\nrepo = %q\ncommit = %q\n", repo, imDesignCommit)
			m, err := LoadIntegrationManifest(imPluginDir(t, "acme", manifest))
			if err != nil {
				t.Fatalf("[upstream] repo %q must stay accepted: %v", repo, err)
			}
			if m.Upstream == nil || m.Upstream.Repo != repo {
				t.Errorf("[upstream] repo = %+v, want %q verbatim", m.Upstream, repo)
			}
		})
	}
}
