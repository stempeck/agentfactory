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
	// writes counts ONLY stdin-bearing invocations, i.e. `af config <file> set`. Since #620 Phase 2
	// the read path also shells out (`af config fingerprint --json`), so a bare invocation count can
	// no longer answer the question every write test actually asks: did a WRITE reach af?
	writes int
	res    exec.Result
	err    error
}

func (f *fakeRunner) Run(ctx context.Context, verb string, args ...string) (exec.Result, error) {
	return f.record(nil, verb, args)
}

func (f *fakeRunner) RunStdin(ctx context.Context, stdin []byte, verb string, args ...string) (exec.Result, error) {
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return f.record(stdin, verb, args)
}

// RunStream satisfies the extended Runner seam. This package never exercises streaming; a minimal
// recorder keeps the fake honest at zero cost.
func (f *fakeRunner) RunStream(ctx context.Context, onChunk func([]byte), verb string, args ...string) (exec.Result, error) {
	return f.record(nil, verb, args)
}

// record captures the invocation. stdin is recorded only when present, so a subsequent read call
// cannot erase the payload a write test is about to assert on.
func (f *fakeRunner) record(stdin []byte, verb string, args []string) (exec.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.verb = verb
	f.args = append([]string(nil), args...)
	if stdin != nil {
		f.stdin = append([]byte(nil), stdin...)
	}
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
	if _, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), ""); err != nil {
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
	if _, err := rejSvc.Write(context.Background(), "dispatch", badConfig, ""); err == nil {
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

// AC-3 / AC#6 — secrets never leave the backend. Extended to PAYLOAD level for #620 Phase 2: the
// raw tier serves whole documents verbatim, so "no secret field is declared" is no longer the whole
// argument. The guarantee now rests on the tier table — every secret-bearing file is either
// PROJECTED through a decode target that has no field for the secret, or EXCLUDED and never opened —
// and this test asserts it against the FULL marshalled Settings value, not against a struct shape.
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
	// models.json: every profile BODY is a map of env exports carrying gateway credentials. Only the
	// profile NAMES may cross the wire.
	mustWrite(t, modelsPath(root), `{"models":{
		"gateway":{"ANTHROPIC_AUTH_TOKEN":"sk-GATEWAY-SECRET","ANTHROPIC_BASE_URL":"https://gateway.secret.internal"},
		"loopback":{"ANTHROPIC_AUTH_TOKEN":"sk-LOOPBACK-SECRET"}
	}}`)
	// telemetry.json is TierExcluded: its headers are literal credentials. The console must not open
	// it at all, so nothing in it can appear even as a key name.
	mustWrite(t, filepath.Join(dir, "telemetry.json"), `{"endpoint":"https://otel.secret.internal","headers":{"authorization":"Bearer sk-TELEMETRY-SECRET"}}`)
	mustWrite(t, dispatchPath(root), validDispatchJSON)
	mustWrite(t, factoryPath(root), `{"type":"factory","version":1,"name":"demo"}`)

	svc := New(root, nil) // read path needs no af seam
	got, err := svc.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// The agent picker still lists every agent...
	if len(got.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(got.Agents))
	}
	// ...and the profile picker still lists every profile...
	if !equalStrings(got.Profiles, []string{"gateway", "loopback"}) {
		t.Fatalf("profiles = %v, want [gateway loopback]", got.Profiles)
	}
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	// A secret VALUE may not appear anywhere in the response — not in a document, not in an agent
	// summary, not in a disposition string.
	for _, secret := range []string{
		"sk-SECRET-TOKEN", "sk-ANOTHER-SECRET", "secret.internal", "claude-opus-4-8",
		"sk-GATEWAY-SECRET", "sk-LOOPBACK-SECRET", "sk-TELEMETRY-SECRET", "Bearer",
	} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("settings response leaked the secret value %q:\n%s", secret, blob)
		}
	}

	// A secret KEY NAME may not appear in the DATA — documents, agent summaries, profile names. It is
	// checked separately because the disposition prose deliberately NAMES the withheld keys to explain
	// why a file is projected; a whole-blob substring scan would fire on that explanation and would
	// then be "fixed" by deleting the explanation, which is exactly backwards.
	data, err := json.Marshal(struct {
		Docs     map[string]json.RawMessage `json:"docs"`
		Agents   []AgentSummary             `json:"agents"`
		Profiles []string                   `json:"profiles"`
	}{docsOf(got), got.Agents, got.Profiles})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"auth_token", "AuthToken", "base_url", "BaseURL", `"model"`,
		"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "authorization", "endpoint",
	} {
		if strings.Contains(string(data), key) {
			t.Fatalf("the settings DATA carries the secret-bearing key %q:\n%s", key, data)
		}
	}

	// Anti-vacuity: the assertions above would also hold on an EMPTY payload. The raw tier must in
	// fact have served whole documents, or this test proves nothing.
	if !strings.Contains(string(data), "trigger_label") || !strings.Contains(string(data), "rootcause") {
		t.Fatalf("the raw tier served nothing — the leak assertions above are vacuous:\n%s", data)
	}

	// An excluded file is ANNOUNCED but never OPENED: the row travels so the console can render "not
	// managed here, and here is who owns it" instead of silently omitting the file, but its document
	// is nil and it has no path constructor to have read one with.
	for _, r := range tierRows {
		if r.Tier != TierExcluded {
			continue
		}
		fv, served := got.Files[r.File]
		if !served {
			t.Errorf("excluded file %q is missing from the payload — the operator cannot see why it is unmanaged", r.File)
			continue
		}
		if fv.Doc != nil {
			t.Errorf("excluded file %q was READ: doc = %s", r.File, fv.Doc)
		}
		if fv.Writable || fv.Fingerprint != "" {
			t.Errorf("excluded file %q is writable=%v fingerprint=%q, want neither", r.File, fv.Writable, fv.Fingerprint)
		}
		if r.path != nil {
			t.Errorf("excluded file %q has a path constructor — the interlock is gone", r.File)
		}
	}
}

