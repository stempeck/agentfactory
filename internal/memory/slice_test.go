package memory

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

var sliceNow = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

func servable(id string, created time.Time) Note {
	return Note{
		ID:      id,
		Agent:   "manager",
		Type:    TypeGotcha,
		Created: created,
		Status:  StatusActive,
		Body:    "body of " + id + "\n",
	}
}

// TestSlice_NeverReturnsExpiredOrGraduated is the load-bearing bound test (T-BOUND). The
// fixture carries a positive control on purpose: "expired notes are absent" is also true of a
// Slice that returns nothing, so the assertion is on the exact returned set, not on absence.
func TestSlice_NeverReturnsExpiredOrGraduated(t *testing.T) {
	keep := servable("keep", sliceNow.Add(-time.Hour))
	keep.Expires = sliceNow.Add(30 * 24 * time.Hour)

	keepNoTTL := servable("keep-no-ttl", sliceNow.Add(-2*time.Hour))
	keepNoTTL.Expires = time.Time{} // zero = no TTL, never "expired at the epoch"

	markedExpired := servable("marked-expired", sliceNow.Add(-3*time.Hour))
	markedExpired.Status = StatusExpired

	markedGraduated := servable("marked-graduated", sliceNow.Add(-4*time.Hour))
	markedGraduated.Status = StatusGraduated
	markedGraduated.GraduatedTo = "issue#631"

	ttlPassed := servable("ttl-passed", sliceNow.Add(-5*time.Hour))
	ttlPassed.Expires = sliceNow.Add(-24 * time.Hour) // Status is still "active"

	malformed := servable("malformed", sliceNow.Add(-6*time.Hour))
	malformed.Malformed = true

	in := []Note{keep, keepNoTTL, markedExpired, markedGraduated, ttlPassed, malformed}
	before := idsOf(in)

	b := DefaultBudget()
	b.Now = sliceNow

	got := idsOf(Slice(in, b))
	want := []string{"keep", "keep-no-ttl"}
	if !equalIDs(got, want) {
		t.Errorf("Slice: got %v, want %v", got, want)
	}

	if !equalIDs(idsOf(in), before) {
		t.Errorf("Slice mutated its input: got %v, want %v", idsOf(in), before)
	}
}

// TestSlice_ExpiryFollowsTheInjectedClock is the internal/statusline/observation_test.go:683
// move: the same fixture must reclassify purely as a function of the injected clock, which is
// what makes "the clock is a parameter" a test result rather than a code-review opinion.
func TestSlice_ExpiryFollowsTheInjectedClock(t *testing.T) {
	keep := servable("keep", sliceNow.Add(-time.Hour))
	keep.Expires = sliceNow.Add(30 * 24 * time.Hour)
	keepNoTTL := servable("keep-no-ttl", sliceNow.Add(-2*time.Hour))

	in := []Note{keep, keepNoTTL}

	b := DefaultBudget()
	b.Now = sliceNow.Add(60 * 24 * time.Hour)

	got := idsOf(Slice(in, b))
	want := []string{"keep-no-ttl"}
	if !equalIDs(got, want) {
		t.Errorf("Slice with the clock advanced 60 days: got %v, want %v", got, want)
	}
}

func TestSlice_EmptyInEmptyOut(t *testing.T) {
	b := DefaultBudget()
	b.Now = sliceNow

	if got := Slice(nil, b); len(got) != 0 {
		t.Errorf("Slice(nil): got %d notes, want 0", len(got))
	}
	if got := Slice([]Note{}, b); len(got) != 0 {
		t.Errorf("Slice(empty): got %d notes, want 0", len(got))
	}
}

func TestSlice_KCeiling(t *testing.T) {
	var in []Note
	for i := 0; i < 20; i++ {
		in = append(in, servable(string(rune('a'+i)), sliceNow.Add(-time.Duration(i)*time.Hour)))
	}

	b := DefaultBudget()
	b.Now = sliceNow

	got := Slice(in, b)
	if len(got) != 5 {
		t.Errorf("Slice with 20 servable notes: got %d, want exactly 5", len(got))
	}
}

