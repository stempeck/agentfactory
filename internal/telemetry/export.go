package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

const (
	defaultTracesPath = "/v1/traces"
	contentTypeJSON   = "application/json"

	// errorBodyLimit bounds how much of a failure response is read and quoted. A backend
	// under load can answer with a very large body, and this runs inline in a lifecycle verb.
	errorBodyLimit = 512

	// secretRefPrefix marks a header value that is a reference to a secret file rather than
	// the secret itself. Matches the convention the rest of the configuration uses.
	secretRefPrefix = "file:"
)

// httpDo is the transport seam. It exists so the status-code handling below — and the cursor
// decision that depends on it — can be exercised with no socket and no backend, which is
// required of every test in this repository. It is separate from the Export seam on purpose:
// substituting Export would move the status-code logic into the substitute, leaving the rule
// that actually matters untested.
var httpDo = func(req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req)
}

// Export posts one encoded payload to the configured backend and reports whether it was
// accepted. It is a package-level var so callers can be tested without a network.
//
// The timeout is taken from the configuration and not second-guessed. Loading normalises it,
// so a non-positive value can only come from a hand-built value; it produces an immediately
// expired deadline, which fails loudly at once rather than quietly becoming unbounded.
//
// A note on secrets: this package is handed no factory root, so it cannot check that a secret
// file path resolves inside one. Rather than dereference a path it cannot vet, it refuses —
// dereferencing belongs to the caller that knows the root. Header values are never echoed,
// including into errors, and a value that appears in a backend's error text is redacted out
// of the message this returns.
var Export = func(cfg config.TelemetryConfig, payload []byte) error {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return errors.New("telemetry: no export endpoint is configured")
	}
	// Sorted so that a configuration with several unresolved references always names the same
	// one, and an operator chasing the error is not sent somewhere different on each run.
	for _, name := range sortedKeys(cfg.Headers) {
		if strings.HasPrefix(cfg.Headers[name], secretRefPrefix) {
			return fmt.Errorf("telemetry: header %q is still an unresolved secret reference; "+
				"it must be dereferenced by the caller that knows the factory root", name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(cfg.ExportTimeoutMS)*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tracesURL(cfg), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telemetry: building export request: %w", err)
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	for _, name := range sortedKeys(cfg.Headers) {
		req.Header.Set(name, cfg.Headers[name])
	}

	resp, err := httpDo(req)
	if err != nil {
		return fmt.Errorf("telemetry: export request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	// Drain whatever is left so the connection can be reused rather than abandoned.
	_, _ = io.Copy(io.Discard, resp.Body)

	// The status code is the entire contract. A 2xx carrying a partial-success body still
	// counts as accepted, because the specification has the client not retry it — treating it
	// as a failure would resend the same batch forever.
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("telemetry: backend rejected the export with %s: %s",
			resp.Status, redactHeaderValues(strings.TrimSpace(string(body)), cfg.Headers))
	}
	return nil
}

// tracesURL joins the endpoint to the traces path without doubling the separator, and falls
// back to the specification's default path when none is configured.
func tracesURL(cfg config.TelemetryConfig) string {
	path := cfg.OTLPHTTPPathTraces
	if path == "" {
		path = defaultTracesPath
	}
	return strings.TrimRight(cfg.Endpoint, "/") + "/" + strings.TrimLeft(path, "/")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// redactHeaderValues removes any configured credential that a backend has echoed back. A
// service that quotes the token it rejected is common, and this error text reaches terminals
// and logs.
func redactHeaderValues(text string, headers map[string]string) string {
	for _, value := range headers {
		if value == "" {
			continue
		}
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	if text == "" {
		return "(no response body)"
	}
	return text
}

// DrainResult reports what one export attempt did, so a caller can tell "nothing to send" from
// "sent and accepted" from "sent and refused" without inspecting the error.
type DrainResult struct {
	Sent     int
	Exported bool
	Payload  []byte
}

// Drain exports at most max unexported records for one agent and advances the export cursor
// only if the backend accepted them.
//
// That conditional is the point. Anything other than acceptance — a rejection, a redirect, a
// timeout, a refused connection — leaves the cursor exactly as it was, so the same records are
// offered again on the next attempt. Delivery is therefore at-least-once, and duplicates are
// harmless because span identities are derived from the record rather than from the attempt.
func Drain(telemetryDir string, cfg config.TelemetryConfig, agent string, max int) (DrainResult, error) {
	var out DrainResult

	pending, _, err := ReadEvents(telemetryDir, Filter{Agent: agent, Unexported: true, Limit: max})
	if err != nil {
		return out, err
	}
	if len(pending) == 0 {
		return out, nil
	}

	payload, unrenderable, err := encodeTraces(pending)
	if err != nil {
		if errors.Is(err, ErrNoRecords) {
			if unrenderable > 0 {
				// Every closed step in this batch is permanently unrenderable. Leaving the
				// cursor where it is would re-read the same doomed records on every attempt
				// and never reach the good ones behind them, so they are written off — counted
				// as loss, which is what makes stepping over them honest rather than silent.
				return out, writeOffUnexportable(telemetryDir, agent, pending, unrenderable)
			}
			// Records are pending but none of them is a closed step, so there is nothing a
			// trace export can carry yet. Not a failure, and not a reason to move the cursor.
			return out, nil
		}
		return out, err
	}
	out.Sent = len(pending)
	out.Payload = payload

	if err := Export(cfg, payload); err != nil {
		return out, err
	}

	if unrenderable > 0 {
		if err := countUnexportable(telemetryDir, agent, unrenderable); err != nil {
			return out, err
		}
	}
	if err := advanceCursor(telemetryDir, agent, pending); err != nil {
		// The batch is delivered but the bookmark did not move, so it will be sent again.
		// Reported rather than swallowed, because a cursor that never advances turns every
		// export into a full resend and only the count would ever show it.
		return out, err
	}
	out.Exported = true
	return out, nil
}
