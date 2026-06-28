package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This source-lint lives in the server package (not internal/exec) on purpose: it must embed
// the very patterns it forbids as fixtures, and the AC#1 grep gate
//
//	! grep -rnE 'sh -c|exec\.Command\("(sh|bash)"' internal/exec/
//
// scans internal/exec/ recursively (including _test.go). Keeping the fixtures here keeps
// internal/exec/ clean while still running under AC#6
//
//	go test ./internal/exec/ ./internal/server/ -run 'TestRunner_InjectableFake|TestExec_NoLiveTreeMutation'

var forbiddenShell = regexp.MustCompile(`sh -c|exec\.Command\("(sh|bash)"`)

var mutatingExec = regexp.MustCompile(`exec\.Command\("af"[^)]*"(down|sling)"`)

func scanForbidden(t *testing.T, dir string) (shellHits, mutateHits []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // package may not exist yet; skip gracefully
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if e.Name() == "lint_test.go" { // this file holds the fixtures; exclude it
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		src := string(b)
		if forbiddenShell.MatchString(src) {
			shellHits = append(shellHits, filepath.Join(dir, e.Name()))
		}
		if mutatingExec.MatchString(src) {
			mutateHits = append(mutateHits, filepath.Join(dir, e.Name()))
		}
	}
	return shellHits, mutateHits
}

// AC1/AC6 — no shell-string exec, and no real live-tree mutation, anywhere in the web module
// source. Mutations go only through the injectable Runner/fake.
func TestExec_NoLiveTreeMutation(t *testing.T) {
	moduleRoot := filepath.Join("..", "..") // web/internal/server -> web
	dirs := []string{
		filepath.Join(moduleRoot, "internal", "exec"),
		filepath.Join(moduleRoot, "internal", "server"),
		filepath.Join(moduleRoot, "internal", "readmodel"),
		filepath.Join(moduleRoot, "internal", "web"),
		filepath.Join(moduleRoot, "internal", "proto"),    // C7: read-only static serving, no exec
		filepath.Join(moduleRoot, "internal", "feedback"), // C6: file write + read-model, no exec
		filepath.Join(moduleRoot, "cmd", "afweb"),
	}
	for _, d := range dirs {
		shellHits, mutateHits := scanForbidden(t, d)
		if len(shellHits) > 0 {
			t.Errorf("shell-string exec found in %v — use argv arrays only (never a shell)", shellHits)
		}
		if len(mutateHits) > 0 {
			t.Errorf("real mutating af invocation found in %v — mutations must go through the Runner seam", mutateHits)
		}
	}
}

// Self-negative — proves the lint is not vacuous. The fixtures are assembled so the raw source
// here never contains the contiguous forbidden literal (the regexes still match at runtime).
func TestExec_NoLiveTreeMutation_SelfNegative(t *testing.T) {
	shDashC := "sh -" + "c"
	mustFlagShell := []string{
		shDashC,
		"bash -" + "c " + "script",
		`exec.Command(` + `"sh", "-c", payload)`,
	}
	for _, s := range mustFlagShell {
		if !forbiddenShell.MatchString(s) {
			t.Errorf("forbiddenShell failed to flag %q", s)
		}
	}
	mustFlagMutate := []string{
		`exec.Command(` + `"af", "down", name)`,
		`exec.Command(` + `"af", "sling", "--agent", name)`,
	}
	for _, s := range mustFlagMutate {
		if !mutatingExec.MatchString(s) {
			t.Errorf("mutatingExec failed to flag %q", s)
		}
	}
	mustNotFlag := []string{
		`exec.Command(` + `"af", "agents", "list", "--json")`,
		`afArgv("down", name)`,
		`cmd.Stdout = &stdout`,
	}
	for _, s := range mustNotFlag {
		if forbiddenShell.MatchString(s) {
			t.Errorf("forbiddenShell false-positive on %q", s)
		}
		if mutatingExec.MatchString(s) {
			t.Errorf("mutatingExec false-positive on %q", s)
		}
	}
}
