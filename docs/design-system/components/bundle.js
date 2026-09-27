/* @ds-bundle: {"format":4,"namespace":"Vink","components":[{"name":"StateBadge"},{"name":"Button"},{"name":"Chip"},{"name":"Tag"},{"name":"KindIcon"},{"name":"Field"},{"name":"PingUrl"},{"name":"Sparkline"},{"name":"MonitorRow"},{"name":"UptimeBar"},{"name":"StatusBanner"},{"name":"EmptyState"}]} */
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
    return '<button type="button" class="vk-btn' + (v !== 'quiet' ? ' vk-btn--' + esc(v) : '') + '"' +
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

  function Field(p) {
    p = p || {}; var id = esc(p.id || 'f'); var hid = id + '-msg';
    return '<div class="vk-field' + (p.error ? ' vk-field--error' : '') + '">' +
      '<label class="vk-field__label" for="' + id + '">' + esc(p.label) + '</label>' +
      '<input class="vk-input' + (p.mono ? ' vk-input--mono' : '') + '" id="' + id + '" value="' + esc(p.value) + '" placeholder="' + esc(p.placeholder) + '"' +
      ((p.error || p.hint) ? ' aria-describedby="' + hid + '"' : '') + (p.error ? ' aria-invalid="true"' : '') + '>' +
      (p.error ? '<span class="vk-field__error" id="' + hid + '">' + glyph('down') + esc(p.error) + '</span>'
        : (p.hint ? '<span class="vk-field__hint" id="' + hid + '">' + esc(p.hint) + '</span>' : '')) + '</div>';
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
  }

  var w = window; w.Vink = w.Vink || {};
  var api = { Mark: Mark, StateBadge: StateBadge, Button: Button, Chip: Chip, Tag: Tag, KindIcon: KindIcon, Field: Field, PingUrl: PingUrl, Sparkline: Sparkline,
    MonitorRow: MonitorRow, UptimeBar: UptimeBar, StatusBanner: StatusBanner, EmptyState: EmptyState, wire: wire, esc: esc };
  for (var k in api) w.Vink[k] = api[k];
})();
