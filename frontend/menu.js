/* Link Monitor — the editing menu the window has to draw itself.

   Why this file exists. The host embeds WebView2 through
   github.com/jchv/go-webview2, where AreDefaultContextMenusEnabled and
   AreDevToolsEnabled are both driven from one Debug flag: the platform's own
   menu cannot be had without F12, and a window whose point is not to look
   like a browser cannot ship developer tools. So the host runs with context
   menus off — and right-click then does nothing anywhere, the text fields
   included. Ctrl+C/X/V/A still work; only the mouse route is gone, and
   right-click → Вставить in a path field is an expectation on a desktop
   program, not a luxury. Hence a menu drawn here, in the page: four items in
   an editable field, one over selected text, nothing over the window's
   furniture. desktop.js suppresses the default and calls in here.

   One honest limitation, which the menu itself admits to: pasting from a
   menu click is the one thing the engine will not simply do.
   execCommand('paste') is refused in Chromium — queryCommandSupported
   reports it unsupported and the field is left untouched — and
   navigator.clipboard.readText() needs the clipboard-read permission, which
   starts out 'prompt' and is answered by the host, not by this page. So
   Вставить tries the async clipboard, and the first time it is refused the
   item disables itself and the menu says which keys do work: Ctrl+V, which
   nothing can block. Every item here either acts or is visibly disabled;
   none of them silently does nothing. */

