// The hygiene pass behind `af memory status --nag` — the half of the vault's lifecycle nobody is
// present to run. #515's read side suppresses stale notes at serve time, but nothing ever asked
// what became of the notes that carry no TTL, noticed that two of them say the same thing, or
// re-checked a graduation whose destination is DERIVED state a redeploy can revert. This file is
// that periodic question, invoked once per dispatcher cycle (.designs/515/scale.md:125-138 chose
// the dispatch loop over the watchdog, whose 30s cadence is three orders of magnitude too fast for
// daily-scale curation).
//
// THE RULE THIS FILE EXISTS TO KEEP: the pass runs every ~300s, so nothing on the HEALTHY path may
// be priced per CYCLE. It reads and marks but never deletes; it costs at most one mail per agent
// per day (scale.md:191); it says nothing when there is nothing to say; and it opens the mail
// backend only on the branch that sends. Every gate below is keyed on the day or on having acted —
// never on "have I run recently", which a change to the dispatch interval would silently defeat.
//
// FAULTS ARE THE EXCEPTION, deliberately. A broken vault, a refused reopen or an unarmable cap is
// reported on every cycle it persists, which does put repeated lines in an unrotated
// .runtime/dispatch.log. That is the lesser cost: a fault that reports once and then goes quiet is
// one an operator reads as fixed, and ADR-007's 2026-06-15 amendment forbids letting an escalation
// go into a void. What must never repeat is a MAIL, and no fault path here sends one.
package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/mail"
	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/session"
)

const (
	// memoryNagDayLayout keys the once-a-day cap. UTC — NOT statusline/daily.go:26's local day
	// key, which labels a spend figure a human reads against their own wall clock. This one bounds
	// machine-generated mail and is compared against UTC-stamped note ages, so a local key would
	// be the only local-time value in the vault and would cross a format boundary on every
	// comparison (the hazard recovery.go:249-251 names). It also makes the cap reproducible in a
	// hermetic test without TZ manipulation, which ADR-018 requires.
	memoryNagDayLayout = "2006-01-02"

	// memoryNagStateVersion versions the on-disk cap record. Every durable record in this tree
	// carries one; recovery.go:267-271 documents what an unversioned on-disk format costs later.
	memoryNagStateVersion = 1

	// memoryNagRepeatAfter is how long an UNCHANGED report stays suppressed. It reconciles the
	// design's two statements of the cap: scale.md:191 says "hard cap 1 nag/agent/day, deduped by
	// note id set hash", design-doc.md:86 says "max 1 mail/agent/day". Deduping alone would mean
	// one mail per distinct report EVER — an agent that ignored its first MEMORY_HYGIENE mail
	// would never hear about those notes again, which retires the only mechanism behind
	// TTLImprovement = 0's "nagged rather than quietly expired". Capping alone would repeat an
	// identical report every day, which is the inbox rent the dedup clause is there to prevent.
	//
	// Weekly, because the thing being asked about took 30 days to become due: a reminder has to
	// arrive often enough that the note is not forgotten and rarely enough that it stays a
	// reminder. New information — any note appearing, changing category, or being reopened —
	// re-arms immediately regardless, subject only to the day cap.
	memoryNagRepeatAfter = 7 * 24 * time.Hour
)

// memoryNagState is the cap, one file per agent. Every field is read by suppresses below; none of
// them is a breadcrumb.
type memoryNagState struct {
	V      int    `json:"v"`
	Day    string `json:"day"`
	Notes  string `json:"note_ids_hash"`
	SentAt string `json:"sent_at"`
}

// suppresses answers the only question the cap exists to answer. Both clauses of the design's cap
// are here, and they bind in different directions: the DAY is a hard ceiling that nothing defeats,
// and the REPORT HASH is a snooze that new information cancels.
//
// An unparseable SentAt does not suppress. It is the same permissive direction loadMemoryNagState
// takes and for the same reason: one extra mail against an agent that silently stops being nagged,
// which looks exactly like a healthy vault.
func (st memoryNagState) suppresses(reportHash, today string, now time.Time) bool {
	if st.Day == today {
		return true
	}
	if st.Notes != reportHash {
		return false
	}
	sent, err := time.Parse(time.RFC3339, st.SentAt)
	return err == nil && now.Sub(sent) < memoryNagRepeatAfter
}

