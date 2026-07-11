// Headless proof of the shipped editor engine's round-trip contract, runnable by
// any reviewer and by CI:
//
//   node web/conformance/test-engine.js .agentfactory/store/formulas
//   make conformance                       # same command, local<->CI parity
//
// It parses every real formula in .agentfactory/store/formulas/, cross-checks the
// parse against Python's tomllib (the conformance ground truth), and verifies that
// each visual-edit operation produces a diff limited to the intended change — the
// "minimal diff" bar set by the iteration-1 feedback.
//
// It targets the SHIPPED engine at web/internal/web/static/formula-editor/scripts/toml-engine.js
// (the same bytes served to the browser and embedded via //go:embed static), so the
// validator the user edits against is the one CI conformance-tests on every push.
'use strict';
const fs = require('fs');
const path = require('path');
const { execFileSync } = require('child_process');

const Engine = require(path.join(__dirname, '..', 'internal', 'web', 'static', 'formula-editor', 'scripts', 'toml-engine.js'));
const ROOT = path.resolve(__dirname, '..', '..');   // web/conformance -> web -> repo root
const DIR = process.argv[2] || path.join(ROOT, '.agentfactory', 'store', 'formulas');

let pass = 0, fail = 0;
function ok(name, cond, extra) {
  if (cond) { pass++; }
  else { fail++; console.log('FAIL', name, extra || ''); }
}

function deepEq(a, b, where) {
  if (a === b) return true;
  if (typeof a !== typeof b) { console.log('  type mismatch at', where, typeof a, typeof b); return false; }
  if (typeof a === 'number') { if (Object.is(a, b)) return true; console.log('  num mismatch at', where, a, b); return false; }
  if (a === null || b === null || typeof a !== 'object') { console.log('  mismatch at', where, JSON.stringify(a), JSON.stringify(b)); return false; }
  if (Array.isArray(a) !== Array.isArray(b)) { console.log('  array-ness mismatch at', where); return false; }
  const ka = Object.keys(a), kb = Object.keys(b);
  if (ka.length !== kb.length) { console.log('  key count mismatch at', where, 'a:', ka.join(','), 'b:', kb.join(',')); return false; }
  for (const k of ka) {
    if (!(k in b)) { console.log('  missing key at', where + '.' + k); return false; }
    if (!deepEq(a[k], b[k], where + '.' + k)) return false;
  }
  return true;
}

// The rarity ladder, replicated here on booleans/counts (NOT node objects) so shape
// expectations are DERIVED from tomllib ground truth rather than hardcoded. Mirrors
// the engine's internal rarityOf(hasGate, mergeCount, wiredCount) (delta-b).
function expectedRarity(hasGate, isMerge, isWired) {
  return hasGate ? 'legendary' : (isMerge ? 'epic' : (isWired ? 'rare' : 'common'));
}

// Longest-path rank over a unit list's `needs` DAG (matches Engine.layout's fixpoint).
// Derives the webdesign maxRank pin from ground truth instead of the literal 21.
function longestPathRank(units) {
  const deps = Object.create(null);
  units.forEach(u => { if (u.id) deps[u.id] = u.needs || []; });
  const memo = Object.create(null);
  function rank(id) {
    if (id in memo) return memo[id];
    memo[id] = 0; // in-progress marker also guards against cycles
    let r = 0;
    (deps[id] || []).forEach(up => { if (deps[up] !== undefined) r = Math.max(r, rank(up) + 1); });
    memo[id] = r;
    return r;
  }
  let max = 0;
  units.forEach(u => { if (u.id) max = Math.max(max, rank(u.id)); });
  return max;
}

// ---------- 1. conformance vs Python tomllib over every store formula ----------
const files = fs.readdirSync(DIR).filter(f => f.endsWith('.toml')).sort();
const truth = JSON.parse(execFileSync('python3', ['-c',
  "import tomllib,json,sys,os; d=sys.argv[1]; print(json.dumps({f: tomllib.load(open(os.path.join(d,f),'rb')) for f in sorted(os.listdir(d)) if f.endswith('.toml')}))",
  DIR], { maxBuffer: 1 << 26 }).toString());

