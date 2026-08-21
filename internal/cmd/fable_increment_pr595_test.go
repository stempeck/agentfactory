package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Pinning tests for PR #595 unresolved review comments (fable-increment), cmd layer.

// T9 — the statusline RENDER hot path is contractually silent on every failure, yet
// downgradeRootMismatch writes a warning to os.Stderr on an AF_ROOT mismatch, so every refresh
// emits stderr. Chosen fix (decisions.md D10): a render-local SILENT downgrade, leaving
// downgradeRootMismatch + its 5 diagnostic callers untouched. Mirrors the fixture of
// TestRender_RootMismatchDowngrades but asserts NO stderr. RED today.
func TestFableIncr_T9_RenderRootMismatchIsSilent(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	os.WriteFile(filepath.Join(fx.clone, ".agentfactory", ".statusline-gate"), []byte("on\n"), 0o644)
	t.Chdir(fx.clone)
	t.Setenv("AF_ROOT", fx.outer)

	statuslineCmd.SetContext(t.Context())
	statuslineCmd.SetIn(strings.NewReader(`{"model":{"display_name":"X"},"workspace":{"project_dir":"/p"}}`))
	var out bytes.Buffer
	statuslineCmd.SetOut(&out)
	t.Cleanup(func() { statuslineCmd.SetOut(nil); statuslineCmd.SetIn(nil) })

	stderr := captureStderr(t, func() {
		if err := runStatusline(statuslineCmd, []string{"render"}); err != nil {
			t.Fatalf("render must downgrade a root mismatch, never hard-error; got: %v", err)
		}
	})
	if stderr != "" {
		t.Errorf("T9: the render hot path must emit NO stderr on an AF_ROOT mismatch (silent-render contract, C-5); got %q", stderr)
	}
}

// T9 GUARD — "preserve warnings only for interactive diagnostic commands." The silent-render
// fix must NOT silence the diagnostic verbs. The `status` verb, under the same mismatch, must
// STILL warn on stderr. Protective: passes now, must keep passing after the T9 fix.
func TestFableIncr_T9_StatusVerbStillWarns(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	t.Chdir(fx.clone)
	t.Setenv("AF_ROOT", fx.outer)
	t.Cleanup(func() { statuslineCmd.SetOut(nil); statuslineCmd.SetIn(nil) })

	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if err := runStatusline(statuslineCmd, []string{"status"}); err != nil {
				t.Fatalf("status must downgrade, never hard-error; got: %v", err)
			}
		})
	})
	if !strings.Contains(stderr, "mismatch") {
		t.Errorf("T9 guard: the diagnostic `status` verb must STILL warn on an AF_ROOT mismatch; stderr = %q", stderr)
	}
}

// T9 (broader contract) — resolveInvokerRoot ALSO writes stderr on two NIL-error success paths
// that silentRootDowngrade (error branches only) cannot reach: the nested-factory warning and the
// stale-AF_ROOT warning. Those leak into the pane on a plain render. The render path now resolves
// via resolveInvokerRootWarn(io.Discard). This pins the nested-factory case — a worktree agent's
// factory enclosed by another, AF_ROOT matching the nested root (the common condition): render
// must emit NO stderr. RED before the io.Discard hatch (warnEnclosingRoot wrote on every render).
func TestFableIncr_T9_RenderNestedFactoryIsSilent(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	os.WriteFile(filepath.Join(fx.clone, ".agentfactory", ".statusline-gate"), []byte("on\n"), 0o644)
	t.Chdir(fx.clone)
	t.Setenv("AF_ROOT", fx.clone) // matches the nested root ⇒ SameResolvedRoot, enclosing != ""

	statuslineCmd.SetContext(t.Context())
	statuslineCmd.SetIn(strings.NewReader(`{"model":{"display_name":"X"},"workspace":{"project_dir":"/p"}}`))
	var out bytes.Buffer
	statuslineCmd.SetOut(&out)
	t.Cleanup(func() { statuslineCmd.SetOut(nil); statuslineCmd.SetIn(nil) })

	stderr := captureStderr(t, func() {
		if err := runStatusline(statuslineCmd, []string{"render"}); err != nil {
			t.Fatalf("render must never hard-error; got: %v", err)
		}
	})
	if stderr != "" {
		t.Errorf("T9: render inside a nested factory must emit NO stderr (silent-render contract, C-5); got %q", stderr)
	}
}