// AC-4 — the write path routes through `af config <file> set` (JSON on stdin), never an in-UI
// config writer. Proven by asserting the exact verb/argv and that the stdin round-trips to the full
// edited config (key order is non-deterministic, so round-trip rather than string-compare).
func TestSettingsWrite_RoutesThroughAfCommand(t *testing.T) {
	// Parameterized over the tier table's writable rows rather than a hand-typed pair, so growing the
	// table grows this test's coverage in the same change.
	payloads := map[string]string{
		"dispatch":   validDispatchJSON,
		"startup":    `{"agents":["manager"],"quality":"default","fidelity":"default","start_dispatch":true}`,
		"messaging":  `{"groups":{"leads":["manager","supervisor"]}}`,
		"statusline": `{"elements":["model","dir","branch"],"color":true}`,
	}
	files := WritableFiles()
	if len(files) == 0 {
		t.Fatal("the tier table derives no writable files — this test would loop zero times")
	}
	for _, file := range files {
		svc, fr := writeService(t, exec.Result{}, nil)
		body, ok := payloads[file]
		if !ok {
			t.Fatalf("no sample document for writable file %q — add one alongside the new tier row", file)
		}
		payload := []byte(body)
		if _, err := svc.Write(context.Background(), file, payload, ""); err != nil {
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
	if _, err := svc.Write(context.Background(), "factory", []byte(`{}`), ""); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("Write(factory) err = %v, want ErrNotWritable", err)
	}
	if fr.writes != 0 {
		t.Fatalf("a read-only file must never reach af (recorded %d writes)", fr.writes)
	}
}

// #620 Phase 2 — the compare-and-set precondition. The console echoes back the fingerprint it read;
// if the file changed underneath, the write is refused BEFORE af runs, so "409" truthfully means
// "nothing was written". On a match the digest is forwarded to af-core as well, which stays the
// authority for the narrow race between this check and its own write.
func TestSettingsWrite_IfContentHashPrecondition(t *testing.T) {
	onDisk := []byte(validDispatchJSON)

	newSvc := func(t *testing.T) (*Service, *fakeRunner) {
		t.Helper()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, dispatchPath(root), string(onDisk))
		fr := &fakeRunner{}
		return New(root, exec.NewWrapper(fr, "")), fr
	}

	t.Run("a stale fingerprint conflicts without touching af", func(t *testing.T) {
		svc, fr := newSvc(t)
		stale := hashHex([]byte(`{"repos":["someone/else-edited-this"]}`))
		_, err := svc.Write(context.Background(), "dispatch", []byte(`{"repos":["o/r"]}`), stale)
		if !errors.Is(err, ErrHashMismatch) {
			t.Fatalf("err = %v, want ErrHashMismatch", err)
		}
		if fr.writes != 0 {
			t.Fatalf("a stale precondition reached af (%d writes) — the file could have been overwritten", fr.writes)
		}
		if !strings.Contains(err.Error(), "re-read") {
			t.Errorf("the conflict does not tell the operator how to recover: %v", err)
		}
	})

	t.Run("a matching fingerprint writes and forwards the flag", func(t *testing.T) {
		svc, fr := newSvc(t)
		current := hashHex(onDisk)
		if _, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), current); err != nil {
			t.Fatalf("Write with the current fingerprint: %v", err)
		}
		want := []string{"dispatch", "set", "--if-content-hash=" + current}
		if fr.verb != "config" || len(fr.args) != 3 || fr.args[0] != want[0] || fr.args[1] != want[1] || fr.args[2] != want[2] {
			t.Fatalf("argv = %s %v, want config %v (single-token = form, so a dash-leading value can never re-parse as a flag)", fr.verb, fr.args, want)
		}
	})

	t.Run("no precondition leaves the argv untouched", func(t *testing.T) {
		svc, fr := newSvc(t)
		if _, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), ""); err != nil {
			t.Fatal(err)
		}
		if len(fr.args) != 2 {
			t.Fatalf("argv = %v, want exactly [dispatch set] — an unconditional write must not grow a flag", fr.args)
		}
	})

	t.Run("a precondition that is not a digest is a bad request, not a conflict", func(t *testing.T) {
		// The shape check must run BEFORE the comparison. Garbage compares unequal to every real
		// digest, so a comparison-first ordering would report it as staleness and send the client into
		// a reload-and-retry loop that re-reading the file can never resolve.
		for _, bad := range []string{"not-a-hash", "--force", hashHex(onDisk)[:63], hashHex(onDisk) + "0", "ZZ" + hashHex(onDisk)[2:]} {
			svc, fr := newSvc(t)
			_, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), bad)
			if !errors.Is(err, ErrBadPrecondition) {
				t.Errorf("Write with precondition %q: err = %v, want ErrBadPrecondition", bad, err)
			}
			if errors.Is(err, ErrHashMismatch) {
				t.Errorf("precondition %q was reported as a stale read: %v", bad, err)
			}
			if fr.writes != 0 {
				t.Errorf("precondition %q reached af (%d writes)", bad, fr.writes)
			}
		}
	})

	t.Run("af's own conflict is surfaced as a conflict, not a validation failure", func(t *testing.T) {
		// Defence in depth for the race window: af rejected the precondition itself. Its exit code is
		// 1, the same as a validation rejection, so only the message distinguishes them.
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, dispatchPath(root), string(onDisk))
		fr := &fakeRunner{
			res: exec.Result{ExitCode: 1},
			err: errors.New("af config: exit 1: Error: invalid config type: --if-content-hash is abc but dispatch.json is currently def — it changed since you read it; re-read the file, re-apply your edit, and retry"),
		}
		svc := New(root, exec.NewWrapper(fr, ""))
		_, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), hashHex(onDisk))
		if !errors.Is(err, ErrHashMismatch) {
			t.Fatalf("err = %v, want ErrHashMismatch", err)
		}
	})

	t.Run("an af too old for the flag is not a conflict", func(t *testing.T) {
		// The trap: cobra prints `unknown flag: --if-content-hash` on a pre-#620 binary. Reading that
		// as a conflict would put every save on an un-upgraded factory into a reload loop.
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, dispatchPath(root), string(onDisk))
		fr := &fakeRunner{res: exec.Result{ExitCode: 1}, err: errors.New("af config: exit 1: Error: unknown flag: --if-content-hash")}
		svc := New(root, exec.NewWrapper(fr, ""))
		_, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), hashHex(onDisk))
		if err == nil {
			t.Fatal("an af that cannot honour the precondition must still surface an error")
		}
		if errors.Is(err, ErrHashMismatch) {
			t.Fatalf("an unsupported flag was read as a stale-read conflict: %v", err)
		}
		// T2 (#621): "not a conflict" is necessary but no longer sufficient. The handler needs a POSITIVE
		// class to route this to a 502 instead of the 422 an unclassified non-zero exit falls onto, so the
		// old-af failure must carry ErrAfTooOld — not merely fail to be ErrHashMismatch.
		if !errors.Is(err, ErrAfTooOld) {
			t.Fatalf("an af too old for the forwarded flag must be classified ErrAfTooOld, got %v", err)
		}
	})
}

