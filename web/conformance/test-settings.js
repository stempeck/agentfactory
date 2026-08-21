// Behavioural proof of the shipped settings client (#620 Phase 3), runnable by any reviewer:
//
//   node web/conformance/test-settings.js
//
// and executed by `make conformance` and by CI's toml-conformance job (#620 Phase 4 wired it into
// both hand-maintained script lists). It exists because the Go-side gate
// (web/internal/web/settings_save_test.go) is a SOURCE SCAN — it can see that there is one write
// call site, but it cannot see what the client actually sends.
//
// It drives the SHIPPED functions — the same bytes served to the browser and embedded via
// //go:embed static — against a fake DOM and a scripted API double, and asserts the PAYLOAD and the
// REQUESTS, not the shape of the source. The mutations this catches and a source scan does not:
//
//     out.push({ labels: labels, agent: agent });          // rebuild under a different literal
//     if (mode === 'all') { doc.agents = []; }             // sentinel collapsed to the wrong arm
//     doc.color = !!(byId('set-sl-color').checked);        // absent-means-ON written away
//     raw.labels = labels;                                 // without deleting a lone `label`
//     return API.put(...).then(function () { self.load(); }) // re-read outside the ok gate
//
// Each of those keeps every Go test green while changing what lands on disk.
'use strict';
const fs = require('fs');
const path = require('path');

const APP = path.join(__dirname, '..', 'internal', 'web', 'static', 'app.js');
const src = fs.readFileSync(APP, 'utf8');

let pass = 0, fail = 0;
function ok(name, cond, extra) {
  if (cond) { pass++; } else { fail++; console.log('FAIL', name, extra === undefined ? '' : extra); }
}
function eq(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  ok(name, g === w, 'got ' + g + ' want ' + w);
}

// --- extract the shipped source --------------------------------------------------------

function sliceBalanced(from) {
  const open = src.indexOf('{', from);
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') { depth++; }
    else if (src[i] === '}') { depth--; if (depth === 0) { return src.slice(from, i + 1); } }
  }
  throw new Error('unbalanced braces at ' + from);
}
function funcSrc(name) {
  const at = src.indexOf('function ' + name + '(');
  if (at < 0) { throw new Error('app.js: no function ' + name); }
  return sliceBalanced(at);
}
// varSrc lifts a `var NAME = <literal>;` declaration, scanning to the first `;` outside any bracket.
function varSrc(name) {
  const at = src.indexOf('var ' + name + ' = ');
  if (at < 0) { throw new Error('app.js: no var ' + name); }
  let depth = 0;
  for (let i = at; i < src.length; i++) {
    const c = src[i];
    if (c === '{' || c === '[' || c === '(') { depth++; }
    else if (c === '}' || c === ']' || c === ')') { depth--; }
    else if (c === ';' && depth === 0) { return src.slice(at, i + 1); }
  }
  throw new Error('app.js: unterminated var ' + name);
}

const CONSTS = ['SETTINGS_WRITABLE', 'UNMANAGED_FILES', 'UNMANAGED_CLI', 'STATUSLINE_ELEMENTS',
  'STARTUP_GATES', 'SETTINGS_SKEW_KEY'].map(varSrc).join('\n');

const FUNCS = ['el', 'telLine', 'show', 'hide', 'showValidation', 'cloneDoc', 'snapshotBaselines',
  'disarmSettingsReload', 'reloadSettings',
  'settingsFiles', 'renderSettings', 'renderDisposition', 'renderDispatchPanel', 'settingsMapRow',
  'mappingRoleFields', 'firstReroutedIndex', 'collectMappings', 'renderStartupPanel', 'renderStatuslinePanel', 'renderStatuslineElements',
  'collectStatuslineElements', 'syncStartupControls', 'renderFactoryPanel', 'renderUnmanagedPanel', 'settingsFactRow',
  'renderRawEditor', 'renderSkewBanner', 'settingsSkewBaseline', 'rememberSettingsSkewBaseline',
  'settingsPayload', 'settingsBaseline', 'applyDispatchControls', 'applyStartupControls',
  'applyStatuslineControls', 'settingsDiff', 'diffKeys', 'diffElements', 'refreshSettingsPanel',
  'saveSettingsFile', 'settingsFailureCopy', 'showSettingsError', 'dismissSettingsError',
  'readRole', 'splitList', 'hasMember', 'isEmptyObject', 'isPlainObject', 'sameJSON', 'setMember', 'dropMember',
  'dropUnlessExplicitNull', 'setRadio', 'getRadio', 'selectWithUnknown', 'settingsHasOption',
].map(funcSrc).join('\n');

const EXPORTS = ['renderSettings', 'settingsPayload', 'collectMappings', 'settingsDiff',
  'saveSettingsFile', 'settingsFailureCopy', 'settingsMapRow', 'refreshSettingsPanel', 'reloadSettings',
  'snapshotBaselines'];

// eslint-disable-next-line no-new-func
const factory = new Function('SettingsViewModel', 'byId', 'document', 'window', 'API',
  CONSTS + '\n' + FUNCS + '\n return {' + EXPORTS.map((n) => n + ': ' + n).join(', ') + '};');

// The SHIPPED view model, so the read path is the one under test rather than the stub every other
// section drives. `baselines` is filled in by load() and by nothing else, so a harness that snapshots
// them itself — as mount() below must, to stay synchronous — cannot see load() stop doing it.
// eslint-disable-next-line no-new-func
const loadFactory = new Function('byId', 'document', 'window', 'API', 'toast',
  CONSTS + '\n' + FUNCS + '\n' + varSrc('SettingsViewModel') + '\n return { vm: SettingsViewModel };');

// --- a DOM small enough to read and real enough to lie about nothing --------------------

function makeDom() {
  const ids = {};
  function walk(node, out) {
    node.children.forEach((c) => { out.push(c); walk(c, out); });
    return out;
  }
  function parseSel(sel) {
    let tag = null, cls = null, attr = null, val = null, rest = sel;
    const ab = rest.indexOf('[');
    if (ab >= 0) {
      const inner = rest.slice(ab + 1, rest.lastIndexOf(']'));
      rest = rest.slice(0, ab);
      const e = inner.indexOf('=');
      if (e < 0) { attr = inner; } else { attr = inner.slice(0, e); val = inner.slice(e + 1).replace(/^"|"$/g, ''); }
    }
    const dot = rest.indexOf('.');
    if (dot >= 0) { cls = rest.slice(dot + 1); rest = rest.slice(0, dot); }
    if (rest) { tag = rest; }
    return { tag, cls, attr, val };
  }
  function matches(n, p) {
    if (p.tag && n.tag !== p.tag) { return false; }
    if (p.cls && String(n.className || '').split(/\s+/).indexOf(p.cls) < 0) { return false; }
    if (p.attr) {
      if (!Object.prototype.hasOwnProperty.call(n.attrs, p.attr)) { return false; }
      if (p.val !== null && n.attrs[p.attr] !== p.val) { return false; }
    }
    return true;
  }
  function query(scope, sel) {
    sel = String(sel).trim();
    const sp = sel.indexOf(' ');
    if (sp > 0 && sel[0] === '#') {
      const head = sel.slice(1, sp);
      return ids[head] ? query(ids[head], sel.slice(sp + 1)) : [];
    }
    const p = parseSel(sel);
    return walk(scope, []).filter((n) => matches(n, p));
  }

  function Node(tag) {
    this.tag = tag;
    this.attrs = {};
    this.className = '';
    this.children = [];
    this.checked = false;
    this.selected = false;
    this.hidden = false;
    this.disabled = false;
    this.type = '';
    this.placeholder = '';
    this.parent = null;
    this._text = '';
  }
  Node.prototype.setAttribute = function (k, v) { this.attrs[k] = String(v); if (k === 'id') { ids[v] = this; } };
  Node.prototype.getAttribute = function (k) {
    return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null;
  };
  Node.prototype.removeAttribute = function (k) { delete this.attrs[k]; };
  Node.prototype.appendChild = function (n) { n.parent = this; this.children.push(n); return n; };
  Node.prototype.remove = function () {
    if (!this.parent) { return; }
    const i = this.parent.children.indexOf(this);
    if (i >= 0) { this.parent.children.splice(i, 1); }
  };
  Node.prototype.addEventListener = function (evt, fn) { (this._on || (this._on = {}))[evt] = fn; };
  Node.prototype.click = function () { if (this._on && this._on.click) { this._on.click(); } };
  Node.prototype.querySelector = function (sel) { return query(this, sel)[0] || null; };
  Node.prototype.querySelectorAll = function (sel) { return query(this, sel); };
  Object.defineProperty(Node.prototype, 'textContent', {
    get() { return this._text; },
    set(v) { this._text = String(v); this.children = []; },
  });
  Object.defineProperty(Node.prototype, 'innerHTML', {
    get() { return ''; },
    // The pinned invariant, enforced rather than described: the client clears with innerHTML and
    // builds with el()/textContent. A non-empty assignment is an XSS sink and fails here.
    set(v) { if (v !== '') { throw new Error('non-empty innerHTML assignment: ' + v); } this.children = []; },
  });
  Object.defineProperty(Node.prototype, 'value', {
    get() {
      if (this.tag !== 'select') { return this._value === undefined ? '' : this._value; }
      const opts = walk(this, []).filter((n) => n.tag === 'option');
      const on = opts.filter((o) => o.selected);
      if (on.length) { return on[on.length - 1]._value || ''; }
      // Assigning a value no <option> carries leaves selectedIndex at -1, and the select then reads
      // as '' — NOT as its first option. Falling back to the first option here would have the harness
      // report an agent the operator had just cleared.
      if (this._value !== undefined) {
        return opts.some((o) => (o._value || '') === this._value) ? this._value : '';
      }
      return opts.length ? (opts[0]._value || '') : '';
    },
    set(v) {
      this._value = String(v);
      if (this.tag === 'select') {
        walk(this, []).filter((n) => n.tag === 'option')
          .forEach((o) => { o.selected = ((o._value || '') === this._value); });
      }
    },
  });

  const root = new Node('body');
  const doc = {
    createElement(tag) { return new Node(tag); },
    createTextNode(t) { const n = new Node('#text'); n._text = String(t); return n; },
    getElementById(id) { return ids[id] || null; },
    querySelector(sel) { return query(root, sel)[0] || null; },
    querySelectorAll(sel) { return query(root, sel); },
  };
  return { doc, root, ids, Node };
}

