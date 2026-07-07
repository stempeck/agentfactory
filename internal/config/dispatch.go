package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// defaultNotifyAgent is the fallback NotifyOnComplete agent when the dispatch
// config leaves it empty. Shared by validateDispatchConfig (which fills it) and
// ValidateDispatchConfig (which validates the effective value) so the two cannot
// silently diverge.
const defaultNotifyAgent = "manager"

// DispatchConfig holds the contents of .agentfactory/dispatch.json
type DispatchConfig struct {
	Repos                      []string          `json:"repos"`
	TriggerLabel               string            `json:"trigger_label"`
	NotifyOnComplete           string            `json:"notify_on_complete"`
	Mappings                   []DispatchMapping `json:"mappings"`
	IntervalSecs               int               `json:"interval_seconds"`
	RetryAfterSecs             int               `json:"retry_after_seconds"`
	RemoveTriggerAfterDispatch bool              `json:"remove_trigger_after_dispatch"`
	Workflows                  []Workflow        `json:"workflows,omitempty"`
}

// DispatchMapping maps GitHub labels to an agent name.
type DispatchMapping struct {
	Label  string   `json:"label,omitempty"`
	Labels []string `json:"labels,omitempty"`
	Source string   `json:"source,omitempty"`
	Agent  string   `json:"agent"`

	// Model (issue #480) optionally pins the per-mapping model profile threaded to
	// `af sling --model` at dispatch time; empty leaves the agent on its durable
	// default. ValidateDispatchConfig cross-checks it against models.json.
	Model string `json:"model,omitempty"`
}

