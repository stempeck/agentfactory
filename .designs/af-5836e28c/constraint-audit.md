# Pre-Synthesis Constraint Audit

## Constraints Legend
- C1: Templates in AF_SRC internal/templates/roles/
- C2: Workspace artifacts stay in factory root
- C3: quickstart.sh remains single entry point
- C4: --delete cleans both sides
- C5: -o works without source tree detection
- C6: Self-hosted case (af source == factory root)
- C7: External project case (af source != factory root)
- C8: Reuse AF_SOURCE_ROOT pattern
- C9: make build still required
- C10: No CLI breaking changes

## Audit Matrix

| Dimension | Recommendation | C1 | C2 | C3 | C4 | C5 | C6 | C7 | C8 | C9 | C10 | Status |
|-----------|---------------|----|----|----|----|----|----|----|----|----|----|--------|
| API | Shared config.ResolveSourceRoot() | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | PASS |
| Data | go.mod module path check | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | PASS |
| UX | Auto-routing with verbose output | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | PASS |
| Scale | Simple sequential resolution | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | PASS |
| Security | Validate go.mod for all resolution paths | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | PASS |
| Integration | Export AF_SOURCE_ROOT in agent-gen-all.sh | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | PASS |

## Detailed Verification

### C5 Check (most subtle): -o flag without source tree detection
The -o (dry run) flag path in formula.go returns after rendering content to stdout (line 130-133). The source root resolution happens at line ~200, which is AFTER the -o return point. Wait — looking more carefully:

The current code calls `ResolveSourceRoot()` BEFORE the -o check would be useful. The proposed change adds the source root resolution after finding factory root (line 109) but BEFORE the -o check (line 130).

**Issue detected**: If we add `srcRoot, err := config.ResolveSourceRoot(root)` before the -o check, then `-o` will fail when AF_SOURCE_ROOT isn't set and factory root isn't the source tree.

**Fix**: Move source root resolution to AFTER the -o check, right before the template write (line 199). The -o flag only needs the rendered content (which uses factory root for workspace path rendering), not the source root for template placement.

**Revised plan**: In formula.go:
- Lines 108-115: Find factory root, compute wsDir (unchanged)
- Lines 118-132: Render template, -o flag returns (unchanged)
- NEW: After -o check, resolve source root for template placement
- Line 200: Use srcRoot for template directory

This preserves C5.

## Audit Result
All dimensions PASS after the C5 fix for -o flag ordering. No constraint violations found.
