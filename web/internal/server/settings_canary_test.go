package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/config"
)

// ---- #620 Phase 2 · AC#6: the payload-level secrets canary ----
//
// Every earlier secrets test in this module asserted a STRUCT SHAPE: "the mirror type declares no
// auth_token field, therefore none can be serialized". The frame-lift retires that argument — the raw
// tier serves whole documents nobody declared a type for — so the guarantee has to be re-established
// at the only place that actually matters: the bytes on the wire.
//
// This test plants a unique, greppable token in EVERY secret-bearing location a real factory has,
// drives the REAL config.Service through the REAL HTTP handler, and requires that none of them
// appears in the response. Distinct tokens per location, so a failure names its own source; none of
// them shares a prefix with the agent and profile NAMES the console legitimately exposes, which a
// single "CANARY-" marker would have collided with.

// canary is one planted secret: where it lives and the token that proves it stayed there.
type canary struct {
	where string
	token string
}

// AC#6 — no secret reaches the browser, asserted over the full HTTP response body.
func TestSettings_CanaryNotInPayload(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Every token is ASCII-alphanumeric so JSON escaping cannot hide it from a substring scan, and
	// each is unique so a hit identifies the exact file that leaked.
	planted := []canary{
		{"agents.json auth_token", "zq7f3aAgentAuthToken"},
		{"agents.json base_url", "zq7f3aAgentBaseUrl"},
		{"agents.json model override", "zq7f3aAgentModelOverride"},
		{"models.json profile body value", "zq7f3aModelsProfileValue"},
		{"models.json profile body key", "zq7f3aModelsProfileKey"},
		{"telemetry.json header", "zq7f3aTelemetryHeader"},
		{"telemetry.json endpoint", "zq7f3aTelemetryEndpoint"},
		{"build-host.json", "zq7f3aBuildHost"},
		{"litellm.yaml", "zq7f3aLitellmKey"},
		{".agentfactory/secrets/gateway.env", "zq7f3aSecretsDirContent"},
	}

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("factory.json", `{"type":"factory","version":1,"name":"demo"}`)
	write("agents.json", `{"agents":{"rootcause":{"type":"specialist","description":"analyst","formula":"rootcause",`+
		`"model":"zq7f3aAgentModelOverride","base_url":"https://zq7f3aAgentBaseUrl.internal","auth_token":"zq7f3aAgentAuthToken"}}}`)
	write("models.json", `{"models":{"loopback":{"zq7f3aModelsProfileKey":"zq7f3aModelsProfileValue"}}}`)
	write("telemetry.json", `{"endpoint":"https://zq7f3aTelemetryEndpoint.internal","headers":{"authorization":"Bearer zq7f3aTelemetryHeader"}}`)
	write("build-host.json", `{"host":"zq7f3aBuildHost"}`)
	write("litellm.yaml", "model_list:\n  - api_key: zq7f3aLitellmKey\n")
	write(filepath.Join("secrets", "gateway.env"), "ANTHROPIC_AUTH_TOKEN=zq7f3aSecretsDirContent\n")
	// The raw tier's documents ARE served in full. These two are the anti-vacuity control: if the
	// scan below cannot see them either, it is scanning nothing.
	write("dispatch.json", `{"repos":["o/r"],"trigger_label":"zq7f3aExposedTriggerLabel","mappings":[{"labels":["bug"],"agent":"rootcause"}]}`)

	// The REAL config.Service — a fake here would only prove the fake is clean. The nil af seam is
	// the read path's supported configuration (the schema fingerprint degrades to "").
	s := New(&fakeMutator{}, fakeAssembler{}, nil, WithSettings(config.New(root, nil)))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, c := range planted {
		if strings.Contains(body, c.token) {
			t.Errorf("the settings payload leaked the canary planted in %s (%q):\n%s", c.where, c.token, body)
		}
	}

	// Anti-vacuity, in both directions. The raw tier must have served a whole document...
	if !strings.Contains(body, "zq7f3aExposedTriggerLabel") {
		t.Fatalf("the raw tier served nothing — every canary assertion above is vacuous:\n%s", body)
	}
	// ...and the projections must have served the names they exist to serve, so "no leak" is not
	// being achieved by serving nothing at all.
	for _, want := range []string{`"rootcause"`, `"loopback"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the payload is missing %s — the projections returned nothing, so the canary scan proves little", want)
		}
	}
	// The secrets DIRECTORY is never named in a payload either: an operator's file listing is itself
	// information the console has no business publishing (C-1).
	if strings.Contains(body, "gateway.env") {
		t.Errorf("the settings payload names a file inside .agentfactory/secrets/:\n%s", body)
	}
}

// C-1's static half. The canary above proves nothing LEAKED on this request; this proves there is no
// code that could read the secrets directory on any other one. tier.go is the single permitted
// mention: its disposition row names the directory precisely so the console can tell an operator the
// directory is off limits — and that row deliberately carries no path constructor.
//
// The markers are the directory as a PATH TOKEN — a whole quoted path component, or a name followed
// by a separator — never the bare English word. Prose that says "sessions print secrets" is a comment
// explaining a redaction, and a check that fired on it would be silenced by deleting the explanation,
// which is the wrong direction.
func TestSettings_SecretsDirectoryIsNeverRead(t *testing.T) {
	moduleRoot := webModuleRoot(t)
	markers := []string{`.agentfactory/secrets`, `"secrets"`, `secrets/`}

	var offenders []string
	scanned, permitted := 0, 0
	err := filepath.WalkDir(moduleRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		named := false
		for _, m := range markers {
			if strings.Contains(string(src), m) {
				named = true
				break
			}
		}
		if !named {
			return nil
		}
		rel := filepath.ToSlash(mustRel(t, moduleRoot, path))
		if rel == "internal/config/tier.go" {
			permitted++
			return nil
		}
		offenders = append(offenders, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", moduleRoot, err)
	}
	if scanned == 0 {
		t.Fatalf("scanned 0 non-test .go files under %s — the walk found nothing, so this proves nothing", moduleRoot)
	}
	// Anti-vacuity: the markers must actually match something, or a typo in one of them would make
	// this test pass forever.
	if permitted == 0 {
		t.Fatalf("the markers %v matched nothing at all across %d files — not even tier.go's disposition "+
			"row, which names the directory verbatim. The scan is broken, not clean.", markers, scanned)
	}
	if len(offenders) != 0 {
		t.Errorf("non-test web-module source names the secrets directory as a path: %v — the console must "+
			"never read, list, or name it outside the tier table's disposition row (C-1)", offenders)
	}
}

func mustRel(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	if err != nil {
		t.Fatalf("rel(%s, %s): %v", base, path, err)
	}
	return rel
}

// webModuleRoot walks up from the test's working directory (which `go test` pins to the package
// source directory) to the web module's own go.mod. It never t.Skips: a resolver that skipped would
// let this check silently vanish from CI.
func webModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	start := dir
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate the web module's go.mod walking up from %s", start)
		}
		dir = parent
	}
}
