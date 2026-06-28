package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

var (
	ErrNotFound       = errors.New("config file not found")
	ErrInvalidVersion = errors.New("unsupported config version")
	ErrInvalidType    = errors.New("invalid config type")
	ErrMissingField   = errors.New("missing required field")
	ErrAgentExists    = errors.New("agent already exists")
	ErrAgentNotFound  = errors.New("agent not found")
	ErrManualAgent    = errors.New("agent was not created by agent-gen")
	ErrInvalidMode    = errors.New("invalid build host mode")
)

// CurrentFactoryVersion is the highest supported factory.json schema version.
const CurrentFactoryVersion = 1

// Default git identity literals (issue #371 C-3). These are the ONE source of
// truth for the agentfactory commit identity: referenced by the factory.json
// default-fill, the installer (DefaultFactoryConfigJSON), the session env
// export, and the trailer hook (via env). A hand-typed second copy would risk
// Gap-6 drift, so do not inline these strings elsewhere — reference the
// constants (TestDefaultGitIdentityConstants pins the exact values).
const (
	DefaultGitUserName  = "agentfactory-cli"
	DefaultGitUserEmail = "293373236+agentfactory-cli@users.noreply.github.com"
)

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
	Model       string   `json:"model,omitempty"`
	BaseURL     string   `json:"base_url,omitempty"`
	AuthToken   string   `json:"auth_token,omitempty"`
}

var validAgentName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

// reservedNames are agent names that conflict with agentfactory internals.
// "dispatch" is reserved because session.SessionName("dispatch") produces the
// af-dispatch session name, which collides with the dispatcher's tmux session.
var reservedNames = map[string]bool{
	"dispatch": true,
}

// MessagingConfig holds the contents of messaging.json
type MessagingConfig struct {
	Groups map[string][]string `json:"groups"`
}

// FactoryConfig holds the contents of factory.json (root marker)
type FactoryConfig struct {
	Type         string       `json:"type"`
	Version      int          `json:"version"`
	Name         string       `json:"name"`
	MaxWorktrees int          `json:"max_worktrees,omitempty"`
	GitIdentity  *GitIdentity `json:"git_identity,omitempty"` // nil ⇒ validator fills C-3 defaults
}

// GitIdentity is the default git author/committer identity drawn from
// factory.json (issue #371 AC-3). A nil *GitIdentity distinguishes "absent on
// disk" (validator fills the C-3 defaults) from "operator set".
type GitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// DefaultGitIdentity returns the C-3 default identity built from the constants.
func DefaultGitIdentity() *GitIdentity {
	return &GitIdentity{Name: DefaultGitUserName, Email: DefaultGitUserEmail}
}

// ResolveIdentity implements the issue #371 IFF presence-gate (AC-2/C-4): it
// returns the default identity to apply ONLY when the ambient name OR email is
// absent; when both are present it returns apply=false ("leave the ambient
// identity"). It is a pure function (no env reads, no shell-out) per ADR-004 —
// the ambient read happens in the cmd layer and is plumbed in as parameters.
func ResolveIdentity(def *GitIdentity, ambientName, ambientEmail string) (name, email string, apply bool) {
	if def == nil {
		return "", "", false
	}
	if ambientName != "" && ambientEmail != "" {
		return "", "", false
	}
	return def.Name, def.Email, true
}

// DefaultFactoryConfigJSON returns the fresh-install factory.json content built
// from the in-code defaults (incl. the C-3 git identity), so there is no
// hand-typed literal to drift from the constants (issue #371 Gap-6).
func DefaultFactoryConfigJSON() string {
	cfg := FactoryConfig{
		Type:         "factory",
		Version:      CurrentFactoryVersion,
		Name:         "agentfactory",
		MaxWorktrees: 4,
		GitIdentity:  DefaultGitIdentity(),
	}
	b, err := json.Marshal(cfg)
	if err != nil { // unreachable for these scalar/pointer field types
		return `{"type":"factory","version":1,"name":"agentfactory"}`
	}
	return string(b)
}

