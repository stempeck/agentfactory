package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// A step title AND a step description carrying a template variable, because sling.go's
// expandStepVars rewrites both in place. If the digest were taken from the parsed Formula instead
// of the file, this fixture is what would give one file two identities.
const digestFormulaTOML = `
formula = "digestfx"
type = "workflow"
version = 1

[inputs.issue]
description = "Issue ID"
type = "string"
required = false
default = "bd-99"

[[steps]]
id = "step1"
title = "Fix {{issue}}"
description = "Working on {{issue}}"
[[steps]]
id = "step2"
title = "Verify {{issue}}"
description = "Checking {{issue}}"
`

func slingWithVars(t *testing.T, root, agentDir, formulaName string, vars []string) string {
	t.Helper()
	installMemStore(t)

	params := InstantiateParams{
		Ctx: withVerbTelemetry(t.Context(), verbTelemetry{
			verb: "sling", start: time.Now(), enabled: telemetryFactoryEnabled(root),
		}),
		FormulaName: formulaName,
		AgentName:   "manager",
		Root:        root,
		WorkDir:     agentDir,
		CLIVars:     vars,
	}
	var buf bytes.Buffer
	if _, _, _, err := instantiateFormulaWorkflow(params, &buf); err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: "manager"})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	for _, r := range records {
		if r.Event == telemetry.EventInstanceStart {
			return r.FormulaDigest
		}
	}
	t.Fatal("no instance_start record was written; the fixture is not driving a real sling")
	return ""
}

func enableTelemetryForTest(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("enabling telemetry: %v", err)
	}
}

// TestFormulaDigestRecordedAtInstantiation is the AC-1(viii) byte-identity anchor: two runs can be
// compared as "the same formula" from the record log alone, without anyone still having the file.
//
// The test passes --var deliberately. Without it the expanded formula and the raw file would hash
// to nearly the same thing for the wrong reason — no substitution happened — and the assertion
// that distinguishes the two derivations would be vacuous.
func TestFormulaDigestRecordedAtInstantiation(t *testing.T) {
	t.Setenv("AF_ACTOR", "manager")
	root, agentDir := createTestFormulaFactoryWithTOML(t, "digestfx", "manager", digestFormulaTOML)
	enableTelemetryForTest(t, root)

	digest := slingWithVars(t, root, agentDir, "digestfx", []string{"issue=bd-42"})

	if len(digest) != 64 {
		t.Fatalf("formula_digest = %q (%d chars), want 64 lowercase hex", digest, len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil || digest != strings.ToLower(digest) {
		t.Fatalf("formula_digest = %q, want lowercase hex", digest)
	}

	raw, err := os.ReadFile(filepath.Join(config.FormulasDir(root), "digestfx.formula.toml"))
	if err != nil {
		t.Fatalf("reading the formula back: %v", err)
	}
	sum := sha256.Sum256(raw)
	if want := hex.EncodeToString(sum[:]); digest != want {
		t.Errorf("formula_digest = %s, want sha256 of the file's raw bytes %s", digest, want)
	}

	// The failure this guards: hashing the parsed formula AFTER expandStepVars. A run with a
	// different --var would then produce a different digest for a byte-identical file, and the
	// "were these two runs the same formula" question would answer no for every run.
	if strings.Contains(string(raw), "bd-42") {
		t.Fatal("the fixture file already contains the substituted value, so it cannot discriminate")
	}
	expanded := strings.ReplaceAll(string(raw), "{{issue}}", "bd-42")
	expandedSum := sha256.Sum256([]byte(expanded))
	if digest == hex.EncodeToString(expandedSum[:]) {
		t.Error("formula_digest matches the VARIABLE-EXPANDED formula; it must be the raw file's hash")
	}
}

// Two slings of one byte-identical file with different --var values must produce ONE digest. This
// is the property the whole field exists for and the one an expanded-formula hash would break.
func TestFormulaDigestIdenticalAcrossVarValues(t *testing.T) {
	t.Setenv("AF_ACTOR", "manager")

	// Two SEPARATE factories, because a second sling into one factory is refused while the first
	// instance is still active. Separate roots also make the point sharper: the digest is a
	// property of the file's bytes and of nothing about the factory that slung it.
	sling := func(varValue string) string {
		root, agentDir := createTestFormulaFactoryWithTOML(t, "digestfx", "manager", digestFormulaTOML)
		enableTelemetryForTest(t, root)
		return slingWithVars(t, root, agentDir, "digestfx", []string{"issue=" + varValue})
	}

	first, second := sling("bd-42"), sling("bd-77")
	if first == "" || first != second {
		t.Errorf("digests %q and %q differ for one byte-identical formula", first, second)
	}
}

// TestFormulaDigestStaysInsideTheTelemetryGate records the phase's open decision (the outline's
// Gaps #1) as a test rather than as a comment. With telemetry off there is no instance_start
// record AT ALL, so there is nowhere for a digest to be carried; making the capture survive
// telemetry-off would mean inventing a second persisted artifact, which is outside this phase.
// The capture therefore sits INSIDE the vt.enabled gate and a telemetry-off sling pays nothing.
func TestFormulaDigestStaysInsideTheTelemetryGate(t *testing.T) {
	t.Setenv("AF_ACTOR", "manager")
	root, agentDir := createTestFormulaFactoryWithTOML(t, "digestfx", "manager", digestFormulaTOML)
	// telemetry deliberately NOT enabled

	installMemStore(t)
	params := InstantiateParams{
		Ctx: withVerbTelemetry(t.Context(), verbTelemetry{
			verb: "sling", start: time.Now(), enabled: telemetryFactoryEnabled(root),
		}),
		FormulaName: "digestfx",
		AgentName:   "manager",
		Root:        root,
		WorkDir:     agentDir,
	}
	var buf bytes.Buffer
	if _, _, _, err := instantiateFormulaWorkflow(params, &buf); err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: "manager"})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("telemetry is off but %d records were written", len(records))
	}
}
