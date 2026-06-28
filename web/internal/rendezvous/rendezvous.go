// Package rendezvous is the web module's self-contained singleton-launch coordinator: a
// file-rendezvous (.runtime/webui_server.json) plus a PID-based start-lock
// (.runtime/webui_start.lock) so the container entrypoint can launch the UI "iff present" and a
// second launch that finds a live, healthy server no-ops instead of binding a duplicate.
//
// This file RE-IMPLEMENTS, by BEHAVIOR, the small af-core ADR-010 slice the web module needs. It
// deliberately does NOT import internal/… — Go's internal seal plus the separate web go.mod make
// that compiler-impossible, and the duplication is the point (compiler-enforced C-2 decoupling;
// mirrors the precedent in web/internal/exec/validate.go).
//
// Sources (copied, not imported):
//   - EndpointInfo / WriteEndpoint / readEndpoint / tryLiveEndpoint / pollForEndpoint / healthCheck:
//     internal/issuestore/mcpstore/lifecycle.go:51-56, 79-142, 263-277
//   - Lock / LockInfo / IsStale / NewLock(=NewWithPath) / Acquire / Release / Read / processAlive
//     (syscall.Kill(pid,0)==nil||EPERM): internal/lock/lock.go:16-26, 45-121
//   - Ensure mirrors discoverOrStart's skeleton (lifecycle.go:79-111): rendezvous file →
//     start-lock → re-check under lock → bind → publish → release.
package rendezvous

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ErrLocked mirrors lock.ErrLocked: the start-lock is held by a live process.
var ErrLocked = errors.New("webui start lock held by active process")

// Tunables are package vars (not consts) so tests can shorten the poll window. Defaults mirror
// mcpstore (lifecycle.go:58-69): 30s tolerates a cold start, process-death detection is the real
// safety net; the per-probe health timeout is short.
var (
	StartWaitTimeout = 30 * time.Second
	StartPollEvery   = 50 * time.Millisecond
	HealthTimeout    = 500 * time.Millisecond
)

