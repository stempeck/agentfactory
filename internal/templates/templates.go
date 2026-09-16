package templates

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

// The directive is the one place the roster's location stays a literal — //go:embed is read by the
// compiler before any const exists.
//
//go:embed roles/*.md.tmpl
var templateFS embed.FS

const (
	roleTemplateDir = "roles"
	roleTemplateExt = ".md.tmpl"
)

// RoleData holds the data injected into role templates.
type RoleData struct {
	Role        string // "manager", "supervisor", etc.
	Description string // From agents.json
	RootDir     string // Agent system root directory (local root -- worktree root for worktree agents)
	WorkDir     string // Current working directory
}

// Templates wraps parsed role templates.
type Templates struct {
	roleTemplates *template.Template
}

// New parses all embedded role templates and returns a Templates instance.
func New() *Templates {
	t := template.Must(template.ParseFS(templateFS, roleTemplateDir+"/*"+roleTemplateExt))
	return &Templates{roleTemplates: t}
}

// RenderRole renders the template for the given role with the provided data.
func (t *Templates) RenderRole(role string, data RoleData) (string, error) {
	var buf bytes.Buffer
	tmplName := role + roleTemplateExt
	if err := t.roleTemplates.ExecuteTemplate(&buf, tmplName, data); err != nil {
		return "", fmt.Errorf("rendering template for role %s: %w", role, err)
	}
	return buf.String(), nil
}

// HasRole reports whether a template exists for the given role name.
func (t *Templates) HasRole(role string) bool {
	tmplName := role + roleTemplateExt
	return t.roleTemplates.Lookup(tmplName) != nil
}

// Roles returns every embedded role name, sorted. The embed FS is the roster's only source of
// truth: a caller that globbed the checkout instead would enumerate the tree it runs FROM rather
// than the binary it runs AGAINST, and a sweep meant to cover "every shipped role" has to mean the
// shipped one. embed.FS.ReadDir returns entries sorted, so the order is stable across calls.
// A caller that cannot act on an empty roster must say so itself — an enumerator that has stopped
// seeing templates is indistinguishable here from a package with none.
func Roles() []string {
	entries, err := templateFS.ReadDir(roleTemplateDir)
	if err != nil {
		return nil
	}
	roles := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), roleTemplateExt) {
			continue
		}
		roles = append(roles, strings.TrimSuffix(e.Name(), roleTemplateExt))
	}
	return roles
}
