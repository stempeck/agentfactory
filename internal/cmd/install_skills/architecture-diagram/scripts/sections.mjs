#!/usr/bin/env node
import { readdirSync, statSync, existsSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { slice } from './lib/sections.mjs';
import { readDoc, norm } from './lib/common.mjs';

const HELP = `usage:
  node sections.mjs <design-doc.md | design-dir>   print the fence-aware structure of the design doc as JSON
  node sections.mjs --discover [root]               list design-doc.md files under root (default: cwd)
  node sections.mjs --quote <design-doc.md> <a> [b] print lines a..b exactly as citation quotes are compared
                                                    (backticks and asterisks dropped, whitespace collapsed)

Writes nothing. Exit codes: 0 ok; 2 input error or --discover found zero or several candidates.
JSON fields: sha256, lines, title, body [first,last] (ends before the first H1 after the title,
outside fences), appended_h1, fences, headings (with a hint), where_to_look (hint -> sections; heading
names only, not a classification of their content), component_items (every table row, top-level list
item and sub-heading of component sections; check_model.mjs requires an inventory record for each),
arrow_lines (every arrow-bearing line of the body; check_model.mjs requires an arrow_ledger record for each).`;

const SKIP = new Set(['node_modules', '.git', 'architecture-diagrams']);

function discover(root) {
  const found = [];
  const walk = (dir, depth) => {
    if (depth > 6) return;
    let entries;
    try { entries = readdirSync(dir, { withFileTypes: true }); } catch { return; }
    for (const e of entries) {
      if (SKIP.has(e.name)) continue;
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p, depth + 1);
      else if (e.name === 'design-doc.md') found.push(p);
    }
  };
  walk(resolve(root), 0);
  return found.sort();
}

function resolveInput(arg) {
  const p = resolve(arg);
  if (!existsSync(p)) return { error: `not found: ${p}` };
  if (statSync(p).isDirectory()) {
    const f = join(p, 'design-doc.md');
    return existsSync(f) ? { path: f } : { error: `no design-doc.md in directory ${p}` };
  }
  return { path: p };
}

const args = process.argv.slice(2);
if (!args.length || args.includes('--help')) {
  process.stdout.write(HELP + '\n');
  process.exit(args.length ? 0 : 2);
}
if (args[0] === '--quote') {
  const r = resolveInput(args[1] || '');
  const a = Number(args[2]), b = Number(args[3] || args[2]);
  if (r.error || !Number.isInteger(a) || !Number.isInteger(b) || a < 1 || b < a) { process.stderr.write('usage: sections.mjs --quote <design-doc.md> <a> [b]\n'); process.exit(2); }
  const { lines } = readDoc(r.path);
  for (let n = a; n <= Math.min(b, lines.length); n++) process.stdout.write(`${n}: ${norm(lines[n - 1])}\n`);
  process.exit(0);
}
if (args[0] === '--discover') {
  const found = discover(args[1] || '.');
  process.stdout.write(JSON.stringify({ candidates: found }, null, 1) + '\n');
  process.exit(found.length === 1 ? 0 : 2);
}
const r = resolveInput(args[0]);
if (r.error) { process.stderr.write(r.error + '\n'); process.exit(2); }
process.stdout.write(JSON.stringify(slice(r.path), null, 1) + '\n');
