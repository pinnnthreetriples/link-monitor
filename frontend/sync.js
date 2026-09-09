/* Link Monitor — the Файлы tab's shared-folder section: whether it is on, both
   folders, when the last pass ran, what moved, what was left alone and why,
   and the conflict copies it made.

   Every sentence here comes from the server, in Russian, and is shown verbatim
   — the message at the top of the section, the reason beside each skipped
   file, and the standing note about deletions. What this file owns is the
   labels and the shape, which are the design's, and nothing else.

   The whole section is built here rather than in index.html because it hangs
   off one anchor element there: the page is shared with other work, and one
   line of markup is a smaller thing to collide over than forty. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;

  var pollTimer = null;
  var busy = false;

  var ARROW_UP = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 19V6M6.5 11.5L12 6l5.5 5.5"></path></svg>';
  var ARROW_DOWN = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 5v13M6.5 12.5L12 18l5.5-5.5"></path></svg>';
  var PAUSE = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M12 8v5M12 16h.01"></path></svg>';
  var WARN = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3.5l9 15.5H3l9-15.5z"></path><path d="M12 9.5v4M12 16.4h.01"></path></svg>';
  var INFO = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M12 11v5M12 8h.01"></path></svg>';

  /* ---------------- the section's furniture ---------------- */

  var nodes = null;

  // build lays the section out once. Everything that changes between passes is
  // filled in by paint.
  function build(anchor) {
    var head = el('div', 'sect-head');
    head.appendChild(el('h2', 'sect-title', 'Общая папка'));
    var run = el('button', 'linkish', 'Сверить сейчас');
    run.type = 'button';
    head.appendChild(run);
    anchor.appendChild(head);

    var kv = el('div', 'kv');
    anchor.appendChild(kv);

    var msg = el('div', 'msg');
    msg.setAttribute('aria-live', 'polite');
    anchor.appendChild(msg);

    // The conflict card. It is the design's warning card rather than a row in
    // a list, because a conflict is the one thing that must be impossible to
    // miss — and it sits above the lists for the same reason.
    var conflicts = el('div', 'warn-card');
    conflicts.hidden = true;
    conflicts.appendChild(LM.ico('warn-ico', WARN));
    var conflictBody = el('div');
    conflictBody.appendChild(el('div', 'warn-title', 'Расхождение'));
    var conflictText = el('div', 'warn-text');
    conflictBody.appendChild(conflictText);
    var conflictList = el('div', 'warn-text');
    conflictBody.appendChild(conflictList);
    conflicts.appendChild(conflictBody);
    anchor.appendChild(conflicts);

    // The history card, for the pass that had nothing to compare against.
    var lost = el('div', 'warn-card');
    lost.hidden = true;
    lost.appendChild(LM.ico('warn-ico', WARN));
    var lostBody = el('div');
    lostBody.appendChild(el('div', 'warn-title', 'История синхронизации недоступна'));
    lostBody.appendChild(el('div', 'warn-text',
      'Программа не смогла прочитать, что было синхронизировано в прошлый раз. ' +
      'Любое различие между машинами она сохранит как копию рядом с вашим файлом, ' +
      'ничего не перезаписывая.'));
    lost.appendChild(lostBody);
    anchor.appendChild(lost);

    var moved = el('div');
    anchor.appendChild(moved);

    var skippedHead = el('div', 'sect-head');
    skippedHead.hidden = true;
    skippedHead.appendChild(el('h2', 'sect-title', 'Пропущено'));
    anchor.appendChild(skippedHead);
    var skipped = el('div');
    anchor.appendChild(skipped);

    var note = el('div', 'note-card');
    note.appendChild(LM.ico('note-ico', INFO));
    var noteText = el('div', 'note-text');
    note.appendChild(noteText);
    anchor.appendChild(note);

    run.addEventListener('click', runPass);
    return {
      run: run,
      kv: kv,
      msg: msg,
      conflicts: conflicts,
      conflictText: conflictText,
      conflictList: conflictList,
      lost: lost,
      moved: moved,
      skippedHead: skippedHead,
      skipped: skipped,
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

  function tookText(ms) {
    if (typeof ms !== 'number' || ms < 0) { return ''; }
    if (ms < 1000) { return ms + ' мс'; }
    return (ms / 1000).toFixed(1).replace('.', ',') + ' с';
  }

  function paintFacts(data) {
    var kv = nodes.kv;
    LM.clear(kv);

    row(kv, 'Синхронизация', data.on ? 'включена' : 'выключена');
    if (!data.on) { return; }

    row(kv, 'Папка здесь', data.folder || '—', true);
    row(kv, 'Папка на второй машине', data.peerFolder || '—', true);
    row(kv, 'Не переносим файлы больше', data.maxSize || '—');

    var when = 'ещё не выполнялась';
    if (data.running) {
      when = 'идёт сейчас';
    } else if (data.at) {
      when = LM.fmtDateTime(data.at);
      var took = tookText(data.tookMs);
      if (took) { when += ' · ' + took; }
    }
    row(kv, 'Последняя сверка', when);
  }

  // A pass that failed is the one thing worth an err tone; a conflict has its
  // own card and does not need the message to shout as well.
  //
  // Whether it failed is the server's answer, not this file's guess: reading
  // the Russian sentence to work it out would put the section one wording
  // change away from drawing a working sync in red.
  function messageTone(data) {
    if (!data.on) { return 'off'; }
    if (data.failed) { return 'err'; }
    if (conflictsOf(data).length > 0) { return 'warn'; }
    return 'ok';
  }

  function conflictsOf(data) {
    return (data.moved || []).filter(function (m) { return !!m.conflict; });
  }

  function paintConflicts(data) {
    var made = conflictsOf(data);
    nodes.conflicts.hidden = made.length === 0;
    if (made.length === 0) { return; }

    nodes.conflictText.textContent = 'Файл изменили на обеих машинах, поэтому ничего не ' +
      'перезаписано: ваш файл на месте, а копия со второй машины лежит рядом с ним.';
    LM.clear(nodes.conflictList);
    made.forEach(function (m) {
      var line = el('div', 'list-name mono', (m.name || '') + '  →  ' + (m.saved || ''));
      nodes.conflictList.appendChild(line);
    });
  }

  function wayText(data, m) {
    var peer = data.peer || 'вторую машину';
    return String(m.way) === 'to_us' ? '← с ' + peer : '→ на ' + peer;
  }

  function paintMoved(data) {
    var box = nodes.moved;
    LM.clear(box);

    var moved = data.moved || [];
    if (!data.on) { return; }
    if (moved.length === 0) {
      var empty = el('p', 'empty', data.at ? 'В этот раз ничего не переносилось.'
        : 'Первая сверка ещё не выполнялась.');
      box.appendChild(empty);
      return;
    }

    moved.forEach(function (m) {
      var line = el('div', 'list-row');
      LM.setTone(line, m.conflict ? 'warn' : 'ok');
      line.appendChild(LM.ico('list-ico', String(m.way) === 'to_us' ? ARROW_DOWN : ARROW_UP));

      var main = el('div', 'list-main');
      main.appendChild(el('div', 'list-name mono', m.name || ''));
      var parts = [wayText(data, m)];
      if (m.size) { parts.push(m.size); }
      if (m.conflict) { parts.push('сохранено как копия рядом'); }
      main.appendChild(el('div', 'list-note', parts.join(' · ')));
      line.appendChild(main);
      box.appendChild(line);
    });
  }

  function paintSkipped(data) {
    var box = nodes.skipped;
    LM.clear(box);

    var skipped = data.on ? (data.skipped || []) : [];
    nodes.skippedHead.hidden = skipped.length === 0;

    skipped.forEach(function (s) {
      var line = el('div', 'list-row');
      LM.setTone(line, s.why === 'busy' ? 'off' : 'warn');
      line.appendChild(LM.ico('list-ico', PAUSE));

      var main = el('div', 'list-main');
      main.appendChild(el('div', 'list-name mono', s.name || ''));
      var parts = [];
      if (s.size) { parts.push(s.size); }
      // The reason is the server's own sentence, shown as it came.
      if (s.reason) { parts.push(s.reason); }
      main.appendChild(el('div', 'list-note', parts.join(' · ')));
      line.appendChild(main);
      box.appendChild(line);
    });
  }

  function paint(data) {
    data = data || {};
    paintFacts(data);
    LM.showMsg(nodes.msg, data.message || '', messageTone(data));
    paintConflicts(data);
    nodes.lost.hidden = !data.historyLost;
    paintMoved(data);
    paintSkipped(data);
    nodes.noteText.textContent = data.note || '';
    nodes.run.hidden = !data.on;
    setBusy(busy || !!data.running);
  }

  function setBusy(on) {
    nodes.run.disabled = on;
    nodes.run.textContent = on ? 'Сверяю…' : 'Сверить сейчас';
  }

  /* ---------------- talking to the app ---------------- */

  function load() {
    return LM.api('GET', '/api/sync').then(paint, function () {
      /* the offline banner already says the app is not answering */
    });
  }

  function runPass() {
    busy = true;
    setBusy(true);
    LM.api('POST', '/api/sync/run').then(function (res) {
      res = res || {};
      LM.pushLog(res.message || 'Сверка запущена.', res.ok ? 'ok' : 'err');
      busy = false;
      // The pass runs in the app, not in this request, so what happens next is
      // simply the next poll.
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
    pollTimer = setInterval(load, 3000);
  }

  function stopPolling() {
    if (!pollTimer) { return; }
    clearInterval(pollTimer);
    pollTimer = null;
  }

  /* ---------------- wiring ---------------- */

  var anchor = $('sync-sect');
  if (anchor) {
    nodes = build(anchor);
    LM.on('tab', function (name) {
      if (name === 'files') { startPolling(); } else { stopPolling(); }
    });
  }
}());