// buildSettingsDom mirrors the ids and control names index.html actually ships. It is written out
// rather than parsed so a rename in index.html shows up here as a failure, not as a silent no-op.
function buildSettingsDom() {
  const dom = makeDom();
  const { doc, root } = dom;
  function add(parent, tag, id, cls) {
    const n = doc.createElement(tag);
    if (id) { n.setAttribute('id', id); }
    if (cls) { n.className = cls; }
    parent.appendChild(n);
    return n;
  }
  function radios(parent, name, values) {
    values.forEach((v) => {
      const r = add(parent, 'input');
      r.type = 'radio';
      r.setAttribute('name', name);
      r.value = v;
    });
  }
  add(root, 'button', 'set-reload').textContent = 'Reload settings';
  add(root, 'div', 'set-banner');

  ['dispatch', 'startup', 'messaging', 'statusline'].forEach((f) => {
    const p = add(root, 'section', 'set-panel-' + f);
    add(p, 'p', 'set-reason-' + f);
    add(p, 'p', 'set-when-' + f);
    add(p, 'textarea', 'set-adv-' + f);
    add(p, 'div', 'set-diff-' + f);
    add(p, 'button', 'set-save-' + f).textContent = 'Save ' + f + '.json';
    add(p, 'button', 'set-dismiss-' + f).hidden = true;
    add(p, 'p', 'set-' + f + '-err').hidden = true;
    add(p, 'p', 'set-' + f + '-ok').hidden = true;

    if (f === 'dispatch') {
      add(p, 'input', 'set-trigger');
      add(p, 'div', 'mapRows');
      add(p, 'button', 'set-add-row');
    }
    if (f === 'startup') {
      add(p, 'input', 'set-startdispatch').type = 'checkbox';
      radios(p, 'set-startup-agents-mode', ['all', 'none', 'list']);
      add(p, 'input', 'set-startup-agents');
      ['quality', 'fidelity', 'improvement', 'telemetry'].forEach((g) => {
        const sel = add(p, 'select', 'set-gate-' + g);
        ['', 'on', 'off', 'default'].forEach((v) => {
          const o = doc.createElement('option');
          o.value = v;
          sel.appendChild(o);
        });
      });
    }
    if (f === 'statusline') {
      add(p, 'div', 'set-statusline-elements');
      radios(p, 'set-statusline-color', ['default', 'on', 'off']);
    }
  });

  const fp = add(root, 'section', 'set-panel-factory');
  add(fp, 'p', 'set-reason-factory');
  add(fp, 'p', 'set-when-factory');
  add(fp, 'pre', 'set-factory');
  add(root, 'div', 'set-unmanaged');
  return dom;
}

// --- the fixture payload ----------------------------------------------------------------

const AGENTS = [{ name: 'manager' }, { name: 'go' }, { name: 'python' }];
const PROFILES = ['default', 'sonnet-cheap'];

function view(doc, over) {
  return Object.assign({ doc, tier: 'raw', writable: true, reason: 'r', effective_when: 'w' }, over || {});
}

// mount wires one payload to a fresh DOM and renders it exactly as load() does — snapshotting the
// baselines from the document AS READ before any control can touch the retained objects.
function mount(files, opts) {
  opts = opts || {};
  const dom = buildSettingsDom();
  // Shared across mounts when the caller passes one, so a second read can see what the first stored —
  // which is the only way the skew comparison is reachable at all.
  const store = opts.store || {};
  const win = {
    sessionStorage: {
      getItem(k) { return Object.prototype.hasOwnProperty.call(store, k) ? store[k] : null; },
      setItem(k, v) { store[k] = String(v); },
    },
  };
  const puts = [];
  const loads = [];
  const vm = {
    data: { files, agents: AGENTS, profiles: PROFILES, schema_fingerprint: opts.fingerprint === undefined ? 'abc' : opts.fingerprint },
    agents: AGENTS,
    profiles: PROFILES,
    baselines: {},
    rows: [],
    load() { loads.push(1); return Promise.resolve(); },
  };
  // The baselines come from the SHIPPED snapshotBaselines, not from a copy of it here: a harness that
  // re-implements the function under test cannot see it break.
  vm.baselines = null;   // filled in below, once the module is built
  const responses = (opts.responses || []).slice();
  const API = {
    put(p, body, extra) {
      puts.push({ path: p, body: JSON.parse(JSON.stringify(body)), extra: extra || {} });
      const r = responses.length ? responses.shift() : { ok: true, _status: 200 };
      return Promise.resolve(r);
    },
  };
  const m = factory(vm, dom.doc.getElementById.bind(dom.doc), dom.doc, win, API);
  vm.baselines = m.snapshotBaselines(files);
  m.renderSettings();
  return { m, vm, dom, puts, loads, byId: dom.doc.getElementById.bind(dom.doc), store };
}

// ==================== M1: the save carries back everything the read carried ====================

{
  // A dispatch.json with keys this console has never heard of, plus a mapping carrying `source` and
  // a model pin. The operator changes ONE field.
  const doc = {
    trigger_label: 'dispatch-me',
    interval_seconds: 30,
    notify_on_complete: true,
    repos: ['a', 'b'],
    workflows: { nightly: { formula: 'x' } },
    a_key_af_grows_tomorrow: { deep: [1, 2, { three: true }] },
    mappings: [{ labels: ['a', 'b'], source: 'pr', agent: 'go', model: 'sonnet-cheap' }],
  };
  const h = mount({ dispatch: view(doc) });
  h.byId('set-trigger').value = 'ship-it';
  const p = h.m.settingsPayload('dispatch');

  eq('M1 trigger_label is the only top-level change', p.trigger_label, 'ship-it');
  eq('M1 interval_seconds survives', p.interval_seconds, 30);
  eq('M1 notify_on_complete survives', p.notify_on_complete, true);
  eq('M1 repos survives', p.repos, ['a', 'b']);
  eq('M1 workflows survives', p.workflows, { nightly: { formula: 'x' } });
  eq('M1 a key af grows tomorrow survives untouched', p.a_key_af_grows_tomorrow, { deep: [1, 2, { three: true }] });
  eq('M1 mapping source survives', p.mappings[0].source, 'pr');
  eq('M1 mapping model pin survives', p.mappings[0].model, 'sonnet-cheap');
  eq('M1 mapping labels survive in order', p.mappings[0].labels, ['a', 'b']);
  // The strong form of the property: apart from the one edited member, the payload is byte-identical
  // to what the server sent. Nothing was selected, copied or re-derived. This is the byte-level half
  // of AC-1 — the flow-level half is the TestSettingsSave_PreservesUndisplayedConfig block at the end
  // of the async section, which names the three blocks the AC calls out one at a time.
  const asRead = JSON.parse(h.vm.baselines.dispatch);
  asRead.trigger_label = 'ship-it';
  eq('M1 the payload differs from the document as read in exactly the edited member', p, asRead);
}

{
  // The accumulation trap. settingsPayload runs on EVERY keystroke (the diff calls it), so a version
  // that mutated the retained document would leave its last write behind: type over a value, change
  // your mind, put the original back — and the save would still carry the abandoned one, which is
  // visible nowhere on screen.
  const h = mount({ dispatch: view({ trigger_label: 'a', repos: ['x'] }) });
  const box = h.byId('set-trigger');
  ['b', 'bc', 'bcd'].forEach((v) => { box.value = v; h.m.settingsDiff('dispatch'); });
  box.value = 'a';
  eq('purity reverting a control abandons the intermediate values', h.m.settingsPayload('dispatch'),
    { trigger_label: 'a', repos: ['x'] });
  eq('purity reverting a control clears the diff', h.m.settingsDiff('dispatch'), []);
  ok('purity a reverted panel cannot be saved', (h.m.refreshSettingsPanel('dispatch'), h.byId('set-save-dispatch').disabled === true));
}

{
  // Two editors over one document. A control the operator never touched must not undo what they
  // typed into the Advanced box — the usual way a two-editor panel eats an edit.
  const h = mount({ startup: view({ agents: ['manager'] }) });
  h.byId('set-adv-startup').value = '{"agents":["manager"],"start_dispatch":true,"watchdog_agents":["go"]}';
  const p = h.m.settingsPayload('startup');
  eq('two editors an untouched checkbox does not undo the raw edit', p.start_dispatch, true);
  eq('two editors an untouched control leaves a key it does not own alone', p.watchdog_agents, ['go']);
  // ...and a control the operator DID move still wins over the raw text.
  h.byId('set-startdispatch').checked = true;
  h.byId('set-adv-startup').value = '{"agents":["manager"],"start_dispatch":false}';
  eq('two editors a moved control wins over the raw text', h.m.settingsPayload('startup').start_dispatch, true);
}

