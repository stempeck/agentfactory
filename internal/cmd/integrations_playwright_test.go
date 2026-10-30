package cmd

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// The CLI and the MCP server drive one chromium build: no @playwright/mcp release depends on a stable
// playwright-core, and npm does not dedupe the two cores, so the pair is matched on the browser
// revision each core downloads. browsers.json is not in the repo, hence the recorded table.
const (
	pwCLIVersion       = "1.63.0"
	pwMCPPin           = "@playwright/mcp@0.0.80"
	pwChromiumRevision = "1243"
)

var pwCoreRevision = map[string]string{
	pwCLIVersion:              pwChromiumRevision,
	"1.63.0-alpha-2026-08-31": pwChromiumRevision,
}

var (
	pwCLIPinRE             = regexp.MustCompile(`playwright@[0-9]+\.[0-9]+\.[0-9]+`)
	pwMCPPinRE             = regexp.MustCompile(`@playwright/mcp@[^" ]+`)
	pwCheckTimeoutDeclared = regexp.MustCompile(`(?m)^\[check\]\n(?:[^\[\n].*\n|\n)*?timeout[ \t]*=`)
	pwTimeoutCallRE        = regexp.MustCompile(`(?m)(^\s*|[;&|(!]\s*|\bexec\s+|\bthen\s+|\bdo\s+)timeout\s`)
	pwBash32AvoidRE        = regexp.MustCompile(`declare -[Ang]|local -n|mapfile|readarray|\$\{[A-Za-z_]+(,,|\^\^)\}|wait -n|\|&|&>>|coproc|\[\[ -v|%\([^)]*\)T|EPOCHSECONDS|(^|[;&|( ])timeout |tail --pid|(^|[^_[:alnum:]])eval `)
)

func pwPrefixRel() string { return ".local/share/agentfactory/playwright/" + pwCLIVersion }

func pwMCPVersion() string { return strings.TrimPrefix(pwMCPPin, "@playwright/mcp@") }

