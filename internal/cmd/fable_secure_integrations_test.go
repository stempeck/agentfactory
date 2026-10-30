package cmd

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

const fableSecureSource = "install_formulas/fable-secure.formula.toml"

// TestFableSecure_DeliveryStaysSecurityDirNoPR: binding an integration changes no delivery: the program
// still lands in .security/ and opens no PR.
func TestFableSecure_DeliveryStaysSecurityDirNoPR(t *testing.T) {
	f, err := formula.ParseFile(fableSecureSource)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := f.Vars["security_dir"]; !ok || !strings.Contains(v.Default, ".security") {
		t.Errorf("security_dir var = %+v, want a .security default", f.Vars["security_dir"])
	}
	for _, s := range f.Steps {
		if strings.Contains(s.Description, "gh pr create") {
			t.Errorf("step %s opens a PR; fable-secure delivers to .security/ only", s.ID)
		}
	}
}