// T9 (broader contract) — the reviewer's own stated scenario: a stale/garbage AF_ROOT that no
// longer resolves to a factory. resolveInvokerRoot warns "AF_ROOT ... does not resolve" on a
// NIL-error success path. Render must be silent. RED before the io.Discard hatch.
func TestFableIncr_T9_RenderStaleAfRootIsSilent(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	os.WriteFile(filepath.Join(fx.outer, ".agentfactory", ".statusline-gate"), []byte("on\n"), 0o644)
	t.Chdir(fx.outer)
	t.Setenv("AF_ROOT", fx.markerless) // set but resolves to no factory ⇒ the stale-AF_ROOT warning

	statuslineCmd.SetContext(t.Context())
	statuslineCmd.SetIn(strings.NewReader(`{"model":{"display_name":"X"},"workspace":{"project_dir":"/p"}}`))
	var out bytes.Buffer
	statuslineCmd.SetOut(&out)
	t.Cleanup(func() { statuslineCmd.SetOut(nil); statuslineCmd.SetIn(nil) })

	stderr := captureStderr(t, func() {
		if err := runStatusline(statuslineCmd, []string{"render"}); err != nil {
			t.Fatalf("render must never hard-error; got: %v", err)
		}
	})
	if stderr != "" {
		t.Errorf("T9: render with a stale AF_ROOT must emit NO stderr (silent-render contract, C-5); got %q", stderr)
	}
}

// T9 GUARD — the render silencing must be RENDER-SCOPED, not a blanket removal of the enclosing
// warning: the diagnostic `status` verb must STILL warn on the same nested-factory condition.
func TestFableIncr_T9_StatusVerbWarnsOnNested(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	t.Chdir(fx.clone)
	t.Setenv("AF_ROOT", fx.clone)
	t.Cleanup(func() { statuslineCmd.SetOut(nil); statuslineCmd.SetIn(nil) })

	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if err := runStatusline(statuslineCmd, []string{"status"}); err != nil {
				t.Fatalf("status must never hard-error; got: %v", err)
			}
		})
	})
	if !strings.Contains(stderr, "enclosing") {
		t.Errorf("T9 guard: the diagnostic `status` verb must STILL warn on a nested factory; stderr = %q", stderr)
	}
}

// T4/F8 — sanitize strips control/escape bytes but not arbitrary text, so a branch or path
// literally named a watchdog needle is surfaced verbatim into the pane where the watchdog's
// detectErrorPattern scans for it → false endpoint-failure respawn of a healthy agent. Chosen
// fix (decisions.md D4/D5): scrub the watchdog needles from rendered content in the cmd render
// core (reusing endpointFailureSignatures, env-free). RED today: the needle is emitted verbatim.
func TestFableIncr_T4_RenderedNeedleIsScrubbed(t *testing.T) {
	root := setupConfigFactory(t)
	os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0o644)

	// A working directory literally named an empty-context endpoint-failure needle
	// (unsupported_api_for_model — valid path/branch characters).
	payload := `{"session_id":"s","model":{"display_name":"M"},"workspace":{"project_dir":"/repo/unsupported_api_for_model"}}`
	var out bytes.Buffer
	if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", time.Now(), false); err != nil {
		t.Fatalf("render core: %v", err)
	}
	if strings.Contains(out.String(), "unsupported_api_for_model") {
		t.Errorf("T4: a rendered element containing a watchdog needle must be scrubbed so detectErrorPattern cannot false-trip; got %q", out.String())
	}
}

// T6/F3 — the `af install --init` banner prints "Statusline: on" unconditionally (outside the
// seed-if-absent block), lying to an operator who ran `af statusline off`. Chosen fix
// (decisions.md D7): reflect the actual gate state via statuslineFactoryEnabled. runInstallInit
// spawns the Python MCP server and is not unit-testable, so this pins the fix mechanically via a
// bounded source scan (the repo's own idiom, cf. watchdog_test.go:730). RED today: runInstallInit
// does not read the gate before announcing the banner.
func TestFableIncr_T6_InstallBannerReadsGateState(t *testing.T) {
	body := funcBody(t, "install.go", "func runInstallInit(")
	if !strings.Contains(body, "statuslineFactoryEnabled") {
		t.Errorf("T6: runInstallInit must read the actual gate state (statuslineFactoryEnabled) before printing the Statusline banner, not announce \"on\" unconditionally")
	}
}

// T7/F4 — a stale in-code comment narrates the intermediate "Phase 3 … until Phase 4a wires it
// in" build state this PR already completed. Chosen fix (decisions.md D8): rewrite it to shipped
// behavior. RED today: the stale narration is present. Source-scan (comment-only change).
func TestFableIncr_T7_StalePhase4aCommentRemoved(t *testing.T) {
	src, err := os.ReadFile("statusline.go")
	if err != nil {
		t.Fatalf("read statusline.go: %v", err)
	}
	for _, stale := range []string{"until Phase 4a wires it in", "In Phase 3 the templates do not yet carry the key"} {
		if strings.Contains(string(src), stale) {
			t.Errorf("T7: the stale Phase-3/4a narration %q must be replaced with a description of shipped behavior", stale)
		}
	}
}

// funcBody returns the text of a top-level function's body, from its `signature` up to the next
// top-level `\nfunc ` (or end of file). Mirrors watchdog_test.go's bounded source-scan idiom so
// the assertion is scoped to the named function, not the whole file.
func funcBody(t *testing.T, file, signature string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	body := string(src)
	start := strings.Index(body, signature)
	if start < 0 {
		t.Fatalf("could not locate %q in %s", signature, file)
	}
	rest := body[start+len(signature):]
	if next := strings.Index(rest, "\nfunc "); next >= 0 {
		return rest[:next]
	}
	return rest
}
