/* Link Monitor — Подключить / Отключить on the Связь tab: the two buttons, the
   dimming that says which one would be a no-op, and the answer the app gives
   back, including the one-time Tailscale login link.

   The login URL is a credential. It lives in an href the user clicks and
   nowhere else, and it goes as soon as fresh status data arrives. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;

  // The tailscale row decides which of the two buttons is dimmed. warn and
  // unknown dim neither: we do not know which way the link is, so we do not
  // pretend to.
  function tailscaleState(status) {
    var found = 'unknown';
    ((status && status.checks) || []).forEach(function (c) {
      if (c.id === 'tailscale') { found = c.state || 'unknown'; }
    });
    return found;
  }

  function paintLinkButtons(status) {
    var st = tailscaleState(status);
    var up = $('link-up-btn');
    var down = $('link-down-btn');

    up.classList.toggle('is-dim', st === 'ok');
    down.classList.toggle('is-dim', st === 'fail');

    if (st === 'ok') { up.title = 'Связь уже поднята — нажатие ничего не сломает.'; }
    else { up.removeAttribute('title'); }
    if (st === 'fail') { down.title = 'Связь и так не поднята — нажатие ничего не сломает.'; }
    else { down.removeAttribute('title'); }
  }

  // The login URL is a one-time credential: it lives in an href the user
  // clicks, never in the console, the title or the hash, and it is dropped as
  // soon as fresh status data arrives.
  var loginAnchorLive = false;

  function dropLoginLink() {
    if (!loginAnchorLive) { return; }
    loginAnchorLive = false;
    var box = $('link-msg');
    var a = box.querySelector('a');
    if (!a) { return; }
    a.parentNode.removeChild(a);
    var br = box.querySelector('br');
    if (br) { br.parentNode.removeChild(br); }
    box.appendChild(el('br'));
    box.appendChild(el('span', null,
      'Ссылка для входа скрыта. Нажмите «Подключить» ещё раз, если вход не завершён.'));
  }

  function showLinkResult(res) {
    var box = $('link-msg');
    LM.clear(box);
    loginAnchorLive = false;
    LM.setTone(box, res.ok ? 'ok' : 'err');
    box.hidden = false;

    var text = res.message || (res.ok ? 'Готово.' : 'Не получилось.');
    box.appendChild(el('span', null, text));

    if (res.needsAdmin) {
      box.appendChild(el('br'));
      box.appendChild(el('span', null, 'Для этого нужны права администратора.'));
    }

    if (res.loginURL) {
      box.appendChild(el('br'));
      var a = el('a', null, 'Открыть страницу входа Tailscale');
      a.href = res.loginURL;
      a.rel = 'noreferrer noopener';
      // Leaves the app: desktop.js and the host route on data-external, and
      // target keeps even an unrouted activation out of the app window.
      a.setAttribute('data-external', '1');
      a.target = '_blank';
      box.appendChild(a);
      loginAnchorLive = true;
    }

    // The log keeps the sentence, never the URL.
    LM.pushLog(text, res.ok ? 'ok' : 'err');
  }

  function linkAction(action, btn, labelNode, busyText, idleText) {
    if (btn.disabled) { return; }
    btn.disabled = true;
    labelNode.textContent = busyText;

    LM.api('POST', '/api/link', { action: action }).then(function (res) {
      res = res || {};
      showLinkResult(res);
      btn.disabled = false;
      labelNode.textContent = idleText;

      // When a login URL came back, do not ask for a status ourselves: that
      // refresh is what drops the one-time link, and firing it here would take
      // the link away in the same breath as offering it. The stream or the poll
      // brings the next status, and the link goes then.
      if (!res.loginURL) {
        LM.refreshStatus();
        LM.loadHistory();
      }
    }, function (err) {
      LM.showMsg($('link-msg'), LM.errText(err), 'err');
      loginAnchorLive = false;
      btn.disabled = false;
      labelNode.textContent = idleText;
    });
  }

  $('link-up-btn').addEventListener('click', function () {
    linkAction('up', $('link-up-btn'), $('link-up-label'), 'Подключаю…', 'Подключить');
  });
  $('link-down-btn').addEventListener('click', function () {
    linkAction('down', $('link-down-btn'), $('link-down-label'), 'Отключаю…', 'Отключить');
  });

  paintLinkButtons(LM.state.status);

  LM.paintLinkButtons = paintLinkButtons;
  LM.dropLoginLink = dropLoginLink;
}());
