# Fix Patterns Reference

Pattern templates for each failure category. Adapt to the specific formula's variables and paths.

## Artifact Pollution

**When**: Unwanted files appear in the PR. Agent workspace artifacts, intermediate analysis files, or superseded iterations committed during pipeline steps.

**Root cause**: Agents commit to their workspace during intermediate steps. Later finalize steps cannot undo already-pushed commits with `git reset`.

**Fix pattern — Zero-Diff Restore**:

```bash
cd "$AF_ROOT"
BASE=$(git merge-base HEAD origin/main)

for path in <list-of-directories-that-must-show-zero-diff>; do
  # Remove files ADDED during pipeline (don't exist on base)
  git diff --diff-filter=A --name-only "$BASE" HEAD -- "$path" | xargs -r git rm -f 2>/dev/null || true
  # Restore files MODIFIED during pipeline to base state
  git checkout "$BASE" -- "$path" 2>/dev/null || true
done

# For specific file globs (e.g., todos/rootcause*)
git diff --diff-filter=A --name-only "$BASE" HEAD -- "<glob-path>" | grep -E '<pattern>' | xargs -r git rm -f 2>/dev/null || true
git checkout "$BASE" -- <glob-path> 2>/dev/null || true

git diff --cached --quiet || git commit -m "<formula-name>: revert intermediate agent artifacts to base branch state"
git push origin HEAD
```

**Key principle**: The goal is NOT "delete unwanted files" — it's "make these paths identical to the base branch in the PR diff." This preserves permanent files (CLAUDE.md, settings) while removing pipeline additions.

---

## Missing Step

**When**: The formula assumes an agent will perform work that nothing enforces. The agent skips it or does it incorrectly.

**Root cause**: Instruction-level enforcement (text says "do X") without a mechanical gate (script verifies X was done).

**Fix pattern — Verification + Nudge-Retry**:

```bash
# Verify the expected artifact exists
if [ ! -f "<expected-artifact-path>" ]; then
  echo "MISSING: <artifact> not produced by <agent>"
  af mail send <agent> -s "<FORMULA>: REMINDER" -m "You must produce <artifact> at <path>. Do it now and signal: af mail send <orchestrator> -s '<SIGNAL>'"

  # Poll with retry
  RETRIES=0
  while [ $RETRIES -lt 3 ]; do
    sleep {{poll_interval}}
    if [ -f "<expected-artifact-path>" ]; then break; fi
    RETRIES=$((RETRIES + 1))
    af mail send <agent> -s "<FORMULA>: RETRY $RETRIES" -m "Still missing <artifact>."
  done

  if [ ! -f "<expected-artifact-path>" ]; then
    af mail send <orchestrator> -s "ESCALATE: <agent> failed to produce <artifact> after 3 retries"
    exit 1
  fi
fi
```

**Key principle**: Every expected artifact gets a verification check. If missing, nudge the agent. If still missing after retries, escalate — don't silently proceed.

---

## Wrong Output Location

**When**: Artifacts land in the wrong directory. Common causes: relative vs absolute path resolution, variable contains bead ID instead of issue number, agent's working directory differs from expected.

**Root cause**: Path variable resolves differently than formula author expected, OR agent writes relative to its own working dir instead of `$AF_ROOT`.

**Fix pattern — Post-hoc Move + Cleanup**:

```bash
cd "$AF_ROOT"

# Move artifacts from wrong location to correct location
WRONG_PATH="<where-artifacts-actually-landed>"
CORRECT_PATH="<where-they-should-be>"

if [ -d "$WRONG_PATH" ] && [ "$WRONG_PATH" != "$CORRECT_PATH" ]; then
  mkdir -p "$CORRECT_PATH"
  cp -r "$WRONG_PATH"/* "$CORRECT_PATH"/ 2>/dev/null || true
  git rm -r --ignore-unmatch "$WRONG_PATH" 2>/dev/null || true
  git add "$CORRECT_PATH"
  git diff --cached --quiet || git commit -m "<formula-name>: relocate artifacts to correct path"
  git push origin HEAD
fi
```

**Alternative — Fix at source**: If the variable resolution is the problem, fix the variable in the dispatch step's instructions rather than moving after the fact. Prefer fixing upstream when possible.

**Key principle**: Determine whether to fix the cause (variable/path in dispatch instructions) or the effect (move after completion). Fix cause when the same agent runs repeatedly; fix effect when it's a one-time correction.

---

## Signal Ordering

**When**: Steps depend on signals that agents don't send, send with wrong subject, or send before work is actually complete.

**Root cause**: Signal subject doesn't match the `test()` regex in the polling loop, OR agent's formula completes (WORK_DONE) without sending the orchestrator-specific signal.

**Fix pattern — Signal Verification + Fallback**:

```bash
# Primary: wait for explicit signal
SIGNAL_RECEIVED=false
RETRIES=0

while [ "$SIGNAL_RECEIVED" = "false" ] && [ $RETRIES -lt 3 ]; do
  SIGNAL=$(af mail inbox --json 2>/dev/null | jq -r '
    .[] | select(.subject | test("<EXPECTED_SIGNAL_REGEX>")) | .id
  ' | head -1)

  if [ -n "$SIGNAL" ]; then
    SIGNAL_RECEIVED=true
    af mail delete "$SIGNAL"
  else
    # Fallback: check if work is done even without signal
    if [ -f "<artifact-that-proves-work-done>" ]; then
      echo "WARNING: Agent completed work but did not send signal. Proceeding."
      SIGNAL_RECEIVED=true
    else
      RETRIES=$((RETRIES + 1))
      af mail send <agent> -s "<FORMULA>" -m "Send signal now: af mail send <orchestrator> -s '<EXACT_SIGNAL_TEXT>'"
      sleep {{poll_interval}}
    fi
  fi
done
```

**Key principle**: Don't rely solely on signals. Check for the ARTIFACT that proves work was done as a fallback. Signals confirm; artifacts verify.

---

## Enforcement Gap

**When**: Step instructions tell the agent to do something, but there's no verification it happened. Agent skips or partially completes.

**Root cause**: Instruction-level enforcement without a forcing function or mechanical check.

**Fix pattern — Post-action Verification Gate**:

```bash
# After the agent claims completion, verify mechanically
<verification-command>
if [ $? -ne 0 ]; then
  echo "ENFORCEMENT FAILED: <what was expected>"
  af mail send <agent> -s "<FORMULA>: ENFORCEMENT" -m "<specific instruction on what to fix>"
  # Retry loop...
fi
```

Verification examples by type:
- **File must exist**: `[ -f "$PATH" ]`
- **File must contain pattern**: `grep -qE '<pattern>' "$PATH"`
- **Commit must exist**: `git log --oneline -1 | grep -q '<expected>'`
- **Tests must pass**: `make test 2>&1 | tail -1 | grep -q 'PASS'`
- **PR must be created**: `gh pr list --head "$BRANCH" --json url | jq -e '.[0]'`

**Key principle**: Every agent claim of "done" gets mechanical verification. Trust but verify. The verification should check the ARTIFACT, not the agent's word.
