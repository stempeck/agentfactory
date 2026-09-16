package cmd

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// setupTokenomicsFactory creates the minimal layout resolveInvokerRoot needs and chdirs into it.
// It mirrors setupTestFactoryForFidelity rather than sharing it: this suite seeds three gate files
// the fidelity suite knows nothing about, and a shared fixture that grew them would change what
// the fidelity tests are standing on.
func setupTokenomicsFactory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	t.Chdir(dir)
	return dir
}

// asAgent is the inverse of memory_nag_test.go's asOperator, which this file reuses. Both signals
// are set explicitly for the reason that helper documents: callerAuthority is fail-closed on
// ABSENCE, so an ambient AF_ROLE or $TMUX would decide the tier instead of the test.
func asAgent(t *testing.T) {
	t.Helper()
	t.Setenv("AF_ROLE", "solver")
	t.Setenv("TMUX", "")
}

func writeGate(t *testing.T, path, state string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(state+"\n"), 0o644); err != nil {
		t.Fatalf("write gate %s: %v", path, err)
	}
}

// lightTheChain switches on every gate the tokenomics self-test consults, so a test that wants a
// dark chain darkens exactly one of them and the reason list names exactly that one.
func lightTheChain(t *testing.T, root string) {
	t.Helper()
	writeGate(t, telemetryGateFile(root), "on")
	writeGate(t, statuslineGateFile(root), "on")
	writeGate(t, tokenomicsGateFile(root), "on")
}

// writeStartupTokenomics writes a startup.json carrying only a tokenomics block. Only that key is
// written because the loader fills every absent key with its shipped default, so a fixture that
// spelled the whole file out would pin defaults this suite has no business pinning.
func writeStartupTokenomics(t *testing.T, root, block string) {
	t.Helper()
	doc := `{"tokenomics":` + block + "}\n"
	if err := os.WriteFile(config.StartupConfigPath(root), []byte(doc), 0o644); err != nil {
		t.Fatalf("write startup.json: %v", err)
	}
	// A block the loader rejects would reach the status surface as "the startup config could not
	// be read", which passes an assertion about inertness for entirely the wrong reason.
	if _, err := config.LoadStartupConfig(root); err != nil {
		t.Fatalf("the fixture %s is not a config this binary accepts: %v", block, err)
	}
}

// tokenomicsProvenanceLines returns the non-empty lines of the toggle audit log (nil when absent).
func tokenomicsProvenanceLines(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(tokenomicsGateLogFile(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read .tokenomics.log: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// runTokenomicsArgs drives the real cobra RunE with the package-level command, so flag wiring is
// exercised rather than bypassed. Stdout is captured because every emit in this verb goes to
// os.Stdout directly (see the seam note on emitTokenomicsJSONDocument).
func runTokenomicsArgs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		runErr = runTokenomics(tokenomicsCmd, args)
	})
	return out, runErr
}

// enableTokenomicsJSON sets --json for one test and guarantees it is cleared afterwards.
// tokenomicsCmd is a package-level singleton and a leaked flag would silently reroute every later
// test in this package through the JSON path — the hazard enableTelemetryJSON documents.
func enableTokenomicsJSON(t *testing.T) {
	t.Helper()
	if err := tokenomicsCmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set --json: %v", err)
	}
	t.Cleanup(func() {
		if err := tokenomicsCmd.Flags().Set("json", "false"); err != nil {
			t.Fatalf("restore --json: %v", err)
		}
	})
}

// ---------------------------------------------------------------- AC-1

// TestTokenomicsOffRefusal is AC-1. The grammar it asserts is fidelity's, not teardown's:
// authority_test.go:13-21 enumerates the teardown-refusal surfaces as a closed set, and a gate
// toggle that borrowed "stops the whole factory ... would kill YOU" would both be false and add a
// surface to that set.
func TestTokenomicsOffRefusal(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	before, err := os.ReadFile(tokenomicsGateFile(root))
	if err != nil {
		t.Fatalf("read seeded toggle: %v", err)
	}
	asAgent(t)

	out, err := runTokenomicsArgs(t, "off")
	if err == nil {
		t.Fatal("af tokenomics off in agent context returned nil; cobra would exit 0 and the gate would be off")
	}
	if out != "" {
		t.Errorf("a refused toggle wrote to stdout: %q", out)
	}
	refusal := err.Error()

	t.Run("the refusal follows the fidelity grammar", func(t *testing.T) {
		for _, want := range []string{
			"af tokenomics off",
			"operator action",
			"Do NOT retry and do NOT disable it another way",
			"af mail send manager",
		} {
			if !strings.Contains(refusal, want) {
				t.Errorf("refusal does not contain %q:\n%s", want, refusal)
			}
		}
	})

	t.Run("the refusal never names the detection mechanism", func(t *testing.T) {
		// Handing the agent the signal it was caught by hands it the bypass.
		for _, banned := range []string{"AF_ROLE", "TMUX", "tmux", "display-message", "#S", "$TMUX"} {
			if strings.Contains(refusal, banned) {
				t.Errorf("refusal leaks the detection mechanism %q:\n%s", banned, refusal)
			}
		}
	})

	t.Run("the refusal does not borrow the teardown claims", func(t *testing.T) {
		for _, banned := range []string{"stops the whole factory", "kill YOU", "every sibling agent"} {
			if strings.Contains(refusal, banned) {
				t.Errorf("refusal reuses the teardown claim %q, which is false for a gate toggle:\n%s", banned, refusal)
			}
		}
	})

	t.Run("the refused write left no trace", func(t *testing.T) {
		after, err := os.ReadFile(tokenomicsGateFile(root))
		if err != nil {
			t.Fatalf("read toggle after refusal: %v", err)
		}
		if string(after) != string(before) {
			t.Errorf("toggle changed under a refusal: %q -> %q", before, after)
		}
		if lines := tokenomicsProvenanceLines(t, root); len(lines) != 0 {
			t.Errorf("a refused toggle wrote %d provenance lines: %v", len(lines), lines)
		}
	})
}

