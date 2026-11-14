package statusline

import (
	"encoding/json"
	"io"
)

type PeakOccupancy struct {
	MessageID      string
	Tokens         int64
	HarnessVersion string
	Cut            bool
}

type startupRecord struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	IsSidechain bool   `json:"isSidechain"`
	IsAPIError  bool   `json:"isApiErrorMessage"`
	Version     string `json:"version"`
	Message     *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// TranscriptPeakOccupancy reads to EOF or to the first main-session Stop record that follows a
// usage-bearing message, and sets Cut when it stopped there. A Stop with no such message before it
// does not cut: a first turn whose only usage-bearing record was an API error has not produced a
// startup figure yet. It does not reuse ScanUsage, which decodes only input and output and holds
// back the trailing run.
func TranscriptPeakOccupancy(r io.Reader) (PeakOccupancy, bool) {
	br := NewTranscriptReader(r)
	perID := map[string]MessageUsage{}
	var order []string
	var version string
	usageSeen := false
	cut := false
	for !cut {
		line, _, ok := ReadTranscriptLine(br)
		if !ok {
			break
		}
		if line == nil {
			continue
		}
		var rec startupRecord
		if json.Unmarshal(line, &rec) != nil || rec.IsSidechain || isAPIErrorRecord(rec) {
			continue
		}
		if rec.Version != "" {
			version = rec.Version
		}
		if rec.Type == "system" && (rec.Subtype == "stop_hook_summary" || rec.Subtype == "turn_duration") {
			cut = usageSeen
			continue
		}
		m := rec.Message
		if m == nil || m.Usage == nil || m.ID == "" {
			continue
		}
		u, known := perID[m.ID]
		if !known {
			order = append(order, m.ID)
		}
		u.Absorb(MessageUsage{
			InputTokens:         m.Usage.InputTokens,
			OutputTokens:        m.Usage.OutputTokens,
			CacheReadTokens:     m.Usage.CacheReadInputTokens,
			CacheCreationTokens: m.Usage.CacheCreationInputTokens,
		})
		perID[m.ID] = u
		if u.Occupancy() > 0 {
			usageSeen = true
		}
	}
	var peak PeakOccupancy
	for _, id := range order {
		if occ := perID[id].Occupancy(); occ > peak.Tokens {
			peak.MessageID, peak.Tokens = id, occ
		}
	}
	if peak.Tokens == 0 {
		return PeakOccupancy{}, false
	}
	peak.HarnessVersion = version
	peak.Cut = cut
	return peak, true
}

// isAPIErrorRecord is how Claude Code writes a failed request: a message id and an all-zero usage,
// just before turn_duration.
func isAPIErrorRecord(rec startupRecord) bool {
	return rec.IsAPIError || (rec.Message != nil && rec.Message.Model == "<synthetic>")
}

// ValidSessionID is exported so internal/cmd applies the one session-id grammar instead of restating
// it, which is the drift session_observation.go warns of.
func ValidSessionID(id string) bool {
	return id != "" && sanitizeSessionID(id) == id
}