func pwDistinct(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	for _, m := range re.FindAllString(s, -1) {
		seen[m] = true
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// pwShellCode drops full-line comments, so a comment that explains why `timeout` or npx is gone does
// not read as a call to it.
func pwShellCode(script string) string {
	var b strings.Builder
	for _, l := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

type pwLockEntry struct {
	Version      string            `json:"version"`
	Resolved     string            `json:"resolved"`
	Integrity    string            `json:"integrity"`
	Dependencies map[string]string `json:"dependencies"`
}

type pwLock struct {
	LockfileVersion int                    `json:"lockfileVersion"`
	Packages        map[string]pwLockEntry `json:"packages"`
}

// pwLockResolve finds the lock entry node's resolution reaches for name required from the package at
// key from: its own node_modules first, then each enclosing node_modules up to the prefix root.
func pwLockResolve(pkgs map[string]pwLockEntry, from, name string) string {
	dir := from
	for {
		key := "node_modules/" + name
		if dir != "" {
			key = dir + "/" + key
		}
		if _, ok := pkgs[key]; ok {
			return key
		}
		if dir == "" {
			return ""
		}
		i := strings.LastIndex(dir, "node_modules/")
		dir = strings.TrimSuffix(dir[:max(i, 0)], "/")
	}
}

// pwLockCores walks the dependency graph from one lock entry and returns every playwright-core
// version it reaches.
func pwLockCores(t *testing.T, pkgs map[string]pwLockEntry, from string) []string {
	t.Helper()
	var cores []string
	seen := map[string]bool{}
	queue := []string{from}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if seen[key] {
			continue
		}
		seen[key] = true
		if strings.HasSuffix(key, "node_modules/playwright-core") {
			cores = append(cores, pkgs[key].Version)
		}
		for dep := range pkgs[key].Dependencies {
			next := pwLockResolve(pkgs, key, dep)
			if next == "" {
				t.Errorf("af/package-lock.json: %s depends on %s, which the lock does not contain", key, dep)
				continue
			}
			queue = append(queue, next)
		}
	}
	return cores
}

func TestPlaywrightReference_Pins(t *testing.T) {
	m, dir := refManifest(t, "playwright")
	if m.Scope != "formula" {
		t.Errorf("scope = %q, want formula", m.Scope)
	}
	if m.Install == nil || m.Install.Run != "af/install.sh" || m.Install.Timeout != "20m" {
		t.Errorf("[install] = %+v, want af/install.sh with timeout 20m", m.Install)
	}
	if m.Install != nil && m.Install.Shared {
		t.Error("[install] shared must stay off: each factory installs its own pinned CLI")
	}
	if m.Claude == nil || !slices.Equal(m.Claude.Plugins, []string{"claude-plugin"}) {
		t.Errorf("[claude] = %+v, want plugins [claude-plugin]", m.Claude)
	}
	if m.Check == nil || m.Check.Run != "af/check.sh" {
		t.Errorf("[check] run must be af/check.sh; got %+v", m.Check)
	}
	toml := refRead(t, dir, "af-integration.toml")
	if !pwCheckTimeoutDeclared.MatchString(toml) {
		t.Error("[check] must declare its measured timeout explicitly")
	}

	install := refRead(t, dir, "af/install.sh")
	if got := pwDistinct(pwCLIPinRE, install); len(got) > 0 {
		t.Errorf("install.sh names the registry specs %q; the committed af/package-lock.json must be the only source of the CLI version", got)
	}
	if !strings.Contains(install, "chromium-"+pwChromiumRevision) {
		t.Errorf("install.sh must gate its early return on chromium-%s, the build both cores download", pwChromiumRevision)
	}
	quarantine := topLevelShellFunction(quickstartScriptContent(t), "_quarantine_stale")
	if quarantine == "" {
		t.Error("quickstart.sh has no top-level _quarantine_stale, so nothing keeps the bootstrap from renaming the integration's chromium build")
	} else if !strings.Contains(quarantine, pwChromiumRevision) {
		t.Errorf("quickstart.sh's _quarantine_stale must exempt chromium revision %s, the build this integration's CLI and MCP server share", pwChromiumRevision)
	}

	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "af", "package.json")); err != nil {
		t.Errorf("af/package.json is missing: %v", err)
	} else if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Errorf("af/package.json is not JSON: %v", err)
	}
	wantDeps := map[string]string{"playwright": pwCLIVersion, "@playwright/mcp": pwMCPVersion()}
	if !maps.Equal(pkg.Dependencies, wantDeps) {
		t.Errorf("af/package.json dependencies = %v, want exactly %v", pkg.Dependencies, wantDeps)
	}

	var lock pwLock
	if raw, err := os.ReadFile(filepath.Join(dir, "af", "package-lock.json")); err != nil {
		t.Errorf("af/package-lock.json is missing: %v", err)
	} else if err := json.Unmarshal(raw, &lock); err != nil {
		t.Errorf("af/package-lock.json is not JSON: %v", err)
	}
	if len(lock.Packages) > 0 {
		if lock.LockfileVersion < 2 {
			t.Errorf("af/package-lock.json lockfileVersion = %d; npm ci needs the packages map of v2+", lock.LockfileVersion)
		}
		if root := lock.Packages[""]; !maps.Equal(root.Dependencies, wantDeps) {
			t.Errorf("af/package-lock.json root dependencies = %v, want af/package.json's %v (npm ci refuses a lock out of sync)", root.Dependencies, wantDeps)
		}
		if v := lock.Packages["node_modules/@playwright/mcp"].Version; v != pwMCPVersion() {
			t.Errorf("af/package-lock.json locks @playwright/mcp at %q, want %s", v, pwMCPVersion())
		}
		if v := lock.Packages["node_modules/playwright"].Version; v != pwCLIVersion {
			t.Errorf("af/package-lock.json locks playwright at %q, want %s", v, pwCLIVersion)
		}
		for key, e := range lock.Packages {
			if key == "" {
				continue
			}
			if !strings.HasPrefix(e.Integrity, "sha512-") || !strings.HasPrefix(e.Resolved, "https://registry.npmjs.org/") {
				t.Errorf("af/package-lock.json %s carries resolved=%q integrity=%q; every entry must bind a registry tarball by sha512", key, e.Resolved, e.Integrity)
			}
		}
		for _, from := range []string{"node_modules/playwright", "node_modules/@playwright/mcp"} {
			cores := pwLockCores(t, lock.Packages, from)
			if len(cores) == 0 {
				t.Errorf("af/package-lock.json: %s reaches no playwright-core", from)
			}
			for _, v := range cores {
				if rev, ok := pwCoreRevision[v]; !ok || rev != pwChromiumRevision {
					t.Errorf("af/package-lock.json: %s runs playwright-core %s, which is not recorded as downloading chromium %s", from, v, pwChromiumRevision)
				}
			}
		}
		if key := pwLockResolve(lock.Packages, "node_modules/playwright", "playwright-core"); key == "" || lock.Packages[key].Version != pwCLIVersion {
			t.Errorf("the CLI's playwright-core must be exactly %s (an exact dependency); got %q", pwCLIVersion, lock.Packages[key].Version)
		}
	}

	mcp := refRead(t, dir, "claude-plugin/.mcp.json")
	for rel, body := range map[string]string{"af/install.sh": install, "af/check.sh": refRead(t, dir, "af/check.sh"), "claude-plugin/.mcp.json": mcp, "af-integration.toml": toml} {
		if !strings.Contains(body, pwPrefixRel()) {
			t.Errorf("%s must name the integration's own prefix %s", rel, pwPrefixRel())
		}
	}
	if strings.Contains(mcp, "npx") {
		t.Error("claude-plugin/.mcp.json launches through npx, which resolves the MCP server outside the lock")
	}
	var servers map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(mcp), &servers); err != nil {
		t.Errorf("claude-plugin/.mcp.json is not JSON: %v", err)
	}
	srv, ok := servers["playwright"]
	if !ok {
		t.Error("claude-plugin/.mcp.json declares no playwright server")
	}
	launch := srv.Command + " " + strings.Join(srv.Args, " ")
	for _, want := range []string{pwPrefixRel() + "/node_modules/.bin/playwright-mcp", "--browser chromium", "--headless", "--isolated"} {
		if !strings.Contains(launch, want) {
			t.Errorf("claude-plugin/.mcp.json must launch with %q; launch = %q", want, launch)
		}
	}

	files := refWalk(t, dir)
	for _, rel := range []string{"af/package.json", "af/package-lock.json"} {
		if !slices.Contains(files, rel) {
			t.Errorf("%s does not ship in the reference, so the @latest ban below never reads it", rel)
		}
	}
	var all strings.Builder
	for _, rel := range files {
		body := refRead(t, dir, rel)
		all.WriteString(body)
		if strings.Contains(body, "@latest") {
			t.Errorf("%s names @latest", rel)
		}
	}
	for _, got := range pwDistinct(pwMCPPinRE, all.String()) {
		if got != pwMCPPin {
			t.Errorf("the playwright tree names MCP version %q, want only %s", got, pwMCPPin)
		}
	}
	if name, err := checkClaudePluginJSON("claude-plugin", filepath.Join(dir, "claude-plugin")); err != nil || name != "playwright" {
		t.Errorf("claude-plugin/.claude-plugin/plugin.json name = %q (err %v), want playwright", name, err)
	}
}

