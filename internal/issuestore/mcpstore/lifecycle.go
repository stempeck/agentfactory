package mcpstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/stempeck/agentfactory/internal/config"

	"github.com/stempeck/agentfactory/internal/lock"
)

var sourceRoot string
var envSourceRoot string

func SetSourceRoot(root string) {
	sourceRoot = root
}

func SetEnvSourceRoot(root string) {
	envSourceRoot = root
}

func ResolvePyPath(factoryRoot string) (string, error) {
	if _, err := os.Stat(filepath.Join(factoryRoot, "py", "__init__.py")); err == nil {
		return factoryRoot, nil
	}
	if sourceRoot != "" {
		if _, err := os.Stat(filepath.Join(sourceRoot, "py", "__init__.py")); err == nil {
			return sourceRoot, nil
		}
	}
	if envSourceRoot != "" {
		if _, err := os.Stat(filepath.Join(envSourceRoot, "py", "__init__.py")); err == nil {
			return envSourceRoot, nil
		}
	}
	return "", fmt.Errorf("cannot locate py/ package: checked factoryRoot=%s, build-time source root=%q, and AF_SOURCE_ROOT=%q", factoryRoot, sourceRoot, envSourceRoot)
}

// endpointInfo mirrors the payload py/issuestore/server.py writes to
// .runtime/mcp_server.json after it binds its listener.
type endpointInfo struct {
	Transport string `json:"transport"`
	Address   string `json:"address"`
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

const (
	// startWaitTimeout bounds how long discoverOrStart waits for a peer
	// process that currently holds mcp_start.lock to publish its endpoint,
	// and how long startServer waits for the freshly spawned server to
	// become healthy. 30s tolerates cold Python starts and large DB migrations;
	// process death detection makes this a safety net, not the primary timeout.
	startWaitTimeout = 30 * time.Second
	startPollEvery   = 50 * time.Millisecond

	// healthTimeout is the per-probe HTTP timeout when checking /health.
	healthTimeout = 500 * time.Millisecond
)

// discoverOrStart returns a base URL of the form "http://127.0.0.1:<port>"
// for the in-tree MCP server rooted at factoryRoot, starting a new Python
// subprocess if none is running. Concurrent callers serialize on
// .runtime/mcp_start.lock; the loser polls for the winner's endpoint file.
func discoverOrStart(factoryRoot string) (string, error) {
	epFile := filepath.Join(factoryRoot, ".runtime", "mcp_server.json")

	if url, ok := tryLiveEndpoint(epFile); ok {
		return url, nil
	}

	lockPath := filepath.Join(factoryRoot, ".runtime", "mcp_start.lock")
	lk := lock.NewWithPath(lockPath)
	sessionID := fmt.Sprintf("mcpstore-%d", os.Getpid())

	err := lk.Acquire(sessionID)
	if errors.Is(err, lock.ErrLocked) {
		return pollForEndpoint(epFile)
	}
	if err != nil {
		return "", fmt.Errorf("mcpstore: acquire start lock: %w", err)
	}
	defer lk.Release()

	// Re-check under the lock: a peer may have spawned a healthy server
	// between our first probe and our lock acquisition.
	if url, ok := tryLiveEndpoint(epFile); ok {
		return url, nil
	}

	info, err := startServer(factoryRoot)
	if err != nil {
		return "", err
	}
	return "http://" + info.Address, nil
}

// tryLiveEndpoint reports whether an endpoint file names a live, healthy
// server. Returns the base URL when yes.
func tryLiveEndpoint(epFile string) (string, bool) {
	info, err := readEndpoint(epFile)
	if err != nil {
		return "", false
	}
	if !isAlive(info.PID) {
		return "", false
	}
	if !healthCheck(info.Address) {
		return "", false
	}
	return "http://" + info.Address, true
}

// pollForEndpoint blocks until either a live endpoint is published or the
// timeout elapses. Used when another caller holds mcp_start.lock.
func pollForEndpoint(epFile string) (string, error) {
	deadline := time.Now().Add(startWaitTimeout)
	for {
		if url, ok := tryLiveEndpoint(epFile); ok {
			return url, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("mcpstore: timed out waiting for peer to publish endpoint file %s", epFile)
		}
		time.Sleep(startPollEvery)
	}
}

// startServer spawns the Python MCP server as a detached subprocess rooted
// at factoryRoot, waits for it to become healthy, and returns the published
// endpoint info.
func startServer(factoryRoot string) (endpointInfo, error) {
	runtimeDir := filepath.Join(factoryRoot, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return endpointInfo{}, fmt.Errorf("mcpstore: create runtime dir: %w", err)
	}
	storeDir := config.StoreDir(factoryRoot)
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		return endpointInfo{}, fmt.Errorf("mcpstore: create store dir: %w", err)
	}

	dbPath := filepath.Join(storeDir, "issues.sqlite")
	epFile := filepath.Join(runtimeDir, "mcp_server.json")

	// Remove any stale endpoint file from a dead previous server so we can
	// detect the new server's publish unambiguously.
	_ = os.Remove(epFile)

	logPath := filepath.Join(runtimeDir, "mcp_server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return endpointInfo{}, fmt.Errorf("mcpstore: create server log: %w", err)
	}

	// Must NOT use exec.CommandContext — the server's lifetime is detached
	// from any single RPC caller's context.
	cmd := exec.Command("python3", "-m", "py.issuestore.server",
		"--db-path", dbPath,
		"--endpoint-file", epFile,
	)
	cmd.Dir = factoryRoot
	pyRoot, err := ResolvePyPath(factoryRoot)
	if err != nil {
		logFile.Close()
		return endpointInfo{}, fmt.Errorf("mcpstore: %w", err)
	}
	cmd.Env = append(os.Environ(), "PYTHONPATH="+pyRoot)
	// Redirect to a log file instead of inheriting the parent's fds.
	// Closing the Go-side handle after Start() releases Go's reference
	// while the child keeps writing via its inherited fd — avoids the
	// `go test` hang from exec.WaitDelay without losing output.
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return endpointInfo{}, fmt.Errorf("mcpstore: start python server: %w", err)
	}
	logFile.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ticker := time.NewTicker(startPollEvery)
	defer ticker.Stop()
	deadline := time.After(startWaitTimeout)
	for {
		select {
		case waitErr := <-done:
			logTail := readLogTail(logPath, 20)
			return endpointInfo{}, fmt.Errorf("mcpstore: python server exited during startup: %v\n\nServer log:\n%s", waitErr, logTail)
		case <-deadline:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			logTail := readLogTail(logPath, 20)
			return endpointInfo{}, fmt.Errorf("mcpstore: python server did not become healthy within %s\n\nServer log:\n%s", startWaitTimeout, logTail)
		case <-ticker.C:
			if info, err := readEndpoint(epFile); err == nil {
				if healthCheck(info.Address) {
					return info, nil
				}
			}
		}
	}
}

func readLogTail(path string, maxLines int) string {
	f, err := os.Open(path)
	if err != nil {
		return "(no server output captured)"
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) == 0 {
		return "(no server output captured)"
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// readEndpoint reads and parses .runtime/mcp_server.json.
func readEndpoint(path string) (endpointInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return endpointInfo{}, err
	}
	var info endpointInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return endpointInfo{}, fmt.Errorf("parse endpoint file: %w", err)
	}
	if info.Address == "" || info.PID == 0 {
		return endpointInfo{}, fmt.Errorf("endpoint file %s is missing address or pid", path)
	}
	return info, nil
}

// isAlive reports whether a process with the given PID exists. Mirrors
// internal/lock/lock.go:processExists. EPERM means the process exists but
// we lack permission to signal it.
func isAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// healthCheck GETs <address>/health with a short timeout and returns true on 200.
func healthCheck(address string) bool {
	client := &http.Client{Timeout: healthTimeout}
	resp, err := client.Get("http://" + address + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
