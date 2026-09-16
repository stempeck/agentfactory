package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// activityLine renders one record whose single content block is a tool_use of the named tool, with
// usage attached so the window measures at all. transcriptLine cannot express this: its tool_use
// block is a fixed Read of a fixed path, which is exactly right for the dedup fixtures it serves and
// useless for counting what different tools do.
//
// version is the host's own stamp on the record. "" omits the key, which is what a host that does
// not stamp one writes.
func activityLine(ts, msgID, version, tool string, input map[string]string) string {
	encoded, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	versionKey := ""
	if version != "" {
		versionKey = fmt.Sprintf(`"version":%q,`, version)
	}
	return fmt.Sprintf(
		`{"timestamp":%q,%s"type":"assistant","message":{"id":%q,"role":"assistant",`+
			`"content":[{"type":"tool_use","id":"tu","name":%q,"input":%s}],`+
			`"usage":{"input_tokens":10,"output_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		ts, versionKey, msgID, tool, encoded)
}

func deriveActivity(t *testing.T, lines ...string) generationScalars {
	t.Helper()
	got := deriveGenerationScalars(strings.NewReader(strings.Join(lines, "\n")+"\n"),
		"2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z")
	if !got.measured {
		t.Fatalf("the fixture is outside its own window: %+v", got)
	}
	return got
}

func readOf(path string) map[string]string    { return map[string]string{"file_path": path} }
func bashOf(command string) map[string]string { return map[string]string{"command": command} }

// TestSubagentAndWorkflowLaunchesAreCountedApart pins the scope fence C-1 draws (#678 K1).
//
// isSubagentTool is the DISPATCH GATE's predicate: dispatch_admit.go reads it to decide whether a
// launch is admitted. The Workflow tool launches agents too, so the tempting simplification is to
// add it there and let one counter cover both — which would silently change an admission decision to
// make a measurement tidier. The counters are separate instead, and this is the test that says so.
func TestSubagentAndWorkflowLaunchesAreCountedApart(t *testing.T) {
	for _, tool := range []string{"Task", "Agent"} {
		t.Run("the sub-agent launcher named "+tool, func(t *testing.T) {
			got := deriveActivity(t, activityLine("2026-08-30T11:10:00.000Z", "m1", "", tool, nil))
			if got.subagentLaunch != 1 {
				t.Errorf("subagent_launches = %d, want 1", got.subagentLaunch)
			}
			if got.workflowLaunch != 0 {
				t.Errorf("workflow_launches = %d for a %s, want 0", got.workflowLaunch, tool)
			}
		})
	}

	t.Run("the Workflow tool is counted on its own line", func(t *testing.T) {
		got := deriveActivity(t,
			activityLine("2026-08-30T11:10:00.000Z", "m1", "", "Workflow", nil),
			activityLine("2026-08-30T11:10:01.000Z", "m2", "", "Workflow", nil),
			activityLine("2026-08-30T11:10:02.000Z", "m3", "", "Task", nil),
		)
		if got.workflowLaunch != 2 {
			t.Errorf("workflow_launches = %d, want 2", got.workflowLaunch)
		}
		if got.subagentLaunch != 1 {
			t.Errorf("subagent_launches = %d, want 1 — a Workflow is not a Task and must not be "+
				"folded into the gate's predicate to tidy the count", got.subagentLaunch)
		}
	})
}

// TestRepeatReadsCountRepetitionNotActivity pins what makes repeat_reads a waste indicator rather
// than a busyness one: it fires on the SECOND read of a path and on nothing else.
func TestRepeatReadsCountRepetitionNotActivity(t *testing.T) {
	cases := []struct {
		name   string
		inputs []map[string]string
		tools  []string
		want   int64
	}{
		{
			name:   "reading two different files is not a repeat",
			tools:  []string{"Read", "Read"},
			inputs: []map[string]string{readOf("/a"), readOf("/b")},
		},
		{
			name:   "reading one file twice is one repeat",
			tools:  []string{"Read", "Read"},
			inputs: []map[string]string{readOf("/a"), readOf("/a")},
			want:   1,
		},
		{
			name:   "reading one file three times is two repeats",
			tools:  []string{"Read", "Read", "Read"},
			inputs: []map[string]string{readOf("/a"), readOf("/a"), readOf("/a")},
			want:   2,
		},
		{
			name:   "a shell that cats a file already read is a repeat",
			tools:  []string{"Read", "Bash"},
			inputs: []map[string]string{readOf("/a"), bashOf("cat /a")},
			want:   1,
		},
		{
			name:   "sed -n reads whole files too",
			tools:  []string{"Read", "Bash"},
			inputs: []map[string]string{readOf("/a"), bashOf("sed -n 1,20p /a")},
			want:   1,
		},
		{
			// A re-read indicator that fired on greps would measure activity, which is the thing it
			// exists to be independent of.
			name:   "a shell that greps a file already read is not a repeat",
			tools:  []string{"Read", "Bash"},
			inputs: []map[string]string{readOf("/a"), bashOf("grep -n x /a")},
		},
		{
			// `cat a b` is a concatenation. Treating its first word as a re-read would make the
			// indicator fire on a different operation entirely.
			name:   "concatenating two files is not a read of either",
			tools:  []string{"Read", "Bash"},
			inputs: []map[string]string{readOf("/a"), bashOf("cat /a /b")},
		},
		{
			name:   "a tool_use with no path to read says nothing",
			tools:  []string{"Read", "Read"},
			inputs: []map[string]string{readOf(""), readOf("")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := make([]string, 0, len(tc.tools))
			for i, tool := range tc.tools {
				lines = append(lines, activityLine(
					fmt.Sprintf("2026-08-30T11:1%d:00.000Z", i), fmt.Sprintf("m%d", i), "", tool, tc.inputs[i]))
			}
			if got := deriveActivity(t, lines...).repeatReads; got != tc.want {
				t.Errorf("repeat_reads = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestHostVersionIsTheHostThatMeasuredTheStep pins whose version this is. The field is stamped by
// the host on its own records, so it says what MEASURED the step — not what af thinks it is running
// under, and not what the launcher installed. A session that spans a host upgrade carries two, and
// the one that measured the end of the step is the one a reader comparing this step to the next
// needs.
func TestHostVersionIsTheHostThatMeasuredTheStep(t *testing.T) {
	t.Run("the last version in the window wins", func(t *testing.T) {
		got := deriveActivity(t,
			activityLine("2026-08-30T11:10:00.000Z", "m1", "2.1.224", "Read", readOf("/a")),
			activityLine("2026-08-30T11:11:00.000Z", "m2", "2.1.258", "Read", readOf("/b")),
		)
		if got.hostVersion != "2.1.258" {
			t.Errorf("host_version = %q, want %q", got.hostVersion, "2.1.258")
		}
	})

	t.Run("a record that stamps no version does not erase one", func(t *testing.T) {
		got := deriveActivity(t,
			activityLine("2026-08-30T11:10:00.000Z", "m1", "2.1.224", "Read", readOf("/a")),
			activityLine("2026-08-30T11:11:00.000Z", "m2", "", "Read", readOf("/b")),
		)
		if got.hostVersion != "2.1.224" {
			t.Errorf("host_version = %q, want %q — an unstamped record is silence, not a new answer",
				got.hostVersion, "2.1.224")
		}
	})

	t.Run("a host that stamps nothing leaves the field absent", func(t *testing.T) {
		got := deriveActivity(t, activityLine("2026-08-30T11:10:00.000Z", "m1", "", "Read", readOf("/a")))
		if got.hostVersion != "" {
			t.Errorf("host_version = %q from a host that stamped none", got.hostVersion)
		}
	})
}

// TestExactThinkTokensAbsorbSilence is the other half of D9, and the half a fixture is most likely
// to miss. The host omits output_tokens_details from the streaming partials of a message whose
// settled record carries it, so within ONE message both shapes appear. The reduction is MAX per
// field, which means the record that said nothing contributes 0 and the one that spoke wins — in
// either order. A reduction that took the LAST value, or that treated absence as an explicit zero,
// would report a thinking message as having done none.
func TestExactThinkTokensAbsorbSilence(t *testing.T) {
	const start, end = "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"

	orders := map[string][]string{
		"the partial arrives first": {
			transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "text", "hi", 1000, 500, 0, 0),
			transcriptLine("2026-08-30T11:10:01.000Z", "msg_A", "tool_use", "", 1000, 500, 0, 0, 300),
		},
		"the settled record arrives first": {
			transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "text", "hi", 1000, 500, 0, 0, 300),
			transcriptLine("2026-08-30T11:10:01.000Z", "msg_A", "tool_use", "", 1000, 500, 0, 0),
		},
	}
	for name, lines := range orders {
		t.Run(name, func(t *testing.T) {
			got := deriveGenerationScalars(strings.NewReader(strings.Join(lines, "\n")+"\n"), start, end)
			if got.think == nil {
				t.Fatal("think_tokens is absent, but one record in this message reported it")
			}
			if *got.think != 300 {
				t.Errorf("think_tokens = %d, want 300 — the record that said nothing about thinking "+
					"contributes 0 to the maximum, it does not overwrite the one that did", *got.think)
			}
		})
	}
}

// TestSessionTranscriptPathPrefersWhatTheHostSaid pins the preference af prime's hook exists to
// establish (#678 K1). The derived path is a dependency on an undocumented host convention; the
// persisted one is the host's own answer. Preferring it is only safe because the marker is keyed to
// a session and checked for existence — otherwise a marker left behind by an earlier session, naming
// a file that is perfectly real, would beat a derivation that was right.
func TestSessionTranscriptPathPrefersWhatTheHostSaid(t *testing.T) {
	writeMarker := func(t *testing.T, workDir, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(workDir, ".runtime"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workDir, ".runtime", "transcript_path"), []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	realFile := func(t *testing.T, name string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("a marker for this session naming a real file is used verbatim", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		hostPath := realFile(t, "somewhere-af-would-never-derive.jsonl")
		writeMarker(t, workDir, "sess-1\t"+hostPath+"\n")

		if got := sessionTranscriptPath(workDir, "sess-1"); got != hostPath {
			t.Errorf("sessionTranscriptPath = %q, want the host's own answer %q", got, hostPath)
		}
	})

	// The MAJOR case. Session N's payload carried a transcript_path; session N+1's did not, so
	// session_id advanced and this marker did not. The file it names is real, which is exactly why an
	// existence check alone cannot catch it: af would measure the new step against the old session's
	// transcript and report figures for work the step did not do.
	t.Run("a marker belonging to another session is not this session's answer", func(t *testing.T) {
		configDir := t.TempDir()
		t.Setenv(claudeConfigDirEnv, configDir)
		workDir := t.TempDir()
		writeMarker(t, workDir, "sess-1\t"+realFile(t, "previous-session.jsonl"))

		want := filepath.Join(configDir, "projects", transcriptDirSlug.Replace(workDir), "sess-2.jsonl")
		if got := sessionTranscriptPath(workDir, "sess-2"); got != want {
			t.Errorf("sessionTranscriptPath = %q, want the derived path %q — a marker left behind by "+
				"session sess-1 named a file that still exists, and preferring it would measure this "+
				"step against another session's transcript", got, want)
		}
	})

	t.Run("a marker naming a file that is gone falls back to the derivation", func(t *testing.T) {
		configDir := t.TempDir()
		t.Setenv(claudeConfigDirEnv, configDir)
		workDir := t.TempDir()
		writeMarker(t, workDir, "sess-1\t"+filepath.Join(t.TempDir(), "expired.jsonl"))

		want := filepath.Join(configDir, "projects", transcriptDirSlug.Replace(workDir), "sess-1.jsonl")
		if got := sessionTranscriptPath(workDir, "sess-1"); got != want {
			t.Errorf("sessionTranscriptPath = %q, want the derived path %q — a stale marker must not "+
				"redirect this step's figures to a transcript that is not this step's", got, want)
		}
	})

	// A marker written by a pre-#678 binary carries a bare path and no session. It is unattributable
	// rather than wrong, so it is declined the same way, and the derivation it falls back to is
	// measured correct.
	t.Run("an unkeyed marker from an older binary is declined", func(t *testing.T) {
		configDir := t.TempDir()
		t.Setenv(claudeConfigDirEnv, configDir)
		workDir := t.TempDir()
		writeMarker(t, workDir, realFile(t, "bare-path.jsonl")+"\n")

		want := filepath.Join(configDir, "projects", transcriptDirSlug.Replace(workDir), "sess-1.jsonl")
		if got := sessionTranscriptPath(workDir, "sess-1"); got != want {
			t.Errorf("sessionTranscriptPath = %q, want the derived path %q", got, want)
		}
	})

	t.Run("an empty marker is not an answer", func(t *testing.T) {
		configDir := t.TempDir()
		t.Setenv(claudeConfigDirEnv, configDir)
		workDir := t.TempDir()
		writeMarker(t, workDir, "  \n")

		want := filepath.Join(configDir, "projects", transcriptDirSlug.Replace(workDir), "sess-1.jsonl")
		if got := sessionTranscriptPath(workDir, "sess-1"); got != want {
			t.Errorf("sessionTranscriptPath = %q, want the derived path %q", got, want)
		}
	})

	// The sub-agent tree is where 37.2% of a session's tokens live, and it used to derive the slug a
	// second time on its own. It now hangs off whatever sessionTranscriptPath decided, so the whole
	// measurement depends on the undocumented convention in one place or in none.
	t.Run("the sub-agent tree follows the same answer", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		hostPath := realFile(t, "sess-1.jsonl")
		writeMarker(t, workDir, "sess-1\t"+hostPath+"\n")

		want := filepath.Join(filepath.Dir(hostPath), "sess-1", "subagents")
		if got := sessionSubagentDir(workDir, "sess-1"); got != want {
			t.Errorf("sessionSubagentDir = %q, want %q — the sub-agent tree is a sibling of the "+
				"transcript the host named, not of a path af derived for itself", got, want)
		}
	})
}
