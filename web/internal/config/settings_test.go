package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/exec"
)

// fakeRunner implements exec.Runner so a config test can drive the REAL exec.Wrapper hermetically
// and assert the FINAL verb/argv/stdin reaching the seam — never spawning a real `af`. It is the
// config-package analogue of the server/exec fakes (FR-3 / ADR-018).
type fakeRunner struct {
	mu    sync.Mutex
	verb  string
	args  []string
	stdin []byte
	calls int
	res   exec.Result
	err   error
}

func (f *fakeRunner) Run(ctx context.Context, verb string, args ...string) (exec.Result, error) {
	return f.RunStdin(ctx, nil, verb, args...)
}

func (f *fakeRunner) RunStdin(ctx context.Context, stdin []byte, verb string, args ...string) (exec.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.verb = verb
	f.args = append([]string(nil), args...)
	f.stdin = append([]byte(nil), stdin...)
	return f.res, f.err
}

// RunStream satisfies the extended Runner seam. This package never exercises streaming; a minimal
// recorder (mirroring RunStdin) keeps the fake honest at zero cost.
func (f *fakeRunner) RunStream(ctx context.Context, onChunk func([]byte), verb string, args ...string) (exec.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.verb = verb
	f.args = append([]string(nil), args...)
	return f.res, f.err
}

var _ exec.Runner = (*fakeRunner)(nil)

// a complete, valid DispatchConfig (the editor always sends the WHOLE document, never a patch).
const validDispatchJSON = `{"repos":["stempeck/agentfactory-pro"],"trigger_label":"go","mappings":[{"labels":["bug"],"agent":"rootcause"}]}`

// writeService builds a config.Service whose write path runs through the REAL exec.Wrapper backed
// by the returned fakeRunner — so a test can assert the exact af invocation.
func writeService(t *testing.T, res exec.Result, err error) (*Service, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{res: res, err: err}
	w := exec.NewWrapper(fr, "")
	return New(t.TempDir(), w), fr
}

// AC-2 — a label→agent mapping is routed to `af config dispatch set` (which does the atomic
// temp+rename + struct + cross-file validation); a mapping to a non-existent agent (simulated by a
// non-zero af exit) is surfaced as an error WITHOUT the web layer ever writing the file itself.
func TestSettings_AtomicCrossFileValidatedWrite(t *testing.T) {
	// Valid write: routed through af, success surfaced.
	svc, fr := writeService(t, exec.Result{}, nil)
	if _, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON)); err != nil {
		t.Fatalf("valid Write: %v", err)
	}
	if fr.verb != "config" || len(fr.args) != 2 || fr.args[0] != "dispatch" || fr.args[1] != "set" {
		t.Fatalf("valid Write routed to verb=%q args=%v, want config [dispatch set]", fr.verb, fr.args)
	}

	// Rejected write: af exits non-zero (e.g. mapping to a non-existent agent). The web layer must
	// surface the error and must NOT have written any config file of its own.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := []byte(`{"repos":["o/r"],"trigger_label":"go","mappings":[{"labels":["bug"],"agent":"rootcause"}]}`)
	dp := dispatchPath(root)
	if err := os.WriteFile(dp, existing, 0o644); err != nil {
		t.Fatal(err)
	}
	rejFr := &fakeRunner{err: errors.New(`dispatch mapping references unknown agent "ghost"`)}
	rejSvc := New(root, exec.NewWrapper(rejFr, ""))
	badConfig := []byte(`{"repos":["o/r"],"trigger_label":"go","mappings":[{"labels":["bug"],"agent":"ghost"}]}`)
	if _, err := rejSvc.Write(context.Background(), "dispatch", badConfig); err == nil {
		t.Fatalf("a rejected mapping must surface an error")
	}
	// "WITHOUT corrupting the file": the web layer never writes dispatch.json; only af would, and af
	// (faked) rejected. So the on-disk file is byte-identical to what it was before the failed Write.
	after, err := os.ReadFile(dp)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(existing) {
		t.Fatalf("dispatch.json was mutated by a rejected write:\n got %s\nwant %s", after, existing)
	}
}

