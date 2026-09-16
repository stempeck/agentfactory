package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// The thinking estimate's divisor. The formula is pinned by design (D17) and implemented as
// specified rather than improved: the point of a pinned estimator is that two runs measured months
// apart are comparable, which a tuned one would not be. It counts RUNES, so a step that emitted
// non-ASCII prose is not charged extra visible tokens for the encoding.
const runesPerTokenEstimate = 4

// Read HERE, in the cmd layer, and never in a library package — ADR-004 keeps internal/config and
// its siblings free of environment reads, and this is the one layer that legitimately knows the
// process it is running in.
const claudeConfigDirEnv = "CLAUDE_CONFIG_DIR"

// measured is not derivable from the pointers — a real step can genuinely produce zero — so it is
// carried explicitly and every pointer is nil when it is false.
//
// reason says WHY when measured is false, from telemetry's closed vocabulary (#678 K1). It is a
// field on the carrier rather than a second return value because the two answers are one fact: a
// derivation that declined and a derivation that declined for a reason are the same derivation, and
// splitting them invites a caller to record one without the other.
//
// think is the host's exact count and is separately nillable from the trio: a step measured on a
// host too old to report thinking has real out/peak figures and no thinking figure, and collapsing
// that into "unmeasured" would discard the figures that ARE trustworthy.
type generationScalars struct {
	measured bool
	reason   string
	out      *int64
	thinkEst *int64
	peak     *int64

	think          *int64
	in             *int64
	cacheRead      *int64
	cacheCreation  *int64
	subagentLaunch int64
	workflowLaunch int64
	repeatReads    int64
	hostVersion    string
}

