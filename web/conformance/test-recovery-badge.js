// Behavioural proof of the shipped occupancy/recovery badge (#596 K10-web, review finding H-4),
// runnable by any reviewer and by CI:
//
//   node web/conformance/test-recovery-badge.js
//   make conformance                       # same command, local<->CI parity
//
// It drives the SHIPPED recoveryLook / contextLook / appendHealthBadges (the same bytes served to
// the browser and embedded via //go:embed static) with real AgentView DTOs and asserts THE BADGES
// THEY EMIT — not the shape of their source.
//
// Why this exists (the PR #585 lesson, applied). The Go-side checks in
// web/internal/web/recoverybadge_test.go are source scans, and a source scan cannot see semantics.
// Mutations that leave the whole Go suite green — and `make conformance`'s other lanes green — while
// fully reinstating the masking defect this phase exists to remove:
//
//     if (!v || v !== 'none') return null;        // inverted gate: halted loses its badge,
//                                                 // every healthy agent gains "Recovery none"
//     if (state === 'fresh') return null;         // …and 'stale'/'dark' fall to the same return
//     return null;                                // unknown literal silently dropped ⇒ a
//                                                 // malformed channel reads as healthy
//     if (pct >= 0) …  →  if (pct > 0)            // the -1 sentinel and a real 0% collapse
//
// Every one of them changes what the operator SEES on the Floor, so every one of them fails here.
// The structural checks stay as the cheap first line of defence.
'use strict';
const fs = require('fs');
const path = require('path');

const APP = path.join(__dirname, '..', 'internal', 'web', 'static', 'app.js');
const src = fs.readFileSync(APP, 'utf8');

let pass = 0, fail = 0;
function ok(name, cond, extra) {
  if (cond) { pass++; } else { fail++; console.log('FAIL', name, extra === undefined ? '' : extra); }
}

// --- extract the shipped functions -----------------------------------------------------

function funcBody(name) {
  const at = src.indexOf('function ' + name + '(');
  if (at < 0) { throw new Error('app.js: no function ' + name); }
  const open = src.indexOf('{', at);
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') { depth++; }
    else if (src[i] === '}') { depth--; if (depth === 0) { return src.slice(at, i + 1); } }
  }
  throw new Error('app.js: unbalanced braces in ' + name);
}

// The three helpers' only DOM need is el(); stubbing exactly that keeps them unmodified.
// The stub records className and text so an assertion can read the rendered badge, which is
// the whole point — asserting the OUTPUT, not the source.
const harness = new Function(
  'return (function () {' +
  '  function el(tag, cls, text) {' +
  '    var e = { tag: tag, className: cls || "", text: "", children: [],' +
  '              appendChild: function (n) { this.children.push(n);' +
  '                if (n && n.nodeText !== undefined) { this.text += n.nodeText; } } };' +
  '    if (text != null) { e.text = String(text); }' +
  '    return e;' +
  '  }' +
  '  var document = { createTextNode: function (t) { return { nodeText: String(t) }; } };' +
  funcBody('recoveryLook') +
  funcBody('contextLook') +
  funcBody('badgeEl') +
  funcBody('appendHealthBadges') +
  '  return { recoveryLook: recoveryLook, contextLook: contextLook,' +
  '           appendHealthBadges: appendHealthBadges, el: el };' +
  '})();'
)();

// render drives the SHIPPED appendHealthBadges over one agent DTO and returns the badges it
// emitted as {cls, label} — exactly what an operator would see beside the status badge.
function render(agent) {
  const host = harness.el('div', 'badges');
  harness.appendHealthBadges(host, agent);
  return host.children.map(b => ({ cls: b.className, label: b.text }));
}

const isHalt = b => /\bhalt\b/.test(b.cls);
const isWarn = b => /\bwarn\b/.test(b.cls);
const isQuiet = b => /\bneutral\b/.test(b.cls);

