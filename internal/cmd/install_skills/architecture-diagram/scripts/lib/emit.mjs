import { basename } from 'node:path';
import { PALETTE, TEXT_COLOR, LINE_COLOR, LEGEND_WIDTH, esc, escSeq, norm, wrap } from './common.mjs';

const SEQ_WIDTH = 48;

const SHAPES = {
  person: (id, l) => `${id}(["${l}"])`,
  internal: (id, l) => `${id}["${l}"]`,
  process: (id, l) => `${id}("${l}")`,
  external: (id, l) => `${id}[["${l}"]]`,
  store: (id, l) => `${id}[("${l}")]`,
  queue: (id, l) => `${id}>"${l}"]`,
};
const SHAPE_WORDS = {
  person: 'stadium = person', internal: 'rectangle = system, container or component', process: 'rounded box = process',
  external: 'double-edged box = external system or entity', store: 'cylinder = data store', queue: 'flag = queue',
};
const PALETTE_ROLE = { process: 'internal' };

const lbl = (lines) => lines.map(esc).join('<br/>');
const deltaStyle = (d) => (d === 'new' || d === 'changed' ? 'bold' : d === 'removed' ? 'dotted' : 'plain');

function classDefs(roles) {
  const out = [];
  for (const key of roles) {
    const [role, style] = key.split('_');
    const p = PALETTE[PALETTE_ROLE[role] || role];
    const width = style === 'bold' ? '3px' : '1.5px';
    const dash = style === 'dotted' ? ',stroke-dasharray:2 3' : '';
    out.push(`  classDef ${key} fill:${p.fill},stroke:${p.stroke},stroke-width:${width},color:${TEXT_COLOR}${dash}`);
  }
  out.push(`  classDef legend fill:#FFFFFF,stroke:${LINE_COLOR},stroke-width:1px,color:${TEXT_COLOR},text-align:left`);
  return out;
}

function frontmatter(title) {
  return [
    '---',
    `title: ${JSON.stringify(title)}`,
    'config:',
    '  layout: elk',
    '  look: classic',
    '  theme: base',
    '  htmlLabels: false',
    '  flowchart:',
    '    htmlLabels: false',
    '    wrappingWidth: 600',
    '  themeVariables:',
    '    background: "#FFFFFF"',
    `    primaryTextColor: "${TEXT_COLOR}"`,
    `    lineColor: "${LINE_COLOR}"`,
    '    fontFamily: "-apple-system, Segoe UI, Helvetica, Arial, sans-serif"',
    '---',
  ];
}

export function legendLines(view, model, glossary, variant = 'canonical') {
  const src = model.meta.source;
  const what = view.state === 'current' ? 'behaviour as the design doc describes it today' : 'the design as stated';
  const lines = variant === 'miro' ? [`Key: ${view.title}`] : ['Key'];
  lines.push(`Source: ${basename(src.path)} · sha256 ${src.sha256.slice(0, 12)} · ${what}, not verified against code`);
  const deltas = new Set(view.nodes.map(n => n.delta));
  const tags = [];
  if (deltas.has('new')) tags.push('[NEW]');
  if (deltas.has('changed')) tags.push('[CHANGED]');
  if (deltas.has('removed')) tags.push('[REMOVED]');
  if (deltas.has('existing')) tags.push('[EXISTING]');
  const tagLine = `Tags: ${tags.length ? tags.join(' ') + ' as the design doc states; ' : ''}no tag = the design doc does not say whether it is new, changed or existing`;
  if (view.type === 'dynamic') {
    lines.push('Numbered steps in order; solid arrow = synchronous call, open arrow = asynchronous message, dashed = reply');
    if ((view.steps || []).some(s => s.when)) lines.push('opt boxes: steps that happen only under the stated condition');
    lines.push(tagLine);
  } else {
    const roles = [...new Set(view.nodes.map(n => n.role))];
    lines.push(variant === 'miro' ? 'Shapes: every box is a rectangle; its kind is written inside it' : 'Shapes: ' + roles.map(r => SHAPE_WORDS[r]).join('; '));
    const styles = new Set(view.nodes.map(n => deltaStyle(n.delta)));
    const b = [];
    if (styles.has('bold')) b.push('thick border = new or changed');
    if (styles.has('dotted')) b.push('dotted border = removed');
    if (styles.has('plain')) b.push('thin border = everything else');
    lines.push('Borders: ' + b.join('; '));
    lines.push(tagLine);
    if (view.type === 'security') {
      lines.push('Each arrow is a flow of the named data, pointing in the direction the data moves');
      if (view.subgraphs.length) lines.push('Thick dashed boxes: trust boundaries stated in the design doc');
      for (const s of view.subgraphs) if (s.rule) lines.push(`Rule of ${s.name}: ${s.rule}`);
    } else {
      const kinds = new Set(view.edges.map(e => e.interaction));
      const l = [];
      if (kinds.has('sync')) l.push('solid arrow = synchronous request');
      if (kinds.has('async')) l.push('dashed arrow = asynchronous message');
      if (l.length) lines.push('Lines: ' + l.join('; '));
      lines.push('Arrows point from initiator to responder');
      if (view.subgraphs.length) lines.push('Dashed boxes: ' + [...new Set(view.subgraphs.map(s => s.kindLabel.replace(/:.*/, '')))].join('; '));
    }
  }
  if (view.type === 'deployment') lines.push('Shows placed elements and the people and outside systems they talk to; other relationships are in the other views');
  if (model.meta.queue_style === 'via' && view.type !== 'dynamic') lines.push('Message brokers are shown as "via <topic>" in edge labels');
  if (view.nodes.some(n => n.parts.open.length)) lines.push('[OPEN] = the design doc leaves a question about this element unresolved');
  lines.push('not stated = the design doc does not say');
  const used = glossary.filter(g => g.expansion != null && new RegExp(`\\b${g.term.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\b`).test(view.text));
  if (used.length) lines.push('Acronyms: ' + used.map(g => `${g.term} = ${g.expansion}`).join('; '));
  return lines;
}

