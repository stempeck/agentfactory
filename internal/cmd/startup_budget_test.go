//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/mail"
	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/templates"
)

// The budgeted inputs a session start reads, by the name the checker reports. The UserPromptSubmit
// mail block is a second copy of the mail input: a nudge or an initial prompt fires it on top of
// the SessionStart one (D-20).
const (
	inputRoleClaudeMd      = "role_claude_md"
	inputPlainPrime        = "plain_prime"
	inputUsingAgentfactory = "using_agentfactory"
	inputMemoryInjection   = "memory_injection"
	inputMailInjection     = "mail_injection"
	inputMemoryList        = "memory_list"
	inputAgentsMd          = "agents_md"
	inputPrimeHook         = "prime_hook"
	inputPromptMail        = "mail_injection_user_prompt"
)

var startupBudgetedInputs = []string{
	inputRoleClaudeMd, inputPlainPrime, inputUsingAgentfactory, inputMemoryInjection,
	inputMailInjection, inputMemoryList, inputAgentsMd, inputPrimeHook,
}

var startupBudgetRoles = []string{"manager", "supervisor"}

const (
	startupVaultN   = 60
	startupVault10N = 10 * startupVaultN
)

func startupCeilings() map[string]int {
	return map[string]int{
		inputRoleClaudeMd:      startupRoleClaudeMdBytes,
		inputPlainPrime:        startupPlainPrimeBytes,
		inputUsingAgentfactory: startupUsingAgentfactoryBytes,
		inputMemoryInjection:   startupMemoryInjectionBytes,
		inputMailInjection:     startupMailInjectionBytes,
		inputMemoryList:        startupMemoryListBytes,
		inputAgentsMd:          startupAgentsMdBytes,
		inputPrimeHook:         startupPrimeHookBytes,
		inputPromptMail:        startupMailInjectionBytes,
	}
}

type startupMode struct {
	name   string
	inputs []string
	sum    int
}

func startupModes() []startupMode {
	withPromptMail := append(append([]string{}, startupBudgetedInputs...), inputPromptMail)
	return []startupMode{
		{"nudge", withPromptMail, startupSumNudgeBytes},
		{"initial_prompt", withPromptMail, startupSumInitialPromptBytes},
		{"compaction", append([]string{}, startupBudgetedInputs...), startupSumCompactionBytes},
	}
}

// startupInputsOver is the one checker (D-21): every input whose rendered size passes its ceiling,
// and every input that has no ceiling at all, sorted.
func startupInputsOver(rendered, ceilings map[string]int) []string {
	var over []string
	for name, size := range rendered {
		if ceiling, ok := ceilings[name]; !ok || size > ceiling {
			over = append(over, name)
		}
	}
	sort.Strings(over)
	return over
}

func startupCeilingSum(inputs []string, ceilings map[string]int) int {
	sum := 0
	for _, name := range inputs {
		sum += ceilings[name]
	}
	return sum
}

// startupTracked holds the two tracked files the budget renders from. They are read relative to
// the package directory, so they are read before any subtest moves the working directory.
type startupTracked struct {
	agentsJSON []byte
	manual     []byte
}

