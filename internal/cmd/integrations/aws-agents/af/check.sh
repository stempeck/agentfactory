#!/usr/bin/env bash
# The plugin's only runtime dependency is the remote AWS Knowledge MCP server its .mcp.json names.
# Reachability is what can be verified without AWS credentials: a TLS handshake and any HTTP answer
# below 500. A plain GET answers with a redirect, so the status is recorded, not required to be 200.
set -u

endpoint="https://knowledge-mcp.global.api.aws"
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "$endpoint")"
rc=$?
if [ "$rc" -ne 0 ]; then
    echo "aws-agents: $endpoint unreachable (curl exit $rc)"
    exit 1
fi
if [ "$code" -ge 500 ] || [ "$code" -eq 0 ]; then
    echo "aws-agents: $endpoint answered HTTP $code"
    exit 1
fi
echo "aws-agents: $endpoint reachable (HTTP $code)"
