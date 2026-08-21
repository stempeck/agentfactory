package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

func runFingerprint(t *testing.T) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := runConfigFingerprint(cmd, nil)
	return buf.String(), err
}

// TestConfigFingerprint covers issue #620 Phase 1 AC 5.
//
// Nothing let a consumer ask a RUNNING af binary what config schema it speaks, so the web
// module's mirror structs could drift from internal/config with nothing failing. The verb
// recomputes the congruence fixture content in-memory by the same reflection walk that
// produces the committed fixtures, hashes it, and reports the digest — design-doc.md:194:
// "The running binary must report its own schema, not a build-time constant."
func TestConfigFingerprint(t *testing.T) {
	t.Run("emits the ok envelope and exits 0", func(t *testing.T) {
		out, err := runFingerprint(t)
		// Like the other --json read commands, this always exits 0 and a consumer branches on
		// `state`. A read verb that exits non-zero forces every caller to distinguish "the
		// factory is unusual" from "the command is missing".
		if err != nil {
			t.Fatalf("the fingerprint verb must always exit 0; got %v (out=%q)", err, out)
		}

		var got struct {
			State       string `json:"state"`
			Fingerprint string `json:"fingerprint"`
			Error       string `json:"error"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("stdout must be a single JSON document; got %q (%v)", out, err)
		}
		if got.State != "ok" {
			t.Errorf(`state must be "ok"; got %q (error=%q)`, got.State, got.Error)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(got.Fingerprint) {
			t.Errorf("fingerprint must be a lowercase hex SHA-256; got %q", got.Fingerprint)
		}
	})

	t.Run("stdout carries nothing but the envelope", func(t *testing.T) {
		// A consumer pipes this into a JSON parser. Any banner or warning on stdout breaks it.
		out, err := runFingerprint(t)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if strings.Count(strings.TrimSpace(out), "\n") != 0 {
			t.Errorf("stdout must be exactly one line; got %q", out)
		}
	})

	t.Run("is stable across runs", func(t *testing.T) {
		// A fingerprint that varies run to run cannot drive a skew banner: every page load
		// would report drift.
		first, err := runFingerprint(t)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		for i := 0; i < 3; i++ {
			again, err := runFingerprint(t)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if again != first {
				t.Fatalf("the fingerprint must be deterministic;\nfirst=%s\nagain=%s", first, again)
			}
		}
	})

	t.Run("matches the library value the fixtures are generated from", func(t *testing.T) {
		// The verb must not compute its own digest by a second, parallel walk — two copies of
		// the walk are the exact drift the phase exists to prevent, and the congruence drift
		// test would not catch a divergence between them.
		want, err := config.SchemaFingerprint()
		if err != nil {
			t.Fatalf("config.SchemaFingerprint: %v", err)
		}
		out, err := runFingerprint(t)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if !strings.Contains(out, want) {
			t.Errorf("the verb must report the library's fingerprint %q; got %q", want, out)
		}
	})

	t.Run("is schema-sensitive", func(t *testing.T) {
		// The property that makes the fingerprint worth transporting: a canonical struct gaining
		// a field must change it. Asserting that honestly means changing the schema, which a
		// test cannot do — so instead assert the digest is a function of the fixture BYTES,
		// which every field addition necessarily changes. A pinned hex literal would prove
		// nothing about sensitivity and would make every legitimate schema change a two-file
		// edit; hashing the same bytes the drift test byte-compares ties the two together.
		fixtures, err := config.CongruenceFixtures()
		if err != nil {
			t.Fatalf("config.CongruenceFixtures: %v", err)
		}
		if len(fixtures) == 0 {
			t.Fatal("no fixtures were generated, so the fingerprint covers nothing")
		}
		base, err := config.SchemaFingerprint()
		if err != nil {
			t.Fatalf("SchemaFingerprint: %v", err)
		}
		if perturbed := config.FingerprintOf(mutateOneFixture(fixtures)); perturbed == base {
			t.Error("a change to the fixture content must change the fingerprint, or a new struct field would ship invisibly")
		}
	})

	t.Run("a failure is reported in the payload, still exiting 0", func(t *testing.T) {
		// The exit-0 contract has to hold on the FAILURE path too, or a consumer cannot tell
		// "this af cannot describe its schema" from "this af has no such command" — and those
		// need different responses. SchemaFingerprint can only fail if marshaling the canonical
		// structs fails, which no test can provoke, so drive the emitter directly.
		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		if err := emitConfigFingerprintError(cmd, errors.New("schema walk exploded")); err != nil {
			t.Fatalf("the error path must still exit 0; got %v", err)
		}

		var got struct {
			State string `json:"state"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("the error payload must be a single JSON document; got %q (%v)", buf.String(), err)
		}
		if got.State != "error" {
			t.Errorf(`state must be "error" so a consumer can branch on it; got %q`, got.State)
		}
		if !strings.Contains(got.Error, "schema walk exploded") {
			t.Errorf("the payload must carry the cause; got %q", got.Error)
		}
	})

	t.Run("registered under config with a --json flag", func(t *testing.T) {
		var found *cobra.Command
		for _, c := range configCmd.Commands() {
			if c.Name() == "fingerprint" {
				found = c
			}
		}
		if found == nil {
			t.Fatal("`config fingerprint` is not registered under configCmd")
		}
		// Read verbs DO carry --json (agents.go:61, formula_show.go:31 register it defaulting
		// to true, documenting that JSON is currently the only supported format). That is the
		// opposite of the setters, whose --json would be a dead control.
		if f := found.Flags().Lookup("json"); f == nil {
			t.Error("`config fingerprint` must register --json like the other read verbs")
		}
	})
}

// mutateOneFixture returns the fixture set with one byte appended to a deterministically chosen
// member, standing in for "a canonical struct gained a field".
func mutateOneFixture(fixtures map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(fixtures))
	for k, v := range fixtures {
		out[k] = v
	}
	name := ""
	for k := range out {
		if name == "" || k < name {
			name = k
		}
	}
	out[name] = append(append([]byte{}, out[name]...), ' ')
	return out
}
