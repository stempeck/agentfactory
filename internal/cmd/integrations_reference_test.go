package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

var refIntegrationNames = []string{"aws-agents", "playwright"}

// refAcquire materialises an embedded reference exactly as af plugin acquire does (manifest validation
// and exec modes included) into a temp dir, so its scripts run the way an installed copy runs them.
func refAcquire(t *testing.T, name string) string {
	t.Helper()
	sub, err := fs.Sub(embeddedIntegrations, "integrations")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := copyEmbeddedIntegration(sub, name, dir); err != nil {
		t.Fatalf("acquiring the embedded %s reference: %v", name, err)
	}
	return dir
}

func refManifest(t *testing.T, name string) (*config.IntegrationManifest, string) {
	t.Helper()
	dir := refAcquire(t, name)
	m, err := config.LoadIntegrationManifestNamed(dir, name)
	if err != nil {
		t.Fatalf("loading the embedded %s manifest: %v", name, err)
	}
	return m, dir
}

func refRead(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

// refFakeBin writes executable stand-ins for the tools a reference script calls, so a test can drive
// the script without network, a gateway or a browser.
func refFakeBin(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fakebin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type refRunResult struct {
	stdout, stderr string
	exit           int
}

// refRun runs a reference script with exactly env (nothing inherited), so the operator's HOME, PATH
// and Claude Code config can never leak into, or be touched by, the script under test.
func refRun(t *testing.T, cwd string, env []string, stdin string, argv ...string) refRunResult {
	t.Helper()
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = cwd
	c.Env = env
	c.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	err := c.Run()
	res := refRunResult{stdout: out.String(), stderr: errb.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		res.exit = exitErr.ExitCode()
	case err != nil:
		t.Fatalf("running %v: %v", argv, err)
	}
	return res
}

func refEnv(home, fakeBin string, extra ...string) []string {
	return append([]string{
		"HOME=" + home,
		"PATH=" + fakeBin + ":/usr/bin:/bin",
		"TMPDIR=" + home,
		"LANG=C",
	}, extra...)
}

func refWalk(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// The budget ledger replaces every bounding tool a reference script uses (timeout, curl --max-time,
// sleep) with a fake that records the bound it was given and returns at once, as if that bound had been
// used up. Summing the bounds the script actually reached, along the path it actually took, is the
// script's worst-case wall time without spending it. A bound nested inside another is charged to the
// outer one (a virtual clock that ends the enclosing timeout when it runs out), so a loop wrapped in
// a timeout costs that timeout, not the sum of its iterations.
const refBudgetLib = `
pin_log() { printf '%s\0' "$@" >>"$PIN_BUDGET_LOG"; printf '\036' >>"$PIN_BUDGET_LOG"; }
pin_secs() { awk -v d="$1" 'BEGIN{m=1; u=substr(d,length(d)); if(u=="s"){d=substr(d,1,length(d)-1)} else if(u=="m"){m=60;d=substr(d,1,length(d)-1)} else if(u=="h"){m=3600;d=substr(d,1,length(d)-1)} else if(u=="d"){m=86400;d=substr(d,1,length(d)-1)}; printf "%.3f", d*m}'; }
pin_charge() {
	[ -n "${PIN_BOUND:-}" ] || return 0
	local f="$PIN_DIR/spent.$PIN_BOUND" spent
	spent=$(awk -v a="$(cat "$f" 2>/dev/null || echo 0)" -v b="$(pin_secs "$1")" 'BEGIN{printf "%.3f", a+b}')
	echo "$spent" >"$f"
	if awk -v s="$spent" -v l="$(pin_secs "$PIN_BOUND_LIMIT")" 'BEGIN{exit !(s>=l)}'; then
		kill -TERM "$PIN_BOUND" 2>/dev/null
		exit 124
	fi
}
`

// refBudgetBoundingFakes stand in for the bounding tools. The fake timeout still wraps its command in
// the real timeout (when there is one) for 20s of wall time, so a regression hangs briefly, not forever.
var refBudgetBoundingFakes = map[string]string{
	"timeout": `. "$PIN_DIR/lib.sh"
ka=0
while [ $# -gt 0 ]; do
	case "$1" in
		-k|--kill-after) ka="$2"; shift 2 ;;
		--kill-after=*) ka="${1#*=}"; shift ;;
		-k*) ka="${1#-k}"; shift ;;
		-s|--signal) shift 2 ;;
		--signal=*|-s*|--preserve-status|--foreground|-v|--verbose) shift ;;
		--) shift; break ;;
		*) break ;;
	esac
done
dur="$1"; shift
pin_log timeout "${PIN_BOUND:--}" "$$" "$dur" "$ka" "$@"
export PIN_BOUND=$$ PIN_BOUND_LIMIT="$dur"
if [ -n "${PIN_REAL_TIMEOUT:-}" ]; then exec "$PIN_REAL_TIMEOUT" 20 "$@"; fi
exec "$@"
`,
	"curl": `. "$PIN_DIR/lib.sh"
pin_log curl "${PIN_BOUND:--}" "$$" "$@"
mt=0; out=""; url=""
while [ $# -gt 0 ]; do
	case "$1" in
		--max-time|-m) mt="$2"; shift 2 ;;
		--max-time=*) mt="${1#*=}"; shift ;;
		-o|--output) out="$2"; shift 2 ;;
		-*) shift ;;
		*) url="$1"; shift ;;
	esac
done
pin_charge "$mt"
case "$url" in
	*/health)
		n=$(( $(cat "$PIN_DIR/health" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$PIN_DIR/health"
		[ "$PIN_HEALTH_UP_AT" -gt 0 ] && [ "$n" -ge "$PIN_HEALTH_UP_AT" ] && exit 0
		exit 7 ;;
	*) [ -n "$out" ] && cp "$PIN_DIR/download" "$out"; exit 0 ;;
esac
`,
	"sleep": `. "$PIN_DIR/lib.sh"
pin_log sleep "${PIN_BOUND:--}" "$$" "$@"
for a in "$@"; do pin_charge "$a"; done
`,
}

// GNU timeout without -k only sends SIGTERM when its bound runs out, and the child outlives the bound
// for as long as it takes to shut down. Each such step is charged this grace, so the relation holds
// with a margin rather than at equality.
const refBudgetTermGrace = 5 * time.Second

var refBudgetDurationRE = regexp.MustCompile(`^([0-9]*\.?[0-9]+)([smhd]?)$`)

type refBudgetCall struct {
	kind, parent string
	args         []string
}

type refBudgetRun struct {
	exit  int
	out   string
	calls []refBudgetCall
}

type refBudgetFixture struct {
	dir, home, pin string
	env            []string
}

// newRefBudgetFixture acquires the reference and puts the bounding fakes, plus the integration's own
// fakes, ahead of PATH. The fakes keep the ledger and their state under f.pin. sleep itself is faked,
// so a fake that must really block uses $PIN_REAL_SLEEP.
func newRefBudgetFixture(t *testing.T, integration string, fakes map[string]string, extraEnv ...string) refBudgetFixture {
	t.Helper()
	base := t.TempDir()
	f := refBudgetFixture{dir: refAcquire(t, integration), home: filepath.Join(base, "home"), pin: filepath.Join(base, "pin")}
	for _, d := range []string{f.home, f.pin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.pin, "lib.sh"), []byte(refBudgetLib), 0o644); err != nil {
		t.Fatal(err)
	}
	realTimeout, _ := exec.LookPath("timeout")
	realSleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	all := maps.Clone(refBudgetBoundingFakes)
	maps.Copy(all, fakes)
	f.env = refEnv(f.home, refFakeBin(t, all), append([]string{
		"PIN_DIR=" + f.pin,
		"PIN_BUDGET_LOG=" + filepath.Join(f.pin, "ledger"),
		"PIN_REAL_TIMEOUT=" + realTimeout,
		"PIN_REAL_SLEEP=" + realSleep,
	}, extraEnv...)...)
	return f
}

// run executes script from a fresh ledger. The gateway health URL answers from probe number healthUpAt
// on (0: never).
func (f refBudgetFixture) run(t *testing.T, script string, healthUpAt int, extraEnv ...string) refBudgetRun {
	t.Helper()
	stale, _ := filepath.Glob(filepath.Join(f.pin, "spent.*"))
	for _, p := range append(stale, filepath.Join(f.pin, "ledger"), filepath.Join(f.pin, "health")) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	env := append(slices.Clone(f.env), "PIN_HEALTH_UP_AT="+strconv.Itoa(healthUpAt))
	r := refRun(t, f.dir, append(env, extraEnv...), "", filepath.Join(f.dir, script))
	data, err := os.ReadFile(filepath.Join(f.pin, "ledger"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	res := refBudgetRun{exit: r.exit, out: r.stdout + r.stderr}
	for _, rec := range strings.Split(string(data), "\x1e") {
		if rec == "" {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(rec, "\x00"), "\x00")
		res.calls = append(res.calls, refBudgetCall{kind: fields[0], parent: fields[1], args: fields[3:]})
	}
	return res
}

func (r refBudgetRun) count(kind string, match func(refBudgetCall) bool) int {
	n := 0
	for _, c := range r.calls {
		if c.kind == kind && (match == nil || match(c)) {
			n++
		}
	}
	return n
}

// worstCase sums the top-level bounds (those not already inside another bound) and itemises them,
// grouped, so a failure names which budgets the sum is made of. A top-level curl with no --max-time is
// unbounded, so it makes the sum infinite.
func (r refBudgetRun) worstCase(t *testing.T) (time.Duration, []string) {
	t.Helper()
	var total time.Duration
	var order []string
	type group struct {
		n   int
		sum time.Duration
	}
	groups := map[string]group{}
	for _, c := range r.calls {
		if c.parent != "-" {
			continue
		}
		var cost time.Duration
		var what string
		switch c.kind {
		case "timeout":
			cost = refBudgetSeconds(t, c.args[0]) + refBudgetSeconds(t, c.args[1])
			cmd := slices.Clone(c.args[2:min(len(c.args), 4)])
			for i, a := range cmd {
				cmd[i] = filepath.Base(a)
			}
			what = "timeout " + c.args[0] + " " + strings.Join(cmd, " ")
			if c.args[1] == "0" {
				cost += refBudgetTermGrace
				what += fmt.Sprintf(" (+%s TERM grace)", refBudgetTermGrace)
			} else {
				what = "timeout -k " + c.args[1] + " " + strings.TrimPrefix(what, "timeout ")
			}
		case "curl":
			mt, ok := refBudgetCurlMaxTime(c.args)
			if !ok {
				return time.Duration(math.MaxInt64), []string{"curl with no --max-time: " + strings.Join(c.args, " ")}
			}
			cost = refBudgetSeconds(t, mt)
			what = "curl --max-time " + mt + " " + filepath.Base(c.args[len(c.args)-1])
		case "sleep":
			for _, a := range c.args {
				cost += refBudgetSeconds(t, a)
			}
			what = "sleep " + strings.Join(c.args, " ")
		default:
			continue
		}
		total += cost
		g, seen := groups[what]
		if !seen {
			order = append(order, what)
		}
		groups[what] = group{n: g.n + 1, sum: g.sum + cost}
	}
	items := make([]string, 0, len(order))
	for _, what := range order {
		items = append(items, fmt.Sprintf("%-60s x%-3d = %s", what, groups[what].n, groups[what].sum))
	}
	return total, items
}

// refBudgetExceeds reports whether worst, plus the wait delay af allows past its deadline, overruns outer.
// It subtracts rather than adds: worstCase's unbounded sentinel plus the delay would wrap negative and fit.
func refBudgetExceeds(worst, outer time.Duration) bool {
	return worst > outer-integrationWaitDelay
}

// unbounded names each call of the given kinds that ran outside every bound.
func (r refBudgetRun) unbounded(kinds ...string) []string {
	var out []string
	for _, c := range r.calls {
		if slices.Contains(kinds, c.kind) && c.parent == "-" {
			out = append(out, c.kind+" "+strings.Join(c.args[:min(len(c.args), 3)], " "))
		}
	}
	return out
}

func refBudgetSeconds(t *testing.T, d string) time.Duration {
	t.Helper()
	m := refBudgetDurationRE.FindStringSubmatch(d)
	if m == nil {
		t.Fatalf("unparseable bound %q", d)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("unparseable bound %q: %v", d, err)
	}
	unit := map[string]time.Duration{"": time.Second, "s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	return time.Duration(n * float64(unit))
}

func refBudgetCurlMaxTime(args []string) (string, bool) {
	for i, a := range args {
		switch {
		case (a == "--max-time" || a == "-m") && i+1 < len(args):
			return args[i+1], true
		case strings.HasPrefix(a, "--max-time="):
			return strings.TrimPrefix(a, "--max-time="), true
		}
	}
	return "", false
}

// TestRefBudget_UnboundedCurlExceedsEveryDeadline: worstCase reports a curl with no --max-time as the
// largest Duration, so the budget check must treat it as overrunning any deadline, never as fitting one.
func TestRefBudget_UnboundedCurlExceedsEveryDeadline(t *testing.T) {
	curl := func(args ...string) refBudgetRun {
		return refBudgetRun{calls: []refBudgetCall{{kind: "curl", parent: "-", args: append(args, "https://example.invalid/install.sh")}}}
	}
	for _, outer := range []time.Duration{3 * time.Minute, 25 * time.Minute, 24 * time.Hour} {
		if worst, items := curl("-fsSL", "-o", "install.sh").worstCase(t); !refBudgetExceeds(worst, outer) {
			t.Errorf("a curl with no --max-time fits a %s deadline: worst=%s %q", outer, worst, items)
		}
		if worst, items := curl("-fsSL", "--max-time", "60", "-o", "install.sh").worstCase(t); refBudgetExceeds(worst, outer) {
			t.Errorf("a curl bounded at 60s overruns a %s deadline: worst=%s %q", outer, worst, items)
		}
	}
}

func TestEmbeddedIntegrations_ThreeReferencesBesideReadme(t *testing.T) {
	entries, err := fs.ReadDir(embeddedIntegrations, "integrations")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"README.md", "aws-agents", "playwright"}; !slices.Equal(names, want) {
		t.Errorf("internal/cmd/integrations must hold the README beside the three references; got %q, want %q", names, want)
	}
}

// TestEmbeddedIntegrations_PassInstallTimeRules: each reference survives what acquire and install check
// before anything runs: the manifest validates under its directory name, every declared script exists,
// is executable and has a shebang, and every shipped Claude Code plugin dir passes the plugin.json rules.
func TestEmbeddedIntegrations_PassInstallTimeRules(t *testing.T) {
	for _, name := range refIntegrationNames {
		t.Run(name, func(t *testing.T) {
			m, dir := refManifest(t, name)
			var runs []string
			if m.Install != nil {
				runs = append(runs, m.Install.Run)
			}
			if m.Check != nil {
				runs = append(runs, m.Check.Run)
			}
			if m.Service != nil {
				runs = append(runs, m.Service.Run)
			}
			if m.Check == nil {
				t.Errorf("%s declares no [check]; every reference reports its health", name)
			}
			for _, run := range runs {
				p := filepath.Join(dir, filepath.FromSlash(run))
				info, err := os.Stat(p)
				if err != nil {
					t.Errorf("declared script %s is missing: %v", run, err)
					continue
				}
				if info.Mode().Perm()&0o111 == 0 {
					t.Errorf("declared script %s is not executable after acquire", run)
				}
				if !strings.HasPrefix(refRead(t, dir, run), "#!") {
					t.Errorf("declared script %s has no shebang", run)
				}
			}
			if m.Claude == nil || len(m.Claude.Plugins) == 0 {
				t.Fatalf("%s binds no Claude Code plugin dir", name)
			}
			for _, rel := range m.Claude.Plugins {
				if strings.HasPrefix(rel, ".upstream/") {
					continue
				}
				if _, err := checkClaudePluginJSON(rel, filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
					t.Errorf("plugin dir %s: %v", rel, err)
				}
			}
		})
	}
}

// TestEmbeddedIntegrations_ScriptsParseAndStayOffTmp: every shell file in the references parses, and no
// script writes under /tmp: install and check run inside the acquisition and snapshot dirs.
func TestEmbeddedIntegrations_ScriptsParseAndStayOffTmp(t *testing.T) {
	for _, name := range refIntegrationNames {
		t.Run(name, func(t *testing.T) {
			dir := refAcquire(t, name)
			scripts := 0
			for _, rel := range refWalk(t, dir) {
				body := refRead(t, dir, rel)
				first, _, _ := strings.Cut(body, "\n")
				if !strings.HasPrefix(first, "#!") || !strings.Contains(first, "sh") {
					continue
				}
				scripts++
				if out, err := exec.Command("bash", "-n", filepath.Join(dir, rel)).CombinedOutput(); err != nil {
					t.Errorf("%s does not parse: %v\n%s", rel, err, out)
				}
				if strings.Contains(body, "/tmp/") {
					t.Errorf("%s writes under /tmp; keep scratch inside the run dir or $TMPDIR", rel)
				}
			}
			if scripts == 0 {
				t.Errorf("%s ships no shell script", name)
			}
		})
	}
}

func TestEmbeddedIntegrationsReadme_ListsEachReference(t *testing.T) {
	b, err := fs.ReadFile(embeddedIntegrations, "integrations/README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	for _, name := range refIntegrationNames {
		if !regexp.MustCompile("(?m)^- `" + regexp.QuoteMeta(name) + "/`").MatchString(readme) {
			t.Errorf("README.md must list the %s reference as a `- `%s/`` line", name, name)
		}
	}
	if strings.Contains(readme, "no reference integration ships in it yet") {
		t.Error("README.md still says no reference integration ships in the directory")
	}
}

func TestAWSAgentsReference_Shape(t *testing.T) {
	m, dir := refManifest(t, "aws-agents")
	if m.Scope != config.IntegrationScopeFormula {
		t.Errorf("aws-agents scope = %q, want formula", m.Scope)
	}
	if m.Upstream == nil || m.Upstream.Repo != "https://github.com/aws/agent-toolkit-for-aws" {
		t.Errorf("aws-agents [upstream] repo = %+v, want https://github.com/aws/agent-toolkit-for-aws", m.Upstream)
	}
	if m.Claude == nil || !slices.Equal(m.Claude.Plugins, []string{".upstream/plugins/aws-agents"}) {
		t.Errorf("aws-agents [claude] plugins = %+v, want [.upstream/plugins/aws-agents]", m.Claude)
	}
	if m.Install != nil || m.Service != nil {
		t.Errorf("aws-agents is manifest-only: no [install] and no [service]; got install=%+v service=%+v", m.Install, m.Service)
	}
	if m.Check == nil || m.Check.Run != "af/check.sh" {
		t.Fatalf("aws-agents [check] run must be af/check.sh; got %+v", m.Check)
	}
	check := refRead(t, dir, "af/check.sh")
	if !strings.Contains(check, "https://knowledge-mcp.global.api.aws") {
		t.Error("aws-agents check.sh must probe https://knowledge-mcp.global.api.aws")
	}
	if !strings.Contains(check, "--max-time") {
		t.Error("aws-agents check.sh must bound its probe with curl --max-time")
	}
}