// TestTokenomicsOffOperatorWritesAndRecords is the other half of AC-1: the refusal is only
// meaningful if the operator path actually writes. Without this row the whole gate is satisfiable
// by a verb that refuses everyone.
func TestTokenomicsOffOperatorWritesAndRecords(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	asOperator(t)

	if _, err := runTokenomicsArgs(t, "off"); err != nil {
		t.Fatalf("af tokenomics off as operator: %v", err)
	}

	data, err := os.ReadFile(tokenomicsGateFile(root))
	if err != nil {
		t.Fatalf("read toggle: %v", err)
	}
	if string(data) != "off\n" {
		t.Errorf("toggle = %q, want %q", data, "off\n")
	}
	// Provenance never goes into the toggle file itself: every reader of a gate file in this
	// package compares the trimmed contents against "on", so an extra byte is a silent disable.
	if strings.Contains(string(data), " ") {
		t.Errorf("toggle file carries provenance-shaped content: %q", data)
	}

	lines := tokenomicsProvenanceLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 provenance line, got %d: %v", len(lines), lines)
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 4 {
		t.Fatalf("provenance line must be `ts actor source state`, got %q", lines[0])
	}
	if fields[1] != "operator" {
		t.Errorf("actor = %q, want %q", fields[1], "operator")
	}
	if fields[2] != tokenomicsSourceCLI {
		t.Errorf("source = %q, want %q", fields[2], tokenomicsSourceCLI)
	}
	if fields[3] != "off" {
		t.Errorf("state = %q, want %q", fields[3], "off")
	}
}

// TestTokenomicsOnIsNeverGated pins the fail-TOWARD-oversight asymmetry fidelity established:
// re-enabling the policy surface is not an operator-only action, because refusing it would leave
// an agent that had somehow reached a disabled factory unable to restore oversight.
func TestTokenomicsOnIsNeverGated(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	writeGate(t, tokenomicsGateFile(root), "off")
	asAgent(t)

	if _, err := runTokenomicsArgs(t, "on"); err != nil {
		t.Fatalf("af tokenomics on in agent context: %v", err)
	}
	data, err := os.ReadFile(tokenomicsGateFile(root))
	if err != nil {
		t.Fatalf("read toggle: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("toggle = %q, want %q", data, "on\n")
	}
}

// TestTokenomicsOnWarnsWhenTelemetryIsOff pins the stream, not just the text. The warning has to
// be on stderr for the same mechanical reason improvement.go:373-381 and warnFidelityProvenanceLost
// give: this package's status assertions read stdout, and one of them fails on the substring
// "WARNING". A warning printed to stdout would break tests that have nothing to do with it.
func TestTokenomicsOnWarnsWhenTelemetryIsOff(t *testing.T) {
	root := setupTokenomicsFactory(t)
	writeGate(t, statuslineGateFile(root), "on")
	writeGate(t, telemetryGateFile(root), "off")
	asOperator(t)

	var stdout string
	stderr := captureStderr(t, func() {
		var err error
		stdout, err = runTokenomicsArgs(t, "on")
		if err != nil {
			t.Errorf("af tokenomics on: %v", err)
		}
	})

	// Advisory only: it never blocks the write the operator asked for.
	if data, err := os.ReadFile(tokenomicsGateFile(root)); err != nil || string(data) != "on\n" {
		t.Errorf("toggle = %q (err %v), want %q — the warning must not block the write", data, err, "on\n")
	}
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("stderr carries no lowercase warning:\n%s", stderr)
	}
	if !strings.Contains(stderr, "telemetry") {
		t.Errorf("the warning does not name telemetry, so it does not say what to fix:\n%s", stderr)
	}
	if strings.Contains(stdout, "WARNING") {
		t.Errorf("an uppercase WARNING reached stdout, the exact collision warnFidelityProvenanceLost avoids:\n%s", stdout)
	}

	t.Run("a lit chain warns about nothing", func(t *testing.T) {
		writeGate(t, telemetryGateFile(root), "on")
		quiet := captureStderr(t, func() {
			if _, err := runTokenomicsArgs(t, "on"); err != nil {
				t.Errorf("af tokenomics on: %v", err)
			}
		})
		if strings.Contains(quiet, "warning:") {
			t.Errorf("telemetry is on and the verb still warned:\n%s", quiet)
		}
	})

	// Every dark leg is named, not the first. The status self-test treats the two gates as equal
	// members of one chain, so an operator told only about telemetry would fix it, switch the
	// surface on again and still be inert — two round trips to learn one thing.
	t.Run("the statusline is the other leg of the same chain", func(t *testing.T) {
		writeGate(t, telemetryGateFile(root), "on")
		writeGate(t, statuslineGateFile(root), "off")
		only := captureStderr(t, func() {
			if _, err := runTokenomicsArgs(t, "on"); err != nil {
				t.Errorf("af tokenomics on: %v", err)
			}
		})
		if !strings.Contains(only, "statusline") {
			t.Errorf("the statusline gate is off and the toggle warned about nothing:\n%s", only)
		}

		writeGate(t, telemetryGateFile(root), "off")
		both := captureStderr(t, func() {
			if _, err := runTokenomicsArgs(t, "on"); err != nil {
				t.Errorf("af tokenomics on: %v", err)
			}
		})
		for _, want := range []string{"telemetry", "statusline"} {
			if !strings.Contains(both, want) {
				t.Errorf("both gates are off and the warning does not name %q:\n%s", want, both)
			}
		}
	})
}

