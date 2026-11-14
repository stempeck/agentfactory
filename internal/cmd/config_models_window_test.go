package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// No test in this file calls t.Parallel: each swaps probeHarnessWindowFn, claudeCodeVersionFn and
// httpProbe, and the detector's pass memo is package state.

type cwProbeStub struct {
	profiles  []map[string]string
	deadlines []time.Duration
}

func cwStubProbe(t *testing.T, p harnessWindowProbe, err error) *cwProbeStub {
	t.Helper()
	s := &cwProbeStub{}
	orig := probeHarnessWindowFn
	probeHarnessWindowFn = func(ctx context.Context, profile map[string]string) (harnessWindowProbe, error) {
		s.profiles = append(s.profiles, profile)
		if dl, ok := ctx.Deadline(); ok {
			s.deadlines = append(s.deadlines, time.Until(dl))
		}
		return p, err
	}
	t.Cleanup(func() { probeHarnessWindowFn = orig })
	return s
}

// cwFactory is a registry holding the declaring claude- profile, with the harness at version.
func cwFactory(t *testing.T, version string) string {
	t.Helper()
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{wdProfile: {"ANTHROPIC_MODEL": wdModel, config.EnvMaxContextTokens: fmt.Sprint(wdDeclared)}},
	})
	wdStubVersion(t, version)
	resetWindowPass()
	t.Cleanup(resetWindowPass)
	return root
}

func cwProbed(window int64) harnessWindowProbe {
	return harnessWindowProbe{ModelID: wdModel, ContextWindow: window, HarnessVersion: wdNew}
}

func cwRequireLine(t *testing.T, out, line string) {
	t.Helper()
	if !strings.Contains("\n"+out, "\n"+line+"\n") {
		t.Errorf("check output lacks the line\n  %s\nout:\n%s", line, out)
	}
}

func cwRequireNoRecord(t *testing.T, root string) {
	t.Helper()
	if data, err := os.ReadFile(wdRecordPath(root, wdProfile)); err == nil {
		t.Errorf("a probe that measured nothing stored a record:\n%s", data)
	}
}

func cwRequireProbeEntry(t *testing.T, root string, window int64) {
	t.Helper()
	rec, ok := wdEntry(t, root, wdProfile, wdKey(wdNew, wdDeclared, wdModel))
	if !ok {
		t.Fatalf("check stored no entry under %q", wdKey(wdNew, wdDeclared, wdModel))
	}
	if rec.Applied != window || rec.Probed != window || rec.Source != "probe" || rec.ModelID != wdModel || rec.Sessions != 0 {
		t.Errorf("stored entry = %+v, want applied = probed = %d, source probe, model %q, sessions 0", rec, window, wdModel)
	}
}