// TestSlice_TotalByteCeiling measures the bytes that actually reach the hook — the canonical
// emitted form of each returned note — because a test that measured post-truncation bodies
// would find "<= 4096" trivially true and would stay green if the ceiling were never
// implemented at all.
func TestSlice_TotalByteCeiling(t *testing.T) {
	// Three-byte runes, so a 400-RUNE excerpt is ~1200 BYTES and the ceiling genuinely binds.
	fat := strings.Repeat("★", 4000)

	var in []Note
	for i := 0; i < 5; i++ {
		n := servable(string(rune('a'+i)), sliceNow.Add(-time.Duration(i)*time.Hour))
		n.Body = fat
		in = append(in, n)
	}

	b := DefaultBudget()
	b.Now = sliceNow

	got := Slice(in, b)
	if len(got) == 0 {
		t.Fatal("Slice returned nothing; the ceiling must bound the block, not empty it")
	}
	if len(got) >= 5 {
		t.Errorf("Slice returned %d notes; the 4096-byte ceiling should have bound before K did", len(got))
	}

	total := 0
	for _, n := range got {
		total += len(Emit(n))
	}
	if total > DefaultTotalBytes {
		t.Errorf("emitted total = %d bytes, want <= %d", total, DefaultTotalBytes)
	}
}

func TestSlice_ExcerptCharCeiling(t *testing.T) {
	n := servable("long", sliceNow)
	n.Body = strings.Repeat("x", 5000)

	b := DefaultBudget()
	b.Now = sliceNow

	got := Slice([]Note{n}, b)
	if len(got) != 1 {
		t.Fatalf("Slice: got %d notes, want 1", len(got))
	}
	if runes := len([]rune(got[0].Body)); runes > DefaultExcerptChars+1 { // +1 for the truncation marker
		t.Errorf("excerpt = %d runes, want <= %d (+1 marker)", runes, DefaultExcerptChars)
	}
	if !strings.HasPrefix(got[0].Body, strings.Repeat("x", 100)) {
		t.Error("excerpt must be the HEAD of the body")
	}
}

// TestSlice_ZeroValueBudgetYieldsDefaults guards the transcript.Options.normalised() failure
// mode: a caller passing Budget{} must get the pinned ceilings, not zeros. A zero-value budget
// that silently returned nothing would present as "memory injection quietly stopped working".
func TestSlice_ZeroValueBudgetYieldsDefaults(t *testing.T) {
	var in []Note
	for i := 0; i < 20; i++ {
		in = append(in, servable(string(rune('a'+i)), sliceNow.Add(-time.Duration(i)*time.Hour)))
	}

	got := Slice(in, Budget{})
	if len(got) != DefaultK {
		t.Errorf("Slice with a zero-value Budget: got %d notes, want %d", len(got), DefaultK)
	}
}

// TestSlice_RanksFormulaMatchFirstThenRecency pins scale.md:28-29. Without a scope key on
// Budget this ranking is inexpressible inside the Slice(notes, Budget) signature, and a
// recency-only implementation silently deletes the AC-515-2 relevance mechanism.
func TestSlice_RanksFormulaMatchFirstThenRecency(t *testing.T) {
	mk := func(id, formula string, ageHours int) Note {
		n := servable(id, sliceNow.Add(-time.Duration(ageHours)*time.Hour))
		n.Formula = formula
		return n
	}
	// Interleaved by age so a recency-only ranking produces a visibly different order.
	in := []Note{
		mk("other-new", "other-formula", 1),
		mk("match-old", "rootcause-all", 2),
		mk("other-old", "other-formula", 3),
		mk("match-new", "rootcause-all", 0),
	}

	b := DefaultBudget()
	b.Now = sliceNow
	b.Formula = "rootcause-all"

	got := idsOf(Slice(in, b))
	want := []string{"match-new", "match-old", "other-new", "other-old"}
	if !equalIDs(got, want) {
		t.Errorf("Slice ranking: got %v, want %v", got, want)
	}

	// Stable: the same input must produce the same order every time.
	if again := idsOf(Slice(in, b)); !equalIDs(again, got) {
		t.Errorf("Slice is not stable: first %v, second %v", got, again)
	}
}

