/* vink UI behaviour beyond htmx: copy buttons, two-step destructive
   confirms, switches that post at once, the CSRF header, three keyboard
   shortcuts and the favicon swap while something is down. No framework,
   no build step. */
(function () {
  'use strict';

  function wire(root) {
    root = root || document;
    root.querySelectorAll('[data-copy]').forEach(function (b) {
      if (b.__vk) return; b.__vk = 1;
      b.addEventListener('click', function () {
        var t = b.getAttribute('data-copy');
        var done = function () { var l = b.textContent; b.textContent = 'Copied'; setTimeout(function () { b.textContent = l; }, 1400); };
        if (navigator.clipboard) navigator.clipboard.writeText(t).then(done, function () {});
      });
    });
    root.querySelectorAll('.vk-switch').forEach(function (s) {
      if (s.__vk) return; s.__vk = 1;
      s.addEventListener('click', function () {
        if (s.hasAttribute('hx-post')) return; // htmx posts and swaps the row
        var on = s.getAttribute('aria-checked') !== 'true';
        s.setAttribute('aria-checked', on); s.querySelector('.vk-switch__word').textContent = on ? 'on' : 'off';
      });
    });
    root.querySelectorAll('[data-confirm]').forEach(function (b) {
      if (b.__vk) return; b.__vk = 1; var label = b.textContent, timer;
      b.addEventListener('click', function (e) {
        if (!b.hasAttribute('data-armed')) {
          e.preventDefault(); e.stopImmediatePropagation();
          b.setAttribute('data-armed', ''); b.textContent = b.getAttribute('data-confirm');
          timer = setTimeout(function () { b.removeAttribute('data-armed'); b.textContent = label; }, 4000);
        } else { clearTimeout(timer); }
      }, true);
    });
  }

  function favicon() {
    var link = document.querySelector('link[rel="icon"]');
    var down = document.querySelector('[data-down-count]');
    if (!link || !down) return;
    var n = parseInt(down.getAttribute('data-down-count'), 10) || 0;
    var want = n > 0 ? link.getAttribute('data-icon-down') : link.getAttribute('data-icon-up');
    if (want && link.getAttribute('href') !== want) link.setAttribute('href', want);
  }

  function csrf() {
    var m = document.querySelector('meta[name="csrf-token"]');
    return m ? m.getAttribute('content') : '';
  }

  // Beside an open drawer the list drops its tags column (the drawer
  // shows them); with nothing selected the list takes the full width.
  // The server renders the drawer only when a monitor is open; the
  // script keeps an empty, hidden one as the target for row clicks.
  function syncDrawer() {
    var page = document.getElementById('page');
    if (!page) return;
    var drawer = document.getElementById('drawer');
    if (!drawer) {
      drawer = document.createElement('aside');
      drawer.className = 'vk-drawer';
      drawer.id = 'drawer';
      page.appendChild(drawer);
    }
    var empty = !drawer.firstElementChild || !!drawer.querySelector('[data-empty]');
    drawer.style.display = empty ? 'none' : '';
    page.classList.toggle('vk-page--full', empty);
  }

  document.addEventListener('DOMContentLoaded', function () { wire(document); favicon(); syncDrawer(); });
  document.addEventListener('htmx:after:settle', function () { wire(document); favicon(); syncDrawer(); });
  // htmx 4 hands the request context as e.detail.ctx (htmx 2 put headers
  // on e.detail). Set the header where it exists, and for a state-changing
  // request without a form put the token in the body too, so a bare
  // hx-post button such as the channel switch passes the CSRF check.
  document.addEventListener('htmx:config:request', function (e) {
    var t = csrf();
    if (!t || !e.detail) return;
    var ctx = e.detail.ctx || e.detail;
    var req = ctx.request || ctx;
    var headers = req.headers || ctx.headers;
    if (headers) headers['X-CSRF-Token'] = t;
    var method = String(req.method || 'GET').toUpperCase();
    var body = req.body;
    if (method !== 'GET' && body && typeof body.has === 'function' && !body.has('_csrf')) body.append('_csrf', t);
  });

  document.addEventListener('keydown', function (e) {
    var tag = (e.target && e.target.tagName) || '';
    var typing = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (e.target && e.target.isContentEditable);
    if (e.key === 'Escape') {
      var close = document.querySelector('[data-drawer-close]');
      if (close) { close.click(); }
      return;
    }
    if (typing || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === '/') { var s = document.querySelector('.vk-top__search'); if (s) { e.preventDefault(); s.focus(); } }
    if (e.key === 'n') { var c = document.querySelector('[data-create]'); if (c) { e.preventDefault(); c.click(); } }
  });

  window.Vink = { wire: wire, favicon: favicon };
})();