// labelOf/clsOf tolerate a missing badge so that a mutation which emits NOTHING reports a clean
// assertion failure instead of a TypeError stack — a crash still fails CI, but it tells a
// reviewer far less about which guarantee broke.
const labelOf = (badges, i) => (badges[i] ? badges[i].label : '');
const clsOf = (badges, i) => (badges[i] ? badges[i].cls : '');

// --- the defect this phase exists to remove --------------------------------------------

// H-4: a breaker-halted agent whose session is live and whose step is ready derives
// status "working". The card must not be able to say only "Working".
{
  const badges = render({ status: 'working', recovery: 'halted', context_state: 'dark', context_pct: -1 });
  ok('halted agent renders a badge', badges.length > 0, badges);
  ok('halted badge is loud, not quiet', badges.some(isHalt), badges);
  ok('halted badge names the state', badges.some(b => /halted/i.test(b.label)), badges);
}

// The whole reason the badge exists: an exhausted agent that has NOT yet tripped the breaker is
// visible only through the occupancy datum.
{
  const badges = render({ status: 'working', recovery: 'none', context_state: 'fresh', context_pct: 92 });
  ok('exhausted-but-not-halted agent still renders its occupancy', badges.length === 1, badges);
  ok('…and shows the number', badges.some(b => /92%/.test(b.label)), badges);
}

// --- the healthy path must stay quiet ---------------------------------------------------

// 'none' is the NORMAL recovery verdict every healthy agent carries. If it rendered, the badge
// would appear on every card and mean nothing.
{
  const badges = render({ status: 'working', recovery: 'none', context_state: 'fresh', context_pct: 12 });
  ok('healthy agent grows no recovery badge', !badges.some(b => /recover/i.test(b.label)), badges);
  ok('healthy agent shows only its quiet occupancy', badges.length === 1 && isQuiet(badges[0]), badges);
}

// --- version skew: an old `af` omits the keys -------------------------------------------

// Go decodes the absent int as 0. A badge keyed on context_pct would read 0 as "0% used =
// healthy"; a badge keyed on the strings correctly says nothing at all.
{
  const badges = render({ status: 'working', recovery: '', context_state: '', context_pct: 0 });
  ok('pre-4A af renders no badge at all', badges.length === 0, badges);
}
{
  const badges = render({ status: 'working' });   // keys entirely absent from the DTO
  ok('missing fields render no badge', badges.length === 0, badges);
}
ok('render(undefined agent) does not throw', (() => {
  try { render(undefined); return true; } catch (e) { return false; }
})());

// --- the five-literal ChannelState domain ------------------------------------------------

// Only 'fresh' is healthy. The other four must each produce a loud badge — and 'malformed' is
// the load-bearing one: af-core never collapses it into 'none', so a UI that ignores it renders
// a malformed occupancy channel as healthy.
for (const state of ['stale', 'dark', 'none', 'malformed']) {
  const badges = render({ status: 'working', recovery: 'none', context_state: state, context_pct: -1 });
  ok('context_state ' + state + ' renders a badge', badges.length === 1, badges);
  ok('context_state ' + state + ' is loud, not quiet', badges.some(isWarn), badges);
}
ok('malformed is not relabelled as none',
  /malformed/i.test(labelOf(render({ context_state: 'malformed', context_pct: -1 }), 0)));

// An unknown literal must STILL render. A lookup map that falls through to "no badge" on a
// literal it does not recognise is how a new unhealthy state would come to read as healthy.
{
  const badges = render({ status: 'working', recovery: 'none', context_state: 'quarantined', context_pct: -1 });
  ok('unknown context_state still renders', badges.length === 1, badges);
  ok('unknown context_state names itself', /quarantined/.test(labelOf(badges, 0)), badges);
}
{
  const badges = render({ status: 'working', recovery: 'evacuating', context_state: 'fresh', context_pct: -1 });
  ok('unknown recovery verdict still renders', badges.length === 1, badges);
  ok('unknown recovery verdict names itself', /evacuating/.test(labelOf(badges, 0)), badges);
}