// T2 (#621) — the console forwards --if-content-hash unconditionally, so a pre-#620 af rejects a
// previously-working dispatch/startup/statusline save with `unknown flag: --if-content-hash`, exit 1.
// That must be classified ErrAfTooOld here (the C-2 boundary keeps af-error-text matching in this
// package) so the handler can map it to 502. The matcher keys on the full phrase, never a bare
// "unknown", or it would swallow genuine validation rejections (`unknown agent "ghost"`) that stay 422.
//
// The `messaging` setter (new in this PR) is deliberately NOT in scope here: verified against the real
// af CLI (Phase-7 sideways), an af lacking the subcommand prints usage and exits 0 — no error text, no
// non-zero exit — so it can never be classified by an error-text matcher. That silent-no-op is a
// distinct out-of-scope defect (see out_of_scope.md), NOT the `unknown command`+exit-1 the earlier
// [Inferred] premise assumed.
func TestSettingsWrite_AfTooOldClassification(t *testing.T) {
	onDisk := []byte(validDispatchJSON)

	newSvcWith := func(t *testing.T, res exec.Result, afErr error) *Service {
		t.Helper()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, dispatchPath(root), string(onDisk))
		return New(root, exec.NewWrapper(&fakeRunner{res: res, err: afErr}, ""))
	}

	// The one VERIFIED old-af failure (the forwarded flag on a setter that predates it) is ErrAfTooOld.
	t.Run("unknown flag (forwarded --if-content-hash on a pre-#620 af)", func(t *testing.T) {
		svc := newSvcWith(t, exec.Result{ExitCode: 1}, errors.New("af config: exit 1: Error: unknown flag: --if-content-hash"))
		_, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), hashHex(onDisk))
		if !errors.Is(err, ErrAfTooOld) {
			t.Fatalf("err = %v, want ErrAfTooOld", err)
		}
	})

	// A too-old af lacking the `messaging` subcommand does NOT error — verified against the real af, it
	// prints usage and exits 0. So `af config messaging set` returns no error and ExitCode 0, and Write
	// must NOT invent an ErrAfTooOld classification for a success it cannot see. (The resulting silent
	// 200 no-op is the out-of-scope deployment-skew defect noted above.)
	t.Run("a missing subcommand exits 0 and is not classified af-too-old", func(t *testing.T) {
		svc := newSvcWith(t, exec.Result{ExitCode: 0}, nil)
		_, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), hashHex(onDisk))
		if err != nil {
			t.Fatalf("an exit-0 child is not an error here: %v", err)
		}
	})

	// Non-overlap guard #1 (protective): af's OWN precondition conflict must stay ErrHashMismatch and
	// must NOT be reclassified ErrAfTooOld — otherwise a sloppy matcher turns a real 409 into a 502.
	t.Run("a genuine hash-mismatch conflict is not af-too-old", func(t *testing.T) {
		svc := newSvcWith(t, exec.Result{ExitCode: 1},
			errors.New("af config: exit 1: Error: --if-content-hash is abc but dispatch.json is def — it changed since you read it; re-read the file, re-apply your edit, and retry"))
		_, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), hashHex(onDisk))
		if !errors.Is(err, ErrHashMismatch) {
			t.Fatalf("err = %v, want ErrHashMismatch", err)
		}
		if errors.Is(err, ErrAfTooOld) {
			t.Fatalf("a stale-read conflict was reclassified as af-too-old (409 would become 502): %v", err)
		}
	})

	// Non-overlap guard #2 (protective): a genuine validation rejection (unknown AGENT, not flag/command)
	// must NOT be classified af-too-old, so it still falls through to the handler's non-zero-exit 422 arm.
	t.Run("a validation rejection is not af-too-old", func(t *testing.T) {
		svc := newSvcWith(t, exec.Result{ExitCode: 1},
			errors.New(`af config: exit 1: Error: dispatch mapping references unknown agent "ghost"`))
		_, err := svc.Write(context.Background(), "dispatch", []byte(validDispatchJSON), hashHex(onDisk))
		if err == nil {
			t.Fatal("a rejected mapping must still surface an error")
		}
		if errors.Is(err, ErrAfTooOld) {
			t.Fatalf("a validation rejection (unknown agent) was swallowed by the af-too-old matcher (422 would become 502): %v", err)
		}
	})
}

