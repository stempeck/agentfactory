package cmd

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/statusline"
)

// This file is #668 K18's derivation half: what a step's SUB-agents spent, summed across the sibling
// transcript tree the main derivation never opens.
//
// The size of the hole it closes is measured, not assumed. internal/statusline/testdata/transcript/
// README.md §(c) reports 37.2% of all corpus tokens living in these files, and reading
// transcript_path alone capturing only 58.3% of a sub-agent-using session's spend.
//
// It runs at STEP CLOSE and nowhere else. DEC-3 scoped this out of the statusline for three reasons
// that are all still true — up to 122 sub-agent files in one session, a 500 ms render budget, and a
// 4 KB maxSnapshotBytes cursor cap that per-file offsets would breach — and none of them is an
// argument against paying the cost once, on a verb that is already doing bead I/O.
// TestSubagentScanStaysOffTheRenderPath is the interlock that keeps it here.

// sessionSubagentDir answers where the host keeps ONE session's sub-agent transcripts:
// <projectDir>/<sessionID>/subagents.
//
// Note the shape, which is the thing worth getting right: the directory is a sibling of
// <sessionID>.jsonl named for the same session, not a child of it. A path derived one level off
// finds nothing, measures nothing, and reports a step that fanned out to five sub-agents as one that
// delegated nothing — silently, forever.
//
// The project directory comes from sessionTranscriptPath, which is the ONE place that decides
// between the host's persisted answer and the slug derivation (#678 K1). Deriving the slug a second
// time here would leave 37.2% of a session's tokens — the share that lives in this tree — still
// depending on the undocumented convention the persisted marker exists to stop depending on, and the
// two derivations would be free to disagree.
func sessionSubagentDir(workDir, sessionID string) string {
	transcript := sessionTranscriptPath(workDir, sessionID)
	if transcript == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(transcript), sessionID, "subagents")
}

// subagentSpend sums what every sub-agent transcript under dir generated inside [start, end).
//
// The quantity is SPEND — input plus output — and not Occupancy. Occupancy is how full ONE window
// was, and windows do not add: summing five sub-agents' occupancies produces a number that describes
// no window that ever existed. Spend is the house's headline definition (statusline/tokens.go), it
// adds across agents, and reusing it keeps this figure comparable with every other token figure the
// factory reports.
//
// Dedup is PER FILE. Each sub-agent has its own message ids drawn from its own conversation, and two
// sub-agents can legitimately carry the same id; a tree-wide map would silently drop one of them.
// Within a file the rule is the main derivation's, for the reason its doc gives: one record per
// content block with the whole message's usage stamped on every one, reduced with MAX per field.
//
// The false return is not zero. An absent tree — a step that delegated nothing — and a tree the host
// has expired read identically from here, and neither is "the sub-agents spent nothing". The caller
// omits the field rather than writing a number it cannot stand behind.
// subagentTree is what one walk of the tree found. total keeps subagent_tokens' existing meaning and
// value exactly — the summed Spend() — and in/out say what it was made of (#678 K1). nestedLaunches
// is the count of delegations the sub-agents themselves made, which is what turns C-10's depth
// question from a directory test into a number.
type subagentTree struct {
	total          int64
	in             int64
	out            int64
	nestedLaunches int64
}

func subagentSpend(dir, startTS, endTS string) (subagentTree, bool) {
	if dir == "" {
		return subagentTree{}, false
	}
	start, startErr := time.Parse(time.RFC3339, startTS)
	end, endErr := time.Parse(time.RFC3339, endTS)
	if startErr != nil || endErr != nil || end.Before(start) {
		return subagentTree{}, false
	}
	paths := subagentTranscriptPaths(dir)
	if len(paths) == 0 {
		return subagentTree{}, false
	}

	var tree subagentTree
	measured := false
	for _, path := range paths {
		one, ok := transcriptSpendWindow(path, start, end)
		if !ok {
			continue
		}
		measured = true
		tree.total += one.total
		tree.in += one.in
		tree.out += one.out
		tree.nestedLaunches += one.nestedLaunches
	}
	if !measured {
		return subagentTree{}, false
	}
	return tree, true
}