{
  // An untouched lone-label row keeps the lone-label form: looking at a document must not rewrite it.
  const doc = { mappings: [{ label: 'x', agent: 'go', source: 'issue' }] };
  const h = mount({ dispatch: view(doc) });
  const p = h.m.settingsPayload('dispatch');
  eq('M1 untouched lone label is not rewritten', p.mappings[0].label, 'x');
  ok('M1 untouched lone-label row grows no labels key', !('labels' in p.mappings[0]));
  eq('M1 untouched lone-label row keeps source', p.mappings[0].source, 'issue');
  eq('M1 an untouched document has an empty diff', h.m.settingsDiff('dispatch'), []);
}

{
  // An EDITED lone-label row must emit `labels` AND delete `label` — af-core rejects both-set as
  // ambiguous (internal/config/dispatch.go:170-172).
  const doc = { mappings: [{ label: 'x', agent: 'go', source: 'issue' }] };
  const h = mount({ dispatch: view(doc) });
  h.vm.rows[0].node.querySelector('[data-role="label"]').value = 'x, y';
  const p = h.m.settingsPayload('dispatch');
  eq('M1 edited row emits labels', p.mappings[0].labels, ['x', 'y']);
  ok('M1 edited row carries no lone label', !('label' in p.mappings[0]));
  eq('M1 edited row still keeps source', p.mappings[0].source, 'issue');
}

{
  // The label comparison must separate the elements it joins. Without a separator, ["a","b"] and
  // ["ab"] compare equal and a real edit is silently not written — a defect no reader would see,
  // because the only visible difference is one character inside a string literal.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['ab'], agent: 'go' }] }) });
  h.vm.rows[0].node.querySelector('[data-role="label"]').value = 'a, b';
  eq('M1 splitting one label into two is a real edit',
    h.m.settingsPayload('dispatch').mappings[0].labels, ['a', 'b']);
  eq('M1 splitting one label into two shows in the diff',
    h.m.settingsDiff('dispatch'), ['mappings[0].labels']);
}

{
  // Multi-label rows round-trip through one comma-joined input without losing the tail.
  const doc = { mappings: [{ labels: ['a', 'b', 'c'], agent: 'go' }] };
  const h = mount({ dispatch: view(doc) });
  eq('M1 the label input shows every label', h.vm.rows[0].node.querySelector('[data-role="label"]').value, 'a, b, c');
  eq('M1 an untouched multi-label row keeps all labels', h.m.settingsPayload('dispatch').mappings[0].labels, ['a', 'b', 'c']);
}

{
  // A row the operator adds and never fills in is not an edit, and is not sent.
  const doc = { mappings: [{ labels: ['a'], agent: 'go' }] };
  const h = mount({ dispatch: view(doc) });
  h.byId('mapRows').appendChild(h.m.settingsMapRow(null));
  eq('M1 a pristine new row is not emitted', h.m.settingsPayload('dispatch').mappings.length, 1);

  const row = h.vm.rows[1].node;
  row.querySelector('[data-role="label"]').value = 'b';
  row.querySelector('[data-role="agent"]').value = 'python';
  const p = h.m.settingsPayload('dispatch');
  eq('M1 a filled new row is emitted', p.mappings.length, 2);
  eq('M1 a new row carries exactly labels and agent', Object.keys(p.mappings[1]).sort(), ['agent', 'labels']);
  eq('M1 a new row carries what was typed', p.mappings[1], { labels: ['b'], agent: 'python' });
}

{
  // The model picker is fed by the payload's `profiles` projection, and a pin naming a profile
  // models.json no longer lists stays visible instead of being silently unpinned.
  const doc = { mappings: [{ labels: ['a'], agent: 'go', model: 'retired-profile' }] };
  const h = mount({ dispatch: view(doc) });
  const msel = h.vm.rows[0].node.querySelector('[data-role="model"]');
  const opts = msel.querySelectorAll('option').map((o) => o.value);
  ok('U1 the model picker offers every profile from the payload',
    PROFILES.every((p) => opts.indexOf(p) >= 0), JSON.stringify(opts));
  ok('U1 the model picker offers "agent default" as the blank option', opts.indexOf('') >= 0, JSON.stringify(opts));
  ok('U1 an unknown model pin stays selectable', opts.indexOf('retired-profile') >= 0, JSON.stringify(opts));
  eq('U1 an unknown model pin survives a save', h.m.settingsPayload('dispatch').mappings[0].model, 'retired-profile');
}

// ==================== sentinels: three states in, the same three states out ====================

[
  ['absent', {}, 'all'],
  ['empty array', { agents: [] }, 'none'],
  ['a list', { agents: ['manager', 'go'] }, 'list'],
].forEach(([name, stored, wantMode]) => {
  const h = mount({ startup: view(JSON.parse(JSON.stringify(stored))) });
  const on = h.dom.doc.querySelectorAll('input[name="set-startup-agents-mode"]').filter((r) => r.checked);
  eq('sentinel agents ' + name + ' renders mode ' + wantMode, on.map((r) => r.value), [wantMode]);
  const p = h.m.settingsPayload('startup');
  eq('sentinel agents ' + name + ' round-trips unchanged', p === null ? {} : p, stored);
  eq('sentinel agents ' + name + ' produces an empty diff', h.m.settingsDiff('startup'), []);
});

{
  const h = mount({ startup: view({ agents: ['manager'] }) });
  h.dom.doc.querySelectorAll('input[name="set-startup-agents-mode"]').forEach((r) => { r.checked = (r.value === 'all'); });
  const p = h.m.settingsPayload('startup');
  ok('sentinel choosing All REMOVES the key rather than writing a value nobody typed',
    p === null || !('agents' in p), JSON.stringify(p));
}
{
  const h = mount({ startup: view({ start_dispatch: true }) });
  h.dom.doc.querySelectorAll('input[name="set-startup-agents-mode"]').forEach((r) => { r.checked = (r.value === 'none'); });
  eq('sentinel choosing None writes the empty array, not null', h.m.settingsPayload('startup').agents, []);
}
{
  const h = mount({ startup: view({ agents: null }) });
  eq('sentinel an explicit null is left alone, not rewritten as absent', h.m.settingsPayload('startup').agents, null);
}
{
  // The names box must not accept a list the save would then discard.
  const h = mount({ startup: view({ agents: ['manager'] }) });
  ok('sentinel the names box is live in List mode', h.byId('set-startup-agents').disabled === false);
  h.dom.doc.querySelectorAll('input[name="set-startup-agents-mode"]').forEach((r) => { r.checked = (r.value === 'all'); });
  h.m.refreshSettingsPanel('startup');
  ok('sentinel the names box is disabled in All mode', h.byId('set-startup-agents').disabled === true);
  h.dom.doc.querySelectorAll('input[name="set-startup-agents-mode"]').forEach((r) => { r.checked = (r.value === 'none'); });
  h.m.refreshSettingsPanel('startup');
  ok('sentinel the names box is disabled in None mode', h.byId('set-startup-agents').disabled === true);
}

[
  ['absent', {}, 'default'],
  ['true', { color: true }, 'on'],
  ['false', { color: false }, 'off'],
].forEach(([name, stored, wantMode]) => {
  const h = mount({ statusline: view(JSON.parse(JSON.stringify(stored))) });
  const on = h.dom.doc.querySelectorAll('input[name="set-statusline-color"]').filter((r) => r.checked);
  eq('sentinel color ' + name + ' renders mode ' + wantMode, on.map((r) => r.value), [wantMode]);
  const p = h.m.settingsPayload('statusline');
  eq('sentinel color ' + name + ' round-trips unchanged', p === null ? {} : p, stored);
});

{
  // The gate enum's fourth state — key absent — is expressible, and is the default for a file that
  // never mentioned it.
  const h = mount({ startup: view({ quality: 'on' }) });
  eq('gates an untouched startup keeps its one gate', h.m.settingsPayload('startup'), { quality: 'on' });
  ok('gates a gate the operator never set stays absent',
    !('fidelity' in h.m.settingsPayload('startup')));
  h.byId('set-gate-quality').value = '';
  ok('gates selecting "not set" removes the key', !('quality' in (h.m.settingsPayload('startup') || {})));
}

{
  // statusline.elements keeps the document's own order, and an element af grows later still renders.
  const h = mount({ statusline: view({ elements: ['daily', 'branch', 'a-future-element'] }) });
  const boxes = h.dom.doc.querySelectorAll('#set-statusline-elements [data-role="sl-element"]');
  eq('elements the document order is preserved first', boxes.slice(0, 3).map((b) => b.value), ['daily', 'branch', 'a-future-element']);
  eq('elements an unknown element stays checked', h.m.settingsPayload('statusline').elements, ['daily', 'branch', 'a-future-element']);
}

// ==================== U1: dispositions, absence, and the secrets allow-list ====================

