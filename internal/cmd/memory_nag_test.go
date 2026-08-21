package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/session"
)

// ------------------------------------------------------------------ fixtures

// nagMail is one captured send. The seam is captured rather than short-circuited on a binary
// check for the reason containment.go:411-435 gives: a test that let the real sender run would
// spawn the MCP store, and a sender that no-ops under test would leave the production branch
// unexercised.
type nagMail struct{ root, recipient, subject, body string }

func captureNagMail(t *testing.T) *[]nagMail {
	t.Helper()
	var sent []nagMail
	orig := sendMemoryNagMail
	sendMemoryNagMail = func(root, recipient, subject, body string) error {
		sent = append(sent, nagMail{root, recipient, subject, body})
		return nil
	}
	t.Cleanup(func() { sendMemoryNagMail = orig })
	return &sent
}

func failNagMail(t *testing.T, err error) *int {
	t.Helper()
	attempts := 0
	orig := sendMemoryNagMail
	sendMemoryNagMail = func(string, string, string, string) error {
		attempts++
		return err
	}
	t.Cleanup(func() { sendMemoryNagMail = orig })
	return &attempts
}

// asOperator is the identity every hygiene-pass test that is not about authority runs under: no
// AF_ROLE, no tmux. It is spelled out per test rather than folded into a fixture because
// callerAuthority is fail-closed on ABSENCE, and a helper that set it silently would hide which
// tier the assertion below actually holds for.
func asOperator(t *testing.T) {
	t.Helper()
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
}

// asTmuxSession puts the process inside a named tmux session, seen through the newCmdTmux seam.
//
// It swaps the seam directly rather than calling setupHermeticSessions, which renames the session
// prefix to "af-test-" — under that prefix isAfProductionSession is false, so the caller
// classifies as an OPERATOR and a carve-out test would pass without the carve-out existing. Here
// the production prefix is what makes callerAuthority say Agent, which is the whole premise.
func asTmuxSession(t *testing.T, name string) {
	t.Helper()
	fake := newFakeTmux()
	fake.currentSession = name
	orig := newCmdTmux
	newCmdTmux = func() cmdTmux { return fake }
	t.Cleanup(func() { newCmdTmux = orig })
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "1")
}

// writeStoreFormula plants a store formula and returns its full-hex sha256. The path is composed
// by hand rather than through config.FormulaStorePath for the reason that constructor's own
// doc comment gives: an expectation derived from the constructor cannot detect a fault inside it.
func writeStoreFormula(t *testing.T, root, name, body string) string {
	t.Helper()
	path := filepath.Join(root, ".agentfactory", "store", "formulas", name+".formula.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, path, body)
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// seedDueNote plants a note the graduation clock has run out on: active, no TTL, older than
// memory.GraduationDueAfter.
func seedDueNote(t *testing.T, root, agent, id, body string) string {
	t.Helper()
	return seedNote(t, root, agent, memory.Note{
		ID:      id,
		Type:    memory.TypeImprovement,
		Body:    body,
		Created: time.Now().UTC().Add(-memory.GraduationDueAfter - 24*time.Hour),
	})
}

func nagStateOf(t *testing.T, root, agent string) memoryNagState {
	t.Helper()
	data, err := os.ReadFile(memoryNagStatePath(root, agent))
	if err != nil {
		t.Fatalf("reading %s's nag state: %v", agent, err)
	}
	var st memoryNagState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decoding %s's nag state: %v", agent, err)
	}
	return st
}

// rewindNagDay moves the recorded day into the past without touching the report hash, which is
// what isolates the day clause of the cap from the report-identity clause.
func rewindNagDay(t *testing.T, root, agent string) {
	t.Helper()
	st := nagStateOf(t, root, agent)
	st.Day = "2000-01-01"
	if err := saveMemoryNagState(root, agent, st); err != nil {
		t.Fatalf("rewinding %s's nag day: %v", agent, err)
	}
}

// rewindNagSentAt ages the recorded send without touching the day or the report hash, which is
// what isolates the snooze window from the other two clauses.
func rewindNagSentAt(t *testing.T, root, agent string, by time.Duration) {
	t.Helper()
	st := nagStateOf(t, root, agent)
	sent, err := time.Parse(time.RFC3339, st.SentAt)
	if err != nil {
		t.Fatalf("the recorded send time must be RFC3339: %q (%v)", st.SentAt, err)
	}
	st.Day = "2000-01-01"
	st.SentAt = sent.Add(-by).Format(time.RFC3339)
	if err := saveMemoryNagState(root, agent, st); err != nil {
		t.Fatalf("ageing %s's nag state: %v", agent, err)
	}
}

func mailFor(t *testing.T, sent []nagMail, agent string) nagMail {
	t.Helper()
	for _, m := range sent {
		if m.recipient == agent {
			return m
		}
	}
	t.Fatalf("no hygiene mail was sent to %s (sent: %d)", agent, len(sent))
	return nagMail{}
}

// ------------------------------------------------------------------ the pass runs

// TestMemoryNag_RunsTheHygienePassForAnOperator is the original RED test for #515 Phase 5: with
// the Phase-2 stub in place `af memory status --nag` errored under EVERY identity, so this failed
// on the verb's own behaviour rather than on a missing symbol.
func TestMemoryNag_RunsTheHygienePassForAnOperator(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	captureNagMail(t)
	seedDueNote(t, factoryRoot, "alice", "grad-due-1", "a durable learning that should have become a formula edit")
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("the hygiene pass must run for an operator: %v", err)
	}
}

