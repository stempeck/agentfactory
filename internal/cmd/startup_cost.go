package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

const (
	startupTurnPendingFile = "startup_turn_pending"
	startupTurnClaimedFile = "startup_turn_claimed"
	startupLaunchRespawn   = "respawn"
	startupTurnReadCap     = 4 << 10
	startupSessionIDCap    = 128
)

const (
	rulingsBlockPresent = "present"
	rulingsBlockPartial = "partial"
	rulingsBlockNoPins  = "no_pins"
	rulingsBlockAbsent  = "absent"
)

const startupUnknown = "unknown"

// startupTranscriptScanCap bounds the per-tick scan for the first Stop. Across 17 manager
// transcripts the latest first Stop ended at 1.4 MB, so a transcript past the cap with none has
// stopped being a startup turn, and keeping its marker would re-read the whole file every tick.
const startupTranscriptScanCap = 16 << 20

const startupScanCapWarning = "warning: startup transcript of %s passed %d bytes with no first Stop; this startup is not measured\n"

type startupTurn struct {
	SessionID      string `json:"session_id"`
	Tokens         int64  `json:"tokens"`
	Budget         int64  `json:"budget"`
	HarnessVersion string `json:"harness_version"`
	RulingsBlock   string `json:"rulings_block"`
	At             string `json:"at"`
}

func startupMeasured(role string) bool { return role == "manager" || role == "supervisor" }

func startupTurnDir(root string) string { return filepath.Join(root, ".runtime", "startup_turn") }

func startupTurnPath(root, agent string) string {
	return filepath.Join(startupTurnDir(root), agent+".json")
}

func writeStartupTurnPending(agentDir, sessionID string) {
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if os.MkdirAll(runtimeDir, 0o755) != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(runtimeDir, startupTurnPendingFile), []byte(sessionID), 0o644)
}

// startupFileUsable runs before every open in the startup half because opening a FIFO blocks, and
// a FIFO's Lstat size is 0: one planted in an agent directory would stall the watchdog tick and
// every recovery it drives.
func startupFileUsable(path string, max int64) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return max <= 0 || info.Size() <= max
}

