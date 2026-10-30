package cmd

import (
	"strings"
	"testing"
)

// TestClaudeMdBuildCommandsListMakeLock pins PR #747 BODY-8 (L-17): the build list in CLAUDE.md is where
// a contributor learns the make targets, and `make lock` is the only way to regenerate the Python locks.
func TestClaudeMdBuildCommandsListMakeLock(t *testing.T) {
	sec, ok := dcSectionTitled(dcReadRepoFile(t, "CLAUDE.md"), "## Build Commands")
	if !ok {
		t.Fatal("CLAUDE.md has no `## Build Commands` heading")
	}
	bullets := map[string]string{}
	for _, line := range strings.Split(sec, "\n") {
		if strings.HasPrefix(line, "- `make ") {
			target, _, _ := strings.Cut(strings.TrimPrefix(line, "- `"), "`")
			bullets[target] = line
		}
	}

	t.Run("lists make lock", func(t *testing.T) {
		bullet, ok := bullets["make lock"]
		if !ok {
			t.Fatal("CLAUDE.md's `## Build Commands` has no \"- `make lock`\" bullet (BODY-8)")
		}
		for _, want := range []string{"python", "hash"} {
			if !strings.Contains(strings.ToLower(bullet), want) {
				t.Errorf("the `make lock` bullet never says it recompiles the %s locks: %q", want, bullet)
			}
		}
	})

	t.Run("keeps the existing build bullets", func(t *testing.T) {
		for _, target := range []string{"make sync-formulas", "make build", "make build-webui", "make test", "make install", "make test-integration", "make clean"} {
			if _, ok := bullets[target]; !ok {
				t.Errorf("CLAUDE.md's `## Build Commands` lost its %q bullet; BODY-8 adds one line and changes no other", target)
			}
		}
	})
}
