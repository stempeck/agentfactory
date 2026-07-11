// Package genjob is the web module's server-owned, detached, file-backed singleton runner for
// "Generate All Agents" — the console button that regenerates and reinstalls every formula-derived
// agent via `af install --agents` and then brings the factory up with `af up` (#502 Phase 2).
//
// The regeneration takes a long time, runs `af down --all` mid-flight (SIGKILLing every agent —
// possibly the console's own session), and reinstalls the `af`/`webui` binaries. So it must run
// DETACHED (its lifetime decoupled from any request/console context) and its transcript must survive
// the console tab/process dying. This package therefore does NOT funnel through the exec module's
// ExecRunner: `run` buffers+blocks and `RunStream` drains a pipe in-process — a full kernel pipe
// buffer blocks the detached child once the reader dies (cross-review H-3). Instead it spawns its own
// `os/exec.Cmd` (plain exec.Command, NOT CommandContext) whose stdout AND stderr point at ONE
// `O_APPEND` `*os.File`; a dead console cannot stall a file write, and the console reconnects later by
// delta-reading that file.
//
// Like web/internal/rendezvous, this file RE-IMPLEMENTS by BEHAVIOR the small af-core slices it needs
// (atomic-rename marker; syscall.Kill(pid,0) liveness) — Go's internal seal plus the separate web
// go.mod make importing internal/… compiler-impossible, and the duplication is the point.
//
// Sources mirrored (copied, not imported):
//   - detached spawn (plain exec.Command, fds→a log file, Close-after-Start): internal/issuestore/mcpstore/lifecycle.go:164-199
//   - atomic-rename marker (temp + os.Rename): web/internal/rendezvous/rendezvous.go:125-144
//   - processAlive (syscall.Kill(pid,0)==nil||EPERM): web/internal/rendezvous/rendezvous.go:239-242
//   - fixed argv + factory-lock singleton posture: web/internal/exec/runner.go:195,234-241,268-277
//   - Guard 5/6 badge strings + abort-before-up gate: internal/cmd/install.go:676,738,695-699
//   - startup.json af-up resolution (nil⇒ALL, []⇒0): internal/config/startup.go:32-49 + internal/cmd/up.go:103-118
package genjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Phase is the Generate-All phase model. The job mirrors install.go's install→up order: it runs the
// install phase first and only advances to the up phase on install success (no half-bootstrap).
type Phase string

const (
	PhaseIdle    Phase = ""        // no job has run (no marker)
	PhaseInstall Phase = "install" // `af install --agents` running (agent-gen + rebuild/reinstall)
	PhaseUp      Phase = "up"      // `af up` running (start the regenerated agents)
	PhaseDone    Phase = "done"    // both phases exited 0
	PhaseFailed  Phase = "failed"  // a phase exited non-zero (or a stale marker's process is gone)
)

func isActivePhase(p Phase) bool { return p == PhaseInstall || p == PhaseUp }

// ErrBusy is returned by Start when a Generate-All is already running — either this process's own
// child or an adopted still-alive run from a prior console incarnation. Phase 3 maps it to HTTP 409.
// The @factory lock is the conceptual singleton arbiter (exec/runner.go:195); this is its in-process
// reflection, not a second competing gate.
var ErrBusy = errors.New("genjob: generate-all already running")

// genjob is the sanctioned SECOND direct-af path (design Phase 2 JOB / H-3): it must self-spawn a
// DETACHED child writing an O_APPEND file, which ExecRunner/RunStream cannot host. The argv is FIXED
// and carries zero caller input (design AC-10), so the exec-safety doctrine is preserved; job_test.go
// pins both slices against drift. The mutating-exec source-lint sees this spawn and exempts this file
// by path (server/lint_test.go, isExemptFromMutateLint) — the exemption is where the sanction is
// recorded, not the shape of these declarations.
var (
	afBinary    = "af"
	installArgv = []string{"install", "--agents"}
	upArgv      = []string{"up"}
)

// Badge is one extracted Guard-5 / Guard-6 warning surfaced to the operator (never a bare `warning:`).
type Badge struct {
	Guard   int    `json:"guard"`   // 5 or 6
	Message string `json:"message"` // the full transcript line that matched
}

