// Package config is the C5 curated settings surface for the web module.
//
// It serves two operator needs without ever importing af-core's internal/config (Go's internal
// seal + the separate go.mod make that compiler-impossible — the point of the decoupling, AC#4):
//
//   - READ (GET /api/settings): a curated view of dispatch.json + startup.json, a read-only view
//     of factory.json, and the agent roster as SECRET-FREE summaries. The per-agent secrets
//     Model/BaseURL/AuthToken (internal/config/config.go:46-55) are stripped BY CONSTRUCTION:
//     agents.json is decoded into a struct that has no secret fields, so they are never even held
//     in memory, let alone serialized to the browser (AC#3 — a mechanical interlock, not a manual
//     delete-before-send).
//
//   - WRITE (PUT /api/settings/{file}): the edited config is routed, as raw JSON on stdin, through
//     `af config <dispatch|startup> set` (the Setter seam). af-core is the SINGLE canonical
//     validator/writer — struct validation + cross-file ValidateDispatchConfig (every referenced
//     agent ∈ agents.json) + atomic temp+rename — so a mapping to a non-existent agent is rejected
//     WITHOUT corrupting the file (#225/#231). The web module does NOT re-declare the config schema
//     nor re-implement validation on the write side (H-P1, resolved to option (a)); the raw bytes
//     pass straight through. factory.json is read-only — there is no `af config factory set`.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/stempeck/agentfactory-web/internal/exec"
)

// dotDir mirrors internal/config/paths.go:10 — the factory's hidden config directory.
const dotDir = ".agentfactory"

func dispatchPath(root string) string { return filepath.Join(root, dotDir, "dispatch.json") }
func startupPath(root string) string  { return filepath.Join(root, dotDir, "startup.json") }
func factoryPath(root string) string  { return filepath.Join(root, dotDir, "factory.json") }
func agentsPath(root string) string   { return filepath.Join(root, dotDir, "agents.json") }

// ErrNotWritable is returned when a write targets a file outside the {dispatch,startup} allowlist
// (e.g. factory.json, which is read-only). The handler maps it to a client error.
var ErrNotWritable = errors.New("settings file is not writable")

// --- mirror structs (re-declared for the READ projection, NOT imported from internal/config) ---

// DispatchMapping mirrors internal/config/dispatch.go DispatchMapping. The label/labels duality is
// preserved so the editor round-trips faithfully (af normalizes a lone label → labels on save).
type DispatchMapping struct {
	Label  string   `json:"label,omitempty"`
	Labels []string `json:"labels,omitempty"`
	Source string   `json:"source,omitempty"`
	Agent  string   `json:"agent"`
}

// Dispatch mirrors internal/config/dispatch.go DispatchConfig (the curated, editable surface).
type Dispatch struct {
	Repos                      []string          `json:"repos"`
	TriggerLabel               string            `json:"trigger_label"`
	NotifyOnComplete           string            `json:"notify_on_complete,omitempty"`
	Mappings                   []DispatchMapping `json:"mappings"`
	IntervalSecs               int               `json:"interval_seconds,omitempty"`
	RetryAfterSecs             int               `json:"retry_after_seconds,omitempty"`
	RemoveTriggerAfterDispatch bool              `json:"remove_trigger_after_dispatch,omitempty"`
}

// Startup mirrors internal/config/startup.go StartupConfig.
type Startup struct {
	Agents         []string `json:"agents"`
	Quality        string   `json:"quality"`
	Fidelity       string   `json:"fidelity"`
	Improvement    string   `json:"improvement"`
	StartDispatch  bool     `json:"start_dispatch"`
	WatchdogAgents []string `json:"watchdog_agents"`
}

// GitIdentity mirrors internal/config/config.go GitIdentity (carries no secret).
type GitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Factory mirrors internal/config/config.go FactoryConfig — shown READ-ONLY; carries no secret.
type Factory struct {
	Type         string       `json:"type"`
	Version      int          `json:"version"`
	Name         string       `json:"name"`
	MaxWorktrees int          `json:"max_worktrees,omitempty"`
	GitIdentity  *GitIdentity `json:"git_identity,omitempty"`
}

// AgentSummary is the SECRET-FREE projection of one agents.json entry, used to populate the mapping
// editor's agent picker. It deliberately OMITS Model/BaseURL/AuthToken, so those secrets are
// structurally impossible to serialize (AC#3 mechanical interlock).
type AgentSummary struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Formula     string `json:"formula,omitempty"`
}

// Settings is the curated document served by GET /api/settings.
type Settings struct {
	Dispatch Dispatch       `json:"dispatch"`
	Startup  Startup        `json:"startup"`
	Factory  Factory        `json:"factory"` // read-only in the UI
	Agents   []AgentSummary `json:"agents"`  // agent picker for the mapping editor; never any secret
}

// agentEntryRead is the secret-free decode target for each agents.json entry. The secret JSON keys
// model/base_url/auth_token have NO matching field, so encoding/json silently drops them — the
// secrets never enter the web module's memory.
type agentEntryRead struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Formula     string `json:"formula,omitempty"`
}