// TestTokenomicsProvenanceNamesTheAgentActor is the other half of the audit log's one interesting
// question. Every other test writes as an operator, and "operator" is the branch a stub returning
// a constant would also produce — so without this row the actor field is unpinned in the only
// direction where getting it wrong matters. `on` is the vehicle because it is the write that is
// never gated, which is precisely why an agent can reach it.
func TestTokenomicsProvenanceNamesTheAgentActor(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	writeGate(t, tokenomicsGateFile(root), "off")
	asAgent(t)

	if _, err := runTokenomicsArgs(t, "on"); err != nil {
		t.Fatalf("af tokenomics on in agent context: %v", err)
	}
	lines := tokenomicsProvenanceLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 provenance line, got %d: %v", len(lines), lines)
	}
	actor := strings.Fields(lines[0])[1]
	if actor == "operator" {
		t.Errorf("an agent-context write was recorded as %q; the log cannot answer who turned it back on", actor)
	}
	if !strings.HasPrefix(actor, "agent") {
		t.Errorf("actor = %q, want an agent identity", actor)
	}
}

// ---------------------------------------------------------------- AC-3

// tokenomicsStatusDoc mirrors the production DTO rather than importing it, for the reason
// telemetryStateJSON gives about its own mirrors: the payload shape is a contract with a consumer,
// and a test that unmarshalled into the production struct would follow every rename silently.
type tokenomicsStatusDoc struct {
	V       int    `json:"v"`
	State   string `json:"state"`
	Enabled bool   `json:"enabled"`
	Policy  struct {
		Umbrella   string `json:"umbrella"`
		Mechanisms []struct {
			Name string `json:"name"`
			On   bool   `json:"on"`
		} `json:"mechanisms"`
		AdmissionMarginPct int    `json:"admission_margin_pct"`
		LearnedMinRuns     int    `json:"learned_min_runs"`
		UnavailableBecause string `json:"unavailable_because"`
	} `json:"policy"`
	Window struct {
		Tokens int64  `json:"tokens"`
		Source string `json:"source"`
	} `json:"window"`
	InertBecause    []string `json:"inert_because"`
	LearnedCoverage struct {
		Aggregates         int    `json:"aggregates"`
		UnavailableBecause string `json:"unavailable_because"`
	} `json:"learned_coverage"`
	RecentInterventions struct {
		Events             []string `json:"events"`
		UnavailableBecause string   `json:"unavailable_because"`
	} `json:"recent_interventions"`
	Provenance []string `json:"provenance"`
	Contract   string   `json:"contract"`
	Error      string   `json:"error"`
}

// statusJSON runs `af tokenomics status --json`, asserts the always-exit-0 contract, and returns
// both the parsed document and the raw keys so presence can be checked independently of type.
func statusJSON(t *testing.T) (tokenomicsStatusDoc, map[string]json.RawMessage) {
	t.Helper()
	out, err := runTokenomicsArgs(t, "status")
	if err != nil {
		t.Fatalf("status --json returned %v; this surface always exits 0 and a consumer branches on .state", err)
	}
	line := strings.TrimSpace(out)
	if line == "" {
		t.Fatal("status --json wrote nothing to os.Stdout — the emit is going through the cobra seam, " +
			"which resolves to the ROOT command's writer and lands in another test's stale buffer during a full-package run")
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("status --json emitted %d lines; a consumer reads one document:\n%s", strings.Count(line, "\n")+1, out)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("status --json is not parseable JSON: %v\n%s", err, line)
	}
	var doc tokenomicsStatusDoc
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		t.Fatalf("status --json does not match the documented shape: %v\n%s", err, line)
	}
	return doc, raw
}

// TestTokenomicsStatusJSON is AC-3. It follows the telemetry_json.go idiom rather than af step
// current's: --json is a real flag, `state` rides on EVERY payload including success, and no field
// is omitempty — degradation is always a difference in VALUE, never a difference in shape.
func TestTokenomicsStatusJSON(t *testing.T) {
	// Resolved through findModuleRoot rather than relative to the working directory, because
	// setupTokenomicsFactory chdirs into a tempdir below.
	source := readPackageSource(t, "tokenomics.go")
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	asOperator(t)
	enableTokenomicsJSON(t)

	doc, raw := statusJSON(t)

	t.Run("the envelope is well formed on the success path", func(t *testing.T) {
		if doc.V != tokenomicsSchemaVersion {
			t.Errorf("v = %d, want %d", doc.V, tokenomicsSchemaVersion)
		}
		if doc.State != tokenomicsStateOK {
			t.Errorf("state = %q on a fully lit chain, want %q (inert_because=%v)", doc.State, tokenomicsStateOK, doc.InertBecause)
		}
		if !doc.Enabled {
			t.Error("enabled = false with the toggle written on")
		}
	})

	t.Run("every documented key is present in every state", func(t *testing.T) {
		for _, k := range []string{
			"v", "state", "enabled", "policy", "window",
			"inert_because", "learned_coverage", "recent_interventions", "provenance", "contract",
		} {
			if _, ok := raw[k]; !ok {
				t.Errorf("payload has no %q key", k)
			}
		}
	})

	t.Run("no list-typed field marshals to null", func(t *testing.T) {
		// A consumer ranges over these. A null is a different shape from an empty list, and the
		// whole point of the no-omitempty rule is that the shape never varies with the state.
		if string(raw["inert_because"]) == "null" {
			t.Error(`"inert_because" marshalled to null`)
		}
		if doc.InertBecause == nil {
			t.Error("inert_because decoded to nil on a healthy chain, want an empty list")
		}
		if len(doc.Policy.Mechanisms) == 0 {
			t.Error("policy.mechanisms is empty; the payload names no mechanism vocabulary")
		}
		if doc.RecentInterventions.Events == nil {
			t.Error("recent_interventions.events decoded to nil, want an empty list")
		}
		// The untouched factory is where a nil provenance slice would surface, and it is the
		// default state of every new factory — so this is the state that has to be asserted, not
		// the post-toggle one where the slice is populated anyway.
		if string(raw["provenance"]) == "null" {
			t.Error(`"provenance" marshalled to null on a factory whose toggle has never been moved`)
		}
		if doc.Provenance == nil {
			t.Error("provenance decoded to nil with no audit log, want an empty list")
		}
	})

	t.Run("a lit chain reports no inert reasons", func(t *testing.T) {
		if len(doc.InertBecause) != 0 {
			t.Errorf("inert_because = %v on a fully lit chain, want empty", doc.InertBecause)
		}
	})

	t.Run("no field in the payload is omitempty", func(t *testing.T) {
		// Asserted structurally because it cannot be asserted behaviourally: an omitempty field
		// that happens to be non-zero in every fixture is invisible until a consumer meets the
		// state that zeroes it.
		//
		// Parsed rather than grepped, following the AST-scan idiom the other guards in this
		// package use. A substring scan reports this file's own explanation of the rule as a
		// violation of it, which is a guard that fires on the documentation and not on the code.
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "tokenomics.go", source, 0)
		if err != nil {
			t.Fatalf("parse tokenomics.go: %v", err)
		}
		tagged := 0
		ast.Inspect(file, func(n ast.Node) bool {
			f, ok := n.(*ast.Field)
			if !ok || f.Tag == nil {
				return true
			}
			tagged++
			if strings.Contains(f.Tag.Value, "omitempty") {
				t.Errorf("%s: tag %s is omitempty; degradation must be a difference in VALUE, "+
					"never in the key set, or a consumer cannot write one parser for the payload",
					fset.Position(f.Pos()), f.Tag.Value)
			}
			return true
		})
		if tagged == 0 {
			t.Fatal("found no struct tags in tokenomics.go; the guard proves nothing")
		}
	})
}

