package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
)

type windowPair struct {
	Declared int64 `json:"declared"`
	Applied  int64 `json:"applied"`
}

type boundPair struct {
	Bound   int64 `json:"bound"`
	Applied int64 `json:"applied"`
}

type windowRecordFile struct {
	Profile    string                  `json:"profile"`
	Identities map[string]windowRecord `json:"identities"`
}

type agentWindow struct {
	Applied    int64     `json:"applied"`
	LaunchedAt time.Time `json:"launched_at"`
	LastSeen   time.Time `json:"last_seen"`
}

type windowRecord struct {
	Profile            string                 `json:"-"`
	ModelID            string                 `json:"model_id"`
	HarnessVersion     string                 `json:"harness_version"`
	Class              string                 `json:"class,omitempty"`
	Declared           int64                  `json:"declared"`
	Applied            int64                  `json:"applied"`
	Agents             map[string]agentWindow `json:"agents,omitempty"`
	FirstSeen          time.Time              `json:"first_seen"`
	LastSeen           time.Time              `json:"last_seen"`
	Sessions           int                    `json:"sessions"`
	Source             string                 `json:"source"`
	Probed             int64                  `json:"probed,omitempty"`
	UnobservedNotified bool                   `json:"unobserved_notified,omitempty"`
	NotifiedFor        []windowPair           `json:"notified_for,omitempty"`
	BoundNotifiedFor   []boundPair            `json:"bound_notified_for,omitempty"`
}

type windowLaunchFact struct {
	Profile        string    `json:"profile"`
	ModelID        string    `json:"model_id"`
	Class          string    `json:"class,omitempty"`
	Declared       int64     `json:"declared"`
	HarnessVersion string    `json:"harness_version"`
	LaunchedAt     time.Time `json:"launched_at"`
	AcceptWindow   bool      `json:"accept_window,omitempty"`
}

type windowIdentity struct {
	Profile        string
	ModelID        string
	HarnessVersion string
	Declared       int64
}

const (
	windowFileMaxBytes    = 64 << 10
	windowFieldMaxBytes   = 128
	windowEntryPruneAfter = 30 * 24 * time.Hour
	windowLastSeenRefresh = 24 * time.Hour
	windowClaimTTL        = 2 * time.Minute
	windowClaimWait       = 2 * time.Second
)

func validWindowName(name string) bool {
	if name == "" || len(name) > windowFieldMaxBytes || strings.HasPrefix(name, ".") {
		return false
	}
	for _, r := range name {
		if !strconv.IsPrint(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func harnessVersionToken(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	tok := fields[0]
	if len(tok) > 32 {
		return ""
	}
	for _, r := range tok {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '+', r == '-':
		default:
			return ""
		}
	}
	return tok
}

func harnessVersionOf(versionOutput string) string {
	return harnessVersionToken(displaySafe(strings.TrimSpace(integrationTail(versionOutput, 1))))
}

func windowDeclared(raw string) int64 {
	d, ok := config.DeclaredContextWindow(raw)
	if !ok {
		return 0
	}
	return d
}

func readWindowFile(path string, maxBytes int64) (data []byte, info os.FileInfo, ok bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, nil, false
	}
	data, err = io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, nil, false
	}
	return data, info, true
}

