package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

// The mergepatrol formula is executable prose: each step's `description` is the literal
// text an LLM agent performs, so a wording regression is a behavioural regression with no
// compiler and no type system standing in the way. Design 651 moves the merge contract's
// enforcement out of that prose and into this file — security.md option SEC1 requires that
// "machine-checkable enforcement lives outside the formula text" — because the write path
// is live and autonomous: the formula was self-edited on 2026-08-25 with nothing in CI
// examining whether the merge semantics survived.
//
// The contract being frozen: GitHub's PR record is the SOLE authority for "merged". The
// landing is `gh pr merge` bound to the head that was tested, `gh pr view --json
// state,mergedAt` is the gate that licenses every destructive action after it, and the
// close verb is not a landing at all — 15 PRs in this repo's history are CLOSED with a
// null mergedAt because local git content state was treated as a second authority.
//
// Two constraints shape every pin below.
//
// Per ADR-018 these are static formula.ParseFile / os.ReadFile assertions: zero live gh,
// zero live git, zero subprocesses. Compliance is by construction, not by guarding.
//
// Every check is a free function over plain strings returning violations, so
// TestMergepatrolMergeSemantics_SelfNegative can prove it bites without a formula on disk.
// Each property below already holds with a 5-10x margin, so passing proves nothing on its
// own; a pin nobody has proven to discriminate is worse than no pin, because it consumes
// the reviewer's attention budget while permitting the regression. That is the same reason
// checkExecutableBranchLiterals is factored out in formula_literal_absence_test.go.

const (
	mergepatrolFormulaPath  = "install_formulas/mergepatrol.formula.toml"
	mergepatrolTemplatePath = "../templates/roles/mergepatrol.md.tmpl"
)

func loadMergepatrol(t *testing.T) *formula.Formula {
	t.Helper()
	f, err := formula.ParseFile(mergepatrolFormulaPath)
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}
	return f
}

// Not named stepByID: a local closure of that name already exists in this package at
// formula_ultrareview_force_review_pro_test.go:39, and a package-level twin would shadow
// it at a distance.
func mergepatrolStepDesc(t *testing.T, f *formula.Formula, id string) string {
	t.Helper()
	for i := range f.Steps {
		if f.Steps[i].ID == id {
			return f.Steps[i].Description
		}
	}
	t.Fatalf("no step with id %q found in mergepatrol formula", id)
	return ""
}

type mergepatrolSurface struct {
	label   string
	content string
}

// Every string surface of the formula an executing agent reads. The absence pins say
// "nowhere in the formula", so a banned command hiding in a step TITLE — as executable to an
// LLM reader as one in a body — must not slip through, and the formula-level description is
// 68 lines of prose that no f.Steps loop touches. Surfaces are swept rather than looked up
// by id so a step appended by a future self-edit is covered
// (design-doc.md:36 — "absence sweep across ALL steps, not per-step").
func mergepatrolSurfaces(f *formula.Formula) []mergepatrolSurface {
	surfaces := []mergepatrolSurface{{"formula description", f.Description}}
	for i := range f.Steps {
		surfaces = append(surfaces,
			mergepatrolSurface{fmt.Sprintf("step %q title", f.Steps[i].ID), f.Steps[i].Title},
			mergepatrolSurface{fmt.Sprintf("step %q", f.Steps[i].ID), f.Steps[i].Description},
		)
	}
	return surfaces
}

// Keyed on the close COMMAND, never on the word "close": the formula uses close/closed/
// closing as legitimate prose on 29 lines, and `gh pr list --state closed` is a
// load-bearing query in patrol-cleanup's closed-unmerged audit. A pin keyed on "clos"
// would false-fail on a clean tree. The REST shape is banned alongside the porcelain
// because PATCHing state=closed on the pulls endpoint is the same act under a different
// spelling, and banning only the porcelain would leave the plumbing open. The state VALUE is
// matched case-insensitively — the REST API accepts CLOSED, and a pin that only knew one
// casing would be defeated by the shift key.
var mergepatrolClosePatterns = []*regexp.Regexp{
	regexp.MustCompile(`gh\s+pr\s+close\b`),
	regexp.MustCompile(`gh\s+api\b.*\bpulls\b.*state=['"]?(?i:closed)`),
	regexp.MustCompile(`gh\s+api\b.*state=['"]?(?i:closed).*\bpulls\b`),
	regexp.MustCompile(`closePullRequest`),
}

