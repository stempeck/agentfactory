//go:build integration

package server

// Issue #455 Phase 3a — the LOAD-BEARING half of AC-5: the gated, real-`af` end-to-end proof that the
// web Sling form path serves the schema of a GENUINELY-parsed formula TOML. Phase 2's unit tier fakes
// the `af` binary (a fakeRunner returns a canned payload), so it proves the formula NAME on the argv but
// NOT the real TOML parse nor the web→af exec/parse seam (FormulaShowJSON → real `af formula show` →
// formschema.Reader.Read). `.analysis/455/rootcause_concern_8.md`: "no test exercises that composition
// against a real agents.json plus a real name-sensitive schema read; every test fakes at least one
// adjacent link" — that isolation is exactly how the wrong-formula bug shipped. This test runs all three
// real links together: resolveFormula (real agents.json via the Phase-1 FormulaResolver) + FormulaShowJSON
// (real branch-built `af`) + formschema.Reader.Read (real parse), driven through a real
// GET /api/agents/{name}/form.
//
// Tier: //go:build integration only — the hermetic web-unit lane (`cd web && go test ./...`, no -tags)
// never compiles this file (mirrors bridge_integration_test.go / cmddir_integration_test.go). It runs
// under `make test-integration` (Makefile:80, `cd web && … go test -tags=integration ./...`). Its SOURCE
// is still raw-scanned by the unit-lane source-lint (server/lint_test.go: TestExec_NoLiveTreeMutation
// parses no build tags), so all external commands are argv arrays via os/exec on programs "go"/"git"
// only — never a shell, never a literal mutating `af` (the form read goes through the Wrapper seam).
//
// SKIP-vs-FAIL is the whole point (mirrors cmddir K7): a toolchain-less or exec-incapable host must SKIP
// loudly (t.Skipf with the reason), NEVER a silent skip and NEVER a false FAIL. `af formula show` is a
// PURE read (no Python/issue-store), so once the branch `af` builds and runs, a wrong/missing schema
// served through the web seam is a genuine regression — a loud t.Fatalf.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory-web/internal/config"
	"github.com/stempeck/agentfactory-web/internal/exec"
	"github.com/stempeck/agentfactory-web/internal/formschema"
	"github.com/stempeck/agentfactory-web/internal/readmodel"
)

// formRepoRoot resolves the repo/worktree root via `git rev-parse --show-toplevel` (a robust locate of
// ./cmd/af from this nested module), Skip-not-Fatal on failure. It mirrors repoRoot in
// bridge_integration_test.go and k7RepoRoot in exec/cmddir_integration_test.go — both invisible here
// (repoRoot is the same package but a distinct name; k7RepoRoot is package exec), so this file carries
// its own copy. MUST be called BEFORE any chdir (it resolves against the process cwd).
func formRepoRoot(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := osexec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("SKIP: not in a git work tree (git rev-parse failed: %v) — cannot locate ./cmd/af to build af", err)
	}
	return strings.TrimSpace(string(out))
}

// formBuildAf builds a branch `af` from ./cmd/af into binDir and returns its path. Any build failure
// (no Go toolchain, a noexec build dir, etc.) is a logged SKIP, never a FAIL — building ./cmd/af is an
// environment prerequisite, not the behaviour under test. The build inherits os.Environ() so it carries
// the lane's exec-capable TMPDIR/GOTMPDIR (Makefile AF_TEST_TMPDIR); CGO is disabled for a static read.
func formBuildAf(t *testing.T, ctx context.Context, repoRoot, binDir string) string {
	t.Helper()
	afPath := filepath.Join(binDir, "af")
	build := osexec.CommandContext(ctx, "go", "build", "-o", afPath, "./cmd/af")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("SKIP: `go build ./cmd/af` failed (toolchain/exec-env unavailable): %v\n%s", err, out)
	}
	return afPath
}

