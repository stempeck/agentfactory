// Package statusline is the pure render engine (K3) behind `af statusline render`
// (issue #591). It is deliberately env-free and cmd-free (ADR-004): the factory root is a
// parameter, the endpoint-redirect decision is a parameter, and every function degrades to
// safe output rather than erroring — a statusline must never be noise or a stall in an
// agent's pane (design constraint C-5).
package statusline

import (
	"encoding/json"
	"io"
)

// Payload is the optional-tolerant subset of Claude Code's 2.1.212 statusline stdin payload
// that this library reads (schema: .designs/591/codebase-snapshot.md:404-448). Unknown fields
// are ignored by encoding/json, which IS the schema-drift tolerance the design relies on:
// every element degrades independently, so an added or renamed upstream field can never blank
// the render. UsedPercentage is a pointer so "0%" (present) is distinguishable from "absent";
// everything else uses the zero value as its own "absent" signal.
type Payload struct {
	SessionID string `json:"session_id"`
	// TranscriptPath locates the session's append-only JSONL transcript, whose per-message usage
	// records are the only API-reported cumulative token source available here — ContextWindow
	// below carries occupancy, which shrinks on compaction and is not spend (PR #595 F1). Empty
	// is the normal absent signal: token accumulation is skipped and the render degrades to cost.
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Model          struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	ContextWindow struct {
		TotalInputTokens  int64    `json:"total_input_tokens"`
		TotalOutputTokens int64    `json:"total_output_tokens"`
		ContextWindowSize int64    `json:"context_window_size"`
		UsedPercentage    *float64 `json:"used_percentage"`
	} `json:"context_window"`
	Cost struct {
		TotalCostUSD      float64 `json:"total_cost_usd"`
		TotalDurationMS   int64   `json:"total_duration_ms"`
		TotalLinesAdded   int64   `json:"total_lines_added"`
		TotalLinesRemoved int64   `json:"total_lines_removed"`
	} `json:"cost"`
}

// ParsePayload decodes one statusline payload. A malformed body returns an error so the cmd
// layer can degrade to a blank render (exit 0); absent or null fields are NOT errors — they
// surface as zero values / a nil UsedPercentage and each drops its own element in Render.
func ParsePayload(r io.Reader) (Payload, error) {
	var p Payload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return Payload{}, err
	}
	return p, nil
}
