package cmd

import (
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

var fencedBlockRegex = regexp.MustCompile("(?s)```(?:bash|sh|shell|zsh)?\\s*\\n(.*?)```")
var inlineBacktickRegex = regexp.MustCompile("`([^`]+)`")
var hasLetterRegex = regexp.MustCompile("[a-zA-Z]")

var shellBuiltins = map[string]bool{
	// Original 20
	"echo": true, "export": true, "if": true, "then": true,
	"else": true, "fi": true, "for": true, "do": true,
	"done": true, "true": true, "false": true, "test": true,
	"[": true, "]": true, "set": true, "unset": true,
	"local": true, "return": true, "shift": true, "read": true,
	// bash builtins (compgen -b)
	"alias": true, "bg": true, "bind": true, "break": true,
	"builtin": true, "caller": true, "cd": true, "command": true,
	"compgen": true, "complete": true, "compopt": true, "continue": true,
	"declare": true, "dirs": true, "disown": true, "enable": true,
	"eval": true, "exec": true, "exit": true, "fc": true,
	"fg": true, "getopts": true, "hash": true, "help": true,
	"history": true, "jobs": true, "kill": true, "let": true,
	"logout": true, "mapfile": true, "popd": true, "printf": true,
	"pushd": true, "pwd": true, "readarray": true, "readonly": true,
	"source": true, "suspend": true, "times": true, "trap": true,
	"type": true, "typeset": true, "ulimit": true, "umask": true,
	"unalias": true, "wait": true,
	// bash keywords (compgen -k)
	"!": true, "[[": true, "]]": true, "case": true, "coproc": true,
	"esac": true, "function": true, "in": true, "select": true,
	"until": true, "while": true, "{": true, "}": true,
	"elif": true, "time": true,
	// special builtins
	".": true, ":": true, "shopt": true,
}

func extractCommands(description string) []string {
	commands := make(map[string]bool)

	// Phase 1: Fenced code blocks
	fencedMatches := fencedBlockRegex.FindAllStringSubmatchIndex(description, -1)
	for _, loc := range fencedMatches {
		block := description[loc[2]:loc[3]]
		var dslCloseQuote byte
		inDSLBody := false
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			if inDSLBody {
				if strings.ContainsRune(line, rune(dslCloseQuote)) {
					inDSLBody = false
				}
				continue
			}

			// Strip trailing comments
			if idx := strings.Index(line, " #"); idx >= 0 {
				line = strings.TrimSpace(line[:idx])
			}
			if line == "" {
				continue
			}

			if cmdToken, isDSL, closeQuote := detectDSLCommand(line); isDSL {
				if cmdToken != "" {
					commands[cmdToken] = true
				}
				if closeQuote != 0 {
					inDSLBody = true
					dslCloseQuote = closeQuote
				}
				continue
			}

			// Handle pipes: split on |, process each segment
			segments := strings.Split(line, "|")
			for _, seg := range segments {
				seg = strings.TrimSpace(seg)
				if seg == "" {
					continue
				}
				extractFencedCommand(seg, commands)
			}
		}
	}

	// Phase 2: Inline backticks (outside fenced blocks)
	// Remove fenced blocks to avoid double-counting
	cleaned := description
	for _, loc := range fencedBlockRegex.FindAllStringIndex(description, -1) {
		cleaned = strings.Replace(cleaned, description[loc[0]:loc[1]], "", 1)
	}
	for _, match := range inlineBacktickRegex.FindAllStringSubmatch(cleaned, -1) {
		content := strings.TrimSpace(match[1])
		if content == "" {
			continue
		}
		token := strings.Fields(content)[0]
		if shouldSkipInlineToken(token) {
			continue
		}
		if !shellBuiltins[token] {
			commands[token] = true
		}
	}

	if len(commands) == 0 {
		return nil
	}
	result := make([]string, 0, len(commands))
	for cmd := range commands {
		result = append(result, cmd)
	}
	sort.Strings(result)
	return result
}

func extractFencedCommand(segment string, commands map[string]bool) {
	tokens := strings.Fields(segment)
	if len(tokens) == 0 {
		return
	}

	// Skip variable assignments at the start: VAR=val cmd → extract cmd
	i := 0
	for i < len(tokens) && strings.Contains(tokens[i], "=") && !strings.HasPrefix(tokens[i], "-") {
		i++
	}
	if i >= len(tokens) {
		return
	}
	cmd := tokens[i]

	// Strip trailing semicolons
	cmd = strings.TrimRight(cmd, ";")
	if cmd == "" {
		return
	}

	// Skip redirections (e.g., 2>/dev/null, >/dev/null)
	if isRedirection(cmd) {
		return
	}
	// Skip template variables
	if strings.HasPrefix(cmd, "{{") || strings.HasPrefix(cmd, "${") || strings.HasPrefix(cmd, "$") {
		return
	}
	// Skip shell builtins
	if shellBuiltins[cmd] {
		return
	}
	if !isPlausibleCommand(cmd) {
		return
	}
	// Skip angle-bracket tokens
	if strings.HasPrefix(cmd, "<") || strings.HasPrefix(cmd, ">") {
		return
	}
	// Skip tokens ending with colon (labels, not commands)
	if strings.HasSuffix(cmd, ":") {
		return
	}

	commands[cmd] = true
}

