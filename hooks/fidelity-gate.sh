#!/bin/bash
# Fidelity gate Stop hook: evaluate last response against the current
# formula step's title + description, pulled directly from the step bead
# via `af step current --json`. Mirrors quality-gate.sh structure 95%
# verbatim — see that file for the canonical recursion guard, lock, and
# transcript-extraction logic. The five substantive deltas in this script
# are marked with FIDELITY-DELTA comments below.
# Receives event JSON on stdin from Claude Code Stop hook

# Detect role from working directory.
# KNOWN FRAGILITY: basename works because agent name is always the leaf directory
# (.agentfactory/agents/<name>/). Breaks if cwd is a subdirectory of the agent
# workspace. See .designs/32/design-doc.md L235.
ROLE=${AF_ROLE:-$(basename "$(pwd)")}
AGENT_RUNTIME="$(pwd)/.runtime"

# notify_grader_unavailable emits a one-time notice per agent per cause (issue #508)
# when the haiku grader cannot produce a verdict, so a persistent outage — which
# fails the gate open every turn (ADR-007 never-block) — becomes visible instead of
# silent. Idempotent via a .runtime marker (the fidelity-gate per-agent-state idiom),
# so it is one mail per cause, not a per-turn storm. Sent to the agent's own inbox
# (ADR-007: no fire-and-forget escalation into a possibly-absent recipient).
notify_grader_unavailable() {
    cause="$1"
    marker="$AGENT_RUNTIME/grader_notice_$cause"
    [ -f "$marker" ] && return 0
    mkdir -p "$AGENT_RUNTIME" 2>/dev/null
    : > "$marker" 2>/dev/null
    af mail send "$ROLE" -s "GRADER_UNAVAILABLE" \
        -m "fidelity gate grader unavailable ($cause): the haiku grader produced no verdict this turn, so the gate is failing open. Check that the claude CLI is on PATH and ~/.claude credentials are valid." \
        2>/dev/null
}

# Find prompt file via af root
FACTORY_ROOT=${AF_ROOT:-$(af root 2>/dev/null)}
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
STEP_ID=$(echo "$STEP_JSON" | jq -r .id)
STEP_TITLE=$(echo "$STEP_JSON" | jq -r .title)
STEP_DESCRIPTION=$(echo "$STEP_JSON" | jq -r .description | head -c 4096)
IS_GATE=$(echo "$STEP_JSON" | jq -r '.is_gate // false')
FORMULA_NAME=$(echo "$STEP_JSON" | jq -r .formula)

# Track evaluation count per step for first-turn bypass guard
CURRENT_STEP_FILE="$AGENT_RUNTIME/fidelity_eval_step"
EVAL_COUNT_FILE="$AGENT_RUNTIME/fidelity_eval_count"
STORED_STEP=$(cat "$CURRENT_STEP_FILE" 2>/dev/null)
if [ "$IS_AF_DONE_TURN" = "true" ]; then
    :
elif [ "$STORED_STEP" != "$STEP_ID" ]; then
    echo 0 > "$EVAL_COUNT_FILE"
    echo "$STEP_ID" > "$CURRENT_STEP_FILE"
fi
EVAL_COUNT=$(cat "$EVAL_COUNT_FILE" 2>/dev/null || echo 0)
echo $((EVAL_COUNT+1)) > "$EVAL_COUNT_FILE"

# Extract tool call and result summary from transcript (recent turns)
TRANSCRIPT=$(echo "$INPUT" | jq -r '.transcript_path // empty')
TOOL_CONTEXT=""
if [ -n "$TRANSCRIPT" ] && [ -f "$TRANSCRIPT" ]; then
    # Reverse lines: tac on Linux, tail -r on macOS
    if command -v tac &>/dev/null; then
        REVERSE="tac"
    else
        REVERSE="tail -r"
    fi
    # Extract recent tool calls (name + inputs)
    TOOL_CALLS=$($REVERSE "$TRANSCRIPT" \
        | jq -c 'select(.message.content[]?.type == "tool_use") | [.message.content[] | select(.type == "tool_use") | {tool: .name, input: .input}]' 2>/dev/null \
        | head -5 \
        | jq -rs 'add // [] | .[] | "- \(.tool): \(.input | to_entries | map("\(.key)=\(.value | tostring)") | join(", "))"' 2>/dev/null)
    # Extract recent tool results (outputs)
    TOOL_RESULTS=$($REVERSE "$TRANSCRIPT" \
        | jq -c 'select(.message.content[]?.type == "tool_result") | [.message.content[] | select(.type == "tool_result") | .content]' 2>/dev/null \
        | head -5 \
        | jq -rs 'add // [] | .[] | "  > \(. | tostring | .[0:300])"' 2>/dev/null)
    # Combine calls and results
    if [ -n "$TOOL_CALLS" ] || [ -n "$TOOL_RESULTS" ]; then
        TOOL_CONTEXT="Tool calls executed:
${TOOL_CALLS}

Tool outputs received:
${TOOL_RESULTS}"
    fi
fi

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

Assistant response: $MESSAGE"
if [ -n "$TOOL_CONTEXT" ]; then
    EVAL_INPUT="$EVAL_INPUT

Tool calls executed in this turn (from transcript):
$TOOL_CONTEXT"
fi

# Run evaluation via haiku
VERDICT=$(env -i HOME="$HOME" PATH="$PATH" claude -p --model haiku --max-turns 1 \
    --system-prompt "You are a JSON-only fidelity gate. You receive an assistant's response, the current formula step's contract, and the tool calls executed this turn. Evaluate adherence to the step contract considering BOTH the text AND the tool evidence. Respond with ONLY valid JSON, nothing else. $(cat "$PROMPT_FILE")" \
    "$EVAL_INPUT" 2>/dev/null)

# Strip markdown code fences if present
VERDICT=$(echo "$VERDICT" | sed 's/^```json//;s/^```//;/^$/d')

# An empty verdict means the grader produced nothing (unavailable / transient) and the
# gate is failing open this turn — surface it once per cause (idempotent), never block.
if [ -z "$VERDICT" ]; then
    notify_grader_unavailable "empty_verdict"
fi

COUNTER_FILE="$AGENT_RUNTIME/fidelity_violations"
if [ -n "$VERDICT" ] && echo "$VERDICT" | jq -e '.ok == false' &>/dev/null; then
    COUNT=$(cat "$COUNTER_FILE" 2>/dev/null || echo 0)
    echo $((COUNT+1)) > "$COUNTER_FILE"
    af mail send "$ROLE" -s "STEP_FIDELITY" -m "$VERDICT" --priority urgent 2>/dev/null

    ESCALATION_THRESHOLD=${AF_FIDELITY_ESCALATION_THRESHOLD:-3}
    if [ "$((COUNT+1))" -ge "$ESCALATION_THRESHOLD" ]; then
        ESCALATION_MSG="FIDELITY VIOLATION - SESSION WILL BE TERMINATED. You have deviated from the formula step. If you do not re-read the current step instructions and execute them literally on your next action, this session will be killed via af down. All progress will be lost. Run af prime to re-read the step, then execute exactly as written."
        af mail send "$ROLE" -s "FIDELITY_ESCALATION" -m "$ESCALATION_MSG" --priority urgent 2>/dev/null
        af mail send supervisor -s "FIDELITY_ESCALATION" -m "Agent $ROLE has $((COUNT+1)) consecutive fidelity violations on step: $STEP_TITLE" --priority urgent 2>/dev/null
    fi
else
    if [ -f "$COUNTER_FILE" ]; then
        echo 0 > "$COUNTER_FILE"
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
