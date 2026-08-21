package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// TestConfigSet_RejectsUnknownField covers issue #620 Phase 1 AC 3 — the write-strict half of
// the write-strict/read-tolerant asymmetry.
//
// Every setter decoded stdin with a plain json.Decoder, so an unknown top-level key was
// silently dropped — and because the setter then writes the DECODED struct back, the key was
// not merely ignored, it was ERASED FROM DISK. Tolerance is right on the load path (config
// evolves additively) and wrong on the write path, where it converts a typo into data loss.
// The statusline nil-"elements" reject (config_set.go:193-202) already made that argument for
// one key; DisallowUnknownFields generalizes it to the whole document shape.
func TestConfigSet_RejectsUnknownField(t *testing.T) {
	// One case per setter, so a decoder made strict at four of five call sites fails here.
	// Each `body` is otherwise VALID — only the extra key makes it wrong — so a rejection can
	// only be attributed to strictness.
	for _, tc := range []struct {
		setter  string
		fn      func(*cobra.Command, []string) error
		path    func(string) string
		valid   string
		unknown string
		badKey  string
	}{
		{
			"dispatch", runConfigDispatchSet, config.DispatchConfigPath,
			`{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"debugger"}]}`,
			`{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"debugger"}],"trigger_labels":"agentic"}`,
			"trigger_labels",
		},
		{
			"startup", runConfigStartupSet, config.StartupConfigPath,
			`{"agents":["manager"]}`,
			`{"agents":["manager"],"watchdog_agent":["manager"]}`,
			"watchdog_agent",
		},
		{
			"messaging", runConfigMessagingSet, config.MessagingConfigPath,
			`{"groups":{"all":["manager"]}}`,
			`{"groups":{"all":["manager"]},"group":{"x":["manager"]}}`,
			"group",
		},
		{
			"statusline", runConfigStatuslineSet, config.StatuslineConfigPath,
			`{"elements":["model","dir"]}`,
			`{"elements":["model","dir"],"colour":true}`,
			"colour",
		},
		{
			"models", runConfigModelsSet, config.ModelsConfigPath,
			`{"models":{"p":{"ANTHROPIC_MODEL":"m"}}}`,
			`{"models":{"p":{"ANTHROPIC_MODEL":"m"}},"model":"p"}`,
			"model",
		},
	} {
		t.Run(tc.setter+" rejects an unknown top-level key", func(t *testing.T) {
			root := setupConfigFactory(t)

			// Seed with the VALID form of the same document, so "left untouched" is provable
			// and so we know the body is rejected for its extra key and nothing else.
			if _, err := runConfigSet(t, tc.fn, tc.valid); err != nil {
				t.Fatalf("the valid form of this document must save (else the case proves nothing): %v", err)
			}
			before, err := os.ReadFile(tc.path(root))
			if err != nil {
				t.Fatalf("read seeded config: %v", err)
			}

			out, err := runConfigSet(t, tc.fn, tc.unknown)
			if err == nil {
				t.Fatalf("%s must reject an unknown top-level key, never silently erase it", tc.setter)
			}
			if !strings.Contains(err.Error(), tc.badKey) {
				t.Errorf("the rejection must NAME the offending key %q so the typo is fixable; got %q", tc.badKey, err.Error())
			}
			if strings.Contains(out, "saved") {
				t.Errorf("a rejected write must not print a success message; got %q", out)
			}
			after, _ := os.ReadFile(tc.path(root))
			if !bytes.Equal(before, after) {
				t.Errorf("%s.json was modified on a rejected write:\nbefore=%s\nafter=%s", tc.setter, before, after)
			}
		})

		t.Run(tc.setter+" creates nothing on a fresh factory", func(t *testing.T) {
			fresh := setupConfigFactory(t)
			if _, err := runConfigSet(t, tc.fn, tc.unknown); err == nil {
				t.Fatalf("%s must reject an unknown key on a fresh factory too", tc.setter)
			}
			if _, err := os.Stat(tc.path(fresh)); !os.IsNotExist(err) {
				t.Errorf("a rejected write created %s.json on a fresh factory (stat err = %v)", tc.setter, err)
			}
		})
	}

	t.Run("arbitrary keys INSIDE a map-typed field stay legal", func(t *testing.T) {
		// DisallowUnknownFields is a STRUCT-FIELD rule. A models profile is "a plain map of env
		// exports" and a messaging group name is operator-chosen, so policing those keys would
		// break the very extensibility those schemas were designed for.
		setupConfigFactory(t)
		if _, err := runConfigSet(t, runConfigModelsSet, `{"models":{"anything":{"ANY_VENDOR_KEY":"v","ANTHROPIC_MODEL":"m"}}}`); err != nil {
			t.Errorf("keys inside the profile map must stay unconstrained; got %v", err)
		}
		if _, err := runConfigSet(t, runConfigMessagingSet, `{"groups":{"any-group-name":["manager"]}}`); err != nil {
			t.Errorf("operator-chosen group names must stay unconstrained; got %v", err)
		}
	})

	t.Run("the load path stays tolerant", func(t *testing.T) {
		// The asymmetry IS the design. A file already on disk carrying a key this binary does
		// not know must keep loading — that is what makes config evolution additive. Reviewers
		// should not "fix" this to match `set`.
		root := setupConfigFactory(t)
		if err := os.WriteFile(config.StatuslineConfigPath(root),
			[]byte(`{"elements":["model"],"future_key_from_a_newer_af":42}`), 0o644); err != nil {
			t.Fatalf("seed statusline.json: %v", err)
		}
		if _, err := config.LoadStatuslineConfig(root); err != nil {
			t.Errorf("LOAD must tolerate an unknown key (additive evolution); got %v", err)
		}
	})

	t.Run("Phase 0's quoting hint survives the strict decoder", func(t *testing.T) {
		// runConfigModelsSet unwraps *json.UnmarshalTypeError to name the quoting rule. That
		// branch only keeps working while decodeJSONStdin wraps with %w — making the decoder
		// strict must not restructure the error chain.
		setupConfigFactory(t)
		_, err := runConfigSet(t, runConfigModelsSet, `{"models":{"p":{"ANTHROPIC_MAX_TOKENS":220000}}}`)
		if err == nil {
			t.Fatal("an unquoted numeric profile value must be rejected")
		}
		if !strings.Contains(err.Error(), "quoted JSON string") {
			t.Errorf("the models quoting hint must survive strict decode; got %q", err.Error())
		}
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			t.Errorf("decodeJSONStdin must keep wrapping with %%w so errors.As still sees the typed error; got %q", err.Error())
		}
	})
}

