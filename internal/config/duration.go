package config

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
)

// compactDuration anchors the whole string and uses ASCII-only digit and unit classes, so a leading
// zero, a sign, surrounding whitespace, a trailing newline, an uppercase unit, a non-ASCII digit and
// a compound term are all non-matches rather than special cases. Mirrors the validAgentName idiom at
// config.go:59.
var compactDuration = regexp.MustCompile(`^([1-9][0-9]*)(m|h|d)$`)

// ParseCompactDuration parses the crons frequency grammar (issue #610, C-2): a positive decimal
// integer followed by exactly one unit — m (minute), h (hour), d (day = 24h). No fractions, no
// compounds, no other units.
//
// It deliberately does not call time.ParseDuration. The stdlib parser has no "d" unit and accepts
// compound terms like "1h30m", so delegating would validate operator config against a grammar
// wider than the one documented. `d` is exactly 24 hours — no calendar arithmetic.
func ParseCompactDuration(s string) (time.Duration, error) {
	parts := compactDuration.FindStringSubmatch(s)
	if parts == nil {
		return 0, fmt.Errorf(`invalid every %q: expected <integer><unit> with unit m, h, or d (e.g. "4h", "14d")`, s)
	}

	var unit time.Duration
	switch parts[2] {
	case "m":
		unit = time.Minute
	case "h":
		unit = time.Hour
	case "d":
		unit = 24 * time.Hour
	}

	// time.Duration is int64 nanoseconds, so an unguarded multiply wraps silently: "9223372036854775807m"
	// becomes -1m and "153722867280912931m" becomes 52s. Either one turns a schedule into a hot loop —
	// the mirror of the never-fires failure this feature exists to eliminate — so the overflow is
	// rejected rather than saturated.
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || n > int64(math.MaxInt64)/int64(unit) {
		return 0, fmt.Errorf("invalid every %q: duration too large (the maximum is about 292 years)", s)
	}
	return time.Duration(n) * unit, nil
}
