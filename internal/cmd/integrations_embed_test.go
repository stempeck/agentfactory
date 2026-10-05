package cmd

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// TestEmbeddedIntegrationManifests pins IMPLREADME_PHASE2 AC 4: the embedded integrations file set
// equals `git ls-files internal/cmd/integrations`, so an `all:` embed cannot ship a stray untracked
// dotfile, and README.md keeps the embed non-empty (an empty embed dir fails `go build`).
func TestEmbeddedIntegrationManifests(t *testing.T) {
	root := findModuleRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "--", "internal/cmd/integrations").Output()
	if err != nil {
		t.Fatalf("git ls-files internal/cmd/integrations: %v", err)
	}
	var tracked []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			tracked = append(tracked, strings.TrimPrefix(l, "internal/cmd/"))
		}
	}

	var embedded []string
	err = fs.WalkDir(embeddedIntegrations, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			embedded = append(embedded, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embeddedIntegrations: %v", err)
	}
	sort.Strings(tracked)
	sort.Strings(embedded)

	if !slices.Contains(tracked, "integrations/README.md") {
		t.Errorf("git ls-files internal/cmd/integrations does not list README.md (tracked: %q)", tracked)
	}
	if !slices.Contains(embedded, "integrations/README.md") {
		t.Errorf("embeddedIntegrations has no integrations/README.md (embedded: %q)", embedded)
	}
	if strings.Join(tracked, "\n") != strings.Join(embedded, "\n") {
		t.Errorf("embedded integrations file set differs from git ls-files:\nembedded: %q\ntracked:  %q", embedded, tracked)
	}
}

// Acquire cannot read an embedded file's mode, so its exec rule is a proxy; this holds the proxy
// equal to the git index mode for every shipped file.
func TestPR724_T10_EmbeddedAcquireModesMatchGitIndex(t *testing.T) {
	root := findModuleRoot(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "-s", "--", "internal/cmd/integrations").Output()
	if err != nil {
		t.Fatalf("git ls-files -s internal/cmd/integrations: %v", err)
	}
	indexMode := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		meta, p, ok := strings.Cut(l, "\t")
		if !ok {
			continue
		}
		indexMode[strings.TrimPrefix(p, "internal/cmd/integrations/")] = strings.Fields(meta)[0]
	}

	sub, err := fs.Sub(embeddedIntegrations, "integrations")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(sub, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		name := en.Name()
		dir := filepath.Join(t.TempDir(), name)
		if err := copyEmbeddedIntegration(sub, name, dir); err != nil {
			t.Errorf("acquiring embedded integration %s: %v", name, err)
			continue
		}
		werr := fs.WalkDir(sub, name, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p[len(name)+1:])))
			if err != nil {
				return err
			}
			acquiredExec := info.Mode().Perm()&0o111 != 0
			if indexExec := indexMode[p] == "100755"; acquiredExec != indexExec {
				t.Errorf("internal/cmd/integrations/%s: acquire makes it executable=%v but its git index mode is %q; "+
					"fix the git mode (git update-index --chmod=+x or -x) or add/drop its #! first line", p, acquiredExec, indexMode[p])
			}
			return nil
		})
		if werr != nil {
			t.Errorf("walking embedded integration %s: %v", name, werr)
		}
	}
}