func readStartupTracked(t *testing.T) startupTracked {
	t.Helper()
	agentsJSON, err := os.ReadFile(filepath.Join("..", "..", ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading the tracked agents.json: %v", err)
	}
	manual, err := os.ReadFile(filepath.Join("..", "..", "USING_AGENTFACTORY.md"))
	if err != nil {
		t.Fatalf("reading the tracked USING_AGENTFACTORY.md: %v", err)
	}
	return startupTracked{agentsJSON: agentsJSON, manual: manual}
}

// startupVaultPins is the pinned half of every budget vault: the ceiling fixture, whose block is
// exactly the 3,072-byte standing ceiling.
func startupVaultPins() []memory.Note { return pinNotes(ceilingPins(false)) }

// startupFactory is a temporary factory carrying the tracked roster, with a manager and a
// supervisor directory and a vault of vaultNotes notes for each: the ceiling pins and enough
// long-bodied newer notes to fill the rest.
func startupFactory(t *testing.T, tracked startupTracked, vaultNotes int) string {
	t.Helper()
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	for _, role := range startupBudgetRoles {
		if err := os.MkdirAll(filepath.Join(afDir, "agents", role), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFixtureFile(t, filepath.Join(afDir, "factory.json"), `{}`)
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"), tracked.agentsJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	pins := startupVaultPins()
	for _, role := range startupBudgetRoles {
		for _, p := range pins {
			seedNote(t, root, role, p)
		}
		for i := 0; i < vaultNotes-len(pins); i++ {
			seedNote(t, root, role, memory.Note{
				ID:      fmt.Sprintf("startup-note-%04d", i),
				Type:    "ops",
				Created: now.Add(-time.Duration(i+1) * time.Minute),
				Body: "# " + paddedSubject(fmt.Sprintf("startup vault learning %04d", i), 200) + "\n\n" +
					strings.Repeat("a recorded observation that runs long ", 20) + "\n",
			})
		}
	}
	return root
}

func budgetAgentDir(root, role string) string {
	return filepath.Join(root, ".agentfactory", "agents", role)
}

func startupRosterEntry(t *testing.T, root, role string) config.AgentEntry {
	t.Helper()
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		t.Fatalf("loading the roster: %v", err)
	}
	entry, ok := agents.Agents[role]
	if !ok {
		t.Fatalf("the tracked roster has no %q entry", role)
	}
	return entry
}

func renderStartupRoleFile(t *testing.T, root, role string) []byte {
	t.Helper()
	content, err := templates.RenderIdentity(templates.New(), role, startupRosterEntry(t, root, role),
		"/factory", "/factory/.agentfactory/agents/"+role)
	if err != nil {
		t.Fatalf("RenderIdentity(%s): %v", role, err)
	}
	return content
}

// renderStartupPlainPrime is a plain, unslimmed `af prime` of the role, called directly so the
// roster entry is the tracked one (the manager is interactive) rather than a fixture's.
func renderStartupPlainPrime(t *testing.T, root, role string) []byte {
	t.Helper()
	installMemStore(t)
	agentDir := budgetAgentDir(root, role)
	t.Chdir(agentDir)
	t.Setenv("AF_ROLE", "")
	primeHookMode = false
	var buf bytes.Buffer
	if _, err := primeAgent(t.Context(), &buf, root, role, agentDir); err != nil {
		t.Fatalf("primeAgent(%s): %v", role, err)
	}
	return buf.Bytes()
}

func renderStartupAgentsMd(t *testing.T, root string) []byte {
	t.Helper()
	if err := writeAgentsMd(root); err != nil {
		t.Fatalf("writeAgentsMd: %v", err)
	}
	data, err := os.ReadFile(config.AgentsMdPath(root))
	if err != nil {
		t.Fatalf("AGENTS.md was not written: %v", err)
	}
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		t.Fatalf("loading the roster: %v", err)
	}
	rows := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "| `") {
			rows++
		}
	}
	if rows != len(agents.Agents) {
		t.Fatalf("AGENTS.md carries %d roster rows, the roster has %d agents", rows, len(agents.Agents))
	}
	return data
}

func renderStartupMemoryInjection(t *testing.T, root, role string, now time.Time) []byte {
	t.Helper()
	notes, err := memory.List(root, role, memory.Filter{})
	if err != nil {
		t.Fatalf("listing %s's vault: %v", role, err)
	}
	b := memory.DefaultBudget()
	b.Now = now
	served := memory.Slice(notes, b)
	var buf bytes.Buffer
	renderMemoryInjection(&buf, served, memoryOverflow(notes, served, b), len(memory.Pins(notes, now)), now)
	return buf.Bytes()
}

