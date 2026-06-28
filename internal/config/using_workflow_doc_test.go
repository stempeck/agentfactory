package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUsingDoc_WorkflowsExampleValidates enforces THREAD-1 (PR #448): USING_AGENTFACTORY.md
// must document a dispatch.json `workflows` example, and that documented example must
// actually load and validate (so we never publish a config users copy that fails to load).
func TestUsingDoc_WorkflowsExampleValidates(t *testing.T) {
	docPath := filepath.Join("..", "..", "USING_AGENTFACTORY.md")
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}

	block, ok := extractJSONBlockContaining(string(data), `"workflows"`)
	if !ok {
		t.Fatalf("USING_AGENTFACTORY.md has no ```json dispatch example containing a \"workflows\" array (THREAD-1: document how to configure workflows alongside the dispatch.json example)")
	}

	root := t.TempDir()
	cfgPath := DispatchConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadDispatchConfig(root)
	if err != nil {
		t.Fatalf("the documented workflows example does not load/validate: %v\n--- documented block ---\n%s", err, block)
	}
	if len(cfg.Workflows) == 0 {
		t.Errorf("the documented dispatch.json example parsed but contains no workflows[] entries")
	}
}

// extractJSONBlockContaining returns the body of the first ```json fenced code block
// whose contents contain needle.
func extractJSONBlockContaining(doc, needle string) (string, bool) {
	const fence = "```"
	rest := doc
	for {
		i := strings.Index(rest, fence+"json")
		if i < 0 {
			return "", false
		}
		rest = rest[i+len(fence+"json"):]
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return "", false
		}
		rest = rest[nl+1:]
		end := strings.Index(rest, fence)
		if end < 0 {
			return "", false
		}
		body := rest[:end]
		if strings.Contains(body, needle) {
			return body, true
		}
		rest = rest[end+len(fence):]
	}
}
