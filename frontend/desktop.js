/* Link Monitor — the window itself, as opposed to what is drawn in it.

   These files are not a page in a browser: they are hosted in an embedded
   WebView2 control inside a native Win32 window with its own title bar and
   taskbar button. A page shows Edge's context menu, blue-highlights its own
   buttons, navigates away when a file is dropped on it and throws the user's
   state away on F5. A program does none of that, so this file takes those
   four behaviours off — and adds the keyboard the desktop expects instead.
   A program does have an editing menu in its text fields, though, and the
   host cannot leave that one to WebView2: menu.js draws it, from here.

   Nothing here changes what the UI looks like. It changes what the window
   does.

   What the host (internal/ui) is expected to do, stated here because the two
   halves have to agree:

   - Keep the WebView2 default context menu OFF
     (AreDefaultContextMenusEnabled = false). In go-webview2 that flag and
     AreDevToolsEnabled are both driven from one Debug flag, so the platform
     menu cannot be had without F12 — and a window whose point is not to look
     like a browser cannot ship developer tools. The cost is that right-click
     then does nothing anywhere, the input fields included, so the editing
     menu those fields need is drawn in the page instead: see menu.js. This
     file still decides where a menu is wanted; menu.js decides what is in it.
   - Switch the status bar OFF (IsStatusBarEnabled = false), so a hovered link
     cannot draw a browser bubble across the bottom of the window.
   - Leave the browser accelerator keys enabled
     (AreBrowserAcceleratorKeysEnabled = true) so F5 and Ctrl+Tab arrive here
     to be dealt with. Switching them off in the host is no worse — a reload
     that never happens is exactly the point — but Ctrl+Tab would then never
     reach this file, and Ctrl+1..4 stays the way to change tabs either way.
   - Route external links instead of navigating them. A link that must leave
     the app is marked data-external and never reaches the app window: this
     file calls window.openExternal(url) when the host has bound such a
     function, otherwise it posts
       {"kind":"open-external","url":"https://…"}
     through window.chrome.webview.postMessage. With neither in place it falls
     back to a separate window and then to the clipboard, so the address is
     never a dead end — but the app window is never navigated away from the UI
     either way. Handling NewWindowRequested in the host closes the last gap
     (a middle-click, which no page script can intercept). */

