package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

type harnessWindowProbe struct {
	ModelID        string // the modelUsage key the harness reported; never a record's model_id, which is the profile's ANTHROPIC_MODEL
	ContextWindow  int64
	Unrecognized   bool
	HarnessVersion string // harnessVersionOf the stdout of `<claude> --version` run under the probe's own scratch env: the gate's reduction, so the two strings agree for one binary
}

var errHarnessAbsent = errors.New("claude not on PATH")

var errProbeVersionMismatch = errors.New("the probe ran another Claude Code version")

var probeHarnessWindowFn = probeHarnessWindow

const (
	harnessProbeCheckTimeout  = 60 * time.Second
	harnessProbeLaunchTimeout = 15 * time.Second
	harnessUnrecognizedMarker = "[claude-code:unrecognized_model]"
)

// probeHarnessWindow runs the installed harness for one turn against a loopback stub and reads the
// context window it applies to the profile's model. Both scratch directories are removed on every
// path: a probe otherwise leaves about 284 KB behind, a transcript included.
func probeHarnessWindow(ctx context.Context, profile map[string]string) (harnessWindowProbe, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return harnessWindowProbe{}, errHarnessAbsent
	}
	scratch, err := os.MkdirTemp("", "af-window-probe-home-")
	if err != nil {
		return harnessWindowProbe{}, err
	}
	defer os.RemoveAll(scratch)
	// HOME, CLAUDE_CONFIG_DIR and TMPDIR all exist on a real install, so the harness is measured on
	// that layout rather than on one it must create for itself.
	for _, dir := range []string{filepath.Join(scratch, "home", ".claude"), filepath.Join(scratch, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return harnessWindowProbe{}, err
		}
	}
	cwd, err := os.MkdirTemp("", "af-window-probe-cwd-")
	if err != nil {
		return harnessWindowProbe{}, err
	}
	defer os.RemoveAll(cwd)
	base, stop, err := startHarnessProbeStub(ctx)
	if err != nil {
		return harnessWindowProbe{}, err
	}
	defer stop()
	env := harnessProbeEnv(profile, scratch, base)

	run := func(args ...string) (stdout, stderr []byte, err error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = cwd
		cmd.Env = env
		cmd.WaitDelay = time.Second
		// The harness starts children of its own, and killing only it on timeout would orphan them.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err = cmd.Run()
		return out.Bytes(), errOut.Bytes(), err
	}
	versionOut, _, err := run("--version")
	if err != nil {
		return harnessWindowProbe{}, fmt.Errorf("claude --version: %w", err)
	}
	version := harnessVersionOf(string(versionOut))

	id := profile["ANTHROPIC_MODEL"]
	args := []string{"-p", "ok", "--output-format", "json"}
	if id != "" {
		args = append(args, "--model", id)
	}
	stdout, stderr, err := run(args...)
	if err != nil {
		return harnessWindowProbe{}, fmt.Errorf("claude -p: %w", err)
	}
	var result struct {
		ModelUsage map[string]map[string]json.RawMessage `json:"modelUsage"`
	}
	if err := json.Unmarshal(stdout, &result); err != nil {
		return harnessWindowProbe{}, fmt.Errorf("claude -p printed no JSON result: %w", err)
	}
	// A single key is taken whatever its name: the harness keys modelUsage by the model it requested
	// and strips a [1m] suffix from it, so modelUsage[ANTHROPIC_MODEL] misses a suffixed id.
	key := ""
	switch {
	case len(result.ModelUsage) == 1:
		for k := range result.ModelUsage {
			key = k
		}
	case len(result.ModelUsage) > 1 && id != "":
		if _, ok := result.ModelUsage[id]; ok {
			key = id
		}
	}
	if key == "" {
		return harnessWindowProbe{}, fmt.Errorf("modelUsage holds %d entries and none is the requested model %q", len(result.ModelUsage), id)
	}
	var window int64
	if raw, ok := result.ModelUsage[key]["contextWindow"]; !ok || json.Unmarshal(raw, &window) != nil || window <= 0 {
		return harnessWindowProbe{}, fmt.Errorf("modelUsage[%q] has no positive contextWindow", key)
	}
	return harnessWindowProbe{
		ModelID:        key,
		ContextWindow:  window,
		Unrecognized:   bytes.Contains(stderr, []byte(harnessUnrecognizedMarker)),
		HarnessVersion: version,
	}, nil
}

// harnessProbeEnv starts from nothing and takes an allowlist from the profile: a profile-borne
// credential, endpoint switch, proxy or NODE_OPTIONS, all of which validateModelProfile accepts as
// any valid env-var name, must never reach a child that only needs to report a window.
func harnessProbeEnv(profile map[string]string, scratch, baseURL string) []string {
	var env []string
	for _, k := range append([]string{"ANTHROPIC_MODEL"}, config.WindowDecidingEnvKeys...) {
		if v := profile[k]; v != "" {
			env = append(env, k+"="+v)
		}
	}
	return append(env,
		"HOME="+filepath.Join(scratch, "home"),
		"CLAUDE_CONFIG_DIR="+filepath.Join(scratch, "home", ".claude"),
		"TMPDIR="+filepath.Join(scratch, "tmp"),
		"PATH="+os.Getenv("PATH"),
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_API_KEY=af-probe-dummy",
		"NO_PROXY=127.0.0.1,localhost",
		"DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
}

// startHarnessProbeStub answers every request at once, an unknown one with a 404, so no request the
// harness makes waits on the stub.
func startHarnessProbeStub(ctx context.Context) (baseURL string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: http.HandlerFunc(serveHarnessProbe), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), func() { _ = srv.Close() }, nil
}

func serveHarnessProbe(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/count_tokens":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":1}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		writeHarnessProbeTurn(w, req.Model)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"af window probe stub"}}`))
	}
}

func writeHarnessProbeTurn(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	for _, ev := range []struct {
		name    string
		payload map[string]any
	}{
		{"message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_probe", "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
		}}},
		{"content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}},
		{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "ok"}}},
		{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
		{"message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1}}},
		{"message_stop", map[string]any{"type": "message_stop"}},
	} {
		data, _ := json.Marshal(ev.payload)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, data)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}
