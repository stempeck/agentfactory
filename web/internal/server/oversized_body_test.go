package server

import (
	"io"
	"strings"
	"testing"
)

// countingReadCloser wraps a request body and tallies how many bytes were actually
// read from it. That tally is the observable which distinguishes "rejected at the
// handler by http.MaxBytesReader" (the read aborts at ~1 MiB) from "buffered whole
// into memory first" (json.Decode reads the entire multi-MiB body before any cap).
// THREAD-2 (PR #520).
type countingReadCloser struct {
	r io.Reader
	n int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	m, err := c.r.Read(p)
	c.n += int64(m)
	return m, err
}

func (c *countingReadCloser) Close() error { return nil }

// oversizeCap mirrors the handler cap (http.MaxBytesReader) and the store's maxBodyLen.
const oversizeCap = 1 << 20

// THREAD-2: an oversized formula PUT body must be rejected at the handler, BEFORE
// json.Decode buffers the whole thing into memory, and must never reach the store.
func TestHandleFormulaPut_OversizedBody_CappedAtHandler(t *testing.T) {
	fs := &fakeFormulaStore{}
	s, _ := formulaServer(t, fs, &fakeGenerator{}, okVerdict)

	huge := strings.Repeat("A", 4<<20) // 4 MiB payload — far over the 1 MiB cap
	body := `{"text":"` + huge + `","base_sha256":""}`
	req := tokPUT("/api/formulas/foo", body)
	cr := &countingReadCloser{r: req.Body}
	req.Body = cr
	req.ContentLength = int64(len(body))

	rec := serve(s, req)

	if cr.n > oversizeCap+4096 {
		t.Fatalf("handler read %d bytes of the request body; want it capped at ~%d "+
			"(http.MaxBytesReader missing on handleFormulaPut)", cr.n, oversizeCap)
	}
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("oversized PUT: code = %d, want a 4xx client error (never a 5xx)", rec.Code)
	}
	if fs.writes != 0 {
		t.Fatalf("store.Write ran for an oversized body (got %d); the cap must reject before the store", fs.writes)
	}
}

// THREAD-2: same guarantee for the factory-generate POST handler. The huge padding is an
// unknown field placed BEFORE "confirm" so that, once capped, the decoder never reaches
// (and never sets) Confirm — the handler falls through to its 422 and Start never runs.
func TestHandleGeneratePost_OversizedBody_CappedAtHandler(t *testing.T) {
	g := &fakeGenerator{}
	s, _ := formulaServer(t, &fakeFormulaStore{}, g, okVerdict)

	huge := strings.Repeat("A", 4<<20)
	body := `{"pad":"` + huge + `","confirm":true}`
	req := tokPOST("/api/factory/generate", body)
	cr := &countingReadCloser{r: req.Body}
	req.Body = cr
	req.ContentLength = int64(len(body))

	serve(s, req)

	if cr.n > oversizeCap+4096 {
		t.Fatalf("generate handler read %d bytes of the request body; want it capped at ~%d "+
			"(http.MaxBytesReader missing on handleGeneratePost)", cr.n, oversizeCap)
	}
	if g.starts != 0 {
		t.Fatalf("generator.Start ran for an oversized body (got %d); the cap must reject before Start", g.starts)
	}
}
