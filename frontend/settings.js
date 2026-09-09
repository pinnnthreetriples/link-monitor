/* Link Monitor — the Настройки tab: what the app says about the machines in the
   tailnet, plus the preference rows from the prototype.

   The machine cards are filled from GET /api/peers only. The fixed API has no
   settings endpoint yet, so the switches below are kept in localStorage and the
   page says so in as many words rather than pretending the app obeys them. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;

  /* ---------------- machines ---------------- */

  function kvRow(key, value, mono) {
    var row = el('div', 'kv-row');
    row.appendChild(el('div', 'kv-key', key));
    row.appendChild(el('div', 'kv-val' + (mono ? ' mono' : ''), value));
    return row;
  }

  // Only one node in the tailnet is the machine this app watches, and the API
  // says which by name. Everything else is another node, not "the second
  // machine" — a tailnet with a phone in it used to show two of those.
  function peerTitle(p) {
    if (p.self) { return 'Этот компьютер'; }
    if (!LM.state.configuredPeer) { return 'Вторая машина'; }
    return LM.isConfiguredPeer(p) ? 'Вторая машина' : 'Ещё одна машина в tailnet';
  }

  function renderPeers(peers) {
    var box = $('peers');
    LM.clear(box);
    var list = peers || [];
    $('peers-empty').hidden = list.length > 0;

    list.forEach(function (p) {
      var block = el('div', 'sect');
      block.appendChild(el('h3', 'sect-title', peerTitle(p)));

      var kv = el('div', 'kv');
      kv.appendChild(kvRow('Имя в tailnet', p.name || 'неизвестно', true));
      kv.appendChild(kvRow('Адрес', p.addr || 'неизвестен', true));

      var stateRow = el('div', 'kv-row');
      stateRow.appendChild(el('div', 'kv-key', 'Состояние'));
      var val = el('div', 'kv-val ' + (p.online ? 't-ok' : 't-off'));
      val.appendChild(el('span', 'dot'));
      val.appendChild(el('span', null, ' ' + (p.online ? 'в сети' : 'не в сети')));
      val.style.display = 'flex';
      val.style.alignItems = 'center';
      val.style.gap = '6px';
      stateRow.appendChild(val);
      kv.appendChild(stateRow);

      block.appendChild(kv);
      box.appendChild(block);
    });
  }

  /* ---------------- local preferences ---------------- */

  var OPTIONS_KEY = 'lm.options';

  var TOGGLES = [
    { key: 'autostart', title: 'Запускать при входе в Windows', note: 'значок в системном трее', on: true },
    { key: 'notify', title: 'Уведомлять при обрыве', note: 'всплывающее окно Windows', on: true },
    { key: 'autoreconnect', title: 'Переподключать автоматически', note: 'поднимать Tailscale при обрыве', on: false }
  ];

  var INTERVALS = ['15 с', '30 с', '1 мин', '5 мин'];

  function readOptions() {
    var base = { interval: 2 };
    TOGGLES.forEach(function (t) { base[t.key] = t.on; });
    try {
      var raw = localStorage.getItem(OPTIONS_KEY);
      if (raw) {
        var saved = JSON.parse(raw);
        Object.keys(base).forEach(function (k) {
          if (saved[k] !== undefined) { base[k] = saved[k]; }
        });
      }
    } catch (e) { /* unreadable storage: the defaults stand */ }
    return base;
  }

  function writeOptions(opts) {
    try { localStorage.setItem(OPTIONS_KEY, JSON.stringify(opts)); }
    catch (e) { /* the choice simply does not survive a reload */ }
  }

  var options = readOptions();

  function renderOptions() {
    var box = $('options');
    LM.clear(box);

    TOGGLES.forEach(function (def) {
      var row = el('div', 'opt-row');
      var main = el('div', 'opt-main');
      main.appendChild(el('div', 'opt-title', def.title));
      main.appendChild(el('div', 'opt-note', def.note));
      row.appendChild(main);

      var btn = el('button', 'toggle');
      btn.type = 'button';
      btn.setAttribute('aria-pressed', options[def.key] ? 'true' : 'false');
      btn.setAttribute('aria-label', def.title);
      btn.addEventListener('click', function () {
        options[def.key] = !options[def.key];
        btn.setAttribute('aria-pressed', options[def.key] ? 'true' : 'false');
        writeOptions(options);
      });
      row.appendChild(btn);
      box.appendChild(row);
    });

    var ivRow = el('div', 'opt-row');
    var ivMain = el('div', 'opt-main');
    ivMain.appendChild(el('div', 'opt-title', 'Интервал опроса'));
    ivMain.appendChild(el('div', 'opt-note', 'как часто проверять связь'));
    ivRow.appendChild(ivMain);

    var chips = el('div', 'chips');
    chips.setAttribute('role', 'group');
    chips.setAttribute('aria-label', 'Интервал опроса');
    INTERVALS.forEach(function (label, i) {
      var chip = el('button', 'chip', label);
      chip.type = 'button';
      chip.setAttribute('aria-pressed', options.interval === i ? 'true' : 'false');
      chip.addEventListener('click', function () {
        options.interval = i;
        writeOptions(options);
        Array.prototype.forEach.call(chips.children, function (node, j) {
          node.setAttribute('aria-pressed', j === i ? 'true' : 'false');
        });
      });
      chips.appendChild(chip);
    });
    ivRow.appendChild(chips);
    box.appendChild(ivRow);
  }

  /* ---------------- wiring ---------------- */

  $('peers-refresh').addEventListener('click', function () { LM.loadPeers(); });

  renderOptions();
  renderPeers(LM.state.peers);

  LM.on('peers', renderPeers);
  LM.on('tab', function (name) {
    if (name === 'settings') { LM.loadPeers(); }
  });
}());
