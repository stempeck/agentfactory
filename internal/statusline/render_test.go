package statusline

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// defaultCfg is the real default render order, DERIVED from the config package rather than
// re-declared here. The previous hand-copy had drifted twice over (it claimed seven elements
// while the shipped default was six, and it listed `daily` which that default excluded), which
// is precisely the drift the exported accessor exists to stop.
func defaultCfg() *config.StatuslineConfig {
	return &config.StatuslineConfig{Elements: config.DefaultStatuslineElements()}
}

func loadGoldenPayload(t *testing.T) Payload {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "payload", "full_2_1_212.json"))
	if err != nil {
		t.Fatalf("open golden fixture: %v", err)
	}
	defer f.Close()
	p, err := ParsePayload(f)
	if err != nil {
		t.Fatalf("ParsePayload golden fixture: %v", err)
	}
	return p
}

func TestRender_GoldenFixture2_1_212(t *testing.T) {
	p := loadGoldenPayload(t)
	// issue #600 (PR #601): the daily/session token halves are RESTORED, now sourced from the
	// truthful cumulative counter (K2) rather than the occupancy the #595 narrowing dropped. This
	// golden is the plain (Color:false) byte-pin of layout + figures; the SGR is pinned separately by
	// the grammar tests (TestRenderColorOnlyOwnPaletteSGR / TestRenderStripSGREqualsPlain) so a
	// palette tweak does not churn this string. AC-7: the #595 cost-only pin EVOLVES, it does not
	// survive. Session tokens come from RenderOpts.SessionTokens; daily tokens from DailyTotals.Tokens.
	daily := DailyTotals{CostUSD: 80.64, Tokens: 1400000, Sessions: 1}

	got := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000})

	// Line 1 ends in elapsed. PR #601 T6: the requirement holder made screenshot parity BINDING, so
	// elapsed now renders in the #591 screenshot format `T <H>h <MM>m` / (`<1h => T <N>m`) — here the
	// golden's 125000 ms (2m5s) renders `T 2m` (minutes only, seconds dropped; ux.md C2.1), NOT the
	// prior Go-duration `T 2m5s`. Line 2 carries both halves: `$ 12.34 · 487k tok` and `D $ 80.64 · 1.4M tok`.
	want := "Opus 4.8 | /home/dev/project/root | af/soldesign | +210 -47 | T 2m\n" +
		"████░░░░░░ 35% (352k/1.0M) | $ 12.34 · 487k tok | D $ 80.64 · 1.4M tok"

	if got != want {
		t.Errorf("golden render mismatch:\n got: %q\nwant: %q", got, want)
	}

	// dir must be project_dir, NOT current_dir — proves the field naming (cross-review L1).
	if !strings.Contains(got, "/home/dev/project/root") {
		t.Errorf("dir did not resolve to workspace.project_dir; got %q", got)
	}
	if strings.Contains(got, "/home/dev/project/current") {
		t.Errorf("dir wrongly used workspace.current_dir; got %q", got)
	}
	// The plain (Color:false) render carries no ESC byte — colour is asserted by the grammar tests.
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("plain render (Color:false) contains an ESC byte: %q", got)
	}
}

func TestRender_RedirectCostPrefix(t *testing.T) {
	p := loadGoldenPayload(t)
	daily := DailyTotals{CostUSD: 80.64, Sessions: 1}

	got := Render(defaultCfg(), p, "af/soldesign", daily, true)

	if !strings.Contains(got, "~$ 12.34") {
		t.Errorf("redirect session cost missing ~ prefix; got %q", got)
	}
	if !strings.Contains(got, "D ~$ 80.64") {
		t.Errorf("redirect daily cost missing ~ prefix; got %q", got)
	}
}

