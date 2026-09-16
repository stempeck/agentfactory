//go:build integration

package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
)

// #673 item 3, LIVE-PROBE. The claim "a second concurrent sub-agent launch is denied" has until now
// rested on one-shot manual observation and on unit tests that issue the second call THEMSELVES — so
// a platform that batched a message's tool calls into one hook invocation would leave every one of
// them green. This is the test that would go red.
//
// It has two legs and they fail for different reasons on purpose:
//
//	Leg 1 runs everywhere. It drives the REAL COMPILED af dispatch-admit as a subprocess against a
//	disposable factory: the real binary, the real PATH resolution, the real config loaders, a real
//	O_EXCL slot file. What it CANNOT prove is the wire contract — it marshals the hook's stdin with
//	the same struct the hook decodes it with, so a renamed json tag stays green here and goes red
//	only in Leg 2. Two subprocess calls in sequence, not two concurrent ones: that is the cap's
//	arithmetic, not the platform's behaviour.
//
//	Leg 2 runs the real claude CLI headless against a loopback stub scripting ONE assistant turn with
//	TWO Agent tool_use blocks and NO pre-held slot. It SKIPS only when the CLI is absent, and even
//	that skip is refusable: AF_REQUIRE_LIVE_CLAUDE=1 turns it into a failure, for a lane that means to
//	guarantee live coverage rather than hope for it. Every other failure — a broken handshake, a
//	non-zero exit, a blown deadline, a refusal count that is not exactly one — is a FAILURE, because
//	those are the platform moving, which is the fact this probe exists to detect. Both legs log the
//	CLI version so a future red is legible.
//
// Neither leg reaches the RELEASE ladder: the deny is the claim half. The release half — a stop
// event is not a completion event, the #673 defect proper — is the fixture replay's next door.
//
// Five observed ways this probe can pass while never exercising the gate, all guarded below:
// an ambient AF_ROOT (rootMismatchError ⇒ silent admit), a noexec temp dir (the hook binary cannot
// execute and Claude Code treats a failed PreToolUse as non-blocking), a pool under the child floor
// (refuses first, with the wrong reason), PATH shadowing of the operator's installed af, and a
// factory nested inside another factory.

const (
	// The design's hard internal deadline, and it bounds the TEST, not each operation: one context is
	// opened at the top of the test body and every subprocess below — both compiles, both admits, the
	// version stamp and the CLI run — is a child of it, so their sum cannot exceed this. The observed
	// end-to-end cost is ~0.7s, so this is a hang detector, not a budget.
	liveProbeDeadline = 55 * time.Second
	liveProbeAgent    = "probe"
	// Set to 1 by a lane that installs the claude CLI on purpose. Mirrors AF_REQUIRE_REAL_STORE in
	// .github/workflows/test.yml, which exists so a missing dependency hard-fails the gating lane
	// instead of skipping it green — the same failure mode, one level up.
	liveProbeRequireEnv = "AF_REQUIRE_LIVE_CLAUDE"
)

type liveProbeFactory struct {
	base       string
	home       string
	root       string
	workDir    string
	afBin      string
	backendKey string
	env        []string
}

