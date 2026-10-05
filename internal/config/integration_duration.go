package config

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
)

// integrationDuration is the manifest grammar (spec L245-247). It differs from compactDuration only
// by the extra s unit and the bare "0", which the manifest needs for `timeout = "30s"` and
// `fresh_for = "0"`; ParseCompactDuration stays the crons grammar (H3-1, A3).
var integrationDuration = regexp.MustCompile(`^([1-9][0-9]*)(s|m|h|d)$`)

// ParseIntegrationDuration parses an af-integration.toml duration: "0" or a positive decimal
// integer followed by exactly one unit — s, m, h, or d (day = 24h).
func ParseIntegrationDuration(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	parts := integrationDuration.FindStringSubmatch(s)
	if parts == nil {
		return 0, fmt.Errorf("invalid duration %q: want 0 or <n>s|m|h|d", s)
	}

	var unit time.Duration
	switch parts[2] {
	case "s":
		unit = time.Second
	case "m":
		unit = time.Minute
	case "h":
		unit = time.Hour
	case "d":
		unit = 24 * time.Hour
	}

	// A wrapped multiply would turn a timeout negative, i.e. unbounded (see ParseCompactDuration).
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || n > int64(math.MaxInt64)/int64(unit) {
		return 0, fmt.Errorf("invalid duration %q: duration too large (the maximum is about 292 years)", s)
	}
	return time.Duration(n) * unit, nil
}