func checkNoCloseCommand(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		for _, re := range mergepatrolClosePatterns {
			if re.MatchString(line) {
				violations = append(violations, fmt.Sprintf(
					"line %d: PR close command %q — mergepatrol never closes a PR; a merge is the only landing and an unrecoverable case escalates with a bead",
					i+1, strings.TrimSpace(line)))
				break
			}
		}
	}
	return violations
}

// The patrol-cleanup condition is an occurrence COUNT, not a Contains: cross-review H1
// requires mergedAt in BOTH the stale-mail and the orphan path, so a boolean would stay
// green all the way down to a single site. Today's margin is 10 — the margin is precisely
// what a future edit erodes.
func checkGitHubMergedAuthority(mergePush, patrolCleanup string) []string {
	var violations []string
	if !strings.Contains(mergePush, "gh pr merge") {
		violations = append(violations, "merge-push no longer lands the PR with `gh pr merge` — content would land without GitHub recording a merge")
	}
	if !strings.Contains(mergePush, "mergedAt") {
		violations = append(violations, "merge-push no longer reads `mergedAt` — nothing verifies that GitHub recorded the merge")
	}
	if !strings.Contains(mergePush, "--merge") {
		violations = append(violations, "merge-push lost the `--merge` conflict-fallback strategy — a conflicted PR would have no sanctioned landing")
	}
	if n := strings.Count(patrolCleanup, "mergedAt"); n < 2 {
		violations = append(violations, fmt.Sprintf(
			"patrol-cleanup references `mergedAt` %d time(s), need >= 2 — the stale-mail path and the orphan path must each verify against GitHub, so one site alone cannot satisfy this pin",
			n))
	}
	return violations
}

// Cross-review C1. The binding is asserted as ONE substring rather than as two
// independent presences: a formula carrying the flag on one line and the variable on an
// unrelated line satisfies "both present" while merging a head nobody tested. headRefOid
// is pinned absent because re-deriving the head at merge time is exactly the failure C1
// names, and it is keyed exactly — headRefName is a legitimate JSON selector elsewhere in
// the formula and a pin keyed on "headRef" would false-fail on three clean lines.
const mergepatrolTestedHeadBinding = `--match-head-commit "$TESTED_HEAD_OID"`

// A merge INVOCATION, as distinct from the narrative mention of "a non-zero `gh pr merge`
// exit". The distinction matters because the strategy-selection prose quotes the whole
// bound command, so whole-step presence checks are satisfied by prose alone — dropping the
// flag from the one line that actually runs would leave them all green. An invocation is
// recognised by its PR argument OR its strategy token, so renaming the placeholder does
// not hide an unbound merge. Verified against the formula: exactly three lines qualify and
// all three are real invocations; the narrative mention carries neither marker.
//
// The key errs toward over-matching: future prose that pairs a strategy token with
// `gh pr merge` on one line will be read as an invocation and required to carry the
// binding. Reword the prose — do not loosen the key, or the unbound-merge escape reopens.
func mergepatrolIsMergeInvocation(line string) bool {
	if !strings.Contains(line, "gh pr merge") {
		return false
	}
	return strings.Contains(line, "<pr-url>") ||
		strings.Contains(line, "<merge-strategy>") ||
		strings.Contains(line, "--rebase") ||
		strings.Contains(line, "--merge") ||
		strings.Contains(line, "--squash")
}

