/* @ds-bundle: {"format":4,"namespace":"Vink","components":[{"name":"StateBadge"},{"name":"Button"},{"name":"Chip"},{"name":"Tag"},{"name":"KindIcon"},{"name":"Field"},{"name":"PingUrl"},{"name":"Sparkline"},{"name":"MonitorRow"},{"name":"UptimeBar"},{"name":"StatusBanner"},{"name":"EmptyState"},{"name":"TopBar"},{"name":"Tabs"},{"name":"FieldRow"},{"name":"Checkbox"},{"name":"Switch"},{"name":"Segmented"},{"name":"KindPicker"},{"name":"Disclosure"},{"name":"Notice"},{"name":"Code"},{"name":"Panel"},{"name":"IncidentRow"},{"name":"SettingsRow"}]} */
/* vink components as canonical HTML. Each function returns the markup a Go html/template
   partial must produce; no framework. Helpers wire the two behaviours the UI has beyond htmx:
   copy-to-clipboard and the two-step destructive confirm. */
(function () {
  var esc = function (s) { return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); };
  var STATES = ['up', 'late', 'down', 'paused', 'new'];
  var st = function (s) { return STATES.indexOf(s) >= 0 ? s : 'new'; };
  var glyph = function (s, extra) { return '<i class="vk-glyph vk-glyph--' + st(s) + (extra ? ' ' + extra : '') + '" aria-hidden="true"></i>'; };

  var MARK = '<g stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none"><path d="M8 3.2 H6 a2.2 2.2 0 0 0 -2.2 2.2 V18.6 a2.2 2.2 0 0 0 2.2 2.2 H8"/><path d="M16 3.2 H18 a2.2 2.2 0 0 1 2.2 2.2 V18.6 a2.2 2.2 0 0 1 -2.2 2.2 H16"/></g>' +
    '<path d="M7.5 10.1 L10.5 12.9 L16.5 7.1" stroke="currentColor" stroke-width="2.9" stroke-linecap="round" stroke-linejoin="round" fill="none"/><rect x="9.6" y="16.2" width="4.8" height="2.2" rx="1.1" fill="currentColor"/>';

  var KINDS = {
    heartbeat: '<path d="M1.5 9 H4.4 L6.4 4 L9.4 12.5 L11.1 8 H14.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" fill="none"/>',
    http: '<g stroke="currentColor" stroke-width="1.5" fill="none"><circle cx="8" cy="8" r="5.75"/><ellipse cx="8" cy="8" rx="2.4" ry="5.75"/><path d="M2.4 8 H13.6" stroke-linecap="round"/></g>',
    tcp: '<g stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round"><circle cx="3.6" cy="8" r="1.9"/><circle cx="12.4" cy="8" r="1.9"/><path d="M5.6 8 H10.4"/></g>',
    dns: '<g stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"><path d="M5 2.2 V13.8"/><path d="M5 3.6 H11.2 L13.2 5.5 L11.2 7.4 H5"/></g>',
    tls: '<g stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"><rect x="3.4" y="7.2" width="9.2" height="6.6" rx="1.6"/><path d="M5.6 7.2 V5.2 a2.4 2.4 0 0 1 4.8 0 V7.2"/></g>',
    icmp: '<g stroke="currentColor" stroke-width="1.5" fill="none" stroke-linecap="round"><path d="M3.2 8.6 a4.2 4.2 0 0 1 4.2 4.2"/><path d="M3.2 4.4 a8.4 8.4 0 0 1 8.4 8.4"/></g><circle cx="3.6" cy="12.4" r="1.4" fill="currentColor"/>'
  };

  function Mark(p) { p = p || {}; var s = p.size || 24; return '<svg viewBox="0 0 24 24" width="' + s + '" height="' + s + '" aria-hidden="true">' + MARK + '</svg>'; }

  function StateBadge(p) {
    p = p || {}; var s = st(p.state);
    return '<span class="vk-state vk-state--' + s + (p.pill ? ' vk-state--pill' : '') + '">' + glyph(s) + esc(p.label || s) +
      (p.since ? ' <span class="vk-state__since">' + esc(p.since) + '</span>' : '') + '</span>';
  }

  function Button(p) {
    p = p || {}; var v = p.variant || 'quiet';
    var cls = 'vk-btn' + (v !== 'quiet' ? ' vk-btn--' + esc(v) : '') + (p.block ? ' vk-btn--block' : '');
    if (p.href) return '<a class="' + cls + '" href="' + esc(p.href) + '">' + esc(p.label) + '</a>';
    return '<button type="' + (p.type === 'submit' ? 'submit' : 'button') + '" class="' + cls + '"' +
      (p.confirm ? ' data-confirm="' + esc(p.confirm) + '"' : '') + (p.disabled ? ' disabled' : '') + '>' + esc(p.label) + '</button>';
  }

  function Chip(p) {
    p = p || {};
    return '<button type="button" class="vk-chip" aria-pressed="' + (p.pressed ? 'true' : 'false') + '">' +
      (p.state ? glyph(p.state) : '') + esc(p.label) + (p.count != null ? '<span class="vk-chip__n">' + esc(p.count) + '</span>' : '') + '</button>';
  }

  function Tag(p) { return '<span class="vk-tag">' + esc((p || {}).label) + '</span>'; }

  function KindIcon(p) {
    var k = (p || {}).kind; if (!KINDS[k]) k = 'http';
    return '<span class="vk-kind" title="' + k + '"><svg viewBox="0 0 16 16" role="img" aria-label="' + k + '">' + KINDS[k] + '</svg></span>';
  }

  function opts(list, value) {
    return (list || []).map(function (o) {
      var v = typeof o === 'object' ? o.value : o, l = typeof o === 'object' ? o.label : o;
      return '<option value="' + esc(v) + '"' + (String(v) === String(value) ? ' selected' : '') + '>' + esc(l) + '</option>';
    }).join('');
  }

  /* control: 'input' (default) | 'select' | 'textarea'; `html` replaces the control (a Segmented, a code field);
     `before`/`after` sit beside it on one line. All three are trusted HTML. */
  function Field(p) {
    p = p || {}; var id = esc(p.id || 'f'), hid = p.id ? id + '-msg' : '', ctl = p.control || 'input';
    var attrs = ' id="' + id + '" name="' + esc(p.name || p.id || 'f') + '"' + ((p.error || p.hint) && hid ? ' aria-describedby="' + hid + '"' : '') +
      (p.error ? ' aria-invalid="true"' : '') + (p.disabled ? ' disabled' : '');
    var cls = 'vk-input' + (p.mono ? ' vk-input--mono' : '');
    var el;
    if (ctl === 'select') el = '<span class="vk-select"><select class="' + cls + '"' + attrs + '>' + opts(p.options, p.value) + '</select></span>';
    else if (ctl === 'textarea') el = '<textarea class="' + cls + ' vk-input--area"' + attrs + ' rows="' + (p.rows || 3) + '" placeholder="' + esc(p.placeholder) + '">' + esc(p.value) + '</textarea>';
    else {
      el = '<input class="' + cls + '"' + attrs + ' type="' + esc(p.type || 'text') + '" value="' + esc(p.value) + '" placeholder="' + esc(p.placeholder) + '"' + (p.autocomplete ? ' autocomplete="' + esc(p.autocomplete) + '"' : '') + '>';
      if (p.prefix || p.suffix) el = '<span class="vk-affix">' + (p.prefix ? '<span class="vk-affix__text">' + esc(p.prefix) + '</span>' : '') + el +
        (p.suffix ? '<span class="vk-affix__text">' + esc(p.suffix) + '</span>' : '') + '</span>';
    }
    if (p.html) el = p.html;
    if (p.before || p.after) el = '<div class="vk-field__row">' + (p.before || '') + el + (p.after || '') + '</div>';
    return '<div class="vk-field' + (p.error ? ' vk-field--error' : '') + '">' +
      (p.html ? '<span class="vk-field__label">' + esc(p.label) + '</span>' : '<label class="vk-field__label" for="' + id + '">' + esc(p.label) + '</label>') + el +
      (p.error ? '<span class="vk-field__error"' + (hid ? ' id="' + hid + '"' : '') + '>' + glyph('down') + esc(p.error) + '</span>'
        : (p.hint ? '<span class="vk-field__hint"' + (hid ? ' id="' + hid + '"' : '') + '>' + esc(p.hint) + '</span>' : '')) + '</div>';
  }

  /* two to four fields side by side; `lead` makes the first column narrow (method + URL) */
  function FieldRow(p) {
    p = p || {}; var f = p.fields || [];
    return '<div class="vk-fieldrow' + (p.lead ? ' vk-fieldrow--lead' : f.length > 2 ? ' vk-fieldrow--' + f.length : '') + '">' + f.join('') + '</div>';
  }

  function Checkbox(p) {
    p = p || {};
    return '<label class="vk-check"><input type="checkbox" name="' + esc(p.name || p.id || 'c') + '"' + (p.value != null ? ' value="' + esc(p.value) + '"' : '') + (p.checked ? ' checked' : '') + (p.disabled ? ' disabled' : '') + '>' +
      '<span class="vk-check__text">' + (p.labelHtml || esc(p.label)) + (p.hint ? '<span class="vk-check__hint">' + esc(p.hint) + '</span>' : '') + '</span></label>';
  }

  function Switch(p) {
    p = p || {}; var on = !!p.checked;
    return '<button type="button" class="vk-switch" role="switch" aria-checked="' + on + '" aria-label="' + esc(p.label || 'Enabled') + '">' +
      '<span class="vk-switch__track" aria-hidden="true"></span><span class="vk-switch__word">' + (on ? 'on' : 'off') + '</span></button>';
  }

  /* one choice (radio) or several (multi: checkboxes) from two to seven short options */
  function Segmented(p) {
    p = p || {}; var type = p.multi ? 'checkbox' : 'radio', vals = [].concat(p.value == null ? [] : p.value).map(String);
    return '<div class="vk-seg' + (p.mono ? ' vk-seg--mono' : '') + '" role="' + (p.multi ? 'group' : 'radiogroup') + '" aria-label="' + esc(p.label) + '">' +
      (p.options || []).map(function (o) {
        var v = typeof o === 'object' ? o.value : o, l = typeof o === 'object' ? o.label : o;
        return '<label class="vk-seg__opt"><input type="' + type + '" name="' + esc(p.name || 'seg') + '" value="' + esc(v) + '"' + (vals.indexOf(String(v)) >= 0 ? ' checked' : '') + '><span>' + esc(l) + '</span></label>';
      }).join('') + '</div>';
  }

  var KIND_INFO = [
    ['heartbeat', 'Heartbeat', 'jobs ping vink'], ['http', 'HTTP', 'requests a URL'], ['tcp', 'TCP', 'opens a port'],
    ['dns', 'DNS', 'resolves a name'], ['tls', 'TLS certificate', 'checks expiry'], ['icmp', 'ICMP ping', 'pings a host']];

  /* the create form's first field; on edit the kind is fixed (locked) because changing it recreates the monitor */
  function KindPicker(p) {
    p = p || {}; var cur = p.value || 'heartbeat';
    return '<fieldset class="vk-kinds"><legend class="vk-field__label">Kind</legend><div class="vk-kinds__grid">' +
      KIND_INFO.map(function (k) {
        return '<label class="vk-kindopt"><input type="radio" name="kind" value="' + k[0] + '"' + (k[0] === cur ? ' checked' : '') + (p.locked && k[0] !== cur ? ' disabled' : '') + '>' +
          '<span class="vk-kindopt__card">' + KindIcon({ kind: k[0] }) + '<span class="vk-kindopt__name">' + k[1] + '</span><span class="vk-kindopt__desc">' + k[2] + '</span></span></label>';
      }).join('') + '</div></fieldset>';
  }

  /* `body` is trusted HTML built from other components */
  function Disclosure(p) {
    p = p || {};
    return '<details class="vk-details"' + (p.open ? ' open' : '') + '><summary><span class="vk-details__title">' + esc(p.title || 'Advanced') + '</span>' +
      (p.summary ? '<span class="vk-details__sum">' + esc(p.summary) + '</span>' : '') + '</summary><div class="vk-details__body">' + (p.body || '') + '</div></details>';
  }

  var TONE_GLYPH = { ok: 'up', error: 'down', warn: 'late' };
  /* inline result next to what caused it: a channel test, a new API key, a failed sign-in. Never a toast. `html` is trusted extra content. */
  function Notice(p) {
    p = p || {}; var t = p.tone || 'info';
    return '<div class="vk-notice vk-notice--' + esc(t) + '" role="' + (t === 'error' ? 'alert' : 'status') + '">' + (TONE_GLYPH[t] ? glyph(TONE_GLYPH[t]) : '') +
      '<div class="vk-notice__text"><p>' + (p.title ? '<b class="vk-notice__title">' + esc(p.title) + '</b> ' : '') + esc(p.text || '') + '</p>' + (p.html || '') + '</div></div>';
  }

  /* read-only code with an optional Copy; yaml: true dims the keys */
  function Code(p) {
    p = p || {}; var body = esc(p.text || '');
    if (p.yaml) body = body.split('\n').map(function (l) { return l.replace(/^(\s*(?:- )?)([\w.-]+:)/, '$1<i>$2</i>'); }).join('\n');
    return '<div class="vk-codebox"><pre class="vk-code">' + body + '</pre>' + (p.copy ? '<button type="button" class="vk-btn vk-copy" data-copy="' + esc(p.text) + '">' + esc(p.copyLabel || 'Copy') + '</button>' : '') + '</div>';
  }

  /* an inline form or group on surface: settings add/edit, the login card uses the auth layout instead. `body`/`actions` are trusted HTML */
  function Panel(p) {
    p = p || {};
    return '<section class="vk-panel"' + (p.id ? ' id="' + esc(p.id) + '"' : '') + '>' + (p.title ? '<div class="vk-panel__head"><h2>' + esc(p.title) + '</h2>' + (p.note ? '<p>' + esc(p.note) + '</p>' : '') + '</div>' : '') +
      (p.body || '') + (p.actions ? '<div class="vk-actions">' + p.actions + '</div>' : '') + '</section>';
  }

  function PingUrl(p) {
    p = p || {}; var base = esc(p.base || 'https://vink.example.com/ping/'), key = esc(p.key || 'k7f3q9x2mz'), slug = esc(p.slug || 'nightly-backup');
    var url = base + key + '/' + slug;
    return '<div class="vk-ping"><code class="vk-ping__url">' + base + key + '/<b>' + slug + '</b></code>' +
      '<button type="button" class="vk-btn vk-copy" data-copy="' + url + '">Copy</button></div>';
  }

  function Sparkline(p) {
    p = p || {}; var pts = p.points || [], w = 96, h = 20, n = pts.length;
    if (!n) return '<svg class="vk-spark" viewBox="0 0 96 20" aria-hidden="true"></svg>';
    var max = Math.max.apply(null, pts), min = Math.min.apply(null, pts), span = (max - min) || 1;
    var xy = pts.map(function (v, i) { return (n === 1 ? w : (i * w / (n - 1))).toFixed(1) + ',' + (h - 2 - (v - min) / span * (h - 4)).toFixed(1); });
    return '<svg class="vk-spark' + (p.state && p.state !== 'up' ? ' vk-spark--' + st(p.state) : '') + '" viewBox="0 0 96 20" preserveAspectRatio="none" aria-hidden="true">' +
      '<polyline points="' + xy.join(' ') + '" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke"/>' +
      '<circle cx="' + xy[n - 1].split(',')[0] + '" cy="' + xy[n - 1].split(',')[1] + '" r="2" fill="currentColor"/></svg>';
  }

  function MonitorRow(p) {
    p = p || {}; var s = st(p.state);
    var trend = p.points ? Sparkline({ points: p.points, state: s }) : '<span class="vk-row__data">' + esc(p.next || '') + '</span>';
    var lastCls = (s === 'down' || s === 'late') ? ' vk-row__data--' + s : '';
    return '<a class="vk-row" href="' + esc(p.href || '#') + '"' + (p.current ? ' aria-current="true"' : '') + '>' +
      glyph(s, 'vk-row__glyph') + '<span class="vk-row__name"><span>' + esc(p.name) + '</span><span class="vk-row__slug">' + esc(p.slug) + '</span></span>' +
      KindIcon({ kind: p.kind }) + '<span class="vk-row__data' + lastCls + '" title="' + esc(p.lastAbs || '') + '">' + esc(p.last) + '</span>' +
      trend + '<span class="vk-row__tags">' + (p.tags || []).map(function (t) { return Tag({ label: t }); }).join('') + '</span></a>';
  }

  function UptimeBar(p) {
    p = p || {}; var days = p.days || [];
    return '<div><div class="vk-uptime' + (p.compact ? ' vk-uptime--compact' : '') + '" role="img" aria-label="' + esc(p.label || (days.length + ' days')) + '">' +
      days.map(function (d) { return '<span class="vk-uptime__cell' + (d !== 'up' ? ' vk-uptime__cell--' + esc(d) : '') + '"></span>'; }).join('') + '</div>' +
      (p.legend ? '<div class="vk-uptime__legend"><span>' + esc(p.legend[0]) + '</span><span>' + esc(p.legend[1]) + '</span><span>' + esc(p.legend[2]) + '</span></div>' : '') + '</div>';
  }

  function StatusBanner(p) {
    p = p || {}; var s = p.state || 'up', g = s === 'maintenance' ? 'paused' : s;
    return '<div class="vk-banner vk-banner--' + esc(s) + '" role="status">' + glyph(g) + '<span>' + esc(p.text) + '</span>' +
      (p.detail ? '<span class="vk-banner__detail">' + esc(p.detail) + '</span>' : '') + '</div>';
  }

  function EmptyState(p) {
    p = p || {}; var url = esc(p.base || 'https://vink.example.com/ping/') + esc(p.key || 'k7f3q9x2mz');
    return '<div class="vk-empty"><h3>No monitors yet</h3><p>Point a cron job at your project’s ping URL. The first ping creates the monitor.</p>' +
      '<pre class="vk-code"><i># every night at 03:00, then tell vink it ran</i>\n0 3 * * * restic backup &amp;&amp; curl -fsS <b>' + url + '/nightly-backup?create=1</b></pre>' +
      '<p>Or run <span class="vk-mono">vink apply -f vink.yaml</span> to declare them all at once.</p></div>';
  }

  /* project header: mark, the project switcher with its three sections, search, user. `hrefs` overrides the section links */
  function TopBar(p) {
    p = p || {}; var org = esc(p.org || 'w4j'), proj = esc(p.project || 'homelab'), cur = p.section || 'monitors', base = '/o/' + org + '/p/' + proj, h = p.hrefs || {};
    var n = p.incidents || 0;
    var link = function (id, label, href, extra) {
      return '<a class="vk-top__link" href="' + esc(h[id] || href) + '"' + (cur === id ? ' aria-current="page"' : '') + '>' + label + (extra || '') + '</a>';
    };
    return '<header class="vk-top"><a class="vk-top__mark" href="' + esc(h.home || '/') + '">' + Mark({ size: 22 }) + '<span>vink</span></a>' +
      '<nav class="vk-top__nav" aria-label="Project"><button type="button" class="vk-top__crumb" aria-haspopup="menu">' + org + ' / <b>' + proj + '</b><i class="vk-caret" aria-hidden="true"></i></button>' +
      link('monitors', 'Monitors', base) +
      link('incidents', 'Incidents', base + '/incidents', n ? '<span class="vk-top__count" title="' + n + ' open">' + glyph('down') + n + '</span>' : '') +
      link('settings', 'Settings', base + '/settings/channels') + '</nav>' +
      '<input class="vk-input vk-top__search" type="search" placeholder="Search monitors  /" aria-label="Search monitors">' +
      '<button type="button" class="vk-top__user" aria-haspopup="menu" title="' + esc(p.user || 'j') + '">' + esc((p.user || 'j').charAt(0).toUpperCase()) + '</button></header>';
  }

  function Tabs(p) {
    p = p || {};
    return '<nav class="vk-tabs" aria-label="' + esc(p.label || 'Sections') + '">' + (p.tabs || []).map(function (t) {
      return '<a class="vk-tab" href="' + esc(t.href || '#') + '"' + (t.id === p.current ? ' aria-current="page"' : '') + '>' + esc(t.label) +
        (t.count != null ? '<span class="vk-tab__n">' + esc(t.count) + '</span>' : '') + '</a>';
    }).join('') + '</nav>';
  }

  /* one incident: open (with Ack), acked, or resolved (muted). The name links to the monitor. */
  function IncidentRow(p) {
    p = p || {}; var s = p.state || 'open', done = s === 'resolved';
    var act = s === 'open' ? Button({ label: 'Ack' }) : s === 'acked' ? '<span>acked by ' + esc(p.ackedBy || 'j') + '</span>' : '<span>resolved ' + esc(p.resolved || '') + '</span>';
    return '<div class="vk-irow vk-irow--' + esc(s) + '">' + glyph(done ? 'up' : 'down') +
      '<a class="vk-irow__name" href="' + esc(p.href || '#') + '"><span>' + esc(p.name) + '</span><span class="vk-row__slug">' + esc(p.slug) + '</span></a>' +
      '<span class="vk-irow__data" title="' + esc(p.reason) + '">' + esc(p.reason) + '</span>' +
      '<span class="vk-irow__data" title="' + esc(p.openedAbs || '') + '">' + esc(p.opened) + '</span>' +
      '<span class="vk-irow__data' + (done ? '' : ' vk-irow__data--live') + '">' + esc(p.duration) + '</span>' +
      '<span class="vk-irow__act">' + act + '</span></div>';
  }

  /* one row of a settings list. cells: [{text | html, size: 's'|'m'|'l', mono, ink}]; `lead`, `titleHtml`, `actions` are trusted HTML.
     Every row in one list uses the same cell sizes so the columns line up. */
  function SettingsRow(p) {
    p = p || {};
    return '<div class="vk-srow' + (p.muted ? ' vk-srow--muted' : '') + '">' + (p.lead != null ? '<span class="vk-srow__lead">' + p.lead + '</span>' : '') +
      '<div class="vk-srow__main"><span class="vk-srow__title">' + (p.titleHtml || esc(p.title)) + '</span>' + (p.sub ? '<span class="vk-srow__sub" title="' + esc(p.sub) + '">' + esc(p.sub) + '</span>' : '') + '</div>' +
      (p.cells || []).map(function (c) {
        return '<span class="vk-srow__cell vk-srow__cell--' + (c.size || 'm') + (c.mono ? ' vk-srow__cell--mono' : '') + (c.ink ? ' vk-srow__cell--ink' : '') + '">' + (c.html || esc(c.text)) + '</span>';
      }).join('') + '<span class="vk-srow__actions">' + (p.actions || '') + '</span></div>';
  }

  /* behaviour: call once after htmx swaps (htmx:afterSettle) or on load */
  function wire(root) {
    root = root || document;
    root.querySelectorAll('[data-copy]').forEach(function (b) {
      if (b.__vk) return; b.__vk = 1;
      b.addEventListener('click', function () {
        var t = b.getAttribute('data-copy'), done = function () { var l = b.textContent; b.textContent = 'Copied'; setTimeout(function () { b.textContent = l; }, 1400); };
        if (navigator.clipboard) navigator.clipboard.writeText(t).then(done, function () {}); });
    });
    root.querySelectorAll('[data-confirm]').forEach(function (b) {
      if (b.__vk) return; b.__vk = 1; var label = b.textContent, timer;
      b.addEventListener('click', function (e) {
        if (!b.hasAttribute('data-armed')) { e.preventDefault(); e.stopImmediatePropagation(); b.setAttribute('data-armed', ''); b.textContent = b.getAttribute('data-confirm');
          timer = setTimeout(function () { b.removeAttribute('data-armed'); b.textContent = label; }, 4000); }
        else { clearTimeout(timer); }
      }, true);
    });
    root.querySelectorAll('.vk-switch').forEach(function (s) {
      if (s.__vk) return; s.__vk = 1;
      s.addEventListener('click', function () { if (s.hasAttribute('hx-post')) return; var on = s.getAttribute('aria-checked') !== 'true';
        s.setAttribute('aria-checked', on); s.querySelector('.vk-switch__word').textContent = on ? 'on' : 'off'; });
    });
  }

  var w = window; w.Vink = w.Vink || {};
  var api = { Mark: Mark, StateBadge: StateBadge, Button: Button, Chip: Chip, Tag: Tag, KindIcon: KindIcon, Field: Field, PingUrl: PingUrl, Sparkline: Sparkline,
    MonitorRow: MonitorRow, UptimeBar: UptimeBar, StatusBanner: StatusBanner, EmptyState: EmptyState,
    TopBar: TopBar, Tabs: Tabs, FieldRow: FieldRow, Checkbox: Checkbox, Switch: Switch, Segmented: Segmented, KindPicker: KindPicker,
    Disclosure: Disclosure, Notice: Notice, Code: Code, Panel: Panel, IncidentRow: IncidentRow, SettingsRow: SettingsRow, wire: wire, esc: esc };
  for (var k in api) w.Vink[k] = api[k];
})();
