Run `af done` only when the step is complete.

   ```bash
   af mail send {{orchestrator}} -s "NOTE" -m "Run \`make test\` before
   pushing."
   ```

   ```markdown
   Run `af done` when finished, then `af handoff`.
   ```