// TestMemoryNag_HealthyVaultIsSilent pins the contract that makes a per-cycle invocation
// affordable. The pass runs every dispatch interval into .runtime/dispatch.log, which nothing
// rotates: one line per cycle is ~1700 lines a day saying nothing happened.
func TestMemoryNag_HealthyVaultIsSilent(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	seedNote(t, factoryRoot, "alice", memory.Note{
		ID:      "fresh-1",
		Type:    memory.TypeGotcha,
		Body:    "a recent learning that is on a TTL and owes nobody an answer",
		Expires: time.Now().UTC().Add(memory.TTLGotcha),
	})
	t.Chdir(factoryRoot)
	asOperator(t)

	out, err := execMemoryOut(t, "status", "--nag")
	if err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if out != "" {
		t.Errorf("a healthy vault must produce no output; got %q", out)
	}
	if len(*sent) != 0 {
		t.Errorf("a healthy vault must send nothing; got %d mail(s)", len(*sent))
	}
	if _, err := os.Stat(memoryNagStatePath(factoryRoot, "alice")); !os.IsNotExist(err) {
		t.Error("nothing was sent, so no day-cap record should exist")
	}
}

// TestMemoryNag_GraduationDueNotesAreNamed covers the half of TTLImprovement = 0 that had nothing
// behind it before this phase: an improvement note is "nagged rather than quietly expired"
// (slice.go), and until now nothing nagged.
func TestMemoryNag_GraduationDueNotesAreNamed(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	due := seedDueNote(t, factoryRoot, "alice", "due-1", "the retry loop needs a jittered backoff")
	// A note still inside the window is the control: without it, "reports due notes" would be
	// satisfied by a pass that reported every note it saw.
	young := seedNote(t, factoryRoot, "alice", memory.Note{
		ID:      "young-1",
		Type:    memory.TypeImprovement,
		Body:    "recorded this morning, nobody owes an answer yet",
		Created: time.Now().UTC().Add(-24 * time.Hour),
	})
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	m := mailFor(t, *sent, "alice")
	if !strings.Contains(m.body, due) {
		t.Errorf("the due note %q is not named in the nag body:\n%s", due, m.body)
	}
	if strings.Contains(m.body, young) {
		t.Errorf("a note inside the graduation window must not be nagged:\n%s", m.body)
	}
	if !strings.HasPrefix(m.subject, "MEMORY_HYGIENE: ") {
		t.Errorf("subject must carry the house label; got %q", m.subject)
	}
	if m.recipient != "alice" {
		t.Errorf("the nag is self-addressed to the vault's owner; got %q", m.recipient)
	}
}

// TestMemoryNag_BodyCarriesIdsNotNoteText pins scale.md:191. A nag that quoted note bodies would
// make the mailbox a second copy of the vault — the unbounded-context failure the mail subsystem
// already paid for once.
func TestMemoryNag_BodyCarriesIdsNotNoteText(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	const secret = "the sentinel sentence that lives only inside the note body"
	seedDueNote(t, factoryRoot, "alice", "due-1", secret)
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if body := mailFor(t, *sent, "alice").body; strings.Contains(body, secret) {
		t.Errorf("the nag copied a note's text into the mail:\n%s", body)
	}
}

// TestMemoryNag_IdenticalBodiesAreReportedTogether covers the dedup half of the pass. It reports
// and stops there: which of two identical notes is the keeper depends on provenance the vault
// cannot rank.
func TestMemoryNag_IdenticalBodiesAreReportedTogether(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	const shared = "tmux send-keys needs a settle delay before the first prompt"
	a := seedNote(t, factoryRoot, "alice", memory.Note{ID: "dup-a", Type: memory.TypeGotcha, Body: shared})
	b := seedNote(t, factoryRoot, "alice", memory.Note{ID: "dup-b", Type: memory.TypeGotcha, Body: shared})
	solo := seedNote(t, factoryRoot, "alice", memory.Note{ID: "solo", Type: memory.TypeGotcha, Body: "something else entirely"})
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	body := mailFor(t, *sent, "alice").body
	for _, id := range []string{a, b} {
		if !strings.Contains(body, id) {
			t.Errorf("duplicate %q is not named in the nag body:\n%s", id, body)
		}
	}
	if strings.Contains(body, solo) {
		t.Errorf("a note with no twin must not be reported as a duplicate:\n%s", body)
	}
	// Nothing was deleted: reporting is the whole of the power this pass has.
	if got := len(noteFilesUnder(t, filepath.Join(factoryRoot, ".agentfactory", "memory", "alice"))); got != 3 {
		t.Errorf("the pass removed notes: want 3 files on disk, got %d", got)
	}
}