export function viewText(view) {
  return [...view.nodes.flatMap(n => n.label), ...view.edges.flatMap(e => e.label), ...view.subgraphs.map(s => s.name), ...(view.steps || []).map(s => s.message)].join(' ');
}

function flowchart(view, model, glossary, variant) {
  const out = variant === 'canonical' ? [...frontmatter(view.title)] : [];
  out.push(`flowchart ${view.direction}`);
  if (variant === 'canonical') {
    out.push(`  accTitle: ${norm(view.title)}`);
    out.push(`  accDescr: ${norm([view.assertion, `Shows ${view.nodes.length} elements and ${view.edges.length} relationships.`].filter(Boolean).join(' '))}`);
  }
  const usedClasses = new Set();
  const nodeLine = (n, indent) => {
    const cls = `${n.role}_${deltaStyle(n.delta)}`;
    usedClasses.add(cls);
    const lines = [...n.label];
    if (variant === 'miro' && n.boundary) lines.push(`in: ${view.subgraphs.find(s => s.id === n.boundary)?.name ?? n.boundary}`);
    const shape = variant === 'miro' ? SHAPES.internal : SHAPES[n.role];
    return `${indent}${shape(n.id, lbl(lines))}:::${cls}`;
  };
  const emitSub = (sg, indent) => {
    out.push(`${indent}subgraph ${sg.id}["${esc(sg.name)} [${esc(sg.kindLabel)}]"]`);
    out.push(`${indent}  direction ${view.direction}`);
    for (const child of view.subgraphs.filter(s => s.parent === sg.id)) emitSub(child, indent + '  ');
    for (const n of view.nodes.filter(n => n.boundary === sg.id)) out.push(nodeLine(n, indent + '  '));
    out.push(`${indent}end`);
  };
  for (const sg of view.subgraphs.filter(s => !s.parent)) emitSub(sg, '  ');
  for (const n of view.nodes.filter(n => !n.boundary)) out.push(nodeLine(n, '  '));
  for (const e of view.edges) {
    const arrow = e.interaction === 'async' ? '-.->' : '-->';
    out.push(`  ${e.from} ${arrow}|"${lbl(e.label)}"| ${e.to}`);
  }
  out.push(`  legend["${lbl(legendLines(view, model, glossary, variant).flatMap(l => wrap(l, LEGEND_WIDTH)))}"]:::legend`);
  out.push(...classDefs([...usedClasses].sort()));
  for (const sg of view.subgraphs) {
    const w = sg.style === 'trust' ? '2px' : '1px';
    out.push(`  style ${sg.id} fill:#FFFFFF,stroke:${LINE_COLOR},stroke-width:${w},stroke-dasharray:6 4,color:${TEXT_COLOR}`);
  }
  if (variant === 'canonical') {
    for (const n of view.nodes) out.push(`  click ${n.id} "#view=${view.id}&el=${n.element}" "Details" _self`);
  }
  return out.join('\n') + '\n';
}

function sequence(view, model, glossary, variant) {
  const out = variant === 'canonical' ? [...frontmatter(view.title)] : [];
  out.push('sequenceDiagram');
  if (variant === 'canonical') {
    out.push(`  accTitle: ${norm(view.title)}`);
    out.push(`  accDescr: ${norm([view.assertion, `Shows ${view.steps.length} steps between ${view.nodes.length} participants.`].filter(Boolean).join(' '))}`);
  }
  out.push('  autonumber');
  for (const n of view.nodes) out.push(`  participant ${n.id} as ${[...n.parts.name, ...n.parts.kind, ...n.parts.open].map(escSeq).join('<br/>')}`);
  let open = null;
  for (const s of view.steps) {
    if (s.when !== open) {
      if (open != null) out.push('  end');
      if (s.when != null) out.push(`  opt ${escSeq(s.when)}`);
      open = s.when;
    }
    const arrow = s.interaction === 'async' ? '-)' : s.interaction === 'reply' ? '-->>' : '->>';
    out.push(`  ${open != null ? '  ' : ''}${s.from}${arrow}${s.to}: ${wrap(s.message, SEQ_WIDTH).map(escSeq).join('<br/>')}`);
  }
  if (open != null) out.push('  end');
  const first = view.nodes[0].id;
  const last = view.nodes[view.nodes.length - 1].id;
  out.push(`  Note over ${first},${last}: ${legendLines(view, model, glossary, variant).flatMap(l => wrap(l, LEGEND_WIDTH)).map(escSeq).join('<br/>')}`);
  return out.join('\n') + '\n';
}

export function emit(view, model, glossary, variant) {
  return view.type === 'dynamic' ? sequence(view, model, glossary, variant) : flowchart(view, model, glossary, variant);
}