// agentsFileRead mirrors agents.json's top-level shape: {"agents": {name: entry}}.
type agentsFileRead struct {
	Agents map[string]agentEntryRead `json:"agents"`
}

// Setter is the write seam: it pipes a complete config document to `af config <file> set` on stdin.
// exec.Wrapper satisfies it (via ConfigSet); tests inject a hermetic fake.
type Setter interface {
	ConfigSet(ctx context.Context, file string, payload []byte) (exec.Result, error)
}

// Service reads curated settings from disk and routes writes through the af command. root is the
// factory root (where .agentfactory/ lives); set is the exec wrapper used for the write path.
type Service struct {
	root string
	set  Setter
}

// New builds a Service over the factory root and the write seam (production: an *exec.Wrapper).
func New(root string, set Setter) *Service {
	return &Service{root: root, set: set}
}

// defaultStartup mirrors internal/config/startup.go defaultStartupConfig — the C-4 backward-compat
// invariant: an absent startup.json yields defaults, not an error.
func defaultStartup() Startup {
	return Startup{Quality: "default", Fidelity: "default", Improvement: "default"}
}

// Read assembles the curated settings document. dispatch.json / factory.json absence yields a zero
// (empty) section; startup.json absence yields defaults (NOT an error); agents.json is projected to
// secret-free summaries. A malformed (corrupt) file is a hard error.
func (s *Service) Read(ctx context.Context) (Settings, error) {
	out := Settings{Startup: defaultStartup()}

	if _, err := readJSONFile(dispatchPath(s.root), &out.Dispatch); err != nil {
		return Settings{}, fmt.Errorf("reading dispatch.json: %w", err)
	}

	var st Startup
	found, err := readJSONFile(startupPath(s.root), &st)
	if err != nil {
		return Settings{}, fmt.Errorf("reading startup.json: %w", err)
	}
	if found {
		out.Startup = st // present ⇒ use it; absent ⇒ keep defaults (C-4)
	}

	if _, err := readJSONFile(factoryPath(s.root), &out.Factory); err != nil {
		return Settings{}, fmt.Errorf("reading factory.json: %w", err)
	}

	var af agentsFileRead
	if _, err := readJSONFile(agentsPath(s.root), &af); err != nil {
		return Settings{}, fmt.Errorf("reading agents.json: %w", err)
	}
	out.Agents = summaries(af.Agents)

	return out, nil
}

// AgentFormula returns the DECLARED formula configured for an agent in agents.json
// (bead-free static config). It reads ONLY agents.json — it deliberately does NOT
// parse dispatch/startup/factory.json (cf. Read), so the Sling form's availability is
// not coupled to unrelated config health (design-doc H2 / Option B).
//
//	found=false  -> agent not present in agents.json (caller returns 404)
//	formula==""  -> agent present but no configured formula (caller returns 422)
//	err != nil   -> agents.json decode failure (caller returns 502)
//
// A MISSING agents.json makes readJSONFile return (false, nil) — so af.Agents is empty,
// name is absent ⇒ found=false ⇒ 404 at the caller. Only a genuine decode error yields err.
// ctx is accepted for interface symmetry / future use; like Read, the body does file I/O
// without threading it today. Keep the parameter.
func (s *Service) AgentFormula(ctx context.Context, name string) (formula string, found bool, err error) {
	var af agentsFileRead
	if _, err := readJSONFile(agentsPath(s.root), &af); err != nil {
		return "", false, fmt.Errorf("reading agents.json: %w", err)
	}
	entry, ok := af.Agents[name]
	if !ok {
		return "", false, nil
	}
	return entry.Formula, true, nil
}

// Write routes the complete edited config to `af config <file> set` (raw JSON on stdin). file is
// restricted to the {dispatch,startup} allowlist; factory.json (or any other) yields ErrNotWritable
// before any exec. The af command performs all validation + the atomic temp+rename write; on a
// non-zero exit its friendly per-field message is surfaced in the returned error.
func (s *Service) Write(ctx context.Context, file string, payload []byte) (exec.Result, error) {
	if file != "dispatch" && file != "startup" {
		return exec.Result{}, fmt.Errorf("%w: %q (factory.json is read-only)", ErrNotWritable, file)
	}
	return s.set.ConfigSet(ctx, file, payload)
}

// summaries projects the agents map into a deterministic (name-sorted) slice of secret-free
// summaries.
func summaries(m map[string]agentEntryRead) []AgentSummary {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]AgentSummary, 0, len(names))
	for _, n := range names {
		e := m[n]
		out = append(out, AgentSummary{Name: n, Type: e.Type, Description: e.Description, Formula: e.Formula})
	}
	return out
}

// readJSONFile reads and unmarshals a JSON file into v. It returns (false, nil) when the file does
// not exist (so callers can apply defaults), and an error only on a read failure or malformed JSON.
func readJSONFile(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, fmt.Errorf("decoding %s: %w", filepath.Base(path), err)
	}
	return true, nil
}
