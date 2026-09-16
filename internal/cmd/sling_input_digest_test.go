package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

func setSlingInputDigestForTest(t *testing.T, digest string) {
	t.Helper()
	orig := slingInputDigest
	slingInputDigest = digest
	t.Cleanup(func() { slingInputDigest = orig })
}

func slingOnce(t *testing.T, fx lifecycleFixture) error {
	t.Helper()
	setSlingFlagsForTest(t, "offpath", fx.agent)
	var err error
	captureStderr(t, func() {
		var out bytes.Buffer
		c := &cobra.Command{}
		c.SetContext(t.Context())
		c.SetOut(&out)
		c.SetErr(&out)
		err = runSling(c, nil)
	})
	return err
}

func recordsFor(t *testing.T, fx lifecycleFixture) []telemetry.StepEvent {
	t.Helper()
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	return records
}

// TestSlingRefusesAnAttestationItCannotTrust pins --input-digest's contract (#678 K1).
//
// The value is an attestation: it says this run was based on inputs whose content hashes to exactly
// that. A malformed one accepted and recorded would be an attestation to nothing, and the records
// carrying it would be indistinguishable from the records carrying a real one — so the refusal has
// to happen while it is still the caller's problem, before any bead or record exists to carry it.
func TestSlingRefusesAnAttestationItCannotTrust(t *testing.T) {
	const goodDigest = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

	t.Run("the shapes it accepts and the shapes it refuses", func(t *testing.T) {
		cases := []struct {
			name    string
			digest  string
			wantErr string
		}{
			{name: "absent, because the flag is optional", digest: ""},
			{name: "a sha-256 digest", digest: goodDigest},
			{name: "uppercase hex is reported on its case", digest: strings.ToUpper(goodDigest),
				wantErr: "got uppercase hexadecimal"},
			{name: "one character short", digest: goodDigest[:63], wantErr: "got 63 characters"},
			{name: "one character long", digest: goodDigest + "0", wantErr: "got 65 characters"},
			{name: "the right length but not hex", digest: strings.Repeat("z", 64), wantErr: "got 64 characters"},
			{name: "a filename someone meant to hash", digest: "inputs.json", wantErr: "got 11 characters"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				err := validateInputDigest(tc.digest)
				if tc.wantErr == "" {
					if err != nil {
						t.Fatalf("validateInputDigest(%q) = %v, want accepted", tc.digest, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("validateInputDigest(%q) was accepted, want refused", tc.digest)
				}
				if !strings.Contains(err.Error(), "64 lowercase hexadecimal characters") {
					t.Errorf("error %q does not name the rule the caller has to satisfy", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q, so it does not say which property failed",
						err, tc.wantErr)
				}
			})
		}
	})

	// The wiring, which the table above cannot reach: a validator nothing calls refuses nothing.
	t.Run("a malformed digest stops the run before anything is created", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		setSlingInputDigestForTest(t, "inputs.json")

		err := slingOnce(t, fx)
		if err == nil {
			t.Fatal("af sling accepted a digest that is not one; the validator is not wired into runSling")
		}
		if !strings.Contains(err.Error(), "--input-digest") {
			t.Errorf("af sling failed with %q, which does not name the flag at fault", err)
		}

		issues, listErr := fx.mem.List(t.Context(), issuestore.Filter{IncludeAllAgents: true, IncludeClosed: true})
		if listErr != nil {
			t.Fatalf("listing the store: %v", listErr)
		}
		if len(issues) != 0 {
			t.Errorf("the refused sling created %d beads; the refusal must come before instantiation", len(issues))
		}
		if got := recordsFor(t, fx); len(got) != 0 {
			t.Errorf("the refused sling wrote %d telemetry records", len(got))
		}
	})

	t.Run("a well-formed digest reaches the instance_start record", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		setSlingInputDigestForTest(t, goodDigest)

		if err := slingOnce(t, fx); err != nil {
			t.Fatalf("af sling: %v", err)
		}

		var seen bool
		for _, r := range recordsFor(t, fx) {
			if r.Event != telemetry.EventInstanceStart {
				continue
			}
			seen = true
			if r.SlingDigest != goodDigest {
				t.Errorf("sling_digest = %q, want %q — the flag is threaded through "+
					"InstantiateParams, not read from the global at the recording site", r.SlingDigest, goodDigest)
			}
		}
		if !seen {
			t.Fatal("no instance_start record was written; the fixture is not driving a real sling")
		}
	})

	// Absent and empty are the same fact here, and the field is omitempty, so a run slung without
	// the flag must leave nothing behind rather than an empty string that shifts every record's bytes.
	t.Run("a run slung without the flag carries no digest", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		setSlingInputDigestForTest(t, "")

		if err := slingOnce(t, fx); err != nil {
			t.Fatalf("af sling: %v", err)
		}
		for _, r := range recordsFor(t, fx) {
			if r.Event == telemetry.EventInstanceStart && r.SlingDigest != "" {
				t.Errorf("sling_digest = %q from a run that was slung without one", r.SlingDigest)
			}
		}
	})
}
