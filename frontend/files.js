/* Browser File objects are streamed to the local API. A filename is display
   metadata, never a path to guess on disk. The explicit path form remains for
   files opened via Explorer's context menu or typed by a power user. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;

  var dir = 'send';
  var pollTimer = null;
  var sending = false;

  var OUTGOING = { to: 1, send: 1, sent: 1, out: 1, outgoing: 1, up: 1, tx: 1 };
  var DONE = { ok: 1, done: 1, complete: 1, completed: 1, finished: 1 };
  var FAILED = { err: 1, error: 1, fail: 1, failed: 1 };
  var CANCELLED = { cancel: 1, canceled: 1, cancelled: 1, aborted: 1 };

  var ARROW_UP = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V6M6.5 11.5L12 6l5.5 5.5"></path></svg>';
  var ARROW_DOWN = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v13M6.5 12.5L12 18l5.5-5.5"></path></svg>';

  function isOutgoing(t) { return !!OUTGOING[String(t.dir || '').toLowerCase()]; }

  // A transfer still in flight carries state "ok" with pct below 100 — nothing
  // has gone wrong with it yet (internal/app/files.go). Only err and cancel are
  // finished regardless of pct.
  function isActive(t) {
    var s = String(t.state || '').toLowerCase();
    if (FAILED[s] || CANCELLED[s]) { return false; }
    return typeof t.pct === 'number' && t.pct < 100;
  }

  // The server preformats the size for a Russian reader ("48,2 МБ") and the UI
  // shows it verbatim. A number is only ever a fallback.
  function sizeText(t) {
    if (typeof t.size === 'string') { return t.size.trim(); }
    return LM.fmtSize(t.size);
  }

  function stateWord(t) {
    var s = String(t.state || '').toLowerCase();
    if (DONE[s]) { return 'готово'; }
    if (FAILED[s]) { return 'ошибка'; }
    if (CANCELLED[s]) { return 'отменено'; }
    return '';
  }

  function stateTone(t) {
    var s = String(t.state || '').toLowerCase();
    if (FAILED[s]) { return 'err'; }
    if (CANCELLED[s]) { return 'warn'; }
    if (DONE[s]) { return 'ok'; }
    return 'off';
  }

  /* ---------------- direction ---------------- */

  function paintDirection() {
    var send = dir === 'send';
    $('dir-send').setAttribute('aria-pressed', send ? 'true' : 'false');
    $('dir-recv').setAttribute('aria-pressed', send ? 'false' : 'true');
    $('send-block').hidden = !send;
    $('recv-block').hidden = send;
  }

  function peerName() {
    var sel = $('peer-select');
    return sel && sel.value ? sel.value : '';
  }

  function paintDropHints() {
    var peer = peerName();
    $('drop-sub').textContent = peer
      ? 'или нажмите, чтобы выбрать файл — уйдёт на ' + peer
      : 'или нажмите, чтобы выбрать файл';
    $('drop-cmd').textContent = 'Файл откроется на второй машине автоматически';
  }

  function fillPeers(peers) {
    var sel = $('peer-select');
    var previous = sel.value;
    LM.clear(sel);

    var others = (peers || []).filter(function (p) { return !p.self; });
    if (others.length === 0) {
      var none = el('option', null, 'машин в tailnet не видно');
      none.value = '';
      sel.appendChild(none);
      sel.disabled = true;
      paintDropHints();
      return;
    }

    sel.disabled = false;
    var configured = '';
    others.forEach(function (p) {
      var label = p.name || p.addr || '';
      var opt = el('option', null, p.online ? label : label + ' (не в сети)');
      opt.value = p.name || p.addr || '';
      if (LM.isConfiguredPeer(p)) { configured = opt.value; }
      sel.appendChild(opt);
    });
    // A select with options picks its first one on its own, and in a tailnet
    // with a phone in it that one can be the phone — sending a file there by
    // default is a mistake the user only notices once it has arrived. So the
    // choice is stated rather than inherited: what the user picked before if it
    // is still listed, else the machine the app watches, else whatever is first.
    if (previous || configured) { sel.value = previous || configured; }
    if (!sel.value && configured) { sel.value = configured; }
    if (!sel.value && sel.options.length) { sel.selectedIndex = 0; }
    paintDropHints();
  }

  /* ---------------- transfers ---------------- */

  function renderTransfers(list) {
    var box = $('xfers');
    LM.clear(box);

    var active = null;
    var rest = [];
    (list || []).forEach(function (t) {
      if (!active && isActive(t)) { active = t; } else { rest.push(t); }
    });

    // The card at the top: whatever is moving right now, as the app reports it.
    var xfer = $('xfer');
    if (active) {
      xfer.hidden = false;
      $('xfer-name').textContent = active.name || '';
      var bits = [isOutgoing(active) ? 'Отправка' : 'Приём'];
      var size = sizeText(active);
      if (size) { bits.push(size); }
      if (typeof active.pct === 'number') { bits.push(Math.round(active.pct) + '%'); }
      $('xfer-meta').textContent = bits.join(' · ');
      $('xfer-fill').style.width = Math.max(0, Math.min(100, active.pct || 0)) + '%';
    } else {
      xfer.hidden = true;
    }

    $('xfers-empty').hidden = rest.length > 0;

    rest.forEach(function (t) {
      var row = el('div', 'list-row');
      LM.setTone(row, stateTone(t));
      row.appendChild(LM.ico('list-ico', isOutgoing(t) ? ARROW_UP : ARROW_DOWN));

      var main = el('div', 'list-main');
      main.appendChild(el('div', 'list-name mono', t.name || ''));

      var parts = [isOutgoing(t) ? 'отправлен' : 'получен'];
      var size = sizeText(t);
      if (size) { parts.push(size); }
      var word = stateWord(t);
      if (word) { parts.push(word); }
      main.appendChild(el('div', 'list-note', parts.join(' · ')));
      row.appendChild(main);

      row.appendChild(el('div', 'list-time mono', LM.fmtTime(t.at)));
      box.appendChild(row);
    });
  }

  function loadTransfers() {
    return LM.api('GET', '/api/files').then(function (data) {
      renderTransfers((data && data.transfers) || []);
    }, function () { /* the offline banner already says it */ });
  }

  /* ---------------- sending ---------------- */

  function busy(on) {
    sending = on;
    $('send-btn').disabled = on;
    $('send-btn').textContent = on ? 'Отправляю…' : 'Отправить';
    $('drop').disabled = on;
  }

  function send(path) {
    if (!path) {
      LM.showMsg($('files-msg'), 'Сначала выберите файл или укажите путь к нему.', 'warn');
      return Promise.resolve();
    }
    var peer = peerName();
    if (!peer) {
      LM.showMsg($('files-msg'), 'Некуда отправлять: приложение не назвало ни одной машины в tailnet.', 'warn');
      return Promise.resolve();
    }

    busy(true);
    return LM.api('POST', '/api/files/send', { path: path, peer: peer }).then(function (res) {
      res = res || {};
      var text = res.message || (res.ok ? 'Файл отправлен.' : 'Отправить не удалось.');
      LM.showMsg($('files-msg'), text, res.ok ? 'ok' : 'err');
      LM.pushLog(text, res.ok ? 'ok' : 'err');
      busy(false);
      loadTransfers();
    }, function (err) {
      LM.showMsg($('files-msg'), LM.errText(err), 'err');
      busy(false);
    });
  }

  function upload(file, peer) {
    var form = new FormData();
    form.append('file', file, file.name);
    return fetch('/api/files/upload?peer=' + encodeURIComponent(peer), {
      method: 'POST', body: form, credentials: 'same-origin'
    }).then(function (response) {
      return response.json().then(function (body) {
        if (!response.ok || !body.ok) { throw new Error(body.message || 'Отправить не удалось.'); }
        return body;
      });
    });
  }

  // Send one file at a time; each response belongs to the correct filename.
  function sendFileList(files) {
    var picked = Array.prototype.slice.call(files || []);
    if (!picked.length || sending) { return; }
    var peer = peerName();
    if (!peer) {
      LM.showMsg($('files-msg'), 'Выберите компьютер для отправки.', 'warn');
      return;
    }
    busy(true);
    var chain = Promise.resolve();
    var sent = 0;
    picked.forEach(function (file) {
      chain = chain.then(function () {
        LM.showMsg($('files-msg'), 'Отправляю: ' + file.name, 'off');
        return upload(file, peer).then(function () { sent += 1; });
      });
    });
    chain.then(function () {
      var message = sent === 1 ? 'Файл отправлен.' : 'Отправлено файлов: ' + sent + '.';
      LM.showMsg($('files-msg'), message, 'ok');
      LM.pushLog(message, 'ok');
    }, function (err) {
      LM.showMsg($('files-msg'), LM.errText(err), 'err');
    }).then(function () {
      busy(false);
      loadTransfers();
    });
  }

  function receive() {
    var btn = $('recv-btn');
    btn.disabled = true;
    btn.textContent = 'Принимаю…';
    LM.api('POST', '/api/files/receive').then(function (res) {
      res = res || {};
      var text = res.message || (res.ok ? 'Готово.' : 'Принять не удалось.');
      var files = res.files || [];
      if (files.length) { text += ' (' + files.join(', ') + ')'; }
      LM.showMsg($('files-msg'), text, res.ok ? 'ok' : 'err');
      LM.pushLog(text, res.ok ? 'ok' : 'err');
      btn.disabled = false;
      btn.textContent = 'Принять входящие файлы';
      loadTransfers();
    }, function (err) {
      LM.showMsg($('files-msg'), LM.errText(err), 'err');
      btn.disabled = false;
      btn.textContent = 'Принять входящие файлы';
    });
  }

  /* ---------------- drag and drop ---------------- */

  function initDrop() {
    var zone = $('drop');
    var input = $('file-input');
    var depth = 0;

    ['dragenter', 'dragover'].forEach(function (type) {
      zone.addEventListener(type, function (ev) {
        ev.preventDefault();
        ev.stopPropagation();
        if (ev.dataTransfer) { ev.dataTransfer.dropEffect = 'copy'; }
        if (type === 'dragenter') { depth += 1; }
        zone.classList.add('is-over');
      });
    });

    zone.addEventListener('dragleave', function (ev) {
      ev.preventDefault();
      depth -= 1;
      if (depth <= 0) { depth = 0; zone.classList.remove('is-over'); }
    });

    zone.addEventListener('drop', function (ev) {
      ev.preventDefault();
      ev.stopPropagation();
      depth = 0;
      zone.classList.remove('is-over');
      var dt = ev.dataTransfer;
      if (!dt) { return; }
      if (dt.files && dt.files.length) {
        sendFileList(dt.files);
        return;
      }
      // A literal path from a terminal may still use the explicit path sender.
      var text = dt.getData ? (dt.getData('text/plain') || '') : '';
      text = text.trim().replace(/^"|"$/g, '');
      if (text) { send(text); }
    });

    zone.addEventListener('click', function () { input.click(); });
    input.addEventListener('change', function () {
      if (input.files && input.files.length) { sendFileList(input.files); }
      input.value = '';
    });
  }

  /* ---------------- wiring ---------------- */

  function startPolling() {
    if (pollTimer) { return; }
    loadTransfers();
    pollTimer = setInterval(loadTransfers, 3000);
  }

  function stopPolling() {
    if (!pollTimer) { return; }
    clearInterval(pollTimer);
    pollTimer = null;
  }

  $('dir-send').addEventListener('click', function () { dir = 'send'; paintDirection(); });
  $('dir-recv').addEventListener('click', function () { dir = 'recv'; paintDirection(); });
  $('peer-select').addEventListener('change', paintDropHints);
  $('send-btn').addEventListener('click', function () {
    send($('path-input').value.trim());
  });
  $('path-input').addEventListener('keydown', function (ev) {
    if (ev.key === 'Enter') { ev.preventDefault(); send($('path-input').value.trim()); }
  });
  $('recv-btn').addEventListener('click', receive);
  $('xfers-refresh').addEventListener('click', loadTransfers);

  initDrop();
  // Native WebView2 file drops can land anywhere, not only on the drop zone.
  window.addEventListener('drop', function (ev) {
    if (!ev.dataTransfer || !ev.dataTransfer.files.length) { return; }
    ev.preventDefault();
    $('tab-files').click();
    dir = 'send';
    paintDirection();
    sendFileList(ev.dataTransfer.files);
  });
  document.addEventListener('paste', function (ev) {
    var target = ev.target;
    if (target && (target.isContentEditable || /^(INPUT|TEXTAREA)$/.test(target.tagName))) { return; }
    var data = ev.clipboardData;
    if (!data || !data.files || !data.files.length) { return; }
    var files = [];
    for (var i = 0; i < data.files.length; i += 1) {
      var file = data.files[i];
      if (file.type === 'image/png' && !file.name) {
        file = new File([file], 'Скриншот-' + Date.now() + '.png', { type: file.type });
      }
      files.push(file);
    }
    ev.preventDefault();
    $('tab-files').click();
    dir = 'send';
    paintDirection();
    sendFileList(files);
  });
  paintDirection();
  fillPeers(LM.state.peers);

  LM.on('peers', fillPeers);
  LM.on('tab', function (name) {
    if (name === 'files') { startPolling(); } else { stopPolling(); }
  });
}());
