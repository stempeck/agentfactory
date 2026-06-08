package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #309 Phase 3, AC-6 (SEC-3): structural ban on raw DESTRUCTIVE tmux ops
// against production-class session names in the default (untagged) test suite.
//
// The #309 hazard is that `go test ./...` run beside a live factory issues real
// `tmux kill-session`/`new-session` calls that destroy a co-tenant's production
// sessions. This scan fails the build if any untagged test reintroduces such a
// call, so the isolation cannot silently regress.
//
// #317 DEMOTION (defense-in-depth): under issue #317 this source-scan is a
// SECONDARY layer. The PRIMARY control is now behavioral and fail-closed — the
// constructor GUARD (`tmux.NewTmux()` returns a guarded client that panics,
// naming the offending test, on any destructive op against a production identity
// in the default test build) plus the `TMUX_TMPDIR` ISOLATE (every package's
// !integration TestMain redirects the whole process tree to a throwaway tmux
// server). Together they make a production-reaching tmux client unconstructable
// and the operator's real socket unreachable from the default suite, regardless
// of what any test writes. This scan is retained as belt-and-suspenders: it
// fails the build if a future change reintroduces a raw destructive tmux literal
// in an untagged test, before such a regression can rely on the primary
// interlocks. See ADR-018 (primary/secondary framing).
//
// Scope is DESTRUCTIVE-only and matches AC #2's grep exactly:
//   - exec.Command("tmux", "new-session"…) / exec.Command("tmux", "kill-session"…)
//   - the killStaleTmuxSession helper (a real kill-session wrapper)
//   - pkill
// Read-only probes (tmux -V / show-environment / has-session) are NOT flagged.
// Files whose //go:build constraint is enabled by the `integration` tag — the
// bare `integration`, or a compound such as `integration && linux` — are skipped
// (they run only under `make test-integration`, never in the default suite). A
// `!integration` file still runs in the default suite and is scanned.

// destructiveTmuxPattern matches a raw destructive tmux operation. It is kept
// in lockstep with the IMPLREADME AC #2 grep.
var destructiveTmuxPattern = regexp.MustCompile(`exec\.Command\("tmux",\s*"(new-session|kill-session)"|killStaleTmuxSession\(|\bpkill\b`)

// seamReassignPattern matches a test reassigning one of the package-global
// session seams. Such a test mutates global state, so it MUST NOT run with
// t.Parallel (Round-1/Round-2 LOW-3): parallel seam-mutating tests race.
var seamReassignPattern = regexp.MustCompile(`\b(sessionPrefixFn|newManagerTmux|newCmdTmux)\s*=[^=]`)

// integrationTagPattern matches a NON-NEGATED `integration` term inside a
// //go:build constraint expression. The leading alternation `(^|[\s&|(])`
// requires the term to sit at the start of the expression or directly after a
// separator (whitespace, `&`, `|`, or `(`) — never after a `!` — so a
// `//go:build !integration` constraint (whose file RUNS in the default suite)
// is correctly NOT matched, while compound forms such as `integration && linux`
// and `linux || integration` ARE. Go's RE2 has no look-behind, hence the
// explicit preceding-character class rather than a `(?<!!)` assertion. The repo
// uses only plain `integration` / `!integration` / platform tags; a
// parenthesized negation `!(integration)` is out of scope (it would read as
// tagged) and never appears on a `//go:build` line here.
var integrationTagPattern = regexp.MustCompile(`(^|[\s&|(])integration\b`)

// isIntegrationOnlyFile reports whether a Go source file is excluded from the
// default build because its //go:build constraint is enabled by the
// `integration` tag (the codebase's convention for real-tmux/real-environment
// tests). It recognizes ANY expression in which `integration` appears as a
// NON-NEGATED term — the bare `integration`, or a compound such as
// `integration && linux` / `linux || integration`. A `//go:build !integration`
// file still runs in the default suite and is therefore NOT skipped. (#317
// Phase 6 hardened the former exact `== "integration"` match, which wrongly
// treated every compound `integration` constraint as untagged — Gap 8.)
func isIntegrationOnlyFile(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			return false
		}
		if strings.HasPrefix(trimmed, "//go:build") {
			constraint := strings.TrimSpace(strings.TrimPrefix(trimmed, "//go:build"))
			return integrationTagPattern.MatchString(constraint)
		}
	}
	return false
}

