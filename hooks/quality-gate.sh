#!/bin/bash
# Quality gate Stop hook: evaluate last response against 7 principles
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
# (ADR-007: no fire-and-forget escalation into a possibly-absent recipient). The mail
# wakes the agent so it sees and acts on the notice.
notify_grader_unavailable() {
    cause="$1"
    marker="$AGENT_RUNTIME/grader_notice_$cause"
    [ -f "$marker" ] && return 0
    mkdir -p "$AGENT_RUNTIME" 2>/dev/null
    : > "$marker" 2>/dev/null
    af mail send "$ROLE" -s "GRADER_UNAVAILABLE" \
        -m "quality gate grader unavailable ($cause): the haiku grader produced no verdict this turn, so the gate is failing open. Check that the claude CLI is on PATH and ~/.claude credentials are valid." \
        2>/dev/null
}

# Find prompt file via af root
FACTORY_ROOT="${AF_ROOT}"
if [ -z "$FACTORY_ROOT" ]; then
    FACTORY_ROOT=$(af root 2>/dev/null)
fi
if [ -z "$FACTORY_ROOT" ]; then
    [ -d "$AGENT_RUNTIME" ] && echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT1: no_factory_root" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi
PROMPT_FILE="$FACTORY_ROOT/.agentfactory/hooks/quality-gate-prompt.txt"

# Check quality gate toggle (default: off)
GATE_STATE=$(cat "$FACTORY_ROOT/.agentfactory/.quality-gate" 2>/dev/null)
if [ "$GATE_STATE" != "on" ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT2: gate_disabled" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi

# Read stdin once into a variable
INPUT=$(cat)

# Exit immediately if this is a re-invocation (recursion prevention)
ACTIVE=$(echo "$INPUT" | jq -r '.stop_hook_active // false')
if [ "$ACTIVE" = "true" ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT3: recursion_guard" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi

# Prevent concurrent runs (per-role PID-file lock with stale detection)
LOCKFILE="$AGENT_RUNTIME/quality-gate.lock"

if [ -f "$LOCKFILE" ]; then
    STORED_PID=$(jq -r '.pid' "$LOCKFILE" 2>/dev/null || grep -o '"pid":[[:space:]]*[0-9]*' "$LOCKFILE" | grep -o '[0-9]*')
    if [ -n "$STORED_PID" ] && kill -0 "$STORED_PID" 2>/dev/null; then
        echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT4a: lock_contention pid=$STORED_PID" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
        echo '{"ok": true}'
        exit 0
    fi
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT4b: stale_lock_recovered pid=$STORED_PID" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
    rm -f "$LOCKFILE"
fi
echo "{\"pid\": $$}" > "$LOCKFILE"
trap 'rm -f "$LOCKFILE" 2>/dev/null' EXIT

# Extract last_assistant_message
MESSAGE=$(echo "$INPUT" | jq -r .last_assistant_message)

if [ -z "$MESSAGE" ] || [ "$MESSAGE" = "null" ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT5: no_message" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi

# This gate has always called `af` (af root above, af mail send below) without ever checking that
# it exists. It sits here, after the lock block, rather than at the top: the lock's contention and
# stale-recovery paths must still reach the debug log on a machine that has no af on PATH.
if ! command -v af &>/dev/null; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT6: no_af_binary" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
    echo '{"ok": true}'
    exit 0
fi

# Tool evidence comes from `af turn evidence` (internal/cmd/turn.go): one turn, oldest-first, each
# result paired to the call that produced it by tool_use_id. It replaces an inline construct that
# reversed the whole transcript, took five JSONL lines, and sliced calls and results in two
# independent passes — so the judge was shown calls and results from different turns, newest
# first, joined to nothing. The fidelity gate derives its evidence from the same call, so both
# gates now present the same evidence derived the same way.
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

# Check claude CLI is available
if ! command -v claude &>/dev/null; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT7: no_claude_binary" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
    notify_grader_unavailable "no_claude_binary"
    echo '{"ok": true}'
    exit 0
fi

# Build evaluation input: assistant text + this turn's tool evidence. The evidence section is
# unconditional — an empty turn and an unreadable transcript are distinct states the judge has
# rules for, and omitting the section would hide exactly the markers those rules key on.
EVAL_INPUT="Assistant response: $MESSAGE

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
    --system-prompt "You are a JSON-only quality gate. You receive an assistant's response along with the tool activity of the turn that just ended. Evaluate the response considering BOTH the text AND the tool evidence, under the evidence rules below. Respond with ONLY valid JSON, nothing else. $(cat "$PROMPT_FILE")" \
    "$EVAL_INPUT" 2>/dev/null)

# Strip markdown code fences if present
VERDICT=$(echo "$VERDICT" | sed 's/^```json//;s/^```//;/^$/d')

# An empty verdict means the grader produced nothing (unavailable / transient) and the
# gate is failing open this turn — surface it once per cause (idempotent), never block.
if [ -z "$VERDICT" ]; then
    notify_grader_unavailable "empty_verdict"
fi

# Mail the verdict to self only on failure. The mail wakes the agent so it reads the
# verdict about the turn that just ended and acts on it (PR #608: mail always wakes).
if [ -n "$VERDICT" ] && echo "$VERDICT" | jq -e '.ok == false' &>/dev/null; then
    af mail send "$ROLE" -s "QUALITY_GATE" -m "$VERDICT" 2>/dev/null
fi

echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) EXIT8: normal_completion" >> "$AGENT_RUNTIME/quality_debug.log" 2>/dev/null
echo '{"ok": true}'
exit 0