// formWriteFactory plants a hermetic factory under root for the form path: a minimal factory.json (so
// af's config.FindFactoryRoot resolves the factory from the child's cmd.Dir), an agents.json mapping
// alpha → minimalworker (a non-empty description because af-core's validateAgentConfig rejects an empty
// one), and the canonical committed minimalworker fixture TOML copied into the formula search path
// (config.FormulasDir = <root>/.agentfactory/store/formulas). The fixture is the source of truth
// (CLAUDE.md: `make sync-formulas` syncs install_formulas/* into the store, byte-identical); reading it
// from <repoRoot> guarantees the authoritative shape — a required `task` input + a source="deferred"
// `orchestrator` var — that the INV-2/AC-4 assertion depends on.
func formWriteFactory(t *testing.T, repoRoot, root string) {
	t.Helper()
	dir := filepath.Join(root, ".agentfactory")
	formulasDir := filepath.Join(dir, "store", "formulas")
	if err := os.MkdirAll(formulasDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", formulasDir, err)
	}
	files := map[string]string{
		filepath.Join(dir, "factory.json"): `{"type":"factory","version":1,"name":"t","max_worktrees":8}`,
		filepath.Join(dir, "agents.json"):  `{"agents":{"alpha":{"type":"autonomous","description":"alpha agent","formula":"minimalworker"}}}`,
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	src := filepath.Join(repoRoot, "internal", "cmd", "install_formulas", "minimalworker.formula.toml")
	data, err := os.ReadFile(src)
	if err != nil {
		// The fixture is git-tracked and present at CI checkout; an unreadable copy is a broken
		// checkout (an environment prerequisite), not the behaviour under test — SKIP loudly.
		t.Skipf("SKIP: canonical fixture not readable at %s: %v", src, err)
	}
	dst := filepath.Join(formulasDir, "minimalworker.formula.toml")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", dst, err)
	}
}

