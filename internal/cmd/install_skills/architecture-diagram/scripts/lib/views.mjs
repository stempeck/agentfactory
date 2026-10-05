import { VIEW_TYPES, VIEW_LABEL, VIEWS_PER_TYPE, NODE_CAP, TOTAL_CAP, KIND_LABEL, ROLE, EDGE_WIDTH, wrap, norm } from './common.mjs';

export function indexModel(m) {
  const el = new Map((m.elements || []).map(e => [e.id, e]));
  const bd = new Map((m.boundaries || []).map(b => [b.id, b]));
  const rel = new Map((m.relationships || []).map(r => [r.id, r]));
  const flow = new Map((m.flows || []).map(f => [f.id, f]));
  const dep = new Map((m.deployments || []).map(d => [d.id, d]));
  const clusterOf = new Map();
  for (const e of m.elements || []) for (const mem of e.cluster_of || []) clusterOf.set(mem, e.id);
  const open = new Set();
  for (const o of m.open || []) for (const id of o.element_ids || []) open.add(id);
  return { m, el, bd, rel, flow, dep, clusterOf, open, sys: m.meta?.system_element };
}

export function chain(ix, id) {
  const out = [];
  const seen = new Set();
  let cur = ix.el.get(id);
  while (cur && !seen.has(cur.id)) {
    out.push(cur.id);
    seen.add(cur.id);
    cur = cur.parent ? ix.el.get(cur.parent) : null;
  }
  return out;
}

const cl = (ix, id) => ix.clusterOf.get(id) || id;

export function liftContext(ix, id) {
  const c = chain(ix, cl(ix, id));
  return cl(ix, c.includes(ix.sys) ? ix.sys : c[c.length - 1]);
}

function liftContainer(ix, id) {
  const c = chain(ix, cl(ix, id));
  const i = c.indexOf(ix.sys);
  if (i === -1) return cl(ix, c[c.length - 1]);
  return cl(ix, i === 0 ? ix.sys : c[i - 1]);
}

function liftComponent(ix, id, of) {
  const c = chain(ix, cl(ix, id));
  const i = c.indexOf(of);
  if (i > 0) return cl(ix, c[i - 1]);
  if (i === 0) return of;
  return liftContainer(ix, id);
}

const isOutside = (e) => e.kind === 'person' || e.kind === 'external_system' || ((e.kind === 'data_store' || e.kind === 'queue') && !e.parent);
const CHANGED = new Set(['new', 'changed', 'removed']);

export function childrenOf(ix, parent) {
  return [...new Set([...ix.el.values()].filter(e => e.parent === parent && !ix.clusterOf.has(e.id)).map(e => e.id))];
}

// Which views the model supports, ranked; check_model requires exactly the top of each list.
export function eligibility(m) {
  const ix = indexModel(m);
  const touchesSys = [...ix.rel.values()].some(r => {
    const a = liftContext(ix, r.from), b = liftContext(ix, r.to);
    return a !== b && (a === ix.sys || b === ix.sys);
  });
  const sysChildren = ix.sys ? childrenOf(ix, ix.sys).filter(id => ['container', 'data_store', 'queue'].includes(ix.el.get(id).kind)) : [];
  const containers = [...ix.el.values()].filter(e => e.kind === 'container' && !ix.clusterOf.has(e.id)).map(c => {
    const comps = childrenOf(ix, c.id).filter(id => ix.el.get(id).kind === 'component');
    const changed = comps.filter(id => CHANGED.has(ix.el.get(id).delta)).length;
    return { id: c.id, comps: comps.length, changed };
  }).filter(c => c.changed >= 1 || c.comps >= 2)
    .sort((a, b) => b.changed - a.changed || b.comps - a.comps || a.id.localeCompare(b.id));
  const flows = [...ix.flow.values()].map(f => {
    const parts = new Set((f.steps || []).flatMap(s => [cl(ix, s.from), cl(ix, s.to)]));
    const changed = [...parts].filter(id => CHANGED.has(ix.el.get(id)?.delta)).length;
    return { id: f.id, proposed: f.state === 'proposed' ? 1 : 0, changed, steps: (f.steps || []).length };
  }).sort((a, b) => b.proposed - a.proposed || b.changed - a.changed || b.steps - a.steps || a.id.localeCompare(b.id));
  const deps = [...ix.dep.values()].map(d => ({ id: d.id, nodes: new Set((d.placements || []).map(p => p.node)).size, placements: (d.placements || []).length }))
    .filter(d => d.nodes >= 2).sort((a, b) => b.placements - a.placements || a.id.localeCompare(b.id));
  const trust = [...ix.bd.values()].filter(b => b.kind === 'trust' && b.rule);
  const inTrust = (id) => trust.some(b => chain(ix, id).some(c => (b.members || []).includes(c)));
  const security = trust.length > 0 && [...ix.rel.values()].some(r => r.sensitive && (inTrust(r.from) || inTrust(r.to)));
  return {
    context: touchesSys,
    container: sysChildren.length >= 2 ? 'produce' : sysChildren.length === 1 ? 'skip' : 'none',
    component: containers.map(c => c.id),
    dynamic: flows.map(f => f.id),
    deployment: deps.map(d => d.id),
    security,
  };
}