func TestConfigModelsCheck_WindowVerdict(t *testing.T) {
	differs := fmt.Sprintf(`profile %q: Claude Code %s applies a %d-token window to model %q, but the profile declares %d`, wdProfile, wdNew, 200000, wdModel, wdDeclared)

	t.Run("unrecognised id fails", func(t *testing.T) {
		cwFactory(t, wdNew)
		p := cwProbed(200000)
		p.Unrecognized = true
		cwStubProbe(t, p, nil)
		out, err := runModelsCmd(t, runConfigModelsCheck, wdProfile)
		if err == nil || err.Error() != "1 profile check(s) failed" {
			t.Errorf("check of an unrecognised id = %v, want \"1 profile check(s) failed\"", err)
		}
		cwRequireLine(t, out, fmt.Sprintf(`profile %q: Claude Code %s does not recognise model %q (window %d)`, wdProfile, wdNew, wdModel, 200000))
	})

	t.Run("window differing from declaration fails", func(t *testing.T) {
		root := cwFactory(t, wdNew)
		cwStubProbe(t, cwProbed(200000), nil)
		out, err := runModelsCmd(t, runConfigModelsCheck, wdProfile)
		if err == nil {
			t.Errorf("check of a window short of the declaration exited 0:\n%s", out)
		}
		cwRequireLine(t, out, differs)
		cwRequireProbeEntry(t, root, 200000)
	})

	t.Run("agreeing window passes", func(t *testing.T) {
		root := cwFactory(t, wdNew)
		s := cwStubProbe(t, cwProbed(wdDeclared), nil)
		out, err := runModelsCmd(t, runConfigModelsCheck, wdProfile)
		if err != nil {
			t.Errorf("check of an agreeing window = %v, want exit 0\n%s", err, out)
		}
		cwRequireLine(t, out, fmt.Sprintf(`profile %q: window %d on Claude Code %s (model %q)`, wdProfile, wdDeclared, wdNew, wdModel))
		cwRequireProbeEntry(t, root, wdDeclared)
		if len(s.deadlines) != 1 || s.deadlines[0] <= 0 || s.deadlines[0] > harnessProbeCheckTimeout {
			t.Errorf("the probe ran with deadlines %v, want one within harnessProbeCheckTimeout (%v)", s.deadlines, harnessProbeCheckTimeout)
		}
		if strings.Contains(out, "nothing to probe") {
			t.Errorf("a not-foreign profile with no base URL still printed a nothing-to-probe line:\n%s", out)
		}
	})

	t.Run("claude absent is a soft line", func(t *testing.T) {
		root := cwFactory(t, wdNew)
		cwStubProbe(t, harnessWindowProbe{}, errHarnessAbsent)
		out, err := runModelsCmd(t, runConfigModelsCheck, wdProfile)
		if err != nil {
			t.Errorf("check with no claude on PATH = %v, want exit 0\n%s", err, out)
		}
		cwRequireLine(t, out, fmt.Sprintf(`profile %q: claude not on PATH — window not measured`, wdProfile))
		cwRequireNoRecord(t, root)
	})

	t.Run("probe error is a hard line", func(t *testing.T) {
		root := cwFactory(t, wdNew)
		cwStubProbe(t, harnessWindowProbe{}, context.DeadlineExceeded)
		out, err := runModelsCmd(t, runConfigModelsCheck, wdProfile)
		if err == nil {
			t.Errorf("check whose probe timed out exited 0:\n%s", out)
		}
		cwRequireLine(t, out, fmt.Sprintf(`profile %q: window probe did not complete (%v) — window not measured`, wdProfile, context.DeadlineExceeded))
		cwRequireNoRecord(t, root)
	})

	t.Run("probe on another version is a hard line", func(t *testing.T) {
		root := cwFactory(t, wdOld)
		cwStubProbe(t, cwProbed(wdDeclared), nil)
		out, err := runModelsCmd(t, runConfigModelsCheck, wdProfile)
		if err == nil {
			t.Errorf("check whose probe ran another harness version exited 0:\n%s", out)
		}
		var line string
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, fmt.Sprintf(`profile %q: window probe did not complete (`, wdProfile)) {
				line = l
			}
		}
		if line == "" || !strings.HasSuffix(line, ") — window not measured") ||
			!strings.Contains(line, `"`+wdNew+`"`) || !strings.Contains(line, `"`+wdOld+`"`) {
			t.Errorf("want the did-not-complete line naming both %q and %q, got %q\nout:\n%s", wdNew, wdOld, line, out)
		}
		cwRequireNoRecord(t, root)
	})

	t.Run("foreign profile not probed", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeValidModels(t, root, &config.ModelsConfig{
			Models: map[string]map[string]string{
				"codex": {
					"ANTHROPIC_MODEL":      "gpt-5.6-sol",
					"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
					"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
				},
				wdProfile: {"ANTHROPIC_MODEL": wdModel, config.EnvMaxContextTokens: fmt.Sprint(wdDeclared)},
			},
		})
		writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
		origHTTP := httpProbe
		httpProbe = func(baseURL, authToken string) ([]string, error) {
			return []string{"gpt-5.6-sol", wdModel}, nil
		}
		t.Cleanup(func() { httpProbe = origHTTP })
		wdStubVersion(t, wdNew)
		resetWindowPass()
		t.Cleanup(resetWindowPass)
		s := cwStubProbe(t, cwProbed(wdDeclared), nil)

		out, _ := runModelsCmd(t, runConfigModelsCheck, "codex")
		if len(s.profiles) != 0 {
			t.Errorf("check codex probed the harness for a foreign profile: %v\n%s", s.profiles, out)
		}
		out, _ = runModelsCmd(t, runConfigModelsCheck)
		if len(s.profiles) != 1 || s.profiles[0]["ANTHROPIC_MODEL"] != wdModel {
			t.Errorf("the sweep probed %v, want exactly the not-foreign profile %q once\n%s", s.profiles, wdProfile, out)
		}
	})

	t.Run("foreign profile with no base URL prints its own line", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeValidModels(t, root, &config.ModelsConfig{
			Models: map[string]map[string]string{"sol": {"ANTHROPIC_MODEL": "gpt-5.6-sol"}},
		})
		wdStubVersion(t, wdNew)
		resetWindowPass()
		t.Cleanup(resetWindowPass)
		s := cwStubProbe(t, cwProbed(wdDeclared), nil)
		out, err := runModelsCmd(t, runConfigModelsCheck, "sol")
		if err != nil {
			t.Errorf("check of a foreign profile with no base URL = %v, want exit 0\n%s", err, out)
		}
		cwRequireLine(t, out, `profile "sol": foreign-class model id and no ANTHROPIC_BASE_URL — nothing to probe`)
		if strings.Contains(out, "(local/default model)") {
			t.Errorf("check still prints the (local/default model) line:\n%s", out)
		}
		if len(s.profiles) != 0 {
			t.Errorf("a foreign profile was probed: %v", s.profiles)
		}
	})

	t.Run("verdict judges the probe when a session row is kept", func(t *testing.T) {
		root := cwFactory(t, wdNew)
		key := wdKey(wdNew, wdDeclared, wdModel)
		wdPlantRecord(t, root, wdProfile, map[string]windowRecord{key: wgShortfall(wdNew, wdDeclared, wdDeclared, wdModel, "scout")})
		before, err := os.ReadFile(wdRecordPath(root, wdProfile))
		if err != nil {
			t.Fatal(err)
		}
		cwStubProbe(t, cwProbed(200000), nil)
		out, err := runModelsCmd(t, runConfigModelsCheck, wdProfile)
		if err == nil {
			t.Errorf("check whose probe measured 200000 against a declared %d exited 0 because a session row said otherwise:\n%s", wdDeclared, out)
		}
		cwRequireLine(t, out, differs+fmt.Sprintf(" (the record keeps the session figure %d)", wdDeclared))
		if after, _ := os.ReadFile(wdRecordPath(root, wdProfile)); string(after) != string(before) {
			t.Errorf("the session row changed:\n before %s\n after  %s", before, after)
		}
	})

	t.Run("failed record write keeps the verdict", func(t *testing.T) {
		root := cwFactory(t, wdNew)
		if err := os.MkdirAll(filepath.Dir(wdWindowsDir(root)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wdWindowsDir(root), []byte("not a directory\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cwStubProbe(t, cwProbed(200000), nil)
		out, err := runModelsCmd(t, runConfigModelsCheck, wdProfile)
		if err == nil {
			t.Errorf("an unstorable short window exited 0; the verdict must stand whether or not it is stored:\n%s", out)
		}
		verdict := strings.Index(out, differs+"\n")
		warning := strings.Index(out, fmt.Sprintf(`warning: window record for profile %q could not be written (`, wdProfile))
		if verdict < 0 || warning < verdict {
			t.Errorf("want the differs verdict followed by the could-not-be-written warning (verdict at %d, warning at %d):\n%s", verdict, warning, out)
		}
	})
}
