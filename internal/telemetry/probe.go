package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// nativeSignalPaths are the addresses an agent session's own instrumentation derives from the
// configured endpoint. It appends /v1/{signal} to the base, which is why the base alone decides
// whether the token counts arrive: no configuration value overrides these per signal.
//
// Traces are excluded because the af side sends those itself, through a path it can be told
// explicitly — that plane is covered by ProbeExport.
var nativeSignalPaths = []string{"/v1/logs", "/v1/metrics"}

// ProbeResult is a reachability verdict for one address, shaped so a caller can render it
// without re-deciding anything. Detail never contains a header value.
type ProbeResult struct {
	Label  string
	URL    string
	Status int
	Err    error
}

// OK reports whether the backend accepted the probe. A 2xx is acceptance; everything else,
// including a 404 on a path the backend does not serve, is not.
func (r ProbeResult) OK() bool { return r.Err == nil && r.Status/100 == 2 }

// Summary renders the verdict in the operator's terms rather than the protocol's.
func (r ProbeResult) Summary() string {
	switch {
	case r.Err != nil:
		return fmt.Sprintf("%s: unreachable (%v)", r.Label, r.Err)
	case r.Status == 404:
		return fmt.Sprintf("%s: reachable but the address is not served (HTTP 404) — data sent here is discarded", r.Label)
	case r.Status == 401 || r.Status == 403:
		return fmt.Sprintf("%s: reachable but the credential was rejected (HTTP %d)", r.Label, r.Status)
	case r.Status/100 != 2:
		return fmt.Sprintf("%s: reachable but refused the data (HTTP %d)", r.Label, r.Status)
	default:
		return fmt.Sprintf("%s: reachable (HTTP %d)", r.Label, r.Status)
	}
}

// Probe answers the question the status surface previously declined to ask: can anything this
// factory sends actually arrive?
//
// It posts an EMPTY payload to each address the two planes use. Empty is deliberate — a probe
// that shipped a real span would inflate the closed-step count the zero-join canary reads, so a
// diagnostic would corrupt the data it exists to diagnose.
//
// Both planes are probed because they are configured differently and fail independently. The af
// plane is handed a path it can be told explicitly; the native plane derives its own from the
// base. A check aimed only at the af path is exactly the check that already existed at install
// time, and it passed while the expensive half returned 404 on every request.
//
// Every result is returned rather than the first failure: an operator debugging an empty
// dashboard needs to know which half is broken, and "the first thing that went wrong" is often
// the less informative half.
// It is a package-level var for the same reason Export is: a caller in another package must be
// able to exercise its own rendering and error handling without a socket.
var Probe = func(cfg config.TelemetryConfig) []ProbeResult {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil
	}
	out := make([]ProbeResult, 0, 1+len(nativeSignalPaths))
	out = append(out, probeOne("step timings", tracesURL(cfg), `{"resourceSpans":[]}`, cfg))
	for _, p := range nativeSignalPaths {
		out = append(out, probeOne(nativeSignalLabel(p), joinEndpoint(cfg.Endpoint, p), emptyPayloadFor(p), cfg))
	}
	return out
}

func nativeSignalLabel(path string) string {
	if path == "/v1/logs" {
		return "token usage"
	}
	return "session metrics"
}

func emptyPayloadFor(path string) string {
	if path == "/v1/logs" {
		return `{"resourceLogs":[]}`
	}
	return `{"resourceMetrics":[]}`
}

// joinEndpoint mirrors tracesURL's joining rule so the probe cannot disagree with the exporter
// about what the address is.
func joinEndpoint(endpoint, path string) string {
	return strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(path, "/")
}

// probeTimeout floors the configured export budget. That budget is tuned for a hook path where
// half a second is the point; a diagnostic an operator is watching needs long enough to
// distinguish "slow" from "absent", and a ceiling so `af telemetry status` cannot hang on an
// operator-set value.
func probeTimeout(cfg config.TelemetryConfig) time.Duration {
	const floor, ceiling = 2 * time.Second, 10 * time.Second
	d := time.Duration(cfg.ExportTimeoutMS) * time.Millisecond
	if d < floor {
		d = floor
	}
	if d > ceiling {
		d = ceiling
	}
	return d
}

// probeOne sends one empty payload and reports what came back.
//
// It goes through the same httpDo seam the exporter uses, which is what lets the whole surface be
// tested with no socket and no backend — the condition every test in this repository is held to.
// Header values are applied but never returned in any field of the result.
func probeOne(label, url, payload string, cfg config.TelemetryConfig) ProbeResult {
	res := ProbeResult{Label: label, URL: url}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout(cfg))
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(payload)))
	if err != nil {
		res.Err = fmt.Errorf("building the request: %w", err)
		return res
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	for _, name := range sortedKeys(cfg.Headers) {
		// An unresolved reference is the caller's bug, not the backend's, and sending it as a
		// credential would be both wrong and confusing to diagnose. Naming it here means the
		// status surface reports the real problem rather than an authentication failure.
		if strings.HasPrefix(cfg.Headers[name], secretRefPrefix) {
			res.Err = fmt.Errorf("header %q is still an unresolved secret reference", name)
			return res
		}
		req.Header.Set(name, cfg.Headers[name])
	}

	resp, err := httpDo(req)
	if err != nil {
		res.Err = err
		return res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorBodyLimit))
	res.Status = resp.StatusCode
	return res
}