// EndpointInfo mirrors mcpstore.endpointInfo (lifecycle.go:51-56) — the payload published to
// .runtime/webui_server.json after the server binds its loopback listener.
type EndpointInfo struct {
	Transport string `json:"transport"`
	Address   string `json:"address"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

// LockInfo mirrors lock.LockInfo (lock.go:16-21): who holds the start-lock.
type LockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
	SessionID  string    `json:"session_id,omitempty"`
	Hostname   string    `json:"hostname,omitempty"`
}

// IsStale reports whether the owning process is dead (lock.go:24-26).
func (l *LockInfo) IsStale() bool { return !processAlive(l.PID) }

// Lock is a file-based PID start-lock at an explicit path (mirrors lock.Lock via NewWithPath,
// lock.go:28-47). Advisory only: Acquire is not atomic (TOCTOU between read and write), exactly as
// the af-core lock documents at lock.go:52 — the re-check-under-lock in Ensure closes the gap.
type Lock struct {
	lockPath string
}

// NewLock mirrors lock.NewWithPath (lock.go:45-47).
func NewLock(path string) *Lock { return &Lock{lockPath: path} }

// Acquire mirrors lock.Acquire (lock.go:53-90): a stale lock (dead PID) is auto-reclaimed; an
// active lock returns ErrLocked; otherwise the caller's identity is written.
func (l *Lock) Acquire(sessionID string) error {
	if existing, err := l.Read(); err == nil {
		if existing.IsStale() {
			_ = os.Remove(l.lockPath)
		} else {
			return ErrLocked
		}
	}
	if err := os.MkdirAll(filepath.Dir(l.lockPath), 0o755); err != nil {
		return fmt.Errorf("rendezvous: create runtime dir: %w", err)
	}
	hostname, _ := os.Hostname()
	info := LockInfo{PID: os.Getpid(), AcquiredAt: time.Now().UTC(), SessionID: sessionID, Hostname: hostname}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("rendezvous: marshal lock info: %w", err)
	}
	if err := os.WriteFile(l.lockPath, data, 0o644); err != nil {
		return fmt.Errorf("rendezvous: write lock file: %w", err)
	}
	return nil
}

// Release removes the lock file (lock.go:93-98).
func (l *Lock) Release() error {
	if err := os.Remove(l.lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rendezvous: remove lock file: %w", err)
	}
	return nil
}

// Read returns the current lock info, or an error if no lock exists (lock.go:101-113).
func (l *Lock) Read() (*LockInfo, error) {
	data, err := os.ReadFile(l.lockPath)
	if err != nil {
		return nil, err
	}
	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("rendezvous: parse lock file: %w", err)
	}
	return &info, nil
}

// ServerFile is the rendezvous endpoint path: <root>/.runtime/webui_server.json.
func ServerFile(root string) string { return filepath.Join(root, ".runtime", "webui_server.json") }

// LockPath is the start-lock path: <root>/.runtime/webui_start.lock.
func LockPath(root string) string { return filepath.Join(root, ".runtime", "webui_start.lock") }

// WriteEndpoint publishes the endpoint file atomically (temp + rename), creating .runtime/ as
// needed.
func WriteEndpoint(root string, info EndpointInfo) error {
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("rendezvous: create runtime dir: %w", err)
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("rendezvous: marshal endpoint: %w", err)
	}
	tmp := ServerFile(root) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("rendezvous: write endpoint tmp: %w", err)
	}
	if err := os.Rename(tmp, ServerFile(root)); err != nil {
		return fmt.Errorf("rendezvous: publish endpoint: %w", err)
	}
	return nil
}

// readEndpoint reads and validates .runtime/webui_server.json (mcpstore readEndpoint,
// lifecycle.go:244-258).
func readEndpoint(path string) (EndpointInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return EndpointInfo{}, err
	}
	var info EndpointInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return EndpointInfo{}, fmt.Errorf("rendezvous: parse endpoint file: %w", err)
	}
	if info.Address == "" || info.PID == 0 {
		return EndpointInfo{}, fmt.Errorf("rendezvous: endpoint file %s missing address or pid", path)
	}
	return info, nil
}

// Ensure coordinates an idempotent singleton launch (mirrors mcpstore.discoverOrStart,
// lifecycle.go:79-111). It returns owned=true only when THIS call bound a fresh server and
// published the endpoint; owned=false means a live server already exists (or a peer won the
// start-lock) and the caller should no-op. bind must bind the listener and return its host:port.
func Ensure(root, sessionID string, bind func() (addr string, err error)) (url string, owned bool, err error) {
	epFile := ServerFile(root)

	// Already healthy → reuse, no-op.
	if u, ok := tryLiveEndpoint(epFile); ok {
		return u, false, nil
	}

	lk := NewLock(LockPath(root))
	acqErr := lk.Acquire(sessionID)
	if errors.Is(acqErr, ErrLocked) {
		// A peer is starting; poll for the endpoint it will publish.
		u, perr := pollForEndpoint(epFile)
		return u, false, perr
	}
	if acqErr != nil {
		return "", false, fmt.Errorf("rendezvous: acquire start lock: %w", acqErr)
	}
	// The endpoint file is the durable rendezvous; the lock only serializes the brief startup
	// window, so release as soon as Ensure returns (mirrors discoverOrStart's deferred Release).
	defer lk.Release()

	// Re-check under the lock: a peer may have published a healthy server between our first probe
	// and our lock acquisition (closes the advisory-lock TOCTOU gap).
	if u, ok := tryLiveEndpoint(epFile); ok {
		return u, false, nil
	}

	addr, berr := bind()
	if berr != nil {
		return "", false, berr
	}
	info := EndpointInfo{Transport: "tcp", Address: addr, PID: os.Getpid(), StartedAt: time.Now().UTC().Format(time.RFC3339)}
	if werr := WriteEndpoint(root, info); werr != nil {
		return "", false, werr
	}
	return "http://" + addr, true, nil
}

// tryLiveEndpoint reports whether the endpoint file names a live, healthy server (mcpstore
// tryLiveEndpoint, lifecycle.go:113-127): endpoint present + PID alive + health probe OK.
func tryLiveEndpoint(epFile string) (string, bool) {
	info, err := readEndpoint(epFile)
	if err != nil {
		return "", false
	}
	if !processAlive(info.PID) {
		return "", false
	}
	if !healthCheck(info.Address) {
		return "", false
	}
	return "http://" + info.Address, true
}

// pollForEndpoint blocks until a live endpoint is published or the timeout elapses (mcpstore
// pollForEndpoint, lifecycle.go:129-142). Used when another launcher holds the start-lock.
func pollForEndpoint(epFile string) (string, error) {
	deadline := time.Now().Add(StartWaitTimeout)
	for {
		if u, ok := tryLiveEndpoint(epFile); ok {
			return u, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("rendezvous: timed out waiting for peer to publish endpoint file %s", epFile)
		}
		time.Sleep(StartPollEvery)
	}
}

// processAlive mirrors lock.processExists / mcpstore.isAlive (lock.go:117-121, lifecycle.go:263-266):
// syscall.Kill(pid,0) succeeds (or EPERM — the process exists but we may not signal it).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// healthCheck GETs <address>/healthz with a short timeout and returns true on 200 (mcpstore
// healthCheck, lifecycle.go:268-277; the web server's liveness route is /healthz).
func healthCheck(address string) bool {
	client := &http.Client{Timeout: HealthTimeout}
	resp, err := client.Get("http://" + address + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
