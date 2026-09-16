"""Tests for the created_after lower bound in issuestore_list (#679/T7).

A whole-history read (mail's ListAll on every `af done`) must not grow without
limit as an agent lives. The created_after filter bounds that read IN the store.
The bound and stored created_at are both RFC-3339 UTC strings, compared
lexically by SQLite — so these tests also pin that a lexical >= is a
chronological >= across the mixed precisions the store actually writes
(_now_iso emits microseconds, or none when the microsecond is 0).
"""

from __future__ import annotations

import os
import tempfile
import unittest

from sqlalchemy import create_engine, event, text

from .. import schema
from ..store import issuestore_list

# A microsecond-precision bound, exactly the shape gate_flags formats.
BOUND = "2026-01-01T00:00:10.000000Z"


def _make_engine(db_path: str):
    engine = create_engine(f"sqlite:///{db_path}")
    event.listen(engine, "connect", schema._set_pragmas)
    schema._create_all(engine)
    return engine


class CreatedAfterTest(unittest.TestCase):
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

    def _seed(self, rows):
        with self.engine.begin() as conn:
            for issue_id, created_at in rows:
                conn.execute(text(
                    "INSERT INTO issues (id, title, assignee, parent_id, status,"
                    " created_at, updated_at)"
                    " VALUES (:id, :id, 'alice', '', 'open', :ts, :ts)"
                ), {"id": issue_id, "ts": created_at})

    def _list_ids(self, args):
        return {r["id"] for r in issuestore_list(self.engine, args)}

    def test_created_after_keeps_at_or_after_drops_before(self):
        # Around the bound: strictly before is dropped, the exact instant and
        # anything after (including one microsecond later) is kept.
        self._seed([
            ("before", "2026-01-01T00:00:09.999999Z"),
            ("at-bound", "2026-01-01T00:00:10.000000Z"),
            ("after-micro", "2026-01-01T00:00:10.000001Z"),
        ])
        got = self._list_ids({"created_after": BOUND})
        self.assertEqual(got, {"at-bound", "after-micro"},
                         "created_after is an inclusive lower bound")

    def test_created_after_handles_mixed_precision(self):
        # _now_iso writes whole seconds with NO fraction (microsecond == 0) and
        # otherwise six fractional digits. The bound's six zero-fraction digits
        # must order correctly against both shapes across the second boundary.
        self._seed([
            ("whole-before", "2026-01-01T00:00:09Z"),
            ("whole-after", "2026-01-01T00:00:11Z"),
            ("micro-after", "2026-01-01T00:00:10.500000Z"),
        ])
        got = self._list_ids({"created_after": BOUND})
        self.assertEqual(got, {"whole-after", "micro-after"},
                         "a fractionless later second is kept; an earlier one is dropped")

    def test_no_bound_returns_everything(self):
        # An absent bound leaves the read unbounded — the field is additive.
        self._seed([
            ("old", "2020-01-01T00:00:00Z"),
            ("new", "2030-01-01T00:00:00Z"),
        ])
        self.assertEqual(self._list_ids({}), {"old", "new"})


if __name__ == "__main__":
    unittest.main()