{
  const h = mount({
    dispatch: view({}, { reason: 'af owns dispatch.json', effective_when: 'next dispatch tick' }),
    startup: view(null, { reason: 'af owns startup.json', effective_when: 'next af up' }),
    factory: view({ root: '/f' }, { writable: false, reason: 'recorded decision C-9', effective_when: 'never' }),
    agents: view(null, { tier: 'projected', writable: false, reason: 'projected', effective_when: 'n/a' }),
    models: view(null, { tier: 'excluded', writable: false, reason: 'credential-bearing', effective_when: 'next launch' }),
    telemetry: view(null, { tier: 'excluded', writable: false, reason: 'gate file', effective_when: 'next launch' }),
    'build-host': view(null, { tier: 'excluded', writable: false, reason: 'host', effective_when: 'next build' }),
    'litellm.yaml': view(null, { tier: 'excluded', writable: false, reason: 'gateway', effective_when: 'gateway reload' }),
    // The row the server really does serve, tagged so its ABSENCE from the screen is provable.
    '.agentfactory/secrets/': view(null, { tier: 'excluded', writable: false, reason: 'MUST-NEVER-RENDER', effective_when: 'MUST-NEVER-RENDER' }),
  });

  eq('U1 a panel renders the payload reason verbatim', h.byId('set-reason-dispatch').textContent, 'af owns dispatch.json');
  ok('U1 a panel renders the payload effective_when verbatim',
    h.byId('set-when-dispatch').textContent.indexOf('next dispatch tick') >= 0, h.byId('set-when-dispatch').textContent);
  ok('U1 an absent raw file says so', h.byId('set-when-startup').textContent.indexOf('does not exist yet') >= 0,
    h.byId('set-when-startup').textContent);
  // Gotcha 8, both ways: `doc:null` means "absent from disk" for a RAW row and "no document served"
  // for a projected/excluded one, so the branch is on TIER. A present raw file must never be
  // described as absent, or the sentence is simply false on the most common panel in the view.
  ok('U1 a PRESENT raw file is not described as absent',
    h.byId('set-when-dispatch').textContent.indexOf('does not exist') < 0,
    h.byId('set-when-dispatch').textContent);
  ok('U1 a projected file gets no panel at all, so it cannot be mislabelled',
    h.byId('set-when-agents') === null && h.byId('set-panel-agents') === null);
  eq('U1 an absent file leaves the Advanced editor blank, not "{}"', h.byId('set-adv-startup').value, '');

  const un = h.byId('set-unmanaged');
  const text = JSON.stringify(un.querySelectorAll('p').map((p) => p.textContent));
  ok('U1 the unmanaged panel lists models.json', text.indexOf('models.json') >= 0, text);
  ok('U1 the unmanaged panel lists telemetry.json', text.indexOf('telemetry.json') >= 0, text);
  ok('U1 the unmanaged panel lists build-host.json', text.indexOf('build-host.json') >= 0, text);
  ok('U1 the unmanaged panel lists litellm.yaml', text.indexOf('litellm.yaml') >= 0, text);
  // C-1. The secrets row IS served (web/internal/config/tier.go:163-169); the allow-list is what
  // keeps it off the screen. A panel built from `!writable` or from `tier === 'excluded'` renders it.
  const all = JSON.stringify(un.querySelectorAll('span').map((s) => s.textContent)) + text;
  ok('U1 the unmanaged panel NEVER names the secrets directory',
    all.indexOf('secrets') < 0 && all.indexOf('MUST-NEVER-RENDER') < 0, all);
  eq('U1 the unmanaged panel lists exactly the four unmanaged files',
    un.querySelectorAll('div.set-unmanaged-item').length, 4);
  ok('U1 the unmanaged panel names the CLI that does manage each file', all.indexOf('af config models set') >= 0, all);

  ok('U1 factory.json renders read-only', h.byId('set-factory').textContent.indexOf('"root"') >= 0);
  ok('U1 there is no factory save button', h.byId('set-save-factory') === null);
  ok('U1 a panel with no changes cannot be saved', h.byId('set-save-dispatch').disabled === true);
}

{
  // The writability gate, tested where it is the ONLY thing holding the button down: the panel is
  // dirty, the payload is valid, and the file is still not the console's to write.
  const h = mount({ dispatch: view({ trigger_label: 'a' }, { writable: false, reason: 'af owns this' }) });
  h.byId('set-trigger').value = 'b';
  h.m.refreshSettingsPanel('dispatch');
  ok('U1 a DIRTY non-writable panel still cannot be saved', h.byId('set-save-dispatch').disabled === true,
    'diff=' + JSON.stringify(h.m.settingsDiff('dispatch')));
  const w = mount({ dispatch: view({ trigger_label: 'a' }, { writable: true }) });
  w.byId('set-trigger').value = 'b';
  w.m.refreshSettingsPanel('dispatch');
  ok('U1 the same dirty panel IS savable when writable', w.byId('set-save-dispatch').disabled === false);
}

// ============ row-level purity: the file half's lesson, applied one level down ============

{
  // Add a route, type a label, delete it again. The diff runs on every keystroke, so a row that
  // accumulated would leave a mapping the operator can see nowhere on screen in the PUT body.
  const h = mount({ dispatch: view({ trigger_label: 't' }) });
  h.byId('mapRows').appendChild(h.m.settingsMapRow(null));
  const row = h.vm.rows[0].node;
  row.querySelector('[data-role="label"]').value = 'x';
  h.m.settingsDiff('dispatch');
  row.querySelector('[data-role="label"]').value = '';
  eq('row purity an added-then-cleared route leaves no phantom mapping',
    h.m.settingsPayload('dispatch'), { trigger_label: 't' });
  eq('row purity an added-then-cleared route clears the diff', h.m.settingsDiff('dispatch'), []);
  h.m.refreshSettingsPanel('dispatch');
  ok('row purity the panel is not left permanently dirty', h.byId('set-save-dispatch').disabled === true);
}

{
  // A net-zero label edit must not rewrite the document's lone-`label` form into `labels`.
  const h = mount({ dispatch: view({ mappings: [{ label: 'x', agent: 'go', source: 'issue' }] }) });
  const box = h.vm.rows[0].node.querySelector('[data-role="label"]');
  box.value = 'x, y';
  h.m.settingsDiff('dispatch');
  box.value = 'x';
  eq('row purity a net-zero label edit leaves the document form alone',
    h.m.settingsPayload('dispatch').mappings[0], { label: 'x', agent: 'go', source: 'issue' });
  eq('row purity a net-zero label edit clears the diff', h.m.settingsDiff('dispatch'), []);
}

{
  // `source` has no curated control, so the Advanced box is its only editor. Touching any control on
  // that row must not throw the edit away — and a diff that stayed silent about it would be worse.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }] }) });
  h.byId('set-adv-dispatch').value = '{"mappings":[{"labels":["a"],"agent":"go","source":"pr"}]}';
  eq('two editors a raw source edit is seen', h.m.settingsDiff('dispatch'), ['mappings[0].source (added)']);
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'default';
  const p = h.m.settingsPayload('dispatch');
  eq('two editors a raw source edit survives touching that row', p.mappings[0].source, 'pr');
  eq('two editors the model pin is applied too', p.mappings[0].model, 'default');
}

{
  // Clearing a model pin back to "agent default" must REMOVE the key, not leave the old pin behind.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go', model: 'default' }] }) });
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = '';
  const p = h.m.settingsPayload('dispatch');
  ok('row a cleared model pin is removed, not left behind', !('model' in p.mappings[0]), JSON.stringify(p.mappings[0]));
  eq('row a cleared model pin shows in the diff', h.m.settingsDiff('dispatch'), ['mappings[0].model (removed)']);
}

// ============ two editors over ONE ARRAY: merge where defined, refuse where not ============
//
// These are the destructive cases. Positional merging is only meaningful while both editors still
// describe the same routes; when they do not, seeding by index does not merge, it TRANSPLANTS — one
// route's `source` lands on another route's labels and the last route is dropped. af accepts the
// result, so the damage reaches disk. Every assertion below is about that.

{
  // Insert a route in the raw box — the reason the raw box exists, since `source` has no control —
  // and touch nothing else. The rows have no opinion, so the typed document must stand verbatim.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }, { labels: ['b'], agent: 'python' }] }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({
    mappings: [
      { labels: ['x'], agent: 'manager', source: 'pr' },
      { labels: ['a'], agent: 'go' },
      { labels: ['b'], agent: 'python' },
    ],
  });
  const p = h.m.settingsPayload('dispatch');
  eq('one array an inserted route is not destroyed', p.mappings.length, 3);
  eq('one array the inserted route keeps its own labels and source', p.mappings[0],
    { labels: ['x'], agent: 'manager', source: 'pr' });
  eq('one array the routes below it are not shifted', p.mappings[2], { labels: ['b'], agent: 'python' });
}

{
  // Now insert in the raw box AND touch a control. The two editors disagree about which routes exist,
  // so there is no defined merge — and quietly picking one is how a route gets overwritten.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }, { labels: ['b'], agent: 'python' }] }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({
    mappings: [
      { labels: ['x'], agent: 'manager', source: 'pr' },
      { labels: ['a'], agent: 'go' },
      { labels: ['b'], agent: 'python' },
    ],
  });
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'default';
  const why = h.m.settingsDiff('dispatch');
  ok('one array a structural disagreement is refused, not guessed', typeof why === 'string', JSON.stringify(why));
  ok('one array the refusal counts both sides', /3 in the JSON/.test(why) && /2 on disk/.test(why), why);
  ok('one array the count refusal says what actually went wrong',
    /can no longer be matched to the row it belongs to/.test(why), why);
  h.m.refreshSettingsPanel('dispatch');
  ok('one array a refused panel cannot be saved', h.byId('set-save-dispatch').disabled === true);
  ok('one array the refusal is shown where the diff would be',
    h.byId('set-diff-dispatch').children.some((c) => /can no longer be matched/.test(c.textContent)));
}

{
  // Deleting in the raw box and touching a control is the same disagreement, and the same refusal.
  // Guessing here RESURRECTS the deleted route wearing another route's keys.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }, { labels: ['b'], agent: 'python' }] }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [{ labels: ['b'], agent: 'python' }] });
  h.vm.rows[1].node.querySelector('[data-role="model"]').value = 'default';
  ok('one array a raw deletion plus a row edit is refused',
    typeof h.m.settingsDiff('dispatch') === 'string', JSON.stringify(h.m.settingsDiff('dispatch')));
}

