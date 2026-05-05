package mcpstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStartWaitTimeoutIs30s(t *testing.T) {
	if startWaitTimeout != 30*time.Second {
		t.Errorf("startWaitTimeout = %v, want 30s", startWaitTimeout)
	}
}

func TestReadLogTail_MissingFile(t *testing.T) {
	got := readLogTail("/nonexistent/path/to/file.log", 20)
	if got != "(no server output captured)" {
		t.Errorf("readLogTail(missing) = %q, want %q", got, "(no server output captured)")
	}
}

func TestReadLogTail_EmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := readLogTail(path, 20)
	if got != "(no server output captured)" {
		t.Errorf("readLogTail(empty) = %q, want %q", got, "(no server output captured)")
	}
}

func TestReadLogTail_FewerThanMaxLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.log")
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readLogTail(path, 20)
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line3") {
		t.Errorf("readLogTail(short) should contain all lines, got %q", got)
	}
}

func TestReadLogTail_MoreThanMaxLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.log")
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readLogTail(path, 5)
	if strings.Contains(got, "line-01") {
		t.Errorf("readLogTail(long, 5) should NOT contain early lines, got %q", got)
	}
	if !strings.Contains(got, "line-30") {
		t.Errorf("readLogTail(long, 5) should contain last line, got %q", got)
	}
	if !strings.Contains(got, "line-26") {
		t.Errorf("readLogTail(long, 5) should contain 5th-from-last line, got %q", got)
	}
}
