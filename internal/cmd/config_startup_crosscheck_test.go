package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestConfigStartupSet_CrossCheckRejectsUnknownAgent covers issue #620 Phase 1 AC 2.
//
// `af config dispatch set` has hard-failed on a dangling agent reference since it shipped;
// `af config startup set` did not check at all, so a startup.json naming an agent that does
// not exist saved cleanly and silently. That asymmetry is worst on recovery.exclude, where a
// typo does not merely name a ghost — it silently UNPROTECTS a real agent, and
// fillRecoveryDefaults then makes the mistake unrecoverable from the file alone.
//
// design-doc.md:191 sets the policy: REJECT unknown names, in all three lists.
func TestConfigStartupSet_CrossCheckRejectsUnknownAgent(t *testing.T) {
	// setupConfigFactory's roster is exactly {debugger, manager}; "ghost" is unknown in all
	// three positions.
	for _, tc := range []struct {
		field string
		body  string
	}{
		{"agents", `{"agents":["manager","ghost"],"quality":"on"}`},
		{"watchdog_agents", `{"agents":["manager"],"watchdog_agents":["ghost"]}`},
		{"recovery.exclude", `{"agents":["manager"],"recovery":{"exclude":["ghost"]}}`},
	} {
		t.Run(tc.field+" rejects an unknown name", func(t *testing.T) {
			root := setupConfigFactory(t)

			// Seed a good file so "left untouched" is provable rather than vacuous.
			if _, err := runConfigSet(t, runConfigStartupSet, `{"agents":["manager"]}`); err != nil {
				t.Fatalf("seeding a valid config: %v", err)
			}
			before, err := os.ReadFile(config.StartupConfigPath(root))
			if err != nil {
				t.Fatalf("read seeded config: %v", err)
			}

			out, err := runConfigSet(t, runConfigStartupSet, tc.body)
			if err == nil {
				t.Fatalf("a %s naming an agent absent from agents.json must be rejected", tc.field)
			}
			// Name both the offending value and the key it came from: with three lists in one
			// document, the value alone does not tell an operator which line to fix.
			for _, want := range []string{"ghost", tc.field} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("rejection must name %q; got %q", want, err.Error())
				}
			}
			if strings.Contains(out, "saved") {
				t.Errorf("a rejected write must not print a success message; got %q", out)
			}
			after, _ := os.ReadFile(config.StartupConfigPath(root))
			if !bytes.Equal(before, after) {
				t.Errorf("startup.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
			}
		})

		t.Run(tc.field+" creates nothing on a fresh factory", func(t *testing.T) {
			// The seeded case alone would still pass if the check ran AFTER a write that
			// happened to reproduce the same bytes.
			fresh := setupConfigFactory(t)
			if _, err := runConfigSet(t, runConfigStartupSet, tc.body); err == nil {
				t.Fatalf("a %s naming an unknown agent must be rejected on a fresh factory too", tc.field)
			}
			if _, err := os.Stat(config.StartupConfigPath(fresh)); !os.IsNotExist(err) {
				t.Errorf("a rejected write created startup.json on a fresh factory (stat err = %v)", err)
			}
		})
	}

	t.Run("a document naming only real agents still saves", func(t *testing.T) {
		setupConfigFactory(t)
		body := `{"agents":["manager","debugger"],"watchdog_agents":["debugger"],"recovery":{"exclude":["manager"]}}`
		if _, err := runConfigSet(t, runConfigStartupSet, body); err != nil {
			t.Fatalf("a document naming only real agents must save; got %v", err)
		}
	})

	t.Run("the nil ALL sentinel and an explicit empty list both check nothing", func(t *testing.T) {
		// agents nil means "ALL" — there are no names to check, and ranging over a nil slice
		// is already a no-op, so the sentinel needs no special case. An explicit [] means
		// "none" and likewise names nobody. Both must survive the new check.
		setupConfigFactory(t)
		for _, body := range []string{
			`{"quality":"on"}`,
			`{"agents":[],"watchdog_agents":[],"recovery":{"exclude":[]}}`,
		} {
			if _, err := runConfigSet(t, runConfigStartupSet, body); err != nil {
				t.Errorf("%s must remain valid; got %v", body, err)
			}
		}
	})

	t.Run("an unreadable agents.json is fatal, matching dispatch", func(t *testing.T) {
		root := setupConfigFactory(t)
		if err := os.Remove(config.AgentsConfigPath(root)); err != nil {
			t.Fatalf("remove agents.json: %v", err)
		}
		_, err := runConfigSet(t, runConfigStartupSet, `{"agents":["manager"]}`)
		if err == nil {
			t.Fatal("an unreadable agents.json must be fatal, as it is for dispatch")
		}
		if !strings.Contains(err.Error(), "agents.json") {
			t.Errorf("the error must name agents.json; got %q", err.Error())
		}
		if _, err := os.Stat(config.StartupConfigPath(root)); !os.IsNotExist(err) {
			t.Error("startup.json must not be written when the cross-check cannot run")
		}
	})

	t.Run("every unknown name is reported, not just the first", func(t *testing.T) {
		// An operator fixing a typo'd roster one round-trip at a time is the failure this
		// avoids: report the whole set so one edit can repair the document.
		setupConfigFactory(t)
		_, err := runConfigSet(t, runConfigStartupSet, `{"agents":["ghost"],"watchdog_agents":["phantom"]}`)
		if err == nil {
			t.Fatal("expected rejection")
		}
		for _, want := range []string{"ghost", "phantom"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("rejection must name every unknown agent, missing %q; got %q", want, err.Error())
			}
		}
	})
}

// TestSaveStartupConfig_StaysPureOfAgentsJson pins the ADR-004 layering the cross-check must
// NOT violate. internal/config/startup.go:183-186 states the rule in a code comment; this is
// the mechanical half. If an implementer ever moves the membership check into
// validateStartupConfig, this fails immediately — as would internal/config/save_test.go, which
// saves a StartupConfig naming "manager" with no agents.json anywhere near its temp dir.
func TestSaveStartupConfig_StaysPureOfAgentsJson(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.StartupConfig{Agents: []string{"manager"}, WatchdogAgents: []string{"ghost"}}
	if err := config.SaveStartupConfig(dir+"/startup.json", cfg); err != nil {
		t.Fatalf("SaveStartupConfig must not cross-check agents.json (ADR-004); got %v", err)
	}
}
