//go:build integration

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestPluginInstallBehavioral_AcquireListInstallResolve is the #538 Phase-5a behavioral
// proof (six_sigma_gaps Gap 11): unit tests prove `af plugin install` DISPATCHED to the
// pipeline; this proves a plugin's agent is actually built, EMBEDDED, and resolvable.
//
// Role templates embed at COMPILE time (//go:embed, internal/templates/templates.go), so
// "embedded / prime non-fallback / sling resolves" can only be shown against a binary
// REBUILT with the plugin template present. To stay non-destructive this drives the REAL
// `af plugin install` against an ISOLATED source copy (git archive HEAD) whose quickstart.sh
// is a no-op and whose Makefile `install` target is a minimal `go build` — the design-doc
// Phase-5 "pipeline-through-agent-gen-all + seamed quickstart" fallback. The seed binary's
// bare `af` on PATH serves agent-gen-all.sh's pre-rebuild calls; the pipeline's own make
// install deposits the freshly-embedded binary at $HOME/.local/bin/af, which then answers
// the K9 verify + every downstream assertion. The real working tree is never touched.
func TestPluginInstallBehavioral_AcquireListInstallResolve(t *testing.T) {
	requirePython3WithServerDeps(t) // sling --agent instantiates a formula via the store
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}

	seedBinary := buildAF(t) // stale embed (plugin template does not exist yet); also PATH seed `af`
	seedBinDir := filepath.Dir(seedBinary)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	// git init + an initial commit — dispatchToSpecialist creates a git worktree, which
	// requires a valid HEAD.
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@e2e.test"},
		{"config", "user.name", "E2E"},
	} {
		c := exec.Command("git", args...)
		c.Dir = workspace
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}
	runAF(t, seedBinary, workspace, "install", "--init")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init factory"}} {
		c := exec.Command("git", args...)
		c.Dir = workspace
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// Drop the shipped store formulas install --init staged so the pipeline's regen loop
	// only touches the two trivial formulas below (fast + hermetic).
	storeDir := config.FormulasDir(workspace)
	if entries, err := os.ReadDir(storeDir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".formula.toml") {
				_ = os.Remove(filepath.Join(storeDir, e.Name()))
			}
		}
	}

	// --- ISOLATED buildable AF source copy: git archive HEAD → tar -x. The pipeline mutates
	// ONLY this copy (agent-gen writes the plugin template here; the minimal Makefile rebuilds
	// af from here). Trim install_formulas to one trivial skill-free formula so the //go:embed
	// stays non-empty and the sync/regen stays tiny; no-op quickstart; minimal make install.
	afSrc := t.TempDir()
	behavioralArchiveInto(t, afSrc)
	behavioralTrimInstallFormulas(t, afSrc)
	behavioralWriteFile(t, filepath.Join(afSrc, "quickstart.sh"), "#!/usr/bin/env bash\nexit 0\n", 0o755)
	behavioralWriteFile(t, filepath.Join(afSrc, "Makefile"),
		"install:\n\tmkdir -p $(HOME)/.local/bin\n\tCGO_ENABLED=0 go build -o $(HOME)/.local/bin/af ./cmd/af\n", 0o644)

	// --- acquire: inert clone into store/plugins/acmeplugin/ (nothing staged yet).
	const pluginName, agentName = "acmeplugin", "acme-triage"
	writePluginFixture(t, workspace, pluginName, map[string]string{
		agentName + ".formula.toml": validPluginFormula(agentName),
	})
	if _, err := os.Stat(filepath.Join(storeDir, agentName+".formula.toml")); err == nil {
		t.Fatal("acquisition must be inert — nothing may be staged before install")
	}

	freshAF := filepath.Join(workspace, ".local", "bin", "af")
	// HOME=workspace isolates the FACTORY state, but it also reroutes the Go
	// module cache the pipeline's `make install` populates to $HOME/go/pkg/mod —
	// under this t.TempDir(). Module-cache files are read-only, so t.TempDir()
	// cleanup's RemoveAll fails ("permission denied") and marks the test FAILED
	// even when every assertion passed. Pin the toolchain caches to the shared
	// ambient ones (outside the temp dir) so the build reuses them and leaves
	// nothing read-only to remove.
	env := append(os.Environ(),
		"HOME="+workspace,
		"AF_SOURCE_ROOT="+afSrc,
		"GOMODCACHE="+behavioralGoEnv(t, "GOMODCACHE"),
		"GOCACHE="+behavioralGoEnv(t, "GOCACHE"),
		"PATH="+filepath.Join(workspace, ".local", "bin")+":"+seedBinDir+":"+os.Getenv("PATH"),
	)

	// --- list: acme-triage shows status ok + the third-party review label.
	listOut, err := behavioralRunAF(t, seedBinary, workspace, env, "plugin", "list")
	if err != nil {
		t.Fatalf("plugin list: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, agentName) || !strings.Contains(listOut, "[ok]") {
		t.Fatalf("plugin list missing %s [ok]:\n%s", agentName, listOut)
	}
	if !strings.Contains(listOut, "third-party") {
		t.Fatalf("plugin list missing the third-party review label:\n%s", listOut)
	}

	// --- install: real agent-gen-all.sh writes the template + minimal make install rebuilds
	// af (embedding it) into workspace/.local/bin; no-op quickstart; K9 verify execs the FRESH
	// af (embedded=true) → exit 0.
	installOut, err := behavioralRunAF(t, seedBinary, workspace, env, "plugin", "install", pluginName)
	if err != nil {
		t.Fatalf("plugin install failed:\n%s", installOut)
	}
	if !strings.Contains(installOut, "plugin install verified") {
		t.Fatalf("plugin install did not report verification:\n%s", installOut)
	}
	if _, err := os.Stat(freshAF); err != nil {
		t.Fatalf("pipeline did not build the fresh binary at %s: %v", freshAF, err)
	}

	// --- registered in agents.json with a formula field (agent-gen ran in the pipeline).
	cfg, cerr := config.LoadAgentConfig(config.AgentsConfigPath(workspace))
	if cerr != nil {
		t.Fatalf("load agents.json: %v", cerr)
	}
	ae, ok := cfg.Agents[agentName]
	if !ok || ae.Formula == "" {
		t.Fatalf("agent %q not registered with a formula field: %+v", agentName, ae)
	}

	// --- staged store formula + recorded manifest.
	if _, e := os.Stat(filepath.Join(storeDir, agentName+".formula.toml")); e != nil {
		t.Fatalf("store formula not staged: %v", e)
	}
	man, merr := config.LoadPluginsConfig(config.PluginsConfigPath(workspace))
	if merr != nil {
		t.Fatalf("load plugins.json: %v", merr)
	}
	if _, ok := man.Plugins[pluginName]; !ok {
		t.Fatalf("plugins.json did not record plugin %q", pluginName)
	}

	// --- role template written into the source copy + rendered workspace CLAUDE.md.
	if _, e := os.Stat(filepath.Join(afSrc, "internal", "templates", "roles", agentName+".md.tmpl")); e != nil {
		t.Fatalf("role template not written to the source copy: %v", e)
	}
	if _, e := os.Stat(filepath.Join(config.AgentDir(workspace, agentName), "CLAUDE.md")); e != nil {
		t.Fatalf("agent workspace CLAUDE.md not rendered: %v", e)
	}

	// --- embedded: `af plugin verify --json` against the FRESH binary → embedded ∧ registered
	// ∧ hash-clean ∧ ok.
	verifyOut, verr := behavioralRunAF(t, freshAF, workspace, env, "plugin", "verify", "--json", pluginName)
	if verr != nil {
		t.Fatalf("plugin verify (fresh binary) failed:\n%s", verifyOut)
	}
	var vp struct {
		OK      bool `json:"ok"`
		Results []struct {
			Agent      string `json:"agent"`
			Registered bool   `json:"registered"`
			Embedded   bool   `json:"embedded"`
			HashClean  bool   `json:"hash_clean"`
			OK         bool   `json:"ok"`
		} `json:"results"`
	}
	if e := json.Unmarshal([]byte(behavioralJSONLine(verifyOut)), &vp); e != nil {
		t.Fatalf("verify JSON parse: %v\n%s", e, verifyOut)
	}
	if !vp.OK {
		t.Fatalf("plugin verify overall not ok:\n%s", verifyOut)
	}
	var foundEmbedded bool
	for _, r := range vp.Results {
		if r.Agent == agentName {
			foundEmbedded = true
			if !r.Embedded || !r.Registered || !r.HashClean || !r.OK {
				t.Fatalf("agent %q verify flags not all true: %+v", agentName, r)
			}
		}
	}
	if !foundEmbedded {
		t.Fatalf("verify results missing agent %q:\n%s", agentName, verifyOut)
	}

	// --- prime NON-fallback: the fresh binary embeds the template, so there is no fallback
	// warning and the specialist template (its generated header) is rendered.
	agentDir := config.AgentDir(workspace, agentName)
	primeOut, perr := behavioralRunAF(t, freshAF, agentDir, env, "prime")
	if perr != nil {
		t.Fatalf("prime failed:\n%s", primeOut)
	}
	if strings.Contains(primeOut, "not embedded") || strings.Contains(primeOut, "inject a generic template") {
		t.Fatalf("prime fell back to a generic template (specialist not embedded):\n%s", primeOut)
	}
	if !strings.Contains(primeOut, "Generated by af formula agent-gen from "+agentName) {
		t.Fatalf("prime did not render the specialist template for %q:\n%s", agentName, primeOut)
	}

	// --- sling --agent RESOLVES: K14 guard passes on the fresh binary; --no-launch skips tmux.
	slingOut, serr := behavioralRunAF(t, freshAF, workspace, env, "sling", "--agent", agentName, "behavioral e2e task", "--no-launch")
	if serr != nil {
		t.Fatalf("sling --agent should resolve on the fresh binary:\n%s", slingOut)
	}
	if strings.Contains(slingOut, "owned by plugin") {
		t.Fatalf("sling hit the K14 refusal despite the embedded template:\n%s", slingOut)
	}
	if !strings.Contains(slingOut, "Dispatched to") {
		t.Fatalf("sling did not resolve/dispatch %q:\n%s", agentName, slingOut)
	}
}

