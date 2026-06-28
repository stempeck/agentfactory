//go:build integration

package server

// Issue #425 Phase 5A — the one behavioral test that proves the `quickdocker.sh <repo> --web`
// front-door bridge end-to-end. It drives `--web` against a REAL running container, parses the
// printed `http://127.0.0.1:<HOSTPORT>/` link, GETs `<link>healthz` and asserts `"ok":true`
// ACROSS the bridge (not in-process like healthz_test.go), then performs a REAL
// `docker stop && docker start` and re-checks — proving Phase 4 restart survival.
//
// Tier: //go:build integration only. The hermetic web-unit lane (`cd web && go test ./...`,
// no -tags) never compiles this file, so it pulls in no docker dependency there. It is wired to
// run under `make test-integration` (the root target gained a `cd web && … -tags=integration`
// line, because root `./...` does not descend into this nested module).
//
// Host idiom: like requireClaude in internal/session/session_integration_test.go, requireDocker
// makes this a clean t.Skip — not a failure — on a host without docker, and the behavioral test
// also skips unless AF_BRIDGE_TEST_REPO names a provisioned af_* container. All external commands
// are invoked via argv arrays only (never a shell string) so the source-lint in lint_test.go
// (TestExec_NoLiveTreeMutation, which raw-scans this directory in the unit lane) stays green.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// factoryURLRe matches the exact clickable link `--web` prints at quickdocker.sh:279
// (`🔗 Open your factory:  http://127.0.0.1:${HOSTPORT}/`). It is anchored to the 127.0.0.1
// loopback host on purpose: a non-loopback address must never match.
var factoryURLRe = regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+/`)

// parseFactoryURL extracts the printed factory link from `--web` output, or "" if none is present.
// The returned link already ends in "/", so the health URL is simply <link>+"healthz".
func parseFactoryURL(out string) string {
	return factoryURLRe.FindString(out)
}

// TestWebBridge_ParseFactoryURL is host-independent (no docker): it pins the exact parse of the
// printed link so the most error-prone part (matching the emoji line with its two spaces and
// trailing slash) is proven even where the behavioral test must skip.
func TestWebBridge_ParseFactoryURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"exact printed line", "🔗 Open your factory:  http://127.0.0.1:20001/", "http://127.0.0.1:20001/"},
		{"embedded in multiline output", "step 1\n🔗 Open your factory:  http://127.0.0.1:29999/\ndone\n", "http://127.0.0.1:29999/"},
		{"high port", "🔗 Open your factory:  http://127.0.0.1:65535/", "http://127.0.0.1:65535/"},
		{"no link present", "ERROR: container 'af_x' is not running", ""},
		{"non-loopback host is not matched", "see http://10.0.0.5:8080/ instead", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFactoryURL(tc.in); got != tc.want {
				t.Errorf("parseFactoryURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	// The health URL is <link>healthz with NO extra slash — the link already ends in "/".
	if link := parseFactoryURL("🔗 Open your factory:  http://127.0.0.1:20001/"); link+"healthz" != "http://127.0.0.1:20001/healthz" {
		t.Errorf("health URL = %q, want http://127.0.0.1:20001/healthz", link+"healthz")
	}
}

// TestWebBridge_ContainerNameForRepo is host-independent: it pins the `af_<sanitized>` derivation to
// what quickdocker.sh computes (:311-316 normalize, then :332 sanitize) for the input forms the
// script accepts, so a mismatch (which would cause a confusing skip) is caught without docker.
func TestWebBridge_ContainerNameForRepo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"owner/repo", "af_owner_repo"},
		{"https://github.com/owner/repo.git", "af_owner_repo"},
		{"git@github.com:owner/repo.git", "af_owner_repo"},
		{"github.com/owner/repo", "af_owner_repo"},
	}
	for _, tc := range cases {
		if got := containerNameForRepo(tc.in); got != tc.want {
			t.Errorf("containerNameForRepo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// requireDocker mirrors requireClaude (internal/session/session_integration_test.go:34-39): a clean
// t.Skip when the docker binary is absent or no docker host is reachable, so this integration test
// is a skip — never a failure — on a docker-less host (e.g. CI without docker, or an agent sandbox).
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH — skipping bridge integration test (needs a real docker host)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "docker", "info").Run(); err != nil {
		t.Skip("`docker info` failed (no reachable docker host) — skipping bridge integration test")
	}
}

// nonNameRe mirrors quickdocker.sh:332's `sed 's/[^a-zA-Z0-9_.-]/_/g'` so the test can derive the
// same container name (`af_<sanitized-repo-path>`) the script computes for --web.
var nonNameRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// normalizeRepoPath mirrors quickdocker.sh:311-316 — the script strips URL/scheme prefixes and a
// trailing `.git` BEFORE sanitizing, so a full-URL AF_BRIDGE_TEST_REPO derives the same name a bare
// `owner/repo` does. Applied in the same order as the script's sequential `${VAR#...}`/`${VAR%...}`.
func normalizeRepoPath(repo string) string {
	for _, p := range []string{"https://", "http://", "git@github.com:", "git@github.com/", "github.com/"} {
		repo = strings.TrimPrefix(repo, p)
	}
	return strings.TrimSuffix(repo, ".git")
}

// containerNameForRepo reproduces quickdocker.sh's `af_<sanitized>` name: normalize (:311-316) then
// sanitize (:332). AF_BRIDGE_TEST_CONTAINER overrides this when a non-derivable name is needed.
func containerNameForRepo(repo string) string {
	return "af_" + nonNameRe.ReplaceAllString(normalizeRepoPath(repo), "_")
}

// repoRoot resolves the worktree root so the test can run quickdocker.sh by absolute path. The web
// console is a nested module, so a relative walk-up is off-by-one prone; `git rev-parse` is robust.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not in a git work tree (git rev-parse failed: %v) — cannot locate quickdocker.sh", err)
	}
	return strings.TrimSpace(string(out))
}

