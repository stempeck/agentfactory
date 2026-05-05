"""Nine MCP tool handlers mapping 1:1 onto issuestore.Store.

Each handler takes (engine, args) and returns a JSON-serializable Python value.
Server.py wraps the returned value in a JSON-RPC tools/call content envelope.

Invariants load-bearing for Phase 5 RunStoreContract:
    - Issue DTO JSON keys match store.go:43-58 exactly.
    - BlockedBy is [{"id": "af-..."}] (NOT [string]).
    - Priority is int on the wire (0=urgent, 1=high, 2=normal, 3=low).
    - Patch/Close MUST NOT mutate Description (C-10).
    - Ready orders by created_at ASC, id ASC and excludes issues with any
      dep target whose status is not ('closed', 'done').
"""

import uuid
from datetime import datetime, timezone
from sqlalchemy import text


NON_TERMINAL_STATUSES = ("open", "hooked", "pinned", "in_progress")
TERMINAL_STATUSES = ("closed", "done")

PRIORITY_NAMES = {0: "urgent", 1: "high", 2: "normal", 3: "low"}


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _new_id() -> str:
    return f"af-{uuid.uuid4().hex[:8]}"


def _labels_for(conn, issue_id):
    rows = conn.execute(
        text("SELECT value FROM labels WHERE issue_id = :id ORDER BY position ASC"),
        {"id": issue_id},
    ).fetchall()
    return [r[0] for r in rows]


def _deps_for(conn, issue_id):
    rows = conn.execute(
        text("SELECT depends_on_id FROM deps WHERE issue_id = :id ORDER BY depends_on_id ASC"),
        {"id": issue_id},
    ).fetchall()
    return [{"id": r[0]} for r in rows]


def _labels_map_for_ids(conn, issue_ids):
    """Prefetch labels for a set of issue IDs in a single query. Returns
    {issue_id: [value, ...]} with label order preserved per position ASC."""
    if not issue_ids:
        return {}
    placeholders = ", ".join(f":id{i}" for i in range(len(issue_ids)))
    params = {f"id{i}": iid for i, iid in enumerate(issue_ids)}
    rows = conn.execute(
        text(
            f"SELECT issue_id, value FROM labels "
            f"WHERE issue_id IN ({placeholders}) "
            f"ORDER BY issue_id ASC, position ASC"
        ),
        params,
    ).fetchall()
    out = {}
    for r in rows:
        out.setdefault(r[0], []).append(r[1])
    return out


def _deps_map_for_ids(conn, issue_ids):
    """Prefetch deps for a set of issue IDs in a single query. Returns
    {issue_id: [{"id": depends_on_id}, ...]} preserving depends_on_id ASC."""
    if not issue_ids:
        return {}
    placeholders = ", ".join(f":id{i}" for i in range(len(issue_ids)))
    params = {f"id{i}": iid for i, iid in enumerate(issue_ids)}
    rows = conn.execute(
        text(
            f"SELECT issue_id, depends_on_id FROM deps "
            f"WHERE issue_id IN ({placeholders}) "
            f"ORDER BY issue_id ASC, depends_on_id ASC"
        ),
        params,
    ).fetchall()
    out = {}
    for r in rows:
        out.setdefault(r[0], []).append({"id": r[1]})
    return out


def _issue_from_row(conn, row, labels_map=None, deps_map=None):
    issue_id = row["id"]
    labels = labels_map.get(issue_id, []) if labels_map is not None else _labels_for(conn, issue_id)
    d = {
        "id": issue_id,
        "title": row["title"],
        "description": row["description"],
        "assignee": row["assignee"],
        "type": row["issue_type"],
        "status": row["status"],
        "priority": int(row["priority"]),
        "labels": labels,
        "notes": row["notes"],
        "close_reason": row["close_reason"],
        "created_at": row["created_at"],
        "updated_at": row["updated_at"],
    }
    parent = row["parent_id"]
    if parent:
        d["parent"] = parent
    blocked = deps_map.get(issue_id, []) if deps_map is not None else _deps_for(conn, issue_id)
    if blocked:
        d["blocked_by"] = blocked
    return d


