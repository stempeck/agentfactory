# User Experience

## Summary
The UX impact of this change is primarily about visibility — operators currently have no indication when gates are bypassed due to lock failure. The change should make gate bypass events visible without being disruptive. The two audiences are: (1) agents receiving gate verdicts, and (2) human operators reviewing agent behavior.

## Constraint Check (BEFORE exploring options)
- [x] C8 (toggle behavior unchanged): Gate on/off defaults are not affected
- [x] C10 (smoke test): UX changes don't affect hook output format
- [x] C14 (exit contract): Notifications are best-effort; exit path unchanged

## Options Explored

#### Option 1: Mail notification on lock failure
- **Description**: When lock acquisition fails after retries, send a mail to the agent with subject "GATE_LOCK_CONTENTION" explaining that the evaluation was skipped due to lock contention.
- **Constraint Compliance**: ✓ C8, ✓ C10, ✓ C14
- **Pros**: Agent sees the skipped evaluation in its inbox; consistent with existing gate verdict mail pattern (QUALITY_GATE, STEP_FIDELITY subjects)
- **Cons**: Adds mail noise if contention is frequent; agent may not act on it
- **Effort**: Low
- **Reversibility**: Easy

#### Option 2: Stderr logging only
- **Description**: Write a warning to stderr when lock fails. Stderr goes to the hook's log output but not to the agent's conversation.
- **Constraint Compliance**: ✓ C8, ✓ C10, ✓ C14
- **Pros**: Zero noise for the agent; available for debugging via hook logs
- **Cons**: Practically invisible — operators must know to check hook stderr; doesn't address the "agent doesn't know" problem
- **Effort**: Low
- **Reversibility**: Easy

#### Option 3: Mail notification + stderr logging
- **Description**: Combine Options 1 and 2 — mail the agent AND write to stderr.
- **Constraint Compliance**: ✓ C8, ✓ C10, ✓ C14
- **Pros**: Both audiences covered; agent and operator can each see the event in their respective channels
- **Cons**: Slight redundancy
- **Effort**: Low
- **Reversibility**: Easy

### Recommendation
**Option 1 (mail notification on lock failure)** — consistent with the existing pattern where gate verdicts are mailed to the agent. The mail subject should clearly distinguish lock contention from actual gate failures. Stderr logging (Option 2) adds little value since hook logs are rarely inspected. Keep it simple: one notification channel, consistent with existing conventions.

Mail format:
```
af mail send "$ROLE" -s "GATE_LOCK_CONTENTION" -m "quality-gate evaluation skipped: lock contention after 3 retries"
```

## Dependencies Produced
- API dimension's retry logic determines when notification fires
- Mail notification requires `af mail send` availability (already checked in scripts)

## Risks Identified
- Mail notification spam under persistent lock issues: Severity Low — lock contention should be rare; if persistent, the notification itself is the desired signal
