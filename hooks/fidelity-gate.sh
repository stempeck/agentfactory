#!/bin/bash
# Fidelity gate Stop hook: evaluate last response against the current
# formula step's title + description, pulled directly from the step bead
# via `af step current --json`. Mirrors quality-gate.sh structure closely —
# see that file for the canonical recursion guard and lock logic; both gates
# now derive their tool evidence from the same `af turn evidence` call. The
# substantive deltas in this script are marked with FIDELITY-DELTA comments
# below.
# Receives event JSON on stdin from Claude Code Stop hook

# Detect role from working directory.
# KNOWN FRAGILITY: basename works because agent name is always the leaf directory
# (.agentfactory/agents/<name>/). Breaks if cwd is a subdirectory of the agent
# workspace. See .designs/32/design-doc.md L235.
ROLE=${AF_ROLE:-$(basename "$(pwd)")}
AGENT_RUNTIME="$(pwd)/.runtime"

# notify_once emits a one-time notice per agent per cause (issue #508) when the gate cannot do
# its job, so a persistent outage — which fails the gate open every turn (ADR-007 never-block) —
# becomes visible instead of silent. Idempotent via a .runtime marker (the fidelity-gate
# per-agent-state idiom), so it is one mail per cause, not a per-turn storm. Sent to the agent's
# own inbox (ADR-007: no fire-and-forget escalation into a possibly-absent recipient). The mail
# wakes the agent so it sees and acts on the notice.
notify_once() {
    cause="$1"
    subject="$2"
    body="$3"
    marker="$AGENT_RUNTIME/grader_notice_$cause"
    [ -f "$marker" ] && return 0
    mkdir -p "$AGENT_RUNTIME" 2>/dev/null
    : > "$marker" 2>/dev/null
    af mail send "$ROLE" -s "$subject" -m "$body" 2>/dev/null
}

notify_grader_unavailable() {
    notify_once "$1" "GRADER_UNAVAILABLE" \
        "fidelity gate grader unavailable ($1): the haiku grader produced no verdict this turn, so the gate is failing open. Check that the claude CLI is on PATH and ~/.claude credentials are valid."
}

# The transcript exists and has content, but the extractor produced no evidence block. The judge
# is told so on the turn itself; this tells the operator, once, that the gate has been grading
# response text alone.
notify_extraction_unavailable() {
    notify_once "extraction_unavailable" "GRADER_UNAVAILABLE" \
        "fidelity gate tool evidence unavailable: the session transcript exists but 'af turn evidence' produced no evidence block, so the gate is grading the response text alone. Check that the installed af binary provides 'af turn evidence' and that the transcript path in the Stop hook payload is readable."
}

# Find prompt file via af root
FACTORY_ROOT="${AF_ROOT}"
if [ -z "$FACTORY_ROOT" ]; then
    FACTORY_ROOT=$(af root 2>/dev/null)
fi
if [ -z "$FACTORY_ROOT" ]; then
    [ -d "$AGENT_RUNTIME" ] && echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT1: no_factory_root" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi
# FIDELITY-DELTA 1: prompt file path
PROMPT_FILE="$FACTORY_ROOT/.agentfactory/hooks/fidelity-gate-prompt.txt"