// TestMemoryNag_OversizeNotesAreFlagged covers the boundary half (AC-515-6). Flagging is the whole
// action — enforcement stays advisory (ADR-007), and the other half of the boundary contract,
// off-topic content, is not mechanically decidable and is left to the human the flag summons.
func TestMemoryNag_OversizeNotesAreFlagged(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	big := seedNote(t, factoryRoot, "alice", memory.Note{
		ID:   "oversize-1",
		Type: memory.TypeGotcha,
		Body: strings.Repeat("this note is a document wearing a note's clothes. ", 200),
	})
	small := seedNote(t, factoryRoot, "alice", memory.Note{ID: "small-1", Type: memory.TypeGotcha, Body: "one sentence"})
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	body := mailFor(t, *sent, "alice").body
	if !strings.Contains(body, big) {
		t.Errorf("the oversize note %q is not flagged:\n%s", big, body)
	}
	if strings.Contains(body, small) {
		t.Errorf("a note inside the budget must not be flagged:\n%s", body)
	}
	if readBackNote(t, factoryRoot, "alice", big).Status != memory.StatusActive {
		t.Error("flagging must not expire the note it flagged")
	}
}

// ------------------------------------------------------------------ Gap 11: re-validation

// TestMemoryNag_ReopensAGraduationWhoseFormulaReverted is Gap 11. A formula: graduation points at
// DERIVED state — `make sync-formulas` is an unconditional cp and `af install` overwrites on any
// byte-diff (ADR-015) — so the claim "this learning became a formula edit" can silently stop being
// true, and the note that would have carried it is already out of the injected block.
func TestMemoryNag_ReopensAGraduationWhoseFormulaReverted(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	sum := writeStoreFormula(t, factoryRoot, "alice", "name = \"alice\"\n[[steps]]\nid = \"one\"\n")
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad-1", Type: memory.TypeImprovement, Body: "landed as a formula step"})
	if err := memory.Graduate(factoryRoot, "alice", id, "formula:alice@"+sum); err != nil {
		t.Fatalf("graduating: %v", err)
	}
	writeStoreFormula(t, factoryRoot, "alice", "name = \"alice\"\n") // the redeploy that reverts it
	t.Chdir(factoryRoot)
	asOperator(t)

	out, err := execMemoryOut(t, "status", "--nag")
	if err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	n := readBackNote(t, factoryRoot, "alice", id)
	if n.Status != memory.StatusActive {
		t.Errorf("a graduation whose formula reverted must be reopened; status is %q", n.Status)
	}
	if n.GraduatedTo != "" {
		t.Errorf("a reopened note must not keep pointing at the graduation it lost; graduated_to = %q", n.GraduatedTo)
	}
	if !strings.Contains(out, id) {
		t.Errorf("the reopen must be visible on the pass's own output:\n%s", out)
	}
	if body := mailFor(t, *sent, "alice").body; !strings.Contains(body, id) {
		t.Errorf("the reopened note is not named in the nag body:\n%s", body)
	}
}

// TestMemoryNag_ReopenLeavesAnIntactGraduationAlone is the control that stops the test above
// being satisfied by a pass that reopens everything.
func TestMemoryNag_ReopenLeavesAnIntactGraduationAlone(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	sum := writeStoreFormula(t, factoryRoot, "alice", "name = \"alice\"\n[[steps]]\nid = \"one\"\n")
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad-1", Type: memory.TypeImprovement, Body: "landed as a formula step"})
	if err := memory.Graduate(factoryRoot, "alice", id, "formula:alice@"+sum); err != nil {
		t.Fatalf("graduating: %v", err)
	}
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if got := readBackNote(t, factoryRoot, "alice", id).Status; got != memory.StatusGraduated {
		t.Errorf("an intact graduation must stay graduated; status is %q", got)
	}
	if len(*sent) != 0 {
		t.Errorf("an intact vault must send nothing; got %d mail(s)", len(*sent))
	}
}

// TestMemoryNag_ReopenAcceptsATruncatedUppercaseHash pins the comparison shape. The destination
// vocabulary accepts any 4-to-64 hex digits in either case (store.go:388-391) while formulaSHA256
// emits all 64 in lower case, so an equality test would reopen every correctly-recorded
// truncation — every intact graduation in the factory, on the first cycle after this shipped.
func TestMemoryNag_ReopenAcceptsATruncatedUppercaseHash(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	captureNagMail(t)
	sum := writeStoreFormula(t, factoryRoot, "alice", "name = \"alice\"\n[[steps]]\nid = \"one\"\n")
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad-1", Type: memory.TypeImprovement, Body: "landed as a formula step"})
	if err := memory.Graduate(factoryRoot, "alice", id, "formula:alice@"+strings.ToUpper(sum[:12])); err != nil {
		t.Fatalf("graduating: %v", err)
	}
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if got := readBackNote(t, factoryRoot, "alice", id).Status; got != memory.StatusGraduated {
		t.Errorf("a truncated, upper-case hash still names the same file; status is %q", got)
	}
}

