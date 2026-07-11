package genjob

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// deadPID spawns a trivial child, reaps it, and returns its now-dead PID (mirrors
// rendezvous_test.deadPID). syscall.Kill(pid,0) on a reaped PID returns ESRCH, so processAlive is
// false — the hermetic "stale owner" fixture. /bin/true is a system-PATH binary (noexec-/tmp safe).
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := osexec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn helper process for deadPID: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

func lookOrSkip(t *testing.T, bin string) {
	t.Helper()
	if _, err := osexec.LookPath(bin); err != nil {
		t.Skipf("%s not on PATH; skipping (the seam contract is carried by the fake-only cases)", bin)
	}
}

// waitForPhase polls Status until the job reaches want (or a terminal phase), bounded by a deadline.
func waitForPhase(t *testing.T, j *Job, want Phase, timeout time.Duration) State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := j.Status()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.Phase == want {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, _ := j.Status()
	t.Fatalf("timed out waiting for phase %q; last phase %q (possible pipe stall?)", want, st.Phase)
	return State{}
}

func killPID(pid int) {
	if pid > 0 {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// stopAndWait kills a long-lived child and blocks until the job's finalize goroutine has settled
// (running cleared, terminal marker published) so t.TempDir cleanup never races a late marker write.
func stopAndWait(t *testing.T, j *Job, pid int) {
	t.Helper()
	killPID(pid)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := j.Status()
		if !st.Running && !processAlive(pid) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 1. Detached context: cancelling the console/request context does NOT kill the detached child.
func TestGenJob_DetachedContext_ChildSurvivesConsoleClose(t *testing.T) {
	lookOrSkip(t, "sleep")
	root := t.TempDir()
	j := New(root, WithSpawn(func(Phase) *osexec.Cmd { return osexec.Command("sleep", "30") }))

	ctx, cancel := context.WithCancel(context.Background())
	if err := j.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st, _ := j.Status()
	pid := st.PID
	t.Cleanup(func() { stopAndWait(t, j, pid) })
	if pid <= 0 {
		t.Fatalf("expected a live child pid, got %d", pid)
	}
	// The child must be its OWN process-group leader (Setpgid), so a signal directed at the console's
	// group cannot reap the regeneration — pgid == pid proves the group was created.
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid {
		t.Fatalf("detached child must lead its own process group (pgid==pid); got pgid=%d err=%v", pgid, err)
	}

	cancel() // simulate the console tab / request context dying
	time.Sleep(150 * time.Millisecond)

	if !processAlive(pid) {
		t.Fatalf("child pid %d was killed by context cancellation — the child must be DETACHED (H-3)", pid)
	}
	st2, _ := j.Status()
	if !st2.Running {
		t.Fatalf("marker must still report running after console close; got phase %q running=%v", st2.Phase, st2.Running)
	}
}

// 2. Install-phase failure aborts BEFORE the up phase (no half-bootstrap, install.go:695-699).
func TestGenJob_InstallFailAbortsBeforeUp(t *testing.T) {
	lookOrSkip(t, "false")
	root := t.TempDir()

	var mu sync.Mutex
	var phases []Phase
	spawn := func(p Phase) *osexec.Cmd {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
		if p == PhaseInstall {
			return osexec.Command("false") // exit 1
		}
		return osexec.Command("true") // the up phase that must NEVER be reached
	}
	j := New(root, WithSpawn(spawn))
	if err := j.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := waitForPhase(t, j, PhaseFailed, 5*time.Second)
	if st.ExitCode == 0 {
		t.Fatalf("failed run must carry a non-zero exit code, got %d", st.ExitCode)
	}
	mu.Lock()
	got := append([]Phase(nil), phases...)
	mu.Unlock()
	if len(got) != 1 || got[0] != PhaseInstall {
		t.Fatalf("up phase must NOT be spawned after install failure; spawn phases = %v", got)
	}
}

// 3. A second Start while one is running is refused (busy / 409-able) — no second child.
func TestGenJob_SecondStartWhileRunning_Refused(t *testing.T) {
	lookOrSkip(t, "sleep")
	root := t.TempDir()

	var mu sync.Mutex
	count := 0
	spawn := func(Phase) *osexec.Cmd {
		mu.Lock()
		count++
		mu.Unlock()
		return osexec.Command("sleep", "30")
	}
	j := New(root, WithSpawn(spawn))
	if err := j.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	st, _ := j.Status()
	t.Cleanup(func() { stopAndWait(t, j, st.PID) })

	err := j.Start(context.Background())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start must return ErrBusy, got %v", err)
	}
	mu.Lock()
	n := count
	mu.Unlock()
	if n != 1 {
		t.Fatalf("a refused Start must NOT spawn a second child; spawn count = %d", n)
	}
}

// 4. Guard-5/Guard-6 substrings become badges; a bare af-up `warning:` does NOT (no child spawned).
func TestGenJob_ScanBadges_SpecificSubstringsOnly(t *testing.T) {
	guard5 := "warning: running inside a live agent session — agent-gen-all.sh runs `af down --all`, which will SIGKILL all agents including this session"
	guard6 := `warning: shipped formula "web-design" has local edits that agent-gen-all.sh will overwrite; make durable edits in internal/cmd/install_formulas/ and re-sync (ADR-015)`
	afUp := "warning: startup set of 5 agents exceeds max_worktrees=3; raise max_worktrees or reduce the agents list"

	transcript := []byte(guard5 + "\n" + afUp + "\n" + guard6 + "\n")
	badges := scanBadges(transcript)
	if len(badges) != 2 {
		t.Fatalf("want exactly 2 badges (Guard 5 + Guard 6), got %d: %+v", len(badges), badges)
	}
	var has5, has6 bool
	for _, b := range badges {
		switch b.Guard {
		case 5:
			has5 = true
		case 6:
			has6 = true
		}
	}
	if !has5 || !has6 {
		t.Fatalf("expected one Guard 5 and one Guard 6 badge, got %+v", badges)
	}
	// The bare af-up cap warning must never be badged.
	for _, b := range badges {
		if b.Message == afUp {
			t.Fatalf("af up's bare `warning:` line must NOT produce a badge (specific-substring match, not prefix)")
		}
	}
}

// 5. Console-death: the writer keeps appending to the O_APPEND log with NO reader (no pipe stall).
func TestGenJob_ConsoleDeath_WriterKeepsAppending(t *testing.T) {
	lookOrSkip(t, "head")
	if _, err := os.Stat("/dev/zero"); err != nil {
		t.Skip("/dev/zero absent")
	}
	root := t.TempDir()
	const payload = 100000 // > the 64KB kernel pipe buffer: a pipe with no reader would block here
	spawn := func(p Phase) *osexec.Cmd {
		if p == PhaseInstall {
			return osexec.Command("head", "-c", "100000", "/dev/zero")
		}
		return osexec.Command("true")
	}
	j := New(root, WithSpawn(spawn))
	if err := j.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// No Progress reader is attached — if the child wrote to a pipe it would stall past 64KB.
	waitForPhase(t, j, PhaseDone, 5*time.Second)

	fi, err := os.Stat(logPath(root))
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if fi.Size() < payload {
		t.Fatalf("log has %d bytes, want >= %d — writes must not stall without a reader", fi.Size(), payload)
	}
}

// 6. Delta-polled Progress does offset reads over the log file.
func TestGenJob_Progress_DeltaReads(t *testing.T) {
	root := t.TempDir()
	j := New(root)
	if err := os.MkdirAll(filepath.Dir(logPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath(root), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	p1, err := j.Progress(0)
	if err != nil {
		t.Fatalf("Progress(0): %v", err)
	}
	if p1.Data != "hello" || p1.Offset != 5 {
		t.Fatalf("first poll = %q off=%d, want hello/5", p1.Data, p1.Offset)
	}
	f, _ := os.OpenFile(logPath(root), os.O_WRONLY|os.O_APPEND, 0o644)
	_, _ = f.WriteString(" world")
	_ = f.Close()

	p2, err := j.Progress(p1.Offset)
	if err != nil {
		t.Fatalf("Progress(%d): %v", p1.Offset, err)
	}
	if p2.Data != " world" {
		t.Fatalf("delta poll = %q, want ' world' (offset read only the appended bytes)", p2.Data)
	}
}

// 7. Re-adoption across a console restart via processAlive.
func TestGenJob_ReAdopt_LivePidRunning_DeadPidStale(t *testing.T) {
	// A: live pid (self) ⇒ Status running, fresh Start refused (adopted).
	rootA := t.TempDir()
	if err := writeState(statePath(rootA), State{PID: os.Getpid(), Phase: PhaseInstall, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	jA := New(rootA, WithSpawn(func(Phase) *osexec.Cmd { return osexec.Command("true") }))
	stA, _ := jA.Status()
	if !stA.Running || stA.Phase != PhaseInstall {
		t.Fatalf("live-pid marker must re-adopt as running/install, got running=%v phase=%q", stA.Running, stA.Phase)
	}
	if err := jA.Start(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("a fresh Start must be refused while an adopted run is alive, got %v", err)
	}

	// B: dead pid ⇒ Status not-running, phase reported aborted/failed (stale marker).
	rootB := t.TempDir()
	if err := writeState(statePath(rootB), State{PID: deadPID(t), Phase: PhaseInstall, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	jB := New(rootB)
	stB, _ := jB.Status()
	if stB.Running {
		t.Fatalf("dead-pid marker must NOT report running")
	}
	if stB.Phase != PhaseFailed {
		t.Fatalf("a stale marker (dead pid) must be reported as an aborted/failed run, got phase %q", stB.Phase)
	}
}

// 8. State marker round-trips via atomic rename.
func TestGenJob_StateMarker_AtomicRoundTrip(t *testing.T) {
	root := t.TempDir()
	want := State{PID: 4321, Phase: PhaseDone, StartedAt: time.Now().Truncate(time.Second), ExitCode: 0}
	if err := writeState(statePath(root), want); err != nil {
		t.Fatalf("writeState: %v", err)
	}
	got, err := readState(statePath(root))
	if err != nil {
		t.Fatalf("readState: %v", err)
	}
	if got.PID != want.PID || got.Phase != want.Phase || got.ExitCode != want.ExitCode {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
	// No temp file left behind (rename is atomic, not a lingering .tmp).
	if _, err := os.Stat(statePath(root) + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("atomic write must not leave a .tmp file behind")
	}
}

// 11. The production (default) spawn emits EXACTLY the fixed argv `af install --agents` / `af up` and
// nothing else — a positive guard on the exec-safety doctrine (IMPLREADME req #3 / AC-10) that the
// module-wide literal-match source-lint cannot provide for the var-based spawn. Hermetic: it only
// CONSTRUCTS the *exec.Cmd (reads .Args), never starts a real `af`.
func TestGenJob_DefaultSpawn_FixedArgv(t *testing.T) {
	j := New(t.TempDir())
	if got, want := j.spawn(PhaseInstall).Args, []string{"af", "install", "--agents"}; !equalArgs(got, want) {
		t.Fatalf("install-phase argv = %v, want %v (fixed, zero caller input)", got, want)
	}
	if got, want := j.spawn(PhaseUp).Args, []string{"af", "up"}; !equalArgs(got, want) {
		t.Fatalf("up-phase argv = %v, want %v", got, want)
	}
	// The underlying fixed slices must not have drifted.
	if !equalArgs(installArgv, []string{"install", "--agents"}) || !equalArgs(upArgv, []string{"up"}) {
		t.Fatalf("fixed argv slices drifted: install=%v up=%v", installArgv, upArgv)
	}
}

func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 9. Stale-binary notice compares installed webui mtime against the (injected) process start.
func TestGenJob_StaleBinaryNotice(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	webui := filepath.Join(home, ".local", "bin", "webui")
	if err := os.MkdirAll(filepath.Dir(webui), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webui, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	// installed mtime (~now) is AFTER a process-start in the past ⇒ stale.
	stale := New(root, WithWebuiPath(webui), WithProcessStart(time.Now().Add(-time.Hour)))
	if cp, _ := stale.Confirm(); !cp.StaleBinary {
		t.Fatalf("installed binary newer than process start must flag StaleBinary")
	}
	// installed mtime is BEFORE a process-start in the future ⇒ not stale.
	fresh := New(root, WithWebuiPath(webui), WithProcessStart(time.Now().Add(time.Hour)))
	if cp, _ := fresh.Confirm(); cp.StaleBinary {
		t.Fatalf("installed binary older than process start must NOT flag StaleBinary")
	}
	// absent binary ⇒ not stale.
	absent := New(root, WithWebuiPath(filepath.Join(home, "nope")), WithProcessStart(time.Now().Add(-time.Hour)))
	if cp, _ := absent.Confirm(); cp.StaleBinary {
		t.Fatalf("absent binary must NOT flag StaleBinary")
	}
}

// 10. Confirm-payload: dispatched sweep (existence-only) + af-up preview from startup.json.
func TestGenJob_ConfirmPayload_DispatchedSweepAndUpPreview(t *testing.T) {
	root := t.TempDir()
	plantDispatched := func(name string) {
		d := filepath.Join(root, ".agentfactory", "agents", name, ".runtime", "dispatched")
		if err := os.MkdirAll(filepath.Dir(d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(d, []byte("dispatcher-identity"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plantDispatched("alpha")
	plantDispatched("gamma")
	// beta has an agent dir but NO dispatched marker.
	if err := os.MkdirAll(filepath.Join(root, ".agentfactory", "agents", "beta", ".runtime"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeStartup := func(body string) {
		p := filepath.Join(root, ".agentfactory", "startup.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// explicit subset
	writeStartup(`{"agents":["alpha","beta"]}`)
	j := New(root)
	cp, err := j.Confirm()
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(cp.Dispatched) != 2 || cp.Dispatched[0] != "alpha" || cp.Dispatched[1] != "gamma" {
		t.Fatalf("dispatched sweep = %v, want [alpha gamma] (existence-only glob, never beta)", cp.Dispatched)
	}
	if cp.UpStartsAll {
		t.Fatalf("explicit agents list must not report UpStartsAll")
	}
	if len(cp.UpPreview) != 2 || cp.UpPreview[0] != "alpha" || cp.UpPreview[1] != "beta" {
		t.Fatalf("up preview = %v, want [alpha beta] (startup.json subset)", cp.UpPreview)
	}

	// explicit empty list ⇒ 0 agents, not ALL
	writeStartup(`{"agents":[]}`)
	cpEmpty, _ := New(root).Confirm()
	if cpEmpty.UpStartsAll || len(cpEmpty.UpPreview) != 0 {
		t.Fatalf("explicit [] must resolve to 0 agents (not ALL), got startsAll=%v preview=%v", cpEmpty.UpStartsAll, cpEmpty.UpPreview)
	}

	// absent startup.json ⇒ ALL (nil-Agents sentinel)
	_ = os.Remove(filepath.Join(root, ".agentfactory", "startup.json"))
	cpAll, _ := New(root).Confirm()
	if !cpAll.UpStartsAll {
		t.Fatalf("absent startup.json must resolve to ALL (nil-Agents sentinel)")
	}
}
