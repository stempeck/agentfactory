package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// ModelsConfig holds the contents of .agentfactory/models.json — the registry of
// per-agent model "export sets" for issue #480. The schema is intentionally
// generic: a profile is a plain map of env exports (name → {ENV_KEY: value}), so
// a new model or export key is a config edit with no code change. Empty values
// are preserved (notably ANTHROPIC_API_KEY:"" is an explicit clear). Like
// startup.json an absent file yields an empty config, never a not-found error.
type ModelsConfig struct {
	Default string                       `json:"default,omitempty"`
	Models  map[string]map[string]string `json:"models"`
	Agents  map[string]string            `json:"agents,omitempty"`
}

// EnvVar is one ordered, empty-value-preserving export. ResolveModelEnv returns a
// slice of these so the launch chokepoint emits them deterministically.
type EnvVar struct{ Key, Value string }

const (
	envModel     = "ANTHROPIC_MODEL"
	envAPIKey    = "ANTHROPIC_API_KEY"
	envBaseURL   = "ANTHROPIC_BASE_URL"
	envAuthToken = "ANTHROPIC_AUTH_TOKEN"
)

// afIdentityKeys are the identity vars session.Manager owns (ADR-003/ADR-004). A
// profile that named one would spoof agent identity, so they are denylisted from
// every profile's export keys.
var afIdentityKeys = map[string]bool{
	"AF_ROLE":        true,
	"AF_ACTOR":       true,
	"AF_ROOT":        true,
	"AF_WORKTREE":    true,
	"AF_WORKTREE_ID": true,
}

// LoadModelsConfig loads and validates .agentfactory/models.json. An absent file
// returns an empty config + nil error (NOT a not-found error), mirroring
// LoadStartupConfig.
func LoadModelsConfig(root string) (*ModelsConfig, error) {
	path := ModelsConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ModelsConfig{}, nil
		}
		return nil, fmt.Errorf("reading models config: %w", err)
	}
	var cfg ModelsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing models config: %w", err)
	}
	if err := validateModelsConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveModelsConfig validates then atomically writes the models config to path
// (an absolute path, not a root) via fsutil.WriteFileAtomic. Mirrors
// SaveStartupConfig.
func SaveModelsConfig(path string, cfg *ModelsConfig) error {
	if err := validateModelsConfig(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling models config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0644)
}

// validateModelsConfig is pure (no env reads) and fail-loud. It rejects, across
// EVERY profile: identity-var keys, a non-empty ANTHROPIC_API_KEY, a malformed
// ANTHROPIC_BASE_URL, and an incomplete endpoint (base_url without auth_token).
// It also rejects an agents/default entry naming a model absent from Models.
func validateModelsConfig(cfg *ModelsConfig) error {
	for name, profile := range cfg.Models {
		if err := validateModelProfile(name, profile); err != nil {
			return err
		}
	}
	for agent, model := range cfg.Agents {
		if _, ok := cfg.Models[model]; !ok {
			return fmt.Errorf("%w: agent %q references undefined model %q", ErrMissingField, agent, model)
		}
	}
	if cfg.Default != "" {
		if _, ok := cfg.Models[cfg.Default]; !ok {
			return fmt.Errorf("%w: default references undefined model %q", ErrMissingField, cfg.Default)
		}
	}
	return nil
}

