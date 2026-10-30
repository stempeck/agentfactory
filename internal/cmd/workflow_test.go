package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestWorkflowSupplyChainLint(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "test.yml"))
	if err != nil {
		t.Fatalf("reading test.yml: %v", err)
	}
	content := string(data)

	t.Run("supply_chain_lint_job_exists", func(t *testing.T) {
		if !strings.Contains(content, "supply-chain-lint:") {
			t.Error("test.yml missing supply-chain-lint job")
		}
	})

	t.Run("pip_require_hashes_check_step_exists", func(t *testing.T) {
		if !strings.Contains(content, "require-hashes") {
			t.Error("test.yml missing pip --require-hashes check step")
		}
	})

	// #743 Phase 2 tightens the pip lines and adds TestPipLines, but the lint job itself is kept
	// byte-for-byte (D-40); the only line it may gain is the per-job schedule gate every existing
	// job gets.
	t.Run("supply_chain_lint_job_is_unchanged", func(t *testing.T) {
		start := strings.Index(content, "\n  supply-chain-lint:\n")
		if start == -1 {
			t.Fatal("test.yml has no top-level supply-chain-lint job")
		}
		block := content[start+1:]
		if end := strings.Index(block, "\n\n  "); end != -1 {
			block = block[:end+1]
		}
		block = strings.Replace(block, "    if: github.event_name != 'schedule'\n", "", 1)
		// The OSS publish retargets every job onto GitHub-hosted runners.
		block = strings.Replace(block, "    runs-on: ubuntu-latest\n", "    runs-on: self-hosted\n", 1)
		if block != supplyChainLintJob {
			t.Errorf("the supply-chain-lint job changed; want exactly:\n%s\ngot:\n%s", supplyChainLintJob, block)
		}
	})
}

const supplyChainLintJob = `  supply-chain-lint:
    name: supply-chain-lint
    runs-on: self-hosted
    steps:
      - uses: actions/checkout@v4
      - name: verify pip installs use --require-hashes
        run: |
          FAILURES=0
          for f in $(grep -rl 'pip.*install' --include='*.sh' . --exclude-dir=.designs --exclude-dir=.agentfactory) Dockerfile Makefile; do
            if [ -f "$f" ] && grep -q 'pip.*install' "$f" && ! grep -q 'require-hashes' "$f"; then
              echo "ERROR: $f has pip install without --require-hashes"
              FAILURES=$((FAILURES + 1))
            fi
          done
          exit $FAILURES
`

// The daily run is what gives the exception register's recheck_by dates teeth: it fails CI on a
// lapsed entry or a newly published advisory without any commit. Every other lane is gated off it.
func TestWorkflowAdvisoryDecaySchedule(t *testing.T) {
	t.Run("the shipped workflow", func(t *testing.T) {
		for _, v := range adScheduleViolations(readRepoFile(t, ".github/workflows/test.yml")) {
			t.Error(v)
		}
	})

	t.Run("the scan rejects each way the schedule loses its teeth", func(t *testing.T) {
		if v := adScheduleViolations(adScheduleFixture); len(v) != 0 {
			t.Fatalf("the well-formed fixture was rejected: %v", v)
		}
		guard := "    if: github.event_name != 'schedule'\n"
		for name, mutated := range map[string]string{
			"cron deleted":                    strings.Replace(adScheduleFixture, "    - cron: '17 5 * * *'\n", "", 1),
			"schedule trigger deleted":        strings.Replace(adScheduleFixture, "  schedule:\n    - cron: '17 5 * * *'\n", "", 1),
			"a lane loses its guard":          strings.Replace(adScheduleFixture, guard, "", 1),
			"a lane is added without a guard": adScheduleFixture + "\n  newlane:\n    runs-on: self-hosted\n    steps:\n      - run: true\n",
			"advisory-decay gains the guard":  strings.Replace(adScheduleFixture, "  advisory-decay:\n", "  advisory-decay:\n"+guard, 1),
			"advisory-decay is deleted":       adScheduleFixture[:strings.Index(adScheduleFixture, "  advisory-decay:\n")],
		} {
			if mutated == adScheduleFixture {
				t.Fatalf("%s: the mutation did not apply to the fixture", name)
			}
			if len(adScheduleViolations(mutated)) == 0 {
				t.Errorf("%s: accepted", name)
			}
		}
	})
}

const adScheduleFixture = `on:
  push:
    branches: [main]
  pull_request:
  schedule:
    - cron: '17 5 * * *'

jobs:
  unit:
    runs-on: self-hosted
    if: github.event_name != 'schedule'
    steps:
      - run: make test

  regen:
    runs-on: self-hosted
    if: github.event_name != 'schedule'
    steps:
      - if: always()
        run: make check-regen

  advisory-decay:
    runs-on: self-hosted
    # No ` + "`if:`" + ` here: this is the job the schedule exists for.
    steps:
      - run: .github/advisory-audit.sh
`

var (
	adTopLevelKeyRe = regexp.MustCompile(`^[^\s#][^:]*:`)
	adJobHeaderRe   = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	adJobIfRe       = regexp.MustCompile(`^    if:\s*(.*?)\s*$`)
	adCronRe        = regexp.MustCompile(`^\s+- cron:\s*\S`)
)

// adScheduleViolations reads the workflow as text (go.mod carries no YAML parser): the `on:` block must
// schedule a cron, every job but advisory-decay must carry the job-level schedule guard, and
// advisory-decay must carry no job-level `if:` at all.
func adScheduleViolations(workflow string) []string {
	blocks := map[string][]string{}
	top := ""
	for _, line := range strings.Split(cvCode(workflow), "\n") {
		if adTopLevelKeyRe.MatchString(line) {
			top = strings.TrimSpace(line[:strings.Index(line, ":")])
		}
		if top != "" {
			blocks[top] = append(blocks[top], line)
		}
	}

	var out []string
	scheduled := false
	inSchedule := false
	for _, line := range blocks["on"] {
		switch {
		case strings.TrimSpace(line) == "schedule:" && strings.HasPrefix(line, "  schedule:"):
			inSchedule = true
		case inSchedule && strings.HasPrefix(line, "    "):
			scheduled = scheduled || adCronRe.MatchString(line)
		case strings.TrimSpace(line) != "":
			inSchedule = false
		}
	}
	if !scheduled {
		out = append(out, "the on: block has no schedule trigger with a cron entry")
	}

	jobIf := map[string][]string{}
	var jobs []string
	job := ""
	for _, line := range blocks["jobs"] {
		if m := adJobHeaderRe.FindStringSubmatch(line); m != nil {
			job = m[1]
			jobs = append(jobs, job)
			jobIf[job] = nil
			continue
		}
		if m := adJobIfRe.FindStringSubmatch(line); m != nil && job != "" {
			jobIf[job] = append(jobIf[job], m[1])
		}
	}
	if _, ok := jobIf["advisory-decay"]; !ok {
		out = append(out, "there is no advisory-decay job for the schedule to run")
	}
	for _, j := range jobs {
		conds := jobIf[j]
		if j == "advisory-decay" {
			if len(conds) != 0 {
				out = append(out, fmt.Sprintf("advisory-decay carries a job-level if: %q; the daily run must reach it", conds))
			}
			continue
		}
		if len(conds) != 1 || conds[0] != "github.event_name != 'schedule'" {
			out = append(out, fmt.Sprintf("job %s is not gated off the daily schedule (job-level if: %q)", j, conds))
		}
	}
	return out
}
