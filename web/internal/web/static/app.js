/* ============================================================================
   app.js — Direction A · "Neon Bazaar" Floor view (Phase 1, vanilla JS).
   Implements the four Phase-1 view-models:
     AppViewModel · PowerBarViewModel · ConfirmViewModel · FloorViewModel
   No framework. Reads /api/agents (honest read-model) and drives the
   allowlisted control verbs (af up / af down / af down --reset) through the
   loopback server. Destructive verbs ALWAYS route through the confirm dialog
   and send confirm:true; the server enforces the same gate independently.
   ========================================================================== */
(function () {
  'use strict';

  // ---- tiny DOM helpers ----
  var $ = function (sel) { return document.querySelector(sel); };
  var byId = function (id) { return document.getElementById(id); };

  // Session token for the #502 write-tier routes (PUT /api/formulas/{name}, POST
  // /api/factory/generate), which require it EVEN on pure loopback. The operator pastes it from the
  // afweb startup log; it is held in sessionStorage (never templated into the served document, so the
  // CSP script-src 'self' stays intact — no inline script). The legacy <meta name="af-token"> is a
  // vestigial fallback for a future templated-delivery path.
  var TOKEN_KEY = 'af-token';
  function authToken() {
    try {
      var t = window.sessionStorage.getItem(TOKEN_KEY);
      if (t) return t;
    } catch (e) { /* sessionStorage unavailable — fall through to the meta */ }
    var m = document.querySelector('meta[name="af-token"]');
    return m ? m.getAttribute('content') : '';
  }
  function setAuthToken(tok) {
    try { window.sessionStorage.setItem(TOKEN_KEY, tok); } catch (e) { /* ignore */ }
  }
  // First-write prompt: when a write is refused (401), ask the operator to paste the startup token
  // once, store it, and let the caller retry. Returns the trimmed token, or '' if dismissed.
  function promptForToken() {
    var tok = window.prompt('Paste the session token printed in the afweb startup log to authorize this write:');
    tok = (tok || '').trim();
    if (tok) setAuthToken(tok);
    return tok;
  }

  // ---- API layer ----
  // sendWrite issues one state-changing request carrying the current token; writeReq wraps it with a
  // single first-write retry: a 401 prompts the operator for the token and retries once. Reads never
  // require the token, so GET is unchanged.
  function sendWrite(method, path, body) {
    var h = { 'Content-Type': 'application/json' };
    var t = authToken(); if (t) h['X-AF-Token'] = t;
    return fetch(path, {
      method: method, headers: h, credentials: 'same-origin',
      body: body ? JSON.stringify(body) : '{}'
    }).then(parse);
  }
  function writeReq(method, path, body) {
    return sendWrite(method, path, body).then(function (env) {
      if (env && env._status === 401 && promptForToken()) {
        return sendWrite(method, path, body); // retry once with the freshly-pasted token
      }
      return env;
    });
  }
  var API = {
    get: function (path) {
      var h = {};
      var t = authToken(); if (t) h['X-AF-Token'] = t;
      return fetch(path, { headers: h, credentials: 'same-origin' }).then(parse);
    },
    post: function (path, body) { return writeReq('POST', path, body); },
    // put sends the COMPLETE edited document as the request body (settings: fed to `af config <file>
    // set` on stdin; formulas: the {text, base_sha256} CAS save).
    put: function (path, body) { return writeReq('PUT', path, body); }
  };
  function parse(res) {
    return res.json().catch(function () { return { ok: false, message: 'bad response' }; })
      .then(function (env) { env._status = res.status; return env; });
  }

  // ---- status → look (honest mapping; never invents "working") ----
  var STATUS = {
    working: { cls: 's-working', label: 'Working' },
    gated:   { cls: 's-gate',    label: 'Gate', gate: true },
    blocked: { cls: 's-wait',    label: 'Waiting on dependencies' },
    idle:    { cls: 's-idle',    label: 'Idle', neutral: true },
    stopped: { cls: 's-idle',    label: 'Stopped', neutral: true },
    error:   { cls: 's-error',   label: 'Needs attention' }
  };
  // filter id → the statuses it matches
  var FILTERS = {
    all: null,
    working: ['working'],
    gate: ['gated'],
    waiting: ['blocked'],
    attention: ['error']
  };

  // ---- toast ----
  var _tt;
  function toast(msg) {
    var t = byId('toast'); if (!t) return;
    t.textContent = msg; t.hidden = false;
    clearTimeout(_tt); _tt = setTimeout(function () { t.hidden = true; }, 2600);
  }

  // =========================================================================
  // ConfirmViewModel — the destructive-action gate. NEVER acts directly.
  // =========================================================================
  var ConfirmViewModel = {
    open: false,
    consequence: '',
    target: '',
    _action: null,
    /** show the modal, naming exactly what is lost; stash the async action. */
    request: function (target, scope, action) {
      this.target = target; this.open = true; this._action = action;
      var who = target || 'this agent';
      byId('resetTitle').textContent = scope === 'factory'
        ? 'Reset the entire factory?' : 'Reset ' + who + '?';
      this.consequence = scope === 'factory'
        ? 'This stops every agent and removes all work-in-progress branches and worktrees across the factory.'
        : "This removes " + who + "'s work-in-progress branch and worktree.";
      byId('resetWhat').textContent = this.consequence;
      byId('resetLose').textContent = 'Unmerged work will be lost — this cannot be undone.';
      var dlg = byId('resetDlg');
      if (dlg && typeof dlg.showModal === 'function') dlg.showModal();
    },
    /** invoked when the dialog closes with the confirm value. */
    confirm: function () {
      this.open = false;
      var act = this._action; this._action = null;
      return act ? Promise.resolve(act()) : Promise.resolve();
    },
    cancel: function () { this.open = false; this._action = null; }
  };

  // wire the native <dialog> close → confirm/cancel
  (function wireDialog() {
    var dlg = byId('resetDlg'); if (!dlg) return;
    dlg.addEventListener('close', function () {
      if (dlg.returnValue === 'confirm') ConfirmViewModel.confirm();
      else ConfirmViewModel.cancel();
    });
  })();

  // =========================================================================
  // PowerBarViewModel — factory-wide up / down / down+reset.
  // =========================================================================
  var PowerBarViewModel = {
    factoryRunning: false,
    transitioning: false,
    startFactory: function () {
      this.transitioning = true;
      return API.post('/api/factory/up').then(report('Factory starting — signs will light on the next refresh'))
        .then(done(this));
    },
    shutDown: function () {
      this.transitioning = true;
      return API.post('/api/factory/down', { reset: false, confirm: false })
        .then(report('Shutting the factory down…')).then(done(this));
    },
    shutDownReset: function () {
      // Destructive: route through the confirm dialog; the action sends confirm:true.
      ConfirmViewModel.request('the entire factory', 'factory', function () {
        return API.post('/api/factory/down', { reset: true, confirm: true })
          .then(report('Factory reset — work-in-progress discarded'));
      });
    }
  };

  // =========================================================================
  // FloorViewModel — the live agent skyline.
  // =========================================================================
  var FloorViewModel = {
    agents: [],
    lastUpdated: '',          // ISO assembled_at
    statusFilter: 'all',
    query: '',
    loading: false,

    refresh: function () {
      this.loading = true;
      var self = this;
      return API.get('/api/agents').then(function (env) {
        self.loading = false;
        if (!env.ok) { showError(env.message || 'read failed'); return; }
        byId('errbox').hidden = true;
        self.agents = env.data || [];
        if (self.agents.length && self.agents[0].assembled_at) {
          self.lastUpdated = self.agents[0].assembled_at;
          AppViewModel.lastUpdated = self.lastUpdated;
        }
        render();
        tickStale();
      }).catch(function (e) { self.loading = false; showError(String(e)); });
    },
    search: function (q) { this.query = (q || '').toLowerCase(); render(); },
    filterByStatus: function (f) { this.statusFilter = f || 'all'; syncFilterButtons(); render(); },
    viewAgent: function (name) { AppViewModel.navigate('agent/' + name); },
    downAgent: function (name) {
      return API.post('/api/agents/' + encodeURIComponent(name) + '/down', { reset: false, confirm: false })
        .then(report(name + ' is stopping')).then(function () { FloorViewModel.refresh(); });
    },
    resetAgent: function (name) {
      ConfirmViewModel.request(name, 'agent', function () {
        return API.post('/api/agents/' + encodeURIComponent(name) + '/down', { reset: true, confirm: true })
          .then(report(name + ' was reset — work-in-progress discarded'))
          .then(function () { FloorViewModel.refresh(); });
      });
    }
  };

  // =========================================================================
  // SlingViewModel — pick an idle agent, build its form, sling a task.
  // Issues the identical `af sling --agent <name> --reset --var k=v …` argv
  // the operator would run by hand; the server hides identity-bearing vars
  // and rejects unknown keys. Sling is fire-and-forget: success copy
  // says "starting", never "working".
  // =========================================================================
  var SlingViewModel = {
    agents: [],     // idle agents (read-model Running == false)
    selected: '',   // selected agent name
    schema: null,   // current form schema {name, fields:[…]}
    query: '',

    activate: function () {
      showSling();
      return this.refresh();
    },
    refresh: function () {
      var self = this;
      return API.get('/api/agents').then(function (env) {
        if (!env.ok) { toast(env.message || 'read failed'); return; }
        self.agents = (env.data || []).filter(function (a) { return !a.running; });
        renderIdleList();
      }).catch(function (e) { toast(String(e)); });
    },
    search: function (q) { this.query = (q || '').toLowerCase(); renderIdleList(); },
    pick: function (name) {
      this.selected = name;
      this.schema = null;
      byId('sling-form-agent').textContent = name;
      byId('sling-success').hidden = true;
      clearSlingError();
      syncIdleSelection();
      var host = byId('sling-form-host');
      host.innerHTML = '';
      host.appendChild(el('p', 'keyhint', 'Loading ' + name + "'s task form…"));
      var self = this;
      return API.get('/api/agents/' + encodeURIComponent(name) + '/form').then(function (env) {
        if (self.selected !== name) return; // a newer pick won
        if (!env.ok) {
          host.innerHTML = '';
          var v = el('p', 'validation', env.message || 'could not load the task form');
          v.hidden = false; host.appendChild(v);
          return;
        }
        self.schema = env.data || { fields: [] };
        renderForm(self.schema);
      }).catch(function (e) { toast(String(e)); });
    },
    sling: function () {
      if (!this.selected || !this.schema) return;
      var primary = this.schema.primary || '';
      // The task box is ALWAYS rendered; it is keyed on the primary field when present, else on a
      // synthetic sentinel. Read its value BEFORE collectFormValues so the task never travels as a --var.
      var taskKey = primary || '__task__';
      var taskCtl = byId('sling-field-' + taskKey);
      var task = taskCtl ? String(taskCtl.value).trim() : '';
      if (!task) { showTaskError(taskKey); return; }

      var vars = collectFormValues(this.schema);
      if (primary) delete vars[primary]; // the task is the positional; it must not double-bind as a --var.

      // Validate EVERY required field (not just the task) before dispatch. required_unless
      // fields are a hint only — the CLI is the arbiter — so they are intentionally not blocked here.
      var fields = (this.schema.fields || []);
      for (var i = 0; i < fields.length; i++) {
        var f = fields[i];
        if (f.required && f.name !== primary) {
          var v = vars[f.name];
          if (!v || v.trim() === '') { showTaskError(f.name); return; }
        }
      }

      var name = this.selected;
      var self = this;
      var dispatch = function () { return self._postSling(name, task, vars); };

      // --reset blast-radius guard: always-`--reset` would discard a live formula step. If the
      // chosen idle agent still holds a live (non-terminal) step, require a browser confirm first.
      // This is a UI affordance, NOT an af-core runtime prompt (ADR-014 unaffected).
      var sel = this._agentByName(name);
      if (sel && sel.step_id && !isTerminalStep(sel.step_state)) {
        ConfirmViewModel.request(name, 'agent', dispatch);
        return;
      }
      return dispatch();
    },
    _agentByName: function (name) {
      for (var i = 0; i < this.agents.length; i++) { if (this.agents[i].name === name) return this.agents[i]; }
      return null;
    },
    _postSling: function (name, task, vars) {
      var btn = byId('sling-btn');
      if (btn) { btn.disabled = true; btn.textContent = 'Slinging…'; }
      clearSlingError();
      return API.post('/api/agents/' + encodeURIComponent(name) + '/sling', { task: task, vars: vars }).then(function (env) {
        if (btn) btn.textContent = 'Sling agent';
        if (env && env.ok) {
          byId('sling-success-agent').textContent = name;
          byId('sling-success').hidden = false;
          byId('sling-success').scrollIntoView({ block: 'nearest' });
          FloorViewModel.refresh(); // so the agent is lit when the operator returns to the Floor
        } else {
          if (btn) btn.disabled = false;
          showSlingError((env && env.message) || 'sling failed');
        }
        return env;
      }).catch(function (e) {
        if (btn) { btn.textContent = 'Sling agent'; btn.disabled = false; }
        showSlingError(String(e));
      });
    }
  };

  // ---- view toggles (exactly one #view-* section visible at a time) ----
  var VIEW_IDS = ['view-floor', 'view-sling', 'view-dispatch', 'view-settings', 'view-prototypes', 'view-agent',
    'view-formulas', 'view-telemetry'];
  function showView(id) {
    VIEW_IDS.forEach(function (v) { var s = byId(v); if (s) s.hidden = (v !== id); });
  }
  function showFloor() { showView('view-floor'); }
  function showSling() { showView('view-sling'); }
  function showDispatch() { showView('view-dispatch'); }
  function showSettings() { showView('view-settings'); }
  function showPrototypes() { showView('view-prototypes'); }
  function showFormulas() { showView('view-formulas'); }
  function showTelemetry() { showView('view-telemetry'); }

  // ---- sling view: idle-agent list ----
  function renderIdleList() {
    var list = byId('sling-agentlist'); if (!list) return;
    var q = SlingViewModel.query;
    var shown = SlingViewModel.agents.filter(function (a) {
      return !q || a.name.toLowerCase().indexOf(q) > -1;
    });
    list.innerHTML = '';
    shown.forEach(function (a) {
      var b = el('button', null);
      b.type = 'button';
      b.setAttribute('role', 'option');
      b.setAttribute('data-name', a.name);
      b.setAttribute('aria-pressed', a.name === SlingViewModel.selected ? 'true' : 'false');
      b.appendChild(el('span', 'an', a.name));
      b.appendChild(el('span', 'idle', 'idle'));
      b.addEventListener('click', function () { SlingViewModel.pick(a.name); });
      list.appendChild(b);
    });
    byId('sling-idle-count').textContent = String(SlingViewModel.agents.length);
    byId('sling-empty').hidden = SlingViewModel.agents.length !== 0;
  }
  function syncIdleSelection() {
    document.querySelectorAll('#sling-agentlist button').forEach(function (b) {
      b.setAttribute('aria-pressed', b.getAttribute('data-name') === SlingViewModel.selected ? 'true' : 'false');
    });
  }

  // ---- sling view: generated form ----
  function renderForm(schema) {
    var host = byId('sling-form-host'); if (!host) return;
    host.innerHTML = '';
    var fields = (schema && schema.fields) || [];
    // Server-authoritative: which field the positional task effectively binds to. Blank == the
    // synthetic-task-box signal (e.g. design-v7, whose issue is hook-sourced) — never re-derived client-side.
    var primary = (schema && schema.primary) || '';
    var primaryField = null;
    for (var i = 0; i < fields.length; i++) { if (fields[i].name === primary) { primaryField = fields[i]; break; } }

    var form = el('form', null); form.id = 'sling-form';
    form.addEventListener('submit', function (e) { e.preventDefault(); SlingViewModel.sling(); });

    // ALWAYS render the prominent task box: the real primary field when present, else a synthetic one.
    form.appendChild(taskBox(primaryField));

    // any other required fields, surfaced.
    fields.forEach(function (f) { if (f.required && f.name !== primary) form.appendChild(fieldRow(f, true)); });

    // optional fields collapsed under Advanced.
    var optional = fields.filter(function (f) { return !f.required; });
    if (optional.length) {
      var det = el('details', 'advanced');
      var sum = document.createElement('summary'); sum.textContent = 'Advanced · optional variables';
      det.appendChild(sum);
      var body = el('div', 'body');
      optional.forEach(function (f) { body.appendChild(fieldRow(f, false)); });
      det.appendChild(body);
      form.appendChild(det);
    }

    var row = el('div', 'formrow'); row.style.marginTop = '18px';
    var btn = el('button', 'btn primary block', 'Sling agent');
    btn.type = 'submit'; btn.id = 'sling-btn';
    btn.disabled = true; // the task box always exists; gated until it has a task (onTaskInput keeps it in sync).
    row.appendChild(btn);
    row.appendChild(el('p', 'keyhint', 'The button stays off until the task box has a task.'));
    form.appendChild(row);

    host.appendChild(form);
  }
  // The prominent task box. `f` is the primary field (its value → positional task), or null for a
  // synthetic box (schema.primary == "") whose value still flows to the positional → assignment bead.
  function taskBox(f) {
    var key = (f && f.name) || '__task__';
    var synthetic = !f;
    var wrap = el('div', 'formrow taskbox');
    var lbl = el('label', 'lbl');
    lbl.setAttribute('for', 'sling-field-' + key);
    // Drive the label from the primary field so distinct agents visibly differ, rather than
    // the same hardcoded placeholder/label for every agent.
    lbl.appendChild(document.createTextNode(synthetic ? 'Task / issue reference' : (f.description || f.name)));
    lbl.appendChild(document.createTextNode(' '));
    lbl.appendChild(el('span', 'req', 'required'));
    wrap.appendChild(lbl);
    var ta = el('textarea', 'field w');
    ta.id = 'sling-field-' + key;
    ta.setAttribute('data-key', key);
    ta.setAttribute('aria-describedby', 'sling-field-err-' + key);
    // Name the bound field in the placeholder so the box is distinct per agent, same reasoning
    // as the label above.
    ta.placeholder = synthetic
      ? 'Paste a GitHub issue/PR URL, a path to a problem description, or describe the task…'
      : 'Paste a GitHub issue/PR URL, a path, or a description — this becomes “' + (f.name || f.description) + '”.';
    ta.addEventListener('input', onTaskInput);
    wrap.appendChild(ta);
    // design-v7 discoverability: explain where a no-visible-vars agent gets its issue.
    if (synthetic) {
      wrap.appendChild(el('p', 'keyhint', 'This agent takes its issue from the task you paste here.'));
    }
    var err = el('p', 'validation', 'Give the agent a task before slinging.');
    err.id = 'sling-field-err-' + key; err.hidden = true;
    wrap.appendChild(err);
    return wrap;
  }
  function fieldRow(f, required) {
    var row = el('div', 'formrow');
    var lbl = el('label', 'lbl');
    lbl.setAttribute('for', 'sling-field-' + f.name);
    lbl.appendChild(document.createTextNode(f.name));
    lbl.appendChild(document.createTextNode(' '));
    var ru = f.required_unless || []; // conditional-required group carried through from the schema
    if (required) {
      lbl.appendChild(el('span', 'req', 'required'));
    } else if (ru.length) {
      // af enforces this at sling time; surface it so the operator knows it's conditionally required.
      lbl.appendChild(el('span', 'req', 'required unless ' + ru.join(', ') + ' set'));
    } else {
      lbl.appendChild(el('span', 'opt', f.default ? ('optional · default ' + f.default) : 'optional'));
    }
    row.appendChild(lbl);
    var ctl = controlFor(f);
    row.appendChild(ctl);
    // Required (non-primary) fields get the same per-field error machinery as the task box —
    // generalized, not a parallel system: same sling-field-err-<name> id + clear-on-input.
    if (required) {
      ctl.addEventListener('input', onFieldInput);
      var err = el('p', 'validation', (f.description || f.name) + ' is required.');
      err.id = 'sling-field-err-' + f.name; err.hidden = true;
      row.appendChild(err);
    }
    return row;
  }
  function controlFor(f) {
    var t = (f.type || '').toLowerCase();
    var input = el('input', 'field w');
    input.type = (t === 'int' || t === 'integer' || t === 'number') ? 'number' : 'text';
    input.id = 'sling-field-' + f.name;
    input.setAttribute('data-key', f.name);
    if (f.default) input.value = f.default;
    return input;
  }
  function onTaskInput(e) {
    var ta = e.target;
    var nonEmpty = ta.value.trim().length > 0;
    var btn = byId('sling-btn');
    if (btn) btn.disabled = !nonEmpty;
    if (nonEmpty) {
      var err = byId('sling-field-err-' + ta.getAttribute('data-key'));
      if (err) err.hidden = true;
    }
  }
  // required non-primary field: clear its own inline error on input (the submit button stays gated
  // on the task box alone, via onTaskInput). Reuses the sling-field-err-<name> convention.
  function onFieldInput(e) {
    var ctl = e.target;
    if (ctl.value.trim().length > 0) {
      var err = byId('sling-field-err-' + ctl.getAttribute('data-key'));
      if (err) err.hidden = true;
    }
  }
  function showTaskError(key) {
    var err = byId('sling-field-err-' + key); if (err) err.hidden = false;
    var ta = byId('sling-field-' + key); if (ta) ta.focus();
  }
  // Persistent inline failure surface: a sling failure renders env.message here, not as an
  // ephemeral toast. Reuses the .validation danger class (the sling-error region in index.html).
  function showSlingError(msg) {
    var box = byId('sling-error'); if (!box) return;
    box.textContent = msg || 'sling failed';
    box.hidden = false;
    box.scrollIntoView({ block: 'nearest' });
  }
  function clearSlingError() {
    var box = byId('sling-error'); if (box) { box.hidden = true; box.textContent = ''; }
  }
  // A live (non-terminal) formula step means re-slinging (always `--reset`) would discard work.
  // step_state strings come from the /api/agents read-model (af-core agents.go): ready/blocked are
  // live; all_complete/no_formula/error/empty (and the empty string) are terminal.
  function isTerminalStep(state) {
    return state === '' || state === 'all_complete' || state === 'no_formula' || state === 'error' || state === 'empty';
  }
  function collectFormValues(schema) {
    var vars = {};
    (schema.fields || []).forEach(function (f) {
      var ctl = byId('sling-field-' + f.name);
      if (!ctl) return;
      var val = ctl.value == null ? '' : String(ctl.value);
      if (val.trim() === '') return; // omit empties so af uses the declared default
      vars[f.name] = val;
    });
    return vars;
  }

  // =========================================================================
  // DispatchViewModel — read-only dispatch history (poll/refresh) + a "new
  // dispatch" indicator. Backed by GET /api/dispatch (af dispatch status --json).
  // af-core computes dispatcher + per-agent liveness, so the table reflects them
  // directly. "new" is a diff against the entries seen on the previous poll.
  // =========================================================================
  var DispatchViewModel = {
    entries: [],
    dispatcherRunning: false,
    seen: null,        // Set-like map of seen entry keys; null until the first load
    lastUpdated: '',

    activate: function () { showDispatch(); return this.refresh(); },
    refresh: function () {
      var self = this;
      return API.get('/api/dispatch').then(function (env) {
        if (!env || !env.ok) { toast((env && env.message) || 'dispatch read failed'); return; }
        var data = env.data || {};
        self.dispatcherRunning = !!data.dispatcher_running;
        self.entries = data.entries || [];
        if (data.assembled_at) { self.lastUpdated = data.assembled_at; AppViewModel.lastUpdated = data.assembled_at; }
        renderDispatch();
        tickStale();
      }).catch(function (e) { toast(String(e)); });
    }
  };

  function renderDispatch() {
    var vm = DispatchViewModel;

    // status line + stat cards
    var statusEl = byId('dispatch-status'); var statusTxt = byId('dispatch-status-text');
    if (statusEl && statusTxt) {
      if (vm.dispatcherRunning) { statusEl.classList.remove('off'); statusTxt.textContent = 'Dispatcher ONLINE'; }
      else { statusEl.classList.add('off'); statusTxt.textContent = 'Dispatcher OFFLINE'; }
    }
    var stats = byId('dispatch-stats');
    if (stats) {
      stats.innerHTML = '';
      var running = vm.entries.filter(function (e) { return e.agent_running; }).length;
      stats.appendChild(statCard('dispatches', String(vm.entries.length)));
      stats.appendChild(statCard('agents running', String(running)));
    }

    // feed: one <li> per entry, with the "new" highlight computed by diffing against the last poll.
    var feed = byId('dispatch-feed'); if (!feed) return;
    feed.innerHTML = '';
    var firstLoad = vm.seen === null;
    if (firstLoad) vm.seen = {};
    vm.entries.forEach(function (e) {
      var key = (e.issue || '') + '|' + (e.dispatched_at || '');
      var isNew = !firstLoad && !vm.seen[key];
      feed.appendChild(dispatchRow(e, isNew));
      vm.seen[key] = true;
    });
    var empty = byId('dispatch-empty');
    if (empty) empty.hidden = vm.entries.length !== 0;
    feed.hidden = vm.entries.length === 0;
  }

  function statCard(k, v) {
    var d = el('div', 'stat');
    d.appendChild(el('div', 'k', k));
    d.appendChild(el('div', 'v', v));
    return d;
  }
  function dispatchRow(e, isNew) {
    var li = el('li', isNew ? 'new' : null);
    li.appendChild(el('span', 'when', relTime(e.dispatched_at)));
    li.appendChild(el('span', 'tag', e.source || 'issue'));
    var route = el('span', 'route');
    route.appendChild(el('span', 'ar', '→'));
    route.appendChild(document.createTextNode(' ' + (e.agent || '—') + ' '));
    route.appendChild(el('span', 'ar', '←'));
    route.appendChild(document.createTextNode(' ' + (e.issue || '')));
    if (e.agent_running) {
      var run = el('span', 'tag'); run.style.background = 'var(--neon-violet)'; run.textContent = 'running';
      route.appendChild(document.createTextNode(' '));
      route.appendChild(run);
    }
    li.appendChild(route);
    if (isNew) li.appendChild(el('span', 'pill-new', 'new'));
    return li;
  }
  function relTime(iso) {
    if (!iso) return '';
    var t = new Date(iso).getTime();
    if (isNaN(t)) return '';
    var s = Math.max(0, Math.round((Date.now() - t) / 1000));
    if (s < 60) return s + 's ago';
    if (s < 3600) return Math.round(s / 60) + 'm ago';
    if (s < 86400) return Math.round(s / 3600) + 'h ago';
    return Math.round(s / 86400) + 'd ago';
  }

  // =========================================================================
  // PrototypesViewModel — list agent-built prototypes (GET /api/prototypes), view the selected one
  // in a SANDBOXED iframe (src GET /proto/{id}/), and submit gate-aware feedback (POST
  // /api/prototypes/{id}/feedback). The feedback panel is disabled with an honest "feedback not
  // currently open" message whenever the prototype's owning agent is not parked at the matching gate
  // (feedback_open=false). "Request changes" deliberately leaves the Decision blank (REJECTED would
  // HALT the agent) and relies on the notes so the design loop iterates.
  // =========================================================================
  var PrototypesViewModel = {
    protos: [],
    selected: null,   // the selected prototype object {id, version, path, feedback_open}

    activate: function () { showPrototypes(); return this.refresh(); },
    refresh: function () {
      var self = this;
      return API.get('/api/prototypes').then(function (env) {
        if (!env || !env.ok) { toast((env && env.message) || 'prototypes read failed'); return; }
        self.protos = env.data || [];
        // keep the current selection if it still exists (with refreshed feedback_open).
        if (self.selected) {
          self.selected = self.protos.filter(function (p) { return p.id === self.selected.id; })[0] || null;
        }
        renderProtoList();
        if (self.selected) { renderFeedbackPanel(self.selected); }
        else { clearProtoViewer(); }
      }).catch(function (e) { toast(String(e)); });
    },
    pick: function (id) {
      var p = this.protos.filter(function (x) { return x.id === id; })[0];
      if (!p) return;
      this.selected = p;
      syncProtoSelection();
      byId('proto-vpath').textContent = p.path || p.id;
      var frame = byId('proto-frame');
      if (frame) frame.src = '/proto/' + encodeURIComponent(p.id) + '/';
      renderFeedbackPanel(p);
    },
    send: function () {
      var p = this.selected;
      if (!p) { toast('Pick a prototype first'); return; }
      if (!p.feedback_open) { return; } // panel is disabled when the gate is not open
      var checked = document.querySelector('input[name="proto-verdict"]:checked');
      var verdict = checked ? checked.value : '';
      var notes = (byId('proto-notes').value || '').trim();
      if (!verdict && !notes) { showProtoErr('Choose Approve / Request changes, or write notes.'); return; }
      hide('proto-fb-err');
      var body = { notes: notes };
      if (verdict === 'approve') { body.decision = 'APPROVED'; }
      // verdict 'changes' => decision left blank so the agent iterates on the notes (not REJECTED).
      var btn = byId('proto-fb-send');
      if (btn) { btn.disabled = true; btn.textContent = 'Sending…'; }
      return API.post('/api/prototypes/' + encodeURIComponent(p.id) + '/feedback', body).then(function (env) {
        if (btn) { btn.disabled = false; btn.textContent = 'Send feedback'; }
        if (env && env.ok) {
          byId('proto-fb-ok-msg').textContent = env.message || 'Feedback saved.';
          byId('proto-fb-ok').hidden = false;
          byId('proto-fb-ok').scrollIntoView({ block: 'nearest' });
        } else {
          showProtoErr((env && env.message) || 'feedback failed');
        }
        return env;
      }).catch(function (e) {
        if (btn) { btn.disabled = false; btn.textContent = 'Send feedback'; }
        toast(String(e));
      });
    }
  };

  function renderProtoList() {
    var vm = PrototypesViewModel;
    var list = byId('proto-list'); if (!list) return;
    list.innerHTML = '';
    vm.protos.forEach(function (p) {
      var b = el('button', null);
      b.type = 'button';
      b.setAttribute('role', 'option');
      b.setAttribute('data-id', p.id);
      b.setAttribute('aria-pressed', vm.selected && vm.selected.id === p.id ? 'true' : 'false');
      b.appendChild(el('div', 'pp', p.path || p.id));
      b.appendChild(el('div', 'pm', 'iteration ' + (p.version || 1)));
      var meta = el('div', 'pm');
      meta.appendChild(el('span', 'pstatus ' + (p.feedback_open ? 'await' : 'ok'),
        p.feedback_open ? 'Awaiting feedback' : 'Feedback closed'));
      b.appendChild(meta);
      b.addEventListener('click', function () { PrototypesViewModel.pick(p.id); });
      list.appendChild(b);
    });
    byId('proto-count').textContent = String(vm.protos.length);
    byId('proto-empty').hidden = vm.protos.length !== 0;
  }
  function syncProtoSelection() {
    var sel = PrototypesViewModel.selected;
    document.querySelectorAll('#proto-list button').forEach(function (b) {
      b.setAttribute('aria-pressed', sel && b.getAttribute('data-id') === sel.id ? 'true' : 'false');
    });
  }
  function setFeedbackEnabled(open) {
    var form = byId('proto-fb-form'); if (!form) return;
    form.querySelectorAll('input, textarea, button').forEach(function (c) { c.disabled = !open; });
  }
  function renderFeedbackPanel(p) {
    byId('proto-fb-ok').hidden = true;
    hide('proto-fb-err');
    var open = !!(p && p.feedback_open);
    var closed = byId('proto-fb-closed'); if (closed) closed.hidden = open;
    setFeedbackEnabled(open);
  }
  function clearProtoViewer() {
    var vp = byId('proto-vpath'); if (vp) vp.textContent = 'Select a prototype';
    var frame = byId('proto-frame'); if (frame) frame.src = 'about:blank';
    var closed = byId('proto-fb-closed'); if (closed) closed.hidden = true;
    byId('proto-fb-ok').hidden = true;
    hide('proto-fb-err');
    setFeedbackEnabled(false);
  }
  function showProtoErr(msg) { var e = byId('proto-fb-err'); if (e) { e.textContent = msg; e.hidden = false; } }

  // =========================================================================
  // SettingsViewModel — curated, validated, atomic editor for dispatch.json +
  // startup.json (factory.json shown read-only; secrets never serialized). The
  // mapping editor maps to DispatchMapping{labels, agent}. Save sends the
  // COMPLETE edited config (merged onto the values read) through PUT
  // /api/settings/{file}, which routes it to `af config <file> set` — the single
  // canonical validator/writer. Friendly per-field errors are surfaced inline.
  // =========================================================================
  var SettingsViewModel = {
    data: null,    // the full GET /api/settings document
    agents: [],    // secret-free agent summaries (the mapping picker's options)

    activate: function () { showSettings(); return this.load(); },
    load: function () {
      var self = this;
      return API.get('/api/settings').then(function (env) {
        if (!env || !env.ok) { toast((env && env.message) || 'settings read failed'); return; }
        self.data = env.data || {};
        self.agents = self.data.agents || [];
        renderSettings();
      }).catch(function (e) { toast(String(e)); });
    },
    addRow: function () {
      var host = byId('mapRows'); if (host) host.appendChild(settingsMapRow(null));
    },
    save: function () {
      var self = this;
      if (!this.data) return;
      ['set-dispatch-err', 'set-dispatch-ok', 'set-startup-err', 'set-startup-ok'].forEach(hide);
      var btn = byId('set-save'); if (btn) { btn.disabled = true; btn.textContent = 'Saving…'; }

      // dispatch.json: preserve everything we read, override the operator-editable trigger + routes.
      var disp = shallowCopy(this.data.dispatch);
      disp.trigger_label = (byId('set-trigger').value || '').trim();
      disp.mappings = collectMappings();

      // startup.json: preserve what we read, override the two curated fields.
      var st = shallowCopy(this.data.startup);
      var agentsStr = (byId('set-startup-agents').value || '').trim();
      st.agents = agentsStr === '' ? null : agentsStr.split(',').map(function (s) { return s.trim(); }).filter(Boolean);
      st.start_dispatch = byId('set-startdispatch').checked;

      return API.put('/api/settings/dispatch', disp).then(function (env) {
        if (env && env.ok) { show('set-dispatch-ok'); }
        else { showValidation('set-dispatch-err', (env && env.message) || 'dispatch save failed'); }
        return API.put('/api/settings/startup', st);
      }).then(function (env) {
        if (env && env.ok) { show('set-startup-ok'); }
        else { showValidation('set-startup-err', (env && env.message) || 'startup save failed'); }
        // re-read so the editor reflects af's normalization (e.g. lone label → labels) on success.
        return self.load();
      }).catch(function (e) { toast(String(e)); }).then(function () {
        if (btn) { btn.disabled = false; btn.textContent = 'Save changes'; }
      });
    }
  };

  function renderSettings() {
    var d = SettingsViewModel.data || {};
    var disp = d.dispatch || {};
    var st = d.startup || {};

    byId('set-trigger').value = disp.trigger_label || '';

    var host = byId('mapRows'); host.innerHTML = '';
    (disp.mappings || []).forEach(function (m) { host.appendChild(settingsMapRow(m)); });

    byId('set-startup-agents').value = (st.agents || []).join(', ');
    byId('set-startdispatch').checked = !!st.start_dispatch;

    byId('set-factory').textContent = JSON.stringify(d.factory || {}, null, 2);

    ['set-dispatch-err', 'set-dispatch-ok', 'set-startup-err', 'set-startup-ok'].forEach(hide);
  }

  // settingsMapRow builds one label→agent row. mapping may be null (a fresh, empty row). The label
  // input shows either the lone `label` or the first of `labels` (af normalizes either form on save).
  function settingsMapRow(mapping) {
    var row = el('div', 'map-row');
    var labelVal = mapping ? (mapping.label || (mapping.labels && mapping.labels[0]) || '') : '';

    var input = el('input', 'field');
    input.setAttribute('aria-label', 'Label');
    input.setAttribute('data-role', 'label');
    input.value = labelVal;
    row.appendChild(input);

    row.appendChild(el('span', 'ar', '→'));

    var sel = el('select', 'field');
    sel.setAttribute('aria-label', 'Agent');
    sel.setAttribute('data-role', 'agent');
    SettingsViewModel.agents.forEach(function (a) {
      var o = el('option', null, a.name); o.value = a.name;
      if (mapping && mapping.agent === a.name) o.selected = true;
      sel.appendChild(o);
    });
    // if the mapping references an agent no longer in agents.json, keep it visible (don't silently drop).
    if (mapping && mapping.agent && !SettingsViewModel.agents.some(function (a) { return a.name === mapping.agent; })) {
      var o2 = el('option', null, mapping.agent + ' (unknown)'); o2.value = mapping.agent; o2.selected = true;
      sel.appendChild(o2);
    }
    row.appendChild(sel);

    var rm = el('button', 'rm', '×');
    rm.type = 'button';
    rm.setAttribute('aria-label', 'Remove route');
    rm.addEventListener('click', function () { row.remove(); });
    row.appendChild(rm);

    return row;
  }
  function collectMappings() {
    var out = [];
    document.querySelectorAll('#mapRows .map-row').forEach(function (row) {
      var label = (row.querySelector('[data-role="label"]').value || '').trim();
      var agentSel = row.querySelector('[data-role="agent"]');
      var agent = agentSel ? agentSel.value : '';
      if (!label || !agent) return;
      out.push({ labels: [label], agent: agent }); // emit the `labels` form (af normalizes either way)
    });
    return out;
  }
  function shallowCopy(o) {
    var out = {}; o = o || {};
    for (var k in o) { if (Object.prototype.hasOwnProperty.call(o, k)) out[k] = o[k]; }
    return out;
  }
  function show(id) { var e = byId(id); if (e) e.hidden = false; }
  function hide(id) { var e = byId(id); if (e) e.hidden = true; }
  function showValidation(id, msg) { var e = byId(id); if (e) { e.textContent = msg; e.hidden = false; } }

  // =========================================================================
  // AgentDetailViewModel — the sixth view (route "agent/<name>", #500). Renders
  // the honest per-agent detail from GET /api/agents/{name}/detail: a status
  // header + receipt-anchored freshness, a conditional gate banner, the
  // facts grid (RUNNING vs DECLARED formula kept DISTINCT — #455), a read-only
  // session snapshot filled via textContent ONLY, and a mail composer.
  // Ages anchor to Date.now() at RESPONSE RECEIPT — the
  // clock-skew-vulnerable server-stamp math in tickStale is deliberately NOT reused.
  // =========================================================================
  var AgentDetailViewModel = {
    name: '',
    data: null,        // last successful detail payload {agent, declared_formula, tail}
    receivedAt: 0,     // Date.now() at receipt of `data` — the anchor for every age
    stale: false,      // true after a failed refresh: keep the last snapshot, warn honestly, keep polling

    activate: function (name) {
      this.name = name;
      this.data = null;
      this.receivedAt = 0;
      this.stale = false;
      byId('agent-name').textContent = name;
      byId('agent-mail-ok').hidden = true;
      hide('agent-mail-err');
      showView('view-agent');
      renderAgentDetail(); // paint the loading shell immediately
      return this.refresh();
    },
    refresh: function () {
      var self = this;
      var name = this.name;
      if (!name) return;
      return API.get('/api/agents/' + encodeURIComponent(name) + '/detail').then(function (env) {
        if (self.name !== name) return; // a newer navigation won
        if (!env || !env.ok) {
          self.stale = true;   // keep the last snapshot; the freshness line flips to the explicit warning
          renderAgentDetail();
          return;
        }
        self.data = env.data || {};
        self.receivedAt = Date.now();
        self.stale = false;
        renderAgentDetail();
      }).catch(function () {
        if (self.name !== name) return;
        self.stale = true;
        renderAgentDetail();
      });
    },
    send: function () {
      var self = this;
      var name = this.name;
      if (!name) return;
      var subject = String((byId('agent-mail-subject') || {}).value || '').trim();
      var body = String((byId('agent-mail-body') || {}).value || '').trim();
      hide('agent-mail-err');
      byId('agent-mail-ok').hidden = true;
      if (!subject) { showAgentMailError('Add a subject before sending.'); return; }
      if (!body) { showAgentMailError('Write a message before sending.'); return; }
      var btn = byId('agent-mail-send');
      if (btn) { btn.disabled = true; btn.textContent = 'Sending…'; }
      return API.post('/api/agents/' + encodeURIComponent(name) + '/mail', { subject: subject, body: body }).then(function (env) {
        if (btn) { btn.disabled = false; btn.textContent = 'Send mail'; }
        if (env && env.ok) {
          var msg = 'Mail queued for ' + name + ' — the agent reads it when it next checks its inbox.';
          if (self.data && self.data.tail && self.data.tail.live) {
            msg += ' It will see a notification banner in its session.';
          }
          byId('agent-mail-ok-msg').textContent = msg;
          byId('agent-mail-ok').hidden = false;
          byId('agent-mail-ok').scrollIntoView({ block: 'nearest' });
          byId('agent-mail-subject').value = '';
          byId('agent-mail-body').value = '';
        } else {
          showAgentMailError((env && env.message) || 'mail failed');
        }
        return env;
      }).catch(function (e) {
        if (btn) { btn.disabled = false; btn.textContent = 'Send mail'; }
        showAgentMailError(String(e));
      });
    }
  };

  // Receipt-anchored age formatter: the argument is a millisecond delta measured from
  // Date.now() at RESPONSE RECEIPT — never new Date(serverStamp), which tickStale uses and which is
  // clock-skew-vulnerable. Returns a trailing-"ago" phrase (or "just now").
  function agoText(ms) {
    var s = Math.max(0, Math.round((ms || 0) / 1000));
    if (s < 2) return 'just now';
    if (s < 60) return s + 's ago';
    if (s < 3600) return Math.round(s / 60) + 'm ago';
    if (s < 86400) return Math.round(s / 3600) + 'h ago';
    return Math.round(s / 86400) + 'd ago';
  }

  function renderAgentDetail() {
    var vm = AgentDetailViewModel;
    var d = vm.data;
    var agent = (d && d.agent) || null;

    var badgeHost = byId('agent-badge');
    if (badgeHost) {
      badgeHost.innerHTML = '';
      var st = STATUS[(agent && agent.status)] || STATUS.idle;
      var badge = el('span', st.neutral ? 'badge neutral' : 'badge');
      if (!st.neutral) badge.appendChild(el('span', 'pip'));
      badge.appendChild(document.createTextNode(st.label));
      badgeHost.appendChild(badge);
    }

    // Freshness strip — receipt-anchored. Stale ⇒ explicit warning; else "Ns ago".
    var fresh = byId('agent-fresh');
    if (fresh) {
      if (vm.stale && vm.receivedAt) { fresh.textContent = 'snapshot may be stale — last captured ' + agoText(Date.now() - vm.receivedAt) + '; retrying'; }
      else if (vm.receivedAt) { fresh.textContent = 'updated ' + agoText(Date.now() - vm.receivedAt); }
      else if (vm.stale) { fresh.textContent = 'could not load — retrying'; }
      else { fresh.textContent = 'loading…'; }
    }

    // Gate banner (conditional): only when the agent is parked at a gate; the composer sits below it.
    var gate = byId('agent-gate');
    if (gate) {
      var isGate = !!(agent && agent.is_gate);
      gate.hidden = !isGate;
      if (isGate) { byId('agent-gate-text').textContent = 'Parked at gate ' + (agent.gate_id || '—') + ' — your input is needed'; }
    }

    renderAgentFacts(agent, d);
    renderAgentSnapshot(d);
  }

  // Facts grid. RUNNING (agent.formula) vs CONFIGURED (declared_formula) are DISTINCT labels —
  // never merged (#455). A reset agent with no recorded run state renders an honest empty state,
  // never blank cells that imply retained-but-hidden data.
  function renderAgentFacts(agent, d) {
    var host = byId('agent-facts'); if (!host) return;
    host.innerHTML = '';
    if (!agent) { host.appendChild(factRow('status', 'loading…')); return; }
    var noRun = !agent.step_id && !agent.step_state && !agent.formula;
    host.appendChild(factRow('status', (STATUS[agent.status] || STATUS.idle).label));
    host.appendChild(factRow('running formula', agent.formula || '—'));
    host.appendChild(factRow('configured formula', (d && d.declared_formula) || '—'));
    if (noRun) {
      host.appendChild(factRow('step', 'no recorded run state'));
    } else {
      host.appendChild(factRow('step', (agent.step_title || agent.step_id || '—') + (agent.step_state ? ' · ' + agent.step_state : '')));
    }
    host.appendChild(factRow('gate', agent.is_gate ? ('parked · ' + (agent.gate_id || '—')) : 'none'));
    if (agent.inputs && Object.keys(agent.inputs).length) {
      Object.keys(agent.inputs).forEach(function (k) { host.appendChild(factRow(k, String(agent.inputs[k]))); });
    }
  }
  function factRow(k, v) {
    var row = el('div', 'fact');
    row.appendChild(el('span', 'fk', k));
    row.appendChild(el('span', 'fv', v));
    return row;
  }

  // Read-only session snapshot. The pane is filled via textContent ONLY — NEVER
  // innerHTML — so terminal bytes can never inject markup. live=false renders the last-known state
  // plus a hint that mail still queues normally (the bead persists; only the delivery banner is
  // skipped); a per-pane "captured Ns ago" is receipt-anchored.
  function renderAgentSnapshot(d) {
    var pre = byId('agent-snapshot'); if (!pre) return;
    var note = byId('agent-snapshot-note');
    var captured = byId('agent-snapshot-captured');
    var tail = (d && d.tail) || null;
    // Follow the newest output ONLY when the operator is already pinned to the bottom, so a manual
    // scroll-back to read older output is never yanked away by the 5s refresh.
    var pinned = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 4;
    if (!tail) {
      pre.textContent = '';
      if (note) note.hidden = true;
      if (captured) captured.hidden = true;
    } else if (tail.live === false) {
      pre.textContent = tail.output || '';
      if (note) {
        note.textContent = 'No live session — showing last-known agent state. This agent is stopped — your mail will wait in its inbox.';
        note.hidden = false;
      }
      if (captured) captured.hidden = true;
    } else {
      pre.textContent = tail.output || '';
      if (note) note.hidden = true;
      if (captured) {
        if (tail.captured_at) { captured.textContent = 'captured ' + agoText(Date.now() - AgentDetailViewModel.receivedAt); captured.hidden = false; }
        else { captured.hidden = true; }
      }
    }
    if (pinned) pre.scrollTop = pre.scrollHeight;
  }

  function showAgentMailError(msg) {
    var box = byId('agent-mail-err'); if (!box) return;
    box.textContent = msg || 'mail failed';
    box.hidden = false;
    box.scrollIntoView({ block: 'nearest' });
  }

  // =========================================================================
  // TelemetryViewModel — the eighth view (route "telemetry", #580 W5).
  //
  // Registers NO timer. Decision 9: on-demand plus an explicit Refresh, no auto-poll and no
  // auto-retry against a rendered dark state — this panel makes the heaviest requests against a
  // routinely-dead upstream, and every fetch is a process spawn.
  //
  // All three reads are dispatched CONCURRENTLY. The obvious reading — "status first, it is the
  // fast local one" — is backwards: the status handler's budget is 35s because the CLI probes three
  // signals sequentially at a 10s ceiling each, while report is 5s and usage 15s. Status is the
  // SLOWEST read, and it is slowest precisely when the backend is dead, which is the case this
  // panel exists to serve. Chaining would blank the view for half a minute.
  // =========================================================================
  var TelemetryViewModel = {
    seq: 0,            // monotonic request token; a response from an older seq is dropped
    status: null,      // last successful StateDTO
    report: null,      // last successful ReportDTO
    usage: null,       // last successful UsageDTO
    statusFail: '',    // relay/transport failure text, per pane — never blanks the pane
    reportFail: '',
    usageFail: '',
    filterFail: '',    // a 400 from the filter validators
    receivedAt: 0,     // Date.now() at the most recent successful receipt
    agent: '',
    instance: '',
    options: null,     // option sets frozen at the last UNFILTERED response

    activate: function () {
      showTelemetry();
      renderTelemetry();                       // paint the shell synchronously, before any fetch
      return this.refresh();
    },

    refresh: function () {
      var self = this;
      this.seq += 1;
      var mine = this.seq;
      var q = '';
      if (this.agent) { q += (q ? '&' : '?') + 'agent=' + encodeURIComponent(this.agent); }
      if (this.instance) { q += (q ? '&' : '?') + 'instance=' + encodeURIComponent(this.instance); }

      var fresh = function (env, kind) {
        if (self.seq !== mine) { return null; }   // a newer refresh won; drop this response
        if (!env || env.ok !== true) {
          if (env && env._status === 400) { self.filterFail = env.message || 'the filter was rejected'; }
          self[kind] = env && env.message ? env.message : 'the telemetry command could not be read; see the console log';
          return null;
        }
        self[kind] = '';
        self.receivedAt = Date.now();
        return env.data || null;
      };

      this.filterFail = '';
      var s = API.get('/api/telemetry').then(function (env) {
        var d = fresh(env, 'statusFail'); if (d) { self.status = d; }
        renderTelemetry();
      }, function () { if (self.seq === mine) { self.statusFail = 'the console could not reach its own telemetry route'; renderTelemetry(); } });

      var r = API.get('/api/telemetry/report' + q).then(function (env) {
        var d = fresh(env, 'reportFail'); if (d) { self.report = d; self.freezeOptions(d); }
        renderTelemetry();
      }, function () { if (self.seq === mine) { self.reportFail = 'the console could not reach its own telemetry route'; renderTelemetry(); } });

      var u = API.get('/api/telemetry/usage' + q).then(function (env) {
        var d = fresh(env, 'usageFail'); if (d) { self.usage = d; }
        renderTelemetry();
      }, function () { if (self.seq === mine) { self.usageFail = 'the console could not reach its own telemetry route'; renderTelemetry(); } });

      return Promise.all([s, r, u]);
    },

    // The option sets are frozen at the last UNFILTERED response. Once a filter is applied the
    // response only contains that filter's values, so deriving options from it would collapse the
    // list to the single selected entry.
    freezeOptions: function (rep) {
      if (this.agent || this.instance) { return; }
      var agents = [], runs = [];
      (rep.rows || []).forEach(function (row) {
        if (row.agent && agents.indexOf(row.agent) < 0) { agents.push(row.agent); }
        if (row.instance_id && runs.indexOf(row.instance_id) < 0) { runs.push(row.instance_id); }
      });
      this.options = { agents: agents.sort(), runs: runs.sort() };
    },

    setFilter: function (agent, instance) {
      this.agent = agent;
      this.instance = instance;
      return this.refresh();
    }
  };

  // sev distinguishes a MEASURED failure from a state that was never measured and from a positive
  // verdict. Collapsing the three would put "we looked and it is broken" and "we never looked" in
  // the same visual bucket, which is the whole distinction this panel exists to preserve.
  function telLine(host, text, next, sev) {
    var wrap = el('div', 'tel-line' + (sev ? ' tel-sev-' + sev : ''));
    wrap.appendChild(el('p', 'tel-line-what', text));
    if (next) { wrap.appendChild(el('p', 'tel-line-next', next)); }
    host.appendChild(wrap);
  }

  // renderTelemetryBanner emits the banner STACK: one line per degraded axis, in the fixed order
  // installed -> recording -> backend. n degraded axes produce n lines, so all eight combinations of
  // the axis space render distinctly with no combinatorial UI.
  function renderTelemetryBanner(vm) {
    var host = byId('tel-banner'); if (!host) return;
    host.innerHTML = '';
    var status = vm.status;

    if (vm.statusFail) {
      telLine(host, "The console could not read this factory's telemetry status: " + vm.statusFail,
        'Nothing was measured, so no state below is a verdict. Check the console log.', 'unknown');
      return;
    }
    if (!status) { telLine(host, 'Reading telemetry status…', 'The status probe can take up to 35 seconds when the backend is unreachable.', 'unknown'); return; }

    // The error envelope is a different key set entirely — {v, state, error} with no axes at all.
    // Reading installed/recording/backend off it would silently render "not installed".
    if (status.state === 'error') {
      telLine(host, "The console could not read this factory's telemetry: " + (status.error || 'the factory could not be resolved'),
        'Run the console from inside an agentfactory workspace.', 'unknown');
      return;
    }

    var inst = status.installed || {};
    var degradedInstalled = !inst.present || !inst.valid || inst.endpoint === '';
    if (degradedInstalled) {
      if (!inst.present) {
        // The CLI's clause is a conditional future ("will be recorded"), and it is emitted only from
        // the `af telemetry on` branch. Restating it in the present tense here would assert local
        // recording in the default state, one line above the "Recording is off" line that
        // contradicts it.
        telLine(host, 'Telemetry is not configured: no telemetry.json found — step timing is recorded locally only while recording is on.',
          'Next: run quickstart.sh with telemetry or create .agentfactory/telemetry.json to export.');
      } else if (!inst.valid) {
        telLine(host, 'The telemetry configuration could not be read.',
          'Next: run quickstart.sh with telemetry or create .agentfactory/telemetry.json to export.');
      } else {
        telLine(host, 'No endpoint is configured in .agentfactory/telemetry.json — there is nothing to query.',
          'Next: run quickstart.sh with telemetry or create .agentfactory/telemetry.json to export.');
      }
    }

    var recording = status.recording || {};
    if (recording.enabled === false) {
      telLine(host, 'Recording is off — this is the current recording state, and existing records remain readable below.',
        'Turn it back on with: af telemetry on');
    }

    // Is the configured address one this factory can actually supervise? ensureTelemetryBackend
    // declines any non-loopback endpoint outright (internal/cmd/telemetry_backend.go:29-33), so
    // this predicate decides which recoveries the panel may honestly offer below. Computed here,
    // above the backend axis, because the axis needs it; the external-endpoint leaf further down
    // reads the same two values rather than deriving them a second time.
    //
    // The host test is anchored so a name that merely CONTAINS a loopback spelling —
    // //localhost.evil.com — is not read as loopback, which would restore exactly the false
    // promise this gate exists to remove. It stays narrower than Go's IsLoopbackEndpoint in the
    // other direction (127.0.0.2 is loopback to Go, not to this test): erring toward withholding
    // a promise is the safe direction, erring toward making one is not.
    var ep = inst.endpoint || '';
    var loopback = (function () {
      // Read the AUTHORITY and compare the whole host. Anything looser is a substring match on
      // a URL, and a URL has many places a loopback spelling can appear without being the host:
      // //localhost.evil.com is a different site, and /api?redir=//localhost is a query value.
      // Both would otherwise be told a local backend will be relaunched for them.
      var at = ep.indexOf('//');
      if (at < 0) { return false; }
      var rest = ep.slice(at + 2);
      var end = rest.length;
      ['/', '?', '#'].forEach(function (c) {
        var i = rest.indexOf(c);
        if (i >= 0 && i < end) { end = i; }
      });
      var host = rest.slice(0, end);
      var creds = host.lastIndexOf('@');
      if (creds >= 0) { host = host.slice(creds + 1); }
      if (host.charAt(0) === '[') {
        var close = host.indexOf(']');
        if (close >= 0) { host = host.slice(0, close + 1); }
      } else {
        var port = host.indexOf(':');
        if (port >= 0) { host = host.slice(0, port); }
      }
      // The whole 127/8 block, not just 127.0.0.1: config.IsLoopbackEndpoint defers to Go's
      // net.IP.IsLoopback (internal/config/endpoint.go:23-35), so the guard DOES supervise
      // 127.0.0.2. Reading that as remote would make the other arm's sentence — "this factory
      // does not start or supervise a backend at the address you configured" — positively
      // false, which is worse than the withheld promise it replaced.
      if (host === 'localhost' || host === '[::1]' || host === '127.0.0.1') { return true; }
      var v4 = host.split('.');
      return v4.length === 4 && v4[0] === '127' && v4.every(function (part) {
        return part !== '' && String(Number(part)) === part && Number(part) >= 0 && Number(part) <= 255;
      });
    })();

    // Backend axis, three-valued. probed:false means no measurement was taken — rendering that as
    // healthy would assert a measurement that never happened.
    var backend = status.backend || {};
    if (backend.probed === true) {
      // The recovery on offer depends on whether anything in this factory owns this address.
      // Promising an automatic relaunch for a company collector would tell the operator to wait
      // ~30s for an attempt the guard returns from on every af up and every watchdog tick,
      // forever — and the bash -l fallback would start a LOCAL backend that is not the endpoint
      // they configured. The loopback arm still cannot promise the relaunch script EXISTS (a
      // --no-telemetry install never wrote one, and no field in the status payload carries that
      // fact), so it states its condition rather than asserting the outcome.
      var backendDownNext = loopback
        ? 'Next: af up (or the next watchdog tick, within ~30s) relaunches it automatically, '
          + 'provided the recording gate is on and this factory installed the bundled backend. '
          + 'If it is still down after that, start a login shell (bash -l) as a manual fallback '
          + '— the same guard runs there too. If the session is alive but unresponsive, '
          + 'tmux attach -t telemetry, but only if tmux has-session -t telemetry succeeds.'
        : 'Next: this factory does not start or supervise a backend at the address you '
          + 'configured — af up and the watchdog only tend the bundled one on this machine, and '
          + 'bash -l would start that local backend instead of reaching yours. Check that the '
          + 'configured address is up and reachable from here.';
      // Correlated-failure dedup (contributing gap #7): connection-refused on every signal is ONE
      // upstream fact (the backend is down), not three independent ones — collapsing it to one line
      // is the banner's half of the same fix the steps table's rowSpan stamp is the other half of.
      // Per-signal lines stay for any MIXED combination, which is the case the per-signal design
      // was built for and the only shape where each line still carries distinct information.
      var allRefused = (backend.signals || []).length > 0
        && (backend.signals || []).every(function (sig) { return sig.status === 0; });
      if (allRefused) {
        // One remedy instead of N — but the CAUSES stay. status === 0 is what probeOne leaves
        // whenever the request never completed, which covers DNS failure, i/o timeout and TLS
        // rejection as well as connection refused; naming any one of them here would assert a
        // cause the panel never measured. Each Summary() already says what actually happened,
        // and the count and labels come from the payload — Probe returns one entry per address
        // (1 + len(nativeSignalPaths)), so a fourth would make a hardcoded "three" a lie.
        telLine(host, 'The backend is unreachable — every address failed: '
          + (backend.signals || [])
            .map(function (sig) { return sig.summary; })
            // The payload always carries a summary (Summary() is never empty and the DTO has
            // no omitempty), so this only guards the degenerate case — but a dangling "; ; "
            // in the middle of the one line an operator gets is worse than a shorter list.
            .filter(function (summary) { return summary; })
            .join('; ') + '.',
          backendDownNext);
      } else {
        (backend.signals || []).forEach(function (sig) {
          if (sig.ok) { return; }
          var next;
          if (sig.status === 401 || sig.status === 403) {
            next = 'Check .agentfactory/secrets/telemetry.root, or re-run the quickstart credential step.';
          } else if (sig.status === 404) {
            next = 'Check that the configured endpoint ends in /api/default.';
          } else if (sig.status === 0) {
            next = backendDownNext;
          } else {
            next = 'The backend refused the data; read the backend\'s own log.';
          }
          telLine(host, sig.summary, next);
        });
      }
    } else if (status.unprobed_cause) {
      telLine(host, 'The backend was not probed: ' + status.unprobed_cause,
        'Check .agentfactory/secrets/telemetry.root, or re-run the quickstart credential step.', 'unknown');
    }

    // External endpoint: non-loopback AND the usage query failed. The AND is load-bearing — a
    // working remote backend must render as healthy, not as unsupported. The test is on the ADDRESS
    // only, which is all the panel knows; it makes no claim about what is running there. The port is
    // not compared (the seed's port is a variable) and no path is required (an external OTLP stack
    // legitimately uses a different one). Nothing here fingerprints the backend.
    var usage = vm.usage;
    var usageBroken = usage && usage.state !== 'ok' && usage.state !== 'not_installed';
    if (ep !== '' && !loopback && usageBroken) {
      telLine(host, "The console's telemetry views expect the bundled backend; your endpoint is external, which may be why this query failed.",
        "Next: use that backend's own UI for token usage and session metrics.", 'unknown');
    }

    // Healthy-empty is a banner-level verdict, and "healthy" means NO axis emitted a line — not
    // merely that a probe ran. Reading it off backend.probed would print "everything is wired"
    // directly beneath a 404. Counting what the axes above actually emitted is the only reading that
    // cannot drift from them.
    var axisLines = host.children.length;
    var rep = vm.report;
    var st = (rep && rep.stats) || {};
    // DroppedUnexported is a SUBSET of Dropped (internal/telemetry/store.go:56-64), so testing it
    // separately would be redundant; it is carried in the sentence, not in the predicate.
    var lossy = st.malformed > 0 || st.dropped > 0;
    var filtered = vm.agent !== '' || vm.instance !== '';
    if (axisLines === 0 && rep && (rep.rows || []).length === 0 && !lossy && !filtered) {
      telLine(host, "No step records yet. Enable telemetry with 'af telemetry on' and run a formula.",
        'Everything is wired — this factory simply has not recorded a step yet.', 'ok');
    }
    if (lossy) {
      // The integers, never a stand-in. A fabricated "some" here would contradict the exact counts
      // the steps pane prints from the same payload, on the same screen.
      telLine(host, st.malformed + ' unreadable lines skipped; ' + st.dropped + ' records dropped (' + st.dropped_unexported + ' never reached a backend).',
        'Records exist that could not be read — this is not an empty factory.');
    }

    if (!host.firstChild) {
      telLine(host, 'Telemetry is installed, recording, and every backend signal answered.',
        'Nothing to act on.', 'ok');
    }
  }

  // renderTelemetrySteps renders the step-timing rows and each row's token verdict.
  //
  // The join is RUN-LEVEL, on instance_id <-> formula_run plus agent, and that is not a shortcut.
  // The report row's `step` is the step TITLE when the step has one, falling back to its id; the
  // usage row's `step_bucket` is always the step ID, optionally suffixed "-tail". The report payload
  // carries no step id at all, so comparing the two fields marks every titled step as having no
  // token data — firing the honesty copy universally and falsely. What the run-level key does prove
  // is one-directional and sufficient: the backend view builds every token row by joining on
  // (formula_run, agent), so no rows for that pair means no rows for any step of it.
  function renderTelemetrySteps(vm) {
    var host = byId('tel-steps'); if (!host) return;
    var note = byId('tel-steps-note');
    host.innerHTML = '';
    if (note) { note.textContent = ''; }

    if (vm.reportFail) {
      host.appendChild(el('p', 'tel-empty', 'The console could not read the step timings: ' + vm.reportFail));
      if (note) { note.textContent = 'The last reading, if any, is unchanged. Nothing was re-measured.'; }
      return;
    }
    var rep = vm.report;
    if (!rep) { host.appendChild(el('p', 'tel-empty', 'Step timings still loading…')); return; }
    if (rep.state === 'error') {
      host.appendChild(el('p', 'tel-empty', 'The console could not read this factory\'s telemetry: ' + (rep.error || 'the factory could not be resolved')));
      return;
    }

    var rows = rep.rows || [];
    var rst = rep.stats || {};
    // Loss is reported BEFORE the empty check, never after it — a log whose every line is corrupt
    // renders no rows, and "no records yet" is exactly the wrong thing to tell an operator whose
    // records exist and cannot be read. DroppedUnexported is a SUBSET of Dropped
    // (internal/telemetry/store.go:56-64), so it is reported in the sentence, not tested separately.
    var lost = rst.malformed > 0 || rst.dropped > 0;
    if (lost) {
      host.appendChild(el('p', 'tel-empty', rst.malformed + ' unreadable lines skipped; ' + rst.dropped + ' records dropped (' + rst.dropped_unexported + ' never reached a backend).'));
    }
    if (rows.length === 0) {
      if (lost) {
        host.appendChild(el('p', 'tel-empty', 'No readable step records — this is not an empty factory. The records above could not be read.'));
      } else if (vm.agent || vm.instance) {
        host.appendChild(el('p', 'tel-empty', 'No step records match the filter you applied (agent "' + (vm.agent || 'any') + '", run "' + (vm.instance || 'any') + '").'));
      } else {
        host.appendChild(el('p', 'tel-empty', "No step records yet. Enable telemetry with 'af telemetry on' and run a formula."));
      }
      return;
    }

    // "Not measured yet" is not "measured, and empty". The three reads are concurrent with very
    // different budgets (report 5s, usage 15s, status 35s), so the report reliably lands first;
    // treating an absent usage payload as an empty one would assert "no token data arrived for this
    // step" against every row before the token query has even returned — an explicit unknown stated
    // before the measurement exists, which is the same defect class as a zero.
    var usageKnown = vm.usage !== null && vm.usageFail === '';
    // usageKnown is a TRANSPORT predicate, and transport is not measurement. The relay carries
    // degradation as data — every dark state arrives at HTTP 200 with ok:true, and ok:false is
    // reserved for a relay failure where nothing was measured at all — so usageKnown is true for
    // not_installed, backend_down, credential_rejected and query_failed alike, with rows [] in
    // every one of them. Deciding the column from transport alone therefore reports a measured
    // negative about a query that never ran: on a default local-recording factory the Tokens pane
    // says nothing was queried while this column asserts data was expected and did not arrive.
    // The payload states its own verdict; read that, exactly as the Tokens and Metrics panes do.
    //
    // The TOKENS half is the right axis, not the top-level state: that one is the worst of the two
    // halves, so a dark metrics half would suppress token rows that did arrive.
    var tok = (usageKnown && vm.usage.tokens) || {};
    var usageUsable = usageKnown && vm.usage.state !== 'error' && tok.state === 'ok';
    var tokenRows = usageUsable ? (tok.rows || []) : [];
    var table = el('table', 'tel-table');
    var head = el('tr');
    ['Agent', 'Step', 'Status', 'Duration', 'Model', 'Token data'].forEach(function (h) { head.appendChild(el('th', '', h)); });
    table.appendChild(head);

    // Dark-state — nothing was measured for ANY step, because nothing was measured at all. This
    // is a fact about the whole query, not about any one row, so it is decided ONCE here rather
    // than re-derived per row: one upstream fact stamped N times is the same correlated-failure
    // repetition contributing gap #7 names for the banner, one column over. usageKnown is checked
    // first (state check before the joinless arm — the same ordering checkJoinlessRenderer pins),
    // and only the usageUsable half gets the shared sentence: telUsageStateText is the generator
    // the Tokens and Metrics panes already use for the same five states, so the three panes on
    // screen keep agreeing instead of offering three accounts of one fact.
    var stepsDark = !usageKnown || !usageUsable;
    var stepsDarkClass = !usageKnown ? 'tel-pending' : 'tel-unknown';
    var stepsDarkText = !usageKnown
      ? (vm.usageFail !== '' ? 'token usage could not be read' : 'token usage still loading')
      : telUsageStateText(tok.state, vm.usage.cause || tok.detail);
    var darkStamped = false;

    rows.forEach(function (r) {
      var tr = el('tr');
      tr.appendChild(el('td', '', r.agent));
      tr.appendChild(el('td', '', r.step));
      tr.appendChild(el('td', '', r.status));
      tr.appendChild(el('td', '', r.status === 'open' ? formatMs(r.duration_ms) + ' so far' : formatMs(r.duration_ms)));
      tr.appendChild(el('td', '', r.model === '' ? '—' : r.model));

      if (stepsDark) {
        if (!darkStamped) {
          var dark = el('td', stepsDarkClass, stepsDarkText);
          dark.rowSpan = rows.length;
          tr.appendChild(dark);
          darkStamped = true;
        }
        // Every row after the first spans under the stamp above — appending a cell here would
        // widen the table by one column past what rowSpan already covers.
        table.appendChild(tr);
        return;
      }

      // An empty key on either side is not a match. Two un-hooked records both carrying "" would
      // otherwise join one session's steps to another's tokens.
      var runMatch = [];
      tokenRows.forEach(function (t) {
        if (t.formula_run !== '' && r.instance_id !== '' && t.formula_run === r.instance_id && t.agent === r.agent) { runMatch.push(t); }
      });

      if (runMatch.length === 0) {
        tr.appendChild(el('td', 'tel-unknown', 'no token data arrived for this step'));
      } else {
        tr.appendChild(el('td', 'tel-unknown', 'recorded for this run, not attributable to a step'));
      }
      table.appendChild(tr);
    });
    host.appendChild(table);

    if (note) {
      note.textContent = 'Token counts are attributed per run, not per step: the timing records carry a step title while the backend buckets tokens by step id, so no per-step figure can be derived honestly. Token data for a run may also exist unattributed — attributes are stamped at launch, so a formula hooked mid-session reports under a stale or empty instance until the agent respawns.';
    }
  }

  function renderTelemetryTokens(vm) {
    var host = byId('tel-tokens'); if (!host) return;
    var note = byId('tel-tokens-note');
    var win = byId('tel-window');
    host.innerHTML = '';
    if (note) { note.textContent = ''; }

    if (vm.usageFail) {
      host.appendChild(el('p', 'tel-empty', 'The console could not read token usage: ' + vm.usageFail));
      return;
    }
    var u = vm.usage;
    if (!u) { host.appendChild(el('p', 'tel-empty', 'Token usage still loading…')); return; }
    if (win) { win.textContent = (u.window && u.window.limit > 0) ? ('rows capped at ' + u.window.limit) : 'no query window — nothing was queried'; }

    if (u.state === 'error') {
      host.appendChild(el('p', 'tel-empty', "The console could not read this factory's telemetry: " + (u.error || 'the factory could not be resolved')));
      return;
    }
    var tok = u.tokens || {};
    var rows = tok.rows || [];
    if (tok.state !== 'ok') {
      host.appendChild(el('p', 'tel-empty', telUsageStateText(tok.state, u.cause || tok.detail)));
      return;
    }
    if (rows.length === 0) {
      host.appendChild(el('p', 'tel-empty', (u.filters && (u.filters.agent || u.filters.instance))
        ? 'No token rows for agent "' + (u.filters.agent || 'any') + '", run "' + (u.filters.instance || 'any') + '".'
        : 'No token rows in this window.'));
      return;
    }

    var table = el('table', 'tel-table');
    var head = el('tr');
    ['Run', 'Agent', 'Model', 'Step bucket', 'Requests', 'Input', 'Output', 'Total'].forEach(function (h) { head.appendChild(el('th', '', h)); });
    table.appendChild(head);
    rows.forEach(function (t) {
      var tr = el('tr');
      tr.appendChild(el('td', '', t.formula_run === '' ? '(unattributed)' : t.formula_run));
      tr.appendChild(el('td', '', t.agent));
      tr.appendChild(el('td', '', t.model));
      // The "-tail" split is the product, not an artifact: it is the work a step's closing turn
      // finished after the step formally ended. Merging it would hide real cost.
      var bucket = String(t.step_bucket);
      if (bucket.length > 5 && bucket.slice(-5) === '-tail') {
        tr.appendChild(el('td', '', bucket.slice(0, -5) + ' (tail — work that continued after the step closed)'));
      } else {
        tr.appendChild(el('td', '', bucket));
      }
      tr.appendChild(el('td', '', String(t.requests)));
      tr.appendChild(el('td', '', String(t.input_tokens)));
      tr.appendChild(el('td', '', String(t.output_tokens)));
      tr.appendChild(el('td', '', String(t.total_tokens)));
      table.appendChild(tr);
    });
    host.appendChild(table);

    if (note && tok.truncated === true) {
      note.textContent = 'Showing ' + rows.length + ' of ' + tok.total + ' rows (capped at ' + ((u.window && u.window.limit) || 'the configured limit') + '). This list is not the total.';
    }
  }

  function renderTelemetryMetrics(vm) {
    var host = byId('tel-metrics'); if (!host) return;
    var note = byId('tel-metrics-note');
    host.innerHTML = '';
    if (note) { note.textContent = ''; }

    if (vm.usageFail) { host.appendChild(el('p', 'tel-empty', 'The console could not read session metrics: ' + vm.usageFail)); return; }
    var u = vm.usage;
    if (!u) { host.appendChild(el('p', 'tel-empty', 'Session metrics still loading…')); return; }

    if (u.state === 'error') {
      host.appendChild(el('p', 'tel-empty', "The console could not read this factory's telemetry: " + (u.error || 'the factory could not be resolved')));
      return;
    }
    var m = u.metrics || {};
    if (m.state !== 'ok') { host.appendChild(el('p', 'tel-empty', telUsageStateText(m.state, u.cause || m.detail))); return; }
    var rows = m.rows || [];
    if (rows.length === 0) {
      // Every metric was queried and every one succeeded — the rows are absent, not unmeasured.
      // Saying "no session metrics" here would report an idle factory, when the same shape is what
      // a renamed metric produces: the names this factory asks for no longer match the ones being
      // recorded, with recording on and the backend healthy.
      //
      // The condition is derived here rather than taken on trust: the CLI appends one state per
      // metric QUERIED, so state 'ok' with no rows means every query succeeded and none returned a
      // series. Deriving it keeps this honest when an older console fronts a newer af — which the
      // rendezvous no-op makes routine — and m.detail names which metrics went silent when it is
      // there. It is appended, never used as the condition, so a payload without it still gets the
      // truthful sentence rather than falling back to the misleading one.
      var quiet = 'No session metric returned a value, though every one was queried. This factory may simply be idle, or the metric names it asks for may no longer match the ones being recorded — the two look identical from here.';
      host.appendChild(el('p', 'tel-empty', m.detail ? quiet + ' (' + m.detail + ')' : quiet));
      return;
    }

    var table = el('table', 'tel-table');
    var head = el('tr');
    ['Metric', 'Agent', 'Instance', 'Value'].forEach(function (h) { head.appendChild(el('th', '', h)); });
    table.appendChild(head);
    rows.forEach(function (row) {
      var tr = el('tr');
      tr.appendChild(el('td', '', row.metric));
      tr.appendChild(el('td', '', row.agent === '' ? '—' : row.agent));
      tr.appendChild(el('td', '', row.instance === '' ? '—' : row.instance));
      tr.appendChild(el('td', '', row.value));       // a STRING on the wire; never coerced
      table.appendChild(tr);
    });
    host.appendChild(table);
    if (note) {
      note.textContent = 'Session metrics are an instant reading and do not honour the window above.';
    }
  }

  // The usage enum is closed at five values and carries only conditions that actually prevented or
  // degraded a query. Recording-off is deliberately absent: the gate never short-circuits the query,
  // so historical backend data stays readable after `af telemetry off`.
  function telUsageStateText(state, detail) {
    var tail = detail ? ' (' + detail + ')' : '';
    if (state === 'not_installed') { return 'No telemetry endpoint is configured, so nothing was queried' + tail + '.'; }
    if (state === 'backend_down') { return 'The backend did not answer the query' + tail + '.'; }
    if (state === 'credential_rejected') { return 'The backend was reachable but the credential was rejected' + tail + '.'; }
    if (state === 'query_failed') { return 'The query reached the backend and failed' + tail + '.'; }
    return 'The query did not complete' + tail + '.';
  }

  function formatMs(ms) {
    if (typeof ms !== 'number') { return 'unknown'; }
    if (ms < 1000) { return ms + 'ms'; }
    return (ms / 1000).toFixed(1) + 's';
  }

  function renderTelemetryChrome(vm) {
    var fresh = byId('tel-fresh');
    if (fresh) {
      // An ABSOLUTE stamp. A relative one ("12s ago") would need a timer to stay true, and this
      // panel registers none — it would freeze at its first value and quietly lie from then on.
      fresh.textContent = vm.receivedAt ? ('updated at ' + new Date(vm.receivedAt).toLocaleTimeString()) : 'not loaded yet';
    }
    var ep = byId('tel-endpoint');
    if (ep) {
      var inst = (vm.status && vm.status.installed) || null;
      ep.textContent = inst && inst.endpoint ? inst.endpoint : 'none configured';
    }
    var gate = byId('tel-gate');
    if (gate) {
      var rec = vm.status && vm.status.recording;
      var state = !rec ? 'not read yet' : (rec.enabled ? 'on' : 'off');
      gate.textContent = 'Current recording state (the gate): ' + state
        + '. The startup default is a separate artifact — the telemetry key in startup.json — and the two can legitimately differ.';
    }
    var err = byId('tel-filter-err');
    if (err) {
      err.textContent = vm.filterFail;
      err.hidden = !vm.filterFail;
    }
    telSyncOptions(vm);
  }

  function telSyncOptions(vm) {
    var opts = vm.options;
    if (!opts) { return; }
    telFillSelect(byId('tel-agent'), opts.agents, vm.agent, 'All agents');
    telFillSelect(byId('tel-instance'), opts.runs, vm.instance, 'All runs');
  }

  function telFillSelect(sel, values, current, allLabel) {
    if (!sel) { return; }
    sel.innerHTML = '';
    sel.appendChild(el('option', '', allLabel));
    sel.firstChild.value = '';
    var seen = false;
    values.forEach(function (v) {
      var o = el('option', '', v);
      o.value = v;
      if (v === current) { o.selected = true; seen = true; }
      sel.appendChild(o);
    });
    if (current && !seen) {
      var o = el('option', '', current + ' (unknown)');
      o.value = current;
      o.selected = true;
      sel.appendChild(o);
    }
  }

  function renderTelemetry() {
    var vm = TelemetryViewModel;
    renderTelemetryChrome(vm);
    renderTelemetryBanner(vm);
    renderTelemetrySteps(vm);
    renderTelemetryTokens(vm);
    renderTelemetryMetrics(vm);
  }

  // =========================================================================
  // AppViewModel — shell / nav / staleness.
  // =========================================================================
  var AppViewModel = {
    currentRoute: 'floor',
    lastUpdated: '',
    navigate: function (route) {
      // Parameterized detail route ("agent/<name>") is parsed FIRST — BEFORE the syncNav(route)
      // below — because a bare syncNav('agent/x') matches no nav anchor and would clear every
      // highlight. The Floor tab stays lit while a detail view is open.
      if (route.indexOf('agent/') === 0) {
        this.currentRoute = route;
        syncNav('floor');
        AgentDetailViewModel.activate(route.slice(6));
        return;
      }
      // #534: the formulas family lives in the transplanted prototype behind #formulaFrame.
      // Entering a route points the persistent iframe at the approved document; leaving the
      // family never unloads it (the section only hides), so unsaved edits survive tab switches.
      // In-frame navigation and dirty guarding are the prototype's own bytes. The formulas/new and
      // formulas/{name} branches below exist to satisfy the route-contract trace for the three
      // declared routes, but are currently unreachable: nothing in the shipped shell navigates to
      // them (no hash/popstate router; the transplant uses its own cross-document <a href> links and
      // never calls back into AppViewModel). Deep-linking is a declined/future enhancement
      // (design 502, Decision 10).
      if (route === 'formulas' || route === 'formulas/new' || route.indexOf('formulas/') === 0) {
        this.currentRoute = route;
        syncNav('formulas');
        showFormulas();
        var frame = byId('formulaFrame');
        if (!frame) { return; }
        if (route === 'formulas/new') { frame.src = '/formula-editor/screens/wizard.html'; return; }
        if (route === 'formulas') {
          if (!frame.getAttribute('src')) { frame.src = '/formula-editor/screens/roster.html'; }
          return;
        }
        var name = route.slice('formulas/'.length);
        API.get('/api/formulas/' + encodeURIComponent(name)).then(function (env) {
          if (env.ok && env.data && typeof env.data.text === 'string') {
            try {
              window.sessionStorage.setItem('af-open',
                JSON.stringify({ name: env.data.name + '.formula.toml', text: env.data.text, mode: 'demo' }));
            } catch (e) { /* handoff degraded — the editor boots its default */ }
            frame.src = '/formula-editor/screens/editor.html';
          } else {
            frame.src = '/formula-editor/screens/roster.html';
          }
        }, function () { frame.src = '/formula-editor/screens/roster.html'; });
        return;
      }
      this.currentRoute = route;
      syncNav(route);                                               // move the highlight first, for every route
      if (route === 'sling') { SlingViewModel.activate(); return; }
      if (route === 'dispatch') { DispatchViewModel.activate(); return; }
      if (route === 'settings') { SettingsViewModel.activate(); return; }
      if (route === 'prototypes') { PrototypesViewModel.activate(); return; }
      // #580: below syncNav(route) deliberately. Above it the highlight would never move; below
      // goHome() the branch would still run, but after the route had been reset to Floor.
      if (route === 'telemetry') { TelemetryViewModel.activate(); return; }
      if (route === 'floor') { showFloor(); return; }
      this.goHome();                                                // unknown route → home, silently
    },
    goHome: function () { this.currentRoute = 'floor'; syncNav('floor'); showFloor(); },
    refresh: function () { return FloorViewModel.refresh(); }
  };

  // ---- response handlers ----
  function report(okMsg) {
    return function (env) {
      if (env && env.ok) { toast(okMsg); }
      else if (env) {
        // 409 ⇒ busy/orchestrated; surface the friendly server message.
        toast(env.message || 'action failed');
      }
      return env;
    };
  }
  function done(vm) { return function (env) { vm.transitioning = false; FloorViewModel.refresh(); return env; }; }
  function showError(msg) {
    var box = byId('errbox'); var m = byId('errmsg');
    if (m) m.textContent = msg;
    if (box) box.hidden = false;
    byId('grid').innerHTML = '';
    byId('empty').hidden = true;
  }
  function cap(s) { return s ? s.charAt(0).toUpperCase() + s.slice(1) : s; }

  // ---- rendering ----
  function render() {
    var grid = byId('grid'); if (!grid) return;
    var lit = FloorViewModel.agents.filter(function (a) { return a.running; });
    var allowed = FILTERS[FloorViewModel.statusFilter];
    var q = FloorViewModel.query;
    // The "Stopped" segment shows ONLY dark cards: the lit grid is emptied for it (no FILTERS['stopped']
    // entry — that would wrongly run against the running grid).
    var stoppedOnly = FloorViewModel.statusFilter === 'stopped';
    var shown = stoppedOnly ? [] : lit.filter(function (a) {
      if (allowed && allowed.indexOf(a.status) === -1) return false;
      if (q && a.name.toLowerCase().indexOf(q) === -1) return false;
      return true;
    });

    grid.innerHTML = '';
    shown.forEach(function (a) { grid.appendChild(card(a)); });

    byId('lit-count').textContent = String(lit.length);
    byId('lit-label').textContent = String(lit.length);
    // #empty stays keyed on lit.length: "No agents running" remains
    // literally true even when dark cards are visible — stopped agents are, by definition, not running.
    byId('empty').hidden = lit.length !== 0;

    renderDarkGroup(q); // additive pass, AFTER the untouched lit pipeline above
  }

  // The dimmed "Dark" group (#500): stopped agents rendered as View-only cards, appended AFTER the
  // lit pipeline. Keyed on !a.running — NEVER status === "stopped", which diverges on the
  // liveness-probe-failure path. It honors the "Stopped" segment itself: visible only under the All
  // or Stopped filters.
  function renderDarkGroup(q) {
    var darkGrid = byId('dark-grid'); if (!darkGrid) return;
    var f = FloorViewModel.statusFilter;
    var show = (f === 'all' || f === 'stopped');
    var dark = show ? FloorViewModel.agents.filter(function (a) { return !a.running; }) : [];
    if (q) { dark = dark.filter(function (a) { return a.name.toLowerCase().indexOf(q) > -1; }); }

    darkGrid.innerHTML = '';
    dark.forEach(function (a) { darkGrid.appendChild(darkCard(a)); });

    var label = byId('dark-label');
    if (label) label.hidden = dark.length === 0;
    var dc = byId('dark-count'); if (dc) dc.textContent = String(dark.length);
    darkGrid.hidden = dark.length === 0;
  }

  // darkCard is the stopped-agent card variant: dimmed, name + "Stopped" badge + last-known formula,
  // and a View button ONLY — no Down/Reset menu, since a stopped agent has nothing to stop.
  // The card() builder is left byte-untouched.
  function darkCard(a) {
    var li = el('li', 'sign s-idle dark');
    li.setAttribute('data-name', a.name);
    li.setAttribute('data-status', 'stopped');

    var badges = el('div', 'badges');
    var badge = el('span', 'badge neutral');
    badge.appendChild(document.createTextNode('Stopped'));
    badges.appendChild(badge);
    li.appendChild(badges);

    li.appendChild(el('div', 'name', a.name));

    var step = el('div', 'step');
    step.appendChild(el('span', 'tt', a.formula ? ('last: ' + a.formula) : 'no recorded formula'));
    li.appendChild(step);

    var acts = el('div', 'acts');
    var view = el('button', 'btn primary', 'View');
    view.type = 'button';
    view.addEventListener('click', function () { FloorViewModel.viewAgent(a.name); });
    acts.appendChild(view);
    li.appendChild(acts);
    return li;
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function card(a) {
    var s = STATUS[a.status] || STATUS.idle;
    var li = el('li', 'sign ' + s.cls);
    li.setAttribute('data-name', a.name);
    li.setAttribute('data-status', a.status);

    // badges
    var badges = el('div', 'badges');
    var badge = el('span', s.neutral ? 'badge neutral' : 'badge');
    if (!s.neutral) badge.appendChild(el('span', 'pip'));
    badge.appendChild(document.createTextNode(s.label));
    badges.appendChild(badge);
    if (s.gate) {
      var g = el('span', 'badge gate'); g.appendChild(el('span', 'pip'));
      g.appendChild(document.createTextNode('Gate')); badges.appendChild(g);
    }
    li.appendChild(badges);

    if (s.gate) li.appendChild(el('div', 'gate-line', 'Gate · your input needed'));

    li.appendChild(el('div', 'name', a.name));

    var step = el('div', 'step');
    step.appendChild(el('span', 'nm', a.step_id || '—'));
    step.appendChild(el('span', 'tt', a.step_title || s.label));
    li.appendChild(step);

    var input = el('p', 'input');
    input.appendChild(document.createTextNode('Input: '));
    input.appendChild(el('b', null, summarizeInputs(a)));
    li.appendChild(input);

    // actions
    var acts = el('div', 'acts');
    var view = el('button', 'btn primary', 'View');
    view.type = 'button';
    view.addEventListener('click', function () { FloorViewModel.viewAgent(a.name); });
    acts.appendChild(view);

    var menu = el('details', 'menu');
    var summary = document.createElement('summary');
    summary.textContent = 'Down ▾';
    menu.appendChild(summary);
    var pop = el('div', 'menu-pop');
    var down = el('button', null, 'Down'); down.type = 'button';
    down.addEventListener('click', function () { menu.open = false; FloorViewModel.downAgent(a.name); });
    var reset = el('button', 'danger', 'Down & Reset'); reset.type = 'button';
    reset.addEventListener('click', function () { menu.open = false; FloorViewModel.resetAgent(a.name); });
    pop.appendChild(down); pop.appendChild(reset);
    menu.appendChild(pop);
    acts.appendChild(menu);

    li.appendChild(acts);
    return li;
  }

  function summarizeInputs(a) {
    if (a.inputs && Object.keys(a.inputs).length) {
      return Object.keys(a.inputs).map(function (k) { return k + '=' + a.inputs[k]; }).join(' · ');
    }
    if (a.formula) return a.formula;
    return '—';
  }

  // ---- staleness clock (fed by AssembledAt) ----
  function tickStale() {
    var el2 = byId('stale'); if (!el2) return;
    // The strip's "updated Ns ago" follows whichever view last refreshed (Floor or Dispatch).
    var src = AppViewModel.lastUpdated || FloorViewModel.lastUpdated;
    if (!src) { el2.textContent = 'just now'; return; }
    var ageMs = Date.now() - new Date(src).getTime();
    if (isNaN(ageMs)) { el2.textContent = 'just now'; return; }
    var s = Math.max(0, Math.round(ageMs / 1000));
    if (s < 2) el2.textContent = 'just now';
    else if (s < 60) el2.textContent = s + 's ago';
    else el2.textContent = Math.round(s / 60) + 'm ago';
  }

  function syncFilterButtons() {
    var seg = document.querySelectorAll('.seg button');
    seg.forEach(function (b) {
      b.setAttribute('aria-pressed', b.getAttribute('data-filter') === FloorViewModel.statusFilter ? 'true' : 'false');
    });
  }

  // Single source of truth for the active-nav highlight: set aria-current="page" on the nav
  // anchor whose data-route matches the active route, clear it from all others. The CSS rule
  // `.app .nav a[aria-current="page"]` (main.css) renders from this — JS owns the attribute.
  function syncNav(route) {
    document.querySelectorAll('.nav a').forEach(function (a) {
      if (a.getAttribute('data-route') === route) a.setAttribute('aria-current', 'page');
      else a.removeAttribute('aria-current');
    });
  }

  // ---- wire DOM → view-models ----
  function wire() {
    byId('pwr-start').addEventListener('click', function () { PowerBarViewModel.startFactory(); });
    byId('pwr-shutdown').addEventListener('click', function () { PowerBarViewModel.shutDown(); });
    byId('pwr-shutdown-reset').addEventListener('click', function () { PowerBarViewModel.shutDownReset(); });
    var es = byId('empty-start'); if (es) es.addEventListener('click', function () { PowerBarViewModel.startFactory(); });
    byId('refresh').addEventListener('click', function () { AppViewModel.refresh(); toast('Floor refreshed'); });

    byId('floor-search').addEventListener('input', function (e) { FloorViewModel.search(e.target.value); });
    var ss = byId('sling-search'); if (ss) ss.addEventListener('input', function (e) { SlingViewModel.search(e.target.value); });

    var dr = byId('dispatch-refresh'); if (dr) dr.addEventListener('click', function () { DispatchViewModel.refresh(); toast('Dispatch refreshed'); });
    var sar = byId('set-add-row'); if (sar) sar.addEventListener('click', function () { SettingsViewModel.addRow(); });
    var ssave = byId('set-save'); if (ssave) ssave.addEventListener('click', function () { SettingsViewModel.save(); });
    var pf = byId('proto-fb-form'); if (pf) pf.addEventListener('submit', function (e) { e.preventDefault(); PrototypesViewModel.send(); });
    var amf = byId('agent-mail-form'); if (amf) amf.addEventListener('submit', function (e) { e.preventDefault(); AgentDetailViewModel.send(); });
    var aback = byId('agent-back'); if (aback) aback.addEventListener('click', function () { AppViewModel.goHome(); });

    // #580: the telemetry section owns its refresh and filters. The shared chrome strip is not
    // route-aware — its #refresh is hardcoded to the Floor — so reusing it here would refresh the
    // wrong view.
    var tr = byId('tel-refresh'); if (tr) tr.addEventListener('click', function () { TelemetryViewModel.refresh().then(function () { toast('Telemetry refreshed'); }); });
    var ta = byId('tel-agent'); if (ta) ta.addEventListener('change', function () { TelemetryViewModel.setFilter(ta.value, TelemetryViewModel.instance); });
    var ti = byId('tel-instance'); if (ti) ti.addEventListener('change', function () { TelemetryViewModel.setFilter(TelemetryViewModel.agent, ti.value); });
    document.querySelectorAll('.seg button').forEach(function (b) {
      b.addEventListener('click', function () { FloorViewModel.filterByStatus(b.getAttribute('data-filter')); });
    });
    document.querySelectorAll('.nav a[data-route]').forEach(function (a) {
      a.addEventListener('click', function (e) { e.preventDefault(); AppViewModel.navigate(a.getAttribute('data-route')); });
    });
    byId('brand-home').addEventListener('click', function () { AppViewModel.goHome(); });
  }

  // ---- boot ----
  function boot() {
    wire();
    syncNav(AppViewModel.currentRoute);                            // JS owns the initial highlight (not the frozen HTML attr)
    FloorViewModel.refresh();
    setInterval(function () {
      FloorViewModel.refresh();
      // Poll the dispatch feed too while its view is active (same 5s cadence).
      if (AppViewModel.currentRoute === 'dispatch') { DispatchViewModel.refresh(); }
      // Poll the open agent-detail view too (poll-ONLY-while-open) — keyed on the
      // parameterized "agent/" route so the snapshot refreshes every 5s exactly while it is visible.
      if (AppViewModel.currentRoute.indexOf('agent/') === 0) { AgentDetailViewModel.refresh(); }
    }, 5000);
    setInterval(tickStale, 1000);                                  // honest staleness clock
    window.setTimeout(function () { document.body.classList.remove('boot'); }, 1400);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();

  // expose for verification / debugging (the logical view-model contract)
  window.setAuthToken = setAuthToken; // #502 T10: operator/console pastes the startup token here
  // window.API stays exposed for verification and for shell-level consumers; the transplanted
  // formula editor (#534) talks to the write tier through its own live-store module instead.
  window.API = API;
  window.AppViewModel = AppViewModel;
  window.PowerBarViewModel = PowerBarViewModel;
  window.ConfirmViewModel = ConfirmViewModel;
  window.FloorViewModel = FloorViewModel;
  window.SlingViewModel = SlingViewModel;
  window.DispatchViewModel = DispatchViewModel;
  window.SettingsViewModel = SettingsViewModel;
  window.PrototypesViewModel = PrototypesViewModel;
  window.AgentDetailViewModel = AgentDetailViewModel;
  window.TelemetryViewModel = TelemetryViewModel;
})();
