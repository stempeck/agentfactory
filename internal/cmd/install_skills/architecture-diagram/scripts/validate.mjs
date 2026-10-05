#!/usr/bin/env node
import { readFileSync, existsSync, readdirSync, statSync } from 'node:fs';
import { join, resolve, relative } from 'node:path';
import { createHash } from 'node:crypto';
import { isDeepStrictEqual } from 'node:util';
import { loadMermaid } from './lib/deps.mjs';
import { checkModel } from './lib/check.mjs';
import { renderAll } from './lib/render.mjs';
import { MERMAID_VERSION, MERMAID_SRI, MERMAID_CDN } from './lib/common.mjs';

const HELP = `usage:
  node validate.mjs <design-dir>/architecture-diagrams   Gate 4: validate everything render.mjs wrote
  node validate.mjs --self-test                            prove the validator rejects known-bad input

Installs pinned mermaid ${MERMAID_VERSION} and jsdom into \${XDG_CACHE_HOME:-~/.cache}/architecture-diagram/deps
on first use (network once). Prints "[n/N] <check> PASS|FAIL" on stderr and JSON on stdout; parser
messages are quoted verbatim. Exit codes: 0 pass; 1 a check failed; 2 usage error; 3 environment
error (node version, npm install).`;

const FLOW_TYPES = /^\s*(flowchart|sequenceDiagram)\b/m;
const BANNED = /^\s*(C4Context|C4Container|C4Component|C4Dynamic|C4Deployment|architecture-beta|block-beta|graph)\b/m;

function frontmatterOf(text) {
  const m = /^---\n([\s\S]*?)\n---\n/.exec(text);
  return m ? m[1] : null;
}