// TestMemoryNag_MissingFormulaIsUnverifiableNotReverted pins the direction of the unknown. The
// store copy is derived (ADR-015), so absence is routine — a fresh checkout, or the window inside
// `make sync-formulas`'s unconditional cp. Reopening on absence would turn an ordinary redeploy
// into a vault-wide un-graduation, and would do it on the dispatcher's cadence.
func TestMemoryNag_MissingFormulaIsUnverifiableNotReverted(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	sum := writeStoreFormula(t, factoryRoot, "alice", "name = \"alice\"\n[[steps]]\nid = \"one\"\n")
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad-1", Type: memory.TypeImprovement, Body: "landed as a formula step"})
	if err := memory.Graduate(factoryRoot, "alice", id, "formula:alice@"+sum); err != nil {
		t.Fatalf("graduating: %v", err)
	}
	if err := os.Remove(filepath.Join(factoryRoot, ".agentfactory", "store", "formulas", "alice.formula.toml")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if got := readBackNote(t, factoryRoot, "alice", id).Status; got != memory.StatusGraduated {
		t.Errorf("an absent store formula proves nothing; status is %q", got)
	}
	// Unverifiable alone never triggers a mail: the owner cannot act on someone else's deploy.
	if len(*sent) != 0 {
		t.Errorf("an unverifiable graduation must not nag on its own; got %d mail(s)", len(*sent))
	}
}

// TestMemoryNag_NonFormulaGraduationsAreLeftAlone: commit:/pr#/issue#/doc: destinations carry
// nothing this pass can re-check, and treating "cannot check" as "reverted" would un-graduate
// every note that went somewhere durable.
func TestMemoryNag_NonFormulaGraduationsAreLeftAlone(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	captureNagMail(t)
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad-pr", Type: memory.TypeImprovement, Body: "landed in a PR"})
	if err := memory.Graduate(factoryRoot, "alice", id, "pr#516"); err != nil {
		t.Fatalf("graduating: %v", err)
	}
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if got := readBackNote(t, factoryRoot, "alice", id).Status; got != memory.StatusGraduated {
		t.Errorf("a non-formula graduation must stay graduated; status is %q", got)
	}
}

// ------------------------------------------------------------------ the cap

// TestMemoryNag_CapsAtOneMailPerAgentPerDay walks the whole cap matrix in one sequence, because
// the two clauses are only separable by holding one input still while the other moves. The pass
// runs on the dispatcher's cadence — ~288 times a day at the default interval — so a cap that
// consulted "have I run recently" instead of the day and the report would be silently defeated by
// an operator changing the dispatch interval.
func TestMemoryNag_CapsAtOneMailPerAgentPerDay(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	seedDueNote(t, factoryRoot, "alice", "due-1", "the first learning nobody answered")
	t.Chdir(factoryRoot)
	asOperator(t)

	run := func(stage string) {
		t.Helper()
		if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
			t.Fatalf("hygiene pass (%s): %v", stage, err)
		}
	}

	// Three cycles, one day, one vault state: the cadence must not be able to buy more mail.
	run("first")
	run("second")
	run("third")
	if len(*sent) != 1 {
		t.Fatalf("three cycles in one day must produce one mail; got %d", len(*sent))
	}

	// New day, SAME report: the report-identity clause holds on its own. A nag that repeats
	// itself with nothing new to say is the inbox rent the cap exists to prevent — and the
	// standing picture is one `af memory status` away, in the DUE and FLAGGED columns.
	rewindNagDay(t, factoryRoot, "alice")
	run("new day, same report")
	if len(*sent) != 1 {
		t.Fatalf("an unchanged report must not be re-sent; got %d mail(s)", len(*sent))
	}

	// New day, same report, but the snooze has run out: the reminder returns. Without this the
	// dedup would mean one mail per distinct report EVER, and an agent that ignored its first
	// MEMORY_HYGIENE mail would never hear about those notes again — retiring the only mechanism
	// behind TTLImprovement = 0.
	rewindNagSentAt(t, factoryRoot, "alice", memoryNagRepeatAfter+time.Hour)
	run("snooze expired, same report")
	if len(*sent) != 2 {
		t.Fatalf("an unchanged report must be re-raised once the snooze lapses; got %d mail(s)", len(*sent))
	}

	// New day AND new information: the nag re-arms without waiting for the snooze, and stamps today.
	rewindNagDay(t, factoryRoot, "alice")
	seedDueNote(t, factoryRoot, "alice", "due-2", "a second learning nobody answered")
	run("new day, new note")
	if len(*sent) != 3 {
		t.Fatalf("a new day with new findings must nag again; got %d mail(s)", len(*sent))
	}
	if got := nagStateOf(t, factoryRoot, "alice").Day; got != time.Now().UTC().Format(memoryNagDayLayout) {
		t.Errorf("the day key must be UTC-stamped at send time; got %q", got)
	}

	// Same day, NEW information: the day clause holds on its own.
	seedDueNote(t, factoryRoot, "alice", "due-3", "a third learning nobody answered")
	run("same day, new note")
	if len(*sent) != 3 {
		t.Fatalf("a new note must not defeat the day cap; got %d mail(s)", len(*sent))
	}
}