for (const f of files) {
  const text = fs.readFileSync(path.join(DIR, f), 'utf8');
  let doc;
  try { doc = Engine.parse(text); }
  catch (e) { ok('parse:' + f, false, e.message); continue; }
  ok('parse:' + f, true);
  ok('conform:' + f, deepEq(doc.js, truth[f], f));
  const findings = Engine.validate(doc.js);
  ok('valid:' + f, findings.length === 0, JSON.stringify(findings.slice(0, 3)));
}

// ---------- 2. minimal-diff patch ops on ultra-review (the 16-step stress case) ----------
const UR = fs.readFileSync(path.join(DIR, 'ultra-review.formula.toml'), 'utf8');
let doc = Engine.parse(UR);

// 2a. retitle one step -> exactly 1 changed line
let t1 = Engine.ops.setStepField(UR, doc, 'branch-setup', 'title', Engine.fmt.string('Set up the working branch'));
let d1 = Engine.lineDiff(UR, t1);
ok('retitle-1-line', d1.ops.filter(o => o.type === 'del').length === 1 && d1.ops.filter(o => o.type === 'add').length === 1, 'changed=' + d1.changedLines);
ok('retitle-parses', Engine.parse(t1).js.steps.find(s => s.id === 'branch-setup').title === 'Set up the working branch');

// 2b. rename a step id -> id line + each referencing needs line only
let t2 = Engine.ops.renameStep(UR, doc, 'load-context', 'load-ctx');
let d2 = Engine.lineDiff(UR, t2);
const refs = (UR.match(/"load-context"/g) || []).length; // id line + needs refs
ok('rename-minimal', d2.ops.filter(o => o.type === 'del').length === refs, 'del=' + d2.ops.filter(o => o.type === 'del').length + ' expected=' + refs);
const js2 = Engine.parse(t2).js;
ok('rename-rewired', js2.steps.find(s => s.id === 'branch-setup').needs[0] === 'load-ctx');
ok('rename-valid', Engine.validate(js2).length === 0);

// 2c. add a step at the end -> pure addition
let t3 = Engine.ops.addStep(UR, doc, { id: 'post-mortem', title: 'Post mortem', needs: ['submit-and-exit'], description: 'Reflect on the review.\nWrite lessons to notes.md' });
let d3 = Engine.lineDiff(UR, t3);
ok('add-pure-addition', d3.ops.filter(o => o.type === 'del').length === 0 && d3.ops.filter(o => o.type === 'add').length > 0);
const js3 = Engine.parse(t3).js;
ok('add-parses', js3.steps[js3.steps.length - 1].id === 'post-mortem' && js3.steps[js3.steps.length - 1].description.indexOf('lessons') > 0);
ok('add-valid', Engine.validate(js3).length === 0);

// 2d. remove a mid-chain step -> block removed AND downstream cables auto-unplugged
let t4 = Engine.ops.removeStep(UR, doc, 'preflight-tests');
const js4 = Engine.parse(t4).js;
ok('remove-gone', !js4.steps.find(s => s.id === 'preflight-tests'));
ok('remove-unplugs-downstream', (js4.steps.find(s => s.id === 'phase-1-eligibility').needs || []).length === 0);
ok('remove-stays-valid', Engine.validate(js4).length === 0, JSON.stringify(Engine.validate(js4).slice(0, 2)));

