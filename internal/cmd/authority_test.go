package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// teardownRefusalSurfaces are the four command surfaces that emit the K1 refusal.
// The body is identical across all of them; only the parenthesized surface token on
// the first line varies (ux.md Option U1).
var teardownRefusalSurfaces = []string{
	"af down",
	"af down --reset",
	"af install --agents",
	"af dispatch stop",
}

func refusalFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestTeardownRefusalFirstLineContract(t *testing.T) {
	got := refusalFirstLine(teardownRefusal("af down"))
	want := "teardown refused: agent context (af down)"
	if got != want {
		t.Fatalf("first line = %q, want %q", got, want)
	}
}

func TestTeardownRefusalBlastRadiusContract(t *testing.T) {
	// Collapse internal whitespace so contract phrases match regardless of the verbatim
	// ux.md line wrapping (e.g. the directive wraps as "Skip this step and\ncontinue").
	flat := strings.Join(strings.Fields(teardownRefusal("af down")), " ")
	for _, want := range []string{
		"YOU (this session)",          // the caller's own session
		"sibling",                     // every sibling agent
		"manager",                     // the interactive manager
		"Do NOT retry",                // do-not-retry directive
		"Skip this step and continue", // proceed-with-remaining-work directive
		"af mail send manager",        // operator redirect
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("refusal text missing required AC-6 element %q", want)
		}
	}
}

func TestTeardownRefusalSurfaceIsOnlyVariation(t *testing.T) {
	body := func(surface string) string {
		full := teardownRefusal(surface)
		if i := strings.IndexByte(full, '\n'); i >= 0 {
			return full[i:]
		}
		return ""
	}
	base := body(teardownRefusalSurfaces[0])
	for _, surface := range teardownRefusalSurfaces {
		if b := body(surface); b != base {
			t.Errorf("body for surface %q differs from the base body; the body must be byte-identical across surfaces", surface)
		}
		wantFirst := "teardown refused: agent context (" + surface + ")"
		if got := refusalFirstLine(teardownRefusal(surface)); got != wantFirst {
			t.Errorf("surface %q first line = %q, want %q", surface, got, wantFirst)
		}
	}
}

func TestTeardownRefusalDoesNotDiscloseMechanism(t *testing.T) {
	got := teardownRefusal("af down")
	for _, banned := range []string{"AF_ROLE", "tmux", "display-message", "#S", "$TMUX"} {
		if strings.Contains(got, banned) {
			t.Errorf("refusal discloses the detection mechanism via %q — ux.md L36-39 forbids handing the agent a bypass recipe", banned)
		}
	}
}

// --- K3 classifier: callerAuthority signal matrix (AC-1) ---

// TestCallerAuthority_SignalMatrix is the design-mandated signal matrix (AF_ROLE ×
// tmux self-identity × error states), written before the classifier lands (TDD). It
// forces callerAuthority to read signal 2 through the newCmdTmux() seam: a real Tmux
// returns "",nil under the guarded default build (tmux.go:302-304), so fake.currentSession
// is the only way to exercise a positive af-identity. Every row forces AF_ROLE explicitly
// because this branch runs inside an agent where AF_ROLE is ambient.
func TestCallerAuthority_SignalMatrix(t *testing.T) {
	fake := installFakeTmuxPresent(t) // overrides newCmdTmux; currentSession spoofs signal 2
	rows := []struct {
		name        string
		afRole      string
		tmux        bool
		sessionName string
		want        Authority
	}{
		{"AFRoleSet_alone_isAgent", "manager", false, "", AuthorityAgent},
		{"BothSignals_isAgent", "manager", true, "af-manager", AuthorityAgent},
		{"incidentVector_AFRoleUnset_TmuxAfIdentity_isAgent", "", true, "af-manager", AuthorityAgent},
		{"AFRoleUnset_TestSession_isOperator", "", true, "af-test-abc123-manager", AuthorityOperator},
		{"AFRoleUnset_NonAfSession_isOperator", "", true, "mysession", AuthorityOperator},
		{"AFRoleUnset_NoTmux_isOperator", "", false, "", AuthorityOperator},
		{"AFRoleUnset_WatchdogIdentity_isAgent", "", true, "af-watchdog", AuthorityAgent},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("AF_ROLE", row.afRole)
			if row.tmux {
				t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
			} else {
				t.Setenv("TMUX", "")
			}
			fake.currentSession = row.sessionName
			if got := callerAuthority(); got != row.want {
				t.Errorf("callerAuthority() = %d, want %d (AF_ROLE=%q TMUX=%v session=%q)",
					got, row.want, row.afRole, row.tmux, row.sessionName)
			}
		})
	}
	// Belt-and-suspenders for the AC-1 grep ('incident'|'af.identity'): the named
	// incident-vector row above is AF_ROLE unset + an af-identity $TMUX session ⇒ Agent.
	t.Logf("incident vector covered: AF_ROLE unset but $TMUX names an af-identity session ⇒ Agent")
}

// --- callerDispatched provenance (AC-2) ---

