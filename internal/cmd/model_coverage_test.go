package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// Issue #598 Phase 3a — surfacing. Phases 1a/2 landed the coverage vocabulary in internal/config
// with zero production callers; these tests pin the three surfaces that consume it: the write-time
// lint (config_set.go), check's per-class verdicts + coverage record (config_models.go), and the
// launch-time warnings (sling.go).
//
// Every test that touches the launch warnings pins ANTHROPIC_BASE_URL with t.Setenv. The ambient
// warn reads the PROCESS environment, an af agent pane exports that variable, and the sanctioned
// test wipe (testsupport/tmuxisolation.NeutralizeAFEnv) clears only AF_*/CLAUDE_* — so without the
// pin these assertions would pass or fail depending on the host that ran them.

// writeCoverageFixture stages a .runtime/model_coverage/<profile>.json record directly (not via
// `check`) so the launch-side reader is tested independently of the command's writer, mirroring
// writeAttestationFixture.
func writeCoverageFixture(t *testing.T, root, profile, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".runtime", "model_coverage")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir model_coverage: %v", err)
	}
	p := filepath.Join(dir, profile+".json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write coverage record: %v", err)
	}
	return p
}

// coverageRecordJSON builds a well-formed record at the current schema version. served=false on a
// class is what the "last check found X NOT SERVED" warning keys on.
func coverageRecordJSON(profile string, classes map[string]bool) string {
	rows := make([]string, 0, len(classes))
	failing := 0
	for _, class := range sortedMapKeys(classes) {
		rows = append(rows, fmt.Sprintf(`{"class":%q,"model":"gw-main-v1","served":%t}`, class, classes[class]))
		if !classes[class] {
			failing++
		}
	}
	return fmt.Sprintf(`{"v":%d,"profile":%q,"checked_at":"2026-08-01T00:00:00Z","served_hash":"deadbeef","failing":%d,"classes":[%s]}`,
		modelCoverageVersion, profile, failing, strings.Join(rows, ","))
}

// pinAmbientEndpoint fixes ANTHROPIC_BASE_URL in the process env and clears the launch-warning
// dedupe, so a case neither inherits the host's shell nor an earlier case's suppression.
func pinAmbientEndpoint(t *testing.T, value string) {
	t.Helper()
	t.Setenv(baseURLKey, value)
	resetModelCoverageWarnings()
	t.Cleanup(resetModelCoverageWarnings)
}

// gatewayServedIDs is the checked-in litellm.yaml shape plus the claude-* aliases Phase 3c adds, so
// a profile derived from it is fully covered.
func gatewayServedIDs() []string {
	return []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "claude-fable-5", "claude-opus-5"}
}

// withFableProfile adds the direct fable profile the seeded registry ships (install.go). A fixture
// that omits it is exercising the UNVERIFIABLE case, not the happy one: with no fable id named
// anywhere, check has nothing to demand an alias for and says so rather than passing. Every fixture
// below that asserts a clean exit therefore has to name one.
func withFableProfile(models map[string]map[string]string) *config.ModelsConfig {
	models["fable-5"] = map[string]string{"ANTHROPIC_MODEL": "claude-fable-5"}
	return &config.ModelsConfig{Models: models}
}

func stubModelsProbe(t *testing.T, ids []string, err error) {
	t.Helper()
	orig := httpProbe
	httpProbe = func(baseURL, authToken string) ([]string, error) { return ids, err }
	t.Cleanup(func() { httpProbe = orig })
}

// --- C5: per-class verdicts -------------------------------------------------------------------

// TestConfigModelsCheck_ModelCoverage_PerClassVerdicts drives both litellm shapes the design names:
// the quickstart-seeded gpt-4o pair and this factory's checked-in gpt-5.6-* trio. The id each
// verdict reports must be the EFFECTIVE post-derivation value, not the raw declaration — that is
// the whole point of routing through config.CompleteEndpointProfile.
func TestConfigModelsCheck_ModelCoverage_PerClassVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		profile map[string]string
		served  []string
		want    map[string]string // class label -> effective id its verdict line must name
		source  map[string]string // class label -> the key the line must credit that id to
	}{
		{
			// The gpt-4o pair quickstart.sh seeds: a main model plus a declared haiku. The
			// small/background expectation is the interesting one — nothing declares it, and it
			// derives from the DECLARED HAIKU rather than from the main model.
			name: "quickstart-seeded gpt-4o shape derives small/background from the declared haiku",
			profile: map[string]string{
				"ANTHROPIC_MODEL":               "gpt-4o",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-4o-mini",
				"ANTHROPIC_BASE_URL":            "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN":          "file:secrets/codex.key",
			},
			served: []string{"gpt-4o", "gpt-4o-mini", "claude-fable-5"},
			want: map[string]string{
				"small/background": "gpt-4o-mini",
				"opus":             "gpt-4o",
				"sonnet":           "gpt-4o",
				"haiku":            "gpt-4o-mini",
			},
			source: map[string]string{
				"small/background": "derived from ANTHROPIC_DEFAULT_HAIKU_MODEL",
				"opus":             "derived from ANTHROPIC_MODEL",
				"haiku":            "declared",
			},
		},
		{
			name: "checked-in gpt-5.6 shape keeps a declared haiku and derives the rest",
			profile: map[string]string{
				"ANTHROPIC_MODEL":               "gpt-5.6-terra",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-luna",
				"ANTHROPIC_BASE_URL":            "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN":          "file:secrets/codex.key",
			},
			served: gatewayServedIDs(),
			want: map[string]string{
				"small/background": "gpt-5.6-luna",
				"opus":             "gpt-5.6-terra",
				"sonnet":           "gpt-5.6-terra",
				"haiku":            "gpt-5.6-luna",
			},
			source: map[string]string{
				"small/background": "derived from ANTHROPIC_DEFAULT_HAIKU_MODEL",
				"sonnet":           "derived from ANTHROPIC_MODEL",
				"haiku":            "declared",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := setupConfigFactory(t)
			writeValidModels(t, root, withFableProfile(map[string]map[string]string{"codex": tc.profile}))
			writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
			stubModelsProbe(t, tc.served, nil)

			out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
			if err != nil {
				t.Fatalf("a gateway serving every derived class and the fable alias must pass; err=%v out=%q", err, out)
			}
			for label, id := range tc.want {
				want := fmt.Sprintf("class %s → %q", label, id)
				if !strings.Contains(out, want) {
					t.Errorf("check must print the effective post-derivation id per class: missing %q\nout=%s", want, out)
				}
			}
			// The parenthetical exists to point at the key the operator must edit, so it has to name
			// the rung the ladder actually used. Crediting ANTHROPIC_MODEL for everything undeclared
			// would send them to the wrong line of models.json — the cross rungs copy from the
			// haiku/small-fast pair first.
			for label, source := range tc.source {
				want := fmt.Sprintf("class %s → %q (%s)", label, tc.want[label], source)
				if !strings.Contains(out, want) {
					t.Errorf("check must credit a class's id to its real source: missing %q\nout=%s", want, out)
				}
			}
			if strings.Count(out, ": served") < len(tc.want) {
				t.Errorf("every covered class must report served; out=%s", out)
			}
			if strings.Contains(out, "sk-real-value") {
				t.Errorf("check must never print token material; out=%q", out)
			}
		})
	}
}