func checkTestedHeadBinding(processBranch, mergePush string) []string {
	var violations []string
	if !strings.Contains(mergePush, "--match-head-commit") {
		violations = append(violations, "merge-push dropped `--match-head-commit` — a head that moved after testing would merge untested commits")
	}
	if !strings.Contains(mergePush, "TESTED_HEAD_OID") {
		violations = append(violations, "merge-push no longer carries `TESTED_HEAD_OID` from process-branch")
	}
	if !strings.Contains(mergePush, mergepatrolTestedHeadBinding) {
		violations = append(violations, "merge-push does not bind `--match-head-commit` to \"$TESTED_HEAD_OID\" — the flag and the variable present separately is not a binding")
	}
	if !strings.Contains(processBranch, "TESTED_HEAD_OID=$(git rev-parse") {
		violations = append(violations, "process-branch no longer captures the tested head with `TESTED_HEAD_OID=$(git rev-parse ...)` — there is nothing for merge-push to bind to")
	}
	if strings.Contains(mergePush, "headRefOid") {
		violations = append(violations, "merge-push fetches `headRefOid` fresh at merge time — that re-derives a head which may have moved since the tests ran")
	}
	invocations := 0
	for i, line := range strings.Split(mergePush, "\n") {
		if !mergepatrolIsMergeInvocation(line) {
			continue
		}
		invocations++
		if !strings.Contains(line, mergepatrolTestedHeadBinding) {
			violations = append(violations, fmt.Sprintf(
				"line %d: merge invocation %q does not carry %s — this is a line that runs, and an unbound merge lands whatever the head happens to be now",
				i+1, strings.TrimSpace(line), mergepatrolTestedHeadBinding))
		}
	}
	if invocations == 0 {
		violations = append(violations, "merge-push contains no `gh pr merge` invocation — there is no landing left to bind")
	}
	return violations
}

// Cross-review C2. All three clauses are required: the push routes the fix to the head
// GitHub will merge, the re-entry re-tests it, and the explicit ban exists so this pin has
// something to grip — before Phase 1 the "fix it yourself" path was implicit, which is how
// an untested landing became reachable.
func checkNoLocalOnlyFix(handleFailures string) []string {
	var violations []string
	if !strings.Contains(handleFailures, "git push origin HEAD:<agent-branch>") {
		violations = append(violations, "handle-failures no longer pushes an authored fix to the PR head branch — the fix would exist only locally")
	}
	if !strings.Contains(handleFailures, "re-enter process-branch") {
		violations = append(violations, "handle-failures no longer re-enters process-branch after a fix — the fix would merge without being tested")
	}
	if !strings.Contains(handleFailures, "Committing a fix to `temp` only is BANNED.") {
		violations = append(violations, "handle-failures lost the explicit ban on committing a fix to `temp` only")
	}
	return violations
}

// AC-4, in two parts.
//
// The specified predicate is `Index(mergedAt) < Index(delete)`. strings.Index returns -1
// on absence, so written bare it is TRUE when the gate is deleted outright (-1 < n) and
// would green-light the single worst regression this file exists to catch. Both operands
// are therefore validated before they are compared.
//
// The bare token is also not the gate. merge-push's FIRST `mergedAt` is prose — Step 1's
// rationale that reachability is the condition under which GitHub sets it — roughly 4,500
// characters above the `gh pr view` call that actually reads it. A branch deletion parked
// anywhere in that window satisfies the specified predicate while still destroying the
// branch before anything has been verified, which is precisely the regression AC-4 names.
// The gate CALL is therefore compared as well. Both comparisons hold on the current
// formula, so this strengthens the pin without narrowing what it accepts today.
func checkRecoverabilityOrdering(mergePush string) []string {
	const gateToken, deleteToken = "mergedAt", "git push origin --delete"
	const gateCallToken = "gh pr view <pr-url> --json state,mergedAt"
	gate := strings.Index(mergePush, gateToken)
	call := strings.Index(mergePush, gateCallToken)
	del := strings.Index(mergePush, deleteToken)
	var violations []string
	if gate < 0 {
		violations = append(violations, "merge-push has no `mergedAt` gate — every destructive action after it would be unlicensed")
	}
	if call < 0 {
		violations = append(violations, "merge-push no longer reads the authority gate `gh pr view <pr-url> --json state,mergedAt` — a bare `mergedAt` mention in prose verifies nothing")
	}
	if del < 0 {
		violations = append(violations, "merge-push no longer deletes the remote branch — the ordering pin has lost its subject")
	}
	if gate >= 0 && del >= 0 && gate > del {
		violations = append(violations, fmt.Sprintf(
			"merge-push deletes the remote branch (index %d) before verifying `mergedAt` (index %d) — an unmerged PR would lose its branch irrecoverably",
			del, gate))
	}
	if call >= 0 && del >= 0 && call > del {
		violations = append(violations, fmt.Sprintf(
			"merge-push deletes the remote branch (index %d) before the authority gate `gh pr view <pr-url> --json state,mergedAt` runs (index %d) — the earlier `mergedAt` above it is prose, not a verification",
			del, call))
	}
	return violations
}