// TestCallerDispatched_Provenance drives the real .runtime/formula_caller substrate both
// directions. callerDispatched self-resolves the local root (config.FindLocalRoot(cwd)) and
// the caller identity (resolveAgentName/AF_ROLE), so the test needs a full factory scaffold
// + t.Chdir + AF_ROLE.
func TestCallerDispatched_Provenance(t *testing.T) {
	root := t.TempDir()
	writeAgentsJSON(t, root, `{"agents":{
  "manager":{"type":"interactive","description":"caller"},
  "solver":{"type":"autonomous","description":"target","formula":"factoryworker"}}}`)
	if err := os.WriteFile(config.FactoryConfigPath(root), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("AF_ROLE", "manager") // caller identity
	installFakeTmuxPresent(t)
	t.Setenv("TMUX", "") // keep signal 2 quiet

	targetDir := config.AgentDir(root, "solver")

	writeRuntimeFile(t, targetDir, "formula_caller", "manager")
	if !callerDispatched("solver") {
		t.Error("callerDispatched(solver) = false, want true when the target's formula_caller matches the caller")
	}

	writeRuntimeFile(t, targetDir, "formula_caller", "someoneelse")
	if callerDispatched("solver") {
		t.Error("callerDispatched(solver) = true, want false when the target's formula_caller differs from the caller")
	}

	if err := os.Remove(filepath.Join(targetDir, ".runtime", "formula_caller")); err != nil {
		t.Fatal(err)
	}
	if callerDispatched("solver") {
		t.Error("callerDispatched(solver) = true, want false when the target's formula_caller is missing")
	}
}

// --- requireOperatorTeardown contract + K4 artifact (AC-3, AC-6) ---

// TestRequireOperatorTeardown_AppendsRefusalArtifact proves the agent branch returns the
// K1 error AND appends (never truncates) the K4 forensic artifact.
func TestRequireOperatorTeardown_AppendsRefusalArtifact(t *testing.T) {
	root := t.TempDir()
	writeAgentsJSON(t, root, `{"agents":{"manager":{"type":"interactive","description":"c"}}}`)
	if err := os.WriteFile(config.FactoryConfigPath(root), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("AF_ROLE", "manager") // signal 1 ⇒ Agent branch
	installFakeTmuxPresent(t)
	t.Setenv("TMUX", "")

	err1 := requireOperatorTeardown("af down")
	err2 := requireOperatorTeardown("af down")
	if err1 == nil || err2 == nil {
		t.Fatalf("agent branch must return a non-nil error so cobra prints+exits 1 (err1=%v err2=%v)", err1, err2)
	}
	if err1.Error() != teardownRefusal("af down") {
		t.Errorf("error message = %q, want the K1 refusal text", err1.Error())
	}

	artifact := filepath.Join(config.AgentDir(root, "manager"), ".runtime", "teardown_refused")
	data, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatalf("reading teardown_refused: %v", err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 2 {
		t.Errorf("teardown_refused has %d lines, want 2 (must APPEND, not truncate); content:\n%s", lines, data)
	}
	if !strings.Contains(string(data), "surface=af down") {
		t.Errorf("artifact line missing surface field; content:\n%s", data)
	}
}

// TestRequireOperatorTeardown_OperatorZeroOutput proves AC-3's byte-for-byte requirement:
// operator context returns nil and writes NO artifact.
func TestRequireOperatorTeardown_OperatorZeroOutput(t *testing.T) {
	root := t.TempDir()
	writeAgentsJSON(t, root, `{"agents":{"manager":{"type":"interactive","description":"c"}}}`)
	if err := os.WriteFile(config.FactoryConfigPath(root), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("AF_ROLE", "") // operator
	installFakeTmuxPresent(t)
	t.Setenv("TMUX", "")

	if err := requireOperatorTeardown("af down"); err != nil {
		t.Fatalf("operator branch must return nil, got %v", err)
	}
	artifact := filepath.Join(config.AgentDir(root, "manager"), ".runtime", "teardown_refused")
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Errorf("operator branch must write no artifact, but %s exists (stat err=%v)", artifact, err)
	}
}

// --- predicates: isSelfSession, isSpecialistTarget ---

func TestIsSelfSession(t *testing.T) {
	t.Setenv("AF_ROLE", "solver")
	if self := session.SessionName("solver"); !isSelfSession(self) {
		t.Errorf("isSelfSession(%q) = false, want true (the caller's own session)", self)
	}
	if isSelfSession(session.SessionName("manager")) {
		t.Error("isSelfSession(af-...-manager) = true, want false for a non-self target")
	}
}

func TestIsSpecialistTarget(t *testing.T) {
	if !isSpecialistTarget(config.AgentEntry{Type: "autonomous", Formula: "factoryworker"}) {
		t.Error("isSpecialistTarget: a formula-backed entry must classify as a specialist")
	}
	if isSpecialistTarget(config.AgentEntry{Type: "interactive"}) {
		t.Error("isSpecialistTarget: an entry without a formula must not classify as a specialist")
	}
}
