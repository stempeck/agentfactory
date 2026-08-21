package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/memory"
)

// Risk R1 made loud. The vault is container-local and git-invisible, and ADR-019 forbids closing
// that hole by requiring container recreation — so the residual stays, and what this suite pins is
// the compensating control: the two moments an operator can still act on it (the preflight of
// `af up`, and factory-wide `af down --all`) say how much is at risk and how long it has been.
//
// Every row also pins the negative half. A warning that fired at zero notes would be noise on
// every factory that never used memory; one that could fail a launch would be worse than the risk
// it describes; and one that printed before the teardown authority gate would hand a refused agent
// a factory-wide note count.

func seedStalenessFactory(t *testing.T, notes int) string {
	t.Helper()
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(afDir, "agents", "solver"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(afDir, "factory.json"), `{"type":"factory","version":1,"name":"test"}`)
	writeFixtureFile(t, filepath.Join(afDir, "agents.json"),
		`{"agents":{"solver":{"type":"autonomous","description":"s"}}}`)
	for i := 0; i < notes; i++ {
		seedNote(t, root, "solver", memory.Note{Body: "a learning worth not losing"})
	}
	return root
}

// upPreflight drives the real runUp far enough to have printed its preflight and returns what
// reached each stream. runUp's own outcome is deliberately ignored: this factory has no git repo,
// so worktree creation fails and runUp returns its non-fatal aggregate — which is precisely the
// point, because the staleness line must be emitted before any of that can go wrong.
func upPreflight(t *testing.T, root string) (stdout, stderr string) {
	t.Helper()
	setupHermeticSessions(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")

	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	_ = runUp(cmd, nil)
	return out.String(), errb.String()
}

func TestUpVaultStaleness_PreflightNamesNoteCountAndNeverExported(t *testing.T) {
	root := seedStalenessFactory(t, 3)

	stdout, stderr := upPreflight(t, root)

	if !strings.Contains(stderr, "vault: 3 notes; last export: never") {
		t.Errorf("af up did not warn that 3 unbacked-up notes are at risk; stderr=%q", stderr)
	}
	// Stderr, joining checkGitHooksExecutable / warnUnobservableAgents (up.go): stdout is the
	// launch transcript other tooling reads, and a warning there would change that contract.
	if strings.Contains(stdout, "vault:") {
		t.Errorf("the vault warning belongs on stderr with its preflight siblings; stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "af memory export") {
		t.Errorf("the warning names a risk but not the remedy; stderr=%q", stderr)
	}
}

func TestUpVaultStaleness_ReportsDaysSinceLastExport(t *testing.T) {
	root := seedStalenessFactory(t, 1)
	if err := saveMemoryExportState(root, time.Now().UTC().Add(-5*24*time.Hour)); err != nil {
		t.Fatalf("seeding the export marker: %v", err)
	}

	_, stderr := upPreflight(t, root)

	if !strings.Contains(stderr, "last export: 5 days ago") {
		t.Errorf("want the age of the last export; stderr=%q", stderr)
	}
	if strings.Contains(stderr, "never") {
		t.Errorf("a factory that HAS been exported must not read as never; stderr=%q", stderr)
	}
	// Five days is inside the cadence, so the line reports without prescribing: a remedy repeated
	// on every launch is scenery by the time it matters.
	if strings.Contains(stderr, "remediation: the vault is container-local") {
		t.Errorf("a recently-exported vault should not carry the remediation line; stderr=%q", stderr)
	}
}

func TestUpVaultStaleness_SilentAtZeroNotes(t *testing.T) {
	root := seedStalenessFactory(t, 0)

	stdout, stderr := upPreflight(t, root)

	// PreservedLine's rule (report.go:23-24): at zero notes nothing is at risk, so a factory that
	// has never recorded a learning sees no new output at all.
	if strings.Contains(stderr, "vault:") || strings.Contains(stdout, "vault:") {
		t.Errorf("an empty vault produced a warning; stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestUpVaultStaleness_NeverBlocksLaunch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny reads")
	}
	root := seedStalenessFactory(t, 2)
	vault := filepath.Join(root, ".agentfactory", "memory")
	if err := os.Chmod(vault, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(vault, 0o755) })

	stdout, stderr := upPreflight(t, root)

	// The preflight cluster's shared contract: a warning that cannot be computed is not a launch
	// failure. `af up` must proceed exactly as it would have.
	if !strings.Contains(stdout, "factory: ") {
		t.Errorf("an unreadable vault stopped af up before its own preflight; stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.Contains(stderr, "vault: ") {
		t.Errorf("an uncountable vault must say nothing rather than guess; stderr=%q", stderr)
	}
}

func TestDownVaultStaleness_WarnsOnFactoryWideTeardown(t *testing.T) {
	setupDownFactory(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	seedNote(t, root, "manager", memory.Note{Body: "a learning that outlives this container only if exported"})

	// Mandatory: with downAll set, an OPERATOR-tier runDown reaches killOrphanedClaudeProcesses
	// and would sweep matching processes across the whole host. The runPkill recorder makes the
	// zero-host-kill posture structural rather than a consequence of some other gate staying correct.
	pkillCalls := installPkillRecorder(t)
	downAll = true
	t.Cleanup(func() { downAll, downReset = false, false })
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")

	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	if err := runDown(cmd, nil); err != nil {
		t.Fatalf("runDown --all: %v", err)
	}

	if !strings.Contains(errb.String(), "vault: 1 note; last export: never") {
		t.Errorf("factory-wide teardown did not warn about the unbacked-up vault; stderr=%q", errb.String())
	}
	if len(*pkillCalls) > 1 {
		t.Errorf("unexpected orphan-sweep calls: %v", *pkillCalls)
	}
}

func TestDownVaultStaleness_SuppressedAtZeroNotes(t *testing.T) {
	setupDownFactory(t)
	installPkillRecorder(t)
	downAll = true
	t.Cleanup(func() { downAll, downReset = false, false })
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")

	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	if err := runDown(cmd, nil); err != nil {
		t.Fatalf("runDown --all: %v", err)
	}

	if strings.Contains(errb.String(), "vault:") {
		t.Errorf("an empty vault produced a teardown warning; stderr=%q", errb.String())
	}
}

func TestDownVaultStaleness_NotPrintedForAScopedDown(t *testing.T) {
	setupDownFactory(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	seedNote(t, root, "manager", memory.Note{Body: "a learning"})
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")

	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	if err := runDown(cmd, []string{"manager"}); err != nil {
		t.Fatalf("scoped runDown: %v", err)
	}

	// Deliberately scoped to --all. Stopping one agent is a routine act an operator performs many
	// times a session; the export reminder belongs at the moment the whole factory is coming down,
	// or it becomes the noise that gets filtered before the one time it mattered.
	if strings.Contains(errb.String(), "vault:") {
		t.Errorf("a scoped `af down <agent>` printed the factory-wide vault warning; stderr=%q", errb.String())
	}
}

func TestDownVaultStaleness_NotLeakedToARefusedAgentCaller(t *testing.T) {
	setupDownFactory(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	seedNote(t, root, "manager", memory.Note{Body: "a learning"})
	installPkillRecorder(t)
	downAll = true
	t.Cleanup(func() { downAll, downReset = false, false })
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")

	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	if err := runDown(cmd, nil); err == nil {
		t.Fatal("factory-wide teardown from agent context must be refused (K5)")
	}

	// The warning sits BELOW the authority gate for this reason: a refused caller learns nothing
	// about the factory it was not allowed to tear down, note counts included.
	if strings.Contains(errb.String(), "vault:") || strings.Contains(out.String(), "vault:") {
		t.Errorf("a refused agent-tier caller was told the factory-wide note count; out=%q stderr=%q",
			out.String(), errb.String())
	}
}

// The formatting rows below drive the helper directly with an injected clock, so a boundary is
// pinned by moving time rather than by sleeping (ADR-018; the memoryAttribution precedent).
func TestUpVaultStaleness_AgeWordingAcrossBoundaries(t *testing.T) {
	root := seedStalenessFactory(t, 1)
	now := time.Now().UTC()

	for _, tc := range []struct {
		name        string
		exportedAgo time.Duration
		want        string
		wantRemedy  bool
	}{
		{"never exported", 0, "last export: never", true},
		{"same day", 2 * time.Hour, "last export: today", false},
		{"one day", 26 * time.Hour, "last export: 1 day ago", false},
		{"three days", 3 * 24 * time.Hour, "last export: 3 days ago", false},
		{"a fortnight", 14 * 24 * time.Hour, "last export: 14 days ago", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			markerPath := filepath.Join(root, ".runtime", "memory_export.json")
			_ = os.Remove(markerPath)
			if tc.exportedAgo != 0 {
				if err := saveMemoryExportState(root, now.Add(-tc.exportedAgo)); err != nil {
					t.Fatalf("seeding the marker: %v", err)
				}
			}

			var buf bytes.Buffer
			warnVaultExportStaleness(&buf, root, now)

			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("want %q; got %q", tc.want, buf.String())
			}
			hasRemedy := strings.Contains(buf.String(), "remediation:")
			if hasRemedy != tc.wantRemedy {
				t.Errorf("remediation present=%v want=%v; got %q", hasRemedy, tc.wantRemedy, buf.String())
			}
		})
	}
}
