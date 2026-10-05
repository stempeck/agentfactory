package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
)

var pluginCheckCmd = &cobra.Command{
	Use:   "check [<name>...]",
	Short: "Run installed integrations' health checks and record the results",
	RunE:  runPluginCheck,
}

// integrationCheckRecord is .runtime/integration_check/<name>.json (spec L632-634): the last
// `af plugin check` result, read back by the service ensure to report a degraded live service.
type integrationCheckRecord struct {
	V                 int    `json:"v"`
	Plugin            string `json:"plugin"`
	State             string `json:"state"`
	Exit              int    `json:"exit"`
	At                string `json:"at"`
	DurationMS        int64  `json:"duration_ms"`
	Output            string `json:"output"`
	HookFailMode      string `json:"hook_fail_mode"`
	ServiceRSSKB      int64  `json:"service_rss_kb"`
	ClaudeCodeVersion string `json:"claude_code_version"`
	// ContentSHA256 is the snapshot hash the check ran against, so admission can tell a result for
	// the consented content from one left by an earlier install (D1).
	ContentSHA256 string `json:"content_sha256,omitempty"`
}

func integrationCheckRecordPath(root, name string) string {
	return filepath.Join(root, ".runtime", "integration_check", name+".json")
}

// claudeCodeVersionFn returns the bounded `claude --version` output, or "" when absent (G15,
// B25). A seam so tests never exec a real claude.
var claudeCodeVersionFn = func() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "claude", "--version")
	c.Stdin = nil
	c.WaitDelay = integrationWaitDelay
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return displaySafe(strings.TrimSpace(integrationTail(string(out), 1)))
}

const (
	integrationCheckOK    = "ok"
	integrationCheckFail  = "fail"
	integrationCheckError = "error"
)

type pluginCheckRowJSON struct {
	Plugin       string `json:"plugin"`
	State        string `json:"state"`
	Exit         int    `json:"exit"`
	DurationMS   int64  `json:"duration_ms"`
	Output       string `json:"output"`
	HookFailMode string `json:"hook_fail_mode"`
	ServiceRSSKB int64  `json:"service_rss_kb"`
}

type pluginCheckJSON struct {
	State   string               `json:"state"`
	Results []pluginCheckRowJSON `json:"results"`
}

func runPluginCheck(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")
	all, _ := cmd.Flags().GetBool("all")
	if all == (len(args) > 0) {
		return errors.New("af plugin check: name one or more integrations, or pass --all")
	}
	cwd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return err
	}
	names := args
	if all {
		names = nil
		for _, name := range slices.Sorted(maps.Keys(cfg.Plugins)) {
			if cfg.Plugins[name].Integration != nil {
				names = append(names, name)
			}
		}
	}

	payload := pluginCheckJSON{State: integrationCheckOK, Results: []pluginCheckRowJSON{}}
	out := cmd.OutOrStdout()
	for _, name := range names {
		entry, ok := cfg.Plugins[name]
		var rec *integrationCheckRecord
		if !ok || entry.Integration == nil {
			rec = &integrationCheckRecord{Plugin: name, State: integrationCheckError, Exit: -1,
				Output: fmt.Sprintf("%q is not an installed integration", name)}
		} else {
			rec = runIntegrationCheck(cmd.Context(), root, name, entry.Integration)
		}
		if rec == nil {
			if !jsonMode {
				fmt.Fprintf(out, "%s: no [check] declared\n", displaySafe(name))
			}
			continue
		}
		payload.Results = append(payload.Results, pluginCheckRowJSON{
			Plugin: rec.Plugin, State: rec.State, Exit: rec.Exit, DurationMS: rec.DurationMS,
			Output: rec.Output, HookFailMode: rec.HookFailMode, ServiceRSSKB: rec.ServiceRSSKB,
		})
		payload.State = worseCheckState(payload.State, rec.State)
		if !jsonMode {
			fmt.Fprintf(out, "%s: %s (exit %d, %dms)\n", displaySafe(rec.Plugin), rec.State, rec.Exit, rec.DurationMS)
			if rec.Output != "" {
				fmt.Fprintln(out, rec.Output)
			}
		}
	}

	// The --json stdout stays the design L352 envelope, so detector rows go to stderr there.
	detectorOut := out
	if jsonMode {
		detectorOut = cmd.ErrOrStderr()
	}
	for _, row := range detectUnaccountedUserScopeFn(root) {
		fmt.Fprintf(detectorOut, "user scope: %s is not accounted for by any installed integration\n", displaySafe(row))
	}
	if jsonMode {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(b))
	}
	if payload.State != integrationCheckOK {
		return fmt.Errorf("af plugin check: state %s", payload.State)
	}
	return nil
}