// --- the -1 sentinel is never printed as a percentage -------------------------------------

// -1 means "no datum". Printing "-1%" would be worse than printing nothing.
for (const state of ['fresh', 'dark', 'malformed']) {
  const badges = render({ recovery: 'none', context_state: state, context_pct: -1 });
  ok('-1 never prints as a percentage (' + state + ')',
    !badges.some(b => /-1|%/.test(b.label)), badges);
}
// …but a real 0% does print: 0 is a legitimate reading when a state literal vouches for it.
ok('a real 0% reading is shown',
  render({ recovery: 'none', context_state: 'fresh', context_pct: 0 }).some(b => /0%/.test(b.label)));

// --- a stopped agent: 'none' is tautological, a latched breaker is not ---------------------

// af reports state 'none' for every agent outside its running-only occupancy sweep, so "no
// context datum" on a stopped card states the obvious and would grow on nearly every dark card.
{
  const badges = render({ status: 'stopped', running: false, recovery: 'none', context_state: 'none', context_pct: -1 });
  ok('stopped agent with no datum renders nothing', badges.length === 0, badges);
}
// …but a latched breaker OUTLIVES the session it halted, and that is exactly what the operator
// must see: this agent will not be recycled until `af recovery reset`.
{
  const badges = render({ status: 'stopped', running: false, recovery: 'halted', context_state: 'none', context_pct: -1 });
  ok('stopped agent still shows a latched breaker', badges.length === 1, badges);
  ok('…and it is the recovery badge', /halted/i.test(labelOf(badges, 0)), badges);
}
// A state that implies a datum ONCE existed still renders on a stopped card.
{
  const badges = render({ status: 'stopped', running: false, recovery: 'none', context_state: 'malformed', context_pct: -1 });
  ok('stopped agent still shows a malformed channel', badges.length === 1, badges);
}
// The suppression is scoped to 'none' + not-running; a RUNNING agent reporting 'none' is a real
// finding (its statusline gate may be off, or it has never rendered).
{
  const badges = render({ status: 'working', running: true, recovery: 'none', context_state: 'none', context_pct: -1 });
  ok('running agent with no datum IS a finding', badges.length === 1, badges);
}

// --- ordering: the more urgent badge comes first ------------------------------------------
{
  const badges = render({ status: 'working', recovery: 'halted', context_state: 'stale', context_pct: 88 });
  ok('both badges render together', badges.length === 2, badges);
  ok('recovery precedes context', /halt/.test(clsOf(badges, 0)) && /warn/.test(clsOf(badges, 1)), badges);
  ok('context badge carries the percentage', /88%/.test(labelOf(badges, 1)), badges);
}

// 'recovering' is distinct from 'halted': in flight, not latched.
{
  const badges = render({ recovery: 'recovering', context_state: 'fresh', context_pct: -1 });
  ok('recovering renders its own badge', badges.length === 1 && /recovering/i.test(labelOf(badges, 0)), badges);
}

// --- the badges never colour themselves from the card's --lit -----------------------------
// --lit is set PER CARD by .sign.s-*, so a --lit badge glows magenta on a Working card and
// renders colourless in the #agent-badge detail host, which sits outside any .sign.
{
  const all = []
    .concat(render({ recovery: 'halted', context_state: 'dark', context_pct: -1 }))
    .concat(render({ recovery: 'recovering', context_state: 'stale', context_pct: 50 }));
  ok('every emitted badge carries an explicit hue class',
    all.every(b => isHalt(b) || isWarn(b) || isQuiet(b)), all);
}

console.log((fail === 0 ? 'PASS' : 'FAIL') +
  ': recovery/occupancy badge behaviour — ' + pass + ' passed, ' + fail + ' failed');
process.exit(fail === 0 ? 0 : 1);