// newLiveProbeFactory builds a whole disposable factory — HOME, CLAUDE_CONFIG_DIR, factory root and
// agent workdir — under one exec-capable directory, and returns the exact environment the CLI and the
// hook grandchild will see. The environment is composed from an ALLOWLIST rather than filtered out of
// os.Environ, because AF_ROOT is exported on developer hosts and it reaches the af grandchild through
// two process boundaries that NeutralizeAFEnv does not cross.
func newLiveProbeFactory(ctx context.Context, t *testing.T, baseURL string) liveProbeFactory {
	t.Helper()
	// Fatal where the gate harness's other caller only logs. A hook binary on a noexec filesystem fails
	// with EACCES, Claude Code treats a failed PreToolUse as non-blocking, BOTH sub-agents spawn and the
	// CLI exits 0 with empty stderr — so a probe that degraded here would PASS, having proven nothing.
	base, err := tryExecCapableDir(t, "af-test-denyprobe")
	if err != nil {
		t.Fatalf("no exec-capable filesystem for the probe factory: %v — the hook binary could not run "+
			"and the probe would pass without ever exercising the gate", err)
	}

	fx := liveProbeFactory{
		base:    base,
		home:    filepath.Join(base, "home"),
		root:    filepath.Join(base, "factory"),
		afBin:   filepath.Join(base, "home", ".local", "bin", "af"),
		workDir: filepath.Join(base, "factory", ".agentfactory", "agents", liveProbeAgent),
	}

	afDir := filepath.Join(fx.root, ".agentfactory")
	for _, dir := range []string{
		filepath.Dir(fx.afBin), filepath.Join(fx.home, ".claude"), fx.workDir, config.StoreDir(fx.root),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(afDir, "factory.json"), `{"type":"factory","version":1,"name":"af-test-denyprobe"}`)
	write(config.AgentsConfigPath(fx.root),
		`{"agents":{"`+liveProbeAgent+`":{"type":"autonomous","description":"live deny probe"}}}`)
	write(tokenomicsGateFile(fx.root), "on\n")
	write(config.StartupConfigPath(fx.root),
		`{"tokenomics":{"enabled":"on","budget":"on","admission_margin_pct":10,"learned_min_runs":1}}`)
	write(config.ModelsConfigPath(fx.root), fmt.Sprintf(
		`{"default":"probe","models":{"probe":{`+
			`"ANTHROPIC_BASE_URL":%q,`+
			`"ANTHROPIC_AUTH_TOKEN":"probe",`+
			`"AF_BACKEND_POOL_TOKENS":"%d",`+
			`"AF_DISABLE_PARALLEL_SUBAGENTS":"1"}}}`, baseURL, capPoolTokens))

	// The third silent-disarm route: a nested factory resolves to the OUTER root and the gate then
	// admits against a pool it was never given. Checked once the marker is on disk, so the walk sees
	// the same thing resolveInvokerRoot will.
	assertNotNestedFactory(t, fx.root)

	// Through the real loaders: a fixture the production path would reject must fail here, not
	// reappear three assertions later as an unexplained silent admit.
	if _, err := config.LoadStartupConfig(fx.root); err != nil {
		t.Fatalf("the probe factory's startup.json does not load: %v", err)
	}
	if _, err := config.LoadAgentConfig(config.AgentsConfigPath(fx.root)); err != nil {
		t.Fatalf("the probe factory's agents.json does not load: %v", err)
	}
	modelsCfg, err := config.LoadModelsConfig(fx.root)
	if err != nil {
		t.Fatalf("the probe factory's models.json does not load: %v", err)
	}
	// The ledger key is composed the way the gate composes it, not re-spelled from the stub URL: a
	// test that guessed the hash input would look in an empty directory and report a slot that was
	// never claimed as one that was never needed.
	fx.backendKey = config.NormalizedEndpoint(modelsCfg.Models["probe"])
	if fx.backendKey == "" {
		t.Fatal("the probe profile declares no poolable endpoint; the gate would be inert by construction")
	}

	// Built where the hook will look for it: the gate resolves af off $HOME/.local/bin through the
	// shipped template's own PATH export.
	plantAFUnderHome(ctx, t, fx.afBin)

	fx.env = []string{
		"HOME=" + fx.home,
		"PATH=" + filepath.Dir(fx.afBin) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMPDIR=" + base,
		"CLAUDE_CONFIG_DIR=" + filepath.Join(fx.home, ".claude"),
		"ANTHROPIC_BASE_URL=" + baseURL,
		"ANTHROPIC_AUTH_TOKEN=probe",
		"ANTHROPIC_API_KEY=probe",
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"DISABLE_BUG_COMMAND=1",
		"DISABLE_NON_ESSENTIAL_MODEL_CALLS=1",
		"NO_COLOR=1",
		"TERM=dumb",
	}
	return fx
}