export function viewSpecs(m) {
  const out = [];
  let n = 0;
  for (const t of VIEW_TYPES) {
    for (const v of (m.views || []).filter(v => v.type === t)) {
      n += 1;
      const slug = v.of ? '-' + String(v.of).replace(/_/g, '-') : '';
      out.push({ ...v, id: `${String(n).padStart(2, '0')}-${t}${slug}` });
    }
  }
  return out;
}

function dfdKind(e) {
  if (e.kind === 'data_store' || e.kind === 'queue') return 'Data Store';
  if (e.kind === 'person' || e.kind === 'external_system') return 'External Entity';
  return 'Process';
}

function shapeRole(e, type) {
  if (type === 'security') {
    const k = dfdKind(e);
    return k === 'Data Store' ? 'store' : k === 'External Entity' ? 'external' : 'process';
  }
  return ROLE[e.kind];
}

function labelParts(ix, e, type) {
  const tag = { new: '[NEW] ', changed: '[CHANGED] ', removed: '[REMOVED] ', existing: '[EXISTING] ' }[e.delta] || '';
  const kind = type === 'context' ? [`[${KIND_LABEL[e.kind]}]`]
    : type === 'security' ? wrap(`[${dfdKind(e)}${e.technology ? ': ' + e.technology : ''}]`)
      : isOutside(e) ? wrap(`[${KIND_LABEL[e.kind]}${e.technology ? ': ' + e.technology : ''}]`)
        : wrap(`[${KIND_LABEL[e.kind]}: ${e.technology || 'not stated'}]`);
  return { name: wrap(tag + e.name), kind, summary: e.summary ? wrap(e.summary) : [], open: ix.open.has(e.id) ? ['[OPEN]'] : [] };
}

function edgeLabel(rs, type) {
  const r = rs[0];
  const tag = { new: '[NEW] ', changed: '[CHANGED] ', removed: '[REMOVED] ' }[r.delta] || '';
  const text = type === 'security' ? (r.data || r.verb) : r.verb;
  const lines = wrap(tag + text + (rs.length > 1 ? ` (×${rs.length})` : ''), EDGE_WIDTH);
  if (type !== 'security' && r.data) lines.push(...wrap(`(${r.data})`, EDGE_WIDTH));
  if (type !== 'context' && type !== 'security' && r.technology) lines.push(...wrap(`[${r.technology}]`, EDGE_WIDTH));
  return lines;
}

function node(ix, id, type, extra = {}) {
  const e = ix.el.get(id);
  const parts = labelParts(ix, e, type);
  return { id, element: id, kind: e.kind, role: shapeRole(e, type), delta: e.delta, parts, label: [...parts.name, ...parts.kind, ...parts.summary, ...parts.open], ...extra };
}