// behavioralRunAF runs a subprocess `af` with a caller-supplied environment (HOME /
// AF_SOURCE_ROOT / PATH), returning combined output and the exit error. runAF hardcodes
// HOME-only env; this behavioral test needs AF_SOURCE_ROOT + a PATH that prefers the
// pipeline-rebuilt binary.
func behavioralRunAF(t *testing.T, binary, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	c := exec.Command(binary, args...)
	c.Dir = dir
	c.Env = env
	out, err := c.CombinedOutput()
	return string(out), err
}

// behavioralArchiveInto extracts `git archive HEAD` of the real repo into dst — a complete,
// buildable AF source copy that isolates every pipeline mutation from the working tree.
func behavioralArchiveInto(t *testing.T, dst string) {
	t.Helper()
	repo := findRepoRoot(t)
	tarPath := filepath.Join(t.TempDir(), "afsrc.tar")
	ar := exec.Command("git", "-C", repo, "archive", "--format=tar", "-o", tarPath, "HEAD")
	if out, err := ar.CombinedOutput(); err != nil {
		t.Fatalf("git archive: %s\n%s", err, out)
	}
	ex := exec.Command("tar", "-xf", tarPath, "-C", dst)
	if out, err := ex.CombinedOutput(); err != nil {
		t.Fatalf("tar extract: %s\n%s", err, out)
	}
}