// Workflow defines an ordered multi-phase pipeline keyed by a single
// operator-applied label (issue #378). Phases reference existing mapping labels —
// mappings[] owns the agent binding, so the pipeline is the single source of
// truth in dispatch.json (no formula edits, decision W-1). A bare {label, phases}
// is the complete v1 shape; the operator verify map (decision W-9) is deferred.
type Workflow struct {
	Label  string   `json:"label"`  // operator-applied GitHub label that triggers the workflow
	Phases []string `json:"phases"` // ordered existing mapping labels
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

// SaveDispatchConfig validates (struct-level) then atomically writes the dispatch
// config to path via temp-file + rename (fsutil.WriteFileAtomic). It does NOT do
// the cross-file agents.json check — callers that have the agents config should
// run ValidateDispatchConfig first (the external/CLI write path does). Mirrors
// SaveBuildHostConfig (config.go).
func SaveDispatchConfig(path string, cfg *DispatchConfig) error {
	if err := validateDispatchConfig(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling dispatch config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0644)
}

// ValidateDispatchConfig cross-checks that every agent the dispatch config
// references — each Mapping.Agent and an EXPLICITLY set NotifyOnComplete — exists in
// the agents config (agents.json). It lifts the previously cmd-layer-only inline check
// (dispatch.go) into a reusable config-package validator so the CLI and any external
// write path share one source of truth (L-1). An EMPTY NotifyOnComplete is left
// unvalidated here: it defaults to "manager" at runtime (validateDispatchConfig), but a
// factory that has no "manager" agent and leaves notify_on_complete unset still has an
// otherwise-valid dispatch.json and must not be blocked from saving it. Only an
// explicitly named, non-existent notify agent is an error.
//
// models (issue #480) is nil-tolerant: when non-nil AND at least one profile is
// defined, every mapping's Model must name a profile defined in models.json —
// mirroring the unknown-agent check — so a dispatch.json that routes to a typo'd
// profile is rejected at the write/start boundary rather than failing loud only when
// the item is finally slung. With NO registry (nil models, absent models.json, or
// zero profiles) a mapping's model is a raw id passed straight to `claude --model`,
// exactly as the launch path treats it, so the cross-check is skipped.
func ValidateDispatchConfig(disp *DispatchConfig, agents *AgentConfig, models *ModelsConfig) error {
	if disp == nil {
		return fmt.Errorf("dispatch config is nil")
	}
	if agents == nil {
		return fmt.Errorf("agents config is nil")
	}
	for _, m := range disp.Mappings {
		if _, ok := agents.Agents[m.Agent]; !ok {
			return fmt.Errorf("dispatch mapping references unknown agent %q", m.Agent)
		}
		if models != nil && len(models.Models) > 0 && m.Model != "" {
			if _, ok := models.Models[m.Model]; !ok {
				return fmt.Errorf("dispatch mapping references undefined model %q", m.Model)
			}
		}
	}
	if disp.NotifyOnComplete != "" {
		if _, ok := agents.Agents[disp.NotifyOnComplete]; !ok {
			return fmt.Errorf("notify_on_complete agent %q not found in agents.json", disp.NotifyOnComplete)
		}
	}
	// Workflow phase agents must be formula-bearing: an agent with no formula can
	// never signal phase completion, so the pipeline would stall (issue #378 Phase 1).
	// Phase->agent resolution mirrors the struct-level single-label rule. Unresolvable
	// phases and unknown agents are the responsibility of validateDispatchConfig and
	// the mapping loop above respectively, so they are skipped (not double-reported).
	for _, wf := range disp.Workflows {
		for _, phase := range wf.Phases {
			m := phaseResolvesAlone(disp.Mappings, phase)
			if m == nil {
				continue
			}
			entry, ok := agents.Agents[m.Agent]
			if !ok {
				continue
			}
			if entry.Formula == "" {
				return fmt.Errorf("workflow %q phase %q maps to agent %q which has no formula (cannot signal completion)", wf.Label, phase, m.Agent)
			}
		}
	}
	// LOW-4 (warn on mid-formula gates) is intentionally NOT implemented here.
	// Detecting gates requires parsing the phase agent's formula via internal/formula
	// (FindFormulaFile + ParseFile), but internal/formula imports internal/config, so
	// importing it here would create an import cycle. LOW-4 is advisory ("warn, never
	// block"), so it is deferred to the cmd layer (the dispatch-start and config-write
	// callers of ValidateDispatchConfig already import both packages). Omitting it
	// blocks no valid config.
	return nil
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
	for i, m := range cfg.Mappings {
		if m.Label != "" && len(m.Labels) > 0 {
			return fmt.Errorf("%w: mapping has both label and labels (ambiguous)", ErrMissingField)
		}
		if m.Label != "" && len(m.Labels) == 0 {
			cfg.Mappings[i].Labels = []string{m.Label}
			cfg.Mappings[i].Label = ""
		}
		if len(cfg.Mappings[i].Labels) == 0 {
			return fmt.Errorf("%w: mapping must have at least one label", ErrMissingField)
		}
		if m.Source != "" && m.Source != "issue" && m.Source != "pr" {
			return fmt.Errorf("%w: mapping source must be \"issue\" or \"pr\", got %q", ErrInvalidType, m.Source)
		}
		if cfg.Mappings[i].Source == "" {
			cfg.Mappings[i].Source = "issue"
		}
		if m.Agent == "" {
			return fmt.Errorf("%w: mapping must have an agent", ErrMissingField)
		}
	}
	if err := validateWorkflows(cfg); err != nil {
		return err
	}
	if cfg.IntervalSecs <= 0 {
		cfg.IntervalSecs = 300 // default 5 minutes
	}
	if cfg.RetryAfterSecs <= 0 {
		cfg.RetryAfterSecs = 1800 // default 30 minutes
	}
	if cfg.NotifyOnComplete == "" {
		cfg.NotifyOnComplete = defaultNotifyAgent
	}
	return nil
}

// validateWorkflows checks the struct-level (no-agents.json-needed) rules for the
// workflow pipeline (issue #378 Phase 1). It MUST run after the mapping loop in
// validateDispatchConfig so the Label->Labels migration and the Source default have
// completed: phase resolution reads Labels, and the mixed-source check reads the
// defaulted Source.
func validateWorkflows(cfg *DispatchConfig) error {
	seen := make(map[string]bool)
	for _, wf := range cfg.Workflows {
		if wf.Label == "" {
			return fmt.Errorf("%w: workflow must have a label", ErrMissingField)
		}
		if seen[wf.Label] {
			return fmt.Errorf("workflow %q has duplicate label", wf.Label)
		}
		seen[wf.Label] = true
		if wf.Label == cfg.TriggerLabel {
			return fmt.Errorf("workflow %q label %q collides with trigger_label", wf.Label, wf.Label)
		}
		if len(wf.Phases) == 0 {
			return fmt.Errorf("workflow %q must have at least one phase", wf.Label)
		}
		// LOW-2: a label may not appear in its own phases. Checked before HIGH-B so a
		// self-referential label reports this more specific message.
		for _, phase := range wf.Phases {
			if phase == wf.Label {
				return fmt.Errorf("workflow %q label %q also appears in its phases", wf.Label, wf.Label)
			}
		}
		// HIGH-B: the workflow label must not also be a mapping label, or a mapping
		// keyed on the bare workflow label could shadow a phase mapping via
		// matchItemToAgent's first-match-wins (the advanced item carries the workflow
		// label alongside the phase label).
		if phaseInAnyMapping(cfg.Mappings, wf.Label) {
			return fmt.Errorf("workflow %q label %q must not also be a mapping label (would shadow a phase via first-match-wins)", wf.Label, wf.Label)
		}
		workflowSource := ""
		for pi, phase := range wf.Phases {
			// LOW-2: no phase may equal trigger_label.
			if phase == cfg.TriggerLabel {
				return fmt.Errorf("workflow %q phase %q collides with trigger_label", wf.Label, phase)
			}
			// CRITICAL-2: every phase must back a mapping resolvable on the phase
			// label ALONE. matchItemToAgent ANDs a mapping's labels, so a phase that
			// only appears inside a multi-label mapping resolves to no agent at advance
			// time and silently stalls. REJECT (do not warn, do not defer).
			m := phaseResolvesAlone(cfg.Mappings, phase)
			if m == nil {
				if !phaseInAnyMapping(cfg.Mappings, phase) {
					return fmt.Errorf("workflow %q references phase %q that is not in any mapping", wf.Label, phase)
				}
				return fmt.Errorf("workflow %q phase %q does not resolve to an agent on the phase label alone (its mapping also requires %v)", wf.Label, phase, otherRequiredLabels(cfg.Mappings, phase))
			}
			// HIGH-2: in v1 (no cross-source resolution yet) every phase of a workflow
			// must resolve to the same source.
			if pi == 0 {
				workflowSource = m.Source
			} else if m.Source != workflowSource {
				return fmt.Errorf("workflow %q has phases with mixed source (%q vs %q); cross-source workflows are not supported", wf.Label, workflowSource, m.Source)
			}
		}
	}
	return nil
}

// phaseResolvesAlone returns the mapping that backs phaseLabel on the phase label
// ALONE — a single-label mapping equal to {phaseLabel} — or nil if none exists.
// This is the predicate behind CRITICAL-2 and the cross-file formula-bearing check.
// It tolerates the raw singular Label form for callers (ValidateDispatchConfig) that
// run before validateDispatchConfig's Label->Labels migration.
func phaseResolvesAlone(mappings []DispatchMapping, phaseLabel string) *DispatchMapping {
	for i := range mappings {
		m := &mappings[i]
		if len(m.Labels) == 1 && m.Labels[0] == phaseLabel {
			return m
		}
		if len(m.Labels) == 0 && m.Label == phaseLabel {
			return m
		}
	}
	return nil
}

// phaseInAnyMapping reports whether label appears in any mapping's label set. Used
// to distinguish "not in any mapping" from "in a mapping but not resolvable on the
// label alone", and to enforce HIGH-B (workflow label is not a mapping label).
func phaseInAnyMapping(mappings []DispatchMapping, label string) bool {
	for _, m := range mappings {
		if m.Label == label {
			return true
		}
		for _, l := range m.Labels {
			if l == label {
				return true
			}
		}
	}
	return false
}

// otherRequiredLabels returns the labels a multi-label mapping requires ALONGSIDE
// phaseLabel, for the CRITICAL-2 rejection message. Returns nil if no multi-label
// mapping contains phaseLabel.
func otherRequiredLabels(mappings []DispatchMapping, phaseLabel string) []string {
	for _, m := range mappings {
		if len(m.Labels) <= 1 {
			continue
		}
		has := false
		for _, l := range m.Labels {
			if l == phaseLabel {
				has = true
				break
			}
		}
		if !has {
			continue
		}
		var others []string
		for _, l := range m.Labels {
			if l != phaseLabel {
				others = append(others, l)
			}
		}
		return others
	}
	return nil
}