// generationRecord is this package's own decode target, deliberately NOT internal/statusline's
// usageRecord. That one reads two fields and documents why it excludes the cache splits: they are
// not part of the SPEND figure the statusline displays. Occupancy is a different quantity and does
// include them, so widening the shared type would have changed a number nobody asked to change.
//
// Content carries Name and a DELIBERATELY NARROW Input (#678 K1). A tool_use block's input is an
// arbitrary JSON object — it is the single richest source of free-form content in the whole
// transcript, holding whole sub-agent prompts and file bodies — so it is decoded into two named
// string fields rather than a map. Everything else in the object is discarded by the decoder before
// it ever exists in memory, which is a stronger guarantee than remembering not to record it. Neither
// field leaves this pass: FilePath and Command are compared to decide whether a read REPEATS, and
// what is recorded is the count.
type generationRecord struct {
	Timestamp string `json:"timestamp"`
	// The host stamps its own version on every record, so this is the version that MEASURED the
	// step rather than anything af knows about itself.
	Version string `json:"version"`
	Message struct {
		ID      string `json:"id"`
		Content []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Name  string `json:"name"`
			Input struct {
				FilePath string `json:"file_path"`
				Command  string `json:"command"`
			} `json:"input"`
		} `json:"content"`
		Usage *struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			// A POINTER, because its absence is the question. The host adds this object only from
			// one version onward, and even there it is missing from the streaming partials of a
			// message whose settled line carries it. Nil means "this line said nothing about
			// thinking", which is not "this line said zero".
			OutputTokensDetails *struct {
				ThinkingTokens int64 `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"message"`
}

// An unreadable transcript is unmeasured, not an error: this runs on the af done hot path, where
// observability never blocks a close, and the transcript belongs to the host rather than to af — it
// can be absent, expired or relocated without anything being wrong.
//
// The cost is O(whole transcript) per close, not O(step). A step's window is a timestamp range and
// the file carries no index, so finding it means decoding every line — a late step in a long session
// re-reads everything before it. Measured at ~30ms over the largest transcript on this host (8.4MB),
// against a verb that already does bead I/O, so it is paid rather than optimised. The statusline's
// byte-offset cursor is the obvious remedy and is deliberately not reused: that cursor is one live
// reader's position, and borrowing it here would make two consumers fight over the same bookmark.
func transcriptGenerationScalars(path, startTS, endTS string) generationScalars {
	if path == "" {
		return generationScalars{reason: telemetry.ReasonTranscriptMissing}
	}
	f, err := os.Open(path)
	if err != nil {
		return generationScalars{reason: telemetry.ReasonTranscriptMissing}
	}
	defer f.Close()
	return deriveGenerationScalars(f, startTS, endTS)
}

// deriveGenerationScalars reduces one step's window of a session transcript to three figures, in
// ONE pass, under TWO OPPOSITE rules.
//
// Usage is DEDUPED. Claude Code writes one record per content block and stamps the whole message's
// usage on every one, so records are reduced per message.id with MAX per field. Summing lines
// instead over-counts by ~2.2x, which is the arithmetic that produced this feature's original
// headline figure. MAX and not first-wins: an in-flight record carries a partial output_tokens that
// first-wins would keep in place of the completed count.
//
// Visible text is NOT deduped. Each content block appears in exactly one record, so the same
// reduction applied to text would discard every block after the first and inflate the thinking
// estimate — the very figure this exists to stop over-stating.
//
// It reads to EOF and deliberately does NOT reuse ScanUsage's trailing-run holdback. That holdback
// exists because the statusline reads a LIVE transcript on a 10s cadence and lands inside a
// message's record run most of the time; here the step is already closed, so holding back the
// trailing run would drop the step's final message every single time.
//
// The line reader is statusline's rather than a bufio.Scanner. Scanner ABANDONS the rest of a file
// on a token above its cap, so one oversized tool result would silently truncate a step's figures
// and still report them as measured; ReadTranscriptLine skips the line's content and keeps going.
func deriveGenerationScalars(r io.Reader, startTS, endTS string) generationScalars {
	// A window that will not parse is reported as one that held nothing, which is the closest of the
	// three words the vocabulary has: the transcript is present and it is this session's, and no
	// record can be inside a window that does not exist. The reason field is for a reader auditing
	// why a baseline is missing, not for diagnosing af — and af writes both of these timestamps.
	start, startErr := time.Parse(time.RFC3339, startTS)
	end, endErr := time.Parse(time.RFC3339, endTS)
	if startErr != nil || endErr != nil || end.Before(start) {
		return generationScalars{reason: telemetry.ReasonNoRecordsInWindow}
	}

	perMessage := map[string]statusline.MessageUsage{}
	var visibleRunes int64
	seen := false

	// #678 K1, counted in the SAME pass. A second pass would double a cost whose doc above explains
	// why it is already the expensive part of a close.
	var subagentLaunch, workflowLaunch, repeatReads int64
	thinkSeen := false
	hostVersion := ""
	// The path is remembered only to answer "again?" and only for this pass's lifetime. A count can
	// be recorded where the set itself could not: nothing here reaches a record.
	readPaths := map[string]bool{}

	br := statusline.NewTranscriptReader(r)
	for {
		line, _, ok := statusline.ReadTranscriptLine(br)
		if !ok {
			break
		}
		var rec generationRecord
		if line == nil || json.Unmarshal(line, &rec) != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.Timestamp)
		// Half-open [start, end), the same window rule the telemetry join uses: a record stamped
		// exactly at the close belongs to whatever comes next.
		if err != nil || ts.Before(start) || !ts.Before(end) {
			continue
		}

		// Counted for EVERY in-window record, before the usage guard below, because what a step DID
		// is a fact about the step whether or not the host also reported what the message cost. Each
		// content block appears in exactly one record (the doc above), so a block counted here is
		// counted once. Last non-empty version wins: a session that spans a host upgrade carries two,
		// and the one that measured the end of the step is the one a reader comparing this step to
		// the next should see.
		//
		// This admits strictly more records than the MAX-dedup pass below, which needs a message.id to
		// dedup BY and a usage object to dedup: a record carrying neither is skipped there and counted
		// here. The divergence is the point — an id and a price are what a token figure requires, not
		// what an action requires, and a launch that went unpriced still happened.
		if rec.Version != "" {
			hostVersion = rec.Version
		}
		for _, b := range rec.Message.Content {
			if b.Type != "tool_use" {
				continue
			}
			switch {
			case isSubagentTool(b.Name):
				subagentLaunch++
			// Counted SEPARATELY and deliberately not folded into isSubagentTool: that predicate is
			// the dispatch gate's and widening it here would change an admission decision to make a
			// measurement tidier.
			case b.Name == workflowToolName:
				workflowLaunch++
			case b.Name == readToolName:
				if p := b.Input.FilePath; p != "" {
					if readPaths[p] {
						repeatReads++
					}
					readPaths[p] = true
				}
			case b.Name == bashToolName:
				// A shell that cats a file is a read wearing a different hat, and the design counts
				// it as one. Only the two forms that are unambiguously whole-file reads are matched;
				// a command that merely mentions a path is left alone, because a re-read indicator
				// that fired on greps would measure activity rather than repetition.
				if p := bashReadTarget(b.Input.Command); p != "" {
					if readPaths[p] {
						repeatReads++
					}
					readPaths[p] = true
				}
			}
		}

		if rec.Message.ID == "" || rec.Message.Usage == nil {
			continue
		}
		seen = true

		thinking := int64(0)
		if d := rec.Message.Usage.OutputTokensDetails; d != nil {
			thinking = d.ThinkingTokens
			thinkSeen = true
		}
		u := perMessage[rec.Message.ID]
		u.Absorb(statusline.MessageUsage{
			InputTokens:         rec.Message.Usage.InputTokens,
			OutputTokens:        rec.Message.Usage.OutputTokens,
			CacheReadTokens:     rec.Message.Usage.CacheReadTokens,
			CacheCreationTokens: rec.Message.Usage.CacheCreationTokens,
			ThinkingTokens:      thinking,
		})
		perMessage[rec.Message.ID] = u

		for _, b := range rec.Message.Content {
			// Only "text" is visible output. A thinking block's text is not written to the
			// transcript at all — it carries a signature and an empty string — which is exactly
			// why thinking has to be estimated by subtraction rather than counted.
			if b.Type == "text" {
				visibleRunes += int64(utf8.RuneCountInString(b.Text))
			}
		}
	}
	if !seen {
		// The transcript opened and its window held no usage-bearing record. Distinct from a missing
		// transcript: the host is reachable and the step is simply not in it — a step that spanned a
		// session recycle, or one whose records the host has rotated away.
		return generationScalars{reason: telemetry.ReasonNoRecordsInWindow}
	}

	var out, peak, think, in, cacheRead, cacheCreation int64
	for _, u := range perMessage {
		out += u.OutputTokens
		think += u.ThinkingTokens
		in += u.InputTokens
		cacheRead += u.CacheReadTokens
		cacheCreation += u.CacheCreationTokens
		if occ := u.Occupancy(); occ > peak {
			peak = occ
		}
	}

	// Tool-call arguments are output that is not visible text, so they land on the thinking side of
	// this subtraction — measured at roughly a 20% over-attribution on a real transcript. The bias
	// is documented on the field rather than corrected for, because the formula is pinned (D17) and
	// a share indicator that changed definition between releases would not be comparable.
	thinkEst := out - visibleRunes/runesPerTokenEstimate
	if thinkEst < 0 {
		thinkEst = 0
	}

	g := generationScalars{
		measured: true,
		out:      &out, thinkEst: &thinkEst, peak: &peak,
		in: &in, cacheRead: &cacheRead, cacheCreation: &cacheCreation,
		subagentLaunch: subagentLaunch, workflowLaunch: workflowLaunch,
		repeatReads: repeatReads, hostVersion: hostVersion,
	}
	// The exact figure exists only if the host said something about thinking somewhere in the window.
	// Without this guard an older host — which reports the detail on no record at all — would produce
	// a confident 0 that is indistinguishable from a step that genuinely did no thinking, and the
	// efficiency predicate would read the wrong one as a baseline.
	if thinkSeen {
		g.think = &think
	}
	return g
}