// State is the on-disk run marker (generate-agents.json), written via atomic rename.
type State struct {
	PID       int       `json:"pid"`                // current child PID (for processAlive re-adoption)
	Phase     Phase     `json:"phase"`              //
	StartedAt time.Time `json:"started_at"`         //
	EndedAt   time.Time `json:"ended_at,omitempty"` // set once the run terminates
	ExitCode  int       `json:"exit_code"`          // valid once Phase == done|failed
	Badges    []Badge   `json:"badges,omitempty"`   // Guard 5/6 warnings scanned from the log
	Running   bool      `json:"running"`            // recomputed against processAlive on every read
}

// Progress is a delta-poll response: the log bytes appended since `from`, plus the current State.
type Progress struct {
	Offset int64  `json:"offset"` // the new EOF offset (the caller's next `from`)
	Data   string `json:"data"`   // log bytes in [from, offset)
	State  State  `json:"state"`  // current job state alongside the tail
}

// ConfirmPayload is the hold-to-confirm preview the console shows before starting a regeneration.
type ConfirmPayload struct {
	Dispatched  []string `json:"dispatched"`    // agents with a .runtime/dispatched marker (will be killed)
	UpPreview   []string `json:"up_preview"`    // agents `af up` would start (startup.json resolution)
	UpStartsAll bool     `json:"up_starts_all"` // nil Agents ⇒ ALL (the nil-vs-[] sentinel)
	StaleBinary bool     `json:"stale_binary"`  // installed webui mtime is newer than this process's start
	Running     bool     `json:"running"`       // a job is already in flight (a Start would be refused)
}

// spawnFunc is the ADR-018 injectable seam. The production default returns a plain (detached)
// exec.Command for the given phase; tests inject a fake over a harmless PATH binary so no real `af`
// ever runs.
type spawnFunc func(Phase) *exec.Cmd

// Job is the server-owned, detached, file-backed singleton Generate-All runner. Construct with New.
type Job struct {
	root         string
	webuiPath    string    // installed console binary checked for staleness (default ~/.local/bin/webui)
	processStart time.Time // this console process's start (stale-binary baseline)
	spawn        spawnFunc

	mu      sync.Mutex // guards `running`; the in-process singleton reflection
	running bool
}

// Option configures a Job (mirrors web/internal/server's WithX functional-options idiom).
type Option func(*Job)

// WithSpawn injects the spawn seam (ADR-018). Default: a plain exec.Command per phase.
func WithSpawn(fn func(Phase) *exec.Cmd) Option { return func(j *Job) { j.spawn = fn } }

// WithWebuiPath overrides the installed-binary path used for the stale-binary check. Tests point this
// at a temp file so the check never depends on a real ~/.local/bin/webui.
func WithWebuiPath(path string) Option { return func(j *Job) { j.webuiPath = path } }

// WithProcessStart injects the process-start instant used for the stale-binary comparison (default:
// captured at New()). An injectable seam per the dependency review (no /proc/self/stat dependency).
func WithProcessStart(t time.Time) Option { return func(j *Job) { j.processStart = t } }

// New builds a Job rooted at the factory root. It captures this process's start time for the
// stale-binary comparison and does NOT spawn anything (Start does).
func New(root string, opts ...Option) *Job {
	home, _ := os.UserHomeDir()
	j := &Job{
		root:         root,
		webuiPath:    filepath.Join(home, ".local", "bin", "webui"),
		processStart: time.Now(),
	}
	// Default (production) spawn: plain exec.Command — NOT exec.CommandContext — so the regeneration's
	// lifetime is DETACHED from any request/console context (mirrors mcpstore/lifecycle.go:170). cmd.Dir
	// is pinned to the factory root like ExecRunner (runner.go:170-173).
	j.spawn = func(p Phase) *exec.Cmd {
		var cmd *exec.Cmd
		if p == PhaseUp {
			cmd = exec.Command(afBinary, upArgv...)
		} else {
			cmd = exec.Command(afBinary, installArgv...)
		}
		cmd.Dir = root
		return cmd
	}
	for _, o := range opts {
		o(j)
	}
	return j
}

