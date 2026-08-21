package memory

import (
	"sort"
	"time"
)

// The injection budget. These are DESIGN defaults chosen in .designs/515/scale.md:30-33, not
// values measured in a running system — scale.md says so in as many words ("design defaults set
// here, not values found in the codebase") — and they are pinned by TestBudgetDefaultsArePinned
// so that changing one is a one-line edit plus a test update rather than silent drift.
//
// Why a ceiling exists at all: the injected block is the only new standing context cost this
// subsystem creates, and the mail subsystem is the cautionary case — an uncapped channel priced
// context so highly that destroying messages became an agent's only relief. Here the cost is a
// bounded constant paid once per session, so relief comes from notes graduating or expiring
// rather than from anything being deleted. Under-serving is therefore the correct failure
// direction: five relevant notes with the rest one `af memory list` away beats an unbounded
// block nobody can afford to keep.
// Source: .designs/515/scale.md:30-33 (Option S-A), recorded 2026-08-15.
const (
	DefaultK            = 5
	DefaultExcerptChars = 400
	DefaultTotalBytes   = 4096
)

// Per-type TTLs. A gotcha or an outcome is about a codebase that moves under it, so it goes
// stale in a quarter; ops facts and model behaviour age slower and get two. An improvement note
// gets NO TTL at all — it is supposed to become a formula edit, so it is nagged rather than
// quietly expired, and letting it lapse would lose the prompt instead of acting on it.
// Source: .designs/515/scale.md:168-174, recorded 2026-08-15.
const (
	TTLGotcha        = 90 * 24 * time.Hour
	TTLOutcome       = 90 * 24 * time.Hour
	TTLOps           = 180 * 24 * time.Hour
	TTLModelBehavior = 180 * 24 * time.Hour
	TTLImprovement   = 0
)

// GraduationDueAfter is how long a note carrying NO TTL may sit active before the hygiene pass
// starts asking its owner what became of it. It is the second half of the TTLImprovement = 0
// decision above: a note the expiry machinery will never reclaim needs some other clock, or
// "nagged rather than quietly expired" is a promise with nothing behind it.
//
// 30 days rather than the 90 a gotcha gets, because the two clocks do opposite things. A TTL ends
// a note, so it should be generous. This one only asks a question, and it has to arrive while the
// run that produced the learning is still recoverable context — and well before any TTL-bearing
// sibling would have lapsed, so the vault is never simultaneously nagging and expiring.
// Source: .designs/515/scale.md:130 ("age > threshold, still active"), which fixes no number;
// pinned by TestGraduationDueThresholdIsPinned, recorded 2026-08-15 (#515 Phase 5).
const GraduationDueAfter = 30 * 24 * time.Hour

// TTLForType returns the lifetime for a note type. A type nobody has defined gets the shortest
// TTL rather than the longest: an unrecognised note is the one least likely to be worth serving
// for half a year.
func TTLForType(noteType string) time.Duration {
	switch noteType {
	case TypeOps, TypeModelBehavior:
		return TTLOps
	case TypeImprovement:
		return TTLImprovement
	default:
		return TTLGotcha
	}
}

// ExpiresFor stamps a note's TTL at creation. A zero TTL yields the ZERO TIME, not
// created+0 — feeding a zero duration through created.Add would make every improvement note
// expired the instant it was written, which is the exact inverse of the design.
func ExpiresFor(created time.Time, noteType string) time.Time {
	ttl := TTLForType(noteType)
	if ttl == 0 {
		return time.Time{}
	}
	return created.UTC().Add(ttl)
}

// Budget bounds one injected block and carries the two inputs the ranking needs. The scope key
// lives here rather than in a third parameter so that the pure Slice(notes, Budget) surface
// stays expressible: without it, "formula-match-first then recency" is unsayable and a slice
// silently degrades to recency-only, deleting the relevance mechanism the design turns on.
type Budget struct {
	// K caps the number of notes served.
	K int
	// ExcerptChars caps each note's body, in runes.
	ExcerptChars int
	// TotalBytes caps the whole block, measured as the sum of the canonical emitted forms of
	// the returned notes — the concrete artifact, so the ceiling is falsifiable rather than
	// nominal.
	TotalBytes int
	// Formula is the scope key. When a run is hooked to a formula, notes tagged with it rank
	// ahead of everything else; when it is empty the ranking is recency alone.
	Formula string
	// Now is the clock expiry is measured against. It is a field rather than an ambient read so
	// the same fixture reclassifies purely as a function of this value.
	Now time.Time
}