// Relationships that lift to the same pair merge only when they say the same thing, so no stated flow hides behind another.
function mergeEdges(list, type, nodeIds) {
  const groups = new Map();
  for (const x of list) {
    if (x.a === x.b || !nodeIds.has(x.a) || !nodeIds.has(x.b)) continue;
    const key = [x.a, x.b, x.r.interaction, norm(x.r.verb).toLowerCase(), norm(x.r.data).toLowerCase(), x.r.sensitive].join('\u0000');
    if (!groups.has(key)) groups.set(key, { from: x.a, to: x.b, interaction: x.r.interaction, rels: [] });
    groups.get(key).rels.push(x.r);
  }
  return [...groups.values()].map(g => ({
    from: g.from, to: g.to, interaction: g.interaction, relationships: g.rels.map(r => r.id),
    delta: g.rels[0].delta, label: edgeLabel(g.rels, type),
  }));
}

function flowState(ix, spec) {
  return spec.type === 'dynamic' && ix.flow.get(spec.of)?.state === 'current' ? 'current' : 'proposed';
}

function titleFor(ix, spec) {
  const sysName = ix.el.get(ix.sys)?.name ?? 'the system';
  const suffix = flowState(ix, spec) === 'current' ? '(current behaviour, as the design doc describes it)' : '(proposed design)';
  switch (spec.type) {
    case 'context': return `System context diagram for ${sysName} ${suffix}`;
    case 'container': return `Container diagram for ${sysName} ${suffix}`;
    case 'component': return `Component diagram for ${ix.el.get(spec.of)?.name ?? spec.of} ${suffix}`;
    case 'dynamic': return `Dynamic diagram for ${ix.flow.get(spec.of)?.name ?? spec.of} ${suffix}`;
    case 'deployment': {
      const env = ix.dep.get(spec.of)?.environment || 'environment not named in design doc';
      return `Deployment diagram for ${sysName}: ${env} ${suffix}`;
    }
    default: return `Security data-flow diagram for ${sysName} ${suffix}`;
  }
}

function questionFor(ix, spec) {
  const sysName = ix.el.get(ix.sys)?.name ?? 'the system';
  switch (spec.type) {
    case 'context': return `What is ${sysName} for, who uses it, and what outside it does it depend on?`;
    case 'container': return `Which applications and data stores make up ${sysName}, and how do they communicate?`;
    case 'component': return `What does this design add or change inside ${ix.el.get(spec.of)?.name ?? spec.of}?`;
    case 'dynamic': return `In what order do the elements interact to carry out: ${ix.flow.get(spec.of)?.name ?? spec.of}?`;
    case 'deployment': return `Where does each part of ${sysName} run?`;
    default: return 'Where do credentials, keys, or sensitive data cross a stated trust boundary?';
  }
}

function buildContext(ix) {
  const rels = [...ix.rel.values()].map(r => ({ r, a: liftContext(ix, r.from), b: liftContext(ix, r.to) }));
  const ids = new Set([ix.sys]);
  for (const x of rels) {
    if (x.a === ix.sys && x.b !== ix.sys) ids.add(x.b);
    if (x.b === ix.sys && x.a !== ix.sys) ids.add(x.a);
  }
  return { nodes: [...ids].map(id => node(ix, id, 'context')), subgraphs: [], edges: mergeEdges(rels, 'context', ids) };
}

function buildNested(ix, type, of) {
  const inner = childrenOf(ix, of);
  const lift = type === 'container' ? (id) => liftContainer(ix, id) : (id) => liftComponent(ix, id, of);
  const rels = [...ix.rel.values()].map(r => ({ r, a: lift(r.from), b: lift(r.to) }));
  const innerSet = new Set(inner);
  const ids = new Set([...inner, of]);
  for (const x of rels) {
    if ((innerSet.has(x.a) || x.a === of) && !ids.has(x.b)) ids.add(x.b);
    if ((innerSet.has(x.b) || x.b === of) && !ids.has(x.a)) ids.add(x.a);
  }
  const e = ix.el.get(of);
  const kindLabel = type === 'container' ? `${KIND_LABEL[e.kind]} boundary` : `Container boundary${e.technology ? ': ' + e.technology : ''}`;
  const nodes = [...ids].filter(id => id !== of).map(id => node(ix, id, type, innerSet.has(id) ? { boundary: of } : {}));
  return { nodes, subgraphs: [{ id: of, name: e.name, kindLabel, style: 'scope', members: inner, parent: null }], edges: mergeEdges(rels, type, ids) };
}

