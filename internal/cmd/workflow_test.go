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

	t.Run("the scan accepts the healing loop's additions", func(t *testing.T) {
		for name, mutated := range map[string]string{
			"a job-level permissions block and a guarded second step": strings.Replace(adScheduleFixture,
				"    steps:\n      - run: .github/advisory-audit.sh\n",
				"    permissions:\n      contents: read\n      issues: write\n    steps:\n      - run: .github/advisory-audit.sh\n"+
					"      - name: install gh\n        if: failure() && github.repository == 'stempeck/agentfactory-pro'\n        run: .github/install-gh.sh\n"+
					"      - name: file healing issues\n        if: failure() && github.repository == 'stempeck/agentfactory-pro'\n        run: .github/advisory-issue.sh\n", 1),
			"workflow_dispatch inputs before the schedule": strings.Replace(adScheduleFixture, "  schedule:\n",
				"  workflow_dispatch:\n    inputs:\n      advisory_dry_run:\n        description: \"Print the healing issues the advisory-decay job would file, and file none\"\n"+
					"        type: boolean\n        default: false\n  schedule:\n", 1),
		} {
			if mutated == adScheduleFixture {
				t.Fatalf("%s: the mutation did not apply to the fixture", name)
			}
			if v := adScheduleViolations(mutated); len(v) != 0 {
				t.Errorf("%s: rejected: %v", name, v)
			}
		}
	})
}

const (
	adRecordEnv    = "ADVISORY_AUDIT_RECORD: ${{ runner.temp }}/advisory-record.json"
	adFilingStepIf = "        if: failure() && github.repository == 'stempeck/agentfactory-pro' && (github.event_name == 'schedule' || (github.event_name == 'workflow_dispatch' && inputs.advisory_dry_run))"
	adAuditStep    = "name: advisory audit (every pin against OSV, the vendor advisories and the exception register)\n" +
		"        env:\n          " + adRecordEnv + "\n        run: .github/advisory-audit.sh"
)

