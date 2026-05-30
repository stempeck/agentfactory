#!/usr/bin/env bash
# Pre-commit hook: block agent sessions from modifying agent-gen-owned scaffold.
# Only activates when AF_ROLE is set (inside an agent session).
set -euo pipefail

if [ -z "${AF_ROLE:-}" ]; then
    exit 0
fi

staged=$(git diff --cached --name-only --diff-filter=ACMR || true)

scaffold_files=$(echo "$staged" | grep -E '^\.agentfactory/(agents/.+/(CLAUDE\.md|\.claude/settings\.json)|(agents|dispatch|factory|messaging)\.json)$' || true)

artifact_files=$(echo "$staged" | grep -E '^\.agentfactory/agents/.+/' | grep -vE '^\.agentfactory/agents/.+/(CLAUDE\.md|\.claude/settings\.json)$' || true)

blocked=0

if [ -n "$scaffold_files" ]; then
    echo "ERROR: agent '$AF_ROLE' attempted to modify agent-gen scaffold files:" >&2
    echo "$scaffold_files" | sed 's/^/  /' >&2
    echo "" >&2
    echo "These files are owned by agent-gen and must not be modified by agents." >&2
    blocked=1
fi

if [ -n "$artifact_files" ]; then
    echo "ERROR: agent '$AF_ROLE' attempted to commit runtime artifact files:" >&2
    echo "$artifact_files" | sed 's/^/  /' >&2
    echo "" >&2
    echo "Agent runtime artifacts (todos/*, logs/*, etc.) must not be committed." >&2
    echo "These files are excluded by .gitignore and should remain local." >&2
    blocked=1
fi

if [ "$blocked" -eq 1 ]; then
    exit 1
fi