// TestMemoryNag_CapIsPerAgent: bob's vault must not be silenced by alice having been nagged.
func TestMemoryNag_CapIsPerAgent(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	seedDueNote(t, factoryRoot, "alice", "due-a", "alice's unanswered learning")
	seedDueNote(t, factoryRoot, "bob", "due-b", "bob's unanswered learning")
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if len(*sent) != 2 {
		t.Fatalf("each vault owner is nagged about their own vault; got %d mail(s)", len(*sent))
	}
	mailFor(t, *sent, "alice")
	mailFor(t, *sent, "bob")
}

// TestMemoryNag_SendFailureLeavesTheCapUnarmed follows deliverCorrective (containment.go:305-349),
// which writes its marker only after the send returns nil. Recording the cap first would spend the
// day's one mail on a send that never happened, and ADR-007's 2026-06-15 amendment forbids letting
// an escalation go into a void.
func TestMemoryNag_SendFailureLeavesTheCapUnarmed(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	seedDueNote(t, factoryRoot, "alice", "due-1", "a learning nobody answered")
	t.Chdir(factoryRoot)
	asOperator(t)

	attempts := failNagMail(t, os.ErrPermission)
	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("a failed nag must not fail the pass: %v", err)
	}
	if *attempts != 1 {
		t.Fatalf("want one send attempt, got %d", *attempts)
	}
	if _, err := os.Stat(memoryNagStatePath(factoryRoot, "alice")); !os.IsNotExist(err) {
		t.Fatal("the day cap must not be armed by a send that failed")
	}

	sent := captureNagMail(t)
	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if len(*sent) != 1 {
		t.Fatalf("the next cycle must retry the failed nag; got %d mail(s)", len(*sent))
	}
}

// TestMemoryNag_StateLivesUnderTheFactoryRoot pins where the cap is kept. A worktree-resident
// record evaporates with the worktree, so the nag would re-arm on every teardown and the hard cap
// would hold only for agents nobody tore down (recovery.go:232-234 states the same rule).
func TestMemoryNag_StateLivesUnderTheFactoryRoot(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	captureNagMail(t)
	seedDueNote(t, factoryRoot, "alice", "due-1", "a learning nobody answered")
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	want := filepath.Join(factoryRoot, ".runtime", "memory_nag", "alice.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the day cap must live at %s: %v", want, err)
	}
	if st := nagStateOf(t, factoryRoot, "alice"); st.V != memoryNagStateVersion {
		t.Errorf("the record must carry its version; got %d", st.V)
	}
}

// ------------------------------------------------------------------ who may run it

// TestMemoryNag_DispatchDaemonIdentityIsAdmitted pins the carve-out ADR-021 records as a residual.
// The daemon classifies AuthorityAgent by signal 2 — it lives in a production af- tmux session —
// so without this the one caller the pass was built for would be refused once per interval into
// .runtime/dispatch.log, and the feature would ship dead.
func TestMemoryNag_DispatchDaemonIdentityIsAdmitted(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	seedDueNote(t, factoryRoot, "alice", "due-1", "a learning nobody answered")
	t.Chdir(factoryRoot)
	asTmuxSession(t, session.DispatchSessionName())

	// The premise: this identity really is agent tier. Without this the test would pass on a
	// build where the gate had been removed altogether.
	if callerAuthority() != AuthorityAgent {
		t.Fatal("the dispatch daemon must classify as agent tier, or this test proves nothing")
	}
	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("the dispatch daemon must be admitted to the hygiene pass: %v", err)
	}
	if len(*sent) != 1 {
		t.Errorf("the daemon's pass must do the work; got %d mail(s)", len(*sent))
	}
}

// TestMemoryNag_OrdinaryAgentSessionIsStillRefused is the other half: the carve-out admits ONE
// session name, not every agent that happens to be inside tmux.
func TestMemoryNag_OrdinaryAgentSessionIsStillRefused(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	seedDueNote(t, factoryRoot, "alice", "due-1", "a learning nobody answered")
	t.Chdir(factoryRoot)
	asTmuxSession(t, session.SessionName("alice"))

	_, err := execMemoryOut(t, "status", "--nag")
	if err == nil {
		t.Fatal("an ordinary agent session must not be admitted to the hygiene pass")
	}
	// ux.md L36-39: a refusal never names the signal it read.
	for _, forbidden := range []string{"AF_ROLE", "TMUX", "tmux session"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("refusal names the detection mechanism %q: %v", forbidden, err)
		}
	}
	if len(*sent) != 0 {
		t.Errorf("the refusal must precede the work; got %d mail(s)", len(*sent))
	}
}