func TestSlice_NoFormulaScopeRanksByRecencyAlone(t *testing.T) {
	newest := servable("new", sliceNow.Add(-1*time.Hour))
	// The newest note is the one carrying a formula, and the others carry none. That asymmetry is
	// what makes the empty-scope guard visible: without it, "does this note's formula equal the
	// budget's" reads as "is this note's formula empty" and the scoped note sinks to last. With
	// every note unscoped the comparison is uniformly true and the guard is unobservable.
	newest.Formula = "rootcause-all"

	in := []Note{
		servable("old", sliceNow.Add(-3*time.Hour)),
		newest,
		servable("mid", sliceNow.Add(-2*time.Hour)),
	}

	b := DefaultBudget()
	b.Now = sliceNow

	got := idsOf(Slice(in, b))
	want := []string{"new", "mid", "old"}
	if !equalIDs(got, want) {
		t.Errorf("Slice without a formula scope: got %v, want %v", got, want)
	}
}

// TestBudgetDefaultsArePinned spells the numbers literally. A test that read the constant into
// its own want would prove nothing (scale.md:33: "design defaults set here, not values found
// in the codebase ... pinned by tests").
func TestBudgetDefaultsArePinned(t *testing.T) {
	if DefaultK != 5 {
		t.Errorf("DefaultK = %d, want 5", DefaultK)
	}
	if DefaultExcerptChars != 400 {
		t.Errorf("DefaultExcerptChars = %d, want 400", DefaultExcerptChars)
	}
	if DefaultTotalBytes != 4096 {
		t.Errorf("DefaultTotalBytes = %d, want 4096", DefaultTotalBytes)
	}

	b := DefaultBudget()
	if b.K != 5 || b.ExcerptChars != 400 || b.TotalBytes != 4096 {
		t.Errorf("DefaultBudget() = {K:%d Excerpt:%d Total:%d}, want {5 400 4096}", b.K, b.ExcerptChars, b.TotalBytes)
	}
}

func TestTTLDefaultsArePinned(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		noteType string
		want     time.Duration
	}{
		{TypeGotcha, 90 * day},
		{TypeOutcome, 90 * day},
		{TypeOps, 180 * day},
		{TypeModelBehavior, 180 * day},
		{TypeImprovement, 0},
		{"something-nobody-defined", 90 * day},
	}
	for _, tc := range cases {
		if got := TTLForType(tc.noteType); got != tc.want {
			t.Errorf("TTLForType(%q) = %v, want %v", tc.noteType, got, tc.want)
		}
	}
}

// TestExpiresFor_ImprovementIsNotBornExpired guards the "0 = none" trap: a zero TTL fed into
// created.Add(ttl) yields Expires == Created, i.e. an improvement note that is expired the
// instant it is written — the exact inverse of scale.md:170-173 ("empty for improvement notes
// ... nagged instead").
func TestExpiresFor_ImprovementIsNotBornExpired(t *testing.T) {
	created := sliceNow

	if got := ExpiresFor(created, TypeImprovement); !got.IsZero() {
		t.Errorf("ExpiresFor(improvement) = %v, want the zero time (no TTL)", got)
	}
	if got := ExpiresFor(created, TypeGotcha); !got.Equal(created.Add(90 * 24 * time.Hour)) {
		t.Errorf("ExpiresFor(gotcha) = %v, want created+90d", got)
	}

	// And the whole point: an improvement note is servable, not born expired.
	n := servable("improvement", created)
	n.Type = TypeImprovement
	n.Expires = ExpiresFor(created, TypeImprovement)

	b := DefaultBudget()
	b.Now = created.Add(365 * 24 * time.Hour)

	if got := Slice([]Note{n}, b); len(got) != 1 {
		t.Errorf("an improvement note is unservable a year later: got %d notes, want 1", len(got))
	}
}

