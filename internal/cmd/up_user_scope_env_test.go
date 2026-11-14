package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestUpWarnsUserScopeEnvConflict pins the user-scope env comparison at `af up`: processes in every
// session carry a key the user-scope settings.json sets, and that value can override the profile's
// export, so af names it. It must not claim the value decides the window: a session's window has been
// observed at the profile's figure while its child shell printed the user-scope one. Only the two
// window keys print their values, because user-scope env may hold credentials; the canary below is a
// token no line af prints may ever carry. The launch is positional, so a call placed inside the
// blanket-only block never runs here.
func TestUpWarnsUserScopeEnvConflict(t *testing.T) {
	const canary = "af-userscope-canary-7f3a"

	root := setupTestFactoryForDone(t, "manager")
	initTestGitRepo(t, root)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"hi": {
				"ANTHROPIC_MODEL":                "claude-opus-5",
				"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "1000000",
				"ANTHROPIC_AUTH_TOKEN":           "profile-placeholder-value",
			},
		},
		Agents: map[string]string{"manager": "hi"},
	})
	const wtID = "wt-userscope"
	wtPath := filepath.Join(root, ".agentfactory", "worktrees", wtID)
	if err := os.MkdirAll(config.AgentDir(wtPath, "manager"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_WORKTREE", wtPath)
	t.Setenv("AF_WORKTREE_ID", wtID)
	t.Chdir(root)

	claudeDir := t.TempDir()
	settings := `{"env":{"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"262144","ANTHROPIC_AUTH_TOKEN":"` + canary + `"}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claudeConfigDirEnv, claudeDir)
	setupHermeticSessions(t)

	origScope := userScopeEnvFn
	userScopeEnvFn = readUserScopeEnv
	t.Cleanup(func() { userScopeEnvFn = origScope })

	cmd, buf := launchCmd(t)
	var err error
	stderr := captureStderr(t, func() { err = runUp(cmd, []string{"manager"}) })
	output := buf.String() + stderr
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("af up manager: err=%v\n%s", err, output)
		}
	})

	if strings.Contains(output, canary) {
		t.Fatalf("a user-scope credential value reached af up's output:\n%s", output)
	}

	const head = "warning: user-scope Claude Code settings ("
	const windowKey = ") set env CLAUDE_CODE_MAX_CONTEXT_TOKENS, which profile \"hi\" also exports with a different value; "
	const tokenKey = ") set env ANTHROPIC_AUTH_TOKEN, which profile \"hi\" also exports with a different value; "
	const windowValues = " (user scope 262144, profile 1000000)"
	const overclaim = "wins inside every session"

	var windowLines, tokenLines []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, head) {
			continue
		}
		switch {
		case strings.Contains(line, "set env CLAUDE_CODE_MAX_CONTEXT_TOKENS,"):
			windowLines = append(windowLines, line)
		case strings.Contains(line, "set env ANTHROPIC_AUTH_TOKEN,"):
			tokenLines = append(tokenLines, line)
		}
	}
	if len(windowLines) != 1 || len(tokenLines) != 1 {
		t.Fatalf("want exactly one line per conflicting key of profile \"hi\"; got window %q, token %q", windowLines, tokenLines)
	}
	windowLine, tokenLine := windowLines[0], tokenLines[0]
	if !strings.Contains(windowLine, windowKey) || !strings.HasSuffix(windowLine, windowValues) {
		t.Errorf("want a line starting %q, naming %q and ending %q (both values for the window key); got %q", head, windowKey, windowValues, windowLine)
	}
	if !strings.Contains(tokenLine, tokenKey) {
		t.Errorf("want a line starting %q and naming %q (the key named, no values); got %q", head, tokenKey, tokenLine)
	}
	for _, line := range []string{windowLine, tokenLine} {
		if strings.Contains(line, overclaim) {
			t.Errorf("the warning claims the user-scope value %q, which reads as the session's window; say what is observed: %q", overclaim, line)
		}
		if !strings.Contains(line, "every session") {
			t.Errorf("the warning must say processes in every session carry the user-scope value: %q", line)
		}
	}
	if !strings.Contains(windowLine, "can override") {
		t.Errorf("the window-key warning must say the user-scope value can override the profile's window: %q", windowLine)
	}
	if strings.Contains(tokenLine, "(user scope") {
		t.Errorf("a non-window key must never print its values: %q", tokenLine)
	}
}
