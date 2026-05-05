package lock

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquire_Fresh(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	if err := l.Acquire("test-session-1"); err != nil {
		t.Fatalf("Acquire on fresh dir failed: %v", err)
	}

	lockPath := filepath.Join(dir, ".runtime", "agent.lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatal("lock file was not created")
	}
}

func TestAcquire_StaleDetection(t *testing.T) {
	dir := t.TempDir()

	// Write a lock file with a dead PID
	runtimeDir := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(runtimeDir, "agent.lock")
	// PID 99999999 should not exist
	lockData := `{"pid":99999999,"acquired_at":"2026-01-01T00:00:00Z","session_id":"old-session"}`
	if err := os.WriteFile(lockPath, []byte(lockData), 0o644); err != nil {
		t.Fatal(err)
	}

	l := New(dir)
	if err := l.Acquire("new-session"); err != nil {
		t.Fatalf("Acquire should succeed after stale lock cleanup: %v", err)
	}

	info, err := l.Read()
	if err != nil {
		t.Fatalf("Read after acquire failed: %v", err)
	}
	if info.SessionID != "new-session" {
		t.Errorf("expected session_id new-session, got %s", info.SessionID)
	}
	if info.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), info.PID)
	}
}

func TestRelease(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	if err := l.Acquire("test-session"); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	lockPath := filepath.Join(dir, ".runtime", "agent.lock")
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("lock file should be removed after release")
	}
}

func TestRead_NoLock(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	_, err := l.Read()
	if err == nil {
		t.Fatal("Read should return error when no lock exists")
	}
}

func TestRead_ValidLock(t *testing.T) {
	dir := t.TempDir()
	l := New(dir)
	if err := l.Acquire("my-session"); err != nil {
		t.Fatal(err)
	}

	info, err := l.Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("expected PID %d, got %d", os.Getpid(), info.PID)
	}
	if info.SessionID != "my-session" {
		t.Errorf("expected session my-session, got %s", info.SessionID)
	}
}

func TestProcessExists_Self(t *testing.T) {
	if !processExists(os.Getpid()) {
		t.Fatal("processExists should return true for current process")
	}
}

func TestProcessExists_Dead(t *testing.T) {
	if processExists(99999999) {
		t.Fatal("processExists should return false for non-existent PID")
	}
}

func TestAcquire_ActiveLock(t *testing.T) {
	dir := t.TempDir()

	// Write a lock file with current PID (simulates another session of same process)
	runtimeDir := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(runtimeDir, "agent.lock")
	lockData := `{"pid":` + fmt.Sprintf("%d", os.Getpid()) + `,"acquired_at":"2026-01-01T00:00:00Z","session_id":"existing-session"}`
	if err := os.WriteFile(lockPath, []byte(lockData), 0o644); err != nil {
		t.Fatal(err)
	}

	l := New(dir)
	err := l.Acquire("new-session")
	if err == nil {
		t.Fatal("Acquire should return ErrLocked when lock is held by active process")
	}
	if err != ErrLocked {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}
