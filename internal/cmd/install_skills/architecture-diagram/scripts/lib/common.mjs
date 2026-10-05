import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';

export const SKILL_VERSION = '1.0.0';
export const MODEL_SCHEMA = 'architecture-diagram.model/1';

export const MERMAID_VERSION = '12.1.0';
export const MERMAID_CDN = `https://cdn.jsdelivr.net/npm/mermaid@${MERMAID_VERSION}/dist/mermaid.min.js`;
// validate.mjs recomputes this from the installed package, so a republished or bumped file fails the gate instead of the page.
export const MERMAID_SRI = 'sha384-EbBpjO7rlR6eqZEcG7GaPpyk9H9WrMyPWX4d3KvPYltgt8Z8l0z6R56B1qP40pR4';

export const VIEW_TYPES = ['context', 'container', 'component', 'dynamic', 'deployment', 'security'];
export const VIEW_LABEL = {
  context: 'System context', container: 'Container', component: 'Component',
  dynamic: 'Dynamic', deployment: 'Deployment', security: 'Security data-flow',
};
export const VIEWS_PER_TYPE = { context: 1, container: 1, component: 3, dynamic: 3, deployment: 2, security: 1 };
// Skill choices, not standards: Context follows 7±2, the rest the split-above-15 rule; the total (nodes + boundaries + edges + edge labels) bounds visual load.
export const NODE_CAP = { context: 9, container: 15, component: 15, dynamic: 15, deployment: 15, security: 15 };
export const TOTAL_CAP = 80;

