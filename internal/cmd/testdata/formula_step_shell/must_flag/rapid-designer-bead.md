<!-- expect: F S -->
2. **Create designer bead with embedded instructions:**
   ```bash
   DESIGNER_BEAD=$(af bead create --title "Designer: design-v7 for {{issue_title}}" \
     --type task \
     --description="## Instructions

   Run `/design-v7` on that problem summary file. Produce a complete design
   document at `.designs/{{issue_id}}/design-doc.md` with all dimension analyses.

   When design-v7 is complete, send a completion signal:
   ```bash
   af mail send $AF_ACTOR -s 'RAPIDSOL: DESIGN COMPLETE [{{issue_id}}]' -m 'Design complete. Results at .designs/{{issue_id}}/design-doc.md'
   ```

   Incorporate the analyst's findings, then commit and run af done when instructed.
   " --json | jq -r '.id')
   ```
