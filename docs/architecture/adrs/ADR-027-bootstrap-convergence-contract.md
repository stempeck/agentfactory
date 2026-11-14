# ADR-027: every bootstrap run converges each installed component to its pin, and a required component that does not converge fails the run

**Status:** Proposed — pending operator ratification
**Date:** 2026-10-27 (written against commit `035dcf8a`; the commit that adds this ADR is recorded when it lands)

## Context

`quickstart.sh` installs the toolchain every factory runs on, and `af install --agents` and
`af plugin install` run it again on every redeploy (`installAgentsPipeline`,
`internal/cmd/install.go:955`, `:1387`; `internal/cmd/plugin.go:1008`). Before this
decision a component that was already present was left as it was: `install_claude` returned as soon
as any `claude` ran (`quickstart.sh:370-373` at `de5f7e97`), and the Codex CLI was kept whenever it
was on PATH (`quickstart.sh:507-510` at `de5f7e97`). A container therefore kept whatever version it
was first given, including versions with published advisories, and a re-run could not tell anyone.

ADR-026 protects a control from the moment its decision record names it. The convergence controls
below landed without such a record, so the next change could relax one (drop the daily job, let a
`required` component fail quietly) with no rule to argue from.

## Decision

**Every run converges each installed component to its pin. A required component that does not
converge is recorded, every other component is still attempted, and the run then exits 1.** The
controls that implement this are protected under ADR-026:

1. **`_converge`** (`quickstart.sh:251`) probes the component, compares the probe with the pin,
   installs, and probes again. A component is `kept` only when the probe prints exactly the pin, and
   an install that leaves a different build on PATH is a failure, not a success. Each call names
   its class: `required` for Go, gh, Claude Code, the Codex CLI and OpenObserve
   (`quickstart.sh:1104`, `:1235`, `:1394`, `:1597`, `:3030`), `optional` for Playwright
   (`quickstart.sh:1517`). Only an optional component may fail without failing the run
   (`quickstart.sh:281`).
2. **The `CONVERGE_FAILURES` ledger** (`quickstart.sh:57`) collects every required component that
   did not converge: from `_converge` (`quickstart.sh:288`), from the root layer's security-pocket
   and Node.js rows (`_apt_converge`, `quickstart.sh:568`), and from the two Python hash locks
   (`_lock_result`, `quickstart.sh:3250`). `_converge_report` (`quickstart.sh:3451`) is the one
   place a ledgered failure becomes the run's exit status (`quickstart.sh:3516-3519`).
3. **The convergence record**, `.runtime/bootstrap/convergence.json` (`quickstart.sh:3475`), is
   written on every run: one row per component with its pin, installed version, integrity
   mechanism and result.
4. **The exception register and the daily decay job.** `.github/advisory-audit.sh` fails on every
   known advisory against a pin unless `.github/advisory-exceptions.json` excuses it. An entry
   excuses only the package@version it was reviewed for (`.github/advisory-audit.sh:360-365`) and
   expires at its `recheck_by` (`.github/advisory-audit.sh:366-370`), which is at most 30 days
   after `reviewed` (`internal/cmd/advisory_exceptions_test.go:102`). An entry that excuses an
   advisory with a published fix above the pin must name that fix in its `fix_available` field, or
   the run reports an `excused-fixable` finding (the `probe` function); a Node.js pin behind its
   newest security release is a `node-behind` finding. With `ADVISORY_AUDIT_RECORD` set, the audit
   writes every finding, one per class and package@version, to a JSON record, and its exit status
   names the class: 0 clean, 1 a finding (`unexcused`, `expired`, `node-behind` or
   `excused-fixable`), 2 a setup error (an unwritable record included), 3 infrastructure errors
   with no finding. The `advisory-decay` job runs that audit on a daily schedule
   (`.github/workflows/test.yml:50-51`, `:269`), so an advisory published after a pin was chosen
   turns CI red without any change landing. When a scheduled run of stempeck/agentfactory-pro fails,
   the job's last two steps (`.github/install-gh.sh` and `.github/advisory-issue.sh`,
   `.github/workflows/test.yml:288-298`) install the pinned `gh` and file one issue per pin from that
   record, locked and labelled `agentic` and `advisory-heal`; findings that one pull request fixes,
   such as Go `stdlib` and `toolchain` from `GO_TOOLCHAIN_VERSION`, share that issue. The
   `advisory-heal` dispatch workflow is the responder: it routes each issue through fable-implement,
   fable-review and fable-increment, then mails `WORKFLOW_COMPLETE` to its `notify_on_complete`
   agent once the pull request is open for review; the operator's squash-merge of that pull request
   lands the heal.

