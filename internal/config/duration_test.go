package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestParseCompactDuration pins the crons frequency grammar (issue #610, C-2). Two properties are
// load-bearing beyond "does it error":
//
//   - The VALUE is asserted, not just the absence of an error. An implementation that maps `d` to
//     time.Hour passes every err==nil check and silently turns "every 14 days" into "every 14 hours".
//   - Overflow is rejected. With the obvious strconv.Atoi + n*time.Minute shape, "9223372036854775807m"
//     yields -1m0s and "153722867280912931m" yields 52s — a cron that fires continuously, which is the
//     exact mirror of the "silently never fires" failure this issue exists to eliminate.
func TestParseCompactDuration(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
		// wantMsg is the required substring of the error message, carried to the end of the
		// actionable tail: api.md:96 pins the full string, and the examples are the part an
		// operator fixing a hand-edited dispatch.json actually reads.
		wantMsg string
	}{
		// --- accepted, with the exact value ---
		{name: "one minute", input: "1m", want: time.Minute},
		{name: "one hour", input: "1h", want: time.Hour},
		{name: "one day is exactly 24 hours, not a calendar day", input: "1d", want: 24 * time.Hour},
		{name: "fourteen days", input: "14d", want: 336 * time.Hour},
		{name: "the design's canonical example", input: "4h", want: 4 * time.Hour},
		{name: "a multi-digit minute count", input: "90m", want: 90 * time.Minute},
		{name: "the largest representable minute count", input: "153722867m", want: 153722867 * time.Minute},
		{name: "the largest representable day count", input: "106751d", want: 106751 * 24 * time.Hour},

		// --- rejected: outside the unit whitelist ---
		{name: "rejects a week unit", input: "1w", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a stdlib second unit", input: "1s", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a stdlib nanosecond unit", input: "1ns", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects an arbitrary unit", input: "1x", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},

		// --- rejected: outside the number grammar ---
		{name: "rejects a fraction", input: "1.5h", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects zero, which would be a hot loop", input: "0m", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a negative sign", input: "-1d", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects an explicit plus sign", input: "+1h", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a leading zero", input: "01h", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects an Arabic-Indic digit", input: "١h", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},

		// --- rejected: shape ---
		{name: "rejects the empty string", input: "", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a bare count with no unit", input: "1", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a bare hour unit with no count", input: "h", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a bare minute unit with no count", input: "m", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a compound term the stdlib would accept", input: "1h30m", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},

		// --- rejected: case, whitespace, framing ---
		{name: "rejects an uppercase hour", input: "1H", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects an uppercase day", input: "1D", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects an uppercase M, the month/minute ambiguity the grammar exists to kill", input: "1M", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects surrounding whitespace", input: " 1h ", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},
		{name: "rejects a trailing newline", input: "1h\n", wantErr: true, wantMsg: `m, h, or d (e.g. "4h", "14d")`},

		// --- rejected: overflow (a wrapped duration fires continuously) ---
		{name: "rejects a minute count one past the representable maximum", input: "153722868m", wantErr: true, wantMsg: "too large (the maximum is about 292 years)"},
		{name: "rejects a day count one past the representable maximum", input: "106752d", wantErr: true, wantMsg: "too large (the maximum is about 292 years)"},
		{name: "rejects an hour count one past the representable maximum", input: "2562048h", wantErr: true, wantMsg: "too large (the maximum is about 292 years)"},
		{name: "rejects max-int64 minutes, which naively wraps to -1m", input: "9223372036854775807m", wantErr: true, wantMsg: "too large (the maximum is about 292 years)"},
		{name: "rejects a count that naively wraps to 52s", input: "153722867280912931m", wantErr: true, wantMsg: "too large (the maximum is about 292 years)"},
		{name: "rejects a day count that naively wraps negative", input: "106751991167300d", wantErr: true, wantMsg: "too large (the maximum is about 292 years)"},
		{name: "rejects a count too large for int64 itself", input: "99999999999999999999m", wantErr: true, wantMsg: "too large (the maximum is about 292 years)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCompactDuration(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseCompactDuration(%q) must be rejected, got %v", tc.input, got)
				}
				if !strings.Contains(err.Error(), tc.wantMsg) {
					t.Errorf("error for %q must name the rule (%q); got: %v", tc.input, tc.wantMsg, err)
				}
				// api.md:96 pins the message shape: the offending value is echoed, quoted.
				if !strings.Contains(err.Error(), fmt.Sprintf("%q", tc.input)) {
					t.Errorf("error must echo the offending input %q in quotes; got: %v", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCompactDuration(%q) must be accepted; got: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseCompactDuration(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if got <= 0 {
				t.Errorf("ParseCompactDuration(%q) returned a non-positive duration %v — a cron on this interval fires continuously", tc.input, got)
			}
		})
	}
}

// The stdlib parser accepts "1h30m" and rejects "1d"; this asserts the grammar is genuinely ours and
// not a delegation, from the outside, without reading the implementation.
func TestParseCompactDuration_IsNotTimeParseDuration(t *testing.T) {
	if _, err := ParseCompactDuration("1h30m"); err == nil {
		t.Error(`"1h30m" is accepted by time.ParseDuration and must be rejected by this grammar`)
	}
	if _, err := ParseCompactDuration("1d"); err != nil {
		t.Errorf(`"1d" is rejected by time.ParseDuration and must be accepted by this grammar; got: %v`, err)
	}
}
