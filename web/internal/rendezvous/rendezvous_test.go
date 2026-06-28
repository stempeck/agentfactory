package rendezvous

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// deadPID spawns a trivial child, reaps it, and returns its now-dead PID. syscall.Kill(pid,0) on a
// reaped PID returns ESRCH, so processAlive(pid) is false — a hermetic "stale owner" fixture.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn helper process for deadPID: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

// healthyServer starts an httptest server answering /healthz with 200 and returns its host:port.
func healthyServer(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func TestLock_AcquireRelease_RoundTrip(t *testing.T) {
	root := t.TempDir()
	lk := NewLock(LockPath(root))
	if err := lk.Acquire("webui-test"); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	info, err := lk.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("lock PID = %d, want %d", info.PID, os.Getpid())
	}
	if info.SessionID != "webui-test" {
		t.Errorf("lock SessionID = %q, want webui-test", info.SessionID)
	}
	if err := lk.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(LockPath(root)); !os.IsNotExist(err) {
		t.Errorf("lock file should be gone after Release, stat err = %v", err)
	}
}

func TestLock_SecondAcquire_ErrLocked(t *testing.T) {
	root := t.TempDir()
	a := NewLock(LockPath(root))
	if err := a.Acquire("a"); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer a.Release()
	b := NewLock(LockPath(root))
	if err := b.Acquire("b"); err != ErrLocked {
		t.Errorf("second Acquire err = %v, want ErrLocked (PID %d is alive)", err, os.Getpid())
	}
}

func TestLock_StalePID_Reclaimed(t *testing.T) {
	root := t.TempDir()
	// Hand-write a lock file owned by a dead PID.
	stale := LockInfo{PID: deadPID(t), AcquiredAt: time.Now().UTC(), SessionID: "ghost"}
	if err := os.MkdirAll(filepath.Dir(LockPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(stale, "", "  ")
	if err := os.WriteFile(LockPath(root), data, 0o644); err != nil {
		t.Fatal(err)
	}
	lk := NewLock(LockPath(root))
	if err := lk.Acquire("reclaimer"); err != nil {
		t.Fatalf("Acquire over stale lock should succeed, got %v", err)
	}
	info, err := lk.Read()
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("after reclaim lock PID = %d, want %d", info.PID, os.Getpid())
	}
}

func TestEndpoint_WriteRead_RoundTrip(t *testing.T) {
	root := t.TempDir()
	in := EndpointInfo{Transport: "tcp", Address: "127.0.0.1:54321", PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := WriteEndpoint(root, in); err != nil {
		t.Fatalf("WriteEndpoint: %v", err)
	}
	out, err := readEndpoint(ServerFile(root))
	if err != nil {
		t.Fatalf("readEndpoint: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch: got %+v want %+v", out, in)
	}
}

func TestEnsure_LiveServer_NoOp(t *testing.T) {
	root := t.TempDir()
	addr := healthyServer(t)
	// Publish an endpoint file for a live (this-process) PID + healthy server.
	if err := WriteEndpoint(root, EndpointInfo{Transport: "tcp", Address: addr, PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	bindCalled := 0
	url, owned, err := Ensure(root, "webui-test", func() (string, error) {
		bindCalled++
		return "127.0.0.1:0", nil
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if owned {
		t.Errorf("owned = true, want false (a live healthy server already runs)")
	}
	if bindCalled != 0 {
		t.Errorf("bind called %d times, want 0 (must not start a duplicate)", bindCalled)
	}
	if url != "http://"+addr {
		t.Errorf("url = %q, want %q", url, "http://"+addr)
	}
}

func TestEnsure_StaleEndpoint_BindsFresh(t *testing.T) {
	root := t.TempDir()
	// A stale endpoint: dead PID, address that nothing listens on.
	if err := WriteEndpoint(root, EndpointInfo{Transport: "tcp", Address: "127.0.0.1:1", PID: deadPID(t), StartedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	bindCalled := 0
	_, owned, err := Ensure(root, "webui-test", func() (string, error) {
		bindCalled++
		return "127.0.0.1:45678", nil
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !owned {
		t.Errorf("owned = false, want true (stale endpoint must be reclaimed and a fresh server bound)")
	}
	if bindCalled != 1 {
		t.Errorf("bind called %d times, want 1", bindCalled)
	}
	got, err := readEndpoint(ServerFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != os.Getpid() || got.Address != "127.0.0.1:45678" {
		t.Errorf("fresh endpoint = %+v, want PID %d addr 127.0.0.1:45678", got, os.Getpid())
	}
}

func TestEnsure_LosesLock_PollsForPeer(t *testing.T) {
	root := t.TempDir()
	// Shorten the poll window so the test is fast.
	defer swapTunables(50*time.Millisecond, 5*time.Millisecond)()

	// A live peer holds the start-lock but has not yet published its endpoint.
	peer := NewLock(LockPath(root))
	if err := peer.Acquire("peer"); err != nil {
		t.Fatalf("peer Acquire: %v", err)
	}
	defer peer.Release()

	// After a short delay the peer publishes a live endpoint.
	addr := healthyServer(t)
	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = WriteEndpoint(root, EndpointInfo{Transport: "tcp", Address: addr, PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339)})
	}()

	bindCalled := 0
	url, owned, err := Ensure(root, "loser", func() (string, error) {
		bindCalled++
		return "127.0.0.1:0", nil
	})
	if err != nil {
		t.Fatalf("Ensure (loser) should find the peer's endpoint, got %v", err)
	}
	if owned {
		t.Errorf("owned = true, want false (lost the start-lock, must reuse the peer's server)")
	}
	if bindCalled != 0 {
		t.Errorf("bind called %d times, want 0 (loser must not start a duplicate)", bindCalled)
	}
	if url != "http://"+addr {
		t.Errorf("url = %q, want %q", url, "http://"+addr)
	}
}

// swapTunables temporarily shortens the poll timeouts and returns a restore func.
func swapTunables(wait, every time.Duration) func() {
	ow, oe := StartWaitTimeout, StartPollEvery
	StartWaitTimeout, StartPollEvery = wait, every
	return func() { StartWaitTimeout, StartPollEvery = ow, oe }
}
