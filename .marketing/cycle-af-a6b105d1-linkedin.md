<!-- DRAFT — cycle af-a6b105d1 short-form (security / supply-chain, #124). Tier B: YOU post this
     yourself - I never post. Operator EDITED 2026-11-01 on PR #129; your review comments landed on
     the Medium file, so the same direction is applied here and every change is enumerated below.
     PLAIN TEXT ONLY - LinkedIn renders no markdown; any *asterisks* or `backticks` ship as
     literal characters. Copy everything between the lines. Sequencing (runbook Step 5): publish
     the Medium piece FIRST, then paste its URL into the footer here before posting. -->

-----8<----- copy below -----8<-----

Your autonomous agents run unattended, for hours, holding your credentials, inside a container you built once and now mostly ignore.

Quick question: can you say what that container actually installed? Not roughly - exactly. Did the packages that were used with your initial container build get updated or are they now old and outdated, and subject to CVEs that make your agents vulnerable?

I couldn't answer that for mine, so I fixed it. agentfactory's bootstrap is now a convergence: every run (af install --agents runs it too) brings each component to a pinned, integrity-verified version and installs every pending Ubuntu security update - and a required component that can't converge fails the build instead of running drifted. A daily audit checks every pin against the advisory databases.

A factory of agents is becoming the most secure way to run multi-agent orchestration.

If you run agents unattended - can you prove what your last container installed? Happy to help.

Read more: <MEDIUM ARTICLE URL — paste after publishing the Medium piece> | Get it: github.com/stempeck/agentfactory

-----8<----- copy above -----8<-----

<!-- Every change the agent made to reconcile your PR #129 review (voice law - nothing else touched).
     Your EDITED on PR #129 was one reply for the whole PR and all five inline comments were on the
     Medium file, so the same direction is applied here:
     1. Cut "quickstart.sh --check reports drift read-only (changes nothing)," (your Medium line-33
        comment - operators shouldn't need diagnostic commands).
     2. Added your hook question (your Medium line-15 comment), adapted to the reader of this post:
        "the initial container build" -> "your initial container build"; "the agentfactory
        vulnerable" -> "your agents vulnerable". Same mechanics fixes as Medium ("packages",
        "CVEs").
     3. Added "(af install --agents runs it too)" (your Medium line-23 edit; verified
        internal/cmd/install.go:974-979) and "installs every pending Ubuntu security update"
        (verified quickstart.sh:636-660). With that clause in place, "and a required one" ->
        " - and a required component" so the subject stays clear.
     4. Length (your standing LinkedIn note: "keep it short and concise or no one will read it"):
        your question took the post from 143 to 179 words, so I trimmed MY words, not yours - cut
        "Did anything float to "latest" while you weren't looking?" (your question covers it) and
        "It writes a convergence.json so the container proves what it ran," (an operator-facing
        internal, same as the --check cut). Now ~160 words.
     If you'd rather this one go out as originally drafted, or SKIP it, say so before you post. -->

<!-- REFERENCE ONLY - the agent's prior text for the two changed paragraphs. Do not paste.
     Quick question: can you say what that container actually installed? Not roughly - exactly.
     Did anything float to "latest" while you weren't looking?

     I couldn't answer that for mine, so I fixed it. agentfactory's bootstrap is now a convergence:
     every run brings each component to a pinned, integrity-verified version, and a required one
     that can't converge fails the build instead of running drifted. It writes a convergence.json
     so the container proves what it ran, quickstart.sh --check reports drift read-only (changes
     nothing), and a daily audit checks every pin against the advisory databases. -->

## Operator Decision
- Decision: EDITED
  (READY to approve for publishing; EDITED — you changed the text, I re-check mechanics only;
   SKIP to drop this piece this cycle.)
- Source: PR #129 comment by `stempeck` (OWNER), 2026-11-01T17:41:46Z
  (https://github.com/stempeck/agentfactory/pull/129#issuecomment-6561531416). One PR-level
  EDITED, not split per draft; no inline comment on this file.
- Resolution: your Medium review applied here too (four changes enumerated above, prior text
  kept as reference). Flagged on PR #129 so you can SKIP or revert this piece before posting.
