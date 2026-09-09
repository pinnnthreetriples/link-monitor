/* Link Monitor — the log on the Связь tab: the statuses and results this page
   actually received, newest first, and the button that empties it.

   It keeps sentences, never URLs, and it drops a status identical to the one
   above it so a quiet link does not fill the list with the same line. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;
  var tone = LM.tone;

  var log = [];
  var lastLogged = '';

  function renderLog() {
    var box = $('log');
    LM.clear(box);
    $('log-empty').hidden = log.length > 0;
    log.forEach(function (e) {
      var row = el('div', 'log-row');
      LM.setTone(row, e.tone);
      row.appendChild(el('div', 'log-dot'));
      row.appendChild(el('div', 'log-time mono', e.time));
      row.appendChild(el('div', 'log-text', e.text));
      box.appendChild(row);
    });
  }

  function pushLog(text, toneName, at) {
    if (!text) { return; }
    var d = at ? new Date(at) : new Date();
    if (isNaN(d.getTime())) { d = new Date(); }
    var hh = d.getHours() < 10 ? '0' + d.getHours() : '' + d.getHours();
    var mm = d.getMinutes() < 10 ? '0' + d.getMinutes() : '' + d.getMinutes();
    log.unshift({ time: hh + ':' + mm, text: text, tone: toneName || 'off' });
    log = log.slice(0, 30);
    renderLog();
  }

  function logStatus(status) {
    if (!status) { return; }
    var line = status.summary || '';
    if (status.detail && status.detail !== line) {
      line = line ? line + ' — ' + status.detail : status.detail;
    }
    if (!line) { return; }
    var key = (status.overall || '') + '|' + line;
    if (key === lastLogged) { return; }
    lastLogged = key;
    pushLog(line, tone(status.overall), status.takenAt);
  }

  $('log-clear').addEventListener('click', function () {
    log = [];
    lastLogged = '';
    renderLog();
  });

  renderLog();

  LM.pushLog = pushLog;
  LM.logStatus = logStatus;
}());
