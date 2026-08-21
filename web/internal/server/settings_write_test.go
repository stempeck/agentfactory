package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/config"
	"github.com/stempeck/agentfactory-web/internal/exec"
)

// ---- #620 Phase 2 · the settings write path's three hardening guarantees ----
//
// The frame-lift made the settings PUT body OPAQUE to this module: it is no longer decoded into a
// typed struct on the way through, so the three properties a typed decode used to supply
// incidentally now have to be asserted directly.
//
//	AC#5.1 body cap   — nothing decodes the body, so nothing bounds it either. Only the reader does.
//	AC#5.2 audit line — nothing names the fields any more, so the log line is the only attribution
//	                    an operator surprised by a settings change will ever have.
//	AC#5.3 409        — a whole-document replace is exactly the shape that loses a concurrent edit,
//	                    so a stale read must be REFUSED rather than merged.

// settingsServer wires a Server with the given settings double and nothing else.
func settingsServer(fs SettingsService) *Server {
	return New(&fakeMutator{}, fakeAssembler{}, nil, WithSettings(fs))
}

// captureLog redirects the standard logger for the duration of one test and returns an accessor for
// what was written. Safe to do package-wide: no test in this module calls t.Parallel(), so no other
// test can be writing to the logger concurrently.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevFlags, prevPrefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	})
	return buf.String
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// AC#5.1 — an oversized settings PUT is refused AT THE READER, before the whole body is buffered
// into memory, and never reaches the settings service.
//
// The observable is the byte tally, not the status code: a handler that read 4 MiB and then rejected
// it would return the same 4xx while having done exactly the thing the cap exists to prevent.
func TestSettingsWrite_BodyCap(t *testing.T) {
	fs := &fakeSettings{}
	s := settingsServer(fs)

	huge := strings.Repeat("A", 4<<20) // 4 MiB — far over the 1 MiB cap
	body := `{"trigger_label":"` + huge + `"}`
	req := loopbackPUT("/api/settings/dispatch", body)
	cr := &countingReadCloser{r: req.Body}
	req.Body = cr
	req.ContentLength = int64(len(body))

	rec := serve(s, req)

	if cr.n > oversizeCap+4096 {
		t.Fatalf("handler read %d bytes of the request body; want it capped at ~%d "+
			"(http.MaxBytesReader missing on handleSettingsWrite)", cr.n, oversizeCap)
	}
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("oversized PUT: code = %d, want a 4xx client error (never a 5xx); body=%s", rec.Code, rec.Body.String())
	}
	if fs.writes != 0 {
		t.Fatalf("the settings service ran for an oversized body (%d writes) — the cap must reject before af is spawned", fs.writes)
	}

	// Anti-vacuity, on THE SAME SERVER: the cap must not be rejecting everything, and rejecting one
	// oversized request must not poison the server for the next one. A fresh *Server here would test
	// neither.
	if rec := serve(s, loopbackPUT("/api/settings/dispatch", `{"repos":["o/r"]}`)); rec.Code != http.StatusOK {
		t.Fatalf("an in-bounds PUT to the same server was rejected too: code = %d; body=%s", rec.Code, rec.Body.String())
	}
	if fs.writes != 1 {
		t.Fatalf("the settings service saw %d writes, want exactly 1 (the in-bounds one)", fs.writes)
	}
}