func DefaultBudget() Budget {
	return Budget{
		K:            DefaultK,
		ExcerptChars: DefaultExcerptChars,
		TotalBytes:   DefaultTotalBytes,
	}
}

// normalised fills a zero-value Budget with the pinned defaults. A caller that passed Budget{}
// and got nothing back would present as "memory injection quietly stopped working" — the same
// silent-darkness failure the statusline path helpers were written to prevent.
func (b Budget) normalised() Budget {
	if b.K <= 0 {
		b.K = DefaultK
	}
	if b.ExcerptChars <= 0 {
		b.ExcerptChars = DefaultExcerptChars
	}
	if b.TotalBytes <= 0 {
		b.TotalBytes = DefaultTotalBytes
	}
	if b.Now.IsZero() {
		// Every serving path sets Now; this is the safety net for one that forgets. Defaulting
		// to the wall clock keeps suppression ON, because the alternative — treating a missing
		// clock as "no TTL filtering" — serves stale notes forever, which is precisely the
		// failure the read-time filter exists to prevent.
		b.Now = time.Now().UTC()
	}
	return b
}

// Slice selects the notes an agent should receive at session start, bounded by b. It is pure
// with respect to its inputs: it never touches the filesystem and never mutates the slice it is
// given, and with Now set its result is a function of its arguments alone.
//
// It is also the ONLY thing that feeds injection, so it is where the three exclusions have to
// live: a graduated or expired note, a note whose TTL has passed, and a note whose frontmatter
// the codec could not read are none of them served. Suppression at read time is what makes
// staleness correct even on a factory where no periodic hygiene job ever runs.
//
// The overflow count a caller needs for the "…and N more" line is the difference between two
// calls — Slice with an effectively unbounded Budget minus Slice with the real one — NOT
// len(notes) - len(result), which counts the graduated, expired and malformed notes this
// function excluded and would over-report. The signature stays (notes, Budget) rather than
// growing a second return value because the exclusions, not the ceilings, are what a caller
// gets wrong when it computes that number itself.
func Slice(notes []Note, b Budget) []Note {
	b = b.normalised()

	servable := make([]Note, 0, len(notes))
	for _, n := range notes {
		if n.Malformed || n.Status != StatusActive {
			continue
		}
		// A zero Expires means "no TTL", never "expired at the epoch".
		if !n.Expires.IsZero() && !n.Expires.After(b.Now) {
			continue
		}
		servable = append(servable, n)
	}

	// Stable, so ties resolve by input order rather than by whatever the sort felt like — an
	// unstable rank here would make the injected block differ between two identical sessions.
	sort.SliceStable(servable, func(i, j int) bool {
		iMatch := b.Formula != "" && servable[i].Formula == b.Formula
		jMatch := b.Formula != "" && servable[j].Formula == b.Formula
		if iMatch != jMatch {
			return iMatch
		}
		return servable[j].Created.Before(servable[i].Created)
	})

	out := make([]Note, 0, b.K)
	total := 0
	for _, n := range servable {
		if len(out) >= b.K {
			break
		}
		n.Body = excerpt(n.Body, b.ExcerptChars)
		size := len(Emit(n))
		if size > b.TotalBytes {
			// A note that would not fit even in an empty block is unservable at this budget, so
			// it is skipped rather than stopped on. Evidence is unbounded, which makes one such
			// note reachable, and stopping would let it starve every note ranked below it:
			// under-serving is the correct failure direction, but returning NOTHING while
			// servable notes exist reads as injection having silently stopped working.
			continue
		}
		if total+size > b.TotalBytes {
			// Stop rather than skip: the result stays a prefix of the notes that CAN be served at
			// this budget, so the overflow really is "…and N more" rather than an arbitrary
			// subset. (Not a prefix of the ranking itself — the branch above may already have
			// skipped a note too large to serve at any position.)
			break
		}
		out = append(out, n)
		total += size
	}
	return out
}

// excerpt caps a body at n runes — characters, not bytes, so a note written in any script gets
// the same amount of text. The ellipsis is the disclosure: truncation is reported in the
// artifact, never applied silently.
func excerpt(body string, n int) string {
	r := []rune(body)
	if len(r) <= n {
		return body
	}
	return string(r[:n]) + "…"
}