// Defects mermaid.parse accepts: callback clicks, deprecated directives, dropped top-level keys, bidirectional edges.
export function lint(text, variant) {
  const errs = [];
  const fm = frontmatterOf(text);
  const body = fm == null ? text : text.slice(text.indexOf('\n---\n', 4) + 5);
  if (variant === 'canonical') {
    if (!fm) errs.push('canonical source has no frontmatter');
    else if (/^(layout|look|theme|themeVariables):/m.test(fm)) errs.push('layout/look/theme at the top level of frontmatter are silently dropped; nest them under config:');
    if (!/^\s*accTitle: /m.test(text) || !/^\s*accDescr: /m.test(text)) errs.push('accTitle and accDescr are required');
  } else if (fm || /accTitle|accDescr|^\s*click /m.test(text)) errs.push('the Miro variant carries no frontmatter, accTitle/accDescr or click lines');
  if (/%%\{/.test(text)) errs.push('%%{init}%% directives are deprecated; use frontmatter config');
  if (/^\s*click \S+ (call|callback)\b/m.test(text) || /^\s*click \S+ [A-Za-z_$][\w$]*\s*$/m.test(text)) errs.push('callback click handlers are not allowed; use "#view=…" links');
  if (/<-->|o--o|x--x/.test(text)) errs.push('bidirectional edges are not allowed');
  if (BANNED.test(body)) errs.push('diagram type is not allowed (C4*, architecture-beta, block-beta, graph)');
  if (!FLOW_TYPES.test(body)) errs.push('only flowchart and sequenceDiagram are produced');
  if (/^\s*flowchart/m.test(body) && !/^\s*legend\[/m.test(body)) errs.push('flowchart has no legend node');
  if (/^\s*sequenceDiagram/m.test(body) && !/^\s*Note over [^:]+: Key/m.test(body)) errs.push('sequence diagram has no "Note over …: Key" legend');
  return errs;
}

async function structure(mermaid, text, view) {
  const errs = [];
  const d = await mermaid.mermaidAPI.getDiagramFromText(text);
  if (view.type === 'dynamic') {
    const actors = [...d.db.getActors().keys()];
    const expected = view.nodes.map(n => n.id);
    if (!isDeepStrictEqual(actors, expected)) errs.push(`participants ${JSON.stringify(actors)} != model ${JSON.stringify(expected)}`);
    const LT = d.db.LINETYPE || {};
    const skip = new Set([LT.NOTE ?? 2, LT.AUTONUMBER ?? 26]);
    const msgs = d.db.getMessages().filter(m => m.from && m.to && !skip.has(m.type));
    if (msgs.length !== view.steps.length) errs.push(`${msgs.length} messages != ${view.steps.length} model steps`);
    return errs;
  }
  const v = d.db.getVertices();
  const vertices = new Set(v instanceof Map ? v.keys() : Object.keys(v));
  const subgraphs = new Set(d.db.getSubGraphs().map(s => s.id));
  const nodes = new Set(view.nodes.map(n => n.id));
  for (const id of vertices) if (!nodes.has(id) && id !== 'legend' && !subgraphs.has(id)) errs.push(`implicit node "${id}" is not in the model (typo in an edge?)`);
  for (const id of nodes) if (!vertices.has(id)) errs.push(`model node "${id}" missing from the diagram`);
  const edges = d.db.getEdges();
  if (edges.length !== view.edges.length) errs.push(`${edges.length} edges != ${view.edges.length} model edges`);
  for (const e of edges) if (!String(e.text || '').trim()) errs.push(`edge ${e.start} -> ${e.end} has no label`);
  return errs;
}

const SELF_TEST = [
  { name: 'valid flowchart', variant: 'canonical', ok: true, text: '---\ntitle: "T"\nconfig:\n  layout: elk\n  look: classic\n  theme: base\n---\nflowchart TB\n  accTitle: T\n  accDescr: d\n  a["A"]\n  b["B"]\n  a -->|"calls"| b\n  legend["Key"]\n' },
  { name: 'syntax error', variant: 'canonical', ok: false, text: '---\ntitle: "T"\nconfig:\n  layout: elk\n---\nflowchart TB\n  accTitle: T\n  accDescr: d\n  a["A" --> b\n  legend["Key"]\n' },
  { name: 'top-level frontmatter key', variant: 'canonical', ok: false, text: '---\ntitle: "T"\nlayout: elk\n---\nflowchart TB\n  accTitle: T\n  accDescr: d\n  a["A"]\n  legend["Key"]\n' },
  { name: 'callback click', variant: 'canonical', ok: false, text: '---\ntitle: "T"\nconfig:\n  layout: elk\n---\nflowchart TB\n  accTitle: T\n  accDescr: d\n  a["A"]\n  legend["Key"]\n  click a call doIt()\n' },
  { name: 'banned type', variant: 'canonical', ok: false, text: '---\ntitle: "T"\nconfig:\n  layout: elk\n---\nC4Context\n  accTitle: T\n  accDescr: d\n' },
  { name: 'implicit node from typo', variant: 'structure', ok: false, text: 'flowchart TB\n  a["A"]\n  b["B"]\n  a -->|"calls"| bb\n  legend["Key"]\n', view: { type: 'context', nodes: [{ id: 'a' }, { id: 'b' }], edges: [{}] } },
  { name: 'raw semicolon in sequence message', variant: 'parse', ok: false, text: 'sequenceDiagram\n  a->>b: x; y\n' },
];

async function selfTest(mermaid) {
  const errs = [];
  for (const c of SELF_TEST) {
    let problems = [];
    try {
      await mermaid.parse(c.text);
      if (c.variant === 'structure') problems = await structure(mermaid, c.text, c.view);
      else if (c.variant !== 'parse') problems = lint(c.text, c.variant);
    } catch (err) { problems = [String(err.message || err)]; }
    if (c.ok && problems.length) errs.push(`self-test "${c.name}" should pass: ${problems.join('; ')}`);
    if (!c.ok && !problems.length) errs.push(`self-test "${c.name}" should fail but passed`);
  }
  return errs;
}

function walk(dir) {
  return readdirSync(dir).flatMap(n => { const p = join(dir, n); return statSync(p).isDirectory() ? walk(p) : [p]; });
}

async function main() {
  const args = process.argv.slice(2);
  if (!args.length || args[0] === '--help') { process.stdout.write(HELP + '\n'); process.exit(args.length ? 0 : 2); }
  const checks = [];
  const run = async (name, fn) => {
    let errors;
    try { errors = await fn(); } catch (err) { errors = [`check crashed: ${err.stack || err}`]; }
    checks.push({ n: checks.length + 1, name, pass: errors.length === 0, errors: errors.slice(0, 50), error_count: errors.length });
  };
  const { mermaid, version, minPath } = await loadMermaid();

  await run('validator engine and self-test', async () => {
    const errs = [];
    if (version !== MERMAID_VERSION) errs.push(`installed mermaid ${version} != pinned ${MERMAID_VERSION}`);
    const sri = 'sha384-' + createHash('sha384').update(readFileSync(minPath)).digest('base64');
    if (sri !== MERMAID_SRI) errs.push(`installed mermaid.min.js hash ${sri} != pinned ${MERMAID_SRI}`);
    return [...errs, ...(await selfTest(mermaid))];
  });
  if (args[0] === '--self-test') return report(checks);

  const out = resolve(args[0]);
  const modelPath = join(out, 'model.json');
  if (!existsSync(modelPath)) { process.stderr.write(`no model.json in ${out}\n`); process.exit(2); }
  const model = JSON.parse(readFileSync(modelPath, 'utf8'));
  const { views, sources } = renderAll(model);
  const files = walk(out).map(f => relative(out, f));

  await run('model gate still passes (design doc unchanged)', () => {
    const r = checkModel(model);
    return r.checks.filter(c => !c.pass).map(c => `${c.name}: ${c.errors[0]}${c.error_count > 1 ? ` (+${c.error_count - 1} more)` : ''}`);
  });
  await run('file set matches the produced views; no .md files', () => {
    const expected = new Set(['model.json', 'index.html', ...sources.map(s => join(s.variant === 'canonical' ? 'views' : 'miro', `${s.view}.mmd`))]);
    const errs = [];
    for (const f of files) if (f.endsWith('.md')) errs.push(`${f}: .md output is forbidden`); else if (!expected.has(f)) errs.push(`${f}: not produced by render.mjs`);
    for (const f of expected) if (!files.includes(f)) errs.push(`${f}: missing; run render.mjs`);
    return errs;
  });
  await run('rendered files equal a fresh render of model.json (no hand edits)', () => {
    const errs = [];
    for (const s of sources) {
      const f = join(out, s.variant === 'canonical' ? 'views' : 'miro', `${s.view}.mmd`);
      if (existsSync(f) && readFileSync(f, 'utf8') !== s.text) errs.push(`${relative(out, f)} differs from a fresh render; repair model.json and re-run render.mjs`);
    }
    return errs;
  });
  await run('every source parses with the pinned Mermaid', async () => {
    const errs = [];
    for (const s of sources) {
      try { await mermaid.parse(s.text); } catch (err) { errs.push(`${s.view} (${s.variant}): ${String(err.message || err).split('\n').slice(0, 4).join(' | ')}`); }
    }
    return errs;
  });
  await run('frontmatter config takes effect (elk, classic, base, SVG text labels)', async () => {
    const errs = [];
    for (const s of sources.filter(s => s.variant === 'canonical')) {
      try {
        const r = await mermaid.parse(s.text);
        const c = r.config || {};
        if (c.layout !== 'elk' || c.look !== 'classic' || c.theme !== 'base' || c.htmlLabels !== false) errs.push(`${s.view}: parsed config ${JSON.stringify({ layout: c.layout, look: c.look, theme: c.theme, htmlLabels: c.htmlLabels })}`);
      } catch { /* reported by the parse check */ }
    }
    return errs;
  });
  await run('lint: legend, accessibility, links, diagram types', () => sources.flatMap(s => lint(s.text, s.variant).map(e => `${s.view} (${s.variant}): ${e}`)));
  await run('parsed diagrams contain exactly the model elements and relationships', async () => {
    const errs = [];
    for (const s of sources) {
      const view = views.find(v => v.id === s.view);
      try { errs.push(...(await structure(mermaid, s.text, view)).map(e => `${s.view} (${s.variant}): ${e}`)); } catch { /* reported by the parse check */ }
    }
    return errs;
  });
  await run('index.html: pinned Mermaid with integrity, embedded model and sources match', () => {
    const errs = [];
    const page = readFileSync(join(out, 'index.html'), 'utf8');
    if (!page.includes(`<script src="${MERMAID_CDN}" integrity="${MERMAID_SRI}" crossorigin="anonymous"`)) errs.push('Mermaid script tag is missing the pinned URL, integrity or crossorigin');
    const embedded = /<script type="application\/json" id="architecture-model">([\s\S]*?)<\/script>/.exec(page);
    if (!embedded) errs.push('embedded model missing');
    else if (!isDeepStrictEqual(JSON.parse(embedded[1]), model)) errs.push('embedded model differs from model.json');
    for (const s of sources) {
      const re = new RegExp(`<script type="text/plain" data-view="${s.view}" data-variant="${s.variant}">\\n([\\s\\S]*?)</script>`);
      const m = re.exec(page);
      if (!m || m[1] !== s.text) errs.push(`embedded ${s.view} ${s.variant} source differs from a fresh render`);
    }
    if (/\{\{[A-Z_]+\}\}/.test(page)) errs.push('unfilled template placeholder');
    return errs;
  });
  report(checks, model);
}

function report(checks, model) {
  const N = checks.length;
  for (const c of checks) process.stderr.write(`[${c.n}/${N}] ${c.name} ${c.pass ? 'PASS' : 'FAIL'}\n${c.pass ? '' : '  ' + c.errors.join('\n  ') + '\n'}`);
  const pass = checks.every(c => c.pass);
  const gaps = model ? model.view_decisions.some(d => d.decision === 'not-produced') : false;
  process.stdout.write(JSON.stringify({ status: pass ? 'PASS' : 'FAIL', gaps, checks }, null, 1) + '\n');
  process.exit(pass ? 0 : 1);
}

main();
