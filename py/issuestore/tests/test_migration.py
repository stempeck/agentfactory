"""Tests for the v1 → v2 schema migration in py/issuestore/schema.py.

The migration installs the data-plane invariant
``parent_id = '' OR assignee != ''`` via SQLite's RENAME → CREATE → INSERT
SELECT → DROP rewrite-dance (SQLite does not support ALTER TABLE ADD CHECK).
"""

from __future__ import annotations

import os
import tempfile
import unittest

from sqlalchemy import create_engine, event, text

from .. import schema


V1_ISSUES_DDL = """
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
    updated_at    TEXT NOT NULL
)
"""

V1_LABELS_DDL = """
CREATE TABLE labels (
    issue_id  TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    position  INTEGER NOT NULL,
    value     TEXT NOT NULL,
    PRIMARY KEY (issue_id, position)
)
"""

V1_DEPS_DDL = """
CREATE TABLE deps (
    issue_id       TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    depends_on_id  TEXT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    PRIMARY KEY (issue_id, depends_on_id)
)
"""

V1_METADATA_DDL = """
CREATE TABLE metadata (
    key    TEXT PRIMARY KEY,
    value  TEXT NOT NULL
)
"""


def _make_v1_engine(db_path: str):
    engine = create_engine(f"sqlite:///{db_path}")
    event.listen(engine, "connect", schema._set_pragmas)
    with engine.begin() as conn:
        conn.exec_driver_sql(V1_ISSUES_DDL)
        conn.exec_driver_sql(V1_LABELS_DDL)
        conn.exec_driver_sql(V1_DEPS_DDL)
        conn.exec_driver_sql(V1_METADATA_DDL)
    return engine


