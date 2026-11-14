package cmd

import (
	"os"
	"strings"
	"testing"
)

// T1 (PR #767): auditDB.osv replaces the whole OSV table, so a second call in the shipped-locks
// subtest silently erases the earlier rows and their offline guard stops going red.
func TestFableIncr767_T1_ShippedLocksSetTheOSVTableInOneCall(t *testing.T) {
	src, err := os.ReadFile("advisory_decay_test.go")
	if err != nil {
		t.Fatalf("read advisory_decay_test.go: %v", err)
	}
	const subtest = `t.Run("shipped locks and register pass at the earliest recheck_by"`
	start := strings.Index(string(src), subtest)
	if start < 0 {
		t.Fatalf("could not locate %s in advisory_decay_test.go", subtest)
	}
	setup, _, found := strings.Cut(string(src)[start:], "r.run(shipped")
	if !found {
		t.Fatal("could not locate the shipped-locks subtest's first r.run(shipped")
	}
	call := strings.Index(setup, "db.osv(")
	if call < 0 {
		t.Fatal("the shipped-locks subtest no longer stubs OSV before its first run")
	}

	if n := strings.Count(setup, "db.osv("); n != 1 {
		t.Errorf("the shipped-locks subtest calls db.osv %d times before its first run, want 1: db.osv replaces the whole table, so add rows to the existing call", n)
	}
	callLine := strings.LastIndex(setup[:call], "\n")
	if above := trailingComment767(setup[:callLine]); !strings.Contains(above, "replaces the whole table") {
		t.Errorf("the comment above the shipped-locks db.osv call must say it replaces the whole table; got %q", above)
	}
}

func trailingComment767(src string) string {
	lines := strings.Split(src, "\n")
	var words []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "//") {
			break
		}
		words = append(strings.Fields(strings.TrimPrefix(line, "//")), words...)
	}
	return strings.Join(words, " ")
}