func TestPlaywrightCheck_ProbesRenderAndMCPNavigation(t *testing.T) {
	dir := refAcquire(t, "playwright")
	check := refRead(t, dir, "af/check.sh")
	code := pwShellCode(check)
	for _, want := range []string{"screenshot", `"initialize"`, `"tools/list"`, "browser_navigate", "kill -0", "mkfifo", "node_modules/.bin/playwright-mcp"} {
		if !strings.Contains(code, want) {
			t.Errorf("check.sh must contain %q (prefix render probe, then the locked MCP's initialize, tools/list and one navigation, each wait bounded by a kill -0 poll)", want)
		}
	}
	for desc, re := range map[string]*regexp.Regexp{
		"a coreutils timeout call (macOS has none)":  pwTimeoutCallRE,
		"a bash >= 4 or GNU-only construct":          pwBash32AvoidRE,
		"npx (resolves the MCP outside the lock)":    regexp.MustCompile(`\bnpx\b`),
		"a /tmp/ path (/tmp is noexec on the image)": regexp.MustCompile(`/tmp/`),
	} {
		if m := re.FindString(code); m != "" {
			t.Errorf("check.sh contains %s: %q", desc, m)
		}
	}
	bash := "bash"
	if _, err := os.Stat("/bin/bash"); err == nil {
		bash = "/bin/bash"
	}
	if out, err := exec.Command(bash, "-n", filepath.Join(dir, "af", "check.sh")).CombinedOutput(); err != nil {
		t.Errorf("%s -n check.sh: %v\n%s", bash, err, out)
	}
}