class MigrationTest(unittest.TestCase):
    def setUp(self):
        fd, self.db_path = tempfile.mkstemp(suffix=".sqlite")
        os.close(fd)

    def tearDown(self):
        for suffix in ("", "-wal", "-shm"):
            p = self.db_path + suffix
            if os.path.exists(p):
                os.remove(p)

    def test_migration_backfills_empty_assignee_children(self):
        engine = _make_v1_engine(self.db_path)
        with engine.begin() as conn:
            conn.execute(text(
                "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                " VALUES ('epic-1', 'alice', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
            ))
            conn.execute(text(
                "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                " VALUES ('child-1', '', 'epic-1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
            ))
            conn.execute(text(
                "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                " VALUES ('orphan-1', '', 'missing-parent', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
            ))

        schema._migrate(engine)

        with engine.begin() as conn:
            child_assignee = conn.execute(
                text("SELECT assignee FROM issues WHERE id='child-1'")
            ).scalar_one()
            self.assertEqual(child_assignee, "alice")

            orphan_assignee = conn.execute(
                text("SELECT assignee FROM issues WHERE id='orphan-1'")
            ).scalar_one()
            self.assertEqual(orphan_assignee, "__system__")

            version = conn.execute(
                text("SELECT value FROM metadata WHERE key='schema_version'")
            ).scalar_one()
            self.assertEqual(version, "2")

            # CHECK constraint is now live on the migrated table.
            from sqlalchemy.exc import IntegrityError
            with self.assertRaises(IntegrityError):
                conn.execute(text(
                    "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                    " VALUES ('bad-1', '', 'epic-1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
                ))

        # Idempotency — second invocation is a no-op and must not re-rewrite.
        schema._migrate(engine)
        with engine.begin() as conn:
            version = conn.execute(
                text("SELECT value FROM metadata WHERE key='schema_version'")
            ).scalar_one()
            self.assertEqual(version, "2")

    def test_migration_preserves_labels_deps_fk_integrity(self):
        engine = _make_v1_engine(self.db_path)
        with engine.begin() as conn:
            conn.execute(text(
                "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                " VALUES ('epic-1', 'alice', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
            ))
            conn.execute(text(
                "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                " VALUES ('child-1', 'alice', 'epic-1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
            ))
            conn.execute(text(
                "INSERT INTO labels (issue_id, position, value)"
                " VALUES ('epic-1', 0, 'pre-migration-label')"
            ))
            conn.execute(text(
                "INSERT INTO deps (issue_id, depends_on_id)"
                " VALUES ('child-1', 'epic-1')"
            ))

        schema._migrate(engine)

        with engine.begin() as conn:
            # Pre-existing label data survived migration.
            pre_label = conn.execute(
                text("SELECT value FROM labels WHERE issue_id = 'epic-1'")
            ).scalar_one()
            self.assertEqual(pre_label, "pre-migration-label")

            # Pre-existing dep data survived migration.
            pre_dep = conn.execute(
                text("SELECT depends_on_id FROM deps WHERE issue_id = 'child-1'")
            ).scalar_one()
            self.assertEqual(pre_dep, "epic-1")

            # New label INSERT succeeds (this is the exact operation that fails
            # when FK text in labels points at dropped issues_pre_v2).
            conn.execute(text(
                "INSERT INTO labels (issue_id, position, value)"
                " VALUES ('epic-1', 1, 'post-migration-label')"
            ))
            post_label = conn.execute(
                text("SELECT value FROM labels WHERE issue_id = 'epic-1' AND position = 1")
            ).scalar_one()
            self.assertEqual(post_label, "post-migration-label")

            # New dep INSERT succeeds.
            conn.execute(text(
                "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                " VALUES ('child-2', 'alice', 'epic-1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
            ))
            conn.execute(text(
                "INSERT INTO deps (issue_id, depends_on_id)"
                " VALUES ('child-2', 'epic-1')"
            ))
            post_dep = conn.execute(
                text("SELECT depends_on_id FROM deps WHERE issue_id = 'child-2'")
            ).scalar_one()
            self.assertEqual(post_dep, "epic-1")

            # FK references in labels/deps point at "issues", not "issues_pre_v2".
            labels_ddl = conn.execute(
                text("SELECT sql FROM sqlite_master WHERE name = 'labels'")
            ).scalar_one()
            self.assertIn("issues", labels_ddl.lower())
            self.assertNotIn("issues_pre_v2", labels_ddl.lower())

            deps_ddl = conn.execute(
                text("SELECT sql FROM sqlite_master WHERE name = 'deps'")
            ).scalar_one()
            self.assertIn("issues", deps_ddl.lower())
            self.assertNotIn("issues_pre_v2", deps_ddl.lower())

            # PRAGMA foreign_key_check reports zero violations.
            fk_violations = conn.execute(text("PRAGMA foreign_key_check")).fetchall()
            self.assertEqual(fk_violations, [],
                             f"FK violations after migration: {fk_violations}")

    def test_broken_v2_db_is_repaired(self):
        """A DB already in the broken v2 state (dangling FK to issues_pre_v2)
        is repaired by the migration function."""
        engine = _make_v1_engine(self.db_path)
        with engine.begin() as conn:
            conn.execute(text(
                "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                " VALUES ('epic-1', 'alice', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
            ))
            conn.execute(text(
                "INSERT INTO labels (issue_id, position, value)"
                " VALUES ('epic-1', 0, 'surviving-label')"
            ))
            conn.execute(text(
                "INSERT INTO deps (issue_id, depends_on_id)"
                " VALUES ('epic-1', 'epic-1')"
            ))

        # Simulate the broken migration: rename issues → issues_pre_v2 (which
        # rewrites FK text in labels/deps), create new issues, copy, drop old.
        with engine.begin() as conn:
            conn.execute(text("PRAGMA foreign_keys=OFF"))
            conn.execute(text("ALTER TABLE issues RENAME TO issues_pre_v2"))
            conn.execute(text("""
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
            """))
            conn.execute(text("INSERT INTO issues SELECT * FROM issues_pre_v2"))
            conn.execute(text("DROP TABLE issues_pre_v2"))
            conn.execute(text("PRAGMA foreign_keys=ON"))
            conn.execute(text(
                "INSERT OR REPLACE INTO metadata(key, value)"
                " VALUES('schema_version', '2')"
            ))

        # Confirm the DB is actually broken: labels FK points at issues_pre_v2.
        with engine.begin() as conn:
            labels_ddl = conn.execute(
                text("SELECT sql FROM sqlite_master WHERE name = 'labels'")
            ).scalar_one()
            self.assertIn("issues_pre_v2", labels_ddl,
                          "Test setup failed: labels FK should reference issues_pre_v2")

        # Run the migration/repair — this should detect and fix the corruption.
        schema._migrate(engine)

        with engine.begin() as conn:
            # FK references now point at "issues".
            labels_ddl = conn.execute(
                text("SELECT sql FROM sqlite_master WHERE name = 'labels'")
            ).scalar_one()
            self.assertNotIn("issues_pre_v2", labels_ddl)

            deps_ddl = conn.execute(
                text("SELECT sql FROM sqlite_master WHERE name = 'deps'")
            ).scalar_one()
            self.assertNotIn("issues_pre_v2", deps_ddl)

            # INSERTs succeed.
            conn.execute(text(
                "INSERT INTO labels (issue_id, position, value)"
                " VALUES ('epic-1', 1, 'post-repair-label')"
            ))
            post_label = conn.execute(
                text("SELECT value FROM labels WHERE issue_id = 'epic-1' AND position = 1")
            ).scalar_one()
            self.assertEqual(post_label, "post-repair-label")

            # Pre-existing data survived repair.
            pre_label = conn.execute(
                text("SELECT value FROM labels WHERE issue_id = 'epic-1' AND position = 0")
            ).scalar_one()
            self.assertEqual(pre_label, "surviving-label")

            # PRAGMA foreign_key_check clean.
            fk_violations = conn.execute(text("PRAGMA foreign_key_check")).fetchall()
            self.assertEqual(fk_violations, [],
                             f"FK violations after repair: {fk_violations}")

    def test_migration_failure_rolls_back(self):
        engine = _make_v1_engine(self.db_path)
        with engine.begin() as conn:
            conn.execute(text(
                "INSERT INTO issues (id, assignee, parent_id, created_at, updated_at)"
                " VALUES ('epic-1', 'alice', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')"
            ))
            # Stage a conflicting rename target so RENAME TO issues_pre_v2 fails
            # mid-migration; engine.begin() must roll the transaction back.
            conn.exec_driver_sql(
                "CREATE TABLE issues_pre_v2 (id TEXT PRIMARY KEY)"
            )

        with self.assertRaises(Exception):
            schema._migrate(engine)

        with engine.begin() as conn:
            version_row = conn.execute(
                text("SELECT value FROM metadata WHERE key='schema_version'")
            ).fetchone()
            self.assertIsNone(version_row, "schema_version must be absent on rollback")

            epic_assignee = conn.execute(
                text("SELECT assignee FROM issues WHERE id='epic-1'")
            ).scalar_one()
            self.assertEqual(epic_assignee, "alice")

            tables = {
                row[0]
                for row in conn.execute(text(
                    "SELECT name FROM sqlite_master WHERE type='table'"
                )).fetchall()
            }
            self.assertIn("issues", tables)


if __name__ == "__main__":
    unittest.main()
