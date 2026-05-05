"""aiohttp + JSON-RPC 2.0 server for the issuestore MCP tools.

Binds to 127.0.0.1 ONLY (R-SEC-1). Writes an endpoint file after the socket is
listening. On SIGTERM: drain aiohttp → engine.dispose() (forces WAL checkpoint
via last-connection-close) → remove endpoint file → sys.exit(0).

CLI:
    python3 -m py.issuestore.server --db-path PATH --endpoint-file PATH [--port N]
"""

import argparse
import asyncio
import json
import os
import signal
import sys
from datetime import datetime, timezone

from aiohttp import web

from . import store
from .schema import create_db


TOOLS = [
    {
        "name": "issuestore_get",
        "description": "Get an issue by id.",
        "inputSchema": {
            "type": "object",
            "properties": {"id": {"type": "string"}},
            "required": ["id"],
        },
    },
    {
        "name": "issuestore_list",
        "description": "List issues matching a filter.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "parent": {"type": "string"},
                "statuses": {"type": "array", "items": {"type": "string"}},
                "type": {"type": "string"},
                "assignee": {"type": "string"},
                "labels": {"type": "array", "items": {"type": "string"}},
                "include_all_agents": {"type": "boolean"},
                "include_closed": {"type": "boolean"},
            },
        },
    },
    {
        "name": "issuestore_ready",
        "description": "List non-terminal issues whose deps are all terminal.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "molecule_id": {"type": "string"},
                "parent": {"type": "string"},
                "assignee": {"type": "string"},
                "include_all_agents": {"type": "boolean"},
            },
        },
    },
    {
        "name": "issuestore_create",
        "description": "Create a new issue.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "title": {"type": "string"},
                "description": {"type": "string"},
                "type": {"type": "string"},
                "assignee": {"type": "string"},
                "priority": {"type": "integer"},
                "parent": {"type": "string"},
                "labels": {"type": "array", "items": {"type": "string"}},
                "actor": {"type": "string"},
            },
            "required": ["title", "type"],
        },
    },
    {
        "name": "issuestore_patch",
        "description": "Update an issue's notes or other mutable fields. Never touches description.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "id": {"type": "string"},
                "notes": {"type": "string"},
                "title": {"type": "string"},
                "status": {"type": "string"},
                "priority": {"type": "integer"},
                "assignee": {"type": "string"},
                "parent": {"type": "string"},
                "type": {"type": "string"},
                "labels": {"type": "array", "items": {"type": "string"}},
            },
            "required": ["id"],
        },
    },
    {
        "name": "issuestore_close",
        "description": "Close an issue. Sets status=closed and close_reason. Never touches description.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "id": {"type": "string"},
                "reason": {"type": "string"},
            },
            "required": ["id"],
        },
    },
    {
        "name": "issuestore_dep_add",
        "description": "Add a dependency edge between two issues.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "issue_id": {"type": "string"},
                "depends_on_id": {"type": "string"},
            },
            "required": ["issue_id", "depends_on_id"],
        },
    },
    {
        "name": "issuestore_render",
        "description": "Render a single issue as structured text.",
        "inputSchema": {
            "type": "object",
            "properties": {"id": {"type": "string"}},
            "required": ["id"],
        },
    },
    {
        "name": "issuestore_render_list",
        "description": "Render a filtered list of issues as structured text.",
        "inputSchema": {
            "type": "object",
            "properties": {
                "parent": {"type": "string"},
                "statuses": {"type": "array", "items": {"type": "string"}},
                "type": {"type": "string"},
                "assignee": {"type": "string"},
                "include_all_agents": {"type": "boolean"},
                "include_closed": {"type": "boolean"},
            },
        },
    },
]


_engine = None
_endpoint_file = None
_shutdown_event = None