{
  // Adding a ROW while the raw box also changed the list is the same disagreement from the other side.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }] }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [{ labels: ['a'], agent: 'go', source: 'pr' }] });
  h.byId('mapRows').appendChild(h.m.settingsMapRow(null));
  const row = h.vm.rows[1].node;
  row.querySelector('[data-role="label"]').value = 'c';
  row.querySelector('[data-role="agent"]').value = 'python';
  ok('one array adding a row while the raw box changed the list is refused',
    typeof h.m.settingsDiff('dispatch') === 'string', JSON.stringify(h.m.settingsDiff('dispatch')));
}

{
  // REORDERING is the case equal counts cannot see. Swap two routes in the text and the array is
  // still 2 long, the rows are still 2, the baseline is still 2 — but text position 1 is now route
  // `b`, and merging row 1 onto it writes `a`'s labels over `b`'s source. af accepts the result.
  const disk = [{ labels: ['a'], agent: 'go', source: 'issue' }, { labels: ['b'], agent: 'python', source: 'pr' }];
  const h = mount({ dispatch: view({ mappings: JSON.parse(JSON.stringify(disk)) }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [disk[1], disk[0]] });
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'default';
  const why = h.m.settingsDiff('dispatch');
  ok('one array reordering the text while editing a row is refused', typeof why === 'string', JSON.stringify(why));
  ok('one array the reorder refusal names the fields the rows own', /label, agent or model/.test(why), why);
  ok('one array the reorder refusal names the position that diverged', /route 1 in the text/.test(why), why);
}

{
  // A refusal the operator cannot act on is a dead end wearing a message. The only other exit is
  // Reload, which discards EVERY dirty panel — so the way out the message names must actually work:
  // emptying the box drops the raw edits and keeps the row edits.
  const disk = [{ labels: ['a'], agent: 'go', source: 'issue' }, { labels: ['b'], agent: 'python', source: 'pr' }];
  const h = mount({ dispatch: view({ mappings: JSON.parse(JSON.stringify(disk)) }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [disk[1], disk[0]] });
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'default';
  const why = h.m.settingsDiff('dispatch');
  ok('one array the refusal names a way forward', /Emptying the JSON box/.test(why), why);

  h.byId('set-adv-dispatch').value = '';
  const p = h.m.settingsPayload('dispatch');
  eq('one array emptying the box clears the disagreement', p.mappings.length, 2);
  eq('one array emptying the box keeps the row edit', p.mappings[0],
    { labels: ['a'], agent: 'go', source: 'issue', model: 'default' });
  eq('one array emptying the box restores the order af wrote', p.mappings[1],
    { labels: ['b'], agent: 'python', source: 'pr' });
  h.m.refreshSettingsPanel('dispatch');
  ok('one array the panel is saveable again once the disagreement is gone',
    h.byId('set-save-dispatch').disabled === false);
}

{
  // The escape hatch has to return the DOCUMENT, not an empty one — and `mappings` cannot see that,
  // because the row controls rebuild that key either way. So it is asserted where the fallback is the
  // only thing standing between the operator and a wiped file: a panel with no curated control at all.
  const disk = { mode: 'beads', mailbox_root: '/x', retention_days: 30 };
  const h = mount({ messaging: view(JSON.parse(JSON.stringify(disk))) });
  h.byId('set-adv-messaging').value = '{"mode":"issues"}';
  h.byId('set-adv-messaging').value = '';
  eq('escape hatch emptying the box restores the document, it does not empty it',
    h.m.settingsPayload('messaging'), disk);
  eq('escape hatch a restored document has nothing to save', h.m.settingsDiff('messaging'), []);
}

{
  // The same, on a panel that does have curated controls: the keys no control owns must come back too.
  const h = mount({
    dispatch: view({ trigger_label: 't', recovery: { enabled: true }, mappings: [{ labels: ['a'], agent: 'go' }] }),
  });
  h.byId('set-adv-dispatch').value = '{"mappings":[]}';
  h.byId('set-adv-dispatch').value = '';
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'default';
  const p = h.m.settingsPayload('dispatch');
  eq('escape hatch the uncontrolled keys come back with the document', p.recovery, { enabled: true });
  eq('escape hatch the controlled keys come back too', p.trigger_label, 't');
  eq('escape hatch the row edit still lands', p.mappings[0].model, 'default');
}

{
  // Remove one route with ×, append another, and edit keys the rows do not own in the text. All three
  // counts are 2, so this is a merge — and the seed must be read at each row's ORIGINAL position.
  // Compacting the index instead grafts the DELETED route's `source` and unknown key onto the
  // BRAND-NEW route: the transplant class this phase exists to prevent, arriving from the other side.
  const disk = [{ labels: ['a'], agent: 'go', source: 'issue' }, { labels: ['b'], agent: 'python', source: 'pr' }];
  const typed = JSON.stringify({
    mappings: [
      { labels: ['a'], agent: 'go', source: 'issue', note: 'A' },
      { labels: ['b'], agent: 'python', source: 'pr', note: 'B' },
    ],
  });

  const last = mount({ dispatch: view({ mappings: JSON.parse(JSON.stringify(disk)) }) });
  last.byId('set-adv-dispatch').value = typed;
  last.vm.rows[1].removed = true;
  last.byId('mapRows').appendChild(last.m.settingsMapRow(null));
  last.vm.rows[2].node.querySelector('[data-role="label"]').value = 'c';
  last.vm.rows[2].node.querySelector('[data-role="agent"]').value = 'manager';
  const lp = last.m.settingsPayload('dispatch');
  eq('seed index removing the LAST row keeps the survivor whole', lp.mappings[0],
    { labels: ['a'], agent: 'go', source: 'issue', note: 'A' });
  eq('seed index the appended route is clean, not the deleted route wearing new labels',
    lp.mappings[1], { labels: ['c'], agent: 'manager' });

  const first = mount({ dispatch: view({ mappings: JSON.parse(JSON.stringify(disk)) }) });
  first.byId('set-adv-dispatch').value = typed;
  first.vm.rows[0].removed = true;
  first.byId('mapRows').appendChild(first.m.settingsMapRow(null));
  first.vm.rows[2].node.querySelector('[data-role="label"]').value = 'c';
  first.vm.rows[2].node.querySelector('[data-role="agent"]').value = 'manager';
  const fp = first.m.settingsPayload('dispatch');
  eq('seed index removing the FIRST row keeps the survivor whole', fp.mappings[0],
    { labels: ['b'], agent: 'python', source: 'pr', note: 'B' });
  eq('seed index the appended route is still clean', fp.mappings[1], { labels: ['c'], agent: 'manager' });
}

{
  // Deleting the same route in BOTH places is not a disagreement, whatever route the two editors took
  // to get there. Refusing here would be the console insisting on a conflict the operator can see is
  // not one.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }, { labels: ['b'], agent: 'python' }] }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [{ labels: ['b'], agent: 'python' }] });
  h.vm.rows[0].removed = true;
  const diff = h.m.settingsDiff('dispatch');
  ok('one array the two editors agreeing is not a conflict', Array.isArray(diff), JSON.stringify(diff));
  eq('one array the agreed deletion is what gets sent', h.m.settingsPayload('dispatch').mappings,
    [{ labels: ['b'], agent: 'python' }]);
}

{
  // Reordering ALONE is well defined — the rows have no opinion — so it must go through untouched.
  const disk = [{ labels: ['a'], agent: 'go', source: 'issue' }, { labels: ['b'], agent: 'python', source: 'pr' }];
  const h = mount({ dispatch: view({ mappings: JSON.parse(JSON.stringify(disk)) }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [disk[1], disk[0]] });
  eq('one array reordering alone stands', h.m.settingsPayload('dispatch').mappings, [disk[1], disk[0]]);
}

{
  // Retyping a LABEL in the text, with no row touched, is the same shape: the rows agree with the
  // file, so they have nothing to say, and the typed value must not be silently reverted.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go', source: 'issue' }] }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [{ labels: ['renamed'], agent: 'go', source: 'issue' }] });
  eq('one array a label retyped in the text alone is not reverted',
    h.m.settingsPayload('dispatch').mappings[0].labels, ['renamed']);
}

{
  // But retyping a label there while ALSO moving a control is two editors writing the same field.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go', source: 'issue' }] }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [{ labels: ['renamed'], agent: 'go', source: 'issue' }] });
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'default';
  ok('one array a label retyped in the text AND a row edit is refused',
    typeof h.m.settingsDiff('dispatch') === 'string', JSON.stringify(h.m.settingsDiff('dispatch')));
}

{
  // The agent and the model pin are row-owned too, so the text writing either of them while a row is
  // also edited is the same collision as a label — all three fields, or the check has a hole per field.
  for (const [field, val] of [['agent', 'manager'], ['model', 'sonnet-cheap']]) {
    const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go', source: 'issue' }] }) });
    const typed = { labels: ['a'], agent: 'go', source: 'issue' };
    typed[field] = val;
    h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [typed] });
    h.vm.rows[0].node.querySelector('[data-role="label"]').value = 'edited-in-the-row';
    ok('one array the text writing ' + field + ' while a row is edited is refused',
      typeof h.m.settingsDiff('dispatch') === 'string', JSON.stringify(h.m.settingsDiff('dispatch')));
  }
}

{
  // The identity check must not over-refuse: editing keys the rows do NOT own, across several routes,
  // while also touching a control, is exactly the merge the seed exists for.
  const h = mount({
    dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }, { labels: ['b'], agent: 'python' }] }),
  });
  h.byId('set-adv-dispatch').value = JSON.stringify({
    mappings: [{ labels: ['a'], agent: 'go', source: 'issue' }, { labels: ['b'], agent: 'python', source: 'pr' }],
  });
  h.vm.rows[1].node.querySelector('[data-role="model"]').value = 'default';
  const p = h.m.settingsPayload('dispatch');
  eq('one array raw edits to uncontrolled keys still merge', p.mappings[0], { labels: ['a'], agent: 'go', source: 'issue' });
  eq('one array the merge applies the control to the right route', p.mappings[1],
    { labels: ['b'], agent: 'python', source: 'pr', model: 'default' });
}

