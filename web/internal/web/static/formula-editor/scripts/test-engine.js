// Headless proof of the editor's round-trip contract, runnable by any reviewer:
//
//   node .designs/web-ui/prototype-v2/scripts/test-engine.js
//
// It parses every real formula in .agentfactory/store/formulas/, cross-checks the
// parse against Python's tomllib (the conformance ground truth), and verifies that
// each visual-edit operation produces a diff limited to the intended change —
// the "minimal diff" bar set by the iteration-1 feedback.
'use strict';
const fs = require('fs');
const path = require('path');
const { execFileSync } = require('child_process');

const Engine = require(path.join(__dirname, 'toml-engine.js'));
const ROOT = path.resolve(__dirname, '..', '..', '..', '..');
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
ok('needs-now-cyclic', Engine.validate(g3js).some(f => f.lamp === 'cycles'), 'cycle must be caught: derive→direction→derive');

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

// ---------- 5. convoy model + computed layout (fan-out/merge case) ----------
const DZ = fs.readFileSync(path.join(DIR, 'design.formula.toml'), 'utf8');
const dz = Engine.parse(DZ);
const model = Engine.graphModel(dz.js);
ok('convoy-model', model.type === 'convoy' && model.nodes.length === 7 && model.edges.length === 6);
const lay = Engine.layout(model);
ok('convoy-layout', lay.maxRank === 1 && lay.ranks[0].length === 6 && lay.ranks[1].length === 1);
ok('convoy-synth-epic', model.nodes.find(n => n.synthesis).rarity === 'epic');

const wm = Engine.graphModel(Engine.parse(WD).js);
const wl = Engine.layout(wm);
ok('webdesign-layout-chain', wl.maxRank === 21, 'maxRank=' + wl.maxRank);
ok('webdesign-gates-legendary', wm.nodes.filter(n => n.rarity === 'legendary').length === 5);

// ---------- 6. inputs/vars table ops ----------
let v1 = Engine.ops.addNamedTable(WD, wdoc, 'vars', 'reviewer', { description: 'Who reviews', source: 'cli', default: 'manager' });
const v1js = Engine.parse(v1).js;
ok('var-added', v1js.vars.reviewer && v1js.vars.reviewer.default === 'manager');
ok('var-add-pure', Engine.lineDiff(WD, v1).ops.filter(o => o.type === 'del').length === 0);
let v2 = Engine.ops.removeNamedTable(v1, Engine.parse(v1), 'vars', 'reviewer');
ok('var-removed', !Engine.parse(v2).js.vars || !Engine.parse(v2).js.vars.reviewer);
ok('var-roundtrip-identity', v2 === WD, 'add+remove should restore the exact original bytes');

console.log('\n' + pass + ' passed, ' + fail + ' failed');
process.exit(fail ? 1 : 0);