// 2e. edit a long description -> only that block changes; every other step byte-identical
const step6 = 'phase-6-post-comment';
const oldDesc = doc.js.steps.find(s => s.id === step6).description;
const newDesc = oldDesc + 'Added by the visual editor: double-check tone.\n';
let t5 = Engine.ops.setStepField(UR, doc, step6, 'description', Engine.fmt.string(newDesc));
const js5 = Engine.parse(t5).js;
ok('desc-roundtrip', js5.steps.find(s => s.id === step6).description === newDesc);
let d5 = Engine.lineDiff(UR, t5);
ok('desc-diff-localized', d5.changedLines <= (oldDesc.split('\n').length + newDesc.split('\n').length + 4), 'changed=' + d5.changedLines);
ok('desc-others-intact', deepEq(js5.steps.filter(s => s.id !== step6), doc.js.steps.filter(s => s.id !== step6), 'other-steps'));

// re-encoding the SAME description must not corrupt content
let t5b = Engine.ops.setStepField(UR, doc, step6, 'description', Engine.fmt.string(oldDesc));
ok('desc-reencode-identity', Engine.parse(t5b).js.steps.find(s => s.id === step6).description === oldDesc);

// ---------- 3. gate + needs ops on web-design (5-gate showcase) ----------
const WD = fs.readFileSync(path.join(DIR, 'web-design.formula.toml'), 'utf8');
let wdoc = Engine.parse(WD);
let g1 = Engine.ops.setStepField(WD, wdoc, 'check-consensus-1', 'gate', Engine.fmt.gate({ type: 'human', id: 'extra-check', timeout: '12h' }));
const gjs = Engine.parse(g1).js;
ok('gate-added', gjs.steps.find(s => s.id === 'check-consensus-1').gate.id === 'extra-check');
ok('gate-diff-1-line', Engine.lineDiff(WD, g1).ops.filter(o => o.type === 'add').length === 1);
let g2 = Engine.ops.setStepField(WD, wdoc, 'push-and-await-feedback-1', 'gate', null);
ok('gate-removed', !Engine.parse(g2).js.steps.find(s => s.id === 'push-and-await-feedback-1').gate);

let g3 = Engine.ops.setStepField(WD, wdoc, 'derive-ui-requirements', 'needs', Engine.fmt.stringArray(['intake-source', 'design-direction']));
const g3js = Engine.parse(g3).js;
ok('needs-set', deepEq(g3js.steps.find(s => s.id === 'derive-ui-requirements').needs, ['intake-source', 'design-direction'], 'needs'));
ok('needs-now-cyclic', Engine.validate(g3js).some(f => f.lamp === 'cycles'), 'cycle must be caught: derive->direction->derive');

// ---------- 4. validation trip-wires (mirror of internal/formula/validate.go) ----------
const tiny = 'formula = "t"\n[[steps]]\nid = "a"\n[[steps]]\nid = "a"\n';
ok('dup-ids-caught', Engine.validate(Engine.parse(tiny).js).some(f => f.lamp === 'ids'));
const dangling2 = 'formula = "t"\n[[steps]]\nid = "a"\nneeds = ["zzz"]\n';
ok('dangling-caught', Engine.validate(Engine.parse(dangling2).js).some(f => f.lamp === 'needs'));
const cyc = 'formula = "t"\n[[steps]]\nid = "a"\nneeds = ["b"]\n[[steps]]\nid = "b"\nneeds = ["a"]\n';
ok('cycle-caught', Engine.validate(Engine.parse(cyc).js).some(f => f.lamp === 'cycles'));
let syntaxErr = false;
try { Engine.parse('formula = "unclosed\n'); } catch (e) { syntaxErr = true; }
ok('syntax-caught', syntaxErr);
const badvar = 'formula = "t"\n[vars.x]\nsource = "magic"\n[[steps]]\nid = "a"\n';
ok('var-source-caught', Engine.validate(Engine.parse(badvar).js).some(f => f.lamp === 'parse'));
const coll = 'formula = "t"\n[inputs.x]\ntype = "string"\n[vars.x]\ndefault = "y"\n[[steps]]\nid = "a"\n';
ok('collision-caught', Engine.validate(Engine.parse(coll).js).some(f => f.lamp === 'parse'));