// The tool names counted in the pass above. Named constants rather than literals at the comparison
// site because they are HOST vocabulary, not af's: they change when the host's tool set changes, and
// a reader deciding whether a count is still correct needs them in one place. isSubagentTool
// (subagent_tool.go) is not extended to cover these — it is the dispatch gate's predicate.
const (
	workflowToolName = "Workflow"
	readToolName     = "Read"
	bashToolName     = "Bash"
)

// bashReadTarget returns the file a shell command reads WHOLE, or "" if it is not that kind of
// command. Only `cat ` and `sed -n ` are matched, per the design: they are the two forms that read a
// file the way the Read tool does. A single argument is required — `cat a b` is a concatenation and
// `cat` with a redirect or a pipe is a different operation, and treating either as a re-read of the
// first word would make the indicator fire on activity rather than on repetition.
func bashReadTarget(command string) string {
	cmd := strings.TrimSpace(command)
	var rest string
	switch {
	case strings.HasPrefix(cmd, "cat "):
		rest = strings.TrimPrefix(cmd, "cat ")
	case strings.HasPrefix(cmd, "sed -n "):
		rest = strings.TrimPrefix(cmd, "sed -n ")
	default:
		return ""
	}
	fields := strings.Fields(rest)
	// sed -n carries a script argument before the file; cat carries the file first.
	if strings.HasPrefix(cmd, "sed -n ") {
		if len(fields) != 2 {
			return ""
		}
		return fields[1]
	}
	if len(fields) != 1 {
		return ""
	}
	return fields[0]
}

