2. **Check prerequisites and record the result:**
   ```bash
   af mail send {{orchestrator}} -s "PREREQ" -m "Checking prerequisites for
   {{issue_uri}} now."
   PREREQ_OUT=$(bash <<'SCRIPT'
   if ! command -v xcodebuild >/dev/null 2>&1; then
     echo "  BLOCKED: the keychain is locked or the key's ACL blocks headless use."
   fi
   SCRIPT
   )
   cat > "$NOTES" <<'EOF'
   Reply "LGTM, run `af done`" once the review is clean.
   EOF
   echo "$PREREQ_OUT"
   ```