// ---------- 5. convoy model + computed layout, DERIVED from tomllib truth ----------
// Shape expectations are computed from the parsed ground truth (truth[f], already
// loaded above) so a legitimate formula edit moves both sides together and never
// reads as an engine regression (Cross-Review H-4).
const DZname = 'design.formula.toml';
const DZ = fs.readFileSync(path.join(DIR, DZname), 'utf8');
const dz = Engine.parse(DZ);
const dt = truth[DZname];
const expNodes = dt.legs.length + (dt.synthesis ? 1 : 0);
const expEdges = dt.synthesis ? (dt.synthesis.depends_on || []).length : 0;
const model = Engine.graphModel(dz.js);
ok('convoy-model', model.type === 'convoy' && model.nodes.length === expNodes && model.edges.length === expEdges,
  'nodes ' + model.nodes.length + '/' + expNodes + ' edges ' + model.edges.length + '/' + expEdges);
const lay = Engine.layout(model);
ok('convoy-layout', lay.maxRank === (dt.synthesis ? 1 : 0) && lay.ranks[0].length === dt.legs.length && (!dt.synthesis || lay.ranks[1].length === 1),
  'maxRank=' + lay.maxRank + ' rank0=' + lay.ranks[0].length + ' rank1=' + (lay.ranks[1] ? lay.ranks[1].length : 'n/a'));
const synthNeeds = dt.synthesis ? (dt.synthesis.depends_on || []).length : 0;
const expSynthRarity = expectedRarity(false, synthNeeds > 1, synthNeeds === 1);
ok('convoy-synth-epic', model.nodes.find(n => n.synthesis).rarity === expSynthRarity,
  'rarity=' + (model.nodes.find(n => n.synthesis) || {}).rarity + ' expected=' + expSynthRarity);

const WDname = 'web-design.formula.toml';
const wdt = truth[WDname];
const wm = Engine.graphModel(Engine.parse(WD).js);
const wl = Engine.layout(wm);
ok('webdesign-layout-chain', wl.maxRank === longestPathRank(wdt.steps), 'maxRank=' + wl.maxRank + ' expected=' + longestPathRank(wdt.steps));
ok('webdesign-gates-legendary', wm.nodes.filter(n => n.rarity === 'legendary').length === wdt.steps.filter(s => s.gate).length,
  'legendary=' + wm.nodes.filter(n => n.rarity === 'legendary').length + ' expected=' + wdt.steps.filter(s => s.gate).length);

// ---------- 6. inputs/vars table ops ----------
let v1 = Engine.ops.addNamedTable(WD, wdoc, 'vars', 'reviewer', { description: 'Who reviews', source: 'cli', default: 'manager' });
const v1js = Engine.parse(v1).js;
ok('var-added', v1js.vars.reviewer && v1js.vars.reviewer.default === 'manager');
ok('var-add-pure', Engine.lineDiff(WD, v1).ops.filter(o => o.type === 'del').length === 0);
let v2 = Engine.ops.removeNamedTable(v1, Engine.parse(v1), 'vars', 'reviewer');
ok('var-removed', !Engine.parse(v2).js.vars || !Engine.parse(v2).js.vars.reviewer);
ok('var-roundtrip-identity', v2 === WD, 'add+remove should restore the exact original bytes');

// ---------- 7. delta-a: expansion formulas get JS cycle detection ----------
// The JS twin of the Phase 0 Go fix (validate.go checkTemplateCycles). The engine's
// findCycle is type-agnostic; delta-a widens the cycle-lamp guard to run for
// expansions too. No store formula is an expansion, so this is exercised only here.
const expCyc = 'formula = "t"\ntype = "expansion"\n[[template]]\nid = "a"\nneeds = ["b"]\n[[template]]\nid = "b"\nneeds = ["a"]\n';
ok('expansion-cycle-caught', Engine.validate(Engine.parse(expCyc).js).some(f => f.lamp === 'cycles'),
  'delta-a: findCycle must run for expansion templates');
