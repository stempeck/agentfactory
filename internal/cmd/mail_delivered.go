package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// mailDeliveredSessions caps how many sessions the file remembers. The file is a cache of what
// each LIVE session has already been shown, not a history: an entry that ages out costs one
// repeated block to a session that has almost certainly ended.
const mailDeliveredSessions = 8

// mailDeliveredEntry is one session's delivered set: message id to the RFC3339 instant it entered
// that session's context.
type mailDeliveredEntry struct {
	Delivered map[string]string `json:"delivered"`
	Updated   string            `json:"updated"`
}

// mailDeliveredRecord is the whole of .runtime/mail_delivered — the dimension the mail store
// cannot carry. The store owns exactly one bit per message and it already means ACKNOWLEDGED
// (mailbox.go:131-145 collapses every terminal verb onto it), so recording SEEN there would
// destroy the acknowledgment `af mail delete` exists to express.
//
// Keyed by session id and never globally: a message that was delivered to a session whose context
// is gone has not been delivered to the agent reading the next one.
type mailDeliveredRecord struct {
	Sessions map[string]mailDeliveredEntry `json:"sessions"`
}

func mailDeliveredPath(workDir string) string {
	return filepath.Join(workDir, ".runtime", "mail_delivered")
}

// mailSourceResets answers whether the host told us this session's context was replaced rather
// than continued. compact and clear both mean the transcript the agent can see no longer holds
// what was delivered to it, so the delivered-state must stop claiming it does. startup, resume and
// fork all carry context forward — and a genuinely new session arrives with a new id anyway.
func mailSourceResets(source string) bool {
	return source == "compact" || source == "clear"
}

// loadMailDelivered returns the whole file plus the entry this payload's session should be judged
// against. The entry comes back EMPTY — not missing — whenever the file is absent, will not
// decode, does not carry this session, or the host reports a context reset. That is the same
// fail-open direction loadPrimeCount takes (prime_economics.go:134-145): the cost of being wrong
// is one repeated block, and the alternative is an agent that silently never sees its mail.
func loadMailDelivered(workDir string, payload hookPayload) (mailDeliveredRecord, mailDeliveredEntry) {
	var rec mailDeliveredRecord
	if data, err := os.ReadFile(mailDeliveredPath(workDir)); err == nil {
		if json.Unmarshal(data, &rec) != nil {
			rec = mailDeliveredRecord{}
		}
	}
	if rec.Sessions == nil {
		rec.Sessions = map[string]mailDeliveredEntry{}
	}
	for id, entry := range rec.Sessions {
		if entry.Delivered == nil {
			entry.Delivered = map[string]string{}
			rec.Sessions[id] = entry
		}
	}

	if mailSourceResets(payload.Source) {
		return rec, mailDeliveredEntry{Delivered: map[string]string{}}
	}
	if entry, ok := rec.Sessions[payload.SessionID]; ok {
		return rec, entry
	}
	return rec, mailDeliveredEntry{Delivered: map[string]string{}}
}

// recordDelivered stamps onto the entry the ids a block ACTUALLY emitted. Only those: an id the
// budget deferred to a later call has not entered context, and recording it would lose the message
// outright.
func recordDelivered(entry mailDeliveredEntry, ids []string, now time.Time) mailDeliveredEntry {
	if entry.Delivered == nil {
		entry.Delivered = make(map[string]string, len(ids))
	}
	stamp := now.UTC().Format(time.RFC3339)
	for _, id := range ids {
		entry.Delivered[id] = stamp
	}
	entry.Updated = stamp
	return entry
}

// saveMailDelivered writes the record back under sessionID, best-effort: a hook that cannot
// persist its bookkeeping must still deliver the mail (ADR-007), so every failure here is silent
// and the next call simply re-delivers.
//
// Reconciliation happens on the way out rather than on a schedule. Pruning to ids still open is
// what makes `af mail delete` the acknowledgment it has always been (C-13): the moment a message
// leaves the open set, every session's claim to have delivered it is dropped, so a message that is
// deleted and later re-sent is delivered again.
func saveMailDelivered(workDir, sessionID string, rec mailDeliveredRecord, entry mailDeliveredEntry, openIDs map[string]bool) {
	if rec.Sessions == nil {
		rec.Sessions = map[string]mailDeliveredEntry{}
	}
	rec.Sessions[sessionID] = entry

	for _, e := range rec.Sessions {
		for id := range e.Delivered {
			if !openIDs[id] {
				delete(e.Delivered, id)
			}
		}
	}
	pruneMailDelivered(rec, sessionID)

	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	path := mailDeliveredPath(workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// pruneMailDelivered keeps the mailDeliveredSessions most recently updated entries, and the
// caller's own session unconditionally. The ordering is spelled out rather than left to sort
// stability because RFC3339 is second-resolution and parallel hooks tie routinely.
func pruneMailDelivered(rec mailDeliveredRecord, keep string) {
	if len(rec.Sessions) <= mailDeliveredSessions {
		return
	}
	ids := make([]string, 0, len(rec.Sessions))
	for id := range rec.Sessions {
		ids = append(ids, id)
	}
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
	for _, id := range ids[mailDeliveredSessions:] {
		delete(rec.Sessions, id)
	}
}

// claimedSession answers whether this call may write delivered-state. It needs a session id to key
// on AND a tmux pane to prove the caller is an agent's own hook rather than a grader, a subagent or
// a human at a shell — none of which own the session's context and none of which should be able to
// suppress the next real delivery.
//
// TMUX_PANE is read inline instead of through hookRunsInAgentPane because that helper warns on
// stderr when it misses (prime.go:493-494). prime pays that once per session; mail runs on
// UserPromptSubmit and would pay it once per PROMPT, plus once for every `af mail check` a human
// types. memory routes the same class of noise to io.Discard (memory.go:915-922).
// An Agent-tool sub-agent inherits the parent's TMUX_PANE and can carry the parent's session id, so
// the pane + id pair alone would let it claim delivered-state and suppress the parent's next real
// delivery (#681 T7). The host writes a sub-agent's own transcript under a `/subagents/` path
// (subagent_occupancy.go), so a `/subagents/` transcript path is refused. This does not close the
// residual case where a sub-agent's SessionStart carries the PARENT's transcript path — that is
// bounded by the pane guard and the per-session-id map and re-verified by the K11 payload-capture check.
func claimedSession(payload hookPayload) bool {
	if strings.Contains(payload.TranscriptPath, "/subagents/") {
		return false
	}
	return payload.SessionID != "" && os.Getenv("TMUX_PANE") != ""
}

// mailExcerpt caps a body at n runes — characters, not bytes, so a message written in any script
// gets the same amount of text and no cut lands mid-rune. It reports whether it cut, because the
// block discloses truncation with an `af mail read <id>` pointer rather than a bare ellipsis: the
// pointer is what keeps the rest one command away. (memory's excerpt is unexported in package
// memory, so this is a local copy of its rune discipline, not a call.)
func mailExcerpt(body string, n int) (string, bool) {
	runes := []rune(body)
	if len(runes) <= n {
		return body, false
	}
	return string(runes[:n]), true
}
