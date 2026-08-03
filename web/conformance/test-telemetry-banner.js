// Behavioural proof of the shipped telemetry banner's backend axis, runnable by any
// reviewer and by CI:
//
//   node web/conformance/test-telemetry-banner.js
//   make conformance                       # same command, local<->CI parity
//
// It drives the SHIPPED renderTelemetryBanner (the same bytes served to the browser and
// embedded via //go:embed static) with real status DTOs and asserts the LINES IT EMITS —
// not the shape of its source.
//
// Why this exists (PR #585, threads T1/S1 and T2/S3). The Go-side checks in
// web/internal/web/telemetry_test.go are source scans, and a source scan cannot see
// reachability: a predicate can be structurally perfect and still never be true. Mutations
// that left the whole Go suite green while making the collapsed line unreachable forever:
//
//     var allRefused = (…).length > 0 && (…).every(fn) && false;   // trailing operand
//     var allRefused = 0 && (…).length > 0 && (…).every(fn);       // leading operand
//     var allRefused = (…).length > 99999 && (…).every(fn);        // unsatisfiable bound
//     var backendDownNext = !loopback ? …                          // inverted gate
//     return true || host === '127.0.0.1' || …                     // predicate pinned open
//
// Every one of them changes what the operator SEES, so every one of them fails here. This
// is the "drive renderTelemetryBanner with a three-signal DTO and assert one line out"
// option the review named; the structural checks stay as the cheap first line of defence.
'use strict';
const fs = require('fs');
const path = require('path');

const APP = path.join(__dirname, '..', 'internal', 'web', 'static', 'app.js');
const src = fs.readFileSync(APP, 'utf8');

let pass = 0, fail = 0;
function ok(name, cond, extra) {
  if (cond) { pass++; } else { fail++; console.log('FAIL', name, extra === undefined ? '' : extra); }
}

// --- extract the shipped function ------------------------------------------------------

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

// The renderer's only outputs are telLine() calls; its only DOM needs are byId and el.
// Stubbing exactly those three keeps the function under test unmodified.
function runBanner(vm) {
  const lines = [];
  // `children` is modelled because the renderer counts what the axes emitted to decide the
  // healthy-empty verdict — a stub without it would take a branch the browser never takes.
  const host = { innerHTML: '', children: [], appendChild(node) { this.children.push(node); } };
  const telLine = (h, copy, next, kind) => {
    lines.push({ copy: String(copy), next: String(next === undefined ? '' : next), kind: kind || '' });
    host.children.push({ line: lines.length });
  };
  const byId = () => host;
  const el = () => ({ appendChild() {}, set textContent(_) {}, style: {} });
  // eslint-disable-next-line no-new-func
  const factory = new Function('telLine', 'byId', 'el', funcBody('renderTelemetryBanner') + '; return renderTelemetryBanner;');
  factory(telLine, byId, el)(vm);
  return lines;
}

// --- fixtures --------------------------------------------------------------------------

const summaries = [
  'step timings: unreachable (dial tcp 127.0.0.1:5080: connect: connection refused)',
  'token usage: unreachable (lookup otel.example: no such host)',
  'session metrics: unreachable (net/http: request canceled (Client.Timeout exceeded))',
];

function statusWith(endpoint, signals) {
  return {
    state: 'ok',
    installed: { present: true, valid: true, endpoint: endpoint, header_count: 1 },
    recording: { enabled: true },
    backend: { probed: true, signals: signals },
    unprobed_cause: '',
  };
}
const allRefusedSignals = summaries.map((s) => ({ label: s.split(':')[0], ok: false, status: 0, summary: s }));
const mixedSignals = [
  { label: 'step timings', ok: false, status: 0, summary: summaries[0] },
  { label: 'token usage', ok: false, status: 404, summary: 'token usage: reachable but the address is not served (HTTP 404) — data sent here is discarded' },
  { label: 'session metrics', ok: true, status: 200, summary: 'session metrics: reachable (HTTP 200)' },
];

const LOOPBACK = 'http://127.0.0.1:5080/api/default';
const REMOTE = 'https://otel.your-company.example/api/default';

// --- T2/S3: the collapse must actually happen ------------------------------------------

{
  const lines = runBanner({ status: statusWith(LOOPBACK, allRefusedSignals), statusFail: '', usage: null, usageFail: '' });
  const backendLines = lines.filter((l) => /unreachable|not served|refused the data|credential was rejected/.test(l.copy));
  ok('all-refused collapses to exactly one backend line', backendLines.length === 1,
    'got ' + backendLines.length + ' -> ' + JSON.stringify(backendLines.map((l) => l.copy)));

  const copy = backendLines.length === 1 ? backendLines[0].copy : '';
  // T2/S1 cont'd: every cause survives; none is invented.
  summaries.forEach((s, i) => {
    const cause = s.slice(s.indexOf('(') + 1, s.lastIndexOf(')'));
    ok('collapsed line carries cause ' + i, copy.indexOf(cause) >= 0, 'missing ' + JSON.stringify(cause) + ' in ' + JSON.stringify(copy));
  });
  ok('collapsed line does not assert a cause it never measured',
    copy.indexOf('got connection refused') < 0, copy);
  ok('collapsed line does not hardcode the signal count', copy.indexOf('all three signals') < 0, copy);
}