// TestConfigModelsCheck_ModelCoverage_NoSubagentVerdict pins Decision 14 at the check surface.
// CLAUDE_CODE_SUBAGENT_MODEL is a member of the exported EndpointClassKeys inventory but derivation
// deliberately never fills it, so a verdict row over the inventory would report an empty id as
// unserved on EVERY endpoint profile — an unfixable hard failure for every gateway in existence.
func TestConfigModelsCheck_ModelCoverage_NoSubagentVerdict(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, withFableProfile(map[string]map[string]string{
		"codex": {
			"ANTHROPIC_MODEL":      "gpt-5.6-terra",
			"ANTHROPIC_BASE_URL":   "http://localhost:4000",
			"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
		},
	}))
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	stubModelsProbe(t, gatewayServedIDs(), nil)

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err != nil {
		t.Fatalf("a fully served gateway must pass; err=%v out=%q", err, out)
	}
	if strings.Contains(out, "CLAUDE_CODE_SUBAGENT_MODEL") || strings.Contains(out, "sub-agent") {
		t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL is never derived, so it must get no verdict row; out=%s", out)
	}
}

// --- C5: unserved class is a HARD failure ------------------------------------------------------

func TestConfigModelsCheck_ModelCoverage_UnservedClassFailsNonZero(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.6-terra",
				"ANTHROPIC_BASE_URL":   "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	// The gateway serves an unrelated id: every derived class resolves to gpt-5.6-terra, which is
	// absent, so all four classes are unserved.
	stubModelsProbe(t, []string{"some-other-model"}, nil)

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("an unserved class must be a HARD failure so `af config models check` exits non-zero; out=%q", out)
	}
	if !strings.Contains(out, "NOT SERVED") {
		t.Errorf("the unserved class must be named NOT SERVED; out=%s", out)
	}
	if !strings.Contains(out, "codex") || !strings.Contains(out, "opus") {
		t.Errorf("the message must name the profile and the class (api.md:129-131); out=%s", out)
	}
	if !strings.Contains(out, "litellm.yaml") {
		t.Errorf("the message must carry a runtime-fixable remedy (Gap 12, ADR-019); out=%s", out)
	}
}

// TestConfigModelsCheck_ModelCoverage_FableAliasUnservedFailsNonZero pins the half of issue #598 no
// env key can express: the Fable class has no ANTHROPIC_DEFAULT_FABLE_MODEL rung, so a Fable request
// leaves the host as its built-in claude-fable-* id whatever the profile declares.
func TestConfigModelsCheck_ModelCoverage_FableAliasUnservedFailsNonZero(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.6-terra",
				"ANTHROPIC_BASE_URL":   "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	// Every derived class IS served; only the claude-fable-* alias is missing.
	stubModelsProbe(t, []string{"gpt-5.6-terra"}, nil)

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("a gateway with no claude-fable-* alias must fail; out=%q", out)
	}
	if !strings.Contains(out, "fable-class") || !strings.Contains(out, "NOT SERVED") {
		t.Errorf("the fable-class row must report NOT SERVED; out=%s", out)
	}
}

// TestConfigModelsCheck_ModelCoverage_PrefixMatchIsNotProofOfFableCoverage: with no fable id named
// anywhere in the registry there is nothing to demand an alias FOR — the id the host will ask for is
// a property of the installed CLI. Passing the row because the gateway aliases some other fable
// generation would be the exact failure this surface exists to prevent: a clean check, a silent
// launch, and a spawn that still dies.
func TestConfigModelsCheck_ModelCoverage_PrefixMatchIsNotProofOfFableCoverage(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.6-terra",
				"ANTHROPIC_BASE_URL":   "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	stubModelsProbe(t, []string{"gpt-5.6-terra", "claude-fable-4-ancient"}, nil)

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("an alias for some other fable generation must not pass the fable row; out=%q", out)
	}
	if !strings.Contains(out, "names none to check it against") {
		t.Errorf("the verdict must say the registry gave it no id to demand, not that no alias exists; out=%s", out)
	}
	rec, ok := readModelCoverageRecord(root, "codex")
	if !ok {
		t.Fatal("the check must have left a readable record")
	}
	for _, verdict := range rec.Classes {
		if verdict.Class == "fable-class" && verdict.Served {
			t.Errorf("an unverifiable fable row must never be recorded served, or the launch stays silent too; got %+v", verdict)
		}
	}
}

