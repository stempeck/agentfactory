package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// StartupConfig holds the contents of .agentfactory/startup.json. Unlike the
// other config loaders, an ABSENT file yields this struct fully defaulted (never
// a not-found error) — that is the C-4 backward-compat invariant.
type StartupConfig struct {
	Agents         []string `json:"agents"`
	Quality        string   `json:"quality"`
	Fidelity       string   `json:"fidelity"`
	Improvement    string   `json:"improvement"`
	StartDispatch  bool     `json:"start_dispatch"`
	WatchdogAgents []string `json:"watchdog_agents"`
}

func defaultStartupConfig() *StartupConfig {
	// Agents nil ⇒ "ALL" (the nil-vs-[] sentinel). WatchdogAgents nil/empty ⇒ the
	// watchdog refuses to start / af up skips it (never "ALL") — issue #408 inverted
	// that sentinel at the cmd layer. gates default ⇒ no-op. The absent-file load
	// path returns this struct WITHOUT running validateStartupConfig, so Improvement
	// must be seeded here too (backward-compat).
	return &StartupConfig{Quality: "default", Fidelity: "default", Improvement: "default"}
}

// LoadStartupConfig loads and validates .agentfactory/startup.json. An absent
// file returns defaults + nil error (NOT a not-found error) — the deliberate
// C-4 divergence from LoadDispatchConfig.
func LoadStartupConfig(root string) (*StartupConfig, error) {
	path := StartupConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultStartupConfig(), nil
		}
		return nil, fmt.Errorf("reading startup config: %w", err)
	}
	var cfg StartupConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing startup config: %w", err)
	}
	if err := validateStartupConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveStartupConfig validates then atomically writes the startup config to path
// via fsutil.WriteFileAtomic. It does not assume the file pre-exists — the
// absent-file-⇒-defaults invariant (C-4) lives in LoadStartupConfig, and Save is
// free to create the file. Mirrors SaveBuildHostConfig (config.go).
func SaveStartupConfig(path string, cfg *StartupConfig) error {
	if err := validateStartupConfig(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling startup config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0644)
}

// validateStartupConfig fills gate defaults in place and rejects bad gate enums.
func validateStartupConfig(cfg *StartupConfig) error {
	if cfg.Quality == "" {
		cfg.Quality = "default"
	}
	if cfg.Fidelity == "" {
		cfg.Fidelity = "default"
	}
	if cfg.Improvement == "" {
		cfg.Improvement = "default"
	}
	for _, g := range []struct{ name, val string }{{"quality", cfg.Quality}, {"fidelity", cfg.Fidelity}, {"improvement", cfg.Improvement}} {
		switch g.val {
		case "on", "off", "default":
		default:
			return fmt.Errorf("%w: startup %s must be \"on\", \"off\", or \"default\", got %q", ErrInvalidType, g.name, g.val)
		}
	}
	// Agents / WatchdogAgents: nil-vs-[] preserved by json.Unmarshal; NO membership
	// check here — internal/config stays pure/decoupled (ADR-004). The agents.json
	// cross-check is a cmd-layer concern done later in Phase 3.
	return nil
}
