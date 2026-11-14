package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestQuickstartLitellmHeredoc_SeedOnlyWhenAbsent keeps the rerun guard that used to ride
// inside the alias test, so retargeting that test at the seeded litellm.yaml does not drop it. It runs
// the gate instead of matching its text, because the gate's spelling differs across the merge this
// PR is waiting on (`[ ! -f … ]` here, main's `_ensure_mode_yaml "api-key"`) while the property —
// seed when absent, never touch an operator's file — is what both must keep.
func TestQuickstartLitellmHeredoc_SeedOnlyWhenAbsent(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH; the gate cannot be executed here (CI provisions it)")
	}
	root := findModuleRoot(t)
	qs, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	setup := setupLitellmSource(t, root)
	const seedStart = `cat > ".agentfactory/litellm.yaml" << 'EOF'`
	s := strings.Index(setup, seedStart)
	if s < 0 {
		t.Fatalf("setup_litellm() no longer writes litellm.yaml with %q", seedStart)
	}
	before := strings.Split(strings.TrimRight(setup[:s], " \t\n"), "\n")
	gate := regexp.MustCompile(`^\s*if (.+); then$`).FindStringSubmatch(before[len(before)-1])
	if gate == nil {
		t.Fatalf("the seed heredoc is not the first statement of an `if …; then` gate (line before it: %q); a "+
			"rerun would overwrite an operator's edited litellm.yaml with no backup", before[len(before)-1])
	}
	names := []string{"log_info", "log_success", "log_warn", "log_error"}
	for _, word := range regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`).FindAllString(gate[1], -1) {
		if !slices.Contains(names, word) && extractShellFunction(string(qs), word) != "" {
			names = append(names, word)
		}
	}
	script := shellFnsFrom(t, names...) + "RED='' GREEN='' YELLOW='' BLUE='' NC=''\n" +
		"if " + gate[1] + "; then echo SEED; else echo KEEP; fi\n"

	const operatorYAML = "# operator-tuned gateway\nmodel_list:\n  - model_name: claude-opus-5\n" +
		"    litellm_params:\n      model: openai/gpt-operator-tuned\n"
	for _, c := range []struct {
		name, existing, want string
	}{
		{"absent_seeds", "", "SEED"},
		{"operator_file_kept", operatorYAML, "KEEP"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgDir := filepath.Join(dir, ".agentfactory")
			if err := os.MkdirAll(cfgDir, 0o755); err != nil {
				t.Fatal(err)
			}
			yamlPath := filepath.Join(cfgDir, "litellm.yaml")
			if c.existing != "" {
				if err := os.WriteFile(yamlPath, []byte(c.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("evaluating the gate `%s`: %v\n%s", gate[1], err, out)
			}
			if got := strings.TrimSpace(string(out)); got != c.want {
				t.Errorf("gate `%s` decided %q, want %q", gate[1], got, c.want)
			}
			if c.existing == "" {
				return
			}
			if got, err := os.ReadFile(yamlPath); err != nil || string(got) != c.existing {
				t.Errorf("the gate changed the operator's litellm.yaml (err %v):\n%s", err, got)
			}
			if entries, _ := os.ReadDir(cfgDir); len(entries) != 1 {
				t.Errorf("the gate wrote beside the operator's file: %v", entries)
			}
		})
	}
}

// shellPinModelKeys are the model exports configure_shell derives from the default profile.
var shellPinModelKeys = []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"}

func shellPinRequireBashJq(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not on PATH; configure_shell cannot be executed here (CI provisions both)")
		}
	}
}

func shellPinConfigureShellFns(t *testing.T) string {
	t.Helper()
	return shellFnsFrom(t, "log_info", "log_success", "log_step", "log_error", "configure_shell")
}

type shellPinShellRun struct {
	out    string
	err    error
	bashrc string
}

// shellPinRunConfigureShell runs configure_shell in a temp factory whose .agentfactory/models.json is
// `models` (nil = absent) under a temp HOME whose .bashrc starts as `priorRC` ("" = no file). The
// colour vars are set because quickstart.sh defines them at top level and `set -u` would otherwise
// abort the first log_* — a harness failure that would look like the guard firing.
func shellPinRunConfigureShell(t *testing.T, fns string, models []byte, priorRC string) shellPinShellRun {
	t.Helper()
	home, factory := t.TempDir(), t.TempDir()
	if models != nil {
		if err := os.MkdirAll(filepath.Join(factory, ".agentfactory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(factory, ".agentfactory", "models.json"), models, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rcPath := filepath.Join(home, ".bashrc")
	if priorRC != "" {
		if err := os.WriteFile(rcPath, []byte(priorRC), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("bash", "-c", fns+"RED='' GREEN='' YELLOW='' BLUE='' NC=''\nconfigure_shell\n")
	cmd.Dir = factory
	cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()

	rc, readErr := os.ReadFile(rcPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("reading .bashrc: %v", readErr)
	}
	// The temp paths carry the test name, so they are masked before any "output names X" match.
	redacted := strings.NewReplacer(home, "<HOME>", factory, "<FACTORY>").Replace(string(out))
	return shellPinShellRun{out: redacted, err: err, bashrc: string(rc)}
}

func shellPinExportLine(key, value string) string {
	return `export ` + key + `="${` + key + `:-` + value + `}"`
}

// shellPinMissingExports returns the keys whose `export K="${K:-v}"` line is absent from rc.
func shellPinMissingExports(rc string, want map[string]string) []string {
	var missing []string
	for key, value := range want {
		if !strings.Contains(rc, shellPinExportLine(key, value)+"\n") {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	return missing
}

func shellPinRegistryJSON(t *testing.T, profile map[string]string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"default": "direct",
		"models":  map[string]map[string]string{"direct": profile},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func shellPinMarkerProfile() map[string]string {
	return map[string]string{
		"ANTHROPIC_MODEL":                "sentinel-marker-main",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "sentinel-marker-opus",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "sentinel-marker-sonnet",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "sentinel-marker-haiku",
	}
}

func shellPinModelExports(profile map[string]string) map[string]string {
	want := map[string]string{}
	for _, key := range shellPinModelKeys {
		want[key] = profile[key]
	}
	return want
}

// TestQuickstartConfigureShell_ExportsDefaultProfileIDs pins value parity: what
// configure_shell writes is the default profile's ids, proven by execution rather than by text.
func TestQuickstartConfigureShell_ExportsDefaultProfileIDs(t *testing.T) {
	shellPinRequireBashJq(t)
	fns := shellPinConfigureShellFns(t)

	t.Run("marker_ids", func(t *testing.T) {
		profile := shellPinMarkerProfile()
		run := shellPinRunConfigureShell(t, fns, shellPinRegistryJSON(t, profile), "")
		if run.err != nil {
			t.Fatalf("configure_shell failed on a complete default profile: %v\n%s", run.err, run.out)
		}
		if missing := shellPinMissingExports(run.bashrc, shellPinModelExports(profile)); len(missing) > 0 {
			t.Errorf(".bashrc does not export the default profile's ids for %v:\n%s", missing, run.bashrc)
		}
	})

	t.Run("shipped_seed", func(t *testing.T) {
		path := filepath.Join(findModuleRoot(t), ".agentfactory", "models.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var reg config.ModelsConfig
		if err := json.Unmarshal(data, &reg); err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
		want := shellPinModelExports(reg.Models[reg.Default])
		for key, value := range want {
			if value == "" {
				t.Fatalf("the shipped default profile %q declares no %s, so parity would be vacuous", reg.Default, key)
			}
		}
		run := shellPinRunConfigureShell(t, fns, data, "")
		if run.err != nil {
			t.Fatalf("configure_shell failed on the shipped seed: %v\n%s", run.err, run.out)
		}
		if missing := shellPinMissingExports(run.bashrc, want); len(missing) > 0 {
			t.Errorf(".bashrc disagrees with the shipped default profile for %v (want %v):\n%s", missing, want, run.bashrc)
		}
	})
}

// TestQuickstartConfigureShell_LiteralDefaultIsCaught proves the parity check above would
// catch the reviewer's mutant: a literal that happens to equal a real id must still be flagged.
func TestQuickstartConfigureShell_LiteralDefaultIsCaught(t *testing.T) {
	shellPinRequireBashJq(t)
	fns := shellPinConfigureShellFns(t)
	derivedRe := regexp.MustCompile(`\$\{ANTHROPIC_MODEL:-\$[A-Za-z_][A-Za-z0-9_]*\}`)
	mutant := derivedRe.ReplaceAllLiteralString(fns, "${ANTHROPIC_MODEL:-claude-opus-5}")
	if mutant == fns {
		t.Fatal("could not plant the mutant: configure_shell's ANTHROPIC_MODEL echo no longer has the " +
			"${ANTHROPIC_MODEL:-$var} shape this mutation targets")
	}

	profile := shellPinMarkerProfile()
	run := shellPinRunConfigureShell(t, mutant, shellPinRegistryJSON(t, profile), "")
	missing := shellPinMissingExports(run.bashrc, shellPinModelExports(profile))
	if len(missing) != 1 || missing[0] != "ANTHROPIC_MODEL" {
		t.Errorf("the parity check reported %v on a configure_shell hardcoding claude-opus-5, want [ANTHROPIC_MODEL]:\n%s",
			missing, run.bashrc)
	}
}

// TestQuickstartConfigureShell_MissingClassKeyOmitsExport pins that a
// default profile may declare only ANTHROPIC_MODEL, so an undeclared or empty class gets no export
// line at all — never a `:-null` one — and the class export a previous block carried does not
// survive the rewrite.
func TestQuickstartConfigureShell_MissingClassKeyOmitsExport(t *testing.T) {
	shellPinRequireBashJq(t)
	fns := shellPinConfigureShellFns(t)
	previous, priorRC := shellPinPreviousBlock(t, fns)
	for _, class := range []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		for _, c := range []struct {
			name  string
			shape func(map[string]string)
		}{
			{"absent", func(p map[string]string) { delete(p, class) }},
			{"empty", func(p map[string]string) { p[class] = "" }},
		} {
			t.Run(class+"/"+c.name, func(t *testing.T) {
				profile := shellPinMarkerProfile()
				c.shape(profile)
				run := shellPinRunConfigureShell(t, fns, shellPinRegistryJSON(t, profile), priorRC)
				if run.err != nil {
					t.Fatalf("configure_shell failed on a default profile without %s: %v\n%s", class, run.err, run.out)
				}
				if strings.Contains(run.bashrc, "export "+class+"=") {
					t.Errorf(".bashrc still exports %s for a profile that declares none:\n%s", class, run.bashrc)
				}
				if strings.Contains(run.bashrc, previous) || strings.Contains(run.bashrc, "sentinel-previous-") {
					t.Errorf("the previous model block survived the rewrite:\n%s", run.bashrc)
				}
				declared := shellPinModelExports(profile)
				delete(declared, class)
				if missing := shellPinMissingExports(run.bashrc, declared); len(missing) > 0 {
					t.Errorf(".bashrc does not export the declared ids for %v:\n%s", missing, run.bashrc)
				}
				for _, bad := range []string{":-null", ":-}"} {
					if strings.Contains(run.bashrc, bad) {
						t.Errorf(".bashrc carries a %q export:\n%s", bad, run.bashrc)
					}
				}
				if !strings.Contains(run.out, class) {
					t.Errorf("output does not name the class it left undeclared:\n%s", run.out)
				}
			})
		}
	}
}

// TestQuickstartConfigureShell_MissingMainModelAborts pins the one key kept strict: without
// ANTHROPIC_MODEL there is nothing to export, so configure_shell stops and names the file and key.
func TestQuickstartConfigureShell_MissingMainModelAborts(t *testing.T) {
	shellPinRequireBashJq(t)
	fns := shellPinConfigureShellFns(t)
	absent := shellPinMarkerProfile()
	delete(absent, "ANTHROPIC_MODEL")
	empty := shellPinMarkerProfile()
	empty["ANTHROPIC_MODEL"] = ""
	for _, c := range []struct {
		name   string
		models []byte
	}{
		{"absent", shellPinRegistryJSON(t, absent)},
		{"empty", shellPinRegistryJSON(t, empty)},
		{"null", []byte(`{"default":"direct","models":{"direct":{"ANTHROPIC_MODEL":null,` +
			`"ANTHROPIC_DEFAULT_OPUS_MODEL":"sentinel-marker-opus","ANTHROPIC_DEFAULT_SONNET_MODEL":"sentinel-marker-sonnet"}}}`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := shellPinRunConfigureShell(t, fns, c.models, "")
			if run.err == nil {
				t.Error("configure_shell exited 0 on a default profile with no usable ANTHROPIC_MODEL, want non-zero")
			}
			for _, name := range []string{"models.json", "ANTHROPIC_MODEL", `the profile named by "default" declares ANTHROPIC_MODEL`} {
				if !strings.Contains(run.out, name) {
					t.Errorf("output does not name %s:\n%s", name, run.out)
				}
			}
			if strings.Contains(run.bashrc, "_MODEL") {
				t.Errorf(".bashrc gained a model export from an aborted derivation:\n%s", run.bashrc)
			}
		})
	}
}

// TestQuickstartConfigureShell_EverySeededProfileIsAValidDefault pins that an
// operator who points `default` at any profile the seed ships must get a working shell block, and
// each class line appears exactly when that profile declares the class.
func TestQuickstartConfigureShell_EverySeededProfileIsAValidDefault(t *testing.T) {
	shellPinRequireBashJq(t)
	fns := shellPinConfigureShellFns(t)
	path := filepath.Join(findModuleRoot(t), ".agentfactory", "models.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var reg map[string]any
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	var typed config.ModelsConfig
	if err := json.Unmarshal(data, &typed); err != nil {
		t.Fatalf("%s does not decode as a registry: %v", path, err)
	}
	names := make([]string, 0, len(typed.Models))
	for name := range typed.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			reg["default"] = name
			models, err := json.Marshal(reg)
			if err != nil {
				t.Fatal(err)
			}
			run := shellPinRunConfigureShell(t, fns, models, "")
			if run.err != nil {
				t.Fatalf("configure_shell failed with the seeded profile %q as default: %v\n%s", name, run.err, run.out)
			}
			if strings.Contains(run.bashrc, "null") {
				t.Errorf(".bashrc carries a null export:\n%s", run.bashrc)
			}
			profile := typed.Models[name]
			for _, key := range shellPinModelKeys {
				line := shellPinExportLine(key, profile[key]) + "\n"
				if declared := profile[key] != ""; declared != strings.Contains(run.bashrc, line) {
					t.Errorf("%s: declared=%v but .bashrc export line present=%v:\n%s",
						key, declared, !declared, run.bashrc)
				}
			}
		})
	}
}

// TestQuickstartConfigureShell_UnreadableRegistryAborts pins that an unreadable registry
// stops configure_shell with a message that tells the operator which file to fix.
func TestQuickstartConfigureShell_UnreadableRegistryAborts(t *testing.T) {
	shellPinRequireBashJq(t)
	fns := shellPinConfigureShellFns(t)
	noDefault, err := json.Marshal(map[string]any{
		"models": map[string]map[string]string{"direct": shellPinMarkerProfile()},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		models []byte
		remedy string
	}{
		{"malformed", []byte(`{"default": "direct", "models": {`), ""},
		{"no_default_key", noDefault, ""},
		{"absent", nil, "af install --init"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := shellPinRunConfigureShell(t, fns, c.models, "")
			if run.err == nil {
				t.Error("configure_shell exited 0, want non-zero")
			}
			for _, want := range []string{"models.json", c.remedy} {
				if !strings.Contains(run.out, want) {
					t.Errorf("output does not name %s:\n%s", want, run.out)
				}
			}
		})
	}
}

// TestQuickstartConfigureShell_FailedDerivationKeepsPreviousBlock pins the ordering: the
// reads run before the strip, so a failed derivation leaves the operator's previous block intact.
func TestQuickstartConfigureShell_FailedDerivationKeepsPreviousBlock(t *testing.T) {
	shellPinRequireBashJq(t)
	fns := shellPinConfigureShellFns(t)
	previous, priorRC := shellPinPreviousBlock(t, fns)

	missingMain := shellPinMarkerProfile()
	delete(missingMain, "ANTHROPIC_MODEL")
	cases := []struct {
		name   string
		models []byte
	}{
		{"malformed", []byte(`{"default": "direct", "models": {`)},
		{"missing_main_model", shellPinRegistryJSON(t, missingMain)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := shellPinRunConfigureShell(t, fns, c.models, priorRC)
			if !strings.Contains(run.bashrc, previous) {
				t.Errorf("the previous model block did not survive a failed derivation; .bashrc is now:\n%s\noutput:\n%s",
					run.bashrc, run.out)
			}
		})
	}
}

// shellPinPreviousBlock returns a complete model block as an earlier run would have written it, and
// a .bashrc holding it, using configure_shell's own marker strings.
func shellPinPreviousBlock(t *testing.T, fns string) (previous, priorRC string) {
	t.Helper()
	markerRe := regexp.MustCompile(`local (begin|end)_marker="([^"]+)"`)
	markers := map[string]string{}
	for _, m := range markerRe.FindAllStringSubmatch(fns, -1) {
		markers[m[1]] = m[2]
	}
	if markers["begin"] == "" || markers["end"] == "" {
		t.Fatalf("could not read begin/end marker strings from configure_shell: %v", markers)
	}
	previous = markers["begin"] + "\n" +
		shellPinExportLine("ANTHROPIC_MODEL", "sentinel-previous-main") + "\n" +
		shellPinExportLine("ANTHROPIC_DEFAULT_OPUS_MODEL", "sentinel-previous-opus") + "\n" +
		shellPinExportLine("ANTHROPIC_DEFAULT_SONNET_MODEL", "sentinel-previous-sonnet") + "\n" +
		shellPinExportLine("ANTHROPIC_DEFAULT_HAIKU_MODEL", "sentinel-previous-haiku") + "\n" +
		shellPinExportLine("CLAUDE_CODE_EFFORT_LEVEL", "xhigh") + "\n" +
		shellPinExportLine("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "1") + "\n" +
		markers["end"] + "\n"
	priorRC = "\n# Added by agentfactory quickstart\n" +
		`export PATH="$HOME/.local/bin:$HOME/go/bin:/usr/local/go/bin:$PATH"` + "\n" + previous
	return previous, priorRC
}

// TestQuickstartConfigureShell_EffortFromDefaultProfile pins that the effort export follows
// the default profile like the model exports do.
func TestQuickstartConfigureShell_EffortFromDefaultProfile(t *testing.T) {
	shellPinRequireBashJq(t)
	profile := shellPinMarkerProfile()
	profile["CLAUDE_CODE_EFFORT_LEVEL"] = "sentinel-marker-effort"
	run := shellPinRunConfigureShell(t, shellPinConfigureShellFns(t), shellPinRegistryJSON(t, profile), "")
	if run.err != nil {
		t.Fatalf("configure_shell failed on a complete default profile: %v\n%s", run.err, run.out)
	}
	if want := shellPinExportLine("CLAUDE_CODE_EFFORT_LEVEL", "sentinel-marker-effort"); !strings.Contains(run.bashrc, want+"\n") {
		t.Errorf(".bashrc lacks %s:\n%s", want, run.bashrc)
	}
}

// TestQuickstartConfigureShell_EffortFallsBackToXhigh pins the omission rule: effort is
// optional in a profile, so the literal survives exactly when the profile is silent.
func TestQuickstartConfigureShell_EffortFallsBackToXhigh(t *testing.T) {
	shellPinRequireBashJq(t)
	run := shellPinRunConfigureShell(t, shellPinConfigureShellFns(t), shellPinRegistryJSON(t, shellPinMarkerProfile()), "")
	if run.err != nil {
		t.Fatalf("configure_shell failed on a complete default profile: %v\n%s", run.err, run.out)
	}
	if want := shellPinExportLine("CLAUDE_CODE_EFFORT_LEVEL", "xhigh"); !strings.Contains(run.bashrc, want+"\n") {
		t.Errorf(".bashrc lacks %s:\n%s", want, run.bashrc)
	}
}
