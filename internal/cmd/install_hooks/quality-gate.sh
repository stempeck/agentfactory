#!/bin/bash
# Quality gate Stop hook: evaluate last response against 7 principles
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
PROMPT_FILE="$FACTORY_ROOT/.agentfactory/hooks/quality-gate-prompt.txt"

# Check quality gate toggle (default: off)
GATE_STATE=$(cat "$FACTORY_ROOT/.agentfactory/.quality-gate" 2>/dev/null)
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

# Prevent concurrent runs (per-role lock)
LOCKFILE="/tmp/af-quality-gate-$ROLE.lock"
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

# Build evaluation input: assistant text + tool call evidence
EVAL_INPUT="Assistant response: $MESSAGE"
if [ -n "$TOOL_CONTEXT" ]; then
    EVAL_INPUT="$EVAL_INPUT

Tool calls executed in this turn (from transcript):
$TOOL_CONTEXT"
fi

# Run evaluation via haiku
VERDICT=$(claude -p --model haiku --max-turns 1 \
    --system-prompt "You are a JSON-only quality gate. You receive an assistant's response along with the tool calls it executed. Evaluate the response considering BOTH the text AND the tool evidence. Respond with ONLY valid JSON, nothing else. $(cat "$PROMPT_FILE")" \
    "$EVAL_INPUT" 2>/dev/null)

# Strip markdown code fences if present
VERDICT=$(echo "$VERDICT" | sed 's/^```json//;s/^```//;/^$/d')

# Mail verdict to self only on failure
if [ -n "$VERDICT" ] && echo "$VERDICT" | jq -e '.ok == false' &>/dev/null; then
    af mail send "$ROLE" -s "QUALITY_GATE" -m "$VERDICT" 2>/dev/null
fi

echo '{"ok": true}'
exit 0