def _row_to_dict(sa_row):
    return {k: sa_row._mapping[k] for k in sa_row._mapping.keys()}


def _fetch_issue_raw(conn, issue_id):
    row = conn.execute(
        text("SELECT * FROM issues WHERE id = :id"), {"id": issue_id}
    ).fetchone()
    if row is None:
        raise KeyError(f"issue not found: {issue_id}")
    return _issue_from_row(conn, _row_to_dict(row))


# ------------------------------------------------------------------ Create

def issuestore_create(engine, args):
    issue_id = _new_id()
    now = _now_iso()
    title = args.get("title", "")
    description = args.get("description", "")
    status = args.get("status", "open") or "open"
    issue_type = args.get("type", "task") or "task"
    assignee = args.get("assignee", "")
    priority = args.get("priority", 2)
    if priority is None:
        priority = 2
    parent_id = args.get("parent", "") or args.get("parent_id", "") or ""
    actor = args.get("actor", "")
    labels = args.get("labels") or []

    with engine.begin() as conn:
        conn.execute(
            text("""
                INSERT INTO issues (
                    id, title, description, status, issue_type, assignee,
                    priority, parent_id, actor, notes, close_reason,
                    created_at, updated_at
                ) VALUES (
                    :id, :title, :description, :status, :issue_type, :assignee,
                    :priority, :parent_id, :actor, '', '',
                    :created_at, :updated_at
                )
            """),
            {
                "id": issue_id,
                "title": title,
                "description": description,
                "status": status,
                "issue_type": issue_type,
                "assignee": assignee,
                "priority": int(priority),
                "parent_id": parent_id,
                "actor": actor,
                "created_at": now,
                "updated_at": now,
            },
        )
        for pos, value in enumerate(labels):
            conn.execute(
                text("INSERT INTO labels (issue_id, position, value) VALUES (:id, :pos, :val)"),
                {"id": issue_id, "pos": pos, "val": value},
            )
        return _fetch_issue_raw(conn, issue_id)


# ------------------------------------------------------------------ Get

def issuestore_get(engine, args):
    issue_id = args["id"]
    with engine.begin() as conn:
        return _fetch_issue_raw(conn, issue_id)


# ------------------------------------------------------------------ List

def _list_filter_clause(args):
    """Build WHERE clauses and params for List / Ready filtering.

    Rules (store.go:138-145):
      - statuses nil/empty → non-terminal set.
      - statuses non-empty → OR of listed values.
      - include_closed=True AND statuses nil → include terminal too.
      - include_all_agents=False AND assignee != "" → filter assignee=?
    """
    clauses = []
    params = {}
    statuses = args.get("statuses") or []
    include_closed = bool(args.get("include_closed", False))
    if statuses:
        # OR of provided statuses, ignoring include_closed (caller was explicit).
        named = []
        for i, s in enumerate(statuses):
            key = f"st{i}"
            params[key] = s
            named.append(f":{key}")
        clauses.append(f"status IN ({', '.join(named)})")
    else:
        default = list(NON_TERMINAL_STATUSES)
        if include_closed:
            default = default + list(TERMINAL_STATUSES)
        named = []
        for i, s in enumerate(default):
            key = f"dst{i}"
            params[key] = s
            named.append(f":{key}")
        clauses.append(f"status IN ({', '.join(named)})")

    if args.get("parent") or args.get("molecule_id"):
        parent = args.get("parent") or args.get("molecule_id")
        clauses.append("parent_id = :parent")
        params["parent"] = parent

    issue_type = args.get("type")
    if issue_type:
        clauses.append("issue_type = :itype")
        params["itype"] = issue_type

    assignee = args.get("assignee", "")
    include_all_agents = bool(args.get("include_all_agents", False))
    if assignee and not include_all_agents:
        clauses.append("assignee = :assignee")
        params["assignee"] = assignee

    # Labels filter: ANDed (all must match). Only applied if a non-empty list.
    labels = args.get("labels") or []
    for i, lv in enumerate(labels):
        key = f"lv{i}"
        clauses.append(
            f"id IN (SELECT issue_id FROM labels WHERE value = :{key})"
        )
        params[key] = lv

    where = " AND ".join(clauses) if clauses else "1=1"
    return where, params