// A fourth address must not make the line claim three.
{
  const four = allRefusedSignals.concat([{ label: 'traces', ok: false, status: 0, summary: 'traces: unreachable (i/o timeout)' }]);
  const lines = runBanner({ status: statusWith(LOOPBACK, four), statusFail: '', usage: null, usageFail: '' });
  const copy = (lines.find((l) => /unreachable/.test(l.copy)) || { copy: '' }).copy;
  ok('four addresses do not render as "three"', copy.indexOf('three') < 0, copy);
  ok('four addresses still collapse to one line',
    lines.filter((l) => /unreachable/.test(l.copy)).length === 1, JSON.stringify(lines.map((l) => l.copy)));
}

// The collapse must not depend on WHICH endpoint is configured. Binding it to the loopback
// predicate (`var allRefused = loopback && …`) makes the collapsed line unreachable for every
// remote endpoint while both suites stay green — so the all-refused case is exercised against
// a remote endpoint too, not just a local one.
{
  const lines = runBanner({ status: statusWith(REMOTE, allRefusedSignals), statusFail: '', usage: null, usageFail: '' });
  const backendLines = lines.filter((l) => /unreachable/.test(l.copy));
  ok('all-refused collapses on a REMOTE endpoint too', backendLines.length === 1,
    'got ' + backendLines.length + ' -> ' + JSON.stringify(backendLines.map((l) => l.copy)));
  const copy = backendLines.length === 1 ? backendLines[0].copy : '';
  ok('remote collapsed line still carries the payload causes', copy.indexOf('no such host') >= 0, copy);
}

// DO-NOT-CHANGE: a MIXED combination must stay per-signal — the collapse is only for the
// all-refused case, and swallowing a 404 behind one line loses the state the probe told apart.
{
  const lines = runBanner({ status: statusWith(LOOPBACK, mixedSignals), statusFail: '', usage: null, usageFail: '' });
  const emitted = lines.map((l) => l.copy);
  ok('mixed status renders per-signal, not collapsed',
    emitted.some((c) => c.indexOf('not served') >= 0) && emitted.some((c) => c.indexOf('connection refused') >= 0),
    JSON.stringify(emitted));
  ok('mixed status does not emit the collapsed sentence',
    !emitted.some((c) => c.indexOf('every address failed') >= 0), JSON.stringify(emitted));
}

// --- T1/S1: the recovery promise is gated on what the guard actually does ---------------

function promiseFor(endpoint) {
  const lines = runBanner({ status: statusWith(endpoint, allRefusedSignals), statusFail: '', usage: null, usageFail: '' });
  const l = lines.find((x) => /unreachable/.test(x.copy));
  return l ? l.next : '';
}

{
  const local = promiseFor(LOOPBACK);
  ok('loopback endpoint IS told about the automatic relaunch', /relaunch/i.test(local), local);
  ok('loopback endpoint keeps the login-shell fallback', /bash -l/.test(local), local);

  const remote = promiseFor(REMOTE);
  ok('remote endpoint is NOT promised an automatic relaunch', !/relaunches it/i.test(remote), remote);
  ok('remote endpoint is not told to start a local backend', !/bash -l would|start a login shell \(bash -l\) as a manual fallback/.test(remote) || /would start that local backend/.test(remote), remote);
}

// The predicate decides which sentence renders, so its VALUE is load-bearing. A predicate
// pinned open (`return true || …`) or loosened back to a substring test restores the false
// promise for exactly the endpoints that cannot receive it.
[
  ['http://127.0.0.1:5080/api/default', true],
  ['http://localhost:5080/api/default', true],
  ['http://[::1]:5080/api/default', true],
  ['https://otel.your-company.example/api/default', false],
  ['https://localhost.evil.com/api/default', false],
  ['https://x.example/api/default?redir=//localhost', false],
].forEach(([endpoint, wantLoopback]) => {
  const next = promiseFor(endpoint);
  const promises = /relaunches it/i.test(next);
  ok('promise matches loopback-ness for ' + endpoint, promises === wantLoopback,
    'promised=' + promises + ' want=' + wantLoopback + ' :: ' + next);
});

// --- report ----------------------------------------------------------------------------

console.log((fail === 0 ? 'PASS' : 'FAIL') + ': telemetry banner behaviour — ' + pass + ' passed, ' + fail + ' failed');
process.exit(fail === 0 ? 0 : 1);