// subagentTranscriptPaths collects every sub-agent transcript under dir, at any depth.
//
// The walk is RECURSIVE and the flat glob it replaces was the whole of six-sigma C-10 (#678 K1). The
// host puts a Workflow tool's children in subagents/workflows/wf_<id>/ rather than beside the direct
// ones, so the flat glob measured a session with 4 direct children and 26 workflow children as a
// session with 4 — a 6.5x under-count that read as "this step barely delegated".
//
// agent-*.jsonl rather than *: the host writes .meta.json sidecars into these directories, and a
// wider glob would feed them to a decoder that reads any JSON object carrying the transcript's field
// names. A sidecar that happens to describe the same generation would then be counted a second time,
// which is why the guard is the pattern and not the decoder's tolerance. Recursion makes the pattern
// MORE load-bearing, not less: the workflow directories also contain a journal.jsonl, which the old
// glob never had to exclude because it never saw one.
//
// Per-entry errors are skipped rather than fatal, the memory_export.go idiom: this tree belongs to
// the host, a sub-agent may still be writing into it, and a figure that omits one file is worth more
// than no figure. An absent root is a legitimate empty — the step delegated nothing.
func subagentTranscriptPaths(dir string) []string {
	var paths []string
	// The return is dropped because the callback never produces one: every error it is handed is
	// answered with nil or fs.SkipDir, and WalkDir reports both as success.
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is skipped whole; a file that cannot be statted is
			// simply not collected. Neither aborts the walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "agent-") && strings.HasSuffix(name, ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	return paths
}

// transcriptSpendWindow reduces one file. An unreadable file is skipped rather than fatal: the tree
// belongs to the host, a sub-agent can still be writing into it while this runs, and a step's figure
// that omits one of five sub-agents is worth more than no figure at all.
func transcriptSpendWindow(path string, start, end time.Time) (subagentTree, bool) {
	f, err := os.Open(path)
	if err != nil {
		return subagentTree{}, false
	}
	defer f.Close()

	perMessage := map[string]statusline.MessageUsage{}
	seen := false
	var nested int64
	br := statusline.NewTranscriptReader(f)
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
		if err != nil || ts.Before(start) || !ts.Before(end) {
			continue
		}
		// Counted before the usage guard, for the reason the parent pass counts there: a delegation
		// this sub-agent made is a fact about the tree whether or not the host also priced the
		// message. Within a MEASURED file, precisely — a file whose window held no priced record at
		// all is dropped whole below, nested launches with it, because a tree that cost this step
		// nothing in this window is not this step's tree to report.
		//
		// This is the SIBLING scan — the launches counted here were made BY sub-agents, and are
		// therefore nested, whereas the identical predicate applied to the parent transcript counts
		// the step's own launches.
		for _, b := range rec.Message.Content {
			if b.Type == "tool_use" && isSubagentTool(b.Name) {
				nested++
			}
		}
		if rec.Message.ID == "" || rec.Message.Usage == nil {
			continue
		}
		seen = true

		u := perMessage[rec.Message.ID]
		u.Absorb(statusline.MessageUsage{
			InputTokens:         rec.Message.Usage.InputTokens,
			OutputTokens:        rec.Message.Usage.OutputTokens,
			CacheReadTokens:     rec.Message.Usage.CacheReadTokens,
			CacheCreationTokens: rec.Message.Usage.CacheCreationTokens,
		})
		perMessage[rec.Message.ID] = u
	}
	if !seen {
		return subagentTree{}, false
	}

	var tree subagentTree
	tree.nestedLaunches = nested
	for _, u := range perMessage {
		// Spend() and not its parts re-added: this figure has shipped since #668 K18 and the split
		// below is an explanation of it, never a redefinition. If the two ever disagreed it would be
		// Spend() that is right, because it is the one every other token figure in the factory
		// already agrees with.
		tree.total += u.Spend()
		tree.in += u.InputTokens
		tree.out += u.OutputTokens
	}
	return tree, true
}