def issuestore_list(engine, args):
    where, params = _list_filter_clause(args)
    with engine.begin() as conn:
        rows = conn.execute(
            text(f"SELECT * FROM issues WHERE {where} ORDER BY created_at ASC, id ASC"),
            params,
        ).fetchall()
        dict_rows = [_row_to_dict(r) for r in rows]
        ids = [r["id"] for r in dict_rows]
        labels_map = _labels_map_for_ids(conn, ids)
        deps_map = _deps_map_for_ids(conn, ids)
        return [_issue_from_row(conn, r, labels_map, deps_map) for r in dict_rows]


# ------------------------------------------------------------------ Ready

def issuestore_ready(engine, args):
    """Return non-terminal issues whose every dep is terminal, ordered by created_at ASC, id ASC."""
    where, params = _list_filter_clause(args)
    molecule_id = args.get("molecule_id", "") or args.get("parent", "") or ""
    query = f"""
        SELECT * FROM issues
        WHERE {where}
          AND NOT EXISTS (
            SELECT 1 FROM deps d
            JOIN issues t ON t.id = d.depends_on_id
            WHERE d.issue_id = issues.id
              AND t.status NOT IN ('closed', 'done')
          )
        ORDER BY created_at ASC, id ASC
    """
    with engine.begin() as conn:
        rows = conn.execute(text(query), params).fetchall()
        dict_rows = [_row_to_dict(r) for r in rows]
        ids = [r["id"] for r in dict_rows]
        labels_map = _labels_map_for_ids(conn, ids)
        deps_map = _deps_map_for_ids(conn, ids)
        steps = [_issue_from_row(conn, r, labels_map, deps_map) for r in dict_rows]
        total_steps = len(steps)
        if molecule_id:
            row = conn.execute(
                text("SELECT COUNT(*) FROM issues WHERE parent_id = :mid"),
                {"mid": molecule_id},
            ).fetchone()
            if row:
                total_steps = row[0]
        return {
            "steps": steps,
            "total_steps": total_steps,
            "molecule_id": molecule_id,
        }


# ------------------------------------------------------------------ Patch

def issuestore_patch(engine, args):
    """Update Notes (and other permitted fields) without touching Description. C-10.

    Accepted fields: notes, title, status, priority, assignee, parent, type, labels.
    description is deliberately NOT accepted — Patch must never mutate it.
    """
    issue_id = args["id"]
    now = _now_iso()
    sets = []
    params = {"id": issue_id, "updated_at": now}

    if "notes" in args and args["notes"] is not None:
        sets.append("notes = :notes")
        params["notes"] = args["notes"]
    if "title" in args and args["title"] is not None:
        sets.append("title = :title")
        params["title"] = args["title"]
    if "status" in args and args["status"] is not None:
        sets.append("status = :status")
        params["status"] = args["status"]
    if "priority" in args and args["priority"] is not None:
        sets.append("priority = :priority")
        params["priority"] = int(args["priority"])
    if "assignee" in args and args["assignee"] is not None:
        sets.append("assignee = :assignee")
        params["assignee"] = args["assignee"]
    if "parent" in args and args["parent"] is not None:
        sets.append("parent_id = :parent_id")
        params["parent_id"] = args["parent"]
    if "type" in args and args["type"] is not None:
        sets.append("issue_type = :issue_type")
        params["issue_type"] = args["type"]

    sets.append("updated_at = :updated_at")

    with engine.begin() as conn:
        conn.execute(
            text(f"UPDATE issues SET {', '.join(sets)} WHERE id = :id"),
            params,
        )
        if "labels" in args and args["labels"] is not None:
            conn.execute(
                text("DELETE FROM labels WHERE issue_id = :id"),
                {"id": issue_id},
            )
            for pos, value in enumerate(args["labels"]):
                conn.execute(
                    text("INSERT INTO labels (issue_id, position, value) VALUES (:id, :pos, :val)"),
                    {"id": issue_id, "pos": pos, "val": value},
                )
    return None