// TestSlice_TiesResolveByInputOrder pins the STABILITY of the ranking rather than the ranking.
// Every other ranking fixture is a total order, under which a stable and an unstable sort are
// indistinguishable — so this is the only place "two identical sessions get the same block" can
// fail. The fixture is deliberately large and interleaved: a handful of tied elements would be
// ordered correctly by an unstable sort too, because small inputs fall to insertion sort.
func TestSlice_TiesResolveByInputOrder(t *testing.T) {
	newer := sliceNow.Add(-1 * time.Hour)
	older := sliceNow.Add(-2 * time.Hour)

	var in []Note
	var wantNewer, wantOlder []string
	for i := 0; i < 16; i++ {
		a := "newer-" + strconv.Itoa(i)
		b := "older-" + strconv.Itoa(i)
		in = append(in, servable(a, newer), servable(b, older))
		wantNewer = append(wantNewer, a)
		wantOlder = append(wantOlder, b)
	}

	b := Budget{K: len(in), TotalBytes: 1 << 20, ExcerptChars: DefaultExcerptChars, Now: sliceNow}
	want := append(append([]string{}, wantNewer...), wantOlder...)

	got := idsOf(Slice(in, b))
	if !equalIDs(got, want) {
		t.Errorf("tied notes must keep their input order:\ngot  %v\nwant %v", got, want)
	}
}

// TestSlice_DoesNotMutateItsInput pins the "never mutates the slice it is given" claim from BOTH
// directions, because the claim has two independent ways to break and each is invisible to the
// other's fixture: excerpting can write through the note copies, and the servable set can be
// built in place (servable := notes[:0]) so that filtering and sorting compact and reorder the
// caller's own backing array. The second one needs a filtered-out note at index 0 — with the
// servable notes first, in-place compaction is the identity and proves nothing.
func TestSlice_DoesNotMutateItsInput(t *testing.T) {
	body := strings.Repeat("x", 5000)
	n := servable("long", sliceNow)
	n.Body = body
	in := []Note{n}

	got := Slice(in, Budget{Now: sliceNow})
	if len(got) != 1 {
		t.Fatalf("Slice: got %d notes, want 1", len(got))
	}
	if got[0].Body == body {
		t.Fatal("the fixture was not excerpted; the test proves nothing")
	}
	if in[0].Body != body {
		t.Errorf("Slice truncated the caller's note: got %d runes, want %d", len([]rune(in[0].Body)), len(body))
	}

	// The caller's slice must survive filtering and ranking intact, element for element. The
	// excluded note is deliberately FIRST and the servable ones are out of recency order, so
	// both an in-place compaction and an in-place sort would show up here.
	dropped := servable("dropped", sliceNow.Add(-time.Hour))
	dropped.Status = StatusGraduated
	older := servable("older", sliceNow.Add(-3*time.Hour))
	newer := servable("newer", sliceNow.Add(-2*time.Hour))

	full := []Note{dropped, older, newer}
	before := append([]Note(nil), full...)

	if ids := idsOf(Slice(full, Budget{Now: sliceNow})); !equalIDs(ids, []string{"newer", "older"}) {
		t.Errorf("Slice: got %v, want [newer older]", ids)
	}
	for i := range full {
		if full[i].ID != before[i].ID || full[i].Status != before[i].Status || full[i].Body != before[i].Body {
			t.Errorf("Slice rearranged the caller's slice at %d: got %q/%q, want %q/%q",
				i, full[i].ID, full[i].Status, before[i].ID, before[i].Status)
		}
	}
}

// TestSlice_MissingClockStillSuppressesExpiredNotes pins normalised()'s wall-clock fallback. A
// caller that forgets Now must not silently get "no TTL filtering": that direction serves stale
// notes forever, and staleness is the whole reason read-time suppression exists.
func TestSlice_MissingClockStillSuppressesExpiredNotes(t *testing.T) {
	longAgo := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	stale := servable("stale", longAgo)
	stale.Expires = longAgo.Add(24 * time.Hour) // expired under any real clock
	fresh := servable("fresh", longAgo)         // no TTL at all

	got := idsOf(Slice([]Note{stale, fresh}, Budget{})) // zero Budget: no clock supplied
	want := []string{"fresh"}
	if !equalIDs(got, want) {
		t.Errorf("Slice with no clock: got %v, want %v", got, want)
	}
}