// TestForm_RealAfParseThroughWebSeam is the Phase-3a load-bearing integration test (AC-5 real-parse half).
// It builds a real branch `af`, stands up a hermetic factory (real agents.json + the canonical
// minimalworker fixture TOML), wires the PRODUCTION form path (real exec.ExecRunner/exec.Wrapper + the
// Phase-1 WithFormulaResolver), and drives a real GET /api/agents/alpha/form. The single request exercises
// resolveFormula (real agents.json) + FormulaShowJSON (real `af`) + formschema.Reader.Read (real parse)
// together, asserting the user-providable `task` input is PRESENT and the deferred `orchestrator` var is
// ABSENT (INV-2 preserved through the genuine parse) — closing concern_8.
func TestForm_RealAfParseThroughWebSeam(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Resolve the repo root and build af BEFORE any chdir (both resolve against the process cwd).
	repoRoot := formRepoRoot(t)
	binDir := t.TempDir() // honors TMPDIR (=$HOME/.cache/af-test in the lane) → exec-capable there
	formBuildAf(t, ctx, repoRoot, binDir)

	// The web exec seam execs the LITERAL "af" via PATH (ExecRunner.afArgv; no env override), so put the
	// freshly built af first on PATH so the seam picks IT up.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The branch af is built without the Makefile's -X main.sourceRoot ldflag; AF_SOURCE_ROOT makes py/
	// reachable for the af child (it inherits this env; ExecRunner sets no cmd.Env).
	t.Setenv("AF_SOURCE_ROOT", repoRoot)

	// Hermetic factory: factory.json + agents.json (alpha→minimalworker) + the canonical fixture TOML.
	factoryRoot := t.TempDir()
	formWriteFactory(t, repoRoot, factoryRoot)

	// The PRODUCTION form path — reproduces web/cmd/afweb/main.go:48-62 plus the Phase-1 resolver wiring.
	runner := exec.NewExecRunner(factoryRoot)  // pins the af child's cmd.Dir to the factory root
	wrapper := exec.NewWrapper(runner, factoryRoot)
	forms := formschema.New(wrapper)           // real FormulaShowJSON → real `af formula show`
	cfg := config.New(factoryRoot, wrapper)    // FormulaResolver over the hermetic agents.json (Phase 1)
	rm := readmodel.New(wrapper, nil)          // real read-model (the form path no longer reads it)
	s := New(wrapper, rm, nil,
		WithFormReader(forms),
		WithFormulaResolver(cfg)) // REQUIRED post-Phase-1, else resolveFormula → 500 "resolver not configured"

	// Pre-flight (mirrors cmddir K7's SKIP-vs-FAIL triage): prove a REAL `af formula show` runs and parses
	// in THIS environment before asserting the web composition. `af formula show` is a pure read (no
	// Python/issue-store), so a process-level error means af could not be RUN at all (toolchain/noexec) —
	// an environment SKIP; an error envelope ({"state":"error",…}) for a correctly-placed hermetic fixture
	// means the genuine web→af parse/seam is broken — a loud FAIL.
	raw, err := wrapper.FormulaShowJSON(ctx, "minimalworker")
	if err != nil {
		t.Skipf("SKIP: real `af formula show` could not run (toolchain/exec-env unavailable): %v", err)
	}
	if strings.TrimSpace(raw) == "" {
		t.Skipf("SKIP: empty `af formula show` output (exec-env unavailable)")
	}
	var probe struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &probe) == nil && probe.State == "error" {
		t.Fatalf("FAIL: real `af formula show minimalworker` returned an error envelope for a correctly-placed hermetic fixture (web→af parse seam broken): %s", probe.Error)
	}

	// Drive the PRODUCTION handler end-to-end: a tokenless, Origin-free loopback GET (Peer Review F1 —
	// handleAgentForm calls guard(w,r,false), so originOK is never evaluated; the loopback default makes
	// authOK true). Matches the unit-tier serveForm helper (server_test.go:393).
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/alpha/form", nil))
	if rec.Code != http.StatusOK {
		// The pre-flight already proved the real parse runs in this env, so a non-200 here is the web
		// composition (resolveFormula/handler/reader) mis-serving — a regression, not an environment skip.
		t.Fatalf("FAIL: GET /api/agents/alpha/form = %d, want 200 (real parse works per pre-flight; web seam mis-served); body=%s", rec.Code, rec.Body.String())
	}

	// data is a formschema.Schema (reader.go:57-63): {name, primary, fields:[{name, required, …}]}.
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Name    string `json:"name"`
			Primary string `json:"primary"`
			Fields  []struct {
				Name     string `json:"name"`
				Required bool   `json:"required"`
			} `json:"fields"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal(rec.Body.Bytes(), &env); jerr != nil {
		t.Fatalf("FAIL: form response not JSON: %v; body=%s", jerr, rec.Body.String())
	}
	if !env.OK {
		t.Fatalf("FAIL: form envelope ok=false; body=%s", rec.Body.String())
	}

	// The CLOSING assertion of concern_8: the schema comes from a REAL `af formula show` TOML parse
	// through the web seam — the user-providable `task` input is PRESENT (+required) and the
	// source="deferred" `orchestrator` var is ABSENT (INV-2 preserved through formschema.Reader.Read).
	var hasTask, taskRequired, leakedOrch bool
	for _, f := range env.Data.Fields {
		switch f.Name {
		case "task":
			hasTask = true
			taskRequired = f.Required
		case "orchestrator":
			leakedOrch = true
		}
	}
	if !hasTask {
		t.Fatalf("FAIL: real-parsed web schema missing the `task` input (the user-providable field); fields=%+v", env.Data.Fields)
	}
	if !taskRequired {
		t.Fatalf("FAIL: `task` must be Required in the real-parsed web schema; fields=%+v", env.Data.Fields)
	}
	if leakedOrch {
		t.Fatalf("FAIL: INV-2 violated — the real-parsed web schema leaked the deferred `orchestrator` var; fields=%+v", env.Data.Fields)
	}
	if env.Data.Primary != "task" {
		t.Fatalf("FAIL: real-parsed web schema primary = %q, want \"task\" (the input-bridge bind target)", env.Data.Primary)
	}
	t.Logf("PASS: a REAL `af formula show` parse through the web form seam returned 200 with `task` present (required) and `orchestrator` (deferred) hidden — concern_8 closed end-to-end")
}
