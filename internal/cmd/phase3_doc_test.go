package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestUsingAgentfactoryDoc_NoStaleSSHForwarding(t *testing.T) {
	data, err := os.ReadFile("../../USING_AGENTFACTORY.md")
	if err != nil {
		t.Fatalf("reading USING_AGENTFACTORY.md: %v", err)
	}
	content := string(data)
	lower := strings.ToLower(content)

	literalPatterns := map[string]string{
		"ssh-add":          "ssh-add",
		"ssh forwarding":   "ssh forwarding",
		"agent forwarding": "agent forwarding",
	}
	for desc, pat := range literalPatterns {
		if strings.Contains(lower, pat) {
			t.Errorf("USING_AGENTFACTORY.md contains stale SSH agent forwarding reference: %q", desc)
		}
	}
	if regexp.MustCompile(`(?i)ssh.agent`).MatchString(content) {
		t.Error("USING_AGENTFACTORY.md contains stale SSH agent forwarding reference: \"ssh agent\"")
	}

	if !strings.Contains(lower, "keypair") && !strings.Contains(lower, "key-based") {
		t.Error("USING_AGENTFACTORY.md should mention keypair or key-based auth approach")
	}

	if !strings.Contains(content, "recreated") {
		t.Error("USING_AGENTFACTORY.md should document that existing iOS containers must be recreated")
	}

	if !strings.Contains(content, "### iOS Projects") {
		t.Error("USING_AGENTFACTORY.md must still have an iOS Projects section")
	}

	if !strings.Contains(content, "AF_BUILD_HOST_USER") {
		t.Error("USING_AGENTFACTORY.md must still document CI automation env vars")
	}
}

