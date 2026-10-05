//go:build !integration

package tmux

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A recycle scrubs stale session env on a live agent's pane, so the primitive must be inert under
// the test guard exactly like SetEnvironment, or a unit test could strip a running agent's env.
func TestPR724_T9_UnsetEnvironmentGuarded(t *testing.T) {
	t.Run("production_identity_panics", func(t *testing.T) {
		tx := NewTmux()
		msg, panicked := capturePanic(func() { _ = tx.UnsetEnvironment("af-manager", "AF_BUILD_HOST") })
		if !panicked {
			t.Fatal(`UnsetEnvironment("af-manager") did not panic under the default-build guard; it must call guardOp("set-environment", target) first`)
		}
		if !strings.Contains(msg, "set-environment") || !strings.Contains(msg, "af-manager") {
			t.Errorf("guard panic must name the op set-environment and the target af-manager:\n%s", msg)
		}
	})

	t.Run("test_namespace_and_pane_inert", func(t *testing.T) {
		ResetRealOpCounter()
		tx := NewTmux()
		for _, target := range []string{"af-test-ab12cd34-x", "%5"} {
			if err := tx.UnsetEnvironment(target, "AF_BUILD_HOST"); err != nil {
				t.Errorf("guarded UnsetEnvironment(%q) must be an inert no-op returning nil, got %v", target, err)
			}
		}
		if n := ProductionRealOpCount(); n != 0 {
			t.Errorf("production real-op count = %d, want 0", n)
		}
	})
}

// p724d1FakeTmux puts a recording `tmux` first and alone on PATH and returns a reader for the argv
// of every invocation. TMUX/TMUX_TMPDIR are blanked too, so nothing here can reach a real server.
func p724d1FakeTmux(t *testing.T) func() [][]string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "argv.log")
	script := "#!/bin/sh\nprintf '%s\\037' \"$@\" >> '" + log + "'\nprintf '\\n' >> '" + log + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	return func() [][]string {
		data, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			calls = append(calls, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
		}
		return calls
	}
}

// The scrub set is dozens of keys and runs on every recycle, so it must cost one tmux exec, not one
// per key.
func TestPR724_T9_UnsetEnvironmentIsOneChainedExec(t *testing.T) {
	t.Run("keys_unset_in_one_exec", func(t *testing.T) {
		calls := p724d1FakeTmux(t)
		tx := &Tmux{guard: false}
		keys := []string{"PR724_D1_K1", "PR724_D1_K2", "PR724_D1_K3"}
		if err := tx.UnsetEnvironment("%7", keys...); err != nil {
			t.Fatalf("UnsetEnvironment: %v", err)
		}
		got := calls()
		if len(got) != 1 {
			t.Fatalf("UnsetEnvironment ran tmux %d times, want exactly 1: %q", len(got), got)
		}
		var segments [][]string
		for seg := range strings.SplitSeq(strings.Join(got[0], "\x1f"), "\x1f;\x1f") {
			segments = append(segments, strings.Split(seg, "\x1f"))
		}
		if len(segments) != len(keys) {
			t.Fatalf("argv %q holds %d ';'-chained commands, want one per key (%d)", got[0], len(segments), len(keys))
		}
		for i, seg := range segments {
			target := slices.Index(seg, "-t")
			if seg[0] != "set-environment" || !slices.Contains(seg, "-u") || target < 0 || target+1 >= len(seg) || seg[target+1] != "%7" || seg[len(seg)-1] != keys[i] {
				t.Errorf("command %d = %q, want set-environment -t %%7 -u %s", i, seg, keys[i])
			}
		}
	})

	t.Run("no_keys_no_exec", func(t *testing.T) {
		calls := p724d1FakeTmux(t)
		tx := &Tmux{guard: false}
		if err := tx.UnsetEnvironment("%7"); err != nil {
			t.Fatalf("UnsetEnvironment with no keys: %v", err)
		}
		if got := calls(); len(got) != 0 {
			t.Errorf("UnsetEnvironment with no keys ran tmux %d times: %q", len(got), got)
		}
	})
}