// startupMailbox holds more full-length messages than one block can carry, so the block renders
// at its largest.
func startupMailbox(now time.Time) []*mail.Message {
	var msgs []*mail.Message
	for i := 0; i < 2*mailInjectK; i++ {
		msgs = append(msgs, &mail.Message{
			ID:        fmt.Sprintf("startup-mail-%02d", i),
			From:      "supervisor",
			To:        "manager",
			Subject:   paddedSubject(fmt.Sprintf("startup mail %02d", i), 64),
			Body:      strings.Repeat("m", 2*mailInjectExcerptChars),
			Timestamp: now.Add(-time.Duration(i+1) * time.Minute),
			Priority:  issuestore.PriorityNormal,
		})
	}
	return msgs
}

func renderStartupMail(t *testing.T, role string, now time.Time) []byte {
	t.Helper()
	served, overflow := selectMailForInjection(startupMailbox(now), now)
	var buf bytes.Buffer
	renderMailInjection(&buf, role, served, overflow, 0, now)
	if !strings.Contains(buf.String(), "…and ") {
		t.Fatalf("the mailbox fixture no longer overflows one block, so the mail input is not at its largest:\n%s", buf.String())
	}
	return buf.Bytes()
}

func renderStartupMemoryList(t *testing.T, root, role string) []byte {
	t.Helper()
	t.Chdir(budgetAgentDir(root, role))
	t.Setenv("AF_ROLE", "")
	out, err := execMemoryOut(t, "list")
	if err != nil {
		t.Fatalf("memory list as %s: %v", role, err)
	}
	return []byte(out)
}

func renderStartupPrimeHook(t *testing.T, root, role string) []byte {
	t.Helper()
	installMemStore(t)
	t.Chdir(budgetAgentDir(root, role))
	t.Setenv("AF_ROLE", "")
	stdout := primeHookCapturing(t, `{"session_id":"sess-budget-`+role+`","source":"startup"}`)
	if len(stdout) == 0 {
		t.Fatalf("af prime --hook as %s emitted nothing", role)
	}
	return []byte(stdout)
}

// startupRendered is every budgeted input of one role at one vault size, rendered, plus the two
// vault-dependent texts the saturation checks read.
type startupRendered struct {
	sizes     map[string]int
	list      string
	injection string
}

func renderStartupInputs(t *testing.T, tracked startupTracked, root, role string) startupRendered {
	t.Helper()
	now := time.Now().UTC()
	list := renderStartupMemoryList(t, root, role)
	injection := renderStartupMemoryInjection(t, root, role, now)
	mailBlock := renderStartupMail(t, role, now)
	return startupRendered{
		sizes: map[string]int{
			inputRoleClaudeMd:      len(renderStartupRoleFile(t, root, role)),
			inputPlainPrime:        len(renderStartupPlainPrime(t, root, role)),
			inputUsingAgentfactory: len(tracked.manual),
			inputMemoryInjection:   len(injection),
			inputMailInjection:     len(mailBlock),
			inputMemoryList:        len(list),
			inputAgentsMd:          len(renderStartupAgentsMd(t, root)),
			inputPrimeHook:         len(renderStartupPrimeHook(t, root, role)),
			inputPromptMail:        len(renderStartupMail(t, role, now)),
		},
		list:      string(list),
		injection: string(injection),
	}
}

var (
	startupListOverflowLine      = regexp.MustCompile(`\n… and \d+ more \(--all-rows, --type, --formula\)\n$`)
	startupInjectionOverflowLine = regexp.MustCompile("\n…and \\d+ more — `af memory list --type <type>` or `--formula <name>`\n")
)

