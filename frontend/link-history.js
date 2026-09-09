/* Link Monitor — the 24-hour strip on the Связь tab: one segment per group of
   probes from /api/history, coloured by the worst state in the group.

   Grouping never averages an outage away, and the strip shows only what the
   server has actually recorded. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;
  var tone = LM.tone;
  var STATE_WORD = LM.STATE_WORD;

  var MAX_SEGMENTS = 120;
  var WORST = { fail: 3, warn: 2, unknown: 1, ok: 0 };

  function worseOf(a, b) {
    var av = WORST[a] === undefined ? 1 : WORST[a];
    var bv = WORST[b] === undefined ? 1 : WORST[b];
    return av >= bv ? a : b;
  }

  // More probes than pixels: group them and let the worst state in a group show,
  // so a short outage cannot be averaged away.
  function bucket(points) {
    if (points.length <= MAX_SEGMENTS) {
      return points.map(function (p) {
        return { state: p.state || 'unknown', from: p.at, to: p.at };
      });
    }
    var size = Math.ceil(points.length / MAX_SEGMENTS);
    var out = [];
    for (var i = 0; i < points.length; i += size) {
      var group = points.slice(i, i + size);
      var st = group[0].state || 'unknown';
      group.forEach(function (p) { st = worseOf(st, p.state || 'unknown'); });
      out.push({ state: st, from: group[0].at, to: group[group.length - 1].at });
    }
    return out;
  }

  function renderStrip(points) {
    var box = $('strip');
    LM.clear(box);
    var has = points && points.length > 0;
    box.hidden = !has;
    $('strip-empty').hidden = !!has;
    $('strip-count').textContent = has ? points.length + ' проверок' : '';
    if (!has) { return; }

    var counts = { ok: 0, warn: 0, fail: 0, unknown: 0 };
    points.forEach(function (p) {
      var s = p.state || 'unknown';
      if (counts[s] === undefined) { counts[s] = 0; }
      counts[s] += 1;
    });
    box.setAttribute('aria-label',
      'История за 24 часа: всего проверок ' + points.length +
      ', в порядке ' + counts.ok +
      ', с предупреждением ' + counts.warn +
      ', с ошибкой ' + counts.fail +
      ', без результата ' + counts.unknown);

    bucket(points).forEach(function (b) {
      var seg = el('div', 'strip-seg');
      LM.setTone(seg, tone(b.state));
      var from = LM.fmtDateTime(b.from);
      var to = LM.fmtDateTime(b.to);
      var when = from && to && from !== to ? from + ' — ' + to : (from || to);
      seg.title = (when ? when + ': ' : '') + (STATE_WORD[b.state] || STATE_WORD.unknown);
      box.appendChild(seg);
    });
  }

  function loadHistory() {
    return LM.api('GET', '/api/history').then(function (data) {
      renderStrip((data && data.points) || []);
    }, function () {
      // The banner already says the app is unreachable; the strip keeps what it had.
    });
  }

  LM.on('ready', function () {
    loadHistory();
    // The strip only moves as fast as the poller behind it.
    setInterval(loadHistory, 60000);
  });

  renderStrip([]);

  LM.loadHistory = loadHistory;
}());