# FIDELITY-DELTA 2: fidelity gate toggle (default: on via af install --init)
GATE_STATE=$(cat "$FACTORY_ROOT/.agentfactory/.fidelity-gate" 2>/dev/null)
if [ "$GATE_STATE" != "on" ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT2: gate_disabled" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi

# Read stdin once into a variable
INPUT=$(cat)

# Exit immediately if this is a re-invocation (recursion prevention)
ACTIVE=$(echo "$INPUT" | jq -r '.stop_hook_active // false')
if [ "$ACTIVE" = "true" ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT3: recursion_guard" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi

# FIDELITY-DELTA 3: distinct lock file path (must not collide with quality-gate)
# Prevent concurrent runs (per-role PID-file lock with stale detection)
LOCKFILE="$AGENT_RUNTIME/fidelity-gate.lock"

if [ -f "$LOCKFILE" ]; then
    STORED_PID=$(jq -r '.pid' "$LOCKFILE" 2>/dev/null || grep -o '"pid":[[:space:]]*[0-9]*' "$LOCKFILE" | grep -o '[0-9]*')
    if [ -n "$STORED_PID" ] && kill -0 "$STORED_PID" 2>/dev/null; then
        echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT4a: lock_contention pid=$STORED_PID" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
        echo '{"ok": true}'
        exit 0
    fi
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT4b: stale_lock_recovered pid=$STORED_PID" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
    rm -f "$LOCKFILE"
fi
echo "{\"pid\": $$}" > "$LOCKFILE"
trap 'rm -f "$LOCKFILE" 2>/dev/null' EXIT

# Extract last_assistant_message
MESSAGE=$(echo "$INPUT" | jq -r .last_assistant_message)

if [ -z "$MESSAGE" ] || [ "$MESSAGE" = "null" ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT5: no_message" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi

# FIDELITY-DELTA 4: pull current step ground truth from the bead via the
# new af step current subcommand. Branch silently when no formula is
# active or all steps are complete — generic supervisors should see zero
# behavior change.
if ! command -v af &>/dev/null; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT6: no_af_binary" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi
LAST_CLOSED_FILE="$AGENT_RUNTIME/last_closed_step"
IS_AF_DONE_TURN="false"
if [ -f "$LAST_CLOSED_FILE" ]; then
    FILE_AGE=$(( $(date +%s) - $(stat -c %Y "$LAST_CLOSED_FILE" 2>/dev/null || echo 0) ))
    if [ "$FILE_AGE" -lt 30 ]; then
        IS_AF_DONE_TURN="true"
        STEP_JSON=$(cat "$LAST_CLOSED_FILE")
    fi
fi
if [ "$IS_AF_DONE_TURN" != "true" ]; then
    STEP_JSON=$(af step current --json 2>/dev/null)
fi
if [ "$IS_AF_DONE_TURN" != "true" ]; then
    STATE=$(echo "$STEP_JSON" | jq -r '.state // "error"' 2>/dev/null)
    if [ "$STATE" != "ready" ]; then
        echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT7: step_not_ready state=$STATE" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
        echo '{"ok": true}'
        exit 0
    fi
fi
# Description is capped at 4KB to keep haiku input size bounded — operators
# writing essay-length step descriptions should not pay haiku token costs
# linearly. The general-purpose `af step current --json` returns the full
# description; the cap is enforced here at the only cost-sensitive consumer.
# When the cap actually bites, the judge is told so: a contract it was shown
# only part of must not be graded as though it were whole, and the judge has
# no other way to know the difference.
STEP_ID=$(echo "$STEP_JSON" | jq -r .id)
STEP_TITLE=$(echo "$STEP_JSON" | jq -r .title)
STEP_DESCRIPTION_FULL=$(echo "$STEP_JSON" | jq -r .description)
STEP_DESCRIPTION=$(printf '%s' "$STEP_DESCRIPTION_FULL" | head -c 4096)
# head -c counts bytes, so the annotation must too — ${#var} would count characters and
# under-report any description containing multi-byte text.
DESCRIPTION_BYTES=$(printf '%s' "$STEP_DESCRIPTION_FULL" | wc -c | tr -d ' ')
SHOWN_BYTES=$(printf '%s' "$STEP_DESCRIPTION" | wc -c | tr -d ' ')
if [ "$DESCRIPTION_BYTES" -gt "$SHOWN_BYTES" ] 2>/dev/null; then
    STEP_DESCRIPTION="$STEP_DESCRIPTION
[step description truncated: showing $SHOWN_BYTES of $DESCRIPTION_BYTES bytes]"
fi
IS_GATE=$(echo "$STEP_JSON" | jq -r '.is_gate // false')
FORMULA_NAME=$(echo "$STEP_JSON" | jq -r .formula)

# The escalation latch holds the step id that has already escalated, so a threshold crossing
# fires once per step instead of on every turn at or above it. It is scoped to one step, so a
# different step spends it — and `af fidelity status` reads this file directly, so a latch left
# behind would report an escalation that is over.
#
# The violation counter is cleared with it. Both escalation messages say the count is "on this
# step", and the threshold reads that same counter: carry it across a step boundary while the latch
# resets and the first flagged turn of a fresh step escalates on an inherited count, with the
# retained SESSION WILL BE TERMINATED threat, over a number that is not what the message claims.
# The two pieces of state are one step-scoped fact and are spent in one place.
LATCH_FILE="$AGENT_RUNTIME/fidelity_escalated_step"
STEP_FILE="$AGENT_RUNTIME/fidelity_counted_step"
if [ "$(cat "$STEP_FILE" 2>/dev/null)" != "$STEP_ID" ]; then
    mkdir -p "$AGENT_RUNTIME" 2>/dev/null
    rm -f "$LATCH_FILE" "$AGENT_RUNTIME/fidelity_violations" 2>/dev/null
    echo "$STEP_ID" > "$STEP_FILE" 2>/dev/null
fi

# Tool evidence comes from `af turn evidence` (internal/cmd/turn.go): one turn, oldest-first,
# each result paired to the call that produced it by tool_use_id. It replaces an inline construct
# that reversed the whole transcript, took five JSONL lines, and sliced calls and results in two
# independent passes — so the judge was shown calls and results from different turns, newest
# first, joined to nothing.
#
# Every transcript-side failure rides in that command's OUTPUT and still exits 0 (ADR-007), so the
# only failure this has to absorb is a binary that predates the subcommand: that writes to stderr,
# prints nothing, and exits 1. Empty stdout is therefore the single "no evidence" signal.
EVIDENCE_UNAVAILABLE="[tool evidence unavailable this turn]"
TRANSCRIPT=$(echo "$INPUT" | jq -r '.transcript_path // empty')
TOOL_CONTEXT=$(af turn evidence --transcript "$TRANSCRIPT" 2>/dev/null)
if [ -z "$TOOL_CONTEXT" ]; then
    TOOL_CONTEXT="$EVIDENCE_UNAVAILABLE"
fi
EVIDENCE_JSON=$(af turn evidence --transcript "$TRANSCRIPT" --format json 2>/dev/null)
BOUNDARY_UUID=$(echo "$EVIDENCE_JSON" | jq -r '.turn.boundary_uuid // empty' 2>/dev/null)
BOUNDARY_TS=$(echo "$EVIDENCE_JSON" | jq -r '.turn.boundary_ts // empty' 2>/dev/null)
CALLS_TOTAL=$(echo "$EVIDENCE_JSON" | jq -r '.turn.calls_total // 0' 2>/dev/null)
CALLS_SHOWN=$(echo "$EVIDENCE_JSON" | jq -r '.turn.calls_shown // 0' 2>/dev/null)
case "$CALLS_TOTAL" in ''|*[!0-9]*) CALLS_TOTAL=0 ;; esac
case "$CALLS_SHOWN" in ''|*[!0-9]*) CALLS_SHOWN=0 ;; esac
[ -n "$BOUNDARY_TS" ] || BOUNDARY_TS="unknown"

# A transcript that exists and has content but yields no evidence is the one state the gate
# cannot tell apart from a genuinely tool-free turn without saying so out loud.
if [ "$TOOL_CONTEXT" = "$EVIDENCE_UNAVAILABLE" ] && [ -s "$TRANSCRIPT" ]; then
    notify_extraction_unavailable
fi

# One audit line per evaluation naming the scope the verdict was formed under, so a disputed
# verdict can be tied back to the exact turn it graded.
echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EVAL: step=$STEP_ID turn=${BOUNDARY_UUID:-none} calls=$CALLS_SHOWN/$CALLS_TOTAL" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null

# Check claude CLI is available
if ! command -v claude &>/dev/null; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT8: no_claude_binary" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
    notify_grader_unavailable "no_claude_binary"
    echo '{"ok": true}'
    exit 0
fi

# FIDELITY-DELTA 5: prepend "Current step:" header to EVAL_INPUT. Description
# is interpolated as a quoted bash variable; bash variable expansion does
# NOT trigger command substitution, so $(...) inside the description is
# safe (the literal characters pass through to claude as a single positional
# argument). Pinned by TestStepCurrent_DescriptionPassthrough.
EVAL_INPUT="Current step:
Formula: $FORMULA_NAME
Step ID: $STEP_ID
Step title: $STEP_TITLE
Is gate step: $IS_GATE

Step description (the contract the agent must follow this turn):
$STEP_DESCRIPTION

---

Assistant response: $MESSAGE

---

$TOOL_CONTEXT"

# Run evaluation via haiku. Forward the OTel telemetry family through the env -i
# allowlist using conditional expansion — nothing is added when a var is unset, so
# with telemetry off this line stays equivalent to the scrubbed form — and tag the
# forwarded resource attributes as grader overhead so per-turn grader spend is
# attributed instead of invisible (issue #329 P4b).
VERDICT=$(env -i HOME="$HOME" PATH="$PATH" \
    ${CLAUDE_CODE_ENABLE_TELEMETRY:+CLAUDE_CODE_ENABLE_TELEMETRY="$CLAUDE_CODE_ENABLE_TELEMETRY"} \
    ${OTEL_METRICS_EXPORTER:+OTEL_METRICS_EXPORTER="$OTEL_METRICS_EXPORTER"} \
    ${OTEL_LOGS_EXPORTER:+OTEL_LOGS_EXPORTER="$OTEL_LOGS_EXPORTER"} \
    ${OTEL_EXPORTER_OTLP_PROTOCOL:+OTEL_EXPORTER_OTLP_PROTOCOL="$OTEL_EXPORTER_OTLP_PROTOCOL"} \
    ${OTEL_EXPORTER_OTLP_ENDPOINT:+OTEL_EXPORTER_OTLP_ENDPOINT="$OTEL_EXPORTER_OTLP_ENDPOINT"} \
    ${OTEL_EXPORTER_OTLP_HEADERS:+OTEL_EXPORTER_OTLP_HEADERS="$OTEL_EXPORTER_OTLP_HEADERS"} \
    ${OTEL_RESOURCE_ATTRIBUTES:+OTEL_RESOURCE_ATTRIBUTES="$OTEL_RESOURCE_ATTRIBUTES,af.overhead=grader"} \
    claude -p --model haiku --max-turns 1 \
    --system-prompt "You are a JSON-only fidelity gate. You receive an assistant's response, the current formula step's contract, and the tool activity of the turn that just ended. Evaluate adherence to the step contract considering BOTH the text AND the tool evidence, under the evidence rules below. Respond with ONLY valid JSON, nothing else. $(cat "$PROMPT_FILE")" \
    "$EVAL_INPUT" 2>/dev/null)

# Strip markdown code fences if present
VERDICT=$(echo "$VERDICT" | sed 's/^```json//;s/^```//;/^$/d')

# An empty verdict means the grader produced nothing (unavailable / transient) and the
# gate is failing open this turn — surface it once per cause (idempotent), never block.
if [ -z "$VERDICT" ]; then
    notify_grader_unavailable "empty_verdict"
fi

COUNTER_FILE="$AGENT_RUNTIME/fidelity_violations"
RUN_RECORD="$AGENT_RUNTIME/fidelity_log.jsonl"
ESCALATED="false"

# Three-way where this used to be two-way. A parsed pass clears the counter, a parsed flag raises
# it, and a verdict that is empty or does not parse leaves it exactly as it was: the grader
# failing open must not launder a violation history into zero.
VERDICT_STATE="unparsed"
if [ -n "$VERDICT" ]; then
    if echo "$VERDICT" | jq -e '.ok == false' &>/dev/null; then
        VERDICT_STATE="flagged"
    elif echo "$VERDICT" | jq -e '.ok == true' &>/dev/null; then
        VERDICT_STATE="passed"
    fi
fi
COUNT=$(cat "$COUNTER_FILE" 2>/dev/null || echo 0)
case "$COUNT" in ''|*[!0-9]*) COUNT=0 ;; esac

