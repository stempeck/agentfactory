package formula

import (
	"strconv"
	"strings"
)

// BackoffConfig defines exponential backoff parameters for wait-type steps.
type BackoffConfig struct {
	Base       string // Base interval (e.g., "30s")
	Multiplier int    // Multiplier for exponential growth (default: 2)
	Max        string // Maximum interval cap (e.g., "10m")
}

// ParseBackoffConfig parses a backoff configuration string.
// Expected format: "base=30s, multiplier=2, max=10m"
// Returns nil if no base is specified (incomplete config).
func ParseBackoffConfig(configStr string) *BackoffConfig {
	cfg := &BackoffConfig{
		Multiplier: 2, // Default multiplier
	}

	// Split by comma and parse key=value pairs
	parts := strings.Split(configStr, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Split by = to get key and value
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}

		key := strings.TrimSpace(strings.ToLower(kv[0]))
		value := strings.TrimSpace(kv[1])

		switch key {
		case "base":
			cfg.Base = value
		case "multiplier":
			if m, err := strconv.Atoi(value); err == nil {
				cfg.Multiplier = m
			}
		case "max":
			cfg.Max = value
		}
	}

	// Return nil if no base was specified (incomplete config)
	if cfg.Base == "" {
		return nil
	}

	return cfg
}