// The filing step turns a red daily run into healing issues; these are the properties that keep it
// from filing on a PR run, from losing the audit's record, or from widening the token's scope.
func TestWorkflowAdvisoryIssueStep(t *testing.T) {
	raw := readRepoFile(t, ".github/workflows/test.yml")
	code := cvCode(raw)

	start := strings.Index(code, "\n  advisory-decay:\n")
	if start == -1 {
		t.Fatal("test.yml has no advisory-decay job")
	}
	job := code[start+1:]
	if end := regexp.MustCompile(`(?m)^  \S`).FindAllStringIndex(job, 2); len(end) == 2 {
		job = job[:end[1][0]]
	}
	var steps []string
	if i := strings.Index(job, "\n    steps:\n"); i != -1 {
		for _, s := range strings.Split(job[i+len("\n    steps:\n"):], "\n      - ") {
			if strings.TrimSpace(s) != "" {
				steps = append(steps, s)
			}
		}
	}
	stepWith := func(needle string) int {
		for i, s := range steps {
			if strings.Contains(s, needle) {
				return i
			}
		}
		return -1
	}
	audit, install, filing := stepWith("run: .github/advisory-audit.sh"), stepWith("name: install gh"), stepWith("name: file healing issues")
	if install == -1 {
		t.Errorf("test.yml has no \"install gh\" step in the advisory-decay job; steps: %q", steps)
	}
	if filing == -1 {
		t.Fatalf("test.yml has no \"file healing issues\" step in the advisory-decay job; steps: %q", steps)
	}
	if audit == -1 {
		t.Fatalf("the advisory-decay job no longer runs .github/advisory-audit.sh; steps: %q", steps)
	}
	if len(steps) != 4 {
		t.Errorf("the advisory-decay job has %d steps, want 4 (checkout, audit, install gh, file healing issues)", len(steps))
	}
	if !(audit < install && install < filing) {
		t.Errorf("step order audit=%d install=%d filing=%d, want the audit, then the gh install, then the filing", audit, install, filing)
	}

	t.Run("the audit step is unchanged", func(t *testing.T) {
		if got := strings.TrimSpace(steps[audit]); got != adAuditStep {
			t.Errorf("the audit step changed (#755 changes only what filing needs):\ngot:\n%s\nwant:\n%s", got, adAuditStep)
		}
	})

	t.Run("gh is installed whenever the filing step runs", func(t *testing.T) {
		if install == -1 {
			t.Fatal("no install gh step")
		}
		for _, want := range []string{adFilingStepIf + "\n", "run: .github/install-gh.sh"} {
			if !strings.Contains(steps[install], want) {
				t.Errorf("the install gh step lacks %q; got:\n%s", strings.TrimSpace(want), steps[install])
			}
		}
		if strings.Contains(steps[install], "ADVISORY_AUDIT_RECORD") {
			t.Error("the install gh step carries the audit record; only the audit and the filer touch it")
		}
	})

	t.Run("the pro repository is the only repository test", func(t *testing.T) {
		tests := regexp.MustCompile(`github\.repository\s*[=!]=\s*'[^']*'`).FindAllString(code, -1)
		if want := "github.repository == 'stempeck/agentfactory-pro'"; len(tests) != 2 || tests[0] != want || tests[1] != want {
			t.Errorf("repository tests = %q, want exactly two %q (the install and filing steps), so the published copy skips both", tests, want)
		}
	})

	t.Run("both steps name the same record path", func(t *testing.T) {
		if n := strings.Count(raw, adRecordEnv); n != 2 {
			t.Errorf("%q appears %d times, want 2 (the audit step writes it, the filing step reads it)", adRecordEnv, n)
		}
		for _, i := range []int{audit, filing} {
			if !strings.Contains(steps[i], adRecordEnv) {
				t.Errorf("step %q lacks %q", strings.SplitN(steps[i], "\n", 2)[0], adRecordEnv)
			}
		}
	})

	t.Run("the filing step runs only on a failed daily or dry run of this repository", func(t *testing.T) {
		if !strings.Contains(steps[filing], adFilingStepIf+"\n") {
			t.Errorf("the filing step lacks the line\n%s\ngot:\n%s", adFilingStepIf, steps[filing])
		}
	})

	t.Run("issues write is granted to the advisory-decay job only", func(t *testing.T) {
		if n := strings.Count(raw, "issues: write"); n != 1 {
			t.Errorf("test.yml names \"issues: write\" %d times (comments included), want exactly 1", n)
		}
		if !strings.Contains(job, "\n    permissions:\n      contents: read\n      issues: write\n") {
			t.Error("the advisory-decay job lacks a job-level permissions block of contents: read and issues: write")
		}
		top := regexp.MustCompile(`(?m)^permissions:\n((?:  .*\n)*)`).FindStringSubmatch(code)
		if top == nil || top[1] != "  contents: read\n" {
			t.Errorf("the workflow-level permissions block must stay contents: read only; got %q", top)
		}
	})

	t.Run("no step can hide its failure", func(t *testing.T) {
		if strings.Contains(steps[audit], "continue-on-error") {
			t.Error("the audit step carries continue-on-error; failure() would never fire the filing step")
		}
		for _, i := range []int{install, filing} {
			if i != -1 && strings.Contains(steps[i], "continue-on-error") {
				t.Errorf("step %q carries continue-on-error; a red run must stay red until its heal merges", strings.SplitN(steps[i], "\n", 2)[0])
			}
		}
	})

	t.Run("workflow_dispatch stays a bare key with the dry-run input", func(t *testing.T) {
		if !regexp.MustCompile(`(?m)^  workflow_dispatch:$`).MatchString(raw) {
			t.Error("`  workflow_dispatch:` is no longer a bare key line")
		}
		in := regexp.MustCompile(`(?m)^  workflow_dispatch:\n    inputs:\n      advisory_dry_run:\n((?:        .*\n)*)`).FindStringSubmatch(code)
		if in == nil {
			t.Fatal("workflow_dispatch has no inputs.advisory_dry_run block")
		}
		for _, want := range []string{"        type: boolean\n", "        default: false\n"} {
			if !strings.Contains(in[1], want) {
				t.Errorf("inputs.advisory_dry_run lacks %q; got:\n%s", want, in[1])
			}
		}
	})

	t.Run("the filing step's labels, dry-run switch and script", func(t *testing.T) {
		for _, want := range []string{
			"ADVISORY_ISSUE_LABELS: agentic,advisory-heal\n",
			"ADVISORY_DEDUPE_LABEL: advisory-heal\n",
			"ADVISORY_ISSUE_DRY_RUN: ${{ inputs.advisory_dry_run && '1' || '' }}\n",
			"run: .github/advisory-issue.sh\n",
		} {
			if !strings.Contains(steps[filing], want) {
				t.Errorf("the filing step lacks %q", strings.TrimSuffix(want, "\n"))
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
