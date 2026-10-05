#!/usr/bin/env node
import { readFileSync, writeFileSync, existsSync, mkdirSync, readdirSync, rmSync, cpSync, statSync } from 'node:fs';
import { join, dirname, basename, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { checkModel } from './lib/check.mjs';
import { renderAll } from './lib/render.mjs';
import { buildPage } from './lib/page.mjs';
import { CACHE } from './lib/deps.mjs';
import { sha256 } from './lib/common.mjs';

const HELP = `usage:
  node render.mjs --prepare <design-dir>/architecture-diagrams
      Create the output directory, or empty it. Before emptying, contents that git does not hold
      unchanged (untracked, modified, ignored, or no git repo) are copied to
      \${XDG_CACHE_HOME:-~/.cache}/architecture-diagram/prev/<12 hex>/<UTC time>/ and the path is printed.
  node render.mjs <design-dir>/architecture-diagrams/model.json
      Gate 3. Refuses unless check_model passes, then writes views/NN-*.mmd (canonical Mermaid),
      miro/NN-*.mmd (Miro paste variant) and index.html. Never writes .md files.
Node labels wrap at 28 characters, edge labels at 24 and legend lines at 64, on word boundaries, so the page, the SVG export and the Miro
paste break lines in the same places. Exit codes: 0 ok; 1 model check failed; 2 usage error.`;

const OUT_NAME = 'architecture-diagrams';

function prepare(dir) {
  const out = resolve(dir);
  if (basename(out) !== OUT_NAME) { process.stderr.write(`refusing: --prepare only manages a directory named ${OUT_NAME}\n`); process.exit(2); }
  let preserved = null;
  if (existsSync(out) && readdirSync(out).length) {
    const git = spawnSync('git', ['-C', dirname(out), 'status', '--porcelain', '--ignored', '--', out], { encoding: 'utf8' });
    if (git.status !== 0 || git.stdout.trim()) {
      const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
      preserved = join(CACHE, 'prev', sha256(out).slice(0, 12), stamp);
      mkdirSync(dirname(preserved), { recursive: true });
      cpSync(out, preserved, { recursive: true });
    }
    for (const entry of readdirSync(out)) rmSync(join(out, entry), { recursive: true, force: true });
  }
  mkdirSync(out, { recursive: true });
  process.stdout.write(JSON.stringify({ out, preserved }, null, 1) + '\n');
}

function render(modelPath) {
  const path = resolve(modelPath);
  const out = dirname(path);
  if (basename(out) !== OUT_NAME) { process.stderr.write(`refusing: model.json must live in a directory named ${OUT_NAME}\n`); process.exit(2); }
  const model = JSON.parse(readFileSync(path, 'utf8'));
  const result = checkModel(model);
  if (result.status !== 'PASS') {
    for (const c of result.checks.filter(c => !c.pass)) process.stderr.write(`[${c.n}] ${c.name} FAIL\n  ${c.errors.join('\n  ')}\n`);
    process.stderr.write('render refused: run check_model.mjs and repair model.json first\n');
    process.exit(1);
  }
  for (const entry of readdirSync(out)) if (entry.endsWith('.md')) { process.stderr.write(`refusing: ${join(out, entry)} is a .md file; this skill never writes or keeps .md output\n`); process.exit(2); }
  const { views, sources } = renderAll(model);
  for (const sub of ['views', 'miro']) { rmSync(join(out, sub), { recursive: true, force: true }); mkdirSync(join(out, sub)); }
  const files = [];
  for (const s of sources) {
    const f = join(out, s.variant === 'canonical' ? 'views' : 'miro', `${s.view}.mmd`);
    writeFileSync(f, s.text);
    files.push(f);
  }
  const page = buildPage(model, views, sources, new Date().toISOString());
  writeFileSync(join(out, 'index.html'), page);
  files.push(join(out, 'index.html'));
  process.stdout.write(JSON.stringify({ out, views: views.map(v => ({ id: v.id, title: v.title, counts: v.counts })), files }, null, 1) + '\n');
}

const args = process.argv.slice(2);
if (!args.length || args[0] === '--help') { process.stdout.write(HELP + '\n'); process.exit(args.length ? 0 : 2); }
if (args[0] === '--prepare') {
  if (!args[1]) { process.stderr.write('usage: render.mjs --prepare <dir>\n'); process.exit(2); }
  prepare(args[1]);
} else {
  if (!existsSync(args[0]) || !statSync(args[0]).isFile()) { process.stderr.write(`not found: ${args[0]}\n`); process.exit(2); }
  render(args[0]);
}