// #620 Phase 2 — the schema fingerprint travels with the settings payload so the client needs one
// request, and EVERY way of not getting it degrades to an empty string rather than failing the page.
func TestSettings_SchemaFingerprint(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, factoryPath(root), `{"type":"factory","version":1,"name":"demo"}`)
	const digest = "ec6c06c4912bfc7fb482c2771295cd0433dc386f1313294967982b6d53d26281"

	for _, tc := range []struct {
		name string
		fr   *fakeRunner
		want string
	}{
		{"ok envelope", &fakeRunner{res: exec.Result{Stdout: `{"state":"ok","fingerprint":"` + digest + `"}`}}, digest},
		{"error envelope at exit 0", &fakeRunner{res: exec.Result{Stdout: `{"state":"error","error":"boom"}`}}, ""},
		{"af older than the verb: non-zero exit, empty stdout", &fakeRunner{err: errors.New(`af config: exit 1: Error: unknown flag: --json`)}, ""},
		{"af not on PATH", &fakeRunner{err: errors.New(`af config: exec: "af": executable file not found in $PATH`)}, ""},
		{"unparseable stdout", &fakeRunner{res: exec.Result{Stdout: "not json"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(root, exec.NewWrapper(tc.fr, ""))
			got, err := svc.Read(context.Background())
			if err != nil {
				t.Fatalf("a fingerprint failure must not fail the whole read: %v", err)
			}
			if got.SchemaFingerprint != tc.want {
				t.Errorf("schema_fingerprint = %q, want %q", got.SchemaFingerprint, tc.want)
			}
			// The rest of the payload is intact regardless — the settings page still loads.
			if got.Files["factory"].Doc == nil {
				t.Error("the settings payload lost its documents because the fingerprint was unavailable")
			}
			// af's stderr is never funnelled into a browser-bound payload.
			if strings.Contains(got.SchemaFingerprint, "af config") || strings.Contains(got.SchemaFingerprint, "Error:") {
				t.Errorf("af stderr leaked into the payload: %q", got.SchemaFingerprint)
			}
		})
	}

	t.Run("no af seam at all", func(t *testing.T) {
		got, err := New(root, nil).Read(context.Background())
		if err != nil {
			t.Fatalf("Read with a nil seam: %v", err)
		}
		if got.SchemaFingerprint != "" {
			t.Errorf("schema_fingerprint = %q, want empty", got.SchemaFingerprint)
		}
	})
}

