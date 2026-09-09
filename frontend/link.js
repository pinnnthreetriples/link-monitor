/* Link Monitor — the Связь tab: the two machines and the line between them, the
   headline, the check rows, the fixes the server offers and the button that
   asks for a fresh check.

   The rest of the tab lives beside this file: link-log.js keeps the log,
   link-history.js draws the 24-hour strip, link-actions.js owns Подключить и
   Отключить. This file holds the wiring that drives all three. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;
  var tone = LM.tone;
  var STATE_WORD = LM.STATE_WORD;

  var LINK_WORD = {
    ok: 'связь есть',
    warn: 'связь частичная',
    fail: 'связи нет',
    unknown: 'состояние неизвестно'
  };

  var OVERALL_ICONS = {
    ok: '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M8.4 12.2l2.5 2.5 4.7-5"></path></svg>',
    warn: '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3.5l9 15.5H3l9-15.5z"></path><path d="M12 9.5v4M12 16.4h.01"></path></svg>',
    err: '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M9.2 9.2l5.6 5.6M14.8 9.2l-5.6 5.6"></path></svg>',
    off: '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M12 8h.01M12 11.5v5"></path></svg>'
  };

  var CHECK_ICONS = {
    tailscale: '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="5" r="2.4"></circle><circle cx="5" cy="19" r="2.4"></circle><circle cx="19" cy="19" r="2.4"></circle><path d="M11 7.2L6.2 16.7M13 7.2l4.8 9.5M7.5 19h9"></path></svg>',
    ssh_out: '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12h15M13.5 6.5L20 12l-6.5 5.5"></path></svg>',
    ssh_in: '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12H5M10.5 6.5L4 12l6.5 5.5"></path></svg>',
    sshd: '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4.5" width="18" height="6.5" rx="2"></rect><rect x="3" y="13" width="18" height="6.5" rx="2"></rect><path d="M7 7.7h.01M7 16.2h.01"></path></svg>',
    kill_switch: '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3.2l7 2.9v5.3c0 4.1-2.8 7.8-7 9.1-4.2-1.3-7-5-7-9.1V6.1l7-2.9z"></path><path d="M9.3 12.2l1.9 1.9 3.5-3.7"></path></svg>',
    _fallback: '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"></circle><path d="M12 8h.01M12 11.5v5"></path></svg>'
  };

  var FIX_ICON = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M14.7 6.3a4 4 0 01-5 5L5 16v3h3l4.7-4.7a4 4 0 015-5l-2.3-2.3 2.3-2.3"></path></svg>';

  var CHECK_ICO_IDLE = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20.5 12a8.5 8.5 0 11-2.6-6.1"></path><path d="M20.5 4.2v5.2h-5.2"></path></svg>';
  var CHECK_ICO_BUSY = '<svg class="spin" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round"><path d="M12 3a9 9 0 019 9" opacity="0.9"></path><circle cx="12" cy="12" r="9" opacity="0.28"></circle></svg>';

  /* ---------------- machines ---------------- */

  /* The state lines a machine card can show. The tones are the palette's own
     four; nothing here adds a colour or moves anything. */
  var ONLINE = { word: 'В сети', tone: 'ok' };
  var OFFLINE = { word: 'Не в сети', tone: 'off' };
  var NOTHING_KNOWN = { word: 'Неизвестно', tone: 'off' };

  function absentHint(name) { return 'Машина ' + name + ' не видна в tailnet.'; }

  /* What the last diagnosis established about the second machine, keyed by the
     token /api/status carries in peerState. The card renders this and works
     nothing out for itself: what is true about the link is the server's to
     state, and a front end deducing it from the check rows would be guessing
     at a fact it was already told.

     Two of the findings mean the machine did not answer, and they share the
     state line and differ in the tooltip, because they differ in what may be
     said next. «В сети» is the lie this table replaced — the tailnet goes on
     listing a node as connected for minutes after it is switched off — and a
     bare «Не в сети» would throw away a fact the tailnet is still asserting,
     so the short line says the machine is not answering and the tooltip says
     what the tailnet still thinks. */
  var PEER_STATE = {
    answers: ONLINE,
    silent_listed: {
      word: 'Не отвечает',
      tone: 'err',
      hint: 'Тайлнет всё ещё считает эту машину подключённой, но на проверку она не отвечает. ' +
        'Так бывает сразу после выключения: тайлнет замечает это не сразу.'
    },
    silent: {
      word: 'Не отвечает',
      tone: 'err',
      hint: 'Машина не отвечает на проверку. Что о ней думает тайлнет, сейчас неизвестно.'
    },
    offline: OFFLINE,
    absent: { word: 'Нет в tailnet', tone: 'off', hint: absentHint },
    unknown: {
      word: 'Неизвестно',
      tone: 'off',
      hint: 'Последняя проверка ничего не выяснила об этой машине.'
    }
  };

  // paintNode fills one card: the name and address it has, plus the one state
  // line and tooltip the caller decided on. It decides nothing itself.
  function paintNode(prefix, node, fallbackName, state) {
    var box = $('node-' + prefix);
    var name = (node && node.name) || fallbackName;
    $('node-' + prefix + '-name').textContent = name;
    $('node-' + prefix + '-addr').textContent = (node && node.addr) || 'адрес неизвестен';
    LM.setTone(box, state.tone);
    $('node-' + prefix + '-state').textContent = state.word;
    // The tooltip is where a second true fact goes when the line above has room
    // for one. A hint that names the machine arrives as a function of the name.
    var hint = typeof state.hint === 'function' ? state.hint(name) : state.hint;
    box.removeAttribute('title');
    if (hint) { box.title = hint; }
  }

  // This machine's own card. Being connected to the tailnet is a local fact the
  // daemon answers about itself, so there is nothing to weigh up here.
  function selfState(self) {
    if (!self) { return NOTHING_KNOWN; }
    return self.online ? ONLINE : OFFLINE;
  }

  function renderNodes(peers, status) {
    var list = peers || [];
    var self = null;
    list.forEach(function (p) { if (p.self && !self) { self = p; } });
    paintNode('self', self, 'Этот компьютер', selfState(self));

    // The right-hand card is the machine the app was configured to watch,
    // found by name. Taking the first non-self entry instead named whatever
    // Tailscale listed first — a phone, in this tailnet — while the check rows
    // below went on naming the real peer, so the card contradicted them.
    var wanted = LM.state.configuredPeer;
    var node = LM.configuredPeerNode(list);

    // The diagnosis is the authority on whether that machine answers, because
    // it is the only party that tried. «online» in /api/peers is the control
    // plane's flag — what the tailnet last heard — and it stays true for
    // minutes after a machine is switched off, which is the state this card was
    // reported for: «В сети» beside a headline reading «Связи нет — похоже, она
    // выключена». So the flag is used only while no diagnosis has an opinion,
    // which is before the first check has finished. A token this build does not
    // recognise claims nothing rather than falling back to the flag.
    var told = (status && typeof status.peerState === 'string') ? status.peerState : '';
    if (told) {
      paintNode('peer', node, wanted || 'Вторая машина', PEER_STATE[told] || PEER_STATE.unknown);
      return;
    }
    if (node) {
      paintNode('peer', node, wanted, node.online ? ONLINE : OFFLINE);
      return;
    }
    if (wanted) {
      // Configured but absent from the tailnet. This is the most alarming
      // thing the card can report, so it says whose absence it is rather than
      // borrowing another node's name and address.
      paintNode('peer', null, wanted, PEER_STATE.absent);
      return;
    }
    // Either /api/peers has not answered yet or it did not say which peer is
    // the configured one. Nothing is known, so nothing is claimed.
    paintNode('peer', null, 'Вторая машина', NOTHING_KNOWN);
  }

  function renderLink(status) {
    var col = $('link-col');
    var overall = status ? status.overall || 'unknown' : 'unknown';
    LM.setTone(col, tone(overall));

    var top;
    if (status && typeof status.latencyMs === 'number' && status.latencyMs > 0) {
      top = status.latencyMs + ' мс';
    } else if (LM.state.checking) {
      top = 'идёт проверка';
    } else {
      top = 'нет данных';
    }
    $('link-top').textContent = top;
    $('link-bottom').textContent = LINK_WORD[overall] || LINK_WORD.unknown;
    $('link-dot').hidden = !LM.state.checking;
  }

  function renderOverall(status) {
    var box = $('overall');
    var overall = status ? status.overall || 'unknown' : 'unknown';
    var t = tone(overall);
    LM.setTone(box, t);
    $('overall-ico').innerHTML = OVERALL_ICONS[t] || OVERALL_ICONS.off;

    if (!status) {
      $('overall-title').textContent = 'Состояние неизвестно';
      $('overall-sub').textContent = 'Приложение ещё не прислало результат проверки.';
      $('overall-stamp').textContent = '';
      return;
    }
    $('overall-title').textContent = status.summary || 'Состояние неизвестно';
    $('overall-sub').textContent = status.detail || '';
    $('overall-stamp').textContent = LM.state.checking
      ? 'идёт проверка'
      : (status.takenAt ? 'обновлено ' + LM.fmtTime(status.takenAt) : '');
  }

  function renderChecks(status) {
    var box = $('checks');
    LM.clear(box);
    var checks = (status && status.checks) || [];
    $('checks-empty').hidden = checks.length > 0;

    checks.forEach(function (c) {
      var row = el('div', 'row');
      LM.setTone(row, tone(c.state));
      row.appendChild(LM.ico('row-ico', CHECK_ICONS[c.id] || CHECK_ICONS._fallback));

      var main = el('div', 'row-main');
      main.appendChild(el('div', 'row-label', c.label || c.id || ''));
      if (c.note) { main.appendChild(el('div', 'row-note mono', c.note)); }
      row.appendChild(main);

      row.appendChild(el('div', 'row-state', STATE_WORD[c.state] || STATE_WORD.unknown));
      box.appendChild(row);
    });
  }

  /* ---------------- fixes ---------------- */

  function renderFixes(status) {
    var box = $('fixes');
    var fixes = (status && status.fixes) || [];
    LM.clear(box);
    $('fixes-sect').hidden = fixes.length === 0;

    fixes.forEach(function (fix) {
      var card = el('div', 'fix');
      var top = el('div', 'fix-top');
      top.appendChild(LM.ico('fix-ico', FIX_ICON));

      var main = el('div', 'row-main');
      main.appendChild(el('div', 'fix-title', fix.title || fix.id || ''));
      if (fix.explanation) { main.appendChild(el('div', 'fix-expl', fix.explanation)); }
      // Said before the button is pressed, never after.
      if (fix.needsAdmin) {
        main.appendChild(el('div', 'badge', 'Потребуются права администратора'));
      }
      top.appendChild(main);
      card.appendChild(top);

      var btn = el('button', 'btn btn-sm', 'Исправить');
      btn.type = 'button';

      function done(text, toneName) {
        // #fix-msg lives outside this block, so the refresh that follows a fix
        // cannot wipe the answer the user is still reading.
        LM.showMsg($('fix-msg'), text, toneName);
        LM.pushLog(text, toneName);
        btn.disabled = false;
        btn.textContent = 'Исправить';
      }

      btn.addEventListener('click', function () {
        btn.disabled = true;
        btn.textContent = 'Выполняю…';
        LM.api('POST', '/api/fix', { id: fix.id }).then(function (res) {
          res = res || {};
          done(res.message || (res.ok ? 'Готово.' : 'Не удалось выполнить.'), res.ok ? 'ok' : 'err');
          LM.refreshStatus();
          LM.loadHistory();
        }, function (err) {
          done(LM.errText(err), 'err');
        });
      });

      card.appendChild(btn);
      box.appendChild(card);
    });
  }

  /* ---------------- check button ---------------- */

  function paintCheckButton() {
    var btn = $('check-btn');
    btn.disabled = LM.state.checking;
    $('check-label').textContent = LM.state.checking ? 'Проверяю…' : 'Проверить';
    $('check-ico').innerHTML = LM.state.checking ? CHECK_ICO_BUSY : CHECK_ICO_IDLE;
  }

  function repaint(status) {
    paintCheckButton();
    LM.paintLinkButtons(status);
    renderLink(status);
    renderOverall(status);
  }

  function runCheck() {
    if (LM.state.checking) { return; }
    LM.state.checking = true;
    repaint(LM.state.status);

    LM.api('POST', '/api/check').then(function (status) {
      LM.applyStatus(status);
      LM.loadHistory();
    }, function (err) {
      LM.state.checking = false;
      repaint(LM.state.status);
      LM.pushLog(LM.errText(err), 'err');
    });
  }

  /* ---------------- wiring ---------------- */

  LM.on('status', function (status) {
    LM.dropLoginLink();
    repaint(status);
    renderChecks(status);
    renderFixes(status);
    // The card is repainted here too, and that is not belt-and-braces: what it
    // says about the second machine comes from the diagnosis, which arrives on
    // this event, while /api/peers is reloaded only on request. A card left to
    // the peers event would go on showing the previous run's finding.
    renderNodes(LM.state.peers, status);
    LM.logStatus(status);
  });

  LM.on('peers', function (peers) { renderNodes(peers, LM.state.status); });

  $('check-btn').addEventListener('click', runCheck);

  paintCheckButton();
  renderNodes(LM.state.peers, LM.state.status);
}());