// wireProbeHooks installs the SHIPPED autonomous settings and then keeps only the two hook events the
// probe needs. Filtering the shipped template rather than authoring a settings.json means the hook
// command string and the Task|Agent matcher stay the ones the factory really installs — if the
// template stops wiring af dispatch-admit, this fails here. The kept entry is re-emitted as its
// ORIGINAL bytes so any field the template carries beyond the two read below (a timeout, say) survives
// the round trip; only the assertion decodes.
//
// The dropped events — SessionStart, Stop, UserPromptSubmit, and PostToolUse (#673 item 1's observer
// relay, which has its own tests) — would reach for the issue store and the gate scripts of a factory
// that has neither, which is noise the probe should not have to survive.
func wireProbeHooks(t *testing.T, agentDir string) {
	t.Helper()
	if err := claude.EnsureSettings(agentDir, claude.Autonomous); err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	path := filepath.Join(agentDir, ".claude", "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shipped settings: %v", err)
	}
	var doc struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode shipped settings: %v", err)
	}

	kept := map[string][]json.RawMessage{}
	var admitCommand string
	for _, entry := range doc.Hooks["PreToolUse"] {
		var decoded struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(entry, &decoded); err != nil {
			t.Fatalf("decode PreToolUse entry: %v", err)
		}
		if !strings.Contains(decoded.Matcher, "Agent") {
			continue
		}
		kept["PreToolUse"] = []json.RawMessage{entry}
		for _, h := range decoded.Hooks {
			admitCommand = h.Command
		}
	}
	if !strings.Contains(admitCommand, "af dispatch-admit") {
		t.Fatalf("the shipped autonomous settings no longer wire af dispatch-admit onto an Agent "+
			"matcher; the probe would run with no gate at all. PreToolUse = %s", doc.Hooks["PreToolUse"])
	}
	if stop := doc.Hooks["SubagentStop"]; len(stop) > 0 {
		kept["SubagentStop"] = stop
	}

	out, err := json.MarshalIndent(map[string]any{"hooks": kept}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// probeStub speaks enough of the streaming Messages API for the real CLI to complete a turn.
//
// It branches on the presence of an Agent tool, NOT on a request counter. The CLI's FIRST request to
// /v1/messages is a tool-less warm-up whose response it discards; a counter-based stub answers that
// one with the scripted turn, the CLI never sees a tool call, and the probe passes having launched
// nothing. The flag is mutex-guarded because the handler is hit concurrently once children are live.
type probeStub struct {
	*httptest.Server
	mu             sync.Mutex
	toolTurnServed bool
	sawAgentTool   bool
	requests       []string
}

func newProbeStub(t *testing.T) *probeStub {
	t.Helper()
	s := &probeStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}

		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(body, &req)
		offersAgent := false
		for _, tool := range req.Tools {
			if tool.Name == "Agent" {
				offersAgent = true
			}
		}

		s.mu.Lock()
		s.requests = append(s.requests, string(body))
		s.sawAgentTool = s.sawAgentTool || offersAgent
		scripted := offersAgent && !s.toolTurnServed
		if scripted {
			s.toolTurnServed = true
		}
		s.mu.Unlock()

		if scripted {
			writeProbeToolTurn(w)
			return
		}
		// Every later turn ends. Without this the CLI keeps asking and keeps launching.
		writeProbeTextTurn(w)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *probeStub) snapshot() (served, sawAgent bool, requests []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toolTurnServed, s.sawAgentTool, append([]string(nil), s.requests...)
}

func sseEvent(w http.ResponseWriter, name string, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func sseStart(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	sseEvent(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg_probe", "type": "message", "role": "assistant", "model": "claude-probe",
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	}})
}

func sseEnd(w http.ResponseWriter, stopReason string) {
	sseEvent(w, "message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 40}})
	sseEvent(w, "message_stop", map[string]any{"type": "message_stop"})
}

// writeProbeToolTurn is the whole experiment: ONE assistant message carrying TWO Agent tool_use
// blocks. No slot is pre-held, so whether the second is denied depends entirely on whether the host
// evaluates the hook per tool call or once per message.
func writeProbeToolTurn(w http.ResponseWriter) {
	sseStart(w)
	for i, call := range []struct {
		id, input string
	}{
		{"toolu_probe_1", `{"description":"probe one","prompt":"reply with the word one","subagent_type":"general-purpose","run_in_background":true}`},
		{"toolu_probe_2", `{"description":"probe two","prompt":"reply with the word two","subagent_type":"general-purpose"}`},
	} {
		sseEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": i,
			"content_block": map[string]any{"type": "tool_use", "id": call.id, "name": "Agent", "input": map[string]any{}}})
		sseEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": call.input}})
		sseEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	sseEnd(w, "tool_use")
}