func readStartupFile(path string, max int64) (string, bool) {
	if !startupFileUsable(path, max) {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(data)) > max {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

func readStartupTurn(root, agent string) (startupTurn, bool) {
	if !startupMeasured(agent) {
		return startupTurn{}, false
	}
	raw, ok := readStartupFile(startupTurnPath(root, agent), startupTurnReadCap)
	if !ok {
		return startupTurn{}, false
	}
	var t startupTurn
	if json.Unmarshal([]byte(raw), &t) != nil {
		return startupTurn{}, false
	}
	if !statusline.ValidSessionID(t.SessionID) {
		t.SessionID = startupUnknown
	}
	switch t.RulingsBlock {
	case rulingsBlockPresent, rulingsBlockPartial, rulingsBlockNoPins, rulingsBlockAbsent:
	default:
		t.RulingsBlock = startupUnknown
	}
	t.HarnessVersion = startupHarnessToken(t.HarnessVersion)
	if _, err := time.Parse(time.RFC3339, t.At); err != nil {
		t.At = startupUnknown
	}
	return t, true
}

func rulingsBlockState(e memoryInjectedEntry, recorded bool) string {
	switch {
	case !recorded:
		return rulingsBlockAbsent
	case e.PinsTotal == 0:
		return rulingsBlockNoPins
	case e.PinsRendered < e.PinsTotal:
		return rulingsBlockPartial
	}
	return rulingsBlockPresent
}

// startupHarnessGrammar is the window half's charset and bound, copied rather than called: no
// component of the startup half may call one of the window half (.designs/754/design-doc.md C-6),
// yet one harness version must not read as a version in one half and as unknown in the other.
var startupHarnessGrammar = regexp.MustCompile(`^[0-9A-Za-z.+-]{1,32}$`)

func startupHarnessToken(version string) string {
	if startupHarnessGrammar.MatchString(version) {
		return version
	}
	return startupUnknown
}

func claimStartupTurn(agentDir, sessionID string) bool {
	marker := filepath.Join(agentDir, ".runtime", startupTurnPendingFile)
	claimed := filepath.Join(agentDir, ".runtime", startupTurnClaimedFile)
	if os.Rename(marker, claimed) != nil {
		return false
	}
	if held, ok := readStartupFile(claimed, startupSessionIDCap); ok && held == sessionID {
		return true
	}
	// A newer session was armed between the caller's read and the rename. Link, unlike rename,
	// never replaces a marker a still newer session may have written in the meantime.
	_ = os.Link(claimed, marker)
	_ = os.Remove(claimed)
	return false
}

func measureStartupTurn(root, agent string) (retired bool) {
	if !startupMeasured(agent) {
		return
	}
	agentDir := resolveAgentDir(root, agent)
	runtimeDir := filepath.Join(agentDir, ".runtime")
	marker, ok := readStartupFile(filepath.Join(runtimeDir, startupTurnPendingFile), startupSessionIDCap)
	if !ok {
		return
	}
	sid, ok := readStartupFile(filepath.Join(runtimeDir, "session_id"), startupSessionIDCap)
	if !ok || sid != marker || !statusline.ValidSessionID(sid) {
		return
	}
	// sessionTranscriptPath reads transcript_path with a plain os.ReadFile.
	transcriptPathFile := filepath.Join(runtimeDir, "transcript_path")
	if _, err := os.Lstat(transcriptPathFile); err == nil && !startupFileUsable(transcriptPathFile, startupTurnReadCap) {
		return
	}
	transcript := sessionTranscriptPath(agentDir, sid)
	if transcript == "" || strings.Contains(transcript, "/subagents/") || !startupFileUsable(transcript, 0) {
		return
	}
	f, err := os.Open(transcript)
	if err != nil {
		return
	}
	peak, ok := statusline.TranscriptPeakOccupancy(io.LimitReader(f, startupTranscriptScanCap))
	info, statErr := f.Stat()
	f.Close()
	if !ok || !peak.Cut {
		if statErr != nil || info.Size() < startupTranscriptScanCap || !claimStartupTurn(agentDir, sid) {
			return
		}
		_ = os.Remove(filepath.Join(runtimeDir, startupTurnClaimedFile))
		return true
	}
	if !claimStartupTurn(agentDir, sid) {
		return
	}
	defer os.Remove(filepath.Join(runtimeDir, startupTurnClaimedFile))

	entry, recorded := memoryInjectedEntry{}, false
	if startupFileUsable(memoryInjectedPath(agentDir), 64<<10) {
		entry, recorded = loadMemoryInjected(agentDir, sid)
	}
	turn := startupTurn{
		SessionID:      sid,
		Tokens:         peak.Tokens,
		Budget:         startupTurnBudgetTokens,
		HarnessVersion: startupHarnessToken(peak.HarnessVersion),
		RulingsBlock:   rulingsBlockState(entry, recorded),
		At:             time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(turn)
	if err != nil || os.MkdirAll(startupTurnDir(root), 0o755) != nil {
		return
	}
	if fsutil.WriteFileAtomic(startupTurnPath(root, agent), append(data, '\n'), 0o644) != nil {
		return
	}
	if telemetryFactoryEnabled(root) {
		ev := telemetryRecordFor(context.Background(), root, agentDir, agent, "", "")
		ev.Event = telemetry.EventStartupEnd
		// telemetryRecordFor fills the agent's current .runtime/session_id, which a newer session
		// may have replaced since the marker was read.
		ev.SessionID = sid
		tokens := turn.Tokens
		ev.CtxTokensUsed = &tokens
		ev.CtxBoundTokens = turn.Budget
		ev.HostVersion = turn.HarnessVersion
		appendTelemetryRecord(root, ev)
	}
	return
}
