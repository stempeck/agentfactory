# Using Agentfactory: Advisory Healing Loops

Operator guide for the advisory healing loop: a red `advisory-decay` run files one issue per pin, and
a three-phase workflow on the dispatcher heals each one into a pull request for the operator to merge.
Split out of [USING_AGENTFACTORY.md](USING_AGENTFACTORY.md), whose Workflows section describes the
engine the loop runs on.

## Advisory healing loop

In the repository the `advisory-decay` job's filing steps name in their `if:`, a red daily
`advisory-decay` run heals itself. When a scheduled run of that job fails, its last two steps run.
`.github/install-gh.sh` installs the `gh` release `quickstart.sh` pins (`GH_VERSION`, checked
against its `GH_SHA256_*` sum) into the runner's temporary directory, because the self-hosted
runners carry no `gh`. `.github/advisory-issue.sh` then reads the audit's finding record and files
one issue per pin a pull request changes. A finding is keyed by class and `package@version`, and the
findings that come from one pin share its issue: Go `stdlib` and `toolchain` (both
`GO_TOOLCHAIN_VERSION`), npm `playwright` and `playwright-core` at one version, and every class of
one key. The title is `advisory-decay: <class> <key>` (for example
`advisory-decay: unexcused oauthlib@4.0.0`), or the pin's `<class> <key>` pairs joined by commas.
The labels are `agentic` and `advisory-heal`, and the issue is locked as soon as it is created, so
only collaborators can add to what the healing agents read. The classes are `unexcused`, `expired`,
`node-behind` and `excused-fixable`. An open `advisory-heal` issue for the same pin is never filed
twice: the run prints that it is already open and moves on, naming any finding of that pin the open
issue does not mark, which is filed once that issue closes. The body names the findings, every file
and test row a move of the pin has to change, what fixes it, the other pins of the same run (each
with its own issue; a move this pin forces on one of them is kept and listed in the pull request)
and the definition of done, which includes `make test` passing on the branch.

The loop uses five labels, all of which must exist on that repository:

| Label | Role |
|-------|------|
| `agentic` | The dispatcher's `trigger_label`; the dispatcher removes it once the pipeline ends |
| `advisory-heal` | The workflow label that starts the pipeline; it stays on the issue, so `gh issue list --label advisory-heal --state all` is the loop's ledger |
| `heal-implement` | Phase 1: fable-implement opens the fix pull request, whose body starts `Resolves <owner>/<repo>#N` |
| `pr-review` | Phase 2: fable-review reviews that pull request |
| `pr-iterate` | Phase 3: fable-increment addresses the review on the same pull request |

fable-implement writes that `Resolves <owner>/<repo>#N` closing line for
every issue-link run on any repository, so the merge closes that issue.

The dispatcher of the factory that watches that repository runs the pipeline. These are its entries
in `.agentfactory/dispatch.json`, beside those of any other workflow such as `feature-workflow`, with
`<owner>/<repo>` the repository the filing steps name:

```json
{
  "repos": ["<owner>/<repo>"],
  "trigger_label": "agentic",
  "notify_on_complete": "manager",
  "mappings": [
    {"labels": ["heal-implement"], "source": "issue", "agent": "fable-implement"},
    {"labels": ["pr-review"],      "source": "issue", "agent": "fable-review"},
    {"labels": ["pr-review"],      "source": "pr",    "agent": "fable-review"},
    {"labels": ["pr-iterate"],     "source": "issue", "agent": "fable-increment"},
    {"labels": ["pr-iterate"],     "source": "pr",    "agent": "fable-increment"}
  ],
  "interval_seconds": 300,
  "retry_after_seconds": 1800,
  "remove_trigger_after_dispatch": true,
  "workflows": [
    {"label": "advisory-heal", "phases": ["heal-implement", "pr-review", "pr-iterate"]}
  ]
}
```

