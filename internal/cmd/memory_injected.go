package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// memoryInjectedSessions bounds the session-keyed injection record (D-15) the way
// mailDeliveredSessions bounds mail's: the record answers "what did THIS session receive", so only
// recent sessions are worth keeping.
const memoryInjectedSessions = 8

type memoryInjectedEntry struct {
	PinsRendered int    `json:"pins_rendered"`
	PinsTotal    int    `json:"pins_total"`
	Updated      string `json:"updated"`
}

type memoryInjectedRecord struct {
	Sessions map[string]memoryInjectedEntry `json:"sessions"`
}

func memoryInjectedPath(workDir string) string {
	return filepath.Join(workDir, ".runtime", "memory_injected.json")
}

func readMemoryInjected(workDir string) memoryInjectedRecord {
	var rec memoryInjectedRecord
	if f, err := os.Open(memoryInjectedPath(workDir)); err == nil {
		data, _ := io.ReadAll(io.LimitReader(f, 64<<10))
		f.Close()
		if json.Unmarshal(data, &rec) != nil {
			rec = memoryInjectedRecord{}
		}
	}
	if rec.Sessions == nil {
		rec.Sessions = map[string]memoryInjectedEntry{}
	}
	return rec
}

// saveMemoryInjected runs on the SessionStart hook path, so every failure is dropped silently. It
// writes only for a claimedSession: a grader, a sub-agent or a shell does not own the session whose
// pinned block it would be recording. The prune order is pruneMailDelivered's.
func saveMemoryInjected(workDir string, payload hookPayload, e memoryInjectedEntry, now time.Time) {
	if !claimedSession(payload) {
		return
	}
	rec := readMemoryInjected(workDir)
	e.Updated = now.UTC().Format(time.RFC3339)
	rec.Sessions[payload.SessionID] = e
	if len(rec.Sessions) > memoryInjectedSessions {
		ids := make([]string, 0, len(rec.Sessions))
		for id := range rec.Sessions {
			ids = append(ids, id)
		}
		keep := payload.SessionID
		sort.Slice(ids, func(i, j int) bool {
			if (ids[i] == keep) != (ids[j] == keep) {
				return ids[i] == keep
			}
			a, b := rec.Sessions[ids[i]].Updated, rec.Sessions[ids[j]].Updated
			if a != b {
				return a > b
			}
			return ids[i] < ids[j]
		})
		for _, id := range ids[memoryInjectedSessions:] {
			delete(rec.Sessions, id)
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	path := memoryInjectedPath(workDir)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

func loadMemoryInjected(workDir, sessionID string) (memoryInjectedEntry, bool) {
	e, ok := readMemoryInjected(workDir).Sessions[sessionID]
	return e, ok
}