// ------------------------------------------------------------------ the standing picture

// TestMemoryNag_StatusTableCarriesDueAndFlagged pins the columns memory_protocol.go:24 promises 42
// role templates. They are appended after BYTES, never spliced in: the row is read positionally by
// TestMemoryStatus_CountsByLifecycleState.
func TestMemoryNag_StatusTableCarriesDueAndFlagged(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	seedDueNote(t, factoryRoot, "alice", "due-1", "a learning nobody answered")
	seedNote(t, factoryRoot, "alice", memory.Note{
		ID:   "oversize-1",
		Type: memory.TypeGotcha,
		Body: strings.Repeat("this note is a document wearing a note's clothes. ", 200),
	})
	t.Chdir(factoryRoot)
	asOperator(t)

	out, err := execMemoryOut(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var cols string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "AGENT") {
			cols = line
		}
	}
	if cols == "" {
		t.Fatalf("no header row in status output:\n%s", out)
	}
	if !strings.Contains(cols, "BYTES") || !strings.Contains(cols, "DUE") || !strings.Contains(cols, "FLAGGED") {
		t.Fatalf("header must end BYTES, DUE, FLAGGED; got %q", cols)
	}
	if strings.Index(cols, "DUE") < strings.Index(cols, "BYTES") {
		t.Errorf("DUE must follow BYTES so the existing columns keep their positions; got %q", cols)
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "alice") {
			continue
		}
		fields := strings.Fields(line)
		if n := len(fields); n < 3 {
			t.Fatalf("alice's row is too short: %q", line)
		}
		// One due note and one oversize note, reported without sending anything.
		if got := fields[len(fields)-2:]; got[0] != "1" || got[1] != "1" {
			t.Errorf("want DUE=1 FLAGGED=1 on alice's row; got %v in %q", got, line)
		}
	}
}

// ------------------------------------------------------------------ coverage holes closed after review

// TestMemoryNag_AReopenedNoteIsAlsoReportedDue proves the pass re-reads the vault after
// re-validation. Reopening mutates status, and every check after it — due, duplicate, oversize —
// would otherwise classify the note from the state it held BEFORE the mutation, so a note that has
// just come back to active would be reported as reopened and then never nagged about again.
func TestMemoryNag_AReopenedNoteIsAlsoReportedDue(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	sum := writeStoreFormula(t, factoryRoot, "alice", "name = \"alice\"\n[[steps]]\nid = \"one\"\n")
	// Old enough that, once active again, it is immediately due.
	id := seedDueNote(t, factoryRoot, "alice", "grad-old", "a learning that graduated a long time ago")
	if err := memory.Graduate(factoryRoot, "alice", id, "formula:alice@"+sum); err != nil {
		t.Fatalf("graduating: %v", err)
	}
	writeStoreFormula(t, factoryRoot, "alice", "name = \"alice\"\n")
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	body := mailFor(t, *sent, "alice").body
	reopenedAt := strings.Index(body, "REOPENED")
	dueAt := strings.Index(body, "DUE")
	if reopenedAt < 0 || dueAt < 0 {
		t.Fatalf("a reopened, long-overdue note must appear under BOTH headings:\n%s", body)
	}
	// Both headings name it, which is only possible if the due pass saw the post-reopen status.
	if strings.Count(body, id) < 2 {
		t.Errorf("note %s must be named under both REOPENED and DUE:\n%s", id, body)
	}
}

// TestMemoryNag_ANoteOnATTLIsNeverDue is the control for the Expires guard. A note the expiry
// machinery WILL reclaim is already on a clock; nagging about it too would charge the same note
// twice and make the injected block's two lifecycles compete.
func TestMemoryNag_ANoteOnATTLIsNeverDue(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	created := time.Now().UTC().Add(-memory.GraduationDueAfter - 24*time.Hour)
	seedNote(t, factoryRoot, "alice", memory.Note{
		ID:      "old-but-ttld",
		Type:    memory.TypeGotcha,
		Body:    "an old gotcha that expiry will reclaim on its own",
		Created: created,
		Expires: memory.ExpiresFor(created, memory.TypeGotcha),
	})
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if len(*sent) != 0 {
		t.Errorf("a note carrying a TTL must not be nagged; got %d mail(s): %+v", len(*sent), *sent)
	}
}