// assertStartupVaultSaturates fails a case whose vault no longer fills the list, the newest-notes
// block and the pinned block, because a fixture that stopped saturating would pass under every
// ceiling while measuring nothing.
func assertStartupVaultSaturates(t *testing.T, r startupRendered) {
	t.Helper()
	if !startupListOverflowLine.MatchString(r.list) {
		t.Errorf("the list text does not end in its `… and N more` line, so the vault does not fill the list:\n%s", lastBytes(r.list, 400))
	}
	if !startupInjectionOverflowLine.MatchString(r.injection) {
		t.Errorf("the injection carries no `…and N more` line, so the vault does not fill the newest-notes block:\n%s", r.injection)
	}
	pins := startupVaultPins()
	if got := memory.PinnedBlockBytes(pins, 0); got > memory.DefaultStandingBytes {
		t.Errorf("the fixture's pinned block is %d B, over the %d B standing ceiling", got, memory.DefaultStandingBytes)
	}
	extra := pins[len(pins)-1]
	extra.ID = "one-pin-more"
	if got := memory.PinnedBlockBytes(append(append([]memory.Note{}, pins...), extra), 0); got <= memory.DefaultStandingBytes {
		t.Errorf("one more pin of the fixture's size makes a %d B block, within the %d B standing ceiling: the pins are not at the ceiling",
			got, memory.DefaultStandingBytes)
	}
}

func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// startupHookCeilings maps each command a settings template runs at SessionStart or on
// UserPromptSubmit to the ceiling that budgets it. A command with no positive entry is unbudgeted.
func startupHookCeilings() map[string]int {
	return map[string]int{
		"af prime --hook":          startupPrimeHookBytes,
		"af mail check --inject":   startupMailInjectionBytes,
		"af memory check --inject": startupMemoryInjectionBytes,
	}
}

func unbudgetedHookCommands(cmds []string) []string {
	ceilings := startupHookCeilings()
	var unbudgeted []string
	for _, c := range cmds {
		if ceiling, ok := ceilings[c]; !ok || ceiling <= 0 {
			unbudgeted = append(unbudgeted, c)
		}
	}
	return unbudgeted
}

func userPromptSubmitCommands(t *testing.T, settings []byte, source string) []string {
	t.Helper()
	var cmds []string
	for _, group := range hookCommandGroups(t, settings, "UserPromptSubmit", source) {
		for _, c := range group {
			if _, after, found := strings.Cut(c, " && "); found {
				c = after
			}
			cmds = append(cmds, strings.TrimSpace(c))
		}
	}
	return cmds
}

func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

// withExtraSessionStartCommand is a copy of settings whose first SessionStart group runs one more
// command.
func withExtraSessionStartCommand(t *testing.T, settings []byte, command string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(settings, &doc); err != nil {
		t.Fatalf("decoding settings: %v", err)
	}
	hooks, ok := doc["hooks"].(map[string]any)
	if !ok {
		t.Fatal("settings carry no hooks object")
	}
	groups, ok := hooks["SessionStart"].([]any)
	if !ok || len(groups) == 0 {
		t.Fatal("settings carry no SessionStart group")
	}
	group, ok := groups[0].(map[string]any)
	if !ok {
		t.Fatal("the first SessionStart group is not an object")
	}
	entries, ok := group["hooks"].([]any)
	if !ok {
		t.Fatal("the first SessionStart group carries no hooks list")
	}
	group["hooks"] = append(entries, map[string]any{"type": "command", "command": sessionStartHookPrefix + command})
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encoding settings: %v", err)
	}
	return out
}