func listWindowFacts(root string) []string {
	dir := windowLaunchedDir(root)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok && name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func windowLaunchedDir(root string) string {
	return filepath.Join(config.StatuslineWindowsDir(root), "launched")
}

func windowRecordPath(root, profile string) (string, bool) {
	if !validWindowName(profile) {
		return "", false
	}
	return filepath.Join(config.StatuslineWindowsDir(root), profile+".json"), true
}

func windowLaunchFactPath(root, agent string) (string, bool) {
	if !validWindowName(agent) || config.ValidateAgentName(agent) != nil {
		return "", false
	}
	return filepath.Join(windowLaunchedDir(root), agent+".json"), true
}

func readWindowLaunchFact(root, agent string) (windowLaunchFact, bool) {
	path, ok := windowLaunchFactPath(root, agent)
	if !ok {
		return windowLaunchFact{}, false
	}
	data, _, ok := readWindowFile(path, windowFileMaxBytes)
	if !ok {
		return windowLaunchFact{}, false
	}
	var f windowLaunchFact
	if json.Unmarshal(data, &f) != nil || !validWindowName(f.Profile) || len(f.ModelID) > windowFieldMaxBytes ||
		f.HarnessVersion != harnessVersionToken(f.HarnessVersion) {
		return windowLaunchFact{}, false
	}
	return f, true
}

func (f windowLaunchFact) identity() windowIdentity {
	return windowIdentity{Profile: f.Profile, ModelID: f.ModelID, HarnessVersion: f.HarnessVersion, Declared: f.Declared}
}

func (id windowIdentity) key() string {
	if id.HarnessVersion == "" {
		return ""
	}
	return id.HarnessVersion + "|" + strconv.FormatInt(id.Declared, 10) + "|" + id.ModelID
}

func readWindowRecordFile(root, profile string) (windowRecordFile, bool) {
	path, ok := windowRecordPath(root, profile)
	if !ok {
		return windowRecordFile{}, false
	}
	data, _, ok := readWindowFile(path, windowFileMaxBytes)
	if !ok {
		return windowRecordFile{}, false
	}
	var f windowRecordFile
	if json.Unmarshal(data, &f) != nil {
		return windowRecordFile{}, false
	}
	return f, true
}

func windowEntryValid(key string, rec windowRecord, now time.Time) bool {
	if len(rec.ModelID) > windowFieldMaxBytes {
		return false
	}
	id := windowIdentity{ModelID: rec.ModelID, HarnessVersion: rec.HarnessVersion, Declared: rec.Declared}
	if key == "" || id.key() != key || rec.HarnessVersion != harnessVersionToken(rec.HarnessVersion) {
		return false
	}
	if now.Sub(rec.LastSeen) > windowEntryPruneAfter {
		return false
	}
	return rec.Applied > 0 || rec.UnobservedNotified
}

func readWindowRecord(root string, id windowIdentity, now time.Time) (windowRecord, bool) {
	key := id.key()
	if key == "" {
		return windowRecord{}, false
	}
	f, ok := readWindowRecordFile(root, id.Profile)
	if !ok {
		return windowRecord{}, false
	}
	rec, ok := f.Identities[key]
	if !ok || !windowEntryValid(key, rec, now) {
		return windowRecord{}, false
	}
	rec.Profile = id.Profile
	return rec, true
}

func (r windowRecord) launchFigure(agent string, launchedAt time.Time) (int64, bool) {
	a, ok := r.Agents[agent]
	if !ok || !a.LaunchedAt.Equal(launchedAt) || a.Applied <= 0 {
		return 0, false
	}
	return a.Applied, true
}

func readCurrentSessionID(agentDir string) (sid string, writtenAt time.Time, ok bool) {
	data, info, ok := readWindowFile(filepath.Join(agentDir, ".runtime", "session_id"), windowFieldMaxBytes)
	if !ok {
		return "", time.Time{}, false
	}
	sid = strings.TrimSpace(string(data))
	if sid == "" {
		return "", time.Time{}, false
	}
	return sid, info.ModTime(), true
}

func recomputeWindowRecord(rec *windowRecord) {
	rec.Applied, rec.Source = 0, ""
	for _, a := range rec.Agents {
		if rec.Applied == 0 || a.Applied < rec.Applied {
			rec.Applied = a.Applied
		}
	}
	switch {
	case len(rec.Agents) > 0:
		rec.Source = "session"
	case rec.Probed > 0:
		rec.Applied, rec.Source = rec.Probed, "probe"
	}
}

func updateWindowRecord(root string, id windowIdentity, now time.Time, mutate func(rec *windowRecord, exists bool) (write bool)) error {
	key := id.key()
	path, ok := windowRecordPath(root, id.Profile)
	if key == "" || !ok {
		return errWindowNoIdentity
	}
	release, ok := claimWindowRecord(root, id.Profile)
	if !ok {
		return errWindowClaim
	}
	defer release()

	f, _ := readWindowRecordFile(root, id.Profile)
	if f.Identities == nil {
		f.Identities = map[string]windowRecord{}
	}
	f.Profile = id.Profile
	rec, exists := f.Identities[key]
	if exists && !windowEntryValid(key, rec, now) {
		rec, exists = windowRecord{}, false
	}
	if !exists {
		rec = windowRecord{}
	}
	rec.Profile = id.Profile
	if !mutate(&rec, exists) {
		return nil
	}
	rec.ModelID, rec.HarnessVersion, rec.Declared = id.ModelID, id.HarnessVersion, id.Declared
	rec.LastSeen = now
	if !exists {
		rec.FirstSeen = now
	}
	for agent, a := range rec.Agents {
		if now.Sub(a.LastSeen) > windowEntryPruneAfter {
			delete(rec.Agents, agent)
		}
	}
	recomputeWindowRecord(&rec)
	f.Identities[key] = rec

	latest := map[string]time.Time{}
	for _, r := range f.Identities {
		for agent, a := range r.Agents {
			if t, seen := latest[agent]; !seen || a.LaunchedAt.After(t) {
				latest[agent] = a.LaunchedAt
			}
		}
	}
	for k, r := range f.Identities {
		changed := false
		for agent, a := range r.Agents {
			if a.LaunchedAt.Before(latest[agent]) {
				delete(r.Agents, agent)
				changed = true
			}
		}
		if changed {
			recomputeWindowRecord(&r)
			f.Identities[k] = r
		}
		if k != key && now.Sub(r.LastSeen) > windowEntryPruneAfter {
			delete(f.Identities, k)
		}
	}

	data, err := json.MarshalIndent(f, "", "  ")
	for err == nil && len(data) > windowFileMaxBytes {
		oldest := ""
		for k, r := range f.Identities {
			if k != key && (oldest == "" || r.LastSeen.Before(f.Identities[oldest].LastSeen)) {
				oldest = k
			}
		}
		if oldest == "" {
			break
		}
		delete(f.Identities, oldest)
		data, err = json.MarshalIndent(f, "", "  ")
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o644)
}

var (
	errWindowNoIdentity = windowError("window record: no identity to file under")
	errWindowClaim      = windowError("window record: claim not obtained")
)

type windowError string

func (e windowError) Error() string { return string(e) }

func claimWindowRecord(root, profile string) (release func(), ok bool) {
	recPath, ok := windowRecordPath(root, profile)
	if !ok {
		return nil, false
	}
	claim := strings.TrimSuffix(recPath, ".json") + ".claim"
	if err := os.MkdirAll(filepath.Dir(claim), 0o755); err != nil {
		return nil, false
	}
	deadline := time.Now().Add(windowClaimWait)
	for {
		now := time.Now()
		mine, err := createStateFile(claim, now)
		if err == nil {
			return func() { releaseWindowClaim(claim, mine) }, true
		}
		if os.IsExist(err) {
			if mine, stolen := stealWindowClaim(claim, now); stolen {
				return func() { releaseWindowClaim(claim, mine) }, true
			}
		}
		if now.After(deadline) {
			return nil, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func stealWindowClaim(claim string, now time.Time) (string, bool) {
	held, info, readable := readWindowFile(claim, windowFieldMaxBytes)
	if readable && now.Sub(info.ModTime()) <= windowClaimTTL {
		return "", false
	}
	corpse := claim + ".stale-" + uniqueLedgerStamp(now)
	if err := os.Rename(claim, corpse); err != nil {
		return "", false
	}
	got, corpseInfo, corpseReadable := readWindowFile(corpse, windowFieldMaxBytes)
	// An unreadable first read is also what a claim another reclaimer just moved away looks like, so
	// a fresh regular file in the corpse means that reclaimer's new claim was taken, not a stale one.
	movedLive := corpseReadable &&
		(readable && !bytes.Equal(got, held) || !readable && now.Sub(corpseInfo.ModTime()) <= windowClaimTTL)
	if movedLive {
		// The claim moved was a fresher holder's, re-created after this caller judged the old one
		// stale. Put it back: this caller retries, and with the path empty its next create would win
		// beside that holder.
		_ = os.Rename(corpse, claim)
		return "", false
	}
	_ = os.Remove(corpse)
	content, err := createStateFile(claim, now)
	if err != nil {
		return "", false
	}
	return content, true
}

func releaseWindowClaim(claim, mine string) {
	if got, _, ok := readWindowFile(claim, windowFieldMaxBytes); !ok || string(got) != mine {
		return
	}
	_ = os.Remove(claim)
}

// writeProbeRecord files a probe's figure under its caller's identity and returns the entry that then
// stands for it. It yields only to a session row of the same identity: a session figure is what the
// harness actually applied, a probe only what it would apply.
func writeProbeRecord(root string, id windowIdentity, p harnessWindowProbe, now time.Time) (windowRecord, error) {
	if p.HarnessVersion == "" || p.HarnessVersion != id.HarnessVersion {
		return windowRecord{}, fmt.Errorf("%w: the probe ran %q, the launch keys on %q", errProbeVersionMismatch, p.HarnessVersion, id.HarnessVersion)
	}
	built := windowRecord{Profile: id.Profile, ModelID: id.ModelID, HarnessVersion: id.HarnessVersion, Declared: id.Declared, Applied: p.ContextWindow, Probed: p.ContextWindow, Source: "probe"}
	var stands windowRecord
	err := updateWindowRecord(root, id, now, func(rec *windowRecord, exists bool) bool {
		if exists && rec.Source == "session" {
			stands = *rec
			return false
		}
		rec.Probed, rec.Class = p.ContextWindow, ""
		return true
	})
	if err != nil {
		return built, err
	}
	if stands.Source == "session" {
		return stands, nil
	}
	rec, ok := readWindowRecord(root, id, now)
	if !ok {
		return built, nil
	}
	return rec, nil
}
