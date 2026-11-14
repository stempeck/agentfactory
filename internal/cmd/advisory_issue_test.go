package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const advisoryIssueRel = ".github/advisory-issue.sh"

// filerGhStub stands in for gh under the filer. It routes on the verb pair, serves canned JSON from
// $CURL_STUB_DIR, records every argv in a NUL ledger, copies each issue body before the filer's EXIT
// trap deletes it, and fails any read or write named in gh.faults.
const filerGhStub = `D="$CURL_STUB_DIR"
: > "$D/gh.reached"
printf '%s\0' "$@" >> "$D/gh.ledger"; printf '\036' >> "$D/gh.ledger"
fault() { grep -qxF "$1" "$D/gh.faults" 2>/dev/null; }
case "$1 $2" in
  "label list")
    fault "label list" && { echo "gh: HTTP 502 (label list)" >&2; exit 1; }
    limit=30; prev=""
    for a in "$@"; do
      case "$a" in --limit=*) limit=${a#--limit=} ;; esac
      { [ "$prev" = --limit ] || [ "$prev" = -L ]; } && limit=$a
      prev=$a
    done
    jq -c --argjson n "$limit" '.[:$n]' "$D/labels.json" ;;
  "issue list")
    fault "issue list" && { echo "gh: HTTP 502 (issue list)" >&2; exit 1; }
    cat "$D/issues.json" ;;
  "issue create")
    fault "issue create" && { echo "gh: create failed" >&2; exit 1; }
    n=$(cat "$D/gh.next" 2>/dev/null || echo 101); echo $((n + 1)) > "$D/gh.next"
    prev=""; for a in "$@"; do case "$prev" in -F|--body-file) cp "$a" "$D/body.$n" ;; esac; prev=$a; done
    [ -f "$D/create.out" ] && { cat "$D/create.out"; exit 0; }
    echo "https://github.com/$GITHUB_REPOSITORY/issues/$n" ;;
  "issue lock")
    fault "issue lock" && { echo "gh: lock failed" >&2; exit 1; }; : ;;
  "api "*)
    fault "api" && { echo "gh: HTTP 502 (api)" >&2; exit 1; }
    n=${2##*/}; cat "$D/locked.$n" 2>/dev/null || echo true ;;
  *) echo "UNROUTED gh $*" >> "$D/calls.log"; exit 2 ;;
esac
`

type filerRun struct {
	res    refRunResult
	calls  [][]string
	dir    string
	bodies map[int]string
}

func (fr filerRun) verb(pair ...string) [][]string {
	var out [][]string
	for _, c := range fr.calls {
		if len(c) >= len(pair) && strings.Join(c[:len(pair)], " ") == strings.Join(pair, " ") {
			out = append(out, c)
		}
	}
	return out
}

func (fr filerRun) onlyBody(t *testing.T) string {
	t.Helper()
	if n := len(fr.verb("issue", "create")); n != 1 || len(fr.bodies) != 1 {
		t.Fatalf("issue create ran %d times with %d bodies captured, want 1\nstdout:\n%s\nstderr:\n%s", n, len(fr.bodies), fr.res.stdout, fr.res.stderr)
	}
	for _, b := range fr.bodies {
		return b
	}
	return ""
}

func (fr filerRun) reachedGh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(fr.dir, "gh.reached")); err != nil || len(fr.calls) == 0 {
		t.Fatalf("the gh stub was never reached (a noexec TMPDIR lets the real gh answer instead): %v\nstderr:\n%s", err, fr.res.stderr)
	}
}

func (fr filerRun) noGh(t *testing.T) {
	t.Helper()
	if len(fr.calls) != 0 {
		t.Errorf("gh was called: %q", fr.calls)
	}
}

// flagValues returns every value given to any of the flags, in order (`-l x`, `--label x`, `--label=x`).
func flagValues(argv []string, flags ...string) []string {
	var out []string
	for i, a := range argv {
		for _, f := range flags {
			if a == f && i+1 < len(argv) {
				out = append(out, argv[i+1])
			} else if strings.HasPrefix(f, "--") && strings.HasPrefix(a, f+"=") {
				out = append(out, strings.TrimPrefix(a, f+"="))
			}
		}
	}
	return out
}

func repoOf(argv []string) string {
	if v := flagValues(argv, "--repo", "-R"); len(v) == 1 {
		return v[0]
	}
	return ""
}

func section(body, from, to string) string {
	i := strings.Index(body, from)
	if i == -1 {
		return ""
	}
	rest := body[i:]
	if j := strings.Index(rest, to); j != -1 {
		return rest[:j]
	}
	return rest
}

// Record builders: schema v1 as the audit writes it.
func filerPinnedIn(locs ...[2]string) []any {
	out := []any{}
	for _, l := range locs {
		out = append(out, map[string]any{"ecosystem": l[0], "repo": l[1]})
	}
	return out
}

func filerRecord(class string, findings ...map[string]any) map[string]any {
	fs := []any{}
	for _, f := range findings {
		fs = append(fs, f)
	}
	return map[string]any{"schema": 1, "today": "2026-11-08", "exit_class": class,
		"counts":   map[string]any{"reported": len(findings), "unexcused": 0, "expired": 0, "node_behind": 0, "excused_fixable": 0, "errors": 0},
		"findings": fs, "errors": []any{}}
}

func oauthlibFinding() map[string]any {
	return map[string]any{"class": "unexcused", "key": "oauthlib@4.0.0", "pinned": true, "fix_published": true,
		"pinned_in": filerPinnedIn([2]string{"PyPI", ""}),
		"advisories": []any{map[string]any{"id": "GHSA-3jp2-3mrg-2vxc", "aliases": []any{"CVE-2026-49272"}, "source": "OSV", "ecosystem": "PyPI",
			"repo": "", "against": "OSV PyPI oauthlib==4.0.0", "fixed": []any{"4.0.1"}, "fixed_raw": ""}},
		"register": []any{map[string]any{"id": "GHSA-3jp2-3mrg-2vxc", "aliases": []any{"CVE-2026-49272"}, "reviewed": "2026-10-27", "recheck_by": "2026-11-26", "key": "oauthlib@3.3.0"}}}
}

func expiredEntry(id, fix string) map[string]any {
	return map[string]any{"id": id, "aliases": []any{}, "reviewed": "2026-10-27", "recheck_by": "2026-11-26", "excuses": []any{id}, "fix_available": fix}
}

func litellmExpiredFinding(entries ...map[string]any) map[string]any {
	if len(entries) == 0 {
		entries = []map[string]any{expiredEntry("GHSA-7hp6-4w63-5g45", "1.100.4")}
	}
	es := []any{}
	for _, e := range entries {
		es = append(es, e)
	}
	return map[string]any{"class": "expired", "key": "litellm@1.93.2", "pinned": true,
		"pinned_in": filerPinnedIn([2]string{"PyPI", ""}, [2]string{"GitHub", "BerriAI/litellm"}), "entries": es, "excused_fixable": []any{}}
}

func openobserveExpiredFinding(entries ...map[string]any) map[string]any {
	es := []any{}
	for _, e := range entries {
		es = append(es, e)
	}
	return map[string]any{"class": "expired", "key": "openobserve@v1.1.2", "pinned": true,
		"pinned_in": filerPinnedIn([2]string{"GitHub", "openobserve/openobserve"}), "entries": es, "excused_fixable": []any{}}
}

// excusedFixable folds the excused-fixable rows of the same key into an expired finding, as the audit does.
func excusedFixable(f map[string]any, rows ...[3]string) map[string]any {
	out := []any{}
	for _, r := range rows {
		out = append(out, map[string]any{"id": r[0], "advisory": r[1], "fixed": []any{r[2]}})
	}
	f["excused_fixable"] = out
	return f
}

func vendorUnexcusedFinding(key, id, repo string, fixed []any, raw string, published bool) map[string]any {
	return map[string]any{"class": "unexcused", "key": key, "pinned": true, "fix_published": published,
		"pinned_in": filerPinnedIn([2]string{"GitHub", repo}),
		"advisories": []any{map[string]any{"id": id, "aliases": []any{}, "source": "GitHub", "ecosystem": "GitHub", "repo": repo,
			"against": "GitHub " + repo + " " + key[strings.LastIndex(key, "@")+1:] + " (unparseable)", "fixed": fixed, "fixed_raw": raw}},
		"register": []any{}}
}

func nodeBehindFinding() map[string]any {
	return map[string]any{"class": "node-behind", "key": "nodejs@v22.23.4", "pinned": true,
		"pinned_in": filerPinnedIn([2]string{"apt", ""}), "releases": []any{"v22.24.0"}}
}

func openobserveExcusedFixableFinding() map[string]any {
	return map[string]any{"class": "excused-fixable", "key": "openobserve@v1.1.2", "pinned": true,
		"pinned_in": filerPinnedIn([2]string{"GitHub", "openobserve/openobserve"}),
		"entries":   []any{map[string]any{"id": "GHSA-rw8w-37p9-mrrp", "advisory": "GHSA-rw8w-37p9-mrrp", "fixed": []any{"v1.2.0"}}}}
}

// pinFinding is an unexcused finding of one advisory-database package at version, as the audit writes
// it for a quickstart constant: GO_TOOLCHAIN_VERSION gives Go stdlib and toolchain, PLAYWRIGHT_VERSION
// gives npm playwright and playwright-core.
func pinFinding(ecosystem, name, version string, ids ...string) map[string]any {
	advs := []any{}
	for _, id := range ids {
		advs = append(advs, map[string]any{"id": id, "aliases": []any{}, "source": "OSV", "ecosystem": ecosystem, "repo": "",
			"against": "OSV " + ecosystem + " " + name + "==" + version, "fixed": []any{"1.26.11", "1.27.4"}, "fixed_raw": ""})
	}
	return map[string]any{"class": "unexcused", "key": name + "@" + version, "pinned": true, "fix_published": true,
		"pinned_in": filerPinnedIn([2]string{ecosystem, ""}), "advisories": advs, "register": []any{}}
}

func litellmExcusedFixableFinding() map[string]any {
	return map[string]any{"class": "excused-fixable", "key": "litellm@1.93.2", "pinned": true,
		"pinned_in": filerPinnedIn([2]string{"PyPI", ""}, [2]string{"GitHub", "BerriAI/litellm"}),
		"entries":   []any{map[string]any{"id": "GHSA-g5ff-637f-6q2m", "advisory": "GHSA-g5ff-637f-6q2m", "fixed": []any{"1.95.0"}}}}
}