function buildDynamic(ix, of) {
  const f = ix.flow.get(of);
  const order = [];
  for (const s of f.steps) for (const id of [s.from, s.to]) if (!order.includes(cl(ix, id))) order.push(cl(ix, id));
  // Participants show name and kind only, so summaries never reach the diagram, its key, or the acronym check.
  const nodes = order.map(id => { const n = node(ix, id, 'dynamic'); return { ...n, label: [...n.parts.name, ...n.parts.kind, ...n.parts.open] }; });
  return {
    nodes, subgraphs: [], edges: [],
    steps: f.steps.map(s => ({ from: cl(ix, s.from), to: cl(ix, s.to), message: s.message, interaction: s.interaction, when: s.when ?? null })),
  };
}

function buildDeployment(ix, of) {
  const d = ix.dep.get(of);
  const placedAt = new Map();
  for (const p of d.placements) {
    const id = cl(ix, p.element);
    if (!placedAt.has(id)) placedAt.set(id, []);
    placedAt.get(id).push(p.node);
  }
  const used = new Set();
  const addB = (bid) => { let b = ix.bd.get(bid); while (b && !used.has(b.id)) { used.add(b.id); b = b.parent ? ix.bd.get(b.parent) : null; } };
  for (const where of placedAt.values()) where.forEach(addB);
  const nodes = [];
  for (const [id, where] of placedAt) for (const w of where) nodes.push(node(ix, id, 'deployment', { id: `${id}__${w}`, boundary: w }));
  const instOf = (id) => {
    for (const c of chain(ix, cl(ix, id))) if (placedAt.has(cl(ix, c))) return placedAt.get(cl(ix, c)).map(w => `${cl(ix, c)}__${w}`);
    return [];
  };
  // People and systems outside the design are drawn beside the nodes so placed parts keep their outside connections.
  const outsideOf = (id) => { const top = chain(ix, cl(ix, id)).at(-1); const e = ix.el.get(cl(ix, top)); return e && isOutside(e) && !placedAt.has(e.id) ? [e.id] : []; };
  const list = [];
  for (const r of ix.rel.values()) {
    const as = instOf(r.from), bs = instOf(r.to);
    for (const a of as) for (const b of bs) list.push({ r, a, b });
    if (as.length && !bs.length) for (const a of as) for (const b of outsideOf(r.to)) list.push({ r, a, b });
    if (bs.length && !as.length) for (const b of bs) for (const a of outsideOf(r.from)) list.push({ r, a, b });
  }
  const outside = new Set(list.flatMap(x => [x.a, x.b]).filter(id => ix.el.has(id)));
  for (const id of outside) nodes.push(node(ix, id, 'deployment'));
  const ids = new Set(nodes.map(n => n.id));
  const subgraphs = [...used].map(bid => {
    const b = ix.bd.get(bid);
    return {
      id: bid, name: b.name, kindLabel: `Deployment node${b.technology ? ': ' + b.technology : ''}`, style: 'deployment',
      members: nodes.filter(n => n.boundary === bid).map(n => n.id), parent: b.parent && used.has(b.parent) ? b.parent : null,
    };
  });
  return { nodes, subgraphs, edges: mergeEdges(list, 'deployment', ids) };
}