func TestManagerStartupInputsWithinBudget(t *testing.T) {
	tracked := readStartupTracked(t)
	ceilings := startupCeilings()
	vaults := []struct {
		name  string
		notes int
	}{{"N", startupVaultN}, {"10N", startupVault10N}}
	roots := map[string]string{}
	for _, v := range vaults {
		roots[v.name] = startupFactory(t, tracked, v.notes)
	}

	for _, role := range startupBudgetRoles {
		t.Run(role, func(t *testing.T) {
			for _, mode := range startupModes() {
				t.Run(mode.name, func(t *testing.T) {
					for _, v := range vaults {
						t.Run(v.name, func(t *testing.T) {
							r := renderStartupInputs(t, tracked, roots[v.name], role)
							assertStartupVaultSaturates(t, r)

							received := map[string]int{}
							for _, name := range mode.inputs {
								received[name] = r.sizes[name]
							}
							if over := startupInputsOver(received, ceilings); len(over) != 0 {
								for _, name := range over {
									t.Errorf("%s is %d B, over its %d B ceiling", name, received[name], ceilings[name])
								}
							}
							sum := startupCeilingSum(mode.inputs, ceilings)
							t.Logf("%s %s at %s notes: the ceilings of the inputs it receives sum to %d B", role, mode.name, v.name, sum)
							if sum != mode.sum {
								t.Errorf("the ceilings %s receives sum to %d B, the mode's constant is %d B", mode.name, sum, mode.sum)
							}
						})
					}
				})
			}
		})
	}

	t.Run("over_ceiling", func(t *testing.T) {
		base := renderStartupInputs(t, tracked, roots["N"], "manager").sizes
		for _, input := range startupBudgetedInputs {
			t.Run(input, func(t *testing.T) {
				padded := map[string]int{}
				for _, name := range startupBudgetedInputs {
					padded[name] = base[name]
				}
				padded[input] = ceilings[input] + 1
				got := startupInputsOver(padded, ceilings)
				if len(got) != 1 || got[0] != input {
					t.Errorf("with %s padded to its ceiling + 1 (%d B) the checker names %v, want exactly [%s]",
						input, padded[input], got, input)
				}
			})
		}
	})

	t.Run("hook_entries", func(t *testing.T) {
		wantSessionStart := sortedCopy([]string{"af prime --hook", "af mail check --inject", "af memory check --inject"})
		for _, tc := range []struct {
			name string
			role claude.RoleType
		}{{"interactive", claude.Interactive}, {"autonomous", claude.Autonomous}} {
			t.Run(tc.name, func(t *testing.T) {
				settings := canonicalSettings(t, tc.role)
				sessionStart := sessionStartCommands(t, settings, tc.name)
				if got := sortedCopy(sessionStart); strings.Join(got, "\n") != strings.Join(wantSessionStart, "\n") {
					t.Errorf("SessionStart runs %q, want exactly %q", got, wantSessionStart)
				}
				ceilings := startupHookCeilings()
				for cmd, want := range map[string]int{
					"af prime --hook":          1024,
					"af mail check --inject":   4608,
					"af memory check --inject": 4736,
				} {
					if ceilings[cmd] != want {
						t.Errorf("%s is budgeted at %d B, want %d B", cmd, ceilings[cmd], want)
					}
				}
				if got := unbudgetedHookCommands(sessionStart); len(got) != 0 {
					t.Errorf("SessionStart entries with no budget constant: %q", got)
				}
				prompt := userPromptSubmitCommands(t, settings, tc.name)
				if len(prompt) != 1 || prompt[0] != "af mail check --inject" {
					t.Errorf("UserPromptSubmit runs %q, want exactly [af mail check --inject]", prompt)
				}
				if got := unbudgetedHookCommands(prompt); len(got) != 0 {
					t.Errorf("UserPromptSubmit entries with no budget constant: %q", got)
				}
			})
		}
		t.Run("unbudgeted_entry_is_red", func(t *testing.T) {
			const fake = "af roster check --inject"
			settings := withExtraSessionStartCommand(t, canonicalSettings(t, claude.Interactive), fake)
			got := unbudgetedHookCommands(sessionStartCommands(t, settings, "settings with an added entry"))
			if len(got) != 1 || got[0] != fake {
				t.Errorf("the mapping reports %q as unbudgeted, want exactly [%s]", got, fake)
			}
		})
	})

	t.Run("pinned_block", func(t *testing.T) {
		pins := ceilingPins(false)
		for _, role := range startupBudgetRoles {
			t.Run(role, func(t *testing.T) {
				for _, src := range []string{"startup", "resume", "clear", "compact"} {
					t.Run(src, func(t *testing.T) {
						t.Setenv("AF_ROLE", "")
						payload := `{"session_id":"s-` + src + `","source":"` + src + `","hook_event_name":"SessionStart"}`
						block := checkInject(t, budgetAgentDir(roots["N"], role), payload)
						if memory.PinnedBlockHeader == "" || !strings.Contains(block, memory.PinnedBlockHeader) {
							t.Errorf("the %s injection lacks memory.PinnedBlockHeader %q:\n%s", src, memory.PinnedBlockHeader, block)
						}
						for _, p := range pins {
							if line := memory.PinnedLine(p.note); line == "" || !strings.Contains(block, line) {
								t.Errorf("the %s injection lacks pin %s's PinnedLine %q", src, p.note.ID, line)
							}
						}
						if !strings.Contains(block, pinnedBlockText(pins)) {
							t.Errorf("the %s injection does not carry the whole pinned block:\n--- want ---\n%s\n--- got ---\n%s",
								src, pinnedBlockText(pins), block)
						}
					})
				}
			})
		}
	})
}

