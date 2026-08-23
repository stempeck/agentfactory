<!-- Phase 5 publish checklist — cycle af-c967c569. Tier B: the operator publishes; the agent
     handles mechanics. Sequencing follows the runbook Channels section. Record each URL (or SKIP)
     on its form line — that's what the gate reads. Close the notifying issue when all are recorded. -->

# Publish checklist — cycle af-c967c569 (web console screen tour)

Status at hand-off: **Medium is already LIVE** (you published it), **homepage is already
pointed at it**. The one open item is the **LinkedIn** post.

## 1. Medium (long-form) — ✅ PUBLISHED
- Vehicle used: `cycle-af-c967c569-medium.html` (rich text, 8 screenshots embedded).
- Verified live: 8 images survived, no raw markdown, subtitle + 5 topics in place.
- **URL:** https://medium.com/@glennstempeck/in-my-factory-of-ai-agents-last-time-i-cracked-the-door-heres-every-room-a04f14babef0
- Decision: PUBLISHED

## 2. Repo homepage → live article — ✅ DONE (agent, Tier A)
- `gh repo edit stempeck/agentfactory --homepage <medium-url>` run and confirmed.
- URL host validated against the runbook `homepage-allowlist` (https://medium.com) before writing.
- Decision: DONE

## 3. LinkedIn (short-form) — ⏳ AWAITING YOU
Plain text, no markdown (LinkedIn renders none — any `*asterisks*` ship literally). The footer
Medium link is already filled with the live URL.

- **Paste source:** `cycle-af-c967c569-linkedin.md` (copy the text between the `---` rules).
- Optional: attach the Floor screenshot (`cycle-af-c967c569-screen-floor.png`).
- lnkd.in shortening on the links is fine (your habit).
- After you post, paste the URL below (or write SKIP).

- Post URL: SKIP
  (Operator on issue #108, 2026-08-23: "I'll post the next one to LinkedIn. Not this one." —
  short-form deferred to next cycle. The draft `cycle-af-c967c569-linkedin.md` stays ready with the
  live Medium URL already in its footer, so it can be reused or refreshed next cycle.)

## Notes
- The Medium paste vehicle (`cycle-af-c967c569-medium.html`) is regenerable scaffolding and gets
  DELETED in phase 6 cleanup — it has served its purpose now that the article is live.
- Release decision: **`release: YES`** (operator, #108). **`v0.3.0` cut** against `main`:
  https://github.com/stempeck/agentfactory/releases/tag/v0.3.0 — the CHANGELOG heading is now
  backed by a real GitHub Release + tag.