function buildSecurity(ix) {
  const sens = [...ix.rel.values()].filter(r => r.sensitive);
  const list = sens.map(r => {
    const a = cl(ix, r.from), b = cl(ix, r.to);
    return r.data_direction === 'reverse' ? { r, a: b, b: a } : { r, a, b };
  });
  const ids = new Set(list.flatMap(x => [x.a, x.b]));
  const trust = [...ix.bd.values()].filter(b => b.kind === 'trust');
  // A member's descendants sit on the same side of the boundary as the member.
  const holds = (b, id) => chain(ix, id).some(c => (b.members || []).map(m => cl(ix, m)).includes(cl(ix, c)));
  const innermost = new Map();
  for (const id of ids) {
    const holding = trust.filter(b => holds(b, id));
    const inner = holding.find(b => !holding.some(o => o !== b && o.parent === b.id));
    if (inner) innermost.set(id, inner.id);
  }
  const usedB = new Set();
  for (const bid of innermost.values()) { let b = ix.bd.get(bid); while (b && !usedB.has(b.id)) { usedB.add(b.id); b = b.parent ? ix.bd.get(b.parent) : null; } }
  const boundaryChain = (id) => { const out = []; let b = ix.bd.get(innermost.get(id)); while (b) { out.push(b.id); b = b.parent ? ix.bd.get(b.parent) : null; } return out; };
  const nodes = [...ids].map(id => node(ix, id, 'security', innermost.has(id) ? { boundary: innermost.get(id) } : {}));
  const subgraphs = [...usedB].map(bid => {
    const b = ix.bd.get(bid);
    return { id: bid, name: b.name, kindLabel: 'Trust boundary', style: 'trust', rule: b.rule, members: nodes.filter(n => n.boundary === bid).map(n => n.id), parent: b.parent && usedB.has(b.parent) ? b.parent : null };
  });
  const edges = mergeEdges(list, 'security', ids).map(e => {
    const ca = boundaryChain(e.from), cb = boundaryChain(e.to);
    const crossed = [...ca.filter(x => !cb.includes(x)), ...cb.filter(x => !ca.includes(x))].map(x => ix.bd.get(x).name);
    return { ...e, crossed };
  });
  return { nodes, subgraphs, edges };
}

export function buildViews(m) {
  const ix = indexModel(m);
  const specs = viewSpecs(m);
  const byKey = new Map(specs.map(s => [`${s.type}:${s.of ?? ''}`, s.id]));
  return specs.map(spec => {
    let v;
    if (spec.type === 'context') v = buildContext(ix);
    else if (spec.type === 'container') v = buildNested(ix, 'container', ix.sys);
    else if (spec.type === 'component') v = buildNested(ix, 'component', spec.of);
    else if (spec.type === 'dynamic') v = buildDynamic(ix, spec.of);
    else if (spec.type === 'deployment') v = buildDeployment(ix, spec.of);
    else v = buildSecurity(ix);
    const containerView = byKey.get('container:');
    const contextView = byKey.get('context:');
    const parent = spec.type === 'context' ? null
      : spec.type === 'container' ? contextView ?? null
        : containerView ?? contextView ?? null;
    const drill = {};
    if (spec.type === 'context' && containerView) drill[ix.sys] = containerView;
    if (spec.type === 'container') for (const n of v.nodes) { const t = byKey.get(`component:${n.element}`); if (t) drill[n.element] = t; }
    const labels = v.edges.filter(e => e.label.length).length + (v.steps ? v.steps.length : 0);
    const edgeCount = v.edges.length + (v.steps ? v.steps.length : 0);
    const counts = { nodes: v.nodes.length, boundaries: v.subgraphs.length, edges: edgeCount, edge_labels: labels };
    counts.total = counts.nodes + counts.boundaries + counts.edges + counts.edge_labels;
    return {
      id: spec.id, type: spec.type, of: spec.of ?? null, state: flowState(ix, spec), title: titleFor(ix, spec), question: questionFor(ix, spec),
      assertion: spec.assertion ?? null, assertion_src: spec.assertion_src ?? [], parent_view: parent, drill_down: drill,
      direction: spec.type === 'security' ? 'LR' : 'TB', ...v, counts,
      over_cap: counts.nodes > NODE_CAP[spec.type] || counts.total > TOTAL_CAP,
    };
  });
}

export { VIEW_LABEL, VIEWS_PER_TYPE };
