import { readDoc } from './common.mjs';

// First matching concept wins; order matters because "Architecture" is a catch-all for components.
export const CONCEPTS = [
  ['relationships', [/dependency graph/i, /^data flows?$/i, /architecture dimension/i, /^dependencies$/i, /^interactions?$/i, /component graph/i]],
  ['flows', [/\bflows?\b/i, /^sequence/i, /walkthrough/i]],
  ['purpose', [/^executive summary$/i, /^overview$/i, /^summary$/i, /^problem statement$/i, /^problem analysis$/i, /^goals?$/i]],
  ['components', [/^key components$/i, /^component responsibilities$/i, /^component layout$/i, /^components$/i, /^(current|proposed) architecture$/i, /^(?!.*elevation).*architecture/i]],
  ['deployment', [/^deployment/i, /^infrastructure/i, /^topology/i, /^interface$/i, /^data model$/i]],
  ['security', [/security/i, /\btrust\b/i, /privacy/i, /threat/i, /^risk registry$/i]],
  ['open', [/^open (questions?|issues?)/i, /known limitations?/i, /caveats/i]],
  ['decisions', [/^decisions( made)?$/i]],
];

const ARROW = /(─+[▶►>→]|[▶►→⇒←⟶]|-{1,2}>|<-{1,2}|={1,2}>)/;

export function stripNumbering(text) {
  return text.replace(/^(appendix\s+[a-z0-9]+[.:]?\s+|§\s*\d+(\.\d+)*\s+|\d+(\.\d+)*[.)]?\s+|[a-z][.)]\s+)/i, '').trim();
}

export function slice(path) {
  const doc = readDoc(path);
  const { lines } = doc;
  const fences = [];
  const inFence = new Array(lines.length + 2).fill(false);
  let open = null;
  lines.forEach((l, i) => {
    const n = i + 1;
    if (/^ {0,3}(```|~~~)/.test(l)) {
      if (open === null) open = n;
      else { fences.push([open, n]); for (let k = open; k <= n; k++) inFence[k] = true; open = null; }
    }
  });
  if (open !== null) { fences.push([open, lines.length]); for (let k = open; k <= lines.length; k++) inFence[k] = true; }

  const headings = [];
  lines.forEach((l, i) => {
    const n = i + 1;
    if (inFence[n]) return;
    const m = /^(#{1,6})\s+(.*?)\s*#*\s*$/.exec(l);
    if (m) headings.push({ line: n, level: m[1].length, text: m[2] });
  });

  const h1s = headings.filter(h => h.level === 1);
  const title = h1s[0] || null;
  const nextH1 = title ? h1s.find(h => h.line > title.line) : null;
  const body = [1, nextH1 ? nextH1.line - 1 : lines.length];
  const appendedH1 = h1s.filter(h => h.line > body[1]).map(h => ({ line: h.line, heading: h.text }));

  const sections = headings
    .filter(h => h.line <= body[1])
    .map((h, idx, arr) => {
      const next = arr.slice(idx + 1).find(o => o.level <= h.level);
      const end = Math.min(next ? next.line - 1 : body[1], body[1]);
      const bare = stripNumbering(h.text.replace(/[`*]/g, ''));
      const concept = (CONCEPTS.find(([, pats]) => pats.some(p => p.test(bare))) || [null])[0];
      return { line: h.line, level: h.level, heading: h.text, range: [h.line, end], concept };
    });

  const byConcept = {};
  for (const s of sections) if (s.concept) (byConcept[s.concept] ||= []).push({ heading: s.heading, range: s.range });

  const covered = (concept) => {
    const out = new Set();
    for (const s of sections.filter(s => s.concept === concept)) for (let n = s.range[0] + 1; n <= s.range[1]; n++) out.add(n);
    return [...out].sort((a, b) => a - b);
  };

  const componentItems = [];
  const compLines = covered('components');
  const compSet = new Set(compLines);
  for (const n of compLines) {
    const l = lines[n - 1];
    if (inFence[n]) continue;
    if (/^\s*\|/.test(l)) {
      if (/^\s*\|[\s:|-]+\|\s*$/.test(l)) continue;
      const nextIsSep = compSet.has(n + 1) && /^\s*\|[\s:|-]+\|\s*$/.test(lines[n] || '');
      if (nextIsSep) continue;
      componentItems.push({ line: n, kind: 'table-row', text: l.trim().slice(0, 160) });
    } else if (/^(- |\* |\d+[.)] )/.test(l)) {
      componentItems.push({ line: n, kind: 'list-item', text: l.trim().slice(0, 160) });
    } else {
      const h = headings.find(h => h.line === n);
      if (h) componentItems.push({ line: n, kind: 'sub-heading', text: h.text.slice(0, 160) });
    }
  }

  const arrowLines = [];
  for (let n = body[0]; n <= body[1]; n++) {
    const l = lines[n - 1];
    if (/^\s*\|[\s:|-]+\|\s*$/.test(l)) continue;
    if (ARROW.test(l)) arrowLines.push({ line: n, text: l.trim().slice(0, 160) });
  }

  return {
    path, sha256: doc.sha256, lines: lines.length, title: title ? { line: title.line, text: title.text } : null,
    body, appended_h1: appendedH1, fences, headings: sections.map(({ line, level, heading, concept }) => ({ line, level, heading, hint: concept })),
    where_to_look: byConcept, component_items: componentItems, arrow_lines: arrowLines,
  };
}