func jobsDir(root string) string   { return filepath.Join(root, ".runtime", "webui_jobs") }
func logPath(root string) string   { return filepath.Join(jobsDir(root), "generate-agents.log") }
func statePath(root string) string { return filepath.Join(jobsDir(root), "generate-agents.json") }

// Start spawns the detached install-phase child writing to the O_APPEND log, records the initial state
// marker, and returns immediately (the child is awaited in a package goroutine that then runs the up
// phase). It is the singleton gate: if a job is already running — this process's own child OR an
// adopted still-alive run from a prior console — it returns ErrBusy WITHOUT spawning.
//
// ctx gates the synchronous setup only; it is deliberately NOT propagated to the child (the child is
// detached), which is exactly why cancelling ctx cannot kill an in-flight regeneration.
func (j *Job) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.isRunningLocked() {
		return ErrBusy
	}
	if err := os.MkdirAll(jobsDir(j.root), 0o755); err != nil {
		return fmt.Errorf("genjob: create jobs dir: %w", err)
	}
	// Fresh transcript per run (re-adoption never calls Start, so it never removes the log).
	_ = os.Remove(logPath(j.root))
	logFile, err := os.OpenFile(logPath(j.root), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("genjob: open log: %w", err)
	}

	started := time.Now()
	cmd := j.spawn(PhaseInstall)
	// Stdout AND Stderr point at the SAME *os.File: os/exec passes one inherited fd (no pipe, no
	// drain goroutine), so a dead console cannot stall the child's writes (H-3). Guard 5/6 badges are
	// extracted later by scanning the log FILE, not by intercepting a live stderr pipe.
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("genjob: start install phase: %w", err)
	}
	j.running = true
	j.writeMarker(State{PID: cmd.Process.Pid, Phase: PhaseInstall, StartedAt: started})
	go j.run(logFile, cmd, started)
	return nil
}

// run awaits the install child, and only on a zero exit advances to the up phase (mirrors
// install.go:695-699 — abort before up on install failure, no half-bootstrap). It runs in a package
// goroutine (never the request goroutine), closing the log fd when the pipeline terminates.
func (j *Job) run(logFile *os.File, installCmd *exec.Cmd, started time.Time) {
	defer logFile.Close()

	if err := installCmd.Wait(); err != nil {
		j.finish(PhaseFailed, installCmd.Process.Pid, exitCodeOf(err), started)
		return // abort before up
	}

	upCmd := j.spawn(PhaseUp)
	upCmd.Stdout = logFile
	upCmd.Stderr = logFile
	detachProcessGroup(upCmd)
	if err := upCmd.Start(); err != nil {
		j.finish(PhaseFailed, installCmd.Process.Pid, -1, started)
		return
	}
	j.writeMarker(State{PID: upCmd.Process.Pid, Phase: PhaseUp, StartedAt: started})
	if err := upCmd.Wait(); err != nil {
		j.finish(PhaseFailed, upCmd.Process.Pid, exitCodeOf(err), started)
		return
	}
	j.finish(PhaseDone, upCmd.Process.Pid, 0, started)
}

// finish writes the terminal marker (with scanned badges), THEN clears the in-process running flag.
// The order matters: a reader that observes running=false must already see the terminal marker, so
// Status can never catch the window between "child died" and "terminal state published" and
// misreport a still-active marker's stale PID as an exit-0 abort.
func (j *Job) finish(p Phase, pid, exit int, started time.Time) {
	j.writeMarker(State{
		PID:       pid,
		Phase:     p,
		StartedAt: started,
		EndedAt:   time.Now(),
		ExitCode:  exit,
		Badges:    j.currentBadges(),
	})
	j.mu.Lock()
	j.running = false
	j.mu.Unlock()
}

