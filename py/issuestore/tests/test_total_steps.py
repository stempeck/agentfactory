"""Tests for total_steps computation in issuestore_ready.

Verifies that total_steps reflects the total number of children under a
formula instance (molecule_id), not just the count of currently ready steps.
"""

from __future__ import annotations

import os
import tempfile
import unittest

from sqlalchemy import create_engine, event, text

from .. import schema
from ..store import issuestore_ready

TS = "2026-01-01T00:00:00Z"


def _make_engine(db_path: str):
    engine = create_engine(f"sqlite:///{db_path}")
    event.listen(engine, "connect", schema._set_pragmas)
    schema._create_all(engine)
    return engine


class TotalStepsTest(unittest.TestCase):
    def setUp(self):
        fd, self.db_path = tempfile.mkstemp(suffix=".sqlite")
        os.close(fd)
        self.engine = _make_engine(self.db_path)

    def tearDown(self):
        self.engine.dispose()
        for suffix in ("", "-wal", "-shm"):
            p = self.db_path + suffix
            if os.path.exists(p):
                os.remove(p)

    def test_linear_chain_total_steps_equals_child_count(self):
        with self.engine.begin() as conn:
            conn.execute(text(
                "INSERT INTO issues (id, title, assignee, parent_id, created_at, updated_at)"
                " VALUES ('epic-1', 'Epic', 'alice', '', :ts, :ts)"
            ), {"ts": TS})
            for i in range(1, 4):
                conn.execute(text(
                    "INSERT INTO issues (id, title, assignee, parent_id, status, created_at, updated_at)"
                    " VALUES (:id, :title, 'alice', 'epic-1', 'open', :ts, :ts)"
                ), {"id": f"child-{i}", "title": f"Child {i}", "ts": TS})
            conn.execute(text(
                "INSERT INTO deps (issue_id, depends_on_id) VALUES ('child-2', 'child-1')"
            ))
            conn.execute(text(
                "INSERT INTO deps (issue_id, depends_on_id) VALUES ('child-3', 'child-2')"
            ))

        result = issuestore_ready(self.engine, {"molecule_id": "epic-1"})

        self.assertEqual(result["total_steps"], 3,
                         "total_steps should be total children (3), not ready count")
        self.assertEqual(len(result["steps"]), 1,
                         "only child-1 should be ready (child-2 and child-3 are blocked)")
        self.assertEqual(result["steps"][0]["id"], "child-1")

    def test_no_molecule_id_total_steps_equals_ready_count(self):
        with self.engine.begin() as conn:
            conn.execute(text(
                "INSERT INTO issues (id, title, assignee, parent_id, created_at, updated_at)"
                " VALUES ('epic-1', 'Epic', 'alice', '', :ts, :ts)"
            ), {"ts": TS})
            for i in range(1, 4):
                conn.execute(text(
                    "INSERT INTO issues (id, title, assignee, parent_id, status, created_at, updated_at)"
                    " VALUES (:id, :title, 'alice', 'epic-1', 'open', :ts, :ts)"
                ), {"id": f"child-{i}", "title": f"Child {i}", "ts": TS})
            conn.execute(text(
                "INSERT INTO deps (issue_id, depends_on_id) VALUES ('child-2', 'child-1')"
            ))
            conn.execute(text(
                "INSERT INTO deps (issue_id, depends_on_id) VALUES ('child-3', 'child-2')"
            ))

        result = issuestore_ready(self.engine, {})

        self.assertEqual(result["total_steps"], len(result["steps"]),
                         "without molecule_id, total_steps should equal len(steps)")


if __name__ == "__main__":
    unittest.main()
