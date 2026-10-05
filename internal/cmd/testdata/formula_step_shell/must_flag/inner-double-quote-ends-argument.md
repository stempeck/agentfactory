<!-- expect: G -->
1. **Create analyst bead with embedded instructions:**
   ```bash
   ANALYST_BEAD=$(af bead create --title "Analyst" \
     --type task \
     --description="## Instructions

   When rootcause-all is complete, send this completion signal:

       af mail send $AF_ACTOR -s 'RAPIDSOL: ANALYSIS COMPLETE [$ISSUE_ID]' -m "Analysis complete. Results at <absolute path to your rootcause_analysis.md>"

   Report the real path you wrote to.
   " --json | jq -r '.id')
   ```
