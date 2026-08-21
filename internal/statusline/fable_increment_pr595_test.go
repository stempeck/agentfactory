package statusline

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// Pinning tests for the unresolved review comments of PR #595 (fable-increment).
// Each test names the thread it pins. RED tests fail against the PR-head code and must pass
// after the fix; protective tests pass now and must keep passing.

// T1/F1 — re-grounded for issue #600 (PR #601): the token halves are RESTORED, but sourced from the
// truthful cumulative COUNTER, never from context-window occupancy. This scenario writes only
// occupancy-shaped snapshots (WriteSnapshot, no transcript accumulator), so the counter is zero and
// SumDaily's clamped Σ max(0, cum−baseline) is zero — the token halves correctly DROP (per-half H-R2),
// and the negative "· -550000 tok" the #595 narrowing produced from occupancy can never appear. The
// assertion is not "tokens never render" (they do, from the counter) but "occupancy is never rendered
// as a spend token, and a daily token figure is never negative".
func TestFableIncr_T1_CompactionNeverRendersSpendTokens(t *testing.T) {
	store := t.TempDir()
	aug1 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	aug2 := time.Date(2026, 8, 2, 0, 5, 0, 0, time.UTC)

	// A manager at 900k ctx / $5.00 before midnight...
	var p1 Payload
	p1.SessionID = "mgr"
	p1.Cost.TotalCostUSD = 5.00
	p1.ContextWindow.TotalInputTokens = 890000
	p1.ContextWindow.TotalOutputTokens = 10000
	if err := WriteSnapshot(store, p1, "manager", aug1); err != nil {
		t.Fatalf("aug1 write: %v", err)
	}

	// ...compacted to 350k after midnight, cost risen to $5.40 (the session's own rollover sets
	// baseline := 900k, so today's token delta = 350k − 900k = −550000).
	var p2 Payload
	p2.SessionID = "mgr"
	p2.Cost.TotalCostUSD = 5.40
	p2.ContextWindow.TotalInputTokens = 340000
	p2.ContextWindow.TotalOutputTokens = 10000
	usedPct := 35.0
	p2.ContextWindow.UsedPercentage = &usedPct
	p2.ContextWindow.ContextWindowSize = 1000000
	if err := WriteSnapshot(store, p2, "manager", aug2); err != nil {
		t.Fatalf("aug2 write: %v", err)
	}

	daily := SumDaily(store, aug2)
	out := Render(defaultCfg(), p2, "main", daily, false)

	// The defect the #595 narrowing removed: a spend-labeled token figure built from occupancy,
	// negative on daily after compaction. Under #600 the token halves render from the truthful
	// counter — zero here (occupancy-only snapshots), so they drop; occupancy is never labeled tok.
	if strings.Contains(out, " tok") {
		t.Errorf("T1: session/daily must not render a spend-labeled token figure built from context occupancy; got %q", out)
	}
	if strings.Contains(out, "-550000") || strings.Contains(out, "· -") {
		t.Errorf("T1: daily must never show a NEGATIVE token count; got %q", out)
	}
	// Protective: cost is the reliable quantity and must survive — daily $0.40, session $5.40.
	if !strings.Contains(out, "0.40") || !strings.Contains(out, "5.40") {
		t.Errorf("T1 guard: cost figures must remain (daily 0.40, session 5.40); got %q", out)
	}
	// STAYS guard (T1-C): the context bar uses the SAME occupancy fields LEGITIMATELY and must
	// keep rendering — occupancy is correct there, only its reuse as "spend" is the defect.
	if !strings.Contains(out, "35%") {
		t.Errorf("T1 guard: the context occupancy bar must remain untouched; got %q", out)
	}
}

