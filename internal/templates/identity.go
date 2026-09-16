package templates

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stempeck/agentfactory/internal/config"
)

// IdentityFile is the name every provisioning site writes an agent's rendered identity to.
const IdentityFile = "CLAUDE.md"

// RenderIdentity renders an agent's CLAUDE.md from the embedded role template, falling back to the
// type default when the agent has no template of its own. It lives here, not in internal/cmd,
// because internal/worktree is one of its callers and internal/cmd already imports internal/worktree.
//
// The WARNING on the fallback path is part of the contract: a formula-generated agent whose template
// is missing from the running binary still functions, but every context carrier downstream will serve
// generic text, and that is worth saying out loud.
func RenderIdentity(tmpl *Templates, role string, entry config.AgentEntry, factoryRoot, dir string) ([]byte, error) {
	templateRole := role
	if !tmpl.HasRole(templateRole) {
		if entry.Formula != "" {
			fmt.Fprintf(os.Stderr, "WARNING: agent %q is formula-generated but its template is not embedded in the binary. Agent will function via workspace CLAUDE.md but af prime will inject a generic template.\n", role)
		}
		templateRole = entry.Type
		if templateRole == "interactive" {
			templateRole = "manager"
		} else if templateRole == "autonomous" {
			templateRole = "supervisor"
		}
	}

	content, err := tmpl.RenderRole(templateRole, RoleData{
		Role:        role,
		Description: entry.Description,
		RootDir:     factoryRoot,
		WorkDir:     dir,
	})
	if err != nil {
		return nil, err
	}
	return []byte(content), nil
}

// WriteIdentity writes rendered identity content to dir/CLAUDE.md, leaving the file untouched when
// it already holds exactly that content. Provisioning runs on every respawn and every af install
// --init, so an unconditional write would churn the mtime of a file agents read at session start.
func WriteIdentity(dir string, content []byte) error {
	path := filepath.Join(dir, IdentityFile)
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(content) {
		return nil
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", IdentityFile, err)
	}
	return nil
}