// AC#5.2 — a successful settings write leaves one metadata-only audit breadcrumb: which file, how
// many bytes, the precondition it carried, and the digest of the document submitted. No document
// bytes, and no line at all for a write that did not happen.
func TestSettingsWrite_AuditLog(t *testing.T) {
	const secret = "trigger-label-that-must-not-be-logged"
	body := `{"repos":["o/r"],"trigger_label":"` + secret + `"}`

	t.Run("a successful write is attributable", func(t *testing.T) {
		logged := captureLog(t)
		fs := &fakeSettings{}
		hash := sha256Hex([]byte(`{"repos":["previous"]}`))
		req := loopbackPUT("/api/settings/dispatch", body)
		req.Header.Set(settingsIfContentHashHeader, hash)

		if rec := serve(settingsServer(fs), req); rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}

		line := logged()
		for _, want := range []string{
			"audit:",
			"file=dispatch",
			fmt.Sprintf("bytes=%d", len(body)),
			// T3 (#621): the settings-write field is `precondition=` — the value is the request's carried
			// precondition, NOT the file's prior content. The FORMULA-write line keeps sha256_before= (its
			// value genuinely IS prior content), so `grep audit:` no longer conflates the two.
			"precondition=" + hash,
			"sha256_after=" + sha256Hex([]byte(body)),
		} {
			if !strings.Contains(line, want) {
				t.Errorf("audit line is missing %q:\n%s", want, line)
			}
		}
		// The breadcrumb is metadata: it must not become a second copy of the document.
		if strings.Contains(line, secret) {
			t.Errorf("the audit line logged document content:\n%s", line)
		}
	})

	t.Run("an unconditional write records the absence of a precondition", func(t *testing.T) {
		logged := captureLog(t)
		if rec := serve(settingsServer(&fakeSettings{}), loopbackPUT("/api/settings/dispatch", body)); rec.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200", rec.Code)
		}
		if !strings.Contains(logged(), "precondition=-") {
			t.Errorf("a headerless write must record precondition=- so the field is never ambiguous:\n%s", logged())
		}
	})

	// A line for a write that did not happen would make the trail worse than none: it would attribute
	// a change that never reached disk.
	for _, tc := range []struct {
		name string
		fs   *fakeSettings
		body string
		code int
	}{
		{"af rejected the document", &fakeSettings{writeErr: errors.New("unknown agent \"ghost\""), writeRes: exec.Result{ExitCode: 1}}, body, http.StatusUnprocessableEntity},
		{"the file is not writable", &fakeSettings{writeErr: config.ErrNotWritable}, body, http.StatusBadRequest},
		{"the read was stale", &fakeSettings{writeErr: config.ErrHashMismatch}, body, http.StatusConflict},
		{"af could not run", &fakeSettings{writeErr: errors.New("executable file not found in $PATH")}, body, http.StatusBadGateway},
		{"the body was empty", &fakeSettings{}, "   ", http.StatusBadRequest},
	} {
		t.Run("no audit line when "+tc.name, func(t *testing.T) {
			logged := captureLog(t)
			rec := serve(settingsServer(tc.fs), loopbackPUT("/api/settings/dispatch", tc.body))
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d; body=%s", rec.Code, tc.code, rec.Body.String())
			}
			if strings.Contains(logged(), "audit: settings write") {
				t.Errorf("a failed write left an audit breadcrumb claiming it succeeded:\n%s", logged())
			}
		})
	}
}

