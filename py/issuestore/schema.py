"""SQLite schema for the in-tree issuestore MCP server.

Defines the 4-table schema (issues, labels, deps, metadata), 3 indexes, and
the 4-PRAGMA hook (journal_mode=WAL, busy_timeout=5000, foreign_keys=ON,
synchronous=NORMAL) that MUST be applied on every connection so CASCADE and
WAL semantics are actually live (R-DATA-2, C-12).
"""

from sqlalchemy import (
    CheckConstraint,
    Column,
    ForeignKey,
    Index,
    Integer,
    MetaData,
    Table,
    Text,
    create_engine,
    event,
)

metadata = MetaData()

issues = Table(
    "issues",
    metadata,
    Column("id", Text, primary_key=True),
    Column("title", Text, nullable=False, server_default=""),
    Column("description", Text, nullable=False, server_default=""),
    Column("status", Text, nullable=False, server_default="open"),
    Column("issue_type", Text, nullable=False, server_default="task"),
    Column("assignee", Text, nullable=False, server_default=""),
    Column("priority", Integer, nullable=False, server_default="2"),
    Column("parent_id", Text, nullable=False, server_default=""),
    Column("actor", Text, nullable=False, server_default=""),
    Column("notes", Text, nullable=False, server_default=""),
    Column("close_reason", Text, nullable=False, server_default=""),
    Column("created_at", Text, nullable=False),
    Column("updated_at", Text, nullable=False),
    CheckConstraint(
        "parent_id = '' OR assignee != ''",
        name="ck_issues_parent_requires_assignee",
    ),
)

labels = Table(
    "labels",
    metadata,
    Column("issue_id", Text, ForeignKey("issues.id", ondelete="CASCADE"), nullable=False),
    Column("position", Integer, nullable=False),
    Column("value", Text, nullable=False),
)

deps = Table(
    "deps",
    metadata,
    Column("issue_id", Text, ForeignKey("issues.id", ondelete="CASCADE"), nullable=False),
    Column("depends_on_id", Text, ForeignKey("issues.id", ondelete="CASCADE"), nullable=False),
)

metadata_kv = Table(
    "metadata",
    metadata,
    Column("key", Text, primary_key=True),
    Column("value", Text, nullable=False),
)

Index("idx_issues_parent_status", issues.c.parent_id, issues.c.status)
Index("idx_issues_assignee", issues.c.assignee)
Index("idx_issues_created_at", issues.c.created_at)


def _set_pragmas(dbapi_conn, _connection_record):
    cursor = dbapi_conn.cursor()
    cursor.execute("PRAGMA journal_mode=WAL")
    cursor.execute("PRAGMA busy_timeout=5000")
    cursor.execute("PRAGMA foreign_keys=ON")
    cursor.execute("PRAGMA synchronous=NORMAL")
    cursor.close()


def create_db(db_path: str):
    engine = create_engine(
        f"sqlite:///{db_path}",
        pool_size=4,
        max_overflow=4,
        pool_pre_ping=True,
    )
    event.listen(engine, "connect", _set_pragmas)

    # SQLAlchemy doesn't emit inline PRIMARY KEY for composite keys from the
    # Table() above without explicit PrimaryKeyConstraint. Use raw DDL via
    # create_all after rewriting the tables' primary-key definition.
    _migrate(engine)
    _create_all(engine)
    return engine


def _create_all(engine):
    """Create tables with composite PKs that SQLAlchemy Table() doesn't express inline.

    labels and deps use composite primary keys; we emit the DDL directly so the
    schema matches design-doc.md:144-182 exactly (PRIMARY KEY (issue_id, position)
    and PRIMARY KEY (issue_id, depends_on_id)).
    """
    ddl_statements = [
        """
        CREATE TABLE IF NOT EXISTS issues (
            id            TEXT PRIMARY KEY,
            title         TEXT NOT NULL DEFAULT '',
            description   TEXT NOT NULL DEFAULT '',
            status        TEXT NOT NULL DEFAULT 'open',
            issue_type    TEXT NOT NULL DEFAULT 'task',
            assignee      TEXT NOT NULL DEFAULT '',
            priority      INTEGER NOT NULL DEFAULT 2,
            parent_id     TEXT NOT NULL DEFAULT '',
            actor         TEXT NOT NULL DEFAULT '',
            notes         TEXT NOT NULL DEFAULT '',
            close_reason  TEXT NOT NULL DEFAULT '',
            created_at    TEXT NOT NULL,
            updated_at    TEXT NOT NULL,
            CHECK (parent_id = '' OR assignee != '')
        )
        """,
        """
        CREATE TABLE IF NOT EXISTS labels (
            issue_id  TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
            position  INTEGER NOT NULL,
            value     TEXT NOT NULL,
            PRIMARY KEY (issue_id, position)
        )
        """,
        """
        CREATE TABLE IF NOT EXISTS deps (
            issue_id       TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
            depends_on_id  TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
            PRIMARY KEY (issue_id, depends_on_id)
        )
        """,
        """
        CREATE TABLE IF NOT EXISTS metadata (
            key    TEXT PRIMARY KEY,
            value  TEXT NOT NULL
        )
        """,
        "CREATE INDEX IF NOT EXISTS idx_issues_parent_status ON issues(parent_id, status)",
        "CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(assignee)",
        "CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at)",
    ]
    with engine.begin() as conn:
        for stmt in ddl_statements:
            conn.exec_driver_sql(stmt)


