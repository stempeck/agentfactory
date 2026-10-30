package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var pinMirrorFiles = []string{
	"quickstart.sh",
	"py/litellm-requirements.txt",
	"USING_TELEMETRY.md",
	"USING_LITELLM.md",
	"Dockerfile",
	".github/workflows/test.yml",
}

var openobserveFixtureRe = regexp.MustCompile(`internal/telemetry/testdata/openobserve-(v[0-9.]+)/`)

// pinMirrorErrors checks the four places outside quickstart.sh that restate a K6 or LiteLLM pin and
// that no other test compares (D-38). Each mirror must name its pin at least once, and only ever that
// value; a mirror that lost its mention would otherwise pass vacuously.
func pinMirrorErrors(files map[string]string) []string {
	qs := files["quickstart.sh"]
	pin := func(name string) string {
		m := regexp.MustCompile(`(?m)^`+name+`="([^"$]+)"$`).FindAllStringSubmatch(qs, -1)
		if len(m) != 1 {
			return ""
		}
		return m[0][1]
	}
	var errs []string
	mirror := func(label, pinName, file string, re *regexp.Regexp) {
		want := pin(pinName)
		if want == "" {
			errs = append(errs, fmt.Sprintf("%s: quickstart.sh declares no single %s literal to mirror", label, pinName))
		}
		matches := re.FindAllStringSubmatch(files[file], -1)
		if len(matches) == 0 {
			errs = append(errs, fmt.Sprintf("%s: %s names no %s value", label, file, pinName))
		}
		for _, m := range matches {
			if want != "" && m[1] != want {
				errs = append(errs, fmt.Sprintf("%s: %s says %q, quickstart.sh pins %s=%q", label, file, m[1], pinName, want))
			}
		}
	}
	mirror("LITELLM", "LITELLM_VERSION", "py/litellm-requirements.txt",
		regexp.MustCompile(`(?m)^litellm(?:\[[^\]]*\])?==([^ ;\\\n]+)`))
	mirror("OPENOBSERVE", "OPENOBSERVE_VERSION", "USING_TELEMETRY.md", openobserveFixtureRe)
	mirror("CODEX", "CODEX_VERSION", "USING_LITELLM.md",
		regexp.MustCompile(`@openai/codex@([0-9]+\.[0-9]+\.[0-9]+)`))
	mirror("NODE_MAJOR", "NODE_MAJOR", "Dockerfile", regexp.MustCompile(`NODE_MAJOR=([0-9]+)`))
	mirror("NODE_MAJOR", "NODE_MAJOR", ".github/workflows/test.yml", regexp.MustCompile(`node-version: '([0-9]+)'`))
	return errs
}

func TestPinMirrors(t *testing.T) {
	t.Run("real mirrors agree with quickstart.sh", func(t *testing.T) {
		files := map[string]string{}
		for _, rel := range pinMirrorFiles {
			files[rel] = readRepoFile(t, rel)
		}
		for _, e := range pinMirrorErrors(files) {
			t.Error(e)
		}
		root := findModuleRoot(t)
		for _, m := range openobserveFixtureRe.FindAllStringSubmatch(files["USING_TELEMETRY.md"], -1) {
			dir := filepath.Join(root, "internal", "telemetry", "testdata", "openobserve-"+m[1])
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				t.Errorf("USING_TELEMETRY.md names the fixture dir %s, which does not exist", dir)
			}
		}
	})

	consistent := func() map[string]string {
		return map[string]string{
			"quickstart.sh": "LITELLM_VERSION=\"1.2.3\"\nOPENOBSERVE_VERSION=\"v9.8.7\"\n" +
				"CODEX_VERSION=\"0.1.2\"\nNODE_MAJOR=\"40\"\n",
			"py/litellm-requirements.txt": "litellm==1.2.3 \\\n    --hash=sha256:00\n",
			"USING_TELEMETRY.md":          "fixtures: `internal/telemetry/testdata/openobserve-v9.8.7/`\n",
			"USING_LITELLM.md":            "npm pack @openai/codex@0.1.2\n",
			"Dockerfile":                  "RUN NODE_MAJOR=40 && \\\n    echo node_${NODE_MAJOR}.x\n",
			".github/workflows/test.yml":  "          node-version: '40'\n",
		}
	}

	t.Run("synthetic consistent mirrors pass", func(t *testing.T) {
		if errs := pinMirrorErrors(consistent()); len(errs) != 0 {
			t.Fatalf("a consistent synthetic tree must pass; got %q", errs)
		}
	})

	mismatches := []struct {
		name, label, file, from, to string
	}{
		{"mismatched litellm lock line", "LITELLM", "py/litellm-requirements.txt", "litellm==1.2.3", "litellm==1.2.4"},
		{"mismatched telemetry fixture dir", "OPENOBSERVE", "USING_TELEMETRY.md", "openobserve-v9.8.7/", "openobserve-v9.8.6/"},
		{"mismatched codex doc version", "CODEX", "USING_LITELLM.md", "@openai/codex@0.1.2", "@openai/codex@0.1.3"},
		{"unversioned codex doc mention", "CODEX", "USING_LITELLM.md", "npm pack @openai/codex@0.1.2", "npm pack @openai/codex"},
		{"mismatched Dockerfile node major", "NODE_MAJOR", "Dockerfile", "NODE_MAJOR=40", "NODE_MAJOR=39"},
		{"mismatched CI node-version", "NODE_MAJOR", ".github/workflows/test.yml", "node-version: '40'", "node-version: '41'"},
	}
	for _, m := range mismatches {
		t.Run("synthetic "+m.name+" is reported", func(t *testing.T) {
			files := consistent()
			files[m.file] = strings.Replace(files[m.file], m.from, m.to, 1)
			errs := pinMirrorErrors(files)
			if len(errs) != 1 || !strings.HasPrefix(errs[0], m.label+":") {
				t.Fatalf("rewriting %q to %q must yield one %s report; got %q", m.from, m.to, m.label, errs)
			}
		})
	}
}