(function () {
  'use strict';

  var LM = (window.LM = window.LM || {});

  var GAP = 6;              // breathing room between the menu and the window edge
  var BLOCKED = 'Буфер обмена закрыт для окна — вставьте с клавиатуры: Ctrl+V.';
  var EMPTY = 'Буфер обмена пуст.';

  var open = null;          // the menu on screen, or null
  var pasteBlocked = false; // set once the clipboard has refused to be read

  /* ---------------- what the pointer landed on ---------------- */

  // The input types that hold editable text. A missing type is a text field;
  // checkboxes, files, colours and date pickers hold none.
  var TEXTY = /^(?:|text|search|url|tel|password|email|number)$/i;

  // A <select> counts as furniture rather than a field: it holds no editable
  // text, so all four items would be dead, and a dead item is worse than no
  // menu at all. Nor is a disabled control, which can hold neither caret nor
  // selection.
  function fieldFor(node) {
    var n = node;
    while (n && n.nodeType === 1) {
      if (n.tagName === 'SELECT') { return null; }
      if (n.tagName === 'TEXTAREA') { return n.disabled ? null : n; }
      if (n.tagName === 'INPUT') {
        return TEXTY.test(n.getAttribute('type') || '') && !n.disabled ? n : null;
      }
      if (n.isContentEditable) { return n; }
      n = n.parentNode;
    }
    return null;
  }

  // The engine's own answer to "is there anything to copy". It is the only
  // oracle that also works in a number field, where selectionStart reads null
  // and setSelectionRange throws.
  function cmdEnabled(name) {
    try { return !!document.queryCommandEnabled(name); } catch (e) { return false; }
  }

  function exec(name, arg) {
    try { return !!document.execCommand(name, false, arg === undefined ? null : arg); }
    catch (e) { return false; }
  }

  function docRange() {
    var sel = window.getSelection ? window.getSelection() : null;
    if (!sel || !sel.rangeCount || sel.isCollapsed) { return null; }
    try { return sel.getRangeAt(0).cloneRange(); } catch (e) { return null; }
  }

  /* ---------------- the context, read before focus moves ---------------- */

  // Focusing the menu blanks the field's highlight, so everything the items
  // need is read here, while the caret is still where the user left it.
  function contextAt(node) {
    var field = fieldFor(node);
    if (!field) {
      var range = docRange();
      if (!range || !String(range)) { return null; }
      return { field: null, start: null, end: null, range: range, text: String(range),
        readOnly: true, hasSel: true };
    }
    if (document.activeElement !== field) { focusOn(field); }
    var c = { field: field, start: null, end: null, text: null,
      range: field.isContentEditable ? docRange() : null,
      readOnly: !!field.readOnly, hasSel: cmdEnabled('copy') };
    try {
      if (typeof field.selectionStart === 'number') {
        c.start = field.selectionStart;
        c.end = field.selectionEnd;
        c.text = String(field.value).slice(c.start, c.end);
      }
    } catch (e) { /* a number field exposes no selection; the exec commands cover it */ }
    if (c.range) { c.text = String(c.range); }
    return c;
  }

  // preventScroll matters: a scroll would fire the handler that closes this.
  function focusOn(node) {
    try { node.focus({ preventScroll: true }); } catch (e) { node.focus(); }
  }

  function restore(c) {
    if (c.field) { focusOn(c.field); }
    if (c.range || !c.field) {
      var sel = window.getSelection ? window.getSelection() : null;
      if (sel && c.range) { sel.removeAllRanges(); sel.addRange(c.range); }
      return;
    }
    if (c.start === null) { return; }
    try { c.field.setSelectionRange(c.start, c.end); } catch (e) { /* no selection API */ }
  }

  function hasAnyText(c) {
    var f = c.field;
    if (!f) { return false; }
    return !!String((f.isContentEditable ? f.textContent : f.value) || '').length;
  }

  /* ---------------- the items ---------------- */

  function itemsFor(c) {
    if (!c.field) {
      return [{ id: 'copy', label: 'Копировать', key: 'Ctrl+C', on: c.hasSel }];
    }
    return [
      { id: 'cut', label: 'Вырезать', key: 'Ctrl+X', on: c.hasSel && !c.readOnly },
      { id: 'copy', label: 'Копировать', key: 'Ctrl+C', on: c.hasSel },
      { id: 'paste', label: 'Вставить', key: 'Ctrl+V', on: !c.readOnly && !pasteBlocked },
      { id: 'all', label: 'Выделить всё', key: 'Ctrl+A', on: hasAnyText(c) }
    ];
  }

  // LM.el is helpers.js's element helper: el(tag, class, text).
  function row(entry) {
    var n = LM.el('div', 'lm-menu-item');
    n.setAttribute('role', 'menuitem');
    n.setAttribute('tabindex', '-1');   // reachable by arrow keys, never by Tab
    n.setAttribute('aria-keyshortcuts', 'Control+' + entry.key.slice(-1));
    if (!entry.on) { n.setAttribute('aria-disabled', 'true'); }
    n.appendChild(LM.el('span', '', entry.label));
    n.appendChild(LM.el('span', 'lm-menu-key', entry.key));
    n.addEventListener('click', function () { fire(entry); });
    // The mouse takes the highlight over from the keyboard, never both at once.
    n.addEventListener('mouseenter', function () { mark(null); });
    return n;
  }

  /* ---------------- opening, placing, closing ---------------- */

  function build(c) {
    var root = LM.el('div', 'lm-menu');
    root.setAttribute('tabindex', '-1');
    var list = LM.el('div', 'lm-menu-list');
    list.setAttribute('role', 'menu');
    list.setAttribute('aria-label', 'Правка');
    root.appendChild(list);

    var items = itemsFor(c);
    items.forEach(function (entry) {
      entry.node = row(entry);
      list.appendChild(entry.node);
    });
    return { root: root, items: items, ctx: c, active: null, at: null };
  }

  // Flip to the other side of the pointer the way a desktop menu does, and
  // clamp only when even the flipped side does not fit — the window is as
  // small as 520x560, so both happen.
  function place() {
    var w = open.root.offsetWidth;
    var h = open.root.offsetHeight;
    var vw = document.documentElement.clientWidth;
    var vh = document.documentElement.clientHeight;
    var left = open.at.x + w + GAP > vw ? open.at.x - w : open.at.x;
    var top = open.at.y + h + GAP > vh ? open.at.y - h : open.at.y;
    open.root.style.left = Math.round(Math.max(GAP, Math.min(left, vw - w - GAP))) + 'px';
    open.root.style.top = Math.round(Math.max(GAP, Math.min(top, vh - h - GAP))) + 'px';
  }

  // A right-click says where it happened; the keyboard's own context key
  // reports no coordinates, so the field it came from answers for it.
  function pointOf(ev) {
    if (ev.clientX > 0 || ev.clientY > 0) { return { x: ev.clientX, y: ev.clientY }; }
    var box = ev.target && ev.target.getBoundingClientRect
      ? ev.target.getBoundingClientRect() : null;
    return box ? { x: box.left + 8, y: box.bottom + 2 } : { x: GAP, y: GAP };
  }

  function openAt(ev) {
    if (open && inMenu(ev.target)) { return true; }
    close('replaced');
    var c = contextAt(ev.target);
    if (!c) { return false; }

    open = build(c);
    open.at = pointOf(ev);
    document.body.appendChild(open.root);
    place();
    if (pasteBlocked && c.field && !c.readOnly) { note(BLOCKED); }
    var first = enabledItems()[0];
    mark(first || null);
    focusOn(first ? first.node : open.root);
    bind(true);
    return true;
  }

  function close(reason) {
    if (!open) { return; }
    var c = open.ctx;
    var root = open.root;
    bind(false);
    open = null;
    if (root.parentNode) { root.parentNode.removeChild(root); }
    // Esc and a scroll leave the field exactly as they found it. An outside
    // click is already moving focus itself, and after an item has run, the
    // action has put the caret where it belongs.
    if (reason === 'key' || reason === 'scroll') { restore(c); }
  }

  // Only ever shown when an item has to explain itself — see paste, below.
  function note(text) {
    if (!open) { return; }
    var n = open.root.querySelector('.lm-menu-note');
    if (!n) {
      n = LM.el('p', 'lm-menu-note');
      n.setAttribute('role', 'status');
      open.root.appendChild(n);
    }
    n.textContent = text;
    place();
  }

  /* ---------------- keyboard and pointer while open ---------------- */

  function enabledItems() {
    return open.items.filter(function (i) { return i.on; });
  }

  function mark(entry) {
    open.items.forEach(function (i) { i.node.classList.toggle('is-active', i === entry); });
    open.active = entry || null;
  }

  function goTo(entry) {
    if (entry) { mark(entry); focusOn(entry.node); }
  }

  // Disabled items are skipped, so Enter always has something to fire.
  function step(d) {
    var list = enabledItems();
    if (!list.length) { return; }
    var i = list.indexOf(open.active);
    if (i < 0) { goTo(d > 0 ? list[0] : list[list.length - 1]); return; }
    goTo(list[(i + d + list.length) % list.length]);
  }

  // Listening on window in the capture phase puts this ahead of desktop.js's
  // own document-level handler, so Esc closes the menu instead of dismissing
  // the panels behind it. Every other key closes the menu and is swallowed,
  // which is what a native menu does with a stray keystroke.
  function onKey(ev) {
    if (!open) { return; }
    var k = ev.key;
    if (k === 'Shift' || k === 'Control' || k === 'Alt' || k === 'Meta') { return; }
    ev.preventDefault();
    ev.stopPropagation();
    if (k === 'ArrowDown' || k === 'ArrowUp') { step(k === 'ArrowDown' ? 1 : -1); return; }
    if (k === 'Home' || k === 'End') {
      var list = enabledItems();
      goTo(k === 'Home' ? list[0] : list[list.length - 1]);
      return;
    }
    if (k === 'Enter' || k === ' ' || k === 'Spacebar') { fire(open.active); return; }
    close('key');
  }

  // A resize event's target is the window, which contains() will not take.
  function inMenu(node) {
    return !!(open && node && node.nodeType && open.root.contains(node));
  }

  function onDown(ev) { if (!inMenu(ev.target)) { close('outside'); } }

  function onScroll(ev) { if (!inMenu(ev.target)) { close('scroll'); } }

  function onBlur() { close('blur'); }

  function bind(on) {
    var m = on ? 'addEventListener' : 'removeEventListener';
    window[m]('keydown', onKey, true);
    window[m]('pointerdown', onDown, true);
    window[m]('mousedown', onDown, true);
    window[m]('scroll', onScroll, true);
    window[m]('resize', onScroll, true);
    // Bubble phase on purpose: an element losing focus does not reach the
    // window that way, so only the window's own blur closes the menu.
    window[m]('blur', onBlur, false);
  }

  /* ---------------- the four actions ---------------- */

  function fire(entry) {
    if (!entry || !entry.on || !open) { return; }
    var c = open.ctx;
    if (entry.id === 'paste') { doPaste(c, entry); return; }
    restore(c);
    if (entry.id === 'cut') { doCut(c); }
    else if (entry.id === 'copy') { doCopy(c); }
    else { doSelectAll(c); }
    close('item');
  }

  // Null when the selection could not be read — a number field — in which
  // case the exec commands are the whole answer, and they need no text.
  function writeText(text) {
    var clip = navigator.clipboard;
    if (!text || !clip || !clip.writeText) { return null; }
    try { return clip.writeText(text); } catch (e) { return null; }
  }

  function doCopy(c) {
    var p = writeText(c.text);
    if (!p) { exec('copy'); return; }
    p.catch(function () { exec('copy'); });
  }

  // execCommand('cut') is one undoable step, which is why it is the fallback
  // rather than the shape of the whole thing: the clipboard is written first,
  // and the text is only removed once it is safely somewhere else.
  function doCut(c) {
    var p = writeText(c.text);
    if (!p) { exec('cut'); return; }
    p.then(
      function () { if (!exec('insertText', '')) { exec('cut'); } },
      function () { exec('cut'); }
    );
  }

  function doSelectAll(c) {
    var f = c.field;
    if (!f) { return; }
    if (!f.isContentEditable) {
      try { f.setSelectionRange(0, String(f.value || '').length); return; }
      catch (e) { /* a number field: the engine's own selectAll instead */ }
    }
    exec('selectAll');
  }

  /* ---------------- paste, the one that has to explain itself ---------------- */

  function doPaste(c, entry) {
    if (execPaste(c)) { close('item'); return; }
    var clip = navigator.clipboard;
    if (!clip || !clip.readText) { refuse(entry); return; }
    clip.readText().then(function (text) {
      if (!open) { return; }
      if (!text) { note(EMPTY); return; }   // an empty clipboard is not a failure
      restore(c);
      if (!exec('insertText', text)) { refuse(entry); return; }
      close('item');
    }, function () { refuse(entry); });
  }

  // Honoured first for any host that does allow it. Chromium says up front
  // that it does not, which leaves the caret in the field untouched while the
  // async clipboard is asked instead.
  function execPaste(c) {
    var supported = false;
    try { supported = !!document.queryCommandSupported('paste'); } catch (e) { supported = false; }
    if (!supported) { return false; }
    restore(c);
    return exec('paste');
  }

  // The clipboard is closed to this window. Say so where the user is looking,
  // and leave the item disabled for the rest of the session with the shortcut
  // that does work still showing beside it.
  function refuse(entry) {
    pasteBlocked = true;
    if (!open) { return; }
    entry.on = false;
    entry.node.setAttribute('aria-disabled', 'true');
    if (open.active === entry) { goTo(enabledItems()[0]); }
    note(BLOCKED);
  }

  // desktop.js owns the contextmenu event and hands it here.
  LM.editMenu = { open: openAt };
}());
