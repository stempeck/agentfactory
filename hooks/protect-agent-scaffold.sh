#!/usr/bin/env bash
# Pre-commit hook: block agent sessions from modifying agent-gen-owned scaffold.
# Only activates when AF_ROLE is set (inside an agent session).
set -euo pipefail

if [ -z "${AF_ROLE:-}" ]; then
    exit 0
fi

scaffold_files=$(git diff --cached --name-only --diff-filter=ACMR | grep -E '^\.agentfactory/(agents/.+/(CLAUDE\.md|\.claude/settings\.json)|(agents|dispatch|factory|messaging)\.json)$' || true)

if [ -n "$scaffold_files" ]; then
    echo "ERROR: agent '$AF_ROLE' attempted to modify agent-gen scaffold files:" >&2
    echo "$scaffold_files" | sed 's/^/  /' >&2
    echo "" >&2
    echo "These files are owned by agent-gen and must not be modified by agents." >&2
    exit 1
fi