// pwPathFakes shadow every PATH route to a playwright: check.sh must run the prefix's binaries, and the
// image's real /usr/bin/playwright and npx must never be reached from a unit test. The sleep override
// keeps the ledger's accounting but also waits a real moment and counts ticks: a sleep that returns at
// once would let a kill -0 poll spend its whole budget before a real child process could answer.
var pwPathFakes = map[string]string{
	"playwright":     `echo "playwright $*" >>"$PIN_DIR/tripwire"; exit 97` + "\n",
	"playwright-mcp": `echo "playwright-mcp $*" >>"$PIN_DIR/tripwire"; exit 97` + "\n",
	"npx":            `echo "npx $*" >>"$PIN_DIR/tripwire"; exit 97` + "\n",
	"sleep": `. "$PIN_DIR/lib.sh"
pin_log sleep "${PIN_BOUND:--}" "$$" "$@"
for a in "$@"; do pin_charge "$a"; done
echo $(( $(cat "$PIN_DIR/ticks" 2>/dev/null || echo 0) + 1 )) >"$PIN_DIR/ticks"
"$PIN_REAL_SLEEP" 0.02
`,
}

// pwPrefixFakes stand in for the two binaries npm ci puts in the prefix. The MCP fake answers the
// request ids listed in $PIN_MCP_ANSWERS; at the first one it does not, it exits, or under
// $PIN_MCP_HANG blocks like a wedged server. $PIN_MCP_SLOW=N answers each request only after check.sh
// has spent N more sleep ticks waiting for it. They run in the background beside check.sh's poll, so
// they never write the budget ledger, whose records would interleave with the poll's sleep records.
var pwPrefixFakes = map[string]string{
	"playwright": `echo $$ >>"$PIN_DIR/pids"
[ -n "${PIN_RENDER_FAIL:-}" ] && exit 1
[ -n "${PIN_RENDER_HANG:-}" ] && exec "$PIN_REAL_SLEEP" 600
[ "$1" = screenshot ] && printf png >"${@: -1}"
exit 0
`,
	"playwright-mcp": `echo $$ >>"$PIN_DIR/pids"
answers=" ${PIN_MCP_ANSWERS:-1 2 3} "
ticks() { cat "$PIN_DIR/ticks" 2>/dev/null || echo 0; }
while IFS= read -r line; do
	for id in 1 2 3; do
		case "$line" in *"\"id\":$id,"*)
			case "$answers" in *" $id "*) ;; *) [ -n "${PIN_MCP_HANG:-}" ] && exec "$PIN_REAL_SLEEP" 600; exit 0 ;; esac
			if [ -n "${PIN_MCP_SLOW:-}" ]; then
				until=$(( $(ticks) + PIN_MCP_SLOW )); n=0
				while [ "$(ticks)" -lt "$until" ] && [ "$n" -lt 3000 ]; do "$PIN_REAL_SLEEP" 0.01; n=$((n + 1)); done
			fi ;;
		esac
	done
	case "$line" in
		*'"id":1,'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake","version":"0"}}}' ;;
		*'"id":2,'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"browser_navigate"}]}}' ;;
		*'"id":3,'*) printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"Page Title: af-check"}]}}' ;;
	esac
done
`,
}

