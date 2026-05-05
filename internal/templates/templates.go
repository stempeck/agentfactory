package templates

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

//go:embed roles/*.md.tmpl
var templateFS embed.FS

// RoleData holds the data injected into role templates.
type RoleData struct {
	Role        string // "manager", "supervisor", etc.
	Description string // From agents.json
	RootDir     string // Agent system root directory
	WorkDir     string // Current working directory
}

// Templates wraps parsed role templates.
type Templates struct {
	roleTemplates *template.Template
}

// New parses all embedded role templates and returns a Templates instance.
func New() *Templates {
	t := template.Must(template.ParseFS(templateFS, "roles/*.md.tmpl"))
	return &Templates{roleTemplates: t}
}

// RenderRole renders the template for the given role with the provided data.
func (t *Templates) RenderRole(role string, data RoleData) (string, error) {
	var buf bytes.Buffer
	tmplName := role + ".md.tmpl"
	if err := t.roleTemplates.ExecuteTemplate(&buf, tmplName, data); err != nil {
		return "", fmt.Errorf("rendering template for role %s: %w", role, err)
	}
	return buf.String(), nil
}

// HasRole reports whether a template exists for the given role name.
func (t *Templates) HasRole(role string) bool {
	tmplName := role + ".md.tmpl"
	return t.roleTemplates.Lookup(tmplName) != nil
}