{
  // A raw deletion with the rows untouched IS well defined: the rows have no opinion, so it stands.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }, { labels: ['b'], agent: 'python' }] }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [{ labels: ['b'], agent: 'python' }] });
  eq('one array a raw deletion alone stands', h.m.settingsPayload('dispatch').mappings.length, 1);
}

{
  // And removing a route with the × button, with the raw box untouched, is well defined the other way.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }, { labels: ['b'], agent: 'python' }] }) });
  h.vm.rows[0].removed = true;
  const p = h.m.settingsPayload('dispatch');
  eq('one array removing a row alone stands', p.mappings.length, 1);
  eq('one array the surviving route is the right one', p.mappings[0], { labels: ['b'], agent: 'python' });
}

// ============ the two-editor rule, for every curated key and not just one ============

{
  const h = mount({ dispatch: view({ trigger_label: 'a' }) });
  h.byId('set-adv-dispatch').value = '{"trigger_label":"z"}';
  eq('two editors an untouched trigger box does not undo a raw trigger edit',
    h.m.settingsPayload('dispatch').trigger_label, 'z');
  h.byId('set-trigger').value = 'q';
  eq('two editors a moved trigger box wins over the raw text',
    h.m.settingsPayload('dispatch').trigger_label, 'q');
}

['quality', 'fidelity', 'improvement', 'telemetry'].forEach((g) => {
  const h = mount({ startup: view({}) });
  const doc = {}; doc[g] = 'off';
  h.byId('set-adv-startup').value = JSON.stringify(doc);
  eq('two editors an untouched ' + g + ' select does not undo a raw gate edit',
    h.m.settingsPayload('startup')[g], 'off');
  h.byId('set-gate-' + g).value = 'on';
  eq('two editors a moved ' + g + ' select wins over the raw text',
    h.m.settingsPayload('startup')[g], 'on');
});

{
  const h = mount({ statusline: view({ elements: ['dir'] }) });
  h.byId('set-adv-statusline').value = '{"elements":["dir","branch"]}';
  eq('two editors untouched element boxes do not undo a raw elements edit',
    h.m.settingsPayload('statusline').elements, ['dir', 'branch']);
}

// ============ the sentinel arms the happy paths never reach ============

{
  // None → All. The baseline is `[]`, which is a DIFFERENT third state from absent; collapsing the
  // two would leave an operator unable to get back to ALL, and the key stuck at [].
  const h = mount({ startup: view({ agents: [] }) });
  h.dom.doc.querySelectorAll('input[name="set-startup-agents-mode"]').forEach((r) => { r.checked = (r.value === 'all'); });
  const p = h.m.settingsPayload('startup');
  ok('sentinel None -> All removes the key, from an EMPTY-ARRAY baseline', p === null || !('agents' in p), JSON.stringify(p));
  eq('sentinel None -> All is named in the diff', h.m.settingsDiff('startup'), ['agents (removed)']);
}
{
  // The explicit-null arm, reached for real: the raw box says null, and the radio agrees with it
  // (null means ALL), so the key must be left exactly as the operator wrote it.
  const h = mount({ startup: view({ agents: ['manager'] }) });
  h.byId('set-adv-startup').value = '{"agents":null}';
  h.dom.doc.querySelectorAll('input[name="set-startup-agents-mode"]').forEach((r) => { r.checked = (r.value === 'all'); });
  const p = h.m.settingsPayload('startup');
  ok('sentinel an explicit null written by hand is not rewritten as absent', 'agents' in p, JSON.stringify(p));
  eq('sentinel that null is left as null', p.agents, null);
}
[true, false].forEach((stored) => {
  // "Not set" must REMOVE the key, in both directions. Absent means ON, so removing it is a real,
  // reachable instruction — not a synonym for "off".
  const h = mount({ statusline: view({ elements: ['dir'], color: stored }) });
  h.dom.doc.querySelectorAll('input[name="set-statusline-color"]').forEach((r) => { r.checked = (r.value === 'default'); });
  const p = h.m.settingsPayload('statusline');
  ok('sentinel colour ' + stored + ' -> Not set removes the key', !('color' in p), JSON.stringify(p));
  eq('sentinel colour ' + stored + ' -> Not set is named in the diff',
    h.m.settingsDiff('statusline'), ['color (removed)']);
});

{
  // A route the operator deliberately blanked is NOT the same as a route they never filled in: it was
  // on disk. It goes to af and comes back a 422, rather than disappearing from the file quietly.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go', source: 'pr' }] }) });
  const row = h.vm.rows[0].node;
  row.querySelector('[data-role="label"]').value = '';
  row.querySelector('[data-role="agent"]').value = '';
  const p = h.m.settingsPayload('dispatch');
  eq('a blanked existing route still reaches af', p.mappings.length, 1);
  eq('a blanked existing route arrives blank, for af to reject', p.mappings[0],
    { labels: [], agent: '', source: 'pr' });
}

{
  const h = mount({ statusline: view({ elements: ['dir'], color: true }) });
  h.byId('set-adv-statusline').value = '{"elements":["dir"],"color":null}';
  h.dom.doc.querySelectorAll('input[name="set-statusline-color"]').forEach((r) => { r.checked = (r.value === 'default'); });
  const p = h.m.settingsPayload('statusline');
  ok('sentinel an explicit null colour written by hand is kept', 'color' in p && p.color === null, JSON.stringify(p));
}
{
  // "Just these:" with nothing typed is an unfinished thought, not the None sentinel.
  const h = mount({ startup: view({ agents: ['manager'] }) });
  h.dom.doc.querySelectorAll('input[name="set-startup-agents-mode"]').forEach((r) => { r.checked = (r.value === 'list'); });
  h.byId('set-startup-agents').value = '';
  eq('sentinel List with an empty box does not write the None sentinel',
    h.m.settingsPayload('startup').agents, ['manager']);
  eq('sentinel List with an empty box reports no change', h.m.settingsDiff('startup'), []);
}

// ============ an existing document the operator empties is an edit, not an absence ============

{
  const h = mount({ messaging: view({ groups: { builders: ['go'] } }) });
  h.byId('set-adv-messaging').value = '{}';
  eq('emptying an existing document is an edit', h.m.settingsPayload('messaging'), {});
  eq('emptying an existing document shows in the diff', h.m.settingsDiff('messaging'), ['groups (removed)']);
  h.m.refreshSettingsPanel('messaging');
  ok('emptying an existing document leaves Save enabled', h.byId('set-save-messaging').disabled === false);
}
{
  // A hand-edited array-rooted document must not have members assigned onto it on the way past.
  const h = mount({ startup: view([]) });
  eq('an array-rooted document is passed through, not decorated', h.m.settingsPayload('startup'), []);
}

// ============ reloading discards every panel's unsaved work, so it says so first ============

{
  const h = mount({ dispatch: view({ trigger_label: 'a' }), startup: view({ agents: [] }) });
  await0(h.m.reloadSettings());
  eq('reload with nothing unsaved re-reads immediately', h.loads.length, 1);

  h.byId('set-trigger').value = 'b';
  h.m.refreshSettingsPanel('dispatch');
  await0(h.m.reloadSettings());
  eq('reload with unsaved edits does NOT re-read on the first click', h.loads.length, 1);
  ok('reload names the panel it would discard',
    h.byId('set-reload').textContent.indexOf('dispatch') >= 0, h.byId('set-reload').textContent);
  await0(h.m.reloadSettings());
  eq('reload re-reads on the second click', h.loads.length, 2);
}
function await0(_p) { /* the stubbed load resolves synchronously; nothing to await */ }

// ==================== the pre-save diff is the payload, not a second story ====================

{
  const doc = { trigger_label: 'a', mappings: [{ labels: ['x'], agent: 'go', model: 'default' }] };
  const h = mount({ dispatch: view(doc) });
  eq('diff a pristine panel has nothing to save', h.m.settingsDiff('dispatch'), []);
  ok('diff a pristine panel disables Save', h.byId('set-save-dispatch').disabled === true);

  h.byId('set-trigger').value = 'b';
  eq('diff names the changed top-level key', h.m.settingsDiff('dispatch'), ['trigger_label']);

  h.byId('set-trigger').value = 'a';
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'sonnet-cheap';
  eq('diff names a changed member of one mapping', h.m.settingsDiff('dispatch'), ['mappings[0].model']);
  h.m.refreshSettingsPanel('dispatch');
  ok('diff a dirty panel enables Save', h.byId('set-save-dispatch').disabled === false);
  ok('diff is shown to the operator',
    h.byId('set-diff-dispatch').children.some((c) => c.textContent.indexOf('mappings[0].model') >= 0));
}

{
  const h = mount({ messaging: view({ mode: 'beads' }) });
  h.byId('set-adv-messaging').value = '{not json';
  ok('advanced malformed JSON reports the reason rather than a bare failure',
    String(h.m.settingsDiff('messaging')).indexOf('not valid JSON') >= 0, h.m.settingsDiff('messaging'));
  ok('advanced malformed JSON disables Save', h.byId('set-save-messaging').disabled === true);

  h.byId('set-adv-messaging').value = '{"mode":"beads","poll_seconds":5}';
  eq('advanced valid JSON is applied', h.m.settingsPayload('messaging'), { mode: 'beads', poll_seconds: 5 });
  eq('advanced valid JSON diffs as an addition', h.m.settingsDiff('messaging'), ['poll_seconds (added)']);
}

