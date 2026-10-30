package cmd

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var toolchainParityFiles = []string{"quickstart.sh", "go.mod", "web/go.mod", ".github/workflows/test.yml"}

var (
	goToolchainPinRe = regexp.MustCompile(`(?m)^GO_TOOLCHAIN_VERSION="(go[^"$]+)"$`)
	goModGoLineRe    = regexp.MustCompile(`(?m)^go \S+$`)
	goModToolchainRe = regexp.MustCompile(`(?m)^toolchain (go\S+)$`)
	ciGoVersionRe    = regexp.MustCompile(`go-version: '([^']+)'`)
	setupGoRefRe     = regexp.MustCompile(`actions/setup-go@\S+`)
)

// toolchainParityErrors holds the one Go toolchain pin to its four mirrors (DD:125): the toolchain
// directive in both modules and the go-version of the four CI lanes. A lane left on an older Go
// would test code the installed toolchain never builds.
func toolchainParityErrors(files map[string]string) []string {
	var errs []string
	pin := ""
	if m := goToolchainPinRe.FindAllStringSubmatch(files["quickstart.sh"], -1); len(m) == 1 {
		pin = m[0][1]
	} else {
		errs = append(errs, fmt.Sprintf("quickstart.sh must declare GO_TOOLCHAIN_VERSION=\"go…\" exactly once; found %d", len(m)))
	}

	for _, mod := range []string{"go.mod", "web/go.mod"} {
		content := files[mod]
		goLines := goModGoLineRe.FindAllStringIndex(content, -1)
		toolchains := goModToolchainRe.FindAllStringSubmatchIndex(content, -1)
		if len(goLines) != 1 {
			errs = append(errs, fmt.Sprintf("%s must keep exactly one `go` line; found %d", mod, len(goLines)))
		}
		if len(toolchains) != 1 {
			errs = append(errs, fmt.Sprintf("%s must carry exactly one `toolchain go…` line; found %d", mod, len(toolchains)))
			continue
		}
		if got := content[toolchains[0][2]:toolchains[0][3]]; pin != "" && got != pin {
			errs = append(errs, fmt.Sprintf("%s says toolchain %s, quickstart.sh pins GO_TOOLCHAIN_VERSION=%s", mod, got, pin))
		}
		if len(goLines) == 1 && toolchains[0][0] < goLines[0][0] {
			errs = append(errs, fmt.Sprintf("%s: the toolchain line must follow the go line", mod))
		}
	}

	ci := files[".github/workflows/test.yml"]
	want := strings.TrimPrefix(pin, "go")
	versions := ciGoVersionRe.FindAllStringSubmatch(ci, -1)
	if len(versions) != 4 {
		errs = append(errs, fmt.Sprintf("test.yml must set go-version in exactly 4 lanes; found %d", len(versions)))
	}
	for _, v := range versions {
		if pin != "" && v[1] != want {
			errs = append(errs, fmt.Sprintf("test.yml says go-version '%s', quickstart.sh pins %s (want '%s')", v[1], pin, want))
		}
	}
	refs := setupGoRefRe.FindAllString(ci, -1)
	if len(refs) != 4 {
		errs = append(errs, fmt.Sprintf("test.yml must use actions/setup-go in exactly 4 lanes; found %d", len(refs)))
	}
	for _, r := range refs {
		if r != "actions/setup-go@v5" {
			errs = append(errs, fmt.Sprintf("test.yml uses %s; the lanes stay on actions/setup-go@v5 (v6 exports GOTOOLCHAIN=local)", r))
		}
	}
	return errs
}

