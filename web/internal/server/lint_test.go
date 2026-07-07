package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This source-lint lives in the server package (not internal/exec) on purpose: it must embed the
// very patterns it forbids as fixtures. It scans the WHOLE web module by WalkDir (the
// extractability_test.go:30-55 precedent), replacing the former hardcoded 7-dir list that missed 5
// of the 11 internal packages and silently skipped a typo'd/absent dir — so a NEW package can no
// longer escape the lint (design-doc L416). Three forbidden classes, each self-negatived:
//   - shell-string exec (argv arrays only, never a shell) — AC#1/AC#6
//   - real mutating af invocation (mutations go only through the injectable Runner) — AC#6
//   - tmux INPUT/interaction primitive (the web tmux surface is READ-only) — AC-3

// forbiddenShell flags a shell interpreter spawned via exec.Command / osexec.CommandContext with
// "sh"/"bash" as the program. The CommandContext spelling is included (peer review Gap 2) so the
// readmodel seam's exact shape can never smuggle a shell.
var forbiddenShell = regexp.MustCompile(`sh -c|Command(Context)?\((ctx, )?"(sh|bash)"`)

// mutatingExec flags a REAL mutating af invocation. `mail` joins down/sling (#500): a rogue direct
// exec.Command("af","mail",…) must be caught (it would otherwise bypass the Runner seam).
var mutatingExec = regexp.MustCompile(`exec\.Command\("af"[^)]*"(down|sling|mail)"`)

// forbiddenTmuxInput flags any tmux INPUT/interaction primitive. Matched in QUOTED-argv context so
// prose and identifiers ("attachment", "// attach …", sendKeys) never false-positive; the READ
// primitives the web tmux surface legitimately uses ("capture-pane", "list-sessions",
// "has-session") are deliberately absent from the set. Verified 2026-07-03: ZERO occurrences
// module-wide, so the pattern starts clean — non-vacuity is proven by the self-negative below.
var forbiddenTmuxInput = regexp.MustCompile(`"(send-keys|paste-buffer|load-buffer|set-buffer|attach-session|attach)"`)

// isExemptFromShellLint reports whether path is the ONE file allowed to carry a shell-exec literal:
// internal/entrypoint/guard_test.go legitimately runs the shipped quickstart.sh bash guard via
// exec.Command("bash", …) to make the IFF-available launch contract CI-visible. The exemption is
// narrow — keyed on the exact path suffix — so no OTHER file inherits it (proven non-broad by the
// self-negative). It scopes ONLY the shell class; the mutating-af and tmux-input classes still apply.
func isExemptFromShellLint(path string) bool {
	return strings.HasSuffix(filepath.ToSlash(path), "internal/entrypoint/guard_test.go")
}

type offense struct {
	path string
	kind string
}

const (
	kindShell     = "shell-string exec"
	kindMutate    = "mutating af invocation"
	kindTmuxInput = "tmux input primitive"
)