// There is no .runtime path constructor in internal/config, deliberately: that package owns
// .agentfactory/* paths only. These mirror recoveryStateDir/recoveryStatePath (recovery.go:235-239).
//
// Factory root, never an agent worktree — the same rule and the same reason as the recovery
// breaker (recovery.go:232-234): a worktree-resident cap evaporates with the worktree, so the nag
// would silently re-arm on every teardown and the "hard cap" would hold only for agents nobody
// tore down.
func memoryNagStateDir(root string) string { return filepath.Join(root, ".runtime", "memory_nag") }

func memoryNagStatePath(root, agent string) string {
	return filepath.Join(memoryNagStateDir(root), agent+".json")
}

// loadMemoryNagState reads the cap. An unreadable record reads as "never nagged", which is the
// OPPOSITE of loadRecoveryState's posture and deliberately so: there the permissive direction
// resumes recycling an agent an operator had halted, here it costs one extra mail. The strict
// direction would stop nagging an agent forever, and a hygiene pass that has gone quiet is
// indistinguishable from a vault that needs nothing.
func loadMemoryNagState(root, agent string) memoryNagState {
	data, err := os.ReadFile(memoryNagStatePath(root, agent))
	if err != nil {
		return memoryNagState{V: memoryNagStateVersion}
	}
	var st memoryNagState
	if err := json.Unmarshal(data, &st); err != nil {
		return memoryNagState{V: memoryNagStateVersion}
	}
	return st
}

