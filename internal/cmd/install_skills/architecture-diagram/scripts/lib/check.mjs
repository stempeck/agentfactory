import { existsSync } from 'node:fs';
import {
  MODEL_SCHEMA, VIEW_TYPES, VIEWS_PER_TYPE, NODE_CAP, TOTAL_CAP, KINDS, KIND_LABEL, DELTAS, DELTA_WORDS, BOUNDARY_KINDS,
  EXCLUSION_REASONS, DECISIONS, RESERVED_IDS, ACRONYM_ALLOWLIST, BANNED_VERBS,
  readDoc, quoteHolds, isVerbatim, isVerbatimSummary, norm, acronymsIn, deltaWordFound, deltaAbout,
} from './common.mjs';
import { slice } from './sections.mjs';
import { buildViews, indexModel, eligibility } from './views.mjs';

const ID = /^[a-z][a-z0-9_]*$/;
const RESTRICTION = /\b(never|not|no|must|only|cannot|can't|without|outside|refus\w*|forbid\w*)\b/i;
const MAX_ERRORS = 50;

export function checkModel(model) {
  const checks = [];
  const add = (name, errors) => checks.push({ n: checks.length + 1, name, pass: errors.length === 0, errors: errors.slice(0, MAX_ERRORS), error_count: errors.length });
  const arr = (k) => (Array.isArray(model?.[k]) ? model[k] : []);

  // 1. structure
  const e1 = [];
  if (model?.schema !== MODEL_SCHEMA) e1.push(`schema must be "${MODEL_SCHEMA}"`);
  const src = model?.meta?.source;
  if (!src || typeof src.path !== 'string' || !/^[0-9a-f]{64}$/.test(src.sha256 || '') || !Number.isInteger(src.lines) || !Array.isArray(src.body)) e1.push('meta.source needs path, sha256, lines, body');
  if (!['container', 'via', 'none'].includes(model?.meta?.queue_style)) e1.push('meta.queue_style must be "container", "via" or "none"');
  const KNOWN = ['schema', 'meta', 'elements', 'relationships', 'boundaries', 'flows', 'deployments', 'open', 'glossary', 'exclusions', 'inventory', 'arrow_ledger', 'view_decisions', 'views'];
  for (const k of Object.keys(model || {})) if (!KNOWN.includes(k)) e1.push(`unknown top-level key "${k}"`);
  for (const k of ['elements', 'relationships', 'boundaries', 'flows', 'deployments', 'open', 'glossary', 'exclusions', 'inventory', 'arrow_ledger', 'view_decisions', 'views']) {
    if (!Array.isArray(model?.[k])) e1.push(`${k} must be an array`);
  }
  const seen = new Map();
  const idCheck = (where, id) => {
    if (typeof id !== 'string' || !ID.test(id) || id.length > 40) e1.push(`${where}: id "${id}" must match ${ID} and be ≤40 chars`);
    else if (RESERVED_IDS.has(id)) e1.push(`${where}: id "${id}" is reserved by Mermaid`);
    if (seen.has(id)) e1.push(`${where}: id "${id}" already used by ${seen.get(id)}`);
    seen.set(id, where);
  };
  arr('elements').forEach((e, i) => {
    const w = `elements[${i}]`;
    idCheck(w, e.id);
    if (!KINDS.includes(e.kind)) e1.push(`${w}.kind "${e.kind}" not in ${KINDS.join('|')}`);
    if (!DELTAS.includes(e.delta)) e1.push(`${w}.delta "${e.delta}" not in ${DELTAS.join('|')}`);
    if (!Array.isArray(e.src)) e1.push(`${w}.src must be an array`);
    if (e.aliases != null && (!Array.isArray(e.aliases) || e.aliases.some(a => typeof a !== 'string'))) e1.push(`${w}.aliases must be an array of strings`);
  });
  arr('boundaries').forEach((b, i) => { idCheck(`boundaries[${i}]`, b.id); if (!BOUNDARY_KINDS.includes(b.kind)) e1.push(`boundaries[${i}].kind "${b.kind}" not in ${BOUNDARY_KINDS.join('|')}`); });
  arr('relationships').forEach((r, i) => {
    idCheck(`relationships[${i}]`, r.id);
    if (!['sync', 'async'].includes(r.interaction)) e1.push(`relationships[${i}].interaction must be sync|async`);
    if (!DELTAS.includes(r.delta)) e1.push(`relationships[${i}].delta "${r.delta}" not in ${DELTAS.join('|')}`);
    if (typeof r.sensitive !== 'boolean') e1.push(`relationships[${i}].sensitive must be true|false`);
    if (r.data_direction != null && !['forward', 'reverse'].includes(r.data_direction)) e1.push(`relationships[${i}].data_direction must be forward|reverse`);
  });
  arr('flows').forEach((f, i) => { idCheck(`flows[${i}]`, f.id); if (!['proposed', 'current'].includes(f.state)) e1.push(`flows[${i}].state must be proposed|current`); if (!Array.isArray(f.steps) || !f.steps.length) e1.push(`flows[${i}].steps must be a non-empty array`); });
  arr('deployments').forEach((d, i) => idCheck(`deployments[${i}]`, d.id));
  arr('exclusions').forEach((x, i) => { if (!EXCLUSION_REASONS.includes(x.reason)) e1.push(`exclusions[${i}].reason "${x.reason}" not in ${EXCLUSION_REASONS.join('|')}`); });
  arr('views').forEach((v, i) => { if (!VIEW_TYPES.includes(v.type)) e1.push(`views[${i}].type "${v.type}" not in ${VIEW_TYPES.join('|')}`); });
  const sysEl = arr('elements').find(e => e.id === model?.meta?.system_element);
  if (!sysEl) e1.push('meta.system_element must name an element');
  else if (sysEl.kind !== 'software_system') e1.push('meta.system_element must be a software_system');
  add('structure, ids and enums', e1);
  if (e1.length) return finish(checks, false);

  let doc = null;
  const e2 = [];
  if (!existsSync(src.path)) e2.push(`meta.source.path not found: ${src.path}`);
  else {
    doc = readDoc(src.path);
    if (doc.sha256 !== src.sha256) e2.push(`design doc changed since extraction: sha256 ${doc.sha256} != meta.source.sha256 ${src.sha256}`);
    if (doc.lines.length !== src.lines) e2.push(`meta.source.lines ${src.lines} != ${doc.lines.length}`);
    const sl = slice(src.path);
    if (sl.body[0] !== src.body[0] || sl.body[1] !== src.body[1]) e2.push(`meta.source.body [${src.body}] != sections.mjs body [${sl.body}]`);
  }
  add('source document unchanged', e2);
  if (e2.length) return finish(checks, false);

  // 3. citations
  const e3 = [];
  const [bodyA, bodyB] = src.body;
  const cite = (where, list, { required = true } = {}) => {
    if (list == null || (Array.isArray(list) && !list.length)) { if (required) e3.push(`${where}: at least one citation required`); return; }
    if (!Array.isArray(list)) { e3.push(`${where}: must be an array of {lines:[a,b], quote}`); return; }
    list.forEach((c, k) => {
      if (!quoteHolds(doc.lines, c)) e3.push(`${where}[${k}]: quote ${JSON.stringify(String(c?.quote ?? '').slice(0, 80))} is not verbatim in lines ${JSON.stringify(c?.lines)}`);
      else if (c.lines[0] < bodyA || c.lines[1] > bodyB) e3.push(`${where}[${k}]: lines ${c.lines} fall outside the document body ${bodyA}-${bodyB}`);
    });
  };
  arr('elements').forEach((e, i) => {
    const w = `elements[${i}](${e.id})`;
    cite(`${w}.src`, e.src, { required: !e.cluster_of });
    if (e.technology != null) cite(`${w}.technology_src`, e.technology_src);
    if (e.delta !== 'unstated') cite(`${w}.delta_src`, e.delta_src);
  });
  arr('relationships').forEach((r, i) => {
    const w = `relationships[${i}](${r.id})`;
    cite(`${w}.src`, r.src);
    if (r.technology != null) cite(`${w}.technology_src`, r.technology_src);
    if (r.delta !== 'unstated') cite(`${w}.delta_src`, r.delta_src);
  });
  arr('boundaries').forEach((b, i) => cite(`boundaries[${i}](${b.id}).src`, b.src));
  arr('flows').forEach((f, i) => { cite(`flows[${i}](${f.id}).src`, f.src); (f.steps || []).forEach((s, k) => cite(`flows[${i}].steps[${k}].src`, s.src)); });
  arr('deployments').forEach((d, i) => { if (d.environment != null) cite(`deployments[${i}].environment_src`, d.environment_src); (d.placements || []).forEach((p, k) => cite(`deployments[${i}].placements[${k}].src`, p.src)); });
  arr('open').forEach((o, i) => cite(`open[${i}].src`, o.src));
  arr('glossary').forEach((g, i) => { if (g.src !== 'standard') cite(`glossary[${i}](${g.term}).src`, g.src); else if (g.expansion == null) e3.push(`glossary[${i}](${g.term}): a literal name (expansion null) must cite where the doc uses it`); });
  arr('exclusions').forEach((x, i) => cite(`exclusions[${i}].src`, x.src));
  arr('view_decisions').forEach((d, i) => cite(`view_decisions[${i}](${d.type}).evidence`, d.evidence, { required: false }));
  arr('views').forEach((v, i) => { if (v.assertion != null) cite(`views[${i}].assertion_src`, v.assertion_src); });
  add('citations are verbatim and inside the body', e3);

  // 4. displayed text comes from the doc
  const ix = indexModel(model);
  const e4 = [];
  arr('elements').forEach((e, i) => {
    const w = `elements[${i}](${e.id})`;
    if (e.cluster_of) {
      const parentName = e.parent ? ix.el.get(e.parent)?.name : 'the environment';
      const expected = `${e.cluster_of.length} ${KIND_LABEL[e.kind].toLowerCase()}s in ${parentName}`;
      if (e.name !== expected) e4.push(`${w}.name must be exactly "${expected}" for a cluster`);
      if (e.summary != null || e.technology != null) e4.push(`${w}: a cluster has summary null and technology null`);
      return;
    }
    if (typeof e.name !== 'string' || !e.name.trim() || e.name.length > 48) e4.push(`${w}.name must be 1-48 chars`);
    else if (!isVerbatim(e.name, e.src)) e4.push(`${w}.name "${e.name}" is not a verbatim part of its src quotes`);
    if (e.summary != null) {
      if (e.summary.length > 80) e4.push(`${w}.summary longer than 80 chars`);
      else if (!isVerbatimSummary(e.summary, e.src)) e4.push(`${w}.summary is not a verbatim part (optionally cut at a word and ended with …) of its src quotes`);
    }
    if (e.technology != null && !isVerbatim(e.technology, e.technology_src)) e4.push(`${w}.technology "${e.technology}" is not verbatim in technology_src`);
    for (const a of e.aliases || []) if (!isVerbatim(a, e.src)) e4.push(`${w}.aliases "${a}" is not verbatim in its src`);
  });
  arr('relationships').forEach((r, i) => {
    const w = `relationships[${i}](${r.id})`;
    const words = norm(r.verb).split(' ').filter(Boolean);
    if (!words.length || words.length > 6) e4.push(`${w}.verb must be 1-6 words`);
    else if (BANNED_VERBS.has(words[0].toLowerCase())) e4.push(`${w}.verb starts with the vague verb "${words[0]}"; say what is done`);
    if (r.technology != null && !isVerbatim(r.technology, r.technology_src)) e4.push(`${w}.technology "${r.technology}" is not verbatim in technology_src`);
    if (r.sensitive && (r.data == null || !isVerbatim(r.data, r.src))) e4.push(`${w}: a sensitive relationship needs data that is verbatim in its src`);
    if (r.data != null && !r.sensitive && !isVerbatim(r.data, r.src)) e4.push(`${w}.data is not verbatim in its src`);
  });
  arr('boundaries').forEach((b, i) => {
    if (!isVerbatim(b.name, b.src)) e4.push(`boundaries[${i}](${b.id}).name "${b.name}" is not verbatim in its src`);
    if (b.technology != null && !isVerbatim(b.technology, b.src)) e4.push(`boundaries[${i}](${b.id}).technology is not verbatim in its src`);
    if (b.kind === 'trust' && (!b.rule || !isVerbatim(b.rule, b.src) || !RESTRICTION.test(b.rule))) e4.push(`boundaries[${i}](${b.id}): a trust boundary needs rule = the verbatim sentence part saying what must not cross it`);
  });
  arr('flows').forEach((f, i) => {
    if (!isVerbatim(f.name, f.src) || f.name.length > 60) e4.push(`flows[${i}](${f.id}).name must be ≤60 chars and verbatim in its src`);
    (f.steps || []).forEach((s, k) => {
      if (!norm(s.message) || s.message.length > 120 || !isVerbatim(s.message, s.src)) e4.push(`flows[${i}].steps[${k}].message must be 1-120 chars and verbatim in the step's src`);
      if (s.when != null && (!norm(s.when) || s.when.length > 80 || !isVerbatim(s.when, s.src))) e4.push(`flows[${i}].steps[${k}].when must be 1-80 chars and verbatim in the step's src`);
    });
  });
  arr('open').forEach((o, i) => { if (!isVerbatim(o.text, o.src)) e4.push(`open[${i}].text must be verbatim in its src`); });
  arr('exclusions').forEach((x, i) => { if (!isVerbatim(x.what, x.src)) e4.push(`exclusions[${i}].what "${String(x.what).slice(0, 60)}" must be verbatim in its src`); });
  arr('deployments').forEach((d, i) => { if (d.environment != null && !isVerbatim(d.environment, d.environment_src)) e4.push(`deployments[${i}].environment is not verbatim in environment_src`); });
  arr('views').forEach((v, i) => {
    if (v.assertion != null && (v.assertion.length > 200 || !isVerbatim(v.assertion, v.assertion_src))) e4.push(`views[${i}].assertion must be ≤200 chars and verbatim in assertion_src (or null)`);
  });
  add('displayed text is verbatim from the doc', e4);

  // 5. references and hierarchy
  const e5 = [];
  const isEl = (id) => ix.el.has(id);
  arr('elements').forEach((e, i) => {
    const w = `elements[${i}](${e.id})`;
    if (e.parent != null && !isEl(e.parent)) e5.push(`${w}.parent "${e.parent}" is not an element`);
    const pk = e.parent ? ix.el.get(e.parent)?.kind : null;
    if (e.kind === 'component' && pk !== 'container') e5.push(`${w}: a component's parent must be a container`);
    if (['container', 'data_store', 'queue'].includes(e.kind) && e.parent != null && pk !== 'software_system') e5.push(`${w}: parent must be null or a software_system`);
    if (['person', 'external_system', 'software_system'].includes(e.kind) && e.parent != null) e5.push(`${w}: a ${e.kind} has no parent`);
    if (e.cluster_of) {
      if (!Array.isArray(e.cluster_of) || e.cluster_of.length < 2) e5.push(`${w}.cluster_of needs ≥2 members`);
      for (const m of e.cluster_of || []) {
        const me = ix.el.get(m);
        if (!me) e5.push(`${w}.cluster_of member "${m}" is not an element`);
        else if (me.kind !== e.kind || (me.parent ?? null) !== (e.parent ?? null)) e5.push(`${w}.cluster_of member "${m}" must share kind and parent with the cluster`);
        else if (!['existing', 'unstated'].includes(me.delta)) e5.push(`${w}.cluster_of member "${m}" is ${me.delta}; only existing or unstated elements may be clustered`);
      }
    }
  });
  arr('relationships').forEach((r, i) => {
    if (!isEl(r.from) || !isEl(r.to)) e5.push(`relationships[${i}](${r.id}): from/to must be elements`);
    else if (r.from === r.to) e5.push(`relationships[${i}](${r.id}): from equals to`);
  });
  arr('boundaries').forEach((b, i) => {
    for (const m of b.members || []) if (!isEl(m)) e5.push(`boundaries[${i}](${b.id}).members "${m}" is not an element`);
    if (b.parent != null && !ix.bd.has(b.parent)) e5.push(`boundaries[${i}](${b.id}).parent is not a boundary`);
  });
  const chainOf = (id) => { const out = []; let c = ix.el.get(id); while (c && !out.includes(c.id)) { out.push(c.id); c = c.parent ? ix.el.get(c.parent) : null; } return out; };
  const related = (x, y) => chainOf(x).includes(y) || chainOf(y).includes(x);
  arr('flows').forEach((f, i) => (f.steps || []).forEach((s, k) => {
    const w = `flows[${i}](${f.id}).steps[${k}]`;
    if (!isEl(s.from) || !isEl(s.to)) { e5.push(`${w}: from/to must be elements`); return; }
    if (!['sync', 'async', 'reply'].includes(s.interaction)) e5.push(`${w}.interaction must be sync|async|reply`);
    if (s.interaction !== 'reply' && s.from !== s.to && ![...ix.rel.values()].some(r => related(r.from, s.from) && related(r.to, s.to))) e5.push(`${w}: no relationship backs ${s.from} -> ${s.to}`);
  }));
  arr('deployments').forEach((d, i) => (d.placements || []).forEach((p, k) => {
    if (!isEl(p.element)) e5.push(`deployments[${i}].placements[${k}].element is not an element`);
    if (ix.bd.get(p.node)?.kind !== 'deployment_node') e5.push(`deployments[${i}].placements[${k}].node must be a deployment_node boundary`);
  }));
  arr('open').forEach((o, i) => (o.element_ids || []).forEach(id => { if (!isEl(id)) e5.push(`open[${i}].element_ids "${id}" is not an element`); }));
  arr('exclusions').forEach((x, i) => (x.ids || []).forEach(id => { if (!isEl(id) && !ix.rel.has(id) && !ix.flow.has(id) && !ix.dep.has(id)) e5.push(`exclusions[${i}].ids "${id}" is not an element, relationship, flow or deployment`); }));
  arr('views').forEach((v, i) => {
    const w = `views[${i}](${v.type})`;
    if (v.type === 'component' && ix.el.get(v.of)?.kind !== 'container') e5.push(`${w}.of must be a container`);
    else if (v.type === 'dynamic' && !ix.flow.has(v.of)) e5.push(`${w}.of must be a flow`);
    else if (v.type === 'deployment' && !ix.dep.has(v.of)) e5.push(`${w}.of must be a deployment`);
    else if (['context', 'container', 'security'].includes(v.type) && v.of != null) e5.push(`${w}.of must be null`);
  });
  add('references and hierarchy', e5);

  // 6. delta words
  const e6 = [];
  const deltaOk = (where, obj, about) => {
    if (obj.delta === 'unstated') return;
    const quotes = (obj.delta_src || []).map(c => norm(c.quote));
    if (!quotes.some(q => deltaWordFound(obj.delta, q))) e6.push(`${where}: delta "${obj.delta}" but no delta_src quote holds an un-negated ${obj.delta} word (${DELTA_WORDS[obj.delta].source})`);
    else if (!quotes.some(q => deltaAbout(obj.delta, q, about))) e6.push(`${where}: the ${obj.delta} word and the element (${about.map(a => `"${a.name}"`).join(' or ')}, an alias, or a doc_id) must sit in the same sentence, semicolon clause or table cell of the delta_src quote; otherwise use "unstated"`);
  };
  arr('elements').forEach((e, i) => deltaOk(`elements[${i}](${e.id})`, e, [e]));
  arr('relationships').forEach((r, i) => deltaOk(`relationships[${i}](${r.id})`, r, [ix.el.get(r.from), ix.el.get(r.to)].filter(Boolean)));
  add('delta comes from explicit words', e6);

  // 7, 8. coverage
  const sl = slice(src.path);
  const exclusionAt = (k) => Number.isInteger(k) && k >= 0 && k < arr('exclusions').length;
  const covers = (cites, line) => (cites || []).some(c => Array.isArray(c?.lines) && c.lines[0] <= line && line <= c.lines[1]);
  const e7 = [];
  const inv = new Map(arr('inventory').map(r => [r.line, r]));
  for (const item of sl.component_items) {
    const r = inv.get(item.line);
    if (!r) { e7.push(`component item at line ${item.line} has no inventory record: ${item.text.slice(0, 70)}`); continue; }
    if (Array.isArray(r.element_ids) && r.element_ids.length) {
      for (const id of r.element_ids) {
        if (!isEl(id)) e7.push(`inventory line ${item.line}: "${id}" is not an element`);
        else if (!covers(ix.el.get(id).src, item.line)) e7.push(`inventory line ${item.line}: element "${id}" does not cite line ${item.line} in its src`);
      }
    } else if (exclusionAt(r.exclusion)) {
      if (!covers(arr('exclusions')[r.exclusion].src, item.line)) e7.push(`inventory line ${item.line}: exclusions[${r.exclusion}] does not cite line ${item.line}`);
    } else e7.push(`inventory line ${item.line}: needs element_ids or exclusion = index into exclusions`);
  }
  add(`every component item is drawn or excluded (${sl.component_items.length} items)`, e7);
  const e8 = [];
  const led = new Map(arr('arrow_ledger').map(r => [r.line, r]));
  for (const a of sl.arrow_lines) {
    const r = led.get(a.line);
    if (!r) { e8.push(`arrow line ${a.line} has no arrow_ledger record: ${a.text.slice(0, 70)}`); continue; }
    if (Array.isArray(r.relationship_ids) && r.relationship_ids.length) {
      for (const id of r.relationship_ids) {
        if (!ix.rel.has(id)) e8.push(`arrow_ledger line ${a.line}: "${id}" is not a relationship`);
        else if (!covers(ix.rel.get(id).src, a.line)) e8.push(`arrow_ledger line ${a.line}: relationship "${id}" does not cite line ${a.line} in its src`);
      }
    } else if (Array.isArray(r.flow_ids) && r.flow_ids.length) {
      for (const id of r.flow_ids) {
        const f = ix.flow.get(id);
        if (!f) e8.push(`arrow_ledger line ${a.line}: "${id}" is not a flow`);
        else if (!covers(f.src, a.line) && !(f.steps || []).some(s => covers(s.src, a.line))) e8.push(`arrow_ledger line ${a.line}: flow "${id}" does not cite line ${a.line} in its src or a step's src`);
      }
    } else if (exclusionAt(r.exclusion)) {
      if (!covers(arr('exclusions')[r.exclusion].src, a.line)) e8.push(`arrow_ledger line ${a.line}: exclusions[${r.exclusion}] does not cite line ${a.line}`);
    } else e8.push(`arrow_ledger line ${a.line}: needs relationship_ids, flow_ids, or exclusion = index into exclusions`);
  }
  add(`every stated arrow is drawn or excluded (${sl.arrow_lines.length} lines)`, e8);

  // 9. view decisions follow mechanically from the model
  const e9 = [];
  const decisions = arr('view_decisions');
  const el9 = eligibility(model);
  const viewLimitIds = new Set(arr('exclusions').filter(x => x.reason === 'view-limit').flatMap(x => x.ids || []));
  const expected = {
    context: el9.context ? [null] : [],
    container: el9.container === 'produce' ? [null] : [],
    component: el9.component.slice(0, VIEWS_PER_TYPE.component),
    dynamic: el9.dynamic.slice(0, VIEWS_PER_TYPE.dynamic),
    deployment: el9.deployment.slice(0, VIEWS_PER_TYPE.deployment),
    security: el9.security ? [null] : [],
  };
  const why = {
    context: 'context is produced exactly when a relationship connects the system to an outside element',
    container: 'container is produced when the system has ≥2 child containers, data stores or queues; skipped with exactly 1; not-produced with 0',
    component: 'component views go to containers with ≥1 new/changed/removed component or ≥2 components, ranked by changed count then component count (top 3)',
    dynamic: 'dynamic views go to flows ranked proposed first, then by new/changed participants, then by step count (top 3)',
    deployment: 'deployment views go to deployments placing elements on ≥2 nodes, ranked by placement count (top 2)',
    security: 'security is produced exactly when a trust boundary with a rule exists and a sensitive relationship touches one of its members or their children',
  };
  for (const t of VIEW_TYPES) {
    const ds = decisions.filter(d => d.type === t);
    const vs = arr('views').filter(v => v.type === t).map(v => v.of ?? null);
    if (ds.length !== 1) { e9.push(`view_decisions needs exactly one record for ${t} (found ${ds.length})`); continue; }
    const d = ds[0];
    const exp = expected[t];
    const wantDecision = exp.length ? 'produced' : (t === 'container' && el9.container === 'skip') ? 'skipped' : (t === 'context' || t === 'container') ? 'not-produced' : 'skipped';
    if (!DECISIONS.includes(d.decision)) e9.push(`${t}: decision must be ${DECISIONS.join('|')}`);
    else if (d.decision !== wantDecision) e9.push(`${t}: decision must be "${wantDecision}" for this model (${why[t]})`);
    if (d.decision !== 'produced' && !norm(d.reason)) e9.push(`${t}: a ${d.decision} decision needs a reason`);
    if (d.decision !== 'produced' && !(d.evidence || []).length && !(d.headings_searched || []).length) e9.push(`${t}: a ${d.decision} decision needs evidence or headings_searched`);
    const sameSet = vs.length === exp.length && exp.every(x => vs.includes(x));
    if (!sameSet) e9.push(`${t}: views must be exactly ${exp.length ? JSON.stringify(exp.map(x => x ?? 'null')) : 'none'}${vs.length ? `, found ${JSON.stringify(vs.map(x => x ?? 'null'))}` : ''} (${why[t]})`);
    const beyond = { component: el9.component, dynamic: el9.dynamic, deployment: el9.deployment }[t]?.slice(VIEWS_PER_TYPE[t]) || [];
    for (const id of beyond) if (!viewLimitIds.has(id)) e9.push(`${t}: "${id}" qualifies for a view beyond the limit; record an exclusion {reason: "view-limit", ids: ["${id}"]}`);
  }
  add('view decisions', e9);

  // 10. caps
  const e10 = [];
  let views = [];
  try { views = buildViews(model); } catch (err) { e10.push(`views could not be built: ${err.message}`); }
  for (const v of views) {
    if (!v.nodes.length) e10.push(`${v.id}: empty view`);
    if (v.type === 'container' && !v.subgraphs[0]?.members.length) e10.push(`${v.id}: the system has no child containers`);
    if (v.over_cap) e10.push(`${v.id}: ${v.counts.nodes} nodes (cap ${NODE_CAP[v.type]}), total ${v.counts.total} (cap ${TOTAL_CAP}); cluster existing elements with cluster_of or HALT`);
  }
  add('views are within caps', e10);

  // 11. nothing in the model is silently left out of every view
  const e11 = [];
  const drawnEl = new Set(), drawnRel = new Set();
  for (const v of views) {
    for (const n of v.nodes) drawnEl.add(n.element);
    for (const s of v.subgraphs) if (ix.el.has(s.id)) drawnEl.add(s.id);
    for (const e of v.edges) for (const r of e.relationships) drawnRel.add(r);
  }
  for (const e of arr('elements')) if (e.cluster_of && drawnEl.has(e.id)) e.cluster_of.forEach(m => drawnEl.add(m));
  const notInView = new Set(arr('exclusions').filter(x => x.reason === 'not-in-any-view').flatMap(x => x.ids || []));
  for (const e of arr('elements')) if (!drawnEl.has(e.id) && !notInView.has(e.id)) e11.push(`element "${e.id}" appears in no view; add an exclusion {what, reason: "not-in-any-view", ids: ["${e.id}"], src} or connect it with a stated relationship`);
  for (const r of arr('relationships')) if (!drawnRel.has(r.id) && !notInView.has(r.id)) e11.push(`relationship "${r.id}" appears in no view; add an exclusion {what, reason: "not-in-any-view", ids: ["${r.id}"], src}`);
  add('every modelled element and relationship is drawn or excluded', e11);

  // 12. acronyms
  const e12 = [];
  const gl = new Set(arr('glossary').map(g => g.term));
  const docIds = new Set(arr('elements').flatMap(e => e.doc_ids || []));
  const prefixes = [...new Set([...docIds].map(id => (/^([A-Z]+)-?\d/.exec(id) || [])[1]).filter(Boolean))];
  const docIdLike = (a) => docIds.has(a) || prefixes.some(p => new RegExp(`^${p}-?\\d+[a-z]?$`).test(a));
  for (const v of views) {
    const places = [
      ...v.nodes.map(n => [`node ${n.id}`, n.label.join(' ')]),
      ...v.edges.map(e => [`edge ${e.from} -> ${e.to}`, e.label.join(' ')]),
      ...v.subgraphs.map(s => [`boundary ${s.id}`, s.name]),
      ...(v.steps || []).map((s, k) => [`step ${k + 1} message`, s.message]),
    ];
    for (const [where, text] of places) {
      for (const a of acronymsIn(text)) {
        if (ACRONYM_ALLOWLIST.has(a) || gl.has(a)) continue;
        if (docIdLike(a)) e12.push(`${v.id} ${where}: "${a}" looks like a design-doc id; labels never show doc ids, so choose a quote without it`);
        else e12.push(`${v.id} ${where}: "${a}" needs a glossary entry whose term is exactly "${a}": {term, expansion, src} (src may be "standard"), or expansion null with a citation when it is a literal name such as a file name`);
      }
    }
  }
  add('acronyms are expanded', [...new Set(e12)]);

  const gaps = decisions.some(d => d.decision === 'not-produced');
  return finish(checks, true, gaps, views);
}

function finish(checks, complete, gaps = false, views = []) {
  const pass = complete && checks.every(c => c.pass);
  return { status: pass ? 'PASS' : 'FAIL', gaps, checks, views: views.map(v => ({ id: v.id, title: v.title, counts: v.counts })) };
}
