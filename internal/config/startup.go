package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// StartupConfig holds the contents of .agentfactory/startup.json. Unlike the
// other config loaders, an ABSENT file yields this struct fully defaulted (never
// a not-found error) — that is the C-4 backward-compat invariant.
type StartupConfig struct {
	Agents         []string `json:"agents"`
	Quality        string   `json:"quality"`
	Fidelity       string   `json:"fidelity"`
	StartDispatch  bool     `json:"start_dispatch"`
	WatchdogAgents []string `json:"watchdog_agents"`
}

func defaultStartupConfig() *StartupConfig {
	// Agents nil ⇒ "ALL" (the nil-vs-[] sentinel). WatchdogAgents nil/empty ⇒ the
	// watchdog refuses to start / af up skips it (never "ALL") — issue #408 inverted
	// that sentinel at the cmd layer. gates default ⇒ no-op.
	return &StartupConfig{Quality: "default", Fidelity: "default"}
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

// validateStartupConfig fills gate defaults in place and rejects bad gate enums.
func validateStartupConfig(cfg *StartupConfig) error {
	if cfg.Quality == "" {
		cfg.Quality = "default"
	}
	if cfg.Fidelity == "" {
		cfg.Fidelity = "default"
	}
	for _, g := range []struct{ name, val string }{{"quality", cfg.Quality}, {"fidelity", cfg.Fidelity}} {
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