func saveMemoryNagState(root, agent string, st memoryNagState) error {
	if err := config.ValidateAgentName(agent); err != nil {
		return fmt.Errorf("refusing to write nag state for an invalid agent name: %w", err)
	}
	st.V = memoryNagStateVersion
	if err := os.MkdirAll(memoryNagStateDir(root), 0o755); err != nil {
		return fmt.Errorf("creating the memory nag state dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(memoryNagStatePath(root, agent), data, 0o644)
}

// memoryNagFindings is one agent's hygiene report. Every field holds note IDS and nothing else:
// the mail is list-sized (scale.md:191 — "ids + titles, never bodies"), and a nag that quoted note
// text back into an inbox would be the second copy of the vault this whole design is arranged
// against.
type memoryNagFindings struct {
	Agent        string
	Reopened     []string
	Due          []string
	Duplicates   []string
	Oversize     []string
	Malformed    []string
	Unverifiable []string
}

// actionable reports whether there is anything worth a mail. Unverifiable graduations are listed
// when a mail is going out anyway but never trigger one on their own: under ADR-015 the store
// formula is derived and `make sync-formulas` is an unconditional cp, so its absence is a routine
// state the note's owner cannot act on. Nagging daily about it would charge an agent rent for
// someone else's deploy.
func (f memoryNagFindings) actionable() bool {
	return len(f.Reopened)+len(f.Due)+len(f.Duplicates)+len(f.Oversize)+len(f.Malformed) > 0
}

func (f memoryNagFindings) total() int {
	return len(f.Reopened) + len(f.Due) + len(f.Duplicates) + len(f.Oversize) + len(f.Malformed) + len(f.Unverifiable)
}

// memoryNagDue reports whether a note has sat active long enough to owe an answer. It targets
// exactly the notes the expiry machinery will never reclaim — Expires zero, which is what
// TTLImprovement = 0 produces (slice.go). A note that DOES carry a TTL is already on a clock and
// gets expired rather than nagged; asking about it as well would charge the same note twice.
//
// A note with NO created stamp is exempt rather than immediately due. The vault is hand-editable,
// so an undated note is an ordinary artifact of someone writing one by hand, and its age is
// genuinely unknowable — nagging on the zero time would date it to year 1 and demand an answer
// about a note written this morning.
func memoryNagDue(n memory.Note, now time.Time) bool {
	return !n.Malformed &&
		n.Status == memory.StatusActive &&
		n.Expires.IsZero() &&
		!n.Created.IsZero() &&
		now.Sub(n.Created) > memory.GraduationDueAfter
}

// memoryNagOversize is the boundary flag (AC-515-6): it asks whether a note is still a note.
//
// The threshold borrows DefaultTotalBytes as a yardstick, NOT as a budget claim — a note this long
// does not actually break injection, because Slice excerpts the body to ExcerptChars before
// measuring anything. What it breaks is the form. A learning that takes more bytes than a whole
// session's block is a document, and a document served as a 400-character excerpt is worse than
// either: too long to be a note, too truncated to be the document.
//
// Only ACTIVE notes are measured — a graduated or expired note is never served, so its size costs
// nobody anything. Flagging is all that happens: enforcement here is advisory by design (ADR-007
// posture), and the other half of the boundary contract, off-topic content, is not mechanically
// decidable — it is left to the human the flag brings to the note.
func memoryNagOversize(n memory.Note) bool {
	return !n.Malformed && n.Status == memory.StatusActive && len(memory.Emit(n)) > memory.DefaultTotalBytes
}

// memoryFormulaDestination splits a formula:<name>@<hash> graduation destination. It reuses
// validateDestination's own split AND its name guard (store.go:388-410) so the reader and the
// writer cannot disagree about where the name ends or what may be in it.
//
// Re-applying the guard on the READ side is not belt-and-braces. validateDestination runs only
// when a graduation is recorded, and the vault is deliberately an operator-editable Obsidian
// directory (AC-515-5), so `graduated_to: formula:../../../etc/shadow@dead` is a designed input,
// not a hypothetical. Without this the dispatch daemon would compose that into a store path and
// hash whatever it found there — nothing leaks, but a note would be reopened on the strength of a
// file nobody meant to name.
func memoryFormulaDestination(dest string) (name, hash string, ok bool) {
	rest, found := strings.CutPrefix(dest, "formula:")
	if !found {
		return "", "", false
	}
	name, hash, found = strings.Cut(rest, "@")
	if !found || name == "" || hash == "" {
		return "", "", false
	}
	if name == "." || name == ".." || strings.ContainsAny(name, " \t/") {
		return "", "", false
	}
	// The hash half gets the write side's guard too (isHex(hash, 4, 64), store.go). Anything
	// outside that vocabulary can never prefix-match a real sha256, so without this a hand-edited
	// `@dunno` would reopen its note on every single cycle — the one failure mode a mark-only
	// lifecycle cannot self-correct out of, because reopening is not idempotent from the owner's
	// point of view: each pass hands them the note back again.
	if len(hash) < 4 || len(hash) > 64 || strings.TrimLeft(strings.ToLower(hash), "0123456789abcdef") != "" {
		return "", "", false
	}
	return name, hash, true
}

// memoryNagRevalidate re-checks every formula: graduation against the store formula it names, and
// reopens the ones whose target moved out from under them (Gap 11, design-doc.md:157).
//
// What a hash mismatch actually proves is narrower than "reverted": it proves the file is not the
// one the graduation was recorded against. A revert produces it, and so does the NEXT legitimate
// edit — including the one /improve-agent is about to make. The design chose this mechanism
// knowing that, because the alternative is trusting a claim about derived state forever, and a
// false reopen costs one note coming back for review while a missed revert loses the learning
// silently. Everything downstream is therefore worded as a question, not a verdict: the note comes
// back to active (where it is served and nagged, not lost) and the mail asks the owner to confirm
// rather than telling them their work was undone.
//
// The comparison is a case-folded PREFIX match, never equality: the destination vocabulary accepts
// a truncated hash in either case (isHex(hash, 4, 64), store.go:388-391) while formulaSHA256
// returns the full 64 lower-hex digits, so `==` would reopen every correctly-recorded truncation.
//
// A MISSING store formula is unverifiable, never reverted. The store copy is derived (ADR-015), so
// absence is routine — a fresh checkout, or the window inside `make sync-formulas`'s unconditional
// cp — and reopening on absence would turn an ordinary redeploy into a vault-wide un-graduation.
func memoryNagRevalidate(warn io.Writer, root, agent string, notes []memory.Note) (reopened, unverifiable []string) {
	for _, n := range notes {
		if n.Malformed || n.Status != memory.StatusGraduated {
			continue
		}
		name, recorded, ok := memoryFormulaDestination(n.GraduatedTo)
		if !ok {
			continue // commit:/issue#/pr#/doc: destinations carry nothing to re-validate against
		}
		sum, err := formulaSHA256(config.FormulaStorePath(root, name))
		if err != nil {
			unverifiable = append(unverifiable, n.ID)
			continue
		}
		if strings.HasPrefix(strings.ToLower(sum), strings.ToLower(recorded)) {
			continue
		}
		if err := memory.Reopen(root, agent, n.ID); err != nil {
			fmt.Fprintf(warn, "warning: could not reopen %s's note %s: %v\n", agent, n.ID, err)
			continue
		}
		// The destination travels with the id because Reopen clears it from the note. An active
		// note carrying a graduated_to would be a contradiction on disk — served while claiming
		// to have left — so the pointer the owner needs to re-check the claim has to survive
		// somewhere else, and the mail is where a human is already looking.
		reopened = append(reopened, n.ID+" (was "+n.GraduatedTo+")")
	}
	return reopened, unverifiable
}

// memoryNagDuplicates returns the ids of every active note that shares its text with another. It
// reports and stops there: which of two identical notes is the keeper depends on provenance the
// vault cannot rank, and removing either is not a power this subsystem has at all.
func memoryNagDuplicates(notes []memory.Note) []string {
	byBody := map[string][]string{}
	for _, n := range notes {
		if n.Malformed || n.Status != memory.StatusActive {
			continue
		}
		sum := sha256.Sum256([]byte(strings.TrimSpace(n.Body)))
		key := fmt.Sprintf("%x", sum[:])
		byBody[key] = append(byBody[key], n.ID)
	}
	var dups []string
	for _, ids := range byBody {
		if len(ids) > 1 {
			dups = append(dups, ids...)
		}
	}
	// Map iteration is unordered, so sort: this list reaches a mail body, and the same vault
	// producing a differently-ordered list on every run makes two identical reports read as two
	// different ones to whoever is comparing them. (The report hash sorts its own keys.)
	sort.Strings(dups)
	return dups
}

// collectMemoryNagFindings runs the whole pass for one agent. Re-validation goes FIRST because it
// mutates: a note it reopens is active again, and every check after it must see the vault as it
// now stands rather than as it stood a moment ago.
func collectMemoryNagFindings(warn io.Writer, root, agent string, now time.Time) (memoryNagFindings, error) {
	notes, err := memory.List(root, agent, memory.Filter{})
	if err != nil {
		return memoryNagFindings{}, err
	}

	f := memoryNagFindings{Agent: agent}
	f.Reopened, f.Unverifiable = memoryNagRevalidate(warn, root, agent, notes)
	if len(f.Reopened) > 0 {
		if notes, err = memory.List(root, agent, memory.Filter{}); err != nil {
			return memoryNagFindings{}, err
		}
	}

	for _, n := range notes {
		if n.Malformed {
			f.Malformed = append(f.Malformed, n.ID)
			continue
		}
		if memoryNagDue(n, now) {
			f.Due = append(f.Due, n.ID)
		}
		if memoryNagOversize(n) {
			f.Oversize = append(f.Oversize, n.ID)
		}
	}
	f.Duplicates = memoryNagDuplicates(notes)
	return f, nil
}

// memoryNagReportHash identifies WHAT was reported, so an unchanged report is not re-sent when the
// day rolls over (scale.md:191's "deduped by note id set hash"). The category travels with the id
// because a note moving from due to reopened is new information even though the id set did not
// change.
func memoryNagReportHash(f memoryNagFindings) string {
	var keys []string
	for category, ids := range map[string][]string{
		"reopened": f.Reopened, "due": f.Due, "duplicate": f.Duplicates,
		"oversize": f.Oversize, "malformed": f.Malformed, "unverifiable": f.Unverifiable,
	} {
		for _, id := range ids {
			keys = append(keys, category+":"+id)
		}
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "|")))
	return fmt.Sprintf("%x", sum[:])
}

