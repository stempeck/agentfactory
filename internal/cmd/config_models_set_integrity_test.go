package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// These three tests pin the write-path integrity guards on `af config models set`. Each one
// covers a save that used to exit 0 while leaving the factory in a state af itself would later
// refuse: a dispatch pin whose profile no longer exists, a display redaction persisted as a
// literal token, and a fitness attestation vouching for bytes it never saw.

const (
	pinnedRegistry  = `{"models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8"},"gpu":{"ANTHROPIC_MODEL":"local-gpu"}}}`
	droppedRegistry = `{"models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8"}}}`
)

// seedPinningDispatch writes a dispatch.json whose only mapping pins the model profile "gpu".
// The label is deliberately not a substring of the agent name, so an assertion that the rejection
// names the label cannot be satisfied by the agent name alone.
func seedPinningDispatch(t *testing.T, root string) {
	t.Helper()
	disp := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["needs-triage"],"agent":"debugger","model":"gpu"}],"notify_on_complete":"manager"}`
	if err := os.WriteFile(config.DispatchConfigPath(root), []byte(disp), 0o644); err != nil {
		t.Fatalf("seed dispatch.json: %v", err)
	}
}

// seedRegistry installs a starting models.json through the command itself, so the fixture is
// exactly what a previous successful save would have left on disk.
func seedRegistry(t *testing.T, body string) {
	t.Helper()
	if _, err := runConfigSet(t, runConfigModelsSet, body); err != nil {
		t.Fatalf("seeding the registry: %v", err)
	}
}

// A dispatch mapping may pin a model profile, and ValidateDispatchConfig rejects a dispatch
// document that pins an undefined one — but that check only ever ran when dispatch.json was the
// document being written. A models set that removed the pinned profile exited 0, leaving a set
// where the byte-identical dispatch.json no longer validated.
func TestConfigModelsSet_ReversePinReject(t *testing.T) {
	t.Run("removing a pinned profile is rejected and models.json is untouched", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, pinnedRegistry)
		seedPinningDispatch(t, root)
		before, err := os.ReadFile(config.ModelsConfigPath(root))
		if err != nil {
			t.Fatalf("read seeded models.json: %v", err)
		}

		stdout, _, err := runConfigModelsSetSplit(t, droppedRegistry)
		if err == nil {
			t.Fatal("a models set that orphans a dispatch-pinned profile must be rejected")
		}
		for _, want := range []string{"gpu", "debugger", "needs-triage"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the rejection must name the profile and the pinning mapping (missing %q); got %v", want, err)
			}
		}
		if strings.Contains(stdout, "saved") {
			t.Errorf("a rejected write must not report a save; got %q", stdout)
		}
		after, _ := os.ReadFile(config.ModelsConfigPath(root))
		if !bytes.Equal(before, after) {
			t.Errorf("models.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
		}
		// The oracle from the analyst's reproduction: the untouched dispatch.json must still
		// validate. Before the guard, this is exactly what stopped being true.
		if _, err := config.LoadDispatchConfig(root); err != nil {
			t.Fatalf("dispatch.json must still validate after the rejection: %v", err)
		}
	})

	t.Run("renaming a pinned profile is rejected too", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, pinnedRegistry)
		seedPinningDispatch(t, root)

		before, err := os.ReadFile(config.ModelsConfigPath(root))
		if err != nil {
			t.Fatalf("read seeded models.json: %v", err)
		}

		renamed := `{"models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8"},"gpu2":{"ANTHROPIC_MODEL":"local-gpu"}}}`
		if _, _, err := runConfigModelsSetSplit(t, renamed); err == nil {
			t.Fatal("renaming a pinned profile orphans the pin just as removing it does")
		} else if !strings.Contains(err.Error(), "gpu") {
			t.Errorf("the rejection must name the orphaned profile; got %v", err)
		}
		after, _ := os.ReadFile(config.ModelsConfigPath(root))
		if !bytes.Equal(before, after) {
			t.Errorf("models.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
		}
	})

	t.Run("a pin that still resolves is not rejected", func(t *testing.T) {
		setupConfigFactory(t)
		seedRegistry(t, pinnedRegistry)
		seedPinningDispatch(t, mustRoot(t))

		edited := `{"models":{"opus":{"ANTHROPIC_MODEL":"claude-opus-4-8"},"gpu":{"ANTHROPIC_MODEL":"local-gpu-v2"}}}`
		stdout, stderr, err := runConfigModelsSetSplit(t, edited)
		if err != nil {
			t.Fatalf("editing a pinned profile in place is legal; got %v (stderr=%q)", err, stderr)
		}
		if !strings.Contains(stdout, "Models configuration saved.") {
			t.Errorf("stdout %q should confirm the save", stdout)
		}
	})

	t.Run("with dispatch.json absent the same set succeeds silently", func(t *testing.T) {
		root := setupConfigFactory(t) // setupConfigFactory writes no dispatch.json
		seedRegistry(t, pinnedRegistry)

		stdout, stderr, err := runConfigModelsSetSplit(t, droppedRegistry)
		if err != nil {
			t.Fatalf("an absent dispatch.json is nothing to check, not a rejection: %v (stderr=%q)", err, stderr)
		}
		if stderr != "" {
			t.Errorf("the absent-file skip must emit nothing at all; got %q", stderr)
		}
		if !strings.Contains(stdout, "Models configuration saved.") {
			t.Errorf("stdout %q should confirm the save", stdout)
		}
		loaded, err := config.LoadModelsConfig(root)
		if err != nil {
			t.Fatalf("reload models.json: %v", err)
		}
		if _, still := loaded.Models["gpu"]; still {
			t.Error("the profile should actually have been removed on the absent-dispatch path")
		}
	})

	t.Run("an unparseable dispatch.json warns and does not block", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, pinnedRegistry)
		if err := os.WriteFile(config.DispatchConfigPath(root), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("seed unparseable dispatch.json: %v", err)
		}

		stdout, stderr, err := runConfigModelsSetSplit(t, droppedRegistry)
		if err != nil {
			t.Fatalf("a half-edited dispatch.json warns and skips, it does not block the write: %v", err)
		}
		if !strings.Contains(stderr, "dispatch.json") {
			t.Errorf("the fall-through must name the file it is ignoring; got %q", stderr)
		}
		if strings.Contains(stdout, "warning") {
			t.Errorf("the warning must not reach stdout; got %q", stdout)
		}
		// Falling through means the write happens, not merely that it is not refused.
		loaded, err := config.LoadModelsConfig(root)
		if err != nil {
			t.Fatalf("reload models.json: %v", err)
		}
		if _, still := loaded.Models["gpu"]; still {
			t.Error("the registry must actually have been written on the warn-and-skip path")
		}
	})

	t.Run("a dispatch.json that fails its own validation also warns and skips", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, pinnedRegistry)
		// Parses as JSON, but validateDispatchConfig rejects it for the missing trigger_label —
		// an error indistinguishable in kind from a parse failure, and the same posture applies.
		incomplete := `{"repos":["o/r"],"mappings":[{"labels":["bug"],"agent":"debugger","model":"gpu"}]}`
		if err := os.WriteFile(config.DispatchConfigPath(root), []byte(incomplete), 0o644); err != nil {
			t.Fatalf("seed incomplete dispatch.json: %v", err)
		}

		_, stderr, err := runConfigModelsSetSplit(t, droppedRegistry)
		if err != nil {
			t.Fatalf("an incomplete dispatch.json must warn and skip, not block: %v", err)
		}
		if !strings.Contains(stderr, "dispatch.json") {
			t.Errorf("the fall-through must name the file it is ignoring; got %q", stderr)
		}
	})

	t.Run("an emptied registry returns pins to raw-id semantics", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, pinnedRegistry)
		seedPinningDispatch(t, root)

		// With no registry at all a mapping's model is a raw id passed straight to the launch,
		// which is what ValidateDispatchConfig itself accepts (it gates the pin check on a
		// non-empty registry). Rejecting here would make the two directions disagree.
		_, stderr, err := runConfigModelsSetSplit(t, `{"models":{}}`)
		if err != nil {
			t.Fatalf("emptying the registry is not orphaning: %v (stderr=%q)", err, stderr)
		}
		if stderr != "" {
			t.Errorf("emptying the registry must not warn; got %q", stderr)
		}
	})

	t.Run("a mapping with no model pin is never implicated", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, pinnedRegistry)
		unpinned := `{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"debugger"}],"notify_on_complete":"manager"}`
		if err := os.WriteFile(config.DispatchConfigPath(root), []byte(unpinned), 0o644); err != nil {
			t.Fatalf("seed dispatch.json: %v", err)
		}

		if _, stderr, err := runConfigModelsSetSplit(t, droppedRegistry); err != nil {
			t.Fatalf("a mapping that pins nothing cannot be orphaned: %v (stderr=%q)", err, stderr)
		}
	})
}

// displayModelValue renders a literal ANTHROPIC_AUTH_TOKEN as **** so `af config models show`
// never prints a secret. Nothing on the write side rejected that placeholder, so an operator
// copying a shown profile back into `set` — or a console rendering it into an editable field —
// persisted **** as the literal token.
func TestConfigModelsSet_RejectsRedactionSentinel(t *testing.T) {
	// No ANTHROPIC_BASE_URL, so neither advisory lint has anything to say and the stderr
	// assertion below is exact.
	const poisoned = `{"models":{"gw":{"ANTHROPIC_MODEL":"gpt-5.3-codex","ANTHROPIC_AUTH_TOKEN":"****"}}}`

	t.Run("on a fresh factory the reject creates nothing", func(t *testing.T) {
		root := setupConfigFactory(t)

		stdout, stderr, err := runConfigModelsSetSplit(t, poisoned)
		if err == nil {
			t.Fatal("the **** display redaction must never be persisted as a literal token")
		}
		for _, want := range []string{"gw", authTokenKey} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the rejection must name the profile and the key (missing %q); got %v", want, err)
			}
		}
		// An operator who pasted a shown profile back needs to be told what **** actually is,
		// not merely that it is refused.
		if !strings.Contains(err.Error(), "af config models show") {
			t.Errorf("the rejection must explain that the value is a display redaction; got %v", err)
		}
		if strings.Contains(stdout, "saved") {
			t.Errorf("a rejected write must not report a save; got %q", stdout)
		}
		if strings.Contains(stderr, "warning:") {
			t.Errorf("a registry rejected before the write must not be linted; got %q", stderr)
		}
		if _, statErr := os.Stat(config.ModelsConfigPath(root)); !os.IsNotExist(statErr) {
			t.Errorf("a rejected write must not create models.json (stat err=%v)", statErr)
		}
	})

	t.Run("an existing registry is left byte-identical", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, `{"models":{"gw":{"ANTHROPIC_MODEL":"gpt-5.3-codex","ANTHROPIC_AUTH_TOKEN":"file:secrets/gw.key"}}}`)
		before, _ := os.ReadFile(config.ModelsConfigPath(root))

		if _, _, err := runConfigModelsSetSplit(t, poisoned); err == nil {
			t.Fatal("expected the redaction placeholder to be rejected")
		}
		after, _ := os.ReadFile(config.ModelsConfigPath(root))
		if !bytes.Equal(before, after) {
			t.Errorf("models.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
		}
	})

	t.Run("real token values still save", func(t *testing.T) {
		setupConfigFactory(t)
		for _, body := range []string{
			`{"models":{"gw":{"ANTHROPIC_MODEL":"gpt-5.3-codex","ANTHROPIC_AUTH_TOKEN":"file:secrets/gw.key"}}}`,
			`{"models":{"gw":{"ANTHROPIC_BASE_URL":"http://localhost:1234","ANTHROPIC_AUTH_TOKEN":"lm-studio","ANTHROPIC_MODEL":"local","ANTHROPIC_API_KEY":""}}}`,
		} {
			if _, err := runConfigSet(t, runConfigModelsSet, body); err != nil {
				t.Errorf("a legitimate token must still save; got %v for %s", err, body)
			}
		}
	})

	t.Run("the placeholder is only a placeholder under the token key", func(t *testing.T) {
		setupConfigFactory(t)
		if _, err := runConfigSet(t, runConfigModelsSet, `{"models":{"odd":{"ANTHROPIC_MODEL":"****"}}}`); err != nil {
			t.Errorf("the guard is scoped to %s and must not police other values; got %v", authTokenKey, err)
		}
	})

	t.Run("a token that merely contains the placeholder is not the placeholder", func(t *testing.T) {
		setupConfigFactory(t)
		body := `{"models":{"gw":{"ANTHROPIC_MODEL":"gpt-5.3-codex","ANTHROPIC_AUTH_TOKEN":"tok-****-real"}}}`
		if _, err := runConfigSet(t, runConfigModelsSet, body); err != nil {
			t.Errorf("only the exact redaction placeholder is rejected; got %v", err)
		}
	})
}

// A fitness attestation records that a profile's transport was verified fit, and the selecting
// launch interlock reads it by NAME. The registry mutates under a stable name, so rewriting a
// profile's endpoint left the old attestation vouching for bytes nobody ever verified.
func TestConfigModelsSet_InvalidatesStaleAttestation(t *testing.T) {
	const attested = `{"models":{` +
		`"codex":{"ANTHROPIC_MODEL":"gpt-5.3-codex","ANTHROPIC_BASE_URL":"https://gw-a.example:4000","ANTHROPIC_AUTH_TOKEN":"file:secrets/codex.key"},` +
		`"other":{"ANTHROPIC_MODEL":"claude-opus-4-8"}}}`
	// codex repointed at a different gateway; other is byte-identical.
	const repointed = `{"models":{` +
		`"codex":{"ANTHROPIC_MODEL":"gpt-5.3-codex","ANTHROPIC_BASE_URL":"https://gw-b.example:9999","ANTHROPIC_AUTH_TOKEN":"file:secrets/codex.key"},` +
		`"other":{"ANTHROPIC_MODEL":"claude-opus-4-8"}}}`

	t.Run("a profile whose content changed loses its attestation", func(t *testing.T) {
		root := setupConfigFactory(t)
		// resolveLaunchModelEnv below dedupes its coverage warning through package state; the
		// package convention is to clear it so no later case inherits this one's suppression.
		resetModelCoverageWarnings()
		t.Cleanup(resetModelCoverageWarnings)
		seedRegistry(t, attested)
		writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
		writeAttestationFixture(t, root, "codex")
		writeAttestationFixture(t, root, "other")
		if !hasFitnessAttestation(root, "codex") || !hasFitnessAttestation(root, "other") {
			t.Fatal("fixture precondition: both profiles must start attested")
		}
		// Positive control: while the attestation still matches the profile it was earned on,
		// a selecting non-loopback launch is allowed. Without this the refusal below could pass
		// for an unrelated reason.
		var warn bytes.Buffer
		if _, _, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), "codex", "", false, &warn); err != nil {
			t.Fatalf("precondition: an attested non-loopback profile must launch; got %v", err)
		}

		stdout, _, err := runConfigModelsSetSplit(t, repointed)
		if err != nil {
			t.Fatalf("repointing a profile is a legal edit — the save must succeed: %v", err)
		}
		if !strings.Contains(stdout, "Models configuration saved.") {
			t.Errorf("stdout %q should confirm the save", stdout)
		}

		if hasFitnessAttestation(root, "codex") {
			t.Errorf("a profile edited after it was attested is no longer the profile that was attested (%s)",
				modelFitnessPath(root, "codex"))
		}
		// An implementation that simply wiped the directory would pass the line above while
		// destroying attestations the operator still owns.
		if !hasFitnessAttestation(root, "other") {
			t.Error("an untouched profile's attestation must survive the save")
		}

		if _, _, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), "codex", "", false, &warn); err == nil {
			t.Error("a selecting launch of the rewritten non-loopback profile must now be refused")
		} else if !strings.Contains(err.Error(), "attest") {
			t.Errorf("the refusal must point at re-attesting; got %v", err)
		}
	})

	t.Run("a removed profile loses its attestation", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, attested)
		writeAttestationFixture(t, root, "codex")

		dropped := `{"models":{"other":{"ANTHROPIC_MODEL":"claude-opus-4-8"}}}`
		if _, _, err := runConfigModelsSetSplit(t, dropped); err != nil {
			t.Fatalf("dropping an unpinned profile is legal: %v", err)
		}
		if hasFitnessAttestation(root, "codex") {
			t.Error("a profile no longer in the registry must not keep an attestation that would " +
				"vouch for whatever is defined under that name next")
		}
	})

	t.Run("a rejected save leaves attestations untouched", func(t *testing.T) {
		root := setupConfigFactory(t)
		seedRegistry(t, attested)
		writeAttestationFixture(t, root, "codex")

		poisoned := `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.3-codex","ANTHROPIC_BASE_URL":"https://gw-b.example:9999","ANTHROPIC_AUTH_TOKEN":"****"}}}`
		if _, _, err := runConfigModelsSetSplit(t, poisoned); err == nil {
			t.Fatal("expected the redaction placeholder to be rejected")
		}
		if !hasFitnessAttestation(root, "codex") {
			t.Error("a write that never happened must not invalidate anything")
		}
	})

	t.Run("an unreadable registry on disk is treated as everything having changed", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeAttestationFixture(t, root, "codex")
		// A hand-mangled registry is exactly what an operator reaches for `set` to replace. With
		// no readable snapshot there is nothing to compare against, so every submitted profile
		// must be assumed changed rather than assumed intact.
		if err := os.WriteFile(config.ModelsConfigPath(root), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("seed unreadable models.json: %v", err)
		}

		if _, _, err := runConfigModelsSetSplit(t, attested); err != nil {
			t.Fatalf("replacing an unreadable registry is the point of the command: %v", err)
		}
		if hasFitnessAttestation(root, "codex") {
			t.Error("with no snapshot to compare against, a submitted profile must not keep an " +
				"attestation nothing can vouch still matches it")
		}
	})

	t.Run("a profile name that is not a plain file name never reaches the filesystem", func(t *testing.T) {
		root := setupConfigFactory(t)
		// Profile names are not validated anywhere, and the attestation path joins them straight
		// into a filesystem path — so an invalidation sweep must never treat a traversal sequence
		// as a file it may delete.
		bystander := filepath.Join(root, ".runtime", "victim.json")
		if err := os.MkdirAll(filepath.Dir(bystander), 0o755); err != nil {
			t.Fatalf("mkdir .runtime: %v", err)
		}
		if err := os.WriteFile(bystander, []byte(`{"keep":"me"}`), 0o644); err != nil {
			t.Fatalf("seed bystander file: %v", err)
		}

		traversal := `{"models":{"../victim":{"ANTHROPIC_MODEL":"claude-opus-4-8"}}}`
		if _, _, err := runConfigModelsSetSplit(t, traversal); err != nil {
			t.Fatalf("the registry itself is legal today; the guard must not start rejecting it: %v", err)
		}
		if _, err := os.Stat(bystander); err != nil {
			t.Errorf("a profile name must never be used to delete a file outside the attestation directory: %v", err)
		}
	})
}

// mustRoot returns the factory root of the working directory the test harness chdir'd into.
func mustRoot(t *testing.T) string {
	t.Helper()
	wd, err := getWd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		t.Fatalf("resolve factory root: %v", err)
	}
	return root
}
