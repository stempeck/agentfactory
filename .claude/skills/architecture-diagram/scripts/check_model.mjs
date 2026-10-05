#!/usr/bin/env node
import { readFileSync, existsSync } from 'node:fs';
import { checkModel } from './lib/check.mjs';

const HELP = `usage: node check_model.mjs <architecture-diagrams/model.json>

Gate 2. Checks the model against the design doc named in meta.source.path and prints JSON
{status, gaps, checks[], views[]} on stdout and one "[n/N] <check> PASS|FAIL" line per check on
stderr. Writes nothing. Exit codes: 0 all checks pass; 1 a check failed; 2 usage or unreadable model.
Each check lists at most 50 errors and the total count.`;

const arg = process.argv[2];
if (!arg || arg === '--help') { process.stdout.write(HELP + '\n'); process.exit(arg ? 0 : 2); }
if (!existsSync(arg)) { process.stderr.write(`not found: ${arg}\n`); process.exit(2); }
let model;
try { model = JSON.parse(readFileSync(arg, 'utf8')); } catch (err) { process.stderr.write(`model.json is not valid JSON: ${err.message}\n`); process.exit(2); }

const result = checkModel(model);
for (const c of result.checks) process.stderr.write(`[${c.n}/${result.checks.length}] ${c.name} ${c.pass ? 'PASS' : 'FAIL'}\n`);
process.stdout.write(JSON.stringify(result, null, 1) + '\n');
process.exit(result.status === 'PASS' ? 0 : 1);
