package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// runConfigSetWithHash drives a setter through a command that HAS the --if-content-hash flag
// registered, which the shared runConfigSet deliberately does not: its bare &cobra.Command{}
// is what proves the handlers stay usable when the flag is absent entirely. Passing an empty
// hash here registers the flag but leaves it unset, which is the third distinct state.
func runConfigSetWithHash(t *testing.T, fn func(*cobra.Command, []string) error, stdin, hash string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.Flags().String(ifContentHashFlag, "", "content precondition")
	if hash != "" {
		if err := cmd.Flags().Set(ifContentHashFlag, hash); err != nil {
			t.Fatalf("set --%s: %v", ifContentHashFlag, err)
		}
	}
	cmd.SetIn(strings.NewReader(stdin))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := fn(cmd, nil)
	return buf.String(), err
}

func sha256Hex(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestConfigSet_IfContentHashMismatch covers issue #620 Phase 1 AC 4.
//
// Nothing in the setter path compared the on-disk content against what the caller last read, so
// a console that GETs a document, lets an operator edit it, and PUTs it back silently clobbered
// any concurrent CLI edit. --if-content-hash makes the write CONDITIONAL on the caller's read
// still being current.
//
// Honest scope, per fsutil/atomic.go:14-15 ("Last-writer-wins semantics still apply"): this is a
// read-compare-write across processes with no lock. It narrows the lost-update window to the
// microseconds between the compare and the rename; it does not close it. That is a real
// improvement over an unconditional write and is not a mutual-exclusion primitive.
func TestConfigSet_IfContentHashMismatch(t *testing.T) {
	// The four CAS-bearing setters (models is deliberately excluded — AC 4 lists four).
	for _, tc := range []struct {
		setter string
		fn     func(*cobra.Command, []string) error
		path   func(string) string
		first  string
		second string
	}{
		{
			"dispatch", runConfigDispatchSet, config.DispatchConfigPath,
			`{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"debugger"}]}`,
			`{"repos":["o/r2"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"manager"}]}`,
		},
		{
			"startup", runConfigStartupSet, config.StartupConfigPath,
			`{"agents":["manager"]}`,
			`{"agents":["debugger"]}`,
		},
		{
			"messaging", runConfigMessagingSet, config.MessagingConfigPath,
			`{"groups":{"all":["manager"]}}`,
			`{"groups":{"all":["debugger"]}}`,
		},
		{
			"statusline", runConfigStatuslineSet, config.StatuslineConfigPath,
			`{"elements":["model","dir"]}`,
			`{"elements":["branch"]}`,
		},
	} {
		t.Run(tc.setter+" refuses a write whose precondition is stale", func(t *testing.T) {
			root := setupConfigFactory(t)

			if _, err := runConfigSet(t, tc.fn, tc.first); err != nil {
				t.Fatalf("seeding: %v", err)
			}
			stale := sha256Hex(t, tc.path(root))

			// A concurrent CLI edit lands between the caller's read and its write.
			if _, err := runConfigSet(t, tc.fn, tc.second); err != nil {
				t.Fatalf("concurrent edit: %v", err)
			}
			before, err := os.ReadFile(tc.path(root))
			if err != nil {
				t.Fatalf("read after concurrent edit: %v", err)
			}
			if current := sha256Hex(t, tc.path(root)); current == stale {
				t.Fatal("the concurrent edit did not change the file, so this case would prove nothing")
			}

			out, err := runConfigSetWithHash(t, tc.fn, tc.first, stale)
			if err == nil {
				t.Fatalf("%s must refuse a write whose --%s no longer matches the file", tc.setter, ifContentHashFlag)
			}
			// Name both digests: a caller that cannot see what it expected versus what is there
			// cannot tell a stale read from a mistyped flag.
			if !strings.Contains(err.Error(), stale) {
				t.Errorf("the rejection must quote the expected digest; got %q", err.Error())
			}
			if strings.Contains(out, "saved") {
				t.Errorf("a refused write must not print a success message; got %q", out)
			}
			after, _ := os.ReadFile(tc.path(root))
			if !bytes.Equal(before, after) {
				t.Errorf("%s.json was modified despite a stale precondition:\nbefore=%s\nafter=%s", tc.setter, before, after)
			}
		})

		t.Run(tc.setter+" writes when the precondition matches", func(t *testing.T) {
			root := setupConfigFactory(t)
			if _, err := runConfigSet(t, tc.fn, tc.first); err != nil {
				t.Fatalf("seeding: %v", err)
			}
			current := sha256Hex(t, tc.path(root))

			if _, err := runConfigSetWithHash(t, tc.fn, tc.second, current); err != nil {
				t.Fatalf("a matching --%s must permit the write; got %v", ifContentHashFlag, err)
			}
			if now := sha256Hex(t, tc.path(root)); now == current {
				t.Error("the write was accepted but the file did not change")
			}
		})

		t.Run(tc.setter+" with the flag registered but unset behaves as today", func(t *testing.T) {
			root := setupConfigFactory(t)
			if _, err := runConfigSet(t, tc.fn, tc.first); err != nil {
				t.Fatalf("seeding: %v", err)
			}
			// An empty flag value is "no precondition", not "the empty document's digest" —
			// otherwise every caller that never opted in would start failing.
			if _, err := runConfigSetWithHash(t, tc.fn, tc.second, ""); err != nil {
				t.Errorf("an unset --%s must preserve today's unconditional behavior; got %v", ifContentHashFlag, err)
			}
			if _, err := os.Stat(tc.path(root)); err != nil {
				t.Errorf("the write should have happened: %v", err)
			}
		})

		t.Run(tc.setter+" registers the flag and reads it", func(t *testing.T) {
			// TestConfigSet_NoJSONFlag's rule: a registered flag that no handler consults
			// misrepresents the CLI contract. Registration is asserted here; that it is READ is
			// asserted by the mismatch case above, which can only pass if the handler consults it.
			var setCmd *cobra.Command
			switch tc.setter {
			case "dispatch":
				setCmd = configDispatchSetCmd
			case "startup":
				setCmd = configStartupSetCmd
			case "messaging":
				setCmd = configMessagingSetCmd
			case "statusline":
				setCmd = configStatuslineSetCmd
			}
			if f := setCmd.Flags().Lookup(ifContentHashFlag); f == nil {
				t.Errorf("`config %s set` must register --%s", tc.setter, ifContentHashFlag)
			}
		})
	}

	t.Run("a precondition against a file that does not exist is refused", func(t *testing.T) {
		// The caller claims to have read content that is not there. Silently treating that as a
		// create would grant exactly the unconditional write the flag exists to prevent, so it
		// is a refusal — and the message has to name the remedy, because "create this file" is a
		// legitimate intent that is simply spelled differently (omit the flag).
		setupConfigFactory(t)
		out, err := runConfigSetWithHash(t, runConfigStatuslineSet, `{"elements":["model"]}`,
			"0000000000000000000000000000000000000000000000000000000000000000")
		if err == nil {
			t.Fatal("a precondition against an absent file must be refused")
		}
		if strings.Contains(out, "saved") {
			t.Errorf("a refused write must not print a success message; got %q", out)
		}
	})

	t.Run("handlers stay usable when the flag is not registered at all", func(t *testing.T) {
		// The shared runConfigSet harness builds a bare &cobra.Command{}. Reading the flag with
		// Flags().GetString on such a command yields "flag accessed but not defined" rather than
		// an empty string, which would redden every pre-existing setter test at once. The read
		// must be nil-safe. This case is the mechanical guard for that.
		setupConfigFactory(t)
		if _, err := runConfigSet(t, runConfigStatuslineSet, `{"elements":["model"]}`); err != nil {
			t.Errorf("a handler must work on a command with no --%s registered; got %v", ifContentHashFlag, err)
		}
	})

	t.Run("models deliberately has no content precondition", func(t *testing.T) {
		// AC 4 scopes the flag to four setters. models set is excluded, and an unregistered flag
		// is the honest signal — better than registering one the handler ignores.
		if f := configModelsSetCmd.Flags().Lookup(ifContentHashFlag); f != nil {
			t.Errorf("`config models set` is out of scope for --%s; registering it unread would be a dead control", ifContentHashFlag)
		}
	})
}