func pwCheckFixture(t *testing.T) refBudgetFixture {
	t.Helper()
	f := newRefBudgetFixture(t, "playwright", pwPathFakes)
	bin := filepath.Join(f.home, filepath.FromSlash(pwPrefixRel()), "node_modules", ".bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range pwPrefixFakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

type pwCheckRun struct {
	refBudgetRun
	wall time.Duration
}

// pwAlive treats a zombie as gone: the container's PID 1 may never reap an orphan, and a zombie runs
// nothing.
func pwAlive(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		if _, perr := os.Stat("/proc/self/stat"); perr == nil {
			return false
		}
		return syscall.Kill(pid, 0) == nil
	}
	s := string(stat)
	if i := strings.LastIndex(s, ")"); i != -1 && i+2 < len(s) {
		return s[i+2] != 'Z' && s[i+2] != 'X'
	}
	return true
}

// pwRunCheck runs check.sh and fails the test if it reached a PATH binary or left one of the prefix
// fakes running after it exited: a hung third-party call must never outlive the script.
func pwRunCheck(t *testing.T, f refBudgetFixture, script string, env ...string) pwCheckRun {
	t.Helper()
	for _, name := range []string{"ticks", "pids", "tripwire"} {
		if err := os.Remove(filepath.Join(f.pin, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	start := time.Now()
	r := pwCheckRun{refBudgetRun: f.run(t, script, 1, env...)}
	r.wall = time.Since(start)
	if tw, _ := os.ReadFile(filepath.Join(f.pin, "tripwire")); len(tw) > 0 {
		t.Errorf("check.sh (%v) ran a PATH binary instead of the prefix's:\n%s", env, tw)
	}
	raw, _ := os.ReadFile(filepath.Join(f.pin, "pids"))
	var pids []int
	for _, field := range strings.Fields(string(raw)) {
		if pid, err := strconv.Atoi(field); err == nil {
			pids = append(pids, pid)
		}
	}
	t.Cleanup(func() {
		for _, pid := range pids {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for _, pid := range pids {
		for pwAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if pwAlive(pid) {
			t.Errorf("check.sh (%v) exited and left pid %d running", env, pid)
		}
	}
	return r
}

// TestPlaywrightCheck_InnerBudgetsFitCheckTimeout: [check] timeout is one deadline around the whole
// script, so the render probe's and the MCP exercise's budgets must fit inside it together, or a slow
// chromium start or a wedged MCP server is reported as a check timeout instead of a specific fail().
// With no coreutils timeout to record a bound, each budget is measured as the poll ticks a hung
// (or slow) fake makes check.sh spend before it gives up.
func TestPlaywrightCheck_InnerBudgetsFitCheckTimeout(t *testing.T) {
	f := pwCheckFixture(t)
	m, err := config.LoadIntegrationManifestNamed(f.dir, "playwright")
	if err != nil {
		t.Fatal(err)
	}
	outer, err := config.ParseIntegrationDuration(m.Check.Timeout)
	if err != nil {
		t.Fatal(err)
	}
	if r := pwRunCheck(t, f, m.Check.Run); r.exit != 0 {
		t.Fatalf("check.sh must pass against answering fakes; exit=%d\n%s", r.exit, r.out)
	}

	bounded := func(r pwCheckRun, env []string, want string) time.Duration {
		t.Helper()
		if want != "" && (r.exit != 1 || !strings.Contains(r.out, want)) {
			t.Errorf("check.sh %v must fail with %q; exit=%d\n%s", env, want, r.exit, r.out)
		}
		if r.wall > 30*time.Second {
			t.Errorf("check.sh %v took %s of wall time against a fake that hangs; its waits are not bounded", env, r.wall)
		}
		worst, items := r.worstCase(t)
		t.Logf("check.sh %v: worst case %s\n\t%s", env, worst, strings.Join(items, "\n\t"))
		return worst
	}

	renderEnv := []string{"PIN_RENDER_HANG=1"}
	render := bounded(pwRunCheck(t, f, m.Check.Run, renderEnv...), renderEnv, "chromium did not render the probe page")
	var mcp time.Duration
	for _, tc := range []struct{ answers, want string }{
		{"none", "did not answer initialize"},
		{"1", "did not answer tools/list"},
		{"1 2", "did not answer the browser_navigate call"},
	} {
		env := []string{"PIN_MCP_HANG=1", "PIN_MCP_ANSWERS=" + tc.answers}
		mcp = max(mcp, bounded(pwRunCheck(t, f, m.Check.Run, env...), env, tc.want))
	}
	// A server that answers every request just inside a per-request budget still overruns the outer
	// deadline unless initialize, tools/list and the navigation share one budget.
	slowEnv := []string{"PIN_MCP_SLOW=61"}
	mcp = max(mcp, bounded(pwRunCheck(t, f, m.Check.Run, slowEnv...), slowEnv, ""))

	if worst := render + mcp + 2*refBudgetTermGrace; refBudgetExceeds(worst, outer) {
		t.Errorf("af/check.sh worst-case inner budget %s (render %s + MCP %s + 2x%s TERM grace) + %s wait delay exceeds [check] timeout %s: a slow chromium start or a wedged MCP server is reported \"[check] timed out after %s\" instead of a specific fail()",
			worst, render, mcp, refBudgetTermGrace, integrationWaitDelay, outer, outer)
	}
}

func TestPlaywrightCheck_FailsWithSpecificDiagnostic(t *testing.T) {
	f := pwCheckFixture(t)
	for _, tc := range []struct{ name, env, want string }{
		{"render", "PIN_RENDER_FAIL=1", "playwright: chromium did not render the probe page"},
		{"initialize", "PIN_MCP_ANSWERS=none", "did not answer initialize"},
		{"tools_list", "PIN_MCP_ANSWERS=1", "did not answer tools/list"},
		{"navigate", "PIN_MCP_ANSWERS=1 2", "did not answer the browser_navigate call"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pwRunCheck(t, f, "af/check.sh", tc.env)
			if r.exit != 1 || !strings.Contains(r.out, tc.want) {
				t.Errorf("check.sh must fail with %q; exit=%d\n%s", tc.want, r.exit, r.out)
			}
		})
	}
}

type pwInstallCase struct {
	prefixVersion string // the prefix CLI's version before the run; "" = no prefix CLI
	prefixLock    string // "shipped" = the prefix holds copies of af/package*.json; "stale" = a different lock; "" = none
	pathVersion   string // a global playwright on PATH; "" = none
}

// pwRunInstall drives install.sh with fakes for npm, sudo and the CLIs. The fake npm ci refuses
// (EUSAGE) unless the prefix holds byte-equal copies of the shipped af/package*.json, as the real one
// does when they are absent, then "installs" a prefix CLI reporting pwCLIVersion.
func pwRunInstall(t *testing.T, c pwInstallCase) (refRunResult, string, string) {
	t.Helper()
	dir := refAcquire(t, "playwright")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cache", "ms-playwright", "chromium-"+pwChromiumRevision), 0o755); err != nil {
		t.Fatal(err)
	}
	callLog := filepath.Join(home, "calls.log")
	prefix := filepath.Join(home, filepath.FromSlash(pwPrefixRel()))
	prefixState := filepath.Join(home, "prefix-pwver")
	tmpl := filepath.Join(home, "prefix-playwright")
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(tmpl, `#!/bin/bash
echo "prefix-playwright $*" >> "`+callLog+`"
case "$1" in
  --version) echo "Version $(cat "`+prefixState+`")" ;;
  screenshot) printf 'png' > "${@: -1}" ;;
esac
exit 0
`, 0o755)
	if c.prefixVersion != "" {
		write(prefixState, c.prefixVersion+"\n", 0o644)
		body, _ := os.ReadFile(tmpl)
		write(filepath.Join(prefix, "node_modules", ".bin", "playwright"), string(body), 0o755)
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		switch c.prefixLock {
		case "shipped":
			if body, err := os.ReadFile(filepath.Join(dir, "af", name)); err == nil {
				write(filepath.Join(prefix, name), string(body), 0o644)
			}
		case "stale":
			write(filepath.Join(prefix, name), "{}\n", 0o644)
		}
	}
	npmRoot := filepath.Join(home, "npm-global")
	if err := os.MkdirAll(npmRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	fakes := map[string]string{
		"npm": `echo "npm $*" >> "` + callLog + `"
case "$1" in
  root) echo "` + npmRoot + `" ;;
  ci)
    p=""; prev=""
    for a in "$@"; do
      [ "$prev" = --prefix ] && p="$a"
      case "$a" in --prefix=*) p="${a#--prefix=}" ;; esac
      prev="$a"
    done
    for f in package.json package-lock.json; do
      if [ -z "$p" ] || ! cmp -s "$p/$f" "` + filepath.Join(dir, "af") + `/$f"; then
        echo "npm error code EUSAGE"; echo "npm error npm ci can only install with an existing package-lock.json"; exit 1
      fi
    done
    mkdir -p "$p/node_modules/.bin"
    cp "` + tmpl + `" "$p/node_modules/.bin/playwright"
    printf '#!/bin/bash\nexit 0\n' > "$p/node_modules/.bin/playwright-mcp"
    chmod 0755 "$p/node_modules/.bin/playwright" "$p/node_modules/.bin/playwright-mcp"
    echo "` + pwCLIVersion + `" > "` + prefixState + `" ;;
esac
exit 0
`,
		"npx":  `echo "npx $*" >> "` + callLog + `"` + "\nexit 0\n",
		"sudo": "exit 1\n",
	}
	// refEnv keeps /usr/bin on PATH, where the image ships a real global playwright; "no global CLI"
	// is modelled as a PATH playwright that cannot run.
	fakes["playwright"] = `echo "path-playwright $*" >> "` + callLog + `"` + "\nexit 127\n"
	if c.pathVersion != "" {
		fakes["playwright"] = `echo "path-playwright $*" >> "` + callLog + `"
case "$1" in
  --version) echo "Version ` + c.pathVersion + `" ;;
  screenshot) printf 'png' > "${@: -1}" ;;
esac
exit 0
`
	}
	res := refRun(t, dir, refEnv(home, refFakeBin(t, fakes)), "", filepath.Join(dir, "af", "install.sh"))
	calls, _ := os.ReadFile(callLog)
	return res, string(calls), prefix
}

func TestPlaywrightInstall_VersionGatedEarlyReturn(t *testing.T) {
	converges := func(t *testing.T, c pwInstallCase, why string) {
		t.Helper()
		res, calls, prefix := pwRunInstall(t, c)
		if res.exit != 0 {
			t.Errorf("install.sh exit=%d; output:\n%s%s", res.exit, res.stdout, res.stderr)
		}
		if want := "npm ci --ignore-scripts --prefix " + prefix; !strings.Contains(calls, want) {
			t.Errorf("%s, install.sh must install the lock with %q; calls:\n%s", why, want, calls)
		}
		if strings.Contains(calls, "npm install") {
			t.Errorf("install.sh must never npm install (the lock is installed by npm ci into the prefix); calls:\n%s", calls)
		}
		if !regexp.MustCompile(`(?m)^prefix-playwright install .*chromium`).MatchString(calls) {
			t.Errorf("install.sh must download chromium with the prefix's own CLI, which registers the build for that CLI; calls:\n%s", calls)
		}
		if strings.Contains(calls, "path-playwright install") {
			t.Errorf("install.sh drove the global playwright on PATH; calls:\n%s", calls)
		}
	}
	t.Run("pinned_version_present", func(t *testing.T) {
		res, calls, _ := pwRunInstall(t, pwInstallCase{prefixVersion: pwCLIVersion, prefixLock: "shipped"})
		if res.exit != 0 {
			t.Errorf("install.sh exit=%d; output:\n%s%s", res.exit, res.stdout, res.stderr)
		}
		for _, banned := range []string{"npm ci", "npm install", "playwright install"} {
			if strings.Contains(calls, banned) {
				t.Errorf("with the prefix CLI at %s, its lock and chromium-%s present, install.sh must return early (found %q); calls:\n%s", pwCLIVersion, pwChromiumRevision, banned, calls)
			}
		}
	})
	t.Run("other_version_present", func(t *testing.T) {
		converges(t, pwInstallCase{prefixVersion: "1.50.0"}, "with the prefix CLI at 1.50.0")
	})
	t.Run("global_cli_only", func(t *testing.T) {
		converges(t, pwInstallCase{pathVersion: pwCLIVersion}, "with only a global playwright "+pwCLIVersion+" on PATH (never adopted)")
	})
	t.Run("prefix_lock_differs", func(t *testing.T) {
		converges(t, pwInstallCase{prefixVersion: pwCLIVersion, prefixLock: "stale"}, "with a prefix lock that differs from the shipped one")
	})
}