const (
	pinMoveText   = "not an acceptable resolution"
	noFixText     = "no fixed version above the pin is published"
	expiredNoFix  = "no advisory these entries excuse has a published fix above the pin"
	operatorText  = "Operator acceptance required:"
	filerRepo     = "stempeck/agentfactory-pro"
	filerRunURL   = "https://github.com/" + filerRepo + "/actions/runs/4242"
	defaultLabels = `[{"name":"agentic"},{"name":"advisory-heal"}]`
)

// The filer turns the audit's finding record into one locked healing issue per pin a pull request
// changes (#755). It reads the record and nothing else; these subtests drive the shipped script against
// a gh stub, as agentfactory-pro, where the loop files.
func TestAdvisoryIssueFiler(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH; the filer is a bash script")
	}
	// macOS ships bash 3.2 at /bin/bash, the interpreter the filer must stay compatible with.
	if _, err := os.Stat("/bin/bash"); err == nil {
		bashPath = "/bin/bash"
	}
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH; the filer reads the record with jq")
	}
	root := findModuleRoot(t)
	filer := filepath.Join(root, advisoryIssueRel)

	withJq := func(t *testing.T, scripts map[string]string) string {
		t.Helper()
		fake := refFakeBin(t, scripts)
		// refEnv's PATH only reaches /usr/bin and /bin; a Homebrew jq lives elsewhere.
		if err := os.Symlink(jqPath, filepath.Join(fake, "jq")); err != nil {
			t.Fatal(err)
		}
		return fake
	}
	fake := withJq(t, map[string]string{"gh": filerGhStub, "curl": auditTripwire("curl"), "wget": auditTripwire("wget"), "python3": auditTripwire("python3")})

	// file runs the filer over record (written to the stub dir as JSON; a string is written verbatim).
	file := func(t *testing.T, record any, setup func(dir string), env ...string) filerRun {
		t.Helper()
		dir := t.TempDir()
		write := func(name, body string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("labels.json", defaultLabels)
		write("issues.json", "[]")
		if record != nil {
			b, ok := record.(string)
			if !ok {
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				b = string(raw)
			}
			write("record.json", b)
		}
		if setup != nil {
			setup(dir)
		}
		home := t.TempDir()
		base := []string{"CURL_STUB_DIR=" + dir, "GITHUB_REPOSITORY=" + filerRepo, "GITHUB_SERVER_URL=https://github.com", "GITHUB_RUN_ID=4242",
			"ADVISORY_AUDIT_RECORD=" + filepath.Join(dir, "record.json"), "ADVISORY_ISSUE_LABELS=agentic,advisory-heal", "ADVISORY_DEDUPE_LABEL=advisory-heal"}
		res := refRun(t, t.TempDir(), refEnv(home, fake, append(base, env...)...), "", bashPath, filer)
		fr := filerRun{res: res, calls: argvLedger(t, filepath.Join(dir, "gh.ledger")), dir: dir, bodies: map[int]string{}}
		matches, _ := filepath.Glob(filepath.Join(dir, "body.*"))
		for _, m := range matches {
			n, _ := strconv.Atoi(strings.TrimPrefix(filepath.Ext(m), "."))
			b, _ := os.ReadFile(m)
			fr.bodies[n] = string(b)
		}
		calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
		if strings.Contains(string(calls), "TRIPWIRE") || strings.Contains(string(calls), "UNROUTED") {
			t.Errorf("the filer made a call outside the modelled gh surface:\n%s", calls)
		}
		return fr
	}
	wantExit := func(t *testing.T, fr filerRun, want int) {
		t.Helper()
		if fr.res.exit != want {
			t.Errorf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", fr.res.exit, want, fr.res.stdout, fr.res.stderr)
		}
	}
	valid := func() map[string]any { return filerRecord("finding", oauthlibFinding()) }
	positiveControl := func(t *testing.T) {
		t.Helper()
		fr := file(t, valid(), nil)
		fr.reachedGh(t)
	}

	t.Run("static", func(t *testing.T) {
		info, err := os.Stat(filer)
		if err != nil {
			t.Fatalf("stat %s: %v", advisoryIssueRel, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("%s must be executable", advisoryIssueRel)
		}
		src := readRepoFile(t, advisoryIssueRel)
		for desc, re := range map[string]string{
			"an uncommented self-hosted (the OSS publish guard scans .github)": `(?m)^[^#\n]*self-hosted`,
			"a pip install line (the CI pip lint demands require-hashes)":      `pip.*install`,
			"ubuntu-latest (hosted runners are not used)":                      `ubuntu-latest`,
			"a bash >= 4 construct":                                            `declare -A|mapfile|readarray|\$\{[A-Za-z_]+,,\}|\[\[ -v|EPOCHSECONDS|(^|[;&| ])timeout `,
		} {
			if regexp.MustCompile(re).MatchString(src) {
				t.Errorf("%s contains %s", advisoryIssueRel, desc)
			}
		}
		// Built, not written: the audit's own text appears in no file but the audit and its test.
		for _, needle := range []string{strings.ToUpper("unexcused"), "advisory audit" + ":"} {
			if strings.Contains(src, needle) {
				t.Errorf("%s contains %q: the filer reads the record, never the audit's printed text", advisoryIssueRel, needle)
			}
		}
		// The audit alone derives a fix (CP-01): the filer orders and compares no versions.
		for _, banned := range []string{"sort -V", "vcmp", "vparse", "version_gt"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s contains %q: the filer compares no versions; the audit's record names the fix", advisoryIssueRel, banned)
			}
		}
		if strings.Contains(src, "advisory-heal") {
			t.Errorf("%s names the label advisory-heal; the dedupe label has one home, ADVISORY_DEDUPE_LABEL in the workflow", advisoryIssueRel)
		}
		// bash 3.2 treats "${a[@]}" of an empty array as unbound under set -u.
		for _, m := range regexp.MustCompile(`"\$\{([A-Za-z_][A-Za-z0-9_]*)\[@\]\}"`).FindAllStringSubmatch(src, -1) {
			bare, guarded := `"${`+m[1]+`[@]}"`, `${`+m[1]+`[@]+"${`+m[1]+`[@]}"}`
			if strings.Count(src, bare) != strings.Count(src, guarded) {
				t.Errorf("%s expands %s outside the bash 3.2 guard %s", advisoryIssueRel, bare, guarded)
			}
		}
	})

	t.Run("gh stub is reached", func(t *testing.T) {
		fr := file(t, valid(), nil)
		fr.reachedGh(t)
		if first := fr.calls[0]; len(first) < 2 || first[0] != "label" || first[1] != "list" {
			t.Errorf("first gh call = %q, want label list", first)
		}
	})

	t.Run("files one locked issue per pin with both labels and the four fields", func(t *testing.T) {
		rec := filerRecord("finding", oauthlibFinding(), litellmExpiredFinding())
		rec["errors"] = []any{"osv querybatch: HTTP 503"}
		fr := file(t, rec, nil)
		wantExit(t, fr, 0)
		fr.reachedGh(t)
		for _, c := range fr.calls {
			if c[0] != "api" && repoOf(c) != filerRepo {
				t.Errorf("gh %q does not pass --repo %s (the step's cwd is not a checkout of it)", c, filerRepo)
			}
		}
		creates := fr.verb("issue", "create")
		if len(creates) != 2 {
			t.Fatalf("issue create ran %d times, want 2 (one per pin): %q", len(creates), fr.calls)
		}
		for i, want := range []string{"advisory-decay: unexcused oauthlib@4.0.0", "advisory-decay: expired litellm@1.93.2"} {
			if got := flagValues(creates[i], "-t", "--title"); len(got) != 1 || got[0] != want {
				t.Errorf("create %d title = %q, want %q", i, got, want)
			}
			if got := flagValues(creates[i], "-l", "--label"); strings.Join(got, ",") != "agentic,advisory-heal" {
				t.Errorf("create %d labels = %q, want [agentic advisory-heal], one flag each", i, got)
			}
		}
		lastCreate := -1
		for i, c := range fr.calls {
			switch {
			case len(c) > 1 && c[0] == "issue" && c[1] == "create":
				lastCreate = i
			case len(c) > 2 && c[0] == "issue" && c[1] == "lock":
				if lastCreate == -1 {
					t.Errorf("issue lock %q ran before any create", c)
				}
			}
		}
		for _, n := range []int{101, 102} {
			found := false
			for _, c := range fr.verb("issue", "lock") {
				found = found || c[2] == strconv.Itoa(n) || c[2] == fmt.Sprintf("https://github.com/%s/issues/%d", filerRepo, n)
			}
			if !found {
				t.Errorf("issue #%d was filed but never locked: %q", n, fr.verb("issue", "lock"))
			}
			if !strings.Contains(fr.res.stdout, fmt.Sprintf("advisory issue: filed #%d https://github.com/%s/issues/%d", n, filerRepo, n)) {
				t.Errorf("stdout does not report filing #%d:\n%s", n, fr.res.stdout)
			}
		}
		oauth, expired := fr.bodies[101], fr.bodies[102]
		for _, want := range []string{"unexcused", "`oauthlib@4.0.0`", "py/", "4.0.1", "GHSA-3jp2-3mrg-2vxc", "CVE-2026-49272", "`oauthlib@3.3.0`",
			"<!-- advisory-decay-key: unexcused oauthlib@4.0.0 -->", filerRunURL, "2026-11-08", "`litellm@1.93.2`", pinMoveText,
			"Resolves " + filerRepo + "#", "first line begins with `Resolves " + filerRepo + "#N`", "ADVISORY_AUDIT_RECORD"} {
			if !strings.Contains(oauth, want) {
				t.Errorf("the oauthlib body lacks %q:\n%s", want, oauth)
			}
		}
		for _, banned := range []string{"oauthlib==4.0.0", "HTTP 503"} {
			if strings.Contains(oauth, banned) {
				t.Errorf("the oauthlib body renders %q (the against text and error messages are never rendered)", banned)
			}
		}
		for _, want := range []string{"expired", "`litellm@1.93.2`", "GHSA-7hp6-4w63-5g45", "2026-11-26", "<!-- advisory-decay-key: expired litellm@1.93.2 -->", "`oauthlib@4.0.0`"} {
			if !strings.Contains(expired, want) {
				t.Errorf("the expired body lacks %q:\n%s", want, expired)
			}
		}

		t.Run("labels come from ADVISORY_ISSUE_LABELS", func(t *testing.T) {
			fr := file(t, valid(), func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, "labels.json"), []byte(`[{"name":"agentic"},{"name":"advisory-heal"},{"name":"triage"}]`), 0o644)
			}, "ADVISORY_ISSUE_LABELS=agentic,advisory-heal,triage")
			wantExit(t, fr, 0)
			for _, c := range fr.verb("issue", "create") {
				if got := flagValues(c, "-l", "--label"); strings.Join(got, ",") != "agentic,advisory-heal,triage" {
					t.Errorf("create labels = %q, want every label the variable lists", got)
				}
			}
			lists := fr.verb("issue", "list")
			if len(lists) == 0 {
				t.Fatal("no dedupe query ran")
			}
			for _, c := range lists {
				if got := flagValues(c, "--label", "-l"); len(got) != 1 || got[0] != "advisory-heal" {
					t.Errorf("dedupe query labels = %q, want advisory-heal alone (the dispatcher strips agentic from a picked-up issue)", got)
				}
			}
		})

		t.Run("the dedupe label comes from ADVISORY_DEDUPE_LABEL", func(t *testing.T) {
			fr := file(t, valid(), func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, "labels.json"), []byte(`[{"name":"agentic"},{"name":"heal-x"}]`), 0o644)
			}, "ADVISORY_ISSUE_LABELS=agentic,heal-x", "ADVISORY_DEDUPE_LABEL=heal-x")
			wantExit(t, fr, 0)
			lists := fr.verb("issue", "list")
			if len(lists) == 0 {
				t.Fatal("no dedupe query ran")
			}
			for _, c := range lists {
				if got := flagValues(c, "--label", "-l"); len(got) != 1 || got[0] != "heal-x" {
					t.Errorf("dedupe query labels = %q, want heal-x alone, the label ADVISORY_DEDUPE_LABEL names", got)
				}
			}
			for _, c := range fr.verb("issue", "create") {
				if got := flagValues(c, "-l", "--label"); strings.Join(got, ",") != "agentic,heal-x" {
					t.Errorf("create labels = %q, want agentic,heal-x", got)
				}
			}
		})

		t.Run("a dedupe label that is unset or unlisted fails", func(t *testing.T) {
			for name, env := range map[string]string{"unset": "ADVISORY_DEDUPE_LABEL=", "unlisted": "ADVISORY_DEDUPE_LABEL=triage"} {
				t.Run(name, func(t *testing.T) {
					fr := file(t, valid(), nil, env)
					wantExit(t, fr, 1)
					if !strings.Contains(fr.res.stderr, "ERROR: ADVISORY_DEDUPE_LABEL") {
						t.Errorf("stderr does not fail naming ADVISORY_DEDUPE_LABEL (an issue filed without the dedupe label is filed again on every rerun):\n%s", fr.res.stderr)
					}
					fr.noGh(t)
				})
			}
			positiveControl(t)
		})

		t.Run("every class has a template", func(t *testing.T) {
			fr := file(t, filerRecord("finding", oauthlibFinding(), litellmExpiredFinding(), nodeBehindFinding(), openobserveExcusedFixableFinding()), nil)
			wantExit(t, fr, 0)
			if n := len(fr.verb("issue", "create")); n != 4 {
				t.Fatalf("issue create ran %d times, want 4 (four pins)", n)
			}
			for _, marker := range []string{"unexcused oauthlib@4.0.0", "expired litellm@1.93.2", "node-behind nodejs@v22.23.4", "excused-fixable openobserve@v1.1.2"} {
				found := false
				for _, b := range fr.bodies {
					found = found || strings.Contains(b, "<!-- advisory-decay-key: "+marker+" -->")
				}
				if !found {
					t.Errorf("no filed body carries the marker for %s", marker)
				}
			}
			for _, b := range fr.bodies {
				if strings.Contains(b, "node-behind nodejs@v22.23.4 -->") && !strings.Contains(b, "v22.24.0") {
					t.Errorf("the node-behind body does not name the newer security release:\n%s", b)
				}
			}
		})
	})

	openIssue := func(number int, title, body string, locked bool) func(dir string) {
		return func(dir string) {
			b, _ := json.Marshal([]map[string]any{{"number": number, "title": title, "body": body}})
			_ = os.WriteFile(filepath.Join(dir, "issues.json"), b, 0o644)
			_ = os.WriteFile(filepath.Join(dir, "locked."+strconv.Itoa(number)), []byte(strconv.FormatBool(locked)+"\n"), 0o644)
		}
	}
	const oauthTitle = "advisory-decay: unexcused oauthlib@4.0.0"
	const oauthMarker = "<!-- advisory-decay-key: unexcused oauthlib@4.0.0 -->"

	t.Run("rerun files nothing", func(t *testing.T) {
		fr := file(t, valid(), openIssue(7, oauthTitle, "Healing issue.\n"+oauthMarker+"\n", true))
		wantExit(t, fr, 0)
		fr.reachedGh(t)
		if n := len(fr.verb("issue", "create")); n != 0 {
			t.Errorf("issue create ran %d times on a rerun", n)
		}
		if !strings.Contains(fr.res.stdout, `advisory issue: "`+oauthTitle+`" already open as #7; not filing`) {
			t.Errorf("stdout does not report the open issue:\n%s", fr.res.stdout)
		}
		api := fr.verb("api", "repos/"+filerRepo+"/issues/7")
		if len(api) != 1 || !strings.Contains(strings.Join(api[0], " "), ".locked") {
			t.Errorf("the lock state of #7 was not read once through the API: %q", fr.calls)
		}
		for _, c := range api {
			if len(flagValues(c, "-X", "--method")) != 0 {
				t.Errorf("the .locked read is not a GET: %q", c)
			}
		}
		if n := len(fr.verb("issue", "lock")); n != 0 {
			t.Errorf("a locked issue was locked again (%d calls)", n)
		}
		for _, c := range fr.verb("issue", "list") {
			s := strings.Join(c, " ")
			for _, want := range []string{"--state open", "advisory-heal", "number", "title", "body"} {
				if !strings.Contains(s, want) {
					t.Errorf("dedupe query %q lacks %q", c, want)
				}
			}
		}
		t.Run("exact title matches without a marker", func(t *testing.T) {
			fr := file(t, valid(), openIssue(7, oauthTitle, "Filed by hand.\n", true))
			wantExit(t, fr, 0)
			fr.reachedGh(t)
			if n := len(fr.verb("issue", "create")); n != 0 {
				t.Errorf("issue create ran %d times although the exact title is open", n)
			}
		})
		t.Run("same key, other class is a hit on the same pin", func(t *testing.T) {
			fr := file(t, valid(), openIssue(7, "advisory-decay: expired oauthlib@4.0.0", "<!-- advisory-decay-key: expired oauthlib@4.0.0 -->\n", true))
			wantExit(t, fr, 0)
			if n := len(fr.verb("issue", "create")); n != 0 {
				t.Errorf("issue create ran %d times, want 0 (one pull request per pin; a second issue would duplicate #7's)", n)
			}
			if !strings.Contains(fr.res.stdout, "#7 does not mark unexcused oauthlib@4.0.0") {
				t.Errorf("stdout does not name the member #7 leaves out:\n%s", fr.res.stdout)
			}
		})
	})

	t.Run("infrastructure record files nothing", func(t *testing.T) {
		rec := filerRecord("infrastructure")
		errs := []any{}
		for i := 0; i < 60; i++ {
			errs = append(errs, fmt.Sprintf("osv querybatch: HTTP 503 (%d)", i))
		}
		rec["errors"] = errs
		fr := file(t, rec, nil)
		wantExit(t, fr, 0)
		if !strings.Contains(fr.res.stdout, "advisory issue: infrastructure errors only (60); no finding, no issue") {
			t.Errorf("stdout does not report the infrastructure-only record:\n%s", fr.res.stdout)
		}
		fr.noGh(t)
		positiveControl(t)
	})

	t.Run("dry run makes no gh call", func(t *testing.T) {
		empty := func(dir string) { _ = os.WriteFile(filepath.Join(dir, "labels.json"), []byte("[]"), 0o644) }
		fr := file(t, valid(), empty, "ADVISORY_ISSUE_DRY_RUN=1")
		wantExit(t, fr, 0)
		for _, want := range []string{oauthTitle, oauthMarker} {
			if !strings.Contains(fr.res.stdout, want) {
				t.Errorf("the dry run does not print %q:\n%s", want, fr.res.stdout)
			}
		}
		fr.noGh(t)
		file(t, valid(), empty).reachedGh(t)
	})

	t.Run("missing record fails", func(t *testing.T) {
		absent := filepath.Join(t.TempDir(), "absent.json")
		fr := file(t, nil, nil, "ADVISORY_AUDIT_RECORD="+absent)
		wantExit(t, fr, 1)
		if want := "ERROR: no finding record at " + absent + "; the audit exited before the register stage (setup failure) — nothing to file"; !strings.Contains(fr.res.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, fr.res.stderr)
		}
		fr.noGh(t)
		positiveControl(t)
	})

	t.Run("missing label fails", func(t *testing.T) {
		fr := file(t, valid(), func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "labels.json"), []byte(`[{"name":"agentic"}]`), 0o644)
		})
		wantExit(t, fr, 1)
		if want := `ERROR: label "advisory-heal" does not exist in ` + filerRepo + `; create it (gh label create) before the loop can file`; !strings.Contains(fr.res.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, fr.res.stderr)
		}
		if len(fr.calls) != 1 || len(fr.verb("label", "list")) != 1 {
			t.Errorf("gh calls = %q, want the one label list", fr.calls)
		}
		t.Run("every listed label is checked", func(t *testing.T) {
			fr := file(t, valid(), nil, "ADVISORY_ISSUE_LABELS=agentic,advisory-heal,extra")
			wantExit(t, fr, 1)
			if !strings.Contains(fr.res.stderr, `label "extra" does not exist`) {
				t.Errorf("stderr does not name the missing label extra:\n%s", fr.res.stderr)
			}
			if n := len(fr.verb("issue", "create")); n != 0 {
				t.Errorf("issue create ran %d times with a label missing", n)
			}
		})
	})

	t.Run("invalid field fails", func(t *testing.T) {
		adv := func(rec map[string]any) map[string]any {
			return rec["findings"].([]any)[0].(map[string]any)["advisories"].([]any)[0].(map[string]any)
		}
		first := func(rec map[string]any) map[string]any { return rec["findings"].([]any)[0].(map[string]any) }
		many := func(n int, f func(i int) any) []any {
			out := []any{}
			for i := 0; i < n; i++ {
				out = append(out, f(i))
			}
			return out
		}
		for _, tc := range []struct {
			name, field string
			mutate      func(rec map[string]any)
		}{
			{"an id with shell text", "id", func(r map[string]any) { adv(r)["id"] = "GHSA-x;rm" }},
			{"an alias with a space", "aliases", func(r map[string]any) { adv(r)["aliases"] = []any{"CVE 1"} }},
			{"a key with a space", "key", func(r map[string]any) { first(r)["key"] = "a b@1" }},
			{"a fixed version with shell text", "fixed", func(r map[string]any) { adv(r)["fixed"] = []any{"1.0 && x"} }},
			{"a slash date", "today", func(r map[string]any) { r["today"] = "2026/01/01" }},
			{"an unknown ecosystem", "ecosystem", func(r map[string]any) { adv(r)["ecosystem"] = "Maven" }},
			{"a three-part GitHub repo", "repo", func(r map[string]any) { adv(r)["ecosystem"], adv(r)["repo"] = "GitHub", "a/b/c" }},
			{"a bad pin location", "repo", func(r map[string]any) { first(r)["pinned_in"] = filerPinnedIn([2]string{"GitHub", "a/b/c"}) }},
			{"a bad register date", "recheck_by", func(r map[string]any) {
				first(r)["register"].([]any)[0].(map[string]any)["recheck_by"] = "soon"
			}},
			{"21 fixed versions", "fixed", func(r map[string]any) {
				adv(r)["fixed"] = many(21, func(i int) any { return fmt.Sprintf("4.0.%d", i+1) })
			}},
			{"a fix_available with a space", "fix_available", func(r map[string]any) {
				r["findings"] = []any{litellmExpiredFinding(expiredEntry("GHSA-7hp6-4w63-5g45", "x y"))}
			}},
			{"51 entries", "entries", func(r map[string]any) {
				r["findings"] = []any{map[string]any{"class": "expired", "key": "pkg@1", "pinned": false, "pinned_in": []any{},
					"entries": many(51, func(i int) any { return expiredEntry(fmt.Sprintf("AF-TEST-%02d", i), "") })}}
			}},
			{"a release with a space", "releases", func(r map[string]any) {
				f := nodeBehindFinding()
				f["releases"] = []any{"v22 24"}
				r["findings"] = []any{f}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rec := valid()
				tc.mutate(rec)
				fr := file(t, rec, nil)
				if fr.res.exit == 0 {
					t.Errorf("exit 0; an invalid %s must fail the step", tc.field)
				}
				if !regexp.MustCompile(`\b` + regexp.QuoteMeta(tc.field) + `\b`).MatchString(fr.res.stderr) {
					t.Errorf("stderr does not name the field %s:\n%s", tc.field, fr.res.stderr)
				}
				fr.noGh(t)
			})
		}
		positiveControl(t)
	})

	t.Run("unknown class fails", func(t *testing.T) {
		f := oauthlibFinding()
		f["class"] = "bogus"
		fr := file(t, filerRecord("finding", f), nil)
		if fr.res.exit == 0 {
			t.Error("exit 0; a class with no template must fail the step")
		}
		if !regexp.MustCompile(`\bclass\b`).MatchString(fr.res.stderr) {
			t.Errorf("stderr does not name the field class:\n%s", fr.res.stderr)
		}
		fr.noGh(t)
		positiveControl(t)
	})

	t.Run("out of grammar fixed_raw is not rendered", func(t *testing.T) {
		const tbd = "TBD (maintainers will fill once fixed)"
		fr := file(t, filerRecord("finding", vendorUnexcusedFinding("openobserve@v1.1.2", "GHSA-3jpx-57gj-w458", "openobserve/openobserve", []any{}, tbd, false)), nil)
		wantExit(t, fr, 0)
		body := fr.onlyBody(t)
		if !strings.Contains(body, "https://github.com/advisories/GHSA-3jpx-57gj-w458") {
			t.Errorf("the body does not link the advisory:\n%s", body)
		}
		for _, banned := range []string{"discarded candidates", "TBD"} {
			if strings.Contains(body, banned) {
				t.Errorf("the body renders %q from an out-of-grammar fixed_raw:\n%s", banned, body)
			}
		}
		t.Run("an OSV id links osv.dev", func(t *testing.T) {
			f := oauthlibFinding()
			f["fix_published"] = false
			a := f["advisories"].([]any)[0].(map[string]any)
			a["id"], a["fixed"], a["fixed_raw"] = "PYSEC-2026-9999", []any{}, tbd
			fr := file(t, filerRecord("finding", f), nil)
			wantExit(t, fr, 0)
			if body := fr.onlyBody(t); !strings.Contains(body, "https://osv.dev/vulnerability/PYSEC-2026-9999") || strings.Contains(body, "TBD") {
				t.Errorf("the body does not link osv.dev alone:\n%s", body)
			}
		})
	})

	t.Run("dedupe hit relocks an unlocked issue", func(t *testing.T) {
		fr := file(t, valid(), openIssue(7, oauthTitle, oauthMarker+"\n", false))
		wantExit(t, fr, 0)
		if n := len(fr.verb("issue", "create")); n != 0 {
			t.Errorf("issue create ran %d times on a dedupe hit", n)
		}
		locks := fr.verb("issue", "lock")
		if len(locks) != 1 || (locks[0][2] != "7" && !strings.HasSuffix(locks[0][2], "/issues/7")) || repoOf(locks[0]) != filerRepo {
			t.Errorf("issue lock calls = %q, want one lock of #7 --repo %s", locks, filerRepo)
		}
	})

	t.Run("label list passes an explicit limit", func(t *testing.T) {
		fr := file(t, valid(), func(dir string) {
			var labels []map[string]string
			for i := 0; i < 38; i++ {
				labels = append(labels, map[string]string{"name": fmt.Sprintf("label-%02d", i)})
			}
			labels = append(labels, map[string]string{"name": "agentic"}, map[string]string{"name": "advisory-heal"})
			b, _ := json.Marshal(labels)
			_ = os.WriteFile(filepath.Join(dir, "labels.json"), b, 0o644)
		})
		wantExit(t, fr, 0)
		lists := fr.verb("label", "list")
		if len(lists) != 1 {
			t.Fatalf("label list ran %d times, want 1", len(lists))
		}
		if got := flagValues(lists[0], "--limit", "-L"); len(got) != 1 || got[0] != "200" {
			t.Errorf("label list --limit = %q, want 200 (gh lists 30 by default)", got)
		}
		if got := flagValues(lists[0], "--json"); len(got) != 1 || got[0] != "name" {
			t.Errorf("label list --json = %q, want name", got)
		}
	})

	t.Run("exit_class and findings must agree", func(t *testing.T) {
		for name, tc := range map[string]struct {
			record map[string]any
			want   string
		}{
			"finding with no findings":     {filerRecord("finding"), "is empty although exit_class is finding"},
			"clean with findings":          {filerRecord("clean", oauthlibFinding()), "is not empty although exit_class is clean"},
			"infrastructure with findings": {filerRecord("infrastructure", oauthlibFinding()), "is not empty although exit_class is infrastructure"},
		} {
			t.Run(name, func(t *testing.T) {
				fr := file(t, tc.record, nil)
				if fr.res.exit == 0 || !strings.Contains(fr.res.stderr, tc.want) {
					t.Errorf("exit %d; a record whose exit_class contradicts its findings must fail saying %q:\n%s", fr.res.exit, tc.want, fr.res.stderr)
				}
				fr.noGh(t)
			})
		}
		positiveControl(t)
	})

	t.Run("a create that prints no issue URL fails", func(t *testing.T) {
		fr := file(t, valid(), func(dir string) {
			_ = os.WriteFile(filepath.Join(dir, "create.out"), []byte("https://github.com/"+filerRepo+"/pull/7\n"), 0o644)
		})
		wantExit(t, fr, 1)
		if !strings.Contains(fr.res.stderr, "not an issue URL") {
			t.Errorf("stderr does not reject the create's URL:\n%s", fr.res.stderr)
		}
		if n := len(fr.verb("issue", "lock")); n != 0 {
			t.Errorf("issue lock ran %d times for a URL that names no issue: %q", n, fr.verb("issue", "lock"))
		}
	})

	t.Run("a failed lock after create fails the step", func(t *testing.T) {
		fr := file(t, valid(), func(dir string) { _ = os.WriteFile(filepath.Join(dir, "gh.faults"), []byte("issue lock\n"), 0o644) })
		wantExit(t, fr, 1)
		if !strings.Contains(fr.res.stdout, "advisory issue: filed #101 https://github.com/"+filerRepo+"/issues/101") {
			t.Errorf("stdout does not report the issue the failed lock left behind:\n%s", fr.res.stdout)
		}
		if !strings.Contains(fr.res.stderr, "the issue is filed but unlocked") {
			t.Errorf("stderr does not say the filed issue is unlocked:\n%s", fr.res.stderr)
		}
	})

	t.Run("open issues are listed once per run", func(t *testing.T) {
		fr := file(t, filerRecord("finding", oauthlibFinding(), litellmExpiredFinding()), nil)
		wantExit(t, fr, 0)
		if n := len(fr.verb("issue", "list")); n != 1 {
			t.Errorf("issue list ran %d times for 2 findings, want 1 (one answer serves every finding)", n)
		}
		if n := len(fr.verb("issue", "create")); n != 2 {
			t.Errorf("issue create ran %d times, want 2", n)
		}
		t.Run("a full page fails closed", func(t *testing.T) {
			fr := file(t, valid(), func(dir string) {
				var open []map[string]any
				for i := 0; i < 100; i++ {
					open = append(open, map[string]any{"number": 1000 + i, "title": fmt.Sprintf("advisory-decay: unexcused other@%d", i), "body": ""})
				}
				b, _ := json.Marshal(open)
				_ = os.WriteFile(filepath.Join(dir, "issues.json"), b, 0o644)
			})
			if fr.res.exit == 0 || !strings.Contains(fr.res.stderr, "::error::") || !strings.Contains(fr.res.stderr, "100") {
				t.Errorf("exit %d; 100 open issues may be a truncated list, so the step must fail naming the cap:\n%s", fr.res.exit, fr.res.stderr)
			}
			if n := len(fr.verb("issue", "create")); n != 0 {
				t.Errorf("issue create ran %d times against a possibly truncated list", n)
			}
		})
	})

	// ---- Producer to consumer: records the audit itself writes ------------------------------------

	auditFake := withJq(t, map[string]string{"curl": auditCurlStub, "gh": auditTripwire("gh"), "wget": auditTripwire("wget"), "nc": auditTripwire("nc"), "python3": auditTripwire("python3")})
	auditScript := []byte(readRepoFile(t, auditScriptRel))
	audited := func(t *testing.T, issue, gateway []auditPin, feeds func(db auditDB)) string {
		t.Helper()
		db := newAuditDB(t)
		feeds(db)
		path := filepath.Join(t.TempDir(), "record.json")
		res := auditRunner{t: t, bash: bashPath, fake: auditFake}.run(auditTree(t, auditScript, issue, gateway, baseRegister()), db, "2026-01-15", "ADVISORY_AUDIT_RECORD="+path)
		if res.exit != 1 {
			t.Fatalf("the audit exited %d, want 1 (a finding)\nstderr:\n%s", res.exit, res.stderr)
		}
		db.assertNoStrayCalls()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the audit wrote no finding record: %v", err)
		}
		return string(raw)
	}
	oauthlibAudited := func(t *testing.T) string {
		return audited(t, baseIssueLock, append([]auditPin{{"oauthlib", "4.0.0"}}, baseGatewayLock...), func(db auditDB) {
			db.osv(auditControlLine, "PyPI\toauthlib\t4.0.0\tGHSA-3jp2-3mrg-2vxc")
			db.vulnFixed("GHSA-3jp2-3mrg-2vxc", "PyPI", "oauthlib", "4.0.1", "CVE-2026-49272")
		})
	}
	openobserveAudited := func(t *testing.T, adv map[string]any) string {
		return audited(t, baseIssueLock, baseGatewayLock, func(db auditDB) { db.ghPages("openobserve/openobserve", []map[string]any{adv}) })
	}

	t.Run("audit record feeds the filer", func(t *testing.T) {
		for name, tc := range map[string]struct {
			record      func(t *testing.T) string
			want, avoid []string
		}{
			"oauthlib": {oauthlibAudited, []string{"4.0.1", pinMoveText}, []string{noFixText}},
			"rw8w": {func(t *testing.T) string {
				return openobserveAudited(t, ghAdvisory("GHSA-rw8w-37p9-mrrp", "CVE-2024-41809", ghVuln{">= v0.5.2", patched("v0.5.2")}))
			}, []string{noFixText + " (discarded candidates: v0.5.2)", operatorText}, []string{pinMoveText}},
			"3jpx": {func(t *testing.T) string {
				return openobserveAudited(t, ghAdvisory("GHSA-3jpx-57gj-w458", "CVE-2025-64744", ghVuln{"All (before fix)", patched("TBD (maintainers will fill once fixed)")}))
			}, []string{"https://github.com/advisories/GHSA-3jpx-57gj-w458", noFixText, operatorText}, []string{"discarded candidates", pinMoveText}},
		} {
			t.Run(name, func(t *testing.T) {
				fr := file(t, tc.record(t), nil)
				wantExit(t, fr, 0)
				body := fr.onlyBody(t)
				for _, w := range tc.want {
					if !strings.Contains(body, w) {
						t.Errorf("the body lacks %q:\n%s", w, body)
					}
				}
				for _, a := range tc.avoid {
					if strings.Contains(body, a) {
						t.Errorf("the body says %q, which the record's fix_published contradicts:\n%s", a, body)
					}
				}
			})
		}
	})

	t.Run("an expired key with a published fix files one issue that names it", func(t *testing.T) {
		db := newAuditDB(t)
		db.ghPages("BerriAI/litellm", []map[string]any{ghAdvisory("GHSA-g5ff-637f-6q2m", "", ghVuln{"< 1.95.0", patched("1.95.0")})})
		reg := append(baseRegister(), auditException("GHSA-g5ff-637f-6q2m", nil, auditPin{"litellm", "1.93.2"}, "2026-01-01", "2026-01-30"))
		path := filepath.Join(t.TempDir(), "record.json")
		res := auditRunner{t: t, bash: bashPath, fake: auditFake}.run(auditTree(t, auditScript, baseIssueLock, baseGatewayLock, reg), db, "2026-01-31", "ADVISORY_AUDIT_RECORD="+path)
		if res.exit != 1 {
			t.Fatalf("the audit exited %d, want 1 (a finding)\nstderr:\n%s", res.exit, res.stderr)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the audit wrote no finding record: %v", err)
		}
		fr := file(t, string(raw), nil)
		wantExit(t, fr, 0)
		var titles []string
		for _, c := range fr.verb("issue", "create") {
			titles = append(titles, flagValues(c, "-t", "--title")...)
		}
		if !reflect.DeepEqual(titles, []string{"advisory-decay: expired litellm@1.93.2"}) {
			t.Errorf("issues filed = %q, want the one expired issue: one key, one issue", titles)
		}
		for n, body := range fr.bodies {
			if strings.Contains(body, noFixText) || strings.Contains(body, expiredNoFix) {
				t.Errorf("issue #%d says no fix is published although 1.95.0 is:\n%s", n, body)
			}
			if !strings.Contains(body, "1.95.0") {
				t.Errorf("issue #%d does not name the published fix 1.95.0:\n%s", n, body)
			}
		}
	})

	t.Run("no fix published renders the exception path", func(t *testing.T) {
		fr := file(t, filerRecord("finding", vendorUnexcusedFinding("openobserve@v1.1.2", "GHSA-rw8w-37p9-mrrp", "openobserve/openobserve", []any{}, "v0.5.2", false)), nil)
		wantExit(t, fr, 0)
		body := fr.onlyBody(t)
		for _, want := range []string{noFixText + " (discarded candidates: v0.5.2)", operatorText} {
			if !strings.Contains(body, want) {
				t.Errorf("the body lacks %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, pinMoveText) {
			t.Errorf("the no-fix body takes the pin-move path:\n%s", body)
		}
	})

	t.Run("oauthlib shaped record renders the fixed version", func(t *testing.T) {
		raw := oauthlibAudited(t)
		var rec auditRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil || len(rec.Findings) != 1 || len(rec.Findings[0].Advisories) != 1 || rec.Findings[0].Advisories[0].Repo != "" {
			t.Fatalf("the audit's oauthlib record is not the one-advisory, repo-less shape this case needs: %v\n%s", err, raw)
		}
		fr := file(t, raw, nil)
		wantExit(t, fr, 0)
		body := fr.onlyBody(t)
		for _, want := range []string{"4.0.1", pinMoveText} {
			if !strings.Contains(body, want) {
				t.Errorf("the body lacks %q:\n%s", want, body)
			}
		}
	})

	movesThePin := regexp.MustCompile(`(?i)move the pin`)
	t.Run("litellm expired renders the renewal path", func(t *testing.T) {
		fr := file(t, filerRecord("finding", litellmExpiredFinding()), nil)
		wantExit(t, fr, 0)
		body := fr.onlyBody(t)
		for _, want := range []string{operatorText, "fix_available", "1.100.4"} {
			if !strings.Contains(body, want) {
				t.Errorf("the body lacks %q:\n%s", want, body)
			}
		}
		if !regexp.MustCompile(`(?i)renew`).MatchString(body) {
			t.Errorf("the body does not say to renew the entry:\n%s", body)
		}
		if strings.Contains(body, pinMoveText) || movesThePin.MatchString(body) {
			t.Errorf("the litellm body says to move the pin (ADR-024 holds it):\n%s", body)
		}
		t.Run("another package with one acknowledged fix moves the pin", func(t *testing.T) {
			fr := file(t, filerRecord("finding", openobserveExpiredFinding(expiredEntry("GHSA-rw8w-37p9-mrrp", "v1.2.0"), expiredEntry("GHSA-3jpx-57gj-w458", ""))), nil)
			wantExit(t, fr, 0)
			body := fr.onlyBody(t)
			if !strings.Contains(body, pinMoveText) || !strings.Contains(body, "v1.2.0") || strings.Contains(body, noFixText) {
				t.Errorf("an entry naming fix_available must send the finding down the pin-move path:\n%s", body)
			}
		})
		t.Run("another package with no acknowledged fix takes the exception path", func(t *testing.T) {
			fr := file(t, filerRecord("finding", openobserveExpiredFinding(expiredEntry("GHSA-rw8w-37p9-mrrp", ""), expiredEntry("GHSA-3jpx-57gj-w458", ""))), nil)
			wantExit(t, fr, 0)
			body := fr.onlyBody(t)
			if !strings.Contains(body, expiredNoFix) || !strings.Contains(body, operatorText) || strings.Contains(body, pinMoveText) {
				t.Errorf("with no entry naming fix_available the body takes the exception path:\n%s", body)
			}
			if strings.Contains(body, noFixText) {
				t.Errorf("the expired body says %q; it knows only the advisories its entries excuse:\n%s", noFixText, body)
			}
		})
	})

	t.Run("empty optional fields are neither validated nor rendered", func(t *testing.T) {
		f := oauthlibFinding()
		f["register"] = []any{}
		fr := file(t, filerRecord("finding", f, openobserveExpiredFinding(expiredEntry("GHSA-rw8w-37p9-mrrp", ""))), nil)
		wantExit(t, fr, 0)
		if len(fr.bodies) != 2 {
			t.Fatalf("filed %d bodies, want 2", len(fr.bodies))
		}
		emptyFix := regexp.MustCompile("fix_available\\W{0,4}(\n|$|\"\"|``)")
		for _, body := range fr.bodies {
			for _, banned := range []string{"discarded candidates", "``", "()"} {
				if strings.Contains(body, banned) {
					t.Errorf("a body renders %q for an empty optional field:\n%s", banned, body)
				}
			}
			if m := emptyFix.FindString(body); m != "" {
				t.Errorf("a body renders an empty fix_available (%q):\n%s", m, body)
			}
		}
	})

	t.Run("a folded expired finding takes the fix it names", func(t *testing.T) {
		dod := func(body string) string { return section(body, "## Definition of done", "## Run") }
		t.Run("litellm renews and acknowledges", func(t *testing.T) {
			f := excusedFixable(litellmExpiredFinding(expiredEntry("GHSA-g5ff-637f-6q2m", "")), [3]string{"GHSA-g5ff-637f-6q2m", "GHSA-g5ff-637f-6q2m", "1.95.0"})
			fr := file(t, filerRecord("finding", f), nil)
			wantExit(t, fr, 0)
			body := fr.onlyBody(t)
			for _, want := range []string{"entry `GHSA-g5ff-637f-6q2m` excuses `GHSA-g5ff-637f-6q2m`, published fix: 1.95.0", "fix_available", "1.95.0", operatorText} {
				if !strings.Contains(body, want) {
					t.Errorf("the body lacks %q:\n%s", want, body)
				}
			}
			for _, banned := range []string{noFixText, expiredNoFix, pinMoveText} {
				if strings.Contains(body, banned) {
					t.Errorf("the body says %q although the finding names the published fix 1.95.0:\n%s", banned, body)
				}
			}
			if movesThePin.MatchString(body) {
				t.Errorf("the litellm body says to move the pin (ADR-024 holds it):\n%s", body)
			}
			if strings.Contains(body, "change nothing else") {
				t.Errorf("the body tells the entry it renews to change nothing else in it:\n%s", body)
			}
			for _, key := range []string{"`expired litellm@1.93.2`", "`excused-fixable litellm@1.93.2`"} {
				if !strings.Contains(dod(body), key) {
					t.Errorf("the definition of done does not name %s; renewing alone would leave the other finding:\n%s", key, dod(body))
				}
			}
		})
		t.Run("another package moves the pin and deletes the entries", func(t *testing.T) {
			f := excusedFixable(openobserveExpiredFinding(expiredEntry("GHSA-rw8w-37p9-mrrp", "")), [3]string{"GHSA-rw8w-37p9-mrrp", "GHSA-zzzz-0000-0001", "v1.2.0"})
			fr := file(t, filerRecord("finding", f), nil)
			wantExit(t, fr, 0)
			body := fr.onlyBody(t)
			for _, want := range []string{pinMoveText, "v1.2.0", "delete the register entries for `openobserve@v1.1.2`"} {
				if !strings.Contains(body, want) {
					t.Errorf("the body lacks %q:\n%s", want, body)
				}
			}
			for _, banned := range []string{noFixText, expiredNoFix} {
				if strings.Contains(body, banned) {
					t.Errorf("the body says %q although the finding names the published fix v1.2.0:\n%s", banned, body)
				}
			}
		})
		t.Run("an expired finding without the field fails", func(t *testing.T) {
			f := litellmExpiredFinding()
			delete(f, "excused_fixable")
			fr := file(t, filerRecord("finding", f), nil)
			if fr.res.exit == 0 || !regexp.MustCompile(`\bexcused_fixable\b`).MatchString(fr.res.stderr) {
				t.Errorf("exit %d; an expired finding without excused_fixable must fail naming the field:\n%s", fr.res.exit, fr.res.stderr)
			}
			fr.noGh(t)
		})
	})

	t.Run("node behind names the apt pin", func(t *testing.T) {
		fr := file(t, filerRecord("finding", nodeBehindFinding()), nil)
		wantExit(t, fr, 0)
		if where := section(fr.onlyBody(t), "Where it is pinned", "What fixes it"); !strings.Contains(where, "NODE_APT_VERSION") || !strings.Contains(where, "apt") {
			t.Errorf("\"Where it is pinned\" does not name the apt pin NODE_APT_VERSION:\n%s", where)
		}
	})

	t.Run("litellm expired names both pin locations", func(t *testing.T) {
		fr := file(t, filerRecord("finding", litellmExpiredFinding()), nil)
		wantExit(t, fr, 0)
		where := section(fr.onlyBody(t), "Where it is pinned", "What fixes it")
		for _, want := range []string{"py/", "BerriAI/litellm"} {
			if !strings.Contains(where, want) {
				t.Errorf("\"Where it is pinned\" lacks %q (one location per pinned_in element):\n%s", want, where)
			}
		}
	})

	t.Run("litellm excused fixable renders the acknowledgement path", func(t *testing.T) {
		fr := file(t, filerRecord("finding", litellmExcusedFixableFinding()), nil)
		wantExit(t, fr, 0)
		body := fr.onlyBody(t)
		for _, want := range []string{"fix_available", "1.95.0", operatorText} {
			if !strings.Contains(body, want) {
				t.Errorf("the body lacks %q:\n%s", want, body)
			}
		}
		if strings.Contains(body, pinMoveText) || movesThePin.MatchString(body) {
			t.Errorf("the litellm body says to move the pin (ADR-024 holds it):\n%s", body)
		}
	})

	t.Run("scoped npm key is filed in backticks", func(t *testing.T) {
		raw := audited(t, baseIssueLock, baseGatewayLock, func(db auditDB) {
			db.osv(auditControlLine, "npm\t@anthropic-ai/claude-code\t2.9.998\tGHSA-ccde-0000-0001")
			db.vuln("GHSA-ccde-0000-0001")
		})
		fr := file(t, raw, nil)
		wantExit(t, fr, 0)
		body := fr.onlyBody(t)
		if !strings.Contains(body, "`@anthropic-ai/claude-code@2.9.998`") {
			t.Errorf("the body does not render the scoped key in backticks:\n%s", body)
		}
		stripped := regexp.MustCompile("(?m)^.*<!-- advisory-decay-(key|pin): .*-->.*$").ReplaceAllString(body, "")
		stripped = regexp.MustCompile("`[^`\n]*`").ReplaceAllString(stripped, "")
		if strings.Contains(stripped, "@anthropic-ai/") {
			t.Errorf("the scoped key appears outside backticks, where GitHub renders @anthropic-ai as a team mention:\n%s", body)
		}
	})

	t.Run("failed issue list files nothing", func(t *testing.T) {
		faulted := func(read string, setup func(dir string)) func(dir string) {
			return func(dir string) {
				if setup != nil {
					setup(dir)
				}
				_ = os.WriteFile(filepath.Join(dir, "gh.faults"), []byte(read+"\n"), 0o644)
			}
		}
		fr := file(t, valid(), faulted("issue list", nil))
		if fr.res.exit == 0 {
			t.Error("exit 0 after a failed dedupe read")
		}
		if !strings.Contains(fr.res.stderr, "::error::") || !strings.Contains(fr.res.stderr, "issue list") {
			t.Errorf("stderr does not fail loudly naming issue list:\n%s", fr.res.stderr)
		}
		fr.reachedGh(t)
		if n := len(fr.verb("issue", "create")); n != 0 {
			t.Errorf("issue create ran %d times after the dedupe read failed", n)
		}
		t.Run("a failed label list", func(t *testing.T) {
			fr := file(t, valid(), faulted("label list", nil))
			if fr.res.exit == 0 || !strings.Contains(fr.res.stderr, "::error::") || !strings.Contains(fr.res.stderr, "label list") || strings.Contains(fr.res.stderr, "does not exist") {
				t.Errorf("exit %d; a failed label read must fail loudly naming label list, not report the labels missing:\n%s", fr.res.exit, fr.res.stderr)
			}
			if n := len(fr.verb("issue", "create")); n != 0 {
				t.Errorf("issue create ran %d times", n)
			}
		})
		t.Run("a failed lock read on a dedupe hit", func(t *testing.T) {
			fr := file(t, valid(), faulted("api", openIssue(7, oauthTitle, oauthMarker+"\n", false)))
			if fr.res.exit == 0 || !strings.Contains(fr.res.stderr, "::error::") || !strings.Contains(fr.res.stderr, "api") {
				t.Errorf("exit %d; a failed .locked read must fail loudly naming the read:\n%s", fr.res.exit, fr.res.stderr)
			}
			if n := len(fr.verb("issue", "create")) + len(fr.verb("issue", "lock")); n != 0 {
				t.Errorf("%d create or lock calls followed a failed read", n)
			}
		})
	})
	t.Run("the job summary counts filed, already-open and failed issues", func(t *testing.T) {
		summaryOf := func(t *testing.T, record any, setup func(dir string), env ...string) (filerRun, string) {
			t.Helper()
			path := filepath.Join(t.TempDir(), "summary.md")
			fr := file(t, record, setup, append([]string{"GITHUB_STEP_SUMMARY=" + path}, env...)...)
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no job summary was written: %v\nstdout:\n%s\nstderr:\n%s", err, fr.res.stdout, fr.res.stderr)
			}
			return fr, string(got)
		}
		wantLine := func(t *testing.T, got, want string) {
			t.Helper()
			if got != want+"\n" {
				t.Errorf("job summary = %q, want exactly the one line %q", got, want)
			}
		}
		t.Run("a filing run", func(t *testing.T) {
			fr, got := summaryOf(t, valid(), nil)
			wantExit(t, fr, 0)
			wantLine(t, got, "advisory issues: 1 filed, 0 already open, 0 failed")
		})
		t.Run("a rerun", func(t *testing.T) {
			fr, got := summaryOf(t, valid(), openIssue(7, oauthTitle, "Healing issue.\n"+oauthMarker+"\n", true))
			wantExit(t, fr, 0)
			wantLine(t, got, "advisory issues: 0 filed, 1 already open, 0 failed")
		})
		t.Run("a failed create", func(t *testing.T) {
			fr, got := summaryOf(t, valid(), func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, "gh.faults"), []byte("issue create\n"), 0o644)
			})
			wantExit(t, fr, 1)
			wantLine(t, got, "advisory issues: 0 filed, 0 already open, 1 failed")
		})
		t.Run("a gh failure before any pin is filed", func(t *testing.T) {
			fr, got := summaryOf(t, valid(), func(dir string) {
				_ = os.WriteFile(filepath.Join(dir, "gh.faults"), []byte("label list\n"), 0o644)
			})
			wantExit(t, fr, 1)
			wantLine(t, got, "advisory issues: 0 filed, 0 already open, 0 failed; stopped early (exit 1), see the step log")
		})
		t.Run("a dry run", func(t *testing.T) {
			fr, got := summaryOf(t, valid(), nil, "ADVISORY_ISSUE_DRY_RUN=1")
			wantExit(t, fr, 0)
			fr.noGh(t)
			wantLine(t, got, "advisory issues (dry run): 1 would be filed, none filed")
		})
		t.Run("without GITHUB_STEP_SUMMARY the line goes nowhere", func(t *testing.T) {
			fr := file(t, valid(), nil)
			wantExit(t, fr, 0)
			if strings.Contains(fr.res.stdout+fr.res.stderr, "advisory issues") {
				t.Errorf("the summary line was printed instead of written to the job summary:\nstdout:\n%s\nstderr:\n%s", fr.res.stdout, fr.res.stderr)
			}
		})
	})
	// ---- One issue per pin (#755) ---------------------------------------------------------------------

	const (
		goTitle     = "advisory-decay: unexcused stdlib@1.26.10, unexcused toolchain@1.26.10"
		goPinMarker = "<!-- advisory-decay-pin: go@1.26.10 -->"
	)
	goPair := func() []map[string]any {
		return []map[string]any{
			pinFinding("Go", "stdlib", "1.26.10", "GO-2026-6859"),
			pinFinding("Go", "toolchain", "1.26.10", "GO-2026-6848", "GO-2026-6858"),
		}
	}
	bodyWith := func(t *testing.T, fr filerRun, marker string) string {
		t.Helper()
		for _, b := range fr.bodies {
			if strings.Contains(b, marker) {
				return b
			}
		}
		t.Fatalf("no filed body carries %q; bodies: %q", marker, fr.bodies)
		return ""
	}
	titles := func(fr filerRun) []string {
		var out []string
		for _, c := range fr.verb("issue", "create") {
			out = append(out, flagValues(c, "-t", "--title")...)
		}
		return out
	}

	t.Run("one pin, one issue", func(t *testing.T) {
		t.Run("Go stdlib and toolchain from GO_TOOLCHAIN_VERSION", func(t *testing.T) {
			g := goPair()
			fr := file(t, filerRecord("finding", oauthlibFinding(), g[0], g[1]), nil)
			wantExit(t, fr, 0)
			if got, want := titles(fr), []string{oauthTitle, goTitle}; !equalStrs(got, want) {
				t.Fatalf("titles = %q, want %q", got, want)
			}
			body := bodyWith(t, fr, goPinMarker)
			for _, want := range []string{
				"<!-- advisory-decay-key: unexcused stdlib@1.26.10 -->", "<!-- advisory-decay-key: unexcused toolchain@1.26.10 -->",
				"GO-2026-6859", "GO-2026-6848", "GO-2026-6858",
				"come from one pin, so one pull request moves it",
				"One version must fix every finding above",
				"For `unexcused stdlib@1.26.10`:", "For `unexcused toolchain@1.26.10`:",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("the Go body lacks %q:\n%s", want, body)
				}
			}
			others := section(body, "## Other findings in this run", "## Definition of done")
			if !strings.Contains(others, "`oauthlib@4.0.0`") || strings.Contains(others, "stdlib@") || strings.Contains(others, "toolchain@") {
				t.Errorf("\"Other findings\" must list only the other pins:\n%s", others)
			}
			if strings.Contains(body, "do not fix them here") {
				t.Errorf("the Go body tells the implementer to leave alone something its own fix changes:\n%s", body)
			}
			dod := section(body, "## Definition of done", "## Run")
			for _, want := range []string{"`unexcused stdlib@1.26.10`", "`unexcused toolchain@1.26.10`"} {
				if !strings.Contains(dod, want) {
					t.Errorf("the definition of done does not name %s:\n%s", want, dod)
				}
			}
			oauth := bodyWith(t, fr, oauthMarker)
			if !strings.Contains(oauth, "<!-- advisory-decay-pin: oauthlib@4.0.0 -->") {
				t.Errorf("the single-finding body lacks its pin marker:\n%s", oauth)
			}
			if o := section(oauth, "## Other findings in this run", "## Definition of done"); !strings.Contains(o, "`stdlib@1.26.10`") || !strings.Contains(o, "`toolchain@1.26.10`") {
				t.Errorf("the oauthlib body's \"Other findings\" lacks the Go pin's findings:\n%s", o)
			}
		})

		t.Run("an unpinned Go version files one issue per key", func(t *testing.T) {
			unpinned := func(name, id string) map[string]any {
				return map[string]any{"class": "expired", "key": name + "@1.25.0", "pinned": false, "pinned_in": filerPinnedIn(),
					"entries": []any{expiredEntry(id, "")}, "excused_fixable": []any{}}
			}
			fr := file(t, filerRecord("finding", unpinned("stdlib", "GO-2025-0001"), unpinned("toolchain", "GO-2025-0002")), nil)
			wantExit(t, fr, 0)
			if got, want := titles(fr), []string{"advisory-decay: expired stdlib@1.25.0", "advisory-decay: expired toolchain@1.25.0"}; !equalStrs(got, want) {
				t.Errorf("titles = %q, want %q (no audited pin carries them, so nothing makes them one pin)", got, want)
			}
			bodyWith(t, fr, "<!-- advisory-decay-pin: stdlib@1.25.0 -->")
			bodyWith(t, fr, "<!-- advisory-decay-pin: toolchain@1.25.0 -->")
		})

		t.Run("a non-Go stdlib and toolchain are not the Go pin", func(t *testing.T) {
			fr := file(t, filerRecord("finding", pinFinding("PyPI", "stdlib", "3.1", "PYSEC-2026-1"), pinFinding("PyPI", "toolchain", "3.1", "PYSEC-2026-2")), nil)
			wantExit(t, fr, 0)
			if got, want := titles(fr), []string{"advisory-decay: unexcused stdlib@3.1", "advisory-decay: unexcused toolchain@3.1"}; !equalStrs(got, want) {
				t.Errorf("titles = %q, want %q (only Go stdlib and toolchain are GO_TOOLCHAIN_VERSION)", got, want)
			}
			bodyWith(t, fr, "<!-- advisory-decay-pin: stdlib@3.1 -->")
			bodyWith(t, fr, "<!-- advisory-decay-pin: toolchain@3.1 -->")
		})

		t.Run("playwright and playwright-core pair by version only", func(t *testing.T) {
			fr := file(t, filerRecord("finding",
				pinFinding("npm", "playwright", "1.64.1", "GHSA-aaaa-bbbb-cccc"), pinFinding("npm", "playwright-core", "1.64.1", "GHSA-aaaa-bbbb-cccd"),
				pinFinding("npm", "playwright", "1.63.0", "GHSA-aaaa-bbbb-ccce"), pinFinding("npm", "playwright-core", "1.63.0", "GHSA-aaaa-bbbb-cccf"),
				pinFinding("npm", "left-pad", "1.64.1", "GHSA-aaaa-bbbb-cccg")), nil)
			wantExit(t, fr, 0)
			want := []string{
				"advisory-decay: unexcused playwright@1.64.1, unexcused playwright-core@1.64.1",
				"advisory-decay: unexcused playwright@1.63.0, unexcused playwright-core@1.63.0",
				"advisory-decay: unexcused left-pad@1.64.1",
			}
			if got := titles(fr); !equalStrs(got, want) {
				t.Errorf("titles = %q, want %q", got, want)
			}
			bodyWith(t, fr, "<!-- advisory-decay-pin: playwright@1.64.1 -->")
			bodyWith(t, fr, "<!-- advisory-decay-pin: playwright@1.63.0 -->")
		})

		t.Run("every class of one key is one pin", func(t *testing.T) {
			fr := file(t, filerRecord("finding", litellmExpiredFinding(), litellmExcusedFixableFinding()), nil)
			wantExit(t, fr, 0)
			if got, want := titles(fr), []string{"advisory-decay: expired litellm@1.93.2, excused-fixable litellm@1.93.2"}; !equalStrs(got, want) {
				t.Fatalf("titles = %q, want %q", got, want)
			}
			dod := section(fr.onlyBody(t), "## Definition of done", "## Run")
			for _, want := range []string{"`expired litellm@1.93.2`", "`excused-fixable litellm@1.93.2`"} {
				if !strings.Contains(dod, want) {
					t.Errorf("the definition of done does not name %s:\n%s", want, dod)
				}
			}
		})

		t.Run("the audit's own Go record files one issue", func(t *testing.T) {
			rec := audited(t, baseIssueLock, baseGatewayLock, func(db auditDB) {
				db.osv(auditControlLine, "Go\tstdlib\t1.99.1\tGO-2099-0001", "Go\ttoolchain\t1.99.1\tGO-2099-0002")
				db.vulnFixed("GO-2099-0001", "Go", "stdlib", "1.99.2")
				db.vulnFixed("GO-2099-0002", "Go", "toolchain", "1.99.2")
			})
			fr := file(t, rec, nil)
			wantExit(t, fr, 0)
			if got, want := titles(fr), []string{"advisory-decay: unexcused stdlib@1.99.1, unexcused toolchain@1.99.1"}; !equalStrs(got, want) {
				t.Errorf("titles = %q, want %q (the grouping must key on fields the audit really writes)", got, want)
			}
		})

		t.Run("the 2026-11-11 record rehearses as three issues", func(t *testing.T) {
			rec := readRepoFile(t, "internal/cmd/testdata/advisory_record_2026-11-11.json")
			fr := file(t, rec, nil, "ADVISORY_ISSUE_DRY_RUN=1")
			wantExit(t, fr, 0)
			fr.noGh(t)
			var heads []string
			for _, l := range strings.Split(fr.res.stdout, "\n") {
				if strings.HasPrefix(l, "advisory issue (dry run): ") {
					heads = append(heads, l)
				}
			}
			want := []string{
				"advisory issue (dry run): advisory-decay: node-behind nodejs@v22.23.4",
				"advisory issue (dry run): advisory-decay: unexcused oauthlib@4.0.0",
				"advisory issue (dry run): " + goTitle,
			}
			if !equalStrs(heads, want) {
				t.Errorf("dry-run blocks = %q, want %q", heads, want)
			}
			if !strings.Contains(fr.res.stdout, "Resolves stempeck/agentfactory-pro#N") {
				t.Errorf("the dry run's definition of done does not resolve on agentfactory-pro:\n%s", fr.res.stdout)
			}
		})
	})

	t.Run("an issue names everything its fix changes", func(t *testing.T) {
		g := goPair()
		fr := file(t, filerRecord("finding", g[0], g[1], nodeBehindFinding(), oauthlibFinding()), nil)
		wantExit(t, fr, 0)
		mirrors := "`internal/cmd/keeptrue_pin_mirrors_test.go` and `internal/cmd/keeptrue_toolchain_parity_test.go`"
		goWhere := section(bodyWith(t, fr, goPinMarker), "## Where it is pinned", "## What fixes it")
		goWant := []string{"`GO_TOOLCHAIN_VERSION`", "`GO_SHA256_ARM64`", "`GO_SHA256_AMD64`", "`toolchain`", "`go-version`",
			"`internal/cmd/quickstart_pins_test.go`", mirrors}
		for _, f := range toolchainParityFiles {
			goWant = append(goWant, "`"+f+"`")
		}
		for _, want := range goWant {
			if !strings.Contains(goWhere, want) {
				t.Errorf("the Go body's \"Where it is pinned\" lacks %s:\n%s", want, goWhere)
			}
		}
		if n := strings.Count(goWhere, "- Go:"); n != 1 {
			t.Errorf("the Go location is listed %d times, want once for the pin:\n%s", n, goWhere)
		}
		nodeWhere := section(bodyWith(t, fr, "<!-- advisory-decay-key: node-behind nodejs@v22.23.4 -->"), "## Where it is pinned", "## What fixes it")
		for _, want := range []string{"`NODE_APT_VERSION`", "`quickstart.sh`", "`internal/cmd/quickstart_pins_test.go`", mirrors} {
			if !strings.Contains(nodeWhere, want) {
				t.Errorf("the Node body's \"Where it is pinned\" lacks %s:\n%s", want, nodeWhere)
			}
		}
		pyWhere := section(bodyWith(t, fr, oauthMarker), "## Where it is pinned", "## What fixes it")
		for _, want := range []string{"`py/*.in`", "`make lock`", "`py/lock-wheels.txt`", mirrors} {
			if !strings.Contains(pyWhere, want) {
				t.Errorf("the PyPI body's \"Where it is pinned\" lacks %s:\n%s", want, pyWhere)
			}
		}

	})
	t.Run("a pull request that moves the Go pin only where its issue says passes the parity check", func(t *testing.T) {
		v := strings.TrimPrefix(goToolchainPinRe.FindStringSubmatch(readRepoFile(t, "quickstart.sh"))[1], "go")
		live := pinFinding("Go", "stdlib", v, "GO-2026-6859")
		live2 := pinFinding("Go", "toolchain", v, "GO-2026-6848")
		fr := file(t, filerRecord("finding", live, live2), nil, "ADVISORY_ISSUE_DRY_RUN=1")
		wantExit(t, fr, 0)
		named := map[string]bool{}
		for _, m := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(section(fr.res.stdout, "## Where it is pinned", "## What fixes it"), -1) {
			named[m[1]] = true
		}
		if errs := goPinMoveErrors(t, named, v, v+"9"); len(errs) != 0 {
			t.Errorf("a pull request that moves the pin only where the issue says still fails TestToolchainParity: %q", errs)
		}
		if errs := goPinMoveErrors(t, map[string]bool{"quickstart.sh": true}, v, v+"9"); len(errs) == 0 {
			t.Error("negative control: moving only quickstart.sh passes the parity check, so this simulation proves nothing")
		}
	})

	t.Run("the definition of done includes make test", func(t *testing.T) {
		dod := section(file(t, valid(), nil).bodies[101], "## Definition of done", "## Run")
		for _, want := range []string{"`make test` passes on this pull request's branch", "`make test` cannot see this finding"} {
			if !strings.Contains(dod, want) {
				t.Errorf("the definition of done lacks %q:\n%s", want, dod)
			}
		}
	})

	t.Run("other pins are named, never fenced off from a forced move", func(t *testing.T) {
		g := goPair()
		fr := file(t, filerRecord("finding", oauthlibFinding(), g[0], g[1]), nil)
		wantExit(t, fr, 0)
		others := section(bodyWith(t, fr, oauthMarker), "## Other findings in this run", "## Definition of done")
		for _, want := range []string{"has its own issue", "Do not move those pins on purpose",
			"keep that move and list it in the pull request", "stays red while any of them is open", "the dispatcher does not wait for it"} {
			if !strings.Contains(others, want) {
				t.Errorf("\"Other findings\" lacks %q:\n%s", want, others)
			}
		}
	})

	t.Run("dedupe is per pin", func(t *testing.T) {
		g := goPair()
		rec := func() map[string]any { return filerRecord("finding", oauthlibFinding(), g[0], g[1]) }
		for name, open := range map[string]func(dir string){
			"the pin marker":             openIssue(7, "Healing Go", goPinMarker+"\n", true),
			"one member's key marker":    openIssue(7, "Healing Go", "<!-- advisory-decay-key: unexcused stdlib@1.26.10 -->\n", true),
			"one member's finding title": openIssue(7, "advisory-decay: unexcused stdlib@1.26.10", "Filed by hand.\n", true),
			"the group title":            openIssue(7, goTitle, "Filed by hand.\n", true),
		} {
			t.Run("a hit on "+name, func(t *testing.T) {
				fr := file(t, rec(), open)
				wantExit(t, fr, 0)
				if got := titles(fr); !equalStrs(got, []string{oauthTitle}) {
					t.Errorf("titles = %q, want only %q (the Go pin is open as #7)", got, oauthTitle)
				}
				if !strings.Contains(fr.res.stdout, `"`+goTitle+`" already open as #7; not filing`) {
					t.Errorf("stdout does not report the open Go issue:\n%s", fr.res.stdout)
				}
			})
		}
		t.Run("a partial hit names the members the open issue leaves out", func(t *testing.T) {
			fr := file(t, rec(), openIssue(7, "Healing Go", "<!-- advisory-decay-key: unexcused stdlib@1.26.10 -->\n", true))
			if !strings.Contains(fr.res.stdout, "#7 does not mark unexcused toolchain@1.26.10") || strings.Contains(fr.res.stdout, "#7 does not mark unexcused stdlib@1.26.10") {
				t.Errorf("stdout must name exactly the member #7 leaves out:\n%s", fr.res.stdout)
			}
		})
		t.Run("a rerun with every pin open files nothing and lists once", func(t *testing.T) {
			fr := file(t, rec(), func(dir string) {
				b, _ := json.Marshal([]map[string]any{
					{"number": 7, "title": goTitle, "body": goPinMarker + "\n"},
					{"number": 8, "title": oauthTitle, "body": oauthMarker + "\n"},
				})
				_ = os.WriteFile(filepath.Join(dir, "issues.json"), b, 0o644)
			})
			wantExit(t, fr, 0)
			if n := len(fr.verb("issue", "create")); n != 0 {
				t.Errorf("issue create ran %d times on a rerun with every pin open", n)
			}
			if n := len(fr.verb("issue", "list")); n != 1 {
				t.Errorf("issue list ran %d times, want 1", n)
			}
		})
	})

	t.Run("the same code files as the public repository", func(t *testing.T) {
		fr := file(t, valid(), nil, "GITHUB_REPOSITORY=stempeck/agentfactory")
		wantExit(t, fr, 0)
		for _, c := range fr.calls {
			if c[0] != "api" && repoOf(c) != "stempeck/agentfactory" {
				t.Errorf("gh %q does not pass --repo stempeck/agentfactory", c)
			}
		}
		if body := fr.onlyBody(t); !strings.Contains(body, "Resolves stempeck/agentfactory#") {
			t.Errorf("the body does not resolve on the repository it runs in:\n%s", body)
		}
	})

	t.Run("the filer names no repository and never writes the register or runs git", func(t *testing.T) {
		src := readRepoFile(t, advisoryIssueRel)
		for desc, re := range map[string]string{
			"a stempeck/ literal (the repository comes from GITHUB_REPOSITORY)": `stempeck/`,
			"a write to the exception register":                                 `>\s*\S*advisory-exceptions\.json`,
			"a git command":                                                     `(?m)(^|[;&|(]\s*)git\s`,
		} {
			if regexp.MustCompile(re).MatchString(src) {
				t.Errorf("%s contains %s", advisoryIssueRel, desc)
			}
		}
	})

	t.Run("the filer's sibling pins are the audit's two-package lines", func(t *testing.T) {
		var audit, filer []string
		for _, m := range regexp.MustCompile(`printf '(\S+) (\S+) %s\\n(\S+) (\S+) %s\\n'`).FindAllStringSubmatch(readRepoFile(t, auditScriptRel), -1) {
			if m[1] != m[3] {
				t.Fatalf("an audit line emits two ecosystems for one pin: %q", m[0])
			}
			audit = append(audit, m[1]+" "+m[2]+" "+m[4])
		}
		for _, m := range regexp.MustCompile(`\{ecosystem: "(\S+)", names: \["(\S+)", "(\S+)"\], pin: "\S+"\}`).FindAllStringSubmatch(readRepoFile(t, advisoryIssueRel), -1) {
			filer = append(filer, m[1]+" "+m[2]+" "+m[3])
		}
		sort.Strings(audit)
		sort.Strings(filer)
		if len(audit) == 0 || !equalStrs(audit, filer) {
			t.Errorf("the filer groups %q, the audit emits %q: a pair the filer does not know files two issues for one pin", filer, audit)
		}
	})
}

// goPinMoveErrors moves the Go toolchain pin from v to v2 in exactly the named files, the way a pull
// request that does only what its issue says would, and returns what TestToolchainParity then reports.
func goPinMoveErrors(t *testing.T, named map[string]bool, v, v2 string) []string {
	t.Helper()
	files := map[string]string{}
	for _, f := range toolchainParityFiles {
		c := readRepoFile(t, f)
		if named[f] {
			c = strings.ReplaceAll(c, "go"+v, "go"+v2)
			c = strings.ReplaceAll(c, "go-version: '"+v+"'", "go-version: '"+v2+"'")
		}
		files[f] = c
	}
	return toolchainParityErrors(files)
}
