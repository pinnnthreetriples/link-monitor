/* Link Monitor — the tiny helpers every tab uses: building a node, moving a
   palette tone onto it, and writing a time or a size the Russian way.

   Nothing here talks to the server or knows what a tab is. It is published on
   window.LM before anything else runs, because everything else builds on it. */

(function () {
  'use strict';

  var LM = (window.LM = window.LM || {});

  function $(id) { return document.getElementById(id); }

  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) { n.className = cls; }
    if (text !== undefined && text !== null) { n.textContent = String(text); }
    return n;
  }

  // The markup passed here is always a literal from our own files, never text
  // that came from the server.
  function ico(cls, markup) {
    var n = el('div', cls);
    n.setAttribute('aria-hidden', 'true');
    n.innerHTML = markup;
    return n;
  }

  function clear(node) { while (node.firstChild) { node.removeChild(node.firstChild); } }

  var TONES = ['t-ok', 't-warn', 't-err', 't-off'];

  function setTone(node, tone) {
    if (!node) { return; }
    node.classList.remove.apply(node.classList, TONES);
    node.classList.add('t-' + tone);
  }

  // Maps the API's state enum onto a palette tone.
  function tone(state) {
    if (state === 'ok') { return 'ok'; }
    if (state === 'warn') { return 'warn'; }
    if (state === 'fail') { return 'err'; }
    return 'off';
  }

  var STATE_WORD = {
    ok: 'В порядке',
    warn: 'Внимание',
    fail: 'Ошибка',
    unknown: 'Неизвестно'
  };

  function pad2(n) { return n < 10 ? '0' + n : '' + n; }

  function parseAt(value) {
    if (!value) { return null; }
    var d = new Date(value);
    return isNaN(d.getTime()) ? null : d;
  }

  function fmtTime(value) {
    var d = parseAt(value);
    return d ? pad2(d.getHours()) + ':' + pad2(d.getMinutes()) : '';
  }

  function fmtDateTime(value) {
    var d = parseAt(value);
    if (!d) { return ''; }
    return pad2(d.getDate()) + '.' + pad2(d.getMonth() + 1) + ' ' +
      pad2(d.getHours()) + ':' + pad2(d.getMinutes());
  }

  // Russian units, comma as the decimal separator.
  function fmtSize(bytes) {
    if (typeof bytes !== 'number' || !isFinite(bytes) || bytes < 0) { return ''; }
    var units = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'];
    var i = 0;
    var v = bytes;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; }
    var s = i === 0 ? String(Math.round(v)) : v.toFixed(1).replace('.', ',');
    return s + ' ' + units[i];
  }

  LM.$ = $;
  LM.el = el;
  LM.ico = ico;
  LM.clear = clear;
  LM.setTone = setTone;
  LM.tone = tone;
  LM.fmtTime = fmtTime;
  LM.fmtDateTime = fmtDateTime;
  LM.fmtSize = fmtSize;
  LM.STATE_WORD = STATE_WORD;
}());