// scanTmuxIsolationViolations walks an internal/ tree and returns
// "file:line: reason" findings for raw destructive tmux ops in untagged
// *_test.go files, plus any t.Parallel call inside a seam-mutating file.
// Files in skip (keyed by base name) are not scanned — used to allowlist the
// enforcement and isolation test files themselves, which embed the very
// literals/names they scan for. Factored out so a fail-on-revert subtest can
// run the SAME logic over a planted temp tree (Round-2 HIGH-2 / Round-3 LOW-A).
func scanTmuxIsolationViolations(root string, skip map[string]bool) []string {
	var findings []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if skip[filepath.Base(path)] {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(data)
		if isIntegrationOnlyFile(content) {
			return nil
		}
		seamMutating := seamReassignPattern.MatchString(content)
		for i, line := range strings.Split(content, "\n") {
			if destructiveTmuxPattern.MatchString(line) {
				findings = append(findings, fmt.Sprintf("%s:%d: raw destructive tmux op: %s",
					path, i+1, strings.TrimSpace(line)))
			}
			if seamMutating && strings.Contains(line, "t.Parallel(") {
				findings = append(findings, fmt.Sprintf("%s:%d: t.Parallel in a seam-mutating test (seams are package globals; would race): %s",
					path, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	return findings
}

// skipIsolationSelfFiles allowlists the Phase-3 test files that embed the
// literal patterns / production names they scan for (mirrors the skipFiles
// idiom in enforce_naming_test.go), plus the #317 Phase-2b interlock test.
//
// interlock_test.go is the sanctioned exception: it raw-execs tmux
// new-session/kill-session ON PURPOSE to PROVE the Phase-2b out-of-process
// backstop redirects those very ops to a private throwaway server (set by the
// per-binary TestMain in internal/testsupport/tmuxisolation), never the
// operator's real socket. The #309 hazard the scan guards against — raw tmux
// reaching the real socket from the default suite — is exactly what TestInterlock
// demonstrates is now impossible; its ops are safe by construction and it
// refuses to run at all unless the redirect is active.
var skipIsolationSelfFiles = map[string]bool{
	"tmux_isolation_enforce_test.go": true,
	"tmux_isolation_test.go":         true,
	"interlock_test.go":              true,
	// #317 Phase 5 SENTINEL: raw-execs new-session/kill-session for its
	// uniquely-named production-identity sentinel on the operator's REAL socket
	// (TMUX_TMPDIR=<original>) to PROVE production sessions survive the default
	// suite with zero real ops. Safe by construction (unique name, raw exec only)
	// and fail-closed (refuses to exec unless the Phase 2b redirect is active),
	// exactly like interlock_test.go.
	"session_survival_test.go": true,
}

func TestNoRawDestructiveTmuxInUntaggedTests(t *testing.T) {
	root := findRepoRoot(t)
	internalDir := filepath.Join(root, "internal")

	findings := scanTmuxIsolationViolations(internalDir, skipIsolationSelfFiles)
	if len(findings) > 0 {
		t.Errorf("found %d raw destructive tmux op(s) / seam-mutating t.Parallel in untagged tests.\n"+
			"Migrate each onto setupHermeticSessions(t), or move the genuinely-real-tmux test behind "+
			"//go:build integration. (security.md SEC-3 / #309 AC-6)\nViolations:\n  %s",
			len(findings), strings.Join(findings, "\n  "))
	}

	// Round-2 HIGH-2 / Round-3 LOW-A: prove the scan is NOT a vacuous pass by
	// running the REAL scan function over a temp tree containing a planted
	// untagged violation. This exercises BOTH the regex AND the directory walk.
	t.Run("catches_reintroduced_violation", func(t *testing.T) {
		dir := t.TempDir()
		// Fixture is written as source TEXT under t.TempDir(); it is never
		// compiled — only read+regex-scanned by scanTmuxIsolationViolations.
		fixture := "package planted\n\n" +
			"import (\n\t\"os/exec\"\n\t\"testing\"\n)\n\n" +
			"func killStaleTmuxSession(t *testing.T, name string) {\n" +
			"\t_ = exec.Command(\"tmux\", \"kill-session\", \"-t\", name).Run()\n}\n\n" +
			"func TestPlanted(t *testing.T) {\n" +
			"\tkillStaleTmuxSession(t, \"af-dispatch\")\n" +
			"\t_ = exec.Command(\"tmux\", \"kill-session\", \"-t\", \"af-dispatch\").Run()\n" +
			"\t_ = exec.Command(\"tmux\", \"new-session\", \"-d\", \"-s\", \"af-dispatch\").Run()\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "planted_test.go"), []byte(fixture), 0o644); err != nil {
			t.Fatalf("writing planted fixture: %v", err)
		}
		got := scanTmuxIsolationViolations(dir, nil)
		if len(got) == 0 {
			t.Fatal("enforcement scan returned ZERO findings for a planted raw " +
				"kill-session/new-session/killStaleTmuxSession fixture — the scan is vacuous")
		}
	})

	// Complementary proof: the scan does NOT flag the same violation when the
	// fixture is behind //go:build integration (so it is not a match-everything
	// scan, and the tag-skip actually works).
	t.Run("skips_integration_tagged_violation", func(t *testing.T) {
		dir := t.TempDir()
		fixture := "//go:build integration\n\n" +
			"package planted\n\n" +
			"import (\n\t\"os/exec\"\n\t\"testing\"\n)\n\n" +
			"func TestTaggedAllowed(t *testing.T) {\n" +
			"\t_ = exec.Command(\"tmux\", \"kill-session\", \"-t\", \"af-dispatch\").Run()\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "tagged_test.go"), []byte(fixture), 0o644); err != nil {
			t.Fatalf("writing tagged fixture: %v", err)
		}
		if got := scanTmuxIsolationViolations(dir, nil); len(got) != 0 {
			t.Fatalf("scan flagged an integration-tagged file (should be skipped): %v", got)
		}
	})

	// Gap 8 (#317 Phase 6): the harden's load-bearing proof. A COMPOUND build
	// constraint `//go:build integration && linux` must be recognized as
	// integration-only and skipped — the exact-equality parser this replaces
	// treated any expression other than the bare `integration` literal as
	// untagged and (wrongly) scanned it. This fixture FAILS on the pre-harden
	// parser and PASSES after, so the compound-skip proof is non-vacuous (AC-1).
	t.Run("skips_compound_integration_tagged_violation", func(t *testing.T) {
		dir := t.TempDir()
		fixture := "//go:build integration && linux\n\n" +
			"package planted\n\n" +
			"import (\n\t\"os/exec\"\n\t\"testing\"\n)\n\n" +
			"func TestCompoundTaggedAllowed(t *testing.T) {\n" +
			"\t_ = exec.Command(\"tmux\", \"kill-session\", \"-t\", \"af-dispatch\").Run()\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "compound_tagged_test.go"), []byte(fixture), 0o644); err != nil {
			t.Fatalf("writing compound-tagged fixture: %v", err)
		}
		if got := scanTmuxIsolationViolations(dir, nil); len(got) != 0 {
			t.Fatalf("scan flagged a compound integration-tagged file (should be skipped): %v", got)
		}
	})

	// Negation safety (#317 Phase 6 Gotcha): a `//go:build !integration` file
	// RUNS in the default suite, so a raw destructive tmux op in it MUST still be
	// flagged. This locks the harden against a naive `\bintegration\b` match that
	// would wrongly match the `integration` token inside `!integration` and
	// silently stop scanning every default-suite file.
	t.Run("flags_negated_integration_violation", func(t *testing.T) {
		dir := t.TempDir()
		fixture := "//go:build !integration\n\n" +
			"package planted\n\n" +
			"import (\n\t\"os/exec\"\n\t\"testing\"\n)\n\n" +
			"func TestNegatedScanned(t *testing.T) {\n" +
			"\t_ = exec.Command(\"tmux\", \"kill-session\", \"-t\", \"af-dispatch\").Run()\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "negated_test.go"), []byte(fixture), 0o644); err != nil {
			t.Fatalf("writing negated fixture: %v", err)
		}
		if got := scanTmuxIsolationViolations(dir, nil); len(got) == 0 {
			t.Fatal("scan skipped a //go:build !integration file (it runs in the default " +
				"suite and MUST be scanned for raw destructive tmux ops)")
		}
	})
}