func validateModelProfile(name string, profile map[string]string) error {
	for key, val := range profile {
		if afIdentityKeys[key] {
			return fmt.Errorf("%w: model %q sets identity var %q reserved for the session manager", ErrInvalidType, name, key)
		}
		if key == envAPIKey && val != "" {
			return fmt.Errorf("%w: model %q sets a non-empty %s; a real key must not appear in a launch line (use \"\" to clear)", ErrInvalidType, name, envAPIKey)
		}
		if key == envBaseURL && val != "" {
			u, err := url.Parse(val)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("model %q has invalid base_url %q: must start with http:// or https://", name, val)
			}
		}
	}

	// The ANTHROPIC_AUTH_TOKEN and ANTHROPIC_BASE_URL keys are coupled, so read them
	// directly (map iteration above is random-order). A file: value is a secret
	// reference (shape-checked here, dereferenced at launch in Phase 2); anything
	// else that looks like a real credential is rejected on a non-loopback endpoint.
	// The file: convention plus the Phase-2 dereference is the actual secret-exposure
	// guarantee — the sk- guard below is a defense-in-depth backstop, not the barrier.
	if tok := profile[envAuthToken]; isSecretRef(tok) {
		if err := validateSecretRefShape(name, tok); err != nil {
			return err
		}
	} else if looksLikeCredential(tok) && !IsLoopbackEndpoint(profile[envBaseURL]) {
		return fmt.Errorf("%w: model %q sets %s to what looks like a literal credential for a non-loopback endpoint; store it in .agentfactory/secrets/ and use a \"file:<path>\" reference", ErrInvalidType, name, envAuthToken)
	}

	return checkEndpointComplete(name, profile)
}

// checkEndpointComplete rejects an incomplete endpoint: a profile that sets ANTHROPIC_BASE_URL must
// also set a non-empty ANTHROPIC_AUTH_TOKEN, else the launched agent cannot
// authenticate. Shared by the load-time validator and the resolver.
func checkEndpointComplete(name string, profile map[string]string) error {
	if profile[envBaseURL] != "" && profile[envAuthToken] == "" {
		return fmt.Errorf("%w: model %q sets %s without %s (incomplete endpoint)", ErrMissingField, name, envBaseURL, envAuthToken)
	}
	return nil
}

// ResolveModelEnv is the pure deterministic resolver. It picks a selection by
// precedence (cliModel > marker > cfg.Agents[agent] > legacyEntryModel >
// cfg.Default), then:
//   - empty selection           ⇒ ok=false (inherit today's global default)
//   - selection names a profile ⇒ ordered []EnvVar (ANTHROPIC_MODEL first, then
//     remaining keys sorted; empty values kept), or an err if that profile's
//     endpoint is incomplete
//   - selection is a raw id     ⇒ emit ANTHROPIC_MODEL only (passthrough)
//
// The marker value is supplied by the cmd layer; the resolver never reads a file
// or the environment (ADR-004). The returned name is the lookup key (profile name
// or raw id), kept distinct from the model id carried in ANTHROPIC_MODEL.
func ResolveModelEnv(cfg *ModelsConfig, agent, cliModel, marker, legacyEntryModel string) (string, []EnvVar, bool, error) {
	selection := firstNonEmpty(cliModel, marker, agentModel(cfg, agent), legacyEntryModel, defaultModel(cfg))
	if selection == "" {
		return "", nil, false, nil
	}

	profile := lookupProfile(cfg, selection)
	if profile == nil {
		return selection, []EnvVar{{Key: envModel, Value: selection}}, true, nil
	}

	if err := checkEndpointComplete(selection, profile); err != nil {
		return selection, nil, false, err
	}
	return selection, orderedEnv(profile), true, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func agentModel(cfg *ModelsConfig, agent string) string {
	if cfg == nil || agent == "" {
		return ""
	}
	return cfg.Agents[agent]
}

func defaultModel(cfg *ModelsConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.Default
}

func lookupProfile(cfg *ModelsConfig, name string) map[string]string {
	if cfg == nil || cfg.Models == nil {
		return nil
	}
	return cfg.Models[name]
}

// orderedEnv flattens a profile into a deterministic slice: ANTHROPIC_MODEL first
// (if present), then the remaining keys in sorted order. Empty values are kept.
func orderedEnv(profile map[string]string) []EnvVar {
	env := make([]EnvVar, 0, len(profile))
	if v, ok := profile[envModel]; ok {
		env = append(env, EnvVar{Key: envModel, Value: v})
	}
	keys := make([]string, 0, len(profile))
	for k := range profile {
		if k == envModel {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, EnvVar{Key: k, Value: profile[k]})
	}
	return env
}
