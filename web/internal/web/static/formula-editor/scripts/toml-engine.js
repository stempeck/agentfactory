/* toml-engine.js — the authoring engine behind the Formula Editor prototype.
 *
 * Architecture (binding, per iteration-1 feedback): the TOML *text* is the single
 * source of truth. This engine parses that text into a model that remembers the
 * exact source span of every table, key, and value. Every visual edit is expressed
 * as a minimal text patch against those spans, then the text is re-parsed. Comments
 * and formatting outside the edited span survive by construction — that is the
 * comment-preserving round-trip the feedback demands, and it is why this parser is
 * hand-built rather than vendored: no off-the-shelf JS TOML library records source
 * spans, and without spans a "save" would re-serialize (and re-format) the whole file.
 *
 * Coverage: the TOML 1.0 constructs the 28 store formulas actually use — bare/quoted/
 * dotted keys, all four string kinds with full escape semantics, integers, floats,
 * booleans, (multiline) arrays, inline tables, [table] and [[array-of-table]] headers,
 * comments. Dates and times are rejected with a clear error.
 *
 * Validation mirrors internal/formula/validate.go: parse, formula field required,
 * type valid (with content inference like parser.go inferType), unique step/leg ids,
 * needs resolve, DFS cycle detection, vars source whitelist, input/var collision,
 * skill-name rules.
 *
 * Runs in the browser (window.TomlEngine) and in node (module.exports) so the
 * round-trip contract is testable headlessly against the real store formulas.
 */
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.TomlEngine = factory();
})(typeof self !== 'undefined' ? self : this, function () {
  'use strict';

  /* ============================ parser ============================ */

  function ParseError(message, pos) {
    var e = new Error(message);
    e.name = 'ParseError';
    e.pos = pos;
    return e;
  }

  var BARE_KEY = /[A-Za-z0-9_-]/;

  function parse(text) {
    var i = 0;
    var n = text.length;
    var rootKvs = [];
    var headers = [];   // {kind:'table'|'array', keyPath, headerStart, headerEnd, kvs, blockStart, blockEnd}
    var current = null; // header block receiving kvs, or null for root
    var definedTables = Object.create(null); // path -> 'table'|'array'|'implicit'
    var lineStarts = null;

    function err(msg, at) { throw ParseError(msg, at === undefined ? i : at); }

    function skipWs() { while (i < n && (text[i] === ' ' || text[i] === '\t')) i++; }

    function skipComment() {
      if (text[i] === '#') { while (i < n && text[i] !== '\n') i++; }
    }

    function expectLineEnd() {
      skipWs();
      skipComment();
      if (i >= n) return;
      if (text[i] === '\n') { i++; return; }
      if (text[i] === '\r' && text[i + 1] === '\n') { i += 2; return; }
      err('expected end of line, found ' + JSON.stringify(text[i]));
    }

    function parseKeyPart() {
      var c = text[i];
      if (c === '"' || c === "'") return parseString().value;
      var s = i;
      while (i < n && BARE_KEY.test(text[i])) i++;
      if (i === s) err('expected a key');
      return text.slice(s, i);
    }

    function parseKeyPath() {
      var parts = [parseKeyPart()];
      skipWs();
      while (text[i] === '.') {
        i++; skipWs();
        parts.push(parseKeyPart());
        skipWs();
      }
      return parts;
    }

    var ESC = { b: '\b', t: '\t', n: '\n', f: '\f', r: '\r', '"': '"', '\\': '\\' };

    function parseString() {
      var start = i;
      var q = text[i];
      var triple = text.substr(i, 3) === q + q + q;
      var out = '';
      if (triple) {
        i += 3;
        // a newline immediately after the opening delimiter is trimmed
        if (text[i] === '\r' && text[i + 1] === '\n') i += 2;
        else if (text[i] === '\n') i++;
        for (;;) {
          if (i >= n) err('unterminated multiline string starting at offset ' + start, start);
          if (text[i] === q && text[i + 1] === q && text[i + 2] === q) {
            // up to two extra quotes belong to the content
            var extra = 0;
            while (extra < 2 && text[i + 3 + extra] === q) extra++;
            out += q.repeat(extra);
            i += 3 + extra;
            break;
          }
          if (q === '"' && text[i] === '\\') {
            var r = readEscape(true);
            if (r !== null) out += r;
          } else {
            out += text[i]; i++;
          }
        }
      } else {
        i++;
        for (;;) {
          if (i >= n || text[i] === '\n') err('unterminated string starting at offset ' + start, start);
          if (text[i] === q) { i++; break; }
          if (q === '"' && text[i] === '\\') {
            var r2 = readEscape(false);
            if (r2 !== null) out += r2;
          } else {
            out += text[i]; i++;
          }
        }
      }
      return {
        kind: 'string', value: out, start: start, end: i,
        strKind: (q === '"' ? (triple ? 'ml-basic' : 'basic') : (triple ? 'ml-literal' : 'literal'))
      };
    }

    function readEscape(multiline) {
      // cursor sits on the backslash
      i++;
      var c = text[i];
      if (multiline && (c === '\n' || c === '\r' || c === ' ' || c === '\t')) {
        // line-ending backslash: must be followed only by whitespace up to a newline,
        // then trims all following whitespace/newlines
        var j = i;
        while (j < n && (text[j] === ' ' || text[j] === '\t')) j++;
        if (text[j] === '\r') j++;
        if (text[j] !== '\n') err('invalid escape "\\' + c + '" in string');
        j++;
        while (j < n && (text[j] === ' ' || text[j] === '\t' || text[j] === '\n' || text[j] === '\r')) j++;
        i = j;
        return null;
      }
      if (c === 'u' || c === 'U') {
        var len = c === 'u' ? 4 : 8;
        var hex = text.substr(i + 1, len);
        if (!new RegExp('^[0-9A-Fa-f]{' + len + '}$').test(hex)) err('invalid unicode escape');
        i += 1 + len;
        return String.fromCodePoint(parseInt(hex, 16));
      }
      if (ESC[c] !== undefined) { i++; return ESC[c]; }
      err('invalid escape "\\' + c + '" in string');
    }

    function parseValue() {
      skipWs();
      var start = i;
      var c = text[i];
      if (c === '"' || c === "'") return parseString();
      if (c === '[') return parseArray();
      if (c === '{') return parseInlineTable();
      if (text.startsWith('true', i)) { i += 4; return { kind: 'bool', value: true, start: start, end: i }; }
      if (text.startsWith('false', i)) { i += 5; return { kind: 'bool', value: false, start: start, end: i }; }
      // number (or the unsupported date/time)
      var s = i;
      while (i < n && /[0-9A-Za-z_+.:\-]/.test(text[i])) i++;
      var raw = text.slice(s, i);
      if (raw === '') err('expected a value');
      if (/[T:]/.test(raw) || /^\d{4}-\d{2}-\d{2}/.test(raw)) {
        err('date/time values are not supported by this editor (offset ' + s + ')', s);
      }
      if (/^[+-]?(inf|nan)$/.test(raw)) {
        return { kind: 'float', value: raw.endsWith('inf') ? (raw[0] === '-' ? -Infinity : Infinity) : NaN, start: s, end: i };
      }
      var cleaned = raw.replace(/_/g, '');
      var num;
      if (/^[+-]?0x[0-9A-Fa-f]+$/.test(cleaned)) num = parseInt(cleaned, 16);
      else if (/^[+-]?0o[0-7]+$/.test(cleaned)) num = parseInt(cleaned.replace('0o', ''), 8);
      else if (/^[+-]?0b[01]+$/.test(cleaned)) num = parseInt(cleaned.replace('0b', ''), 2);
      else if (/^[+-]?\d+$/.test(cleaned)) num = parseInt(cleaned, 10);
      else if (/^[+-]?(\d+\.\d+([eE][+-]?\d+)?|\d+[eE][+-]?\d+)$/.test(cleaned)) num = parseFloat(cleaned);
      else err('invalid value ' + JSON.stringify(raw), s);
      var isFloat = /[.eE]/.test(cleaned) && !/^[+-]?0x/.test(cleaned);
      return { kind: isFloat ? 'float' : 'int', value: num, start: s, end: i };
    }

    function parseArray() {
      var start = i;
      i++; // [
      var items = [];
      for (;;) {
        skipArrayFiller();
        if (i >= n) err('unterminated array starting at offset ' + start, start);
        if (text[i] === ']') { i++; break; }
        items.push(parseValue());
        skipArrayFiller();
        if (text[i] === ',') { i++; continue; }
        if (text[i] === ']') { i++; break; }
        err('expected "," or "]" in array');
      }
      return { kind: 'array', items: items, start: start, end: i };
    }

    function skipArrayFiller() {
      for (;;) {
        skipWs();
        if (text[i] === '#') { skipComment(); continue; }
        if (text[i] === '\n') { i++; continue; }
        if (text[i] === '\r' && text[i + 1] === '\n') { i += 2; continue; }
        break;
      }
    }

    function parseInlineTable() {
      var start = i;
      i++; // {
      var kvs = [];
      skipWs();
      if (text[i] === '}') { i++; return { kind: 'inline-table', kvs: kvs, start: start, end: i }; }
      for (;;) {
        skipWs();
        var kStart = i;
        var keyPath = parseKeyPath();
        skipWs();
        if (text[i] !== '=') err('expected "=" in inline table');
        i++;
        var v = parseValue();
        kvs.push({ keyPath: keyPath, keyStart: kStart, value: v });
        skipWs();
        if (text[i] === ',') { i++; continue; }
        if (text[i] === '}') { i++; break; }
        err('expected "," or "}" in inline table');
      }
      return { kind: 'inline-table', kvs: kvs, start: start, end: i };
    }

    function lineStartBefore(pos) {
      var j = pos;
      while (j > 0 && text[j - 1] !== '\n') j--;
      return j;
    }

    // main loop
    while (i < n) {
      skipWs();
      if (i >= n) break;
      var c = text[i];
      if (c === '\n') { i++; continue; }
      if (c === '\r' && text[i + 1] === '\n') { i += 2; continue; }
      if (c === '#') { skipComment(); continue; }
      if (c === '[') {
        var headerStart = lineStartBefore(i);
        var isArray = text[i + 1] === '[';
        i += isArray ? 2 : 1;
        skipWs();
        var kp = parseKeyPath();
        skipWs();
        if (isArray) {
          if (text[i] !== ']' || text[i + 1] !== ']') err('expected "]]"');
          i += 2;
        } else {
          if (text[i] !== ']') err('expected "]"');
          i++;
        }
        var headerEnd = i;
        expectLineEnd();
        var pathKey = kp.join(' ');
        var prior = definedTables[pathKey];
        if (isArray) {
          if (prior && prior !== 'array') err('table [' + kp.join('.') + '] redefined as array of tables', headerStart);
          definedTables[pathKey] = 'array';
        } else {
          if (prior === 'table') err('table [' + kp.join('.') + '] defined twice', headerStart);
          if (prior === 'array') err('array of tables [[' + kp.join('.') + ']] redefined as table', headerStart);
          definedTables[pathKey] = 'table';
        }
        if (current) current.blockEnd = headerStart;
        current = {
          kind: isArray ? 'array' : 'table', keyPath: kp,
          headerStart: headerStart, headerEnd: headerEnd,
          kvs: [], blockStart: headerStart, blockEnd: n
        };
        headers.push(current);
        continue;
      }
      // key-value
      var kvStart = i;
      var keyPath2 = parseKeyPath();
      skipWs();
      if (text[i] !== '=') err('expected "=" after key ' + keyPath2.join('.'));
      i++;
      var val = parseValue();
      var kv = { keyPath: keyPath2, span: { start: kvStart, end: val.end }, value: val };
      (current ? current.kvs : rootKvs).push(kv);
      expectLineEnd();
    }
    if (current) current.blockEnd = n;

    var doc = { text: text, rootKvs: rootKvs, headers: headers };
    doc.js = toJS(doc);
    return doc;
  }

  /* -------- semantic tree (matches what tomllib/the Go engine would see) -------- */

  function toJS(doc) {
    var out = {};
    function setPath(obj, path, value, what) {
      var o = obj;
      for (var k = 0; k < path.length - 1; k++) {
        var key = path[k];
        if (o[key] === undefined) o[key] = {};
        if (Array.isArray(o[key])) o = o[key][o[key].length - 1];
        else o = o[key];
      }
      var last = path[path.length - 1];
      if (what === 'kv' && Object.prototype.hasOwnProperty.call(o, last)) {
        throw ParseError('duplicate key: ' + path.join('.'));
      }
      o[last] = value;
      return o;
    }
    function valueOf(v) {
      if (v.kind === 'array') return v.items.map(valueOf);
      if (v.kind === 'inline-table') {
        var t = {};
        v.kvs.forEach(function (kv) {
          var o = t;
          for (var k = 0; k < kv.keyPath.length - 1; k++) {
            if (o[kv.keyPath[k]] === undefined) o[kv.keyPath[k]] = {};
            o = o[kv.keyPath[k]];
          }
          o[kv.keyPath[kv.keyPath.length - 1]] = valueOf(kv.value);
        });
        return t;
      }
      return v.value;
    }
    doc.rootKvs.forEach(function (kv) { setPath(out, kv.keyPath, valueOf(kv.value), 'kv'); });
    doc.headers.forEach(function (h) {
      var container;
      if (h.kind === 'array') {
        // append a fresh element to the array at keyPath
        var o = out;
        for (var k = 0; k < h.keyPath.length - 1; k++) {
          var key = h.keyPath[k];
          if (o[key] === undefined) o[key] = {};
          if (Array.isArray(o[key])) o = o[key][o[key].length - 1];
          else o = o[key];
        }
        var last = h.keyPath[h.keyPath.length - 1];
        if (o[last] === undefined) o[last] = [];
        container = {};
        o[last].push(container);
      } else {
        container = setPath(out, h.keyPath.concat(), {}, 'table');
        container = (function () {
          var o2 = out;
          for (var k2 = 0; k2 < h.keyPath.length; k2++) {
            o2 = Array.isArray(o2[h.keyPath[k2]]) ? o2[h.keyPath[k2]][o2[h.keyPath[k2]].length - 1] : o2[h.keyPath[k2]];
          }
          return o2;
        })();
      }
      h.kvs.forEach(function (kv) { setPath(container, kv.keyPath, valueOf(kv.value), 'kv'); });
      h.container = container;
    });
    return out;
  }

  /* ============================ serializer helpers ============================ */

  function escCtrl(m) {
    return '\\u' + m.charCodeAt(0).toString(16).toUpperCase().padStart(4, '0');
  }
  /* control chars that must be \u-escaped (tab is legal literally in every
     string kind; newline is legal in multiline strings and handled separately) */
  var CTRL_BASIC = new RegExp('[\\x00-\\x08\\x0b-\\x1f\\x7f]', 'g');
  var CTRL_ML = new RegExp('[\\x00-\\x08\\x0b\\x0c\\x0e-\\x1f\\x7f]', 'g');

  function escBasic(s) {
    return s.replace(/[\\"]/g, function (m) { return '\\' + m; })
            .replace(/\n/g, '\\n')
            .replace(CTRL_BASIC, escCtrl);
  }

  function escMlBasic(s) {
    // escape backslashes and control chars; break any run of 3+ quotes
    var out = s.replace(/\\/g, '\\\\')
               .replace(/\r(?!\n)/g, '\\r')
               .replace(CTRL_ML, escCtrl)
               .replace(/"""/g, '""\\"');
    // a trailing quote would merge with the closing delimiter
    if (out.endsWith('"')) out = out.slice(0, -1) + '\\"';
    return out;
  }

  function fmtString(s) {
    if (s.indexOf('\n') !== -1) return '"""\n' + escMlBasic(s) + '"""';
    return '"' + escBasic(s) + '"';
  }

  function fmtStringArray(arr) {
    return '[' + arr.map(fmtString).join(', ') + ']';
  }

  function fmtGate(g) {
    var parts = ['type = ' + fmtString(g.type || 'human')];
    if (g.id) parts.push('id = ' + fmtString(g.id));
    if (g.timeout) parts.push('timeout = ' + fmtString(g.timeout));
    return '{ ' + parts.join(', ') + ' }';
  }

  function fmtValue(v) {
    if (typeof v === 'string') return fmtString(v);
    if (typeof v === 'boolean') return v ? 'true' : 'false';
    if (typeof v === 'number') return String(v);
    if (Array.isArray(v)) return '[' + v.map(fmtValue).join(', ') + ']';
    if (v && typeof v === 'object') {
      return '{ ' + Object.keys(v).map(function (k) { return k + ' = ' + fmtValue(v[k]); }).join(', ') + ' }';
    }
    throw new Error('cannot format value: ' + v);
  }

  /* ============================ patch operations ============================ */
  /* Every op takes (text, ...) and returns the new full text. Callers re-parse. */

  function splice(text, start, end, insert) {
    return text.slice(0, start) + insert + text.slice(end);
  }

  function lineBoundsOf(text, span) {
    var s = span.start;
    while (s > 0 && text[s - 1] !== '\n') s--;
    var e = span.end;
    while (e < text.length && text[e] !== '\n') e++;
    if (e < text.length) e++; // include the newline
    return { start: s, end: e };
  }

  function findHeader(doc, kind, keyPath, index) {
    var found = -1;
    for (var h = 0; h < doc.headers.length; h++) {
      var hd = doc.headers[h];
      if (hd.kind === kind && hd.keyPath.join('.') === keyPath.join('.')) {
        found++;
        if (index === undefined || found === index) return hd;
      }
    }
    return null;
  }

  function findKv(kvs, key) {
    for (var k = 0; k < kvs.length; k++) {
      if (kvs[k].keyPath.length === 1 && kvs[k].keyPath[0] === key) return kvs[k];
    }
    return null;
  }

  // Set (or insert, or with value===null remove) a single-key kv inside a block.
  // block===null targets the root region (before the first header).
  function setKV(text, doc, block, key, rawValue) {
    var kvs = block ? block.kvs : doc.rootKvs;
    var kv = findKv(kvs, key);
    if (kv) {
      if (rawValue === null) {
        var lb = lineBoundsOf(text, kv.span);
        return splice(text, lb.start, lb.end, '');
      }
      return splice(text, kv.value.start, kv.value.end, rawValue);
    }
    if (rawValue === null) return text;
    var insertAt;
    if (kvs.length) {
      var lastKv = kvs[kvs.length - 1];
      insertAt = lineBoundsOf(text, lastKv.span).end;
    } else if (block) {
      insertAt = lineBoundsOf(text, { start: block.headerStart, end: block.headerEnd }).end;
    } else {
      insertAt = 0;
    }
    return splice(text, insertAt, insertAt, key + ' = ' + rawValue + '\n');
  }

  // ---- unit-level ops. A "unit" is one [[steps]] / [[legs]] / [[template]] /
  // [[aspects]] block — the editor passes the unit key for the formula's type,
  // so the same ops drive workflows, convoys, and expansions. ----

  function stepBlocks(doc, unitKey) {
    var key = unitKey || 'steps';
    return doc.headers.filter(function (h) {
      return h.kind === 'array' && h.keyPath.length === 1 && h.keyPath[0] === key;
    });
  }

  function stepBlockById(doc, id, unitKey) {
    var blocks = stepBlocks(doc, unitKey);
    for (var b = 0; b < blocks.length; b++) {
      var kv = findKv(blocks[b].kvs, 'id');
      if (kv && kv.value.value === id) return blocks[b];
    }
    return null;
  }

  function setStepField(text, doc, stepId, key, rawValue, unitKey) {
    var block = stepBlockById(doc, stepId, unitKey);
    if (!block) throw new Error('no unit with id ' + JSON.stringify(stepId));
    return setKV(text, doc, block, key, rawValue);
  }

  // Rename a step id and rewire every needs list that references it.
  // Applies patches back-to-front so spans stay valid within one pass.
  function renameStep(text, doc, oldId, newId, unitKey) {
    var patches = [];
    var block = stepBlockById(doc, oldId, unitKey);
    if (!block) throw new Error('no unit with id ' + JSON.stringify(oldId));
    var idKv = findKv(block.kvs, 'id');
    patches.push({ start: idKv.value.start, end: idKv.value.end, ins: fmtString(newId) });
    stepBlocks(doc, unitKey).forEach(function (b) {
      var needsKv = findKv(b.kvs, 'needs');
      if (!needsKv || needsKv.value.kind !== 'array') return;
      needsKv.value.items.forEach(function (item) {
        if (item.kind === 'string' && item.value === oldId) {
          patches.push({ start: item.start, end: item.end, ins: fmtString(newId) });
        }
      });
    });
    // convoy legs are also referenced from [synthesis] depends_on
    var synth = findHeader(doc, 'table', ['synthesis']);
    if (synth) {
      var depKv = findKv(synth.kvs, 'depends_on');
      if (depKv && depKv.value.kind === 'array') {
        depKv.value.items.forEach(function (item) {
          if (item.kind === 'string' && item.value === oldId) {
            patches.push({ start: item.start, end: item.end, ins: fmtString(newId) });
          }
        });
      }
    }
    patches.sort(function (a, b) { return b.start - a.start; });
    patches.forEach(function (p) { text = splice(text, p.start, p.end, p.ins); });
    return text;
  }

  // Insert a block at `at`, adding a blank-line separator only when the text
  // before the insertion point doesn't already provide one. Keeping the
  // separator OUTSIDE the block means add+remove restores the original bytes.
  function insertBlock(text, at, block) {
    var needSep = at > 0 && !(text[at - 1] === '\n' && (at < 2 || text[at - 2] === '\n'));
    if (at > 0 && text[at - 1] !== '\n') block = '\n' + block;
    else if (needSep) block = '\n' + block;
    return splice(text, at, at, block);
  }

  function buildStepText(step, unitKey) {
    var out = '[[' + (unitKey || 'steps') + ']]\n';
    out += 'id = ' + fmtString(step.id) + '\n';
    if (step.title) out += 'title = ' + fmtString(step.title) + '\n';
    if (step.needs && step.needs.length) out += 'needs = ' + fmtStringArray(step.needs) + '\n';
    if (step.gate) out += 'gate = ' + fmtGate(step.gate) + '\n';
    var desc = step.description || '';
    out += 'description = """\n' + escMlBasic(desc.endsWith('\n') || desc === '' ? desc : desc + '\n') + '"""\n';
    return out;
  }

  function addStep(text, doc, step, afterStepId, unitKey) {
    var blocks = stepBlocks(doc, unitKey);
    var at;
    if (afterStepId) {
      var after = stepBlockById(doc, afterStepId, unitKey);
      at = after ? after.blockEnd : text.length;
    } else {
      at = blocks.length ? blocks[blocks.length - 1].blockEnd : text.length;
    }
    var block = buildStepText(step, unitKey);
    // when inserting before a following block, keep a blank line after ours too
    if (at < text.length) block += '\n';
    return insertBlock(text, at, block);
  }

  function removeStep(text, doc, stepId, unitKey) {
    var block = stepBlockById(doc, stepId, unitKey);
    if (!block) throw new Error('no unit with id ' + JSON.stringify(stepId));
    // also unplug references to it, back-to-front
    var patches = [{ start: block.blockStart, end: block.blockEnd, ins: '' }];
    stepBlocks(doc, unitKey).forEach(function (b) {
      if (b === block) return;
      var needsKv = findKv(b.kvs, 'needs');
      if (!needsKv || needsKv.value.kind !== 'array') return;
      var kept = needsKv.value.items
        .filter(function (it) { return !(it.kind === 'string' && it.value === stepId); })
        .map(function (it) { return it.value; });
      if (kept.length !== needsKv.value.items.length) {
        if (kept.length) {
          patches.push({ start: needsKv.value.start, end: needsKv.value.end, ins: fmtStringArray(kept) });
        } else {
          var lb = lineBoundsOf(text, needsKv.span);
          patches.push({ start: lb.start, end: lb.end, ins: '' });
        }
      }
    });
    var synth2 = findHeader(doc, 'table', ['synthesis']);
    if (synth2) {
      var depKv2 = findKv(synth2.kvs, 'depends_on');
      if (depKv2 && depKv2.value.kind === 'array') {
        var kept2 = depKv2.value.items
          .filter(function (it) { return !(it.kind === 'string' && it.value === stepId); })
          .map(function (it) { return it.value; });
        if (kept2.length !== depKv2.value.items.length) {
          patches.push({ start: depKv2.value.start, end: depKv2.value.end, ins: fmtStringArray(kept2) });
        }
      }
    }
    patches.sort(function (a, b) { return b.start - a.start; });
    patches.forEach(function (p) { text = splice(text, p.start, p.end, p.ins); });
    return text;
  }

  // ---- inputs/vars table ops: [inputs.name] / [vars.name] ----

  function namedTable(doc, group, name) {
    return findHeader(doc, 'table', [group, name]);
  }

  function addNamedTable(text, doc, group, name, fields) {
    var siblings = doc.headers.filter(function (h) {
      return h.kind === 'table' && h.keyPath.length === 2 && h.keyPath[0] === group;
    });
    var at;
    if (siblings.length) at = siblings[siblings.length - 1].blockEnd;
    else {
      // before the first [[steps]]/[[legs]] block, else EOF
      var firstUnit = doc.headers.filter(function (h) { return h.kind === 'array'; })[0];
      at = firstUnit ? firstUnit.blockStart : text.length;
    }
    var out = '[' + group + '.' + name + ']\n';
    Object.keys(fields).forEach(function (k) {
      if (fields[k] === undefined || fields[k] === '') return;
      out += k + ' = ' + fmtValue(fields[k]) + '\n';
    });
    if (at < text.length) out += '\n';
    return insertBlock(text, at, out);
  }

  function removeNamedTable(text, doc, group, name) {
    var h = namedTable(doc, group, name);
    if (!h) return text;
    return splice(text, h.blockStart, h.blockEnd, '');
  }

  /* ============================ validation ============================ */
  /* Mirrors internal/formula/validate.go. Each finding:
   * { lamp: 'parse'|'ids'|'needs'|'cycles', message, ref: {stepId?, line?} } */

  var VALID_VAR_SOURCES = ['', 'cli', 'env', 'literal', 'hook_bead', 'bead_title', 'bead_description', 'deferred'];
  var VALID_SKILL_NAME = /^[a-zA-Z][a-zA-Z0-9_-]*$/;

  function inferType(js) {
    if (js.type) return js.type;
    if (js.steps && js.steps.length) return 'workflow';
    if (js.legs && js.legs.length) return 'convoy';
    if (js.template && js.template.length) return 'expansion';
    if (js.aspects && js.aspects.length) return 'aspect';
    return '';
  }

  function validate(js) {
    var findings = [];
    function bad(lamp, message, ref) { findings.push({ lamp: lamp, message: message, ref: ref || {} }); }

    if (!js.formula) bad('parse', 'formula field is required');

    var type = inferType(js);
    if (js.type && ['convoy', 'workflow', 'expansion', 'aspect'].indexOf(js.type) === -1) {
      bad('parse', 'invalid formula type "' + js.type + '" (must be convoy, workflow, expansion, or aspect)');
    }

    if (js.vars) {
      Object.keys(js.vars).forEach(function (name) {
        var src = (js.vars[name] && js.vars[name].source) || '';
        if (VALID_VAR_SOURCES.indexOf(src) === -1) {
          bad('parse', 'variable "' + name + '" has invalid source "' + src + '"; valid: cli, env, literal, hook_bead, bead_title, bead_description, deferred');
        }
      });
    }
    if (js.inputs && js.vars) {
      Object.keys(js.inputs).forEach(function (name) {
        if (Object.prototype.hasOwnProperty.call(js.vars, name)) {
          bad('parse', 'input "' + name + '" collides with var of the same name');
        }
      });
    }
    if (js.skills) {
      var seenSkill = Object.create(null);
      js.skills.forEach(function (name) {
        if (!VALID_SKILL_NAME.test(name)) bad('parse', 'invalid skill name: ' + (name === '' ? '(empty string)' : JSON.stringify(name)));
        if (seenSkill[name]) bad('parse', 'duplicate skill: ' + JSON.stringify(name));
        seenSkill[name] = true;
      });
    }

    var units = null, unitWord = '', needsKey = 'needs';
    if (type === 'workflow') { units = js.steps || []; unitWord = 'step'; }
    else if (type === 'convoy') { units = js.legs || []; unitWord = 'leg'; }
    else if (type === 'expansion') { units = js.template || []; unitWord = 'template'; }
    else if (type === 'aspect') { units = js.aspects || []; unitWord = 'aspect'; }
    else bad('parse', 'formula has no steps, legs, template, or aspects — nothing to run');

    var seen = Object.create(null);
    if (units) {
      if (!units.length) bad('ids', type + ' formula requires at least one ' + unitWord);
      units.forEach(function (u) {
        if (!u.id) { bad('ids', unitWord + ' missing required id field'); return; }
        if (seen[u.id]) bad('ids', 'duplicate ' + unitWord + ' id: ' + u.id, { stepId: u.id });
        seen[u.id] = true;
      });
      if (type === 'workflow' || type === 'expansion') {
        units.forEach(function (u) {
          (u[needsKey] || []).forEach(function (need) {
            if (!seen[need]) bad('needs', unitWord + ' "' + u.id + '" needs unknown ' + unitWord + ': ' + need, { stepId: u.id, missing: need });
          });
        });
      }
      if (type === 'convoy' && js.synthesis && js.synthesis.depends_on) {
        js.synthesis.depends_on.forEach(function (dep) {
          if (!seen[dep]) bad('needs', 'synthesis depends_on references unknown leg: ' + dep, { stepId: 'synthesis', missing: dep });
        });
      }
      if (type === 'workflow' || type === 'expansion') {
        var cyc = findCycle(units);
        if (cyc) bad('cycles', 'cycle detected involving step: ' + cyc, { stepId: cyc });
      }
    }
    return findings;
  }

  // DFS with in-stack marking — the same shape as validate.go checkCycles.
  function findCycle(steps) {
    var deps = Object.create(null);
    steps.forEach(function (s) { if (s.id) deps[s.id] = s.needs || []; });
    var visited = Object.create(null), inStack = Object.create(null);
    var offender = null;
    function visit(id) {
      if (inStack[id]) { offender = id; return true; }
      if (visited[id]) return false;
      visited[id] = true; inStack[id] = true;
      var ds = deps[id] || [];
      for (var d = 0; d < ds.length; d++) if (visit(ds[d])) return true;
      inStack[id] = false;
      return false;
    }
    for (var s = 0; s < steps.length; s++) {
      if (steps[s].id && visit(steps[s].id)) return offender;
    }
    return null;
  }

  /* ============================ DAG layout ============================ */
  /* Rank = longest path from a root (computed from the parsed TOML — never
   * hand-placed). Rows within a rank are ordered by the barycenter of each
   * node's upstream rows, which keeps fan-out/merge edges short. */

  function graphModel(js) {
    var type = inferType(js);
    var nodes = [], edges = [];
    if (type === 'convoy') {
      (js.legs || []).forEach(function (l) {
        nodes.push({ id: l.id, title: l.title || l.id, gate: null, needs: [] });
      });
      if (js.synthesis) {
        var deps = js.synthesis.depends_on || [];
        nodes.push({ id: '✦synthesis', title: js.synthesis.title || 'Synthesis', gate: null, needs: deps, synthesis: true });
        deps.forEach(function (d) { edges.push({ from: d, to: '✦synthesis' }); });
      }
    } else {
      var units = js.steps || js.template || js.aspects || [];
      units.forEach(function (s) {
        nodes.push({ id: s.id, title: s.title || s.id, gate: s.gate || null, needs: (s.needs || []).slice() });
        (s.needs || []).forEach(function (nd) { edges.push({ from: nd, to: s.id }); });
      });
    }
    nodes.forEach(function (nd) {
      nd.rarity = nd.gate ? 'legendary' : (nd.needs.length > 1 ? 'epic' : (nd.needs.length === 1 ? 'rare' : 'common'));
    });
    return { type: type, nodes: nodes, edges: edges };
  }

  function layout(model) {
    var byId = Object.create(null);
    model.nodes.forEach(function (nd) { byId[nd.id] = nd; });
    // ranks by longest path (iterate to fixpoint; cycles guard with cap)
    model.nodes.forEach(function (nd) { nd.rank = 0; });
    var changed = true, guard = 0;
    while (changed && guard++ < model.nodes.length + 2) {
      changed = false;
      model.nodes.forEach(function (nd) {
        nd.needs.forEach(function (up) {
          var u = byId[up];
          if (u && nd.rank < u.rank + 1) { nd.rank = u.rank + 1; changed = true; }
        });
      });
    }
    var ranks = [];
    model.nodes.forEach(function (nd) {
      (ranks[nd.rank] = ranks[nd.rank] || []).push(nd);
    });
    // initial rows in file order, then two barycenter passes
    ranks.forEach(function (col) { col.forEach(function (nd, r) { nd.row = r; }); });
    for (var pass = 0; pass < 2; pass++) {
      ranks.forEach(function (col) {
        col.forEach(function (nd) {
          var ups = nd.needs.map(function (u) { return byId[u]; }).filter(Boolean);
          if (ups.length) nd.bary = ups.reduce(function (a, u) { return a + u.row; }, 0) / ups.length;
          else nd.bary = nd.row;
        });
        col.sort(function (a, b) { return a.bary - b.bary || a.row - b.row; });
        col.forEach(function (nd, r) { nd.row = r; });
      });
    }
    var maxRank = ranks.length - 1;
    var maxRows = ranks.reduce(function (a, c) { return Math.max(a, c.length); }, 0);
    return { ranks: ranks, maxRank: maxRank, maxRows: maxRows, byId: byId };
  }

  /* ============================ line diff ============================ */
  /* LCS-based line diff for the hatch's "diff vs file" proof. Returns hunks of
   * {type:'ctx'|'del'|'add', aLine?, bLine?, text}. */

  function lineDiff(aText, bText) {
    var a = aText.split('\n'), b = bText.split('\n');
    // trim common prefix/suffix to keep the LCS matrix small
    var pre = 0;
    while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
    var sufA = a.length, sufB = b.length;
    while (sufA > pre && sufB > pre && a[sufA - 1] === b[sufB - 1]) { sufA--; sufB--; }
    var am = a.slice(pre, sufA), bm = b.slice(pre, sufB);
    var m = am.length, n2 = bm.length;
    var LIMIT = 4000000;
    var ops = [];
    if (m * n2 > LIMIT) {
      // fall back to a whole-block replace for pathological edits
      am.forEach(function (l, idx) { ops.push({ type: 'del', aLine: pre + idx + 1, text: l }); });
      bm.forEach(function (l, idx) { ops.push({ type: 'add', bLine: pre + idx + 1, text: l }); });
    } else {
      var dp = new Uint32Array((m + 1) * (n2 + 1));
      for (var ii = m - 1; ii >= 0; ii--) {
        for (var jj = n2 - 1; jj >= 0; jj--) {
          dp[ii * (n2 + 1) + jj] = am[ii] === bm[jj]
            ? dp[(ii + 1) * (n2 + 1) + jj + 1] + 1
            : Math.max(dp[(ii + 1) * (n2 + 1) + jj], dp[ii * (n2 + 1) + jj + 1]);
        }
      }
      var x = 0, y = 0;
      while (x < m && y < n2) {
        if (am[x] === bm[y]) { ops.push({ type: 'ctx', aLine: pre + x + 1, bLine: pre + y + 1, text: am[x] }); x++; y++; }
        else if (dp[(x + 1) * (n2 + 1) + y] >= dp[x * (n2 + 1) + y + 1]) { ops.push({ type: 'del', aLine: pre + x + 1, text: am[x] }); x++; }
        else { ops.push({ type: 'add', bLine: pre + y + 1, text: bm[y] }); y++; }
      }
      while (x < m) { ops.push({ type: 'del', aLine: pre + x + 1, text: am[x] }); x++; }
      while (y < n2) { ops.push({ type: 'add', bLine: pre + y + 1, text: bm[y] }); y++; }
    }
    var changed = ops.filter(function (o) { return o.type !== 'ctx'; }).length;
    return { ops: ops, changedLines: changed, prefixLines: pre };
  }

  /* ============================ formula stats (roster) ============================ */

  function stats(js) {
    var type = inferType(js);
    var units = js.steps || js.legs || js.template || js.aspects || [];
    var gates = units.filter(function (u) { return u.gate; }).length;
    var merges = units.filter(function (u) { return (u.needs || []).length > 1; }).length;
    if (type === 'convoy' && js.synthesis) merges++;
    var wired = units.filter(function (u) { return (u.needs || []).length === 1; }).length;
    var rarity = gates ? 'legendary' : (merges ? 'epic' : (wired ? 'rare' : 'common'));
    return {
      name: js.formula || '(unnamed)',
      version: js.version, type: type || '(untyped)',
      description: (js.description || '').trim().split('\n')[0],
      units: units.length, gates: gates, merges: merges,
      skills: js.skills || [], rarity: rarity
    };
  }

  return {
    parse: parse,
    validate: validate,
    inferType: inferType,
    graphModel: graphModel,
    layout: layout,
    lineDiff: lineDiff,
    stats: stats,
    fmt: { string: fmtString, stringArray: fmtStringArray, gate: fmtGate, value: fmtValue },
    ops: {
      setKV: setKV,
      setStepField: setStepField,
      renameStep: renameStep,
      addStep: addStep,
      removeStep: removeStep,
      addNamedTable: addNamedTable,
      removeNamedTable: removeNamedTable,
      namedTable: namedTable,
      stepBlocks: stepBlocks,
      stepBlockById: stepBlockById,
      findKv: findKv,
      splice: splice
    }
  };
});