async def handle_jsonrpc(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except Exception as e:
        return web.json_response(
            {"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": f"parse error: {e}"}}
        )

    method = body.get("method")
    rpc_id = body.get("id")
    params = body.get("params") or {}

    if method == "initialize":
        return web.json_response({
            "jsonrpc": "2.0",
            "id": rpc_id,
            "result": {"serverInfo": {"name": "issuestore", "version": "0.1.0"}},
        })

    if method == "tools/list":
        return web.json_response({
            "jsonrpc": "2.0",
            "id": rpc_id,
            "result": {"tools": TOOLS},
        })

    if method == "tools/call":
        name = params.get("name")
        args = params.get("arguments") or {}
        handler = getattr(store, name, None) if name else None
        if handler is None or not name or not name.startswith("issuestore_"):
            return web.json_response({
                "jsonrpc": "2.0",
                "id": rpc_id,
                "error": {"code": -32601, "message": f"unknown tool: {name}"},
            })
        try:
            result = await asyncio.to_thread(handler, _engine, args)
            return web.json_response({
                "jsonrpc": "2.0",
                "id": rpc_id,
                "result": {"content": [{"type": "text", "text": json.dumps(result)}]},
            })
        except Exception as e:
            return web.json_response({
                "jsonrpc": "2.0",
                "id": rpc_id,
                "result": {
                    "content": [{"type": "text", "text": str(e)}],
                    "isError": True,
                },
            })

    return web.json_response({
        "jsonrpc": "2.0",
        "id": rpc_id,
        "error": {"code": -32601, "message": f"unknown method: {method}"},
    })


async def handle_health(_request: web.Request) -> web.Response:
    return web.json_response({"status": "ok"})


def _write_endpoint(endpoint_file: str, port: int) -> None:
    payload = {
        "transport": "http",
        "address": f"127.0.0.1:{port}",
        "pid": os.getpid(),
        "started_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
    }
    tmp = endpoint_file + ".tmp"
    with open(tmp, "w") as f:
        json.dump(payload, f)
    os.replace(tmp, endpoint_file)


def _remove_endpoint() -> None:
    if _endpoint_file and os.path.exists(_endpoint_file):
        try:
            os.remove(_endpoint_file)
        except OSError:
            pass


def _install_signal_handlers(loop: asyncio.AbstractEventLoop) -> None:
    def _trigger():
        if _shutdown_event is not None and not _shutdown_event.is_set():
            _shutdown_event.set()

    for sig in (signal.SIGTERM, signal.SIGINT):
        try:
            loop.add_signal_handler(sig, _trigger)
        except NotImplementedError:
            signal.signal(sig, lambda *_: _trigger())


async def _serve(db_path: str, endpoint_file: str, port: int) -> int:
    global _engine, _endpoint_file, _shutdown_event

    _engine = create_db(db_path)
    _endpoint_file = endpoint_file
    _shutdown_event = asyncio.Event()

    app = web.Application()
    app.router.add_post("/", handle_jsonrpc)
    app.router.add_get("/health", handle_health)

    runner = web.AppRunner(app)
    await runner.setup()
    # R-SEC-1: literal 127.0.0.1, never 0.0.0.0, never "localhost".
    site = web.TCPSite(runner, host="127.0.0.1", port=port)
    await site.start()

    actual_port = site._server.sockets[0].getsockname()[1]
    _write_endpoint(endpoint_file, actual_port)

    loop = asyncio.get_running_loop()
    _install_signal_handlers(loop)

    try:
        await _shutdown_event.wait()
    finally:
        # Drain aiohttp → dispose engine (flushes WAL via last-connection-close) → remove endpoint file.
        await runner.cleanup()
        if _engine is not None:
            _engine.dispose()
        _remove_endpoint()
    return 0


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="issuestore MCP server (Phase 4)")
    parser.add_argument("--db-path", required=True)
    parser.add_argument("--endpoint-file", required=True)
    parser.add_argument("--port", type=int, default=0, help="0 = ephemeral")
    args = parser.parse_args(argv)
    return asyncio.run(_serve(args.db_path, args.endpoint_file, args.port))


if __name__ == "__main__":
    sys.exit(main())
