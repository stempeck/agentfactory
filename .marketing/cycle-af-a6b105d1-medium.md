<!-- DRAFT — cycle af-a6b105d1 flagship (security / supply-chain reproducibility, #124).
     Operator EDITED 2026-11-01 on PR #129 (EDITED + five inline review comments). Glenn's prose
     from those comments is canonical and sits in the body verbatim (mechanics-only fixes); his
     cuts are applied. Every change is enumerated at the bottom. Tier B: YOU publish by pasting
     into Medium - I never post.
     - The italic line under the title goes in Medium's SUBTITLE field, not the body.
     - One figure is marked [FIGURE: ...]; the image is staged in .marketing/ and embedded in the
       rich-text paste vehicle (cycle-af-a6b105d1-paste.html). Fill the repo link = stable;
       there is no live URL to fill for this piece. -->

# My agents run unattended with my credentials. So I made the install prove itself.

*Reproducible, integrity-verified installs for a multi-agent Claude Code factory - converge to the pins or fail the build, and prove exactly what ran.*

Here's a thing you stop thinking about once your agents work: they run unattended, for hours, holding your credentials, inside a container you built once and now mostly ignore. I run a lot of them. And one day I asked myself a question I couldn't actually answer - did this container install EXACTLY what I pinned? Or did a base image float, a package pick up a silent point bump, a tool quietly pull "latest" while I wasn't looking? Did the packages that were used with the initial container build get updated or are they now old and outdated, and subject to CVEs that make the agentfactory vulnerable?

For autonomous agents that is not a tidiness problem. It is a trust problem. Version drift across containers is one of the quieter ways a run goes weird and you burn an afternoon chasing it - and an unpinned, unaudited dependency walking into the box your agent runs your credentials in is the louder way. "It worked on the last container" is a bad answer when you can't say what the last container even had.

Knowing what agents are actually going to do (having a blueprint, like the formula/toml and agent personas) and also knowing they're operating in the most secure environment possible WHILE giving them the freedom necessary to do their job effectively is paramount. That starts with using the least number of and safest packages possible, pinning them, updating them any time a CVE that could impact us arises, and letting agentfactory enforce the blueprints while the container itself enforces good security practices.

So I made the bootstrap prove itself.

## Bootstrap is now a convergence

Every time `quickstart.sh` runs (which also happens on `af install --agents`), it does not just "install stuff if missing" anymore. It CONVERGES: for each component it checks what's installed, compares it to the pin, and brings it to exactly that pinned version - then verifies it before anything uses it. The mechanism depends on where the thing comes from = `sha256`/`sha512` digests for the downloaded toolchains (Claude Code, Codex, gh, Go, OpenObserve, Playwright), signed apt packages for the system layer (Ubuntu security, Node.js), and pip `--require-hashes` for Python.

That includes the packages from the initial container build - every run refreshes apt and installs every pending Ubuntu security update for them, so they don't sit there old and outdated.

And here's the part that makes it a guarantee instead of a hope: a REQUIRED component that cannot converge does not get waved through. The build exits non-zero. You do not get an agent running on a drifted toolchain - you get a failure you can see.

Then it writes down what it did. `convergence.json` is one row per component - the pin, what actually landed, how it was verified, and whether it was kept or changed. It is the container proving what it is running, without you re-probing it by hand:

[FIGURE: cycle-af-a6b105d1-screen-convergence.png — CAPTURED. The real .runtime/bootstrap/convergence.json from a 2026-10-30 bootstrap, rendered as a terminal view: ten components (ubuntu-security, nodejs, go, gh, claude, playwright, python-lock, litellm-lock, codex, openobserve), each with pin = installed, its integrity method, and result "kept". Values are verbatim from the real file. Embedded in the paste vehicle cycle-af-a6b105d1-paste.html.]

And drift is not the only thing that rots. A pin you trusted on Monday can have a CVE disclosed against it on Thursday. So there's a daily audit in CI that runs every pinned package against the advisory databases - a newly disclosed problem in a pin surfaces on its own, instead of waiting for me to cut the next release and notice.

## Why I'm spending the project's time on this

Because I decided the project has to. This is written down now as an architecture decision the repo is held to: agentfactory is committed to being the most secure way to run an agent, and every change keeps or STRENGTHENS every security control - a capability that cannot be built without weakening one does not get built. That is the real shift. This started as a factory of agents. What it is becoming is the most secure way to run multi-agent orchestration, and the pins-and-convergence work is just the first thing you can see from the outside.

Plugins are part of that too - third-party agents and tools install behind a trust boundary, from a pinned commit, as a read-only snapshot, with their provenance recorded. And there's a security-program review formula, `fable-secure`, that can call an external scanner over your repo when you bind one as a plugin. More of those to come.

The code is on GitHub: github.com/stempeck/agentfactory. If you run agents unattended - can you say, right now, what your last container actually installed, and prove it? Or are you trusting "latest" like I was? Happy to help if you want to wire this into your own setup.

Learn it, Live it, Share it!

<!-- Alt titles if this one isn't your taste:
     - "Can you prove what your agent's container actually installed?"
     - "The build fails before the agent runs on the wrong version" -->

<!-- Every change the agent made to reconcile your PR #129 review (voice law - nothing else touched):

     YOUR TEXT, inserted verbatim (mechanics-only fixes listed):
     1. Hook, last question (your comment on line 15): "Did the packages that were used with the
        initial container build get updated or are they now old and outdated, and subject to CVEs
        that make the agentfactory vulnerable?"
        Mechanics: "package that were" -> "packages that were" (subject-verb agreement);
        "CVE's" -> "CVEs" (plural, not possessive). Not inserted: "That's a core problem we're
        trying to solve here, and we need to emphasize the security implications of building with
        agent more." - read as direction to me, not article copy.
     2. New paragraph after "It is a trust problem..." (your comment on line 17): your text
        verbatim, minus the opening "Agree it's a trust problem." (a reply to my draft, not copy).
        No mechanics changes.
     3. Convergence opener (your comment on line 23): "Every time `quickstart.sh` runs" ->
        "Every time `quickstart.sh` runs (which also happens on `af install --agents`)".
        Mechanics: "tie" -> "time"; dropped the stray space inside the backticks.
        Verified: internal/cmd/install.go:974-979 runs quickstart.sh after agent-gen-all.sh.

     YOUR CUTS:
     4. Removed the "## Audit an install without touching it" heading, "The switch I reach for most
        is this one:", the `./quickstart.sh --check` block and its paragraph (your comment on line
        33 - operators shouldn't need commands; --check is manager-level). The daily CVE audit
        paragraph that sat under that heading stays, now directly after the figure.
     5. Removed the "One limit worth saying plainly..." paragraph (your comment on line 49).
     6. Same rule, applied once more (your line-49 comment: "focus on what people CAN DO not the
        counter-arguements or gotchas"): removed "Honest about where that one stands: agentfactory
        ships the integration POINT, not the scanner - bind your own today, and when none is bound
        the gate records "tool absent" and the review carries on instead of lying about a scan it
        didn't run." The plugins paragraph stays accurate without it - the sentence before already
        says fable-secure calls a scanner "when you bind one as a plugin".

     NEW AGENT TEXT (strike freely):
     7. One sentence after the convergence opener, answering your hook question with the shipped
        mechanism: "That includes the packages from the initial container build - every run
        refreshes apt and installs every pending Ubuntu security update for them, so they don't
        sit there old and outdated." Verified: quickstart.sh:636-660 (apt-get update, then
        --only-upgrade of every installed package with a newer -security build).

     Unchanged: title, subtitle, figure, and every other sentence. -->

<!-- REFERENCE ONLY - the agent's prior text that your review replaced or cut. Do not paste.

     Convergence opener (before #3):
     Every time `quickstart.sh` runs, it does not just "install stuff if missing" anymore.

     Section removed (#4):
     ## Audit an install without touching it
     The switch I reach for most is this one:
         ./quickstart.sh --check
     That is a read-only drift report. It tells you where an existing install has drifted from the
     pins and changes NOTHING - no installs, no package lists refreshed, no surprises. You can run
     it on a box that's been up for weeks and just ask "are you still what I think you are?"

     Paragraph removed (#5):
     One limit worth saying plainly, because convergence is easy to oversell: pinning and verifying
     a known set does not bless a dependency you add OUTSIDE the pins - put an unpinned tool in the
     box and that's on you, not on the convergence. What this gives you is that the set the project
     DOES manage is exactly what you pinned, verified, and auditable - every container, every run.

     Sentence removed (#6): quoted in full under #6 above. -->

## Operator Decision
- Decision: EDITED
  (READY to approve for publishing; EDITED — you changed the text, I re-check mechanics only;
   SKIP to drop this piece this cycle.)
- Source: PR #129 comment by `stempeck` (OWNER), 2026-11-01T17:41:46Z
  (https://github.com/stempeck/agentfactory/pull/129#issuecomment-6561531416), plus five inline
  review comments on this file, 17:42-17:48Z (review comment ids 4436682732, 4436688999,
  4436691165, 4436699349, 4436704697). Author verified against the runbook operator.
- Resolution: your prose inserted verbatim with mechanics-only fixes, your cuts applied, one new
  agent sentence marked for you to strike - all seven changes enumerated above, replaced text kept
  as reference. Paste vehicle re-rendered to match. Your review is recorded as the new voice
  calibration source in the runbook (Voice calibration 2026-11-01).
