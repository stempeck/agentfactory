package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Untagged so the `!integration` gate harness and the `integration` deny probe share one exec-capable
// directory probe. Both need it for the same reason and the build tags are the only thing that ever
// separated them: /tmp is mounted noexec on some machines, and a binary planted there fails to exec
// while the test around it keeps reporting green against whatever the ambient PATH resolves instead.

const execCapableMarker = "af-test-exec-ok"

// tryExecCapableDir returns a directory proven to run a file, by writing a script there and running it.
//
// The repo tree is the LAST candidate, so `make test` — which redirects both TMPDIR and GOTMPDIR to
// $HOME/.cache/af-test (Makefile:55-59) — never reaches it. A bare `go test` on a machine with a noexec
// /tmp does, and then builds binaries under internal/cmd/testdata/<prefix>*. The Cleanup below removes
// them, so only a hard kill leaves any behind — and every prefix passed here must therefore also be
// listed in .gitignore, or that survivor is a staged 16 MB binary. Both current prefixes are.
//
// Non-fatal by design: some callers only want to HARDEN an existing test, while others cannot prove
// anything at all without it. The distinction — and the reason for it — belongs at the call site.
func tryExecCapableDir(t *testing.T, prefix string) (string, error) {
	t.Helper()

	var tried []string
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = ""
	} else {
		cache = filepath.Join(cache, "af-test")
	}
	for _, base := range []string{os.Getenv("GOTMPDIR"), os.Getenv("TMPDIR"), cache,
		filepath.Join(findRepoRoot(t), "internal", "cmd", "testdata")} {
		if base == "" {
			continue
		}
		if err := os.MkdirAll(base, 0o755); err != nil {
			tried = append(tried, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		dir, err := os.MkdirTemp(base, prefix)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		if err := probeExecBit(dir); err != nil {
			_ = os.RemoveAll(dir)
			tried = append(tried, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		return dir, nil
	}
	return "", fmt.Errorf("every candidate rejected: %s", strings.Join(tried, "; "))
}

func probeExecBit(dir string) error {
	probe := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/bash\necho "+execCapableMarker+"\n"), 0o755); err != nil {
		return err
	}
	out, err := exec.Command(probe).CombinedOutput()
	if err != nil {
		return fmt.Errorf("exec probe failed: %v (%s)", err, bytes.TrimSpace(out))
	}
	if !bytes.Contains(out, []byte(execCapableMarker)) {
		return fmt.Errorf("exec probe produced %q", out)
	}
	return nil
}
