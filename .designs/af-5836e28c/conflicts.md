# Cross-Dimension Conflict Matrix

|              | API | Data | UX | Scale | Security | Integration |
|--------------|-----|------|----|-------|----------|-------------|
| **API**      | -   | ○    | ○  | ○     | ○        | ○           |
| **Data**     | ○   | -    | ○  | ○     | ○        | ○           |
| **UX**       | ○   | ○    | -  | ○     | ⚠        | ○           |
| **Scale**    | ○   | ○    | ○  | -     | ○        | ○           |
| **Security** | ○   | ○    | ⚠  | ○     | -        | ○           |
| **Integration** | ○ | ○   | ○  | ○     | ○        | -           |

Legend: ○ No conflict, ⚠ Tension (trade-off needed), ✗ Direct conflict (resolution required)

## Summary
This is a straightforward design with minimal cross-dimension conflicts. The only tension is between Security (validate all paths) and UX (keep errors minimal/friendly). No direct conflicts exist.

## Tension: Security vs UX

- **Nature**: Security recommends validating go.mod for ALL source root resolution paths (including AF_SOURCE_ROOT). UX recommends friendly error messages and minimal friction. If validation is too strict, users with legitimate but non-standard setups (e.g., forked repos with different module names) get blocked with no workaround.
- **Impact**: Strict validation could reject valid AF_SOURCE_ROOT values that point to a valid source tree but with a renamed go.mod module.
- **Resolution Options**:
  1. **Security wins**: Always require go.mod with "agentfactory" in module path. No exceptions. Users with renamed forks must change their module name or contribute upstream.
  2. **UX wins**: Accept any AF_SOURCE_ROOT without validation. Users are trusted to set it correctly.
  3. **Hybrid**: Validate go.mod when auto-detecting (factory root check), but trust explicit AF_SOURCE_ROOT if `internal/templates/roles/` directory exists there. The explicit env var implies user intent.
- **Chosen Resolution**: Option 1 (Security wins) because:
  - The forked-repo scenario is extremely rare for this tool
  - Writing templates to the wrong directory is the exact problem we're solving
  - The validation cost is one file read
  - If someone really needs to override, they can add "agentfactory" to their fork's module path