// worseCheckState orders ok < fail < error: an unknown outcome outranks a known failure.
func worseCheckState(a, b string) string {
	rank := map[string]int{integrationCheckOK: 0, integrationCheckFail: 1, integrationCheckError: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// runIntegrationCheck runs one integration's [check] and writes its record. It returns nil when
// the integration declares no [check] (D24: nothing to record).
func runIntegrationCheck(ctx context.Context, root, name string, in *config.PluginIntegration) *integrationCheckRecord {
	rec := &integrationCheckRecord{V: 1, Plugin: name, Exit: -1, HookFailMode: in.HookFailMode, ContentSHA256: in.ContentSHA256}
	snap, m, err := config.VerifiedIntegrationSnapshot(root, name, in)
	if err != nil {
		rec.State, rec.Output = integrationCheckError, displaySafe(err.Error())
		return finishIntegrationCheck(root, rec)
	}
	if m.Check == nil {
		return nil
	}
	secrets, err := integrationSecretValues(root, m)
	if err != nil {
		// A value af cannot read is a value af cannot redact, so the check does not run.
		rec.State, rec.Output = integrationCheckError, displaySafe(err.Error())
		return finishIntegrationCheck(root, rec)
	}
	timeout, err := config.ParseIntegrationDuration(m.Check.Timeout)
	if err != nil {
		rec.State, rec.Output = integrationCheckError, displaySafe(err.Error())
		return finishIntegrationCheck(root, rec)
	}

	res := runIntegrationScript(ctx, snap, m.Check.Run, timeout)
	rec.DurationMS = res.duration.Milliseconds()
	rec.Output = redactIntegrationOutput(integrationTail(res.output, integrationOutputLines), secrets)
	switch {
	case res.timedOut:
		rec.State = integrationCheckFail
		rec.Output = strings.TrimSpace(rec.Output + fmt.Sprintf("\n[check] timed out after %s", timeout))
	case res.startErr != nil:
		rec.State = integrationCheckError
		rec.Output = strings.TrimSpace(rec.Output + "\n" + displaySafe(res.startErr.Error()))
	case res.exit != 0:
		rec.State, rec.Exit = integrationCheckFail, res.exit
	default:
		rec.State, rec.Exit = integrationCheckOK, 0
	}
	if in.Service != "" {
		rec.ServiceRSSKB = servicePaneRSSKB(in.Service)
	}
	rec.ClaudeCodeVersion = claudeCodeVersionFn()
	return finishIntegrationCheck(root, rec)
}

func finishIntegrationCheck(root string, rec *integrationCheckRecord) *integrationCheckRecord {
	rec.At = time.Now().UTC().Format(time.RFC3339)
	if err := writeIntegrationCheckRecord(root, rec); err != nil {
		rec.State = integrationCheckError
		rec.Output = strings.TrimSpace(rec.Output + "\n" + displaySafe(err.Error()))
	}
	return rec
}

func writeIntegrationCheckRecord(root string, rec *integrationCheckRecord) error {
	p := integrationCheckRecordPath(root, rec.Plugin)
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("writing check record: %w", err)
	}
	if err := fsutil.WriteFileAtomic(p, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing check record %s: %w", p, err)
	}
	return nil
}

// integrationSecretValues dereferences the manifest's file: [env] values (factory-root
// relative, D10) so their contents can be redacted from script output.
func integrationSecretValues(root string, m *config.IntegrationManifest) ([]string, error) {
	var values []string
	for _, key := range slices.Sorted(maps.Keys(m.Env)) {
		ref, ok := strings.CutPrefix(m.Env[key], "file:")
		if !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ref)))
		if err != nil {
			return nil, fmt.Errorf("[env] %s: reading %s: %w", key, ref, err)
		}
		values = append(values, string(b))
	}
	return values, nil
}

