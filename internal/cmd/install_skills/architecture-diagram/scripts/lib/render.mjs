import { buildViews } from './views.mjs';
import { emit, viewText } from './emit.mjs';

export function renderAll(model) {
  const views = buildViews(model);
  for (const v of views) v.text = viewText(v);
  const glossary = model.glossary || [];
  const sources = [];
  for (const v of views) for (const variant of ['canonical', 'miro']) sources.push({ view: v.id, variant, text: emit(v, model, glossary, variant) });
  return { views, sources };
}