Every phase resolves on its first single-label mapping, and all three of those are
`"source": "issue"`, so the workflow keeps the rule that every phase shares one `source` (Workflows
in [USING_AGENTFACTORY.md](USING_AGENTFACTORY.md)). Each `"source": "pr"` twin must stay after its
`"source": "issue"` twin (in the other order the config is rejected as mixed source), and
`feature-workflow` needs both twins too. With the `pr-review` twin the dispatcher waits
for exactly one pull request linked to the issue before `pr-review` and hands fable-review that pull
request's URL. With the `pr-iterate` twin the final phase consumes that pull request, so the pipeline
completes when fable-increment is done and the pull request is open or merged (see "A final phase
that consumes a pull request" under Workflows in [USING_AGENTFACTORY.md](USING_AGENTFACTORY.md)).

**The operator's single act.** When the final phase completes, the dispatcher removes `pr-iterate`
and `agentic` and mails `manager` (the `notify_on_complete` agent) `WORKFLOW_COMPLETE: advisory-heal`.
That mail is the cue to review the pull request and squash-merge it; GitHub then closes the issue.
No agent approves or merges. The pull request's own `advisory-decay` check stays red while another
pin's finding is open; that is expected, and the dispatcher does not wait for it. A pull request
that changes the exception register beyond what an agent may do on its own (adding an entry, moving
a `recheck_by` later, or adding or changing `fix_available`) starts its body with
`Operator acceptance required:`, and the operator's merge is the acceptance; moving a pin, deleting
an entry or shortening a `recheck_by` needs no such line. Where the agents and the operator share one
GitHub account, no second account can approve a pull request, so the operator approves a pull
request by merging it. That is a convention, not an enforced control; the same identity could merge
on its own, and no healing formula does.

**Where to look.** `af dispatch status --json` shows each issue's current phase (poll it more often
than the 300-second interval to see every phase); `.runtime/dispatch.log` in the factory root holds
every dispatch cycle's output, its `hold` and `drop` lines included; the `WORKFLOW_COMPLETE` mail
marks the end of a pipeline; the `advisory-decay` run's job summary has one line counting the issues
the run filed, found already open and failed to file, and says when the filing step stopped early;
and `gh issue list --label advisory-heal --state all` lists every issue the loop has filed, open or
closed.

**Stalls.** A pipeline that stalls mails `WORKFLOW_STALL: <workflow>` to `notify_on_complete` once
per new stall reason, and `af dispatch status --json` shows that reason as `stall` on its entry. A
heal whose pull request was closed without merging before `WORKFLOW_COMPLETE` stalls; to retry it,
close the issue, and the next scheduled run files a fresh one. A pull request closed after
`WORKFLOW_COMPLETE` is not seen again, because the dispatcher no longer watches the completed issue;
closing its issue is what makes the next scheduled run file a fresh one.

**When no fix is published.** If no fixed version above the pin is published, or the package is
`litellm` (whose pin moves only on a session-shaped probe, ADR-024), the issue asks instead for
an exception entry in `.github/advisory-exceptions.json` for exactly that `package@version`: a
reason, evidence citing the issue, `reviewed` set to today and `recheck_by` at most 30 days later.
For an `expired` `litellm` entry the agent renews it and keeps its `fix_available`; for an
`excused-fixable` `litellm` entry it adds the `fix_available` acknowledgement. Each of these pull
requests starts `Operator acceptance required:`. When an expired key's entries excuse an advisory whose
fix is published, the key gets one `expired` issue that names that fix: for `litellm` one pull request
renews the expired entries and adds the acknowledgement, with the same acceptance line; for any
other package the pin moves.

**Rehearsal.** `gh workflow run test.yml -f advisory_dry_run=true`
runs the job with `advisory_dry_run` set: the install step installs `gh`, and the filing step prints
every issue it would file and files none. It needs no labels, but it works only while the audit is
red, because both steps run only after the audit step fails. Check the result by the run's id and
its step log, not by an empty issue list. A manual run without `advisory_dry_run` never files.

**Enabling the loop, in order.**

1. In the factory's root run `git pull --ff-only && GOTOOLCHAIN=local make install` as one command,
   so the dispatcher never runs the new entries with a binary that predates them.
2. Confirm that `af dispatch status --json` reports `"dispatcher_running": true`; if it does not, run
   `af dispatch start`.
3. Rehearse with the dry run above while the audit is still red.
4. Watch the first scheduled run (05:17 UTC) file its issues; a re-run of an older
   scheduled run uses that run's old commit and cannot file. Follow each issue through the phases
   (`af dispatch status --json`, `.runtime/dispatch.log`) until `WORKFLOW_COMPLETE` arrives. Then
   check that the pull request's first body line begins with `Resolves <owner>/<repo>#N`,
   that its `advisory-decay` check (select it by name) is green or red only for other pins that have
   their own issues, and that the register diff matches what the issue asked for; then squash-merge
   it.