// AC-3 — secrets never leave the backend: the curated read omits the per-agent Model/BaseURL/
// AuthToken, by construction.
func TestSettings_RejectsUnsafeField(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, dotDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// agents.json carrying live secrets that must NEVER reach the browser.
	agentsJSON := `{"agents":{
		"rootcause":{"type":"specialist","description":"root cause analyst","formula":"rootcause","model":"claude-opus-4-8","base_url":"https://secret.internal","auth_token":"sk-SECRET-TOKEN"},
		"web-design":{"type":"specialist","description":"frontend","auth_token":"sk-ANOTHER-SECRET"}
	}}`
	mustWrite(t, filepath.Join(dir, "agents.json"), agentsJSON)
	mustWrite(t, dispatchPath(root), validDispatchJSON)
	mustWrite(t, factoryPath(root), `{"type":"factory","version":1,"name":"demo"}`)

	svc := New(root, nil) // read path needs no Setter
	got, err := svc.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// The agent picker still lists every agent...
	if len(got.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(got.Agents))
	}
	// ...but the serialized response carries no secret, anywhere.
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"auth_token", "AuthToken", "base_url", "BaseURL", "sk-SECRET-TOKEN", "sk-ANOTHER-SECRET", "secret.internal", `"model"`, "claude-opus-4-8"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("settings response leaked %q:\n%s", secret, blob)
		}
	}
}

// AC-4 — the write path routes through `af config <file> set` (JSON on stdin), never an in-UI
// config writer. Proven by asserting the exact verb/argv and that the stdin round-trips to the full
// edited config (key order is non-deterministic, so round-trip rather than string-compare).
func TestSettingsWrite_RoutesThroughAfCommand(t *testing.T) {
	for _, file := range []string{"dispatch", "startup"} {
		svc, fr := writeService(t, exec.Result{}, nil)
		payload := []byte(validDispatchJSON)
		if file == "startup" {
			payload = []byte(`{"agents":["manager"],"quality":"default","fidelity":"default","start_dispatch":true}`)
		}
		if _, err := svc.Write(context.Background(), file, payload); err != nil {
			t.Fatalf("Write(%s): %v", file, err)
		}
		if fr.verb != "config" {
			t.Fatalf("verb = %q, want config", fr.verb)
		}
		if len(fr.args) != 2 || fr.args[0] != file || fr.args[1] != "set" {
			t.Fatalf("argv = %v, want [%s set]", fr.args, file)
		}
		// stdin must round-trip to the exact document we handed Write (raw passthrough — no in-UI
		// re-marshal/validation).
		var sent, got map[string]any
		if err := json.Unmarshal(payload, &sent); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(fr.stdin, &got); err != nil {
			t.Fatalf("stdin is not the JSON we sent: %v (%q)", err, fr.stdin)
		}
		if !jsonEqual(sent, got) {
			t.Fatalf("stdin payload diverged:\n sent %v\n  got %v", sent, got)
		}
	}

	// factory.json is read-only: Write must refuse it BEFORE any af invocation.
	svc, fr := writeService(t, exec.Result{}, nil)
	if _, err := svc.Write(context.Background(), "factory", []byte(`{}`)); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("Write(factory) err = %v, want ErrNotWritable", err)
	}
	if fr.calls != 0 {
		t.Fatalf("a read-only file must never reach af (recorded %d calls)", fr.calls)
	}
}

// startup.json absent ⇒ defaults, not an error (C-4 backward-compat).
func TestSettings_Read_StartupAbsentDefaults(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, factoryPath(root), `{"type":"factory","version":1,"name":"demo"}`)
	// no startup.json, no dispatch.json, no agents.json on disk.

	svc := New(root, nil)
	got, err := svc.Read(context.Background())
	if err != nil {
		t.Fatalf("Read with absent files must not error: %v", err)
	}
	if got.Startup.Quality != "default" || got.Startup.Fidelity != "default" {
		t.Fatalf("absent startup.json should yield defaults, got %+v", got.Startup)
	}
	if got.Factory.Name != "demo" {
		t.Fatalf("factory read-only view = %+v, want name=demo", got.Factory)
	}
}

// #483 Phase 2b — a CLI-set improvement value must survive the web Read() decode. Before the
// Startup mirror gained the Improvement field, encoding/json silently dropped the on-disk
// `improvement` key, so a subsequent full-replace web save erased it. This is the read-side proof
// of the round-trip: the value is decoded and carried, not dropped.
func TestSettings_Read_PreservesImprovement(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, startupPath(root), `{"agents":["manager"],"quality":"default","fidelity":"default","improvement":"on","start_dispatch":true}`)

	svc := New(root, nil) // read path needs no Setter
	got, err := svc.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Startup.Improvement != "on" {
		t.Fatalf("on-disk improvement was dropped by Read: got Startup.Improvement=%q, want \"on\" (Startup=%+v)", got.Startup.Improvement, got.Startup)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonEqual(a, b map[string]any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