# ------------------------------------------------------------------ Close

def issuestore_close(engine, args):
    """Set status=closed + close_reason without touching description. C-10."""
    issue_id = args["id"]
    reason = args.get("reason", "") or ""
    now = _now_iso()
    with engine.begin() as conn:
        conn.execute(
            text("""
                UPDATE issues
                SET status = 'closed',
                    close_reason = :reason,
                    updated_at = :updated_at
                WHERE id = :id
            """),
            {"id": issue_id, "reason": reason, "updated_at": now},
        )
    return None


# ------------------------------------------------------------------ DepAdd

def issuestore_dep_add(engine, args):
    issue_id = args["issue_id"]
    depends_on_id = args["depends_on_id"]
    with engine.begin() as conn:
        conn.execute(
            text("INSERT OR IGNORE INTO deps (issue_id, depends_on_id) VALUES (:a, :b)"),
            {"a": issue_id, "b": depends_on_id},
        )
    return None


# ------------------------------------------------------------------ Render

_RENDER_TEMPLATE_HEADERS = [
    "Issue", "Title", "Status", "Type", "Priority", "Assignee",
    "Parent", "Labels", "Description", "Notes", "Close Reason",
    "Dependencies", "Created", "Updated",
]


def _render_issue(issue: dict) -> str:
    priority_name = PRIORITY_NAMES.get(int(issue.get("priority", 2)), "normal")
    labels = issue.get("labels") or []
    parent = issue.get("parent", "")
    description = issue.get("description", "") or ""
    notes = issue.get("notes", "") or ""
    close_reason = issue.get("close_reason", "") or ""
    blocked_by = issue.get("blocked_by") or []

    def indent_block(body: str, empty_placeholder: str) -> str:
        if not body:
            return f"  {empty_placeholder}"
        return "\n".join(f"  {line}" for line in body.splitlines())

    deps_repr = ", ".join(r["id"] for r in blocked_by) if blocked_by else "(none)"
    labels_repr = ", ".join(labels) if labels else "(none)"
    parent_repr = parent if parent else "(none)"

    lines = [
        f"Issue: {issue['id']}",
        f"Title: {issue.get('title', '')}",
        f"Status: {issue.get('status', '')}",
        f"Type: {issue.get('type', '')}",
        f"Priority: {priority_name}",
        f"Assignee: {issue.get('assignee', '')}",
        f"Parent: {parent_repr}",
        f"Labels: {labels_repr}",
        "Description:",
        indent_block(description, "(empty)"),
        "Notes:",
        indent_block(notes, "(none)"),
        "Close Reason:",
        indent_block(close_reason, "(none)"),
        f"Dependencies: {deps_repr}",
        f"Created: {issue.get('created_at', '')}",
        f"Updated: {issue.get('updated_at', '')}",
    ]
    return "\n".join(lines)


def issuestore_render(engine, args):
    issue_id = args["id"]
    with engine.begin() as conn:
        issue = _fetch_issue_raw(conn, issue_id)
    return _render_issue(issue)


def issuestore_render_list(engine, args):
    issues = issuestore_list(engine, args)
    return "\n\n".join(_render_issue(i) for i in issues)
