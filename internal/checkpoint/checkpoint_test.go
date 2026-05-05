package checkpoint

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteRead(t *testing.T) {
	dir := t.TempDir()

	original := &Checkpoint{
		FormulaID:     "f-abc",
		CurrentStep:   "step-1",
		StepTitle:     "Do the thing",
		ModifiedFiles: []string{"main.go", "go.mod"},
		LastCommit:    "deadbeef",
		Branch:        "feature/test",
		HookedBead:    "hq-123",
		Timestamp:     time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC),
		SessionID:     "test-session",
		Notes:         "some context",
	}

	if err := Write(dir, original); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("Read returned nil")
	}

	if got.FormulaID != original.FormulaID {
		t.Errorf("FormulaID = %q, want %q", got.FormulaID, original.FormulaID)
	}
	if got.CurrentStep != original.CurrentStep {
		t.Errorf("CurrentStep = %q, want %q", got.CurrentStep, original.CurrentStep)
	}
	if got.StepTitle != original.StepTitle {
		t.Errorf("StepTitle = %q, want %q", got.StepTitle, original.StepTitle)
	}
	if len(got.ModifiedFiles) != len(original.ModifiedFiles) {
		t.Errorf("ModifiedFiles len = %d, want %d", len(got.ModifiedFiles), len(original.ModifiedFiles))
	}
	if got.LastCommit != original.LastCommit {
		t.Errorf("LastCommit = %q, want %q", got.LastCommit, original.LastCommit)
	}
	if got.Branch != original.Branch {
		t.Errorf("Branch = %q, want %q", got.Branch, original.Branch)
	}
	if got.HookedBead != original.HookedBead {
		t.Errorf("HookedBead = %q, want %q", got.HookedBead, original.HookedBead)
	}
	if !got.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, original.Timestamp)
	}
	if got.SessionID != original.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, original.SessionID)
	}
	if got.Notes != original.Notes {
		t.Errorf("Notes = %q, want %q", got.Notes, original.Notes)
	}
}

func TestReadNoFile(t *testing.T) {
	dir := t.TempDir()

	cp, err := Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if cp != nil {
		t.Fatalf("expected nil checkpoint, got %+v", cp)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()

	cp := &Checkpoint{
		FormulaID: "f-remove",
		Timestamp: time.Now(),
		SessionID: "sess",
	}
	if err := Write(dir, cp); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(Path(dir)); err != nil {
		t.Fatalf("checkpoint file not found after write: %v", err)
	}

	if err := Remove(dir); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read after Remove: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after Remove")
	}
}

func TestCapture(t *testing.T) {
	dir := t.TempDir()

	// Initialize a git repo
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Create a file and commit it
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "hello.txt"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Modify a file to create dirty state
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}

	cp, err := Capture(dir)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	if cp.LastCommit == "" {
		t.Error("LastCommit is empty")
	}
	if len(cp.LastCommit) < 7 {
		t.Errorf("LastCommit too short: %q", cp.LastCommit)
	}
	if cp.Branch == "" {
		t.Error("Branch is empty")
	}
	if len(cp.ModifiedFiles) == 0 {
		t.Error("ModifiedFiles is empty, expected at least 1 modified file")
	}
	if cp.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
}

func TestWithFormula(t *testing.T) {
	cp := &Checkpoint{}
	result := cp.WithFormula("f-123", "step-2", "Build the thing")

	if result != cp {
		t.Error("WithFormula should return same pointer")
	}
	if cp.FormulaID != "f-123" {
		t.Errorf("FormulaID = %q, want %q", cp.FormulaID, "f-123")
	}
	if cp.CurrentStep != "step-2" {
		t.Errorf("CurrentStep = %q, want %q", cp.CurrentStep, "step-2")
	}
	if cp.StepTitle != "Build the thing" {
		t.Errorf("StepTitle = %q, want %q", cp.StepTitle, "Build the thing")
	}
}

func TestWithHookedBead(t *testing.T) {
	cp := &Checkpoint{}
	result := cp.WithHookedBead("hq-456")

	if result != cp {
		t.Error("WithHookedBead should return same pointer")
	}
	if cp.HookedBead != "hq-456" {
		t.Errorf("HookedBead = %q, want %q", cp.HookedBead, "hq-456")
	}
}

func TestSummary(t *testing.T) {
	tests := []struct {
		name string
		cp   Checkpoint
		want string
	}{
		{
			name: "empty",
			cp:   Checkpoint{},
			want: "no significant state",
		},
		{
			name: "formula only",
			cp:   Checkpoint{FormulaID: "f-1"},
			want: "formula f-1",
		},
		{
			name: "formula with step",
			cp:   Checkpoint{FormulaID: "f-1", CurrentStep: "s-2"},
			want: "formula f-1, step s-2",
		},
		{
			name: "hooked bead",
			cp:   Checkpoint{HookedBead: "hq-99"},
			want: "hooked: hq-99",
		},
		{
			name: "modified files",
			cp:   Checkpoint{ModifiedFiles: []string{"a.go", "b.go", "c.go"}},
			want: "3 modified files",
		},
		{
			name: "branch",
			cp:   Checkpoint{Branch: "feature/x"},
			want: "branch: feature/x",
		},
		{
			name: "full",
			cp: Checkpoint{
				FormulaID:     "f-1",
				CurrentStep:   "s-2",
				HookedBead:    "hq-99",
				ModifiedFiles: []string{"a.go"},
				Branch:        "main",
			},
			want: "formula f-1, step s-2, hooked: hq-99, 1 modified files, branch: main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cp.Summary()
			if got != tt.want {
				t.Errorf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsStale(t *testing.T) {
	cp := &Checkpoint{
		Timestamp: time.Now().Add(-2 * time.Hour),
	}

	if !cp.IsStale(1 * time.Hour) {
		t.Error("expected stale with 1h threshold")
	}
	if cp.IsStale(3 * time.Hour) {
		t.Error("expected not stale with 3h threshold")
	}
}

func TestAutoTimestamp(t *testing.T) {
	dir := t.TempDir()

	cp := &Checkpoint{
		SessionID: "manual-session",
	}
	// Timestamp is zero — Write should auto-set it
	if err := Write(dir, cp); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if cp.Timestamp.IsZero() {
		t.Error("Write did not set Timestamp")
	}
	if time.Since(cp.Timestamp) > 5*time.Second {
		t.Error("auto-set Timestamp is not recent")
	}
}

func TestAutoSessionID(t *testing.T) {
	dir := t.TempDir()

	cp := &Checkpoint{
		Timestamp: time.Now(),
	}
	if err := Write(dir, cp); err != nil {
		t.Fatalf("Write: %v", err)
	}
	expected := fmt.Sprintf("pid-%d", os.Getpid())
	if cp.SessionID != expected {
		t.Errorf("SessionID = %q, want %q", cp.SessionID, expected)
	}
}
