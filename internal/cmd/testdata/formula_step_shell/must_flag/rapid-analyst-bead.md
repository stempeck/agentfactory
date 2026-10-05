<!-- expect: F S P -->
1. **Create analyst bead with embedded instructions:**
   ```bash
   cd "${AF_WORKTREE:-$AF_ROOT}"
   ANALYST_BEAD=$(af bead create --title "Analyst: rootcause-all for {{issue_title}}" \
     --type task \
     --description="## Instructions

   ### Phase 1: Run rootcause-all
   Run `/rootcause-all` on that problem summary file.

   When rootcause-all is complete, send a completion signal:
   ```bash
   af mail send $AF_ACTOR -s 'RAPIDSOL: ANALYSIS COMPLETE [{{issue_id}}]' -m "Analysis complete. Results at <absolute path to your rootcause_analysis.md>"
   ```
   Report the real path you wrote to.
   ```

   ### Phase 2: One Cross-Review Round
   Complete your own formula normally, including `af done`.
   " --json | jq -r '.id')
   ```