// goldenStruct returns the golden payload as a struct so individual fields can be zeroed
// to exercise per-element fail-open without hand-editing JSON.
func goldenStruct() Payload {
	pct := 35.2
	var p Payload
	p.SessionID = "sess-Golden_01"
	p.Cwd = "/home/dev/fallback/cwd"
	p.Model.DisplayName = "Opus 4.8"
	p.Workspace.ProjectDir = "/home/dev/project/root"
	p.Workspace.CurrentDir = "/home/dev/project/current"
	p.ContextWindow.TotalInputTokens = 340000
	p.ContextWindow.TotalOutputTokens = 12000
	p.ContextWindow.ContextWindowSize = 1000000
	p.ContextWindow.UsedPercentage = &pct
	p.Cost.TotalCostUSD = 12.34
	p.Cost.TotalDurationMS = 125000
	p.Cost.TotalLinesAdded = 210
	p.Cost.TotalLinesRemoved = 47
	return p
}

func assertNoOrphanSeparators(t *testing.T, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "|") || strings.HasSuffix(line, "|") {
			t.Errorf("orphan separator at line edge: %q", line)
		}
		if strings.Contains(line, "|  |") || strings.Contains(line, " |  ") {
			t.Errorf("adjacent/empty separator in line: %q", line)
		}
	}
}

func TestRender_PerElementFailOpen(t *testing.T) {
	daily := DailyTotals{CostUSD: 80.64, Sessions: 1}
	branch := "af/soldesign"

	// Baseline: everything present ⇒ all elements render.
	base := Render(defaultCfg(), goldenStruct(), branch, daily, false)
	for _, want := range []string{"Opus 4.8", "/home/dev/project/root", "af/soldesign", "+210 -47", "35%", "$ 12.34", "D $ 80.64"} {
		if !strings.Contains(base, want) {
			t.Fatalf("baseline missing %q in %q", want, base)
		}
	}

	cases := []struct {
		name    string
		mutate  func(*Payload)
		branch  string
		daily   DailyTotals
		absent  string // substring that must NOT appear
		present string // a sibling that must still appear (fail-open is per element)
	}{
		{"model dropped", func(p *Payload) { p.Model.DisplayName = "" }, branch, daily, "Opus 4.8", "af/soldesign"},
		{"dir dropped when all sources empty", func(p *Payload) { p.Workspace.ProjectDir = ""; p.Workspace.CurrentDir = ""; p.Cwd = "" }, branch, daily, "/home/dev/project", "af/soldesign"},
		{"branch dropped", func(p *Payload) {}, "", daily, "af/soldesign", "Opus 4.8"},
		{"context dropped when used_percentage null", func(p *Payload) { p.ContextWindow.UsedPercentage = nil }, branch, daily, "35%", "$ 12.34"},
		{"session/diff/elapsed dropped when cost family zero", func(p *Payload) { p.Cost = Payload{}.Cost }, branch, daily, "$ 12.34", "af/soldesign"},
		{"daily dropped when zero", func(p *Payload) {}, branch, DailyTotals{}, "D $", "$ 12.34"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goldenStruct()
			tc.mutate(&p)
			out := Render(defaultCfg(), p, tc.branch, tc.daily, false)
			if strings.Contains(out, tc.absent) {
				t.Errorf("expected %q to be dropped, got %q", tc.absent, out)
			}
			if tc.present != "" && !strings.Contains(out, tc.present) {
				t.Errorf("expected sibling %q to remain, got %q", tc.present, out)
			}
			assertNoOrphanSeparators(t, out)
		})
	}

	// Zero cost must never render "$0.00" as fact (Gap 12).
	p := goldenStruct()
	p.Cost = Payload{}.Cost
	out := Render(defaultCfg(), p, branch, DailyTotals{}, false)
	if strings.Contains(out, "$0.00") || strings.Contains(out, "$ 0.00") {
		t.Errorf("rendered $0.00 as fact: %q", out)
	}
}