// ==================== the skew banner reports only what it can know ====================

{
  const h = mount({ dispatch: view({}) }, { fingerprint: '' });
  const copy = JSON.stringify(h.byId('set-banner').querySelectorAll('p').map((p) => p.textContent));
  ok('skew a missing fingerprint is reported', copy.indexOf('did not report a config-schema fingerprint') >= 0, copy);
}
{
  const store = {};
  const h = mount({ dispatch: view({}) }, { fingerprint: 'abc', store });
  eq('skew a first read with a fingerprint shows no banner', h.byId('set-banner').children.length, 0);
  ok('skew the first fingerprint is remembered', store['af-settings-schema-fingerprint'] === 'abc');

  const same = mount({ dispatch: view({}) }, { fingerprint: 'abc', store });
  eq('skew an unchanged fingerprint shows no banner', same.byId('set-banner').children.length, 0);

  // af was upgraded under the open console.
  const changed = mount({ dispatch: view({}) }, { fingerprint: 'def', store });
  const copy = JSON.stringify(changed.byId('set-banner').querySelectorAll('p').map((p) => p.textContent));
  ok('skew a CHANGED fingerprint is reported', copy.indexOf('fingerprint changed between this read') >= 0, copy);

  // ...and the warning does not then stick around forever claiming a change already taken in.
  const after = mount({ dispatch: view({}) }, { fingerprint: 'def', store });
  eq('skew the warning clears once the change has been read', after.byId('set-banner').children.length, 0);
}

{
  // A route that arrived with no `agent` key must not GROW one just by being looked at. Materialising
  // `agent: ""` is the same harm as dropping a key, arriving from the other direction.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], source: 'issue' }] }) });
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'default';
  const p = h.m.settingsPayload('dispatch');
  ok('row an absent agent key is not materialised by editing the row',
    !('agent' in p.mappings[0]), JSON.stringify(p.mappings[0]));
  eq('row the rest of the route is untouched', p.mappings[0],
    { labels: ['a'], source: 'issue', model: 'default' });
}

{
  // The other arm, which the rule above must not break: a route the operator BLANKED had the key, so
  // it keeps it and goes to af for the 422 rather than quietly losing its agent.
  const h = mount({ dispatch: view({ mappings: [{ labels: ['a'], agent: 'go' }] }) });
  h.vm.rows[0].node.querySelector('[data-role="agent"]').value = '';
  const p = h.m.settingsPayload('dispatch');
  eq('row a blanked agent keeps its key and goes to af', p.mappings[0], { labels: ['a'], agent: '' });
}

{
  // A panel whose save is REFUSED is still dirty — reload would throw away the very edits that caused
  // the refusal, so it has to arm the confirm and name the panel like any other unsaved work.
  const disk = [{ labels: ['a'], agent: 'go', source: 'issue' }, { labels: ['b'], agent: 'python', source: 'pr' }];
  const h = mount({ dispatch: view({ mappings: JSON.parse(JSON.stringify(disk)) }) });
  h.byId('set-adv-dispatch').value = JSON.stringify({ mappings: [disk[1], disk[0]] });
  h.vm.rows[0].node.querySelector('[data-role="model"]').value = 'default';
  h.m.reloadSettings();
  eq('reload a refused panel still counts as unsaved work', h.byId('set-reload').getAttribute('data-armed'), '1');
  ok('reload the confirm names the refused panel', /dispatch/.test(h.byId('set-reload').textContent),
    h.byId('set-reload').textContent);
  eq('reload arming does not re-read', h.loads.length, 0);
}

{
  // "the console will not invent one" is a promise only a panel with a save path can make. factory is
  // read-only, so on a factory root without factory.json the sentence would describe a choice the
  // console never had.
  const h = mount({ factory: view(null, { writable: false }), dispatch: view({}) });
  ok('disposition a read-only absent file does not promise not to invent one',
    h.byId('set-when-factory').textContent.indexOf('will not invent') < 0,
    h.byId('set-when-factory').textContent);

  const w = mount({ startup: view(null) });
  ok('disposition a WRITABLE absent file does say so',
    w.byId('set-when-startup').textContent.indexOf('will not invent') >= 0,
    w.byId('set-when-startup').textContent);
}

// ==================== AC-5: one save writes one file, and stops on failure ====================

