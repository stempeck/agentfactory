package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #548 Phase 3 scaffold — doc-language drift guard for Phase 4's tier-accuracy sweep (Phase-4
// AC #7). It scans the doc surfaces that make an authority/tier claim about `af down` and asserts
// each keeps the factory-wide operator baseline, carries no stale/inaccurate framing, and never
// presents the scoped grant without the operator qualifier. It is scoped ONLY to surfaces that
// actually make an authority claim (Peer Review Correction #3): command mentions of `af down
// --all` with no authority statement, and worktree-cleanup "teardown" language, are excluded so
// the test does not flag mere command references. manager.md.tmpl / supervisor.md.tmpl carry no
// authority claim and are intentionally NOT scanned.
//
// The assertions are green against the current text (Phase 3 changes no docs). Phase 4 tightens
// them as it rewrites the surfaces for the #548 manager tier.
func TestTeardownDocLanguageDrift(t *testing.T) {
	root := findModuleRoot(t)

	surfaces := []struct {
		path          string // relative to the module root
		mustContain   string // the tier/authority baseline that must survive
		mustNotAppear string // stale/inaccurate framing that must stay absent
	}{
		{"USING_AGENTFACTORY.md", "operator action", "self-only"},
		{"CLAUDE.md", "operator-only", "self-only"},
		{"internal/cmd/down.go", "operator action", "self-only"},
		{"internal/cmd/install_formulas/rapid-soldesign-plan.formula.toml", "operator action", "self-only"},
		{"internal/templates/roles/rapid-soldesign-plan.md.tmpl", "operator action", "self-only"},
	}

	for _, s := range surfaces {
		t.Run(s.path, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, s.path))
			if err != nil {
				t.Fatalf("reading %s: %v", s.path, err)
			}
			content := string(data)

			if !strings.Contains(content, s.mustContain) {
				t.Errorf("%s: missing the tier/authority baseline %q — factory-wide teardown must stay operator-scoped", s.path, s.mustContain)
			}
			if strings.Contains(content, s.mustNotAppear) {
				t.Errorf("%s: carries stale/inaccurate authority framing %q", s.path, s.mustNotAppear)
			}
			// Tier accuracy: a surface that mentions the scoped grant ("specialist ... dispatched")
			// must keep the factory-wide "operator" qualifier, so the grant can never read as a
			// blanket teardown right.
			if strings.Contains(content, "specialist") && strings.Contains(content, "dispatched") &&
				!strings.Contains(content, "operator") {
				t.Errorf("%s: states the scoped-stop grant without the factory-wide 'operator' qualifier", s.path)
			}
		})
	}
}