// TestMemoryNag_OversizeIsMeasuredOnlyOnServedNotes is the control for the status guard on the
// boundary flag. A graduated or expired note is never injected, so its size costs nobody anything
// — flagging it would send an agent to curate a note that has already left.
func TestMemoryNag_OversizeIsMeasuredOnlyOnServedNotes(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	id := seedNote(t, factoryRoot, "alice", memory.Note{
		ID:   "oversize-graduated",
		Type: memory.TypeGotcha,
		Body: strings.Repeat("this note is a document wearing a note's clothes. ", 200),
	})
	if err := memory.Graduate(factoryRoot, "alice", id, "doc:docs/gotchas.md"); err != nil {
		t.Fatalf("graduating: %v", err)
	}
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if len(*sent) != 0 {
		t.Errorf("a note that is no longer served must not be flagged for size; got %d mail(s)", len(*sent))
	}
}

// TestMemoryNag_OneBrokenVaultDoesNotStopThePass: the agent whose vault broke is exactly the one
// whose siblings still need curating, and this runs unattended on the dispatcher's cadence, so a
// pass that aborted on the first bad vault would go dark for the whole factory without saying so.
func TestMemoryNag_OneBrokenVaultDoesNotStopThePass(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	seedDueNote(t, factoryRoot, "alice", "due-1", "alice's unanswered learning")
	// bob's vault is a regular file where a directory belongs: List cannot read it.
	bobVault := filepath.Join(factoryRoot, ".agentfactory", "memory", "bob")
	if err := os.MkdirAll(filepath.Dir(bobVault), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, bobVault, "not a directory\n")
	t.Chdir(factoryRoot)
	asOperator(t)

	out, err := execMemoryOut(t, "status", "--nag")
	if err != nil {
		t.Fatalf("a broken vault must not fail the pass: %v", err)
	}
	if !strings.Contains(out, "bob") {
		t.Errorf("the skipped vault must be reported, not swallowed:\n%s", out)
	}
	mailFor(t, *sent, "alice")
}

// TestMemoryGraduate_FillsInTheFormulaHash covers memoryGraduationDestination. The destination
// vocabulary requires formula:<name>@<hash>, but the instruction shipped to 42 role templates
// (templates/memory_protocol.go:22) and the design's own worked example (integration.md:107) both
// spell it formula:<name> — so an agent following the instruction it was given got a refusal, and
// the hashed graduations this whole hygiene pass re-validates could never come into existence.
func TestMemoryGraduate_FillsInTheFormulaHash(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	sum := writeStoreFormula(t, factoryRoot, "alice", "name = \"alice\"\n[[steps]]\nid = \"one\"\n")
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad-1", Type: memory.TypeImprovement, Body: "became a step"})
	t.Chdir(aliceDir)
	t.Setenv("AF_ROLE", "alice")

	if _, err := execMemoryOut(t, "graduate", id, "--to", "formula:alice"); err != nil {
		t.Fatalf("the destination an agent was told to use must be accepted: %v", err)
	}
	n := readBackNote(t, factoryRoot, "alice", id)
	if n.GraduatedTo != "formula:alice@"+sum {
		t.Errorf("GraduatedTo = %q, want the hash filled in from the store formula (%q)", n.GraduatedTo, "formula:alice@"+sum)
	}
}

// TestMemoryGraduate_LeavesAnUnknownFormulaToTheCore is the control: the destination vocabulary
// belongs to internal/memory, and pre-empting it here would give a formula typo a different error
// surface than every other malformed destination.
func TestMemoryGraduate_LeavesAnUnknownFormulaToTheCore(t *testing.T) {
	factoryRoot, aliceDir := setupMemoryFixture(t)
	id := seedNote(t, factoryRoot, "alice", memory.Note{ID: "grad-1", Type: memory.TypeImprovement, Body: "became a step"})
	t.Chdir(aliceDir)
	t.Setenv("AF_ROLE", "alice")

	if _, err := execMemoryOut(t, "graduate", id, "--to", "formula:no-such-formula"); err == nil {
		t.Error("a destination naming no store formula must still be refused by the core")
	}
	if got := readBackNote(t, factoryRoot, "alice", id).Status; got != memory.StatusActive {
		t.Errorf("a refused graduation must not have marked the note; status is %q", got)
	}
}

// TestMemoryNag_CarveOutRequiresBeingInsideTmux: the session query only reflects OUR pane when we
// are actually in one, so without the $TMUX precondition the carve-out could admit a caller
// callerAuthority itself would not have read as the daemon.
func TestMemoryNag_CarveOutRequiresBeingInsideTmux(t *testing.T) {
	asTmuxSession(t, session.DispatchSessionName())
	if !callerIsDispatchDaemon() {
		t.Fatal("inside the af-dispatch session the carve-out must apply, or this test proves nothing")
	}
	t.Setenv("TMUX", "")
	if callerIsDispatchDaemon() {
		t.Error("outside tmux the session name is not ours to read; the carve-out must not apply")
	}
}