// scanTree walks root and flags every .go file (INCLUDING _test.go — build tags don't exempt raw
// bytes) that carries a forbidden pattern. lint_test.go itself is skipped (it holds the fixtures);
// non-source dirs are skipped (the extractability_test.go skip switch). Taking root as a parameter
// lets the planted-file test drive a fresh temp tree through the same code path.
func scanTree(t *testing.T, root string) []offense {
	t.Helper()
	var offenses []offense
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".gotmp", "vendor", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		if d.Name() == "lint_test.go" { // this file holds the fixtures; never scan it
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(b)
		if forbiddenShell.MatchString(src) && !isExemptFromShellLint(path) {
			offenses = append(offenses, offense{path, kindShell})
		}
		if mutatingExec.MatchString(src) {
			offenses = append(offenses, offense{path, kindMutate})
		}
		if forbiddenTmuxInput.MatchString(src) {
			offenses = append(offenses, offense{path, kindTmuxInput})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return offenses
}

func moduleRoot() string { return filepath.Join("..", "..") } // web/internal/server -> web

// AC1/AC6 — no shell-string exec, and no real live-tree mutation, anywhere in the web module
// source. Mutations go only through the injectable Runner/fake.
func TestExec_NoLiveTreeMutation(t *testing.T) {
	for _, o := range scanTree(t, moduleRoot()) {
		switch o.kind {
		case kindShell:
			t.Errorf("shell-string exec found in %s — use argv arrays only (never a shell)", o.path)
		case kindMutate:
			t.Errorf("real mutating af invocation found in %s — mutations must go through the Runner seam", o.path)
		}
	}
}

// TestLint_NoSessionInputPrimitives — AC-3's read-only invariant as a CI property: no tmux INPUT
// primitive may appear anywhere in the web tmux surface. The web console reads panes; it never
// types into them.
func TestLint_NoSessionInputPrimitives(t *testing.T) {
	for _, o := range scanTree(t, moduleRoot()) {
		if o.kind == kindTmuxInput {
			t.Errorf("tmux INPUT primitive found in %s — the web tmux surface is READ-ONLY (AC-3): no send-keys/paste-buffer/load-buffer/set-buffer/attach", o.path)
		}
	}
}

// TestLint_WalkDetectsNewDir — design-doc L416: the module WalkDir (not a hardcoded dir list)
// detects a planted forbidden file in a NEW, never-listed dir. Hermetic: a temp tree with a nested
// package the lint has no knowledge of.
func TestLint_WalkDetectsNewDir(t *testing.T) {
	root := t.TempDir()
	newpkg := filepath.Join(root, "internal", "totally", "newpkg")
	if err := os.MkdirAll(newpkg, 0o755); err != nil {
		t.Fatal(err)
	}
	// Assembled from fragments so THIS file's raw bytes never carry the contiguous forbidden literal.
	planted := "package newpkg\n\nfunc x() { _ = " + `exec.Command(` + `"sh", "-c", "boom") }` + "\n"
	if err := os.WriteFile(filepath.Join(newpkg, "rogue.go"), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range scanTree(t, root) {
		if o.kind == kindShell {
			found = true
		}
	}
	if !found {
		t.Error("module walk failed to detect a planted shell-exec in a NEW dir — a new package could silently escape the lint")
	}
}

// TestLint_EntrypointExemptionIsNarrow proves the shell-lint exemption is scoped to exactly
// entrypoint/guard_test.go and does not leak to any sibling (e.g. a same-basename file elsewhere).
func TestLint_EntrypointExemptionIsNarrow(t *testing.T) {
	if !isExemptFromShellLint(filepath.FromSlash("web/internal/entrypoint/guard_test.go")) {
		t.Error("the legitimate entrypoint/guard_test.go bash-guard test must be exempt")
	}
	for _, p := range []string{
		filepath.FromSlash("web/internal/server/guard_test.go"),
		filepath.FromSlash("web/internal/entrypoint/other_test.go"),
		filepath.FromSlash("web/cmd/afweb/main.go"),
	} {
		if isExemptFromShellLint(p) {
			t.Errorf("exemption wrongly leaked to %q — it must be narrow", p)
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
		`osexec.CommandContext(` + `ctx, "bash", "-c", payload)`, // CommandContext spelling (Gap 2)
	}
	for _, s := range mustFlagShell {
		if !forbiddenShell.MatchString(s) {
			t.Errorf("forbiddenShell failed to flag %q", s)
		}
	}
	mustFlagMutate := []string{
		`exec.Command(` + `"af", "down", name)`,
		`exec.Command(` + `"af", "sling", "--agent", name)`,
		`exec.Command(` + `"af", "mail", "send", name)`, // #500: mail must be caught
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
		`osexec.CommandContext(` + `ctx, "tmux", "list-sessions")`, // a tmux READ is not a shell
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

// TestLint_NoSessionInputPrimitives_SelfNegative proves forbiddenTmuxInput matches every input
// primitive (in quoted-argv context) and never false-positives on the READ primitives or prose.
func TestLint_NoSessionInputPrimitives_SelfNegative(t *testing.T) {
	mustFlag := []string{
		`execCommand(ctx, "tmux", ` + `"send-` + `keys", "-t", s)`,
		`"paste-` + `buffer"`,
		`"load-` + `buffer"`,
		`"set-` + `buffer"`,
		`"` + `attach"`,
		`"attach-` + `session"`,
	}
	for _, s := range mustFlag {
		if !forbiddenTmuxInput.MatchString(s) {
			t.Errorf("forbiddenTmuxInput failed to flag %q", s)
		}
	}
	mustNotFlag := []string{
		`"capture-pane"`,          // the web surface's actual snapshot read
		`"list-sessions"`,         // the liveness/probe read
		`"has-session"`,           // the exact-match membership probe
		`attachment := true`,      // prose/identifier — no quotes around a primitive
		`// attach to the flow`,   // comment
		`var sendKeys = false`,    // camelCase identifier, no hyphen, no quotes
	}
	for _, s := range mustNotFlag {
		if forbiddenTmuxInput.MatchString(s) {
			t.Errorf("forbiddenTmuxInput false-positive on %q", s)
		}
	}
}