Two controls these build on are protected with them:

- **`--check`** (`quickstart.sh:1917-1918`; `_converge_check`, `quickstart.sh:3417`) runs the same
  probes as a real run with nothing mutating reachable, and exits 1 on drift.
- **Integrity at every install site.** Both Python locks install only with
  `--require-hashes --only-binary :all:` (`_pip_install_locked`, `quickstart.sh:299`; the image at
  `Dockerfile:43`), and every vendor binary is sha256- or sha512-verified before it runs (the
  integrity column, `quickstart.sh:188-196`).

Each of these can only be strengthened. A change may add a component, move one from `optional` to
`required`, shorten an expiry or add an advisory source. It may not skip a probe, move a component
from `required` to `optional`, let an entry excuse a version it was not reviewed for, or remove the
schedule.

On the register, an agent may on its own move a pin, delete a register entry or shorten a
`recheck_by`; a new entry, or a `recheck_by` moved later, is a proposal accepted only by the
operator's merge of the pull request that proposes it, whose body starts
`Operator acceptance required:`. Adding or changing `fix_available` is the operator's
acknowledgement of a published fix, so it is likewise a proposal accepted only by the operator's
merge of the pull request that proposes it, whose body starts `Operator acceptance required:`.

The design behind the contract is `.designs/743/design-doc.md` (cited as text: the OSS repository
does not carry `.designs/`).

## Consequences

**Costs:**
- A run without egress exits 1 even on a container that has already converged, because the
  security-pocket row needs a fresh `apt-get update` (`quickstart.sh:636-645`).
- Every pin move re-opens the register entries for the old version, and every entry has to be
  re-reviewed at least every 30 days. The operator owns that re-review.
- The first converging run on an existing container changes many components at once.
- Heals are made in stempeck/agentfactory-pro, and the public repository is healed only by a
  publish: its `advisory-decay` run files nothing and stays red until the publish that carries the
  heal. The publish guard (`refuse_reverting_public_heals` in `todos/stempeck_publish_oss.sh`)
  refuses to publish while the public checkout is behind its upstream or carries a newer lock pin,
  `*_VERSION` constant or register `recheck_by` than the source, or a register entry the source
  lacks for a `package@version` a source lock pin or `*_VERSION` constant still pins. Under this
  flow each of those is a hand edit on the public repository, which the publish would otherwise
  silently revert.

**Earned properties:**
- An installed required component never sits on a version other than its pin without a `FAILED`
  row and a non-zero exit.
- A new advisory against a pin turns CI red within a day.
- A finding reaches a responder within a day: the failed scheduled run of
  stempeck/agentfactory-pro files its issue, and the `advisory-heal` workflow picks it up on the
  dispatcher's next poll.
- `convergence.json` shows what a container runs without re-probing it.

## Corpus links

- `../trust-boundaries.md`: the boundaries ADR-026 protects, which now include these controls.
- Related ADRs: [ADR-026](ADR-026-most-secure-way-to-run-an-agent.md) (the rule that protects
  them), [ADR-019](ADR-019-no-container-recreation.md) (why existing containers converge in place
  instead of being recreated).
