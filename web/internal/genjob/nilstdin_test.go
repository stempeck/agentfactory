package genjob

import (
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// nilStdinHelperEnv gates TestGenJobNilStdinRefusalHelperProcess: unset (a normal `go test ./...`
// run) it is a no-op self-skip; set (only by TestGenJob_NilStdin_RefusesNamingEnv's spawn), it plays
// the child half of the scenario. Mirrors internal/cmd/dispatch_reclaim_crossprocess_test.go's
// self-re-exec idiom — the only spawn construction that is neither a real `af` binary (genjob's
// fixed installArgv never sets --litellm, so the real install.go refusal is unreachable through
// genjob's actual spawn) nor a shell (forbidden module-wide by web/internal/server/lint_test.go's
// forbiddenShell, checked even against _test.go files).
const nilStdinHelperEnv = "AF_TEST_GENJOB_NILSTDIN_REFUSAL"

// TestGenJobNilStdinRefusalHelperProcess is the child half of TestGenJob_NilStdin_RefusesNamingEnv,
// never a real test — it self-skips unless nilStdinHelperEnv is set. Deliberately does not contain
// the substring "TestGenJob_NilStdin" so a `-run 'TestGenJob_NilStdin'` selection can never
// incidentally schedule it standalone.
func TestGenJobNilStdinRefusalHelperProcess(t *testing.T) {
	if os.Getenv(nilStdinHelperEnv) == "" {
		t.Skip("helper")
	}
	var b [1]byte
	if _, err := os.Stdin.Read(b[:]); err != io.EOF {
		fmt.Fprintln(os.Stdout, "helper: expected EOF on stdin, did not get it")
		os.Exit(2)
	}
	// Mirrors install.go's promptOpenAIKey EOF-refusal message shape (names the env var, per the
	// "names the env" requirement); this test only needs the env var name to appear in the job log,
	// not byte-identical text to the real message.
	fmt.Fprintln(os.Stdout, "cannot run af install --agents --litellm without an OpenAI API key: export OPENAI_API_KEY or create <keyfile>")
	os.Exit(1)
}

// TestGenJob_NilStdin_RefusesNamingEnv pins that a nil-stdin install-phase run produces a loud,
// non-zero decline naming OPENAI_API_KEY in the job log, and that the up phase is never spawned
// afterward (genjob's only observable proxy for "before teardown" — mirrors
// TestGenJob_InstallFailAbortsBeforeUp's shape). The stub's stdin is left nil deliberately: job.go
// never sets cmd.Stdin anywhere, so this is the same nil-default every other WithSpawn stub gets —
// no job.go change is needed or permitted (DO-NOT-CHANGE: no new job.go stdin API).
//
// NOTE: genjob's actual production installArgv ([]string{"install","--agents"}) never passes
// --litellm, so install.go's real OPENAI_API_KEY refusal is not reachable via genjob's real spawn
// today — this test's stub mimics the refusal's SHAPE (which install.go's own tests cover
// end-to-end), proving genjob's plumbing (nil stdin -> EOF, stdout/stderr -> log, pre-teardown
// abort), not production reachability. See todos/fable-implement/decisions.md D8.
func TestGenJob_NilStdin_RefusesNamingEnv(t *testing.T) {
	root := t.TempDir()
	var mu sync.Mutex
	var phases []Phase
	spawn := func(p Phase) *osexec.Cmd {
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
		if p != PhaseInstall {
			return osexec.Command("true") // up phase must never be reached
		}
		cmd := osexec.Command(os.Args[0], "-test.run=^TestGenJobNilStdinRefusalHelperProcess$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), nilStdinHelperEnv+"=1")
		return cmd // Stdin left nil — the nil-stdin condition under test
	}
	j := New(root, WithSpawn(spawn))
	if err := j.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := waitForPhase(t, j, PhaseFailed, 5*time.Second)
	if st.ExitCode == 0 {
		t.Fatalf("nil-stdin refusal must be a non-zero exit, got %d", st.ExitCode)
	}

	data, err := os.ReadFile(logPath(root))
	if err != nil {
		t.Fatalf("reading job log: %v", err)
	}
	if !strings.Contains(string(data), "OPENAI_API_KEY") {
		t.Fatalf("job log does not name the env var in the refusal:\n%s", data)
	}

	mu.Lock()
	got := append([]Phase(nil), phases...)
	mu.Unlock()
	if len(got) != 1 || got[0] != PhaseInstall {
		t.Fatalf("up phase must NOT be spawned after a nil-stdin decline (refusal must land BEFORE any further phase/teardown); spawn phases = %v", got)
	}
}
