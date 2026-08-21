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
  //
  // `extra` carries per-request headers. It exists because the settings CAS precondition travels as a
  // REQUEST HEADER (X-AF-If-Content-Hash, server.go:574) and the settings body is contractually the
  // bare document — there is no envelope field to hide a precondition in, the way the formulas editor
  // hides one in `base_sha256`.
  function sendWrite(method, path, body, extra) {
    var h = { 'Content-Type': 'application/json' };
    var t = authToken(); if (t) h['X-AF-Token'] = t;
    if (extra) {
      for (var k in extra) { if (Object.prototype.hasOwnProperty.call(extra, k)) h[k] = extra[k]; }
    }
    return fetch(path, {
      method: method, headers: h, credentials: 'same-origin',
      body: body ? JSON.stringify(body) : '{}'
    }).then(parse);
  }
  function writeReq(method, path, body, extra) {
    return sendWrite(method, path, body, extra).then(function (env) {
      if (env && env._status === 401 && promptForToken()) {
        return sendWrite(method, path, body, extra); // retry once with the freshly-pasted token
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
    put: function (path, body, extra) { return writeReq('PUT', path, body, extra); }
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
  // ---- occupancy + recovery → look (K10-web, #596) ----
  // These ride BESIDE the status badge and never feed STATUS, data-status or FILTERS: the honesty
  // enum keeps its three Phase-0 inputs, so a breaker-halted agent still reports "working" and it
  // is this badge that stops the card reading as fine.
  //
  // Both helpers decide on the STRING field and never on context_pct. An `af` predating Phase 4A
  // omits the keys entirely, Go decodes the absent int as 0, and 0 reads as "0% used = healthy" —
  // indistinguishable from a genuinely fresh agent. The emitted domains of context_state and
  // recovery both exclude "", so empty means exactly "this af has no datum to give".
  //
  // Each falls through to a terminal return rather than a lookup-map miss: a literal we do not
  // recognise still renders. Silently dropping an unknown state is how a malformed channel would
  // come to read as healthy, which is the defect class this phase closes.

  // recoveryLook returns the badge look for a breaker verdict, or null when there is nothing to
  // say. 'none' is the NORMAL healthy value every agent carries — not an absence.
  function recoveryLook(v) {
    if (!v || v === 'none') return null;
    if (v === 'halted') return { cls: 'badge halt', label: 'Recovery halted' };
    if (v === 'recovering') return { cls: 'badge warn', label: 'Recovering' };
    return { cls: 'badge halt', label: 'Recovery ' + v };
  }

  // contextLook returns the badge look for an occupancy channel. 'fresh' is the only healthy
  // state, and a fresh reading still renders its percentage: an exhausted-but-not-yet-halted
  // agent is visible ONLY through the number, and the web module cannot see af's threshold
  // config, so it reports the datum rather than inventing a verdict about it.
  function contextLook(state, pct) {
    if (!state) return null;
    var shown = (typeof pct === 'number' && pct >= 0) ? (pct + '%') : null;
    if (state === 'fresh') {
      return shown ? { cls: 'badge neutral', label: 'Context ' + shown } : null;
    }
    var label;
    if (state === 'stale') label = 'Context stale';
    else if (state === 'dark') label = 'Context dark';
    else if (state === 'none') label = 'No context datum';
    else if (state === 'malformed') label = 'Context malformed';
    else label = 'Context ' + state;
    return { cls: 'badge warn', label: shown ? (label + ' · ' + shown) : label };
  }

  // badgeEl builds one badge through el(), never innerHTML (module-wide lint).
  function badgeEl(look) {
    var b = el('span', look.cls);
    b.appendChild(el('span', 'pip'));
    b.appendChild(document.createTextNode(look.label));
    return b;
  }

  // appendHealthBadges puts the recovery badge first — it is the more urgent of the two.
  //
  // A STOPPED agent is the one case where 'none' says nothing: af reports state 'none' for every
  // agent outside its running-only occupancy sweep, so "no context datum" on a stopped card is
  // tautological rather than a finding. Every other state still renders there — stale/dark/
  // malformed all imply a datum once existed — and the recovery badge ALWAYS renders, because a
  // latched breaker outlives the session it halted and is exactly what an operator must see.
  function appendHealthBadges(host, a) {
    var r = recoveryLook(a && a.recovery);
    if (r) host.appendChild(badgeEl(r));
    if (a && a.running === false && a.context_state === 'none') return;
    var c = contextLook(a && a.context_state, a && a.context_pct);
    if (c) host.appendChild(badgeEl(c));
  }

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
  // SettingsViewModel — #620 M1/U1. One panel per config document under
  // .agentfactory/, and one save per panel: `af config <file> set` stays the
  // single canonical validator and writer, and a save carries back EVERYTHING
  // the console read.
  //
  // THE PROPERTY THIS VIEW EXISTS FOR. The console never builds a document out
  // of form state. Every payload STARTS as the document the server sent — per
  // file AND per mapping row — and the controls overwrite only the members they
  // own. So a canonical key af grows tomorrow survives a save today, with nobody
  // editing this file. That is not a style preference. The same defect — a
  // hand-maintained enumeration of keys sitting on a whole-document replace
  // path — erased `improvement`, then `telemetry`, then `workflows` /
  // `mappings[].model` / the 14-key `recovery` block, and each of the first two
  // was "fixed" by adding one more key to the enumeration, i.e. by re-arming it.
  // Phase 2 deleted the substrate on the server; collectMappings below was the
  // same defect one level down, at row granularity.
  //
  // The reading test for any future edit here: if af grows a canonical key
  // tomorrow and nobody touches app.js, does it still survive a save?
  // =========================================================================

  // The documents the console may write, in panel order. The server's tier table is the authority on
  // WHY each one is writable; every panel renders that row's own `reason` rather than restating it.
  var SETTINGS_WRITABLE = ['dispatch', 'startup', 'messaging', 'statusline'];

  // UNMANAGED_FILES is an explicit ALLOW-list, and must stay one. The served files map carries a
  // `.agentfactory/secrets/` row (web/internal/config/tier.go:163-169), so a panel built from
  // `!writable` or from `tier === 'excluded'` would render the secrets directory — C-1 says the
  // console never reads, lists or names it. Naming the four files that ARE listed is the only shape
  // in which that cannot happen by accident.
  var UNMANAGED_FILES = ['models', 'telemetry', 'build-host', 'litellm.yaml'];

  // Who owns each unmanaged file instead. Each row's `reason` already says this in prose; these are
  // the commands, so the panel routes a secret-bearing workflow to the CLI rather than to a dead end.
  var UNMANAGED_CLI = {
    models: 'af config models set',
    telemetry: 'af telemetry on|off — which writes the .telemetry-gate file; telemetry.json itself has no CLI writer',
    'build-host': 'af config build-host',
    'litellm.yaml': 'the gateway owns it — af has no seam for it'
  };

  // The statusline element vocabulary (internal/config/statusline.go:48), listed so the panel can
  // offer the ones nobody has enabled yet. An element in the document that is NOT here still renders,
  // marked unknown — dropping it from this list is exactly how it would get dropped from the file.
  var STATUSLINE_ELEMENTS = ['model', 'dir', 'branch', 'diff', 'elapsed', 'context', 'session', 'daily'];

  // The four startup gates, each an on|off|default enum (internal/config/startup.go:173-178).
  var STARTUP_GATES = ['quality', 'fidelity', 'improvement', 'telemetry'];

  var SettingsViewModel = {
    data: null,       // the full GET /api/settings document
    agents: [],       // secret-free agent summaries (the mapping picker's options)
    profiles: [],     // model-profile NAMES only (the mapping row's model picker)
    baselines: {},    // file -> JSON text of the document AS READ; the diff's immutable other side
    rows: [],         // one descriptor per mapping row: { node, base, removed }

    activate: function () { showSettings(); return this.load(); },
    load: function () {
      var self = this;
      return API.get('/api/settings').then(function (env) {
        if (!env || !env.ok) { toast((env && env.message) || 'settings read failed'); return; }
        self.data = env.data || {};
        self.agents = self.data.agents || [];
        self.profiles = self.data.profiles || [];
        self.baselines = snapshotBaselines(self.data.files || {});
        renderSettings();
      }).catch(function (e) { toast(String(e)); });
    },
    addRow: function () {
      var host = byId('mapRows');
      if (host) { host.appendChild(settingsMapRow(null)); refreshSettingsPanel('dispatch'); }
    },
    // save writes EXACTLY ONE file. Each panel's button calls it with that panel's own noun; no caller
    // passes two, and saveSettingsFile has no second write for one to reach. See AC-5 there.
    save: function (file) { return saveSettingsFile(file); }
  };

  function settingsFiles() {
    var d = SettingsViewModel.data || {};
    return d.files || {};
  }

  // snapshotBaselines freezes every document AS READ, as TEXT, before any control can touch it. The
  // rows hold references into these documents, so a baseline kept by reference would drift along with
  // them and every diff would come out empty — which is the failure mode where the console shows "No
  // unsaved changes" over a panel full of them.
  //
  // A file the payload omits entirely is stored as the text "null", not skipped, so `undefined`
  // ("this console never read that file") stays distinguishable from `null` ("af has no document").
  function snapshotBaselines(files) {
    var out = {};
    for (var k in files) {
      if (Object.prototype.hasOwnProperty.call(files, k)) {
        out[k] = JSON.stringify(files[k].doc === undefined ? null : files[k].doc);
      }
    }
    return out;
  }

  function renderSettings() {
    var files = settingsFiles();
    renderSkewBanner();
    renderDisposition('dispatch');
    renderDisposition('startup');
    renderDisposition('messaging');
    renderDisposition('statusline');
    renderDisposition('factory');
    renderDispatchPanel(files.dispatch);
    renderStartupPanel(files.startup);
    renderRawEditor('messaging', files.messaging);
    renderStatuslinePanel(files.statusline);
    renderFactoryPanel(files.factory);
    renderUnmanagedPanel(files);
    // Success notices are per-save and do not survive a re-read. Error notices deliberately DO: they
    // stay until the operator dismisses them or starts another save of that same panel.
    SETTINGS_WRITABLE.forEach(function (file) { hide('set-' + file + '-ok'); refreshSettingsPanel(file); });
  }

  // renderDisposition prints the server's own words about a file: why the console may or may not
  // change it, and when a change lands. Nothing here restates them — a second copy of a disposition
  // is a second thing that has to be kept true.
  function renderDisposition(file) {
    var view = settingsFiles()[file] || {};
    var reason = byId('set-reason-' + file);
    if (reason) { reason.textContent = view.reason || ''; }
    var when = byId('set-when-' + file);
    if (!when) { return; }
    // doc===null means two different things: for a RAW row the file is absent from disk, for a
    // projected or excluded row the tier serves no document at all. Branch on tier, never on doc.
    // Only a panel with a save path can promise not to invent a document; on a read-only row the
    // sentence would be describing a choice the console never had.
    var absent = view.tier === 'raw' && !view.doc && view.writable;
    when.textContent = 'Takes effect: ' + (view.effective_when || 'unknown')
      + (absent ? ' · this file does not exist yet, and the console will not invent one — nothing is written until you enter a document.' : '');
  }

  function renderDispatchPanel(view) {
    view = view || {};
    var doc = view.doc || null;
    var trigger = byId('set-trigger');
    if (trigger) { trigger.value = (doc && doc.trigger_label) || ''; }

    SettingsViewModel.rows = [];
    var host = byId('mapRows');
    if (host) {
      host.innerHTML = '';
      ((doc && doc.mappings) || []).forEach(function (m) { host.appendChild(settingsMapRow(m)); });
    }
    renderRawEditor('dispatch', view);
  }

  // settingsMapRow builds one route row and — the point of the whole exercise — keeps the raw mapping
  // object it was read with as `base`. Every collect starts from a fresh copy of that, so `source`,
  // any label past the first, and every key this console has never heard of ride through. A row added
  // by "+ Add route" has a base of {} for the same reason.
  //
  // `base` is never written to. The row is the file half of this view in miniature, and the file half
  // learned the hard way (see settingsPayload) that an accumulator behind a preview that runs on every
  // keystroke will keep values the operator has already taken back off the screen.
  function settingsMapRow(mapping) {
    var raw = mapping || {};
    var row = el('div', 'map-row');
    var desc = { node: row, base: raw, removed: false };
    var labels = raw.labels || (raw.label ? [raw.label] : []);

    var input = el('input', 'field');
    input.setAttribute('aria-label', 'Labels');
    input.setAttribute('data-role', 'label');
    input.placeholder = 'label, another-label';
    input.value = labels.join(', ');
    row.appendChild(input);

    row.appendChild(el('span', 'ar', '→'));

    var sel = el('select', 'field');
    sel.setAttribute('aria-label', 'Agent');
    sel.setAttribute('data-role', 'agent');
    if (!raw.agent) {
      var o0 = el('option', null, '— choose an agent —'); o0.value = ''; o0.selected = true;
      sel.appendChild(o0);
    }
    SettingsViewModel.agents.forEach(function (a) {
      var o = el('option', null, a.name); o.value = a.name;
      if (raw.agent === a.name) o.selected = true;
      sel.appendChild(o);
    });
    // if the mapping references an agent no longer in agents.json, keep it visible (don't silently drop).
    if (raw.agent && !SettingsViewModel.agents.some(function (a) { return a.name === raw.agent; })) {
      var o2 = el('option', null, raw.agent + ' (unknown)'); o2.value = raw.agent; o2.selected = true;
      sel.appendChild(o2);
    }
    row.appendChild(sel);

    // The per-row model pin, fed by the payload's `profiles` projection — profile NAMES only, because
    // every profile BODY in models.json is a map of gateway credentials and never leaves the server.
    // Blank means the agent's own model, matching DispatchMapping.Model's own semantics.
    var msel = el('select', 'field');
    msel.setAttribute('aria-label', 'Model profile');
    msel.setAttribute('data-role', 'model');
    var none = el('option', null, 'agent default'); none.value = '';
    msel.appendChild(none);
    SettingsViewModel.profiles.forEach(function (p) {
      var o = el('option', null, p); o.value = p;
      if (raw.model === p) o.selected = true;
      msel.appendChild(o);
    });
    // Same house norm as the unknown agent above: a pin naming a profile models.json no longer lists
    // stays visible rather than being silently unpinned.
    if (raw.model && SettingsViewModel.profiles.indexOf(raw.model) < 0) {
      var o3 = el('option', null, raw.model + ' (unknown)'); o3.value = raw.model; o3.selected = true;
      msel.appendChild(o3);
    }
    row.appendChild(msel);

    var rm = el('button', 'rm', '×');
    rm.type = 'button';
    rm.setAttribute('aria-label', 'Remove route');
    rm.addEventListener('click', function () {
      desc.removed = true;
      row.remove();
      refreshSettingsPanel('dispatch');
    });
    row.appendChild(rm);

    SettingsViewModel.rows.push(desc);
    return row;
  }

  // collectMappings merges each row's controls onto a fresh copy of the RAW ROW OBJECT the document
  // was read with. Nothing here constructs a mapping, which is why `source` and every key af grows
  // after this console shipped survive: no line of this function has ever heard of them.
  //
  // It is pure, for the same reason settingsPayload is: the diff calls it on every keystroke, so a
  // version that wrote back onto the row would keep values the operator had already taken off the
  // screen — add a route, type a label, delete it, and the panel would go on PUTting a row that is
  // visible nowhere.
  //
  // `seed` is the mappings array the Advanced editor currently holds, if any. `source` has no curated
  // control, so that box is its only editor; merging onto the seed at the same position is what keeps
  // a `source` typed there from being dropped the moment a model pin is touched on that row.
  //
  // The labels/label duality is the sharp edge. af-core rejects a mapping carrying BOTH as ambiguous
  // (internal/config/dispatch.go:170-172), so writing `labels` must delete a lone `label` — and a row
  // the operator did not touch keeps whichever form it arrived in, so an untouched document is not
  // rewritten just by being looked at.
  // The fields a route ROW owns: the labels box, the agent picker and the model picker. Everything
  // else in a mapping — `source` today, whatever af adds after this console shipped — has no control,
  // so the Advanced editor is its only editor.
  function mappingRoleFields(m) {
    return JSON.stringify([m.labels || (m.label ? [m.label] : []), m.agent, m.model]);
  }

  // firstReroutedIndex answers "does position i in the Advanced text still mean the same route as row
  // i above?", and returns the first position where it does not (-1 when they all do).
  //
  // Equal lengths are not enough. Swap two routes in the text and every count still matches, but row
  // 1's labels would be merged onto route 2's `source`. Comparing only the fields the rows own keeps
  // editing `source` there a merge, and makes reordering — or retyping a label there while also
  // editing a row — the disagreement it actually is.
  function firstReroutedIndex(raw, was) {
    for (var i = 0; i < was.length; i++) {
      if (mappingRoleFields(raw[i] || {}) !== mappingRoleFields(was[i])) { return i; }
    }
    return -1;
  }

  function collectMappings(seed) {
    var out = [];
    SettingsViewModel.rows.forEach(function (desc, i) {
      if (desc.removed) { return; }
      var from = (seed && isPlainObject(seed[i])) ? seed[i] : desc.base;
      var row = cloneDoc(from);
      var labels = splitList(readRole(desc.node, 'label'));
      var agent = readRole(desc.node, 'agent');
      var model = readRole(desc.node, 'model');
      // A row added and never filled in is not an edit. Anything else — including a row the operator
      // deliberately blanked — goes to af and comes back as a 422, rather than vanishing quietly.
      if (!labels.length && agent === '' && isEmptyObject(row)) { return; }
      var was = row.labels || (row.label ? [row.label] : []);
      if (!sameJSON(labels, was)) {
        row.labels = labels;
        delete row.label;
      }
      // A route that arrived WITHOUT an agent key must not grow one just by being looked at — that is
      // the same harm as dropping a key, from the other direction. But a route the operator blanked
      // keeps its now-empty key and goes to af for the 422, rather than quietly losing the agent.
      setMember(row, 'agent', agent, agent !== '' || hasMember(row, 'agent'));
      setMember(row, 'model', model, model !== '');
      out.push(row);
    });
    return out;
  }

  function renderStartupPanel(view) {
    view = view || {};
    var doc = view.doc || null;

    var sd = byId('set-startdispatch');
    if (sd) { sd.checked = !!(doc && doc.start_dispatch); }

    // The agents sentinel is three-valued and is read back as three: absent or null means ALL, an
    // empty array means none, a non-empty array is a list (internal/config/startup.go:106).
    var agents = doc ? doc.agents : undefined;
    var mode = 'all';
    if (agents && agents.length) { mode = 'list'; }
    else if (agents) { mode = 'none'; }
    setRadio('set-startup-agents-mode', mode);
    var names = byId('set-startup-agents');
    if (names) { names.value = (agents && agents.length) ? agents.join(', ') : ''; }

    STARTUP_GATES.forEach(function (g) {
      var sel = byId('set-gate-' + g);
      if (sel) { selectWithUnknown(sel, (doc && typeof doc[g] === 'string') ? doc[g] : ''); }
    });

    renderRawEditor('startup', view);
  }

  // The names box only means anything in "Just these" mode. Left live in the other two it would
  // accept a list the save then silently discards — a small instance of exactly the harm this view
  // exists to remove, so the control says so instead of the operator finding out later.
  function syncStartupControls() {
    var names = byId('set-startup-agents');
    if (names) { names.disabled = getRadio('set-startup-agents-mode', 'all') !== 'list'; }
  }

  function renderStatuslinePanel(view) {
    view = view || {};
    var doc = view.doc || null;
    renderStatuslineElements(doc);
    // `color` is a *bool where ABSENT means ON, and af-core deliberately never fills it in
    // (internal/config/statusline.go:25-40). A plain two-state checkbox would write a value the
    // operator never typed into a document that had no such key — the same harm as dropping one they
    // did type, one file over. So it gets the same three-way control the agents sentinel gets.
    var color = doc ? doc.color : undefined;
    setRadio('set-statusline-color', color === true ? 'on' : (color === false ? 'off' : 'default'));
    renderRawEditor('statusline', view);
  }

  function renderStatuslineElements(doc) {
    var host = byId('set-statusline-elements');
    if (!host) { return; }
    host.innerHTML = '';
    var chosen = (doc && doc.elements) || [];
    var order = [], seen = {};
    // The document's own order first, so enabling a ninth element never reorders the operator's eight.
    chosen.forEach(function (n) { if (!seen[n]) { seen[n] = true; order.push(n); } });
    STATUSLINE_ELEMENTS.forEach(function (n) { if (!seen[n]) { seen[n] = true; order.push(n); } });
    var group = el('div', 'radios');
    order.forEach(function (name) {
      var lab = el('label');
      var cb = el('input');
      cb.type = 'checkbox';
      cb.value = name;
      cb.setAttribute('data-role', 'sl-element');
      cb.checked = chosen.indexOf(name) >= 0;
      lab.appendChild(cb);
      var unknown = STATUSLINE_ELEMENTS.indexOf(name) < 0 ? ' (unknown)' : '';
      lab.appendChild(document.createTextNode(' ' + name + unknown));
      group.appendChild(lab);
    });
    host.appendChild(group);
  }

  function collectStatuslineElements() {
    var out = [];
    document.querySelectorAll('#set-statusline-elements [data-role="sl-element"]').forEach(function (cb) {
      if (cb.checked) { out.push(cb.value); }
    });
    return out;
  }

  function renderFactoryPanel(view) {
    view = view || {};
    var pre = byId('set-factory');
    if (!pre) { return; }
    pre.textContent = view.doc ? JSON.stringify(view.doc, null, 2)
      : 'factory.json is not present at this factory root.';
  }

  function renderUnmanagedPanel(files) {
    var host = byId('set-unmanaged');
    if (!host) { return; }
    host.innerHTML = '';
    UNMANAGED_FILES.forEach(function (file) {
      var view = files[file];
      if (!view) { return; }
      var item = el('div', 'set-unmanaged-item');
      item.appendChild(el('p', 'set-file', file.indexOf('.') >= 0 ? file : file + '.json'));
      item.appendChild(el('p', 'muted', view.reason || ''));
      var facts = el('div', 'facts');
      facts.appendChild(settingsFactRow('Managed by', UNMANAGED_CLI[file] || 'not managed by af'));
      facts.appendChild(settingsFactRow('Takes effect', view.effective_when || 'unknown'));
      item.appendChild(facts);
      host.appendChild(item);
    });
  }

  function settingsFactRow(k, v) {
    var row = el('div', 'fact');
    row.appendChild(el('span', 'fk', k));
    row.appendChild(el('span', 'fv', v));
    return row;
  }

  // renderRawEditor fills a panel's Advanced textarea with the document as read. It is deliberately
  // BLANK for a file that does not exist: an empty box is how the console says "af never wrote this",
  // and a pretty-printed {} would be the console inventing a document — which is precisely what the
  // deleted defaultStartup() did, and why a save then materialized defaults nobody chose.
  function renderRawEditor(file, view) {
    var box = byId('set-adv-' + file);
    if (!box) { return; }
    var doc = (view && view.doc) || null;
    box.value = doc ? JSON.stringify(doc, null, 2) : '';
  }

  // ---- the settings skew banner ----
  //
  // This reports what the console can honestly know about version skew, and nothing more. The design's
  // intent is to compare `schema_fingerprint` — the running af's own config-schema digest — against
  // the fixtures this console was BUILT against. That baseline does not exist: nothing in web/ embeds
  // af-core's congruence fixtures or a hash of them (they are reached only at test time, through a
  // repo-relative walk-up, which no shipped binary can do). Rather than invent one, this renders the
  // two skew facts that ARE real today: af could not report a fingerprint at all, and the fingerprint
  // changed between two reads. Adding a build-time baseline is an ADR-008 embed-plus-drift-test job on
  // the Go side, not something the client can conjure.
  //
  // The remembered fingerprint lives in sessionStorage — per tab, gone when the tab closes. That is
  // the right lifetime for "af changed under you", and it is the only browser storage this view uses.
  var SETTINGS_SKEW_KEY = 'af-settings-schema-fingerprint';

  function renderSkewBanner() {
    var host = byId('set-banner');
    if (!host) { return; }
    host.innerHTML = '';
    var seen = (SettingsViewModel.data || {}).schema_fingerprint;
    if (seen === undefined) { return; }
    if (seen === '') {
      telLine(host,
        'This factory’s af did not report a config-schema fingerprint, so the console cannot check whether the two agree on what these documents look like.',
        'Settings still load and save — af remains the validator either way. If `af config fingerprint --json` is unavailable, upgrade af.',
        'unknown');
      return;
    }
    // The remembered value is advanced on EVERY read, so the banner reports the read at which af
    // changed and then stops. Remembering only the first read instead would leave the sentence below
    // on screen for the rest of the tab's life, still claiming a change that has long since been
    // taken in — a warning that cannot be cleared is one an operator learns to scroll past.
    var previous = settingsSkewBaseline();
    rememberSettingsSkewBaseline(seen);
    if (previous && previous !== seen) {
      telLine(host,
        'The af binary’s config schema fingerprint changed between this read and the last one — the af serving this console is not the af it was.',
        'Anything you had typed was written against the older schema. Check it against the panels above before saving.',
        'unknown');
    }
  }

  function settingsSkewBaseline() {
    try { return window.sessionStorage.getItem(SETTINGS_SKEW_KEY) || ''; } catch (e) { return ''; }
  }
  function rememberSettingsSkewBaseline(v) {
    try { window.sessionStorage.setItem(SETTINGS_SKEW_KEY, v); } catch (e) { /* no sessionStorage — the comparison simply cannot be made */ }
  }

  // ---- building the payload ----
  //
  // settingsPayload returns the EXACT object one panel will PUT. It throws when the Advanced editor
  // does not parse, and returns null when there is nothing to write at all (an absent file nobody
  // has authored).
  //
  // It is a PURE FUNCTION OF THE CURRENT CONTROL STATE, rebuilt from the document as read on every
  // call — it accumulates nothing. That is not tidiness. The diff runs this on every keystroke, and
  // an accumulating version leaves the last value it wrote behind: type "b" over "a", change your
  // mind, type "a" again, and the control now agrees with the file while the document still holds
  // "b" — a save that writes a value the operator can no longer see anywhere on screen.
  //
  // Starting from the document as read is also what carries the unknown keys: the payload BEGINS as
  // a copy of exactly what the server sent, and the controls only overwrite the members they own.
  //
  // The diff preview calls this too, deliberately. One function builds the payload, so the preview
  // cannot describe a document the save will not send — the two-sources drift that a separately
  // derived preview would reintroduce.
  function settingsPayload(file) {
    var view = settingsFiles()[file] || {};
    var box = byId('set-adv-' + file);
    var text = box ? (box.value || '').trim() : '';
    // The Advanced editor holds the WHOLE document (renderRawEditor puts it there), so when it has
    // content it is the document. An empty box is not an instruction to delete anything — it is the
    // absence of one — so the document as read stands, and for a file af never wrote that is {}.
    var doc;
    try { doc = text !== '' ? JSON.parse(text) : settingsBaseline(file); }
    catch (e) {
      throw new Error('The raw JSON in this panel is not valid JSON: ' + String((e && e.message) || e));
    }

    var base = settingsBaseline(file);
    if (file === 'dispatch') { applyDispatchControls(doc, base); }
    else if (file === 'startup') { applyStartupControls(doc, base); }
    else if (file === 'statusline') { applyStatuslineControls(doc, base); }
    // messaging has no curated control in v1 — its raw editor IS the panel.

    // null means "there is nothing here to write", which is only true of a file af has never written
    // and that the operator has not authored. An EXISTING document emptied to `{}` is an edit — and a
    // legal one — so it goes to af. Refusing it here would be the console deciding which documents af
    // is allowed to store, on top of a message ("this file does not exist") that would be false.
    if (view.doc == null && text === '' && isEmptyObject(doc)) { return null; }
    return doc;
  }

  // settingsBaseline is the document AS READ, parsed fresh on every call so no caller can hand a
  // mutated copy to the next one. It is the same snapshot the diff is computed against.
  //
  // The curated controls compare themselves to it to decide whether they have anything to say, which
  // is what makes "the operator did not touch this control" and "this control has no opinion" the
  // same thing. Without that, a control would assert its rendered value over the Advanced editor on
  // every save, and typing a key into the raw JSON box would be undone by a control nobody touched.
  function settingsBaseline(file) {
    var text = SettingsViewModel.baselines[file];
    if (text === undefined) { return {}; }
    var doc = JSON.parse(text);
    return isPlainObject(doc) ? doc : {};
  }

  // Every apply* below follows one rule: a control that still reads what it was RENDERED with says
  // nothing. Only a control the operator actually moved writes to the document.
  function applyDispatchControls(doc, base) {
    var trigger = byId('set-trigger');
    var t = trigger ? (trigger.value || '').trim() : '';
    if (t !== (base.trigger_label || '')) { setMember(doc, 'trigger_label', t, t !== ''); }

    // Two editors over one array. `source` has no curated control, so the raw box is its only editor
    // and its row-level edits must survive — but the route rows and the raw text can also disagree
    // about WHICH routes exist, and then no merge is defined. The three cases, and the one refusal:
    //
    //   only the raw box changed the list  → the rows have no opinion; the raw text stands
    //   only the rows changed              → the rows win, as the panel copy says
    //   both changed, same routes in the
    //   same order                         → merge row-wise by position (this is what carries `source`)
    //   both changed, and the text no
    //   longer lines up with the rows      → REFUSE. Seeding by position here does not merge, it
    //                                        transplants: one route's `source` lands on another
    //                                        route's labels — a legal document af accepts, so the
    //                                        damage reaches disk. Refusing is the only answer that
    //                                        does not require guessing.
    var was = base.mappings || [];
    var raw = Array.isArray(doc.mappings) ? doc.mappings : null;
    var rawChanged = raw !== null && !sameJSON(raw, was);
    var live = SettingsViewModel.rows.filter(function (d) { return !d.removed; }).length;
    var counted = rawChanged && raw.length === was.length && live === was.length;
    var off = counted ? firstReroutedIndex(raw, was) : -1;
    var aligned = counted && off < 0;

    var maps = collectMappings(aligned ? raw : null);
    if (sameJSON(maps, was)) { return; }
    // Two editors that arrived at the same array are not in disagreement, whatever route they took.
    // Deleting a route with × AND deleting it in the text is the common shape, and refusing it would
    // be the console insisting there is a conflict the operator can plainly see there is not.
    if (rawChanged && !aligned && !sameJSON(maps, raw)) {
      // Every refusal names the cheap way out. Emptying the box makes `settingsPayload` fall back to
      // the document as read, so the raw edits go and the row edits stay — without it the only exit
      // is Reload, which discards every panel's work.
      throw new Error((counted
        ? 'The raw JSON below changes a label, agent or model — the fields the route rows above own —'
          + ' so route ' + (off + 1) + ' in the text is no longer the same route as row ' + (off + 1)
          + ' above. Nothing was sent, because pairing them up would write one route\'s settings onto'
          + ' another. Change labels, agents and models in the rows above, and use the JSON for the'
          + ' keys they do not cover.'
        : 'The route rows and the raw JSON below have both changed which routes exist (' + live
          + ' in the rows, ' + raw.length + ' in the JSON, ' + was.length + ' on disk), so a route in'
          + ' the text can no longer be matched to the row it belongs to. Nothing was sent, because'
          + ' saving would have to guess. Change routes in one of the two places at a time.')
        + ' Emptying the JSON box restores the document as af wrote it and keeps your row edits.');
    }
    doc.mappings = maps;
  }

  function applyStartupControls(doc, base) {
    // The sentinel is compared as the THREE-VALUED thing it is: undefined (⇒ ALL), [] (⇒ none), or a
    // list. Rendering `null` as ALL and then writing back "absent" would edit a byte nobody asked
    // about, so an untouched control compares equal to null too and leaves it exactly as it was.
    var mode = getRadio('set-startup-agents-mode', 'all');
    var names = byId('set-startup-agents');
    var want = mode === 'none' ? [] : (mode === 'list' ? splitList(names ? names.value : '') : undefined);
    var had = (base.agents && base.agents.length) ? base.agents : (base.agents ? [] : undefined);
    // "Just these:" with nothing typed yet is an unfinished thought, not the None sentinel. Writing
    // `[]` here would silently start no agents at all under a radio that says "Just these".
    if (mode === 'list' && !want.length) { want = had; }
    if (!sameJSON(want, had)) {
      if (want === undefined) { dropUnlessExplicitNull(doc, 'agents'); }
      else { doc.agents = want; }
    }

    var sd = byId('set-startdispatch');
    var on = !!(sd && sd.checked);
    if (on !== !!base.start_dispatch) { setMember(doc, 'start_dispatch', on, on); }

    STARTUP_GATES.forEach(function (g) {
      var sel = byId('set-gate-' + g);
      var v = sel ? sel.value : '';
      if (v !== (typeof base[g] === 'string' ? base[g] : '')) { setMember(doc, g, v, v !== ''); }
    });
  }

  function applyStatuslineControls(doc, base) {
    var elems = collectStatuslineElements();
    // An operator who unchecks everything means "show nothing", which is `[]`. Removing the key
    // instead would hand the statusline back its defaults — the opposite of what they just said.
    if (!sameJSON(elems, base.elements || [])) { doc.elements = elems; }

    var mode = getRadio('set-statusline-color', 'default');
    var want = mode === 'on' ? true : (mode === 'off' ? false : undefined);
    var had = base.color === true ? true : (base.color === false ? false : undefined);
    if (!sameJSON(want, had)) {
      if (want === undefined) { dropUnlessExplicitNull(doc, 'color'); }
      else { doc.color = want; }
    }
  }

  // ---- the pre-save diff ----
  //
  // settingsDiff names the keys a save would change, computed from the SAME object settingsPayload
  // hands to PUT and from the snapshot taken before any control could touch it.
  //
  // It returns either an ARRAY of key names or a STRING saying why no save is possible right now,
  // because those are the only two things the panel ever has to report — and the string is the
  // payload builder's own sentence, so the panel cannot describe the problem differently from the
  // save that refused for it.
  function settingsDiff(file) {
    var payload;
    try { payload = settingsPayload(file); }
    catch (e) { return String((e && e.message) || e); }
    var text = SettingsViewModel.baselines[file];
    return diffKeys(text === undefined ? null : JSON.parse(text), payload);
  }

  // diffKeys names changes at the granularity an operator reasons at: a top-level key, or a member of
  // one array element ("mappings[2].model").
  function diffKeys(a, b) {
    var out = [], seen = {}, k;
    a = a || {};
    b = b || {};
    for (k in a) {
      if (!Object.prototype.hasOwnProperty.call(a, k)) { continue; }
      seen[k] = true;
      if (!Object.prototype.hasOwnProperty.call(b, k)) { out.push(k + ' (removed)'); }
      else if (JSON.stringify(a[k]) !== JSON.stringify(b[k])) { out = out.concat(diffElements(k, a[k], b[k])); }
    }
    for (k in b) {
      if (Object.prototype.hasOwnProperty.call(b, k) && !seen[k]) { out.push(k + ' (added)'); }
    }
    return out;
  }

  function diffElements(key, a, b) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) { return [key]; }
    var out = [];
    for (var i = 0; i < a.length; i++) {
      if (JSON.stringify(a[i]) === JSON.stringify(b[i])) { continue; }
      if (!isPlainObject(a[i]) || !isPlainObject(b[i])) { out.push(key + '[' + i + ']'); continue; }
      var sub = diffKeys(a[i], b[i]);
      if (!sub.length) { out.push(key + '[' + i + ']'); continue; }
      out = out.concat(sub.map(function (s) { return key + '[' + i + '].' + s; }));
    }
    return out.length ? out : [key];
  }

  // refreshSettingsPanel recomputes one panel's diff and, from it, whether there is anything to save.
  // Dirty-state and the preview are the same computation, so a panel can never offer to save nothing,
  // nor refuse to save something.
  function refreshSettingsPanel(file) {
    var view = settingsFiles()[file] || {};
    if (file === 'startup') { syncStartupControls(); }
    disarmSettingsReload();
    var diff = settingsDiff(file);
    var blocked = typeof diff === 'string';
    var host = byId('set-diff-' + file);
    if (host) {
      host.innerHTML = '';
      if (blocked) {
        host.appendChild(el('p', 'tel-unknown', diff));
      } else if (!diff.length) {
        host.appendChild(el('p', 'tel-empty', 'No unsaved changes.'));
      } else {
        host.appendChild(el('p', 'set-diff-keys', 'Will change: ' + diff.join(', ')));
      }
    }
    var btn = byId('set-save-' + file);
    if (btn) { btn.disabled = !view.writable || blocked || !diff.length; }
  }

  // ---- the save ----
  //
  // saveSettingsFile writes EXACTLY ONE config file. That is the whole of AC-5. The chained save this
  // replaced PUT dispatch and then, in an unconditional .then, PUT startup — so a rejected first write
  // did not stop the second. The fix is not a guard on the second write; it is that this function has
  // no second write for a guard to protect, so no later edit can invert a branch and bring the defect
  // back.
  //
  // The re-read lives inside the ok arm, so af's normalization (a lone label coming back as labels;
  // recovery defaults materialized on first save) is reflected only when something was actually
  // written. writeReq has already resolved its one 401 retry by the time this sees `env`, so a token
  // prompt cannot read as a failed save.
  function saveSettingsFile(file) {
    var self = SettingsViewModel;
    if (!self.data) { return Promise.resolve(); }
    hide('set-' + file + '-ok');
    dismissSettingsError(file);

    var payload;
    // settingsPayload's own sentence is shown verbatim: it is the same string the panel's preview is
    // already displaying, so the refusal cannot be described one way above the button and another below.
    try { payload = settingsPayload(file); }
    catch (e) {
      showSettingsError(file, String((e && e.message) || e) + ' Nothing was sent.');
      return Promise.resolve();
    }
    if (payload === null) {
      showSettingsError(file, 'Nothing to save: this file does not exist and no document has been entered for it.');
      return Promise.resolve();
    }

    var btn = byId('set-save-' + file);
    var label = btn ? btn.textContent : '';
    if (btn) { btn.disabled = true; btn.textContent = 'Saving…'; }

    // The precondition is the fingerprint THIS panel read. A file with no document on disk carries no
    // fingerprint, and an unparseable precondition is a 400 — so send no header at all rather than an
    // empty or invented one.
    var extra = {};
    var fp = (settingsFiles()[file] || {}).fingerprint;
    if (fp) { extra['X-AF-If-Content-Hash'] = fp; }

    return API.put('/api/settings/' + file, payload, extra).then(function (env) {
      if (env && env.ok) {
        return self.load().then(function () { show('set-' + file + '-ok'); });
      }
      showSettingsError(file, settingsFailureCopy(file, env));
      return null;
    }).catch(function (e) {
      showSettingsError(file, String(e));
      return null;
    }).then(function () {
      if (btn) { btn.disabled = false; btn.textContent = label; }
      refreshSettingsPanel(file);
    });
  }

  // settingsFailureCopy turns one refused write into a sentence the operator can act on. The arms are
  // distinguishable ONLY through _status: 400, 409, 422 and 502 all arrive as {ok:false, message}.
  // 409 is the odd one out — it is not a rejection of the document at all. Nothing was written, and
  // the edit is still valid; it was just based on a read that has since gone stale.
  function settingsFailureCopy(file, env) {
    var msg = (env && env.message) || (file + '.json could not be saved');
    var status = env ? env._status : 0;
    if (status === 409) {
      return msg + ' — nothing was written. Reload the settings, re-apply this edit, and save again.';
    }
    if (status === 502) { return 'af could not run, so nothing was written: ' + msg; }
    return msg;
  }

  // A failed save stays on screen until the operator dismisses it or starts another save of the same
  // panel. A notice that clears itself is a notice the operator can miss.
  function showSettingsError(file, msg) {
    showValidation('set-' + file + '-err', msg);
    show('set-dismiss-' + file);
  }
  function dismissSettingsError(file) {
    hide('set-' + file + '-err');
    hide('set-dismiss-' + file);
  }

  // reloadSettings re-reads every panel, which throws away whatever is unsaved in all four of them. It
  // names them and asks again rather than doing it on the first click: the panels already compute a
  // live dirty state, so the console knows exactly what it is about to discard and can say so.
  function reloadSettings() {
    var btn = byId('set-reload');
    // A refusal counts as dirty: settingsDiff answers with a reason STRING there, and a panel the
    // operator cannot save yet is exactly the one whose work a reload would throw away.
    var dirty = SETTINGS_WRITABLE.filter(function (f) {
      return settingsDiff(f).length > 0;
    });
    if (dirty.length && btn && btn.getAttribute('data-armed') !== '1') {
      btn.setAttribute('data-armed', '1');
      btn.textContent = 'Discard unsaved edits in ' + dirty.join(', ') + '? Click again';
      return Promise.resolve();
    }
    disarmSettingsReload();
    return SettingsViewModel.load();
  }
  function disarmSettingsReload() {
    var btn = byId('set-reload');
    if (btn && btn.getAttribute('data-armed') === '1') {
      btn.removeAttribute('data-armed');
      btn.textContent = 'Reload settings';
    }
  }

  // wireSettingsPanels binds each panel's own Save and Dismiss and recomputes that panel's diff on
  // every edit. One button, one noun: there is no wiring here through which two files could be asked
  // for at once.
  function wireSettingsPanels() {
    SETTINGS_WRITABLE.forEach(function (file) {
      var btn = byId('set-save-' + file);
      if (btn) { btn.addEventListener('click', function () { SettingsViewModel.save(file); }); }
      var dis = byId('set-dismiss-' + file);
      if (dis) { dis.addEventListener('click', function () { dismissSettingsError(file); }); }
      var panel = byId('set-panel-' + file);
      if (panel) {
        panel.addEventListener('input', function () { refreshSettingsPanel(file); });
        panel.addEventListener('change', function () { refreshSettingsPanel(file); });
      }
    });
  }

  // ---- small settings helpers ----

  function readRole(node, role) {
    var e = node.querySelector('[data-role="' + role + '"]');
    return e ? (e.value || '').trim() : '';
  }
  function splitList(s) {
    return String(s || '').split(',').map(function (p) { return p.trim(); }).filter(Boolean);
  }
  function hasMember(o, key) {
    return Object.prototype.hasOwnProperty.call(o, key);
  }
  function isEmptyObject(o) {
    for (var k in o) { if (Object.prototype.hasOwnProperty.call(o, k)) { return false; } }
    return true;
  }
  function isPlainObject(v) {
    return v !== null && typeof v === 'object' && !Array.isArray(v);
  }
  // sameJSON compares two values the way the diff does, so "this control has nothing to say" and
  // "this key is absent from the diff" can never disagree. undefined vs [] stays a difference, which
  // is the whole point for the two three-valued sentinels.
  function sameJSON(a, b) {
    return JSON.stringify(a) === JSON.stringify(b);
  }
  // cloneDoc copies a document (or one row of one) through JSON, the same encoding it arrived in and
  // will leave in. Every key survives, including the ones this console has never heard of — which is
  // the property, and is why the copy is structural rather than a list of members.
  function cloneDoc(o) {
    return JSON.parse(JSON.stringify(o));
  }
  // setMember writes a member the panel owns, or REMOVES it when the control says "not set". Writing a
  // key the operator never typed is the same class of harm as dropping one they did.
  function setMember(doc, key, value, present) {
    if (present) { doc[key] = value; }
    else { dropMember(doc, key); }
  }
  function dropMember(doc, key) {
    if (hasMember(doc, key)) { delete doc[key]; }
  }
  // dropUnlessExplicitNull backs off from a document that already says null. To af, null and absent
  // mean the same thing for both three-valued sentinels, so rewriting one as the other would be the
  // console editing a byte nobody asked it to.
  function dropUnlessExplicitNull(doc, key) {
    if (hasMember(doc, key) && doc[key] === null) { return; }
    dropMember(doc, key);
  }
  function setRadio(name, value) {
    document.querySelectorAll('input[name="' + name + '"]').forEach(function (o) { o.checked = (o.value === value); });
  }
  function getRadio(name, fallback) {
    var out = fallback;
    document.querySelectorAll('input[name="' + name + '"]').forEach(function (o) { if (o.checked) { out = o.value; } });
    return out;
  }
  // selectWithUnknown selects `cur`, keeping a value the enum does not list VISIBLE rather than
  // letting the browser silently select nothing and the save then drop it.
  function selectWithUnknown(sel, cur) {
    sel.querySelectorAll('option[data-unknown]').forEach(function (o) { o.remove(); });
    if (cur !== '' && !settingsHasOption(sel, cur)) {
      var o = el('option', null, cur + ' (unknown)');
      o.value = cur;
      o.setAttribute('data-unknown', '1');
      sel.appendChild(o);
    }
    sel.value = cur;
  }
  function settingsHasOption(sel, value) {
    var opts = sel.querySelectorAll('option');
    for (var i = 0; i < opts.length; i++) { if (opts[i].value === value) { return true; } }
    return false;
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
      appendHealthBadges(badgeHost, agent);
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
      // INTERRUPTED is an OPEN row that the recovery funnel explained, not a closed one: the join
      // says why the step stopped being worked on, it never records an end. So it carries
      // elapsed-so-far exactly as 'open' does, and dropping the qualifier here would present a
      // figure nothing measured as a recorded duration.
      tr.appendChild(el('td', '', r.status === 'open' || r.status === 'INTERRUPTED'
        ? formatMs(r.duration_ms) + ' so far'
        : formatMs(r.duration_ms)));
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
  function darkCard(a) {
    var li = el('li', 'sign s-idle dark');
    li.setAttribute('data-name', a.name);
    li.setAttribute('data-status', 'stopped');

    var badges = el('div', 'badges');
    var badge = el('span', 'badge neutral');
    badge.appendChild(document.createTextNode('Stopped'));
    badges.appendChild(badge);
    // A latched breaker outlives the session it halted, so a stopped card is exactly where an
    // operator needs to see that this agent will not be recycled until `af recovery reset`.
    appendHealthBadges(badges, a);
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
    appendHealthBadges(badges, a);
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
    var srl = byId('set-reload'); if (srl) srl.addEventListener('click', function () { reloadSettings(); });
    // Each settings panel owns its own Save. There is deliberately no factory-wide settings save to
    // bind here: one button that wrote four files is what made a partial write possible (AC-5).
    wireSettingsPanels();
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
