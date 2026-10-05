<!-- expect: S -->
1. **Create analyst bead with embedded instructions:**
   ```bash
   ANALYST_BEAD=$(af bead create --title "Analyst: rootcause-all for {{issue_title}}" \
     --type task \
     --description="## Instructions

   ### Phase 2: Stay Alive for Cross-Review
   After completing rootcause-all, remain in your session. Do NOT call `af done`.
   " --json | jq -r '.id')
   ```
