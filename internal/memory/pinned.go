package memory

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

const PinnedBlockHeader = "Standing operator rulings (pinned by the operator; in force until withdrawn):\n"

// sentinelFence neutralizes the frame sentinel in any content a `<system-reminder>` block
// carries in from elsewhere — a memory note's body or attribution, a mail message's body, subject
// or sender. Such content can hold the literal `<system-reminder>` / `</system-reminder>` wrapper,
// which would otherwise close the provenance frame early and let the text after it re-enter the
// session OUTSIDE the framing that security.md T4 mitigation #1 depends on. Escaping every angle
// bracket is bypass-proof: no sentinel of any spelling or casing survives, so a block can hold only
// its own single real wrapper.
//
// One replacer for every injecting writer, deliberately: a second copy is a second place for the
// escape set to drift, and the frame is only as strong as its weakest writer.
var sentinelFence = strings.NewReplacer("<", "&lt;", ">", "&gt;")

func FenceSentinel(s string) string { return sentinelFence.Replace(s) }

// stripControl drops the runes statusline.SanitizeToken strips, without its 64-rune cap (this
// package may not import statusline), plus the line and paragraph separators, which that set
// keeps and which would break a pinned line in two.
func stripControl(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func Subject(n Note) string {
	for _, line := range strings.Split(n.Body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
		if line != "" {
			return line
		}
	}
	return ""
}

func PinnedSubject(n Note) string { return strings.TrimSpace(stripControl(Subject(n))) }

func pinnedDate(n Note) string {
	if n.Created.IsZero() {
		return "undated"
	}
	return n.Created.UTC().Format(time.DateOnly)
}

// PinnedLine is all a session receives of a pinned note: "- [2026-10-01] <subject>\n".
func PinnedLine(n Note) string {
	return "- [" + pinnedDate(n) + "] " + FenceSentinel(PinnedSubject(n)) + "\n"
}

func PinnedOverBudgetLine(notShown int) string {
	return fmt.Sprintf("standing rulings over budget: %d not shown — run af memory status\n", notShown)
}

// PinnedBlockBytes is the rendered size of the block, header, closing line and separator included,
// because the 3,072 B ceiling is a ceiling on what the session reads.
func PinnedBlockBytes(shown []Note, notShown int) int {
	if len(shown) == 0 && notShown == 0 {
		return 0
	}
	size := len(PinnedBlockHeader)
	for _, n := range shown {
		size += len(PinnedLine(n))
	}
	if notShown > 0 {
		size += len(PinnedOverBudgetLine(notShown))
	}
	return size + 1
}

// Pins ignores the TTL: a standing ruling is in force until the operator withdraws it (D-14).
// Oldest first, so a vault over its ceiling serves its longest-standing rulings.
func Pins(notes []Note, now time.Time) []Note {
	var pins []Note
	for _, n := range notes {
		if n.Standing && !n.Malformed && n.Status == StatusActive {
			pins = append(pins, n)
		}
	}
	sort.SliceStable(pins, func(i, j int) bool {
		a, b := pins[i].Created, pins[j].Created
		if a.IsZero() != b.IsZero() {
			return b.IsZero()
		}
		return a.Before(b)
	})
	return pins
}

// PinsShown scans downward because the block size is not monotone in k: the closing line is
// charged at every k < len(pins) and not at k = len(pins), so a scan that climbs and stops at the
// first k over the ceiling can stop short of a vault that fits whole.
func PinsShown(pins []Note, standingBytes int) int {
	for k := len(pins); k > 0; k-- {
		if PinnedBlockBytes(pins[:k], len(pins)-k) <= standingBytes {
			return k
		}
	}
	return 0
}

// Attribution is the one-line provenance a note is never served without.
func Attribution(n Note, now time.Time) string {
	typ := n.Type
	if typ == "" {
		typ = "-"
	}
	parts := []string{"type=" + typ}
	if !n.Created.IsZero() {
		parts = append(parts, fmt.Sprintf("recorded %dd ago", int(now.Sub(n.Created).Hours()/24)))
	}
	if n.Formula != "" {
		parts = append(parts, "formula="+n.Formula)
	}
	if n.Run != "" {
		parts = append(parts, "run="+n.Run)
	}
	if len(n.Evidence) > 0 {
		parts = append(parts, "evidence: "+strings.Join(n.Evidence, ", "))
	}
	return strings.Join(parts, " · ")
}

// InjectedEntry is a newest note exactly as the session receives it, so that what Slice charges
// is what the renderer writes.
func InjectedEntry(n Note, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s] %s\n", FenceSentinel(n.ID), FenceSentinel(Attribution(n, now)))
	for _, line := range strings.Split(strings.TrimRight(n.Body, "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", FenceSentinel(line))
	}
	b.WriteString("\n")
	return b.String()
}
