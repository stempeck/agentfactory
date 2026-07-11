/* shared.js — cross-screen plumbing for the Formula Editor prototype v2.
 * Dispatch slips (toasts), the IndexedDB stash for File System Access handles
 * (handles are structured-cloneable, so they survive page navigation), the
 * sessionStorage handoff that carries formula text between roster/wizard/editor,
 * and small DOM helpers. No frameworks — plain HTML5, per the issue's constraint. */
(function () {
  'use strict';

  var AF = window.AF = {};

  AF.$ = function (id) { return document.getElementById(id); };

  AF.esc = function (s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
                    .replace(/"/g, '&quot;');
  };

  /* ---------------- dispatch slips ---------------- */
  AF.slip = function (msg, kind) {
    var host = AF.$('slips');
    if (!host) return;
    var el = document.createElement('div');
    el.className = 'slip' + (kind ? ' ' + kind : '');
    el.setAttribute('role', 'status');
    el.innerHTML = msg;
    host.appendChild(el);
    setTimeout(function () { el.remove(); }, 7000);
  };

  /* ---------------- IndexedDB stash for FS Access handles ---------------- */
  function db() {
    return new Promise(function (resolve, reject) {
      var req = indexedDB.open('af-formula-editor', 1);
      req.onupgradeneeded = function () { req.result.createObjectStore('handles'); };
      req.onsuccess = function () { resolve(req.result); };
      req.onerror = function () { reject(req.error); };
    });
  }
  /* The stash never rejects: browsers without (or blocking) IndexedDB simply
     lose handle persistence, and every caller degrades to demo/paste mode. */
  AF.stash = {
    put: function (key, value) {
      return db().then(function (d) {
        return new Promise(function (resolve, reject) {
          var tx = d.transaction('handles', 'readwrite');
          tx.objectStore('handles').put(value, key);
          tx.oncomplete = resolve; tx.onerror = function () { reject(tx.error); };
        });
      }).catch(function () { return undefined; });
    },
    get: function (key) {
      return db().then(function (d) {
        return new Promise(function (resolve, reject) {
          var tx = d.transaction('handles', 'readonly');
          var rq = tx.objectStore('handles').get(key);
          rq.onsuccess = function () { resolve(rq.result); };
          rq.onerror = function () { reject(rq.error); };
        });
      }).catch(function () { return undefined; });
    },
    del: function (key) {
      return db().then(function (d) {
        return new Promise(function (resolve) {
          var tx = d.transaction('handles', 'readwrite');
          tx.objectStore('handles').delete(key);
          tx.oncomplete = resolve;
        });
      }).catch(function () { return undefined; });
    }
  };

  /* ---------------- File System Access helpers ---------------- */
  AF.fsSupported = typeof window.showOpenFilePicker === 'function';
  AF.dirSupported = typeof window.showDirectoryPicker === 'function';

  AF.verifyPermission = function (handle, readWrite) {
    var opts = readWrite ? { mode: 'readwrite' } : { mode: 'read' };
    return handle.queryPermission(opts).then(function (p) {
      if (p === 'granted') return true;
      return handle.requestPermission(opts).then(function (p2) { return p2 === 'granted'; });
    });
  };

  AF.pickTomlFile = function () {
    return window.showOpenFilePicker({
      types: [{ description: 'Formula TOML', accept: { 'application/toml': ['.toml'] } }],
      multiple: false
    }).then(function (handles) { return handles[0]; });
  };

  /* ---------------- cross-screen handoff ----------------
   * { name, text, mode: 'demo'|'file'|'new', dirty } in sessionStorage;
   * a live FileSystemFileHandle rides separately in the stash under 'open-file'. */
  AF.handoff = function (payload) {
    sessionStorage.setItem('af-open', JSON.stringify(payload));
  };
  AF.takeHandoff = function () {
    var raw = sessionStorage.getItem('af-open');
    if (!raw) return null;
    try { return JSON.parse(raw); } catch (e) { return null; }
  };

  AF.openInEditor = function (payload, fileHandle) {
    AF.handoff(payload);
    var go = function () { window.location.href = 'editor.html'; };
    if (fileHandle) AF.stash.put('open-file', fileHandle).then(go, go);
    else AF.stash.del('open-file').then(go, go);
  };

  /* ---------------- misc ---------------- */
  AF.download = function (name, text) {
    var a = document.createElement('a');
    a.href = URL.createObjectURL(new Blob([text], { type: 'application/toml' }));
    a.download = name;
    document.body.appendChild(a);
    a.click();
    setTimeout(function () { URL.revokeObjectURL(a.href); a.remove(); }, 400);
  };

  AF.debounce = function (fn, ms) {
    var t = null;
    return function () {
      var args = arguments, self = this;
      clearTimeout(t);
      t = setTimeout(function () { fn.apply(self, args); }, ms);
    };
  };

  AF.reducedMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
})();
