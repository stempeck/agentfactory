package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var ErrLocked = errors.New("identity lock held by active process")

// LockInfo describes who holds the lock.
type LockInfo struct {
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquired_at"`
	SessionID  string    `json:"session_id,omitempty"`
	Hostname   string    `json:"hostname,omitempty"`
}

// IsStale returns true if the owning process is dead.
func (l *LockInfo) IsStale() bool {
	return !processExists(l.PID)
}

// Lock is a file-based identity lock at <workerDir>/.runtime/agent.lock.
type Lock struct {
	workerDir string
	lockPath  string
}

// New creates a Lock for the given worker directory. The lock file path is
// fixed at <workerDir>/.runtime/agent.lock — the conventional identity-lock
// location for af agent sessions.
func New(workerDir string) *Lock {
	return NewWithPath(filepath.Join(workerDir, ".runtime", "agent.lock"))
}

// NewWithPath creates a Lock at the given absolute path. Use this when the
// caller owns naming the lock file (for example, mcpstore's mcp_start.lock).
// The workerDir field is left as the zero value; it is never read by Acquire,
// Release, or Read.
func NewWithPath(path string) *Lock {
	return &Lock{lockPath: path}
}

// Acquire attempts to acquire the identity lock.
// If a stale lock exists (dead PID), it is automatically cleaned up.
// If an active lock exists, ErrLocked is returned.
// Note: not atomic (TOCTOU between read and write). Advisory lock only.
func (l *Lock) Acquire(sessionID string) error {
	// Check existing lock
	existing, err := l.Read()
	if err == nil {
		// Lock file exists — check if stale
		if existing.IsStale() {
			// Stale lock, remove it
			os.Remove(l.lockPath)
		} else {
			return ErrLocked
		}
	}

	// Create .runtime directory if needed
	runtimeDir := filepath.Dir(l.lockPath)
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("creating runtime dir: %w", err)
	}

	hostname, _ := os.Hostname()
	info := LockInfo{
		PID:        os.Getpid(),
		AcquiredAt: time.Now().UTC(),
		SessionID:  sessionID,
		Hostname:   hostname,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling lock info: %w", err)
	}

	if err := os.WriteFile(l.lockPath, data, 0o644); err != nil {
		return fmt.Errorf("writing lock file: %w", err)
	}

	return nil
}

// Release removes the lock file.
func (l *Lock) Release() error {
	if err := os.Remove(l.lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing lock file: %w", err)
	}
	return nil
}

// Read returns the current lock info, or an error if no lock exists.
func (l *Lock) Read() (*LockInfo, error) {
	data, err := os.ReadFile(l.lockPath)
	if err != nil {
		return nil, fmt.Errorf("reading lock file: %w", err)
	}

	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parsing lock file: %w", err)
	}

	return &info, nil
}

// processExists checks if a process with the given PID is alive.
// Returns true if the process exists (even if owned by another user).
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	// EPERM means the process exists but we lack permission to signal it
	return err == nil || err == syscall.EPERM
}
