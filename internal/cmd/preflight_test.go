package cmd

import (
	"strings"
	"testing"
)

func TestExtractCommands_FencedBlocks(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want []string
	}{
		{
			name: "single command",
			desc: "```bash\ngt prime\n```",
			want: []string{"gt"},
		},
		{
			name: "multiple lines",
			desc: "```bash\ngt prime\nbd show x\n```",
			want: []string{"bd", "gt"},
		},
		{
			name: "pipes",
			desc: "```bash\ngt polecats --json | jq '.x'\n```",
			want: []string{"gt", "jq"},
		},
		{
			name: "variable assignment",
			desc: "```bash\nVAR=val gt prime\n```",
			want: []string{"gt"},
		},
		{
			name: "comment lines skipped",
			desc: "```bash\n# This is a comment\ngt prime\n```",
			want: []string{"gt"},
		},
		{
			name: "empty lines skipped",
			desc: "```bash\n\ngt prime\n\n```",
			want: []string{"gt"},
		},
		{
			name: "bare fence no language",
			desc: "```\ngt prime\n```",
			want: []string{"gt"},
		},
		{
			name: "redirect not a command",
			desc: "```bash\ngt polecats 2>/dev/null\n```",
			want: []string{"gt"},
		},
		{
			name: "pipe with redirect",
			desc: "```bash\ngt polecats --json 2>/dev/null | jq '.x'\n```",
			want: []string{"gt", "jq"},
		},
		{
			name: "real formula description",
			desc: "Initialize your session and understand your assignment.\n\n**1. Prime your environment:**\n```bash\ngt prime                    # Load role context\nbd prime                    # Load beads context\n```\n\n**2. Check your hook:**\n```bash\ngt hook               # Shows your pinned molecule and hook_bead\n```\n\nThe hook_bead is your assigned issue. Read it carefully:\n```bash\nbd show {{issue}}           # Full issue details\n```\n\n**3. Check inbox for additional context:**\n```bash\ngt mail inbox\n# Read any HANDOFF or assignment messages\n```\n\n**4. Extract requirements from the bead:**\nThe bead is your source of truth. It may provide requirements in one of three forms:\n\n- **Inline requirements**: The bead description itself contains the spec. Extract requirements directly.\n- **Proposal document path**: The bead references a file (e.g., `docs/feature-proposal.md`,\n  `IMPLREADME.md`). Read it completely.\n- **GitHub issue link**: The bead contains a URL (e.g., `https://github.com/org/repo/issues/123`).\n  Fetch the issue via `gh issue view <number> --repo <org/repo>` and extract requirements from it.\n\nWhichever form, capture the full requirements — this is your spec.\n\n**5. Understand the requirements:**\n- What exactly needs to be done?\n- What files are likely involved?\n- Are there dependencies or blockers?\n- What does \"done\" look like?\n- Reproduction steps (for bugs)\n- Error messages or expected behavior\n- Acceptance criteria\n\n**6. Verify you can proceed:**\n- No unresolved blockers on the issue\n- You understand what to do\n- Required resources are available\n\nIf blocked or unclear, mail Witness immediately:\n```bash\ngt mail send <rig>/witness -s \"HELP: Unclear requirements\" -m \"Issue: {{issue}}\nQuestion: <what you need clarified>\"\n```\n\n**Exit criteria:** You understand the work and can begin implementation.",
			want: []string{"bd", "gh", "gt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommands(tt.desc)
			if len(got) != len(tt.want) {
				t.Fatalf("extractCommands() returned %d commands, want %d: got %v", len(got), len(tt.want), got)
			}
			for i, cmd := range tt.want {
				if got[i] != cmd {
					t.Errorf("extractCommands()[%d] = %q, want %q", i, got[i], cmd)
				}
			}
		})
	}
}

func TestExtractCommands_InlineBackticks(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want []string
	}{
		{
			name: "simple command",
			desc: "Run `gt done` to finish",
			want: []string{"gt"},
		},
		{
			name: "command with args",
			desc: "Use `gh issue view 123`",
			want: []string{"gh"},
		},
		{
			name: "skip flag",
			desc: "Pass `-f` for force",
			want: nil,
		},
		{
			name: "skip path dot slash",
			desc: "Read `./script.sh`",
			want: nil,
		},
		{
			name: "skip filename with dot",
			desc: "See `issues.jsonl`",
			want: nil,
		},
		{
			name: "skip variable",
			desc: "Use `$HOME` path",
			want: nil,
		},
		{
			name: "skip all caps",
			desc: "Set `SPEC` value",
			want: nil,
		},
		{
			name: "skip angle bracket",
			desc: "Provide `<number>`",
			want: nil,
		},
		{
			name: "skip bracket",
			desc: "Check `[optional]`",
			want: nil,
		},
		{
			name: "skip slash path",
			desc: "Read `/etc/hosts`",
			want: nil,
		},
		{
			name: "skip gt redirect",
			desc: "Redirect `>output.txt`",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommands(tt.desc)
			if len(got) != len(tt.want) {
				t.Fatalf("extractCommands() returned %d commands, want %d: got %v, want %v", len(got), len(tt.want), got, tt.want)
			}
			for i, cmd := range tt.want {
				if got[i] != cmd {
					t.Errorf("extractCommands()[%d] = %q, want %q", i, got[i], cmd)
				}
			}
		})
	}
}