def _migrate(engine):
    """Run forward migrations. Idempotent; uses metadata.schema_version.

    Both the v1→v2 migration and the v2 FK repair use a raw DBAPI connection
    so that PRAGMA foreign_keys=OFF and PRAGMA legacy_alter_table=ON take
    effect. These pragmas are no-ops inside SQLAlchemy's engine.begin()
    transaction — SQLite silently ignores them mid-transaction.
    """
    from sqlalchemy import text
    with engine.begin() as conn:
        exists_row = conn.execute(text(
            "SELECT name FROM sqlite_master WHERE type='table' AND name='issues'"
        )).fetchone()
        if not exists_row:
            return  # fresh DB; _create_all will install v2 schema directly
        row = conn.execute(text("SELECT value FROM metadata WHERE key='schema_version'")).fetchone()
        version = int(row[0]) if row else 1

    if version < 2:
        _migrate_v1_to_v2(engine)

    _repair_broken_v2_fk(engine)


def _migrate_v1_to_v2(engine):
    """v1→v2: add CHECK (parent_id = '' OR assignee != '') via table rewrite."""
    print("issuestore: migrating schema v1 → v2 (parent_id/assignee CHECK invariant)", flush=True)
    raw_conn = engine.raw_connection()
    try:
        cur = raw_conn.cursor()
        cur.execute("PRAGMA foreign_keys=OFF")
        cur.execute("PRAGMA legacy_alter_table=ON")
        cur.execute("BEGIN")
        try:
            cur.execute("""
                UPDATE issues
                SET assignee = COALESCE(
                    (SELECT p.assignee FROM issues p WHERE p.id = issues.parent_id AND p.assignee != ''),
                    '__system__'
                ),
                updated_at = strftime('%Y-%m-%dT%H:%M:%SZ','now')
                WHERE parent_id != '' AND assignee = ''
            """)
            cur.execute("ALTER TABLE issues RENAME TO issues_pre_v2")
            cur.execute("""
                CREATE TABLE issues (
                    id            TEXT PRIMARY KEY,
                    title         TEXT NOT NULL DEFAULT '',
                    description   TEXT NOT NULL DEFAULT '',
                    status        TEXT NOT NULL DEFAULT 'open',
                    issue_type    TEXT NOT NULL DEFAULT 'task',
                    assignee      TEXT NOT NULL DEFAULT '',
                    priority      INTEGER NOT NULL DEFAULT 2,
                    parent_id     TEXT NOT NULL DEFAULT '',
                    actor         TEXT NOT NULL DEFAULT '',
                    notes         TEXT NOT NULL DEFAULT '',
                    close_reason  TEXT NOT NULL DEFAULT '',
                    created_at    TEXT NOT NULL,
                    updated_at    TEXT NOT NULL,
                    CHECK (parent_id = '' OR assignee != '')
                )
            """)
            cur.execute("INSERT INTO issues SELECT * FROM issues_pre_v2")
            cur.execute("DROP TABLE issues_pre_v2")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_issues_parent_status ON issues(parent_id, status)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_issues_assignee ON issues(assignee)")
            cur.execute("CREATE INDEX IF NOT EXISTS idx_issues_created_at ON issues(created_at)")
            cur.execute("INSERT OR REPLACE INTO metadata(key, value) VALUES('schema_version', '2')")
            cur.execute("COMMIT")
        except Exception:
            cur.execute("ROLLBACK")
            raise
        finally:
            cur.execute("PRAGMA foreign_keys=ON")
            cur.execute("PRAGMA legacy_alter_table=OFF")
            cur.close()
    finally:
        raw_conn.close()
    print("issuestore: schema migration v2 complete", flush=True)


def _repair_broken_v2_fk(engine):
    """Detect and repair labels/deps FK references stranded on issues_pre_v2."""
    from sqlalchemy import text
    with engine.begin() as conn:
        row = conn.execute(text(
            "SELECT sql FROM sqlite_master WHERE name = 'labels'"
        )).fetchone()
        if not row or "issues_pre_v2" not in row[0]:
            return

    print("issuestore: repairing broken FK references in labels/deps", flush=True)
    raw_conn = engine.raw_connection()
    try:
        cur = raw_conn.cursor()
        cur.execute("PRAGMA foreign_keys=OFF")
        cur.execute("BEGIN")
        try:
            cur.execute("CREATE TEMP TABLE labels_bak AS SELECT * FROM labels")
            cur.execute("CREATE TEMP TABLE deps_bak AS SELECT * FROM deps")
            cur.execute("DROP TABLE labels")
            cur.execute("DROP TABLE deps")
            cur.execute("""
                CREATE TABLE labels (
                    issue_id  TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
                    position  INTEGER NOT NULL,
                    value     TEXT NOT NULL,
                    PRIMARY KEY (issue_id, position)
                )
            """)
            cur.execute("""
                CREATE TABLE deps (
                    issue_id       TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
                    depends_on_id  TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
                    PRIMARY KEY (issue_id, depends_on_id)
                )
            """)
            cur.execute("INSERT INTO labels SELECT * FROM labels_bak")
            cur.execute("INSERT INTO deps SELECT * FROM deps_bak")
            cur.execute("DROP TABLE labels_bak")
            cur.execute("DROP TABLE deps_bak")
            cur.execute("COMMIT")
        except Exception:
            cur.execute("ROLLBACK")
            raise
        finally:
            cur.execute("PRAGMA foreign_keys=ON")
            cur.close()
    finally:
        raw_conn.close()
    print("issuestore: FK repair complete", flush=True)