// AC#5.3 — a stale read is a 409 Conflict, not a 422. The distinction is load-bearing for the
// client: 422 means "your document is wrong, fix it"; 409 means "your document is fine but the file
// moved underneath you — reload and re-apply".
//
// The case ORDER inside the handler is what this pins. af signals a failed precondition with the
// same non-zero exit code as a validation rejection, so an ErrHashMismatch arm placed AFTER the
// exit-code arm would be unreachable and every conflict would present as a 422.
func TestSettingsWrite_ConflictOnHashMismatch(t *testing.T) {
	body := `{"repos":["o/r"]}`

	t.Run("a conflict carried on a non-zero exit is still a conflict", func(t *testing.T) {
		fs := &fakeSettings{
			writeErr: fmt.Errorf("af rejected the precondition: %w", config.ErrHashMismatch),
			writeRes: exec.Result{ExitCode: 1}, // the arm-ordering trap
		}
		rec := serve(settingsServer(fs), loopbackPUT("/api/settings/dispatch", body))
		if rec.Code != http.StatusConflict {
			t.Fatalf("code = %d, want 409 — the ErrHashMismatch arm must precede the non-zero-exit arm; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"ok":false`) {
			t.Errorf("a 409 envelope must not claim success: %s", rec.Body.String())
		}
	})

	t.Run("a conflict detected before exec is a conflict", func(t *testing.T) {
		rec := serve(settingsServer(&fakeSettings{writeErr: config.ErrHashMismatch}), loopbackPUT("/api/settings/dispatch", body))
		if rec.Code != http.StatusConflict {
			t.Fatalf("code = %d, want 409; body=%s", rec.Code, rec.Body.String())
		}
	})

	// A refusal must reach the operator carrying the tier row's RECORDED reason, not a tidy constant.
	// Driven through the REAL config.Service, because a fake injecting the bare sentinel would pass
	// whatever the handler chose to say — the composition is the thing under test, and it is the only
	// place the Reason column's whole purpose (telling the operator WHY, from the table) is observable.
	t.Run("a read-only file explains itself from the tier row", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755); err != nil {
			t.Fatal(err)
		}
		s := settingsServer(config.New(root, nil))
		rec := serve(s, loopbackPUT("/api/settings/factory", `{"type":"factory","name":"demo","version":1}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("code = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		for _, want := range []string{"recorded decision C-9", "af config factory set", "factory"} {
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("the 400 body does not carry %q from the tier row — the operator is told \"no\" "+
					"without being told why:\n%s", want, rec.Body.String())
			}
		}
	})

	// The negative arms: 409 must be reserved for staleness, or the client's reload-and-retry loop
	// will fire on ordinary validation failures and silently re-apply the operator's edit.
	for _, tc := range []struct {
		name string
		fs   *fakeSettings
		code int
	}{
		{"a validation rejection stays 422", &fakeSettings{writeErr: errors.New("unknown agent \"ghost\""), writeRes: exec.Result{ExitCode: 1}}, http.StatusUnprocessableEntity},
		{"an unwritable file stays 400", &fakeSettings{writeErr: config.ErrNotWritable}, http.StatusBadRequest},
		{"a precondition that is not a digest is 400, not 409", &fakeSettings{writeErr: config.ErrBadPrecondition}, http.StatusBadRequest},
		{"an infrastructure failure stays 502", &fakeSettings{writeErr: errors.New("executable file not found in $PATH")}, http.StatusBadGateway},
		{"a clean write stays 200", &fakeSettings{}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(settingsServer(tc.fs), loopbackPUT("/api/settings/dispatch", body))
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d; body=%s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

// T2 (#621) — an "af too old" child failure (ErrAfTooOld) is a 502, not a 422. The document is fine;
// the local af binary is too old to process the forwarded --if-content-hash flag or a new subcommand.
// The trap this pins is arm ORDER: an af-too-old failure exits non-zero, so a naïve handler lands it on
// the res.ExitCode!=0 → 422 arm. The ErrAfTooOld arm must precede it, exactly as ErrHashMismatch does.
func TestSettingsWrite_AfTooOldIs502(t *testing.T) {
	body := `{"repos":["o/r"]}`
	fs := &fakeSettings{
		writeErr: fmt.Errorf("af config: exit 1: Error: unknown flag: --if-content-hash: %w", config.ErrAfTooOld),
		writeRes: exec.Result{ExitCode: 1}, // the arm-ordering trap: a non-zero exit must NOT pre-empt the 502 arm
	}
	rec := serve(settingsServer(fs), loopbackPUT("/api/settings/dispatch", body))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502 — the ErrAfTooOld arm must precede the non-zero-exit 422 arm; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Errorf("a 502 envelope must not claim success: %s", rec.Body.String())
	}
}

// T3 (#621) protective/characterization — the FORMULA-write audit line (server.go:1229) keeps
// sha256_before=, whose value genuinely IS the file's prior on-disk content. This is the ONLY test that
// pins that line; it is GREEN now and after the T3 settings-write rename, and goes RED the instant a
// careless rename of `sha256_before` sweeps both audit lines together.
func TestFormulaWrite_AuditLineNotRenamed(t *testing.T) {
	logged := captureLog(t)
	s, _ := formulaServer(t, &fakeFormulaStore{}, &fakeGenerator{}, okVerdict)
	rec := serve(s, tokPUT("/api/formulas/foo", `{"text":"[meta]\nname='foo'\n","base_sha256":""}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("formula save: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	line := logged()
	for _, want := range []string{"audit: formula write", "name=foo", "sha256_before=", "sha256_after="} {
		if !strings.Contains(line, want) {
			t.Fatalf("formula-write audit line missing %q — did the T3 rename leak across both audit lines?:\n%s", want, line)
		}
	}
	// precondition= belongs to the SETTINGS-write line ONLY; the formula line's before-hash is real content.
	if strings.Contains(line, "precondition=") {
		t.Fatalf("the formula-write audit line was renamed to precondition= — T3 renames ONLY the settings-write field:\n%s", line)
	}
}
