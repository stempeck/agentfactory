package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// Phase 5 addendum pins for design 695 Phase 2 slice B that have no natural home in an existing
// test file (concern_tests §2, §3 and §5.9).

// D59 / spec L439, L458: requireOperator is added to internal/cmd/authority.go beside
// requireOperatorTeardown, and defined nowhere else.
func TestRequireOperator_DefinedOnceInAuthorityGo(t *testing.T) {
	def := regexp.MustCompile(`(?m)^func requireOperator\(`)
	files, err := filepath.Glob("*.go") // cwd == internal/cmd during `go test`
	if err != nil {
		t.Fatal(err)
	}
	var where []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		for range def.FindAllIndex(src, -1) {
			where = append(where, f)
		}
	}
	if len(where) != 1 || where[0] != "authority.go" {
		t.Errorf("`func requireOperator(` is defined in %v; want exactly once, in authority.go (D59, spec L458)", where)
	}
}

// Phase 3 delivers integrations with --plugin-dir from buildStartupCommand, never from the kill-scope
// `claude :=` literal that TestKillScopeDrift pins (D24; spec L518-519). Protective.
func TestSessionLaunch_PluginDirOnlyInBuildStartupCommand(t *testing.T) {
	dir := filepath.Join(findModuleRoot(t), "internal", "session")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var mentions []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		if !strings.Contains(string(src), "--plugin-dir") {
			continue
		}
		mentions = append(mentions, filepath.Base(f))
		for _, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "claude := ") && strings.Contains(line, "--plugin-dir") {
				t.Errorf("%s: --plugin-dir rides the claude := literal: %s", filepath.Base(f), strings.TrimSpace(line))
			}
		}
	}
	if len(mentions) != 1 || mentions[0] != "session.go" {
		t.Errorf("--plugin-dir is emitted from %v; want exactly session.go (buildStartupCommand)", mentions)
	}
}

// consumers.md install.go:869-870: `af install --agents` ends with the report-only
// runPluginVerifyReport, which execs `af plugin verify --all`. Phase 2 widens that report to
// integration snapshot drift through verifyPlugins; this pins the --all path the report runs
// (plugin_verify_integration_test.go pins the by-name path).
func TestPluginVerifyReport_AllNamesIntegrationDrift(t *testing.T) {
	e := intBFactory(t)
	snap := intBRecord(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}), nil)
	if _, err := os.Stat(config.PluginsConfigPath(e.root)); err != nil {
		t.Fatalf("fixture: the K15 gate at install.go:869 reads plugins.json presence: %v", err)
	}
	skill := "claude-plugin/skills/" + intBName + "/SKILL.md"
	p := filepath.Join(snap, filepath.FromSlash(skill))
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}

	out, err := runPlugin(t, "verify", map[string]string{"all": "true"})
	if err == nil {
		t.Errorf("verify --all (what the install.go:870 report runs) must fail on a drifted integration snapshot:\n%s", out)
	}
	if c := intBCombined(out, err); !strings.Contains(c, skill) || !strings.Contains(c, intBName) {
		t.Errorf("verify --all must name the integration %s and its drifted file %s:\n%s", intBName, skill, c)
	}
}