// T2/F9 (Consider) — a detached HEAD makes `git rev-parse --abbrev-ref HEAD` print the literal
// token "HEAD", which ReadBranch surfaces verbatim, contradicting its own degrade-to-empty
// contract. The shipped test used a NON-repo (git errored → "") so it never reached this path.
// RED today: ReadBranch returns "HEAD".
func TestFableIncr_T2_DetachedHeadReturnsEmpty(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)

	run := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = repo
		out, err := c.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	sha := run("rev-parse", "HEAD")
	// Detach HEAD onto the raw commit — the real trigger (tag/SHA checkout, rebase, bisect).
	cmd := exec.Command("git", "-c", "advice.detachedHead=false", "checkout", sha)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout %s (detach): %v\n%s", sha, err, out)
	}

	if got := ReadBranch(repo); got == "HEAD" || got == sha {
		t.Errorf("T2: ReadBranch on a detached HEAD must degrade to '' (or a short SHA), never the literal git sentinel; got %q", got)
	}
}

// T3/F7 (Consider) — the budget test omitted the write-heavy hot-path steps. Production runs
// WriteSnapshot→Prune→SumDaily→Render every render; on the first render after local midnight
// Prune rewrites every stale foreign file (atomic temp+rename). This exercises that spike with
// a many-stale-files midnight case inside the timed region. Deliverable/coverage test: it PASSES
// now (a generous, logged ceiling) — the ask was to add the exercise, not fix a code defect.
func TestFableIncr_T3_BudgetIncludesWriteAndPruneMidnight(t *testing.T) {
	store := t.TempDir()
	yesterday := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	afterMidnight := time.Date(2026, 8, 2, 0, 5, 0, 0, time.UTC)

	// Seed many stale (yesterday-dated) foreign sessions that Prune must roll over at midnight.
	for i := 0; i < 200; i++ {
		var sp Payload
		sp.SessionID = "stale" + strconv.Itoa(i)
		sp.Cost.TotalCostUSD = float64(i) * 0.01
		sp.ContextWindow.TotalInputTokens = int64(i) * 10
		if err := WriteSnapshot(store, sp, "manager", yesterday); err != nil {
			t.Fatalf("seed stale %d: %v", i, err)
		}
	}

	var p Payload
	p.SessionID = "live"
	p.Cost.TotalCostUSD = 1.23
	p.ContextWindow.TotalInputTokens = 5000
	usedPct := 10.0
	p.ContextWindow.UsedPercentage = &usedPct
	p.ContextWindow.ContextWindowSize = 200000

	// Time the FULL production hot-path order at the first post-midnight render.
	start := time.Now()
	if err := WriteSnapshot(store, p, "manager", afterMidnight); err != nil {
		t.Fatalf("hot write: %v", err)
	}
	Prune(store, afterMidnight)
	daily := SumDaily(store, afterMidnight)
	_ = Render(defaultCfg(), p, "main", daily, false)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("budget: write+prune+sum+render over a 200-stale-file midnight rollover took %v (generous ceiling 500ms)", elapsed)
	}
	t.Logf("T3 write-heavy midnight hot path (200 stale files): %v", elapsed)
}