// TestConfigModelsCheck_ModelCoverage_DirectProfileClaudeIDsChecked covers the registry half of the
// alias verdict: checkProfile is handed one profile and cannot see cfg.Models, so the claude-* ids
// the registry's DIRECT profiles name have to be passed in.
func TestConfigModelsCheck_ModelCoverage_DirectProfileClaudeIDsChecked(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.6-terra",
				"ANTHROPIC_BASE_URL":   "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
			"opus-5": {"ANTHROPIC_MODEL": "claude-opus-5"},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	// Fable alias present, but the direct profile's claude-opus-5 is not aliased.
	stubModelsProbe(t, []string{"gpt-5.6-terra", "claude-fable-5"}, nil)

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("a claude-* id named by a direct profile but not aliased must fail; out=%q", out)
	}
	if !strings.Contains(out, "claude-opus-5") {
		t.Errorf("the unserved direct-profile id must be named; out=%s", out)
	}
	// The id has to survive into the RECORD's class name, not just the terminal line: the launch
	// warning replays class names, and "alias NOT SERVED" would not say which alias to add.
	rec, ok := readModelCoverageRecord(root, "codex")
	if !ok {
		t.Fatal("the check must have left a readable record")
	}
	if !strings.Contains(strings.Join(unservedCoverageClasses(rec), " "), "claude-opus-5") {
		t.Errorf("the recorded class name must carry the id; unserved=%v", unservedCoverageClasses(rec))
	}
}

// TestConfigModelsCheck_ModelCoverage_EveryFableIDNeedsItsOwnAlias: an alias for claude-fable-4 does
// not answer a claude-fable-5 request, so checking one representative fable id would leave the rest
// as exactly the silent gap this surface exists to close.
func TestConfigModelsCheck_ModelCoverage_EveryFableIDNeedsItsOwnAlias(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.6-terra",
				"ANTHROPIC_BASE_URL":   "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
			"fable-5":      {"ANTHROPIC_MODEL": "claude-fable-5"},
			"fable-5-fast": {"ANTHROPIC_MODEL": "claude-fable-5-fast"},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	stubModelsProbe(t, []string{"gpt-5.6-terra", "claude-fable-5"}, nil)

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("a second fable id with no alias must still fail; out=%q", out)
	}
	if !strings.Contains(out, `"claude-fable-5-fast": NOT SERVED`) {
		t.Errorf("the unaliased fable id must be named; out=%s", out)
	}
	if !strings.Contains(out, `"claude-fable-5": served`) {
		t.Errorf("the aliased fable id must still report served; out=%s", out)
	}

	rec, ok := readModelCoverageRecord(root, "codex")
	if !ok {
		t.Fatal("the check must have left a readable record")
	}
	if got := unservedCoverageClasses(rec); len(got) != 1 || got[0] != "fable-class" {
		t.Errorf("the launch warning must name fable-class exactly once; got %v", got)
	}
}

// TestModelCoverage_UnservedClassesAreDedupedByName drives the case the check-level test above
// cannot: TWO unserved rows sharing a class. Only then does the deduper do any work — with one row
// served the list has a single entry whatever the function does. A registry naming two fable ids on
// a gateway aliasing neither is exactly that shape, and "fable-class, fable-class" would read as a
// rendering bug rather than as the two aliases it is.
func TestModelCoverage_UnservedClassesAreDedupedByName(t *testing.T) {
	rec := modelCoverageRecord{
		Profile: "codex",
		Classes: []modelCoverageVerdict{
			{Class: "opus", Model: "gpt-5.6-terra", Served: true},
			{Class: "fable-class", Model: "claude-fable-5", Served: false},
			{Class: "fable-class", Model: "claude-fable-5-fast", Served: false},
			{Class: "haiku", Model: "gpt-5.6-luna", Served: false},
		},
	}
	got := unservedCoverageClasses(rec)
	want := []string{"fable-class", "haiku"}
	if len(got) != len(want) {
		t.Fatalf("unservedCoverageClasses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unservedCoverageClasses = %v, want %v (record order preserved)", got, want)
		}
	}
}

// TestModelCoverage_ServedHashIgnoresOrder pins what the field is for: it fingerprints WHICH ids a
// gateway advertised, not the order it happened to list them in. A gateway that merely reorders its
// model_list must not read as a changed inventory.
func TestModelCoverage_ServedHashIgnoresOrder(t *testing.T) {
	forward := servedListHash([]string{"gpt-5.6-terra", "gpt-5.6-luna", "claude-fable-5"})
	shuffled := servedListHash([]string{"claude-fable-5", "gpt-5.6-terra", "gpt-5.6-luna"})
	if forward != shuffled {
		t.Errorf("reordering the served list must not change the hash;\n %s\n %s", forward, shuffled)
	}
	if forward == servedListHash([]string{"gpt-5.6-terra", "gpt-5.6-luna"}) {
		t.Error("dropping an advertised id MUST change the hash, or the field records nothing")
	}
	// The hash is written to a file an operator can read; it must be a digest of the ids, not the
	// ids themselves.
	if strings.Contains(forward, "gpt-5.6-terra") {
		t.Errorf("the hash must not carry the ids verbatim; got %q", forward)
	}
}