if [ "$VERDICT_STATE" = "flagged" ]; then
    COUNT=$((COUNT+1))
    echo "$COUNT" > "$COUNTER_FILE"

    # The verdict the agent reads is the judge's JSON plus the scope it was formed under. Without
    # the step id and the turn boundary, a verdict about one turn reads as a standing accusation,
    # and the agent re-executes work an earlier turn already did.
    VERDICT_MAIL="$VERDICT

--- verdict scope ---
step: $STEP_ID
turn ended: $BOUNDARY_TS
tool calls shown: $CALLS_SHOWN of $CALLS_TOTAL
This verdict covers only the turn ending at $BOUNDARY_TS. If the cited work happened in an earlier turn, reply with the artifact instead of re-executing.
Delete this mail (af mail delete <id>) after acting on it."

    # Supersede the prior unread verdict for THIS step before filing the new one, so a mail
    # backlog cannot be replayed as a storm through SessionStart injection. Deleting a bead is
    # closing it (internal/mail/mailbox.go:92-96) and the inbox holds only unread mail, so every
    # hit here is a verdict the agent has not acted on yet. mail.Message carries no step field —
    # the step id is matched out of the scope block above.
    for STALE_ID in $(af mail inbox --json 2>/dev/null | jq -r --arg scope "step: $STEP_ID" '.[] | select(.subject == "STEP_FIDELITY") | select(.body | contains($scope + "\n")) | .id' 2>/dev/null); do
        af mail delete "$STALE_ID" >/dev/null 2>&1
    done
    af mail send "$ROLE" -s "STEP_FIDELITY" -m "$VERDICT_MAIL" --priority urgent 2>/dev/null

    ESCALATION_THRESHOLD=${AF_FIDELITY_ESCALATION_THRESHOLD:-3}
    if [ "$COUNT" -ge "$ESCALATION_THRESHOLD" ] && [ "$(cat "$LATCH_FILE" 2>/dev/null)" != "$STEP_ID" ]; then
        echo "$STEP_ID" > "$LATCH_FILE"
        ESCALATED="true"

        # Supervisor first: the self-copy's claim about it is worded from this delivery report, so
        # the report has to exist before the claim is composed. This send deliberately stays
        # waking — it is the one push path to an overseer, and silencing it would make every
        # escalation invisible.
        SUPERVISOR_REPORT=$(af mail send supervisor -s "FIDELITY_ESCALATION" -m "Agent $ROLE has $COUNT flagged fidelity evaluations on step: $STEP_TITLE (counted since the last passing verdict)" --priority urgent --report-delivery 2>/dev/null)
        case "$SUPERVISOR_REPORT" in
            Notified*)
                SUPERVISOR_CLAIM="Supervisor notified."
                ;;
            *)
                # Claim only what is knowable: an absent-recipient send files the bead and
                # succeeds, so "notified" would be an invention (ADR-007 amendment clause 1).
                SUPERVISOR_CLAIM="Supervisor copy filed; delivery to a live supervisor session was not confirmed."
                echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) SUPERVISOR_UNREACHABLE: ${SUPERVISOR_REPORT:-no delivery report}" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
                ;;
        esac

        ESCALATION_MSG="FIDELITY VIOLATION - SESSION WILL BE TERMINATED. You have deviated from the formula step. If you do not re-read the current step instructions and execute them literally on your next action, this session will be killed via af down. All progress will be lost. Run af prime to re-read the step, then execute exactly as written.