// TestTokenomicsStatusJSONDarkChain is the "say why, do not print zeros" half. Both causes must be
// named when both are present: a reader that stopped at the first would send an operator to fix
// telemetry and leave them dark on statusline.
func TestTokenomicsStatusJSONDarkChain(t *testing.T) {
	root := setupTokenomicsFactory(t)
	asOperator(t)
	enableTokenomicsJSON(t)

	for _, tc := range []struct {
		name          string
		telemetry     string
		statusline    string
		wantMentioned []string
		wantAbsent    []string
	}{
		{"both dark", "off", "off", []string{"telemetry", "statusline"}, nil},
		{"only telemetry dark", "off", "on", []string{"telemetry"}, []string{"statusline"}},
		{"only statusline dark", "on", "off", []string{"statusline"}, []string{"telemetry"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeGate(t, telemetryGateFile(root), tc.telemetry)
			writeGate(t, statuslineGateFile(root), tc.statusline)
			writeGate(t, tokenomicsGateFile(root), "on")

			doc, _ := statusJSON(t)
			joined := strings.Join(doc.InertBecause, " | ")
			for _, want := range tc.wantMentioned {
				if !strings.Contains(joined, want) {
					t.Errorf("inert_because does not name %q: %v", want, doc.InertBecause)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(joined, absent) {
					t.Errorf("inert_because names %q, which is on: %v", absent, doc.InertBecause)
				}
			}
			if doc.State == tokenomicsStateOK {
				t.Errorf("state = %q with %d inert reasons; a dark chain is not ok", doc.State, len(doc.InertBecause))
			}
		})
	}

	t.Run("the umbrella toggle is itself an inert reason", func(t *testing.T) {
		writeGate(t, telemetryGateFile(root), "on")
		writeGate(t, statuslineGateFile(root), "on")
		writeGate(t, tokenomicsGateFile(root), "off")

		doc, _ := statusJSON(t)
		if !strings.Contains(strings.Join(doc.InertBecause, " | "), "tokenomics") {
			t.Errorf("the toggle is off and inert_because does not say so: %v", doc.InertBecause)
		}
		if doc.Enabled {
			t.Error("enabled = true with the toggle written off")
		}
	})
}

// TestTokenomicsStatusJSONNamesWindowAndInputs pins the arithmetic half of the self-test: the
// denominator, WHERE it came from, and the two operands that decide admission. A bare 200000 is
// indistinguishable from a measurement, which is exactly why config.ResolveContextWindow returns a
// source alongside it.
func TestTokenomicsStatusJSONNamesWindowAndInputs(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	asOperator(t)
	enableTokenomicsJSON(t)

	t.Run("an undeclared window is surfaced as a fallback", func(t *testing.T) {
		doc, _ := statusJSON(t)
		wantTokens, wantSource := config.ResolveContextWindow(nil, 0)
		if doc.Window.Tokens != wantTokens {
			t.Errorf("window.tokens = %d, want %d", doc.Window.Tokens, wantTokens)
		}
		if doc.Window.Source != wantSource {
			t.Errorf("window.source = %q, want %q — an assumed denominator must not read as a measured one",
				doc.Window.Source, wantSource)
		}
		if doc.Window.Source != config.WindowSourceFallback {
			t.Errorf("window.source = %q, want %q", doc.Window.Source, config.WindowSourceFallback)
		}
	})

	t.Run("a declared window is surfaced as declared", func(t *testing.T) {
		models := `{"default":"big","models":{"big":{"` + config.EnvMaxContextTokens + `":"1000000"}}}`
		if err := os.WriteFile(config.ModelsConfigPath(root), []byte(models), 0o644); err != nil {
			t.Fatalf("write models.json: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(config.ModelsConfigPath(root)) })

		doc, _ := statusJSON(t)
		if doc.Window.Tokens != 1_000_000 {
			t.Errorf("window.tokens = %d, want 1000000", doc.Window.Tokens)
		}
		if doc.Window.Source != config.WindowSourceDeclared {
			t.Errorf("window.source = %q, want %q", doc.Window.Source, config.WindowSourceDeclared)
		}
	})

	t.Run("the arithmetic inputs are named", func(t *testing.T) {
		doc, _ := statusJSON(t)
		// The shipped defaults, read through the loader rather than re-typed, so the assertion
		// tracks a default change instead of pinning a stale copy of it.
		cfg, err := config.LoadStartupConfig(root)
		if err != nil {
			t.Fatalf("LoadStartupConfig: %v", err)
		}
		if doc.Policy.AdmissionMarginPct != cfg.Tokenomics.AdmissionMarginPct {
			t.Errorf("policy.admission_margin_pct = %d, want %d",
				doc.Policy.AdmissionMarginPct, cfg.Tokenomics.AdmissionMarginPct)
		}
		if doc.Policy.LearnedMinRuns != cfg.Tokenomics.LearnedMinRuns {
			t.Errorf("policy.learned_min_runs = %d, want %d",
				doc.Policy.LearnedMinRuns, cfg.Tokenomics.LearnedMinRuns)
		}
	})

	t.Run("the behavior contract is pointed at", func(t *testing.T) {
		doc, _ := statusJSON(t)
		if strings.TrimSpace(doc.Contract) == "" {
			t.Fatal("contract is empty; status names no behavior-contract section")
		}
		if !strings.Contains(doc.Contract, "USING_TOKENOMICS.md") {
			t.Errorf("contract %q does not name the document that carries the section", doc.Contract)
		}
	})
}

// TestTokenomicsStatusJSONHonestAboutMissingSources is Gotcha 12 stated as behavior, and #668 Phase
// 6 changed what makes it true without changing what it asserts. Both readers are now wired — the
// digest directory and the intervention records — so the zeroes below are measured rather than
// stubbed, and the reasons beside them say COLD START rather than "a later phase builds this". The
// assertion is the same either way and is the point: a zero with no reason beside it reads as
// measured-and-empty, which is the one answer worse than "nothing here yet".
func TestTokenomicsStatusJSONHonestAboutMissingSources(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	asOperator(t)
	enableTokenomicsJSON(t)

	doc, _ := statusJSON(t)
	if doc.LearnedCoverage.Aggregates != 0 {
		t.Errorf("learned_coverage.aggregates = %d with no digest on disk", doc.LearnedCoverage.Aggregates)
	}
	if strings.TrimSpace(doc.LearnedCoverage.UnavailableBecause) == "" {
		t.Error("learned_coverage reports zero with no reason, which reads as `measured and empty`")
	}
	if len(doc.RecentInterventions.Events) != 0 {
		t.Errorf("recent_interventions.events = %v with no mechanism able to emit one", doc.RecentInterventions.Events)
	}
	if strings.TrimSpace(doc.RecentInterventions.UnavailableBecause) == "" {
		t.Error("recent_interventions reports an empty list with no reason")
	}
}

// TestTokenomicsStatusJSONUnresolvableRoot is the reason --json is read BEFORE anything can fail.
// A consumer that got a non-zero exit and an empty stdout here could not tell a broken factory
// from a broken binary.
func TestTokenomicsStatusJSONUnresolvableRoot(t *testing.T) {
	markerless, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	t.Chdir(markerless)
	asOperator(t)
	enableTokenomicsJSON(t)

	out, runErr := runTokenomicsArgs(t, "status")
	if runErr != nil {
		t.Fatalf("status --json outside a factory returned %v, want nil", runErr)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		t.Fatalf("status --json outside a factory is not parseable JSON: %v\n%s", err, out)
	}
	for _, k := range []string{"v", "state", "error"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("error payload has no %q key: %s", k, out)
		}
	}
	var doc tokenomicsStatusDoc
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.State != tokenomicsStateError {
		t.Errorf("state = %q, want %q", doc.State, tokenomicsStateError)
	}
	if doc.V != tokenomicsSchemaVersion {
		t.Errorf("v = %d, want %d", doc.V, tokenomicsSchemaVersion)
	}
	if strings.TrimSpace(doc.Error) == "" {
		t.Error("state is error and the payload says nothing about what failed")
	}
}

// TestTokenomicsStatusHumanPathUnchanged guards the regression registering a --json flag invites:
// a flag read in the wrong place turns the default human surface into JSON for every operator who
// never asked for it.
func TestTokenomicsStatusHumanPathUnchanged(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	writeGate(t, telemetryGateFile(root), "off")
	asOperator(t)

	out, err := runTokenomicsArgs(t, "status")
	if err != nil {
		t.Fatalf("af tokenomics status: %v", err)
	}
	if json.Valid([]byte(strings.TrimSpace(out))) && strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("the human path emitted JSON:\n%s", out)
	}

	// The first line is the stable grep contract every gate verb in this package opens with,
	// printed before anything downstream can fail (statusline.go:299-305).
	first := strings.SplitN(out, "\n", 2)[0]
	if first != "tokenomics: on" {
		t.Errorf("first line = %q, want %q", first, "tokenomics: on")
	}

	t.Run("the human self-test names its inert cause", func(t *testing.T) {
		if !strings.Contains(out, "inert because") {
			t.Errorf("a dark chain printed no `inert because` verdict:\n%s", out)
		}
		if !strings.Contains(out, "telemetry") {
			t.Errorf("the inert verdict does not name telemetry:\n%s", out)
		}
	})

	t.Run("the human self-test names the window and its source", func(t *testing.T) {
		wantTokens, wantSource := config.ResolveContextWindow(nil, 0)
		if !strings.Contains(out, itoaWindow(wantTokens)) {
			t.Errorf("status does not print the resolved window %d:\n%s", wantTokens, out)
		}
		if !strings.Contains(out, wantSource) {
			t.Errorf("status prints a bare denominator with no source %q:\n%s", wantSource, out)
		}
	})

	t.Run("the human self-test names the arithmetic inputs and the contract", func(t *testing.T) {
		for _, want := range []string{"admission margin", "learned min runs", "USING_TOKENOMICS.md"} {
			if !strings.Contains(out, want) {
				t.Errorf("status does not mention %q:\n%s", want, out)
			}
		}
	})
}

// TestTokenomicsStatusReportsAConfigDarkChain covers the half of the chain that lives in
// startup.json rather than in a gate file. The three gate files are only the outer layer: the
// umbrella's OTHER input is the tokenomics.enabled enum, and underneath that sit six per-mechanism
// enums. A status that watched only the gate files would report `self-test: live` and state ok for
// a factory in which nothing can ever fire, which is precisely the "prints zeros for a chain that
// is simply dark" failure the surface exists to prevent — one config layer further down.
func TestTokenomicsStatusReportsAConfigDarkChain(t *testing.T) {
	allOff := `"budget":"off","thrift":"off","dispatch":"off","interview":"off","effort":"off","escalate":"off"`

	for _, tc := range []struct {
		name          string
		block         string
		wantMentioned []string
		wantAbsent    []string
		wantLive      bool
	}{
		{
			name:     "a lit chain with a live config really is live",
			block:    `{"enabled":"on"}`,
			wantLive: true,
		},
		{
			// Escalate is the one mechanism whose "default" resolves OFF, so it is off in every
			// other row here — and an operator who turns on ONLY escalate is exactly the operator
			// most likely to run status to check it took. Without this row, a mechanism scan that
			// skipped escalate would call this live factory dark, which is Gotcha 3 inverted.
			name:     "a chain lit only by escalate is live",
			block:    `{"enabled":"on","budget":"off","thrift":"off","dispatch":"off","interview":"off","effort":"off","escalate":"on"}`,
			wantLive: true,
		},
		{
			name:          "the enum vetoes the toggle",
			block:         `{"enabled":"off"}`,
			wantMentioned: []string{"startup.json", "enabled=off"},
			// The general-form reason must NOT also fire: it would be true but redundant, and a
			// reason list that restates one cause twice reads as two things to fix.
			wantAbsent: []string{"nothing underneath"},
		},
		{
			name:          "an umbrella with nothing underneath it",
			block:         `{"enabled":"on",` + allOff + `}`,
			wantMentioned: []string{"every mechanism", "nothing underneath"},
			wantAbsent:    []string{"enabled=off"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := setupTokenomicsFactory(t)
			lightTheChain(t, root)
			writeStartupTokenomics(t, root, tc.block)
			asOperator(t)

			human, err := runTokenomicsArgs(t, "status")
			if err != nil {
				t.Fatalf("af tokenomics status: %v", err)
			}

			enableTokenomicsJSON(t)
			doc, _ := statusJSON(t)
			joined := strings.Join(doc.InertBecause, " | ")

			if tc.wantLive {
				if doc.State != tokenomicsStateOK {
					t.Errorf("state = %q with every gate on and the config live, want %q (inert_because=%v)",
						doc.State, tokenomicsStateOK, doc.InertBecause)
				}
				if len(doc.InertBecause) != 0 {
					t.Errorf("a live chain was given %d reasons to be inert: %v", len(doc.InertBecause), doc.InertBecause)
				}
				if !strings.Contains(human, "self-test: live") {
					t.Errorf("the control row does not report a live self-test, so the dark rows below prove nothing:\n%s", human)
				}
				return
			}

			if doc.State == tokenomicsStateOK {
				t.Errorf("state = %q for a config-dark chain; every gate file is on but no mechanism can fire", doc.State)
			}
			if strings.Contains(human, "self-test: live") {
				t.Errorf("the human surface calls itself live while the config silences every mechanism:\n%s", human)
			}
			for _, want := range tc.wantMentioned {
				if !strings.Contains(joined, want) {
					t.Errorf("inert_because does not mention %q, so the operator is not told what to edit: %v", want, doc.InertBecause)
				}
				if !strings.Contains(human, want) {
					t.Errorf("the human self-test does not mention %q:\n%s", want, human)
				}
			}
			// Each cause is named ONCE. Every assertion above is a presence check, and a reason
			// list can satisfy all of them while also carrying a reason that is false or that
			// restates a cause already given — which sends an operator to edit a second thing.
			for _, absent := range tc.wantAbsent {
				if strings.Contains(joined, absent) {
					t.Errorf("inert_because also says %q, which is not a separate cause here: %v", absent, doc.InertBecause)
				}
			}
			if len(doc.InertBecause) != 1 {
				t.Errorf("a chain with exactly one dark layer was given %d reasons: %v", len(doc.InertBecause), doc.InertBecause)
			}
			// The reason must be matched by the payload it explains: a reason list that named a
			// dead umbrella while the mechanism list showed mechanisms on would be two surfaces
			// disagreeing about one fact.
			for _, m := range doc.Policy.Mechanisms {
				if m.On {
					t.Errorf("mechanism %q reports on while the surface reports itself inert", m.Name)
				}
			}
		})
	}
}

// TestTokenomicsStatusUnreadableConfig is the one input to this surface that can fail to load, and
// therefore the one place the "never fill a missing source with a guess" rule is easiest to break.
// A zero config resolves to margin 0%, min runs 1 and five mechanisms on — numbers no factory will
// ever run under, since a factory whose startup.json does not load will not launch at all — so
// printing them would be inventing an operator's policy out of a parse error.
func TestTokenomicsStatusUnreadableConfig(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	asOperator(t)

	// A value the loader REJECTS rather than malformed JSON, because that is the realistic way an
	// operator reaches this state, and because it proves the surface reacts to the load failing
	// rather than to the file being absent.
	bad := `{"tokenomics":{"admission_margin_pct":250}}` + "\n"
	if err := os.WriteFile(config.StartupConfigPath(root), []byte(bad), 0o644); err != nil {
		t.Fatalf("write startup.json: %v", err)
	}
	if _, err := config.LoadStartupConfig(root); err == nil {
		t.Fatal("the fixture loads cleanly, so this test never enters the state it is written for")
	}

	human, err := runTokenomicsArgs(t, "status")
	if err != nil {
		t.Fatalf("af tokenomics status: %v", err)
	}

	t.Run("the human surface reports no policy it cannot read", func(t *testing.T) {
		for _, banned := range []string{
			"admission margin: 0%",
			"learned min runs: 1",
			"budget=on",
			"tokenomics.enabled=default",
		} {
			if strings.Contains(human, banned) {
				t.Errorf("status printed %q, which is the zero config's resolution and not any value this factory runs under:\n%s", banned, human)
			}
		}
		if !strings.Contains(human, "could not be read") {
			t.Errorf("status does not say the config failed to load:\n%s", human)
		}
		// The window comes from models.json, not from the block that failed, so withholding it
		// would be its own dishonesty.
		wantTokens, _ := config.ResolveContextWindow(nil, 0)
		if !strings.Contains(human, itoaWindow(wantTokens)) {
			t.Errorf("status withheld the resolved window %d, which does not depend on startup.json:\n%s", wantTokens, human)
		}
	})

	enableTokenomicsJSON(t)
	doc, raw := statusJSON(t)

	t.Run("the payload marks the policy unavailable rather than zero", func(t *testing.T) {
		if strings.TrimSpace(doc.Policy.UnavailableBecause) == "" {
			t.Error("policy reports numbers with no unavailable_because; a consumer cannot tell a failed load from a configured 0")
		}
		if doc.State == tokenomicsStateOK {
			t.Errorf("state = %q with an unreadable config", doc.State)
		}
		if doc.Policy.Umbrella == "default" {
			t.Error(`policy.umbrella = "default", which is a positive claim about a file the surface just said it could not read`)
		}
		for _, m := range doc.Policy.Mechanisms {
			if m.On {
				t.Errorf("mechanism %q reports on, resolved from a config that did not load", m.Name)
			}
		}
		// The shape must not vary with the state: every key a healthy payload has is still here.
		for _, k := range []string{"v", "state", "enabled", "policy", "window", "inert_because",
			"learned_coverage", "recent_interventions", "provenance", "contract"} {
			if _, ok := raw[k]; !ok {
				t.Errorf("the degraded payload dropped the %q key", k)
			}
		}
	})
}

// TestTokenomicsStatusReadsTheConfiguredKnobs is the inverse of the defaults assertion in
// TestTokenomicsStatusJSONNamesWindowAndInputs: that one reads the shipped values through the same
// loader the code uses, so a surface that hard-coded 10 and 2 would satisfy it. These are values no
// default supplies.
func TestTokenomicsStatusReadsTheConfiguredKnobs(t *testing.T) {
	const (
		wantMargin  = 40
		wantMinRuns = 7
	)
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	writeStartupTokenomics(t, root, `{"enabled":"on","admission_margin_pct":40,"learned_min_runs":7}`)
	asOperator(t)

	human, err := runTokenomicsArgs(t, "status")
	if err != nil {
		t.Fatalf("af tokenomics status: %v", err)
	}
	for _, want := range []string{"40%", "learned min runs: 7", "60% projected occupancy"} {
		if !strings.Contains(human, want) {
			t.Errorf("the human self-test does not report %q:\n%s", want, human)
		}
	}

	enableTokenomicsJSON(t)
	doc, _ := statusJSON(t)
	if doc.Policy.AdmissionMarginPct != wantMargin {
		t.Errorf("policy.admission_margin_pct = %d, want %d", doc.Policy.AdmissionMarginPct, wantMargin)
	}
	if doc.Policy.LearnedMinRuns != wantMinRuns {
		t.Errorf("policy.learned_min_runs = %d, want %d", doc.Policy.LearnedMinRuns, wantMinRuns)
	}
}

// TestTokenomicsStatusNamesEveryMechanism pins the vocabulary and its ORDER on both surfaces. The
// mechanism list is the addressable set an operator edits in startup.json, so a payload that
// silently dropped one would leave a key nobody could discover from the CLI, and a list whose order
// drifted between runs would make two status captures diff for no reason.
func TestTokenomicsStatusNamesEveryMechanism(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	asOperator(t)

	want := tokenomics.Mechanisms()
	if len(want) != 6 {
		t.Fatalf("tokenomics.Mechanisms() has %d entries, want the 6 api.md fixes", len(want))
	}

	human, err := runTokenomicsArgs(t, "status")
	if err != nil {
		t.Fatalf("af tokenomics status: %v", err)
	}
	for _, m := range want {
		if !strings.Contains(human, string(m)+"=") {
			t.Errorf("the human mechanism line does not name %q:\n%s", m, human)
		}
	}

	enableTokenomicsJSON(t)
	doc, _ := statusJSON(t)
	if len(doc.Policy.Mechanisms) != len(want) {
		t.Fatalf("policy.mechanisms has %d entries, want %d: %+v", len(doc.Policy.Mechanisms), len(want), doc.Policy.Mechanisms)
	}
	for i, m := range want {
		if doc.Policy.Mechanisms[i].Name != string(m) {
			t.Errorf("policy.mechanisms[%d].name = %q, want %q", i, doc.Policy.Mechanisms[i].Name, m)
		}
	}
	// The default posture, asserted here because this is the only test that walks the payload's
	// mechanism list by name: escalate is the one mechanism whose "default" resolves off, because
	// it moves work to another backend rather than counselling about the current one.
	for _, m := range doc.Policy.Mechanisms {
		wantOn := m.Name != string(tokenomics.MechanismEscalate)
		if m.On != wantOn {
			t.Errorf("mechanism %q = %v under the shipped defaults, want %v", m.Name, m.On, wantOn)
		}
	}
}

// TestTokenomicsStatusCarriesProvenance is the "who turned it back on" surface. The audit log is
// written by every toggle and read by nothing else in this phase, so without this the log is a file
// the factory writes and never shows anyone.
func TestTokenomicsStatusCarriesProvenance(t *testing.T) {
	root := setupTokenomicsFactory(t)
	lightTheChain(t, root)
	asOperator(t)

	t.Run("an untouched factory says so rather than showing an empty list", func(t *testing.T) {
		human, err := runTokenomicsArgs(t, "status")
		if err != nil {
			t.Fatalf("af tokenomics status: %v", err)
		}
		if !strings.Contains(human, "no toggle has been recorded") {
			t.Errorf("a factory with no audit log printed no explanation:\n%s", human)
		}
	})

	// More writes than the tail renders, so the cap and the ordering are both exercised. The
	// sequence is deliberately NOT an alternation: the log line is `RFC3339 actor source state`
	// with a fixed actor and source at SECOND resolution, so seven toggles inside one test differ
	// only in the state word — and under a period-2 alternation the first five lines are
	// byte-identical to the last five, which would let a reader that took the HEAD of the log pass
	// a test written to pin the tail.
	states := []string{"on", "on", "on", "off", "off", "on", "off"}
	for _, s := range states {
		if _, err := runTokenomicsArgs(t, s); err != nil {
			t.Fatalf("af tokenomics %s: %v", s, err)
		}
	}
	all := tokenomicsProvenanceLines(t, root)
	if len(all) != len(states) {
		t.Fatalf("the audit log has %d lines after %d toggles: %v", len(all), len(states), all)
	}

	human, err := runTokenomicsArgs(t, "status")
	if err != nil {
		t.Fatalf("af tokenomics status: %v", err)
	}
	enableTokenomicsJSON(t)
	doc, raw := statusJSON(t)

	t.Run("the payload carries the tail, oldest first and capped", func(t *testing.T) {
		if _, ok := raw["provenance"]; !ok {
			t.Fatal(`the payload has no "provenance" key`)
		}
		if doc.Provenance == nil {
			t.Fatal("provenance decoded to nil, want a list")
		}
		if len(doc.Provenance) != tokenomicsProvenanceTailLines {
			t.Fatalf("provenance has %d lines, want the %d-line tail: %v",
				len(doc.Provenance), tokenomicsProvenanceTailLines, doc.Provenance)
		}
		wantTail := all[len(all)-tokenomicsProvenanceTailLines:]
		for i := range wantTail {
			if doc.Provenance[i] != wantTail[i] {
				t.Errorf("provenance[%d] = %q, want %q (the tail is the LAST lines, oldest first)",
					i, doc.Provenance[i], wantTail[i])
			}
		}
	})

	t.Run("the human surface shows the same lines", func(t *testing.T) {
		if strings.Contains(human, "no toggle has been recorded") {
			t.Errorf("the audit log has %d lines and status still reports none:\n%s", len(all), human)
		}
		for _, line := range all[len(all)-tokenomicsProvenanceTailLines:] {
			if !strings.Contains(human, line) {
				t.Errorf("the human surface omits provenance line %q:\n%s", line, human)
			}
		}
		// The cap is asserted by COUNTING rather than by asserting the older lines are absent.
		// Seven toggles inside one test land inside one second, and the actor and source are fixed,
		// so an older line is byte-identical to one inside the tail — an absence assertion would be
		// asserting something about the clock's resolution rather than about the tail.
		if !strings.Contains(human, "provenance (last "+itoaWindow(tokenomicsProvenanceTailLines)+"):") {
			t.Errorf("the provenance header does not announce a %d-line tail:\n%s", tokenomicsProvenanceTailLines, human)
		}
		printed := 0
		for _, l := range strings.Split(human, "\n") {
			if strings.HasPrefix(l, "  ") && strings.Contains(l, tokenomicsSourceCLI) {
				printed++
			}
		}
		if printed != tokenomicsProvenanceTailLines {
			t.Errorf("the human surface printed %d provenance lines after %d toggles, want the %d-line tail:\n%s",
				printed, len(all), tokenomicsProvenanceTailLines, human)
		}
	})
}

// TestTokenomicsUsage pins the closed arg vocabulary: an unrecognised subcommand is an error
// rather than a silent no-op, matching runFidelity's default branch.
func TestTokenomicsUsage(t *testing.T) {
	setupTokenomicsFactory(t)
	asOperator(t)

	if _, err := runTokenomicsArgs(t, "enable"); err == nil {
		t.Error("af tokenomics enable returned nil; an unknown subcommand must not be a silent no-op")
	}

	// The arity half of the same vocabulary, which every other test in this file bypasses by
	// calling RunE directly. `af tokenomics off status` must not silently toggle and then ignore
	// the rest — cobra's Args validator is the only thing standing between that and an operator
	// who mistyped.
	t.Run("a second argument is rejected", func(t *testing.T) {
		if tokenomicsCmd.Args == nil {
			t.Fatal("tokenomics registers no Args validator; any number of arguments is accepted")
		}
		if err := tokenomicsCmd.Args(tokenomicsCmd, []string{"off", "status"}); err == nil {
			t.Error("two arguments were accepted; a mistyped invocation must not run one of them")
		}
		for _, ok := range [][]string{{}, {"status"}} {
			if err := tokenomicsCmd.Args(tokenomicsCmd, ok); err != nil {
				t.Errorf("args %v were rejected: %v", ok, err)
			}
		}
	})
}

// TestTokenomicsCommandRegistered is the "Birth of a Verb" check: a verb that exists but is not
// attached to rootCmd is unreachable, and every other test in this file calls RunE directly and
// would pass anyway.
func TestTokenomicsCommandRegistered(t *testing.T) {
	var found bool
	for _, c := range rootCmd.Commands() {
		if strings.Fields(c.Use)[0] == "tokenomics" {
			found = true
			if c.RunE == nil {
				t.Error("tokenomics is registered with no RunE")
			}
			if c.Flags().Lookup("json") == nil {
				t.Error("tokenomics registers no --json flag, so the machine-readable surface is unreachable")
			}
			break
		}
	}
	if !found {
		t.Error("rootCmd has no tokenomics command; af tokenomics is unreachable from the CLI")
	}
}

func itoaWindow(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
