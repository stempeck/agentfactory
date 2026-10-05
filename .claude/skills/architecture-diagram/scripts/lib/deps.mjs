import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, copyFileSync, readFileSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { homedir } from 'node:os';
import { pathToFileURL, fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const MANIFEST_DIR = join(HERE, '..');
export const CACHE = join(process.env.XDG_CACHE_HOME || join(homedir(), '.cache'), 'architecture-diagram');
const DEPS = join(CACHE, 'deps');

// Intersection of the pinned packages' engines: mermaid 12.1.0 >=22.12.0, jsdom 29.1.1 ^22.13.0 || >=24.0.0.
export function nodeSupported(version = process.versions.node) {
  const [maj, min] = version.split('.').map(Number);
  return (maj === 22 && min >= 13) || maj >= 24;
}

export function ensureDeps() {
  if (!nodeSupported()) {
    process.stderr.write(`ENVIRONMENT: node ${process.versions.node} is outside the supported range "^22.13.0 || >=24.0.0"\n`);
    process.exit(3);
  }
  const lockSrc = join(MANIFEST_DIR, 'package-lock.json');
  const lockDst = join(DEPS, 'package-lock.json');
  const installed = existsSync(join(DEPS, 'node_modules', 'mermaid', 'package.json')) && existsSync(join(DEPS, 'node_modules', 'jsdom', 'package.json'));
  if (installed && existsSync(lockDst) && readFileSync(lockDst, 'utf8') === readFileSync(lockSrc, 'utf8')) return DEPS;
  mkdirSync(DEPS, { recursive: true });
  copyFileSync(join(MANIFEST_DIR, 'package.json'), join(DEPS, 'package.json'));
  copyFileSync(lockSrc, lockDst);
  process.stderr.write(`installing pinned validator packages into ${DEPS} (one time, about 225 MB)\n`);
  const r = spawnSync('npm', ['ci', '--ignore-scripts', '--no-audit', '--no-fund', '--loglevel=error'], { cwd: DEPS, encoding: 'utf8' });
  if (r.status !== 0) {
    process.stderr.write(`VALIDATOR_INSTALL: npm ci failed in ${DEPS}\n${r.stderr || ''}${r.error ? r.error.message + '\n' : ''}`);
    process.exit(3);
  }
  return DEPS;
}

let cached = null;

// Mermaid's parser needs DOMPurify, which needs a window; sequence diagrams also touch Option.
export async function loadMermaid() {
  if (cached) return cached;
  const deps = ensureDeps();
  const { JSDOM } = await import(pathToFileURL(join(deps, 'node_modules', 'jsdom', 'lib', 'api.js')).href);
  const dom = new JSDOM('<!doctype html><html><body></body></html>', { pretendToBeVisual: true });
  for (const k of ['window', 'document', 'DOMParser', 'Element', 'HTMLElement', 'Option']) globalThis[k] = k === 'window' ? dom.window : dom.window[k];
  const mermaid = (await import(pathToFileURL(join(deps, 'node_modules', 'mermaid', 'dist', 'mermaid.core.mjs')).href)).default;
  mermaid.initialize({ startOnLoad: false, securityLevel: 'strict' });
  const pkg = JSON.parse(readFileSync(join(deps, 'node_modules', 'mermaid', 'package.json'), 'utf8'));
  cached = { mermaid, version: pkg.version, minPath: join(deps, 'node_modules', 'mermaid', 'dist', 'mermaid.min.js') };
  return cached;
}