$COUNT fidelity evaluations have been flagged on this step since the last passing verdict. $SUPERVISOR_CLAIM If you believe these verdicts are wrong, reply to this mail with the evidence: the tool call and the artifact it produced."
        af mail send "$ROLE" -s "FIDELITY_ESCALATION" -m "$ESCALATION_MSG" --priority urgent 2>/dev/null
    fi
elif [ "$VERDICT_STATE" = "passed" ]; then
    COUNT=0
    echo 0 > "$COUNTER_FILE"
    # A pass spends the latch, so the next threshold crossing on this step can escalate again —
    # and so `af fidelity status` stops reporting an escalation that is over.
    rm -f "$LATCH_FILE" 2>/dev/null
fi

# One line per parsed evaluation, passes included: the record `af fidelity status` reads
# (internal/cmd/fidelity.go:352-377), and the only positive evidence a compliant run leaves
# behind. Built with jq rather than string concatenation because the reader silently skips a line
# it cannot parse, so a quoting bug would surface as wrong counts instead of as an error. A turn
# the grader could not grade is not written at all — it was not an evaluation, and verdict_ok has
# no value for it that would not either invent a pass or invent a failure.
if [ "$VERDICT_STATE" != "unparsed" ]; then
    VERDICT_OK="true"
    [ "$VERDICT_STATE" = "flagged" ] && VERDICT_OK="false"
    mkdir -p "$AGENT_RUNTIME" 2>/dev/null
    RECORD_LINE=$(jq -c -n \
        --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        --arg step_id "$STEP_ID" \
        --argjson verdict_ok "$VERDICT_OK" \
        --argjson calls_total "$CALLS_TOTAL" \
        --argjson calls_shown "$CALLS_SHOWN" \
        --argjson violations_after "$COUNT" \
        --argjson escalated "$ESCALATED" \
        '{ts: $ts, step_id: $step_id, verdict_ok: $verdict_ok, calls_total: $calls_total, calls_shown: $calls_shown, violations_after: $violations_after, escalated: $escalated}' 2>/dev/null)
    if [ -n "$RECORD_LINE" ]; then
        printf '%s\n' "$RECORD_LINE" >> "$RUN_RECORD" 2>/dev/null
    fi
fi

VELOCITY_FILE="$AGENT_RUNTIME/done_velocity"
if [ -f "$VELOCITY_FILE" ]; then
    UPDATED=$(jq --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '.last_eval_between = $ts' "$VELOCITY_FILE")
    echo "$UPDATED" > "$VELOCITY_FILE"
fi

echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT9: normal_completion" >> "$AGENT_RUNTIME/fidelity_debug.log" 2>/dev/null
echo '{"ok": true}'
exit 0
