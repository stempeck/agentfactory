package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestParseIntegrationDuration pins the manifest duration grammar (spec L245-247): `0` or
// ^[1-9][0-9]*(s|m|h|d)$, with the exact error text `invalid duration %q: want 0 or <n>s|m|h|d`.
// The crons parser (ParseCompactDuration) and its "invalid every" text stay untouched (H3-1, A3).
func TestParseIntegrationDuration(t *testing.T) {
	accepts := []struct {
		in   string
		want time.Duration
	}{
		{"0", 0},
		{"30s", 30 * time.Second},
		{"10m", 10 * time.Minute},
		{"1h", time.Hour},
		{"2d", 48 * time.Hour},
		{"90s", 90 * time.Second},
	}
	for _, tc := range accepts {
		t.Run("accepts_"+tc.in, func(t *testing.T) {
			got, err := ParseIntegrationDuration(tc.in)
			if err != nil {
				t.Fatalf("ParseIntegrationDuration(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseIntegrationDuration(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	rejects := []struct{ name, in string }{
		{"week_unit", "1w"},
		{"negative", "-1s"},
		{"empty", ""},
		{"double_zero", "00"},
		{"zero_with_unit", "0s"},
		{"leading_zero", "010m"},
		{"fraction", "1.5h"},
		{"unit_only", "h"},
		{"compound", "1h30m"},
		{"uppercase_unit", "10M"},
		{"surrounding_space", " 10m"},
		{"trailing_newline", "10m\n"},
		{"plus_sign", "+5m"},
	}
	for _, tc := range rejects {
		t.Run("rejects_"+tc.name, func(t *testing.T) {
			got, err := ParseIntegrationDuration(tc.in)
			if err == nil {
				t.Fatalf("ParseIntegrationDuration(%q) = %v, want an error", tc.in, got)
			}
			want := fmt.Sprintf("invalid duration %q: want 0 or <n>s|m|h|d", tc.in)
			if err.Error() != want {
				t.Errorf("ParseIntegrationDuration(%q) error:\n got %q\nwant %q", tc.in, err.Error(), want)
			}
		})
	}

	t.Run("rejects_overflow", func(t *testing.T) {
		// time.Duration is int64 nanoseconds: an unguarded multiply wraps (duration.go:39-43 guards
		// the crons parser the same way), and a wrapped negative timeout would be unbounded.
		const in = "9223372036854775807d"
		got, err := ParseIntegrationDuration(in)
		if err == nil {
			t.Fatalf("ParseIntegrationDuration(%q) = %v, want an overflow error", in, got)
		}
		if !strings.HasPrefix(err.Error(), fmt.Sprintf("invalid duration %q", in)) {
			t.Errorf("overflow error must start with `invalid duration %q`: %v", in, err)
		}
	})

	t.Run("compact_duration_text_unchanged", func(t *testing.T) {
		// Protective (DO-NOT-CHANGE): the crons parser still rejects the manifest's own forms with
		// its original "invalid every" text, so the new grammar was added beside it, not into it.
		for _, in := range []string{"30s", "0"} {
			_, err := ParseCompactDuration(in)
			want := fmt.Sprintf(`invalid every %q: expected <integer><unit> with unit m, h, or d (e.g. "4h", "14d")`, in)
			if err == nil || err.Error() != want {
				t.Errorf("ParseCompactDuration(%q) error = %v, want %q", in, err, want)
			}
		}
		if d, err := ParseCompactDuration("4h"); err != nil || d != 4*time.Hour {
			t.Errorf(`ParseCompactDuration("4h") = %v, %v; want 4h, nil`, d, err)
		}
	})
}
