/* Link Monitor — the Файлы tab's shared-clipboard section: whether it is on,
   the one action that turns it off, what has travelled, and what was
   deliberately left behind.

   Every sentence here comes from the server, in Russian, and is shown verbatim
   — the message at the top, the reason beside each line, and the standing note
   about what is and is not carried. What this file owns is the labels and the
   shape, which are the design's, and nothing else.

   What this file never has is the content. The API does not serve it: a line
   says when something happened, which way it went and how big it was, and
   there is no field for what it was. Nothing here could show it if it wanted
   to.

   The whole section is built here rather than in index.html because it hangs
   off one anchor element there — the same reason sync.js does. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;

  var pollTimer = null;
  var busy = false;
  var state = null;

  var ARROW_UP = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V6M6.5 11.5L12 6l5.5 5.5"></path></svg>';
  var ARROW_DOWN = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v13M6.5 12.5L12 18l5.5-5.5"></path></svg>';
  var LOCK = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="11" width="14" height="9" rx="1.6"></rect><path d="M8.5 11V8.2a3.5 3.5 0 017 0V11"></path></svg>';
  var PAUSE = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M12 8v5M12 16h.01"></path></svg>';
  var CROSS = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M9.2 9.2l5.6 5.6M14.8 9.2l-5.6 5.6"></path></svg>';
  var WARN = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3.5l9 15.5H3l9-15.5z"></path><path d="M12 9.5v4M12 16.4h.01"></path></svg>';
  var INFO = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M12 11v5M12 8h.01"></path></svg>';

  /* The icon and the tone one kind of line gets. The kinds are the server's
     own tokens; an unknown one falls through to a neutral row rather than
     being dropped, so a kind this file has no wording for is visible instead
     of silent. */
  var KIND = {
    sent: { ico: ARROW_UP, tone: 'ok' },
    received: { ico: ARROW_DOWN, tone: 'ok' },
    marked: { ico: LOCK, tone: 'warn' },
    too_big: { ico: PAUSE, tone: 'warn' },
    failed: { ico: CROSS, tone: 'err' }
  };

  /* ---------------- the section's furniture ---------------- */

  var nodes = null;

  // build lays the section out once. Everything that changes is filled in by
  // paint.
  function build(anchor) {
    var head = el('div', 'sect-head');
    head.appendChild(el('h2', 'sect-title', 'Общий буфер обмена'));
    // The switch lives at the top of the section and is one click, because
    // that is the rule: it must be possible to see that it is on and to turn
    // it off in one action.
    var toggle = el('button', 'linkish', 'Включить');
    toggle.type = 'button';
    head.appendChild(toggle);
    anchor.appendChild(head);

    var kv = el('div', 'kv');
    anchor.appendChild(kv);

    var msg = el('div', 'msg');
    msg.setAttribute('aria-live', 'polite');
    anchor.appendChild(msg);

    // The peer's program not being there is a normal state and gets the
    // design's warning card rather than an error tone: nothing is broken, the
    // feature simply needs the program on both machines.
    var missing = el('div', 'warn-card');
    missing.hidden = true;
    missing.appendChild(LM.ico('warn-ico', WARN));
    var missingBody = el('div');
    missingBody.appendChild(el('div', 'warn-title', 'Программа не запущена на второй машине'));
    var missingText = el('div', 'warn-text');
    missingBody.appendChild(missingText);
    missing.appendChild(missingBody);
    anchor.appendChild(missing);

    var eventsHead = el('div', 'sect-head');
    eventsHead.hidden = true;
    eventsHead.appendChild(el('h2', 'sect-title', 'Последнее'));
    anchor.appendChild(eventsHead);
    var events = el('div');
    anchor.appendChild(events);
    var empty = el('p', 'empty', 'Пока ничего не передавалось.');
    anchor.appendChild(empty);

    var note = el('div', 'note-card');
    note.appendChild(LM.ico('note-ico', INFO));
    var noteText = el('div', 'note-text');
    note.appendChild(noteText);
    anchor.appendChild(note);

    toggle.addEventListener('click', flip);
    return {
      toggle: toggle,
      kv: kv,
      msg: msg,
      missing: missing,
      missingText: missingText,
      eventsHead: eventsHead,
      events: events,
      empty: empty,
      noteText: noteText
    };
  }

  /* ---------------- painting ---------------- */

  function row(kv, key, value, mono) {
    var line = el('div', 'kv-row');
    line.appendChild(el('div', 'kv-key', key));
    line.appendChild(el('div', mono ? 'kv-val mono' : 'kv-val', value));
    kv.appendChild(line);
  }

  function stateWord(data) {
    if (!data.available) { return 'недоступен'; }
    return data.on ? 'включён' : 'выключен';
  }

  // countsWord lists only what is not zero: a row of zeroes says nothing, and
  // the two refusals are the numbers worth noticing when they appear.
  function countsWord(counts) {
    var parts = [];
    if (counts.sent) { parts.push('передано ' + counts.sent); }
    if (counts.received) { parts.push('получено ' + counts.received); }
    if (counts.failed) { parts.push('не удалось ' + counts.failed); }
    return parts.length ? parts.join(' · ') : 'пока ничего';
  }

  function skippedWord(counts) {
    var parts = [];
    if (counts.marked) { parts.push('запрещено программой-источником: ' + counts.marked); }
    if (counts.tooBig) { parts.push('больше допустимого: ' + counts.tooBig); }
    if (counts.notText) { parts.push('не текст: ' + counts.notText); }
    return parts.length ? parts.join(' · ') : '';
  }

  function paintFacts(data) {
    var kv = nodes.kv;
    LM.clear(kv);

    row(kv, 'Состояние', stateWord(data));
    if (!data.available) { return; }

    row(kv, 'Вторая машина', data.peer || '—', true);
    row(kv, 'Не передаём текст больше', data.maxSize || '—');
    row(kv, 'За этот запуск', countsWord(data.counts || {}));

    var skipped = skippedWord(data.counts || {});
    if (skipped) { row(kv, 'Не передано', skipped); }
  }

  // The tone is the server's answer, not this file's guess: reading the
  // Russian sentence to work out whether something is wrong would put the
  // section one wording change away from drawing a working feature in red.
  function messageTone(data) {
    if (!data.available || !data.on) { return 'off'; }
    if (data.failed) { return 'err'; }
    if (data.peerMissing) { return 'warn'; }
    return 'ok';
  }

  function paintEvents(data) {
    var box = nodes.events;
    LM.clear(box);

    var items = (data.available && data.on) ? (data.events || []) : [];
    nodes.eventsHead.hidden = items.length === 0;
    nodes.empty.hidden = items.length !== 0 || !data.available || !data.on;

    items.forEach(function (e) {
      var look = KIND[e.kind] || { ico: PAUSE, tone: 'off' };
      var line = el('div', 'list-row');
      LM.setTone(line, look.tone);
      line.appendChild(LM.ico('list-ico', look.ico));

      var main = el('div', 'list-main');
      // The label is the server's own sentence, shown as it came. There is no
      // name and no content to put beside it — only the label, the time and,
      // when there is one, the size.
      main.appendChild(el('div', 'list-name', e.label || ''));
      var parts = [];
      if (e.at) { parts.push(LM.fmtDateTime(e.at)); }
      if (e.size) { parts.push(e.size); }
      main.appendChild(el('div', 'list-note', parts.join(' · ')));
      line.appendChild(main);
      box.appendChild(line);
    });
  }

  function paint(data) {
    state = data = data || {};
    paintFacts(data);
    LM.showMsg(nodes.msg, data.message || '', messageTone(data));

    nodes.missing.hidden = !data.peerMissing;
    if (data.peerMissing) {
      nodes.missingText.textContent = 'Запустите Link Monitor на второй машине — ' +
        'общий буфер обмена работает только тогда, когда программа открыта на обеих. ' +
        'Скопированное здесь никуда не ушло.';
    }

    paintEvents(data);
    nodes.noteText.textContent = data.note || '';
    nodes.toggle.hidden = !data.available;
    setBusy(busy);
  }

  function setBusy(on) {
    nodes.toggle.disabled = on;
    if (on) {
      nodes.toggle.textContent = '…';
      return;
    }
    nodes.toggle.textContent = (state && state.on) ? 'Выключить' : 'Включить';
  }

  /* ---------------- talking to the app ---------------- */

  function load() {
    return LM.api('GET', '/api/clip').then(paint, function () {
      /* the offline banner already says the app is not answering */
    });
  }

  // flip is the one action. It sends the request for the state the user is
  // asking for rather than a "toggle", so a stale screen cannot switch the
  // feature on by accident.
  function flip() {
    var path = (state && state.on) ? '/api/clip/off' : '/api/clip/on';
    busy = true;
    setBusy(true);
    LM.api('POST', path).then(function (res) {
      res = res || {};
      LM.pushLog(res.message || '', res.ok ? 'ok' : 'err');
      busy = false;
      load();
    }, function (err) {
      busy = false;
      LM.showMsg(nodes.msg, LM.errText(err), 'err');
      setBusy(false);
    });
  }

  function startPolling() {
    if (pollTimer) { return; }
    load();
    pollTimer = setInterval(load, 2000);
  }

  function stopPolling() {
    if (!pollTimer) { return; }
    clearInterval(pollTimer);
    pollTimer = null;
  }

  /* ---------------- wiring ---------------- */

  var anchor = $('clip-sect');
  if (anchor) {
    nodes = build(anchor);
    LM.on('tab', function (name) {
      if (name === 'files') { startPolling(); } else { stopPolling(); }
    });
  }
}());