// BODY-1/F2 INVERTED (issue #600, K9 ledger item 4). This used to pin the opposite property:
// that agent A's default render was INVARIANT to agent B's spend, because `daily` had been
// dropped from the default set to stop it resetting the watchdog's silence hash.
//
// #600 restored `daily` to the default, so the render now moves with cross-session spend — and
// that movement is the REQUIREMENT, not a defect: a daily total that does not reflect the day's
// actual spend is simply wrong. The masking guarantee this test used to carry has relocated to
// the watchdog, which strips sentinel-marked statusline lines before hashing;
// TestCheckSilence_StatuslineOnlyChange_StillTrips (internal/cmd/watchdog_test.go) proves a
// statusline-only pane change — including a cross-session daily move — still trips silence
// (integration.md:106-109: "the watchdog test — not render invariance — carries the C-1
// guarantee").
//
// Inverted here rather than in Phase 3 because the flip is caused by Phase 1's default change
// and the replacement guarantee ships in Phase 1 too; deferring would mean knowingly shipping a
// red suite.
//
// The original byte rigor is preserved rather than weakened to "before != after": that alone
// would pass even if the render broke for some unrelated reason. The assertion is that the
// difference is EXACTLY the daily token.
func TestFableIncr_BODY1_DefaultRenderVariesWithCrossSessionSpend(t *testing.T) {
	store := t.TempDir()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

	cfg, err := config.LoadStatuslineConfig(t.TempDir()) // no statusline.json ⇒ the real default set
	if err != nil {
		t.Fatalf("load default config: %v", err)
	}

	var a Payload
	a.SessionID = "agentA"
	a.Cost.TotalCostUSD = 1.00
	a.ContextWindow.TotalInputTokens = 5000
	usedPct := 12.0
	a.ContextWindow.UsedPercentage = &usedPct
	a.ContextWindow.ContextWindowSize = 200000
	if err := WriteSnapshot(store, a, "manager", now); err != nil {
		t.Fatalf("write A: %v", err)
	}

	before := Render(cfg, a, "main", SumDaily(store, now), false)

	// Another agent spends — the shared factory daily total ticks up.
	var b Payload
	b.SessionID = "agentB"
	b.Cost.TotalCostUSD = 7.77
	b.ContextWindow.TotalInputTokens = 123456
	if err := WriteSnapshot(store, b, "manager", now); err != nil {
		t.Fatalf("write B: %v", err)
	}

	after := Render(cfg, a, "main", SumDaily(store, now), false)

	if before == after {
		t.Errorf("BODY-1 inverted: agent A's DEFAULT statusline must reflect the factory-wide daily total, so it must change when agent B spends; got %q both times", before)
	}

	// Byte rigor preserved: the daily token is the ONLY thing another session's spend may move.
	trimDaily := func(s string) string {
		i := strings.LastIndex(s, " | D ")
		if i < 0 {
			t.Fatalf("expected a daily token in the default render; got %q", s)
		}
		return s[:i]
	}
	if trimDaily(before) != trimDaily(after) {
		t.Errorf("only the daily token may move with another session's spend:\nbefore=%q\nafter =%q", before, after)
	}
}

// T5/F6 (render side) — decisions.md D6 keeps render.go's lineOf map + renderElement switch
// per-element (a new element there is genuine code, not a name copy), so they can't be data-driven
// from config's canonical list. This guard closes the residual drift the review flagged: EVERY
// valid element name (config.StatuslineElementNames — the single source consolidated in D6) must be
// handled by BOTH lineOf and renderElement, so adding a whitelist element without wiring the
// renderer fails CI instead of silently dropping the element (PR #595 T5/F6).
func TestFableIncr_T5_EveryValidElementRenders(t *testing.T) {
	// A fully-populated payload so no element fail-opens for a missing source.
	var p Payload
	p.Model.DisplayName = "M"
	p.Workspace.ProjectDir = "/d"
	p.Cost.TotalCostUSD = 1.23
	p.Cost.TotalLinesAdded = 3
	p.Cost.TotalLinesRemoved = 1
	p.Cost.TotalDurationMS = 65000
	usedPct := 25.0
	p.ContextWindow.UsedPercentage = &usedPct
	p.ContextWindow.ContextWindowSize = 200000
	p.ContextWindow.TotalInputTokens = 5000
	daily := DailyTotals{CostUSD: 2.0, Sessions: 1}

	for _, el := range config.StatuslineElementNames() {
		if _, ok := lineOf[el]; !ok {
			t.Errorf("T5: valid element %q has no lineOf entry — renderer drift from the config whitelist", el)
		}
		if _, ok := renderElement(el, p, "main", daily, RenderOpts{}); !ok {
			t.Errorf("T5: valid element %q is not handled by renderElement (ok=false with a full payload) — renderer drift", el)
		}
	}
}
