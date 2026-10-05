- **Q6 — Reflection round:**
  ```bash
  for NAME in {{claude_analyst}} {{gpt_analyst}}; do
    af mail send "$NAME" -s "MULTIAGENT: SYNTHESIS Q$N (final round)" -m "Synthesis of Q$N:

  $(cat {{consult_dir}}/q$N/synthesis.md)

  ---
  Reply EXACTLY once:
  af mail send $AF_ACTOR -s 'MULTIAGENT: REFLECTION Q$N $NAME' -m '<CONCUR|DISSENT>: <reasons>'"
  done
  ```