func dockerContainerRunning(name string) bool {
	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// webuiInstalled checks the Phase 0 prerequisite the bridge itself enforces (quickdocker.sh:123):
// webui must be installed inside the container at /home/dev/.local/bin/webui.
func webuiInstalled(name string) bool {
	return exec.Command("docker", "exec", name, "test", "-x", "/home/dev/.local/bin/webui").Run() == nil
}

// runWeb runs `quickdocker.sh <repo> --web` and returns its combined output. --web is detached and
// idempotent, so it must RETURN PROMPTLY ("reveal the URL", not a session). A blocking session would
// trip the context deadline, failing the "returns promptly" assertion rather than hanging the suite.
func runWeb(t *testing.T, script, repo string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, script, repo, "--web")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("`quickdocker.sh %s --web` did not return promptly — the bridge must be detached, not a blocking session.\noutput:\n%s", repo, out.String())
	}
	if err != nil {
		t.Fatalf("`quickdocker.sh %s --web` failed: %v\noutput:\n%s", repo, err, out.String())
	}
	return out.String()
}

// pollHealthz GETs <link>healthz across the bridge until it returns 200 with a body containing
// `"ok":true` (the handleHealthz envelope, web/internal/server/server.go:294-296), or fails after
// `within`. Asserting on the body — not just the status — guards against the bridge returning a 200
// with the wrong payload.
func pollHealthz(t *testing.T, link string, within time.Duration) {
	t.Helper()
	healthURL := link + "healthz"
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(within)
	var lastStatus int
	var lastBody string
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastStatus, lastBody, lastErr = resp.StatusCode, string(body), nil
		if resp.StatusCode == http.StatusOK && strings.Contains(lastBody, `"ok":true`) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("GET %s never returned 200 + \"ok\":true within %s (last status=%d, lastErr=%v, lastBody=%q)",
		healthURL, within, lastStatus, lastErr, lastBody)
}

// restartContainer performs a REAL `docker stop && docker start` (NOT docker exec, NOT a
// quickstart.sh re-run): only a real start re-runs PID-1 `bash --login` (quickdocker.sh:493), which
// re-fires the Phase 4 login-init relaunch guard (~/.bash_profile, quickdocker.sh:572-585).
func restartContainer(t *testing.T, name string) {
	t.Helper()
	if out, err := exec.Command("docker", "stop", name).CombinedOutput(); err != nil {
		t.Fatalf("docker stop %s: %v\n%s", name, err, out)
	}
	if out, err := exec.Command("docker", "start", name).CombinedOutput(); err != nil {
		t.Fatalf("docker start %s: %v\n%s", name, err, out)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if dockerContainerRunning(name) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("container %s did not report Running within 30s after docker start", name)
}

// TestWebBridge_HealthzAcrossBridge is the behavioral end-to-end proof (design AC-1 + AC-2). It needs
// a real docker host AND a provisioned container, so it skips cleanly otherwise:
//   - requireDocker: docker binary + reachable host.
//   - AF_BRIDGE_TEST_REPO: owner/repo of a running, webui-installed af_* container. Optional
//     AF_BRIDGE_TEST_CONTAINER overrides the derived `af_<sanitized>` name.
func TestWebBridge_HealthzAcrossBridge(t *testing.T) {
	requireDocker(t)

	repo := os.Getenv("AF_BRIDGE_TEST_REPO")
	if repo == "" {
		t.Skip("AF_BRIDGE_TEST_REPO not set — set it to the owner/repo of a provisioned af_* container to run the bridge test")
	}
	container := os.Getenv("AF_BRIDGE_TEST_CONTAINER")
	if container == "" {
		container = containerNameForRepo(repo)
	}
	if !dockerContainerRunning(container) {
		t.Skipf("container %q is not running — provision it first (quickdocker.sh %s) then retry", container, repo)
	}
	if !webuiInstalled(container) {
		t.Skipf("webui not installed in %q (Phase 0 prerequisite) — run quickstart.sh inside it once, then retry", container)
	}

	script := filepath.Join(repoRoot(t), "quickdocker.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("quickdocker.sh not found at %s: %v", script, err)
	}

	// AC-1: --web reveals the link (promptly, detached) and /healthz answers 200 {"ok":true} across the bridge.
	link := parseFactoryURL(runWeb(t, script, repo))
	if link == "" {
		t.Fatalf("no http://127.0.0.1:<port>/ link in --web output for repo %q", repo)
	}
	pollHealthz(t, link, 15*time.Second)

	// AC-2: a REAL restart, then re-run --web (Phase 4 relaunches webui inside on login) and re-check.
	restartContainer(t, container)
	link2 := parseFactoryURL(runWeb(t, script, repo))
	if link2 == "" {
		t.Fatalf("no http://127.0.0.1:<port>/ link in --web output after restart for repo %q", repo)
	}
	pollHealthz(t, link2, 30*time.Second)
}
