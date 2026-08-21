package cmd

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// incident598CodexProfile is the profile the incident ran on, as `./quickstart.sh --litellm` seeds
// it (quickstart.sh:893-903): a main model, a small one, and no class keys at all.
func incident598CodexProfile() map[string]string {
	return map[string]string{
		"ANTHROPIC_BASE_URL":            "http://localhost:4000",
		"ANTHROPIC_AUTH_TOKEN":          "file:.agentfactory/secrets/litellm.key",
		"ANTHROPIC_API_KEY":             "",
		"ANTHROPIC_MODEL":               "gpt-4o",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-4o-mini",
	}
}

// incident598Factory stages the incident's registry on disk and returns the factory root. The fable
// profile is what makes the fable verdict CONCRETE: with no fable id named anywhere in the registry,
// check has nothing to demand an alias for and reports the unverifiable row instead — a different
// message with a different remedy, and not the incident's shape.
func incident598Factory(t *testing.T) string {
	t.Helper()
	root := setupConfigFactory(t)
	writeValidModels(t, root, withFableProfile(map[string]map[string]string{
		"codex": incident598CodexProfile(),
	}))
	writeSecretFile(t, root, ".agentfactory/secrets/litellm.key", "sk-litellm-testvalue")
	return root
}

// TestIncident598_AC6_CheckHardFailsNamingFableClass is the incident at the operator's surface.
//
// A /gpt-fable-review sub-agent spawn on the codex profile died with `Invalid model name passed in
// model=claude-fable-5`, and nothing named it: the fable class has no profile key, so no amount of
// editing models.json covers it, and before this design nothing checked whether the gateway did.
// The test drives the real `af config models check` against a gateway that serves the profile's own
// models but no claude-fable-* alias, and requires that this is a hard failure that says the word
// fable-class out loud.
//
// All transport is faked through the httpProbe seam — no network (config_models.go:116, ADR-009).
func TestIncident598_AC6_CheckHardFailsNamingFableClass(t *testing.T) {
	t.Run("gateway without the fable alias hard-fails and names the class", func(t *testing.T) {
		root := incident598Factory(t)
		// Exactly the gateway the incident had: it serves what the profile declares, and the two ids
		// the host asks for by its own name that happen to be aliased. No claude-fable-*.
		stubModelsProbe(t, []string{"gpt-4o", "gpt-4o-mini", "claude-opus-5"}, nil)

		out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
		if err == nil {
			t.Fatalf("check exited 0 on a gateway that cannot serve the fable class; the bootstrap gate "+
				"(quickstart.sh) and CI both branch on this exit code, so a soft verdict here is how the incident "+
				"reaches an operator's terminal instead of their build\n%s", out)
		}
		if !strings.Contains(out, "fable-class") {
			t.Errorf("check failed without naming fable-class; the incident's whole cost was that nothing named the "+
				"class that died\n%s", out)
		}
		if !strings.Contains(out, "NOT SERVED") {
			t.Errorf("check failed without a NOT SERVED verdict line\n%s", out)
		}
		// The remedy has to be actionable from the message alone — the fable class is not fixable in
		// models.json, so a verdict that stopped short of the gateway would leave the operator editing
		// the one file that cannot help.
		for _, anchor := range []string{"litellm.yaml", "USING_LITELLM.md"} {
			if !strings.Contains(out, anchor) {
				t.Errorf("the fable verdict does not point at %s; %q is the remedy every NOT SERVED line carries\n%s",
					anchor, aliasRemedy, out)
			}
		}

		// The verdict has to survive the command, because the launch-time warning reads the record and
		// not the terminal — an operator who ran check yesterday is warned at `af up` today.
		rec, ok := readModelCoverageRecord(root, "codex")
		if !ok {
			t.Fatalf("check wrote no coverage record, so nothing warns at launch time\n%s", out)
		}
		unserved := unservedCoverageClasses(rec)
		if len(unserved) == 0 {
			t.Errorf("the coverage record marks every class served while check hard-failed\n%s", out)
		}
		foundFable := false
		for _, class := range unserved {
			if strings.Contains(class, "fable") {
				foundFable = true
			}
		}
		if !foundFable {
			t.Errorf("the coverage record's unserved classes %v do not include the fable class\n%s", unserved, out)
		}

		// The opus half of the incident: the class the profile never declared now derives from the main
		// model, so its verdict is about gpt-4o rather than about a claude- id the gateway would refuse.
		// If derivation regressed, this row would read `class opus → "claude-opus-5"` or `(no id)`.
		opusLabel, _ := config.EndpointClassLabel("ANTHROPIC_DEFAULT_OPUS_MODEL")
		wantOpus := `class ` + opusLabel + ` → "gpt-4o" (derived from ANTHROPIC_MODEL): served`
		if !strings.Contains(out, wantOpus) {
			t.Errorf("check does not report the opus class as derived-and-served; want a line %q\n%s", wantOpus, out)
		}
	})

	// Without this the red above proves nothing: a fixture that can never pass would produce the same
	// failure whether or not the fable verdict works. This is the same fixture with the one alias the
	// incident's gateway lacked.
	t.Run("adding the alias the gateway lacked clears the check", func(t *testing.T) {
		root := incident598Factory(t)
		stubModelsProbe(t, []string{"gpt-4o", "gpt-4o-mini", "claude-opus-5", "claude-fable-5"}, nil)

		out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
		if err != nil {
			t.Fatalf("check still fails once the gateway aliases claude-fable-5, so the failure above is the "+
				"fixture's and not the incident's: %v\n%s", err, out)
		}
		if strings.Contains(out, "NOT SERVED") {
			t.Errorf("check exited 0 but still printed a NOT SERVED verdict\n%s", out)
		}
		rec, ok := readModelCoverageRecord(root, "codex")
		if !ok {
			t.Fatalf("check wrote no coverage record on the clean path\n%s", out)
		}
		if unserved := unservedCoverageClasses(rec); len(unserved) != 0 {
			t.Errorf("the coverage record still lists %v as unserved after a clean check\n%s", unserved, out)
		}
	})
}
