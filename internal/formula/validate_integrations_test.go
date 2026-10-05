package formula

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func integrationsFormula(required, optional []string) *Formula {
	return &Formula{
		Name:                 "x",
		Type:                 TypeWorkflow,
		Steps:                []Step{{ID: "s1"}},
		Integrations:         required,
		IntegrationsOptional: optional,
	}
}

// classifyLamp (internal/cmd/formula_validate.go) keys on these substrings;
// an integrations error carrying one would be shown under the wrong lamp.
var lampSubstrings = []string{"cycle", "id:", "needs unknown"}

func TestValidate_IntegrationListsSyntax(t *testing.T) {
	refused := []struct {
		name     string
		required []string
		optional []string
		want     []string
	}{
		{"path traversal name", []string{"../etc"}, nil, []string{"../etc"}},
		{"leading digit", []string{"1bad"}, nil, []string{"1bad"}},
		{"empty name", nil, []string{""}, []string{"integration"}},
		{"embedded space", nil, []string{"a b"}, []string{"a b"}},
		{"duplicate in integrations", []string{"aws-core", "aws-core"}, nil, []string{"duplicate", "aws-core"}},
		{"duplicate in integrations_optional", nil, []string{"defenseclaw", "defenseclaw"}, []string{"duplicate", "defenseclaw"}},
		{"same name in both lists", []string{"aws-core"}, []string{"aws-core"}, []string{"aws-core"}},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			err := integrationsFormula(tc.required, tc.optional).Validate()
			if err == nil {
				t.Fatalf("Validate() = nil for integrations=%q integrations_optional=%q, want an error", tc.required, tc.optional)
			}
			msg := err.Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("err = %q, want it to contain %q", msg, w)
				}
			}
			for _, lamp := range lampSubstrings {
				if strings.Contains(msg, lamp) {
					t.Errorf("err = %q contains %q, which classifyLamp would mis-lamp", msg, lamp)
				}
			}
		})
	}

	t.Run("hyphenated names in each list accepted", func(t *testing.T) {
		if err := integrationsFormula([]string{"fix-ture"}, []string{"defense-claw"}).Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})
	t.Run("empty lists accepted", func(t *testing.T) {
		if err := integrationsFormula(nil, []string{}).Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})
	t.Run("toml keys decode", func(t *testing.T) {
		f, err := Parse([]byte(`formula = "x"
type = "workflow"
integrations = ["aws-core", "fix-ture"]
integrations_optional = ["defenseclaw"]

[[steps]]
id = "s1"
title = "one"
`))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if strings.Join(f.Integrations, ",") != "aws-core,fix-ture" {
			t.Errorf("Integrations = %q, want [aws-core fix-ture]", f.Integrations)
		}
		if strings.Join(f.IntegrationsOptional, ",") != "defenseclaw" {
			t.Errorf("IntegrationsOptional = %q, want [defenseclaw]", f.IntegrationsOptional)
		}
	})
	t.Run("toml bad name refused at parse", func(t *testing.T) {
		_, err := Parse([]byte(`formula = "x"
type = "workflow"
integrations = ["../etc"]

[[steps]]
id = "s1"
title = "one"
`))
		if err == nil {
			t.Fatal(`Parse accepted integrations = ["../etc"], want a validation error`)
		}
	})
}

func TestValidSkillName_AcceptsNamespaced(t *testing.T) {
	for _, name := range []string{"aws-core:search", "acme-int-plugin:acme-int", "plain-skill"} {
		t.Run("accepts "+name, func(t *testing.T) {
			if !validSkillName.MatchString(name) {
				t.Errorf("validSkillName rejects %q, want accepted", name)
			}
		})
	}
	for _, name := range []string{"a:b:c", ":skill", "ns:", "ns:../x", "ns:1bad", "../etc", "foo/bar", "", "1bad"} {
		t.Run("rejects "+name, func(t *testing.T) {
			if validSkillName.MatchString(name) {
				t.Errorf("validSkillName accepts %q, want rejected", name)
			}
		})
	}

	t.Run("Validate accepts a namespaced skill", func(t *testing.T) {
		f := &Formula{Name: "x", Type: TypeWorkflow, Steps: []Step{{ID: "s1"}}, Skills: []string{"ns:skill"}}
		if err := f.Validate(); err != nil {
			t.Fatalf("Validate() = %v, want nil", err)
		}
	})
	t.Run("Validate refuses a double namespace", func(t *testing.T) {
		f := &Formula{Name: "x", Type: TypeWorkflow, Steps: []Step{{ID: "s1"}}, Skills: []string{"a:b:c"}}
		err := f.Validate()
		if err == nil || !strings.Contains(err.Error(), "invalid skill name") {
			t.Fatalf("Validate() = %v, want an invalid skill name error", err)
		}
	})
	t.Run("ValidateSkills skips namespaced names", func(t *testing.T) {
		f := &Formula{Name: "x", Skills: []string{"ns:skill"}}
		if err := f.ValidateSkills(t.TempDir()); err != nil {
			t.Fatalf("ValidateSkills() = %v, want nil (namespaced skills resolve at admission)", err)
		}
	})
	t.Run("ValidateSkills still stats unnamespaced SKILL.md", func(t *testing.T) {
		dir := t.TempDir()
		f := &Formula{Name: "x", Skills: []string{"local"}}
		err := f.ValidateSkills(dir)
		if err == nil || !strings.Contains(err.Error(), "no SKILL.md") {
			t.Fatalf("ValidateSkills() = %v, want the missing SKILL.md error", err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "local"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "local", "SKILL.md"), []byte("# local\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := f.ValidateSkills(dir); err != nil {
			t.Fatalf("ValidateSkills() = %v after creating SKILL.md, want nil", err)
		}
	})
}