func TestExtractCommands_ShellBuiltins(t *testing.T) {
	desc := "```bash\necho hello\nexport FOO=bar\nif true; then echo done; fi\nset -e\nread input\nlocal x=1\nreturn 0\nshift\nunset VAR\ntest -f file\n```\nUse `echo` and `export` and `for` loops"
	got := extractCommands(desc)
	if len(got) != 0 {
		t.Errorf("extractCommands() should return empty for all builtins, got %v", got)
	}
}

func TestExtractCommands_Deduplication(t *testing.T) {
	desc := "```bash\ngt prime\ngt hook\n```\nRun `gt done`"
	got := extractCommands(desc)
	if len(got) != 1 {
		t.Fatalf("extractCommands() returned %d commands, want 1: got %v", len(got), got)
	}
	if got[0] != "gt" {
		t.Errorf("extractCommands()[0] = %q, want %q", got[0], "gt")
	}
}

func TestExtractCommands_Empty(t *testing.T) {
	tests := []struct {
		name string
		desc string
	}{
		{name: "empty string", desc: ""},
		{name: "plain text", desc: "Just plain text with no code at all"},
		{name: "no backticks", desc: "No backticks and no fenced blocks here"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommands(tt.desc)
			if len(got) != 0 {
				t.Errorf("extractCommands(%q) = %v, want empty", tt.desc, got)
			}
		})
	}
}

func TestExtractCommands_TemplateVariables(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want []string
	}{
		{
			name: "dollar-brace variable",
			desc: "```bash\n${issue}\n```",
			want: nil,
		},
		{
			name: "double-curly variable",
			desc: "```bash\n{{issue}}\n```",
			want: nil,
		},
		{
			name: "inline dollar-brace",
			desc: "Use `${HOME}` path",
			want: nil,
		},
		{
			name: "inline double-curly",
			desc: "Show `{{issue}}` details",
			want: nil,
		},
		{
			name: "command with template arg",
			desc: "```bash\nbd show {{issue}}\n```",
			want: []string{"bd"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCommands(tt.desc)
			if len(got) != len(tt.want) {
				t.Fatalf("extractCommands() returned %d commands, want %d: got %v, want %v", len(got), len(tt.want), got, tt.want)
			}
			for i, cmd := range tt.want {
				if got[i] != cmd {
					t.Errorf("extractCommands()[%d] = %q, want %q", i, got[i], cmd)
				}
			}
		})
	}
}

func TestOutputCommandPreflight_MissingCommands(t *testing.T) {
	var buf strings.Builder
	outputCommandPreflight(&buf, "```bash\nzzz_nonexistent_cmd_12345 --help\n```")
	output := buf.String()

	if !strings.Contains(output, "### Command Pre-flight") {
		t.Error("output should contain '### Command Pre-flight'")
	}
	if !strings.Contains(output, "NOT FOUND") {
		t.Error("output should contain 'NOT FOUND'")
	}
	if !strings.Contains(output, "zzz_nonexistent_cmd_12345") {
		t.Error("output should contain the missing command name")
	}
	if !strings.Contains(output, "best-effort") {
		t.Error("output should contain 'best-effort' advisory text")
	}
	if strings.Contains(output, "Action required") {
		t.Error("output should NOT contain 'Action required' hard-stop language")
	}
}

func TestOutputCommandPreflight_AllPresent(t *testing.T) {
	var buf strings.Builder
	outputCommandPreflight(&buf, "```bash\nls -la\n```")
	if buf.Len() != 0 {
		t.Errorf("expected no output when all commands found, got %q", buf.String())
	}
}

func TestOutputCommandPreflight_NoCommands(t *testing.T) {
	tests := []struct {
		name string
		desc string
	}{
		{name: "empty description", desc: ""},
		{name: "plain text", desc: "Just plain text, no commands"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			outputCommandPreflight(&buf, tt.desc)
			if buf.Len() != 0 {
				t.Errorf("expected no output for %q, got %q", tt.name, buf.String())
			}
		})
	}
}

func TestOutputStandingDirective(t *testing.T) {
	var buf strings.Builder
	outputStandingDirective(&buf)
	output := buf.String()

	if !strings.Contains(output, "### Command Verification") {
		t.Error("output should contain '### Command Verification'")
	}
	if !strings.Contains(output, "which") {
		t.Error("output should contain 'which'")
	}
	if !strings.Contains(output, "command -v") {
		t.Error("output should contain 'command -v'")
	}
	if !strings.Contains(output, "af mail send") {
		t.Error("output should contain 'af mail send'")
	}
	if !strings.Contains(output, "STOP") {
		t.Error("output should contain 'STOP'")
	}
}