// behavioralTrimInstallFormulas replaces the copy's install_formulas with a single trivial
// skill-free formula: the //go:embed target stays non-empty (build succeeds) while the
// agent-gen-all.sh sync/regen loop stays tiny.
func behavioralTrimInstallFormulas(t *testing.T, afSrc string) {
	t.Helper()
	dir := filepath.Join(afSrc, "internal", "cmd", "install_formulas")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read install_formulas: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".formula.toml") {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				t.Fatalf("trim install_formulas: %v", err)
			}
		}
	}
	behavioralWriteFile(t, filepath.Join(dir, "zzseed.formula.toml"), validPluginFormula("zzseed"), 0o644)
}

func behavioralWriteFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// behavioralGoEnv reports an ambient `go env` value (e.g. GOMODCACHE) so the
// pipeline build can be pinned to the shared toolchain caches instead of the
// per-test HOME under t.TempDir().
func behavioralGoEnv(t *testing.T, key string) string {
	t.Helper()
	out, err := exec.Command("go", "env", key).Output()
	if err != nil {
		t.Fatalf("go env %s: %v", key, err)
	}
	return strings.TrimSpace(string(out))
}

// behavioralJSONLine returns the last line of s that looks like a JSON object — the verify
// --json payload, past any store-spawn stderr noise captured by CombinedOutput.
func behavioralJSONLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "{") {
			return l
		}
	}
	return strings.TrimSpace(s)
}