func TestToolchainParity(t *testing.T) {
	t.Run("real toolchain mirrors agree with quickstart.sh", func(t *testing.T) {
		files := map[string]string{}
		for _, rel := range toolchainParityFiles {
			files[rel] = readRepoFile(t, rel)
		}
		for _, e := range toolchainParityErrors(files) {
			t.Error(e)
		}
	})

	lane := "      - uses: actions/setup-go@v5\n        with:\n          go-version: '7.8.9'\n"
	consistent := func() map[string]string {
		return map[string]string{
			"quickstart.sh":              "GO_TOOLCHAIN_VERSION=\"go7.8.9\"\n",
			"go.mod":                     "module a\n\ngo 1.24.2\n\ntoolchain go7.8.9\n",
			"web/go.mod":                 "module b\n\ngo 1.24.2\ntoolchain go7.8.9\n",
			".github/workflows/test.yml": strings.Repeat(lane, 4),
		}
	}

	t.Run("synthetic consistent mirrors pass", func(t *testing.T) {
		if errs := toolchainParityErrors(consistent()); len(errs) != 0 {
			t.Fatalf("a consistent synthetic tree must pass; got %q", errs)
		}
	})

	cases := []struct {
		name, file, from, to, wantSubstr string
	}{
		{"mismatched go-version", ".github/workflows/test.yml", "go-version: '7.8.9'", "go-version: '7.8'", "go-version '7.8'"},
		{"mismatched toolchain line", "go.mod", "toolchain go7.8.9", "toolchain go7.8.8", "go.mod says toolchain go7.8.8"},
		{"missing web toolchain line", "web/go.mod", "toolchain go7.8.9\n", "", "web/go.mod must carry exactly one"},
		{"toolchain before go line", "go.mod", "go 1.24.2\n\ntoolchain go7.8.9\n", "toolchain go7.8.9\ngo 1.24.2\n", "must follow the go line"},
		{"setup-go bumped", ".github/workflows/test.yml", "setup-go@v5", "setup-go@v6", "actions/setup-go@v6"},
	}
	for _, tc := range cases {
		t.Run("synthetic "+tc.name+" is reported", func(t *testing.T) {
			files := consistent()
			files[tc.file] = strings.Replace(files[tc.file], tc.from, tc.to, 1)
			errs := toolchainParityErrors(files)
			if len(errs) != 1 || !strings.Contains(errs[0], tc.wantSubstr) {
				t.Fatalf("got %q; want exactly one report containing %q", errs, tc.wantSubstr)
			}
		})
	}
}

// TestAgentGenAllRebuildsWithLocalToolchain: once go.mod carries a toolchain directive, a bare
// `make install` under GOTOOLCHAIN=auto would download the newer Go and verify it with the old
// cmd/go (H-3).
func TestAgentGenAllRebuildsWithLocalToolchain(t *testing.T) {
	content := readRepoFile(t, "agent-gen-all.sh")
	local := regexp.MustCompile(`(?m)^[ \t]*GOTOOLCHAIN=local make -C "\$AF_SRC" install$`)
	bare := regexp.MustCompile(`(?m)^[ \t]*make -C "\$AF_SRC" install$`)

	if n := len(bare.FindAllStringIndex(content, -1)); n != 0 {
		t.Errorf("agent-gen-all.sh has %d bare `make -C \"$AF_SRC\" install` line(s); the rebuild must run under GOTOOLCHAIN=local", n)
	}
	matches := local.FindAllStringIndex(content, -1)
	if len(matches) != 1 {
		t.Fatalf("agent-gen-all.sh must carry exactly one `GOTOOLCHAIN=local make -C \"$AF_SRC\" install` line; found %d", len(matches))
	}

	marker := strings.Index(content, "# --- Rebuild af")
	if marker == -1 {
		t.Fatal("agent-gen-all.sh lost its `# --- Rebuild af` section")
	}
	guard := strings.Index(content[marker:], `if [ "$DO_BUILD" = true ]; then`)
	if guard == -1 {
		t.Fatal("agent-gen-all.sh's rebuild section lost its `if [ \"$DO_BUILD\" = true ]` guard")
	}
	guard += marker
	end := strings.Index(content[guard:], "\nfi\n")
	if end == -1 {
		t.Fatal("agent-gen-all.sh's DO_BUILD block has no closing fi")
	}
	if at := matches[0][0]; at < guard || at > guard+end {
		t.Error("the GOTOOLCHAIN=local rebuild must sit inside the `# --- Rebuild af` section's DO_BUILD block (--no-build skips it)")
	}
}