func writeProbeTextTurn(w http.ResponseWriter) {
	sseStart(w)
	sseEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""}})
	sseEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "text_delta", "text": "done"}})
	sseEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	sseEnd(w, "end_turn")
}

// claudeVersionStamp runs under the probe's composed environment, not the ambient one: it is the only
// other place this file execs the CLI, and reading the operator's real ~/.claude here would be the one
// crack in the allowlist the rest of the file is built around.
func claudeVersionStamp(ctx context.Context, t *testing.T, claudePath string, env []string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, claudePath, "--version")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("claude is on PATH at %s but `claude --version` failed: %v — a CLI that cannot "+
			"report its version cannot be held to a contract, so this is a failure, not a skip",
			claudePath, err)
	}
	return strings.TrimSpace(string(out))
}

func TestDispatchAdmit_LiveDenyProbe(t *testing.T) {
	// Leg 1 is claude-independent by construction, so this parent body always reaches its assertions
	// and the test reports PASS at column 0 whether or not the CLI exists (statusline_failopen's
	// two-leg discipline). AC #3's grep is satisfied by that PASS, which is exactly why the live leg
	// logs the CLI version it exercised: the version stamp, not the PASS, is what proves a live run.
	ctx, cancel := context.WithTimeout(t.Context(), liveProbeDeadline)
	defer cancel()

	stub := newProbeStub(t)
	fx := newLiveProbeFactory(ctx, t, stub.URL)

	admit := func() (string, string) {
		t.Helper()
		payload, err := json.Marshal(dispatchAdmitPayload{ToolName: "Agent", Cwd: fx.workDir})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, fx.afBin, "dispatch-admit")
		cmd.Dir = fx.workDir
		cmd.Env = fx.env
		cmd.Stdin = bytes.NewReader(payload)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("af dispatch-admit exited non-zero (%v); ADR-007 requires this hook to exit 0 "+
				"on every path.\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
		}
		return stdout.String(), stderr.String()
	}

	first, stderr := admit()
	if first != "" {
		t.Fatalf("the first sub-agent launch against an idle cap was not admitted silently:\n%s\n%s", first, stderr)
	}
	ledger := reservationDir(fx.workDir, fx.backendKey)
	if _, err := os.Stat(filepath.Join(ledger, sequentialSlotName)); err != nil {
		t.Fatalf("the admitted launch claimed no sequential.slot under %s: %v — the compiled binary "+
			"never reached the cap, so nothing below would be evidence of anything", ledger, err)
	}

	second, stderr := admit()
	if !strings.Contains(second, `"permissionDecision":"deny"`) {
		t.Fatalf("a second sub-agent launch, while the first still holds the slot, was not denied by "+
			"the compiled binary:\nstdout: %s\nstderr: %s", second, stderr)
	}
	if !strings.Contains(second, "AF_DISABLE_PARALLEL_SUBAGENTS") {
		t.Errorf("the deny does not name the cap that produced it:\n%s", second)
	}

	refusals := dispatchRefusals(t, fx.root, liveProbeAgent)
	if len(refusals) != 1 {
		t.Fatalf("two launches through the compiled binary recorded %d refusals, want exactly 1", len(refusals))
	}
	assertRefusalBreadcrumb(t, fx.workDir)

	// Leg 2. The only skip in this file, and it is the only one the design permits — and a lane that
	// exports AF_REQUIRE_LIVE_CLAUDE=1 does not permit even that, because a green PASS with the live
	// half skipped is indistinguishable from a green PASS with it run.
	t.Run("real claude CLI, one turn, two Agent tool_use blocks", func(t *testing.T) {
		claudePath, err := exec.LookPath("claude")
		if err != nil {
			// The flag is read in TestMain, not here: NeutralizeAFEnv wipes the AF_* family before
			// m.Run, so an os.Getenv on this line would always see "" and the switch would be dead
			// code that reads like a satisfied one. Same trap, same answer as afRequireRealStore.
			if afRequireLiveClaude {
				t.Fatalf("%s=1 but the claude CLI is not on PATH (%v); this lane asked to guarantee "+
					"live coverage, so a skip here is the vacuous green it was set to prevent",
					liveProbeRequireEnv, err)
			}
			t.Skipf("the claude CLI is not on PATH (%v); the live half of the deny probe cannot run "+
				"here. Set %s=1 to make this a failure. Every other failure mode in this file already "+
				"is one.", err, liveProbeRequireEnv)
		}
		live := newProbeStub(t)
		fxLive := newLiveProbeFactory(ctx, t, live.URL)
		wireProbeHooks(t, fxLive.workDir)

		version := claudeVersionStamp(ctx, t, claudePath, fxLive.env)
		t.Logf("live deny probe exercising claude %s", version)

		cmd := exec.CommandContext(ctx, claudePath,
			"--print", "--verbose",
			"--output-format", "stream-json",
			"--include-hook-events",
			"Launch two sub-agents.")
		cmd.Dir = fxLive.workDir
		// Env is the allowlist newLiveProbeFactory composed. In particular it carries no AF_ROOT: an
		// ambient one makes resolveInvokerRootWarn return rootMismatchError and the gate admits in
		// silence, which reads exactly like a healthy run.
		cmd.Env = append(fxLive.env, "ANTHROPIC_BASE_URL="+live.URL)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		// Buffers mean os/exec uses pipes, and Wait blocks until EVERY writer closes them — including a
		// hook grandchild that inherited them. CommandContext kills only the direct child, so without a
		// WaitDelay a wedged af dispatch-admit outlives the deadline and Run never returns: the hang the
		// design forbids, in the one place a context cannot reach.
		cmd.WaitDelay = 5 * time.Second
		started := time.Now()
		runErr := cmd.Run()
		elapsed := time.Since(started)

		if ctx.Err() != nil {
			t.Fatalf("claude %s did not finish inside %v against a loopback stub — a probe that hangs "+
				"is worse than one that fails.\nstdout: %s\nstderr: %s",
				version, liveProbeDeadline, stdout.String(), stderr.String())
		}
		if runErr != nil {
			t.Fatalf("claude %s exited non-zero (%v) against the stub; the handshake broke and this is "+
				"a FAILURE, not a skip.\nstdout: %s\nstderr: %s", version, runErr, stdout.String(), stderr.String())
		}
		served, sawAgent, requests := live.snapshot()
		if !sawAgent {
			t.Fatalf("claude %s never offered an Agent tool in %d requests; the tool the gate matches "+
				"on has been renamed or withdrawn", version, len(requests))
		}
		if !served {
			t.Fatalf("claude %s never took the scripted two-tool_use turn", version)
		}
		t.Logf("claude %s completed the scripted turn in %v over %d API requests", version, elapsed, len(requests))

		tasks, hooks := countProbeStreamEvents(t, stdout.Bytes())

		// The discriminator. 0 refusals means the host batched the message's tool calls into a single
		// hook invocation (or the gate went inert); more than 1 means it over-refused. Never "the
		// second call was denied": the two hooks are concurrent processes and O_EXCL picks the winner.
		refusals := dispatchRefusals(t, fxLive.root, liveProbeAgent)
		if len(refusals) != 1 {
			t.Fatalf("claude %s: one turn carrying two Agent tool_use blocks produced %d dispatch "+
				"refusals, want exactly 1.\nstdout: %s\nstderr: %s", version, len(refusals), stdout.String(), stderr.String())
		}
		assertSequentialOnlyRefusal(t, refusals[0])
		assertRefusalBreadcrumb(t, fxLive.workDir)

		if tasks != 1 {
			t.Errorf("claude %s started %d sub-agents, want exactly 1 — the deny did not block a "+
				"launch, it only produced text", version, tasks)
		}
		if hooks != 2 {
			t.Errorf("claude %s reported %d PreToolUse hook invocations for ONE message carrying TWO "+
				"Agent tool_use blocks, want 2. This is the per-call contract the whole sequential cap "+
				"rests on", version, hooks)
		}

		// The CLI's own acknowledgement: it fed the deny reason back to the model verbatim. Asserted
		// over the stub's request log rather than the output format, so it survives a renamed event.
		echoed := false
		for _, body := range requests {
			if strings.Contains(body, "AF_DISABLE_PARALLEL_SUBAGENTS") {
				echoed = true
			}
		}
		if !echoed {
			t.Errorf("claude %s never returned the deny reason to the model; a deny the model cannot "+
				"see is a deny it cannot respond to", version)
		}

		assertProbeC7Fold(t, version, fxLive.workDir)
	})
}

