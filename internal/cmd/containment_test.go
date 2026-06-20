package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// recordedBead captures one call to the sendContainmentMail seam so behavioral
// tests can assert exactly-one-bead / dedup / least-disclosure without touching a
// real mail router (CMP-8 / AC-11). The real detect→resolve→dedup path runs; only
// the mail egress is faked, mirroring the newIssueStore / runGitDetect seams.
type recordedBead struct {
	wd      string
	role    string
	subject string
	body    string
}

// installContainmentRecorder swaps the sendContainmentMail seam for a recorder
// (save → reassign → t.Cleanup-restore, the done_test.go:540-542 idiom). Returns a
// pointer to the slice the seam appends to.
func installContainmentRecorder(t *testing.T) *[]recordedBead {
	t.Helper()
	var recs []recordedBead
	orig := sendContainmentMail
	sendContainmentMail = func(wd, role, subject, body string) error {
		recs = append(recs, recordedBead{wd, role, subject, body})
		return nil
	}
	t.Cleanup(func() { sendContainmentMail = orig })
	return &recs
}

// installFailingContainmentMail swaps the seam for one that records the attempt
// then returns an error, to exercise the AC-10 observable-failure path.
func installFailingContainmentMail(t *testing.T) *[]recordedBead {
	t.Helper()
	var recs []recordedBead
	orig := sendContainmentMail
	sendContainmentMail = func(wd, role, subject, body string) error {
		recs = append(recs, recordedBead{wd, role, subject, body})
		return errors.New("forced send failure")
	}
	t.Cleanup(func() { sendContainmentMail = orig })
	return &recs
}

// setupWorktreeContainmentEnv builds the worktree layout, writes the on-disk
// worktree_id, and exports the session env the boundary resolver trusts. Returns
// (worktreeRoot, agentDir).
func setupWorktreeContainmentEnv(t *testing.T, agentName string) (string, string) {
	t.Helper()
	_, wtAgentDir := setupWorktreeFixture(t, agentName)
	// wtAgentDir = <wtRoot>/.agentfactory/agents/<name>; worktree root is 3 up.
	worktreeRoot := filepath.Dir(filepath.Dir(filepath.Dir(wtAgentDir)))
	wtID := filepath.Base(worktreeRoot)
	writeRuntimeFile(t, wtAgentDir, "worktree_id", wtID+"\n")
	t.Setenv("AF_WORKTREE", worktreeRoot)
	t.Setenv("AF_WORKTREE_ID", wtID)
	t.Setenv("AF_ROLE", agentName)
	t.Setenv("AF_ACTOR", agentName)
	return worktreeRoot, wtAgentDir
}

// setupFactoryContainmentEnv builds the non-worktree layout and clears AF_WORKTREE
// so the resolver falls back to config.FindLocalRoot(cwd) (= factory root, D-5).
func setupFactoryContainmentEnv(t *testing.T, agentName string) (string, string) {
	t.Helper()
	factoryRoot, agentDir := setupFactoryFixture(t, agentName)
	t.Setenv("AF_WORKTREE", "") // empty == unset for the resolver
	t.Setenv("AF_WORKTREE_ID", "")
	t.Setenv("AF_ROLE", agentName)
	t.Setenv("AF_ACTOR", agentName)
	return factoryRoot, agentDir
}