// Status returns the current marker-backed State, recomputing Running. While THIS process owns an
// in-flight run (j.running), the marker is authoritative-running — its finalize goroutine will publish
// the terminal state imminently, so a transiently-dead child PID must NOT be reinterpreted here. Only a
// FOREIGN marker (not owned) whose active-phase process is gone is reported as an aborted run (the
// re-adoption path: a prior console incarnation crashed mid-run).
func (j *Job) Status() (State, error) {
	j.mu.Lock()
	owned := j.running
	j.mu.Unlock()

	st, err := readState(statePath(j.root))
	if err != nil {
		return State{Phase: PhaseIdle}, nil // no marker ⇒ idle (not an error)
	}
	if isActivePhase(st.Phase) {
		if owned || processAlive(st.PID) {
			st.Running = true
		} else {
			st.Running = false
			st.Phase = PhaseFailed // stale FOREIGN marker: the run's process is gone ⇒ aborted
		}
	} else {
		st.Running = false
	}
	st.Badges = j.currentBadges()
	return st, nil
}

// Progress returns the log bytes appended since `from` plus the current State (delta poll).
func (j *Job) Progress(from int64) (Progress, error) {
	st, _ := j.Status()
	data, offset, err := readLogFrom(logPath(j.root), from)
	if err != nil {
		return Progress{State: st}, err
	}
	return Progress{Offset: offset, Data: string(data), State: st}, nil
}

// Confirm assembles the hold-to-confirm payload: the dispatched-marker sweep, the af-up preview from
// startup.json, and the stale-binary notice.
func (j *Job) Confirm() (ConfirmPayload, error) {
	preview, startsAll := j.upPreview()
	j.mu.Lock()
	running := j.isRunningLocked()
	j.mu.Unlock()
	return ConfirmPayload{
		Dispatched:  j.dispatchedSweep(),
		UpPreview:   preview,
		UpStartsAll: startsAll,
		StaleBinary: j.staleBinary(),
		Running:     running,
	}, nil
}

// isRunningLocked reports whether a Generate-All is in flight: this process's own child, or an adopted
// still-alive run from a prior console incarnation (marker in an active phase with a live PID). Must be
// called with j.mu held.
func (j *Job) isRunningLocked() bool {
	if j.running {
		return true
	}
	st, err := readState(statePath(j.root))
	if err != nil {
		return false
	}
	return isActivePhase(st.Phase) && processAlive(st.PID)
}

// scanBadges extracts Guard-5 / Guard-6 warning badges from the child transcript. It matches the
// SPECIFIC Guard substrings, never the bare `warning:` prefix — the up phase runs `af up`, whose own
// `warning:` lines (up.go:124-126, et al.) land in the same log and must NOT be mis-badged.
func scanBadges(transcript []byte) []Badge {
	var badges []Badge
	for _, raw := range strings.Split(string(transcript), "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.Contains(line, "live agent session"):
			badges = append(badges, Badge{Guard: 5, Message: line})
		case strings.Contains(line, "has local edits that agent-gen-all"):
			badges = append(badges, Badge{Guard: 6, Message: line})
		}
	}
	return badges
}

func (j *Job) currentBadges() []Badge {
	data, err := os.ReadFile(logPath(j.root))
	if err != nil {
		return nil
	}
	return scanBadges(data)
}

// dispatchedSweep returns the names of agents currently carrying a .runtime/dispatched marker. This
// multi-agent glob is net-new (exec.isDispatched, runner.go:234-241, checks ONE path); it applies the
// same existence-only idiom per agent — the marker's CONTENT is never read, only its existence.
func (j *Job) dispatchedSweep() []string {
	pattern := filepath.Join(j.root, ".agentfactory", "agents", "*", ".runtime", "dispatched")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	var names []string
	for _, m := range matches {
		if fi, statErr := os.Stat(m); statErr != nil || fi.IsDir() {
			continue
		}
		// <root>/.agentfactory/agents/<name>/.runtime/dispatched → <name>
		names = append(names, filepath.Base(filepath.Dir(filepath.Dir(m))))
	}
	sort.Strings(names)
	return names
}

// startupFile is the web-module re-declaration (by behavior) of the startup.json slice af-up reads;
// only the nil-vs-[] Agents sentinel matters for the preview.
type startupFile struct {
	Agents []string `json:"agents"`
}

