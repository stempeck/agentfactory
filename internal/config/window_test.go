package config

import (
	"os"
	"strings"
	"testing"
)

func TestResolveContextWindow(t *testing.T) {
	tests := []struct {
		name       string
		profile    map[string]string
		hostWindow int64
		want       int64
		wantSource string
	}{
		{
			// The operator's declaration is the only operand that can be RIGHT about a gateway
			// the host does not recognise, so it outranks a host report that is an assumption.
			name:       "a declaration outranks the host report",
			profile:    map[string]string{EnvMaxContextTokens: "1000000"},
			hostWindow: 200000,
			want:       1000000,
			wantSource: WindowSourceDeclared,
		},
		{
			name:       "the host report is used when nothing is declared",
			profile:    map[string]string{},
			hostWindow: 262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "the fallback is used when neither operand is usable",
			profile:    map[string]string{},
			hostWindow: 0,
			want:       200000,
			wantSource: WindowSourceFallback,
		},
		{
			name:       "a nil profile is the same as an empty one",
			profile:    nil,
			hostWindow: 262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			// "" is how an operator clears the key, and how config_set writes an unset one.
			name:       "an empty declaration counts as absent",
			profile:    map[string]string{EnvMaxContextTokens: ""},
			hostWindow: 262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "a non-numeric declaration counts as absent",
			profile:    map[string]string{EnvMaxContextTokens: "one million"},
			hostWindow: 262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			// A window of zero is not a window. Falling through is what keeps a denominator
			// from ever being 0.
			name:       "a zero declaration counts as absent",
			profile:    map[string]string{EnvMaxContextTokens: "0"},
			hostWindow: 262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "a negative host report counts as absent",
			profile:    map[string]string{},
			hostWindow: -1,
			want:       200000,
			wantSource: WindowSourceFallback,
		},
		{
			// DecimalTokenCount accepts the whole unsigned range, so the conversion to int64 is
			// where a wrap would happen. A wrapped window is a NEGATIVE denominator, which is
			// worse than no answer at all.
			name:       "a declaration too large for int64 is skipped, not wrapped",
			profile:    map[string]string{EnvMaxContextTokens: "18446744073709551615"},
			hostWindow: 262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "the largest declaration that does fit is honoured",
			profile:    map[string]string{EnvMaxContextTokens: "9223372036854775807"},
			hostWindow: 262144,
			want:       9223372036854775807,
			wantSource: WindowSourceDeclared,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, source := ResolveContextWindow(tc.profile, tc.hostWindow)
			if got != tc.want {
				t.Errorf("window = %d, want %d", got, tc.want)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
			if got <= 0 {
				t.Errorf("window = %d; the resolver must never hand a caller a denominator it cannot divide by", got)
			}
		})
	}
}

// TestResolveContextWindowIsPure is the ADR-004 guard stated as behavior rather than as a
// grep. The package reads no environment at all, and the temptation
// here is specific: the declared window's name IS an environment variable, so a resolver that
// fell back to reading it would look correct on the host that set it and be wrong everywhere
// else. The caller knows which agent it is asking about; the process environment does not.
func TestResolveContextWindowIsPure(t *testing.T) {
	t.Setenv(EnvMaxContextTokens, "999999")

	got, source := ResolveContextWindow(map[string]string{}, 0)
	if got != 200000 || source != WindowSourceFallback {
		t.Errorf("ResolveContextWindow(empty, 0) = (%d, %q) with %s set in the environment; "+
			"want the fallback — this package resolves from its arguments only (ADR-004)",
			got, source, EnvMaxContextTokens)
	}
	if os.Getenv(EnvMaxContextTokens) != "999999" {
		t.Fatal("the test's own fixture did not take effect, so it proves nothing")
	}
}

// TestResolveContextWindowPerAgent is the substrate half of the design's D7/H-R4 requirement:
// one factory, one startup.json, two agents on different model profiles, two different windows.
// The admission predicate that consumes this arrives in a later phase; what Phase 1 owes is a
// resolver whose answer is a function of the AGENT's profile and nothing factory-global.
func TestResolveContextWindowPerAgent(t *testing.T) {
	cfg := &ModelsConfig{Models: map[string]map[string]string{
		"big":   {envModel: "gpt-5.6-sol", EnvMaxContextTokens: "1000000"},
		"small": {envModel: "gpt-5.6-sol"},
	}}

	big, bigSrc := ResolveContextWindow(cfg.Models["big"], 0)
	small, smallSrc := ResolveContextWindow(cfg.Models["small"], 0)

	if big != 1000000 || bigSrc != WindowSourceDeclared {
		t.Errorf("agent on the declaring profile resolved (%d, %q), want (1000000, %q)", big, bigSrc, WindowSourceDeclared)
	}
	if small != 200000 || smallSrc != WindowSourceFallback {
		t.Errorf("agent on the silent profile resolved (%d, %q), want (200000, %q)", small, smallSrc, WindowSourceFallback)
	}
	if big == small {
		t.Error("two agents on different profiles resolved the same window; the operand is not per-agent")
	}
}

// TestResolveContextWindowIgnoresBoundTokens pins the other half of D7/H-R4. bound_tokens is
// factory-global and StepContextConfig's doc says it is a default "and nowhere else in the decision
// path" — it is written once as ctx_bound_tokens for annotation. A resolver that took a
// StartupConfig would let one factory-wide number become every agent's denominator, which is
// the mistake the decision exists to prevent, so the signature is the proof.
func TestResolveContextWindowIgnoresBoundTokens(t *testing.T) {
	cfg := defaultStartupConfig()
	if cfg.StepContext.BoundTokens == 0 {
		t.Fatal("the shipped default for bound_tokens is 0, so this test cannot distinguish anything")
	}

	got, source := ResolveContextWindow(map[string]string{}, 0)
	if source != WindowSourceFallback {
		t.Errorf("source = %q, want %q: bound_tokens must never reach the resolver", source, WindowSourceFallback)
	}
	if got != 200000 {
		t.Errorf("window = %d, want the foreign-model fallback", got)
	}
}

func TestPairingLintUsesTheResolver(t *testing.T) {
	// The lint's cap and the resolver's fallback are the same host assumption, and they were
	// separately open-coded before #668. Two copies of one number drift; this asserts the lint
	// still speaks the resolver's answer.
	fallback, _ := ResolveContextWindow(nil, 0)
	warning, ok := PairingLintProfile("codex", map[string]string{
		envModel:         "gpt-5.6-sol",
		envCompactWindow: "220000",
	})
	if !ok {
		t.Fatal("the lint stopped firing on the shape it exists for")
	}
	if !strings.Contains(warning, "200000") {
		t.Errorf("warning %q no longer names the cap", warning)
	}
	if fallback != 200000 {
		t.Errorf("resolver fallback = %d but the lint warns about 200000; the two have drifted", fallback)
	}
}