func bashPayload(command, cwd string) containmentPayload {
	return containmentPayload{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{"command":` + mustJSON(command) + `}`),
		Cwd:       cwd,
	}
}

func writePayload(tool, filePath, cwd string) containmentPayload {
	return containmentPayload{
		ToolName:  tool,
		ToolInput: json.RawMessage(`{"file_path":` + mustJSON(filePath) + `}`),
		Cwd:       cwd,
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeAdditionalContext(t *testing.T, stdout string) string {
	t.Helper()
	if strings.TrimSpace(stdout) == "" {
		return ""
	}
	var out struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout is not valid hook JSON: %v\n%s", err, stdout)
	}
	return out.HookSpecificOutput.AdditionalContext
}

// --- AC-4 (drift → exactly one bead) + additionalContext + exit 0 ---

func TestContainment_EffectiveTargetOut_OneBead(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	recs := installContainmentRecorder(t)

	var stdout bytes.Buffer
	p := bashPayload("cd /parent && touch x", worktreeRoot)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("runContainmentCheckCore returned non-nil error (must always exit 0): %v", err)
	}

	if len(*recs) != 1 {
		t.Fatalf("expected exactly 1 corrective bead, got %d", len(*recs))
	}
	b := (*recs)[0]
	if b.subject != "WORKTREE_CONTAINMENT" {
		t.Errorf("subject = %q, want WORKTREE_CONTAINMENT", b.subject)
	}
	if b.role != "solver" {
		t.Errorf("self-addressed role = %q, want solver", b.role)
	}
	if !strings.Contains(b.body, "/parent") {
		t.Errorf("body should name the offending path /parent; got: %s", b.body)
	}
	if !strings.Contains(b.body, worktreeRoot) {
		t.Errorf("body should name the agent's OWN boundary %q; got: %s", worktreeRoot, b.body)
	}

	ac := decodeAdditionalContext(t, stdout.String())
	if ac == "" {
		t.Error("expected hookSpecificOutput.additionalContext on stdout, got none")
	}
	if !strings.Contains(ac, "/parent") {
		t.Errorf("additionalContext should reference the offending path; got: %s", ac)
	}
}

// --- in-bounds → silent ---

func TestContainment_InBounds_Silent(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	recs := installContainmentRecorder(t)

	var stdout bytes.Buffer
	// Write inside the worktree boundary.
	p := writePayload("Write", filepath.Join(worktreeRoot, "notes.txt"), worktreeRoot)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("must exit 0: %v", err)
	}
	if len(*recs) != 0 {
		t.Fatalf("in-bounds must be silent, got %d beads", len(*recs))
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("in-bounds must emit no additionalContext; got: %s", stdout.String())
	}
}

// --- AC-9 repeat same escape → still exactly one bead ---

func TestContainment_RepeatEscape_OneBead(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	recs := installContainmentRecorder(t)

	p := bashPayload("cd /parent && touch x", worktreeRoot)
	for i := 0; i < 3; i++ {
		var stdout bytes.Buffer
		if err := runContainmentCheckCore(&stdout, p); err != nil {
			t.Fatalf("iteration %d: must exit 0: %v", i, err)
		}
	}
	if len(*recs) != 1 {
		t.Fatalf("repeated identical escape must dedup to one bead (AC-9), got %d", len(*recs))
	}
}

// --- Thread 1 (PR #396): a compound `cd <in-bounds> && cd <parent>` — the escaping
// SECOND cd must be detected (exactly one corrective), and clearDedup must NOT run, so a
// pre-existing escape marker survives. Under the first-match-wins bug, parseBashTarget
// returns the in-bounds first cd, the core judges in-bounds → zero correctives AND
// clearDedup wipes containment_seen. Both assertions fail until the scan-all fix lands. ---

func TestContainment_CompoundCdSecondEscapes_OneBead_MarkerSurvives(t *testing.T) {
	worktreeRoot, agentDir := setupWorktreeContainmentEnv(t, "solver")
	parent := filepath.Dir(worktreeRoot) // out of bounds (shared parent)
	recs := installContainmentRecorder(t)

	// Pre-seed a dedup marker for a DIFFERENT prior escape so we can prove this
	// in-bounds-looking compound command does NOT wipe containment_seen via clearDedup.
	markerDir := filepath.Join(agentDir, ".runtime", "containment_seen")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	priorMarker := filepath.Join(markerDir, "prior-escape-marker")
	if err := os.WriteFile(priorMarker, []byte("seen\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	// First cd is in-bounds (the worktree root); the effective/escaping dir is the SECOND cd.
	p := bashPayload("cd "+worktreeRoot+" && cd "+parent, worktreeRoot)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("must exit 0 (ADR-007): %v", err)
	}

	// Harm 1 — the escaping second cd must be detected: exactly one corrective. Use
	// non-fatal Errorf (and guard the index) so Harm 2 is still checked independently
	// even if the corrective count regresses.
	if len(*recs) != 1 {
		t.Errorf("compound `cd <in> && cd <parent>`: want exactly 1 corrective for the "+
			"escaping second cd, got %d", len(*recs))
	} else {
		if !strings.Contains((*recs)[0].body, parent) {
			t.Errorf("corrective body must name the escaping path %q; got: %s", parent, (*recs)[0].body)
		}
		if ac := decodeAdditionalContext(t, stdout.String()); ac == "" {
			t.Error("expected same-loop additionalContext nudge for the escape; got none")
		}
	}

	// Harm 2 — clearDedup must NOT have run: the pre-seeded marker must survive.
	// Checked independently of Harm 1 so a clearDedup-only regression is still caught.
	if _, err := os.Stat(priorMarker); err != nil {
		t.Errorf("clearDedup must NOT run when a compound command's later cd escapes — "+
			"pre-existing escape marker was wiped: %v", err)
	}
}

// --- Thread 1 (PR #396): escape-then-return — the FIRST cd escapes and the LAST cd
// returns in-bounds. "First out-of-bounds" must still flag the escaping first segment;
// a "last decidable target" rule would wrongly clear it. This pins the chosen policy. ---

func TestContainment_CompoundEscapeThenReturn_OneBead(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	parent := filepath.Dir(worktreeRoot) // out of bounds (shared parent)
	recs := installContainmentRecorder(t)

	var stdout bytes.Buffer
	// First cd escapes to the parent; the LAST cd returns in-bounds.
	p := bashPayload("cd "+parent+" && cd "+worktreeRoot, worktreeRoot)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("must exit 0 (ADR-007): %v", err)
	}
	if len(*recs) != 1 {
		t.Fatalf("escape-then-return: the escaping FIRST cd must be flagged (first-out-of-bounds, "+
			"not last-decidable); want 1 corrective, got %d", len(*recs))
	}
	if !strings.Contains((*recs)[0].body, parent) {
		t.Errorf("corrective body must name the escaping parent %q; got: %s", parent, (*recs)[0].body)
	}
}

// --- AC-10 forced send failure → observable artifact, never swallowed ---

func TestContainment_ForcedSendFailure_Artifact(t *testing.T) {
	worktreeRoot, agentDir := setupWorktreeContainmentEnv(t, "solver")
	recs := installFailingContainmentMail(t)

	var stdout bytes.Buffer
	p := bashPayload("cd /parent && touch x", worktreeRoot)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("must still exit 0 even when the send fails (ADR-007): %v", err)
	}
	if len(*recs) != 1 {
		t.Fatalf("the send must be attempted once; got %d", len(*recs))
	}

	logPath := filepath.Join(agentDir, ".runtime", "containment_debug.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected observable failure artifact at %s (AC-10): %v", logPath, err)
	}
	if !strings.Contains(string(data), "forced send failure") {
		t.Errorf("containment_debug.log should record the send error; got: %s", data)
	}
}

// --- AC-5 both layouts resolve the boundary correctly ---

func TestContainment_BothLayouts(t *testing.T) {
	t.Run("worktree", func(t *testing.T) {
		worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
		recs := installContainmentRecorder(t)
		var stdout bytes.Buffer
		p := bashPayload("cd /parent && touch x", worktreeRoot)
		if err := runContainmentCheckCore(&stdout, p); err != nil {
			t.Fatalf("must exit 0: %v", err)
		}
		if len(*recs) != 1 {
			t.Fatalf("worktree layout: want 1 bead, got %d", len(*recs))
		}
		if !strings.Contains((*recs)[0].body, worktreeRoot) {
			t.Errorf("worktree boundary should be AF_WORKTREE %q; body: %s", worktreeRoot, (*recs)[0].body)
		}
	})

	t.Run("non-worktree", func(t *testing.T) {
		factoryRoot, agentDir := setupFactoryContainmentEnv(t, "manager")
		recs := installContainmentRecorder(t)
		var stdout bytes.Buffer
		// cwd is the agent dir; resolver falls back to FindLocalRoot → factoryRoot.
		p := bashPayload("cd /parent && touch x", agentDir)
		if err := runContainmentCheckCore(&stdout, p); err != nil {
			t.Fatalf("must exit 0: %v", err)
		}
		if len(*recs) != 1 {
			t.Fatalf("non-worktree layout: want 1 bead, got %d", len(*recs))
		}
		if !strings.Contains((*recs)[0].body, factoryRoot) {
			t.Errorf("non-worktree boundary should be the factory root %q; body: %s", factoryRoot, (*recs)[0].body)
		}
	})
}

// --- AC-4 the two EXPECTED boundary-by-effect cases raise no violation ---

func TestContainment_ExpectedCases_NoViolation(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")

	cases := []struct {
		name    string
		payload containmentPayload
	}{
		{"git-push-no-cd", bashPayload("git push origin main", worktreeRoot)},
		{"home-cache-write-no-cd", bashPayload("mkdir -p $HOME/.cache/af-test", worktreeRoot)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs := installContainmentRecorder(t)
			var stdout bytes.Buffer
			if err := runContainmentCheckCore(&stdout, tc.payload); err != nil {
				t.Fatalf("must exit 0: %v", err)
			}
			if len(*recs) != 0 {
				t.Fatalf("%s is EXPECTED-by-scope; want 0 beads, got %d", tc.name, len(*recs))
			}
		})
	}
}

// --- ADR-007 internal error still exits 0 ---

func TestContainment_InternalError_ExitsZero(t *testing.T) {
	worktreeRoot, _ := setupWorktreeContainmentEnv(t, "solver")
	recs := installContainmentRecorder(t)

	cases := []struct {
		name    string
		payload containmentPayload
	}{
		{"empty-payload", containmentPayload{}},
		{"malformed-tool-input", containmentPayload{ToolName: "Bash", ToolInput: json.RawMessage(`{bad`), Cwd: worktreeRoot}},
		{"unknown-tool", containmentPayload{ToolName: "WebFetch", ToolInput: json.RawMessage(`{}`), Cwd: worktreeRoot}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			if err := runContainmentCheckCore(&stdout, tc.payload); err != nil {
				t.Fatalf("internal error must still return nil (exit 0): %v", err)
			}
		})
	}
	_ = recs
}

// --- AC-7 / G3 env-spoof value-equality invariant is pinned ---
//
// The H3 env-spoof cross-check only has teeth if AF_WORKTREE_ID (env, exported at
// session.go:293 from m.worktreeID) equals the on-disk worktree_id
// (filepath.Base(worktreePath), worktree.go:600). If a future change makes them
// diverge, the only env-spoof mitigation silently no-ops. This test fails loudly
// if that load-bearing equality is ever broken.
func TestContainment_EnvSpoofValueEquality_G3(t *testing.T) {
	worktreeRoot, agentDir := setupWorktreeContainmentEnv(t, "solver")

	if got, want := os.Getenv("AF_WORKTREE_ID"), filepath.Base(worktreeRoot); got != want {
		t.Fatalf("AF_WORKTREE_ID (%q) must equal filepath.Base(worktreePath) (%q)", got, want)
	}

	onDisk, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "worktree_id"))
	if err != nil {
		t.Fatalf("reading on-disk worktree_id: %v", err)
	}
	if got, want := strings.TrimSpace(string(onDisk)), filepath.Base(worktreeRoot); got != want {
		t.Fatalf("on-disk worktree_id (%q) must equal filepath.Base(worktreePath) (%q)", got, want)
	}
}

// --- H3 cross-check actually fires on a spoofed AF_WORKTREE_ID ---

func TestContainment_EnvSpoofMismatch_FailObservable(t *testing.T) {
	worktreeRoot, agentDir := setupWorktreeContainmentEnv(t, "solver")
	// Spoof: env claims a different worktree id than the one on disk.
	t.Setenv("AF_WORKTREE_ID", "wt-IMPOSTER")
	installContainmentRecorder(t)

	var stdout bytes.Buffer
	p := bashPayload("cd /parent && touch x", worktreeRoot)
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("must exit 0: %v", err)
	}

	logPath := filepath.Join(agentDir, ".runtime", "containment_debug.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("env-spoof mismatch must be recorded to containment_debug.log: %v", err)
	}
	if !strings.Contains(string(data), "AF_WORKTREE_ID") {
		t.Errorf("debug log should record the env-spoof mismatch; got: %s", data)
	}
}

// --- effective-target extraction unit coverage (C1) ---

func TestContainment_ParseEffectiveTarget(t *testing.T) {
	cwd := "/work/wt"
	cases := []struct {
		name      string
		toolName  string
		toolInput string
		want      []string
	}{
		{"bash-cd-absolute", "Bash", `{"command":"cd /parent && touch x"}`, []string{"/parent"}},
		{"bash-pushd-absolute", "Bash", `{"command":"pushd /tmp/elsewhere"}`, []string{"/tmp/elsewhere"}},
		{"bash-git-C", "Bash", `{"command":"git -C /other/repo status"}`, []string{"/other/repo"}},
		{"bash-no-cd", "Bash", `{"command":"git push origin main"}`, nil},
		{"bash-cd-command-subst-undecidable", "Bash", `{"command":"cd $(pwd)/.."}`, nil},
		// Thread 1: a compound command exposes BOTH cd targets in order — the in-bounds
		// first and the escaping second — so the core can flag the first out-of-bounds one.
		{"bash-compound-cd-second-escapes", "Bash", `{"command":"cd /work/wt && cd /parent"}`, []string{"/work/wt", "/parent"}},
		{"write-abs", "Write", `{"file_path":"/parent/foo.txt"}`, []string{"/parent/foo.txt"}},
		{"edit-abs", "Edit", `{"file_path":"/parent/bar.go"}`, []string{"/parent/bar.go"}},
		{"unknown-tool", "WebFetch", `{}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseEffectiveTarget(tc.toolName, json.RawMessage(tc.toolInput), cwd)
			if !slices.Equal(got, tc.want) {
				t.Errorf("parseEffectiveTarget(%s,%s) = %v, want %v", tc.toolName, tc.toolInput, got, tc.want)
			}
		})
	}
}