// security.md T4. A merge that branch protection refuses is an escalation, not a bypass.
func checkNoAdminMerge(content string) []string {
	if strings.Contains(content, "--admin") {
		return []string{"`--admin` bypasses branch protection — a refused merge must escalate, never be forced"}
	}
	return nil
}

// The landing push is what created two authorities for "merged": it put content on the
// default branch while GitHub still recorded the PR as unmerged. ParseFile is a plain
// toml.Decode and does not expand {{default_branch}}, so the literal token survives into
// the description and is what this pin grips.
//
// Any refspec landing on the default branch counts, not only the `temp:` spelling that was
// removed: pushing HEAD or any other local ref there restores the same two authorities
// under a different name. Verified absent from the current formula, which reaches the
// default branch only through `origin/{{default_branch}}` read paths.
//
// Cost of that breadth: prose carrying both "push" and a `:{{default_branch}}` refspec trips
// this even when it is read-only or a prohibition. Reword the prose — a narrower key would
// let the landing push return under a spelling nobody thought to enumerate.
func checkNoLandingPush(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "temp:{{default_branch}}") ||
			(strings.Contains(line, "push") && strings.Contains(line, ":{{default_branch}}")) {
			violations = append(violations, fmt.Sprintf(
				"line %d: %q pushes content to the default branch outside GitHub's merge — that is the two-authorities bug design 651 removed",
				i+1, strings.TrimSpace(line)))
		}
	}
	return violations
}

// Cross-review L3. Keyed on the checked box only: the formula ships eight legitimate
// unchecked `- [ ]` boxes, and a pattern broad enough to catch those would fail on a clean
// tree. A pre-checked gate is a gate the agent is told it has already passed.
func checkNoPrecheckedChecklists(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "[x]") || strings.Contains(line, "[X]") {
			violations = append(violations, fmt.Sprintf(
				"line %d: pre-checked box %q — a verification gate must ship unchecked",
				i+1, strings.TrimSpace(line)))
		}
	}
	return violations
}

// AC-2. Scoped by its caller to merge-push: `af bead create` occurs six times across five
// steps, so a whole-formula presence check could not fail even if the escalation record
// were deleted outright.
func checkEscalationInvestigationRecord(mergePush string) []string {
	if !strings.Contains(mergePush, "af bead create") {
		return []string{"merge-push escalation no longer files a bead — an unrecoverable merge would leave no investigation record, which is the silent close under another name"}
	}
	return nil
}

// The role template is the agent's identity artifact. If it does not state the same
// authority the formula enforces, the two drift and the agent is primed with the old
// contract.
func checkMergedAuthorityDocumented(template string) []string {
	if !strings.Contains(template, "mergedAt") {
		return []string{"role template does not document `mergedAt` as the authority for merged"}
	}
	return nil
}

func TestMergepatrolFormula_NoCloseCommand(t *testing.T) {
	f := loadMergepatrol(t)
	for _, s := range mergepatrolSurfaces(f) {
		for _, v := range checkNoCloseCommand(s.content) {
			t.Errorf("%s: %s", s.label, v)
		}
	}
}