// TestMemoryNag_UnarmableCapMeansNoMail closes the direction that matters most on a 300s loop. If
// the mail went first and the cap could not be recorded afterwards, the next cycle would re-read a
// zero state, both clauses would miss, and the same mail would go again — 288 beads a day for as
// long as the disk stayed full, each one waking the recipient's session. Withdrawing the cap after
// a failed send costs at most one quiet day; the other order costs a mail storm.
func TestMemoryNag_UnarmableCapMeansNoMail(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	seedDueNote(t, factoryRoot, "alice", "due-1", "a learning nobody answered")
	// A regular file where the state DIRECTORY belongs: MkdirAll cannot proceed.
	runtimeDir := filepath.Join(factoryRoot, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(runtimeDir, "memory_nag"), "not a directory\n")
	t.Chdir(factoryRoot)
	asOperator(t)

	out, err := execMemoryOut(t, "status", "--nag")
	if err != nil {
		t.Fatalf("an unarmable cap must not fail the pass: %v", err)
	}
	if len(*sent) != 0 {
		t.Fatalf("a nag that cannot be capped must not be sent; got %d mail(s)", len(*sent))
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("the skipped nag must be reported, not swallowed:\n%s", out)
	}
}

// TestMemoryNag_HandEditedDestinationCannotEscapeTheStore. validateDestination guards the WRITE
// path only, and the vault is deliberately an operator-editable Obsidian directory (AC-515-5), so
// a traversing graduated_to is a designed input. Without the read-side guard the dispatch daemon
// would hash a file outside the factory and reopen a note on the strength of it.
func TestMemoryNag_HandEditedDestinationCannotEscapeTheStore(t *testing.T) {
	for _, dest := range []string{
		"formula:../../../etc/passwd@dead",
		"formula:..@dead",
		"formula:.@dead",
		"formula:sub/dir@dead",
		"formula:alice@dunno", // not hex: could never match a real sum, so it would reopen forever
		"formula:alice@ab",    // shorter than the write side accepts
		"formula:alice@" + strings.Repeat("a", 65),
	} {
		if _, _, ok := memoryFormulaDestination(dest); ok {
			t.Errorf("memoryFormulaDestination(%q) was accepted; a name that is not a bare formula "+
				"name must not reach a store path", dest)
		}
	}
	// Positive control: the guard rejects a shape, not every destination.
	name, hash, ok := memoryFormulaDestination("formula:alice@abcdef01")
	if !ok || name != "alice" || hash != "abcdef01" {
		t.Errorf("a well-formed destination must still split: (%q, %q, %v)", name, hash, ok)
	}
}

// TestMemoryNag_TraversingGraduationIsNotReopened is the end-to-end half: a note carrying a
// hand-edited traversing destination stays graduated rather than being reopened by whatever bytes
// happened to be at the composed path.
func TestMemoryNag_TraversingGraduationIsNotReopened(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	captureNagMail(t)
	// Written straight to disk: memory.Write validates the destination, so the only way this note
	// can exist is the way it exists in reality — someone editing the vault by hand.
	const id = "hand-edited"
	vault := filepath.Join(factoryRoot, ".agentfactory", "memory", "alice")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(vault, id+".md"), string(memory.Emit(memory.Note{
		ID:          id,
		Agent:       "alice",
		Type:        memory.TypeImprovement,
		Created:     time.Now().UTC(),
		Body:        "graduated_to was written by hand",
		Status:      memory.StatusGraduated,
		GraduatedTo: "formula:../../../etc/passwd@dead",
	})))
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	if got := readBackNote(t, factoryRoot, "alice", id).Status; got != memory.StatusGraduated {
		t.Errorf("a destination the pass refuses to resolve proves nothing; status is %q", got)
	}
}

// TestMemoryNag_MalformedNotesAreReported. A malformed note is the one thing in the vault no verb
// can act on — every mark path refuses it — so it can only be fixed by a human opening the file,
// and nothing else in the system would ever mention that it exists.
func TestMemoryNag_MalformedNotesAreReported(t *testing.T) {
	factoryRoot, _ := setupMemoryFixture(t)
	sent := captureNagMail(t)
	vault := filepath.Join(factoryRoot, ".agentfactory", "memory", "alice")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(vault, "broken.md"),
		"---\nstatus: whatever-this-is\n---\n\nfrontmatter the codec cannot read\n")
	seedNote(t, factoryRoot, "alice", memory.Note{ID: "fine", Type: memory.TypeGotcha, Body: "a readable note"})
	t.Chdir(factoryRoot)
	asOperator(t)

	if _, err := execMemoryOut(t, "status", "--nag"); err != nil {
		t.Fatalf("hygiene pass: %v", err)
	}
	body := mailFor(t, *sent, "alice").body
	if !strings.Contains(body, "MALFORMED") || !strings.Contains(body, "broken") {
		t.Errorf("an unmarkable note must be reported to a human who can open the file:\n%s", body)
	}
	if strings.Contains(body, "fine") {
		t.Errorf("a readable note must not be reported as malformed:\n%s", body)
	}
}
