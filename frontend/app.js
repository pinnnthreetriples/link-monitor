/* Link Monitor — the shell: the event bus, the theme, the tabs, the state every
   tab shares, and the live status stream that feeds it. The helpers live in
   helpers.js and the API client in api.js; the tabs themselves live in link.js,
   files.js, ports.js and settings.js and use what is published on window.LM.

   Nothing here invents data. Every sentence about the link comes from the
   server; the only Russian written in the frontend is the page's own furniture
   (tab names, buttons, headings, and the words for the state enums). */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var api = LM.api;
  var setOffline = LM.setOffline;

  /* ---------------- event bus ---------------- */

  var handlers = {};

  function on(name, fn) {
    (handlers[name] = handlers[name] || []).push(fn);
  }

  function emit(name, data) {
    (handlers[name] || []).forEach(function (fn) {
      try { fn(data); } catch (e) { /* one bad listener must not stop the rest */ }
    });
  }

  /* ---------------- theme ---------------- */

  var THEME_KEY = 'lm.theme';

  function readTheme() {
    try { return localStorage.getItem(THEME_KEY); } catch (e) { return null; }
  }

  function writeTheme(value) {
    try { localStorage.setItem(THEME_KEY, value); }
    catch (e) { /* private mode: the choice simply does not survive a reload */ }
  }

  function systemDark() {
    return !!(window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
  }

  function isDark() {
    var pinned = document.documentElement.getAttribute('data-theme');
    if (pinned === 'dark') { return true; }
    if (pinned === 'light') { return false; }
    return systemDark();
  }

  function paintThemeButton() {
    var dark = isDark();
    // Same as the prototype: the sun shows while dark is on, the moon while light is.
    $('theme-ico-dark').hidden = !dark;
    $('theme-ico-light').hidden = dark;
    $('theme-btn').setAttribute('aria-label', dark ? 'Включить светлую тему' : 'Включить тёмную тему');
  }

  function initTheme() {
    var saved = readTheme();
    if (saved === 'dark' || saved === 'light') {
      document.documentElement.setAttribute('data-theme', saved);
    }
    paintThemeButton();

    $('theme-btn').addEventListener('click', function () {
      var next = isDark() ? 'light' : 'dark';
      document.documentElement.setAttribute('data-theme', next);
      writeTheme(next);
      paintThemeButton();
    });

    if (window.matchMedia) {
      var mq = window.matchMedia('(prefers-color-scheme: dark)');
      var onChange = function () { paintThemeButton(); };
      if (mq.addEventListener) { mq.addEventListener('change', onChange); }
      else if (mq.addListener) { mq.addListener(onChange); }
    }
  }

  /* ---------------- tabs ---------------- */

  var TABS = ['link', 'files', 'ports', 'settings'];

  function selectTab(name) {
    try {
      if (location.hash.slice(1) !== name) {
        history.replaceState(null, '', '#' + name);
      }
    } catch (e) { /* file:// and friends may refuse; the tab still switches */ }

    TABS.forEach(function (t) {
      var btn = $('tab-' + t);
      var panel = $('panel-' + t);
      var active = t === name;
      btn.setAttribute('aria-selected', active ? 'true' : 'false');
      btn.tabIndex = active ? 0 : -1;
      panel.hidden = !active;
    });
    emit('tab', name);
  }

  function initTabs() {
    TABS.forEach(function (t, i) {
      var btn = $('tab-' + t);
      btn.addEventListener('click', function () { selectTab(t); });
      btn.addEventListener('keydown', function (ev) {
        var next = -1;
        if (ev.key === 'ArrowRight') { next = (i + 1) % TABS.length; }
        else if (ev.key === 'ArrowLeft') { next = (i - 1 + TABS.length) % TABS.length; }
        else if (ev.key === 'Home') { next = 0; }
        else if (ev.key === 'End') { next = TABS.length - 1; }
        if (next < 0) { return; }
        ev.preventDefault();
        selectTab(TABS[next]);
        $('tab-' + TABS[next]).focus();
      });
    });

    var wanted = '';
    try { wanted = (location.hash || '').slice(1); } catch (e) { wanted = ''; }
    selectTab(TABS.indexOf(wanted) >= 0 ? wanted : 'link');
  }

  /* ---------------- shared state ---------------- */

  var state = {
    status: null,
    peers: [],
    // The tailnet name of the machine this install watches, as /api/peers
    // reports it. Empty until that call succeeds, and empty against a build
    // old enough not to send it — never filled in by guesswork.
    configuredPeer: '',
    checking: false
  };

  var lastStatusAt = 0;

  function applyStatus(status) {
    if (!status) { return; }
    state.status = status;
    state.checking = status.checking === true;
    lastStatusAt = Date.now();
    emit('status', status);
  }

  function refreshStatus() {
    return api('GET', '/api/status').then(applyStatus, function () { /* banner shown */ });
  }

  function loadPeers() {
    return api('GET', '/api/peers').then(function (data) {
      state.peers = (data && data.peers) || [];
      // A response without the field leaves the last known name alone rather
      // than blanking it: a reload is not news about the configuration.
      if (data && typeof data.configuredPeer === 'string') {
        state.configuredPeer = data.configuredPeer;
      }
      emit('peers', state.peers);
      return state.peers;
    }, function () {
      emit('peers', state.peers);
      return state.peers;
    });
  }

  // Which node is "the second machine" is a configuration fact the app states
  // in configuredPeer; it is never derived from the list. A tailnet may hold a
  // phone, a tablet and three laptops, and the order Tailscale lists them in
  // says nothing about which one the user cares about.
  function isConfiguredPeer(p) {
    var want = state.configuredPeer;
    if (!p || !want) { return false; }
    // MagicDNS names are case-insensitive. The address is compared as well
    // because a peer may be configured by address instead of by name.
    return (!!p.name && p.name.toLowerCase() === want.toLowerCase()) || p.addr === want;
  }

  // configuredPeerNode returns that node, or null when it is not in the list —
  // which is a state worth showing, not a reason to fall back to another node.
  function configuredPeerNode(peers) {
    var found = null;
    (peers || state.peers || []).forEach(function (p) {
      if (!found && isConfiguredPeer(p)) { found = p; }
    });
    return found;
  }

  /* ---------------- live stream, with polling as the fallback ---------------- */

  var es = null;
  var pollTimer = null;
  var reconnectTimer = null;
  var openedAt = 0;
  var backoff = 1000;
  var BACKOFF_MAX = 30000;
  var POLL_MS = 10000;

  function startPolling() {
    if (pollTimer) { return; }
    // Do not re-ask for a status that arrived a moment ago: a flapping stream
    // would otherwise turn into a request storm.
    if (Date.now() - lastStatusAt > 3000) { refreshStatus(); }
    pollTimer = setInterval(refreshStatus, POLL_MS);
  }

  function stopPolling() {
    if (!pollTimer) { return; }
    clearInterval(pollTimer);
    pollTimer = null;
  }

  function startStream() {
    reconnectTimer = null;
    if (!window.EventSource) { startPolling(); return; }

    try {
      es = new EventSource('/api/events');
    } catch (e) {
      startPolling();
      return;
    }

    es.onopen = function () {
      openedAt = Date.now();
      stopPolling();
      setOffline(false);
    };

    es.onmessage = function (ev) {
      if (!ev || !ev.data) { return; }
      var status;
      try { status = JSON.parse(ev.data); } catch (e) { return; }
      setOffline(false);
      applyStatus(status);
    };

    es.onerror = function () {
      // Take the reconnect into our own hands so the backoff is ours, and keep
      // a 10 s poll running meanwhile so the page never shows stale data mutely.
      if (es) { es.close(); es = null; }
      startPolling();
      if (reconnectTimer) { return; }
      // Only a stream that genuinely worked for a while earns a fast retry;
      // one that opens and dies at once keeps backing off.
      if (openedAt && Date.now() - openedAt > 5000) { backoff = 1000; }
      openedAt = 0;
      reconnectTimer = setTimeout(startStream, backoff);
      backoff = Math.min(backoff * 2, BACKOFF_MAX);
    };
  }

  /* ---------------- boot ---------------- */

  function init() {
    initTheme();
    initTabs();
    emit('ready');

    refreshStatus();
    loadPeers();
    startStream();
  }

  /* ---------------- exports for the tab modules ---------------- */

  LM.on = on;
  LM.emit = emit;
  LM.state = state;
  LM.applyStatus = applyStatus;
  LM.refreshStatus = refreshStatus;
  LM.loadPeers = loadPeers;
  LM.isConfiguredPeer = isConfiguredPeer;
  LM.configuredPeerNode = configuredPeerNode;

  // The tab modules are deferred scripts too, and they register their listeners
  // while readyState is already "interactive" — so init must wait for
  // DOMContentLoaded (which fires after every deferred script has run) rather
  // than firing the moment this file executes.
  if (document.readyState === 'complete') {
    setTimeout(init, 0);
  } else {
    document.addEventListener('DOMContentLoaded', init);
  }
}());
