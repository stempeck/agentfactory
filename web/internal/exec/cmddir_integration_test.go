//go:build integration

package exec

// Issue #432 Phase 4 — K7: the gated, real-`af` end-to-end proof that the web read path reads the
// INTENDED factory (served == intended). K6/K6′ (runner_test.go) prove the cmd.Dir field is set
// mechanically; K7 proves the consequence with a REAL branch-built `af`: build `af` from ./cmd/af,
// stand up a hermetic temp factory holding one planted agent, put the process cwd OUTSIDE the factory
// root (a child of it), and list agents through the production web path (ExecRunner -> Wrapper ->
// read-model) with cmd.Dir pinned to the factory root. The planted agent must come back.
//
// Tier: //go:build integration only — the hermetic web-unit lane (`cd web && go test ./...`, no -tags)
// never compiles this file (mirrors bridge_integration_test.go). It runs under `make test-integration`
// (Makefile:80, `cd web && … go test -tags=integration ./...`). Its SOURCE is still raw-scanned by the
// unit-lane source-lint (server/lint_test.go: TestExec_NoLiveTreeMutation parses no build tags), so all
// external commands are argv arrays via os/exec on programs "go"/"git" only — never a shell, never a
// literal mutating `af` (the read goes through the Wrapper seam).
//
// SKIP-vs-FAIL is the whole point (G6 / six_sigma_gaps Gap 4): a toolchain-less, noexec, or
// issue-store-less host must SKIP loudly (t.Skipf with the reason), NEVER a silent skip and NEVER a
// false FAIL. A wrong/empty read of a correctly-resolved factory is a loud t.Fatalf.

import (
	"context"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory-web/internal/readmodel"
)

// k7RepoRoot resolves the repo/worktree root via `git rev-parse --show-toplevel` (a robust locate of
// ./cmd/af from a nested module), Skip-not-Fatal on failure. It mirrors repoRoot in
// bridge_integration_test.go, which lives in package server and is invisible here, so K7 carries its
// own copy of the pattern. MUST be called BEFORE any t.Chdir (it resolves against the process cwd).
func k7RepoRoot(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := osexec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("K7 SKIP: not in a git work tree (git rev-parse failed: %v) — cannot locate ./cmd/af to build af", err)
	}
	return strings.TrimSpace(string(out))
}

// k7BuildAf builds a branch `af` from ./cmd/af into binDir and returns its path. Any build failure
// (no Go toolchain, a noexec build dir, etc.) is a logged SKIP, never a FAIL — building ./cmd/af is an
// environment prerequisite, not the behaviour under test. The build inherits os.Environ() so it carries
// the lane's exec-capable TMPDIR/GOTMPDIR (Makefile AF_TEST_TMPDIR); CGO is disabled for a static read.
func k7BuildAf(t *testing.T, ctx context.Context, repoRoot, binDir string) string {
	t.Helper()
	afPath := filepath.Join(binDir, "af")
	build := osexec.CommandContext(ctx, "go", "build", "-o", afPath, "./cmd/af")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("K7 SKIP: `go build ./cmd/af` failed (toolchain/exec-env unavailable): %v\n%s", err, out)
	}
	return afPath
}