func isRedirection(token string) bool {
	if strings.HasPrefix(token, ">") || strings.HasPrefix(token, "<") {
		return true
	}
	// Handle 2>/dev/null, 2>&1, etc.
	for i, ch := range token {
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch == '>' || ch == '<' {
			return i > 0 // e.g., "2>" at position 1+
		}
		break
	}
	return false
}

var dslCommands = map[string]bool{
	"awk": true, "gawk": true, "mawk": true, "nawk": true, "sed": true,
}

func detectDSLCommand(line string) (cmdToken string, isDSL bool, closeQuote byte) {
	tokens := strings.Fields(line)
	if len(tokens) == 0 {
		return "", false, 0
	}
	i := 0
	for i < len(tokens) && strings.Contains(tokens[i], "=") && !strings.HasPrefix(tokens[i], "-") {
		for dslCmd := range dslCommands {
			if strings.Contains(tokens[i], "=$("+dslCmd) {
				rest := strings.Join(tokens[i+1:], " ")
				for _, q := range []byte{'"', '\''} {
					count := strings.Count(rest, string(q))
					if count == 0 {
						continue
					}
					if count%2 == 0 {
						return dslCmd, true, 0
					}
					return dslCmd, true, q
				}
				return dslCmd, true, 0
			}
		}
		i++
	}
	if i >= len(tokens) {
		return "", false, 0
	}
	cmd := strings.TrimRight(tokens[i], ";")
	if !dslCommands[cmd] {
		return "", false, 0
	}
	rest := strings.Join(tokens[i+1:], " ")
	for _, q := range []byte{'"', '\''} {
		count := strings.Count(rest, string(q))
		if count == 0 {
			continue
		}
		if count%2 == 0 {
			return cmd, true, 0
		}
		return cmd, true, q
	}
	return cmd, true, 0
}

func isPlausibleCommand(token string) bool {
	if !hasLetterRegex.MatchString(token) {
		return false
	}
	if strings.HasPrefix(token, "\"") || strings.HasPrefix(token, "'") {
		return false
	}
	if strings.HasSuffix(token, ")") && !strings.Contains(token, "(") {
		return false
	}
	if strings.Contains(token, ".") || strings.Contains(token, "/") {
		return false
	}
	if token == strings.ToUpper(token) && token != strings.ToLower(token) {
		return false
	}
	if strings.HasPrefix(token, "**") || strings.HasPrefix(token, "---") {
		return false
	}
	return true
}

func shouldSkipInlineToken(token string) bool {
	if token == "" {
		return true
	}
	// Skip flags
	if strings.HasPrefix(token, "-") {
		return true
	}
	// Skip variables
	if strings.HasPrefix(token, "$") {
		return true
	}
	// Skip paths
	if strings.HasPrefix(token, ".") || strings.HasPrefix(token, "/") {
		return true
	}
	// Skip angle brackets and redirects
	if strings.HasPrefix(token, "<") || strings.HasPrefix(token, ">") {
		return true
	}
	// Skip brackets
	if strings.HasPrefix(token, "[") {
		return true
	}
	// Skip template variables
	if strings.HasPrefix(token, "{{") {
		return true
	}
	// Skip if contains . or / (filenames, paths)
	if strings.Contains(token, ".") || strings.Contains(token, "/") {
		return true
	}
	// Skip all-caps tokens
	if token == strings.ToUpper(token) && token != strings.ToLower(token) {
		return true
	}
	return false
}

func outputCommandPreflight(out io.Writer, description string) {
	cmds := extractCommands(description)
	if len(cmds) == 0 {
		return
	}

	var missing []string
	for _, cmd := range cmds {
		if _, err := exec.LookPath(cmd); err != nil {
			missing = append(missing, cmd)
		}
	}
	if len(missing) == 0 {
		return
	}

	fmt.Fprintln(out, "### Command Pre-flight")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "**WARNING — the following commands are referenced in this step but are NOT available on PATH:**")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "| Command | Status |")
	fmt.Fprintln(out, "|---------|--------|")
	for _, cmd := range missing {
		fmt.Fprintf(out, "| %s | NOT FOUND |\n", cmd)
	}
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "**Note:** The commands listed above were not found on PATH. This check is best-effort — the scanner may produce false positives for complex step instructions. Verify command availability with `which <command>` before proceeding.")
	fmt.Fprintln(out, "")
}

func outputStandingDirective(out io.Writer) {
	fmt.Fprintln(out, "### Command Verification")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Before executing any CLI command from the step instructions above:")
	fmt.Fprintln(out, "1. Verify the command exists: `which <command>` or `command -v <command>`")
	fmt.Fprintln(out, "2. If a command is not found: STOP. Do not skip, substitute, or continue. Report the missing command to the dispatcher via `af mail send`.")
	fmt.Fprintln(out, "")
}