func TestMergepatrolFormula_GitHubMergedAuthority(t *testing.T) {
	f := loadMergepatrol(t)
	for _, v := range checkGitHubMergedAuthority(
		mergepatrolStepDesc(t, f, "merge-push"),
		mergepatrolStepDesc(t, f, "patrol-cleanup"),
	) {
		t.Error(v)
	}
}

func TestMergepatrolFormula_TestedHeadBinding(t *testing.T) {
	f := loadMergepatrol(t)
	for _, v := range checkTestedHeadBinding(
		mergepatrolStepDesc(t, f, "process-branch"),
		mergepatrolStepDesc(t, f, "merge-push"),
	) {
		t.Error(v)
	}
}

func TestMergepatrolFormula_NoLocalOnlyFix(t *testing.T) {
	f := loadMergepatrol(t)
	for _, v := range checkNoLocalOnlyFix(mergepatrolStepDesc(t, f, "handle-failures")) {
		t.Error(v)
	}
}

func TestMergepatrolFormula_RecoverabilityOrdering(t *testing.T) {
	f := loadMergepatrol(t)
	for _, v := range checkRecoverabilityOrdering(mergepatrolStepDesc(t, f, "merge-push")) {
		t.Error(v)
	}
}

func TestMergepatrolFormula_NoAdminMerge(t *testing.T) {
	f := loadMergepatrol(t)
	for _, s := range mergepatrolSurfaces(f) {
		for _, v := range checkNoAdminMerge(s.content) {
			t.Errorf("%s: %s", s.label, v)
		}
	}
}

func TestMergepatrolFormula_NoLandingPush(t *testing.T) {
	f := loadMergepatrol(t)
	for _, s := range mergepatrolSurfaces(f) {
		for _, v := range checkNoLandingPush(s.content) {
			t.Errorf("%s: %s", s.label, v)
		}
	}
}

func TestMergepatrolFormula_NoPrecheckedChecklists(t *testing.T) {
	f := loadMergepatrol(t)
	for _, s := range mergepatrolSurfaces(f) {
		for _, v := range checkNoPrecheckedChecklists(s.content) {
			t.Errorf("%s: %s", s.label, v)
		}
	}
}

func TestMergepatrolFormula_EscalationInvestigationRecord(t *testing.T) {
	f := loadMergepatrol(t)
	for _, v := range checkEscalationInvestigationRecord(mergepatrolStepDesc(t, f, "merge-push")) {
		t.Error(v)
	}
}