const expAcyclic = 'formula = "t"\ntype = "expansion"\n[[template]]\nid = "a"\n[[template]]\nid = "b"\nneeds = ["a"]\n';
ok('expansion-acyclic-clean', Engine.validate(Engine.parse(expAcyclic).js).length === 0,
  'acyclic expansion must stay valid: ' + JSON.stringify(Engine.validate(Engine.parse(expAcyclic).js).slice(0, 2)));

// ---------- 8. encoding-edge cases (Cross-Review H-4): CRLF / no-newline / non-UTF-8 ----------
const lfSrc = 'formula = "t"\n[[steps]]\nid = "a"\nneeds = []\n[[steps]]\nid = "b"\nneeds = ["a"]\n';
const crlfSrc = lfSrc.replace(/\n/g, '\r\n');
ok('crlf-parses-like-lf', deepEq(Engine.parse(crlfSrc).js, Engine.parse(lfSrc).js, 'crlf-vs-lf'));
ok('crlf-valid', Engine.validate(Engine.parse(crlfSrc).js).length === 0);
const noNlSrc = 'formula = "t"\n[[steps]]\nid = "a"';   // no trailing newline
ok('no-trailing-newline-parses', Engine.parse(noNlSrc).js.steps[0].id === 'a');
ok('no-trailing-newline-eq', deepEq(Engine.parse(noNlSrc).js, Engine.parse(noNlSrc + '\n').js, 'no-nl-vs-nl'));
// invalid UTF-8 byte (0xff) inside a string decodes to U+FFFD and must NOT crash the parser
const nonUtf8 = Buffer.from([0x66, 0x6f, 0x72, 0x6d, 0x75, 0x6c, 0x61, 0x20, 0x3d, 0x20, 0x22, 0xff, 0x22, 0x0a]).toString('utf8');
let nonUtf8Threw = false, nonUtf8Doc = null;
try { nonUtf8Doc = Engine.parse(nonUtf8); } catch (e) { nonUtf8Threw = true; }
ok('non-utf8-no-throw', !nonUtf8Threw && nonUtf8Doc && nonUtf8Doc.js.formula === '�',
  'invalid byte should decode to U+FFFD without throwing');

// ---------- 9. diff-minimality: removeStep / gate-removal / needs-set / root-region setKV ----------
{
  const rdoc = Engine.parse(UR);
  const rt = Engine.ops.removeStep(UR, rdoc, 'preflight-tests');
  const rd = Engine.lineDiff(UR, rt);
  ok('removestep-no-unrelated-adds', rd.ops.filter(o => o.type === 'add').length === 0, 'adds=' + rd.ops.filter(o => o.type === 'add').length);
  ok('removestep-deletes-block', rd.ops.filter(o => o.type === 'del').length > 0);
}
{
  const gdoc = Engine.parse(WD);
  const gt = Engine.ops.setStepField(WD, gdoc, 'push-and-await-feedback-1', 'gate', null);
  const gd = Engine.lineDiff(WD, gt);
  ok('gate-removal-min', gd.ops.filter(o => o.type === 'add').length === 0 && gd.ops.filter(o => o.type === 'del').length === 1,
    'del=' + gd.ops.filter(o => o.type === 'del').length + ' add=' + gd.ops.filter(o => o.type === 'add').length);
  ok('gate-removal-gone', !Engine.parse(gt).js.steps.find(s => s.id === 'push-and-await-feedback-1').gate);
}
{
  const ndoc = Engine.parse(WD);
  const nt = Engine.ops.setStepField(WD, ndoc, 'derive-ui-requirements', 'needs', Engine.fmt.stringArray(['intake-source']));
  const nd = Engine.lineDiff(WD, nt);
  ok('needs-set-min', nd.changedLines <= 2, 'changed=' + nd.changedLines);
  ok('needs-set-applied', deepEq(Engine.parse(nt).js.steps.find(s => s.id === 'derive-ui-requirements').needs, ['intake-source'], 'needs-set'));
}
{
  const sdoc = Engine.parse(WD);
  const st = Engine.ops.setKV(WD, sdoc, null, 'version', '99');
  const sd = Engine.lineDiff(WD, st);
  ok('root-setkv-min', sd.changedLines <= 2 && sd.changedLines >= 1, 'changed=' + sd.changedLines);
  ok('root-setkv-applied', Engine.parse(st).js.version === 99);
}