// transcriptDirSlug folds a working directory into the host's project-directory name. The rule is
// `[/._] → -` and it is a fact about Claude Code rather than a choice: derived by reading the `cwd`
// field back out of the transcripts in every one of 1,898 project directories on a real host
// (claude 2.1.224) and confirming the mapping reproduces the directory name in all 1,898.
//
// This comment used to warn that underscore "bites silently" — that an agent named
// `soldesign_engineer`, or a factory under /home/dev/my_repo, would resolve to a directory that does
// not exist. That warning was WRONG and is corrected here rather than deleted, because it was
// repeated downstream as a known defect. Re-measured on claude 2.1.258 over every project directory
// on a real host: the replacer reproduces the actual directory name in 548 of 548 cases, including
// all 391 whose cwd contains an underscore. The host slugifies `_` → `-` exactly as this rule does.
//
// The derivation is nonetheless a DEPENDENCY ON AN UNDOCUMENTED CONVENTION, which is why
// sessionTranscriptPath now prefers a path the host handed us. That is the honest reason for the
// preference; there is no live failure being repaired.
var transcriptDirSlug = strings.NewReplacer("/", "-", ".", "-", "_", "-")

// sessionTranscriptPath answers where the host keeps one session's transcript, preferring what the
// host SAID over what af can derive.
//
// The persisted path (.runtime/transcript_path, written by af prime's SessionStart hook, #678 K1) is
// the host's own answer and is used when it names a file that exists. Otherwise the derivation
// stands: <configDir>/projects/<slug>/<sessionID>.jsonl. The fallback is not a degraded mode — it is
// measured correct on every project directory on a real host (see transcriptDirSlug) — so an agent
// primed by an older af, or one whose hook payload carried no transcript_path, loses nothing.
//
// Two checks are what make preferring safe, and existence alone is not one of them. The marker names
// the session it was written for, and a marker written for a DIFFERENT session is ignored even though
// the file it names is perfectly real — that is precisely the case where preferring would redirect
// this step's figures to somebody else's transcript.
//
// An empty session id yields "", because there is no transcript for a session nobody recorded.
func sessionTranscriptPath(workDir, sessionID string) string {
	if sessionID == "" || workDir == "" {
		return ""
	}
	if p := persistedTranscriptPath(workDir, sessionID); p != "" {
		return p
	}
	return filepath.Join(claudeConfigDir(), "projects", transcriptDirSlug.Replace(workDir), sessionID+".jsonl")
}