// TestConfigModelsCheck_ModelCoverage_NoMainModel_ReportsUncovered is full issue #598: an endpoint
// profile that declares no ANTHROPIC_MODEL derives nothing, so every class launches empty.
func TestConfigModelsCheck_ModelCoverage_NoMainModel_ReportsUncovered(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"bare-gateway": {
				"ANTHROPIC_BASE_URL":   "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	stubModelsProbe(t, gatewayServedIDs(), nil)

	out, err := runModelsCmd(t, runConfigModelsCheck, "bare-gateway")
	if err == nil {
		t.Fatalf("an endpoint profile that derives no class at all must fail; out=%q", out)
	}
	if !strings.Contains(out, "no id") {
		t.Errorf("a class with nothing to request must say so rather than reporting an empty id as served; out=%s", out)
	}
	if !strings.Contains(out, modelKey) {
		t.Errorf("the remedy must name ANTHROPIC_MODEL, the source the whole ladder needs; out=%s", out)
	}
}

// TestConfigModelsCheck_ModelCoverage_UnreachableSkipsVerdicts keeps the transport failure ordered
// ahead of coverage: with no served list there is nothing to compare against, and inventing verdicts
// would report every class unserved for a reason that is not about coverage.
func TestConfigModelsCheck_ModelCoverage_UnreachableSkipsVerdicts(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.6-terra",
				"ANTHROPIC_BASE_URL":   "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	stubModelsProbe(t, nil, os.ErrDeadlineExceeded)

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("an unreachable endpoint must still fail; out=%q", out)
	}
	if strings.Contains(out, "NOT SERVED") {
		t.Errorf("no per-class verdict may be printed when the probe never returned a list; out=%s", out)
	}
	rec, ok := readModelCoverageRecord(root, "codex")
	if !ok {
		t.Fatal("an unreachable check must still record that it ran, or a stale passing record survives it")
	}
	if len(rec.Classes) != 0 {
		t.Errorf("a probe that returned no list must record no verdicts, not invented ones; got %+v", rec.Classes)
	}
}

// TestConfigModelsCheck_ModelCoverage_SecretFailureAlsoInvalidatesTheRecord: every path that gives
// up before a served list must supersede the last record, not just the unreachable one. A profile
// whose secret has been rotated away measured nothing either.
func TestConfigModelsCheck_ModelCoverage_SecretFailureAlsoInvalidatesTheRecord(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.6-terra",
				"ANTHROPIC_BASE_URL":   "http://localhost:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/gone.key",
			},
		},
	})
	writeCoverageFixture(t, root, "codex", coverageRecordJSON("codex", map[string]bool{"opus": true}))
	stubModelsProbe(t, gatewayServedIDs(), nil)

	if _, err := runModelsCmd(t, runConfigModelsCheck, "codex"); err == nil {
		t.Fatal("a missing secret file must fail the check")
	}
	rec, ok := readModelCoverageRecord(root, "codex")
	if !ok {
		t.Fatal("the check must still record that it ran")
	}
	if len(rec.Classes) != 0 {
		t.Errorf("a check that never probed must supersede the prior verdicts; got %+v", rec.Classes)
	}
}

// TestConfigModelsCheck_ModelCoverage_UnreachableInvalidatesAPassingRecord is the sequence that makes
// the empty record matter: a profile checks clean, the gateway later goes down, and the operator
// re-checks. Leaving the passing record in place would have every launch after the outage keep
// reporting coverage that was measured against a gateway that is no longer answering.
func TestConfigModelsCheck_ModelCoverage_UnreachableInvalidatesAPassingRecord(t *testing.T) {
	pinAmbientEndpoint(t, "")
	root := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, root, gatewayLoopbackModels())
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	writeCoverageFixture(t, root, "codex", coverageRecordJSON("codex", map[string]bool{
		"opus": true, "fable-class": true,
	}))

	var quiet bytes.Buffer
	if _, _, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), "codex", "", false, &quiet); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if quiet.String() != "" {
		t.Fatalf("the staged passing record must start out silent, else the assertion below is vacuous; got %q", quiet.String())
	}

	stubModelsProbe(t, nil, os.ErrDeadlineExceeded)
	t.Chdir(root)
	if _, err := runModelsCmd(t, runConfigModelsCheck, "codex"); err == nil {
		t.Fatal("an unreachable endpoint must fail")
	}

	resetModelCoverageWarnings()
	var warn bytes.Buffer
	if _, _, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), "codex", "", false, &warn); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(warn.String(), "af config models check codex") {
		t.Errorf("after a failed check the launch must stop reporting the superseded verdicts; got %q", warn.String())
	}
}

// --- C5: the coverage record --------------------------------------------------------------------

