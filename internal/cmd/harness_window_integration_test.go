//go:build integration

package cmd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

type shippedWindowProbe struct {
	p   harnessWindowProbe
	err error
}

// TestShippedClaudeProfilesWindowAgreement probes every not-foreign tracked profile on the harness
// liveClaudeBinary hands it. It enumerates the profiles itself rather than through the rows test,
// which is SKIPOSS and absent from the published build, and memoises per (id, window exports) to
// stay inside the package's -timeout.
func TestShippedClaudeProfilesWindowAgreement(t *testing.T) {
	bin := liveClaudeBinary(t)
	pin := quickstartPin(t, "CLAUDE_CODE_VERSION")

	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("reading the harness binary %s: %v", bin, err)
	}
	for _, k := range harnessWindowKeyViolations(data) {
		t.Errorf("the harness %s names %s, which neither config.WindowDecidingEnvKeys nor notWindowDecidingEnvKeys sorts: measure whether it moves contextWindow and add it to one", bin, k)
	}

	cfg, err := config.LoadModelsConfig(findModuleRoot(t))
	if err != nil || cfg == nil {
		t.Fatalf("loading the tracked .agentfactory/models.json: %v", err)
	}
	memo := map[string]shippedWindowProbe{}
	versionChecked, checked := false, 0
	for _, name := range sortedMapKeys(cfg.Models) {
		profile := cfg.Models[name]
		if config.ForeignModelProfile(profile) {
			continue
		}
		checked++
		id := profile["ANTHROPIC_MODEL"]
		parts := []string{id}
		for _, k := range config.WindowDecidingEnvKeys {
			parts = append(parts, k+"="+profile[k])
		}
		key := strings.Join(parts, "\x00")
		r, ok := memo[key]
		if !ok {
			ctx, cancel := context.WithTimeout(context.Background(), harnessProbeCheckTimeout)
			r.p, r.err = probeHarnessWindow(ctx, profile)
			cancel()
			memo[key] = r
		}
		if r.err != nil {
			t.Errorf("profile %q: window probe did not complete (%v) — window not measured", name, r.err)
			continue
		}
		if !versionChecked {
			versionChecked = true
			if r.p.HarnessVersion != pin {
				t.Errorf("the probe ran Claude Code %q, the pin is %q", r.p.HarnessVersion, pin)
			}
		}
		if r.p.Unrecognized {
			t.Errorf("profile %q: Claude Code %s does not recognise model %q (window %d)", name, r.p.HarnessVersion, id, r.p.ContextWindow)
			continue
		}
		if declared := windowDeclared(profile[config.EnvMaxContextTokens]); declared > 0 && r.p.ContextWindow != declared {
			t.Errorf("profile %q: Claude Code %s applies a %d-token window to model %q, but the profile declares %d", name, r.p.HarnessVersion, r.p.ContextWindow, id, declared)
		}
	}
	if checked == 0 {
		t.Fatal("the tracked registry holds no not-foreign profile to probe")
	}
}
