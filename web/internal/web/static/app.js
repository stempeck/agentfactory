/* ============================================================================
   app.js — Direction A · "Neon Bazaar" Floor view (Phase 1, vanilla JS).
   Implements the four Phase-1 view-models from design-contract.yaml:
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

  // Optional session token (present only when the server templated it for a
  // non-loopback bind; on pure loopback it is absent and not required).
  function authToken() {
    var m = document.querySelector('meta[name="af-token"]');
    return m ? m.getAttribute('content') : '';
  }

  // ---- API layer ----
  var API = {
    get: function (path) {
      var h = {};
      var t = authToken(); if (t) h['X-AF-Token'] = t;
      return fetch(path, { headers: h, credentials: 'same-origin' }).then(parse);
    },
    post: function (path, body) {
      var h = { 'Content-Type': 'application/json' };
      var t = authToken(); if (t) h['X-AF-Token'] = t;
      return fetch(path, {
        method: 'POST', headers: h, credentials: 'same-origin',
        body: body ? JSON.stringify(body) : '{}'
      }).then(parse);
    },
    // put sends the COMPLETE edited config document as the request body. The settings write path
    // (PUT /api/settings/{file}) feeds that body straight to `af config <file> set` on stdin.
    put: function (path, body) {
      var h = { 'Content-Type': 'application/json' };
      var t = authToken(); if (t) h['X-AF-Token'] = t;
      return fetch(path, {
        method: 'PUT', headers: h, credentials: 'same-origin',
        body: body ? JSON.stringify(body) : '{}'
      }).then(parse);
    }
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
    viewAgent: function (name) { toast('Agent detail for ' + name + ' arrives in a later phase'); },
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
  // (INV-2) and rejects unknown keys. Sling is fire-and-forget: success copy
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

      // Validate EVERY required field (not just the task) before dispatch (K7). required_unless
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

      // --reset blast-radius guard (K6): always-`--reset` would discard a live formula step. If the
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
  var VIEW_IDS = ['view-floor', 'view-sling', 'view-dispatch', 'view-settings', 'view-prototypes'];
  function showView(id) {
    VIEW_IDS.forEach(function (v) { var s = byId(v); if (s) s.hidden = (v !== id); });
  }
  function showFloor() { showView('view-floor'); }
  function showSling() { showView('view-sling'); }
  function showDispatch() { showView('view-dispatch'); }
  function showSettings() { showView('view-settings'); }
  function showPrototypes() { showView('view-prototypes'); }

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
    // Server-authoritative: which field the positional task effectively binds to (K3). Blank == the
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
    // L2 — drive the label from the primary field so distinct agents visibly differ.
    lbl.appendChild(document.createTextNode(synthetic ? 'Task / issue reference' : (f.description || f.name)));
    lbl.appendChild(document.createTextNode(' '));
    lbl.appendChild(el('span', 'req', 'required'));
    wrap.appendChild(lbl);
    var ta = el('textarea', 'field w');
    ta.id = 'sling-field-' + key;
    ta.setAttribute('data-key', key);
    ta.setAttribute('aria-describedby', 'sling-field-err-' + key);
    // L2 — name the bound field in the placeholder so the box is distinct per agent.
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
    // Required (non-primary) fields get the same per-field error machinery as the task box (K7) —
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
  // Persistent inline failure surface (K7): a sling failure renders env.message here, not as an
  // ephemeral toast. Reuses the .validation danger class (#sling-error in index.html).
  function showSlingError(msg) {
    var box = byId('sling-error'); if (!box) return;
    box.textContent = msg || 'sling failed';
    box.hidden = false;
    box.scrollIntoView({ block: 'nearest' });
  }
  function clearSlingError() {
    var box = byId('sling-error'); if (box) { box.hidden = true; box.textContent = ''; }
  }
  // A live (non-terminal) formula step means re-slinging (always `--reset`) would discard work (K6).
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
  // AppViewModel — shell / nav / staleness.
  // =========================================================================
  var AppViewModel = {
    currentRoute: 'floor',
    lastUpdated: '',
    navigate: function (route) {
      this.currentRoute = route;
      syncNav(route);                                               // move the highlight first, for every route
      if (route === 'sling') { SlingViewModel.activate(); return; }
      if (route === 'dispatch') { DispatchViewModel.activate(); return; }
      if (route === 'settings') { SettingsViewModel.activate(); return; }
      if (route === 'prototypes') { PrototypesViewModel.activate(); return; }
      if (route === 'floor') { showFloor(); return; }
      toast(cap(route) + ' view arrives in a later phase');
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
    var shown = lit.filter(function (a) {
      if (allowed && allowed.indexOf(a.status) === -1) return false;
      if (q && a.name.toLowerCase().indexOf(q) === -1) return false;
      return true;
    });

    grid.innerHTML = '';
    shown.forEach(function (a) { grid.appendChild(card(a)); });

    byId('lit-count').textContent = String(lit.length);
    byId('lit-label').textContent = String(lit.length);
    byId('empty').hidden = lit.length !== 0;
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
      // Poll the dispatch feed too while its view is active (same 5s cadence; scale.md Decision 1).
      if (AppViewModel.currentRoute === 'dispatch') { DispatchViewModel.refresh(); }
    }, 5000);
    setInterval(tickStale, 1000);                                  // honest staleness clock
    window.setTimeout(function () { document.body.classList.remove('boot'); }, 1400);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();

  // expose for verification / debugging (the logical view-model contract)
  window.AppViewModel = AppViewModel;
  window.PowerBarViewModel = PowerBarViewModel;
  window.ConfirmViewModel = ConfirmViewModel;
  window.FloorViewModel = FloorViewModel;
  window.SlingViewModel = SlingViewModel;
  window.DispatchViewModel = DispatchViewModel;
  window.SettingsViewModel = SettingsViewModel;
  window.PrototypesViewModel = PrototypesViewModel;
})();