func TestExtractCommands_BashBuiltins(t *testing.T) {
	desc := "```bash\ndeclare -A PHASE_SKILLS\nmapfile -t PHASE_NUMS\nwhile IFS= read -r p; do\ncase \"$x\" in\nuntil false; do\nselect item in list; do\nfunction foo\neval \"echo\"\nexec 3>&1\n```"
	got := extractCommands(desc)
	if len(got) != 0 {
		t.Errorf("extractCommands() should return empty for all bash builtins, got %v", got)
	}
}

func TestExtractCommands_TokenShapeFiltering(t *testing.T) {
	desc := "```bash\n)\n.\n\"\n/^#{2,3}\ntrue)\n6.\n&&\n||\n;;\n*)\n---\n**bold**\n```"
	got := extractCommands(desc)
	if len(got) != 0 {
		t.Errorf("extractCommands() should return empty for non-command tokens, got %v", got)
	}
}

func TestExtractCommands_QuotedVariables(t *testing.T) {
	desc := "```bash\n\"$SECTION\"\n\"$VAR\"\n\"$OUTLINE_PATH\"\n```"
	got := extractCommands(desc)
	if len(got) != 0 {
		t.Errorf("extractCommands() should return empty for quoted variables, got %v", got)
	}
}

func TestExtractCommands_AwkSedBodies(t *testing.T) {
	t.Run("single-quoted awk", func(t *testing.T) {
		desc := "```bash\nawk '{ found=1; next }' file\n```"
		got := extractCommands(desc)
		if len(got) != 1 || got[0] != "awk" {
			t.Errorf("extractCommands() should return [\"awk\"] only, got %v", got)
		}
	})

	t.Run("double-quoted multi-line awk", func(t *testing.T) {
		desc := "```bash\nawk \"\n    /^#{2,3} .*Phase ${PNUM}:/ { found=1; next }\n    found && /^#{2,3} .*Phase [0-9]+[A-Za-z]?:/ { exit }\n    found { print }\n\" \"$OUTLINE_PATH\"\n```"
		got := extractCommands(desc)
		if len(got) != 1 || got[0] != "awk" {
			t.Errorf("extractCommands() should return [\"awk\"] only for multi-line awk body, got %v", got)
		}
	})
}

func TestExtractCommands_ComplexStepDescription(t *testing.T) {
	desc := "```bash\ndeclare -A PHASE_SKILLS\nmapfile -t PHASE_NUMS < <(grep -oP '(?<=Phase )\\d+' \"$OUTLINE_PATH\" | sort -u)\nwhile IFS= read -r p; do\n  PNUM=\"$p\"\n  SECTION=$(awk \"\n    /^#{2,3} .*Phase ${PNUM}:/ { found=1; next }\n    found && /^#{2,3} .*Phase [0-9]+[A-Za-z]?:/ { exit }\n    found { print }\n  \" \"$OUTLINE_PATH\")\n  SKILLS=$(echo \"$SECTION\" | grep -E '^#{2,3}' | sed -E 's/^#{2,3} //' | sort -u | awk '{print $2}')\n  printf '%05d%s %s\\n' \"$PNUM\" \"Phase\" \"$SKILLS\"\ndone\n```"

	got := extractCommands(desc)

	// These are legitimate external commands that SHOULD be detected
	legitimate := map[string]bool{"grep": true, "sort": true, "awk": true, "sed": true}

	// These are false positives that should NOT be in the result
	falsePositives := []string{
		"declare", "mapfile", "while", "read", "do", "done",
		"found", "/^#{2,3}", "\"$SECTION\"", "true)", ")", ".",
		"\"", "Phase", "markdown", "6.", "echo", "printf",
	}

	for _, fp := range falsePositives {
		for _, cmd := range got {
			if cmd == fp {
				t.Errorf("extractCommands() returned false positive %q in result %v", fp, got)
			}
		}
	}

	// Verify at least some legitimate commands are found
	foundLegitimate := 0
	for _, cmd := range got {
		if legitimate[cmd] {
			foundLegitimate++
		}
	}
	if foundLegitimate == 0 {
		t.Errorf("extractCommands() should find at least some legitimate commands (grep, sort, awk, sed), got %v", got)
	}
}

func TestExtractCommands_RealCommands(t *testing.T) {
	desc := "```bash\ngt prime\nbd show issue\nnonexistent-tool --help\n```"

	got := extractCommands(desc)

	expected := []string{"bd", "gt", "nonexistent-tool"}
	for _, exp := range expected {
		found := false
		for _, cmd := range got {
			if cmd == exp {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("extractCommands() should detect real command %q, got %v", exp, got)
		}
	}
}