// DefaultAgentsConfigJSON returns the fresh-install agents.json content built from the
// AgentConfig struct (single-source, compiler-checked — mirroring DefaultFactoryConfigJSON).
// Beyond manager+supervisor it SEEDS the four dispatch specialists referenced by the
// baked-in default dispatch.json (issue #73 K5), so a fresh `af install --init` factory is
// valid-by-construction on EVERY init path (cross-review C1): ValidateDispatchConfig then
// finds every mapping agent present. The role templates are already embedded
// (internal/templates/roles/*.tmpl), so a seeded registry entry is sufficient for af prime
// and `af sling --agent` to resolve each specialist — no agent-gen run or rebuild. Each
// specialist carries a non-empty description (validateAgentConfig requires it) and the
// matching formula (so the feature-workflow phases resolve to formula-bearing agents).
func DefaultAgentsConfigJSON() string {
	cfg := AgentConfig{Agents: map[string]AgentEntry{
		"manager":              {Type: "interactive", Description: "Interactive agent for human-supervised work", Directive: "Read your memory and docs, and prove it."},
		"supervisor":           {Type: "autonomous", Description: "Autonomous agent for independent task execution", Directive: "Read your memory and docs, and prove it."},
		"rapid-soldesign-plan": {Type: "autonomous", Description: "Autonomous design + implementation-planning specialist", Formula: "rapid-soldesign-plan"},
		"rapid-implement":      {Type: "autonomous", Description: "Autonomous test-first implementation specialist", Formula: "rapid-implement"},
		"ultra-review":         {Type: "autonomous", Description: "Autonomous multi-agent PR review specialist", Formula: "ultra-review"},
		"rapid-increment":      {Type: "autonomous", Description: "Autonomous PR-iteration specialist", Formula: "rapid-increment"},
	}}
	b, err := json.Marshal(cfg)
	if err != nil { // unreachable for these field types
		return `{"agents":{"manager":{"type":"interactive","description":"Interactive agent for human-supervised work","directive":"Read your memory and docs, and prove it."},"supervisor":{"type":"autonomous","description":"Autonomous agent for independent task execution","directive":"Read your memory and docs, and prove it."}}}`
	}
	return string(b)
}

// BuildHostConfig holds the contents of build-host.json
type BuildHostConfig struct {
	Mode      string `json:"mode"`
	Host      string `json:"host,omitempty"`
	User      string `json:"user,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
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

// LoadBuildHostConfig loads and validates build-host.json.
// Returns (nil, nil) when the file does not exist.
func LoadBuildHostConfig(path string) (*BuildHostConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading build host config: %w", err)
	}

	var config BuildHostConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing build host config: %w", err)
	}

	if err := validateBuildHostConfig(&config); err != nil {
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
		if agent.BaseURL != "" {
			u, err := url.Parse(agent.BaseURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("agent %q has invalid base_url %q: must start with http:// or https://", name, agent.BaseURL)
			}
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
	// Default-fill the C-3 git identity when absent on disk (issue #371 AC-2/AC-3),
	// mirroring the in-place default-fill in validateDispatchConfig/validateStartupConfig.
	if c.GitIdentity == nil {
		c.GitIdentity = DefaultGitIdentity()
	}
	return nil
}

func validateBuildHostConfig(c *BuildHostConfig) error {
	if c.Mode != "local" && c.Mode != "ssh" {
		return fmt.Errorf("%w: must be \"local\" or \"ssh\", got %q", ErrInvalidMode, c.Mode)
	}
	if c.Mode == "ssh" {
		if c.Host == "" {
			return fmt.Errorf("%w: ssh mode requires host", ErrMissingField)
		}
		if c.User == "" {
			return fmt.Errorf("%w: ssh mode requires user", ErrMissingField)
		}
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

// SaveBuildHostConfig writes build-host.json atomically.
func SaveBuildHostConfig(path string, cfg *BuildHostConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling build host config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0644)
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