(function () {
  'use strict';

  var TABS = ['link', 'files', 'ports', 'settings'];

  function $(id) { return document.getElementById(id); }

  /* ---------------- what counts as text ---------------- */

  // True for the places where the platform's own editing menu and shortcuts
  // must survive: the input fields and anything editable.
  function isEditable(node) {
    var n = node;
    while (n && n.nodeType === 1) {
      var tag = n.tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') { return true; }
      if (n.isContentEditable) { return true; }
      n = n.parentNode;
    }
    return false;
  }

  /* ---------------- 1. the context menu ---------------- */

  // Reload, Save as, Print, Inspect — one right-click on a button and the
  // window admits it is a web page. The host has that menu switched off
  // entirely, so preventDefault here is belt and braces rather than the point:
  // what matters is that the fields still need cut/copy/paste, and menu.js
  // draws it. It answers for where a menu is carried — an editable field, or
  // selected text to copy — and leaves the window's furniture bare.
  function initContextMenu() {
    document.addEventListener('contextmenu', function (ev) {
      ev.preventDefault();
      var menu = window.LM && window.LM.editMenu;
      if (menu) { menu.open(ev); }
    });
  }

  /* ---------------- 2. dropping a file anywhere else ---------------- */

  // files.js sends file drops anywhere in the window. This guard only stops
  // WebView2 from navigating to a dropped file or unsupported text payload.
  function inDropZone(node) {
    var zone = $('drop');
    return !!(zone && node && zone.contains(node));
  }

  function initDragGuard() {
    ['dragenter', 'dragover'].forEach(function (type) {
      window.addEventListener(type, function (ev) {
        if (inDropZone(ev.target)) { return; }
        ev.preventDefault();
        if (ev.dataTransfer) {
          var types = Array.prototype.slice.call(ev.dataTransfer.types || []);
          ev.dataTransfer.dropEffect = types.indexOf('Files') >= 0 ? 'copy' : 'none';
        }
      });
    });

    window.addEventListener('drop', function (ev) {
      if (inDropZone(ev.target)) { return; }
      ev.preventDefault();
    });
  }

  /* ---------------- 3. external links ---------------- */

  var COPIED = 'Ссылка скопирована в буфер обмена — откройте её в браузере.';

  function anchorFor(node) {
    var n = node;
    while (n && n.nodeType === 1) {
      if (n.tagName === 'A' && n.getAttribute('href')) { return n; }
      n = n.parentNode;
    }
    return null;
  }

  // Only what genuinely leaves this window. A relative or in-page href is the
  // UI's own business and is left alone.
  function externalURL(a) {
    var raw = a.getAttribute('href') || '';
    if (!/^[a-z][a-z0-9+.-]*:/i.test(raw)) { return ''; }
    if (/^(javascript|data|blob):/i.test(raw)) { return ''; }
    return a.href;
  }

  function toClipboard(text) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text);
        return true;
      }
    } catch (e) { /* the host may withhold the clipboard; fall through */ }
    return false;
  }

  // Says what happened next to the link itself, in the box the link lives in.
  function noteBeside(a, text) {
    var box = a.parentNode;
    if (!box) { return; }
    var note = box.querySelector('.lm-route-note');
    if (!note) {
      note = document.createElement('span');
      note.className = 'hint lm-route-note';
      box.appendChild(document.createElement('br'));
      box.appendChild(note);
    }
    note.textContent = text;
  }

  function route(url, a) {
    if (typeof window.openExternal === 'function') {
      try { window.openExternal(url); return; } catch (e) { /* try the next way */ }
    }

    var wv = window.chrome && window.chrome.webview;
    if (wv && typeof wv.postMessage === 'function') {
      try {
        wv.postMessage(JSON.stringify({ kind: 'open-external', url: url }));
        return;
      } catch (e) { /* try the next way */ }
    }

    // A separate window at worst: whatever happens to it, this one keeps the UI.
    var opened = null;
    try { opened = window.open(url, '_blank', 'noopener'); } catch (e) { opened = null; }
    if (opened) { return; }

    if (a && toClipboard(url)) { noteBeside(a, COPIED); }
  }

  function initLinks() {
    document.addEventListener('click', function (ev) {
      if (ev.defaultPrevented || ev.button !== 0) { return; }
      var a = anchorFor(ev.target);
      if (!a) { return; }
      var url = externalURL(a);
      if (!url) { return; }
      ev.preventDefault();
      route(url, a);
    });
  }

  /* ---------------- 4. the keyboard a desktop window has ---------------- */

  function tabIndexNow() {
    for (var i = 0; i < TABS.length; i += 1) {
      var btn = $('tab-' + TABS[i]);
      if (btn && btn.getAttribute('aria-selected') === 'true') { return i; }
    }
    return 0;
  }

  // Clicking the tab button reuses the one code path that switches tabs, so
  // the hash, the aria state and the per-tab polling all stay in step.
  function goToTab(i) {
    var btn = $('tab-' + TABS[(i + TABS.length) % TABS.length]);
    if (btn) { btn.click(); }
  }

  // F5 in a document means "fetch it again". Here the document IS the state,
  // so the honest reading of F5 is the one the window already offers: check
  // the link again, on the tab that shows the answer.
  function reCheck() {
    goToTab(0);
    var btn = $('check-btn');
    if (btn && !btn.disabled) { btn.click(); }
  }

  // Esc, the desktop's "never mind": it takes down the answer panels the user
  // has finished reading, then gives up the text field. The offline banner is
  // not one of them — it is live status, not something to dismiss.
  function onEscape(ev) {
    var open = document.querySelectorAll('.msg:not([hidden])');
    if (open.length) {
      ev.preventDefault();
      Array.prototype.forEach.call(open, function (n) { n.hidden = true; });
      return;
    }
    var active = document.activeElement;
    if (active && active.blur && isEditable(active)) {
      ev.preventDefault();
      active.blur();
    }
  }

  // Digits only: reading ev.code as well keeps Ctrl+1..4 working on a layout
  // where ev.key is not the digit itself.
  function digit(ev) {
    if (ev.key >= '1' && ev.key <= '4' && ev.key.length === 1) { return Number(ev.key); }
    var m = /^(?:Digit|Numpad)([1-4])$/.exec(ev.code || '');
    return m ? Number(m[1]) : 0;
  }

  // Nothing below collides with editing text: no Ctrl+A/C/X/V/Z, no arrows,
  // no Home/End. Zoom is left alone on purpose — it is an accessibility
  // control, not a browser artefact.
  function onKeyDown(ev) {
    if (ev.altKey) { return; }
    var ctrl = ev.ctrlKey || ev.metaKey;
    var letter = ev.key && ev.key.length === 1 ? ev.key.toLowerCase() : '';

    if (ev.key === 'Escape') { onEscape(ev); return; }

    if (!ctrl && ev.key !== 'F5') { return; }

    // A reload would throw away everything this window is holding.
    if (ev.key === 'F5' || (ctrl && letter === 'r')) {
      ev.preventDefault();
      // Mid-sentence in a field, the key is refused and nothing else happens:
      // a tab switch would pull the ground out from under what is being typed.
      if (!isEditable(document.activeElement)) { reCheck(); }
      return;
    }

    // Print, save-as and view-source are the browser showing through.
    if (ctrl && (letter === 'p' || letter === 's' || letter === 'u')) {
      ev.preventDefault();
      return;
    }

    if (ctrl && ev.key === 'Tab') {
      ev.preventDefault();
      goToTab(tabIndexNow() + (ev.shiftKey ? -1 : 1));
      return;
    }

    var n = ctrl && !ev.shiftKey ? digit(ev) : 0;
    if (n) {
      ev.preventDefault();
      goToTab(n - 1);
    }
  }

  function initKeyboard() {
    // Capture: a field that stops a key from bubbling must not be able to let
    // a reload through behind it.
    document.addEventListener('keydown', onKeyDown, true);
  }

  /* ---------------- boot ---------------- */

  initContextMenu();
  initDragGuard();
  initLinks();
  initKeyboard();

  if (window.LM) {
    // Published so a tab module can hand a URL out of the window the same way
    // a click on it would.
    window.LM.openExternal = function (url) { route(url, null); };
  }
}());
