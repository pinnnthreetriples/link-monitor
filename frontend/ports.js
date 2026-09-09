/* Link Monitor — the Порты tab: live SSH forwards, a form to add one, and the
   tailscale serve button that hands back a URL for the phone.

   Everything shown here comes from GET /api/forward; nothing is remembered
   locally, so a forward that the app dropped disappears from the list too. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;

  var PORT_ICON = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12h15M13.5 6.5L20 12l-6.5 5.5"></path></svg>';
  var STOP_ICON = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 6l12 12M18 6L6 18"></path></svg>';

  function port(value) {
    var n = parseInt(value, 10);
    if (!isFinite(n) || n < 1 || n > 65535) { return 0; }
    return n;
  }

  /* ---------------- list ---------------- */

  function renderForwards(list) {
    var box = $('fwd-list');
    LM.clear(box);
    $('fwd-empty').hidden = (list || []).length > 0;

    (list || []).forEach(function (f) {
      var row = el('div', 'list-row t-ok');
      row.appendChild(LM.ico('list-ico', PORT_ICON));

      var main = el('div', 'list-main');
      main.appendChild(el('div', 'list-name mono',
        'localhost:' + f.localPort + '  →  ' + (f.remoteHost || '') + ':' + f.remotePort));
      main.appendChild(el('div', 'list-note', 'порт открыт на этой машине'));
      row.appendChild(main);

      var stop = el('button', 'btn btn-square');
      stop.type = 'button';
      stop.innerHTML = STOP_ICON;
      stop.setAttribute('aria-label', 'Остановить проброс порта ' + f.localPort);
      stop.title = 'Остановить';
      stop.addEventListener('click', function () {
        stop.disabled = true;
        LM.api('DELETE', '/api/forward', { id: f.id }).then(function (res) {
          res = res || {};
          var text = res.message || (res.ok ? 'Проброс остановлен.' : 'Остановить не удалось.');
          LM.showMsg($('fwd-msg'), text, res.ok ? 'ok' : 'err');
          load();
        }, function (err) {
          LM.showMsg($('fwd-msg'), LM.errText(err), 'err');
          stop.disabled = false;
        });
      });
      row.appendChild(stop);

      box.appendChild(row);
    });
  }

  function load() {
    return LM.api('GET', '/api/forward').then(function (data) {
      renderForwards((data && data.forwards) || []);
    }, function () { /* the offline banner already says it */ });
  }

  /* ---------------- create ---------------- */

  function create() {
    var localPort = port($('fwd-local').value);
    var remotePort = port($('fwd-remote').value);
    var host = $('fwd-host').value.trim();

    if (!localPort) {
      LM.showMsg($('fwd-msg'), 'Локальный порт должен быть числом от 1 до 65535.', 'warn');
      $('fwd-local').focus();
      return;
    }
    if (!host) {
      LM.showMsg($('fwd-msg'), 'Укажите хост на той стороне — например, localhost.', 'warn');
      $('fwd-host').focus();
      return;
    }
    if (!remotePort) {
      LM.showMsg($('fwd-msg'), 'Порт на той стороне должен быть числом от 1 до 65535.', 'warn');
      $('fwd-remote').focus();
      return;
    }

    var btn = $('fwd-btn');
    btn.disabled = true;
    btn.textContent = 'Пробрасываю…';

    LM.api('POST', '/api/forward', {
      localPort: localPort,
      remoteHost: host,
      remotePort: remotePort
    }).then(function (res) {
      res = res || {};
      var text = res.message || (res.ok ? 'Порт проброшен.' : 'Пробросить не удалось.');
      if (res.ok && res.addr) { text += ' Адрес: ' + res.addr; }
      LM.showMsg($('fwd-msg'), text, res.ok ? 'ok' : 'err');
      LM.pushLog(text, res.ok ? 'ok' : 'err');
      btn.disabled = false;
      btn.textContent = 'Пробросить порт';
      load();
    }, function (err) {
      LM.showMsg($('fwd-msg'), LM.errText(err), 'err');
      btn.disabled = false;
      btn.textContent = 'Пробросить порт';
    });
  }

  /* ---------------- tailscale serve ---------------- */

  function serve() {
    var p = port($('serve-port').value);
    if (!p) {
      LM.showMsg($('serve-msg'), 'Укажите порт от 1 до 65535 — тот, что слушает на этой машине.', 'warn');
      $('serve-port').focus();
      return;
    }

    var btn = $('serve-btn');
    btn.disabled = true;
    btn.textContent = 'Открываю…';

    LM.api('POST', '/api/serve', { port: p }).then(function (res) {
      res = res || {};
      var box = $('serve-msg');
      LM.clear(box);
      LM.setTone(box, res.ok ? 'ok' : 'err');
      box.hidden = false;

      var text = res.message || (res.ok ? 'Адрес открыт.' : 'Открыть не удалось.');
      box.appendChild(el('span', null, text));

      if (res.url) {
        box.appendChild(el('br'));
        // The address is data the user came for, so it stays visible and
        // selectable; opening it is the host's job, not this window's.
        var a = el('a', 'mono', res.url);
        a.href = res.url;
        a.rel = 'noreferrer noopener';
        a.setAttribute('data-external', '1');
        a.target = '_blank';
        box.appendChild(a);
      }
      LM.pushLog(text + (res.url ? ' ' + res.url : ''), res.ok ? 'ok' : 'err');

      btn.disabled = false;
      btn.textContent = 'Открыть для телефона';
    }, function (err) {
      LM.showMsg($('serve-msg'), LM.errText(err), 'err');
      btn.disabled = false;
      btn.textContent = 'Открыть для телефона';
    });
  }

  /* ---------------- wiring ---------------- */

  $('fwd-btn').addEventListener('click', create);
  $('fwd-refresh').addEventListener('click', load);
  $('serve-btn').addEventListener('click', serve);

  ['fwd-local', 'fwd-host', 'fwd-remote'].forEach(function (id) {
    $(id).addEventListener('keydown', function (ev) {
      if (ev.key === 'Enter') { ev.preventDefault(); create(); }
    });
  });
  $('serve-port').addEventListener('keydown', function (ev) {
    if (ev.key === 'Enter') { ev.preventDefault(); serve(); }
  });

  LM.on('tab', function (name) {
    if (name === 'ports') { load(); }
  });
}());