// TestConfigStatuslineSet_UnknownKeyStillEchoesSchema is the H-R1 contract under strictness.
//
// TestConfigStatuslineSet_WrongKeyRejectedNothingWritten requires a wrong-key rejection to name
// the real key and carry a usable example. Before strict decode that text came from the
// nil-Elements guard; with strict decode the document now fails at DECODE, one step earlier.
// The operator-facing contract must not regress just because the rejection moved: from where
// they stand, "I misspelled the key" is the same mistake either way.
func TestConfigStatuslineSet_UnknownKeyStillEchoesSchema(t *testing.T) {
	setupConfigFactory(t)

	_, err := runConfigSet(t, runConfigStatuslineSet, `{"statusline":["model","branch"]}`)
	if err == nil {
		t.Fatal("a wrong-key document must be rejected")
	}
	for _, want := range []string{"elements", `{"elements":`, "statusline"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the decode-time rejection must still echo the schema and name the offending key; missing %q in %q", want, err.Error())
		}
	}

	// The nil-Elements guard is NOT subsumed by strictness: `{}` carries no unknown field, so
	// it decodes clean to a nil Elements and only the guard can catch it. Deleting the guard
	// once strictness lands would silently re-open the blanked-statusline hole.
	if _, err := runConfigSet(t, runConfigStatuslineSet, `{}`); err == nil {
		t.Fatal("an empty document must still be rejected by the nil-elements guard")
	}
}