// memoryNagMessage renders the one mail. The subject follows the house ALL-CAPS label convention
// (WORKTREE_CONTAINMENT, IMPROVEMENT, RECOVERY HALTED). The body carries ids, counts and the verbs
// that answer them — never a note's text: the vault is where notes live, the mailbox is a pointer
// to it.
func memoryNagMessage(f memoryNagFindings) (subject, body string) {
	subject = fmt.Sprintf("MEMORY_HYGIENE: %s has %d note(s) awaiting curation", f.Agent, f.total())

	var b strings.Builder
	b.WriteString("Your memory vault is waiting on you. Ids only — read one with `af memory show <id>`.\n")
	section := func(heading string, ids []string) {
		if len(ids) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s (%d): %s\n", heading, len(ids), strings.Join(ids, ", "))
	}
	section("REOPENED — the store formula each of these graduated to is no longer the file the "+
		"graduation was recorded against; it may have moved on rather than reverted, so they are "+
		"active again for you to confirm and re-graduate", f.Reopened)
	section(fmt.Sprintf("DUE — active with no TTL for over %d days; graduate it "+
		"(af memory graduate <id> --to formula:<name>) or expire it (af memory expire <id>)",
		int(memory.GraduationDueAfter.Hours()/24)), f.Due)
	section("IDENTICAL — these notes say the same thing; keep one and expire the rest", f.Duplicates)
	section("OVERSIZE — each of these is longer than a whole session's memory block, so it is a "+
		"document rather than a learning and is served to you truncated; split it, or graduate it "+
		"into a real document and expire the note", f.Oversize)
	section("MALFORMED — the frontmatter cannot be read, so no verb can mark them; an operator has "+
		"to repair the file", f.Malformed)
	section("UNVERIFIABLE — the store formula each graduated to is not present, so the graduation "+
		"could not be re-checked; no action needed unless it stays absent", f.Unverifiable)
	return subject, b.String()
}

