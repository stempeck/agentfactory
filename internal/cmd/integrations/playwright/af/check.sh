#!/usr/bin/env bash
# Two probes, both bounded: the prefix CLI renders a page through chromium, then the locked MCP server
# the plugin launches answers initialize and tools/list over stdio and performs one real navigation.
# macOS /bin/bash 3.2 has no coproc and macOS has no coreutils timeout, so the server talks over two
# FIFOs and every wait is a kill -0 poll whose deadline lives in this shell.
set -u

prefix="$HOME/.local/share/agentfactory/playwright/1.63.0"
pw="$prefix/node_modules/.bin/playwright"
mcp="$prefix/node_modules/.bin/playwright-mcp"
render_budget=60
mcp_budget=90
fail() { echo "playwright: $1"; exit 1; }

[ -x "$pw" ] || fail "the playwright CLI is not installed at $pw"
[ -x "$mcp" ] || fail "the Playwright MCP server is not installed at $mcp"

work="$(mktemp -d "${TMPDIR:-$HOME}/af-playwright-check.XXXXXX")" || fail "cannot create a scratch dir"
render_pid="" mcp_pid="" drain_pid=""
cleanup() {
    for pid in $render_pid $mcp_pid $drain_pid; do kill "$pid" 2>/dev/null; done
    rm -rf "$work"
}
trap cleanup EXIT
trap '' PIPE

"$pw" screenshot "data:text/html,<h1>af</h1>" "$work/probe.png" >/dev/null 2>&1 &
render_pid=$!
while kill -0 "$render_pid" 2>/dev/null && [ "$render_budget" -gt 0 ]; do
    sleep 1
    render_budget=$((render_budget - 1))
done
kill -0 "$render_pid" 2>/dev/null && fail "chromium did not render the probe page within 60s (playwright screenshot)"
if ! wait "$render_pid" || [ ! -s "$work/probe.png" ]; then
    fail "chromium did not render the probe page (playwright screenshot)"
fi
render_pid=""

mkfifo "$work/in" "$work/out" || fail "cannot create the MCP server's FIFOs"
"$mcp" --browser chromium --headless --isolated <"$work/in" >"$work/out" 2>"$work/mcp.err" &
mcp_pid=$!
cat "$work/out" >"$work/replies" &
drain_pid=$!
exec 3>"$work/in"

send() { printf '%s\n' "$1" >&3 2>/dev/null; }
# One budget across every request: a server that answers each one just inside a per-request budget
# would still overrun the [check] timeout.
reply() {
    local live
    while :; do
        live=no
        { kill -0 "$mcp_pid" || kill -0 "$drain_pid"; } 2>/dev/null && live=yes
        r="$(grep -E "\"id\":$1[,}]" "$work/replies" | head -n 1)"
        [ -n "$r" ] && return 0
        [ "$live" = yes ] && [ "$mcp_budget" -gt 0 ] || return 1
        sleep 1
        mcp_budget=$((mcp_budget - 1))
    done
}

send '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"af-check","version":"1"}}}'
reply 1 || fail "$mcp did not answer initialize: $(tail -3 "$work/mcp.err")"
case "$r" in *serverInfo*) ;; *) fail "$mcp answered initialize without serverInfo" ;; esac
send '{"jsonrpc":"2.0","method":"notifications/initialized"}'

send '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
reply 2 || fail "$mcp did not answer tools/list"
case "$r" in *'"browser_navigate"'*) ;; *) fail "$mcp lists no browser_navigate tool" ;; esac

send '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"browser_navigate","arguments":{"url":"data:text/html,<title>af-check</title><h1>af</h1>"}}}'
reply 3 || fail "$mcp did not answer the browser_navigate call"
case "$r" in
    *'"isError":true'*) fail "browser_navigate failed: ${r:0:300}" ;;
    *af-check*) ;;
    *) fail "browser_navigate did not reach the probe page: ${r:0:300}" ;;
esac

echo "playwright: chromium renders and the locked MCP server navigates"