// upPreview resolves what `af up` would start from startup.json, mirroring up.go:103-118 /
// startup.go:32-49: an absent/unparseable file or a nil Agents list ⇒ ALL (enumerate the roster); an
// explicit list (including the empty list ⇒ 0 agents) ⇒ that list verbatim.
func (j *Job) upPreview() (agents []string, startsAll bool) {
	data, err := os.ReadFile(filepath.Join(j.root, ".agentfactory", "startup.json"))
	if err != nil {
		return j.roster(), true // absent ⇒ defaults (nil Agents ⇒ ALL)
	}
	var cfg startupFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return j.roster(), true // unparseable ⇒ best-effort ALL preview
	}
	if cfg.Agents == nil {
		return j.roster(), true // nil ⇒ ALL
	}
	sort.Strings(cfg.Agents)
	return cfg.Agents, false // explicit list (incl. [] ⇒ 0 agents)
}

// roster enumerates the configured agents (the dirs under .agentfactory/agents/*) — the blanket set
// `af up` starts when startup.json is absent or has a nil Agents list.
func (j *Job) roster() []string {
	matches, err := filepath.Glob(filepath.Join(j.root, ".agentfactory", "agents", "*"))
	if err != nil {
		return nil
	}
	var names []string
	for _, m := range matches {
		if fi, statErr := os.Stat(m); statErr == nil && fi.IsDir() {
			names = append(names, filepath.Base(m))
		}
	}
	sort.Strings(names)
	return names
}

// staleBinary reports whether the installed console binary was reinstalled AFTER this process started
// (a successful regen rebuilds/reinstalls webui, so the running console may be stale). A missing binary
// is not stale.
func (j *Job) staleBinary() bool {
	fi, err := os.Stat(j.webuiPath)
	if err != nil {
		return false
	}
	return fi.ModTime().After(j.processStart)
}

// writeMarker publishes the state marker atomically (temp + os.Rename), mirroring
// rendezvous.WriteEndpoint (rendezvous.go:125-144). Best-effort: a marker write failure never blocks
// the regeneration (the child owns the durable log; the marker is recoverable state).
func (j *Job) writeMarker(s State) {
	_ = writeState(statePath(j.root), s)
}

func writeState(path string, s State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("genjob: create jobs dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("genjob: marshal state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("genjob: write state tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("genjob: publish state: %w", err)
	}
	return nil
}

func readState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("genjob: parse state file: %w", err)
	}
	return s, nil
}

// readLogFrom returns the log bytes in [from, EOF) and the new EOF offset. An absent log is an empty
// tail at offset 0 (a poll before the first run is not an error).
func readLogFrom(path string, from int64) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := fi.Size()
	if from < 0 || from > size {
		from = size // a truncated/rotated log resets the caller to EOF rather than erroring
	}
	if _, err := f.Seek(from, 0); err != nil {
		return nil, 0, err
	}
	buf := make([]byte, size-from)
	// io.ReadFull (not a single Read) so a legal short read never silently drops the tail while still
	// advancing the offset. A concurrent truncation/rotation yields ErrUnexpectedEOF/EOF — return what
	// was read and advance the offset by exactly that many bytes, so the next poll resumes correctly.
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, 0, err
	}
	return buf[:n], from + int64(n), nil
}

// detachProcessGroup puts the child in its OWN process group (Setpgid) so a signal directed at the
// console's process group — SIGHUP when the controlling terminal/tab closes, or a SIGINT/SIGTERM to the
// foreground group — cannot reap the regeneration. This is the process-group half of "survive the
// console dying" (the O_APPEND file is the pipe-stall half); it mirrors mcpstore's setProcGroup
// (lifecycle_unix.go:16), applied here before Start. SysProcAttr.Setpgid is unix-only; the web module
// is linux-only in practice (rendezvous.go and this file already call syscall.Kill unconditionally), so
// no build-tag split is needed.
func detachProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// processAlive mirrors rendezvous.processAlive (rendezvous.go:239-242): syscall.Kill(pid,0) succeeds
// (or EPERM — the process exists but we may not signal it).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func exitCodeOf(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
