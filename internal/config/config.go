package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var (
	ErrNotFound       = errors.New("config file not found")
	ErrInvalidVersion = errors.New("unsupported config version")
	ErrInvalidType    = errors.New("invalid config type")
	ErrMissingField   = errors.New("missing required field")
	ErrAgentExists    = errors.New("agent already exists")
	ErrAgentNotFound  = errors.New("agent not found")
	ErrManualAgent    = errors.New("agent was not created by agent-gen")
)

// CurrentFactoryVersion is the highest supported factory.json schema version.
const CurrentFactoryVersion = 1

// AgentConfig holds the contents of agents.json
type AgentConfig struct {
	Agents map[string]AgentEntry `json:"agents"`
}

// AgentEntry defines a single agent
type AgentEntry struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Directive   string   `json:"directive,omitempty"`
	Formula     string   `json:"formula,omitempty"`
	SparsePaths []string `json:"sparse_paths,omitempty"`
}

var validAgentName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// reservedNames are agent names that conflict with agentfactory internals.
// "dispatch" is reserved because session.SessionName("dispatch") produces
// "af-dispatch", which collides with the dispatcher's tmux session name.
var reservedNames = map[string]bool{
	"dispatch": true,
}

// MessagingConfig holds the contents of messaging.json
type MessagingConfig struct {
	Groups map[string][]string `json:"groups"`
}

// FactoryConfig holds the contents of factory.json (root marker)
type FactoryConfig struct {
	Type         string `json:"type"`
	Version      int    `json:"version"`
	Name         string `json:"name"`
	MaxWorktrees int    `json:"max_worktrees,omitempty"`
}

// LoadAgentConfig loads and validates agents.json
func LoadAgentConfig(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading agent config: %w", err)
	}

	var config AgentConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing agent config: %w", err)
	}

	if err := validateAgentConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// LoadMessagingConfig loads and validates messaging.json, cross-referencing agents
func LoadMessagingConfig(path string, agents *AgentConfig) (*MessagingConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading messaging config: %w", err)
	}

	var config MessagingConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing messaging config: %w", err)
	}

	if err := validateMessagingConfig(&config, agents); err != nil {
		return nil, err
	}

	return &config, nil
}

// LoadFactoryConfig loads and validates factory.json
func LoadFactoryConfig(path string) (*FactoryConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("reading factory config: %w", err)
	}

	var config FactoryConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing factory config: %w", err)
	}

	if err := validateFactoryConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

func validateAgentConfig(c *AgentConfig) error {
	if c.Agents == nil {
		c.Agents = make(map[string]AgentEntry)
	}
	for name, agent := range c.Agents {
		if err := ValidateAgentName(name); err != nil {
			return fmt.Errorf("invalid agent name in config: %w", err)
		}
		if agent.Type != "interactive" && agent.Type != "autonomous" {
			return fmt.Errorf("%w: agent %q has type %q, must be \"interactive\" or \"autonomous\"", ErrInvalidType, name, agent.Type)
		}
		if agent.Description == "" {
			return fmt.Errorf("%w: agent %q has empty description", ErrMissingField, name)
		}
	}
	return nil
}

func validateMessagingConfig(c *MessagingConfig, agents *AgentConfig) error {
	if c.Groups == nil {
		c.Groups = make(map[string][]string)
	}
	for groupName, members := range c.Groups {
		for _, member := range members {
			if _, exists := agents.Agents[member]; !exists {
				return fmt.Errorf("%w: group %q references unknown agent %q", ErrMissingField, groupName, member)
			}
		}
	}
	return nil
}

func validateFactoryConfig(c *FactoryConfig) error {
	if c.Type != "factory" {
		return fmt.Errorf("%w: expected type \"factory\", got %q", ErrInvalidType, c.Type)
	}
	if c.Version < 1 {
		return fmt.Errorf("%w: version must be >= 1, got %d", ErrInvalidVersion, c.Version)
	}
	if c.Version > CurrentFactoryVersion {
		return fmt.Errorf("%w: version %d is newer than supported version %d", ErrInvalidVersion, c.Version, CurrentFactoryVersion)
	}
	return nil
}

// ValidateAgentName validates an agent name for filesystem and JSON safety.
func ValidateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("agent name too long (max 64 characters)")
	}
	if reservedNames[name] {
		return fmt.Errorf("agent name %q is reserved", name)
	}
	if !validAgentName.MatchString(name) {
		return fmt.Errorf("invalid agent name %q: must match [a-zA-Z][a-zA-Z0-9_-]*", name)
	}
	return nil
}

// SaveAgentConfig writes agents.json atomically via temp file + rename.
func SaveAgentConfig(path string, cfg *AgentConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent config: %w", err)
	}
	data = append(data, '\n')
	tmp := filepath.Join(filepath.Dir(path), ".agents.json.tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing temp agent config: %w", err)
	}
	return os.Rename(tmp, path)
}

// RemoveAgentEntry removes a formula-generated agent from the config.
// Returns ErrAgentNotFound if the agent doesn't exist, or ErrManualAgent
// if the agent was not created by agent-gen (Formula field is empty).
func RemoveAgentEntry(cfg *AgentConfig, name string) error {
	existing, exists := cfg.Agents[name]
	if !exists {
		return fmt.Errorf("%w: %q", ErrAgentNotFound, name)
	}
	if existing.Formula == "" {
		return fmt.Errorf("%w: %q (no formula field) — not created by agent-gen", ErrManualAgent, name)
	}
	delete(cfg.Agents, name)
	return nil
}

// AddAgentEntry adds or updates an agent entry with formula-field protection.
func AddAgentEntry(cfg *AgentConfig, name string, entry AgentEntry) error {
	existing, exists := cfg.Agents[name]
	if exists && existing.Formula == "" {
		return fmt.Errorf("%w: agent %q was not created by agent-gen (no formula field)", ErrAgentExists, name)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentEntry)
	}
	cfg.Agents[name] = entry
	return nil
}
