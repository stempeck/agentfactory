3. **Report and run the new tests:**
   ```bash
   af mail send <orchestrator> -s "REVIEW" -m "Findings for <pr-number>:
   see <path to review.md>"
   <TEST_PATTERN_CMD for the PR's new tests>
   cat <the <role>'s notes file>
   ```
