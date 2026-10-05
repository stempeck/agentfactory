package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// driftSnapshot rewrites one declared file of a recorded (read-only) snapshot, so its fresh hash no longer
// matches the recorded content_sha256.
func driftSnapshot(t *testing.T, snap, name string) {
	t.Helper()
	if err := intBChmodTree(snap, true); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(snap, "claude-plugin", "skills", name, "SKILL.md")
	if err := os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: tampered\n---\ntampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := intBChmodTree(snap, false); err != nil {
		t.Fatal(err)
	}
}

func writeCheckRecordState(t *testing.T, root, name, state string, at time.Time) {
	t.Helper()
	admWriteCheckRecord(t, root, name, "", at)
	p := integrationCheckRecordPath(root, name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), `"state":"ok"`, `"state":"`+state+`"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func primeLinesNaming(out, name string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, name) {
			lines = append(lines, l)
		}
	}
	return lines
}

func TestPrime_IntegrationLines(t *testing.T) {
	e := intBFactory(t)
	root := e.root
	agentDir := config.AgentDir(root, "supervisor")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installMemStore(t)
	t.Setenv("AF_ROLE", "")
	t.Chdir(agentDir)

	record := func(name string) string {
		return intBRecord(t, e, name, intBSource(e, name, intBManifestOpts{noCheck: true}), nil)
	}
	okSnap, badSnap, driftSnap := record("ok-int"), record("bad-int"), record("drift-int")
	writeCheckRecordState(t, root, "ok-int", "ok", time.Now())
	writeCheckRecordState(t, root, "bad-int", "fail", time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC))
	driftSnapshot(t, driftSnap, "drift-int")
	goneSnap := filepath.Join(snapshotParent(root, "gone-int"), strings.Repeat("d", 64))

	noPin := runPrimeCapturing(t)

	pin := integrationPin{
		Formula: "f",
		Bindings: []integrationPinBinding{
			pinFixtureBinding(t, root, "ok-int", okSnap, true),
			pinFixtureBinding(t, root, "bad-int", badSnap, true),
			pinFixtureBinding(t, root, "drift-int", driftSnap, true),
			pinFixtureBinding(t, root, "gone-int", goneSnap, true),
		},
		Skipped: []integrationPinSkipped{{Name: "opt-int", Reason: "not installed"}},
	}
	pinPath := pinFixtureWrite(t, agentDir, pin)
	pinned := runPrimeCapturing(t)

	want := map[string]string{
		"ok-int":    "ok",
		"bad-int":   "check failed at 2026-09-28T01:02:03Z",
		"drift-int": "not bound: content changed (1 files)",
		"gone-int":  "not bound",
		"opt-int":   "skipped (optional): not installed",
	}
	for name, frag := range want {
		lines := primeLinesNaming(pinned, name)
		if len(lines) != 1 {
			t.Errorf("want exactly one prime line naming %s, got %d: %q\n--- output ---\n%s", name, len(lines), lines, pinned)
			continue
		}
		if !strings.Contains(lines[0], frag) {
			t.Errorf("prime line for %s = %q, want it to contain %q", name, lines[0], frag)
		}
	}

	t.Run("no_pin_output_is_unchanged", func(t *testing.T) {
		if err := os.Remove(pinPath); err != nil {
			t.Fatal(err)
		}
		if again := runPrimeCapturing(t); again != noPin {
			t.Errorf("plain prime without a pin changed between runs:\n--- first ---\n%s\n--- again ---\n%s", noPin, again)
		}
		for name := range want {
			if strings.Contains(noPin, name) {
				t.Errorf("plain prime without a pin must print no integration line; found %s", name)
			}
		}
	})

	// Last: a hook run persists its session id, which changes the plain identity line.
	t.Run("hook_mode_prints_no_integration_lines", func(t *testing.T) {
		pinFixtureWrite(t, agentDir, pin)
		hook := primeHookCapturing(t, `{"session_id":"sess-hook","source":"startup"}`)
		for name := range want {
			if strings.Contains(hook, name) {
				t.Errorf("af prime --hook must not carry integration lines (D14); found %s in:\n%s", name, hook)
			}
		}
	})
}

// After a re-install the check record describes the new content; it must not be reported as the state of the
// older snapshot this session's pin still binds.
func TestPrime_IntegrationLineIgnoresAnotherSnapshotsRecord(t *testing.T) {
	e := intBFactory(t)
	root := e.root
	agentDir := config.AgentDir(root, "supervisor")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installMemStore(t)
	t.Setenv("AF_ROLE", "")
	t.Chdir(agentDir)

	snap := intBRecord(t, e, "re-int", intBSource(e, "re-int", intBManifestOpts{noCheck: true}), nil)
	pb := pinFixtureBinding(t, root, "re-int", snap, true)
	pinFixtureWrite(t, agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{pb}})
	at := time.Date(2026, 9, 28, 1, 2, 3, 0, time.UTC)

	admWriteCheckRecordState(t, root, "re-int", "fail", strings.Repeat("e", 64), at)
	if lines := primeLinesNaming(runPrimeCapturing(t), "re-int"); len(lines) != 1 || strings.Contains(lines[0], "check failed") || !strings.Contains(lines[0], "ok") {
		t.Errorf("a failing record for other content must not be shown as the pinned snapshot's state; lines %q", lines)
	}

	admWriteCheckRecordState(t, root, "re-int", "fail", pb.ContentSHA256, at)
	if lines := primeLinesNaming(runPrimeCapturing(t), "re-int"); len(lines) != 1 || !strings.Contains(lines[0], "check failed at 2026-09-28T01:02:03Z") {
		t.Errorf("a failing record for the pinned snapshot must be shown; lines %q", lines)
	}
}
