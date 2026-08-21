package memory

import (
	"testing"
	"time"
)

// TestFableIncrPr630_ITEM2_CreatedAcceptsBareDate pins the Consider on PR #630
// (thread PRRT_kwDORt0n_M6ZksBM): `created` must accept the bare date-only form its sibling
// `expires` already accepts, so an operator who simplifies `created:` to `2026-08-15` in Obsidian
// does not silently malform the note (which would drop it from Slice and make it unmarkable).
// Before the fix `created` parsed with RFC3339 only, so a bare date set Malformed=true.
func TestFableIncrPr630_ITEM2_CreatedAcceptsBareDate(t *testing.T) {
	raw := "---\nid: n1\nagent: manager\ntype: gotcha\ncreated: 2026-08-15\n---\nthe body\n"

	n := Parse([]byte(raw))
	if n.Malformed {
		t.Fatalf("bare-date created flagged Malformed; want tolerated like expires:\n%s", raw)
	}
	want := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	if !n.Created.Equal(want) {
		t.Errorf("Created: got %v, want %v", n.Created, want)
	}
}