// countProbeStreamEvents counts sub-agent starts and PreToolUse hook invocations out of the CLI's
// stream-json output. The hook count is the direct reading of the fact this probe exists for: two
// tool_use blocks in one assistant message must produce two hook invocations.
func countProbeStreamEvents(t *testing.T, out []byte) (tasks, hooks int) {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		var ev struct {
			Subtype  string `json:"subtype"`
			HookName string `json:"hook_name"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		switch {
		case ev.Subtype == "task_started":
			tasks++
		case ev.Subtype == "hook_started" && strings.Contains(ev.HookName, "PreToolUse"):
			hooks++
		}
	}
	return tasks, hooks
}

// assertProbeC7Fold is the design's budget-permitting extra (C-7): where a SubagentStop actually
// fires inside the probe's window, PAYLOAD-CAPTURE writes the host's payload and the documented
// background_tasks[] element schema can be checked against a live one. Where it does not — one call
// asks for run_in_background and the CLI need not wait for it — the fold DEGRADES to a recorded
// observation. It never fails the deny half, which stands on its own.
func assertProbeC7Fold(t *testing.T, version, workDir string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workDir, ".runtime", "dispatch_stop_payload.json"))
	if err != nil {
		t.Logf("C-7 fold not elicited under claude %s (no SubagentStop reached PAYLOAD-CAPTURE within "+
			"the probe's window); the deny half stands on its own", version)
		return
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Errorf("PAYLOAD-CAPTURE wrote a document that does not decode: %v", err)
		return
	}
	if _, ok := payload[dispatchStopPayloadFreeText]; ok {
		t.Errorf("PAYLOAD-CAPTURE kept %s", dispatchStopPayloadFreeText)
	}
	tasksRaw, ok := payload["background_tasks"]
	if !ok {
		t.Logf("C-7 fold: claude %s sent a SubagentStop with no background_tasks[]; E0 stays dark", version)
		return
	}
	var tasks []map[string]json.RawMessage
	if err := json.Unmarshal(tasksRaw, &tasks); err != nil {
		t.Errorf("background_tasks does not decode: %v", err)
		return
	}
	for i, task := range tasks {
		for _, key := range []string{"id", "type", "status", "description", "agent_type"} {
			if _, ok := task[key]; !ok {
				t.Errorf("C-7 fold: claude %s background_tasks[%d] is missing %q", version, i, key)
			}
		}
	}
	t.Logf("C-7 fold: claude %s recorded %d background_tasks at SubagentStop: %s", version, len(tasks), tasksRaw)

	rejudgeCapturedFixture(t, version, payload, tasks)
}

// rejudgeCapturedFixture is the only thing in the tree that can falsify the committed stop fixture.
//
// Several of TestDispatchDeny_FixtureReplay's assertions are arithmetic over a JSON file we committed:
// no production change and no platform change can make them fail, which is the vacuity this whole
// phase exists to end. Here we hold a payload the host produced MINUTES ago, so here is where they get
// re-judged — the top-level key set, transcript_path naming the parent, the child's transcript under
// <session>/subagents/, and the stopping child still reading "running".
//
// One of them cannot be re-judged here and is named rather than papered over: the replay also asserts
// that BOTH background_tasks entries read "running" at stop time, and this probe's own deny guarantees
// only ONE child ever launches. The two-children observation stays a fact about the Spike S capture
// alone; what IS re-judged is the same fact's load-bearing half, that a stopping child is not reported
// as finished.
//
// Extra top-level keys are an OBSERVATION, not a failure: field presence is conditional on session
// settings (testdata/dispatch_README.md). A key the fixture has and the live payload lacks is the
// failure, because that is a fact the replay still asserts and the platform has stopped supplying.
func rejudgeCapturedFixture(t *testing.T, version string, live map[string]json.RawMessage, tasks []map[string]json.RawMessage) {
	t.Helper()
	var fx dispatchStopFixture
	readDispatchFixture(t, "dispatch_stop_payload_2_1_258.json", &fx)
	if fx.CLIVersion != version {
		t.Logf("C-7 fold: the committed fixture is from claude %s and this run is claude %s; any delta "+
			"below is version drift, and testdata/dispatch_README.md asks for a re-capture, not an edit",
			fx.CLIVersion, version)
	}
	if len(fx.Payloads) == 0 {
		t.Fatal("the committed stop fixture carries no payloads")
	}
	var captured map[string]json.RawMessage
	if err := json.Unmarshal(fx.Payloads[0], &captured); err != nil {
		t.Fatal(err)
	}
	// PAYLOAD-CAPTURE strips it on the way to disk, so the live document cannot have it and its absence
	// is not evidence of anything.
	delete(captured, dispatchStopPayloadFreeText)

	// The live document is what PAYLOAD-CAPTURE wrote, not what the host sent, so OUR redaction is a
	// route to this failure too — and it is the half we control, which is why the message names it
	// first. A future reader sent to re-capture a fixture over a bug in dispatch_retire.go has been
	// sent to the wrong file.
	for key := range captured {
		if _, ok := live[key]; !ok {
			t.Errorf("C-7 fold: %q reached neither the live payload nor disk, though the committed "+
				"fixture carries it and TestDispatchDeny_FixtureReplay reads it as a platform fact. "+
				"Check captureDispatchStopPayload's redaction (dispatch_retire.go) BEFORE concluding "+
				"claude %s stopped sending it; only the second case calls for a re-capture", key, version)
		}
	}
	for key := range live {
		if _, ok := captured[key]; !ok {
			t.Logf("C-7 fold: claude %s sends a top-level %q the committed fixture does not carry", version, key)
		}
	}

	str := func(key string) string {
		var s string
		if raw, ok := live[key]; ok {
			_ = json.Unmarshal(raw, &s)
		}
		return s
	}
	session, parent, child, agentID := str("session_id"), str("transcript_path"), str("agent_transcript_path"), str("agent_id")
	if session == "" || parent == "" {
		t.Errorf("C-7 fold: claude %s sent session_id=%q transcript_path=%q; the release ladder reads "+
			"both as evidence hints", version, session, parent)
		return
	}
	if filepath.Base(parent) != session+".jsonl" {
		t.Errorf("C-7 fold: claude %s sent transcript_path %q for session %q. The fixture records this "+
			"field as the PARENT transcript, which is why E1 outranks E2 (dispatch_release.go)",
			version, parent, session)
	}
	if child != "" && !strings.Contains(child, filepath.Join(session, "subagents")) {
		t.Errorf("C-7 fold: claude %s sent agent_transcript_path %q, no longer under the session's "+
			"subagents dir; E1's glob derives its evidence path from that layout", version, child)
	}

	for _, task := range tasks {
		var id, status string
		_ = json.Unmarshal(task["id"], &id)
		_ = json.Unmarshal(task["status"], &status)
		if id != agentID {
			continue
		}
		// The observation E0 rests on: the host describes the child whose SubagentStop this IS as still
		// running. A stop event is not a completion event. If that ever changes, background_tasks becomes
		// a usable evidence rung and the ladder should be revisited — but not silently.
		if status != "running" {
			t.Errorf("C-7 fold: claude %s reported its own stopping child %s as %q, not \"running\"; E0 "+
				"is dark on the strength of the opposite observation", version, id, status)
		}
		return
	}
	t.Logf("C-7 fold: claude %s did not list its own stopping child %s in background_tasks", version, agentID)
}