export const KINDS = ['person', 'software_system', 'container', 'component', 'external_system', 'data_store', 'queue'];
export const KIND_LABEL = {
  person: 'Person', software_system: 'Software System', container: 'Container', component: 'Component',
  external_system: 'External System', data_store: 'Data Store', queue: 'Queue',
};
export const DELTAS = ['new', 'changed', 'removed', 'existing', 'unstated'];
export const DELTA_WORDS = {
  removed: /\b(remov\w*|delet\w*|retir\w*|drop(s|ped)?|decommission\w*)\b/gi,
  new: /\b(new|adds?|added|introduc\w*|creat\w*)\b/gi,
  changed: /\b(modif\w*|chang\w*|gains?|extend\w*|amend\w*|replac\w*|updat\w*|mod|adds?|added|fix\w*|alter\w*|rewrit\w*|refactor\w*|renam\w*|mov(e|es|ed))\b/gi,
  existing: /\b(unchanged|untouched|as today|existing|pre-existing|today's)\b/gi,
};
const NEGATION = /\b(no|not|without|never|nor|neither)\b/i;

// A change word counts only when none of the three words before it negates it ("No new functions").
export function deltaWordFound(delta, text) {
  const re = new RegExp(DELTA_WORDS[delta].source, 'gi');
  const words = norm(text);
  for (let m; (m = re.exec(words));) {
    const before = words.slice(0, m.index).split(' ').slice(-3).join(' ');
    if (!NEGATION.test(before)) return true;
  }
  return false;
}

// A change word must sit in the same clause (sentence, semicolon part or table cell) as the element it marks.
export function deltaAbout(delta, quote, about) {
  return norm(quote).split(/(?<=[.;])\s+|\s*\|\s*/).some(c => deltaWordFound(delta, c) && about.some(a => mentions(c, a)));
}

export function mentions(text, element) {
  const t = norm(text).toLowerCase();
  if (element.name && t.includes(norm(element.name).toLowerCase())) return true;
  if ((element.aliases || []).some(a => t.includes(norm(a).toLowerCase()))) return true;
  return (element.doc_ids || []).some(id => new RegExp(`\\b${String(id).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\b`, 'i').test(norm(text)));
}
export const BOUNDARY_KINDS = ['trust', 'deployment_node', 'group'];
export const EXCLUSION_REASONS = [
  'deliverable', 'withdrawn-from-design', 'code-level', 'current-state', 'build-order', 'control-flow',
  'file-relation', 'plumbing', 'no-relationship-stated', 'view-limit', 'ambiguous-target', 'out-of-scope',
  'not-a-relationship', 'not-in-any-view',
];
export const DECISIONS = ['produced', 'skipped', 'not-produced'];

// Words Mermaid's flowchart and sequence grammars treat as keywords, plus the id the legend node uses.
export const RESERVED_IDS = new Set([
  'end', 'graph', 'flowchart', 'subgraph', 'class', 'classdef', 'click', 'style', 'linkstyle', 'default', 'call',
  'href', 'legend', 'participant', 'actor', 'note', 'loop', 'alt', 'opt', 'par', 'rect', 'box', 'critical',
  'break', 'and', 'else', 'as', 'title', 'direction', 'autonumber', 'activate', 'deactivate', 'o', 'x',
]);
export const ACRONYM_ALLOWLIST = new Set([
  'API', 'CI', 'CLI', 'CPU', 'DNS', 'HTTP', 'HTTPS', 'ID', 'JSON', 'OS', 'PR', 'SQL', 'SSH', 'TCP', 'TLS',
  'UDP', 'UI', 'URL', 'YAML', 'CA', 'CSS', 'HTML', 'XML', 'PDF', 'REST',
]);
export const BANNED_VERBS = new Set([
  'uses', 'use', 'contains', 'relates', 'has', 'supports', 'manages', 'integrates', 'interacts',
  'communicates', 'connects', 'talks',
]);

export const ROLE = {
  person: 'person', software_system: 'internal', container: 'internal', component: 'internal',
  external_system: 'external', data_store: 'store', queue: 'queue',
};
// Paul Tol bright/high-contrast strokes; every stroke clears 3:1 on white and on its fill (WCAG 1.4.11), text clears 15:1.
export const PALETTE = {
  person: { stroke: '#AA3377', fill: '#FAF3F7' },
  internal: { stroke: '#4477AA', fill: '#F4F7FA' },
  external: { stroke: '#7A7A7A', fill: '#F7F7F7' },
  store: { stroke: '#228833', fill: '#F2F8F3' },
  queue: { stroke: '#BB5566', fill: '#FBF5F6' },
};
export const TEXT_COLOR = '#1A1A1A';
export const LINE_COLOR = '#555555';
export const LABEL_WIDTH = 28;
// Mermaid 12.1.0 wraps plain-text edge labels at a fixed 200px with no config key, so edge lines break here first.
export const EDGE_WIDTH = 24;
export const LEGEND_WIDTH = 64;

export function sha256(data) {
  return createHash('sha256').update(data).digest('hex');
}

export function readDoc(path) {
  const buf = readFileSync(path);
  const text = buf.toString('utf8');
  const lines = text.split(/\r?\n/);
  if (lines.length && lines[lines.length - 1] === '') lines.pop();
  return { text, lines, sha256: sha256(buf) };
}

// Quotes are compared after dropping code ticks and bold markers, which a reader never sees as content; a single '*' is kept because it is often code (a regex, a glob).
export function norm(s) {
  return String(s ?? '').replace(/`/g, '').replace(/\*\*/g, '').replace(/\s+/g, ' ').trim();
}

export function quoteHolds(lines, cite) {
  if (!cite || !Array.isArray(cite.lines) || cite.lines.length !== 2) return false;
  const [a, b] = cite.lines;
  if (!Number.isInteger(a) || !Number.isInteger(b) || a < 1 || b < a || b > lines.length) return false;
  const q = norm(cite.quote);
  if (!q) return false;
  return norm(lines.slice(a - 1, b).join(' ')).includes(q);
}

export function citedText(cites) {
  return (cites || []).map(c => norm(c.quote)).join(' \u0000 ');
}

export function isVerbatim(value, cites) {
  const v = norm(value).toLowerCase();
  if (!v) return false;
  return citedText(cites).toLowerCase().includes(v);
}

export function isVerbatimSummary(value, cites) {
  const v = norm(value);
  if (v.endsWith('…')) return isVerbatim(v.slice(0, -1).trimEnd(), cites);
  return isVerbatim(v, cites);
}

export function wrap(text, width = LABEL_WIDTH) {
  const words = norm(text).split(' ').filter(Boolean);
  const out = [];
  let cur = '';
  for (const w of words) {
    if (!cur) cur = w;
    else if ((cur + ' ' + w).length <= width) cur += ' ' + w;
    else { out.push(cur); cur = w; }
  }
  if (cur) out.push(cur);
  return out;
}

// '#' first because every replacement below introduces one.
export function esc(s) {
  return norm(s)
    .replace(/#/g, '#35;')
    .replace(/"/g, '#quot;')
    .replace(/</g, '#lt;')
    .replace(/>/g, '#gt;')
    .replace(/\|/g, '#124;');
}

// A bare ';' ends a sequence message; it is parked before esc() so the entity semicolons esc() adds survive.
export function escSeq(s) {
  return esc(String(s ?? '').replace(/;/g, '\u0001')).replace(/\u0001/g, '#59;');
}

// The renderer's own tags and English words docs capitalise for emphasis are not acronyms.
const NOT_ACRONYMS = new Set(['NEW', 'CHANGED', 'REMOVED', 'OPEN', 'EXISTING', 'AND', 'OR', 'NOT', 'NO', 'MUST', 'ONLY', 'NEVER', 'ALL',
  'ANY', 'NONE', 'IS', 'ARE', 'BE', 'IN', 'ON', 'TO', 'OF', 'FOR', 'THE', 'IF', 'THEN', 'ELSE', 'WITH', 'WITHOUT', 'AT', 'BY',
  'AS', 'IT', 'ONE', 'TWO', 'YES', 'OK', 'NOTE', 'WARNING', 'IMPORTANT', 'TODO', 'TBD', 'NOW', 'BOTH', 'EVERY', 'EACH']);

export function acronymsIn(text) {
  return [...new Set((norm(text).match(/\b[A-Z][A-Z0-9]{1,}\b/g) || []))].filter(a => !NOT_ACRONYMS.has(a));
}

export function fail(code, message) {
  process.stderr.write(message.endsWith('\n') ? message : message + '\n');
  process.exit(code);
}
