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

# Find prompt file via af root
FACTORY_ROOT=${AF_ROOT:-$(af root 2>/dev/null)}
if [ -z "$FACTORY_ROOT" ]; then
    echo '{"ok": true}'
    exit 0
fi
# FIDELITY-DELTA 1: prompt file path
PROMPT_FILE="$FACTORY_ROOT/hooks/fidelity-gate-prompt.txt"

# FIDELITY-DELTA 2: fidelity gate toggle (default: on via af install --init)
GATE_STATE=$(cat "$FACTORY_ROOT/.fidelity-gate" 2>/dev/null)
if [ "$GATE_STATE" != "on" ]; then
    echo '{"ok": true}'
    exit 0
fi

# Read stdin once into a variable
INPUT=$(cat)

# Exit immediately if this is a re-invocation (recursion prevention)
ACTIVE=$(echo "$INPUT" | jq -r '.stop_hook_active // false')
if [ "$ACTIVE" = "true" ]; then
    echo '{"ok": true}'
    exit 0
fi

# FIDELITY-DELTA 3: distinct lock file path (must not collide with quality-gate)
# Prevent concurrent runs (per-role lock)
LOCKFILE="/tmp/af-fidelity-gate-$ROLE.lock"
if ! mkdir "$LOCKFILE" 2>/dev/null; then
    echo '{"ok": true}'
    exit 0
fi
trap "rmdir $LOCKFILE 2>/dev/null" EXIT

# Extract last_assistant_message
MESSAGE=$(echo "$INPUT" | jq -r .last_assistant_message)

if [ -z "$MESSAGE" ] || [ "$MESSAGE" = "null" ]; then
    echo '{"ok": true}'
    exit 0
fi

# FIDELITY-DELTA 4: pull current step ground truth from the bead via the
# new af step current subcommand. Branch silently when no formula is
# active or all steps are complete — generic supervisors should see zero
# behavior change.
if ! command -v af &>/dev/null; then
    echo '{"ok": true}'
    exit 0
fi
STEP_JSON=$(af step current --json 2>/dev/null)
STATE=$(echo "$STEP_JSON" | jq -r '.state // "error"' 2>/dev/null)
if [ "$STATE" != "ready" ]; then
    echo '{"ok": true}'
    exit 0
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
VERDICT=$(claude -p --model haiku --max-turns 1 \
    --system-prompt "You are a JSON-only fidelity gate. You receive an assistant's response, the current formula step's contract, and the tool calls executed this turn. Evaluate adherence to the step contract considering BOTH the text AND the tool evidence. Respond with ONLY valid JSON, nothing else. $(cat "$PROMPT_FILE")" \
    "$EVAL_INPUT" 2>/dev/null)

# Strip markdown code fences if present
VERDICT=$(echo "$VERDICT" | sed 's/^```json//;s/^```//;/^$/d')

# Mail verdict to self only on failure. FIDELITY-DELTA: subject is STEP_FIDELITY.
if [ -n "$VERDICT" ] && echo "$VERDICT" | jq -e '.ok == false' &>/dev/null; then
    af mail send "$ROLE" -s "STEP_FIDELITY" -m "$VERDICT" 2>/dev/null
fi

echo '{"ok": true}'
exit 0