(async function () {
  {
    const h = mount({
      dispatch: view({ trigger_label: 'a' }, { fingerprint: 'sha256:dead' }),
      startup: view({ agents: ['manager'] }),
    });
    h.byId('set-trigger').value = 'b';
    await h.m.saveSettingsFile('dispatch');
    eq('AC-5 exactly one PUT is issued', h.puts.length, 1);
    eq('AC-5 the PUT names only the panel that was saved', h.puts[0].path, '/api/settings/dispatch');
    eq('AC-5 the read fingerprint travels as the write precondition',
      h.puts[0].extra['X-AF-If-Content-Hash'], 'sha256:dead');
    eq('AC-5 a successful write re-reads exactly once', h.loads.length, 1);
    ok('AC-5 a successful write reports success', h.byId('set-dispatch-ok').hidden === false);
  }

  {
    // The defect this phase exists to remove: a rejected first write followed by a second write.
    const h = mount({
      dispatch: view({ trigger_label: 'a' }),
      startup: view({ agents: ['manager'] }),
    }, { responses: [{ ok: false, _status: 422, message: 'mappings[0]: agent "ghost" is not in agents.json' }] });
    h.byId('set-trigger').value = 'b';
    await h.m.saveSettingsFile('dispatch');
    eq('AC-5 a rejected write issues no second PUT', h.puts.length, 1);
    ok('AC-5 no other file is written', h.puts.every((p) => p.path === '/api/settings/dispatch'));
    eq('AC-5 a rejected write does NOT re-read', h.loads.length, 0);
    ok('AC-5 af\'s own message is shown at that panel',
      h.byId('set-dispatch-err').textContent.indexOf('agent "ghost" is not in agents.json') >= 0,
      h.byId('set-dispatch-err').textContent);
    ok('AC-5 the failure is dismissible', h.byId('set-dismiss-dispatch').hidden === false);
    eq('AC-5 the operator\'s edit is still in the form', h.byId('set-trigger').value, 'b');
    eq('AC-5 the Save button is re-enabled after a failure', h.byId('set-save-dispatch').disabled, false);
  }

  {
    const h = mount({ dispatch: view({ trigger_label: 'a' }, { fingerprint: 'sha256:stale' }) },
      { responses: [{ ok: false, _status: 409, message: 'dispatch.json changed on disk since it was read' }] });
    h.byId('set-trigger').value = 'b';
    await h.m.saveSettingsFile('dispatch');
    const msg = h.byId('set-dispatch-err').textContent;
    ok('CAS a 409 says nothing was written', msg.indexOf('nothing was written') >= 0, msg);
    ok('CAS a 409 tells the operator to reload and re-apply', /reload/i.test(msg) && /re-apply/i.test(msg), msg);
    eq('CAS a 409 does not silently re-read over the operator\'s edit', h.loads.length, 0);
  }

  {
    const h = mount({ dispatch: view({ trigger_label: 'a' }) },
      { responses: [{ ok: false, _status: 502, message: 'exec: "af": executable file not found in $PATH' }] });
    h.byId('set-trigger').value = 'b';
    await h.m.saveSettingsFile('dispatch');
    ok('a 502 says af could not run', h.byId('set-dispatch-err').textContent.indexOf('af could not run') >= 0,
      h.byId('set-dispatch-err').textContent);
  }

  {
    // 400 and 422 are af's and the server's own words. The console adds nothing to them, because
    // anything it added would be a second opinion about a document it did not validate.
    const cases = [
      [400, 'settings: request body is empty'],
      [422, 'startup.json: unknown field "quantity"'],
    ];
    for (const [status, message] of cases) {
      const h = mount({ dispatch: view({ trigger_label: 'a' }) }, { responses: [{ ok: false, _status: status, message }] });
      h.byId('set-trigger').value = 'b';
      await h.m.saveSettingsFile('dispatch');
      eq('a ' + status + " shows af's message verbatim", h.byId('set-dispatch-err').textContent, message);
      eq('a ' + status + ' does not re-read', h.loads.length, 0);
      eq('a ' + status + ' issues exactly one PUT', h.puts.length, 1);
    }
  }

  {
    // An absent file with no fingerprint sends no precondition at all: an unparseable one is a 400.
    const h = mount({ startup: view(null) });
    h.byId('set-adv-startup').value = '{"start_dispatch":true}';
    await h.m.saveSettingsFile('startup');
    eq('CAS an absent file is written without a precondition', Object.keys(h.puts[0].extra), []);
    eq('CAS an authored document is what gets written', h.puts[0].body, { start_dispatch: true });
  }

  {
    // Nothing is ever PUT for a file the operator has not authored — the deleted defaultStartup()
    // invariant, enforced from the client side too.
    const h = mount({ startup: view(null) });
    await h.m.saveSettingsFile('startup');
    eq('an absent file nobody authored is never PUT', h.puts.length, 0);
    ok('an absent file nobody authored explains why', h.byId('set-startup-err').textContent.indexOf('Nothing to save') >= 0,
      h.byId('set-startup-err').textContent);
  }

  {
    const h = mount({ messaging: view({ mode: 'beads' }) });
    h.byId('set-adv-messaging').value = '{not json';
    await h.m.saveSettingsFile('messaging');
    eq('malformed JSON sends nothing', h.puts.length, 0);
    ok('malformed JSON explains itself inline',
      h.byId('set-messaging-err').textContent.indexOf('not valid JSON') >= 0,
      h.byId('set-messaging-err').textContent);
  }

  {
    // ---- the READ path, driven through the shipped SettingsViewModel ----
    const dom = buildSettingsDom();
    const files = { dispatch: view({ trigger_label: 'a', mappings: [{ labels: ['x'], agent: 'go' }] }) };
    const gets = [];
    const toasts = [];
    const api = {
      get(p) {
        gets.push(p);
        return Promise.resolve({ ok: true, data: { files, agents: AGENTS, profiles: PROFILES, schema_fingerprint: 'abc' } });
      },
    };
    const win = { sessionStorage: { getItem() { return null; }, setItem() {} } };
    const vm = loadFactory(dom.doc.getElementById.bind(dom.doc), dom.doc, win, api,
      (msg) => toasts.push(String(msg))).vm;

    await vm.load();
    eq('read the settings document is fetched once', gets, ['/api/settings']);
    eq('read the profiles reach the model picker', vm.profiles, PROFILES);
    eq('read every baseline is frozen as TEXT', typeof vm.baselines.dispatch, 'string');
    eq('read the baseline is the document as read', JSON.parse(vm.baselines.dispatch), files.dispatch.doc);

    // The rows hold references INTO the retained document. A baseline kept by reference drifts along
    // with them, every diff comes out empty, and the console shows "No unsaved changes" over a panel
    // full of them.
    files.dispatch.doc.trigger_label = 'edited through the retained object';
    eq('read the baseline does not drift with the document it was taken from',
      JSON.parse(vm.baselines.dispatch).trigger_label, 'a');

    // Every successful save re-reads, so the read path runs many times over one open console. Rows
    // that accumulate instead of being replaced would make the NEXT save write every route twice.
    await vm.load();
    eq('read a second read replaces the rows rather than appending', vm.rows.length, 1);
    eq('read a second read leaves one row in the DOM too',
      dom.doc.getElementById('mapRows').children.length, 1);

    // A refused read must not leave a half-populated model behind for a save to build a payload from.
    const before = JSON.stringify(vm.data);
    api.get = () => Promise.resolve({ ok: false, message: 'settings read failed' });
    await vm.load();
    eq('read a refused read says so', toasts, ['settings read failed']);
    eq('read a refused read changes nothing', JSON.stringify(vm.data), before);
  }

  // ==================== AC-6: af normalizes, and the re-read shows what af wrote ====================
  // The one seam nothing else in this file reaches. Every other save here runs against mount()'s
  // view-model stub, whose load() only counts (it does not fetch, re-snapshot or re-render), so after
  // any of them the DOM is by construction still pre-save; and the read-path block above never PUTs.
  // Save and re-read have been exercised in disjoint harnesses, which leaves the operator-facing
  // question — what do I see after af rewrote my document? — unasserted.
  //
  // The behaviour under test is AC-6's fourth clause, "the client still reflects af's normalization
  // after a successful save". af-core rewrites a lone `label` into a one-element `labels`
  // (internal/config/dispatch.go:173-176; carrying BOTH is rejected as ambiguous at :170-172) and
  // materializes its own defaults (:183-201). So the API double here does what af does to the
  // document it is handed, and then serves THAT back — echoing the payload would make this block pass
  // while proving nothing.
  {
    const dom = buildSettingsDom();
    const byId = dom.doc.getElementById.bind(dom.doc);
    // af-legal as read: non-empty repos, non-empty trigger_label, at least one mapping
    // (internal/config/dispatch.go:158-167). Row 0 is the row the operator edits. Row 1 is an
    // untouched lone-`label` row — the client deliberately leaves that form alone, so af is the only
    // thing that can rewrite it, which is what makes the re-read the only place it becomes visible.
    let doc = {
      repos: ['owner/repo'],
      trigger_label: 'dispatch-me',
      mappings: [{ label: 'x', agent: 'go' }, { label: 'ops', agent: 'python' }],
    };
    const gets = [];
    const puts = [];
    const toasts = [];
    const api = {
      get(p) {
        gets.push(p);
        return Promise.resolve({ ok: true, data: { files: { dispatch: view(doc) }, agents: AGENTS, profiles: PROFILES, schema_fingerprint: 'abc' } });
      },
      put(p, body) {
        puts.push({ path: p, body: JSON.parse(JSON.stringify(body)) });
        doc = JSON.parse(JSON.stringify(body));
        doc.mappings = doc.mappings.map(function (m) {
          const stored = Object.assign({}, m);
          if (stored.label && !stored.labels) { stored.labels = [stored.label]; delete stored.label; }
          if (!stored.source) { stored.source = 'issue'; }
          return stored;
        });
        doc.notify_on_complete = 'manager';
        doc.interval_seconds = 300;
        doc.retry_after_seconds = 1800;
        doc.remove_trigger_after_dispatch = false;
        return Promise.resolve({ ok: true, _status: 200 });
      },
    };
    const win = { sessionStorage: { getItem() { return null; }, setItem() {} } };
    const vm = loadFactory(byId, dom.doc, win, api, (msg) => toasts.push(String(msg))).vm;

    await vm.load();
    eq('AC-6 a lone label renders as the label it is',
      vm.rows[0].node.querySelector('[data-role="label"]').value, 'x');

    // The missing space is load-bearing. The operator types 'x,y', splitList trims it, af stores
    // ["x","y"], and the re-read renders labels.join(', ') — so 'x, y' in the box is only reachable
    // by re-rendering from af's document. Typing the canonical spacing would make the assertion
    // below true whether the panel was re-rendered or left exactly as the operator left it.
    vm.rows[0].node.querySelector('[data-role="label"]').value = 'x,y';
    await vm.save('dispatch');

    eq('AC-6 the edited row is written in labels form', puts[0].body.mappings[0].labels, ['x', 'y']);
    ok('AC-6 the edited row carries no lone label', !('label' in puts[0].body.mappings[0]));
    eq('AC-6 the untouched row is written in the form it arrived in', puts[0].body.mappings[1].label, 'ops');
    ok('AC-6 the untouched row grows no labels key', !('labels' in puts[0].body.mappings[1]));
    eq('AC-6 a successful save re-reads exactly once', gets, ['/api/settings', '/api/settings']);

    eq('AC-6 the panel shows what af wrote, not what the operator typed',
      vm.rows[0].node.querySelector('[data-role="label"]').value, 'x, y');
    const stored = JSON.parse(vm.baselines.dispatch);
    eq('AC-6 the row af rewrote comes back in labels form', stored.mappings[1].labels, ['ops']);
    ok('AC-6 the rewritten row no longer carries a lone label', !('label' in stored.mappings[1]));
    eq('AC-6 af’s frozen source default is visible in the re-read', stored.mappings[0].source, 'issue');
    eq('AC-6 af’s frozen interval default is visible in the re-read', stored.interval_seconds, 300);
    const advanced = JSON.parse(byId('set-adv-dispatch').value);
    ok('AC-6 the advanced editor shows af’s document too',
      !('label' in advanced.mappings[1]) && advanced.mappings[1].labels.length === 1);
    eq('AC-6 the re-read replaces the rows rather than appending', vm.rows.length, 2);
    ok('AC-6 the save reports success', byId('set-dispatch-ok').hidden === false);

    // A baseline left at the pre-save document would keep every member af normalized in the diff
    // forever: the console would report unsaved changes over a file it had just written, and the
    // next save would re-send them.
    ok('AC-6 the panel is pristine after the save', byId('set-save-dispatch').disabled === true);
    ok('AC-6 the diff reports nothing left to save',
      byId('set-diff-dispatch').children.some((c) => c.textContent.indexOf('No unsaved changes') >= 0));
    eq('AC-6 a save that succeeded says nothing in a toast', toasts, []);
  }

  // ==================== TestSettingsSave_PreservesUndisplayedConfig: AC-1, end to end ====================
  // AC-1's named evidence (design-doc.md:29): seed workflows + a per-row model pin + a tuned
  // recovery block, edit one unrelated field, and assert every other member is returned. It fails
  // if the per-row merge is replaced with a rebuild — a rebuild emits only the members the row's
  // three inputs know about, so `model` and the whole `recovery`/`workflows` pair vanish from the
  // payload while every Go test stays green.
  //
  // This is the flow-level half of the AC. Its byte-level half is
  // 'M1 the payload differs from the document as read in exactly the edited member' (see the M1
  // section above), which diffs the entire payload against the document as read rather than
  // naming members one at a time; and its re-read half is the label→labels case below.
  {
    const doc = {
      trigger_label: 'a',
      recovery: { enabled: true, breaker_threshold: 3, cooldown_seconds: 900 },
      workflows: { nightly: { formula: 'x' } },
      mappings: [{ label: 'x', source: 'issue', agent: 'go', model: 'default' }],
    };
    const h = mount({ dispatch: view(doc) });
    h.byId('set-trigger').value = 'b';
    await h.m.saveSettingsFile('dispatch');
    const sent = h.puts[0].body;
    eq('TestSettingsSave_PreservesUndisplayedConfig the recovery block is returned intact', sent.recovery, doc.recovery);
    eq('TestSettingsSave_PreservesUndisplayedConfig the workflows block is returned intact', sent.workflows, { nightly: { formula: 'x' } });
    eq('TestSettingsSave_PreservesUndisplayedConfig the untouched mapping is returned intact', sent.mappings[0],
      { label: 'x', source: 'issue', agent: 'go', model: 'default' });
    eq('TestSettingsSave_PreservesUndisplayedConfig the edited field is the only change', sent.trigger_label, 'b');
  }

  console.log((fail === 0 ? 'PASS' : 'FAIL') + ': settings client behaviour — ' + pass + ' passed, ' + fail + ' failed');
  process.exit(fail === 0 ? 0 : 1);
}());