// ---------- 10. Phase 1b parity-fixture corpus: dual-consumer verdict interlock ----------
// The frozen corpus at internal/formula/testdata/parity/ is the SAME manifest the root Go test
// (internal/formula/parity_test.go) asserts against the composed Go verdict. Asserting it here
// against the shipped JS engine makes the two validators agree per fixture — so a rule that lands
// in one validator but not the other flips a recorded verdict and reddens this lane. PARITY_DIR
// reaches across the module wall to the single source of truth (conflicts.md T8: test DATA may
// cross legally — no import, no go.mod entry; the toml-conformance lane checks out the full repo).
// Assertions key on accept/reject and the JS lamp CATEGORY, never message text — Go and JS word
// their cycle errors differently ("cycle detected involving step" vs the cycles lamp).
const PARITY_DIR = path.join(ROOT, 'internal', 'formula', 'testdata', 'parity');

// jsVerdict reduces the shipped engine's two rejection channels to one shape:
//   - Engine.parse THROWS (malformed TOML, or any datetime — the Gap-13 dialect divergence)
//   - Engine.validate returns findings, each carrying a lamp (parse|ids|needs|cycles)
// accept == parse succeeds AND validate returns zero findings.
function jsVerdict(text) {
  let doc;
  try { doc = Engine.parse(text); }
  catch (e) { return { accept: false, channel: 'parse-throw', lamps: [] }; }
  const findings = Engine.validate(doc.js);
  return { accept: findings.length === 0, channel: 'validate', lamps: findings.map(function (f) { return f.lamp; }) };
}

function runParityFixtures(dir) {
  const manifest = JSON.parse(fs.readFileSync(path.join(dir, 'manifest.json'), 'utf8'));
  (manifest.fixtures || []).forEach(function (fx) {
    const v = jsVerdict(fs.readFileSync(path.join(dir, fx.file), 'utf8'));
    if (fx.verdict === 'accept') {
      ok('parity-accept:' + fx.file, v.accept === true, 'ch=' + v.channel + ' lamps=[' + v.lamps + ']');
    } else {
      ok('parity-reject:' + fx.file, v.accept === false, 'expected reject (Go stage ' + (fx.stage || '?') + ')');
      if (fx.lamp) {
        ok('parity-lamp:' + fx.file, v.lamps.indexOf(fx.lamp) !== -1, 'want lamp ' + fx.lamp + ' got [' + v.lamps + ']');
      }
    }
  });
  // parseDialect pins the ONE intentional Go/JS divergence (Gap 13): Go accepts a datetime on an
  // unknown key while the JS engine rejects every datetime at parse. The JS channel is pinned
  // here; the Go acceptance is pinned in parity_test.go. This asserts a KNOWN divergence, not
  // agreement — so the fixture that would otherwise look like a parity failure is expected.
  (manifest.parseDialect || []).forEach(function (dx) {
    const v = jsVerdict(fs.readFileSync(path.join(dir, dx.file), 'utf8'));
    if (dx.js === 'parse-error') {
      ok('parity-dialect:' + dx.file, v.channel === 'parse-throw', 'expected JS parse throw, got ch=' + v.channel);
    } else {
      ok('parity-dialect:' + dx.file, v.accept === true, 'expected JS accept, got ch=' + v.channel);
    }
  });
}
if (fs.existsSync(PARITY_DIR)) { runParityFixtures(PARITY_DIR); }

console.log('\n' + pass + ' passed, ' + fail + ' failed');
process.exit(fail ? 1 : 0);