// sendMemoryNagMail is the hygiene pass's mail seam, shaped like sendContainmentMail
// (containment.go:411-435) and deliberately WITHOUT an isTestBinary() short-circuit for the same
// reason: a binary check silently no-ops the production path under test, so isolation is the test
// reassigning this var (ADR-009).
//
// The nag is self-addressed, owner to owner, because the sender is a daemon with no agent identity
// of its own and the subject matter is the recipient's own vault. Priority stays Normal:
// containment is an alarm, this is a chore, and an urgent recurring mail would re-open the "rent by
// another name" risk the day cap closes.
var sendMemoryNagMail = func(root, recipient, subject, body string) error {
	store, err := newIssueStoreAt(root, os.Getenv("AF_ACTOR"))
	if err != nil {
		return err
	}
	router, err := mail.NewRouter(root, store)
	if err != nil {
		return err
	}
	return router.Send(context.Background(), mail.NewMessage(recipient, recipient, subject, body))
}

// callerIsDispatchDaemon reports whether this process IS the dispatch loop. The hygiene pass is
// operator-tier, and the dispatcher runs inside the af-dispatch tmux session — an agent-context
// signal to callerAuthority (authority.go:79-83) — so without this carve-out the one caller the
// pass was built for would be refused once every interval into .runtime/dispatch.log, and the
// feature would ship dead in a way no hermetic test could see.
//
// Shape copied from sling --reset's carve-out for the same daemon (sling.go:194-197) and admitted
// at the same cost: ADR-021 records the dispatch-daemon carve-out as a residual and says the
// session name is no more forgeable by a same-uid process than the tier signals callerAuthority
// already trusts. It deliberately does NOT touch callerAuthority itself — that classifier is the
// single decision behind six teardown surfaces, and a third Authority value was rejected by
// ADR-021 because existing `== AuthorityAgent` consumers would fail OPEN past it.
func callerIsDispatchDaemon() bool {
	// $TMUX first, exactly as callerAuthority gates its own signal 2 (authority.go:79): the
	// session query only reflects OUR pane when we are actually inside one, and from a bare shell
	// on a host that merely has an af-dispatch server running it would echo that server's name.
	// Without this the carve-out could admit a caller the classifier itself would not have read as
	// the daemon — a strictly wider hole than the one ADR-021 sanctioned.
	if os.Getenv("TMUX") == "" {
		return false
	}
	cur, err := newCmdTmux().CurrentSessionName()
	return err == nil && cur != "" && cur == session.DispatchSessionName()
}