// servicePaneRSSKB is the resident set size of the service's pane process, or 0 when it cannot be
// read: the figure is diagnostic and never decides the check state.
func servicePaneRSSKB(session string) int64 {
	pid, err := servicePanePIDFn(session)
	if err != nil || pid <= 0 {
		return 0
	}
	if kb, ok := procStatusRSSKB(pid); ok {
		return kb
	}
	return psRSSKB(pid)
}

// procStatusRSSKB reads VmRSS from /proc, which macOS lacks; the container image ships no ps, so
// /proc is the only source there.
func procStatusRSSKB(pid int) (int64, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb, true
		}
	}
	return 0, false
}

func psRSSKB(pid int) int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kb
}

// integrationWaitDelay bounds how long a timed-out script's inherited pipes may hold Wait open.
const integrationWaitDelay = 2 * time.Second

// integrationOutputLines is how much script output is shown and recorded (spec L570: last 20).
const integrationOutputLines = 20

// integrationOutputCap bounds the bytes kept from a script's output; only the tail is shown.
const integrationOutputCap = 64 << 10

type integrationScriptResult struct {
	exit     int
	timedOut bool
	startErr error
	output   string
	duration time.Duration
}

// runIntegrationScript runs a declared [install]/[check] script from dir with its timeout and
// stdin closed, keeping the tail of its combined output.
func runIntegrationScript(ctx context.Context, dir, run string, timeout time.Duration) integrationScriptResult {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := &tailBuffer{max: integrationOutputCap}
	c := exec.CommandContext(ctx, filepath.Join(dir, filepath.FromSlash(run)))
	c.Dir = dir
	c.Stdin = nil
	c.Stdout, c.Stderr = out, out
	c.WaitDelay = integrationWaitDelay
	start := time.Now()
	err := c.Run()
	res := integrationScriptResult{exit: -1, output: string(out.b), duration: time.Since(start)}
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.timedOut = true
	case errors.As(err, &exitErr):
		res.exit = exitErr.ExitCode()
	case err != nil:
		res.startErr = err
	default:
		res.exit = 0
	}
	return res
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

// integrationTail returns the last n lines of s.
func integrationTail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

const integrationRedacted = "[redacted]"

// integrationSecretMinLen keeps a short dereferenced value (D45) from redacting common words.
const integrationSecretMinLen = 8

var (
	integrationSKToken     = regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`)
	integrationBearerToken = regexp.MustCompile(`(?i)(bearer )\S+`)
)

// redactIntegrationOutput masks the exact secretValues plus sk-[A-Za-z0-9_-]{8,} and Bearer \S+,
// then applies displaySafe per line (B11, D19): third-party output is shown to an operator and
// recorded on disk, so neither may carry a credential or raw control bytes.
func redactIntegrationOutput(s string, secretValues []string) string {
	for _, v := range secretValues {
		v = strings.TrimRight(v, "\r\n")
		if len(v) >= integrationSecretMinLen {
			s = strings.ReplaceAll(s, v, integrationRedacted)
		}
	}
	s = integrationSKToken.ReplaceAllString(s, integrationRedacted)
	s = integrationBearerToken.ReplaceAllString(s, "${1}"+integrationRedacted)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = displaySafe(l)
	}
	return strings.Join(lines, "\n")
}
