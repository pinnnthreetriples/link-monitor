/* Link Monitor — the client for /api: one request function, the offline banner
   it raises when the app stops answering, and the .msg box that shows what came
   back.

   Every sentence this file displays was worded by the server. The two it owns
   are about the page's own inability to reach it. */

(function () {
  'use strict';

  var LM = window.LM;
  var $ = LM.$;
  var el = LM.el;
  var clear = LM.clear;
  var setTone = LM.setTone;

  var OFFLINE_TEXT = 'Приложение не отвечает — проверьте, запущено ли оно. ' +
    'Всё, что показано ниже, могло устареть.';

  function ApiError(message) {
    this.name = 'ApiError';
    this.message = message;
  }
  ApiError.prototype = Object.create(Error.prototype);

  var offline = false;

  function setOffline(state, text) {
    if (state === offline) { return; }
    offline = state;
    var banner = $('offline-banner');
    if (!banner) { return; }
    banner.hidden = !state;
    if (state) { $('offline-text').textContent = text || OFFLINE_TEXT; }
  }

  function api(method, path, body) {
    var opts = { method: method, headers: {} };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    return fetch(path, opts).then(function (res) {
      setOffline(false);
      return res.text().then(function (text) {
        var data = null;
        if (text) {
          try { data = JSON.parse(text); } catch (e) { data = null; }
        }
        if (!res.ok) {
          throw new ApiError(
            data && data.message
              ? data.message
              : 'Приложение ответило ошибкой ' + res.status + '.'
          );
        }
        return data;
      });
    }, function () {
      setOffline(true);
      throw new ApiError('Приложение не отвечает — запрос не дошёл.');
    });
  }

  function errText(err) {
    return err && err.message ? err.message : 'Не удалось выполнить запрос.';
  }

  // Fills one of the .msg boxes with a result the server worded itself.
  function showMsg(node, text, toneName) {
    if (!node) { return; }
    clear(node);
    setTone(node, toneName || 'off');
    node.appendChild(el('span', null, text));
    node.hidden = false;
  }

  LM.api = api;
  LM.errText = errText;
  LM.showMsg = showMsg;
  // The live stream in app.js clears and raises the same banner this file owns.
  LM.setOffline = setOffline;
}());
