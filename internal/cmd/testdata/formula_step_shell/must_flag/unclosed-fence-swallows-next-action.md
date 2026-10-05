<!-- expect: F -->
1. **Stop designer, start the worker:**
   ```bash
   af down {{worker_name}} && af sling --agent {{worker_name}} --persistent --reset "Run /rootcause-review."
   ```

2. **Verify CORRECT agent session started (mechanical enforcement):**
  ```bash
  sleep 5
  if ! tmux has-session -t "af-{{worker_name}}" 2>/dev/null; then
    exit 1
  fi

3. **Wait for peer review complete signal:**
   ```bash
   RETRIES=0
   ```
