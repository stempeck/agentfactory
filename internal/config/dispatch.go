package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// DispatchConfig holds the contents of .agentfactory/dispatch.json
type DispatchConfig struct {
	Repos            []string          `json:"repos"`
	TriggerLabel     string            `json:"trigger_label"`
	NotifyOnComplete string            `json:"notify_on_complete"`
	Mappings         []DispatchMapping `json:"mappings"`
	IntervalSecs     int               `json:"interval_seconds"`
	RetryAfterSecs   int               `json:"retry_after_seconds"`
}

// DispatchMapping maps a GitHub label to an agent name.
type DispatchMapping struct {
	Label string `json:"label"`
	Agent string `json:"agent"`
}

// LoadDispatchConfig loads and validates .agentfactory/dispatch.json.
func LoadDispatchConfig(root string) (*DispatchConfig, error) {
	path := DispatchConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading dispatch config: %w", err)
	}
	var cfg DispatchConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing dispatch config: %w", err)
	}
	if err := validateDispatchConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// validateDispatchConfig checks that the dispatch config is well-formed.
func validateDispatchConfig(cfg *DispatchConfig) error {
	if len(cfg.Repos) == 0 {
		return fmt.Errorf("%w: dispatch config must have at least one repo", ErrMissingField)
	}
	if cfg.TriggerLabel == "" {
		return fmt.Errorf("%w: dispatch config must have a trigger_label", ErrMissingField)
	}
	if len(cfg.Mappings) == 0 {
		return fmt.Errorf("%w: dispatch config must have at least one mapping", ErrMissingField)
	}
	for _, m := range cfg.Mappings {
		if m.Label == "" {
			return fmt.Errorf("%w: mapping must have a label", ErrMissingField)
		}
		if m.Agent == "" {
			return fmt.Errorf("%w: mapping must have an agent", ErrMissingField)
		}
	}
	if cfg.IntervalSecs <= 0 {
		cfg.IntervalSecs = 300 // default 5 minutes
	}
	if cfg.RetryAfterSecs <= 0 {
		cfg.RetryAfterSecs = 1800 // default 30 minutes
	}
	if cfg.NotifyOnComplete == "" {
		cfg.NotifyOnComplete = "manager"
	}
	return nil
}