// persistedTranscriptPath returns the host's own answer for THIS session, or "" for every other
// case — no marker, a marker in the pre-#678 bare-path format, a marker belonging to another
// session, or one naming a file the host has since expired. Every one of those falls back to the
// derivation, which is measured correct.
func persistedTranscriptPath(workDir, sessionID string) string {
	raw, err := os.ReadFile(filepath.Join(workDir, ".runtime", "transcript_path"))
	if err != nil {
		return ""
	}
	marker, p, ok := strings.Cut(strings.TrimSpace(string(raw)), "\t")
	if !ok || marker != sessionID || p == "" {
		return ""
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return p
}

func claudeConfigDir() string {
	if dir := os.Getenv(claudeConfigDirEnv); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// attachGenerationScalars is af done's half of the derivation: it decides whether the question can
// be asked at all, and only then asks it.
//
// The session guard is stepCumTokensDelta's, for the same reason (cross-review HIGH-3). A transcript
// is ONE session's. A step that spanned a recycle has its first half in a transcript this function
// will never open, so the figures it could produce would describe part of a step while looking like
// the whole of one. Refusing is the honest answer, and the pointers stay nil to say so.
func attachGenerationScalars(ev *telemetry.StepEvent, span stepSpan, workDir string) {
	// #678 K1: the refusal says which refusal it was. A step whose figures are absent because no
	// transcript could describe it honestly, and one absent because the host lost the file, are
	// different facts about the experiment — the first is expected and the second is a defect — and a
	// reader with only nil pointers cannot tell them apart.
	//
	// The two refusals are separated because they are not the same event. A step with no opening
	// record has no WINDOW: telemetry was switched on mid-formula, and there is nothing to look in.
	// Calling that a session mismatch would report a defect (the step's two ends disagreeing about
	// which session ran it) every time an operator turned the feature on.
	if span.startTS == "" {
		ev.GenerationUnmeasuredReason = telemetry.ReasonNoRecordsInWindow
		return
	}
	if span.sessionID == "" || ev.SessionID == "" || span.sessionID != ev.SessionID {
		ev.GenerationUnmeasuredReason = telemetry.ReasonSessionMismatch
		return
	}
	// #668 K18. Asked SEPARATELY from the trio below, and deliberately not gated on it: a step whose
	// main transcript the host has expired can still have a readable sub-agent tree, and refusing the
	// figure it CAN produce because of the one it cannot would lose the larger half — 37.2% of corpus
	// tokens live in these files (internal/statusline/testdata/transcript/README.md §c).
	if s, ok := subagentSpend(sessionSubagentDir(workDir, ev.SessionID), span.startTS, ev.TS); ok {
		total, in, out, nested := s.total, s.in, s.out, s.nestedLaunches
		ev.SubagentTokens = &total
		ev.SubagentInTokens, ev.SubagentOutTokens = &in, &out
		ev.SubagentNestedLaunches = &nested
	}

	g := transcriptGenerationScalars(sessionTranscriptPath(workDir, ev.SessionID), span.startTS, ev.TS)
	if !g.measured {
		ev.GenerationUnmeasuredReason = g.reason
		return
	}
	ev.OutTokens, ev.ThinkTokensEst, ev.PeakCtxTokens = g.out, g.thinkEst, g.peak
	ev.ThinkTokens = g.think
	ev.InTokens, ev.CacheReadTokens, ev.CacheCreationTokens = g.in, g.cacheRead, g.cacheCreation
	ev.HostVersion = g.hostVersion
	// Recorded even at zero, unlike the token figures: this pass READ the whole window, so "no
	// sub-agent was launched" is something it observed rather than something it failed to see. The
	// token pointers stay nil-when-absent because their absence means the opposite — nobody measured.
	subagentLaunch, workflowLaunch, repeatReads := g.subagentLaunch, g.workflowLaunch, g.repeatReads
	ev.SubagentLaunches, ev.WorkflowLaunches = &subagentLaunch, &workflowLaunch
	ev.RepeatReads = &repeatReads
}
