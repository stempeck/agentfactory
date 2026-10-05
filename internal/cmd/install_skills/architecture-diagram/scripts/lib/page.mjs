import { readFileSync } from 'node:fs';
import { join, dirname, basename } from 'node:path';
import { fileURLToPath } from 'node:url';
import { MERMAID_CDN, MERMAID_SRI, MERMAID_VERSION, SKILL_VERSION } from './common.mjs';

const TEMPLATE = join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'assets', 'index.template.html');

// '<' is escaped so a design-doc quote containing "</script" cannot end the data block; JSON.parse restores it.
export function embedJSON(value) {
  return JSON.stringify(value).replace(/</g, '\\u003c').replace(/\u2028/g, '\\u2028').replace(/\u2029/g, '\\u2029');
}

const html = (s) => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

export function pageViews(views) {
  return views.map(v => ({
    id: v.id, type: v.type, title: v.title, question: v.question, assertion: v.assertion,
    assertion_lines: (v.assertion_src || []).map(c => c.lines), parent_view: v.parent_view, drill_down: v.drill_down,
    nodes: v.nodes.map(n => ({ id: n.id, element: n.element, boundary: n.boundary ?? null })),
    edges: v.edges.map(e => ({ from: e.from, to: e.to, relationships: e.relationships, crossed: e.crossed ?? null })),
    steps: v.steps ?? null, subgraphs: v.subgraphs.map(s => ({ id: s.id, name: s.name, kind: s.kindLabel })), counts: v.counts,
  }));
}

export function buildPage(model, views, sources, generatedAt) {
  for (const s of sources) if (/<\/script/i.test(s.text)) throw new Error(`${s.view} ${s.variant} source contains "</script"`);
  const sys = model.elements.find(e => e.id === model.meta.system_element);
  const sourceBlocks = sources.map(s => `<script type="text/plain" data-view="${s.view}" data-variant="${s.variant}">\n${s.text}</script>`).join('\n');
  const fill = {
    TITLE: html(`${sys.name}: architecture diagrams (proposed design)`),
    SYSTEM: html(sys.name),
    SOURCE: html(`${basename(model.meta.source.path)} (sha256 ${model.meta.source.sha256.slice(0, 12)})`),
    GENERATED: html(generatedAt),
    SKILL_VERSION: html(SKILL_VERSION),
    MERMAID_VERSION: html(MERMAID_VERSION),
    MERMAID_URL: MERMAID_CDN,
    MERMAID_SRI,
    MODEL_JSON: embedJSON(model),
    VIEWS_JSON: embedJSON(pageViews(views)),
    SOURCES: sourceBlocks,
  };
  let out = readFileSync(TEMPLATE, 'utf8');
  for (const [k, v] of Object.entries(fill)) out = out.split(`{{${k}}}`).join(v);
  const left = out.match(/\{\{[A-Z_]+\}\}/);
  if (left) throw new Error(`template placeholder ${left[0]} was not filled`);
  return out;
}
