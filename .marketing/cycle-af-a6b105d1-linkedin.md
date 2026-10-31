<!-- DRAFT — cycle af-a6b105d1 short-form (security / supply-chain, #124). Tier B: YOU post this
     yourself - I never post. Your edits are canonical; I fix mechanics only (spelling/grammar)
     after and enumerate them. Review on GitHub (the PR), not in this worktree file.
     PLAIN TEXT ONLY - LinkedIn renders no markdown; any *asterisks* or `backticks` ship as
     literal characters. Copy everything between the lines. Sequencing (runbook Step 5): publish
     the Medium piece FIRST, then paste its URL into the footer here before posting. -->

-----8<----- copy below -----8<-----

Your autonomous agents run unattended, for hours, holding your credentials, inside a container you built once and now mostly ignore.

Quick question: can you say what that container actually installed? Not roughly - exactly. Did anything float to "latest" while you weren't looking?

I couldn't answer that for mine, so I fixed it. agentfactory's bootstrap is now a convergence: every run brings each component to a pinned, integrity-verified version, and a required one that can't converge fails the build instead of running drifted. It writes a convergence.json so the container proves what it ran, quickstart.sh --check reports drift read-only (changes nothing), and a daily audit checks every pin against the advisory databases.

A factory of agents is becoming the most secure way to run multi-agent orchestration.

If you run agents unattended - can you prove what your last container installed? Happy to help.

Read more: <MEDIUM ARTICLE URL — paste after publishing the Medium piece> | Get it: github.com/stempeck/agentfactory

-----8<----- copy above -----8<-----

## Operator Decision
- Decision: ______
  (READY to approve for publishing; EDITED — you changed the text, I re-check mechanics only;
   SKIP to drop this piece this cycle.)
- Notes: ______