// TestSlice_ExpiryBoundaryIsExclusive — a note whose expiry instant IS now has expired. This is
// the entire difference between !Expires.After(now) and Expires.Before(now), and no other
// fixture sets the two equal.
func TestSlice_ExpiryBoundaryIsExclusive(t *testing.T) {
	atBoundary := servable("at-boundary", sliceNow.Add(-time.Hour))
	atBoundary.Expires = sliceNow
	justAfter := servable("one-nanosecond-left", sliceNow.Add(-time.Hour))
	justAfter.Expires = sliceNow.Add(time.Nanosecond)

	b := DefaultBudget()
	b.Now = sliceNow

	got := idsOf(Slice([]Note{atBoundary, justAfter}, b))
	want := []string{"one-nanosecond-left"}
	if !equalIDs(got, want) {
		t.Errorf("at the expiry boundary: got %v, want %v", got, want)
	}
}

// TestSlice_ByteCeilingStopsRatherThanSkips pins the documented "the result is a PREFIX of the
// ranking" semantic, which is what makes the remainder reportable as "…and N more". A ceiling
// that skipped the note that did not fit and kept going would return an arbitrary subset, and a
// fixture of equal-sized notes cannot tell the two apart.
func TestSlice_ByteCeilingStopsRatherThanSkips(t *testing.T) {
	fatNewest := servable("fat-newest", sliceNow.Add(-1*time.Hour))
	fatNewest.Body = strings.Repeat("y", 3000)
	fatMiddle := servable("fat-middle", sliceNow.Add(-2*time.Hour))
	fatMiddle.Body = strings.Repeat("y", 3000)
	smallOldest := servable("small-oldest", sliceNow.Add(-3*time.Hour))

	b := DefaultBudget()
	b.ExcerptChars = 3000 // the excerpt must not be what bounds this fixture
	b.Now = sliceNow

	got := idsOf(Slice([]Note{fatNewest, fatMiddle, smallOldest}, b))
	want := []string{"fat-newest"}
	if !equalIDs(got, want) {
		t.Errorf("Slice: got %v, want %v — the block must stop at the overflow, not skip past it", got, want)
	}
}

// TestSlice_AnOversizedNoteDoesNotStarveTheBlock — Evidence is unbounded and Phase 2 populates
// it from CLI input, so a single note whose canonical form alone exceeds the block ceiling is
// reachable. Under-serving is the correct failure direction; serving NOTHING while servable
// notes exist is a step past it, and it would present as injection having quietly stopped.
func TestSlice_AnOversizedNoteDoesNotStarveTheBlock(t *testing.T) {
	fat := servable("fat", sliceNow.Add(-1*time.Hour)) // ranks FIRST, so it is hit first
	for i := 0; i < 400; i++ {
		fat.Evidence = append(fat.Evidence, "issue#"+strconv.Itoa(i))
	}
	thin := servable("thin", sliceNow.Add(-2*time.Hour))

	if size := len(Emit(fat)); size <= DefaultTotalBytes {
		t.Fatalf("the fixture emits %d bytes and fits inside the %d-byte block; the test proves nothing",
			size, DefaultTotalBytes)
	}

	b := DefaultBudget()
	b.Now = sliceNow

	got := idsOf(Slice([]Note{fat, thin}, b))
	want := []string{"thin"}
	if !equalIDs(got, want) {
		t.Errorf("Slice: got %v, want %v", got, want)
	}

	// The same note at rank 2, which is the case a "skip only when nothing has been served yet"
	// implementation gets wrong: it would stop at the oversized note and drop everything below it
	// while reporting a block that looks complete.
	first := servable("first", sliceNow)
	last := servable("last", sliceNow.Add(-3*time.Hour))
	midFat := fat
	midFat.ID = "mid-fat"
	midFat.Created = sliceNow.Add(-1 * time.Hour)

	got = idsOf(Slice([]Note{first, midFat, last}, b))
	want = []string{"first", "last"}
	if !equalIDs(got, want) {
		t.Errorf("with the oversized note at rank 2: got %v, want %v", got, want)
	}
}