// EXPECTED RED until Phase 3 regenerates the role template from the v6 formula: the file
// on disk is still the v5 artifact and contains no `mergedAt`. All three phases ship in
// one PR, so CI only ever sees the green end state. Do not skip, soften or delete this pin
// to obtain a green package — no commit in this repo's history has relaxed a pin instead
// of landing the change the pin demands.
func TestMergepatrolTemplate_MergedAuthorityDocumented(t *testing.T) {
	b, err := os.ReadFile(mergepatrolTemplatePath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	for _, v := range checkMergedAuthorityDocumented(string(b)) {
		t.Error(v)
	}
}

type mergeSemanticsCase struct {
	name        string
	check       func(string) []string
	mustFlag    []string
	mustNotFlag []string
}

// Known-clean fixtures, used both as the mustNotFlag baseline and as the fixed argument
// when a two-argument check is adapted to the single-string shape the table drives.
const (
	goodMergePush = "Land the PR through GitHub, then verify GitHub recorded it as merged.\n" +
		"On a clean rebase substitute `--rebase`; when the rebase conflicts substitute `--merge`.\n" +
		"TESTED_HEAD_OID=<the SHA process-branch recorded>\n" +
		"gh pr merge <pr-url> <merge-strategy> --match-head-commit \"$TESTED_HEAD_OID\"\n" +
		"gh pr view <pr-url> --json state,mergedAt\n" +
		"Require BOTH: `state` is MERGED and `mergedAt` is non-null.\n" +
		"If GitHub never reports merged: af bead create --type task --priority 1\n" +
		"git push origin --delete <agent-branch>"

	goodPatrolCleanup = "Stale MERGE_READY mail: gh pr view <pr-url> --json state,mergedAt — delete only when mergedAt is non-null.\n" +
		"Orphan PR: gh pr view <pr-url> --json state,mergedAt — a null mergedAt keeps the work item alive."

	goodProcessBranch = "TESTED_HEAD_OID=$(git rev-parse origin/<agent-branch>)\n" +
		"echo \"tested head: $TESTED_HEAD_OID\""

	goodHandleFailures = "Author the fix, then push it where GitHub will merge it:\n" +
		"git push origin HEAD:<agent-branch>\n" +
		"Then re-enter process-branch: re-run it from Step 1 against the new head.\n" +
		"**Committing a fix to `temp` only is BANNED.** It is not a shortcut; it is an untested landing."
)

// TestMergepatrolMergeSemantics_SelfNegative proves the ten checks above are NOT vacuous:
// every defective fixture MUST be flagged, and every known-legitimate residual MUST NOT
// be. If this regresses, the green pins above are worthless. Mirrors
// TestBranchLiteralLintSelfNegative (formula_literal_absence_test.go:121-166).
func TestMergepatrolMergeSemantics_SelfNegative(t *testing.T) {
	cases := []mergeSemanticsCase{
		{
			name:  "NoCloseCommand",
			check: checkNoCloseCommand,
			mustFlag: []string{
				"gh pr close <pr-url>",
				"gh pr close 645 --comment \"content already landed on the default branch\"",
				"gh api repos/{owner}/{repo}/pulls/645 -X PATCH -f state=closed",
				"gh api --method PATCH /repos/OWNER/REPO/pulls/645 -f state=closed",
				// state=closed BEFORE the path, so the reversed-order pattern is the only
				// one that can catch it — without this fixture a third of the close
				// surface would be unproven.
				"gh api -X PATCH -f state=closed repos/o/r/pulls/645",
				"gh api repos/o/r/pulls/645 -X PATCH -f state='closed'",
				// The REST API accepts the uppercase state too, so the case-insensitive
				// value match needs its own fixture or it is unproven.
				"gh api repos/o/r/pulls/645 -X PATCH -f state=CLOSED",
				"gh api graphql -f query='mutation { closePullRequest(input: {pullRequestId: $id}) { clientMutationId } }'",
			},
			mustNotFlag: []string{
				"mergepatrol never closes PRs — unrecoverable cases are escalated with a bead.",
				"NEVER close a skipped step with a bare `af done`.",
				"gh pr list --state closed --search \"closed:>=2026-08-18 -is:merged\" --json number,url,closedAt,mergedAt --limit 100",
				"`--state closed` INCLUDES merged PRs, so the query MUST exclude them with `-is:merged`",
				"Content would land while the PR reads \"closed with unmerged commits\".",
				"Step 4: Audit for closed-unmerged PRs",
				"gh pr view <pr-url> --json state,mergedAt",
				"af mail delete <message-id>",
				"Close this task when done.",
				goodMergePush,
			},
		},
		{
			name:  "GitHubMergedAuthority/merge-push",
			check: func(s string) []string { return checkGitHubMergedAuthority(s, goodPatrolCleanup) },
			mustFlag: []string{
				// Isolates the `gh pr merge` clause: mergedAt and --merge are both present,
				// so only the missing landing command can be what fires.
				"Land it, then verify.\nOn a conflict use --merge.\ngh pr view <pr-url> --json state,mergedAt",
				"gh pr merge <pr-url> --merge --match-head-commit \"$TESTED_HEAD_OID\"",
				"gh pr merge <pr-url> --rebase --match-head-commit \"$TESTED_HEAD_OID\"\ngh pr view <pr-url> --json state,mergedAt",
			},
			mustNotFlag: []string{goodMergePush},
		},
		{
			name:  "GitHubMergedAuthority/patrol-cleanup",
			check: func(s string) []string { return checkGitHubMergedAuthority(goodMergePush, s) },
			mustFlag: []string{
				"Delete the stale MERGE_READY mail once the PR looks merged.",
				"Stale mail: gh pr view <pr-url> --json state,mergedAt — delete when merged.\nOrphan PR: drop the work item if the branch is gone.",
			},
			mustNotFlag: []string{goodPatrolCleanup},
		},
		{
			name:  "TestedHeadBinding/merge-push",
			check: func(s string) []string { return checkTestedHeadBinding(goodProcessBranch, s) },
			mustFlag: []string{
				"gh pr merge <pr-url> --rebase",
				// --squash is a merge strategy this formula does not use today; the key
				// still has to recognise it, or a future unbound squash-merge walks in.
				"gh pr merge <pr-url> --squash",
				"gh pr merge <pr-url> --rebase --match-head-commit \"$(git rev-parse origin/<agent-branch>)\"",
				"TESTED_HEAD_OID=<the SHA process-branch recorded>\ngh pr merge <pr-url> --rebase",
				"TESTED_HEAD_OID was recorded earlier.\ngh pr merge <pr-url> --rebase --match-head-commit \"$HEAD_OID\"",
				goodMergePush + "\nHEAD_OID=$(gh pr view <pr-url> --json headRefOid -q .headRefOid)",
				// The binding survives in the strategy-selection prose while the line that
				// actually runs has lost it. Every whole-step presence check passes here;
				// only the per-invocation sweep catches it.
				"On a clean rebase use `gh pr merge <pr-url> --rebase --match-head-commit \"$TESTED_HEAD_OID\"`.\n" +
					"gh pr merge <pr-url> <merge-strategy>",
				// No invocation left at all: nothing to bind, so presence of the tokens in
				// prose must not be mistaken for a bound landing.
				"TESTED_HEAD_OID=<the SHA process-branch recorded>\n" +
					"Substitute `--match-head-commit \"$TESTED_HEAD_OID\"` when you land it.",
				// Unbound executed line spelling its argument differently: recognised as an
				// invocation by its strategy token rather than by the <pr-url> placeholder.
				"On a clean rebase use `gh pr merge <pr-url> --rebase --match-head-commit \"$TESTED_HEAD_OID\"`.\n" +
					"gh pr merge <pr-number> <merge-strategy>",
				"On a clean rebase use `gh pr merge <pr-url> --rebase --match-head-commit \"$TESTED_HEAD_OID\"`.\n" +
					"gh pr merge \"$PR_URL\" --rebase",
			},
			mustNotFlag: []string{
				goodMergePush,
				goodMergePush + "\ngh pr list --json number,title,url,headRefName,baseRefName,isCrossRepository",
			},
		},
		{
			name:  "TestedHeadBinding/process-branch",
			check: func(s string) []string { return checkTestedHeadBinding(s, goodMergePush) },
			mustFlag: []string{
				"Build temp from the PR head and run the suite.",
				"Record TESTED_HEAD_OID somewhere before merging.",
			},
			mustNotFlag: []string{goodProcessBranch},
		},
		{
			name:  "NoLocalOnlyFix",
			check: checkNoLocalOnlyFix,
			mustFlag: []string{
				"Fix it yourself, commit the fix to temp, and proceed to merge-push.",
				"git push origin HEAD:<agent-branch>\nThen re-enter process-branch: re-run it from Step 1.",
				"git push origin HEAD:<agent-branch>\n**Committing a fix to `temp` only is BANNED.**",
				"Then re-enter process-branch: re-run it from Step 1.\n**Committing a fix to `temp` only is BANNED.**",
			},
			mustNotFlag: []string{
				goodHandleFailures,
				goodHandleFailures + "\ngit branch -D fixwork",
			},
		},
		{
			name:  "RecoverabilityOrdering",
			check: checkRecoverabilityOrdering,
			mustFlag: []string{
				// The gate deleted outright. This is the fixture the design's literal
				// `Index(gate) < Index(delete)` predicate would have let through.
				"git push origin --delete <agent-branch>",
				"gh pr view <pr-url> --json state,mergedAt\nRequire state MERGED.",
				"git push origin --delete <agent-branch>\nThen confirm mergedAt is non-null.",
				// The deletion parked between the prose mention of `mergedAt` and the gate
				// that actually reads it. This satisfies the design's literal
				// Index(mergedAt) < Index(delete) predicate and is exactly the AC-4
				// regression, so only the gate-call comparison can catch it.
				"Reachability is the one condition under which GitHub sets `mergedAt`.\n" +
					"gh pr merge <pr-url> <merge-strategy> --match-head-commit \"$TESTED_HEAD_OID\"\n" +
					"git push origin --delete <agent-branch>\n" +
					"gh pr view <pr-url> --json state,mergedAt",
			},
			mustNotFlag: []string{
				goodMergePush,
				"gh pr view <pr-url> --json state,mergedAt\ngit push origin --delete <agent-branch>",
			},
		},
		{
			name:  "NoAdminMerge",
			check: checkNoAdminMerge,
			mustFlag: []string{
				"gh pr merge <pr-url> --rebase --admin --match-head-commit \"$TESTED_HEAD_OID\"",
				"If branch protection refuses, retry with `gh pr merge --admin`.",
			},
			mustNotFlag: []string{
				goodMergePush,
				"If branch protection refuses the merge, escalate to the operator (a repo admin) rather than bypassing it.",
			},
		},
		{
			name:  "NoLandingPush",
			check: checkNoLandingPush,
			mustFlag: []string{
				"git push origin temp:{{default_branch}}",
				"git push --force-with-lease origin temp:{{default_branch}}",
				// The same landing under a different refspec spelling.
				"git push origin HEAD:{{default_branch}}",
			},
			mustNotFlag: []string{
				goodMergePush,
				"git branch -D temp",
				"git checkout --detach origin/{{default_branch}}",
				"git checkout -b temp origin/{{default_branch}}",
			},
		},
		{
			name:  "NoPrecheckedChecklists",
			check: checkNoPrecheckedChecklists,
			mustFlag: []string{
				"- [x] `mergedAt` non-null verified via gh",
				"- [X] Branch deleted from origin",
			},
			mustNotFlag: []string{
				"- [ ] `mergedAt` non-null verified via gh (state MERGED — nothing below is licensed without it)",
				"- [ ] Branch deleted from origin (ONLY after mergedAt verified)",
				goodMergePush,
			},
		},
		{
			name:  "EscalationInvestigationRecord",
			check: checkEscalationInvestigationRecord,
			mustFlag: []string{
				"If GitHub never reports merged, leave the PR open and move on to the next branch.",
				"gh pr view <pr-url> --json state,mergedAt",
			},
			mustNotFlag: []string{
				goodMergePush,
				"af bead create --type task --priority 1 \\",
			},
		},
		{
			name:  "MergedAuthorityDocumented",
			check: checkMergedAuthorityDocumented,
			mustFlag: []string{
				"no associated agent, the label is removed and the PR is closed without MERGED mail.",
				"mergepatrol lands PRs that carry the merge_ready label.",
			},
			mustNotFlag: []string{
				"Landing is complete only when `gh pr view --json state,mergedAt` reports MERGED with a non-null mergedAt.",
			},
		},
	}

	for _, tc := range cases {
		for _, s := range tc.mustFlag {
			if v := tc.check(s); len(v) == 0 {
				t.Errorf("%s: self-negative bite failed: check did NOT flag %q (the check is vacuous)", tc.name, s)
			}
		}
		for _, s := range tc.mustNotFlag {
			if v := tc.check(s); len(v) > 0 {
				t.Errorf("%s: false-positive: check should not flag legitimate text %q, got %v", tc.name, s, v)
			}
		}
	}
}