func TestConfigModelsCheck_ModelCoverage_RecordWritten(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, withFableProfile(map[string]map[string]string{
		"codex": {
			"ANTHROPIC_MODEL":      "gpt-5.6-terra",
			"ANTHROPIC_BASE_URL":   "http://localhost:4000",
			"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
		},
	}))
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	stubModelsProbe(t, gatewayServedIDs(), nil)

	if _, err := runModelsCmd(t, runConfigModelsCheck, "codex"); err != nil {
		t.Fatalf("fixture must pass so the record is written on the success path too; err=%v", err)
	}

	recDir := filepath.Join(root, ".runtime", "model_coverage")
	data, err := os.ReadFile(filepath.Join(recDir, "codex.json"))
	if err != nil {
		t.Fatalf("check must leave a coverage record behind: %v", err)
	}
	var rec struct {
		V          int    `json:"v"`
		Profile    string `json:"profile"`
		CheckedAt  string `json:"checked_at"`
		ServedHash string `json:"served_hash"`
		Classes    []struct {
			Class  string `json:"class"`
			Model  string `json:"model"`
			Served bool   `json:"served"`
		} `json:"classes"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("the coverage record must be valid JSON: %v", err)
	}
	if rec.V != modelCoverageVersion {
		t.Errorf("the record must carry the schema version so a future shape can be rejected; got v=%d", rec.V)
	}
	if rec.Profile != "codex" {
		t.Errorf("the record must name its profile (the fail-closed reader keys on it); got %q", rec.Profile)
	}
	if rec.CheckedAt == "" || rec.ServedHash == "" {
		t.Errorf("the record must carry the probe timestamp and the served-list hash; got %+v", rec)
	}
	if len(rec.Classes) < len(config.DerivedEndpointClassKeys()) {
		t.Errorf("the record must carry one verdict per derivable class plus the alias rows; got %d", len(rec.Classes))
	}
	if strings.Contains(string(data), "sk-real-value") {
		t.Errorf("the record must carry no token material (security.md:104-108)")
	}
	for _, e := range mustReadDir(t, recDir) {
		if strings.HasSuffix(e, ".tmp") {
			t.Errorf("temp residue after the atomic coverage-record write: %s", e)
		}
	}
}

// TestConfigModelsCheck_ModelCoverage_RecordReadFailsClosed pins the reader's posture: anything it
// cannot fully trust counts as ABSENT, so a forged record can at most suppress a warning.
func TestConfigModelsCheck_ModelCoverage_RecordReadFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"corrupt", "not json{{{"},
		{"profile mismatch", coverageRecordJSON("other", map[string]bool{"opus": true})},
		{"unrecognised version", `{"v":99,"profile":"codex","checked_at":"2026-08-01T00:00:00Z","classes":[]}`},
		{"empty file", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeCoverageFixture(t, root, "codex", tc.body)
			if _, ok := readModelCoverageRecord(root, "codex"); ok {
				t.Errorf("a %s record must be treated as absent (fail-closed, security.md:111-114)", tc.name)
			}
		})
	}

	t.Run("unreadable", func(t *testing.T) {
		root := t.TempDir()
		if _, ok := readModelCoverageRecord(root, "never-written"); ok {
			t.Error("a record that does not exist must be treated as absent")
		}
	})

	t.Run("round trip", func(t *testing.T) {
		root := t.TempDir()
		if err := writeModelCoverageRecord(root, modelCoverageRecord{
			Profile:   "codex",
			CheckedAt: "2026-08-01T00:00:00Z",
			Classes:   []modelCoverageVerdict{{Class: "opus", Model: "gpt-5.6-terra", Served: true}},
		}); err != nil {
			t.Fatalf("write coverage record: %v", err)
		}
		rec, ok := readModelCoverageRecord(root, "codex")
		if !ok {
			t.Fatal("a record this package just wrote must read back — otherwise every launch warns forever")
		}
		if rec.V != modelCoverageVersion {
			t.Errorf("the writer must stamp the version, not the caller; got v=%d", rec.V)
		}
	})
}

// --- C5: --live ---------------------------------------------------------------------------------

func TestConfigModelsCheck_ModelCoverage_LiveFlagRegistered(t *testing.T) {
	f := configModelsCheckCmd.Flags().Lookup("live")
	if f == nil {
		t.Fatal("`af config models check` must register --live for the per-class /v1/messages smoke")
	}
	if f.DefValue != "false" {
		t.Errorf("--live must default to false so no default-suite run performs a live request; got %q", f.DefValue)
	}
	if configModelsCheckLive {
		t.Error("the --live package var must be false unless a case sets it, or an unrelated check case would try to send one")
	}
}

// enableLiveSmoke turns --live on and substitutes its transport seam, so the behavioural stage runs
// with no network at all (ADR-018). Both are package vars and both are restored: a leaked flag would
// have every later check case in this binary attempt a live request.
func enableLiveSmoke(t *testing.T, respond func(model string) (*http.Response, error)) *[]string {
	t.Helper()
	origFlag, origSeam := configModelsCheckLive, modelsMessagesDo
	configModelsCheckLive = true
	var asked []string
	modelsMessagesDo = func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("reading the smoke request body: %v", err)
		}
		var payload struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("the smoke body must be JSON: %v (%q)", err, body)
		}
		asked = append(asked, fmt.Sprintf("%s %s max_tokens=%d version=%s auth=%t",
			req.URL.String(), payload.Model, payload.MaxTokens,
			req.Header.Get("anthropic-version"), req.Header.Get("Authorization") != ""))
		return respond(payload.Model)
	}
	t.Cleanup(func() { configModelsCheckLive, modelsMessagesDo = origFlag, origSeam })
	return &asked
}

func liveAnswer(status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

func liveFixture(t *testing.T) string {
	t.Helper()
	root := setupConfigFactory(t)
	writeValidModels(t, root, withFableProfile(map[string]map[string]string{
		"codex": {
			"ANTHROPIC_MODEL":               "gpt-5.6-terra",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-luna",
			"ANTHROPIC_BASE_URL":            "http://localhost:4000",
			"ANTHROPIC_AUTH_TOKEN":          "file:secrets/codex.key",
		},
	}))
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	return root
}

func TestConfigModelsCheck_ModelCoverage_LiveSmokesEveryServedClass(t *testing.T) {
	root := liveFixture(t)
	stubModelsProbe(t, gatewayServedIDs(), nil)
	asked := enableLiveSmoke(t, func(string) (*http.Response, error) {
		return liveAnswer(200, `{"type":"message","content":[]}`)
	})

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err != nil {
		t.Fatalf("a gateway that answers every class must pass; err=%v out=%q", err, out)
	}
	if len(*asked) == 0 {
		t.Fatal("--live must send a request per served class")
	}
	for _, req := range *asked {
		// max_tokens is the floor a Responses-API-backed model accepts; a smaller ceiling is
		// rejected outright and would read as an unserved class rather than as a bad request.
		if !strings.Contains(req, "http://localhost:4000/v1/messages") ||
			!strings.Contains(req, "max_tokens=16") ||
			!strings.Contains(req, "version=2023-06-01") ||
			!strings.Contains(req, "auth=true") {
			t.Errorf("unexpected smoke request shape: %s", req)
		}
	}
	if !strings.Contains(out, "--live") || !strings.Contains(out, "answered") {
		t.Errorf("each smoked class must report that the gateway answered; out=%s", out)
	}
	if strings.Contains(out, "sk-real-value") {
		t.Errorf("--live must never print token material; out=%q", out)
	}
	rec, ok := readModelCoverageRecord(root, "codex")
	if !ok || rec.Failing != 0 {
		t.Errorf("a fully answering gateway must record no failures; ok=%t rec=%+v", ok, rec)
	}
}

// TestConfigModelsCheck_ModelCoverage_LiveRefusalFailsAndIsRecorded is the case that makes running
// the smoke before the verdicts are counted matter: a gateway that lists a class and then refuses it
// must not leave a record telling every later launch that nothing was wrong.
func TestConfigModelsCheck_ModelCoverage_LiveRefusalFailsAndIsRecorded(t *testing.T) {
	root := liveFixture(t)
	stubModelsProbe(t, gatewayServedIDs(), nil)
	enableLiveSmoke(t, func(model string) (*http.Response, error) {
		if model == "gpt-5.6-luna" {
			return liveAnswer(400, `{"type":"error"}`)
		}
		return liveAnswer(200, `{"type":"message"}`)
	})

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("a class the gateway lists but refuses must fail the check; out=%q", out)
	}
	if !strings.Contains(out, `--live haiku → "gpt-5.6-luna": NOT SERVED`) {
		t.Errorf("the refusing class must be named; out=%s", out)
	}
	rec, ok := readModelCoverageRecord(root, "codex")
	if !ok {
		t.Fatal("the check must have left a readable record")
	}
	if rec.Failing == 0 {
		t.Errorf("a refusal must reach the record, not only the exit code; rec=%+v", rec)
	}
	for _, verdict := range rec.Classes {
		if verdict.Model == "gpt-5.6-luna" && verdict.Served {
			t.Errorf("the refused class must be recorded unserved; got %+v", verdict)
		}
	}
}

// TestConfigModelsCheck_ModelCoverage_LiveSkipsUnsmokeableRows: a row that already failed
// structurally has nothing to ask for, and the fable family row's id is a wildcard, not a model name.
func TestConfigModelsCheck_ModelCoverage_LiveSkipsUnsmokeableRows(t *testing.T) {
	root := liveFixture(t)
	stubModelsProbe(t, []string{"gpt-5.6-terra", "claude-fable-5"}, nil) // gpt-5.6-luna absent
	asked := enableLiveSmoke(t, func(string) (*http.Response, error) {
		return liveAnswer(200, `{"type":"message"}`)
	})

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("the structurally unserved classes must still fail; out=%q", out)
	}
	// Without this the skip assertions below would hold trivially for a --live stage that ran
	// nothing at all.
	if len(*asked) == 0 || !strings.Contains(strings.Join(*asked, "\n"), "gpt-5.6-terra") {
		t.Fatalf("--live must still smoke the classes that DID list; asked=%v", *asked)
	}
	for _, req := range *asked {
		if strings.Contains(req, "gpt-5.6-luna") {
			t.Errorf("a class the gateway never listed must not be smoked: %s", req)
		}
		if strings.Contains(req, "*") {
			t.Errorf("a wildcard is not a model name and must not be smoked: %s", req)
		}
	}
	_ = root
}

// TestConfigModelsCheck_ModelCoverage_LiveTransportErrorIsAVerdict: a refusal need not be an HTTP
// status. A connection that never completes is the same operator-visible outcome — that class cannot
// be served — and must not escape as an unhandled error.
func TestConfigModelsCheck_ModelCoverage_LiveTransportErrorIsAVerdict(t *testing.T) {
	liveFixture(t)
	stubModelsProbe(t, gatewayServedIDs(), nil)
	enableLiveSmoke(t, func(string) (*http.Response, error) {
		return nil, os.ErrDeadlineExceeded
	})

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("a smoke that never completed must fail the check; out=%q", out)
	}
	if !strings.Contains(out, "--live") || !strings.Contains(out, "NOT SERVED") {
		t.Errorf("the transport failure must surface as a per-class verdict; out=%s", out)
	}
}

// TestConfigModelsCheck_ModelCoverage_LiveRejectsANonMessageAnswer: a gateway can return 200 with an
// error envelope. Only a message body proves the class is actually served.
func TestConfigModelsCheck_ModelCoverage_LiveRejectsANonMessageAnswer(t *testing.T) {
	liveFixture(t)
	stubModelsProbe(t, gatewayServedIDs(), nil)
	enableLiveSmoke(t, func(string) (*http.Response, error) {
		return liveAnswer(200, `{"type":"error","error":{"message":"no such model"}}`)
	})

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("a 200 that is not a message must not count as served; out=%q", out)
	}
	if !strings.Contains(out, "want a message") {
		t.Errorf("the verdict must say what was expected; out=%s", out)
	}
}

// --- C4: the write-time lint ---------------------------------------------------------------------

func TestConfigModelsSet_ModelCoverage_WarnsOnStderrAndStillSaves(t *testing.T) {
	root := setupConfigFactory(t)
	stdin := `{"models":{"codex":{"ANTHROPIC_MODEL":"gpt-5.6-terra","ANTHROPIC_BASE_URL":"http://localhost:4000","ANTHROPIC_AUTH_TOKEN":"file:secrets/codex.key"}}}`

	stdout, stderr, err := runConfigModelsSetSplit(t, stdin)
	if err != nil {
		t.Fatalf("the coverage lint warns and never rejects; got err=%v", err)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "codex") {
		t.Errorf("an endpoint profile leaving classes to derivation must warn on stderr; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "derivation") {
		t.Errorf("the warning must name what will be derived; stderr=%q", stderr)
	}
	if !strings.Contains(stdout, "Models configuration saved.") || strings.Contains(stdout, "warning") {
		t.Errorf("stdout must carry only the save confirmation; stdout=%q", stdout)
	}
	cfg, loadErr := config.LoadModelsConfig(root)
	if loadErr != nil {
		t.Fatalf("reload models.json: %v", loadErr)
	}
	if _, ok := cfg.Models["codex"]; !ok {
		t.Error("the lint must never block the write — the profile has to survive on disk")
	}
}

func TestConfigModelsSet_ModelCoverage_SilentForDirectProfiles(t *testing.T) {
	setupConfigFactory(t)
	stdin := `{"models":{"opus-5":{"ANTHROPIC_MODEL":"claude-opus-5"},"sonnet-5":{"ANTHROPIC_MODEL":"claude-sonnet-5"}}}`

	_, stderr, err := runConfigModelsSetSplit(t, stdin)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if stderr != "" {
		t.Errorf("profiles with no ANTHROPIC_BASE_URL must not warn — derivation never runs there; stderr=%q", stderr)
	}
}

// --- C6: the launch surfaces ----------------------------------------------------------------------

// TestResolveLaunchModelEnv_ModelCoverage_RespawnEmitsNothing is the first assertion in this package
// that the resolver's warn buffer is EMPTY. Every other resolver test asserts with strings.Contains,
// so until now "the respawn path emits zero new output" — the phase's own end state — was enforced
// by nothing at all.
//
// It drives the ambient endpoint SET, because a respawn inherits the operator's shell exactly as a
// launch does: silence here has to hold for the noisiest environment, not the quietest.
func TestResolveLaunchModelEnv_ModelCoverage_RespawnEmitsNothing(t *testing.T) {
	pinAmbientEndpoint(t, "http://ambient.example:9999")
	dir := setupTestFactoryForDone(t, "manager")
	cfg := gatewayLoopbackModels()
	cfg.Agents = map[string]string{"manager": "codex"}
	writeValidModels(t, dir, cfg)
	// deliberately no coverage record: a fresh launch would warn here.

	var warn bytes.Buffer
	name, env, err := resolveRespawnModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", &warn)
	if err != nil {
		t.Fatalf("a respawn must never brick; got err: %v", err)
	}
	if name != "codex" || len(env) == 0 {
		t.Fatalf("fixture must resolve through the profile branch, else the assertion below is vacuous; got name=%q env=%v", name, env)
	}
	if warn.String() != "" {
		t.Errorf("the respawn path must emit ZERO new output; got %q", warn.String())
	}
}

// TestResolveLaunchModelEnv_ModelCoverage_RespawnIsSilentWhenNoProfileResolves covers the OTHER
// respawn shape, and the commonest one: an agent with no models.json entry, whose operator has
// ANTHROPIC_BASE_URL in their shell. That exits through the no-profile branch, which the
// profile-resolving respawn test above never reaches — so without this case the ambient warning
// could start firing on every compact and every handoff with the suite still green.
func TestResolveLaunchModelEnv_ModelCoverage_RespawnIsSilentWhenNoProfileResolves(t *testing.T) {
	pinAmbientEndpoint(t, "http://ambient.example:9999")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{"opus-5": {"ANTHROPIC_MODEL": "claude-opus-5"}},
	})

	var warn bytes.Buffer
	name, env, err := resolveRespawnModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", &warn)
	if err != nil {
		t.Fatalf("a respawn must never brick; got err: %v", err)
	}
	if name != "" || env != nil {
		t.Fatalf("fixture must resolve NO profile, else this exercises the wrong branch; got name=%q env=%v", name, env)
	}
	if warn.String() != "" {
		t.Errorf("a respawn must stay silent on the ambient warning too; got %q", warn.String())
	}

	// Same factory, same environment, through the launch entry point: proves the silence above is
	// the respawn wrapper's doing and not a fixture that could never warn at all.
	resetModelCoverageWarnings()
	var launchWarn bytes.Buffer
	if _, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", "", false, &launchWarn); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(launchWarn.String(), baseURLKey) {
		t.Errorf("a fresh launch in this same state MUST warn, or the respawn assertion is vacuous; got %q", launchWarn.String())
	}
}

// TestResolveLaunchModelEnv_ModelCoverage_WarnsWithoutAnExplicitModelFlag is the case a
// cliModel-based gate would silently lose: `af up` with no --model still resolves the profile from
// the agents map, and it is the launch path that runs most. A coverage surface that only speaks when
// an operator types --model would report almost nothing.
func TestResolveLaunchModelEnv_ModelCoverage_WarnsWithoutAnExplicitModelFlag(t *testing.T) {
	pinAmbientEndpoint(t, "")
	dir := setupTestFactoryForDone(t, "manager")
	cfg := gatewayLoopbackModels()
	cfg.Agents = map[string]string{"manager": "codex"}
	writeValidModels(t, dir, cfg)

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", "", false, &warn)
	if err != nil {
		t.Fatalf("the coverage warning must never block a launch; got err: %v", err)
	}
	if name != "codex" || len(env) == 0 {
		t.Fatalf("fixture must resolve the profile from the agents map; got name=%q env=%v", name, env)
	}
	if !strings.Contains(warn.String(), "af config models check codex") {
		t.Errorf("a launch that resolved a profile without --model must still report coverage; got %q", warn.String())
	}
}

func TestResolveLaunchModelEnv_ModelCoverage_SelectingWarnsNeverBlocks(t *testing.T) {
	pinAmbientEndpoint(t, "")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, gatewayLoopbackModels())

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn)
	if err != nil {
		t.Fatalf("the coverage warning must never block a launch; got err: %v", err)
	}
	if name != "codex" || len(env) == 0 {
		t.Fatalf("the launch must still return its export set; got name=%q env=%v", name, env)
	}
	if !strings.Contains(warn.String(), "codex") || !strings.Contains(warn.String(), "af config models check codex") {
		t.Errorf("an absent coverage record must warn naming the profile and the remedy; got %q", warn.String())
	}
}

func TestResolveLaunchModelEnv_ModelCoverage_FreshPassingRecordIsSilent(t *testing.T) {
	pinAmbientEndpoint(t, "")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, gatewayLoopbackModels())
	writeCoverageFixture(t, dir, "codex", coverageRecordJSON("codex", map[string]bool{
		"small/background": true, "opus": true, "sonnet": true, "haiku": true, "fable-class": true,
	}))

	var warn bytes.Buffer
	if _, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if warn.String() != "" {
		t.Errorf("a fresh, fully-served record must produce no warning — otherwise the positive cases pass vacuously; got %q", warn.String())
	}
}

func TestResolveLaunchModelEnv_ModelCoverage_FailingRecordWarns(t *testing.T) {
	pinAmbientEndpoint(t, "")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, gatewayLoopbackModels())
	writeCoverageFixture(t, dir, "codex", coverageRecordJSON("codex", map[string]bool{
		"opus": true, "fable-class": false,
	}))

	var warn bytes.Buffer
	if _, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn); err != nil {
		t.Fatalf("a failing record warns; it must never block; got err: %v", err)
	}
	if !strings.Contains(warn.String(), "fable-class") || !strings.Contains(warn.String(), "NOT SERVED") {
		t.Errorf("the warning must name the class the last check found unserved; got %q", warn.String())
	}
}

func TestResolveLaunchModelEnv_ModelCoverage_StaleRecordWarns(t *testing.T) {
	pinAmbientEndpoint(t, "")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, gatewayLoopbackModels())
	path := writeCoverageFixture(t, dir, "codex", coverageRecordJSON("codex", map[string]bool{"opus": true}))
	// Staleness is "older than models.json", not a TTL (scale.md S3): the profile changed after it
	// was last measured, so the verdicts may no longer describe it.
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("backdate the coverage record: %v", err)
	}

	var warn bytes.Buffer
	if _, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn); err != nil {
		t.Fatalf("a stale record warns; it must never block; got err: %v", err)
	}
	if !strings.Contains(warn.String(), "predates") {
		t.Errorf("a record older than models.json must warn that it is stale; got %q", warn.String())
	}
}

func TestResolveLaunchModelEnv_ModelCoverage_CorruptRecordWarns(t *testing.T) {
	pinAmbientEndpoint(t, "")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, gatewayLoopbackModels())
	writeCoverageFixture(t, dir, "codex", "not json{{{")

	var warn bytes.Buffer
	if _, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn); err != nil {
		t.Fatalf("a corrupt record warns; unlike the attestation interlock it must never refuse; got err: %v", err)
	}
	if !strings.Contains(warn.String(), "af config models check codex") {
		t.Errorf("a corrupt record must be treated as no measurement at all and warn with the remedy; got %q", warn.String())
	}
}

// TestResolveLaunchModelEnv_ModelCoverage_AmbientEndpointWarns covers six_sigma_gaps Gap 13: an
// operator's shell rc exports ANTHROPIC_BASE_URL, no profile resolves, and the session silently
// inherits a gateway af never selected.
func TestResolveLaunchModelEnv_ModelCoverage_AmbientEndpointWarns(t *testing.T) {
	pinAmbientEndpoint(t, "http://ambient.example:9999")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{"opus-5": {"ANTHROPIC_MODEL": "claude-opus-5"}},
	})

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", "", false, &warn)
	if err != nil {
		t.Fatalf("the ambient warning must never block; got err: %v", err)
	}
	if name != "" || env != nil {
		t.Fatalf("fixture must resolve NO profile, else this tests the wrong branch; got name=%q env=%v", name, env)
	}
	if !strings.Contains(warn.String(), baseURLKey) {
		t.Errorf("the warning must name the variable so the operator can find it in their shell rc; got %q", warn.String())
	}
	if strings.Contains(warn.String(), "ambient.example") {
		t.Errorf("presence only — the value must never be read or printed (SEC-5); got %q", warn.String())
	}
	if n := strings.Count(warn.String(), "warning:"); n != 1 {
		t.Errorf("exactly one warning, not %d; got %q", n, warn.String())
	}
}

func TestResolveLaunchModelEnv_ModelCoverage_NoAmbientWarnWhenUnset(t *testing.T) {
	pinAmbientEndpoint(t, "")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{"opus-5": {"ANTHROPIC_MODEL": "claude-opus-5"}},
	})

	var warn bytes.Buffer
	if _, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", "", false, &warn); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if warn.String() != "" {
		t.Errorf("no ambient endpoint means no warning; got %q", warn.String())
	}
}

// TestResolveLaunchModelEnv_ModelCoverage_DedupesAcrossAgents mirrors what `af up` does: one
// resolver call per agent from a loop in the caller (up.go:284). Without dedupe a ten-agent factory
// prints the same line ten times.
func TestResolveLaunchModelEnv_ModelCoverage_DedupesAcrossAgents(t *testing.T) {
	pinAmbientEndpoint(t, "http://ambient.example:9999")
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, gatewayLoopbackModels())

	var warn bytes.Buffer
	for i := 0; i < 4; i++ {
		if _, _, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "codex", "", false, &warn); err != nil {
			t.Fatalf("iteration %d: unexpected err: %v", i, err)
		}
	}
	if n := strings.Count(warn.String(), "no coverage check on record"); n != 1 {
		t.Errorf("the coverage warning must appear once per profile, not once per agent; got %d in %q", n, warn.String())
	}
}