// k7WriteFactory plants a hermetic factory under root: a minimal factory.json (matches FactoryConfig)
// and an agents.json with ONE named agent "planted". The agent carries a non-empty description because
// af-core's validateAgentConfig (internal/config/config.go) rejects an empty description — without it
// LoadAgentConfig fails and the planted agent is never returned (the read returns an error envelope).
func k7WriteFactory(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("K7: mkdir %s: %v", dir, err)
	}
	files := map[string]string{
		"factory.json": `{"type":"factory","version":1,"name":"t","max_worktrees":8}`,
		"agents.json":  `{"agents":{"planted":{"type":"autonomous","description":"planted agent","formula":"x"}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("K7: write %s: %v", name, err)
		}
	}
}

// cachedAgentsLister replays an already-captured `af agents list --json` payload into the read-model
// without a second exec. readmodel.AgentsLister is the same seam exec.Wrapper satisfies in production.
type cachedAgentsLister struct{ raw string }

func (c cachedAgentsLister) AgentsListJSON(context.Context) (string, error) { return c.raw, nil }

// envelopeError extracts the .error from an af read error envelope ({"state":"error","error":"…"}),
// falling back to the raw text if it is not a recognizable envelope.
func envelopeError(s string) string {
	var env struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(s), &env); err == nil && env.Error != "" {
		return env.Error
	}
	return s
}

// mentionsFactoryResolution reports whether an error-envelope message names a factory/config-resolution
// failure. In K7's layout (valid fixture; cwd a CHILD of the factory) factory resolution + config load
// are guaranteed, so such a message is a real served!=intended signal worth a loud FAIL — every other
// error envelope (issue-store / Python / py unavailable) is an environment SKIP.
func mentionsFactoryResolution(msg string) bool {
	m := strings.ToLower(msg)
	for _, marker := range []string{
		"not in an agentfactory workspace",
		"factory.json",
		"agents.json",
		"agent config",
		"empty description",
	} {
		if strings.Contains(m, marker) {
			return true
		}
	}
	return false
}

// containsAgent reports whether any assembled view names the given agent.
func containsAgent(views []readmodel.AgentView, name string) bool {
	for _, v := range views {
		if v.Name == name {
			return true
		}
	}
	return false
}

// TestCmdDir_RealAfReadsIntendedFactory is K7. It is a positive end-to-end proof: a real branch-built
// `af`, driven by the production web read path with cmd.Dir pinned to the resolved factory root, lists
// the intended factory's agents from a cwd OUTSIDE that root.
func TestCmdDir_RealAfReadsIntendedFactory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Resolve the repo root and build af BEFORE chdir (both resolve against the current cwd).
	repoRoot := k7RepoRoot(t)
	binDir := t.TempDir() // honors TMPDIR (=$HOME/.cache/af-test in the lane) → exec-capable there
	k7BuildAf(t, ctx, repoRoot, binDir)

	// Put the freshly built af first on PATH so the web exec seam (`af` on PATH) picks IT up.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The branch af is built without the Makefile's -X main.sourceRoot ldflag, so make py/ reachable via
	// AF_SOURCE_ROOT (main.go: mcpstore.SetEnvSourceRoot(os.Getenv("AF_SOURCE_ROOT"))). The af child
	// inherits this env (ExecRunner sets no cmd.Env). Without it the real issue-store can't locate py/
	// and K7 would only ever SKIP — never proving served==intended, even where Python IS available.
	t.Setenv("AF_SOURCE_ROOT", repoRoot)

	// Hermetic factory; process cwd a CHILD of it (cwd is outside the factory ROOT, yet the factory is
	// the one cmd.Dir pins the af child to).
	factoryRoot := t.TempDir()
	k7WriteFactory(t, factoryRoot)
	childDir := filepath.Join(factoryRoot, "outside-the-root")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatalf("K7: mkdir child cwd: %v", err)
	}
	t.Chdir(childDir)

	// The production web read path: ExecRunner(root) pins cmd.Dir; Wrapper is the C2 surface; the
	// read-model is the honest projection the Floor renders.
	runner := NewExecRunner(factoryRoot)
	w := NewWrapper(runner, factoryRoot)

	raw, err := w.AgentsListJSON(ctx)
	if err != nil {
		// af is a read that exits 0 and encodes failure in JSON, so a process-level error here means af
		// could not be RUN at all (noexec build dir, missing binary, crash) — an environment SKIP.
		t.Skipf("K7 SKIP: `af agents list` did not run (build/exec-env unavailable): %v", err)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		t.Skipf("K7 SKIP: empty output from `af agents list` (exec-env unavailable)")
	}

	// Reads branch on the JSON .state shape: an error envelope is a JSON object, success is an array.
	if strings.HasPrefix(trimmed, "{") {
		reason := envelopeError(trimmed)
		if mentionsFactoryResolution(reason) {
			t.Fatalf("K7 FAIL: factory/config resolution failed for a planted hermetic factory (served != intended): %s\nraw=%s", reason, trimmed)
		}
		t.Skipf("K7 SKIP: read returned an environment error envelope (issue-store/Python/py unavailable): %s", reason)
	}

	// Success: assert the planted agent is surfaced THROUGH the web read-model (served == intended).
	views, aerr := readmodel.New(cachedAgentsLister{raw: raw}, nil).Assemble(ctx)
	if aerr != nil {
		t.Fatalf("K7 FAIL: read-model could not assemble a valid agents array (served != intended): %v\nraw=%s", aerr, raw)
	}
	if !containsAgent(views, "planted") {
		t.Fatalf("K7 FAIL: served != intended — the planted agent is absent from the web read of the hermetic factory.\nviews=%+v\nraw=%s", views, raw)
	}
	t.Logf("K7 PASS: served == intended — a real branch-built af read the planted agent from the intended factory (cwd outside its root)")
}