// #620 Phase 2 — an ABSENT file is served as absent. This replaces TestSettings_Read_-
// StartupAbsentDefaults, which asserted the opposite: that a missing startup.json yields fabricated
// defaults. That was the C-4 reading at the time, and it was the amplifier of the erasure bug — the
// console could not tell "the operator never wrote this file" from "the operator wrote exactly these
// values", so the first save MATERIALIZED defaults nobody chose. Absence is af-core's to interpret.
func TestSettings_Read_AbsentServedAsAbsent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, factoryPath(root), `{"type":"factory","version":1,"name":"demo"}`)
	// no startup.json, no dispatch.json, no agents.json, no models.json on disk.

	svc := New(root, nil)
	got, err := svc.Read(context.Background())
	if err != nil {
		t.Fatalf("Read with absent files must not error: %v", err)
	}
	for _, file := range []string{"startup", "dispatch", "messaging", "statusline"} {
		fv, ok := got.Files[file]
		if !ok {
			t.Fatalf("files[%q] is missing entirely; an absent file must still carry its disposition", file)
		}
		if fv.Doc != nil {
			t.Errorf("files[%q].doc = %s, want null — nothing on disk means nothing to serve, not a "+
				"fabricated document the operator never wrote", file, fv.Doc)
		}
		if fv.Fingerprint != "" {
			t.Errorf("files[%q].fingerprint = %q, want empty — there are no bytes to digest", file, fv.Fingerprint)
		}
		if fv.Reason == "" {
			t.Errorf("files[%q] carries no reason", file)
		}
	}
	// The file that IS on disk is served, so the absences above are attributable.
	if got.Files["factory"].Doc == nil {
		t.Fatal("factory.json is on disk but was not served — the read path is broken, not merely empty")
	}
	// The whole payload must marshal: a nil RawMessage encodes as null, an empty-but-non-nil one is
	// a hard marshal error, and the difference is invisible until something serializes it.
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("the settings payload does not marshal: %v", err)
	}
}

// #620 Phase 2 — a malformed file is a loud error, not an empty document. Serving it as absent would
// invite the console to save over a document it could not read: precisely the erasure this phase ends.
func TestSettings_Read_MalformedIsAnError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, dotDir), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, dispatchPath(root), `{"repos":[`)

	svc := New(root, nil)
	if _, err := svc.Read(context.Background()); err == nil {
		t.Fatal("a corrupt dispatch.json must surface an error, not an empty document")
	} else if !strings.Contains(err.Error(), "dispatch") {
		t.Errorf("the error does not name the offending file: %v", err)
	}
}

// docsOf lifts just the served documents out of a Settings value, so a leak scan can look at the
// DATA without also reading the disposition prose that describes it.
func docsOf(s Settings) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for name, fv := range s.Files {
		if fv.Doc != nil {
			out[name] = fv.Doc
		}
	}
	return out
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