func TestRender_HostileStringsSanitized(t *testing.T) {
	p := goldenStruct()
	p.Model.DisplayName = "Op\x1b[31mus\x07 4.8"                // CSI color + BEL
	p.Workspace.ProjectDir = "/home/\x1b]0;pwn\x07dev/p\x7froj" // OSC title-set + DEL
	branch := "ma\x1b[2Jin\x00\xc2\x9b"                         // CSI clear + NUL + valid C1 CSI (U+009B)

	out := Render(defaultCfg(), p, branch, DailyTotals{CostUSD: 1}, false)

	// No control RUNE survives — C0 (< 0x20), DEL, ESC, AND the C1 set (U+0080–U+009F,
	// which includes the 8-bit CSI/OSC introducers). The line separator "\n" is checked
	// per-line so it is not itself flagged.
	for _, line := range strings.Split(out, "\n") {
		for _, r := range line {
			if isControlRune(r) {
				t.Fatalf("control rune U+%04X survived sanitization in line %q", r, line)
			}
		}
	}
	// The visible letters survive; the escape machinery does not.
	if !strings.Contains(out, "main") {
		t.Errorf("expected sanitized branch to read 'main', got %q", out)
	}

	// Length cap with middle-ellipsis on a very long path.
	long := "/" + strings.Repeat("abcdefghij/", 20) + "end"
	p2 := goldenStruct()
	p2.Workspace.ProjectDir = long
	out2 := Render(defaultCfg(), p2, "b", DailyTotals{}, false)
	if !strings.Contains(out2, "…") {
		t.Errorf("expected middle-ellipsis on an over-long path, got %q", out2)
	}
	if strings.Contains(out2, long) {
		t.Errorf("over-long path was not capped: %q", out2)
	}
}

// watchdogNeedles is a FROZEN local copy of internal/cmd/watchdog.go's
// endpointFailureSignatures + the thinking-block needle. The statusline library cannot
// import internal/cmd (purity AC-4), so a copy is the only option; this test is the guard
// that the renderer never prints one of these substrings and respawns a healthy agent (Gap 3).
var watchdogNeedles = []string{
	"502 Bad Gateway",
	"503 Service Unavailable",
	"504 Gateway Timeout",
	"connection refused",
	"connection timed out",
	"unsupported_api_for_model",
	"Invalid model name",
	"litellm.InternalServerError",
	"litellm.ServiceUnavailableError",
	"litellm.APIConnectionError",
	"litellm.Timeout",
	"Invalid signature in thinking block",
}

func TestRender_NoWatchdogNeedlesEverEmitted(t *testing.T) {
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	// Fault injection surfaces: malformed stdin, a broken config, an unreadable store.
	badPayload, err := ParsePayload(strings.NewReader("{not valid json"))
	if err == nil {
		t.Fatalf("expected malformed stdin to error")
	}
	brokenCfg := &config.StatuslineConfig{Elements: []string{"model", "bogus-element", "daily"}}
	missingStore := filepath.Join(t.TempDir(), "does-not-exist")
	daily := SumDaily(missingStore, now) // ENOENT store ⇒ zero totals, no error

	outputs := []string{
		Render(brokenCfg, badPayload, "", daily, false),
		Render(nil, goldenStruct(), "", DailyTotals{}, false),
		Render(defaultCfg(), badPayload, "", daily, true),
	}
	for i, out := range outputs {
		for _, needle := range watchdogNeedles {
			if strings.Contains(out, needle) {
				t.Errorf("output[%d] emitted watchdog needle %q: %q", i, needle, out)
			}
		}
	}
}

func TestRender_Budget(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	// 500 distinct fresh sessions, seeded through the real write path.
	for i := 0; i < 500; i++ {
		var sp Payload
		sp.SessionID = "sess" + strconv.Itoa(i)
		sp.Cost.TotalCostUSD = float64(i) * 0.01
		sp.ContextWindow.TotalInputTokens = int64(i) * 10
		sp.ContextWindow.TotalOutputTokens = int64(i) * 2
		if err := WriteSnapshot(dir, sp, "manager", now); err != nil {
			t.Fatalf("seed snapshot %d: %v", i, err)
		}
	}

	p := loadGoldenPayload(t)

	start := time.Now()
	daily := SumDaily(dir, now)
	_ = Render(defaultCfg(), p, "af/soldesign", daily, false)
	elapsed := time.Since(start)

	if daily.Sessions != 500 {
		t.Errorf("expected 500 sessions summed, got %d", daily.Sessions)
	}
	// p95 < 50ms target; assert the generous hard ceiling to stay CI-stable.
	if elapsed > 200*time.Millisecond {
		t.Errorf("budget exceeded: Render+SumDaily over 500 snapshots took %v (ceiling 200ms)", elapsed)
	}
	t.Logf("Render+SumDaily over 500 snapshots: %v", elapsed)
}
