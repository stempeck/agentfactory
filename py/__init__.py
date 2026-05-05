# This package directory shares a name with the PyPI `py` library that
# pytest depends on (pytest's `_pytest/compat.py` does `LEGACY_PATH =
# py.path.local` at import time). PyPI ships a `py.py` shim at
# site-packages that re-exports `py.path` and `py.error` from pytest's
# vendored `_pytest._py`, but that shim is shadowed whenever this repo
# is on sys.path (per the shim's own comment: `if pylib is installed
# this file will get skipped — py/__init__.py has higher precedence`).
# Mirror the shim's re-exports here so pytest works when the test runner
# imports `py` from cwd=repo root, without breaking `py.issuestore.*`
# subpackage resolution. The imports are optional: in production installs
# that omit pytest, the shim is silently skipped (py.path is only touched
# by pytest itself).
try:
    import sys as _sys
    import _pytest._py.error as _error
    import _pytest._py.path as _path
    error = _error
    path = _path
    _sys.modules["py.error"] = _error
    _sys.modules["py.path"] = _path
except ImportError:
    pass