func TestManagerRoleTemplateWithinCeiling(t *testing.T) {
	tracked := readStartupTracked(t)
	root := startupFactory(t, tracked, 0)
	for _, role := range startupBudgetRoles {
		if got := len(renderStartupRoleFile(t, root, role)); got > startupRoleClaudeMdBytes {
			t.Errorf("the %s role file renders %d B, over its %d B ceiling", role, got, startupRoleClaudeMdBytes)
		}
	}
}

func TestPlainPrimeWithinCeiling(t *testing.T) {
	tracked := readStartupTracked(t)
	root := startupFactory(t, tracked, 0)
	for _, role := range startupBudgetRoles {
		t.Run(role, func(t *testing.T) {
			if got := len(renderStartupPlainPrime(t, root, role)); got > startupPlainPrimeBytes {
				t.Errorf("a plain af prime as %s is %d B, over its %d B ceiling", role, got, startupPlainPrimeBytes)
			}
		})
	}
}

func TestUsingAgentfactoryWithinCeiling(t *testing.T) {
	tracked := readStartupTracked(t)
	if got := len(tracked.manual); got > startupUsingAgentfactoryBytes {
		t.Errorf("USING_AGENTFACTORY.md is %d B, over its %d B ceiling", got, startupUsingAgentfactoryBytes)
	}
}

func TestAgentsMdWithinCeiling(t *testing.T) {
	tracked := readStartupTracked(t)
	root := startupFactory(t, tracked, 0)
	if got := len(renderStartupAgentsMd(t, root)); got > startupAgentsMdBytes {
		t.Errorf("AGENTS.md rendered from the tracked roster is %d B, over its %d B ceiling", got, startupAgentsMdBytes)
	}
}

func TestStartupBudgetConstantsArePinned(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"startupRoleClaudeMdBytes", startupRoleClaudeMdBytes, 16384},
		{"startupPlainPrimeBytes", startupPlainPrimeBytes, 16384},
		{"startupUsingAgentfactoryBytes", startupUsingAgentfactoryBytes, 65536},
		{"startupMemoryInjectionBytes", startupMemoryInjectionBytes, 4736},
		{"startupMailInjectionBytes", startupMailInjectionBytes, 4608},
		{"startupMemoryListBytes", startupMemoryListBytes, 8192},
		{"startupAgentsMdBytes", startupAgentsMdBytes, 16384},
		{"startupPrimeHookBytes", startupPrimeHookBytes, 1024},
		{"memoryInjectFrameBytes", memoryInjectFrameBytes, 640},
		{"startupTurnBudgetTokens", startupTurnBudgetTokens, 90000},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}

	literals := map[string]int{"nudge": 137856, "initial_prompt": 137856, "compaction": 133248}
	ceilings := startupCeilings()
	for _, mode := range startupModes() {
		sum := startupCeilingSum(mode.inputs, ceilings)
		if mode.sum != literals[mode.name] {
			t.Errorf("the %s sum constant is %d, want %d", mode.name, mode.sum, literals[mode.name])
		}
		if sum != mode.sum {
			t.Errorf("the ceilings %s receives sum to %d, its constant says %d", mode.name, sum, mode.sum)
		}
	}
}