// TestSlice_CeilingBoundariesAreInclusive pins the two off-by-ones the ceilings can carry. A
// note that fits EXACTLY must be served and a body of exactly ExcerptChars runes must not be
// marked as truncated — the ellipsis is a disclosure, and disclosing a truncation that did not
// happen is as wrong as hiding one that did. The package pins the expiry boundary for the same
// reason; these are the same kind of claim.
func TestSlice_CeilingBoundariesAreInclusive(t *testing.T) {
	exact := servable("exact-fit", sliceNow)
	size := len(Emit(exact))

	b := DefaultBudget()
	b.Now = sliceNow
	b.TotalBytes = size // the note fills the block precisely

	if got := idsOf(Slice([]Note{exact}, b)); !equalIDs(got, []string{"exact-fit"}) {
		t.Errorf("a note that fits exactly: got %v, want [exact-fit] (block = %d bytes)", got, size)
	}
	b.TotalBytes = size - 1
	if got := idsOf(Slice([]Note{exact}, b)); len(got) != 0 {
		t.Errorf("a note one byte too large: got %v, want nothing", got)
	}

	atLimit := servable("at-limit", sliceNow)
	atLimit.Body = strings.Repeat("z", DefaultExcerptChars)
	b = DefaultBudget()
	b.Now = sliceNow

	got := Slice([]Note{atLimit}, b)
	if len(got) != 1 {
		t.Fatalf("Slice: got %d notes, want 1", len(got))
	}
	if got[0].Body != atLimit.Body {
		t.Errorf("a body of exactly %d runes was marked truncated: got %q", DefaultExcerptChars, got[0].Body)
	}
}

// TestSlice_ZeroValueBudgetAppliesTheExcerptCeiling is the third leg of normalised(). K and
// TotalBytes are pinned by the tests above; without this one, dropping ExcerptChars from
// normalised() leaves every test green and ships an unbounded per-note body into the hook.
// The assertion is exact rather than an upper bound: an ExcerptChars of 0 truncates every body
// to the ellipsis alone, which satisfies "<= 400" while serving nothing.
func TestSlice_ZeroValueBudgetAppliesTheExcerptCeiling(t *testing.T) {
	n := servable("long", sliceNow)
	n.Body = strings.Repeat("x", 5000)

	got := Slice([]Note{n}, Budget{Now: sliceNow})
	if len(got) != 1 {
		t.Fatalf("Slice: got %d notes, want 1", len(got))
	}
	if want := strings.Repeat("x", DefaultExcerptChars) + "…"; got[0].Body != want {
		t.Errorf("zero-value Budget: excerpt is %d runes, want exactly %d plus the truncation marker",
			len([]rune(got[0].Body)), DefaultExcerptChars)
	}
}

func idsOf(notes []Note) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.ID)
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestGraduationDueThresholdIsPinned holds the second clock the vault runs on. TTLImprovement = 0
// means the expiry machinery will never reclaim an improvement note, so this is the only thing
// standing between "nagged rather than quietly expired" and a promise with nothing behind it.
//
// The number is a design default (.designs/515/scale.md:130 fixes none), so the pin is a one-line
// edit plus a test update rather than silent drift — the same posture TestBudgetDefaultsArePinned
// takes. What the relations below actually guard is the shape: a threshold at or past a TTL would
// let the vault expire a note it had never got round to asking about, and one at zero would nag
// about a note written a second ago.
func TestGraduationDueThresholdIsPinned(t *testing.T) {
	if got, want := GraduationDueAfter, 30*24*time.Hour; got != want {
		t.Errorf("GraduationDueAfter = %v, want %v", got, want)
	}
	if GraduationDueAfter <= 0 {
		t.Fatal("a non-positive threshold would nag about every note the moment it was written")
	}
	// A question must arrive before any answer would have expired, or the vault would be
	// simultaneously nagging and expiring the same generation of notes.
	for _, ttl := range []time.Duration{TTLGotcha, TTLOutcome, TTLOps, TTLModelBehavior} {
		if GraduationDueAfter >= ttl {
			t.Errorf("GraduationDueAfter (%v) must arrive before the shortest TTL (%v)", GraduationDueAfter, ttl)
		}
	}
}
