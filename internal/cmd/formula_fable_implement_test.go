package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

// argvLedger reads the records a stub wrote with `printf '%s\0' "$@"; printf '\036'`: one argv per
// record, so arguments holding spaces (titles, bodies) survive intact.
func argvLedger(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out [][]string
	for _, rec := range strings.Split(string(data), "\x1e") {
		if rec == "" {
			continue
		}
		out = append(out, strings.Split(strings.TrimSuffix(rec, "\x00"), "\x00"))
	}
	return out
}

func ledgerHas(calls [][]string, prefix ...string) bool {
	for _, c := range calls {
		if len(c) >= len(prefix) && slices.Equal(c[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// The MODE=issue block of submit-and-exit is the only place a healing PR gets the closing reference
// that lets the operator's merge close the issue; these pins keep it, and keep it scoped to issue input.
func TestFableImplementClosingReference(t *testing.T) {
	const step = "submit-and-exit"
	copies := map[string]string{
		"source":    "install_formulas/fable-implement.formula.toml",
		"installed": "../../.agentfactory/store/formulas/fable-implement.formula.toml",
	}
	issueBlock := func(t *testing.T, path string) (desc, issue, pr string) {
		t.Helper()
		f, err := formula.ParseFile(path)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, s := range f.Steps {
			if s.ID == step {
				desc = s.Description
			}
		}
		if desc == "" {
			t.Fatalf("%s has no %s step", path, step)
		}
		for _, b := range fencedBlocks(desc) {
			switch {
			case strings.Contains(b.body, "gh pr create"):
				issue = b.body
			case strings.Contains(b.body, "gh pr comment"):
				pr = b.body
			}
		}
		if issue == "" || pr == "" {
			t.Fatalf("%s %s: cannot find both the MODE=issue (gh pr create) and MODE=pr (gh pr comment) blocks", path, step)
		}
		return desc, issue, pr
	}

	for label, path := range copies {
		t.Run(label+" copy", func(t *testing.T) {
			desc, issue, pr := issueBlock(t, path)
			for _, want := range []string{
				"Resolves $ISSUE_REPO#$ISSUE_NUM ({{issue}}).", "Resolves {{issue}}.", "af bead show {{issue}} --json",
				"gh pr view", "--json body", "exit 1", "gh repo view", "visibility", "PUBLIC", "gh pr lock",
			} {
				if !strings.Contains(issue, want) {
					t.Errorf("%s's MODE=issue block lacks %q", step, want)
				}
			}
			if strings.Contains(pr, "lock") {
				t.Errorf("%s's MODE=pr block locks the PR; only an issue-link PR is locked:\n%s", step, pr)
			}
			if len(desc) >= 4096 {
				t.Errorf("%s's description is %d bytes; the fidelity gate reads only the first 4096", step, len(desc))
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "closedByPullRequestsReferences") {
				t.Error("fable-implement looks up linked PRs; a healing run opens its own PR and never reuses another")
			}
		})
	}

	t.Run("the self-check and the lock sit inside the issue-link guard", func(t *testing.T) {
		_, issue, _ := issueBlock(t, copies["source"])
		create := strings.Index(issue, "gh pr create")
		guard := regexp.MustCompile(`(?m)^if \[ -n "\$ISSUE_URL" \]; then$`).FindStringIndex(issue[create:])
		if guard == nil {
			t.Fatalf("no `if [ -n \"$ISSUE_URL\" ]; then` line follows gh pr create:\n%s", issue)
		}
		body := issue[create+guard[1]:]
		end := regexp.MustCompile(`(?m)^fi$`).FindStringIndex(body)
		if end == nil {
			t.Fatalf("the issue-link guard after gh pr create is never closed:\n%s", issue)
		}
		inside, after := body[:end[0]], body[end[1]:]
		for _, want := range []string{"gh pr view", "--json body", "exit 1", "gh repo view", "gh pr lock"} {
			if !strings.Contains(inside, want) {
				t.Errorf("%q is not inside the issue-link guard", want)
			}
			if strings.Contains(after, want) {
				t.Errorf("%q also runs after the issue-link guard", want)
			}
		}
	})

	t.Run("the rendered block", func(t *testing.T) {
		_, issue, _ := issueBlock(t, copies["source"])
		rendered := formula.ExpandTemplateVars(issue, map[string]string{"issue": "af-t1"})
		const ledgers = `printf '%s\0' "$@" >> "$STUB_DIR/$(basename "$0").ledger"; printf '\036' >> "$STUB_DIR/$(basename "$0").ledger"` + "\n"
		fake := refFakeBin(t, map[string]string{
			"af": ledgers + `case "$1 $2" in
  "bead show")
    [ -z "${AF_SHOW_FAIL:-}" ] || { echo "af: issue store unavailable" >&2; exit 1; }
    if [ -n "${AF_SHOW_JSON:-}" ]; then printf '%s\n' "$AF_SHOW_JSON"; else jq -cn --arg d "$AF_DESC" '{id:"af-t1", description:$d}'; fi ;;
  "mail send") ;;
  *) echo "UNROUTED af $*" >&2; exit 2 ;;
esac
`,
			"gh": ledgers + `case "$1 $2" in
  "pr create")
    prev=""; for a in "$@"; do [ "$prev" = --body ] && printf '%s' "$a" > "$STUB_DIR/body"; prev=$a; done
    printf '%s\n' "$PR_CREATE_URL" ;;
  "pr view")
    if [ -n "${PR_VIEW_OVERRIDE:-}" ]; then printf '%s\n' "$PR_VIEW_OVERRIDE"; else cat "$STUB_DIR/body"; echo; fi ;;
  "repo view") printf '%s\n' "$VIS" ;;
  "pr lock") [ -z "${LOCK_FAIL:-}" ] || { echo "gh: lock failed" >&2; exit 1; } ;;
  *) echo "UNROUTED gh $*" >&2; exit 2 ;;
esac
`,
		})
		const prURL = "https://github.com/stempeck/agentfactory/pull/5"
		type run struct {
			res    refRunResult
			gh, af [][]string
			line1  string
		}
		attempt := func(t *testing.T, env ...string) run {
			t.Helper()
			dir := t.TempDir()
			base := []string{"STUB_DIR=" + dir, "PR_CREATE_URL=" + prURL, "VIS=PUBLIC", "AF_DESC="}
			res := refRun(t, dir, refEnv(dir, fake, append(base, env...)...), "", "bash", "-c", rendered)
			body, _ := os.ReadFile(filepath.Join(dir, "body"))
			r := run{res: res, gh: argvLedger(t, filepath.Join(dir, "gh.ledger")), af: argvLedger(t, filepath.Join(dir, "af.ledger")),
				line1: strings.SplitN(string(body), "\n", 2)[0]}
			for _, c := range append(r.gh, r.af...) {
				if strings.Contains(strings.Join(c, " "), "UNROUTED") {
					t.Errorf("an unrouted stub call: %q", c)
				}
			}
			if strings.Contains(res.stderr, "UNROUTED") {
				t.Errorf("an unrouted stub call:\n%s", res.stderr)
			}
			return r
		}
		drive := func(t *testing.T, env ...string) run {
			t.Helper()
			r := attempt(t, env...)
			if !ledgerHas(r.gh, "pr", "create") {
				t.Fatalf("the block never reached gh pr create (exit %d)\nstdout:\n%s\nstderr:\n%s", r.res.exit, r.res.stdout, r.res.stderr)
			}
			return r
		}

		t.Run("a pro issue link resolves on agentfactory-pro", func(t *testing.T) {
			r := drive(t, "AF_DESC=Heal https://github.com/stempeck/agentfactory-pro/issues/9 please")
			if want := "Resolves stempeck/agentfactory-pro#9 (af-t1)."; r.res.exit != 0 || r.line1 != want {
				t.Errorf("exit %d, PR body line 1 = %q; want 0 and %q (a heal merge closes its issue in pro)", r.res.exit, r.line1, want)
			}
		})

		t.Run("an issue link closes the issue and locks the public PR", func(t *testing.T) {
			r := drive(t, "AF_DESC=Heal https://github.com/stempeck/agentfactory/issues/9 please")
			if r.res.exit != 0 {
				t.Fatalf("exit = %d, want 0\nstderr:\n%s", r.res.exit, r.res.stderr)
			}
			if want := "Resolves stempeck/agentfactory#9 (af-t1)."; r.line1 != want {
				t.Errorf("PR body line 1 = %q, want %q", r.line1, want)
			}
			if !ledgerHas(r.gh, "pr", "view", prURL) {
				t.Errorf("the block never re-read the created PR's body: %q", r.gh)
			}
			if !ledgerHas(r.gh, "repo", "view", "stempeck/agentfactory") {
				t.Errorf("the block never read the PR repository's visibility: %q", r.gh)
			}
			if !ledgerHas(r.gh, "pr", "lock", prURL) {
				t.Errorf("a public issue-link PR was not locked: %q", r.gh)
			}
			if ledgerHas(r.af, "mail", "send") {
				t.Errorf("a matching body sent mail: %q", r.af)
			}
		})

		t.Run("without an issue link the step does what it did before", func(t *testing.T) {
			r := drive(t, "AF_DESC=Implement the thing described inline.")
			if r.res.exit != 0 {
				t.Fatalf("exit = %d, want 0\nstderr:\n%s", r.res.exit, r.res.stderr)
			}
			if want := "Resolves af-t1."; r.line1 != want {
				t.Errorf("PR body line 1 = %q, want %q", r.line1, want)
			}
			if len(r.gh) != 1 {
				t.Errorf("gh calls = %q, want only pr create", r.gh)
			}
			if ledgerHas(r.af, "mail", "send") {
				t.Errorf("mail was sent: %q", r.af)
			}
			if !strings.Contains(r.res.stdout, prURL) {
				t.Errorf("the step no longer prints the created PR's URL; stdout:\n%s", r.res.stdout)
			}
		})

		t.Run("a body that lost its closing reference fails and asks for help", func(t *testing.T) {
			r := drive(t, "AF_DESC=https://github.com/stempeck/agentfactory/issues/9", "PR_VIEW_OVERRIDE=Resolves af-t1.")
			if r.res.exit != 1 {
				t.Errorf("exit = %d, want 1", r.res.exit)
			}
			if !ledgerHas(r.af, "mail", "send", "supervisor", "-s", "HELP: PR body lacks closing reference") {
				t.Errorf("no HELP mail to supervisor: %q", r.af)
			}
			if ledgerHas(r.gh, "pr", "lock") {
				t.Errorf("a PR that failed its self-check was locked: %q", r.gh)
			}
		})

		t.Run("a private repository's PR is not locked", func(t *testing.T) {
			r := drive(t, "AF_DESC=https://github.com/stempeck/agentfactory/issues/9", "VIS=PRIVATE")
			if r.res.exit != 0 {
				t.Errorf("exit = %d, want 0 (a private repository is not a failure)\nstderr:\n%s", r.res.exit, r.res.stderr)
			}
			if ledgerHas(r.gh, "pr", "lock") {
				t.Errorf("a private repository's PR was locked: %q", r.gh)
			}
		})

		t.Run("the first issue link wins", func(t *testing.T) {
			r := drive(t, "AF_DESC=https://github.com/stempeck/agentfactory/issues/9 then https://github.com/stempeck/agentfactory/issues/12")
			if want := "Resolves stempeck/agentfactory#9 (af-t1)."; r.line1 != want {
				t.Errorf("PR body line 1 = %q, want %q", r.line1, want)
			}
		})

		t.Run("visibility comes from the PR's repository", func(t *testing.T) {
			r := drive(t, "AF_DESC=https://github.com/upstream/project/issues/3")
			if want := "Resolves upstream/project#3 (af-t1)."; r.line1 != want {
				t.Errorf("PR body line 1 = %q, want %q", r.line1, want)
			}
			if !ledgerHas(r.gh, "repo", "view", "stempeck/agentfactory") {
				t.Errorf("visibility was not read from the PR's repository stempeck/agentfactory: %q", r.gh)
			}
		})

		t.Run("an issue link the step cannot read fails before any PR is opened", func(t *testing.T) {
			for name, env := range map[string][]string{
				"the bead read fails":                   {"AF_SHOW_FAIL=1", "AF_DESC=https://github.com/stempeck/agentfactory/issues/9"},
				"the bead has no description":           {`AF_SHOW_JSON={"id":"af-t1"}`},
				"the bead answer is not JSON":           {"AF_SHOW_JSON=issue store unavailable"},
				"a link without its scheme":             {"AF_DESC=Heal github.com/stempeck/agentfactory/issues/9 please"},
				"a link the strict pattern cannot read": {"AF_DESC=Heal http://github.com/stempeck/agentfactory/issues/9 please"},
			} {
				t.Run(name, func(t *testing.T) {
					r := attempt(t, env...)
					if r.res.exit != 1 {
						t.Errorf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", r.res.exit, r.res.stdout, r.res.stderr)
					}
					if ledgerHas(r.gh, "pr", "create") {
						t.Errorf("a PR was opened although the step could not prove its closing reference (line 1 %q): %q", r.line1, r.gh)
					}
					sent := false
					for _, c := range r.af {
						if len(c) > 4 && c[0] == "mail" && c[1] == "send" && c[2] == "supervisor" && c[3] == "-s" && strings.HasPrefix(c[4], "HELP: ") {
							sent = true
						}
					}
					if !sent {
						t.Errorf("no HELP mail to supervisor: %q", r.af)
					}
				})
			}
		})

		t.Run("a failed lock asks for help and fails", func(t *testing.T) {
			r := drive(t, "AF_DESC=https://github.com/stempeck/agentfactory/issues/9", "LOCK_FAIL=1")
			if r.res.exit != 1 {
				t.Errorf("exit = %d, want 1", r.res.exit)
			}
			if !ledgerHas(r.af, "mail", "send", "supervisor", "-s", "HELP: PR lock failed") {
				t.Errorf("no HELP mail to supervisor after the lock failed: %q", r.af)
			}
		})

		t.Run("an empty PR URL fails before any PR is read", func(t *testing.T) {
			r := drive(t, "AF_DESC=https://github.com/stempeck/agentfactory/issues/9", "PR_CREATE_URL=")
			if r.res.exit != 1 {
				t.Errorf("exit = %d, want 1", r.res.exit)
			}
			if ledgerHas(r.gh, "pr", "view") || ledgerHas(r.gh, "pr", "lock") {
				t.Errorf("gh pr view with no URL picks the current branch's PR: %q", r.gh)
			}
			if !ledgerHas(r.af, "mail", "send", "supervisor") {
				t.Errorf("no HELP mail to supervisor: %q", r.af)
			}
		})
	})

	t.Run("the Supervisor does not close an issue the merge closes", func(t *testing.T) {
		for label, path := range copies {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, stale := range []string{"Close your issue after successful merge", "The Supervisor will close it after successful merge"} {
				if strings.Contains(string(raw), stale) {
					t.Errorf("the %s copy still says %q; for an issue link the merge closes the issue through $CLOSES", label, stale)
				}
			}
			desc, _, _ := issueBlock(t, path)
			if model := section(desc, "**Ephemeral Agent Model:**", "**3."); !strings.Contains(model, "$CLOSES") {
				t.Errorf("the %s copy's Supervisor list does not say which issues the merge closes ($CLOSES):\n%s", label, model)
			}
		}
	})

	t.Run("no healing formula merges", func(t *testing.T) {
		needle := "gh pr " + "merge"
		for _, name := range []string{"fable-implement", "fable-review", "fable-increment"} {
			raw, err := os.ReadFile(filepath.Join("install_formulas", name+".formula.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), needle) {
				t.Errorf("%s runs %q; the operator's merge is the acceptance act", name, needle)
			}
		}
	})
}