// runMemoryNag is the hygiene pass. On a healthy vault it does nothing and says nothing — the
// silence is the contract, not an omission: this runs in the dispatcher's own shell loop at ~300s,
// and a line per cycle is 1728 lines a day in a log with no rotation.
//
// The mail backend is opened only on the branch that sends, and that laziness is load-bearing
// rather than tidy: newIssueStoreAt can spend up to 30s spawning the MCP server, and the pass runs
// serially inside the dispatch loop, so a store opened every cycle would couple dispatch cadence to
// a Python process 287 of 288 daily cycles have no use for.
func runMemoryNag(cmd *cobra.Command) error {
	_, root, _, err := memoryReadContext()
	if err != nil {
		return err
	}
	cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return fmt.Errorf("reading the agent roster: %w", err)
	}
	agents := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		agents = append(agents, name)
	}
	sort.Strings(agents)

	out, warn := cmd.OutOrStdout(), cmd.ErrOrStderr()
	now := time.Now().UTC()
	today := now.Format(memoryNagDayLayout)

	for _, agent := range agents {
		f, err := collectMemoryNagFindings(warn, root, agent, now)
		if err != nil {
			// One unreadable vault must not end the pass: the agent whose vault broke is exactly
			// the one whose siblings still need curating.
			fmt.Fprintf(warn, "warning: hygiene pass skipped %s: %v\n", agent, err)
			continue
		}
		for _, id := range f.Reopened {
			fmt.Fprintf(out, "reopened %s/%s: the formula it graduated to is not the file the graduation recorded\n", agent, id)
		}
		if !f.actionable() {
			continue
		}

		reportHash := memoryNagReportHash(f)
		if loadMemoryNagState(root, agent).suppresses(reportHash, today, now) {
			continue
		}
		// Arm the cap BEFORE sending, and withdraw it if the send fails. deliverCorrective
		// (containment.go:305-349) writes its marker only after a successful send, and that is
		// right for an alarm that must reach someone; this is a recurring chore on a 300s loop, so
		// the two failure directions are not symmetric. An unarmable cap after a successful send
		// would repeat the mail EVERY CYCLE for as long as the disk stayed full — 288 beads a day,
		// each waking the recipient's session — which is the "rent by another name" risk (R6,
		// scale.md:191) reopened at full cadence. A withdrawal that itself fails costs one day of
		// silence about notes that are, by construction, at least 30 days old.
		state := memoryNagState{Day: today, Notes: reportHash, SentAt: now.Format(time.RFC3339)}
		if err := saveMemoryNagState(root, agent, state); err != nil {
			fmt.Fprintf(warn, "warning: not nagging %s — the day cap could not be armed: %v\n", agent, err)
			continue
		}
		subject, body := memoryNagMessage(f)
		if err := sendMemoryNagMail(root, agent, subject, body); err != nil {
			// ADR-007's amendment forbids discarding a send result silently, and a nag whose mail
			// never arrives looks exactly like a vault that needed nothing.
			fmt.Fprintf(warn, "warning: hygiene nag to %s failed: %v\n", agent, err)
			if rmErr := os.Remove(memoryNagStatePath(root, agent)); rmErr != nil {
				fmt.Fprintf(warn, "warning: %s's day cap stays armed despite the failed send: %v\n", agent, rmErr)
			}
			continue
		}
		fmt.Fprintf(out, "nagged %s: %s\n", agent, subject)
	}
	return nil
}
