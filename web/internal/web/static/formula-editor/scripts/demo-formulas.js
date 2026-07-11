/* demo-formulas.js — PRODUCTION live-store module (#534). Replaces the prototype's
 * embedded demo snapshots wholesale (approval production note 4: demo data must not
 * ship; production reads the live store). It exposes the IDENTICAL interface the
 * approved screens consume — DemoFormulas.{files, inventory} — populated
 * synchronously from GET /api/formulas so the screens' boot code runs unmodified,
 * plus the two production verbs the declared bind seams call:
 *   DemoFormulas.pour(fileName, text)      -> Promise; PUT CAS save to the store
 *   DemoFormulas.generateAll(outEl, done)  -> POST /api/factory/generate + delta-poll
 * This file is the ONE whole-file seam in the manifest (replaced.tsv); the
 * reconstruction tripwire restores the prototype module over it and demands byte
 * identity with the approved tree. */
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.DemoFormulas = factory();
})(typeof self !== 'undefined' ? self : this, function () {
  'use strict';

  var files = {};
  var inventory = [];
  var shas = {};   // fileName -> sha256 of the loaded baseline (the PUT CAS precondition)

  var SUFFIX = '.formula.toml'; // the server speaks bare names; the screens speak file names
  var TOKEN_KEY = 'af-token';   // shared with the shell (app.js #502 T10)

  function authToken() {
    try { return window.sessionStorage.getItem(TOKEN_KEY) || ''; } catch (e) { return ''; }
  }
  function setAuthToken(tok) {
    try { window.sessionStorage.setItem(TOKEN_KEY, tok); } catch (e) { /* degraded */ }
  }

  function markUnreachable(why) {
    // Design gap G1 (todos/fable-frontend/design_gaps.md): existing elements only, no new UI.
    if (typeof console !== 'undefined' && console.error) console.error('live store unreachable: ' + why);
    if (typeof document !== 'undefined') {
      var tag = document.getElementById('storeTag');
      if (tag) tag.textContent = 'store unreachable — check the console server';
    }
  }

  /* Synchronous load: the approved screens read files/inventory at script-parse time,
     so the catalog must exist before the next script tag executes. */
  try {
    var xhr = new XMLHttpRequest();
    xhr.open('GET', '/api/formulas', false);
    var t0 = authToken(); if (t0) xhr.setRequestHeader('X-AF-Token', t0);
    xhr.send(null);
    if (xhr.status === 200) {
      var env0 = JSON.parse(xhr.responseText);
      var rows = (env0 && env0.ok && env0.data && env0.data.formulas) || [];
      rows.forEach(function (row) {
        var fileName = row.name + SUFFIX;
        inventory.push(fileName);
        // read_only rows carry no text (non-conforming names the store refuses to
        // write): inventory-only, so the roster's locked-card affordance renders them.
        if (!row.read_only && typeof row.text === 'string') {
          files[fileName] = row.text;
          shas[fileName] = row.sha256 || '';
        }
      });
      inventory.sort();
    } else {
      markUnreachable('store returned HTTP ' + xhr.status);
    }
  } catch (e0) {
    markUnreachable(String((e0 && e0.message) || e0));
  }

  function req(method, path, body, tok) {
    var h = { 'Content-Type': 'application/json' };
    var t = tok !== undefined ? tok : authToken();
    if (t) h['X-AF-Token'] = t;
    return fetch(path, {
      method: method, headers: h, credentials: 'same-origin',
      body: body ? JSON.stringify(body) : undefined
    }).then(function (res) {
      return res.json().catch(function () { return { ok: false, message: 'bad response' }; })
        .then(function (env) { env._status = res.status; return env; });
    });
  }

  /* One first-write retry on 401 — the shell's own token pattern (app.js promptForToken). */
  function write(method, path, body) {
    return req(method, path, body).then(function (env) {
      if (env._status !== 401) return env;
      var tok = window.prompt('Paste the session token printed in the afweb startup log to authorize this write:');
      tok = (tok || '').trim();
      if (!tok) return env;
      setAuthToken(tok);
      return req(method, path, body, tok);
    });
  }

  function pour(fileName, text) {
    var bare = fileName.replace(/\.formula\.toml$/, '');
    return write('PUT', '/api/formulas/' + encodeURIComponent(bare), {
      text: text, base_sha256: shas[fileName] || ''
    }).then(function (env) {
      if (!env.ok) throw new Error(env.message || ('save failed (HTTP ' + env._status + ')'));
      shas[fileName] = (env.data && env.data.sha256) || '';
      files[fileName] = text;
      if (inventory.indexOf(fileName) === -1) { inventory.push(fileName); inventory.sort(); }
    });
  }

  /* Streams the real Generate-All run into the roster's existing console element.
     The approval pinned this seam: "breakerThrown … production streams af output". */
  function generateAll(out, done) {
    function line(txt, cls) {
      var s = document.createElement('span');
      s.className = cls || '';
      s.textContent = txt + '\n';
      out.appendChild(s);
      out.scrollTop = out.scrollHeight;
    }
    var offset = 0;
    function poll() {
      req('GET', '/api/factory/generate?from=' + offset).then(function (env) {
        if (!env.ok) { line('progress unavailable: ' + (env.message || ('HTTP ' + env._status)), 'alarm'); done(); return; }
        var prog = env.data || {};
        if (prog.data) {
          var parts = prog.data.split('\n');
          parts.forEach(function (l, i) { if (l || i < parts.length - 1) line(l, 'dim'); });
        }
        offset = prog.offset || offset;
        var st = prog.state || {};
        if (st.running) { setTimeout(poll, 700); return; }
        if (st.exit_code === 0) line('FLOOR READY — factory regenerated and the agents are back up.', 'ok');
        else line('Regeneration ended (exit ' + st.exit_code + ') — see the log above.', 'alarm');
        done();
      }, function (e) { line('progress unavailable: ' + e.message, 'alarm'); done(); });
    }
    line('$ af install --agents', 'dim');
    write('POST', '/api/factory/generate', { confirm: true }).then(function (env) {
      if (!env.ok) { line('could not start: ' + (env.message || ('HTTP ' + env._status)), 'alarm'); done(); return; }
      poll();
    }, function (e) { line('could not start: ' + e.message, 'alarm'); done(); });
  }

  return { files: files, inventory: inventory, pour: pour, generateAll: generateAll };
});
